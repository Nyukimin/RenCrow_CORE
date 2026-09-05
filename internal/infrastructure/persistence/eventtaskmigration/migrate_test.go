package eventtaskmigration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appresilience "github.com/Nyukimin/RenCrow_CORE/internal/application/resilience"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	executionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/execution"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	_ "modernc.org/sqlite"
)

type migrationFixture struct {
	dir, eventSource, l1Source, reportSource         string
	target, reportTarget, dryManifest, applyManifest string
	resilienceSource, resilienceTarget               string
	events                                           []modulecore.EventEnvelope
	receiptTask                                      modulecore.TaskID
}

func TestDryRunApplyPreserveGraphAndUseReceiptOrDerivedTask(t *testing.T) {
	fixture := newMigrationFixture(t)
	dry, err := Run(context.Background(), fixture.options(ModeDryRun))
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if dry.Status != StatusReady || dry.TotalEvents != 3 || dry.OrchestratorEvents != 2 || dry.MappedByReceipt != 1 || dry.MappedDerived != 1 || dry.NoTaskEvents != 1 || dry.Dependencies != 1 {
		t.Fatalf("unexpected dry receipt: %#v", dry)
	}
	if dry.ExecutionReportRows != 3 || dry.MappedReportByEvent != 2 || dry.MappedReportDerived != 1 {
		t.Fatalf("unexpected execution report receipt: %#v", dry)
	}
	if dry.ResilienceFiles != 3 || dry.ResilienceIncidents != 1 || dry.MappedRepairByReport != 1 || dry.SourceResilienceSHA256 == "" || dry.CanonicalResilienceSHA256 == "" {
		t.Fatalf("unexpected resilience receipt: %#v", dry)
	}
	if _, err := os.Stat(fixture.target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run created/opened target: %v", err)
	}
	if _, err := os.Stat(fixture.reportTarget); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run created execution report target: %v", err)
	}
	if _, err := os.Stat(fixture.resilienceTarget); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run created resilience target: %v", err)
	}
	assertMode(t, fixture.dryManifest, 0o600)

	applyOptions := fixture.options(ModeApply)
	applyOptions.DryRunManifest = fixture.dryManifest
	applied, err := Run(context.Background(), applyOptions)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied.Status != StatusApplied {
		t.Fatalf("apply status = %q", applied.Status)
	}
	if err := comparePlanFields(dry, applied); err != nil {
		t.Fatalf("dry/apply receipts differ: %v", err)
	}
	assertMode(t, fixture.applyManifest, 0o600)
	assertMode(t, fixture.target, 0o600)
	assertMode(t, fixture.reportTarget, 0o600)
	assertMode(t, fixture.resilienceTarget, 0o700)
	migratedIncident, err := (appresilience.Store{Root: fixture.resilienceTarget}).Load("incident-a")
	if err != nil {
		t.Fatalf("load migrated resilience incident: %v", err)
	}
	wantRepairTask, _ := modulecore.NewMigrationID(modulecore.CanonicalTaskID, "execution_report", "job_id", "report-only")
	if migratedIncident.RepairTaskID != modulecore.TaskID(wantRepairTask) {
		t.Fatalf("repair TaskID = %q, want exact execution report mapping %q", migratedIncident.RepairTaskID, wantRepairTask)
	}
	if migratedIncident.Kind != "panic" || migratedIncident.OccurrenceCount != 1 || migratedIncident.Details["keep"] != "yes" {
		t.Fatalf("non-identity incident fields changed: %#v", migratedIncident)
	}
	evidence, err := os.ReadFile(filepath.Join(fixture.resilienceTarget, "incidents", "incident-a", "doctor.json"))
	if err != nil || string(evidence) != "evidence bytes\n" {
		t.Fatalf("resilience evidence not byte-preserved: %q err=%v", evidence, err)
	}
	monitor, err := os.ReadFile(filepath.Join(fixture.resilienceTarget, "monitor.json"))
	if err != nil || string(monitor) != "{\"keep\":true}\n" {
		t.Fatalf("root resilience file not byte-preserved: %q err=%v", monitor, err)
	}
	reportStore, err := executionpersistence.NewJSONLReportStore(fixture.reportTarget)
	if err != nil {
		t.Fatalf("open migrated execution reports: %v", err)
	}
	reports, err := reportStore.ListRecent(context.Background(), 10)
	if err != nil || len(reports) != 3 {
		t.Fatalf("current report store read: rows=%d err=%v", len(reports), err)
	}
	if _, err := reportStore.GetByTaskID(context.Background(), fixture.receiptTask); err != nil {
		t.Fatalf("current report store TaskID lookup: %v", err)
	}
	reportBytes, err := os.ReadFile(fixture.reportTarget)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(reportBytes, []byte(`"job_id"`)) || !bytes.Contains(reportBytes, []byte(`"task_id"`)) {
		t.Fatalf("execution report identities not migrated: %s", reportBytes)
	}
	lines := bytes.Split(bytes.TrimSpace(reportBytes), []byte("\n"))
	var reportOnly map[string]any
	if err := json.Unmarshal(lines[2], &reportOnly); err != nil {
		t.Fatal(err)
	}
	wantReportOnly, _ := modulecore.NewMigrationID(modulecore.CanonicalTaskID, "execution_report", "job_id", "report-only")
	if reportOnly["task_id"] != wantReportOnly || reportOnly["goal"] != "third" {
		t.Fatalf("report-only deterministic mapping/preservation failed: %#v", reportOnly)
	}

	store, err := eventstore.NewSQLiteStore(fixture.target)
	if err != nil {
		t.Fatalf("open migrated target: %v", err)
	}
	defer store.Close()
	first, found, err := store.GetByID(context.Background(), fixture.events[0].EventID)
	if err != nil || !found {
		t.Fatalf("read first: found=%v err=%v", found, err)
	}
	if first.EventSeq != 1 || first.TaskID != fixture.receiptTask || first.CausationEventID != "" || len(first.DependencyEventIDs) != 0 {
		t.Fatalf("first identity mismatch: %#v", first)
	}
	if _, ok := first.Payload["job_id"]; ok {
		t.Fatalf("legacy job_id survived: %#v", first.Payload)
	}
	if first.Payload["task_id"] != string(fixture.receiptTask) || first.Payload["event_seq"] != float64(1) && first.Payload["event_seq"] != int64(1) {
		t.Fatalf("first payload not migrated: %#v", first.Payload)
	}
	second, found, err := store.GetByID(context.Background(), fixture.events[1].EventID)
	if err != nil || !found {
		t.Fatalf("read second: found=%v err=%v", found, err)
	}
	if second.EventSeq != 2 || second.TaskID == "" || second.TaskID == fixture.receiptTask || second.CausationEventID != "" {
		t.Fatalf("derived/graph mismatch: %#v", second)
	}
	third, found, err := store.GetByID(context.Background(), fixture.events[2].EventID)
	if err != nil || !found {
		t.Fatalf("read third: found=%v err=%v", found, err)
	}
	if third.EventSeq != 3 || third.Payload["job_id"] != "domain-job-unchanged" || third.CausationEventID != second.EventID {
		t.Fatalf("non-orchestrator payload changed: %#v", third)
	}
	probe := modulecore.NewRootEventEnvelope("probe", "probe.next", time.Now().UTC(), map[string]any{"ok": true})
	persisted, err := store.AppendSequenced(context.Background(), probe)
	if err != nil {
		t.Fatalf("append next live event: %v", err)
	}
	if persisted.EventSeq != 4 {
		t.Fatalf("next sequence = %d, want 4", persisted.EventSeq)
	}
}

