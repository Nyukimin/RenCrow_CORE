package idlechat

import (
	"fmt"
	"math/rand"
	"strings"

	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
)

// pickRandom はスライスからn個をランダムに選択
func pickRandom(slice []string, n int) []string {
	if n >= len(slice) {
		// シャッフルして全て返す
		result := make([]string, len(slice))
		copy(result, slice)
		rand.Shuffle(len(result), func(i, j int) {
			result[i], result[j] = result[j], result[i]
		})
		return result
	}

	indices := rand.Perm(len(slice))[:n]
	result := make([]string, n)
	for i, idx := range indices {
		result[i] = slice[idx]
	}
	return result
}

func topicPromptFooter(movieMode bool) string {
	if movieMode {
		return `回答は映画タイトル妄想のお題だけを1行で出力してください。
- 必ず「〜ってどんな映画？」の形にする
- 実在映画名は使わない
- タイトル部分は短く印象的にする
- 質問文は最後の「どんな映画？」だけにする
- 40文字以内を目安に簡潔にする`
	}
	return `回答はお題だけを1行で出力してください。
- 質問文・感想文・呼びかけは禁止
- 「〜って面白いんじゃない？」のような会話調は禁止
- 体言止め、または「〜の関係」「〜を考える」のような題名調にする
- ジャンル名だけで終わらせず、人・物・場所・場面のどれかを1つ必ず入れる
	- 40文字以内を目安に簡潔にする`
}

func newsTopicPromptFooter() string {
	return `回答はお題だけを1行で出力してください。
- 質問文・感想文・呼びかけは禁止
- 「〜って面白いんじゃない？」のような会話調は禁止
- 見出しの単純な繰り返しではなく、論点・背景・影響のどれかが見える題名調にする
- 任意分野や別素材との掛け合わせ、架空映画化は禁止
- 40文字以内を目安に簡潔にする`
}

func pickTopicAnchor() topicAnchor {
	return topicAnchorPool[rand.Intn(len(topicAnchorPool))]
}

func buildSingleGenrePrompt(genre string, anchor topicAnchor, movieMode bool) string {
	bannedKeywords := extractBannedKeywords()
	return fmt.Sprintf(`以下のジャンルを深掘りした、興味深い話題を1つ提案してください。

ジャンル: %s
具体アンカー (%s): %s

要件:
- 深い洞察と新しい視点
- 会話が発展する具体性
- エンターテイメント性
- ジャンル名だけで終わらせず、具体アンカーを自然に織り込む

禁止事項:
- %s に関するトピックは避ける
- 「もし〜だったら」形式は使わない
- 教科書的な真面目な説明は避ける
- 直近トピックと類似した内容は避ける
- 抽象語だけで閉じた題名にしない

%s`, genre, anchor.Kind, anchor.Value, strings.Join(bannedKeywords, "、"), topicPromptFooter(movieMode))
}

func buildDoubleGenrePrompt(genres []string, anchor topicAnchor, movieMode bool) string {
	bannedKeywords := extractBannedKeywords()
	return fmt.Sprintf(`以下の2つのジャンルを組み合わせた、面白い話題を1つ提案してください。

ジャンル: %s × %s
具体アンカー (%s): %s

要件:
- 意外な組み合わせだが、深く考えると繋がりが見える
- 会話が深まる具体性
- 適度なエンターテイメント性
- 2ジャンルに具体アンカーを接続し、人・物・場所・場面が見える題名にする

禁止事項:
- %s に関するトピックは避ける
- 「もし〜だったら」形式は使わない
- 教科書的な真面目な組み合わせは避ける
- 直近トピックと類似した内容は避ける
- 抽象語だけで閉じた題名にしない

%s`, genres[0], genres[1], anchor.Kind, anchor.Value, strings.Join(bannedKeywords, "、"), topicPromptFooter(movieMode))
}

