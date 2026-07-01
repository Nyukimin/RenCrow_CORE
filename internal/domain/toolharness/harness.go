package toolharness

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	domaintool "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/tool"
)

type Repair struct {
	Type       string
	Path       []string
	BeforeType string
	AfterType  string
	Note       string
}

type RelationDefault struct {
	Field  string
	Value  any
	Reason string
}

type Result struct {
	Input            map[string]any
	Repairs          []Repair
	RelationDefaults []RelationDefault
}

type ToolSpec struct {
	RequiredFields    []string
	OptionalFields    []string
	PathFields        []string
	ArrayStringFields []string
}

type Registry map[string]ToolSpec

type Harness struct {
	registry Registry
}

type ValidationStatus string

const (
	ValidationStatusValid    ValidationStatus = "valid"
	ValidationStatusRepaired ValidationStatus = "repaired"
)

type Event struct {
	EventID          string            `json:"event_id"`
	ToolName         string            `json:"tool_name"`
	RawInputHash     string            `json:"raw_input_hash"`
	ValidationStatus ValidationStatus  `json:"validation_status"`
	Repairs          []Repair          `json:"repairs_applied,omitempty"`
	RelationDefaults []RelationDefault `json:"relation_defaults_applied,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
}

type Recorder interface {
	RecordToolMediationEvent(event Event) error
}

func New() *Harness {
	return NewWithRegistry(DefaultRegistry())
}

func NewWithRegistry(registry Registry) *Harness {
	if registry == nil {
		registry = Registry{}
	}
	return &Harness{registry: registry}
}

func DefaultRegistry() Registry {
	return Registry{
		"shell": {
			RequiredFields: []string{"command"},
			OptionalFields: []string{"mode"},
		},
		"file_read": {
			RequiredFields: []string{"path"},
			OptionalFields: []string{"offset", "limit", "mode"},
			PathFields:     []string{"path"},
		},
		"file_write": {
			RequiredFields: []string{"path", "content"},
			OptionalFields: []string{"mode"},
			PathFields:     []string{"path"},
		},
		"file_list": {
			RequiredFields: []string{"path"},
			OptionalFields: []string{"offset", "limit"},
			PathFields:     []string{"path"},
		},
		"web_search": {
			RequiredFields: []string{"query"},
		},
		"web_gather.fetch": {
			RequiredFields: []string{"url"},
			OptionalFields: []string{"fetch_provider", "extractor", "namespace", "source_id", "store_staging", "refresh", "policy"},
		},
		"web_gather.search": {
			RequiredFields: []string{"query"},
			OptionalFields: []string{"provider", "limit", "language", "freshness", "namespace", "refresh"},
		},
		"web_gather.search_and_fetch": {
			RequiredFields: []string{"query"},
			OptionalFields: []string{"provider", "limit", "max_fetches", "language", "freshness", "namespace", "refresh", "fetch_provider", "extractor", "store_staging", "policy"},
		},
		"subagent": {
			RequiredFields: []string{"agent", "message"},
		},
		"multi_file_read": {
			RequiredFields:    []string{"paths"},
			ArrayStringFields: []string{"paths"},
		},
		"shell_batch": {
			RequiredFields:    []string{"commands"},
			ArrayStringFields: []string{"commands"},
		},
	}
}

func RegistryFromManifests(manifests []domaintool.ToolManifest) Registry {
	registry := make(Registry, len(manifests))
	for _, manifest := range manifests {
		if manifest.ID == "" {
			continue
		}
		registry[manifest.ID] = ToolSpecFromManifest(manifest)
	}
	return registry
}

func ToolSpecFromManifest(manifest domaintool.ToolManifest) ToolSpec {
	schema := manifest.InputSchema
	required := schemaStringList(schema["required"])
	requiredSet := stringSet(required)
	properties := schemaMap(schema["properties"])
	spec := ToolSpec{
		RequiredFields: required,
	}
	for field, raw := range properties {
		if !requiredSet[field] {
			spec.OptionalFields = append(spec.OptionalFields, field)
		}
		prop := schemaMap(raw)
		if isPathSchemaField(field, prop) {
			spec.PathFields = append(spec.PathFields, field)
		}
		if isArrayStringSchemaField(prop) {
			spec.ArrayStringFields = append(spec.ArrayStringFields, field)
		}
	}
	return spec
}

func (h *Harness) Mediate(toolName string, raw map[string]any) Result {
	input := cloneMap(raw)
	if input == nil {
		input = map[string]any{}
	}

	result := Result{Input: input}
	spec := h.spec(toolName)
	result.applyPlaceholderUnwrap(spec)
	result.applyOptionalNullOmission(spec)
	result.applyMarkdownPathUnwrap(spec)
	result.applyJSONArrayStringParse(spec)
	result.applyBareStringWrap(spec)
	result.applyRelationDefaults(toolName)
	return result
}

func (h *Harness) spec(toolName string) ToolSpec {
	if h == nil || h.registry == nil {
		return ToolSpec{}
	}
	return h.registry[toolName]
}

func (r *Result) Repaired() bool {
	return len(r.Repairs) > 0 || len(r.RelationDefaults) > 0
}

func (r *Result) Metadata() map[string]any {
	if !r.Repaired() {
		return nil
	}
	meta := map[string]any{
		"tool_harness_status": "repaired",
	}
	if len(r.Repairs) > 0 {
		repairs := make([]map[string]any, 0, len(r.Repairs))
		for _, repair := range r.Repairs {
			repairs = append(repairs, map[string]any{
				"type":        repair.Type,
				"path":        repair.Path,
				"before_type": repair.BeforeType,
				"after_type":  repair.AfterType,
				"note":        repair.Note,
			})
		}
		meta["repairs_applied"] = repairs
	}
	if len(r.RelationDefaults) > 0 {
		defaults := make([]map[string]any, 0, len(r.RelationDefaults))
		for _, def := range r.RelationDefaults {
			defaults = append(defaults, map[string]any{
				"field":  def.Field,
				"value":  def.Value,
				"reason": def.Reason,
			})
		}
		meta["relation_defaults_applied"] = defaults
	}
	return meta
}

func (r *Result) addRepair(kind string, p []string, before any, after any, note string) {
	r.Repairs = append(r.Repairs, Repair{
		Type:       kind,
		Path:       p,
		BeforeType: typeName(before),
		AfterType:  typeName(after),
		Note:       note,
	})
}

func (r *Result) applyPlaceholderUnwrap(spec ToolSpec) {
	if len(r.Input) != 1 {
		return
	}
	var wrapper string
	var value any
	for k, v := range r.Input {
		wrapper = k
		value = v
	}
	if !isWrapperKey(wrapper) {
		return
	}
	inner, ok := value.(map[string]any)
	if !ok {
		return
	}
	if !hasAnyRequiredField(spec, inner) {
		return
	}
	before := r.Input
	r.Input = cloneMap(inner)
	r.addRepair("empty_placeholder_object_unwrap", []string{wrapper}, before, r.Input, "unwrapped single arguments object")
}

func (r *Result) applyOptionalNullOmission(spec ToolSpec) {
	optional := stringSet(spec.OptionalFields)
	for field, value := range r.Input {
		if value == nil && optional[field] {
			delete(r.Input, field)
			r.addRepair("optional_null_omission", []string{field}, nil, "(omitted)", "removed null optional field")
		}
	}
}

func (r *Result) applyMarkdownPathUnwrap(spec ToolSpec) {
	for _, field := range spec.PathFields {
		value, ok := r.Input[field].(string)
		if !ok {
			continue
		}
		unwrapped, changed := unwrapMarkdownPath(value)
		if changed {
			r.Input[field] = unwrapped
			r.addRepair("markdown_autolink_path_unwrap", []string{field}, value, unwrapped, "unwrapped markdown link in path field")
		}
	}
}

func (r *Result) applyJSONArrayStringParse(spec ToolSpec) {
	for _, field := range spec.ArrayStringFields {
		value, ok := r.Input[field].(string)
		if !ok {
			continue
		}
		var parsed []string
		if err := json.Unmarshal([]byte(value), &parsed); err != nil {
			continue
		}
		r.Input[field] = parsed
		r.addRepair("json_array_string_parse", []string{field}, value, parsed, "parsed JSON array string")
	}
}

func (r *Result) applyBareStringWrap(spec ToolSpec) {
	for _, field := range spec.ArrayStringFields {
		value, ok := r.Input[field].(string)
		if !ok {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(value), "[") {
			continue
		}
		wrapped := []string{value}
		r.Input[field] = wrapped
		r.addRepair("bare_string_wrap", []string{field}, value, wrapped, "wrapped bare string as single-item array")
	}
}

func (r *Result) applyRelationDefaults(toolName string) {
	if toolName != "file_read" {
		return
	}
	_, hasOffset := r.Input["offset"]
	_, hasLimit := r.Input["limit"]
	if hasLimit && !hasOffset {
		r.Input["offset"] = 0
		r.RelationDefaults = append(r.RelationDefaults, RelationDefault{
			Field:  "offset",
			Value:  0,
			Reason: "limit was provided without offset",
		})
		return
	}
	if hasOffset && !hasLimit {
		r.Input["limit"] = 2000
		r.RelationDefaults = append(r.RelationDefaults, RelationDefault{
			Field:  "limit",
			Value:  2000,
			Reason: "offset was provided without limit",
		})
	}
}

func BuildRetryMessage(toolName string, problems []string, example map[string]any) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Tool input was invalid for %s.\n\n", toolName)
	b.WriteString("Problems:\n")
	for i, p := range problems {
		fmt.Fprintf(&b, "%d. %s\n", i+1, p)
	}
	if len(example) > 0 {
		if data, err := json.MarshalIndent(example, "", "  "); err == nil {
			b.WriteString("\nRetry using:\n")
			b.Write(data)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func NewEvent(eventID string, toolName string, raw map[string]any, result Result, now time.Time) Event {
	status := ValidationStatusValid
	if result.Repaired() {
		status = ValidationStatusRepaired
	}
	return Event{
		EventID:          eventID,
		ToolName:         toolName,
		RawInputHash:     HashInput(raw),
		ValidationStatus: status,
		Repairs:          result.Repairs,
		RelationDefaults: result.RelationDefaults,
		CreatedAt:        now.UTC(),
	}
}

func HashInput(input map[string]any) string {
	data, err := json.Marshal(input)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", sum[:])
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	out := make(map[string]any, len(input))
	for k, v := range input {
		if nested, ok := v.(map[string]any); ok {
			out[k] = cloneMap(nested)
			continue
		}
		out[k] = v
	}
	return out
}

func typeName(value any) string {
	if value == nil {
		return "null"
	}
	return fmt.Sprintf("%T", value)
}

func isWrapperKey(key string) bool {
	switch key {
	case "args", "input", "params", "arguments":
		return true
	default:
		return false
	}
}

func stringSet(items []string) map[string]bool {
	out := make(map[string]bool, len(items))
	for _, item := range items {
		out[item] = true
	}
	return out
}

func schemaMap(value any) map[string]any {
	if value == nil {
		return nil
	}
	if m, ok := value.(map[string]any); ok {
		return m
	}
	return nil
}

func schemaStringList(value any) []string {
	items, ok := value.([]any)
	if !ok {
		if typed, ok := value.([]string); ok {
			return append([]string(nil), typed...)
		}
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func isPathSchemaField(field string, prop map[string]any) bool {
	if prop == nil {
		return false
	}
	if fmt.Sprint(prop["format"]) == "path" || fmt.Sprint(prop["x-rencrow-type"]) == "path" {
		return true
	}
	lower := strings.ToLower(field)
	return lower == "path" || strings.HasSuffix(lower, "_path") || strings.HasSuffix(lower, "path")
}

func isArrayStringSchemaField(prop map[string]any) bool {
	if prop == nil || fmt.Sprint(prop["type"]) != "array" {
		return false
	}
	items := schemaMap(prop["items"])
	return items != nil && fmt.Sprint(items["type"]) == "string"
}

func hasAnyRequiredField(spec ToolSpec, input map[string]any) bool {
	for _, field := range spec.RequiredFields {
		if _, ok := input[field]; ok {
			return true
		}
	}
	return false
}

var markdownPathRE = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)

func unwrapMarkdownPath(value string) (string, bool) {
	match := markdownPathRE.FindStringSubmatchIndex(value)
	if match == nil {
		return value, false
	}
	linkText := value[match[2]:match[3]]
	rawURL := value[match[4]:match[5]]
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" {
		return value, false
	}
	urlBase := path.Base(parsed.Path)
	if urlBase == "." || urlBase == "/" {
		urlBase = path.Base(parsed.Hostname())
	}
	if urlBase != path.Base(linkText) {
		return value, false
	}
	out := value[:match[0]] + linkText + value[match[1]:]
	return out, true
}
