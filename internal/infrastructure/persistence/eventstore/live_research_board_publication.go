package eventstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	appresearch "github.com/Nyukimin/RenCrow_CORE/internal/application/research"
)

var (
	ErrResearchBoardPublicationScopeMismatch = errors.New("research BOARD publication scope mismatch")
	ErrResearchBoardPublicationConflict      = errors.New("research BOARD publication conflicts with its immutable request")
	ErrResearchBoardPublicationUnavailable   = errors.New("research BOARD publication journal unavailable")
)

const maxResearchBoardLeaseDuration = 2 * time.Minute

type researchBoardPublicationRow struct {
	Scope          appresearch.BoardPublicationScope
	OperationID    string
	DestinationRef string
	DataScope      string
	ChannelID      string
	ThreadID       string
	NewThread      bool
	Title          string
	Recipients     []string
	ContentDigest  string
	IdempotencyKey string
	PreparedBytes  []byte
	PayloadSHA256  string
	State          string
	LeaseToken     string
	LeaseExpiresAt time.Time
	Version        int64
	AttemptCount   int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type researchBoardPublicationClaimResult struct {
	Lease   appresearch.BoardPublicationLease
	Claimed bool
}

// ClaimResearchBoardPublication persists the first exact prepared request
// before BOARD I/O and leases the semantic operation for one caller. A
// repeated identical request can only recover the stored request bytes.
func (s *SQLiteStore) ClaimResearchBoardPublication(ctx context.Context, input appresearch.BoardPublicationClaimInput) (appresearch.BoardPublicationLease, bool, error) {
	if err := s.ensureOpen(); err != nil {
		return appresearch.BoardPublicationLease{}, false, err
	}
	if ctx == nil || validateResearchBoardPublicationClaim(input) != nil {
		return appresearch.BoardPublicationLease{}, false, ErrResearchBoardPublicationUnavailable
	}
	input = cloneResearchBoardClaim(input)
	if s.shouldQueueWrite(ctx) {
		result, err := enqueueOrderedWriteResult(s, ctx, 0, input, func(commandCtx context.Context) (researchBoardPublicationClaimResult, error) {
			lease, claimed, claimErr := s.claimResearchBoardPublicationInline(commandCtx, input)
			return researchBoardPublicationClaimResult{Lease: lease, Claimed: claimed}, claimErr
		})
		return result.Lease, result.Claimed, err
	}
	return s.claimResearchBoardPublicationInline(ctx, input)
}

func (s *SQLiteStore) claimResearchBoardPublicationInline(ctx context.Context, input appresearch.BoardPublicationClaimInput) (appresearch.BoardPublicationLease, bool, error) {
	if input.LeaseDuration <= 0 || input.LeaseDuration > maxResearchBoardLeaseDuration {
		return appresearch.BoardPublicationLease{}, false, ErrResearchBoardPublicationUnavailable
	}
	now := normalizedNow(input.Now)
	nowText := formatWorkTime(now)
	expiresAt := now.Add(input.LeaseDuration)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return appresearch.BoardPublicationLease{}, false, err
	}
	defer tx.Rollback()
	if _, err := requireBoardPublicationPinTx(ctx, tx, researchBoardEventScope(input.Scope)); err != nil {
		return appresearch.BoardPublicationLease{}, false, errors.Join(ErrResearchBoardPublicationScopeMismatch, err)
	}
	row, found, err := loadResearchBoardPublicationTx(ctx, tx, input.OperationID)
	if err != nil {
		return appresearch.BoardPublicationLease{}, false, err
	}
	if found {
		if !sameResearchBoardPublicationInput(row, input) {
			return appresearch.BoardPublicationLease{}, false, ErrResearchBoardPublicationConflict
		}
		if row.State == "completed" {
			if err := tx.Commit(); err != nil {
				return appresearch.BoardPublicationLease{}, false, err
			}
			return appresearch.BoardPublicationLease{}, false, nil
		}
		if row.State == "leased" && row.LeaseExpiresAt.After(now) {
			if err := tx.Commit(); err != nil {
				return appresearch.BoardPublicationLease{}, false, err
			}
			return appresearch.BoardPublicationLease{}, false, nil
		}
	} else {
		recipientsJSON, err := json.Marshal(input.Recipients)
		if err != nil {
			return appresearch.BoardPublicationLease{}, false, ErrResearchBoardPublicationUnavailable
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO live_research_board_publication
				(operation_id,owner_id,principal_id,journal_instance_id,destination_ref,data_scope,channel_id,thread_id,
				 new_thread,title,recipients_json,content_digest,idempotency_key,prepared_bytes,payload_sha256,state,
				 lease_token,lease_expires_at,version,attempt_count,created_at,updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'pending','','',1,0,?,?)`,
			input.OperationID, input.Scope.OwnerID, input.Scope.PrincipalID, input.Scope.JournalInstanceID,
			input.DestinationRef, input.DataScope, input.ChannelID, input.ThreadID, boolInt(input.NewThread), input.Title,
			string(recipientsJSON), input.ContentDigest, input.IdempotencyKey, input.PreparedBytes, input.PayloadSHA256,
			nowText, nowText); err != nil {
			return appresearch.BoardPublicationLease{}, false, err
		}
	}
	token, err := newResearchBoardLeaseToken()
	if err != nil {
		return appresearch.BoardPublicationLease{}, false, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE live_research_board_publication
		SET state='leased',lease_token=?,lease_expires_at=?,version=version+1,updated_at=?
		WHERE operation_id=? AND state<>'completed'
		  AND (state='pending' OR (state='leased' AND julianday(lease_expires_at)<=julianday(?)))`,
		token, formatWorkTime(expiresAt), nowText, input.OperationID, nowText)
	if err != nil {
		return appresearch.BoardPublicationLease{}, false, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return appresearch.BoardPublicationLease{}, false, err
	}
	if updated != 1 {
		if err := tx.Commit(); err != nil {
			return appresearch.BoardPublicationLease{}, false, err
		}
		return appresearch.BoardPublicationLease{}, false, nil
	}
	row, found, err = loadResearchBoardPublicationTx(ctx, tx, input.OperationID)
	if err != nil || !found || row.State != "leased" || row.LeaseToken != token || !row.LeaseExpiresAt.Equal(expiresAt) {
		if err != nil {
			return appresearch.BoardPublicationLease{}, false, err
		}
		return appresearch.BoardPublicationLease{}, false, ErrResearchBoardPublicationConflict
	}
	if err := tx.Commit(); err != nil {
		return appresearch.BoardPublicationLease{}, false, err
	}
	return researchBoardLease(row), true, nil
}

func (s *SQLiteStore) RecordResearchBoardPublicationAttempt(ctx context.Context, lease appresearch.BoardPublicationLease, now time.Time) (appresearch.BoardPublicationAttempt, error) {
	if err := s.ensureOpen(); err != nil {
		return appresearch.BoardPublicationAttempt{}, err
	}
	if ctx == nil || !validResearchBoardLease(lease) {
		return appresearch.BoardPublicationAttempt{}, ErrResearchBoardPublicationUnavailable
	}
	lease = cloneResearchBoardLease(lease)
	if s.shouldQueueWrite(ctx) {
		return enqueueOrderedWriteResult(s, ctx, 0, struct {
			Lease appresearch.BoardPublicationLease
			Now   time.Time
		}{lease, now}, func(commandCtx context.Context) (appresearch.BoardPublicationAttempt, error) {
			return s.recordResearchBoardPublicationAttemptInline(commandCtx, lease, now)
		})
	}
	return s.recordResearchBoardPublicationAttemptInline(ctx, lease, now)
}

func (s *SQLiteStore) recordResearchBoardPublicationAttemptInline(ctx context.Context, lease appresearch.BoardPublicationLease, now time.Time) (appresearch.BoardPublicationAttempt, error) {
	now = normalizedNow(now)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return appresearch.BoardPublicationAttempt{}, err
	}
	defer tx.Rollback()
	row, found, err := loadResearchBoardPublicationTx(ctx, tx, lease.OperationID)
	if err != nil || !found {
		if err != nil {
			return appresearch.BoardPublicationAttempt{}, err
		}
		return appresearch.BoardPublicationAttempt{}, ErrResearchBoardPublicationConflict
	}
	if !researchBoardLeaseMatches(row, lease) || row.State != "leased" || !row.LeaseExpiresAt.After(now) {
		return appresearch.BoardPublicationAttempt{}, ErrResearchBoardPublicationConflict
	}
	ordinal := row.AttemptCount + 1
	startedAt := formatWorkTime(now)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO live_research_board_publication_attempt(operation_id,attempt_ordinal,payload_sha256,prepared_bytes,started_at)
		VALUES (?,?,?,?,?)`, row.OperationID, ordinal, row.PayloadSHA256, row.PreparedBytes, startedAt); err != nil {
		return appresearch.BoardPublicationAttempt{}, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE live_research_board_publication SET attempt_count=?,updated_at=?
		WHERE operation_id=? AND state='leased' AND lease_token=? AND version=? AND attempt_count=?`,
		ordinal, startedAt, row.OperationID, lease.LeaseToken, lease.Version, row.AttemptCount)
	if err != nil {
		return appresearch.BoardPublicationAttempt{}, err
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		return appresearch.BoardPublicationAttempt{}, ErrResearchBoardPublicationConflict
	}
	if err := tx.Commit(); err != nil {
		return appresearch.BoardPublicationAttempt{}, err
	}
	return appresearch.BoardPublicationAttempt{
		OperationID: row.OperationID, Ordinal: ordinal, PayloadSHA256: row.PayloadSHA256,
		PreparedBytes: append([]byte(nil), row.PreparedBytes...), StartedAt: now,
	}, nil
}

