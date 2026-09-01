package dci

import (
	"math"
	"strings"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func validSearchStep(now time.Time) SearchStep {
	return SearchStep{
		StepNo:      1,
		EventID:     modulecore.NewEventID(),
		EventType:   "dci.file.read",
		Tool:        "file_read",
		ResultCount: 1,
		Status:      "completed",
		CreatedAt:   now,
	}
}

func validSearchTrace(now time.Time) SearchTrace {
	return SearchTrace{
		TraceID:            modulecore.NewTraceID(),
		ActionID:           modulecore.NewActionID(),
		StartedAt:          now,
		EndedAt:            now.Add(time.Second),
		ActorAttribution:   ActorAttributionAuthenticated,
		ActorKind:          "agent",
		ActorID:            "shiro",
		Mode:               "dci",
		UserQuery:          "DCI",
		CorpusScope:        []string{"/allowed"},
		Status:             "completed",
		FinalEvidenceCount: 1,
		Steps:              []SearchStep{validSearchStep(now)},
	}
}

func validEvidence() Evidence {
	return Evidence{
		EvidenceID:       modulecore.NewEvidenceID(),
		CreatedByEventID: modulecore.NewEventID(),
		FilePath:         "spec.md",
		LineStart:        1,
		LineEnd:          1,
		Snippet:          "DCI evidence",
		Confidence:       0.7,
	}
}

func validSearchResult(now time.Time) SearchResult {
	trace := validSearchTrace(now)
	pack := EvidencePack{
		ActionID:    trace.ActionID,
		Query:       trace.UserQuery,
		CorpusScope: []string{"/allowed"},
		Evidence:    []Evidence{validEvidence()},
	}
	return SearchResult{Trace: trace, Pack: pack}
}

func TestValidateSearchTraceRejectsMalformedTrace(t *testing.T) {
	now := time.Date(2026, 5, 20, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*SearchTrace)
		want   string
	}{
		{name: "missing trace_id", mutate: func(trace *SearchTrace) {
			trace.TraceID = ""
		}, want: "trace_id"},
		{name: "missing action_id", mutate: func(trace *SearchTrace) {
			trace.ActionID = ""
		}, want: "action_id"},
		{name: "missing started_at", mutate: func(trace *SearchTrace) {
			trace.StartedAt = time.Time{}
		}, want: "started_at"},
		{name: "terminal missing ended_at", mutate: func(trace *SearchTrace) {
			trace.EndedAt = time.Time{}
		}, want: "ended_at"},
		{name: "ended before started", mutate: func(trace *SearchTrace) {
			trace.EndedAt = trace.StartedAt.Add(-time.Second)
		}, want: "ended_at must be >= started_at"},
		{name: "idempotency surrounding whitespace", mutate: func(trace *SearchTrace) {
			trace.IdempotencyKey = " idem "
		}, want: "idempotency_key must not have surrounding whitespace"},
		{name: "failed missing error", mutate: func(trace *SearchTrace) {
			trace.Status = "failed"
			trace.ErrorMessage = ""
		}, want: "error_message"},
		{name: "negative evidence count", mutate: func(trace *SearchTrace) {
			trace.FinalEvidenceCount = -1
		}, want: "final_evidence_count"},
		{name: "duplicate step", mutate: func(trace *SearchTrace) {
			trace.Steps = append(trace.Steps, trace.Steps[0])
		}, want: "duplicate step_no"},
		{name: "step missing created_at", mutate: func(trace *SearchTrace) {
			trace.Steps[0].CreatedAt = time.Time{}
		}, want: "created_at"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			trace := validSearchTrace(now)
			tt.mutate(&trace)
			err := ValidateSearchTrace(trace)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateSearchTrace() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestValidateSearchTraceAcceptsCompleteTrace(t *testing.T) {
	now := time.Date(2026, 5, 20, 8, 0, 0, 0, time.UTC)
	if err := ValidateSearchTrace(validSearchTrace(now)); err != nil {
		t.Fatalf("ValidateSearchTrace() error = %v", err)
	}
}

func TestValidateSearchTraceActorAttributionStates(t *testing.T) {
	now := time.Date(2026, 5, 20, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		mutate    func(*SearchTrace)
		validate  func(SearchTrace) error
		wantError string
	}{
		{
			name: "authenticated user",
			mutate: func(trace *SearchTrace) {
				trace.ActorKind = "user"
				trace.ActorID = "user-1"
			},
			validate: ValidateSearchTrace,
		},
		{
			name: "authenticated missing actor id",
			mutate: func(trace *SearchTrace) {
				trace.ActorID = ""
			},
			validate:  ValidateSearchTrace,
			wantError: "actor_id",
		},
		{
			name: "authenticated actor kind must be lower-case",
			mutate: func(trace *SearchTrace) {
				trace.ActorKind = "Agent"
			},
			validate:  ValidateSearchTrace,
			wantError: "canonical form",
		},
		{
			name: "authenticated actor kind surrounding whitespace",
			mutate: func(trace *SearchTrace) {
				trace.ActorKind = " agent"
			},
			validate:  ValidateSearchTrace,
			wantError: "canonical form",
		},
		{
			name: "authenticated actor id surrounding whitespace",
			mutate: func(trace *SearchTrace) {
				trace.ActorID = " shiro"
			},
			validate:  ValidateSearchTrace,
			wantError: "actor_id must not have surrounding whitespace",
		},
		{
			name: "legacy runtime rejected",
			mutate: func(trace *SearchTrace) {
				trace.ActorAttribution = ActorAttributionLegacyUnattributed
				trace.ActorKind = ""
				trace.ActorID = ""
			},
			validate:  ValidateSearchTrace,
			wantError: "not allowed for runtime",
		},
		{
			name: "legacy persisted empty actor accepted",
			mutate: func(trace *SearchTrace) {
				trace.ActorAttribution = ActorAttributionLegacyUnattributed
				trace.ActorKind = ""
				trace.ActorID = ""
			},
			validate: ValidateStoredSearchTrace,
		},
		{
			name: "legacy persisted one actor rejected",
			mutate: func(trace *SearchTrace) {
				trace.ActorAttribution = ActorAttributionLegacyUnattributed
				trace.ActorKind = "agent"
				trace.ActorID = ""
			},
			validate:  ValidateStoredSearchTrace,
			wantError: "empty actor_kind and actor_id",
		},
		{
			name: "legacy persisted implementation actor not inferred",
			mutate: func(trace *SearchTrace) {
				trace.ActorAttribution = ActorAttributionLegacyUnattributed
				trace.ActorKind = ""
				trace.ActorID = "Worker"
			},
			validate:  ValidateStoredSearchTrace,
			wantError: "empty actor_kind and actor_id",
		},
		{
			name: "legacy persisted whitespace actor is not empty",
			mutate: func(trace *SearchTrace) {
				trace.ActorAttribution = ActorAttributionLegacyUnattributed
				trace.ActorKind = " "
				trace.ActorID = " "
			},
			validate:  ValidateStoredSearchTrace,
			wantError: "empty actor_kind and actor_id",
		},
		{
			name: "unknown attribution rejected",
			mutate: func(trace *SearchTrace) {
				trace.ActorAttribution = "worker"
			},
			validate:  ValidateStoredSearchTrace,
			wantError: "invalid actor_attribution",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			trace := validSearchTrace(now)
			tt.mutate(&trace)
			err := tt.validate(trace)
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("validation error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("validation error = %v, want %q", err, tt.wantError)
			}
		})
	}
}

func TestValidateSearchStepAcceptsTerminalAndErrorStatuses(t *testing.T) {
	now := time.Date(2026, 5, 20, 8, 0, 0, 0, time.UTC)
	for _, status := range []string{"ok", "completed", "stopped"} {
		step := validSearchStep(now)
		step.Status = status
		if err := ValidateSearchStep(step); err != nil {
			t.Fatalf("ValidateSearchStep(%s) failed: %v", status, err)
		}
	}
	step := validSearchStep(now)
	step.Status = "error"
	step.ErrorMessage = "boom"
	if err := ValidateSearchStep(step); err != nil {
		t.Fatalf("error status with message should validate: %v", err)
	}
}

func TestValidateSearchTraceRequiredFieldsAndStatuses(t *testing.T) {
	now := time.Date(2026, 5, 20, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*SearchTrace)
		want   string
	}{
		{name: "trace id", mutate: func(trace *SearchTrace) { trace.TraceID = "" }, want: "trace_id"},
		{name: "action id", mutate: func(trace *SearchTrace) { trace.ActionID = "" }, want: "action_id"},
		{name: "actor kind", mutate: func(trace *SearchTrace) { trace.ActorKind = "" }, want: "actor_kind"},
		{name: "actor id", mutate: func(trace *SearchTrace) { trace.ActorID = "" }, want: "actor_id"},
		{name: "implementation actor", mutate: func(trace *SearchTrace) { trace.ActorKind = "worker" }, want: "authenticated user or agent"},
		{name: "mode", mutate: func(trace *SearchTrace) { trace.Mode = "" }, want: "mode"},
		{name: "query", mutate: func(trace *SearchTrace) { trace.UserQuery = "" }, want: "user_query"},
		{name: "status", mutate: func(trace *SearchTrace) { trace.Status = "" }, want: "status"},
		{name: "invalid status", mutate: func(trace *SearchTrace) { trace.Status = "running" }, want: "invalid status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			trace := validSearchTrace(now)
			tt.mutate(&trace)
			err := ValidateSearchTrace(trace)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateSearchTrace() error = %v, want %s", err, tt.want)
			}
		})
	}

	stepTests := []struct {
		name   string
		mutate func(*SearchStep)
		want   string
	}{
		{name: "step no", mutate: func(step *SearchStep) { step.StepNo = 0 }, want: "step_no"},
		{name: "step event id", mutate: func(step *SearchStep) { step.EventID = "" }, want: "event_id"},
		{name: "step event type", mutate: func(step *SearchStep) { step.EventType = "" }, want: "event_type"},
		{name: "step invalid event type", mutate: func(step *SearchStep) { step.EventType = "dci.search.completed" }, want: "invalid event_type"},
		{name: "step tool", mutate: func(step *SearchStep) { step.Tool = "" }, want: "tool"},
		{name: "step status", mutate: func(step *SearchStep) { step.Status = "" }, want: "status"},
		{name: "step invalid status", mutate: func(step *SearchStep) { step.Status = "done" }, want: "invalid status"},
		{name: "step error message", mutate: func(step *SearchStep) { step.Status = "error" }, want: "error_message"},
		{name: "step result count", mutate: func(step *SearchStep) { step.ResultCount = -1 }, want: "result_count"},
		{name: "step created at", mutate: func(step *SearchStep) { step.CreatedAt = time.Time{} }, want: "created_at"},
	}
	for _, tt := range stepTests {
		t.Run(tt.name, func(t *testing.T) {
			step := validSearchStep(now)
			tt.mutate(&step)
			err := ValidateSearchStep(step)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateSearchStep() error = %v, want %s", err, tt.want)
			}
		})
	}
}

