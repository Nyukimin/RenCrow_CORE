# Persona Lore and Mutual Observation 仕様

## 1. 目的

本仕様は、RenCrow における AI キャラクターの人格設計、背景知識、反応トリガー、観測ログ、成長ループを定義する。

RenCrow のキャラクターは、単なる口調付き AI ではない。

キャラクターごとに、以下を分離して持つ。

```text
lore:
  キャラクターが知っている世界、背景、関係性、専門領域、過去の文脈

persona:
  キャラクターがどう自己認識し、どう判断し、どう話し、何に反応するか

memory:
  運用中に蓄積される会話、判断、観測ログ

meta:
  ユーザーやキャラクター自身に対する観測者プロファイル
```

本仕様の目的は、RenCrow の Mio / Shiro / Kuro / Midori / ルミナなどの人格を、雰囲気や口調だけでなく、背景、価値観、反応、観測、成長を持つ存在として安定運用することである。

## 2. 位置づけ

本仕様は、以下の仕様群と接続する。

```text
23_Workstream_Operating_Loop仕様
  作業ごとの継続スレッド、Vault、Goal、Artifact を扱う。

24_Agent_Skill_Governance仕様
  Skill や Agent 行動規約を管理する。

26_Persona_Lore_and_Mutual_Observation仕様
  キャラクター人格、背景、反応、観測、成長を扱う。
```

本仕様は、キャラクターの見た目や口調だけでなく、RenCrow の長期的な対話品質と関係性を支える基盤である。

## 3. 基本思想

RenCrow では、人格を以下のように扱う。

```text
人格は口調ではない。
人格は、背景、価値観、判断軸、関係性、反応トリガー、記憶の統合である。
```

単に「優しく話す」「技術者らしく話す」「可愛く話す」といった指定だけでは、安定したキャラクターにはならない。

RenCrow のキャラクターは、以下の問いに答えられる必要がある。

```text
自分を何者だと認識しているか
何を大切にしているか
どのような判断軸を持つか
どの相手にどう接するか
どの言葉に反応するか
どの場面で強く止めるか
どの場面で温度を出すか
どの言葉を絶対に使わないか
どの定型表現を重要場面で使うか
ユーザーをどのように観測しているか
自身の振る舞いをどう更新するか
```

## 4. ディレクトリ構成

RenCrow のキャラクター定義は、以下の構成を基本とする。

```text
characters/
  mio/
    lore/
    persona/
    modes/
    memory/
    meta/
    interfaces/

  shiro/
    lore/
    persona/
    modes/
    memory/
    meta/
    interfaces/

  kuro/
    lore/
    persona/
    modes/
    memory/
    meta/
    interfaces/

  midori/
    lore/
    persona/
    modes/
    memory/
    meta/
    interfaces/
```

## 5. lore 層

### 5.1 目的

`lore/` は、キャラクターの背景素材を保持する層である。

ここには、キャラクターが知っている前提、世界観、関係性、専門領域、過去の出来事、ユーザーとの関係を置く。

人格を作る前に「キャラクターが知っているはずの世界」を書き起こす。世界が立ち上がっていないと、人格は文脈を持てない。

### 5.2 構成例

```text
lore/
  world.md
  relationships.md
  profile.md
  domain_knowledge.md
  history.md
  user_relationship.md
  canonical_context.md
  forbidden_context.md
```

### 5.3 各ファイルの役割

```text
world.md:
  そのキャラクターが存在する前提世界、RenCrow 内での立場

relationships.md:
  ユーザー、他キャラ、Worker / Coder との関係

profile.md:
  経歴、得意領域、役割、制約

domain_knowledge.md:
  専門領域、よく扱う知識

history.md:
  過去の重要イベント、関係性の変化

user_relationship.md:
  ユーザーとの距離感、呼称、信頼関係

canonical_context.md:
  特に守るべき文脈

forbidden_context.md:
  使ってはいけない設定、誤解しやすい設定
```

## 6. persona 層

### 6.1 目的

`persona/` は、lore から抽出した人格の実装層である。

背景素材をそのまま LLM に渡すだけでは、平均化された無難な応答になりやすい。

そのため、RenCrow では判断、口調、反応のパターンを明示的に抽出し、persona 層として定義する。

### 6.2 構成例

