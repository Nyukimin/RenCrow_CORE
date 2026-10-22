package conversationruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	maxClientSubmissionIDBytes = 128
	maxDelegationContextBytes  = 64 * 1024
	safeTurnErrorText          = "応答を作成できませんでした。入力内容を保ったまま、もう一度お試しください。"
	conversationActorMio       = "mio"
)

var ErrStaleReport = errors.New("accepted report is stale or no longer current")

// CommitBundle is the application-owned persistence contract for one
// response-first result. A storage adapter maps this DTO to its journal API.
type CommitBundle struct {
	OperationID     string
	SessionID       modulecore.SessionID
	TurnID          modulecore.TurnID
	GenerationEpoch int64
	ResultState     string
	Events          []modulecore.EventEnvelope
	MessageIndexes  []MessageIndex
	Delegation      *DelegationRecord
	Outbox          []WorkItem
}

type DelegationRecord struct {
	DelegationID         string
	Revision             int64
	OriginTurnID         modulecore.TurnID
	Assignee             string
	InstructionMessageID modulecore.MessageID
	ContextEventID       modulecore.EventID
	State                string
	WorkTaskID           string
	ReportRevision       int64
}

type WorkItem struct {
	WorkID        string
	DedupeKey     string
	Kind          string
	SourceEventID modulecore.EventID
	SessionID     modulecore.SessionID
	Priority      int
	NotBefore     time.Time
}

type CommittedBundle struct {
	OperationID string
	SessionID   modulecore.SessionID
	TurnID      modulecore.TurnID
	ResultState string
	Events      []modulecore.EventEnvelope
}

// TurnJournal is the narrow application port implemented by the EventStore
// adapter. Commit and lookup are separate so an unknown commit result never
// triggers another generation call.
type TurnJournal interface {
	CommitTurnBundle(context.Context, CommitBundle) (CommittedBundle, error)
	LoadCommittedOutcome(context.Context, modulecore.TurnID) (CommittedBundle, bool, error)
}

// ReadViewBuilder supplies the complete, authorized canonical prompt history.
type ReadViewBuilder interface {
	BuildReadView(context.Context, ReadViewRequest) (ReadView, error)
}

// PromptComposer adds Mio's authored character and stable prompt to a
// canonical conversation snapshot without requiring a Task or execution Run.
type PromptComposer interface {
	ComposeConversationPrompt(context.Context, conversation.ConversationScope, agent.PromptSnapshot) (agent.PromptSnapshot, error)
}

type EmissionGenerator interface {
	GenerateEmission(context.Context, conversation.ConversationScope, agent.PromptSnapshot, conversation.EmissionAllowlist) (conversation.AgentEmission, agent.InvocationReceipt, error)
}

// CommittedEventPublisher publishes an already committed batch. Implementors
// must deduplicate by canonical event ID because recovery republishes the
// stored batch after an unknown delivery result.
type CommittedEventPublisher interface {
	PublishCommitted(context.Context, CommittedBundle) error
}

type RuntimeConfig struct {
	PolicyVersion    string
	AllowedAssignees []string
}

type RuntimeDependencies struct {
	ReadViews            ReadViewBuilder
	Composer             PromptComposer
	Generator            EmissionGenerator
	Journal              TurnJournal
	Publisher            CommittedEventPublisher
	ClarificationReplies ClarificationReplyStore
	Config               RuntimeConfig
}

type AcceptedTurnInput struct {
	ReadViewRequest ReadViewRequest
}

type TurnStatus string

const (
	TurnStatusDelivered          TurnStatus = "delivered"
	TurnStatusDeliveryPending    TurnStatus = "delivery_pending"
	TurnStatusFailedBeforeCommit TurnStatus = "failed_before_commit"
	TurnStatusCommitUncertain    TurnStatus = "commit_uncertain"
	TurnStatusStaleReport        TurnStatus = "stale_report"
)

type TurnResult struct {
	Status      TurnStatus
	ResultState string
	Committed   bool
	SafeError   bool
	Batch       CommittedBundle
	Cause       error
}

type Runtime struct {
	readViews            ReadViewBuilder
	composer             PromptComposer
	generator            EmissionGenerator
	journal              TurnJournal
	publisher            CommittedEventPublisher
	clarificationReplies ClarificationReplyStore
	config               RuntimeConfig
	locks                sessionLocks
}

