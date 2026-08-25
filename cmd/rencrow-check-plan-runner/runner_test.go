package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testManifest = `{
  "schema_version": 1,
  "purpose": "operational_status",
  "phase": "runtime",
  "checks": [
    {
      "check_id": "core_health",
      "guarantee_id": "core_health_available",
      "owner": "RenCrow_CORE",
      "purpose": "runtime health",
      "target": "core:/health",
      "phase": "runtime",
      "consumer": "runtime operational status",
      "failure_action": "degraded",
      "cost": "low",
      "safety_gate": false
    },
    {
      "check_id": "core_readiness",
      "guarantee_id": "core_readiness_available",
      "owner": "RenCrow_CORE",
      "purpose": "runtime readiness",
      "target": "core:/health/ready",
      "phase": "runtime",
      "consumer": "runtime readiness",
      "failure_action": "blocked",
      "cost": "low",
      "safety_gate": false
    },
    {
      "check_id": "core_l1_lightweight_query",
      "guarantee_id": "conversation_l1_available",
      "owner": "RenCrow_CORE",
      "purpose": "runtime availability",
      "target": "conversation_l1",
      "phase": "runtime",
      "consumer": "database operational status",
      "failure_action": "degraded",
      "cost": "low",
      "safety_gate": false
    },
    {
      "check_id": "core_l1_snapshot_integrity",
      "guarantee_id": "conversation_l1_integrity",
      "owner": "RenCrow_CORE",
      "purpose": "offline restore integrity",
      "target": "conversation_l1 snapshot",
      "phase": "backup",
      "consumer": "backup completion gate",
      "failure_action": "blocked",
      "cost": "high",
      "safety_gate": false
    }
  ]
}`

func writeTestManifest(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "core-checks.json")
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return path
}

func TestDefaultManifestPathHonorsExplicitOwnerConfiguration(t *testing.T) {
	want := filepath.Join(t.TempDir(), "core-runtime.json")
	t.Setenv("RENCROW_CORE_CHECK_MANIFEST", want)
	if got := defaultManifestPath(); got != want {
		t.Fatalf("defaultManifestPath() = %q, want %q", got, want)
	}
}

func plannerOutputForManifest(t *testing.T, manifestPath string, now time.Time) []byte {
	t.Helper()
	// The implementation must invoke the external planner. Unit tests inject
	// this deterministic planner result so the owner runner can be tested
	// without copying Tools planner logic into CORE.
	return []byte(`{"schema_version":1,"status":"ready","purpose":"operational_status","phase":"runtime","evaluated_at":"` + now.UTC().Format(time.RFC3339) + `","plan_revision":"sha256:fixture","included":[{"check_id":"core_health","reason":"required"},{"check_id":"core_l1_lightweight_query","reason":"required"},{"check_id":"core_readiness","reason":"required"}],"excluded":[],"deferred":[{"check_id":"core_l1_snapshot_integrity","classifications":["wrong_phase"],"reason":"backup check is deferred during runtime"}],"errors":[]}`)
}

func TestRuntimeRunnerExecutesOnlyIncludedChecksAndDefersSnapshotIntegrity(t *testing.T) {
	var healthHits, readinessHits, l1Hits, snapshotHits atomic.Int32
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
			snapshotHits.Add(1)
			http.Error(w, "must not run during runtime", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	now := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	manifestPath := writeTestManifest(t, testManifest)
	planner := func(context.Context, []string) ([]byte, error) {
		return plannerOutputForManifest(t, manifestPath, now), nil
	}
	var out bytes.Buffer
	receipt, err := runRunner(context.Background(), runnerOptions{
		ManifestPath: manifestPath,
		CoreURL:      srv.URL,
		Phase:        "runtime",
		Now:          now,
		Planner:      planner,
		HTTPClient:   srv.Client(),
	}, &out)
	if err != nil {
		t.Fatalf("runRunner: %v", err)
	}
	if receipt.Status != "passed" {
		t.Fatalf("receipt status=%q: %+v", receipt.Status, receipt)
	}
	if len(receipt.Results) != 3 {
		t.Fatalf("results=%d, want 3: %+v", len(receipt.Results), receipt.Results)
	}
	if healthHits.Load() != 1 || readinessHits.Load() != 1 || l1Hits.Load() != 1 {
		t.Fatalf("unexpected route hits: health=%d readiness=%d l1=%d", healthHits.Load(), readinessHits.Load(), l1Hits.Load())
	}
	if snapshotHits.Load() != 0 {
		t.Fatalf("backup-only integrity route was invoked: %d", snapshotHits.Load())
	}
	if receipt.PlanRevision != "sha256:fixture" {
		t.Fatalf("plan revision = %q", receipt.PlanRevision)
	}
	var encoded map[string]any
	if err := json.Unmarshal(out.Bytes(), &encoded); err != nil {
		t.Fatalf("receipt is not JSON: %v", err)
	}
	if _, ok := encoded["results"]; !ok {
		t.Fatalf("receipt missing results: %s", out.String())
	}
}

func TestRuntimeRunnerFailsClosedForBlockedPlanWithoutExecutingChecks(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "must not run", http.StatusInternalServerError)
	}))
	defer srv.Close()

	now := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	manifestPath := writeTestManifest(t, testManifest)
	planner := func(context.Context, []string) ([]byte, error) {
		return []byte(`{"schema_version":1,"status":"blocked","purpose":"operational_status","phase":"runtime","evaluated_at":"2026-08-25T00:00:00Z","plan_revision":"sha256:blocked","included":[],"excluded":[],"deferred":[],"errors":["malformed safety gate"]}`), nil
	}
	_, err := runRunner(context.Background(), runnerOptions{
		ManifestPath: manifestPath,
		CoreURL:      srv.URL,
		Phase:        "runtime",
		Now:          now,
		Planner:      planner,
		HTTPClient:   srv.Client(),
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("expected blocked error, got %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("blocked plan executed checks: %d", hits.Load())
	}
}

func TestRuntimeRunnerFailsClosedForUnknownIncludedCheck(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifest)
	planner := func(context.Context, []string) ([]byte, error) {
		return []byte(`{"schema_version":1,"status":"ready","purpose":"operational_status","phase":"runtime","evaluated_at":"2026-08-25T00:00:00Z","plan_revision":"sha256:unknown","included":[{"check_id":"core_not_allowlisted","reason":"tampered"}],"excluded":[],"deferred":[],"errors":[]}`), nil
	}
	_, err := runRunner(context.Background(), runnerOptions{
		ManifestPath: manifestPath,
		CoreURL:      "http://127.0.0.1:1",
		Phase:        "runtime",
		Now:          time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC),
		Planner:      planner,
		HTTPClient:   &http.Client{Timeout: time.Second},
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "not allowlisted") {
		t.Fatalf("expected unknown check rejection, got %v", err)
	}
}

func TestRuntimeRunnerFailsClosedForTamperedPlanShape(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifest)
	planner := func(context.Context, []string) ([]byte, error) {
		return []byte(`{"schema_version":1,"status":"ready","purpose":"operational_status","phase":"backup","evaluated_at":"2026-08-25T00:00:00Z","plan_revision":"sha256:tampered","included":[],"excluded":[],"deferred":[],"errors":[]}`), nil
	}
	_, err := runRunner(context.Background(), runnerOptions{
		ManifestPath: manifestPath,
		CoreURL:      "http://127.0.0.1:1",
		Phase:        "runtime",
		Now:          time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC),
		Planner:      planner,
		HTTPClient:   &http.Client{Timeout: time.Second},
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "purpose/phase") {
		t.Fatalf("expected tampered plan rejection, got %v", err)
	}
}
