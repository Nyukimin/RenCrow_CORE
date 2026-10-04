package storagehost

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	persistworkstream "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type countedWorkstreamOwner struct {
	*persistworkstream.SQLiteStore
	saveCalls atomic.Int32
}

func (owner *countedWorkstreamOwner) SaveGoalForStorageHostOperation(ctx context.Context, identity persistworkstream.WorkstreamStorageHostOperationIdentity, goal domainworkstream.Goal) (domainworkstream.Goal, error) {
	owner.saveCalls.Add(1)
	return owner.SQLiteStore.SaveGoalForStorageHostOperation(ctx, identity, goal)
}

func (owner *countedWorkstreamOwner) SaveWorkstreamRowForStorageHostOperation(ctx context.Context, identity persistworkstream.WorkstreamStorageHostOperationIdentity, item domainworkstream.Workstream) (domainworkstream.Workstream, error) {
	owner.saveCalls.Add(1)
	return owner.SQLiteStore.SaveWorkstreamRowForStorageHostOperation(ctx, identity, item)
}
func (owner *countedWorkstreamOwner) SaveArtifactForStorageHostOperation(ctx context.Context, identity persistworkstream.WorkstreamStorageHostOperationIdentity, item domainworkstream.Artifact) (domainworkstream.Artifact, error) {
	owner.saveCalls.Add(1)
	return owner.SQLiteStore.SaveArtifactForStorageHostOperation(ctx, identity, item)
}
func (owner *countedWorkstreamOwner) SaveArtifactAnnotationForStorageHostOperation(ctx context.Context, identity persistworkstream.WorkstreamStorageHostOperationIdentity, item domainworkstream.ArtifactAnnotation) (domainworkstream.ArtifactAnnotation, error) {
	owner.saveCalls.Add(1)
	return owner.SQLiteStore.SaveArtifactAnnotationForStorageHostOperation(ctx, identity, item)
}
func (owner *countedWorkstreamOwner) SaveSteeringItemForStorageHostOperation(ctx context.Context, identity persistworkstream.WorkstreamStorageHostOperationIdentity, item domainworkstream.SteeringItem) (domainworkstream.SteeringItem, error) {
	owner.saveCalls.Add(1)
	return owner.SQLiteStore.SaveSteeringItemForStorageHostOperation(ctx, identity, item)
}
func (owner *countedWorkstreamOwner) SaveHeartbeatScheduleForStorageHostOperation(ctx context.Context, identity persistworkstream.WorkstreamStorageHostOperationIdentity, item domainworkstream.HeartbeatSchedule) (domainworkstream.HeartbeatSchedule, error) {
	owner.saveCalls.Add(1)
	return owner.SQLiteStore.SaveHeartbeatScheduleForStorageHostOperation(ctx, identity, item)
}
func (owner *countedWorkstreamOwner) SaveVaultUpdateLogForStorageHostOperation(ctx context.Context, identity persistworkstream.WorkstreamStorageHostOperationIdentity, item domainworkstream.VaultUpdateLog) (domainworkstream.VaultUpdateLog, error) {
	owner.saveCalls.Add(1)
	return owner.SQLiteStore.SaveVaultUpdateLogForStorageHostOperation(ctx, identity, item)
}
func (owner *countedWorkstreamOwner) SaveQueueFreezeForStorageHostOperation(ctx context.Context, identity persistworkstream.WorkstreamStorageHostOperationIdentity, item domainworkstream.QueueFreeze) (domainworkstream.QueueFreeze, error) {
	owner.saveCalls.Add(1)
	return owner.SQLiteStore.SaveQueueFreezeForStorageHostOperation(ctx, identity, item)
}
func (owner *countedWorkstreamOwner) SaveStageRunReceiptForStorageHostOperation(ctx context.Context, identity persistworkstream.WorkstreamStorageHostOperationIdentity, item domainworkstream.StageRunReceipt) (domainworkstream.StageRunReceipt, error) {
	owner.saveCalls.Add(1)
	return owner.SQLiteStore.SaveStageRunReceiptForStorageHostOperation(ctx, identity, item)
}
func (owner *countedWorkstreamOwner) SaveClosureReceiptForStorageHostOperation(ctx context.Context, identity persistworkstream.WorkstreamStorageHostOperationIdentity, item domainworkstream.ClosureReceipt) (domainworkstream.ClosureReceipt, error) {
	owner.saveCalls.Add(1)
	return owner.SQLiteStore.SaveClosureReceiptForStorageHostOperation(ctx, identity, item)
}
func (owner *countedWorkstreamOwner) AcquireImplementationLeaseIfUnfrozenForStorageHostOperation(ctx context.Context, identity persistworkstream.WorkstreamStorageHostOperationIdentity, item domainworkstream.ImplementationLease) (persistworkstream.WorkstreamLeaseAcquireResult, error) {
	owner.saveCalls.Add(1)
	return owner.SQLiteStore.AcquireImplementationLeaseIfUnfrozenForStorageHostOperation(ctx, identity, item)
}
func (owner *countedWorkstreamOwner) ResolveQueueFreezeAndAcquireLeaseForStorageHostOperation(ctx context.Context, identity persistworkstream.WorkstreamStorageHostOperationIdentity, freezeID string, resolution domainworkstream.QueueFreezeResolution, replacement domainworkstream.ImplementationLease) (persistworkstream.WorkstreamFreezeLeaseResult, error) {
	owner.saveCalls.Add(1)
	return owner.SQLiteStore.ResolveQueueFreezeAndAcquireLeaseForStorageHostOperation(ctx, identity, freezeID, resolution, replacement)
}
func (owner *countedWorkstreamOwner) ReleaseImplementationLeaseForStorageHostOperation(ctx context.Context, identity persistworkstream.WorkstreamStorageHostOperationIdentity, leaseName, holderUnitID string) error {
	owner.saveCalls.Add(1)
	return owner.SQLiteStore.ReleaseImplementationLeaseForStorageHostOperation(ctx, identity, leaseName, holderUnitID)
}
func (owner *countedWorkstreamOwner) HeartbeatImplementationLeaseForStorageHostOperation(ctx context.Context, identity persistworkstream.WorkstreamStorageHostOperationIdentity, item domainworkstream.ImplementationLease) error {
	owner.saveCalls.Add(1)
	return owner.SQLiteStore.HeartbeatImplementationLeaseForStorageHostOperation(ctx, identity, item)
}

