package vision

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/transportmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	domainvision "github.com/Nyukimin/RenCrow_CORE/internal/domain/vision"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	transportpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestActionAnalyzerPersistsVisionActionRequestAndResponse(t *testing.T) {
	requestID := modulecore.NewRequestID()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"ok":true,"request_id":%q,"provider":"vision","model":"Wild","kind":"image","text":"ok"}`, requestID)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	taskStore, err := taskpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = taskStore.Close() })
	actionStore, err := actionpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatal(err)
	}
	actions := actionmanager.New(actionStore)
	transportStore, err := transportpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "transport"))
	if err != nil {
		t.Fatal(err)
	}
	transports := transportmanager.New(transportStore, actions)
	tasks := taskmanager.New(taskStore, taskmanager.DefaultParallelLimits())
	analyzer, err := NewActionAnalyzer(client, actions, tasks, transports, "mio")
	if err != nil {
		t.Fatal(err)
	}

	result, err := analyzer.Analyze(context.Background(), domainvision.AnalyzeRequest{
		RequestID: requestID, SessionID: "session", Kind: "image",
		Filename: "image.png", ContentType: "image/png", Data: []byte("image"),
	})
	if err != nil {
		t.Fatal(err)
	}
	storedActions, err := actions.ListActions(context.Background(), domainaction.Filter{})
	if err != nil || len(storedActions) != 1 {
		t.Fatalf("actions=%#v err=%v", storedActions, err)
	}
	action := storedActions[0]
	attempts, _ := actions.ListAttempts(context.Background(), domainaction.AttemptFilter{ActionID: action.ActionID})
	requests, _ := transports.ListRequests(context.Background(), domaintransport.RequestFilter{AttemptID: action.CurrentAttemptID})
	responses, _ := transports.ListResponses(context.Background(), domaintransport.ResponseFilter{AttemptID: action.CurrentAttemptID})
	task, _ := tasks.Get(context.Background(), action.TaskID)
	run, _ := tasks.GetRun(context.Background(), action.RunID)
	if action.Kind != domainaction.KindVision || action.Status != domainaction.StatusSucceeded ||
		len(attempts) != 1 || attempts[0].Status != domainaction.AttemptStatusSucceeded ||
		len(requests) != 1 || requests[0].RequestID != requestID ||
		len(responses) != 1 || responses[0].RequestID != requestID ||
		result.ResponseID != responses[0].ResponseID {
		t.Fatalf("identity chain action=%#v attempts=%#v requests=%#v responses=%#v result=%#v", action, attempts, requests, responses, result)
	}
	if task.Status != domaintask.StatusSucceeded || run.Status != domaintask.RunStatusSucceeded {
		t.Fatalf("task=%#v run=%#v", task, run)
	}
}
