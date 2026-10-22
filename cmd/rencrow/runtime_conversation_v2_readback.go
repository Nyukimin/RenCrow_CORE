package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"reflect"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	responseFirstReadbackLeaseDuration = time.Minute
	responseFirstReadbackTimeout       = 30 * time.Second
	responseFirstReadbackDrainPeriod   = time.Second
	responseFirstReadbackDrainBatch    = 8
)

var responseFirstReadbackAssigneeOrder = []string{"shiro", "midori"}

type responseFirstReadbackWiring struct {
	Assignees map[string]agent.ReadbackAssigneeConfig
}

// responseFirstReadbackWiringFor deliberately selects only the readback routes
// with a reviewed, non-Tool conversation path. Kuro's current route can invoke
// Codex with full filesystem access, so it is never even added to the generator.
func responseFirstReadbackWiringFor(
	cfg *config.Config,
	providers map[string]agent.ConversationGenerationProvider,
) responseFirstReadbackWiring {
	result := responseFirstReadbackWiring{Assignees: make(map[string]agent.ReadbackAssigneeConfig)}
	if cfg == nil || cfg.Prompts == nil {
		return result
	}
	for _, assignee := range responseFirstReadbackAssigneeOrder {
		provider := providers[assignee]
		prompt := responseFirstReadbackSystemPrompt(cfg, assignee)
		if provider == nil || strings.TrimSpace(prompt) == "" {
			continue
		}
		result.Assignees[assignee] = agent.ReadbackAssigneeConfig{
			Provider: provider, SystemPrompt: prompt,
			StableRuntimeContext: strings.TrimSpace(cfg.Prompts.StableRuntimeContexts[assignee]),
		}
	}
	return result
}

func responseFirstReadbackSystemPrompt(cfg *config.Config, assignee string) string {
	if cfg == nil || cfg.Prompts == nil {
		return ""
	}
	var prompt string
	switch assignee {
	case "shiro":
		prompt = cfg.Prompts.Worker
		if cfg.Worker.PersonaFile != "" {
			if personality, ok := config.LoadPersonaFile(cfg.WorkspaceDir, cfg.Worker.PersonaFile); ok {
				prompt = (agent.AgentPersona{Name: "Shiro", Personality: personality, Tone: cfg.Worker.Tone}).BuildSystemPrompt(prompt)
			}
		}
	case "midori":
		prompt = cfg.Prompts.Wild
		if strings.TrimSpace(prompt) == "" {
			for name, candidate := range cfg.Prompts.CharacterPrompts {
				if strings.EqualFold(strings.TrimSpace(name), assignee) {
					prompt = candidate
					break
				}
			}
		}
	default:
		return ""
	}
	return strings.TrimSpace(prompt)
}

func filterResponseFirstReadbackAssignees(configured map[string]agent.ReadbackAssigneeConfig) map[string]agent.ReadbackAssigneeConfig {
	filtered := make(map[string]agent.ReadbackAssigneeConfig, len(responseFirstReadbackAssigneeOrder))
	for _, assignee := range responseFirstReadbackAssigneeOrder {
		config, ok := configured[assignee]
		if !ok || config.Provider == nil || strings.TrimSpace(config.SystemPrompt) == "" {
			continue
		}
		filtered[assignee] = config
	}
	return filtered
}

func responseFirstAssigneeIDs(configured map[string]agent.ReadbackAssigneeConfig) []string {
	ids := make([]string, 0, len(configured))
	for _, assignee := range responseFirstReadbackAssigneeOrder {
		if _, ok := configured[assignee]; ok {
			ids = append(ids, assignee)
		}
	}
	return ids
}

type responseFirstReadbackDispatcher interface {
	DispatchOne(context.Context) (conversationruntime.DelegationDispatchResult, error)
}

type responseFirstDelegationPersistence interface {
	ClaimWork(context.Context, eventstore.WorkClaimInput) ([]eventstore.LeasedWorkItem, error)
	LoadDelegationDispatch(context.Context, eventstore.DelegationDispatchInput) (eventstore.DelegationDispatch, error)
	AcceptReadback(context.Context, eventstore.ReadbackInput) (eventstore.ReadbackAcceptance, error)
	FailReadback(context.Context, eventstore.ReadbackFailureInput) (eventstore.ReadbackFailure, error)
	AckWork(context.Context, eventstore.WorkLeaseInput) error
	RetryWork(context.Context, eventstore.WorkRetryInput) error
}

