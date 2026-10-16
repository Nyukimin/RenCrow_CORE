package workstream

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const cancellationPendingReason = "cancellation closure is pending"

type closureGateState struct {
	target                  string
	phase                   string
	status                  string
	malformedCompletedField string
}

type closureGateCase struct {
	name            string
	attemptSameUnit bool
	queueFrozen     bool
	states          []closureGateState
	wantBlocked     bool
	wantReason      string
}

func closureGateCases() []closureGateCase {
	return []closureGateCase{
		{
			name: "prepared rejected closure for another unit blocks after reopen",
			states: []closureGateState{{
				target: domainbacklog.DeliveryRejected,
				phase:  domainworkstream.ClosurePhasePrepared,
				status: domainworkstream.ClosureStatusPrepared,
			}},
			wantBlocked: true,
		},
		{
			name:            "failed rejected closure for the same unit blocks after reopen",
			attemptSameUnit: true,
			states: []closureGateState{{
				target: domainbacklog.DeliveryRejected,
				phase:  domainworkstream.ClosurePhasePrepared,
				status: domainworkstream.ClosureStatusFailed,
			}},
			wantBlocked: true,
		},
		{
			name: "completed rejected closure before final phase blocks after reopen",
			states: []closureGateState{{
				target: domainbacklog.DeliveryRejected,
				phase:  domainworkstream.ClosurePhaseLease,
				status: domainworkstream.ClosureStatusCompleted,
			}},
			wantBlocked: true,
		},
		{
			name: "completed rejected closure without released lease blocks after reopen",
			states: []closureGateState{{
				target:                  domainbacklog.DeliveryRejected,
				phase:                   domainworkstream.ClosurePhaseDone,
				status:                  domainworkstream.ClosureStatusCompleted,
				malformedCompletedField: "lease_released",
			}},
			wantBlocked: true,
		},
		{
			name: "completed rejected closure without completion time blocks after reopen",
			states: []closureGateState{{
				target:                  domainbacklog.DeliveryRejected,
				phase:                   domainworkstream.ClosurePhaseDone,
				status:                  domainworkstream.ClosureStatusCompleted,
				malformedCompletedField: "completed_at",
			}},
			wantBlocked: true,
		},
		{
			name: "completed rejected closure with error blocks after reopen",
			states: []closureGateState{{
				target:                  domainbacklog.DeliveryRejected,
				phase:                   domainworkstream.ClosurePhaseDone,
				status:                  domainworkstream.ClosureStatusCompleted,
				malformedCompletedField: "error",
			}},
			wantBlocked: true,
		},
		{
			name: "completed rejected closure without action id blocks after reopen",
			states: []closureGateState{{
				target:                  domainbacklog.DeliveryRejected,
				phase:                   domainworkstream.ClosurePhaseDone,
				status:                  domainworkstream.ClosureStatusCompleted,
				malformedCompletedField: "action_id",
			}},
			wantBlocked: true,
		},
		{
			name: "final rejected closure allows acquisition",
			states: []closureGateState{{
				target: domainbacklog.DeliveryRejected,
				phase:  domainworkstream.ClosurePhaseDone,
				status: domainworkstream.ClosureStatusCompleted,
			}},
		},
		{
			name: "final receipt supersedes prepared receipt history",
			states: []closureGateState{
				{
					target: domainbacklog.DeliveryRejected,
					phase:  domainworkstream.ClosurePhasePrepared,
					status: domainworkstream.ClosureStatusPrepared,
				},
				{
					target: domainbacklog.DeliveryRejected,
					phase:  domainworkstream.ClosurePhaseDone,
					status: domainworkstream.ClosureStatusCompleted,
				},
			},
		},
		{
			name: "later failed receipt reopens a completed rejected closure",
			states: []closureGateState{
				{
					target: domainbacklog.DeliveryRejected,
					phase:  domainworkstream.ClosurePhaseDone,
					status: domainworkstream.ClosureStatusCompleted,
				},
				{
					target: domainbacklog.DeliveryRejected,
					phase:  domainworkstream.ClosurePhasePrepared,
					status: domainworkstream.ClosureStatusFailed,
				},
			},
			wantBlocked: true,
		},
		{
			name:        "queue freeze retains rejection priority over cancellation pending",
			queueFrozen: true,
			states: []closureGateState{{
				target: domainbacklog.DeliveryRejected,
				phase:  domainworkstream.ClosurePhasePrepared,
				status: domainworkstream.ClosureStatusPrepared,
			}},
			wantBlocked: true,
			wantReason:  domainworkstream.ErrQueueFrozen.Error(),
		},
		{
			name: "legacy empty target preserves done closure behavior",
			states: []closureGateState{{
				phase:  domainworkstream.ClosurePhasePrepared,
				status: domainworkstream.ClosureStatusPrepared,
			}},
		},
	}
}

