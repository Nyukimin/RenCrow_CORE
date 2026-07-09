# IdleChat Topic Generator / Interestingness Judge 詳細仕様

## 0. 背景と目的

IdleChat は、ユーザーが一定時間操作しないアイドル時間に、Mio / Shiro 等のエージェント同士が自律的に雑談する仕組みである。目的は、エージェントの人格表現、楽しめる雑談・架空映画妄想・未来展望の自動生成、Viewer / TTS 経由のリアルタイム表示・読み上げである。

既存仕様では、IdleChat のお題カテゴリは `single / double / external / movie / news / forecast / story` の7カテゴリで追跡する。内部実装名や生成関数が異なっても、Viewer 表示、履歴、ログ、テストではこのカテゴリ単位で追跡できる必要がある。

本仕様では、`topic_generator.go` に以下を追加する。

```text
1. カテゴリ別 topic 候補生成
2. 候補ごとの内部メタ生成
3. Interestingness Judge による採点
4. コード側の決定的バリデーション
5. 重複排除・リトライ
6. 採用 topic と内部メタの返却
```

重要な方針は、**LLMには面白い候補を出させるが、カテゴリ契約・TTS契約・ログ整合性はコードで決定的に守る**こと。

## 1. 既存仕様との接続

### 1.1 対象カテゴリ

対象カテゴリは次の7つ。

```go
single
double
external
movie
news
forecast
story
```

既存仕様では、通常 IdleChat の自動ローテーションおよび通常評価で、`single → double → external → movie → news → forecast → story-simple` の順で最低1巡できることが求められている。`forecast` と `story` はモード別カテゴリだが、自動ローテーションにも含め、手動起動または専用E2Eでも個別検証できる必要がある。

### 1.2 お題品質の正本

お題生成では、既存のお題サンプル正本を固定出力してはいけない。ただし、各カテゴリの題名品質、粒度、混ぜ方、禁止すべきメタ表現の基準として扱う。

カテゴリ別基準は以下。

```text
Single   : 1ジャンルに人物・物・場所・場面の具体アンカーを入れる。
Double   : 2領域の共通構造が見える題名にする。
External : provider名、取得経路、記事・ページ・検索結果などのメタ語を出さず、素材とジャンルを自然に接続する。
Movie    : 必ず `「〜」ってどんな映画？` の形にする。
News     : ニュースの論点・背景・影響を扱い、ランダムジャンルや外部素材と混ぜない。
Forecast : 将来変化の問いとして、対象領域と変化先が分かる題名にする。
Story    : 元話、視点変更、語り直しの軸が分かる題名にする。
```

### 1.3 TTS / Viewer 契約

Single / Double / External / Movie / News / Forecast の topic は、取得済み topic に `今日のお題。` を前置するだけの置換処理とする。カテゴリ名、内部 strategy、seed、provider 名は読み上げ本文へ入れない。また、これらの topic 本文を LLM で再生成、要約、言い換えしてはいけない。

Story だけは例外として、内部 topic を読み上げに適した短いタイトルへ生成変換してよい。ただし、元話と改変軸を失ってはいけない。Story タイトル生成プロンプトは `prompts/idle_chat/story_topic_title.md` が正本であり、カテゴリ判定や Viewer / TTS ルーティングを担当しない。

この仕様により、本機能では以下を厳守する。

```text
- display topic は topic_generator の採用 topic を正本にする。
- speech topic は Story 以外、コードで `今日のお題。` を前置するだけ。
- Judge の explanation / opening_hook / avoid は Viewer に表示しない。
- Judge の explanation / opening_hook / avoid は TTS に読ませない。
```

## 2. 実装対象ファイル

想定ファイル構成。

```text
internal/application/idlechat/
├── topic_generator.go
├── topic_generator_prompt.go        # 新規。prompt組み立て責務を分離してもよい
├── topic_judge.go                   # 新規。judge呼び出し・採点処理
├── topic_validation.go              # 新規。決定的バリデーション
├── topic_generator_test.go
├── topic_judge_test.go
└── topic_validation_test.go

prompts/idle_chat/
├── topic_generator_common.md        # 新規
├── topic_generator_single.md        # 新規
├── topic_generator_double.md        # 新規
├── topic_generator_external.md      # 新規
├── topic_generator_movie.md         # 新規
├── topic_generator_news.md          # 新規
├── topic_generator_forecast.md      # 新規
├── topic_generator_story.md         # 新規
├── topic_judge.md                   # 新規
└── story_topic_title.md             # 既存正本。Story読み上げタイトル専用
```

既存仕様では、`internal/application/idlechat/topic_generator.go` はトピック生成戦略・外部シード取得を担当するコンポーネントとして整理されている。
本変更では、既存責務を壊さず、候補生成と Judge を追加する。

## 3. 全体処理フロー

### 3.1 通常フロー

```text
GenerateTopic(category, context)
  1. category を正規化する
  2. category に必要な seed を取得する
  3. seed 不足ならカテゴリ成功扱いにせずエラーを返す
  4. category prompt を組み立てる
  5. LLM で topic candidates を生成する
  6. JSON parse
  7. 各 candidate を決定的バリデーションする
  8. invalid candidate を除外する
  9. Interestingness Judge に渡す
 10. Judge 結果を parse
 11. Judge 結果も決定的バリデーションする
 12. recent_topics 類似チェック
 13. winner を採用
 14. TopicGenerationResult を返す
```

### 3.2 リトライ

最大3回。

```text
attempt 1:
  通常候補生成

attempt 2:
  失敗理由を短く渡し、同カテゴリで再生成

attempt 3:
  より制約を強めた再生成

all failed:
  topic_generation_failed を返す
  別カテゴリへすり替えない
```

