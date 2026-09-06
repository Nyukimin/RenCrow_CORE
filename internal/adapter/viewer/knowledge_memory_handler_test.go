package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	domainkm "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type stubKnowledgeMemoryStore struct {
	personal []domainkm.PersonalArchiveEntry
	creative []domainkm.CreativeKnowledgeItem
	news     []domainkm.NewsKnowledgeItem
	intake   []domainkm.DailyIntakeRule
	temporal []domainkm.TemporalMemoryMarker
	dream    []domainkm.DreamConsolidationRun
}

func (s *stubKnowledgeMemoryStore) ListPersonalArchiveEntries(_ context.Context, _ int) ([]domainkm.PersonalArchiveEntry, error) {
	return s.personal, nil
}

func (s *stubKnowledgeMemoryStore) ListCreativeKnowledgeItems(_ context.Context, _ int) ([]domainkm.CreativeKnowledgeItem, error) {
	return s.creative, nil
}

func (s *stubKnowledgeMemoryStore) ListNewsKnowledgeItems(_ context.Context, _ int) ([]domainkm.NewsKnowledgeItem, error) {
	return s.news, nil
}

func (s *stubKnowledgeMemoryStore) ListDailyIntakeRules(_ context.Context, _ int) ([]domainkm.DailyIntakeRule, error) {
	return s.intake, nil
}

func (s *stubKnowledgeMemoryStore) ListTemporalMemoryMarkers(_ context.Context, _ int) ([]domainkm.TemporalMemoryMarker, error) {
	return s.temporal, nil
}

func (s *stubKnowledgeMemoryStore) ListDreamConsolidationRuns(_ context.Context, _ int) ([]domainkm.DreamConsolidationRun, error) {
	return s.dream, nil
}

func (s *stubKnowledgeMemoryStore) SavePersonalArchiveEntry(_ context.Context, item domainkm.PersonalArchiveEntry) error {
	if err := domainkm.ValidatePersonalArchiveEntry(item); err != nil {
		return err
	}
	s.personal = append(s.personal, item)
	return nil
}

func (s *stubKnowledgeMemoryStore) SaveCreativeKnowledgeItem(_ context.Context, item domainkm.CreativeKnowledgeItem) error {
	if err := domainkm.ValidateCreativeKnowledgeItem(item); err != nil {
		return err
	}
	s.creative = append(s.creative, item)
	return nil
}

func (s *stubKnowledgeMemoryStore) SaveNewsKnowledgeItem(_ context.Context, item domainkm.NewsKnowledgeItem) error {
	if err := domainkm.ValidateNewsKnowledgeItem(item); err != nil {
		return err
	}
	s.news = append(s.news, item)
	return nil
}

func (s *stubKnowledgeMemoryStore) SaveDailyIntakeRule(_ context.Context, item domainkm.DailyIntakeRule) error {
	if err := domainkm.ValidateDailyIntakeRule(item); err != nil {
		return err
	}
	s.intake = append(s.intake, item)
	return nil
}

func (s *stubKnowledgeMemoryStore) SaveTemporalMemoryMarker(_ context.Context, item domainkm.TemporalMemoryMarker) error {
	if err := domainkm.ValidateTemporalMemoryMarker(item); err != nil {
		return err
	}
	s.temporal = append(s.temporal, item)
	return nil
}

func (s *stubKnowledgeMemoryStore) SaveDreamConsolidationRun(_ context.Context, item domainkm.DreamConsolidationRun) error {
	if err := domainkm.ValidateDreamConsolidationRun(item); err != nil {
		return err
	}
	s.dream = append(s.dream, item)
	return nil
}

