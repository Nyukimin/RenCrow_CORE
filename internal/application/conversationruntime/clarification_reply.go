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

const clarificationReplyOperationPrefix = "clarification.reply:"

type ClarificationReplyContextRequest struct {
	SessionID   modulecore.SessionID
	ReplyTurnID modulecore.TurnID
	OwnerID     string
}

type ClarificationReplyReceipt struct {
	PromptMessageID modulecore.MessageID
	SourceTurnID    modulecore.TurnID
	DelegationID    modulecore.RequestID
	Revision        uint64
}

// StoredClarificationReplyContext is the owner-validated canonical join for
// one accepted public answer, its prompt, the private assignee question, and
// the original handoff context.
type StoredClarificationReplyContext struct {
	Reply           AcceptedTurn
	OwnerID         string
	Receipt         ClarificationReplyReceipt
	AnswerEvent     modulecore.EventEnvelope
	AnswerMessage   MessageIndex
	PromptEvent     modulecore.EventEnvelope
	PromptMessage   MessageIndex
	ControlTurn     AcceptedTurn
	QuestionEvent   modulecore.EventEnvelope
	QuestionMessage MessageIndex
	Handoff         conversation.HandoffEnvelope
	OriginTurnID    modulecore.TurnID
	Context         []DelegationDispatchMessage
	AnswerText      string
}

type ClarificationReplyContinuationInput struct {
	OperationID       string
	OwnerID           string
	SessionID         modulecore.SessionID
	ReplyTurnID       modulecore.TurnID
	PromptMessageID   modulecore.MessageID
	DelegationID      modulecore.RequestID
	Revision          uint64
	GenerationEpoch   int64
	Instruction       string
	InvocationReceipt agent.InvocationReceipt
	EventID           modulecore.EventID
	MessageID         modulecore.MessageID
	ContextEventID    modulecore.EventID
	ReceiptEventID    modulecore.EventID
	OccurredAt        time.Time
}

type ClarificationReplyContinuationResult struct {
	OperationID       string
	OwnerID           string
	SessionID         modulecore.SessionID
	ReplyTurnID       modulecore.TurnID
	PromptMessageID   modulecore.MessageID
	DelegationID      modulecore.RequestID
	Revision          uint64
	NextRevision      uint64
	ResultState       string
	InvocationReceipt agent.InvocationReceipt
	ContextEvent      modulecore.EventEnvelope
	Event             modulecore.EventEnvelope
	ReceiptEvent      modulecore.EventEnvelope
	Message           MessageIndex
	Events            []modulecore.EventEnvelope
	MessageIndexes    []MessageIndex
	NextHandoff       *conversation.HandoffEnvelope
	DispatchQueued    bool
}

// ClarificationReplyStore is the application port for canonical reply context
// and the fenced continuation operation.
type ClarificationReplyStore interface {
	LoadClarificationReplyContext(context.Context, ClarificationReplyContextRequest) (StoredClarificationReplyContext, error)
	LoadClarificationReplyContinuationOutcome(context.Context, modulecore.TurnID) (ClarificationReplyContinuationResult, bool, error)
	CommitClarificationReplyContinuation(context.Context, ClarificationReplyContinuationInput) (ClarificationReplyContinuationResult, error)
}

type ClarificationReplyInstructionGenerator interface {
	GenerateClarificationReplyInstruction(context.Context, conversation.ConversationScope, agent.PromptSnapshot, conversation.ClarificationReplyGenerationContext) (string, agent.InvocationReceipt, error)
}

