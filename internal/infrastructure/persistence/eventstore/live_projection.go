package eventstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const conversationProjectionWorkKind = "conversation_projection"

const maxProjectionDeliveryRetryDelay = time.Minute

var (
	ErrConversationProjectionConflict          = errors.New("conversation projection work or canonical output conflicts")
	ErrConversationProjectionNotReady          = errors.New("conversation projection is not eligible before first delivery attempt")
	ErrConversationProjectionBlocked           = errors.New("conversation projection cannot pass an unapplied public message")
	ErrConversationProjectionTerminalAttention = errors.New("clarification reply delivery exhausted attempts and needs attention")
)

type ConversationProjectionDeliveryAttemptInput struct {
	WorkID        string
	SourceEventID modulecore.EventID
	OwnerID       string
	AttemptedAt   time.Time
}

// PendingClarificationReplyProjection is an accepted public answer that still
// needs its first delivery attempt before normal projection work can be claimed.
type PendingClarificationReplyProjection struct {
	WorkID        string                   `json:"work_id"`
	SourceEventID modulecore.EventID       `json:"source_event_id"`
	SessionID     modulecore.SessionID     `json:"session_id"`
	Event         modulecore.EventEnvelope `json:"event"`
}

type LoadConversationProjectionInput struct {
	Lease   WorkLeaseInput
	OwnerID string
}

type LeasedConversationProjection struct {
	Work              LeasedWorkItem                            `json:"work"`
	OwnerID           string                                    `json:"owner_id"`
	SessionID         modulecore.SessionID                      `json:"session_id"`
	ExpectedWatermark modulecore.EventSeq                       `json:"expected_watermark"`
	AppliedThrough    modulecore.EventSeq                       `json:"applied_through"`
	Items             []domconv.CanonicalConversationProjection `json:"items"`
}

type CompleteConversationProjectionInput struct {
	Lease             WorkLeaseInput
	OwnerID           string
	ExpectedWatermark modulecore.EventSeq
	Receipt           domconv.ConversationProjectionApplyReceipt
}

// ConversationProjectionWorkID returns the stable work identity used by
// output commit paths that create a projection item directly from its event.
func ConversationProjectionWorkID(eventID modulecore.EventID) string {
	return "projection:" + string(eventID)
}

func conversationProjectionDedupeKey(eventID modulecore.EventID) string {
	return "conversation_projection:" + string(eventID)
}

func insertConversationProjectionWork(ctx context.Context, tx *sql.Tx, event modulecore.EventEnvelope, index MessageIndex) error {
	if index.Visibility != "public" || index.ToKind != "user" {
		return nil
	}
	var ownerID string
	if err := tx.QueryRowContext(ctx, `SELECT owner_id FROM live_conversation_session WHERE session_id=?`, string(index.SessionID)).Scan(&ownerID); err != nil {
		return err
	}
	if _, _, err := loadPublicProjectionOutput(ctx, tx, event.EventID, ownerID); err != nil {
		return err
	}
	workID := ConversationProjectionWorkID(event.EventID)
	_, err := tx.ExecContext(ctx, `INSERT INTO live_work_outbox
		(work_id,dedupe_key,kind,source_event_id,session_id,priority,state,attempt,not_before,version)
		VALUES(?,?,?,?,?,10,'pending',0,'',1)`, workID, conversationProjectionDedupeKey(event.EventID), conversationProjectionWorkKind,
		string(event.EventID), string(index.SessionID))
	return err
}

func insertClarificationReplyProjectionWork(ctx context.Context, tx *sql.Tx, event modulecore.EventEnvelope, index MessageIndex, receipt ClarificationReplyReceipt) error {
	if receipt.OwnerID == "" || receipt.SessionID != index.SessionID || receipt.ReplyTurnID != index.TurnID ||
		receipt.ReplyMessageID != index.MessageID || receipt.ReplyMessageID != event.MessageID || receipt.SessionID != event.SessionID {
		return ErrConversationProjectionConflict
	}
	state, found, err := readSessionStateTx(ctx, tx, receipt.SessionID, receipt.OwnerID)
	if err != nil {
		return err
	}
	if !found || state.RuntimeVersion != "v2" || event.EventSeq <= state.CutoverSeq {
		return ErrConversationSessionConflict
	}
	loadedIndex, loadedEvent, err := loadPublicClarificationReplyProjectionAnswer(ctx, tx, event.EventID, receipt.OwnerID)
	if err != nil {
		return fmt.Errorf("validate accepted clarification projection source: %w", err)
	}
	if loadedIndex != index || loadedEvent.EventSeq != event.EventSeq || loadedEvent.MessageID != event.MessageID {
		return ErrConversationProjectionConflict
	}
	workID := ConversationProjectionWorkID(event.EventID)
	_, err = tx.ExecContext(ctx, `INSERT INTO live_work_outbox
		(work_id,dedupe_key,kind,source_event_id,session_id,priority,state,attempt,not_before,version)
		VALUES(?,?,?,?,?,10,'pending',0,'',1)`, workID, conversationProjectionDedupeKey(event.EventID), conversationProjectionWorkKind,
		string(event.EventID), string(index.SessionID))
	return err
}

