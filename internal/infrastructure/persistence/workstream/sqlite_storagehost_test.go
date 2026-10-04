package workstream

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func workstreamStorageHostTestGoal() domainworkstream.Goal {
	return domainworkstream.Goal{
		GoalID: "goal-storagehost-receipt", TraceID: "trace-storagehost-receipt", WorkstreamID: "ws-storagehost",
		Title: "Receipt proof", SuccessCriteria: []string{"effect and receipt commit together"},
		Verification: []string{"reopen and compare"}, Status: domainworkstream.StatusDraft,
		CreatedAt: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC),
	}
}

func TestWorkstreamTypedStorageHostRowsCommitExactResultAndEffectReceipt(t *testing.T) {
	now := time.Date(2026, 10, 4, 13, 0, 0, 0, time.UTC)
	workstream := domainworkstream.Workstream{WorkstreamID: "ws-host-save", Name: "storage host", Status: domainworkstream.StatusActive, VaultPath: "vault/workstreams/ws-host-save", CreatedAt: now}
	goal := workstreamStorageHostTestGoal()
	artifact := domainworkstream.Artifact{ArtifactID: "artifact-host-save", WorkstreamID: workstream.WorkstreamID, Type: "markdown", Status: "draft", CreatedAt: now}
	annotation := domainworkstream.ArtifactAnnotation{AnnotationID: "annotation-host-save", ArtifactID: artifact.ArtifactID, Comment: "check proof", Status: "open", CreatedAt: now}
	steering := domainworkstream.SteeringItem{SteeringID: "steering-host-save", WorkstreamID: workstream.WorkstreamID, Instruction: "keep changes bounded", Status: "pending", CreatedAt: now}
	schedule := domainworkstream.HeartbeatSchedule{ScheduleID: modulecore.NewScheduleID(), WorkstreamID: workstream.WorkstreamID, ScheduleText: "daily", Task: "draft only", Status: domainworkstream.StatusActive, CreatedAt: now}
	vaultLog := domainworkstream.VaultUpdateLog{UpdateID: "update-host-save", WorkstreamID: workstream.WorkstreamID, FilePath: "vault/workstreams/ws-host-save/STATUS.md", ReviewStatus: domainworkstream.VaultReviewPending, CreatedAt: now}
	freeze := domainworkstream.QueueFreeze{FreezeID: "freeze-host-save", BlockedUnitID: "unit-host-save", BlockedRevision: 3, ReasonCode: "blocked", InvalidatedFromStage: "BUILD", CreatedAt: now}
	stageReceipt := domainworkstream.StageRunReceipt{ReceiptID: modulecore.NewReceiptID(), IdempotencyKey: "host-save:stage", UnitID: "unit-host-save", ImplementationRevision: 3, TargetStage: "BUILD", PayloadHash: "payload-hash", Status: domainworkstream.StageRunCompleted, CreatedAt: now}
	closureReceipt := domainworkstream.ClosureReceipt{ReceiptID: modulecore.NewReceiptID(), IdempotencyKey: "host-save:closure", UnitID: "unit-host-save", ImplementationRevision: 3, Phase: domainworkstream.ClosurePhasePrepared, Status: domainworkstream.ClosureStatusPrepared, CreatedAt: now}

	for _, tc := range []struct {
		name, operation, table, idColumn, effectID string
		save                                       func(*SQLiteStore, WorkstreamStorageHostOperationIdentity) (any, error)
	}{
		{"workstream_row", WorkstreamSaveWorkstreamStorageHostOperation, "workstream", "workstream_id", workstream.WorkstreamID, func(s *SQLiteStore, id WorkstreamStorageHostOperationIdentity) (any, error) {
			return s.SaveWorkstreamRowForStorageHostOperation(context.Background(), id, workstream)
		}},
		{"goal", WorkstreamSaveGoalStorageHostOperation, "workstream_goal", "goal_id", goal.GoalID, func(s *SQLiteStore, id WorkstreamStorageHostOperationIdentity) (any, error) {
			return s.SaveGoalForStorageHostOperation(context.Background(), id, goal)
		}},
		{"artifact", WorkstreamSaveArtifactStorageHostOperation, "artifact", "artifact_id", artifact.ArtifactID, func(s *SQLiteStore, id WorkstreamStorageHostOperationIdentity) (any, error) {
			return s.SaveArtifactForStorageHostOperation(context.Background(), id, artifact)
		}},
		{"artifact_annotation", WorkstreamSaveArtifactAnnotationStorageHostOperation, "artifact_annotation", "annotation_id", annotation.AnnotationID, func(s *SQLiteStore, id WorkstreamStorageHostOperationIdentity) (any, error) {
			return s.SaveArtifactAnnotationForStorageHostOperation(context.Background(), id, annotation)
		}},
		{"steering_item", WorkstreamSaveSteeringItemStorageHostOperation, "steering_queue", "steering_id", steering.SteeringID, func(s *SQLiteStore, id WorkstreamStorageHostOperationIdentity) (any, error) {
			return s.SaveSteeringItemForStorageHostOperation(context.Background(), id, steering)
		}},
		{"heartbeat_schedule", WorkstreamSaveHeartbeatScheduleStorageHostOperation, "heartbeat_schedule", "schedule_id", string(schedule.ScheduleID), func(s *SQLiteStore, id WorkstreamStorageHostOperationIdentity) (any, error) {
			return s.SaveHeartbeatScheduleForStorageHostOperation(context.Background(), id, schedule)
		}},
		{"vault_update_log", WorkstreamSaveVaultUpdateLogStorageHostOperation, "vault_update_log", "update_id", vaultLog.UpdateID, func(s *SQLiteStore, id WorkstreamStorageHostOperationIdentity) (any, error) {
			return s.SaveVaultUpdateLogForStorageHostOperation(context.Background(), id, vaultLog)
		}},
		{"queue_freeze", WorkstreamSaveQueueFreezeStorageHostOperation, "queue_freeze", "freeze_id", freeze.FreezeID, func(s *SQLiteStore, id WorkstreamStorageHostOperationIdentity) (any, error) {
			return s.SaveQueueFreezeForStorageHostOperation(context.Background(), id, freeze)
		}},
		{"stage_run_receipt", WorkstreamSaveStageRunReceiptStorageHostOperation, "stage_run_receipt", "receipt_id", string(stageReceipt.ReceiptID), func(s *SQLiteStore, id WorkstreamStorageHostOperationIdentity) (any, error) {
			return s.SaveStageRunReceiptForStorageHostOperation(context.Background(), id, stageReceipt)
		}},
		{"closure_receipt", WorkstreamSaveClosureReceiptStorageHostOperation, "closure_receipt", "receipt_id", string(closureReceipt.ReceiptID), func(s *SQLiteStore, id WorkstreamStorageHostOperationIdentity) (any, error) {
			return s.SaveClosureReceiptForStorageHostOperation(context.Background(), id, closureReceipt)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "workstream.sqlite")
			store, err := NewSQLiteStore(path)
			if err != nil {
				t.Fatalf("open owner: %v", err)
			}
			identity := workstreamStorageHostIdentityForTest(tc.operation, "host-save-"+tc.name)
			result, err := tc.save(store, identity)
			if err != nil {
				t.Fatalf("save: %v", err)
			}
			if err := store.Close(); err != nil {
				t.Fatalf("close owner: %v", err)
			}
			store, err = NewSQLiteStore(path)
			if err != nil {
				t.Fatalf("reopen owner: %v", err)
			}
			defer store.Close()
			stored, found, err := store.LookupWorkstreamStorageHostOperationResult(context.Background(), identity, tc.table, tc.idColumn, tc.effectID)
			if err != nil || !found {
				t.Fatalf("receipt found=%v err=%v", found, err)
			}
			want, err := json.Marshal(result)
			if err != nil || !reflect.DeepEqual([]byte(stored), want) {
				t.Fatalf("stored result=%s want=%s err=%v", stored, want, err)
			}
		})
	}
}