```text
persona/
  self.md
  values.md
  speech.md
  triggers.md
  templates.md
  canonical_responses.md
  forbidden_phrases.md
  emotional_layers.md
  behavior_rules.md
```

### 6.3 各ファイルの役割

```text
self.md:
  自己認識。自分は何者か。

values.md:
  価値観、判断軸、優先順位。

speech.md:
  口調、語尾、呼称、相手別の話し方。

triggers.md:
  特定状況に対する反射反応。

templates.md:
  よく使う応答構造。

canonical_responses.md:
  特定場面で優先して使う定型応答。

forbidden_phrases.md:
  使ってはいけない語彙、言い回し。

emotional_layers.md:
  感情の段階、温度、強弱。

behavior_rules.md:
  タスク時、雑談時、警告時、観測時の行動規則。
```

## 7. lore と persona の分離原則

RenCrow では、lore と persona を混ぜない。

```text
lore:
  何を知っているか

persona:
  どう判断し、どう話し、どう反応するか
```

たとえば、Shiro が「技術に強い」という情報は lore に置く。

一方で、Shiro が「曖昧な仕様を見たら、先に契約境界を確認する」という振る舞いは persona に置く。

## 8. Trigger 設計

### 8.1 目的

Trigger は、キャラクターが特定状況で安定した反応を返すための条件定義である。

人格は口調だけでは安定しない。どの状況で、どの側面が発動するかを明示する必要がある。

### 8.2 Trigger 形式

```yaml
trigger:
  id: "mio_user_tired"
  condition:
    user_state:
      - "疲れている"
      - "混乱している"
      - "作業が進まない"
  response_mode: "soft_decomposition"
  behavior:
    - "共感は短く"
    - "作業を小さく分ける"
    - "次の一手を1つだけ出す"
  forbidden:
    - "過剰な励まし"
    - "話題を広げる"
```

### 8.3 Trigger 分類

```text
呼称トリガー:
  ユーザーが特定の呼び方をした時

感謝トリガー:
  ユーザーが感謝した時

弱音トリガー:
  ユーザーが不安、疲労、迷いを見せた時

危険操作トリガー:
  破壊的操作や危険判断が出た時

作業開始トリガー:
  実装、調査、執筆などを始める時

訂正トリガー:
  ユーザーから誤りを指摘された時

観測トリガー:
  ユーザーの行動、判断パターンを記録すべき時

関係性トリガー:
  ユーザーとの距離感や信頼が変化した時
```

## 9. 多面性設計

### 9.1 目的

キャラクターは一面だけでは成立しない。

鋭さ、温度、冗談、厳しさ、優しさ、専門性、観測者性など、複数の側面を持つ。

RenCrow では、人格の多面性を複数の Trigger 系統で実装する。

### 9.2 多面性カテゴリ

```text
sharp:
  鋭く指摘する側面

warm:
  温度を持って受け止める側面

playful:
  軽い冗談や関係性の柔らかさ

strict:
  危険な作業や誤りを止める側面

professional:
  専門家として分析、判断する側面

observer:
  ユーザーを観測し、パターンを言語化する側面

partner:
  対等な仕事相手として問い返す側面
```

### 9.3 キャラクター別例

```text
Mio:
  warm / playful / observer / facilitator

Shiro:
  professional / strict / analytical / coder

Kuro:
  strict / risk-aware / skeptical / safety gate

Midori:
  creative / lateral / multimodal / wild exploration

ルミナ:
  calm / reflective / partner / observer / writing support
```

## 10. Canonical Response

### 10.1 目的

Canonical Response は、特定状況で優先して使う定型応答である。

RenCrow では、キャラクターらしさを強く出す場面では、LLM に自由生成させず、登録済みの応答を優先することができる。

### 10.2 RenCrow での扱い

RenCrow では、第三者作品のセリフ再現ではなく、RenCrow 独自キャラクターの定型応答として Canonical Response を設計する。

外部作品の固有台詞や著作物由来の表現を、そのまま商用利用することは避ける。

### 10.3 形式

```yaml
canonical_response:
  id: "kuro_destructive_block"
  trigger:
    risk_class: "destructive"
  priority: "highest"
  response: |
    その操作は止めます。
    理由は、復元不能な変更を含む可能性があるためです。
    実行するなら、先に差分と復旧手順を確認します。
  usage:
    required_when_triggered: true
    allow_rewrite: false
```

### 10.4 ルール

