package heartbeat

import (
	"context"
	"errors"
	"testing"
	"time"

	gmailapp "github.com/Nyukimin/RenCrow_CORE/internal/application/gmailintake"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type gmailIntakeFunc func(context.Context) (gmailapp.GmailRunReport, error)

func (f gmailIntakeFunc) Run(ctx context.Context) (gmailapp.GmailRunReport, error) { return f(ctx) }

func TestGmailIntakeRunsWithDurableShiroIdentityAndOwnerScope(t *testing.T) {
	for _, fail := range []bool{false, true} {
		svc := withHeartbeatTestTaskOwner(t, NewHeartbeatService(nil, nil, t.TempDir(), 30))
		var identity execution.Identity
		intake := gmailIntakeFunc(func(ctx context.Context) (gmailapp.GmailRunReport, error) {
			var err error
			identity, err = execution.IdentityFromContext(ctx)
			if err != nil {
				t.Fatal(err)
			}
			scope, ok := domaintool.ToolExecutionScopeFromContext(ctx)
			if !ok || scope.ActorID != "shiro" || scope.AuthenticatedUserID != "ren" || !scope.Allows(domaintool.DataScopeUser) {
				t.Fatal("wrong worker scope")
			}
			if fail {
				return gmailapp.GmailRunReport{}, errors.New("source unavailable")
			}
			return gmailapp.GmailRunReport{}, nil
		})
		svc.WithGmailIntake(intake, "ren", time.Hour, time.Minute, false)
		_, err := svc.runGmailIntake(context.Background())
		if (err != nil) != fail {
			t.Fatalf("err=%v", err)
		}
		reader := svc.taskOwner.(interface {
			Get(context.Context, modulecore.TaskID) (domaintask.Task, error)
		})
		task, err := reader.Get(context.Background(), identity.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		want := domaintask.StatusSucceeded
		if fail {
			want = domaintask.StatusFailed
		}
		if task.Assignee != "shiro" || task.Status != want {
			t.Fatalf("terminal task=%+v", task)
		}
	}
}

func TestGmailCollectionSingleFlightAndShutdownCancel(t *testing.T) {
	svc := withHeartbeatTestTaskOwner(t, NewHeartbeatService(nil, nil, t.TempDir(), 30))
	started := make(chan struct{})
	stopped := make(chan struct{})
	svc.WithGmailIntake(gmailIntakeFunc(func(ctx context.Context) (gmailapp.GmailRunReport, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return gmailapp.GmailRunReport{}, ctx.Err()
	}), "ren", time.Hour, time.Minute, false)
	if !svc.startGmailIntake() {
		t.Fatal("did not start")
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("intake did not enter")
	}
	if svc.startGmailIntake() {
		t.Fatal("concurrent intake started")
	}
	svc.stopGmailIntake()
	select {
	case <-stopped:
	default:
		t.Fatal("shutdown did not await cancellation")
	}
}
