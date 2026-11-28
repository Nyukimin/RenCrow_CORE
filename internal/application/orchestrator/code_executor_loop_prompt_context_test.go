package orchestrator

import (
	"context"
	"strings"
	"testing"

	moduleapp "github.com/Nyukimin/RenCrow_CORE/internal/application/moduleregistry"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/proposal"
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

func TestCodeExecutor_ModuleResolvedRequestKeepsConfiguredCoderLoop(t *testing.T) {
	t.Setenv("RENCROW_WORKSPACE_ROOT", "/workspace/RenCrow")
	coder := &loopMessageCaptureCoder{responses: []string{
		`{"type":"read_request","actions":[{"action":"shell_command","target":"cat README.md"}]}`,
		`{"type":"final_report","summary":"調査完了","changed_files":[],"tests_run":[],"remaining_risks":[]}`,
	}}
	worker := &recordingCodeWorkerExecutionService{}
	executor := NewDefaultCodeExecutor(nil, coder, nil, nil, worker, nil, noopEventEmitter).
		WithCoderLoopPrompts(map[string]string{"coder2": "CoderLoop prompt"}).
		WithModuleResolver(moduleapp.DefaultRegistry())
	req := newOrchestratorTestCodeExecutionRequest(t, modulecore.NewTaskID(), "RenCrow_STT の音声入力を修正して", routing.RouteCODE2, "sess-1", "test", "chat-1")

	if _, err := executor.ExecuteCode(context.Background(), req); err != nil {
		t.Fatalf("ExecuteCode failed: %v", err)
	}
	if len(coder.captured) == 0 {
		t.Fatal("module resolution bypassed the configured CoderLoop")
	}
	if worker.begins != 1 || worker.observations != 1 {
		t.Fatalf("normal CoderLoop worker calls: begins=%d observations=%d, want 1 each", worker.begins, worker.observations)
	}
	lastUserMessage := coder.captured[0][len(coder.captured[0])-1].Content
	if !strings.Contains(lastUserMessage, "RenCrow module context") || !strings.Contains(lastUserMessage, "module_id: stt") {
		t.Fatalf("CoderLoop did not receive the resolved module context: %q", lastUserMessage)
	}
}

type proposalAndLoopCoder struct {
	proposal      *proposal.Proposal
	proposalCalls int
	loopCalls     int
	proposalInput string
}

func (c *proposalAndLoopCoder) Generate(context.Context, conversation.TurnInput, string) (string, error) {
	return "", nil
}

func (c *proposalAndLoopCoder) GenerateProposal(_ context.Context, input conversation.TurnInput) (*proposal.Proposal, error) {
	c.proposalCalls++
	c.proposalInput = input.MessageText()
	return c.proposal, nil
}

func (c *proposalAndLoopCoder) GenerateWithContext(context.Context, []llm.Message) (string, error) {
	c.loopCalls++
	return `{"type":"read_request","actions":[{"action":"shell_command","target":"cat README.md"}]}`, nil
}

func TestCodeExecutor_DiagnosticModeBypassesCoderLoopAndObservations(t *testing.T) {
	t.Setenv("RENCROW_WORKSPACE_ROOT", "/workspace/RenCrow")
	coder := &proposalAndLoopCoder{proposal: proposal.NewProposal(
		"Inspect the request",
		`[{"type":"file_edit","action":"create","target":"must-not-exist.txt","content":"must not execute"}]`,
		"Low risk",
		"Low cost",
	)}
	worker := &recordingCodeWorkerExecutionService{}
	executor := NewDefaultCodeExecutor(nil, coder, nil, nil, worker, nil, noopEventEmitter).
		WithCoderLoopPrompts(map[string]string{"coder2": "CoderLoop prompt"}).
		WithModuleResolver(moduleapp.DefaultRegistry())
	message := "/code2 " + codeDiagnosticNoWriteDirective + "\nRenCrow_STT の音声入力を調査し、結果だけ報告してください。"
	req := newOrchestratorTestCodeExecutionRequest(t, modulecore.NewTaskID(), message, routing.RouteCODE2, "sess-1", "test", "chat-1")

	if _, err := executor.ExecuteCode(context.Background(), req); err == nil {
		t.Fatal("diagnostic-only proposal with file commands should be rejected")
	} else if !strings.Contains(err.Error(), "diagnostic-only request") {
		t.Fatalf("unexpected diagnostic-only error: %v", err)
	}
	if coder.proposalCalls != 1 {
		t.Fatalf("configured Coder provider proposal calls=%d, want 1", coder.proposalCalls)
	}
	if !strings.Contains(coder.proposalInput, "module_id: stt") {
		t.Fatalf("original request flag was lost while resolving module context: %q", coder.proposalInput)
	}
	if coder.loopCalls != 0 || worker.begins != 0 || worker.observations != 0 || worker.calls != 0 {
		t.Fatalf("diagnostic mode entered execution loop: loop=%d begins=%d observations=%d proposals=%d", coder.loopCalls, worker.begins, worker.observations, worker.calls)
	}
}
