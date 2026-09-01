package dcimigration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestCreateProjectedL1SnapshotPreservesRawDCIAndProjectsCanonicalMetadata(t *testing.T) {
	snapshot := makeTestSnapshot(t, "l1-projection-happy")
	invalidSnippet := "raw evidence " + string([]byte{0xc3, 0xc3})
	updateEvidenceSnippetAcrossSources(t, snapshot, invalidSnippet)
	addProjectionNonDCIRow(t, filepath.Join(snapshot, "source-l1"))
	addProjectionMetadata(t, snapshot)

	currentSource := filepath.Join(snapshot, "source-l1")
	archiveSource := filepath.Join(snapshot, "source-archive")
	beforeCurrentHash := fileBytesHash(t, currentSource)
	beforeArchiveHash := fileBytesHash(t, archiveSource)
	report := classifyTestMigrationSnapshot(t, snapshot, ExpectedCounts{
		Searches: 1, ReadEvents: 1, EvidenceEvents: 1, TotalEvents: 4,
		NormalizedTextValues: 1, InvalidUTF8Bytes: 2,
	})
	currentTarget := filepath.Join(t.TempDir(), "projected-current")
	archiveTarget := filepath.Join(t.TempDir(), "projected-archive")

	currentEvidence, err := createProjectedL1Snapshot(context.Background(), currentSource, currentTarget, false, report.Snapshot, report.Plan)
	if err != nil {
		t.Fatalf("create current projected L1 snapshot: %v", err)
	}
	archiveEvidence, err := createProjectedL1Snapshot(context.Background(), archiveSource, archiveTarget, true, report.Snapshot, report.Plan)
	if err != nil {
		t.Fatalf("create archive projected L1 snapshot: %v", err)
	}
	assertProjectionEvidence(t, currentEvidence, 1, 1)
	assertProjectionEvidence(t, archiveEvidence, 1, 0)
	assertProjectionFile(t, currentTarget)
	assertProjectionFile(t, archiveTarget)

	ids := report.Plan.evidence["legacy-evidence-1"]
	expectedRawHash := rawTextSHA256(invalidSnippet)
	currentRow := readProjectedRowForTest(t, currentTarget, "l1_staging_item")
	archiveRow := readProjectedRowForTest(t, archiveTarget, "l1_staging_item_archive")
	for name, row := range map[string]projectionTestRow{"current": currentRow, "archive": archiveRow} {
		if row.EventID != string(ids.createdEventID) {
			t.Errorf("%s projected event_id = %q, want %q", name, row.EventID, ids.createdEventID)
		}
		wantID := fmt.Sprintf("kb:dci:%s:%s", ids.createdEventID, expectedRawHash[:12])
		if row.ID != wantID {
			t.Errorf("%s projected id = %q, want %q", name, row.ID, wantID)
		}
		if row.RawText != invalidSnippet {
			t.Errorf("%s raw_text changed or normalized: %q", name, row.RawText)
		}
		if row.RawHash != expectedRawHash {
			t.Errorf("%s raw_hash = %q, want %q", name, row.RawHash, expectedRawHash)
		}
		metadata, err := decodeMetadata(row.MetaJSON)
		if err != nil {
			t.Fatalf("decode %s projected metadata: %v", name, err)
		}
		assertCanonicalProjectionMetadata(t, name, metadata, report, true)
	}
	currentRegistry := readProjectedRegistryMetadataForTest(t, currentTarget)
	assertCanonicalProjectionMetadata(t, "registry", currentRegistry, report, false)
	if currentRegistry["registry_unrelated"] != "keep" {
		t.Fatalf("unrelated registry metadata was not preserved: %#v", currentRegistry)
	}
	assertProjectionNonDCIRow(t, currentTarget)
	if got := fileBytesHash(t, currentSource); got != beforeCurrentHash {
		t.Fatalf("current source changed: before=%s after=%s", beforeCurrentHash, got)
	}
	if got := fileBytesHash(t, archiveSource); got != beforeArchiveHash {
		t.Fatalf("archive source changed: before=%s after=%s", beforeArchiveHash, got)
	}
}