func TestDerivedTaskIsStableAndSeparatedByTrace(t *testing.T) {
	traceA, traceB := modulecore.NewTraceID(), modulecore.NewTraceID()
	payloadA, payloadB := map[string]any{"job_id": "same"}, map[string]any{"job_id": "same"}
	derived := make(map[string]modulecore.TaskID)
	a1, _, err := migrateOrchestratorPayload(traceA, 1, payloadA, nil, derived)
	if err != nil {
		t.Fatal(err)
	}
	a2, _, err := migrateOrchestratorPayload(traceA, 2, map[string]any{"job_id": "same"}, nil, derived)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := migrateOrchestratorPayload(traceB, 3, payloadB, nil, derived)
	if err != nil {
		t.Fatal(err)
	}
	if a1 != a2 || a1 == b {
		t.Fatalf("derived mapping stability/separation failed: %q %q %q", a1, a2, b)
	}
}

func TestAmbiguousReceiptFailsClosed(t *testing.T) {
	fixture := newMigrationFixture(t)
	db := openWritable(t, fixture.l1Source)
	_, err := db.Exec(`INSERT INTO conversation_turn_receipt(trace_id, root_task_id) VALUES(?, ?)`, fixture.events[0].TraceID, modulecore.NewTaskID())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	receipt, err := Run(context.Background(), fixture.options(ModeDryRun))
	if err == nil || receipt.ErrorCode != "receipt_ambiguous" {
		t.Fatalf("ambiguous receipt result: receipt=%#v err=%v", receipt, err)
	}
}

