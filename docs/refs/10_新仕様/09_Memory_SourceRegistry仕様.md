# Memory / Source Registry 仕様

> 正本参照: Memory lifecycle / Recall Context / Prompt Injection Policy は `../01_正本仕様/18_Memory_Lifecycle_Recall_Context.md` を正本実装仕様として一次参照とする。本書は保存媒体、Source Registry、L1 SQLite などの境界仕様として併用する。

## 目的

Memory / Source Registry は、会話記憶、外部ソース、知識、検証済み情報を扱う永続化境界である。

記憶は無審査で正式化しない。observed、candidate、validated、promoted の状態を分ける。

## 構成

| 領域 | 役割 | 主担当 |
| --- | --- | --- |
| conversation memory | 会話履歴、summary、RecallPack | `internal/domain/conversation`, `internal/infrastructure/persistence/conversation` |
| L1SQLite | event、staging、source registry、news、knowledge、search cache | `internal/infrastructure/persistence/conversation/l1_sqlite_*.go` |
| Domain Graph DB | Movie / 漫画 / 音楽 / 小説 / ゲームなどの外部世界の関係事実 | domain-specific DB / handler / importer |
| VectorDB | thread memory / KB vector search | `internal/infrastructure/persistence/conversation/vectordb_*.go` |
| DuckDB | archive、thread summary、parquet export | `internal/infrastructure/persistence/conversation/duckdb_*.go` |
| RealConversationManager | recall、thread、archive、KB の統合 facade | `internal/infrastructure/persistence/conversation/real_manager_*.go` |
| Source Registry | 外部ソース登録、sweep、stage、validate、promote | `internal/application/sourcefetcher`, `internal/adapter/viewer/source_registry_handler.go` |
| Web Gather | 外部 API key に依存しない Web 検索候補取得、fetch、本文抽出、staging | `46_Web情報収集ツール仕様.md`, `modules/webgather`, `internal/application/webgather` |
| OperationMemory | runtime-readable な運用記憶、日次ノート | `operation_memory_dir`（既定: `~/.rencrow/memory`）, `internal/infrastructure/persistence/memory` |
| session repository | session state と distributed session の永続化 | `internal/domain/session`, `internal/infrastructure/persistence/session`, `cmd/rencrow/runtime_sessions.go` |
| Glossary / RSS | RSS/Atom 由来の topic / glossary 文脈 | `internal/glossary`, `cmd/glossary` |
| Knowledge CLI / core importer | KB 初期投入、語彙更新、運用 CLI | `cmd/kb-admin`, `cmd/vocabulary`, `cmd/rencrow/cli_knowledge.go`, `internal/application/knowledge` |

## 検索システムDB境界

検索・外部情報収集・記憶昇格に必要なDBは、検索候補を扱う hot store、外部世界の関係事実を扱う Domain Graph DB、意味検索用 VectorDB、任意の archive 境界に分ける。

通常の検索システムとしては L1 SQLite と Qdrant を基本必須境界とする。Movie、漫画、音楽、小説、ゲーム、人物、組織、キャラクター、シリーズ、受賞歴など、外部世界の関連性そのものを扱うドメインでは Domain Graph DB を追加の正本DBとして必須にする。

| DB | 必須度 | 役割 |
| --- | --- | --- |
| L1 SQLite | 必須 | search cache、fetch cache、Source Registry、staging、validation 状態、promotion 履歴、event log、軽い knowledge の正本 |
| Domain Graph DB | ドメイン採用時必須 | Movie / 漫画 / 音楽 / 小説 / ゲームなどの作品、人物、組織、キャラクター、シリーズ、関係 edge、出典 assertion の正本 |
| Qdrant | 必須寄り | validated / promoted 済み KB のベクトル検索 |
| DuckDB | 任意 | 長期 archive、集計、履歴分析、Parquet export |

L1 SQLite は検索システムの hot store / 正本DBである。検索候補、外部取得結果、RSS/Atom/sitemap 由来情報、Google Custom Search 結果、Web Gather 結果は、まず L1 SQLite の cache または staging に保存する。無審査で UserMemory、正式 Knowledge、Qdrant へ直書きしてはいけない。

Domain Graph DB は、外部世界に存在する作品・人物・組織・キャラクター・シリーズ・ジャンル・関連作品などの関係を保持する正本DBである。これは search cache、staging、汎用 KB、Qdrant、DuckDB の代替ではない。外部取得結果は、L1 SQLite の staging で validation した後、関係事実として採用するものだけを Domain Graph DB へ promote する。

Domain Graph DB では、外部カタログ事実とユーザー固有状態を混ぜない。「見た」「読んだ」「好き」「苦手」「話題にしたい」は user event / preference signal として別テーブルへ保存する。

