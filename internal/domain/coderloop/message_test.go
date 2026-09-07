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
			name:    "read request embedded in text",
			content: `prefix {"type":"read_request","actions":[{"action":"shell_command","target":"rg TODO"}]} suffix`,
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
			content: `{"type":"patch_proposal","intent":"fix","patch":"*** Begin Patch\n*** End Patch","tests":["go test ./..."],"test_hint":{"changed_surface":["internal/domain"],"expected_impact":"domain contract","recommended_tier":"related","recommended_steps":["go test ./internal/domain/..."],"risk":"medium"}}`,
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

func TestExtractJSONHandlesNestedObjectsAndEscapedBraces(t *testing.T) {
	got, err := extractJSON(`before {"type":"plan","nested":{"text":"brace } inside string"},"steps":[]} after`)
	if err != nil {
		t.Fatalf("extractJSON failed: %v", err)
	}
	if !strings.Contains(got, `"nested"`) || !strings.Contains(got, `brace } inside string`) {
		t.Fatalf("unexpected JSON extraction: %s", got)
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