func NewRuntime(dependencies RuntimeDependencies) (*Runtime, error) {
	if dependencies.ReadViews == nil || dependencies.Composer == nil || dependencies.Generator == nil || dependencies.Journal == nil || dependencies.Publisher == nil {
		return nil, errors.New("conversation runtime dependencies are required")
	}
	policyVersion := strings.TrimSpace(dependencies.Config.PolicyVersion)
	if policyVersion == "" {
		return nil, errors.New("conversation runtime policy_version is required")
	}
	seen := make(map[string]struct{}, len(dependencies.Config.AllowedAssignees))
	assignees := make([]string, 0, len(dependencies.Config.AllowedAssignees))
	for _, assignee := range dependencies.Config.AllowedAssignees {
		assignee = strings.TrimSpace(assignee)
		if assignee == "" {
			return nil, errors.New("conversation runtime allowed assignee is empty")
		}
		if _, exists := seen[assignee]; exists {
			return nil, fmt.Errorf("conversation runtime allowed assignee %q is duplicated", assignee)
		}
		seen[assignee] = struct{}{}
		assignees = append(assignees, assignee)
	}
	return &Runtime{
		readViews:            dependencies.ReadViews,
		composer:             dependencies.Composer,
		generator:            dependencies.Generator,
		journal:              dependencies.Journal,
		publisher:            dependencies.Publisher,
		clarificationReplies: dependencies.ClarificationReplies,
		config: RuntimeConfig{
			PolicyVersion: policyVersion, AllowedAssignees: assignees,
		},
	}, nil
}

// RunAcceptedTurn generates one structured Mio emission, commits its complete
// public/private result, then publishes only the returned committed batch.
// The current accepted input is supplied by BuildReadView and is not appended
// to the prompt a second time.
func (r *Runtime) RunAcceptedTurn(ctx context.Context, input AcceptedTurnInput) TurnResult {
	if ctx == nil {
		return TurnResult{Status: TurnStatusFailedBeforeCommit, Cause: errors.New("conversation runtime context is required")}
	}
	if r == nil || r.readViews == nil || r.composer == nil || r.generator == nil || r.journal == nil || r.publisher == nil {
		err := errors.New("conversation runtime is unavailable")
		markV2TimingError(ctx, 0, err)
		return TurnResult{Status: TurnStatusFailedBeforeCommit, Cause: err}
	}
	if err := validateRuntimeInput(input); err != nil {
		markV2TimingError(ctx, 0, err)
		return TurnResult{Status: TurnStatusFailedBeforeCommit, Cause: err}
	}
	accepted := input.ReadViewRequest.Accepted
	release, err := r.locks.acquire(ctx, accepted.SessionID)
	if err != nil {
		markV2TimingError(ctx, 0, err)
		return TurnResult{Status: TurnStatusFailedBeforeCommit, Cause: err}
	}
	defer release()

	if err := ctx.Err(); err != nil {
		markV2TimingError(ctx, 0, err)
		return TurnResult{Status: TurnStatusFailedBeforeCommit, Cause: err}
	}
	if accepted.TriggerKind == "clarification_control" {
		return r.runClarificationControlTurn(ctx, input)
	}
	if accepted.TriggerKind == "clarification_reply" {
		return r.runClarificationReplyTurn(ctx, input)
	}
	prior, found, err := r.journal.LoadCommittedOutcome(ctx, accepted.TurnID)
	if err != nil {
		if ctx.Err() != nil {
			markV2TimingError(ctx, 0, ctx.Err())
			return TurnResult{Status: TurnStatusFailedBeforeCommit, Cause: ctx.Err()}
		}
		markV2TimingError(ctx, 0, err)
		return TurnResult{Status: TurnStatusCommitUncertain, Cause: err}
	}
	if found {
		if err := validateCommittedBundle(prior, accepted.SessionID, accepted.TurnID); err != nil || prior.OperationID != turnOperationID(accepted.TurnID) {
			if err == nil {
				err = errors.New("committed replay operation identity is invalid")
			}
			markV2TimingError(ctx, turntiming.PointV2Commit, err)
			return TurnResult{Status: TurnStatusCommitUncertain, Cause: err}
		}
		return r.publish(ctx, prior, false, nil)
	}

	readViewTurn, readViewSpan, readViewTimed := beginV2TimingSpan(ctx, turntiming.PointV2ReadView)
	view, err := r.readViews.BuildReadView(ctx, input.ReadViewRequest)
	endV2TimingSpan(readViewTurn, readViewSpan, readViewTimed, err)
	if err != nil {
		markV2TimingError(ctx, turntiming.PointV2ReadView, err)
		if ctx.Err() != nil {
			return TurnResult{Status: TurnStatusFailedBeforeCommit, Cause: ctx.Err()}
		}
		return r.commitSafeError(ctx, input, agent.InvocationReceipt{}, err)
	}
	promptTurn, promptSpan, promptTimed := beginV2TimingSpan(ctx, turntiming.PointV2PromptComposition)
	snapshot, err := promptSnapshot(view, accepted)
	if err != nil {
		endV2TimingSpan(promptTurn, promptSpan, promptTimed, err)
		markV2TimingError(ctx, turntiming.PointV2PromptComposition, err)
		return r.commitSafeError(ctx, input, agent.InvocationReceipt{}, err)
	}
	if err := ctx.Err(); err != nil {
		endV2TimingSpan(promptTurn, promptSpan, promptTimed, err)
		markV2TimingError(ctx, turntiming.PointV2PromptComposition, err)
		return TurnResult{Status: TurnStatusFailedBeforeCommit, Cause: err}
	}

	scope := conversation.ConversationScope{
		TurnID: accepted.TurnID, SessionID: accepted.SessionID, TraceID: accepted.InputEvent.TraceID,
		ActorID: accepted.ActorID, InputID: accepted.InputMessageID,
		GenerationID: fmt.Sprintf("%s:%d", accepted.TurnID, accepted.GenerationEpoch),
		Purpose:      purposeForAcceptedTurn(accepted), PolicyVersion: r.config.PolicyVersion,
	}
	canonicalSnapshot := snapshot.Clone()
	snapshot, err = r.composer.ComposeConversationPrompt(ctx, scope, snapshot)
	if err != nil {
		endV2TimingSpan(promptTurn, promptSpan, promptTimed, err)
		markV2TimingError(ctx, turntiming.PointV2PromptComposition, err)
		if ctx.Err() != nil {
			return TurnResult{Status: TurnStatusFailedBeforeCommit, Cause: ctx.Err()}
		}
		return r.commitSafeError(ctx, input, agent.InvocationReceipt{}, err)
	}
	if err := validateComposedPrompt(snapshot, canonicalSnapshot, accepted, view); err != nil {
		endV2TimingSpan(promptTurn, promptSpan, promptTimed, err)
		markV2TimingError(ctx, turntiming.PointV2PromptComposition, err)
		return r.commitSafeError(ctx, input, agent.InvocationReceipt{}, err)
	}
	endV2TimingSpan(promptTurn, promptSpan, promptTimed, nil)
	allowlist := conversation.EmissionAllowlist{
		AllowedAssignees:        append([]string(nil), r.config.AllowedAssignees...),
		AllowedSourceMessageIDs: append([]modulecore.MessageID(nil), snapshot.AllowedSourceMessageIDs...),
	}
	generationTurn, generationSpan, generationTimed := beginV2TimingSpan(ctx, turntiming.PointV2Generation)
	emission, receipt, generationErr := r.generator.GenerateEmission(ctx, scope, snapshot, allowlist)
	generationResult := generationErr
	if ctx.Err() != nil {
		generationResult = ctx.Err()
	}
	endV2TimingSpan(generationTurn, generationSpan, generationTimed, generationResult)
	if err := ctx.Err(); err != nil {
		markV2TimingError(ctx, turntiming.PointV2Generation, err)
		return TurnResult{Status: TurnStatusFailedBeforeCommit, Cause: err}
	}
	if generationErr != nil {
		markV2TimingError(ctx, turntiming.PointV2Generation, generationErr)
		return r.commitSafeError(ctx, input, receipt, generationErr)
	}
	validated, err := validateTypedEmission(scope, allowlist, emission)
	if err != nil {
		markV2TimingError(ctx, turntiming.PointV2Generation, err)
		return r.commitSafeError(ctx, input, receipt, err)
	}
	if scope.Purpose == conversation.PurposeReportReply && validated.Kind != conversation.EmissionKindReply {
		err := errors.New("report reply must be a direct Mio response")
		markV2TimingError(ctx, turntiming.PointV2Generation, err)
		return r.commitSafeError(ctx, input, receipt, err)
	}
	prepared, err := r.prepareEmission(input, view, snapshot, scope, validated, receipt)
	if err != nil {
		markV2TimingError(ctx, turntiming.PointV2Generation, err)
		return r.commitSafeError(ctx, input, receipt, err)
	}
	if err := ctx.Err(); err != nil {
		markV2TimingError(ctx, turntiming.PointV2Generation, err)
		return TurnResult{Status: TurnStatusFailedBeforeCommit, Cause: err}
	}
	return r.commitAndPublish(ctx, prepared, accepted.SessionID, accepted.TurnID, false, nil)
}