func closureGateReceipt(unitID string, receiptID modulecore.ReceiptID, key string, now time.Time, state closureGateState) domainworkstream.ClosureReceipt {
	receipt := domainworkstream.ClosureReceipt{
		ReceiptID:              receiptID,
		IdempotencyKey:         key,
		UnitID:                 unitID,
		ImplementationRevision: 1,
		TargetDeliveryState:    state.target,
		Phase:                  state.phase,
		Status:                 state.status,
		CreatedAt:              now,
	}
	if state.target == domainbacklog.DeliveryRejected && state.status == domainworkstream.ClosureStatusCompleted {
		receipt.ActionID = modulecore.ActionID("act_00000000-0000-5000-8000-000000000007")
		receipt.LeaseReleased = true
		receipt.CompletedAt = now
		switch state.malformedCompletedField {
		case "lease_released":
			receipt.LeaseReleased = false
		case "completed_at":
			receipt.CompletedAt = time.Time{}
		case "error":
			receipt.Error = "closure incomplete"
		case "action_id":
			receipt.ActionID = ""
		}
	}
	return receipt
}

func writeRawClosureReceipt(t *testing.T, root string, receipt domainworkstream.ClosureReceipt) {
	t.Helper()
	line, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if len(line) <= maxJSONLRecordBytes {
		t.Fatalf("raw closure receipt size=%d, want above %d bytes", len(line), maxJSONLRecordBytes)
	}
	path := filepath.Join(root, "closure_receipt.jsonl")
	if err := os.WriteFile(path, append(line, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteStoreRejectsLeaseWhileRejectedClosureIsUnfinished(t *testing.T) {
	ctx := context.Background()
	for _, testCase := range closureGateCases() {
		t.Run(testCase.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "workstream.db")
			store, err := NewSQLiteStore(path)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
			cancelledUnit := "unit-cancelled"
			lease := domainworkstream.ImplementationLease{
				LeaseName: "atlas_implementation", HolderUnitID: cancelledUnit,
				HolderWorkstreamID: "ws-cancelled", Stage: "QUEUED", AcquiredAt: now, HeartbeatAt: now,
			}
			if acquired, reason, err := store.AcquireImplementationLeaseIfUnfrozen(ctx, lease); err != nil || !acquired || reason != "" {
				t.Fatalf("seed lease acquire=%v reason=%q err=%v", acquired, reason, err)
			}
			if err := store.ReleaseImplementationLease(ctx, lease.LeaseName, cancelledUnit); err != nil {
				t.Fatal(err)
			}
			if testCase.queueFrozen {
				if err := store.SaveQueueFreeze(ctx, domainworkstream.QueueFreeze{
					FreezeID: "freeze-active", BlockedUnitID: cancelledUnit, BlockedRevision: 1,
					FreezeRevision: 1, Status: domainworkstream.QueueFreezeActive, CreatedAt: now,
				}); err != nil {
					t.Fatalf("save active queue freeze: %v", err)
				}
			}

			receiptID := modulecore.NewReceiptID()
			for _, state := range testCase.states {
				receipt := closureGateReceipt(cancelledUnit, receiptID, "unit-cancelled:1:closure", now, state)
				if err := store.SaveClosureReceipt(ctx, receipt); err != nil {
					t.Fatalf("save closure receipt: %v", err)
				}
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}

			reopened, err := NewSQLiteStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			attempt := lease
			if !testCase.attemptSameUnit {
				attempt.HolderUnitID = "unit-next"
				attempt.HolderWorkstreamID = "ws-next"
			}
			acquired, reason, err := reopened.AcquireImplementationLeaseIfUnfrozen(ctx, attempt)
			if err != nil {
				t.Fatalf("acquire after reopen: %v", err)
			}
			if testCase.wantBlocked {
				wantReason := testCase.wantReason
				if wantReason == "" {
					wantReason = cancellationPendingReason
				}
				if acquired || reason != wantReason {
					t.Fatalf("unfinished rejected closure acquire=%v reason=%q, want reason %q", acquired, reason, wantReason)
				}
				return
			}
			if !acquired || reason != "" {
				t.Fatalf("completed/legacy closure acquire=%v reason=%q, want acquired", acquired, reason)
			}
		})
	}
}

func TestJSONLStoreRejectsLeaseWhileRejectedClosureIsUnfinished(t *testing.T) {
	ctx := context.Background()
	for _, testCase := range closureGateCases() {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			store := NewJSONLStore(root)
			now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
			cancelledUnit := "unit-cancelled"
			lease := domainworkstream.ImplementationLease{
				LeaseName: "atlas_implementation", HolderUnitID: cancelledUnit,
				HolderWorkstreamID: "ws-cancelled", Stage: "QUEUED", AcquiredAt: now, HeartbeatAt: now,
			}
			if acquired, reason, err := store.AcquireImplementationLeaseIfUnfrozen(ctx, lease); err != nil || !acquired || reason != "" {
				t.Fatalf("seed lease acquire=%v reason=%q err=%v", acquired, reason, err)
			}
			if err := store.ReleaseImplementationLease(ctx, lease.LeaseName, cancelledUnit); err != nil {
				t.Fatal(err)
			}
			if testCase.queueFrozen {
				if err := store.SaveQueueFreeze(ctx, domainworkstream.QueueFreeze{
					FreezeID: "freeze-active", BlockedUnitID: cancelledUnit, BlockedRevision: 1,
					FreezeRevision: 1, Status: domainworkstream.QueueFreezeActive, CreatedAt: now,
				}); err != nil {
					t.Fatalf("save active queue freeze: %v", err)
				}
			}

			receiptID := modulecore.NewReceiptID()
			for _, state := range testCase.states {
				receipt := closureGateReceipt(cancelledUnit, receiptID, "unit-cancelled:1:closure", now, state)
				if err := store.SaveClosureReceipt(ctx, receipt); err != nil {
					t.Fatalf("save closure receipt: %v", err)
				}
			}

			reopened := NewJSONLStore(root)
			attempt := lease
			if !testCase.attemptSameUnit {
				attempt.HolderUnitID = "unit-next"
				attempt.HolderWorkstreamID = "ws-next"
			}
			acquired, reason, err := reopened.AcquireImplementationLeaseIfUnfrozen(ctx, attempt)
			if err != nil {
				t.Fatalf("acquire after reopen: %v", err)
			}
			if testCase.wantBlocked {
				wantReason := testCase.wantReason
				if wantReason == "" {
					wantReason = cancellationPendingReason
				}
				if acquired || reason != wantReason {
					t.Fatalf("unfinished rejected closure acquire=%v reason=%q, want reason %q", acquired, reason, wantReason)
				}
				return
			}
			if !acquired || reason != "" {
				t.Fatalf("completed/legacy closure acquire=%v reason=%q, want acquired", acquired, reason)
			}
		})
	}
}