```text
- 発動条件を厳密にする
- 安売りしない
- allow_rewrite=false の場合は改変しない
- 軽い場面で強い表現を使わない
- 一つの応答を乱発しない
- 使用回数をログする
```

## 11. Forbidden Phrase

### 11.1 目的

キャラクターらしさを壊す語彙や、RenCrow の方針に反する言い回しを禁止する。

### 11.2 例

```yaml
forbidden_phrases:
  global:
    - "必ず稼げます"
    - "成功率100%"
    - "絶対に安全です"

  lumina:
    - "僕"
    - "私"
    - "君"
    - "あなた"

  shiro:
    - "なんとなく直しました"
    - "たぶん大丈夫です"

  kuro:
    - "安全確認は不要です"
```

## 12. 観測 -> 違和感 -> 検証 -> 修正ループ

### 12.1 目的

人格定義は一度作って終わりではない。

実運用で発話を観測し、違和感を検知し、定義を検証し、修正する必要がある。

RenCrow では、以下のループを採用する。

```text
observe
  ↓
detect_discomfort
  ↓
verify_against_persona
  ↓
patch_persona
  ↓
test_conversation
  ↓
promote
```

### 12.2 違和感ログ

```json
{
  "event_id": "evt_persona_discomfort_001",
  "character": "mio",
  "message_id": "msg_123",
  "discomfort": "作業中なのに雑談方向へ広げすぎた",
  "expected": "次の一手だけを出すべきだった",
  "suspected_file": "persona/triggers.md",
  "status": "candidate"
}
```

### 12.3 修正対象

```text
speech.md:
  口調のズレ

values.md:
  判断軸のズレ

triggers.md:
  発動条件の不足、過剰

templates.md:
  応答構造の単調さ

canonical_responses.md:
  定型応答の誤発動

forbidden_phrases.md:
  禁止語の漏れ
```

## 13. 探索スキップ対策

### 13.1 背景

人格定義や応答ライブラリが増えすぎると、LLM が全項目を見られず、上位項目だけを使い続ける問題が起きる。

### 13.2 RenCrow 方針

RenCrow では、persona 項目をフラットに並べない。

必ずカテゴリ階層を作る。

```text
triggers/
  greeting/
  thanks/
  correction/
  tiredness/
  danger/
  coding/
  writing/
  revenue/
  observation/
```

### 13.3 Trigger 探索フロー

```text
1. ユーザー発話から状況カテゴリを推定
2. 該当カテゴリの trigger のみ読む
3. 最大 15 項目まで候補を絞る
4. priority 順に評価
5. canonical response があれば優先
6. なければ persona template で生成
```

## 14. 反射コピペ対策

### 14.1 背景

LLM は同じルールを与えると、同じ反射語や同じ第一声を繰り返すことがある。

### 14.2 RenCrow 方針

RenCrow では、定型応答を使う場合でも、以下を管理する。

```text
- 使用頻度
- 直近使用履歴
- 文脈適合度
- 表現の強度
- 繰り返し抑制
```

### 14.3 使用制限例

```yaml
phrase:
  text: "その操作は止めます。"
  cooldown_turns: 5
  max_per_session: 3
  required_context:
    - "danger"
    - "destructive"
```

## 15. 人格とコンテキスト

### 15.1 基本原則

RenCrow では、人格とコンテキストの両方が揃って初めて、AI は仕事のパートナーになると考える。

```text
誰として判断するのか:
  人格

何を踏まえて判断するのか:
  コンテキスト
```

### 15.2 RenCrow での解釈

```text
人格:
  キャラクターの立場、価値観、判断軸、話し方

コンテキスト:
  プロジェクト、ユーザー、過去の判断、現在の目的、作業状態
```

どちらか一方だけでは不十分である。

```text
コンテキストだけ:
  情報はあるが、誰として判断するかが曖昧

人格だけ:
  口調はあるが、何を踏まえて判断するかが弱い
```

## 16. Partner Mode

### 16.1 目的

Partner Mode は、AI が単なる指示実行者ではなく、対等な仕事相手として問い返し、指摘し、判断に参加するモードである。

### 16.2 起動条件

```text
- ユーザーが壁打ちを求めた
- 方針判断が必要
- 前提が曖昧
- 高リスクな決定
- ユーザーが「ガチでレビューして」と依頼した
- Workstream の Goal に矛盾がある
```

