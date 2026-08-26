package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

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
	if rec := post("/v1/atlas/items/e2e/revalidate", `{"decision":"PROMOTE","reason":"owner review","forced":true,"bypass_reason":"runtime_continuity"}`); rec.Code != http.StatusOK {
		t.Fatalf("revalidate status=%d body=%s", rec.Code, rec.Body.String())
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

func TestAtlasOwnerMaturationHTTPRequiresLoopbackBearerAndCmdControl(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	service := appbacklog.NewService(&atlasHTTPItemStore{items: []domainbacklog.Item{atlasMaturationHTTPItem("auth", start)}}, nil).WithClock(func() time.Time { return start.Add(7 * 24 * time.Hour) })
	token := "atlas-owner-token-012345678901234567890123"
	handler := NewAtlasHandler(service, "ren", []byte(token))
	body := `{"decision":"PROMOTE","reason":"owner review","forced":true}`

	cases := []struct {
		name       string
		remoteAddr string
		authorize  bool
		profile    string
		wantStatus int
	}{
		{name: "missing bearer", remoteAddr: "127.0.0.1:43210", profile: "cmd-control", wantStatus: http.StatusUnauthorized},
		{name: "non loopback", remoteAddr: "192.0.2.10:43210", authorize: true, profile: "cmd-control", wantStatus: http.StatusNotFound},
		{name: "wrong profile", remoteAddr: "127.0.0.1:43210", authorize: true, profile: "cmd-diagnostics", wantStatus: http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1/atlas/items/auth/revalidate", bytes.NewBufferString(body))
			request.RemoteAddr = tc.remoteAddr
			if tc.authorize {
				request.Header.Set("Authorization", "Bearer "+token)
			}
			request.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
			request.Header.Set("X-RenCrow-Interaction-Profile", tc.profile)
			record := httptest.NewRecorder()
			handler.ServeHTTP(record, request)
			if record.Code != tc.wantStatus {
				t.Fatalf("status=%d body=%s want=%d", record.Code, record.Body.String(), tc.wantStatus)
			}
		})
	}
}

func TestAtlasOwnerRevalidateHTTPReturnsLatestPersistedRecord(t *testing.T) {
	start := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	now := start.Add(7 * 24 * time.Hour)
	store := &atlasHTTPItemStore{items: []domainbacklog.Item{atlasMaturationHTTPItem("revalidate", start)}}
	service := appbacklog.NewService(store, nil).WithClock(func() time.Time { return now })
	handler := NewAtlasHandler(service, "ren", []byte(atlasOwnerHTTPToken))
	record := atlasOwnerHTTPPost(t, handler, atlasOwnerHTTPToken, "/v1/atlas/items/revalidate/revalidate", `{"request_id":"review-1","decision":"PROMOTE","reason":"still valuable","related_backlogs":["related-1"],"conflicting_specs":["spec-1"],"technology_changes":["Go"],"architecture_impact":"none","implementation_value":"high","next_review_trigger":"new evidence","forced":true}`)
	if record.Code != http.StatusOK {
		t.Fatalf("revalidate status=%d body=%s", record.Code, record.Body.String())
	}
	var response struct {
		Item               domainbacklog.Item               `json:"item"`
		RevalidationRecord domainbacklog.RevalidationRecord `json:"revalidation_record"`
	}
	if err := json.Unmarshal(record.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode revalidate response: %v body=%s", err, record.Body.String())
	}
	if response.Item.MaturationState != domainbacklog.MaturationStatePromoted || len(response.Item.RevalidationRecords) != 1 {
		t.Fatalf("revalidate item=%+v", response.Item)
	}
	if response.RevalidationRecord.BacklogID != response.Item.ItemID || response.RevalidationRecord.Decision != domainbacklog.RevalidationDecisionPromote || response.RevalidationRecord.MaturationDays != 7 || response.RevalidationRecord.Reason != "still valuable" {
		t.Fatalf("revalidation receipt=%+v item=%+v", response.RevalidationRecord, response.Item)
	}
	if !reflect.DeepEqual(response.RevalidationRecord, response.Item.RevalidationRecords[len(response.Item.RevalidationRecords)-1]) {
		t.Fatalf("response receipt differs from latest persisted record: receipt=%+v item=%+v", response.RevalidationRecord, response.Item.RevalidationRecords)
	}
	if len(store.items) != 1 || !reflect.DeepEqual(store.items[0].RevalidationRecords, response.Item.RevalidationRecords) {
		t.Fatalf("persisted revalidation history=%+v response=%+v", store.items[0].RevalidationRecords, response.Item.RevalidationRecords)
	}
}

