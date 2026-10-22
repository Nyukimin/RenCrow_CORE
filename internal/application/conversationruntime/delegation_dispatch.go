package conversationruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const DispatchDelegationWorkKind = "dispatch_delegation"

const (
	maxDelegationDispatchContextBytes = 64 * 1024
	delegationDispatchCleanupTimeout  = 5 * time.Second
)

const readbackFailurePublicText = "委譲先から有効な復唱を確認できませんでした。Mioは再び応答できます。"

type ReadbackFailureReason string

const (
	ReadbackFailureInvalidOutput ReadbackFailureReason = "invalid_output"
	ReadbackFailureExpired       ReadbackFailureReason = "deadline_exceeded"
)

var (
	ErrDelegationDispatchUnavailable = errors.New("delegation dispatcher is unavailable")
	ErrDelegationDispatchInvalid     = errors.New("delegation dispatch input is invalid")
)

type DelegationDispatchLease struct {
	WorkID        string
	DedupeKey     string
	Kind          string
	SourceEventID modulecore.EventID
	SessionID     modulecore.SessionID
	Priority      int
	Attempt       int
	LeaseToken    string
	LeaseExpires  time.Time
	Version       int64
	Assignee      string
}

type DispatchClaimRequest struct {
	Kind          string
	Limit         int
	LeaseDuration time.Duration
	Now           time.Time
}

type DelegationDispatchMessage struct {
	MessageID modulecore.MessageID `json:"message_id"`
	Role      string               `json:"role"`
	Text      string               `json:"text"`
}

type DelegationDispatchSource struct {
	Handoff          conversation.HandoffEnvelope
	Context          []DelegationDispatchMessage
	AcceptedAt       time.Time
	ReadbackDeadline time.Time
	FailureIntent    *ReadbackFailureIntent
	ResponseTo       *DelegationDispatchReplySource
}

// DelegationDispatchReplySource identifies the authenticated clarification
// reply that directly created this handoff revision. The EventStore remains
// responsible for adding these fields to the public readback event.
type DelegationDispatchReplySource struct {
	TurnID             modulecore.TurnID
	ClientSubmissionID string
}

type ReadbackFailureIntent struct {
	DelegationID modulecore.RequestID
	Revision     uint64
	Assignee     string
	Reason       ReadbackFailureReason
}

type AcceptReadbackRequest struct {
	OperationID  string
	DelegationID modulecore.RequestID
	Revision     uint64
	Assignee     string
	Readback     conversation.ReadbackEnvelope
	EventID      modulecore.EventID
	MessageID    modulecore.MessageID
	OccurredAt   time.Time
}

type ReadbackAcceptance struct {
	OperationID     string
	DelegationID    modulecore.RequestID
	Revision        uint64
	OriginTurnID    modulecore.TurnID
	SessionID       modulecore.SessionID
	Status          conversation.ReadbackStatus
	Availability    string
	TaskStartQueued bool
	Event           modulecore.EventEnvelope
}

type FailReadbackRequest struct {
	OperationID  string
	Lease        DelegationDispatchLease
	Now          time.Time
	DelegationID modulecore.RequestID
	Revision     uint64
	Assignee     string
	Reason       ReadbackFailureReason
	EventID      modulecore.EventID
	MessageID    modulecore.MessageID
	OccurredAt   time.Time
}

type ReadbackFailure struct {
	OperationID  string
	DelegationID modulecore.RequestID
	Revision     uint64
	OriginTurnID modulecore.TurnID
	SessionID    modulecore.SessionID
	Reason       ReadbackFailureReason
	Availability string
	Event        modulecore.EventEnvelope
}

// DelegationDispatchStore is the conversation runtime's narrow port for
// leasing, validating, accepting, and completing one private handoff.
type DelegationDispatchStore interface {
	ClaimDispatchWork(context.Context, DispatchClaimRequest) ([]DelegationDispatchLease, error)
	LoadDelegationDispatch(context.Context, DelegationDispatchLease, time.Time) (DelegationDispatchSource, error)
	AcceptReadback(context.Context, AcceptReadbackRequest) (ReadbackAcceptance, error)
	FailReadback(context.Context, FailReadbackRequest) (ReadbackFailure, error)
	AckDispatch(context.Context, DelegationDispatchLease, time.Time) error
	RetryDispatch(context.Context, DelegationDispatchLease, time.Time) error
}

