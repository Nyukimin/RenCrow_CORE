package tools

import (
	"context"
	"testing"

	modulebrowser "github.com/Nyukimin/RenCrow_CORE/modules/browseractor"
)

type fakeBrowserActorToolRunner struct {
	req modulebrowser.RunRequest
	resp modulebrowser.RunResponse
}

func (f *fakeBrowserActorToolRunner) Run(_ context.Context, req modulebrowser.RunRequest) (modulebrowser.RunResponse, error) {
	f.req = req
	return f.resp, nil
}

func TestToolRunnerBrowserRunSuccess(t *testing.T) {
	fake := &fakeBrowserActorToolRunner{resp: modulebrowser.RunResponse{SchemaVersion: "1.0", RunID: "run_1", Status: modulebrowser.StatusCompleted}}
	runner := NewToolRunner(ToolRunnerConfig{BrowserActorRunner: fake})
	resp, err := runner.ExecuteV2(context.Background(), "browser.run", map[string]any{
		"run_id": "run_1",
		"start_url": "file:///tmp/basic.html",
		"actions": []any{map[string]any{"type": "open"}},
	})
	if err != nil || resp.IsError() {
		t.Fatalf("ExecuteV2 err=%v resp=%+v", err, resp)
	}
	if fake.req.RunID != "run_1" || fake.req.StartURL == "" {
		t.Fatalf("unexpected request: %+v", fake.req)
	}
}

func TestToolRunnerBrowserRunMapsPermissionError(t *testing.T) {
	fake := &fakeBrowserActorToolRunner{resp: modulebrowser.RunResponse{
		SchemaVersion: "1.0",
		RunID: "run_1",
		Status: modulebrowser.StatusFailed,
		RiskLevel: modulebrowser.RiskExternalEffect,
		Error: &modulebrowser.Error{Code: modulebrowser.ErrPermissionDenied, Message: "blocked"},
	}}
	runner := NewToolRunner(ToolRunnerConfig{BrowserActorRunner: fake})
	resp, err := runner.ExecuteV2(context.Background(), "browser.run", map[string]any{
		"start_url": "file:///tmp/basic.html",
		"actions": []any{map[string]any{"type": "click", "selector": "#send"}},
	})
	if err != nil {
		t.Fatalf("ExecuteV2 err=%v", err)
	}
	if !resp.IsError() || resp.Error.Code != "PERMISSION_DENIED" {
		t.Fatalf("expected permission error, got %+v", resp)
	}
}
