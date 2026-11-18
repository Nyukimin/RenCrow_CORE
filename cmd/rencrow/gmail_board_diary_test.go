package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	gmailapp "github.com/Nyukimin/RenCrow_CORE/internal/application/gmailintake"
)

func TestGmailBoardDiaryWriterUsesPinnedDedicatedPrincipalAndExactDiaryScope(t *testing.T) {
	const principal, journal = "lumina-diary-import", "journal_0123456789abcdef0123456789abcdef"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-import-token" || r.Header.Get("X-BOARD-Principal-ID") != principal || r.Header.Get("X-BOARD-Journal-Instance-ID") != journal {
			t.Errorf("dedicated BOARD auth/pin headers missing")
		}
		w.Header().Set("X-Correlation-ID", "cor_fixture")
		switch r.URL.Path {
		case "/api/v1/connection":
			_ = json.NewEncoder(w).Encode(map[string]string{"principal_id": principal, "journal_instance_id": journal, "correlation_id": "cor_fixture"})
		case "/api/v1/diaries":
			if r.Method != http.MethodGet || r.URL.Query().Get("date") != "2026-11-15" || r.URL.Query().Get("actor_id") != "lumina" {
				t.Errorf("unexpected diary GET: %s %s", r.Method, r.URL.String())
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"date": "2026-11-15", "entries": []any{}, "correlation_id": "cor_fixture"})
		case "/api/v1/diaries/2026-11-15":
			if r.Method != http.MethodPut {
				t.Errorf("unexpected diary method: %s", r.Method)
			}
			var payload struct {
				Revision   int    `json:"revision"`
				Body       string `json:"body"`
				Reflection string `json:"reflection"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"entry": map[string]any{"actor_id": "lumina", "date": "2026-11-15", "revision": payload.Revision, "body": payload.Body, "reflection": payload.Reflection, "updated_at": "2026-11-16T01:02:03Z"}, "correlation_id": "cor_fixture"})
		default:
			t.Errorf("unexpected BOARD target %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	tokenPath := filepath.Join(t.TempDir(), "diary-import.json")
	credentials := `{"token":"fixture-import-token","expected_connection":{"principal_id":"` + principal + `","journal_instance_id":"` + journal + `"}}`
	if err := os.WriteFile(tokenPath, []byte(credentials), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(tokenPath, 0o600); err != nil {
		t.Fatal(err)
	}
	writer, err := newGmailBoardDiaryWriter(config.BoardCommunicationConfig{URL: server.URL, DiaryImportTokenFile: tokenPath})
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, found, err := writer.GetDiary(context.Background(), "2026-11-15"); err != nil || found {
		t.Fatalf("GetDiary missing: found=%t err=%v", found, err)
	}
	prepared := gmailapp.GmailDiary{ActorID: "lumina", Date: "2026-11-15", Revision: 1, Body: "conversation and summary", Reflection: "reflection"}
	got, err := writer.PutDiary(context.Background(), prepared)
	if err != nil || got != prepared {
		t.Fatalf("PutDiary = %#v err=%v", got, err)
	}
}

func TestGmailBoardDiaryWriterClosePreventsSubsequentWrite(t *testing.T) {
	const principal, journal = "lumina-diary-import", "journal_0123456789abcdef0123456789abcdef"
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Correlation-ID", "cor_fixture")
		if r.URL.Path == "/api/v1/connection" {
			_ = json.NewEncoder(w).Encode(map[string]string{"principal_id": principal, "journal_instance_id": journal, "correlation_id": "cor_fixture"})
			return
		}
		if r.Method == http.MethodPut {
			writes.Add(1)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"entry": map[string]any{"actor_id": "lumina", "date": "2026-11-15", "revision": 1, "body": "body", "reflection": "reflection", "updated_at": "2026-11-16T01:02:03Z"}, "correlation_id": "cor_fixture"})
	}))
	defer server.Close()
	tokenPath := filepath.Join(t.TempDir(), "diary-import.json")
	credentials := `{"token":"fixture-import-token","expected_connection":{"principal_id":"` + principal + `","journal_instance_id":"` + journal + `"}}`
	if err := os.WriteFile(tokenPath, []byte(credentials), 0o600); err != nil {
		t.Fatal(err)
	}
	writer, err := newGmailBoardDiaryWriter(config.BoardCommunicationConfig{URL: server.URL, DiaryImportTokenFile: tokenPath})
	if err != nil {
		t.Fatal(err)
	}
	(&Dependencies{gmailDiaryWriter: writer}).Shutdown()
	if _, err := writer.PutDiary(context.Background(), gmailapp.GmailDiary{ActorID: "lumina", Date: "2026-11-15", Revision: 1, Body: "body", Reflection: "reflection"}); err == nil {
		t.Fatal("write succeeded after client close")
	}
	if got := writes.Load(); got != 0 {
		t.Fatalf("close allowed %d diary writes", got)
	}
}

func TestNewGmailBoardDiaryWriterFailsClosedWithoutDedicatedCredentials(t *testing.T) {
	if _, err := newGmailBoardDiaryWriter(config.BoardCommunicationConfig{}); err == nil {
		t.Fatal("missing dedicated credential file was accepted")
	}
}
