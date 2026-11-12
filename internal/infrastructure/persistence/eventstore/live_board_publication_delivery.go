package eventstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var (
	ErrBoardPublicationDeliveryUnavailable = errors.New("board publication delivery journal is unavailable")
	ErrBoardPublicationScopeMismatch       = errors.New("board publication scope does not match the stored intent")
	ErrBoardPublicationAttemptConflict     = errors.New("board publication attempt conflicts with its immutable history")
	ErrBoardPublicationOutcomeNotFound     = errors.New("board publication outcome was not found")
)

// BoardPublicationScope binds delivery operations to the owner and the
// currently pinned BOARD journal and principal.
type BoardPublicationScope struct {
	OwnerID           string
	JournalInstanceID string
	PrincipalID       string
}

type BoardPublicationClaimInput struct {
	Scope                      BoardPublicationScope
	ResponderActorID           string `json:"responder_actor_id,omitempty"`
	CanonicalResponseMessageID modulecore.MessageID
	Now                        time.Time
	LeaseDuration              time.Duration
}

// BoardPublicationLease carries the existing outbox fence and the canonical
// response and private target needed to prepare a BOARD send.
type BoardPublicationLease struct {
	Scope             BoardPublicationScope
	WorkID            string
	LogicalKey        string
	LeaseToken        string
	LeaseExpiresAt    time.Time
	Version           int64
	QueueAttempt      int
	TargetKind        string
	Title             string
	CanonicalResponse modulecore.EventEnvelope
	ResponseIndex     MessageIndex
	PublicationIntent conversationruntime.ExternalPublicationIntent
	Attempts          []BoardPublicationAttempt
	Fences            []BoardPublicationClaimFence
}

// BoardPublicationAttempt contains the immutable exact JSON bytes prepared
// for one ordinal send attempt. Claim proof remains inside PreparedBytes.
type BoardPublicationAttempt struct {
	WorkID                       string
	Ordinal                      int
	LogicalKey                   string
	PreparedBytes                []byte
	PayloadSHA256                string
	ProcessingClaimID            string
	ProcessingClaimReceiptSHA256 string
	ProcessingClaimLeaseUntil    time.Time
}

type BoardPublicationAttemptInput struct {
	Lease                     BoardPublicationLease
	Now                       time.Time
	LogicalKey                string
	PreparedBytes             []byte
	PayloadSHA256             string
	ProcessingClaimLeaseUntil time.Time
}

type BoardPublicationClaimFenceDisposition string

const (
	BoardPublicationClaimFenceUnknown           BoardPublicationClaimFenceDisposition = "unknown"
	BoardPublicationClaimFenceProvenUncommitted BoardPublicationClaimFenceDisposition = "proven_uncommitted_fenced"
)

// BoardPublicationClaimFenceProof is an application-verified, same-scope
// fence for one exact prior attempt. Raw claim receipt material stays only in
// the immutable prepared JSON bytes.
type BoardPublicationClaimFenceProof struct {
	Scope              BoardPublicationScope
	PriorClaimID       string
	FreshClaimID       string
	FreshReceiptSHA256 string
	FreshLeaseUntil    time.Time
	Disposition        BoardPublicationClaimFenceDisposition
}

type BoardPublicationClaimFenceInput struct {
	Lease          BoardPublicationLease
	Now            time.Time
	AttemptOrdinal int
	AttemptSHA256  string
	Proof          BoardPublicationClaimFenceProof
}

type BoardPublicationClaimFence struct {
	WorkID             string
	AttemptOrdinal     int
	AttemptSHA256      string
	LeaseVersion       int64
	FreshClaimID       string
	FreshReceiptSHA256 string
	FreshLeaseUntil    time.Time
}

type BoardPublicationRetryInput struct {
	Lease BoardPublicationLease
	Now   time.Time
}

// BoardPublicationSendReceipt is the non-secret durable receipt returned by
// BOARD for a stored send.
type BoardPublicationSendReceipt struct {
	MessageID string
	ThreadID  string
	StoredAt  time.Time
	Status    string
}

// BoardPublicationCanonicalMessage keeps only routing and causal metadata;
// the message body remains authoritative in the CORE response event.
type BoardPublicationCanonicalMessage struct {
	MessageID         string
	ThreadID          string
	ChannelID         string
	SenderPrincipalID string
	Recipients        []string
	ReplyTo           string
	CausationID       string
	HopCount          int
}

type BoardPublicationCompletionInput struct {
	Lease            BoardPublicationLease
	Now              time.Time
	AttemptOrdinal   int
	AttemptSHA256    string
	SendReceipt      BoardPublicationSendReceipt
	CanonicalMessage BoardPublicationCanonicalMessage
}

// BoardPublicationOutcome binds the accepted BOARD send receipt and canonical
// message tuple to the exact CORE publication attempt.
type BoardPublicationOutcome struct {
	Scope                   BoardPublicationScope
	WorkID                  string
	LogicalKey              string
	AttemptOrdinal          int
	PayloadSHA256           string
	SendReceipt             BoardPublicationSendReceipt
	CanonicalMessage        BoardPublicationCanonicalMessage
	ExpectedPeerPrincipalID string
}

type BoardPublicationLookupInput struct {
	Scope           BoardPublicationScope
	ParentMessageID string
}

// ClaimBoardPublication claims one matching existing board_publication row.
// available=false,nil means no matching work is currently due.
func (s *SQLiteStore) ClaimBoardPublication(ctx context.Context, input BoardPublicationClaimInput) (BoardPublicationLease, bool, error) {
	if err := s.ensureOpen(); err != nil {
		return BoardPublicationLease{}, false, err
	}
	if ctx == nil {
		return BoardPublicationLease{}, false, errors.New("BOARD publication claim context is required")
	}
	if err := validateBoardPublicationScope(input.Scope); err != nil {
		return BoardPublicationLease{}, false, err
	}
	if input.ResponderActorID != "" && input.ResponderActorID != "mio" &&
		(!domainconversation.IsExternalPeerResponderActor(input.ResponderActorID) || input.ResponderActorID != input.Scope.PrincipalID) {
		return BoardPublicationLease{}, false, ErrBoardPublicationScopeMismatch
	}
	if input.LeaseDuration <= 0 || input.LeaseDuration > maxWorkLeaseDuration {
		return BoardPublicationLease{}, false, fmt.Errorf("BOARD publication lease duration must be positive and at most %s", maxWorkLeaseDuration)
	}
	if input.CanonicalResponseMessageID != "" {
		if err := input.CanonicalResponseMessageID.Validate(); err != nil {
			return BoardPublicationLease{}, false, fmt.Errorf("canonical response message id: %w", err)
		}
	}
	now := normalizedNow(input.Now)
	nowText := formatWorkTime(now)
	expiresAt := now.Add(input.LeaseDuration)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BoardPublicationLease{}, false, err
	}
	defer tx.Rollback()
	var pinErr error
	if input.ResponderActorID == "" {
		pinErr = requireBoardPublicationPinCandidateTx(ctx, tx, input.Scope)
	} else {
		pinErr = requireBoardPublicationPinForResponderTx(ctx, tx, input.Scope, input.ResponderActorID)
	}
	if pinErr != nil {
		return BoardPublicationLease{}, false, pinErr
	}
	var workID string
	err = tx.QueryRowContext(ctx, `
		SELECT w.work_id
		FROM live_work_outbox w
		JOIN live_board_publication p ON p.work_id=w.work_id
		JOIN live_conversation_session s ON s.session_id=w.session_id
		LEFT JOIN live_board_publication_outcome o ON o.work_id=w.work_id
		WHERE w.kind='board_publication' AND s.owner_id=? AND p.core_principal_id=?
		  AND (?='' OR p.response_message_id=?)
		  AND o.work_id IS NULL
		  AND ((w.state='pending' AND (w.not_before='' OR julianday(w.not_before)<=julianday(?)))
		    OR (w.state='leased' AND w.lease_expires_at IS NOT NULL AND julianday(w.lease_expires_at)<=julianday(?)))
		ORDER BY w.priority ASC, CASE WHEN w.not_before='' THEN 0 ELSE julianday(w.not_before) END ASC, w.work_id ASC
		LIMIT 1`, input.Scope.OwnerID, input.Scope.PrincipalID,
		string(input.CanonicalResponseMessageID), string(input.CanonicalResponseMessageID), nowText, nowText).Scan(&workID)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return BoardPublicationLease{}, false, err
		}
		return BoardPublicationLease{}, false, nil
	}
	if err != nil {
		return BoardPublicationLease{}, false, err
	}
	lease, bundle, _, _, err := loadBoardPublicationSourceTx(ctx, tx, workID, input.Scope)
	if err != nil {
		return BoardPublicationLease{}, false, err
	}
	storedResponderActorID := lease.CanonicalResponse.ActorID
	if bundle.SafeError {
		storedResponderActorID = bundle.Lookup.SourceBinding.EffectiveResponderActorID()
	}
	if input.ResponderActorID != "" && !boardPublicationActorMatchesResponder(storedResponderActorID, input.ResponderActorID) {
		return BoardPublicationLease{}, false, ErrBoardPublicationScopeMismatch
	}
	token := string(modulecore.NewRequestID())
	result, err := tx.ExecContext(ctx, `
		UPDATE live_work_outbox
		SET state='leased',attempt=attempt+1,lease_token=?,lease_expires_at=?,version=version+1
		WHERE work_id=? AND kind='board_publication' AND (
			(state='pending' AND (not_before='' OR julianday(not_before)<=julianday(?))) OR
			(state='leased' AND lease_expires_at IS NOT NULL AND julianday(lease_expires_at)<=julianday(?))
		) AND NOT EXISTS (SELECT 1 FROM live_board_publication_outcome o WHERE o.work_id=live_work_outbox.work_id)`,
		token, formatWorkTime(expiresAt), workID, nowText, nowText)
	if err != nil {
		return BoardPublicationLease{}, false, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return BoardPublicationLease{}, false, err
	}
	if updated != 1 {
		if err := tx.Commit(); err != nil {
			return BoardPublicationLease{}, false, err
		}
		return BoardPublicationLease{}, false, nil
	}
	work, state, err := readWorkForUpdate(ctx, tx, workID)
	if err != nil || state != "leased" {
		if err != nil {
			return BoardPublicationLease{}, false, err
		}
		return BoardPublicationLease{}, false, ErrWorkConflict
	}
	lease, _, _, _, err = loadCurrentBoardPublicationLeaseTx(ctx, tx, BoardPublicationLease{
		Scope: input.Scope, WorkID: workID, LeaseToken: token, LeaseExpiresAt: work.LeaseExpiresAt, Version: work.Version,
	}, now)
	if err != nil {
		return BoardPublicationLease{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return BoardPublicationLease{}, false, err
	}
	return lease, true, nil
}

