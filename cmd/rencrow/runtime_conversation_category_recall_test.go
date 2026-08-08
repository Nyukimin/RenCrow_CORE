package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

func TestBuildConversationRuntimeCategoryRecallMissingCatalogIsPartialOnlyWhenRelevant(t *testing.T) {
	cfg := &config.Config{
		Conversation: config.ConversationConfig{Enabled: false},
		Storage: config.StorageConfig{
			Databases: config.DatabasePathsConfig{
				ConversationL1: filepath.Join(t.TempDir(), "l1.db"),
				MovieCatalog:   filepath.Join(t.TempDir(), "missing-movie.db"),
			},
		},
	}

	runtime := buildConversationRuntime(cfg, primaryLLMProviders{}, nil, nil)
	if runtime.Engine == nil || runtime.L1Store == nil {
		t.Fatal("L1 conversation runtime should be available with a configured L1 store")
	}
	defer runtime.L1Store.Close()

	unrelated, err := runtime.Engine.BeginTurn(context.Background(), "category-recall-unrelated", "今日は元気です")
	if err != nil {
		t.Fatalf("unrelated BeginTurn failed: %v", err)
	}
	if len(unrelated.CategoryFailures) != 0 {
		t.Fatalf("missing catalog must not be queried for unrelated speech: %+v", unrelated.CategoryFailures)
	}

	related, err := runtime.Engine.BeginTurn(context.Background(), "category-recall-related", "映画の話をしよう")
	if err != nil {
		t.Fatalf("related BeginTurn failed: %v", err)
	}
	if len(related.CategoryFailures) == 0 {
		t.Fatal("related speech should preserve the unavailable movie source failure")
	}
	foundUnavailable := false
	for _, failure := range related.CategoryFailures {
		if failure.SourceID == "movie_catalog" && failure.Code == domconv.CategoryRecallFailureSourceUnavailable {
			foundUnavailable = true
			break
		}
	}
	if !foundUnavailable {
		t.Fatalf("related speech did not record movie_catalog source_unavailable: %+v", related.CategoryFailures)
	}
}
