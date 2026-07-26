package line

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const assistantNotificationBodyLimit = 16 * 1024

// AssistantNotificationRequest is the correlation-preserving LINE push
// contract accepted from RenCrow_ASSISTANT.
type AssistantNotificationRequest struct {
	DeliveryID string `json:"delivery_id"`
	TraceID    string `json:"trace_id"`
	UserID     string `json:"user_id"`
	Title      string `json:"title"`
	Body       string `json:"body"`
}

// AssistantNotificationResponse reports the low-level transport result without
// exposing the LINE target.
type AssistantNotificationResponse struct {
	Status     string `json:"status"`
	DeliveryID string `json:"delivery_id"`
	TraceID    string `json:"trace_id"`
	Duplicate  bool   `json:"duplicate"`
	TargetType string `json:"target_type"`
}

// AssistantPushAdapter is the existing outbound channel capability used by the
// ASSISTANT transport boundary.
type AssistantPushAdapter interface {
	Probe(ctx context.Context) error
	Send(ctx context.Context, target, message string) error
}

// AssistantTargetResolver resolves the current configured LINE target on each
// request so first-DM enrollment does not require a CORE restart.
type AssistantTargetResolver func() (target string, targetType string, err error)

// AssistantNotificationHandler is CORE's localhost transport boundary for
// ASSISTANT-owned notifications. It reuses the configured LINE adapter.
type AssistantNotificationHandler struct {
	adapter       AssistantPushAdapter
	resolveTarget AssistantTargetResolver
	receipts      *AssistantPushReceiptStore
	now           func() time.Time
}

// NewAssistantNotificationHandler creates the localhost LINE transport
// boundary. Network exposure is restricted by the composition root.
func NewAssistantNotificationHandler(
	adapter AssistantPushAdapter,
	resolveTarget AssistantTargetResolver,
	receipts *AssistantPushReceiptStore,
	now func() time.Time,
) *AssistantNotificationHandler {
	if now == nil {
		now = time.Now
	}
	return &AssistantNotificationHandler{
		adapter:       adapter,
		resolveTarget: resolveTarget,
		receipts:      receipts,
		now:           now,
	}
}

func (h *AssistantNotificationHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h == nil || h.adapter == nil || h.resolveTarget == nil || h.receipts == nil {
		http.Error(w, "LINE notification transport unavailable", http.StatusServiceUnavailable)
		return
	}

	request, err := decodeAssistantNotificationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.adapter.Probe(r.Context()); err != nil {
		http.Error(w, "LINE notification transport unavailable", http.StatusServiceUnavailable)
		return
	}
	target, targetType, err := h.resolveTarget()
	if err != nil || strings.TrimSpace(target) == "" {
		http.Error(w, "LINE notification target unavailable", http.StatusServiceUnavailable)
		return
	}
	if _, err := TargetKind(target); err != nil {
		http.Error(w, "LINE notification target invalid", http.StatusServiceUnavailable)
		return
	}

	message := "【" + request.Title + "】\n" + request.Body
	log.Printf(
		"[AssistantNotification] LINE push attempt delivery_id=%s trace_id=%s target=%s type=%s",
		request.DeliveryID,
		request.TraceID,
		MaskTargetID(target),
		targetType,
	)
	duplicate, err := h.receipts.SendOnce(
		r.Context(),
		request.DeliveryID,
		request.TraceID,
		h.now(),
		func(ctx context.Context) error {
			return h.adapter.Send(ctx, target, message)
		},
	)
	if errors.Is(err, ErrAssistantPushUncertain) {
		http.Error(w, "LINE notification delivery state is uncertain", http.StatusConflict)
		return
	}
	if err != nil {
		log.Printf(
			"[AssistantNotification] LINE push uncertain delivery_id=%s trace_id=%s: %v",
			request.DeliveryID,
			request.TraceID,
			err,
		)
		http.Error(w, "LINE notification send failed", http.StatusBadGateway)
		return
	}

	writeAssistantNotificationJSON(w, AssistantNotificationResponse{
		Status:     "sent",
		DeliveryID: request.DeliveryID,
		TraceID:    request.TraceID,
		Duplicate:  duplicate,
		TargetType: targetType,
	})
}

func decodeAssistantNotificationRequest(r *http.Request) (AssistantNotificationRequest, error) {
	var request AssistantNotificationRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, assistantNotificationBodyLimit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, fmt.Errorf("invalid JSON request")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return request, fmt.Errorf("request must contain one JSON object")
	}
	request.DeliveryID = strings.TrimSpace(request.DeliveryID)
	request.TraceID = strings.TrimSpace(request.TraceID)
	request.UserID = strings.TrimSpace(request.UserID)
	request.Title = strings.TrimSpace(request.Title)
	request.Body = strings.TrimSpace(request.Body)
	for name, value := range map[string]string{
		"delivery_id": request.DeliveryID,
		"trace_id":    request.TraceID,
		"user_id":     request.UserID,
		"title":       request.Title,
		"body":        request.Body,
	} {
		if value == "" {
			return request, fmt.Errorf("%s is required", name)
		}
	}
	for name, value := range map[string]string{
		"delivery_id": request.DeliveryID,
		"trace_id":    request.TraceID,
		"user_id":     request.UserID,
	} {
		if len(value) > 128 || strings.ContainsAny(value, "\r\n\t") {
			return request, fmt.Errorf("%s is invalid", name)
		}
	}
	if utf8.RuneCountInString(request.Title) > 20 {
		return request, fmt.Errorf("title must be 20 characters or fewer")
	}
	if utf8.RuneCountInString(request.Body) > 100 {
		return request, fmt.Errorf("body must be 100 characters or fewer")
	}
	return request, nil
}

func writeAssistantNotificationJSON(w http.ResponseWriter, response AssistantNotificationResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}