// generateSingleGenrePrompt は1ジャンル単体のプロンプトを生成
func generateSingleGenrePrompt(movieMode bool) (string, []string, topicAnchor) {
	genres := pickRandom(genrePool, 1)
	anchor := pickTopicAnchor()
	return buildSingleGenrePrompt(genres[0], anchor, movieMode), genres, anchor
}

// generateDoubleGenrePrompt は2ジャンル掛け合わせのプロンプトを生成
func generateDoubleGenrePrompt(movieMode bool) (string, []string, topicAnchor) {
	genres := pickRandom(genrePool, 2)
	anchor := pickTopicAnchor()
	return buildDoubleGenrePrompt(genres, anchor, movieMode), genres, anchor
}

// generateMoviePrompt は架空映画カテゴリのプロンプトを生成する。
func generateMoviePrompt() (string, []string, topicAnchor) {
	genres := pickRandom(genrePool, 1)
	anchor := pickTopicAnchor()
	return buildSingleGenrePrompt(genres[0], anchor, true), genres, anchor
}

// generateExternalPrompt は外部刺激を使ったプロンプトを生成
func generateExternalPrompt() (string, string, bool) {
	cache := getDailyCache()
	if cache == nil {
		return "", "external_seed_unavailable", false
	}

	var seed string
	if len(cache.WikipediaSeeds) > 0 {
		seed = cache.WikipediaSeeds[rand.Intn(len(cache.WikipediaSeeds))]
	} else {
		return "", "external_seed_unavailable", false
	}

	genre := pickRandom(genrePool, 1)[0]
	bannedKeywords := extractBannedKeywords()

	prompt := fmt.Sprintf(`以下の素材とジャンルを自然に接続した、会話向けのお題を1つ提案してください。

素材: %s
ジャンル: %s

要件:
- 素材そのものの具体性を残す
- ジャンルは混ぜるが、無理な連想ゲームにしない
- 人・物・場所・場面のどれかが見える題名にする
- 深く考察できる具体的な話題にする

禁止事項:
- %s に関するトピックは避ける
- 「もし〜だったら」形式は使わない
- 取得元、出典種別、ランダム取得、記事取得の話にしない
- 素材を「記事」「ページ」「ニュース」「検索結果」として扱わない

%s`, seed, genre, strings.Join(bannedKeywords, "、"), topicPromptFooter(false))

	return prompt, "Wikipedia:" + seed, true
}

// generateNewsPrompt はニュース見出しを純粋に深掘りするプロンプトを生成する。
func generateNewsPrompt() (string, string, bool) {
	cache := getDailyCache()
	topicSeed, ok := modulechat.SelectNewsTopicSeed(cache, rand.Int())
	if !ok || topicSeed.News == nil {
		return "", "news_seed_unavailable", false
	}
	seed := *topicSeed.News
	title := strings.TrimSpace(seed.Title)
	if title == "" {
		return "", "news_seed_unavailable", false
	}
	bannedKeywords := extractBannedKeywords()

	prompt := fmt.Sprintf(`以下のニュース見出しを1件だけ深掘りする、会話向けのお題を1つ提案してください。

ニュース見出し: %s

要件:
- このニュース自体の論点、背景、影響が見える
- 任意分野や別テーマを混ぜない
- 会話が発展する具体性を持たせる
- 見出しの言い換えだけで終わらせない

禁止事項:
- %s に関するトピックは避ける
- 「もし〜だったら」形式は使わない
- 別素材との掛け合わせにしない

%s`, title, strings.Join(bannedKeywords, "、"), newsTopicPromptFooter())

	return prompt, newsSeedSourceLabel(seed), true
}

func newsSeedSourceLabel(seed NewsSeed) string {
	return modulechat.NewsSeedSourceLabel(seed)
}

// extractBannedKeywords は頻出キーワードを抽出
func extractBannedKeywords() []string {
	return []string{
		"AI", "タイムマシン", "過去", "未来", "宇宙人",
		"もし", "だったら", "なら", "想像", "考えて",
	}
}
