# RenCrow 記憶システム実装仕様書

最終更新: 2026-05-23  
対象: RenCrow / Chat / Worker / Coder / Persona Agents  
正本仕様: `docs/memory/RenCrow_memory_system_user_adaptive_spec.md`  
実装方針: Safe Build Mode

---

## 1. 概要

本仕様書は、RenCrow の記憶システムを「れんさんを学習し続ける記憶OS」として実装するための詳細実装仕様である。

正本仕様では、記憶システムを単なる RAG、ログ保存、検索キャッシュ、Knowledge DB ではなく、れんさんの考え方・好み・作業方針・継続中プロジェクト・関係性を毎ターン自然に反映する基盤として定義している。

v0.1 の目的は、全自動の長期記憶システムではなく、以下を安全に動かすことである。

- L0/L1/L2 の想起
- `user:ren` の最小 User Memory
- Recall Pack 生成
- Memory Promotion の状態管理
- Viewer での最小可視化
- Knowledge DB / Source Registry との分離確認

v0.1 は既存DBや既存会話経路を壊さないことを最優先とする。

---

## 2. v0.1 のスコープ

v0.1 で実装するもの。

| 機能 | 内容 |
|---|---|
| L0 Recall | 現在会話・直近発話・未解決論点を取得する |
| L1 Recall | SQLite WAL の今日の会話・イベント・作業状態を取得する |
| L2 Recall | DuckDB の要約・履歴・月次相当情報を取得する |
| User Memory CRUD | `user:ren` の記憶を作成・更新・論理削除・取得する |
| Memory State | `observed / candidate / confirmed / pinned` を扱う |
| Memory Commands | 「覚えて」「忘れて」「これは違う」「これを優先して」を扱う |
| Recall Pack | 毎ターン LLM に渡す記憶束を生成する |
| Viewer 表示 | 「思い出したこと」「今日の流れ」などで表示する |
| Memory Inspector | 開発者向けに score / event_id / namespace / state を表示する |
| 接続確認 | L1 SQLite / L2 DuckDB / L3 Vector sidecar の接続を確認する |
| Tests | 代表ケースのユニットテストと最小E2Eを追加する |

---

## 3. v0.1 で作らないもの

v0.1 では以下を作らない。

- 全自動の長期記憶昇格
- センシティブ情報の自動保存
- 外部検索結果の自動大量投入
- ファインチューン
- 複雑な月次ダイジェスト
- 完全な News DB
- 全キャラクターの高度な成長記憶
- UI の大規模再設計
- 物理削除
- 自律的な Source Registry 追加
- LangGraph への全面移行
- 複雑な記憶スコア学習

---

## 4. 全体アーキテクチャ

処理フロー。

```text
User Message
  ↓
Intent / Domain / Entity Extractor
  ↓
L0 Recall
  ↓
L1 Recall
  ↓
L2 Recall
  ↓
User Memory Recall
  ↓
Character Memory Recall
  ↓
Knowledge DB Recall
  ↓
Recall Ranking
  ↓
Recall Pack Builder
  ↓
Persona Prompt Builder
  ↓
Local LLM
  ↓
Response
  ↓
L1 Event Append
  ↓
Memory Candidate Generator
```

v0.1 では L3 は接続確認と confirmed/pinned 取得までを対象とし、自動昇格は行わない。

---

## 5. L0/L1/L2/L3 の責務

| Layer | 役割 | 推奨ストア | v0.1対応 |
|---|---|---|---|
| L0 | 現在会話、直近発話、割り込み状態 | runtime state / Redis | 実装対象 |
| L1 | 今日の記憶、イベントログ、検索キャッシュ | SQLite WAL | 実装対象 |
| L2 | 今月の流れ、要約、作業履歴 | DuckDB | 実装対象 |
| L3 | 長期記憶、pinned memory、Vector sidecar | DuckDB + Qdrant | 接続整理・取得のみ |

L0 は長期保存しない。  
L1 は今日の復帰用。  
L2 は数日から数週間の復帰用。  
L3 はユーザー理解と長期プロジェクト記憶用。

---

## 6. namespace 設計

namespace は必ず用途別に分離する。

