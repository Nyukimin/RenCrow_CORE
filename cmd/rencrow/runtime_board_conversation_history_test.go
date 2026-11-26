package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentroutine"
)

func TestRegisteredBoardConversationHistory(t *testing.T) {
	token := strings.Repeat("c", 64)
	tokenPath := filepath.Join(t.TempDir(), "owner.token")
	if err := os.WriteFile(tokenPath, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := agentroutine.Open(filepath.Join(t.TempDir(), "routine.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Date(2026, 11, 25, 15, 0, 0, 0, time.UTC) // JST November 26 boundary.
	for _, actor := range agentHeartbeatActorIDs[:2] {
		_, err = store.SaveFact(context.Background(), agentroutine.Activity{ActorID: actor, Date: "2026-11-26", Key: "chat:conversation", Kind: agentroutine.ActivityMorningChat, At: at, Source: agentRoutineModuleID, SourceID: "chat", Confirmed: true, Reference: `[{"actor_id":"mio","text":"朝ですね"},{"actor_id":"shiro","text":"おはよう"}]`})
		if err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true, AuthTokenFile: tokenPath, UserID: "ren"}}
	mux := http.NewServeMux()
	registerRuntimeTaskActivitiesRoute(mux, cfg, &Dependencies{agentRoutine: &agentRoutineRuntime{store: store}})
	handler := withInteractionProfileGuard(mux)
	request := func(method, query, bearer, client, remote string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://127.0.0.1:18790/viewer/board/conversation-history"+query, nil)
		req.RemoteAddr = remote
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		req.Header.Set("X-RenCrow-Client", client)
		req.Header.Set(interactionProfileHeader, "board-studio-readonly")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	for _, tc := range []struct {
		name, method, query, bearer, client, remote string
		want                                        int
	}{
		{"read", "GET", "?date=2026-11-26", token, "RenCrow_BOARD", "127.0.0.1:49152", 200},
		{"empty", "GET", "?date=2026-11-25", token, "RenCrow_BOARD", "127.0.0.1:49152", 200},
		{"no bearer", "GET", "?date=2026-11-26", "", "RenCrow_BOARD", "127.0.0.1:49152", 401},
		{"wrong client", "GET", "?date=2026-11-26", token, "RenCrow_CMD", "127.0.0.1:49152", 403},
		{"write", "POST", "?date=2026-11-26", token, "RenCrow_BOARD", "127.0.0.1:49152", 403},
		{"remote", "GET", "?date=2026-11-26", token, "RenCrow_BOARD", "192.0.2.1:49152", 404},
		{"missing date", "GET", "", token, "RenCrow_BOARD", "127.0.0.1:49152", 400},
		{"invalid date", "GET", "?date=2026-02-30", token, "RenCrow_BOARD", "127.0.0.1:49152", 400},
		{"duplicate date", "GET", "?date=2026-11-26&date=2026-11-25", token, "RenCrow_BOARD", "127.0.0.1:49152", 400},
		{"extra query", "GET", "?date=2026-11-26&actor=mio", token, "RenCrow_BOARD", "127.0.0.1:49152", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := request(tc.method, tc.query, tc.bearer, tc.client, tc.remote)
			if rec.Code != tc.want {
				t.Fatalf("status=%d body=%s want=%d", rec.Code, rec.Body.String(), tc.want)
			}
			if tc.want == 200 {
				var payload struct {
					Items []struct {
						ID    string    `json:"id"`
						At    time.Time `json:"at"`
						Turns []struct {
							ActorID string `json:"actor_id"`
							Text    string `json:"text"`
						} `json:"turns"`
					} `json:"items"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil || payload.Items == nil {
					t.Fatalf("invalid payload: %s %v", rec.Body.String(), err)
				}
				if tc.name == "read" && (len(payload.Items) != 1 || payload.Items[0].ID != "chat" || !payload.Items[0].At.Equal(at) || len(payload.Items[0].Turns) != 2 || payload.Items[0].Turns[1].Text != "おはよう") {
					t.Fatalf("wrong/doubled transcript: %s", rec.Body.String())
				}
				if tc.name == "empty" && len(payload.Items) != 0 {
					t.Fatal(rec.Body.String())
				}
				if rec.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("private transcript must not be cached")
				}
			}
		})
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if rec := request("GET", "?date=2026-11-26", token, "RenCrow_BOARD", "127.0.0.1:49152"); rec.Code != 503 {
		t.Fatalf("closed DB status=%d", rec.Code)
	}
}

func TestBoardConversationHistoryFactsAndRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, reference string
		kind            agentroutine.ActivityKind
		confirmed       bool
		wantError       bool
		wantItems       int
	}{
		{"long conversation", `[{"actor_id":"mio","text":"first"},{"actor_id":"shiro","text":"second"},{"actor_id":"mio","text":"third"}]`, agentroutine.ActivityConversation, true, false, 1},
		{"unconfirmed", `invalid`, agentroutine.ActivityMorningChat, false, false, 0},
		{"other activity", `private worker artifact`, agentroutine.ActivityHobby, true, false, 0},
		{"corrupt", `invalid`, agentroutine.ActivityMorningChat, true, true, 0},
		{"empty", `[]`, agentroutine.ActivityMorningChat, true, true, 0},
		{"unknown actor", `[{"actor_id":"user","text":"private"},{"actor_id":"mio","text":"reply"}]`, agentroutine.ActivityConversation, true, true, 0},
		{"wrong participant", `[{"actor_id":"kuro","text":"first"},{"actor_id":"shiro","text":"second"}]`, agentroutine.ActivityConversation, true, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "routine.db")
			store, err := agentroutine.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			fact := agentroutine.Activity{ActorID: "mio", Date: "2026-11-26", Key: "conversation", Kind: tc.kind, At: time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC), Source: agentRoutineModuleID, SourceID: "chat", Confirmed: tc.confirmed, Reference: tc.reference}
			if _, err := store.SaveFact(context.Background(), fact); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = agentroutine.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			runtime := &agentRoutineRuntime{store: store}
			items, err := runtime.ReadBoardConversationHistory(context.Background(), fact.Date)
			if (err != nil) != tc.wantError || len(items) != tc.wantItems {
				t.Fatalf("items=%+v err=%v", items, err)
			}
			if tc.name == "long conversation" {
				if len(items[0].Turns) != 3 {
					t.Fatal(items)
				}
				fact.ActorID = "shiro"
				fact.Reference = `[{"actor_id":"mio","text":"conflict"},{"actor_id":"shiro","text":"second"}]`
				if _, err := store.SaveFact(context.Background(), fact); err != nil {
					t.Fatal(err)
				}
				if _, err := runtime.ReadBoardConversationHistory(context.Background(), fact.Date); err == nil {
					t.Fatal("conflicting participant copies accepted")
				}
			}
		})
	}
	var runtime *agentRoutineRuntime
	if _, err := runtime.ReadBoardConversationHistory(context.Background(), "2026-11-26"); err == nil {
		t.Fatal("missing runtime must not become empty success")
	}
}
