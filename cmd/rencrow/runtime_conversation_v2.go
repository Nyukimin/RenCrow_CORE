package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	responseFirstActorID                       = "viewer-user"
	responseFirstRuntimeVer                    = "v2"
	responseFirstPolicyVer                     = "response-first-v1.0"
	responseFirstTriggerKind                   = "user_message"
	responseFirstClarificationReplyTriggerKind = "clarification_reply"
)

type responseFirstEventPublisher interface {
	conversationruntime.CommittedEventPublisher
	PublishAcceptedInput(context.Context, modulecore.EventEnvelope) error
}

type responseFirstJournalStore interface {
	AcceptTurn(context.Context, eventstore.AcceptInput) (eventstore.AcceptedTurn, error)
	CommitTurnBundle(context.Context, eventstore.CommitInput) (eventstore.CommittedBatch, error)
	LoadCommittedOutcome(context.Context, modulecore.TurnID) (eventstore.CommittedBatch, bool, error)
	ReadSessionState(context.Context, modulecore.SessionID, string) (eventstore.SessionState, bool, error)
	ReadLegacySessionProjection(context.Context, modulecore.SessionID, string, modulecore.EventSeq) (eventstore.LegacyProjection, error)
	ReadPendingAcceptedTurns(context.Context, modulecore.SessionID, string) ([]eventstore.AcceptedTurn, error)
	ReadCompleteSessionTail(context.Context, modulecore.SessionID, modulecore.EventSeq) (eventstore.CanonicalTail, error)
	LoadClarificationControlContext(context.Context, eventstore.ClarificationControlContextInput) (eventstore.ClarificationControlContext, error)
	CommitClarificationDecision(context.Context, eventstore.ClarificationDecisionInput) (eventstore.ClarificationDecisionCommit, error)
	CommitClarificationTerminal(context.Context, eventstore.ClarificationTerminalInput) (eventstore.ClarificationDecisionCommit, error)
	LoadClarificationDecisionOutcome(context.Context, modulecore.TurnID) (eventstore.ClarificationDecisionCommit, bool, error)
	LoadClarificationReplyContext(context.Context, eventstore.ClarificationReplyContextInput) (eventstore.ClarificationReplyContext, error)
	LoadClarificationReplyContinuationOutcome(context.Context, modulecore.TurnID) (eventstore.ClarificationReplyContinuationCommit, bool, error)
	CommitClarificationReplyContinuation(context.Context, eventstore.ClarificationReplyContinuationInput) (eventstore.ClarificationReplyContinuationCommit, error)
}

type responseFirstReservationReader interface {
	IsCanonicalV2Reserved(context.Context, string, string) (bool, error)
}

type responseFirstConversationProvider struct {
	provider interface {
		GenerateConversation(context.Context, domainconversation.ConversationScope, llm.GenerateRequest) (llm.GenerateResponse, error)
	}
	name string
}

func (p responseFirstConversationProvider) GenerateConversation(ctx context.Context, scope domainconversation.ConversationScope, request llm.GenerateRequest) (llm.GenerateResponse, error) {
	return p.provider.GenerateConversation(ctx, scope, request)
}

func (p responseFirstConversationProvider) Name() string {
	if strings.TrimSpace(p.name) != "" {
		return strings.TrimSpace(p.name)
	}
	return "conversation"
}

type responseFirstConversationRuntime struct {
	store          responseFirstJournalStore
	reservations   responseFirstReservationReader
	runtime        *conversationruntime.Runtime
	ownerID        string
	publisher      responseFirstEventPublisher
	processing     *responseFirstV2Processing
	dispatcher     responseFirstReadbackDispatcher
	taskAdmissions []*conversationruntime.DelegationTaskAdmission
	executor       *conversationruntime.DelegatedTaskExecutor
	reportDelivery *conversationruntime.ReportDeliveryService
	dispatchWake   chan struct{}
	projection     *responseFirstConversationProjection
}

func newResponseFirstConversationRuntime(
	store responseFirstJournalStore,
	mio conversationruntime.PromptComposer,
	provider interface {
		GenerateConversation(context.Context, domainconversation.ConversationScope, llm.GenerateRequest) (llm.GenerateResponse, error)
	},
	publisher responseFirstEventPublisher,
	ownerID string,
	readback ...responseFirstReadbackWiring,
) (*responseFirstConversationRuntime, error) {
	return newResponseFirstConversationRuntimeWithProjection(store, mio, provider, publisher, ownerID, nil, readback...)
}