// RecordBoardPublicationAttempt durably records exact prepared bytes before
// transport. Exact replays return the existing immutable ordinal.
func (s *SQLiteStore) RecordBoardPublicationAttempt(ctx context.Context, input BoardPublicationAttemptInput) (BoardPublicationAttempt, error) {
	if err := s.ensureOpen(); err != nil {
		return BoardPublicationAttempt{}, err
	}
	if ctx == nil {
		return BoardPublicationAttempt{}, errors.New("BOARD publication attempt context is required")
	}
	now := normalizedNow(input.Now)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BoardPublicationAttempt{}, err
	}
	defer tx.Rollback()
	lease, _, _, _, err := loadCurrentBoardPublicationLeaseTx(ctx, tx, input.Lease, now)
	if err != nil {
		return BoardPublicationAttempt{}, err
	}
	prepared, err := validateBoardPublicationPrepared(lease, input)
	if err != nil {
		return BoardPublicationAttempt{}, err
	}
	if exists, err := boardPublicationOutcomeExistsTx(ctx, tx, lease.WorkID); err != nil {
		return BoardPublicationAttempt{}, err
	} else if exists {
		return BoardPublicationAttempt{}, ErrBoardPublicationAttemptConflict
	}
	attempts, err := loadBoardPublicationAttemptsTx(ctx, tx, lease.WorkID)
	if err != nil {
		return BoardPublicationAttempt{}, err
	}
	if len(attempts) > 0 {
		last := attempts[len(attempts)-1]
		if last.LogicalKey == input.LogicalKey && last.PayloadSHA256 == input.PayloadSHA256 &&
			last.ProcessingClaimLeaseUntil.Equal(input.ProcessingClaimLeaseUntil) && string(last.PreparedBytes) == string(input.PreparedBytes) {
			if err := tx.Commit(); err != nil {
				return BoardPublicationAttempt{}, err
			}
			return last, nil
		}
		if normalizedBoardPublicationTargetKind(lease.TargetKind) == boardPublicationTargetNewRoot {
			return BoardPublicationAttempt{}, ErrBoardPublicationAttemptConflict
		}
		fence, found, err := loadUsableBoardPublicationFenceTx(ctx, tx, lease.WorkID, last.Ordinal, prepared.ProcessingClaimID, prepared.ReceiptSHA256, input.ProcessingClaimLeaseUntil, now)
		if err != nil {
			return BoardPublicationAttempt{}, err
		}
		if !found || fence.AttemptSHA256 != last.PayloadSHA256 {
			return BoardPublicationAttempt{}, ErrBoardPublicationAttemptConflict
		}
		attempt, err := insertBoardPublicationAttemptTx(ctx, tx, lease.WorkID, last.Ordinal+1, input, prepared, last.Ordinal, fence.FreshReceiptSHA256)
		if err != nil {
			return BoardPublicationAttempt{}, err
		}
		if err := tx.Commit(); err != nil {
			return BoardPublicationAttempt{}, err
		}
		return attempt, nil
	}
	if normalizedBoardPublicationTargetKind(lease.TargetKind) == boardPublicationTargetNewRoot {
		if !input.ProcessingClaimLeaseUntil.IsZero() {
			return BoardPublicationAttempt{}, ErrBoardPublicationAttemptConflict
		}
	} else if !input.ProcessingClaimLeaseUntil.After(now) {
		return BoardPublicationAttempt{}, ErrBoardPublicationAttemptConflict
	}
	attempt, err := insertBoardPublicationAttemptTx(ctx, tx, lease.WorkID, 1, input, prepared, 0, "")
	if err != nil {
		return BoardPublicationAttempt{}, err
	}
	if err := tx.Commit(); err != nil {
		return BoardPublicationAttempt{}, err
	}
	return attempt, nil
}