既存仕様では、seed取得失敗、生成失敗、カテゴリ未対応は明示的なエラーまたは診断として出し、別カテゴリで成功したように扱わないことが求められている。

## 4. データ型

### 4.1 TopicCategory

```go
type TopicCategory string

const (
    TopicCategorySingle   TopicCategory = "single"
    TopicCategoryDouble   TopicCategory = "double"
    TopicCategoryExternal TopicCategory = "external"
    TopicCategoryMovie    TopicCategory = "movie"
    TopicCategoryNews     TopicCategory = "news"
    TopicCategoryForecast TopicCategory = "forecast"
    TopicCategoryStory    TopicCategory = "story"
)
```

`story-simple` は外部ローテーションやE2Eの strategy 名として残ってよいが、内部カテゴリ正本は `story` に正規化する。

```go
func NormalizeTopicCategory(s string) (TopicCategory, error) {
    switch strings.ToLower(strings.TrimSpace(s)) {
    case "single":
        return TopicCategorySingle, nil
    case "double":
        return TopicCategoryDouble, nil
    case "external":
        return TopicCategoryExternal, nil
    case "movie":
        return TopicCategoryMovie, nil
    case "news":
        return TopicCategoryNews, nil
    case "forecast":
        return TopicCategoryForecast, nil
    case "story", "story-simple":
        return TopicCategoryStory, nil
    default:
        return "", ErrUnsupportedTopicCategory
    }
}
```

### 4.2 TopicSeed

```go
type TopicSeed struct {
    Category TopicCategory `json:"category"`

    // Single
    Genre1 string `json:"genre_1,omitempty"`

    // Double
    Genre2 string `json:"genre_2,omitempty"`

    // External
    ExternalMaterial *ExternalMaterialSeed `json:"external_material,omitempty"`

    // News
    News *NewsSeed `json:"news,omitempty"`

    // Forecast
    ForecastDomain string `json:"forecast_domain,omitempty"`
    TrendKeywords  []string `json:"trend_keywords,omitempty"`

    // Story
    StoryBase      string `json:"story_base,omitempty"`       // 例: 桃太郎
    StoryTransform string `json:"story_transform,omitempty"`  // 例: 鬼側の記録係

    RecentTopics []RecentTopic `json:"recent_topics,omitempty"`
}
```

```go
type ExternalMaterialSeed struct {
    Title    string `json:"title"`
    Summary  string `json:"summary,omitempty"`
    Provider string `json:"provider,omitempty"` // ログ専用。prompt本文には出さない
    URL      string `json:"url,omitempty"`      // ログ専用。prompt本文には出さない
    Category string `json:"category,omitempty"` // ログ専用
}
```

```go
type NewsSeed struct {
    Title    string `json:"title"`
    Category string `json:"category,omitempty"` // general / culture / business / world / sports / tech
    Source   string `json:"source,omitempty"`
    URL      string `json:"url,omitempty"`
    Summary  string `json:"summary,omitempty"`
}
```

News seed は `title / category / source / url` を保持できる。News はニュースシード1件から生成し、ランダムジャンルを混ぜず、Externalへ黙ってすり替えない。

### 4.3 TopicCandidate

LLM 生成側の出力。

```go
type TopicCandidate struct {
    Topic string `json:"topic"`

    // 内部メタ。Viewer/TTSには出さない。
    InterestingnessAxis string `json:"interestingness_axis"`
    OpeningHook         string `json:"opening_hook"`
    Avoid               string `json:"avoid"`

    // LLMの自己説明。ログには残してよいが、Viewer/TTSには出さない。
    Rationale string `json:"rationale,omitempty"`
}
```

`InterestingnessAxis` はカテゴリごとに固定値を推奨する。

```go
var ExpectedAxisByCategory = map[TopicCategory]string{
    TopicCategorySingle:   "観察",
    TopicCategoryDouble:   "接続",
    TopicCategoryExternal: "偶然の意味化",
    TopicCategoryMovie:    "共同妄想",
    TopicCategoryNews:     "現実の影響",
    TopicCategoryForecast: "変化の分岐",
    TopicCategoryStory:    "視点反転",
}
```

### 4.4 TopicJudgeResult

Judge 側の出力。

```go
type TopicJudgeResult struct {
    WinnerTopic string            `json:"winner_topic"`
    Scores      []TopicJudgeScore `json:"scores"`
    RejectReasonSummary string    `json:"reject_reason_summary,omitempty"`
}
```

```go
type TopicJudgeScore struct {
    Topic string `json:"topic"`

    CategoryFit          int `json:"category_fit"`           // 0-5
    Concreteness         int `json:"concreteness"`            // 0-5
    Curiosity            int `json:"curiosity"`               // 0-5
    ConversationPotential int `json:"conversation_potential"` // 0-5
    AxisStrength         int `json:"axis_strength"`           // 0-5
    Novelty              int `json:"novelty"`                 // 0-5
    Safety               int `json:"safety"`                  // 0-5

    Total int `json:"total"` // 0-35
    Reason string `json:"reason"`
}
```

### 4.5 TopicGenerationResult

最終返却。

```go
type TopicGenerationResult struct {
    Topic    string        `json:"topic"`    // Viewer / history / summary 正本
    Category TopicCategory `json:"category"` // 正本カテゴリ
    Strategy string        `json:"strategy"` // 例: "single", "double", "external", "movie", "news", "forecast", "story-simple"

    InterestingnessAxis string `json:"interestingness_axis"`
    OpeningHook         string `json:"opening_hook"`
    Avoid               string `json:"avoid"`

    Seed TopicSeed `json:"seed"`

    Candidates []TopicCandidate `json:"candidates,omitempty"`
    Judge      *TopicJudgeResult `json:"judge,omitempty"`

    Provider string `json:"provider"` // 例: "mio", "forecast"
}
```

