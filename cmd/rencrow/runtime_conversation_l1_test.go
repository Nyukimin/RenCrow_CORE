package main

import (
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
)

func TestBuildConversationRuntimeUsesL1ConversationEngineWithoutAdvancedRuntime(t *testing.T) {
	cfg := &config.Config{
		Conversation: config.ConversationConfig{Enabled: false},
		Storage: config.StorageConfig{
			Databases: config.DatabasePathsConfig{
				ConversationL1: filepath.Join(t.TempDir(), "l1.db"),
			},
		},
	}

	runtime := buildConversationRuntime(cfg, primaryLLMProviders{}, nil, nil)
	if runtime.L1Store == nil {
		t.Fatal("L1Store is nil; configured Viewer read store must not depend on Conversation engine")
	}
	defer runtime.L1Store.Close()
	if runtime.Engine == nil {
		t.Fatal("L1 conversation engine is nil; shared Agent context must not depend on advanced conversation runtime")
	}
	if runtime.Manager != nil {
		t.Fatalf("advanced conversation manager unexpectedly enabled: %v", runtime.Manager)
	}
}