func (s *SQLiteStore) ReleaseResearchBoardPublication(ctx context.Context, lease appresearch.BoardPublicationLease, now time.Time) (bool, error) {
	if err := s.ensureOpen(); err != nil {
		return false, err
	}
	if ctx == nil || !validResearchBoardLease(lease) {
		return false, ErrResearchBoardPublicationUnavailable
	}
	lease = cloneResearchBoardLease(lease)
	if s.shouldQueueWrite(ctx) {
		var released bool
		err := enqueueOrderedWriteResultNoValue(s, ctx, struct {
			Lease appresearch.BoardPublicationLease
			Now   time.Time
		}{lease, now}, func(commandCtx context.Context) error {
			var releaseErr error
			released, releaseErr = s.releaseResearchBoardPublicationInline(commandCtx, lease, now)
			return releaseErr
		})
		return released, err
	}
	return s.releaseResearchBoardPublicationInline(ctx, lease, now)
}

func (s *SQLiteStore) releaseResearchBoardPublicationInline(ctx context.Context, lease appresearch.BoardPublicationLease, now time.Time) (bool, error) {
	nowText := formatWorkTime(normalizedNow(now))
	result, err := s.db.ExecContext(ctx, `
		UPDATE live_research_board_publication
		SET state='pending',lease_token='',lease_expires_at='',version=version+1,updated_at=?
		WHERE operation_id=? AND state='leased' AND lease_token=? AND version=?`,
		nowText, lease.OperationID, lease.LeaseToken, lease.Version)
	if err != nil {
		return false, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return updated == 1, nil
}

func (s *SQLiteStore) CompleteResearchBoardPublication(ctx context.Context, input appresearch.BoardPublicationCompletionInput) (appresearch.BoardPublicationOutcome, error) {
	if err := s.ensureOpen(); err != nil {
		return appresearch.BoardPublicationOutcome{}, err
	}
	if ctx == nil || !validResearchBoardLease(input.Lease) || !validResearchBoardCompletion(input) {
		return appresearch.BoardPublicationOutcome{}, ErrResearchBoardPublicationUnavailable
	}
	input.Lease = cloneResearchBoardLease(input.Lease)
	input.CanonicalMessage.Recipients = append([]string(nil), input.CanonicalMessage.Recipients...)
	if s.shouldQueueWrite(ctx) {
		return enqueueOrderedWriteResult(s, ctx, 0, input, func(commandCtx context.Context) (appresearch.BoardPublicationOutcome, error) {
			return s.completeResearchBoardPublicationInline(commandCtx, input)
		})
	}
	return s.completeResearchBoardPublicationInline(ctx, input)
}

func (s *SQLiteStore) completeResearchBoardPublicationInline(ctx context.Context, input appresearch.BoardPublicationCompletionInput) (appresearch.BoardPublicationOutcome, error) {
	now := normalizedNow(input.Now)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return appresearch.BoardPublicationOutcome{}, err
	}
	defer tx.Rollback()
	row, found, err := loadResearchBoardPublicationTx(ctx, tx, input.Lease.OperationID)
	if err != nil || !found {
		if err != nil {
			return appresearch.BoardPublicationOutcome{}, err
		}
		return appresearch.BoardPublicationOutcome{}, ErrResearchBoardPublicationConflict
	}
	if !researchBoardLeaseMatches(row, input.Lease) {
		return appresearch.BoardPublicationOutcome{}, ErrResearchBoardPublicationConflict
	}
	if row.State == "completed" {
		outcome, exists, err := loadResearchBoardPublicationOutcomeTx(ctx, tx, row)
		if err != nil || !exists || !sameResearchBoardCompletion(outcome, input) {
			if err != nil {
				return appresearch.BoardPublicationOutcome{}, err
			}
			return appresearch.BoardPublicationOutcome{}, ErrResearchBoardPublicationConflict
		}
		if err := tx.Commit(); err != nil {
			return appresearch.BoardPublicationOutcome{}, err
		}
		return outcome, nil
	}
	if row.State != "leased" || !row.LeaseExpiresAt.After(now) {
		return appresearch.BoardPublicationOutcome{}, ErrResearchBoardPublicationConflict
	}
	var attempt appresearch.BoardPublicationAttempt
	var startedAt string
	err = tx.QueryRowContext(ctx, `
		SELECT operation_id,attempt_ordinal,payload_sha256,prepared_bytes,started_at
		FROM live_research_board_publication_attempt WHERE operation_id=? AND attempt_ordinal=?`,
		row.OperationID, input.AttemptOrdinal).Scan(&attempt.OperationID, &attempt.Ordinal, &attempt.PayloadSHA256, &attempt.PreparedBytes, &startedAt)
	if err != nil {
		return appresearch.BoardPublicationOutcome{}, ErrResearchBoardPublicationConflict
	}
	attempt.StartedAt, err = time.Parse(time.RFC3339Nano, startedAt)
	if err != nil || attempt.PayloadSHA256 != input.AttemptSHA256 || attempt.PayloadSHA256 != row.PayloadSHA256 || !bytesEqual(attempt.PreparedBytes, row.PreparedBytes) || input.AttemptOrdinal != row.AttemptCount {
		return appresearch.BoardPublicationOutcome{}, ErrResearchBoardPublicationConflict
	}
	recipientsJSON, err := json.Marshal(input.CanonicalMessage.Recipients)
	if err != nil {
		return appresearch.BoardPublicationOutcome{}, ErrResearchBoardPublicationUnavailable
	}
	completedAt := formatWorkTime(now)
	outcome := appresearch.BoardPublicationOutcome{
		Scope: row.Scope, OperationID: row.OperationID, DestinationRef: row.DestinationRef, DataScope: row.DataScope,
		ContentDigest: row.ContentDigest, IdempotencyKey: row.IdempotencyKey, AttemptOrdinal: input.AttemptOrdinal,
		PayloadSHA256: input.AttemptSHA256, SendReceipt: input.SendReceipt, Canonical: input.CanonicalMessage,
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO live_research_board_publication_outcome
			(operation_id,owner_id,principal_id,journal_instance_id,destination_ref,data_scope,content_digest,idempotency_key,
			 attempt_ordinal,payload_sha256,message_id,thread_id,stored_at,status,channel_id,sender_principal_id,
			 recipients_json,body_digest,completed_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		outcome.OperationID, outcome.Scope.OwnerID, outcome.Scope.PrincipalID, outcome.Scope.JournalInstanceID,
		outcome.DestinationRef, outcome.DataScope, outcome.ContentDigest, outcome.IdempotencyKey,
		outcome.AttemptOrdinal, outcome.PayloadSHA256, outcome.SendReceipt.MessageID, outcome.SendReceipt.ThreadID,
		formatWorkTime(outcome.SendReceipt.StoredAt), outcome.SendReceipt.Status, outcome.Canonical.ChannelID,
		outcome.Canonical.SenderPrincipalID, string(recipientsJSON), outcome.Canonical.BodyDigest, completedAt); err != nil {
		return appresearch.BoardPublicationOutcome{}, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE live_research_board_publication
		SET state='completed',lease_token='',lease_expires_at='',version=version+1,updated_at=?
		WHERE operation_id=? AND state='leased' AND lease_token=? AND version=? AND attempt_count=?`,
		completedAt, row.OperationID, input.Lease.LeaseToken, input.Lease.Version, input.AttemptOrdinal)
	if err != nil {
		return appresearch.BoardPublicationOutcome{}, err
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		return appresearch.BoardPublicationOutcome{}, ErrResearchBoardPublicationConflict
	}
	if err := tx.Commit(); err != nil {
		return appresearch.BoardPublicationOutcome{}, err
	}
	return outcome, nil
}

func (s *SQLiteStore) LoadResearchBoardPublication(ctx context.Context, scope appresearch.BoardPublicationScope, operationID string) (appresearch.BoardPublicationOutcome, bool, error) {
	if err := s.ensureOpen(); err != nil {
		return appresearch.BoardPublicationOutcome{}, false, err
	}
	if ctx == nil || validateResearchBoardPublicationScope(scope) != nil || !validResearchBoardOperationID(operationID) {
		return appresearch.BoardPublicationOutcome{}, false, ErrResearchBoardPublicationUnavailable
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return appresearch.BoardPublicationOutcome{}, false, err
	}
	defer tx.Rollback()
	row, found, err := loadResearchBoardPublicationTx(ctx, tx, operationID)
	if err != nil || !found {
		if err != nil {
			return appresearch.BoardPublicationOutcome{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return appresearch.BoardPublicationOutcome{}, false, err
		}
		return appresearch.BoardPublicationOutcome{}, false, nil
	}
	if row.Scope != scope {
		return appresearch.BoardPublicationOutcome{}, false, ErrResearchBoardPublicationScopeMismatch
	}
	if _, err := requireBoardPublicationPinTx(ctx, tx, researchBoardEventScope(scope)); err != nil {
		return appresearch.BoardPublicationOutcome{}, false, errors.Join(ErrResearchBoardPublicationScopeMismatch, err)
	}
	if row.State != "completed" {
		if err := tx.Commit(); err != nil {
			return appresearch.BoardPublicationOutcome{}, false, err
		}
		return appresearch.BoardPublicationOutcome{}, false, nil
	}
	outcome, found, err := loadResearchBoardPublicationOutcomeTx(ctx, tx, row)
	if err != nil || !found {
		if err != nil {
			return appresearch.BoardPublicationOutcome{}, false, err
		}
		return appresearch.BoardPublicationOutcome{}, false, ErrResearchBoardPublicationUnavailable
	}
	if err := tx.Commit(); err != nil {
		return appresearch.BoardPublicationOutcome{}, false, err
	}
	return outcome, true, nil
}

func enqueueOrderedWriteResultNoValue(s *SQLiteStore, ctx context.Context, command any, apply func(context.Context) error) error {
	sizeBytes, err := queuedWriteSize(command)
	if err != nil {
		return err
	}
	return s.enqueueOrderedWrite(ctx, 0, sizeBytes, apply)
}

func validateResearchBoardPublicationClaim(input appresearch.BoardPublicationClaimInput) error {
	destination := appresearch.BoardDestination{
		Ref: input.DestinationRef, OwnerID: input.Scope.OwnerID, PrincipalID: input.Scope.PrincipalID,
		ChannelID: input.ChannelID, ThreadID: input.ThreadID, NewThread: input.NewThread,
		Recipients: input.Recipients, DataScope: input.DataScope, Title: input.Title,
	}
	if validateResearchBoardPublicationScope(input.Scope) != nil || destination.Validate() != nil ||
		!validResearchBoardOperationID(input.OperationID) || input.OperationID != appresearch.BoardOperationID(input.Scope, input.DestinationRef, input.DataScope, input.ChannelID, input.ThreadID, input.NewThread, input.Title, input.ContentDigest, input.Recipients) ||
		!validResearchBoardDigest(input.ContentDigest) || input.IdempotencyKey != input.OperationID ||
		len(input.PreparedBytes) == 0 || len(input.PreparedBytes) > 64<<10 || !validResearchBoardDigest(input.PayloadSHA256) {
		return ErrResearchBoardPublicationUnavailable
	}
	digest := sha256.Sum256(input.PreparedBytes)
	if hex.EncodeToString(digest[:]) != input.PayloadSHA256 {
		return ErrResearchBoardPublicationConflict
	}
	return nil
}

func validateResearchBoardPublicationScope(scope appresearch.BoardPublicationScope) error {
	return validateBoardPublicationScope(researchBoardEventScope(scope))
}

func researchBoardEventScope(scope appresearch.BoardPublicationScope) BoardPublicationScope {
	return BoardPublicationScope{OwnerID: scope.OwnerID, PrincipalID: scope.PrincipalID, JournalInstanceID: scope.JournalInstanceID}
}

func validResearchBoardOperationID(value string) bool {
	const prefix = "research-board-"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+64 {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, prefix))
	return err == nil && len(decoded) == sha256.Size
}

func validResearchBoardDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func validResearchBoardCanonicalID(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for index, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || index > 0 && (char == '_' || char == '-')) {
			return false
		}
	}
	return true
}

func sameResearchBoardPublicationInput(row researchBoardPublicationRow, input appresearch.BoardPublicationClaimInput) bool {
	return row.Scope == input.Scope && row.OperationID == input.OperationID && row.DestinationRef == input.DestinationRef &&
		row.DataScope == input.DataScope && row.ChannelID == input.ChannelID && row.ThreadID == input.ThreadID && row.NewThread == input.NewThread &&
		row.Title == input.Title && reflect.DeepEqual(row.Recipients, input.Recipients) && row.ContentDigest == input.ContentDigest &&
		row.IdempotencyKey == input.IdempotencyKey && row.PayloadSHA256 == input.PayloadSHA256 && bytesEqual(row.PreparedBytes, input.PreparedBytes)
}

func validResearchBoardLease(lease appresearch.BoardPublicationLease) bool {
	return validateResearchBoardPublicationScope(lease.Scope) == nil && validResearchBoardOperationID(lease.OperationID) &&
		lease.LeaseToken != "" && lease.Version > 0 && validResearchBoardDigest(lease.PayloadSHA256) && len(lease.PreparedBytes) > 0 &&
		sha256Bytes(lease.PreparedBytes) == lease.PayloadSHA256
}

func researchBoardLeaseMatches(row researchBoardPublicationRow, lease appresearch.BoardPublicationLease) bool {
	return row.Scope == lease.Scope && row.OperationID == lease.OperationID && row.DestinationRef == lease.DestinationRef &&
		row.DataScope == lease.DataScope && row.ChannelID == lease.ChannelID && row.ThreadID == lease.ThreadID && row.NewThread == lease.NewThread &&
		row.Title == lease.Title && reflect.DeepEqual(row.Recipients, lease.Recipients) && row.ContentDigest == lease.ContentDigest &&
		row.IdempotencyKey == lease.IdempotencyKey && row.PayloadSHA256 == lease.PayloadSHA256 && bytesEqual(row.PreparedBytes, lease.PreparedBytes) &&
		row.LeaseToken == lease.LeaseToken && row.Version == lease.Version && row.LeaseExpiresAt.Equal(lease.LeaseExpiresAt)
}

func validResearchBoardCompletion(input appresearch.BoardPublicationCompletionInput) bool {
	lease := input.Lease
	r := input.SendReceipt
	m := input.CanonicalMessage
	return input.AttemptOrdinal > 0 && validResearchBoardDigest(input.AttemptSHA256) &&
		validResearchBoardCanonicalID(r.MessageID) && validResearchBoardCanonicalID(r.ThreadID) && r.Status == "stored" && !r.StoredAt.IsZero() &&
		m.MessageID == r.MessageID && m.ThreadID == r.ThreadID && m.ChannelID == lease.ChannelID && m.SenderPrincipalID == lease.Scope.PrincipalID &&
		reflect.DeepEqual(m.Recipients, lease.Recipients) && m.BodyDigest == lease.ContentDigest &&
		(lease.NewThread || r.ThreadID == lease.ThreadID)
}

func sameResearchBoardCompletion(outcome appresearch.BoardPublicationOutcome, input appresearch.BoardPublicationCompletionInput) bool {
	return outcome.Scope == input.Lease.Scope && outcome.OperationID == input.Lease.OperationID &&
		outcome.AttemptOrdinal == input.AttemptOrdinal && outcome.PayloadSHA256 == input.AttemptSHA256 &&
		outcome.SendReceipt == input.SendReceipt && reflect.DeepEqual(outcome.Canonical, input.CanonicalMessage)
}

func researchBoardLease(row researchBoardPublicationRow) appresearch.BoardPublicationLease {
	return appresearch.BoardPublicationLease{
		Scope: row.Scope, OperationID: row.OperationID, DestinationRef: row.DestinationRef, DataScope: row.DataScope,
		ChannelID: row.ChannelID, ThreadID: row.ThreadID, NewThread: row.NewThread, Title: row.Title,
		Recipients: append([]string(nil), row.Recipients...), ContentDigest: row.ContentDigest, IdempotencyKey: row.IdempotencyKey,
		PreparedBytes: append([]byte(nil), row.PreparedBytes...), PayloadSHA256: row.PayloadSHA256,
		LeaseToken: row.LeaseToken, LeaseExpiresAt: row.LeaseExpiresAt, Version: row.Version, AttemptCount: row.AttemptCount,
	}
}

func loadResearchBoardPublicationTx(ctx context.Context, tx *sql.Tx, operationID string) (researchBoardPublicationRow, bool, error) {
	var row researchBoardPublicationRow
	var newThread int
	var recipientsJSON string
	var leaseExpiresAt, createdAt, updatedAt string
	err := tx.QueryRowContext(ctx, `
		SELECT operation_id,owner_id,principal_id,journal_instance_id,destination_ref,data_scope,channel_id,thread_id,
			new_thread,title,recipients_json,content_digest,idempotency_key,prepared_bytes,payload_sha256,state,
			lease_token,lease_expires_at,version,attempt_count,created_at,updated_at
		FROM live_research_board_publication WHERE operation_id=?`, operationID).Scan(
		&row.OperationID, &row.Scope.OwnerID, &row.Scope.PrincipalID, &row.Scope.JournalInstanceID, &row.DestinationRef,
		&row.DataScope, &row.ChannelID, &row.ThreadID, &newThread, &row.Title, &recipientsJSON, &row.ContentDigest,
		&row.IdempotencyKey, &row.PreparedBytes, &row.PayloadSHA256, &row.State, &row.LeaseToken, &leaseExpiresAt,
		&row.Version, &row.AttemptCount, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return researchBoardPublicationRow{}, false, nil
	}
	if err != nil {
		return researchBoardPublicationRow{}, false, err
	}
	if newThread != 0 && newThread != 1 || json.Unmarshal([]byte(recipientsJSON), &row.Recipients) != nil || row.Recipients == nil {
		return researchBoardPublicationRow{}, false, ErrResearchBoardPublicationUnavailable
	}
	row.NewThread = newThread == 1
	if leaseExpiresAt != "" {
		row.LeaseExpiresAt, err = time.Parse(time.RFC3339Nano, leaseExpiresAt)
		if err != nil {
			return researchBoardPublicationRow{}, false, ErrResearchBoardPublicationUnavailable
		}
	}
	row.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return researchBoardPublicationRow{}, false, ErrResearchBoardPublicationUnavailable
	}
	row.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return researchBoardPublicationRow{}, false, ErrResearchBoardPublicationUnavailable
	}
	return row, true, nil
}

func loadResearchBoardPublicationOutcomeTx(ctx context.Context, tx *sql.Tx, row researchBoardPublicationRow) (appresearch.BoardPublicationOutcome, bool, error) {
	var outcome appresearch.BoardPublicationOutcome
	var newThread = row.NewThread
	var recipientsJSON, storedAt string
	err := tx.QueryRowContext(ctx, `
		SELECT operation_id,owner_id,principal_id,journal_instance_id,destination_ref,data_scope,content_digest,idempotency_key,
			attempt_ordinal,payload_sha256,message_id,thread_id,stored_at,status,channel_id,sender_principal_id,recipients_json,body_digest
		FROM live_research_board_publication_outcome WHERE operation_id=?`, row.OperationID).Scan(
		&outcome.OperationID, &outcome.Scope.OwnerID, &outcome.Scope.PrincipalID, &outcome.Scope.JournalInstanceID,
		&outcome.DestinationRef, &outcome.DataScope, &outcome.ContentDigest, &outcome.IdempotencyKey,
		&outcome.AttemptOrdinal, &outcome.PayloadSHA256, &outcome.SendReceipt.MessageID, &outcome.SendReceipt.ThreadID,
		&storedAt, &outcome.SendReceipt.Status, &outcome.Canonical.ChannelID, &outcome.Canonical.SenderPrincipalID,
		&recipientsJSON, &outcome.Canonical.BodyDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return appresearch.BoardPublicationOutcome{}, false, nil
	}
	if err != nil {
		return appresearch.BoardPublicationOutcome{}, false, err
	}
	outcome.SendReceipt.StoredAt, err = time.Parse(time.RFC3339Nano, storedAt)
	if err != nil || json.Unmarshal([]byte(recipientsJSON), &outcome.Canonical.Recipients) != nil || outcome.Canonical.Recipients == nil {
		return appresearch.BoardPublicationOutcome{}, false, ErrResearchBoardPublicationUnavailable
	}
	outcome.Canonical.MessageID, outcome.Canonical.ThreadID = outcome.SendReceipt.MessageID, outcome.SendReceipt.ThreadID
	if !newThread && outcome.SendReceipt.ThreadID != row.ThreadID || outcome.Canonical.BodyDigest != row.ContentDigest ||
		outcome.Scope != row.Scope || outcome.OperationID != row.OperationID || outcome.DestinationRef != row.DestinationRef ||
		outcome.DataScope != row.DataScope || outcome.ContentDigest != row.ContentDigest || outcome.IdempotencyKey != row.IdempotencyKey ||
		outcome.PayloadSHA256 != row.PayloadSHA256 || outcome.Canonical.ChannelID != row.ChannelID || outcome.Canonical.SenderPrincipalID != row.Scope.PrincipalID ||
		!reflect.DeepEqual(outcome.Canonical.Recipients, row.Recipients) || outcome.SendReceipt.Status != "stored" || outcome.SendReceipt.StoredAt.IsZero() {
		return appresearch.BoardPublicationOutcome{}, false, ErrResearchBoardPublicationUnavailable
	}
	return outcome, true, nil
}

func bytesEqual(left, right []byte) bool {
	return string(left) == string(right)
}

func sha256Bytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func newResearchBoardLeaseToken() (string, error) {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}

func cloneResearchBoardClaim(input appresearch.BoardPublicationClaimInput) appresearch.BoardPublicationClaimInput {
	input.Recipients = append([]string(nil), input.Recipients...)
	input.PreparedBytes = append([]byte(nil), input.PreparedBytes...)
	return input
}

func cloneResearchBoardLease(lease appresearch.BoardPublicationLease) appresearch.BoardPublicationLease {
	lease.Recipients = append([]string(nil), lease.Recipients...)
	lease.PreparedBytes = append([]byte(nil), lease.PreparedBytes...)
	return lease
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
