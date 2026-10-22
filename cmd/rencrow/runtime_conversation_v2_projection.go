package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	responseFirstProjectionWorkKind      = "conversation_projection"
	responseFirstProjectionLeaseDuration = 2 * time.Minute
	responseFirstProjectionRetryDelay    = time.Second
	responseFirstProjectionChildCapacity = 4096
)

type responseFirstProjectionPersistence interface {
	ClaimWork(context.Context, eventstore.WorkClaimInput) ([]eventstore.LeasedWorkItem, error)
	ListUnmarkedClarificationReplyProjections(context.Context, string, int) ([]eventstore.PendingClarificationReplyProjection, error)
	RetryUnmarkedClarificationReplyProjection(context.Context, string, string, modulecore.EventID, time.Time, time.Duration) error
	MarkConversationProjectionDeliveryAttempt(context.Context, eventstore.ConversationProjectionDeliveryAttemptInput) error
	LoadLeasedConversationProjection(context.Context, eventstore.LoadConversationProjectionInput) (eventstore.LeasedConversationProjection, error)
	CompleteConversationProjection(context.Context, eventstore.CompleteConversationProjectionInput) error
	RetryWork(context.Context, eventstore.WorkRetryInput) error
}

type responseFirstCanonicalProjectionL1 interface {
	ApplyCanonicalConversationProjection(context.Context, []domainconversation.CanonicalConversationProjection) (domainconversation.ConversationProjectionApplyReceipt, error)
}

type responseFirstProjectionPublisher struct {
	store   responseFirstProjectionPersistence
	ownerID string
	next    responseFirstEventPublisher
	wake    chan<- struct{}
}

func (p *responseFirstProjectionPublisher) PublishAcceptedInput(ctx context.Context, event modulecore.EventEnvelope) error {
	return p.next.PublishAcceptedInput(ctx, event)
}

// PublishCommitted persists the first delivery-attempt marker immediately
// before delegating publication. A failed publication still leaves a retryable
// durable projection because the public attempt has begun.
func (p *responseFirstProjectionPublisher) PublishCommitted(ctx context.Context, bundle conversationruntime.CommittedBundle) error {
	marked := false
	for _, event := range bundle.Events {
		if !responseFirstPublicProjectionOutput(event) {
			continue
		}
		workID := responseFirstProjectionWorkID(bundle, event)
		if err := p.store.MarkConversationProjectionDeliveryAttempt(ctx, eventstore.ConversationProjectionDeliveryAttemptInput{
			WorkID: workID, SourceEventID: event.EventID, OwnerID: p.ownerID, AttemptedAt: time.Now().UTC(),
		}); err != nil {
			return fmt.Errorf("mark public conversation projection delivery attempt: %w", err)
		}
		marked = true
	}
	err := p.next.PublishCommitted(ctx, bundle)
	if marked {
		signalResponseFirstDrainLane(p.wake)
	}
	return err
}

func responseFirstPublicProjectionOutput(event modulecore.EventEnvelope) bool {
	return event.ComponentID == "orchestrator" && event.EventType == "agent.response" &&
		strings.EqualFold(strings.TrimSpace(fmt.Sprint(event.Payload["visibility"])), "public") &&
		strings.EqualFold(strings.TrimSpace(fmt.Sprint(event.Payload["to"])), "user")
}

func responseFirstProjectionWorkID(bundle conversationruntime.CommittedBundle, event modulecore.EventEnvelope) string {
	if strings.HasPrefix(bundle.OperationID, "conversation.output:") && bundle.TurnID != "" {
		return "projection:" + string(bundle.TurnID)
	}
	return eventstore.ConversationProjectionWorkID(event.EventID)
}

type responseFirstProjectionChild struct {
	child        turntiming.ChildWork
	recorder     turntiming.TurnRecorder
	generation   uint64
	leaseToken   string
	leaseVersion int64
}

// responseFirstProjectionChildren is a bounded, process-local bridge from a
// foreground durable commit to its later projection owner. EventStore remains
// authoritative; entries survive claim/retry and disappear only after the
// specialized EventStore completion transaction succeeds.
type responseFirstProjectionChildren struct {
	mu       sync.Mutex
	capacity int
	entries  map[string]*responseFirstProjectionChild
}

