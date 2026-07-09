package middleware

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	domainllm "github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

type rawLogStubProvider struct{}

func (p rawLogStubProvider) Generate(ctx context.Context, req domainllm.GenerateRequest) (domainllm.GenerateResponse, error) {
	return domainllm.GenerateResponse{Content: "表示されない低レイヤraw", FinishReason: "stop"}, nil
}

func (p rawLogStubProvider) Name() string {
	return "stub"
}

func TestRawLogProviderDoesNotWriteProviderCallsToIdleChatRaw(t *testing.T) {
	dir := t.TempDir()
	chatPath := filepath.Join(dir, "chat_raw.log")
	idlePath := filepath.Join(dir, "IdleChat_raw.log")
	t.Setenv("RENCROW_CHAT_RAW_LOG", chatPath)
	t.Setenv("RENCROW_IDLECHAT_RAW_LOG", idlePath)

	provider := NewRawLogProvider(rawLogStubProvider{}, "chat")
	_, err := provider.Generate(context.Background(), domainllm.GenerateRequest{
		Messages:  []domainllm.Message{{Role: "user", Content: "hello"}},
		MaxTokens: 64,
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	chatRaw, err := os.ReadFile(chatPath)
	if err != nil {
		t.Fatalf("chat raw was not written: %v", err)
	}
	if !strings.Contains(string(chatRaw), "表示されない低レイヤraw") {
		t.Fatalf("chat raw missing provider content: %q", string(chatRaw))
	}
	if _, err := os.Stat(idlePath); !os.IsNotExist(err) {
		t.Fatalf("provider raw call should not create IdleChat raw log, stat err=%v", err)
	}
}