| namespace | 用途 | 例 |
|---|---|---|
| `conv:<thread_id>` | 会話スレッド | `conv:1779520855` |
| `user:<uid>` | ユーザー記憶 | `user:ren` |
| `char:<persona>` | キャラ記憶 | `char:mio` |
| `kb:<domain>` | 外部知識 | `kb:local_llm` |

禁止事項。

- `kb:*` をユーザー嗜好として使わない
- `user:ren` に外部記事本文を直接入れない
- `char:mio` にれんさんの恒常的好みを保存しない
- `conv:*` の一時発話を即 `pinned` にしない

---

## 7. データモデル

主要モデル。

```go
type MemoryState string

const (
	MemoryObserved  MemoryState = "observed"
	MemoryCandidate MemoryState = "candidate"
	MemoryConfirmed MemoryState = "confirmed"
	MemoryPinned    MemoryState = "pinned"
)

type MemoryNamespaceKind string

const (
	NamespaceConversation MemoryNamespaceKind = "conv"
	NamespaceUser         MemoryNamespaceKind = "user"
	NamespaceCharacter    MemoryNamespaceKind = "char"
	NamespaceKnowledge    MemoryNamespaceKind = "kb"
)

type UserMemory struct {
	ID               string
	Namespace        string
	UserID           string
	Type             string
	Statement        string
	EvidenceEventIDs []string
	Confidence       float64
	Sensitivity      string
	State            MemoryState
	Scope            string
	Active           bool
	SupersededBy     string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	LastReferencedAt time.Time
}

type RecallPack struct {
	SessionID string
	UserID    string
	Items     []RecallItem
	CreatedAt time.Time
}

type RecallItem struct {
	Layer       string
	Namespace   string
	MemoryID    string
	Kind        string
	Summary     string
	Score       float64
	State       MemoryState
	SourceID    string
	EventIDs    []string
	Sensitivity string
}
```

---

## 8. DBスキーマ

### L1 SQLite

既存の `l1_memory_event` を拡張または互換利用する。

必須項目。

| column | type | 内容 |
|---|---|---|
| id | text | memory/event ID |
| namespace | text | `conv:*` / `user:*` / `char:*` / `kb:*` |
| session_id | text | session |
| thread_id | integer | thread |
| speaker | text | user / assistant / memory |
| message | text | 本文または要約 |
| meta_json | text | type, evidence, sensitivity 等 |
| memory_state | text | observed/candidate/confirmed/pinned |
| layer | text | L0/L1/L2/L3 |
| source | text | conversation/promoter/validator |
| active | boolean | 論理削除 |
| superseded_by | text | 上書き先 |
| created_at | timestamp | 作成時刻 |
| updated_at | timestamp | 更新時刻 |

### User Memory View

`user:ren` の取得用に以下の query helper を作る。

- `ListUserMemories(userID, state, limit)`
- `CreateUserMemory(input)`
- `UpdateUserMemoryState(id, state)`
- `DeactivateUserMemory(id, reason)`
- `SupersedeUserMemory(oldID, newID)`

---

## 9. Recall Pack 仕様

Recall Pack は毎ターン生成する。

入れるもの。

- 現在の話題
- 未解決の論点
- 今日の流れ
- 今月の要約
- `user:ren` の confirmed/pinned 制約
- 関連する project memory
- キャラ固有の必要最小記憶
- `kb:<domain>` の関連知識
- event_id / source_id / retrieved_at

入れないもの。

- raw log 全文
- think
- 未検証の大量候補
- センシティブ情報の無条件挿入
- Knowledge DB をユーザー嗜好化した文
- 古く superseded 済みの記憶

出力構造。

```json
{
  "session_id": "viewer-user",
  "user_id": "ren",
  "items": [
    {
      "layer": "L1",
      "namespace": "user:ren",
      "kind": "preference",
      "summary": "短く論理的な説明を好む",
      "state": "confirmed",
      "score": 0.92,
      "event_ids": ["evt_001"]
    }
  ]
}
```

---

## 10. Recall Ranking 仕様

基本スコア。

```text
score =
  semantic_similarity * 0.45
+ recency             * 0.20
+ user_importance     * 0.20
+ project_relevance   * 0.10
+ persona_affinity    * 0.05
```

v0.1 では簡易実装でよい。

