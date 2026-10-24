package conversation

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

func TestPreparedRecallViewSelectsMeaningfulScopedBriefs(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	statement := "The owner likes blue glaze; buying it is only a future possibility."
	summary := "Not purchased yet; if it is purchased, the blue glaze is preferred."
	view, err := NewPreparedRecallView(PreparedRecallViewInput{
		AuthenticatedOwnerID: "owner-a",
		UserScope:            "user:owner-a",
		SourceVersion:        "owner-revision-17",
		MemoryBudget:         preparedRecallTestBudget(),
		Coverage: PreparedRecallCoverage{
			UserMemory: PreparedRecallCoverageFull,
			Knowledge:  PreparedRecallCoveragePartial,
		},
		EligibleUserMemories: []domainmemory.UserMemory{{
			ID: "memory-blue", UserID: "owner-a", Type: domainmemory.UserMemoryTypePreference,
			Statement: statement, EvidenceEventIDs: []string{"event-17"},
			UpdatedByEventID: "event-17", State: domainmemory.MemoryStateConfirmed,
			Scope: "mio_only", Sensitivity: "normal", Active: true,
		}},
		ValidatedCategoryRecords: []CategoryRecallRecord{{
			Category: "hobby", SourceID: "hobby-catalog", RecordID: "glaze-1",
			Title: "Blue glaze", Summary: summary,
			ProvenanceURLs: []string{"https://example.test/glaze-1"},
			RetrievedAt:    now.Add(-time.Hour), ValidatedAt: now.Add(-time.Minute),
			FreshUntil: now.Add(time.Hour), State: CategoryRecordStateValidated,
			Sensitivity: "normal", Scope: "user:owner-a", Roles: []string{"chat"},
		}},
	})
	if err != nil {
		t.Fatalf("NewPreparedRecallView: %v", err)
	}

	selected := view.Select(PreparedRecallSelectInput{
		AuthenticatedOwnerID: "owner-a", UserScope: "user:owner-a",
		ExpectedSourceVersion: "owner-revision-17", Message: "blue glaze",
		Persona: "mio", Role: "chat", Now: now,
	})
	if selected.PreparationStatus != PreparedRecallPreparationReady || selected.MatchStatus != PreparedRecallMatchMatched {
		t.Fatalf("unexpected selection status: %#v", selected)
	}
	if selected.SourceVersion != "owner-revision-17" || selected.Coverage.UserMemory != PreparedRecallCoverageFull || selected.Coverage.Knowledge != PreparedRecallCoveragePartial {
		t.Fatalf("owner version or coverage changed: %#v", selected)
	}
	if len(selected.UserMemories) != 1 || !selected.UserMemories[0].Decision.Selected || selected.UserMemories[0].Decision.Item.Statement != statement || selected.UserMemories[0].Representation != PreparedRecallRepresentationStatement {
		t.Fatalf("confirmed statement was not preserved: %#v", selected.UserMemories)
	}
	if got := selected.UserMemories[0].Decision.Item.EvidenceEventIDs; len(got) != 1 || got[0] != "event-17" {
		t.Fatalf("memory provenance was not preserved: %#v", got)
	}
	if len(selected.Knowledge) != 1 {
		t.Fatalf("knowledge selection count = %d, want 1", len(selected.Knowledge))
	}
	knowledge := selected.Knowledge[0]
	if knowledge.Representation != PreparedRecallRepresentationValidatedSummary || knowledge.Record.Summary != summary {
		t.Fatalf("validated brief was replaced or misclassified: %#v", knowledge)
	}
	if knowledge.Record.RecordID != "glaze-1" || knowledge.Record.Scope != "user:owner-a" || knowledge.Record.FreshUntil != now.Add(time.Hour) || len(knowledge.Record.ProvenanceURLs) != 1 || knowledge.Record.ProvenanceURLs[0] != "https://example.test/glaze-1" {
		t.Fatalf("scope, expiry, or provenance was not preserved: %#v", knowledge.Record)
	}
	if selected.Truncated.Keys || selected.Truncated.QueryRunes || selected.Truncated.PostingVisits || selected.Truncated.UserMemoryCandidates || selected.Truncated.KnowledgeCandidates {
		t.Fatalf("unexpected truncation: %#v", selected.Truncated)
	}
}