type atlasHTTPRevalidationEvaluator struct{}

func (atlasHTTPRevalidationEvaluator) Evaluate(context.Context, appbacklog.RevalidationEvaluationInput) (appbacklog.RevalidationEvaluation, error) {
	return appbacklog.RevalidationEvaluation{Proposal: appbacklog.RevalidationProposal{
		Decision: domainbacklog.RevalidationDecisionHold, Reason: "依存仕様待ち",
		Necessity: "必要", Duplication: "なし", Mergeability: "統合不要",
		ArchitecturalFit: "整合", TechnologyValidity: "有効",
		ImplementationValue: "依存後に価値あり", Timing: "現在は早い",
		ArchitectureImpact: "新規層なし", NextReviewTrigger: "依存仕様の確定",
	}, ReviewAgents: []string{"shiro"}}, nil
}

func TestAtlasOwnerNormalRevalidationUsesRenCrowEvaluator(t *testing.T) {
	start := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	store := &atlasHTTPItemStore{items: []domainbacklog.Item{atlasMaturationHTTPItem("normal", start)}}
	service := appbacklog.NewService(store, nil).
		WithClock(func() time.Time { return start.Add(7 * 24 * time.Hour) }).
		WithRevalidationEvaluator(atlasHTTPRevalidationEvaluator{})
	handler := NewAtlasHandler(service, "ren", []byte(atlasOwnerHTTPToken))
	record := atlasOwnerHTTPPost(t, handler, atlasOwnerHTTPToken, "/v1/atlas/items/normal/revalidate", `{}`)
	if record.Code != http.StatusOK {
		t.Fatalf("normal revalidation status=%d body=%s", record.Code, record.Body.String())
	}
	var response struct {
		Item domainbacklog.Item `json:"item"`
	}
	if err := json.Unmarshal(record.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Item.MaturationState != domainbacklog.MaturationStateHold || len(response.Item.RevalidationRecords) != 1 || !reflect.DeepEqual(response.Item.RevalidationRecords[0].ReviewAgents, []string{"shiro"}) {
		t.Fatalf("normal evaluator result=%+v", response.Item)
	}
}

func TestAtlasOwnerRevalidateHTTPRejectsEarlyAndRequiresValidForcedBypass(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	now := start.Add(24 * time.Hour)
	store := &atlasHTTPItemStore{items: []domainbacklog.Item{atlasMaturationHTTPItem("early", start)}}
	service := appbacklog.NewService(store, nil).WithClock(func() time.Time { return now }).WithRevalidationEvaluator(atlasHTTPRevalidationEvaluator{})
	handler := NewAtlasHandler(service, "ren", []byte(atlasOwnerHTTPToken))
	base := `"decision":"PROMOTE","reason":"urgent continuity"`

	cases := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{name: "ordinary semantic injection", body: `{` + base + `}`, wantStatus: http.StatusBadRequest},
		{name: "forced without bypass", body: `{` + base + `,"forced":true}`, wantStatus: http.StatusConflict},
		{name: "forced invalid bypass", body: `{` + base + `,"forced":true,"bypass_reason":"operator_preference"}`, wantStatus: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record := atlasOwnerHTTPPost(t, handler, atlasOwnerHTTPToken, "/v1/atlas/items/early/revalidate", tc.body)
			if record.Code != tc.wantStatus {
				t.Fatalf("status=%d body=%s want=%d", record.Code, record.Body.String(), tc.wantStatus)
			}
		})
	}
	earlyNormal := atlasOwnerHTTPPost(t, handler, atlasOwnerHTTPToken, "/v1/atlas/items/early/revalidate", `{}`)
	if earlyNormal.Code != http.StatusConflict {
		t.Fatalf("early normal evaluation status=%d body=%s", earlyNormal.Code, earlyNormal.Body.String())
	}
	valid := atlasOwnerHTTPPost(t, handler, atlasOwnerHTTPToken, "/v1/atlas/items/early/revalidate", `{`+base+`,"forced":true,"bypass_reason":"runtime_continuity"}`)
	if valid.Code != http.StatusOK {
		t.Fatalf("valid forced revalidate status=%d body=%s", valid.Code, valid.Body.String())
	}
	var response struct {
		Item domainbacklog.Item `json:"item"`
	}
	if err := json.Unmarshal(valid.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode valid forced response: %v body=%s", err, valid.Body.String())
	}
	if !response.Item.MaturationBypass || response.Item.BypassReason != domainbacklog.MaturationBypassRuntimeContinuity || len(response.Item.RevalidationRecords) != 1 || !response.Item.RevalidationRecords[0].Forced {
		t.Fatalf("forced bypass was not persisted: %+v", response.Item)
	}
}

