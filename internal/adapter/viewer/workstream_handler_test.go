package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
)

type stubWorkstreamLister struct {
	workstreams  []domainworkstream.Workstream
	goals        []domainworkstream.Goal
	artifacts    []domainworkstream.Artifact
	annotations  []domainworkstream.ArtifactAnnotation
	steering     []domainworkstream.SteeringItem
	heartbeats   []domainworkstream.HeartbeatSchedule
	vaultUpdates []domainworkstream.VaultUpdateLog
	saved        []domainworkstream.Workstream
	savedGoals   []domainworkstream.Goal
	applied      []domainworkstream.VaultUpdateLog
	appliedPath  string
	preview      *domainworkstream.VaultUpdatePreview
	limit        int
}

func (s *stubWorkstreamLister) ListWorkstreams(_ context.Context, limit int) ([]domainworkstream.Workstream, error) {
	s.limit = limit
	return s.workstreams, nil
}

func (s *stubWorkstreamLister) ListGoals(_ context.Context, limit int) ([]domainworkstream.Goal, error) {
	s.limit = limit
	return s.goals, nil
}

func (s *stubWorkstreamLister) ListArtifacts(_ context.Context, limit int) ([]domainworkstream.Artifact, error) {
	s.limit = limit
	return s.artifacts, nil
}

func (s *stubWorkstreamLister) ListArtifactAnnotations(_ context.Context, limit int) ([]domainworkstream.ArtifactAnnotation, error) {
	s.limit = limit
	return s.annotations, nil
}

func (s *stubWorkstreamLister) ListSteeringItems(_ context.Context, limit int) ([]domainworkstream.SteeringItem, error) {
	s.limit = limit
	return s.steering, nil
}

func (s *stubWorkstreamLister) ListHeartbeatSchedules(_ context.Context, limit int) ([]domainworkstream.HeartbeatSchedule, error) {
	s.limit = limit
	return s.heartbeats, nil
}

func (s *stubWorkstreamLister) ListVaultUpdateLogs(_ context.Context, limit int) ([]domainworkstream.VaultUpdateLog, error) {
	s.limit = limit
	return s.vaultUpdates, nil
}

func (s *stubWorkstreamLister) SaveWorkstream(_ context.Context, item domainworkstream.Workstream) error {
	if err := domainworkstream.ValidateWorkstream(item); err != nil {
		return err
	}
	s.saved = append(s.saved, item)
	return nil
}

func (s *stubWorkstreamLister) SaveGoal(_ context.Context, item domainworkstream.Goal) error {
	if err := domainworkstream.ValidateGoal(item); err != nil {
		return err
	}
	s.savedGoals = append(s.savedGoals, item)
	return nil
}

func (s *stubWorkstreamLister) SaveArtifact(_ context.Context, item domainworkstream.Artifact) error {
	if err := domainworkstream.ValidateArtifact(item); err != nil {
		return err
	}
	s.artifacts = append(s.artifacts, item)
	return nil
}

func (s *stubWorkstreamLister) SaveArtifactAnnotation(_ context.Context, item domainworkstream.ArtifactAnnotation) error {
	if err := domainworkstream.ValidateArtifactAnnotation(item); err != nil {
		return err
	}
	s.annotations = append(s.annotations, item)
	return nil
}

func (s *stubWorkstreamLister) SaveSteeringItem(_ context.Context, item domainworkstream.SteeringItem) error {
	if err := domainworkstream.ValidateSteeringItem(item); err != nil {
		return err
	}
	s.steering = append(s.steering, item)
	return nil
}

func (s *stubWorkstreamLister) SaveHeartbeatSchedule(_ context.Context, item domainworkstream.HeartbeatSchedule) error {
	if err := domainworkstream.ValidateHeartbeatSchedule(item); err != nil {
		return err
	}
	s.heartbeats = append(s.heartbeats, item)
	return nil
}

func (s *stubWorkstreamLister) SaveVaultUpdateLog(_ context.Context, item domainworkstream.VaultUpdateLog) error {
	if err := domainworkstream.ValidateVaultUpdateLog(item); err != nil {
		return err
	}
	s.vaultUpdates = append(s.vaultUpdates, item)
	return nil
}