func TestPreparedRecallViewRequiresOwnerScopeAndOwnerVersion(t *testing.T) {
	base := PreparedRecallViewInput{
		AuthenticatedOwnerID: "owner-a", UserScope: "user:owner-a", SourceVersion: "owner-revision",
		MemoryBudget: preparedRecallTestBudget(),
		Coverage:     PreparedRecallCoverage{UserMemory: PreparedRecallCoverageNone, Knowledge: PreparedRecallCoverageNone},
	}
	cases := []struct {
		name   string
		change func(*PreparedRecallViewInput)
	}{
		{name: "authenticated owner", change: func(input *PreparedRecallViewInput) { input.AuthenticatedOwnerID = " " }},
		{name: "user scope", change: func(input *PreparedRecallViewInput) { input.UserScope = "" }},
		{name: "owner version", change: func(input *PreparedRecallViewInput) { input.SourceVersion = "" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.change(&input)
			if _, err := NewPreparedRecallView(input); err == nil {
				t.Fatal("NewPreparedRecallView accepted an unbound snapshot")
			}
		})
	}
}

func TestPreparedRecallViewRejectsUnboundedBuildInputs(t *testing.T) {
	base := PreparedRecallViewInput{
		AuthenticatedOwnerID: "owner-a", UserScope: "user:owner-a", SourceVersion: "bounded-build-rev",
		MemoryBudget: preparedRecallTestBudget(),
		Coverage:     PreparedRecallCoverage{UserMemory: PreparedRecallCoverageFull, Knowledge: PreparedRecallCoverageNone},
	}

	t.Run("too many records", func(t *testing.T) {
		input := base
		input.EligibleUserMemories = make([]domainmemory.UserMemory, 5001)
		for i := range input.EligibleUserMemories {
			input.EligibleUserMemories[i] = domainmemory.UserMemory{
				ID: fmt.Sprintf("memory-%05d", i), UserID: "owner-a", Statement: fmt.Sprintf("topic-%05d", i),
				State: domainmemory.MemoryStateConfirmed, Scope: "all", Sensitivity: "normal", Active: true,
			}
		}
		if _, err := NewPreparedRecallView(input); err == nil {
			t.Fatal("NewPreparedRecallView accepted an over-limit source population")
		}
	})

	t.Run("too many per-item keys", func(t *testing.T) {
		input := base
		terms := make([]string, 129)
		for i := range terms {
			terms[i] = fmt.Sprintf("qxwz%03d", i)
		}
		input.EligibleUserMemories = []domainmemory.UserMemory{{
			ID: "oversized-index-item", UserID: "owner-a", Statement: strings.Join(terms, " "),
			State: domainmemory.MemoryStateConfirmed, Scope: "all", Sensitivity: "normal", Active: true,
		}}
		if _, err := NewPreparedRecallView(input); err == nil {
			t.Fatal("NewPreparedRecallView accepted an item with unbounded index keys")
		}
	})

	t.Run("aggregate memory budget includes retained generations", func(t *testing.T) {
		input := base
		oldView, err := NewPreparedRecallView(input)
		if err != nil {
			t.Fatalf("build retained generation: %v", err)
		}
		oldBytes := oldView.EstimatedBytes()
		overlapLimit := 2*oldBytes + uint64(preparedRecallBuildScratchBytes)
		input.MemoryBudget = PreparedRecallMemoryBudget{
			AggregateLimitBytes: overlapLimit,
			RetainedBytes:       oldBytes,
		}
		newView, err := NewPreparedRecallView(input)
		if err != nil {
			t.Fatalf("build overlapping generation within reservation: %v", err)
		}
		if oldBytes+newView.EstimatedBytes()+uint64(preparedRecallBuildScratchBytes) > input.MemoryBudget.AggregateLimitBytes {
			t.Fatalf("overlapping generations exceeded caller reservation: old=%d new=%d limit=%d", oldBytes, newView.EstimatedBytes(), input.MemoryBudget.AggregateLimitBytes)
		}

		input.MemoryBudget.AggregateLimitBytes--
		if _, err := NewPreparedRecallView(input); !errors.Is(err, ErrPreparedRecallCapacity) {
			t.Fatalf("under-reserved overlapping build error=%v, want capacity rejection", err)
		}
	})

	t.Run("retained data estimate caps view bytes", func(t *testing.T) {
		input := base
		input.EligibleUserMemories = make([]domainmemory.UserMemory, 1200)
		longToken := strings.Repeat("x", PreparedRecallMaxIndexTextBytes)
		for i := range input.EligibleUserMemories {
			input.EligibleUserMemories[i] = domainmemory.UserMemory{
				ID: fmt.Sprintf("large-memory-%04d", i), UserID: "owner-a", Statement: longToken,
				State: domainmemory.MemoryStateConfirmed, Scope: "all", Sensitivity: "normal", Active: true,
			}
		}
		if _, err := NewPreparedRecallView(input); !errors.Is(err, ErrPreparedRecallCapacity) {
			t.Fatalf("over-budget snapshot error=%v, want capacity rejection", err)
		}
	})
}

