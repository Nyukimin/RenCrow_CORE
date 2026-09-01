package dcimigration

import (
	"context"
	"path/filepath"
	"testing"

	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestMaterializeMigrationRecordsUsesRetainedPlanAndHistoricalEvidenceTime(t *testing.T) {
	snapshot := makeTestSnapshot(t, "materialize-retained-plan")
	report := classifyTestMigrationSnapshot(t, snapshot, ExpectedCounts{Searches: 1, ReadEvents: 1, EvidenceEvents: 1, TotalEvents: 4})
	records, err := materializeMigrationRecords(context.Background(), report.Snapshot, report.Plan)
	if err != nil {
		t.Fatalf("materializeMigrationRecords: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("materialized records = %d, want 1", len(records))
	}
	record := records[0]
	if err := domaindci.ValidateStoredSearchResult(record.Result); err != nil {
		t.Fatalf("materialized result validation: %v", err)
	}
	searchIDs := report.Plan.searches["legacy-search-1"]
	if record.Result.Trace.ActionID != searchIDs.actionID || record.Result.Trace.TraceID != searchIDs.traceID || record.Result.Trace.ActorAttribution != searchIDs.actorAttribution || record.Result.Trace.ActorKind != searchIDs.actorKind || record.Result.Trace.ActorID != searchIDs.actorID {
		t.Fatalf("materialized trace does not use retained search mapping: %#v", record.Result.Trace)
	}
	if record.Result.Trace.IdempotencyKey != "" || len(record.Result.Trace.Steps) != 1 || record.Result.Trace.Steps[0].EventID != report.Plan.readEvents[readEventKey{searchID: "legacy-search-1", stepNo: 1}] {
		t.Fatalf("materialized trace identity/step = %#v", record.Result.Trace)
	}
	if len(record.Result.Pack.Evidence) != 1 {
		t.Fatalf("materialized evidence = %#v", record.Result.Pack.Evidence)
	}
	evidence := record.Result.Pack.Evidence[0]
	evidenceIDs := report.Plan.evidence["legacy-evidence-1"]
	if evidence.EvidenceID != evidenceIDs.evidenceID || evidence.CreatedByEventID != evidenceIDs.createdEventID || evidence.Snippet != report.Snapshot.Evidence["legacy-evidence-1"].Snippet {
		t.Fatalf("materialized evidence does not use retained mapping/snapshot: %#v", evidence)
	}
	if got := record.EvidenceCreatedAt[evidence.EvidenceID]; !got.Equal(report.Snapshot.Evidence["legacy-evidence-1"].CreatedAt) {
		t.Fatalf("historical evidence created_at = %v, want %v", got, report.Snapshot.Evidence["legacy-evidence-1"].CreatedAt)
	}
}

func TestMaterializeMigrationRecordsPreservesLegacyUnattributedActor(t *testing.T) {
	snapshot := makeTestSnapshot(t, "materialize-legacy-actor")
	updateLegacyActor(t, filepath.Join(snapshot, "source-dci"), "Worker")
	writeJSONLTestActor(t, filepath.Join(snapshot, "source-dci-jsonl"), "legacy-search-1", "Worker")
	report := classifyTestMigrationSnapshot(t, snapshot, ExpectedCounts{Searches: 1, ReadEvents: 1, EvidenceEvents: 1, TotalEvents: 4})
	records, err := materializeMigrationRecords(context.Background(), report.Snapshot, report.Plan)
	if err != nil {
		t.Fatalf("materializeMigrationRecords: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("materialized records = %d, want 1", len(records))
	}
	trace := records[0].Result.Trace
	if trace.ActorAttribution != domaindci.ActorAttributionLegacyUnattributed || trace.ActorKind != "" || trace.ActorID != "" {
		t.Fatalf("legacy actor attribution = %#v", trace)
	}
	if err := domaindci.ValidateStoredSearchResult(records[0].Result); err != nil {
		t.Fatalf("legacy materialized result validation: %v", err)
	}
}

func TestMaterializeMigrationRecordsRejectsMissingAndExtraPlanMappings(t *testing.T) {
	snapshot := makeTestSnapshot(t, "materialize-plan-coverage")
	report := classifyTestMigrationSnapshot(t, snapshot, ExpectedCounts{Searches: 1, ReadEvents: 1, EvidenceEvents: 1, TotalEvents: 4})
	tests := []struct {
		name   string
		mutate func(*migrationPlan)
	}{
		{name: "missing search", mutate: func(plan *migrationPlan) { delete(plan.searches, "legacy-search-1") }},
		{name: "extra search", mutate: func(plan *migrationPlan) { plan.searches["extra"] = searchMigrationIDs{} }},
		{name: "missing read", mutate: func(plan *migrationPlan) {
			delete(plan.readEvents, readEventKey{searchID: "legacy-search-1", stepNo: 1})
		}},
		{name: "extra read", mutate: func(plan *migrationPlan) {
			plan.readEvents[readEventKey{searchID: "extra", stepNo: 1}] = modulecore.EventID("evt_extra")
		}},
		{name: "missing evidence", mutate: func(plan *migrationPlan) { delete(plan.evidence, "legacy-evidence-1") }},
		{name: "extra evidence", mutate: func(plan *migrationPlan) { plan.evidence["extra"] = evidenceMigrationIDs{} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := cloneMigrationPlanForTest(report.Plan)
			tt.mutate(&plan)
			if _, err := materializeMigrationRecords(context.Background(), report.Snapshot, plan); err == nil {
				t.Fatalf("materializeMigrationRecords(%s) = nil error, want coverage rejection", tt.name)
			}
		})
	}
}

func classifyTestMigrationSnapshot(t *testing.T, snapshot string, expected ExpectedCounts) classificationReport {
	t.Helper()
	options := Options{
		SnapshotDir: snapshot, SourceDCI: "source-dci", SourceDCIJSONL: "source-dci-jsonl",
		SourceEventStore: "source-event-store", SourceL1: "source-l1", SourceArchive: "source-archive",
		Manifest: "materialize-manifest.json", Expected: expected, AgentIDs: testAgentIDs,
	}
	paths, err := resolvePaths(options)
	if err != nil {
		t.Fatalf("resolvePaths: %v", err)
	}
	report, err := classifySnapshot(context.Background(), paths, options)
	if err != nil {
		t.Fatalf("classifySnapshot: %v", err)
	}
	return report
}

func cloneMigrationPlanForTest(plan migrationPlan) migrationPlan {
	clone := plan
	clone.Events = append([]modulecore.EventEnvelope(nil), plan.Events...)
	clone.mappingLines = append([]string(nil), plan.mappingLines...)
	clone.searches = make(map[string]searchMigrationIDs, len(plan.searches))
	for key, value := range plan.searches {
		clone.searches[key] = value
	}
	clone.readEvents = make(map[readEventKey]modulecore.EventID, len(plan.readEvents))
	for key, value := range plan.readEvents {
		clone.readEvents[key] = value
	}
	clone.evidence = make(map[string]evidenceMigrationIDs, len(plan.evidence))
	for key, value := range plan.evidence {
		clone.evidence[key] = value
	}
	return clone
}
