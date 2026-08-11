package tools

import (
	"context"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

type movieCatalogLookupStub struct {
	kind, name, information string
	limit                   int
}

func (s *movieCatalogLookupStub) Lookup(_ context.Context, kind, name, information string, limit int) (any, error) {
	s.kind, s.name, s.information, s.limit = kind, name, information, limit
	return map[string]any{"kind": kind, "name": name, "matches": 1}, nil
}

func TestMovieCatalogLookupMetadataAndExecution(t *testing.T) {
	lookup := &movieCatalogLookupStub{}
	runner := NewToolRunner(ToolRunnerConfig{MovieCatalogLookup: lookup, DisableToolHarness: true})
	metadata, err := runner.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var found *tool.ToolMetadata
	for i := range metadata {
		if metadata[i].ToolID == "movie_catalog.lookup" {
			found = &metadata[i]
			break
		}
	}
	if found == nil {
		t.Fatal("movie_catalog.lookup metadata missing")
	}
	if found.Category != "query" || found.Origin != tool.OriginCoreRuntime {
		t.Fatalf("unexpected metadata: %#v", found)
	}
	if found.Parameters["additionalProperties"] != false {
		t.Fatalf("schema must reject extra fields: %#v", found.Parameters)
	}
	properties := found.Parameters["properties"].(map[string]any)
	information := properties["information"].(map[string]any)
	if len(information["enum"].([]any)) != 5 {
		t.Fatalf("information enum missing: %#v", information)
	}

	response, err := runner.ExecuteV2(context.Background(), "movie_catalog.lookup", map[string]any{"kind": "person", "name": "Al Pacino", "information": "filmography", "limit": float64(7)})
	if err != nil || response == nil || response.IsError() {
		t.Fatalf("execution failed response=%#v err=%v", response, err)
	}
	if lookup.kind != "person" || lookup.name != "Al Pacino" || lookup.information != "filmography" || lookup.limit != 7 {
		t.Fatalf("unexpected adapter request: %#v", lookup)
	}
}

func TestMovieCatalogLookupRejectsInvalidAndArbitraryArguments(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{MovieCatalogLookup: &movieCatalogLookupStub{}, DisableToolHarness: true})
	requests := []map[string]any{
		{"kind": "actor", "name": "x"},
		{"kind": "person", "name": " "},
		{"kind": "movie", "name": "x", "limit": 21},
		{"kind": "movie", "name": "x", "information": "profile"},
		{"kind": "movie", "name": "x", "sql": "SELECT * FROM people"},
	}
	for _, args := range requests {
		response, err := runner.ExecuteV2(context.Background(), "movie_catalog.lookup", args)
		if err != nil {
			t.Fatal(err)
		}
		if response == nil || !response.IsError() || response.Error.Code != tool.ErrValidationFailed {
			t.Fatalf("expected validation error for %#v: %#v", args, response)
		}
	}
}

func TestMovieCatalogLookupIsUnregisteredWithoutDependency(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{DisableToolHarness: true})
	metadata, err := runner.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range metadata {
		if item.ToolID == "movie_catalog.lookup" {
			t.Fatal("movie catalog Tool must not be registered without dependency")
		}
	}
}
