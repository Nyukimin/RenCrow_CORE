package dcimigration

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestCreateBuiltEventStorePreservesSourceAndAppendsExactPlan(t *testing.T) {
	fixture := newBuildEventStoreFixture(t)
	sourceBefore, err := os.ReadFile(fixture.source)
	if err != nil {
		t.Fatal(err)
	}

	evidence, err := createBuiltEventStore(context.Background(), fixture.source, fixture.target, fixture.snapshot, fixture.plan)
	if err != nil {
		t.Fatalf("createBuiltEventStore() error = %v", err)
	}
	if evidence.SourceSchemaSHA256 == "" || evidence.OutputSchemaSHA256 != evidence.SourceSchemaSHA256 || evidence.SourceNonDCISHA256 == "" || evidence.OutputNonDCISHA256 != evidence.SourceNonDCISHA256 || evidence.OutputLogicalSHA256 == "" {
		t.Fatalf("hash evidence = %#v", evidence)
	}
	if evidence.SourceEnvelopeCount != 2 || evidence.PlannedEnvelopeCount != len(fixture.plan.Events) || evidence.OutputEnvelopeCount != evidence.SourceEnvelopeCount+evidence.PlannedEnvelopeCount {
		t.Fatalf("envelope evidence = %#v", evidence)
	}
	if evidence.SourceDependencyCount != 1 || evidence.PlannedDependencyCount != 3 || evidence.OutputDependencyCount != 4 {
		t.Fatalf("dependency evidence = %#v", evidence)
	}
	if evidence.PlannedDCIEventCount != len(fixture.plan.Events) || evidence.OutputDCIEventCount != len(fixture.plan.Events) || evidence.ForeignKeyViolations != 0 || evidence.QuickCheckOK != 1 || evidence.SidecarZero != 1 {
		t.Fatalf("integrity evidence = %#v", evidence)
	}

	sourceAfter, err := os.ReadFile(fixture.source)
	if err != nil {
		t.Fatal(err)
	}
	if string(sourceAfter) != string(sourceBefore) {
		t.Fatal("Event Store source bytes changed")
	}
	info, err := os.Lstat(fixture.target)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("target permissions = %o, want 600", info.Mode().Perm())
	}
	for _, suffix := range sqliteSidecarSuffixes {
		if _, err := os.Lstat(fixture.target + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("target sidecar %q exists: %v", suffix, err)
		}
	}

	output, err := readBuildEventStorePath(context.Background(), fixture.target, false)
	if err != nil {
		t.Fatalf("read built Event Store: %v", err)
	}
	if err := verifyBuildEventStoreOutput(fixture.sourceRows, output, fixture.planned); err != nil {
		t.Fatalf("verify built Event Store: %v", err)
	}
}

func TestCreateBuiltEventStoreRejectsInvalidPathsAndCanceledContext(t *testing.T) {
	fixture := newBuildEventStoreFixture(t)
	tests := []struct {
		name   string
		call   func() error
		assert func(t *testing.T)
	}{
		{
			name: "existing target",
			call: func() error {
				if err := os.WriteFile(fixture.target, []byte("existing"), 0o600); err != nil {
					return err
				}
				_, err := createBuiltEventStore(context.Background(), fixture.source, fixture.target, fixture.snapshot, fixture.plan)
				return err
			},
			assert: func(t *testing.T) {
				if _, err := os.Stat(fixture.target); err != nil {
					t.Fatalf("existing target was removed: %v", err)
				}
			},
		},
		{
			name: "source target alias",
			call: func() error {
				_, err := createBuiltEventStore(context.Background(), fixture.source, fixture.source, fixture.snapshot, fixture.plan)
				return err
			},
		},
		{
			name: "canceled context",
			call: func() error {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				_, err := createBuiltEventStore(ctx, fixture.source, fixture.target, fixture.snapshot, fixture.plan)
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture = newBuildEventStoreFixture(t)
			if err := tt.call(); err == nil {
				t.Fatal("createBuiltEventStore() unexpectedly succeeded")
			}
			if tt.assert != nil {
				tt.assert(t)
			}
			if _, err := os.Lstat(fixture.target); tt.name != "existing target" && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("target residue after %s: %v", tt.name, err)
			}
		})
	}

	if runtime.GOOS == "windows" {
		return
	}
	fixture = newBuildEventStoreFixture(t)
	link := filepath.Join(t.TempDir(), "event-store-link")
	if err := os.Symlink(fixture.source, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := createBuiltEventStore(context.Background(), link, fixture.target, fixture.snapshot, fixture.plan); err == nil {
		t.Fatal("symlink source was accepted")
	}
	fixture = newBuildEventStoreFixture(t)
	buildParent := filepath.Join(t.TempDir(), "parent-link")
	if err := os.Symlink(filepath.Dir(fixture.target), buildParent); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	linkTarget := filepath.Join(buildParent, filepath.Base(fixture.target))
	if _, err := createBuiltEventStore(context.Background(), fixture.source, linkTarget, fixture.snapshot, fixture.plan); err == nil {
		t.Fatal("target through symlinked parent was accepted")
	}
}