// MarkConversationProjectionDeliveryAttempt unlocks one projection only after
// the first public delivery attempt has begun. The caller invokes this before
// publishing the committed event; replaying the same mark is idempotent.
func (s *SQLiteStore) MarkConversationProjectionDeliveryAttempt(ctx context.Context, input ConversationProjectionDeliveryAttemptInput) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if ctx == nil || strings.TrimSpace(input.OwnerID) == "" || input.OwnerID != strings.TrimSpace(input.OwnerID) || strings.TrimSpace(input.WorkID) == "" {
		return errors.New("projection delivery context, work, and owner are required")
	}
	if err := input.SourceEventID.Validate(); err != nil {
		return fmt.Errorf("source_event_id: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	work, state, err := readWorkForUpdate(ctx, tx, input.WorkID)
	if err != nil || work.Kind != conversationProjectionWorkKind || work.SourceEventID != input.SourceEventID ||
		(state != "pending" && state != "leased" && state != "completed") {
		return ErrConversationProjectionConflict
	}
	stateRecord, found, err := readSessionStateTx(ctx, tx, work.SessionID, input.OwnerID)
	if err != nil {
		return err
	}
	if !found {
		return ErrConversationSessionConflict
	}
	index, event, err := loadPublicProjectionSource(ctx, tx, input.SourceEventID, input.OwnerID)
	if err != nil {
		return err
	}
	if work.SessionID != index.SessionID {
		return ErrConversationProjectionConflict
	}
	if stateRecord.RuntimeVersion != "v2" || event.EventSeq <= stateRecord.CutoverSeq {
		return ErrConversationSessionConflict
	}
	var previousWork, previousSession, previousOwner string
	err = tx.QueryRowContext(ctx, `SELECT work_id, session_id, owner_id FROM live_conversation_projection_delivery WHERE event_id=?`, string(input.SourceEventID)).Scan(&previousWork, &previousSession, &previousOwner)
	if err == nil {
		if previousWork != input.WorkID || previousSession != string(work.SessionID) || previousOwner != input.OwnerID {
			return ErrConversationProjectionConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if state == "completed" {
		return ErrConversationProjectionConflict
	}
	attemptedAt := input.AttemptedAt
	if attemptedAt.IsZero() {
		attemptedAt = time.Now().UTC()
	} else {
		attemptedAt = attemptedAt.UTC()
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO live_conversation_projection_delivery(event_id,work_id,session_id,owner_id,attempted_at)
		VALUES(?,?,?,?,?)`, string(input.SourceEventID), input.WorkID, string(index.SessionID), input.OwnerID, formatWorkTime(attemptedAt)); err != nil {
		return err
	}
	return tx.Commit()
}

// ListUnmarkedClarificationReplyProjections returns a bounded batch of
// accepted clarification answers that still need a public delivery attempt.
// Reading the work never records the attempt marker.
func (s *SQLiteStore) ListUnmarkedClarificationReplyProjections(ctx context.Context, ownerID string, limit int) ([]PendingClarificationReplyProjection, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	if ctx == nil || strings.TrimSpace(ownerID) == "" || ownerID != strings.TrimSpace(ownerID) || limit < 1 || limit > maxListLimit {
		return nil, errors.New("clarification projection context, owner, and bounded limit are required")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	nowText := formatWorkTime(time.Now().UTC())
	rows, err := tx.QueryContext(ctx, `
		SELECT w.work_id,w.dedupe_key,w.source_event_id,w.session_id
		FROM live_work_outbox w
		JOIN live_conversation_session s ON s.session_id=w.session_id
		JOIN event_envelope e ON e.event_id=w.source_event_id
		JOIN live_message_index m ON m.event_id=e.event_id
		JOIN live_conversation_turn t ON t.turn_id=m.turn_id AND t.session_id=m.session_id
		WHERE w.kind=? AND w.state='pending' AND s.owner_id=? AND s.runtime_version='v2'
		  AND (w.not_before='' OR julianday(w.not_before)<=julianday(?))
		  AND t.owner_id=s.owner_id AND t.trigger_kind='clarification_reply'
		  AND e.event_seq>s.cutover_seq
		  AND NOT EXISTS (SELECT 1 FROM live_conversation_projection_delivery p
		    WHERE p.event_id=w.source_event_id)
		ORDER BY e.event_seq ASC,w.work_id ASC LIMIT ?`, conversationProjectionWorkKind, ownerID, nowText, limit)
	if err != nil {
		return nil, err
	}
	type pendingRow struct {
		workID, dedupeKey, sourceEventID, sessionID string
	}
	pending := make([]pendingRow, 0, limit)
	for rows.Next() {
		var row pendingRow
		if err := rows.Scan(&row.workID, &row.dedupeKey, &row.sourceEventID, &row.sessionID); err != nil {
			rows.Close()
			return nil, err
		}
		pending = append(pending, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	result := make([]PendingClarificationReplyProjection, 0, len(pending))
	for _, row := range pending {
		eventID := modulecore.EventID(row.sourceEventID)
		if row.workID != ConversationProjectionWorkID(eventID) || row.dedupeKey != conversationProjectionDedupeKey(eventID) {
			return nil, ErrConversationProjectionConflict
		}
		index, event, err := loadPublicClarificationReplyProjectionAnswer(ctx, tx, eventID, ownerID)
		if err != nil || string(index.SessionID) != row.sessionID {
			return nil, ErrConversationProjectionConflict
		}
		result = append(result, PendingClarificationReplyProjection{
			WorkID: row.workID, SourceEventID: eventID, SessionID: index.SessionID, Event: event,
		})
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

// RetryUnmarkedClarificationReplyProjection records a failed public delivery
// attempt without opening the projection claim gate. Retry delays and total
// attempts are bounded; exhausted work moves to needs_attention.
func (s *SQLiteStore) RetryUnmarkedClarificationReplyProjection(ctx context.Context, ownerID, workID string, sourceEventID modulecore.EventID, now time.Time, retryAfter time.Duration) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if ctx == nil || strings.TrimSpace(ownerID) == "" || ownerID != strings.TrimSpace(ownerID) ||
		strings.TrimSpace(workID) == "" || workID != strings.TrimSpace(workID) || retryAfter <= 0 || retryAfter > maxProjectionDeliveryRetryDelay {
		return errors.New("unmarked clarification projection retry requires owner, work, context, and a bounded delay")
	}
	if err := sourceEventID.Validate(); err != nil {
		return fmt.Errorf("source_event_id: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	work, state, err := readWorkForUpdate(ctx, tx, workID)
	if err != nil {
		return err
	}
	if work.Kind != conversationProjectionWorkKind || work.SourceEventID != sourceEventID || state != "pending" ||
		work.WorkID != ConversationProjectionWorkID(sourceEventID) {
		return ErrConversationProjectionConflict
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	if !work.NotBefore.IsZero() && now.Before(work.NotBefore) {
		return ErrConversationProjectionNotReady
	}
	session, found, err := readSessionStateTx(ctx, tx, work.SessionID, ownerID)
	if err != nil {
		return err
	}
	if !found || session.RuntimeVersion != "v2" {
		return ErrConversationSessionConflict
	}
	index, event, err := loadPublicClarificationReplyProjectionAnswer(ctx, tx, sourceEventID, ownerID)
	if err != nil || index.SessionID != work.SessionID || event.EventSeq <= session.CutoverSeq {
		return ErrConversationProjectionConflict
	}
	var markedWork, markedOwner string
	err = tx.QueryRowContext(ctx, `SELECT work_id,owner_id FROM live_conversation_projection_delivery WHERE event_id=?`, string(sourceEventID)).Scan(&markedWork, &markedOwner)
	if err == nil {
		if markedWork != workID || markedOwner != ownerID {
			return ErrConversationProjectionConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	newAttempt := work.Attempt + 1
	newState, notBefore := "pending", formatWorkTime(now.Add(retryAfter))
	if newAttempt >= maxWorkAttempts {
		newState, notBefore = "needs_attention", ""
	}
	result, err := tx.ExecContext(ctx, `UPDATE live_work_outbox SET attempt=?,state=?,not_before=?,version=version+1
		WHERE work_id=? AND kind=? AND source_event_id=? AND state='pending' AND version=? AND attempt=?
		AND NOT EXISTS (SELECT 1 FROM live_conversation_projection_delivery WHERE event_id=? AND work_id=?)`,
		newAttempt, newState, notBefore, workID, conversationProjectionWorkKind, string(sourceEventID), work.Version, work.Attempt,
		string(sourceEventID), workID)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		return ErrConversationProjectionConflict
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if newState == "needs_attention" {
		return ErrConversationProjectionTerminalAttention
	}
	return nil
}

// LoadLeasedConversationProjection validates the v2 owner, first delivery
// marker, and lease before persisting or returning canonical thread assignments.
// A failed later L1 apply can safely call this again and receives identical IDs.
func (s *SQLiteStore) LoadLeasedConversationProjection(ctx context.Context, input LoadConversationProjectionInput) (LeasedConversationProjection, error) {
	if err := s.ensureOpen(); err != nil {
		return LeasedConversationProjection{}, err
	}
	if ctx == nil || strings.TrimSpace(input.OwnerID) == "" || input.OwnerID != strings.TrimSpace(input.OwnerID) {
		return LeasedConversationProjection{}, errors.New("projection context and owner are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return LeasedConversationProjection{}, err
	}
	defer tx.Rollback()
	work, workState, err := readWorkForUpdate(ctx, tx, input.Lease.WorkID)
	if err != nil {
		return LeasedConversationProjection{}, err
	}
	if work.Kind != conversationProjectionWorkKind {
		return LeasedConversationProjection{}, ErrConversationProjectionConflict
	}
	if _, err := validateWorkLease(work, workState, input.Lease, normalizedNow(input.Lease.Now)); err != nil {
		return LeasedConversationProjection{}, err
	}
	var markedOwner, markedSession string
	err = tx.QueryRowContext(ctx, `SELECT owner_id,session_id FROM live_conversation_projection_delivery WHERE event_id=? AND work_id=?`, string(work.SourceEventID), work.WorkID).Scan(&markedOwner, &markedSession)
	if errors.Is(err, sql.ErrNoRows) {
		return LeasedConversationProjection{}, ErrConversationProjectionNotReady
	}
	if err != nil {
		return LeasedConversationProjection{}, err
	}
	if markedOwner != input.OwnerID || markedSession != string(work.SessionID) {
		return LeasedConversationProjection{}, ErrConversationSessionConflict
	}
	state, found, err := readSessionStateTx(ctx, tx, work.SessionID, input.OwnerID)
	if err != nil {
		return LeasedConversationProjection{}, err
	}
	if !found || state.RuntimeVersion != "v2" {
		return LeasedConversationProjection{}, ErrConversationSessionConflict
	}
	outputIndex, outputEvent, err := loadPublicProjectionSource(ctx, tx, work.SourceEventID, input.OwnerID)
	if err != nil {
		return LeasedConversationProjection{}, err
	}
	if outputIndex.SessionID != work.SessionID || outputEvent.EventSeq <= state.CutoverSeq || outputEvent.EventSeq <= state.ProjectionWatermark {
		return LeasedConversationProjection{}, ErrProjectionWatermarkConflict
	}
	if err := requirePriorPublicMessagesApplied(ctx, tx, work.SessionID, state.ProjectionWatermark, outputEvent.EventSeq, outputEvent.MessageID, work.WorkID); err != nil {
		return LeasedConversationProjection{}, err
	}
	sources, err := projectionUnitSources(ctx, tx, outputIndex, outputEvent, input.OwnerID, state.ProjectionWatermark)
	if err != nil {
		return LeasedConversationProjection{}, fmt.Errorf("load projection unit sources: %w", err)
	}
	items, err := s.persistProjectionAssignment(ctx, tx, work, sources)
	if err != nil {
		return LeasedConversationProjection{}, fmt.Errorf("persist projection assignment: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return LeasedConversationProjection{}, err
	}
	return LeasedConversationProjection{
		Work: work, OwnerID: input.OwnerID, SessionID: work.SessionID,
		ExpectedWatermark: state.ProjectionWatermark, AppliedThrough: outputEvent.EventSeq, Items: items,
	}, nil
}

// CompleteConversationProjection moves the watermark and acknowledges the
// lease in one EventStore transaction, only after an exact L1 apply receipt is
// supplied and every public indexed message in the range is accounted for.
func (s *SQLiteStore) CompleteConversationProjection(ctx context.Context, input CompleteConversationProjectionInput) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if ctx == nil || strings.TrimSpace(input.OwnerID) == "" || input.OwnerID != strings.TrimSpace(input.OwnerID) {
		return errors.New("projection completion context and owner are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	work, workState, err := readWorkForUpdate(ctx, tx, input.Lease.WorkID)
	if err != nil {
		return err
	}
	if work.Kind != conversationProjectionWorkKind {
		return ErrConversationProjectionConflict
	}
	state, found, err := readSessionStateTx(ctx, tx, work.SessionID, input.OwnerID)
	if err != nil {
		return err
	}
	if !found || state.RuntimeVersion != "v2" {
		return ErrConversationSessionConflict
	}
	if workState == "completed" {
		if work.LeaseToken != input.Lease.LeaseToken || work.Version != input.Lease.Version {
			return ErrWorkConflict
		}
		var sourceSeq int64
		if err := tx.QueryRowContext(ctx, `SELECT event_seq FROM event_envelope WHERE event_id=?`, string(work.SourceEventID)).Scan(&sourceSeq); err != nil {
			return err
		}
		if input.Receipt.AppliedThrough != modulecore.EventSeq(sourceSeq) {
			return ErrConversationProjectionConflict
		}
		if err := verifyProjectionApplyReceipt(ctx, tx, work, input.Receipt); err != nil {
			return err
		}
		if err := requireProjectionWorkApplied(ctx, tx, work.WorkID, len(input.Receipt.Items)); err != nil {
			return err
		}
		return tx.Commit()
	}
	if _, err := validateWorkLease(work, workState, input.Lease, normalizedNow(input.Lease.Now)); err != nil {
		return err
	}
	if state.ProjectionWatermark != input.ExpectedWatermark || input.Receipt.SessionID != work.SessionID || input.Receipt.AppliedThrough <= state.ProjectionWatermark {
		return ErrProjectionWatermarkConflict
	}
	var sourceSeq int64
	if err := tx.QueryRowContext(ctx, `SELECT event_seq FROM event_envelope WHERE event_id=?`, string(work.SourceEventID)).Scan(&sourceSeq); err != nil {
		return err
	}
	if modulecore.EventSeq(sourceSeq) != input.Receipt.AppliedThrough {
		return ErrConversationProjectionConflict
	}
	if err := verifyProjectionApplyReceipt(ctx, tx, work, input.Receipt); err != nil {
		return err
	}
	if err := requireProjectionRangeAccounted(ctx, tx, work.SessionID, state.ProjectionWatermark, modulecore.EventSeq(sourceSeq), work.WorkID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE live_conversation_projection_message SET applied=1 WHERE work_id=? AND applied=0`, work.WorkID)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil || updated == 0 {
		return ErrConversationProjectionConflict
	}
	watermark, err := contiguousProjectionWatermark(ctx, tx, work.SessionID, state.ProjectionWatermark)
	if err != nil {
		return err
	}
	result, err = tx.ExecContext(ctx, `UPDATE live_conversation_session SET projection_watermark=?
		WHERE session_id=? AND owner_id=? AND runtime_version='v2' AND projection_watermark=? AND cutover_seq<=?`,
		int64(watermark), string(work.SessionID), input.OwnerID, int64(state.ProjectionWatermark), int64(watermark))
	if err != nil {
		return err
	}
	updated, err = result.RowsAffected()
	if err != nil || updated != 1 {
		return ErrProjectionWatermarkConflict
	}
	if err := completeLeasedWork(ctx, tx, input.Lease); err != nil {
		return err
	}
	return tx.Commit()
}

func loadPublicProjectionOutput(ctx context.Context, tx *sql.Tx, eventID modulecore.EventID, ownerID string) (MessageIndex, modulecore.EventEnvelope, error) {
	index, err := loadProjectionIndex(ctx, tx, eventID)
	if err != nil {
		return MessageIndex{}, modulecore.EventEnvelope{}, err
	}
	if index.Visibility != "public" {
		return MessageIndex{}, modulecore.EventEnvelope{}, ErrConversationProjectionConflict
	}
	event, err := loadEventEnvelopeTx(ctx, tx, eventID)
	if err != nil {
		return MessageIndex{}, modulecore.EventEnvelope{}, err
	}
	if err := validateMessageIndex(index, event, index.SessionID, index.TurnID); err != nil {
		return MessageIndex{}, modulecore.EventEnvelope{}, ErrConversationProjectionConflict
	}
	if event.EventType != "agent.response" || event.ComponentID != "orchestrator" || event.MessageID != index.MessageID ||
		index.ToKind != "user" || index.ToActor != ownerID || strings.TrimSpace(payloadString(event.Payload, "visibility")) != "public" ||
		payloadString(event.Payload, "to") != "user" || payloadString(event.Payload, "from") != index.FromActor ||
		payloadString(event.Payload, "source_actor_id") != index.FromActor || strings.TrimSpace(projectionText(event.Payload)) == "" {
		return MessageIndex{}, modulecore.EventEnvelope{}, ErrConversationProjectionConflict
	}
	return index, event, nil
}

func loadPublicProjectionSource(ctx context.Context, tx *sql.Tx, eventID modulecore.EventID, ownerID string) (MessageIndex, modulecore.EventEnvelope, error) {
	var triggerKind string
	err := tx.QueryRowContext(ctx, `SELECT t.trigger_kind FROM live_message_index m
		JOIN live_conversation_turn t ON t.turn_id=m.turn_id AND t.session_id=m.session_id
		WHERE m.event_id=?`, string(eventID)).Scan(&triggerKind)
	if err != nil {
		return MessageIndex{}, modulecore.EventEnvelope{}, err
	}
	if triggerKind == "clarification_reply" {
		return loadPublicClarificationReplyProjectionAnswer(ctx, tx, eventID, ownerID)
	}
	return loadPublicProjectionOutput(ctx, tx, eventID, ownerID)
}

func loadPublicClarificationReplyProjectionAnswer(ctx context.Context, tx *sql.Tx, eventID modulecore.EventID, ownerID string) (MessageIndex, modulecore.EventEnvelope, error) {
	index, err := loadProjectionIndex(ctx, tx, eventID)
	if err != nil {
		return MessageIndex{}, modulecore.EventEnvelope{}, err
	}
	if index.Visibility != "public" || index.Kind != "user_message" || index.ToKind != "agent" || index.ToActor != "mio" {
		return MessageIndex{}, modulecore.EventEnvelope{}, ErrConversationProjectionConflict
	}
	loaded, err := loadCommittedClarificationReplyContextTx(ctx, tx, ClarificationReplyContextInput{
		SessionID: index.SessionID, ReplyTurnID: index.TurnID, OwnerID: ownerID,
	})
	if err != nil {
		return MessageIndex{}, modulecore.EventEnvelope{}, ErrConversationProjectionConflict
	}
	answerIndex, answerEvent := loaded.AnswerMessage, loaded.AnswerEvent
	payloadVisibility := payloadString(answerEvent.Payload, "visibility")
	if answerIndex.EventID != eventID || answerIndex.MessageID != index.MessageID || answerIndex.SessionID != index.SessionID ||
		answerIndex.TurnID != index.TurnID || answerIndex.FromActor != index.FromActor || answerIndex.ToKind != "agent" ||
		answerIndex.ToActor != "mio" || answerIndex.Kind != "user_message" || answerIndex.Visibility != "public" ||
		answerIndex.ReplyToMessageID != loaded.Receipt.PromptMessageID || answerEvent.EventID != eventID ||
		answerEvent.EventType != "conversation.user_message" || answerEvent.ComponentID != "orchestrator" ||
		answerEvent.SessionID != index.SessionID || answerEvent.TurnID != index.TurnID || answerEvent.MessageID != index.MessageID ||
		answerEvent.ActorKind != "" || answerEvent.ActorID != "" ||
		payloadString(answerEvent.Payload, "from") != "user" || payloadString(answerEvent.Payload, "to") != "mio" ||
		payloadString(answerEvent.Payload, "source_actor_id") != answerIndex.FromActor ||
		(payloadVisibility != "" && payloadVisibility != "public") ||
		payloadString(answerEvent.Payload, "reply_to_message_id") != string(loaded.Receipt.PromptMessageID) ||
		strings.TrimSpace(payloadString(answerEvent.Payload, "content")) == "" ||
		payloadString(answerEvent.Payload, "text") != payloadString(answerEvent.Payload, "content") {
		return MessageIndex{}, modulecore.EventEnvelope{}, ErrConversationProjectionConflict
	}
	return answerIndex, answerEvent, nil
}

func loadProjectionIndex(ctx context.Context, tx *sql.Tx, eventID modulecore.EventID) (MessageIndex, error) {
	var index MessageIndex
	var messageID, storedEventID, sessionID, turnID, from, toKind, to, kind, visibility string
	var replyTo sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT message_id,event_id,session_id,turn_id,from_actor,to_kind,to_actor,kind,reply_to_message_id,visibility
		FROM live_message_index WHERE event_id=?`, string(eventID)).Scan(
		&messageID, &storedEventID, &sessionID, &turnID, &from, &toKind, &to, &kind, &replyTo, &visibility,
	)
	if err != nil {
		return MessageIndex{}, err
	}
	index = MessageIndex{
		MessageID: modulecore.MessageID(messageID), EventID: modulecore.EventID(storedEventID), SessionID: modulecore.SessionID(sessionID),
		TurnID: modulecore.TurnID(turnID), FromActor: from, ToKind: toKind, ToActor: to, Kind: kind,
		ReplyToMessageID: modulecore.MessageID(replyTo.String), Visibility: visibility,
	}
	return index, nil
}

func projectionUnitSources(ctx context.Context, tx *sql.Tx, outputIndex MessageIndex, outputEvent modulecore.EventEnvelope, ownerID string, watermark modulecore.EventSeq) ([]domconv.ConversationProjectionSource, error) {
	var inputMessageID, triggerKind, actorID string
	err := tx.QueryRowContext(ctx, `SELECT input_message_id,trigger_kind,actor_id FROM live_conversation_turn WHERE turn_id=? AND session_id=? AND owner_id=?`,
		string(outputEvent.TurnID), string(outputEvent.SessionID), ownerID).Scan(&inputMessageID, &triggerKind, &actorID)
	if err != nil {
		return nil, err
	}
	if triggerKind == "clarification_reply" {
		answerIndex, answerEvent, err := loadPublicClarificationReplyProjectionAnswer(ctx, tx, outputEvent.EventID, ownerID)
		if err != nil || answerIndex.MessageID != outputIndex.MessageID || answerEvent.EventSeq != outputEvent.EventSeq ||
			inputMessageID != string(answerIndex.MessageID) || outputIndex.FromActor != actorID {
			return nil, ErrConversationProjectionConflict
		}
		return []domconv.ConversationProjectionSource{projectionSource(answerIndex, answerEvent, "user")}, nil
	}
	sources := make([]domconv.ConversationProjectionSource, 0, 2)
	if triggerKind == "user_message" && inputMessageID != "" {
		var inputEventID string
		var inputVisibility string
		err := tx.QueryRowContext(ctx, `SELECT event_id,visibility FROM live_message_index WHERE message_id=? AND session_id=? AND turn_id=?`, inputMessageID, string(outputEvent.SessionID), string(outputEvent.TurnID)).Scan(&inputEventID, &inputVisibility)
		if err != nil {
			return nil, err
		}
		if inputVisibility == "public" {
			var inputSeq int64
			if err := tx.QueryRowContext(ctx, `SELECT event_seq FROM event_envelope WHERE event_id=?`, inputEventID).Scan(&inputSeq); err != nil {
				return nil, err
			}
			if modulecore.EventSeq(inputSeq) > watermark {
				inputIndex, inputEvent, err := loadPublicProjectionInput(ctx, tx, modulecore.EventID(inputEventID), actorID)
				if err != nil {
					return nil, err
				}
				sources = append(sources, projectionSource(inputIndex, inputEvent, "user"))
			}
		} else if inputVisibility != "private" {
			return nil, ErrConversationProjectionConflict
		}
	}
	sources = append(sources, projectionSource(outputIndex, outputEvent, outputIndex.FromActor))
	if len(sources) == 0 || len(sources) > 2 {
		return nil, ErrConversationProjectionConflict
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].EventSeq < sources[j].EventSeq })
	if len(sources) == 2 && (sources[0].Speaker != "user" || sources[1].EventID != outputEvent.EventID) {
		return nil, ErrConversationProjectionConflict
	}
	return sources, nil
}

func loadPublicProjectionInput(ctx context.Context, tx *sql.Tx, eventID modulecore.EventID, actorID string) (MessageIndex, modulecore.EventEnvelope, error) {
	index, err := loadProjectionIndex(ctx, tx, eventID)
	if err != nil {
		return MessageIndex{}, modulecore.EventEnvelope{}, err
	}
	if index.Visibility != "public" {
		return MessageIndex{}, modulecore.EventEnvelope{}, ErrConversationProjectionConflict
	}
	event, err := loadEventEnvelopeTx(ctx, tx, eventID)
	if err != nil {
		return MessageIndex{}, modulecore.EventEnvelope{}, err
	}
	if err := validateMessageIndex(index, event, index.SessionID, index.TurnID); err != nil {
		return MessageIndex{}, modulecore.EventEnvelope{}, ErrConversationProjectionConflict
	}
	if event.EventType != "conversation.user_message" || event.ComponentID != "orchestrator" || index.Kind != "user_message" ||
		index.FromActor != actorID || index.ToKind != "agent" || index.ToActor != "mio" || payloadString(event.Payload, "from") != "user" ||
		payloadString(event.Payload, "source_actor_id") != actorID || payloadString(event.Payload, "to") != "mio" || strings.TrimSpace(projectionText(event.Payload)) == "" {
		return MessageIndex{}, modulecore.EventEnvelope{}, ErrConversationProjectionConflict
	}
	return index, event, nil
}

func projectionSource(index MessageIndex, event modulecore.EventEnvelope, speaker string) domconv.ConversationProjectionSource {
	return domconv.ConversationProjectionSource{
		SessionID: index.SessionID, TurnID: index.TurnID, MessageID: index.MessageID, EventID: index.EventID,
		EventSeq: event.EventSeq, ActorID: index.FromActor, Speaker: speaker, Text: projectionText(event.Payload), OccurredAt: event.OccurredAt.UTC(), Visibility: "public",
	}
}

func projectionText(payload map[string]any) string {
	text := payloadText(payload, "content")
	if text == "" {
		text = payloadText(payload, "text")
	}
	return text
}

func requirePriorPublicMessagesApplied(ctx context.Context, tx *sql.Tx, sessionID modulecore.SessionID, watermark, through modulecore.EventSeq, sourceMessageID modulecore.MessageID, workID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT m.message_id,m.kind,t.trigger_kind,e.event_seq FROM live_message_index m
		JOIN event_envelope e ON e.event_id=m.event_id
		JOIN live_conversation_turn t ON t.turn_id=m.turn_id AND t.session_id=m.session_id
		WHERE m.session_id=? AND m.visibility='public' AND e.event_seq>? AND e.event_seq<=? ORDER BY e.event_seq`,
		string(sessionID), int64(watermark), int64(through))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var messageID, kind, triggerKind string
		var seq int64
		if err := rows.Scan(&messageID, &kind, &triggerKind, &seq); err != nil {
			return err
		}
		if messageID == string(sourceMessageID) {
			continue
		}
		// A regular accepted input is projected with its own response. An
		// unrelated accepted input may arrive between another turn's input and
		// response, so it does not block that intact same-Turn pair. Clarification
		// replies have their own singleton work and remain ordering barriers.
		if kind == "user_message" && triggerKind == "user_message" {
			continue
		}
		var applied int
		var mappedWork string
		err := tx.QueryRowContext(ctx, `SELECT applied,work_id FROM live_conversation_projection_message WHERE message_id=?`, messageID).Scan(&applied, &mappedWork)
		if err == nil && mappedWork == workID {
			continue
		}
		if errors.Is(err, sql.ErrNoRows) || (err == nil && applied != 1) {
			return ErrConversationProjectionBlocked
		}
		if err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *SQLiteStore) persistProjectionAssignment(ctx context.Context, tx *sql.Tx, work LeasedWorkItem, sources []domconv.ConversationProjectionSource) ([]domconv.CanonicalConversationProjection, error) {
	items := make([]domconv.CanonicalConversationProjection, 0, len(sources))
	unmapped := make([]domconv.ConversationProjectionSource, 0, len(sources))
	for _, source := range sources {
		item, found, err := loadProjectionMapping(ctx, tx, source.MessageID)
		if err != nil {
			return nil, err
		}
		if !found {
			unmapped = append(unmapped, source)
			continue
		}
		if item.EventID != source.EventID || item.EventSeq != source.EventSeq || item.SessionID != source.SessionID || item.TurnID != source.TurnID || item.Speaker != source.Speaker || item.Text != source.Text {
			return nil, ErrConversationProjectionConflict
		}
		var mappedWork string
		if err := tx.QueryRowContext(ctx, `SELECT work_id FROM live_conversation_projection_message WHERE message_id=?`, string(source.MessageID)).Scan(&mappedWork); err != nil {
			return nil, err
		}
		if source.EventID == work.SourceEventID && mappedWork != work.WorkID {
			return nil, ErrConversationProjectionConflict
		}
		if mappedWork == work.WorkID {
			items = append(items, item)
		}
	}
	if len(unmapped) == 0 {
		if len(items) != len(sources) || len(items) == 0 {
			return nil, ErrConversationProjectionConflict
		}
		return items, nil
	}
	if len(items) != 0 {
		return nil, ErrConversationProjectionConflict
	}
	var active domconv.ConversationProjectionThread
	var rawID string
	err := tx.QueryRowContext(ctx, `SELECT thread_id,thread_seq,message_count FROM live_conversation_projection_thread WHERE session_id=? AND status='active'`, string(work.SessionID)).Scan(&rawID, &active.Sequence, &active.MessageCount)
	if err == nil {
		active.ID = modulecore.ThreadID(rawID)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var nextSequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(thread_seq),0)+1 FROM live_conversation_projection_thread WHERE session_id=?`, string(work.SessionID)).Scan(&nextSequence); err != nil {
		return nil, err
	}
	packed, err := domconv.PackConversationProjectionUnit(domconv.ConversationProjectionPackInput{
		Active: active, NextSequence: modulecore.ThreadSeq(nextSequence), NewThreadID: modulecore.NewThreadID(),
		ThreadKind: modulecore.ThreadKindUserConversation, Sources: unmapped, InitialPair: len(unmapped) == 2,
	})
	if err != nil {
		return nil, err
	}
	if packed.Closed != nil && packed.Closed.ID == active.ID {
		if _, err := tx.ExecContext(ctx, `UPDATE live_conversation_projection_thread SET message_count=?,status='closed' WHERE thread_id=? AND status='active'`, packed.Closed.MessageCount, string(active.ID)); err != nil {
			return nil, err
		}
	}
	if packed.Active.ID != "" && packed.Active.ID != active.ID {
		status := "active"
		if packed.Closed != nil && packed.Closed.ID == packed.Active.ID {
			status = "closed"
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO live_conversation_projection_thread(session_id,thread_id,thread_seq,message_count,status) VALUES(?,?,?,?,?)`,
			string(work.SessionID), string(packed.Active.ID), int64(packed.Active.Sequence), packed.Active.MessageCount, status); err != nil {
			return nil, err
		}
	} else if packed.Active.ID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE live_conversation_projection_thread SET message_count=? WHERE thread_id=? AND status='active'`, packed.Active.MessageCount, string(packed.Active.ID)); err != nil {
			return nil, err
		}
	} else if packed.Closed != nil && packed.Closed.ID != active.ID {
		if _, err := tx.ExecContext(ctx, `INSERT INTO live_conversation_projection_thread(session_id,thread_id,thread_seq,message_count,status) VALUES(?,?,?,?, 'closed')`,
			string(work.SessionID), string(packed.Closed.ID), int64(packed.Closed.Sequence), packed.Closed.MessageCount); err != nil {
			return nil, err
		}
	}
	for _, item := range packed.Items {
		if _, err := tx.ExecContext(ctx, `INSERT INTO live_conversation_projection_message
			(message_id,event_id,session_id,turn_id,event_seq,thread_id,thread_seq,thread_kind,content_sha256,work_id,applied)
			VALUES(?,?,?,?,?,?,?,?,?,?,0)`, string(item.MessageID), string(item.EventID), string(item.SessionID), string(item.TurnID), int64(item.EventSeq),
			string(item.ThreadID), int64(item.ThreadSeq), string(item.ThreadKind), item.ContentSHA256, work.WorkID); err != nil {
			return nil, err
		}
	}
	return packed.Items, nil
}

func loadProjectionMapping(ctx context.Context, tx *sql.Tx, messageID modulecore.MessageID) (domconv.CanonicalConversationProjection, bool, error) {
	var item domconv.CanonicalConversationProjection
	var eventID, sessionID, turnID, threadID, threadKind string
	var eventSeq, threadSeq int64
	err := tx.QueryRowContext(ctx, `SELECT message_id,event_id,session_id,turn_id,event_seq,thread_id,thread_seq,thread_kind,content_sha256
		FROM live_conversation_projection_message WHERE message_id=?`, string(messageID)).Scan(
		&item.MessageID, &eventID, &sessionID, &turnID, &eventSeq, &threadID, &threadSeq, &threadKind, &item.ContentSHA256,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domconv.CanonicalConversationProjection{}, false, nil
	}
	if err != nil {
		return domconv.CanonicalConversationProjection{}, false, err
	}
	item.EventID, item.SessionID, item.TurnID, item.EventSeq = modulecore.EventID(eventID), modulecore.SessionID(sessionID), modulecore.TurnID(turnID), modulecore.EventSeq(eventSeq)
	item.ThreadID, item.ThreadSeq, item.ThreadKind = modulecore.ThreadID(threadID), modulecore.ThreadSeq(threadSeq), modulecore.ThreadKind(threadKind)
	var event modulecore.EventEnvelope
	event, err = loadEventEnvelopeTx(ctx, tx, item.EventID)
	if err != nil {
		return domconv.CanonicalConversationProjection{}, false, err
	}
	index, err := loadProjectionIndex(ctx, tx, item.EventID)
	if err != nil {
		return domconv.CanonicalConversationProjection{}, false, err
	}
	item.ActorID, item.Speaker, item.Text, item.OccurredAt, item.Visibility = index.FromActor, index.FromActor, projectionText(event.Payload), event.OccurredAt.UTC(), index.Visibility
	if index.Kind == "user_message" {
		item.Speaker = "user"
	}
	hash, err := domconv.ConversationProjectionContentHash(item)
	if err != nil || hash != item.ContentSHA256 {
		return domconv.CanonicalConversationProjection{}, false, ErrConversationProjectionConflict
	}
	return item, true, nil
}

func verifyProjectionApplyReceipt(ctx context.Context, tx *sql.Tx, work LeasedWorkItem, receipt domconv.ConversationProjectionApplyReceipt) error {
	if receipt.SessionID != work.SessionID || len(receipt.Items) == 0 {
		return ErrConversationProjectionConflict
	}
	rows, err := tx.QueryContext(ctx, `SELECT message_id,event_id,event_seq,content_sha256 FROM live_conversation_projection_message WHERE work_id=? ORDER BY event_seq`, work.WorkID)
	if err != nil {
		return err
	}
	defer rows.Close()
	index := 0
	for rows.Next() {
		var messageID, eventID, hash string
		var seq int64
		if err := rows.Scan(&messageID, &eventID, &seq, &hash); err != nil {
			return err
		}
		if index >= len(receipt.Items) || receipt.Items[index] != (domconv.ConversationProjectionItemReceipt{
			MessageID: modulecore.MessageID(messageID), EventID: modulecore.EventID(eventID), EventSeq: modulecore.EventSeq(seq), ContentSHA256: hash,
		}) {
			return ErrConversationProjectionConflict
		}
		index++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if index == 0 || index != len(receipt.Items) {
		return ErrConversationProjectionConflict
	}
	return nil
}

func requireProjectionRangeAccounted(ctx context.Context, tx *sql.Tx, sessionID modulecore.SessionID, watermark, through modulecore.EventSeq, workID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT m.message_id,m.kind,t.trigger_kind FROM live_message_index m JOIN event_envelope e ON e.event_id=m.event_id
		JOIN live_conversation_turn t ON t.turn_id=m.turn_id AND t.session_id=m.session_id
		WHERE m.session_id=? AND m.visibility='public' AND e.event_seq>? AND e.event_seq<=? ORDER BY e.event_seq`,
		string(sessionID), int64(watermark), int64(through))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var messageID, kind, triggerKind string
		if err := rows.Scan(&messageID, &kind, &triggerKind); err != nil {
			return err
		}
		var applied int
		var mappedWork string
		err := tx.QueryRowContext(ctx, `SELECT applied,work_id FROM live_conversation_projection_message WHERE message_id=?`, messageID).Scan(&applied, &mappedWork)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && !(applied == 1 || mappedWork == workID)) {
			if kind == "user_message" && triggerKind == "user_message" {
				continue
			}
			return ErrConversationProjectionBlocked
		}
		if err != nil {
			return err
		}
	}
	return rows.Err()
}