func TestAmbiguousLegacyEventJobMappingFailsClosedAtExecutionReportJoin(t *testing.T) {
	fixture := newMigrationFixture(t)
	db := openWritable(t, fixture.eventSource)
	var raw string
	if err := db.QueryRow(`SELECT envelope_json FROM event_envelope WHERE rowid=2`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatal(err)
	}
	payload := envelope["payload"].(map[string]any)
	payload["job_id"] = "legacy-a"
	encoded, _ := json.Marshal(envelope)
	if _, err := db.Exec(`UPDATE event_envelope SET envelope_json=? WHERE rowid=2`, string(encoded)); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	receipt, err := Run(context.Background(), fixture.options(ModeDryRun))
	if err == nil || receipt.ErrorCode != "report_job_ambiguous" {
		t.Fatalf("receipt=%#v err=%v", receipt, err)
	}
}

func TestRepeatedLegacyEventJobAcrossTracesIsAllowedWithoutReportJoin(t *testing.T) {
	fixture := newMigrationFixture(t)
	db := openWritable(t, fixture.eventSource)
	var raw string
	if err := db.QueryRow(`SELECT envelope_json FROM event_envelope WHERE rowid=2`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatal(err)
	}
	envelope["payload"].(map[string]any)["job_id"] = "legacy-a"
	encoded, _ := json.Marshal(envelope)
	if _, err := db.Exec(`UPDATE event_envelope SET envelope_json=? WHERE rowid=2`, string(encoded)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reports := readReportObjects(t, fixture.reportSource)
	reports[0]["job_id"] = "report-without-event"
	writeReportObjects(t, fixture.reportSource, reports)
	receipt, err := Run(context.Background(), fixture.options(ModeDryRun))
	if err != nil || receipt.Status != StatusReady {
		t.Fatalf("receipt=%#v err=%v", receipt, err)
	}
}

func TestExecutionReportRowsFailClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, migrationFixture)
		code   string
	}{
		{"duplicate_job", func(t *testing.T, f migrationFixture) {
			rows := readReportObjects(t, f.reportSource)
			rows[1]["job_id"] = rows[0]["job_id"]
			writeReportObjects(t, f.reportSource, rows)
		}, "report_job_duplicate"},
		{"already_task_id", func(t *testing.T, f migrationFixture) {
			rows := readReportObjects(t, f.reportSource)
			rows[0]["task_id"] = modulecore.NewTaskID()
			writeReportObjects(t, f.reportSource, rows)
		}, "report_row_invalid"},
		{"unknown_field", func(t *testing.T, f migrationFixture) {
			rows := readReportObjects(t, f.reportSource)
			rows[0]["unknown_owner_field"] = true
			writeReportObjects(t, f.reportSource, rows)
		}, "report_contract_invalid"},
		{"missing_goal", func(t *testing.T, f migrationFixture) {
			rows := readReportObjects(t, f.reportSource)
			delete(rows[0], "goal")
			writeReportObjects(t, f.reportSource, rows)
		}, "report_contract_invalid"},
		{"duplicate_json_key", func(t *testing.T, f migrationFixture) {
			raw := `{"job_id":"one","job_id":"two","goal":"g","status":"passed","created_at":"2026-09-05T01:00:00Z","finished_at":"2026-09-05T01:00:01Z"}` + "\n"
			if err := os.WriteFile(f.reportSource, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "report_row_invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMigrationFixture(t)
			test.mutate(t, fixture)
			receipt, err := Run(context.Background(), fixture.options(ModeDryRun))
			if err == nil || receipt.ErrorCode != test.code {
				t.Fatalf("receipt=%#v err=%v", receipt, err)
			}
		})
	}
}

