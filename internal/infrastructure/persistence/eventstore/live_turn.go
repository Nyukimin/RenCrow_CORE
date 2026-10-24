package eventstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var (
	ErrOperationConflict           = errors.New("operation id conflicts with a different committed request")
	ErrClientSubmissionConflict    = errors.New("client submission id conflicts with a different accepted input")
	ErrConversationSessionConflict = errors.New("conversation session owner, runtime version, or epoch conflicts")
	ErrConversationTurnConflict    = errors.New("conversation turn is not in a state that accepts this operation")
	ErrClarificationReplyConflict  = errors.New("clarification reply prompt or delegation revision conflicts with current state")
)

// AcceptInput is the trusted runtime's minimum durable input-acceptance unit.
// OperationID and ClientSubmissionID must be stable across HTTP retries.
type AcceptInput struct {
	OperationID        string
	OwnerID            string
	RuntimeVersion     string
	ClientSubmissionID string
	ActorID            string
	TriggerKind        string
	GenerationEpoch    int64
	// CreateNewSession is a trusted assertion that SessionID was freshly
	// minted for this request. Existing v1 sessions cannot be auto-cut over.
	CreateNewSession bool
	InputEvent       modulecore.EventEnvelope
	InputMessage     MessageIndex
}

// AcceptedTurn is the persisted input and the durable identity needed to
// resume generation after a process restart.
type AcceptedTurn struct {
	OperationID        string                     `json:"operation_id"`
	ClientSubmissionID string                     `json:"client_submission_id"`
	SessionID          modulecore.SessionID       `json:"session_id"`
	TurnID             modulecore.TurnID          `json:"turn_id"`
	ActorID            string                     `json:"actor_id"`
	TriggerKind        string                     `json:"trigger_kind"`
	InputMessageID     modulecore.MessageID       `json:"input_message_id"`
	AdmissionSeq       modulecore.EventSeq        `json:"admission_seq"`
	GenerationEpoch    int64                      `json:"generation_epoch"`
	State              string                     `json:"state"`
	InputEvent         modulecore.EventEnvelope   `json:"input_event"`
	ClarificationReply *ClarificationReplyReceipt `json:"clarification_reply,omitempty"`
}

// ClarificationReplyReceipt is the durable, single-consumer binding between a
// public Mio clarification prompt and the authenticated user's accepted
// answer. The source turn, delegation, and revision are derived from stored
// private question and handoff records.
type ClarificationReplyReceipt struct {
	PromptMessageID    modulecore.MessageID `json:"prompt_message_id"`
	SourceTurnID       modulecore.TurnID    `json:"source_turn_id"`
	DelegationID       modulecore.RequestID `json:"delegation_id"`
	Revision           uint64               `json:"revision"`
	ReplyTurnID        modulecore.TurnID    `json:"reply_turn_id"`
	ReplyMessageID     modulecore.MessageID `json:"reply_message_id"`
	OwnerID            string               `json:"owner_id"`
	SessionID          modulecore.SessionID `json:"session_id"`
	ClientSubmissionID string               `json:"client_submission_id"`
	SubmissionSHA256   string               `json:"submission_sha256"`
}

// MessageIndex contains routing and visibility metadata only. Message bodies
// remain in the canonical event envelope.
type MessageIndex struct {
	MessageID        modulecore.MessageID `json:"message_id"`
	EventID          modulecore.EventID   `json:"event_id"`
	SessionID        modulecore.SessionID `json:"session_id"`
	TurnID           modulecore.TurnID    `json:"turn_id"`
	FromActor        string               `json:"from_actor"`
	ToKind           string               `json:"to_kind"`
	ToActor          string               `json:"to_actor"`
	Kind             string               `json:"kind"`
	ReplyToMessageID modulecore.MessageID `json:"reply_to_message_id,omitempty"`
	Visibility       string               `json:"visibility"`
}

// CommitInput is one complete result bundle. Events are supplied with
// EventSeq zero; the store assigns their sequences and persists the events,
// indexes, optional delegation, outbox work, turn state, and receipt together.
type CommitInput struct {
	OperationID     string                     `json:"operation_id,omitempty"`
	SessionID       modulecore.SessionID       `json:"session_id"`
	TurnID          modulecore.TurnID          `json:"turn_id"`
	GenerationEpoch int64                      `json:"generation_epoch"`
	ResultState     string                     `json:"result_state"`
	Events          []modulecore.EventEnvelope `json:"events"`
	MessageIndexes  []MessageIndex             `json:"message_indexes,omitempty"`
	Delegation      *DelegationRecord          `json:"delegation,omitempty"`
	Outbox          []WorkItem                 `json:"outbox,omitempty"`
}

// DelegationRecord is the minimum durable handoff state. The instruction
// event must be private and stored outside the orchestrator component.
type DelegationRecord struct {
	DelegationID         string               `json:"delegation_id"`
	Revision             int64                `json:"revision"`
	OriginTurnID         modulecore.TurnID    `json:"origin_turn_id"`
	Assignee             string               `json:"assignee"`
	InstructionMessageID modulecore.MessageID `json:"instruction_message_id"`
	ContextEventID       modulecore.EventID   `json:"context_event_id"`
	State                string               `json:"state"`
	WorkTaskID           string               `json:"work_task_id,omitempty"`
	ReportRevision       int64                `json:"report_revision"`
}

// WorkItem is a durable request for later delivery or projection. The outbox
// is only an acceptance queue; it does not claim that the work was completed.
type WorkItem struct {
	WorkID        string               `json:"work_id"`
	DedupeKey     string               `json:"dedupe_key"`
	Kind          string               `json:"kind"`
	SourceEventID modulecore.EventID   `json:"source_event_id"`
	SessionID     modulecore.SessionID `json:"session_id"`
	Priority      int                  `json:"priority"`
	NotBefore     time.Time            `json:"not_before,omitempty"`
}

// CommittedBatch contains the canonical envelopes exactly as persisted, in
// event_seq order. A publish-only relay can use these without appending again.
type CommittedBatch struct {
	OperationID string                     `json:"operation_id"`
	SessionID   modulecore.SessionID       `json:"session_id"`
	TurnID      modulecore.TurnID          `json:"turn_id"`
	ResultState string                     `json:"result_state"`
	Events      []modulecore.EventEnvelope `json:"events"`
}