func (r *Runtime) runClarificationReplyTurn(ctx context.Context, input AcceptedTurnInput) TurnResult {
	accepted := input.ReadViewRequest.Accepted
	store := r.clarificationReplies
	if store == nil {
		return clarificationReplyFailed(ctx, errors.New("clarification reply store is unavailable"))
	}
	loadTurn, loadSpan, loadTimed := beginV2TimingSpan(ctx, turntiming.PointV2Commit)
	prior, found, err := store.LoadClarificationReplyContinuationOutcome(ctx, accepted.TurnID)
	endV2TimingSpan(loadTurn, loadSpan, loadTimed, err)
	if err != nil {
		markV2TimingError(ctx, turntiming.PointV2Commit, err)
		return TurnResult{Status: TurnStatusCommitUncertain, Cause: err}
	}
	if found {
		if err := validateClarificationReplyContinuationResult(prior, input, nil, nil); err != nil {
			markV2TimingError(ctx, turntiming.PointV2Commit, err)
			return TurnResult{Status: TurnStatusCommitUncertain, Cause: err}
		}
		return r.publishClarificationReply(ctx, prior)
	}
	if accepted.State != "accepted" {
		err := errors.New("clarification reply Turn is awaiting readback without a stored continuation outcome")
		markV2TimingError(ctx, turntiming.PointV2Commit, err)
		return TurnResult{Status: TurnStatusCommitUncertain, Cause: err}
	}
	if err := ctx.Err(); err != nil {
		return clarificationReplyFailed(ctx, err)
	}
	stored, err := store.LoadClarificationReplyContext(ctx, ClarificationReplyContextRequest{
		SessionID: accepted.SessionID, ReplyTurnID: accepted.TurnID, OwnerID: input.ReadViewRequest.Session.OwnerID,
	})
	if err != nil {
		markV2TimingError(ctx, turntiming.PointV2ReadView, err)
		return clarificationReplyFailed(ctx, fmt.Errorf("load canonical clarification reply context: %w", err))
	}
	if err := validateStoredClarificationReplyContext(stored, input.ReadViewRequest); err != nil {
		markV2TimingError(ctx, turntiming.PointV2ReadView, err)
		return clarificationReplyFailed(ctx, err)
	}

	scope := conversation.ConversationScope{
		TurnID: accepted.TurnID, SessionID: accepted.SessionID, TraceID: accepted.InputEvent.TraceID,
		ActorID: accepted.ActorID, InputID: accepted.InputMessageID,
		GenerationID: fmt.Sprintf("%s:%d", accepted.TurnID, accepted.GenerationEpoch),
		Purpose:      conversation.PurposeClarificationReply, PolicyVersion: r.config.PolicyVersion,
	}
	generationContext := conversation.ClarificationReplyGenerationContext{
		Handoff: stored.Handoff, QuestionMessageID: stored.QuestionMessage.MessageID,
		QuestionSender: stored.QuestionMessage.FromActor, QuestionText: payloadString(stored.QuestionEvent.Payload, "text"),
	}
	if err := generationContext.Validate(scope); err != nil {
		return clarificationReplyFailed(ctx, fmt.Errorf("validate stored clarification reply generation context: %w", err))
	}
	canonical, err := clarificationReplyPromptSnapshot(stored)
	if err != nil {
		return clarificationReplyFailed(ctx, err)
	}
	composed, err := r.composer.ComposeConversationPrompt(ctx, scope, canonical.Clone())
	if err != nil {
		if ctx.Err() != nil {
			return clarificationReplyFailed(ctx, ctx.Err())
		}
		markV2TimingError(ctx, turntiming.PointV2PromptComposition, err)
		return clarificationReplyFailed(ctx, fmt.Errorf("compose clarification reply prompt: %w", err))
	}
	if err := validateComposedClarificationReplyPrompt(composed, canonical, accepted); err != nil {
		markV2TimingError(ctx, turntiming.PointV2PromptComposition, err)
		return clarificationReplyFailed(ctx, err)
	}
	generator, ok := r.generator.(ClarificationReplyInstructionGenerator)
	if !ok {
		err := errors.New("clarification reply instruction generator is unavailable")
		return clarificationReplyFailed(ctx, err)
	}
	instruction, receipt, generationErr := generator.GenerateClarificationReplyInstruction(ctx, scope, composed, generationContext)
	if err := ctx.Err(); err != nil {
		markV2TimingError(ctx, turntiming.PointV2Generation, err)
		return clarificationReplyFailed(ctx, err)
	}
	if generationErr != nil {
		markV2TimingError(ctx, turntiming.PointV2Generation, generationErr)
		return clarificationReplyFailed(ctx, fmt.Errorf("generate clarification reply instruction: %w", generationErr))
	}
	if err := validateClarificationReplyGenerationOutput(instruction, receipt); err != nil {
		markV2TimingError(ctx, turntiming.PointV2Generation, err)
		return clarificationReplyFailed(ctx, err)
	}

	commitInput := ClarificationReplyContinuationInput{
		OperationID: clarificationReplyOperationPrefix + string(accepted.TurnID), OwnerID: stored.OwnerID,
		SessionID: accepted.SessionID, ReplyTurnID: accepted.TurnID, PromptMessageID: stored.Receipt.PromptMessageID,
		DelegationID: stored.Handoff.DelegationID, Revision: stored.Handoff.Revision,
		GenerationEpoch: accepted.GenerationEpoch, Instruction: instruction, InvocationReceipt: receipt,
		EventID: modulecore.NewEventID(), MessageID: modulecore.NewMessageID(),
		ContextEventID: modulecore.NewEventID(), ReceiptEventID: modulecore.NewEventID(), OccurredAt: time.Now().UTC(),
	}
	commitTurn, commitSpan, commitTimed := beginV2TimingSpan(ctx, turntiming.PointV2Commit)
	committed, commitErr := store.CommitClarificationReplyContinuation(ctx, commitInput)
	endV2TimingSpan(commitTurn, commitSpan, commitTimed, commitErr)
	if commitErr != nil {
		lookupCtx := context.WithoutCancel(ctx)
		saved, savedFound, lookupErr := store.LoadClarificationReplyContinuationOutcome(lookupCtx, accepted.TurnID)
		if lookupErr != nil {
			markV2TimingError(ctx, turntiming.PointV2Commit, lookupErr)
			return TurnResult{Status: TurnStatusCommitUncertain, Cause: errors.Join(commitErr, lookupErr)}
		}
		if !savedFound {
			markV2TimingError(ctx, turntiming.PointV2Commit, commitErr)
			return TurnResult{Status: TurnStatusCommitUncertain, Cause: commitErr}
		}
		if err := validateClarificationReplyContinuationResult(saved, input, &stored, &commitInput); err != nil {
			markV2TimingError(ctx, turntiming.PointV2Commit, err)
			return TurnResult{Status: TurnStatusCommitUncertain, Cause: errors.Join(commitErr, err)}
		}
		return r.publishClarificationReply(ctx, saved)
	}
	if err := validateClarificationReplyContinuationResult(committed, input, &stored, &commitInput); err != nil {
		markV2TimingError(ctx, turntiming.PointV2Commit, err)
		return TurnResult{Status: TurnStatusCommitUncertain, Cause: err}
	}
	return r.publishClarificationReply(ctx, committed)
}