func newWorkstreamStorageHostFixture(t *testing.T, root string) (*persistworkstream.SQLiteStore, *countedWorkstreamOwner, *Handler, *httptest.Server, *WorkstreamClient) {
	t.Helper()
	store, err := persistworkstream.NewSQLiteStore(filepath.Join(root, "workstream.sqlite"))
	if err != nil {
		t.Fatalf("open workstream owner: %v", err)
	}
	owner := &countedWorkstreamOwner{SQLiteStore: store}
	handler, err := NewHandler(HandlerConfig{Token: testToken, JournalDir: filepath.Join(root, "journal")})
	if err != nil {
		store.Close()
		t.Fatalf("open storagehost: %v", err)
	}
	if err := RegisterWorkstreamGroup(handler, owner); err != nil {
		handler.Close()
		store.Close()
		t.Fatalf("register workstream group: %v", err)
	}
	server := httptest.NewServer(handler)
	base, err := NewClient(ClientConfig{Endpoint: server.URL, Token: testToken, HTTPClient: server.Client()})
	if err != nil {
		server.Close()
		handler.Close()
		store.Close()
		t.Fatalf("open storagehost client: %v", err)
	}
	if err := base.Handshake(context.Background()); err != nil {
		server.Close()
		handler.Close()
		store.Close()
		t.Fatalf("storagehost handshake: %v", err)
	}
	return store, owner, handler, server, NewWorkstreamClient(base)
}

func workstreamGoalFixture() domainworkstream.Goal {
	return domainworkstream.Goal{
		GoalID: "goal-request-replay", TraceID: "trace-request-replay", WorkstreamID: "workstream-contract",
		Title: "Persist goal", SuccessCriteria: []string{"exact result survives restart"},
		Verification: []string{"owner receipt verified"}, Status: domainworkstream.StatusDraft,
		CreatedAt: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC),
	}
}

func TestWorkstreamSaveGoalLostResponseReopensWithOriginalResultWithoutRerun(t *testing.T) {
	root := t.TempDir()
	store, owner, handler, server, client := newWorkstreamStorageHostFixture(t, root)
	const requestID = "workstream-goal-lost-response"
	handler.crashAfterCommitFor = requestID
	goal := workstreamGoalFixture()
	if result, err := client.SaveGoal(context.Background(), requestID, goal); storageHostErrorCode(err) != ErrorCodeOutcomeUnknown || result.GoalID != "" {
		t.Fatalf("lost response result=%+v err=%v, want outcome_unknown", result, err)
	}
	if got := owner.saveCalls.Load(); got != 1 {
		t.Fatalf("owner writes=%d, want 1 before reopen", got)
	}
	server.Close()
	if err := handler.Close(); err != nil {
		t.Fatalf("close storagehost: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close owner: %v", err)
	}

	store, owner, handler, server, client = newWorkstreamStorageHostFixture(t, root)
	defer server.Close()
	defer handler.Close()
	defer store.Close()
	result, err := client.SaveGoal(context.Background(), requestID, goal)
	if err != nil || !reflect.DeepEqual(result, goal) {
		t.Fatalf("reopened result=%+v err=%v, want original goal", result, err)
	}
	if got := owner.saveCalls.Load(); got != 0 {
		t.Fatalf("owner writes after recovery=%d, want 0", got)
	}
	changed := goal
	changed.Title = "changed operation payload"
	if _, err := client.SaveGoal(context.Background(), requestID, changed); storageHostErrorCode(err) != ErrorCodeDuplicateConflict {
		t.Fatalf("same op_id with changed payload err=%v, want duplicate_conflict", err)
	}
	listed, err := client.ListGoals(context.Background(), 10)
	if err != nil || len(listed) != 1 || !reflect.DeepEqual(listed[0], goal) {
		t.Fatalf("typed goal list=%+v err=%v, want original goal", listed, err)
	}
}