func TestCreateBuiltEventStoreRejectsSourceThroughSymlinkedParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink parent invariant is Unix-specific")
	}
	fixture := newBuildEventStoreFixture(t)
	sourceBefore, err := os.ReadFile(fixture.source)
	if err != nil {
		t.Fatal(err)
	}
	linkParent := filepath.Join(t.TempDir(), "source-parent-link")
	if err := os.Symlink(filepath.Dir(fixture.source), linkParent); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	linkedSource := filepath.Join(linkParent, filepath.Base(fixture.source))
	if _, err := createBuiltEventStore(context.Background(), linkedSource, fixture.target, fixture.snapshot, fixture.plan); err == nil {
		t.Fatal("source through symlinked parent was accepted")
	}
	sourceAfter, err := os.ReadFile(fixture.source)
	if err != nil {
		t.Fatal(err)
	}
	if string(sourceAfter) != string(sourceBefore) {
		t.Fatal("Event Store source bytes changed after rejected path")
	}
	if _, err := os.Lstat(fixture.target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target residue after rejected path: %v", err)
	}
}

func TestCreateBuiltEventStoreRejectsCollisionAndInvalidPlansBeforeOutput(t *testing.T) {
	fixture := newBuildEventStoreFixture(t)
	tests := []struct {
		name   string
		mutate func(*migrationPlan)
	}{
		{
			name: "missing event",
			mutate: func(plan *migrationPlan) {
				plan.Events = plan.Events[:len(plan.Events)-1]
			},
		},
		{
			name: "extra event",
			mutate: func(plan *migrationPlan) {
				plan.Events = append(plan.Events, plan.Events[0])
			},
		},
		{
			name: "non-DCI event",
			mutate: func(plan *migrationPlan) {
				plan.Events[0].EventType = "conversation.message.received"
			},
		},
		{
			name: "collision",
			mutate: func(plan *migrationPlan) {
				for index := range plan.Events {
					plan.Events[index].EventID = modulecore.EventID(firstBuildEventStoreSourceID(t, fixture.source))
					break
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			local := newBuildEventStoreFixture(t)
			plan := cloneMigrationPlanForTest(local.plan)
			tt.mutate(&plan)
			if _, err := createBuiltEventStore(context.Background(), local.source, local.target, local.snapshot, plan); err == nil {
				t.Fatal("invalid plan was accepted")
			}
			if _, err := os.Lstat(local.target); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("target was created for invalid plan: %v", err)
			}
		})
	}
}