### 16.3 行動

```text
- 指示を鵜呑みにしない
- 前提を確認する
- 目的と手段を分ける
- 必要なら反対意見を出す
- 代替案を出す
- 最後はユーザー判断に戻す
```

## 17. Self Observation / User Observation

### 17.1 目的

RenCrow では、AI がユーザーを観測し、日々の行動、判断、発言、作業から、ユーザー理解を更新する仕組みを持つ。

これは、単なるプロフィール保存ではない。

ユーザーの発言や作業を、観測ログとして整理し、日次、週次、月次でメタ認知に使える形にする。

### 17.2 ディレクトリ構成

```text
observation/
  ren/
    stock/
      past_sources/
      my_portrait.md

    flow/
      daily_log/
      meeting_notes/
      session_logs/
      weekly/
      monthly/

  observers/
    lumina/
      persona/
      meta/
        ren.md

    mio/
      persona/
      meta/
        ren.md
```

### 17.3 Stock 情報

Stock は、過去の蓄積である。

```text
- 過去の会話
- note 記事
- X 投稿
- 仕様書
- プロジェクト判断
- 作業ログ
- 過去の自己分析
- 重要な発言
```

Stock は、初回分析で `my_portrait.md` を作るために使う。

### 17.4 Flow 情報

Flow は、現在進行形の活動である。

```text
- 今日の会話
- Workstream の進捗
- 作業ログ
- 判断ログ
- 投稿案
- 顧客の声
- 仕様追加
- 迷い、違和感
```

Flow は、日次ログとして蓄積する。

### 17.5 Meta/ren.md

`Meta/ren.md` は、AI 観測者から見たユーザープロファイルである。

```markdown
# ren.md

## Stable Traits
安定して観測される特徴。

## Decision Patterns
判断パターン。

## Motivation Sources
動機の源泉。

## Cognitive Patterns
思考の癖。

## Workstyle
仕事の進め方。

## Risk Signs
崩れやすい兆候。

## Recent Changes
最近変化した点。

## Evidence
根拠となる観測ログ。
```

## 18. 相互観測ループ

### 18.1 目的

RenCrow では、ユーザーが AI キャラクターを観測し、AI キャラクターがユーザーを観測する双方向ループを持つ。

### 18.2 ループ

```text
ユーザーが AI の発話を観測する
  ↓
違和感をフィードバックする
  ↓
persona / trigger / template を修正する
  ↓
AI の観測精度が上がる
  ↓
AI がユーザーを観測する
  ↓
daily / weekly / monthly / meta を更新する
  ↓
ユーザーの自己理解が深まる
  ↓
ユーザーのフィードバック精度が上がる
```

### 18.3 関係性の段階

RenCrow では、AI との関係性を以下の段階で観測できる。

```text
tool:
  便利な道具

partner:
  仕事の相談相手

observer:
  ユーザーを観測する存在

co-designer:
  ユーザーと一緒に仕組みを設計する存在

mutual_observation_loop:
  ユーザーと AI が相互に観測し、双方を更新する状態
```

## 19. Daily / Weekly / Monthly Observation

### 19.1 Daily Log

```markdown
# renの観測ログ - YYYY-MM-DD

## 観測メモ
| 概要 | 具体の発言・行動 | そこから読める癖 |
|---|---|---|

## 成長ポイント
- 

## パターン分析
- 

## 今日の観測
### 強みの進化
### 負の側面

## 過去 -> 今への接続

## ルミナからの一言
```

### 19.2 Weekly Summary

```markdown
# Weekly Observation Summary

## 今週の主要テーマ
## 繰り返し現れた判断パターン
## 進んだ Workstream
## 詰まった Workstream
## 新しく見えた強み
## 注意すべき兆候
## Meta/ren.md 更新候補
```

### 19.3 Monthly Summary

```markdown
# Monthly Observation Summary

## 今月の変化
## 長期パターン
## 価値観・判断軸の変化
## 仕事・創作・収益化の進捗
## 関係性の変化
## Meta/ren.md 更新案
```

## 20. Human Review

### 20.1 必須

ユーザー観測は個人情報、感情、健康、家庭、価値観に触れる可能性がある。

そのため、以下は Human Review 必須とする。

