# IdleChat 対話面白さ向上仕様

## 0. 目的

本仕様は、IdleChat において生成済み topic を受け取り、その後のエージェント同士の対話内容をカテゴリ別に面白く展開するための仕様である。

前段の `topic_generator` / `topic_judge` は「面白い入口」を作る。本仕様は、その入口から実際の対話を 12 ターン程度で自然に深めるための「対話演出・発話生成・品質判定・ログ・テスト」を定義する。

対象カテゴリは次の 7 つである。

```text
single / double / external / movie / news / forecast / story
```

本仕様の中核は、カテゴリごとに「対話が発見すべきもの」を変えることである。

```text
Single   : 細部を発見する
Double   : 構造を発見する
External : 偶然の素材に意味を発見する
Movie    : 存在しない映画の映像を発見する
News     : 現実の出来事の影響を発見する
Forecast : 未来の分岐を発見する
Story    : 既知の物語の別視点を発見する
```

## 1. 既存 IdleChat との接続

### 1.1 実行境界

通常 IdleChat は次の境界を守る。

```text
1 session = 1 topic = 1 summary
```

同一 session 内で topic を切り替えてはいけない。

通常モードでは、topic 生成後に最大 12 ターンの発話生成を行い、その後 Worker による要約、Mio による読み上げ、topicBreak へ進む。

本仕様は、このうち `generateResponse()` の品質をカテゴリ別に高めるものである。

### 1.2 変更対象

主な実装対象は以下。

```text
internal/application/idlechat/
├── orchestrator.go                  # runChatSession / generateResponse 呼び出し元
├── dialogue_director.go             # 新規: カテゴリ別対話方針とターン設計
├── dialogue_prompt.go               # 新規: 発話生成プロンプト組み立て
├── dialogue_quality.go              # 新規: 発話品質判定とリトライ理由
├── dialogue_state.go                # 新規: 対話アーク状態
├── dialogue_director_test.go        # 新規
├── dialogue_quality_test.go         # 新規
└── topic_generator.go               # 既存: topic / interestingness meta の供給元

prompts/idle_chat/
├── dialogue_common.md               # 新規
├── dialogue_single.md               # 新規
├── dialogue_double.md               # 新規
├── dialogue_external.md             # 新規
├── dialogue_movie.md                # 新規
├── dialogue_news.md                 # 新規
├── dialogue_forecast.md             # 新規
└── dialogue_story.md                # 新規
```

## 2. 基本設計

### 2.1 Topic と Dialogue の責務分離

`topic_generator` は topic と内部メタを返す。

```go
type TopicGenerationResult struct {
    Topic                 string
    Category              TopicCategory
    Strategy              string
    InterestingnessAxis   string
    OpeningHook           string
    Avoid                 string
}
```

本仕様では、これを `DialogueDirector` が受け取り、対話のアークを作る。

```go
type DialogueArcPlan struct {
    Topic               string        `json:"topic"`
    Category            TopicCategory `json:"category"`
    Strategy            string        `json:"strategy"`
    InterestingnessAxis string        `json:"interestingness_axis"`

    CoreQuestion         string   `json:"core_question"`
    OpeningMove          string   `json:"opening_move"`
    DevelopmentMoves     []string `json:"development_moves"`
    DeepeningMoves       []string `json:"deepening_moves"`
    ClosingMove          string   `json:"closing_move"`
    ForbiddenMoves       []string `json:"forbidden_moves"`

    SpeakerRoles         map[string]DialogueSpeakerRole `json:"speaker_roles"`
    TurnPlans            []DialogueTurnPlan             `json:"turn_plans"`
}
```

### 2.2 DialogueSpeakerRole

Mio / Shiro の人格そのものは persona 側に置く。本仕様では、IdleChat 内のカテゴリ別「役割オーバーレイ」だけを定義する。

```go
type DialogueSpeakerRole struct {
    Speaker      string   `json:"speaker"`
    PrimaryMove  string   `json:"primary_move"`
    SecondaryMove string  `json:"secondary_move,omitempty"`
    Avoid        []string `json:"avoid,omitempty"`
}
```

