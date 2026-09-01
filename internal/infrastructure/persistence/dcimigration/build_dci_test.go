package dcimigration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/dci"
)

func TestCreateBuiltDCIProducesExactAuthenticatedSnapshot(t *testing.T) {
	fixture := newBuildDCIFixture(t, false)
	evidence, err := createBuiltDCI(context.Background(), fixture.target, fixture.snapshot, fixture.plan)
	if err != nil {
		t.Fatalf("createBuiltDCI() error = %v", err)
	}
	if evidence.OutputSchemaSHA256 == "" || evidence.OutputLogicalSHA256 == "" {
		t.Fatalf("hash evidence = %#v", evidence)
	}
	if evidence.TraceRows != 1 || evidence.StepRows != 1 || evidence.EvidenceRows != 1 || evidence.QueryTermRows != 0 {
		t.Fatalf("row evidence = %#v", evidence)
	}
	if evidence.AuthenticatedTraces != 1 || evidence.LegacyUnattributedTraces != 0 {
		t.Fatalf("actor evidence = %#v", evidence)
	}
	if evidence.DistinctActionIDs != 1 || evidence.DistinctTraceIDs != 1 || evidence.DistinctStepEventIDs != 1 || evidence.DistinctEvidenceIDs != 1 || evidence.DistinctCreatedEventIDs != 1 {
		t.Fatalf("distinct identity evidence = %#v", evidence)
	}
	if evidence.LegacyKeyMarkers != 0 || evidence.OrphanActionRefs != 0 || evidence.ForeignKeyViolations != 0 || evidence.QuickCheckOK != 1 || evidence.SidecarZero != 1 {
		t.Fatalf("integrity evidence = %#v", evidence)
	}
	assertBuiltDCIFile(t, fixture.target)

	store, err := dci.NewSQLiteStore(fixture.target)
	if err != nil {
		t.Fatalf("open built DCI store: %v", err)
	}
	got, found, err := store.FindSearchResultByActionID(context.Background(), fixture.records[0].Result.Trace.ActionID)
	closeErr := store.Close()
	if err != nil || !found || closeErr != nil {
		t.Fatalf("owner lookup found=%v err=%v close=%v", found, err, closeErr)
	}
	if !equalBuildDCIResults(fixture.records[0].Result, got) {
		t.Fatalf("owner result differs from materialized result")
	}
	if got.Trace.IdempotencyKey != "" {
		t.Fatal("owner lookup returned a non-empty idempotency key")
	}

	db := openTestDB(t, fixture.target)
	defer db.Close()
	var createdAt string
	evidenceID := fixture.records[0].Result.Pack.Evidence[0].EvidenceID
	if err := db.QueryRow(`SELECT created_at FROM dci_evidence WHERE evidence_id = ?`, string(evidenceID)).Scan(&createdAt); err != nil {
		t.Fatalf("read historical evidence timestamp: %v", err)
	}
	if want := formatBuildDCITime(fixture.records[0].EvidenceCreatedAt[evidenceID]); createdAt != want {
		t.Fatalf("historical evidence created_at = %q, want %q", createdAt, want)
	}
}

func TestCreateBuiltDCIPreservesLegacyUnattributedActor(t *testing.T) {
	fixture := newBuildDCIFixture(t, true)
	evidence, err := createBuiltDCI(context.Background(), fixture.target, fixture.snapshot, fixture.plan)
	if err != nil {
		t.Fatalf("createBuiltDCI() error = %v", err)
	}
	if evidence.AuthenticatedTraces != 0 || evidence.LegacyUnattributedTraces != 1 {
		t.Fatalf("legacy actor evidence = %#v", evidence)
	}
	assertBuiltDCIFile(t, fixture.target)
}

