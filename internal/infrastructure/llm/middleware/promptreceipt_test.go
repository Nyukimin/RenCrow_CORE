package middleware

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	domainllm "github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

type promptReceiptStubProvider struct{}

func (p promptReceiptStubProvider) Generate(context.Context, domainllm.GenerateRequest) (domainllm.GenerateResponse, error) {
	return domainllm.GenerateResponse{Content: "ok", FinishReason: "stop"}, nil
}

func (p promptReceiptStubProvider) Name() string { return "stub" }

func (p promptReceiptStubProvider) Chat(context.Context, domainllm.ChatRequest) (domainllm.ChatResponse, error) {
	return domainllm.ChatResponse{
		Message:      domainllm.ChatMessage{Role: "assistant", Content: "ok"},
		Done:         true,
		FinishReason: "stop",
	}, nil
}

func TestPromptReceiptProviderStoresBoundedSystemTailAndMetadataOnly(t *testing.T) {
	resetPromptReceiptForTest()
	t.Cleanup(resetPromptReceiptForTest)
	receiptPath := filepath.Join(t.TempDir(), "prompt_receipt.jsonl")
	t.Setenv("RENCROW_PROMPT_RECEIPT_LOG", receiptPath)

	provider := NewPromptReceiptProvider(promptReceiptStubProvider{}, "chat")
	ctx := domainllm.WithExecutionObservation(context.Background(), domainllm.ExecutionObservation{
		RequestID: "request-1", TraceID: "trace-1", JobID: "job-1", SessionID: "session-1",
		Initiator: "mio", Caller: "agent.mio", Purpose: "chat",
	})
	_, err := provider.Generate(ctx, domainllm.GenerateRequest{
		Messages: []domainllm.Message{
			{Role: "system", Content: "固定人格。最後の指示 token=do-not-store@example.com。"},
			{Role: "user", Content: "これはユーザー入力なので保存しない。"},
		},
		MaxTokens: 128,
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	data, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatalf("receipt was not written: %v", err)
	}
	var receipt promptReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatalf("receipt JSON: %v\n%s", err, data)
	}
	if receipt.SchemaVersion != 1 || receipt.Kind != "generate" || receipt.Provider != "chat" {
		t.Fatalf("receipt identity = %+v", receipt)
	}
	if receipt.RequestID != "request-1" || receipt.TraceID != "trace-1" || receipt.JobID != "job-1" {
		t.Fatalf("receipt correlation = %+v", receipt)
	}
	if receipt.PromptHash == "" || receipt.SystemPromptHash == "" {
		t.Fatalf("prompt hashes must be present: %+v", receipt)
	}
	if receipt.LatestPromptSentence == "" || !strings.Contains(receipt.LatestPromptSentence, "最後の指示") {
		t.Fatalf("latest system sentence missing: %+v", receipt)
	}
	if strings.Contains(receipt.LatestPromptSentence, "do-not-store") || strings.Contains(string(data), "ユーザー入力なので保存しない") {
		t.Fatalf("receipt leaked prompt/user content: %s", data)
	}
	if receipt.SectionCounts["system"] != 1 || receipt.SectionCounts["user"] != 1 {
		t.Fatalf("section counts = %+v", receipt.SectionCounts)
	}
}

func TestPromptReceiptProviderSupportsChatRequests(t *testing.T) {
	resetPromptReceiptForTest()
	t.Cleanup(resetPromptReceiptForTest)
	receiptPath := filepath.Join(t.TempDir(), "prompt_receipt.jsonl")
	t.Setenv("RENCROW_PROMPT_RECEIPT_LOG", receiptPath)

	provider := NewPromptReceiptProvider(promptReceiptStubProvider{}, "chat")
	_, err := provider.Chat(context.Background(), domainllm.ChatRequest{
		Messages: []domainllm.ChatMessage{
			{Role: "system", Content: "chat system. tail."},
			{Role: "user", Content: "hello"},
		},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	data, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatalf("receipt was not written: %v", err)
	}
	var receipt promptReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatalf("receipt JSON: %v", err)
	}
	if receipt.Kind != "chat" || receipt.LatestPromptSentence != "tail" {
		t.Fatalf("chat receipt = %+v", receipt)
	}
}

func TestLatestPromptSentenceUsesLastSystemSentence(t *testing.T) {
	got := latestPromptSentence("一つ目。二つ目\n最後の一文")
	if got != "最後の一文" {
		t.Fatalf("latestPromptSentence() = %q", got)
	}
}

func resetPromptReceiptForTest() {
	promptReceiptMu.Lock()
	defer promptReceiptMu.Unlock()
	if promptReceiptFile != nil {
		_ = promptReceiptFile.Close()
	}
	promptReceiptFile = nil
	promptReceiptErr = nil
	promptReceiptOnce = sync.Once{}
}