func TestResilienceRepairMappingAndTreeSafetyFailClosed(t *testing.T) {
	t.Run("missing_report_mapping", func(t *testing.T) {
		fixture := newMigrationFixture(t)
		path := filepath.Join(fixture.resilienceSource, "incidents", "incident-a", "incident.json")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var incident map[string]any
		if err := json.Unmarshal(data, &incident); err != nil {
			t.Fatal(err)
		}
		incident["repair_job_id"] = "not-in-reports"
		encoded, _ := json.Marshal(incident)
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		receipt, err := Run(context.Background(), fixture.options(ModeDryRun))
		if err == nil || receipt.ErrorCode != "resilience_repair_mapping_missing" {
			t.Fatalf("receipt=%#v err=%v", receipt, err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		fixture := newMigrationFixture(t)
		if err := os.Symlink("doctor.json", filepath.Join(fixture.resilienceSource, "incidents", "incident-a", "doctor-link")); err != nil {
			t.Fatal(err)
		}
		receipt, err := Run(context.Background(), fixture.options(ModeDryRun))
		if err == nil || receipt.ErrorCode != "resilience_source_invalid" {
			t.Fatalf("receipt=%#v err=%v", receipt, err)
		}
	})
	t.Run("unsupported_nested_directory", func(t *testing.T) {
		fixture := newMigrationFixture(t)
		if err := os.Mkdir(filepath.Join(fixture.resilienceSource, "incidents", "incident-a", "nested"), 0o700); err != nil {
			t.Fatal(err)
		}
		receipt, err := Run(context.Background(), fixture.options(ModeDryRun))
		if err == nil || receipt.ErrorCode != "resilience_source_invalid" {
			t.Fatalf("receipt=%#v err=%v", receipt, err)
		}
	})
	t.Run("duplicate_incident_key", func(t *testing.T) {
		fixture := newMigrationFixture(t)
		path := filepath.Join(fixture.resilienceSource, "incidents", "incident-a", "incident.json")
		raw := `{"signature":"incident-a","signature":"duplicate","kind":"panic","status":"repair_requested","first_seen":"2026-09-05T01:00:00Z","last_seen":"2026-09-05T01:01:00Z","occurrence_count":1,"repair_job_id":"report-only","repair_attempts":1}`
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		receipt, err := Run(context.Background(), fixture.options(ModeDryRun))
		if err == nil || receipt.ErrorCode != "resilience_incident_invalid" {
			t.Fatalf("receipt=%#v err=%v", receipt, err)
		}
	})
	t.Run("unknown_incident_field", func(t *testing.T) {
		fixture := newMigrationFixture(t)
		path := filepath.Join(fixture.resilienceSource, "incidents", "incident-a", "incident.json")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var incident map[string]any
		if err := json.Unmarshal(data, &incident); err != nil {
			t.Fatal(err)
		}
		incident["unknown_field"] = true
		encoded, _ := json.Marshal(incident)
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		receipt, err := Run(context.Background(), fixture.options(ModeDryRun))
		if err == nil || receipt.ErrorCode != "resilience_contract_invalid" {
			t.Fatalf("receipt=%#v err=%v", receipt, err)
		}
	})
	t.Run("existing_target_prevents_all_writes", func(t *testing.T) {
		fixture := newMigrationFixture(t)
		if _, err := Run(context.Background(), fixture.options(ModeDryRun)); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(fixture.resilienceTarget, 0o700); err != nil {
			t.Fatal(err)
		}
		options := fixture.options(ModeApply)
		options.DryRunManifest = fixture.dryManifest
		receipt, err := Run(context.Background(), options)
		if err == nil || receipt.ErrorCode != "resilience_target_exists" {
			t.Fatalf("receipt=%#v err=%v", receipt, err)
		}
		if _, statErr := os.Stat(fixture.target); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("event target created before resilience preflight: %v", statErr)
		}
	})
}

func TestLateApplyFailureRemovesAllFreshTargets(t *testing.T) {
	fixture := newMigrationFixture(t)
	dryPaths, err := validateAndResolveOptions(fixture.options(ModeDryRun))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := prepare(context.Background(), dryPaths)
	if err != nil {
		t.Fatal(err)
	}
	plan.resilience.files[0].rel = filepath.Join("..", "escape")
	applyPaths := dryPaths
	applyPaths.manifest = fixture.applyManifest
	if err := applyAllFresh(context.Background(), applyPaths, plan); err == nil {
		t.Fatal("unsafe late resilience output unexpectedly applied")
	}
	for _, target := range []string{fixture.target, fixture.reportTarget, fixture.resilienceTarget} {
		if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed apply left target %s: %v", target, err)
		}
	}
}

func TestMalformedEnvelopeIndexAndEdgeFailClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, migrationFixture)
		code   string
	}{
		{"missing_schema_column", func(t *testing.T, f migrationFixture) {
			db := openWritable(t, f.l1Source)
			execSQL(t, db, `ALTER TABLE conversation_turn_receipt RENAME TO old_receipt`)
			execSQL(t, db, `CREATE TABLE conversation_turn_receipt(trace_id TEXT NOT NULL)`)
			_ = db.Close()
		}, "receipt_schema_invalid"},
		{"already_step09_schema", func(t *testing.T, f migrationFixture) {
			execDB(t, f.eventSource, `ALTER TABLE event_envelope ADD COLUMN event_seq INTEGER`)
		}, "event_schema_invalid"},
		{"malformed_json", func(t *testing.T, f migrationFixture) {
			execDB(t, f.eventSource, `UPDATE event_envelope SET envelope_json='{' WHERE rowid=1`)
		}, "envelope_invalid"},
		{"index_mismatch", func(t *testing.T, f migrationFixture) {
			execDB(t, f.eventSource, `UPDATE event_envelope SET component_id='wrong' WHERE rowid=1`)
		}, "index_mismatch"},
		{"edge_mismatch", func(t *testing.T, f migrationFixture) { execDB(t, f.eventSource, `DELETE FROM event_dependency`) }, "edge_mismatch"},
		{"malformed_edge", func(t *testing.T, f migrationFixture) {
			execDB(t, f.eventSource, `UPDATE event_dependency SET relation_type='invalid'`)
		}, "edge_invalid"},
		{"invalid_id", func(t *testing.T, f migrationFixture) {
			db := openWritable(t, f.eventSource)
			var raw string
			if err := db.QueryRow(`SELECT envelope_json FROM event_envelope WHERE rowid=1`).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var object map[string]any
			if err := json.Unmarshal([]byte(raw), &object); err != nil {
				t.Fatal(err)
			}
			object["event_id"] = "bad"
			encoded, _ := json.Marshal(object)
			if _, err := db.Exec(`UPDATE event_envelope SET event_id='bad', envelope_json=? WHERE rowid=1`, string(encoded)); err != nil {
				t.Fatal(err)
			}
			_ = db.Close()
		}, "envelope_invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMigrationFixture(t)
			test.mutate(t, fixture)
			receipt, err := Run(context.Background(), fixture.options(ModeDryRun))
			if err == nil || receipt.ErrorCode != test.code {
				t.Fatalf("receipt=%#v err=%v", receipt, err)
			}
		})
	}
}