func TestSaveWorkstreamRowStorageHostDoesNotCreateVaultFiles(t *testing.T) {
	base := t.TempDir()
	vaultRoot := filepath.Join(base, "native-vault-root")
	store, err := NewSQLiteStoreWithVault(filepath.Join(base, "workstream.sqlite"), vaultRoot)
	if err != nil {
		t.Fatalf("open owner: %v", err)
	}
	defer store.Close()
	item := domainworkstream.Workstream{WorkstreamID: "ws-no-native-path", Name: "remote row", Status: domainworkstream.StatusActive, CreatedAt: time.Date(2026, 10, 4, 14, 0, 0, 0, time.UTC)}
	identity := workstreamStorageHostIdentityForTest(WorkstreamSaveWorkstreamStorageHostOperation, "host-save-workstream-no-vault")
	if _, err := store.SaveWorkstreamRowForStorageHostOperation(context.Background(), identity, item); err != nil {
		t.Fatalf("persist row without native file setup: %v", err)
	}
	if _, err := os.Stat(vaultRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("storage-host row operation touched native VaultRoot: stat err=%v", err)
	}
}

func workstreamStorageHostIdentityForTest(operation, opID string) WorkstreamStorageHostOperationIdentity {
	payload := sha256.Sum256([]byte("typed " + operation + " request"))
	return WorkstreamStorageHostOperationIdentity{OpID: opID, Operation: operation, PayloadSHA256: hex.EncodeToString(payload[:]), WriterGeneration: 17}
}