// RecordBoardPublicationClaimFence records a proven same-scope fence for an
// exact unresolved prior attempt before any later ordinal can be created.
func (s *SQLiteStore) RecordBoardPublicationClaimFence(ctx context.Context, input BoardPublicationClaimFenceInput) (BoardPublicationClaimFence, error) {
	if err := s.ensureOpen(); err != nil {
		return BoardPublicationClaimFence{}, err
	}
	if ctx == nil {
		return BoardPublicationClaimFence{}, errors.New("BOARD publication claim fence context is required")
	}
	now := normalizedNow(input.Now)
	proof := input.Proof
	if proof.Disposition != BoardPublicationClaimFenceProvenUncommitted || proof.Scope != input.Lease.Scope ||
		!validBoardPublicationID(proof.PriorClaimID) || !validBoardPublicationID(proof.FreshClaimID) ||
		!validBoardPublicationDigest(proof.FreshReceiptSHA256) || !proof.FreshLeaseUntil.After(now) {
		return BoardPublicationClaimFence{}, ErrBoardPublicationAttemptConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BoardPublicationClaimFence{}, err
	}
	defer tx.Rollback()
	lease, _, _, _, err := loadCurrentBoardPublicationLeaseTx(ctx, tx, input.Lease, now)
	if err != nil {
		return BoardPublicationClaimFence{}, err
	}
	if normalizedBoardPublicationTargetKind(lease.TargetKind) == boardPublicationTargetNewRoot {
		return BoardPublicationClaimFence{}, ErrBoardPublicationAttemptConflict
	}
	if exists, err := boardPublicationOutcomeExistsTx(ctx, tx, lease.WorkID); err != nil {
		return BoardPublicationClaimFence{}, err
	} else if exists {
		return BoardPublicationClaimFence{}, ErrBoardPublicationAttemptConflict
	}
	attempts, err := loadBoardPublicationAttemptsTx(ctx, tx, lease.WorkID)
	if err != nil {
		return BoardPublicationClaimFence{}, err
	}
	if len(attempts) == 0 || attempts[len(attempts)-1].Ordinal != input.AttemptOrdinal ||
		attempts[len(attempts)-1].PayloadSHA256 != input.AttemptSHA256 ||
		attempts[len(attempts)-1].ProcessingClaimID != proof.PriorClaimID {
		return BoardPublicationClaimFence{}, ErrBoardPublicationAttemptConflict
	}
	prior := attempts[len(attempts)-1]
	if prior.ProcessingClaimReceiptSHA256 == proof.FreshReceiptSHA256 {
		return BoardPublicationClaimFence{}, ErrBoardPublicationAttemptConflict
	}
	if existing, found, err := loadBoardPublicationFenceTx(ctx, tx, lease.WorkID, prior.Ordinal, proof.FreshReceiptSHA256); err != nil {
		return BoardPublicationClaimFence{}, err
	} else if found {
		if existing.AttemptSHA256 != prior.PayloadSHA256 || existing.FreshClaimID != proof.FreshClaimID ||
			!existing.FreshLeaseUntil.Equal(proof.FreshLeaseUntil) || existing.LeaseVersion > lease.Version {
			return BoardPublicationClaimFence{}, ErrBoardPublicationAttemptConflict
		}
		if err := tx.Commit(); err != nil {
			return BoardPublicationClaimFence{}, err
		}
		return existing, nil
	}
	fence := BoardPublicationClaimFence{
		WorkID: lease.WorkID, AttemptOrdinal: prior.Ordinal, AttemptSHA256: prior.PayloadSHA256,
		LeaseVersion: lease.Version, FreshClaimID: proof.FreshClaimID, FreshReceiptSHA256: proof.FreshReceiptSHA256,
		FreshLeaseUntil: proof.FreshLeaseUntil,
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO live_board_publication_claim_fence
			(work_id,prior_attempt_ordinal,prior_attempt_sha256,lease_version,prior_claim_id,fresh_claim_id,fresh_receipt_sha256,fresh_lease_until,created_at)
		VALUES (?,?,?,?,?,?,?,?,?)`, lease.WorkID, prior.Ordinal, prior.PayloadSHA256, lease.Version,
		proof.PriorClaimID, proof.FreshClaimID, proof.FreshReceiptSHA256, formatWorkTime(proof.FreshLeaseUntil), formatWorkTime(now)); err != nil {
		return BoardPublicationClaimFence{}, fmt.Errorf("record proven BOARD claim fence: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return BoardPublicationClaimFence{}, err
	}
	return fence, nil
}

// CompleteBoardPublication atomically records the canonical BOARD tuple and
// completes the currently leased outbox work item.
func (s *SQLiteStore) CompleteBoardPublication(ctx context.Context, input BoardPublicationCompletionInput) (BoardPublicationOutcome, error) {
	if err := s.ensureOpen(); err != nil {
		return BoardPublicationOutcome{}, err
	}
	if ctx == nil {
		return BoardPublicationOutcome{}, errors.New("BOARD publication completion context is required")
	}
	now := normalizedNow(input.Now)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BoardPublicationOutcome{}, err
	}
	defer tx.Rollback()
	work, state, err := readWorkForUpdate(ctx, tx, input.Lease.WorkID)
	if err != nil {
		return BoardPublicationOutcome{}, err
	}
	if state == "completed" {
		if work.LeaseToken != input.Lease.LeaseToken || work.Version != input.Lease.Version {
			return BoardPublicationOutcome{}, ErrWorkConflict
		}
		if _, _, _, _, err := loadBoardPublicationSourceTx(ctx, tx, work.WorkID, input.Lease.Scope); err != nil {
			return BoardPublicationOutcome{}, err
		}
		outcome, found, err := loadBoardPublicationOutcomeByWorkTx(ctx, tx, work.WorkID)
		if err != nil || !found {
			if err != nil {
				return BoardPublicationOutcome{}, err
			}
			return BoardPublicationOutcome{}, ErrBoardPublicationOutcomeNotFound
		}
		if !boardPublicationOutcomeMatchesInput(outcome, input) {
			return BoardPublicationOutcome{}, ErrBoardPublicationAttemptConflict
		}
		if err := tx.Commit(); err != nil {
			return BoardPublicationOutcome{}, err
		}
		return outcome, nil
	}
	lease, bundle, _, _, err := loadCurrentBoardPublicationLeaseTx(ctx, tx, input.Lease, now)
	if err != nil {
		return BoardPublicationOutcome{}, err
	}
	if input.AttemptOrdinal < 1 || !validBoardPublicationDigest(input.AttemptSHA256) {
		return BoardPublicationOutcome{}, ErrBoardPublicationAttemptConflict
	}
	attempts, err := loadBoardPublicationAttemptsTx(ctx, tx, lease.WorkID)
	if err != nil {
		return BoardPublicationOutcome{}, err
	}
	if input.AttemptOrdinal > len(attempts) || attempts[input.AttemptOrdinal-1].Ordinal != input.AttemptOrdinal ||
		attempts[input.AttemptOrdinal-1].PayloadSHA256 != input.AttemptSHA256 {
		return BoardPublicationOutcome{}, ErrBoardPublicationAttemptConflict
	}
	if err := validateBoardPublicationCompletion(lease, bundle, input); err != nil {
		return BoardPublicationOutcome{}, err
	}
	canonical := input.CanonicalMessage
	recipientsJSON, err := json.Marshal(canonical.Recipients)
	if err != nil {
		return BoardPublicationOutcome{}, err
	}
	outcome := BoardPublicationOutcome{
		Scope: lease.Scope, WorkID: lease.WorkID, LogicalKey: lease.LogicalKey,
		AttemptOrdinal: input.AttemptOrdinal, PayloadSHA256: input.AttemptSHA256,
		SendReceipt: input.SendReceipt, CanonicalMessage: canonical,
		ExpectedPeerPrincipalID: lease.PublicationIntent.Target.PeerPrincipalID,
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO live_board_publication_outcome
			(work_id,owner_id,journal_instance_id,principal_id,logical_key,attempt_ordinal,payload_sha256,
			 board_message_id,board_thread_id,stored_at,status,channel_id,sender_principal_id,recipients_json,
			 reply_to,causation_id,hop_count,expected_peer_principal_id)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		outcome.WorkID, outcome.Scope.OwnerID, outcome.Scope.JournalInstanceID, outcome.Scope.PrincipalID,
		outcome.LogicalKey, outcome.AttemptOrdinal, outcome.PayloadSHA256,
		outcome.CanonicalMessage.MessageID, outcome.CanonicalMessage.ThreadID, formatWorkTime(outcome.SendReceipt.StoredAt),
		outcome.SendReceipt.Status, outcome.CanonicalMessage.ChannelID, outcome.CanonicalMessage.SenderPrincipalID,
		string(recipientsJSON), outcome.CanonicalMessage.ReplyTo, outcome.CanonicalMessage.CausationID,
		outcome.CanonicalMessage.HopCount, outcome.ExpectedPeerPrincipalID); err != nil {
		return BoardPublicationOutcome{}, fmt.Errorf("insert BOARD publication outcome: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE live_work_outbox SET state='completed'
		WHERE work_id=? AND kind='board_publication' AND state='leased' AND lease_token=? AND version=?`,
		lease.WorkID, lease.LeaseToken, lease.Version)
	if err != nil {
		return BoardPublicationOutcome{}, err
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		return BoardPublicationOutcome{}, ErrWorkConflict
	}
	if err := tx.Commit(); err != nil {
		return BoardPublicationOutcome{}, err
	}
	return outcome, nil
}

// LoadBoardPublicationByParentMessage resolves the saved send that a later
// inbound BOARD reply names as its parent.
func (s *SQLiteStore) LoadBoardPublicationByParentMessage(ctx context.Context, input BoardPublicationLookupInput) (BoardPublicationOutcome, bool, error) {
	if err := s.ensureOpen(); err != nil {
		return BoardPublicationOutcome{}, false, err
	}
	if ctx == nil {
		return BoardPublicationOutcome{}, false, errors.New("BOARD publication lookup context is required")
	}
	if err := validateBoardPublicationScope(input.Scope); err != nil || !validBoardPublicationID(input.ParentMessageID) {
		if err != nil {
			return BoardPublicationOutcome{}, false, err
		}
		return BoardPublicationOutcome{}, false, ErrBoardPublicationOutcomeNotFound
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return BoardPublicationOutcome{}, false, err
	}
	defer tx.Rollback()
	if err := requireBoardPublicationPinCandidateTx(ctx, tx, input.Scope); err != nil {
		return BoardPublicationOutcome{}, false, err
	}
	var workID string
	err = tx.QueryRowContext(ctx, `SELECT work_id FROM live_board_publication_outcome WHERE board_message_id=?`, input.ParentMessageID).Scan(&workID)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return BoardPublicationOutcome{}, false, err
		}
		return BoardPublicationOutcome{}, false, nil
	}
	if err != nil {
		return BoardPublicationOutcome{}, false, err
	}
	if _, _, _, _, err := loadBoardPublicationSourceTx(ctx, tx, workID, input.Scope); err != nil {
		return BoardPublicationOutcome{}, false, err
	}
	outcome, found, err := loadBoardPublicationOutcomeByWorkTx(ctx, tx, workID)
	if err != nil {
		return BoardPublicationOutcome{}, false, err
	}
	if !found {
		return BoardPublicationOutcome{}, false, ErrBoardPublicationOutcomeNotFound
	}
	if outcome.Scope != input.Scope || outcome.CanonicalMessage.MessageID != input.ParentMessageID {
		return BoardPublicationOutcome{}, false, ErrBoardPublicationScopeMismatch
	}
	if err := tx.Commit(); err != nil {
		return BoardPublicationOutcome{}, false, err
	}
	return outcome, true, nil
}

