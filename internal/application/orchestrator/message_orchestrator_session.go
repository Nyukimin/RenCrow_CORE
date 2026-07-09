package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

type messageSessionLifecycle struct {
	sessionRepo SessionRepository
}

func newMessageSessionLifecycle(sessionRepo SessionRepository) *messageSessionLifecycle {
	return &messageSessionLifecycle{sessionRepo: sessionRepo}
}

func (l *messageSessionLifecycle) LoadForRequest(ctx context.Context, req ProcessMessageRequest) (*session.Session, error) {
	sess, err := l.loadOrCreate(ctx, req.SessionID, req.Channel, req.ChatID)
	if err != nil {
		log.Printf("[MessageOrch] ProcessMessage ERROR: failed to load or create session: %v", err)
		return nil, fmt.Errorf("failed to load or create session: %w", err)
	}
	log.Printf("[MessageOrch] Session loaded/created: %s", sess.ID())
	return sess, nil
}

func (l *messageSessionLifecycle) SaveCompletedTask(ctx context.Context, sess *session.Session, t task.Task) error {
	sess.AddTask(t)
	if err := l.sessionRepo.Save(ctx, sess); err != nil {
		log.Printf("[MessageOrch] ProcessMessage ERROR: failed to save session: %v", err)
		return fmt.Errorf("failed to save session: %w", err)
	}
	return nil
}

func (l *messageSessionLifecycle) loadOrCreate(ctx context.Context, id, channel, chatID string) (*session.Session, error) {
	sess, err := l.sessionRepo.Load(ctx, id)
	if err != nil {
		if errors.Is(err, session.ErrSessionNotFound) {
			return session.NewSession(id, channel, chatID), nil
		}
		return nil, err
	}
	return sess, nil
}