func TestCreateBuiltEventStoreRejectsPreexistingDCIHistory(t *testing.T) {
	fixture := newBuildEventStoreFixture(t)
	store, err := eventstore.NewSQLiteStore(fixture.source)
	if err != nil {
		t.Fatal(err)
	}
	dciEvent := modulecore.NewRootEventEnvelope("dci", "dci.search.started", time.Now().UTC(), map[string]any{"status": "started"})
	if err := store.Append(context.Background(), dciEvent); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := createBuiltEventStore(context.Background(), fixture.source, fixture.target, fixture.snapshot, fixture.plan); err == nil {
		t.Fatal("preexisting DCI history was accepted")
	}
	if _, err := os.Lstat(fixture.target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target was created after source rejection: %v", err)
	}
}

func TestCreateBuiltEventStoreCleansTargetAndSidecarsAfterPostAppendFailure(t *testing.T) {
	fixture := newBuildEventStoreFixture(t)
	original := buildEventStoreAfterAppend
	buildEventStoreAfterAppend = func(path string) error {
		if err := os.WriteFile(path+"-wal", []byte("test residue"), 0o600); err != nil {
			return err
		}
		return errors.New("forced post-append failure")
	}
	t.Cleanup(func() { buildEventStoreAfterAppend = original })
	if _, err := createBuiltEventStore(context.Background(), fixture.source, fixture.target, fixture.snapshot, fixture.plan); err == nil {
		t.Fatal("post-append failure was not returned")
	}
	if _, err := os.Lstat(fixture.target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target residue after failure: %v", err)
	}
	for _, suffix := range sqliteSidecarSuffixes {
		if _, err := os.Lstat(fixture.target + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("target sidecar residue %q: %v", suffix, err)
		}
	}
}

func TestCreateBuiltEventStoreRejectsPostAppendSchemaAndNonDCIDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, path string)
	}{
		{
			name: "schema drift",
			mutate: func(t *testing.T, path string) {
				db, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				_, err = db.Exec(`CREATE TABLE unexpected_build_table (value TEXT)`)
				closeErr := db.Close()
				if err != nil {
					t.Fatal(err)
				}
				if closeErr != nil {
					t.Fatal(closeErr)
				}
			},
		},
		{
			name: "non-DCI event drift",
			mutate: func(t *testing.T, path string) {
				store, err := eventstore.NewSQLiteStore(path)
				if err != nil {
					t.Fatal(err)
				}
				extra := modulecore.NewRootEventEnvelope("orchestrator", "conversation.message.received", time.Now().UTC(), map[string]any{"extra": true})
				if err := store.Append(context.Background(), extra); err != nil {
					_ = store.Close()
					t.Fatal(err)
				}
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newBuildEventStoreFixture(t)
			original := buildEventStoreAfterAppend
			buildEventStoreAfterAppend = func(path string) error {
				tt.mutate(t, path)
				return nil
			}
			t.Cleanup(func() { buildEventStoreAfterAppend = original })
			if _, err := createBuiltEventStore(context.Background(), fixture.source, fixture.target, fixture.snapshot, fixture.plan); err == nil {
				t.Fatal("post-append drift was accepted")
			}
			if _, err := os.Lstat(fixture.target); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("target residue after %s: %v", tt.name, err)
			}
		})
	}
}