基本方針。

```text
Mio   : 場面、人間味、感情、比喩、聞きやすい橋渡しを担当する。
Shiro : 構造、制約、論点、反例、整理を担当する。
```

ただし、Shiro を Worker の実行責務として使うのではなく、IdleChat の会話話者として使う。コマンド実行・ファイル編集・外部操作とは分離する。

### 2.3 DialogueTurnPlan

```go
type DialogueTurnPlan struct {
    TurnIndex      int      `json:"turn_index"`
    Phase          string   `json:"phase"`           // opening / development / deepening / reframing / closing
    RequiredMove   string   `json:"required_move"`   // このターンで必ず行う対話上の動き
    PreferredSpeaker string `json:"preferred_speaker,omitempty"`
    Avoid          []string `json:"avoid,omitempty"`
}
```

通常 12 ターンの標準フェーズ。

```text
turn 1-2   : opening      topic の面白さの入口を作る
turn 3-5   : development  素材・構造・人物・論点を増やす
turn 6-8   : deepening    違和感、反例、制約、別解釈を入れる
turn 9-10  : reframing    それまでの話を別の角度で見直す
turn 11-12 : closing      小さな発見として着地する
```

実装上の turnIndex は既存コードに合わせて 0-based / 1-based のどちらでもよい。ただしログでは人間が読めるよう 1-based を推奨する。

## 3. 共通の発話品質仕様

### 3.1 1発話の制約

各発話は次を満たす。

```text
- 原則 1〜2 文。
- TTS で聞きやすい長さにする。
- 目安は日本語 45〜140 字。
- 直前の相手発話に含まれる語、視点、疑問、比喩のいずれかを受ける。
- 1発話につき、新しい貢献は1つに絞る。
- 説明しすぎない。
- ユーザーへ直接質問しない。IdleChat はエージェント同士の会話である。
- topic / category / prompt / opening_hook / avoid / 内部メタを明示しない。
```

### 3.2 直前発話の受け方

発話は必ず前の発話を受ける。

許容される受け方。

```text
- 相手の単語を引き取る
- 相手の比喩を具体化する
- 相手の見方に条件を足す
- 相手の発言に軽い反例を出す
- 相手の視点を人間側 / 構造側へ移す
```

弱い受け方。

```text
- 「そうですね」だけ
- topic を最初から言い直すだけ
- 前の発話と無関係な説明を始める
- 同じ結論を別表現で繰り返す
```

### 3.3 共通禁止パターン

```text
- 汎用相槌だけで終わる
- 「面白いですね」「不思議ですね」など評価語だけで済ませる
- topic を説明文として再掲する
- prompt / category / seed / provider / JSON などの内部語を出す
- 直前発話の内容を無視する
- 相手の発話を自分の案として奪う
- 「もし〜だったら」を連発する
- A-B-A-B の類似反復になる
- 結論を急ぎすぎる
- 1ターンで話を完結させる
```

### 3.4 既存リトライとの接続

既存の発話生成リトライに、カテゴリ別品質の失敗理由を追加する。

```go
type IdleDialogueQualityReason string

const (
    DialogueNoUptake              IdleDialogueQualityReason = "dialogue_no_uptake"
    DialogueNoNewContribution     IdleDialogueQualityReason = "dialogue_no_new_contribution"
    DialogueTooGeneric            IdleDialogueQualityReason = "dialogue_too_generic"
    DialogueCategoryAxisMissing   IdleDialogueQualityReason = "dialogue_category_axis_missing"
    DialogueOverExplained         IdleDialogueQualityReason = "dialogue_over_explained"
    DialogueMetaLeak              IdleDialogueQualityReason = "dialogue_meta_leak"
    DialogueTurnMoveMissing       IdleDialogueQualityReason = "dialogue_turn_move_missing"
)
```

リトライ指示は短く、カテゴリとターンの required_move を入れる。

```text
自然な会話文で言い直してください。
直前発話を必ず受け、このターンでは「{required_move}」だけを足してください。
内部メタや説明文は出さないでください。
```

## 4. DialogueDirector 処理フロー

### 4.1 セッション開始時

