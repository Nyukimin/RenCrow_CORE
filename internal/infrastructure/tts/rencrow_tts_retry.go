package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
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

type synthesisTransportResult struct {
	body       []byte
	requestID  modulecore.RequestID
	responseID modulecore.ResponseID
}

func (b *RenCrowTTSBridge) postSynthesisWithRetry(ctx context.Context, reqBody []byte) (synthesisTransportResult, error) {
	for attempt := 0; ; attempt++ {
		owner, owned := domaintransport.ReceiptOwnerFromContext(ctx)
		requestID := modulecore.NewRequestID()
		if owned {
			requestReceipt, err := owner.BeginRequest(ctx, "tts.synthesis")
			if err != nil {
				return synthesisTransportResult{}, fmt.Errorf("begin TTS transport request: %w", err)
			}
			requestID = requestReceipt.RequestID
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, normalizeSynthesisURL(b.cfg.HTTPBaseURL), bytes.NewReader(reqBody))
		if err != nil {
			if owned {
				if receiptErr := owner.FailWithoutResponse(context.WithoutCancel(ctx), requestID, err.Error()); receiptErr != nil {
					return synthesisTransportResult{}, fmt.Errorf("build /synthesis request: %w; persist transport failure: %v", err, receiptErr)
				}
			}
			return synthesisTransportResult{}, fmt.Errorf("build /synthesis request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-RenCrow-TTS-Request-Id", string(requestID))
		if token := strings.TrimSpace(b.cfg.AuthToken); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		resp, err := b.client.Do(req)
		if err != nil {
			if owned {
				if receiptErr := owner.FailWithoutResponse(context.WithoutCancel(ctx), requestID, err.Error()); receiptErr != nil {
					return synthesisTransportResult{}, fmt.Errorf("/synthesis request failed: %w; persist transport failure: %v", err, receiptErr)
				}
			}
			if shouldRetryTransportError(err, attempt) {
				if sleepErr := sleepWithContext(ctx, backoffForAttempt(attempt)); sleepErr != nil {
					return synthesisTransportResult{}, fmt.Errorf("/synthesis retry cancelled: %w", sleepErr)
				}
				continue
			}
			return synthesisTransportResult{}, fmt.Errorf("/synthesis request failed: %w", err)
		}

		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		resp.Body.Close()
		externalRef := gatewayResponseRequestID(body)
		responseID := modulecore.NewResponseID()
		if owned {
			responseReceipt, receiptErr := owner.CompleteResponse(context.WithoutCancel(ctx), requestID, externalRef)
			if receiptErr != nil {
				return synthesisTransportResult{}, fmt.Errorf("persist TTS transport response: %w", receiptErr)
			}
			responseID = responseReceipt.ResponseID
		}
		if readErr != nil {
			return synthesisTransportResult{}, fmt.Errorf("read /synthesis response: %w", readErr)
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if owned && externalRef != string(requestID) {
				return synthesisTransportResult{}, fmt.Errorf("TTS transport identity mismatch")
			}
			return synthesisTransportResult{body: body, requestID: requestID, responseID: responseID}, nil
		}

		code, message := parseSynthesisError(body)
		if code == "" {
			return synthesisTransportResult{}, fmt.Errorf("/synthesis bad status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		if shouldRetrySynthesis(code, attempt) {
			if err := sleepWithContext(ctx, backoffForAttempt(attempt)); err != nil {
				return synthesisTransportResult{}, fmt.Errorf("/synthesis retry cancelled: %w", err)
			}
			continue
		}
		return synthesisTransportResult{}, fmt.Errorf("/synthesis failed status=%d code=%s message=%s", resp.StatusCode, code, message)
	}
}

func gatewayResponseRequestID(body []byte) string {
	var response struct {
		RequestID string `json:"request_id"`
	}
	if json.Unmarshal(body, &response) != nil {
		return ""
	}
	return strings.TrimSpace(response.RequestID)
}

func shouldRetryTransportError(err error, attempt int) bool {
	if err == nil {
		return false
	}
	return moduletts.ShouldRetrySynthesisTransportError(err.Error(), attempt)
}