func newResponseFirstConversationRuntimeWithProjection(
	store responseFirstJournalStore,
	mio conversationruntime.PromptComposer,
	provider interface {
		GenerateConversation(context.Context, domainconversation.ConversationScope, llm.GenerateRequest) (llm.GenerateResponse, error)
	},
	publisher responseFirstEventPublisher,
	ownerID string,
	selectedProjectionOwner any,
	readback ...responseFirstReadbackWiring,
) (*responseFirstConversationRuntime, error) {
	if store == nil || mio == nil || provider == nil || publisher == nil || ownerID == "" {
		return nil, errors.New("response-first conversation dependencies are unavailable")
	}
	var projection *responseFirstConversationProjection
	var projectionChildren *responseFirstProjectionChildren
	if selectedProjectionOwner != nil {
		projectionPersistence, storeOK := store.(responseFirstProjectionPersistence)
		projectionL1, l1OK := selectedProjectionOwner.(responseFirstCanonicalProjectionL1)
		if !storeOK || !l1OK {
			return nil, errors.New("response-first canonical projection requires the selected EventStore and Chat L1 owner projection ports")
		}
		projectionChildren = newResponseFirstProjectionChildren()
		projection = newResponseFirstConversationProjection(projectionPersistence, projectionL1, ownerID, projectionChildren, publisher)
		publisher = &responseFirstProjectionPublisher{store: projectionPersistence, ownerID: ownerID, next: publisher, wake: projection.wake}
	}
	var dispatcher responseFirstReadbackDispatcher
	allowedAssignees := []string(nil)
	if len(readback) > 0 {
		assignees := filterResponseFirstReadbackAssignees(readback[0].Assignees)
		if dispatchStore, ok := store.(responseFirstDelegationPersistence); ok && len(assignees) != 0 {
			readbackGenerator := agent.NewReadbackGenerator(assignees)
			configuredDispatcher, err := conversationruntime.NewDelegationDispatcher(conversationruntime.DelegationDispatcherDependencies{
				Store:     responseFirstDelegationDispatchStore{store: dispatchStore, outcomes: store, publisher: publisher},
				Generator: readbackGenerator, Publisher: publisher,
				Config: conversationruntime.DelegationDispatcherConfig{
					PolicyVersion: responseFirstPolicyVer, LeaseDuration: responseFirstReadbackLeaseDuration,
				},
			})
			if err != nil {
				return nil, fmt.Errorf("configure response-first delegation readback: %w", err)
			}
			dispatcher = configuredDispatcher
			allowedAssignees = responseFirstAssigneeIDs(assignees)
		}
	}
	runtime, err := conversationruntime.NewRuntime(conversationruntime.RuntimeDependencies{
		ReadViews:            conversationruntime.NewReadViewService(responseFirstCanonicalReader{store: store}),
		Composer:             mio,
		Generator:            agent.NewMioEmissionGenerator(responseFirstConversationProvider{provider: provider}),
		Journal:              responseFirstTurnJournal{store: store, projectionChildren: projectionChildren},
		ClarificationReplies: responseFirstTurnJournal{store: store},
		Publisher:            publisher,
		Config: conversationruntime.RuntimeConfig{
			PolicyVersion: responseFirstPolicyVer, AllowedAssignees: allowedAssignees,
		},
	})
	if err != nil {
		return nil, err
	}
	var dispatchWake chan struct{}
	if dispatcher != nil {
		dispatchWake = make(chan struct{}, 1)
	}
	return &responseFirstConversationRuntime{
		store: store, runtime: runtime, ownerID: ownerID, publisher: publisher,
		dispatcher: dispatcher, dispatchWake: dispatchWake, projection: projection,
	}, nil
}