func clarificationReplyFailed(ctx context.Context, cause error) TurnResult {
	if err := ctx.Err(); err != nil {
		cause = err
	}
	return TurnResult{Status: TurnStatusFailedBeforeCommit, Cause: cause}
}

func (r *Runtime) publishClarificationReply(ctx context.Context, result ClarificationReplyContinuationResult) TurnResult {
	batch := CommittedBundle{
		OperationID: result.OperationID, SessionID: result.SessionID,
		TurnID: result.ReplyTurnID, ResultState: result.ResultState,
		Events: append([]modulecore.EventEnvelope(nil), result.Events...),
	}
	turnResult := TurnResult{
		Status: TurnStatusDelivered, ResultState: result.ResultState, Committed: true, Batch: cloneCommittedBundle(batch),
	}
	publishTurn, publishSpan, publishTimed := beginV2TimingSpan(ctx, turntiming.PointV2Publish)
	publishErr := r.publisher.PublishCommitted(ctx, cloneCommittedBundle(batch))
	endV2TimingSpan(publishTurn, publishSpan, publishTimed, publishErr)
	if publishErr != nil {
		markV2TimingError(ctx, turntiming.PointV2Publish, publishErr)
		turnResult.Status = TurnStatusDeliveryPending
		turnResult.Cause = publishErr
	}
	return turnResult
}

