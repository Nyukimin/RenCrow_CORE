package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	modulebrowser "github.com/Nyukimin/RenCrow_CORE/modules/browseractor"
)

type fakeBrowserActorRunner struct {
	runReq     modulebrowser.RunRequest
	runResp    modulebrowser.RunResponse
	doctorResp modulebrowser.DoctorResponse
}

func (f *fakeBrowserActorRunner) Run(_ context.Context, req modulebrowser.RunRequest) (modulebrowser.RunResponse, error) {
	f.runReq = req
	return f.runResp, nil
}

func (f *fakeBrowserActorRunner) Doctor(context.Context) (modulebrowser.DoctorResponse, error) {
	return f.doctorResp, nil
}

func TestRunBrowserActorCommandDoctorJSON(t *testing.T) {
	runner := &fakeBrowserActorRunner{doctorResp: modulebrowser.DoctorResponse{SchemaVersion: "1.0", OK: true}}
	var out, errOut bytes.Buffer
	code := runBrowserActorCommand([]string{"doctor", "--json"}, browserActorCLIDeps{Runner: runner}, strings.NewReader(""), &out, &errOut)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), `"ok": true`) {
		t.Fatalf("unexpected out: %s", out.String())
	}
}

func TestRunBrowserActorCommandRunRequiresEnabled(t *testing.T) {
	runner := &fakeBrowserActorRunner{}
	var out, errOut bytes.Buffer
	code := runBrowserActorCommand([]string{"run", "--json"}, browserActorCLIDeps{Config: config.BrowserActorConfig{Enabled: false}, Runner: runner}, strings.NewReader(`{}`), &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "browser_actor.enabled=true") {
		t.Fatalf("expected enabled error, code=%d stderr=%s", code, errOut.String())
	}
}

func TestRunBrowserActorCommandRunJSON(t *testing.T) {
	runner := &fakeBrowserActorRunner{runResp: modulebrowser.RunResponse{SchemaVersion: "1.0", RunID: "run_1", Status: modulebrowser.StatusCompleted}}
	req := modulebrowser.RunRequest{RunID: "run_1", StartURL: "file:///tmp/basic.html", Actions: []modulebrowser.Action{{Type: "open"}}}
	body, _ := json.Marshal(req)
	var out, errOut bytes.Buffer
	code := runBrowserActorCommand([]string{"run", "--json"}, browserActorCLIDeps{
		Config: config.BrowserActorConfig{
			Enabled:         true,
			ArtifactRoot:    "workspace/browser_runs",
			TimeoutMS:       123,
			MaxActions:      5,
			AllowedOrigins:  []string{"file://"},
			HeadlessDefault: browserActorBoolPtr(true),
			SaveTrace:       browserActorBoolPtr(true),
			SaveScreenshot:  browserActorBoolPtr(true),
			MaskSecrets:     browserActorBoolPtr(true),
		},
		Runner: runner,
	}, bytes.NewReader(body), &out, &errOut)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s out=%s", code, errOut.String(), out.String())
	}
	if runner.runReq.TimeoutMS != 123 || runner.runReq.MaxActions != 5 || len(runner.runReq.AllowedOrigins) != 1 {
		t.Fatalf("CLI defaults not applied: %+v", runner.runReq)
	}
	if !runner.runReq.Headless || !runner.runReq.SaveTrace || !runner.runReq.SaveScreenshot || !runner.runReq.MaskSecrets {
		t.Fatalf("CLI safe defaults not applied: %+v", runner.runReq)
	}
}

func browserActorBoolPtr(value bool) *bool {
	return &value
}
