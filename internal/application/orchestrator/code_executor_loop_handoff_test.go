package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type loopHandoffCoderStub struct {
	response string
}

func (s *loopHandoffCoderStub) Generate(context.Context, conversation.TurnInput, string) (string, error) {
	return s.response, nil
}

func (s *loopHandoffCoderStub) GenerateWithContext(context.Context, []llm.Message) (string, error) {
	return s.response, nil
}

func TestCodeExecutor_CoderLoopReportsBackThroughDelegationChain(t *testing.T) {
	coder := &loopHandoffCoderStub{response: `{"type":"final_report","summary":"修正完了","changed_files":["README.md"],"tests_run":["go test ./..."],"remaining_risks":[]}`}
	worker := &recordingCodeWorkerExecutionService{}
	var events []codeExecutorEvent
	executor := NewDefaultCodeExecutor(coder, nil, nil, nil, worker, nil, recordingCodeEventEmitter(&events)).
		WithCoderLoopPrompts(map[string]string{"coder1": "CoderLoop prompt"})

	jobID := modulecore.NewTaskID()
	req := newOrchestratorTestCodeExecutionRequest(t, jobID, "会話内容を保ったまま修正して", routing.RouteCODE1, "sess-1", "test", "chat-1")

	if _, err := executor.ExecuteCode(context.Background(), req); err != nil {
		t.Fatalf("ExecuteCode failed: %v", err)
	}

	want := []struct {
		eventType string
		from      string
		to        string
		prefix    string
	}{
		{"agent.delegate", "mio", "shiro", "Shiro、"},
		{"agent.acknowledge", "shiro", "mio", "Mio、"},
		{"agent.delegate", "shiro", "coder1", "Coder1、"},
		{"agent.acknowledge", "coder1", "shiro", "Shiro、"},
		{"agent.report", "coder1", "shiro", "Shiro、"},
		{"agent.report", "shiro", "mio", "Mio、"},
	}
	last := -1
	for _, expected := range want {
		idx := codeExecutorEventIndex(events, expected.eventType, expected.from, expected.to)
		if idx <= last {
			t.Fatalf("handoff event order mismatch for %s %s->%s: index=%d last=%d events=%#v", expected.eventType, expected.from, expected.to, idx, last, events)
		}
		if !strings.HasPrefix(events[idx].content, expected.prefix) {
			t.Fatalf("%s %s->%s must start by naming the recipient: %q", expected.eventType, expected.from, expected.to, events[idx].content)
		}
		last = idx
	}
}
