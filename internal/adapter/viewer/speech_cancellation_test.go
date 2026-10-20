package viewer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
)

func TestSpeechCancellationRegistryCancelsOnlyTheMatchingTurn(t *testing.T) {
	registry := NewSpeechCancellationRegistry()
	cancellationA := orchestrator.NewSpeechTurnCancellation()
	tokenA, cleanupA, err := registry.Register("turn-a", cancellationA)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupA()
	cancellationB := orchestrator.NewSpeechTurnCancellation()
	tokenB, cleanupB, err := registry.Register("turn-b", cancellationB)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupB()
	if tokenA == tokenB {
		t.Fatal("two accepted turns received the same cancellation capability")
	}

	speechCtxA, doneA := orchestrator.SpeechSynthesisContext(orchestrator.WithSpeechTurnCancellation(context.Background(), cancellationA))
	defer doneA()
	speechCtxB, doneB := orchestrator.SpeechSynthesisContext(orchestrator.WithSpeechTurnCancellation(context.Background(), cancellationB))
	defer doneB()

	if !registry.cancelToken(tokenA) {
		t.Fatal("matching capability was not found")
	}
	if !errors.Is(speechCtxA.Err(), context.Canceled) {
		t.Fatalf("matching speech context error=%v want=%v", speechCtxA.Err(), context.Canceled)
	}
	if speechCtxB.Err() != nil {
		t.Fatalf("unrelated speech context was canceled: %v", speechCtxB.Err())
	}
}

func TestSpeechCancellationHandlerIsIdempotentAndDoesNotRevealRegistryState(t *testing.T) {
	registry := NewSpeechCancellationRegistry()
	cancellation := orchestrator.NewSpeechTurnCancellation()
	token, cleanup, err := registry.Register("turn-a", cancellation)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	speechCtx, done := orchestrator.SpeechSynthesisContext(orchestrator.WithSpeechTurnCancellation(context.Background(), cancellation))
	defer done()
	handler := registry.HandleCancel()

	for _, cap := range []string{token, token, "unknown-capability"} {
		body, err := json.Marshal(map[string]string{"speech_cancel_token": cap})
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/viewer/tts/cancel", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusNoContent, rec.Body.String())
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("Cache-Control=%q want=no-store", got)
		}
	}
	if !errors.Is(speechCtx.Err(), context.Canceled) {
		t.Fatalf("speech context error=%v want=%v", speechCtx.Err(), context.Canceled)
	}
}

func TestSpeechCancellationRouteIsolatesCapabilityToItsAcceptedTurn(t *testing.T) {
	registry := NewSpeechCancellationRegistry()
	cancellationA := orchestrator.NewSpeechTurnCancellation()
	tokenA, cleanupA, err := registry.Register("turn-a", cancellationA)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupA()
	cancellationB := orchestrator.NewSpeechTurnCancellation()
	tokenB, cleanupB, err := registry.Register("turn-b", cancellationB)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupB()
	if tokenA == tokenB {
		t.Fatal("two accepted turns received the same cancellation capability")
	}
	speechCtxA, doneA := orchestrator.SpeechSynthesisContext(orchestrator.WithSpeechTurnCancellation(context.Background(), cancellationA))
	defer doneA()
	speechCtxB, doneB := orchestrator.SpeechSynthesisContext(orchestrator.WithSpeechTurnCancellation(context.Background(), cancellationB))
	defer doneB()

	body, err := json.Marshal(map[string]string{"speech_cancel_token": tokenA})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/viewer/tts/cancel", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	registry.HandleCancel().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusNoContent, rec.Body.String())
	}
	if !errors.Is(speechCtxA.Err(), context.Canceled) {
		t.Fatalf("matching speech context error=%v want=%v", speechCtxA.Err(), context.Canceled)
	}
	if speechCtxB.Err() != nil {
		t.Fatalf("unrelated speech context was canceled: %v", speechCtxB.Err())
	}
}

