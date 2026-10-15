package agent

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

// Failure knowledge: docs/調査/*ShiroChatのPrefix_Cache不成立とStrata_prefill.md.
// The Gateway keeps only the Character and Stable blocks in the leading system
// message, so every constant instruction must be typed Stable and nothing that
// changes per turn (time, recall, route) may be.

func staticPrefixOf(messages []llm.Message) []llm.Message {
	var static []llm.Message
	for _, message := range messages {
		if message.Type == llm.PromptContextCharacter || message.Type == llm.PromptContextStable {
			static = append(static, message)
		}
	}
	return static
}

// Every conversational character goes through assemblePromptContext, so the
// static-first order is enforced there rather than trusted to each caller: a
// Stable block that a caller passes among the dynamic messages is still placed
// before every Recall and Variable block.
func TestAssemblePromptContextPlacesEveryStableBlockBeforeTheDynamicOnes(t *testing.T) {
	dynamic := []llm.Message{
		{Role: "system", Content: "recall one", Type: llm.PromptContextRecall},
		{Role: "user", Content: "history question"},
		{Role: "system", Content: "late stable rule", Type: llm.PromptContextStable},
		{Role: "system", Content: "now", Type: llm.PromptContextVariable},
		{Role: "assistant", Content: "history answer"},
	}
	messages := assemblePromptContext("character", "stable runtime", dynamic, llm.Message{Role: "user", Content: "question"})

	rank := map[llm.PromptContextType]int{llm.PromptContextCharacter: 0, llm.PromptContextStable: 1, llm.PromptContextRecall: 2, llm.PromptContextVariable: 3, llm.PromptContextUser: 4}
	previous := -1
	for i, message := range messages {
		current, known := rank[message.Type]
		if !known {
			t.Fatalf("message %d has an unknown type: %#v", i, message)
		}
		if current < previous {
			t.Fatalf("message %d (%s) comes after a later block kind; order = %#v", i, message.Type, messages)
		}
		previous = current
	}
	if staticPrefixOf(messages)[len(staticPrefixOf(messages))-1].Content != "late stable rule" {
		t.Fatalf("the late Stable block must end the static prefix: %#v", staticPrefixOf(messages))
	}
	if messages[len(messages)-1].Type != llm.PromptContextUser {
		t.Fatalf("the user message must stay last: %#v", messages)
	}
}

func TestShiroStaticPrefixDoesNotDependOnTheTurn(t *testing.T) {
	var captured [][]llm.Message
	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			captured = append(captured, append([]llm.Message(nil), req.Messages...))
			return llm.GenerateResponse{Content: "ok", FinishReason: "stop"}, nil
		},
	}
	shiro := NewShiroAgent(provider, &mockToolRunner{}, &mockMCPClient{}, "test prompt", nil).
		WithPersona(AgentPersona{Name: "Shiro", Personality: "precise worker"})

	for _, text := range []string{"ファイルを作成して", "全く別の依頼です"} {
		if _, err := shiro.Execute(context.Background(), newAgentTurnInput(t, text, "line", "U123")); err != nil {
			t.Fatalf("Execute(%q) error = %v", text, err)
		}
	}
	if len(captured) != 2 {
		t.Fatalf("captured %d requests, want 2", len(captured))
	}

	first, second := staticPrefixOf(captured[0]), staticPrefixOf(captured[1])
	if len(first) == 0 {
		t.Fatalf("no Character/Stable blocks in %#v", captured[0])
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("the static prefix changed between turns:\n%#v\n%#v", first, second)
	}
	if staticPromptHash(captured[0]) != staticPromptHash(captured[1]) {
		t.Fatal("staticPromptHash changed between turns")
	}
	for _, message := range first {
		if strings.Contains(message.Content, llm.CurrentJSTTimePrefix) {
			t.Fatalf("time leaked into the static prefix: %#v", message)
		}
	}

	// The constant Japanese-response rule and the persona text are instructions
	// that never change, so they belong to the static prefix, not to Variable.
	for _, constant := range []string{"必ず自然な日本語で応答", "precise worker"} {
		if !hasPromptContextMessage(captured[0], llm.PromptContextStable, constant) {
			t.Errorf("%q must be a Stable block: %#v", constant, captured[0])
		}
		if hasPromptContextMessage(captured[0], llm.PromptContextVariable, constant) {
			t.Errorf("%q must not be a Variable block: %#v", constant, captured[0])
		}
	}
	if !hasPromptContextMessage(captured[0], llm.PromptContextVariable, llm.CurrentJSTTimePrefix) {
		t.Errorf("the time line must stay a Variable block: %#v", captured[0])
	}
}

func TestShiroReActHandsOverTheTypedPromptContext(t *testing.T) {
	var tasks []SubagentTask
	manager := &mockSubagentManager{runSyncFunc: func(ctx context.Context, task SubagentTask) (SubagentResult, error) {
		tasks = append(tasks, task)
		return SubagentResult{AgentName: task.AgentName, Output: "ok"}, nil
	}}
	shiro := NewShiroAgent(&mockLLMProvider{}, &mockToolRunner{}, &mockMCPClient{}, "test prompt", manager).
		WithPersona(AgentPersona{Name: "Shiro", Personality: "precise worker"})

	for _, text := range []string{"ファイルを作成して", "全く別の依頼です"} {
		if _, err := shiro.Execute(context.Background(), newAgentTurnInput(t, text, "line", "U123")); err != nil {
			t.Fatalf("Execute(%q) error = %v", text, err)
		}
	}
	if len(tasks) != 2 {
		t.Fatalf("subagent ran %d times, want 2", len(tasks))
	}

	rank := map[llm.PromptContextType]int{llm.PromptContextCharacter: 0, llm.PromptContextStable: 1, llm.PromptContextRecall: 2, llm.PromptContextVariable: 3, llm.PromptContextUser: 4}
	for i, task := range tasks {
		if task.SystemPrompt != "" {
			t.Errorf("task %d: SystemPrompt = %q; the context must travel as typed messages, not as a flattened string", i, task.SystemPrompt)
		}
		if len(task.PromptContext) < 4 {
			t.Fatalf("task %d: PromptContext = %#v", i, task.PromptContext)
		}
		previous := -1
		for _, message := range task.PromptContext {
			current, known := rank[message.Type]
			if !known || current < previous {
				t.Fatalf("task %d: block order violates Character, Stable, Recall, Variable, User: %#v", i, task.PromptContext)
			}
			previous = current
		}
		last := task.PromptContext[len(task.PromptContext)-1]
		if last.Role != "user" || last.Type != llm.PromptContextUser || last.Content != task.Instruction {
			t.Errorf("task %d: the final message must be the user instruction, got %#v", i, last)
		}
		if !hasPromptContextMessage(task.PromptContext, llm.PromptContextVariable, llm.CurrentJSTTimePrefix) {
			t.Errorf("task %d: the time line is missing from %#v", i, task.PromptContext)
		}
	}
	if !reflect.DeepEqual(staticPrefixOf(tasks[0].PromptContext), staticPrefixOf(tasks[1].PromptContext)) {
		t.Fatalf("the static prefix of the ReAct path changed between turns")
	}
}
