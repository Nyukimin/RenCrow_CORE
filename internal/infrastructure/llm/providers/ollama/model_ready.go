package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

func (p *OllamaProvider) ensureModelReady(ctx context.Context, model string) error {
	// TTL キャッシュ: 30秒以内に ready 確認済みならプリフライトをスキップ
	p.readyCacheMu.Lock()
	if t, ok := p.readyCache[model]; ok && time.Since(t) < preflightTTL {
		p.readyCacheMu.Unlock()
		return nil
	}
	p.readyCacheMu.Unlock()

	log.Printf("[OllamaProvider] preflight start model=%s num_ctx=%d", model, p.numCtx)
	loaded, err := p.isModelLoaded(ctx, model)
	if err != nil {
		return fmt.Errorf("ollama model health check failed for %s: %w", model, err)
	}
	if loaded {
		log.Printf("[OllamaProvider] preflight ready model=%s source=resident", model)
		p.readyCacheMu.Lock()
		p.readyCache[model] = time.Now()
		p.readyCacheMu.Unlock()
		return nil
	}
	log.Printf("[OllamaProvider] preflight warmup model=%s", model)
	if err := p.warmModel(ctx, model); err != nil {
		return fmt.Errorf("ollama model warmup failed for %s: %w", model, err)
	}
	log.Printf("[OllamaProvider] preflight ready model=%s source=warmup", model)
	p.readyCacheMu.Lock()
	p.readyCache[model] = time.Now()
	p.readyCacheMu.Unlock()
	return nil
}

func (p *OllamaProvider) isModelLoaded(ctx context.Context, model string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", p.baseURL+"/api/ps", nil)
	if err != nil {
		return false, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("status=%d body=%s", resp.StatusCode, string(body))
	}
	var psResp ollamaPsResponse
	if err := json.NewDecoder(resp.Body).Decode(&psResp); err != nil {
		return false, err
	}
	for _, m := range psResp.Models {
		if strings.TrimSpace(m.Name) == strings.TrimSpace(model) {
			return true, nil
		}
	}
	return false, nil
}

func (p *OllamaProvider) warmModel(ctx context.Context, model string) error {
	options := map[string]interface{}{
		"temperature": 0,
		"num_predict": 0,
		"stop":        []string{},
	}
	if p.numCtx > 0 {
		options["num_ctx"] = p.numCtx
	}
	body, err := json.Marshal(map[string]interface{}{
		"model":      model,
		"prompt":     "",
		"stream":     false,
		"keep_alive": -1,
		"options":    options,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status=%d body=%s", resp.StatusCode, string(respBody))
	}
	return nil
}
