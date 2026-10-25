package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestDistributedCodeExecutionHoldsCoderLeaseForTaskAndReleasesOnEveryReturn(t *testing.T) {
	injected := errors.New("injected coder mailbox failure")
	for _, tc := range []struct {
		name       string
		cancel     bool
		mailboxErr error
		wantErr    bool
	}{
		{name: "success"},
		{name: "mailbox_error", mailboxErr: injected, wantErr: true},
		{name: "cancel", cancel: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status := NewCoderStatus()
			selector := newDistributedCoderSelection(nil, map[string]domaintransport.Transport{
				"coder4": &distMockTransport{},
			}, NewNodeSelector(), nil, status)
			shiroCalled := false
			coordinator := newDistributedCodeExecutionCoordinator(
				session.NewCentralMemory(),
				func(string, string, string, string, string, string, string, string, string) {},
				func(string, string, string, string, string, string, string, string) {},
				selector.AcquireForRoute,
				func() map[string]interface{} { return nil },
				func() int { return 0 },
				func(ctx context.Context, target string, msg domaintransport.Message, receiveOn string) (domaintransport.Message, error) {
					if target != "coder4" || receiveOn != "mio" {
						t.Fatalf("coder mailbox target=%q receiveOn=%q", target, receiveOn)
					}
					if !status.IsBusy("coder4") {
						t.Fatal("coder lease must remain held during mailbox execution")
					}
					if tc.cancel {
						return domaintransport.Message{}, ctx.Err()
					}
					if tc.mailboxErr != nil {
						return domaintransport.Message{}, tc.mailboxErr
					}
					response := domaintransport.NewMessage(target, msg.From, msg.SessionID, msg.TaskID, "coder result")
					response.Type = domaintransport.MessageTypeResult
					return response, nil
				},
				func(context.Context, string, domaintransport.Message) (domaintransport.Message, error) {
					shiroCalled = true
					if !status.IsBusy("coder4") {
						t.Fatal("coder lease must remain held until the Shiro task finishes")
					}
					return domaintransport.Message{From: "shiro", To: "mio", Content: "finished", Type: domaintransport.MessageTypeResult}, nil
				},
			)

			ctx := context.Background()
			if tc.cancel {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			_, err := coordinator.Execute(ctx, newOrchestratorTestTurnInput(t, "work", "viewer", "user"), routing.RouteCODE4, modulecore.NewTaskID())
			if (err != nil) != tc.wantErr {
				t.Fatalf("Execute error=%v, wantErr=%t", err, tc.wantErr)
			}
			if got, want := shiroCalled, !tc.wantErr; got != want {
				t.Fatalf("Shiro called=%t, want %t", got, want)
			}
			if status.IsBusy("coder4") {
				t.Fatal("coder lease must be released after task success, error, or cancellation")
			}
		})
	}
}