優先するもの。

- pinned
- confirmed
- 現在進行中 project
- 明示された制約
- 最近の未完了作業
- 何度も訂正された内容

下げるもの。

- observed のみ
- candidate だが根拠が弱い
- 古く superseded 済み
- sensitive
- 低信頼ソース由来

---

## 11. Memory Promotion 仕様

状態遷移。

```text
observed
  ↓
candidate
  ↓
confirmed
  ↓
pinned
```

操作別処理。

| ユーザー発話 | 処理 |
|---|---|
| 覚えて | candidate または confirmed 作成 |
| 忘れて | active=false |
| これは違う | confidence低下、必要なら superseded |
| これは古い | deprecated扱い、active=false または superseded |
| これを優先して | pinned または priority上昇 |
| これは一時的 | ttl_policy=short |

candidate から confirmed に上げる条件。

- れんさんが明示した
- 複数回観測された
- 作業品質に強く影響する
- evidence_event_ids がある
- sensitive ではない

pinned にする条件。

- 明示的に「常に」「優先して」等がある
- 安全性・重大な制約に関係する
- 応答品質に常時影響する
- 人間確認済み

---

## 12. User Memory 仕様

対象 namespace は `user:ren`。

type。

| type | 内容 |
|---|---|
| profile | 明示されたプロフィール |
| preference | 出力嗜好 |
| project | 長期プロジェクト |
| constraint | 禁止事項・制約 |
| relationship | 関係性 |
| episode | 重要な出来事 |
| skill | 得意領域 |
| sensitive | 慎重扱い情報 |

API。

```text
GET    /viewer/memory/user?user_id=ren
POST   /viewer/memory/user
POST   /viewer/memory/user/state
POST   /viewer/memory/user/forget
POST   /viewer/memory/user/supersede
```

v0.1 では `user_id=ren` 固定でよい。

---

## 13. Character Memory 仕様

namespace は `char:<persona>`。

保存するもの。

- 口調調整
- 得意不得意
- 採用された返答パターン
- 避けるべき振る舞い
- KPI
- れんさんとの距離感

v0.1 では読み取りと Memory Inspector 表示まで。  
自動更新は v0.2 以降。

---

## 14. Knowledge DB 仕様

namespace は `kb:<domain>`。

対象。

- AI技術
- ローカルLLM
- RenCrow仕様
- 映画
- ニュース
- 製品仕様

禁止事項。

- `kb:*` の内容を `user:ren` の好みとして扱わない
- 外部記事を user memory に直接昇格しない
- 出典なし knowledge を confirmed 扱いしない

v0.1 では既存 Knowledge DB / Qdrant / Source Registry との分離確認を行う。

---

## 15. Source Registry / Staging / Validator 仕様

外部取得データは必ず以下を通す。

```text
Source Registry
  ↓
Staging
  ↓
Validator
  ↓
Promotion
  ↓
Knowledge DB / Memory
```

v0.1 で確認すること。

- source_id が存在する
- staging item が作成される
- validation_status が付く
- promote 先が `kb:*` と `user:*` で分離される
- Source Registry から user memory へ自動昇格しない

Validator の最低確認項目。

- namespace 妥当性
- raw_hash 重複
- source_id 存在
- sensitivity
- summary_draft と raw_text の矛盾
- target namespace の妥当性

---

## 16. API 仕様

### Recall Pack

```text
GET /viewer/memory/recall-pack?session_id=viewer-user&user_id=ren
```

返却。

```json
{
  "session_id": "viewer-user",
  "user_id": "ren",
  "items": []
}
```

### User Memory 作成

```text
POST /viewer/memory/user
```

```json
{
  "user_id": "ren",
  "type": "preference",
  "statement": "短く論理的な説明を好む",
  "state": "candidate",
  "evidence_event_ids": ["evt_001"]
}
```

### 状態変更

```text
POST /viewer/memory/user/state
```

```json
{
  "id": "usrmem_001",
  "state": "confirmed",
  "reason": "user_explicit"
}
```

### 忘却

```text
POST /viewer/memory/user/forget
```

```json
{
  "id": "usrmem_001",
  "reason": "user_requested"
}
```

---

## 17. Viewer / Memory Inspector UI 仕様

