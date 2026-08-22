package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	appbacklog "github.com/Nyukimin/RenCrow_CORE/internal/application/backlog"
	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	workstreampersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/workstream"
)

func TestAtlasOwnerHTTPFlowReachesLiveVerifiedWithEvidence(t *testing.T) {
	items := &atlasHTTPItemStore{}
	workstream := workstreampersistence.NewJSONLStore(t.TempDir())
	service := appbacklog.NewService(items, workstream).WithEvidenceVerifier(atlasHTTPVerifier{})
	token := []byte("atlas-owner-token-012345678901234567890123")
	handler := NewAtlasHandler(service, "ren", token)

	post := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+path, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+string(token))
		req.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
		req.Header.Set("X-RenCrow-Interaction-Profile", "cmd-control")
		req.RemoteAddr = "127.0.0.1:43210"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	decodeItem := func(rec *httptest.ResponseRecorder) domainbacklog.Item {
		var body struct {
			Item domainbacklog.Item `json:"item"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode status=%d body=%s err=%v", rec.Code, rec.Body.String(), err)
		}
		return body.Item
	}

	if rec := post("/v1/atlas/intake", `{"item_id":"e2e","title":"Atlas E2E","purpose":"verify the Atlas lifecycle","source_refs":[{"type":"manual","locator":"e2e"}]}`); rec.Code < 200 || rec.Code >= 300 {
		t.Fatalf("intake status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := post("/v1/atlas/items/e2e/candidate", `{}`); rec.Code != http.StatusOK {
		t.Fatalf("candidate status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := post("/v1/atlas/items/e2e/adopt", `{"reason":"owner"}`); rec.Code != http.StatusOK {
		t.Fatalf("adopt status=%d body=%s", rec.Code, rec.Body.String())
	}
	stages := []string{domainbacklog.DeliverySpec, domainbacklog.DeliveryTDDRed, domainbacklog.DeliveryTDDGreen, domainbacklog.DeliveryRefactor, domainbacklog.DeliveryE2EPredeploy, domainbacklog.DeliveryBuild, domainbacklog.DeliveryDeploy, domainbacklog.DeliveryRestart, domainbacklog.DeliveryPostDeployVerify, domainbacklog.DeliveryLiveVerified}
	evidenceKind := map[string]string{
		domainbacklog.DeliverySpec:             "spec",
		domainbacklog.DeliveryTDDRed:           "execution_report",
		domainbacklog.DeliveryTDDGreen:         "execution_report",
		domainbacklog.DeliveryRefactor:         "execution_report",
		domainbacklog.DeliveryE2EPredeploy:     "execution_report",
		domainbacklog.DeliveryBuild:            "execution_report",
		domainbacklog.DeliveryDeploy:           "deploy_receipt",
		domainbacklog.DeliveryRestart:          "deploy_receipt",
		domainbacklog.DeliveryPostDeployVerify: "readiness",
		domainbacklog.DeliveryLiveVerified:     "production_smoke",
	}
	for _, stage := range stages {
		body := `{"delivery_state":"` + stage + `","evidence_refs":[{"stage":"` + stage + `","kind":"` + evidenceKind[stage] + `","ref":"` + stage + `-evidence","passed":true}]}`
		rec := post("/v1/atlas/items/e2e/revise", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("revise stage=%s status=%d body=%s", stage, rec.Code, rec.Body.String())
		}
		item := decodeItem(rec)
		wantState := stage
		if stage == domainbacklog.DeliveryLiveVerified {
			wantState = domainbacklog.DeliveryDone
		}
		if item.DeliveryState != wantState {
			t.Fatalf("stage=%s item=%+v", stage, item)
		}
	}
	if lease, found, err := workstream.GetImplementationLease(context.Background(), domainbacklog.ImplementationLeaseName); err != nil {
		t.Fatalf("get implementation lease: %v", err)
	} else if found {
		t.Fatalf("canonical LIVE_VERIFIED closure must release lease: %+v", lease)
	}
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/viewer/atlas", nil))
	if get.Code != http.StatusOK {
		t.Fatalf("projection status=%d body=%s", get.Code, get.Body.String())
	}
	var projection appbacklog.Projection
	if err := json.Unmarshal(get.Body.Bytes(), &projection); err != nil {
		t.Fatalf("decode projection: %v body=%s", err, get.Body.String())
	}
	if projection.Active != nil || len(projection.Current) != 1 || projection.Current[0].ItemID != "e2e" || projection.Current[0].DeliveryState != domainbacklog.DeliveryDone {
		t.Fatalf("canonical DONE projection=%+v body=%s", projection, get.Body.String())
	}
	closureFound := false
	for _, closure := range projection.ClosureReceipts {
		if closure.UnitID == "unit_atlas_e2e" && closure.Status == domainworkstream.ClosureStatusCompleted {
			closureFound = true
			break
		}
	}
	if !closureFound {
		t.Fatalf("completed closure receipt missing: %+v", projection.ClosureReceipts)
	}
	evidence := httptest.NewRecorder()
	handler.ServeHTTP(evidence, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/viewer/atlas/evidence/unit_atlas_e2e", nil))
	if evidence.Code != http.StatusOK || !bytes.Contains(evidence.Body.Bytes(), []byte(`"evidence"`)) {
		t.Fatalf("evidence status=%d body=%s", evidence.Code, evidence.Body.String())
	}
}

type atlasHTTPVerifier struct{}

func (atlasHTTPVerifier) Verify(_ context.Context, request appbacklog.EvidenceVerificationRequest) (bool, error) {
	return request.Ref.Ref != "", nil
}

type atlasHTTPItemStore struct{ items []domainbacklog.Item }

func (s *atlasHTTPItemStore) List(_ context.Context, _ int) ([]domainbacklog.Item, error) {
	return append([]domainbacklog.Item(nil), s.items...), nil
}
func (s *atlasHTTPItemStore) Save(_ context.Context, item domainbacklog.Item) error {
	for i := range s.items {
		if s.items[i].ItemID == item.ItemID {
			s.items[i] = item
			return nil
		}
	}
	s.items = append(s.items, item)
	return nil
}

func TestAtlasOwnerPOSTRequiresBearerAndProfile(t *testing.T) {
	service := appbacklog.NewService(&atlasHTTPItemStore{}, nil)
	handler := NewAtlasHandler(service, "ren", []byte("atlas-owner-token-012345678901234567890123"))
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1/atlas/intake", bytes.NewBufferString(`{"title":"x"}`))
	req.RemoteAddr = "127.0.0.1:43210"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAtlasSpecificationProjectionUsesSpecIDAndResolvesItemReferences(t *testing.T) {
	store := &atlasHTTPItemStore{items: []domainbacklog.Item{{
		SchemaVersion: domainbacklog.SchemaVersion2, ItemID: "atlas:spec-item", FeatureID: "spec-item", Title: "spec item",
		ConceptState: domainbacklog.ConceptCandidate, DeliveryState: domainbacklog.DeliveryNone,
		SpecificationRefs: []string{"spec_atlas_lifecycle_functional_v1"},
	}}}
	handler := HandleAtlas(appbacklog.NewService(store, nil))
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/viewer/atlas/specifications/spec_atlas_lifecycle_functional_v1", nil)
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, request)
	if record.Code != http.StatusOK || !bytes.Contains(record.Body.Bytes(), []byte(`"body_available":true`)) {
		t.Fatalf("specification status=%d body=%s", record.Code, record.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1/viewer/atlas/items/atlas:spec-item", nil)
	record = httptest.NewRecorder()
	handler.ServeHTTP(record, request)
	if record.Code != http.StatusOK || !bytes.Contains(record.Body.Bytes(), []byte(`"resolved_specifications"`)) {
		t.Fatalf("item resolved specs status=%d body=%s", record.Code, record.Body.String())
	}
}

func TestAtlasSpecificationProjectionRejectsEncodedPathSegments(t *testing.T) {
	handler := HandleAtlas(appbacklog.NewService(&atlasHTTPItemStore{}, nil))
	for _, target := range []string{
		"http://127.0.0.1/viewer/atlas/specifications/spec%2Fsecret",
		"http://127.0.0.1/viewer/atlas/specifications/..%5Csecret",
	} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		record := httptest.NewRecorder()
		handler.ServeHTTP(record, request)
		if record.Code != http.StatusNotFound {
			t.Fatalf("target=%s status=%d body=%s", target, record.Code, record.Body.String())
		}
	}
}

func TestAtlasOwnerHTTPIntakePreservesSchemaV2DesignCard(t *testing.T) {
	store := &atlasHTTPItemStore{}
	token := []byte("atlas-owner-token-012345678901234567890123")
	handler := NewAtlasHandler(appbacklog.NewService(store, nil), "ren", token)
	body := `{"item_id":"http-design-card","feature_id":"atlas.http-design-card","kind":"idea","title":"HTTP design card","purpose":"retain a partial design card","problem":"owner intake dropped design memory","idea":"carry every card field through CORE","background":"the owner API is the normal intake path","expected_effect":["lossless reconstruction","auditable state"],"relation_refs":["atlas:lifecycle","atlas:memory"],"specification_refs":["spec_atlas_idea_recording_v1","spec_l0v2_external"],"source_refs":[{"type":"test","locator":"http-design-card"}]}`
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1/atlas/intake", bytes.NewBufferString(body))
	request.Header.Set("Authorization", "Bearer "+string(token))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
	request.Header.Set("X-RenCrow-Interaction-Profile", "cmd-control")
	request.RemoteAddr = "127.0.0.1:43210"
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, request)
	if record.Code != http.StatusCreated {
		t.Fatalf("intake status=%d body=%s", record.Code, record.Body.String())
	}
	var result appbacklog.IntakeResult
	if err := json.Unmarshal(record.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode intake response: %v body=%s", err, record.Body.String())
	}
	item := result.Item
	if item.FeatureID != "atlas.http-design-card" || item.Problem != "owner intake dropped design memory" || item.Idea != "carry every card field through CORE" || item.Background != "the owner API is the normal intake path" {
		t.Fatalf("HTTP intake lost design card scalars: %+v", item)
	}
	if !reflect.DeepEqual(item.ExpectedEffect, []string{"lossless reconstruction", "auditable state"}) || !reflect.DeepEqual(item.RelationRefs, []string{"atlas:lifecycle", "atlas:memory"}) || !reflect.DeepEqual(item.SpecificationRefs, []string{"spec_atlas_idea_recording_v1", "spec_l0v2_external"}) {
		t.Fatalf("HTTP intake lost design card slices: %+v", item)
	}
	if len(store.items) != 1 || store.items[0].FeatureID != item.FeatureID || store.items[0].Problem != item.Problem || store.items[0].Idea != item.Idea || store.items[0].Background != item.Background || !reflect.DeepEqual(store.items[0].ExpectedEffect, item.ExpectedEffect) || !reflect.DeepEqual(store.items[0].RelationRefs, item.RelationRefs) || !reflect.DeepEqual(store.items[0].SpecificationRefs, item.SpecificationRefs) {
		t.Fatalf("HTTP intake persistence lost design card fields: stored=%+v response=%+v", store.items, item)
	}
}