`Topic`, `Category`, `Strategy` は Viewer 表示、履歴、ログ、TTSイベントで追跡可能にする。既存仕様でも、各 session の topic/category/strategy が Viewer 表示、履歴、ログ、TTSイベントで追跡できることが正当性条件になっている。

## 5. カテゴリ別の生成仕様

### 5.1 Single

面白さの核。

```text
観察。
小さい題材を深く見る。
```

必須 seed。

```text
Genre1
```

生成条件。

```text
- 1ジャンルだけを扱う。
- 人物・物・場所・場面のうち2つ以上を入れる。
- 抽象語だけで終わらせない。
- 大きな社会論ではなく、小さな場面から入る。
- Mio が感情や生活感を拾え、Shiro が構造や意味を拾える余地を残す。
```

例。

```text
古書店の店主が見つけた、手紙に残る記憶の扱い方
```

NG。

```text
記憶について考える
古書店の魅力
```

### 5.2 Double

面白さの核。

```text
接続。
遠い2領域の共通構造を見つける。
```

必須 seed。

```text
Genre1
Genre2
```

生成条件。

```text
- Genre1 と Genre2 の両方を使う。
- 表面的な共通点ではなく、共通する仕組み・制約・悩みが見える題にする。
- 「AとB」だけで終わらせない。
- 「何が共通するのか」まで topic に含める。
```

例。

```text
潮汐と郵便制度に共通する、遅れて届くものの設計
```

NG。

```text
潮汐と郵便制度について
海と手紙の不思議な関係
```

### 5.3 External

面白さの核。

```text
偶然の意味化。
外部素材を自然な題に変換する。
```

必須 seed。

```text
ExternalMaterial.Title
Genre1
```

任意 seed。

```text
ExternalMaterial.Summary
```

生成条件。

```text
- external_material は「素材」として扱う。
- provider 名、取得経路、記事、ページ、検索結果、Wikipedia、外部刺激、ランダム記事、偶然の記事などのメタ語を topic に出さない。
- 素材と Genre1 を自然に接続する。
- News と混同しない。
- ニュース見出しの深掘りは External ではなく News で扱う。
```

External は外部刺激とジャンルの組み合わせを扱うカテゴリであり、純粋なニュース深掘りではない。生成 prompt では外部刺激を素材として渡し、取得経路や provider 名をお題本文に出させない。

例。

```text
地下鉄博物館に残る音声案内と織物の記録性
```

NG。

```text
Wikipediaで見つけた地下鉄博物館と織物
ランダム記事から考える地下鉄博物館
検索結果に出てきた音声案内について
```

### 5.4 Movie

面白さの核。

```text
共同妄想。
存在しない映画をタイトルから立ち上げる。
```

必須 seed。

```text
なし
```

任意 seed。

```text
Genre1
RecentTopics
```

生成条件。

```text
- 必ず `「〜」ってどんな映画？` の形式にする。
- topic にあらすじを書かない。
- 映像が浮かぶ短い架空タイトルにする。
- 既存映画タイトルと近すぎないようにする。
- ジャンルを確定しすぎず、Mio と Shiro が解釈を足せる余白を残す。
```

Movie は独立カテゴリであり、Single / Double / External の隠しフラグとして扱ってはいけない。Movie として生成した topic は Viewer、履歴、ログ、E2E で `movie` として識別できる必要がある。

例。

```text
「雨上がりの映写室」ってどんな映画？
```

NG。

```text
雨の日に映画館で幽霊に会う少年の感動映画ってどんな映画？
架空映画について話す
```

### 5.5 News

面白さの核。

```text
現実の影響。
ニュースの論点・背景・影響を会話可能な題にする。
```

必須 seed。

```text
News.Title
```

任意 seed。

```text
News.Summary
News.Category
News.Source
News.URL
```

生成条件。

```text
- NewsSeed 1件だけを扱う。
- Genre1 / Genre2 / ExternalMaterial と混ぜない。
- 見出しの単純な言い換えで終わらせない。
- 「何が、誰に、どう影響するか」が見える題にする。
- 煽らない。
- 断定しすぎない。
- 制度、現場、生活、判断、影響、背景、論点のいずれかを含む。
```

News はニュースシード1件を選び、そのニュースの論点・背景・影響を深掘りする。ランダムジャンルを混ぜず、Externalへ黙ってすり替えないことが契約になっている。

例。

```text
新しい医療制度の検討が、現場の判断に与える影響
```

NG。

```text
医療制度のニュースについて
医療制度と古書店を組み合わせた話
```

### 5.6 Forecast

面白さの核。

```text
変化の分岐。
未来を断定せず、変化の筋道を考える。
```

必須 seed。

```text
ForecastDomain
```

任意 seed。

```text
TrendKeywords
```

生成条件。

```text
- ForecastDomain を中心にする。
- 「何が、何を、どう変えるか」が分かる問いにする。
- 便利になる / 危険になるだけの単純な題にしない。
- 人の生活、仕事、記憶、制度、創作、関係性などへの変化が見える題にする。
- 予言ではなく、考察の入口にする。
```

通常モードは単発トピック、未来展望モードは番組形式で、通常モードは12ターン/トピック、未来展望モードは100ターン/ドメイン・最大600ターンという設計差がある。

例。

```text
AI 技術が、個人の記憶整理をどう変えるか
```

NG。

```text
AIの未来
AIは人類をどう変えるか
```

### 5.7 Story

面白さの核。

```text
視点反転。
知っている物語を別の視点から語り直す。
```

必須 seed。

```text
StoryBase
```

任意 seed。

