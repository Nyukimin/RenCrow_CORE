package conversationruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const clarificationDecisionOperationPrefix = "clarification.decision:"

// ClarificationControlContextRequest asks the EventStore adapter for the
// exact persisted clarification question and handoff bound to an accepted
// control Turn. OwnerID and Assignee come from the trusted runtime, not from
// the question payload.
type ClarificationControlContextRequest struct {
	SessionID     modulecore.SessionID
	ControlTurnID modulecore.TurnID
	OwnerID       string
	Assignee      string
}

// StoredClarificationControlContext is the storage-validated context for one
// accepted clarification control Turn. Context contains the exact source
// messages captured by the original handoff.
type StoredClarificationControlContext struct {
	ControlTurn       AcceptedTurn
	OwnerID           string
	OriginUserActorID string
	Handoff           conversation.HandoffEnvelope
	QuestionEvent     modulecore.EventEnvelope
	QuestionMessage   MessageIndex
	Context           []DelegationDispatchMessage
}

// ClarificationControlContextReader is implemented by the EventStore adapter.
// It must recheck the current delegation revision, control Turn, owner, and
// private assignee-to-Mio question in one trusted read.
type ClarificationControlContextReader interface {
	LoadClarificationControlContext(context.Context, ClarificationControlContextRequest) (StoredClarificationControlContext, error)
}

// ClarificationDecisionCommitInput carries a typed Mio decision and
// runtime-issued event identities to the EventStore's fenced unit of work.
type ClarificationDecisionCommitInput struct {
	OperationID       string
	SessionID         modulecore.SessionID
	ControlTurnID     modulecore.TurnID
	DelegationID      modulecore.RequestID
	Revision          uint64
	GenerationEpoch   int64
	Decision          conversation.ClarificationDecision
	InvocationReceipt agent.InvocationReceipt
	EventID           modulecore.EventID
	MessageID         modulecore.MessageID
	ContextEventID    modulecore.EventID
	ReceiptEventID    modulecore.EventID
	OccurredAt        time.Time
}

// ClarificationTerminalCommitInput records a safe, durable terminal for a
// clarification control Turn whose generation failed, output was invalid, or
// same-assignee continuation reached the stored revision limit.
type ClarificationTerminalCommitInput struct {
	OperationID       string
	SessionID         modulecore.SessionID
	ControlTurnID     modulecore.TurnID
	DelegationID      modulecore.RequestID
	Revision          uint64
	GenerationEpoch   int64
	Reason            conversation.ClarificationTerminalReason
	AttemptedKind     conversation.ClarificationDecisionKind
	InvocationReceipt agent.InvocationReceipt
	EventID           modulecore.EventID
	MessageID         modulecore.MessageID
	ReceiptEventID    modulecore.EventID
	OccurredAt        time.Time
}

// ClarificationDecisionCommitResult is the exact durable decision receipt.
// Events may include the original handoff Turn during continuation, so this
// result is deliberately separate from the ordinary same-Turn commit bundle.
type ClarificationDecisionCommitResult struct {
	OperationID       string
	ControlTurnID     modulecore.TurnID
	DelegationID      modulecore.RequestID
	Revision          uint64
	Kind              conversation.ClarificationDecisionKind
	TerminalReason    conversation.ClarificationTerminalReason
	ResultState       string
	InvocationReceipt agent.InvocationReceipt
	Event             modulecore.EventEnvelope
	ReceiptEvent      modulecore.EventEnvelope
	Message           MessageIndex
	NextHandoff       *conversation.HandoffEnvelope
	DispatchQueued    bool
	Events            []modulecore.EventEnvelope
	MessageIndexes    []MessageIndex
}

// ClarificationDecisionJournal persists a decision with a current-revision
// fence and can reload its exact result after an uncertain commit or restart.
type ClarificationDecisionJournal interface {
	CommitClarificationDecision(context.Context, ClarificationDecisionCommitInput) (ClarificationDecisionCommitResult, error)
	CommitClarificationTerminal(context.Context, ClarificationTerminalCommitInput) (ClarificationDecisionCommitResult, error)
	LoadClarificationDecisionOutcome(context.Context, modulecore.TurnID) (ClarificationDecisionCommitResult, bool, error)
}

// ClarificationDecisionGenerator is Mio's one-call typed control decision
// route. It cannot choose an assignee or mutate the stored handoff contract.
type ClarificationDecisionGenerator interface {
	GenerateClarificationDecision(context.Context, conversation.ConversationScope, agent.PromptSnapshot, conversation.ClarificationControlContext, conversation.EmissionAllowlist) (conversation.ClarificationDecision, agent.InvocationReceipt, error)
}

