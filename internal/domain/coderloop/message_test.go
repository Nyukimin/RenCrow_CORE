package coderloop

import (
	"errors"
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
			content: `{"type":"patch_proposal","intent":"fix","patch":"*** Begin Patch\n*** End Patch","tests":["go test ./..."]}`,
			assert: func(t *testing.T, msg *CoderMessage) {
				t.Helper()
				if msg.PatchProposal == nil || msg.PatchProposal.Intent != "fix" {
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