func (r *Runtime) commitSafeError(ctx context.Context, input AcceptedTurnInput, receipt agent.InvocationReceipt, cause error) TurnResult {
	if err := ctx.Err(); err != nil {
		return TurnResult{Status: TurnStatusFailedBeforeCommit, SafeError: true, Cause: err}
	}
	accepted := input.ReadViewRequest.Accepted
	prepared := CommitBundle{
		OperationID: turnOperationID(accepted.TurnID), SessionID: accepted.SessionID,
		TurnID: accepted.TurnID, GenerationEpoch: accepted.GenerationEpoch, ResultState: "completed",
	}
	publicEvent, index, err := r.publicMessage(input, "turn_error", "turn_error", "available", "", safeTurnErrorText)
	if err != nil {
		markV2TimingError(ctx, turntiming.PointV2Commit, err)
		return TurnResult{Status: TurnStatusFailedBeforeCommit, SafeError: true, Cause: err}
	}
	prepared.Events = append(prepared.Events, publicEvent)
	prepared.MessageIndexes = append(prepared.MessageIndexes, index)
	prepared.Outbox = append(prepared.Outbox, projectionWork(accepted.SessionID, accepted.TurnID, publicEvent.EventID))
	if receipt.Status != "" || receipt.RequestID != "" || receipt.ResponseID != "" {
		prepared.Events = append(prepared.Events, r.receiptEvent(accepted, receipt))
	}
	result := r.commitAndPublish(ctx, prepared, accepted.SessionID, accepted.TurnID, true, cause)
	return result
}