func validateStoredClarificationControlContext(stored StoredClarificationControlContext, request ReadViewRequest, messages []PromptMessage) (conversation.ClarificationControlContext, error) {
	invalid := func(message string) (conversation.ClarificationControlContext, error) {
		return conversation.ClarificationControlContext{}, fmt.Errorf("%w: %s", ErrReadViewInvalid, message)
	}
	accepted := request.Accepted
	if stored.OwnerID != request.Session.OwnerID || !reflect.DeepEqual(stored.ControlTurn, accepted) ||
		!reflect.DeepEqual(stored.QuestionEvent, accepted.InputEvent) {
		return invalid("stored clarification control Turn or owner does not match the accepted input")
	}
	handoff := stored.Handoff
	if err := conversation.ValidateHandoffEnvelope(handoff); err != nil {
		return invalid("stored clarification handoff is invalid")
	}
	questionText, err := inputText(accepted.InputEvent)
	if err != nil {
		return invalid("accepted private clarification question is invalid")
	}
	revision, revisionOK := payloadUint64(accepted.InputEvent.Payload["delegation_revision"])
	questionIndex := stored.QuestionMessage
	if !revisionOK || handoff.DelegationID != accepted.InputEvent.RequestID || handoff.Revision != revision ||
		handoff.OriginTurnID == accepted.TurnID || handoff.SessionID != accepted.SessionID ||
		handoff.SourceTraceID != accepted.InputEvent.TraceID || handoff.ToActor != accepted.ActorID ||
		questionIndex.MessageID != accepted.InputMessageID || questionIndex.EventID != accepted.InputEvent.EventID ||
		questionIndex.SessionID != accepted.SessionID || questionIndex.TurnID != accepted.TurnID ||
		questionIndex.FromActor != accepted.ActorID || questionIndex.ToKind != "agent" || questionIndex.ToActor != "mio" ||
		questionIndex.Kind != "clarification_question" || questionIndex.Visibility != "private" ||
		questionIndex.ReplyToMessageID != handoff.ReplyTo ||
		accepted.InputEvent.EventType != "conversation.clarification_question" ||
		accepted.InputEvent.ComponentID != "conversation_runtime" || accepted.InputEvent.ActorKind != "" || accepted.InputEvent.ActorID != "" ||
		accepted.InputEvent.RequestID != handoff.DelegationID ||
		payloadString(accepted.InputEvent.Payload, "visibility") != "private" ||
		payloadString(accepted.InputEvent.Payload, "kind") != "clarification_question" ||
		payloadString(accepted.InputEvent.Payload, "status") != "needs_clarification" ||
		payloadString(accepted.InputEvent.Payload, "from") != accepted.ActorID ||
		payloadString(accepted.InputEvent.Payload, "source_actor_id") != accepted.ActorID ||
		payloadString(accepted.InputEvent.Payload, "to") != "mio" ||
		payloadString(accepted.InputEvent.Payload, "text") != questionText ||
		payloadString(accepted.InputEvent.Payload, "content") != questionText {
		return invalid("stored clarification handoff or private question binding is inconsistent")
	}
	if len(messages) == 0 {
		return invalid("clarification prompt has no retained conversation context")
	}
	last := messages[len(messages)-1]
	if last.Role != "user" || last.MessageID != string(accepted.InputMessageID) || last.Text != questionText || last.TurnID != accepted.TurnID {
		return invalid("clarification prompt does not end with the accepted private question")
	}
	priorMessages := messages[:len(messages)-1]
	visible := make(map[modulecore.MessageID]PromptMessage, len(priorMessages))
	visibleOrder := make(map[modulecore.MessageID]int, len(priorMessages))
	for index, message := range priorMessages {
		id := modulecore.MessageID(message.MessageID)
		if id.Validate() != nil || (message.Role != "user" && message.Role != "assistant") || strings.TrimSpace(message.Text) == "" {
			return invalid("clarification retained conversation contains an invalid source message")
		}
		visible[id] = message
		visibleOrder[id] = index
	}
	if len(stored.Context) == 0 || len(stored.Context) > conversation.MaxSourceMessageIDs+1 {
		return invalid("stored clarification source context is empty or oversized")
	}
	seen := make(map[modulecore.MessageID]struct{}, len(stored.Context))
	var contextBytes int
	previousOrder := -1
	originalFound := false
	for _, source := range stored.Context {
		if source.MessageID.Validate() != nil || (source.Role != "user" && source.Role != "assistant") ||
			!utf8.ValidString(source.Text) || strings.TrimSpace(source.Text) == "" {
			return invalid("stored clarification source message is invalid")
		}
		if _, duplicate := seen[source.MessageID]; duplicate {
			return invalid("stored clarification source context contains duplicate messages")
		}
		seen[source.MessageID] = struct{}{}
		contextBytes += len(source.MessageID) + len(source.Role) + len([]byte(source.Text))
		if contextBytes > maxDelegationContextBytes {
			return invalid("stored clarification source context exceeds its byte bound")
		}
		message, found := visible[source.MessageID]
		order := visibleOrder[source.MessageID]
		if !found || order <= previousOrder || message.Role != source.Role || message.Text != source.Text {
			return invalid("stored clarification source context differs from the canonical prompt history")
		}
		previousOrder = order
		if source.MessageID == handoff.OriginUserMessageID {
			if source.Role != "user" {
				return invalid("stored clarification origin request is not a user message")
			}
			originalFound = true
		}
	}
	if !originalFound {
		return invalid("stored clarification source context omits the original user request")
	}
	for _, constraint := range handoff.Constraints {
		for _, sourceID := range constraint.SourceMessageIDs {
			if _, found := seen[sourceID]; !found {
				return invalid("stored clarification context omits a constrained source message")
			}
		}
	}
	control := conversation.ClarificationControlContext{
		Handoff: handoff, QuestionDelegationID: handoff.DelegationID,
		QuestionRevision: handoff.Revision, QuestionMessageID: accepted.InputMessageID,
		QuestionSender: accepted.ActorID, QuestionText: questionText,
	}
	scope := conversation.ConversationScope{
		TurnID: accepted.TurnID, SessionID: accepted.SessionID, TraceID: accepted.InputEvent.TraceID,
		ActorID: accepted.ActorID, InputID: accepted.InputMessageID,
		GenerationID: fmt.Sprintf("%s:%d", accepted.TurnID, accepted.GenerationEpoch),
		Purpose:      conversation.PurposeClarificationControl, PolicyVersion: "read-view-validation",
	}
	if err := control.Validate(scope); err != nil {
		return invalid("clarification control context failed its domain binding")
	}
	return control, nil
}

