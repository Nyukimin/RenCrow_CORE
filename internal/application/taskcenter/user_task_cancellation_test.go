package taskcenter

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	taskmanager "github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainsession "github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	tool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	persistworkstream "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type userTaskCancellationPublicationReaderStub struct {
	binding domainsession.CanonicalSessionPublicationBinding
	err     error
}

func (r userTaskCancellationPublicationReaderStub) CurrentPublicationBinding(context.Context, string) (domainsession.CanonicalSessionPublicationBinding, error) {
	return r.binding, r.err
}

func TestUserTaskCancellationRequiresCurrentAuthenticatedSessionOwner(t *testing.T) {
	planStore, err := persistworkstream.NewSQLiteStore(filepath.Join(t.TempDir(), "task-cancel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = planStore.Close() })
	taskStore, err := taskpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = taskStore.Close() })
	manager := taskmanager.New(taskStore, taskmanager.DefaultParallelLimits())
	service, err := NewService(planStore, taskCenterTestPolicy("mio"), []string{"mio"}, []Kind{{ID: "research", Label: "Research", ExecutorAvailable: true}}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	service.nativeExecution = &NativeExecution{Tasks: manager}
	address, err := domainconversation.NewChannelAddress("viewer", "cmd:user-a")
	if err != nil {
		t.Fatal(err)
	}
	sessionID := modulecore.NewSessionID()
	canonical, err := domainsession.NewCanonicalSessionWithOwner(sessionID, "user-a", "2026-11-25", address, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	currentBinding, err := domainsession.NewCanonicalSessionPublicationBinding(canonical)
	if err != nil {
		t.Fatal(err)
	}
	turnBinding, err := domainconversation.NewSessionBindingWithPublicationIncarnation(sessionID, address, domainconversation.SessionAudiencePrivate, "user-a", 1, currentBinding.PublicationIncarnation())
	if err != nil {
		t.Fatal(err)
	}
	createCtx := domainconversation.WithSessionBinding(context.Background(), turnBinding)
	created, err := manager.Create(createCtx, domaintask.Task{
		Title: "legacy request", Route: domaintask.RouteGeneral, Assignee: "mio", OriginSessionID: sessionID,
		OriginThreadID: modulecore.NewThreadID(), OriginTurnID: modulecore.NewTurnID(), OriginMessageID: modulecore.NewMessageID(),
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	trusted := tool.WithToolExecutionScope(context.Background(), taskCenterServiceTestScope(t, "local-user"))
	service.WithUserTaskCancellationPublicationReader(userTaskCancellationPublicationReaderStub{binding: currentBinding})
	forgedOrigin, err := manager.Create(context.Background(), domaintask.Task{
		Title: "forged unbound origin", Route: domaintask.RouteGeneral, Assignee: "mio", OriginSessionID: modulecore.NewSessionID(),
		OriginThreadID: modulecore.NewThreadID(), OriginTurnID: modulecore.NewTurnID(), OriginMessageID: modulecore.NewMessageID(),
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.InspectUserTaskCancellation(trusted, forgedOrigin.TaskID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unbound/forged origin error=%v, want forbidden", err)
	}
	inspection, err := service.InspectUserTaskCancellation(trusted, created.TaskID)
	if err != nil || !inspection.Cancellable {
		t.Fatalf("inspection=%+v err=%v", inspection, err)
	}
	cancelled, err := service.CancelUserOriginTask(trusted, created.TaskID)
	if err != nil || !cancelled.Cancelled {
		t.Fatalf("cancel=%+v err=%v", cancelled, err)
	}
	spoofed := taskCenterServiceTestScope(t, "another-user")
	spoofed.AuthenticatedUserID = "local-user"
	spoofedCtx := tool.WithToolExecutionScope(context.Background(), spoofed)
	if _, err := service.CancelUserOriginTask(spoofedCtx, created.TaskID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("spoofed identity error=%v, want forbidden", err)
	}
	foreignCanonical, err := domainsession.NewCanonicalSessionWithOwner(sessionID, "another-user", "2026-11-25", address, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	foreignBinding, err := domainsession.NewCanonicalSessionPublicationBinding(foreignCanonical)
	if err != nil {
		t.Fatal(err)
	}
	service.WithUserTaskCancellationPublicationReader(userTaskCancellationPublicationReaderStub{binding: foreignBinding})
	if _, err := service.InspectUserTaskCancellation(trusted, created.TaskID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("current owner mismatch error=%v, want forbidden", err)
	}
	service.WithUserTaskCancellationPublicationReader(userTaskCancellationPublicationReaderStub{err: errors.New("reader unavailable")})
	if _, err := service.InspectUserTaskCancellation(trusted, created.TaskID); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("publication evidence failure=%v, want unavailable", err)
	}
	service.userTaskCancellationPublications = nil
	if _, err := service.InspectUserTaskCancellation(trusted, created.TaskID); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing publication evidence reader=%v, want unavailable", err)
	}
}