func TestKnowledgeMemoryCreateHandlers(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		body      string
		handler   func(KnowledgeMemoryStore) http.HandlerFunc
		assertion func(*testing.T, *stubKnowledgeMemoryStore)
	}{
		{
			name:    "personal archive",
			path:    "/viewer/knowledge-memory/personal-archive",
			handler: HandlePersonalArchiveCreate,
			body: `{
				"entry_id":"pa_1",
				"user_id":"ren",
				"original_text":"protected original",
				"protected":true
			}`,
			assertion: func(t *testing.T, store *stubKnowledgeMemoryStore) {
				t.Helper()
				if len(store.personal) != 1 || store.personal[0].EntryID != "pa_1" {
					t.Fatalf("personal=%#v", store.personal)
				}
				if store.personal[0].CreatedAt.IsZero() {
					t.Fatalf("personal created_at was not assigned: %#v", store.personal[0])
				}
			},
		},
		{
			name:    "creative knowledge",
			path:    "/viewer/knowledge-memory/creative-knowledge",
			handler: HandleCreativeKnowledgeCreate,
			body: `{
				"item_id":"ck_1",
				"title":"作品A",
				"creator_names":["作者A"],
				"status":"candidate"
			}`,
			assertion: func(t *testing.T, store *stubKnowledgeMemoryStore) {
				t.Helper()
				if len(store.creative) != 1 || store.creative[0].Title != "作品A" {
					t.Fatalf("creative=%#v", store.creative)
				}
				if store.creative[0].CreatedAt.IsZero() {
					t.Fatalf("creative created_at was not assigned: %#v", store.creative[0])
				}
			},
		},
		{
			name:    "news knowledge",
			path:    "/viewer/knowledge-memory/news-knowledge",
			handler: HandleNewsKnowledgeCreate,
			body: `{
				"item_id":"news_1",
				"source":"example",
				"topic":"tech",
				"status":"candidate"
			}`,
			assertion: func(t *testing.T, store *stubKnowledgeMemoryStore) {
				t.Helper()
				if len(store.news) != 1 || store.news[0].Topic != "tech" {
					t.Fatalf("news=%#v", store.news)
				}
				if store.news[0].CreatedAt.IsZero() {
					t.Fatalf("news created_at was not assigned: %#v", store.news[0])
				}
			},
		},
		{
			name:    "daily intake rule",
			path:    "/viewer/knowledge-memory/daily-intake-rules",
			handler: HandleDailyIntakeRuleCreate,
			body: `{
				"rule_id":"rule_1",
				"user_id":"ren",
				"topic":"AI",
				"cadence":"daily",
				"status":"candidate"
			}`,
			assertion: func(t *testing.T, store *stubKnowledgeMemoryStore) {
				t.Helper()
				if len(store.intake) != 1 || store.intake[0].RuleID != "rule_1" {
					t.Fatalf("intake=%#v", store.intake)
				}
				if store.intake[0].CreatedAt.IsZero() {
					t.Fatalf("intake created_at was not assigned: %#v", store.intake[0])
				}
			},
		},
		{
			name:    "temporal marker",
			path:    "/viewer/knowledge-memory/temporal-markers",
			handler: HandleTemporalMemoryMarkerCreate,
			body: `{
				"marker_id":"tm_1",
				"layer":"week",
				"reference_id":"entry_1",
				"summary":"一週間記憶候補"
			}`,
			assertion: func(t *testing.T, store *stubKnowledgeMemoryStore) {
				t.Helper()
				if len(store.temporal) != 1 || store.temporal[0].Layer != "week" {
					t.Fatalf("temporal=%#v", store.temporal)
				}
				if store.temporal[0].CreatedAt.IsZero() {
					t.Fatalf("temporal created_at was not assigned: %#v", store.temporal[0])
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &stubKnowledgeMemoryStore{}
			req := httptest.NewRequest(http.MethodPost, tt.path, bytes.NewBufferString(tt.body))
			rec := httptest.NewRecorder()

			tt.handler(store).ServeHTTP(rec, req)

			if rec.Code != http.StatusCreated {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			tt.assertion(t, store)
		})
	}
}

func TestDreamConsolidationCreateStoresTaskOwnedIdentity(t *testing.T) {
	store := &stubKnowledgeMemoryStore{}
	taskID := modulecore.NewTaskID()
	runID := modulecore.NewRunID()
	req := httptest.NewRequest(http.MethodPost, "/viewer/knowledge-memory/dream-runs", bytes.NewBufferString(dreamRunCreateBody(taskID, runID, "mio", "draft", "pending")))
	rec := httptest.NewRecorder()

	HandleDreamConsolidationRunCreate(store, stubBrowserTraceRunVerifier{assignee: "mio"}).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.dream) != 1 {
		t.Fatalf("dream=%#v", store.dream)
	}
	saved := store.dream[0]
	if saved.TaskID != taskID || saved.RunID != runID || saved.ActorID != "mio" {
		t.Fatalf("dream identity=%#v", saved)
	}
	if saved.ReviewStatus != "pending" || saved.CreatedAt.IsZero() {
		t.Fatalf("dream=%#v", saved)
	}
}