func (r *Runtime) runClarificationControlTurn(ctx context.Context, input AcceptedTurnInput) TurnResult {
	accepted := input.ReadViewRequest.Accepted
	journal, ok := r.journal.(ClarificationDecisionJournal)
	if !ok {
		return clarificationFailed(ctx, errors.New("clarification decision journal is unavailable"))
	}

	loadTurn, loadSpan, loadTimed := beginV2TimingSpan(ctx, turntiming.PointV2Commit)
	prior, found, err := journal.LoadClarificationDecisionOutcome(ctx, accepted.TurnID)
	endV2TimingSpan(loadTurn, loadSpan, loadTimed, err)
	if err != nil {
		markV2TimingError(ctx, turntiming.PointV2Commit, err)
		return TurnResult{Status: TurnStatusCommitUncertain, Cause: err}
	}
	if found {
		if err := validateClarificationDecisionResult(prior, input, nil, nil); err != nil {
			markV2TimingError(ctx, turntiming.PointV2Commit, err)
			return TurnResult{Status: TurnStatusCommitUncertain, Cause: err}
		}
		return r.publishClarification(ctx, prior, nil)
	}
	if err := ctx.Err(); err != nil {
		return clarificationFailed(ctx, err)
	}
	readTurn, readSpan, readTimed := beginV2TimingSpan(ctx, turntiming.PointV2ReadView)
	view, err := r.readViews.BuildReadView(ctx, input.ReadViewRequest)
	endV2TimingSpan(readTurn, readSpan, readTimed, err)
	if err != nil {
		markV2TimingError(ctx, turntiming.PointV2ReadView, err)
		return clarificationFailed(ctx, err)
	}
	if view.ClarificationControl == nil {
		return clarificationFailed(ctx, fmt.Errorf("%w: validated clarification control context is missing", ErrReadViewInvalid))
	}
	if !containsExactString(r.config.AllowedAssignees, view.ClarificationControl.Handoff.ToActor) {
		return clarificationFailed(ctx, fmt.Errorf("%w: clarification assignee is not allowed", ErrReadViewInvalid))
	}

	scope := conversation.ConversationScope{
		TurnID: accepted.TurnID, SessionID: accepted.SessionID, TraceID: accepted.InputEvent.TraceID,
		ActorID: accepted.ActorID, InputID: accepted.InputMessageID,
		GenerationID: fmt.Sprintf("%s:%d", accepted.TurnID, accepted.GenerationEpoch),
		Purpose:      conversation.PurposeClarificationControl, PolicyVersion: r.config.PolicyVersion,
	}
	control := *view.ClarificationControl
	if err := validateRuntimeClarificationControl(scope, accepted, control); err != nil {
		return clarificationFailed(ctx, err)
	}
	generator, ok := r.generator.(ClarificationDecisionGenerator)
	if !ok {
		cause := errors.New("clarification decision generator is unavailable")
		return r.commitClarificationTerminal(ctx, input, conversation.ClarificationTerminalGenerationError, "", agent.InvocationReceipt{Status: "generation_failed"}, cause)
	}

	promptTurn, promptSpan, promptTimed := beginV2TimingSpan(ctx, turntiming.PointV2PromptComposition)
	canonicalSnapshot, err := promptSnapshot(view, accepted)
	if err != nil {
		endV2TimingSpan(promptTurn, promptSpan, promptTimed, err)
		markV2TimingError(ctx, turntiming.PointV2PromptComposition, err)
		return clarificationFailed(ctx, err)
	}
	snapshot, err := r.composer.ComposeConversationPrompt(ctx, scope, canonicalSnapshot.Clone())
	if err != nil {
		endV2TimingSpan(promptTurn, promptSpan, promptTimed, err)
		markV2TimingError(ctx, turntiming.PointV2PromptComposition, err)
		return clarificationFailed(ctx, err)
	}
	if err := validateComposedPrompt(snapshot, canonicalSnapshot, accepted, view); err != nil {
		endV2TimingSpan(promptTurn, promptSpan, promptTimed, err)
		markV2TimingError(ctx, turntiming.PointV2PromptComposition, err)
		return clarificationFailed(ctx, err)
	}
	// ComposeConversationPrompt receives the accepted private question as the
	// final user input. The typed generator appends that question with explicit
	// private-control metadata, so remove only that final canonical input here.
	if len(snapshot.Messages) <= 1 || len(snapshot.AllowedSourceMessageIDs) <= 1 ||
		snapshot.AllowedSourceMessageIDs[len(snapshot.AllowedSourceMessageIDs)-1] != accepted.InputMessageID ||
		snapshot.Messages[len(snapshot.Messages)-1].Role != "user" ||
		snapshot.Messages[len(snapshot.Messages)-1].Content != control.QuestionText {
		endV2TimingSpan(promptTurn, promptSpan, promptTimed, errors.New("clarification composer did not retain the private question as its final input"))
		return clarificationFailed(ctx, fmt.Errorf("%w: composed prompt lost the final private question", ErrReadViewInvalid))
	}
	snapshot.Messages = append([]llm.Message(nil), snapshot.Messages[:len(snapshot.Messages)-1]...)
	snapshot.AllowedSourceMessageIDs = append([]modulecore.MessageID(nil), snapshot.AllowedSourceMessageIDs[:len(snapshot.AllowedSourceMessageIDs)-1]...)
	if len(snapshot.AllowedSourceMessageIDs) == 0 {
		endV2TimingSpan(promptTurn, promptSpan, promptTimed, errors.New("clarification prompt lost all canonical sources"))
		return clarificationFailed(ctx, fmt.Errorf("%w: clarification prompt has no retained source context", ErrReadViewInvalid))
	}
	endV2TimingSpan(promptTurn, promptSpan, promptTimed, nil)
	allowlist := conversation.EmissionAllowlist{
		AllowedAssignees:        append([]string(nil), r.config.AllowedAssignees...),
		AllowedSourceMessageIDs: append([]modulecore.MessageID(nil), snapshot.AllowedSourceMessageIDs...),
	}

	generationTurn, generationSpan, generationTimed := beginV2TimingSpan(ctx, turntiming.PointV2Generation)
	decision, receipt, generationErr := generator.GenerateClarificationDecision(ctx, scope, snapshot, control, allowlist)
	generationResult := generationErr
	if ctx.Err() != nil {
		generationResult = ctx.Err()
	}
	endV2TimingSpan(generationTurn, generationSpan, generationTimed, generationResult)
	if err := ctx.Err(); err != nil {
		markV2TimingError(ctx, turntiming.PointV2Generation, err)
		return clarificationFailed(ctx, err)
	}
	if generationErr != nil {
		markV2TimingError(ctx, turntiming.PointV2Generation, generationErr)
		reason := conversation.ClarificationTerminalGenerationError
		if receipt.Status == "invalid_output" {
			reason = conversation.ClarificationTerminalInvalidOutput
		}
		return r.commitClarificationTerminal(ctx, input, reason, "", receipt, generationErr)
	}
	decision, err = validateClarificationDecision(scope, decision)
	if err != nil {
		markV2TimingError(ctx, turntiming.PointV2Generation, err)
		return r.commitClarificationTerminal(ctx, input, conversation.ClarificationTerminalInvalidOutput, "", receipt, err)
	}
	if decision.Kind == conversation.ClarificationDecisionContinueDelegation && control.Handoff.Revision >= conversation.MaxClarificationRevision {
		return r.commitClarificationTerminal(ctx, input, conversation.ClarificationTerminalRevisionLimit, decision.Kind, receipt, nil)
	}

	commitInput := ClarificationDecisionCommitInput{
		OperationID: clarificationDecisionOperationPrefix + string(accepted.TurnID),
		SessionID:   accepted.SessionID, ControlTurnID: accepted.TurnID,
		DelegationID: control.Handoff.DelegationID, Revision: control.Handoff.Revision,
		GenerationEpoch: accepted.GenerationEpoch, Decision: decision, InvocationReceipt: receipt,
		EventID: modulecore.NewEventID(), MessageID: modulecore.NewMessageID(), ReceiptEventID: modulecore.NewEventID(),
		OccurredAt: time.Now().UTC(),
	}
	if decision.Kind == conversation.ClarificationDecisionContinueDelegation {
		commitInput.ContextEventID = modulecore.NewEventID()
	}
	commitTurn, commitSpan, commitTimed := beginV2TimingSpan(ctx, turntiming.PointV2Commit)
	committed, commitErr := journal.CommitClarificationDecision(ctx, commitInput)
	endV2TimingSpan(commitTurn, commitSpan, commitTimed, commitErr)
	if commitErr != nil {
		lookupCtx := context.WithoutCancel(ctx)
		saved, savedFound, lookupErr := journal.LoadClarificationDecisionOutcome(lookupCtx, accepted.TurnID)
		if lookupErr != nil {
			markV2TimingError(ctx, turntiming.PointV2Commit, lookupErr)
			return TurnResult{Status: TurnStatusCommitUncertain, Cause: errors.Join(commitErr, lookupErr)}
		}
		if !savedFound {
			markV2TimingError(ctx, turntiming.PointV2Commit, commitErr)
			return TurnResult{Status: TurnStatusCommitUncertain, Cause: commitErr}
		}
		if err := validateClarificationDecisionResult(saved, input, &control, &decision); err != nil {
			markV2TimingError(ctx, turntiming.PointV2Commit, err)
			return TurnResult{Status: TurnStatusCommitUncertain, Cause: errors.Join(commitErr, err)}
		}
		return r.publishClarification(ctx, saved, commitErr)
	}
	if err := validateClarificationDecisionResult(committed, input, &control, &decision); err != nil {
		markV2TimingError(ctx, turntiming.PointV2Commit, err)
		return TurnResult{Status: TurnStatusCommitUncertain, Cause: err}
	}
	return r.publishClarification(ctx, committed, nil)
}

