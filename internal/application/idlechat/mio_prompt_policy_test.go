package idlechat

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMioPromptAllowsExternalInformationInIdleChat(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to resolve test file path")
	}

	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", ".."))
	promptPath := filepath.Join(repositoryRoot, "prompts", "mio.md")
	data, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatalf("read Mio prompt: %v", err)
	}
	prompt := string(data)

	if strings.Contains(prompt, "IdleChatでは外部検索をしません") {
		t.Fatal("Mio prompt must not prohibit external search in IdleChat")
	}

	for _, required := range []string{
		"通常会話とIdleChatのどちらでも",
		"外部情報を、許可されたCORE経路で積極的に利用します",
		"外部検索またはURL本文取得を利用します",
		"外部取得に失敗しても会話を止めず",
	} {
		if !strings.Contains(prompt, required) {
			t.Errorf("Mio prompt is missing external information policy: %q", required)
		}
	}
}
