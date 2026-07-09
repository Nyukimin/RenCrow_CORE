# RenCrow 記憶システム仕様書
## れんを学習し続け、よりよく寄り添うエージェントシステム

最終更新: 2026-05-23  
対象: RenCrow / Chat / Worker / Coder / Persona Agents  
位置づけ: 記憶システムの正本仕様

---

## 0. この仕様の目的

RenCrowの記憶システムは、単なるRAG、検索システム、ログ保存、ナレッジDBではない。

目的は、れんの考え方、好み、作業の進め方、過去の判断、継続中のプロジェクト、会話の温度感を学習し続け、れんにとってよりよく寄り添うエージェントシステムを作ることである。

ここでいう「寄り添う」とは、過剰に共感することではない。  
れんの意図を先回りして汲み取り、必要な記憶を適切に思い出し、現在の作業に自然につなげ、必要なときは率直に危うさを指摘することを指す。

RenCrowは、れんのための「記憶を持つ作業机」である。  
過去の会話、作業、設計、判断、好み、関係性を、毎ターンの応答に静かに反映する。

---

## 1. 基本思想

RenCrowの中心は、ローカルLLMそのものではなく、れんを理解し続ける記憶OSである。

```text
検索APIは外部センサー
DBは記憶の土台
RAGは知識参照の仕組み
ユーザー記憶は user:<uid> に分離
キャラクター記憶は人格成長用
会話記憶は現在の文脈維持用
Knowledge DBは外部知識用
ローカルLLMは、それらを材料にして推論するエンジン
```

重要なのは、すべてを1つの巨大なRAGに押し込まないこと。

RenCrowでは、以下を明確に分ける。

```text
れん自身に関する記憶
れんとの関係性に関する記憶
現在の会話の文脈
作業・プロジェクトの記憶
キャラクターごとの成長記憶
外部知識DB
ニュース・最新情報
完全保存ログ
```

---

## 2. RenCrowが学習し続ける対象

RenCrowが学習する対象は、主に以下である。

| 分類 | 内容 | 目的 |
|---|---|---|
| ユーザープロファイル | 職業、活動領域、長期プロジェクト、好み | 応答の前提を整える |
| 出力嗜好 | 短さ、論理性、文体、禁止事項 | 返答品質の安定 |
| 思考傾向 | 何を重視するか、どこに違和感を持つか | 意図理解と先回り |
| 作業履歴 | 設計、実装、調査、記事作成の流れ | 継続作業の復帰 |
| 判断履歴 | 過去に選んだ方針、捨てた案、理由 | 同じ議論の蒸し返し防止 |
| 関係性 | ルミナや他キャラとの距離感 | 対話の自然さ |
| プロジェクト記憶 | RenCrow、BipolarToBalance、AIの血脈など | 文脈維持 |
| 注意点 | 避けたい表現、危険な作業、確認が必要な領域 | 安全性と信頼性 |
| エピソード | ある日の会話や気づき | 想起と連想 |
| 外部知識 | 映画、技術、ニュース、論文など | RAG・説明・雑談 |

ただし、すべてを無条件に「確定記憶」へ入れない。  
会話中の観測は、まず候補として扱い、必要に応じて昇格させる。

---

## 3. 記憶の種類

RenCrowの記憶は、少なくとも4種類に分離する。

| 種類 | namespace | 内容 | 共有範囲 |
|---|---|---|---|
| 会話記憶 | `conv:<thread_id>` | 今の会話、今日、今月、長期エピソード | 参加キャラで共有 |
| ユーザー記憶 | `user:<uid>` | 嗜好、プロフィール、プロジェクト、制約 | 全キャラが参照可 |
| キャラ記憶 | `char:<persona>` | 口調成長、KPI、得意不得意、れんとの関係性 | 基本は本人、必要時共有 |
| 知識DB | `kb:<domain>` | 映画、音楽、ニュース、技術知識 | 全キャラが参照可 |

この分離は必須である。

たとえば、以下を混ぜてはいけない。

```text
れんの出力嗜好
Mioの人格設定
映画作品データ
今日の会話ログ
OpenAI公式ブログの要約
```

