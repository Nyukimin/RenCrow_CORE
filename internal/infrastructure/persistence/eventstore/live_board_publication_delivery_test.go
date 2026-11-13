package eventstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestBoardPublicationLeaseAttemptReopenAndCompletion(t *testing.T) {
	fixture := newBoardPublicationDeliveryFixture(t)
	ctx := context.Background()
	claimInput := BoardPublicationClaimInput{Scope: fixture.scope, Now: fixture.now, LeaseDuration: time.Minute}
	lease, available, err := fixture.store.ClaimBoardPublication(ctx, claimInput)
	if err != nil || !available {
		t.Fatalf("ClaimBoardPublication(exact owner, instance, principal) = available %t, err %v; want one scoped lease", available, err)
	}
	if lease.Scope != fixture.scope || lease.WorkID != externalBoardPublicationWorkID(fixture.lookup.OperationID) ||
		lease.LogicalKey != fixture.bundle.PublicationIntent.StableLogicalKey || lease.Version < 1 || lease.LeaseToken == "" ||
		len(lease.Attempts) != 0 {
		t.Fatalf("initial BOARD publication lease identity/history = %#v", lease)
	}
	if lease.CanonicalResponse.MessageID != fixture.bundle.PublicationIntent.CanonicalResponseMessageID ||
		payloadString(lease.CanonicalResponse.Payload, "text") != "Synthetic canonical response." ||
		lease.ResponseIndex.MessageID != lease.CanonicalResponse.MessageID || lease.PublicationIntent != fixture.bundle.PublicationIntent {
		t.Fatalf("lease did not carry the canonical B1 response and private target: %#v", lease)
	}

	prepared := preparedBoardPublicationBytes(lease, "claim-b2-first", "synthetic-private-claim-receipt-1")
	preparedHash := boardPublicationPayloadHash(prepared)
	attempt, err := fixture.store.RecordBoardPublicationAttempt(ctx, BoardPublicationAttemptInput{
		Lease: lease, Now: fixture.now, LogicalKey: lease.LogicalKey, PreparedBytes: prepared, PayloadSHA256: preparedHash,
		ProcessingClaimLeaseUntil: fixture.now.Add(30 * time.Second),
	})
	if err != nil {
		t.Fatalf("RecordBoardPublicationAttempt(first exact send): %v", err)
	}
	if attempt.WorkID != lease.WorkID || attempt.Ordinal != 1 || attempt.LogicalKey != lease.LogicalKey ||
		attempt.PayloadSHA256 != preparedHash || !bytes.Equal(attempt.PreparedBytes, prepared) {
		t.Fatalf("stored first BOARD attempt differs from exact prepared bytes: %#v", attempt)
	}
	if strings.Contains(strings.ToLower(string(attempt.PreparedBytes)), "authorization") || strings.Contains(strings.ToLower(string(attempt.PreparedBytes)), "bearer ") {
		t.Fatal("prepared BOARD attempt persisted Authorization or bearer material")
	}

	fixture.reopen(t)
	reclaimAt := lease.LeaseExpiresAt.Add(time.Second)
	reclaimed, available, err := fixture.store.ClaimBoardPublication(ctx, BoardPublicationClaimInput{
		Scope: fixture.scope, Now: reclaimAt, LeaseDuration: time.Minute,
	})
	if err != nil || !available {
		t.Fatalf("ClaimBoardPublication(reclaim unresolved attempt) = available %t, err %v", available, err)
	}
	if reclaimed.LeaseToken == lease.LeaseToken || reclaimed.Version <= lease.Version || len(reclaimed.Attempts) != 1 ||
		!reflect.DeepEqual(reclaimed.Attempts[0], attempt) {
		t.Fatalf("reclaimed lease did not preserve the exact unresolved attempt and advance its fence: old=%#v new=%#v", lease, reclaimed)
	}
	replayed, err := fixture.store.RecordBoardPublicationAttempt(ctx, BoardPublicationAttemptInput{
		Lease: reclaimed, Now: reclaimAt, LogicalKey: attempt.LogicalKey,
		PreparedBytes: append([]byte(nil), attempt.PreparedBytes...), PayloadSHA256: attempt.PayloadSHA256,
		ProcessingClaimLeaseUntil: attempt.ProcessingClaimLeaseUntil,
	})
	if err != nil || !reflect.DeepEqual(replayed, attempt) {
		t.Fatalf("RecordBoardPublicationAttempt(exact unresolved replay) = %#v, err %v; want same immutable attempt", replayed, err)
	}

	message := boardPublicationCanonicalMessage(reclaimed)
	if reclaimed.CanonicalResponse.CausationEventID != fixture.accepted.InputEvent.EventID ||
		string(reclaimed.CanonicalResponse.CausationEventID) == message.CausationID ||
		message.CausationID != string(fixture.bundle.PublicationIntent.Target.ReplyTo) {
		t.Fatalf("CORE and BOARD causes must remain distinct while BOARD reply inherits its root message: core=%q board=%q root=%q",
			reclaimed.CanonicalResponse.CausationEventID, message.CausationID, fixture.bundle.PublicationIntent.Target.ReplyTo)
	}
	sendReceipt := BoardPublicationSendReceipt{
		MessageID: message.MessageID, ThreadID: message.ThreadID, StoredAt: reclaimAt, Status: "stored",
	}
	outcome, err := fixture.store.CompleteBoardPublication(ctx, BoardPublicationCompletionInput{
		Lease: reclaimed, Now: reclaimAt, AttemptOrdinal: attempt.Ordinal, AttemptSHA256: attempt.PayloadSHA256,
		SendReceipt: sendReceipt, CanonicalMessage: message,
	})
	if err != nil {
		t.Fatalf("CompleteBoardPublication(exact current lease and canonical BOARD tuple): %v", err)
	}
	if outcome.Scope != fixture.scope || outcome.WorkID != lease.WorkID || outcome.LogicalKey != lease.LogicalKey ||
		outcome.AttemptOrdinal != attempt.Ordinal || outcome.PayloadSHA256 != attempt.PayloadSHA256 ||
		outcome.SendReceipt != sendReceipt || !reflect.DeepEqual(outcome.CanonicalMessage, message) ||
		outcome.ExpectedPeerPrincipalID != fixture.bundle.PublicationIntent.Target.PeerPrincipalID {
		t.Fatalf("completed BOARD publication outcome = %#v", outcome)
	}
	var workState string
	if err := fixture.store.db.QueryRowContext(ctx, `SELECT state FROM live_work_outbox WHERE work_id=?`, lease.WorkID).Scan(&workState); err != nil {
		t.Fatalf("read completed BOARD publication work state: %v", err)
	}
	if workState != "completed" {
		t.Fatalf("BOARD publication work state = %q, want completed in the outcome transaction", workState)
	}
	loaded, found, err := fixture.store.LoadBoardPublicationByParentMessage(ctx, BoardPublicationLookupInput{
		Scope: fixture.scope, ParentMessageID: message.MessageID,
	})
	if err != nil || !found || !reflect.DeepEqual(loaded, outcome) {
		t.Fatalf("LoadBoardPublicationByParentMessage(completed BOARD parent) = %#v, found %t, err %v; want exact terminal-correlation tuple", loaded, found, err)
	}
	wrongScope := fixture.scope
	wrongScope.PrincipalID = "principal-other"
	if _, found, err := fixture.store.LoadBoardPublicationByParentMessage(ctx, BoardPublicationLookupInput{
		Scope: wrongScope, ParentMessageID: message.MessageID,
	}); found || !errors.Is(err, ErrBoardPublicationScopeMismatch) {
		t.Fatalf("LoadBoardPublicationByParentMessage(wrong principal) = found %t, err %v; want scope error without receipt", found, err)
	}
	if _, available, err := fixture.store.ClaimBoardPublication(ctx, claimInput); err != nil || available {
		t.Fatalf("ClaimBoardPublication(no remaining work) = available %t, err %v; want no work without error", available, err)
	}
}