func validateStoredClarificationReplyContext(stored StoredClarificationReplyContext, request ReadViewRequest) error {
	invalid := func(message string) error { return fmt.Errorf("%w: %s", ErrReadViewInvalid, message) }
	accepted := request.Accepted
	if stored.OwnerID != request.Session.OwnerID || !reflect.DeepEqual(stored.Reply, accepted) ||
		!reflect.DeepEqual(stored.AnswerEvent, accepted.InputEvent) {
		return invalid("stored clarification reply Turn or owner does not match the accepted input")
	}
	receipt, handoff := stored.Receipt, stored.Handoff
	if err := conversation.ValidateHandoffEnvelope(handoff); err != nil {
		return invalid("stored clarification reply handoff is invalid")
	}
	answerText := payloadString(stored.AnswerEvent.Payload, "content")
	if accepted.TriggerKind != "clarification_reply" || accepted.State != "accepted" ||
		stored.AnswerEvent.EventType != "conversation.user_message" || stored.AnswerEvent.ComponentID != "orchestrator" ||
		stored.AnswerEvent.ActorKind != "" || stored.AnswerEvent.ActorID != "" ||
		stored.AnswerEvent.SessionID != accepted.SessionID || stored.AnswerEvent.TurnID != accepted.TurnID ||
		stored.AnswerEvent.MessageID != accepted.InputMessageID || stored.AnswerEvent.TraceID != accepted.InputEvent.TraceID ||
		stored.AnswerMessage.MessageID != accepted.InputMessageID || stored.AnswerMessage.EventID != stored.AnswerEvent.EventID ||
		stored.AnswerMessage.SessionID != accepted.SessionID || stored.AnswerMessage.TurnID != accepted.TurnID ||
		stored.AnswerMessage.FromActor != accepted.ActorID || stored.AnswerMessage.ToKind != "agent" ||
		stored.AnswerMessage.ToActor != "mio" || stored.AnswerMessage.Kind != "user_message" ||
		stored.AnswerMessage.Visibility != "public" || stored.AnswerMessage.ReplyToMessageID != receipt.PromptMessageID ||
		payloadString(stored.AnswerEvent.Payload, "from") != "user" ||
		payloadString(stored.AnswerEvent.Payload, "source_actor_id") != accepted.ActorID ||
		payloadString(stored.AnswerEvent.Payload, "text") != answerText ||
		payloadString(stored.AnswerEvent.Payload, "reply_to_message_id") != string(receipt.PromptMessageID) ||
		!utf8.ValidString(answerText) || strings.TrimSpace(answerText) == "" || len([]byte(answerText)) > conversation.MaxUserMessageBytes || stored.AnswerText != answerText {
		return invalid("stored public answer is inconsistent with the accepted clarification reply")
	}
	if receipt.PromptMessageID.Validate() != nil || receipt.SourceTurnID.Validate() != nil || receipt.DelegationID.Validate() != nil ||
		receipt.Revision == 0 || receipt.SourceTurnID != stored.OriginTurnID || handoff.OriginTurnID != stored.OriginTurnID ||
		handoff.SessionID != accepted.SessionID || handoff.DelegationID != receipt.DelegationID || handoff.Revision != receipt.Revision ||
		stored.PromptMessage.MessageID != receipt.PromptMessageID || stored.PromptEvent.EventID != stored.PromptMessage.EventID ||
		stored.PromptEvent.EventType != "agent.response" || stored.PromptEvent.ComponentID != "orchestrator" ||
		stored.PromptEvent.SessionID != accepted.SessionID || stored.PromptEvent.TurnID != stored.PromptMessage.TurnID ||
		stored.PromptEvent.TraceID != handoff.SourceTraceID || stored.PromptEvent.CausationEventID != stored.QuestionEvent.EventID ||
		stored.PromptMessage.FromActor != "mio" || stored.PromptMessage.ToKind != "user" ||
		stored.PromptMessage.ToActor != stored.OwnerID || stored.PromptMessage.Kind != "clarification_prompt" ||
		stored.PromptMessage.Visibility != "public" || stored.PromptMessage.ReplyToMessageID != handoff.ReplyTo ||
		payloadString(stored.PromptEvent.Payload, "kind") != "clarification_prompt" ||
		payloadString(stored.PromptEvent.Payload, "status") != "awaiting_user_clarification" ||
		payloadString(stored.PromptEvent.Payload, "visibility") != "public" ||
		payloadString(stored.PromptEvent.Payload, "from") != "mio" || payloadString(stored.PromptEvent.Payload, "to") != "user" ||
		payloadString(stored.PromptEvent.Payload, "delegation_id") != string(receipt.DelegationID) ||
		!payloadUint64Matches(stored.PromptEvent.Payload, "delegation_revision", receipt.Revision) ||
		payloadString(stored.PromptEvent.Payload, "reply_to_message_id") != string(handoff.ReplyTo) {
		return invalid("stored clarification prompt does not match the current public answer target")
	}
	questionText := payloadString(stored.QuestionEvent.Payload, "text")
	questionRevision, revisionOK := payloadUint64(stored.QuestionEvent.Payload["delegation_revision"])
	if stored.ControlTurn.SessionID != accepted.SessionID || stored.ControlTurn.TurnID != stored.PromptMessage.TurnID ||
		stored.ControlTurn.ActorID != handoff.ToActor || stored.ControlTurn.TriggerKind != "clarification_control" ||
		stored.ControlTurn.InputMessageID != stored.QuestionMessage.MessageID ||
		!reflect.DeepEqual(stored.ControlTurn.InputEvent, stored.QuestionEvent) ||
		stored.QuestionEvent.EventType != "conversation.clarification_question" || stored.QuestionEvent.ComponentID != "conversation_runtime" ||
		stored.QuestionEvent.TraceID != handoff.SourceTraceID || stored.QuestionEvent.SessionID != accepted.SessionID ||
		stored.QuestionEvent.TurnID != stored.ControlTurn.TurnID || stored.QuestionEvent.RequestID != handoff.DelegationID ||
		stored.QuestionMessage.EventID != stored.QuestionEvent.EventID || stored.QuestionMessage.SessionID != accepted.SessionID ||
		stored.QuestionMessage.TurnID != stored.ControlTurn.TurnID || stored.QuestionMessage.FromActor != handoff.ToActor ||
		stored.QuestionMessage.ToKind != "agent" || stored.QuestionMessage.ToActor != "mio" ||
		stored.QuestionMessage.Kind != "clarification_question" || stored.QuestionMessage.Visibility != "private" ||
		stored.QuestionMessage.ReplyToMessageID != handoff.ReplyTo || !revisionOK || questionRevision != handoff.Revision ||
		payloadString(stored.QuestionEvent.Payload, "kind") != "clarification_question" ||
		payloadString(stored.QuestionEvent.Payload, "status") != string(conversation.ReadbackStatusNeedsClarification) ||
		payloadString(stored.QuestionEvent.Payload, "visibility") != "private" ||
		payloadString(stored.QuestionEvent.Payload, "from") != handoff.ToActor ||
		payloadString(stored.QuestionEvent.Payload, "source_actor_id") != handoff.ToActor ||
		payloadString(stored.QuestionEvent.Payload, "to") != "mio" || questionText == "" ||
		payloadString(stored.QuestionEvent.Payload, "content") != questionText ||
		payloadString(stored.QuestionEvent.Payload, "reply_to_message_id") != string(handoff.ReplyTo) {
		return invalid("stored private assignee question does not match the canonical handoff")
	}
	if len(stored.Context) == 0 || len(stored.Context) > conversation.MaxSourceMessageIDs+1 {
		return invalid("stored clarification reply context is empty or oversized")
	}
	seen := make(map[modulecore.MessageID]struct{}, len(stored.Context))
	var contextBytes int
	originFound := false
	for _, source := range stored.Context {
		if source.MessageID.Validate() != nil || (source.Role != "user" && source.Role != "assistant") ||
			!utf8.ValidString(source.Text) || strings.TrimSpace(source.Text) == "" {
			return invalid("stored clarification reply source message is invalid")
		}
		if _, duplicate := seen[source.MessageID]; duplicate || source.MessageID == stored.AnswerMessage.MessageID {
			return invalid("stored clarification reply source context contains duplicate or already-appended answer")
		}
		seen[source.MessageID] = struct{}{}
		contextBytes += len(source.MessageID) + len(source.Role) + len([]byte(source.Text))
		if contextBytes > maxDelegationContextBytes {
			return invalid("stored clarification reply context exceeds its byte bound")
		}
		if source.MessageID == handoff.OriginUserMessageID {
			originFound = source.Role == "user"
		}
	}
	if !originFound {
		return invalid("stored clarification reply context omits the original user request")
	}
	for _, constraint := range handoff.Constraints {
		for _, sourceID := range constraint.SourceMessageIDs {
			if _, found := seen[sourceID]; !found {
				return invalid("stored clarification reply context omits a constrained source message")
			}
		}
	}
	return nil
}