func TestDreamConsolidationCreateRejectsAdoptedReviewStatus(t *testing.T) {
	store := &stubKnowledgeMemoryStore{}
	req := httptest.NewRequest(http.MethodPost, "/viewer/knowledge-memory/dream-runs", bytes.NewBufferString(dreamRunCreateBody(modulecore.NewTaskID(), modulecore.NewRunID(), "mio", "draft", "adopted")))
	rec := httptest.NewRecorder()

	HandleDreamConsolidationRunCreate(store, stubBrowserTraceRunVerifier{assignee: "mio"}).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.dream) != 0 {
		t.Fatalf("dream=%#v", store.dream)
	}
}

func TestDreamConsolidationCreateRequiresVerifiedTaskOwnedRun(t *testing.T) {
	taskID := modulecore.NewTaskID()
	runID := modulecore.NewRunID()
	tests := []struct {
		name     string
		body     string
		verifier BrowserTraceRunVerifier
		wantCode int
	}{
		{
			name:     "legacy minted run id",
			body:     `{"task_id":"` + string(taskID) + `","run_id":"dream_1","actor_id":"mio","status":"draft","review_status":"pending"}`,
			verifier: stubBrowserTraceRunVerifier{assignee: "mio"},
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "missing task id",
			body:     `{"run_id":"` + string(runID) + `","actor_id":"mio","status":"draft","review_status":"pending"}`,
			verifier: stubBrowserTraceRunVerifier{assignee: "mio"},
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "actor is not a core agent",
			body:     dreamRunCreateBody(taskID, runID, "ren", "draft", "pending"),
			verifier: stubBrowserTraceRunVerifier{},
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "task or run missing",
			body:     dreamRunCreateBody(taskID, runID, "mio", "draft", "pending"),
			verifier: stubBrowserTraceRunVerifier{err: fmt.Errorf("not found")},
			wantCode: http.StatusForbidden,
		},
		{
			name:     "actor differs from assignee",
			body:     dreamRunCreateBody(taskID, runID, "mio", "draft", "pending"),
			verifier: stubBrowserTraceRunVerifier{assignee: "shiro"},
			wantCode: http.StatusForbidden,
		},
		{
			name:     "verifier unavailable",
			body:     dreamRunCreateBody(taskID, runID, "mio", "draft", "pending"),
			verifier: nil,
			wantCode: http.StatusServiceUnavailable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &stubKnowledgeMemoryStore{}
			req := httptest.NewRequest(http.MethodPost, "/viewer/knowledge-memory/dream-runs", bytes.NewBufferString(tt.body))
			rec := httptest.NewRecorder()

			HandleDreamConsolidationRunCreate(store, tt.verifier).ServeHTTP(rec, req)

			if rec.Code != tt.wantCode {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tt.wantCode, rec.Body.String())
			}
			if len(store.dream) != 0 {
				t.Fatalf("dream=%#v", store.dream)
			}
		})
	}
}

func dreamRunCreateBody(taskID modulecore.TaskID, runID modulecore.RunID, actorID string, status string, reviewStatus string) string {
	return fmt.Sprintf(`{
		"task_id":%q,
		"run_id":%q,
		"actor_id":%q,
		"scope":["knowledge"],
		"status":%q,
		"review_status":%q
	}`, string(taskID), string(runID), actorID, status, reviewStatus)
}

