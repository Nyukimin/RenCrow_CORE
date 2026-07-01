package chat

import (
	"encoding/json"
	"fmt"
	"strings"
)

func RenderTopicPromptPlaceholders(template string, values map[string]string) string {
	out := template
	for key, value := range values {
		out = strings.ReplaceAll(out, "{"+key+"}", value)
	}
	return out
}

func BuildTopicGenerationPrompt(category TopicCategory, seed TopicSeed, recent []RecentTopic, candidateCount, attempt int, lastErr error) (string, error) {
	if candidateCount <= 0 {
		candidateCount = 5
	}
	seedJSON, _ := json.MarshalIndent(seed, "", "  ")
	recentJSON, _ := json.MarshalIndent(recent, "", "  ")
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`あなたは RenCrow IdleChat の topic generator です。

目的:
Mio と Shiro が自律的に雑談したとき、12ターン程度で自然に深まり、
聞いているユーザーが「続きを聞きたい」と感じる topic 候補を生成してください。

重要:
- あなたは topic 候補を生成するだけです。
- カテゴリ判定、最終採用、Viewer表示、TTS読み上げ、ログ記録は実装コードが担当します。
- 出力は JSON のみ。
- candidates 配列に %d 件を返してください。
- candidates は topic 文字列だけの配列にしてください。
- topic は1行。
- topic には説明文を含めない。
- topic にカテゴリ名、内部 strategy、provider 名、取得経路、seed ID を出さない。
- 抽象語だけで終わらせない。
- 人物・物・場所・場面・制度・出来事のうち、可能な限り1つ以上を入れる。
- Mio と Shiro の見方が分かれそうな余地を残す。
- 「面白い」「不思議な」「深い」などの評価語でごまかさない。
- recent_topics と似すぎない。
- 既存の基準お題をそのまま出力してはいけない。

入力:
category: %s

seed:
%s

recent_topics:
%s
`, candidateCount, category, string(seedJSON), string(recentJSON)))

	sb.WriteString("\nカテゴリ別条件:\n")
	sb.WriteString(TopicCategoryGenerationRules(category))
	if attempt >= 2 {
		sb.WriteString(fmt.Sprintf("\n\n再生成条件:\n- attempt=%d。\n- 前回失敗理由を避ける。\n", attempt))
		if lastErr != nil {
			sb.WriteString("- 前回失敗理由: ")
			sb.WriteString(strings.TrimSpace(lastErr.Error()))
			sb.WriteString("\n")
		}
	}
	sb.WriteString(`

出力形式:
{
  "candidates": [
    "..."
  ]
}`)
	return sb.String(), nil
}

func BuildTopicJudgePrompt(category TopicCategory, seed TopicSeed, recent []RecentTopic, candidates []TopicCandidate) (string, error) {
	seedJSON, _ := json.MarshalIndent(seed, "", "  ")
	recentJSON, _ := json.MarshalIndent(recent, "", "  ")
	candidatesJSON, _ := json.MarshalIndent(candidates, "", "  ")
	return fmt.Sprintf(`あなたは RenCrow IdleChat の topic judge です。

目的:
候補 topic の中から、Mio と Shiro が12ターン程度で自然に会話を深められ、
聞いているユーザーが続きを聞きたくなるものを1つ選んでください。

重要:
- あなたは採点と winner 選択だけを行います。
- topic を新規生成してはいけません。
- 候補に存在しない topic を winner_topic にしてはいけません。
- Viewer 表示、TTS、ログ記録は実装コードが担当します。
- 仕様違反の候補は、面白そうでも safety を低くしてください。

評価基準:
1. category_fit: カテゴリ仕様に合っているか。
2. concreteness: 人物・物・場所・場面・制度・出来事が見えるか。
3. curiosity: 聞いた瞬間に「どういうこと？」が生まれるか。
4. conversation_potential: Mio と Shiro の見方が分かれ、12ターン程度で自然に展開できそうか。
5. axis_strength: single=観察、double=接続、external=偶然の意味化、movie=共同妄想、news=現実の影響、forecast=変化の分岐、story=視点反転。
6. novelty: recent_topics と似すぎていないか。
7. safety: 契約違反がないか。

スコアは各0〜5。total は7項目の単純合計。

category: %s

seed:
%s

recent_topics:
%s

candidates:
%s

出力 JSON:
{
  "winner_topic": "...",
  "scores": [
    {
      "topic": "...",
      "category_fit": 0,
      "concreteness": 0,
      "curiosity": 0,
      "conversation_potential": 0,
      "axis_strength": 0,
      "novelty": 0,
      "safety": 0,
      "total": 0,
      "reason": "短く"
    }
  ],
  "reject_reason_summary": "落選候補に共通する弱さ"
}`, category, string(seedJSON), string(recentJSON), string(candidatesJSON)), nil
}

func TopicCategoryGenerationRules(category TopicCategory) string {
	switch category {
	case TopicCategorySingle:
		return `category = single
面白さ: 観察。ひとつのジャンルを、具体的な人物・物・場所・場面から深掘りする。
必須:
- seed.genre_1 を中心にする。
- 1ジャンルだけを扱う。
- 人物、物、場所、場面のうち2つ以上を入れる。
- 大きな社会論ではなく、小さな場面から始まる題にする。`
	case TopicCategoryDouble:
		return `category = double
面白さ: 接続。一見離れた2領域の間に、同じ構造・制約・悩みを見つける。
必須:
- seed.genre_1 と seed.genre_2 の両方を使う。
- 表面的な共通点ではなく、共通する仕組みが見える題にする。
- 「AとB」だけで終わらせず、「何が共通するのか」まで題名に含める。`
	case TopicCategoryExternal:
		return `category = external
面白さ: 偶然の意味化。外から来た素材とジャンルを自然に接続する。
必須:
- seed.external_material を「素材」として扱う。
- seed.genre_1 と自然に接続する。
- Wikipedia、外部刺激、ランダム記事、偶然の記事、記事、ページ、検索結果、provider名、取得元、RSS などのメタ語を topic に出さない。
- Newsカテゴリと混同しない。`
	case TopicCategoryMovie:
		return `category = movie
面白さ: 共同妄想。存在しない映画を、タイトルから少しずつ立ち上げたくなること。
必須:
- topic は必ず ` + "`「〜」ってどんな映画？`" + ` の形にする。
- 「〜」の中は架空映画のタイトルだけにする。
- topic にあらすじを書かない。
- タイトルは短く、映像が浮かぶものにする。`
	case TopicCategoryNews:
		return `category = news
面白さ: 現実の影響。ニュースの論点・背景・影響を会話できる形にする。
必須:
- seed.news の内容だけを扱う。
- ランダムジャンルや external_material と混ぜない。
- 見出しの言い換えではなく、「何が誰にどう影響するか」が見える題にする。
- news.source や URL を topic に出さない。`
	case TopicCategoryForecast:
		return `category = forecast
面白さ: 変化の分岐。未来を断定せず、現在の兆しから変化の筋道を考える。
必須:
- seed.forecast_domain を中心にする。
- 「何が、何を、どう変えるか」が分かる問いにする。
- 便利になる／危険になるだけの単純な題にしない。
- 予言ではなく、考察の入口にする。`
	case TopicCategoryStory:
		return `category = story
面白さ: 視点反転。よく知っている昔話・童話を、別の視点や役割から語り直す。
必須:
- seed.story_base の元話が分かるようにする。
- 視点変更、役割変更、語り手変更、時代変更のうち1つを入れる。
- 元話の骨格を消さない。
- 読み上げ向けタイトルはここでは生成しない。`
	default:
		return ""
	}
}