func TestJSONLStoreReadsAndGatesOversizedHistoricalClosureReceipt(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := NewJSONLStore(root)
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	receipt := closureGateReceipt("unit-cancelled", modulecore.NewReceiptID(), "unit-cancelled:1:closure", now, closureGateState{
		target: domainbacklog.DeliveryRejected,
		phase:  domainworkstream.ClosurePhasePrepared,
		status: domainworkstream.ClosureStatusPrepared,
	})
	receipt.Error = strings.Repeat("x", 70*1024)
	if err := store.SaveClosureReceipt(ctx, receipt); err != nil {
		t.Fatalf("save valid historical closure receipt larger than 64 KiB: %v", err)
	}

	reopened := NewJSONLStore(root)
	items, err := reopened.ListClosureReceipts(ctx, 10)
	if err != nil || len(items) != 1 || items[0] != receipt {
		t.Fatalf("read oversized historical receipt items=%d err=%v", len(items), err)
	}
	lease := domainworkstream.ImplementationLease{
		LeaseName: "atlas_implementation", HolderUnitID: "unit-next", HolderWorkstreamID: "ws-next",
		Stage: "QUEUED", AcquiredAt: now, HeartbeatAt: now,
	}
	acquired, reason, err := reopened.AcquireImplementationLeaseIfUnfrozen(ctx, lease)
	if err != nil || acquired || reason != cancellationPendingReason {
		t.Fatalf("gate with oversized historical closure acquire=%v reason=%q err=%v", acquired, reason, err)
	}
}