func TestWorkstreamLeaseMutationsPersistExactStorageHostProofAcrossReopen(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 4, 15, 0, 0, 0, time.UTC)
	t.Run("acquire", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "workstream.sqlite")
		store, err := NewSQLiteStore(path)
		if err != nil {
			t.Fatalf("open owner: %v", err)
		}
		lease := domainworkstream.ImplementationLease{LeaseName: "atlas", HolderUnitID: "unit-a", HolderWorkstreamID: "ws-a", Stage: "BUILD", AcquiredAt: now, HeartbeatAt: now}
		identity := workstreamStorageHostIdentityForTest(WorkstreamAcquireLeaseStorageHostOperation, "host-lease-acquire")
		original, err := store.AcquireImplementationLeaseIfUnfrozenForStorageHostOperation(ctx, identity, lease)
		if err != nil || !original.Acquired || original.Reason != "" {
			t.Fatalf("acquire result=%+v err=%v", original, err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("close owner: %v", err)
		}
		store, err = NewSQLiteStore(path)
		if err != nil {
			t.Fatalf("reopen owner: %v", err)
		}
		defer store.Close()
		receipt, found, err := store.LookupWorkstreamStorageHostOperationProof(ctx, identity, lease.LeaseName)
		var recovered WorkstreamLeaseAcquireResult
		if err != nil || !found || json.Unmarshal(receipt, &recovered) != nil || recovered != original {
			t.Fatalf("receipt=%s found=%v recovered=%+v err=%v", receipt, found, recovered, err)
		}
		replayed, err := store.AcquireImplementationLeaseIfUnfrozenForStorageHostOperation(ctx, identity, lease)
		if err != nil || replayed != original {
			t.Fatalf("direct exact replay=%+v err=%v, want %+v", replayed, err, original)
		}
		var generation int64
		if err := store.db.QueryRow(`SELECT writer_generation FROM `+workstreamStorageHostReceiptTable+` WHERE op_id = ?`, identity.OpID).Scan(&generation); err != nil || generation != identity.WriterGeneration {
			t.Fatalf("generation=%d err=%v", generation, err)
		}
	})
	t.Run("resolve_freeze_and_lease", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "workstream.sqlite")
		store, err := NewSQLiteStore(path)
		if err != nil {
			t.Fatalf("open owner: %v", err)
		}
		freeze := domainworkstream.QueueFreeze{FreezeID: "freeze-lease", BlockedUnitID: "unit-old", BlockedRevision: 2, FreezeRevision: 4, Status: domainworkstream.QueueFreezeActive, CreatedAt: now}
		if err := store.SaveQueueFreeze(ctx, freeze); err != nil {
			t.Fatalf("preseed freeze: %v", err)
		}
		replacement := domainworkstream.ImplementationLease{LeaseName: "atlas", HolderUnitID: "unit-new", HolderWorkstreamID: "ws-new", AcquiredAt: now, HeartbeatAt: now}
		resolution := domainworkstream.QueueFreezeResolution{ExpectedFreezeRevision: 4, ActionID: modulecore.ActionID("act_00000000-0000-5000-8000-000000000007"), ReplacementUnitID: replacement.HolderUnitID, SupersedesUnitID: freeze.BlockedUnitID, BlockerResolutionRefs: []domainbacklog.EvidenceRef{{Kind: "fix", Ref: "fix-proof", Verified: true, VerificationResult: domainbacklog.EvidenceVerificationVerified}}, ResolutionPayloadHash: "resolution-hash"}
		identity := workstreamStorageHostIdentityForTest(WorkstreamResolveFreezeStorageHostOperation, "host-freeze-resolve")
		original, err := store.ResolveQueueFreezeAndAcquireLeaseForStorageHostOperation(ctx, identity, freeze.FreezeID, resolution, replacement)
		if err != nil || !original.Acquired || original.Freeze.Status != domainworkstream.QueueFreezeResolved || original.Lease.HolderUnitID != replacement.HolderUnitID {
			t.Fatalf("resolve result=%+v err=%v", original, err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("close owner: %v", err)
		}
		store, err = NewSQLiteStore(path)
		if err != nil {
			t.Fatalf("reopen owner: %v", err)
		}
		defer store.Close()
		receipt, found, err := store.LookupWorkstreamStorageHostOperationProof(ctx, identity, freeze.FreezeID)
		var recovered WorkstreamFreezeLeaseResult
		if err != nil || !found || json.Unmarshal(receipt, &recovered) != nil || !reflect.DeepEqual(recovered, original) {
			t.Fatalf("receipt=%s found=%v recovered=%+v err=%v", receipt, found, recovered, err)
		}
		replayed, err := store.ResolveQueueFreezeAndAcquireLeaseForStorageHostOperation(ctx, identity, freeze.FreezeID, resolution, replacement)
		if err != nil || !reflect.DeepEqual(replayed, original) {
			t.Fatalf("direct resolve replay=%+v err=%v, want original %+v", replayed, err, original)
		}
	})
	t.Run("heartbeat_and_release", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "workstream.sqlite")
		store, err := NewSQLiteStore(path)
		if err != nil {
			t.Fatalf("open owner: %v", err)
		}
		lease := domainworkstream.ImplementationLease{LeaseName: "atlas", HolderUnitID: "unit-a", HolderWorkstreamID: "ws-a", Stage: "BUILD", AcquiredAt: now, HeartbeatAt: now}
		if acquired, err := store.AcquireImplementationLease(ctx, lease); err != nil || !acquired {
			t.Fatalf("preseed lease acquired=%v err=%v", acquired, err)
		}
		heartbeat := lease
		heartbeat.Stage, heartbeat.Revision, heartbeat.HeartbeatAt = "VERIFY", "r2", now.Add(time.Minute)
		heartbeatID := workstreamStorageHostIdentityForTest(WorkstreamHeartbeatLeaseStorageHostOperation, "host-lease-heartbeat")
		if err := store.HeartbeatImplementationLeaseForStorageHostOperation(ctx, heartbeatID, heartbeat); err != nil {
			t.Fatalf("heartbeat: %v", err)
		}
		releaseID := workstreamStorageHostIdentityForTest(WorkstreamReleaseLeaseStorageHostOperation, "host-lease-release")
		if err := store.ReleaseImplementationLeaseForStorageHostOperation(ctx, releaseID, lease.LeaseName, lease.HolderUnitID); err != nil {
			t.Fatalf("release: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("close owner: %v", err)
		}
		store, err = NewSQLiteStore(path)
		if err != nil {
			t.Fatalf("reopen owner: %v", err)
		}
		defer store.Close()
		for _, identity := range []WorkstreamStorageHostOperationIdentity{heartbeatID, releaseID} {
			if _, found, err := store.LookupWorkstreamStorageHostOperationProof(ctx, identity, lease.LeaseName); err != nil || !found {
				t.Fatalf("operation %s proof found=%v err=%v", identity.Operation, found, err)
			}
		}
		if _, found, err := store.GetImplementationLease(ctx, lease.LeaseName); err != nil || found {
			t.Fatalf("lease after release found=%v err=%v", found, err)
		}
	})
}