func (r *Runtime) commitClarificationTerminal(ctx context.Context, input AcceptedTurnInput, reason conversation.ClarificationTerminalReason, attemptedKind conversation.ClarificationDecisionKind, receipt agent.InvocationReceipt, cause error) TurnResult {
	journal, ok := r.journal.(ClarificationDecisionJournal)
	if !ok {
		return clarificationFailed(ctx, errors.New("clarification terminal journal is unavailable"))
	}
	accepted := input.ReadViewRequest.Accepted
	revision, revisionOK := payloadUint64(accepted.InputEvent.Payload["delegation_revision"])
	status, validReason := clarificationTerminalStatus(reason)
	if !revisionOK || !validReason {
		return clarificationFailed(ctx, errors.New("clarification terminal input has an invalid reason or revision"))
	}
	if receipt.Status == "" || receipt.Status == "completed" {
		receipt.Status = status
	}
	if receipt.Status != status {
		return clarificationFailed(ctx, errors.New("clarification terminal receipt status does not match its reason"))
	}
	terminalInput := ClarificationTerminalCommitInput{
		OperationID: clarificationDecisionOperationPrefix + string(accepted.TurnID),
		SessionID:   accepted.SessionID, ControlTurnID: accepted.TurnID,
		DelegationID: accepted.InputEvent.RequestID, Revision: revision,
		GenerationEpoch: accepted.GenerationEpoch, Reason: reason, AttemptedKind: attemptedKind,
		InvocationReceipt: receipt, EventID: modulecore.NewEventID(), MessageID: modulecore.NewMessageID(),
		ReceiptEventID: modulecore.NewEventID(), OccurredAt: time.Now().UTC(),
	}
	commitCtx := ctx
	if commitCtx == nil || commitCtx.Err() != nil {
		commitCtx = context.WithoutCancel(ctx)
	}
	commitTurn, commitSpan, commitTimed := beginV2TimingSpan(commitCtx, turntiming.PointV2Commit)
	committed, commitErr := journal.CommitClarificationTerminal(commitCtx, terminalInput)
	endV2TimingSpan(commitTurn, commitSpan, commitTimed, commitErr)
	if commitErr != nil {
		lookupCtx := context.WithoutCancel(ctx)
		saved, found, lookupErr := journal.LoadClarificationDecisionOutcome(lookupCtx, accepted.TurnID)
		if lookupErr != nil {
			markV2TimingError(ctx, turntiming.PointV2Commit, lookupErr)
			return TurnResult{Status: TurnStatusCommitUncertain, Cause: errors.Join(cause, commitErr, lookupErr)}
		}
		if !found {
			markV2TimingError(ctx, turntiming.PointV2Commit, commitErr)
			return TurnResult{Status: TurnStatusCommitUncertain, Cause: errors.Join(cause, commitErr)}
		}
		if saved.TerminalReason != "" {
			if err := validateClarificationTerminalResult(saved, input, nil); err != nil {
				markV2TimingError(ctx, turntiming.PointV2Commit, err)
				return TurnResult{Status: TurnStatusCommitUncertain, Cause: errors.Join(cause, commitErr, err)}
			}
		} else if err := validateClarificationDecisionResult(saved, input, nil, nil); err != nil {
			markV2TimingError(ctx, turntiming.PointV2Commit, err)
			return TurnResult{Status: TurnStatusCommitUncertain, Cause: errors.Join(cause, commitErr, err)}
		}
		return r.publishClarification(ctx, saved, errors.Join(cause, commitErr))
	}
	if err := validateClarificationTerminalResult(committed, input, nil); err != nil {
		markV2TimingError(ctx, turntiming.PointV2Commit, err)
		return TurnResult{Status: TurnStatusCommitUncertain, Cause: errors.Join(cause, err)}
	}
	return r.publishClarification(ctx, committed, cause)
}

