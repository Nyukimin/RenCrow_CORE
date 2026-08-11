package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	capdomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	domaincontext "github.com/Nyukimin/RenCrow_CORE/internal/domain/context"
	domainskill "github.com/Nyukimin/RenCrow_CORE/internal/domain/skillgovernance"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/mcp"
	toolsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

type runtimeCapabilityRunnerStub struct {
	metadata []domaintool.ToolMetadata
	err      error
}

func (s runtimeCapabilityRunnerStub) ExecuteV2(context.Context, string, map[string]any) (*domaintool.ToolResponse, error) {
	return nil, errors.New("not implemented")
}

func (s runtimeCapabilityRunnerStub) ListTools(context.Context) ([]domaintool.ToolMetadata, error) {
	return append([]domaintool.ToolMetadata(nil), s.metadata...), s.err
}

func TestBuildRuntimeCapabilityContextProjectsToolsSkillsAndMCPDeterministically(t *testing.T) {
	got := buildRuntimeCapabilityContext(
		[]domaintool.ToolMetadata{
			{ToolID: "z_tool", Description: "z", Origin: domaintool.OriginCoreRuntime},
			{ToolID: "a_tool", Description: "a", Origin: domaintool.OriginRenCrowTools},
			{ToolID: "a_tool", Description: "duplicate", Origin: domaintool.OriginCoreRuntime},
		},
		[]domainskill.SkillManifest{
			{SkillID: "skill.disabled", Description: "disabled", Scope: domainskill.ScopeCore, Enabled: false},
			{SkillID: "skill.enabled", Description: "enabled", Scope: domainskill.ScopeProject, Enabled: true},
		},
		[]runtimeMCPObservation{
			{ServerName: "serena", ToolName: "search", Origin: "serena", Available: true},
			{ServerName: "serena", ToolName: "write", Reason: "権限未構成"},
		},
	)

	for _, want := range []string{
		"利用可能: a_tool",
		"利用可能: z_tool",
		"利用可能: skill.enabled",
		"利用不可: skill.disabled",
		"理由: Skillが無効化されています",
		"利用可能: serena.search",
		"利用不可: serena.write",
		"理由: 権限未構成",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("context does not contain %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "a_tool") != 1 {
		t.Fatalf("duplicate Tool was not removed:\n%s", got)
	}
	if strings.Index(got, "利用可能: a_tool") > strings.Index(got, "利用可能: z_tool") {
		t.Fatalf("Tool order is not deterministic:\n%s", got)
	}
	if strings.Contains(got, "/") || strings.Contains(got, "\\") {
		t.Fatalf("rendered capability context contains a path separator:\n%s", got)
	}
}

func TestBuildRuntimeCapabilityContextWithLoadedSkillsUnionsGovernanceFailClosed(t *testing.T) {
	got := buildRuntimeCapabilityContextWithSkills(
		nil,
		[]domaincontext.SkillMetadata{
			{Name: "enabled", Description: "loaded body", BodyText: "trusted"},
			{Name: "disabled", Description: "loaded but disabled", BodyText: "trusted"},
		},
		[]domainskill.SkillManifest{
			{SkillID: "disabled", Enabled: false, Scope: domainskill.ScopeCore},
			{SkillID: "missing", Enabled: true, Scope: domainskill.ScopeProject, Description: "manifest only"},
		},
		nil,
	)
	if !strings.Contains(got, "利用可能: enabled") {
		t.Fatalf("loaded skill should be available: %s", got)
	}
	if !strings.Contains(got, "利用不可: disabled") || !strings.Contains(got, "無効化または競合") {
		t.Fatalf("disabled governance state must win: %s", got)
	}
	if !strings.Contains(got, "利用不可: missing") || !strings.Contains(got, "SKILL.mdがロードされていません") {
		t.Fatalf("manifest without loaded body must fail closed: %s", got)
	}
}

func TestRuntimeCapabilityContextFromWorkerRunnerFailsClosedOnListError(t *testing.T) {
	got := runtimeCapabilityContextFromWorkerRunner(
		context.Background(),
		runtimeCapabilityRunnerStub{err: errors.New("/secret/runtime path unavailable")},
		nil,
		nil,
	)
	if strings.Contains(got, "利用可能: ") && !strings.Contains(got, "利用可能: なし") {
		t.Fatalf("ListTools failure must not expose available Tools:\n%s", got)
	}
	if !strings.Contains(got, "利用不可: worker_toolrunner") || !strings.Contains(got, "Worker Tool一覧を取得できません") {
		t.Fatalf("ListTools failure reason is missing:\n%s", got)
	}
	if strings.Contains(got, "/secret") || strings.Contains(got, "runtime path") {
		t.Fatalf("underlying ListTools error leaked into context:\n%s", got)
	}
}

func TestEmptyGenericMCPClientDoesNotProjectAvailableMCP(t *testing.T) {
	client := mcp.NewMCPClient()
	observations := observeGenericMCPClient(context.Background(), client)
	got := buildRuntimeCapabilityContext(nil, nil, observations)

	if len(observations) != 0 {
		t.Fatalf("empty MCP client observations = %#v, want none", observations)
	}
	if !strings.Contains(got, "### MCP") || !strings.Contains(got, "利用可能: なし") {
		t.Fatalf("empty MCP section must be explicit:\n%s", got)
	}
	if strings.Contains(got, "利用可能: serena") {
		t.Fatalf("empty MCP client must not claim an available server:\n%s", got)
	}
}

func TestRegisteredGenericMCPWithoutObservedToolsIsUnavailable(t *testing.T) {
	client := mcp.NewMCPClient()
	if err := client.RegisterServer(mcp.ServerConfig{Name: "generic", Command: "mcp-server"}); err != nil {
		t.Fatalf("RegisterServer failed: %v", err)
	}
	got := buildRuntimeCapabilityContext(nil, nil, observeGenericMCPClient(context.Background(), client))
	if strings.Contains(got, "利用可能: generic") {
		t.Fatalf("unobserved MCP server must not be available:\n%s", got)
	}
	if !strings.Contains(got, "利用不可: generic") || !strings.Contains(got, "MCP Toolが未観測です") {
		t.Fatalf("unobserved MCP reason is missing:\n%s", got)
	}
}

func TestAppendRuntimeCapabilityContextKeepsExistingContextText(t *testing.T) {
	contexts := map[string]string{
		"mio":   "mio contract",
		"shiro": "shiro contract",
	}
	appendRuntimeCapabilityContext(contexts, "## Runtime Capability Snapshot\n- 利用可能: shell")

	for name, want := range map[string]string{
		"mio":   "mio contract",
		"shiro": "shiro contract",
	} {
		if !strings.HasPrefix(contexts[name], want+"\n\n## Runtime Capability Snapshot") {
			t.Fatalf("%s context was not appended as a separate section: %q", name, contexts[name])
		}
	}
}

func TestRuntimeCapabilityContextDoesNotGrantCoderTools(t *testing.T) {
	contextText := buildRuntimeCapabilityContext([]domaintool.ToolMetadata{{ToolID: "shell"}}, nil, nil)
	if !strings.Contains(contextText, "実行権限を付与しません") || !strings.Contains(contextText, "許可されたRunner") {
		t.Fatalf("capability context boundary is missing: %q", contextText)
	}
}

func TestRuntimeCapabilitySnapshotCoversEveryProductionWorkerMetadata(t *testing.T) {
	registry := &runtimeCapabilityToolRegistryStub{entries: []capdomain.ToolEntry{{
		Name:        "registered_runtime_tool",
		Description: "起動時に登録されたruntime Tool",
	}}}
	runtime := buildToolRuntimeWithCapabilities(testCapabilityRuntimeConfig(t), nil, registry, nil, nil, nil)
	metadata, err := runtime.WorkerRunnerV2.ListTools(context.Background())
	if err != nil {
		t.Fatalf("production Worker ListTools failed: %v", err)
	}

	snapshot := capdomain.Normalize(buildRuntimeCapabilitySnapshotWithSkills(metadata, nil, nil, nil))
	rendered := capdomain.RenderStableRuntimeContext(snapshot)
	if len(metadata) == 0 {
		t.Fatal("production Worker metadata unexpectedly empty")
	}
	for _, item := range metadata {
		if strings.HasPrefix(item.ToolID, "mcp.") {
			t.Fatalf("MCP metadata requires an explicit observed MCP projection: %#v", item)
		}
		entry, ok := findRuntimeCapability(snapshot, capdomain.CapabilityKindTool, item.ToolID)
		if !ok || entry.Status != capdomain.CapabilityStatusAvailable {
			t.Fatalf("Worker metadata is missing from available Tool snapshot: metadata=%#v snapshot=%#v", item, snapshot.Entries)
		}
		if !strings.Contains(rendered, "利用可能: "+strings.ToLower(strings.TrimSpace(item.ToolID))) {
			t.Fatalf("rendered snapshot is missing Worker metadata %q:\n%s", item.ToolID, rendered)
		}
	}
}

func TestRuntimeCapabilitySnapshotSkillsMatchWorkerSkillReadCatalog(t *testing.T) {
	loaded := []domaincontext.SkillMetadata{
		{Name: "review", Description: "コードレビュー", BodyText: "trusted review body"},
		{Name: "release-check", Description: "リリース確認", BodyText: "trusted release body"},
	}
	catalog := toolsinfra.NewSkillCatalog(loaded)
	runtime := buildToolRuntimeWithCapabilities(testCapabilityRuntimeConfig(t), nil, nil, nil, catalog, nil)
	metadata, err := runtime.WorkerRunnerV2.ListTools(context.Background())
	if err != nil {
		t.Fatalf("production Worker ListTools failed: %v", err)
	}
	if !hasToolMetadata(metadata, "skill.read") {
		t.Fatalf("Worker metadata missing skill.read: %#v", metadata)
	}

	snapshot := capdomain.Normalize(buildRuntimeCapabilitySnapshotWithSkills(metadata, loaded, nil, nil))
	rendered := capdomain.RenderStableRuntimeContext(snapshot)
	for _, skill := range loaded {
		body, err := catalog.Read(skill.Name)
		if err != nil || body != skill.BodyText {
			t.Fatalf("discovered Skill %q has no matching startup catalog body: body=%q err=%v", skill.Name, body, err)
		}
		response, err := runtime.WorkerRunnerV2.ExecuteV2(context.Background(), "skill.read", map[string]any{"name": skill.Name})
		if err != nil || response == nil || response.IsError() || response.String() != skill.BodyText {
			t.Fatalf("production Worker skill.read mismatch for %q: response=%#v err=%v", skill.Name, response, err)
		}
		entry, ok := findRuntimeCapability(snapshot, capdomain.CapabilityKindSkill, skill.Name)
		if !ok || entry.Status != capdomain.CapabilityStatusAvailable {
			t.Fatalf("discovered Skill %q is missing from available snapshot: %#v", skill.Name, snapshot.Entries)
		}
		if !strings.Contains(rendered, "利用可能: "+strings.ToLower(skill.Name)) {
			t.Fatalf("rendered snapshot is missing Skill %q:\n%s", skill.Name, rendered)
		}
	}
}

func TestRuntimeCapabilitySnapshotMirrorsEveryObservedMCPCatalogEntry(t *testing.T) {
	client := &serenaRuntimeClientStub{toolNames: []string{"find.symbol", "replace_symbol"}, result: "ok"}
	catalog := toolsinfra.NewMCPToolCatalog("serena", client, client.toolNames)
	runtime := buildToolRuntimeWithCapabilities(testCapabilityRuntimeConfig(t), nil, nil, nil, nil, catalog)
	metadata, err := runtime.WorkerRunnerV2.ListTools(context.Background())
	if err != nil {
		t.Fatalf("production Worker ListTools failed: %v", err)
	}

	observations := make([]runtimeMCPObservation, 0, len(catalog.Entries()))
	for _, item := range catalog.Entries() {
		observations = append(observations, runtimeMCPObservation{
			ServerName:  "serena",
			ToolName:    item.RemoteName,
			ExposedName: item.ToolID,
			Origin:      "serena",
			Available:   true,
		})
	}
	snapshot := capdomain.Normalize(buildRuntimeCapabilitySnapshotWithSkills(metadata, nil, nil, observations))
	rendered := capdomain.RenderStableRuntimeContext(snapshot)
	for _, item := range catalog.Entries() {
		if !hasToolMetadata(metadata, item.ToolID) {
			t.Fatalf("observed MCP catalog entry is missing from Worker metadata: %#v", item)
		}
		response, err := runtime.WorkerRunnerV2.ExecuteV2(context.Background(), item.ToolID, map[string]any{"probe": item.ToolID})
		if err != nil || response == nil || response.IsError() || response.String() != "ok" {
			t.Fatalf("production Worker MCP execution failed for %q: response=%#v err=%v", item.ToolID, response, err)
		}
		entry, ok := findRuntimeCapability(snapshot, capdomain.CapabilityKindMCP, item.ToolID)
		if !ok || entry.Status != capdomain.CapabilityStatusAvailable {
			t.Fatalf("observed MCP catalog entry is missing from available MCP snapshot: item=%#v snapshot=%#v", item, snapshot.Entries)
		}
		if _, duplicatedAsTool := findRuntimeCapability(snapshot, capdomain.CapabilityKindTool, item.ToolID); duplicatedAsTool {
			t.Fatalf("MCP catalog entry must not be projected as an ordinary Tool: %q", item.ToolID)
		}
		if !strings.Contains(rendered, "利用可能: "+strings.ToLower(item.ToolID)) {
			t.Fatalf("rendered snapshot is missing MCP %q:\n%s", item.ToolID, rendered)
		}
	}
}

func testCapabilityRuntimeConfig(t *testing.T) *config.Config {
	t.Helper()
	disabled := false
	return &config.Config{
		WorkspaceDir: t.TempDir(),
		ToolHarness: config.ToolHarnessConfig{
			Enabled:      &disabled,
			RecordEvents: &disabled,
		},
	}
}

func findRuntimeCapability(snapshot capdomain.RuntimeCapabilitySnapshot, kind capdomain.CapabilityKind, name string) (capdomain.RuntimeCapability, bool) {
	for _, entry := range snapshot.Entries {
		if entry.Kind == kind && strings.EqualFold(strings.TrimSpace(entry.Name), strings.TrimSpace(name)) {
			return entry, true
		}
	}
	return capdomain.RuntimeCapability{}, false
}

type runtimeCapabilityToolRegistryStub struct {
	entries []capdomain.ToolEntry
}

func (s *runtimeCapabilityToolRegistryStub) Register(_ context.Context, entry capdomain.ToolEntry) error {
	s.entries = append(s.entries, entry)
	return nil
}

func (s *runtimeCapabilityToolRegistryStub) ListForPlatform(context.Context, string) ([]capdomain.ToolEntry, error) {
	return append([]capdomain.ToolEntry(nil), s.entries...), nil
}

func (s *runtimeCapabilityToolRegistryStub) Get(_ context.Context, name string) (capdomain.ToolEntry, error) {
	for _, entry := range s.entries {
		if entry.Name == name {
			return entry, nil
		}
	}
	return capdomain.ToolEntry{}, errors.New("runtime tool not found")
}

func (s *runtimeCapabilityToolRegistryStub) Close() error { return nil }