func TestJSONLClosureReadFailsClosedOnMalformedRecords(t *testing.T) {
	for _, testCase := range []struct {
		name string
		line string
	}{
		{name: "malformed JSON", line: `{"receipt_id":`},
		{name: "invalid JSON above previous reader limit", line: strings.Repeat("x", 2<<20)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "closure_receipt.jsonl")
			if err := os.WriteFile(path, []byte(testCase.line+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			store := NewJSONLStore(root)
			if _, err := store.ListClosureReceipts(context.Background(), 10); err == nil {
				t.Fatal("invalid closure record was silently accepted")
			}
		})
	}
}

func TestJSONLStoreAllowsLeaseAfterOversizedHistoricalCompletedClosure(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	receipt := closureGateReceipt("unit-cancelled", modulecore.NewReceiptID(), "unit-cancelled:1:closure", now, closureGateState{
		target: domainbacklog.DeliveryRejected,
		phase:  domainworkstream.ClosurePhaseDone,
		status: domainworkstream.ClosureStatusCompleted,
	})
	receipt.GoalID = strings.Repeat("x", maxJSONLRecordBytes+1024)
	writeRawClosureReceipt(t, root, receipt)

	lease := domainworkstream.ImplementationLease{
		LeaseName: "atlas_implementation", HolderUnitID: "unit-next", HolderWorkstreamID: "ws-next",
		Stage: "QUEUED", AcquiredAt: now, HeartbeatAt: now,
	}
	acquired, reason, err := NewJSONLStore(root).AcquireImplementationLeaseIfUnfrozen(ctx, lease)
	if err != nil || !acquired || reason != "" {
		t.Fatalf("acquire after oversized completed historical closure=%v reason=%q err=%v, want acquired", acquired, reason, err)
	}
}

func TestJSONLStoreBlocksLeaseWithReasonAfterOversizedHistoricalPendingClosure(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	receipt := closureGateReceipt("unit-cancelled", modulecore.NewReceiptID(), "unit-cancelled:1:closure", now, closureGateState{
		target: domainbacklog.DeliveryRejected,
		phase:  domainworkstream.ClosurePhasePrepared,
		status: domainworkstream.ClosureStatusPrepared,
	})
	receipt.Error = strings.Repeat("x", maxJSONLRecordBytes+1024)
	writeRawClosureReceipt(t, root, receipt)

	lease := domainworkstream.ImplementationLease{
		LeaseName: "atlas_implementation", HolderUnitID: "unit-next", HolderWorkstreamID: "ws-next",
		Stage: "QUEUED", AcquiredAt: now, HeartbeatAt: now,
	}
	acquired, reason, err := NewJSONLStore(root).AcquireImplementationLeaseIfUnfrozen(ctx, lease)
	if err != nil || acquired || reason != cancellationPendingReason {
		t.Fatalf("acquire after oversized pending historical closure=%v reason=%q err=%v, want cancellation closure pending", acquired, reason, err)
	}
}