func newResponseFirstProjectionChildren() *responseFirstProjectionChildren {
	return &responseFirstProjectionChildren{
		capacity: responseFirstProjectionChildCapacity,
		entries:  make(map[string]*responseFirstProjectionChild),
	}
}

func (r *responseFirstProjectionChildren) registerBeforeCommit(ctx context.Context, workID string) {
	if r == nil {
		return
	}
	turn, found := turntiming.BatchTurnFromContext(ctx)
	if !found {
		return
	}
	if strings.TrimSpace(workID) == "" {
		turn.MarkWorkTrackingIncomplete()
		return
	}
	r.mu.Lock()
	if _, exists := r.entries[workID]; exists {
		r.mu.Unlock()
		return
	}
	if len(r.entries) >= r.capacity {
		r.mu.Unlock()
		turn.MarkWorkTrackingIncomplete()
		return
	}
	child, ok := turn.RegisterChild()
	if !ok {
		r.mu.Unlock()
		return
	}
	r.entries[workID] = &responseFirstProjectionChild{child: child, recorder: turn}
	r.mu.Unlock()
}

func (r *responseFirstProjectionChildren) beginLease(work eventstore.LeasedWorkItem) (uint64, bool) {
	if r == nil {
		return 0, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.entries[work.WorkID]
	if entry == nil {
		return 0, false
	}
	if entry.generation == ^uint64(0) {
		entry.recorder.MarkWorkTrackingIncomplete()
		return 0, false
	}
	entry.generation++
	entry.leaseToken = work.LeaseToken
	entry.leaseVersion = work.Version
	return entry.generation, true
}

func (r *responseFirstProjectionChildren) complete(workID string, generation uint64, lease eventstore.WorkLeaseInput) bool {
	if r == nil || generation == 0 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.entries[workID]
	if entry == nil || entry.generation != generation || entry.leaseToken != lease.LeaseToken || entry.leaseVersion != lease.Version {
		return false
	}
	if !entry.child.Complete() {
		return false
	}
	delete(r.entries, workID)
	return true
}

func (r *responseFirstProjectionChildren) abandonUncommitted(workID string) bool {
	if r == nil || strings.TrimSpace(workID) == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.entries[workID]
	if entry == nil {
		return true
	}
	if entry.generation != 0 || entry.leaseToken != "" || !entry.child.Complete() {
		return false
	}
	delete(r.entries, workID)
	return true
}

// failTerminal marks diagnostic work incomplete before releasing its child.
// A terminal EventStore outcome must never masquerade as successful drain.
func (r *responseFirstProjectionChildren) failTerminal(workID string) bool {
	if r == nil || strings.TrimSpace(workID) == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.entries[workID]
	if entry == nil {
		return true
	}
	entry.recorder.MarkWorkTrackingIncomplete()
	if !entry.child.Complete() {
		return false
	}
	delete(r.entries, workID)
	return true
}

type responseFirstConversationProjection struct {
	store             responseFirstProjectionPersistence
	l1                responseFirstCanonicalProjectionL1
	ownerID           string
	children          *responseFirstProjectionChildren
	acceptedPublisher responseFirstEventPublisher
	now               func() time.Time
	wake              chan struct{}
}

func newResponseFirstConversationProjection(
	store responseFirstProjectionPersistence,
	l1 responseFirstCanonicalProjectionL1,
	ownerID string,
	children *responseFirstProjectionChildren,
	acceptedPublisher responseFirstEventPublisher,
) *responseFirstConversationProjection {
	return &responseFirstConversationProjection{
		store: store, l1: l1, ownerID: ownerID, children: children, acceptedPublisher: acceptedPublisher,
		now: time.Now, wake: make(chan struct{}, 1),
	}
}

func (p *responseFirstConversationProjection) drainOne(ctx context.Context) (bool, error) {
	if p == nil || p.store == nil || p.l1 == nil || ctx == nil {
		return false, errors.New("response-first canonical conversation projection is unavailable")
	}
	var replyDeliveryErr error
	replyDeliveryProgress := false
	if p.acceptedPublisher != nil {
		pending, err := p.store.ListUnmarkedClarificationReplyProjections(ctx, p.ownerID, 1)
		if err != nil {
			replyDeliveryErr = fmt.Errorf("list unmarked clarification reply projections: %w", err)
		} else if len(pending) > 1 {
			replyDeliveryErr = errors.New("clarification reply projection owner exceeded its bounded result limit")
		} else if len(pending) == 1 {
			item := pending[0]
			if err := validateResponseFirstClarificationReplyProjection(item, p.ownerID); err != nil {
				replyDeliveryErr = err
			} else if err := p.acceptedPublisher.PublishAcceptedInput(ctx, item.Event); err != nil {
				replyDeliveryErr = fmt.Errorf("publish accepted clarification reply before projection: %w", err)
			} else {
				replyDeliveryProgress = true
				if err := p.store.MarkConversationProjectionDeliveryAttempt(ctx, eventstore.ConversationProjectionDeliveryAttemptInput{
					WorkID: item.WorkID, SourceEventID: item.SourceEventID, OwnerID: p.ownerID, AttemptedAt: p.currentTime(),
				}); err != nil {
					replyDeliveryErr = fmt.Errorf("mark published clarification reply projection: %w", err)
				}
			}
			if replyDeliveryErr != nil {
				retryErr := p.store.RetryUnmarkedClarificationReplyProjection(
					ctx, p.ownerID, item.WorkID, item.SourceEventID, p.currentTime(), responseFirstProjectionRetryDelay,
				)
				if errors.Is(retryErr, eventstore.ErrConversationProjectionTerminalAttention) {
					log.Printf("response-first clarification reply projection needs attention work_id=%s source_event_id=%s", item.WorkID, item.SourceEventID)
					if p.children != nil && !p.children.failTerminal(item.WorkID) {
						retryErr = errors.Join(retryErr, errors.New("terminal clarification reply projection child could not be released as failed"))
					}
				}
				if retryErr != nil {
					replyDeliveryErr = errors.Join(replyDeliveryErr, fmt.Errorf("schedule clarification reply projection retry: %w", retryErr))
				}
			}
		}
	}
	now := p.currentTime()
	claimed, err := p.store.ClaimWork(ctx, eventstore.WorkClaimInput{
		Kind: responseFirstProjectionWorkKind, Limit: 1,
		LeaseDuration: responseFirstProjectionLeaseDuration, Now: now,
	})
	if err != nil {
		return replyDeliveryProgress, errors.Join(replyDeliveryErr, fmt.Errorf("claim canonical conversation projection: %w", err))
	}
	if len(claimed) == 0 {
		return replyDeliveryProgress, replyDeliveryErr
	}
	work := claimed[0]
	lease := eventstore.WorkLeaseInput{
		WorkID: work.WorkID, LeaseToken: work.LeaseToken, Version: work.Version, Now: p.currentTime(),
	}
	generation, tracked := p.children.beginLease(work)
	loaded, err := p.store.LoadLeasedConversationProjection(ctx, eventstore.LoadConversationProjectionInput{
		Lease: lease, OwnerID: p.ownerID,
	})
	if err != nil {
		return true, errors.Join(replyDeliveryErr, p.retryAfterProjectionFailure(ctx, lease, fmt.Errorf("load leased canonical conversation projection: %w", err)))
	}
	if loaded.Work.WorkID != work.WorkID || loaded.Work.LeaseToken != work.LeaseToken || loaded.Work.Version != work.Version ||
		loaded.Work.Kind != responseFirstProjectionWorkKind || loaded.Work.SourceEventID != work.SourceEventID ||
		loaded.Work.SessionID != work.SessionID || loaded.OwnerID != p.ownerID || loaded.SessionID != work.SessionID ||
		loaded.AppliedThrough <= loaded.ExpectedWatermark || len(loaded.Items) == 0 {
		return true, errors.Join(replyDeliveryErr, p.retryAfterProjectionFailure(ctx, lease, errors.New("loaded canonical conversation projection does not match its lease")))
	}
	receipt, err := p.l1.ApplyCanonicalConversationProjection(ctx, loaded.Items)
	if err != nil {
		return true, errors.Join(replyDeliveryErr, p.retryAfterProjectionFailure(ctx, lease, fmt.Errorf("apply canonical conversation projection to selected L1 owner: %w", err)))
	}
	completion := eventstore.CompleteConversationProjectionInput{
		Lease: lease, OwnerID: p.ownerID, ExpectedWatermark: loaded.ExpectedWatermark, Receipt: receipt,
	}
	if err := p.store.CompleteConversationProjection(ctx, completion); err != nil {
		if retryErr := p.store.RetryWork(ctx, eventstore.WorkRetryInput{WorkLeaseInput: lease}); retryErr != nil {
			// A conflicting retry may mean the atomic watermark+completion committed
			// but its result was lost. Replay the specialized completion, which is
			// explicitly idempotent for the exact lease and L1 receipt.
			replayErr := p.store.CompleteConversationProjection(ctx, completion)
			if replayErr == nil {
				return true, errors.Join(replyDeliveryErr, p.completeProjectionChild(work.WorkID, generation, lease, tracked))
			}
			return true, errors.Join(replyDeliveryErr,
				fmt.Errorf("complete canonical conversation projection: %w", err),
				fmt.Errorf("retry canonical conversation projection after completion failure: %w", retryErr),
				fmt.Errorf("replay canonical conversation projection completion: %w", replayErr),
			)
		}
		return true, errors.Join(replyDeliveryErr, fmt.Errorf("complete canonical conversation projection; durable retry scheduled: %w", err))
	}
	return true, errors.Join(replyDeliveryErr, p.completeProjectionChild(work.WorkID, generation, lease, tracked))
}

func (p *responseFirstConversationProjection) currentTime() time.Time {
	if p != nil && p.now != nil {
		return p.now().UTC()
	}
	return time.Now().UTC()
}

func validateResponseFirstClarificationReplyProjection(item eventstore.PendingClarificationReplyProjection, ownerID string) error {
	if strings.TrimSpace(item.WorkID) == "" || item.SourceEventID.Validate() != nil || item.SessionID.Validate() != nil ||
		item.Event.EventID != item.SourceEventID || item.Event.SessionID != item.SessionID || item.Event.EventSeq <= 0 ||
		item.Event.MessageID == "" || strings.TrimSpace(ownerID) == "" ||
		item.Event.EventType != "conversation.user_message" || item.Event.ComponentID != "orchestrator" ||
		responseFirstEventPayloadString(item.Event, "from") != "user" || responseFirstEventPayloadString(item.Event, "source_actor_id") != responseFirstActorID ||
		responseFirstEventPayloadString(item.Event, "visibility") != "public" || responseFirstEventPayloadString(item.Event, "reply_to_message_id") == "" {
		return errors.New("listed clarification reply projection is not a canonical public user answer")
	}
	if err := modulecore.ValidateEventEnvelope(item.Event); err != nil {
		return fmt.Errorf("listed clarification reply projection event is invalid: %w", err)
	}
	return nil
}

func responseFirstEventPayloadString(event modulecore.EventEnvelope, key string) string {
	value, _ := event.Payload[key].(string)
	return value
}

func (p *responseFirstConversationProjection) retryAfterProjectionFailure(ctx context.Context, lease eventstore.WorkLeaseInput, cause error) error {
	if err := p.store.RetryWork(ctx, eventstore.WorkRetryInput{WorkLeaseInput: lease}); err != nil {
		return errors.Join(cause, fmt.Errorf("schedule canonical conversation projection retry: %w", err))
	}
	return cause
}

func (p *responseFirstConversationProjection) completeProjectionChild(workID string, generation uint64, lease eventstore.WorkLeaseInput, tracked bool) error {
	if !tracked {
		return nil
	}
	if !p.children.complete(workID, generation, lease) {
		return errors.New("canonical conversation projection completed but stale RAM child completion was rejected")
	}
	return nil
}
