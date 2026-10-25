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
	responses []string
	index     int
}

func (s *loopHandoffCoderStub) Generate(context.Context, conversation.TurnInput, string) (string, error) {
	return s.next(), nil
}

func (s *loopHandoffCoderStub) GenerateWithContext(context.Context, []llm.Message) (string, error) {
	return s.next(), nil
}

func (s *loopHandoffCoderStub) next() string {
	if len(s.responses) == 0 {
		return ""
	}
	if s.index >= len(s.responses) {
		return s.responses[len(s.responses)-1]
	}
	response := s.responses[s.index]
	s.index++
	return response
}

func TestCodeExecutor_CoderLoopReportsBackThroughDelegationChain(t *testing.T) {
	coder := &loopHandoffCoderStub{responses: []string{
		`{"type":"read_request","actions":[{"action":"shell_command","target":"cat README.md"}]}`,
		`{"type":"final_report","summary":"調査完了","changed_files":["README.md"],"tests_run":["go test ./..."],"remaining_risks":[]}`,
	}}
	worker := &recordingCodeWorkerExecutionService{}
	var events []codeExecutorEvent
	executor := NewDefaultCodeExecutor(coder, nil, nil, nil, worker, nil, recordingCodeEventEmitter(&events)).
		WithCoderLoopPrompts(map[string]string{"coder1": "CoderLoop prompt"})

	taskID := modulecore.NewTaskID()
	req := newOrchestratorTestCodeExecutionRequest(t, taskID, "会話内容を保ったまま修正して", routing.RouteCODE1, "sess-1", "test", "chat-1")

	response, err := executor.ExecuteCode(context.Background(), req)
	if err != nil {
		t.Fatalf("ExecuteCode failed: %v", err)
	}
	if strings.Contains(response.Response, "README.md") || strings.Contains(response.Response, "go test ./...") {
		t.Fatalf("Coder claims were displayed as Worker evidence: %q", response.Response)
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
	for _, event := range events {
		if event.taskID != taskID.String() {
			t.Fatalf("loop event task ID=%q, want supplied task %q: %+v", event.taskID, taskID, events)
		}
	}
}
