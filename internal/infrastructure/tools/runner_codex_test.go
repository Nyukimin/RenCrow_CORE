package tools

import (
	"context"
	"errors"
	"testing"
)

type fakeCodexToolRunner struct {
	req  CodexRunRequest
	resp CodexRunResponse
	err  error
}

func (f *fakeCodexToolRunner) Run(_ context.Context, req CodexRunRequest) (CodexRunResponse, error) {
	f.req = req
	return f.resp, f.err
}

func TestToolRunnerCodexRunSuccess(t *testing.T) {
	fake := &fakeCodexToolRunner{resp: CodexRunResponse{Status: "completed", FinalText: "done", Sandbox: "read-only"}}
	runner := NewToolRunner(ToolRunnerConfig{CodexRunner: fake})
	resp, err := runner.ExecuteV2(context.Background(), "codex.run", map[string]any{
		"prompt":      "summarize this repo",
		"working_dir": "/repo",
	})
	if err != nil || resp.IsError() {
		t.Fatalf("ExecuteV2 err=%v resp=%+v", err, resp)
	}
	if fake.req.Prompt != "summarize this repo" || fake.req.WorkingDir != "/repo" {
		t.Fatalf("unexpected request: %+v", fake.req)
	}
	if fake.req.Sandbox != "read-only" || fake.req.Ephemeral {
		t.Fatalf("unexpected request defaults: %+v", fake.req)
	}
}

func TestToolRunnerCodexRunRejectsDangerFullAccess(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{CodexRunner: &fakeCodexToolRunner{}})
	resp, err := runner.ExecuteV2(context.Background(), "codex.run", map[string]any{
		"prompt":  "edit files",
		"sandbox": "danger-full-access",
	})
	if err != nil {
		t.Fatalf("ExecuteV2 err=%v", err)
	}
	if !resp.IsError() || resp.Error.Code != "VALIDATION_FAILED" {
		t.Fatalf("expected validation error, got %+v", resp)
	}
}

func TestToolRunnerCodexRunMapsTimeout(t *testing.T) {
	fake := &fakeCodexToolRunner{
		resp: CodexRunResponse{Status: "timeout", TimedOut: true, StderrTail: "timed out"},
		err:  errors.New("context deadline exceeded"),
	}
	runner := NewToolRunner(ToolRunnerConfig{CodexRunner: fake})
	resp, err := runner.ExecuteV2(context.Background(), "codex.run", map[string]any{"prompt": "slow task"})
	if err != nil {
		t.Fatalf("ExecuteV2 err=%v", err)
	}
	if !resp.IsError() || resp.Error.Code != "TIMEOUT" {
		t.Fatalf("expected timeout error, got %+v", resp)
	}
}

func TestParseCodexJSONLExtractsFinalMessageAndUsage(t *testing.T) {
	data := []byte(`{"type":"thread.started","thread_id":"t1"}` + "\n" +
		`{"type":"item.completed","item":{"type":"agent_message","text":"first"}}` + "\n" +
		`{"type":"item.completed","item":{"type":"agent_message","text":"final"}}` + "\n" +
		`{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":2}}` + "\n")
	got := parseCodexJSONL(data)
	if got.FinalText != "final" {
		t.Fatalf("unexpected final text: %q", got.FinalText)
	}
	if got.EventCounts["item.completed"] != 2 || got.Usage["input_tokens"].(float64) != 10 {
		t.Fatalf("unexpected parse result: %+v", got)
	}
}