func clarificationReplyPromptSnapshot(stored StoredClarificationReplyContext) (agent.PromptSnapshot, error) {
	if len(stored.Context)+1 > conversation.MaxSourceMessageIDs+2 {
		return agent.PromptSnapshot{}, fmt.Errorf("%w: clarification reply prompt exceeds its source message bound", ErrReadViewInvalid)
	}
	snapshot := agent.PromptSnapshot{
		Messages:                make([]llm.Message, 0, len(stored.Context)+1),
		AllowedSourceMessageIDs: make([]modulecore.MessageID, 0, len(stored.Context)+1),
		SourceRevision:          "clarification-handoff:" + stored.Handoff.ContextSnapshotHash,
		CutoverRevision:         "clarification-reply",
	}
	for _, source := range stored.Context {
		snapshot.Messages = append(snapshot.Messages, llm.Message{Role: source.Role, Content: source.Text})
		snapshot.AllowedSourceMessageIDs = append(snapshot.AllowedSourceMessageIDs, source.MessageID)
	}
	snapshot.Messages = append(snapshot.Messages, llm.Message{Role: "user", Content: stored.AnswerText})
	snapshot.AllowedSourceMessageIDs = append(snapshot.AllowedSourceMessageIDs, stored.AnswerMessage.MessageID)
	return snapshot, nil
}