```text
StoryTransform
```

生成条件。

```text
- 元話が分かるようにする。
- 視点変更、役割変更、語り手変更、時代変更のうち1つを入れる。
- 元話の骨格を消さない。
- あらすじではなく、「どう語り直すか」が分かる題にする。
- 読み上げタイトル変換は story_topic_title.md 側に任せる。
```

Story は昔話・童話を改変して朗読する物語セッションとして扱われる。Storyモードでは、ストーリー選択から感情ラベル付けまでは決定論的、ドラフト生成とリビジョンのみLLMが担当するパイプラインになっている。

例。

```text
桃太郎を、鬼側の記録係から語り直す物語
```

NG。

```text
新しい桃太郎
鬼がかわいそうな話
```

## 6. Prompt 仕様

### 6.1 `prompts/idle_chat/topic_generator_common.md`

```text
あなたは RenCrow IdleChat の topic generator です。

目的:
Mio と Shiro が自律的に雑談したとき、12ターン程度で自然に深まり、
聞いているユーザーが「続きを聞きたい」と感じる topic 候補を生成してください。

重要:
あなたは topic 候補を生成するだけです。
カテゴリ判定、最終採用、Viewer表示、TTS読み上げ、ログ記録は実装コードが担当します。

共通条件:
- 出力は JSON のみ。
- candidates 配列に {candidate_count} 件を返す。
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
category: {category}
seed:
{seed_json}

recent_topics:
{recent_topics_json}

出力形式:
{
  "candidates": [
    {
      "topic": "...",
      "interestingness_axis": "...",
      "opening_hook": "このお題で最初に拾うべき面白さを20〜60字で書く",
      "avoid": "このお題で避けるべき退屈な展開を20〜60字で書く",
      "rationale": "なぜ会話が続くかを短く書く"
    }
  ]
}
```

### 6.2 `topic_generator_single.md`

```text
category = single

このカテゴリの面白さ:
観察。
ひとつのジャンルや素材を、具体的な人物・物・場所・場面から深掘りすること。

必須:
- seed.genre_1 を中心にする。
- 1ジャンルだけを扱う。
- 人物、物、場所、場面のうち2つ以上を入れる。
- 大きな社会論ではなく、小さな場面から始まる題にする。
- Mio が感情や生活感を拾え、Shiro が構造や意味を拾える余地を作る。

良い方向:
古書店の店主が見つけた、手紙に残る記憶の扱い方

悪い方向:
記憶について考える
古書店の魅力

interestingness_axis は必ず "観察" にする。
```

### 6.3 `topic_generator_double.md`

```text
category = double

このカテゴリの面白さ:
接続。
一見離れた2領域の間に、同じ構造・制約・悩みを見つけること。

必須:
- seed.genre_1 と seed.genre_2 の両方を使う。
- 表面的な共通点ではなく、共通する仕組みが見える題にする。
- 「AとB」だけで終わらせず、「何が共通するのか」まで題名に含める。
- こじつけではなく、会話で検討できる仮説にする。

良い方向:
潮汐と郵便制度に共通する、遅れて届くものの設計

悪い方向:
潮汐と郵便制度について
海と手紙の不思議な関係

interestingness_axis は必ず "接続" にする。
```

### 6.4 `topic_generator_external.md`

```text
category = external

このカテゴリの面白さ:
偶然の意味化。
外から来た素材とジャンルを自然に接続し、偶然拾ったものが必然に見えること。

必須:
- seed.external_material を「素材」として扱う。
- seed.genre_1 と自然に接続する。
- Wikipedia、外部刺激、ランダム記事、偶然の記事、記事、ページ、検索結果、provider名、取得元、RSS などのメタ語を topic に出さない。
- 変な組み合わせでも、題名としては落ち着いた日本語にする。
- Newsカテゴリと混同しない。

良い方向:
地下鉄博物館に残る音声案内と織物の記録性

悪い方向:
Wikipediaで見つけた地下鉄博物館について
ランダム記事と織物の話
検索結果から考える音声案内

interestingness_axis は必ず "偶然の意味化" にする。
```

### 6.5 `topic_generator_movie.md`

```text
category = movie

このカテゴリの面白さ:
共同妄想。
存在しない映画を、タイトルから少しずつ立ち上げたくなること。

必須:
- topic は必ず `「〜」ってどんな映画？` の形にする。
- 「〜」の中は架空映画のタイトルだけにする。
- topic にあらすじを書かない。
- タイトルは短く、映像が浮かぶものにする。
- ジャンルを確定しすぎず、Mio と Shiro が解釈を足せる余白を残す。
- 既存映画のタイトルに近すぎない。

良い方向:
「雨上がりの映写室」ってどんな映画？

悪い方向:
「雨の日に映画館で幽霊に会う少年の感動映画」ってどんな映画？
架空映画について話す

interestingness_axis は必ず "共同妄想" にする。
```

### 6.6 `topic_generator_news.md`

```text
category = news

このカテゴリの面白さ:
現実の影響。
ニュースを紹介することではなく、その論点・背景・影響を会話できる形にすること。

必須:
- seed.news の内容だけを扱う。
- ランダムジャンルや external_material と混ぜない。
- 見出しの言い換えではなく、「何が誰にどう影響するか」が見える題にする。
- 煽らない。
- 断定しない。
- 制度、現場、生活、判断、影響、背景、論点のいずれかを含める。
- news.source や URL を topic に出さない。

良い方向:
新しい医療制度の検討が、現場の判断に与える影響

悪い方向:
医療制度のニュースについて
医療制度と古書店を組み合わせた話
NHKの記事から考える医療制度

interestingness_axis は必ず "現実の影響" にする。
```

### 6.7 `topic_generator_forecast.md`

