package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
)

func TestMioAgentChatInjectsRuntimePromptContext(t *testing.T) {
	var captured llm.GenerateRequest
	provider := &mockLLMProvider{generateFunc: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
		captured = req
		return llm.GenerateResponse{Content: "確認しました。"}, nil
	}}
	mio := NewMioAgent(provider, nil, nil, nil, nil, nil).
		WithSystemPrompt("Mio fixed persona").
		WithAgentContractsPrompt("## Agent Contract Index\n- shiro: execution and evidence\n- kuro: root cause analysis").
		WithRecentExpressionHistory(MioExpressionHistory{
			Openings:    []string{"前回の書き出し"},
			Evaluations: []string{"かなり自然"},
			Connectors:  []string{"そのうえで"},
			Closings:    []string{"ここまで確認できます"},
		})

	request := newAgentTurnInput(t, "実行結果を確認して", "viewer", "chat-1").WithRoute(routing.RouteOPS)
	if _, err := mio.Chat(context.Background(), request); err != nil {
		t.Fatalf("Chat() error = %v", err)
	}

	joined := joinPromptMessages(captured.Messages)
	for _, want := range []string{
		"Mio fixed persona",
		"Agent Contract Index",
		"tone: LOW",
		"前回の書き出し",
		"最近の表現履歴",
		"実行していない処理を完了と表現しない",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("runtime prompt missing %q:\n%s", want, joined)
		}
	}
	lastRank := -1
	ranks := map[llm.PromptContextType]int{
		llm.PromptContextCharacter: 0,
		llm.PromptContextStable:    1,
		llm.PromptContextRecall:    2,
		llm.PromptContextVariable:  3,
		llm.PromptContextUser:      4,
	}
	for index, message := range captured.Messages {
		rank, ok := ranks[message.Type]
		if !ok {
			t.Fatalf("message %d lacks prompt context type: %#v", index, message)
		}
		if rank < lastRank {
			t.Fatalf("prompt context order regressed at %d: %#v", index, captured.Messages)
		}
		lastRank = rank
	}
}

func TestMioAgentChatRemembersExpressionHistoryForNextTurn(t *testing.T) {
	call := 0
	provider := &mockLLMProvider{generateFunc: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
		call++
		if call == 1 {
			return llm.GenerateResponse{Content: "ここから見ます。前提がつながりました。"}, nil
		}
		if !strings.Contains(joinPromptMessages(req.Messages), "ここから見ます") {
			t.Fatalf("second turn did not receive recent expression history:\n%s", joinPromptMessages(req.Messages))
		}
		return llm.GenerateResponse{Content: "今回は別の入り方にします。"}, nil
	}}
	mio := NewMioAgent(provider, nil, nil, nil, nil, nil).WithSystemPrompt("persona")
	for i, message := range []string{"最初の相談", "次の相談"} {
		if _, err := mio.Chat(context.Background(), newAgentTurnInput(t, message, "viewer", "chat-1")); err != nil {
			t.Fatalf("turn %d Chat() error = %v", i+1, err)
		}
	}
}

func TestMioExpressionHistoryPromptIsSmallAndCapped(t *testing.T) {
	history := MioExpressionHistory{}
	for i := 0; i < 8; i++ {
		history.Openings = append(history.Openings, fmt.Sprintf("opening-%d", i))
		history.Evaluations = append(history.Evaluations, fmt.Sprintf("evaluation-%d", i))
		history.Connectors = append(history.Connectors, fmt.Sprintf("connector-%d", i))
		history.Closings = append(history.Closings, fmt.Sprintf("closing-%d", i))
	}
	prompt := history.Prompt()
	if strings.Count(prompt, "opening-") != 3 || strings.Count(prompt, "evaluation-") != 3 || strings.Count(prompt, "connector-") != 3 || strings.Count(prompt, "closing-") != 3 {
		t.Fatalf("history should be capped at three entries per category:\n%s", prompt)
	}
	if len([]rune(prompt)) > 900 {
		t.Fatalf("history prompt is too large: %d runes", len([]rune(prompt)))
	}
}

func TestMioExpressionHistoryExcludesDegenerateGeneratedWording(t *testing.T) {
	history := MioExpressionHistory{
		Openings: []string{"通常の書き出し", "いって、いって、こんにちは"},
		Closings: []string{"安全に確認します", "0000000000000000"},
	}
	prompt := history.Prompt()
	if strings.Contains(prompt, "いって、いって") || strings.Contains(prompt, "00000000") {
		t.Fatalf("degenerate wording leaked into expression history:\n%s", prompt)
	}
	if !strings.Contains(prompt, "通常の書き出し") || !strings.Contains(prompt, "安全に確認します") {
		t.Fatalf("healthy wording was lost:\n%s", prompt)
	}
}

func TestMioToneTracksConversationRisk(t *testing.T) {
	cases := []struct {
		name  string
		input conversation.TurnInput
		want  string
	}{
		{name: "normal chat", input: newAgentTurnInput(t, "設計を相談したい", "viewer", "chat-1"), want: "MEDIUM"},
		{name: "ops", input: newAgentTurnInput(t, "再起動を確認して", "viewer", "chat-1").WithRoute(routing.RouteOPS), want: "LOW"},
		{name: "security", input: newAgentTurnInput(t, "認証情報の扱いを相談したい", "viewer", "chat-1"), want: "LOW"},
		{name: "idle", input: newAgentTurnInput(t, "最近気になること", "idlechat", "idle-1"), want: "HIGH"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mioToneForTask(tc.input); got != tc.want {
				t.Fatalf("mioToneForTask() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMioRuntimeContextIsNotInjectedForAnotherViewerRecipient(t *testing.T) {
	provider := &mockLLMProvider{generateFunc: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
		for _, message := range req.Messages {
			if strings.Contains(message.Content, "Runtime-injected Mio context") {
				t.Fatalf("Mio context leaked into another recipient: %#v", req.Messages)
			}
		}
		return llm.GenerateResponse{Content: "Shiroです"}, nil
	}}
	mio := NewMioAgent(provider, nil, nil, nil, nil, nil).
		WithSystemPrompt("Mio fixed persona").
		WithViewerRecipientPrompts(map[string]string{"shiro": "Shiro fixed persona"})
	request := newAgentTurnInput(t, "確認して", "viewer", "chat-1").WithViewerRecipient("shiro")
	if _, err := mio.Chat(context.Background(), request); err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
}

func joinPromptMessages(messages []llm.Message) string {
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		parts = append(parts, message.Content)
	}
	return strings.Join(parts, "\n\n")
}
