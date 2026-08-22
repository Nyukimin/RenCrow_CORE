package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	appbacklog "github.com/Nyukimin/RenCrow_CORE/internal/application/backlog"
	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	workstreampersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/workstream"
)

func TestAtlasOwnerHTTPFlowReachesLiveVerifiedWithEvidence(t *testing.T) {
	items := &atlasHTTPItemStore{}
	service := appbacklog.NewService(items, workstreampersistence.NewJSONLStore(t.TempDir()))
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
	for _, stage := range stages {
		body := `{"delivery_state":"` + stage + `","evidence_refs":[{"stage":"` + stage + `","kind":"stage","ref":"` + stage + `-evidence","passed":true}]}`
		rec := post("/v1/atlas/items/e2e/revise", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("revise stage=%s status=%d body=%s", stage, rec.Code, rec.Body.String())
		}
		item := decodeItem(rec)
		if item.DeliveryState != stage {
			t.Fatalf("stage=%s item=%+v", stage, item)
		}
	}
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/viewer/atlas", nil))
	if get.Code != http.StatusOK || !bytes.Contains(get.Body.Bytes(), []byte(`"active":null`)) {
		t.Fatalf("projection status=%d body=%s", get.Code, get.Body.String())
	}
	evidence := httptest.NewRecorder()
	handler.ServeHTTP(evidence, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/viewer/atlas/evidence/unit_atlas_e2e", nil))
	if evidence.Code != http.StatusOK || !bytes.Contains(evidence.Body.Bytes(), []byte(`"evidence"`)) {
		t.Fatalf("evidence status=%d body=%s", evidence.Code, evidence.Body.String())
	}
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
