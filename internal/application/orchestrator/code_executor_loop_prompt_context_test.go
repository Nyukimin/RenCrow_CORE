package orchestrator

import (
	"context"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// loopMessageCaptureCoder は GenerateWithContext に渡された会話履歴を記録する。
type loopMessageCaptureCoder struct {
	responses []string
	index     int
	captured  [][]llm.Message
}

func (c *loopMessageCaptureCoder) Generate(context.Context, conversation.TurnInput, string) (string, error) {
	return c.next(), nil
}

func (c *loopMessageCaptureCoder) GenerateWithContext(_ context.Context, messages []llm.Message) (string, error) {
	c.captured = append(c.captured, append([]llm.Message(nil), messages...))
	return c.next(), nil
}

func (c *loopMessageCaptureCoder) next() string {
	if len(c.responses) == 0 {
		return ""
	}
	if c.index >= len(c.responses) {
		return c.responses[len(c.responses)-1]
	}
	response := c.responses[c.index]
	c.index++
	return response
}

// CoderLoop の先頭 system は Character として型付けして渡す。型が無いと Gateway は
// 先頭固定の配置を適用できず（instruction_without_block）、時刻などの Variable が
// 先頭 system へ畳み込まれて prefix cache が毎ターン崩れる。
func TestCoderLoopLeadingSystemPromptIsTypedCharacter(t *testing.T) {
	coder := &loopMessageCaptureCoder{responses: []string{
		`{"type":"read_request","actions":[{"action":"shell_command","target":"cat README.md"}]}`,
		`{"type":"final_report","summary":"調査完了","changed_files":[],"tests_run":[],"remaining_risks":[]}`,
	}}
	worker := &recordingCodeWorkerExecutionService{}
	var events []codeExecutorEvent
	executor := NewDefaultCodeExecutor(coder, nil, nil, nil, worker, nil, recordingCodeEventEmitter(&events)).
		WithCoderLoopPrompts(map[string]string{"coder1": "CoderLoop prompt"})

	req := newOrchestratorTestCodeExecutionRequest(t, modulecore.NewTaskID(), "修正して", routing.RouteCODE1, "sess-1", "test", "chat-1")
	if _, err := executor.ExecuteCode(context.Background(), req); err != nil {
		t.Fatalf("ExecuteCode failed: %v", err)
	}

	if len(coder.captured) == 0 {
		t.Fatal("GenerateWithContext was not called")
	}
	first := coder.captured[0][0]
	if first.Role != "system" || first.Content != "CoderLoop prompt" {
		t.Fatalf("leading message = %#v, want the CoderLoop system prompt", first)
	}
	if first.Type != llm.PromptContextCharacter {
		t.Fatalf("leading system prompt type = %q, want %q", first.Type, llm.PromptContextCharacter)
	}
}