func validateTypedEmission(scope conversation.ConversationScope, allowlist conversation.EmissionAllowlist, emission conversation.AgentEmission) (conversation.AgentEmission, error) {
	raw, err := json.Marshal(emission)
	if err != nil {
		return conversation.AgentEmission{}, fmt.Errorf("encode Mio emission for validation: %w", err)
	}
	return conversation.ParseAgentEmission(raw, scope, allowlist)
}

func (r *Runtime) prepareEmission(input AcceptedTurnInput, view ReadView, snapshot agent.PromptSnapshot, scope conversation.ConversationScope, emission conversation.AgentEmission, receipt agent.InvocationReceipt) (CommitBundle, error) {
	accepted := input.ReadViewRequest.Accepted
	publicKind := "reply"
	indexKind := "assistant_message"
	availability := "available"
	resultState := "completed"
	if scope.Purpose == conversation.PurposeReportReply && emission.Kind != conversation.EmissionKindReply {
		return CommitBundle{}, errors.New("report reply cannot create a delegation")
	}
	if emission.Kind == conversation.EmissionKindDelegate {
		publicKind, indexKind, availability, resultState = "delegation_notice", "delegation_notice", "pending", "awaiting_readback"
	}
	var contextEvent modulecore.EventEnvelope
	var handoff conversation.HandoffEnvelope
	var err error
	if emission.Kind == conversation.EmissionKindDelegate {
		if emission.Delegation == nil {
			return CommitBundle{}, errors.New("Mio delegation is missing")
		}
		contextEvent, handoff, err = buildDelegationContext(accepted, view, snapshot, scope, *emission.Delegation)
		if err != nil {
			return CommitBundle{}, err
		}
	}
	prepared := CommitBundle{
		OperationID: turnOperationID(accepted.TurnID), SessionID: accepted.SessionID,
		TurnID: accepted.TurnID, GenerationEpoch: accepted.GenerationEpoch, ResultState: resultState,
	}
	delegationID := ""
	if emission.Kind == conversation.EmissionKindDelegate {
		delegationID = string(handoff.DelegationID)
	}
	publicEvent, publicIndex, err := r.publicMessage(input, publicKind, indexKind, availability, delegationID, emission.UserMessage.Text)
	if err != nil {
		return CommitBundle{}, err
	}
	if emission.Kind == conversation.EmissionKindDelegate {
		publicEvent.Payload["delegation_revision"] = uint64(handoff.Revision)
	}
	prepared.Events = append(prepared.Events, publicEvent)
	prepared.MessageIndexes = append(prepared.MessageIndexes, publicIndex)
	prepared.Outbox = append(prepared.Outbox, projectionWork(accepted.SessionID, accepted.TurnID, publicEvent.EventID))
	if emission.Kind == conversation.EmissionKindDelegate {
		instructionEvent := r.instructionEvent(accepted, *emission.Delegation, handoff.InstructionMessageID, publicEvent.EventID)
		instructionIndex := messageIndexFor(instructionEvent, accepted, "agent", emission.Delegation.To, "delegation_instruction", "private")
		prepared.Events = append(prepared.Events, contextEvent, instructionEvent)
		prepared.MessageIndexes = append(prepared.MessageIndexes, instructionIndex)
		prepared.Delegation = &DelegationRecord{
			DelegationID: string(handoff.DelegationID), Revision: int64(handoff.Revision),
			OriginTurnID: accepted.TurnID, Assignee: emission.Delegation.To,
			InstructionMessageID: instructionEvent.MessageID, ContextEventID: contextEvent.EventID,
			State: "dispatch_accepted",
		}
		prepared.Outbox = append(prepared.Outbox, WorkItem{
			WorkID: "dispatch:" + string(accepted.TurnID), DedupeKey: "delegation:" + string(handoff.DelegationID) + ":1:start",
			Kind: "dispatch_delegation", SourceEventID: instructionEvent.EventID,
			SessionID: accepted.SessionID, Priority: 5,
		})
	}
	if receipt.Status != "" || receipt.RequestID != "" || receipt.ResponseID != "" {
		prepared.Events = append(prepared.Events, r.receiptEvent(accepted, receipt))
	}
	return prepared, nil
}

