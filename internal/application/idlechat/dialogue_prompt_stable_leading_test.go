package idlechat

import (
	"context"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
)

// 先頭の system は人格・セッション文脈・語彙メモで、同じ会話のターン間では変わらない
// （Character）。対話方針は直前の発話とターン計画を含み、ターンごとに変わる。これを先頭へ
// 連結すると Gateway は先頭を固定できず、prefix cache が毎ターン崩れる。Variable の
// 別 message にして、Gateway が最後の user の封筒へ移せるようにする。
func TestGenerateResponseKeepsLeadingSystemStableAndTypesDialogueOrder(t *testing.T) {
	provider := &capturingIdleProvider{response: "郵便配達員が古書店の棚で宛先不明の手紙を見つける入口がよさそう。しろなら、誰が隠したと思う？"}
	o := NewIdleChatOrchestrator(provider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")
	nightGateAllowOrchestrator(t, o)
	sessionContext := "SESSION-MARK-A"
	glossary := "GLOSSARY-MARK-あおぞら"
	director := NewDialogueDirector(DefaultDialogueInterestingnessConfig())
	topicResult := TopicGenerationResult{
		Topic: "郵便と古書店", Category: TopicCategoryDouble, Strategy: "double",
		InterestingnessAxis: "接続", OpeningHook: "成長を待つ設計の共通点を拾う", Avoid: "列挙だけで終わらせない",
	}
	plan := director.BuildArcPlan(topicResult)
	state := director.NewArcState("idle-stable-leading", topicResult, plan)
	o.dialogueConfig = DefaultDialogueInterestingnessConfig()
	o.SetRecentTopicProvider(func(context.Context, int) ([]string, error) { return []string{glossary}, nil })
	o.mu.Lock()
	o.sessionContext = sessionContext
	o.currentTopicResult = &topicResult
	o.currentDialoguePlan = &plan
	o.currentDialogueState = &state
	o.mu.Unlock()

	var leading []llm.Message
	var dialogue []string
	for index, turn := range []int{0, 4} {
		before := len(provider.requests)
		if _, err := o.generateResponse("mio", "shiro", "idle-stable-leading", turn, turn, "郵便と古書店"); err != nil {
			t.Fatalf("generateResponse(turn %d) error = %v", turn, err)
		}
		if len(provider.requests) == before {
			t.Fatalf("generateResponse(turn %d) sent no request", turn)
		}
		messages := provider.requests[before].Messages
		leading = append(leading, messages[0])

		for position, message := range messages {
			if message.Role == "system" && message.Type == "" {
				t.Fatalf("turn %d message %d is a system message without a Type: %q", turn, position, message.Content)
			}
		}
		if messages[0].Role != "system" || messages[0].Type != llm.PromptContextCharacter {
			t.Fatalf("turn %d leading message = role %q type %q, want a Character system message", turn, messages[0].Role, messages[0].Type)
		}
		for _, persistent := range []string{sessionContext, glossary} {
			if !strings.Contains(messages[0].Content, persistent) {
				t.Fatalf("turn %d leading system message lost persistent context %q:\n%s", turn, persistent, messages[0].Content)
			}
		}
		var found string
		for _, message := range messages[1:] {
			if message.Role == "system" && message.Type == llm.PromptContextVariable && message.Metadata["runtime_context_kind"] == "idlechat_dialogue" {
				found = message.Content
			}
		}
		if found == "" {
			t.Fatalf("turn %d has no idlechat_dialogue Variable message: %+v", turn, messages)
		}
		if strings.Contains(messages[0].Content, found) {
			t.Fatalf("turn %d leading system message contains the per-turn dialogue prompt", turn)
		}
		dialogue = append(dialogue, found)
		_ = index
	}
	if dialogue[0] == dialogue[1] {
		t.Fatal("test setup: the dialogue prompt did not change between turns, so it proves nothing")
	}
	if leading[0].Content != leading[1].Content {
		t.Fatalf("leading system message changed between turns:\n%s\n----\n%s", leading[0].Content, leading[1].Content)
	}
}
