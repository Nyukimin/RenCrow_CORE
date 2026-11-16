package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	gmailapp "github.com/Nyukimin/RenCrow_CORE/internal/application/gmailintake"
)

type routeGmailDiaryBackfillStub struct{ starts int }

func (*routeGmailDiaryBackfillStub) Receipts(context.Context, int) ([]gmailapp.GmailReceipt, error) {
	return nil, nil
}

func (s *routeGmailDiaryBackfillStub) StartGmailDiaryBackfill() bool {
	s.starts++
	return true
}

func TestGmailDiaryBackfillTraversesRegisteredRoutesAndGlobalGuards(t *testing.T) {
	const token = "route-test-owner-token-012345678901234567890123"
	starter := &routeGmailDiaryBackfillStub{}
	deps := &Dependencies{atlasHandler: viewer.NewAtlasHandler(nil, "ren", []byte(token), starter)}
	mux := http.NewServeMux()
	registerFeatureRoutes(mux, &config.Config{}, deps, sttRuntime{}, voiceChatRuntime{}, viewer.DebugSystemOptions{})
	handler := withTailscaleViewerOnlyGuard(withInteractionProfileGuard(mux))

	request := func(method, path, auth, profile string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://127.0.0.1"+path, nil)
		req.RemoteAddr = "127.0.0.1:49152"
		req.Header.Set("Authorization", "Bearer "+auth)
		req.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
		req.Header.Set("X-RenCrow-Interaction-Profile", profile)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}

	accepted := request(http.MethodPost, "/viewer/atlas/gmail/diary-backfill", token, "cmd-diagnostics")
	if accepted.Code != http.StatusAccepted || accepted.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("registered owner POST status=%d content-type=%q body=%q", accepted.Code, accepted.Header().Get("Content-Type"), accepted.Body.String())
	}
	var payload map[string]string
	if err := json.Unmarshal(accepted.Body.Bytes(), &payload); err != nil || payload["status"] != "accepted" {
		t.Fatalf("registered owner POST response=%q err=%v", accepted.Body.String(), err)
	}
	if starter.starts != 1 {
		t.Fatalf("backfill starts=%d, want 1", starter.starts)
	}

	unauthorized := request(http.MethodPost, "/viewer/atlas/gmail/diary-backfill", "", "cmd-diagnostics")
	if unauthorized.Code != http.StatusUnauthorized || starter.starts != 1 {
		t.Fatalf("unauthorized POST status=%d starts=%d body=%q", unauthorized.Code, starter.starts, unauthorized.Body.String())
	}
	wrongProfile := request(http.MethodPost, "/viewer/atlas/gmail/diary-backfill", token, "cmd-control")
	if wrongProfile.Code != http.StatusForbidden || starter.starts != 1 {
		t.Fatalf("wrong-profile POST status=%d starts=%d", wrongProfile.Code, starter.starts)
	}
	unrelatedPost := request(http.MethodPost, "/viewer/atlas/gmail", token, "cmd-diagnostics")
	if unrelatedPost.Code != http.StatusForbidden || starter.starts != 1 {
		t.Fatalf("unrelated diagnostics POST status=%d starts=%d", unrelatedPost.Code, starter.starts)
	}
	ordinaryRead := request(http.MethodGet, "/viewer/atlas/gmail", token, "cmd-diagnostics")
	if ordinaryRead.Code != http.StatusOK || ordinaryRead.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("ordinary Atlas Gmail GET status=%d content-type=%q body=%q", ordinaryRead.Code, ordinaryRead.Header().Get("Content-Type"), ordinaryRead.Body.String())
	}
}