type ReadbackGenerationPort interface {
	GenerateReadback(context.Context, conversation.ConversationScope, conversation.HandoffEnvelope, string) (conversation.ReadbackEnvelope, agent.InvocationReceipt, error)
}

type DelegationDispatcherConfig struct {
	PolicyVersion string
	LeaseDuration time.Duration
	Now           func() time.Time
}

type DelegationDispatcherDependencies struct {
	Store     DelegationDispatchStore
	Generator ReadbackGenerationPort
	Publisher CommittedEventPublisher
	Config    DelegationDispatcherConfig
}

type DelegationDispatchResult struct {
	Claimed       bool
	Accepted      bool
	Failed        bool
	Published     bool
	Acknowledged  bool
	WorkID        string
	DelegationID  modulecore.RequestID
	Revision      uint64
	Status        conversation.ReadbackStatus
	FailureReason ReadbackFailureReason
}

type DelegationDispatcher struct {
	store         DelegationDispatchStore
	generator     ReadbackGenerationPort
	publisher     CommittedEventPublisher
	policyVersion string
	leaseDuration time.Duration
	now           func() time.Time
}

func NewDelegationDispatcher(dependencies DelegationDispatcherDependencies) (*DelegationDispatcher, error) {
	if dependencies.Store == nil || dependencies.Generator == nil || dependencies.Publisher == nil {
		return nil, errors.New("delegation dispatcher dependencies are required")
	}
	policyVersion := strings.TrimSpace(dependencies.Config.PolicyVersion)
	if policyVersion == "" {
		return nil, errors.New("delegation dispatcher policy_version is required")
	}
	if dependencies.Config.LeaseDuration <= 0 {
		return nil, errors.New("delegation dispatcher lease duration must be positive")
	}
	now := dependencies.Config.Now
	if now == nil {
		now = time.Now
	}
	return &DelegationDispatcher{
		store: dependencies.Store, generator: dependencies.Generator, publisher: dependencies.Publisher,
		policyVersion: policyVersion, leaseDuration: dependencies.Config.LeaseDuration, now: now,
	}, nil
}

// DispatchOne claims at most one delegation handoff, asks its stored assignee
// for a readback, accepts the readback durably, publishes the canonical public
// event, then acknowledges the lease. Task creation remains behind the
// EventStore's accepted-readback transaction and is not performed here.
func (d *DelegationDispatcher) DispatchOne(ctx context.Context) (DelegationDispatchResult, error) {
	if ctx == nil {
		return DelegationDispatchResult{}, errors.New("delegation dispatch context is required")
	}
	if err := ctx.Err(); err != nil {
		return DelegationDispatchResult{}, err
	}
	if d == nil || d.store == nil || d.generator == nil || d.publisher == nil || d.now == nil {
		return DelegationDispatchResult{}, ErrDelegationDispatchUnavailable
	}
	leases, err := d.store.ClaimDispatchWork(ctx, DispatchClaimRequest{
		Kind: DispatchDelegationWorkKind, Limit: 1, LeaseDuration: d.leaseDuration, Now: d.now().UTC(),
	})
	if err != nil {
		return DelegationDispatchResult{}, fmt.Errorf("claim delegation dispatch work: %w", err)
	}
	if len(leases) == 0 {
		return DelegationDispatchResult{}, nil
	}
	if len(leases) != 1 {
		var retryErrs []error
		for _, lease := range leases {
			if err := d.retryLease(ctx, lease); err != nil {
				retryErrs = append(retryErrs, err)
			}
		}
		cause := fmt.Errorf("claim delegation dispatch returned %d leases, want at most one", len(leases))
		if len(retryErrs) != 0 {
			return DelegationDispatchResult{Claimed: true}, errors.Join(append([]error{cause}, retryErrs...)...)
		}
		return DelegationDispatchResult{Claimed: true}, cause
	}
	return d.processLease(ctx, leases[0])
}

