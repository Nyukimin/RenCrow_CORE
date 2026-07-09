package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

func TestPhase18DistributedAutonomousCoordinatorUsesUpdatedReportStore(t *testing.T) {
	var emitted []string
	var executed bool
	reporter := &distMockReportStore{}
	coordinator := newDistributedAutonomousCoordinator(
		nil,
		func() int { return 0 },
		func(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) {
			emitted = append(emitted, eventType+":"+content)
		},
		func(ctx context.Context, gotTask task.Task, route routing.Route, sessionID, ttsSessionID string) (string, error) {
			executed = true
			if route != routing.RoutePLAN {
				t.Fatalf("expected route PLAN, got %s", route)
			}
			if sessionID != "sess-1" || ttsSessionID != "tts-1" {
				t.Fatalf("unexpected route context: session=%s tts=%s", sessionID, ttsSessionID)
			}
			return "計画しました", nil
		},
	)
	coordinator.SetReportStore(reporter)

	tk := task.NewTask(task.NewJobID(), "買い物の計画を作ってください", "line", "U123")
	resp, err := coordinator.Execute(context.Background(), tk, routing.RoutePLAN, "sess-1", "tts-1")
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
		func(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) {},
		func(ctx context.Context, gotTask task.Task, route routing.Route, sessionID, ttsSessionID string) (string, error) {
			userMessages = append(userMessages, gotTask.UserMessage())
			if len(userMessages) == 1 {
				return "provider error", errors.New("provider error")
			}
			return "復旧しました", nil
		},
	)

	tk := task.NewTask(task.NewJobID(), "実行してください", "line", "U123")
	resp, err := coordinator.Execute(context.Background(), tk, routing.RouteOPS, "sess-1", "tts-1")
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
		func(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) {},
		func(ctx context.Context, gotTask task.Task, route routing.Route, sessionID, ttsSessionID string) (string, error) {
			return "途中結果", errors.New("command error")
		},
	)

	tk := task.NewTask(task.NewJobID(), "実行してください", "line", "U123")
	resp, err := coordinator.Execute(context.Background(), tk, routing.RouteOPS, "sess-1", "tts-1")
	if err == nil {
		t.Fatal("expected executor error")
	}
	if resp != "途中結果" {
		t.Fatalf("expected partial response to be returned, got %q", resp)
	}
}
