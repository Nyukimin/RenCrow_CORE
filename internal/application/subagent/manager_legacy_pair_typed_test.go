package subagent

import (
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

// PromptContext が無いときの従来の組（system + user）も型付きで渡す。型が無いと
// Gateway は先頭固定の配置を適用できず（instruction_without_block）、時刻などの
// Variable が先頭 system へ畳み込まれて prefix cache が毎ターン崩れる。
func TestSubagentChatMessagesLegacyPairIsTyped(t *testing.T) {
	for name, task := range map[string]agent.SubagentTask{
		"default system prompt": {Instruction: "do it"},
		"custom system prompt":  {Instruction: "do it", SystemPrompt: "custom prompt"},
	} {
		t.Run(name, func(t *testing.T) {
			messages := subagentChatMessages(task)
			if len(messages) != 2 {
				t.Fatalf("messages = %#v, want system and user", messages)
			}
			if messages[0].Role != "system" || messages[0].Type != llm.PromptContextCharacter {
				t.Fatalf("leading message = %#v, want a Character system message", messages[0])
			}
			if task.SystemPrompt != "" && messages[0].Content != task.SystemPrompt {
				t.Fatalf("leading content = %q, want the task system prompt", messages[0].Content)
			}
			if task.SystemPrompt == "" && messages[0].Content != defaultSystemPrompt {
				t.Fatalf("leading content = %q, want the default system prompt", messages[0].Content)
			}
			if messages[1].Role != "user" || messages[1].Type != llm.PromptContextUser || messages[1].Content != "do it" {
				t.Fatalf("second message = %#v, want the instruction as a User message", messages[1])
			}
		})
	}
}