ユーザー向け表示名。

| 内部名 | 表示名 |
|---|---|
| L0 | 今の話 |
| L1 | 今日の流れ |
| L2 | 最近の流れ |
| L3 | 長く覚えていること |
| Recall Pack | 思い出したこと |
| Knowledge DB | 関連する知識 |

Memory Inspector に表示するもの。

- memory_id
- namespace
- layer
- type
- state
- score
- evidence_event_ids
- source_id
- sensitivity
- active
- superseded_by
- last_referenced_at
- created_at
- updated_at

UI は既存 Viewer に小さく追加する。  
大規模再設計はしない。

---

## 18. 設定ファイル仕様

`config.yaml` の `conversation` に追加・整理する。

```yaml
conversation:
  enabled: true
  redis_url: redis://localhost:6379
  l1_sqlite_path: /home/nyukimi/.rencrow/l1_memory.db
  duckdb_path: /home/nyukimi/.rencrow/memory.duckdb
  vectordb_url: localhost:6334
  vector_collection: rencrow_memory_3584
  vector_dimension: 3584
  embed_provider: ollama
  embed_base_url: http://100.83.207.6:11434
  embed_model: nomic-embed-code:latest
  summary_model: Chat
  default_user_id: ren
  recall_pack_enabled: true
  memory_inspector_enabled: true
```

---

## 19. 実装ディレクトリ構成

推奨構成。

```text
internal/domain/memory/
  models.go
  namespace.go
  recall_pack.go
  promotion.go

internal/application/memory/
  recall_orchestrator.go
  recall_ranker.go
  recall_pack_builder.go
  user_memory_service.go
  memory_command_service.go
  candidate_generator.go

internal/infrastructure/persistence/memory/
  l1_user_memory_store.go
  l2_summary_reader.go
  l3_vector_reader.go

internal/adapter/viewer/
  memory_user_handler.go
  memory_recall_pack_handler.go
  memory_inspector_handler.go

internal/adapter/viewer/assets/js/tabs/
  memory.js

tests または Go test:
  memory_*_test.go
```

既存の `internal/infrastructure/persistence/conversation` と重なる場合は、急に移動せず adapter/service を薄く足す。

---

## 20. モジュール責務

| モジュール | 責務 |
|---|---|
| `domain/memory` | 型、状態、namespace、validation |
| `recall_orchestrator` | L0/L1/L2/User/Char/KB 取得の統合 |
| `recall_ranker` | Recall 候補の並び替え |
| `recall_pack_builder` | LLM投入用 Recall Pack 作成 |
| `user_memory_service` | `user:ren` CRUD |
| `memory_command_service` | 覚えて/忘れて/これは違う/優先 |
| `candidate_generator` | raw/event から候補を作る |
| `memory_inspector_handler` | 開発者向け可視化 |
| `memory_user_handler` | Viewer API |
| `memory.js` | Viewer 表示 |

---

## 21. 実装順

### Phase 0: 仕様固定

- 本仕様書を作成
- 正本仕様との差分を確認
- v0.1 の対象外を明記

完了条件:
- 実装対象と非対象が明確

### Phase 1: domain model

- `MemoryState`
- `NamespaceKind`
- `UserMemory`
- `RecallPack`
- `RecallItem`

完了条件:
- namespace validation test が通る

### Phase 2: User Memory CRUD

- `user:ren` 作成
- 一覧
- 状態更新
- 論理削除
- supersede

完了条件:
- CRUD unit test が通る

### Phase 3: Recall Pack Builder

- L0/L1/L2/User Memory を束ねる
- raw log を入れない
- sensitive を無条件に入れない

完了条件:
- Recall Pack unit test が通る

### Phase 4: Memory Commands

- 覚えて
- 忘れて
- これは違う
- これを優先して

完了条件:
- 発話から期待操作に変換できる

### Phase 5: Viewer API

- `/viewer/memory/user`
- `/viewer/memory/recall-pack`
- `/viewer/memory/inspector`

完了条件:
- curl で取得・更新できる

### Phase 6: Viewer UI

- 思い出したこと
- 今日の流れ
- 長く覚えていること
- 開発者向け Inspector

完了条件:
- 既存 Viewer を壊さず表示できる