type responseFirstEventReader interface {
	GetByID(context.Context, modulecore.EventID) (modulecore.EventEnvelope, bool, error)
}

// responseFirstDelegationDispatchStore adapts the EventStore-owned work and
// readback transactions to the conversation runtime port. Before releasing the
// private handoff to a generator, it republishes the already-committed Mio turn.
// This also resumes an interrupted foreground publication before low-priority
// readback generation after process restart.
type responseFirstDelegationDispatchStore struct {
	store     responseFirstDelegationPersistence
	outcomes  responseFirstJournalStore
	publisher conversationruntime.CommittedEventPublisher
}

func (s responseFirstDelegationDispatchStore) ClaimDispatchWork(ctx context.Context, request conversationruntime.DispatchClaimRequest) ([]conversationruntime.DelegationDispatchLease, error) {
	claimed, err := s.store.ClaimWork(ctx, eventstore.WorkClaimInput{
		Kind: request.Kind, Limit: request.Limit, LeaseDuration: request.LeaseDuration, Now: request.Now,
	})
	if err != nil {
		return nil, err
	}
	leases := make([]conversationruntime.DelegationDispatchLease, 0, len(claimed))
	for _, item := range claimed {
		leases = append(leases, conversationruntime.DelegationDispatchLease{
			WorkID: item.WorkID, DedupeKey: item.DedupeKey, Kind: item.Kind,
			SourceEventID: item.SourceEventID, SessionID: item.SessionID, Priority: item.Priority,
			Attempt: item.Attempt, LeaseToken: item.LeaseToken, LeaseExpires: item.LeaseExpiresAt,
			Version: item.Version, Assignee: item.Assignee,
		})
	}
	return leases, nil
}

func (s responseFirstDelegationDispatchStore) LoadDelegationDispatch(ctx context.Context, lease conversationruntime.DelegationDispatchLease, now time.Time) (conversationruntime.DelegationDispatchSource, error) {
	loaded, err := s.store.LoadDelegationDispatch(ctx, eventstore.DelegationDispatchInput{
		WorkID: lease.WorkID, LeaseToken: lease.LeaseToken, Version: lease.Version,
		Now: now, SessionID: lease.SessionID, Assignee: lease.Assignee,
	})
	if err != nil {
		return conversationruntime.DelegationDispatchSource{}, err
	}
	if err := validateResponseFirstDelegationDispatchSource(lease, loaded); err != nil {
		return conversationruntime.DelegationDispatchSource{}, err
	}
	batch, found, err := s.outcomes.LoadCommittedOutcome(ctx, loaded.Handoff.OriginTurnID)
	if err != nil {
		return conversationruntime.DelegationDispatchSource{}, fmt.Errorf("load committed Mio delegation notice: %w", err)
	}
	if !found {
		return conversationruntime.DelegationDispatchSource{}, errors.New("committed Mio delegation notice is not available for publication")
	}
	originalHandoff, err := responseFirstOriginHandoff(batch)
	if err != nil {
		return conversationruntime.DelegationDispatchSource{}, err
	}
	if err := validateResponseFirstDelegationNotice(batch, originalHandoff); err != nil {
		return conversationruntime.DelegationDispatchSource{}, err
	}
	responseTo, err := validateResponseFirstDelegationRevisionChain(ctx, s.store, s.outcomes, loaded, originalHandoff)
	if err != nil {
		return conversationruntime.DelegationDispatchSource{}, err
	}
	if err := s.publisher.PublishCommitted(ctx, toConversationCommittedBundle(batch)); err != nil {
		return conversationruntime.DelegationDispatchSource{}, fmt.Errorf("publish committed Mio delegation notice before readback: %w", err)
	}
	contextMessages := make([]conversationruntime.DelegationDispatchMessage, 0, len(loaded.Context))
	for _, message := range loaded.Context {
		contextMessages = append(contextMessages, conversationruntime.DelegationDispatchMessage{
			MessageID: message.MessageID, Role: message.Role, Text: message.Text,
		})
	}
	var failureIntent *conversationruntime.ReadbackFailureIntent
	if loaded.FailureIntent != nil {
		failureIntent = &conversationruntime.ReadbackFailureIntent{
			DelegationID: loaded.FailureIntent.DelegationID, Revision: loaded.FailureIntent.Revision,
			Assignee: loaded.FailureIntent.Assignee, Reason: conversationruntime.ReadbackFailureReason(loaded.FailureIntent.Reason),
		}
	}
	return conversationruntime.DelegationDispatchSource{
		Handoff: loaded.Handoff, Context: contextMessages, AcceptedAt: loaded.AcceptedAt,
		ReadbackDeadline: loaded.ReadbackDeadline, FailureIntent: failureIntent, ResponseTo: responseTo,
	}, nil
}