func TestApplyRejectsChangedSourceAndExistingTargetOrSidecar(t *testing.T) {
	t.Run("changed_source", func(t *testing.T) {
		fixture := newMigrationFixture(t)
		if _, err := Run(context.Background(), fixture.options(ModeDryRun)); err != nil {
			t.Fatal(err)
		}
		execDB(t, fixture.l1Source, `INSERT INTO conversation_turn_receipt(trace_id, root_task_id) VALUES(?, ?)`, modulecore.NewTraceID(), modulecore.NewTaskID())
		options := fixture.options(ModeApply)
		options.DryRunManifest = fixture.dryManifest
		receipt, err := Run(context.Background(), options)
		if err == nil || receipt.ErrorCode != "manifest_mismatch" {
			t.Fatalf("receipt=%#v err=%v", receipt, err)
		}
	})
	t.Run("changed_execution_reports", func(t *testing.T) {
		fixture := newMigrationFixture(t)
		if _, err := Run(context.Background(), fixture.options(ModeDryRun)); err != nil {
			t.Fatal(err)
		}
		rows := readReportObjects(t, fixture.reportSource)
		rows[0]["goal"] = "changed after dry run"
		writeReportObjects(t, fixture.reportSource, rows)
		options := fixture.options(ModeApply)
		options.DryRunManifest = fixture.dryManifest
		receipt, err := Run(context.Background(), options)
		if err == nil || receipt.ErrorCode != "manifest_mismatch" {
			t.Fatalf("receipt=%#v err=%v", receipt, err)
		}
	})
	t.Run("changed_resilience", func(t *testing.T) {
		fixture := newMigrationFixture(t)
		if _, err := Run(context.Background(), fixture.options(ModeDryRun)); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(fixture.resilienceSource, "incidents", "incident-a", "doctor.json")
		if err := os.WriteFile(path, []byte("changed after dry run\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		options := fixture.options(ModeApply)
		options.DryRunManifest = fixture.dryManifest
		receipt, err := Run(context.Background(), options)
		if err == nil || receipt.ErrorCode != "manifest_mismatch" {
			t.Fatalf("receipt=%#v err=%v", receipt, err)
		}
	})
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		t.Run("existing"+strings.ReplaceAll(suffix, "-", "_"), func(t *testing.T) {
			fixture := newMigrationFixture(t)
			if _, err := Run(context.Background(), fixture.options(ModeDryRun)); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(fixture.target+suffix, []byte("occupied"), 0o600); err != nil {
				t.Fatal(err)
			}
			options := fixture.options(ModeApply)
			options.DryRunManifest = fixture.dryManifest
			receipt, err := Run(context.Background(), options)
			if err == nil || receipt.ErrorCode != "target_exists" {
				t.Fatalf("receipt=%#v err=%v", receipt, err)
			}
		})
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		t.Run("existing_report"+strings.ReplaceAll(suffix, "-", "_"), func(t *testing.T) {
			fixture := newMigrationFixture(t)
			if _, err := Run(context.Background(), fixture.options(ModeDryRun)); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(fixture.reportTarget+suffix, []byte("occupied"), 0o600); err != nil {
				t.Fatal(err)
			}
			options := fixture.options(ModeApply)
			options.DryRunManifest = fixture.dryManifest
			receipt, err := Run(context.Background(), options)
			if err == nil || receipt.ErrorCode != "report_target_exists" {
				t.Fatalf("receipt=%#v err=%v", receipt, err)
			}
			if _, statErr := os.Stat(fixture.target); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("event target created before report preflight: %v", statErr)
			}
		})
	}
}