func buildDelegationContext(accepted AcceptedTurn, view ReadView, snapshot agent.PromptSnapshot, scope conversation.ConversationScope, delegation conversation.AgentDelegation) (modulecore.EventEnvelope, conversation.HandoffEnvelope, error) {
	selected := make(map[modulecore.MessageID]struct{}, len(delegation.SourceMessageIDs)+1)
	for _, id := range delegation.SourceMessageIDs {
		selected[id] = struct{}{}
	}
	// Preserve the original accepted request even when Mio did not cite it.
	selected[accepted.InputMessageID] = struct{}{}
	type sourceMessage struct {
		MessageID string `json:"message_id"`
		Role      string `json:"role"`
		Text      string `json:"text"`
	}
	sources := make([]sourceMessage, 0, len(selected))
	for _, message := range view.Messages {
		id := modulecore.MessageID(message.MessageID)
		if _, keep := selected[id]; keep {
			sources = append(sources, sourceMessage{MessageID: message.MessageID, Role: message.Role, Text: message.Text})
		}
	}
	if len(sources) == 0 || len(sources) > len(delegation.SourceMessageIDs)+1 {
		return modulecore.EventEnvelope{}, conversation.HandoffEnvelope{}, errors.New("delegation context source set is invalid")
	}
	contextMaterial := struct {
		SourceRevision  string          `json:"source_revision"`
		CutoverRevision string          `json:"cutover_revision"`
		Messages        []sourceMessage `json:"messages"`
	}{SourceRevision: snapshot.SourceRevision, CutoverRevision: snapshot.CutoverRevision, Messages: sources}
	encodedSources, err := json.Marshal(contextMaterial)
	if err != nil {
		return modulecore.EventEnvelope{}, conversation.HandoffEnvelope{}, err
	}
	if len(encodedSources) > maxDelegationContextBytes {
		return modulecore.EventEnvelope{}, conversation.HandoffEnvelope{}, errors.New("delegation context exceeds the bounded attachment size")
	}
	hash := sha256.Sum256(encodedSources)
	delegationID := modulecore.NewRequestID()
	instructionMessageID := modulecore.NewMessageID()
	handoff := conversation.HandoffEnvelope{
		DelegationID: delegationID, Revision: 1, OriginTurnID: accepted.TurnID,
		OriginUserMessageID: accepted.InputMessageID, SessionID: accepted.SessionID,
		SourceTraceID: scope.TraceID, FromActor: conversationActorMio, ToActor: delegation.To,
		InstructionMessageID: instructionMessageID, ContextSnapshotHash: hex.EncodeToString(hash[:]),
		AllowedCapabilities: []string{}, ReplyTo: accepted.InputMessageID,
		Goal: delegation.Goal, Instruction: delegation.Instruction,
		Constraints:    append([]conversation.AgentConstraint(nil), delegation.Constraints...),
		ExpectedOutput: delegation.ExpectedOutput,
	}
	if err := conversation.ValidateHandoffEnvelope(handoff); err != nil {
		return modulecore.EventEnvelope{}, conversation.HandoffEnvelope{}, fmt.Errorf("validate delegation handoff: %w", err)
	}
	payload := map[string]any{
		"source_revision": contextMaterial.SourceRevision, "cutover_revision": contextMaterial.CutoverRevision,
		"messages": sources, "context_snapshot_hash": handoff.ContextSnapshotHash, "handoff": handoff,
	}
	event := modulecore.NewEventEnvelope(scope.TraceID, accepted.InputEvent.EventID, nil, "conversation_runtime", "conversation.delegation_context", time.Now().UTC(), payload)
	event.SessionID, event.TurnID = accepted.SessionID, accepted.TurnID
	return event, handoff, nil
}

func (r *Runtime) publicMessage(input AcceptedTurnInput, kind, indexKind, availability, delegationID, text string) (modulecore.EventEnvelope, MessageIndex, error) {
	accepted := input.ReadViewRequest.Accepted
	replyToMessageID := accepted.InputMessageID
	var report *acceptedReport
	if accepted.TriggerKind == "report_reply" {
		parsed, err := parseAcceptedReport(accepted)
		if err != nil {
			return modulecore.EventEnvelope{}, MessageIndex{}, err
		}
		report = &parsed
		replyToMessageID = parsed.OriginMessageID
		delegationID = string(parsed.DelegationID)
		if kind == "reply" {
			kind = "report_reply"
			indexKind = "report_reply"
		}
	}
	messageID := modulecore.NewMessageID()
	payload := map[string]any{
		"kind": kind, "client_submission_id": accepted.ClientSubmissionID, "availability": availability, "visibility": "public",
		"from": conversationActorMio, "source_actor_id": conversationActorMio, "to": "user",
		"content": text, "text": text, "reply_to_message_id": string(replyToMessageID),
	}
	if delegationID != "" {
		payload["delegation_id"] = delegationID
	}
	if report != nil {
		payload["origin_trace_id"] = string(report.OriginTraceID)
		payload["origin_turn_id"] = string(report.OriginTurnID)
		payload["origin_message_id"] = string(report.OriginMessageID)
		payload["delegation_revision"] = report.DelegationRevision
	}
	event := modulecore.NewEventEnvelope(accepted.InputEvent.TraceID, accepted.InputEvent.EventID, nil, "orchestrator", "agent.response", time.Now().UTC(), payload)
	event.SessionID, event.TurnID, event.MessageID = accepted.SessionID, accepted.TurnID, messageID
	index := MessageIndex{
		MessageID: messageID, EventID: event.EventID, SessionID: accepted.SessionID, TurnID: accepted.TurnID,
		FromActor: conversationActorMio, ToKind: "user", ToActor: input.ReadViewRequest.Session.OwnerID,
		Kind: indexKind, ReplyToMessageID: replyToMessageID, Visibility: "public",
	}
	return event, index, nil
}