func (r *responseFirstConversationRuntime) Accept(ctx context.Context, request viewer.V2SendRequest) (viewer.V2AcceptedTurn, error) {
	if r == nil || r.store == nil || ctx == nil {
		return viewer.V2AcceptedTurn{}, errors.New("response-first conversation runtime is unavailable")
	}
	if request.ActorID != responseFirstActorID || request.To != modulechat.ViewerRecipientMio {
		return viewer.V2AcceptedTurn{}, errors.New("response-first Viewer actor or recipient is invalid")
	}
	if err := request.SessionID.Validate(); err != nil {
		return viewer.V2AcceptedTurn{}, err
	}
	if err := request.TraceID.Validate(); err != nil {
		return viewer.V2AcceptedTurn{}, err
	}
	if err := request.TurnID.Validate(); err != nil {
		return viewer.V2AcceptedTurn{}, err
	}
	if err := request.InputMessageID.Validate(); err != nil {
		return viewer.V2AcceptedTurn{}, err
	}
	if request.ReplyToMessageID != "" {
		if err := request.ReplyToMessageID.Validate(); err != nil {
			return viewer.V2AcceptedTurn{}, err
		}
		if r.projection == nil {
			return viewer.V2AcceptedTurn{}, errors.New("response-first clarification reply projection lane is unavailable")
		}
	}

	state, stateFound, err := r.store.ReadSessionState(ctx, request.SessionID, r.ownerID)
	if err != nil {
		return viewer.V2AcceptedTurn{}, fmt.Errorf("read response-first session state: %w", err)
	}
	createNewSession := !stateFound
	epoch := int64(1)
	if stateFound {
		if state.RuntimeVersion != conversationruntime.ConversationRuntimeV2 {
			log.Printf("[Viewer] response-first v2 refused legacy session_id=%s runtime_version=%s; verified idle cutover is unavailable", request.SessionID, state.RuntimeVersion)
			return viewer.V2AcceptedTurn{}, errors.New("response-first v2 refuses an existing legacy session; verified idle cutover is unavailable")
		}
		epoch = state.GenerationEpoch
	} else {
		projection, projectionErr := r.store.ReadLegacySessionProjection(ctx, request.SessionID, r.ownerID, 0)
		if projectionErr == nil && projection.AppliedThrough > 0 {
			log.Printf("[Viewer] response-first v2 refused legacy session_id=%s legacy_through_seq=%d; verified idle cutover is unavailable", request.SessionID, projection.AppliedThrough)
			return viewer.V2AcceptedTurn{}, errors.New("response-first v2 refuses an existing legacy session; verified idle cutover is unavailable")
		}
		if projectionErr != nil && !errors.Is(projectionErr, eventstore.ErrLegacySessionBoundaryConflict) {
			return viewer.V2AcceptedTurn{}, fmt.Errorf("verify response-first session has no legacy history: %w", projectionErr)
		}
		if r.reservations == nil {
			return viewer.V2AcceptedTurn{}, errors.New("response-first v2 reservation owner is unavailable")
		}
		reserved, reservationErr := r.reservations.IsCanonicalV2Reserved(ctx, string(request.SessionID), r.ownerID)
		if reservationErr != nil || !reserved {
			return viewer.V2AcceptedTurn{}, errors.New("response-first v2 requires a matching owner reservation before acceptance")
		}
	}

	payload := map[string]any{
		"from": "user", "source_actor_id": request.ActorID, "to": "mio", "text": request.Message,
		"content": request.Message, "visibility": "public", "client_submission_id": request.ClientSubmissionID,
	}
	triggerKind := responseFirstTriggerKind
	if request.ReplyToMessageID != "" {
		triggerKind = responseFirstClarificationReplyTriggerKind
		payload["reply_to_message_id"] = string(request.ReplyToMessageID)
	}
	inputEvent := modulecore.NewEventEnvelope(request.TraceID, "", nil, "orchestrator", "conversation.user_message", time.Now().UTC(), payload)
	inputEvent.SessionID = request.SessionID
	inputEvent.TurnID = request.TurnID
	inputEvent.MessageID = request.InputMessageID
	messageIndex := eventstore.MessageIndex{
		MessageID: request.InputMessageID, EventID: inputEvent.EventID, SessionID: request.SessionID,
		TurnID: request.TurnID, FromActor: request.ActorID, ToKind: "agent", ToActor: "mio",
		Kind: "user_message", ReplyToMessageID: request.ReplyToMessageID, Visibility: "public",
	}
	precommitReplyProjectionWorkID := ""
	if triggerKind == responseFirstClarificationReplyTriggerKind {
		precommitReplyProjectionWorkID = eventstore.ConversationProjectionWorkID(inputEvent.EventID)
		r.projection.children.registerBeforeCommit(ctx, precommitReplyProjectionWorkID)
	}
	accepted, err := r.store.AcceptTurn(ctx, eventstore.AcceptInput{
		OperationID: stableViewerAcceptOperationID(r.ownerID, request.SessionID, request.ClientSubmissionID),
		OwnerID:     r.ownerID, RuntimeVersion: responseFirstRuntimeVer,
		ClientSubmissionID: request.ClientSubmissionID, ActorID: request.ActorID,
		TriggerKind: triggerKind, GenerationEpoch: epoch, CreateNewSession: createNewSession,
		InputEvent: inputEvent, InputMessage: messageIndex,
	})
	if errors.Is(err, eventstore.ErrClientSubmissionConflict) || errors.Is(err, eventstore.ErrOperationConflict) {
		if precommitReplyProjectionWorkID != "" {
			markResponseFirstTurnWorkTrackingIncomplete(ctx)
			r.projection.children.abandonUncommitted(precommitReplyProjectionWorkID)
		}
		return viewer.V2AcceptedTurn{}, viewer.ErrV2SubmissionConflict
	}
	if err != nil {
		if precommitReplyProjectionWorkID != "" {
			// AcceptTurn may have committed before its acknowledgement was lost.
			// Keep the pre-registered child unresolved until durable receipt lookup
			// can prove which outbox item owns it.
			markResponseFirstTurnWorkTrackingIncomplete(ctx)
		}
		return viewer.V2AcceptedTurn{}, fmt.Errorf("accept response-first turn: %w", err)
	}
	if accepted.SessionID != request.SessionID || accepted.ActorID != request.ActorID || accepted.ClientSubmissionID != request.ClientSubmissionID {
		if precommitReplyProjectionWorkID != "" {
			markResponseFirstTurnWorkTrackingIncomplete(ctx)
		}
		return viewer.V2AcceptedTurn{}, errors.New("response-first acceptance receipt identity mismatch")
	}
	if precommitReplyProjectionWorkID != "" && accepted.InputEvent.EventID != inputEvent.EventID {
		// The operation replay resolved to an earlier accepted EventID. Retire
		// only this request's non-durable provisional child. The canonical work
		// may already have drained, so do not attach a child after the commit.
		markResponseFirstTurnWorkTrackingIncomplete(ctx)
		r.projection.children.abandonUncommitted(precommitReplyProjectionWorkID)
	}
	if accepted.TriggerKind == responseFirstClarificationReplyTriggerKind {
		// Reply acceptance already registered durable projection work. The
		// singleton projection lane publishes the canonical answer before it
		// opens the projection marker and applies the answer to L1.
		signalResponseFirstDrainLane(r.projection.wake)
	} else if publishErr := r.publisher.PublishAcceptedInput(ctx, accepted.InputEvent); publishErr != nil {
		// Acceptance is already durable. Publication is retried by the canonical
		// relay drain; it must not turn a committed acceptance into an HTTP error.
		log.Printf("[Viewer] accepted response-first input publication pending session_id=%s turn_id=%s error_type=%T", accepted.SessionID, accepted.TurnID, publishErr)
	}

	// Accepted turns are resumed on a client retry. Completed turns use their
	// stored result receipt, so processing only republishes and never generates.
	idempotentReplay := true
	pending, err := r.store.ReadPendingAcceptedTurns(ctx, accepted.SessionID, r.ownerID)
	if err != nil {
		return viewer.V2AcceptedTurn{}, fmt.Errorf("read accepted response-first turn: %w", err)
	}
	for _, item := range pending {
		if item.TurnID == accepted.TurnID && item.State == "accepted" {
			idempotentReplay = false
			break
		}
	}
	if idempotentReplay {
		if accepted.TriggerKind == responseFirstClarificationReplyTriggerKind {
			if _, found, lookupErr := r.store.LoadClarificationReplyContinuationOutcome(ctx, accepted.TurnID); lookupErr != nil {
				return viewer.V2AcceptedTurn{}, fmt.Errorf("read response-first clarification reply result receipt: %w", lookupErr)
			} else if found {
				idempotentReplay = false
			}
		}
	}
	if idempotentReplay {
		if _, found, lookupErr := r.store.LoadCommittedOutcome(ctx, accepted.TurnID); lookupErr != nil {
			return viewer.V2AcceptedTurn{}, fmt.Errorf("read response-first result receipt: %w", lookupErr)
		} else if found {
			idempotentReplay = false
		}
	}
	return viewer.V2AcceptedTurn{
		SessionID: accepted.SessionID, TurnID: accepted.TurnID, TraceID: accepted.InputEvent.TraceID,
		InputMessageID: accepted.InputMessageID, ClientSubmissionID: accepted.ClientSubmissionID,
		ActorID: accepted.ActorID, IdempotentReplay: idempotentReplay,
	}, nil
}