func TestPathContainmentSymlinksAndTrailingManifestFail(t *testing.T) {
	t.Run("source_outside", func(t *testing.T) {
		fixture := newMigrationFixture(t)
		outside := filepath.Join(t.TempDir(), "outside.db")
		if err := copyFile(fixture.eventSource, outside); err != nil {
			t.Fatal(err)
		}
		options := fixture.options(ModeDryRun)
		options.SourceEventStore = outside
		if _, err := Run(context.Background(), options); err == nil {
			t.Fatal("outside source accepted")
		}
	})
	t.Run("source_symlink", func(t *testing.T) {
		fixture := newMigrationFixture(t)
		link := filepath.Join(fixture.dir, "event-link.db")
		if err := os.Symlink(fixture.eventSource, link); err != nil {
			t.Fatal(err)
		}
		options := fixture.options(ModeDryRun)
		options.SourceEventStore = link
		if _, err := Run(context.Background(), options); err == nil {
			t.Fatal("source symlink accepted")
		}
	})
	t.Run("report_source_symlink", func(t *testing.T) {
		fixture := newMigrationFixture(t)
		link := filepath.Join(fixture.dir, "report-link.jsonl")
		if err := os.Symlink(fixture.reportSource, link); err != nil {
			t.Fatal(err)
		}
		options := fixture.options(ModeDryRun)
		options.SourceExecutionReports = link
		if _, err := Run(context.Background(), options); err == nil {
			t.Fatal("execution report source symlink accepted")
		}
	})
	t.Run("target_inside_resilience_source", func(t *testing.T) {
		fixture := newMigrationFixture(t)
		options := fixture.options(ModeDryRun)
		options.TargetExecutionReports = filepath.Join(fixture.resilienceSource, "unsafe-target.jsonl")
		receipt, err := Run(context.Background(), options)
		if err == nil || receipt.ErrorCode != "path_alias" {
			t.Fatalf("receipt=%#v err=%v", receipt, err)
		}
	})
	t.Run("manifest_symlink", func(t *testing.T) {
		fixture := newMigrationFixture(t)
		real := filepath.Join(fixture.dir, "real-manifest.json")
		if err := os.WriteFile(real, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, fixture.dryManifest); err != nil {
			t.Fatal(err)
		}
		if _, err := Run(context.Background(), fixture.options(ModeDryRun)); err == nil {
			t.Fatal("manifest symlink accepted")
		}
	})
	t.Run("manifest_hardlink_to_source", func(t *testing.T) {
		fixture := newMigrationFixture(t)
		if err := os.Link(fixture.eventSource, fixture.dryManifest); err != nil {
			t.Fatal(err)
		}
		receipt, err := Run(context.Background(), fixture.options(ModeDryRun))
		if err == nil || receipt.ErrorCode != "path_alias" {
			t.Fatalf("receipt=%#v err=%v", receipt, err)
		}
	})
	t.Run("manifest_exact_source_does_not_replace_source", func(t *testing.T) {
		fixture := newMigrationFixture(t)
		before, err := hashFile(fixture.eventSource)
		if err != nil {
			t.Fatal(err)
		}
		options := fixture.options(ModeDryRun)
		options.Manifest = fixture.eventSource
		receipt, err := Run(context.Background(), options)
		if err == nil || receipt.ErrorCode != "path_alias" {
			t.Fatalf("receipt=%#v err=%v", receipt, err)
		}
		after, hashErr := hashFile(fixture.eventSource)
		if hashErr != nil || before != after {
			t.Fatalf("source changed by rejected manifest alias: before=%s after=%s err=%v", before, after, hashErr)
		}
	})
	t.Run("manifest_source_sidecar_path_rejected", func(t *testing.T) {
		fixture := newMigrationFixture(t)
		options := fixture.options(ModeDryRun)
		options.Manifest = fixture.eventSource + "-wal"
		receipt, err := Run(context.Background(), options)
		if err == nil || receipt.ErrorCode != "path_alias" {
			t.Fatalf("receipt=%#v err=%v", receipt, err)
		}
		if _, statErr := os.Lstat(options.Manifest); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("source sidecar path was created: %v", statErr)
		}
	})
	t.Run("trailing_dry_manifest", func(t *testing.T) {
		fixture := newMigrationFixture(t)
		if _, err := Run(context.Background(), fixture.options(ModeDryRun)); err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(fixture.dryManifest, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = file.WriteString("{}")
		_ = file.Close()
		options := fixture.options(ModeApply)
		options.DryRunManifest = fixture.dryManifest
		receipt, err := Run(context.Background(), options)
		if err == nil || receipt.ErrorCode != "dry_run_manifest_invalid" {
			t.Fatalf("receipt=%#v err=%v", receipt, err)
		}
	})
	t.Run("unsafe_dry_manifest_permissions", func(t *testing.T) {
		fixture := newMigrationFixture(t)
		if _, err := Run(context.Background(), fixture.options(ModeDryRun)); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(fixture.dryManifest, 0o644); err != nil {
			t.Fatal(err)
		}
		options := fixture.options(ModeApply)
		options.DryRunManifest = fixture.dryManifest
		receipt, err := Run(context.Background(), options)
		if err == nil || receipt.ErrorCode != "dry_run_manifest_invalid" {
			t.Fatalf("receipt=%#v err=%v", receipt, err)
		}
	})
}