func TestPreparedRecallViewRejectsCategoryRecordsWithoutExplicitScope(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	input := PreparedRecallViewInput{
		AuthenticatedOwnerID: "owner-a", UserScope: "user:owner-a", SourceVersion: "missing-scope-rev",
		MemoryBudget: preparedRecallTestBudget(),
		Coverage:     PreparedRecallCoverage{UserMemory: PreparedRecallCoverageNone, Knowledge: PreparedRecallCoverageFull},
		ValidatedCategoryRecords: []CategoryRecallRecord{
			preparedRecallCategory(now, "missing-scope", "blue glaze brief", "", time.Time{}),
		},
	}
	if _, err := NewPreparedRecallView(input); err == nil {
		t.Fatal("NewPreparedRecallView accepted a category record with no scope")
	}
}

func TestPreparedRecallViewDeniesOwnerOrScopeMismatchWithoutHints(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	view, err := NewPreparedRecallView(PreparedRecallViewInput{
		AuthenticatedOwnerID: "owner-a", UserScope: "user:owner-a", SourceVersion: "rev-a",
		MemoryBudget: preparedRecallTestBudget(),
		Coverage:     PreparedRecallCoverage{UserMemory: PreparedRecallCoverageFull, Knowledge: PreparedRecallCoverageFull},
		EligibleUserMemories: []domainmemory.UserMemory{
			{ID: "private-memory-id", UserID: "owner-a", Statement: "blue glaze private statement", State: domainmemory.MemoryStateConfirmed, Scope: "mio_only", Sensitivity: "normal", Active: true},
			{ID: "other-user-memory-id", UserID: "owner-b", Statement: "blue glaze private statement", State: domainmemory.MemoryStateConfirmed, Scope: "mio_only", Sensitivity: "normal", Active: true},
		},
		ValidatedCategoryRecords: []CategoryRecallRecord{
			{
				Category: "hobby", SourceID: "private-source", RecordID: "private-location-id",
				Title: "Private blue glaze", Summary: "Private validated brief",
				ProvenanceURLs: []string{"https://example.test/private"}, RetrievedAt: now,
				ValidatedAt: now, State: CategoryRecordStateValidated, Scope: "user:owner-a",
				Roles: []string{"chat"},
			},
			{
				Category: "hobby", SourceID: "other-user-source", RecordID: "other-user-location-id",
				Title: "Private blue glaze", Summary: "Other user's private brief",
				ProvenanceURLs: []string{"https://example.test/other-user"}, RetrievedAt: now,
				ValidatedAt: now, State: CategoryRecordStateValidated, Scope: "user:owner-b",
				Roles: []string{"chat"},
			},
		},
	})
	if err != nil {
		t.Fatalf("NewPreparedRecallView: %v", err)
	}

	for _, query := range []PreparedRecallSelectInput{
		{AuthenticatedOwnerID: "owner-b", UserScope: "user:owner-b", ExpectedSourceVersion: "rev-a", Message: "blue glaze", Persona: "mio", Role: "chat", Now: now},
		{AuthenticatedOwnerID: "owner-a", UserScope: "user:owner-b", ExpectedSourceVersion: "rev-a", Message: "blue glaze", Persona: "mio", Role: "chat", Now: now},
	} {
		selected := view.Select(query)
		if selected.PreparationStatus != PreparedRecallPreparationDenied || len(selected.UserMemories) != 0 || len(selected.Knowledge) != 0 || selected.UserMemoryCandidateCount != 0 || selected.KnowledgeCandidateCount != 0 {
			t.Fatalf("owner/scope mismatch was not denied cleanly: %#v", selected)
		}
		if selected.SourceVersion != "" || selected.Coverage != (PreparedRecallCoverage{}) {
			t.Fatalf("owner/scope mismatch exposed owner snapshot metadata: %#v", selected)
		}
		serialized := fmt.Sprintf("%#v", selected)
		for _, private := range []string{"private-memory-id", "private-location-id", "Private validated brief", "https://example.test/private"} {
			if strings.Contains(serialized, private) {
				t.Fatalf("scope mismatch returned private hint %q: %s", private, serialized)
			}
		}
	}

	allowed := view.Select(PreparedRecallSelectInput{
		AuthenticatedOwnerID: "owner-a", UserScope: "user:owner-a", ExpectedSourceVersion: "rev-a",
		Message: "blue glaze", Persona: "mio", Role: "chat", Now: now,
	})
	serialized := fmt.Sprintf("%#v", allowed)
	for _, private := range []string{"other-user-memory-id", "other-user-location-id", "Other user's private brief", "https://example.test/other-user"} {
		if strings.Contains(serialized, private) {
			t.Fatalf("another user's same-name record leaked: %s", serialized)
		}
	}
}