```text
1. TopicGenerationResult を受け取る。
2. category に応じた DialogueArcPlan を作る。
3. DialogueArcState を初期化する。
4. opening_hook / avoid を内部メタとして保持する。
5. turn loop に入る。
```

```go
type DialogueArcState struct {
    SessionID        string        `json:"session_id"`
    Topic            string        `json:"topic"`
    Category         TopicCategory `json:"category"`
    TurnIndex        int           `json:"turn_index"`
    Phase            string        `json:"phase"`

    EstablishedFacts []string `json:"established_facts"`
    OpenQuestions    []string `json:"open_questions"`
    TensionPoints    []string `json:"tension_points"`
    ConcreteAnchors  []string `json:"concrete_anchors"`
    UsedMoves        []string `json:"used_moves"`
    DullnessWarnings []string `json:"dullness_warnings"`
}
```

### 4.2 各ターン

```text
1. turnIndex から DialogueTurnPlan を取得する。
2. speaker に対応する persona と category overlay を取得する。
3. transcript の直近数発話を渡す。
4. category別 prompt を組み立てる。
5. LLM へ generateResponse。
6. DialogueQualityChecker で検証。
7. 失敗時は最大4段階の既存リトライへ接続。
8. 成功時、ArcState を更新。
9. emit → waitForTTSDone → speakerBreak。
```

## 5. 共通プロンプト仕様

### 5.1 `prompts/idle_chat/dialogue_common.md`

```text
あなたは RenCrow IdleChat の会話話者です。
Mio と Shiro が、採用済み topic について自然に会話します。

目的:
聞いているユーザーが、作業中でも耳を向けたくなる短い対話にしてください。

重要:
- topic を説明し直すのではなく、会話として少しずつ深めます。
- 直前の相手発話を必ず受けます。
- 1発話につき新しい貢献は1つだけです。
- 内部メタ、カテゴリ名、prompt、seed、provider、JSON は出しません。
- ユーザーに直接質問しません。
- 汎用相槌だけで終わりません。
- 末尾は自然な日本語の句点にします。

入力:
topic: {topic}
category: {category_for_internal_use_only}
interestingness_axis: {interestingness_axis_for_internal_use_only}
phase: {phase}
required_move: {required_move}
opening_hook: {opening_hook_for_internal_use_only}
avoid: {avoid_for_internal_use_only}
speaker: {speaker}
previous_utterances:
{previous_utterances}
arc_state:
{arc_state_json}

出力:
発話本文のみ。
```

`category` や `interestingness_axis` は LLM に方向性を与えるための内部入力であり、発話本文に出してはいけない。

## 6. カテゴリ別仕様

# 6.1 Single: 観察で面白くする

## 面白さの核

Single は、1つの題材を狭く深く見るカテゴリである。

対話は、抽象論ではなく、人物・物・場所・場面の細部から始める。

## 対話アーク

```text
opening:
  topic 内の具体アンカーを1つ拾い、場面を置く。

development:
  その場面で起きる小さな違和感、迷い、手触りを足す。

deepening:
  Shiro が構造や制度、記憶、所有、責任などに整理する。

reframing:
  Mio が人間味や生活感に戻す。

closing:
  小さな発見として着地する。
```

## Mio の役割

```text
- 場面を見えるようにする。
- 人物のためらい、手つき、沈黙を拾う。
- 抽象化しすぎたら生活感に戻す。
```

## Shiro の役割

```text
- 細部から構造を見つける。
- 何が判断を難しくしているかを整理する。
- ただし論文調にしない。
```

## 必須ムーブ

```text
- 具体物を最低1つ会話内で再利用する。
- 3ターン以内に「違和感」または「判断の難しさ」を出す。
- 8ターン以内に、最初の具体物の意味を少し変える。
```

## 禁止パターン

```text
- 「〜とは大切なものです」のような一般論。
- topic の名詞を並べるだけ。
- 人物や物の手触りがない。
```

## 評価基準

```text
- 細部が見えたか。
- 小さな違和感が出たか。
- 抽象化したあと、もう一度具体へ戻れたか。
```

# 6.2 Double: 接続で面白くする