func TestWorkstreamGroupRegistersOnlyClosedTypedOperations(t *testing.T) {
	store, _, handler, server, client := newWorkstreamStorageHostFixture(t, t.TempDir())
	defer server.Close()
	defer handler.Close()
	defer store.Close()
	contract, err := client.client.Contract(context.Background())
	if err != nil {
		t.Fatalf("Contract: %v", err)
	}
	want := map[string]bool{
		"list_workstreams": false, "list_goals": false, "list_artifacts": false,
		"list_artifact_annotations": false, "list_steering_items": false,
		"list_heartbeat_schedules": false, "list_vault_update_logs": false,
		"find_goal_by_id": false, "get_queue_freeze": false, "list_queue_freezes": false,
		"find_stage_run_receipt": false, "list_stage_run_receipts": false,
		"find_closure_receipt": false, "list_closure_receipts": false,
		"get_implementation_lease": false,
		"save_goal":                true, "save_workstream": true, "save_artifact": true,
		"save_artifact_annotation": true, "save_steering_item": true,
		"save_heartbeat_schedule": true, "save_vault_update_log": true,
		"save_queue_freeze": true, "save_stage_run_receipt": true,
		"save_closure_receipt": true, "acquire_implementation_lease_if_unfrozen": true,
		"resolve_queue_freeze_and_acquire_lease": true, "release_implementation_lease": true,
		"heartbeat_implementation_lease": true,
	}
	got := make(map[string]bool)
	for _, operation := range contract.Operations {
		if operation.Group == GroupWorkstream {
			got[operation.Op] = operation.Mutating
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("workstream operation contract=%v, want exactly %v", got, want)
	}
}

func TestWorkstreamTypedReadOperationsPreserveLifecycleDTOs(t *testing.T) {
	store, owner, handler, server, client := newWorkstreamStorageHostFixture(t, t.TempDir())
	defer server.Close()
	defer handler.Close()
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 10, 4, 18, 0, 0, 0, time.UTC)
	workstream := domainworkstream.Workstream{WorkstreamID: "ws-read-contract", Name: "read DTO", Status: domainworkstream.StatusActive, CreatedAt: now}
	goal := workstreamGoalFixture()
	goal.WorkstreamID = workstream.WorkstreamID
	artifact := domainworkstream.Artifact{ArtifactID: "artifact-read-contract", WorkstreamID: workstream.WorkstreamID, Type: "markdown", Status: "draft", CreatedAt: now}
	annotation := domainworkstream.ArtifactAnnotation{AnnotationID: "annotation-read-contract", ArtifactID: artifact.ArtifactID, Comment: "read", Status: "open", CreatedAt: now}
	steering := domainworkstream.SteeringItem{SteeringID: "steering-read-contract", WorkstreamID: workstream.WorkstreamID, Instruction: "read", Status: "pending", CreatedAt: now}
	schedule := domainworkstream.HeartbeatSchedule{ScheduleID: modulecore.NewScheduleID(), WorkstreamID: workstream.WorkstreamID, ScheduleText: "daily", Task: "read", Status: domainworkstream.StatusActive, CreatedAt: now}
	vaultLog := domainworkstream.VaultUpdateLog{UpdateID: "update-read-contract", WorkstreamID: workstream.WorkstreamID, FilePath: "vault/read/STATUS.md", ReviewStatus: domainworkstream.VaultReviewPending, CreatedAt: now}
	freeze := domainworkstream.QueueFreeze{FreezeID: "freeze-read-contract", BlockedUnitID: "unit-read", BlockedRevision: 1, ReasonCode: "blocked", InvalidatedFromStage: "BUILD", CreatedAt: now}
	freeze.FreezeRevision, freeze.Status, freeze.UpdatedAt = 1, domainworkstream.QueueFreezeActive, now
	stageReceipt := domainworkstream.StageRunReceipt{ReceiptID: modulecore.NewReceiptID(), IdempotencyKey: "read:stage", UnitID: "unit-read", ImplementationRevision: 1, TargetStage: "BUILD", PayloadHash: "hash", Status: domainworkstream.StageRunCompleted, CreatedAt: now}
	closureReceipt := domainworkstream.ClosureReceipt{ReceiptID: modulecore.NewReceiptID(), IdempotencyKey: "read:closure", UnitID: "unit-read", ImplementationRevision: 1, Phase: domainworkstream.ClosurePhasePrepared, Status: domainworkstream.ClosureStatusPrepared, CreatedAt: now}
	lease := domainworkstream.ImplementationLease{LeaseName: "atlas", HolderUnitID: "unit-read", HolderWorkstreamID: workstream.WorkstreamID, AcquiredAt: now, HeartbeatAt: now}
	if acquired, err := owner.AcquireImplementationLease(ctx, lease); err != nil || !acquired {
		t.Fatalf("seed implementation lease acquired=%v err=%v", acquired, err)
	}
	for _, save := range []func() error{
		func() error { return owner.SaveWorkstream(ctx, workstream) },
		func() error { return owner.SaveGoal(ctx, goal) },
		func() error { return owner.SaveArtifact(ctx, artifact) },
		func() error { return owner.SaveArtifactAnnotation(ctx, annotation) },
		func() error { return owner.SaveSteeringItem(ctx, steering) },
		func() error { return owner.SaveHeartbeatSchedule(ctx, schedule) },
		func() error { return owner.SaveVaultUpdateLog(ctx, vaultLog) },
		func() error { return owner.SaveQueueFreeze(ctx, freeze) },
		func() error { return owner.SaveStageRunReceipt(ctx, stageReceipt) },
		func() error { return owner.SaveClosureReceipt(ctx, closureReceipt) },
	} {
		if err := save(); err != nil {
			t.Fatalf("seed typed row: %v", err)
		}
	}
	if got, err := client.ListWorkstreams(ctx, 20); err != nil || !reflect.DeepEqual(got, []domainworkstream.Workstream{workstream}) {
		t.Fatalf("ListWorkstreams=%+v err=%v", got, err)
	}
	if got, found, err := client.FindGoalByID(ctx, goal.GoalID); err != nil || !found || !reflect.DeepEqual(got, goal) {
		t.Fatalf("FindGoalByID=%+v found=%v err=%v", got, found, err)
	}
	if got, err := client.ListArtifacts(ctx, 20); err != nil || !reflect.DeepEqual(got, []domainworkstream.Artifact{artifact}) {
		t.Fatalf("ListArtifacts=%+v err=%v", got, err)
	}
	if got, err := client.ListArtifactAnnotations(ctx, 20); err != nil || !reflect.DeepEqual(got, []domainworkstream.ArtifactAnnotation{annotation}) {
		t.Fatalf("ListArtifactAnnotations=%+v err=%v", got, err)
	}
	if got, err := client.ListSteeringItems(ctx, 20); err != nil || !reflect.DeepEqual(got, []domainworkstream.SteeringItem{steering}) {
		t.Fatalf("ListSteeringItems=%+v err=%v", got, err)
	}
	if got, err := client.ListHeartbeatSchedules(ctx, 20); err != nil || !reflect.DeepEqual(got, []domainworkstream.HeartbeatSchedule{schedule}) {
		t.Fatalf("ListHeartbeatSchedules=%+v err=%v", got, err)
	}
	if got, err := client.ListVaultUpdateLogs(ctx, 20); err != nil || !reflect.DeepEqual(got, []domainworkstream.VaultUpdateLog{vaultLog}) {
		t.Fatalf("ListVaultUpdateLogs=%+v err=%v", got, err)
	}
	if got, err := client.ListQueueFreezes(ctx, 20); err != nil || !reflect.DeepEqual(got, []domainworkstream.QueueFreeze{freeze}) {
		t.Fatalf("ListQueueFreezes=%+v err=%v", got, err)
	}
	if got, found, err := client.GetQueueFreeze(ctx, freeze.FreezeID); err != nil || !found || !reflect.DeepEqual(got, freeze) {
		t.Fatalf("GetQueueFreeze=%+v found=%v err=%v", got, found, err)
	}
	if got, found, err := client.FindStageRunReceipt(ctx, stageReceipt.IdempotencyKey); err != nil || !found || !reflect.DeepEqual(got, stageReceipt) {
		t.Fatalf("FindStageRunReceipt=%+v found=%v err=%v", got, found, err)
	}
	if got, err := client.ListStageRunReceipts(ctx, 20); err != nil || !reflect.DeepEqual(got, []domainworkstream.StageRunReceipt{stageReceipt}) {
		t.Fatalf("ListStageRunReceipts=%+v err=%v", got, err)
	}
	if got, found, err := client.FindClosureReceipt(ctx, closureReceipt.IdempotencyKey); err != nil || !found || !reflect.DeepEqual(got, closureReceipt) {
		t.Fatalf("FindClosureReceipt=%+v found=%v err=%v", got, found, err)
	}
	if got, err := client.ListClosureReceipts(ctx, 20); err != nil || !reflect.DeepEqual(got, []domainworkstream.ClosureReceipt{closureReceipt}) {
		t.Fatalf("ListClosureReceipts=%+v err=%v", got, err)
	}
	if got, found, err := client.GetImplementationLease(ctx, lease.LeaseName); err != nil || !found || !reflect.DeepEqual(got, lease) {
		t.Fatalf("GetImplementationLease=%+v found=%v err=%v", got, found, err)
	}
}

