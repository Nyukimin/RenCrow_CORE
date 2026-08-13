package viewer

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

type chatGPTL3ImporterStub struct {
	apply   bool
	records []l1sqlite.ChatGPTL3ImportRecord
}

func (stub *chatGPTL3ImporterStub) ImportChatGPTL3Records(_ context.Context, records []l1sqlite.ChatGPTL3ImportRecord, apply bool) (l1sqlite.ChatGPTL3ImportResult, error) {
	stub.apply = apply
	stub.records = records
	return l1sqlite.ChatGPTL3ImportResult{Validated: len(records)}, nil
}

func (stub *chatGPTL3ImporterStub) ConfirmChatGPTL3Candidates(_ context.Context, _ string, _ string, _ bool) (l1sqlite.ChatGPTL3ConfirmResult, error) {
	return l1sqlite.ChatGPTL3ConfirmResult{}, nil
}

func TestHandleChatGPTL3ImportDefaultsToDryRun(t *testing.T) {
	stub := &chatGPTL3ImporterStub{}
	body := `{"records":[{"format":"rencrow.chatgpt_l3.v1","export_id":"hash","evidence_id":"chatgpt_export:conv-1:msg-1","conversation_id":"conv-1","message_id":"msg-1","role":"user","text":"映画が好き","on_current_branch":true}]}`
	recorder := httptest.NewRecorder()
	HandleChatGPTL3Import(stub).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/viewer/memory/import/chatgpt", bytes.NewBufferString(body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if stub.apply || len(stub.records) != 1 || stub.records[0].EvidenceID != "chatgpt_export:conv-1:msg-1" {
		t.Fatalf("stub=%+v", stub)
	}
}