これらはすべて「記憶」ではあるが、用途、信頼度、更新頻度、削除条件が違う。

---

## 4. 記憶レイヤー

RenCrowは、時間軸に沿って記憶を4層に分ける。

| 層 | 名前 | 推奨ストア | 内容 |
|---|---|---|---|
| L0 | 現在会話 | LangGraph state / checkpoint | 現在スレッド、直近発話、割り込み状態 |
| L1 | 今日の記憶 | SQLite WAL | 今日のエピソード、イベントログ、検索キャッシュ |
| L2 | 今月の記憶 | DuckDB + Parquet | 月次ハイライト、日次ダイジェスト、話題クラスタ |
| L3 | 長期記憶 | DuckDB + Parquet + Vector sidecar | ユーザー嗜好、プロジェクト記憶、長期エピソード |

### 4.1 L0 現在会話

L0は、いま進行中の会話スレッドである。

保持するもの。

```text
直近メッセージ
現在の話題
未解決の論点
ユーザーの最新意図
割り込み状態
次に話すべきキャラ
```

L0は長期保存の場所ではない。  
古くなった会話は、要約されてL1以降へ送られる。

### 4.2 L1 今日の記憶

L1は、今日の会話・作業・判断を保持する。

保持するもの。

```text
今日話したこと
今日決めたこと
今日の作業状態
今日の未完了タスク
今日の検索キャッシュ
今日のEvent Log
```

L1は、れんが同じ日に何度も作業に戻るための記憶である。  
「さっきの続き」「今日決めた方針」「今朝の話」を自然に思い出すために使う。

### 4.3 L2 今月の記憶

L2は、日々の記憶を整理した月次・週次の記憶である。

保持するもの。

```text
今月の主要テーマ
継続中のプロジェクト状態
週次・月次ハイライト
よく出た論点
繰り返された判断
作業の流れ
```

L2は、数日〜数週間の文脈復帰に使う。

### 4.4 L3 長期記憶

L3は、れんを長く理解するための記憶である。

保持するもの。

```text
れんの恒常的な好み
長期プロジェクト
重要な設計判断
繰り返し使う作業方針
関係性の前提
明示的に保存された記憶
高頻度で参照されるエピソード
```

L3は、VectorDBによる意味検索と、DuckDBによるメタ管理を併用する。

---

## 5. ユーザー記憶の型

`user:<uid>` には、次の型を持たせる。

| type | 内容 | 保存方針 |
|---|---|---|
| `profile` | 生年、職業、活動領域 | 明示情報のみ。長期保持 |
| `preference` | 短く論理的な文章が好み、PowerShellを好む等 | 明示＋反復観測。更新可能 |
| `project` | RenCrow、BipolarToBalance、AIの血脈など | 長期記憶。関連エピソードと接続 |
| `constraint` | 絵文字不要、過剰共感不要、Canvas禁止など | 強く保持。プロンプトに反映 |
| `relationship` | ルミナとの距離感、呼称、対話のテンポ | 慎重に保持。人格制御に使用 |
| `episode` | ある日の議論や判断 | L1→L2→L3へ圧縮 |
| `skill` | AI設計、組込み、執筆、画像生成など | 推論補助に使用 |
| `sensitive` | 健康、家庭、個人事情など | 原則保存しないか、明示確認つき |

### ユーザー記憶レコード例

```json
{
  "memory_id": "usrmem_001",
  "user_id": "ren",
  "type": "preference",
  "statement": "れんさんは短く、読みやすく、論理の通った文章を好む",
  "evidence_event_ids": ["evt_001", "evt_118"],
  "confidence": 0.92,
  "sensitivity": "normal",
  "scope": "all_personas",
  "created_at": "2026-05-01T12:00:00+09:00",
  "last_confirmed_at": "2026-05-01T12:00:00+09:00",
  "ttl_policy": "pinned",
  "superseded_by": null,
  "embedding_id": "vec_001"
}
```

---

## 6. 記憶の昇格ルール

RenCrowは、会話から推測した内容をいきなり長期記憶へ確定しない。

