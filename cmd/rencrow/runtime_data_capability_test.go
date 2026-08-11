package main

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/datacapability"
	capdomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
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
