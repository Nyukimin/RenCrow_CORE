package comfyui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/agent"
)

const (
	DefaultBaseURL      = "http://100.83.207.6:8188"
	DefaultClientID     = "rencrow-server"
	DefaultPollInterval = 3 * time.Second
	DefaultTimeout      = 5 * time.Minute
	MaxPromptRunes      = 1200
	DefaultWidth        = 768
	DefaultHeight       = 768
	MaxDimension        = 1024
)

type Config struct {
	BaseURL      string
	ClientID     string
	PollInterval time.Duration
	Timeout      time.Duration
}

type Client struct {
	cfg    Config
	client *http.Client
}

type GenerateRequest struct {
	Prompt         string
	Seed           int64
	Width          int
	Height         int
	FilenamePrefix string
}

type imageMeta struct {
	Filename  string `json:"filename"`
	Subfolder string `json:"subfolder"`
	Type      string `json:"type"`
}

func NewClient(cfg Config) *Client {
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	cfg.ClientID = strings.TrimSpace(cfg.ClientID)
	if cfg.ClientID == "" {
		cfg.ClientID = DefaultClientID
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	return &Client{
		cfg:    cfg,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) GenerateImage(ctx context.Context, prompt string) (agent.ImageGenerationResult, error) {
	return c.GenerateImageRequest(ctx, GenerateRequest{Prompt: prompt})
}

func (c *Client) GenerateImageRequest(ctx context.Context, req GenerateRequest) (agent.ImageGenerationResult, error) {
	if err := validateGenerateRequest(&req); err != nil {
		return agent.ImageGenerationResult{}, err
	}
	promptID, err := c.queuePrompt(ctx, req)
	if err != nil {
		return agent.ImageGenerationResult{}, err
	}
	meta, err := c.pollImage(ctx, promptID)
	if err != nil {
		return agent.ImageGenerationResult{}, err
	}
	return agent.ImageGenerationResult{
		PromptID: promptID,
		ImageURL: c.viewURL(meta),
		Filename: meta.Filename,
	}, nil
}

func validateGenerateRequest(req *GenerateRequest) error {
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		return fmt.Errorf("comfyui prompt is required")
	}
	if len([]rune(req.Prompt)) > MaxPromptRunes {
		return fmt.Errorf("comfyui prompt too long: %d > %d", len([]rune(req.Prompt)), MaxPromptRunes)
	}
	if req.Width <= 0 {
		req.Width = DefaultWidth
	}
	if req.Height <= 0 {
		req.Height = DefaultHeight
	}
	if req.Width > MaxDimension || req.Height > MaxDimension || req.Width%8 != 0 || req.Height%8 != 0 {
		return fmt.Errorf("comfyui dimensions must be multiples of 8 and <= %d: %dx%d", MaxDimension, req.Width, req.Height)
	}
	if req.Seed <= 0 {
		req.Seed = time.Now().UnixNano() % 2147483647
	}
	req.FilenamePrefix = sanitizeFilenamePrefix(req.FilenamePrefix)
	if req.FilenamePrefix == "" {
		req.FilenamePrefix = "rencrow_zimage"
	}
	return nil
}

func (c *Client) queuePrompt(ctx context.Context, req GenerateRequest) (string, error) {
	body := map[string]any{
		"client_id": c.cfg.ClientID,
		"prompt":    defaultZImageWorkflow(req),
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+"/prompt", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("comfyui /prompt request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("comfyui /prompt bad status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var out struct {
		PromptID   string         `json:"prompt_id"`
		NodeErrors map[string]any `json:"node_errors"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return "", fmt.Errorf("decode comfyui /prompt response: %w", err)
	}
	if strings.TrimSpace(out.PromptID) == "" {
		return "", fmt.Errorf("comfyui /prompt response missing prompt_id")
	}
	if len(out.NodeErrors) > 0 {
		return "", fmt.Errorf("comfyui /prompt node_errors: %+v", out.NodeErrors)
	}
	return out.PromptID, nil
}

func (c *Client) pollImage(ctx context.Context, promptID string) (imageMeta, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	ticker := time.NewTicker(c.cfg.PollInterval)
	defer ticker.Stop()
	for {
		meta, ok, err := c.fetchHistoryImage(ctx, promptID)
		if err != nil {
			return imageMeta{}, err
		}
		if ok {
			return meta, nil
		}
		select {
		case <-ctx.Done():
			return imageMeta{}, fmt.Errorf("comfyui history polling timed out: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (c *Client) fetchHistoryImage(ctx context.Context, promptID string) (imageMeta, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.BaseURL+"/history/"+url.PathEscape(promptID), nil)
	if err != nil {
		return imageMeta{}, false, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return imageMeta{}, false, fmt.Errorf("comfyui /history request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return imageMeta{}, false, fmt.Errorf("comfyui /history bad status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if strings.TrimSpace(string(body)) == "{}" {
		return imageMeta{}, false, nil
	}
	meta, ok := parseHistoryImage(promptID, body)
	return meta, ok, nil
}

func parseHistoryImage(promptID string, body []byte) (imageMeta, bool) {
	var root map[string]struct {
		Outputs map[string]struct {
			Images []imageMeta `json:"images"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal(body, &root); err != nil {
		return imageMeta{}, false
	}
	entry, ok := root[promptID]
	if !ok {
		return imageMeta{}, false
	}
	for _, output := range entry.Outputs {
		if len(output.Images) == 0 {
			continue
		}
		if strings.TrimSpace(output.Images[0].Filename) != "" {
			if output.Images[0].Type == "" {
				output.Images[0].Type = "output"
			}
			return output.Images[0], true
		}
	}
	return imageMeta{}, false
}

func (c *Client) viewURL(meta imageMeta) string {
	v := url.Values{}
	v.Set("filename", meta.Filename)
	if strings.TrimSpace(meta.Subfolder) != "" {
		v.Set("subfolder", meta.Subfolder)
	}
	typ := strings.TrimSpace(meta.Type)
	if typ == "" {
		typ = "output"
	}
	v.Set("type", typ)
	return c.cfg.BaseURL + "/view?" + v.Encode()
}

func sanitizeFilenamePrefix(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range prefix {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('_')
		}
	}
	out := strings.Trim(b.String(), "_-")
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

func defaultZImageWorkflow(req GenerateRequest) map[string]any {
	return map[string]any{
		"1":  node("UNETLoader", map[string]any{"unet_name": "z_image_turbo_nvfp4.safetensors", "weight_dtype": "default"}),
		"2":  node("CLIPLoader", map[string]any{"clip_name": "qwen_3_4b_fp8_mixed.safetensors", "type": "lumina2", "device": "default"}),
		"3":  node("VAELoader", map[string]any{"vae_name": "ae.safetensors"}),
		"11": node("LoraLoader", map[string]any{"model": []any{"1", 0}, "clip": []any{"2", 0}, "lora_name": "AWPortrait-Z.safetensors", "strength_model": 0.8, "strength_clip": 0.8}),
		"12": node("LoraLoader", map[string]any{"model": []any{"11", 0}, "clip": []any{"11", 1}, "lora_name": "REALSTAGRAM_ZIMG.safetensors", "strength_model": 0.3, "strength_clip": 0.3}),
		"4":  node("CLIPTextEncode", map[string]any{"clip": []any{"12", 1}, "text": req.Prompt}),
		"5":  node("ConditioningZeroOut", map[string]any{"conditioning": []any{"4", 0}}),
		"6":  node("ModelSamplingAuraFlow", map[string]any{"model": []any{"12", 0}, "shift": 3.0}),
		"7":  node("EmptySD3LatentImage", map[string]any{"width": req.Width, "height": req.Height, "batch_size": 1}),
		"8":  node("KSampler", map[string]any{"model": []any{"6", 0}, "positive": []any{"4", 0}, "negative": []any{"5", 0}, "latent_image": []any{"7", 0}, "seed": req.Seed, "steps": 8, "cfg": 1.0, "sampler_name": "res_multistep", "scheduler": "simple", "denoise": 1.0}),
		"9":  node("VAEDecode", map[string]any{"samples": []any{"8", 0}, "vae": []any{"3", 0}}),
		"10": node("SaveImage", map[string]any{"images": []any{"9", 0}, "filename_prefix": req.FilenamePrefix + "_" + strconv.FormatInt(req.Seed, 10)}),
	}
}

func node(classType string, inputs map[string]any) map[string]any {
	return map[string]any{
		"class_type": classType,
		"inputs":     inputs,
	}
}