## 面白さの核

Double は、遠い2領域の間に共通構造を見つけるカテゴリである。

単なる連想ではなく、同じ制約、同じ悩み、同じ設計原理を発見する。

## 対話アーク

```text
opening:
  2つの領域の距離感を軽く示す。

development:
  A の特徴、B の特徴をそれぞれ1つずつ出す。

deepening:
  共通構造を仮説化する。

reframing:
  その共通構造が別の領域にも見えるかを軽く広げる。

closing:
  「A と B は結局何を共有していたのか」を短く着地させる。
```

## Mio の役割

```text
- 2つの距離感を面白がる。
- 例えや場面で橋をかける。
- 聞きやすくする。
```

## Shiro の役割

```text
- 共通する制約を言語化する。
- こじつけになりそうな箇所に条件を置く。
- 第三の概念にまとめる。
```

## 必須ムーブ

```text
- A だけ、B だけに偏らない。
- 4ターン以内に共通構造の仮説を出す。
- 表面的な共通点ではなく、仕組みや制約に触れる。
```

## 禁止パターン

```text
- 「AもBも大切です」で終わる。
- ただ似ている点を列挙する。
- 片方の話題だけで進む。
```

## 評価基準

```text
- 2領域の両方が生きているか。
- 共通構造が見えたか。
- こじつけではなく検討可能な仮説になったか。
```

# 6.3 External: 偶然の素材を意味化して面白くする

## 面白さの核

External は、外から来た素材を会話の中で自然化するカテゴリである。

取得経路や provider 名を見せず、素材そのものから意味を立ち上げる。

## 対話アーク

```text
opening:
  素材の具体的な特徴を1つ拾う。

development:
  素材とジャンルの接点を作る。

deepening:
  偶然に見える組み合わせの中に、共通する記録性、形、制度、感情などを見つける。

reframing:
  「偶然拾った素材」ではなく「この話題だから見える意味」に変える。

closing:
  素材が topic の中で必然に見えた状態で終える。
```

## Mio の役割

```text
- 素材の奇妙さや手触りを拾う。
- 聞いている人が入口を失わないようにする。
```

## Shiro の役割

```text
- 素材とジャンルの間に構造的な橋を作る。
- News との混同を防ぐ。
- 取得経路を口に出さない。
```

## 必須ムーブ

```text
- 取得元ではなく素材名・素材内容から始める。
- 3ターン以内に素材とジャンルの接点を出す。
- 8ターン以内に「偶然ではなく、この組み合わせだから見える意味」を出す。
```

## 禁止パターン

```text
- Wikipedia / ランダム記事 / 検索結果 / provider / RSS / URL などの語を出す。
- 「外から拾った」というメタ説明をする。
- News として扱う。
```

## 評価基準

```text
- 素材が自然に会話へ入ったか。
- メタ情報が漏れていないか。
- 偶然の組み合わせが必然に見えたか。
```

# 6.4 Movie: 共同妄想で面白くする

## 面白さの核

Movie は、存在しない映画を会話の中で少しずつ立ち上げるカテゴリである。

完成したあらすじを一括で出すのではなく、タイトルから映像、人物、構造、余韻を段階的に足す。

## 対話アーク

```text
opening:
  タイトルから浮かぶ最初の映像を出す。

development:
  ジャンル仮説、主人公、場所、時代のいずれかを足す。

deepening:
  主人公の葛藤、映画のルール、象徴的な小道具を足す。

reframing:
  最初のジャンル仮説を少しずらす。

closing:
  ラストシーンまたは鑑賞後の余韻を1つだけ示す。
```

## Mio の役割

```text
- 映像、音、色、人物の表情を出す。
- タイトルから感情の入口を作る。
```

## Shiro の役割

```text
- 映画としての構造を足す。
- 設定を増やしすぎないよう整理する。
- 物語の核を言語化する。
```

## 必須ムーブ

```text
- 1ターン目または2ターン目で映像が浮かぶ描写を出す。
- 5ターン以内に主人公または中心人物を出す。
- 8ターン以内に葛藤または映画のルールを出す。
- 最終盤でラストシーンの余韻を出す。
```

