package workstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
)

func TestSQLiteStoreConfiguresSerializedBusyTimeout(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "workstream.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	if got := store.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", got)
	}
	var busyTimeout int
	if err := store.db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("busy_timeout query failed: %v", err)
	}
	if busyTimeout != sqliteBusyTimeoutMilliseconds {
		t.Fatalf("busy_timeout = %d, want %d", busyTimeout, sqliteBusyTimeoutMilliseconds)
	}
}

func TestSQLiteStoreImplementationLeaseIsSingletonAndPersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workstream.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC)
	first := domainworkstream.ImplementationLease{LeaseName: "atlas_implementation", HolderUnitID: "unit-1", HolderWorkstreamID: "ws-1", Stage: "QUEUED", AcquiredAt: now, HeartbeatAt: now}
	second := first
	second.HolderUnitID = "unit-2"
	acquired, err := store.AcquireImplementationLease(context.Background(), first)
	if err != nil || !acquired {
		t.Fatalf("first acquire=%v err=%v", acquired, err)
	}
	acquired, err = store.AcquireImplementationLease(context.Background(), second)
	if err != nil || acquired {
		t.Fatalf("second acquire=%v err=%v", acquired, err)
	}
	got, ok, err := store.GetImplementationLease(context.Background(), "atlas_implementation")
	if err != nil || !ok || got.HolderUnitID != "unit-1" {
		t.Fatalf("lease=%+v ok=%v err=%v", got, ok, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, ok, err = reopened.GetImplementationLease(context.Background(), "atlas_implementation")
	if err != nil || !ok || got.HolderUnitID != "unit-1" {
		t.Fatalf("reopened lease=%+v ok=%v err=%v", got, ok, err)
	}
}

func TestSQLiteStoreLifecycleReceiptsAndFreezeSurviveReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workstream.db")
	ctx := context.Background()
	now := time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveQueueFreeze(ctx, domainworkstream.QueueFreeze{
		FreezeID: "freeze-1", BlockedUnitID: "unit-1", BlockedRevision: 2,
		ReasonCode: "dependency", InvalidatedFromStage: "BUILD", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStageRunReceipt(ctx, domainworkstream.StageRunReceipt{
		ReceiptID: "stage-1", IdempotencyKey: "unit-1:2:BUILD", UnitID: "unit-1",
		ImplementationRevision: 2, TargetStage: "BUILD", PayloadHash: "hash-1",
		Status: domainworkstream.StageRunCompleted, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveClosureReceipt(ctx, domainworkstream.ClosureReceipt{
		ReceiptID: "closure-1", IdempotencyKey: "unit-1:2:DONE", UnitID: "unit-1",
		ImplementationRevision: 2, Phase: domainworkstream.ClosurePhasePrepared,
		Status: domainworkstream.ClosureStatusPrepared, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	freeze, found, err := reopened.GetQueueFreeze(ctx, "freeze-1")
	if err != nil || !found || freeze.BlockedRevision != 2 {
		t.Fatalf("freeze=%+v found=%v err=%v", freeze, found, err)
	}
	stage, found, err := reopened.FindStageRunReceipt(ctx, "unit-1:2:BUILD")
	if err != nil || !found || stage.PayloadHash != "hash-1" {
		t.Fatalf("stage=%+v found=%v err=%v", stage, found, err)
	}
	closure, found, err := reopened.FindClosureReceipt(ctx, "unit-1:2:DONE")
	if err != nil || !found || closure.Phase != domainworkstream.ClosurePhasePrepared {
		t.Fatalf("closure=%+v found=%v err=%v", closure, found, err)
	}
}

func TestSQLiteStoreAtomicFreezeLeaseOperationsAreIdempotentAndPersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workstream.db")
	ctx := context.Background()
	now := time.Date(2026, 8, 22, 1, 0, 0, 0, time.UTC)
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	initial := domainworkstream.ImplementationLease{LeaseName: "atlas_implementation", HolderUnitID: "unit-initial", HolderWorkstreamID: "ws-initial", Stage: "QUEUED", AcquiredAt: now, HeartbeatAt: now}
	if acquired, reason, err := store.AcquireImplementationLeaseIfUnfrozen(ctx, initial); err != nil || !acquired || reason != "" {
		t.Fatalf("initial atomic acquire=%v reason=%q err=%v", acquired, reason, err)
	}
	if err := store.ReleaseImplementationLease(ctx, initial.LeaseName, initial.HolderUnitID); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveQueueFreeze(ctx, domainworkstream.QueueFreeze{
		FreezeID: "freeze-atomic", BlockedUnitID: "unit-blocked", BlockedRevision: 2, FreezeRevision: 7,
		ReasonCode: "blocked", InvalidatedFromStage: "BUILD", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	blockedLease := initial
	blockedLease.HolderUnitID = "unit-blocked"
	if acquired, reason, err := store.AcquireImplementationLeaseIfUnfrozen(ctx, blockedLease); err != nil || acquired || reason != domainworkstream.ErrQueueFrozen.Error() {
		t.Fatalf("frozen atomic acquire=%v reason=%q err=%v", acquired, reason, err)
	}
	replacement := initial
	replacement.HolderUnitID = "unit-replacement"
	replacement.HolderWorkstreamID = "ws-replacement"
	resolution := domainworkstream.QueueFreezeResolution{
		ExpectedFreezeRevision: 7,
		ResolutionRequestID:    "resolve-1",
		ReplacementUnitID:      "unit-replacement",
		SupersedesUnitID:       "unit-blocked",
		BlockerResolutionRefs:  []domainbacklog.EvidenceRef{{Kind: "fix", Ref: "fix-1", Verified: true, VerificationResult: domainbacklog.EvidenceVerificationVerified}},
		ResolutionPayloadHash:  "resolution-hash-1",
	}
	resolved, lease, acquired, err := store.ResolveQueueFreezeAndAcquireLease(ctx, "freeze-atomic", resolution, replacement)
	if err != nil || !acquired || resolved.Status != domainworkstream.QueueFreezeResolved || lease.HolderUnitID != replacement.HolderUnitID {
		t.Fatalf("resolve freeze=%+v lease=%+v acquired=%v err=%v", resolved, lease, acquired, err)
	}
	if resolved.SupersedesUnitID != resolution.SupersedesUnitID || resolved.ReplacementUnitID != resolution.ReplacementUnitID || resolved.ResolutionPayloadHash != resolution.ResolutionPayloadHash || len(resolved.BlockerResolutionRefs) != 1 {
		t.Fatalf("resolved freeze lost complete metadata: %+v", resolved)
	}
	replayed, replayLease, replayAcquired, err := store.ResolveQueueFreezeAndAcquireLease(ctx, "freeze-atomic", resolution, replacement)
	if err != nil || !replayAcquired || replayed.ResolutionRequestID != resolved.ResolutionRequestID || replayLease.HolderUnitID != replacement.HolderUnitID {
		t.Fatalf("replay freeze=%+v lease=%+v acquired=%v err=%v", replayed, replayLease, replayAcquired, err)
	}
	sameRequestConflict := resolution
	sameRequestConflict.ResolutionPayloadHash = "different-resolution-hash"
	if _, _, _, err := store.ResolveQueueFreezeAndAcquireLease(ctx, "freeze-atomic", sameRequestConflict, replacement); !errors.Is(err, domainworkstream.ErrQueueFreezeResolutionConflict) {
		t.Fatalf("same request with different resolution payload err=%v", err)
	}
	conflicting := resolution
	conflicting.ResolutionRequestID = "resolve-2"
	if _, _, _, err := store.ResolveQueueFreezeAndAcquireLease(ctx, "freeze-atomic", conflicting, replacement); !errors.Is(err, domainworkstream.ErrQueueFreezeResolutionConflict) {
		t.Fatalf("conflicting resolution err=%v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persistedFreeze, found, err := reopened.GetQueueFreeze(ctx, "freeze-atomic")
	if err != nil || !found || persistedFreeze.Status != domainworkstream.QueueFreezeResolved || persistedFreeze.SupersedesUnitID != resolution.SupersedesUnitID || persistedFreeze.ResolutionPayloadHash != resolution.ResolutionPayloadHash || len(persistedFreeze.BlockerResolutionRefs) != 1 {
		t.Fatalf("reopened freeze=%+v found=%v err=%v", persistedFreeze, found, err)
	}
	persistedLease, found, err := reopened.GetImplementationLease(ctx, replacement.LeaseName)
	if err != nil || !found || persistedLease.HolderUnitID != replacement.HolderUnitID {
		t.Fatalf("reopened lease=%+v found=%v err=%v", persistedLease, found, err)
	}
}

func TestSQLiteStoreConcurrentGoalWrites(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "workstream.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	const workers = 8
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- store.SaveGoal(context.Background(), domainworkstream.Goal{
				GoalID:          fmt.Sprintf("concurrent-goal-%d", i),
				WorkstreamID:    "workstream-concurrent",
				Title:           "concurrent owner write",
				SuccessCriteria: []string{"saved"},
				Verification:    []string{"listed"},
				Status:          domainworkstream.StatusDraft,
				CreatedAt:       time.Now().UTC(),
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent SaveGoal failed: %v", err)
		}
	}
	goals, err := store.ListGoals(context.Background(), workers)
	if err != nil || len(goals) != workers {
		t.Fatalf("concurrent goal count = %d, err=%v; want %d", len(goals), err, workers)
	}
}

func TestSQLiteStoreSavesAndListsWorkstreamRecords(t *testing.T) {
	vaultRoot := filepath.Join(t.TempDir(), "vault")
	store, err := NewSQLiteStoreWithVault(filepath.Join(t.TempDir(), "workstream.db"), vaultRoot)
	if err != nil {
		t.Fatalf("NewSQLiteStoreWithVault() error = %v", err)
	}
	defer store.Close()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	if err := store.SaveWorkstream(context.Background(), domainworkstream.Workstream{
		WorkstreamID: "ws_1",
		Name:         "収益化",
		Status:       domainworkstream.StatusActive,
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("SaveWorkstream() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(vaultRoot, "ws_1", "STATUS.md")); err != nil {
		t.Fatalf("expected vault STATUS.md: %v", err)
	}
	if err := store.SaveGoal(context.Background(), domainworkstream.Goal{
		GoalID:          "goal_1",
		WorkstreamID:    "ws_1",
		Title:           "LPを作る",
		SuccessCriteria: []string{"CTAがある"},
		Verification:    []string{"Viewerで確認する"},
		Status:          domainworkstream.StatusActive,
		CreatedAt:       now,
	}); err != nil {
		t.Fatalf("SaveGoal() error = %v", err)
	}
	if err := store.SaveArtifact(context.Background(), domainworkstream.Artifact{
		ArtifactID:   "art_1",
		WorkstreamID: "ws_1",
		Type:         "markdown",
		Status:       "draft",
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("SaveArtifact() error = %v", err)
	}
	if err := store.SaveArtifactAnnotation(context.Background(), domainworkstream.ArtifactAnnotation{
		AnnotationID: "ann_1",
		ArtifactID:   "art_1",
		Comment:      "見出しが抽象的",
		Status:       "open",
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("SaveArtifactAnnotation() error = %v", err)
	}
	if err := store.SaveSteeringItem(context.Background(), domainworkstream.SteeringItem{
		SteeringID:   "stq_1",
		WorkstreamID: "ws_1",
		Instruction:  "CTAを直す",
		Status:       "pending",
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("SaveSteeringItem() error = %v", err)
	}
	if err := store.SaveHeartbeatSchedule(context.Background(), domainworkstream.HeartbeatSchedule{
		HeartbeatID:  "hb_1",
		WorkstreamID: "ws_1",
		ScheduleText: "daily 08:00",
		Task:         "確認",
		Status:       domainworkstream.StatusActive,
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("SaveHeartbeatSchedule() error = %v", err)
	}
	if err := store.SaveVaultUpdateLog(context.Background(), domainworkstream.VaultUpdateLog{
		UpdateID:     "vu_1",
		WorkstreamID: "ws_1",
		FilePath:     "STATUS.md",
		ReviewStatus: "pending",
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("SaveVaultUpdateLog() error = %v", err)
	}
	assertOne := func(name string, err error, got int) {
		t.Helper()
		if err != nil || got != 1 {
			t.Fatalf("%s count = %d, err = %v", name, got, err)
		}
	}
	workstreams, err := store.ListWorkstreams(context.Background(), 10)
	assertOne("workstreams", err, len(workstreams))
	goals, err := store.ListGoals(context.Background(), 10)
	assertOne("goals", err, len(goals))
	artifacts, err := store.ListArtifacts(context.Background(), 10)
	assertOne("artifacts", err, len(artifacts))
	annotations, err := store.ListArtifactAnnotations(context.Background(), 10)
	assertOne("annotations", err, len(annotations))
	steering, err := store.ListSteeringItems(context.Background(), 10)
	assertOne("steering", err, len(steering))
	heartbeats, err := store.ListHeartbeatSchedules(context.Background(), 10)
	assertOne("heartbeats", err, len(heartbeats))
	vaultUpdates, err := store.ListVaultUpdateLogs(context.Background(), 10)
	assertOne("vault updates", err, len(vaultUpdates))
}

func TestSQLiteStoreArtifactPayloadSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workstream.db")
	ctx := context.Background()
	payload := json.RawMessage(`{"schema":"development_methodology","version":1,"stages":["IMPLEMENT","VERIFY"]}`)
	expected := domainworkstream.Artifact{
		ArtifactID:   "artifact-payload-1",
		TraceID:      "trace-payload-1",
		WorkstreamID: "workstream-payload-1",
		Type:         "atlas_methodology",
		Title:        "Development methodology",
		Status:       domainworkstream.StatusDraft,
		Payload:      payload,
		CreatedAt:    time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC),
	}

	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	if err := store.SaveArtifact(ctx, expected); err != nil {
		_ = store.Close()
		t.Fatalf("SaveArtifact failed: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("reopen NewSQLiteStore() error = %v", err)
	}
	defer reopened.Close()
	artifacts, err := reopened.ListArtifacts(ctx, 10)
	if err != nil {
		t.Fatalf("ListArtifacts after reopen failed: %v", err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("reopened artifacts = %d, want 1", len(artifacts))
	}
	got := artifacts[0]
	if got.ArtifactID != expected.ArtifactID || got.TraceID != expected.TraceID || got.WorkstreamID != expected.WorkstreamID {
		t.Fatalf("reopened artifact identity = %+v, want %+v", got, expected)
	}
	if !json.Valid(got.Payload) || string(got.Payload) != string(payload) {
		t.Fatalf("reopened payload = %s, want valid payload %s", got.Payload, payload)
	}
}

func TestSQLiteStoreRejectsGoalWithoutSuccessCriteria(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "workstream.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	err = store.SaveGoal(context.Background(), domainworkstream.Goal{
		GoalID:       "goal_1",
		WorkstreamID: "ws_1",
		Title:        "LPを作る",
		Status:       domainworkstream.StatusActive,
		CreatedAt:    time.Now(),
	})
	if err == nil {
		t.Fatal("expected goal without success criteria to fail")
	}
}

func TestSQLiteStoreFindGoalByIDUsesExactPrimaryKey(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "workstream.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	item := domainworkstream.Goal{
		GoalID: "goal_exact", WorkstreamID: "ws_1", Title: "exact",
		SuccessCriteria: []string{"one"}, Verification: []string{"check"}, Status: domainworkstream.StatusDraft, CreatedAt: time.Now().UTC(),
	}
	if err := store.SaveGoal(ctx, item); err != nil {
		t.Fatalf("SaveGoal() failed: %v", err)
	}
	got, found, err := store.FindGoalByID(ctx, item.GoalID)
	if err != nil || !found || got.GoalID != item.GoalID || got.Title != item.Title {
		t.Fatalf("FindGoalByID() = %#v, found=%v, err=%v", got, found, err)
	}
	if _, found, err := store.FindGoalByID(ctx, "missing"); err != nil || found {
		t.Fatalf("missing FindGoalByID() found=%v err=%v", found, err)
	}
}