記憶候補は、次の段階を通る。

```text
observed
  ↓
candidate
  ↓
confirmed
  ↓
pinned
```

| 状態 | 意味 |
|---|---|
| `observed` | 1回の会話で観測された |
| `candidate` | 複数回出た、または重要そう |
| `confirmed` | れんが明示した、または十分に確度が高い |
| `pinned` | 常に参照すべき重要記憶 |

### 昇格条件

```text
れんが「覚えて」と言った
同じ嗜好・制約が複数回現れた
重要プロジェクトに関わる判断だった
以後の応答品質に大きく影響する
本人確認された
```

### 昇格しないもの

```text
一時的な気分
単発の雑談
誤認の可能性が高い推測
センシティブ情報
会話の流れで出ただけの余談
```

---

## 7. 完全保存ログとの分離

RenCrowには、記憶とは別に「完全保存」を置く。

```text
記憶 = 今後の応答に使うために整理・圧縮・選別されたもの
記録 = 会話・作業・報告を後から辿るために保存されたもの
```

完全保存に入れるもの。

```text
会話ログ
作業ログ
実行結果
生成物
検索結果
報告書
Event Log
staging raw data
```

記憶に入れるもの。

```text
要約
判断
好み
制約
プロジェクト状態
重要なエピソード
再利用価値のある知識
```

Thinkの中身は完全保存しない。  
ただし、会話・報告・作業の成果は、記録として残す。

---

## 8. 毎ターンの想起フロー

RenCrowでは、毎ターン必ず想起を行う。

ただし、毎ターン外部検索するわけではない。  
まずローカル記憶とローカルDBを参照し、必要なときだけ外部検索する。

```text
User Message
  ↓
Intent / Domain / Entity extraction
  ↓
L0 current thread
  ↓
L1 today memory
  ↓
L2 monthly memory
  ↓
User Profile Memory
  ↓
Character Memory
  ↓
L3 long-term user memory
  ↓
Knowledge DB
  ↓
News DB
  ↓
Search Cache
  ↓
External Search only if needed
  ↓
Recall Pack
  ↓
Persona Prompt
  ↓
Local LLM
```

---

## 9. Recall Pack

Recall Packは、毎ターンLLMへ渡す記憶・知識・文脈の束である。

古い会話全文を丸ごと渡すのではなく、整理された要約と根拠を渡す。

### Recall Pack構成

```text
【現在の会話】
- 直近の話題
- ユーザーの最新意図
- 未解決の論点

【ユーザー記憶】
- 出力の好み
- プロジェクト方針
- 制約
- 関係性の前提

【今日の記憶】
- 今日決めたこと
- 今日の作業状態
- 未完了指示

【今月の記憶】
- 継続中のテーマ
- 重要な判断
- 繰り返し出た論点

【長期記憶】
- 関連する過去プロジェクト
- 似た判断
- れんの恒常的な方針

【関連知識】
- Knowledge DB候補
- News DB候補
- Source Registry候補

【出典・根拠】
- source_id
- source_url
- event_id
- retrieved_at
- is_raw_or_summary
```

### Recall Packサイズ目安

| モデル文脈 | Recall Pack目安 |
|---|---|
| 8k | 600〜900 tokens |
| 16k | 1000〜1500 tokens |
| 32k | 1500〜2500 tokens |
| 64k以上 | 3000 tokens以内を基本 |

大事なのは、量ではなく選別である。  
ローカルLLMの強みは、大量の未整理情報を詰め込むことではなく、整理された材料から深く推論することに使う。

---

## 10. 想起ランキング

Recall Orchestratorは、候補記憶をスコアリングして選ぶ。

基本スコア。

```text
score =
  semantic_similarity * 0.45
+ recency             * 0.20
+ user_importance     * 0.20
+ project_relevance   * 0.10
+ persona_affinity    * 0.05
```

ただし、次の記憶は重みを上げる。

```text
pinned memory
現在進行中プロジェクト
明示的な制約
最近の未完了作業
れんが何度も訂正した内容
安全に関わる注意点
```

