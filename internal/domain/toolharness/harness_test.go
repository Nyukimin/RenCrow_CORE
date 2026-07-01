package toolharness

import (
	"reflect"
	"strings"
	"testing"
	"time"

	domaintool "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/tool"
)

func TestMediateValidInputUnchanged(t *testing.T) {
	h := New()
	raw := map[string]any{"path": "/tmp/a.txt", "limit": 10, "offset": 5}

	result := h.Mediate("file_read", raw)

	if !reflect.DeepEqual(result.Input, raw) {
		t.Fatalf("valid input changed: got %#v want %#v", result.Input, raw)
	}
	if result.Repaired() {
		t.Fatalf("valid input should not be marked repaired: %#v", result)
	}
}

func TestMediateOptionalNullOmission(t *testing.T) {
	h := New()

	result := h.Mediate("file_read", map[string]any{
		"path":   "/tmp/a.txt",
		"limit":  nil,
		"offset": nil,
	})

	if _, ok := result.Input["limit"]; ok {
		t.Fatal("limit should be omitted")
	}
	if _, ok := result.Input["offset"]; ok {
		t.Fatal("offset should be omitted")
	}
	if len(result.Repairs) != 2 {
		t.Fatalf("repairs = %d, want 2", len(result.Repairs))
	}
}

func TestMediateDoesNotOmitRequiredNull(t *testing.T) {
	h := New()

	result := h.Mediate("file_read", map[string]any{"path": nil})

	if _, ok := result.Input["path"]; !ok {
		t.Fatal("required null path should remain for validator to reject")
	}
}

func TestMediateJSONArrayStringBeforeBareStringWrap(t *testing.T) {
	h := New()

	result := h.Mediate("multi_file_read", map[string]any{
		"paths": `["a.md","b.md"]`,
	})

	paths, ok := result.Input["paths"].([]string)
	if !ok {
		t.Fatalf("paths type = %T, want []string", result.Input["paths"])
	}
	if !reflect.DeepEqual(paths, []string{"a.md", "b.md"}) {
		t.Fatalf("paths = %#v", paths)
	}
	if result.Repairs[0].Type != "json_array_string_parse" {
		t.Fatalf("first repair = %s", result.Repairs[0].Type)
	}
}

func TestMediateBareStringWrap(t *testing.T) {
	h := New()

	result := h.Mediate("multi_file_read", map[string]any{"paths": "a.md"})

	if !reflect.DeepEqual(result.Input["paths"], []string{"a.md"}) {
		t.Fatalf("paths = %#v", result.Input["paths"])
	}
}

func TestMediatePlaceholderUnwrap(t *testing.T) {
	h := New()

	result := h.Mediate("file_read", map[string]any{
		"args": map[string]any{"path": "/tmp/a.md"},
	})

	if result.Input["path"] != "/tmp/a.md" {
		t.Fatalf("path = %#v", result.Input["path"])
	}
}

func TestMediateWithCustomRegistry(t *testing.T) {
	h := NewWithRegistry(Registry{
		"custom_read": {
			RequiredFields:    []string{"target"},
			OptionalFields:    []string{"limit"},
			PathFields:        []string{"target"},
			ArrayStringFields: []string{"tags"},
		},
	})

	result := h.Mediate("custom_read", map[string]any{
		"input": map[string]any{
			"target": "/tmp/[notes.md](http://notes.md)",
			"limit":  nil,
			"tags":   `["a","b"]`,
		},
	})

	if result.Input["target"] != "/tmp/notes.md" {
		t.Fatalf("target = %#v", result.Input["target"])
	}
	if _, ok := result.Input["limit"]; ok {
		t.Fatal("limit should be omitted by custom optional field")
	}
	if !reflect.DeepEqual(result.Input["tags"], []string{"a", "b"}) {
		t.Fatalf("tags = %#v", result.Input["tags"])
	}
}