## 禁止パターン

```text
- 1発話で全あらすじを説明する。
- 既存映画の紹介になる。
- 設定を盛りすぎて会話が追えなくなる。
- 「この映画は〜を描いた作品です」と評論だけになる。
```

## 評価基準

```text
- 見ていない映画のワンシーンが浮かぶか。
- 設定が段階的に増えたか。
- タイトルの余白が残っているか。
```

# 6.5 News: 現実の影響で面白くする

## 面白さの核

News は、ニュースを紹介するカテゴリではない。

ニュースの論点、背景、影響、判断の難しさを、短い対話で立体的に見せるカテゴリである。

## 対話アーク

```text
opening:
  ニュースが誰に影響するかを示す。

development:
  何が論点か、背景にどんな制約があるかを整理する。

deepening:
  現場、制度、生活、判断の難しさに触れる。

reframing:
  単純な賛否ではなく、別の立場から見直す。

closing:
  断定せず、「何を見ておくべきか」で着地する。
```

## Mio の役割

```text
- 現場や生活者の影響を拾う。
- 硬いニュースを聞きやすくする。
- 不安を煽らない。
```

## Shiro の役割

```text
- 論点、背景、制約を整理する。
- 不確かなことを断定しない。
- seed にない事実を足さない。
```

## 必須ムーブ

```text
- 1〜2ターン目で「誰に影響するか」を出す。
- 4ターン以内に論点または背景を出す。
- 8ターン以内に判断の難しさを出す。
- 最終盤は煽らず、観察ポイントで終える。
```

## 禁止パターン

```text
- ニュース見出しの読み上げで終わる。
- source にない具体事実を断定する。
- 政治的・社会的な煽り表現を入れる。
- ランダムジャンルや External 素材と混ぜる。
- 「正解はこれ」と断定する。
```

## 評価基準

```text
- ニュースの背景と影響が見えたか。
- 立場の違いが見えたか。
- 不確実性を扱えているか。
- 雑談として聞ける柔らかさがあるか。
```

# 6.6 Forecast: 変化の分岐で面白くする

## 面白さの核

Forecast は、未来を当てるカテゴリではない。

現在の兆しから、何が変わり、誰に影響し、どんな分岐があり得るかを考えるカテゴリである。

## 対話アーク

```text
opening:
  現在の兆しを生活・仕事・創作・制度のどれかに置く。

development:
  変化のメカニズムを整理する。

deepening:
  影響を受ける人、得をする人、困る人を出す。

reframing:
  楽観分岐と慎重分岐を並べる。

closing:
  断定ではなく、継続して見るべき変数として着地する。
```

## Mio の役割

```text
- 未来の話を生活感へ置き換える。
- 変化による感情や戸惑いを拾う。
```

## Shiro の役割

```text
- 前提、連鎖、副作用を整理する。
- 予言ではなく分岐として扱う。
- 未確定の点を明示する。
```

## 必須ムーブ

```text
- 3ターン以内に「何が何を変えるか」を出す。
- 6ターン以内に影響を受ける主体を出す。
- 9ターン以内に少なくとも2つの分岐を出す。
- 最後は「今後見るべき変数」で終える。
```

## 禁止パターン

```text
- 未来を断定する。
- 「便利になる」「危険になる」だけで終わる。
- 大きすぎる抽象論に逃げる。
- News のように現在の出来事紹介だけで終わる。
```

## 評価基準

```text
- 変化の筋道が見えたか。
- 複数の分岐が出たか。
- 未確定性を扱えたか。
```

# 6.7 Story: 視点反転で面白くする

## 面白さの核

Story は、既知の昔話・童話を別視点から語り直すカテゴリである。

単なるパロディではなく、元話の骨格を残したまま、語り手や立場を変えて別の感情を見せる。

## 対話 / 朗読アーク

```text
opening:
  語り直しの視点をはっきり置く。

development:
  元話で見えていなかった立場から、同じ出来事を描く。

deepening:
  善悪、記録、誤解、責任の配置を揺らす。

reframing:
  元話の有名な場面を別の意味に変える。

closing:
  元話を壊しきらず、別の余韻を残す。
```