func (d *DelegationDispatcher) processLease(ctx context.Context, lease DelegationDispatchLease) (DelegationDispatchResult, error) {
	result := DelegationDispatchResult{Claimed: true, WorkID: lease.WorkID}
	fail := func(cause error) (DelegationDispatchResult, error) {
		if retryErr := d.retryLease(ctx, lease); retryErr != nil {
			return result, errors.Join(cause, fmt.Errorf("retry delegation dispatch lease: %w", retryErr))
		}
		return result, cause
	}
	if err := validateDispatchLease(lease, d.now()); err != nil {
		return fail(err)
	}
	loaded, err := d.store.LoadDelegationDispatch(ctx, lease, d.now().UTC())
	if err != nil {
		return fail(fmt.Errorf("load guarded private delegation handoff: %w", err))
	}
	handoff := loaded.Handoff
	if err := conversation.ValidateHandoffEnvelope(handoff); err != nil {
		return fail(fmt.Errorf("validate stored delegation handoff: %w", err))
	}
	if err := validateDelegationDispatchReplySource(loaded.ResponseTo); err != nil {
		return fail(fmt.Errorf("validate accepted clarification reply source: %w", err))
	}
	if handoff.SessionID != lease.SessionID || handoff.ToActor != lease.Assignee {
		return fail(fmt.Errorf("%w: stored handoff recipient or session differs from trusted lease", ErrDelegationDispatchInvalid))
	}
	if lease.DedupeKey != delegationDispatchDedupeKey(handoff.DelegationID, handoff.Revision) {
		return fail(fmt.Errorf("%w: stored handoff revision differs from claimed dispatch key", ErrDelegationDispatchInvalid))
	}
	if loaded.AcceptedAt.IsZero() || loaded.ReadbackDeadline.IsZero() || !loaded.ReadbackDeadline.After(loaded.AcceptedAt) {
		return fail(fmt.Errorf("%w: stored readback acceptance time or deadline is invalid", ErrDelegationDispatchInvalid))
	}
	if loaded.FailureIntent != nil {
		if err := validateReadbackFailureIntent(*loaded.FailureIntent, handoff); err != nil {
			return fail(fmt.Errorf("validate persisted readback failure intent: %w", err))
		}
		return d.failReadback(ctx, lease, handoff, loaded.FailureIntent.Reason)
	}
	if !d.now().Before(loaded.ReadbackDeadline) {
		return d.failReadback(ctx, lease, handoff, ReadbackFailureExpired)
	}
	authorizedContext, err := encodeDelegationDispatchContext(loaded.Context, handoff)
	if err != nil {
		return fail(err)
	}
	scope := conversation.ConversationScope{
		TurnID: handoff.OriginTurnID, SessionID: handoff.SessionID, TraceID: handoff.SourceTraceID,
		ActorID: handoff.ToActor, InputID: handoff.InstructionMessageID,
		GenerationID: delegationReadbackOperationID(handoff.DelegationID, handoff.Revision),
		Purpose:      conversation.PurposeReadback, PolicyVersion: d.policyVersion,
	}
	generationCtx, cancelGeneration := context.WithDeadline(ctx, loaded.ReadbackDeadline)
	readback, receipt, err := d.generator.GenerateReadback(generationCtx, scope, handoff, authorizedContext)
	cancelGeneration()
	if err != nil {
		if !d.now().Before(loaded.ReadbackDeadline) {
			return d.failReadback(ctx, lease, handoff, ReadbackFailureExpired)
		}
		if receipt.Status == "invalid_output" {
			return d.failReadback(ctx, lease, handoff, ReadbackFailureInvalidOutput)
		}
		return fail(fmt.Errorf("generate assignee readback: %w", err))
	}
	if !d.now().Before(loaded.ReadbackDeadline) {
		return d.failReadback(ctx, lease, handoff, ReadbackFailureExpired)
	}
	trusted := conversation.TrustedReadbackMetadata{
		DelegationID: handoff.DelegationID, Revision: handoff.Revision, Sender: handoff.ToActor,
	}
	// Bind identity from the lease-guarded stored handoff. The model response is
	// data only, even if a custom generator returns runtime fields.
	readback.DelegationID, readback.Revision, readback.Sender = trusted.DelegationID, trusted.Revision, trusted.Sender
	if err := conversation.ValidateReadbackEnvelope(readback, handoff, trusted); err != nil {
		return d.failReadback(ctx, lease, handoff, ReadbackFailureInvalidOutput)
	}
	operationID := delegationReadbackOperationID(handoff.DelegationID, handoff.Revision)
	accepted, err := d.store.AcceptReadback(ctx, AcceptReadbackRequest{
		OperationID: operationID, DelegationID: handoff.DelegationID, Revision: handoff.Revision,
		Assignee: handoff.ToActor, Readback: readback, EventID: modulecore.NewEventID(),
		MessageID: modulecore.NewMessageID(), OccurredAt: d.now().UTC(),
	})
	if err != nil {
		return fail(fmt.Errorf("durably accept delegation readback: %w", err))
	}
	result.Accepted, result.Status = true, accepted.Status
	result.DelegationID, result.Revision = handoff.DelegationID, handoff.Revision
	if err := validateDelegationReadbackAcceptance(accepted, operationID, lease, handoff, readback, loaded.ResponseTo); err != nil {
		return fail(fmt.Errorf("validate durable readback acceptance: %w", err))
	}
	bundle := CommittedBundle{
		OperationID: accepted.OperationID, SessionID: accepted.SessionID, TurnID: accepted.OriginTurnID,
		ResultState: "completed", Events: []modulecore.EventEnvelope{accepted.Event},
	}
	if err := d.publisher.PublishCommitted(ctx, cloneCommittedBundle(bundle)); err != nil {
		return fail(fmt.Errorf("publish committed public readback: %w", err))
	}
	result.Published = true
	if err := d.store.AckDispatch(ctx, lease, d.now().UTC()); err != nil {
		return fail(fmt.Errorf("acknowledge durable delegation readback: %w", err))
	}
	result.Acknowledged = true
	return result, nil
}

