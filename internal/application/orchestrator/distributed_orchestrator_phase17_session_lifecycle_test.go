package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
)

type phase17SessionRepo struct {
	loadSession *session.Session
	loadErr     error
	saveErr     error
	saved       []*session.Session
}

func (r *phase17SessionRepo) Load(context.Context, string) (*session.Session, error) {
	if r.loadErr != nil {
		return nil, r.loadErr
	}
	if r.loadSession == nil {
		return nil, session.ErrSessionNotFound
	}
	return r.loadSession, nil
}

func (r *phase17SessionRepo) Save(_ context.Context, sess *session.Session) error {
	r.saved = append(r.saved, sess)
	return r.saveErr
}

func (r *phase17SessionRepo) Exists(context.Context, string) (bool, error) {
	return r.loadSession != nil, nil
}

func (r *phase17SessionRepo) Delete(context.Context, string) error {
	r.loadSession = nil
	return nil
}

func TestPhase17DistributedSessionLifecycleReturnsLoadErrorWithoutLegacyCreation(t *testing.T) {
	wantErr := errors.New("temporary store error")
	repo := &phase17SessionRepo{loadErr: wantErr}
	lifecycle := newDistributedSessionLifecycle(repo)

	_, err := lifecycle.LoadForRequest(context.Background(), ProcessMessageRequest{
		SessionID: "sess-1",
		Channel:   "line",
		ChatID:    "U123",
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("LoadForRequest() error=%v, want %v", err, wantErr)
	}
}

func TestPhase17DistributedSessionLifecycleReturnsExistingSession(t *testing.T) {
	existing := newCanonicalOrchestratorTestSession("line", "U123")
	repo := &phase17SessionRepo{loadSession: existing}
	lifecycle := newDistributedSessionLifecycle(repo)

	sess, err := lifecycle.LoadForRequest(context.Background(), ProcessMessageRequest{SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("expected existing session, got error %v", err)
	}
	if sess != existing {
		t.Fatalf("expected existing session pointer")
	}
}

func TestPhase17DistributedSessionLifecycleSaveCompletedTaskAddsTaskBeforeSave(t *testing.T) {
	repo := &phase17SessionRepo{}
	lifecycle := newDistributedSessionLifecycle(repo)
	sess := newCanonicalOrchestratorTestSession("line", "U123")
	tk := newOrchestratorTestTurnInput(t, "hello", "line", "U123")

	if err := lifecycle.SaveCompletedTurnInput(context.Background(), sess, tk); err != nil {
		t.Fatalf("SaveCompletedTask failed: %v", err)
	}
	if len(repo.saved) != 1 || repo.saved[0] != sess {
		t.Fatalf("expected one saved session, got %#v", repo.saved)
	}
	if got := sess.GetHistory(); len(got) != 1 || got[0].MessageText() != "hello" {
		t.Fatalf("expected saved task in session history, got %#v", got)
	}
}

func TestPhase17DistributedSessionLifecycleSaveErrorReturnsErrorAfterTaskAdded(t *testing.T) {
	repo := &phase17SessionRepo{saveErr: errors.New("save failed")}
	lifecycle := newDistributedSessionLifecycle(repo)
	sess := newCanonicalOrchestratorTestSession("line", "U123")
	tk := newOrchestratorTestTurnInput(t, "hello", "line", "U123")

	err := lifecycle.SaveCompletedTurnInput(context.Background(), sess, tk)
	if err == nil {
		t.Fatal("expected save error")
	}
	if len(sess.GetHistory()) != 1 {
		t.Fatalf("expected task to be added before save error, got %d", len(sess.GetHistory()))
	}
}
