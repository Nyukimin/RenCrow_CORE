package knowledgememory

import (
	"strings"
	"testing"
	"time"
)

func TestValidatePersonalArchiveRequiresProtectedOriginal(t *testing.T) {
	err := ValidatePersonalArchiveEntry(PersonalArchiveEntry{
		EntryID:      "pa_1",
		UserID:       "ren",
		OriginalText: "bio",
		Protected:    false,
	})
	if err == nil || !strings.Contains(err.Error(), "protected") {
		t.Fatalf("expected protected error, got %v", err)
	}
}

func TestValidateKnowledgeMemoryAcceptsCompleteRecords(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	if err := ValidatePersonalArchiveEntry(PersonalArchiveEntry{
		EntryID:      "pa_1",
		UserID:       "ren",
		OriginalText: "bio",
		Protected:    true,
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("personal archive should validate: %v", err)
	}
	if err := ValidateCreativeKnowledgeItem(CreativeKnowledgeItem{
		ItemID:    "ck_1",
		Title:     "Work",
		Status:    "promoted",
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("creative item should validate: %v", err)
	}
	if err := ValidateNewsKnowledgeItem(NewsKnowledgeItem{
		ItemID:    "news_1",
		Source:    "example",
		Topic:     "tech",
		Status:    "reviewed",
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("news item should validate: %v", err)
	}
	if err := ValidateDailyIntakeRule(DailyIntakeRule{
		RuleID:    "rule_1",
		UserID:    "ren",
		Topic:     "AI",
		Cadence:   "daily",
		Status:    "enabled",
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("daily intake rule should validate: %v", err)
	}
	if err := ValidateTemporalMemoryMarker(TemporalMemoryMarker{
		MarkerID:    "tm_1",
		Layer:       "long_term",
		ReferenceID: "ref_1",
		Summary:     "summary",
		AccessCount: 1,
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("temporal marker should validate: %v", err)
	}
	for _, run := range []DreamConsolidationRun{
		{RunID: "dream_1", Status: "proposal", ReviewStatus: "pending", CreatedAt: now},
		{RunID: "dream_2", Status: "reviewed", ReviewStatus: "approved", CreatedAt: now},
		{RunID: "dream_3", Status: "rejected", ReviewStatus: "rejected", CreatedAt: now},
	} {
		if err := ValidateDreamConsolidationRun(run); err != nil {
			t.Fatalf("dream run should validate: %#v err=%v", run, err)
		}
	}
}

func TestValidateTemporalMemoryMarkerRejectsUnknownLayer(t *testing.T) {
	err := ValidateTemporalMemoryMarker(TemporalMemoryMarker{
		MarkerID:    "tm_1",
		Layer:       "unknown",
		ReferenceID: "ref_1",
		Summary:     "summary",
	})
	if err == nil || !strings.Contains(err.Error(), "layer") {
		t.Fatalf("expected layer error, got %v", err)
	}
}

func TestValidateTemporalMemoryMarkerRejectsNegativeAccessCount(t *testing.T) {
	err := ValidateTemporalMemoryMarker(TemporalMemoryMarker{
		MarkerID:    "tm_1",
		Layer:       "week",
		ReferenceID: "ref_1",
		Summary:     "summary",
		AccessCount: -1,
	})
	if err == nil || !strings.Contains(err.Error(), "access_count") {
		t.Fatalf("expected access_count error, got %v", err)
	}
}

func TestValidateKnowledgeMemoryRejectsMissingCreatedAt(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{
			name: "personal archive",
			err: ValidatePersonalArchiveEntry(PersonalArchiveEntry{
				EntryID:      "pa_1",
				UserID:       "ren",
				OriginalText: "bio",
				Protected:    true,
			}),
		},
		{
			name: "creative",
			err: ValidateCreativeKnowledgeItem(CreativeKnowledgeItem{
				ItemID: "ck_1",
				Title:  "Work",
				Status: "candidate",
			}),
		},
		{
			name: "news",
			err: ValidateNewsKnowledgeItem(NewsKnowledgeItem{
				ItemID: "news_1",
				Source: "example",
				Topic:  "tech",
				Status: "candidate",
			}),
		},
		{
			name: "daily intake",
			err: ValidateDailyIntakeRule(DailyIntakeRule{
				RuleID:  "rule_1",
				UserID:  "ren",
				Topic:   "AI",
				Cadence: "daily",
				Status:  "active",
			}),
		},
		{
			name: "temporal marker",
			err: ValidateTemporalMemoryMarker(TemporalMemoryMarker{
				MarkerID:    "tm_1",
				Layer:       "today",
				ReferenceID: "ref_1",
				Summary:     "summary",
			}),
		},
		{
			name: "dream",
			err: ValidateDreamConsolidationRun(DreamConsolidationRun{
				RunID:        "dream_1",
				Status:       "proposal",
				ReviewStatus: "pending",
			}),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err == nil || !strings.Contains(tt.err.Error(), "created_at") {
				t.Fatalf("validation error = %v, want created_at", tt.err)
			}
		})
	}
}

func TestValidateKnowledgeMemoryRejectsUnknownStatus(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		err  error
	}{
		{
			name: "creative",
			err: ValidateCreativeKnowledgeItem(CreativeKnowledgeItem{
				ItemID:    "ck_1",
				Title:     "Work",
				Status:    "done",
				CreatedAt: now,
			}),
		},
		{
			name: "news",
			err: ValidateNewsKnowledgeItem(NewsKnowledgeItem{
				ItemID:    "news_1",
				Source:    "example",
				Topic:     "tech",
				Status:    "done",
				CreatedAt: now,
			}),
		},
		{
			name: "daily intake",
			err: ValidateDailyIntakeRule(DailyIntakeRule{
				RuleID:    "rule_1",
				UserID:    "ren",
				Topic:     "AI",
				Cadence:   "daily",
				Status:    "done",
				CreatedAt: now,
			}),
		},
		{
			name: "dream status",
			err: ValidateDreamConsolidationRun(DreamConsolidationRun{
				RunID:        "dream_1",
				Status:       "done",
				ReviewStatus: "pending",
				CreatedAt:    now,
			}),
		},
		{
			name: "dream review status",
			err: ValidateDreamConsolidationRun(DreamConsolidationRun{
				RunID:        "dream_1",
				Status:       "proposal",
				ReviewStatus: "done",
				CreatedAt:    now,
			}),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err == nil || !strings.Contains(tt.err.Error(), "unsupported") {
				t.Fatalf("validation error = %v, want unsupported", tt.err)
			}
		})
	}
}

func TestValidateDreamConsolidationRejectsAutoApprove(t *testing.T) {
	err := ValidateDreamConsolidationRun(DreamConsolidationRun{
		RunID:        "dream_1",
		Status:       "draft",
		ReviewStatus: "approved",
		CreatedAt:    time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
	})
	if err == nil || !strings.Contains(err.Error(), "auto-approved") {
		t.Fatalf("expected auto-approved error, got %v", err)
	}
}

func TestValidateDreamConsolidationRejectsInconsistentReviewState(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		run  DreamConsolidationRun
		want string
	}{
		{
			name: "pending promoted",
			run:  DreamConsolidationRun{RunID: "dream_1", Status: "promoted", ReviewStatus: "pending", CreatedAt: now},
			want: "pending review",
		},
		{
			name: "rejected reviewed",
			run:  DreamConsolidationRun{RunID: "dream_1", Status: "reviewed", ReviewStatus: "rejected", CreatedAt: now},
			want: "rejected review",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateDreamConsolidationRun(tt.run)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateDreamConsolidationRun() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestValidateKnowledgeMemoryRequiredFields(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "archive entry id", err: ValidatePersonalArchiveEntry(PersonalArchiveEntry{UserID: "ren", OriginalText: "bio", Protected: true, CreatedAt: now}), want: "entry_id"},
		{name: "archive user", err: ValidatePersonalArchiveEntry(PersonalArchiveEntry{EntryID: "pa_1", OriginalText: "bio", Protected: true, CreatedAt: now}), want: "user_id"},
		{name: "archive text", err: ValidatePersonalArchiveEntry(PersonalArchiveEntry{EntryID: "pa_1", UserID: "ren", Protected: true, CreatedAt: now}), want: "original_text"},
		{name: "creative id", err: ValidateCreativeKnowledgeItem(CreativeKnowledgeItem{Title: "Work", Status: "candidate", CreatedAt: now}), want: "item_id"},
		{name: "creative title", err: ValidateCreativeKnowledgeItem(CreativeKnowledgeItem{ItemID: "ck_1", Status: "candidate", CreatedAt: now}), want: "title"},
		{name: "news source", err: ValidateNewsKnowledgeItem(NewsKnowledgeItem{ItemID: "news_1", Topic: "tech", Status: "candidate", CreatedAt: now}), want: "source"},
		{name: "news topic", err: ValidateNewsKnowledgeItem(NewsKnowledgeItem{ItemID: "news_1", Source: "example", Status: "candidate", CreatedAt: now}), want: "topic"},
		{name: "daily user", err: ValidateDailyIntakeRule(DailyIntakeRule{RuleID: "rule_1", Topic: "AI", Cadence: "daily", Status: "active", CreatedAt: now}), want: "user_id"},
		{name: "daily cadence", err: ValidateDailyIntakeRule(DailyIntakeRule{RuleID: "rule_1", UserID: "ren", Topic: "AI", Status: "active", CreatedAt: now}), want: "cadence"},
		{name: "marker id", err: ValidateTemporalMemoryMarker(TemporalMemoryMarker{Layer: "today", ReferenceID: "ref_1", Summary: "summary", CreatedAt: now}), want: "marker_id"},
		{name: "marker reference", err: ValidateTemporalMemoryMarker(TemporalMemoryMarker{MarkerID: "tm_1", Layer: "today", Summary: "summary", CreatedAt: now}), want: "reference_id"},
		{name: "marker summary", err: ValidateTemporalMemoryMarker(TemporalMemoryMarker{MarkerID: "tm_1", Layer: "today", ReferenceID: "ref_1", CreatedAt: now}), want: "summary"},
		{name: "dream run id", err: ValidateDreamConsolidationRun(DreamConsolidationRun{Status: "proposal", ReviewStatus: "pending", CreatedAt: now}), want: "run_id"},
		{name: "dream status", err: ValidateDreamConsolidationRun(DreamConsolidationRun{RunID: "dream_1", ReviewStatus: "pending", CreatedAt: now}), want: "status"},
		{name: "dream review", err: ValidateDreamConsolidationRun(DreamConsolidationRun{RunID: "dream_1", Status: "proposal", CreatedAt: now}), want: "review_status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err == nil || !strings.Contains(tt.err.Error(), tt.want) {
				t.Fatalf("err=%v, want %s", tt.err, tt.want)
			}
		})
	}
}