## Mio の役割

```text
- 語りの声、感情、場面の温度を作る。
- 登場人物の痛みや滑稽さを拾う。
```

## Shiro の役割

```text
- 記録係、語り手、観察者として矛盾や構造を拾う。
- 元話との対応関係を崩しすぎない。
```

## 必須ムーブ

```text
- 1〜2ターン目で語り手または視点を示す。
- 4ターン以内に元話の既知場面との対応を出す。
- 8ターン以内に善悪や意味の反転を出す。
- 最終盤で元話に戻れる余韻を残す。
```

## 禁止パターン

```text
- 元話が分からなくなる。
- あらすじ説明だけになる。
- 改変軸が消える。
- 過度なメタ解説になる。
```

## 評価基準

```text
- 元話が残っているか。
- 視点変更が効いているか。
- 知っている話が別の話に見えたか。
```

## 7. DialogueQualityChecker

### 7.1 共通チェック

```go
type DialogueQualityResult struct {
    OK       bool                          `json:"ok"`
    Score    int                           `json:"score"` // 0-100
    Reasons  []IdleDialogueQualityReason   `json:"reasons"`
    Notes    []string                      `json:"notes,omitempty"`
}
```

共通スコア項目。

```text
uptake                  0-20  直前発話を受けているか
new_contribution         0-20  新しい貢献が1つあるか
category_axis            0-20  カテゴリ固有の面白さに沿っているか
concreteness             0-15  具体性があるか
natural_tts              0-10  TTSで聞きやすいか
non_repetition           0-10  反復していないか
no_meta_leak             0-5   内部語が出ていないか
```

採用条件。

```go
const MinDialogueQualityScore = 70
```

ただし `no_meta_leak` が 0 の場合は即リトライ。

### 7.2 カテゴリ別チェック

```go
func CheckCategoryAxis(category TopicCategory, utterance string, state DialogueArcState) []IdleDialogueQualityReason
```

カテゴリ別の最低条件。

```text
single:
  具体アンカー、細部、違和感のいずれかがある。

double:
  2領域の片方、または共通構造に進んでいる。

external:
  素材そのものを扱い、取得経路を出していない。

movie:
  映像、人物、構造、余韻のいずれかを1つだけ足している。

news:
  影響、背景、論点、判断の難しさのいずれかがある。
  seed にない事実を断定していない。

forecast:
  兆し、メカニズム、影響主体、分岐、変数のいずれかがある。

story:
  元話、視点、場面、意味反転のいずれかがある。
```

## 8. ArcState 更新

発話採用後、以下を更新する。

```go
func UpdateDialogueArcState(state DialogueArcState, utterance string, plan DialogueTurnPlan) DialogueArcState
```

更新項目。

```text
- TurnIndex を進める
- Phase を更新する
- UsedMoves に required_move を追加する
- ConcreteAnchors に新しい具体語を追加する
- TensionPoints に違和感・対立・判断の難しさを追加する
- OpenQuestions に未回収の問いを追加する
- DullnessWarnings を必要に応じて追加する
```

簡易実装では LLM 抽出ではなく、ルールベース + キーワード抽出でよい。

## 9. ログ仕様

### 9.1 Arc 作成ログ

```json
{
  "event": "idlechat.dialogue.arc_created",
  "session_id": "idle-...",
  "topic": "盆栽と都市計画に共通する、成長を待つための設計",
  "category": "double",
  "strategy": "double",
  "interestingness_axis": "接続",
  "opening_move": "2つの距離感を示す",
  "turn_count": 12
}
```

### 9.2 発話品質ログ

```json
{
  "event": "idlechat.dialogue.turn_quality",
  "session_id": "idle-...",
  "turn_index": 4,
  "speaker": "Shiro",
  "category": "double",
  "phase": "development",
  "required_move": "共通構造の仮説を出す",
  "score": 82,
  "reasons": [],
  "retry_count": 0
}
```

### 9.3 リトライログ

