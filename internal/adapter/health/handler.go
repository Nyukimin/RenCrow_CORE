package health

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	domainhealth "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/health"
)

// CheckRunner はヘルスチェック実行のインタフェース
type CheckRunner interface {
	RunChecks(ctx context.Context) domainhealth.HealthReport
	IsReady(ctx context.Context) bool
}

// Handler はヘルスチェック HTTP ハンドラ
type Handler struct {
	service CheckRunner
	timeout time.Duration
}

// NewHandler は新しい Handler を作成
func NewHandler(service CheckRunner) *Handler {
	return NewHandlerWithTimeout(service, 3*time.Second)
}

// NewHandlerWithTimeout は request 単位の timeout を指定して Handler を作成する
func NewHandlerWithTimeout(service CheckRunner, timeout time.Duration) *Handler {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	return &Handler{service: service, timeout: timeout}
}

// HandleHealth は /health エンドポイント（全チェック結果を返す）
func (h *Handler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()

	report := h.service.RunChecks(ctx)

	// Duration を milliseconds に変換した出力用構造体
	type checkJSON struct {
		Name       string  `json:"name"`
		Status     string  `json:"status"`
		Message    string  `json:"message,omitempty"`
		DurationMs float64 `json:"duration_ms"`
	}
	type reportJSON struct {
		Status    string      `json:"status"`
		Checks    []checkJSON `json:"checks"`
		Timestamp string      `json:"timestamp"`
	}

	out := reportJSON{
		Status:    string(report.Status),
		Timestamp: report.Timestamp.Format(time.RFC3339),
	}
	for _, c := range report.Checks {
		out.Checks = append(out.Checks, checkJSON{
			Name:       c.Name,
			Status:     string(c.Status),
			Message:    c.Message,
			DurationMs: float64(c.Duration.Milliseconds()),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	if report.Status == domainhealth.StatusDown {
		w.WriteHeader(http.StatusServiceUnavailable)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	json.NewEncoder(w).Encode(out)
}

// HandleReady は /ready エンドポイント（200 or 503）
func (h *Handler) HandleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()

	if h.service.IsReady(ctx) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ready":true}`))
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"ready":false}`))
	}
}
