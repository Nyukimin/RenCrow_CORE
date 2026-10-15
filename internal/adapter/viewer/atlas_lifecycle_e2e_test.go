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
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	appbacklog "github.com/Nyukimin/RenCrow_CORE/internal/application/backlog"
	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	featurebacklog "github.com/Nyukimin/RenCrow_CORE/internal/features/backlog"
	backlogpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/backlog"
	workstreampersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
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
	if detail.Item.BacklogItemID != "atlas:atlas.lifecycle" || detail.Item.Purpose == "" || detail.Item.Problem == "" || detail.Item.Idea == "" || detail.Item.Background == "" {
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
		ReceiptID: modulecore.ReceiptID("rcp_00000000-0000-5000-8000-000000000030"), IdempotencyKey: "atlas-lifecycle-v1:2:DONE",
		ActionID: modulecore.NewActionID(),
		UnitID:   "atlas-lifecycle-v1", BacklogItemID: item.BacklogItemID, ImplementationRevision: 2,
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
	if len(projection.Current) != 1 || projection.Current[0].BacklogItemID != item.BacklogItemID || projection.Current[0].ImplementationRevision != 2 {
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
	first = runtime.candidateAndAdopt(t, string(first.BacklogItemID))
	runtime.verifier.expected[string(first.BacklogItemID)] = atlasVerifierExpectation{itemID: string(first.BacklogItemID), unitID: first.ImplementationUnit, revision: first.ImplementationRevision}

	status, body := runtime.post(t, "/v1/atlas/items/"+string(first.BacklogItemID)+"/revise", map[string]any{
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
	status, body = runtime.post(t, "/v1/atlas/items/"+string(second.BacklogItemID)+"/candidate", map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("replacement candidate status=%d body=%s", status, body)
	}
	status, body = runtime.post(t, "/v1/atlas/items/"+string(second.BacklogItemID)+"/revalidate", map[string]any{
		"decision": "PROMOTE", "reason": "replacement owner review",
		"forced": true, "bypass_reason": domainbacklog.MaturationBypassRuntimeContinuity,
	})
	if status != http.StatusOK {
		t.Fatalf("replacement revalidate status=%d body=%s", status, body)
	}
	var adoption appbacklog.AdoptionResult
	status, body = runtime.post(t, "/v1/atlas/items/"+string(second.BacklogItemID)+"/adopt", map[string]any{"reason": "replace blocked unit"})
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
	replacement, found, err := runtime.items.FindByID(context.Background(), string(second.BacklogItemID))
	if err != nil || !found {
		t.Fatalf("replacement lookup found=%v err=%v", found, err)
	}
	replacement.SupersedesUnitID = first.ImplementationUnit
	if err := runtime.items.Save(context.Background(), replacement); err != nil {
		t.Fatalf("seed replacement supersedes relation: %v", err)
	}

	resolutionBody := map[string]any{
		"action_id":                "act_00000000-0000-5000-8000-000000000006",
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

func TestAtlasHTTPResolveQueueFreezeCancelledDisposition(t *testing.T) {
	runtime := newAtlasLifecycleHTTPRuntime(t)
	blocked := runtime.intakeDesignCard(t, "http-lifecycle-cancelled", "HTTP lifecycle cancelled retry")
	blocked = runtime.candidateAndAdopt(t, string(blocked.BacklogItemID))
	runtime.verifier.expected[string(blocked.BacklogItemID)] = atlasVerifierExpectation{
		itemID: string(blocked.BacklogItemID), unitID: blocked.ImplementationUnit, revision: blocked.ImplementationRevision,
	}
	runtime.service.WithDevelopmentEventSink(runtime.events)

	status, body := runtime.post(t, "/v1/atlas/items/"+string(blocked.BacklogItemID)+"/revise", map[string]any{
		"request_id":        "http-cancelled-blocked-request",
		"expected_revision": blocked.ImplementationRevision,
		"delivery_state":    domainbacklog.DeliveryBlocked,
		"reason":            "worker_failure",
		"evidence_refs": []domainbacklog.EvidenceRef{{
			Stage: domainbacklog.DeliveryBlocked, Kind: "worker_failure", Ref: "worker-failure-http-cancelled", Passed: true,
		}},
	})
	if status != http.StatusOK {
		t.Fatalf("BLOCKED revise status=%d body=%s", status, body)
	}

	status, body = runtime.get(t, "/viewer/atlas/queue-freezes")
	if status != http.StatusOK {
		t.Fatalf("queue freeze list status=%d body=%s", status, body)
	}
	var freezeList struct {
		Freezes []domainworkstream.QueueFreeze `json:"queue_freezes"`
	}
	decodeAtlasLifecycleJSON(t, body, &freezeList)
	if len(freezeList.Freezes) != 1 {
		t.Fatalf("active queue freezes=%+v", freezeList.Freezes)
	}
	freeze := freezeList.Freezes[0]
	blockedBefore, found, err := runtime.items.FindByID(context.Background(), string(blocked.BacklogItemID))
	if err != nil || !found {
		t.Fatalf("blocked item lookup found=%v err=%v", found, err)
	}

	request := map[string]any{
		"action_id":                modulecore.NewActionID(),
		"expected_freeze_revision": freeze.FreezeRevision,
		"disposition":              "cancelled",
		"blocked_unit_id":          blocked.ImplementationUnit,
		"blocked_revision":         blocked.ImplementationRevision,
		"reason":                   "system owner withdrew retry",
	}
	freezePath := "/v1/atlas/queue-freezes/" + freeze.FreezeID + "/resolve"

	duplicateEvidence := blockedBefore
	duplicateEvidence.EvidenceRefs = append(append([]domainbacklog.EvidenceRef(nil), blockedBefore.EvidenceRefs...), blockedBefore.EvidenceRefs[0])
	if err := runtime.items.Save(context.Background(), duplicateEvidence); err != nil {
		t.Fatalf("seed duplicated current evidence: %v", err)
	}
	if status, body := runtime.post(t, freezePath, request); status != http.StatusBadRequest {
		t.Fatalf("cancel with duplicated current evidence status=%d body=%s", status, body)
	}
	if err := runtime.items.Save(context.Background(), blockedBefore); err != nil {
		t.Fatalf("restore blocked item after duplicate evidence check: %v", err)
	}
	blockedBefore, found, err = runtime.items.FindByID(context.Background(), string(blocked.BacklogItemID))
	if err != nil || !found {
		t.Fatalf("blocked item lookup after evidence restore found=%v err=%v", found, err)
	}
	blockedBeforeJSON, err := json.Marshal(blockedBefore)
	if err != nil {
		t.Fatalf("encode blocked item baseline after evidence restore: %v", err)
	}

	stageKey := fmt.Sprintf("%s:%d:%s", blocked.ImplementationUnit, blocked.ImplementationRevision, domainbacklog.DeliveryBlocked)
	stageReceipt, stageFound, err := runtime.workstream.FindStageRunReceipt(context.Background(), stageKey)
	if err != nil || !stageFound {
		t.Fatalf("find BLOCKED stage receipt found=%v err=%v", stageFound, err)
	}
	for _, eventID := range []modulecore.EventID{freeze.TransitionEventID, stageReceipt.TransitionEventID} {
		runtime.events.mu.Lock()
		event, found := runtime.events.events[eventID]
		if !found {
			runtime.events.mu.Unlock()
			t.Fatalf("canonical event %q missing from test sink", eventID)
		}
		original := event
		event.ArtifactID = "transition:not-the-canonical-identity"
		runtime.events.events[eventID] = event
		runtime.events.mu.Unlock()

		if status, body := runtime.post(t, freezePath, request); status != http.StatusBadRequest {
			t.Fatalf("cancel with non-canonical event artifact %q status=%d body=%s", eventID, status, body)
		}

		runtime.events.mu.Lock()
		runtime.events.events[eventID] = original
		runtime.events.mu.Unlock()
	}
	runtime.events.mu.Lock()
	_, found = runtime.events.events[freeze.TransitionEventID]
	if !found {
		runtime.events.mu.Unlock()
		t.Fatalf("freeze transition event %q missing before declared-missing test", freeze.TransitionEventID)
	}
	delete(runtime.events.events, freeze.TransitionEventID)
	runtime.events.mu.Unlock()
	if status, body := runtime.post(t, freezePath, request); status != http.StatusBadRequest {
		t.Fatalf("cancel with undeclared missing transition event status=%d body=%s", status, body)
	}
	request["expected_missing_transition_event_ids"] = []modulecore.EventID{freeze.TransitionEventID}
	legacyFreeze := freeze
	legacyFreeze.Status = ""
	legacyFreezeJSON, err := json.Marshal(legacyFreeze)
	if err != nil {
		t.Fatalf("encode legacy freeze fixture: %v", err)
	}
	freezeFile, err := os.OpenFile(filepath.Join(runtime.workstreamRoot, "queue_freeze.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open legacy freeze fixture: %v", err)
	}
	if _, err := freezeFile.Write(append(legacyFreezeJSON, '\n')); err != nil {
		_ = freezeFile.Close()
		t.Fatalf("append legacy freeze fixture: %v", err)
	}
	if err := freezeFile.Close(); err != nil {
		t.Fatalf("close legacy freeze fixture: %v", err)
	}

	heldLease := domainworkstream.ImplementationLease{
		LeaseName: domainbacklog.ImplementationLeaseName, HolderUnitID: "atlas-cancellation-lease-holder",
		HolderWorkstreamID: "atlas-cancellation-lease-workstream", AcquiredAt: time.Now().UTC(), HeartbeatAt: time.Now().UTC(),
	}
	leaseJSON, err := json.Marshal(heldLease)
	if err != nil {
		t.Fatalf("encode held lease fixture: %v", err)
	}
	leaseFile, err := os.OpenFile(filepath.Join(runtime.workstreamRoot, "implementation_lease.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open held lease fixture: %v", err)
	}
	if _, err := leaseFile.Write(append(leaseJSON, '\n')); err != nil {
		_ = leaseFile.Close()
		t.Fatalf("append held lease fixture: %v", err)
	}
	if err := leaseFile.Close(); err != nil {
		t.Fatalf("close held lease fixture: %v", err)
	}
	if status, body := runtime.post(t, freezePath, request); status != http.StatusLocked {
		t.Fatalf("cancel with held implementation lease status=%d want=%d body=%s", status, http.StatusLocked, body)
	}
	if err := runtime.workstream.ReleaseImplementationLease(context.Background(), heldLease.LeaseName, heldLease.HolderUnitID); err != nil {
		t.Fatalf("release held lease fixture: %v", err)
	}

	status, body = runtime.post(t, freezePath, request)
	if status != http.StatusOK {
		t.Fatalf("cancel queue freeze status=%d body=%s", status, body)
	}
	var response struct {
		Freeze   map[string]json.RawMessage           `json:"freeze"`
		Lease    domainworkstream.ImplementationLease `json:"lease"`
		Acquired bool                                 `json:"acquired"`
	}
	decodeAtlasLifecycleJSON(t, body, &response)
	var freezeStatus string
	var disposition string
	var cancellationReason string
	var actionID string
	var payloadHash string
	var observedMissing []modulecore.EventID
	for key, target := range map[string]*string{
		"status": &freezeStatus, "resolution_disposition": &disposition,
		"cancellation_reason": &cancellationReason, "action_id": &actionID,
		"resolution_payload_hash": &payloadHash,
	} {
		if err := json.Unmarshal(response.Freeze[key], target); err != nil {
			t.Fatalf("decode freeze.%s: %v body=%s", key, err, body)
		}
	}
	if err := json.Unmarshal(response.Freeze["observed_missing_transition_event_ids"], &observedMissing); err != nil {
		t.Fatalf("decode observed missing transition event ids: %v body=%s", err, body)
	}
	if freezeStatus != domainworkstream.QueueFreezeResolved || disposition != "cancelled" || cancellationReason != "system owner withdrew retry" || actionID != string(request["action_id"].(modulecore.ActionID)) || payloadHash == "" {
		t.Fatalf("incomplete cancelled freeze receipt status=%q disposition=%q reason=%q action=%q hash=%q", freezeStatus, disposition, cancellationReason, actionID, payloadHash)
	}
	if len(observedMissing) != 1 || observedMissing[0] != freeze.TransitionEventID {
		t.Fatalf("observed missing transition event ids=%v want [%s]", observedMissing, freeze.TransitionEventID)
	}
	if response.Acquired || response.Lease.HolderUnitID != "" {
		t.Fatalf("cancelled resolution acquired a lease: %+v", response)
	}

	blockedAfter, found, err := runtime.items.FindByID(context.Background(), string(blocked.BacklogItemID))
	if err != nil || !found {
		t.Fatalf("blocked item after cancellation lookup found=%v err=%v", found, err)
	}
	blockedAfterJSON, err := json.Marshal(blockedAfter)
	if err != nil {
		t.Fatalf("encode blocked item after cancellation: %v", err)
	}
	if !bytes.Equal(blockedBeforeJSON, blockedAfterJSON) {
		t.Fatalf("cancellation mutated blocked item before=%s after=%s", blockedBeforeJSON, blockedAfterJSON)
	}

	status, replayBody := runtime.post(t, freezePath, request)
	if status != http.StatusOK || !bytes.Equal(body, replayBody) {
		t.Fatalf("exact cancellation replay status=%d body=%s first=%s", status, replayBody, body)
	}
	conflicting := map[string]any{}
	for key, value := range request {
		conflicting[key] = value
	}
	conflicting["reason"] = "different cancellation reason"
	status, body = runtime.post(t, freezePath, conflicting)
	if status != http.StatusConflict {
		t.Fatalf("conflicting cancellation status=%d body=%s", status, body)
	}
}

func TestAtlasHTTPResolveQueueFreezeMissingTransitionEventContract(t *testing.T) {
	tests := []struct {
		name               string
		missingEvents      func(atlasCancellationHTTPFixture) []modulecore.EventID
		expected           func(atlasCancellationHTTPFixture) []modulecore.EventID
		declare            bool
		readError          bool
		wantStatus         int
		differentSetReplay bool
	}{
		{
			name:     "all-present-empty",
			expected: func(atlasCancellationHTTPFixture) []modulecore.EventID { return []modulecore.EventID{} },
			declare:  true, wantStatus: http.StatusOK,
		},
		{
			name: "freeze-event-missing-explicitly",
			missingEvents: func(f atlasCancellationHTTPFixture) []modulecore.EventID {
				return []modulecore.EventID{f.freeze.TransitionEventID}
			},
			expected: func(f atlasCancellationHTTPFixture) []modulecore.EventID {
				return []modulecore.EventID{f.freeze.TransitionEventID}
			},
			declare: true, wantStatus: http.StatusOK, differentSetReplay: true,
		},
		{
			name: "stage-event-missing-explicitly",
			missingEvents: func(f atlasCancellationHTTPFixture) []modulecore.EventID {
				return []modulecore.EventID{f.stageReceipt.TransitionEventID}
			},
			expected: func(f atlasCancellationHTTPFixture) []modulecore.EventID {
				return []modulecore.EventID{f.stageReceipt.TransitionEventID}
			},
			declare: true, wantStatus: http.StatusOK,
		},
		{
			name: "both-events-missing-explicitly-sorted",
			missingEvents: func(f atlasCancellationHTTPFixture) []modulecore.EventID {
				return []modulecore.EventID{f.freeze.TransitionEventID, f.stageReceipt.TransitionEventID}
			},
			expected: func(f atlasCancellationHTTPFixture) []modulecore.EventID {
				return sortedAtlasEventIDs([]modulecore.EventID{f.freeze.TransitionEventID, f.stageReceipt.TransitionEventID})
			},
			declare: true, wantStatus: http.StatusOK,
		},
		{
			name: "missing-event-undeclared",
			missingEvents: func(f atlasCancellationHTTPFixture) []modulecore.EventID {
				return []modulecore.EventID{f.freeze.TransitionEventID}
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "unknown-canonical-id",
			missingEvents: func(f atlasCancellationHTTPFixture) []modulecore.EventID {
				return []modulecore.EventID{f.freeze.TransitionEventID}
			},
			expected: func(atlasCancellationHTTPFixture) []modulecore.EventID {
				return []modulecore.EventID{modulecore.NewEventID()}
			},
			declare: true, wantStatus: http.StatusBadRequest,
		},
		{
			name: "malformed-id",
			expected: func(atlasCancellationHTTPFixture) []modulecore.EventID {
				return []modulecore.EventID{"not-an-event-id"}
			},
			declare: true, wantStatus: http.StatusBadRequest,
		},
		{
			name: "duplicate-id",
			missingEvents: func(f atlasCancellationHTTPFixture) []modulecore.EventID {
				return []modulecore.EventID{f.freeze.TransitionEventID}
			},
			expected: func(f atlasCancellationHTTPFixture) []modulecore.EventID {
				return []modulecore.EventID{f.freeze.TransitionEventID, f.freeze.TransitionEventID}
			},
			declare: true, wantStatus: http.StatusBadRequest,
		},
		{
			name: "unsorted-two-id-list",
			missingEvents: func(f atlasCancellationHTTPFixture) []modulecore.EventID {
				return []modulecore.EventID{f.freeze.TransitionEventID, f.stageReceipt.TransitionEventID}
			},
			expected: func(f atlasCancellationHTTPFixture) []modulecore.EventID {
				ids := sortedAtlasEventIDs([]modulecore.EventID{f.freeze.TransitionEventID, f.stageReceipt.TransitionEventID})
				return []modulecore.EventID{ids[1], ids[0]}
			},
			declare: true, wantStatus: http.StatusBadRequest,
		},
		{
			name: "different-observed-set",
			missingEvents: func(f atlasCancellationHTTPFixture) []modulecore.EventID {
				return []modulecore.EventID{f.freeze.TransitionEventID}
			},
			expected: func(f atlasCancellationHTTPFixture) []modulecore.EventID {
				return []modulecore.EventID{f.stageReceipt.TransitionEventID}
			},
			declare: true, wantStatus: http.StatusBadRequest,
		},
		{
			name:     "reader-error-is-not-missing",
			expected: func(atlasCancellationHTTPFixture) []modulecore.EventID { return []modulecore.EventID{} },
			declare:  true, readError: true, wantStatus: http.StatusBadRequest,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			fixture := newAtlasCancellationHTTPFixture(t, test.name)
			if test.missingEvents != nil {
				for _, eventID := range test.missingEvents(fixture) {
					fixture.runtime.events.mu.Lock()
					if _, found := fixture.runtime.events.events[eventID]; !found {
						fixture.runtime.events.mu.Unlock()
						t.Fatalf("test event %q was already missing", eventID)
					}
					delete(fixture.runtime.events.events, eventID)
					fixture.runtime.events.mu.Unlock()
				}
			}
			if test.readError {
				fixture.runtime.events.mu.Lock()
				fixture.runtime.events.readErr = fmt.Errorf("canonical event log unavailable")
				fixture.runtime.events.mu.Unlock()
			}
			request := map[string]any{
				"action_id": modulecore.NewActionID(), "expected_freeze_revision": fixture.freeze.FreezeRevision,
				"disposition": "cancelled", "blocked_unit_id": fixture.item.ImplementationUnit,
				"blocked_revision": fixture.item.ImplementationRevision, "reason": "system owner withdrew retry",
			}
			if test.declare {
				request["expected_missing_transition_event_ids"] = test.expected(fixture)
			}
			status, body := fixture.runtime.post(t, fixture.path, request)
			if status != test.wantStatus {
				t.Fatalf("cancel status=%d want=%d body=%s", status, test.wantStatus, body)
			}
			if status != http.StatusOK {
				freeze, found, err := fixture.runtime.workstream.GetQueueFreeze(context.Background(), fixture.freeze.FreezeID)
				if err != nil || !found || freeze.Status != domainworkstream.QueueFreezeActive || len(freeze.ObservedMissingTransitionEventIDs) != 0 {
					t.Fatalf("rejected cancellation changed freeze=%+v found=%v err=%v", freeze, found, err)
				}
				return
			}
			var response struct {
				Freeze   domainworkstream.QueueFreeze         `json:"freeze"`
				Lease    domainworkstream.ImplementationLease `json:"lease"`
				Acquired bool                                 `json:"acquired"`
			}
			decodeAtlasLifecycleJSON(t, body, &response)
			expected := test.expected(fixture)
			if !sameAtlasEventIDs(response.Freeze.ObservedMissingTransitionEventIDs, expected) || response.Acquired || response.Lease.HolderUnitID != "" {
				t.Fatalf("cancel receipt missing IDs=%v want=%v acquired=%v lease=%+v", response.Freeze.ObservedMissingTransitionEventIDs, expected, response.Acquired, response.Lease)
			}
			if test.differentSetReplay {
				request["expected_missing_transition_event_ids"] = []modulecore.EventID{}
				status, body = fixture.runtime.post(t, fixture.path, request)
				if status != http.StatusConflict {
					t.Fatalf("different missing-event replay status=%d body=%s", status, body)
				}
			}
		})
	}
}

func TestAtlasHTTPResolveQueueFreezeFourAllPresentEventsWithEmptyExpected(t *testing.T) {
	for index := 0; index < 4; index++ {
		index := index
		t.Run(fmt.Sprintf("freeze-%d", index+1), func(t *testing.T) {
			fixture := newAtlasCancellationHTTPFixture(t, fmt.Sprintf("all-present-%d", index+1))
			request := map[string]any{
				"action_id": modulecore.NewActionID(), "expected_freeze_revision": fixture.freeze.FreezeRevision,
				"disposition": "cancelled", "blocked_unit_id": fixture.item.ImplementationUnit,
				"blocked_revision": fixture.item.ImplementationRevision, "reason": "system owner withdrew retry",
				"expected_missing_transition_event_ids": []modulecore.EventID{},
			}
			status, body := fixture.runtime.post(t, fixture.path, request)
			if status != http.StatusOK {
				t.Fatalf("all-present cancellation status=%d body=%s", status, body)
			}
			var response struct {
				Freeze domainworkstream.QueueFreeze `json:"freeze"`
			}
			decodeAtlasLifecycleJSON(t, body, &response)
			if len(response.Freeze.ObservedMissingTransitionEventIDs) != 0 {
				t.Fatalf("all-present freeze observed missing Event IDs=%v", response.Freeze.ObservedMissingTransitionEventIDs)
			}
		})
	}
}

type atlasCancellationHTTPFixture struct {
	runtime      *atlasLifecycleHTTPRuntime
	item         domainbacklog.Item
	freeze       domainworkstream.QueueFreeze
	stageReceipt domainworkstream.StageRunReceipt
	path         string
}

func newAtlasCancellationHTTPFixture(t *testing.T, suffix string) atlasCancellationHTTPFixture {
	t.Helper()
	runtime := newAtlasLifecycleHTTPRuntime(t)
	item := runtime.intakeDesignCard(t, "http-cancel-missing-"+suffix, "HTTP cancellation missing Event "+suffix)
	item = runtime.candidateAndAdopt(t, string(item.BacklogItemID))
	runtime.verifier.expected[string(item.BacklogItemID)] = atlasVerifierExpectation{
		itemID: string(item.BacklogItemID), unitID: item.ImplementationUnit, revision: item.ImplementationRevision,
	}
	runtime.service.WithDevelopmentEventSink(runtime.events)
	status, body := runtime.post(t, "/v1/atlas/items/"+string(item.BacklogItemID)+"/revise", map[string]any{
		"request_id": "http-cancel-missing-" + suffix, "expected_revision": item.ImplementationRevision,
		"delivery_state": domainbacklog.DeliveryBlocked, "reason": "worker_failure",
		"evidence_refs": []domainbacklog.EvidenceRef{{
			Stage: domainbacklog.DeliveryBlocked, Kind: "worker_failure", Ref: "worker-failure-missing-" + suffix, Passed: true,
		}},
	})
	if status != http.StatusOK {
		t.Fatalf("BLOCKED revise status=%d body=%s", status, body)
	}
	status, body = runtime.get(t, "/viewer/atlas/queue-freezes")
	if status != http.StatusOK {
		t.Fatalf("queue freeze list status=%d body=%s", status, body)
	}
	var freezeList struct {
		Freezes []domainworkstream.QueueFreeze `json:"queue_freezes"`
	}
	decodeAtlasLifecycleJSON(t, body, &freezeList)
	if len(freezeList.Freezes) != 1 {
		t.Fatalf("queue freezes=%+v", freezeList.Freezes)
	}
	freeze := freezeList.Freezes[0]
	stageKey := fmt.Sprintf("%s:%d:%s", item.ImplementationUnit, item.ImplementationRevision, domainbacklog.DeliveryBlocked)
	stageReceipt, found, err := runtime.workstream.FindStageRunReceipt(context.Background(), stageKey)
	if err != nil || !found {
		t.Fatalf("BLOCKED stage receipt found=%v err=%v", found, err)
	}
	return atlasCancellationHTTPFixture{
		runtime: runtime, item: item, freeze: freeze, stageReceipt: stageReceipt,
		path: "/v1/atlas/queue-freezes/" + freeze.FreezeID + "/resolve",
	}
}

func sortedAtlasEventIDs(eventIDs []modulecore.EventID) []modulecore.EventID {
	sorted := append([]modulecore.EventID(nil), eventIDs...)
	sort.Slice(sorted, func(left, right int) bool { return sorted[left] < sorted[right] })
	return sorted
}

func sameAtlasEventIDs(left, right []modulecore.EventID) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func TestAtlasHTTPResolveQueueFreezeCancelledDispositionFailsClosed(t *testing.T) {
	runtime := newAtlasLifecycleHTTPRuntime(t)
	blocked := runtime.intakeDesignCard(t, "http-lifecycle-cancelled-fail-closed", "HTTP lifecycle cancellation fail closed")
	blocked = runtime.candidateAndAdopt(t, string(blocked.BacklogItemID))
	runtime.verifier.expected[string(blocked.BacklogItemID)] = atlasVerifierExpectation{
		itemID: string(blocked.BacklogItemID), unitID: blocked.ImplementationUnit, revision: blocked.ImplementationRevision,
	}
	status, body := runtime.post(t, "/v1/atlas/items/"+string(blocked.BacklogItemID)+"/revise", map[string]any{
		"request_id":        "http-cancelled-fail-closed-blocked-request",
		"expected_revision": blocked.ImplementationRevision,
		"delivery_state":    domainbacklog.DeliveryBlocked,
		"reason":            "worker_failure",
		"evidence_refs": []domainbacklog.EvidenceRef{{
			Stage: domainbacklog.DeliveryBlocked, Kind: "worker_failure", Ref: "worker-failure-http-cancelled-fail-closed", Passed: true,
		}},
	})
	if status != http.StatusOK {
		t.Fatalf("BLOCKED revise status=%d body=%s", status, body)
	}
	status, body = runtime.get(t, "/viewer/atlas/queue-freezes")
	if status != http.StatusOK {
		t.Fatalf("queue freeze list status=%d body=%s", status, body)
	}
	var freezeList struct {
		Freezes []domainworkstream.QueueFreeze `json:"queue_freezes"`
	}
	decodeAtlasLifecycleJSON(t, body, &freezeList)
	if len(freezeList.Freezes) != 1 {
		t.Fatalf("active queue freezes=%+v", freezeList.Freezes)
	}
	freeze := freezeList.Freezes[0]
	request := map[string]any{
		"action_id":                modulecore.NewActionID(),
		"expected_freeze_revision": freeze.FreezeRevision,
		"disposition":              "cancelled",
		"blocked_unit_id":          blocked.ImplementationUnit,
		"blocked_revision":         blocked.ImplementationRevision,
		"reason":                   "system owner withdrew retry",
	}
	path := "/v1/atlas/queue-freezes/" + freeze.FreezeID + "/resolve"
	unknown := map[string]any{}
	for key, value := range request {
		unknown[key] = value
	}
	unknown["disposition"] = "repaired"
	if status, body := runtime.post(t, path, unknown); status != http.StatusBadRequest {
		t.Fatalf("unknown disposition status=%d body=%s", status, body)
	}
	conflictingShape := map[string]any{}
	for key, value := range request {
		conflictingShape[key] = value
	}
	conflictingShape["replacement_unit_id"] = "unexpected-replacement"
	if status, body := runtime.post(t, path, conflictingShape); status != http.StatusBadRequest {
		t.Fatalf("cancelled request with replacement status=%d body=%s", status, body)
	}
	if status, body := runtime.post(t, path, request); status != http.StatusBadRequest {
		t.Fatalf("cancelled request without event reader status=%d body=%s", status, body)
	}
	status, body = runtime.get(t, "/viewer/atlas/queue-freezes")
	if status != http.StatusOK {
		t.Fatalf("queue freeze list after rejected cancellation status=%d body=%s", status, body)
	}
	decodeAtlasLifecycleJSON(t, body, &freezeList)
	if len(freezeList.Freezes) != 1 || freezeList.Freezes[0].Status != domainworkstream.QueueFreezeActive ||
		freezeList.Freezes[0].ResolutionDisposition != "" {
		t.Fatalf("invalid cancellation changed active freeze: %+v", freezeList.Freezes)
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
	events         *atlasLifecycleDevelopmentEventSink
	token          string
}

func newAtlasLifecycleHTTPRuntime(t *testing.T) *atlasLifecycleHTTPRuntime {
	t.Helper()
	root := atlasLifecycleTempRoot(t)
	workstreamRoot := filepath.Join(root, "workstream")
	items := backlogpersistence.NewJSONLStore(filepath.Join(root, "backlog.jsonl"))
	verifier := &atlasStrictHTTPVerifier{expected: map[string]atlasVerifierExpectation{}}
	workstream := workstreampersistence.NewJSONLStore(workstreamRoot)
	service := withAtlasTestExecutionOwners(t, appbacklog.NewService(items, workstream).WithEvidenceVerifier(verifier))
	events := &atlasLifecycleDevelopmentEventSink{events: make(map[modulecore.EventID]appbacklog.DevelopmentEvent)}
	token := "atlas-http-owner-token-012345678901234567890123"
	server := httptest.NewServer(NewAtlasHandler(service, "ren", []byte(token)))
	runtime := &atlasLifecycleHTTPRuntime{
		client: http.DefaultClient, server: server, items: items, workstream: workstream, workstreamRoot: workstreamRoot,
		service: service, verifier: verifier, events: events, token: token,
	}
	t.Cleanup(server.Close)
	return runtime
}

type atlasLifecycleDevelopmentEventSink struct {
	mu      sync.Mutex
	events  map[modulecore.EventID]appbacklog.DevelopmentEvent
	readErr error
}

func (s *atlasLifecycleDevelopmentEventSink) AppendDevelopmentEvent(ctx context.Context, event appbacklog.DevelopmentEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if event.EventID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.events == nil {
		s.events = make(map[modulecore.EventID]appbacklog.DevelopmentEvent)
	}
	if _, exists := s.events[event.EventID]; exists {
		return fmt.Errorf("duplicate Atlas development event id %q", event.EventID)
	}
	s.events[event.EventID] = cloneAtlasLifecycleDevelopmentEvent(event)
	return nil
}

func (s *atlasLifecycleDevelopmentEventSink) GetDevelopmentEvent(ctx context.Context, eventID modulecore.EventID) (appbacklog.DevelopmentEvent, bool, error) {
	if err := ctx.Err(); err != nil {
		return appbacklog.DevelopmentEvent{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readErr != nil {
		return appbacklog.DevelopmentEvent{}, false, s.readErr
	}
	event, found := s.events[eventID]
	return cloneAtlasLifecycleDevelopmentEvent(event), found, nil
}

func cloneAtlasLifecycleDevelopmentEvent(event appbacklog.DevelopmentEvent) appbacklog.DevelopmentEvent {
	if event.Fields != nil {
		fields := make(map[string]any, len(event.Fields))
		for key, value := range event.Fields {
			fields[key] = value
		}
		event.Fields = fields
	}
	return event
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
		"backlog_item_id": itemID, "kind": "idea", "title": title,
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
	if result.Item.BacklogItemID != modulecore.BacklogItemID(itemID) || result.Item.Purpose == "" || result.Item.Problem == "" || result.Item.Idea == "" || result.Item.Background == "" || len(result.Item.ExpectedEffect) == 0 {
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
	want, ok := v.expected[request.BacklogItemID]
	if !ok {
		return false, fmt.Errorf("unexpected HTTP evidence item %q", request.BacklogItemID)
	}
	if string(request.BacklogItemID) != want.itemID || request.ImplementationUnitID != want.unitID || request.ImplementationRevision != want.revision || strings.TrimSpace(request.TargetDeliveryState) == "" {
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