func TestPreparedRecallViewRejectsForgottenSnapshotVersionAndExpiredOrIneligibleRecords(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	active := domainmemory.UserMemory{
		ID: "remembered", UserID: "owner-a", Statement: "blue glaze preference",
		State: domainmemory.MemoryStateConfirmed, Scope: "all", Sensitivity: "normal", Active: true,
	}
	missingProvenance := preparedRecallCategory(now, "missing-provenance", "blue glaze no provenance", "user:owner-a", time.Time{})
	missingProvenance.ProvenanceURLs = nil
	locationHintOnly := preparedRecallCategory(now, "location-hint-only", "", "user:owner-a", time.Time{})
	view, err := NewPreparedRecallView(PreparedRecallViewInput{
		AuthenticatedOwnerID: "owner-a", UserScope: "user:owner-a", SourceVersion: "before-forget",
		MemoryBudget:         preparedRecallTestBudget(),
		Coverage:             PreparedRecallCoverage{UserMemory: PreparedRecallCoverageFull, Knowledge: PreparedRecallCoverageFull},
		EligibleUserMemories: []domainmemory.UserMemory{active},
		ValidatedCategoryRecords: []CategoryRecallRecord{
			preparedRecallCategory(now, "expired", "blue glaze expired brief", "user:owner-a", now),
			missingProvenance,
			locationHintOnly,
		},
	})
	if err != nil {
		t.Fatalf("NewPreparedRecallView: %v", err)
	}
	current := view.Select(PreparedRecallSelectInput{
		AuthenticatedOwnerID: "owner-a", UserScope: "user:owner-a", ExpectedSourceVersion: "before-forget",
		Message: "blue glaze", Persona: "mio", Role: "chat", Now: now,
	})
	if len(current.Knowledge) != 0 {
		t.Fatalf("expired, provenance-free, or location-only records became brief content: %#v", current.Knowledge)
	}

	stale := view.Select(PreparedRecallSelectInput{
		AuthenticatedOwnerID: "owner-a", UserScope: "user:owner-a", ExpectedSourceVersion: "after-forget",
		Message: "blue glaze", Persona: "mio", Role: "chat", Now: now,
	})
	if stale.PreparationStatus != PreparedRecallPreparationStale || len(stale.UserMemories) != 0 || len(stale.Knowledge) != 0 {
		t.Fatalf("old version survived owner forget/scope revision: %#v", stale)
	}

	forgotten, err := NewPreparedRecallView(PreparedRecallViewInput{
		AuthenticatedOwnerID: "owner-a", UserScope: "user:owner-a", SourceVersion: "after-forget",
		MemoryBudget: preparedRecallTestBudget(),
		Coverage:     PreparedRecallCoverage{UserMemory: PreparedRecallCoverageFull, Knowledge: PreparedRecallCoverageFull},
		EligibleUserMemories: []domainmemory.UserMemory{{
			ID: "remembered", UserID: "owner-a", Statement: active.Statement,
			State: domainmemory.MemoryStateConfirmed, Scope: "all", Sensitivity: "normal", Active: false,
		}},
	})
	if err != nil {
		t.Fatalf("NewPreparedRecallView after forget: %v", err)
	}
	selected := forgotten.Select(PreparedRecallSelectInput{
		AuthenticatedOwnerID: "owner-a", UserScope: "user:owner-a", ExpectedSourceVersion: "after-forget",
		Message: "blue glaze", Persona: "mio", Role: "chat", Now: now,
	})
	if len(selected.UserMemories) != 0 || len(selected.Knowledge) != 0 || selected.MatchStatus != PreparedRecallMatchNoMatch {
		t.Fatalf("forgotten/ineligible records were selected: %#v", selected)
	}
}

