package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRunnerRealProcessUsesToolsPlannerAndFailsClosedForUnknownCanonicalCheck(t *testing.T) {
	planner := realPlannerBinary(t)
	runner := buildRunnerBinary(t)

	var healthHits, readinessHits, l1Hits, backupHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			healthHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true,"status":"ok"}`)
		case "/health/ready":
			readinessHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true,"ready":true}`)
		case "/viewer/memory/layers":
			l1Hits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"l0":[],"l1":[],"l2":[],"l3":[],"l3_qdrant":[]}`)
		case "/snapshot-integrity":
			backupHits.Add(1)
			http.Error(w, "backup check must be deferred", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	manifest := filepath.Join(t.TempDir(), "core-checks.json")
	contents, err := os.ReadFile(filepath.Join(coreRepositoryRoot(t), "config", "checks", "core.json"))
	if err != nil {
		t.Fatalf("read CORE manifest: %v", err)
	}
	if err := os.WriteFile(manifest, contents, 0600); err != nil {
		t.Fatalf("write isolated manifest: %v", err)
	}

	cmd := exec.Command(runner,
		"--manifest", manifest,
		"--planner", planner,
		"--core-url", srv.URL,
		"--phase", "runtime",
		"--now", "2026-08-25T00:00:00Z",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatalf("runner unexpectedly accepted an unknown canonical check: stdout=%s", stdout.String())
	}
	var receipt runnerReceipt
	if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil {
		t.Fatalf("decode runner receipt: %v stdout=%s", err, stdout.String())
	}
	if receipt.Status != "blocked" || !strings.HasPrefix(receipt.PlanRevision, "sha256:") || !strings.Contains(receipt.Error, "not allowlisted") {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
	if len(receipt.Results) != 0 {
		t.Fatalf("unexpected plan execution receipt: %+v", receipt)
	}
	if healthHits.Load() != 0 || readinessHits.Load() != 0 || l1Hits.Load() != 0 || backupHits.Load() != 0 {
		t.Fatalf("unexpected isolated route hits: health=%d readiness=%d l1=%d backup=%d", healthHits.Load(), readinessHits.Load(), l1Hits.Load(), backupHits.Load())
	}
}

func realPlannerBinary(t *testing.T) string {
	t.Helper()
	if configured := strings.TrimSpace(os.Getenv("RENCROW_CHECK_PLAN_BIN")); configured != "" {
		if _, err := os.Stat(configured); err == nil {
			return configured
		}
		t.Fatalf("RENCROW_CHECK_PLAN_BIN does not exist: %s", configured)
	}
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Skip("cannot resolve CORE source path for sibling Tools planner")
	}
	coreRoot := filepath.Clean(filepath.Join(filepath.Dir(sourcePath), "..", ".."))
	name := "rencrow-check-plan"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(coreRoot, "..", "RenCrow_Tools", "tools", "quality", "check_plan", name)
	if _, err := os.Stat(path); err != nil {
		t.Skipf("real Tools planner unavailable at %s", path)
	}
	return path
}

func buildRunnerBinary(t *testing.T) string {
	t.Helper()
	name := "rencrow-check-plan-runner"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	output := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-o", output, "./cmd/rencrow-check-plan-runner")
	cmd.Dir = coreRepositoryRoot(t)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build runner: %v stderr=%s", err, stderr.String())
	}
	return output
}

func coreRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve CORE source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(sourcePath), "..", ".."))
}