func TestCreateProjectedL1SnapshotWithNoClassifiedRowsSucceeds(t *testing.T) {
	snapshot := makeTestSnapshot(t, "l1-projection-no-rows")
	for _, update := range []struct {
		path  string
		query string
	}{
		{path: filepath.Join(snapshot, "source-l1"), query: `UPDATE l1_staging_item SET kind=?, namespace=?, meta_json=?`},
		{path: filepath.Join(snapshot, "source-archive"), query: `UPDATE l1_staging_item_archive SET kind=?, namespace=?, meta_json=?`},
	} {
		db := openTestDB(t, update.path)
		mustExec(t, db, update.query, "news", "kb:news", `{}`)
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
	db := openTestDB(t, filepath.Join(snapshot, "source-l1"))
	mustExec(t, db, `UPDATE l1_source_registry SET kind=?, meta_json=?`, "news", `{}`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	report := classifyTestMigrationSnapshot(t, snapshot, ExpectedCounts{Searches: 1, ReadEvents: 1, EvidenceEvents: 1, TotalEvents: 4})
	currentTarget := filepath.Join(t.TempDir(), "projected-current")
	archiveTarget := filepath.Join(t.TempDir(), "projected-archive")
	currentEvidence, err := createProjectedL1Snapshot(context.Background(), filepath.Join(snapshot, "source-l1"), currentTarget, false, report.Snapshot, report.Plan)
	if err != nil {
		t.Fatalf("create no-row current projection: %v", err)
	}
	archiveEvidence, err := createProjectedL1Snapshot(context.Background(), filepath.Join(snapshot, "source-archive"), archiveTarget, true, report.Snapshot, report.Plan)
	if err != nil {
		t.Fatalf("create no-row archive projection: %v", err)
	}
	if currentEvidence.DCIStagingRows != 0 || currentEvidence.RegistryRows != 0 || archiveEvidence.DCIStagingRows != 0 || archiveEvidence.RegistryRows != 0 {
		t.Fatalf("no-row projection counts = current=%#v archive=%#v", currentEvidence, archiveEvidence)
	}
}

func TestCreateProjectedL1SnapshotRejectsInvalidPlanBeforeCreatingTarget(t *testing.T) {
	snapshot := makeTestSnapshot(t, "l1-projection-plan-coverage")
	report := classifyTestMigrationSnapshot(t, snapshot, ExpectedCounts{Searches: 1, ReadEvents: 1, EvidenceEvents: 1, TotalEvents: 4})
	tests := []struct {
		name   string
		mutate func(*migrationPlan)
	}{
		{name: "missing evidence", mutate: func(plan *migrationPlan) { delete(plan.evidence, "legacy-evidence-1") }},
		{name: "extra read", mutate: func(plan *migrationPlan) {
			plan.readEvents[readEventKey{searchID: "other", stepNo: 1}] = modulecore.EventID("evt_other")
		}},
		{name: "extra search", mutate: func(plan *migrationPlan) { plan.searches["other"] = searchMigrationIDs{} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := cloneMigrationPlanForTest(report.Plan)
			tt.mutate(&plan)
			target := filepath.Join(t.TempDir(), "projected")
			if _, err := createProjectedL1Snapshot(context.Background(), filepath.Join(snapshot, "source-l1"), target, false, report.Snapshot, plan); err == nil {
				t.Fatalf("invalid plan was accepted")
			}
			assertNoProjectionResidue(t, target)
		})
	}
}

func TestCreateProjectedL1SnapshotRejectsCollisionAndCleansClone(t *testing.T) {
	snapshot := makeTestSnapshot(t, "l1-projection-collision")
	report := classifyTestMigrationSnapshot(t, snapshot, ExpectedCounts{Searches: 1, ReadEvents: 1, EvidenceEvents: 1, TotalEvents: 4})
	ids := report.Plan.evidence["legacy-evidence-1"]
	collisionID := fmt.Sprintf("kb:dci:%s:%s", ids.createdEventID, rawTextSHA256("evidence text")[:12])
	db := openTestDB(t, filepath.Join(snapshot, "source-l1"))
	mustExec(t, db, `INSERT INTO l1_staging_item(id,kind,namespace,event_id,source_id,fetched_at,raw_text,raw_hash,validation_status,meta_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, collisionID, "news", "kb:news", "collision-event", "news-source", "2026-08-31T00:00:01Z", "news text", "news-hash", "pending", `{}`, "2026-08-31T00:00:01Z", "2026-08-31T00:00:01Z")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "projected")
	if _, err := createProjectedL1Snapshot(context.Background(), filepath.Join(snapshot, "source-l1"), target, false, report.Snapshot, report.Plan); err == nil {
		t.Fatal("canonical staging collision was accepted")
	}
	assertNoProjectionResidue(t, target)
}

func TestCreateProjectedL1SnapshotRejectsChangedLegacyTupleAndCleansClone(t *testing.T) {
	snapshot := makeTestSnapshot(t, "l1-projection-old-tuple")
	report := classifyTestMigrationSnapshot(t, snapshot, ExpectedCounts{Searches: 1, ReadEvents: 1, EvidenceEvents: 1, TotalEvents: 4})
	db := openTestDB(t, filepath.Join(snapshot, "source-l1"))
	mustExec(t, db, `UPDATE l1_staging_item SET event_id=? WHERE id=?`, "changed-event", "staging-1")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "projected")
	if _, err := createProjectedL1Snapshot(context.Background(), filepath.Join(snapshot, "source-l1"), target, false, report.Snapshot, report.Plan); err == nil {
		t.Fatal("changed legacy staging tuple was accepted")
	}
	assertNoProjectionResidue(t, target)
}

func TestCreateProjectedL1SnapshotRejectsUnsafeExistingSymlinkAndCanceledTargets(t *testing.T) {
	snapshot := makeTestSnapshot(t, "l1-projection-paths")
	report := classifyTestMigrationSnapshot(t, snapshot, ExpectedCounts{Searches: 1, ReadEvents: 1, EvidenceEvents: 1, TotalEvents: 4})
	source := filepath.Join(snapshot, "source-l1")
	existing := filepath.Join(t.TempDir(), "existing")
	if err := os.WriteFile(existing, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := createProjectedL1Snapshot(context.Background(), source, existing, false, report.Snapshot, report.Plan); err == nil {
		t.Fatal("existing target was accepted")
	}
	if got, err := os.ReadFile(existing); err != nil || string(got) != "keep" {
		t.Fatalf("existing target changed: %q/%v", got, err)
	}
	if runtime.GOOS != "windows" {
		realTarget := filepath.Join(t.TempDir(), "real-target")
		if err := os.WriteFile(realTarget, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		linkTarget := filepath.Join(t.TempDir(), "link-target")
		if err := os.Symlink(realTarget, linkTarget); err != nil {
			t.Fatal(err)
		}
		if _, err := createProjectedL1Snapshot(context.Background(), source, linkTarget, false, report.Snapshot, report.Plan); err == nil {
			t.Fatal("symlink target was accepted")
		}
		if got, err := os.ReadFile(realTarget); err != nil || string(got) != "keep" {
			t.Fatalf("symlink target referent changed: %q/%v", got, err)
		}
	}
	canceledTarget := filepath.Join(t.TempDir(), "canceled")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := createProjectedL1Snapshot(ctx, source, canceledTarget, false, report.Snapshot, report.Plan); err == nil {
		t.Fatal("canceled projection was accepted")
	}
	assertNoProjectionResidue(t, canceledTarget)
}

func TestDryRunRejectsClassifiedDCIStagingRawHashMismatch(t *testing.T) {
	snapshot := makeTestSnapshot(t, "l1-projection-raw-hash")
	db := openTestDB(t, filepath.Join(snapshot, "source-l1"))
	mustExec(t, db, `UPDATE l1_staging_item SET raw_hash=?`, strings.Repeat("0", 64))
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	manifest, err := runTestSnapshotResult(t, snapshot, "raw-hash-mismatch.json")
	if err == nil || manifest.Status != StatusBlocked || manifest.ErrorCode != "raw_hash_mismatch" {
		t.Fatalf("raw hash mismatch result = %#v, err=%v", manifest, err)
	}
}

type projectionTestRow struct {
	ID       string
	EventID  string
	SourceID string
	RawText  string
	RawHash  string
	MetaJSON string
}

func addProjectionNonDCIRow(t *testing.T, path string) {
	t.Helper()
	db := openTestDB(t, path)
	defer db.Close()
	mustExec(t, db, `INSERT INTO l1_staging_item(id,kind,namespace,event_id,source_id,fetched_at,raw_text,raw_hash,validation_status,meta_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, "news-1", "news", "kb:news", "news-event", "news-source", "2026-08-31T00:00:01Z", "news text", "news-hash", "pending", `{}`, "2026-08-31T00:00:01Z", "2026-08-31T00:00:01Z")
}

func addProjectionMetadata(t *testing.T, snapshot string) {
	t.Helper()
	currentDB := openTestDB(t, filepath.Join(snapshot, "source-l1"))
	mustExec(t, currentDB, `UPDATE l1_staging_item SET meta_json=? WHERE id=?`, `{"source_kind":"dci","search_event_id":"legacy-search-1","evidence_id":"legacy-evidence-1","file_path":"spec.md","line_start":1,"line_end":1,"heading":"Heading","reason":"matched","confidence":0.8,"unrelated":"keep"}`, "staging-1")
	mustExec(t, currentDB, `UPDATE l1_source_registry SET meta_json=?`, `{"source_kind":"dci","search_event_id":"legacy-search-1","evidence_id":"legacy-evidence-1","registry_unrelated":"keep"}`)
	if err := currentDB.Close(); err != nil {
		t.Fatal(err)
	}
	archiveDB := openTestDB(t, filepath.Join(snapshot, "source-archive"))
	mustExec(t, archiveDB, `UPDATE l1_staging_item_archive SET meta_json=?`, `{"source_kind":"dci","search_event_id":"legacy-search-1","evidence_id":"legacy-evidence-1","file_path":"spec.md","line_start":1,"line_end":1,"heading":"Heading","reason":"matched","confidence":0.8,"archive_unrelated":"keep"}`)
	if err := archiveDB.Close(); err != nil {
		t.Fatal(err)
	}
}

func readProjectedRowForTest(t *testing.T, path, table string) projectionTestRow {
	t.Helper()
	db := openTestDB(t, path)
	defer db.Close()
	var row projectionTestRow
	if err := db.QueryRow(`SELECT id,event_id,source_id,raw_text,raw_hash,meta_json FROM `+table+` WHERE source_id='source-1'`).Scan(&row.ID, &row.EventID, &row.SourceID, &row.RawText, &row.RawHash, &row.MetaJSON); err != nil {
		t.Fatalf("read projected %s row: %v", table, err)
	}
	return row
}

func readProjectedRegistryMetadataForTest(t *testing.T, path string) map[string]any {
	t.Helper()
	db := openTestDB(t, path)
	defer db.Close()
	var raw string
	if err := db.QueryRow(`SELECT meta_json FROM l1_source_registry WHERE source_id='source-1'`).Scan(&raw); err != nil {
		t.Fatalf("read projected registry row: %v", err)
	}
	metadata, err := decodeMetadata(raw)
	if err != nil {
		t.Fatalf("decode projected registry metadata: %v", err)
	}
	return metadata
}

func assertCanonicalProjectionMetadata(t *testing.T, name string, metadata map[string]any, report classificationReport, staging bool) {
	t.Helper()
	search := report.Plan.searches["legacy-search-1"]
	evidence := report.Plan.evidence["legacy-evidence-1"]
	if _, ok := metadata["search_event_id"]; ok {
		t.Errorf("%s retained legacy search_event_id: %#v", name, metadata)
	}
	want := map[string]any{
		"source_kind":               "dci",
		"search_action_id":          string(search.actionID),
		"trace_id":                  string(search.traceID),
		"evidence_id":               string(evidence.evidenceID),
		"evidence_created_event_id": string(evidence.createdEventID),
		"query":                     report.Snapshot.Searches["legacy-search-1"].Query,
	}
	for key, value := range want {
		if !reflect.DeepEqual(metadata[key], value) {
			t.Errorf("%s metadata[%q] = %#v, want %#v", name, key, metadata[key], value)
		}
	}
	if staging {
		if metadata["unrelated"] != "keep" && metadata["archive_unrelated"] != "keep" {
			t.Errorf("staging unrelated metadata was not preserved: %#v", metadata)
		}
		return
	}
	if metadata["registry_unrelated"] != "keep" {
		t.Errorf("registry unrelated metadata was not preserved: %#v", metadata)
	}
}

func assertProjectionEvidence(t *testing.T, evidence l1ProjectionEvidence, dciRows, registryRows int) {
	t.Helper()
	if evidence.DCIStagingRows != dciRows || evidence.RegistryRows != registryRows || evidence.CanonicalStagingRows != dciRows || evidence.CanonicalRegistryRows != registryRows {
		t.Fatalf("projection row evidence = %#v, want DCI=%d registry=%d", evidence, dciRows, registryRows)
	}
	if evidence.OldStagingRowsRemaining != 0 || evidence.RawTextHashMismatches != 0 || evidence.RawHashMismatches != 0 || evidence.PromotedReferences != 0 || evidence.OrphanRows != 0 || evidence.ForeignKeyViolations != 0 || evidence.QuickCheckOK != 1 || evidence.SidecarZero != 1 {
		t.Fatalf("projection integrity evidence = %#v", evidence)
	}
	if evidence.SourceSchemaSHA256 == "" || evidence.OutputSchemaSHA256 != evidence.SourceSchemaSHA256 || evidence.SourceNonDCISHA256 == "" || evidence.OutputNonDCISHA256 != evidence.SourceNonDCISHA256 {
		t.Fatalf("projection hash evidence = %#v", evidence)
	}
}

func assertProjectionFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("projected file permissions = %o, want 600", info.Mode().Perm())
	}
	for _, suffix := range sqliteSidecarSuffixes {
		if _, err := os.Lstat(path + suffix); !os.IsNotExist(err) {
			t.Fatalf("projected sidecar %s exists: %v", suffix, err)
		}
	}
}

func assertNoProjectionResidue(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("projection target residue %q: %v", path, err)
	}
	for _, suffix := range sqliteSidecarSuffixes {
		if _, err := os.Lstat(path + suffix); !os.IsNotExist(err) {
			t.Fatalf("projection sidecar residue %q: %v", path+suffix, err)
		}
	}
}

func assertProjectionNonDCIRow(t *testing.T, path string) {
	t.Helper()
	db := openTestDB(t, path)
	defer db.Close()
	var kind, namespace, eventID, rawText, rawHash, metadata string
	if err := db.QueryRow(`SELECT kind,namespace,event_id,raw_text,raw_hash,meta_json FROM l1_staging_item WHERE id='news-1'`).Scan(&kind, &namespace, &eventID, &rawText, &rawHash, &metadata); err != nil {
		t.Fatalf("read projected non-DCI row: %v", err)
	}
	if kind != "news" || namespace != "kb:news" || eventID != "news-event" || rawText != "news text" || rawHash != "news-hash" || metadata != `{}` {
		t.Fatalf("projected non-DCI row changed: %q %q %q %q %q %q", kind, namespace, eventID, rawText, rawHash, metadata)
	}
}