func clarificationTerminalStatus(reason conversation.ClarificationTerminalReason) (string, bool) {
	switch reason {
	case conversation.ClarificationTerminalGenerationError:
		return "generation_failed", true
	case conversation.ClarificationTerminalInvalidOutput:
		return "invalid_output", true
	case conversation.ClarificationTerminalRevisionLimit:
		return "completed", true
	default:
		return "", false
	}
}

func validateRuntimeClarificationControl(scope conversation.ConversationScope, accepted AcceptedTurn, control conversation.ClarificationControlContext) error {
	if err := control.Validate(scope); err != nil {
		return fmt.Errorf("%w: clarification context does not match accepted private question: %v", ErrReadViewInvalid, err)
	}
	question, err := inputText(accepted.InputEvent)
	acceptedRevision, revisionOK := payloadUint64(accepted.InputEvent.Payload["delegation_revision"])
	if err != nil || !revisionOK || control.QuestionText != question || control.QuestionMessageID != accepted.InputMessageID ||
		control.QuestionDelegationID != accepted.InputEvent.RequestID || control.QuestionRevision != acceptedRevision ||
		control.Handoff.DelegationID != accepted.InputEvent.RequestID || control.Handoff.ToActor != accepted.ActorID {
		return fmt.Errorf("%w: clarification context differs from the accepted private question", ErrReadViewInvalid)
	}
	return nil
}

func validateClarificationDecision(scope conversation.ConversationScope, decision conversation.ClarificationDecision) (conversation.ClarificationDecision, error) {
	raw, err := json.Marshal(decision)
	if err != nil {
		return conversation.ClarificationDecision{}, fmt.Errorf("encode clarification decision for validation: %w", err)
	}
	return conversation.ParseClarificationDecision(raw, scope)
}

func validateClarificationReceiptEvent(event modulecore.EventEnvelope, receipt agent.InvocationReceipt, accepted AcceptedTurn) error {
	return validateClarificationReceiptEventStatus(event, receipt, accepted, "completed")
}

func validateClarificationReceiptEventStatus(event modulecore.EventEnvelope, receipt agent.InvocationReceipt, accepted AcceptedTurn, expectedStatus string) error {
	if receipt.Status != expectedStatus || receipt.TokensUsed < 0 ||
		receipt.RequestID != strings.TrimSpace(receipt.RequestID) || receipt.ResponseID != strings.TrimSpace(receipt.ResponseID) ||
		receipt.ProviderLabel != strings.TrimSpace(receipt.ProviderLabel) || len(receipt.RequestID) > 512 ||
		len(receipt.ResponseID) > 512 || len(receipt.ProviderLabel) > 512 {
		return fmt.Errorf("Mio invocation receipt fields are invalid")
	}
	if event.EventType != "conversation.mio_emission_receipt" || event.ComponentID != "conversation_runtime" ||
		event.SessionID != accepted.SessionID || event.TurnID != accepted.TurnID ||
		event.TraceID != accepted.InputEvent.TraceID || event.CausationEventID != accepted.InputEvent.EventID ||
		event.RequestID != "" || event.MessageID != "" || event.ActorKind != "" || event.ActorID != "" {
		return fmt.Errorf("Mio receipt event identity is invalid")
	}
	expectedKeys := 1
	if payloadString(event.Payload, "status") != receipt.Status {
		return fmt.Errorf("Mio receipt status differs from its event")
	}
	if receipt.RequestID != "" {
		expectedKeys++
		if payloadString(event.Payload, "request_id") != receipt.RequestID {
			return fmt.Errorf("Mio receipt request ID differs from its event")
		}
	} else if _, exists := event.Payload["request_id"]; exists {
		return fmt.Errorf("Mio receipt event contains an unbound request ID")
	}
	if receipt.ResponseID != "" {
		expectedKeys++
		if payloadString(event.Payload, "response_id") != receipt.ResponseID {
			return fmt.Errorf("Mio receipt response ID differs from its event")
		}
	} else if _, exists := event.Payload["response_id"]; exists {
		return fmt.Errorf("Mio receipt event contains an unbound response ID")
	}
	if len(event.Payload) != expectedKeys {
		return fmt.Errorf("Mio receipt event contains non-minimal payload fields")
	}
	return nil
}