func responseFirstOriginHandoff(batch eventstore.CommittedBatch) (conversation.HandoffEnvelope, error) {
	if batch.ResultState != "awaiting_readback" {
		return conversation.HandoffEnvelope{}, errors.New("committed Mio delegation result is not awaiting readback")
	}
	var handoff conversation.HandoffEnvelope
	found := false
	for _, event := range batch.Events {
		if event.EventType != "conversation.delegation_context" || event.ComponentID != "conversation_runtime" ||
			event.SessionID != batch.SessionID || event.TurnID != batch.TurnID {
			continue
		}
		if err := modulecore.ValidateEventEnvelope(event); err != nil {
			return conversation.HandoffEnvelope{}, fmt.Errorf("committed Mio delegation context is not canonical: %w", err)
		}
		if found {
			return conversation.HandoffEnvelope{}, errors.New("committed Mio delegation result has multiple origin handoff contexts")
		}
		value, ok := event.Payload["handoff"]
		if !ok {
			return conversation.HandoffEnvelope{}, errors.New("committed Mio delegation context has no handoff envelope")
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return conversation.HandoffEnvelope{}, fmt.Errorf("encode committed Mio origin handoff: %w", err)
		}
		if err := json.Unmarshal(encoded, &handoff); err != nil {
			return conversation.HandoffEnvelope{}, fmt.Errorf("decode committed Mio origin handoff: %w", err)
		}
		if (event.RequestID != "" && event.RequestID != handoff.DelegationID) || event.TraceID != handoff.SourceTraceID {
			return conversation.HandoffEnvelope{}, errors.New("committed Mio origin handoff identity is inconsistent")
		}
		found = true
	}
	if !found {
		return conversation.HandoffEnvelope{}, errors.New("committed Mio origin handoff context is missing")
	}
	if err := conversation.ValidateHandoffEnvelope(handoff); err != nil {
		return conversation.HandoffEnvelope{}, fmt.Errorf("validate committed Mio origin handoff: %w", err)
	}
	if batch.SessionID != handoff.SessionID || batch.TurnID != handoff.OriginTurnID {
		return conversation.HandoffEnvelope{}, errors.New("committed Mio origin handoff does not match its Turn bundle")
	}
	return handoff, nil
}