// RetryBoardPublication schedules another specialized lease without changing
// the immutable exact attempt or treating an unknown send as uncommitted.
func (s *SQLiteStore) RetryBoardPublication(ctx context.Context, input BoardPublicationRetryInput) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if ctx == nil {
		return errors.New("BOARD publication retry context is required")
	}
	now := normalizedNow(input.Now)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	lease, _, work, _, err := loadCurrentBoardPublicationLeaseTx(ctx, tx, input.Lease, now)
	if err != nil {
		return err
	}
	if exists, err := boardPublicationOutcomeExistsTx(ctx, tx, lease.WorkID); err != nil {
		return err
	} else if exists {
		return ErrWorkConflict
	}
	delays := [...]time.Duration{time.Second, 5 * time.Second, 30 * time.Second}
	delay := delays[len(delays)-1]
	if work.Attempt > 0 && work.Attempt <= len(delays) {
		delay = delays[work.Attempt-1]
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE live_work_outbox SET state='pending',not_before=?,lease_token=NULL,lease_expires_at=NULL,version=version+1
		WHERE work_id=? AND kind='board_publication' AND state='leased' AND lease_token=? AND version=?`,
		formatWorkTime(now.Add(delay)), lease.WorkID, lease.LeaseToken, lease.Version)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		return ErrWorkConflict
	}
	return tx.Commit()
}

func validateBoardPublicationScope(scope BoardPublicationScope) error {
	if strings.TrimSpace(scope.OwnerID) == "" || scope.OwnerID != strings.TrimSpace(scope.OwnerID) ||
		!validBoardConnectionJournal(scope.JournalInstanceID) || !validBoardConnectionPrincipal(scope.PrincipalID) {
		return ErrBoardPublicationScopeMismatch
	}
	return nil
}

func requireBoardPublicationPinTx(ctx context.Context, tx *sql.Tx, scope BoardPublicationScope) (BoardConnectionPin, error) {
	if err := validateBoardPublicationScope(scope); err != nil {
		return BoardConnectionPin{}, err
	}
	pin, found, err := readBoardConnectionPinTx(ctx, tx)
	if err != nil {
		return BoardConnectionPin{}, err
	}
	if !found || pin.OwnerID != scope.OwnerID || pin.JournalInstanceID != scope.JournalInstanceID || pin.PrincipalID != scope.PrincipalID {
		return BoardConnectionPin{}, ErrBoardPublicationScopeMismatch
	}
	return pin, nil
}

// requireBoardPublicationPinCandidateTx is a coarse preflight for APIs that
// must reject a wrong scope before discovering work. A selected peer claim is
// still checked against its typed responder binding in loadBoardPublicationSourceTx.
func requireBoardPublicationPinCandidateTx(ctx context.Context, tx *sql.Tx, scope BoardPublicationScope) error {
	if err := validateBoardPublicationScope(scope); err != nil {
		return err
	}
	pin, found, err := readBoardConnectionPinTx(ctx, tx)
	if err != nil {
		return err
	}
	if found && boardConnectionPinMatchesPublicationScope(pin, scope) {
		return nil
	}
	pin, found, err = readBoardConnectionPinForPrincipalTx(ctx, tx, scope.PrincipalID)
	if err != nil {
		return err
	}
	if found && boardConnectionPinMatchesPublicationScope(pin, scope) {
		return nil
	}
	return ErrBoardPublicationScopeMismatch
}

func requireBoardPublicationPinForResponderTx(ctx context.Context, tx *sql.Tx, scope BoardPublicationScope, responderActorID string) error {
	if err := validateBoardPublicationScope(scope); err != nil {
		return err
	}
	if responderActorID == "" {
		_, err := requireBoardPublicationPinTx(ctx, tx, scope)
		return err
	}
	if responderActorID == "mio" {
		_, err := requireBoardPublicationPinTx(ctx, tx, scope)
		return err
	}
	if !domainconversation.IsExternalPeerResponderActor(responderActorID) || responderActorID != scope.PrincipalID {
		return ErrBoardPublicationScopeMismatch
	}
	pin, found, err := readBoardConnectionPinForPrincipalTx(ctx, tx, scope.PrincipalID)
	if err != nil {
		return err
	}
	if !found || !boardConnectionPinMatchesPublicationScope(pin, scope) {
		return ErrBoardPublicationScopeMismatch
	}
	return nil
}

func boardPublicationActorMatchesResponder(storedActorID, responderActorID string) bool {
	return responderActorID == "" || storedActorID == responderActorID
}

func requireBoardPublicationPeerPinTx(ctx context.Context, tx *sql.Tx, scope BoardPublicationScope, binding domainconversation.ExternalPeerBinding) (BoardConnectionPin, error) {
	if err := validateBoardPublicationScope(scope); err != nil {
		return BoardConnectionPin{}, err
	}
	if binding.BoardInstanceID != scope.JournalInstanceID || binding.RecipientPrincipalID != scope.PrincipalID {
		return BoardConnectionPin{}, ErrBoardPublicationScopeMismatch
	}
	pin, found, err := readBoardPeerConnectionPinTx(ctx, tx, scope, binding.ResponderActorID)
	if err != nil {
		if errors.Is(err, ErrBoardPeerIngressScopeMismatch) {
			return BoardConnectionPin{}, ErrBoardPublicationScopeMismatch
		}
		return BoardConnectionPin{}, err
	}
	if !found || !boardConnectionPinMatchesPublicationScope(pin, scope) {
		return BoardConnectionPin{}, ErrBoardPublicationScopeMismatch
	}
	return pin, nil
}

func boardConnectionPinMatchesPublicationScope(pin BoardConnectionPin, scope BoardPublicationScope) bool {
	return pin.OwnerID == scope.OwnerID && pin.JournalInstanceID == scope.JournalInstanceID && pin.PrincipalID == scope.PrincipalID
}

func loadBoardPublicationSourceTx(ctx context.Context, tx *sql.Tx, workID string, scope BoardPublicationScope) (BoardPublicationLease, conversationruntime.ExternalCommittedBundle, LeasedWorkItem, string, error) {
	work, state, err := readWorkForUpdate(ctx, tx, workID)
	if err != nil {
		return BoardPublicationLease{}, conversationruntime.ExternalCommittedBundle{}, LeasedWorkItem{}, "", err
	}
	var responseMessageID, boardInstanceID, corePrincipalID, peerPrincipalID, channelID, targetKind, title, threadID, replyTo string
	err = tx.QueryRowContext(ctx, `
		SELECT response_message_id,board_instance_id,core_principal_id,peer_principal_id,channel_id,target_kind,title,thread_id,reply_to
		FROM live_board_publication WHERE work_id=?`, workID).Scan(
		&responseMessageID, &boardInstanceID, &corePrincipalID, &peerPrincipalID, &channelID, &targetKind, &title, &threadID, &replyTo)
	if err != nil {
		return BoardPublicationLease{}, conversationruntime.ExternalCommittedBundle{}, LeasedWorkItem{}, "", err
	}
	if targetKind == "" {
		targetKind = boardPublicationTargetReply
	}
	if work.Kind != "board_publication" || corePrincipalID != scope.PrincipalID {
		return BoardPublicationLease{}, conversationruntime.ExternalCommittedBundle{}, LeasedWorkItem{}, "", ErrBoardPublicationScopeMismatch
	}
	var bundle conversationruntime.ExternalCommittedBundle
	if targetKind == boardPublicationTargetNewRoot {
		if !strings.HasPrefix(work.DedupeKey, "board-owner-request-publication:") || threadID != "" || replyTo != "" || strings.TrimSpace(title) == "" {
			return BoardPublicationLease{}, conversationruntime.ExternalCommittedBundle{}, LeasedWorkItem{}, "", ErrBoardPublicationScopeMismatch
		}
		operationID := strings.TrimPrefix(work.DedupeKey, "board-owner-request-publication:")
		operationReceipt, found, err := readOperationReceipt(ctx, tx, operationID)
		if err != nil {
			return BoardPublicationLease{}, conversationruntime.ExternalCommittedBundle{}, LeasedWorkItem{}, "", err
		}
		if !found || operationReceipt.operationKind != boardOwnerRequestCommitOperationKind {
			return BoardPublicationLease{}, conversationruntime.ExternalCommittedBundle{}, LeasedWorkItem{}, "", ErrBoardOwnerRequestCorrupt
		}
		var commitReceipt boardOwnerRequestCommitReceipt
		decoder := json.NewDecoder(strings.NewReader(operationReceipt.resultJSON))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&commitReceipt); err != nil || commitReceipt.WorkID != workID || commitReceipt.Lookup.OperationID != operationID ||
			commitReceipt.Lookup.OwnerID != scope.OwnerID || commitReceipt.Lookup.Source.BoardInstanceID != boardInstanceID ||
			commitReceipt.Lookup.Source.CorePrincipalID != scope.PrincipalID {
			return BoardPublicationLease{}, conversationruntime.ExternalCommittedBundle{}, LeasedWorkItem{}, "", ErrBoardOwnerRequestCorrupt
		}
		ownerSource := commitReceipt.Lookup.Source
		if !validBoardOwnerRequestSource(ownerSource, true) || ownerSource.OwnerID != scope.OwnerID ||
			ownerSource.BoardInstanceID != scope.JournalInstanceID || ownerSource.CorePrincipalID != scope.PrincipalID {
			return BoardPublicationLease{}, conversationruntime.ExternalCommittedBundle{}, LeasedWorkItem{}, "", ErrBoardPublicationScopeMismatch
		}
		if _, err := requireBoardOwnerRequestPinTx(ctx, tx, ownerSource); err != nil {
			return BoardPublicationLease{}, conversationruntime.ExternalCommittedBundle{}, LeasedWorkItem{}, "", fmt.Errorf("%w: %w", ErrBoardPublicationScopeMismatch, err)
		}
		outcome, found, err := loadBoardOwnerRequestOutcomeTx(ctx, tx, commitReceipt.Lookup)
		if err != nil {
			return BoardPublicationLease{}, conversationruntime.ExternalCommittedBundle{}, LeasedWorkItem{}, "", err
		}
		if !found || outcome.Event.MessageID != modulecore.MessageID(responseMessageID) ||
			outcome.Target != (conversationruntime.NewBoardThreadTarget{
				BoardInstanceID: boardInstanceID, CorePrincipalID: corePrincipalID, PeerPrincipalID: peerPrincipalID,
				ChannelID: channelID, Title: title,
			}) || outcome.Event.EventID != modulecore.EventID(work.SourceEventID) || work.SessionID != outcome.Event.SessionID {
			return BoardPublicationLease{}, conversationruntime.ExternalCommittedBundle{}, LeasedWorkItem{}, "", ErrBoardOwnerRequestCorrupt
		}
		bundle = conversationruntime.ExternalCommittedBundle{
			Lookup: conversationruntime.ExternalTurnLookup{}, ResultState: "completed",
			CanonicalEvents: []modulecore.EventEnvelope{outcome.Event},
			MessageIndexes:  []conversationruntime.MessageIndex{outcome.MessageIndex}, InvocationReceipt: outcome.InvocationReceipt,
			PublicationIntent: conversationruntime.ExternalPublicationIntent{
				CanonicalResponseMessageID: outcome.Event.MessageID,
				Target: conversationruntime.ExternalReplyTarget{
					BoardInstanceID: boardInstanceID, CorePrincipalID: corePrincipalID, PeerPrincipalID: peerPrincipalID,
					ChannelID: channelID,
				}, StableLogicalKey: outcome.IdempotencyKey,
			},
		}
	} else {
		if targetKind != boardPublicationTargetReply || title != "" || !strings.HasPrefix(work.DedupeKey, "external-publication:") {
			return BoardPublicationLease{}, conversationruntime.ExternalCommittedBundle{}, LeasedWorkItem{}, "", ErrBoardPublicationScopeMismatch
		}
		operationID := strings.TrimPrefix(work.DedupeKey, "external-publication:")
		operationReceipt, found, err := readOperationReceipt(ctx, tx, operationID)
		if err != nil {
			return BoardPublicationLease{}, conversationruntime.ExternalCommittedBundle{}, LeasedWorkItem{}, "", err
		}
		if !found || operationReceipt.operationKind != externalTurnCommitOperationKind {
			return BoardPublicationLease{}, conversationruntime.ExternalCommittedBundle{}, LeasedWorkItem{}, "", fmt.Errorf("%w: BOARD work has no typed external commit receipt", errExternalTurnOutcomeCorrupt)
		}
		var commitReceipt externalCommitReceipt
		if err := json.Unmarshal([]byte(operationReceipt.resultJSON), &commitReceipt); err != nil || commitReceipt.WorkID != workID ||
			commitReceipt.Lookup.OperationID != operationID || commitReceipt.Lookup.OwnerID != scope.OwnerID ||
			commitReceipt.Lookup.SourceBinding.RecipientPrincipalID != scope.PrincipalID {
			return BoardPublicationLease{}, conversationruntime.ExternalCommittedBundle{}, LeasedWorkItem{}, "", fmt.Errorf("%w: BOARD work does not match its typed external receipt", errExternalTurnOutcomeCorrupt)
		}
		bundle, found, err = loadExternalCommittedOutcomeTx(ctx, tx, commitReceipt.Lookup)
		if err != nil {
			return BoardPublicationLease{}, conversationruntime.ExternalCommittedBundle{}, LeasedWorkItem{}, "", err
		}
		if _, err := requireBoardPublicationPeerPinTx(ctx, tx, scope, bundle.Lookup.SourceBinding); err != nil {
			return BoardPublicationLease{}, conversationruntime.ExternalCommittedBundle{}, LeasedWorkItem{}, "", err
		}
		if !found || bundle.PublicationIntent.Target != (conversationruntime.ExternalReplyTarget{
			BoardInstanceID: boardInstanceID, CorePrincipalID: corePrincipalID, PeerPrincipalID: peerPrincipalID,
			ChannelID: channelID, ThreadID: threadID, ReplyTo: replyTo,
		}) || bundle.PublicationIntent.CanonicalResponseMessageID != modulecore.MessageID(responseMessageID) {
			return BoardPublicationLease{}, conversationruntime.ExternalCommittedBundle{}, LeasedWorkItem{}, "", fmt.Errorf("%w: BOARD work target differs from its canonical external result", errExternalTurnOutcomeCorrupt)
		}
		if bundle.Lookup.OwnerID != scope.OwnerID || bundle.Lookup.SourceBinding.RecipientPrincipalID != scope.PrincipalID ||
			work.SessionID != bundle.Lookup.SessionID || work.SourceEventID == "" {
			return BoardPublicationLease{}, conversationruntime.ExternalCommittedBundle{}, LeasedWorkItem{}, "", ErrBoardPublicationScopeMismatch
		}
	}
	var response *modulecore.EventEnvelope
	var responseIndex *MessageIndex
	for index := range bundle.CanonicalEvents {
		if bundle.CanonicalEvents[index].MessageID == bundle.PublicationIntent.CanonicalResponseMessageID {
			response = &bundle.CanonicalEvents[index]
			break
		}
	}
	for index := range bundle.MessageIndexes {
		if bundle.MessageIndexes[index].MessageID == bundle.PublicationIntent.CanonicalResponseMessageID {
			converted := eventStoreMessageIndex(bundle.MessageIndexes[index])
			responseIndex = &converted
			break
		}
	}
	if response == nil || responseIndex == nil || response.EventID != work.SourceEventID || response.SessionID != work.SessionID ||
		responseIndex.EventID != response.EventID || responseIndex.Visibility != "public" ||
		payloadString(response.Payload, "text") == "" {
		return BoardPublicationLease{}, conversationruntime.ExternalCommittedBundle{}, LeasedWorkItem{}, "", fmt.Errorf("%w: BOARD work source is not the canonical public response", errExternalTurnOutcomeCorrupt)
	}
	lease := BoardPublicationLease{
		Scope: scope, WorkID: work.WorkID, LogicalKey: bundle.PublicationIntent.StableLogicalKey,
		LeaseToken: work.LeaseToken, LeaseExpiresAt: work.LeaseExpiresAt, Version: work.Version, QueueAttempt: work.Attempt,
		TargetKind: targetKind, Title: title,
		CanonicalResponse: *response, ResponseIndex: *responseIndex, PublicationIntent: bundle.PublicationIntent,
	}
	lease.Attempts, err = loadBoardPublicationAttemptsTx(ctx, tx, workID)
	if err != nil {
		return BoardPublicationLease{}, conversationruntime.ExternalCommittedBundle{}, LeasedWorkItem{}, "", err
	}
	lease.Fences, err = loadBoardPublicationFencesTx(ctx, tx, workID)
	if err != nil {
		return BoardPublicationLease{}, conversationruntime.ExternalCommittedBundle{}, LeasedWorkItem{}, "", err
	}
	return lease, bundle, work, state, nil
}

func loadCurrentBoardPublicationLeaseTx(ctx context.Context, tx *sql.Tx, supplied BoardPublicationLease, now time.Time) (BoardPublicationLease, conversationruntime.ExternalCommittedBundle, LeasedWorkItem, string, error) {
	if err := validateBoardPublicationScope(supplied.Scope); err != nil || supplied.WorkID == "" || supplied.LeaseToken == "" || supplied.Version < 1 {
		if err != nil {
			return BoardPublicationLease{}, conversationruntime.ExternalCommittedBundle{}, LeasedWorkItem{}, "", err
		}
		return BoardPublicationLease{}, conversationruntime.ExternalCommittedBundle{}, LeasedWorkItem{}, "", ErrWorkConflict
	}
	lease, bundle, work, state, err := loadBoardPublicationSourceTx(ctx, tx, supplied.WorkID, supplied.Scope)
	if err != nil {
		return BoardPublicationLease{}, conversationruntime.ExternalCommittedBundle{}, LeasedWorkItem{}, "", err
	}
	if state != "leased" || work.LeaseToken != supplied.LeaseToken || work.Version != supplied.Version ||
		!work.LeaseExpiresAt.Equal(supplied.LeaseExpiresAt) {
		return BoardPublicationLease{}, conversationruntime.ExternalCommittedBundle{}, LeasedWorkItem{}, "", ErrWorkConflict
	}
	if _, err := validateWorkLease(work, state, WorkLeaseInput{
		WorkID: supplied.WorkID, LeaseToken: supplied.LeaseToken, Version: supplied.Version, Now: now,
	}, now); err != nil {
		return BoardPublicationLease{}, conversationruntime.ExternalCommittedBundle{}, LeasedWorkItem{}, "", err
	}
	return lease, bundle, work, state, nil
}

func loadBoardPublicationAttemptsTx(ctx context.Context, tx *sql.Tx, workID string) ([]BoardPublicationAttempt, error) {
	var targetKind, title, channelID, peerPrincipalID, threadID, replyTo string
	if err := tx.QueryRowContext(ctx, `
		SELECT target_kind,title,channel_id,peer_principal_id,thread_id,reply_to
		FROM live_board_publication WHERE work_id=?`, workID).
		Scan(&targetKind, &title, &channelID, &peerPrincipalID, &threadID, &replyTo); err != nil {
		return nil, err
	}
	targetKind = normalizedBoardPublicationTargetKind(targetKind)
	rows, err := tx.QueryContext(ctx, `
		SELECT attempt_ordinal,logical_key,prepared_bytes,payload_sha256,processing_claim_id,
		       processing_claim_receipt_sha256,processing_claim_lease_until
		FROM live_board_publication_attempt WHERE work_id=? ORDER BY attempt_ordinal`, workID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	attempts := make([]BoardPublicationAttempt, 0)
	for rows.Next() {
		var attempt BoardPublicationAttempt
		var leaseUntil string
		if err := rows.Scan(&attempt.Ordinal, &attempt.LogicalKey, &attempt.PreparedBytes, &attempt.PayloadSHA256,
			&attempt.ProcessingClaimID, &attempt.ProcessingClaimReceiptSHA256, &leaseUntil); err != nil {
			return nil, err
		}
		attempt.WorkID = workID
		var err error
		attempt.ProcessingClaimLeaseUntil, err = parseWorkTime(leaseUntil)
		if err != nil {
			return nil, err
		}
		wire, err := decodeBoardPublicationPreparedWire(attempt.PreparedBytes)
		if err != nil || !validBoardPublicationDigest(attempt.PayloadSHA256) || boardPublicationDigest(attempt.PreparedBytes) != attempt.PayloadSHA256 ||
			wire.IdempotencyKey != attempt.LogicalKey || attempt.Ordinal != len(attempts)+1 || attempt.LogicalKey == "" {
			return nil, fmt.Errorf("%w: immutable BOARD publication attempt is corrupt", ErrBoardPublicationAttemptConflict)
		}
		if targetKind == boardPublicationTargetNewRoot {
			if title == "" || strings.TrimSpace(title) == "" || threadID != "" || replyTo != "" ||
				wire.ChannelID != channelID || wire.ThreadID != "" || wire.Title != title || wire.ReplyTo != "" || wire.Kind != "request" ||
				!reflect.DeepEqual(wire.Recipients, []string{peerPrincipalID}) || wire.ProcessingClaimID != "" || wire.ProcessingClaimReceipt != "" ||
				attempt.ProcessingClaimID != "" || attempt.ProcessingClaimReceiptSHA256 != boardPublicationDigest(nil) ||
				!attempt.ProcessingClaimLeaseUntil.IsZero() || boardPublicationAttemptHasReplyOrClaimFields(attempt.PreparedBytes) {
				return nil, fmt.Errorf("%w: immutable BOARD new-root attempt is corrupt", ErrBoardPublicationAttemptConflict)
			}
		} else if targetKind == boardPublicationTargetReply {
			if wire.ChannelID != channelID || threadID == "" || wire.ThreadID != threadID || wire.Title != "" || replyTo == "" ||
				wire.ReplyTo != replyTo || wire.Kind != "reply" || wire.ProcessingClaimID != attempt.ProcessingClaimID ||
				!validBoardPublicationID(attempt.ProcessingClaimID) || !validBoardPublicationOpaque(wire.ProcessingClaimReceipt) ||
				boardPublicationDigest([]byte(wire.ProcessingClaimReceipt)) != attempt.ProcessingClaimReceiptSHA256 ||
				attempt.ProcessingClaimLeaseUntil.IsZero() {
				return nil, fmt.Errorf("%w: immutable BOARD reply attempt is corrupt", ErrBoardPublicationAttemptConflict)
			}
		} else {
			return nil, fmt.Errorf("%w: immutable BOARD target kind is corrupt", ErrBoardPublicationAttemptConflict)
		}
		attempt.PreparedBytes = append([]byte(nil), attempt.PreparedBytes...)
		attempts = append(attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return attempts, nil
}

func loadBoardPublicationFencesTx(ctx context.Context, tx *sql.Tx, workID string) ([]BoardPublicationClaimFence, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT prior_attempt_ordinal,prior_attempt_sha256,lease_version,fresh_claim_id,fresh_receipt_sha256,fresh_lease_until
		FROM live_board_publication_claim_fence WHERE work_id=? ORDER BY prior_attempt_ordinal,created_at,fresh_receipt_sha256`, workID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	fences := make([]BoardPublicationClaimFence, 0)
	for rows.Next() {
		var fence BoardPublicationClaimFence
		var freshUntil string
		if err := rows.Scan(&fence.AttemptOrdinal, &fence.AttemptSHA256, &fence.LeaseVersion, &fence.FreshClaimID, &fence.FreshReceiptSHA256, &freshUntil); err != nil {
			return nil, err
		}
		fence.WorkID = workID
		var err error
		fence.FreshLeaseUntil, err = parseWorkTime(freshUntil)
		if err != nil || !validBoardPublicationDigest(fence.AttemptSHA256) || !validBoardPublicationDigest(fence.FreshReceiptSHA256) || fence.FreshClaimID == "" || fence.LeaseVersion < 1 {
			return nil, fmt.Errorf("%w: BOARD publication claim fence is corrupt", ErrBoardPublicationAttemptConflict)
		}
		fences = append(fences, fence)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return fences, nil
}

func validateBoardPublicationPrepared(lease BoardPublicationLease, input BoardPublicationAttemptInput) (struct {
	ProcessingClaimID string
	ReceiptSHA256     string
}, error) {
	if input.LogicalKey == "" || input.LogicalKey != lease.LogicalKey || len(input.PreparedBytes) == 0 || len(input.PreparedBytes) > 1<<20 ||
		!validBoardPublicationDigest(input.PayloadSHA256) || boardPublicationDigest(input.PreparedBytes) != input.PayloadSHA256 {
		return struct {
			ProcessingClaimID string
			ReceiptSHA256     string
		}{}, ErrBoardPublicationAttemptConflict
	}
	wire, err := decodeBoardPublicationPreparedWire(input.PreparedBytes)
	if err != nil {
		return struct {
			ProcessingClaimID string
			ReceiptSHA256     string
		}{}, ErrBoardPublicationAttemptConflict
	}
	target := lease.PublicationIntent.Target
	targetKind := normalizedBoardPublicationTargetKind(lease.TargetKind)
	if wire.ChannelID != target.ChannelID || wire.Body != payloadString(lease.CanonicalResponse.Payload, "text") ||
		len(wire.Recipients) != 1 || wire.Recipients[0] != target.PeerPrincipalID || wire.ReplyTo != target.ReplyTo || wire.IdempotencyKey != lease.LogicalKey {
		return struct {
			ProcessingClaimID string
			ReceiptSHA256     string
		}{}, ErrBoardPublicationAttemptConflict
	}
	switch targetKind {
	case boardPublicationTargetReply:
		if target.ThreadID == "" || target.ReplyTo == "" || wire.ThreadID != target.ThreadID || wire.Title != "" || wire.Kind != "reply" ||
			input.ProcessingClaimLeaseUntil.IsZero() || !validBoardPublicationID(wire.ProcessingClaimID) ||
			!validBoardPublicationOpaque(wire.ProcessingClaimReceipt) {
			return struct {
				ProcessingClaimID string
				ReceiptSHA256     string
			}{}, ErrBoardPublicationAttemptConflict
		}
	case boardPublicationTargetNewRoot:
		if target.ThreadID != "" || target.ReplyTo != "" || strings.TrimSpace(lease.Title) == "" || wire.ThreadID != "" ||
			wire.Title != lease.Title || wire.Kind != "request" || wire.ProcessingClaimID != "" || wire.ProcessingClaimReceipt != "" ||
			!input.ProcessingClaimLeaseUntil.IsZero() || boardPublicationAttemptHasReplyOrClaimFields(input.PreparedBytes) ||
			!boardPublicationPreparedMentionsMatch(input.PreparedBytes, lease.CanonicalResponse.Payload, target.PeerPrincipalID) {
			return struct {
				ProcessingClaimID string
				ReceiptSHA256     string
			}{}, ErrBoardPublicationAttemptConflict
		}
	default:
		return struct {
			ProcessingClaimID string
			ReceiptSHA256     string
		}{}, ErrBoardPublicationAttemptConflict
	}
	return struct {
		ProcessingClaimID string
		ReceiptSHA256     string
	}{ProcessingClaimID: wire.ProcessingClaimID, ReceiptSHA256: boardPublicationDigest([]byte(wire.ProcessingClaimReceipt))}, nil
}

func boardPublicationPreparedMentionsMatch(payload []byte, canonicalPayload map[string]any, peerPrincipalID string) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return false
	}
	expected, explicit := canonicalPayload["mentions"]
	rawMentions, supplied := fields["mentions"]
	if !explicit {
		return !supplied
	}
	if !boardOwnerRequestMentionsMatch(expected, peerPrincipalID) || !supplied {
		return false
	}
	var mentions []string
	if err := json.Unmarshal(rawMentions, &mentions); err != nil {
		return false
	}
	return len(mentions) == 1 && mentions[0] == peerPrincipalID
}