func (d *DelegationDispatcher) failReadback(ctx context.Context, lease DelegationDispatchLease, handoff conversation.HandoffEnvelope, reason ReadbackFailureReason) (DelegationDispatchResult, error) {
	result := DelegationDispatchResult{
		Claimed: true, WorkID: lease.WorkID, DelegationID: handoff.DelegationID,
		Revision: handoff.Revision, Failed: true, FailureReason: reason,
	}
	if !validReadbackFailureReason(reason) {
		return result, fmt.Errorf("%w: unsupported readback failure reason", ErrDelegationDispatchInvalid)
	}
	occurredAt := d.now().UTC()
	operationID := delegationReadbackFailureOperationID(handoff.DelegationID, handoff.Revision)
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), delegationDispatchCleanupTimeout)
	defer cancel()
	failure, err := d.store.FailReadback(cleanupCtx, FailReadbackRequest{
		OperationID: operationID, Lease: lease, Now: occurredAt,
		DelegationID: handoff.DelegationID, Revision: handoff.Revision, Assignee: handoff.ToActor,
		Reason: reason, EventID: modulecore.NewEventID(), MessageID: modulecore.NewMessageID(), OccurredAt: occurredAt,
	})
	if err != nil {
		return result, fmt.Errorf("commit terminal delegation readback failure: %w", err)
	}
	if err := validateDelegationReadbackFailure(failure, operationID, lease, handoff, reason); err != nil {
		return result, fmt.Errorf("validate committed delegation readback failure: %w", err)
	}
	result.Acknowledged = true // FailReadback closes the leased outbox item atomically.
	bundle := CommittedBundle{
		OperationID: failure.OperationID, SessionID: failure.SessionID, TurnID: failure.OriginTurnID,
		ResultState: "completed", Events: []modulecore.EventEnvelope{failure.Event},
	}
	if err := d.publisher.PublishCommitted(ctx, cloneCommittedBundle(bundle)); err != nil {
		// The terminal state and event are already durable. Delivery failures are
		// recovered by the canonical event relay and must not re-run generation.
		return result, fmt.Errorf("publish committed public handoff failure: %w", err)
	}
	result.Published = true
	return result, nil
}

