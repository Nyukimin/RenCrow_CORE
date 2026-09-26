package llmfit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The CLI provider is exercised with a helper process: the test binary re-executes
// itself with LLMFIT_TEST_HELPER=1 and behaves like a fake llmfit executable.
// This keeps the test cross-platform (no shell scripts) and off the network.

const (
	helperEnvFlag = "LLMFIT_TEST_HELPER"
	helperEnvMode = "LLMFIT_TEST_HELPER_MODE"
)

func TestMain(m *testing.M) {
	if os.Getenv(helperEnvFlag) == "1" {
		os.Exit(runFakeLLMFit())
	}
	os.Exit(m.Run())
}

func runFakeLLMFit() int {
	args := os.Args[1:]
	subcommand := ""
	if len(args) > 0 {
		subcommand = args[0]
	}
	switch os.Getenv(helperEnvMode) {
	case "sleep":
		time.Sleep(5 * time.Second)
		return 0
	case "invalid_json":
		fmt.Fprint(os.Stdout, "this is not json")
		return 0
	case "fail":
		fmt.Fprint(os.Stderr, "llmfit: detector exploded")
		return 2
	case "stderr_noise":
		fmt.Fprintln(os.Stderr, "warning: something on stderr")
	}
	switch subcommand {
	case "system":
		// Real CLI shape: {"providers":...,"system":...} without "node".
		data, err := os.ReadFile(filepath.Join("testdata", "system_cli.json"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "fixture: %v", err)
			return 1
		}
		_, _ = os.Stdout.Write(data)
	case "recommend":
		data, err := os.ReadFile(filepath.Join("testdata", "top.json"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "fixture: %v", err)
			return 1
		}
		_, _ = os.Stdout.Write(data)
	default:
		fmt.Fprintf(os.Stderr, "unexpected args: %v", args)
		return 1
	}
	return 0
}

func newFakeCLIProvider(t *testing.T, mode string, timeout time.Duration) *CLIProvider {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	t.Setenv(helperEnvFlag, "1")
	t.Setenv(helperEnvMode, mode)
	return NewCLIProvider("local", exe, timeout)
}

func TestCLIProviderSystemAndModels(t *testing.T) {
	p := newFakeCLIProvider(t, "stderr_noise", 5*time.Second)
	if err := p.Health(context.Background()); err != nil {
		t.Fatalf("health: %v", err)
	}
	profile, err := p.System(context.Background())
	if err != nil {
		t.Fatalf("system: %v", err)
	}
	if profile.NodeID != "local" || profile.NodeName != "local" || !profile.UnifiedMemory || profile.Backend != "SYCL" {
		t.Fatalf("profile mismatch (node name must fall back to the configured id): %+v", profile)
	}
	if profile.OS != runtime.GOOS {
		t.Fatalf("CLI provider must fill OS from the local host, got %q", profile.OS)
	}
	models, err := p.TopModels(context.Background(), ModelFitQuery{Limit: 7})
	if err != nil {
		t.Fatalf("top models: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models=%d want 2", len(models))
	}
	if models[0].NodeID != "local" {
		t.Fatalf("node id=%q", models[0].NodeID)
	}
}

func TestCLIProviderSearchFiltersByKeyword(t *testing.T) {
	p := newFakeCLIProvider(t, "", 5*time.Second)
	models, err := p.SearchModels(context.Background(), ModelFitQuery{Keyword: "KIMI"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(models) != 1 || models[0].ModelID != "khazarai/Qwen3-4B-Kimi2.5-Reasoning-Distilled-GGUF" {
		t.Fatalf("search must filter by keyword case-insensitively: %+v", models)
	}
}

func TestCLIProviderRejectsInvalidQuery(t *testing.T) {
	p := newFakeCLIProvider(t, "", 5*time.Second)
	_, err := p.TopModels(context.Background(), ModelFitQuery{MinFit: "bogus"})
	if ErrorKindOf(err) != ErrorKindBadRequest {
		t.Fatalf("kind=%s err=%v", ErrorKindOf(err), err)
	}
}

func TestCLIProviderExecutableNotFound(t *testing.T) {
	p := NewCLIProvider("local", "llmfit-definitely-missing-for-test", time.Second)
	if err := p.Health(context.Background()); ErrorKindOf(err) != ErrorKindExecutable {
		t.Fatalf("health kind=%s err=%v", ErrorKindOf(err), err)
	}
	_, err := p.System(context.Background())
	if ErrorKindOf(err) != ErrorKindExecutable || !strings.Contains(err.Error(), "llmfit-definitely-missing-for-test") {
		t.Fatalf("system kind=%s err=%v", ErrorKindOf(err), err)
	}
}

func TestCLIProviderCommandTimeout(t *testing.T) {
	p := newFakeCLIProvider(t, "sleep", 150*time.Millisecond)
	start := time.Now()
	_, err := p.System(context.Background())
	if ErrorKindOf(err) != ErrorKindUnreachable {
		t.Fatalf("kind=%s err=%v", ErrorKindOf(err), err)
	}
	if time.Since(start) > 4*time.Second {
		t.Fatal("command must be killed at the timeout")
	}
}

func TestCLIProviderInvalidJSON(t *testing.T) {
	p := newFakeCLIProvider(t, "invalid_json", 5*time.Second)
	_, err := p.System(context.Background())
	if ErrorKindOf(err) != ErrorKindInvalidResponse {
		t.Fatalf("kind=%s err=%v", ErrorKindOf(err), err)
	}
}

func TestCLIProviderNonZeroExitSummarizesStderr(t *testing.T) {
	p := newFakeCLIProvider(t, "fail", 5*time.Second)
	_, err := p.TopModels(context.Background(), ModelFitQuery{})
	if ErrorKindOf(err) != ErrorKindServerError {
		t.Fatalf("kind=%s err=%v", ErrorKindOf(err), err)
	}
	if !strings.Contains(err.Error(), "exit") || !strings.Contains(err.Error(), "detector exploded") {
		t.Fatalf("error must summarize exit status and stderr: %v", err)
	}
}
