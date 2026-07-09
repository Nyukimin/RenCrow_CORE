package orchestrator

import (
	"context"
	"log"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

type distributedSessionLifecycle struct {
	sessionRepo SessionRepository
}

func newDistributedSessionLifecycle(sessionRepo SessionRepository) *distributedSessionLifecycle {
	return &distributedSessionLifecycle{sessionRepo: sessionRepo}
}

func (l *distributedSessionLifecycle) LoadForRequest(ctx context.Context, req ProcessMessageRequest) (*session.Session, error) {
	sess, err := l.loadOrCreate(ctx, req.SessionID, req.Channel, req.ChatID)
	if err != nil {
		log.Printf("[DistributedOrch] ProcessMessage ERROR: failed to load or create session: %v", err)
		return nil, err
	}
	log.Printf("[DistributedOrch] Session loaded/created: %s", sess.ID())
	return sess, nil
}

func (l *distributedSessionLifecycle) SaveCompletedTask(ctx context.Context, sess *session.Session, t task.Task) error {
	sess.AddTask(t)
	if err := l.sessionRepo.Save(ctx, sess); err != nil {
		log.Printf("[DistributedOrch] ProcessMessage ERROR: failed to save session: %v", err)
		return err
	}
	return nil
}

func (l *distributedSessionLifecycle) loadOrCreate(ctx context.Context, id, channel, chatID string) (*session.Session, error) {
	sess, err := l.sessionRepo.Load(ctx, id)
	if err != nil {
		return session.NewSession(id, channel, chatID), nil
	}
	return sess, nil
}