func TestCreateBuiltDCIRejectsInvalidTargetsAndCanceledContext(t *testing.T) {
	tests := []struct {
		name string
		call func(*buildDCIFixture) error
	}{
		{
			name: "existing target",
			call: func(fixture *buildDCIFixture) error {
				if err := os.WriteFile(fixture.target, []byte("preserve target"), 0o600); err != nil {
					return err
				}
				_, err := createBuiltDCI(context.Background(), fixture.target, fixture.snapshot, fixture.plan)
				return err
			},
		},
		{
			name: "canceled context",
			call: func(fixture *buildDCIFixture) error {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				_, err := createBuiltDCI(ctx, fixture.target, fixture.snapshot, fixture.plan)
				return err
			},
		},
		{
			name: "nil context",
			call: func(fixture *buildDCIFixture) error {
				_, err := createBuiltDCI(nil, fixture.target, fixture.snapshot, fixture.plan)
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newBuildDCIFixture(t, false)
			if err := tt.call(&fixture); err == nil {
				t.Fatal("invalid target/context was accepted")
			}
			if tt.name == "existing target" {
				data, err := os.ReadFile(fixture.target)
				if err != nil || string(data) != "preserve target" {
					t.Fatalf("existing target changed=%q err=%v", data, err)
				}
				return
			}
			if _, err := os.Lstat(fixture.target); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("target residue after %s: %v", tt.name, err)
			}
		})
	}

	if runtime.GOOS == "windows" {
		return
	}
	fixture := newBuildDCIFixture(t, false)
	realTarget := filepath.Join(t.TempDir(), "real-target")
	if err := os.WriteFile(realTarget, []byte("preserve symlink target"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkTarget := filepath.Join(t.TempDir(), "symlink-target")
	if err := os.Symlink(realTarget, symlinkTarget); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := createBuiltDCI(context.Background(), symlinkTarget, fixture.snapshot, fixture.plan); err == nil {
		t.Fatal("symlink target was accepted")
	}
	if data, err := os.ReadFile(realTarget); err != nil || string(data) != "preserve symlink target" {
		t.Fatalf("symlink target referent changed=%q err=%v", data, err)
	}

	realParent := t.TempDir()
	linkedParent := filepath.Join(t.TempDir(), "parent-link")
	if err := os.Symlink(realParent, linkedParent); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	linkedTarget := filepath.Join(linkedParent, "nested-target")
	if _, err := createBuiltDCI(context.Background(), linkedTarget, fixture.snapshot, fixture.plan); err == nil {
		t.Fatal("target through symlinked parent was accepted")
	}
	if _, err := os.Lstat(filepath.Join(realParent, "nested-target")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlinked parent target residue: %v", err)
	}
}

func TestCreateBuiltDCIRejectsInvalidRetainedPlanBeforeOutput(t *testing.T) {
	base := newBuildDCIFixture(t, false)
	tests := []struct {
		name   string
		mutate func(*migrationPlan)
	}{
		{name: "missing event", mutate: func(plan *migrationPlan) { plan.Events = plan.Events[:len(plan.Events)-1] }},
		{name: "extra event", mutate: func(plan *migrationPlan) { plan.Events = append(plan.Events, plan.Events[0]) }},
		{name: "missing search mapping", mutate: func(plan *migrationPlan) { delete(plan.searches, "legacy-search-1") }},
		{name: "wrong actual count", mutate: func(plan *migrationPlan) { plan.actual.TotalEvents++ }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newBuildDCIFixture(t, false)
			plan := cloneMigrationPlanForTest(base.plan)
			tt.mutate(&plan)
			if _, err := createBuiltDCI(context.Background(), fixture.target, fixture.snapshot, plan); err == nil {
				t.Fatal("invalid retained plan was accepted")
			}
			if _, err := os.Lstat(fixture.target); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("target created for invalid retained plan: %v", err)
			}
		})
	}
}

