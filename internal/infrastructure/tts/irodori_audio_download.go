package tts

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

func (p *IrodoriProvider) downloadAudio(ctx context.Context, rawURL string) (*http.Response, error) {
	audioURL, err := moduletts.ResolveIrodoriAudioDownloadURL(p.baseURL, rawURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, audioURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build irodori audio request: %w", err)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download irodori audio failed: %w", err)
	}
	return resp, nil
}

func parseIrodoriAudioURL(r io.Reader) (string, error) {
	var raw json.RawMessage
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return "", fmt.Errorf("decode irodori response: %w", err)
	}
	return moduletts.ParseIrodoriAudioURL(raw)
}