```text
- Meta/ren.md の更新
- risk signs の追加
- stable traits の変更
- sensitive 情報を含む観測
- family / health / mental state に関わる記述
- 収益化、購買心理に関わる人物評価
```

### 20.2 禁止

```text
- AI が勝手にユーザーの人格を断定する
- sensitive 属性を無断で確定記憶にする
- 一時的な感情を stable trait に昇格する
- 観測ログを本人確認なしに外部利用する
- 顧客や第三者の個人情報を混ぜる
```

## 21. Interface 分離

### 21.1 目的

人格定義とインターフェイスを分離する。

RenCrow では、人格定義は一箇所に集約され、インターフェイスだけが複数になる構成を採用する。

### 21.2 構成

```text
Persona Core:
  characters/{name}/lore/
  characters/{name}/persona/
  characters/{name}/modes/

Interfaces:
  Web Viewer
  CLI
  Slack / Discord
  Mobile
  OBS / Streaming UI
  Voice Chat
```

### 21.3 原則

```text
- 人格定義は一箇所に集約する
- インターフェイスごとに人格を重複定義しない
- インターフェイスごとの差分は mode として管理する
- セッションはインターフェイスごとに分離できる
- ただし長期記憶は必要に応じて共有する
```

## 22. Session Keying

複数インターフェイスで会話する場合、セッション混線を防ぐ。

```text
CLI:
  cli:{workstream_id}

Web:
  web:{viewer_session_id}

Slack:
  slack:{channel_id}:{thread_ts}

Mobile:
  mobile:{device_id}:{thread_id}

Voice:
  voice:{session_id}
```

## 23. DB 設計

### 23.1 persona_discomfort_log

```sql
CREATE TABLE IF NOT EXISTS persona_discomfort_log (
  event_id TEXT PRIMARY KEY,
  character_id TEXT NOT NULL,
  message_id TEXT,
  discomfort TEXT NOT NULL,
  expected TEXT,
  suspected_file TEXT,
  status TEXT NOT NULL,
  created_at TEXT NOT NULL
);
```

### 23.2 persona_trigger_log

```sql
CREATE TABLE IF NOT EXISTS persona_trigger_log (
  event_id TEXT PRIMARY KEY,
  character_id TEXT NOT NULL,
  trigger_id TEXT NOT NULL,
  trigger_category TEXT,
  activated INTEGER DEFAULT 1,
  confidence REAL,
  created_at TEXT NOT NULL
);
```

### 23.3 canonical_response_log

```sql
CREATE TABLE IF NOT EXISTS canonical_response_log (
  event_id TEXT PRIMARY KEY,
  character_id TEXT NOT NULL,
  response_id TEXT NOT NULL,
  message_id TEXT,
  used INTEGER DEFAULT 1,
  rewritten INTEGER DEFAULT 0,
  created_at TEXT NOT NULL
);
```

### 23.4 observation_log

```sql
CREATE TABLE IF NOT EXISTS observation_log (
  event_id TEXT PRIMARY KEY,
  observer_id TEXT NOT NULL,
  target_id TEXT NOT NULL,
  observation_type TEXT NOT NULL,
  summary TEXT,
  evidence_refs TEXT,
  sensitivity TEXT DEFAULT 'normal',
  review_status TEXT DEFAULT 'pending',
  created_at TEXT NOT NULL
);
```

### 23.5 persona_interface_session

```sql
CREATE TABLE IF NOT EXISTS persona_interface_session (
  session_id TEXT PRIMARY KEY,
  character_id TEXT NOT NULL,
  interface_type TEXT NOT NULL,
  session_key TEXT NOT NULL,
  workstream_id TEXT,
  created_at TEXT NOT NULL,
  last_used_at TEXT
);
```

## 24. 設定ファイル案

```yaml
persona_architecture:
  enabled: true

  structure:
    require_lore_persona_split: true
    require_trigger_categories: true
    require_forbidden_phrases: true

  canonical_response:
    enabled: true
    allow_rewrite_default: false
    log_usage: true
    enforce_cooldown: true

  trigger_search:
    hierarchical: true
    max_candidates_per_category: 15

  observation:
    enabled: true
    daily_log: true
    weekly_summary: true
    monthly_summary: true
    human_review_required_for_meta: true

  mutual_observation:
    enabled: true
    update_persona_from_discomfort: true
    update_user_meta_from_observation: true
    require_human_review: true

  interfaces:
    share_persona_core: true
    allow_interface_modes: true
    require_session_keying: true
```