func TestValidateEvidenceRequiresIndependentIDsAndReverseReference(t *testing.T) {
	evidence := validEvidence()
	if err := ValidateEvidence(evidence); err != nil {
		t.Fatalf("ValidateEvidence() error = %v", err)
	}
	evidence.EvidenceID = modulecore.EvidenceID(evidence.CreatedByEventID)
	if err := ValidateEvidence(evidence); err == nil || !strings.Contains(err.Error(), "evidence_id") {
		t.Fatalf("expected evidence ID prefix validation, got %v", err)
	}
}

func TestValidateSearchResultRejectsInvalidEvidenceProjection(t *testing.T) {
	now := time.Date(2026, 5, 20, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*SearchResult)
		want   string
	}{
		{name: "evidence count", mutate: func(result *SearchResult) {
			result.Trace.FinalEvidenceCount = 0
		}, want: "final_evidence_count"},
		{name: "query mismatch", mutate: func(result *SearchResult) {
			result.Pack.Query = "other query"
		}, want: "query must match"},
		{name: "corpus scope mismatch", mutate: func(result *SearchResult) {
			result.Pack.CorpusScope = []string{"/other"}
		}, want: "corpus_scope must match"},
		{name: "duplicate evidence id", mutate: func(result *SearchResult) {
			result.Pack.Evidence = append(result.Pack.Evidence, result.Pack.Evidence[0])
			result.Trace.FinalEvidenceCount = 2
		}, want: "duplicate evidence_id"},
		{name: "duplicate created event", mutate: func(result *SearchResult) {
			second := validEvidence()
			second.CreatedByEventID = result.Pack.Evidence[0].CreatedByEventID
			result.Pack.Evidence = append(result.Pack.Evidence, second)
			result.Trace.FinalEvidenceCount = 2
		}, want: "duplicate created_by_event_id"},
		{name: "created event is read step", mutate: func(result *SearchResult) {
			result.Pack.Evidence[0].CreatedByEventID = result.Trace.Steps[0].EventID
		}, want: "file-read step"},
		{name: "blank file path", mutate: func(result *SearchResult) {
			result.Pack.Evidence[0].FilePath = " "
		}, want: "file_path"},
		{name: "blank snippet", mutate: func(result *SearchResult) {
			result.Pack.Evidence[0].Snippet = " "
		}, want: "snippet"},
		{name: "non-positive line start", mutate: func(result *SearchResult) {
			result.Pack.Evidence[0].LineStart = 0
		}, want: "line_start"},
		{name: "line range", mutate: func(result *SearchResult) {
			result.Pack.Evidence[0].LineEnd = 0
		}, want: "line_end"},
		{name: "confidence below zero", mutate: func(result *SearchResult) {
			result.Pack.Evidence[0].Confidence = -0.1
		}, want: "confidence"},
		{name: "confidence above one", mutate: func(result *SearchResult) {
			result.Pack.Evidence[0].Confidence = 1.1
		}, want: "confidence"},
		{name: "confidence nan", mutate: func(result *SearchResult) {
			result.Pack.Evidence[0].Confidence = math.NaN()
		}, want: "confidence"},
		{name: "pack confidence below zero", mutate: func(result *SearchResult) {
			result.Pack.Confidence = -0.1
		}, want: "confidence"},
		{name: "pack confidence above one", mutate: func(result *SearchResult) {
			result.Pack.Confidence = 1.1
		}, want: "confidence"},
		{name: "pack confidence nan", mutate: func(result *SearchResult) {
			result.Pack.Confidence = math.NaN()
		}, want: "confidence"},
		{name: "derived term blank", mutate: func(result *SearchResult) {
			result.Pack.DerivedTerms = []string{""}
		}, want: "derived_terms"},
		{name: "derived term surrounding whitespace", mutate: func(result *SearchResult) {
			result.Pack.DerivedTerms = []string{" dci "}
		}, want: "derived_terms"},
		{name: "duplicate derived term", mutate: func(result *SearchResult) {
			result.Pack.DerivedTerms = []string{"dci", "dci"}
		}, want: "duplicate derived term"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := validSearchResult(now)
			tt.mutate(&result)
			err := ValidateSearchResult(result)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateSearchResult() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestValidateEvidencePackAcceptsUnicodeDerivedTerms(t *testing.T) {
	result := validSearchResult(time.Date(2026, 5, 20, 8, 0, 0, 0, time.UTC))
	result.Pack.DerivedTerms = []string{"仕様"}
	result.Pack.Confidence = 0.5
	if err := ValidateEvidencePack(result.Pack); err != nil {
		t.Fatalf("ValidateEvidencePack() rejected Unicode derived term: %v", err)
	}
}

func TestValidateSearchResultAcceptsIndependentEvidence(t *testing.T) {
	now := time.Date(2026, 5, 20, 8, 0, 0, 0, time.UTC)
	if err := ValidateSearchResult(validSearchResult(now)); err != nil {
		t.Fatalf("ValidateSearchResult() error = %v", err)
	}
}