```text
category = forecast

このカテゴリの面白さ:
変化の分岐。
未来を断定することではなく、現在の兆しから変化の筋道を考えたくなること。

必須:
- seed.forecast_domain を中心にする。
- 「何が、何を、どう変えるか」が分かる問いにする。
- 便利になる／危険になるだけの単純な題にしない。
- 人の生活、仕事、記憶、制度、創作、関係性などへの変化が見える題にする。
- 予言ではなく、考察の入口にする。

良い方向:
AI 技術が、個人の記憶整理をどう変えるか

悪い方向:
AIの未来
AIは人類をどう変えるか
未来社会について

interestingness_axis は必ず "変化の分岐" にする。
```

### 6.8 `topic_generator_story.md`

```text
category = story

このカテゴリの面白さ:
視点反転。
よく知っている昔話・童話を、別の視点や役割から語り直すこと。

必須:
- seed.story_base の元話が分かるようにする。
- 視点変更、役割変更、語り手変更、時代変更のうち1つを入れる。
- 元話の骨格を消さない。
- あらすじではなく、「どう語り直すか」が分かる題にする。
- 読み上げ向けタイトルはここでは生成しない。

良い方向:
桃太郎を、鬼側の記録係から語り直す物語

悪い方向:
新しい桃太郎
鬼がかわいそうな話

interestingness_axis は必ず "視点反転" にする。
```

## 7. Interestingness Judge 仕様

### 7.1 `prompts/idle_chat/topic_judge.md`

```text
あなたは RenCrow IdleChat の topic judge です。

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
1. category_fit:
   カテゴリ仕様に合っているか。

2. concreteness:
   人物・物・場所・場面・制度・出来事が見えるか。

3. curiosity:
   聞いた瞬間に「どういうこと？」が生まれるか。

4. conversation_potential:
   Mio と Shiro の見方が分かれそうか。
   12ターン程度で自然に展開できそうか。

5. axis_strength:
   カテゴリ固有の面白さが出ているか。
   single=観察、double=接続、external=偶然の意味化、movie=共同妄想、
   news=現実の影響、forecast=変化の分岐、story=視点反転。

6. novelty:
   recent_topics と似すぎていないか。

7. safety:
   契約違反がないか。
   例:
   - External に provider名や取得経路が出ている
   - News がランダムジャンルと混ざっている
   - Movie が `「〜」ってどんな映画？` 形式ではない
   - Story で元話や改変軸が消えている
   - Forecast が断定予言になっている
   - topic が説明文やあらすじになっている

入力:
category: {category}

seed:
{seed_json}

recent_topics:
{recent_topics_json}

candidates:
{candidates_json}

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
}
```

### 7.2 採点ルール

スコアは各0〜5。

```text
0 = 完全に不適合
1 = かなり弱い
2 = 弱い
3 = 許容
4 = 良い
5 = 非常に良い
```

`total` は単純合計。

```go
total := category_fit +
         concreteness +
         curiosity +
         conversation_potential +
         axis_strength +
         novelty +
         safety
```

採用条件。

```go
const MinJudgeTotal = 24
const MinCategoryFit = 4
const MinSafety = 4
```

どれかを満たさない場合は不採用。

```text
- winner total < 24
- winner category_fit < 4
- winner safety < 4
- winner_topic が candidates に存在しない
- winner_topic が決定的バリデーションに失敗
```

### 7.3 タイブレーク

同点の場合は以下の順で選ぶ。

```text
1. safety が高い
2. category_fit が高い
3. conversation_potential が高い
4. novelty が高い
5. topic が短い
6. candidates 内の順序が早い
```

## 8. 決定的バリデーション

LLM出力は信用せず、必ずコードで検証する。

### 8.1 共通バリデーション

```go
func ValidateCommonTopic(topic string) error
```

条件。

```text
- topic が空でない
- topic が1行である
- 前後空白を除去しても空でない
- 長すぎない
  - 原則 10〜80 rune
  - Movie は全体 8〜40 rune 程度を推奨。ただし形式優先。
- 説明文形式ではない
- JSON崩れがない
- 禁止メタ語を含まない
```

共通禁止語。

```go
var CommonForbiddenMetaTerms = []string{
    "カテゴリ", "strategy", "provider", "seed", "内部",
    "生成", "プロンプト", "JSON", "候補",
}
```

### 8.2 External 禁止語

```go
var ExternalForbiddenTerms = []string{
    "Wikipedia", "ウィキペディア",
    "外部刺激", "ランダム記事", "偶然の記事",
    "記事", "ページ", "検索結果", "取得元",
    "provider", "RSS", "URL",
}
```

Externalではこれらを topic に含めたら即 invalid。

### 8.3 Movie 形式

```go
var movieTopicPattern = regexp.MustCompile(`^「[^」]{2,24}」ってどんな映画？$`)
```

条件。

```text
- 必ず上記形式に一致する
- 鍵括弧内に「映画」「物語」「あらすじ」などの説明語が多すぎない
- 鍵括弧内に句点を含めない
```

### 8.4 News バリデーション

条件。

```text
- NewsSeed が存在する
- topic に source / URL / RSS / provider を含めない
- topic に Genre1 / Genre2 由来のランダムジャンルを混ぜない
- topic に ExternalMaterial 由来の語が混ざっていない
- 「ニュースについて」だけで終わらない
- 影響 / 背景 / 論点 / 現場 / 制度 / 生活 / 判断 のいずれかに近い語を含むことを推奨
```

ここで推奨語がない場合は即 invalid ではなく、Judge の `axis_strength` と `concreteness` で落とす。ただし「ニュースについて」のような弱い題は invalid にしてよい。

### 8.5 Forecast バリデーション

