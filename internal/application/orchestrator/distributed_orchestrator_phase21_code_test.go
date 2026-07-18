package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
)

func TestPhase21DistributedCodeExecutionCoordinatorAddsCoderConfigAndFinishesWithoutProposal(t *testing.T) {
	var coderMsg domaintransport.Message
	var events []OrchestratorEvent
	coordinator := newDistributedCodeExecutionCoordinator(
		session.NewCentralMemory(),
		func(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) {
			events = append(events, NewEvent(eventType, from, to, content, route, jobID, sessionID, channel, chatID))
		},
		func(from, to, content, route, jobID, sessionID, channel, chatID string) {},
		func(route routing.Route, userMessage string) string { return "coder3" },
		func() map[string]interface{} { return map[string]interface{}{"coder3": "cfg"} },
		func() int { return 0 },
		func(ctx context.Context, targetAgent string, msg domaintransport.Message, receiveOnAgent string) (domaintransport.Message, error) {
			coderMsg = msg
			return domaintransport.Message{From: targetAgent, To: "shiro", Content: "coder result", Type: domaintransport.MessageTypeResult}, nil
		},
		func(ctx context.Context, targetAgent string, msg domaintransport.Message) (domaintransport.Message, error) {
			if targetAgent != "shiro" {
				t.Fatalf("expected shiro target, got %s", targetAgent)
			}
			return domaintransport.Message{From: "shiro", To: "mio", Content: "final result", Type: domaintransport.MessageTypeResult}, nil
		},
	)

	resp, err := coordinator.Execute(context.Background(), task.NewTask(task.NewJobID(), "code please", "line", "U123"), routing.RouteCODE3, "sess-1", "job-1")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if resp != "final result" {
		t.Fatalf("expected final result, got %q", resp)
	}
	if coderMsg.Context["route"] != "CODE3" || coderMsg.Context["retry_attempt"] != 0 || coderMsg.Context["channel"] != "line" || coderMsg.Context["chat_id"] != "U123" {
		t.Fatalf("unexpected coder context: %#v", coderMsg.Context)
	}
	if coderMsg.Context["coder_config"] != "cfg" {
		t.Fatalf("expected coder_config, got %#v", coderMsg.Context)
	}
	mioDelegate := orchestratorEventIndex(events, "agent.delegate", "mio", "shiro")
	shiroReadback := orchestratorEventIndex(events, "agent.acknowledge", "shiro", "mio")
	coderDelegate := orchestratorEventIndex(events, "agent.delegate", "shiro", "coder3")
	coderReadback := orchestratorEventIndex(events, "agent.acknowledge", "coder3", "shiro")
	coderReport := orchestratorEventIndex(events, "agent.report", "coder3", "shiro")
	shiroReport := orchestratorEventIndex(events, "agent.report", "shiro", "mio")
	if mioDelegate < 0 || shiroReadback < 0 || coderDelegate < 0 || coderReadback < 0 || coderReport < 0 || shiroReport < 0 {
		t.Fatalf("missing distributed handoff speech events: %#v", events)
	}
	if !(mioDelegate < shiroReadback && shiroReadback < coderDelegate && coderDelegate < coderReadback && coderReadback < coderReport && coderReport < shiroReport) {
		t.Fatalf("unexpected distributed handoff order: %#v", events)
	}
}

