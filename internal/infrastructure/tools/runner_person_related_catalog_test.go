package tools

import (
	"context"
	"errors"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

type personRelatedCatalogLookupStub struct {
	personName string
	category   string
	limit      int
	result     any
	err        error
}

func (s *personRelatedCatalogLookupStub) Lookup(_ context.Context, personName, category string, limit int) (any, error) {
	s.personName, s.category, s.limit = personName, category, limit
	return s.result, s.err
}

func TestPersonRelatedCatalogLookupMetadataAndExecution(t *testing.T) {
	lookup := &personRelatedCatalogLookupStub{result: map[string]any{"items": []any{"x"}}}
	runner := NewToolRunner(ToolRunnerConfig{PersonRelatedCatalogLookup: lookup, DisableToolHarness: true})
	metadata, err := runner.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var found *tool.ToolMetadata
	for i := range metadata {
		if metadata[i].ToolID == "person_related_catalog.lookup" {
			found = &metadata[i]
			break
		}
	}
	if found == nil {
		t.Fatal("person_related_catalog.lookup metadata missing")
	}
	if found.Category != "query" || found.Origin != tool.OriginCoreRuntime {
		t.Fatalf("unexpected metadata: %#v", found)
	}
	if found.Parameters["additionalProperties"] != false {
		t.Fatalf("schema must reject extra fields: %#v", found.Parameters)
	}
	properties := found.Parameters["properties"].(map[string]any)
	category := properties["category"].(map[string]any)
	if got := category["enum"].([]any); len(got) != 7 {
		t.Fatalf("category enum missing: %#v", category)
	}
	if got := properties["limit"].(map[string]any); got["minimum"] != 1 || got["maximum"] != 50 {
		t.Fatalf("unexpected limit contract: %#v", got)
	}

	response, err := runner.ExecuteV2(context.Background(), "person_related_catalog.lookup", map[string]any{
		"person_name": "Al Pacino",
		"category":    "drama",
		"limit":       float64(7),
	})
	if err != nil || response == nil || response.IsError() {
		t.Fatalf("execution failed response=%#v err=%v", response, err)
	}
	if lookup.personName != "Al Pacino" || lookup.category != "drama" || lookup.limit != 7 {
		t.Fatalf("unexpected adapter request: %#v", lookup)
	}

	response, err = runner.ExecuteV2(context.Background(), "person_related_catalog.lookup", map[string]any{
		"person_name": "Al Pacino",
		"category":    "drama",
	})
	if err != nil || response == nil || response.IsError() || lookup.limit != 20 {
		t.Fatalf("default limit failed response=%#v err=%v lookup=%#v", response, err, lookup)
	}
}

func TestPersonRelatedCatalogLookupRejectsInvalidAndArbitraryArguments(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{PersonRelatedCatalogLookup: &personRelatedCatalogLookupStub{}, DisableToolHarness: true})
	requests := []map[string]any{
		{"person_name": "x", "category": "person"},
		{"person_name": " ", "category": "drama"},
		{"person_name": "x", "category": "drama", "limit": 51},
		{"person_name": "x", "category": "drama", "limit": 1.5},
		{"person_name": "x", "category": "drama", "sql": "SELECT * FROM hobby_related_items"},
		{"person_name": "x", "category": "drama", "path": "/tmp/hobby.sqlite"},
	}
	for _, args := range requests {
		response, err := runner.ExecuteV2(context.Background(), "person_related_catalog.lookup", args)
		if err != nil {
			t.Fatal(err)
		}
		if response == nil || !response.IsError() || response.Error.Code != tool.ErrValidationFailed {
			t.Fatalf("expected validation error for %#v: %#v", args, response)
		}
	}
}

func TestPersonRelatedCatalogLookupMapsNotFoundAndAmbiguous(t *testing.T) {
	lookup := &personRelatedCatalogLookupStub{err: &PersonRelatedCatalogNotFoundError{PersonName: "Nobody"}}
	runner := NewToolRunner(ToolRunnerConfig{PersonRelatedCatalogLookup: lookup, DisableToolHarness: true})
	response, err := runner.ExecuteV2(context.Background(), "person_related_catalog.lookup", map[string]any{"person_name": "Nobody", "category": "drama"})
	if err != nil || response == nil || !response.IsError() || response.Error.Code != tool.ErrNotFound {
		t.Fatalf("expected not found response=%#v err=%v", response, err)
	}

	lookup.err = &PersonRelatedCatalogAmbiguousError{Candidates: []PersonRelatedCatalogCandidate{
		{PersonID: "p1", Name: "Same", URL: "https://example.test/p1"},
		{PersonID: "p2", Name: "Same", URL: "https://example.test/p2"},
	}}
	response, err = runner.ExecuteV2(context.Background(), "person_related_catalog.lookup", map[string]any{"person_name": "Same", "category": "drama"})
	if err != nil || response == nil || !response.IsError() || response.Error.Code != tool.ErrConflict {
		t.Fatalf("expected ambiguous response=%#v err=%v", response, err)
	}
	if response.Error.Details["status"] != "ambiguous" {
		t.Fatalf("ambiguous status missing: %#v", response.Error.Details)
	}
	if _, ok := response.Error.Details["candidates"].([]PersonRelatedCatalogCandidate); !ok {
		t.Fatalf("ambiguous candidates missing: %#v", response.Error.Details)
	}
}

func TestPersonRelatedCatalogLookupMapsUnavailableAsInternal(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{
		PersonRelatedCatalogLookup: &personRelatedCatalogLookupStub{err: errors.New("schema is unavailable")},
		DisableToolHarness:         true,
	})
	response, err := runner.ExecuteV2(context.Background(), "person_related_catalog.lookup", map[string]any{"person_name": "x", "category": "drama"})
	if err != nil || response == nil || !response.IsError() || response.Error.Code != tool.ErrInternalError {
		t.Fatalf("expected internal response=%#v err=%v", response, err)
	}
}

func TestPersonRelatedCatalogLookupIsUnregisteredWithoutDependency(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{DisableToolHarness: true})
	metadata, err := runner.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range metadata {
		if item.ToolID == "person_related_catalog.lookup" {
			t.Fatal("person related catalog Tool must not be registered without dependency")
		}
	}
}