func validateReadbackFailureIntent(intent ReadbackFailureIntent, handoff conversation.HandoffEnvelope) error {
	if intent.DelegationID != handoff.DelegationID || intent.Revision != handoff.Revision ||
		intent.Assignee != handoff.ToActor || !validReadbackFailureReason(intent.Reason) {
		return errors.New("readback failure intent does not match the current handoff")
	}
	return nil
}

func validateDelegationReadbackFailure(failure ReadbackFailure, operationID string, lease DelegationDispatchLease, handoff conversation.HandoffEnvelope, reason ReadbackFailureReason) error {
	if failure.OperationID != operationID || failure.DelegationID != handoff.DelegationID || failure.Revision != handoff.Revision ||
		failure.OriginTurnID != handoff.OriginTurnID || failure.SessionID != handoff.SessionID ||
		failure.Reason != reason || failure.Availability != "free" {
		return errors.New("readback failure identity, reason, or availability is inconsistent")
	}
	event := failure.Event
	if err := modulecore.ValidateEventEnvelope(event); err != nil {
		return fmt.Errorf("public handoff failure event is not canonical: %w", err)
	}
	if event.EventSeq <= 0 || event.EventType != "agent.response" || event.ComponentID != "orchestrator" ||
		event.SessionID != lease.SessionID || event.TurnID != handoff.OriginTurnID || event.TraceID != handoff.SourceTraceID ||
		event.RequestID != handoff.DelegationID || event.MessageID == "" || event.ActorKind != "" || event.ActorID != "" ||
		event.TaskID != "" || event.RunID != "" || event.Payload == nil {
		return errors.New("handoff failure event is not the committed public response for this delegation")
	}
	allowedKeys := map[string]struct{}{
		"kind": {}, "status": {}, "reason": {}, "delegation_id": {}, "delegation_revision": {},
		"availability": {}, "visibility": {}, "from": {}, "source_actor_id": {}, "to": {},
		"content": {}, "text": {}, "reply_to_message_id": {},
	}
	for key := range event.Payload {
		if _, ok := allowedKeys[key]; !ok {
			return fmt.Errorf("handoff failure public event contains unexpected field %q", key)
		}
	}
	if payloadString(event.Payload, "kind") != "handoff_error" || payloadString(event.Payload, "status") != "failed" ||
		payloadString(event.Payload, "reason") != string(reason) || payloadString(event.Payload, "delegation_id") != string(handoff.DelegationID) ||
		!payloadRevisionMatches(event.Payload["delegation_revision"], handoff.Revision) ||
		payloadString(event.Payload, "availability") != "free" || payloadString(event.Payload, "visibility") != "public" ||
		payloadString(event.Payload, "from") != "mio" || payloadString(event.Payload, "source_actor_id") != "mio" ||
		payloadString(event.Payload, "to") != "user" || payloadString(event.Payload, "content") != readbackFailurePublicText ||
		payloadString(event.Payload, "text") != readbackFailurePublicText ||
		payloadString(event.Payload, "reply_to_message_id") != string(handoff.ReplyTo) {
		return errors.New("handoff failure event payload does not match the committed terminal result")
	}
	return nil
}

func validReadbackFailureReason(reason ReadbackFailureReason) bool {
	return reason == ReadbackFailureInvalidOutput || reason == ReadbackFailureExpired
}

func validateDispatchLease(lease DelegationDispatchLease, now time.Time) error {
	if lease.WorkID == "" || lease.Kind != DispatchDelegationWorkKind || lease.SourceEventID.Validate() != nil ||
		lease.SessionID.Validate() != nil || strings.TrimSpace(lease.Assignee) == "" || lease.Assignee != strings.TrimSpace(lease.Assignee) ||
		lease.LeaseToken == "" || lease.Version < 1 || lease.LeaseExpires.IsZero() || !lease.LeaseExpires.After(now) {
		return fmt.Errorf("%w: claimed dispatch lease is incomplete or expired", ErrDelegationDispatchInvalid)
	}
	return nil
}

