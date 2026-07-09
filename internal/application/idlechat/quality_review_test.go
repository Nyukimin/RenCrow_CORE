package idlechat

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
)

type queuedQualityProvider struct {
	responses []string
	requests  []llm.GenerateRequest
}

func (p *queuedQualityProvider) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.requests = append(p.requests, req)
	if len(p.responses) == 0 {
		return llm.GenerateResponse{Content: "ok"}, nil
	}
	out := p.responses[0]
	p.responses = p.responses[1:]
	return llm.GenerateResponse{Content: out}, nil
}

func (p *queuedQualityProvider) Name() string {
	return "queued-quality"
}

func TestSaveSummaryReviewsQualityButDoesNotInjectPromptGuidance(t *testing.T) {
	provider := &queuedQualityProvider{responses: []string{
		"会話の要約です。",
		"QUALITY: fail\nBORING_CAUSE: テンプレ反復で聞き手の楽しみが落ちた\nINTEREST_HOOK: 猫市長が市役所の机で魚の予算を隠す選択\nMISSED_TURN: 制度論に逃げず、誰が困るかを出せた\nPROMPT_FIX: INTEREST_HOOKを1つ選び、誰かが損をする選択か隠し事が露出する瞬間に変える。2文以内で余白を残す。",
	}}
	o := NewIdleChatOrchestrator(provider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")
	transcript := []string{
		"mio: もし猫が市長だったら面白いよね。",
		"shiro: もし市長なら予算配分が変わりますね。",
		"mio: もし猫なら会議も変わるかも。",
		"shiro: もしそうなら制度設計が重要です。",
	}

	summary := o.saveSummary("s1", "猫市長", TopicStrategy("manual"), transcript, time.Now(), time.Now(), len(transcript), true, "template_repeat")

	if !strings.Contains(summary, "注記: テンプレ反復で打ち切り") {
		t.Fatalf("summary should keep loop note, got %q", summary)
	}
	if len(o.history) != 1 {
		t.Fatalf("history len = %d, want 1", len(o.history))
	}
	record := o.history[0]
	if !strings.Contains(record.QualityReview, "QUALITY: fail") {
		t.Fatalf("quality review not recorded: %q", record.QualityReview)
	}
	if !strings.Contains(record.QualityReview, "INTEREST_HOOK") {
		t.Fatalf("interest hook not recorded: %q", record.QualityReview)
	}
	if !strings.Contains(record.PromptGuidance, "INTEREST_HOOK") || !strings.Contains(record.PromptGuidance, "2文以内") {
		t.Fatalf("prompt guidance not recorded: %q", record.PromptGuidance)
	}
	if got := o.getSystemPrompt("mio"); strings.Contains(got, "聞き手体験レビュー") || strings.Contains(got, "INTEREST_HOOK") || strings.Contains(got, "2文以内") {
		t.Fatalf("system prompt should not include review guidance: %q", got)
	}
}

func TestHeuristicQualityReviewProducesInterestGuidanceAndLengthControl(t *testing.T) {
	transcript := []string{
		"mio: 雨の文化祭前夜、実行委員長が体育館の鍵を隠したら面白いよね。",
		"shiro: その設定は有効ですね。文化祭運営の構造的な矛盾を検証できます。",
		"mio: 雨の文化祭前夜、実行委員長が誰を守ったのか気になる。",
		"shiro: その設定は有効ですね。責任の所在を検証できます。",
	}

	review, guide := heuristicQualityReview("雨季の熱帯の祝祭と文化祭前夜の緊張感", "manual", transcript, "", "template_repeat")

	for _, want := range []string{"BORING_CAUSE", "INTEREST_HOOK", "MISSED_TURN", "PROMPT_FIX", "LENGTH_CONTROL"} {
		if !strings.Contains(review, want) {
			t.Fatalf("review missing %s:\n%s", want, review)
		}
	}
	for _, want := range []string{"INTEREST_HOOK", "2文以内", "余白"} {
		if !strings.Contains(guide, want) {
			t.Fatalf("guide missing %s:\n%s", want, guide)
		}
	}
}

func TestLoopReasonLabelEmptyDoesNotInventCutoff(t *testing.T) {
	if got := loopReasonLabel(""); got != "" {
		t.Fatalf("loopReasonLabel(empty) = %q, want empty", got)
	}
}

func TestHeuristicQualityReviewCompletedSessionDoesNotInventCutoff(t *testing.T) {
	transcript := []string{
		"mio: 映写機の鍵が机に残ってるの、気になるよね。",
		"shiro: その鍵は、誰が最後に上映室へ入ったかを示している。",
		"mio: じゃあ、その人が見せたくなかった場面があるのかも。",
		"shiro: 上映室の扉が閉じたままなら、隠されたフィルムが残っている。",
	}

	review, _ := heuristicQualityReview("映画館に残った鍵", "manual", transcript, "", "")

	if strings.Contains(review, "打ち切り") {
		t.Fatalf("completed session heuristic review must not invent cutoff:\n%s", review)
	}
}

func TestPromptGuidesFromHistoryLoadsRecentGuidance(t *testing.T) {
	history := []SessionSummary{
		{PromptGuidance: "古い補正"},
		{PromptGuidance: "新しい補正"},
		{PromptGuidance: "新しい補正"},
	}
	got := promptGuidesFromHistory(history, 2)
	if len(got) != 2 || got[0] != "古い補正" || got[1] != "新しい補正" {
		t.Fatalf("unexpected guides: %#v", got)
	}
}

func TestNormalizeQualityReviewStripsEnglishThinkPrefix(t *testing.T) {
	raw := "Okay, let's tackle this problem.\nThe user provided a log.\nQUALITY: fail\nBORING_CAUSE: 返答崩れで終了した\nINTEREST_HOOK: 錆びた鍵と秘密の部屋\nMISSED_TURN: 鍵の意味を掘る前に抽象化した\nPROMPT_FIX: INTEREST_HOOKを場面と選択に変換する。\nLENGTH_CONTROL: 2文以内。"
	got := normalizeQualityReview(raw)
	if strings.Contains(strings.ToLower(got), "okay, let's") || strings.Contains(strings.ToLower(got), "the user provided") {
		t.Fatalf("normalizeQualityReview did not strip think prefix: %q", got)
	}
	if !strings.HasPrefix(got, "QUALITY: fail") {
		t.Fatalf("normalized review should start with QUALITY: %q", got)
	}
}

func TestReviewSessionEndRejectsFalseCutoffForCompletedSession(t *testing.T) {
	provider := &queuedQualityProvider{responses: []string{
		"QUALITY: fail\nBORING_CAUSE: 打ち切り注記: 反復検知で打ち切り\nINTEREST_HOOK: 映写機の鍵\nMISSED_TURN: 反復検知で打ち切りの直前で鍵を開けられた\nPROMPT_FIX: INTEREST_HOOKを場面と選択に変換する。\nLENGTH_CONTROL: 2文以内。",
	}}
	o := NewIdleChatOrchestrator(provider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")
	transcript := []string{
		"mio: 映写機の鍵が机に残ってるの、気になるよね。",
		"shiro: その鍵は、誰が最後に上映室へ入ったかを示している。",
		"mio: じゃあ、その人が見せたくなかった場面があるのかも。",
		"shiro: 上映室の扉が閉じたままなら、隠されたフィルムが残っている。",
	}

	review, _ := o.reviewSessionEnd("映画館に残った鍵", "manual", transcript, "要約です。", "")

	if strings.Contains(review, "打ち切り") {
		t.Fatalf("completed session review must not keep false cutoff wording:\n%s", review)
	}
	if len(provider.requests) == 0 {
		t.Fatal("expected quality review request")
	}
	prompt := provider.requests[0].Messages[len(provider.requests[0].Messages)-1].Content
	if !strings.Contains(prompt, "打ち切り理由: なし") {
		t.Fatalf("quality review prompt should make non-cutoff explicit:\n%s", prompt)
	}
}