条件。

```text
- ForecastDomain が存在する
- topic が問い、または変化の構造を含む
- 「未来」「人類」「社会」だけの巨大抽象にしない
- 断定予言を避ける
```

弱い例。

```text
AIの未来
人類はどうなるか
```

### 8.6 Story バリデーション

条件。

```text
- StoryBase が存在する
- topic に StoryBase が含まれる、または明確に元話が分かる
- 視点変更 / 役割変更 / 語り手変更 / 時代変更の手がかりを含む
- あらすじ化しない
```

## 9. 重複排除

既存仕様では、直近12トピックとの類似度チェックと最大3回リトライがある。

### 9.1 正規化

```go
func NormalizeTopicForSimilarity(s string) string {
    s = strings.TrimSpace(s)
    s = strings.ReplaceAll(s, "　", " ")
    s = strings.ToLower(s)
    s = removePunctuationForSimilarity(s)
    s = collapseSpaces(s)
    return s
}
```

### 9.2 完全一致

```go
if NormalizeTopicForSimilarity(candidate.Topic) == NormalizeTopicForSimilarity(recent.Topic) {
    return ErrRecentTopicExactDuplicate
}
```

### 9.3 類似度

既存の類似度計算がある場合はそれを使う。ない場合は暫定で Jaccard / n-gram 類似でよい。

推奨しきい値。

```go
const RecentTopicSimilarityThreshold = 0.82
```

同カテゴリの直近 topic との類似は特に厳しく見る。

```go
if sim >= 0.82 {
    return ErrRecentTopicTooSimilar
}
```

## 10. Error / Diagnostic

```go
var (
    ErrUnsupportedTopicCategory      = errors.New("topic_category_unsupported")
    ErrTopicSeedUnavailable          = errors.New("topic_seed_unavailable")
    ErrTopicGenerationInvalidJSON    = errors.New("topic_generation_invalid_json")
    ErrTopicGenerationNoCandidates   = errors.New("topic_generation_no_candidates")
    ErrTopicContractViolation        = errors.New("topic_contract_violation")
    ErrTopicJudgeInvalidJSON         = errors.New("topic_judge_invalid_json")
    ErrTopicJudgeWinnerMissing       = errors.New("topic_judge_winner_missing")
    ErrTopicJudgeLowScore            = errors.New("topic_judge_low_score")
    ErrRecentTopicExactDuplicate     = errors.New("topic_recent_exact_duplicate")
    ErrRecentTopicTooSimilar         = errors.New("topic_recent_too_similar")
    ErrTopicGenerationFailed         = errors.New("topic_generation_failed")
)
```

診断ログには最低限以下を残す。

```go
type TopicGenerationDiagnostic struct {
    SessionID string `json:"session_id,omitempty"`
    Category  string `json:"category"`
    Strategy  string `json:"strategy"`

    Attempt int `json:"attempt"`

    ErrorCode string `json:"error_code,omitempty"`
    ErrorMessage string `json:"error_message,omitempty"`

    SeedSummary string `json:"seed_summary,omitempty"`

    CandidateCount int `json:"candidate_count,omitempty"`
    InvalidCandidates []InvalidCandidateDiagnostic `json:"invalid_candidates,omitempty"`

    WinnerTopic string `json:"winner_topic,omitempty"`
    JudgeTotal int `json:"judge_total,omitempty"`
}
```

## 11. Config

```yaml
idle_chat:
  topic_generation:
    enabled: true

    candidates_per_attempt: 5
    max_attempts: 3

    judge_enabled: true
    min_judge_total: 24
    min_category_fit: 4
    min_safety: 4

    recent_topic_window: 12
    recent_similarity_threshold: 0.82

    log_candidates: true
    log_judge_scores: true

    prompts:
      common: "prompts/idle_chat/topic_generator_common.md"
      single: "prompts/idle_chat/topic_generator_single.md"
      double: "prompts/idle_chat/topic_generator_double.md"
      external: "prompts/idle_chat/topic_generator_external.md"
      movie: "prompts/idle_chat/topic_generator_movie.md"
      news: "prompts/idle_chat/topic_generator_news.md"
      forecast: "prompts/idle_chat/topic_generator_forecast.md"
      story: "prompts/idle_chat/topic_generator_story.md"
      judge: "prompts/idle_chat/topic_judge.md"
```

Forecast で外部 Coder API を使う場合は `idle_chat.forecast_external_enabled: true` の明示設定が必要であり、明示設定がない場合は外部 Coder provider を選択しない。生成失敗時も別の外部 provider へ自動切替しない。

## 12. 実装疑似コード