func validateClarificationDecisionResult(result ClarificationDecisionCommitResult, input AcceptedTurnInput, control *conversation.ClarificationControlContext, decision *conversation.ClarificationDecision) error {
	if result.TerminalReason != "" {
		return validateClarificationTerminalResult(result, input, nil)
	}
	accepted := input.ReadViewRequest.Accepted
	invalid := func(message string) error {
		return fmt.Errorf("clarification decision receipt is invalid: %s", message)
	}
	delegationID := accepted.InputEvent.RequestID
	revision, revisionOK := payloadUint64(accepted.InputEvent.Payload["delegation_revision"])
	if !revisionOK || revision == 0 || result.OperationID != clarificationDecisionOperationPrefix+string(accepted.TurnID) ||
		result.ControlTurnID != accepted.TurnID || result.DelegationID != delegationID || result.Revision != revision ||
		(result.Kind != conversation.ClarificationDecisionAskUser && result.Kind != conversation.ClarificationDecisionContinueDelegation) {
		return invalid("operation identity, delegation, revision, or kind does not match the accepted control Turn")
	}
	if decision != nil && result.Kind != decision.Kind {
		return invalid("committed decision kind differs from Mio's validated decision")
	}
	if result.Event.EventID == "" || result.Message.EventID != result.Event.EventID || result.Message.MessageID != result.Event.MessageID ||
		result.Message.MessageID == accepted.InputMessageID || len(result.MessageIndexes) != 1 || !reflect.DeepEqual(result.MessageIndexes[0], result.Message) {
		return invalid("primary event and message index do not match")
	}
	if len(result.Events) < 2 {
		return invalid("committed event list is missing its decision or receipt event")
	}
	// The operation ID is stable per control Turn. A concurrent idempotent
	// commit may therefore return the winner's runtime-issued event IDs instead
	// of the IDs in this caller's commit attempt; validate the stored receipt's
	// content and bindings below rather than requiring this attempt's IDs.
	var previous modulecore.EventSeq
	seenEvents := make(map[modulecore.EventID]struct{}, len(result.Events))
	for index, event := range result.Events {
		if err := modulecore.ValidateEventEnvelope(event); err != nil {
			return invalid("committed event envelope failed validation")
		}
		if event.EventID == accepted.InputEvent.EventID || event.EventSeq <= accepted.AdmissionSeq || (index > 0 && event.EventSeq <= previous) ||
			event.SessionID != accepted.SessionID || event.TraceID != accepted.InputEvent.TraceID {
			return invalid("committed event sequence, session, or trace is invalid")
		}
		if _, duplicate := seenEvents[event.EventID]; duplicate {
			return invalid("committed event list contains duplicate event IDs")
		}
		seenEvents[event.EventID] = struct{}{}
		previous = event.EventSeq
	}
	if !reflect.DeepEqual(result.ReceiptEvent, result.Events[len(result.Events)-1]) || result.ReceiptEvent.EventID == "" ||
		result.ReceiptEvent.MessageID != "" || result.InvocationReceipt.Status == "" ||
		validateClarificationReceiptEvent(result.ReceiptEvent, result.InvocationReceipt, accepted) != nil {
		return invalid("minimal Mio generation receipt is missing or inconsistent")
	}
	if control != nil && result.Kind == conversation.ClarificationDecisionContinueDelegation {
		expected := control.Handoff
		expected.Revision++
		if decision == nil || decision.Kind != conversation.ClarificationDecisionContinueDelegation {
			return invalid("continuation decision is missing")
		}
		expected.Instruction = decision.Instruction
		expected.InstructionMessageID = result.Message.MessageID
		if result.NextHandoff == nil || !reflect.DeepEqual(*result.NextHandoff, expected) {
			return invalid("continuation changed the canonical handoff contract")
		}
	}
	primary := result.Event
	if !reflect.DeepEqual(primary, result.Events[len(result.Events)-2]) {
		return invalid("primary output is not immediately before the receipt event")
	}
	switch result.Kind {
	case conversation.ClarificationDecisionAskUser:
		if result.ResultState != "completed" || result.DispatchQueued || result.NextHandoff != nil || len(result.Events) != 2 ||
			primary.EventType != "agent.response" || primary.ComponentID != "orchestrator" ||
			primary.ActorKind != "" || primary.ActorID != "" || primary.TurnID != accepted.TurnID ||
			primary.RequestID != "" || primary.CausationEventID != accepted.InputEvent.EventID ||
			payloadString(primary.Payload, "kind") != "clarification_prompt" || payloadString(primary.Payload, "status") != "awaiting_user_clarification" ||
			payloadString(primary.Payload, "availability") != "free" || payloadString(primary.Payload, "client_submission_id") != accepted.ClientSubmissionID ||
			payloadString(primary.Payload, "visibility") != "public" ||
			payloadString(primary.Payload, "from") != conversationActorMio || payloadString(primary.Payload, "source_actor_id") != conversationActorMio ||
			payloadString(primary.Payload, "to") != "user" || payloadString(primary.Payload, "text") == "" ||
			payloadString(primary.Payload, "content") != payloadString(primary.Payload, "text") ||
			payloadString(primary.Payload, "delegation_id") != string(delegationID) ||
			messageIndexMatches(result.Message, primary, accepted.SessionID, accepted.TurnID, "user", input.ReadViewRequest.Session.OwnerID, "clarification_prompt", "public") != nil ||
			result.Message.ReplyToMessageID != modulecore.MessageID(payloadString(accepted.InputEvent.Payload, "reply_to_message_id")) {
			return invalid("ask_user receipt does not contain one public clarification prompt")
		}
		if decision != nil && payloadString(primary.Payload, "text") != decision.Question {
			return invalid("public clarification prompt differs from Mio's decision")
		}
		if payloadUint64Value(primary.Payload, "delegation_revision") != revision {
			return invalid("public clarification prompt revision is invalid")
		}
	case conversation.ClarificationDecisionContinueDelegation:
		if result.ResultState != "awaiting_readback" || !result.DispatchQueued || result.NextHandoff == nil || len(result.Events) != 3 ||
			result.NextHandoff.DelegationID != delegationID || result.NextHandoff.Revision != revision+1 ||
			result.NextHandoff.SessionID != accepted.SessionID || result.NextHandoff.SourceTraceID != accepted.InputEvent.TraceID ||
			result.NextHandoff.ToActor != accepted.ActorID || result.NextHandoff.OriginTurnID == accepted.TurnID ||
			result.Events[0].EventType != "conversation.delegation_context" || result.Events[0].ComponentID != "conversation_runtime" ||
			result.Events[0].TurnID != result.NextHandoff.OriginTurnID || result.Events[0].RequestID != delegationID ||
			primary.EventType != "conversation.delegation_instruction" || primary.ComponentID != "delegation_mailbox" ||
			primary.ActorKind != "agent" || primary.ActorID != conversationActorMio || primary.TurnID != result.NextHandoff.OriginTurnID ||
			primary.RequestID != delegationID || primary.CausationEventID != accepted.InputEvent.EventID ||
			payloadString(primary.Payload, "kind") != "delegation_instruction" || payloadString(primary.Payload, "visibility") != "private" ||
			payloadString(primary.Payload, "from") != conversationActorMio || payloadString(primary.Payload, "source_actor_id") != conversationActorMio ||
			payloadString(primary.Payload, "to") != accepted.ActorID || payloadString(primary.Payload, "text") == "" ||
			payloadString(primary.Payload, "content") != payloadString(primary.Payload, "text") ||
			payloadString(primary.Payload, "delegation_id") != string(delegationID) ||
			messageIndexMatches(result.Message, primary, accepted.SessionID, result.NextHandoff.OriginTurnID, "agent", accepted.ActorID, "delegation_instruction", "private") != nil ||
			result.Message.ReplyToMessageID != result.NextHandoff.ReplyTo ||
			payloadUint64Value(primary.Payload, "delegation_revision") != revision+1 {
			return invalid("continue_delegation receipt does not contain a private same-assignee continuation")
		}
		if decision != nil && (result.NextHandoff.Instruction != decision.Instruction || payloadString(primary.Payload, "text") != decision.Instruction) {
			return invalid("private continuation instruction differs from Mio's decision")
		}
		if decision == nil && result.NextHandoff.Instruction != payloadString(primary.Payload, "text") {
			return invalid("private continuation instruction differs from its handoff")
		}
		var handoffPayload conversation.HandoffEnvelope
		handoffBytes, err := json.Marshal(result.Events[0].Payload["handoff"])
		if err != nil || json.Unmarshal(handoffBytes, &handoffPayload) != nil || !reflect.DeepEqual(handoffPayload, *result.NextHandoff) {
			return invalid("continuation context event does not contain the next canonical handoff")
		}
		if err := conversation.ValidateHandoffEnvelope(*result.NextHandoff); err != nil {
			return invalid("continued handoff envelope failed validation")
		}
	default:
		return invalid("decision kind is unsupported")
	}
	return nil
}