func TestWorkstreamSaveGoalReceiptReconcilesOriginalResultAfterLaterRowUpdate(t *testing.T) {
	root := t.TempDir()
	store, owner, handler, server, client := newWorkstreamStorageHostFixture(t, root)
	const requestID = "workstream-goal-lost-response-before-later-update"
	handler.crashAfterCommitFor = requestID
	original := workstreamGoalFixture()
	if _, err := client.SaveGoal(context.Background(), requestID, original); storageHostErrorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("lost response err=%v, want outcome_unknown", err)
	}
	later := original
	later.Title = "A later legitimate update"
	if err := store.SaveGoal(context.Background(), later); err != nil {
		t.Fatalf("write later canonical state: %v", err)
	}
	server.Close()
	if err := handler.Close(); err != nil {
		t.Fatalf("close storagehost: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close owner: %v", err)
	}

	store, owner, handler, server, client = newWorkstreamStorageHostFixture(t, root)
	defer server.Close()
	defer handler.Close()
	defer store.Close()
	result, err := client.SaveGoal(context.Background(), requestID, original)
	if err != nil || !reflect.DeepEqual(result, original) {
		t.Fatalf("recovered original result=%+v err=%v, want original %+v", result, err, original)
	}
	if got := owner.saveCalls.Load(); got != 0 {
		t.Fatalf("owner writes during receipt recovery=%d, want 0", got)
	}
	current, found, err := store.FindGoalByID(context.Background(), original.GoalID)
	if err != nil || !found || !reflect.DeepEqual(current, later) {
		t.Fatalf("current canonical row=%+v found=%v err=%v, want later state %+v", current, found, err, later)
	}
}

func TestWorkstreamReconciliationRejectsRehashedSubstitutedResultWithoutRerun(t *testing.T) {
	root := t.TempDir()
	store, owner, handler, server, client := newWorkstreamStorageHostFixture(t, root)
	const requestID = "workstream-goal-rehashed-substitution"
	handler.crashAfterCommitFor = requestID
	original := workstreamGoalFixture()
	if _, err := client.SaveGoal(context.Background(), requestID, original); storageHostErrorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("lost response err=%v, want outcome_unknown", err)
	}
	substituted := original
	substituted.Title = "rehashed but substituted"
	substitutedJSON, err := json.Marshal(substituted)
	if err != nil {
		t.Fatalf("marshal substituted result: %v", err)
	}
	substitutedHash := sha256.Sum256(substitutedJSON)
	tamperDB, err := sql.Open("sqlite", filepath.Join(root, "workstream.sqlite"))
	if err != nil {
		t.Fatalf("open receipt tamper handle: %v", err)
	}
	if _, err := tamperDB.Exec(`UPDATE workstream_storagehost_operation_receipt SET result_json=?, result_sha256=?, effect_json=?, effect_sha256=? WHERE op_id=?`, string(substitutedJSON), hex.EncodeToString(substitutedHash[:]), string(substitutedJSON), hex.EncodeToString(substitutedHash[:]), requestID); err != nil {
		_ = tamperDB.Close()
		t.Fatalf("substitute and rehash owner proof: %v", err)
	}
	if err := tamperDB.Close(); err != nil {
		t.Fatalf("close receipt tamper handle: %v", err)
	}
	server.Close()
	if err := handler.Close(); err != nil {
		t.Fatalf("close storagehost: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close owner: %v", err)
	}

	store, owner, handler, server, client = newWorkstreamStorageHostFixture(t, root)
	defer server.Close()
	defer handler.Close()
	defer store.Close()
	if _, err := client.SaveGoal(context.Background(), requestID, original); storageHostErrorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("substituted proof retry err=%v, want outcome_unknown", err)
	}
	if got := owner.saveCalls.Load(); got != 0 {
		t.Fatalf("owner writes during corrupt-proof reconciliation=%d, want 0", got)
	}
}