```go
func (g *TopicGenerator) GenerateInterestingTopic(
    ctx context.Context,
    category TopicCategory,
    seed TopicSeed,
    recent []RecentTopic,
) (*TopicGenerationResult, error) {
    normalized, err := NormalizeTopicCategory(string(category))
    if err != nil {
        return nil, err
    }

    if err := ValidateSeedForCategory(normalized, seed); err != nil {
        g.logTopicDiagnostic(normalized, seed, 0, ErrTopicSeedUnavailable, err)
        return nil, ErrTopicSeedUnavailable
    }

    var lastErr error

    for attempt := 1; attempt <= g.config.MaxAttempts; attempt++ {
        prompt, err := g.BuildGenerationPrompt(normalized, seed, recent, attempt, lastErr)
        if err != nil {
            return nil, err
        }

        raw, err := g.llm.Generate(ctx, prompt, LLMOptions{
            Think: false,
            JSONOnly: true,
        })
        if err != nil {
            lastErr = err
            continue
        }

        candidates, err := ParseTopicCandidates(raw)
        if err != nil {
            lastErr = ErrTopicGenerationInvalidJSON
            continue
        }

        validCandidates := make([]TopicCandidate, 0, len(candidates))
        invalids := make([]InvalidCandidateDiagnostic, 0)

        for _, c := range candidates {
            if err := ValidateTopicCandidate(normalized, seed, c); err != nil {
                invalids = append(invalids, InvalidCandidateDiagnostic{
                    Topic: c.Topic,
                    Error: err.Error(),
                })
                continue
            }

            if err := CheckRecentTopicSimilarity(c.Topic, recent, g.config.RecentSimilarityThreshold); err != nil {
                invalids = append(invalids, InvalidCandidateDiagnostic{
                    Topic: c.Topic,
                    Error: err.Error(),
                })
                continue
            }

            validCandidates = append(validCandidates, c)
        }

        if len(validCandidates) == 0 {
            lastErr = ErrTopicGenerationNoCandidates
            g.logInvalidCandidates(normalized, seed, attempt, invalids)
            continue
        }

        winner, judge, err := g.JudgeCandidates(ctx, normalized, seed, recent, validCandidates)
        if err != nil {
            lastErr = err
            continue
        }

        if err := ValidateTopicCandidate(normalized, seed, winner); err != nil {
            lastErr = err
            continue
        }

        if err := CheckRecentTopicSimilarity(winner.Topic, recent, g.config.RecentSimilarityThreshold); err != nil {
            lastErr = err
            continue
        }

        return &TopicGenerationResult{
            Topic: winner.Topic,
            Category: normalized,
            Strategy: StrategyFromCategory(normalized),
            InterestingnessAxis: winner.InterestingnessAxis,
            OpeningHook: winner.OpeningHook,
            Avoid: winner.Avoid,
            Seed: seed,
            Candidates: validCandidates,
            Judge: judge,
            Provider: g.providerName,
        }, nil
    }

    g.logTopicDiagnostic(category, seed, g.config.MaxAttempts, ErrTopicGenerationFailed, lastErr)
    return nil, ErrTopicGenerationFailed
}
```

## 13. Judge 疑似コード

```go
func (g *TopicGenerator) JudgeCandidates(
    ctx context.Context,
    category TopicCategory,
    seed TopicSeed,
    recent []RecentTopic,
    candidates []TopicCandidate,
) (TopicCandidate, *TopicJudgeResult, error) {
    if !g.config.JudgeEnabled {
        return candidates[0], nil, nil
    }

    prompt, err := g.BuildJudgePrompt(category, seed, recent, candidates)
    if err != nil {
        return TopicCandidate{}, nil, err
    }

    raw, err := g.llm.Generate(ctx, prompt, LLMOptions{
        Think: false,
        JSONOnly: true,
    })
    if err != nil {
        return TopicCandidate{}, nil, err
    }

    judge, err := ParseTopicJudgeResult(raw)
    if err != nil {
        return TopicCandidate{}, nil, ErrTopicJudgeInvalidJSON
    }

    winner, score, ok := FindWinnerCandidate(judge, candidates)
    if !ok {
        return TopicCandidate{}, judge, ErrTopicJudgeWinnerMissing
    }

    if score.Total < g.config.MinJudgeTotal ||
       score.CategoryFit < g.config.MinCategoryFit ||
       score.Safety < g.config.MinSafety {
        return TopicCandidate{}, judge, ErrTopicJudgeLowScore
    }

    return winner, judge, nil
}
```

## 14. TTS / Viewer 連携

topic generation の返却値から、表示と読み上げを分ける。

```go
func BuildDisplayTopic(result TopicGenerationResult) string {
    return result.Topic
}
```

```go
func BuildSpeechTopic(result TopicGenerationResult) string {
    if result.Category == TopicCategoryStory {
        // story_topic_title.md を使う既存/別処理に委譲。
        // ここで本文生成、あらすじ生成、カテゴリ判定をしてはいけない。
        title := BuildStorySpeechTitle(result.Topic)
        return "今日のお題。" + title
    }

    normalized := NormalizeTopicForSpeech(result.Topic)
    return "今日のお題。" + normalized
}
```

Story 以外は、取得済み topic に `今日のお題。` を前置するだけ。LLM による再生成・要約・言い換えは禁止。

## 15. 発話生成側への接続

`TopicGenerationResult` の `OpeningHook` と `Avoid` は、最初の発話生成 prompt に内部メタとして渡す。

ただし、Mio / Shiro の発話にそのまま出さない。

```text
【topic】
{topic}

【このtopicの面白さの軸】
{interestingness_axis}

【最初に拾うべき面白さ】
{opening_hook}

【避ける退屈な展開】
{avoid}
```

目的は、カテゴリ別の勝ち筋を発話生成に渡すこと。

```text
Single   : 細部から入る
Double   : 共通構造を探す
External : 偶然の素材を自然化する
Movie    : 映像を立ち上げる
News     : 影響と判断を扱う
Forecast : 分岐を整理する
Story    : 視点を反転する
```

## 16. ログ仕様

採用時に以下を記録する。

```json
{
  "event": "idlechat.topic.generated",
  "category": "double",
  "strategy": "double",
  "topic": "盆栽と都市計画に共通する、成長を待つための設計",
  "interestingness_axis": "接続",
  "opening_hook": "小さく整えることと、大きく育つことの矛盾を拾う",
  "avoid": "盆栽と都市の共通点を表面的に並べるだけで終わらせない",
  "provider": "mio",
  "attempt": 1,
  "candidate_count": 5,
  "judge_total": 31,
  "seed": {
    "category": "double",
    "genre_1": "盆栽",
    "genre_2": "都市計画"
  }
}
```

失敗時。