func TestAtlasOwnerMaturationHTTPRejectsUnknownTrailingAndOversizedJSON(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	store := &atlasHTTPItemStore{items: []domainbacklog.Item{atlasMaturationHTTPItem("strict", start)}}
	service := appbacklog.NewService(store, nil).WithClock(func() time.Time { return start.Add(7 * 24 * time.Hour) })
	handler := NewAtlasHandler(service, "ren", []byte(atlasOwnerHTTPToken))

	cases := []struct {
		name string
		body string
	}{
		{name: "unknown field", body: `{"decision":"PROMOTE","reason":"still valuable","forced":true,"unknown":true}`},
		{name: "trailing value", body: `{"decision":"PROMOTE","reason":"still valuable","forced":true}{}`},
		{name: "body bound", body: `{"decision":"PROMOTE","reason":"` + strings.Repeat("x", 64<<10) + `","forced":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record := atlasOwnerHTTPPost(t, handler, atlasOwnerHTTPToken, "/v1/atlas/items/strict/revalidate", tc.body)
			if record.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s want=%d", record.Code, record.Body.String(), http.StatusBadRequest)
			}
		})
	}
	if len(store.items) != 1 || len(store.items[0].RevalidationRecords) != 0 || store.items[0].MaturationState != domainbacklog.MaturationStateMaturation {
		t.Fatalf("strict decoder failure mutated item: %+v", store.items[0])
	}
}

func TestAtlasOwnerRevalidateHTTPMapsMissingMergeTargetToNotFound(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	store := &atlasHTTPItemStore{items: []domainbacklog.Item{atlasMaturationHTTPItem("merge", start)}}
	service := appbacklog.NewService(store, nil).WithClock(func() time.Time { return start.Add(7 * 24 * time.Hour) })
	handler := NewAtlasHandler(service, "ren", []byte(atlasOwnerHTTPToken))
	record := atlasOwnerHTTPPost(t, handler, atlasOwnerHTTPToken, "/v1/atlas/items/merge/revalidate", `{"decision":"MERGE","reason":"duplicate","merged_into":"missing-target"}`)
	if record.Code != http.StatusBadRequest {
		t.Fatalf("non-forced semantic injection status=%d body=%s want=%d", record.Code, record.Body.String(), http.StatusBadRequest)
	}
}

func TestAtlasOwnerEnrichHTTPParsesMinorAndMajorRequests(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	now := start.Add(7 * 24 * time.Hour)
	item := atlasMaturationHTTPItem("enrich", start)
	item.MaturationState = domainbacklog.MaturationStatePromoted
	store := &atlasHTTPItemStore{items: []domainbacklog.Item{item}}
	service := appbacklog.NewService(store, nil).WithClock(func() time.Time { return now })
	handler := NewAtlasHandler(service, "ren", []byte(atlasOwnerHTTPToken))

	minor := atlasOwnerHTTPPost(t, handler, atlasOwnerHTTPToken, "/v1/atlas/items/enrich/enrich", `{"source_refs":[{"type":"url","locator":"https://example.test/new"}],"related_ids":["related"],"relation_refs":["relation"],"body":"updated body","background":"updated background","priority":"high"}`)
	if minor.Code != http.StatusOK {
		t.Fatalf("minor enrich status=%d body=%s", minor.Code, minor.Body.String())
	}
	var minorResponse struct {
		Item domainbacklog.Item `json:"item"`
	}
	if err := json.Unmarshal(minor.Body.Bytes(), &minorResponse); err != nil {
		t.Fatalf("decode minor enrich: %v body=%s", err, minor.Body.String())
	}
	if minorResponse.Item.Body != "updated body" || minorResponse.Item.Background != "updated background" || minorResponse.Item.Priority != "high" || len(minorResponse.Item.SourceRefs) != 2 || !reflect.DeepEqual(minorResponse.Item.RelatedIDs, []string{"related"}) || !reflect.DeepEqual(minorResponse.Item.RelationRefs, []string{"relation"}) {
		t.Fatalf("minor enrich fields=%+v", minorResponse.Item)
	}
	if minorResponse.Item.MaturationState != item.MaturationState || minorResponse.Item.MaturationStartedAt != item.MaturationStartedAt || minorResponse.Item.MaturationEligibleAt != item.MaturationEligibleAt {
		t.Fatalf("minor enrich reset maturation clocks: before=%+v after=%+v", item, minorResponse.Item)
	}

	major := atlasOwnerHTTPPost(t, handler, atlasOwnerHTTPToken, "/v1/atlas/items/enrich/enrich", `{"material_change":true,"material_change_reason":"architecture changed","body":"major body"}`)
	if major.Code != http.StatusOK {
		t.Fatalf("major enrich status=%d body=%s", major.Code, major.Body.String())
	}
	var majorResponse struct {
		Item domainbacklog.Item `json:"item"`
	}
	if err := json.Unmarshal(major.Body.Bytes(), &majorResponse); err != nil {
		t.Fatalf("decode major enrich: %v body=%s", err, major.Body.String())
	}
	if majorResponse.Item.MaturationState != domainbacklog.MaturationStateMaturation || majorResponse.Item.MaturationStartedAt != now.Format(time.RFC3339) || majorResponse.Item.MaturationEligibleAt != now.Add(7*24*time.Hour).Format(time.RFC3339) || majorResponse.Item.LastMaterialChangeAt != now.Format(time.RFC3339) || majorResponse.Item.Body != "major body" {
		t.Fatalf("major enrich did not reset maturation: %+v", majorResponse.Item)
	}
}

const atlasOwnerHTTPToken = "atlas-owner-token-012345678901234567890123"

func atlasMaturationHTTPItem(id string, start time.Time) domainbacklog.Item {
	return domainbacklog.Item{
		SchemaVersion:        domainbacklog.SchemaVersion2,
		ItemID:               id,
		FeatureID:            id,
		Title:                "Atlas maturation " + id,
		Purpose:              "validate Atlas maturation over HTTP",
		ConceptState:         domainbacklog.ConceptCandidate,
		DeliveryState:        domainbacklog.DeliveryNone,
		MaturationState:      domainbacklog.MaturationStateMaturation,
		MaturationStartedAt:  start.Format(time.RFC3339),
		MaturationEligibleAt: start.Add(7 * 24 * time.Hour).Format(time.RFC3339),
		CreatedAt:            start.Format(time.RFC3339),
		UpdatedAt:            start.Format(time.RFC3339),
		SourceRefs:           []domainbacklog.SourceRef{{Type: "test", Locator: id}},
	}
}

func atlasOwnerHTTPPost(t *testing.T, handler http.HandlerFunc, token, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+path, bytes.NewBufferString(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
	request.Header.Set("X-RenCrow-Interaction-Profile", "cmd-control")
	request.RemoteAddr = "127.0.0.1:43210"
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, request)
	return record
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
