package viewer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appbacklog "github.com/Nyukimin/RenCrow_CORE/internal/application/backlog"
	gmailapp "github.com/Nyukimin/RenCrow_CORE/internal/application/gmailintake"
	domainkm "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

type gmailReceiptReaderStub struct {
	calls int
	t     *testing.T
}

type gmailDiaryBackfillStub struct{ calls int }

func (s *gmailDiaryBackfillStub) Receipts(context.Context, int) ([]gmailapp.GmailReceipt, error) {
	return nil, nil
}
func (s *gmailDiaryBackfillStub) StartGmailDiaryBackfill() bool { s.calls++; return true }

func (s *gmailReceiptReaderStub) Receipts(ctx context.Context, n int) ([]gmailapp.GmailReceipt, error) {
	s.calls++
	scope, ok := domaintool.ToolExecutionScopeFromContext(ctx)
	if !ok || scope.AuthenticatedUserID != "ren" || !scope.Allows(domaintool.DataScopeUser) {
		s.t.Fatal("missing owner scope")
	}
	return []gmailapp.GmailReceipt{{Status: "complete", Message: gmailapp.GmailMessage{Text: "private-spec"}, Prepared: []appbacklog.IntakeRequest{{Body: "prepared-only"}}, PreparedKnowledge: []gmailapp.GmailPreparedKnowledge{{Item: domainkm.NewsKnowledgeItem{Topic: "prepared-knowledge-only"}}}}}, nil
}

func TestAtlasGmailReceiptsRequireLocalOwnerAuthentication(t *testing.T) {
	reader := &gmailReceiptReaderStub{t: t}
	token := "owner-token-012345678901234567890123456789"
	handler := NewAtlasHandler(nil, "ren", []byte(token), reader)
	for _, tc := range []struct {
		auth, profile, remote string
		want                  int
	}{
		{"", "", "127.0.0.1:1", 401},
		{token, "", "127.0.0.1:1", 403},
		{token, "cmd-diagnostics", "203.0.113.2:1", 404},
		{token, "cmd-control", "127.0.0.1:1", 403},
		{token, "cmd-diagnostics", "127.0.0.1:1", 200},
	} {
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/viewer/atlas/gmail", nil)
		req.RemoteAddr = tc.remote
		req.Header.Set("Authorization", "Bearer "+tc.auth)
		req.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
		req.Header.Set("X-RenCrow-Interaction-Profile", tc.profile)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != tc.want {
			t.Fatalf("status=%d want=%d", response.Code, tc.want)
		}
		if tc.want != 200 && strings.Contains(response.Body.String(), "private-spec") {
			t.Fatal("mail disclosed")
		}
		if strings.Contains(response.Body.String(), "prepared-only") {
			t.Fatal("internal recovery payload disclosed")
		}
		if strings.Contains(response.Body.String(), "prepared-knowledge-only") {
			t.Fatal("private knowledge recovery payload disclosed")
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("private response cacheable")
		}
	}
	if reader.calls != 1 {
		t.Fatalf("unauthorized store reads: %d", reader.calls)
	}
}

func TestAtlasGmailDiaryBackfillRequiresOwnerAuthenticationAndPOST(t *testing.T) {
	starter := &gmailDiaryBackfillStub{}
	token := "owner-token-012345678901234567890123456789"
	handler := NewAtlasHandler(nil, "ren", []byte(token), starter)
	for _, tc := range []struct {
		method, auth, profile, remote string
		want                          int
	}{
		{http.MethodPost, "", "cmd-diagnostics", "127.0.0.1:1", http.StatusUnauthorized},
		{http.MethodPost, token, "cmd-diagnostics", "203.0.113.2:1", http.StatusNotFound},
		{http.MethodPost, token, "cmd-control", "127.0.0.1:1", http.StatusForbidden},
		{http.MethodGet, token, "cmd-diagnostics", "127.0.0.1:1", http.StatusMethodNotAllowed},
		{http.MethodPost, token, "cmd-diagnostics", "127.0.0.1:1", http.StatusAccepted},
	} {
		req := httptest.NewRequest(tc.method, "http://127.0.0.1/viewer/atlas/gmail/diary-backfill", nil)
		req.RemoteAddr = tc.remote
		req.Header.Set("Authorization", "Bearer "+tc.auth)
		req.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
		req.Header.Set("X-RenCrow-Interaction-Profile", tc.profile)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != tc.want {
			t.Fatalf("method=%s status=%d want=%d body=%s", tc.method, response.Code, tc.want, response.Body.String())
		}
	}
	if starter.calls != 1 {
		t.Fatalf("backfill started %d times", starter.calls)
	}
}