func purposeForAcceptedTurn(accepted AcceptedTurn) conversation.ConversationPurpose {
	if accepted.TriggerKind == "report_reply" {
		return conversation.PurposeReportReply
	}
	if accepted.TriggerKind == "clarification_control" {
		return conversation.PurposeClarificationControl
	}
	return conversation.PurposeMioTurn
}

func (r *Runtime) instructionEvent(accepted AcceptedTurn, delegation conversation.AgentDelegation, messageID modulecore.MessageID, noticeEventID modulecore.EventID) modulecore.EventEnvelope {
	payload := map[string]any{
		"from": conversationActorMio, "source_actor_id": conversationActorMio,
		"to": delegation.To, "goal": delegation.Goal, "text": delegation.Instruction,
		"constraints": delegation.Constraints, "expected_output": delegation.ExpectedOutput,
		"source_message_ids": delegation.SourceMessageIDs,
	}
	event := modulecore.NewEventEnvelope(accepted.InputEvent.TraceID, noticeEventID, nil, "delegation_mailbox", "conversation.delegation_instruction", time.Now().UTC(), payload)
	event.ActorKind, event.ActorID = "agent", conversationActorMio
	event.SessionID, event.TurnID, event.MessageID = accepted.SessionID, accepted.TurnID, messageID
	return event
}

func (r *Runtime) receiptEvent(accepted AcceptedTurn, receipt agent.InvocationReceipt) modulecore.EventEnvelope {
	payload := map[string]any{"status": receipt.Status}
	if receipt.RequestID != "" {
		payload["request_id"] = receipt.RequestID
	}
	if receipt.ResponseID != "" {
		payload["response_id"] = receipt.ResponseID
	}
	event := modulecore.NewEventEnvelope(accepted.InputEvent.TraceID, accepted.InputEvent.EventID, nil, "conversation_runtime", "conversation.mio_emission_receipt", time.Now().UTC(), payload)
	event.SessionID, event.TurnID = accepted.SessionID, accepted.TurnID
	return event
}

func messageIndexFor(event modulecore.EventEnvelope, accepted AcceptedTurn, toKind, toActor, kind, visibility string) MessageIndex {
	return MessageIndex{
		MessageID: event.MessageID, EventID: event.EventID, SessionID: accepted.SessionID, TurnID: accepted.TurnID,
		FromActor: conversationActorMio, ToKind: toKind, ToActor: toActor, Kind: kind,
		ReplyToMessageID: accepted.InputMessageID, Visibility: visibility,
	}
}

func (r *Runtime) commitAndPublish(ctx context.Context, prepared CommitBundle, sessionID modulecore.SessionID, turnID modulecore.TurnID, safeError bool, cause error) TurnResult {
	commitTurn, commitSpan, commitTimed := beginV2TimingSpan(ctx, turntiming.PointV2Commit)
	committed, err := r.journal.CommitTurnBundle(ctx, prepared)
	endV2TimingSpan(commitTurn, commitSpan, commitTimed, err)
	if err != nil {
		if errors.Is(err, ErrStaleReport) {
			// The journal's report revision fence is deterministic and runs before
			// persistence, so this output cannot be published or recovered.
			markV2TimingError(ctx, turntiming.PointV2Commit, err)
			return TurnResult{Status: TurnStatusStaleReport, Cause: err}
		}
		lookupCtx := context.WithoutCancel(ctx)
		saved, found, lookupErr := r.journal.LoadCommittedOutcome(lookupCtx, turnID)
		if lookupErr != nil {
			markV2TimingError(ctx, turntiming.PointV2Commit, lookupErr)
			return TurnResult{Status: TurnStatusCommitUncertain, SafeError: safeError, Cause: errors.Join(err, lookupErr)}
		}
		if !found {
			markV2TimingError(ctx, turntiming.PointV2Commit, err)
			return TurnResult{Status: TurnStatusCommitUncertain, SafeError: safeError, Cause: err}
		}
		if validationErr := validateCommittedBundle(saved, sessionID, turnID); validationErr != nil || saved.OperationID != prepared.OperationID || saved.ResultState != prepared.ResultState {
			if validationErr == nil {
				validationErr = errors.New("saved output does not match the prepared operation")
			}
			markV2TimingError(ctx, turntiming.PointV2Commit, validationErr)
			return TurnResult{Status: TurnStatusCommitUncertain, SafeError: safeError, Cause: errors.Join(err, validationErr)}
		}
		return r.publish(ctx, saved, safeError, cause)
	}
	if err := validateCommittedBundle(committed, sessionID, turnID); err != nil {
		markV2TimingError(ctx, turntiming.PointV2Commit, err)
		return TurnResult{Status: TurnStatusCommitUncertain, SafeError: safeError, Cause: err}
	}
	if committed.OperationID != prepared.OperationID || committed.ResultState != prepared.ResultState {
		err := errors.New("committed bundle does not match the prepared operation")
		markV2TimingError(ctx, turntiming.PointV2Commit, err)
		return TurnResult{Status: TurnStatusCommitUncertain, SafeError: safeError, Cause: err}
	}
	return r.publish(ctx, committed, safeError, cause)
}

