package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	entryadapter "github.com/Nyukimin/RenCrow_CORE/internal/adapter/entry"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	ttsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tts"
)

type fakeEntryProcessor struct {
	calls int
	resp  orchestrator.ProcessMessageResponse
	err   error
}

func (f *fakeEntryProcessor) ProcessMessage(_ context.Context, _ orchestrator.ProcessMessageRequest) (orchestrator.ProcessMessageResponse, error) {
	f.calls++
	if f.resp.Route == "" {
		f.resp.Route = routing.RouteCHAT
	}
	if f.resp.JobID == "" {
		f.resp.JobID = "job-1"
	}
	if f.resp.Response == "" {
		f.resp.Response = "ok"
	}
	return f.resp, f.err
}

func TestProcessEntryRequest_Normal(t *testing.T) {
	proc := &fakeEntryProcessor{}
	req := entryadapter.Request{SessionID: "s1", Channel: "viewer", UserID: "u1", Message: "ログ確認して"}

	res, err := processEntryRequest(context.Background(), proc, req, filepath.Join(t.TempDir(), "execution_report.jsonl"))
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if proc.calls != 1 {
		t.Fatalf("expected 1 call, got %d", proc.calls)
	}
	if res.Response == "" {
		t.Fatal("expected response")
	}
}

func TestProcessEntryRequest_TTSUsesAutonomousAndWritesReport(t *testing.T) {
	proc := &fakeEntryProcessor{resp: orchestrator.ProcessMessageResponse{Route: routing.RouteCODE3, JobID: "job-tts", Response: "TTS implemented"}}
	reportPath := filepath.Join(t.TempDir(), "execution_report.jsonl")
	req := entryadapter.Request{SessionID: "s1", Channel: "viewer", UserID: "u1", Message: "TTS実装して"}
	runtime := ttsEntryRuntime{
		synthesizer: synthStub{
			out: ttsinfra.SynthesisOutput{
				Provider:      "sbv2",
				VoiceID:       "mio",
				AudioFilePath: "/tmp/sbv2.wav",
				DurationMS:    1200,
			},
		},
		player: playStub{
			out: ttsinfra.PlaybackResult{
				Command:  "echo test",
				ExitCode: 0,
			},
		},
	}

	res, err := processEntryRequestWithRuntime(context.Background(), proc, req, reportPath, runtime)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if proc.calls != 1 {
		t.Fatalf("expected 1 call in autonomous apply, got %d", proc.calls)
	}
	if res.JobID != "job-tts" {
		t.Fatalf("unexpected job id: %s", res.JobID)
	}
	if !strings.Contains(res.EvidenceRef, "execution_report:") {
		t.Fatalf("expected evidence ref, got %q", res.EvidenceRef)
	}

	b, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("expected report file, got %v", err)
	}
	if !strings.Contains(string(b), `"status":"passed"`) {
		t.Fatalf("expected passed report, got: %s", string(b))
	}
	if !strings.Contains(string(b), `"tts_provider":"sbv2"`) {
		t.Fatalf("expected tts provider evidence, got: %s", string(b))
	}
	if !strings.Contains(string(b), `"playback_exit_code":0`) {
		t.Fatalf("expected playback exit code evidence, got: %s", string(b))
	}
}

func TestProcessEntryRequest_TTSRequiresRuntime(t *testing.T) {
	proc := &fakeEntryProcessor{}
	reportPath := filepath.Join(t.TempDir(), "execution_report.jsonl")
	req := entryadapter.Request{SessionID: "s1", Channel: "viewer", UserID: "u1", Message: "TTS実装して"}

	_, err := processEntryRequest(context.Background(), proc, req, reportPath)
	if err == nil {
		t.Fatal("expected error when tts runtime is not configured")
	}
}

func TestProcessEntryRequest_TTSPlaybackFailure(t *testing.T) {
	proc := &fakeEntryProcessor{resp: orchestrator.ProcessMessageResponse{Route: routing.RouteCODE3, JobID: "job-tts", Response: "TTS implemented"}}
	reportPath := filepath.Join(t.TempDir(), "execution_report.jsonl")
	req := entryadapter.Request{SessionID: "s1", Channel: "viewer", UserID: "u1", Message: "TTS実装して"}
	runtime := ttsEntryRuntime{
		synthesizer: synthStub{
			out: ttsinfra.SynthesisOutput{
				Provider:      "sbv2",
				VoiceID:       "mio",
				AudioFilePath: "/tmp/sbv2.wav",
				DurationMS:    1200,
			},
		},
		player: playStub{
			out: ttsinfra.PlaybackResult{
				Command:  "play /tmp/sbv2.wav",
				ExitCode: 1,
			},
			err: fmt.Errorf("failed"),
		},
	}

	_, err := processEntryRequestWithRuntime(context.Background(), proc, req, reportPath, runtime)
	if err == nil {
		t.Fatal("expected playback failure")
	}
	b, readErr := os.ReadFile(reportPath)
	if readErr != nil {
		t.Fatalf("expected report file, got %v", readErr)
	}
	if !strings.Contains(string(b), `"status":"failed"`) {
		t.Fatalf("expected failed report, got: %s", string(b))
	}
	if !strings.Contains(string(b), `"tts_error_kind":"playback"`) {
		t.Fatalf("expected playback error kind, got: %s", string(b))
	}
}

type synthStub struct {
	out ttsinfra.SynthesisOutput
	err error
}

func (s synthStub) Synthesize(_ context.Context, _ ttsinfra.SynthesisInput) (ttsinfra.SynthesisOutput, error) {
	return s.out, s.err
}

type playStub struct {
	out ttsinfra.PlaybackResult
	err error
}

func (p playStub) Play(_ context.Context, _ string) (ttsinfra.PlaybackResult, error) {
	return p.out, p.err
}

func TestBuildTTSEntryRuntime_Configured(t *testing.T) {
	cfg := &config.Config{
		TTS: config.TTSConfig{
			Enabled:          true,
			ProviderPriority: []string{"irodori"},
			PlaybackCommands: []config.TTSCommandConfig{{Name: "sh", Args: []string{"-c", "exit 0"}}},
			Irodori: config.TTSIrodoriConfig{
				Enabled: true,
				BaseURL: "http://127.0.0.1:7870",
				VoiceID: "mio",
			},
		},
	}
	rt := buildTTSEntryRuntime(cfg)
	if !rt.configured() {
		t.Fatal("expected runtime configured")
	}
}