次の記憶は重みを下げる。

```text
未確認の推測
古くて上書きされた内容
一度しか出ていない雑談
低信頼ソース由来の情報
センシティブで利用に注意が必要な内容
```

---

## 11. キャラクター記憶

RenCrowの各キャラクターは、共通記憶を参照しつつ、自分専用の記憶も持つ。

キャラクター記憶に保存するもの。

```text
口調の調整
れんとの距離感
得意な作業
苦手な作業
よく採用された返答パターン
避けるべき振る舞い
KPI
役割ごとの成功例
```

例。

```json
{
  "memory_id": "charmem_lumina_001",
  "persona": "lumina",
  "type": "relationship",
  "statement": "れんさんには、後輩として丁寧で落ち着いた距離感で話す",
  "evidence_event_ids": ["evt_210", "evt_433"],
  "confidence": 0.98,
  "scope": "lumina_only",
  "ttl_policy": "pinned"
}
```

キャラクター記憶は、人格ファインチューンの代替である。  
話し方や役割は、LoRAやファインチューンではなく、YAML / Markdown / 記憶で管理する。

---

## 12. Persona Prompt Builder

RenCrowでは、人格をモデルに焼き込まない。  
人格は外部設定として管理する。

```text
personas/*.yaml
styleguides/*.md
templates/*.md
character_memory
user_memory
Recall Pack
```

これらを組み合わせて、毎ターンPersona Promptを生成する。

```text
Persona Prompt =
  System Role
+ Persona YAML
+ Styleguide MD
+ Character Memory
+ User Memory
+ Recall Pack
+ Task Instruction
+ Output Template
```

これにより、キャラクターは成長しながらも、編集可能で、差分管理できる。

---

## 13. Knowledge DB

Knowledge DBは、れん自身に関する記憶とは分離する。

対象。

```text
映画
小説
ドラマ
漫画
音楽
演劇
ボドゲ
AI技術
ローカルLLM
論文
ニュース
製品仕様
```

namespace例。

```text
kb:movie
kb:novel
kb:manga
kb:music
kb:stage
kb:boardgame
kb:ai
kb:local_llm
kb:news
```

Knowledge DBは、RAGや雑談の材料として使う。  
ただし、Knowledge DBの内容を、れんの好みとして扱ってはいけない。

---

## 14. ニュース・雑談記憶

RenCrowは、日々のニュースの話や雑談からも想起できるようにする。

ニュースは以下の流れで扱う。

```text
RSS / 公式API / Source Registry
  ↓
staging
  ↓
要約
  ↓
News DB
  ↓
Daily News Digest
  ↓
会話時のRecall候補
```

ニュース雑談では、次の3つを組み合わせる。

```text
今日のニュース
れんの過去の関心
関連する長期記憶
```

例。

```text
半導体ニュース
  ↓
RenCrowのローカルLLM運用
  ↓
RTX4060Ti / MacBook Pro M5 Max の記憶
  ↓
れんの「ローカルファースト」方針
```

雑談は、ニュースそのものを説明するだけでなく、れんの文脈に接続して話す。

---

## 15. 外部検索APIの扱い

検索APIは、RenCrowの主食ではない。

使う場面。

```text
未知語の確認
最新情報の確認
公式ページ探索
出典の裏取り
ローカルDBにない固有名詞の同定
```

避ける場面。

```text
日々のニュース取得
初期DB育成
定期DB更新
毎ターンの雑談補足
大量RAG素材の収集
```

基本方針。

```text
registry-first
local-first
cache-first
search-last
```

---

## 16. Staging / Validator / Promotion

外部取得データも、記憶候補も、いきなり正式DBに入れない。

```text
Observed data
  ↓
Staging
  ↓
Validator
  ↓
Promotion
  ↓
Official Memory / Knowledge DB
```

### staging JSONL最小スキーマ