func TestPreparedRecallViewBoundsLargeUnrelatedPopulation(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	memories := make([]domainmemory.UserMemory, 2000)
	for i := range memories {
		memories[i] = domainmemory.UserMemory{
			ID: fmt.Sprintf("unrelated-%05d", i), UserID: "owner-a",
			Statement: fmt.Sprintf("unrelated-topic-%05d", i), State: domainmemory.MemoryStateConfirmed,
			Scope: "all", Sensitivity: "normal", Active: true,
		}
	}
	memories = append(memories, domainmemory.UserMemory{
		ID: "target", UserID: "owner-a", Statement: "qxwz preference",
		State: domainmemory.MemoryStateConfirmed, Scope: "all", Sensitivity: "normal", Active: true,
	})
	view, err := NewPreparedRecallView(PreparedRecallViewInput{
		AuthenticatedOwnerID: "owner-a", UserScope: "user:owner-a", SourceVersion: "large-rev",
		MemoryBudget:         preparedRecallTestBudget(),
		Coverage:             PreparedRecallCoverage{UserMemory: PreparedRecallCoverageFull, Knowledge: PreparedRecallCoverageNone},
		EligibleUserMemories: memories,
	})
	if err != nil {
		t.Fatalf("NewPreparedRecallView: %v", err)
	}

	selected := view.Select(PreparedRecallSelectInput{
		AuthenticatedOwnerID: "owner-a", UserScope: "user:owner-a", ExpectedSourceVersion: "large-rev",
		Message: "qxwz", Persona: "mio", Role: "chat", Now: now,
	})
	if len(selected.UserMemories) != 1 || selected.UserMemories[0].Decision.Item.ID != "target" {
		t.Fatalf("large unrelated population changed the match: %#v", selected.UserMemories)
	}
	if selected.PostingVisits >= len(memories) || selected.PostingVisits > PreparedRecallMaxPostingVisits || selected.UserMemoryCandidateCount != 1 {
		t.Fatalf("selection work scaled with unrelated population: visits=%d candidates=%d population=%d", selected.PostingVisits, selected.UserMemoryCandidateCount, len(memories))
	}
}

func TestPreparedRecallViewPreservesPersonaMemoryScope(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	view, err := NewPreparedRecallView(PreparedRecallViewInput{
		AuthenticatedOwnerID: "owner-a", UserScope: "user:owner-a", SourceVersion: "persona-rev",
		MemoryBudget: preparedRecallTestBudget(),
		Coverage:     PreparedRecallCoverage{UserMemory: PreparedRecallCoverageFull, Knowledge: PreparedRecallCoverageNone},
		EligibleUserMemories: []domainmemory.UserMemory{{
			ID: "mio-only-memory", UserID: "owner-a", Statement: "qxwz preference",
			State: domainmemory.MemoryStateConfirmed, Scope: "mio_only", Sensitivity: "normal", Active: true,
		}},
	})
	if err != nil {
		t.Fatalf("NewPreparedRecallView: %v", err)
	}
	selected := view.Select(PreparedRecallSelectInput{
		AuthenticatedOwnerID: "owner-a", UserScope: "user:owner-a", ExpectedSourceVersion: "persona-rev",
		Message: "qxwz", Persona: "shiro", Role: "chat", Now: now,
	})
	if len(selected.UserMemories) != 0 {
		t.Fatalf("persona-scoped memory crossed to another persona: %#v", selected.UserMemories)
	}
}

