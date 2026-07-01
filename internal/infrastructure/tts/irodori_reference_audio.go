package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	moduletts "github.com/Nyukimin/picoclaw_multiLLM/modules/tts"
)

func (p *IrodoriProvider) referenceAudioFileData(ctx context.Context) (any, error) {
	if strings.TrimSpace(p.cfg.ReferenceAudio) == "" && strings.TrimSpace(p.cfg.ReferenceAudioURL) == "" {
		return nil, nil
	}
	uploadedPath, err := p.uploadReferenceAudio(ctx)
	if err == nil && strings.TrimSpace(uploadedPath) != "" {
		return irodoriUploadedAudio(uploadedPath), nil
	}
	if strings.TrimSpace(p.cfg.ReferenceAudioURL) != "" {
		return nil, err
	}
	return irodoriUploadedAudio(p.cfg.ReferenceAudio), nil
}

func (p *IrodoriProvider) uploadReferenceAudio(ctx context.Context) (string, error) {
	p.refMu.Lock()
	defer p.refMu.Unlock()
	if strings.TrimSpace(p.refPath) != "" {
		return p.refPath, nil
	}
	var (
		r        io.ReadCloser
		fileName string
		err      error
	)
	if rawURL := strings.TrimSpace(p.cfg.ReferenceAudioURL); rawURL != "" {
		r, fileName, err = p.openReferenceAudioURL(ctx, rawURL)
	} else {
		r, fileName, err = openReferenceAudioFile(p.cfg.ReferenceAudio)
	}
	if err != nil {
		return "", err
	}
	defer r.Close()
	uploadedPath, err := p.uploadFile(ctx, r, fileName)
	if err != nil {
		return "", err
	}
	p.refPath = uploadedPath
	return uploadedPath, nil
}

func (p *IrodoriProvider) openReferenceAudioURL(ctx context.Context, rawURL string) (io.ReadCloser, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("build irodori reference audio request: %w", err)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download irodori reference audio failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		return nil, "", fmt.Errorf("irodori reference audio bad status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	name := "reference.wav"
	if u, err := url.Parse(rawURL); err == nil {
		if base := filepath.Base(u.Path); base != "." && base != "/" && strings.TrimSpace(base) != "" {
			name = base
		}
	}
	return resp.Body, name, nil
}

func openReferenceAudioFile(referenceAudio string) (io.ReadCloser, string, error) {
	path := strings.TrimSpace(referenceAudio)
	if path == "" {
		return nil, "", fmt.Errorf("irodori reference_audio is empty")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, "", fmt.Errorf("open irodori reference audio: %w", err)
	}
	name := filepath.Base(path)
	if name == "." || strings.TrimSpace(name) == "" {
		name = "reference.wav"
	}
	return f, name, nil
}

func (p *IrodoriProvider) uploadFile(ctx context.Context, r io.Reader, fileName string) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("files", fileName)
	if err != nil {
		return "", fmt.Errorf("create irodori upload form: %w", err)
	}
	if _, err := io.Copy(part, r); err != nil {
		return "", fmt.Errorf("write irodori upload form: %w", err)
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("close irodori upload form: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.baseURL, "/")+"/gradio_api/upload", &body)
	if err != nil {
		return "", fmt.Errorf("build irodori upload request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("irodori upload failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("irodori upload bad status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var paths []string
	if err := json.NewDecoder(resp.Body).Decode(&paths); err != nil {
		return "", fmt.Errorf("decode irodori upload response: %w", err)
	}
	if len(paths) == 0 || strings.TrimSpace(paths[0]) == "" {
		return "", fmt.Errorf("irodori upload response has no file path")
	}
	return paths[0], nil
}

func irodoriUploadedAudio(referenceAudio string) any {
	return moduletts.BuildIrodoriUploadedAudioFileData(referenceAudio)
}