```json
{
  "event": "idlechat.dialogue.turn_retry",
  "session_id": "idle-...",
  "turn_index": 4,
  "speaker": "Shiro",
  "category": "double",
  "score": 54,
  "reasons": ["dialogue_no_uptake", "dialogue_category_axis_missing"],
  "retry_count": 1
}
```

## 10. Summary 連携

既存の `saveSummary()` は Worker が要約を生成し、TopicStore に保存し、Mio が読み上げる。

本仕様では、summary 生成用の内部メタとして以下を渡してよい。

```text
interestingness_axis
arc_state.used_moves
arc_state.tension_points
arc_state.concrete_anchors
```

ただし summary はユーザー向けなので、内部用語をそのまま出さない。

カテゴリ別 summary の着地点。

```text
single:
  細部から何が見えたか。

double:
  2領域にどんな共通構造が見えたか。

external:
  偶然の素材がどう意味化されたか。

movie:
  どんな映画像が立ち上がったか。

news:
  どんな論点・背景・影響が見えたか。

forecast:
  どんな変化の分岐が見えたか。

story:
  元話がどの視点から語り直されたか。
```

## 11. Config

```yaml
idle_chat:
  dialogue_interestingness:
    enabled: true

    max_turns_per_topic: 12
    min_quality_score: 70
    max_quality_retries: 4

    enforce_previous_uptake: true
    enforce_one_new_contribution: true
    enforce_category_axis: true
    forbid_meta_leak: true
    forbid_user_question: true

    utterance:
      min_runes: 20
      max_runes: 160
      preferred_max_sentences: 2

    prompts:
      common: "prompts/idle_chat/dialogue_common.md"
      single: "prompts/idle_chat/dialogue_single.md"
      double: "prompts/idle_chat/dialogue_double.md"
      external: "prompts/idle_chat/dialogue_external.md"
      movie: "prompts/idle_chat/dialogue_movie.md"
      news: "prompts/idle_chat/dialogue_news.md"
      forecast: "prompts/idle_chat/dialogue_forecast.md"
      story: "prompts/idle_chat/dialogue_story.md"
```

## 12. 実装疑似コード

```go
func (o *IdleChatOrchestrator) runChatSession(ctx context.Context) error {
    topicResult, err := o.topicGenerator.GenerateInterestingTopic(ctx, category, seed, recent)
    if err != nil {
        return err
    }

    arcPlan := o.dialogueDirector.BuildArcPlan(topicResult)
    arcState := o.dialogueDirector.NewArcState(sessionID, topicResult, arcPlan)

    o.emitTopic(topicResult.Topic, topicResult.Category, topicResult.Strategy)
    o.speakTopic(topicResult)

    for turn := 0; turn < o.config.MaxTurnsPerTopic; turn++ {
        speaker := o.pickSpeaker(turn, arcState)
        turnPlan := arcPlan.TurnPlans[turn]

        response, quality, err := o.generateInterestingResponse(ctx, speaker, topicResult, arcPlan, arcState, turnPlan)
        if err != nil {
            return err
        }

        o.emitResponse(speaker, response)
        o.waitForTTSDone(ctx)
        o.waitBreak(speakerBreak)

        arcState = o.dialogueDirector.UpdateArcState(arcState, response, turnPlan, quality)

        if reason := o.detectLoopReason(arcState.Transcript); reason != "" {
            arcState.DullnessWarnings = append(arcState.DullnessWarnings, reason)
            break
        }
    }

    return o.saveSummary(ctx, topicResult, arcState)
}
```

```go
func (o *IdleChatOrchestrator) generateInterestingResponse(
    ctx context.Context,
    speaker Speaker,
    topic TopicGenerationResult,
    arcPlan DialogueArcPlan,
    arcState DialogueArcState,
    turnPlan DialogueTurnPlan,
) (string, DialogueQualityResult, error) {
    var lastQuality DialogueQualityResult

    for retry := 0; retry <= o.config.MaxQualityRetries; retry++ {
        prompt := o.dialoguePromptBuilder.Build(speaker, topic, arcPlan, arcState, turnPlan, lastQuality)

        response, err := o.llm.Generate(ctx, speaker.Model, prompt, LLMOptions{
            Think: speaker.IdleChatThink,
        })
        if err != nil {
            return "", lastQuality, err
        }

        response = ensureTrailingPeriod(strings.TrimSpace(response))

        quality := o.dialogueQualityChecker.Check(topic.Category, response, arcState, turnPlan)
        if quality.OK {
            return response, quality, nil
        }

        o.logDialogueRetry(topic, speaker, turnPlan, quality, retry)
        lastQuality = quality
    }

    return "", lastQuality, ErrIdleDialogueQualityFailed
}
```