func validateClarificationTerminalResult(result ClarificationDecisionCommitResult, input AcceptedTurnInput, expectedReason *conversation.ClarificationTerminalReason) error {
	accepted := input.ReadViewRequest.Accepted
	invalid := func(message string) error {
		return fmt.Errorf("clarification terminal receipt is invalid: %s", message)
	}
	revision, revisionOK := payloadUint64(accepted.InputEvent.Payload["delegation_revision"])
	status, validReason := clarificationTerminalStatus(result.TerminalReason)
	if !revisionOK || !validReason || result.OperationID != clarificationDecisionOperationPrefix+string(accepted.TurnID) ||
		result.ControlTurnID != accepted.TurnID || result.DelegationID != accepted.InputEvent.RequestID || result.Revision != revision ||
		result.Kind != "" || result.ResultState != "completed" || result.DispatchQueued || result.NextHandoff != nil ||
		len(result.Events) != 2 || result.TerminalReason == conversation.ClarificationTerminalRevisionLimit && revision < conversation.MaxClarificationRevision {
		return invalid("operation identity, terminal reason, revision, or result state is inconsistent")
	}
	if expectedReason != nil && result.TerminalReason != *expectedReason {
		return invalid("terminal reason differs from the requested result")
	}
	if result.Event.EventID == "" || result.Message.EventID != result.Event.EventID || result.Message.MessageID != result.Event.MessageID ||
		result.Message.MessageID == accepted.InputMessageID || len(result.MessageIndexes) != 1 || !reflect.DeepEqual(result.MessageIndexes[0], result.Message) {
		return invalid("public terminal event and message index do not match")
	}
	primary, receiptEvent := result.Events[0], result.Events[1]
	if !reflect.DeepEqual(result.Event, primary) || !reflect.DeepEqual(result.ReceiptEvent, receiptEvent) {
		return invalid("terminal result does not contain the exact public event and receipt in order")
	}
	var previous modulecore.EventSeq
	seenEvents := make(map[modulecore.EventID]struct{}, len(result.Events))
	for index, event := range result.Events {
		if err := modulecore.ValidateEventEnvelope(event); err != nil {
			return invalid("terminal event envelope failed validation")
		}
		if event.EventID == accepted.InputEvent.EventID || event.EventSeq <= accepted.AdmissionSeq || (index > 0 && event.EventSeq <= previous) ||
			event.SessionID != accepted.SessionID || event.TraceID != accepted.InputEvent.TraceID {
			return invalid("terminal event sequence, session, or trace is invalid")
		}
		if _, duplicate := seenEvents[event.EventID]; duplicate {
			return invalid("terminal event list contains duplicate event IDs")
		}
		seenEvents[event.EventID] = struct{}{}
		previous = event.EventSeq
	}
	if primary.EventType != "agent.response" || primary.ComponentID != "orchestrator" || primary.ActorKind != "" || primary.ActorID != "" ||
		primary.TurnID != accepted.TurnID || primary.RequestID != "" || primary.CausationEventID != accepted.InputEvent.EventID ||
		payloadString(primary.Payload, "kind") != "turn_error" ||
		payloadString(primary.Payload, "availability") != "free" || payloadString(primary.Payload, "visibility") != "public" ||
		payloadString(primary.Payload, "text") != conversation.ClarificationTerminalUserText ||
		payloadString(primary.Payload, "content") != conversation.ClarificationTerminalUserText ||
		payloadString(primary.Payload, "from") != conversationActorMio || payloadString(primary.Payload, "source_actor_id") != conversationActorMio ||
		payloadString(primary.Payload, "to") != "user" || len(primary.Payload) != 12 ||
		payloadString(primary.Payload, "delegation_id") != string(accepted.InputEvent.RequestID) ||
		payloadUint64Value(primary.Payload, "delegation_revision") != revision ||
		payloadString(primary.Payload, "client_submission_id") != accepted.ClientSubmissionID ||
		payloadString(primary.Payload, "reply_to_message_id") != payloadString(accepted.InputEvent.Payload, "reply_to_message_id") ||
		messageIndexMatches(result.Message, primary, accepted.SessionID, accepted.TurnID, "user", input.ReadViewRequest.Session.OwnerID, "turn_error", "public") != nil ||
		result.Message.ReplyToMessageID != modulecore.MessageID(payloadString(accepted.InputEvent.Payload, "reply_to_message_id")) {
		return invalid("terminal result does not contain the fixed public turn_error")
	}
	if receiptEvent.MessageID != "" || receiptEvent.RequestID != "" || result.InvocationReceipt.Status != status ||
		validateClarificationReceiptEventStatus(receiptEvent, result.InvocationReceipt, accepted, status) != nil {
		return invalid("terminal Mio generation receipt is missing or inconsistent")
	}
	return nil
}