func TestRegistryFromManifestsDerivesToolSpecFromInputSchema(t *testing.T) {
	registry := RegistryFromManifests([]domaintool.ToolManifest{
		{
			ID:         "manifest_read",
			Version:    "1.0.0",
			SideEffect: domaintool.SideEffectNone,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []any{"target_path"},
				"properties": map[string]any{
					"target_path": map[string]any{"type": "string", "format": "path"},
					"labels":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"limit":       map[string]any{"type": "integer"},
				},
			},
		},
	})
	h := NewWithRegistry(registry)

	result := h.Mediate("manifest_read", map[string]any{
		"args": map[string]any{
			"target_path": "/tmp/[notes.md](http://notes.md)",
			"labels":      "alpha",
			"limit":       nil,
		},
	})

	if result.Input["target_path"] != "/tmp/notes.md" {
		t.Fatalf("target_path = %#v", result.Input["target_path"])
	}
	if !reflect.DeepEqual(result.Input["labels"], []string{"alpha"}) {
		t.Fatalf("labels = %#v", result.Input["labels"])
	}
	if _, ok := result.Input["limit"]; ok {
		t.Fatal("limit should be omitted as optional field")
	}
}

func TestMediateMarkdownPathUnwrapOnlyPathField(t *testing.T) {
	h := New()

	result := h.Mediate("file_write", map[string]any{
		"path":    "/tmp/[notes.md](http://notes.md)",
		"content": "[click](https://example.com)",
	})

	if result.Input["path"] != "/tmp/notes.md" {
		t.Fatalf("path = %#v", result.Input["path"])
	}
	if result.Input["content"] != "[click](https://example.com)" {
		t.Fatalf("content should not be repaired: %#v", result.Input["content"])
	}
}

func TestMediateFileReadLimitDefaultsOffset(t *testing.T) {
	h := New()

	result := h.Mediate("file_read", map[string]any{"path": "/tmp/a.md", "limit": 30})

	if result.Input["offset"] != 0 {
		t.Fatalf("offset = %#v, want 0", result.Input["offset"])
	}
	if len(result.RelationDefaults) != 1 {
		t.Fatalf("relation defaults = %#v", result.RelationDefaults)
	}
}

func TestMediateFileReadOffsetDefaultsLimit(t *testing.T) {
	h := New()

	result := h.Mediate("file_read", map[string]any{"path": "/tmp/a.md", "offset": 10})

	if result.Input["limit"] != 2000 {
		t.Fatalf("limit = %#v, want 2000", result.Input["limit"])
	}
}

func TestBuildRetryMessage(t *testing.T) {
	msg := BuildRetryMessage("file_read", []string{"field path expected string"}, map[string]any{"path": "docs/example.md"})

	if !strings.Contains(msg, "Tool input was invalid for file_read.") {
		t.Fatalf("missing header: %s", msg)
	}
	if !strings.Contains(msg, `"path": "docs/example.md"`) {
		t.Fatalf("missing example: %s", msg)
	}
}

func TestResultMetadataIncludesRepairsAndRelationDefaults(t *testing.T) {
	result := Result{
		Input: map[string]any{"path": "docs/a.md", "limit": 10, "offset": 0},
		Repairs: []Repair{{
			Type:       "markdown_autolink_path_unwrap",
			Path:       []string{"path"},
			BeforeType: "string",
			AfterType:  "string",
			Note:       "unwrapped markdown link in path field",
		}},
		RelationDefaults: []RelationDefault{{
			Field:  "offset",
			Value:  0,
			Reason: "limit was provided without offset",
		}},
	}

	meta := result.Metadata()
	if meta["tool_harness_status"] != "repaired" {
		t.Fatalf("status = %#v", meta["tool_harness_status"])
	}
	repairs, ok := meta["repairs_applied"].([]map[string]any)
	if !ok || len(repairs) != 1 || repairs[0]["type"] != "markdown_autolink_path_unwrap" {
		t.Fatalf("repairs metadata mismatch: %#v", meta["repairs_applied"])
	}
	defaults, ok := meta["relation_defaults_applied"].([]map[string]any)
	if !ok || len(defaults) != 1 || defaults[0]["field"] != "offset" {
		t.Fatalf("defaults metadata mismatch: %#v", meta["relation_defaults_applied"])
	}
}