func TestWorkstreamStorageHostReceiptMutationFailureRollsBackCanonicalRow(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "workstream.sqlite"))
	if err != nil {
		t.Fatalf("open owner: %v", err)
	}
	defer store.Close()
	if err := store.ensureWorkstreamStorageHostReceiptSchema(context.Background()); err != nil {
		t.Fatalf("create receipt schema: %v", err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER fail_workstream_storagehost_receipt BEFORE INSERT ON ` + workstreamStorageHostReceiptTable + ` BEGIN SELECT RAISE(ABORT, 'receipt blocked'); END`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}
	goal := workstreamStorageHostTestGoal()
	identity := workstreamStorageHostTestIdentity()
	if _, err := store.SaveGoalForStorageHostOperation(context.Background(), identity, goal); err == nil {
		t.Fatal("receipt insert failure unexpectedly succeeded")
	}
	if _, found, err := store.FindGoalByID(context.Background(), goal.GoalID); err != nil || found {
		t.Fatalf("goal row found=%v err=%v after owner receipt insert failure, want TX rollback", found, err)
	}
	now := time.Date(2026, 10, 4, 17, 0, 0, 0, time.UTC)
	lease := domainworkstream.ImplementationLease{LeaseName: "atlas", HolderUnitID: "unit-atomic", HolderWorkstreamID: "ws-atomic", AcquiredAt: now, HeartbeatAt: now}
	leaseIdentity := workstreamStorageHostIdentityForTest(WorkstreamAcquireLeaseStorageHostOperation, "host-atomic-lease")
	if _, err := store.AcquireImplementationLeaseIfUnfrozenForStorageHostOperation(context.Background(), leaseIdentity, lease); err == nil {
		t.Fatal("lease owner operation unexpectedly succeeded after receipt insert failure")
	}
	if _, found, err := store.GetImplementationLease(context.Background(), lease.LeaseName); err != nil || found {
		t.Fatalf("lease row found=%v err=%v after custom receipt insert failure, want TX rollback", found, err)
	}
}

func TestWorkstreamStorageHostReceiptBindsOperationPayloadAndGeneration(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "workstream.sqlite"))
	if err != nil {
		t.Fatalf("open owner: %v", err)
	}
	defer store.Close()
	goal, identity := workstreamStorageHostTestGoal(), workstreamStorageHostTestIdentity()
	if _, err := store.SaveGoalForStorageHostOperation(context.Background(), identity, goal); err != nil {
		t.Fatalf("save goal: %v", err)
	}
	for name, changed := range map[string]WorkstreamStorageHostOperationIdentity{
		"operation": func() WorkstreamStorageHostOperationIdentity {
			item := identity
			item.Operation = WorkstreamSaveArtifactStorageHostOperation
			return item
		}(),
		"payload": func() WorkstreamStorageHostOperationIdentity {
			item := identity
			item.PayloadSHA256 = fmt.Sprintf("%064x", 9)
			return item
		}(),
		"generation": func() WorkstreamStorageHostOperationIdentity { item := identity; item.WriterGeneration++; return item }(),
	} {
		t.Run(name, func(t *testing.T) {
			if _, found, err := store.LookupWorkstreamStorageHostOperationResult(context.Background(), changed, "workstream_goal", "goal_id", goal.GoalID); err == nil || found {
				t.Fatalf("mismatched identity found=%v err=%v, want unproven", found, err)
			}
		})
	}
}

func workstreamStorageHostTestIdentity() WorkstreamStorageHostOperationIdentity {
	payload := sha256.Sum256([]byte("typed goal operation"))
	return WorkstreamStorageHostOperationIdentity{
		OpID: "workstream-owner-receipt-test", Operation: WorkstreamSaveGoalStorageHostOperation,
		PayloadSHA256: hex.EncodeToString(payload[:]), WriterGeneration: 7,
	}
}

func TestSaveGoalStorageHostReceiptSurvivesReopenWithExactGenerationAndResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workstream.sqlite")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("open owner: %v", err)
	}
	goal, identity := workstreamStorageHostTestGoal(), workstreamStorageHostTestIdentity()
	got, err := store.SaveGoalForStorageHostOperation(context.Background(), identity, goal)
	if err != nil || !reflect.DeepEqual(got, goal) {
		t.Fatalf("owner result=%+v err=%v", got, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close owner: %v", err)
	}

	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("reopen owner: %v", err)
	}
	defer store.Close()
	resultJSON, found, err := store.LookupWorkstreamStorageHostOperationReceipt(context.Background(), identity, goal)
	if err != nil || !found {
		t.Fatalf("receipt found=%v err=%v", found, err)
	}
	var replay domainworkstream.Goal
	if err := json.Unmarshal(resultJSON, &replay); err != nil || !reflect.DeepEqual(replay, goal) {
		t.Fatalf("stored result=%+v err=%v", replay, err)
	}
	var generation int64
	if err := store.db.QueryRow(`SELECT writer_generation FROM `+workstreamStorageHostReceiptTable+` WHERE op_id = ?`, identity.OpID).Scan(&generation); err != nil || generation != identity.WriterGeneration {
		t.Fatalf("writer generation=%d err=%v, want %d", generation, err, identity.WriterGeneration)
	}
	if replayed, err := store.SaveGoalForStorageHostOperation(context.Background(), identity, goal); err != nil || !reflect.DeepEqual(replayed, goal) {
		t.Fatalf("owner exact replay=%+v err=%v", replayed, err)
	}
}

func TestWorkstreamStorageHostReceiptDoesNotCertifyMissingOrSubstitutedProof(t *testing.T) {
	for _, mode := range []string{"missing", "bad_checksum", "changed_result"} {
		t.Run(mode, func(t *testing.T) {
			store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "workstream.sqlite"))
			if err != nil {
				t.Fatalf("open owner: %v", err)
			}
			defer store.Close()
			goal, identity := workstreamStorageHostTestGoal(), workstreamStorageHostTestIdentity()
			if _, err := store.SaveGoalForStorageHostOperation(context.Background(), identity, goal); err != nil {
				t.Fatalf("save owner operation: %v", err)
			}
			switch mode {
			case "missing":
				_, err = store.db.Exec(`DELETE FROM `+workstreamStorageHostReceiptTable+` WHERE op_id = ?`, identity.OpID)
			case "bad_checksum":
				_, err = store.db.Exec(`UPDATE `+workstreamStorageHostReceiptTable+` SET result_sha256 = ? WHERE op_id = ?`, "0", identity.OpID)
			case "changed_result":
				_, err = store.db.Exec(`UPDATE `+workstreamStorageHostReceiptTable+` SET result_json = ? WHERE op_id = ?`, `{"goal_id":"substituted"}`, identity.OpID)
			}
			if err != nil {
				t.Fatalf("tamper owner receipt: %v", err)
			}
			if _, found, err := store.LookupWorkstreamStorageHostOperationReceipt(context.Background(), identity, goal); err == nil || found {
				t.Fatalf("tampered owner proof found=%v err=%v, want unresolved", found, err)
			}
		})
	}
}
