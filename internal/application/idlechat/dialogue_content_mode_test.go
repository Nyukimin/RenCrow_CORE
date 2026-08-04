package idlechat

import (
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
)

func TestClassifyDialogueContentPolicyUsesSeriousPrecedence(t *testing.T) {
	tests := []struct {
		name   string
		result TopicGenerationResult
		mode   DialogueContentMode
	}{
		{
			name:   "war",
			result: TopicGenerationResult{Topic: "戦争で避難を続ける家族と停戦交渉"},
			mode:   DialogueContentModeSerious,
		},
		{
			name: "disaster from source context",
			result: TopicGenerationResult{
				Topic: "現地支援をどこから立て直すか",
				Seed: TopicSeed{News: &NewsSeed{
					Title:   "大地震後の支援",
					Summary: "被災地では避難と救助が続いている。",
				}},
			},
			mode: DialogueContentModeSerious,
		},
		{
			name:   "politics",
			result: TopicGenerationResult{Topic: "選挙制度を変える政策への賛否"},
			mode:   DialogueContentModeAssertive,
		},
		{
			name: "political forecast domain",
			result: TopicGenerationResult{
				Topic: "自治体の意思決定をAIが補助する未来",
				Seed:  TopicSeed{ForecastDomain: "政治"},
			},
			mode: DialogueContentModeAssertive,
		},
		{
			name:   "ideology",
			result: TopicGenerationResult{Topic: "資本主義と社会主義の価値観"},
			mode:   DialogueContentModeAssertive,
		},
		{
			name:   "ordinary",
			result: TopicGenerationResult{Topic: "休日の散歩と帰り道のごはん"},
			mode:   DialogueContentModeFree,
		},
		{
			name:   "software maintenance is not political conservatism",
			result: TopicGenerationResult{Topic: "ソフトウェア保守と障害対応"},
			mode:   DialogueContentModeFree,
		},
		{
			name:   "caption design is not terrorism",
			result: TopicGenerationResult{Topic: "映画予告のテロップデザイン"},
			mode:   DialogueContentModeFree,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyDialogueContentPolicy(tt.result)
			if got.Mode != tt.mode {
				t.Fatalf("mode = %q, want %q; reasons=%v", got.Mode, tt.mode, got.Reasons)
			}
			if len(got.Reasons) == 0 {
				t.Fatal("classification reasons must be observable")
			}
		})
	}
}

func TestBuildDialoguePromptIncludesSeriousContentPolicy(t *testing.T) {
	director := NewDialogueDirector(DefaultDialogueInterestingnessConfig())
	result := TopicGenerationResult{
		Topic:    "大地震の被災地で続く避難と支援",
		Category: TopicCategoryNews,
		Strategy: "news",
	}
	plan := director.BuildArcPlan(result)
	state := director.NewArcState("idle-serious", result, plan)
	prompt := BuildDialoguePrompt(DialoguePromptInput{
		Result:   result,
		Plan:     plan,
		State:    state,
		TurnPlan: plan.TurnPlans[0],
		Speaker:  "mio",
	})

	for _, want := range []string{"content_mode: serious", "口調はMioとShiro", "茶化さず", "被害を矮小化"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("serious prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestBuildDialoguePromptAllowsAssertivePoliticalOpinion(t *testing.T) {
	director := NewDialogueDirector(DefaultDialogueInterestingnessConfig())
	result := TopicGenerationResult{Topic: "選挙制度を変える政策への賛否", Category: TopicCategoryNews, Strategy: "news"}
	plan := director.BuildArcPlan(result)
	prompt := BuildDialoguePrompt(DialoguePromptInput{
		Result: result, Plan: plan, State: director.NewArcState("idle-assertive", result, plan), TurnPlan: plan.TurnPlans[0], Speaker: "shiro",
	})

	for _, want := range []string{"content_mode: assertive", "強い意見", "率直な批判", "人工的に中立化せず"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("assertive prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestCompactRetryPreservesSeriousContentPolicy(t *testing.T) {
	messages := buildIdleCompactRetryMessagesWithPolicy(
		"mio",
		"大地震後の避難と被災地支援",
		"避難所の生活が長引いている。",
		"内容を修復する",
		DialogueContentPolicy{Mode: DialogueContentModeSerious, Reasons: []string{"disaster_or_human_harm"}},
	)
	joined := messages[0].Content + "\n" + messages[1].Content
	for _, want := range []string{"content_mode: serious", "内容は真面目", "被害や喪失を茶化さず"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("serious retry missing %q:\n%s", want, joined)
		}
	}
}

func TestDialogueQualityCheckerRejectsContentModeViolations(t *testing.T) {
	checker := NewDialogueQualityChecker(DefaultDialogueInterestingnessConfig())
	tests := []struct {
		name      string
		mode      DialogueContentMode
		utterance string
	}{
		{
			name:      "serious trivialization",
			mode:      DialogueContentModeSerious,
			utterance: "その被災地の話、笑い話のネタにすれば盛り上がりそう。",
		},
		{
			name:      "assertive forced neutrality",
			mode:      DialogueContentModeAssertive,
			utterance: "政治の話なので意見は控えて、どちらも正しいことにしましょう。",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := checker.Check(DialogueQualityInput{
				Category:    TopicCategoryNews,
				ContentMode: tt.mode,
				Utterance:   tt.utterance,
				State:       DialogueArcState{Category: TopicCategoryNews, ContentMode: tt.mode},
				TurnPlan:    DialogueTurnPlan{TurnIndex: 1},
			})
			if result.OK || !containsDialogueReason(result.Reasons, DialogueContentModeViolation) {
				t.Fatalf("content mode violation was not rejected: %#v", result)
			}
		})
	}
}

func TestGenerateResponseSeriousModeSkipsFunCandidate(t *testing.T) {
	provider := &capturingIdleProvider{response: "避難が続く人の生活を先に見ないと、この地震の被害は数字だけでは捉えられないね。分からない点は分からないまま話したい。"}
	o := NewIdleChatOrchestrator(provider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")

	got, err := o.generateResponse("mio", "shiro", "idle-serious-mode", 0, 0, "大地震後の避難と被災地支援")
	if err != nil {
		t.Fatalf("generateResponse() error = %v", err)
	}
	if got == "" {
		t.Fatal("serious response must not be empty")
	}
	if len(provider.requests) != 1 {
		t.Fatalf("serious Mio turn must skip fun candidate; requests=%d, want 1", len(provider.requests))
	}
	joined := ""
	for _, message := range provider.requests[0].Messages {
		joined += "\n" + message.Content
	}
	if !strings.Contains(joined, "content_mode: serious") || strings.Contains(joined, "読者の楽しみ") {
		t.Fatalf("serious request has wrong content policy:\n%s", joined)
	}
}

func TestIdleAssertiveScorePrefersDirectOpinionOverForcedNeutrality(t *testing.T) {
	policy := DialogueContentPolicy{Mode: DialogueContentModeAssertive, Reasons: []string{"politics_or_public_policy"}}
	neutral := "政治の話なので意見は控えて、どちらも正しいことにしましょう。"
	direct := "私はこの政策に反対だと思う。現場への影響を説明せず負担だけ増やす制度は変えるべきだ。"

	neutralScore := idleAlternativeScorePercent(neutral, "", "", "選挙後の政策", policy)
	directScore := idleAlternativeScorePercent(direct, "", "", "選挙後の政策", policy)
	if directScore <= neutralScore {
		t.Fatalf("assertive score direct=%d, neutral=%d", directScore, neutralScore)
	}
}