func TestJSONLWriterRejectsClosureReceiptAboveWriteLimit(t *testing.T) {
	ctx := context.Background()
	store := NewJSONLStore(t.TempDir())
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	receipt := closureGateReceipt("unit-cancelled", modulecore.NewReceiptID(), "unit-cancelled:1:closure", now, closureGateState{
		target: domainbacklog.DeliveryRejected,
		phase:  domainworkstream.ClosurePhasePrepared,
		status: domainworkstream.ClosureStatusPrepared,
	})
	receipt.Error = strings.Repeat("x", 2<<20)
	if err := store.SaveClosureReceipt(ctx, receipt); err == nil {
		t.Fatal("writer accepted a closure receipt larger than the write limit")
	}
}

func TestGenericJSONLWriterDoesNotUseClosureReceiptLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "other.jsonl")
	value := struct {
		Payload string `json:"payload"`
	}{Payload: strings.Repeat("x", maxJSONLRecordBytes+1)}
	if err := appendJSONL(path, value); err != nil {
		t.Fatalf("generic JSONL writer rejected a record above the closure-specific limit: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() <= maxJSONLRecordBytes {
		t.Fatalf("generic JSONL record size=%d, want above closure-specific limit %d", info.Size(), maxJSONLRecordBytes)
	}
}