func newMigrationFixture(t *testing.T) migrationFixture {
	t.Helper()
	dir := t.TempDir()
	traceReceipt, traceDerived := modulecore.NewTraceID(), modulecore.NewTraceID()
	receiptTask := modulecore.NewTaskID()
	first := modulecore.NewEventEnvelope(traceReceipt, "", nil, "orchestrator", "message.received", time.Date(2026, 9, 5, 1, 2, 3, 4, time.UTC), map[string]any{"seq": 101, "job_id": "legacy-a", "content": "keep"})
	first.EventID = modulecore.NewEventID()
	first.SessionID = modulecore.NewSessionID()
	first.MessageID = modulecore.NewMessageID()
	second := modulecore.NewEventEnvelope(traceDerived, "", nil, "orchestrator", "agent.response", time.Date(2026, 9, 5, 1, 2, 4, 5, time.UTC), map[string]any{"seq": 102, "job_id": "legacy-b", "nested": map[string]any{"keep": true}})
	second.EventID = modulecore.NewEventID()
	third := modulecore.NewEventEnvelope(traceDerived, second.EventID, nil, "scheduler", "scheduler.job", time.Date(2026, 9, 5, 1, 2, 5, 6, time.UTC), map[string]any{"job_id": "domain-job-unchanged"})
	third.EventID = modulecore.NewEventID()
	fixture := migrationFixture{
		dir: dir, eventSource: filepath.Join(dir, "events.db"), l1Source: filepath.Join(dir, "l1.db"), reportSource: filepath.Join(dir, "execution-report-source.jsonl"),
		target: filepath.Join(dir, "target.db"), reportTarget: filepath.Join(dir, "execution-report-target.jsonl"),
		dryManifest: filepath.Join(dir, "dry.json"), applyManifest: filepath.Join(dir, "apply.json"), events: []modulecore.EventEnvelope{first, second, third}, receiptTask: receiptTask,
		resilienceSource: filepath.Join(dir, "resilience-source"), resilienceTarget: filepath.Join(dir, "resilience-target"),
	}
	createLegacyEventStore(t, fixture.eventSource, fixture.events)
	createReceiptStore(t, fixture.l1Source, map[modulecore.TraceID]modulecore.TaskID{traceReceipt: receiptTask})
	createLegacyExecutionReports(t, fixture.reportSource)
	createLegacyResilience(t, fixture.resilienceSource)
	return fixture
}

func (f migrationFixture) options(mode string) Options {
	manifest := f.dryManifest
	if mode == ModeApply {
		manifest = f.applyManifest
	}
	return Options{Mode: mode, SnapshotDir: f.dir, SourceEventStore: f.eventSource, SourceConversationL1: f.l1Source, SourceExecutionReports: f.reportSource, SourceResilienceRoot: f.resilienceSource, TargetEventStore: f.target, TargetExecutionReports: f.reportTarget, TargetResilienceRoot: f.resilienceTarget, Manifest: manifest}
}

