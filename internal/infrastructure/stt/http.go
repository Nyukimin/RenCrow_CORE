package stt

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"time"

	modulestt "github.com/Nyukimin/picoclaw_multiLLM/modules/stt"
)

type Handler struct {
	Provider Provider
	Now      func() time.Time
}

func NewHandler(provider Provider) *Handler {
	return &Handler{Provider: provider, Now: time.Now}
}

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	provider := h.Provider
	if provider == nil {
		writeJSON(w, http.StatusServiceUnavailable, Health{Status: "error", Ready: false})
		return
	}
	writeJSON(w, http.StatusOK, provider.Health(r.Context()))
}

func (h *Handler) File(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	result, status := h.transcribeMultipart(r.Context(), r)
	writeJSON(w, status, result)
}

func (h *Handler) ChatInput(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	result, status := h.transcribeMultipart(r.Context(), r)
	if status >= 400 {
		writeJSON(w, status, result)
		return
	}
	writeJSON(w, http.StatusOK, modulestt.BuildChatInputEnvelope(modulestt.ChatInputEnvelopeInput{
		Provider:  result.Provider,
		Text:      result.Text,
		EventID:   result.EventID,
		ErrorCode: result.ErrorCode,
	}))
}

func (h *Handler) transcribeMultipart(ctx context.Context, r *http.Request) (Result, int) {
	provider := h.Provider
	if provider == nil {
		return Result{ErrorCode: ErrorProviderFailure, Message: "stt provider is not configured"}, http.StatusServiceUnavailable
	}
	wav, err := readMultipartWAV(r)
	if err != nil {
		return Result{ErrorCode: ErrorInvalidAudio, Message: "音声ファイル形式が不正です。"}, http.StatusBadRequest
	}
	started := time.Now()
	result, err := provider.Transcribe(ctx, wav)
	result.ProcessingMS = time.Since(started).Milliseconds()
	result.Provider = provider.Name()
	if result.EventID == "" {
		now := time.Now
		if h.Now != nil {
			now = h.Now
		}
		result.EventID = NextEventID(now())
	}
	if err != nil {
		var sttErr *Error
		if errors.As(err, &sttErr) {
			result.ErrorCode = sttErr.Code
			result.Message = sttErr.Message
			return result, modulestt.StatusForHandlerError(sttErr.Code)
		}
		result.ErrorCode = ErrorProviderFailure
		result.Message = err.Error()
		return result, http.StatusBadGateway
	}
	decision := modulestt.NormalizeHandlerResult(modulestt.HandlerResultInput{
		Text:      result.Text,
		Language:  result.Language,
		ErrorCode: result.ErrorCode,
		Message:   result.Message,
	})
	result.Language = decision.Language
	result.ErrorCode = decision.ErrorCode
	result.Message = decision.Message
	return result, http.StatusOK
}

func readMultipartWAV(r *http.Request) ([]byte, error) {
	if err := r.ParseMultipartForm(64 << 20); err != nil && err != multipart.ErrMessageTooLarge {
		return nil, err
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	wav, err := io.ReadAll(io.LimitReader(file, 64<<20))
	if err != nil {
		return nil, err
	}
	if !IsWAV(wav) {
		return nil, NewError(ErrorInvalidAudio, "invalid wav", nil)
	}
	return wav, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