func TestStorageHostCancellationPendingProofReasonIsDistinctFromQueueFreeze(t *testing.T) {
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	lease := domainworkstream.ImplementationLease{
		LeaseName: "atlas_implementation", HolderUnitID: "unit-next", HolderWorkstreamID: "ws-next",
		Stage: "QUEUED", AcquiredAt: now, HeartbeatAt: now,
	}
	receipt := closureGateReceipt("unit-cancelled", modulecore.NewReceiptID(), "unit-cancelled:1:closure", now, closureGateState{
		target: domainbacklog.DeliveryRejected,
		phase:  domainworkstream.ClosurePhasePrepared,
		status: domainworkstream.ClosureStatusPrepared,
	})
	resultJSON, err := json.Marshal(WorkstreamLeaseAcquireResult{Reason: cancellationPendingReason})
	if err != nil {
		t.Fatal(err)
	}
	effectJSON, err := json.Marshal(WorkstreamLeaseMutationEffect{
		RequestedLease: lease, ClosurePending: true, ClosureReceipt: &receipt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateWorkstreamCustomReceiptProof(WorkstreamAcquireLeaseStorageHostOperation, string(resultJSON), string(effectJSON), lease.LeaseName); err != nil {
		t.Fatalf("closure-pending result with exact closure evidence was rejected: %v", err)
	}
	effect := WorkstreamLeaseMutationEffect{RequestedLease: lease, ClosurePending: true, ClosureReceipt: &receipt, QueueFrozen: true}
	effectJSON, err = json.Marshal(effect)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateWorkstreamCustomReceiptProof(WorkstreamAcquireLeaseStorageHostOperation, string(resultJSON), string(effectJSON), lease.LeaseName); err == nil {
		t.Fatal("closure-pending proof was accepted with QueueFrozen set")
	}
	frozenResultJSON, err := json.Marshal(WorkstreamLeaseAcquireResult{Reason: domainworkstream.ErrQueueFrozen.Error()})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateWorkstreamCustomReceiptProof(WorkstreamAcquireLeaseStorageHostOperation, string(frozenResultJSON), string(effectJSON), lease.LeaseName); err != nil {
		t.Fatalf("queue-frozen result with exact closure evidence was rejected: %v", err)
	}
	receipt.TargetDeliveryState = domainbacklog.DeliveryDone
	effect.QueueFrozen = false
	effect.ClosureReceipt = &receipt
	effectJSON, err = json.Marshal(effect)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateWorkstreamCustomReceiptProof(WorkstreamAcquireLeaseStorageHostOperation, string(resultJSON), string(effectJSON), lease.LeaseName); err == nil {
		t.Fatal("closure-pending proof was accepted with a completed DONE target")
	}
}

func TestStorageHostLeasePathsBlockIncompleteRejectedClosures(t *testing.T) {
	ctx := context.Background()
	for _, testCase := range closureGateCases() {
		if len(testCase.states) != 1 || testCase.states[0].malformedCompletedField == "" {
			continue
		}
		t.Run(testCase.name, func(t *testing.T) {
			now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
			receipt := closureGateReceipt("unit-cancelled", modulecore.NewReceiptID(), "unit-cancelled:1:closure", now, testCase.states[0])

			t.Run("acquire", func(t *testing.T) {
				store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "workstream.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				if err := store.SaveClosureReceipt(ctx, receipt); err != nil {
					t.Fatalf("save closure receipt: %v", err)
				}
				lease := domainworkstream.ImplementationLease{
					LeaseName: "atlas_implementation", HolderUnitID: "unit-next", HolderWorkstreamID: "ws-next",
					Stage: "QUEUED", AcquiredAt: now, HeartbeatAt: now,
				}
				identity := workstreamStorageHostIdentityForTest(WorkstreamAcquireLeaseStorageHostOperation, "host-incomplete-closure-acquire")
				result, err := store.AcquireImplementationLeaseIfUnfrozenForStorageHostOperation(ctx, identity, lease)
				if err != nil || result.Acquired || result.Reason != cancellationPendingReason {
					t.Fatalf("storage-host acquire result=%+v err=%v, want cancellation pending", result, err)
				}
				if stored, found, err := store.GetImplementationLease(ctx, lease.LeaseName); err != nil || found {
					t.Fatalf("lease after blocked storage-host acquire=%+v found=%v err=%v", stored, found, err)
				}
			})

			t.Run("resolve", func(t *testing.T) {
				store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "workstream.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				freeze := domainworkstream.QueueFreeze{
					FreezeID: "freeze-active", BlockedUnitID: "unit-blocked", BlockedRevision: 2,
					FreezeRevision: 4, Status: domainworkstream.QueueFreezeActive, CreatedAt: now,
				}
				if err := store.SaveQueueFreeze(ctx, freeze); err != nil {
					t.Fatalf("save queue freeze: %v", err)
				}
				if err := store.SaveClosureReceipt(ctx, receipt); err != nil {
					t.Fatalf("save closure receipt: %v", err)
				}
				replacement := domainworkstream.ImplementationLease{
					LeaseName: "atlas_implementation", HolderUnitID: "unit-replacement", HolderWorkstreamID: "ws-replacement",
					Stage: "QUEUED", AcquiredAt: now, HeartbeatAt: now,
				}
				resolution := domainworkstream.QueueFreezeResolution{
					ExpectedFreezeRevision: 4,
					ActionID:               modulecore.ActionID("act_00000000-0000-5000-8000-000000000007"),
					ReplacementUnitID:      replacement.HolderUnitID,
					SupersedesUnitID:       freeze.BlockedUnitID,
					BlockerResolutionRefs: []domainbacklog.EvidenceRef{{
						Kind: "fix", Ref: "fix-proof", Verified: true,
						VerificationResult: domainbacklog.EvidenceVerificationVerified,
					}},
					ResolutionPayloadHash: "resolution-hash",
				}
				identity := workstreamStorageHostIdentityForTest(WorkstreamResolveFreezeStorageHostOperation, "host-incomplete-closure-resolve")
				result, err := store.ResolveQueueFreezeAndAcquireLeaseForStorageHostOperation(ctx, identity, freeze.FreezeID, resolution, replacement)
				if err != nil || result.Acquired || result.Reason != cancellationPendingReason {
					t.Fatalf("storage-host resolve result=%+v err=%v, want cancellation pending", result, err)
				}
				storedFreeze, found, err := store.GetQueueFreeze(ctx, freeze.FreezeID)
				if err != nil || !found || storedFreeze.Status != domainworkstream.QueueFreezeActive || storedFreeze.ResolutionAcquired {
					t.Fatalf("freeze after blocked storage-host resolve=%+v found=%v err=%v", storedFreeze, found, err)
				}
				if stored, found, err := store.GetImplementationLease(ctx, replacement.LeaseName); err != nil || found {
					t.Fatalf("replacement lease after blocked storage-host resolve=%+v found=%v err=%v", stored, found, err)
				}
			})
		})
	}
}