func TestBoardPublicationRejectsWrongScopeAndExpiredLease(t *testing.T) {
	fixture := newBoardPublicationDeliveryFixture(t)
	ctx := context.Background()
	for _, test := range []struct {
		name   string
		mutate func(*BoardPublicationScope)
	}{
		{name: "owner", mutate: func(scope *BoardPublicationScope) { scope.OwnerID = "owner-other" }},
		{name: "journal instance", mutate: func(scope *BoardPublicationScope) {
			scope.JournalInstanceID = "journal_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		}},
		{name: "principal", mutate: func(scope *BoardPublicationScope) { scope.PrincipalID = "principal-other" }},
	} {
		t.Run("wrong scope/"+test.name, func(t *testing.T) {
			wrong := fixture.scope
			test.mutate(&wrong)
			if _, available, err := fixture.store.ClaimBoardPublication(ctx, BoardPublicationClaimInput{
				Scope: wrong, Now: fixture.now, LeaseDuration: time.Minute,
			}); available || !errors.Is(err, ErrBoardPublicationScopeMismatch) {
				t.Fatalf("ClaimBoardPublication(wrong %s) = available %t, err %v; want scope error without a lease", test.name, available, err)
			}
		})
	}
	var workState string
	if err := fixture.store.db.QueryRowContext(ctx, `SELECT state FROM live_work_outbox WHERE work_id=?`, externalBoardPublicationWorkID(fixture.lookup.OperationID)).Scan(&workState); err != nil {
		t.Fatalf("read work state after wrong-scope claims: %v", err)
	}
	if workState != "pending" {
		t.Fatalf("work state after wrong-scope claims = %q, want pending", workState)
	}

	lease, available, err := fixture.store.ClaimBoardPublication(ctx, BoardPublicationClaimInput{
		Scope: fixture.scope, Now: fixture.now, LeaseDuration: time.Minute,
	})
	if err != nil || !available {
		t.Fatalf("ClaimBoardPublication(valid scope) = available %t, err %v", available, err)
	}
	prepared := preparedBoardPublicationBytes(lease, "claim-expiring", "synthetic-private-claim-receipt-expiring")
	attempt, err := fixture.store.RecordBoardPublicationAttempt(ctx, BoardPublicationAttemptInput{
		Lease: lease, Now: fixture.now, LogicalKey: lease.LogicalKey, PreparedBytes: prepared,
		PayloadSHA256: boardPublicationPayloadHash(prepared), ProcessingClaimLeaseUntil: fixture.now.Add(30 * time.Second),
	})
	if err != nil {
		t.Fatalf("RecordBoardPublicationAttempt(expiring lease): %v", err)
	}
	expiredAt := lease.LeaseExpiresAt.Add(time.Second)
	if _, err := fixture.store.RecordBoardPublicationAttempt(ctx, BoardPublicationAttemptInput{
		Lease: lease, Now: expiredAt, LogicalKey: attempt.LogicalKey,
		PreparedBytes: attempt.PreparedBytes, PayloadSHA256: attempt.PayloadSHA256,
		ProcessingClaimLeaseUntil: attempt.ProcessingClaimLeaseUntil,
	}); !errors.Is(err, ErrWorkConflict) {
		t.Fatalf("RecordBoardPublicationAttempt(expired lease) error = %v, want ErrWorkConflict", err)
	}
	message := boardPublicationCanonicalMessage(lease)
	if _, err := fixture.store.CompleteBoardPublication(ctx, BoardPublicationCompletionInput{
		Lease: lease, Now: expiredAt, AttemptOrdinal: attempt.Ordinal, AttemptSHA256: attempt.PayloadSHA256,
		SendReceipt:      BoardPublicationSendReceipt{MessageID: message.MessageID, ThreadID: message.ThreadID, StoredAt: expiredAt, Status: "stored"},
		CanonicalMessage: message,
	}); !errors.Is(err, ErrWorkConflict) {
		t.Fatalf("CompleteBoardPublication(expired lease) error = %v, want ErrWorkConflict", err)
	}
}

func TestBoardPublicationTypedRetryPreservesUnknownExactAttempt(t *testing.T) {
	fixture := newBoardPublicationDeliveryFixture(t)
	ctx := context.Background()
	lease, available, err := fixture.store.ClaimBoardPublication(ctx, BoardPublicationClaimInput{
		Scope: fixture.scope, Now: fixture.now, LeaseDuration: time.Minute,
	})
	if err != nil || !available {
		t.Fatalf("ClaimBoardPublication(valid scope) = available %t, err %v", available, err)
	}
	prepared := preparedBoardPublicationBytes(lease, "claim-retry", "synthetic-private-claim-receipt-retry")
	attempt, err := fixture.store.RecordBoardPublicationAttempt(ctx, BoardPublicationAttemptInput{
		Lease: lease, Now: fixture.now, LogicalKey: lease.LogicalKey, PreparedBytes: prepared,
		PayloadSHA256: boardPublicationPayloadHash(prepared), ProcessingClaimLeaseUntil: fixture.now.Add(30 * time.Second),
	})
	if err != nil {
		t.Fatalf("RecordBoardPublicationAttempt(unknown outcome): %v", err)
	}
	if err := fixture.store.RetryBoardPublication(ctx, BoardPublicationRetryInput{Lease: lease, Now: fixture.now}); err != nil {
		t.Fatalf("RetryBoardPublication(schedule existing lease): %v", err)
	}
	var state, notBefore string
	if err := fixture.store.db.QueryRowContext(ctx, `SELECT state,not_before FROM live_work_outbox WHERE work_id=?`, lease.WorkID).Scan(&state, &notBefore); err != nil {
		t.Fatalf("read specialized retry state: %v", err)
	}
	if state != "pending" || notBefore == "" {
		t.Fatalf("typed retry state/deadline = %q/%q, want pending with backoff", state, notBefore)
	}
	if _, available, err := fixture.store.ClaimBoardPublication(ctx, BoardPublicationClaimInput{
		Scope: fixture.scope, Now: fixture.now, LeaseDuration: time.Minute,
	}); err != nil || available {
		t.Fatalf("ClaimBoardPublication(before typed retry deadline) = available %t, err %v", available, err)
	}
	retryAt := fixture.now.Add(2 * time.Second)
	reclaimed, available, err := fixture.store.ClaimBoardPublication(ctx, BoardPublicationClaimInput{
		Scope: fixture.scope, Now: retryAt, LeaseDuration: time.Minute,
	})
	if err != nil || !available || len(reclaimed.Attempts) != 1 || !reflect.DeepEqual(reclaimed.Attempts[0], attempt) {
		t.Fatalf("ClaimBoardPublication(after specialized retry) = %#v, available %t, err %v; want same unresolved bytes", reclaimed, available, err)
	}
}

func TestBoardPublicationNewAttemptRequiresProvenSameScopeFence(t *testing.T) {
	fixture := newBoardPublicationDeliveryFixture(t)
	ctx := context.Background()
	lease, available, err := fixture.store.ClaimBoardPublication(ctx, BoardPublicationClaimInput{
		Scope: fixture.scope, Now: fixture.now, LeaseDuration: time.Minute,
	})
	if err != nil || !available {
		t.Fatalf("ClaimBoardPublication(valid scope) = available %t, err %v", available, err)
	}
	if _, err := fixture.store.RecordBoardPublicationClaimFence(ctx, BoardPublicationClaimFenceInput{
		Lease: lease, Now: fixture.now, AttemptOrdinal: 1, AttemptSHA256: strings.Repeat("0", 64),
		Proof: BoardPublicationClaimFenceProof{
			Scope: fixture.scope, PriorClaimID: "claim-absent", FreshClaimID: "claim-second",
			FreshReceiptSHA256: boardPublicationDigest([]byte("synthetic-private-claim-receipt-second")),
			FreshLeaseUntil:    fixture.now.Add(30 * time.Second), Disposition: BoardPublicationClaimFenceProvenUncommitted,
		},
	}); !errors.Is(err, ErrBoardPublicationAttemptConflict) {
		t.Fatalf("RecordBoardPublicationClaimFence(before any attempt) error = %v, want attempt conflict", err)
	}
	preparedFirst := preparedBoardPublicationBytes(lease, "claim-first", "synthetic-private-claim-receipt-first")
	attemptFirst, err := fixture.store.RecordBoardPublicationAttempt(ctx, BoardPublicationAttemptInput{
		Lease: lease, Now: fixture.now, LogicalKey: lease.LogicalKey,
		PreparedBytes: preparedFirst, PayloadSHA256: boardPublicationPayloadHash(preparedFirst),
		ProcessingClaimLeaseUntil: fixture.now.Add(30 * time.Second),
	})
	if err != nil {
		t.Fatalf("RecordBoardPublicationAttempt(first unresolved attempt): %v", err)
	}
	preparedSecond := preparedBoardPublicationBytes(lease, "claim-first", "synthetic-private-claim-receipt-second")
	secondInput := BoardPublicationAttemptInput{
		Lease: lease, Now: fixture.now, LogicalKey: lease.LogicalKey,
		PreparedBytes: preparedSecond, PayloadSHA256: boardPublicationPayloadHash(preparedSecond),
		ProcessingClaimLeaseUntil: fixture.now.Add(30 * time.Second),
	}
	if _, err := fixture.store.RecordBoardPublicationAttempt(ctx, secondInput); !errors.Is(err, ErrBoardPublicationAttemptConflict) {
		t.Fatalf("RecordBoardPublicationAttempt(new bytes before a fence) error = %v, want immutable attempt conflict", err)
	}
	if _, err := fixture.store.RecordBoardPublicationAttempt(ctx, BoardPublicationAttemptInput{
		Lease: lease, Now: fixture.now, LogicalKey: lease.LogicalKey + ":new-key",
		PreparedBytes: preparedSecond, PayloadSHA256: boardPublicationPayloadHash(preparedSecond),
	}); !errors.Is(err, ErrBoardPublicationAttemptConflict) {
		t.Fatalf("RecordBoardPublicationAttempt(new logical key) error = %v, want immutable attempt conflict", err)
	}

	fenceProof := BoardPublicationClaimFenceProof{
		Scope: fixture.scope, PriorClaimID: "claim-first", FreshClaimID: "claim-first",
		FreshReceiptSHA256: boardPublicationDigest([]byte("synthetic-private-claim-receipt-second")),
		FreshLeaseUntil:    fixture.now.Add(30 * time.Second), Disposition: BoardPublicationClaimFenceUnknown,
	}
	if _, err := fixture.store.RecordBoardPublicationClaimFence(ctx, BoardPublicationClaimFenceInput{
		Lease: lease, Now: fixture.now, AttemptOrdinal: attemptFirst.Ordinal,
		AttemptSHA256: attemptFirst.PayloadSHA256, Proof: fenceProof,
	}); !errors.Is(err, ErrBoardPublicationAttemptConflict) {
		t.Fatalf("RecordBoardPublicationClaimFence(unknown outcome) error = %v, want fail-closed attempt conflict", err)
	}
	if _, err := fixture.store.RecordBoardPublicationAttempt(ctx, secondInput); !errors.Is(err, ErrBoardPublicationAttemptConflict) {
		t.Fatalf("unknown attempt outcome authorized a new ordinal: error = %v", err)
	}

	reclaimAt := lease.LeaseExpiresAt.Add(time.Second)
	reclaimed, available, err := fixture.store.ClaimBoardPublication(ctx, BoardPublicationClaimInput{
		Scope: fixture.scope, Now: reclaimAt, LeaseDuration: time.Minute,
	})
	if err != nil || !available || len(reclaimed.Attempts) != 1 || !reflect.DeepEqual(reclaimed.Attempts[0], attemptFirst) {
		t.Fatalf("ClaimBoardPublication(reclaim before claim fence) = %#v, available %t, err %v", reclaimed, available, err)
	}
	fenceProof.Disposition = BoardPublicationClaimFenceProvenUncommitted
	fenceProof.FreshLeaseUntil = reclaimAt.Add(30 * time.Second)
	fence, err := fixture.store.RecordBoardPublicationClaimFence(ctx, BoardPublicationClaimFenceInput{
		Lease: reclaimed, Now: reclaimAt, AttemptOrdinal: attemptFirst.Ordinal,
		AttemptSHA256: attemptFirst.PayloadSHA256, Proof: fenceProof,
	})
	if err != nil {
		t.Fatalf("RecordBoardPublicationClaimFence(proven same-scope old attempt): %v", err)
	}
	if fence.WorkID != reclaimed.WorkID || fence.AttemptOrdinal != attemptFirst.Ordinal ||
		fence.AttemptSHA256 != attemptFirst.PayloadSHA256 || fence.LeaseVersion != reclaimed.Version ||
		fence.FreshClaimID != "claim-first" || fence.FreshReceiptSHA256 != fenceProof.FreshReceiptSHA256 ||
		!fence.FreshLeaseUntil.Equal(fenceProof.FreshLeaseUntil) {
		t.Fatalf("stored claim fence does not bind the exact old attempt and fresh proof: %#v", fence)
	}
	secondInput.Lease, secondInput.Now, secondInput.ProcessingClaimLeaseUntil = reclaimed, reclaimAt, fenceProof.FreshLeaseUntil
	attemptSecond, err := fixture.store.RecordBoardPublicationAttempt(ctx, secondInput)
	if err != nil {
		t.Fatalf("RecordBoardPublicationAttempt(after proven fence and fresh claim): %v", err)
	}
	if attemptSecond.Ordinal != attemptFirst.Ordinal+1 || attemptSecond.LogicalKey != attemptFirst.LogicalKey ||
		attemptSecond.PayloadSHA256 != secondInput.PayloadSHA256 || !bytes.Equal(attemptSecond.PreparedBytes, preparedSecond) ||
		!bytes.Equal(attemptFirst.PreparedBytes, preparedFirst) {
		t.Fatalf("new fenced attempt changed key/ordinal or old exact bytes: first=%#v second=%#v", attemptFirst, attemptSecond)
	}
}

func TestCompleteBoardPublicationRejectsMismatchedCanonicalTuple(t *testing.T) {
	fixture := newBoardPublicationDeliveryFixture(t)
	ctx := context.Background()
	lease, available, err := fixture.store.ClaimBoardPublication(ctx, BoardPublicationClaimInput{
		Scope: fixture.scope, Now: fixture.now, LeaseDuration: time.Minute,
	})
	if err != nil || !available {
		t.Fatalf("ClaimBoardPublication(valid scope) = available %t, err %v", available, err)
	}
	prepared := preparedBoardPublicationBytes(lease, "claim-completion", "synthetic-private-claim-receipt-completion")
	attempt, err := fixture.store.RecordBoardPublicationAttempt(ctx, BoardPublicationAttemptInput{
		Lease: lease, Now: fixture.now, LogicalKey: lease.LogicalKey, PreparedBytes: prepared,
		PayloadSHA256: boardPublicationPayloadHash(prepared), ProcessingClaimLeaseUntil: fixture.now.Add(30 * time.Second),
	})
	if err != nil {
		t.Fatalf("RecordBoardPublicationAttempt(completion tuple fixture): %v", err)
	}
	base := BoardPublicationCompletionInput{
		Lease: lease, Now: fixture.now, AttemptOrdinal: attempt.Ordinal, AttemptSHA256: attempt.PayloadSHA256,
		CanonicalMessage: boardPublicationCanonicalMessage(lease),
	}
	base.SendReceipt = BoardPublicationSendReceipt{
		MessageID: base.CanonicalMessage.MessageID, ThreadID: base.CanonicalMessage.ThreadID,
		StoredAt: fixture.now, Status: "stored",
	}
	for _, test := range []struct {
		name   string
		mutate func(*BoardPublicationCompletionInput)
	}{
		{name: "send receipt message", mutate: func(input *BoardPublicationCompletionInput) { input.SendReceipt.MessageID = "board-output-other" }},
		{name: "send receipt thread", mutate: func(input *BoardPublicationCompletionInput) { input.SendReceipt.ThreadID = "thread-other" }},
		{name: "canonical thread", mutate: func(input *BoardPublicationCompletionInput) { input.CanonicalMessage.ThreadID = "thread-other" }},
		{name: "channel", mutate: func(input *BoardPublicationCompletionInput) { input.CanonicalMessage.ChannelID = "channel-other" }},
		{name: "sender", mutate: func(input *BoardPublicationCompletionInput) {
			input.CanonicalMessage.SenderPrincipalID = "principal-other"
		}},
		{name: "expected peer", mutate: func(input *BoardPublicationCompletionInput) {
			input.CanonicalMessage.Recipients = []string{"peer-other"}
		}},
		{name: "reply parent", mutate: func(input *BoardPublicationCompletionInput) { input.CanonicalMessage.ReplyTo = "board-parent-other" }},
		{name: "causation", mutate: func(input *BoardPublicationCompletionInput) {
			input.CanonicalMessage.CausationID = "board-causation-other"
		}},
		{name: "hop count", mutate: func(input *BoardPublicationCompletionInput) { input.CanonicalMessage.HopCount++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			wrong := base
			wrong.CanonicalMessage.Recipients = append([]string(nil), base.CanonicalMessage.Recipients...)
			test.mutate(&wrong)
			if _, err := fixture.store.CompleteBoardPublication(ctx, wrong); err == nil {
				t.Fatalf("CompleteBoardPublication(mismatched %s) accepted an unbound BOARD tuple", test.name)
			}
			var state string
			if err := fixture.store.db.QueryRowContext(ctx, `SELECT state FROM live_work_outbox WHERE work_id=?`, lease.WorkID).Scan(&state); err != nil {
				t.Fatalf("read work state after rejected %s tuple: %v", test.name, err)
			}
			if state != "leased" {
				t.Fatalf("work state after rejected %s tuple = %q, want leased", test.name, state)
			}
		})
	}
	if _, err := fixture.store.db.ExecContext(ctx, `CREATE TRIGGER fail_board_publication_outcome BEFORE INSERT ON live_board_publication_outcome BEGIN SELECT RAISE(ABORT,'synthetic outcome insert failure'); END`); err != nil {
		t.Fatalf("install late BOARD outcome failure trigger: %v", err)
	}
	if _, err := fixture.store.CompleteBoardPublication(ctx, base); err == nil {
		t.Fatal("CompleteBoardPublication accepted an injected outcome insert failure")
	}
	var rollbackState string
	var outcomeCount int
	if err := fixture.store.db.QueryRowContext(ctx, `SELECT state FROM live_work_outbox WHERE work_id=?`, lease.WorkID).Scan(&rollbackState); err != nil {
		t.Fatalf("read work state after failed outcome insert: %v", err)
	}
	if err := fixture.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM live_board_publication_outcome WHERE work_id=?`, lease.WorkID).Scan(&outcomeCount); err != nil {
		t.Fatalf("count outcomes after failed outcome insert: %v", err)
	}
	if rollbackState != "leased" || outcomeCount != 0 {
		t.Fatalf("failed completion partially committed: work=%q outcomes=%d", rollbackState, outcomeCount)
	}
	if _, err := fixture.store.db.ExecContext(ctx, `DROP TRIGGER fail_board_publication_outcome`); err != nil {
		t.Fatalf("drop late BOARD outcome failure trigger: %v", err)
	}
	if _, err := fixture.store.CompleteBoardPublication(ctx, base); err != nil {
		t.Fatalf("CompleteBoardPublication(exact canonical tuple after rejected variants): %v", err)
	}
}

func TestGenericWorkAPIsExcludeBoardPublication(t *testing.T) {
	for _, operation := range []string{"ack", "retry"} {
		t.Run(operation, func(t *testing.T) {
			fixture := newBoardPublicationDeliveryFixture(t)
			ctx := context.Background()
			workID := externalBoardPublicationWorkID(fixture.lookup.OperationID)
			for _, kind := range []string{"", "board_publication"} {
				claims, err := fixture.store.ClaimWork(ctx, WorkClaimInput{
					Kind: kind, Limit: 1, LeaseDuration: time.Minute, Now: fixture.now,
				})
				if err != nil || len(claims) != 0 {
					t.Fatalf("ClaimWork(kind=%q) exposed board_publication: claims=%#v err=%v", kind, claims, err)
				}
			}
			token := "synthetic-generic-lease-token"
			expires := formatWorkTime(fixture.now.Add(time.Minute))
			if _, err := fixture.store.db.ExecContext(ctx, `
				UPDATE live_work_outbox
				SET state='leased', attempt=attempt+1, lease_token=?, lease_expires_at=?, version=version+1
				WHERE work_id=? AND kind='board_publication' AND state='pending'`, token, expires, workID); err != nil {
				t.Fatalf("seed current generic lease-shaped fence for isolation: %v", err)
			}
			var version int64
			if err := fixture.store.db.QueryRowContext(ctx, `SELECT version FROM live_work_outbox WHERE work_id=?`, workID).Scan(&version); err != nil {
				t.Fatalf("read synthetic lease version: %v", err)
			}
			lease := WorkLeaseInput{WorkID: workID, LeaseToken: token, Version: version, Now: fixture.now}
			if operation == "ack" {
				if err := fixture.store.AckWork(ctx, lease); err == nil {
					t.Fatal("AckWork accepted board_publication despite its specialized owner boundary")
				}
			} else if err := fixture.store.RetryWork(ctx, WorkRetryInput{WorkLeaseInput: lease}); err == nil {
				t.Fatal("RetryWork accepted board_publication despite its specialized owner boundary")
			}
			var state, storedToken string
			var storedVersion int64
			if err := fixture.store.db.QueryRowContext(ctx, `SELECT state,lease_token,version FROM live_work_outbox WHERE work_id=?`, workID).Scan(&state, &storedToken, &storedVersion); err != nil {
				t.Fatalf("read work after generic completion/retry attempt: %v", err)
			}
			if state != "leased" || storedToken != token || storedVersion != version {
				t.Fatalf("generic %s mutated specialized BOARD lease: state=%q token=%q version=%d", operation, state, storedToken, storedVersion)
			}
		})
	}
}

func TestSafeErrorBoardPublicationUsesSourceBoundResponderForClaimAndCompletion(t *testing.T) {
	ctx := context.Background()
	fixture := newSafeErrorBoardPublicationDeliveryFixtureForResponder(t, "shiro", true)
	response := externalResponseEvent(fixture.bundle.CanonicalEvents, fixture.bundle.PublicationIntent.CanonicalResponseMessageID)
	if !fixture.bundle.SafeError || fixture.bundle.ExpectedLookup.SourceBinding.EffectiveResponderActorID() != "shiro" ||
		response == nil || response.ActorID != "" || payloadString(response.Payload, "from") != "shiro" ||
		payloadString(response.Payload, "kind") != "turn_error" {
		t.Fatalf("safe-error fixture does not preserve its actorless canonical response and trusted Shiro binding: bundle=%#v response=%#v", fixture.bundle, response)
	}

	// The legacy pin allows the deliberately different Mio request to reach the
	// canonical actor check; it must still be rejected without leasing the row.
	if _, err := fixture.store.PinBoardConnection(ctx, BoardConnectionPin{
		OwnerID: fixture.scope.OwnerID, JournalInstanceID: fixture.scope.JournalInstanceID,
		PrincipalID: fixture.scope.PrincipalID, CutoverAt: fixture.accepted.InputEvent.OccurredAt.UTC(),
	}); err != nil {
		t.Fatalf("PinBoardConnection(legacy mismatch-path pin): %v", err)
	}
	if _, available, err := fixture.store.ClaimBoardPublication(ctx, BoardPublicationClaimInput{
		Scope: fixture.scope, ResponderActorID: "mio", Now: fixture.now, LeaseDuration: time.Minute,
	}); available || !errors.Is(err, ErrBoardPublicationScopeMismatch) {
		t.Fatalf("ClaimBoardPublication(wrong Mio responder for safe-error Shiro source) = available %t, err %v; want scope error", available, err)
	}
	var state string
	var attempt int
	if err := fixture.store.db.QueryRowContext(ctx, `SELECT state,attempt FROM live_work_outbox WHERE work_id=?`, externalBoardPublicationWorkID(fixture.lookup.OperationID)).Scan(&state, &attempt); err != nil {
		t.Fatalf("read safe-error work after wrong responder: %v", err)
	}
	if state != "pending" || attempt != 0 {
		t.Fatalf("wrong safe-error responder mutated work: state=%q attempt=%d", state, attempt)
	}

	lease, available, err := fixture.store.ClaimBoardPublication(ctx, BoardPublicationClaimInput{
		Scope: fixture.scope, ResponderActorID: "shiro", Now: fixture.now, LeaseDuration: time.Minute,
	})
	if err != nil || !available || lease.CanonicalResponse.ActorID != "" ||
		fixture.bundle.ExpectedLookup.SourceBinding.EffectiveResponderActorID() != "shiro" {
		t.Fatalf("ClaimBoardPublication(safe-error Shiro source) = available %t lease=%#v err %v; want actorless response claimed by its bound responder", available, lease, err)
	}
	prepared := preparedBoardPublicationBytes(lease, "safe-error-claim", "synthetic-safe-error-claim-receipt")
	attemptRecord, err := fixture.store.RecordBoardPublicationAttempt(ctx, BoardPublicationAttemptInput{
		Lease: lease, Now: fixture.now, LogicalKey: lease.LogicalKey, PreparedBytes: prepared,
		PayloadSHA256: boardPublicationPayloadHash(prepared), ProcessingClaimLeaseUntil: fixture.now.Add(30 * time.Second),
	})
	if err != nil {
		t.Fatalf("RecordBoardPublicationAttempt(safe-error exact response): %v", err)
	}
	message := boardPublicationCanonicalMessage(lease)
	receipt := BoardPublicationSendReceipt{MessageID: message.MessageID, ThreadID: message.ThreadID, StoredAt: fixture.now, Status: "stored"}
	outcome, err := fixture.store.CompleteBoardPublication(ctx, BoardPublicationCompletionInput{
		Lease: lease, Now: fixture.now, AttemptOrdinal: attemptRecord.Ordinal,
		AttemptSHA256: attemptRecord.PayloadSHA256, SendReceipt: receipt, CanonicalMessage: message,
	})
	if err != nil {
		t.Fatalf("CompleteBoardPublication(safe-error Shiro source): %v", err)
	}
	fixture.reopen(t)
	loaded, found, err := fixture.store.LoadBoardPublicationByTurn(ctx, BoardPublicationTurnLookupInput{
		Scope: fixture.scope, SessionID: fixture.accepted.SessionID, TurnID: fixture.accepted.TurnID,
		ResponderActorID: "shiro",
	})
	if err != nil || !found || !reflect.DeepEqual(loaded, outcome) {
		t.Fatalf("LoadBoardPublicationByTurn(completed safe-error Shiro source) = %#v found=%t err=%v; want exact outcome", loaded, found, err)
	}
}

func TestBoardPublicationClaimUsesResponderScopedPin(t *testing.T) {
	ctx := context.Background()
	for _, actorID := range []string{"shiro", "midori", "kuro"} {
		t.Run(actorID, func(t *testing.T) {
			fixture := newBoardPublicationDeliveryFixtureForResponder(t, actorID, true)
			if _, available, err := fixture.store.ClaimBoardPublication(ctx, BoardPublicationClaimInput{
				Scope: fixture.scope, Now: fixture.now, LeaseDuration: time.Minute,
			}); err != nil || !available {
				t.Fatalf("ClaimBoardPublication(explicit %s responder with scoped pin) = available %t, err %v; want scoped lease", actorID, available, err)
			}
		})
	}
}

func TestKuroBoardPublicationStoresReplyAndReadbackAcrossReopen(t *testing.T) {
	ctx := context.Background()
	fixture := newBoardPublicationDeliveryFixtureForResponder(t, "kuro", true)
	lease, available, err := fixture.store.ClaimBoardPublication(ctx, BoardPublicationClaimInput{
		Scope: fixture.scope, Now: fixture.now, LeaseDuration: time.Minute,
	})
	if err != nil || !available || lease.CanonicalResponse.ActorID != "kuro" || payloadString(lease.CanonicalResponse.Payload, "from") != "kuro" {
		t.Fatalf("ClaimBoardPublication(Kuro) = lease=%#v available=%t err=%v; want Kuro-authored scoped reply", lease, available, err)
	}
	prepared := preparedBoardPublicationBytes(lease, "kuro-board-chat-claim", "synthetic-kuro-processing-receipt")
	attempt, err := fixture.store.RecordBoardPublicationAttempt(ctx, BoardPublicationAttemptInput{
		Lease: lease, Now: fixture.now, LogicalKey: lease.LogicalKey, PreparedBytes: prepared, PayloadSHA256: boardPublicationPayloadHash(prepared),
		ProcessingClaimLeaseUntil: fixture.now.Add(30 * time.Second),
	})
	if err != nil || attempt.Ordinal != 1 {
		t.Fatalf("RecordBoardPublicationAttempt(Kuro exact reply): attempt=%#v err=%v", attempt, err)
	}
	message := boardPublicationCanonicalMessage(lease)
	receipt := BoardPublicationSendReceipt{MessageID: message.MessageID, ThreadID: message.ThreadID, StoredAt: fixture.now, Status: "stored"}
	outcome, err := fixture.store.CompleteBoardPublication(ctx, BoardPublicationCompletionInput{
		Lease: lease, Now: fixture.now, AttemptOrdinal: attempt.Ordinal, AttemptSHA256: attempt.PayloadSHA256,
		SendReceipt: receipt, CanonicalMessage: message,
	})
	if err != nil || outcome.CanonicalMessage.SenderPrincipalID != "kuro" || outcome.CanonicalMessage.ReplyTo != "board-message-b2-delivery" {
		t.Fatalf("CompleteBoardPublication(Kuro reply) = outcome=%#v err=%v; want saved reply from pinned Kuro principal", outcome, err)
	}
	fixture.reopen(t)
	loaded, found, err := fixture.store.LoadBoardPublicationByTurn(ctx, BoardPublicationTurnLookupInput{
		Scope: fixture.scope, SessionID: fixture.lookup.SessionID, TurnID: fixture.lookup.TurnID, ResponderActorID: "kuro",
	})
	if err != nil || !found || !reflect.DeepEqual(loaded, outcome) {
		t.Fatalf("LoadBoardPublicationByTurn(Kuro after reopen) = %#v found=%t err=%v; want exact stored reply receipt", loaded, found, err)
	}
}

func TestBoardPublicationExplicitResponderDoesNotFallBackToLegacyPin(t *testing.T) {
	ctx := context.Background()
	fixture := newBoardPublicationDeliveryFixtureForResponder(t, "shiro", false)
	if _, available, err := fixture.store.ClaimBoardPublication(ctx, BoardPublicationClaimInput{
		Scope: fixture.scope, Now: fixture.now, LeaseDuration: time.Minute,
	}); available || !errors.Is(err, ErrBoardPublicationScopeMismatch) {
		t.Fatalf("ClaimBoardPublication(explicit Shiro responder with legacy pin only) = available %t, err %v; want scoped-pin rejection", available, err)
	}
	pin := BoardConnectionPin{
		OwnerID: fixture.scope.OwnerID, JournalInstanceID: fixture.scope.JournalInstanceID,
		PrincipalID: fixture.scope.PrincipalID, CutoverAt: fixture.accepted.InputEvent.OccurredAt.UTC(),
	}
	if _, err := fixture.store.PinBoardConnectionForPrincipal(ctx, pin); err != nil {
		t.Fatalf("PinBoardConnectionForPrincipal(Shiro responder): %v", err)
	}
	if _, available, err := fixture.store.ClaimBoardPublication(ctx, BoardPublicationClaimInput{
		Scope: fixture.scope, Now: fixture.now, LeaseDuration: time.Minute,
	}); err != nil || !available {
		t.Fatalf("ClaimBoardPublication(after installing scoped pin) = available %t, err %v; want scoped lease", available, err)
	}
}

func TestBoardPublicationLegacyResponderIgnoresUnrelatedScopedPin(t *testing.T) {
	ctx := context.Background()
	fixture := newBoardPublicationDeliveryFixture(t)
	unrelated := BoardConnectionPin{
		OwnerID: fixture.scope.OwnerID + "-other", JournalInstanceID: fixture.scope.JournalInstanceID,
		PrincipalID: fixture.scope.PrincipalID, CutoverAt: fixture.accepted.InputEvent.OccurredAt.UTC(),
	}
	if _, err := fixture.store.PinBoardConnectionForPrincipal(ctx, unrelated); err != nil {
		t.Fatalf("PinBoardConnectionForPrincipal(unrelated scoped receipt): %v", err)
	}
	if _, available, err := fixture.store.ClaimBoardPublication(ctx, BoardPublicationClaimInput{
		Scope: fixture.scope, Now: fixture.now, LeaseDuration: time.Minute,
	}); err != nil || !available {
		t.Fatalf("ClaimBoardPublication(legacy Mio responder with unrelated scoped pin) = available %t, err %v; want legacy pin lease", available, err)
	}
}

type boardPublicationDeliveryFixture struct {
	store        *SQLiteStore
	databasePath string
	input        AcceptInput
	accepted     AcceptedTurn
	lookup       conversationruntime.ExternalTurnLookup
	bundle       conversationruntime.ExternalCommitBundle
	scope        BoardPublicationScope
	now          time.Time
}

func newBoardPublicationDeliveryFixture(t *testing.T) *boardPublicationDeliveryFixture {
	return newBoardPublicationDeliveryFixtureForResponder(t, "", false)
}

func newBoardPublicationDeliveryFixtureForResponder(t *testing.T, responderActorID string, scopedPin bool) *boardPublicationDeliveryFixture {
	return newBoardPublicationDeliveryFixtureForResponderOutcome(t, responderActorID, scopedPin, false)
}

func newSafeErrorBoardPublicationDeliveryFixtureForResponder(t *testing.T, responderActorID string, scopedPin bool) *boardPublicationDeliveryFixture {
	return newBoardPublicationDeliveryFixtureForResponderOutcome(t, responderActorID, scopedPin, true)
}

func newBoardPublicationDeliveryFixtureForResponderOutcome(t *testing.T, responderActorID string, scopedPin, safeError bool) *boardPublicationDeliveryFixture {
	return newBoardPublicationDeliveryFixtureForSource(t, responderActorID, scopedPin, safeError, "grok")
}

func newBoardPublicationDeliveryFixtureForSource(t *testing.T, responderActorID string, scopedPin, safeError bool, senderPrincipalID string) *boardPublicationDeliveryFixture {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "board-publication-delivery.sqlite")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	fixture := &boardPublicationDeliveryFixture{store: store, databasePath: path}
	t.Cleanup(func() {
		if fixture.store != nil {
			if err := fixture.store.Close(); err != nil {
				t.Errorf("Close(): %v", err)
			}
		}
	})
	const journalInstanceID = "journal_0123456789abcdef0123456789abcdef"
	fixture.input = externalPeerAcceptInput(modulecore.NewSessionID(), "board-message-b2-delivery", "Synthetic publication delivery request.")
	fixture.input.ExternalPeerBinding.BoardInstanceID = journalInstanceID
	fixture.input.ExternalPeerBinding.SenderPrincipalID = senderPrincipalID
	if responderActorID != "" {
		fixture.input.ExternalPeerBinding.ResponderActorID = responderActorID
		fixture.input.ExternalPeerBinding.RecipientPrincipalID = responderActorID
		fixture.input.InputEvent.Payload["to"] = fixture.input.ExternalPeerBinding.EffectiveResponderActorID()
		fixture.input.InputMessage.ToActor = fixture.input.ExternalPeerBinding.EffectiveResponderActorID()
	}
	fixture.input.InputEvent.Payload["external_peer_binding"] = externalPeerBindingPayload(*fixture.input.ExternalPeerBinding)
	sourceActorID := "board:" + journalInstanceID + ":" + fixture.input.ExternalPeerBinding.SenderPrincipalID
	fixture.input.ActorID = sourceActorID
	fixture.input.InputEvent.Payload["source_actor_id"] = sourceActorID
	fixture.input.InputMessage.FromActor = sourceActorID
	fixture.accepted, err = store.AcceptTurn(ctx, fixture.input)
	if err != nil {
		t.Fatalf("AcceptTurn(real external peer EventStore fixture): %v", err)
	}
	fixture.lookup, fixture.bundle = externalPublicationCommitFixture(t, fixture.input, fixture.accepted)
	if safeError {
		fixture.bundle = externalPublicationSafeErrorBundle(t, fixture.bundle)
	}
	pin := BoardConnectionPin{
		OwnerID: fixture.lookup.OwnerID, JournalInstanceID: journalInstanceID,
		PrincipalID: fixture.lookup.SourceBinding.RecipientPrincipalID, CutoverAt: fixture.accepted.InputEvent.OccurredAt.UTC(),
	}
	if scopedPin {
		if _, err := store.PinBoardConnectionForPrincipal(ctx, pin); err != nil {
			t.Fatalf("PinBoardConnectionForPrincipal(synthetic exact owner scope): %v", err)
		}
	} else if _, err := store.PinBoardConnection(ctx, pin); err != nil {
		t.Fatalf("PinBoardConnection(synthetic exact owner scope): %v", err)
	}
	if _, err := store.CommitExternalTurnBundle(ctx, fixture.bundle); err != nil {
		t.Fatalf("CommitExternalTurnBundle(real B1 canonical response fixture): %v", err)
	}
	fixture.scope = BoardPublicationScope{
		OwnerID: pin.OwnerID, JournalInstanceID: pin.JournalInstanceID, PrincipalID: pin.PrincipalID,
	}
	fixture.now = fixture.accepted.InputEvent.OccurredAt.Add(5 * time.Minute)
	return fixture
}

func externalPublicationSafeErrorBundle(t *testing.T, bundle conversationruntime.ExternalCommitBundle) conversationruntime.ExternalCommitBundle {
	t.Helper()
	const safeText = "A safe response could not be produced."
	bundle.SafeError = true
	bundle.FailureStage = conversationruntime.ExternalFailureStageGeneration
	bundle.InvocationReceipt.Status = "generation_failed"
	responseFound, receiptFound := false, false
	for index := range bundle.CanonicalEvents {
		event := &bundle.CanonicalEvents[index]
		if event.MessageID == bundle.PublicationIntent.CanonicalResponseMessageID {
			event.ActorKind, event.ActorID = "", ""
			event.Payload["kind"] = "turn_error"
			event.Payload["from"] = bundle.ExpectedLookup.SourceBinding.EffectiveResponderActorID()
			event.Payload["source_actor_id"] = bundle.ExpectedLookup.SourceBinding.EffectiveResponderActorID()
			event.Payload["text"], event.Payload["content"] = safeText, safeText
			responseFound = true
		} else if event.EventType == "conversation.mio_emission_receipt" {
			event.Payload["status"] = bundle.InvocationReceipt.Status
			receiptFound = true
		}
	}
	if !responseFound || !receiptFound || len(bundle.MessageIndexes) != 1 {
		t.Fatal("safe-error publication fixture is missing its canonical response, receipt, or message index")
	}
	bundle.MessageIndexes[0].Kind = "turn_error"
	return bundle
}

func (fixture *boardPublicationDeliveryFixture) reopen(t *testing.T) {
	t.Helper()
	if err := fixture.store.Close(); err != nil {
		t.Fatalf("Close(before delivery recovery): %v", err)
	}
	fixture.store = nil
	store, err := NewSQLiteStore(fixture.databasePath)
	if err != nil {
		t.Fatalf("NewSQLiteStore(reopen delivery journal): %v", err)
	}
	fixture.store = store
}

func preparedBoardPublicationBytes(lease BoardPublicationLease, claimID, claimReceipt string) []byte {
	target := lease.PublicationIntent.Target
	body := payloadString(lease.CanonicalResponse.Payload, "text")
	prepared, _ := json.Marshal(map[string]any{
		"channel_id": target.ChannelID, "thread_id": target.ThreadID, "body": body,
		"recipients": []string{target.PeerPrincipalID}, "reply_to": target.ReplyTo, "kind": "reply",
		"idempotency_key": lease.LogicalKey, "processing_claim_id": claimID,
		"processing_claim_receipt": claimReceipt,
	})
	return prepared
}

func boardPublicationPayloadHash(prepared []byte) string {
	hash := sha256.Sum256(prepared)
	return hex.EncodeToString(hash[:])
}

func boardPublicationCanonicalMessage(lease BoardPublicationLease) BoardPublicationCanonicalMessage {
	target := lease.PublicationIntent.Target
	return BoardPublicationCanonicalMessage{
		MessageID: "board-output-b2-canonical-1", ThreadID: target.ThreadID, ChannelID: target.ChannelID,
		SenderPrincipalID: target.CorePrincipalID, Recipients: []string{target.PeerPrincipalID},
		ReplyTo: target.ReplyTo, CausationID: string(target.ReplyTo), HopCount: 1,
	}
}