func (s *SQLiteStore) migrateLiveJournal() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	statements := liveJournalSchemaStatements()
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func liveJournalSchemaStatements() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS live_conversation_session (
			session_id TEXT PRIMARY KEY NOT NULL,
			owner_id TEXT NOT NULL,
			runtime_version TEXT NOT NULL,
			cutover_seq INTEGER NOT NULL DEFAULT 0,
			admission_seq INTEGER NOT NULL DEFAULT 0,
			projection_watermark INTEGER NOT NULL DEFAULT 0,
			epoch INTEGER NOT NULL DEFAULT 1
		)`,
		`CREATE TABLE IF NOT EXISTS live_conversation_turn (
			turn_id TEXT PRIMARY KEY NOT NULL,
			session_id TEXT NOT NULL,
			owner_id TEXT NOT NULL,
			actor_id TEXT NOT NULL,
			trigger_kind TEXT NOT NULL,
			client_submission_id TEXT NOT NULL,
			submission_sha256 TEXT NOT NULL,
			accept_receipt_id TEXT NOT NULL,
			input_message_id TEXT NOT NULL,
			admission_seq INTEGER NOT NULL,
			state TEXT NOT NULL,
			generation_epoch INTEGER NOT NULL,
			source_turn_id TEXT,
			delegation_id TEXT,
			result_receipt_id TEXT,
			UNIQUE (owner_id, session_id, client_submission_id),
			FOREIGN KEY (session_id) REFERENCES live_conversation_session(session_id) ON UPDATE RESTRICT ON DELETE RESTRICT,
			FOREIGN KEY (input_message_id) REFERENCES live_message_index(message_id) ON UPDATE RESTRICT ON DELETE RESTRICT
		)`,
		`CREATE TABLE IF NOT EXISTS live_message_index (
			message_id TEXT PRIMARY KEY NOT NULL,
			event_id TEXT NOT NULL UNIQUE,
			session_id TEXT NOT NULL,
			turn_id TEXT NOT NULL,
			from_actor TEXT NOT NULL,
			to_kind TEXT NOT NULL,
			to_actor TEXT NOT NULL,
			kind TEXT NOT NULL,
			reply_to_message_id TEXT,
			visibility TEXT NOT NULL CHECK (visibility IN ('public', 'private')),
			FOREIGN KEY (event_id) REFERENCES event_envelope(event_id) ON UPDATE RESTRICT ON DELETE RESTRICT,
			FOREIGN KEY (session_id) REFERENCES live_conversation_session(session_id) ON UPDATE RESTRICT ON DELETE RESTRICT
		)`,
		`CREATE TABLE IF NOT EXISTS live_delegation (
			delegation_id TEXT NOT NULL,
			revision INTEGER NOT NULL,
			origin_turn_id TEXT NOT NULL,
			assignee TEXT NOT NULL,
			instruction_message_id TEXT NOT NULL,
			context_event_id TEXT NOT NULL,
			state TEXT NOT NULL,
			work_task_id TEXT,
			report_revision INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (delegation_id, revision),
			FOREIGN KEY (origin_turn_id) REFERENCES live_conversation_turn(turn_id) ON UPDATE RESTRICT ON DELETE RESTRICT,
			FOREIGN KEY (instruction_message_id) REFERENCES live_message_index(message_id) ON UPDATE RESTRICT ON DELETE RESTRICT,
			FOREIGN KEY (context_event_id) REFERENCES event_envelope(event_id) ON UPDATE RESTRICT ON DELETE RESTRICT
		)`,
		`CREATE TABLE IF NOT EXISTS live_clarification_reply_receipt (
			prompt_message_id TEXT PRIMARY KEY NOT NULL,
			owner_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			source_turn_id TEXT NOT NULL,
			delegation_id TEXT NOT NULL,
			revision INTEGER NOT NULL CHECK (revision > 0),
			reply_turn_id TEXT NOT NULL UNIQUE,
			reply_message_id TEXT NOT NULL UNIQUE,
			client_submission_id TEXT NOT NULL,
			submission_sha256 TEXT NOT NULL CHECK (length(submission_sha256) = 64),
			created_at TEXT NOT NULL,
			FOREIGN KEY (prompt_message_id) REFERENCES live_message_index(message_id) ON UPDATE RESTRICT ON DELETE RESTRICT,
			FOREIGN KEY (source_turn_id) REFERENCES live_conversation_turn(turn_id) ON UPDATE RESTRICT ON DELETE RESTRICT,
			FOREIGN KEY (delegation_id, revision) REFERENCES live_delegation(delegation_id, revision) ON UPDATE RESTRICT ON DELETE RESTRICT,
			FOREIGN KEY (reply_turn_id) REFERENCES live_conversation_turn(turn_id) ON UPDATE RESTRICT ON DELETE RESTRICT,
			FOREIGN KEY (reply_message_id) REFERENCES live_message_index(message_id) ON UPDATE RESTRICT ON DELETE RESTRICT,
			FOREIGN KEY (session_id) REFERENCES live_conversation_session(session_id) ON UPDATE RESTRICT ON DELETE RESTRICT
		)`,
		`CREATE TABLE IF NOT EXISTS live_work_outbox (
			work_id TEXT PRIMARY KEY NOT NULL,
			dedupe_key TEXT NOT NULL UNIQUE,
			kind TEXT NOT NULL,
			source_event_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			priority INTEGER NOT NULL,
			state TEXT NOT NULL DEFAULT 'pending',
			attempt INTEGER NOT NULL DEFAULT 0,
			not_before TEXT NOT NULL DEFAULT '',
			lease_token TEXT,
			lease_expires_at TEXT,
			version INTEGER NOT NULL DEFAULT 1,
			FOREIGN KEY (source_event_id) REFERENCES event_envelope(event_id) ON UPDATE RESTRICT ON DELETE RESTRICT,
			FOREIGN KEY (session_id) REFERENCES live_conversation_session(session_id) ON UPDATE RESTRICT ON DELETE RESTRICT
		)`,
		`CREATE TABLE IF NOT EXISTS live_operation_receipt (
			operation_id TEXT PRIMARY KEY NOT NULL,
			operation_kind TEXT NOT NULL,
			input_sha256 TEXT NOT NULL,
			result_json TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS live_conversation_projection_delivery (
			event_id TEXT PRIMARY KEY NOT NULL,
			work_id TEXT NOT NULL UNIQUE,
		session_id TEXT NOT NULL,
			owner_id TEXT NOT NULL,
			attempted_at TEXT NOT NULL,
			FOREIGN KEY (event_id) REFERENCES event_envelope(event_id) ON UPDATE RESTRICT ON DELETE RESTRICT,
			FOREIGN KEY (work_id) REFERENCES live_work_outbox(work_id) ON UPDATE RESTRICT ON DELETE RESTRICT,
			FOREIGN KEY (session_id) REFERENCES live_conversation_session(session_id) ON UPDATE RESTRICT ON DELETE RESTRICT
		)`,
		`CREATE TABLE IF NOT EXISTS live_conversation_projection_thread (
			session_id TEXT NOT NULL,
			thread_id TEXT PRIMARY KEY NOT NULL,
			thread_seq INTEGER NOT NULL,
			message_count INTEGER NOT NULL CHECK(message_count BETWEEN 1 AND 12),
			status TEXT NOT NULL CHECK(status IN ('active','closed')),
			UNIQUE (session_id, thread_id),
			UNIQUE (session_id, thread_seq),
			FOREIGN KEY (session_id) REFERENCES live_conversation_session(session_id) ON UPDATE RESTRICT ON DELETE RESTRICT
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS live_conversation_projection_one_active_thread
			ON live_conversation_projection_thread(session_id) WHERE status='active'`,
		`CREATE TABLE IF NOT EXISTS live_conversation_projection_message (
			message_id TEXT PRIMARY KEY NOT NULL,
			event_id TEXT NOT NULL UNIQUE,
			session_id TEXT NOT NULL,
			turn_id TEXT NOT NULL,
			event_seq INTEGER NOT NULL UNIQUE,
			thread_id TEXT NOT NULL,
			thread_seq INTEGER NOT NULL,
			thread_kind TEXT NOT NULL,
			content_sha256 TEXT NOT NULL CHECK(length(content_sha256)=64),
			work_id TEXT NOT NULL,
			applied INTEGER NOT NULL DEFAULT 0 CHECK(applied IN (0,1)),
			UNIQUE (thread_id, thread_seq),
			FOREIGN KEY (event_id) REFERENCES event_envelope(event_id) ON UPDATE RESTRICT ON DELETE RESTRICT,
			FOREIGN KEY (session_id) REFERENCES live_conversation_session(session_id) ON UPDATE RESTRICT ON DELETE RESTRICT,
			FOREIGN KEY (thread_id) REFERENCES live_conversation_projection_thread(thread_id) ON UPDATE RESTRICT ON DELETE RESTRICT,
			FOREIGN KEY (work_id) REFERENCES live_work_outbox(work_id) ON UPDATE RESTRICT ON DELETE RESTRICT
		)`,
		`CREATE INDEX IF NOT EXISTS live_conversation_projection_message_progress
			ON live_conversation_projection_message(session_id, event_seq, applied)`,
		`CREATE INDEX IF NOT EXISTS live_message_session_idx
			ON live_message_index (session_id, turn_id, message_id)`,
		`CREATE INDEX IF NOT EXISTS live_work_outbox_state_idx
			ON live_work_outbox (state, priority, not_before)`,
		`CREATE TRIGGER IF NOT EXISTS live_operation_receipt_append_only_update
			BEFORE UPDATE ON live_operation_receipt
			BEGIN
				SELECT RAISE(ABORT, 'live_operation_receipt is append-only');
			END`,
		`CREATE TRIGGER IF NOT EXISTS live_operation_receipt_append_only_delete
			BEFORE DELETE ON live_operation_receipt
			BEGIN
				SELECT RAISE(ABORT, 'live_operation_receipt is append-only');
			END`,
		`CREATE TRIGGER IF NOT EXISTS live_clarification_reply_receipt_append_only_update
			BEFORE UPDATE ON live_clarification_reply_receipt
			BEGIN
				SELECT RAISE(ABORT, 'live_clarification_reply_receipt is append-only');
			END`,
		`CREATE TRIGGER IF NOT EXISTS live_clarification_reply_receipt_append_only_delete
			BEFORE DELETE ON live_clarification_reply_receipt
			BEGIN
				SELECT RAISE(ABORT, 'live_clarification_reply_receipt is append-only');
			END`,
	}
}

// LiveJournalTableNames returns the table names owned by the current live
// Event Store journal schema. The returned slice is independent of the owner
// schema descriptor.
func LiveJournalTableNames() []string {
	return liveJournalTableNamesFromStatements(liveJournalSchemaStatements())
}

// ValidateLiveJournalSchema compares the live journal tables, indexes, and
// triggers against the exact DDL used by the Event Store owner. It only reads
// the supplied database; the expected schema is built in a private in-memory
// SQLite database from the same statements used by migrateLiveJournal.
func ValidateLiveJournalSchema(ctx context.Context, db *sql.DB) error {
	if ctx == nil || db == nil {
		return errors.New("live journal schema validation requires context and database")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	tableNames := LiveJournalTableNames()
	if len(tableNames) == 0 {
		return errors.New("Event Store owner has no live journal table definitions")
	}
	expectedDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return fmt.Errorf("open expected live journal schema: %w", err)
	}
	expectedDB.SetMaxOpenConns(1)
	expectedDB.SetMaxIdleConns(1)
	defer expectedDB.Close()
	for _, statement := range liveJournalSchemaStatements() {
		if _, err := expectedDB.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("build expected live journal schema: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateLiveJournalTableSet(ctx, db, tableNames); err != nil {
		return err
	}
	expected, err := liveJournalSchemaObjects(ctx, expectedDB)
	if err != nil {
		return fmt.Errorf("inspect expected live journal schema: %w", err)
	}
	actual, err := liveJournalSchemaObjects(ctx, db)
	if err != nil {
		return fmt.Errorf("inspect live journal schema: %w", err)
	}
	if len(expected) != len(actual) {
		return errors.New("live journal schema objects do not match Event Store owner schema")
	}
	for index := range expected {
		if expected[index] != actual[index] {
			return errors.New("live journal schema objects do not match Event Store owner schema")
		}
	}
	return nil
}

type liveJournalSchemaObject struct {
	typeName string
	name     string
	table    string
	sql      string
}

func liveJournalTableNamesFromStatements(statements []string) []string {
	const createTablePrefix = "CREATE TABLE IF NOT EXISTS "
	tableNames := make([]string, 0, len(statements))
	for _, statement := range statements {
		statement = strings.TrimSpace(statement)
		if !strings.HasPrefix(statement, createTablePrefix) {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(statement, createTablePrefix))
		if len(fields) > 0 {
			tableNames = append(tableNames, fields[0])
		}
	}
	sort.Strings(tableNames)
	return tableNames
}

func validateLiveJournalTableSet(ctx context.Context, db *sql.DB, tableNames []string) error {
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name NOT IN ('event_envelope','event_dependency') ORDER BY name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	index := 0
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		if index >= len(tableNames) || name != tableNames[index] {
			return errors.New("live journal table set does not match Event Store owner schema")
		}
		index++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if index != len(tableNames) {
		return errors.New("live journal table set does not match Event Store owner schema")
	}
	return nil
}

func liveJournalSchemaObjects(ctx context.Context, db *sql.DB) ([]liveJournalSchemaObject, error) {
	rows, err := db.QueryContext(ctx, `SELECT type, name, tbl_name, COALESCE(sql, '') FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	objects := make([]liveJournalSchemaObject, 0)
	for rows.Next() {
		var object liveJournalSchemaObject
		if err := rows.Scan(&object.typeName, &object.name, &object.table, &object.sql); err != nil {
			return nil, err
		}
		if object.name == "event_envelope" || object.name == "event_dependency" || object.table == "event_envelope" || object.table == "event_dependency" {
			continue
		}
		objects = append(objects, object)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return objects, nil
}

// AcceptTurn persists the input envelope, its turn/session index and the
// operation receipt atomically. It is safe to retry after an unknown commit
// result using the same OperationID and ClientSubmissionID.
func (s *SQLiteStore) AcceptTurn(ctx context.Context, input AcceptInput) (AcceptedTurn, error) {
	if err := s.ensureOpen(); err != nil {
		return AcceptedTurn{}, err
	}
	if ctx == nil {
		return AcceptedTurn{}, errors.New("accept turn context is required")
	}
	if err := validateAcceptInput(input); err != nil {
		return AcceptedTurn{}, err
	}
	inputHash, err := acceptInputHash(input)
	if err != nil {
		return AcceptedTurn{}, err
	}
	if s.shouldQueueWrite(ctx) {
		queuedInput := cloneQueuedValue(input)
		return enqueueOrderedWriteResult(s, ctx, 1, queuedInput, func(commandCtx context.Context) (AcceptedTurn, error) {
			return s.AcceptTurn(commandCtx, queuedInput)
		})
	}
	tx, unlockSequence, err := s.beginSequenceTx(ctx)
	if err != nil {
		return AcceptedTurn{}, err
	}
	defer unlockSequence()
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if receipt, found, err := readOperationReceipt(ctx, tx, input.OperationID); err != nil {
		return AcceptedTurn{}, err
	} else if found {
		if receipt.operationKind != "accept_turn" || receipt.inputSHA256 != inputHash {
			return AcceptedTurn{}, ErrOperationConflict
		}
		var accepted AcceptedTurn
		if err := json.Unmarshal([]byte(receipt.resultJSON), &accepted); err != nil {
			return AcceptedTurn{}, fmt.Errorf("decode accepted turn receipt: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return AcceptedTurn{}, err
		}
		committed = true
		s.recordSessionProtocolPin(input.InputEvent.SessionID, input.OwnerID, input.RuntimeVersion)
		return accepted, nil
	}

	var existingTurnID, existingHash, acceptanceReceiptID string
	err = tx.QueryRowContext(ctx, `
		SELECT turn_id, submission_sha256, accept_receipt_id
		FROM live_conversation_turn
		WHERE owner_id = ? AND session_id = ? AND client_submission_id = ?`,
		input.OwnerID, string(input.InputEvent.SessionID), input.ClientSubmissionID,
	).Scan(&existingTurnID, &existingHash, &acceptanceReceiptID)
	if err == nil {
		if existingHash != inputHash {
			return AcceptedTurn{}, ErrClientSubmissionConflict
		}
		var accepted AcceptedTurn
		var resultJSON string
		if err := tx.QueryRowContext(ctx, `SELECT result_json FROM live_operation_receipt WHERE operation_id = ?`, acceptanceReceiptID).Scan(&resultJSON); err != nil {
			return AcceptedTurn{}, fmt.Errorf("read acceptance receipt for turn %q: %w", existingTurnID, err)
		}
		if err := json.Unmarshal([]byte(resultJSON), &accepted); err != nil {
			return AcceptedTurn{}, fmt.Errorf("decode acceptance receipt for turn %q: %w", existingTurnID, err)
		}
		if err := insertOperationReceipt(ctx, tx, input.OperationID, "accept_turn", inputHash, accepted); err != nil {
			return AcceptedTurn{}, err
		}
		if err := tx.Commit(); err != nil {
			return AcceptedTurn{}, err
		}
		committed = true
		s.recordSessionProtocolPin(input.InputEvent.SessionID, input.OwnerID, input.RuntimeVersion)
		return accepted, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return AcceptedTurn{}, err
	}

	var ownerID, runtimeVersion string
	var epoch int64
	err = tx.QueryRowContext(ctx, `SELECT owner_id, runtime_version, epoch FROM live_conversation_session WHERE session_id = ?`, string(input.InputEvent.SessionID)).Scan(&ownerID, &runtimeVersion, &epoch)
	sessionExists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return AcceptedTurn{}, err
	}
	if sessionExists && (ownerID != input.OwnerID || runtimeVersion != input.RuntimeVersion || epoch != input.GenerationEpoch) {
		return AcceptedTurn{}, ErrConversationSessionConflict
	}
	if sessionExists && input.CreateNewSession {
		return AcceptedTurn{}, ErrConversationSessionConflict
	}
	if !sessionExists {
		if !input.CreateNewSession {
			return AcceptedTurn{}, ErrConversationSessionConflict
		}
		var existingEvents int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM event_envelope
			WHERE json_extract(envelope_json, '$.session_id') = ?`, string(input.InputEvent.SessionID)).Scan(&existingEvents); err != nil {
			return AcceptedTurn{}, err
		}
		if existingEvents != 0 {
			return AcceptedTurn{}, ErrConversationSessionConflict
		}
	}
	var clarificationReply *ClarificationReplyReceipt
	if input.TriggerKind == "clarification_reply" {
		receipt, err := validateClarificationReplyTarget(ctx, tx, input)
		if err != nil {
			return AcceptedTurn{}, err
		}
		receipt.SubmissionSHA256 = inputHash
		clarificationReply = &receipt
	} else if err := validateReplyTarget(ctx, tx, input.InputMessage.ReplyToMessageID, input.InputEvent.SessionID); err != nil {
		return AcceptedTurn{}, err
	}

	event := input.InputEvent
	event.EventSeq, err = s.nextSequence(ctx)
	if err != nil {
		return AcceptedTurn{}, err
	}
	if err := ensureEventSeqAvailable(ctx, tx, event.EventSeq); err != nil {
		return AcceptedTurn{}, err
	}
	if err := ensureEventIDAbsent(ctx, tx, event.EventID); err != nil {
		return AcceptedTurn{}, err
	}
	if err := s.validateReferences(ctx, tx, event); err != nil {
		return AcceptedTurn{}, err
	}
	if !sessionExists {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO live_conversation_session
				(session_id, owner_id, runtime_version, cutover_seq, admission_seq, projection_watermark, epoch)
			VALUES (?, ?, ?, 0, ?, 0, ?)`,
			string(event.SessionID), input.OwnerID, input.RuntimeVersion, int64(event.EventSeq), input.GenerationEpoch,
		); err != nil {
			return AcceptedTurn{}, err
		}
	}
	event, err = insertLiveEvent(ctx, tx, event)
	if err != nil {
		return AcceptedTurn{}, err
	}
	if err := insertMessageIndex(ctx, tx, input.InputMessage); err != nil {
		return AcceptedTurn{}, err
	}
	accepted := AcceptedTurn{
		OperationID:        input.OperationID,
		ClientSubmissionID: input.ClientSubmissionID,
		SessionID:          event.SessionID,
		TurnID:             event.TurnID,
		ActorID:            input.ActorID,
		TriggerKind:        input.TriggerKind,
		InputMessageID:     event.MessageID,
		AdmissionSeq:       event.EventSeq,
		GenerationEpoch:    input.GenerationEpoch,
		State:              "accepted",
		InputEvent:         event,
		ClarificationReply: clarificationReply,
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO live_conversation_turn
			(turn_id, session_id, owner_id, actor_id, trigger_kind, client_submission_id,
			 submission_sha256, accept_receipt_id, input_message_id, admission_seq, state, generation_epoch, source_turn_id, delegation_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'accepted', ?, ?, ?)`,
		string(event.TurnID), string(event.SessionID), input.OwnerID, input.ActorID, input.TriggerKind,
		input.ClientSubmissionID, inputHash, input.OperationID, string(event.MessageID), int64(event.EventSeq), input.GenerationEpoch,
		nullableClarificationReplySourceTurn(clarificationReply), nullableClarificationReplyDelegation(clarificationReply),
	); err != nil {
		return AcceptedTurn{}, err
	}
	if clarificationReply != nil {
		if err := insertClarificationReplyReceipt(ctx, tx, *clarificationReply); err != nil {
			return AcceptedTurn{}, err
		}
		if err := insertClarificationReplyProjectionWork(ctx, tx, event, input.InputMessage, *clarificationReply); err != nil {
			return AcceptedTurn{}, err
		}
	}
	if sessionExists {
		if _, err := tx.ExecContext(ctx, `UPDATE live_conversation_session SET admission_seq = ? WHERE session_id = ?`, int64(event.EventSeq), string(event.SessionID)); err != nil {
			return AcceptedTurn{}, err
		}
	}
	if err := insertOperationReceipt(ctx, tx, input.OperationID, "accept_turn", inputHash, accepted); err != nil {
		return AcceptedTurn{}, err
	}
	if err := tx.Commit(); err != nil {
		return AcceptedTurn{}, err
	}
	committed = true
	s.recordSessionProtocolPin(input.InputEvent.SessionID, input.OwnerID, input.RuntimeVersion)
	s.observeCommittedOrchestratorEvent(accepted.InputEvent)
	return accepted, nil
}

// CommitTurnBundle persists one output unit and returns only after the full
// event/index/state/outbox/receipt transaction commits.
func (s *SQLiteStore) CommitTurnBundle(ctx context.Context, input CommitInput) (CommittedBatch, error) {
	if s.shouldQueueWrite(ctx) {
		if err := s.ensureOpen(); err != nil {
			return CommittedBatch{}, err
		}
		if ctx == nil {
			return CommittedBatch{}, errors.New("commit turn context is required")
		}
		if err := validateCommitInput(input); err != nil {
			return CommittedBatch{}, err
		}
		if _, err := commitInputHash(input); err != nil {
			return CommittedBatch{}, err
		}
		queuedInput := cloneCommitInput(input)
		sizeBytes, err := queuedWriteSize(queuedInput)
		if err != nil {
			return CommittedBatch{}, fmt.Errorf("size queued turn bundle: %w", err)
		}
		var committed CommittedBatch
		err = s.enqueueOrderedWrite(ctx, len(queuedInput.Events), sizeBytes, func(commandCtx context.Context) error {
			var commitErr error
			committed, commitErr = s.commitTurnBundleInline(commandCtx, queuedInput)
			return commitErr
		})
		if err != nil {
			return CommittedBatch{}, err
		}
		return committed, nil
	}
	return s.commitTurnBundleInline(ctx, input)
}

func cloneCommitInput(input CommitInput) CommitInput {
	input.Events = append([]modulecore.EventEnvelope(nil), input.Events...)
	for index := range input.Events {
		input.Events[index].DependencyEventIDs = append([]modulecore.EventID(nil), input.Events[index].DependencyEventIDs...)
		input.Events[index].Payload = cloneMutablePayload(input.Events[index].Payload)
	}
	input.MessageIndexes = append([]MessageIndex(nil), input.MessageIndexes...)
	input.Outbox = append([]WorkItem(nil), input.Outbox...)
	if input.Delegation != nil {
		delegation := *input.Delegation
		input.Delegation = &delegation
	}
	return input
}

func (s *SQLiteStore) commitTurnBundleInline(ctx context.Context, input CommitInput) (CommittedBatch, error) {
	if err := s.ensureOpen(); err != nil {
		return CommittedBatch{}, err
	}
	if ctx == nil {
		return CommittedBatch{}, errors.New("commit turn context is required")
	}
	if err := validateCommitInput(input); err != nil {
		return CommittedBatch{}, err
	}
	inputHash, err := commitInputHash(input)
	if err != nil {
		return CommittedBatch{}, err
	}
	tx, unlockSequence, err := s.beginSequenceTx(ctx)
	if err != nil {
		return CommittedBatch{}, err
	}
	defer unlockSequence()
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if receipt, found, err := readOperationReceipt(ctx, tx, input.OperationID); err != nil {
		return CommittedBatch{}, err
	} else if found {
		if receipt.operationKind != "commit_turn_bundle" || receipt.inputSHA256 != inputHash {
			return CommittedBatch{}, ErrOperationConflict
		}
		var batch CommittedBatch
		if err := json.Unmarshal([]byte(receipt.resultJSON), &batch); err != nil {
			return CommittedBatch{}, fmt.Errorf("decode committed batch receipt: %w", err)
		}
		if err := validateStoredCommittedBatch(batch, input.TurnID, input.SessionID); err != nil {
			return CommittedBatch{}, err
		}
		if err := tx.Commit(); err != nil {
			return CommittedBatch{}, err
		}
		committed = true
		return batch, nil
	}

	var turnSessionID, turnState, turnTriggerKind, inputMessageID string
	var generationEpoch int64
	err = tx.QueryRowContext(ctx, `
		SELECT t.session_id, t.state, t.trigger_kind, t.generation_epoch, t.input_message_id
		FROM live_conversation_turn t WHERE t.turn_id = ?`, string(input.TurnID),
	).Scan(&turnSessionID, &turnState, &turnTriggerKind, &generationEpoch, &inputMessageID)
	if errors.Is(err, sql.ErrNoRows) {
		return CommittedBatch{}, fmt.Errorf("conversation turn %q does not exist", input.TurnID)
	}
	if err != nil {
		return CommittedBatch{}, err
	}
	if turnSessionID != string(input.SessionID) || turnState != "accepted" || generationEpoch != input.GenerationEpoch {
		return CommittedBatch{}, ErrConversationTurnConflict
	}
	if turnTriggerKind == "report_reply" {
		if err := validateReportReplyCommitFence(ctx, tx, input.TurnID, input.SessionID); err != nil {
			var staleRevision *staleReportRevisionConflict
			if errors.As(err, &staleRevision) {
				if quarantineErr := quarantineStaleReportTurn(ctx, tx, input.TurnID, input.SessionID, staleRevision); quarantineErr != nil {
					return CommittedBatch{}, fmt.Errorf("quarantine stale report turn: %w", quarantineErr)
				}
				if commitErr := tx.Commit(); commitErr != nil {
					return CommittedBatch{}, fmt.Errorf("commit stale report quarantine: %w", commitErr)
				}
				committed = true
			}
			return CommittedBatch{}, err
		}
	}
	var inputTraceID string
	if err := tx.QueryRowContext(ctx, `SELECT trace_id FROM event_envelope WHERE event_id = (SELECT event_id FROM live_message_index WHERE message_id = ?)`, inputMessageID).Scan(&inputTraceID); err != nil {
		return CommittedBatch{}, fmt.Errorf("read input trace for turn %q: %w", input.TurnID, err)
	}

	batchEvents := make([]modulecore.EventEnvelope, 0, len(input.Events))
	for _, unsequenced := range input.Events {
		event := unsequenced
		event.EventSeq, err = s.nextSequence(ctx)
		if err != nil {
			return CommittedBatch{}, err
		}
		if err := ensureEventSeqAvailable(ctx, tx, event.EventSeq); err != nil {
			return CommittedBatch{}, err
		}
		if err := ensureEventIDAbsent(ctx, tx, event.EventID); err != nil {
			return CommittedBatch{}, err
		}
		if string(event.TraceID) != inputTraceID {
			return CommittedBatch{}, fmt.Errorf("event %q belongs to another trace", event.EventID)
		}
		if err := s.validateReferences(ctx, tx, event); err != nil {
			return CommittedBatch{}, err
		}
		event, err = insertLiveEvent(ctx, tx, event)
		if err != nil {
			return CommittedBatch{}, err
		}
		batchEvents = append(batchEvents, event)
	}

	if err := validateReplyTargets(ctx, tx, input); err != nil {
		return CommittedBatch{}, err
	}
	for _, message := range input.MessageIndexes {
		if err := insertMessageIndex(ctx, tx, message); err != nil {
			return CommittedBatch{}, err
		}
	}
	if input.Delegation != nil {
		delegation := input.Delegation
		if err := validateOutboxSource(ctx, tx, delegation.ContextEventID, inputTraceID, input.SessionID); err != nil {
			return CommittedBatch{}, fmt.Errorf("validate delegation context: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO live_delegation
				(delegation_id, revision, origin_turn_id, assignee, instruction_message_id,
				 context_event_id, state, work_task_id, report_revision)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			delegation.DelegationID, delegation.Revision, string(delegation.OriginTurnID), delegation.Assignee,
			string(delegation.InstructionMessageID), string(delegation.ContextEventID), delegation.State,
			nullableString(delegation.WorkTaskID), delegation.ReportRevision,
		); err != nil {
			return CommittedBatch{}, err
		}
	}
	for _, item := range input.Outbox {
		if item.SessionID != input.SessionID {
			return CommittedBatch{}, fmt.Errorf("work item %q belongs to another session", item.WorkID)
		}
		if err := validateOutboxSource(ctx, tx, item.SourceEventID, inputTraceID, input.SessionID); err != nil {
			return CommittedBatch{}, err
		}
		notBefore := ""
		if !item.NotBefore.IsZero() {
			notBefore = item.NotBefore.UTC().Format(time.RFC3339Nano)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO live_work_outbox
				(work_id, dedupe_key, kind, source_event_id, session_id, priority, state, attempt, not_before, version)
			VALUES (?, ?, ?, ?, ?, ?, 'pending', 0, ?, 1)`,
			item.WorkID, item.DedupeKey, item.Kind, string(item.SourceEventID), string(item.SessionID), item.Priority, notBefore,
		); err != nil {
			return CommittedBatch{}, err
		}
	}

	batch := CommittedBatch{
		OperationID: input.OperationID,
		SessionID:   input.SessionID,
		TurnID:      input.TurnID,
		ResultState: input.ResultState,
		Events:      batchEvents,
	}
	if err := validateStoredCommittedBatch(batch, input.TurnID, input.SessionID); err != nil {
		return CommittedBatch{}, err
	}
	delegationID := sql.NullString{}
	if input.Delegation != nil {
		delegationID = sql.NullString{String: input.Delegation.DelegationID, Valid: true}
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE live_conversation_turn
		SET state = ?, delegation_id = ?, result_receipt_id = ?
		WHERE turn_id = ? AND state = 'accepted'`,
		input.ResultState, delegationID, input.OperationID, string(input.TurnID),
	)
	if err != nil {
		return CommittedBatch{}, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return CommittedBatch{}, fmt.Errorf("check committed turn update: %w", err)
	}
	if updated != 1 {
		return CommittedBatch{}, ErrConversationTurnConflict
	}
	if err := insertOperationReceipt(ctx, tx, input.OperationID, "commit_turn_bundle", inputHash, batch); err != nil {
		return CommittedBatch{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommittedBatch{}, err
	}
	committed = true
	s.observeCommittedOrchestratorEvents(batchEvents)
	return batch, nil
}

// LoadCommittedOutcome resolves a commit whose caller did not observe the
// commit response. It returns the exact receipt result and never regenerates.
func (s *SQLiteStore) LoadCommittedOutcome(ctx context.Context, turnID modulecore.TurnID) (CommittedBatch, bool, error) {
	if err := s.ensureOpen(); err != nil {
		return CommittedBatch{}, false, err
	}
	if ctx == nil {
		return CommittedBatch{}, false, errors.New("committed outcome context is required")
	}
	if err := turnID.Validate(); err != nil {
		return CommittedBatch{}, false, fmt.Errorf("turn_id: %w", err)
	}
	var sessionID, receiptID sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT session_id, result_receipt_id FROM live_conversation_turn WHERE turn_id = ?`, string(turnID)).Scan(&sessionID, &receiptID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !receiptID.Valid) {
		return CommittedBatch{}, false, nil
	}
	if err != nil {
		return CommittedBatch{}, false, err
	}
	var operationKind, resultJSON string
	if err := s.db.QueryRowContext(ctx, `SELECT operation_kind, result_json FROM live_operation_receipt WHERE operation_id = ?`, receiptID.String).Scan(&operationKind, &resultJSON); err != nil {
		return CommittedBatch{}, false, fmt.Errorf("read committed outcome receipt %q: %w", receiptID.String, err)
	}
	if operationKind != "commit_turn_bundle" {
		return CommittedBatch{}, false, fmt.Errorf("turn %q references non-commit receipt %q", turnID, receiptID.String)
	}
	var batch CommittedBatch
	if err := json.Unmarshal([]byte(resultJSON), &batch); err != nil {
		return CommittedBatch{}, false, fmt.Errorf("decode committed outcome receipt: %w", err)
	}
	if err := validateStoredCommittedBatch(batch, turnID, modulecore.SessionID(sessionID.String)); err != nil {
		return CommittedBatch{}, false, err
	}
	return batch, true, nil
}