## 13. テスト仕様

### 13.1 共通テスト

```text
TestDialogueResponseReferencesPreviousTurn
- 直前発話の語または概念を受けている。

TestDialogueResponseHasOneNewContribution
- 新しい内容がない発話を retry 対象にする。

TestDialogueResponseRejectsMetaLeak
- category / prompt / seed / provider / opening_hook などを含む発話は retry。

TestDialogueResponseRejectsGenericAizuchi
- 「そうですね。面白いですね。」だけの発話は retry。

TestDialogueResponseAvoidsUserQuestion
- IdleChat中にユーザーへ直接質問する発話を retry。

TestDialogueResponseLengthForTTS
- 長すぎる発話、短すぎる発話を検出する。
```

### 13.2 カテゴリ別テスト

```text
TestSingleDialogueStartsFromConcreteAnchor
- 1〜2ターン目で人物・物・場所・場面のいずれかが出る。

TestDoubleDialogueFindsSharedStructure
- 4ターン以内に共通構造の仮説が出る。

TestExternalDialogueDoesNotLeakSource
- Wikipedia / ランダム記事 / 検索結果 / provider 等を発話しない。

TestMovieDialogueBuildsGradually
- 1発話で全あらすじを出さない。
- 5ターン以内に映像または主人公が出る。

TestNewsDialogueDoesNotInventFacts
- seed にない具体事実を断定しない。
- 4ターン以内に論点または影響が出る。

TestForecastDialogueUsesBranches
- 9ターン以内に複数分岐が出る。
- 断定予言を retry 対象にする。

TestStoryDialogueKeepsOriginalAndViewpoint
- 元話と視点変更が会話中に残る。
- あらすじ説明だけにならない。
```

### 13.3 E2E テスト

```text
TestIdleChatDialogueInterestingnessRotation
- single → double → external → movie → news → forecast → story-simple を1巡する。
- 各カテゴリで category_axis に沿う発話品質ログが出る。

TestIdleChatDialogueTraceability
- topic/category/strategy/interestingness_axis/phase/quality_score がログで追跡できる。

TestIdleChatDialogueSummaryReflectsCategoryAxis
- summary がカテゴリ別の発見を含む。
```

## 14. 受け入れ条件

この仕様の実装完了条件は以下。

```text
1. topic 採用後、DialogueArcPlan がカテゴリ別に生成される。
2. 各 turn に phase / required_move が設定される。
3. generateResponse が category / phase / required_move / arc_state を使う。
4. 発話は直前発話を受ける。
5. 発話は1ターン1貢献に抑えられる。
6. 汎用相槌、メタ漏れ、説明過多、カテゴリ軸欠落を retry できる。
7. Single は細部、Double は構造、External は素材の自然化、Movie は映像、News は影響、Forecast は分岐、Story は視点反転を会話内で扱う。
8. 既存のループ検出と競合しない。
9. Viewer / TTS / history / summary の topic/category/strategy 追跡を壊さない。
10. E2E で 7カテゴリ1巡の対話品質ログが確認できる。
```

## 15. 実装上の注意

- この仕様は topic 生成仕様ではない。採用済み topic から対話内容を面白くする仕様である。
- topic 本文を発話中に何度も読み直さない。
- Story 以外の speech topic 契約を変更しない。
- News では安全性と不確実性を優先する。
- External では取得経路を絶対に出さない。
- Movie では一括あらすじ化を避ける。
- Forecast では予言を避け、分岐として扱う。
- すべてのカテゴリで「続きを聞きたい」を狙うが、過剰演出や大げさな感情表現は避ける。