```json
{
  "event_id": "evt_20260501_000001",
  "source_id": "openai_blog",
  "source_type": "rss",
  "source_url": "https://example.com/article",
  "fetched_at": "2026-05-01T07:00:00+09:00",
  "published_at": "2026-05-01T02:15:00+09:00",
  "title": "記事タイトル",
  "raw_text": "取得した本文または抜粋",
  "raw_hash": "sha256...",
  "summary_draft": "ローカルLLMが作った仮要約",
  "keywords": ["AI", "LLM"],
  "language": "ja",
  "license_note": "unknown",
  "validation_status": "pending"
}
```

### validatorが確認すること

```text
source_idがSource Registryにあるか
URLが正規化されているか
raw_hashが重複していないか
published_atとfetched_atが妥当か
summary_draftがraw_textと矛盾していないか
sensitivity分類が必要か
ユーザー記憶へ昇格してよいか
出典メタデータが足りているか
```

---

## 17. Source Registry

Source Registryは、RenCrowが信頼する外部情報源の台帳である。

保持するもの。

```json
{
  "source_id": "openai_blog",
  "name": "OpenAI Blog",
  "type": "official_blog",
  "url": "https://openai.com/blog",
  "feed_url": "https://openai.com/news/rss.xml",
  "trust_level": "official",
  "fetch_frequency": "daily",
  "allowed_use": ["news", "ai_update", "citation"],
  "requires_validation": true,
  "last_checked_at": null
}
```

外部ソースは、検索で見つけたからといって即採用しない。  
候補としてstagingに置き、validatorまたは人間確認後にSource Registryへ昇格する。

---

## 18. EventId

RenCrowでは、すべての処理にEventIdを付ける。

対象。

```text
ユーザー発話
AI応答
記憶候補生成
記憶昇格
検索
RSS取得
要約
validator結果
DB書き込み
エラー
外部ツール実行
```

EventIdにより、後から以下を追跡できる。

```text
どの会話から記憶が作られたか
どの記憶が応答に使われたか
どの出典が要約に使われたか
どの判断で正式DBへ昇格したか
どの処理で失敗したか
```

---

## 19. 忘却・訂正・上書き

RenCrowは、覚えるだけでなく、忘れる・訂正する・上書きする機能を持つ。

ユーザー操作。

```text
覚えて
忘れて
これは違う
これは古い
これを優先して
これは一時的な話
この方針に更新して
```

内部処理。

| 操作 | 処理 |
|---|---|
| 覚えて | candidateまたはconfirmedへ昇格 |
| 忘れて | active=false / deleted_at付与 |
| これは違う | confidence低下、superseded_by設定 |
| これは古い | deprecatedへ移行 |
| これを優先 | pinnedまたはpriority上昇 |
| 一時的 | ttl_policyをshortに設定 |
| 更新 | 旧記憶をsuperseded_byで連結 |

物理削除は慎重に行う。  
通常は論理削除にし、完全削除は明示操作またはガバナンスポリシーに従う。

---

## 20. センシティブ情報の扱い

RenCrowは、れんに寄り添うための記憶システムだが、何でも保存してよいわけではない。

センシティブな情報は、原則として自動確定しない。

対象例。

```text
健康状態
家庭事情
精神状態
政治的立場
宗教
精密な住所
個人識別情報
他者のプライバシー
```

扱い方。

```text
明示的に保存依頼された場合のみ保存を検討
通常は会話文脈内で扱い、長期記憶化しない
保存する場合はsensitivityを付与
Recall Packへ無条件に入れない
必要最小限に要約
削除・忘却を容易にする
```

---

## 21. データ肥大化対策

RenCrowは、長期運用でDBが爆発しないようにする。

基本方針。

```text
寿命を決める
要約する
重複を消す
低価値記憶を間引く
Hot / Warm / Coldを分ける
全文は完全保存へ、記憶は要約へ
```

### データライフサイクル

```text
L0: 現在会話のみ。ターン上限で要約。
L1: 今日の記憶。日次Digest後に圧縮。
L2: 今月の記憶。月次ハイライト化。
L3: 長期記憶。重要な要約と意味ベクトルのみ。
完全保存: rawログをParquet/Zstd等で保存。
```

### 削除・圧縮基準

```text
30日未参照かつscore低
重複内容
superseded済み
一時的な話題
低信頼ソース由来
既に月次要約へ吸収済み
```

