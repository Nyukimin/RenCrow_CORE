package webgather

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	modulewebgather "github.com/Nyukimin/picoclaw_multiLLM/modules/webgather"
)

func TestWebwrightFetcherRunsConvertsAndReturnsArtifact(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer listener.Close()

	dir := t.TempDir()
	outputDir := filepath.Join(dir, "runs")
	stagingDir := filepath.Join(dir, "staging")
	var runnerCalled bool
	var converterCalled bool
	fetcher := NewWebwrightFetcher(WebwrightFetcherConfig{
		Enabled:           true,
		RunnerPath:        "tools/webwright_fetch/run_webwright_fetch.py",
		ConverterPath:     "tools/webwright_fetch/webwright_to_staging.py",
		OutputDir:         outputDir,
		StagingOutputDir:  stagingDir,
		ResponsesEndpoint: "http://" + listener.Addr().String() + "/v1/responses",
	}).WithRunner(func(_ context.Context, _ string, args []string) (string, string, int, error) {
		if len(args) == 0 {
			t.Fatalf("expected command args")
		}
		switch args[0] {
		case "tools/webwright_fetch/run_webwright_fetch.py":
			runnerCalled = true
			reportDir := filepath.Join(outputDir, "task")
			if err := os.MkdirAll(reportDir, 0o755); err != nil {
				t.Fatalf("mkdir report dir: %v", err)
			}
			report := map[string]any{"report": map[string]any{"title": "Example", "summary": "Browser fetched body"}}
			b, _ := json.Marshal(report)
			if err := os.WriteFile(filepath.Join(reportDir, "report.json"), b, 0o644); err != nil {
				t.Fatalf("write report: %v", err)
			}
		case "tools/webwright_fetch/webwright_to_staging.py":
			converterCalled = true
			output := argAfter(args, "--output")
			item := map[string]any{
				"SourceURL": "https://example.com/page",
				"RawText":   "Browser fetched body",
				"Meta": map[string]any{
					"webwright":       true,
					"tool":            "webwright_fetch",
					"review_required": true,
					"auto_promote":    false,
				},
			}
			b, _ := json.Marshal(item)
			if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
				t.Fatalf("mkdir jsonl dir: %v", err)
			}
			if err := os.WriteFile(output, append(b, '\n'), 0o644); err != nil {
				t.Fatalf("write jsonl: %v", err)
			}
		default:
			t.Fatalf("unexpected command args: %v", args)
		}
		return "", "", 0, nil
	})

	artifact, err := fetcher.Fetch(context.Background(), "https://example.com/page", modulewebgather.DefaultFetchPolicy())
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if !runnerCalled || !converterCalled {
		t.Fatalf("expected runner and converter calls, runner=%v converter=%v", runnerCalled, converterCalled)
	}
	if artifact.ProviderName != "webwright" || artifact.FinalURL != "https://example.com/page" || string(artifact.Body) != "Browser fetched body" {
		t.Fatalf("unexpected artifact: %+v body=%s", artifact, string(artifact.Body))
	}
	if artifact.Meta["webwright_report_path"] == "" || artifact.Meta["webwright_jsonl_path"] == "" {
		t.Fatalf("expected report/jsonl paths in meta: %+v", artifact.Meta)
	}
}

