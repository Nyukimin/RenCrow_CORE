package core

import (
	"database/sql/driver"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Canonical IDs are distinct string types. New runtime IDs contain UUIDv7;
// deterministic migration IDs contain UUIDv5.
type (
	TraceID      string
	EventID      string
	SessionID    string
	ThreadID     string
	TurnID       string
	MessageID    string
	UtteranceID  string
	WorkstreamID string
	GoalID       string
	TaskID       string
	RunID        string
	ActionID     string
	AttemptID    string
	RequestID    string
	ResponseID   string
	ArtifactID   string
	EvidenceID   string
	MemoryID     string
	RelationID   string
	ScheduleID   string
	QueueItemID  string
	CheckpointID string
	ReceiptID    string
)

// ThreadSeq is the explicit, positive ordering number of a thread in a session.
type ThreadSeq int64

func (seq ThreadSeq) Validate() error {
	if seq <= 0 {
		return fmt.Errorf("thread sequence must be positive")
	}
	return nil
}

// ThreadKind identifies the domain meaning of a thread.
type ThreadKind string

const (
	ThreadKindUserConversation ThreadKind = "user_conversation"
	ThreadKindAgentDiscussion  ThreadKind = "agent_discussion"
	ThreadKindIdleChat         ThreadKind = "idlechat"
	ThreadKindDocument         ThreadKind = "document"
	ThreadKindSystem           ThreadKind = "system"
)

func (kind ThreadKind) Validate() error {
	switch kind {
	case ThreadKindUserConversation, ThreadKindAgentDiscussion, ThreadKindIdleChat, ThreadKindDocument, ThreadKindSystem:
		return nil
	default:
		return fmt.Errorf("invalid thread kind %q", kind)
	}
}

// CanonicalIDType selects the target type for deterministic migration IDs.
type CanonicalIDType string

const (
	CanonicalTraceID      CanonicalIDType = "TraceID"
	CanonicalEventID      CanonicalIDType = "EventID"
	CanonicalSessionID    CanonicalIDType = "SessionID"
	CanonicalThreadID     CanonicalIDType = "ThreadID"
	CanonicalTurnID       CanonicalIDType = "TurnID"
	CanonicalMessageID    CanonicalIDType = "MessageID"
	CanonicalUtteranceID  CanonicalIDType = "UtteranceID"
	CanonicalWorkstreamID CanonicalIDType = "WorkstreamID"
	CanonicalGoalID       CanonicalIDType = "GoalID"
	CanonicalTaskID       CanonicalIDType = "TaskID"
	CanonicalRunID        CanonicalIDType = "RunID"
	CanonicalActionID     CanonicalIDType = "ActionID"
	CanonicalAttemptID    CanonicalIDType = "AttemptID"
	CanonicalRequestID    CanonicalIDType = "RequestID"
	CanonicalResponseID   CanonicalIDType = "ResponseID"
	CanonicalArtifactID   CanonicalIDType = "ArtifactID"
	CanonicalEvidenceID   CanonicalIDType = "EvidenceID"
	CanonicalMemoryID     CanonicalIDType = "MemoryID"
	CanonicalRelationID   CanonicalIDType = "RelationID"
	CanonicalScheduleID   CanonicalIDType = "ScheduleID"
	CanonicalQueueItemID  CanonicalIDType = "QueueItemID"
	CanonicalCheckpointID CanonicalIDType = "CheckpointID"
	CanonicalReceiptID    CanonicalIDType = "ReceiptID"
)

const (
	renCrowMigrationNamespaceText = "6570d821-e63e-592d-a51f-8cf4b43cdba5"
	traceIDPrefix                 = "trc_"
	eventIDPrefix                 = "evt_"
	sessionIDPrefix               = "ses_"
	threadIDPrefix                = "thr_"
	turnIDPrefix                  = "turn_"
	messageIDPrefix               = "msg_"
	utteranceIDPrefix             = "utt_"
	workstreamIDPrefix            = "ws_"
	goalIDPrefix                  = "gol_"
	taskIDPrefix                  = "tsk_"
	runIDPrefix                   = "run_"
	actionIDPrefix                = "act_"
	attemptIDPrefix               = "att_"
	requestIDPrefix               = "req_"
	responseIDPrefix              = "rsp_"
	artifactIDPrefix              = "art_"
	evidenceIDPrefix              = "evd_"
	memoryIDPrefix                = "mem_"
	relationIDPrefix              = "rel_"
	scheduleIDPrefix              = "sch_"
	queueItemIDPrefix             = "qit_"
	checkpointIDPrefix            = "ckp_"
	receiptIDPrefix               = "rcp_"
)

var (
	renCrowMigrationNamespace = uuid.MustParse(renCrowMigrationNamespaceText)
	canonicalIDPrefixes       = map[CanonicalIDType]string{
		CanonicalTraceID: traceIDPrefix, CanonicalEventID: eventIDPrefix, CanonicalSessionID: sessionIDPrefix,
		CanonicalThreadID: threadIDPrefix, CanonicalTurnID: turnIDPrefix, CanonicalMessageID: messageIDPrefix,
		CanonicalUtteranceID: utteranceIDPrefix, CanonicalWorkstreamID: workstreamIDPrefix, CanonicalGoalID: goalIDPrefix,
		CanonicalTaskID: taskIDPrefix, CanonicalRunID: runIDPrefix, CanonicalActionID: actionIDPrefix,
		CanonicalAttemptID: attemptIDPrefix, CanonicalRequestID: requestIDPrefix, CanonicalResponseID: responseIDPrefix,
		CanonicalArtifactID: artifactIDPrefix, CanonicalEvidenceID: evidenceIDPrefix, CanonicalMemoryID: memoryIDPrefix,
		CanonicalRelationID: relationIDPrefix, CanonicalScheduleID: scheduleIDPrefix, CanonicalQueueItemID: queueItemIDPrefix,
		CanonicalCheckpointID: checkpointIDPrefix, CanonicalReceiptID: receiptIDPrefix,
	}
)

type uuidV7Generator func() (uuid.UUID, error)

func newCanonicalID(prefix string, generate uuidV7Generator) (string, error) {
	if prefix == "" {
		return "", fmt.Errorf("canonical ID prefix is required")
	}
	id, err := generate()
	if err != nil {
		return "", fmt.Errorf("generate canonical UUIDv7: %w", err)
	}
	if id.Version() != 7 {
		return "", fmt.Errorf("canonical ID generator returned UUIDv%d, want UUIDv7", id.Version())
	}
	return prefix + id.String(), nil
}

func mustNewCanonicalID(prefix string) string {
	raw, err := newCanonicalID(prefix, uuid.NewV7)
	if err != nil {
		panic(err)
	}
	return raw
}

// NewMigrationID deterministically maps one legacy field value to its target
// canonical ID. The mapping is for migration processes, not runtime lookup.
func NewMigrationID(targetType CanonicalIDType, sourceTable, sourceField, sourceValue string) (string, error) {
	prefix, ok := canonicalIDPrefixes[targetType]
	if !ok {
		return "", fmt.Errorf("unknown canonical ID type %q", targetType)
	}
	if strings.TrimSpace(sourceTable) == "" {
		return "", fmt.Errorf("source table is required")
	}
	if strings.TrimSpace(sourceField) == "" {
		return "", fmt.Errorf("source field is required")
	}
	if sourceValue == "" {
		return "", fmt.Errorf("source value is required")
	}
	name := string(targetType) + "\x00" + sourceTable + "\x00" + sourceField + "\x00" + sourceValue
	return prefix + uuid.NewSHA1(renCrowMigrationNamespace, []byte(name)).String(), nil
}

func validateCanonicalID(raw, prefix string) error {
	if raw == "" {
		return fmt.Errorf("canonical ID is required")
	}
	if !strings.HasPrefix(raw, prefix) {
		return fmt.Errorf("canonical ID %q must use prefix %q", raw, prefix)
	}
	id, err := uuid.Parse(strings.TrimPrefix(raw, prefix))
	if err != nil {
		return fmt.Errorf("parse canonical ID %q: %w", raw, err)
	}
	if version := id.Version(); version != 5 && version != 7 {
		return fmt.Errorf("canonical ID %q uses UUIDv%d, want UUIDv5 or UUIDv7", raw, version)
	}
	return nil
}

func canonicalIDValue[T ~string](id T, prefix string) (driver.Value, error) {
	if err := validateCanonicalID(string(id), prefix); err != nil {
		return nil, err
	}
	return string(id), nil
}

func scanCanonicalID[T ~string](destination *T, source any, prefix string) error {
	if destination == nil {
		return fmt.Errorf("canonical ID destination is nil")
	}
	var raw string
	switch value := source.(type) {
	case string:
		raw = value
	case []byte:
		raw = string(value)
	default:
		return fmt.Errorf("scan canonical ID from %T", source)
	}
	if err := validateCanonicalID(raw, prefix); err != nil {
		return err
	}
	*destination = T(raw)
	return nil
}

func NewTraceID() TraceID           { return TraceID(mustNewCanonicalID(traceIDPrefix)) }
func NewEventID() EventID           { return EventID(mustNewCanonicalID(eventIDPrefix)) }
func NewSessionID() SessionID       { return SessionID(mustNewCanonicalID(sessionIDPrefix)) }
func NewThreadID() ThreadID         { return ThreadID(mustNewCanonicalID(threadIDPrefix)) }
func NewTurnID() TurnID             { return TurnID(mustNewCanonicalID(turnIDPrefix)) }
func NewMessageID() MessageID       { return MessageID(mustNewCanonicalID(messageIDPrefix)) }
func NewUtteranceID() UtteranceID   { return UtteranceID(mustNewCanonicalID(utteranceIDPrefix)) }
func NewWorkstreamID() WorkstreamID { return WorkstreamID(mustNewCanonicalID(workstreamIDPrefix)) }
func NewGoalID() GoalID             { return GoalID(mustNewCanonicalID(goalIDPrefix)) }
func NewTaskID() TaskID             { return TaskID(mustNewCanonicalID(taskIDPrefix)) }
func NewRunID() RunID               { return RunID(mustNewCanonicalID(runIDPrefix)) }
func NewActionID() ActionID         { return ActionID(mustNewCanonicalID(actionIDPrefix)) }
func NewAttemptID() AttemptID       { return AttemptID(mustNewCanonicalID(attemptIDPrefix)) }
func NewRequestID() RequestID       { return RequestID(mustNewCanonicalID(requestIDPrefix)) }
func NewResponseID() ResponseID     { return ResponseID(mustNewCanonicalID(responseIDPrefix)) }
func NewArtifactID() ArtifactID     { return ArtifactID(mustNewCanonicalID(artifactIDPrefix)) }
func NewEvidenceID() EvidenceID     { return EvidenceID(mustNewCanonicalID(evidenceIDPrefix)) }
func NewMemoryID() MemoryID         { return MemoryID(mustNewCanonicalID(memoryIDPrefix)) }
func NewRelationID() RelationID     { return RelationID(mustNewCanonicalID(relationIDPrefix)) }
func NewScheduleID() ScheduleID     { return ScheduleID(mustNewCanonicalID(scheduleIDPrefix)) }
func NewQueueItemID() QueueItemID   { return QueueItemID(mustNewCanonicalID(queueItemIDPrefix)) }
func NewCheckpointID() CheckpointID { return CheckpointID(mustNewCanonicalID(checkpointIDPrefix)) }
func NewReceiptID() ReceiptID       { return ReceiptID(mustNewCanonicalID(receiptIDPrefix)) }

// ParseTaskID validates a Task identity received at a module or transport
// boundary. New identity generation remains owned solely by NewTaskID.
func ParseTaskID(raw string) (TaskID, error) {
	id := TaskID(strings.TrimSpace(raw))
	if err := id.Validate(); err != nil {
		return "", err
	}
	return id, nil
}

func (id TaskID) String() string { return string(id) }
func (id TaskID) IsZero() bool   { return id == "" }

func (id TraceID) Validate() error                   { return validateCanonicalID(string(id), traceIDPrefix) }
func (id TraceID) Value() (driver.Value, error)      { return canonicalIDValue(id, traceIDPrefix) }
func (id *TraceID) Scan(src any) error               { return scanCanonicalID(id, src, traceIDPrefix) }
func (id EventID) Validate() error                   { return validateCanonicalID(string(id), eventIDPrefix) }
func (id EventID) Value() (driver.Value, error)      { return canonicalIDValue(id, eventIDPrefix) }
func (id *EventID) Scan(src any) error               { return scanCanonicalID(id, src, eventIDPrefix) }
func (id SessionID) Validate() error                 { return validateCanonicalID(string(id), sessionIDPrefix) }
func (id SessionID) Value() (driver.Value, error)    { return canonicalIDValue(id, sessionIDPrefix) }
func (id *SessionID) Scan(src any) error             { return scanCanonicalID(id, src, sessionIDPrefix) }
func (id ThreadID) Validate() error                  { return validateCanonicalID(string(id), threadIDPrefix) }
func (id ThreadID) Value() (driver.Value, error)     { return canonicalIDValue(id, threadIDPrefix) }
func (id *ThreadID) Scan(src any) error              { return scanCanonicalID(id, src, threadIDPrefix) }
func (id TurnID) Validate() error                    { return validateCanonicalID(string(id), turnIDPrefix) }
func (id TurnID) Value() (driver.Value, error)       { return canonicalIDValue(id, turnIDPrefix) }
func (id *TurnID) Scan(src any) error                { return scanCanonicalID(id, src, turnIDPrefix) }
func (id MessageID) Validate() error                 { return validateCanonicalID(string(id), messageIDPrefix) }
func (id MessageID) Value() (driver.Value, error)    { return canonicalIDValue(id, messageIDPrefix) }
func (id *MessageID) Scan(src any) error             { return scanCanonicalID(id, src, messageIDPrefix) }
func (id UtteranceID) Validate() error               { return validateCanonicalID(string(id), utteranceIDPrefix) }
func (id UtteranceID) Value() (driver.Value, error)  { return canonicalIDValue(id, utteranceIDPrefix) }
func (id *UtteranceID) Scan(src any) error           { return scanCanonicalID(id, src, utteranceIDPrefix) }
func (id WorkstreamID) Validate() error              { return validateCanonicalID(string(id), workstreamIDPrefix) }
func (id WorkstreamID) Value() (driver.Value, error) { return canonicalIDValue(id, workstreamIDPrefix) }
func (id *WorkstreamID) Scan(src any) error          { return scanCanonicalID(id, src, workstreamIDPrefix) }
func (id GoalID) Validate() error                    { return validateCanonicalID(string(id), goalIDPrefix) }
func (id GoalID) Value() (driver.Value, error)       { return canonicalIDValue(id, goalIDPrefix) }
func (id *GoalID) Scan(src any) error                { return scanCanonicalID(id, src, goalIDPrefix) }
func (id TaskID) Validate() error                    { return validateCanonicalID(string(id), taskIDPrefix) }
func (id TaskID) Value() (driver.Value, error)       { return canonicalIDValue(id, taskIDPrefix) }
func (id *TaskID) Scan(src any) error                { return scanCanonicalID(id, src, taskIDPrefix) }
func (id RunID) Validate() error                     { return validateCanonicalID(string(id), runIDPrefix) }
func (id RunID) Value() (driver.Value, error)        { return canonicalIDValue(id, runIDPrefix) }
func (id *RunID) Scan(src any) error                 { return scanCanonicalID(id, src, runIDPrefix) }
func (id ActionID) Validate() error                  { return validateCanonicalID(string(id), actionIDPrefix) }
func (id ActionID) Value() (driver.Value, error)     { return canonicalIDValue(id, actionIDPrefix) }
func (id *ActionID) Scan(src any) error              { return scanCanonicalID(id, src, actionIDPrefix) }
func (id AttemptID) Validate() error                 { return validateCanonicalID(string(id), attemptIDPrefix) }
func (id AttemptID) Value() (driver.Value, error)    { return canonicalIDValue(id, attemptIDPrefix) }
func (id *AttemptID) Scan(src any) error             { return scanCanonicalID(id, src, attemptIDPrefix) }
func (id RequestID) Validate() error                 { return validateCanonicalID(string(id), requestIDPrefix) }
func (id RequestID) Value() (driver.Value, error)    { return canonicalIDValue(id, requestIDPrefix) }
func (id *RequestID) Scan(src any) error             { return scanCanonicalID(id, src, requestIDPrefix) }
func (id ResponseID) Validate() error                { return validateCanonicalID(string(id), responseIDPrefix) }
func (id ResponseID) Value() (driver.Value, error)   { return canonicalIDValue(id, responseIDPrefix) }
func (id *ResponseID) Scan(src any) error            { return scanCanonicalID(id, src, responseIDPrefix) }
func (id ArtifactID) Validate() error                { return validateCanonicalID(string(id), artifactIDPrefix) }
func (id ArtifactID) Value() (driver.Value, error)   { return canonicalIDValue(id, artifactIDPrefix) }
func (id *ArtifactID) Scan(src any) error            { return scanCanonicalID(id, src, artifactIDPrefix) }
func (id EvidenceID) Validate() error                { return validateCanonicalID(string(id), evidenceIDPrefix) }
func (id EvidenceID) Value() (driver.Value, error)   { return canonicalIDValue(id, evidenceIDPrefix) }
func (id *EvidenceID) Scan(src any) error            { return scanCanonicalID(id, src, evidenceIDPrefix) }
func (id MemoryID) Validate() error                  { return validateCanonicalID(string(id), memoryIDPrefix) }
func (id MemoryID) Value() (driver.Value, error)     { return canonicalIDValue(id, memoryIDPrefix) }
func (id *MemoryID) Scan(src any) error              { return scanCanonicalID(id, src, memoryIDPrefix) }
func (id RelationID) Validate() error                { return validateCanonicalID(string(id), relationIDPrefix) }
func (id RelationID) Value() (driver.Value, error)   { return canonicalIDValue(id, relationIDPrefix) }
func (id *RelationID) Scan(src any) error            { return scanCanonicalID(id, src, relationIDPrefix) }
func (id ScheduleID) Validate() error                { return validateCanonicalID(string(id), scheduleIDPrefix) }
func (id ScheduleID) Value() (driver.Value, error)   { return canonicalIDValue(id, scheduleIDPrefix) }
func (id *ScheduleID) Scan(src any) error            { return scanCanonicalID(id, src, scheduleIDPrefix) }
func (id QueueItemID) Validate() error               { return validateCanonicalID(string(id), queueItemIDPrefix) }
func (id QueueItemID) Value() (driver.Value, error)  { return canonicalIDValue(id, queueItemIDPrefix) }
func (id *QueueItemID) Scan(src any) error           { return scanCanonicalID(id, src, queueItemIDPrefix) }
func (id CheckpointID) Validate() error              { return validateCanonicalID(string(id), checkpointIDPrefix) }
func (id CheckpointID) Value() (driver.Value, error) { return canonicalIDValue(id, checkpointIDPrefix) }
func (id *CheckpointID) Scan(src any) error          { return scanCanonicalID(id, src, checkpointIDPrefix) }
func (id ReceiptID) Validate() error                 { return validateCanonicalID(string(id), receiptIDPrefix) }
func (id ReceiptID) Value() (driver.Value, error)    { return canonicalIDValue(id, receiptIDPrefix) }
func (id *ReceiptID) Scan(src any) error             { return scanCanonicalID(id, src, receiptIDPrefix) }