---

## 22. ファインチューン方針

RenCrowの記憶システムに、ファインチューンは不要である。

理由。

```text
知識はRAG / Knowledge DBで扱う
記憶はDB / VectorDBで扱う
人格はYAML / Markdownで扱う
口調はStyleguideで扱う
成長はCharacter Memoryで扱う
```

ファインチューンは、記憶の更新・削除・根拠追跡に向かない。  
れんに寄り添うためには、モデルに焼き込むより、外部化された編集可能な記憶として扱う方がよい。

---

## 23. UI要件

RenCrowのUIは、技術用語を前面に出さない。

ユーザーには、以下のように見せる。

```text
思い出したこと
今日の流れ
今月の流れ
関連する知識
確認した出典
未完了の指示
今の作業机
```

隠してよい技術用語。

```text
VectorDB
Embedding
RAG
DuckDB
SQLite
BM25
PMI
```

開発者向けには、Memory Inspectorを用意する。

Memory Inspectorで見るもの。

```text
Recall Pack内容
どの記憶が使われたか
各記憶のscore
source_id
event_id
昇格状態
sensitivity
superseded_by
最終参照日時
```

---

## 24. 初期MVP実装順

最初から全機能を入れない。  
れんに寄り添うための記憶ループを先に作る。

### Phase 0: 設計固定

```text
config/system.yaml
personas/registry.yaml
memory/schema.sql
source_registry.yaml
staging_schema.json
```

### Phase 1: 1キャラ + L0/L1/L2想起

```text
User
  ↓
L0/L1/L2 recall
  ↓
Persona Prompt
  ↓
Local LLM
  ↓
Response
  ↓
L1 episode append
```

この段階ではL3はno-opでよい。

### Phase 2: ユーザー記憶

```text
覚えて
忘れて
ピン留め
これは違う
要約して保存
```

を実装する。

### Phase 3: Recall Pack可視化

```text
思い出したこと
今日の流れ
関連プロジェクト
使った出典
```

をUIに出す。

### Phase 4: Staging / Validator

```text
memory_candidate
external_fetch
summary_draft
source_candidate
```

を正式DB前に検証する。

### Phase 5: Source Registry + RSS取得

ニュース・技術情報・公式ブログの定期取得を開始する。

### Phase 6: Knowledge DB最小版

まず以下から始める。

```text
RenCrow仕様
AI技術メモ
ローカルLLM運用
映画DB
ニュースDB
```

---

## 25. 非機能要件

RenCrow記憶システムは、以下を満たす。

| 要件 | 内容 |
|---|---|
| ローカルファースト | 可能な限りローカル推論・ローカルDBで処理 |
| 追跡可能性 | すべての記憶にevent_id / evidenceを持たせる |
| 編集可能性 | YAML / Markdown / DBで外から直せる |
| 忘却可能性 | ユーザーが忘れさせられる |
| 昇格制御 | 推測を即確定しない |
| 分離性 | user / char / conv / kbを混ぜない |
| 圧縮性 | rawログと記憶要約を分ける |
| 安全性 | センシティブ記憶を慎重に扱う |
| 継続性 | 今日、今月、長期の文脈を自然に接続 |
| 可観測性 | Memory Inspectorで確認できる |

---

## 26. まとめ

RenCrowの記憶システムは、れんを学習し続けるための基盤である。

単に情報を覚えるのではない。  
れんの考え方、判断、好み、作業の流れ、プロジェクト、対話の距離感を少しずつ理解し、毎回の応答に反映する。

最終形はこうである。

```text
れんが話す
  ↓
RenCrowが現在の文脈を理解する
  ↓
今日の流れを思い出す
  ↓
今月の作業を思い出す
  ↓
長期の方針を思い出す
  ↓
必要なら外部知識を確認する
  ↓
人格ごとの距離感で応答する
  ↓
新しい気づきを記憶候補にする
  ↓
重要なものだけ正式記憶へ昇格する
```

この循環により、RenCrowは単なるチャットではなく、れんのために育っていくエージェントシステムになる。