func markResponseFirstTurnWorkTrackingIncomplete(ctx context.Context) {
	if turn, found := turntiming.BatchTurnFromContext(ctx); found {
		turn.MarkWorkTrackingIncomplete()
	}
}

// ValidateExistingSession permits a session already present in the legacy
// session repository only after the canonical journal proves it is v2. A
// legacy JSON session is never promoted from this ingress path.
func (r *responseFirstConversationRuntime) ValidateExistingSession(ctx context.Context, sessionID modulecore.SessionID) error {
	if r == nil || r.store == nil || ctx == nil {
		return errors.New("response-first conversation runtime is unavailable")
	}
	state, found, err := r.store.ReadSessionState(ctx, sessionID, r.ownerID)
	if err != nil {
		return fmt.Errorf("read response-first session state: %w", err)
	}
	if found {
		if state.RuntimeVersion == conversationruntime.ConversationRuntimeV2 {
			return nil
		}
		return errors.New("response-first v2 refuses an existing legacy session; verified idle cutover is unavailable")
	}
	if r.reservations == nil {
		return errors.New("response-first v2 reservation owner is unavailable")
	}
	reserved, err := r.reservations.IsCanonicalV2Reserved(ctx, string(sessionID), r.ownerID)
	if err != nil || !reserved {
		return errors.New("response-first v2 refuses an existing legacy session; verified idle cutover is unavailable")
	}
	return nil
}

func stableViewerAcceptOperationID(ownerID string, sessionID modulecore.SessionID, submissionID string) string {
	digest := sha256.Sum256([]byte(ownerID + "\x00" + string(sessionID) + "\x00" + submissionID))
	return "viewer.accept:" + hex.EncodeToString(digest[:])
}

func (r *responseFirstConversationRuntime) Process(ctx context.Context, accepted viewer.V2AcceptedTurn) error {
	if r == nil || r.store == nil || r.runtime == nil || ctx == nil {
		return errors.New("response-first conversation runtime is unavailable")
	}
	clarificationReply, replyFound, err := r.store.LoadClarificationReplyContinuationOutcome(ctx, accepted.TurnID)
	if err != nil {
		return fmt.Errorf("load committed clarification reply continuation: %w", err)
	}
	if replyFound {
		bundle := conversationruntime.CommittedBundle{
			OperationID: clarificationReply.OperationID, SessionID: clarificationReply.SessionID,
			TurnID: clarificationReply.ReplyTurnID, ResultState: clarificationReply.ResultState,
			Events: append([]modulecore.EventEnvelope(nil), clarificationReply.Events...),
		}
		if err := r.publisher.PublishCommitted(ctx, bundle); err != nil {
			return err
		}
		r.signalReadbackDrain()
		return nil
	}
	pending, err := r.store.ReadPendingAcceptedTurns(ctx, accepted.SessionID, r.ownerID)
	if err != nil {
		return fmt.Errorf("read response-first turn for processing: %w", err)
	}
	for _, item := range pending {
		if item.TurnID != accepted.TurnID {
			continue
		}
		if item.State != "accepted" {
			if (item.TriggerKind != "clarification_control" && item.TriggerKind != responseFirstClarificationReplyTriggerKind) || item.State != "awaiting_readback" {
				return nil
			}
			if item.TriggerKind == responseFirstClarificationReplyTriggerKind {
				return errors.New("clarification reply is awaiting readback without a committed continuation outcome")
			}
			break
		}
		state, found, err := r.store.ReadSessionState(ctx, item.SessionID, r.ownerID)
		if err != nil || !found {
			if err == nil {
				err = errors.New("accepted response-first turn has no session state")
			}
			return fmt.Errorf("read response-first session state: %w", err)
		}
		legacy, err := r.store.ReadLegacySessionProjection(ctx, item.SessionID, r.ownerID, state.CutoverSeq)
		if err != nil {
			return fmt.Errorf("read response-first legacy projection: %w", err)
		}
		result := r.runtime.RunAcceptedTurn(ctx, conversationruntime.AcceptedTurnInput{ReadViewRequest: conversationruntime.ReadViewRequest{
			Principal: conversationruntime.Principal{OwnerID: r.ownerID, ActorID: item.ActorID},
			Session: conversationruntime.SessionState{
				SessionID: state.SessionID, OwnerID: state.OwnerID, RuntimeVersion: state.RuntimeVersion,
				CutoverSeq: state.CutoverSeq, AdmissionSeq: state.AdmissionSeq,
				ProjectionWatermark: state.ProjectionWatermark, GenerationEpoch: state.GenerationEpoch,
			},
			Accepted: toConversationAcceptedTurn(item), Legacy: toConversationLegacyProjection(legacy),
		}})
		if result.Status == conversationruntime.TurnStatusDelivered {
			r.signalReadbackDrain()
			return nil
		}
		if result.Cause != nil {
			return result.Cause
		}
		return fmt.Errorf("response-first processing ended in state %q", result.Status)
	}

	clarification, clarificationFound, err := r.store.LoadClarificationDecisionOutcome(ctx, accepted.TurnID)
	if err != nil {
		return fmt.Errorf("load committed clarification decision: %w", err)
	}
	if clarificationFound {
		bundle := conversationruntime.CommittedBundle{
			OperationID: clarification.OperationID, SessionID: clarification.SessionID,
			TurnID: clarification.ControlTurnID, ResultState: clarification.ResultState,
			Events: append([]modulecore.EventEnvelope(nil), clarification.Events...),
		}
		if err := r.publisher.PublishCommitted(ctx, bundle); err != nil {
			return err
		}
		r.signalReadbackDrain()
		return nil
	}

	batch, found, err := r.store.LoadCommittedOutcome(ctx, accepted.TurnID)
	if err != nil {
		return fmt.Errorf("load committed response-first result: %w", err)
	}
	if !found {
		return errors.New("response-first turn is neither accepted nor committed")
	}
	if err := r.publisher.PublishCommitted(ctx, toConversationCommittedBundle(batch)); err != nil {
		return err
	}
	r.signalReadbackDrain()
	return nil
}