func TestDreamConsolidationProposalCreateBuildsPendingProposal(t *testing.T) {
	store := &stubKnowledgeMemoryStore{
		personal: []domainkm.PersonalArchiveEntry{{
			EntryID:      "pa_1",
			UserID:       "ren",
			OriginalText: "protected original",
			Protected:    true,
		}},
	}
	taskID := modulecore.NewTaskID()
	runID := modulecore.NewRunID()
	req := httptest.NewRequest(http.MethodPost, "/viewer/knowledge-memory/dream-runs/propose", bytes.NewBufferString(fmt.Sprintf(`{
		"task_id":%q,
		"run_id":%q,
		"actor_id":"mio",
		"scope":["personal_archive"],
		"limit":5,
		"now":"2026-05-18T12:00:00Z"
	}`, string(taskID), string(runID))))
	rec := httptest.NewRecorder()

	HandleDreamConsolidationProposalCreate(store, stubBrowserTraceRunVerifier{assignee: "mio"}).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.dream) != 1 || store.dream[0].Status != "proposal" || store.dream[0].ReviewStatus != "pending" {
		t.Fatalf("dream=%#v", store.dream)
	}
	if store.dream[0].TaskID != taskID || store.dream[0].RunID != runID || store.dream[0].ActorID != "mio" {
		t.Fatalf("dream identity=%#v", store.dream[0])
	}
	if len(store.dream[0].IdeaSeeds) != 1 || store.dream[0].IdeaSeeds[0] == "" {
		t.Fatalf("idea seeds=%#v", store.dream[0].IdeaSeeds)
	}
}

func TestDreamConsolidationProposalCreateRequiresVerifiedTaskOwnedRun(t *testing.T) {
	taskID := modulecore.NewTaskID()
	runID := modulecore.NewRunID()
	tests := []struct {
		name     string
		body     string
		verifier BrowserTraceRunVerifier
		wantCode int
	}{
		{
			name:     "identity missing",
			body:     `{"scope":["personal_archive"]}`,
			verifier: stubBrowserTraceRunVerifier{assignee: "mio"},
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "task or run missing",
			body:     fmt.Sprintf(`{"task_id":%q,"run_id":%q,"actor_id":"mio"}`, string(taskID), string(runID)),
			verifier: stubBrowserTraceRunVerifier{err: fmt.Errorf("not found")},
			wantCode: http.StatusForbidden,
		},
		{
			name:     "actor differs from assignee",
			body:     fmt.Sprintf(`{"task_id":%q,"run_id":%q,"actor_id":"mio"}`, string(taskID), string(runID)),
			verifier: stubBrowserTraceRunVerifier{assignee: "shiro"},
			wantCode: http.StatusForbidden,
		},
		{
			name:     "verifier unavailable",
			body:     fmt.Sprintf(`{"task_id":%q,"run_id":%q,"actor_id":"mio"}`, string(taskID), string(runID)),
			verifier: nil,
			wantCode: http.StatusServiceUnavailable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &stubKnowledgeMemoryStore{}
			req := httptest.NewRequest(http.MethodPost, "/viewer/knowledge-memory/dream-runs/propose", bytes.NewBufferString(tt.body))
			rec := httptest.NewRecorder()

			HandleDreamConsolidationProposalCreate(store, tt.verifier).ServeHTTP(rec, req)

			if rec.Code != tt.wantCode {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tt.wantCode, rec.Body.String())
			}
			if len(store.dream) != 0 {
				t.Fatalf("dream=%#v", store.dream)
			}
		})
	}
}