### Phase 7: E2E

- 1ターン会話
- L1保存
- Recall Pack表示
- 「覚えて」操作
- `user:ren` candidate 作成
- confirmed/pinned は明示操作のみ

完了条件:
- E2E が再現可能

---

## 22. テスト仕様

必須テスト。

| テスト | 期待 |
|---|---|
| namespace validation | `user:ren`, `conv:123`, `char:mio`, `kb:ai` が有効 |
| namespace混入防止 | `kb:*` を user memory として保存しない |
| User Memory create | candidate が作成される |
| confirmed昇格 | evidence なしでは拒否 |
| pinned昇格 | 明示理由なしでは拒否 |
| forget | active=false になる |
| supersede | old.superseded_by が入る |
| sensitive | 自動 confirmed にならない |
| Recall Pack | raw log 全文を含まない |
| Knowledge DB | user preference として扱わない |
| API | JSON schema に合う |
| Viewer | 思い出したことが表示される |

---

## 23. E2E確認手順

1. RenCrow を起動する
2. `/health` が 200 であることを確認
3. Viewer から通常発話を1回送る
4. L1 に `conv:<thread_id>` の observed が入ることを確認
5. `/compact` を実行する
6. L2 に summary が入ることを確認
7. 「覚えて: 私は短く論理的な説明を好む」を送る
8. `user:ren` に candidate または confirmed が作られることを確認
9. Memory Inspector で evidence_event_ids を確認
10. Recall Pack に該当 memory が入ることを確認
11. `kb:<domain>` の Knowledge が user memory に混ざらないことを確認

---

## 24. 完了条件

v0.1 完了条件。

- L0/L1/L2 の Recall が動く
- `user:ren` の User Memory CRUD が動く
- `observed / candidate / confirmed / pinned` が保存できる
- 「覚えて」「忘れて」「これは違う」「これを優先して」が最小動作する
- Recall Pack が毎ターン生成される
- Recall Pack が Viewer で見える
- Memory Inspector で state / namespace / evidence が見える
- Knowledge DB と User Memory が混ざらない
- raw log と memory が混ざらない
- sensitive memory が自動 confirmed にならない
- 代表ユニットテストが通る
- 1ターン以上の E2E が通る

---

## 25. v0.2以降の拡張

- 月次ダイジェスト
- Character Memory 自動更新
- User Memory の重複統合
- 類似 memory の自動 supersede 提案
- Source Registry 自動候補生成
- News DB 本格運用
- Memory score の学習
- Recall Pack の token 最適化
- Memory Inspector の差分表示
- sensitive memory の確認UI
- 完全保存ログの Parquet/Zstd 化
- LangGraph checkpoint との統合

---

## 26. リスクと対策

| リスク | 対策 |
|---|---|
| namespace混入 | validation を domain layer に置く |
| 推測の確定 | confirmed/pinned に evidence と reason を必須化 |
| Knowledgeを嗜好扱い | `kb:* → user:*` 自動昇格禁止 |
| raw log肥大化 | Recall Pack には要約だけ入れる |
| sensitive保存 | 自動昇格禁止、sensitivity 必須 |
| 古い記憶の誤利用 | superseded_by / active / last_confirmed_at を使う |
| UIが技術的すぎる | ユーザー向け語彙へ変換 |
| 既存実装破壊 | Safe Build Mode、最小差分、既存API互換 |
| Vector次元不整合 | collection名とdimensionをconfig管理 |
| 外部検索依存 | local-first / cache-first / search-last を徹底 |

---

## 27. 最終まとめ

v0.1 は、RenCrow を完全な自律記憶AIにする段階ではない。

最初に作るべきものは、れんさんの記憶を安全に扱うための土台である。

- namespace を分ける
- raw log と memory を分ける
- Recall Pack を毎ターン作る
- 推測を即確定しない
- user memory と Knowledge DB を混ぜない
- 忘却・訂正・上書きを可能にする
- Viewer で「何を思い出したか」を見えるようにする

この順番で実装することで、RenCrow は単なるチャットではなく、れんさんの作業・判断・好み・継続プロジェクトを少しずつ理解し、必要なときに自然に思い出すエージェントシステムへ成長できる。