func TestWebwrightFetcherSynthesizesReportFromStepLogs(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer listener.Close()

	dir := t.TempDir()
	outputDir := filepath.Join(dir, "runs")
	stagingDir := filepath.Join(dir, "staging")
	var converterInput string
	fetcher := NewWebwrightFetcher(WebwrightFetcherConfig{
		Enabled:           true,
		RunnerPath:        "tools/webwright_fetch/run_webwright_fetch.py",
		ConverterPath:     "tools/webwright_fetch/webwright_to_staging.py",
		OutputDir:         outputDir,
		StagingOutputDir:  stagingDir,
		ResponsesEndpoint: "http://" + listener.Addr().String() + "/v1/responses",
	}).WithRunner(func(_ context.Context, _ string, args []string) (string, string, int, error) {
		switch args[0] {
		case "tools/webwright_fetch/run_webwright_fetch.py":
			runDir := filepath.Join(outputDir, webwrightTaskID("https://example.com/page")+"_20260601_000000")
			logDir := filepath.Join(runDir, "logs")
			if err := os.MkdirAll(logDir, 0o755); err != nil {
				t.Fatalf("mkdir log dir: %v", err)
			}
			logText := "URL: https://example.com/page\nTITLE: Example\nSUMMARY: Browser log body"
			if err := os.WriteFile(filepath.Join(logDir, "step_0001.log"), []byte(logText), 0o644); err != nil {
				t.Fatalf("write step log: %v", err)
			}
		case "tools/webwright_fetch/webwright_to_staging.py":
			converterInput = argAfter(args, "--input")
			if filepath.Base(converterInput) != "report.json" {
				t.Fatalf("expected synthesized report input, got %s", converterInput)
			}
			output := argAfter(args, "--output")
			item := map[string]any{
				"SourceURL": "https://example.com/page",
				"RawText":   "Browser log body",
				"Meta":      map[string]any{"webwright": true, "tool": "webwright_fetch"},
			}
			b, _ := json.Marshal(item)
			if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
				t.Fatalf("mkdir jsonl dir: %v", err)
			}
			if err := os.WriteFile(output, append(b, '\n'), 0o644); err != nil {
				t.Fatalf("write jsonl: %v", err)
			}
		default:
			t.Fatalf("unexpected command args: %v", args)
		}
		return "", "", 0, nil
	})

	artifact, err := fetcher.Fetch(context.Background(), "https://example.com/page", modulewebgather.DefaultFetchPolicy())
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if converterInput == "" {
		t.Fatal("expected converter to be called with synthesized report")
	}
	if string(artifact.Body) != "Browser log body" {
		t.Fatalf("unexpected body: %s", string(artifact.Body))
	}
}

func TestWebwrightFetcherAppliesFetchPolicyTimeout(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer listener.Close()

	fetcher := NewWebwrightFetcher(WebwrightFetcherConfig{
		Enabled:           true,
		OutputDir:         t.TempDir(),
		StagingOutputDir:  t.TempDir(),
		ResponsesEndpoint: "http://" + listener.Addr().String() + "/v1/responses",
	}).WithRunner(func(ctx context.Context, _ string, _ []string) (string, string, int, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("expected runner context deadline")
		}
		if time.Until(deadline) > time.Second {
			t.Fatalf("unexpectedly distant deadline: %s", time.Until(deadline))
		}
		return "", "", 1, context.DeadlineExceeded
	})

	policy := modulewebgather.DefaultFetchPolicy()
	policy.RequestTimeout = 50 * time.Millisecond
	_, err = fetcher.Fetch(context.Background(), "https://example.com/page", policy)
	if err == nil {
		t.Fatal("expected timeout-backed runner error")
	}
}

func TestWebwrightFetcherPreflightStopsBeforeRunner(t *testing.T) {
	var called bool
	fetcher := NewWebwrightFetcher(WebwrightFetcherConfig{
		Enabled:           true,
		OutputDir:         t.TempDir(),
		StagingOutputDir:  t.TempDir(),
		ResponsesEndpoint: "http://127.0.0.1:1/v1/responses",
	}).WithRunner(func(context.Context, string, []string) (string, string, int, error) {
		called = true
		return "", "", 0, nil
	})
	_, err := fetcher.Fetch(context.Background(), "https://example.com/page", modulewebgather.DefaultFetchPolicy())
	if err == nil {
		t.Fatal("expected preflight error")
	}
	if called {
		t.Fatal("runner must not be called when responses endpoint preflight fails")
	}
}

func argAfter(args []string, key string) string {
	for i := 0; i < len(args)-1; i++ {
		if strings.TrimSpace(args[i]) == key {
			return strings.TrimSpace(args[i+1])
		}
	}
	return ""
}