Domain Graph DB の assertion には、source URL、source ID、取得/観測時刻、confidence、validation status、evidence を残す。矛盾する事実は無言で overwrite せず、assertion と validation 状態で扱う。

Qdrant へ入れるのは、validated / promoted 済みで、意味検索する価値がある KB のみとする。Google検索結果、RSS取得結果、Webページ本文、Source Registry staging を Qdrant へ直接 upsert してはいけない。

Domain Graph DB から Qdrant へ同期する場合は、作品・人物・関係の要約や説明など、意味検索に使う文書表現だけを同期する。関係 edge の正本は Domain Graph DB に残す。

DuckDB は運用必須ではない。古い staging、古い event、古い search cache、日次 digest、分析用 archive を扱う cold store として後段で使う。

SearXNG、YaCy、Google Custom Search、RSS/Atom、sitemap は DB ではなく discovery provider / source である。Source Registry は独立DBではなく、L1 SQLite 内の管理テーブルである。

## 状態遷移

```text
observed
  -> candidate / staging
  -> validated or rejected
  -> promoted to memory / news / knowledge
```

禁止:

- Source Registry を無審査で正式 memory へ昇格する。
- observed / candidate / validated / promoted を同じ状態として扱う。
- Viewer 表示 state と永続化 state を混同する。

## RecallPack

RecallPack はプロンプト注入用の文脈である。

- role に応じて KB / search cache / thread summary を選別する。
- token budget を守る。
- rejected trace を残せるようにする。
- prompt text だけを真実の保存先にしない。
- L0/L1/L2/L3 の layer、score、採用理由、prompt 位置、採用/不採用 decision を trace できるようにする。

Recall budget は context の一部に収める。現行実装は `ApplyRecallBudget()` と `RecallBudgetRatio` を持ち、精密 tokenizer に差し替えられる `TokenEstimator` 入口を持つ。

role-filtered retrieval は Chat / Worker / Wild で retrieval 候補を変える。Chat は会話記憶中心、Worker は KB/search 込み、Wild は記憶と KB を中心に扱う。

Agent KPI / Level は AgentStatus として runtime state 側に保持する。現行実装は `internal/domain/conversation/agent_status.go` と `internal/infrastructure/persistence/conversation/real_manager_agent_status.go` で KPI 加算、Level 更新、RealConversationManager での保持を扱う。Viewer 表示や運用 UI は未接続のため、実装済み core と未接続 UI を分けて追跡する。

## Memory 操作語彙とメタデータ拡張方針

外部 memory service や外部 semantic DB は RenCrow の正本 memory store へ直接導入しない。ただし、agent memory 系ツールの設計から、次の操作語彙と metadata 方針は RenCrow 内部仕様として採用できる。

### 操作語彙

RenCrow の memory 周辺 API / CLI / Viewer 表示では、次の3系統を基本語彙にする。

| 操作 | RenCrowでの意味 | 注意 |
| --- | --- | --- |
| `remember` | memory candidate / operation memory / staging へ保存する | `confirmed` への直接昇格ではない |
| `recall` | RecallPack / Memory tab / API で関連候補を取り出す | 採用/不採用 decision と理由を trace する |
| `answer` | recall した記憶を根拠として回答案を作る | prompt text だけを保存先にしない |

`remember` は保存入口であり、正式化入口ではない。`observed` / `candidate` / `validated` / `promoted` の状態遷移を飛ばしてはいけない。

### 通常会話からの memory candidate

通常会話中にユーザーが自己申告した好みや継続指示は、条件に合う場合のみ `chat_auto_candidate` として `user:<uid>` の candidate に保存できる。

- candidate は `confirmed` / `pinned` へ自動昇格しない。
- candidate は Mio の system prompt へ注入しない。
- 既存 active memory と同じ statement は重複保存しない。
- 疑問文、記憶確認質問、検索・調査要求、長すぎる文は候補化しない。
- Viewer Memory タブまたは API で review してから `confirmed` / `pinned` へ昇格する。

例:

- `俺は映画が好き` -> `preference` candidate: `映画が好き`
- `今後は短く答えて` -> `constraint` candidate: `短く答えて`
- `俺が映画が好きってこと知ってる？` -> candidate ではなく recall 質問として扱う

### 外部 Recall / KB 混入の抑制

Chat の通常会話では、会話記憶と UserMemory を優先する。Knowledge DB / SearchCache は、外部情報要求が明確な場合だけ使う。

Google Custom Search API を使う Web検索は、quota を消費するためさらに強く制限する。`最新`、`今日`、`ニュース`、`Xについて教えて` だけでは Google API を叩かない。ユーザーが明示的に検索・調査を求めた場合だけ `web_search` を使う。

外部情報要求の例:

- `検索して`
- `調べて`
- `調査して`
- `Webで調べて`
- `インターネットで検索して`

ユーザー自身の記憶確認や好みの自己申告は、外部検索や KB 検索へ流さない。外部検索結果を user memory として扱ってはいけない。

