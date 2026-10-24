package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	idlechat "github.com/Nyukimin/RenCrow_CORE/internal/application/idlechat"
)

func TestLegacyIdleChatStartRoutesReturnTypedConflictWithoutSideEffects(t *testing.T) {
	resetIdleChatTTSQueue()
	t.Cleanup(resetIdleChatTTSQueue)

	ctx, cancel := context.WithCancel(context.Background())
	item := &idleChatTTSItem{
		ev:     idlechat.TimelineEvent{SessionID: "legacy-start-rejection", MessageID: "queued-speech"},
		ctx:    ctx,
		cancel: cancel,
	}
	if !registerIdleChatTTSItem(item) {
		t.Fatal("failed to register pre-existing queued TTS item")
	}
	t.Cleanup(func() {
		unregisterIdleChatTTSItem(item)
		cancel()
	})

	routes := []struct {
		path    string
		handler func(*Dependencies) http.HandlerFunc
	}{
		{path: "/viewer/idlechat/start", handler: func(d *Dependencies) http.HandlerFunc { return d.handleIdleChatStart() }},
		{path: "/viewer/idlechat/forecast", handler: func(d *Dependencies) http.HandlerFunc { return d.handleIdleChatForecast() }},
		{path: "/viewer/idlechat/story", handler: func(d *Dependencies) http.HandlerFunc { return d.handleIdleChatStory() }},
		{path: "/viewer/idlechat/story-simple", handler: func(d *Dependencies) http.HandlerFunc { return d.handleIdleChatStorySimple() }},
	}

	for _, route := range routes {
		t.Run(route.path, func(t *testing.T) {
			orch := &idlechat.IdleChatOrchestrator{}
			deps := &Dependencies{idleChatOrch: orch}
			before := idleChatStartSideEffectSnapshot(orch)

			request := httptest.NewRequest(http.MethodPost, route.path, strings.NewReader(`{}`))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			route.handler(deps).ServeHTTP(recorder, request)

			if recorder.Code != http.StatusConflict {
				t.Errorf("status=%d body=%q, want typed 409", recorder.Code, recorder.Body.String())
			}
			if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
				t.Errorf("Content-Type=%q, want application/json", got)
			}
			var response struct {
				OK        bool   `json:"ok"`
				ErrorCode string `json:"error_code"`
				Error     string `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Errorf("decode typed response: %v; body=%q", err, recorder.Body.String())
			} else {
				if response.OK {
					t.Error("response ok=true, want false")
				}
				if response.ErrorCode != "legacy_idlechat_start_disabled" {
					t.Errorf("error_code=%q, want legacy_idlechat_start_disabled", response.ErrorCode)
				}
				if strings.TrimSpace(response.Error) == "" {
					t.Error("error message is empty")
				}
			}

			if after := idleChatStartSideEffectSnapshot(orch); !reflect.DeepEqual(after, before) {
				t.Errorf("IdleChat state changed: before=%+v after=%+v", before, after)
			}
			select {
			case <-ctx.Done():
				t.Error("legacy start request canceled pre-existing queued TTS")
			default:
			}
		})
	}
}

type idleChatStartStateSnapshot struct {
	mode         string
	manual       bool
	active       bool
	disabled     bool
	currentTopic string
	storyStock   idlechat.StoryEpisodeStockSnapshot
}

func idleChatStartSideEffectSnapshot(orch *idlechat.IdleChatOrchestrator) idleChatStartStateSnapshot {
	return idleChatStartStateSnapshot{
		mode:         orch.CurrentMode(),
		manual:       orch.IsManualMode(),
		active:       orch.IsChatActive(),
		disabled:     orch.IsDisabled(),
		currentTopic: orch.CurrentTopic(),
		storyStock:   orch.StoryEpisodeStockSnapshot(),
	}
}
