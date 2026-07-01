package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	domainpersona "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/persona"
)

type stubPersonaObservationStore struct {
	discomforts  []domainpersona.DiscomfortLog
	triggers     []domainpersona.TriggerLog
	canonicals   []domainpersona.CanonicalResponseLog
	observations []domainpersona.ObservationLog
	metaUpdates  []domainpersona.MetaProfileUpdate
	sessions     []domainpersona.InterfaceSession
	applied      []domainpersona.MetaProfileUpdate
	appliedPath  string
}

func (s *stubPersonaObservationStore) ListDiscomfortLogs(_ context.Context, _ int) ([]domainpersona.DiscomfortLog, error) {
	return s.discomforts, nil
}
func (s *stubPersonaObservationStore) ListTriggerLogs(_ context.Context, _ int) ([]domainpersona.TriggerLog, error) {
	return s.triggers, nil
}
func (s *stubPersonaObservationStore) ListCanonicalResponseLogs(_ context.Context, _ int) ([]domainpersona.CanonicalResponseLog, error) {
	return s.canonicals, nil
}
func (s *stubPersonaObservationStore) ListObservationLogs(_ context.Context, _ int) ([]domainpersona.ObservationLog, error) {
	return s.observations, nil
}
func (s *stubPersonaObservationStore) ListMetaProfileUpdates(_ context.Context, _ int) ([]domainpersona.MetaProfileUpdate, error) {
	return s.metaUpdates, nil
}
func (s *stubPersonaObservationStore) ListInterfaceSessions(_ context.Context, _ int) ([]domainpersona.InterfaceSession, error) {
	return s.sessions, nil
}
func (s *stubPersonaObservationStore) SaveDiscomfortLog(_ context.Context, item domainpersona.DiscomfortLog) error {
	if err := domainpersona.ValidateDiscomfortLog(item); err != nil {
		return err
	}
	s.discomforts = append(s.discomforts, item)
	return nil
}
func (s *stubPersonaObservationStore) SaveTriggerLog(_ context.Context, item domainpersona.TriggerLog) error {
	if err := domainpersona.ValidateTriggerLog(item); err != nil {
		return err
	}
	s.triggers = append(s.triggers, item)
	return nil
}
func (s *stubPersonaObservationStore) SaveCanonicalResponseLog(_ context.Context, item domainpersona.CanonicalResponseLog) error {
	if err := domainpersona.ValidateCanonicalResponseLog(item); err != nil {
		return err
	}
	s.canonicals = append(s.canonicals, item)
	return nil
}
func (s *stubPersonaObservationStore) SaveObservationLog(_ context.Context, item domainpersona.ObservationLog) error {
	if err := domainpersona.ValidateObservationLog(item); err != nil {
		return err
	}
	s.observations = append(s.observations, item)
	return nil
}
func (s *stubPersonaObservationStore) SaveMetaProfileUpdate(_ context.Context, item domainpersona.MetaProfileUpdate) error {
	if err := domainpersona.ValidateMetaProfileUpdate(item); err != nil {
		return err
	}
	s.metaUpdates = append(s.metaUpdates, item)
	return nil
}
func (s *stubPersonaObservationStore) SaveInterfaceSession(_ context.Context, item domainpersona.InterfaceSession) error {
	if err := domainpersona.ValidateInterfaceSession(item); err != nil {
		return err
	}
	s.sessions = append(s.sessions, item)
	return nil
}

func (s *stubPersonaObservationStore) ApplyMetaProfileUpdate(_ context.Context, item domainpersona.MetaProfileUpdate) (string, error) {
	s.applied = append(s.applied, item)
	if s.appliedPath == "" {
		s.appliedPath = "/tmp/persona/observers/lumina/meta/ren.md"
	}
	return s.appliedPath, nil
}

