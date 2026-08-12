package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	knowledgememorypersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/knowledgememory"
)

func TestRunKnowledgeMemoryPromoteUsesOnlyConfiguredFixedPaths(t *testing.T) {
	cfg := &config.Config{}
	cfg.KnowledgeMemory.LogPath = "/fixed/source"
	cfg.Storage.Databases.KnowledgeMemory = "/fixed/target.db"
	called := false
	promote := func(_ context.Context, source, target string) (knowledgememorypersistence.ImportReport, error) {
		called = true
		if source != "/fixed/source" || target != "/fixed/target.db" {
			t.Fatalf("promotion paths = %q %q", source, target)
		}
		return knowledgememorypersistence.ImportReport{SourceCount: 3, ImportedCount: 3, Coverage: knowledgememorypersistence.CoverageReceipt{State: knowledgememorypersistence.KnowledgeMemoryCoverageReady}}, nil
	}
	var out, errOut bytes.Buffer
	if code := runKnowledgeMemoryCommand(context.Background(), []string{"promote"}, cfg, promote, &out, &errOut); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut.String())
	}
	if !called || !strings.Contains(out.String(), `"source_count":3`) {
		t.Fatalf("called=%v output=%s", called, out.String())
	}
}

func TestRunKnowledgeMemoryPromoteRejectsPathArgumentsAndUnreadyResult(t *testing.T) {
	cfg := &config.Config{}
	cfg.KnowledgeMemory.LogPath = "/fixed/source"
	cfg.Storage.Databases.KnowledgeMemory = "/fixed/target.db"
	for _, test := range []struct {
		args    []string
		result  knowledgememorypersistence.ImportReport
		promErr error
	}{
		{args: []string{"promote", "/other"}},
		{args: []string{"promote"}, result: knowledgememorypersistence.ImportReport{SourceCount: 2, ImportedCount: 1, Coverage: knowledgememorypersistence.CoverageReceipt{State: knowledgememorypersistence.KnowledgeMemoryCoverageIndexing}}},
		{args: []string{"promote"}, promErr: errors.New("manifest mismatch")},
	} {
		var out, errOut bytes.Buffer
		code := runKnowledgeMemoryCommand(context.Background(), test.args, cfg, func(context.Context, string, string) (knowledgememorypersistence.ImportReport, error) {
			return test.result, test.promErr
		}, &out, &errOut)
		if code == 0 {
			t.Fatalf("args=%v unexpectedly succeeded", test.args)
		}
	}
}
