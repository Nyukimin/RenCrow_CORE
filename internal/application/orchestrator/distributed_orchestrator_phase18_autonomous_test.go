package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestPhase18DistributedAutonomousCoordinatorUsesUpdatedReportStore(t *testing.T) {
	var emitted []string
	var executed bool
	reporter := &distMockReportStore{}
	coordinator := newDistributedAutonomousCoordinator(
		nil,
		func() int { return 0 },
		func(eventType, from, to, content, route, taskID, sessionID, channel, chatID string) {
			emitted = append(emitted, eventType+":"+content)
		},
		func(ctx context.Context, gotTask conversation.TurnInput, route routing.Route, taskID modulecore.TaskID, ttsSessionID string) (string, error) {
			executed = true
			if route != routing.RoutePLAN {
				t.Fatalf("expected route PLAN, got %s", route)
			}
			if gotTask.SessionID() != "sess-1" || ttsSessionID != "tts-1" {
				t.Fatalf("unexpected route context: session=%s tts=%s", gotTask.SessionID(), ttsSessionID)
			}
			return "計画しました", nil
		},
	)
	coordinator.SetReportStore(reporter)

	taskID := modulecore.NewTaskID()
	tk := newOrchestratorTestTurnInput(t, "買い物の計画を作ってください", "line", "U123").WithSessionID("sess-1")
	resp, err := coordinator.Execute(context.Background(), tk, routing.RoutePLAN, taskID, "tts-1")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if resp != "計画しました" {
		t.Fatalf("expected response to be returned, got %q", resp)
	}
	if !executed {
		t.Fatal("expected route direct executor to be called")
	}
	if len(emitted) == 0 {
		t.Fatal("expected entry stage events to be emitted")
	}
	if len(reporter.reports) != 1 {
		t.Fatalf("expected one execution report to be saved, got %d", len(reporter.reports))
	}
	if reporter.reports[0].Status != "passed" {
		t.Fatalf("expected passed report, got %s", reporter.reports[0].Status)
	}
}

func TestPhase18DistributedAutonomousCoordinatorAddsRetryMessageOnlyAfterFirstAttempt(t *testing.T) {
	var userMessages []string
	coordinator := newDistributedAutonomousCoordinator(
		nil,
		func() int { return 1 },
		func(eventType, from, to, content, route, taskID, sessionID, channel, chatID string) {},
		func(ctx context.Context, gotTask conversation.TurnInput, route routing.Route, taskID modulecore.TaskID, ttsSessionID string) (string, error) {
			userMessages = append(userMessages, gotTask.MessageText())
			if len(userMessages) == 1 {
				return "provider error", errors.New("provider error")
			}
			return "復旧しました", nil
		},
	)

	taskID := modulecore.NewTaskID()
	tk := newOrchestratorTestTurnInput(t, "実行してください", "line", "U123").WithSessionID("sess-1")
	resp, err := coordinator.Execute(context.Background(), tk, routing.RouteOPS, taskID, "tts-1")
	if err != nil {
		t.Fatalf("Execute failed after retry: %v", err)
	}
	if resp != "復旧しました" {
		t.Fatalf("expected retry response, got %q", resp)
	}
	if len(userMessages) != 2 {
		t.Fatalf("expected two attempts, got %#v", userMessages)
	}
	if strings.Contains(userMessages[0], "Executor Retry Context") {
		t.Fatalf("first attempt should not include retry context: %q", userMessages[0])
	}
	if !strings.Contains(userMessages[1], "Executor Retry Context") || !strings.Contains(userMessages[1], "retry_attempt: 1") {
		t.Fatalf("retry attempt should include retry context: %q", userMessages[1])
	}
}

func TestPhase18DistributedAutonomousCoordinatorReturnsResultResponseOnError(t *testing.T) {
	coordinator := newDistributedAutonomousCoordinator(
		nil,
		func() int { return 0 },
		func(eventType, from, to, content, route, taskID, sessionID, channel, chatID string) {},
		func(ctx context.Context, gotTask conversation.TurnInput, route routing.Route, taskID modulecore.TaskID, ttsSessionID string) (string, error) {
			return "途中結果", errors.New("command error")
		},
	)

	taskID := modulecore.NewTaskID()
	tk := newOrchestratorTestTurnInput(t, "実行してください", "line", "U123").WithSessionID("sess-1")
	resp, err := coordinator.Execute(context.Background(), tk, routing.RouteOPS, taskID, "tts-1")
	if err == nil {
		t.Fatal("expected executor error")
	}
	if resp != "途中結果" {
		t.Fatalf("expected partial response to be returned, got %q", resp)
	}
}
