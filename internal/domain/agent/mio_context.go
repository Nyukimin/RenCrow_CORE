package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	domainnews "github.com/Nyukimin/RenCrow_CORE/internal/domain/newsbrief"
)

type dailyNewsBriefContextKey struct{}

// WithDailyNewsBrief supplies a prepared factual morning brief to Mio without
// replacing the user's original message or conversation turn.
func WithDailyNewsBrief(ctx context.Context, brief domainnews.DailyNewsBrief) context.Context {
	return context.WithValue(ctx, dailyNewsBriefContextKey{}, brief)
}

func dailyNewsBriefFromContext(ctx context.Context) (domainnews.DailyNewsBrief, bool) {
	if ctx == nil {
		return domainnews.DailyNewsBrief{}, false
	}
	brief, ok := ctx.Value(dailyNewsBriefContextKey{}).(domainnews.DailyNewsBrief)
	return brief, ok
}

func dailyNewsBriefSystemPrompt(brief domainnews.DailyNewsBrief) string {
	var b strings.Builder
	if brief.Source == domainnews.SourceLiveSearch {
		b.WriteString("これはニュース収集Workerが今取得したLiveNewsSearchの結果です。ユーザーの質問には、この資料を第一の根拠として答えてください。\n")
	} else {
		b.WriteString("これは04:00 JSTに準備されたDailyNewsBriefです。ユーザーの質問には、この資料を第一の根拠として答えてください。\n")
	}
	fetchedAt := "不明"
	if !brief.FetchedAt.IsZero() {
		fetchedAt = brief.FetchedAt.In(time.FixedZone("JST", 9*60*60)).Format("2006-01-02 15:04 JST")
	}
	fmt.Fprintf(&b, "対象日: %s / 取得時刻: %s / 状態: %s\n", brief.Date, fetchedAt, brief.Status)
	b.WriteString("指示:\n")
	b.WriteString("- 資料にない事実を推測しない。資料があるのに「検索できない」「検索できないので答えられない」と拒否しない。\n")
	b.WriteString("- 初回の朝刊依頼では主要項目を番号付きで簡潔に示し、各項目のsourceを付ける。\n")
	b.WriteString("- ユーザーが「2番を詳しく」のように番号またはitem idを指定したら、その記事だけを深掘りする。\n")
	if brief.Source == domainnews.SourceLiveSearch {
		b.WriteString("- LiveNewsSearchの取得時刻を示し、04:00の朝刊キャッシュとは混同しない。\n")
	} else {
		b.WriteString("- 04:00時点の準備済み情報であることを明示し、リアルタイム速報とは呼ばない。\n")
	}
	b.WriteString("- SNS由来の話題が含まれる場合は、確認済み事実と混同しない。\n")
	b.WriteString("\n記事一覧:\n")
	maxItems := len(brief.Items)
	if maxItems > 8 {
		maxItems = 8
	}
	for index, item := range brief.Items[:maxItems] {
		fmt.Fprintf(&b, "[%d] id=%s title=%s category=%s source=%s\n", index+1, item.ID, item.Title, item.Category, item.Source)
		if summary := firstNonEmptyBriefText(item.Summary, item.TranslatedBody); summary != "" {
			fmt.Fprintf(&b, "要約: %s\n", truncateBriefText(summary, 700))
		}
		if perspective := strings.TrimSpace(item.Perspective); perspective != "" {
			fmt.Fprintf(&b, "Shiroの見解: %s\n", truncateBriefText(perspective, 500))
		}
		if item.URL != "" {
			fmt.Fprintf(&b, "URL: %s\n", item.URL)
		}
	}
	if len(brief.Items) > maxItems {
		fmt.Fprintf(&b, "（全%d件のうち先頭%d件を表示。続きはユーザーが「ニュース一覧の続き」と指定した場合に扱う）\n", len(brief.Items), maxItems)
	}
	return b.String()
}

func firstNonEmptyBriefText(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func truncateBriefText(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if limit <= 0 || len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "…"
}