func createLegacyResilience(t *testing.T, root string) {
	t.Helper()
	incidentDir := filepath.Join(root, "incidents", "incident-a")
	if err := os.MkdirAll(incidentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	incident := map[string]any{
		"signature": "incident-a", "kind": "panic", "status": "repair_requested",
		"first_seen": "2026-09-05T01:00:00Z", "last_seen": "2026-09-05T01:01:00Z", "occurrence_count": 1,
		"repair_job_id": "report-only", "repair_attempts": 1, "repair_requested_at": "2026-09-05T01:02:00Z",
		"details": map[string]string{"keep": "yes"},
	}
	encoded, err := json.MarshalIndent(incident, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(incidentDir, "incident.json"), append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(incidentDir, "doctor.json"), []byte("evidence bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "monitor.json"), []byte("{\"keep\":true}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func createLegacyEventStore(t *testing.T, path string, events []modulecore.EventEnvelope) {
	t.Helper()
	db := openWritable(t, path)
	defer db.Close()
	execSQL(t, db, `CREATE TABLE event_envelope(event_id TEXT PRIMARY KEY NOT NULL, trace_id TEXT NOT NULL, schema_version TEXT NOT NULL, event_type TEXT NOT NULL, component_id TEXT NOT NULL, occurred_at TEXT NOT NULL, envelope_json TEXT NOT NULL)`)
	execSQL(t, db, `CREATE TABLE event_dependency(event_id TEXT NOT NULL, dependency_event_id TEXT NOT NULL, relation_type TEXT NOT NULL, PRIMARY KEY(event_id, dependency_event_id))`)
	for _, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		var object map[string]any
		if err := json.Unmarshal(encoded, &object); err != nil {
			t.Fatal(err)
		}
		delete(object, "event_seq")
		encoded, _ = json.Marshal(object)
		execSQL(t, db, `INSERT INTO event_envelope(event_id, trace_id, schema_version, event_type, component_id, occurred_at, envelope_json) VALUES(?,?,?,?,?,?,?)`, event.EventID, event.TraceID, event.SchemaVersion, event.EventType, event.ComponentID, event.OccurredAt.Format(time.RFC3339Nano), string(encoded))
		if event.CausationEventID != "" {
			execSQL(t, db, `INSERT INTO event_dependency VALUES(?,?,?)`, event.EventID, event.CausationEventID, "causation")
		}
		for _, dependency := range event.DependencyEventIDs {
			execSQL(t, db, `INSERT INTO event_dependency VALUES(?,?,?)`, event.EventID, dependency, "dependency")
		}
	}
}

func createReceiptStore(t *testing.T, path string, mappings map[modulecore.TraceID]modulecore.TaskID) {
	t.Helper()
	db := openWritable(t, path)
	defer db.Close()
	execSQL(t, db, `CREATE TABLE conversation_turn_receipt(trace_id TEXT NOT NULL, root_task_id TEXT NOT NULL)`)
	for traceID, taskID := range mappings {
		execSQL(t, db, `INSERT INTO conversation_turn_receipt VALUES(?,?)`, traceID, taskID)
	}
}

func createLegacyExecutionReports(t *testing.T, path string) {
	t.Helper()
	rows := []map[string]any{
		{"job_id": "legacy-a", "goal": "first", "status": "passed", "route": "CHAT", "created_at": "2026-09-05T01:02:03Z", "finished_at": "2026-09-05T01:02:04Z", "playback_exit_code": 0, "attempt_count": 1, "repair_count": 0},
		{"job_id": "legacy-b", "goal": "second", "status": "failed", "error": "kept", "created_at": "2026-09-05T01:03:03Z", "finished_at": "2026-09-05T01:03:04Z", "playback_exit_code": 0, "attempt_count": 2, "repair_count": 1},
		{"job_id": "report-only", "goal": "third", "status": "passed", "artifacts": []string{"a", "b"}, "created_at": "2026-09-05T01:04:03Z", "finished_at": "2026-09-05T01:04:04Z", "playback_exit_code": 0, "attempt_count": 1, "repair_count": 0},
	}
	var output bytes.Buffer
	for _, row := range rows {
		encoded, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		output.Write(encoded)
		output.WriteByte('\n')
	}
	if err := os.WriteFile(path, output.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readReportObjects(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	rows := make([]map[string]any, len(lines))
	for index, line := range lines {
		if err := json.Unmarshal(line, &rows[index]); err != nil {
			t.Fatal(err)
		}
	}
	return rows
}

func writeReportObjects(t *testing.T, path string, rows []map[string]any) {
	t.Helper()
	var output bytes.Buffer
	for _, row := range rows {
		encoded, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		output.Write(encoded)
		output.WriteByte('\n')
	}
	if err := os.WriteFile(path, output.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func openWritable(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	return db
}

func TestCleanupAppliedTargetsOnlyRemovesTargetsCreatedByThisApply(t *testing.T) {
	dir := t.TempDir()
	paths := resolvedPaths{
		targetEventStore:       filepath.Join(dir, "event.db"),
		targetExecutionReports: filepath.Join(dir, "reports.jsonl"),
		targetResilienceRoot:   filepath.Join(dir, "resilience"),
	}
	if err := os.WriteFile(paths.targetEventStore, []byte("external-event"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.targetExecutionReports, []byte("external-report"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(paths.targetResilienceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.targetResilienceRoot, "external"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := cleanupAppliedTargets(paths, false, false, false); err != nil {
		t.Fatalf("cleanup unowned targets: %v", err)
	}
	for _, path := range []string{paths.targetEventStore, paths.targetExecutionReports, filepath.Join(paths.targetResilienceRoot, "external")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("unowned target %s was removed: %v", path, err)
		}
	}
}

func execDB(t *testing.T, path, statement string, args ...any) {
	t.Helper()
	db := openWritable(t, path)
	defer db.Close()
	execSQL(t, db, statement, args...)
}

func execSQL(t *testing.T, db *sql.DB, statement string, args ...any) {
	t.Helper()
	if _, err := db.Exec(statement, args...); err != nil {
		t.Fatal(err)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("%s mode = %o, want %o", path, info.Mode().Perm(), want)
	}
}

func copyFile(source, target string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(target, data, 0o600)
}