func (r *responseFirstConversationRuntime) signalReadbackDrain() {
	if r == nil || r.dispatchWake == nil {
		return
	}
	select {
	case r.dispatchWake <- struct{}{}:
	default:
	}
}

// ResumePendingAcceptedTurns is the per-session restart entry point. A startup
// coordinator can call it once it has the session ID; no session enumerator is
// currently available in the canonical EventStore contract.
func (r *responseFirstConversationRuntime) ResumePendingAcceptedTurns(ctx context.Context, sessionID modulecore.SessionID) error {
	if r == nil || r.store == nil || ctx == nil {
		return errors.New("response-first recovery runtime is unavailable")
	}
	pending, err := r.store.ReadPendingAcceptedTurns(ctx, sessionID, r.ownerID)
	if err != nil {
		return fmt.Errorf("read pending response-first turns: %w", err)
	}
	for _, item := range pending {
		if err := ctx.Err(); err != nil {
			return err
		}
		if item.State != "accepted" && !((item.TriggerKind == "clarification_control" || item.TriggerKind == responseFirstClarificationReplyTriggerKind) && item.State == "awaiting_readback") {
			continue
		}
		if err := r.Process(ctx, viewer.V2AcceptedTurn{
			SessionID: item.SessionID, TurnID: item.TurnID, TraceID: item.InputEvent.TraceID,
			InputMessageID: item.InputMessageID, ClientSubmissionID: item.ClientSubmissionID, ActorID: item.ActorID,
		}); err != nil {
			return fmt.Errorf("resume accepted response-first turn %s: %w", item.TurnID, err)
		}
	}
	return nil
}

type responseFirstCanonicalReader struct{ store responseFirstJournalStore }

func (r responseFirstCanonicalReader) ReadCompleteSessionTail(ctx context.Context, sessionID modulecore.SessionID, after modulecore.EventSeq) (conversationruntime.CanonicalTail, error) {
	tail, err := r.store.ReadCompleteSessionTail(ctx, sessionID, after)
	if err != nil {
		return conversationruntime.CanonicalTail{}, err
	}
	result := conversationruntime.CanonicalTail{ThroughSeq: tail.ThroughSeq, Complete: tail.Complete, Messages: make([]conversationruntime.CanonicalMessage, 0, len(tail.Messages))}
	for _, message := range tail.Messages {
		result.Messages = append(result.Messages, conversationruntime.CanonicalMessage{
			Event: message.Event, Index: toConversationMessageIndex(message.Index),
		})
	}
	return result, nil
}

func (r responseFirstCanonicalReader) LoadClarificationControlContext(ctx context.Context, request conversationruntime.ClarificationControlContextRequest) (conversationruntime.StoredClarificationControlContext, error) {
	stored, err := r.store.LoadClarificationControlContext(ctx, eventstore.ClarificationControlContextInput{
		SessionID: request.SessionID, ControlTurnID: request.ControlTurnID,
		OwnerID: request.OwnerID, Assignee: request.Assignee,
	})
	if err != nil {
		return conversationruntime.StoredClarificationControlContext{}, err
	}
	contextMessages := make([]conversationruntime.DelegationDispatchMessage, 0, len(stored.Context))
	for _, message := range stored.Context {
		contextMessages = append(contextMessages, conversationruntime.DelegationDispatchMessage{
			MessageID: message.MessageID, Role: message.Role, Text: message.Text,
		})
	}
	return conversationruntime.StoredClarificationControlContext{
		ControlTurn: toConversationAcceptedTurn(stored.ControlTurn), OwnerID: stored.OwnerID,
		OriginUserActorID: stored.OriginUserActorID, Handoff: stored.Handoff, QuestionEvent: stored.QuestionEvent,
		QuestionMessage: toConversationMessageIndex(stored.QuestionMessage), Context: contextMessages,
	}, nil
}