func TestWorkstreamHeldLeaseRehashedSuccessIsRejectedAfterReopen(t *testing.T) {
	root := t.TempDir()
	store, owner, handler, server, client := newWorkstreamStorageHostFixture(t, root)
	ctx := context.Background()
	now := time.Date(2026, 10, 4, 19, 0, 0, 0, time.UTC)
	foreign := domainworkstream.ImplementationLease{LeaseName: "atlas", HolderUnitID: "unit-foreign", HolderWorkstreamID: "ws-foreign", Stage: "BUILD", AcquiredAt: now, HeartbeatAt: now}
	if acquired, err := store.AcquireImplementationLease(ctx, foreign); err != nil || !acquired {
		t.Fatalf("seed foreign lease acquired=%v err=%v", acquired, err)
	}
	requested := domainworkstream.ImplementationLease{LeaseName: foreign.LeaseName, HolderUnitID: "unit-requested", HolderWorkstreamID: "ws-requested", Stage: "PLAN", AcquiredAt: now, HeartbeatAt: now}
	const requestID = "workstream-held-lease-rehashed-success"
	handler.crashAfterCommitFor = requestID
	if acquired, reason, err := client.AcquireImplementationLeaseIfUnfrozen(ctx, requestID, requested); storageHostErrorCode(err) != ErrorCodeOutcomeUnknown || acquired || reason != "" {
		t.Fatalf("lost response result acquired=%v reason=%q err=%v, want outcome_unknown", acquired, reason, err)
	}
	if got := owner.saveCalls.Load(); got != 1 {
		t.Fatalf("owner calls before recovery=%d, want 1", got)
	}

	substitutedJSON, err := json.Marshal(persistworkstream.WorkstreamLeaseAcquireResult{Acquired: true})
	if err != nil {
		t.Fatalf("marshal substituted result: %v", err)
	}
	substitutedHash := sha256.Sum256(substitutedJSON)
	tamperDB, err := sql.Open("sqlite", filepath.Join(root, "workstream.sqlite"))
	if err != nil {
		t.Fatalf("open receipt tamper handle: %v", err)
	}
	if _, err := tamperDB.Exec(`UPDATE workstream_storagehost_operation_receipt SET result_json=?, result_sha256=? WHERE op_id=?`, string(substitutedJSON), hex.EncodeToString(substitutedHash[:]), requestID); err != nil {
		_ = tamperDB.Close()
		t.Fatalf("substitute and rehash result only: %v", err)
	}
	if err := tamperDB.Close(); err != nil {
		t.Fatalf("close receipt tamper handle: %v", err)
	}
	server.Close()
	if err := handler.Close(); err != nil {
		t.Fatalf("close storagehost: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close owner: %v", err)
	}

	store, owner, handler, server, client = newWorkstreamStorageHostFixture(t, root)
	defer server.Close()
	defer handler.Close()
	defer store.Close()
	acquired, reason, err := client.AcquireImplementationLeaseIfUnfrozen(ctx, requestID, requested)
	if storageHostErrorCode(err) != ErrorCodeOutcomeUnknown {
		t.Errorf("recovered tampered acquire acquired=%v reason=%q err=%v, want outcome_unknown", acquired, reason, err)
	}
	if acquired || reason != "" {
		t.Errorf("recovered tampered acquire exposed acquired=%v reason=%q, want no result", acquired, reason)
	}
	if got := owner.saveCalls.Load(); got != 0 {
		t.Errorf("owner calls during corrupt-proof recovery=%d, want 0", got)
	}
	canonical, found, err := store.GetImplementationLease(ctx, foreign.LeaseName)
	if err != nil || !found || canonical != foreign {
		t.Errorf("canonical lease=%+v found=%v err=%v, want foreign holder %+v", canonical, found, err, foreign)
	}
}

func TestWorkstreamAcquireHeldLeaseReplaysOriginalFailureAfterCanonicalChange(t *testing.T) {
	root := t.TempDir()
	store, owner, handler, server, client := newWorkstreamStorageHostFixture(t, root)
	ctx := context.Background()
	now := time.Date(2026, 10, 4, 19, 15, 0, 0, time.UTC)
	foreign := domainworkstream.ImplementationLease{LeaseName: "atlas", HolderUnitID: "unit-foreign", HolderWorkstreamID: "ws-foreign", Stage: "BUILD", AcquiredAt: now, HeartbeatAt: now}
	if acquired, err := store.AcquireImplementationLease(ctx, foreign); err != nil || !acquired {
		t.Fatalf("seed foreign lease acquired=%v err=%v", acquired, err)
	}
	requested := domainworkstream.ImplementationLease{LeaseName: foreign.LeaseName, HolderUnitID: "unit-requested", HolderWorkstreamID: "ws-requested", Stage: "PLAN", AcquiredAt: now, HeartbeatAt: now}
	const requestID = "workstream-held-lease-original-failure"
	acquired, reason, err := client.AcquireImplementationLeaseIfUnfrozen(ctx, requestID, requested)
	if err != nil || acquired || reason != domainworkstream.ErrImplementationLeaseHeld.Error() {
		t.Fatalf("initial held-lease result acquired=%v reason=%q err=%v", acquired, reason, err)
	}
	if got := owner.saveCalls.Load(); got != 1 {
		t.Fatalf("owner calls before state change=%d, want 1", got)
	}
	if err := store.ReleaseImplementationLease(ctx, foreign.LeaseName, foreign.HolderUnitID); err != nil {
		t.Fatalf("release foreign lease after committed operation: %v", err)
	}
	server.Close()
	if err := handler.Close(); err != nil {
		t.Fatalf("close storagehost: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close owner: %v", err)
	}

	store, owner, handler, server, client = newWorkstreamStorageHostFixture(t, root)
	defer server.Close()
	defer handler.Close()
	defer store.Close()
	acquired, reason, err = client.AcquireImplementationLeaseIfUnfrozen(ctx, requestID, requested)
	if err != nil || acquired || reason != domainworkstream.ErrImplementationLeaseHeld.Error() {
		t.Errorf("replayed result after canonical lease release acquired=%v reason=%q err=%v, want original held failure", acquired, reason, err)
	}
	if got := owner.saveCalls.Load(); got != 0 {
		t.Errorf("owner calls during exact-result recovery=%d, want 0", got)
	}
	if _, found, err := store.GetImplementationLease(ctx, foreign.LeaseName); err != nil || found {
		t.Errorf("current canonical lease found=%v err=%v, want still released", found, err)
	}
}