func TestClosureLeaseGateFailsClosedForUnknownTargetState(t *testing.T) {
	receipt := closureGateReceipt("unit-unknown", modulecore.NewReceiptID(), "unit-unknown:1:closure", time.Now().UTC(), closureGateState{
		target: "UNRECOGNIZED",
		phase:  domainworkstream.ClosurePhasePrepared,
		status: domainworkstream.ClosureStatusPrepared,
	})
	if reason, err := closureReceiptLeaseBlockReason(receipt); err == nil || reason != "" {
		t.Fatalf("unknown target state reason=%q err=%v, want fail-closed validation error", reason, err)
	}
	for _, backend := range []string{"sqlite", "jsonl"} {
		t.Run(backend, func(t *testing.T) {
			var acquire func() (bool, string, error)
			lease := domainworkstream.ImplementationLease{
				LeaseName: "atlas_implementation", HolderUnitID: "unit-next", HolderWorkstreamID: "ws-next",
				Stage: "QUEUED", AcquiredAt: receipt.CreatedAt, HeartbeatAt: receipt.CreatedAt,
			}
			if backend == "sqlite" {
				store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "workstream.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				if err := store.SaveClosureReceipt(context.Background(), receipt); err != nil {
					t.Fatal(err)
				}
				acquire = func() (bool, string, error) {
					return store.AcquireImplementationLeaseIfUnfrozen(context.Background(), lease)
				}
			} else {
				store := NewJSONLStore(t.TempDir())
				if err := store.SaveClosureReceipt(context.Background(), receipt); err != nil {
					t.Fatal(err)
				}
				acquire = func() (bool, string, error) {
					return store.AcquireImplementationLeaseIfUnfrozen(context.Background(), lease)
				}
			}
			if acquired, reason, err := acquire(); acquired || reason != "" || err == nil {
				t.Fatalf("unknown target acquire=%v reason=%q err=%v, want fail-closed error", acquired, reason, err)
			}
		})
	}
}

func TestClosureLeaseGateFailsClosedForInvalidRejectedActionID(t *testing.T) {
	receipt := closureGateReceipt("unit-rejected", modulecore.NewReceiptID(), "unit-rejected:1:closure", time.Now().UTC(), closureGateState{
		target: domainbacklog.DeliveryRejected,
		phase:  domainworkstream.ClosurePhaseDone,
		status: domainworkstream.ClosureStatusCompleted,
	})
	receipt.ActionID = "invalid"
	if reason, err := closureReceiptLeaseBlockReason(receipt); err == nil || reason != "" {
		t.Fatalf("invalid rejected action id reason=%q err=%v, want fail-closed validation error", reason, err)
	}
}

func TestSQLiteClosureGatePropagatesMalformedOlderReceiptAfterPendingCandidate(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "workstream.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.db.Exec(`INSERT INTO closure_receipt (receipt_id, idempotency_key, unit_id, implementation_revision, created_at, payload) VALUES (?, ?, ?, ?, ?, ?)`,
		"receipt-corrupt", "unit-corrupt:1:closure", "unit-corrupt", 1, time.Now().UTC().Format(timeFormatRFC3339Nano), "{malformed"); err != nil {
		t.Fatalf("seed malformed older closure receipt: %v", err)
	}
	now := time.Now().UTC()
	if err := store.SaveClosureReceipt(ctx, closureGateReceipt("unit-cancelled", modulecore.NewReceiptID(), "unit-cancelled:1:closure", now, closureGateState{
		target: domainbacklog.DeliveryRejected,
		phase:  domainworkstream.ClosurePhasePrepared,
		status: domainworkstream.ClosureStatusPrepared,
	})); err != nil {
		t.Fatalf("seed newest pending closure receipt: %v", err)
	}
	lease := domainworkstream.ImplementationLease{
		LeaseName: "atlas_implementation", HolderUnitID: "unit-next", HolderWorkstreamID: "ws-next",
		Stage: "QUEUED", AcquiredAt: now, HeartbeatAt: now,
	}
	if acquired, reason, err := store.AcquireImplementationLeaseIfUnfrozen(ctx, lease); acquired || reason != "" || err == nil {
		t.Fatalf("acquire=%v reason=%q err=%v, want malformed older row error even after pending candidate", acquired, reason, err)
	}
}