### Memory Type

memory item / staging item / operation memory note は、可能な範囲で次の type を持てるようにする。

| type | 用途 |
| --- | --- |
| `instruction` | ユーザーの継続指示、運用方針 |
| `fact` | 確認済みの事実 |
| `decision` | 決定事項、採用/不採用判断 |
| `goal` | 長期・短期目標 |
| `commitment` | 約束、期限、やること |
| `preference` | 好み、表示・応答・作業スタイル |
| `relationship` | 人・キャラクター・組織の関係 |
| `context` | 背景文脈 |
| `event` | 発生した出来事 |
| `learning` | 失敗から得た教訓、再発防止 |
| `observation` | 観測結果、未検証の気づき |
| `artifact` | ファイル、commit、URL、report などの成果物 |
| `error` | 障害、失敗、未解決問題 |

type は検索・表示・昇格判断の補助 metadata であり、type が付いただけで信頼済みになるわけではない。

### Provenance / Confidence

memory item は、正式化前後を問わず、可能な限り provenance と confidence を保持する。

最低限持つべき観点:

- `source_kind`: user / agent / tool / source_registry / viewer / log / imported_file
- `source_id`: message_id、job_id、source_id、file path、URL など
- `observed_at`: 観測時刻
- `created_by`: 保存した主体
- `confidence`: 0.0 から 1.0 の確からしさ
- `evidence`: 根拠の短い説明または参照
- `inferred`: ユーザー明示ではなく推測なら true

ユーザー明示の指示と agent の推測を混同してはいけない。`confirmed` へ昇格する場合でも、根拠と保存経路を失わない。

### Conflict Detection

新しい candidate が既存の confirmed / promoted memory と矛盾する場合、黙って上書きしない。

扱い:

- 新旧両方を保持する。
- 新しい item は `candidate` または `conflicted` 相当の review 状態に置く。
- Viewer Memory タブまたは運用 report で確認待ちとして表示する。
- 明示承認または validator 通過後に、置換・無効化・併存を決める。

例:

- `preference`: 「短く答えて」 と 「詳しく説明して」が両立しない。
- `instruction`: 古い運用手順と新しい禁止事項が矛盾する。
- `fact`: endpoint、port、host、model 名が変わった。

### Temporal Query

memory 周辺の調査・Viewer表示では、将来次の時間軸 query を扱えるようにする。

| query | 用途 |
| --- | --- |
| `as_of` | ある時点で何を覚えていたか確認する |
| `changed_since` | いつから記憶・設定・判断が変わったか確認する |

これは「さっきまでできていた」「前に決めた」「仕様にあったはず」という回帰調査に使う。現時点の表示だけで過去状態を断定してはいけない。

### Daily Memory Summary

日次 report / Daily Desk / OperationMemory では、次の分類で memory summary を作れるようにする。

- 今日の `decision`
- 未完了の `commitment`
- 新しい `preference`
- 重要な `error`
- 再発防止の `learning`
- 昇格待ちの `candidate`
- 矛盾確認待ちの item

summary は閲覧・共有・再利用のための投影であり、DB/runtime 側の正本 state を置き換えない。

## OperationMemory

OperationMemory は repo の `workspace/` ではなく、DB や runtime state と同じ永続領域に置く。
既定の保存先は `~/.rencrow/memory/` で、設定 `operation_memory_dir` で上書きできる。

- 長期記憶は `MEMORY.md` に保存する。
- 日次ノートは `YYYYMM/YYYYMMDD.md` に保存する。
- キャラクター設定やスキル定義を置く `workspace/` と混同しない。

## session repository

session repository は session state を保存する境界であり、RecallPack や OperationMemory と同じ memory 周辺にあるが責務は異なる。

- `internal/domain/session` は session / distributed session の domain contract を持つ。
- `internal/infrastructure/persistence/session` は JSON repository などの永続化を担当する。
- `cmd/rencrow/runtime_sessions.go` は session repository と OperationMemory の runtime wiring を担当する。
- session_id は発話、応答、chunk、job_id と混同しない。

## KB / Source

Web search result や外部ソースは、Source Registry / KB として保存される。

保存、stage、validate、promote は別フェーズである。正式な memory / knowledge として扱うには検証済み状態が必要である。

RenCrow の常用 Web 情報収集は `46_Web情報収集ツール仕様.md` を正とする。検索候補取得、URL fetch、本文抽出、staging を分離し、外部 API key を前提にしない。取得結果は pending staging として扱い、validate / promote を通さず正式 memory / knowledge にしてはいけない。

`cmd/kb-admin`、`cmd/vocabulary`、`cmd/rencrow/cli_knowledge.go` は Knowledge DB の初期投入・確認・運用補助である。CLI から投入する場合も、未検証外部データを直接 confirmed memory として扱わない。