func TestResultMetadataNilWhenUnchanged(t *testing.T) {
	result := Result{Input: map[string]any{"path": "docs/a.md"}}
	if result.Repaired() {
		t.Fatal("unchanged result should not be repaired")
	}
	if meta := result.Metadata(); meta != nil {
		t.Fatalf("metadata = %#v, want nil", meta)
	}
}

func TestNewEventAndHashInput(t *testing.T) {
	raw := map[string]any{"path": "docs/[a.md](https://example.com/a.md)", "limit": 10}
	result := New().Mediate("file_read", raw)
	now := time.Date(2026, 6, 12, 2, 0, 0, 123, time.FixedZone("JST", 9*60*60))

	event := NewEvent("evt_1", "file_read", raw, result, now)
	if event.EventID != "evt_1" || event.ToolName != "file_read" {
		t.Fatalf("event identity mismatch: %#v", event)
	}
	if event.ValidationStatus != ValidationStatusRepaired {
		t.Fatalf("status = %s", event.ValidationStatus)
	}
	if event.RawInputHash == "" || !strings.HasPrefix(event.RawInputHash, "sha256:") {
		t.Fatalf("hash = %q", event.RawInputHash)
	}
	if !event.CreatedAt.Equal(now.UTC()) {
		t.Fatalf("created_at = %s want %s", event.CreatedAt, now.UTC())
	}
	if len(event.Repairs) == 0 && len(event.RelationDefaults) == 0 {
		t.Fatalf("expected repair evidence: %#v", event)
	}

	sameHash := HashInput(map[string]any{"limit": 10, "path": "docs/[a.md](https://example.com/a.md)"})
	if event.RawInputHash != sameHash {
		t.Fatalf("hash should be deterministic: %s vs %s", event.RawInputHash, sameHash)
	}
}

func TestHashInputReturnsEmptyForUnmarshalableInput(t *testing.T) {
	got := HashInput(map[string]any{"bad": func() {}})
	if got != "" {
		t.Fatalf("hash = %q, want empty for unmarshalable input", got)
	}
}

func TestRegistryFromManifestsSkipsEmptyIDAndDerivesSchemaKinds(t *testing.T) {
	registry := RegistryFromManifests([]domaintool.ToolManifest{
		{
			Version:    "1.0.0",
			SideEffect: domaintool.SideEffectNone,
			InputSchema: map[string]any{
				"required": []any{"ignored"},
			},
		},
		{
			ID:         "manifest_custom",
			Version:    "1.0.0",
			SideEffect: domaintool.SideEffectNone,
			InputSchema: map[string]any{
				"required": []string{"config_path"},
				"properties": map[string]any{
					"config_path": map[string]any{"type": "string"},
					"artifact":    map[string]any{"type": "string", "x-rencrow-type": "path"},
					"names":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"ignored":     "not-a-map",
				},
			},
		},
	})
	if _, ok := registry[""]; ok {
		t.Fatal("empty manifest id should be skipped")
	}
	spec := registry["manifest_custom"]
	if !reflect.DeepEqual(spec.RequiredFields, []string{"config_path"}) {
		t.Fatalf("required fields = %#v", spec.RequiredFields)
	}
	if !sameStringSet(spec.PathFields, []string{"artifact", "config_path"}) {
		t.Fatalf("path fields = %#v", spec.PathFields)
	}
	if !reflect.DeepEqual(spec.ArrayStringFields, []string{"names"}) {
		t.Fatalf("array string fields = %#v", spec.ArrayStringFields)
	}
}

func sameStringSet(got []string, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := make(map[string]int, len(got))
	for _, item := range got {
		seen[item]++
	}
	for _, item := range want {
		if seen[item] == 0 {
			return false
		}
		seen[item]--
	}
	return true
}
