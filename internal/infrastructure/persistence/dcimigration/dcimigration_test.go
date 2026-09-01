package dcimigration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var testAgentIDs = []string{"mio", "shiro", "kuro", "midori"}

func TestNormalizeUTF8TextReplacesInvalidBytesOneForOne(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		want         string
		invalidBytes int
	}{
		{name: "one invalid byte", input: "prefix" + string([]byte{0xc3, 0x28}), want: "prefix\uFFFD(", invalidBytes: 1},
		{name: "two invalid bytes", input: "prefix" + string([]byte{0xc3, 0xc3}), want: "prefix\uFFFD\uFFFD", invalidBytes: 2},
		{name: "valid replacement rune", input: "prefix\uFFFD", want: "prefix\uFFFD", invalidBytes: 0},
		{name: "valid text unchanged", input: "valid UTF-8 日本語", want: "valid UTF-8 日本語", invalidBytes: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, invalidBytes := normalizeUTF8Text(tt.input)
			if got != tt.want || invalidBytes != tt.invalidBytes {
				t.Fatalf("normalizeUTF8Text() = %q/%d, want %q/%d", got, invalidBytes, tt.want, tt.invalidBytes)
			}
		})
	}
}

func TestDryRunClassifiesDeduplicatedDCISnapshotAndWritesBoundedReceipt(t *testing.T) {
	root := t.TempDir()
	snapshot := filepath.Join(root, "snapshot")
	if err := os.Mkdir(snapshot, 0o700); err != nil {
		t.Fatal(err)
	}
	legacySearch := "legacy-search-1"
	legacyEvidence := "legacy-evidence-1"
	writeLegacyDCITestDB(t, filepath.Join(snapshot, "source-dci"), legacySearch, legacyEvidence, false)
	writeJSONLTest(t, filepath.Join(snapshot, "source-dci-jsonl"), legacySearch)
	writeEventStoreTestDB(t, filepath.Join(snapshot, "source-event-store"), nil)
	writeL1TestDB(t, filepath.Join(snapshot, "source-l1"), legacySearch, legacyEvidence, true, false)
	writeArchiveTestDB(t, filepath.Join(snapshot, "source-archive"), legacySearch, legacyEvidence, true)

	manifest, err := DryRun(context.Background(), Options{
		SnapshotDir: snapshot, SourceDCI: "source-dci", SourceDCIJSONL: "source-dci-jsonl",
		SourceEventStore: "source-event-store", SourceL1: "source-l1", SourceArchive: "source-archive",
		Manifest: "manifest.json", Expected: ExpectedCounts{Searches: 1, ReadEvents: 1, EvidenceEvents: 1, TotalEvents: 4}, AgentIDs: testAgentIDs,
	})
	if err != nil {
		t.Fatalf("DryRun() error = %v, manifest = %#v", err, manifest)
	}
	if manifest.Status != StatusReady || manifest.Mode != ModeDryRun || manifest.SchemaVersion != ManifestSchemaVersion {
		t.Fatalf("manifest header = %#v", manifest)
	}
	if manifest.ActualCounts != (ActualCounts{Searches: 1, ReadEvents: 1, EvidenceEvents: 1, TotalEvents: 4}) {
		t.Fatalf("actual counts = %#v", manifest.ActualCounts)
	}
	if manifest.ActorClassification.AuthenticatedAgent != 1 || manifest.ActorClassification.LegacyUnattributed != 0 {
		t.Fatalf("actor classification = %#v", manifest.ActorClassification)
	}
	if manifest.DedupeCounts.SearchesRemoved != 1 || manifest.DedupeCounts.StepsRemoved != 1 || manifest.DedupeCounts.EvidenceRemoved != 2 || manifest.DedupeCounts.StagingDuplicates != 1 {
		t.Fatalf("dedupe counts = %#v", manifest.DedupeCounts)
	}
	if manifest.ExclusionReasonCounts == nil || len(manifest.ExclusionReasonCounts) != 0 {
		t.Fatalf("exclusion counts = %#v", manifest.ExclusionReasonCounts)
	}
	if manifest.MappingSHA256 == "" || manifest.ActionSetSHA256 == "" || manifest.TraceSetSHA256 == "" || manifest.EvidenceSetSHA256 == "" || manifest.EventSetSHA256 == "" || manifest.EventPlanSHA256 == "" {
		t.Fatalf("missing deterministic hashes = %#v", manifest)
	}
	data, err := os.ReadFile(filepath.Join(snapshot, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(data)) > maxManifestBytes || strings.Contains(string(data), legacySearch) || strings.Contains(string(data), legacyEvidence) || strings.Contains(string(data), "spec.md") || strings.Contains(string(data), "secret") {
		t.Fatalf("manifest leaked raw source data or exceeded bound: %s", data)
	}
	info, err := os.Stat(filepath.Join(snapshot, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("manifest permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestDryRunMappingAndIDSetsAreSnapshotRootIndependent(t *testing.T) {
	first := makeTestSnapshot(t, "first")
	second := makeTestSnapshot(t, "second")
	firstManifest := runTestSnapshot(t, first, filepath.Join(first, "manifest.json"))
	secondManifest := runTestSnapshot(t, second, filepath.Join(second, "manifest.json"))
	if firstManifest.MappingSHA256 != secondManifest.MappingSHA256 || firstManifest.ActionSetSHA256 != secondManifest.ActionSetSHA256 || firstManifest.TraceSetSHA256 != secondManifest.TraceSetSHA256 || firstManifest.EvidenceSetSHA256 != secondManifest.EvidenceSetSHA256 || firstManifest.EventSetSHA256 != secondManifest.EventSetSHA256 || firstManifest.EventPlanSHA256 != secondManifest.EventPlanSHA256 {
		t.Fatalf("root-dependent mapping hashes:\nfirst=%#v\nsecond=%#v", firstManifest, secondManifest)
	}
}

func TestEventPlanSHA256IsOrderIndependentAndContentSensitive(t *testing.T) {
	snapshot := makeTestSnapshot(t, "event-plan-hash")
	options := Options{
		SnapshotDir: snapshot, SourceDCI: "source-dci", SourceDCIJSONL: "source-dci-jsonl",
		SourceEventStore: "source-event-store", SourceL1: "source-l1", SourceArchive: "source-archive",
		Manifest: "event-plan-hash-check.json", Expected: ExpectedCounts{Searches: 1, ReadEvents: 1, EvidenceEvents: 1, TotalEvents: 4}, AgentIDs: testAgentIDs,
	}
	paths, err := resolvePaths(options)
	if err != nil {
		t.Fatalf("resolvePaths() error = %v", err)
	}
	report, err := classifySnapshot(context.Background(), paths, options)
	if err != nil {
		t.Fatalf("classifySnapshot() error = %v", err)
	}
	want := report.Manifest.EventPlanSHA256
	if want == "" {
		t.Fatal("classifySnapshot() returned an empty event plan hash")
	}

	reordered := append([]modulecore.EventEnvelope(nil), report.Plan.Events...)
	for left, right := 0, len(reordered)-1; left < right; left, right = left+1, right-1 {
		reordered[left], reordered[right] = reordered[right], reordered[left]
	}
	reorderedHash, err := hashEventPlan(reordered)
	if err != nil {
		t.Fatalf("hashEventPlan(reordered) error = %v", err)
	}
	if reorderedHash != want {
		t.Fatalf("event plan hash depends on slice order: want=%s got=%s", want, reorderedHash)
	}

	payloadChanged := append([]modulecore.EventEnvelope(nil), report.Plan.Events...)
	payload := make(map[string]any, len(payloadChanged[0].Payload)+1)
	for key, value := range payloadChanged[0].Payload {
		payload[key] = value
	}
	payload["status"] = "mutated"
	payloadChanged[0].Payload = payload
	payloadHash, err := hashEventPlan(payloadChanged)
	if err != nil {
		t.Fatalf("hashEventPlan(payload mutation) error = %v", err)
	}
	if payloadHash == want {
		t.Fatal("event plan hash did not change after payload mutation")
	}

	causationChanged := append([]modulecore.EventEnvelope(nil), report.Plan.Events...)
	changed := false
	for index := range causationChanged {
		if causationChanged[index].CausationEventID == "" {
			continue
		}
		causationChanged[index].CausationEventID = modulecore.EventID("evt-mutated-causation")
		changed = true
		break
	}
	if !changed {
		t.Fatal("test plan has no causation event to mutate")
	}
	causationHash, err := hashEventPlan(causationChanged)
	if err != nil {
		t.Fatalf("hashEventPlan(causation mutation) error = %v", err)
	}
	if causationHash == want {
		t.Fatal("event plan hash did not change after causation mutation")
	}
}

func TestDryRunBlockedReceiptAndNoSourceMutation(t *testing.T) {
	root := t.TempDir()
	snapshot := makeTestSnapshot(t, filepath.Join("blocked", "snapshot"))
	source := filepath.Join(snapshot, "source-dci")
	before := fileBytesHash(t, source)
	manifest, err := DryRun(context.Background(), Options{
		SnapshotDir: snapshot, SourceDCI: "source-dci", SourceDCIJSONL: "source-dci-jsonl", SourceEventStore: "source-event-store", SourceL1: "source-l1", SourceArchive: "source-archive", Manifest: "blocked.json",
		Expected: ExpectedCounts{Searches: 99, ReadEvents: 1, EvidenceEvents: 1, TotalEvents: 4}, AgentIDs: testAgentIDs,
	})
	if err == nil || manifest.Status != StatusBlocked || manifest.ErrorCode != "expected_count_mismatch" {
		t.Fatalf("blocked result = %#v, err=%v", manifest, err)
	}
	if got := fileBytesHash(t, source); got != before {
		t.Fatalf("source changed during dry-run: before=%s after=%s", before, got)
	}
	if _, err := os.Stat(filepath.Join(snapshot, "blocked.json")); err != nil {
		t.Fatalf("blocked manifest missing: %v", err)
	}
	entries, err := os.ReadDir(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == "blocked.json" || strings.HasPrefix(entry.Name(), "source-") {
			continue
		}
		t.Fatalf("unexpected dry-run output %q", entry.Name())
	}
	_ = root
}

func TestDryRunRejectsMissingParentUnexpectedToolAndPromotedReference(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(string)
		code   string
	}{
		{name: "unexpected tool", mutate: func(path string) { updateLegacyStepTool(t, path, "shell") }, code: "unexpected_tool"},
		{name: "missing parent", mutate: func(path string) { updateLegacyEvidenceParent(t, path, "missing-search") }, code: "missing_parent"},
		{name: "promoted staging", mutate: func(path string) { addPromotedReference(t, path) }, code: "promoted_staging_reference"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := makeTestSnapshot(t, tt.name)
			tt.mutate(snapshot)
			manifest, err := DryRun(context.Background(), Options{
				SnapshotDir: snapshot, SourceDCI: "source-dci", SourceDCIJSONL: "source-dci-jsonl", SourceEventStore: "source-event-store", SourceL1: "source-l1", SourceArchive: "source-archive", Manifest: "manifest.json",
				Expected: ExpectedCounts{Searches: 1, ReadEvents: 1, EvidenceEvents: 1, TotalEvents: 4}, AgentIDs: testAgentIDs,
			})
			if err == nil || manifest.Status != StatusBlocked || manifest.ErrorCode != tt.code {
				t.Fatalf("result = %#v, err=%v, want code %s", manifest, err, tt.code)
			}
		})
	}
}

func TestDryRunRejectsConflictingEvidenceMalformedJSONLOversizedSchemaAndCollision(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*testing.T, string)
		code    string
	}{
		{name: "conflicting evidence", prepare: func(t *testing.T, snapshot string) {
			updateL1EvidenceSnippet(t, filepath.Join(snapshot, "source-l1"), "different")
		}, code: "conflicting_duplicate_evidence"},
		{name: "malformed JSONL", prepare: func(t *testing.T, snapshot string) {
			if err := os.WriteFile(filepath.Join(snapshot, "source-dci-jsonl"), []byte(`{"event_id":"x","unknown":true}`+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, code: "malformed_jsonl"},
		{name: "unknown DCI schema", prepare: func(t *testing.T, snapshot string) { addUnknownDCITable(t, filepath.Join(snapshot, "source-dci")) }, code: "unknown_schema"},
		{name: "event collision", prepare: func(t *testing.T, snapshot string) {
			addCollisionEvent(t, filepath.Join(snapshot, "source-event-store"), "legacy-search-1")
		}, code: "event_collision"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := makeTestSnapshot(t, tt.name)
			tt.prepare(t, snapshot)
			manifest, err := DryRun(context.Background(), Options{
				SnapshotDir: snapshot, SourceDCI: "source-dci", SourceDCIJSONL: "source-dci-jsonl", SourceEventStore: "source-event-store", SourceL1: "source-l1", SourceArchive: "source-archive", Manifest: "manifest.json",
				Expected: ExpectedCounts{Searches: 1, ReadEvents: 1, EvidenceEvents: 1, TotalEvents: 4}, AgentIDs: testAgentIDs,
			})
			if err == nil || manifest.Status != StatusBlocked || manifest.ErrorCode != tt.code {
				t.Fatalf("result = %#v, err=%v, want code %s", manifest, err, tt.code)
			}
		})
	}
}

func TestDryRunKeepsEvidenceSourceSeparateFromL1ProjectionSource(t *testing.T) {
	snapshot := makeTestSnapshot(t, "projection-source-identity")
	const projectionSourceID = "dci:file:0123456789abcdef"

	current := openTestDB(t, filepath.Join(snapshot, "source-l1"))
	mustExec(t, current, `UPDATE l1_staging_item SET source_id=?`, projectionSourceID)
	mustExec(t, current, `UPDATE l1_source_registry SET source_id=?`, projectionSourceID)
	if err := current.Close(); err != nil {
		t.Fatal(err)
	}
	archive := openTestDB(t, filepath.Join(snapshot, "source-archive"))
	mustExec(t, archive, `UPDATE l1_staging_item_archive SET source_id=?`, projectionSourceID)
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}

	options := Options{
		SnapshotDir: snapshot, SourceDCI: "source-dci", SourceDCIJSONL: "source-dci-jsonl",
		SourceEventStore: "source-event-store", SourceL1: "source-l1", SourceArchive: "source-archive",
		Manifest: "manifest.json", Expected: ExpectedCounts{Searches: 1, ReadEvents: 1, EvidenceEvents: 1, TotalEvents: 4}, AgentIDs: testAgentIDs,
	}
	paths, err := resolvePaths(options)
	if err != nil {
		t.Fatalf("resolvePaths() error = %v", err)
	}
	report, err := classifySnapshot(context.Background(), paths, options)
	if err != nil {
		t.Fatalf("classifySnapshot() error = %v", err)
	}
	evidence, ok := report.Snapshot.Evidence["legacy-evidence-1"]
	if !ok {
		t.Fatal("classified snapshot is missing legacy evidence")
	}
	if evidence.SourceID != "source-1" {
		t.Fatalf("evidence source_id = %q, want original DCI source_id", evidence.SourceID)
	}
}

func TestLoadL1KeepsProjectionSourceOutOfEvidenceProvenance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source-l1")
	writeL1TestDB(t, path, "legacy-search-1", "legacy-evidence-1", true, false)
	const projectionSourceID = "dci:file:0123456789abcdef"
	db := openTestDB(t, path)
	mustExec(t, db, `UPDATE l1_staging_item SET source_id=?`, projectionSourceID)
	mustExec(t, db, `UPDATE l1_source_registry SET source_id=?`, projectionSourceID)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	data, _, err := loadL1Current(context.Background(), path)
	if err != nil {
		t.Fatalf("loadL1Current() error = %v", err)
	}
	if len(data.Evidence) != 1 || data.Evidence[0].SourceID != "" {
		t.Fatalf("L1 evidence provenance = %#v, want no Evidence source_id", data.Evidence)
	}
	ref, ok := data.StagingRefs["staging-1"]
	if !ok || ref.SourceID != projectionSourceID {
		t.Fatalf("L1 projection source ref = %#v, want %q", ref, projectionSourceID)
	}
}