func validateComposedClarificationReplyPrompt(snapshot, canonical agent.PromptSnapshot, accepted AcceptedTurn) error {
	if len(snapshot.Messages) == 0 || snapshot.Messages[len(snapshot.Messages)-1].Role != "user" ||
		snapshot.Messages[len(snapshot.Messages)-1].Content != canonical.Messages[len(canonical.Messages)-1].Content ||
		len(snapshot.AllowedSourceMessageIDs) != len(canonical.AllowedSourceMessageIDs) ||
		snapshot.SourceRevision != canonical.SourceRevision || snapshot.CutoverRevision != canonical.CutoverRevision {
		return errors.New("Mio clarification reply prompt composition changed its canonical answer or source metadata")
	}
	for index := range canonical.AllowedSourceMessageIDs {
		if snapshot.AllowedSourceMessageIDs[index] != canonical.AllowedSourceMessageIDs[index] {
			return errors.New("Mio clarification reply prompt composition changed its source allowlist")
		}
	}
	nonSystem := make([]llm.Message, 0, len(canonical.Messages))
	for _, message := range snapshot.Messages {
		if message.Role != "system" {
			nonSystem = append(nonSystem, message)
		}
	}
	if !samePromptMessages(nonSystem, canonical.Messages) || snapshot.AllowedSourceMessageIDs[len(snapshot.AllowedSourceMessageIDs)-1] != accepted.InputMessageID {
		return errors.New("Mio clarification reply prompt composition changed canonical conversation history")
	}
	return nil
}

func validateClarificationReplyGenerationOutput(instruction string, receipt agent.InvocationReceipt) error {
	if receipt.Status != "completed" || receipt.TokensUsed < 0 || receipt.RequestID != strings.TrimSpace(receipt.RequestID) ||
		receipt.ResponseID != strings.TrimSpace(receipt.ResponseID) || receipt.ProviderLabel != strings.TrimSpace(receipt.ProviderLabel) ||
		len(receipt.RequestID) > 512 || len(receipt.ResponseID) > 512 || len(receipt.ProviderLabel) > 512 ||
		!utf8.ValidString(instruction) || strings.TrimSpace(instruction) == "" || len([]byte(instruction)) > conversation.MaxInstructionBytes {
		return errors.New("Mio clarification reply instruction or invocation receipt is invalid")
	}
	return nil
}