func TestResolveQueueFreezeAndAcquireRejectsPendingCancellationClosure(t *testing.T) {
	ctx := context.Background()
	for _, backend := range []string{"sqlite", "jsonl"} {
		t.Run(backend, func(t *testing.T) {
			root := t.TempDir()
			now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
			freeze := domainworkstream.QueueFreeze{
				FreezeID: "freeze-active", BlockedUnitID: "unit-blocked", BlockedRevision: 2,
				FreezeRevision: 4, Status: domainworkstream.QueueFreezeActive, CreatedAt: now,
			}
			replacement := domainworkstream.ImplementationLease{
				LeaseName: "atlas_implementation", HolderUnitID: "unit-replacement", HolderWorkstreamID: "ws-replacement",
				Stage: "QUEUED", AcquiredAt: now, HeartbeatAt: now,
			}
			resolution := domainworkstream.QueueFreezeResolution{
				ExpectedFreezeRevision: 4,
				ActionID:               modulecore.ActionID("act_00000000-0000-5000-8000-000000000007"),
				ReplacementUnitID:      replacement.HolderUnitID,
				SupersedesUnitID:       freeze.BlockedUnitID,
				BlockerResolutionRefs: []domainbacklog.EvidenceRef{{
					Kind: "fix", Ref: "fix-proof", Verified: true,
					VerificationResult: domainbacklog.EvidenceVerificationVerified,
				}},
				ResolutionPayloadHash: "resolution-hash",
			}
			closure := closureGateReceipt("unit-cancelled", modulecore.NewReceiptID(), "unit-cancelled:1:closure", now, closureGateState{
				target: domainbacklog.DeliveryRejected,
				phase:  domainworkstream.ClosurePhasePrepared,
				status: domainworkstream.ClosureStatusPrepared,
			})

			var resolve func() (bool, error)
			var getFreeze func() (domainworkstream.QueueFreeze, bool, error)
			var getLease func() (domainworkstream.ImplementationLease, bool, error)
			if backend == "sqlite" {
				store, err := NewSQLiteStore(filepath.Join(root, "workstream.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				if err := store.SaveQueueFreeze(ctx, freeze); err != nil {
					t.Fatal(err)
				}
				if err := store.SaveClosureReceipt(ctx, closure); err != nil {
					t.Fatal(err)
				}
				resolve = func() (bool, error) {
					_, _, acquired, err := store.ResolveQueueFreezeAndAcquireLease(ctx, freeze.FreezeID, resolution, replacement)
					return acquired, err
				}
				getFreeze = func() (domainworkstream.QueueFreeze, bool, error) { return store.GetQueueFreeze(ctx, freeze.FreezeID) }
				getLease = func() (domainworkstream.ImplementationLease, bool, error) {
					return store.GetImplementationLease(ctx, replacement.LeaseName)
				}
			} else {
				store := NewJSONLStore(root)
				if err := store.SaveQueueFreeze(ctx, freeze); err != nil {
					t.Fatal(err)
				}
				if err := store.SaveClosureReceipt(ctx, closure); err != nil {
					t.Fatal(err)
				}
				resolve = func() (bool, error) {
					_, _, acquired, err := store.ResolveQueueFreezeAndAcquireLease(ctx, freeze.FreezeID, resolution, replacement)
					return acquired, err
				}
				getFreeze = func() (domainworkstream.QueueFreeze, bool, error) { return store.GetQueueFreeze(ctx, freeze.FreezeID) }
				getLease = func() (domainworkstream.ImplementationLease, bool, error) {
					return store.GetImplementationLease(ctx, replacement.LeaseName)
				}
			}

			acquired, err := resolve()
			if acquired || err != domainworkstream.ErrCancellationPending {
				t.Fatalf("resolve acquire=%v err=%v, want cancellation-pending rejection", acquired, err)
			}
			storedFreeze, found, err := getFreeze()
			if err != nil || !found || storedFreeze.Status != domainworkstream.QueueFreezeActive || storedFreeze.ResolutionAcquired {
				t.Fatalf("freeze after rejected resolution=%+v found=%v err=%v", storedFreeze, found, err)
			}
			if lease, found, err := getLease(); err != nil || found {
				t.Fatalf("replacement lease after rejected resolution=%+v found=%v err=%v", lease, found, err)
			}
		})
	}
}