func TestDryRunRejectsRegistrySourceUnboundFromL1Projection(t *testing.T) {
	snapshot := makeTestSnapshot(t, "registry-projection-source-mismatch")
	db := openTestDB(t, filepath.Join(snapshot, "source-l1"))
	mustExec(t, db, `UPDATE l1_source_registry SET source_id=?`, "dci:file:unbound-source")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	manifest, err := runTestSnapshotResult(t, snapshot, "manifest.json")
	if err == nil || manifest.Status != StatusBlocked || manifest.ErrorCode != "conflicting_duplicate_registry" {
		t.Fatalf("result = %#v, err=%v, want conflicting_duplicate_registry", manifest, err)
	}
}

func TestDryRunRejectsEventStoreEnvelopeColumnAndDependencyMismatches(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
		code   string
	}{
		{name: "column envelope mismatch", mutate: mutateEventEnvelopeColumnMismatch, code: "event_envelope_mismatch"},
		{name: "dependency envelope mismatch", mutate: mutateEventDependencyMismatch, code: "event_dependency_mismatch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := makeTestSnapshot(t, tt.name)
			tt.mutate(t, filepath.Join(snapshot, "source-event-store"))
			manifest, err := runTestSnapshotResult(t, snapshot, "manifest.json")
			if err == nil || manifest.Status != StatusBlocked || manifest.ErrorCode != tt.code {
				t.Fatalf("result = %#v, err=%v, want code %s", manifest, err, tt.code)
			}
		})
	}
}

func TestDryRunRejectsExistingDCIEventHistoryAndPreservesNonDCIEventCollisionBoundary(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
		code   string
	}{
		{name: "partial DCI history", mutate: addExistingDCIEvent, code: "partial_dci_history"},
		{name: "planned ID non DCI collision", mutate: func(t *testing.T, path string) { addCollisionEvent(t, path, "legacy-search-1") }, code: "event_collision"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := makeTestSnapshot(t, tt.name)
			tt.mutate(t, filepath.Join(snapshot, "source-event-store"))
			manifest, err := runTestSnapshotResult(t, snapshot, "manifest.json")
			if err == nil || manifest.Status != StatusBlocked || manifest.ErrorCode != tt.code {
				t.Fatalf("result = %#v, err=%v, want code %s", manifest, err, tt.code)
			}
		})
	}
}

func TestDryRunRejectsInconsistentLegacyL1Markers(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{name: "partial marker", mutate: func(t *testing.T, path string) {
			db := openTestDB(t, path)
			defer db.Close()
			mustExec(t, db, `UPDATE l1_staging_item SET meta_json=?`, `{"search_event_id":"legacy-search-1","evidence_id":"legacy-evidence-1"}`)
		}},
		{name: "wrong legacy event id", mutate: func(t *testing.T, path string) {
			db := openTestDB(t, path)
			defer db.Close()
			mustExec(t, db, `UPDATE l1_staging_item SET event_id=?`, "wrong-event-id")
		}},
		{name: "canonical marker mixture", mutate: func(t *testing.T, path string) {
			db := openTestDB(t, path)
			defer db.Close()
			mustExec(t, db, `UPDATE l1_staging_item SET meta_json=?`, `{"source_kind":"dci","search_event_id":"legacy-search-1","evidence_id":"legacy-evidence-1","trace_id":"trc_canonical-marker"}`)
		}},
		{name: "wrong registry kind", mutate: func(t *testing.T, path string) {
			db := openTestDB(t, path)
			defer db.Close()
			mustExec(t, db, `UPDATE l1_source_registry SET kind=?`, "news")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := makeTestSnapshot(t, tt.name)
			tt.mutate(t, filepath.Join(snapshot, "source-l1"))
			manifest, err := runTestSnapshotResult(t, snapshot, "manifest.json")
			if err == nil || manifest.Status != StatusBlocked || manifest.ErrorCode != "legacy_marker_mismatch" {
				t.Fatalf("result = %#v, err=%v, want legacy_marker_mismatch", manifest, err)
			}
		})
	}
}