func TestVerifyBuiltEventStoreRejectsTamperedOutput(t *testing.T) {
	fixture := newBuildEventStoreFixture(t)
	if _, err := createBuiltEventStore(context.Background(), fixture.source, fixture.target, fixture.snapshot, fixture.plan); err != nil {
		t.Fatalf("createBuiltEventStore() error = %v", err)
	}
	db, err := sql.Open("sqlite", fixture.target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TRIGGER event_envelope_append_only_update`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE event_envelope SET component_id = 'tampered' WHERE event_type LIKE 'dci.%'`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	verificationDB, err := openSQLiteReadOnly(context.Background(), fixture.target)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := readBuildEventStore(context.Background(), verificationDB, false)
	closeErr := verificationDB.Close()
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if err == nil {
		if err := verifyBuildEventStoreOutput(fixture.sourceRows, rows, fixture.planned); err == nil {
			t.Fatal("tampered output was accepted")
		}
	}
}

type buildEventStoreFixture struct {
	source     string
	target     string
	snapshot   sourceSnapshot
	plan       migrationPlan
	sourceRows buildEventStoreRows
	planned    map[modulecore.EventID]modulecore.EventEnvelope
}

func newBuildEventStoreFixture(t *testing.T) buildEventStoreFixture {
	t.Helper()
	sourceRoot := makeTestSnapshot(t, "build-event-store-source")
	eventStorePath := filepath.Join(sourceRoot, "source-event-store")
	if err := os.Remove(eventStorePath); err != nil {
		t.Fatal(err)
	}
	store, err := eventstore.NewSQLiteStore(eventStorePath)
	if err != nil {
		t.Fatal(err)
	}
	traceID := modulecore.NewTraceID()
	root := modulecore.NewRootEventEnvelope("orchestrator", "conversation.message.received", time.Date(2026, 8, 31, 1, 0, 0, 0, time.UTC), map[string]any{"kind": "root"})
	root.TraceID = traceID
	child := modulecore.NewEventEnvelope(traceID, root.EventID, nil, "orchestrator", "conversation.message.completed", root.OccurredAt.Add(time.Second), map[string]any{"kind": "child"})
	if err := store.AppendBatch(context.Background(), []modulecore.EventEnvelope{root, child}); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	snapshotRoot := filepath.Join(t.TempDir(), "captured")
	_, err = Capture(context.Background(), CaptureOptions{
		SnapshotDir: snapshotRoot, LiveDCI: filepath.Join(sourceRoot, "source-dci"), LiveDCIJSONL: filepath.Join(sourceRoot, "source-dci-jsonl"),
		LiveEventStore: eventStorePath, LiveL1: filepath.Join(sourceRoot, "source-l1"), LiveArchive: filepath.Join(sourceRoot, "source-archive"),
	})
	if err != nil {
		t.Fatalf("Capture() error = %v", err)
	}
	manifest, err := DryRun(context.Background(), Options{
		SnapshotDir: snapshotRoot, SourceDCI: "source-dci", SourceDCIJSONL: "source-dci-jsonl", SourceEventStore: "source-event-store", SourceL1: "source-l1", SourceArchive: "source-archive", Manifest: "dry-run.json",
		Expected: ExpectedCounts{Searches: 1, ReadEvents: 1, EvidenceEvents: 1, TotalEvents: 4}, AgentIDs: testAgentIDs,
	})
	if err != nil {
		t.Fatalf("DryRun() error = %v", err)
	}
	if manifest.Status != StatusReady {
		t.Fatalf("manifest status = %#v", manifest)
	}
	options := buildOptions{
		SnapshotDir: snapshotRoot, BuildDir: filepath.Join(t.TempDir(), "event-store-built"), CaptureReceipt: CaptureReceiptFilename, DryRunManifest: "dry-run.json", AgentIDs: append([]string(nil), testAgentIDs...),
	}
	prepared, err := prepareBuild(context.Background(), options)
	if err != nil {
		t.Fatalf("prepareBuild() error = %v", err)
	}
	sourceRows, err := readBuildEventStorePath(context.Background(), prepared.paths.sources.eventStore, true)
	if err != nil {
		t.Fatalf("read source Event Store: %v", err)
	}
	planned, err := validateBuildEventStorePlan(prepared.snapshot, prepared.plan)
	if err != nil {
		t.Fatalf("validate plan: %v", err)
	}
	return buildEventStoreFixture{source: prepared.paths.sources.eventStore, target: options.BuildDir, snapshot: prepared.snapshot, plan: prepared.plan, sourceRows: sourceRows, planned: planned}
}

func firstBuildEventStoreSourceID(t *testing.T, path string) string {
	t.Helper()
	rows, err := readBuildEventStorePath(context.Background(), path, true)
	if err != nil {
		t.Fatal(err)
	}
	for eventID := range rows.envelopes {
		return string(eventID)
	}
	t.Fatal("source Event Store has no events")
	return ""
}
