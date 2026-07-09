package tts

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

func shouldRetrySynthesis(code string, attempt int) bool {
	return moduletts.ShouldRetrySynthesis(code, attempt)
}

func backoffForAttempt(attempt int) time.Duration {
	return moduletts.SynthesisBackoffForAttempt(attempt)
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (b *RenCrowTTSBridge) postSynthesisWithRetry(ctx context.Context, reqBody []byte, sessionID string, chunkIndex int) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, normalizeSynthesisURL(b.cfg.HTTPBaseURL), bytes.NewReader(reqBody))
		if err != nil {
			return nil, fmt.Errorf("build /synthesis request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-RenCrow-TTS-Request-Id", buildRequestIDHeader(sessionID, chunkIndex))

		resp, err := b.client.Do(req)
		if err != nil {
			if shouldRetryTransportError(err, attempt) {
				if sleepErr := sleepWithContext(ctx, backoffForAttempt(attempt)); sleepErr != nil {
					return nil, fmt.Errorf("/synthesis retry cancelled: %w", sleepErr)
				}
				continue
			}
			return nil, fmt.Errorf("/synthesis request failed: %w", err)
		}

		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read /synthesis response: %w", readErr)
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return body, nil
		}

		code, message := parseSynthesisError(body)
		if code == "" {
			return nil, fmt.Errorf("/synthesis bad status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		if shouldRetrySynthesis(code, attempt) {
			if err := sleepWithContext(ctx, backoffForAttempt(attempt)); err != nil {
				return nil, fmt.Errorf("/synthesis retry cancelled: %w", err)
			}
			continue
		}
		return nil, fmt.Errorf("/synthesis failed status=%d code=%s message=%s", resp.StatusCode, code, message)
	}
}

func shouldRetryTransportError(err error, attempt int) bool {
	if err == nil {
		return false
	}
	return moduletts.ShouldRetrySynthesisTransportError(err.Error(), attempt)
}
