package resilience

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestCaptureCoalescesSameSignature(t *testing.T) {
	store := Store{Root: t.TempDir()}
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	first, err := store.Capture(Observation{SignatureSource: "panic: nil pointer\nmain.run", Kind: "panic", At: now})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Capture(Observation{SignatureSource: "panic: nil pointer\nmain.run", Kind: "panic", At: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if first.Signature != second.Signature || second.OccurrenceCount != 2 {
		t.Fatalf("incident was not coalesced: first=%+v second=%+v", first, second)
	}
}

func TestUnresolvedIncidentIsNeverGarbageCollected(t *testing.T) {
	store := Store{Root: t.TempDir()}
	now := time.Now().UTC()
	incident, err := store.Capture(Observation{SignatureSource: "fatal", Kind: "fatal", At: now.Add(-90 * 24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	evidence := filepath.Join(store.IncidentDir(incident.Signature), "first.log.gz")
	if err := os.WriteFile(evidence, []byte("evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GC(now, 7*24*time.Hour, 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(evidence); err != nil {
		t.Fatalf("unresolved evidence was deleted: %v", err)
	}
}

func TestResolvedIncidentPrunesDetailsThenMetadata(t *testing.T) {
	store := Store{Root: t.TempDir()}
	now := time.Now().UTC()
	incident, err := store.Capture(Observation{SignatureSource: "panic x", Kind: "panic", At: now.Add(-40 * 24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	resolvedAt := now.Add(-8 * 24 * time.Hour)
	incident.Status = StatusResolved
	incident.ResolvedAt = &resolvedAt
	if err := store.Save(incident); err != nil {
		t.Fatal(err)
	}
	evidence := filepath.Join(store.IncidentDir(incident.Signature), "latest.log.gz")
	if err := os.WriteFile(evidence, []byte("evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := store.GC(now, 7*24*time.Hour, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.PrunedDetails) != 1 {
		t.Fatalf("expected details prune: %+v", result)
	}
	if _, err := os.Stat(evidence); !os.IsNotExist(err) {
		t.Fatalf("resolved details still exist: %v", err)
	}
	loaded, err := store.Load(incident.Signature)
	if err != nil || loaded.DetailsPrunedAt == nil {
		t.Fatalf("compact metadata was not retained: incident=%+v err=%v", loaded, err)
	}

	oldResolved := now.Add(-31 * 24 * time.Hour)
	loaded.ResolvedAt = &oldResolved
	if err := store.Save(loaded); err != nil {
		t.Fatal(err)
	}
	result, err = store.GC(now, 7*24*time.Hour, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Deleted) != 1 {
		t.Fatalf("expected metadata deletion: %+v", result)
	}
}

func TestRepairAttemptLimitAndStableResolution(t *testing.T) {
	store := Store{Root: t.TempDir()}
	now := time.Now().UTC()
	incident, err := store.Capture(Observation{SignatureSource: "hang", Kind: "hang", At: now})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRepairRequested(incident, modulecore.NewTaskID(), now, 2); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRepairFailed(incident, "failed"); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRepairRequested(incident, modulecore.NewTaskID(), now.Add(time.Hour), 2); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRepairCompleted(incident, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if resolved, err := store.ResolveStable(incident, now.Add(25*time.Hour), 24*time.Hour); err != nil || resolved {
		t.Fatalf("resolved too early: resolved=%v err=%v", resolved, err)
	}
	if resolved, err := store.ResolveStable(incident, now.Add(27*time.Hour), 24*time.Hour); err != nil || !resolved {
		t.Fatalf("did not resolve after stable window: resolved=%v err=%v", resolved, err)
	}
	if err := store.MarkRepairRequested(incident, modulecore.NewTaskID(), now, 2); err == nil {
		t.Fatal("expected automatic repair limit error")
	}
}

func TestIncidentPersistenceRequiresCanonicalRepairTaskID(t *testing.T) {
	store := Store{Root: t.TempDir()}
	now := time.Now().UTC()
	incident, err := store.Capture(Observation{SignatureSource: "repair identity", Kind: "panic", At: now})
	if err != nil {
		t.Fatal(err)
	}
	incident.Status = StatusRepairRequested
	if err := store.Save(incident); err == nil {
		t.Fatal("active repair without repair_task_id was accepted")
	}
	incident.RepairTaskID = modulecore.TaskID("legacy-job")
	if err := store.Save(incident); err == nil {
		t.Fatal("invalid repair_task_id was accepted")
	}
	legacy, _ := json.Marshal(map[string]any{
		"signature": incident.Signature, "kind": incident.Kind, "status": StatusRepairRequested,
		"first_seen": now, "last_seen": now, "occurrence_count": 1, "repair_job_id": "legacy-job", "repair_attempts": 1,
	})
	if err := os.WriteFile(store.metadataPath(incident.Signature), legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(incident.Signature); err == nil {
		t.Fatal("legacy repair_job_id compatibility read was accepted")
	}
}