type responseFirstTurnJournal struct {
	store              responseFirstJournalStore
	projectionChildren *responseFirstProjectionChildren
}

func (j responseFirstTurnJournal) CommitTurnBundle(ctx context.Context, bundle conversationruntime.CommitBundle) (conversationruntime.CommittedBundle, error) {
	projectionWorkIDs := make([]string, 0, len(bundle.Outbox))
	input := eventstore.CommitInput{
		OperationID: bundle.OperationID, SessionID: bundle.SessionID, TurnID: bundle.TurnID,
		GenerationEpoch: bundle.GenerationEpoch, ResultState: bundle.ResultState, Events: bundle.Events,
		MessageIndexes: make([]eventstore.MessageIndex, 0, len(bundle.MessageIndexes)),
		Outbox:         make([]eventstore.WorkItem, 0, len(bundle.Outbox)),
	}
	for _, index := range bundle.MessageIndexes {
		input.MessageIndexes = append(input.MessageIndexes, toEventstoreMessageIndex(index))
	}
	for _, item := range bundle.Outbox {
		if item.Kind == responseFirstProjectionWorkKind {
			j.projectionChildren.registerBeforeCommit(ctx, item.WorkID)
			projectionWorkIDs = append(projectionWorkIDs, item.WorkID)
		}
		input.Outbox = append(input.Outbox, eventstore.WorkItem{
			WorkID: item.WorkID, DedupeKey: item.DedupeKey, Kind: item.Kind,
			SourceEventID: item.SourceEventID, SessionID: item.SessionID, Priority: item.Priority, NotBefore: item.NotBefore,
		})
	}
	if bundle.Delegation != nil {
		input.Delegation = &eventstore.DelegationRecord{
			DelegationID: bundle.Delegation.DelegationID, Revision: bundle.Delegation.Revision,
			OriginTurnID: bundle.Delegation.OriginTurnID, Assignee: bundle.Delegation.Assignee,
			InstructionMessageID: bundle.Delegation.InstructionMessageID, ContextEventID: bundle.Delegation.ContextEventID,
			State: bundle.Delegation.State, WorkTaskID: bundle.Delegation.WorkTaskID, ReportRevision: bundle.Delegation.ReportRevision,
		}
	}
	committed, err := j.store.CommitTurnBundle(ctx, input)
	if err != nil {
		j.abandonProjectionChildrenWithoutDurableWork(ctx, bundle.TurnID, projectionWorkIDs)
		if errors.Is(err, eventstore.ErrReportConflict) {
			return conversationruntime.CommittedBundle{}, fmt.Errorf("%w: %w", conversationruntime.ErrStaleReport, err)
		}
		return conversationruntime.CommittedBundle{}, err
	}
	return toConversationCommittedBundle(committed), nil
}

func (j responseFirstTurnJournal) LoadCommittedOutcome(ctx context.Context, turnID modulecore.TurnID) (conversationruntime.CommittedBundle, bool, error) {
	batch, found, err := j.store.LoadCommittedOutcome(ctx, turnID)
	return toConversationCommittedBundle(batch), found, err
}

func (j responseFirstTurnJournal) CommitClarificationDecision(ctx context.Context, input conversationruntime.ClarificationDecisionCommitInput) (conversationruntime.ClarificationDecisionCommitResult, error) {
	projectionWorkID := eventstore.ConversationProjectionWorkID(input.EventID)
	j.projectionChildren.registerBeforeCommit(ctx, projectionWorkID)
	committed, err := j.store.CommitClarificationDecision(ctx, eventstore.ClarificationDecisionInput{
		OperationID: input.OperationID, SessionID: input.SessionID, ControlTurnID: input.ControlTurnID,
		DelegationID: input.DelegationID, Revision: input.Revision, GenerationEpoch: input.GenerationEpoch,
		Decision: input.Decision, InvocationReceipt: input.InvocationReceipt,
		EventID: input.EventID, MessageID: input.MessageID, ContextEventID: input.ContextEventID,
		ReceiptEventID: input.ReceiptEventID, OccurredAt: input.OccurredAt,
	})
	if err != nil {
		j.abandonClarificationProjectionChildWithoutDurableWork(ctx, input.ControlTurnID, projectionWorkID)
		return conversationruntime.ClarificationDecisionCommitResult{}, err
	}
	return toConversationClarificationDecisionCommitResult(committed), nil
}

func (j responseFirstTurnJournal) CommitClarificationTerminal(ctx context.Context, input conversationruntime.ClarificationTerminalCommitInput) (conversationruntime.ClarificationDecisionCommitResult, error) {
	projectionWorkID := eventstore.ConversationProjectionWorkID(input.EventID)
	j.projectionChildren.registerBeforeCommit(ctx, projectionWorkID)
	committed, err := j.store.CommitClarificationTerminal(ctx, eventstore.ClarificationTerminalInput{
		OperationID: input.OperationID, SessionID: input.SessionID, ControlTurnID: input.ControlTurnID,
		DelegationID: input.DelegationID, Revision: input.Revision, GenerationEpoch: input.GenerationEpoch,
		Reason: input.Reason, AttemptedKind: input.AttemptedKind, InvocationReceipt: input.InvocationReceipt,
		EventID: input.EventID, MessageID: input.MessageID, ReceiptEventID: input.ReceiptEventID,
		OccurredAt: input.OccurredAt,
	})
	if err != nil {
		j.abandonClarificationProjectionChildWithoutDurableWork(ctx, input.ControlTurnID, projectionWorkID)
		return conversationruntime.ClarificationDecisionCommitResult{}, err
	}
	return toConversationClarificationDecisionCommitResult(committed), nil
}

