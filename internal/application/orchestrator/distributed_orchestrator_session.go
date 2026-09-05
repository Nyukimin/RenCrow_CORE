package orchestrator

import (
	"context"
	"log"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
)

type distributedSessionLifecycle struct {
	sessionRepo SessionRepository
}

func newDistributedSessionLifecycle(sessionRepo SessionRepository) *distributedSessionLifecycle {
	return &distributedSessionLifecycle{sessionRepo: sessionRepo}
}

func (l *distributedSessionLifecycle) LoadForRequest(ctx context.Context, req ProcessMessageRequest) (*session.Session, error) {
	sess, err := l.load(ctx, req.SessionID)
	if err != nil {
		log.Printf("[DistributedOrch] ProcessMessage ERROR: failed to load session: %v", err)
		return nil, err
	}
	log.Printf("[DistributedOrch] Session loaded: %s", sess.ID())
	return sess, nil
}

func (l *distributedSessionLifecycle) ResolveForRequest(ctx context.Context, req ProcessMessageRequest, now time.Time) (*session.Session, ProcessMessageRequest, error) {
	delegate := messageSessionLifecycle{sessionRepo: l.sessionRepo}
	return delegate.ResolveForRequest(ctx, req, now)
}

func (l *distributedSessionLifecycle) SaveCompletedTurnInput(ctx context.Context, sess *session.Session, input conversation.TurnInput) error {
	sess.AddTurnInput(input)
	if err := l.sessionRepo.Save(ctx, sess); err != nil {
		log.Printf("[DistributedOrch] ProcessMessage ERROR: failed to save session: %v", err)
		return err
	}
	return nil
}

func (l *distributedSessionLifecycle) load(ctx context.Context, id string) (*session.Session, error) {
	return l.sessionRepo.Load(ctx, id)
}
