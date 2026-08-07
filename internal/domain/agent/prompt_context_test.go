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