func TestWorkstreamResolveLeaseRehashedFailureIsRejectedAfterReopen(t *testing.T) {
	root := t.TempDir()
	store, owner, handler, server, client := newWorkstreamStorageHostFixture(t, root)
	ctx := context.Background()
	now := time.Date(2026, 10, 4, 19, 30, 0, 0, time.UTC)
	freeze := domainworkstream.QueueFreeze{FreezeID: "freeze-result-binding", BlockedUnitID: "unit-blocked", BlockedRevision: 3, FreezeRevision: 5, Status: domainworkstream.QueueFreezeActive, CreatedAt: now}
	if err := store.SaveQueueFreeze(ctx, freeze); err != nil {
		t.Fatalf("seed active freeze: %v", err)
	}
	replacement := domainworkstream.ImplementationLease{LeaseName: "atlas", HolderUnitID: "unit-replacement", HolderWorkstreamID: "ws-replacement", Stage: "PLAN", AcquiredAt: now, HeartbeatAt: now}
	resolution := domainworkstream.QueueFreezeResolution{ExpectedFreezeRevision: 5, ActionID: modulecore.ActionID("act_00000000-0000-5000-8000-000000000009"), ReplacementUnitID: replacement.HolderUnitID, SupersedesUnitID: freeze.BlockedUnitID, BlockerResolutionRefs: []domainbacklog.EvidenceRef{{Kind: "fix", Ref: "result-binding", Verified: true, VerificationResult: domainbacklog.EvidenceVerificationVerified}}, ResolutionPayloadHash: "resolution-result-binding"}
	const requestID = "workstream-resolve-lease-rehashed-failure"
	handler.crashAfterCommitFor = requestID
	if _, _, acquired, err := client.ResolveQueueFreezeAndAcquireLease(ctx, requestID, freeze.FreezeID, resolution, replacement); storageHostErrorCode(err) != ErrorCodeOutcomeUnknown || acquired {
		t.Fatalf("lost response acquired=%v err=%v, want outcome_unknown", acquired, err)
	}
	if got := owner.saveCalls.Load(); got != 1 {
		t.Fatalf("owner calls before recovery=%d, want 1", got)
	}

	tamperDB, err := sql.Open("sqlite", filepath.Join(root, "workstream.sqlite"))
	if err != nil {
		t.Fatalf("open receipt tamper handle: %v", err)
	}
	var resultJSON string
	if err := tamperDB.QueryRow(`SELECT result_json FROM workstream_storagehost_operation_receipt WHERE op_id=?`, requestID).Scan(&resultJSON); err != nil {
		_ = tamperDB.Close()
		t.Fatalf("read original result: %v", err)
	}
	var substituted persistworkstream.WorkstreamFreezeLeaseResult
	if err := json.Unmarshal([]byte(resultJSON), &substituted); err != nil || !substituted.Acquired {
		_ = tamperDB.Close()
		t.Fatalf("original resolve result=%+v err=%v, want acquired=true", substituted, err)
	}
	substituted.Acquired = false
	substitutedJSON, err := json.Marshal(substituted)
	if err != nil {
		_ = tamperDB.Close()
		t.Fatalf("marshal substituted result: %v", err)
	}
	substitutedHash := sha256.Sum256(substitutedJSON)
	if _, err := tamperDB.Exec(`UPDATE workstream_storagehost_operation_receipt SET result_json=?, result_sha256=? WHERE op_id=?`, string(substitutedJSON), hex.EncodeToString(substitutedHash[:]), requestID); err != nil {
		_ = tamperDB.Close()
		t.Fatalf("substitute and rehash result only: %v", err)
	}
	if err := tamperDB.Close(); err != nil {
		t.Fatalf("close receipt tamper handle: %v", err)
	}
	server.Close()
	if err := handler.Close(); err != nil {
		t.Fatalf("close storagehost: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close owner: %v", err)
	}

	store, owner, handler, server, client = newWorkstreamStorageHostFixture(t, root)
	defer server.Close()
	defer handler.Close()
	defer store.Close()
	_, _, acquired, err := client.ResolveQueueFreezeAndAcquireLease(ctx, requestID, freeze.FreezeID, resolution, replacement)
	if storageHostErrorCode(err) != ErrorCodeOutcomeUnknown {
		t.Errorf("recovered tampered resolve acquired=%v err=%v, want outcome_unknown", acquired, err)
	}
	if acquired {
		t.Errorf("recovered tampered resolve exposed acquired=true, want no result")
	}
	if got := owner.saveCalls.Load(); got != 0 {
		t.Errorf("owner calls during corrupt-proof recovery=%d, want 0", got)
	}
	canonical, found, err := store.GetImplementationLease(ctx, replacement.LeaseName)
	if err != nil || !found || canonical != replacement {
		t.Errorf("canonical lease=%+v found=%v err=%v, want committed replacement %+v", canonical, found, err, replacement)
	}
}