func TestHandlePersonaObservationStatus(t *testing.T) {
	store := &stubPersonaObservationStore{
		discomforts: []domainpersona.DiscomfortLog{{
			EventID:     "evt_discomfort_1",
			CharacterID: "mio",
			Discomfort:  "期待と違う",
			Status:      "candidate",
		}},
		observations: []domainpersona.ObservationLog{{
			EventID:         "evt_observation_1",
			ObserverID:      "lumina",
			TargetID:        "ren",
			ObservationType: "daily",
			Sensitivity:     "normal",
			ReviewStatus:    "pending",
		}},
		metaUpdates: []domainpersona.MetaProfileUpdate{{
			UpdateID:        "meta_upd_1",
			ObserverID:      "lumina",
			TargetID:        "ren",
			Section:         "Workstyle",
			ProposedContent: "作業を仕様化してから進める",
			Sensitivity:     "normal",
			ReviewStatus:    "pending",
		}},
	}
	req := httptest.NewRequest(http.MethodGet, "/viewer/persona-observation?limit=5", nil)
	rec := httptest.NewRecorder()

	HandlePersonaObservationStatus(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Discomforts  []domainpersona.DiscomfortLog             `json:"discomfort_logs"`
		Observations []domainpersona.ObservationLog            `json:"observation_logs"`
		MetaUpdates  []domainpersona.MetaProfileUpdate         `json:"meta_profile_updates"`
		Characters   map[string]domainpersona.CharacterProfile `json:"characters"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Discomforts) != 1 || len(body.Observations) != 1 || len(body.MetaUpdates) != 1 {
		t.Fatalf("unexpected body: %#v", body)
	}
}

func TestHandlePersonaObservationStatusIncludesRuntimeCharacters(t *testing.T) {
	store := &stubPersonaObservationStore{}
	characters := map[string]domainpersona.CharacterProfile{
		"mio": {
			CharacterID: "mio",
			Lore:        map[string]string{"profile": "Mio profile"},
			Persona:     map[string]string{"self": "Mio self"},
			Modes:       map[string]string{"as_character": "As character"},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/viewer/persona-observation?limit=5", nil)
	rec := httptest.NewRecorder()

	HandlePersonaObservationStatus(store, characters).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Characters map[string]domainpersona.CharacterProfile `json:"characters"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Characters["mio"].Persona["self"] != "Mio self" {
		t.Fatalf("characters=%#v", body.Characters)
	}
}

func TestHandlePersonaObservationCreateEndpoints(t *testing.T) {
	store := &stubPersonaObservationStore{}
	tests := []struct {
		name    string
		handler http.HandlerFunc
		path    string
		body    string
	}{
		{
			name:    "discomfort",
			handler: HandlePersonaDiscomfortCreate(store),
			path:    "/viewer/persona-observation/discomforts",
			body:    `{"event_id":"evt_discomfort_1","character_id":"mio","discomfort":"硬すぎる"}`,
		},
		{
			name:    "trigger",
			handler: HandlePersonaTriggerLogCreate(store),
			path:    "/viewer/persona-observation/triggers",
			body:    `{"event_id":"evt_trigger_1","character_id":"kuro","trigger_id":"danger","activated":true,"confidence":0.5}`,
		},
		{
			name:    "canonical",
			handler: HandlePersonaCanonicalResponseLogCreate(store),
			path:    "/viewer/persona-observation/canonical-responses",
			body:    `{"event_id":"evt_canonical_1","character_id":"kuro","response_id":"block","used":true}`,
		},
		{
			name:    "observation",
			handler: HandlePersonaObservationLogCreate(store),
			path:    "/viewer/persona-observation/observations",
			body:    `{"event_id":"evt_observation_1","observer_id":"lumina","target_id":"ren","observation_type":"daily","summary":"候補"}`,
		},
		{
			name:    "meta_update",
			handler: HandlePersonaMetaProfileUpdateCreate(store),
			path:    "/viewer/persona-observation/meta-updates",
			body:    `{"update_id":"meta_upd_1","observer_id":"lumina","target_id":"ren","section":"Workstyle","proposed_content":"作業を仕様化してから進める","review_status":"approved"}`,
		},
		{
			name:    "session",
			handler: HandlePersonaInterfaceSessionCreate(store),
			path:    "/viewer/persona-observation/sessions",
			body:    `{"session_id":"persona_session_1","character_id":"mio","interface_type":"web","session_key":"web:viewer"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tt.path, bytes.NewBufferString(tt.body))
			rec := httptest.NewRecorder()
			tt.handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusCreated {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandlePersonaMetaProfileUpdateCreateForcesPending(t *testing.T) {
	store := &stubPersonaObservationStore{}
	req := httptest.NewRequest(http.MethodPost, "/viewer/persona-observation/meta-updates", bytes.NewBufferString(`{
		"update_id":"meta_upd_1",
		"observer_id":"lumina",
		"target_id":"ren",
		"section":"Risk Signs",
		"proposed_content":"疲労時は判断を急がない方がよい",
		"sensitivity":"health",
		"review_status":"approved"
	}`))
	rec := httptest.NewRecorder()

	HandlePersonaMetaProfileUpdateCreate(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.metaUpdates) != 1 || store.metaUpdates[0].ReviewStatus != "pending" {
		t.Fatalf("meta updates=%#v", store.metaUpdates)
	}
}

func TestHandlePersonaObservationAggregateCreatesPendingSummaryAndMetaUpdate(t *testing.T) {
	store := &stubPersonaObservationStore{
		observations: []domainpersona.ObservationLog{{
			EventID:         "evt_observation_1",
			ObserverID:      "lumina",
			TargetID:        "ren",
			ObservationType: "daily",
			Summary:         "仕様を先に固定してから実装する",
			Sensitivity:     "normal",
			ReviewStatus:    "pending",
		}, {
			EventID:         "evt_observation_2",
			ObserverID:      "lumina",
			TargetID:        "ren",
			ObservationType: "daily_summary",
			Summary:         "既存summaryは再集約しない",
			Sensitivity:     "normal",
			ReviewStatus:    "pending",
		}},
	}
	req := httptest.NewRequest(http.MethodPost, "/viewer/persona-observation/aggregate", bytes.NewBufferString(`{
		"observer_id":"lumina",
		"target_id":"ren",
		"period":"weekly"
	}`))
	rec := httptest.NewRecorder()

	HandlePersonaObservationAggregate(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.observations) != 3 {
		t.Fatalf("observations=%#v", store.observations)
	}
	summary := store.observations[2]
	if summary.ObservationType != "weekly_summary" || summary.ReviewStatus != "pending" || len(summary.EvidenceRefs) != 1 {
		t.Fatalf("summary=%#v", summary)
	}
	if len(store.metaUpdates) != 1 {
		t.Fatalf("meta updates=%#v", store.metaUpdates)
	}
	meta := store.metaUpdates[0]
	if meta.Section != "Stable Traits" || meta.ReviewStatus != "pending" || len(meta.EvidenceRefs) != 1 {
		t.Fatalf("meta=%#v", meta)
	}
	var response struct {
		SourceCount  int  `json:"source_count"`
		AutoApproved bool `json:"auto_approved"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.SourceCount != 1 || response.AutoApproved {
		t.Fatalf("response=%#v", response)
	}
}

func TestHandlePersonaObservationAggregateRejectsInvalidPeriod(t *testing.T) {
	store := &stubPersonaObservationStore{}
	req := httptest.NewRequest(http.MethodPost, "/viewer/persona-observation/aggregate", bytes.NewBufferString(`{
		"observer_id":"lumina",
		"target_id":"ren",
		"period":"yearly"
	}`))
	rec := httptest.NewRecorder()

	HandlePersonaObservationAggregate(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.observations) != 0 || len(store.metaUpdates) != 0 {
		t.Fatalf("unexpected writes observations=%#v meta=%#v", store.observations, store.metaUpdates)
	}
}

func TestHandlePersonaMetaProfileUpdateReviewRejectsPending(t *testing.T) {
	store := &stubPersonaObservationStore{}
	req := httptest.NewRequest(http.MethodPost, "/viewer/persona-observation/meta-updates/review", bytes.NewBufferString(`{
		"update_id":"meta_upd_1",
		"observer_id":"lumina",
		"target_id":"ren",
		"section":"Risk Signs",
		"proposed_content":"疲労時は判断を急がない方がよい",
		"sensitivity":"health",
		"review_status":"pending"
	}`))
	rec := httptest.NewRecorder()

	HandlePersonaMetaProfileUpdateReview(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlePersonaMetaProfileUpdateReviewApprovesSensitiveUpdate(t *testing.T) {
	store := &stubPersonaObservationStore{appliedPath: "/tmp/persona/observers/lumina/meta/ren.md"}
	req := httptest.NewRequest(http.MethodPost, "/viewer/persona-observation/meta-updates/review", bytes.NewBufferString(`{
		"update_id":"meta_upd_1",
		"observer_id":"lumina",
		"target_id":"ren",
		"section":"Risk Signs",
		"proposed_content":"疲労時は判断を急がない方がよい",
		"sensitivity":"health",
		"review_status":"approved"
	}`))
	rec := httptest.NewRecorder()

	HandlePersonaMetaProfileUpdateReview(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Applied     bool   `json:"applied"`
		AppliedPath string `json:"applied_path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.Applied || body.AppliedPath != store.appliedPath {
		t.Fatalf("unexpected apply result: %#v", body)
	}
	if len(store.metaUpdates) != 1 || store.metaUpdates[0].ReviewStatus != "approved" || store.metaUpdates[0].ReviewedAt.IsZero() {
		t.Fatalf("meta updates=%#v", store.metaUpdates)
	}
	if len(store.applied) != 1 || store.applied[0].ReviewStatus != "approved" {
		t.Fatalf("applied=%#v", store.applied)
	}
}

func TestHandlePersonaMetaProfileUpdateReviewRejectDoesNotApply(t *testing.T) {
	store := &stubPersonaObservationStore{}
	req := httptest.NewRequest(http.MethodPost, "/viewer/persona-observation/meta-updates/review", bytes.NewBufferString(`{
		"update_id":"meta_upd_1",
		"observer_id":"lumina",
		"target_id":"ren",
		"section":"Risk Signs",
		"proposed_content":"疲労時は判断を急がない方がよい",
		"sensitivity":"health",
		"review_status":"rejected"
	}`))
	rec := httptest.NewRecorder()

	HandlePersonaMetaProfileUpdateReview(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.applied) != 0 {
		t.Fatalf("expected rejected update not to apply, applied=%#v", store.applied)
	}
	if len(store.metaUpdates) != 1 || store.metaUpdates[0].ReviewStatus != "rejected" {
		t.Fatalf("meta updates=%#v", store.metaUpdates)
	}
}

func TestHandlePersonaObservationRejectsSensitiveApprovedObservation(t *testing.T) {
	store := &stubPersonaObservationStore{}
	req := httptest.NewRequest(http.MethodPost, "/viewer/persona-observation/observations", bytes.NewBufferString(`{
		"event_id":"evt_observation_1",
		"observer_id":"lumina",
		"target_id":"ren",
		"observation_type":"daily",
		"sensitivity":"health",
		"review_status":"approved"
	}`))
	rec := httptest.NewRecorder()

	HandlePersonaObservationLogCreate(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