func delegationDispatchDedupeKey(id modulecore.RequestID, revision uint64) string {
	return fmt.Sprintf("delegation:%s:%d:start", id, revision)
}

func delegationReadbackOperationID(id modulecore.RequestID, revision uint64) string {
	return fmt.Sprintf("delegation-readback:%s:%d", id, revision)
}

func delegationReadbackFailureOperationID(id modulecore.RequestID, revision uint64) string {
	return fmt.Sprintf("delegation-readback-failure:%s:%d", id, revision)
}

func encodeDelegationDispatchContext(messages []DelegationDispatchMessage, handoff conversation.HandoffEnvelope) (string, error) {
	if len(messages) == 0 || len(messages) > 256 {
		return "", fmt.Errorf("%w: authorized delegation context has an invalid message count", ErrDelegationDispatchInvalid)
	}
	seen := make(map[modulecore.MessageID]struct{}, len(messages))
	foundOrigin := false
	for _, message := range messages {
		if message.MessageID.Validate() != nil || (message.Role != "user" && message.Role != "assistant") || strings.TrimSpace(message.Text) == "" {
			return "", fmt.Errorf("%w: authorized delegation context message is invalid", ErrDelegationDispatchInvalid)
		}
		if _, duplicate := seen[message.MessageID]; duplicate {
			return "", fmt.Errorf("%w: authorized delegation context contains duplicate message IDs", ErrDelegationDispatchInvalid)
		}
		seen[message.MessageID] = struct{}{}
		if message.MessageID == handoff.OriginUserMessageID && message.Role == "user" {
			foundOrigin = true
		}
	}
	if !foundOrigin {
		return "", fmt.Errorf("%w: authorized delegation context omits the origin request", ErrDelegationDispatchInvalid)
	}
	for _, constraint := range handoff.Constraints {
		for _, sourceID := range constraint.SourceMessageIDs {
			if _, found := seen[sourceID]; !found {
				return "", fmt.Errorf("%w: authorized delegation context omits a constraint source", ErrDelegationDispatchInvalid)
			}
		}
	}
	encoded, err := json.Marshal(messages)
	if err != nil {
		return "", fmt.Errorf("encode authorized delegation context: %w", err)
	}
	if len(encoded) > maxDelegationDispatchContextBytes {
		return "", fmt.Errorf("%w: authorized delegation context exceeds %d bytes", ErrDelegationDispatchInvalid, maxDelegationDispatchContextBytes)
	}
	return string(encoded), nil
}