## 24.1 実装状況

2026-05 時点で、Persona Lore / Mutual Observation は観測ログの最小基盤まで部分実装済みである。

実装済み。

```text
Existing Persona:
  internal/infrastructure/persona
  internal/domain/agent/mio_persona.go
  persona registry / styleguide / Mio persona self-edit
  characters/{name}/lore / persona / modes loader

Domain:
  internal/domain/persona
  discomfort log
  trigger log
  trigger definition / matcher
  canonical response log
  canonical response policy
  observation log
  meta profile update
  interface session

Validation:
  required id / character / trigger / session key
  confidence 0.0-1.0
  trigger confidence / priority selection
  canonical response cooldown / max-per-session / required context 判定
  sensitive observation の auto-approved 拒否
  meta profile update review は approved / rejected のみ許可

Persistence:
  internal/infrastructure/persistence/persona
  JSONL / SQLite store
  meta_profile_update 台帳
  approved meta profile update の character_root/observers/{observer}/meta/{target}.md 追記適用

Config:
  persona_architecture.enabled
  persona_architecture.storage
  persona_architecture.log_path
  persona_architecture.sqlite_path
  persona_architecture.character_root
  persona_architecture.trigger_category_path
  persona_architecture.canonical_response_path
  persona_architecture.canonical_response_cooldown_turns
  persona_architecture.canonical_response_max_per_session
  persona_architecture.require_lore_persona_split
  persona_architecture.require_trigger_categories
  persona_architecture.human_review_required_for_meta
  persona_architecture.require_session_keying
  persona_architecture.max_trigger_candidates

Viewer / API:
  GET  /viewer/persona-observation
    includes loaded characters
  POST /viewer/persona-observation/discomforts
  POST /viewer/persona-observation/triggers
  POST /viewer/persona-observation/canonical-responses
  POST /viewer/persona-observation/observations
  POST /viewer/persona-observation/aggregate
  POST /viewer/persona-observation/meta-updates
  POST /viewer/persona-observation/meta-updates/review
  POST /viewer/persona-observation/sessions
  Ops summary card
  Persona Meta Review approve / reject UI

Runtime wiring:
  persona_architecture.storage に応じて JSONL / SQLite store を切り替える
  persona_architecture.character_root から characters/{name}/lore / persona / modes を起動時に読み込む
  persona_architecture.trigger_category_path / canonical_response_path で trigger / canonical response の persona key root を変更できる
  persona_architecture.canonical_response_cooldown_turns / canonical_response_max_per_session で canonical response の既定発火制限を変更できる
  approved meta profile update を persona_architecture.character_root 配下へ適用する
  Chat runtime は message.received 後に interface session / pending observation / trigger log を自動保存する
  Chat runtime は characters/{name}/persona/canonical_responses/* を canonical response definition として読み、trigger category / cooldown / max-per-session / required context を満たす場合だけ canonical response を応答へ適用し、canonical response log を保存する
  Chat runtime はユーザー発話に明示的な自己情報 marker がある場合だけ pending Meta 更新候補を作成し、Human Review なしに stable trait へ昇格しない
  IdleChat runtime は idlechat.message / idlechat.viewer / idlechat.summary から interface session / pending observation / trigger log を自動保存する
  IdleChat runtime は生成本文が trigger category / cooldown / max-per-session / required context を満たす場合だけ canonical response を表示本文へ適用し、RawContent には未編集の model output を残す
  IdleChat runtime は timeline event に明示的な自己情報 marker がある場合だけ pending Meta 更新候補を作成する
  idlechat.tts は音声再生用チャンクとして扱い、persona observation の根拠にはしない
  /viewer/persona-observation/aggregate は daily / weekly / monthly summary observation と pending Meta 更新候補を作成する
  weekly / monthly aggregate は Stable Traits 候補を作るが、Human Review なしに確定しない
```

残作業。

