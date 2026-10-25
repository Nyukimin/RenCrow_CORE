package coderloop

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseCoderMessageVariants(t *testing.T) {
	tests := []struct {
		name    string
		content string
		assert  func(t *testing.T, msg *CoderMessage)
	}{
		{
			name:    "read request with surrounding whitespace",
			content: " \n {\"type\":\"read_request\",\"actions\":[{\"action\":\"shell_command\",\"target\":\"rg TODO\"}]} \t ",
			assert: func(t *testing.T, msg *CoderMessage) {
				t.Helper()
				if msg.ReadRequest == nil || len(msg.ReadRequest.Actions) != 1 {
					t.Fatalf("read request not parsed: %#v", msg)
				}
			},
		},
		{
			name:    "plan",
			content: `{"type":"plan","task_summary":"split config","steps":["read","edit"],"risk":["low"]}`,
			assert: func(t *testing.T, msg *CoderMessage) {
				t.Helper()
				if msg.Plan == nil || msg.Plan.TaskSummary != "split config" {
					t.Fatalf("plan not parsed: %#v", msg)
				}
			},
		},
		{
			name:    "patch proposal",
			content: `{"type":"patch_proposal","intent":"fix","patch":"[{\"type\":\"file_edit\",\"action\":\"create\",\"target\":\"src/main.go\",\"content\":\"package main\"}]","tests":["go test ./..."],"test_hint":{"changed_surface":["internal/domain"],"expected_impact":"domain contract","recommended_tier":"related","recommended_steps":["go test ./internal/domain/..."],"risk":"medium"}}`,
			assert: func(t *testing.T, msg *CoderMessage) {
				t.Helper()
				if msg.PatchProposal == nil || msg.PatchProposal.Intent != "fix" || msg.PatchProposal.TestHint == nil || msg.PatchProposal.TestHint.RecommendedTestTier != "related" {
					t.Fatalf("patch proposal not parsed: %#v", msg)
				}
			},
		},
		{
			name:    "test request",
			content: `{"type":"test_request","actions":[{"action":"shell_command","target":"go test ./internal/domain/..."}]}`,
			assert: func(t *testing.T, msg *CoderMessage) {
				t.Helper()
				if msg.TestRequest == nil || msg.TestRequest.Actions[0].Target == "" {
					t.Fatalf("test request not parsed: %#v", msg)
				}
			},
		},
		{
			name:    "revision request",
			content: `{"type":"revision_request","reason":"test failed","actions":[{"action":"shell_command","target":"go test"}]}`,
			assert: func(t *testing.T, msg *CoderMessage) {
				t.Helper()
				if msg.RevisionRequest == nil || msg.RevisionRequest.Reason != "test failed" {
					t.Fatalf("revision request not parsed: %#v", msg)
				}
			},
		},
		{
			name:    "final report",
			content: `{"type":"final_report","summary":"done","changed_files":["a.go"],"tests_run":["go test"],"remaining_risks":["none"]}`,
			assert: func(t *testing.T, msg *CoderMessage) {
				t.Helper()
				if msg.FinalReport == nil || msg.FinalReport.Summary != "done" {
					t.Fatalf("final report not parsed: %#v", msg)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, err := ParseCoderMessage(tt.content)
			if err != nil {
				t.Fatalf("ParseCoderMessage failed: %v", err)
			}
			if msg.Raw == "" || msg.Type == "" {
				t.Fatalf("raw/type not populated: %#v", msg)
			}
			tt.assert(t, msg)
		})
	}
}

func TestPatchProposalTestHintJSONContract(t *testing.T) {
	proposal := PatchProposalMessage{
		Type:   TypePatchProposal,
		Intent: "contract",
		Patch:  "[]",
		TestHint: &TestHint{
			ChangedSurface:       []string{"internal/domain"},
			ExpectedImpact:       "domain contract",
			RecommendedTestTier:  "related",
			RecommendedTestSteps: []string{"go test ./internal/domain/..."},
			Risk:                 "medium",
		},
	}

	encoded, err := json.Marshal(proposal)
	if err != nil {
		t.Fatalf("marshal patch proposal: %v", err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatalf("decode patch proposal: %v", err)
	}
	var hint map[string]json.RawMessage
	if err := json.Unmarshal(object["test_hint"], &hint); err != nil {
		t.Fatalf("decode test_hint: %v", err)
	}
	for _, key := range []string{"changed_surface", "expected_impact", "recommended_tier", "recommended_steps", "risk"} {
		if _, ok := hint[key]; !ok {
			t.Fatalf("test_hint missing %q: %s", key, encoded)
		}
	}
	if len(hint) != 5 {
		t.Fatalf("test_hint contains unexpected fields: %#v", hint)
	}

	var roundTrip PatchProposalMessage
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatalf("unmarshal patch proposal: %v", err)
	}
	if roundTrip.TestHint == nil || roundTrip.TestHint.RecommendedTestTier != "related" || len(roundTrip.TestHint.RecommendedTestSteps) != 1 {
		t.Fatalf("test_hint round trip mismatch: %#v", roundTrip.TestHint)
	}

	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate coderloop test source")
	}
	schemaPath := filepath.Join(filepath.Dir(sourceFile), "..", "..", "..", "schemas", "agent_message.schema.json")
	schemaBytes, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read agent message schema: %v", err)
	}
	var schema struct {
		OneOf []struct {
			Title      string                     `json:"title"`
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"oneOf"`
		Definitions map[string]json.RawMessage `json:"definitions"`
	}
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		t.Fatalf("decode agent message schema: %v", err)
	}
	var patchProperties map[string]json.RawMessage
	for _, variant := range schema.OneOf {
		if variant.Title == "patch_proposal" {
			patchProperties = variant.Properties
			break
		}
	}
	testHintSchema, ok := patchProperties["test_hint"]
	if !ok {
		t.Fatal("patch_proposal schema has no optional test_hint property")
	}
	var testHintRef struct {
		Ref string `json:"$ref"`
	}
	if err := json.Unmarshal(testHintSchema, &testHintRef); err != nil || testHintRef.Ref != "#/definitions/TestHint" {
		t.Fatalf("patch_proposal test_hint ref = %q, want #/definitions/TestHint", testHintRef.Ref)
	}
	definitionRaw, ok := schema.Definitions["TestHint"]
	if !ok {
		t.Fatal("schema has no TestHint definition")
	}
	var definition struct {
		Properties           map[string]json.RawMessage `json:"properties"`
		AdditionalProperties *bool                      `json:"additionalProperties"`
	}
	if err := json.Unmarshal(definitionRaw, &definition); err != nil {
		t.Fatalf("decode TestHint definition: %v", err)
	}
	if definition.AdditionalProperties == nil || *definition.AdditionalProperties {
		t.Fatal("TestHint schema must set additionalProperties=false")
	}
	if len(definition.Properties) != len(hint) {
		t.Fatalf("schema/type TestHint property count mismatch: schema=%d domain=%d", len(definition.Properties), len(hint))
	}
	for key := range hint {
		if _, ok := definition.Properties[key]; !ok {
			t.Fatalf("schema TestHint is missing serialized domain field %q", key)
		}
	}
	for key := range definition.Properties {
		if _, ok := hint[key]; !ok {
			t.Fatalf("schema TestHint has field %q absent from serialized domain hint", key)
		}
	}
}

func TestParseCoderMessageRejectsInvalidResponses(t *testing.T) {
	for _, content := range []string{
		"no json",
		"null",
		"[]",
		`{"type":"unknown"}`,
		`{"type":`,
	} {
		if _, err := ParseCoderMessage(content); err == nil {
			t.Fatalf("ParseCoderMessage(%q) should fail", content)
		}
	}
}

func TestParseCoderMessageRejectsInvalidKnownMessageShapes(t *testing.T) {
	cases := []string{
		`{"type":"read_request","actions":"bad"}`,
		`{"type":"plan","steps":"bad"}`,
		`{"type":"patch_proposal","tests":"bad"}`,
		`{"type":"test_request","actions":"bad"}`,
		`{"type":"revision_request","actions":"bad"}`,
		`{"type":"final_report","changed_files":"bad"}`,
	}
	for _, content := range cases {
		t.Run(content, func(t *testing.T) {
			if _, err := ParseCoderMessage(content); err == nil {
				t.Fatal("expected shape error")
			}
		})
	}
}

func TestParseCoderMessageHandlesEscapesBracesAndJapaneseText(t *testing.T) {
	content := `{"type":"plan","task_summary":"brace } and {, escaped quote: \"quoted\", embedded object: {\"type\":\"final_report\"}, にゃりゅ","steps":[],"display_hint":{"future":"optional"}}`
	got, err := ParseCoderMessage(content)
	if err != nil {
		t.Fatalf("ParseCoderMessage failed: %v", err)
	}
	if got.Plan == nil || got.Plan.TaskSummary != `brace } and {, escaped quote: "quoted", embedded object: {"type":"final_report"}, にゃりゅ` {
		t.Fatalf("unexpected plan parse: %#v", got.Plan)
	}
	if got.Raw != content {
		t.Fatalf("Raw = %q, want trimmed JSON object %q", got.Raw, content)
	}
}

func TestParseCoderMessageRejectsProseFencesAndMultipleObjects(t *testing.T) {
	object := `{"type":"plan","task_summary":"x","steps":[]}`
	cases := []struct {
		name    string
		content string
	}{
		{name: "prefix prose", content: "reasoning " + object},
		{name: "suffix prose", content: object + " done"},
		{name: "markdown fence", content: " ```json\n" + object + "\n``` "},
		{name: "second object", content: object + ` {"type":"final_report","summary":"done"}`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseCoderMessage(tt.content); err == nil {
				t.Fatalf("expected strict JSON rejection for %q", tt.content)
			}
		})
	}
}

func TestParseCoderMessageValidatesActions(t *testing.T) {
	valid := []struct {
		name    string
		content string
	}{
		{
			name:    "shell command with omitted args",
			content: `{"type":"read_request","actions":[{"action":"shell_command","target":"arbitrary command text"}]}`,
		},
		{
			name:    "shell command with empty args",
			content: `{"type":"test_request","actions":[{"action":"shell_command","target":"go test ./...","args":{}}]}`,
		},
		{
			name:    "mcp tool with nested args",
			content: `{"type":"read_request","actions":[{"action":"mcp_tool","target":"files.read","args":{"path":"src/main.go","options":{"lines":[1,2],"enabled":true}}}]}`,
		},
		{
			name:    "shell command with nil args",
			content: `{"type":"read_request","actions":[{"action":"shell_command","target":"git status","args":null}]}`,
		},
		{
			name:    "mcp tool with omitted optional args",
			content: `{"type":"read_request","actions":[{"action":"mcp_tool","target":"files.list"}]}`,
		},
		{
			name:    "revision request may omit actions",
			content: `{"type":"revision_request","reason":"the prior check failed"}`,
		},
		{
			name:    "revision request may provide empty actions",
			content: `{"type":"revision_request","reason":"the prior check failed","actions":[]}`,
		},
	}
	for _, tt := range valid {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseCoderMessage(tt.content); err != nil {
				t.Fatalf("ParseCoderMessage failed: %v", err)
			}
		})
	}

	invalid := []struct {
		name    string
		content string
	}{
		{name: "read actions missing", content: `{"type":"read_request"}`},
		{name: "read actions empty", content: `{"type":"read_request","actions":[]}`},
		{name: "test actions null", content: `{"type":"test_request","actions":null}`},
		{name: "unknown action", content: `{"type":"read_request","actions":[{"action":"file_edit","target":"a.go"}]}`},
		{name: "blank target", content: `{"type":"read_request","actions":[{"action":"shell_command","target":"   "}]}`},
		{name: "shell args nonempty", content: `{"type":"read_request","actions":[{"action":"shell_command","target":"go test","args":{"cwd":"."}}]}`},
		{name: "shell args array", content: `{"type":"read_request","actions":[{"action":"shell_command","target":"go test","args":[]}]}`},
		{name: "mcp args string", content: `{"type":"read_request","actions":[{"action":"mcp_tool","target":"files.read","args":"src/main.go"}]}`},
		{name: "mcp args array", content: `{"type":"read_request","actions":[{"action":"mcp_tool","target":"files.read","args":[]}]}`},
		{name: "mcp args null", content: `{"type":"read_request","actions":[{"action":"mcp_tool","target":"files.read","args":null}]}`},
		{name: "revision actions wrong type", content: `{"type":"revision_request","reason":"retry","actions":"bad"}`},
		{name: "revision action invalid", content: `{"type":"revision_request","reason":"retry","actions":[{"action":"unknown","target":"x"}]}`},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseCoderMessage(tt.content); err == nil {
				t.Fatal("expected action validation error")
			}
		})
	}
}

func TestWorkerActionSchemaMatchesParserContract(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate coderloop test source")
	}
	schemaPath := filepath.Join(filepath.Dir(sourceFile), "..", "..", "..", "schemas", "agent_message.schema.json")
	schemaBytes, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read agent message schema: %v", err)
	}
	var schema struct {
		Definitions map[string]json.RawMessage `json:"definitions"`
	}
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		t.Fatalf("decode agent message schema: %v", err)
	}
	definitionRaw, ok := schema.Definitions["WorkerAction"]
	if !ok {
		t.Fatal("schema has no WorkerAction definition")
	}
	var definition map[string]any
	if err := json.Unmarshal(definitionRaw, &definition); err != nil {
		t.Fatalf("decode WorkerAction definition: %v", err)
	}
	required := definition["required"].([]any)
	if len(required) != 2 || required[0] != "action" || required[1] != "target" {
		t.Fatalf("WorkerAction required fields = %v, want action and target only", required)
	}
	properties := definition["properties"].(map[string]any)
	action := properties["action"].(map[string]any)
	enum := action["enum"].([]any)
	if len(enum) != 2 || enum[0] != "shell_command" || enum[1] != "mcp_tool" {
		t.Fatalf("WorkerAction actions = %v, want shell_command and mcp_tool", enum)
	}
	target := properties["target"].(map[string]any)
	if target["minLength"] != float64(1) || target["pattern"] != `\S` {
		t.Fatalf("WorkerAction target schema must reject blank strings: %v", target)
	}
	if _, ok := properties["args"]; !ok {
		t.Fatal("WorkerAction schema must declare optional args")
	}
	if definition["additionalProperties"] != false {
		t.Fatal("WorkerAction schema must set additionalProperties=false")
	}

	argsSchemas := make(map[string]map[string]any)
	for _, item := range definition["allOf"].([]any) {
		branch := item.(map[string]any)
		condition := branch["if"].(map[string]any)["properties"].(map[string]any)["action"].(map[string]any)
		thenProps := branch["then"].(map[string]any)["properties"].(map[string]any)
		argsSchemas[condition["const"].(string)] = thenProps["args"].(map[string]any)
	}
	shellArgs := argsSchemas["shell_command"]["oneOf"].([]any)
	hasNull, hasEmptyObject := false, false
	for _, item := range shellArgs {
		option := item.(map[string]any)
		hasNull = hasNull || option["type"] == "null"
		hasEmptyObject = hasEmptyObject || (option["type"] == "object" && option["maxProperties"] == float64(0))
	}
	if !hasNull || !hasEmptyObject {
		t.Fatalf("shell_command args must allow only null or an empty object: %v", shellArgs)
	}
	if argsSchemas["mcp_tool"]["type"] != "object" {
		t.Fatalf("mcp_tool args must be an object: %v", argsSchemas["mcp_tool"])
	}
}

func TestParseCoderMessageRejectsAmbiguousActionAliasesAndDuplicates(t *testing.T) {
	validAction := `{"action":"shell_command","target":"cat README.md"}`
	tests := []struct {
		name    string
		content string
	}{
		{
			name:    "read canonical empty and case alias populated",
			content: `{"type":"read_request","actions":[],"Actions":[` + validAction + `]}`,
		},
		{
			name:    "test only case alias",
			content: `{"type":"test_request","Actions":[` + validAction + `]}`,
		},
		{
			name:    "revision canonical and case alias",
			content: `{"type":"revision_request","reason":"retry","actions":[],"Actions":[` + validAction + `]}`,
		},
		{
			name:    "duplicate exact action arrays",
			content: `{"type":"read_request","actions":[` + validAction + `],"actions":[]}`,
		},
		{
			name:    "case alias for action field",
			content: `{"type":"read_request","actions":[{"action":"shell_command","Action":"mcp_tool","target":"cat README.md"}]}`,
		},
		{
			name:    "duplicate exact target fields",
			content: `{"type":"read_request","actions":[{"action":"shell_command","target":"cat README.md","target":"git status"}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseCoderMessage(tt.content); err == nil {
				t.Fatalf("expected ambiguous fields to be rejected: %s", tt.content)
			}
		})
	}
}

func TestParseCoderMessagePreservesNestedMCPArgs(t *testing.T) {
	content, err := json.Marshal(map[string]any{
		"type": "read_request",
		"actions": []any{map[string]any{
			"action": "mcp_tool",
			"target": "files.read",
			"args": map[string]any{
				"path": "src/main.go",
				"options": map[string]any{
					"lines":   []int{1, 2},
					"enabled": true,
				},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := ParseCoderMessage(string(content))
	if err != nil {
		t.Fatalf("ParseCoderMessage failed: %v", err)
	}
	if msg.ReadRequest == nil || len(msg.ReadRequest.Actions) != 1 {
		t.Fatalf("read request was not parsed: %#v", msg)
	}
	args := msg.ReadRequest.Actions[0].Args
	options, ok := args["options"].(map[string]any)
	if !ok || options["enabled"] != true {
		t.Fatalf("nested mcp args were not retained: %#v", args)
	}
	lines, ok := options["lines"].([]any)
	if !ok || len(lines) != 2 || lines[0] != float64(1) || lines[1] != float64(2) {
		t.Fatalf("nested mcp array was not retained: %#v", options["lines"])
	}
}

func TestParseCoderMessageRejectsEmptyOrNoOpPatch(t *testing.T) {
	for _, patchText := range []string{"", " \n\t ", "[]"} {
		content, err := json.Marshal(map[string]string{
			"type":   "patch_proposal",
			"intent": "no change",
			"patch":  patchText,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseCoderMessage(string(content)); err == nil {
			t.Fatalf("empty/no-op patch %q should be rejected", patchText)
		}
	}

	for _, intent := range []string{"", " \n\t "} {
		content, err := json.Marshal(map[string]string{
			"type":   "patch_proposal",
			"intent": intent,
			"patch":  `[{"type":"file_edit","action":"create","target":"src/main.go","content":"package main"}]`,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseCoderMessage(string(content)); err == nil {
			t.Fatalf("blank patch intent %q should be rejected", intent)
		}
	}
}

func TestParseCoderMessageAllowsOptionalPlanAndFinalFields(t *testing.T) {
	for _, content := range []string{
		`{"type":"plan","steps":[]}`,
		`{"type":"plan","task_summary":"summary"}`,
		`{"type":"final_report"}`,
		`{"type":"plan","task_summary":"","steps":[]}`,
		`{"type":"final_report","summary":"","future_annotation":{"label":"non-executing"}}`,
	} {
		if _, err := ParseCoderMessage(content); err != nil {
			t.Fatalf("valid optional fields were over-constrained for %s: %v", content, err)
		}
	}
}

func TestParseCoderMessageAcceptsMarkdownPatch(t *testing.T) {
	fence := strings.Repeat(string(rune(96)), 3)
	patchText := "## Patch\n\n" + fence + "go:src/main.go\npackage main\n" + fence
	content, err := json.Marshal(map[string]string{
		"type":   "patch_proposal",
		"intent": "preserve supported markdown patch format",
		"patch":  patchText,
	})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := ParseCoderMessage(string(content))
	if err != nil {
		t.Fatalf("ParseCoderMessage rejected valid markdown patch: %v", err)
	}
	if msg.PatchProposal == nil || msg.PatchProposal.Patch != patchText {
		t.Fatalf("markdown patch was not retained: %#v", msg.PatchProposal)
	}
}

func TestObservationResultAndActions(t *testing.T) {
	short, truncated := TruncateOutput("short")
	if short != "short" || truncated {
		t.Fatalf("short output should not truncate: %q %v", short, truncated)
	}

	long := strings.Repeat("x", maxObservationOutputBytes+1)
	trimmed, truncated := TruncateOutput(long)
	if !truncated || !strings.HasSuffix(trimmed, "\n...[truncated]") {
		t.Fatalf("long output should truncate: len=%d truncated=%v", len(trimmed), truncated)
	}

	okResult := NewObservationActionResult("shell_command", "go test", long, nil)
	if okResult.Status != "ok" || !okResult.Truncated {
		t.Fatalf("ok result should be truncated: %#v", okResult)
	}
	errResult := NewObservationActionResult("shell_command", "go test", "", errors.New("boom"))
	if errResult.Status != "error" || errResult.Output != "boom" {
		t.Fatalf("error result mismatch: %#v", errResult)
	}

	observation := NewObservationResult(3, []ObservationActionResult{okResult, errResult})
	if observation.Type != "observation" || !strings.Contains(observation.ToJSON(), `"turn":3`) {
		t.Fatalf("observation JSON mismatch: %#v json=%s", observation, observation.ToJSON())
	}

	actions := ActionsFromWorkerActions([]WorkerAction{{Action: "mcp_tool", Target: "file_read", Args: map[string]any{"path": "a.go"}}})
	if len(actions) != 1 || actions[0].Target != "file_read" || actions[0].Args["path"] != "a.go" {
		t.Fatalf("actions mismatch: %#v", actions)
	}
}