func (s *stubWorkstreamLister) ApplyVaultUpdate(_ context.Context, item domainworkstream.VaultUpdateLog) (string, error) {
	s.applied = append(s.applied, item)
	if s.appliedPath == "" {
		s.appliedPath = "/tmp/vault/ws_1/STATUS.md"
	}
	return s.appliedPath, nil
}

func (s *stubWorkstreamLister) PreviewVaultUpdate(_ context.Context, item domainworkstream.VaultUpdateLog) (*domainworkstream.VaultUpdatePreview, error) {
	if s.preview != nil {
		return s.preview, nil
	}
	return &domainworkstream.VaultUpdatePreview{
		UpdateID:        item.UpdateID,
		FilePath:        item.FilePath,
		CurrentContent:  "# STATUS\n\nold",
		ProposedContent: item.ProposedContent,
		AddedLines:      1,
		RemovedLines:    1,
		UnifiedDiff:     "--- current\n+++ proposed\n@@ -3,1 +3,1 @@\n-old\n+new\n",
	}, nil
}

func TestHandleWorkstreamStatus(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &stubWorkstreamLister{
		workstreams: []domainworkstream.Workstream{{
			WorkstreamID: "ws_1",
			Name:         "収益化",
			Status:       domainworkstream.StatusActive,
			CreatedAt:    now,
		}},
		goals: []domainworkstream.Goal{{
			GoalID:          "goal_1",
			WorkstreamID:    "ws_1",
			Title:           "低単価商品を作る",
			SuccessCriteria: []string{"対象読者が明確"},
			Verification:    []string{"Revenue checklist"},
			Status:          domainworkstream.StatusActive,
			CreatedAt:       now,
		}},
	}
	req := httptest.NewRequest(http.MethodGet, "/viewer/workstreams?limit=5", nil)
	rec := httptest.NewRecorder()

	HandleWorkstreamStatus(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if store.limit != 5 {
		t.Fatalf("limit=%d", store.limit)
	}
	var body struct {
		Workstreams  []domainworkstream.Workstream         `json:"workstreams"`
		Goals        []domainworkstream.Goal               `json:"goals"`
		Annotations  []domainworkstream.ArtifactAnnotation `json:"annotations"`
		Heartbeats   []domainworkstream.HeartbeatSchedule  `json:"heartbeats"`
		VaultUpdates []domainworkstream.VaultUpdateLog     `json:"vault_updates"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Workstreams) != 1 || body.Workstreams[0].WorkstreamID != "ws_1" {
		t.Fatalf("workstreams=%#v", body.Workstreams)
	}
	if len(body.Goals) != 1 || body.Goals[0].GoalID != "goal_1" {
		t.Fatalf("goals=%#v", body.Goals)
	}
	if body.Annotations == nil {
		t.Fatal("expected annotations key")
	}
	if body.Heartbeats == nil {
		t.Fatal("expected heartbeats key")
	}
	if body.VaultUpdates == nil {
		t.Fatal("expected vault_updates key")
	}
}

func TestHandleWorkstreamStatusInvalidLimit(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/viewer/workstreams?limit=bad", nil)
	rec := httptest.NewRecorder()

	HandleWorkstreamStatus(&stubWorkstreamLister{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestHandleWorkstreamCreate(t *testing.T) {
	store := &stubWorkstreamLister{}
	body := bytes.NewBufferString(`{"workstream_id":"ws_1","name":"収益化"}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/workstreams", body)
	rec := httptest.NewRecorder()

	HandleWorkstreamStatus(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.saved) != 1 {
		t.Fatalf("saved=%#v", store.saved)
	}
	if store.saved[0].Status != domainworkstream.StatusDraft {
		t.Fatalf("status=%q", store.saved[0].Status)
	}
	if store.saved[0].CreatedAt.IsZero() {
		t.Fatal("expected created_at default")
	}
}

func TestHandleWorkstreamCreateRejectsInvalidPayload(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/viewer/workstreams", bytes.NewBufferString(`{"workstream_id":"ws_1"}`))
	rec := httptest.NewRecorder()

	HandleWorkstreamStatus(&stubWorkstreamLister{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleWorkstreamGoalCreate(t *testing.T) {
	store := &stubWorkstreamLister{}
	req := httptest.NewRequest(http.MethodPost, "/viewer/workstreams/goals", bytes.NewBufferString(`{
		"goal_id":"goal_1",
		"workstream_id":"ws_1",
		"title":"LPを作る",
		"success_criteria":["CTAがある"],
		"verification":["Viewerで確認する"]
	}`))
	rec := httptest.NewRecorder()

	HandleWorkstreamGoalCreate(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.savedGoals) != 1 || store.savedGoals[0].Status != domainworkstream.StatusDraft {
		t.Fatalf("saved goals=%#v", store.savedGoals)
	}
}

func TestHandleWorkstreamArtifactCreate(t *testing.T) {
	store := &stubWorkstreamLister{}
	req := httptest.NewRequest(http.MethodPost, "/viewer/workstreams/artifacts", bytes.NewBufferString(`{
		"artifact_id":"art_1",
		"workstream_id":"ws_1",
		"artifact_type":"markdown",
		"file_path":"vault/workstreams/ws_1/STATUS.md"
	}`))
	rec := httptest.NewRecorder()

	HandleWorkstreamArtifactCreate(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.artifacts) != 1 || store.artifacts[0].Status != "draft" {
		t.Fatalf("artifacts=%#v", store.artifacts)
	}
}

func TestHandleWorkstreamAnnotationCreate(t *testing.T) {
	store := &stubWorkstreamLister{}
	req := httptest.NewRequest(http.MethodPost, "/viewer/workstreams/annotations", bytes.NewBufferString(`{
		"annotation_id":"ann_1",
		"artifact_id":"art_1",
		"target":"hero_heading",
		"comment":"見出しが抽象的"
	}`))
	rec := httptest.NewRecorder()

	HandleWorkstreamAnnotationCreate(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.annotations) != 1 || store.annotations[0].Status != "open" {
		t.Fatalf("annotations=%#v", store.annotations)
	}
}

func TestHandleWorkstreamAnnotationCreateAddsSteeringForKnownArtifact(t *testing.T) {
	store := &stubWorkstreamLister{
		artifacts: []domainworkstream.Artifact{{
			ArtifactID:   "art_1",
			WorkstreamID: "ws_1",
			Type:         "markdown",
			Status:       "draft",
		}},
	}
	req := httptest.NewRequest(http.MethodPost, "/viewer/workstreams/annotations", bytes.NewBufferString(`{
		"annotation_id":"ann_1",
		"artifact_id":"art_1",
		"target":"hero_heading",
		"comment":"見出しが抽象的"
	}`))
	rec := httptest.NewRecorder()

	HandleWorkstreamAnnotationCreate(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.steering) != 1 {
		t.Fatalf("steering=%#v", store.steering)
	}
	if store.steering[0].WorkstreamID != "ws_1" || store.steering[0].TargetArtifactID != "art_1" {
		t.Fatalf("steering=%#v", store.steering)
	}
	if store.steering[0].Instruction != "見出しが抽象的" || store.steering[0].Status != "pending" {
		t.Fatalf("steering=%#v", store.steering)
	}
}

func TestHandleWorkstreamSteeringCreate(t *testing.T) {
	store := &stubWorkstreamLister{}
	req := httptest.NewRequest(http.MethodPost, "/viewer/workstreams/steering", bytes.NewBufferString(`{
		"steering_id":"stq_1",
		"workstream_id":"ws_1",
		"instruction":"CTAを具体化する"
	}`))
	rec := httptest.NewRecorder()

	HandleWorkstreamSteeringCreate(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.steering) != 1 || store.steering[0].Status != "pending" {
		t.Fatalf("steering=%#v", store.steering)
	}
}

func TestHandleWorkstreamHeartbeatCreate(t *testing.T) {
	store := &stubWorkstreamLister{}
	req := httptest.NewRequest(http.MethodPost, "/viewer/workstreams/heartbeats", bytes.NewBufferString(`{
		"heartbeat_id":"hb_1",
		"workstream_id":"ws_1",
		"schedule_text":"daily 08:00",
		"task":"draft report only"
	}`))
	rec := httptest.NewRecorder()

	HandleWorkstreamHeartbeatCreate(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.heartbeats) != 1 || store.heartbeats[0].Status != domainworkstream.StatusActive {
		t.Fatalf("heartbeats=%#v", store.heartbeats)
	}
}

func TestHandleWorkstreamVaultUpdateCreate(t *testing.T) {
	store := &stubWorkstreamLister{}
	req := httptest.NewRequest(http.MethodPost, "/viewer/workstreams/vault-updates", bytes.NewBufferString(`{
		"update_id":"upd_1",
		"workstream_id":"ws_1",
		"file_path":"vault/workstreams/ws_1/STATUS.md",
		"update_type":"status"
	}`))
	rec := httptest.NewRecorder()

	HandleWorkstreamVaultUpdateCreate(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.vaultUpdates) != 1 || store.vaultUpdates[0].ReviewStatus != "pending" {
		t.Fatalf("vaultUpdates=%#v", store.vaultUpdates)
	}
}

func TestHandleWorkstreamVaultUpdateReviewApproves(t *testing.T) {
	store := &stubWorkstreamLister{}
	req := httptest.NewRequest(http.MethodPost, "/viewer/workstreams/vault-updates/review", bytes.NewBufferString(`{
		"update_id":"upd_1",
		"workstream_id":"ws_1",
		"file_path":"vault/workstreams/ws_1/STATUS.md",
		"update_type":"status",
		"review_status":"approved"
	}`))
	rec := httptest.NewRecorder()

	HandleWorkstreamVaultUpdateReview(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.vaultUpdates) != 1 {
		t.Fatalf("vaultUpdates=%#v", store.vaultUpdates)
	}
	if store.vaultUpdates[0].ReviewStatus != domainworkstream.VaultReviewApproved {
		t.Fatalf("vaultUpdates=%#v", store.vaultUpdates)
	}
	if len(store.applied) != 0 {
		t.Fatalf("expected approval without proposed_content to remain ledger-only, applied=%#v", store.applied)
	}
}

func TestHandleWorkstreamVaultUpdateReviewAppliesProposedContent(t *testing.T) {
	store := &stubWorkstreamLister{appliedPath: "/tmp/vault/ws_1/STATUS.md"}
	req := httptest.NewRequest(http.MethodPost, "/viewer/workstreams/vault-updates/review", bytes.NewBufferString(`{
		"update_id":"upd_1",
		"workstream_id":"ws_1",
		"file_path":"ws_1/STATUS.md",
		"update_type":"status",
		"proposed_content":"# STATUS\n\napproved",
		"review_status":"approved"
	}`))
	rec := httptest.NewRecorder()

	HandleWorkstreamVaultUpdateReview(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Applied     bool   `json:"applied"`
		AppliedPath string `json:"applied_path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Applied || body.AppliedPath != store.appliedPath {
		t.Fatalf("unexpected apply result: %#v", body)
	}
	if len(store.applied) != 1 || store.applied[0].ProposedContent == "" {
		t.Fatalf("applied=%#v", store.applied)
	}
	if len(store.vaultUpdates) != 1 || store.vaultUpdates[0].ReviewStatus != domainworkstream.VaultReviewApproved || !store.vaultUpdates[0].Applied || store.vaultUpdates[0].AppliedPath != store.appliedPath {
		t.Fatalf("vaultUpdates=%#v", store.vaultUpdates)
	}
}

func TestHandleWorkstreamVaultUpdatePreview(t *testing.T) {
	store := &stubWorkstreamLister{}
	req := httptest.NewRequest(http.MethodPost, "/viewer/workstreams/vault-updates/preview", bytes.NewBufferString(`{
		"update_id":"upd_1",
		"workstream_id":"ws_1",
		"file_path":"vault/workstreams/ws_1/STATUS.md",
		"proposed_content":"# STATUS\n\nnew",
		"review_status":"pending"
	}`))
	rec := httptest.NewRecorder()

	HandleWorkstreamVaultUpdatePreview(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"unified_diff"`) || !strings.Contains(rec.Body.String(), `+new`) {
		t.Fatalf("expected diff preview response, got %s", rec.Body.String())
	}
}

func TestHandleWorkstreamVaultUpdateReviewRejectsPendingStatus(t *testing.T) {
	store := &stubWorkstreamLister{}
	req := httptest.NewRequest(http.MethodPost, "/viewer/workstreams/vault-updates/review", bytes.NewBufferString(`{
		"update_id":"upd_1",
		"workstream_id":"ws_1",
		"file_path":"vault/workstreams/ws_1/STATUS.md",
		"review_status":"pending"
	}`))
	rec := httptest.NewRecorder()

	HandleWorkstreamVaultUpdateReview(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.vaultUpdates) != 0 {
		t.Fatalf("vaultUpdates=%#v", store.vaultUpdates)
	}
}