func TestSpeechCancellationHandlerRejectsMalformedRequests(t *testing.T) {
	handler := NewSpeechCancellationRegistry().HandleCancel()
	for _, tt := range []struct {
		name   string
		method string
		body   string
		want   int
	}{
		{name: "wrong method", method: http.MethodGet, body: `{}`, want: http.StatusMethodNotAllowed},
		{name: "invalid json", method: http.MethodPost, body: `{`, want: http.StatusBadRequest},
		{name: "empty capability", method: http.MethodPost, body: `{"speech_cancel_token":" "}`, want: http.StatusBadRequest},
		{name: "unknown field", method: http.MethodPost, body: `{"speech_cancel_token":"cap","turn_id":"turn-a"}`, want: http.StatusBadRequest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tt.method, "/viewer/tts/cancel", strings.NewReader(tt.body))
			handler.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tt.want, rec.Body.String())
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control=%q want=no-store", got)
			}
		})
	}
}

func TestSpeechCancellationCleanupOnlyRemovesItsExactEntry(t *testing.T) {
	registry := NewSpeechCancellationRegistry()
	oldCancellation := orchestrator.NewSpeechTurnCancellation()
	token, cleanupOld, err := registry.Register("turn-old", oldCancellation)
	if err != nil {
		t.Fatal(err)
	}
	newCancellation := orchestrator.NewSpeechTurnCancellation()
	registry.mu.Lock()
	registry.entries[token] = &speechCancellationEntry{turnID: "turn-new", cancellation: newCancellation}
	registry.mu.Unlock()

	cleanupOld()
	if !registry.cancelToken(token) {
		t.Fatal("stale cleanup removed the replacement capability entry")
	}
	oldSpeechCtx, doneOld := orchestrator.SpeechSynthesisContext(orchestrator.WithSpeechTurnCancellation(context.Background(), oldCancellation))
	defer doneOld()
	newSpeechCtx, doneNew := orchestrator.SpeechSynthesisContext(orchestrator.WithSpeechTurnCancellation(context.Background(), newCancellation))
	defer doneNew()
	if oldSpeechCtx.Err() != nil {
		t.Fatalf("stale cancellation reached old turn unexpectedly: %v", oldSpeechCtx.Err())
	}
	if !errors.Is(newSpeechCtx.Err(), context.Canceled) {
		t.Fatalf("replacement speech context error=%v want=%v", newSpeechCtx.Err(), context.Canceled)
	}
}

func TestSpeechCancellationCompletionRacesOnlyItsOwnCapability(t *testing.T) {
	registry := NewSpeechCancellationRegistry()
	cancellationA := orchestrator.NewSpeechTurnCancellation()
	tokenA, cleanupA, err := registry.Register("turn-a", cancellationA)
	if err != nil {
		t.Fatal(err)
	}
	cancellationB := orchestrator.NewSpeechTurnCancellation()
	_, cleanupB, err := registry.Register("turn-b", cancellationB)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupB()
	speechCtxB, doneB := orchestrator.SpeechSynthesisContext(orchestrator.WithSpeechTurnCancellation(context.Background(), cancellationB))
	defer doneB()

	start := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		<-start
		registry.cancelToken(tokenA)
	}()
	go func() {
		defer workers.Done()
		<-start
		cleanupA()
	}()
	close(start)
	workers.Wait()
	if speechCtxB.Err() != nil {
		t.Fatalf("completion race canceled an unrelated turn: %v", speechCtxB.Err())
	}
}

func TestSpeechCancellationCapabilityHasAtLeast128Bits(t *testing.T) {
	registry := NewSpeechCancellationRegistry()
	token, cleanup, err := registry.Register("turn-a", orchestrator.NewSpeechTurnCancellation())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("capability is not raw URL-safe base64: %v", err)
	}
	if len(decoded) < 16 {
		t.Fatalf("capability entropy bytes=%d want at least 16", len(decoded))
	}
}