func (j responseFirstTurnJournal) LoadClarificationDecisionOutcome(ctx context.Context, controlTurnID modulecore.TurnID) (conversationruntime.ClarificationDecisionCommitResult, bool, error) {
	committed, found, err := j.store.LoadClarificationDecisionOutcome(ctx, controlTurnID)
	return toConversationClarificationDecisionCommitResult(committed), found, err
}

func (j responseFirstTurnJournal) LoadClarificationReplyContext(ctx context.Context, request conversationruntime.ClarificationReplyContextRequest) (conversationruntime.StoredClarificationReplyContext, error) {
	stored, err := j.store.LoadClarificationReplyContext(ctx, eventstore.ClarificationReplyContextInput{
		SessionID: request.SessionID, ReplyTurnID: request.ReplyTurnID, OwnerID: request.OwnerID,
	})
	if err != nil {
		return conversationruntime.StoredClarificationReplyContext{}, err
	}
	contextMessages := make([]conversationruntime.DelegationDispatchMessage, 0, len(stored.Context))
	for _, message := range stored.Context {
		contextMessages = append(contextMessages, conversationruntime.DelegationDispatchMessage{
			MessageID: message.MessageID, Role: message.Role, Text: message.Text,
		})
	}
	return conversationruntime.StoredClarificationReplyContext{
		Reply: toConversationAcceptedTurn(stored.Reply), OwnerID: stored.Receipt.OwnerID,
		Receipt: conversationruntime.ClarificationReplyReceipt{
			PromptMessageID: stored.Receipt.PromptMessageID, SourceTurnID: stored.Receipt.SourceTurnID,
			DelegationID: stored.Receipt.DelegationID, Revision: stored.Receipt.Revision,
		},
		AnswerEvent: stored.AnswerEvent, AnswerMessage: toConversationMessageIndex(stored.AnswerMessage),
		PromptEvent: stored.PromptEvent, PromptMessage: toConversationMessageIndex(stored.PromptMessage),
		ControlTurn: toConversationAcceptedTurn(stored.ControlTurn), QuestionEvent: stored.QuestionEvent,
		QuestionMessage: toConversationMessageIndex(stored.QuestionMessage), Handoff: stored.Handoff,
		OriginTurnID: stored.OriginTurnID, Context: contextMessages, AnswerText: stored.AnswerText,
	}, nil
}

func (j responseFirstTurnJournal) LoadClarificationReplyContinuationOutcome(ctx context.Context, replyTurnID modulecore.TurnID) (conversationruntime.ClarificationReplyContinuationResult, bool, error) {
	committed, found, err := j.store.LoadClarificationReplyContinuationOutcome(ctx, replyTurnID)
	return toConversationClarificationReplyContinuationResult(committed), found, err
}

func (j responseFirstTurnJournal) CommitClarificationReplyContinuation(ctx context.Context, input conversationruntime.ClarificationReplyContinuationInput) (conversationruntime.ClarificationReplyContinuationResult, error) {
	committed, err := j.store.CommitClarificationReplyContinuation(ctx, eventstore.ClarificationReplyContinuationInput{
		OperationID: input.OperationID, OwnerID: input.OwnerID, SessionID: input.SessionID,
		ReplyTurnID: input.ReplyTurnID, PromptMessageID: input.PromptMessageID,
		DelegationID: input.DelegationID, Revision: input.Revision, GenerationEpoch: input.GenerationEpoch,
		Instruction: input.Instruction, InvocationReceipt: input.InvocationReceipt, EventID: input.EventID,
		MessageID: input.MessageID, ContextEventID: input.ContextEventID, ReceiptEventID: input.ReceiptEventID,
		OccurredAt: input.OccurredAt,
	})
	if err != nil {
		return conversationruntime.ClarificationReplyContinuationResult{}, err
	}
	return toConversationClarificationReplyContinuationResult(committed), nil
}

func toConversationAcceptedTurn(turn eventstore.AcceptedTurn) conversationruntime.AcceptedTurn {
	return conversationruntime.AcceptedTurn{
		SessionID: turn.SessionID, TurnID: turn.TurnID, ActorID: turn.ActorID,
		TriggerKind:        turn.TriggerKind,
		ClientSubmissionID: turn.ClientSubmissionID, InputMessageID: turn.InputMessageID,
		AdmissionSeq: turn.AdmissionSeq, GenerationEpoch: turn.GenerationEpoch, State: turn.State, InputEvent: turn.InputEvent,
	}
}

func toConversationLegacyProjection(projection eventstore.LegacyProjection) conversationruntime.LegacyProjection {
	result := conversationruntime.LegacyProjection{
		AppliedThrough: projection.AppliedThrough, Complete: projection.Complete,
		Messages: make([]conversationruntime.LegacyMessage, 0, len(projection.Messages)),
		Gaps:     make([]conversationruntime.LegacyProjectionGap, 0, len(projection.Gaps)),
	}
	for _, message := range projection.Messages {
		result.Messages = append(result.Messages, conversationruntime.LegacyMessage{
			Index: toConversationMessageIndex(message.Index), EventSeq: message.EventSeq, Text: message.Text,
			TurnOrder: message.TurnOrder, MessageOrder: message.MessageOrder,
		})
	}
	for _, gap := range projection.Gaps {
		result.Gaps = append(result.Gaps, conversationruntime.LegacyProjectionGap{EventSeq: gap.EventSeq, EventID: gap.EventID, Reason: gap.Reason})
	}
	return result
}