func TestWorkstreamMutationsReconcileAfterOwnerAndHandlerReopen(t *testing.T) {
	now := time.Date(2026, 10, 4, 16, 0, 0, 0, time.UTC)
	workstream := domainworkstream.Workstream{WorkstreamID: "ws-rpc-save", Name: "RPC owner", Status: domainworkstream.StatusActive, CreatedAt: now}
	goal := workstreamGoalFixture()
	artifact := domainworkstream.Artifact{ArtifactID: "artifact-rpc-save", WorkstreamID: workstream.WorkstreamID, Type: "markdown", Status: "draft", CreatedAt: now}
	annotation := domainworkstream.ArtifactAnnotation{AnnotationID: "annotation-rpc-save", ArtifactID: artifact.ArtifactID, Comment: "review", Status: "open", CreatedAt: now}
	steering := domainworkstream.SteeringItem{SteeringID: "steering-rpc-save", WorkstreamID: workstream.WorkstreamID, Instruction: "keep audit", Status: "pending", CreatedAt: now}
	schedule := domainworkstream.HeartbeatSchedule{ScheduleID: modulecore.NewScheduleID(), WorkstreamID: workstream.WorkstreamID, ScheduleText: "daily", Task: "draft only", Status: domainworkstream.StatusActive, CreatedAt: now}
	vaultLog := domainworkstream.VaultUpdateLog{UpdateID: "update-rpc-save", WorkstreamID: workstream.WorkstreamID, FilePath: "vault/workstreams/ws-rpc-save/STATUS.md", ReviewStatus: domainworkstream.VaultReviewPending, CreatedAt: now}
	freeze := domainworkstream.QueueFreeze{FreezeID: "freeze-rpc-save", BlockedUnitID: "unit-rpc-save", BlockedRevision: 2, ReasonCode: "blocked", InvalidatedFromStage: "BUILD", CreatedAt: now}
	stageReceipt := domainworkstream.StageRunReceipt{ReceiptID: modulecore.NewReceiptID(), IdempotencyKey: "rpc-save:stage", UnitID: "unit-rpc-save", ImplementationRevision: 2, TargetStage: "BUILD", PayloadHash: "rpc-payload", Status: domainworkstream.StageRunCompleted, CreatedAt: now}
	closureReceipt := domainworkstream.ClosureReceipt{ReceiptID: modulecore.NewReceiptID(), IdempotencyKey: "rpc-save:closure", UnitID: "unit-rpc-save", ImplementationRevision: 2, Phase: domainworkstream.ClosurePhasePrepared, Status: domainworkstream.ClosureStatusPrepared, CreatedAt: now}
	lease := domainworkstream.ImplementationLease{LeaseName: "atlas", HolderUnitID: "unit-rpc-save", HolderWorkstreamID: "ws-rpc-save", Stage: "BUILD", AcquiredAt: now, HeartbeatAt: now}
	resolveFreeze := domainworkstream.QueueFreeze{FreezeID: "freeze-rpc-resolve", BlockedUnitID: "unit-old", BlockedRevision: 2, FreezeRevision: 3, Status: domainworkstream.QueueFreezeActive, CreatedAt: now}
	replacement := domainworkstream.ImplementationLease{LeaseName: "atlas", HolderUnitID: "unit-new", HolderWorkstreamID: "ws-new", AcquiredAt: now, HeartbeatAt: now}
	resolution := domainworkstream.QueueFreezeResolution{ExpectedFreezeRevision: 3, ActionID: modulecore.ActionID("act_00000000-0000-5000-8000-000000000008"), ReplacementUnitID: replacement.HolderUnitID, SupersedesUnitID: resolveFreeze.BlockedUnitID, BlockerResolutionRefs: []domainbacklog.EvidenceRef{{Kind: "fix", Ref: "rpc-fix", Verified: true, VerificationResult: domainbacklog.EvidenceVerificationVerified}}, ResolutionPayloadHash: "rpc-resolution-hash"}
	heartbeat := lease
	heartbeat.Stage, heartbeat.Revision, heartbeat.HeartbeatAt = "VERIFY", "r3", now.Add(time.Minute)

	cases := []struct {
		name    string
		prepare func(*persistworkstream.SQLiteStore) error
		call    func(*WorkstreamClient, string) (any, error)
		want    func(*persistworkstream.SQLiteStore) (any, error)
	}{
		{name: "goal", call: func(c *WorkstreamClient, id string) (any, error) { return c.SaveGoal(context.Background(), id, goal) }, want: func(*persistworkstream.SQLiteStore) (any, error) { return goal, nil }},
		{name: "workstream_row", call: func(c *WorkstreamClient, id string) (any, error) {
			return c.SaveWorkstreamRow(context.Background(), id, workstream)
		}, want: func(*persistworkstream.SQLiteStore) (any, error) { return workstream, nil }},
		{name: "artifact", call: func(c *WorkstreamClient, id string) (any, error) {
			return c.SaveArtifact(context.Background(), id, artifact)
		}, want: func(*persistworkstream.SQLiteStore) (any, error) { return artifact, nil }},
		{name: "artifact_annotation", call: func(c *WorkstreamClient, id string) (any, error) {
			return c.SaveArtifactAnnotation(context.Background(), id, annotation)
		}, want: func(*persistworkstream.SQLiteStore) (any, error) { return annotation, nil }},
		{name: "steering_item", call: func(c *WorkstreamClient, id string) (any, error) {
			return c.SaveSteeringItem(context.Background(), id, steering)
		}, want: func(*persistworkstream.SQLiteStore) (any, error) { return steering, nil }},
		{name: "heartbeat_schedule", call: func(c *WorkstreamClient, id string) (any, error) {
			return c.SaveHeartbeatSchedule(context.Background(), id, schedule)
		}, want: func(*persistworkstream.SQLiteStore) (any, error) { return schedule, nil }},
		{name: "vault_update_log", call: func(c *WorkstreamClient, id string) (any, error) {
			return c.SaveVaultUpdateLog(context.Background(), id, vaultLog)
		}, want: func(*persistworkstream.SQLiteStore) (any, error) { return vaultLog, nil }},
		{name: "queue_freeze", call: func(c *WorkstreamClient, id string) (any, error) {
			return c.SaveQueueFreeze(context.Background(), id, freeze)
		}, want: func(*persistworkstream.SQLiteStore) (any, error) {
			normalized := freeze
			normalized.FreezeRevision = 1
			normalized.Status = domainworkstream.QueueFreezeActive
			normalized.UpdatedAt = normalized.CreatedAt
			return normalized, nil
		}},
		{name: "stage_run_receipt", call: func(c *WorkstreamClient, id string) (any, error) {
			return c.SaveStageRunReceipt(context.Background(), id, stageReceipt)
		}, want: func(*persistworkstream.SQLiteStore) (any, error) { return stageReceipt, nil }},
		{name: "closure_receipt", call: func(c *WorkstreamClient, id string) (any, error) {
			return c.SaveClosureReceipt(context.Background(), id, closureReceipt)
		}, want: func(*persistworkstream.SQLiteStore) (any, error) { return closureReceipt, nil }},
		{name: "acquire_lease", call: func(c *WorkstreamClient, id string) (any, error) {
			acquired, reason, err := c.AcquireImplementationLeaseIfUnfrozen(context.Background(), id, lease)
			return persistworkstream.WorkstreamLeaseAcquireResult{Acquired: acquired, Reason: reason}, err
		}, want: func(*persistworkstream.SQLiteStore) (any, error) {
			return persistworkstream.WorkstreamLeaseAcquireResult{Acquired: true}, nil
		}},
		{name: "resolve_freeze_and_lease", prepare: func(s *persistworkstream.SQLiteStore) error {
			return s.SaveQueueFreeze(context.Background(), resolveFreeze)
		}, call: func(c *WorkstreamClient, id string) (any, error) {
			freezeResult, leaseResult, acquired, err := c.ResolveQueueFreezeAndAcquireLease(context.Background(), id, resolveFreeze.FreezeID, resolution, replacement)
			return persistworkstream.WorkstreamFreezeLeaseResult{Freeze: freezeResult, Lease: leaseResult, Acquired: acquired}, err
		}, want: func(s *persistworkstream.SQLiteStore) (any, error) {
			freezeResult, found, err := s.GetQueueFreeze(context.Background(), resolveFreeze.FreezeID)
			if err != nil || !found {
				return nil, err
			}
			leaseResult, found, err := s.GetImplementationLease(context.Background(), replacement.LeaseName)
			if err != nil || !found {
				return nil, err
			}
			return persistworkstream.WorkstreamFreezeLeaseResult{Freeze: freezeResult, Lease: leaseResult, Acquired: true}, nil
		}},
		{name: "release_lease", prepare: func(s *persistworkstream.SQLiteStore) error {
			_, err := s.AcquireImplementationLease(context.Background(), lease)
			return err
		}, call: func(c *WorkstreamClient, id string) (any, error) {
			return struct{}{}, c.ReleaseImplementationLease(context.Background(), id, lease.LeaseName, lease.HolderUnitID)
		}, want: func(*persistworkstream.SQLiteStore) (any, error) { return struct{}{}, nil }},
		{name: "heartbeat_lease", prepare: func(s *persistworkstream.SQLiteStore) error {
			_, err := s.AcquireImplementationLease(context.Background(), lease)
			return err
		}, call: func(c *WorkstreamClient, id string) (any, error) {
			return struct{}{}, c.HeartbeatImplementationLease(context.Background(), id, heartbeat)
		}, want: func(*persistworkstream.SQLiteStore) (any, error) { return struct{}{}, nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			store, owner, handler, server, client := newWorkstreamStorageHostFixture(t, root)
			if tc.prepare != nil {
				if err := tc.prepare(store); err != nil {
					t.Fatalf("prepare owner state: %v", err)
				}
			}
			requestID := "workstream-recovery-" + tc.name
			handler.crashAfterCommitFor = requestID
			if _, err := tc.call(client, requestID); storageHostErrorCode(err) != ErrorCodeOutcomeUnknown {
				t.Fatalf("first lost-response call err=%v, want outcome_unknown", err)
			}
			if got := owner.saveCalls.Load(); got != 1 {
				t.Fatalf("owner mutation count before restart=%d, want 1", got)
			}
			want, err := tc.want(store)
			if err != nil {
				t.Fatalf("capture original effect result: %v", err)
			}
			server.Close()
			if err := handler.Close(); err != nil {
				t.Fatalf("close storagehost: %v", err)
			}
			if err := store.Close(); err != nil {
				t.Fatalf("close owner: %v", err)
			}

			store, owner, handler, server, client = newWorkstreamStorageHostFixture(t, root)
			defer server.Close()
			defer handler.Close()
			defer store.Close()
			got, err := tc.call(client, requestID)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("recovered result=%#v err=%v, want original %#v", got, err, want)
			}
			if count := owner.saveCalls.Load(); count != 0 {
				t.Fatalf("owner mutation reruns after restart=%d, want 0", count)
			}
		})
	}
}

func storageHostErrorCode(err error) string {
	var storageErr *Error
	if errors.As(err, &storageErr) {
		return storageErr.Code
	}
	return ""
}
