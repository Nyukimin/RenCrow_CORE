package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appbacklog "github.com/Nyukimin/RenCrow_CORE/internal/application/backlog"
	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	featurebacklog "github.com/Nyukimin/RenCrow_CORE/internal/features/backlog"
	backlogpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/backlog"
	workstreampersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/workstream"
)

// TestAtlasHTTPBackfillAndSpecificationProjection exercises the actual Viewer
// HTTP route with the embedded canonical package and the real append-only
// backlog/workstream stores. The fixture is deliberately not referenced: the
// production import source is LoadBackfillPackage's embedded canonical data.
func TestAtlasHTTPBackfillAndSpecificationProjection(t *testing.T) {
	runtime := newAtlasLifecycleHTTPRuntime(t)
	pkg, err := featurebacklog.LoadBackfillPackage()
	if err != nil {
		t.Fatal(err)
	}

	first, err := runtime.service.ReconcileBackfill(context.Background(), pkg)
	if err != nil {
		t.Fatal(err)
	}
	if first.Imported != len(pkg.Items) || first.Updated != 0 || first.Skipped != 0 {
		t.Fatalf("canonical backfill report=%+v items=%d specs=%d", first, len(pkg.Items), len(pkg.SpecificationArtifacts))
	}
	items, err := runtime.items.List(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != len(pkg.Items) {
		t.Fatalf("canonical item count=%d want=%d", len(items), len(pkg.Items))
	}
	if got := countBackfillReceipts(t, runtime.items.Path()); got != 1 {
		t.Fatalf("canonical import receipt count=%d want=1", got)
	}

	second, err := runtime.service.ReconcileBackfill(context.Background(), pkg)
	if err != nil {
		t.Fatal(err)
	}
	if second.Imported != 0 || second.Updated != 0 || second.Skipped != len(pkg.Items) {
		t.Fatalf("idempotent canonical backfill report=%+v", second)
	}
	items, err = runtime.items.List(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != len(pkg.Items) || countBackfillReceipts(t, runtime.items.Path()) != 1 {
		t.Fatalf("second canonical import changed counts items=%d receipts=%d", len(items), countBackfillReceipts(t, runtime.items.Path()))
	}
	if got := len(pkg.SpecificationArtifacts); got == 0 {
		t.Fatal("canonical package has no specification artifacts")
	}

	status, body := runtime.get(t, "/viewer/atlas/items/atlas:atlas.lifecycle")
	if status != http.StatusOK {
		t.Fatalf("Atlas item detail status=%d body=%s", status, body)
	}
	var detail struct {
		Item                   domainbacklog.Item                    `json:"item"`
		ResolvedSpecifications []domainbacklog.SpecificationArtifact `json:"resolved_specifications"`
	}
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatalf("decode Atlas item detail: %v body=%s", err, body)
	}
	if detail.Item.ItemID != "atlas:atlas.lifecycle" || detail.Item.Purpose == "" || detail.Item.Problem == "" || detail.Item.Idea == "" || detail.Item.Background == "" {
		t.Fatalf("lossless design card detail=%+v", detail.Item)
	}
	if len(detail.Item.SourceRefs) == 0 || detail.Item.SourceRefs[0].Strength == "" {
		t.Fatalf("source strength/refs missing: %+v", detail.Item.SourceRefs)
	}
	if len(detail.Item.SpecificationRefs) == 0 || len(detail.ResolvedSpecifications) != len(detail.Item.SpecificationRefs) {
		t.Fatalf("specification refs unresolved item=%+v resolved=%d", detail.Item.SpecificationRefs, len(detail.ResolvedSpecifications))
	}
	for _, artifact := range detail.ResolvedSpecifications {
		if !artifact.BodyAvailable || strings.TrimSpace(artifact.Content) == "" || strings.TrimSpace(artifact.ContentSHA256) == "" {
			t.Fatalf("specification body/hash unavailable: %+v", artifact)
		}
	}
}

// TestAtlasHTTPBackfillReconcilePreservesCompletedRevision reproduces the
// production restart path: a completed runtime item exists, startup reconciles
// the embedded Backfill package, and Viewer must still match the same revision
// to its closure receipt.
func TestAtlasHTTPBackfillReconcilePreservesCompletedRevision(t *testing.T) {
	runtime := newAtlasLifecycleHTTPRuntime(t)
	pkg, err := featurebacklog.LoadBackfillPackage()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.service.ReconcileBackfill(context.Background(), pkg); err != nil {
		t.Fatal(err)
	}
	item, found, err := runtime.items.FindByID(context.Background(), "atlas:atlas.lifecycle")
	if err != nil || !found {
		t.Fatalf("lifecycle item lookup found=%v err=%v", found, err)
	}
	item.DeliveryState = domainbacklog.DeliveryDone
	item.ImplementationUnit = "atlas-lifecycle-v1"
	item.ImplementationRevision = 2
	item.EvidenceRefs = []domainbacklog.EvidenceRef{{
		Stage: domainbacklog.DeliveryLiveVerified, Kind: "production_smoke", Ref: "isolated-smoke", Verified: true,
	}}
	if err := runtime.items.Save(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)
	if err := runtime.workstream.SaveClosureReceipt(context.Background(), domainworkstream.ClosureReceipt{
		ReceiptID: "isolated-closure", IdempotencyKey: "atlas-lifecycle-v1:2:DONE",
		UnitID: "atlas-lifecycle-v1", ItemID: item.ItemID, ImplementationRevision: 2,
		Phase: domainworkstream.ClosurePhaseDone, Status: domainworkstream.ClosureStatusCompleted,
		LeaseReleased: true, CreatedAt: now, UpdatedAt: now, CompletedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	reconciled, err := runtime.service.ReconcileBackfill(context.Background(), pkg)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.Imported != 0 || reconciled.Updated != 0 || reconciled.Skipped != len(pkg.Items) {
		t.Fatalf("completed runtime reconcile report=%+v", reconciled)
	}

	status, body := runtime.get(t, "/viewer/atlas/items/atlas:atlas.lifecycle")
	if status != http.StatusOK {
		t.Fatalf("item detail status=%d body=%s", status, body)
	}
	var detail struct {
		Item domainbacklog.Item `json:"item"`
	}
	decodeAtlasLifecycleJSON(t, body, &detail)
	if detail.Item.DeliveryState != domainbacklog.DeliveryDone || detail.Item.ImplementationRevision != 2 {
		t.Fatalf("reconcile rolled back completed lifecycle: %+v", detail.Item)
	}

	status, body = runtime.get(t, "/viewer/atlas")
	if status != http.StatusOK {
		t.Fatalf("projection status=%d body=%s", status, body)
	}
	var projection appbacklog.Projection
	decodeAtlasLifecycleJSON(t, body, &projection)
	if len(projection.Current) != 1 || projection.Current[0].ItemID != item.ItemID || projection.Current[0].ImplementationRevision != 2 {
		t.Fatalf("completed lifecycle missing from Current: %+v", projection.Current)
	}
}

// TestAtlasHTTPDesignCardLifecycleAndFreezeResolution intentionally sends the
// full Design Card through the normal owner intake route. It must not silently
// seed missing fields through the store: if the route rejects Problem/Idea/
// Background/ExpectedEffect, this is a contract gap that blocks the rest of
// the HTTP lifecycle proof.
func TestAtlasHTTPDesignCardLifecycleAndFreezeResolution(t *testing.T) {
	runtime := newAtlasLifecycleHTTPRuntime(t)

	first := runtime.intakeDesignCard(t, "http-lifecycle-blocked", "HTTP lifecycle blocked unit")
	first = runtime.candidateAndAdopt(t, first.ItemID)
	runtime.verifier.expected[first.ItemID] = atlasVerifierExpectation{itemID: first.ItemID, unitID: first.ImplementationUnit, revision: first.ImplementationRevision}

	status, body := runtime.post(t, "/v1/atlas/items/"+first.ItemID+"/revise", map[string]any{
		"request_id":        "http-blocked-request",
		"expected_revision": first.ImplementationRevision,
		"delivery_state":    domainbacklog.DeliveryBlocked,
		"reason":            "worker_failure",
		"evidence_refs": []domainbacklog.EvidenceRef{{
			Stage: domainbacklog.DeliveryBlocked, Kind: "worker_failure", Ref: "worker-failure-http-1", Passed: true,
		}},
	})
	if status != http.StatusOK {
		t.Fatalf("BLOCKED revise status=%d body=%s", status, body)
	}
	var blockedBody struct {
		Item domainbacklog.Item `json:"item"`
	}
	decodeAtlasLifecycleJSON(t, body, &blockedBody)
	if blockedBody.Item.DeliveryState != domainbacklog.DeliveryBlocked {
		t.Fatalf("blocked item=%+v", blockedBody.Item)
	}

	status, body = runtime.get(t, "/viewer/atlas/queue-freezes")
	if status != http.StatusOK {
		t.Fatalf("queue freeze list status=%d body=%s", status, body)
	}
	var freezeList struct {
		Freezes []domainworkstream.QueueFreeze `json:"queue_freezes"`
	}
	decodeAtlasLifecycleJSON(t, body, &freezeList)
	if len(freezeList.Freezes) != 1 || freezeList.Freezes[0].Status != domainworkstream.QueueFreezeActive {
		t.Fatalf("durable active queue freeze=%+v", freezeList.Freezes)
	}
	freeze := freezeList.Freezes[0]

	second := runtime.intakeDesignCard(t, "http-lifecycle-replacement", "HTTP lifecycle replacement")
	status, body = runtime.post(t, "/v1/atlas/items/"+second.ItemID+"/candidate", map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("replacement candidate status=%d body=%s", status, body)
	}
	status, body = runtime.post(t, "/v1/atlas/items/"+second.ItemID+"/revalidate", map[string]any{
		"decision": "PROMOTE", "reason": "replacement owner review",
		"forced": true, "bypass_reason": domainbacklog.MaturationBypassRuntimeContinuity,
	})
	if status != http.StatusOK {
		t.Fatalf("replacement revalidate status=%d body=%s", status, body)
	}
	var adoption appbacklog.AdoptionResult
	status, body = runtime.post(t, "/v1/atlas/items/"+second.ItemID+"/adopt", map[string]any{"reason": "replace blocked unit"})
	if status == http.StatusOK {
		decodeAtlasLifecycleJSON(t, body, &adoption)
	} else {
		t.Fatalf("replacement adopt status=%d body=%s", status, body)
	}
	if adoption.LeaseAcquired {
		t.Fatalf("active queue freeze must prevent replacement lease: %+v", adoption)
	}

	// Intake/adopt has no field for the replacement relation yet. Preserve the
	// HTTP-created/adopted unit and seed only that setup relation through the
	// owner backlog store; freeze resolution itself remains HTTP-only below.
	replacement, found, err := runtime.items.FindByID(context.Background(), second.ItemID)
	if err != nil || !found {
		t.Fatalf("replacement lookup found=%v err=%v", found, err)
	}
	replacement.SupersedesUnitID = first.ImplementationUnit
	if err := runtime.items.Save(context.Background(), replacement); err != nil {
		t.Fatalf("seed replacement supersedes relation: %v", err)
	}

	resolutionBody := map[string]any{
		"request_id":               "http-freeze-resolution-1",
		"expected_freeze_revision": freeze.FreezeRevision,
		"replacement_unit_id":      replacement.ImplementationUnit,
		"supersedes_unit_id":       first.ImplementationUnit,
		"blocker_resolution_refs": []domainbacklog.EvidenceRef{{
			Stage: domainbacklog.DeliveryBlocked, Kind: "blocker_resolution", Ref: "blocker-resolution-http-1", Passed: true,
		}},
	}
	status, body = runtime.post(t, "/v1/atlas/queue-freezes/"+freeze.FreezeID+"/resolve", resolutionBody)
	if status != http.StatusOK {
		t.Fatalf("resolve freeze status=%d body=%s", status, body)
	}
	var resolution struct {
		Freeze   domainworkstream.QueueFreeze         `json:"freeze"`
		Lease    domainworkstream.ImplementationLease `json:"lease"`
		Acquired bool                                 `json:"acquired"`
	}
	decodeAtlasLifecycleJSON(t, body, &resolution)
	if !resolution.Acquired || resolution.Freeze.Status != domainworkstream.QueueFreezeResolved || resolution.Freeze.SupersedesUnitID != first.ImplementationUnit || resolution.Freeze.ReplacementUnitID != replacement.ImplementationUnit || len(resolution.Freeze.BlockerResolutionRefs) != 1 || !resolution.Freeze.BlockerResolutionRefs[0].IsVerified() {
		t.Fatalf("incomplete HTTP freeze resolution=%+v", resolution)
	}
	if resolution.Lease.HolderUnitID != replacement.ImplementationUnit {
		t.Fatalf("replacement lease=%+v", resolution.Lease)
	}

	itemCountBeforeReplay := countBacklogItems(t, runtime.items)
	freezeRecordCountBeforeReplay := countJSONLLines(t, filepath.Join(runtime.workstreamRoot, "queue_freeze.jsonl"))
	status, body = runtime.post(t, "/v1/atlas/queue-freezes/"+freeze.FreezeID+"/resolve", resolutionBody)
	if status != http.StatusOK {
		t.Fatalf("exact resolution replay status=%d body=%s", status, body)
	}
	if countBacklogItems(t, runtime.items) != itemCountBeforeReplay || countJSONLLines(t, filepath.Join(runtime.workstreamRoot, "queue_freeze.jsonl")) != freezeRecordCountBeforeReplay {
		t.Fatalf("exact replay changed item/freeze counts items=%d/%d freezes=%d/%d", countBacklogItems(t, runtime.items), itemCountBeforeReplay, countJSONLLines(t, filepath.Join(runtime.workstreamRoot, "queue_freeze.jsonl")), freezeRecordCountBeforeReplay)
	}

	conflicting := map[string]any{}
	for key, value := range resolutionBody {
		conflicting[key] = value
	}
	conflicting["blocker_resolution_refs"] = []domainbacklog.EvidenceRef{{
		Stage: domainbacklog.DeliveryBlocked, Kind: "blocker_resolution", Ref: "different-resolution", Passed: true,
	}}
	status, body = runtime.post(t, "/v1/atlas/queue-freezes/"+freeze.FreezeID+"/resolve", conflicting)
	if status != http.StatusConflict {
		t.Fatalf("conflicting resolution status=%d body=%s", status, body)
	}
}

type atlasLifecycleHTTPRuntime struct {
	client         *http.Client
	server         *httptest.Server
	items          *backlogpersistence.JSONLStore
	workstream     *workstreampersistence.JSONLStore
	workstreamRoot string
	service        *appbacklog.Service
	verifier       *atlasStrictHTTPVerifier
	token          string
}

func newAtlasLifecycleHTTPRuntime(t *testing.T) *atlasLifecycleHTTPRuntime {
	t.Helper()
	root := atlasLifecycleTempRoot(t)
	workstreamRoot := filepath.Join(root, "workstream")
	items := backlogpersistence.NewJSONLStore(filepath.Join(root, "backlog.jsonl"))
	verifier := &atlasStrictHTTPVerifier{expected: map[string]atlasVerifierExpectation{}}
	workstream := workstreampersistence.NewJSONLStore(workstreamRoot)
	service := appbacklog.NewService(items, workstream).WithEvidenceVerifier(verifier)
	token := "atlas-http-owner-token-012345678901234567890123"
	server := httptest.NewServer(NewAtlasHandler(service, "ren", []byte(token)))
	runtime := &atlasLifecycleHTTPRuntime{
		client: http.DefaultClient, server: server, items: items, workstream: workstream, workstreamRoot: workstreamRoot,
		service: service, verifier: verifier, token: token,
	}
	t.Cleanup(server.Close)
	return runtime
}

func atlasLifecycleTempRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join("Tmp", "test-runtime")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(root, "atlas-lifecycle-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func (r *atlasLifecycleHTTPRuntime) post(t *testing.T, path string, payload any) (int, []byte) {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, r.server.URL+path, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+r.token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
	request.Header.Set("X-RenCrow-Interaction-Profile", "cmd-control")
	response, err := r.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, body
}

func (r *atlasLifecycleHTTPRuntime) get(t *testing.T, path string) (int, []byte) {
	t.Helper()
	response, err := r.client.Get(r.server.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, body
}

func (r *atlasLifecycleHTTPRuntime) intakeDesignCard(t *testing.T, itemID, title string) domainbacklog.Item {
	t.Helper()
	status, body := r.post(t, "/v1/atlas/intake", map[string]any{
		"item_id": itemID, "kind": "idea", "title": title,
		"purpose":         "prove the full Atlas design card survives the HTTP owner route",
		"problem":         "HTTP intake may drop the design memory needed for later recovery",
		"idea":            "carry the complete design card through canonical CORE intake",
		"background":      "the lifecycle is exercised through a real isolated Viewer runtime",
		"expected_effect": []string{"lossless design memory", "auditable lifecycle"},
		"category":        "Atlas", "source": "atlas-http-e2e",
		"source_refs": []domainbacklog.SourceRef{{Type: "test", Locator: "atlas-http-e2e/" + itemID, Strength: "direct_spec", CapturedAt: "2026-08-22T00:00:00Z", RawOrSummary: "typed HTTP E2E source"}},
	})
	if status < 200 || status >= 300 {
		t.Fatalf("lossless Design Card HTTP intake contract gap: status=%d body=%s", status, body)
	}
	var result appbacklog.IntakeResult
	decodeAtlasLifecycleJSON(t, body, &result)
	if result.Item.ItemID != itemID || result.Item.Purpose == "" || result.Item.Problem == "" || result.Item.Idea == "" || result.Item.Background == "" || len(result.Item.ExpectedEffect) == 0 {
		t.Fatalf("HTTP intake lost Design Card fields: %+v", result.Item)
	}
	return result.Item
}

func (r *atlasLifecycleHTTPRuntime) candidateAndAdopt(t *testing.T, itemID string) domainbacklog.Item {
	t.Helper()
	status, body := r.post(t, "/v1/atlas/items/"+itemID+"/candidate", map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("candidate status=%d body=%s", status, body)
	}
	status, body = r.post(t, "/v1/atlas/items/"+itemID+"/revalidate", map[string]any{
		"decision": "PROMOTE", "reason": "owner HTTP E2E maturation review",
		"forced": true, "bypass_reason": domainbacklog.MaturationBypassRuntimeContinuity,
	})
	if status != http.StatusOK {
		t.Fatalf("revalidate status=%d body=%s", status, body)
	}
	status, body = r.post(t, "/v1/atlas/items/"+itemID+"/adopt", map[string]any{"reason": "owner HTTP E2E adoption"})
	if status != http.StatusOK {
		t.Fatalf("adopt status=%d body=%s", status, body)
	}
	var adoption appbacklog.AdoptionResult
	decodeAtlasLifecycleJSON(t, body, &adoption)
	return adoption.Item
}

type atlasVerifierExpectation struct {
	itemID   string
	unitID   string
	revision int
}

type atlasStrictHTTPVerifier struct {
	expected map[string]atlasVerifierExpectation
}

func (v *atlasStrictHTTPVerifier) Verify(_ context.Context, request appbacklog.EvidenceVerificationRequest) (bool, error) {
	want, ok := v.expected[request.ItemID]
	if !ok {
		return false, fmt.Errorf("unexpected HTTP evidence item %q", request.ItemID)
	}
	if request.ItemID != want.itemID || request.ImplementationUnitID != want.unitID || request.ImplementationRevision != want.revision || strings.TrimSpace(request.TargetDeliveryState) == "" {
		return false, fmt.Errorf("typed HTTP evidence context mismatch: %+v want=%+v", request, want)
	}
	if !strings.EqualFold(request.Ref.Stage, request.TargetDeliveryState) || strings.TrimSpace(request.Ref.Ref) == "" {
		return false, fmt.Errorf("HTTP evidence stage/ref mismatch: %+v", request)
	}
	return true, nil
}

func decodeAtlasLifecycleJSON(t *testing.T, body []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(body, target); err != nil {
		t.Fatalf("decode HTTP JSON: %v body=%s", err, body)
	}
}

func countBackfillReceipts(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		var receipt domainbacklog.BackfillImportReceipt
		if json.Unmarshal(bytes.TrimSpace(line), &receipt) == nil && receipt.RecordType == "atlas_backfill_import" {
			count++
		}
	}
	return count
}

func countBacklogItems(t *testing.T, store *backlogpersistence.JSONLStore) int {
	t.Helper()
	items, err := store.List(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	return len(items)
}

func countJSONLLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) > 0 {
			count++
		}
	}
	return count
}
