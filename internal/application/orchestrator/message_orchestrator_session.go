package orchestrator

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

type canonicalSessionRepository interface {
	LoadOrCreateCanonical(context.Context, string, conversation.ChannelAddress, time.Time) (*session.Session, error)
}

type messageSessionLifecycle struct {
	sessionRepo SessionRepository
}

func newMessageSessionLifecycle(sessionRepo SessionRepository) *messageSessionLifecycle {
	return &messageSessionLifecycle{sessionRepo: sessionRepo}
}

func (l *messageSessionLifecycle) LoadForRequest(ctx context.Context, req ProcessMessageRequest) (*session.Session, error) {
	sess, err := l.load(ctx, req.SessionID)
	if err != nil {
		log.Printf("[MessageOrch] ProcessMessage ERROR: failed to load session: %v", err)
		return nil, fmt.Errorf("failed to load session: %w", err)
	}
	log.Printf("[MessageOrch] Session loaded: %s", sess.ID())
	return sess, nil
}

func (l *messageSessionLifecycle) ResolveForRequest(ctx context.Context, req ProcessMessageRequest, now time.Time) (*session.Session, ProcessMessageRequest, error) {
	if req.SessionID != "" {
		sess, err := l.LoadForRequest(ctx, req)
		if err != nil {
			return nil, req, err
		}
		req.SessionID = sess.ID()
		return sess, req, nil
	}
	address, err := conversation.NewChannelAddress(req.Channel, req.ChatID)
	if err != nil {
		return nil, req, fmt.Errorf("invalid ChannelAddress: %w", err)
	}
	repo, ok := l.sessionRepo.(canonicalSessionRepository)
	if !ok {
		return nil, req, fmt.Errorf("canonical session repository is unavailable")
	}
	sess, err := repo.LoadOrCreateCanonical(ctx, now.UTC().Format("2006-01-02"), address, now.UTC())
	if err != nil {
		return nil, req, fmt.Errorf("resolve canonical session: %w", err)
	}
	req.SessionID = sess.ID()
	return sess, req, nil
}

func (l *messageSessionLifecycle) SaveCompletedTask(ctx context.Context, sess *session.Session, t task.Task) error {
	sess.AddTask(t)
	if err := l.sessionRepo.Save(ctx, sess); err != nil {
		log.Printf("[MessageOrch] ProcessMessage ERROR: failed to save session: %v", err)
		return fmt.Errorf("failed to save session: %w", err)
	}
	return nil
}

func (l *messageSessionLifecycle) load(ctx context.Context, id string) (*session.Session, error) {
	return l.sessionRepo.Load(ctx, id)
}