func normalizedBoardPublicationTargetKind(targetKind string) string {
	if targetKind == "" {
		return boardPublicationTargetReply
	}
	return targetKind
}

func boardPublicationAttemptHasReplyOrClaimFields(payload []byte) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return true
	}
	for _, name := range []string{"thread_id", "reply_to", "processing_claim_id", "processing_claim_receipt"} {
		if _, exists := fields[name]; exists {
			return true
		}
	}
	return false
}

func validBoardPublicationOpaque(value string) bool {
	if value == "" || len(value) > 4096 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range []byte(value) {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func insertBoardPublicationAttemptTx(ctx context.Context, tx *sql.Tx, workID string, ordinal int, input BoardPublicationAttemptInput, prepared struct {
	ProcessingClaimID string
	ReceiptSHA256     string
}, priorOrdinal int, fenceReceiptSHA256 string) (BoardPublicationAttempt, error) {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO live_board_publication_attempt
			(work_id,attempt_ordinal,logical_key,prepared_bytes,payload_sha256,processing_claim_id,
			 processing_claim_receipt_sha256,processing_claim_lease_until,prior_attempt_ordinal,fence_receipt_sha256)
		VALUES (?,?,?,?,?,?,?,?,?,?)`, workID, ordinal, input.LogicalKey, input.PreparedBytes, input.PayloadSHA256,
		prepared.ProcessingClaimID, prepared.ReceiptSHA256, formatWorkTime(input.ProcessingClaimLeaseUntil), priorOrdinal, fenceReceiptSHA256); err != nil {
		return BoardPublicationAttempt{}, fmt.Errorf("insert immutable BOARD publication attempt: %w", err)
	}
	return BoardPublicationAttempt{
		WorkID: workID, Ordinal: ordinal, LogicalKey: input.LogicalKey,
		PreparedBytes: append([]byte(nil), input.PreparedBytes...), PayloadSHA256: input.PayloadSHA256,
		ProcessingClaimID: prepared.ProcessingClaimID, ProcessingClaimReceiptSHA256: prepared.ReceiptSHA256,
		ProcessingClaimLeaseUntil: input.ProcessingClaimLeaseUntil,
	}, nil
}

func loadUsableBoardPublicationFenceTx(ctx context.Context, tx *sql.Tx, workID string, priorOrdinal int, freshClaimID, receiptSHA256 string, leaseUntil, now time.Time) (BoardPublicationClaimFence, bool, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT prior_attempt_sha256,lease_version,prior_claim_id,fresh_claim_id,fresh_receipt_sha256,fresh_lease_until
		FROM live_board_publication_claim_fence
		WHERE work_id=? AND prior_attempt_ordinal=? AND fresh_receipt_sha256=?`, workID, priorOrdinal, receiptSHA256)
	if err != nil {
		return BoardPublicationClaimFence{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return BoardPublicationClaimFence{}, false, rows.Err()
	}
	var fence BoardPublicationClaimFence
	var priorClaimID string
	var until string
	fence.WorkID, fence.AttemptOrdinal = workID, priorOrdinal
	if err := rows.Scan(&fence.AttemptSHA256, &fence.LeaseVersion, &priorClaimID, &fence.FreshClaimID, &fence.FreshReceiptSHA256, &until); err != nil {
		return BoardPublicationClaimFence{}, false, err
	}
	fence.FreshLeaseUntil, err = parseWorkTime(until)
	if err != nil {
		return BoardPublicationClaimFence{}, false, err
	}
	if rows.Next() {
		return BoardPublicationClaimFence{}, false, ErrBoardPublicationAttemptConflict
	}
	if err := rows.Err(); err != nil {
		return BoardPublicationClaimFence{}, false, err
	}
	if priorClaimID == "" || fence.FreshClaimID != freshClaimID || !fence.FreshLeaseUntil.Equal(leaseUntil) || !fence.FreshLeaseUntil.After(now) {
		return BoardPublicationClaimFence{}, false, nil
	}
	return fence, true, nil
}

func loadBoardPublicationFenceTx(ctx context.Context, tx *sql.Tx, workID string, priorOrdinal int, receiptSHA256 string) (BoardPublicationClaimFence, bool, error) {
	var fence BoardPublicationClaimFence
	var until string
	err := tx.QueryRowContext(ctx, `
		SELECT prior_attempt_sha256,lease_version,fresh_claim_id,fresh_receipt_sha256,fresh_lease_until
		FROM live_board_publication_claim_fence WHERE work_id=? AND prior_attempt_ordinal=? AND fresh_receipt_sha256=?`,
		workID, priorOrdinal, receiptSHA256).Scan(&fence.AttemptSHA256, &fence.LeaseVersion, &fence.FreshClaimID, &fence.FreshReceiptSHA256, &until)
	if errors.Is(err, sql.ErrNoRows) {
		return BoardPublicationClaimFence{}, false, nil
	}
	if err != nil {
		return BoardPublicationClaimFence{}, false, err
	}
	fence.WorkID, fence.AttemptOrdinal = workID, priorOrdinal
	fence.FreshLeaseUntil, err = parseWorkTime(until)
	if err != nil {
		return BoardPublicationClaimFence{}, false, err
	}
	return fence, true, nil
}

func boardPublicationOutcomeExistsTx(ctx context.Context, tx *sql.Tx, workID string) (bool, error) {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM live_board_publication_outcome WHERE work_id=?`, workID).Scan(&count); err != nil {
		return false, err
	}
	return count != 0, nil
}

func validateBoardPublicationCompletion(lease BoardPublicationLease, bundle conversationruntime.ExternalCommittedBundle, input BoardPublicationCompletionInput) error {
	receipt, message, target := input.SendReceipt, input.CanonicalMessage, lease.PublicationIntent.Target
	response := externalResponseEvent(bundle.CanonicalEvents, lease.PublicationIntent.CanonicalResponseMessageID)
	targetKind := lease.TargetKind
	if targetKind == "" {
		targetKind = boardPublicationTargetReply
	}
	wantCausationID, wantHopCount := target.ReplyTo, 1
	switch targetKind {
	case boardPublicationTargetReply:
		if target.ThreadID == "" || target.ReplyTo == "" || lease.Title != "" {
			return ErrBoardPublicationAttemptConflict
		}
	case boardPublicationTargetNewRoot:
		if target.ThreadID != "" || target.ReplyTo != "" || strings.TrimSpace(lease.Title) == "" {
			return ErrBoardPublicationAttemptConflict
		}
		wantCausationID, wantHopCount = receipt.MessageID, 0
	default:
		return ErrBoardPublicationAttemptConflict
	}
	if response == nil || !validBoardPublicationID(receipt.MessageID) || !validBoardPublicationID(receipt.ThreadID) ||
		receipt.StoredAt.IsZero() || receipt.Status != "stored" || message.MessageID != receipt.MessageID || message.ThreadID != receipt.ThreadID ||
		!validBoardPublicationID(message.MessageID) || !validBoardPublicationID(message.ThreadID) ||
		(target.ThreadID != "" && message.ThreadID != target.ThreadID) ||
		message.ChannelID != target.ChannelID || message.SenderPrincipalID != target.CorePrincipalID ||
		len(message.Recipients) != 1 || message.Recipients[0] != target.PeerPrincipalID || message.ReplyTo != target.ReplyTo ||
		message.CausationID != wantCausationID || message.HopCount != wantHopCount {
		return ErrBoardPublicationAttemptConflict
	}
	return nil
}

func loadBoardPublicationOutcomeByWorkTx(ctx context.Context, tx *sql.Tx, workID string) (BoardPublicationOutcome, bool, error) {
	var outcome BoardPublicationOutcome
	var storedAt, recipientsJSON string
	err := tx.QueryRowContext(ctx, `
		SELECT owner_id,journal_instance_id,principal_id,logical_key,attempt_ordinal,payload_sha256,
		       board_message_id,board_thread_id,stored_at,status,channel_id,sender_principal_id,
		       recipients_json,reply_to,causation_id,hop_count,expected_peer_principal_id
		FROM live_board_publication_outcome WHERE work_id=?`, workID).Scan(
		&outcome.Scope.OwnerID, &outcome.Scope.JournalInstanceID, &outcome.Scope.PrincipalID,
		&outcome.LogicalKey, &outcome.AttemptOrdinal, &outcome.PayloadSHA256,
		&outcome.SendReceipt.MessageID, &outcome.SendReceipt.ThreadID, &storedAt, &outcome.SendReceipt.Status,
		&outcome.CanonicalMessage.ChannelID, &outcome.CanonicalMessage.SenderPrincipalID, &recipientsJSON,
		&outcome.CanonicalMessage.ReplyTo, &outcome.CanonicalMessage.CausationID, &outcome.CanonicalMessage.HopCount,
		&outcome.ExpectedPeerPrincipalID)
	if errors.Is(err, sql.ErrNoRows) {
		return BoardPublicationOutcome{}, false, nil
	}
	if err != nil {
		return BoardPublicationOutcome{}, false, err
	}
	outcome.WorkID = workID
	outcome.CanonicalMessage.MessageID = outcome.SendReceipt.MessageID
	outcome.CanonicalMessage.ThreadID = outcome.SendReceipt.ThreadID
	if err := json.Unmarshal([]byte(recipientsJSON), &outcome.CanonicalMessage.Recipients); err != nil || outcome.CanonicalMessage.Recipients == nil {
		return BoardPublicationOutcome{}, false, fmt.Errorf("%w: stored BOARD recipients are corrupt", ErrBoardPublicationOutcomeNotFound)
	}
	outcome.SendReceipt.StoredAt, err = parseWorkTime(storedAt)
	if err != nil {
		return BoardPublicationOutcome{}, false, fmt.Errorf("%w: stored BOARD receipt time is corrupt", ErrBoardPublicationOutcomeNotFound)
	}
	lease, bundle, work, state, err := loadBoardPublicationSourceTx(ctx, tx, workID, outcome.Scope)
	if err != nil {
		return BoardPublicationOutcome{}, false, err
	}
	if state != "completed" || outcome.LogicalKey != lease.LogicalKey || outcome.ExpectedPeerPrincipalID != lease.PublicationIntent.Target.PeerPrincipalID ||
		outcome.AttemptOrdinal < 1 || outcome.AttemptOrdinal > len(lease.Attempts) ||
		lease.Attempts[outcome.AttemptOrdinal-1].PayloadSHA256 != outcome.PayloadSHA256 || work.Kind != "board_publication" {
		return BoardPublicationOutcome{}, false, fmt.Errorf("%w: BOARD publication result does not match its immutable attempt", ErrBoardPublicationOutcomeNotFound)
	}
	validationInput := BoardPublicationCompletionInput{
		Lease: lease, AttemptOrdinal: outcome.AttemptOrdinal, AttemptSHA256: outcome.PayloadSHA256,
		SendReceipt: outcome.SendReceipt, CanonicalMessage: outcome.CanonicalMessage,
	}
	if err := validateBoardPublicationCompletion(lease, bundle, validationInput); err != nil {
		return BoardPublicationOutcome{}, false, fmt.Errorf("%w: stored BOARD publication tuple is invalid", ErrBoardPublicationOutcomeNotFound)
	}
	return outcome, true, nil
}

func boardPublicationOutcomeMatchesInput(outcome BoardPublicationOutcome, input BoardPublicationCompletionInput) bool {
	return outcome.Scope == input.Lease.Scope && outcome.WorkID == input.Lease.WorkID && outcome.LogicalKey == input.Lease.LogicalKey &&
		outcome.AttemptOrdinal == input.AttemptOrdinal && outcome.PayloadSHA256 == input.AttemptSHA256 &&
		outcome.SendReceipt == input.SendReceipt && reflect.DeepEqual(outcome.CanonicalMessage, input.CanonicalMessage)
}

type boardPublicationPreparedWire struct {
	ChannelID              string   `json:"channel_id"`
	ThreadID               string   `json:"thread_id,omitempty"`
	Title                  string   `json:"title,omitempty"`
	Body                   string   `json:"body"`
	Recipients             []string `json:"recipients"`
	Mentions               []string `json:"mentions,omitempty"`
	ReplyTo                string   `json:"reply_to,omitempty"`
	Kind                   string   `json:"kind,omitempty"`
	IdempotencyKey         string   `json:"idempotency_key"`
	ProcessingClaimID      string   `json:"processing_claim_id,omitempty"`
	ProcessingClaimReceipt string   `json:"processing_claim_receipt,omitempty"`
}

func decodeBoardPublicationPreparedWire(payload []byte) (boardPublicationPreparedWire, error) {
	var wire boardPublicationPreparedWire
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return boardPublicationPreparedWire{}, fmt.Errorf("decode exact BOARD send: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return boardPublicationPreparedWire{}, errors.New("exact BOARD send has trailing JSON data")
	}
	return wire, nil
}

func boardPublicationDigest(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func validBoardPublicationDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}

func validBoardPublicationID(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	first := value[0]
	if !((first >= 'a' && first <= 'z') || (first >= '0' && first <= '9')) {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' && character != '-' {
			return false
		}
	}
	return true
}