func TestPreparedRecallViewFailsClosedForUnknownPersona(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	view, err := NewPreparedRecallView(PreparedRecallViewInput{
		AuthenticatedOwnerID: "owner-a", UserScope: "user:owner-a", SourceVersion: "unknown-persona-rev",
		MemoryBudget: preparedRecallTestBudget(),
		Coverage:     PreparedRecallCoverage{UserMemory: PreparedRecallCoverageFull, Knowledge: PreparedRecallCoverageNone},
		EligibleUserMemories: []domainmemory.UserMemory{{
			ID: "mio-only-memory", UserID: "owner-a", Statement: "qxwz preference",
			State: domainmemory.MemoryStateConfirmed, Scope: "mio_only", Sensitivity: "normal", Active: true,
		}},
	})
	if err != nil {
		t.Fatalf("NewPreparedRecallView: %v", err)
	}
	for _, persona := range []string{"", "unknown-agent"} {
		selected := view.Select(PreparedRecallSelectInput{
			AuthenticatedOwnerID: "owner-a", UserScope: "user:owner-a", ExpectedSourceVersion: "unknown-persona-rev",
			Message: "qxwz", Persona: persona, Role: "chat", Now: now,
		})
		if selected.PreparationStatus != PreparedRecallPreparationDenied || len(selected.UserMemories) != 0 {
			t.Fatalf("invalid persona %q did not fail closed: %#v", persona, selected)
		}
	}
}

func TestPreparedRecallViewBoundsLongQueryBeforeIndexAndRanking(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	memories := make([]domainmemory.UserMemory, 100)
	for i := range memories {
		memories[i] = domainmemory.UserMemory{
			ID: fmt.Sprintf("common-%03d", i), UserID: "owner-a", Statement: "qxwz common preference",
			State: domainmemory.MemoryStateConfirmed, Scope: "all", Sensitivity: "normal", Active: true,
		}
	}
	memories = append(memories, domainmemory.UserMemory{
		ID: "tail-only", UserID: "owner-a", Statement: "rendezvous preference",
		State: domainmemory.MemoryStateConfirmed, Scope: "all", Sensitivity: "normal", Active: true,
	})
	view, err := NewPreparedRecallView(PreparedRecallViewInput{
		AuthenticatedOwnerID: "owner-a", UserScope: "user:owner-a", SourceVersion: "long-query-rev",
		MemoryBudget:         preparedRecallTestBudget(),
		Coverage:             PreparedRecallCoverage{UserMemory: PreparedRecallCoverageFull, Knowledge: PreparedRecallCoverageNone},
		EligibleUserMemories: memories,
	})
	if err != nil {
		t.Fatalf("NewPreparedRecallView: %v", err)
	}
	message := strings.Repeat("qxwz ", 20000) + "rendezvous"
	selected := view.Select(PreparedRecallSelectInput{
		AuthenticatedOwnerID: "owner-a", UserScope: "user:owner-a", ExpectedSourceVersion: "long-query-rev",
		Message: message, Persona: "mio", Role: "chat", Now: now,
	})
	if !selected.Truncated.QueryRunes || selected.Truncated.Keys || selected.KeysUsed > PreparedRecallMaxKeys || selected.MatchStatus != PreparedRecallMatchBudgetLimited {
		t.Fatalf("long input was not bounded and reported: keys=%d truncated=%#v", selected.KeysUsed, selected.Truncated)
	}
	for _, memory := range selected.UserMemories {
		if memory.Decision.Item.ID == "tail-only" {
			t.Fatal("ranker used the unbounded tail of the message after key preparation stopped")
		}
	}
}

func TestPreparedRecallViewRequiresExplicitEvaluationTime(t *testing.T) {
	view, err := NewPreparedRecallView(PreparedRecallViewInput{
		AuthenticatedOwnerID: "owner-a", UserScope: "user:owner-a", SourceVersion: "clock-rev",
		MemoryBudget: preparedRecallTestBudget(),
		Coverage:     PreparedRecallCoverage{UserMemory: PreparedRecallCoverageFull, Knowledge: PreparedRecallCoverageFull},
		EligibleUserMemories: []domainmemory.UserMemory{{
			ID: "memory", UserID: "owner-a", Statement: "blue glaze preference",
			State: domainmemory.MemoryStateConfirmed, Scope: "all", Sensitivity: "normal", Active: true,
		}},
	})
	if err != nil {
		t.Fatalf("NewPreparedRecallView: %v", err)
	}
	selected := view.Select(PreparedRecallSelectInput{
		AuthenticatedOwnerID: "owner-a", UserScope: "user:owner-a", ExpectedSourceVersion: "clock-rev",
		Message: "blue glaze", Persona: "mio", Role: "chat",
	})
	if selected.PreparationStatus != PreparedRecallPreparationUnavailable || len(selected.UserMemories) != 0 || len(selected.Knowledge) != 0 {
		t.Fatalf("missing evaluation time did not fail closed: %#v", selected)
	}
}