func TestCreateBuiltDCICleansTargetAfterPostCreateFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, path string)
	}{
		{
			name: "schema drift",
			mutate: func(t *testing.T, path string) {
				db := openTestDB(t, path)
				mustExec(t, db, `CREATE TABLE unexpected_build_table (value TEXT)`)
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "row drift",
			mutate: func(t *testing.T, path string) {
				db := openTestDB(t, path)
				mustExec(t, db, `UPDATE dci_evidence SET snippet = 'tampered evidence'`)
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "orphan action reference",
			mutate: func(t *testing.T, path string) {
				db := openTestDB(t, path)
				mustExec(t, db, `PRAGMA foreign_keys = OFF`)
				mustExec(t, db, `INSERT INTO dci_query_terms(action_id, term, term_type, parent_term, created_at) VALUES(?,?,?,?,?)`, "orphan-action", "orphan", "derived", "orphan", "2026-08-31T00:00:00Z")
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "timestamp drift",
			mutate: func(t *testing.T, path string) {
				db := openTestDB(t, path)
				mustExec(t, db, `UPDATE dci_evidence SET created_at = '2026-09-01T00:00:00Z'`)
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newBuildDCIFixture(t, false)
			original := buildDCIAfterCreate
			buildDCIAfterCreate = func(path string) error {
				tt.mutate(t, path)
				return nil
			}
			t.Cleanup(func() { buildDCIAfterCreate = original })
			if _, err := createBuiltDCI(context.Background(), fixture.target, fixture.snapshot, fixture.plan); err == nil {
				t.Fatal("post-create drift was accepted")
			}
			assertBuiltDCITargetClean(t, fixture.target)
		})
	}

	fixture := newBuildDCIFixture(t, false)
	original := buildDCIAfterCreate
	buildDCIAfterCreate = func(path string) error {
		if err := os.WriteFile(path+"-wal", []byte("sidecar residue"), 0o600); err != nil {
			return err
		}
		return errors.New("post-create failure with path payload and action ID")
	}
	t.Cleanup(func() { buildDCIAfterCreate = original })
	if _, err := createBuiltDCI(context.Background(), fixture.target, fixture.snapshot, fixture.plan); err == nil {
		t.Fatal("injected post-create failure was not returned")
	}
	assertBuiltDCITargetClean(t, fixture.target)
}

func TestCreateBuiltDCIErrorsAreBoundedAndIdentityFree(t *testing.T) {
	fixture := newBuildDCIFixture(t, false)
	original := buildDCIAfterCreate
	buildDCIAfterCreate = func(path string) error {
		return errors.New(path + " payload evidence text legacy-search-1 action ID")
	}
	t.Cleanup(func() { buildDCIAfterCreate = original })
	_, err := createBuiltDCI(context.Background(), fixture.target, fixture.snapshot, fixture.plan)
	if err == nil {
		t.Fatal("injected failure was not returned")
	}
	for _, forbidden := range []string{fixture.target, "evidence text", "legacy-search-1", string(fixture.records[0].Result.Trace.ActionID)} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("error leaked %q: %v", forbidden, err)
		}
	}
}

type buildDCIFixture struct {
	snapshot sourceSnapshot
	plan     migrationPlan
	target   string
	records  []dci.MigrationRecord
}

func newBuildDCIFixture(t *testing.T, legacy bool) buildDCIFixture {
	t.Helper()
	snapshotRoot := makeTestSnapshot(t, "build-dci")
	if legacy {
		updateLegacyActor(t, filepath.Join(snapshotRoot, "source-dci"), "Worker")
		writeJSONLTestActor(t, filepath.Join(snapshotRoot, "source-dci-jsonl"), "legacy-search-1", "Worker")
	}
	report := classifyTestMigrationSnapshot(t, snapshotRoot, ExpectedCounts{Searches: 1, ReadEvents: 1, EvidenceEvents: 1, TotalEvents: 4})
	records, err := materializeMigrationRecords(context.Background(), report.Snapshot, report.Plan)
	if err != nil {
		t.Fatalf("materializeMigrationRecords() error = %v", err)
	}
	return buildDCIFixture{
		snapshot: report.Snapshot,
		plan:     report.Plan,
		target:   filepath.Join(t.TempDir(), "dci-built"),
		records:  records,
	}
}

func assertBuiltDCIFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		t.Fatal("built DCI target is not a regular non-symlink file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("built DCI target permissions = %o, want 600", info.Mode().Perm())
	}
	for _, suffix := range sqliteSidecarSuffixes {
		if _, err := os.Lstat(path + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("built DCI sidecar %q exists: %v", suffix, err)
		}
	}
}

func assertBuiltDCITargetClean(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed DCI target remains or lstat failed: %v", err)
	}
	for _, suffix := range sqliteSidecarSuffixes {
		if _, err := os.Lstat(path + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed DCI sidecar %q remains or lstat failed: %v", suffix, err)
		}
	}
}