func validateDelegationReadbackAcceptance(
	accepted ReadbackAcceptance,
	operationID string,
	lease DelegationDispatchLease,
	handoff conversation.HandoffEnvelope,
	readback conversation.ReadbackEnvelope,
	responseTo *DelegationDispatchReplySource,
) error {
	if accepted.OperationID != operationID || accepted.DelegationID != handoff.DelegationID || accepted.Revision != handoff.Revision ||
		accepted.OriginTurnID != handoff.OriginTurnID || accepted.SessionID != handoff.SessionID || accepted.Status != readback.Status ||
		accepted.Availability != "free" || accepted.TaskStartQueued != (readback.Status == conversation.ReadbackStatusAccepted) {
		return errors.New("readback acceptance identity, status, or task gate is inconsistent")
	}
	event := accepted.Event
	if err := modulecore.ValidateEventEnvelope(event); err != nil {
		return fmt.Errorf("public readback event is not canonical: %w", err)
	}
	if event.EventSeq <= 0 || event.EventType != "agent.response" || event.ComponentID != "orchestrator" ||
		event.SessionID != lease.SessionID || event.TurnID != handoff.OriginTurnID || event.TraceID != handoff.SourceTraceID ||
		event.RequestID != handoff.DelegationID || event.MessageID == "" || event.ActorKind != "" || event.ActorID != "" ||
		event.TaskID != "" || event.RunID != "" || event.Payload == nil {
		return errors.New("readback event is not the committed public response for this handoff")
	}
	allowedKeys := map[string]struct{}{
		"kind": {}, "schema_version": {}, "status": {}, "understood_goal": {}, "acknowledged_constraint_keys": {},
		"expected_output": {}, "delegation_id": {}, "delegation_revision": {}, "availability": {}, "visibility": {},
		"from": {}, "source_actor_id": {}, "to": {}, "content": {}, "text": {}, "reply_to_message_id": {},
		"response_to_turn_id": {}, "response_to_client_submission_id": {},
	}
	for key := range event.Payload {
		if _, ok := allowedKeys[key]; !ok {
			return fmt.Errorf("readback public event contains unexpected field %q", key)
		}
	}
	expectedKind := "readback"
	if readback.Status != conversation.ReadbackStatusAccepted {
		expectedKind = "handoff_error"
	}
	if payloadString(event.Payload, "kind") != expectedKind || payloadString(event.Payload, "status") != string(readback.Status) ||
		payloadString(event.Payload, "visibility") != "public" || payloadString(event.Payload, "availability") != "free" ||
		payloadString(event.Payload, "from") != handoff.ToActor || payloadString(event.Payload, "source_actor_id") != handoff.ToActor ||
		payloadString(event.Payload, "to") != "user" || payloadString(event.Payload, "delegation_id") != string(handoff.DelegationID) ||
		payloadString(event.Payload, "schema_version") != readback.SchemaVersion || payloadString(event.Payload, "content") == "" ||
		payloadString(event.Payload, "content") != payloadString(event.Payload, "text") ||
		!strings.Contains(payloadString(event.Payload, "content"), readback.Text) {
		return errors.New("readback public event payload does not match the accepted readback")
	}
	if !payloadRevisionMatches(event.Payload["delegation_revision"], handoff.Revision) ||
		payloadString(event.Payload, "understood_goal") != readback.UnderstoodGoal ||
		payloadString(event.Payload, "expected_output") != readback.ExpectedOutput {
		return errors.New("readback public event does not preserve accepted delegation metadata")
	}
	responseTurnValue, hasResponseTurn := event.Payload["response_to_turn_id"]
	responseSubmissionValue, hasResponseSubmission := event.Payload["response_to_client_submission_id"]
	if responseTo == nil {
		if hasResponseTurn || hasResponseSubmission {
			return errors.New("readback public event has response-to metadata without an accepted clarification reply source")
		}
		return nil
	}
	if err := validateDelegationDispatchReplySource(responseTo); err != nil {
		return err
	}
	responseTurn, turnIsString := responseTurnValue.(string)
	responseSubmission, submissionIsString := responseSubmissionValue.(string)
	if !hasResponseTurn || !hasResponseSubmission || !turnIsString || !submissionIsString ||
		modulecore.TurnID(responseTurn).Validate() != nil ||
		modulecore.TurnID(responseTurn) != responseTo.TurnID ||
		strings.TrimSpace(responseSubmission) == "" || responseSubmission != strings.TrimSpace(responseSubmission) ||
		responseSubmission != responseTo.ClientSubmissionID {
		return errors.New("readback public event response-to metadata does not match its accepted clarification reply source")
	}
	return nil
}

func validateDelegationDispatchReplySource(source *DelegationDispatchReplySource) error {
	if source == nil {
		return nil
	}
	if source.TurnID.Validate() != nil || strings.TrimSpace(source.ClientSubmissionID) == "" ||
		source.ClientSubmissionID != strings.TrimSpace(source.ClientSubmissionID) {
		return errors.New("accepted clarification reply source identity is invalid")
	}
	return nil
}

func payloadRevisionMatches(value any, revision uint64) bool {
	switch number := value.(type) {
	case uint64:
		return number == revision
	case uint:
		return uint64(number) == revision
	case int64:
		return number >= 0 && uint64(number) == revision
	case int:
		return number >= 0 && uint64(number) == revision
	case float64:
		return number >= 0 && number == float64(revision)
	default:
		return false
	}
}

func (d *DelegationDispatcher) retryLease(ctx context.Context, lease DelegationDispatchLease) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), delegationDispatchCleanupTimeout)
	defer cancel()
	return d.store.RetryDispatch(cleanupCtx, lease, d.now().UTC())
}
