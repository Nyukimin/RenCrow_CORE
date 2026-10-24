package main

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	mioDeltaPublicationQueueCapacity = 64
	mioDeltaPublicationMaxBytes      = 16 * 1024
)

var (
	errMioDeltaPublicationQueueFull = errors.New("Mio response delta publication queue is full")
	errMioDeltaPublicationTooLarge  = errors.New("Mio response delta exceeds the in-memory publication limit")
)

// mioDeltaPublicationQueue separates guarded admission from guarded fanout.
// Admit is used inside prepared-turn and Session binding guards and only does
// a bounded channel send. The worker prepares event bytes outside both guards
// and transfers a retained qualification to EventHub for writer-time claim.
type mioDeltaPublicationQueue struct {
	ownerID string
	hub     *viewer.EventHub
	deltas  chan *mioDeltaPublication
	slots   chan struct{}
	worker  sync.WaitGroup
	mu      sync.Mutex
	err     error
	closed  bool
}

type mioDeltaPublication struct {
	delta         orchestrator.MioAnswerDelta
	ctx           context.Context
	qualification domainconversation.PreparedChatTurnPublicationGuard
	ready         chan struct{}
	mu            sync.Mutex
	finished      bool
	publish       bool
}

func (p *mioDeltaPublication) Finish(publish bool) {
	if p == nil {
		return
	}
	p.mu.Lock()
	if !p.finished {
		p.finished = true
		p.publish = publish
		close(p.ready)
	}
	p.mu.Unlock()
}

func newMioDeltaPublicationQueue(ownerID string, hub *viewer.EventHub) *mioDeltaPublicationQueue {
	queue := &mioDeltaPublicationQueue{
		ownerID: ownerID,
		hub:     hub,
		deltas:  make(chan *mioDeltaPublication, mioDeltaPublicationQueueCapacity),
		slots:   make(chan struct{}, mioDeltaPublicationQueueCapacity),
	}
	queue.worker.Add(1)
	go queue.run()
	return queue
}

func (q *mioDeltaPublicationQueue) Admit(ctx context.Context, delta orchestrator.MioAnswerDelta, qualification domainconversation.PreparedChatTurnPublicationGuard) (func(bool), error) {
	if q == nil || q.hub == nil || q.ownerID == "" {
		return nil, errors.New("Mio response delta publication is unavailable")
	}
	if ctx == nil {
		return nil, errors.New("Mio response delta publication context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if delta.Text == "" || len(delta.Text) > mioDeltaPublicationMaxBytes {
		return nil, errMioDeltaPublicationTooLarge
	}
	if qualification == nil {
		return nil, errors.New("Mio response delta publication qualification is unavailable")
	}
	publication := &mioDeltaPublication{delta: delta, ctx: ctx, qualification: qualification, ready: make(chan struct{})}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return nil, errors.New("Mio response delta publication queue is closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case q.slots <- struct{}{}:
	default:
		return nil, errMioDeltaPublicationQueueFull
	}
	select {
	case q.deltas <- publication:
		return publication.Finish, nil
	default:
		<-q.slots
		return nil, errMioDeltaPublicationQueueFull
	}
}

func (q *mioDeltaPublicationQueue) run() {
	defer q.worker.Done()
	for publication := range q.deltas {
		func() {
			defer func() { <-q.slots }()
			<-publication.ready
			publication.mu.Lock()
			publish := publication.publish
			delta := publication.delta
			qualification := publication.qualification
			publication.mu.Unlock()
			if !publish {
				qualification.Release()
				return
			}
			if err := publication.ctx.Err(); err != nil {
				qualification.Release()
				return
			}
			event := orchestrator.NewEventWithTraceID(
				modulecore.TraceID(delta.TraceID), "agent.response_delta", "mio", "user", delta.Text,
				"CHAT", delta.TaskID, delta.SessionID, "viewer", "cmd:"+q.ownerID,
			)
			event.TurnID = modulecore.TurnID(delta.TurnID)
			event.MessageID = modulecore.MessageID(delta.MessageID)
			event.RunID = modulecore.RunID(delta.RunID)
			event.ActorKind = "agent"
			event.ActorID = "mio"
			prepared, err := q.hub.PreparePrivateMioDelta(q.ownerID, event)
			if err != nil {
				qualification.Release()
				q.mu.Lock()
				if q.err == nil {
					q.err = fmt.Errorf("prepare private Mio response delta: %w", err)
				}
				q.mu.Unlock()
				return
			}
			if err := q.hub.PublishPreparedPrivateMioDelta(q.ownerID, prepared, qualification); err != nil {
				q.mu.Lock()
				if q.err == nil {
					q.err = fmt.Errorf("publish private Mio response delta: %w", err)
				}
				q.mu.Unlock()
			}
		}()
	}
}

// CloseAndWait drains already-admitted deltas before returning. Consequently,
// a provider error or cancellation does not erase any prefix admitted earlier.
func (q *mioDeltaPublicationQueue) CloseAndWait() error {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	if !q.closed {
		q.closed = true
		close(q.deltas)
	}
	q.mu.Unlock()
	q.worker.Wait()
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.err
}