func TestDryRunAcceptsNonDCIL1KindsWithoutMarkers(t *testing.T) {
	snapshot := makeTestSnapshot(t, "non-dci-l1-kinds")
	db := openTestDB(t, filepath.Join(snapshot, "source-l1"))
	mustExec(t, db, `UPDATE l1_staging_item SET namespace=?, meta_json=?`, "kb:news", `{}`)
	mustExec(t, db, `UPDATE l1_source_registry SET kind=?, meta_json=?`, "search_fallback", `{}`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	manifest, err := runTestSnapshotResult(t, snapshot, "manifest.json")
	if err != nil || manifest.Status != StatusReady {
		t.Fatalf("non-DCI L1 rows result = %#v, err=%v, want ready", manifest, err)
	}
}

func TestDryRunRejectsUnsupportedLegacyQueryTerms(t *testing.T) {
	snapshot := makeTestSnapshot(t, "query-terms")
	db := openTestDB(t, filepath.Join(snapshot, "source-dci"))
	mustExec(t, db, `INSERT INTO dci_query_terms(event_id,term,term_type,parent_term,created_at) VALUES(?,?,?,?,?)`, "legacy-search-1", "term", "keyword", "", "2026-08-31T00:00:00Z")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	manifest, err := runTestSnapshotResult(t, snapshot, "manifest.json")
	if err == nil || manifest.Status != StatusBlocked || manifest.ErrorCode != "unsupported_query_terms" {
		t.Fatalf("result = %#v, err=%v, want unsupported_query_terms", manifest, err)
	}
	if manifest.SourceCounts.DCIQueryTerms != 1 {
		t.Fatalf("query-term source count = %#v, want 1", manifest.SourceCounts)
	}
}

func TestDryRunRejectsDuplicateReadFilePathForUnprovableEvidenceAttribution(t *testing.T) {
	snapshot := makeTestSnapshot(t, "duplicate-read-path")
	db := openTestDB(t, filepath.Join(snapshot, "source-dci"))
	mustExec(t, db, `INSERT INTO dci_search_step(event_id,step_no,tool,command_text,file_path,result_count,status,error_message,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, "legacy-search-1", 2, "read_file", "read again", "spec.md", 1, "ok", "", "2026-08-31T00:00:01Z")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	manifest, err := runTestSnapshotResult(t, snapshot, "manifest.json")
	if err == nil || manifest.Status != StatusBlocked || manifest.ErrorCode != "ambiguous_evidence_attribution" {
		t.Fatalf("result = %#v, err=%v, want ambiguous_evidence_attribution", manifest, err)
	}
}

func TestDryRunRejectsNestedSourcesUnderBroadSnapshotRoot(t *testing.T) {
	broadRoot := t.TempDir()
	nested := filepath.Join(broadRoot, "offline", "cohort")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	writeLegacyDCITestDB(t, filepath.Join(nested, "source-dci"), "legacy-search-1", "legacy-evidence-1", false)
	writeJSONLTest(t, filepath.Join(nested, "source-dci-jsonl"), "legacy-search-1")
	writeEventStoreTestDB(t, filepath.Join(nested, "source-event-store"), nil)
	writeL1TestDB(t, filepath.Join(nested, "source-l1"), "legacy-search-1", "legacy-evidence-1", true, false)
	writeArchiveTestDB(t, filepath.Join(nested, "source-archive"), "legacy-search-1", "legacy-evidence-1", true)
	manifest, err := DryRun(context.Background(), Options{
		SnapshotDir: broadRoot,
		SourceDCI:   filepath.Join("offline", "cohort", "source-dci"), SourceDCIJSONL: filepath.Join("offline", "cohort", "source-dci-jsonl"),
		SourceEventStore: filepath.Join("offline", "cohort", "source-event-store"), SourceL1: filepath.Join("offline", "cohort", "source-l1"), SourceArchive: filepath.Join("offline", "cohort", "source-archive"),
		Manifest: "manifest.json", Expected: ExpectedCounts{Searches: 1, ReadEvents: 1, EvidenceEvents: 1, TotalEvents: 4}, AgentIDs: testAgentIDs,
	})
	if err == nil || manifest.Status != StatusBlocked || manifest.ErrorCode != "unsafe_path" {
		t.Fatalf("nested source layout result = %#v, err=%v, want unsafe_path", manifest, err)
	}
}

func TestDryRunRejectsUnknownL1SchemaVersion(t *testing.T) {
	snapshot := makeTestSnapshot(t, "unknown-l1-version")
	db := openTestDB(t, filepath.Join(snapshot, "source-l1"))
	mustExec(t, db, `PRAGMA user_version = 1`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	manifest, err := runTestSnapshotResult(t, snapshot, "manifest.json")
	if err == nil || manifest.Status != StatusBlocked || manifest.ErrorCode != "unknown_schema" {
		t.Fatalf("result = %#v, err=%v, want unknown_schema", manifest, err)
	}
}

func TestValidateManifestRejectsMalformedReadyAndNegativeCounts(t *testing.T) {
	ready := runTestSnapshot(t, makeTestSnapshot(t, "valid-manifest"), filepath.Join(t.TempDir(), "manifest.json"))
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{name: "ready error code", mutate: func(manifest *Manifest) { manifest.ErrorCode = "unexpected" }},
		{name: "wrong source hash set", mutate: func(manifest *Manifest) { delete(manifest.SourceDatabaseLogicalSHA256, "source_archive") }},
		{name: "invalid hash", mutate: func(manifest *Manifest) { manifest.MappingSHA256 = strings.Repeat("A", 64) }},
		{name: "negative source count", mutate: func(manifest *Manifest) { manifest.SourceCounts.DCITraces = -1 }},
		{name: "negative dedupe count", mutate: func(manifest *Manifest) { manifest.DedupeCounts.EvidenceRemoved = -1 }},
		{name: "negative normalized text count", mutate: func(manifest *Manifest) { manifest.ActualCounts.NormalizedTextValues = -1 }},
		{name: "negative invalid UTF-8 count", mutate: func(manifest *Manifest) { manifest.ActualCounts.InvalidUTF8Bytes = -1 }},
		{name: "negative actor count", mutate: func(manifest *Manifest) { manifest.ActorClassification.LegacyUnattributed = -1 }},
		{name: "nonzero zero counter", mutate: func(manifest *Manifest) { manifest.PlannedZeroCounters.OrphanZero = 1 }},
		{name: "unknown exclusion reason", mutate: func(manifest *Manifest) { manifest.ExclusionReasonCounts["other"] = 1 }},
		{name: "oversized actor label", mutate: func(manifest *Manifest) { manifest.LegacyActorLabelCounts[strings.Repeat("x", maxActorLabel+1)] = 1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := cloneManifest(ready)
			tt.mutate(&candidate)
			if err := validateManifest(candidate); err == nil {
				t.Fatalf("validateManifest(%s) = nil, want rejection", tt.name)
			}
			if err := validateManifest(ready); err != nil {
				t.Fatalf("test mutation leaked into ready manifest after %s: %v", tt.name, err)
			}
		})
	}
	hashMaps := []struct {
		name     string
		field    func(*Manifest) map[string]string
		required []string
	}{
		{name: "source database logical", field: func(manifest *Manifest) map[string]string { return manifest.SourceDatabaseLogicalSHA256 }, required: requiredDatabaseLogicalHashKeys},
		{name: "source schema", field: func(manifest *Manifest) map[string]string { return manifest.SourceSchemaSHA256 }, required: requiredSchemaHashKeys},
		{name: "source DCI classification", field: func(manifest *Manifest) map[string]string { return manifest.SourceDCIClassificationSHA256 }, required: requiredClassificationHashKeys},
		{name: "source file", field: func(manifest *Manifest) map[string]string { return manifest.SourceFileSHA256 }, required: requiredFileHashKeys},
		{name: "source non-DCI logical", field: func(manifest *Manifest) map[string]string { return manifest.SourceNonDCILogicalSHA256 }, required: requiredNonDCILogicalHashKeys},
	}
	for _, hashMap := range hashMaps {
		hashMap := hashMap
		t.Run(hashMap.name, func(t *testing.T) {
			t.Run("missing", func(t *testing.T) {
				candidate := cloneManifest(ready)
				delete(hashMap.field(&candidate), hashMap.required[0])
				if err := validateManifest(candidate); err == nil {
					t.Fatalf("validateManifest(%s missing) = nil, want rejection", hashMap.name)
				}
				if err := validateManifest(ready); err != nil {
					t.Fatalf("test mutation leaked into ready manifest after %s missing: %v", hashMap.name, err)
				}
			})
			t.Run("unknown", func(t *testing.T) {
				candidate := cloneManifest(ready)
				values := hashMap.field(&candidate)
				delete(values, hashMap.required[0])
				values["unknown"] = strings.Repeat("0", 64)
				if err := validateManifest(candidate); err == nil {
					t.Fatalf("validateManifest(%s unknown) = nil, want rejection", hashMap.name)
				}
				if err := validateManifest(ready); err != nil {
					t.Fatalf("test mutation leaked into ready manifest after %s unknown: %v", hashMap.name, err)
				}
			})
			t.Run("invalid", func(t *testing.T) {
				candidate := cloneManifest(ready)
				hashMap.field(&candidate)[hashMap.required[0]] = strings.Repeat("A", 64)
				if err := validateManifest(candidate); err == nil {
					t.Fatalf("validateManifest(%s invalid) = nil, want rejection", hashMap.name)
				}
				if err := validateManifest(ready); err != nil {
					t.Fatalf("test mutation leaked into ready manifest after %s invalid: %v", hashMap.name, err)
				}
			})
		})
	}
	for _, tt := range []struct {
		name   string
		mutate func(*Manifest)
	}{
		{name: "bad logical hash algorithm", mutate: func(manifest *Manifest) { manifest.LogicalHashAlgorithm = "sha256" }},
		{name: "missing text normalization algorithm", mutate: func(manifest *Manifest) { manifest.TextNormalizationAlgorithm = "" }},
		{name: "bad text normalization algorithm", mutate: func(manifest *Manifest) { manifest.TextNormalizationAlgorithm = "sha256" }},
		{name: "v1 schema", mutate: func(manifest *Manifest) { manifest.SchemaVersion = "rencrow.identity.dci-migration/v1" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			candidate := cloneManifest(ready)
			tt.mutate(&candidate)
			if err := validateManifest(candidate); err == nil {
				t.Fatalf("validateManifest(%s) = nil, want rejection", tt.name)
			}
			if err := validateManifest(ready); err != nil {
				t.Fatalf("test mutation leaked into ready manifest after %s: %v", tt.name, err)
			}
		})
	}
	blocked := newBaseManifest(ExpectedCounts{})
	blocked.Status = StatusBlocked
	blocked.ErrorCode = "unknown_schema"
	blocked.SourceDatabaseLogicalSHA256["source_dci"] = strings.Repeat("0", 64)
	if err := validateManifest(blocked); err != nil {
		t.Fatalf("blocked partial receipt rejected: %v", err)
	}
}

func TestValidateManifestRequiresEventPlanSHA256ForReady(t *testing.T) {
	ready := runTestSnapshot(t, makeTestSnapshot(t, "event-plan-manifest-validation"), filepath.Join(t.TempDir(), "manifest.json"))
	missing := cloneManifest(ready)
	missing.EventPlanSHA256 = ""
	if err := validateManifest(missing); err == nil {
		t.Fatal("ready manifest with missing event_plan_sha256 was accepted")
	}
	invalid := cloneManifest(ready)
	invalid.EventPlanSHA256 = strings.Repeat("A", 64)
	if err := validateManifest(invalid); err == nil {
		t.Fatal("ready manifest with invalid event_plan_sha256 was accepted")
	}

	blocked := newBaseManifest(ExpectedCounts{})
	blocked.Status = StatusBlocked
	blocked.ErrorCode = "unknown_schema"
	if blocked.EventPlanSHA256 != "" {
		t.Fatalf("new blocked manifest event plan hash = %q, want empty", blocked.EventPlanSHA256)
	}
	if err := validateManifest(blocked); err != nil {
		t.Fatalf("blocked manifest may omit event_plan_sha256: %v", err)
	}
	blocked.TextNormalizationAlgorithm = ""
	if err := validateManifest(blocked); err == nil {
		t.Fatal("blocked manifest with missing text normalization algorithm was accepted")
	}
	blocked.TextNormalizationAlgorithm = "sha256"
	if err := validateManifest(blocked); err == nil {
		t.Fatal("blocked manifest with wrong text normalization algorithm was accepted")
	}
}

func TestPersistReadyManifestBlocksWhenOutputAlreadyExists(t *testing.T) {
	ready := runTestSnapshot(t, makeTestSnapshot(t, "ready-persist"), filepath.Join(t.TempDir(), "manifest.json"))
	path := filepath.Join(t.TempDir(), "occupied.json")
	before := []byte("existing output")
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := persistReadyManifest(path, ready)
	if err == nil {
		t.Fatal("persistReadyManifest() error = nil, want manifest_write failure")
	}
	if got.Status != StatusBlocked || got.ErrorCode != "manifest_write" {
		t.Fatalf("persistReadyManifest() receipt = %#v, want blocked/manifest_write", got)
	}
	if err := validateManifest(got); err != nil {
		t.Fatalf("blocked persistence-failure receipt is invalid: %v", err)
	}
	if after, err := os.ReadFile(path); err != nil {
		t.Fatal(err)
	} else if string(after) != string(before) {
		t.Fatalf("existing output was modified: before=%q after=%q", before, after)
	}
}

func TestDerivedDedupeCountsRejectNegativeValues(t *testing.T) {
	snapshot := sourceSnapshot{
		Counts:   SourceCounts{},
		Searches: map[string]legacySearch{"search": {ID: "search"}},
	}
	if err := validateDerivedDedupeCounts(snapshot, ActualCounts{}); err == nil {
		t.Fatal("validateDerivedDedupeCounts() = nil, want negative count rejection")
	}
}

func TestDryRunRejectsJSONLSidecarAndNonDryRunAPIHasNoAlternateMode(t *testing.T) {
	snapshot := makeTestSnapshot(t, "sidecar")
	if err := os.WriteFile(filepath.Join(snapshot, "source-dci-wal"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := DryRun(context.Background(), Options{
		SnapshotDir: snapshot, SourceDCI: "source-dci", SourceDCIJSONL: "source-dci-jsonl", SourceEventStore: "source-event-store", SourceL1: "source-l1", SourceArchive: "source-archive", Manifest: "manifest.json",
		Expected: ExpectedCounts{Searches: 1, ReadEvents: 1, EvidenceEvents: 1, TotalEvents: 4}, AgentIDs: testAgentIDs,
	})
	if err == nil || manifest.Status != StatusBlocked {
		t.Fatalf("sidecar result = %#v, err=%v", manifest, err)
	}
}

func TestClassifySnapshotRetainsOneDeterministicPlanAndTypedIdentityMaps(t *testing.T) {
	snapshot := makeTestSnapshot(t, "retained-plan")
	options := Options{
		SnapshotDir: snapshot, SourceDCI: "source-dci", SourceDCIJSONL: "source-dci-jsonl",
		SourceEventStore: "source-event-store", SourceL1: "source-l1", SourceArchive: "source-archive",
		Manifest: "manifest.json", Expected: ExpectedCounts{Searches: 1, ReadEvents: 1, EvidenceEvents: 1, TotalEvents: 4}, AgentIDs: testAgentIDs,
	}
	paths, err := resolvePaths(options)
	if err != nil {
		t.Fatalf("resolvePaths: %v", err)
	}
	report, err := classifySnapshot(context.Background(), paths, options)
	if err != nil {
		t.Fatalf("classifySnapshot: %v", err)
	}
	if len(report.Snapshot.Searches) != 1 || len(report.Snapshot.Evidence) != 1 {
		t.Fatalf("retained source snapshot = searches=%d evidence=%d", len(report.Snapshot.Searches), len(report.Snapshot.Evidence))
	}
	plan := report.Plan
	if len(plan.searches) != len(report.Snapshot.Searches) || len(plan.evidence) != len(report.Snapshot.Evidence) {
		t.Fatalf("plan identity maps = searches=%d evidence=%d, want %d/%d", len(plan.searches), len(plan.evidence), len(report.Snapshot.Searches), len(report.Snapshot.Evidence))
	}
	readCount := 0
	for legacySearchID, search := range report.Snapshot.Searches {
		ids, ok := plan.searches[legacySearchID]
		if !ok || ids.actionID == "" || ids.traceID == "" || ids.startedEventID == "" || ids.terminalEventID == "" {
			t.Fatalf("missing search identity map for %q: %#v", legacySearchID, ids)
		}
		if ids.actorAttribution == "" || (ids.actorAttribution == domaindci.ActorAttributionAuthenticated && (ids.actorKind == "" || ids.actorID == "")) || (ids.actorAttribution == domaindci.ActorAttributionLegacyUnattributed && (ids.actorKind != "" || ids.actorID != "")) {
			t.Fatalf("invalid retained actor classification for %q: %#v", legacySearchID, ids)
		}
		for stepNo, step := range search.Steps {
			if step.Tool != "read_file" {
				continue
			}
			readCount++
			ids, ok := plan.readEvents[readEventKey{searchID: legacySearchID, stepNo: stepNo}]
			if !ok || ids == "" {
				t.Fatalf("missing read identity map for %q/%d", legacySearchID, stepNo)
			}
		}
	}
	if len(plan.readEvents) != readCount {
		t.Fatalf("read identity maps = %d, want %d", len(plan.readEvents), readCount)
	}
	for legacyEvidenceID := range report.Snapshot.Evidence {
		ids, ok := plan.evidence[legacyEvidenceID]
		if !ok || ids.evidenceID == "" || ids.createdEventID == "" {
			t.Fatalf("missing evidence identity map for %q: %#v", legacyEvidenceID, ids)
		}
	}
	if report.Manifest.ActualCounts != plan.actual || report.Manifest.EventPlanSHA256 != plan.eventPlanSHA256 || report.Manifest.MappingSHA256 != hashCanonicalLines(plan.mappingLines) {
		t.Fatalf("manifest is not the retained plan projection: manifest=%#v plan=%#v", report.Manifest, plan)
	}
	plannedManifest := manifestFromSnapshot(report.Snapshot, options.Expected, plan.actual, plan.mappingLines, plan.Events, plan.eventPlanSHA256, options.AgentIDs)
	if !reflect.DeepEqual(report.Manifest, plannedManifest) {
		t.Fatalf("retained plan manifest differs from dry-run manifest:\nreport=%#v\nplanned=%#v", report.Manifest, plannedManifest)
	}
	repeated, err := planMigration(context.Background(), report.Snapshot, options.AgentIDs)
	if err != nil {
		t.Fatalf("repeat planMigration: %v", err)
	}
	if !reflect.DeepEqual(plan, repeated) {
		t.Fatalf("migration planning is not deterministic:\nfirst=%#v\nsecond=%#v", plan, repeated)
	}
}

func TestDryRunNormalizesInvalidUTF8EvidenceBeforePlanningWithoutMutatingSources(t *testing.T) {
	snapshot := makeTestSnapshot(t, "invalid-utf8-evidence")
	invalidSnippet := "production evidence " + string([]byte{0xc3, 0xc3})
	normalizedSnippet := "production evidence \uFFFD\uFFFD"
	updateEvidenceSnippetAcrossSources(t, snapshot, invalidSnippet)
	sourceFiles := []string{
		filepath.Join(snapshot, "source-dci"),
		filepath.Join(snapshot, "source-dci-jsonl"),
		filepath.Join(snapshot, "source-l1"),
		filepath.Join(snapshot, "source-archive"),
	}
	beforeFileHashes := make(map[string]string, len(sourceFiles))
	for _, path := range sourceFiles {
		beforeFileHashes[path] = fileBytesHash(t, path)
	}
	beforeCurrentText, beforeCurrentHash := readL1RawFields(t, filepath.Join(snapshot, "source-l1"), "l1_staging_item")
	beforeArchiveText, beforeArchiveHash := readL1RawFields(t, filepath.Join(snapshot, "source-archive"), "l1_staging_item_archive")
	options := Options{
		SnapshotDir: snapshot, SourceDCI: "source-dci", SourceDCIJSONL: "source-dci-jsonl",
		SourceEventStore: "source-event-store", SourceL1: "source-l1", SourceArchive: "source-archive",
		Manifest: "normalized.json", Expected: ExpectedCounts{
			Searches: 1, ReadEvents: 1, EvidenceEvents: 1, TotalEvents: 4,
			NormalizedTextValues: 1, InvalidUTF8Bytes: 2,
		}, AgentIDs: testAgentIDs,
	}
	paths, err := resolvePaths(options)
	if err != nil {
		t.Fatalf("resolvePaths: %v", err)
	}
	report, err := classifySnapshot(context.Background(), paths, options)
	if err != nil {
		t.Fatalf("classifySnapshot: %v", err)
	}
	canonicalEvidence, ok := report.Snapshot.Evidence["legacy-evidence-1"]
	if !ok || canonicalEvidence.Snippet != normalizedSnippet {
		t.Fatalf("canonical evidence snippet = %q/%v, want %q", canonicalEvidence.Snippet, ok, normalizedSnippet)
	}
	if report.Plan.actual.NormalizedTextValues != 1 || report.Plan.actual.InvalidUTF8Bytes != 2 {
		t.Fatalf("plan normalization counts = %#v, want values=1 bytes=2", report.Plan.actual)
	}
	var plannedSnippet string
	for _, event := range report.Plan.Events {
		if event.EventType != "dci.evidence.created" {
			continue
		}
		var found bool
		plannedSnippet, found = event.Payload["snippet"].(string)
		if !found {
			t.Fatalf("planned evidence event snippet has unexpected type: %#v", event.Payload["snippet"])
		}
		break
	}
	if plannedSnippet != normalizedSnippet {
		t.Fatalf("planned evidence event snippet = %q, want %q", plannedSnippet, normalizedSnippet)
	}
	if report.Manifest.TextNormalizationAlgorithm != TextNormalizationAlgorithm || report.Manifest.ExpectedCounts != options.Expected || report.Manifest.ActualCounts != report.Plan.actual {
		t.Fatalf("normalization manifest binding = %#v", report.Manifest)
	}

	manifest, err := DryRun(context.Background(), options)
	if err != nil || manifest.Status != StatusReady {
		t.Fatalf("normalized DryRun = %#v, err=%v", manifest, err)
	}
	if manifest.TextNormalizationAlgorithm != TextNormalizationAlgorithm || manifest.ActualCounts.NormalizedTextValues != 1 || manifest.ActualCounts.InvalidUTF8Bytes != 2 {
		t.Fatalf("normalized ready manifest = %#v", manifest)
	}
	receipt, err := os.ReadFile(filepath.Join(snapshot, "normalized.json"))
	if err != nil {
		t.Fatalf("read normalized manifest: %v", err)
	}
	if strings.Contains(string(receipt), invalidSnippet) || strings.Contains(string(receipt), "production evidence") {
		t.Fatalf("normalized manifest leaked source evidence text: %q", receipt)
	}

	mismatchOptions := options
	mismatchOptions.Manifest = "normalized-mismatch.json"
	mismatchOptions.Expected.NormalizedTextValues = 0
	mismatchOptions.Expected.InvalidUTF8Bytes = 0
	mismatchManifest, mismatchErr := DryRun(context.Background(), mismatchOptions)
	if mismatchErr == nil || mismatchManifest.Status != StatusBlocked || mismatchManifest.ErrorCode != "expected_count_mismatch" {
		t.Fatalf("normalization mismatch = %#v, err=%v", mismatchManifest, mismatchErr)
	}
	if mismatchManifest.TextNormalizationAlgorithm != TextNormalizationAlgorithm || mismatchManifest.ActualCounts.NormalizedTextValues != 1 || mismatchManifest.ActualCounts.InvalidUTF8Bytes != 2 || mismatchManifest.ExpectedCounts != mismatchOptions.Expected {
		t.Fatalf("normalization mismatch manifest = %#v", mismatchManifest)
	}
	mismatchReceipt, err := os.ReadFile(filepath.Join(snapshot, "normalized-mismatch.json"))
	if err != nil {
		t.Fatalf("read normalization mismatch manifest: %v", err)
	}
	if strings.Contains(string(mismatchReceipt), invalidSnippet) || strings.Contains(string(mismatchReceipt), "production evidence") {
		t.Fatalf("blocked normalization manifest leaked source evidence text: %q", mismatchReceipt)
	}

	for _, path := range sourceFiles {
		if got := fileBytesHash(t, path); got != beforeFileHashes[path] {
			t.Fatalf("source file %s changed: before=%s after=%s", filepath.Base(path), beforeFileHashes[path], got)
		}
	}
	afterCurrentText, afterCurrentHash := readL1RawFields(t, filepath.Join(snapshot, "source-l1"), "l1_staging_item")
	afterArchiveText, afterArchiveHash := readL1RawFields(t, filepath.Join(snapshot, "source-archive"), "l1_staging_item_archive")
	if afterCurrentText != beforeCurrentText || afterCurrentHash != beforeCurrentHash || afterArchiveText != beforeArchiveText || afterArchiveHash != beforeArchiveHash {
		t.Fatalf("raw L1/archive fields changed: current=%q/%q -> %q/%q archive=%q/%q -> %q/%q", beforeCurrentText, beforeCurrentHash, afterCurrentText, afterCurrentHash, beforeArchiveText, beforeArchiveHash, afterArchiveText, afterArchiveHash)
	}
}

func TestBuildEventPlanCoversNoEvidenceLimitAndLegacyActor(t *testing.T) {
	started := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	search := legacySearch{
		ID: "legacy-no-evidence", StartedAt: started, EndedAt: started.Add(2 * time.Second),
		Actor: "Worker", Mode: "dci", Query: "bounded query", CorpusScope: []string{},
		Status: "completed", Steps: map[int]legacyStep{
			1: {SearchID: "legacy-no-evidence", StepNo: 1, Tool: "read_file", FilePath: "unreadable.md", ResultCount: 0, Status: "error", ErrorMessage: "permission denied", CreatedAt: started.Add(time.Second)},
			2: {SearchID: "legacy-no-evidence", StepNo: 2, Tool: "limit", CommandText: "candidate limit", Status: "stopped", CreatedAt: started.Add(time.Second)},
		},
	}
	snapshot := sourceSnapshot{Searches: map[string]legacySearch{search.ID: search}, Evidence: map[string]legacyEvidence{}}
	actual, events, _, _, err := buildEventPlan(context.Background(), snapshot, testAgentIDs)
	if err != nil {
		t.Fatalf("buildEventPlan() error = %v", err)
	}
	if actual != (ActualCounts{Searches: 1, ReadEvents: 1, EvidenceEvents: 0, TotalEvents: 3, LegacyLimitSteps: 1}) {
		t.Fatalf("actual counts = %#v", actual)
	}
	if err := modulecore.ValidateEventEnvelopeGraph(events); err != nil {
		t.Fatalf("planned event graph invalid: %v", err)
	}
	for _, event := range events {
		if event.ActionID == "" || event.ActorKind != "" || event.ActorID != "" {
			t.Fatalf("legacy actor was attributed: %#v", event)
		}
		if event.EventType == "dci.search.requested" || event.EventType == "dci.source.selected" {
			t.Fatalf("synthetic event was planned: %#v", event)
		}
	}
	terminal := events[len(events)-1]
	if terminal.EventType != "dci.search.completed" || terminal.Payload["legacy_limit_steps"] != 1 {
		t.Fatalf("terminal limit projection = %#v", terminal)
	}
	var lastRead modulecore.EventID
	for _, event := range events {
		if event.EventType == "dci.file.read" {
			lastRead = event.EventID
		}
	}
	if terminal.CausationEventID != lastRead || len(terminal.DependencyEventIDs) != 0 {
		t.Fatalf("zero-evidence terminal join = cause %q deps %#v, want last read %q and no deps", terminal.CausationEventID, terminal.DependencyEventIDs, lastRead)
	}
	limitations, ok := terminal.Payload["limitations"].([]string)
	if !ok || len(limitations) != 1 || limitations[0] != "legacy_limit_projection" {
		t.Fatalf("terminal limitations = %#v", terminal.Payload["limitations"])
	}
}

func TestBuildEventPlanUsesCanonicalPayloadWithoutRawLegacyIDs(t *testing.T) {
	started := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	legacySearchID := "legacy-search-payload"
	legacyEvidenceID := "legacy-evidence-payload"
	search := legacySearch{
		ID: legacySearchID, StartedAt: started, EndedAt: started.Add(3 * time.Second),
		Actor: "Worker", Mode: "dci", Query: "payload migration", CorpusScope: []string{},
		Status: "completed", FinalEvidenceCount: 1, Steps: map[int]legacyStep{
			1: {SearchID: legacySearchID, StepNo: 1, Tool: "read_file", FilePath: "spec.md", ResultCount: 1, Status: "ok", CreatedAt: started.Add(time.Second)},
			2: {SearchID: legacySearchID, StepNo: 2, Tool: "limit", CommandText: "bounded", Status: "stopped", CreatedAt: started.Add(2 * time.Second)},
		},
	}
	evidence := legacyEvidence{ID: legacyEvidenceID, SearchID: legacySearchID, SourceID: "source-1", FilePath: "spec.md", LineStart: 1, LineEnd: 1, Snippet: "evidence", Reason: "matched", Confidence: 0.8, CreatedAt: started.Add(time.Second)}
	snapshot := sourceSnapshot{
		Searches: map[string]legacySearch{legacySearchID: search},
		Evidence: map[string]legacyEvidence{legacyEvidenceID: evidence},
	}
	actual, events, mappingLines, eventPlanSHA256, err := buildEventPlan(context.Background(), snapshot, testAgentIDs)
	if err != nil {
		t.Fatalf("buildEventPlan() error = %v", err)
	}
	if actual != (ActualCounts{Searches: 1, ReadEvents: 1, EvidenceEvents: 1, TotalEvents: 4, LegacyLimitSteps: 1}) {
		t.Fatalf("actual counts = %#v", actual)
	}
	if err := validatePlannedPayloads(events, snapshot); err != nil {
		t.Fatalf("planned payload validation = %v", err)
	}
	manifest := manifestFromSnapshot(snapshot, ExpectedCounts{}, actual, mappingLines, events, eventPlanSHA256, testAgentIDs)
	if manifest.PlannedZeroCounters.LegacyKeyZero != 0 {
		t.Fatalf("planned legacy key counter = %#v, want zero", manifest.PlannedZeroCounters)
	}
	var read, evidenceEvent, terminal modulecore.EventEnvelope
	for _, event := range events {
		payload, err := json.Marshal(event.Payload)
		if err != nil {
			t.Fatalf("marshal %s payload: %v", event.EventType, err)
		}
		encoded := string(payload)
		for _, forbidden := range []string{"legacy_search_id", "legacy_evidence_id", "legacy_step_no", "legacy_final_evidence_count", "search_event_id"} {
			if strings.Contains(encoded, `"`+forbidden+`"`) {
				t.Fatalf("event %s contains forbidden payload key %q: %s", event.EventType, forbidden, encoded)
			}
		}
		if strings.Contains(encoded, legacySearchID) || strings.Contains(encoded, legacyEvidenceID) {
			t.Fatalf("event %s contains a raw legacy ID: %s", event.EventType, encoded)
		}
		if event.Payload["legacy_actor_label"] != "Worker" {
			t.Fatalf("event %s lost allowed actor label: %#v", event.EventType, event.Payload)
		}
		switch event.EventType {
		case "dci.file.read":
			read = event
		case "dci.evidence.created":
			evidenceEvent = event
		case "dci.search.completed":
			terminal = event
		}
	}
	if read.Payload["step_no"] != 1 {
		t.Fatalf("canonical step_no = %#v, payload = %#v", read.Payload["step_no"], read.Payload)
	}
	if _, exists := read.Payload["legacy_step_no"]; exists {
		t.Fatalf("legacy step_no remains in read payload: %#v", read.Payload)
	}
	if evidenceEvent.Payload["legacy_actor_label"] != "Worker" {
		t.Fatalf("evidence event actor label = %#v", evidenceEvent.Payload["legacy_actor_label"])
	}
	if terminal.Payload["evidence_count"] != 1 || terminal.Payload["legacy_limit_steps"] != 1 {
		t.Fatalf("terminal canonical counters = %#v", terminal.Payload)
	}
	if terminal.CausationEventID != evidenceEvent.EventID || len(terminal.DependencyEventIDs) != 0 {
		t.Fatalf("one-evidence terminal join = cause %q deps %#v, want evidence %q and no deps", terminal.CausationEventID, terminal.DependencyEventIDs, evidenceEvent.EventID)
	}
	limitations, ok := terminal.Payload["limitations"].([]string)
	if !ok || len(limitations) != 1 || limitations[0] != "legacy_limit_projection" {
		t.Fatalf("terminal limitations = %#v", terminal.Payload["limitations"])
	}
}

func TestValidatePlannedPayloadsRejectsInjectedForbiddenKeyAndRawLegacyID(t *testing.T) {
	legacySearchID := "legacy-search-injected"
	legacyEvidenceID := "legacy-evidence-injected"
	event := canonicalTestEvent(t, modulecore.NewEventID(), "dci.search.started", modulecore.NewTraceID())
	event.Payload = map[string]any{
		"nested": []map[string]any{
			map[string]any{"legacy_step_no": 1},
			map[string]any{"safe_reference": legacyEvidenceID},
		},
	}
	snapshot := sourceSnapshot{
		Searches: map[string]legacySearch{legacySearchID: {ID: legacySearchID}},
		Evidence: map[string]legacyEvidence{legacyEvidenceID: {ID: legacyEvidenceID, SearchID: legacySearchID}},
	}
	audit := auditPlannedPayload(eventsForTest(event), snapshot)
	if audit.LegacyKeyCount != 1 || audit.RawLegacyIDCount != 1 {
		t.Fatalf("planned payload audit = %#v, want one key and one raw ID", audit)
	}
	manifest := manifestFromSnapshot(snapshot, ExpectedCounts{}, ActualCounts{}, nil, eventsForTest(event), "", nil)
	if manifest.PlannedZeroCounters.LegacyKeyZero != audit.LegacyKeyCount+audit.RawLegacyIDCount {
		t.Fatalf("planned legacy key counter = %#v, want measured violations", manifest.PlannedZeroCounters)
	}
	if err := validatePlannedPayloads(eventsForTest(event), snapshot); err == nil {
		t.Fatal("validatePlannedPayloads() = nil, want fail-closed rejection")
	}
}

func TestBuildEventPlanTerminalJoinsAllEvidenceBranches(t *testing.T) {
	started := time.Date(2026, 8, 31, 2, 0, 0, 0, time.UTC)
	searchID := "legacy-join-search"
	search := legacySearch{
		ID: searchID, StartedAt: started, EndedAt: started.Add(5 * time.Second),
		Actor: "Worker", Mode: "dci", Query: "evidence join", CorpusScope: []string{},
		Status: "completed", Steps: map[int]legacyStep{
			1: {SearchID: searchID, StepNo: 1, Tool: "read_file", FilePath: "first.md", ResultCount: 2, Status: "ok", CreatedAt: started.Add(time.Second)},
			2: {SearchID: searchID, StepNo: 2, Tool: "read_file", FilePath: "second.md", ResultCount: 1, Status: "ok", CreatedAt: started.Add(2 * time.Second)},
		},
	}
	evidence := map[string]legacyEvidence{
		"legacy-evidence-a": {ID: "legacy-evidence-a", SearchID: searchID, SourceID: "source-a", FilePath: "first.md", LineStart: 1, LineEnd: 1, Snippet: "a", Reason: "matched", Confidence: 0.8, CreatedAt: started.Add(3 * time.Second)},
		"legacy-evidence-b": {ID: "legacy-evidence-b", SearchID: searchID, SourceID: "source-b", FilePath: "second.md", LineStart: 2, LineEnd: 2, Snippet: "b", Reason: "matched", Confidence: 0.7, CreatedAt: started.Add(3 * time.Second)},
		"legacy-evidence-c": {ID: "legacy-evidence-c", SearchID: searchID, SourceID: "source-c", FilePath: "first.md", LineStart: 3, LineEnd: 3, Snippet: "c", Reason: "matched", Confidence: 0.6, CreatedAt: started.Add(3 * time.Second)},
	}
	snapshot := sourceSnapshot{Searches: map[string]legacySearch{searchID: search}, Evidence: evidence}
	actual, events, _, _, err := buildEventPlan(context.Background(), snapshot, testAgentIDs)
	if err != nil {
		t.Fatalf("buildEventPlan() error = %v", err)
	}
	if actual.EvidenceEvents != 3 || actual.ReadEvents != 2 {
		t.Fatalf("actual evidence/read counts = %#v, want 3/2", actual)
	}
	if err := modulecore.ValidateEventEnvelopeGraph(events); err != nil {
		t.Fatalf("joined planned event graph invalid: %v", err)
	}

	evidenceEventIDs := make([]modulecore.EventID, 0, 3)
	var terminal modulecore.EventEnvelope
	for _, event := range events {
		switch event.EventType {
		case "dci.evidence.created":
			evidenceEventIDs = append(evidenceEventIDs, event.EventID)
		case "dci.search.completed":
			terminal = event
		}
	}
	if len(evidenceEventIDs) != 3 || terminal.EventID == "" {
		t.Fatalf("planned evidence/terminal events = %#v, terminal=%#v", evidenceEventIDs, terminal)
	}
	if terminal.CausationEventID != evidenceEventIDs[len(evidenceEventIDs)-1] {
		t.Fatalf("terminal cause = %q, want last deterministic evidence %q", terminal.CausationEventID, evidenceEventIDs[len(evidenceEventIDs)-1])
	}
	wantDependencies := append([]modulecore.EventID(nil), evidenceEventIDs[:len(evidenceEventIDs)-1]...)
	sort.Slice(wantDependencies, func(left, right int) bool { return string(wantDependencies[left]) < string(wantDependencies[right]) })
	if !reflect.DeepEqual(terminal.DependencyEventIDs, wantDependencies) {
		t.Fatalf("terminal dependencies = %#v, want sorted earlier evidence IDs %#v", terminal.DependencyEventIDs, wantDependencies)
	}
	covered := make(map[modulecore.EventID]int, len(evidenceEventIDs))
	covered[terminal.CausationEventID]++
	for _, dependencyID := range terminal.DependencyEventIDs {
		if dependencyID == terminal.CausationEventID {
			t.Fatalf("terminal dependency duplicates cause %q", dependencyID)
		}
		covered[dependencyID]++
	}
	for _, evidenceEventID := range evidenceEventIDs {
		if covered[evidenceEventID] != 1 {
			t.Fatalf("evidence event %q covered %d times by terminal join", evidenceEventID, covered[evidenceEventID])
		}
	}

	failedSearch := search
	failedSearch.Status = "failed"
	failedSearch.ErrorMessage = "historical failure"
	failedSnapshot := sourceSnapshot{Searches: map[string]legacySearch{searchID: failedSearch}, Evidence: evidence}
	_, failedEvents, _, _, err := buildEventPlan(context.Background(), failedSnapshot, testAgentIDs)
	if err != nil {
		t.Fatalf("failed buildEventPlan() error = %v", err)
	}
	var failedTerminal modulecore.EventEnvelope
	for _, event := range failedEvents {
		if event.EventType == "dci.search.failed" {
			failedTerminal = event
			break
		}
	}
	if failedTerminal.EventID == "" || failedTerminal.CausationEventID != evidenceEventIDs[len(evidenceEventIDs)-1] || !reflect.DeepEqual(failedTerminal.DependencyEventIDs, wantDependencies) {
		t.Fatalf("failed terminal join = %#v, want cause %q and dependencies %#v", failedTerminal, evidenceEventIDs[len(evidenceEventIDs)-1], wantDependencies)
	}
}

func eventsForTest(event modulecore.EventEnvelope) []modulecore.EventEnvelope {
	return []modulecore.EventEnvelope{event}
}

func TestManifestActorClassificationUsesCanonicalAgentIDsWithoutPayloadLookup(t *testing.T) {
	started := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	search := legacySearch{
		ID: "legacy-agent-classification", StartedAt: started, EndedAt: started.Add(time.Second),
		Actor: "shiro", Mode: "dci", Query: "classification", CorpusScope: []string{},
		Status: "completed", Steps: map[int]legacyStep{},
	}
	snapshot := sourceSnapshot{Searches: map[string]legacySearch{search.ID: search}, Evidence: map[string]legacyEvidence{}}
	actual, events, mappingLines, eventPlanSHA256, err := buildEventPlan(context.Background(), snapshot, testAgentIDs)
	if err != nil {
		t.Fatalf("buildEventPlan() error = %v", err)
	}
	withShiro := manifestFromSnapshot(snapshot, ExpectedCounts{}, actual, mappingLines, events, eventPlanSHA256, testAgentIDs)
	withoutShiro := manifestFromSnapshot(snapshot, ExpectedCounts{}, actual, mappingLines, events, eventPlanSHA256, []string{"mio"})
	if withShiro.ActorClassification.AuthenticatedAgent != 1 || withShiro.ActorClassification.LegacyUnattributed != 0 {
		t.Fatalf("canonical agent classification = %#v", withShiro.ActorClassification)
	}
	if withoutShiro.ActorClassification.AuthenticatedAgent != 0 || withoutShiro.ActorClassification.LegacyUnattributed != 1 {
		t.Fatalf("non-canonical agent classification = %#v", withoutShiro.ActorClassification)
	}
	if withShiro.MappingSHA256 != withoutShiro.MappingSHA256 || withShiro.EventSetSHA256 != withoutShiro.EventSetSHA256 {
		t.Fatalf("agent classification changed deterministic ID hashes: with=%#v without=%#v", withShiro, withoutShiro)
	}
}

func TestBuildEventPlanSyntheticProductionCounts(t *testing.T) {
	started := time.Date(2026, 8, 31, 1, 0, 0, 0, time.UTC)
	searches := make(map[string]legacySearch, 10)
	evidence := make(map[string]legacyEvidence, 26)
	evidenceIndex := 0
	for index := 0; index < 10; index++ {
		searchID := fmt.Sprintf("synthetic-search-%02d", index)
		readSteps := 1
		if index < 2 {
			readSteps = 2
		}
		search := legacySearch{
			ID: searchID, StartedAt: started.Add(time.Duration(index) * time.Minute),
			EndedAt: started.Add(time.Duration(index)*time.Minute + 3*time.Second), Actor: "shiro",
			Mode: "dci", Query: fmt.Sprintf("synthetic query %02d", index), CorpusScope: []string{},
			Status: "completed", Steps: make(map[int]legacyStep),
		}
		for stepNo := 1; stepNo <= readSteps; stepNo++ {
			search.Steps[stepNo] = legacyStep{SearchID: searchID, StepNo: stepNo, Tool: "read_file", FilePath: fmt.Sprintf("doc-%02d-%d.md", index, stepNo), ResultCount: 1, Status: "ok", CreatedAt: search.StartedAt.Add(time.Duration(stepNo) * time.Second)}
		}
		if index == 0 {
			search.Steps[3] = legacyStep{SearchID: searchID, StepNo: 3, Tool: "limit", CommandText: "bounded", Status: "stopped", CreatedAt: search.StartedAt.Add(2 * time.Second)}
		}
		evidenceForSearch := 2
		if index < 6 {
			evidenceForSearch = 3
		}
		search.FinalEvidenceCount = evidenceForSearch
		for item := 0; item < evidenceForSearch; item++ {
			evidenceID := fmt.Sprintf("synthetic-evidence-%02d", evidenceIndex)
			evidenceIndex++
			evidence[evidenceID] = legacyEvidence{ID: evidenceID, SearchID: searchID, SourceID: "synthetic-source", FilePath: fmt.Sprintf("doc-%02d-1.md", index), LineStart: item + 1, LineEnd: item + 1, Snippet: "synthetic evidence", Confidence: 0.5, CreatedAt: search.StartedAt.Add(time.Second)}
		}
		searches[searchID] = search
	}
	actual, events, _, _, err := buildEventPlan(context.Background(), sourceSnapshot{Searches: searches, Evidence: evidence}, testAgentIDs)
	if err != nil {
		t.Fatalf("buildEventPlan() error = %v", err)
	}
	if actual != (ActualCounts{Searches: 10, ReadEvents: 12, EvidenceEvents: 26, TotalEvents: 58, LegacyLimitSteps: 1}) {
		t.Fatalf("synthetic production counts = %#v", actual)
	}
	if err := modulecore.ValidateEventEnvelopeGraph(events); err != nil {
		t.Fatalf("synthetic production graph invalid: %v", err)
	}
}

func TestDryRunRejectsStrictAndOversizedJSONL(t *testing.T) {
	tests := []struct {
		name string
		data string
		code string
	}{
		{name: "duplicate key", data: `{"event_id":"duplicate","event_id":"duplicate"}` + "\n", code: "malformed_jsonl"},
		{name: "trailing value", data: `{"event_id":"trailing"} {}`, code: "malformed_jsonl"},
		{name: "oversized line", data: strings.Repeat("x", maxJSONLLine+1) + "\n", code: "oversized_jsonl"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := makeTestSnapshot(t, tt.name)
			if err := os.WriteFile(filepath.Join(snapshot, "source-dci-jsonl"), []byte(tt.data), 0o600); err != nil {
				t.Fatal(err)
			}
			manifest, err := DryRun(context.Background(), Options{
				SnapshotDir: snapshot, SourceDCI: "source-dci", SourceDCIJSONL: "source-dci-jsonl", SourceEventStore: "source-event-store", SourceL1: "source-l1", SourceArchive: "source-archive", Manifest: "manifest.json",
				Expected: ExpectedCounts{Searches: 1, ReadEvents: 1, EvidenceEvents: 1, TotalEvents: 4}, AgentIDs: testAgentIDs,
			})
			if err == nil || manifest.Status != StatusBlocked || manifest.ErrorCode != tt.code {
				t.Fatalf("result = %#v, err=%v, want code %s", manifest, err, tt.code)
			}
		})
	}
}

func TestDryRunDoesNotMutateSourcesOrCreateOtherOutput(t *testing.T) {
	snapshot := makeTestSnapshot(t, "no-mutation")
	sourceNames := []string{"source-dci", "source-dci-jsonl", "source-event-store", "source-l1", "source-archive"}
	before := make(map[string]string, len(sourceNames))
	for _, name := range sourceNames {
		before[name] = fileBytesHash(t, filepath.Join(snapshot, name))
	}
	if _, err := runTestSnapshotResult(t, snapshot, "manifest.json"); err != nil {
		t.Fatal(err)
	}
	for _, name := range sourceNames {
		if got := fileBytesHash(t, filepath.Join(snapshot, name)); got != before[name] {
			t.Fatalf("source %s changed: before=%s after=%s", name, before[name], got)
		}
	}
	entries, err := os.ReadDir(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(sourceNames)+1 {
		t.Fatalf("output entries = %d, want only five sources plus manifest", len(entries))
	}
	for _, entry := range entries {
		if entry.Name() != "manifest.json" && !strings.HasPrefix(entry.Name(), "source-") {
			t.Fatalf("unexpected dry-run output %q", entry.Name())
		}
	}
}

func TestDryRunClassifiesUnknownLegacyActorWithoutInference(t *testing.T) {
	snapshot := makeTestSnapshot(t, "legacy-actor")
	updateLegacyActor(t, filepath.Join(snapshot, "source-dci"), "Worker")
	writeJSONLTestActor(t, filepath.Join(snapshot, "source-dci-jsonl"), "legacy-search-1", "Worker")
	manifest, err := runTestSnapshotResult(t, snapshot, "manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ActorClassification.AuthenticatedAgent != 0 || manifest.ActorClassification.LegacyUnattributed != 1 {
		t.Fatalf("actor classification = %#v", manifest.ActorClassification)
	}
	if manifest.LegacyActorLabelCounts["Worker"] != 1 {
		t.Fatalf("legacy actor labels = %#v", manifest.LegacyActorLabelCounts)
	}
}

func TestDryRunRejectsPromotedArchiveReference(t *testing.T) {
	snapshot := makeTestSnapshot(t, "promoted-archive")
	addPromotedArchiveReference(t, filepath.Join(snapshot, "source-archive"))
	manifest, err := runTestSnapshotResult(t, snapshot, "manifest.json")
	if err == nil || manifest.Status != StatusBlocked || manifest.ErrorCode != "promoted_staging_reference" {
		t.Fatalf("result = %#v, err=%v", manifest, err)
	}
}

func makeTestSnapshot(t *testing.T, label string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), label)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	writeLegacyDCITestDB(t, filepath.Join(root, "source-dci"), "legacy-search-1", "legacy-evidence-1", false)
	writeJSONLTest(t, filepath.Join(root, "source-dci-jsonl"), "legacy-search-1")
	writeEventStoreTestDB(t, filepath.Join(root, "source-event-store"), nil)
	writeL1TestDB(t, filepath.Join(root, "source-l1"), "legacy-search-1", "legacy-evidence-1", true, false)
	writeArchiveTestDB(t, filepath.Join(root, "source-archive"), "legacy-search-1", "legacy-evidence-1", true)
	return root
}

func runTestSnapshot(t *testing.T, snapshot, manifest string) Manifest {
	t.Helper()
	got, err := runTestSnapshotResult(t, snapshot, filepath.Base(manifest))
	if err != nil {
		t.Fatalf("DryRun(%s): %v (%#v)", snapshot, err, got)
	}
	return got
}

func runTestSnapshotResult(t *testing.T, snapshot, manifest string) (Manifest, error) {
	t.Helper()
	manifestName := filepath.Base(manifest)
	got, err := DryRun(context.Background(), Options{
		SnapshotDir: snapshot, SourceDCI: "source-dci", SourceDCIJSONL: "source-dci-jsonl", SourceEventStore: "source-event-store", SourceL1: "source-l1", SourceArchive: "source-archive", Manifest: manifestName,
		Expected: ExpectedCounts{Searches: 1, ReadEvents: 1, EvidenceEvents: 1, TotalEvents: 4}, AgentIDs: testAgentIDs,
	})
	return got, err
}

func cloneManifest(manifest Manifest) Manifest {
	clone := manifest
	clone.ExclusionReasonCounts = cloneIntMap(manifest.ExclusionReasonCounts)
	clone.LegacyActorLabelCounts = cloneIntMap(manifest.LegacyActorLabelCounts)
	clone.SourceDatabaseLogicalSHA256 = cloneStringMap(manifest.SourceDatabaseLogicalSHA256)
	clone.SourceSchemaSHA256 = cloneStringMap(manifest.SourceSchemaSHA256)
	clone.SourceDCIClassificationSHA256 = cloneStringMap(manifest.SourceDCIClassificationSHA256)
	clone.SourceFileSHA256 = cloneStringMap(manifest.SourceFileSHA256)
	clone.SourceNonDCILogicalSHA256 = cloneStringMap(manifest.SourceNonDCILogicalSHA256)
	return clone
}

func cloneIntMap(values map[string]int) map[string]int {
	if values == nil {
		return nil
	}
	clone := make(map[string]int, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func writeLegacyDCITestDB(t *testing.T, path, searchID, evidenceID string, failed bool) {
	t.Helper()
	db := openTestDB(t, path)
	defer db.Close()
	mustExec(t, db, `CREATE TABLE dci_search_trace (event_id TEXT PRIMARY KEY, started_at TEXT NOT NULL, ended_at TEXT, actor TEXT NOT NULL, mode TEXT NOT NULL, user_query TEXT, corpus_scope TEXT, status TEXT NOT NULL, final_evidence_count INTEGER DEFAULT 0, error_message TEXT)`)
	mustExec(t, db, `CREATE TABLE dci_search_step (id INTEGER PRIMARY KEY AUTOINCREMENT, event_id TEXT NOT NULL, step_no INTEGER NOT NULL, tool TEXT NOT NULL, command_text TEXT, file_path TEXT, result_count INTEGER, status TEXT NOT NULL, error_message TEXT, created_at TEXT NOT NULL)`)
	mustExec(t, db, `CREATE TABLE dci_evidence (evidence_id TEXT PRIMARY KEY, event_id TEXT NOT NULL, source_id TEXT, file_path TEXT NOT NULL, heading TEXT, line_start INTEGER, line_end INTEGER, snippet TEXT NOT NULL, reason TEXT, confidence REAL DEFAULT 0.0, created_at TEXT NOT NULL)`)
	mustExec(t, db, `CREATE TABLE dci_query_terms (id INTEGER PRIMARY KEY AUTOINCREMENT, event_id TEXT NOT NULL, term TEXT NOT NULL, term_type TEXT, parent_term TEXT, created_at TEXT NOT NULL)`)
	status, errorMessage := "completed", ""
	if failed {
		status, errorMessage = "failed", "legacy failure"
	}
	mustExec(t, db, `INSERT INTO dci_search_trace(event_id,started_at,ended_at,actor,mode,user_query,corpus_scope,status,final_evidence_count,error_message) VALUES(?,?,?,?,?,?,?,?,?,?)`, searchID, "2026-08-31T00:00:00Z", "2026-08-31T00:00:02Z", "shiro", "dci", "canonical migration test", `[]`, status, 1, errorMessage)
	mustExec(t, db, `INSERT INTO dci_search_step(event_id,step_no,tool,command_text,file_path,result_count,status,error_message,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, searchID, 1, "read_file", "read", "spec.md", 1, "ok", "", "2026-08-31T00:00:01Z")
	mustExec(t, db, `INSERT INTO dci_evidence(evidence_id,event_id,source_id,file_path,heading,line_start,line_end,snippet,reason,confidence,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, evidenceID, searchID, "source-1", "spec.md", "Heading", 1, 1, "evidence text", "matched", 0.8, "2026-08-31T00:00:01Z")
}

func writeJSONLTest(t *testing.T, path, searchID string) {
	t.Helper()
	writeJSONLTestActor(t, path, searchID, "shiro")
}

func writeJSONLTestActor(t *testing.T, path, searchID, actor string) {
	t.Helper()
	record := `{"event_id":"` + searchID + `","started_at":"2026-08-31T00:00:00Z","ended_at":"2026-08-31T00:00:02Z","actor":"` + actor + `","mode":"dci","user_query":"canonical migration test","corpus_scope":[],"steps":[{"step_no":1,"tool":"read_file","command_text":"read","file_path":"spec.md","result_count":1,"status":"ok","error_message":"","created_at":"2026-08-31T00:00:01Z"}],"final_evidence_count":1,"status":"completed","error_message":""}` + "\n"
	if err := os.WriteFile(path, []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeEventStoreTestDB(t *testing.T, path string, eventIDs []string) {
	t.Helper()
	db := openTestDB(t, path)
	defer db.Close()
	mustExec(t, db, `CREATE TABLE event_envelope (event_id TEXT PRIMARY KEY NOT NULL, trace_id TEXT NOT NULL, schema_version TEXT NOT NULL, event_type TEXT NOT NULL, component_id TEXT NOT NULL, occurred_at TEXT NOT NULL, envelope_json TEXT NOT NULL)`)
	mustExec(t, db, `CREATE TABLE event_dependency (event_id TEXT NOT NULL, dependency_event_id TEXT NOT NULL, relation_type TEXT NOT NULL CHECK (relation_type IN ('causation','dependency')), PRIMARY KEY(event_id,dependency_event_id), FOREIGN KEY(event_id) REFERENCES event_envelope(event_id) ON UPDATE RESTRICT ON DELETE RESTRICT, FOREIGN KEY(dependency_event_id) REFERENCES event_envelope(event_id) ON UPDATE RESTRICT ON DELETE RESTRICT)`)
	for _, eventID := range eventIDs {
		event := canonicalTestEvent(t, modulecore.EventID(eventID), "conversation.message.received", modulecore.NewTraceID())
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		mustExec(t, db, `INSERT INTO event_envelope(event_id,trace_id,schema_version,event_type,component_id,occurred_at,envelope_json) VALUES(?,?,?,?,?,?,?)`, eventID, event.TraceID, event.SchemaVersion, event.EventType, event.ComponentID, event.OccurredAt.Format(time.RFC3339Nano), encoded)
	}
}

func writeL1TestDB(t *testing.T, path, searchID, evidenceID string, includeEvidence, promoted bool) {
	t.Helper()
	db := openTestDB(t, path)
	defer db.Close()
	mustExec(t, db, `CREATE TABLE l1_staging_item (id TEXT PRIMARY KEY, kind TEXT NOT NULL, namespace TEXT NOT NULL, event_id TEXT NOT NULL, source_id TEXT NOT NULL, source_url TEXT NOT NULL DEFAULT '', fetched_at TIMESTAMP NOT NULL, published_at TIMESTAMP, raw_text TEXT NOT NULL, raw_hash TEXT NOT NULL, summary_draft TEXT NOT NULL DEFAULT '', keywords_json TEXT NOT NULL DEFAULT '[]', license_note TEXT NOT NULL DEFAULT '', validation_status TEXT NOT NULL, meta_json TEXT NOT NULL DEFAULT '{}', created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL)`)
	mustExec(t, db, `CREATE TABLE l1_source_registry (source_id TEXT PRIMARY KEY, url TEXT NOT NULL, kind TEXT NOT NULL, trust_score REAL NOT NULL, fetch_interval_sec INTEGER NOT NULL, license_note TEXT NOT NULL, enabled INTEGER NOT NULL, meta_json TEXT NOT NULL DEFAULT '{}', last_fetched_at TIMESTAMP, last_status TEXT NOT NULL DEFAULT '', last_error TEXT NOT NULL DEFAULT '', created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL)`)
	if includeEvidence {
		meta := `{"source_kind":"dci","search_event_id":"` + searchID + `","evidence_id":"` + evidenceID + `","file_path":"spec.md","line_start":1,"line_end":1,"heading":"Heading","reason":"matched","confidence":0.8}`
		mustExec(t, db, `INSERT INTO l1_staging_item(id,kind,namespace,event_id,source_id,fetched_at,raw_text,raw_hash,validation_status,meta_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, "staging-1", "search_result", "kb:dci", searchID+":"+evidenceID, "source-1", "2026-08-31T00:00:01Z", "evidence text", rawTextSHA256("evidence text"), "pending", meta, "2026-08-31T00:00:01Z", "2026-08-31T00:00:01Z")
		registryMeta := `{"source_kind":"dci","search_event_id":"` + searchID + `","evidence_id":"` + evidenceID + `"}`
		mustExec(t, db, `INSERT INTO l1_source_registry(source_id,url,kind,trust_score,fetch_interval_sec,license_note,enabled,meta_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, "source-1", "https://local.invalid/source", "search_fallback", 0.5, 3600, "test", 0, registryMeta, "2026-08-31T00:00:00Z", "2026-08-31T00:00:00Z")
	}
	if promoted {
		mustExec(t, db, `CREATE TABLE l1_news_item (id TEXT PRIMARY KEY, staging_id TEXT NOT NULL UNIQUE)`)
		mustExec(t, db, `INSERT INTO l1_news_item(id,staging_id) VALUES(?,?)`, "news-1", "staging-1")
	}
}

func writeArchiveTestDB(t *testing.T, path, searchID, evidenceID string, includeEvidence bool) {
	t.Helper()
	db := openTestDB(t, path)
	defer db.Close()
	mustExec(t, db, `CREATE TABLE l1_staging_item_archive (id VARCHAR PRIMARY KEY, kind VARCHAR NOT NULL, namespace VARCHAR NOT NULL, event_id VARCHAR NOT NULL, source_id VARCHAR NOT NULL, source_url TEXT NOT NULL, fetched_at TIMESTAMP NOT NULL, published_at TIMESTAMP, raw_text TEXT NOT NULL, raw_hash VARCHAR NOT NULL, summary_draft TEXT NOT NULL, keywords_json TEXT NOT NULL, license_note TEXT NOT NULL, validation_status VARCHAR NOT NULL, meta_json TEXT NOT NULL, created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL)`)
	if includeEvidence {
		meta := `{"source_kind":"dci","search_event_id":"` + searchID + `","evidence_id":"` + evidenceID + `","file_path":"spec.md","line_start":1,"line_end":1,"heading":"Heading","reason":"matched","confidence":0.8}`
		mustExec(t, db, `INSERT INTO l1_staging_item_archive(id,kind,namespace,event_id,source_id,source_url,fetched_at,raw_text,raw_hash,summary_draft,keywords_json,license_note,validation_status,meta_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, "archive-1", "search_result", "kb:dci", searchID+":"+evidenceID, "source-1", "https://local.invalid/source", "2026-08-31T00:00:01Z", "evidence text", rawTextSHA256("evidence text"), "summary", "[]", "test", "pending", meta, "2026-08-31T00:00:01Z", "2026-08-31T00:00:01Z")
	}
}

func openTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func updateLegacyStepTool(t *testing.T, snapshot, tool string) {
	t.Helper()
	db := openTestDB(t, filepath.Join(snapshot, "source-dci"))
	defer db.Close()
	mustExec(t, db, `UPDATE dci_search_step SET tool=?`, tool)
}

func updateLegacyActor(t *testing.T, path, actor string) {
	t.Helper()
	db := openTestDB(t, path)
	defer db.Close()
	mustExec(t, db, `UPDATE dci_search_trace SET actor=?`, actor)
}

func updateLegacyEvidenceParent(t *testing.T, snapshot, searchID string) {
	t.Helper()
	db := openTestDB(t, filepath.Join(snapshot, "source-dci"))
	defer db.Close()
	mustExec(t, db, `UPDATE dci_evidence SET event_id=?`, searchID)
}

func addPromotedReference(t *testing.T, snapshot string) {
	t.Helper()
	db := openTestDB(t, filepath.Join(snapshot, "source-l1"))
	defer db.Close()
	mustExec(t, db, `CREATE TABLE l1_knowledge_item (id TEXT PRIMARY KEY, staging_id TEXT NOT NULL UNIQUE)`)
	mustExec(t, db, `INSERT INTO l1_knowledge_item(id,staging_id) VALUES(?,?)`, "knowledge-1", "staging-1")
}

func addPromotedArchiveReference(t *testing.T, path string) {
	t.Helper()
	db := openTestDB(t, path)
	defer db.Close()
	mustExec(t, db, `CREATE TABLE l1_knowledge_item_archive (id TEXT PRIMARY KEY, staging_id TEXT NOT NULL UNIQUE)`)
	mustExec(t, db, `INSERT INTO l1_knowledge_item_archive(id,staging_id) VALUES(?,?)`, "archive-knowledge-1", "archive-1")
}

func updateL1EvidenceSnippet(t *testing.T, path, snippet string) {
	t.Helper()
	db := openTestDB(t, path)
	defer db.Close()
	mustExec(t, db, `UPDATE l1_staging_item SET raw_text=?, raw_hash=?`, snippet, rawTextSHA256(snippet))
}

func updateEvidenceSnippetAcrossSources(t *testing.T, snapshot, snippet string) {
	t.Helper()
	updates := []struct {
		path       string
		query      string
		updateHash bool
	}{
		{path: filepath.Join(snapshot, "source-dci"), query: `UPDATE dci_evidence SET snippet=?`},
		{path: filepath.Join(snapshot, "source-l1"), query: `UPDATE l1_staging_item SET raw_text=?, raw_hash=?`, updateHash: true},
		{path: filepath.Join(snapshot, "source-archive"), query: `UPDATE l1_staging_item_archive SET raw_text=?, raw_hash=?`, updateHash: true},
	}
	for _, update := range updates {
		db := openTestDB(t, update.path)
		if update.updateHash {
			mustExec(t, db, update.query, snippet, rawTextSHA256(snippet))
		} else {
			mustExec(t, db, update.query, snippet)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close updated source %s: %v", filepath.Base(update.path), err)
		}
	}
}

func readL1RawFields(t *testing.T, path, table string) (string, string) {
	t.Helper()
	db := openTestDB(t, path)
	defer db.Close()
	var rawText, rawHash string
	if err := db.QueryRow(`SELECT raw_text, raw_hash FROM `+table+` LIMIT 1`).Scan(&rawText, &rawHash); err != nil {
		t.Fatalf("read %s raw fields: %v", filepath.Base(path), err)
	}
	return rawText, rawHash
}

func addUnknownDCITable(t *testing.T, path string) {
	t.Helper()
	db := openTestDB(t, path)
	defer db.Close()
	mustExec(t, db, `CREATE TABLE unexpected (id TEXT)`)
}

func addCollisionEvent(t *testing.T, path, searchID string) {
	t.Helper()
	db := openTestDB(t, path)
	defer db.Close()
	eventID, err := modulecore.NewMigrationID(modulecore.CanonicalEventID, "dci_search_trace", "started_event_id", searchID)
	if err != nil {
		t.Fatal(err)
	}
	traceID, err := modulecore.NewMigrationID(modulecore.CanonicalTraceID, "dci_search_trace", "event_id", searchID)
	if err != nil {
		t.Fatal(err)
	}
	event := canonicalTestEvent(t, modulecore.EventID(eventID), "conversation.message.received", modulecore.TraceID(traceID))
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO event_envelope(event_id,trace_id,schema_version,event_type,component_id,occurred_at,envelope_json) VALUES(?,?,?,?,?,?,?)`, event.EventID, event.TraceID, event.SchemaVersion, event.EventType, event.ComponentID, event.OccurredAt.Format(time.RFC3339Nano), encoded)
}

func canonicalTestEvent(t *testing.T, eventID modulecore.EventID, eventType string, traceID modulecore.TraceID) modulecore.EventEnvelope {
	t.Helper()
	return modulecore.EventEnvelope{
		SchemaVersion: modulecore.EventEnvelopeSchemaVersion,
		EventID:       eventID, TraceID: traceID, EventType: eventType, ComponentID: "test",
		OccurredAt: time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC),
		Payload:    map[string]any{"fixture": "event-store"},
	}
}

func addExistingDCIEvent(t *testing.T, path string) {
	t.Helper()
	db := openTestDB(t, path)
	defer db.Close()
	event := canonicalTestEvent(t, modulecore.NewEventID(), "dci.search.completed", modulecore.NewTraceID())
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO event_envelope(event_id,trace_id,schema_version,event_type,component_id,occurred_at,envelope_json) VALUES(?,?,?,?,?,?,?)`, event.EventID, event.TraceID, event.SchemaVersion, event.EventType, event.ComponentID, event.OccurredAt.Format(time.RFC3339Nano), encoded)
}

func mutateEventEnvelopeColumnMismatch(t *testing.T, path string) {
	t.Helper()
	db := openTestDB(t, path)
	defer db.Close()
	event := canonicalTestEvent(t, modulecore.NewEventID(), "conversation.message.received", modulecore.NewTraceID())
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	otherTrace := modulecore.NewTraceID()
	mustExec(t, db, `INSERT INTO event_envelope(event_id,trace_id,schema_version,event_type,component_id,occurred_at,envelope_json) VALUES(?,?,?,?,?,?,?)`, event.EventID, otherTrace, event.SchemaVersion, event.EventType, event.ComponentID, event.OccurredAt.Format(time.RFC3339Nano), encoded)
}

func mutateEventDependencyMismatch(t *testing.T, path string) {
	t.Helper()
	db := openTestDB(t, path)
	defer db.Close()
	traceID := modulecore.NewTraceID()
	parent := canonicalTestEvent(t, modulecore.NewEventID(), "conversation.message.received", traceID)
	child := canonicalTestEvent(t, modulecore.NewEventID(), "routing.selected", traceID)
	child.CausationEventID = parent.EventID
	for _, event := range []modulecore.EventEnvelope{parent, child} {
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		mustExec(t, db, `INSERT INTO event_envelope(event_id,trace_id,schema_version,event_type,component_id,occurred_at,envelope_json) VALUES(?,?,?,?,?,?,?)`, event.EventID, event.TraceID, event.SchemaVersion, event.EventType, event.ComponentID, event.OccurredAt.Format(time.RFC3339Nano), encoded)
	}
	// Deliberately persist the same edge with the wrong relation type.  The
	// envelope says causation; a loose row-level validator used to accept this.
	mustExec(t, db, `INSERT INTO event_dependency(event_id,dependency_event_id,relation_type) VALUES(?,?,?)`, child.EventID, parent.EventID, "dependency")
}

func fileBytesHash(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