func (r *Runtime) publish(ctx context.Context, batch CommittedBundle, safeError bool, cause error) TurnResult {
	result := TurnResult{
		Status: TurnStatusDelivered, ResultState: batch.ResultState, Committed: true,
		SafeError: safeError, Batch: cloneCommittedBundle(batch), Cause: cause,
	}
	publishTurn, publishSpan, publishTimed := beginV2TimingSpan(ctx, turntiming.PointV2Publish)
	publishErr := r.publisher.PublishCommitted(ctx, cloneCommittedBundle(batch))
	endV2TimingSpan(publishTurn, publishSpan, publishTimed, publishErr)
	if publishErr != nil {
		markV2TimingError(ctx, turntiming.PointV2Publish, publishErr)
		result.Status = TurnStatusDeliveryPending
		result.Cause = errors.Join(result.Cause, publishErr)
	}
	return result
}

func validateRuntimeInput(input AcceptedTurnInput) error {
	clientSubmissionID := input.ReadViewRequest.Accepted.ClientSubmissionID
	if len(clientSubmissionID) == 0 || len(clientSubmissionID) > maxClientSubmissionIDBytes || strings.TrimSpace(clientSubmissionID) != clientSubmissionID {
		return errors.New("client_submission_id is required and must be at most 128 bytes")
	}
	return validateReadViewRequest(input.ReadViewRequest)
}

func promptSnapshot(view ReadView, accepted AcceptedTurn) (agent.PromptSnapshot, error) {
	if view.SessionID != accepted.SessionID || len(view.Messages) == 0 {
		return agent.PromptSnapshot{}, errors.New("conversation read view does not match accepted turn")
	}
	last := view.Messages[len(view.Messages)-1]
	if last.Role != "user" || last.MessageID != string(accepted.InputMessageID) || strings.TrimSpace(last.Text) == "" {
		return agent.PromptSnapshot{}, errors.New("conversation read view does not end with the accepted user input")
	}
	seen := make(map[string]struct{}, len(view.Messages))
	snapshot := agent.PromptSnapshot{
		Messages:                make([]llm.Message, 0, len(view.Messages)),
		AllowedSourceMessageIDs: make([]modulecore.MessageID, 0, len(view.Messages)),
		SourceRevision:          fmt.Sprintf("canonical-through:%d", view.ThroughSeq),
		CutoverRevision:         fmt.Sprintf("cutover:%d", view.CutoverSeq),
	}
	for _, message := range view.Messages {
		if message.Role != "user" && message.Role != "assistant" {
			return agent.PromptSnapshot{}, errors.New("conversation read view contains an unsupported role")
		}
		id := modulecore.MessageID(message.MessageID)
		if err := id.Validate(); err != nil {
			return agent.PromptSnapshot{}, errors.New("conversation read view contains an invalid message identity")
		}
		if strings.TrimSpace(message.Text) == "" {
			return agent.PromptSnapshot{}, errors.New("conversation read view contains an empty message")
		}
		if _, exists := seen[message.MessageID]; exists {
			return agent.PromptSnapshot{}, errors.New("conversation read view contains a duplicate message identity")
		}
		seen[message.MessageID] = struct{}{}
		snapshot.Messages = append(snapshot.Messages, llm.Message{Role: message.Role, Content: message.Text})
		snapshot.AllowedSourceMessageIDs = append(snapshot.AllowedSourceMessageIDs, id)
	}
	return snapshot, nil
}

func validateComposedPrompt(snapshot, canonical agent.PromptSnapshot, accepted AcceptedTurn, view ReadView) error {
	if len(snapshot.Messages) == 0 || snapshot.Messages[len(snapshot.Messages)-1].Role != "user" ||
		snapshot.Messages[len(snapshot.Messages)-1].Content != view.Messages[len(view.Messages)-1].Text {
		return errors.New("Mio prompt composition must keep the accepted input last")
	}
	if len(snapshot.AllowedSourceMessageIDs) != len(canonical.AllowedSourceMessageIDs) {
		return errors.New("Mio prompt composition changed the canonical source allowlist")
	}
	for index, messageID := range canonical.AllowedSourceMessageIDs {
		if snapshot.AllowedSourceMessageIDs[index] != messageID {
			return errors.New("Mio prompt composition changed the canonical source allowlist")
		}
	}
	if snapshot.SourceRevision != canonical.SourceRevision || snapshot.CutoverRevision != canonical.CutoverRevision {
		return errors.New("Mio prompt composition changed the canonical source revision")
	}
	nonSystem := make([]llm.Message, 0, len(canonical.Messages))
	for _, message := range snapshot.Messages {
		if message.Role != "system" {
			nonSystem = append(nonSystem, message)
		}
	}
	if !samePromptMessages(nonSystem, canonical.Messages) {
		return errors.New("Mio prompt composition changed the canonical conversation history")
	}
	if snapshot.Messages[len(snapshot.Messages)-1].Content == "" || string(accepted.InputMessageID) == "" ||
		snapshot.AllowedSourceMessageIDs[len(snapshot.AllowedSourceMessageIDs)-1] != accepted.InputMessageID {
		return errors.New("Mio prompt composition removed the accepted input")
	}
	return nil
}

