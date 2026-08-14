package capability

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

func TestParseToolDefinitionCanonicalFull(t *testing.T) {
	entry := ToolEntry{
		Name:        "report_tool",
		Description: "report tool",
		SchemaJSON:  `{"Type":"function","Function":{"Name":"report_tool","Description":"report tool","Parameters":{"type":"object"}}}`,
	}
	definition, err := ParseToolDefinition(entry)
	if err != nil {
		t.Fatalf("ParseToolDefinition() error = %v", err)
	}
	if definition.Type != "function" || definition.Function.Name != entry.Name || definition.Function.Description != entry.Description || !reflect.DeepEqual(definition.Function.Parameters, map[string]any{"type": "object"}) {
		t.Fatalf("definition = %+v", definition)
	}
}

func TestParseToolDefinitionLegacyWrapsTrustedMetadata(t *testing.T) {
	entry := ToolEntry{
		Name:        "legacy_tool",
		Description: "trusted legacy tool",
		SchemaJSON:  `{"type":"object","properties":{"query":{"type":"string"}}}`,
	}
	definition, err := ParseToolDefinition(entry)
	if err != nil {
		t.Fatalf("ParseToolDefinition() error = %v", err)
	}
	wantParameters := map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}}
	if definition.Type != "function" || definition.Function.Name != entry.Name || definition.Function.Description != entry.Description || !reflect.DeepEqual(definition.Function.Parameters, wantParameters) {
		t.Fatalf("definition = %+v", definition)
	}
}

func TestParseToolDefinitionRejectsInvalidOrAmbiguousEntries(t *testing.T) {
	tests := []ToolEntry{
		{Name: "broken", Description: "broken", SchemaJSON: "not json"},
		{Name: "description_mismatch", Description: "trusted", SchemaJSON: `{"type":"function","function":{"name":"description_mismatch","description":"schema","parameters":{}}}`},
		{Name: "wrong_type", Description: "wrong", SchemaJSON: `{"type":"object","function":{"name":"wrong_type","description":"wrong","parameters":{}}}`},
		{Name: "wrong_name", Description: "wrong", SchemaJSON: `{"type":"function","function":{"name":"other","description":"wrong","parameters":{}}}`},
		{Name: "partial", Description: "partial", SchemaJSON: `{"type":"function"}`},
		{Name: "nil_parameters", Description: "nil", SchemaJSON: `{"type":"function","function":{"name":"nil_parameters","description":"nil","parameters":null}}`},
		{Name: "empty_description", Description: " ", SchemaJSON: `{"type":"object"}`},
		{Name: "padded_description", Description: " description ", SchemaJSON: `{"type":"object"}`},
		{Name: "padded_name ", Description: "name", SchemaJSON: `{"type":"object"}`},
		{Name: " ", Description: "name", SchemaJSON: `{"type":"object"}`},
		{Name: "array", Description: "array", SchemaJSON: `[]`},
	}
	for _, entry := range tests {
		if definition, err := ParseToolDefinition(entry); err == nil {
			t.Errorf("entry %+v parsed as %+v, want rejection", entry, definition)
		}
	}
}

func TestParseToolDefinitionCanonicalJSONRoundTrip(t *testing.T) {
	entry := ToolEntry{Name: "tool", Description: "description", SchemaJSON: `{"type":"function","function":{"name":"tool","description":"description","parameters":{"type":"object"}}}`}
	definition, err := ParseToolDefinition(entry)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	var decoded llm.ToolDefinition
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Function.Name != "tool" || decoded.Function.Parameters["type"] != "object" {
		t.Fatalf("round-tripped definition = %+v", decoded)
	}
}
