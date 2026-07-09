package viewer

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/characterruntime"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
)

type captureCharacterRuntimeEvents struct {
	events []orchestrator.OrchestratorEvent
}

func (c *captureCharacterRuntimeEvents) OnEvent(ev orchestrator.OrchestratorEvent) {
	c.events = append(c.events, ev)
}

func TestHandleCharacterRuntimeRunRoundEmitsSixTurns(t *testing.T) {
	events := &captureCharacterRuntimeEvents{}
	handler := HandleCharacterRuntime(characterruntime.NewService(), events)
	rec := httptest.NewRecorder()

	handler(rec, httptest.NewRequest(http.MethodPost, "/viewer/character-runtime", strings.NewReader(`{"user_message":"進めて","requested_by":"viewer-test"}`)))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Result struct {
			Turns []struct {
				CharacterID string `json:"character_id"`
			} `json:"turns"`
		} `json:"result"`
	}
	if err := json.NewDecoder(bytes.NewReader(rec.Body.Bytes())).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Result.Turns) != 6 || len(events.events) != 6 {
		t.Fatalf("turns=%d events=%d body=%s", len(body.Result.Turns), len(events.events), rec.Body.String())
	}
	if body.Result.Turns[0].CharacterID != "mio" || body.Result.Turns[5].CharacterID != "gin" {
		t.Fatalf("unexpected turn order: %#v", body.Result.Turns)
	}
	if events.events[0].Type != "character_runtime.turn" || events.events[0].Route != "CHARACTER_RUNTIME" {
		t.Fatalf("unexpected event: %#v", events.events[0])
	}
}

func TestHandleCharacterRuntimeGetCharacters(t *testing.T) {
	rec := httptest.NewRecorder()
	HandleCharacterRuntime(characterruntime.NewService(), nil)(rec, httptest.NewRequest(http.MethodGet, "/viewer/character-runtime", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"kin"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