func toConversationMessageIndex(index eventstore.MessageIndex) conversationruntime.MessageIndex {
	return conversationruntime.MessageIndex{
		MessageID: index.MessageID, EventID: index.EventID, SessionID: index.SessionID, TurnID: index.TurnID,
		FromActor: index.FromActor, ToKind: index.ToKind, ToActor: index.ToActor, Kind: index.Kind,
		ReplyToMessageID: index.ReplyToMessageID, Visibility: index.Visibility,
	}
}

func toEventstoreMessageIndex(index conversationruntime.MessageIndex) eventstore.MessageIndex {
	return eventstore.MessageIndex{
		MessageID: index.MessageID, EventID: index.EventID, SessionID: index.SessionID, TurnID: index.TurnID,
		FromActor: index.FromActor, ToKind: index.ToKind, ToActor: index.ToActor, Kind: index.Kind,
		ReplyToMessageID: index.ReplyToMessageID, Visibility: index.Visibility,
	}
}

func toConversationCommittedBundle(batch eventstore.CommittedBatch) conversationruntime.CommittedBundle {
	return conversationruntime.CommittedBundle{
		OperationID: batch.OperationID, SessionID: batch.SessionID, TurnID: batch.TurnID,
		ResultState: batch.ResultState, Events: batch.Events,
	}
}

func toConversationClarificationDecisionCommitResult(committed eventstore.ClarificationDecisionCommit) conversationruntime.ClarificationDecisionCommitResult {
	indexes := make([]conversationruntime.MessageIndex, 0, len(committed.MessageIndexes))
	for _, index := range committed.MessageIndexes {
		indexes = append(indexes, toConversationMessageIndex(index))
	}
	return conversationruntime.ClarificationDecisionCommitResult{
		OperationID: committed.OperationID, ControlTurnID: committed.ControlTurnID,
		DelegationID: committed.DelegationID, Revision: committed.Revision, Kind: committed.Kind,
		TerminalReason: committed.TerminalReason, ResultState: committed.ResultState, InvocationReceipt: committed.InvocationReceipt,
		Event: committed.Event, ReceiptEvent: committed.ReceiptEvent,
		Message: toConversationMessageIndex(committed.Message), NextHandoff: committed.NextHandoff,
		DispatchQueued: committed.DispatchQueued,
		Events:         append([]modulecore.EventEnvelope(nil), committed.Events...), MessageIndexes: indexes,
	}
}

func toConversationClarificationReplyContinuationResult(committed eventstore.ClarificationReplyContinuationCommit) conversationruntime.ClarificationReplyContinuationResult {
	indexes := make([]conversationruntime.MessageIndex, 0, len(committed.MessageIndexes))
	for _, index := range committed.MessageIndexes {
		indexes = append(indexes, toConversationMessageIndex(index))
	}
	return conversationruntime.ClarificationReplyContinuationResult{
		OperationID: committed.OperationID, OwnerID: committed.OwnerID, SessionID: committed.SessionID,
		ReplyTurnID: committed.ReplyTurnID, PromptMessageID: committed.PromptMessageID,
		DelegationID: committed.DelegationID, Revision: committed.Revision, NextRevision: committed.NextRevision,
		ResultState: committed.ResultState, InvocationReceipt: committed.InvocationReceipt,
		ContextEvent: committed.ContextEvent, Event: committed.Event, ReceiptEvent: committed.ReceiptEvent,
		Message: toConversationMessageIndex(committed.Message), Events: append([]modulecore.EventEnvelope(nil), committed.Events...),
		MessageIndexes: indexes, NextHandoff: committed.NextHandoff, DispatchQueued: committed.DispatchQueued,
	}
}

func (j responseFirstTurnJournal) abandonProjectionChildrenWithoutDurableWork(ctx context.Context, turnID modulecore.TurnID, workIDs []string) {
	if ctx == nil || j.store == nil || j.projectionChildren == nil || len(workIDs) == 0 {
		return
	}
	batch, found, err := j.store.LoadCommittedOutcome(context.WithoutCancel(ctx), turnID)
	if err != nil {
		return
	}
	committed := toConversationCommittedBundle(batch)
	for _, workID := range workIDs {
		durable := false
		if found {
			for _, event := range committed.Events {
				if responseFirstPublicProjectionOutput(event) && responseFirstProjectionWorkID(committed, event) == workID {
					durable = true
					break
				}
			}
		}
		if !durable {
			j.projectionChildren.abandonUncommitted(workID)
		}
	}
}

func (j responseFirstTurnJournal) abandonClarificationProjectionChildWithoutDurableWork(ctx context.Context, controlTurnID modulecore.TurnID, workID string) {
	if ctx == nil || j.store == nil || j.projectionChildren == nil || strings.TrimSpace(workID) == "" {
		return
	}
	committed, found, err := j.store.LoadClarificationDecisionOutcome(context.WithoutCancel(ctx), controlTurnID)
	if err != nil {
		return
	}
	durable := found && committed.Message.Visibility == "public" && committed.Message.ToKind == "user" &&
		eventstore.ConversationProjectionWorkID(committed.Event.EventID) == workID
	if !durable {
		j.projectionChildren.abandonUncommitted(workID)
	}
}
