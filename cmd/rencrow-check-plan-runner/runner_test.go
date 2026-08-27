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

const testManifestV2 = `{
  "schema_version": 2,
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
      "safety_gate": false,
      "coverage": ["readiness"],
      "executor": {"kind": "owner_cli", "command_id": "core-health"},
      "receipt_schema": "rencrow.check-receipt.v1",
      "surfaces": ["browser_ui"]
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
      "safety_gate": false,
      "coverage": ["durability"],
      "executor": {"kind": "owner_cli", "command_id": "core-l1-snapshot-integrity"},
      "receipt_schema": "rencrow.check-receipt.v1",
      "surfaces": ["backup_restore"]
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

func TestRuntimeRunnerProjectsV2ManifestForRequestedPhase(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestV2)
	sourceBefore, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read source manifest: %v", err)
	}
	now := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	var plannerArgs []string
	var plannerInputPath string
	planner := func(_ context.Context, args []string) ([]byte, error) {
		plannerArgs = append([]string(nil), args...)
		if len(args) != 5 || args[0] != "plan" || args[1] != "--input" || args[3] != "--now" {
			t.Fatalf("unexpected planner args: %v", args)
		}
		plannerInputPath = args[2]
		projected, err := os.ReadFile(plannerInputPath)
		if err != nil {
			t.Fatalf("read projected planner input: %v", err)
		}
		var request struct {
			SchemaVersion int                          `json:"schema_version"`
			Purpose       string                       `json:"purpose"`
			Phase         string                       `json:"phase"`
			Checks        []map[string]json.RawMessage `json:"checks"`
		}
		decoder := json.NewDecoder(bytes.NewReader(projected))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			t.Fatalf("decode projected planner input: %v", err)
		}
		if request.SchemaVersion != 1 || request.Purpose != "operational_status" || request.Phase != "startup" {
			t.Fatalf("unexpected projected request: %+v", request)
		}
		if len(request.Checks) != 2 {
			t.Fatalf("projected checks=%d, want 2", len(request.Checks))
		}
		for _, check := range request.Checks {
			for _, extension := range []string{"coverage", "executor", "receipt_schema", "surfaces"} {
				if _, ok := check[extension]; ok {
					t.Fatalf("v2 extension %q leaked into v1 planner input: %s", extension, string(projected))
				}
			}
		}
		return []byte(`{"schema_version":1,"status":"ready","purpose":"operational_status","phase":"startup","evaluated_at":"2026-08-27T00:00:00Z","plan_revision":"sha256:v2-projection","included":[],"excluded":[],"deferred":[{"check_id":"core_health","classifications":["wrong_phase"],"reason":"runtime check is deferred for startup"},{"check_id":"core_l1_snapshot_integrity","classifications":["wrong_phase"],"reason":"backup check is deferred for startup"}],"errors":[]}`), nil
	}

	receipt, err := runRunner(context.Background(), runnerOptions{
		ManifestPath: manifestPath,
		CoreURL:      "http://127.0.0.1:1",
		Phase:        "startup",
		Now:          now,
		Planner:      planner,
		HTTPClient:   &http.Client{Timeout: time.Second},
	}, io.Discard)
	if err != nil {
		t.Fatalf("runRunner: %v", err)
	}
	if receipt.Status != "passed" || receipt.Phase != "startup" || len(receipt.Deferred) != 2 {
		t.Fatalf("unexpected v2 projection receipt: %+v", receipt)
	}
	if len(plannerArgs) != 5 || plannerArgs[4] != now.Format(time.RFC3339) {
		t.Fatalf("planner evaluation args = %v", plannerArgs)
	}
	if plannerInputPath == manifestPath {
		t.Fatal("planner received the source manifest instead of a v1 projection")
	}
	if _, err := os.Stat(plannerInputPath); !os.IsNotExist(err) {
		t.Fatalf("projected planner input was not cleaned up: path=%s err=%v", plannerInputPath, err)
	}
	sourceAfter, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read source manifest after run: %v", err)
	}
	if !bytes.Equal(sourceBefore, sourceAfter) {
		t.Fatalf("source manifest was mutated")
	}
}

func TestLoadV2ManifestRejectsUnknownOrMalformedExtensions(t *testing.T) {
	tests := []struct {
		name string
		edit func(map[string]any)
		want string
	}{
		{
			name: "unknown check field",
			edit: func(check map[string]any) { check["unexpected"] = true },
			want: "unknown field",
		},
		{
			name: "invalid executor command id",
			edit: func(check map[string]any) {
				check["executor"] = map[string]any{"kind": "owner_cli", "command_id": "not a stable id"}
			},
			want: "executor.command_id",
		},
		{
			name: "invalid receipt schema",
			edit: func(check map[string]any) { check["receipt_schema"] = "rencrow.check-receipt.v9" },
			want: "receipt_schema",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var document map[string]any
			if err := json.Unmarshal([]byte(testManifestV2), &document); err != nil {
				t.Fatalf("decode fixture: %v", err)
			}
			checks, ok := document["checks"].([]any)
			if !ok || len(checks) == 0 {
				t.Fatal("fixture checks missing")
			}
			check, ok := checks[0].(map[string]any)
			if !ok {
				t.Fatal("fixture check is not an object")
			}
			tt.edit(check)
			contents, err := json.Marshal(document)
			if err != nil {
				t.Fatalf("encode fixture: %v", err)
			}
			_, err = loadCheckManifest(writeTestManifest(t, string(contents)))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("load error = %v, want substring %q", err, tt.want)
			}
		})
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
