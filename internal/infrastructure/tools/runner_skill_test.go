package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	domaincontext "github.com/Nyukimin/RenCrow_CORE/internal/domain/context"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

func TestSkillCatalogReadsOnlyStartupLoadedBodyAndBoundsIt(t *testing.T) {
	loadedBody := "trusted instruction body"
	skills := []domaincontext.SkillMetadata{{Name: "review", Description: "Review code", BodyText: loadedBody}}
	catalog := NewSkillCatalog(skills)
	skills[0].BodyText = "changed after startup"

	got, err := catalog.Read("review")
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if got != loadedBody {
		t.Fatalf("Read returned %q, want startup body %q", got, loadedBody)
	}

	longBody := strings.Repeat("あ", MaxSkillBodyBytes)
	bounded := NewSkillCatalog([]domaincontext.SkillMetadata{{Name: "long", BodyText: longBody}})
	got, err = bounded.Read("long")
	if err != nil {
		t.Fatalf("bounded Read failed: %v", err)
	}
	if len(got) > MaxSkillBodyBytes || !strings.HasSuffix(got, skillBodyTruncationNotice) {
		t.Fatalf("body is not bounded: len=%d suffix=%q", len(got), got[max(0, len(got)-len(skillBodyTruncationNotice)):])
	}
}

func TestSkillCatalogRejectsPathsAndUnknownNames(t *testing.T) {
	catalog := NewSkillCatalog([]domaincontext.SkillMetadata{{Name: "review", BodyText: "body"}})
	if _, err := catalog.Read("../review"); !errors.Is(err, ErrSkillNameInvalid) {
		t.Fatalf("path should be rejected explicitly, got %v", err)
	}
	if _, err := catalog.Read("missing"); !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("unknown name should fail explicitly, got %v", err)
	}
}

func TestSkillCatalogUsesSnapshotCanonicalNameAndSkipsEmptyBodies(t *testing.T) {
	catalog := NewSkillCatalog([]domaincontext.SkillMetadata{
		{Name: "Architecture-Review", BodyText: "trusted body"},
		{Name: "empty", BodyText: "  "},
	})
	if catalog.Len() != 1 {
		t.Fatalf("catalog len = %d, want 1", catalog.Len())
	}
	got, err := catalog.Read("architecture-review")
	if err != nil || got != "trusted body" {
		t.Fatalf("canonical Read = %q, %v", got, err)
	}
	if _, err := catalog.Read("empty"); !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("empty body should not be loaded, got %v", err)
	}
}

func TestSkillReadIsWorkerOnlyQueryToolAndIsExposedToToolDefinitions(t *testing.T) {
	catalog := NewSkillCatalog([]domaincontext.SkillMetadata{{Name: "review", Description: "Review", BodyText: "trusted body"}})
	worker := NewToolRunner(ToolRunnerConfig{SkillCatalog: catalog})
	chat := NewToolRunner(ToolRunnerConfig{})

	metas, err := worker.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}
	var found bool
	for _, metadata := range metas {
		if metadata.ToolID == "skill.read" {
			found = true
			if metadata.Category != "query" || metadata.Origin != tool.OriginCoreRuntime {
				t.Fatalf("skill.read metadata is not query-only core runtime: %#v", metadata)
			}
		}
	}
	if !found {
		t.Fatal("Worker runner did not expose skill.read")
	}
	foundDefinition := false
	for _, definition := range worker.ToolDefinitions() {
		if definition.Function.Name == "skill.read" {
			foundDefinition = true
			break
		}
	}
	if !foundDefinition {
		t.Fatal("skill.read was not exposed to ToolDefinitions")
	}
	for _, metadata := range mustListMetadata(t, chat) {
		if metadata.ToolID == "skill.read" {
			t.Fatal("Chat runner must not receive skill.read")
		}
	}

	resp, err := worker.ExecuteV2(context.Background(), "skill.read", map[string]any{"name": "review"})
	if err != nil || resp == nil || resp.IsError() || resp.String() != "trusted body" {
		t.Fatalf("skill.read execution failed: resp=%#v err=%v", resp, err)
	}
	unknown, err := worker.ExecuteV2(context.Background(), "skill.read", map[string]any{"name": "missing"})
	if err != nil || unknown == nil || !unknown.IsError() || unknown.Error.Code != tool.ErrNotFound {
		t.Fatalf("unknown skill should fail explicitly: resp=%#v err=%v", unknown, err)
	}
	path, err := worker.ExecuteV2(context.Background(), "skill.read", map[string]any{"name": "../review"})
	if err != nil || path == nil || !path.IsError() || path.Error.Code != tool.ErrValidationFailed {
		t.Fatalf("path input should be rejected: resp=%#v err=%v", path, err)
	}
}

func TestEmptySkillCatalogDoesNotRegisterSkillRead(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{SkillCatalog: NewSkillCatalog(nil)})
	for _, metadata := range mustListMetadata(t, runner) {
		if metadata.ToolID == "skill.read" {
			t.Fatal("empty catalog must not register skill.read")
		}
	}
	if _, err := runner.ExecuteV2(context.Background(), "skill.read", map[string]any{"name": "review"}); err == nil {
		t.Fatal("empty catalog skill.read should be unknown")
	}
}

func mustListMetadata(t *testing.T, runner *ToolRunner) []tool.ToolMetadata {
	t.Helper()
	metas, err := runner.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}
	return metas
}
