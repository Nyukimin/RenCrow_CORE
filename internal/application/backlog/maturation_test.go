package backlog

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
)

func maturationCandidate(id string, start time.Time) domainbacklog.Item {
	return domainbacklog.Item{
		SchemaVersion:        domainbacklog.SchemaVersion2,
		ItemID:               id,
		Title:                "maturation " + id,
		Purpose:              "validate Atlas maturation",
		ConceptState:         domainbacklog.ConceptCandidate,
		DeliveryState:        domainbacklog.DeliveryNone,
		MaturationState:      domainbacklog.MaturationStateMaturation,
		MaturationStartedAt:  start.UTC().Format(time.RFC3339),
		MaturationEligibleAt: start.UTC().Add(maturationPeriod).Format(time.RFC3339),
		CreatedAt:            start.UTC().Format(time.RFC3339),
		UpdatedAt:            start.UTC().Format(time.RFC3339),
		SourceRefs: []domainbacklog.SourceRef{{
			Type: "test", Locator: id,
		}},
	}
}

func TestCandidateInitializesAndReplaysMaturationWithoutReset(t *testing.T) {
	start := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	clock := start
	store := &memoryItemStore{}
	service := NewService(store, nil).WithClock(func() time.Time { return clock })
	intake, err := service.Intake(context.Background(), IntakeRequest{
		ItemID: "candidate-clock", Title: "candidate clock", Purpose: "maturation clock",
		SourceRefs: []domainbacklog.SourceRef{{Type: "test", Locator: "candidate-clock"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Candidate(context.Background(), intake.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	if first.MaturationState != domainbacklog.MaturationStateMaturation || first.MaturationStartedAt != start.Format(time.RFC3339) || first.MaturationEligibleAt != start.Add(maturationPeriod).Format(time.RFC3339) {
		t.Fatalf("candidate maturation=%+v", first)
	}
	clock = start.Add(2 * time.Hour)
	second, err := service.Candidate(context.Background(), intake.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	if second.MaturationStartedAt != first.MaturationStartedAt || second.MaturationEligibleAt != first.MaturationEligibleAt || second.MaturationState != first.MaturationState {
		t.Fatalf("idempotent Candidate reset maturation: first=%+v second=%+v", first, second)
	}
}

func TestCandidateBackfillsLegacyClockAndDoesNotTouchAdoptedHistory(t *testing.T) {
	created := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	legacy := maturationCandidate("legacy-candidate", created)
	legacy.MaturationState = ""
	legacy.MaturationStartedAt = ""
	legacy.MaturationEligibleAt = ""
	legacy.CreatedAt = created.Format(time.RFC3339)
	unknown := legacy
	unknown.ItemID = "unknown-created"
	unknown.Title = "unknown created"
	unknown.CreatedAt = "not-a-timestamp"
	unknown.SourceRefs = []domainbacklog.SourceRef{{Type: "test", Locator: unknown.ItemID}}
	adopted := domainbacklog.Item{
		SchemaVersion: domainbacklog.SchemaVersion2, ItemID: "already-adopted", Title: "adopted",
		ConceptState: domainbacklog.ConceptAdopted, DeliveryState: domainbacklog.DeliveryQueued,
		CreatedAt: created.Format(time.RFC3339), UpdatedAt: created.Format(time.RFC3339),
	}
	beforeAdopted := adopted
	store := &memoryItemStore{items: []domainbacklog.Item{legacy, unknown, adopted}}
	service := NewService(store, nil).WithClock(func() time.Time { return now })
	got, err := service.Candidate(context.Background(), legacy.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	if got.MaturationStartedAt != created.Format(time.RFC3339) || got.MaturationEligibleAt != created.Add(maturationPeriod).Format(time.RFC3339) {
		t.Fatalf("legacy CreatedAt was not used: %+v", got)
	}
	got, err = service.Candidate(context.Background(), unknown.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	if got.MaturationStartedAt != now.Format(time.RFC3339) || got.MaturationEligibleAt != now.Add(maturationPeriod).Format(time.RFC3339) {
		t.Fatalf("unparseable CreatedAt did not use service clock: %+v", got)
	}
	if _, err := service.Candidate(context.Background(), adopted.ItemID); err == nil {
		t.Fatal("already-adopted item unexpectedly became a candidate")
	}
	if !reflect.DeepEqual(store.items[2], beforeAdopted) {
		t.Fatalf("adopted history changed: got=%+v before=%+v", store.items[2], beforeAdopted)
	}
}

func TestRevalidateUsesExactSevenDayBoundaryAndPreservesAuditPayload(t *testing.T) {
	start := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	now := start.Add(7*24*time.Hour - time.Second)
	item := maturationCandidate("boundary", start)
	store := &memoryItemStore{items: []domainbacklog.Item{item}}
	service := NewService(store, nil).WithClock(func() time.Time { return now })
	request := RevalidateRequest{
		Decision:        domainbacklog.RevalidationDecisionPromote,
		Reason:          "still aligned with the purpose",
		RelatedBacklogs: []string{"related-1"}, ConflictingSpecs: []string{"spec-1"},
		TechnologyChanges: []string{"Go"}, ArchitectureImpact: "none",
		ImplementationValue: "high", NextReviewTrigger: "new evidence",
		ReviewAgents: []string{"Mio", "Shiro"},
	}
	before, err := service.Revalidate(context.Background(), item.ItemID, request)
	if !errors.Is(err, ErrMaturationNotEligible) || before.ItemID != "" {
		t.Fatalf("pre-boundary revalidation result=%+v err=%v", before, err)
	}
	if !reflect.DeepEqual(store.items[0], item) {
		t.Fatalf("rejected early request mutated item: got=%+v want=%+v", store.items[0], item)
	}
	now = start.Add(7 * 24 * time.Hour)
	result, err := service.Revalidate(context.Background(), item.ItemID, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.MaturationState != domainbacklog.MaturationStatePromoted || result.ConceptState != domainbacklog.ConceptCandidate || result.DeliveryState != domainbacklog.DeliveryNone {
		t.Fatalf("PROMOTE changed orthogonal states: %+v", result)
	}
	if len(result.RevalidationRecords) != 1 {
		t.Fatalf("records=%+v", result.RevalidationRecords)
	}
	record := result.RevalidationRecords[0]
	if record.BacklogID != item.ItemID || record.RevalidationDate != now.Format(time.RFC3339) || record.MaturationDays != 7 || record.Decision != domainbacklog.RevalidationDecisionPromote || record.Reason != request.Reason || !reflect.DeepEqual(record.RelatedBacklogs, request.RelatedBacklogs) || !reflect.DeepEqual(record.ConflictingSpecs, request.ConflictingSpecs) || !reflect.DeepEqual(record.TechnologyChanges, request.TechnologyChanges) || !reflect.DeepEqual(record.ReviewAgents, request.ReviewAgents) {
		t.Fatalf("revalidation audit not preserved: %+v", record)
	}
}

func TestRevalidateAppliesAllDecisionsAndValidatesMergeTarget(t *testing.T) {
	now := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	start := now.Add(-maturationPeriod)
	items := []domainbacklog.Item{
		maturationCandidate("promote", start),
		maturationCandidate("merge", start),
		maturationCandidate("hold", start),
		maturationCandidate("drop", start),
		maturationCandidate("merge-target", start),
	}
	store := &memoryItemStore{items: items}
	service := NewService(store, nil).WithClock(func() time.Time { return now })
	base := func(decision string) RevalidateRequest {
		return RevalidateRequest{Decision: decision, Reason: "owner review", ReviewAgents: []string{"Mio"}}
	}
	if got, err := service.Revalidate(context.Background(), "promote", base(domainbacklog.RevalidationDecisionPromote)); err != nil || got.MaturationState != domainbacklog.MaturationStatePromoted || got.ConceptState != domainbacklog.ConceptCandidate {
		t.Fatalf("PROMOTE got=%+v err=%v", got, err)
	}
	if _, err := service.Revalidate(context.Background(), "merge", base(domainbacklog.RevalidationDecisionMerge)); err == nil {
		t.Fatal("MERGE without target unexpectedly accepted")
	}
	merge := base(domainbacklog.RevalidationDecisionMerge)
	merge.MergedInto = "merge-target"
	got, err := service.Revalidate(context.Background(), "merge", merge)
	if err != nil {
		t.Fatal(err)
	}
	if got.MaturationState != domainbacklog.MaturationStateMerged || got.ConceptState != domainbacklog.ConceptDeferred || got.MergedInto != merge.MergedInto || got.RevalidationRecords[0].MergedInto != merge.MergedInto {
		t.Fatalf("MERGE result=%+v", got)
	}
	if _, err := service.Revalidate(context.Background(), "merge-target", func() RevalidateRequest {
		value := base(domainbacklog.RevalidationDecisionMerge)
		value.MergedInto = "merge-target"
		return value
	}()); err == nil {
		t.Fatal("self MERGE unexpectedly accepted")
	}
	holdRequest := base(domainbacklog.RevalidationDecisionHold)
	holdRequest.NextReviewTrigger = "dependency-ready"
	got, err = service.Revalidate(context.Background(), "hold", holdRequest)
	if err != nil || got.MaturationState != domainbacklog.MaturationStateHold || got.ConceptState != domainbacklog.ConceptDeferred {
		t.Fatalf("HOLD got=%+v err=%v", got, err)
	}
	got, err = service.Revalidate(context.Background(), "drop", base(domainbacklog.RevalidationDecisionDrop))
	if err != nil || got.MaturationState != domainbacklog.MaturationStateDropped || got.ConceptState != domainbacklog.ConceptRejected || got.DeliveryState != domainbacklog.DeliveryRejected {
		t.Fatalf("DROP got=%+v err=%v", got, err)
	}
	for _, invalid := range []RevalidateRequest{
		{Decision: "UNKNOWN", Reason: "reason", ReviewAgents: []string{"Mio"}},
		{Decision: domainbacklog.RevalidationDecisionHold, ReviewAgents: []string{"Mio"}},
		{Decision: domainbacklog.RevalidationDecisionHold, Reason: "reason"},
	} {
		if _, err := service.Revalidate(context.Background(), "hold", invalid); err == nil {
			t.Fatalf("invalid request unexpectedly accepted: %+v", invalid)
		}
	}
}

func TestRevalidateHoldCanBeTriggeredBeforeNextEligibilityAndForceIsBounded(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	now := start.Add(maturationPeriod)
	items := []domainbacklog.Item{
		maturationCandidate("hold-trigger", start),
		maturationCandidate("force", start),
	}
	store := &memoryItemStore{items: items}
	service := NewService(store, nil).WithClock(func() time.Time { return now })
	hold, err := service.Revalidate(context.Background(), "hold-trigger", RevalidateRequest{
		Decision: domainbacklog.RevalidationDecisionHold, Reason: "wait for runtime evidence", ReviewAgents: []string{"Mio"},
		NextReviewTrigger: "runtime evidence",
	})
	if err != nil || hold.MaturationState != domainbacklog.MaturationStateHold {
		t.Fatalf("initial HOLD=%+v err=%v", hold, err)
	}
	now = start.Add(24 * time.Hour)
	promoted, err := service.Revalidate(context.Background(), "hold-trigger", RevalidateRequest{
		Decision: domainbacklog.RevalidationDecisionPromote, Reason: "trigger arrived", ReviewAgents: []string{"Shiro"},
		Trigger: "runtime evidence",
	})
	if err != nil || promoted.MaturationState != domainbacklog.MaturationStatePromoted {
		t.Fatalf("triggered revalidation=%+v err=%v", promoted, err)
	}
	now = start.Add(24 * time.Hour)
	if _, err := service.Revalidate(context.Background(), "force", RevalidateRequest{
		Decision: domainbacklog.RevalidationDecisionMerge, Reason: "merge now", ReviewAgents: []string{"Mio"}, Forced: true, MergedInto: "hold-trigger",
	}); !errors.Is(err, ErrMaturationForceMerge) {
		t.Fatalf("forced MERGE err=%v", err)
	}
	force := RevalidateRequest{Decision: domainbacklog.RevalidationDecisionPromote, Reason: "urgent continuity", ReviewAgents: []string{"Mio"}, Forced: true}
	if _, err := service.Revalidate(context.Background(), "force", force); !errors.Is(err, ErrMaturationBypassRequired) {
		t.Fatalf("early forced PROMOTE without bypass err=%v", err)
	}
	force.BypassReason = "operator_preference"
	if _, err := service.Revalidate(context.Background(), "force", force); !errors.Is(err, ErrMaturationBypassInvalid) {
		t.Fatalf("invalid bypass reason err=%v", err)
	}
	force.BypassReason = domainbacklog.MaturationBypassRuntimeContinuity
	result, err := service.Revalidate(context.Background(), "force", force)
	if err != nil {
		t.Fatal(err)
	}
	if !result.MaturationBypass || result.BypassReason != force.BypassReason || !result.RevalidationRecords[0].Forced || !result.RevalidationRecords[0].MaturationBypass || result.RevalidationRecords[0].BypassReason != force.BypassReason {
		t.Fatalf("forced bypass audit=%+v", result)
	}
}

func TestTriggeredReviewDoesNotInheritEarlierBypassIntoNewRecord(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	now := start.Add(24 * time.Hour)
	store := &memoryItemStore{items: []domainbacklog.Item{maturationCandidate("bypass-history", start)}}
	service := NewService(store, nil).WithClock(func() time.Time { return now })
	held, err := service.Revalidate(context.Background(), "bypass-history", RevalidateRequest{
		Decision: domainbacklog.RevalidationDecisionHold, Reason: "production failure needs observation",
		ReviewAgents: []string{"Mio"}, Forced: true, BypassReason: domainbacklog.MaturationBypassProductionFailure,
		NextReviewTrigger: "runtime recovered",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !held.RevalidationRecords[0].MaturationBypass {
		t.Fatalf("first record did not retain bypass: %+v", held.RevalidationRecords)
	}
	now = start.Add(48 * time.Hour)
	promoted, err := service.Revalidate(context.Background(), "bypass-history", RevalidateRequest{
		Decision: domainbacklog.RevalidationDecisionPromote, Reason: "runtime recovered",
		ReviewAgents: []string{"Shiro"}, Trigger: "runtime recovered",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(promoted.RevalidationRecords) != 2 || promoted.RevalidationRecords[1].MaturationBypass || promoted.RevalidationRecords[1].BypassReason != "" {
		t.Fatalf("triggered record inherited an old bypass: %+v", promoted.RevalidationRecords)
	}
}

func TestHoldRemainsEventDrivenAfterEligibilityDate(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	item := maturationCandidate("hold-event", now.Add(-30*24*time.Hour))
	item.ConceptState = domainbacklog.ConceptDeferred
	item.MaturationState = domainbacklog.MaturationStateHold
	item.NextReviewTrigger = "dependency-ready"
	store := &memoryItemStore{items: []domainbacklog.Item{item}}
	service := NewService(store, nil).WithClock(func() time.Time { return now })
	request := RevalidateRequest{Decision: domainbacklog.RevalidationDecisionPromote, Reason: "dependency is ready", ReviewAgents: []string{"Shiro"}}
	if _, err := service.Revalidate(context.Background(), item.ItemID, request); !errors.Is(err, ErrMaturationNotEligible) {
		t.Fatalf("triggerless expired HOLD error=%v want=%v", err, ErrMaturationNotEligible)
	}
	request.Trigger = "dependency-ready"
	if _, err := service.Revalidate(context.Background(), item.ItemID, request); err != nil {
		t.Fatalf("matching HOLD trigger rejected: %v", err)
	}
}

func TestHoldDecisionAlwaysRequiresNextReviewTrigger(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	item := maturationCandidate("hold-needs-trigger", now.Add(-8*24*time.Hour))
	store := &memoryItemStore{items: []domainbacklog.Item{item}}
	service := NewService(store, nil).WithClock(func() time.Time { return now })
	request := RevalidateRequest{Decision: domainbacklog.RevalidationDecisionHold, Reason: "wait", ReviewAgents: []string{"Shiro"}}
	if _, err := service.Revalidate(context.Background(), item.ItemID, request); !errors.Is(err, ErrMaturationDecisionInvalid) {
		t.Fatalf("HOLD without trigger error=%v want=%v", err, ErrMaturationDecisionInvalid)
	}
}

func TestEnrichMinorPreservesClockAndMaterialChangeResetsWithHistory(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	now := start.Add(maturationPeriod)
	item := maturationCandidate("enrich", start)
	item.MaturationState = domainbacklog.MaturationStatePromoted
	item.RevalidationRecords = []domainbacklog.RevalidationRecord{{
		BacklogID: item.ItemID, RevalidationDate: now.Format(time.RFC3339), MaturationDays: 7,
		Decision: domainbacklog.RevalidationDecisionPromote, Reason: "initial", ReviewAgents: []string{"Mio"},
	}}
	store := &memoryItemStore{items: []domainbacklog.Item{item}}
	service := NewService(store, nil).WithClock(func() time.Time { return now })
	minor, err := service.Enrich(context.Background(), item.ItemID, EnrichRequest{
		SourceRefs: []domainbacklog.SourceRef{{Type: "url", Locator: "https://example.test/new"}},
		RelatedIDs: []string{"related"}, RelationRefs: []string{"relation"},
		Body: "updated body", Background: "updated background", Priority: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	if minor.MaturationState != item.MaturationState || minor.MaturationStartedAt != item.MaturationStartedAt || minor.MaturationEligibleAt != item.MaturationEligibleAt || minor.LastMaterialChangeAt != item.LastMaterialChangeAt {
		t.Fatalf("minor enrichment reset maturation clocks: before=%+v after=%+v", item, minor)
	}
	if minor.Body != "updated body" || minor.Background != "updated background" || minor.Priority != "high" || len(minor.SourceRefs) != 2 || len(minor.RelatedIDs) != 1 || len(minor.RelationRefs) != 1 {
		t.Fatalf("minor enrichment fields=%+v", minor)
	}
	if _, err := service.Enrich(context.Background(), item.ItemID, EnrichRequest{MaterialChange: true}); !errors.Is(err, ErrMaturationReasonRequired) {
		t.Fatalf("material change without reason err=%v", err)
	}
	now = start.Add(8 * 24 * time.Hour)
	major, err := service.Enrich(context.Background(), item.ItemID, EnrichRequest{MaterialChange: true, Reason: "new architecture changes the value"})
	if err != nil {
		t.Fatal(err)
	}
	if major.MaturationState != domainbacklog.MaturationStateMaturation || major.ConceptState != domainbacklog.ConceptCandidate || major.MaturationStartedAt != now.Format(time.RFC3339) || major.MaturationEligibleAt != now.Add(maturationPeriod).Format(time.RFC3339) || major.LastMaterialChangeAt != now.Format(time.RFC3339) {
		t.Fatalf("material change did not reset maturation: %+v", major)
	}
	if len(major.RevalidationRecords) != 1 || major.RevalidationRecords[0].Decision != domainbacklog.RevalidationDecisionPromote {
		t.Fatalf("material change discarded audit history: %+v", major.RevalidationRecords)
	}
}

func TestBackfillPreservesMaturationRuntimeOverlay(t *testing.T) {
	record := domainbacklog.RevalidationRecord{
		BacklogID: "overlay", RevalidationDate: "2026-08-20T00:00:00Z", MaturationDays: 7,
		Decision: domainbacklog.RevalidationDecisionPromote, Reason: "retain", ReviewAgents: []string{"Mio"},
		TechnologyChanges: []string{"Go"}, RelatedBacklogs: []string{"other"},
	}
	current := maturationCandidate("overlay", time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	current.MaturationState = domainbacklog.MaturationStatePromoted
	current.LastMaterialChangeAt = "2026-08-10T00:00:00Z"
	current.MergedInto = "target"
	current.NextReviewTrigger = "trigger"
	current.MaturationBypass = true
	current.BypassReason = domainbacklog.MaturationBypassSecurityIssue
	current.RevalidationRecords = []domainbacklog.RevalidationRecord{record}
	incoming := domainbacklog.Item{ItemID: current.ItemID, Title: "incoming", ConceptState: domainbacklog.ConceptCandidate, DeliveryState: domainbacklog.DeliveryNone}
	got := preserveRuntimeOverlay(current, incoming)
	if got.MaturationState != current.MaturationState || got.MaturationStartedAt != current.MaturationStartedAt || got.MaturationEligibleAt != current.MaturationEligibleAt || got.LastMaterialChangeAt != current.LastMaterialChangeAt || got.MergedInto != current.MergedInto || got.NextReviewTrigger != current.NextReviewTrigger || got.MaturationBypass != current.MaturationBypass || got.BypassReason != current.BypassReason || !reflect.DeepEqual(got.RevalidationRecords, current.RevalidationRecords) {
		t.Fatalf("runtime maturation overlay lost fields: got=%+v current=%+v", got, current)
	}
	got.RevalidationRecords[0].ReviewAgents[0] = "mutated"
	if current.RevalidationRecords[0].ReviewAgents[0] == "mutated" {
		t.Fatal("runtime overlay aliased revalidation history")
	}
}