type operationReceipt struct {
	operationKind string
	inputSHA256   string
	resultJSON    string
}

func readOperationReceipt(ctx context.Context, tx *sql.Tx, operationID string) (operationReceipt, bool, error) {
	var receipt operationReceipt
	err := tx.QueryRowContext(ctx, `
		SELECT operation_kind, input_sha256, result_json
		FROM live_operation_receipt WHERE operation_id = ?`, operationID,
	).Scan(&receipt.operationKind, &receipt.inputSHA256, &receipt.resultJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return operationReceipt{}, false, nil
	}
	if err != nil {
		return operationReceipt{}, false, err
	}
	return receipt, true, nil
}

func insertOperationReceipt(ctx context.Context, tx *sql.Tx, operationID, operationKind, inputHash string, result any) error {
	encoded, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal %s receipt: %w", operationKind, err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO live_operation_receipt (operation_id, operation_kind, input_sha256, result_json, created_at)
		VALUES (?, ?, ?, ?, ?)`, operationID, operationKind, inputHash, string(encoded), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func validateAcceptInput(input AcceptInput) error {
	for name, value := range map[string]string{
		"operation_id": input.OperationID, "owner_id": input.OwnerID, "runtime_version": input.RuntimeVersion,
		"client_submission_id": input.ClientSubmissionID, "actor_id": input.ActorID, "trigger_kind": input.TriggerKind,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if input.GenerationEpoch <= 0 {
		return errors.New("generation_epoch must be positive")
	}
	if err := modulecore.ValidateEventEnvelope(input.InputEvent); err != nil {
		return fmt.Errorf("validate input event: %w", err)
	}
	if input.InputEvent.EventSeq != 0 {
		return errors.New("input event event_seq must be zero for live acceptance")
	}
	if input.InputEvent.SessionID == "" || input.InputEvent.TurnID == "" || input.InputEvent.MessageID == "" {
		return errors.New("input event session_id, turn_id, and message_id are required")
	}
	if input.InputEvent.ComponentID != "orchestrator" {
		return errors.New("accepted user input must use the orchestrator component")
	}
	if input.InputEvent.ActorKind != "" || input.InputEvent.ActorID != "" {
		return errors.New("public conversation event cannot carry execution actor identity")
	}
	if payloadString(input.InputEvent.Payload, "from") != "user" || payloadString(input.InputEvent.Payload, "source_actor_id") != input.ActorID {
		return errors.New("accepted user event payload does not match the authenticated actor")
	}
	if err := validateMessageIndex(input.InputMessage, input.InputEvent, input.InputEvent.SessionID, input.InputEvent.TurnID); err != nil {
		return fmt.Errorf("validate input message index: %w", err)
	}
	if input.InputMessage.Visibility != "public" {
		return errors.New("accepted user input message must be public")
	}
	if input.TriggerKind == "clarification_reply" {
		if input.InputMessage.ReplyToMessageID == "" || payloadString(input.InputEvent.Payload, "reply_to_message_id") != string(input.InputMessage.ReplyToMessageID) ||
			input.InputMessage.FromActor != input.ActorID || input.InputMessage.ToKind != "agent" || input.InputMessage.ToActor != "mio" {
			return ErrClarificationReplyConflict
		}
	}
	return nil
}

func validateCommitInput(input CommitInput) error {
	if strings.TrimSpace(input.OperationID) == "" {
		return errors.New("operation_id is required")
	}
	if err := input.SessionID.Validate(); err != nil {
		return fmt.Errorf("session_id: %w", err)
	}
	if err := input.TurnID.Validate(); err != nil {
		return fmt.Errorf("turn_id: %w", err)
	}
	if input.GenerationEpoch <= 0 {
		return errors.New("generation_epoch must be positive")
	}
	if len(input.Events) == 0 {
		return errors.New("commit bundle must contain at least one event")
	}
	if input.ResultState != "completed" && input.ResultState != "awaiting_readback" {
		return fmt.Errorf("unsupported result_state %q", input.ResultState)
	}
	if (input.ResultState == "awaiting_readback") != (input.Delegation != nil) {
		return errors.New("awaiting_readback requires one delegation record and direct completion forbids it")
	}
	seenEvents := make(map[modulecore.EventID]modulecore.EventEnvelope, len(input.Events))
	for _, event := range input.Events {
		if err := modulecore.ValidateEventEnvelope(event); err != nil {
			return fmt.Errorf("validate commit event %q: %w", event.EventID, err)
		}
		if event.EventSeq != 0 {
			return fmt.Errorf("commit event %q event_seq must be zero", event.EventID)
		}
		if event.SessionID != input.SessionID || event.TurnID != input.TurnID {
			return fmt.Errorf("commit event %q does not match session and turn", event.EventID)
		}
		if _, exists := seenEvents[event.EventID]; exists {
			return fmt.Errorf("duplicate event_id %q in commit bundle", event.EventID)
		}
		seenEvents[event.EventID] = event
	}
	seenMessages := make(map[modulecore.MessageID]MessageIndex, len(input.MessageIndexes))
	publicUserMessage := false
	for _, message := range input.MessageIndexes {
		event, exists := seenEvents[message.EventID]
		if !exists {
			return fmt.Errorf("message index event %q is not in commit bundle", message.EventID)
		}
		if err := validateMessageIndex(message, event, input.SessionID, input.TurnID); err != nil {
			return fmt.Errorf("validate message index %q: %w", message.MessageID, err)
		}
		if _, exists := seenMessages[message.MessageID]; exists {
			return fmt.Errorf("duplicate message_id %q in commit bundle", message.MessageID)
		}
		seenMessages[message.MessageID] = message
		if message.Visibility == "public" && event.ComponentID == "orchestrator" && message.ToKind == "user" {
			publicUserMessage = true
		}
	}
	for _, event := range input.Events {
		if event.MessageID != "" {
			if _, exists := seenMessages[event.MessageID]; !exists {
				return fmt.Errorf("message event %q has no message index", event.EventID)
			}
		}
		if event.ComponentID == "orchestrator" {
			message, exists := seenMessages[event.MessageID]
			if event.MessageID == "" || !exists || message.Visibility != "public" {
				return fmt.Errorf("orchestrator event %q must be a public indexed message", event.EventID)
			}
		}
	}
	if !publicUserMessage {
		return errors.New("commit bundle must contain a public user-facing message in orchestrator component")
	}
	if input.Delegation != nil {
		delegation := input.Delegation
		if strings.TrimSpace(delegation.DelegationID) == "" || strings.TrimSpace(delegation.Assignee) == "" || delegation.Revision <= 0 {
			return errors.New("delegation_id, assignee, and positive revision are required")
		}
		if delegation.OriginTurnID != input.TurnID || delegation.State != "dispatch_accepted" {
			return errors.New("delegation origin turn or initial state is invalid")
		}
		instruction, exists := seenMessages[delegation.InstructionMessageID]
		if !exists || instruction.Visibility != "private" {
			return errors.New("delegation instruction must be indexed as private")
		}
		instructionEvent := seenEvents[instruction.EventID]
		if instructionEvent.ComponentID == "orchestrator" {
			return errors.New("private delegation instruction must not use orchestrator component")
		}
		if err := delegation.ContextEventID.Validate(); err != nil {
			return fmt.Errorf("delegation context_event_id: %w", err)
		}
		if delegation.ContextEventID == "" {
			return errors.New("delegation context_event_id is required")
		}
		dispatchFound := false
		for _, item := range input.Outbox {
			if item.Kind == "dispatch_delegation" && item.SourceEventID == instruction.EventID {
				dispatchFound = true
				break
			}
		}
		if !dispatchFound {
			return errors.New("delegation instruction requires a dispatch_delegation outbox item")
		}
	}
	seenWorkIDs := make(map[string]struct{}, len(input.Outbox))
	seenDedupeKeys := make(map[string]struct{}, len(input.Outbox))
	for _, item := range input.Outbox {
		if strings.TrimSpace(item.WorkID) == "" || strings.TrimSpace(item.DedupeKey) == "" || strings.TrimSpace(item.Kind) == "" || item.SourceEventID == "" {
			return errors.New("outbox work_id, dedupe_key, kind, and source_event_id are required")
		}
		if _, exists := seenWorkIDs[item.WorkID]; exists {
			return fmt.Errorf("duplicate work_id %q", item.WorkID)
		}
		if _, exists := seenDedupeKeys[item.DedupeKey]; exists {
			return fmt.Errorf("duplicate outbox dedupe_key %q", item.DedupeKey)
		}
		seenWorkIDs[item.WorkID] = struct{}{}
		seenDedupeKeys[item.DedupeKey] = struct{}{}
	}
	return nil
}

func validateMessageIndex(message MessageIndex, event modulecore.EventEnvelope, sessionID modulecore.SessionID, turnID modulecore.TurnID) error {
	if err := message.MessageID.Validate(); err != nil {
		return fmt.Errorf("message_id: %w", err)
	}
	if err := message.EventID.Validate(); err != nil {
		return fmt.Errorf("event_id: %w", err)
	}
	if message.MessageID != event.MessageID || message.EventID != event.EventID {
		return errors.New("message index identity does not match canonical event")
	}
	if event.ComponentID == "orchestrator" {
		if event.ActorKind != "" || event.ActorID != "" {
			return errors.New("public orchestrator message cannot carry execution actor identity")
		}
		if payloadString(event.Payload, "source_actor_id") != message.FromActor {
			return errors.New("message index from_actor does not match trusted payload source_actor_id")
		}
		from := payloadString(event.Payload, "from")
		if event.EventType == "conversation.user_message" {
			if from != "user" {
				return errors.New("accepted public input must use the user display role")
			}
		} else if from != message.FromActor {
			return errors.New("public message from does not match its indexed actor")
		}
	} else {
		if (event.ActorKind == "") != (event.ActorID == "") {
			return errors.New("private component event actor identity must be complete")
		}
		if event.ActorID != "" && event.ActorID != message.FromActor {
			return errors.New("message index from_actor does not match canonical event actor")
		}
	}
	if message.SessionID != sessionID || message.TurnID != turnID || event.SessionID != sessionID || event.TurnID != turnID {
		return errors.New("message index session or turn does not match canonical event")
	}
	for name, value := range map[string]string{
		"from_actor": message.FromActor, "to_kind": message.ToKind, "to_actor": message.ToActor, "kind": message.Kind,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("message index %s is required", name)
		}
	}
	if message.Visibility != "public" && message.Visibility != "private" {
		return fmt.Errorf("message index visibility %q is invalid", message.Visibility)
	}
	if event.ComponentID == "orchestrator" && message.Visibility != "public" {
		return errors.New("private message cannot use orchestrator component")
	}
	if message.ReplyToMessageID != "" {
		if err := message.ReplyToMessageID.Validate(); err != nil {
			return fmt.Errorf("reply_to_message_id: %w", err)
		}
		if message.ReplyToMessageID == message.MessageID {
			return errors.New("message cannot reply to itself")
		}
	}
	return nil
}

func payloadString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
}

func acceptInputHash(input AcceptInput) (string, error) {
	material := struct {
		OwnerID            string         `json:"owner_id"`
		RuntimeVersion     string         `json:"runtime_version"`
		ClientSubmissionID string         `json:"client_submission_id"`
		SessionID          string         `json:"session_id"`
		ActorID            string         `json:"actor_id"`
		TriggerKind        string         `json:"trigger_kind"`
		GenerationEpoch    int64          `json:"generation_epoch"`
		EventType          string         `json:"event_type"`
		ComponentID        string         `json:"component_id"`
		ActorKind          string         `json:"actor_kind"`
		EventActorID       string         `json:"event_actor_id"`
		Payload            map[string]any `json:"payload,omitempty"`
		Message            struct {
			FromActor        string               `json:"from_actor"`
			ToKind           string               `json:"to_kind"`
			ToActor          string               `json:"to_actor"`
			Kind             string               `json:"kind"`
			ReplyToMessageID modulecore.MessageID `json:"reply_to_message_id,omitempty"`
			Visibility       string               `json:"visibility"`
		} `json:"message"`
	}{
		OwnerID: input.OwnerID, RuntimeVersion: input.RuntimeVersion, ClientSubmissionID: input.ClientSubmissionID,
		SessionID: string(input.InputEvent.SessionID), ActorID: input.ActorID, TriggerKind: input.TriggerKind,
		GenerationEpoch: input.GenerationEpoch, EventType: input.InputEvent.EventType, ComponentID: input.InputEvent.ComponentID,
		ActorKind: input.InputEvent.ActorKind, EventActorID: input.InputEvent.ActorID, Payload: input.InputEvent.Payload,
	}
	material.Message.FromActor = input.InputMessage.FromActor
	material.Message.ToKind = input.InputMessage.ToKind
	material.Message.ToActor = input.InputMessage.ToActor
	material.Message.Kind = input.InputMessage.Kind
	material.Message.ReplyToMessageID = input.InputMessage.ReplyToMessageID
	material.Message.Visibility = input.InputMessage.Visibility
	return hashJSON(material)
}

func commitInputHash(input CommitInput) (string, error) {
	input.OperationID = ""
	return hashJSON(input)
}

func hashJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func insertLiveEvent(ctx context.Context, tx *sql.Tx, event modulecore.EventEnvelope) (modulecore.EventEnvelope, error) {
	if err := modulecore.ValidateEventEnvelope(event); err != nil {
		return modulecore.EventEnvelope{}, fmt.Errorf("validate event envelope: %w", err)
	}
	if err := event.EventSeq.Validate(); err != nil {
		return modulecore.EventEnvelope{}, fmt.Errorf("event_seq: %w", err)
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return modulecore.EventEnvelope{}, fmt.Errorf("marshal event envelope: %w", err)
	}
	var persisted modulecore.EventEnvelope
	if err := json.Unmarshal(payload, &persisted); err != nil {
		return modulecore.EventEnvelope{}, fmt.Errorf("decode persisted event envelope: %w", err)
	}
	if err := modulecore.ValidateEventEnvelope(persisted); err != nil {
		return modulecore.EventEnvelope{}, fmt.Errorf("validate persisted event envelope: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO event_envelope
			(event_id, event_seq, trace_id, schema_version, event_type, component_id, occurred_at, envelope_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		string(event.EventID), int64(event.EventSeq), string(event.TraceID), event.SchemaVersion,
		event.EventType, event.ComponentID, event.OccurredAt.Format(time.RFC3339Nano), string(payload),
	); err != nil {
		return modulecore.EventEnvelope{}, err
	}
	if event.CausationEventID != "" {
		if err := insertDependency(ctx, tx, event.EventID, event.CausationEventID, "causation"); err != nil {
			return modulecore.EventEnvelope{}, err
		}
	}
	for _, dependencyID := range event.DependencyEventIDs {
		if err := insertDependency(ctx, tx, event.EventID, dependencyID, "dependency"); err != nil {
			return modulecore.EventEnvelope{}, err
		}
	}
	return persisted, nil
}

func ensureEventIDAbsent(ctx context.Context, tx *sql.Tx, eventID modulecore.EventID) error {
	var existing int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM event_envelope WHERE event_id = ? LIMIT 1`, string(eventID)).Scan(&existing)
	if err == nil {
		return fmt.Errorf("duplicate event_id %q", eventID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

func insertMessageIndex(ctx context.Context, tx *sql.Tx, message MessageIndex) error {
	var replyTo any
	if message.ReplyToMessageID != "" {
		replyTo = string(message.ReplyToMessageID)
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO live_message_index
			(message_id, event_id, session_id, turn_id, from_actor, to_kind, to_actor, kind, reply_to_message_id, visibility)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(message.MessageID), string(message.EventID), string(message.SessionID), string(message.TurnID),
		message.FromActor, message.ToKind, message.ToActor, message.Kind, replyTo, message.Visibility,
	)
	return err
}

func validateOutboxSource(ctx context.Context, tx *sql.Tx, eventID modulecore.EventID, expectedTraceID string, expectedSessionID modulecore.SessionID) error {
	var traceID, sessionID string
	err := tx.QueryRowContext(ctx, `SELECT trace_id, COALESCE(json_extract(envelope_json, '$.session_id'), '') FROM event_envelope WHERE event_id = ?`, string(eventID)).Scan(&traceID, &sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("outbox source event %q does not exist", eventID)
	}
	if err != nil {
		return err
	}
	if traceID != expectedTraceID {
		return fmt.Errorf("outbox source event %q belongs to another trace", eventID)
	}
	if sessionID != string(expectedSessionID) {
		return fmt.Errorf("outbox source event %q belongs to another session", eventID)
	}
	return nil
}

func validateReplyTarget(ctx context.Context, tx *sql.Tx, messageID modulecore.MessageID, sessionID modulecore.SessionID) error {
	if messageID == "" {
		return nil
	}
	var storedSessionID string
	err := tx.QueryRowContext(ctx, `SELECT session_id FROM live_message_index WHERE message_id = ?`, string(messageID)).Scan(&storedSessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("reply target message %q does not exist", messageID)
	}
	if err != nil {
		return err
	}
	if storedSessionID != string(sessionID) {
		return fmt.Errorf("reply target message %q belongs to another session", messageID)
	}
	return nil
}

func validateReplyTargets(ctx context.Context, tx *sql.Tx, input CommitInput) error {
	eventOrder := make(map[modulecore.EventID]int, len(input.Events))
	for index, event := range input.Events {
		eventOrder[event.EventID] = index
	}
	messageEvents := make(map[modulecore.MessageID]modulecore.EventID, len(input.MessageIndexes))
	for _, message := range input.MessageIndexes {
		messageEvents[message.MessageID] = message.EventID
	}
	for _, message := range input.MessageIndexes {
		if message.ReplyToMessageID == "" {
			continue
		}
		if targetEventID, exists := messageEvents[message.ReplyToMessageID]; exists {
			if eventOrder[targetEventID] >= eventOrder[message.EventID] {
				return fmt.Errorf("reply target message %q must precede message %q", message.ReplyToMessageID, message.MessageID)
			}
			continue
		}
		if err := validateReplyTarget(ctx, tx, message.ReplyToMessageID, input.SessionID); err != nil {
			return err
		}
	}
	return nil
}

func validateStoredCommittedBatch(batch CommittedBatch, turnID modulecore.TurnID, sessionID modulecore.SessionID) error {
	if batch.TurnID != turnID || batch.SessionID != sessionID || strings.TrimSpace(batch.OperationID) == "" {
		return errors.New("committed batch receipt identity does not match turn")
	}
	if len(batch.Events) == 0 {
		return errors.New("committed batch receipt contains no events")
	}
	var previous modulecore.EventSeq
	for index, event := range batch.Events {
		if err := modulecore.ValidateEventEnvelope(event); err != nil {
			return fmt.Errorf("stored committed event %q: %w", event.EventID, err)
		}
		if err := event.EventSeq.Validate(); err != nil {
			return fmt.Errorf("stored committed event %q sequence: %w", event.EventID, err)
		}
		if event.SessionID != sessionID || event.TurnID != turnID {
			return fmt.Errorf("stored committed event %q does not match turn", event.EventID)
		}
		if index > 0 && event.EventSeq <= previous {
			return errors.New("stored committed batch events are not in event_seq order")
		}
		previous = event.EventSeq
	}
	return nil
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