```json
{
  "event": "idlechat.topic.generation_failed",
  "category": "news",
  "strategy": "news",
  "error_code": "topic_seed_unavailable",
  "message": "news seed is required for news category"
}
```

News seed 取得失敗時は `news_seed_unavailable` 等の診断をログに残し、カテゴリ成功として扱わない。

## 17. テスト仕様

### 17.1 Category Prompt Tests

```text
TestSingleTopicHasConcreteAnchor
- single topic が人物・物・場所・場面の少なくとも2つを含む傾向を検証する。
- 少なくとも空・抽象語だけではないことを検証する。

TestDoubleTopicHasBothGenres
- genre_1 / genre_2 が両方含まれる。
- 「共通する」「似ている」「同じ構造」など、接続の手がかりがある。

TestExternalTopicDoesNotLeakProvider
- Wikipedia / ランダム記事 / 検索結果 / provider / URL を含む候補は invalid。

TestMovieTopicFormat
- `^「[^」]+」ってどんな映画？$` に一致する。
- Movie category として記録される。
- movie=true の隠し属性だけで表現しない。

TestNewsTopicUsesOnlyNewsSeed
- Genre や ExternalMaterial と混ぜない。
- NewsSeed が無ければ ErrTopicSeedUnavailable。
- News を External 成功扱いにしない。

TestForecastTopicHasChangeQuestion
- ForecastDomain を含む。
- 変化先が分かる。

TestStoryTopicKeepsBaseAndTransform
- StoryBase が残る。
- 視点変更または語り直し軸が残る。
```

### 17.2 Judge Tests

```text
TestJudgeRejectsMovieWrongFormat
- Movie候補が形式違反の場合 safety <= 3 になり、不採用。

TestJudgeRejectsExternalProviderLeak
- External候補に Wikipedia 等がある場合 safety <= 3 になり、不採用。

TestJudgeRejectsNewsGenreMix
- News候補がランダムジャンルと混ざる場合 safety <= 3 になり、不採用。

TestJudgeWinnerMustExistInCandidates
- Judge が候補外の winner_topic を返した場合 ErrTopicJudgeWinnerMissing。

TestJudgeLowScoreRetries
- winner total < MinJudgeTotal の場合、再生成 attempt へ進む。
```

### 17.3 Integration Tests

```text
TestRotationCoversAllCategories
- single → double → external → movie → news → forecast → story-simple を1巡できる。

TestTopicCategoryStrategyTraceability
- session の topic/category/strategy が Viewer、履歴、ログ、TTSイベントで一致する。

TestNonStorySpeechTopicDoesNotRewrite
- Single / Double / External / Movie / News / Forecast は `今日のお題。` 前置のみ。
- LLMで言い換えない。

TestStorySpeechTitleDelegatesToStoryTitlePrompt
- Story の読み上げタイトル生成は story_topic_title.md に委譲。
- topic_generator / topic_judge では読み上げタイトルを生成しない。

TestRecentTopicSimilarityRetry
- 直近12 topic と類似度が閾値以上なら不採用。
- 最大3回まで再生成。

TestNoCrossCategoryFallback
- News seed なしで External や Single にフォールバックしない。
- External seed なしで Single に成功扱いしない。
```

既存仕様では、正本・参照元仕様・実装・Viewer・E2Eのカテゴリ一覧一致、ローテーション1巡、各 session の topic/category/strategy 追跡、News/Movieのカテゴリ保持、失敗時の明示的エラー、テストまたはE2Eログ確認が正当性条件になっている。

## 18. 受け入れ条件

この実装は、以下を満たしたら完了とする。

```text
1. topic_generator がカテゴリ別 prompt を使って候補を生成する。
2. Interestingness Judge が候補を採点し、winner を選ぶ。
3. winner は必ず候補内から選ばれる。
4. カテゴリ契約違反の候補はコード側で invalid になる。
5. Movie は必ず `「〜」ってどんな映画？` 形式になる。
6. External は provider名・取得経路・記事・ページ・検索結果などのメタ語を出さない。
7. News は news seed 1件だけから生成され、ランダムジャンルや外部素材と混ざらない。
8. Story は元話と改変軸を保持する。
9. Single / Double / External / Movie / News / Forecast の読み上げ topic は LLM で再生成・要約・言い換えしない。
10. Story の読み上げタイトル生成は `story_topic_title.md` に委譲する。
11. 直近12 topic 類似チェックで重複を避ける。
12. seed 不足、生成失敗、カテゴリ未対応は明示的なエラーとしてログに残し、別カテゴリ成功扱いにしない。
13. E2Eで `single → double → external → movie → news → forecast → story-simple` を1巡できる。
14. Viewer / 履歴 / ログ / TTSイベントで topic/category/strategy が一致する。
```

## 19. 実装メモ

今回の設計では、`topic` と `opening_hook` を分ける。

```text
topic:
Viewer / history / summary / TTS の正本。

opening_hook:
最初の発話生成に渡す内部メタ。
ユーザーには表示しない。

avoid:
退屈化を避ける内部メタ。
ユーザーには表示しない。

interestingness_axis:
カテゴリ別の面白さを発話生成に伝える内部メタ。
```

この分離により、topic は短くきれいに保ちつつ、Mio / Shiro の最初の会話だけはカテゴリ固有の面白さに寄せられる。

最終的な狙いは、次の状態。

```text
Single   は観察で始まる。
Double   は接続で始まる。
External は偶然の意味化で始まる。
Movie    は映像の立ち上げで始まる。
News     は現実の影響で始まる。
Forecast は変化の分岐で始まる。
Story    は視点反転で始まる。
```

これを `topic_generator` と `topic_judge` の責務に閉じ込める。
発話生成側には、採用済み topic と内部メタだけを渡す。