func contiguousProjectionWatermark(ctx context.Context, tx *sql.Tx, sessionID modulecore.SessionID, watermark modulecore.EventSeq) (modulecore.EventSeq, error) {
	rows, err := tx.QueryContext(ctx, `SELECT m.message_id,e.event_seq FROM live_message_index m
		JOIN event_envelope e ON e.event_id=m.event_id
		WHERE m.session_id=? AND m.visibility='public' AND e.event_seq>? ORDER BY e.event_seq`, string(sessionID), int64(watermark))
	if err != nil {
		return watermark, err
	}
	defer rows.Close()
	contiguous := watermark
	for rows.Next() {
		var messageID string
		var seq int64
		if err := rows.Scan(&messageID, &seq); err != nil {
			return watermark, err
		}
		var applied int
		err := tx.QueryRowContext(ctx, `SELECT applied FROM live_conversation_projection_message WHERE message_id=?`, messageID).Scan(&applied)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && applied != 1) {
			break
		}
		if err != nil {
			return watermark, err
		}
		contiguous = modulecore.EventSeq(seq)
	}
	if err := rows.Err(); err != nil {
		return watermark, err
	}
	return contiguous, nil
}

func requireProjectionWorkApplied(ctx context.Context, tx *sql.Tx, workID string, expected int) error {
	var total, applied int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(applied),0) FROM live_conversation_projection_message WHERE work_id=?`, workID).Scan(&total, &applied); err != nil {
		return err
	}
	if expected < 1 || total != expected || applied != expected {
		return ErrConversationProjectionConflict
	}
	return nil
}
