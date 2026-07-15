package main

import (
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
)

func TestBuildConversationRuntimeOpensL1ForViewerWithoutConversationEngine(t *testing.T) {
	cfg := &config.Config{
		Conversation: config.ConversationConfig{
			Enabled:      false,
			L1SQLitePath: filepath.Join(t.TempDir(), "l1.db"),
		},
	}

	runtime := buildConversationRuntime(cfg, primaryLLMProviders{}, nil, nil)
	if runtime.L1Store == nil {
		t.Fatal("L1Store is nil; configured Viewer read store must not depend on Conversation engine")
	}
	defer runtime.L1Store.Close()
	if runtime.Engine != nil || runtime.Manager != nil {
		t.Fatalf("conversation runtime unexpectedly enabled: engine=%v manager=%v", runtime.Engine, runtime.Manager)
	}
}