## 外部入力 risk metadata

外部ソース、添付、channel message は、本文や memory と混ぜずに risk metadata を持つ。

現行実装では `internal/domain/security.DetectPromptInjectionWarnings` が代表的な prompt injection pattern を検出し、attachment 抽出文の `SecurityWarnings` に保存する。Source Registry fetch 由来テキストにも同じ検出器を適用し、`L1SourceFetchPayload.Meta` / `L1StagingItem.Meta` の `security_warnings` と `security_warning_source: source_registry` に保存する。これは拒否判定そのものではなく、外部入力を扱う downstream が警告として参照するための metadata である。Viewer は Source Registry run 結果と staging review table で warning 件数を表示し、本文 / memory / prompt と混同しない。

検出対象の例:

- previous instruction の無視要求。
- system prompt の開示要求。
- tool / shell / command 実行の誘導。

warning 付き外部入力を、検証済み memory や prompt 方針として昇格してはいけない。

Viewer / API は Source Registry staging を次の順に扱う。

1. `GET /viewer/source-registry?action=staging&status=pending` で candidate を確認する。
2. `POST /viewer/source-registry?action=validate` で `ValidateStagingItem` を実行する。
3. `POST /viewer/source-registry?action=promote` で validated staging のみを news / knowledge / memory へ昇格する。

pending のまま promote した場合は失敗であり、fallback 成功扱いにしない。

## L1 SQLite

L1SQLite は hot store として次を扱う。

- memory event: `observed` / `candidate` / `confirmed` の状態を持つ会話・記憶 event。
- event log: message saved、search cache、state update、promotion、recall trace などの追跡 event。
- search cache: query 正規化、hash、TTL、source URL、fresh hit、類似 query hit、manual invalidate。
- staging: external fetch、memory candidate、search result を raw_text / summary_draft / raw_hash / validation_status つきで保持する。
- source registry: source_id、URL、kind、trust score、fetch interval、license note、enabled を持つ。
- news: validated staging 由来の news item、category 別 recent、source metadata。
- daily digest: day / morning / noon / evening slot の digest。
- knowledge: `kb:<domain>` の汎用 knowledge item、lexical 検索入口、Qdrant KB 同期。

## Archive

DuckDB は thread summary と L1 memory / news / knowledge / staging archive を扱う。
Parquet export は cold archive として使い、保存時または promotion 時に archive table へ同期する。

## Viewer との境界

Viewer memory / source registry panel は永続化状態の投影である。

Viewer で見えることは重要な観測だが、表示状態を直接正式 memory state と混同しない。

## 実装箇所

| 仕様 | 主担当 |
| --- | --- |
| memory domain | `internal/domain/memory`, `internal/domain/conversation` |
| L1SQLite schema / state | `internal/infrastructure/persistence/conversation/l1_sqlite_*.go` |
| staging validation | `internal/infrastructure/persistence/conversation/l1_sqlite_staging_validation.go` |
| promotion | `internal/infrastructure/persistence/conversation/l1_sqlite_promotions.go` |
| VectorDB thread / KB | `internal/infrastructure/persistence/conversation/vectordb_thread_memory.go`, `vectordb_kb.go` |
| DuckDB archive / export | `internal/infrastructure/persistence/conversation/duckdb_*.go` |
| source sweep | `internal/application/sourcefetcher/registry_sweeper.go` |
| search cache | `internal/infrastructure/persistence/conversation/l1_sqlite_search_cache.go` |
| event log | `internal/infrastructure/persistence/conversation/l1_sqlite_events.go` |
| news / digest | `internal/infrastructure/persistence/conversation/l1_sqlite_news_digest.go` |
| knowledge DB | `internal/infrastructure/persistence/conversation/l1_sqlite_knowledge.go` |
| knowledge CLI / importer | `cmd/kb-admin`, `cmd/vocabulary`, `cmd/rencrow/cli_knowledge.go`, `internal/application/knowledge` |
| session repository | `internal/domain/session`, `internal/infrastructure/persistence/session`, `cmd/rencrow/runtime_sessions.go` |
| archive job | `internal/application/archive`, `internal/infrastructure/persistence/conversation/duckdb_export.go` |
| Viewer source API | `internal/adapter/viewer/source_registry_handler.go` |
| Viewer memory API | `internal/adapter/viewer/memory_*_handler.go` |

## 検証

```bash
GOCACHE=/tmp/rencrow-gocache go test ./internal/domain/conversation ./internal/domain/memory ./internal/infrastructure/persistence/conversation ./internal/application/sourcefetcher ./internal/adapter/viewer
```

確認対象:

- state transition が飛ばされない。
- rejected / validated / promoted が区別される。
- duplicate raw hash が扱える。
- Viewer 表示と永続化 state が整合する。
- Source Registry の無審査 promote が起きない。