func TestPhase21DistributedCodeExecutionCoordinatorReturnsNoCoderMapped(t *testing.T) {
	coordinator := newDistributedCodeExecutionCoordinator(
		session.NewCentralMemory(),
		func(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) {},
		func(from, to, content, route, jobID, sessionID, channel, chatID string) {},
		func(route routing.Route, userMessage string) string { return "" },
		func() map[string]interface{} { return nil },
		func() int { return 0 },
		nil,
		nil,
	)

	_, err := coordinator.Execute(context.Background(), task.NewTask(task.NewJobID(), "code please", "line", "U123"), routing.RouteCODE, "sess-1", "job-1")
	if err == nil || err.Error() != "no coder mapped for route CODE" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPhase21DistributedCodeExecutionCoordinatorRetriesCoderMailboxFailure(t *testing.T) {
	var attempts []string
	coordinator := newDistributedCodeExecutionCoordinator(
		session.NewCentralMemory(),
		func(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) {},
		func(from, to, content, route, jobID, sessionID, channel, chatID string) {},
		func(route routing.Route, userMessage string) string { return "coder3" },
		func() map[string]interface{} { return nil },
		func() int { return 1 },
		func(ctx context.Context, targetAgent string, msg domaintransport.Message, receiveOnAgent string) (domaintransport.Message, error) {
			attempts = append(attempts, msg.Content)
			if len(attempts) == 1 {
				return domaintransport.Message{}, errors.New("command not found")
			}
			return domaintransport.Message{From: targetAgent, To: "shiro", Content: "coder result", Type: domaintransport.MessageTypeResult}, nil
		},
		func(ctx context.Context, targetAgent string, msg domaintransport.Message) (domaintransport.Message, error) {
			return domaintransport.Message{From: "shiro", To: "mio", Content: "final result", Type: domaintransport.MessageTypeResult}, nil
		},
	)

	resp, err := coordinator.Execute(context.Background(), task.NewTask(task.NewJobID(), "code please", "line", "U123"), routing.RouteCODE3, "sess-1", "job-1")
	if err != nil {
		t.Fatalf("Execute failed after retry: %v", err)
	}
	if resp != "final result" {
		t.Fatalf("expected final result, got %q", resp)
	}
	if len(attempts) != 2 {
		t.Fatalf("expected two coder attempts, got %#v", attempts)
	}
	if attempts[0] == attempts[1] {
		t.Fatalf("expected retry instruction to change request text")
	}
}

func TestPhase21DistributedCodeExecutionCoordinatorRecordsCoderProposalEvidence(t *testing.T) {
	evidence := &recordingCoderProposalEvidenceRecorder{}
	coordinator := newDistributedCodeExecutionCoordinator(
		session.NewCentralMemory(),
		func(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) {},
		func(from, to, content, route, jobID, sessionID, channel, chatID string) {},
		func(route routing.Route, userMessage string) string { return "coder3" },
		func() map[string]interface{} { return nil },
		func() int { return 0 },
		func(ctx context.Context, targetAgent string, msg domaintransport.Message, receiveOnAgent string) (domaintransport.Message, error) {
			return domaintransport.Message{
				From:    targetAgent,
				To:      "shiro",
				Content: "coder result",
				Type:    domaintransport.MessageTypeResult,
				Proposal: &domaintransport.ProposalPayload{
					Plan:     "Update Skill behavior",
					Patch:    "diff --git a/skills/core/example/SKILL.md b/skills/core/example/SKILL.md",
					Risk:     "low",
					CostHint: "low",
				},
			}, nil
		},
		func(ctx context.Context, targetAgent string, msg domaintransport.Message) (domaintransport.Message, error) {
			return domaintransport.Message{
				From:    "shiro",
				To:      "mio",
				Content: "final result",
				Type:    domaintransport.MessageTypeResult,
				Result: &domaintransport.ResultPayload{
					Success:      true,
					Summary:      "実行: 1 件, 成功: 1 件, 失敗: 0 件",
					ExecutedCmds: 1,
				},
			}, nil
		},
	)
	coordinator.SetCoderProposalEvidenceRecorder(evidence)

	_, err := coordinator.Execute(context.Background(), task.NewTask(task.NewJobID(), "Skillを更新して", "line", "U123"), routing.RouteCODE3, "sess-1", "job-1")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if len(evidence.items) != 1 {
		t.Fatalf("recorded evidence count=%d, want 1", len(evidence.items))
	}
	got := evidence.items[0]
	if got.JobID != "job-1" || got.SessionID != "sess-1" || got.Route != "CODE3" || got.Agent != "coder3" {
		t.Fatalf("unexpected evidence metadata: %#v", got)
	}
	if got.Patch == "" || got.Plan == "" || got.FormattedResult != "final result" {
		t.Fatalf("proposal evidence missing content: %#v", got)
	}
	if !got.Success || got.ExecutionSummary == "" {
		t.Fatalf("proposal evidence should include successful execution summary: %#v", got)
	}
}
