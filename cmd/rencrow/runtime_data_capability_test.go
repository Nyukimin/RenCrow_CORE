package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/datacapability"
	capdomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	toolsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

func TestRuntimeDataCapabilityCatalogAllKeysNoPaths(t *testing.T) {
	cfg := &config.Config{}
	cfg.Storage.Databases.Glossary = "/srv/private/glossary.db"
	catalog := buildRuntimeDataCapabilityCatalog(cfg, false, false)
	all := catalog.catalog.All()
	encoded, _ := json.Marshal(all)
	if strings.Contains(string(encoded), "/srv/") {
		t.Fatalf("path leak: %s", encoded)
	}
	if len(all) != 20 {
		t.Fatalf("entries=%d", len(all))
	}
	listed, err := catalog.Execute("list_catalog", "")
	if err != nil || len(listed.([]datacapability.Entry)) != 20 {
		t.Fatalf("list_catalog=%#v err=%v", listed, err)
	}
	entry, _ := catalog.catalog.Describe("glossary")
	if entry.Status != "unavailable" {
		t.Fatalf("entry=%#v", entry)
	}
}

func TestRuntimeDataCapabilityCatalogInvestmentNeverUsesConversationL1AsOwnerFallback(t *testing.T) {
	dir := t.TempDir()
	l1 := filepath.Join(dir, "l1.db")
	if err := os.WriteFile(l1, []byte("projection-ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Storage.Databases.ConversationL1 = l1
	catalog := buildRuntimeDataCapabilityCatalog(cfg, false, false)
	entry, err := catalog.catalog.Describe("investment")
	if err != nil || entry.Status != "unavailable" || entry.Reason == "" {
		t.Fatalf("investment entry=%#v err=%v", entry, err)
	}
}

func TestRuntimeDataCapabilityCatalogInvestmentUsesRegisteredOwnerRoutesWithoutLocalDB(t *testing.T) {
	catalog := buildRuntimeDataCapabilityCatalog(&config.Config{}, false, false)
	recall := newRuntimeDataRecallRegistry()
	write := newRuntimeDataWriteRegistry()
	if err := recall.Register("investment", "portfolio_snapshot", dataRecallAccessInternal, func(_ context.Context, request toolsinfra.DataRecallRequest) (runtimeDataRecallResult, error) {
		return newRuntimeDataRecallResult(request.Store, request.Operation, []map[string]any{}), nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := write.RegisterWithContract("investment", "ensure_portfolio_initialized", dataRecallAccessInternal, runtimeDataWriteContract{
		RequiredPayloadFields: []string{"run_id"},
	}, func(context.Context, toolsinfra.DataWriteRequest) (runtimeDataWriteOwnerResult, error) {
		return validRuntimeDataWriteOwnerResult(), nil
	}); err != nil {
		t.Fatal(err)
	}
	catalog.BindRouteRegistries(recall, write)

	value, err := catalog.Execute("describe", "investment")
	if err != nil {
		t.Fatal(err)
	}
	entry := value.(datacapability.Entry)
	if entry.Status != "available" || entry.Reason != "" {
		t.Fatalf("registered owner routes must determine availability without local DB: %#v", entry)
	}
	if !entry.OwnerRouteOnly || entry.PhysicalKey != "" {
		t.Fatalf("investment owner boundary projection=%#v", entry)
	}
}

func TestRuntimeDataCapabilityCatalogProjectsOnlyRegisteredExecutableRoutes(t *testing.T) {
	catalog := buildRuntimeDataCapabilityCatalog(&config.Config{}, false, false)
	recall := newRuntimeDataRecallRegistry()
	write := newRuntimeDataWriteRegistry()
	if err := recall.Register("advisor", "adoptions", dataRecallAccessInternal, func(_ context.Context, request toolsinfra.DataRecallRequest) (runtimeDataRecallResult, error) {
		return newRuntimeDataRecallResult(request.Store, request.Operation, []map[string]any{}), nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := write.RegisterWithContract("advisor", "record_adoption", dataRecallAccessInternal, runtimeDataWriteContract{
		RequiredPayloadFields: []string{"run_id", "adopted", "outcome"},
		OptionalPayloadFields: []string{"reason"},
	}, func(context.Context, toolsinfra.DataWriteRequest) (runtimeDataWriteOwnerResult, error) {
		return validRuntimeDataWriteOwnerResult(), nil
	}); err != nil {
		t.Fatal(err)
	}
	catalog.BindRouteRegistries(recall, write)
	value, err := catalog.Execute("describe", "advisor")
	if err != nil {
		t.Fatal(err)
	}
	entry := value.(datacapability.Entry)
	if entry.Status != "available" || entry.Reason != "" {
		t.Fatalf("registered read/write entry status=%#v", entry)
	}
	if !reflect.DeepEqual(entry.RecallRoutes, []datacapability.Route{{Operation: "adoptions", ToolID: "data.recall", Access: "internal"}}) {
		t.Fatalf("recall routes=%#v", entry.RecallRoutes)
	}
	if !reflect.DeepEqual(entry.WriteRoutes, []datacapability.Route{{Operation: "record_adoption", ToolID: "data.write", Access: "internal", RequiredFields: []string{"adopted", "outcome", "run_id"}, OptionalFields: []string{"reason"}}}) {
		t.Fatalf("write routes=%#v", entry.WriteRoutes)
	}
	encoded, _ := json.Marshal(entry)
	for _, forbidden := range []string{"callback", "/srv/", "database_path"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("route projection leaked %q: %s", forbidden, encoded)
		}
	}
	unknown, err := catalog.Execute("describe", "sandbox")
	if err != nil {
		t.Fatal(err)
	}
	if got := unknown.(datacapability.Entry); len(got.RecallRoutes) != 0 || len(got.WriteRoutes) != 0 || got.Status != "blocked" || got.Reason != "owner_route_unavailable" {
		t.Fatalf("unregistered routes projected: %#v", got)
	}
	availableValue, err := catalog.Execute("list_available", "")
	if err != nil {
		t.Fatal(err)
	}
	available := availableValue.([]datacapability.Entry)
	if len(available) != 1 || available[0].Name != "advisor" {
		t.Fatalf("executable available entries=%#v", available)
	}

	partialCatalog := buildRuntimeDataCapabilityCatalog(&config.Config{}, false, false)
	partialCatalog.BindRouteRegistries(recall, newRuntimeDataWriteRegistry())
	partial, err := partialCatalog.Execute("describe", "advisor")
	if err != nil {
		t.Fatal(err)
	}
	if got := partial.(datacapability.Entry); got.Status != "blocked" || got.Reason != "owner_route_incomplete" {
		t.Fatalf("partial route status=%#v", got)
	}
}

func TestRuntimeDataCapabilityCatalogProjectsDurableRequirementRecallRoute(t *testing.T) {
	catalog := buildRuntimeDataCapabilityCatalog(&config.Config{}, false, false)
	recall := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallDurableStoreWorkflow(recall, &dataRecallDurableStoreStub{}); err != nil {
		t.Fatal(err)
	}
	catalog.BindRouteRegistries(recall, newRuntimeDataWriteRegistry())
	value, err := catalog.Execute("describe", "durable_store_workflow")
	if err != nil {
		t.Fatal(err)
	}
	entry := value.(datacapability.Entry)
	want := []datacapability.Route{
		{Operation: "exact_request", ToolID: "data.recall", Access: string(dataRecallAccessUser)},
		{Operation: "requirement", ToolID: "data.recall", Access: string(dataRecallAccessUser)},
	}
	if !reflect.DeepEqual(entry.RecallRoutes, want) {
		t.Fatalf("durable recall routes=%#v, want %#v", entry.RecallRoutes, want)
	}
	if entry.Status != "blocked" || entry.Reason != "owner_route_incomplete" {
		t.Fatalf("durable entry status=%#v", entry)
	}
}

func TestRenderRuntimeDataRouteContextUsesRegistrySnapshotContracts(t *testing.T) {
	recall := newRuntimeDataRecallRegistry()
	write := newRuntimeDataWriteRegistry()
	if err := recall.Register("dci", "search_traces", dataRecallAccessInternal, func(_ context.Context, request toolsinfra.DataRecallRequest) (runtimeDataRecallResult, error) {
		return newRuntimeDataRecallResult(request.Store, request.Operation, []map[string]any{}), nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := write.RegisterWithContract("dci", "search", dataRecallAccessInternal, runtimeDataWriteContract{
		RequiredPayloadFields: []string{"query"}, OptionalPayloadFields: []string{"intent"},
	}, func(context.Context, toolsinfra.DataWriteRequest) (runtimeDataWriteOwnerResult, error) {
		return validRuntimeDataWriteOwnerResult(), nil
	}); err != nil {
		t.Fatal(err)
	}
	rendered := renderRuntimeDataRouteContext(recall, write)
	for _, want := range []string{
		"data.recall dci/search_traces access=internal request=query,limit",
		"data.write dci/search access=internal required=query optional=intent",
		"A route absent here is unavailable",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("route context missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "callback") || strings.Contains(rendered, "/srv/") {
		t.Fatalf("unsafe route context: %s", rendered)
	}
}

func TestDataCapabilityCatalogCoversDatabasePathsConfigFields(t *testing.T) {
	want := map[string]bool{}
	typ := reflect.TypeOf(config.DatabasePathsConfig{})
	for i := 0; i < typ.NumField(); i++ {
		key := typ.Field(i).Tag.Get("yaml")
		if key != "" && key != "-" {
			want[key] = true
		}
	}
	got := map[string]bool{}
	for _, key := range datacapability.KnownStoreKeys() {
		got[key] = true
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("catalog/config drift got=%v want=%v", got, want)
	}
}

func TestProductionRunnersAndSnapshotExposeDataCatalogAndGlossary(t *testing.T) {
	disabled := false
	cfg := &config.Config{WorkspaceDir: t.TempDir(), ToolHarness: config.ToolHarnessConfig{Enabled: &disabled, RecordEvents: &disabled}}
	cfg.Storage.Databases.Glossary = seedRuntimeGlossary(t, true)
	runtime := buildToolRuntimeWithCapabilities(cfg, nil, nil, nil, nil, nil)
	if runtime.DataCapabilityCatalog == nil {
		t.Fatal("tool runtime did not retain startup data capability catalog")
	}
	viewerEntries, err := runtime.DataCapabilityCatalog.Entries(context.Background())
	if err != nil || len(viewerEntries) != 20 {
		t.Fatalf("viewer catalog entries=%d err=%v", len(viewerEntries), err)
	}
	runners := []struct {
		name   string
		runner domaintool.RunnerV2
	}{{"chat", runtime.ChatRunnerV2}, {"worker", runtime.WorkerRunnerV2}}
	for _, item := range runners {
		metadata, err := item.runner.ListTools(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, toolID := range []string{"data_capability.describe", "glossary.lookup"} {
			if !hasToolMetadata(metadata, toolID) {
				t.Fatalf("%s missing %s", item.name, toolID)
			}
		}
		if resp, err := item.runner.ExecuteV2(context.Background(), "data_capability.describe", map[string]any{"operation": "describe", "name": "glossary"}); err != nil || resp.IsError() {
			t.Fatalf("%s catalog resp=%#v err=%v", item.name, resp, err)
		}
		if resp, err := item.runner.ExecuteV2(context.Background(), "glossary.lookup", map[string]any{"operation": "define_term", "term": "Go"}); err != nil || resp.IsError() {
			t.Fatalf("%s glossary resp=%#v err=%v", item.name, resp, err)
		}
	}
	metadata, _ := runtime.WorkerRunnerV2.ListTools(context.Background())
	snapshot := capdomain.Normalize(buildRuntimeCapabilitySnapshotWithSkills(metadata, nil, nil, nil))
	for _, toolID := range []string{"data_capability.describe", "glossary.lookup"} {
		entry, ok := findRuntimeCapability(snapshot, capdomain.CapabilityKindTool, toolID)
		if !ok || entry.Status != capdomain.CapabilityStatusAvailable {
			t.Fatalf("snapshot missing %s: %#v", toolID, snapshot.Entries)
		}
	}
}