func messageIndexMatches(index MessageIndex, event modulecore.EventEnvelope, sessionID modulecore.SessionID, turnID modulecore.TurnID, toKind, toActor, kind, visibility string) error {
	if index.MessageID != event.MessageID || index.EventID != event.EventID || index.SessionID != sessionID || index.TurnID != turnID ||
		index.FromActor != conversationActorMio || index.ToKind != toKind || index.ToActor != toActor || index.Kind != kind || index.Visibility != visibility {
		return fmt.Errorf("clarification output message index does not match its event")
	}
	return nil
}

func containsExactString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func payloadUint64Value(payload map[string]any, key string) uint64 {
	value, ok := payloadUint64(payload[key])
	if !ok {
		return 0
	}
	return value
}

func clarificationFailed(ctx context.Context, cause error) TurnResult {
	if ctx != nil && ctx.Err() != nil {
		cause = errors.Join(cause, ctx.Err())
	}
	markV2TimingError(ctx, turntiming.PointV2Generation, cause)
	return TurnResult{Status: TurnStatusFailedBeforeCommit, Cause: cause}
}

func (r *Runtime) publishClarification(ctx context.Context, result ClarificationDecisionCommitResult, cause error) TurnResult {
	batch := CommittedBundle{
		OperationID: result.OperationID, SessionID: result.Event.SessionID,
		TurnID: result.ControlTurnID, ResultState: result.ResultState,
		Events: append([]modulecore.EventEnvelope(nil), result.Events...),
	}
	turnResult := TurnResult{
		Status: TurnStatusDelivered, ResultState: result.ResultState, Committed: true,
		Batch: cloneCommittedBundle(batch), Cause: cause,
	}
	publishTurn, publishSpan, publishTimed := beginV2TimingSpan(ctx, turntiming.PointV2Publish)
	publishErr := r.publisher.PublishCommitted(ctx, cloneCommittedBundle(batch))
	endV2TimingSpan(publishTurn, publishSpan, publishTimed, publishErr)
	if publishErr != nil {
		markV2TimingError(ctx, turntiming.PointV2Publish, publishErr)
		turnResult.Status = TurnStatusDeliveryPending
		turnResult.Cause = errors.Join(turnResult.Cause, publishErr)
	}
	return turnResult
}