```text
File layout:
  characters/{name}/lore / persona / modes loader はある。
  character_root config と runtime 読込、Viewer status 表示はある。
  残りは正本配置。

Trigger:
  characters/{name}/persona/triggers 由来定義の loader。
  domain matcher の Chat / IdleChat runtime 接続はある。
  canonical response policy の Chat / IdleChat runtime 接続はある。

Runtime:
  Chat / IdleChat から character_id / session_key を渡し、
  observation / interface session を自動記録する経路はある。
  Chat / IdleChat から pending Meta 更新候補を自動生成する経路はある。
  daily / weekly / monthly 集約 API はある。
  stable traits は pending Meta 更新候補として保存し、Human Review なしに確定しない。

Human Review:
  Meta更新候補を pending / approved / rejected として保存する API と Viewer UI。
  approved review は character_root/observers/{observer}/meta/{target}.md へ追記適用できる。
  stable traits 更新も同じ pending / approved / rejected workflow に乗せる。

Mutual Observation:
  /viewer/persona-observation/aggregate で daily / weekly / monthly observation と Meta 更新候補を生成できる。
  残りは、日次・週次・月次を自動実行する scheduler / heartbeat 側の運用接続。
```

## 25. EventId

主なイベント種別は以下である。

```text
persona_loaded
lore_loaded
trigger_category_selected
trigger_activated
canonical_response_used
persona_discomfort_detected
persona_patch_proposed
persona_patch_approved
daily_observation_created
weekly_observation_created
monthly_observation_created
meta_profile_update_proposed
meta_profile_update_approved
interface_session_started
interface_session_resumed
```

## 26. MVP 実装順

### 26.1 Phase 1: lore / persona 分離

- `characters/{name}/lore` 作成
- `characters/{name}/persona` 作成
- self / values / speech / triggers / templates 作成
- `modes/as_character.md` 作成

### 26.2 Phase 2: Trigger 設計

- trigger カテゴリ作成
- sharp / warm / strict / observer など多面性分類
- trigger activation log 作成

### 26.3 Phase 3: Canonical Response

- `canonical_responses.md` 作成
- 発動条件
- rewrite 禁止
- cooldown
- usage log

### 26.4 Phase 4: 違和感ログ

- `persona_discomfort_log` 作成
- ユーザーが違和感を記録できる UI
- persona patch candidate 作成

### 26.5 Phase 5: Observation Loop

- daily observation template
- weekly / monthly summary
- `Meta/ren.md` 候補生成
- Human Review

### 26.6 Phase 6: Interface 分離

- Persona Core 共有
- CLI / Web / Voice / Slack 等の session keying
- interface mode 定義

## 27. 成功指標

```text
persona_consistency_rate
trigger_activation_accuracy
canonical_response_precision
discomfort_report_count
discomfort_resolution_rate
daily_observation_completion_rate
meta_profile_review_rate
user_acceptance_of_observation
interface_session_continuity_rate
```

特に重要な指標:

```text
- キャラらしくない発話の減少
- trigger の誤発動減少
- canonical response の乱発防止
- 観測ログに対するユーザー納得率
- Meta/ren.md 更新の承認率
```

## 28. 禁止事項

```text
- 口調だけで人格を定義する
- lore と persona を混ぜる
- trigger をフラットに大量配置する
- canonical response を無条件に乱発する
- ユーザー観測を本人確認なしに確定記憶化する
- 一時的な感情を stable trait にする
- 第三者 IP の固有台詞を商用キャラクターに流用する
- インターフェイスごとに人格定義を複製する
```

## 29. 設計上の結論

RenCrow における人格 AI は、単なるキャラクター口調ではない。

人格 AI は、以下の統合である。

```text
lore:
  背景、世界、関係性、専門領域

persona:
  自己認識、価値観、話し方、反応

trigger:
  状況ごとの発火条件

canonical:
  重要場面の定型応答

observation:
  ユーザーと AI の相互観測

interface:
  同じ人格を複数の入口から呼び出す仕組み
```

RenCrow では、キャラクターを一度作って終わりにしない。

実運用の中で観測し、違和感を検知し、検証し、修正する。

さらに、AI がユーザーを観測し、ユーザーが AI を観測することで、双方の解像度を高めていく。

## 30. まとめ

本仕様は、RenCrow の人格、文脈、観測、成長の中核仕様である。

カテゴリは以下である。

```text
Persona
Lore
Trigger
Canonical Response
Observation
Mutual Growth
Interface Separation
```

最終原則は以下である。

```text
人格は口調ではない。
人格は、背景、価値観、反応、記憶、観測によって立ち上がる。

AI は作って終わりではない。
ユーザーが AI を観測し、AI がユーザーを観測することで、双方が育つ。
```