func samePromptMessages(left, right []llm.Message) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Role != right[index].Role || left[index].Content != right[index].Content {
			return false
		}
	}
	return true
}

func projectionWork(sessionID modulecore.SessionID, turnID modulecore.TurnID, sourceEventID modulecore.EventID) WorkItem {
	return WorkItem{
		WorkID: "projection:" + string(turnID), DedupeKey: "conversation_projection:" + string(turnID),
		Kind: "conversation_projection", SourceEventID: sourceEventID, SessionID: sessionID, Priority: 10,
	}
}

func turnOperationID(turnID modulecore.TurnID) string { return "conversation.output:" + string(turnID) }

func validateCommittedBundle(batch CommittedBundle, sessionID modulecore.SessionID, turnID modulecore.TurnID) error {
	if batch.OperationID == "" || batch.SessionID != sessionID || batch.TurnID != turnID ||
		(batch.ResultState != "completed" && batch.ResultState != "awaiting_readback") || len(batch.Events) == 0 {
		return errors.New("committed bundle identity or state is invalid")
	}
	var previous modulecore.EventSeq
	for index, event := range batch.Events {
		if err := modulecore.ValidateEventEnvelope(event); err != nil {
			return fmt.Errorf("committed event envelope is invalid: %w", err)
		}
		if event.EventSeq <= 0 || event.SessionID != sessionID || event.TurnID != turnID || (index > 0 && event.EventSeq <= previous) {
			return errors.New("committed event sequence or identity is invalid")
		}
		previous = event.EventSeq
	}
	return nil
}

func cloneCommittedBundle(batch CommittedBundle) CommittedBundle {
	cloned := batch
	cloned.Events = append([]modulecore.EventEnvelope(nil), batch.Events...)
	for index := range cloned.Events {
		cloned.Events[index].DependencyEventIDs = append([]modulecore.EventID(nil), batch.Events[index].DependencyEventIDs...)
		if batch.Events[index].Payload != nil {
			cloned.Events[index].Payload = make(map[string]any, len(batch.Events[index].Payload))
			for key, value := range batch.Events[index].Payload {
				cloned.Events[index].Payload[key] = value
			}
		}
	}
	return cloned
}

func beginV2TimingSpan(ctx context.Context, point uint16) (turntiming.TurnRecorder, turntiming.Span, bool) {
	turn, ok := turntiming.BatchTurnFromContext(ctx)
	if !ok {
		return turntiming.TurnRecorder{}, turntiming.Span{}, false
	}
	span, ok := turn.Begin(point, turntiming.ComponentV2ConversationRuntime, 1, 0, turntiming.Span{})
	return turn, span, ok
}

func endV2TimingSpan(turn turntiming.TurnRecorder, span turntiming.Span, active bool, err error) {
	if active {
		turn.End(span, v2TimingResult(err))
	}
}

func markV2TimingError(ctx context.Context, failedPoint uint16, err error) {
	if err == nil {
		return
	}
	turn, ok := turntiming.BatchTurnFromContext(ctx)
	if !ok {
		return
	}
	turn.MarkResult(turntiming.PointV2Error, turntiming.ComponentV2ConversationRuntime, 1, uint32(failedPoint), v2TimingResult(err))
}

func v2TimingResult(err error) turntiming.Result {
	switch {
	case err == nil:
		return turntiming.ResultOK
	case errors.Is(err, context.Canceled):
		return turntiming.ResultCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return turntiming.ResultTimeout
	default:
		return turntiming.ResultError
	}
}

type sessionLocks struct {
	mu      sync.Mutex
	entries map[modulecore.SessionID]*sessionLockEntry
}

type sessionLockEntry struct {
	semaphore chan struct{}
	refs      int
}

func (locks *sessionLocks) acquire(ctx context.Context, sessionID modulecore.SessionID) (func(), error) {
	locks.mu.Lock()
	if locks.entries == nil {
		locks.entries = make(map[modulecore.SessionID]*sessionLockEntry)
	}
	entry := locks.entries[sessionID]
	if entry == nil {
		entry = &sessionLockEntry{semaphore: make(chan struct{}, 1)}
		locks.entries[sessionID] = entry
	}
	entry.refs++
	locks.mu.Unlock()
	select {
	case entry.semaphore <- struct{}{}:
		return func() {
			<-entry.semaphore
			locks.releaseRef(sessionID, entry)
		}, nil
	case <-ctx.Done():
		locks.releaseRef(sessionID, entry)
		return nil, ctx.Err()
	}
}

func (locks *sessionLocks) releaseRef(sessionID modulecore.SessionID, entry *sessionLockEntry) {
	locks.mu.Lock()
	entry.refs--
	if entry.refs == 0 && locks.entries[sessionID] == entry {
		delete(locks.entries, sessionID)
	}
	locks.mu.Unlock()
}