func validateResponseFirstDelegationRevisionChain(
	ctx context.Context,
	store responseFirstDelegationPersistence,
	outcomes responseFirstJournalStore,
	loaded eventstore.DelegationDispatch,
	original conversation.HandoffEnvelope,
) (*conversationruntime.DelegationDispatchReplySource, error) {
	current := loaded.Handoff
	if err := validateResponseFirstHandoffImmutableLineage(original, current); err != nil {
		return nil, err
	}
	if current.Revision == original.Revision {
		if !reflect.DeepEqual(current, original) {
			return nil, errors.New("initial dispatched handoff differs from the committed origin handoff")
		}
		return nil, nil
	}
	reader, ok := store.(responseFirstEventReader)
	if !ok {
		return nil, errors.New("EventStore event reader is required to validate a continued handoff")
	}
	instruction, found, err := reader.GetByID(ctx, loaded.Work.SourceEventID)
	if err != nil {
		return nil, fmt.Errorf("load persisted continuation instruction: %w", err)
	}
	if !found {
		return nil, errors.New("persisted continuation instruction is missing")
	}
	if err := validateResponseFirstContinuationInstruction(instruction, current); err != nil {
		return nil, err
	}
	cause, found, err := reader.GetByID(ctx, instruction.CausationEventID)
	if err != nil {
		return nil, fmt.Errorf("load persisted continuation cause: %w", err)
	}
	if !found {
		return nil, errors.New("persisted continuation cause is missing")
	}
	if cause.EventType == "conversation.delegation_context" {
		var responseTo *conversationruntime.DelegationDispatchReplySource
		if err := validateResponseFirstAcceptedReplyContext(ctx, outcomes, current, cause, instruction, loaded.Context, true, &responseTo); err != nil {
			return nil, err
		}
		return responseTo, nil
	}
	if cause.EventType != "conversation.clarification_question" {
		return nil, errors.New("persisted continuation cause is neither a clarification question nor an accepted reply context")
	}
	if current.ContextSnapshotHash == original.ContextSnapshotHash {
		if err := validateResponseFirstHandoffLineage(original, current); err != nil {
			return nil, err
		}
	}
	previousRevision := current.Revision - 1
	if err := validateResponseFirstContinuationQuestion(cause, current, previousRevision); err != nil {
		return nil, err
	}
	decision, found, err := outcomes.LoadClarificationDecisionOutcome(ctx, cause.TurnID)
	if err != nil {
		return nil, fmt.Errorf("load durable continuation decision receipt: %w", err)
	}
	if !found {
		return nil, errors.New("durable continuation decision receipt is missing")
	}
	if err := validateResponseFirstContinuationReceipt(decision, current, previousRevision, instruction, cause.TurnID); err != nil {
		return nil, err
	}
	if current.ContextSnapshotHash != original.ContextSnapshotHash {
		if len(decision.Events) == 0 {
			return nil, errors.New("durable continuation receipt omits the current private context")
		}
		if err := validateResponseFirstAcceptedReplyContext(ctx, outcomes, current, decision.Events[0], instruction, loaded.Context, false, nil); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func validateResponseFirstHandoffLineage(original, current conversation.HandoffEnvelope) error {
	if err := validateResponseFirstHandoffImmutableLineage(original, current); err != nil {
		return err
	}
	if current.ContextSnapshotHash != original.ContextSnapshotHash {
		return errors.New("current delegation handoff does not preserve its committed origin lineage")
	}
	return nil
}

func validateResponseFirstHandoffImmutableLineage(original, current conversation.HandoffEnvelope) error {
	if err := conversation.ValidateHandoffEnvelope(current); err != nil {
		return fmt.Errorf("validate loaded private delegation handoff before publication: %w", err)
	}
	if current.Revision < original.Revision || current.Revision == 0 {
		return errors.New("current delegation handoff revision predates its committed origin")
	}
	expected := original
	expected.Revision = current.Revision
	expected.Instruction = current.Instruction
	expected.InstructionMessageID = current.InstructionMessageID
	expected.ContextSnapshotHash = current.ContextSnapshotHash
	if !reflect.DeepEqual(expected, current) {
		return errors.New("current delegation handoff does not preserve its committed origin lineage")
	}
	return nil
}

func validateResponseFirstAcceptedReplyContext(
	ctx context.Context,
	outcomes responseFirstJournalStore,
	handoff conversation.HandoffEnvelope,
	contextEvent modulecore.EventEnvelope,
	instruction modulecore.EventEnvelope,
	loadedMessages []eventstore.DelegationContextMessage,
	directContinuation bool,
	responseTo **conversationruntime.DelegationDispatchReplySource,
) error {
	if responseTo != nil {
		*responseTo = nil
	}
	if err := modulecore.ValidateEventEnvelope(contextEvent); err != nil {
		return fmt.Errorf("persisted reply context is not canonical: %w", err)
	}
	if contextEvent.EventType != "conversation.delegation_context" || contextEvent.ComponentID != "conversation_runtime" ||
		contextEvent.SessionID != handoff.SessionID || contextEvent.TurnID != handoff.OriginTurnID ||
		contextEvent.RequestID != handoff.DelegationID || contextEvent.TraceID != handoff.SourceTraceID {
		return errors.New("persisted reply context does not match the current private handoff")
	}
	decodeSnapshot := func(event modulecore.EventEnvelope) (string, string, []eventstore.DelegationContextMessage, string, conversation.HandoffEnvelope, error) {
		sourceRevision, sourceOK := event.Payload["source_revision"].(string)
		cutoverRevision, cutoverOK := event.Payload["cutover_revision"].(string)
		storedHash, hashOK := event.Payload["context_snapshot_hash"].(string)
		rawMessages, messagesOK := event.Payload["messages"]
		rawHandoff, handoffOK := event.Payload["handoff"]
		if !sourceOK || !cutoverOK || !hashOK || !messagesOK || !handoffOK {
			return "", "", nil, "", conversation.HandoffEnvelope{}, errors.New("persisted reply context has an incomplete snapshot")
		}
		encodedMessages, err := json.Marshal(rawMessages)
		if err != nil {
			return "", "", nil, "", conversation.HandoffEnvelope{}, err
		}
		var messages []eventstore.DelegationContextMessage
		if err := json.Unmarshal(encodedMessages, &messages); err != nil {
			return "", "", nil, "", conversation.HandoffEnvelope{}, err
		}
		encodedHandoff, err := json.Marshal(rawHandoff)
		if err != nil {
			return "", "", nil, "", conversation.HandoffEnvelope{}, err
		}
		var snapshotHandoff conversation.HandoffEnvelope
		if err := json.Unmarshal(encodedHandoff, &snapshotHandoff); err != nil {
			return "", "", nil, "", conversation.HandoffEnvelope{}, err
		}
		material, err := json.Marshal(struct {
			SourceRevision  string                                `json:"source_revision"`
			CutoverRevision string                                `json:"cutover_revision"`
			Messages        []eventstore.DelegationContextMessage `json:"messages"`
		}{SourceRevision: sourceRevision, CutoverRevision: cutoverRevision, Messages: messages})
		if err != nil {
			return "", "", nil, "", conversation.HandoffEnvelope{}, err
		}
		digest := sha256.Sum256(material)
		return sourceRevision, cutoverRevision, messages, storedHash, snapshotHandoff, func() error {
			if hex.EncodeToString(digest[:]) != storedHash || snapshotHandoff.ContextSnapshotHash != storedHash {
				return errors.New("persisted reply context hash does not match its content and handoff")
			}
			return nil
		}()
	}
	sourceRevision, cutoverRevision, messages, storedHash, snapshotHandoff, err := decodeSnapshot(contextEvent)
	if err != nil {
		return fmt.Errorf("validate persisted reply context snapshot: %w", err)
	}
	if storedHash != handoff.ContextSnapshotHash || !reflect.DeepEqual(messages, loadedMessages) ||
		!sameResponseFirstHandoffLineage(snapshotHandoff, handoff) {
		return errors.New("persisted reply context content does not bind the loaded handoff")
	}
	if len(messages) < 2 || messages[len(messages)-1].Role != "user" || messages[len(messages)-1].MessageID == "" {
		return errors.New("persisted reply context does not end with a canonical user answer")
	}
	tail, err := outcomes.ReadCompleteSessionTail(ctx, handoff.SessionID, 0)
	if err != nil {
		return fmt.Errorf("load canonical reply message tail: %w", err)
	}
	if !tail.Complete {
		return errors.New("canonical reply message tail is incomplete")
	}
	var replyTurnID modulecore.TurnID
	for _, message := range tail.Messages {
		if message.Index.MessageID != messages[len(messages)-1].MessageID {
			continue
		}
		if replyTurnID != "" || message.Event.EventType != "conversation.user_message" || message.Event.ComponentID != "orchestrator" ||
			message.Event.SessionID != handoff.SessionID || message.Event.TurnID != message.Index.TurnID ||
			message.Event.MessageID != message.Index.MessageID || message.Index.FromActor == "" ||
			message.Index.ToKind != "agent" || message.Index.ToActor != "mio" || message.Index.Kind != "user_message" ||
			message.Index.Visibility != "public" || responseFirstPayloadString(message.Event.Payload, "source_actor_id") != message.Index.FromActor ||
			responseFirstPayloadString(message.Event.Payload, "reply_to_message_id") != string(message.Index.ReplyToMessageID) {
			return errors.New("reply context answer is not a canonical authenticated user message")
		}
		replyTurnID = message.Index.TurnID
	}
	if replyTurnID == "" {
		return errors.New("reply context answer is missing from the canonical message tail")
	}
	reply, found, err := outcomes.LoadClarificationReplyContinuationOutcome(ctx, replyTurnID)
	if err != nil {
		return fmt.Errorf("load durable accepted clarification reply receipt: %w", err)
	}
	if !found || reply.ReplyTurnID != replyTurnID || reply.SessionID != handoff.SessionID ||
		reply.DelegationID != handoff.DelegationID || reply.NextHandoff == nil ||
		reply.Revision == 0 || reply.NextRevision != reply.Revision+1 || reply.NextRevision > handoff.Revision {
		return errors.New("durable accepted clarification reply receipt does not bind the current handoff")
	}
	ownerState, ownerFound, err := outcomes.ReadSessionState(ctx, handoff.SessionID, reply.OwnerID)
	if err != nil {
		return fmt.Errorf("validate accepted reply session owner: %w", err)
	}
	if !ownerFound || ownerState.OwnerID != reply.OwnerID {
		return errors.New("durable accepted clarification reply owner does not own the handoff session")
	}
	replySource, replyCutover, replyMessages, replyHash, replyHandoff, err := decodeSnapshot(reply.ContextEvent)
	if err != nil || replySource != sourceRevision || replyCutover != cutoverRevision ||
		!reflect.DeepEqual(replyMessages, messages) || replyHash != storedHash || !sameResponseFirstHandoffLineage(replyHandoff, *reply.NextHandoff) {
		return errors.New("durable accepted clarification reply receipt does not bind the current context content")
	}
	if reply.ReplyTurnID != replyTurnID || reply.ReplyClientSubmissionID == "" ||
		reply.ReplyClientSubmissionID != strings.TrimSpace(reply.ReplyClientSubmissionID) {
		return errors.New("durable accepted clarification reply receipt does not bind canonical answer identity")
	}
	if directContinuation {
		if responseTo == nil || reply.NextRevision != handoff.Revision || reply.Revision+1 != handoff.Revision ||
			reply.ResultState != "awaiting_readback" || !reply.DispatchQueued || !reflect.DeepEqual(*reply.NextHandoff, handoff) ||
			!reflect.DeepEqual(reply.ContextEvent, contextEvent) || !reflect.DeepEqual(reply.Event, instruction) ||
			instruction.CausationEventID != contextEvent.EventID {
			return errors.New("direct clarification reply continuation events do not exactly bind the dispatched handoff")
		}
		*responseTo = &conversationruntime.DelegationDispatchReplySource{
			TurnID: replyTurnID, ClientSubmissionID: reply.ReplyClientSubmissionID,
		}
	} else if !sameResponseFirstHandoffLineage(*reply.NextHandoff, handoff) {
		return errors.New("durable accepted clarification reply receipt changes immutable handoff lineage")
	}
	return nil
}

func sameResponseFirstHandoffLineage(left, right conversation.HandoffEnvelope) bool {
	left.Revision, right.Revision = 1, 1
	left.Instruction, right.Instruction = "", ""
	left.InstructionMessageID, right.InstructionMessageID = "", ""
	return reflect.DeepEqual(left, right)
}

func validateResponseFirstContinuationInstruction(event modulecore.EventEnvelope, handoff conversation.HandoffEnvelope) error {
	if err := modulecore.ValidateEventEnvelope(event); err != nil {
		return fmt.Errorf("persisted continuation instruction is not canonical: %w", err)
	}
	if event.EventType != "conversation.delegation_instruction" || event.ComponentID != "delegation_mailbox" ||
		event.SessionID != handoff.SessionID || event.TurnID != handoff.OriginTurnID || event.RequestID != handoff.DelegationID ||
		event.ActorKind != "agent" || event.ActorID != "mio" || event.MessageID != handoff.InstructionMessageID ||
		responseFirstPayloadString(event.Payload, "kind") != "delegation_instruction" ||
		responseFirstPayloadString(event.Payload, "visibility") != "private" ||
		responseFirstPayloadString(event.Payload, "delegation_id") != string(handoff.DelegationID) ||
		responseFirstPayloadString(event.Payload, "to") != handoff.ToActor ||
		!responseFirstPayloadRevisionMatches(event.Payload["delegation_revision"], handoff.Revision) ||
		responseFirstPayloadString(event.Payload, "text") != handoff.Instruction {
		return errors.New("persisted continuation instruction does not match the current private handoff")
	}
	if event.CausationEventID == "" {
		return errors.New("persisted continuation instruction has no clarification question cause")
	}
	return nil
}

func validateResponseFirstContinuationQuestion(event modulecore.EventEnvelope, handoff conversation.HandoffEnvelope, revision uint64) error {
	if err := modulecore.ValidateEventEnvelope(event); err != nil {
		return fmt.Errorf("persisted clarification question is not canonical: %w", err)
	}
	if event.EventType != "conversation.clarification_question" || event.ComponentID != "conversation_runtime" ||
		event.SessionID != handoff.SessionID || event.RequestID != handoff.DelegationID || event.TurnID == "" ||
		event.TraceID != handoff.SourceTraceID || responseFirstPayloadString(event.Payload, "kind") != "clarification_question" ||
		responseFirstPayloadString(event.Payload, "visibility") != "private" ||
		responseFirstPayloadString(event.Payload, "delegation_id") != string(handoff.DelegationID) ||
		!responseFirstPayloadRevisionMatches(event.Payload["delegation_revision"], revision) {
		return errors.New("persisted clarification question does not match the prior handoff revision")
	}
	return nil
}

func validateResponseFirstContinuationReceipt(
	result eventstore.ClarificationDecisionCommit,
	handoff conversation.HandoffEnvelope,
	previousRevision uint64,
	instruction modulecore.EventEnvelope,
	controlTurnID modulecore.TurnID,
) error {
	if result.OperationID == "" || result.SessionID != handoff.SessionID || result.ControlTurnID != controlTurnID ||
		result.DelegationID != handoff.DelegationID || result.Revision != previousRevision ||
		result.Kind != conversation.ClarificationDecisionContinueDelegation || result.TerminalReason != "" ||
		result.ResultState != "awaiting_readback" || !result.DispatchQueued || result.NextHandoff == nil ||
		!reflect.DeepEqual(*result.NextHandoff, handoff) || result.Event.EventID != instruction.EventID ||
		!reflect.DeepEqual(result.Event, instruction) || len(result.Events) != 3 || !reflect.DeepEqual(result.Events[1], instruction) ||
		result.Events[0].EventType != "conversation.delegation_context" || result.Events[0].ComponentID != "conversation_runtime" ||
		result.Events[0].SessionID != handoff.SessionID || result.Events[0].TurnID != handoff.OriginTurnID ||
		result.Events[0].RequestID != handoff.DelegationID || !reflect.DeepEqual(result.Events[2], result.ReceiptEvent) ||
		result.ReceiptEvent.EventType != "conversation.mio_emission_receipt" || result.ReceiptEvent.ComponentID != "conversation_runtime" {
		return errors.New("durable continuation receipt does not bind the current private handoff")
	}
	var contextHandoff conversation.HandoffEnvelope
	encoded, err := json.Marshal(result.Events[0].Payload["handoff"])
	if err != nil || json.Unmarshal(encoded, &contextHandoff) != nil || !reflect.DeepEqual(contextHandoff, handoff) {
		return errors.New("durable continuation context does not contain its exact next handoff")
	}
	if result.ReceiptEvent.TurnID != controlTurnID || result.Events[2].TurnID != controlTurnID ||
		result.Event.CausationEventID == "" {
		return errors.New("durable continuation receipt has inconsistent control Turn identities")
	}
	for _, event := range result.Events {
		if err := modulecore.ValidateEventEnvelope(event); err != nil {
			return fmt.Errorf("durable continuation bundle contains a non-canonical event: %w", err)
		}
	}
	return nil
}

func validateResponseFirstDelegationDispatchSource(lease conversationruntime.DelegationDispatchLease, loaded eventstore.DelegationDispatch) error {
	if err := conversation.ValidateHandoffEnvelope(loaded.Handoff); err != nil {
		return fmt.Errorf("validate loaded private delegation handoff before publication: %w", err)
	}
	if loaded.Work.WorkID != lease.WorkID || loaded.Work.LeaseToken != lease.LeaseToken || loaded.Work.Version != lease.Version ||
		loaded.Work.SessionID != lease.SessionID || loaded.Work.Assignee != lease.Assignee ||
		loaded.Handoff.SessionID != lease.SessionID || loaded.Handoff.ToActor != lease.Assignee ||
		lease.DedupeKey != fmt.Sprintf("delegation:%s:%d:start", loaded.Handoff.DelegationID, loaded.Handoff.Revision) {
		return errors.New("loaded delegation handoff does not match the guarded lease")
	}
	return nil
}

func (s responseFirstDelegationDispatchStore) AcceptReadback(ctx context.Context, request conversationruntime.AcceptReadbackRequest) (conversationruntime.ReadbackAcceptance, error) {
	accepted, err := s.store.AcceptReadback(ctx, eventstore.ReadbackInput{
		OperationID: request.OperationID, DelegationID: request.DelegationID, Revision: request.Revision,
		Assignee: request.Assignee, Readback: request.Readback, EventID: request.EventID,
		MessageID: request.MessageID, OccurredAt: request.OccurredAt,
	})
	if err != nil {
		return conversationruntime.ReadbackAcceptance{}, err
	}
	return conversationruntime.ReadbackAcceptance{
		OperationID: accepted.OperationID, DelegationID: accepted.DelegationID, Revision: accepted.Revision,
		OriginTurnID: accepted.OriginTurnID, SessionID: accepted.SessionID, Status: accepted.Status,
		Availability: accepted.Availability, TaskStartQueued: accepted.TaskStartQueued, Event: accepted.Event,
	}, nil
}

func (s responseFirstDelegationDispatchStore) FailReadback(ctx context.Context, request conversationruntime.FailReadbackRequest) (conversationruntime.ReadbackFailure, error) {
	failure, err := s.store.FailReadback(ctx, eventstore.ReadbackFailureInput{
		OperationID: request.OperationID,
		Work: eventstore.WorkLeaseInput{
			WorkID: request.Lease.WorkID, LeaseToken: request.Lease.LeaseToken, Version: request.Lease.Version, Now: request.Now,
		},
		DelegationID: request.DelegationID, Revision: request.Revision, Assignee: request.Assignee,
		Reason: eventstore.ReadbackFailureReason(request.Reason), EventID: request.EventID,
		MessageID: request.MessageID, OccurredAt: request.OccurredAt,
	})
	if err != nil {
		return conversationruntime.ReadbackFailure{}, err
	}
	return conversationruntime.ReadbackFailure{
		OperationID: failure.OperationID, DelegationID: failure.DelegationID, Revision: failure.Revision,
		OriginTurnID: failure.OriginTurnID, SessionID: failure.SessionID,
		Reason: conversationruntime.ReadbackFailureReason(failure.Reason), Availability: failure.Availability,
		Event: failure.Event,
	}, nil
}

func (s responseFirstDelegationDispatchStore) AckDispatch(ctx context.Context, lease conversationruntime.DelegationDispatchLease, now time.Time) error {
	return s.store.AckWork(ctx, eventstore.WorkLeaseInput{
		WorkID: lease.WorkID, LeaseToken: lease.LeaseToken, Version: lease.Version, Now: now,
	})
}

func (s responseFirstDelegationDispatchStore) RetryDispatch(ctx context.Context, lease conversationruntime.DelegationDispatchLease, now time.Time) error {
	return s.store.RetryWork(ctx, eventstore.WorkRetryInput{WorkLeaseInput: eventstore.WorkLeaseInput{
		WorkID: lease.WorkID, LeaseToken: lease.LeaseToken, Version: lease.Version, Now: now,
	}})
}

func validateResponseFirstDelegationNotice(batch eventstore.CommittedBatch, handoff conversation.HandoffEnvelope) error {
	if batch.ResultState != "awaiting_readback" || batch.SessionID != handoff.SessionID || batch.TurnID != handoff.OriginTurnID {
		return errors.New("committed Mio delegation result does not match the dispatched handoff")
	}
	for _, event := range batch.Events {
		if event.EventType != "agent.response" || event.ComponentID != "orchestrator" || event.EventSeq <= 0 ||
			event.SessionID != handoff.SessionID || event.TurnID != handoff.OriginTurnID || event.TraceID != handoff.SourceTraceID {
			continue
		}
		payload := event.Payload
		if responseFirstPayloadString(payload, "kind") != "delegation_notice" ||
			responseFirstPayloadString(payload, "visibility") != "public" ||
			responseFirstPayloadString(payload, "to") != "user" ||
			responseFirstPayloadString(payload, "delegation_id") != string(handoff.DelegationID) ||
			!responseFirstPayloadRevisionMatches(payload["delegation_revision"], handoff.Revision) {
			continue
		}
		if err := modulecore.ValidateEventEnvelope(event); err != nil {
			return fmt.Errorf("committed Mio delegation notice is not canonical: %w", err)
		}
		return nil
	}
	return errors.New("committed Mio delegation notice is missing from the origin turn")
}

func responseFirstPayloadString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}

func responseFirstPayloadRevisionMatches(value any, want uint64) bool {
	switch number := value.(type) {
	case uint64:
		return number == want
	case uint:
		return uint64(number) == want
	case int64:
		return number >= 0 && uint64(number) == want
	case int:
		return number >= 0 && uint64(number) == want
	case float64:
		return number >= 0 && number == float64(want)
	default:
		return false
	}
}

func (r *responseFirstConversationRuntime) startReadbackDrain() func() {
	if r == nil || r.dispatcher == nil || r.dispatchWake == nil {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(responseFirstReadbackDrainPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-r.dispatchWake:
			}
			for batch := 0; batch < responseFirstReadbackDrainBatch; batch++ {
				workCtx, workCancel := context.WithTimeout(ctx, responseFirstReadbackTimeout)
				result, err := r.dispatcher.DispatchOne(workCtx)
				workCancel()
				if err != nil {
					log.Printf("response-first delegation readback deferred error_type=%T", err)
					break
				}
				if !result.Claimed {
					break
				}
			}
		}
	}()
	r.signalReadbackDrain()
	return func() {
		cancel()
		<-done
	}
}
