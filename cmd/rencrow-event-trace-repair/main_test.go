package main

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestRunDryRunProducesBoundedReceipt(t *testing.T) {
	snapshot := t.TempDir()
	source := filepath.Join(snapshot, "event_store.db")
	store, err := eventstore.NewSQLiteStore(source)
	if err != nil {
		t.Fatal(err)
	}
	jobID := "job-cli"
	for _, event := range []modulecore.EventEnvelope{
		modulecore.NewEventEnvelope(modulecore.NewTraceID(), "", nil, "orchestrator", "message.received", time.Now().UTC(), map[string]any{"job_id": jobID}),
		modulecore.NewEventEnvelope(modulecore.NewTraceID(), "", nil, "orchestrator", "agent.response", time.Now().UTC(), map[string]any{"job_id": jobID}),
	} {
		if err := store.Append(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err = run([]string{
		"--snapshot-dir", snapshot,
		"--source-store", source,
		"--output-store", filepath.Join(snapshot, "repaired.db"),
		"--manifest", filepath.Join(snapshot, "dry-run.json"),
		"--mode", "dry-run",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run: %v stderr=%s", err, stderr.String())
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"status":"ready"`)) || !bytes.Contains(stdout.Bytes(), []byte(`"repair_job_count":1`)) {
		t.Fatalf("unexpected stdout: %s", stdout.String())
	}
}

func TestRunApplyRequiresAllCutoverFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run([]string{"--mode", "apply"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("apply without cutover flags must fail")
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"status":"blocked"`)) {
		t.Fatalf("apply failure must emit bounded blocked receipt: %s", stdout.String())
	}
}
