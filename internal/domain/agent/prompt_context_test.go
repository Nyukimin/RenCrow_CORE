package agent

import (
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

func TestCharacterPromptMessagesHaveCanonicalTypesAndOrder(t *testing.T) {
	prompt := strings.Join([]string{"identity", "policy", "scope", "knowledge"}, "\n\n---\n\n")
	messages := characterPromptMessages(prompt)
	if len(messages) != 4 {
		t.Fatalf("character blocks = %d, want 4", len(messages))
	}
	for index, message := range messages {
		if message.Type != llm.PromptContextCharacter {
			t.Fatalf("block %d type = %q", index, message.Type)
		}
		if got := message.Metadata["character_prompt_block"]; got != characterPromptBlockNames[index] {
			t.Fatalf("block %d name = %q, want %q", index, got, characterPromptBlockNames[index])
		}
	}
}

func TestStaticPromptHashIgnoresDynamicPromptContext(t *testing.T) {
	static := append(
		characterPromptMessages(strings.Join([]string{"identity", "policy", "scope", "knowledge"}, "\n\n---\n\n")),
		stableRuntimeContextMessage("contract revision: 7")...,
	)
	first := append(append([]llm.Message{}, static...),
		llm.Message{Role: "system", Content: "time: first; route: CHAT", Type: llm.PromptContextVariable},
		llm.Message{Role: "user", Content: "first", Type: llm.PromptContextUser},
	)
	second := append(append([]llm.Message{}, static...),
		llm.Message{Role: "system", Content: "time: second; route: OPS", Type: llm.PromptContextVariable},
		llm.Message{Role: "user", Content: "second", Type: llm.PromptContextUser},
	)
	if got, want := staticPromptHash(first), staticPromptHash(second); got != want {
		t.Fatalf("dynamic context changed static hash: %s != %s", got, want)
	}
}

func TestStableRuntimeContextSplitsCanonicalContracts(t *testing.T) {
	content := "# Mio Agent Contract Index\nagent\n\n## Routing and Handoff\ninteraction\n\n## Mio Tool Boundary\ntools"
	messages := stableRuntimeContextMessage(content)
	if len(messages) != 3 {
		t.Fatalf("stable blocks = %d, want 3", len(messages))
	}
	for index, want := range []string{"agent_contract", "interaction_contract", "tool_boundary"} {
		if got := messages[index].Metadata["runtime_context_kind"]; got != want {
			t.Fatalf("stable block %d kind = %q, want %q", index, got, want)
		}
	}
}

func TestStableRuntimeContextSplitsSharedAgentControl(t *testing.T) {
	content := "# Shared Agent Control\n\n## Agent Profile\nagent\n\n## Routing\nroutes\n\n## Handoff\nhandoff\n\n## Tools\ntools"
	messages := stableRuntimeContextMessage(content)
	if len(messages) != 3 {
		t.Fatalf("stable blocks = %d, want 3: %#v", len(messages), messages)
	}
	for index, want := range []string{"agent_contract", "interaction_contract", "tool_boundary"} {
		if got := messages[index].Metadata["runtime_context_kind"]; got != want {
			t.Fatalf("stable block %d kind = %q, want %q", index, got, want)
		}
	}
	if !strings.Contains(messages[1].Content, "## Handoff") {
		t.Fatalf("interaction contract lost Handoff: %#v", messages[1])
	}
}

func TestAssemblePromptContextUsesCanonicalOrderAndSingleCharacterBlocks(t *testing.T) {
	character := strings.Join([]string{"identity", "policy", "scope", "knowledge"}, "\n\n---\n\n")
	stable := "# Shared Agent Control\nagent\n\n## Routing\nroute\n\n## Handoff\nhandoff\n\n## Tools\ntools"
	dynamic := []llm.Message{
		{Role: "system", Content: "runtime", Type: llm.PromptContextVariable},
		{Role: "assistant", Content: "recall", Type: llm.PromptContextRecall},
	}
	messages := assemblePromptContext(character, stable, dynamic, llm.Message{Role: "user", Content: "now"})
	wantTypes := []llm.PromptContextType{
		llm.PromptContextCharacter, llm.PromptContextCharacter, llm.PromptContextCharacter, llm.PromptContextCharacter,
		llm.PromptContextStable, llm.PromptContextStable, llm.PromptContextStable,
		llm.PromptContextRecall, llm.PromptContextVariable, llm.PromptContextUser,
	}
	if len(messages) != len(wantTypes) {
		t.Fatalf("messages = %d, want %d: %#v", len(messages), len(wantTypes), messages)
	}
	for index, want := range wantTypes {
		if messages[index].Type != want {
			t.Fatalf("message %d type = %q, want %q", index, messages[index].Type, want)
		}
	}
}