func TestDreamConsolidationReviewApprovesWithoutAutoPromote(t *testing.T) {
	now := fixedViewerKnowledgeMemoryTime()
	taskID := modulecore.NewTaskID()
	runID := modulecore.NewRunID()
	store := &stubKnowledgeMemoryStore{
		dream: []domainkm.DreamConsolidationRun{{
			TaskID:       taskID,
			RunID:        runID,
			ActorID:      "mio",
			Scope:        []string{"personal_archive"},
			IdeaSeeds:    []string{"seed"},
			Status:       "proposal",
			ReviewStatus: "pending",
			CreatedAt:    now,
		}},
	}
	req := httptest.NewRequest(http.MethodPost, "/viewer/knowledge-memory/dream-runs/review", bytes.NewBufferString(fmt.Sprintf(`{
		"run_id":%q,
		"review_status":"adopted"
	}`, string(runID))))
	rec := httptest.NewRecorder()

	HandleDreamConsolidationReview(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.dream) != 2 {
		t.Fatalf("dream=%#v", store.dream)
	}
	reviewed := store.dream[1]
	if reviewed.RunID != runID || reviewed.TaskID != taskID || reviewed.ActorID != "mio" {
		t.Fatalf("reviewed dream identity=%#v", reviewed)
	}
	if reviewed.Status != "reviewed" || reviewed.ReviewStatus != "adopted" {
		t.Fatalf("reviewed dream=%#v", reviewed)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if body["promoted"] != false || body["auto_promote"] != false {
		t.Fatalf("body=%#v", body)
	}
}

func TestDreamConsolidationReviewPromotesOnlyAdopted(t *testing.T) {
	now := fixedViewerKnowledgeMemoryTime()
	runID := modulecore.NewRunID()
	store := &stubKnowledgeMemoryStore{
		dream: []domainkm.DreamConsolidationRun{{
			TaskID:       modulecore.NewTaskID(),
			RunID:        runID,
			ActorID:      "mio",
			IdeaSeeds:    []string{"seed"},
			Status:       "proposal",
			ReviewStatus: "pending",
			CreatedAt:    now,
		}},
	}
	req := httptest.NewRequest(http.MethodPost, "/viewer/knowledge-memory/dream-runs/review", bytes.NewBufferString(fmt.Sprintf(`{
		"run_id":%q,
		"review_status":"adopted",
		"promote":true
	}`, string(runID))))
	rec := httptest.NewRecorder()

	HandleDreamConsolidationReview(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	promoted := store.dream[len(store.dream)-1]
	if promoted.Status != "promoted" || promoted.ReviewStatus != "adopted" {
		t.Fatalf("promoted dream=%#v", promoted)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if body["promoted"] != true || body["auto_promote"] != false {
		t.Fatalf("body=%#v", body)
	}
}

func TestDreamConsolidationReviewRejectsPromoteWithoutAdoption(t *testing.T) {
	runID := modulecore.NewRunID()
	store := &stubKnowledgeMemoryStore{
		dream: []domainkm.DreamConsolidationRun{{
			TaskID:       modulecore.NewTaskID(),
			RunID:        runID,
			ActorID:      "mio",
			Status:       "proposal",
			ReviewStatus: "pending",
		}},
	}
	req := httptest.NewRequest(http.MethodPost, "/viewer/knowledge-memory/dream-runs/review", bytes.NewBufferString(fmt.Sprintf(`{
		"run_id":%q,
		"review_status":"rejected",
		"promote":true
	}`, string(runID))))
	rec := httptest.NewRecorder()

	HandleDreamConsolidationReview(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.dream) != 1 {
		t.Fatalf("dream=%#v", store.dream)
	}
}

func TestKnowledgeMemoryReviewPromotesNewsWithComparison(t *testing.T) {
	now := fixedViewerKnowledgeMemoryTime()
	store := &stubKnowledgeMemoryStore{
		news: []domainkm.NewsKnowledgeItem{{
			ItemID:    "news_1",
			Source:    "example",
			Topic:     "tech",
			Status:    "candidate",
			CreatedAt: now,
		}},
	}
	req := httptest.NewRequest(http.MethodPost, "/viewer/knowledge-memory/review", bytes.NewBufferString(`{
		"detail_type":"news_knowledge",
		"id":"news_1",
		"review_status":"adopted",
		"promote":true,
		"reviewed_by":"viewer"
	}`))
	rec := httptest.NewRecorder()

	HandleKnowledgeMemoryReview(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.news) != 2 || store.news[1].Status != "promoted" {
		t.Fatalf("news=%#v", store.news)
	}
	var body struct {
		Status       string `json:"status"`
		Promoted     bool   `json:"promoted"`
		AutoPromote  bool   `json:"auto_promote"`
		ReviewStatus string `json:"review_status"`
		Comparison   struct {
			CurrentStatus string                     `json:"current_status"`
			TargetStatus  string                     `json:"target_status"`
			FormalTarget  string                     `json:"formal_target"`
			TargetItem    domainkm.NewsKnowledgeItem `json:"target_item"`
		} `json:"comparison"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if body.Status != "reviewed" || !body.Promoted || body.AutoPromote || body.ReviewStatus != "adopted" {
		t.Fatalf("body=%#v", body)
	}
	if body.Comparison.CurrentStatus != "candidate" || body.Comparison.TargetStatus != "promoted" || body.Comparison.TargetItem.Status != "promoted" {
		t.Fatalf("comparison=%#v", body.Comparison)
	}
	if body.Comparison.FormalTarget != "knowledge_memory.news_knowledge:promoted" {
		t.Fatalf("formal target=%q", body.Comparison.FormalTarget)
	}
}

func TestKnowledgeMemoryReviewEnablesDailyIntakeRuleAfterAdoption(t *testing.T) {
	now := fixedViewerKnowledgeMemoryTime()
	store := &stubKnowledgeMemoryStore{
		intake: []domainkm.DailyIntakeRule{{
			RuleID:    "rule_1",
			UserID:    "ren",
			Topic:     "AI news",
			Cadence:   "daily",
			Status:    "candidate",
			CreatedAt: now,
		}},
	}
	req := httptest.NewRequest(http.MethodPost, "/viewer/knowledge-memory/review", bytes.NewBufferString(`{
		"detail_type":"daily_intake_rule",
		"id":"rule_1",
		"review_status":"adopted",
		"promote":true
	}`))
	rec := httptest.NewRecorder()

	HandleKnowledgeMemoryReview(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.intake) != 2 || store.intake[1].Status != "enabled" {
		t.Fatalf("intake=%#v", store.intake)
	}
	var body struct {
		Comparison struct {
			TargetStatus string `json:"target_status"`
			FormalTarget string `json:"formal_target"`
		} `json:"comparison"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if body.Comparison.TargetStatus != "enabled" || body.Comparison.FormalTarget != "source_registry.daily_intake_rule:enabled" {
		t.Fatalf("comparison=%#v", body.Comparison)
	}
}

func fixedViewerKnowledgeMemoryTime() time.Time {
	return time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
}

func TestKnowledgeMemoryReviewRejectsPromoteWithoutAdoption(t *testing.T) {
	store := &stubKnowledgeMemoryStore{
		creative: []domainkm.CreativeKnowledgeItem{{
			ItemID: "ck_1",
			Title:  "作品A",
			Status: "candidate",
		}},
	}
	req := httptest.NewRequest(http.MethodPost, "/viewer/knowledge-memory/review", bytes.NewBufferString(`{
		"detail_type":"creative_knowledge",
		"id":"ck_1",
		"review_status":"rejected",
		"promote":true
	}`))
	rec := httptest.NewRecorder()

	HandleKnowledgeMemoryReview(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.creative) != 1 {
		t.Fatalf("creative=%#v", store.creative)
	}
}

func TestKnowledgeMemoryStatusReturnsDetailByTypeAndID(t *testing.T) {
	store := &stubKnowledgeMemoryStore{
		personal: []domainkm.PersonalArchiveEntry{{
			EntryID:      "pa_1",
			UserID:       "ren",
			OriginalText: "protected original bio",
			Protected:    true,
		}},
		dream: []domainkm.DreamConsolidationRun{{
			TaskID:       modulecore.NewTaskID(),
			RunID:        modulecore.NewRunID(),
			ActorID:      "mio",
			Status:       "draft",
			ReviewStatus: "pending",
		}},
	}
	req := httptest.NewRequest(http.MethodGet, "/viewer/knowledge-memory?detail_type=personal_archive&id=pa_1", nil)
	rec := httptest.NewRecorder()

	HandleKnowledgeMemoryStatus(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		DetailType string `json:"detail_type"`
		ID         string `json:"id"`
		Item       struct {
			EntryID      string `json:"entry_id"`
			OriginalText string `json:"original_text"`
			Protected    bool   `json:"protected"`
		} `json:"item"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if out.DetailType != "personal_archive" || out.ID != "pa_1" || !out.Item.Protected || out.Item.OriginalText == "" {
		t.Fatalf("unexpected detail: %+v", out)
	}
}