func TestPreparedRecallViewCapsQueryKeys(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	view, err := NewPreparedRecallView(PreparedRecallViewInput{
		AuthenticatedOwnerID: "owner-a", UserScope: "user:owner-a", SourceVersion: "key-cap-rev",
		MemoryBudget: preparedRecallTestBudget(),
		Coverage:     PreparedRecallCoverage{UserMemory: PreparedRecallCoverageNone, Knowledge: PreparedRecallCoverageNone},
	})
	if err != nil {
		t.Fatalf("NewPreparedRecallView: %v", err)
	}
	query := strings.Join([]string{
		"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india", "juliet",
		"kilo", "lima", "mike", "november", "oscar", "papa", "quebec", "romeo", "sierra", "tango",
		"uniform", "victor", "whiskey", "xray", "yankee", "zulu",
	}, " ")
	selected := view.Select(PreparedRecallSelectInput{
		AuthenticatedOwnerID: "owner-a", UserScope: "user:owner-a", ExpectedSourceVersion: "key-cap-rev",
		Message: query, Persona: "mio", Role: "chat", Now: now,
	})
	if selected.KeysUsed != PreparedRecallMaxKeys || !selected.Truncated.Keys || selected.Truncated.QueryRunes || selected.MatchStatus != PreparedRecallMatchBudgetLimited {
		t.Fatalf("query key cap was not reported: %#v", selected)
	}
}

func TestPreparedRecallViewCopiesSnapshotAndSelectionData(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	memory := domainmemory.UserMemory{
		ID: "copy-memory", UserID: "owner-a", Statement: "blue glaze preference",
		EvidenceEventIDs: []string{"event-original"}, State: domainmemory.MemoryStateConfirmed,
		Scope: "mio_only", Sensitivity: "normal", Active: true,
	}
	record := preparedRecallCategory(now, "copy-record", "Blue glaze is preferred.", "user:owner-a", time.Time{})
	record.ProvenanceURLs = []string{"https://example.test/original"}
	record.Roles = []string{"chat"}
	record.PromptSource = &llm.PromptSourceRef{Owner: "owner-a", SourceID: "source-original"}
	view, err := NewPreparedRecallView(PreparedRecallViewInput{
		AuthenticatedOwnerID: "owner-a", UserScope: "user:owner-a", SourceVersion: "copy-rev",
		MemoryBudget:         preparedRecallTestBudget(),
		Coverage:             PreparedRecallCoverage{UserMemory: PreparedRecallCoverageFull, Knowledge: PreparedRecallCoverageFull},
		EligibleUserMemories: []domainmemory.UserMemory{memory}, ValidatedCategoryRecords: []CategoryRecallRecord{record},
	})
	if err != nil {
		t.Fatalf("NewPreparedRecallView: %v", err)
	}

	// Caller-owned slices and records cannot mutate the immutable snapshot.
	memory.Statement = "changed after construction"
	memory.EvidenceEventIDs[0] = "event-changed"
	record.Summary = "changed after construction"
	record.ProvenanceURLs[0] = "https://example.test/changed"
	record.Roles[0] = "tool"
	record.PromptSource.Owner = "owner-changed"

	input := PreparedRecallSelectInput{
		AuthenticatedOwnerID: "owner-a", UserScope: "user:owner-a", ExpectedSourceVersion: "copy-rev",
		Message: "blue glaze", Persona: "mio", Role: "chat", Now: now,
	}
	first := view.Select(input)
	if len(first.UserMemories) != 1 || first.UserMemories[0].Decision.Item.Statement != "blue glaze preference" || first.UserMemories[0].Decision.Item.EvidenceEventIDs[0] != "event-original" {
		t.Fatalf("input memory mutation changed the snapshot: %#v", first.UserMemories)
	}
	if len(first.Knowledge) != 1 || first.Knowledge[0].Record.Summary != "Blue glaze is preferred." || first.Knowledge[0].Record.ProvenanceURLs[0] != "https://example.test/original" || first.Knowledge[0].Record.Roles[0] != "chat" || first.Knowledge[0].Record.PromptSource.Owner != "owner-a" {
		t.Fatalf("input category mutation changed the snapshot: %#v", first.Knowledge)
	}

	// Returned slices are copies too; mutating one result cannot alter later reads.
	first.UserMemories[0].Decision.Item.EvidenceEventIDs[0] = "output-changed"
	first.Knowledge[0].Record.ProvenanceURLs[0] = "https://example.test/output-changed"
	first.Knowledge[0].Record.PromptSource.Owner = "output-changed"
	second := view.Select(input)
	if second.UserMemories[0].Decision.Item.EvidenceEventIDs[0] != "event-original" || second.Knowledge[0].Record.ProvenanceURLs[0] != "https://example.test/original" || second.Knowledge[0].Record.PromptSource.Owner != "owner-a" {
		t.Fatalf("selection result mutation changed later reads: %#v", second)
	}
}

