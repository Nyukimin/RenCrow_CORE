package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

var characterPromptBlockNames = []string{
	"00_system.md",
	"10_policy.md",
	"20_scope.md",
	"30_knowledge.md",
}

func characterPromptMessages(prompt string) []llm.Message {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil
	}
	parts := strings.Split(prompt, "\n\n---\n\n")
	if len(parts) != len(characterPromptBlockNames) {
		return []llm.Message{{
			Role:    "system",
			Content: prompt,
			Type:    llm.PromptContextCharacter,
		}}
	}
	messages := make([]llm.Message, 0, len(parts))
	for index, part := range parts {
		messages = append(messages, llm.Message{
			Role:    "system",
			Content: strings.TrimSpace(part),
			Type:    llm.PromptContextCharacter,
			Metadata: map[string]string{
				"character_prompt_block": characterPromptBlockNames[index],
			},
		})
	}
	return messages
}

func stableRuntimeContextMessage(content string) []llm.Message {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	type heading struct {
		marker string
		kind   string
	}
	layouts := [][]heading{
		{
			{marker: "# Mio Agent Contract Index", kind: "agent_contract"},
			{marker: "## Routing and Handoff", kind: "interaction_contract"},
			{marker: "## Mio Tool Boundary", kind: "tool_boundary"},
		},
		{
			{marker: "# Shared Agent Control", kind: "agent_contract"},
			{marker: "## Routing", kind: "interaction_contract"},
			{marker: "## Tools", kind: "tool_boundary"},
		},
	}
	var headings []heading
	for _, layout := range layouts {
		if strings.HasPrefix(content, layout[0].marker) {
			headings = layout
			break
		}
	}
	if len(headings) == 0 {
		return []llm.Message{{Role: "system", Content: content, Type: llm.PromptContextStable, Metadata: map[string]string{"runtime_context_kind": "agent_contract"}}}
	}
	starts := make([]int, len(headings))
	for index, heading := range headings {
		starts[index] = strings.Index(content, heading.marker)
		if starts[index] < 0 || (index > 0 && starts[index] <= starts[index-1]) {
			return []llm.Message{{Role: "system", Content: content, Type: llm.PromptContextStable, Metadata: map[string]string{"runtime_context_kind": "agent_contract"}}}
		}
	}
	messages := make([]llm.Message, 0, len(headings))
	for index, heading := range headings {
		end := len(content)
		if index+1 < len(starts) {
			end = starts[index+1]
		}
		messages = append(messages, llm.Message{
			Role:    "system",
			Content: strings.TrimSpace(content[starts[index]:end]),
			Type:    llm.PromptContextStable,
			Metadata: map[string]string{
				"runtime_context_kind": heading.kind,
			},
		})
	}
	return messages
}

// assemblePromptContext is the single deterministic assembly path shared by
// every conversational character. Dynamic messages are grouped by semantic
// type; the current user message is always last.
func assemblePromptContext(characterPrompt, stableRuntimeContext string, dynamic []llm.Message, user llm.Message) []llm.Message {
	messages := make([]llm.Message, 0, 8+len(dynamic))
	messages = append(messages, characterPromptMessages(characterPrompt)...)
	messages = append(messages, stableRuntimeContextMessage(stableRuntimeContext)...)
	for _, message := range dynamic {
		if message.Type == llm.PromptContextVariable {
			continue
		}
		if message.Type == "" {
			message.Type = llm.PromptContextRecall
		}
		messages = append(messages, message)
	}
	for _, message := range dynamic {
		if message.Type == llm.PromptContextVariable {
			messages = append(messages, message)
		}
	}
	user.Type = llm.PromptContextUser
	messages = append(messages, user)
	return messages
}

func renderSystemMessages(messages []llm.Message) string {
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		if strings.EqualFold(strings.TrimSpace(message.Role), "system") && strings.TrimSpace(message.Content) != "" {
			parts = append(parts, strings.TrimSpace(message.Content))
		}
	}
	return strings.Join(parts, "\n\n")
}

func staticPromptHash(messages []llm.Message) string {
	hash := sha256.New()
	for _, message := range messages {
		if message.Type != llm.PromptContextCharacter && message.Type != llm.PromptContextStable {
			continue
		}
		hash.Write([]byte(message.Type))
		hash.Write([]byte{0})
		hash.Write([]byte(message.Metadata["character_prompt_block"]))
		hash.Write([]byte{0})
		hash.Write([]byte(message.Content))
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}
