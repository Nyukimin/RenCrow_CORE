package workstream

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
)

func TestJSONLStoreSaveAndListWorkstreamRecords(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	ctx := context.Background()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	if err := store.SaveWorkstream(ctx, domainworkstream.Workstream{
		WorkstreamID: "ws_1",
		Name:         "収益化",
		Status:       domainworkstream.StatusActive,
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("SaveWorkstream failed: %v", err)
	}
	if err := store.SaveGoal(ctx, domainworkstream.Goal{
		GoalID:          "goal_1",
		WorkstreamID:    "ws_1",
		Title:           "低単価商品を作る",
		SuccessCriteria: []string{"対象読者が明確"},
		Verification:    []string{"Revenue checklist"},
		Status:          domainworkstream.StatusActive,
		CreatedAt:       now,
	}); err != nil {
		t.Fatalf("SaveGoal failed: %v", err)
	}
	if err := store.SaveArtifact(ctx, domainworkstream.Artifact{
		ArtifactID:   "art_1",
		WorkstreamID: "ws_1",
		Type:         "markdown",
		FilePath:     "vault/workstreams/ws_1/STATUS.md",
		Status:       "draft",
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("SaveArtifact failed: %v", err)
	}
	if err := store.SaveArtifactAnnotation(ctx, domainworkstream.ArtifactAnnotation{
		AnnotationID: "ann_1",
		ArtifactID:   "art_1",
		Comment:      "見出しが抽象的",
		Status:       "open",
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("SaveArtifactAnnotation failed: %v", err)
	}
	if err := store.SaveSteeringItem(ctx, domainworkstream.SteeringItem{
		SteeringID:   "stq_1",
		WorkstreamID: "ws_1",
		Instruction:  "CTAを具体化する",
		Status:       "pending",
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("SaveSteeringItem failed: %v", err)
	}
	if err := store.SaveHeartbeatSchedule(ctx, domainworkstream.HeartbeatSchedule{
		HeartbeatID:  "hb_1",
		WorkstreamID: "ws_1",
		ScheduleText: "daily 08:00",
		Task:         "draft report only",
		Status:       domainworkstream.StatusActive,
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("SaveHeartbeatSchedule failed: %v", err)
	}
	if err := store.SaveVaultUpdateLog(ctx, domainworkstream.VaultUpdateLog{
		UpdateID:     "upd_1",
		WorkstreamID: "ws_1",
		FilePath:     "vault/workstreams/ws_1/STATUS.md",
		UpdateType:   "status",
		ReviewStatus: "pending",
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("SaveVaultUpdateLog failed: %v", err)
	}

	workstreams, err := store.ListWorkstreams(ctx, 10)
	if err != nil || len(workstreams) != 1 || workstreams[0].WorkstreamID != "ws_1" {
		t.Fatalf("workstreams=%#v err=%v", workstreams, err)
	}
	goals, err := store.ListGoals(ctx, 10)
	if err != nil || len(goals) != 1 || goals[0].GoalID != "goal_1" {
		t.Fatalf("goals=%#v err=%v", goals, err)
	}
	artifacts, err := store.ListArtifacts(ctx, 10)
	if err != nil || len(artifacts) != 1 || artifacts[0].ArtifactID != "art_1" {
		t.Fatalf("artifacts=%#v err=%v", artifacts, err)
	}
	annotations, err := store.ListArtifactAnnotations(ctx, 10)
	if err != nil || len(annotations) != 1 || annotations[0].AnnotationID != "ann_1" {
		t.Fatalf("annotations=%#v err=%v", annotations, err)
	}
	steering, err := store.ListSteeringItems(ctx, 10)
	if err != nil || len(steering) != 1 || steering[0].SteeringID != "stq_1" {
		t.Fatalf("steering=%#v err=%v", steering, err)
	}
	heartbeats, err := store.ListHeartbeatSchedules(ctx, 10)
	if err != nil || len(heartbeats) != 1 || heartbeats[0].HeartbeatID != "hb_1" {
		t.Fatalf("heartbeats=%#v err=%v", heartbeats, err)
	}
	vaultUpdates, err := store.ListVaultUpdateLogs(ctx, 10)
	if err != nil || len(vaultUpdates) != 1 || vaultUpdates[0].UpdateID != "upd_1" {
		t.Fatalf("vaultUpdates=%#v err=%v", vaultUpdates, err)
	}
}

func TestJSONLStoreArtifactPayloadSurvivesReopen(t *testing.T) {
	root := t.TempDir()
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

	store := NewJSONLStore(root)
	if err := store.SaveArtifact(ctx, expected); err != nil {
		t.Fatalf("SaveArtifact failed: %v", err)
	}

	reopened := NewJSONLStore(root)
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

func TestJSONLStoreImplementationLeaseIsSingletonAndRecoverable(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	now := time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC)
	first := domainworkstream.ImplementationLease{LeaseName: "atlas_implementation", HolderUnitID: "unit-1", HolderWorkstreamID: "ws-1", Stage: "QUEUED", AcquiredAt: now, HeartbeatAt: now}
	second := first
	second.HolderUnitID = "unit-2"
	second.HolderWorkstreamID = "ws-2"
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
	if err := store.ReleaseImplementationLease(context.Background(), "atlas_implementation", "unit-1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.GetImplementationLease(context.Background(), "atlas_implementation"); err != nil || ok {
		t.Fatalf("released lease still active ok=%v err=%v", ok, err)
	}
}

func TestJSONLStoreLifecycleReceiptsAndFreezeSurviveReopen(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	now := time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)
	store := NewJSONLStore(root)
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

	reopened := NewJSONLStore(root)
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

func TestJSONLStoreAtomicFreezeLeaseOperationsAreIdempotentAndPersisted(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	now := time.Date(2026, 8, 22, 1, 0, 0, 0, time.UTC)
	store := NewJSONLStore(root)
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
	reopened := NewJSONLStore(root)
	persistedFreeze, found, err := reopened.GetQueueFreeze(ctx, "freeze-atomic")
	if err != nil || !found || persistedFreeze.Status != domainworkstream.QueueFreezeResolved || persistedFreeze.SupersedesUnitID != resolution.SupersedesUnitID || persistedFreeze.ResolutionPayloadHash != resolution.ResolutionPayloadHash || len(persistedFreeze.BlockerResolutionRefs) != 1 {
		t.Fatalf("reopened freeze=%+v found=%v err=%v", persistedFreeze, found, err)
	}
	persistedLease, found, err := reopened.GetImplementationLease(ctx, replacement.LeaseName)
	if err != nil || !found || persistedLease.HolderUnitID != replacement.HolderUnitID {
		t.Fatalf("reopened lease=%+v found=%v err=%v", persistedLease, found, err)
	}
}

func TestJSONLStoreFreezeResolutionRecoversAfterResolvedAppendFailure(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	now := time.Date(2026, 8, 22, 3, 0, 0, 0, time.UTC)
	store := NewJSONLStore(root)
	if err := store.SaveQueueFreeze(ctx, domainworkstream.QueueFreeze{
		FreezeID: "freeze-crash", BlockedUnitID: "unit-blocked", BlockedRevision: 2, FreezeRevision: 7,
		ReasonCode: "blocked", InvalidatedFromStage: "BUILD", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	replacement := domainworkstream.ImplementationLease{
		LeaseName: "atlas_implementation", HolderUnitID: "unit-replacement", HolderWorkstreamID: "ws-replacement",
		Stage: "QUEUED", Revision: "1", AcquiredAt: now, HeartbeatAt: now,
	}
	resolution := domainworkstream.QueueFreezeResolution{
		ExpectedFreezeRevision: 7,
		ResolutionRequestID:    "resolve-crash-1",
		ReplacementUnitID:      "unit-replacement",
		SupersedesUnitID:       "unit-blocked",
		BlockerResolutionRefs:  []domainbacklog.EvidenceRef{{Kind: "fix", Ref: "fix-crash", Verified: true, VerificationResult: domainbacklog.EvidenceVerificationVerified}},
		ResolutionPayloadHash:  "resolution-crash-hash",
	}
	store.resolutionAppendHook = func(stage string) error {
		if stage == "after_lease_before_resolved_freeze" {
			return errors.New("simulated crash between lease and resolved freeze")
		}
		return nil
	}
	if _, _, acquired, err := store.ResolveQueueFreezeAndAcquireLease(ctx, "freeze-crash", resolution, replacement); err == nil || acquired {
		t.Fatalf("faulted resolution acquired=%v err=%v", acquired, err)
	}

	reopened := NewJSONLStore(root)
	pending, found, err := reopened.GetQueueFreeze(ctx, "freeze-crash")
	if err != nil || !found || pending.Status != domainworkstream.QueueFreezeActive {
		t.Fatalf("reopened pending freeze=%+v found=%v err=%v", pending, found, err)
	}
	if pending.ResolutionRequestID != resolution.ResolutionRequestID || pending.SupersedesUnitID != resolution.SupersedesUnitID || pending.ResolutionPayloadHash != resolution.ResolutionPayloadHash || len(pending.BlockerResolutionRefs) != 1 {
		t.Fatalf("pending freeze lost request metadata: %+v", pending)
	}
	persistedLease, leaseFound, err := reopened.GetImplementationLease(ctx, replacement.LeaseName)
	if err != nil || !leaseFound || persistedLease.HolderUnitID != replacement.HolderUnitID {
		t.Fatalf("replacement lease after fault=%+v found=%v err=%v", persistedLease, leaseFound, err)
	}

	other := replacement
	other.HolderUnitID = "unit-other"
	if acquired, reason, err := reopened.AcquireImplementationLeaseIfUnfrozen(ctx, other); err != nil || acquired || reason != domainworkstream.ErrQueueFrozen.Error() {
		t.Fatalf("active pending freeze must block other unit acquired=%v reason=%q err=%v", acquired, reason, err)
	}

	conflicting := resolution
	conflicting.ResolutionPayloadHash = "different-resolution"
	if _, _, acquired, err := reopened.ResolveQueueFreezeAndAcquireLease(ctx, "freeze-crash", conflicting, replacement); !errors.Is(err, domainworkstream.ErrQueueFreezeResolutionConflict) || acquired {
		t.Fatalf("conflicting replay acquired=%v err=%v", acquired, err)
	}

	resolved, resolvedLease, acquired, err := reopened.ResolveQueueFreezeAndAcquireLease(ctx, "freeze-crash", resolution, replacement)
	if err != nil || !acquired || resolved.Status != domainworkstream.QueueFreezeResolved || resolvedLease.HolderUnitID != replacement.HolderUnitID {
		t.Fatalf("exact replay resolution freeze=%+v lease=%+v acquired=%v err=%v", resolved, resolvedLease, acquired, err)
	}
	reopenedAgain := NewJSONLStore(root)
	final, found, err := reopenedAgain.GetQueueFreeze(ctx, "freeze-crash")
	if err != nil || !found || final.Status != domainworkstream.QueueFreezeResolved || !final.MatchesResolved(resolution) {
		t.Fatalf("final resolved freeze=%+v found=%v err=%v", final, found, err)
	}
}

func TestJSONLStoreListsLatestVaultUpdatePerID(t *testing.T) {
	ctx := context.Background()
	store := NewJSONLStore(t.TempDir())
	now := time.Date(2026, 5, 20, 0, 20, 0, 0, time.UTC)
	for _, item := range []domainworkstream.VaultUpdateLog{
		{
			UpdateID:        "upd_1",
			WorkstreamID:    "ws_1",
			FilePath:        "vault/workstreams/ws_1/STATUS.md",
			ProposedContent: "draft",
			ReviewStatus:    "pending",
			CreatedAt:       now,
		},
		{
			UpdateID:        "upd_1",
			WorkstreamID:    "ws_1",
			FilePath:        "vault/workstreams/ws_1/STATUS.md",
			ProposedContent: "adopted",
			ReviewStatus:    "adopted",
			CreatedAt:       now.Add(time.Second),
		},
	} {
		if err := store.SaveVaultUpdateLog(ctx, item); err != nil {
			t.Fatalf("SaveVaultUpdateLog failed: %v", err)
		}
	}
	vaultUpdates, err := store.ListVaultUpdateLogs(ctx, 10)
	if err != nil {
		t.Fatalf("ListVaultUpdateLogs failed: %v", err)
	}
	if len(vaultUpdates) != 1 || vaultUpdates[0].UpdateID != "upd_1" || vaultUpdates[0].ReviewStatus != "adopted" {
		t.Fatalf("vaultUpdates=%#v, want latest adopted state only", vaultUpdates)
	}
}

func TestJSONLStoreRejectsGoalWithoutContract(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	err := store.SaveGoal(context.Background(), domainworkstream.Goal{
		GoalID:       "goal_1",
		WorkstreamID: "ws_1",
		Title:        "missing contract",
	})
	if err == nil {
		t.Fatal("expected missing success criteria / verification to fail")
	}
}

func TestJSONLStoreFindGoalByIDReturnsLatestExactRecord(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	ctx := context.Background()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	first := domainworkstream.Goal{
		GoalID: "goal_exact", WorkstreamID: "ws_1", Title: "first",
		SuccessCriteria: []string{"one"}, Verification: []string{"check"}, Status: domainworkstream.StatusDraft, CreatedAt: now,
	}
	second := first
	second.Title = "latest"
	second.CreatedAt = now.Add(time.Second)
	if err := store.SaveGoal(ctx, first); err != nil {
		t.Fatalf("SaveGoal(first) failed: %v", err)
	}
	if err := store.SaveGoal(ctx, second); err != nil {
		t.Fatalf("SaveGoal(second) failed: %v", err)
	}
	got, found, err := store.FindGoalByID(ctx, "goal_exact")
	if err != nil || !found || got.Title != "latest" {
		t.Fatalf("FindGoalByID() = %#v, found=%v, err=%v", got, found, err)
	}
	if _, found, err := store.FindGoalByID(ctx, "missing"); err != nil || found {
		t.Fatalf("missing FindGoalByID() found=%v err=%v", found, err)
	}
}

func TestJSONLStoreRejectsInvalidArtifactAndSteering(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	ctx := context.Background()
	if err := store.SaveArtifact(ctx, domainworkstream.Artifact{
		ArtifactID: "art_1",
	}); err == nil {
		t.Fatal("expected invalid artifact to fail")
	}
	if err := store.SaveArtifactAnnotation(ctx, domainworkstream.ArtifactAnnotation{
		AnnotationID: "ann_1",
	}); err == nil {
		t.Fatal("expected invalid artifact annotation to fail")
	}
	if err := store.SaveSteeringItem(ctx, domainworkstream.SteeringItem{
		SteeringID: "stq_1",
	}); err == nil {
		t.Fatal("expected invalid steering item to fail")
	}
	if err := store.SaveHeartbeatSchedule(ctx, domainworkstream.HeartbeatSchedule{
		HeartbeatID: "hb_1",
	}); err == nil {
		t.Fatal("expected invalid heartbeat schedule to fail")
	}
	if err := store.SaveVaultUpdateLog(ctx, domainworkstream.VaultUpdateLog{
		UpdateID: "upd_1",
	}); err == nil {
		t.Fatal("expected invalid vault update log to fail")
	}
}

func TestJSONLStoreWithVaultCreatesInitialFiles(t *testing.T) {
	root := t.TempDir()
	vaultRoot := filepath.Join(root, "vault")
	store := NewJSONLStoreWithVault(filepath.Join(root, "logs"), vaultRoot)
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	if err := store.SaveWorkstream(context.Background(), domainworkstream.Workstream{
		WorkstreamID: "ws_revenue",
		Name:         "収益化",
		Description:  "Revenue loop",
		Status:       domainworkstream.StatusActive,
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("SaveWorkstream failed: %v", err)
	}

	for _, name := range []string{"README.md", "STATUS.md", "TODO.md", "OPEN_LOOPS.md", "ARTIFACTS.md", "NOTES.md", "MEMORY.md"} {
		if _, err := os.Stat(filepath.Join(vaultRoot, "ws_revenue", name)); err != nil {
			t.Fatalf("expected %s to be created: %v", name, err)
		}
	}
	workstreams, err := store.ListWorkstreams(context.Background(), 1)
	if err != nil {
		t.Fatalf("ListWorkstreams failed: %v", err)
	}
	if len(workstreams) != 1 || workstreams[0].VaultPath != filepath.Join(vaultRoot, "ws_revenue") {
		t.Fatalf("unexpected workstream record: %#v", workstreams)
	}
}

func TestJSONLStoreWithVaultDoesNotOverwriteExistingFiles(t *testing.T) {
	root := t.TempDir()
	vaultRoot := filepath.Join(root, "vault")
	readme := filepath.Join(vaultRoot, "ws_existing", "README.md")
	if err := os.MkdirAll(filepath.Dir(readme), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(readme, []byte("existing content\n"), 0644); err != nil {
		t.Fatalf("write existing README: %v", err)
	}
	store := NewJSONLStoreWithVault(filepath.Join(root, "logs"), vaultRoot)

	if err := store.SaveWorkstream(context.Background(), domainworkstream.Workstream{
		WorkstreamID: "ws_existing",
		Name:         "Existing",
		Status:       domainworkstream.StatusActive,
		CreatedAt:    time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("SaveWorkstream failed: %v", err)
	}
	content, err := os.ReadFile(readme)
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	if string(content) != "existing content\n" {
		t.Fatalf("README was overwritten: %q", content)
	}
}

func TestJSONLStoreWithVaultRejectsUnsafeWorkstreamID(t *testing.T) {
	store := NewJSONLStoreWithVault(t.TempDir(), t.TempDir())
	err := store.SaveWorkstream(context.Background(), domainworkstream.Workstream{
		WorkstreamID: "../escape",
		Name:         "escape",
		Status:       domainworkstream.StatusActive,
		CreatedAt:    time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
	})
	if err == nil || !strings.Contains(err.Error(), "invalid workstream_id") {
		t.Fatalf("expected unsafe workstream id to fail, got %v", err)
	}
}

func TestJSONLStoreApplyVaultUpdateWritesAdoptedProposedContent(t *testing.T) {
	root := t.TempDir()
	vaultRoot := filepath.Join(root, "vault")
	store := NewJSONLStoreWithVault(filepath.Join(root, "logs"), vaultRoot)

	appliedPath, err := store.ApplyVaultUpdate(context.Background(), domainworkstream.VaultUpdateLog{
		UpdateID:        "upd_1",
		WorkstreamID:    "ws_1",
		FilePath:        "ws_1/STATUS.md",
		ProposedContent: "# STATUS\n\nadopted\n",
		ReviewStatus:    domainworkstream.VaultReviewAdopted,
		CreatedAt:       time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("ApplyVaultUpdate failed: %v", err)
	}
	expectedPath := filepath.Join(vaultRoot, "ws_1", "STATUS.md")
	if appliedPath != expectedPath {
		t.Fatalf("path=%q want %q", appliedPath, expectedPath)
	}
	content, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("read applied file: %v", err)
	}
	if string(content) != "# STATUS\n\nadopted\n" {
		t.Fatalf("content=%q", content)
	}
}

func TestJSONLStoreApplyVaultUpdateAppendsStructuredContent(t *testing.T) {
	root := t.TempDir()
	vaultRoot := filepath.Join(root, "vault")
	statusPath := filepath.Join(vaultRoot, "ws_1", "STATUS.md")
	if err := os.MkdirAll(filepath.Dir(statusPath), 0o755); err != nil {
		t.Fatalf("mkdir status dir: %v", err)
	}
	if err := os.WriteFile(statusPath, []byte("# STATUS\n\n## Current Goal\n\n既存\n"), 0o644); err != nil {
		t.Fatalf("write status: %v", err)
	}
	store := NewJSONLStoreWithVault(filepath.Join(root, "logs"), vaultRoot)
	item := domainworkstream.VaultUpdateLog{
		UpdateID:        "upd_append",
		WorkstreamID:    "ws_1",
		FilePath:        "ws_1/STATUS.md",
		UpdateType:      "append_status",
		ProposedContent: "- Next Action: Source Registry relation確認済み\n",
		ReviewStatus:    domainworkstream.VaultReviewAdopted,
		CreatedAt:       time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
	}

	preview, err := store.PreviewVaultUpdate(context.Background(), item)
	if err != nil {
		t.Fatalf("PreviewVaultUpdate failed: %v", err)
	}
	if !strings.Contains(preview.ProposedContent, "## 2026-05-18 status upd_append") {
		t.Fatalf("preview proposed content missing structured heading: %q", preview.ProposedContent)
	}
	if !strings.Contains(preview.UnifiedDiff, "+## 2026-05-18 status upd_append") {
		t.Fatalf("preview diff missing appended heading: %q", preview.UnifiedDiff)
	}

	appliedPath, err := store.ApplyVaultUpdate(context.Background(), item)
	if err != nil {
		t.Fatalf("ApplyVaultUpdate failed: %v", err)
	}
	if appliedPath != statusPath {
		t.Fatalf("path=%q want %q", appliedPath, statusPath)
	}
	content, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatalf("read applied status: %v", err)
	}
	got := string(content)
	if !strings.Contains(got, "## Current Goal\n\n既存") {
		t.Fatalf("existing content was not preserved: %q", got)
	}
	if !strings.Contains(got, "## 2026-05-18 status upd_append") || !strings.Contains(got, "Source Registry relation確認済み") {
		t.Fatalf("structured append missing from content: %q", got)
	}
}

func TestJSONLStoreApplyVaultUpdateRejectsTraversalPath(t *testing.T) {
	store := NewJSONLStoreWithVault(t.TempDir(), t.TempDir())

	_, err := store.ApplyVaultUpdate(context.Background(), domainworkstream.VaultUpdateLog{
		UpdateID:        "upd_1",
		WorkstreamID:    "ws_1",
		FilePath:        "../outside.md",
		ProposedContent: "escape",
		ReviewStatus:    domainworkstream.VaultReviewAdopted,
		CreatedAt:       time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
	})
	if err == nil || !strings.Contains(err.Error(), "escapes vault root") {
		t.Fatalf("expected traversal to fail, got %v", err)
	}
}

func TestJSONLStoreApplyVaultUpdateRequiresAdoptedReview(t *testing.T) {
	store := NewJSONLStoreWithVault(t.TempDir(), t.TempDir())

	_, err := store.ApplyVaultUpdate(context.Background(), domainworkstream.VaultUpdateLog{
		UpdateID:        "upd_1",
		WorkstreamID:    "ws_1",
		FilePath:        "ws_1/STATUS.md",
		ProposedContent: "# STATUS\n",
		ReviewStatus:    domainworkstream.VaultReviewPending,
		CreatedAt:       time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
	})
	if err == nil || !strings.Contains(err.Error(), "must be adopted") {
		t.Fatalf("expected non-adopted review to fail, got %v", err)
	}
}