func TestPreparedRecallViewReportsCoverageAndQueryTruncationSeparately(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	memories := make([]domainmemory.UserMemory, 150)
	records := make([]CategoryRecallRecord, 150)
	for i := 0; i < 150; i++ {
		memories[i] = domainmemory.UserMemory{
			ID: fmt.Sprintf("memory-%03d", i), UserID: "owner-a", Statement: "shared target",
			State: domainmemory.MemoryStateConfirmed, Scope: "all", Sensitivity: "normal", Active: true,
		}
		records[i] = preparedRecallCategory(now, fmt.Sprintf("category-%03d", i), "shared target brief", "public", time.Time{})
	}
	view, err := NewPreparedRecallView(PreparedRecallViewInput{
		AuthenticatedOwnerID: "owner-a", UserScope: "user:owner-a", SourceVersion: "crowded-rev",
		MemoryBudget:         preparedRecallTestBudget(),
		Coverage:             PreparedRecallCoverage{UserMemory: PreparedRecallCoveragePartial, Knowledge: PreparedRecallCoverageFull},
		EligibleUserMemories: memories, ValidatedCategoryRecords: records,
	})
	if err != nil {
		t.Fatalf("NewPreparedRecallView: %v", err)
	}

	selected := view.Select(PreparedRecallSelectInput{
		AuthenticatedOwnerID: "owner-a", UserScope: "user:owner-a", ExpectedSourceVersion: "crowded-rev",
		Message: "shared target", Persona: "mio", Role: "chat", Now: now,
	})
	if selected.PostingVisits != PreparedRecallMaxPostingVisits || selected.UserMemoryCandidateCount != PreparedRecallMaxCandidatesPerSource || selected.KnowledgeCandidateCount != PreparedRecallMaxCandidatesPerSource {
		t.Fatalf("candidate/posting limits not applied: %#v", selected)
	}
	if !selected.Truncated.PostingVisits || !selected.Truncated.UserMemoryCandidates || !selected.Truncated.KnowledgeCandidates || selected.Truncated.Keys || selected.Truncated.QueryRunes {
		t.Fatalf("query truncation dimensions collapsed: %#v", selected.Truncated)
	}
	if selected.Coverage.UserMemory != PreparedRecallCoveragePartial || selected.Coverage.Knowledge != PreparedRecallCoverageFull || selected.MatchStatus != PreparedRecallMatchBudgetLimited {
		t.Fatalf("owner coverage was mixed with query truncation: %#v", selected)
	}
}

func preparedRecallCategory(now time.Time, id, summary, scope string, freshUntil time.Time) CategoryRecallRecord {
	return CategoryRecallRecord{
		Category: "hobby", SourceID: "hobby-catalog", RecordID: id,
		Title: "Blue glaze", Summary: summary,
		ProvenanceURLs: []string{"https://example.test/" + id},
		RetrievedAt:    now.Add(-time.Hour), ValidatedAt: now.Add(-time.Minute),
		FreshUntil: freshUntil, State: CategoryRecordStateValidated,
		Sensitivity: "normal", Scope: scope, Roles: []string{"chat"}, Score: 1,
	}
}

func preparedRecallTestBudget() PreparedRecallMemoryBudget {
	return PreparedRecallMemoryBudget{AggregateLimitBytes: PreparedRecallMaxAggregateBytes}
}
