package tools

import (
	"context"
	"errors"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

type personRelatedCatalogCollectorStub struct {
	personName string
	category   string
	result     any
	err        error
}

func (s *personRelatedCatalogCollectorStub) Collect(_ context.Context, personName, category string) (any, error) {
	s.personName, s.category = personName, category
	return s.result, s.err
}

func TestPersonRelatedCatalogCollectMetadataAndExecution(t *testing.T) {
	collector := &personRelatedCatalogCollectorStub{result: map[string]any{"status": "success"}}
	runner := NewToolRunner(ToolRunnerConfig{PersonRelatedCatalogCollector: collector, DisableToolHarness: true})
	metadata, err := runner.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var found *tool.ToolMetadata
	for i := range metadata {
		if metadata[i].ToolID == "person_related_catalog.collect" {
			found = &metadata[i]
			break
		}
	}
	if found == nil || found.Category != "mutation" {
		t.Fatalf("collect Tool metadata missing or not mutation: %#v", found)
	}
	if found.Parameters["additionalProperties"] != false {
		t.Fatalf("collect schema must reject extra fields: %#v", found.Parameters)
	}
	properties := found.Parameters["properties"].(map[string]any)
	if len(properties) != 2 || len(properties["category"].(map[string]any)["enum"].([]any)) != 6 {
		t.Fatalf("unexpected collect parameters: %#v", found.Parameters)
	}

	response, err := runner.ExecuteV2(context.Background(), "person_related_catalog.collect", map[string]any{"person_name": "役所広司", "category": "drama"})
	if err != nil || response == nil || response.IsError() {
		t.Fatalf("collect execution failed response=%#v err=%v", response, err)
	}
	if collector.personName != "役所広司" || collector.category != "drama" {
		t.Fatalf("unexpected collector request: %#v", collector)
	}
}

func TestPersonRelatedCatalogCollectRejectsMovieLimitAndArbitraryArguments(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{PersonRelatedCatalogCollector: &personRelatedCatalogCollectorStub{}, DisableToolHarness: true})
	for _, args := range []map[string]any{
		{"person_name": "役所広司", "category": "movie"},
		{"person_name": "役所広司", "category": "drama", "limit": 1},
		{"person_name": "役所広司", "category": "drama", "sql": "SELECT 1"},
		{"person_name": "", "category": "drama"},
	} {
		response, err := runner.ExecuteV2(context.Background(), "person_related_catalog.collect", args)
		if err != nil || response == nil || !response.IsError() || response.Error.Code != tool.ErrValidationFailed {
			t.Fatalf("expected validation error for %#v: response=%#v err=%v", args, response, err)
		}
	}
}

func TestPersonRelatedCatalogCollectMapsProviderError(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{
		PersonRelatedCatalogCollector: &personRelatedCatalogCollectorStub{err: errors.New("provider failed")},
		DisableToolHarness:            true,
	})
	response, err := runner.ExecuteV2(context.Background(), "person_related_catalog.collect", map[string]any{"person_name": "役所広司", "category": "drama"})
	if err != nil || response == nil || !response.IsError() || response.Error.Code != tool.ErrInternalError {
		t.Fatalf("expected structured provider error: response=%#v err=%v", response, err)
	}
}

func TestPersonRelatedCatalogCollectIsUnregisteredWithoutCollector(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{DisableToolHarness: true})
	metadata, err := runner.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range metadata {
		if item.ToolID == "person_related_catalog.collect" {
			t.Fatal("collect Tool must not be registered without collector")
		}
	}
}