func validateClarificationReplyContinuationResult(result ClarificationReplyContinuationResult, input AcceptedTurnInput, stored *StoredClarificationReplyContext, commit *ClarificationReplyContinuationInput) error {
	accepted := input.ReadViewRequest.Accepted
	ownerID := input.ReadViewRequest.Session.OwnerID
	if result.OperationID != clarificationReplyOperationPrefix+string(accepted.TurnID) || result.OwnerID != ownerID ||
		result.SessionID != accepted.SessionID || result.ReplyTurnID != accepted.TurnID ||
		result.PromptMessageID.Validate() != nil || result.DelegationID.Validate() != nil || result.Revision == 0 ||
		result.NextRevision != result.Revision+1 || result.ResultState != "awaiting_readback" || !result.DispatchQueued ||
		result.NextHandoff == nil || len(result.Events) != 3 || len(result.MessageIndexes) != 1 ||
		result.MessageIndexes[0] != result.Message || !reflect.DeepEqual(result.Events[0], result.ContextEvent) ||
		!reflect.DeepEqual(result.Events[1], result.Event) || !reflect.DeepEqual(result.Events[2], result.ReceiptEvent) {
		return errors.New("clarification reply continuation result identity or event batch is invalid")
	}
	handoff := *result.NextHandoff
	if err := conversation.ValidateHandoffEnvelope(handoff); err != nil {
		return fmt.Errorf("validate committed clarification reply handoff: %w", err)
	}
	if handoff.SessionID != accepted.SessionID || handoff.DelegationID != result.DelegationID || handoff.Revision != result.NextRevision ||
		handoff.OriginTurnID == accepted.TurnID || handoff.ContextSnapshotHash == "" {
		return errors.New("clarification reply continuation handoff binding is invalid")
	}
	if commit != nil && (result.OperationID != commit.OperationID || result.OwnerID != commit.OwnerID || result.SessionID != commit.SessionID ||
		result.ReplyTurnID != commit.ReplyTurnID || result.PromptMessageID != commit.PromptMessageID ||
		result.DelegationID != commit.DelegationID || result.Revision != commit.Revision ||
		!samePersistedClarificationReplyReceipt(result.InvocationReceipt, commit.InvocationReceipt) || handoff.Instruction != commit.Instruction) {
		return errors.New("committed clarification reply result differs from its prepared operation")
	}
	if stored != nil {
		previous := stored.Handoff
		if result.PromptMessageID != stored.Receipt.PromptMessageID || result.DelegationID != previous.DelegationID || result.Revision != previous.Revision ||
			handoff.OriginTurnID != previous.OriginTurnID || handoff.OriginUserMessageID != previous.OriginUserMessageID ||
			handoff.SessionID != previous.SessionID || handoff.SourceTraceID != previous.SourceTraceID ||
			handoff.FromActor != previous.FromActor || handoff.ToActor != previous.ToActor || handoff.ReplyTo != previous.ReplyTo ||
			handoff.Goal != previous.Goal || !reflect.DeepEqual(handoff.Constraints, previous.Constraints) ||
			handoff.ExpectedOutput != previous.ExpectedOutput || !reflect.DeepEqual(handoff.AllowedCapabilities, previous.AllowedCapabilities) {
			return errors.New("clarification reply continuation changed the stored handoff contract")
		}
	}
	for index, event := range result.Events {
		if err := modulecore.ValidateEventEnvelope(event); err != nil || event.EventSeq <= 0 || event.SessionID != accepted.SessionID ||
			(index > 0 && event.EventSeq <= result.Events[index-1].EventSeq) {
			return errors.New("clarification reply continuation contains an invalid canonical event")
		}
	}
	contextHandoff, err := payloadHandoff(result.ContextEvent.Payload["handoff"])
	if err != nil || !reflect.DeepEqual(contextHandoff, handoff) || result.ContextEvent.EventType != "conversation.delegation_context" ||
		result.ContextEvent.ComponentID != "conversation_runtime" || result.ContextEvent.TurnID != handoff.OriginTurnID ||
		result.ContextEvent.TraceID != handoff.SourceTraceID || result.ContextEvent.RequestID != handoff.DelegationID ||
		payloadString(result.ContextEvent.Payload, "context_snapshot_hash") != handoff.ContextSnapshotHash {
		return errors.New("clarification reply context event does not match its canonical next handoff")
	}
	instruction := result.Event
	if instruction.EventType != "conversation.delegation_instruction" || instruction.ComponentID != "delegation_mailbox" ||
		instruction.SessionID != accepted.SessionID || instruction.TurnID != handoff.OriginTurnID || instruction.TraceID != handoff.SourceTraceID ||
		instruction.RequestID != handoff.DelegationID || instruction.CausationEventID != result.ContextEvent.EventID ||
		instruction.MessageID != result.Message.MessageID ||
		instruction.ActorKind != "agent" || instruction.ActorID != "mio" ||
		payloadString(instruction.Payload, "kind") != "delegation_instruction" ||
		payloadString(instruction.Payload, "delegation_id") != string(handoff.DelegationID) ||
		!payloadUint64Matches(instruction.Payload, "delegation_revision", handoff.Revision) ||
		payloadString(instruction.Payload, "visibility") != "private" || payloadString(instruction.Payload, "from") != "mio" ||
		payloadString(instruction.Payload, "source_actor_id") != "mio" || payloadString(instruction.Payload, "to") != handoff.ToActor ||
		payloadString(instruction.Payload, "content") != handoff.Instruction || payloadString(instruction.Payload, "text") != handoff.Instruction ||
		result.Message.EventID != instruction.EventID || result.Message.SessionID != accepted.SessionID ||
		result.Message.TurnID != handoff.OriginTurnID || result.Message.FromActor != "mio" || result.Message.ToKind != "agent" ||
		result.Message.ToActor != handoff.ToActor || result.Message.Kind != "delegation_instruction" ||
		result.Message.Visibility != "private" || result.Message.ReplyToMessageID != handoff.ReplyTo {
		return errors.New("clarification reply instruction event is invalid or not private to the stored assignee")
	}
	receipt := result.ReceiptEvent
	if receipt.EventType != "conversation.mio_emission_receipt" || receipt.ComponentID != "conversation_runtime" ||
		receipt.SessionID != accepted.SessionID || receipt.TurnID != accepted.TurnID || receipt.TraceID != accepted.InputEvent.TraceID ||
		receipt.CausationEventID != accepted.InputEvent.EventID || receipt.MessageID != "" || receipt.RequestID != "" ||
		receipt.ActorKind != "" || receipt.ActorID != "" ||
		payloadString(receipt.Payload, "status") != result.InvocationReceipt.Status ||
		payloadString(receipt.Payload, "request_id") != result.InvocationReceipt.RequestID ||
		payloadString(receipt.Payload, "response_id") != result.InvocationReceipt.ResponseID {
		return errors.New("clarification reply receipt event identity or invocation fields are invalid")
	}
	if stored != nil {
		want := append(append([]DelegationDispatchMessage(nil), stored.Context...), DelegationDispatchMessage{
			MessageID: stored.AnswerMessage.MessageID, Role: "user", Text: stored.AnswerText,
		})
		got, err := decodeDelegationContextMessages(result.ContextEvent.Payload["messages"])
		if err != nil || !reflect.DeepEqual(got, want) ||
			receipt.CausationEventID != stored.AnswerEvent.EventID {
			return errors.New("clarification reply commit did not append the accepted public answer to canonical handoff context")
		}
	}
	if err := validateClarificationReplyGenerationOutput(handoff.Instruction, result.InvocationReceipt); err != nil {
		return err
	}
	return nil
}

func samePersistedClarificationReplyReceipt(saved, generated agent.InvocationReceipt) bool {
	return saved.RequestID == generated.RequestID && saved.ResponseID == generated.ResponseID && saved.Status == generated.Status
}

func payloadHandoff(raw any) (conversation.HandoffEnvelope, error) {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return conversation.HandoffEnvelope{}, err
	}
	var handoff conversation.HandoffEnvelope
	if err := json.Unmarshal(encoded, &handoff); err != nil {
		return conversation.HandoffEnvelope{}, err
	}
	return handoff, nil
}

func decodeDelegationContextMessages(raw any) ([]DelegationDispatchMessage, error) {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var messages []DelegationDispatchMessage
	if err := json.Unmarshal(encoded, &messages); err != nil {
		return nil, err
	}
	return messages, nil
}

func payloadUint64Matches(payload map[string]any, key string, want uint64) bool {
	value, ok := payloadUint64(payload[key])
	return ok && value == want
}
