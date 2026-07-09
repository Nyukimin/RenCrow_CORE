# RenCrow v5.0 実装仕様書: 会話LLMシステム統合

**バージョン**: 5.0.0
**作成日**: 2026-03-04
**最終更新**: 2026-03-07
**ステータス**: Phase 1〜4.2 実装完了・KB自動保存有効・本番稼働中
**ステータス補足**: §5 の LangGraph ベース `ConversationOrchestrator` は現行採用されていない。`refactor_questions.md` Q5 では別途設計フェーズ送り、`実装仕様_会話エンジン_v5.1.md` では Go の `ConversationEngine` を実装済み正本として扱う。
**前提バージョン**: v4.0（分散実行）

### 実装状況（2026-03-07）

| Phase | コンポーネント | 状態 | 備考 |
|-------|--------------|------|------|
| Phase 1 | ドメイン層（Thread/Session/ThreadSummary） | ✅ 完了 | `internal/domain/conversation/` |
| Phase 1 | EmbeddingProviderインターフェース | ✅ 完了 | `internal/domain/conversation/embedding.go` |
| Phase 1 | ConversationSummarizerインターフェース | ✅ 完了 | `internal/domain/conversation/summarizer.go` |
| Phase 2 | RedisStore（短期記憶） | ✅ 完了 | `internal/infrastructure/persistence/conversation/` |
| Phase 2 | DuckDBStore（中期記憶） | ✅ 完了 | 同上 |
| Phase 2 | VectorDBStore（長期記憶 Qdrant） | ✅ 完了 | gRPCポート: 6334 |
| Phase 2 | RealConversationManager | ✅ 完了 | 3層記憶統合 |
| Phase 3 | OllamaEmbedder | ✅ 完了 | `internal/infrastructure/llm/ollama/embedder.go` |
| Phase 3 | LLMSummarizer | ✅ 完了 | `internal/infrastructure/persistence/conversation/llm_summarizer.go` |
| Phase 3 | main.go DI（Embedder/Summarizer注入） | ✅ 完了 | `cmd/rencrow/main.go` |
| インフラ | docker-compose.infra.yml（Qdrant port 6334） | ✅ 完了 | gRPCポート公開 |
| インフラ | systemdサービス（rencrow.service） | ✅ 完了 | `~/.config/systemd/user/rencrow.service` |
| インフラ | systemdサービス（rencrow-funnel.service） | ✅ 完了 | Tailscale Funnel永続化 |
| テスト | 統合テスト（Redis + DuckDB + Qdrant） | ✅ 9件全通過 | `integration_test.go` |
| **Phase 4.1** | **KB基盤実装** | ✅ **完了** | **VectorDB KB機能** |
| Phase 4.1 | SaveKB / SearchKB（VectorDB） | ✅ 完了 | ドメイン別コレクション |
| Phase 4.1 | SaveWebSearchToKB（RealManager） | ✅ 完了 | Web検索結果→KB保存 |
| Phase 4.1 | SearchKB（RealManager） | ✅ 完了 | KB検索API |
| Phase 4.1 | RAG統合（ConversationEngine） | ✅ 完了 | BeginTurn で自動KB検索 |
| Phase 4.1 | KB管理CLI（kb-admin） | ✅ 完了 | search/stats/list/cleanup |
| Phase 4.1 | KB運用ガイド | ✅ 完了 | `docs/KB運用ガイド.md` |
| Phase 4.2 | Worker RESEARCH自動保存 | ✅ 完了 | Mio executeWebSearch で自動保存 |
| Phase 4.2 | ToolResponse Metadata拡張 | ✅ 完了 | 構造化データ格納 |
| Phase 4.2 | main.go DI統合 | ✅ 完了 | WithConversationManager注入 |
| Phase 4.2 | E2Eテスト追加 | ✅ 完了 | kb_autosave_test.go (3件) |

### インフラ構成

| サービス | ポート | プロトコル | 備考 |
|---------|--------|-----------|------|
| Redis | 6379 | TCP | 短期記憶（TTL: 24h） |
| DuckDB | ファイル | - | `/home/nyukimi/.rencrow/memory.duckdb` |
| Qdrant | 6333 (REST) / **6334 (gRPC)** | HTTP / gRPC | 長期記憶（VectorDB）|
| Ollama | 11434 | HTTP | kawaguchike-llm: 100.83.207.6 |

**注意**: VectorDBStoreはQdrantの**gRPCポート6334**に接続。RESTポート6333ではない。

### config.yaml（会話LLM設定）

```yaml
conversation:
  enabled: true
  redis_url: "redis://localhost:6379"
  duckdb_path: "/home/nyukimi/.rencrow/memory.duckdb"
  vectordb_url: "localhost:6334"        # gRPCポート
  embed_model: "nomic-embed-code:latest"
  summary_model: "Chat"
```

---

## 📋 目次

1. [概要](#1-概要)
2. [アーキテクチャ設計](#2-アーキテクチャ設計)
3. [データモデル](#3-データモデル)
4. [記憶レイヤー](#4-記憶レイヤー)
5. [LangGraph設計](#5-langgraph設計)
6. [コンポーネント仕様](#6-コンポーネント仕様)
7. [API仕様](#7-api仕様)
8. [フロー図](#8-フロー図)
9. [実装ガイド](#9-実装ガイド)
10. [テスト仕様](#10-テスト仕様)
11. [運用仕様](#11-運用仕様)
12. [マイグレーション](#12-マイグレーション)

---

## 1. 概要

### 1.1 目的

RenCrow v5.0 では、**会話LLMシステム**を統合し、以下を実現します:

- **継続的な会話**: ユーザーとChatの会話を記憶し、文脈を保持
- **複数キャラクター**: Chat（Mio）とWorker（Shiro）が会話に参加
- **知識拡充**: Chatは他のAgentから新規知識を学習
- **スケーラビリティ**: 将来的にCoder達も会話に参加可能

### 1.2 主要機能

| 機能 | 説明 |
|------|------|
| **Thread管理** | 会話の「話題のまとまり」を管理（6〜8ターン） |
| **Session管理** | プロセス再起動や割り込み復帰に対応（24h〜7d） |
| **4層記憶** | 短期（RAM）/中期（Redis→DuckDB）/長期（VectorDB）/KB（VectorDB） |
| **新規性判定** | Chatが知らない情報を自動検出し、記憶 |
| **Agent状態管理** | Workerのタスク状態（idle/busy）を管理 |
| **LangGraph統合** | Router Node + Character Nodes で会話を制御 |

### 1.3 キャラクター構成

| キャラクター | Agent | LLM | 会話参加条件 | 記憶対象 |
|-------------|-------|-----|-------------|---------|
| **ミオ（澪）** | Chat | Ollama (Chat) | 常時参加 | ✅ ユーザー⇄Chatは常に記憶 |
| **シロ（白）** | Worker | Ollama (Worker) | タスクが空いている時のみ | ✅ Chatが知らない情報なら記憶 |
| **アカ/アオ/ギン** | Coder | DeepSeek/OpenAI/Claude | 将来的に参加可能 | ✅ Chatが知らない情報なら記憶 |

**設計原則**:
- **ユーザー⇄Chat**: 主会話、常に記憶
- **Chat⇄Worker/Coder**: 副会話、新規情報のみ記憶
- **Worker⇄Coder**: 記憶対象外（実行ログとして保存）

---

## 2. アーキテクチャ設計

### 2.1 全体構成

```
┌──────────────────────────────────────────────────────────────┐
│                         User Input                            │
│                    (LINE/Slack/Discord/etc.)                  │
└───────────────────────────┬──────────────────────────────────┘
                            │
                            ▼
┌──────────────────────────────────────────────────────────────┐
│                      Adapter Layer                            │
│                   (LINE Handler, etc.)                        │
└───────────────────────────┬──────────────────────────────────┘
                            │
                            ▼
┌──────────────────────────────────────────────────────────────┐
│               ConversationOrchestrator                        │
│                    (LangGraph)                                │
│  ┌────────────────────────────────────────────────────────┐  │
│  │  State: Thread (turns[], domain, targets[], ct{})      │  │
│  └────────────────────────────────────────────────────────┘  │
│                                                                │
│  ┌──────────┐    ┌──────────┐    ┌──────────┐              │
│  │  Router  │───▶│   Mio    │───▶│  Shiro   │              │
│  │   Node   │◀───│   Node   │◀───│   Node   │              │
│  └──────────┘    └──────────┘    └──────────┘              │
│       │               │                 │                     │
│       └───────────────┴─────────────────┘                     │
│                       │                                        │
│                       ▼                                        │
│            ┌─────────────────────┐                           │
│            │   Memory Manager    │                           │
│            │  - Recall (想起)    │                           │
│            │  - Store (記録)     │                           │
│            │  - Novelty (新規性) │                           │
│            └─────────────────────┘                           │
└───────────────────────┬──────────────────────────────────────┘
                        │
        ┌───────────────┼───────────────┐
        │               │               │
        ▼               ▼               ▼
┌─────────────┐ ┌─────────────┐ ┌─────────────┐
│   短期記憶   │ │   中期記憶   │ │   長期記憶   │
│  (RAM State) │ │ Redis/DuckDB │ │  VectorDB   │
│  6-8ターン   │ │   24h-7d    │ │   無期限     │
└─────────────┘ └─────────────┘ └─────────────┘
```

### 2.2 v4.0からの変更点

| 項目 | v4.0（分散実行） | v5.0（会話LLM） |
|------|-----------------|----------------|
| **Orchestrator** | MessageOrchestrator | ConversationOrchestrator (LangGraph) |
| **状態管理** | Session（単純な履歴） | Thread（話題のまとまり）+ Session |
| **記憶** | セッション履歴のみ | 4層記憶（短期/中期/長期/KB） |
| **会話参加** | Chatのみ | Chat + Worker（idle時） + Coder（将来） |
| **知識学習** | なし | 新規性判定で自動学習 |

### 2.3 互換性

**v4.0との互換性**:
- ✅ 設定フラグ（`conversation.enabled`）で切り替え
- ✅ 既存の `MessageOrchestrator` と共存可能
- ✅ 5分以内でロールバック可能
- ✅ 分散実行（Transport層）はそのまま利用可能

---

## 3. データモデル

### 3.1 Message（メッセージ）

**定義**:
```go
// Message は発話の最小単位
type Message struct {
	Speaker   string                 `json:"speaker"`    // user, mio, shiro, aka, ao, gin, system, tool, memory
	Msg       string                 `json:"msg"`        // 本文
	Timestamp time.Time              `json:"ts"`         // タイムスタンプ
	Meta      map[string]interface{} `json:"meta,omitempty"` // 引用元、検索URL、ドメイン等
}
```

**Speaker種別**:
| Speaker | 説明 |
|---------|------|
| `user` | ユーザー入力 |
| `mio` | Chat（Mio） |
| `shiro` | Worker（Shiro） |
| `aka` / `ao` / `gin` | Coder（将来） |
| `system` | システムメッセージ（エラー、通知等） |
| `tool` | ツール実行結果（検索、ファイル操作等） |
| `memory` | 想起パック（過去の会話要約） |

**Meta例**:
```json
{
  "source": "google_search",
  "url": "https://example.com",
  "domain": "movie",
  "is_novel": true,
  "similarity": 0.65
}
```

---

### 3.2 Thread（スレッド）

**定義**:
```go
// Thread は「話題のまとまり」（6〜8ターン相当）
type Thread struct {
	ID        int64              `json:"thread_id"`
	SessionID string             `json:"session_id"`
	Domain    string             `json:"domain"`      // movie, tech, general等
	Turns     []Message          `json:"turns"`       // 最新12件
	Targets   []string           `json:"targets"`     // 指名キャラ（例: ["mio"]）
	Cooldown  map[string]int     `json:"ct"`          // クールタイム（例: {"mio":0, "shiro":1}）
	StartTime time.Time          `json:"ts_start"`
	EndTime   *time.Time         `json:"ts_end,omitempty"`
	Status    ThreadStatus       `json:"status"`      // active, closed, archived
}

type ThreadStatus string

const (
	ThreadActive   ThreadStatus = "active"
	ThreadClosed   ThreadStatus = "closed"
	ThreadArchived ThreadStatus = "archived"
)
```

**保持数**: 最新12メッセージ（6〜8往復相当）

**Thread開始条件**（いずれか）:
1. 初回入力
2. 新トピック提示（キーワード: 「ところで」「別件」「質問変えて」）
3. キャラ側提案へのユーザー応答

**Thread終了条件**（いずれか）:
1. 話題切替語検出
2. コサイン類似度 < 0.75（embedding評価）
3. ドメイン変化（CHAT → RESEARCH 等）
4. 10分無入力
5. ターン上限（12ターン超）

---

### 3.3 Session（セッション）

**定義**:
```go
// Session はプロセス再起動や割り込み復帰に必要なまとまり
type Session struct {
	ID           string          `json:"session_id"`   // 例: sess_20260304_001
	UserID       string          `json:"user_id"`      // LINE User ID等
	History      []ThreadSummary `json:"history"`      // Thread要約リスト
	Agenda       string          `json:"agenda"`       // 継続中のタスク
	LastThreadID int64           `json:"last_thread_id"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}
```

**保持期間**:
- Redis（hot）: 24時間
- DuckDB（warm）: 7日間

---

### 3.4 ThreadSummary（Thread要約）

**定義**:
```go
// ThreadSummary はThread終了時に生成される要約
type ThreadSummary struct {
	ThreadID  int64     `json:"thread_id"`
	Domain    string    `json:"domain"`
	Summary   string    `json:"summary"`    // LLM生成要約（200文字以内）
	Keywords  []string  `json:"keywords"`   // LLM抽出キーワード（5〜10個）
	Embedding []float32 `json:"embedding"`  // VectorDB用（次元数: 384 or 1536）
	StartTime time.Time `json:"ts_start"`
	EndTime   time.Time `json:"ts_end"`
	IsNovel   bool      `json:"is_novel"`   // 新規情報フラグ
}
```

**要約生成プロンプト例**:
```
以下の会話を200文字以内で要約してください。
また、重要なキーワードを5〜10個抽出してください。

【会話】
（Thread.Turnsの内容）

【要約】
（LLM出力）

【キーワード】
（LLM出力）
```

---

### 3.5 AgentStatus（Agent状態）

**定義**:
```go
// AgentStatus はAgentのタスク状態を管理
type AgentStatus struct {
	AgentName     string    `json:"agent_name"`      // mio, shiro, aka, ao, gin
	IsIdle        bool      `json:"is_idle"`         // タスクが空いているか
	LastTaskTime  time.Time `json:"last_task"`       // 最後のタスク完了時刻
	CurrentTask   string    `json:"current_task"`    // 現在のタスク（空なら""）
	ConversationOK bool     `json:"conversation_ok"` // 会話参加可能か
}
```

**判定ロジック**:
```go
func (s *AgentStatus) CanJoinConversation() bool {
	// Chat（Mio）は常時参加可能
	if s.AgentName == "mio" {
		return true
	}

	// Worker（Shiro）はタスクが空いている時のみ
	if s.AgentName == "shiro" {
		return s.IsIdle && s.CurrentTask == ""
	}

	// Coder（将来）: 条件未定
	return false
}
```

---

## 4. 記憶レイヤー

### 4.1 レイヤー一覧

| レイヤー | 物理ストア | TTL/保持期間 | 目的 | 実装 |
|---------|-----------|-------------|------|------|
| **短期記憶** | LangGraph State（RAM） | 1 Thread（6〜8ターン） | アクティブ会話の文脈 | `internal/application/orchestrator/` |
| **中期記憶** | Redis（hot）→ DuckDB（warm） | 24h（Redis）→ 7d（DuckDB） | セッション継続、割り込み復帰 | `internal/infrastructure/persistence/memory/` |
| **長期記憶** | VectorDB `user:<uid>` + MetaDB | 無期限（手動削除可能） | プロファイル、Thread要約、重要知識 | `internal/infrastructure/persistence/memory/vectordb.go` |
| **知識ベース** | VectorDB `kb:<domain>` | 無期限（ETL更新） | RAG用ドメイン資料 | `internal/infrastructure/persistence/memory/vectordb.go` |

### 4.2 短期記憶（Thread State）

**保持構造**（LangGraph State）:
```go
type ConversationState struct {
	Thread      Thread            `json:"thread"`
	AgentStatus map[string]AgentStatus `json:"agent_status"`
	RecallPack  []Message         `json:"recall_pack"`  // 想起パック
	NextSpeaker string            `json:"next_speaker"` // Router決定
}
```

**保持数**: 最新12メッセージ

**更新タイミング**:
- ターン開始時: Recall実行 → RecallPack生成
- ターン終了時: Thread.Turnsに追加
- Thread終了時: ThreadSummary生成 → 中期記憶へフラッシュ

---

### 4.3 中期記憶（Redis / DuckDB）

#### 4.3.1 Redis（hot）

**キー設計**:
```
sess:<session_id> = Session JSON
  - history: []ThreadSummary
  - agenda: string
  - last_thread_id: int64
TTL: 24h
```

**Redis操作**:
```go
// Session保存
func (r *RedisStore) SaveSession(ctx context.Context, sess Session) error

// Session取得
func (r *RedisStore) GetSession(ctx context.Context, sessionID string) (Session, error)

// Session削除（TTL期限切れで自動削除、または手動削除）
func (r *RedisStore) DeleteSession(ctx context.Context, sessionID string) error
```

#### 4.3.2 DuckDB（warm）

**テーブル設計**:
```sql
CREATE TABLE session_thread (
  thread_id INTEGER PRIMARY KEY,
  session_id TEXT NOT NULL,
  ts_start TIMESTAMP NOT NULL,
  ts_end TIMESTAMP,
  domain TEXT,
  summary TEXT,
  keywords TEXT[],        -- 配列型
  embedding FLOAT[],      -- 配列型（384次元 or 1536次元）
  is_novel BOOLEAN,
  INDEX idx_session (session_id),
  INDEX idx_domain (domain),
  INDEX idx_ts (ts_start)
);
```

**DuckDB操作**:
```go
// ThreadSummary保存
func (d *DuckDBStore) SaveThreadSummary(ctx context.Context, summary ThreadSummary) error

// Session履歴取得
func (d *DuckDBStore) GetSessionHistory(ctx context.Context, sessionID string, limit int) ([]ThreadSummary, error)

// ドメイン検索
func (d *DuckDBStore) SearchByDomain(ctx context.Context, domain string, limit int) ([]ThreadSummary, error)
```

**バックグラウンド処理**:
- Redis → DuckDB フラッシュ（1時間ごと）
- DuckDB クリーンアップ（7日以上経過したレコードを削除）

---

### 4.4 長期記憶（VectorDB + MetaDB）

#### 4.4.1 VectorDB設計

**namespace**: `user:<uid>`（例: `user:line_U123456`）

**保存対象**:
- Thread要約（`ThreadSummary`）
- 重要プロファイル（「好きな映画: SF」等）
- 繰り返し出現するトピック
- KPI履歴（満足度、エンゲージメント）

**VectorDB操作**:
```go
// 長期記憶保存
func (v *VectorDBStore) SaveLongTermMemory(ctx context.Context, userID string, summary ThreadSummary) error

// 類似検索
func (v *VectorDBStore) SearchSimilar(ctx context.Context, userID string, query string, topK int) ([]ThreadSummary, error)

// メタフィルタ検索
func (v *VectorDBStore) SearchWithFilter(ctx context.Context, userID string, filter map[string]interface{}, topK int) ([]ThreadSummary, error)
```

#### 4.4.2 MetaDB設計（オプション）

Thread要約のメタ情報を別管理する場合:
```sql
CREATE TABLE thread_meta (
  thread_id INTEGER PRIMARY KEY,
  user_id TEXT NOT NULL,
  domain TEXT,
  keywords TEXT[],
  ts_start TIMESTAMP,
  ts_end TIMESTAMP,
  is_novel BOOLEAN,
  similarity_score FLOAT,
  INDEX idx_user (user_id),
  INDEX idx_domain (domain)
);
```

---

### 4.5 知識ベース（Domain KB）

**namespace**: `kb:<domain>`（例: `kb:movie`, `kb:history`）

**データソース**:
1. Worker の RESEARCH 検索結果（Google Custom Search）
2. ETL による外部データ取り込み（週次/月次）
3. 手動登録（運用ツール）

**KB操作**:
```go
// KB保存
func (v *VectorDBStore) SaveKB(ctx context.Context, domain string, doc Document) error

// KB検索
func (v *VectorDBStore) SearchKB(ctx context.Context, domain string, query string, topK int) ([]Document, error)
```

**Document構造**:
```go
type Document struct {
	ID        string    `json:"id"`
	Domain    string    `json:"domain"`
	Content   string    `json:"content"`
	Source    string    `json:"source"`      // URL等
	Embedding []float32 `json:"embedding"`
	Meta      map[string]interface{} `json:"meta"`
}
```

---

### 4.6 記憶の新規性判定

**目的**: Chatが知らない情報かを判定し、記憶対象を決定

**判定フロー**:
```
1. Thread内のメッセージから重要情報を抽出（LLM）
   - プロンプト: 「以下の会話から、新規知識を抽出してください」
2. 抽出情報のEmbedding生成
3. VectorDB類似検索（長期記憶 + KB）
   - topK=5
4. 最高類似度を取得
5. 類似度 < 0.8 なら「新規情報」と判定
6. 新規情報なら長期記憶へ保存
```

**実装**:
```go
// 新規性判定
func (m *MemoryManager) IsNovelInformation(ctx context.Context, msg Message) (bool, float32, error)

// 記憶判定（ユーザー⇄Chat or 新規情報）
func (m *MemoryManager) ShouldMemorize(ctx context.Context, thread Thread) (bool, error)
```

**判定例**:
| メッセージ | 類似度 | 判定 | 理由 |
|-----------|--------|------|------|
| Worker: 「ファイルを作成しました」 | 0.95 | ❌ 記憶不要 | 既知の手順 |
| Worker: 「このライブラリはv3.0以降で破壊的変更があります」 | 0.65 | ✅ 記憶 | 新規知識 |
| Coder: 「この設計パターンはメモリ効率が良いです」 | 0.55 | ✅ 記憶 | 新規知識 |
| User: 「好きな映画はSF」 | 0.70 | ✅ 記憶 | ユーザー⇄Chatなので常に記憶 |

---

## 5. LangGraph設計

[WARN] 本節の LangGraph ベース `ConversationOrchestrator` は設計候補であり、現行採用ではない。現行の実装正本は `実装仕様_会話エンジン_v5.1.md` の Go `ConversationEngine` である。

### 5.1 ノード構成

```
ConversationOrchestrator (LangGraph)
  ├─ Router Node（発話者決定）
  ├─ Mio Node（Chat Agent）
  ├─ Shiro Node（Worker Agent）
  ├─ Memory Manager（想起・記録）
  └─ Adapter Manager（検索・KB参照）
```

### 5.2 State定義

```go
type ConversationState struct {
	// Thread情報
	Thread      Thread            `json:"thread"`

	// Agent状態
	AgentStatus map[string]AgentStatus `json:"agent_status"`

	// 想起パック（過去の会話要約）
	RecallPack  []Message         `json:"recall_pack"`

	// Router決定
	NextSpeaker string            `json:"next_speaker"`

	// 検索結果（一時保存）
	SearchResults []SearchResult  `json:"search_results"`

	// フラグ
	ThreadShouldClose bool        `json:"thread_should_close"`
}
```

### 5.3 ノード定義

#### 5.3.1 Router Node

**責務**: 発話者（Mio/Shiro）を決定

**入力**: `ConversationState`

**出力**: `ConversationState.NextSpeaker`

**ロジック**:
```go
func RouterNode(ctx context.Context, state ConversationState) (ConversationState, error) {
	// 1. ユーザー指名チェック（@ミオ, @シロ）
	if mentions := extractMentions(state.Thread.Turns[len(state.Thread.Turns)-1].Msg); len(mentions) > 0 {
		state.NextSpeaker = mentions[0]
		return state, nil
	}

	// 2. Agent状態フィルタ
	candidates := []string{}
	for name, status := range state.AgentStatus {
		if status.CanJoinConversation() {
			candidates = append(candidates, name)
		}
	}

	// 3. ドメイン適性判定
	if domain := state.Thread.Domain; domain != "" {
		// 検索系 → Shiro優先
		if domain == "RESEARCH" && contains(candidates, "shiro") {
			state.NextSpeaker = "shiro"
			return state, nil
		}
		// 即答系 → Mio優先
		if contains(candidates, "mio") {
			state.NextSpeaker = "mio"
			return state, nil
		}
	}

	// 4. ラウンドロビン + クールタイム
	state.NextSpeaker = selectWithCooldown(candidates, state.Thread.Cooldown)

	// 5. クールタイム更新
	state.Thread.Cooldown[state.NextSpeaker] = 0
	for name := range state.Thread.Cooldown {
		if name != state.NextSpeaker {
			state.Thread.Cooldown[name]++
		}
	}

	return state, nil
}
```

#### 5.3.2 Mio Node（Chat Agent）

**責務**: ユーザー対話、即答、ルーティング決定

**入力**: `ConversationState`

**出力**: `ConversationState.Thread.Turns`（応答を追加）

**ロジック**:
```go
func MioNode(ctx context.Context, state ConversationState) (ConversationState, error) {
	// 1. プロンプト組み立て
	prompt := buildPrompt(state.Thread, state.RecallPack)

	// 2. LLM呼び出し
	response, err := state.MioAgent.Generate(ctx, prompt)
	if err != nil {
		return state, err
	}

	// 3. 応答をThreadに追加
	msg := Message{
		Speaker:   "mio",
		Msg:       response,
		Timestamp: time.Now(),
	}
	state.Thread.Turns = append(state.Thread.Turns, msg)

	// 4. Thread終了判定
	state.ThreadShouldClose = shouldCloseThread(state.Thread)

	return state, nil
}
```

#### 5.3.3 Shiro Node（Worker Agent）

**責務**: 実行・道具係、RESEARCH検索

**入力**: `ConversationState`

**出力**: `ConversationState.Thread.Turns`（応答を追加）

**ロジック**:
```go
func ShiroNode(ctx context.Context, state ConversationState) (ConversationState, error) {
	// 1. タスク実行判定
	if needsExecution(state.Thread.Turns[len(state.Thread.Turns)-1].Msg) {
		// Worker実行ロジック（既存のWorkerExecutionService流用）
		result, err := executeWorkerTask(ctx, state)
		if err != nil {
			return state, err
		}

		// 実行結果をThreadに追加
		msg := Message{
			Speaker:   "shiro",
			Msg:       result.Summary,
			Timestamp: time.Now(),
			Meta: map[string]interface{}{
				"execution_status": result.Status,
				"git_commit":       result.GitCommit,
			},
		}
		state.Thread.Turns = append(state.Thread.Turns, msg)
	} else {
		// 会話のみ
		prompt := buildPrompt(state.Thread, state.RecallPack)
		response, err := state.ShiroAgent.Generate(ctx, prompt)
		if err != nil {
			return state, err
		}

		msg := Message{
			Speaker:   "shiro",
			Msg:       response,
			Timestamp: time.Now(),
		}
		state.Thread.Turns = append(state.Thread.Turns, msg)
	}

	return state, nil
}
```

### 5.4 エッジ定義

```go
func BuildConversationGraph() *langgraph.Graph {
	graph := langgraph.NewGraph()

	// ノード登録
	graph.AddNode("router", RouterNode)
	graph.AddNode("mio", MioNode)
	graph.AddNode("shiro", ShiroNode)
	graph.AddNode("memory", MemoryNode)

	// エッジ定義
	graph.AddEdge("START", "memory")        // 想起
	graph.AddEdge("memory", "router")       // Router決定
	graph.AddConditionalEdge("router", func(state ConversationState) string {
		return state.NextSpeaker // "mio" or "shiro"
	})
	graph.AddEdge("mio", "memory")          // 記録
	graph.AddEdge("shiro", "memory")        // 記録
	graph.AddConditionalEdge("memory", func(state ConversationState) string {
		if state.ThreadShouldClose {
			return "END"
		}
		return "router"
	})

	return graph
}
```

---

## 6. コンポーネント仕様

### 6.1 MemoryManager

**責務**: 記憶の想起・記録・新規性判定

**インターフェース**:
```go
type MemoryManager interface {
	// 想起（Recall）: 短期 → 中期 → 長期 → KB の順に検索
	Recall(ctx context.Context, sessionID string, query string) ([]Message, error)

	// 記録（Store）: ターン終了時、Thread終了時
	Store(ctx context.Context, thread Thread) error

	// Thread終了時の要約生成とフラッシュ
	FlushThread(ctx context.Context, thread Thread) (ThreadSummary, error)

	// 新規性判定
	IsNovelInformation(ctx context.Context, msg Message) (bool, float32, error)

	// 記憶判定（ユーザー⇄Chat or 新規情報）
	ShouldMemorize(ctx context.Context, thread Thread) (bool, error)

	// Session取得
	GetSession(ctx context.Context, sessionID string) (Session, error)

	// Session保存
	SaveSession(ctx context.Context, session Session) error
}
```

**実装**:
```go
type memoryManager struct {
	redisStore   *RedisStore
	duckDBStore  *DuckDBStore
	vectorDBStore *VectorDBStore
	llmProvider  LLMProvider
}

func NewMemoryManager(
	redis *RedisStore,
	duckdb *DuckDBStore,
	vectordb *VectorDBStore,
	llm LLMProvider,
) MemoryManager {
	return &memoryManager{
		redisStore:   redis,
		duckDBStore:  duckdb,
		vectorDBStore: vectordb,
		llmProvider:  llm,
	}
}
```

---

### 6.2 ConversationOrchestrator

**責務**: LangGraphの実行とState管理

**インターフェース**:
```go
type ConversationOrchestrator interface {
	// 会話ターン実行
	ProcessTurn(ctx context.Context, userMsg Message, sessionID string) (Message, error)

	// Thread取得
	GetThread(ctx context.Context, threadID int64) (Thread, error)

	// Session取得
	GetSession(ctx context.Context, sessionID string) (Session, error)
}
```

**実装**:
```go
type conversationOrchestrator struct {
	graph         *langgraph.Graph
	memoryManager MemoryManager
	mioAgent      *agent.MioAgent
	shiroAgent    *agent.ShiroAgent
	agentStatus   map[string]*AgentStatus
}

func NewConversationOrchestrator(
	memMgr MemoryManager,
	mio *agent.MioAgent,
	shiro *agent.ShiroAgent,
) ConversationOrchestrator {
	orch := &conversationOrchestrator{
		memoryManager: memMgr,
		mioAgent:      mio,
		shiroAgent:    shiro,
		agentStatus: map[string]*AgentStatus{
			"mio":   {AgentName: "mio", IsIdle: true, ConversationOK: true},
			"shiro": {AgentName: "shiro", IsIdle: true, ConversationOK: true},
		},
	}

	orch.graph = BuildConversationGraph()
	return orch
}
```

---

### 6.3 AgentStatusManager

**責務**: Agent状態（idle/busy）の管理

**インターフェース**:
```go
type AgentStatusManager interface {
	// 状態取得
	GetStatus(agentName string) (AgentStatus, error)

	// 状態更新
	UpdateStatus(agentName string, status AgentStatus) error

	// タスク開始
	StartTask(agentName string, taskID string) error

	// タスク完了
	FinishTask(agentName string) error

	// 会話参加可能なAgentリスト
	GetAvailableAgents() ([]string, error)
}
```

---

## 7. API仕様

### 7.1 内部API（Goインターフェース）

上記の `MemoryManager`, `ConversationOrchestrator`, `AgentStatusManager` を参照。

### 7.2 管理ツールAPI（CLI/HTTP）

**CLI**: `cmd/rencrow-memory-admin/`

```bash
# Thread一覧
rencrow-memory-admin thread list --session <session_id>

# Thread詳細
rencrow-memory-admin thread get --thread <thread_id>

# Thread削除
rencrow-memory-admin thread delete --thread <thread_id>

# Session一覧
rencrow-memory-admin session list --user <user_id>

# Session復帰テスト
rencrow-memory-admin session restore --session <session_id>

# メモリ使用量
rencrow-memory-admin stats
```

### 7.3 KB（Knowledge Base）API

**CLI**: `cmd/kb-admin/` （Phase 4.1 実装完了）

```bash
# KB検索テスト
kb-admin search programming "Go言語 並行処理"

# 統計情報表示
kb-admin stats

# ドキュメント一覧（Phase 4.2）
kb-admin list programming

# 古いドキュメント削除（Phase 4.2）
kb-admin cleanup general 30  # 30日より古いドキュメントを削除
```

**Go API**:

```go
// 1. Web検索結果をKBに保存
results := []WebSearchResult{
    {
        Title:   "Go言語の並行処理入門",
        Link:    "https://example.com/go-concurrency",
        Snippet: "ゴルーチンとチャネルの基本的な使い方を解説します。",
    },
}
err := mgr.SaveWebSearchToKB(ctx, "programming", "Go言語 並行処理", results)

// 2. KB検索（RAG）
docs, err := mgr.SearchKB(ctx, "programming", "Go言語のエラーハンドリング", 5)
for _, doc := range docs {
    fmt.Printf("[Score: %.4f] %s\n", doc.Score, doc.Source)
}

// 3. 自動RAG統合（ConversationEngine.BeginTurn）
pack, err := engine.BeginTurn(ctx, sessionID, userMessage)
// pack.LongFacts に [KB] プレフィックス付きでKB検索結果が含まれる
```

**ドメイン設計**:
- `general` - 汎用知識
- `programming` - プログラミング技術
- `movie` / `anime` - エンタメ情報
- `tech` / `history` - 専門分野

**詳細**: `docs/KB運用ガイド.md` を参照

---

## 8. フロー図

### 8.1 会話ターンフロー

```
┌────────────────────────────────────────────────────────────┐
│  ユーザー入力                                               │
└───────────────────────┬────────────────────────────────────┘
                        │
                        ▼
┌────────────────────────────────────────────────────────────┐
│  ConversationOrchestrator.ProcessTurn()                     │
└───────────────────────┬────────────────────────────────────┘
                        │
                        ▼
┌────────────────────────────────────────────────────────────┐
│  Memory Node: Recall（想起）                                │
│  - 短期記憶から最新メッセージ取得                            │
│  - 中期記憶からSession要約取得                              │
│  - 長期記憶から類似Thread検索                               │
│  - KBからドメイン知識検索                                   │
│  → RecallPack生成                                          │
└───────────────────────┬────────────────────────────────────┘
                        │
                        ▼
┌────────────────────────────────────────────────────────────┐
│  Router Node: 発話者決定                                    │
│  1. ユーザー指名チェック（@ミオ, @シロ）                    │
│  2. Agent状態フィルタ（idle/busy）                         │
│  3. ドメイン適性判定（検索→Shiro, 即答→Mio）              │
│  4. ラウンドロビン + クールタイム                           │
│  → NextSpeaker決定（mio or shiro）                         │
└───────────────────────┬────────────────────────────────────┘
                        │
        ┌───────────────┴───────────────┐
        │                               │
        ▼                               ▼
┌──────────────────┐          ┌──────────────────┐
│  Mio Node        │          │  Shiro Node      │
│  - プロンプト組立 │          │  - タスク実行判定 │
│  - LLM呼び出し   │          │  - Worker実行     │
│  - 応答生成      │          │  - 応答生成       │
└──────────┬───────┘          └──────┬───────────┘
           │                         │
           └───────────┬─────────────┘
                       │
                       ▼
┌────────────────────────────────────────────────────────────┐
│  Memory Node: Store（記録）                                 │
│  - Thread.Turnsに応答追加                                   │
│  - Thread終了判定                                           │
│    → 終了なら FlushThread（要約生成 → 中期記憶へ）          │
│    → 新規性判定 → 長期記憶へ保存                            │
└───────────────────────┬────────────────────────────────────┘
                        │
                        ▼
┌────────────────────────────────────────────────────────────┐
│  応答返却                                                    │
└────────────────────────────────────────────────────────────┘
```

### 8.2 Thread終了フロー

```
┌────────────────────────────────────────────────────────────┐
│  Thread終了判定（いずれか）                                  │
│  - 話題切替語検出                                           │
│  - コサイン類似度 < 0.75                                    │
│  - ドメイン変化                                             │
│  - 10分無入力                                               │
│  - ターン上限（12ターン超）                                 │
└───────────────────────┬────────────────────────────────────┘
                        │
                        ▼
┌────────────────────────────────────────────────────────────┐
│  MemoryManager.FlushThread()                                │
└───────────────────────┬────────────────────────────────────┘
                        │
                        ▼
┌────────────────────────────────────────────────────────────┐
│  要約生成（LLM）                                             │
│  - Thread.Turnsから重要情報抽出                             │
│  - 要約文生成（200文字以内）                                │
│  - キーワード抽出（5〜10個）                                │
│  - Embedding生成                                            │
└───────────────────────┬────────────────────────────────────┘
                        │
                        ▼
┌────────────────────────────────────────────────────────────┐
│  新規性判定                                                  │
│  - VectorDB類似検索（長期記憶 + KB）                        │
│  - 類似度 < 0.8 なら「新規情報」                            │
└───────────────────────┬────────────────────────────────────┘
                        │
        ┌───────────────┴───────────────┐
        │                               │
        ▼                               ▼
┌──────────────────┐          ┌──────────────────┐
│  中期記憶へ保存   │          │  長期記憶へ保存   │
│  - Redis         │          │  - VectorDB      │
│  - DuckDB        │          │  （新規情報のみ） │
└──────────────────┘          └──────────────────┘
```

---

## 9. 実装ガイド

### 9.1 Phase 1: 基盤整備（Week 1-2）

**目標**: データモデルとMemory Manager実装

**ディレクトリ構造**:
```
internal/
  domain/
    memory/
      models.go           # Message, Thread, Session, ThreadSummary
      manager.go          # MemoryManager インターフェース
      novelty.go          # 新規性判定ロジック
  infrastructure/
    persistence/
      memory/
        redis.go          # RedisStore
        duckdb.go         # DuckDBStore
        vectordb.go       # VectorDBStore
  domain/
    agent/
      status.go           # AgentStatus, AgentStatusManager
```

**タスクリスト**:
- [ ] データモデル定義（`models.go`）
- [ ] MemoryManager インターフェース（`manager.go`）
- [ ] RedisStore実装（`redis.go`）
- [ ] DuckDBStore実装（`duckdb.go`）
- [ ] VectorDBStore実装（`vectordb.go`）
- [ ] 新規性判定ロジック（`novelty.go`）
- [ ] AgentStatus実装（`status.go`）
- [ ] 単体テスト（カバレッジ 90%以上）

---

### 9.2 Phase 2: LangGraph統合（Week 3-4）

**目標**: ConversationOrchestrator + Router Node実装

**ディレクトリ構造**:
```
internal/
  application/
    orchestrator/
      conversation_orchestrator.go  # ConversationOrchestrator
      graph.go                       # LangGraph定義
      nodes.go                       # Router/Mio/Shiro/Memory Node
```

**タスクリスト**:
- [ ] LangGraph依存追加（`go get github.com/langchain-ai/langgraph-go`）
- [ ] ConversationState定義（`graph.go`）
- [ ] Router Node実装（`nodes.go`）
- [ ] Mio Node実装（`nodes.go`）
- [ ] Shiro Node実装（`nodes.go`）
- [ ] Memory Node実装（`nodes.go`）
- [ ] エッジ定義（`graph.go`）
- [ ] ConversationOrchestrator実装（`conversation_orchestrator.go`）
- [ ] 統合テスト

---

### 9.3 Phase 3: Thread管理とVectorDB（Week 5-6）

**目標**: Thread開始/終了条件、長期記憶（VectorDB）実装

**タスクリスト**:
- [ ] Thread開始/終了条件実装（`internal/domain/memory/thread_lifecycle.go`）
- [ ] Thread要約生成（LLM）
- [ ] Embedding生成（`internal/infrastructure/embedding/`）
- [ ] VectorDB統合（ChromaDB or Qdrant）
- [ ] 長期記憶の想起フロー実装
- [ ] E2Eテスト（Thread作成 → 要約 → 検索）

---

### 9.4 Phase 4: KB統合と運用準備（Week 7-8）

**目標**: 知識ベース（KB）統合、運用ツール整備

#### Phase 4.1: KB基盤実装 ✅ 完了（2026-03-07）

**実装内容**:
- [x] KB検索機能実装（VectorDB `kb_{domain}` コレクション）
  - `SaveKB()` - Knowledge BaseへDocument保存
  - `SearchKB()` - ドメイン別ベクトル検索（Top-K）
  - `initKBCollection()` - ドメイン別コレクション自動作成
- [x] SaveWebSearchToKB（RealConversationManager）
  - Web検索結果→Document変換
  - Title + Snippet の embedding 生成
  - メタ情報保存（query, search_index）
- [x] SearchKB（RealConversationManager）
  - クエリの embedding 生成
  - VectorDB検索実行
  - Document配列を返却
- [x] Chat RAG統合（ConversationEngine.BeginTurn）
  - 現在のドメインを取得
  - KB検索を自動実行
  - 検索結果を `[KB]` プレフィックス付きで LongFacts に追加
- [x] KB管理CLI（`cmd/kb-admin/`）
  - `search` - KB検索テスト
  - `stats` - 統計情報表示
  - `list` - ドキュメント一覧（基本構造）
  - `cleanup` - 古いドキュメント削除（基本構造）
- [x] ドキュメント整備
  - `docs/KB運用ガイド.md` 作成
  - `cmd/kb-admin/README.md` 作成
  - 実装仕様更新

**テスト結果**: 47/47 PASS ✓

#### Phase 4.2: Worker RESEARCH自動保存 ✅ 完了 (2026-03-07)

**実装内容**:
- [x] ToolRunner リファクタ（構造化レスポンス対応）
  - ToolResponse に Metadata フィールド追加
  - ExecuteV2 メソッド追加（agent.ToolRunner インターフェース拡張）
  - web_search V2実装（executeWebSearchV2）
- [x] Mio executeWebSearch 改修
  - ExecuteV2 で構造化データ取得
  - Metadata から search_items 抽出
  - SaveWebSearchToKB() 自動呼び出し
- [x] ConversationManager DI統合
  - main.go で realMgr を Mio に注入
  - WithConversationManager() メソッド追加
  - nil チェック + ログ出力
- [x] E2Eテスト追加（3件）
  - TestKBAutosave_WebSearch_SavesCalled
  - TestKBAutosave_NoConversationManager_GracefulDegradation
  - TestKBAutosave_MetadataExtraction

**動作フロー**:
```
ユーザー: "Rustについて教えて"
  ↓
Mio.Chat() → キーワード検出（"教えて"）
  ↓
executeWebSearch() → toolRunner.ExecuteV2("web_search", ...)
  ↓
Metadata から search_items 抽出
  ↓
conversationMgr.SaveWebSearchToKB("general", "Rustについて", results)
  ↓
KB に自動保存 ✅
```

**テスト結果**: 15/15 PASS (統合テスト全通過)

**制限事項**:
- domain は現在 "general" 固定（Thread.Domain 取得は将来対応）
- kb-admin の Embedder 未初期化（search コマンドが動作しない可能性）
  - [ ] バッチ処理対応（複数ドメイン一括処理）
- [ ] 本番デプロイ準備
  - [ ] 設定ファイル検証
  - [ ] ヘルスチェック追加（VectorDB接続監視）
  - [ ] ログ・メトリクス整備

---

## 10. テスト仕様

### 10.1 単体テスト

**カバレッジ目標**: 90%以上

**テストファイル**:
```
internal/domain/memory/manager_test.go
internal/infrastructure/persistence/memory/redis_test.go
internal/infrastructure/persistence/memory/duckdb_test.go
internal/infrastructure/persistence/memory/vectordb_test.go
internal/domain/agent/status_test.go
```

**テストケース例**:
```go
func TestMemoryManager_Recall(t *testing.T) {
	// 短期 → 中期 → 長期 → KB の順に検索
}

func TestMemoryManager_FlushThread(t *testing.T) {
	// Thread要約生成 → 中期記憶へ保存
}

func TestMemoryManager_IsNovelInformation(t *testing.T) {
	// 類似度 < 0.8 なら新規情報
}

func TestAgentStatus_CanJoinConversation(t *testing.T) {
	// Mio: 常時参加可能
	// Shiro: idle時のみ参加可能
}
```

---

### 10.2 統合テスト

**テストシナリオ**:
1. **基本会話フロー**:
   - ユーザー入力 → Router → Mio → 応答
2. **Thread開始/終了**:
   - 初回入力 → Thread作成 → 10分無入力 → Thread終了 → 要約生成
3. **新規性判定**:
   - Worker応答「新規知識」→ 類似度 < 0.8 → 長期記憶へ保存
4. **Session復帰**:
   - プロセス再起動 → Session復元 → 会話継続

---

### 10.3 E2Eテスト

**テストツール**: `cmd/test-chat/`, `cmd/test-worker/`

**シナリオ**:
```
1. ユーザー: 「おすすめの映画教えて」
   → Mio: 「どんなジャンルがお好きですか？」
2. ユーザー: 「SF映画」
   → Mio: 「インターステラーやブレードランナー2049はいかがでしょう？」
3. ユーザー: 「ブレードランナー2049について詳しく教えて」
   → Mio: （KB検索 → RAG） → 詳細説明
4. 10分無入力 → Thread終了 → 要約生成
5. ユーザー: 「さっきの映画の話、覚えてる？」
   → Mio: （中期記憶から復元） → 「はい、ブレードランナー2049についてお話ししましたね」
```

---

## 11. 運用仕様

### 11.1 メモリ使用量

**目標**: <10MB（RenCrow本体のみ）

**内訳**:
- LangGraph State（RAM）: ~2MB（Thread 12メッセージ × 複数Session）
- Redis: 外部プロセス（メモリ制約外）
- DuckDB: 外部プロセス（メモリ制約外）
- VectorDB: 外部プロセス（メモリ制約外）

**モニタリング**:
```bash
# メモリ使用量確認
rencrow-memory-admin stats
```

---

### 11.2 ログ

**必須項目**:
- すべてのログに `turn_id`, `thread_id`, `session_id` を付与
- Thread開始/終了をログ出力
- 想起パックの内容をログ出力（デバッグ用）

**ログ例**:
```json
{
  "level": "info",
  "event": "thread.start",
  "thread_id": 42,
  "session_id": "sess_20260304_001",
  "user_id": "line_U123456",
  "ts": "2026-03-04T10:00:00Z"
}

{
  "level": "info",
  "event": "thread.close",
  "thread_id": 42,
  "session_id": "sess_20260304_001",
  "summary": "SF映画についての会話",
  "keywords": ["SF", "映画", "ブレードランナー2049"],
  "is_novel": false,
  "ts": "2026-03-04T10:15:00Z"
}
```

---

### 11.3 バックアップ

**DuckDB**:
- 毎日深夜にバックアップ（`duckdb_backup_YYYYMMDD.db`）
- 7日分保持

**VectorDB**:
- ベンダー提供のバックアップ機能を利用
- または定期的にエクスポート（JSON/CSV）

---

## 12. マイグレーション

### 12.1 v4.0からv5.0への移行

**移行手順**:
1. **設定追加**（`config.yaml`）:
   ```yaml
   conversation:
     enabled: true
     redis_url: "redis://localhost:6379"
     duckdb_path: "/var/lib/rencrow/memory.duckdb"
     vectordb_url: "http://localhost:6333"  # Qdrant例
   ```

2. **依存インストール**:
   ```bash
   # Redis
   sudo apt install redis-server
   sudo systemctl start redis

   # DuckDB（Goパッケージのみ、外部プロセス不要）
   go get github.com/marcboeker/go-duckdb

   # VectorDB（Qdrant例）
   docker run -d -p 6333:6333 qdrant/qdrant
   ```

3. **データベース初期化**:
   ```bash
   rencrow-memory-admin init
   ```

4. **RenCrow再起動**:
   ```bash
   sudo systemctl restart rencrow
   ```

5. **動作確認**:
   ```bash
   # ヘルスチェック
   curl http://localhost:18790/health

   # メモリ統計
   rencrow-memory-admin stats
   ```

---

### 12.2 ロールバック手順

**v5.0 → v4.0へのロールバック**（5分以内）:

1. **設定変更**（`config.yaml`）:
   ```yaml
   conversation:
     enabled: false  # 無効化
   ```

2. **RenCrow再起動**:
   ```bash
   sudo systemctl restart rencrow
   ```

3. **動作確認**:
   ```bash
   curl http://localhost:18790/health
   ```

**注意**: ロールバック後も、v5.0で保存した記憶データ（Redis/DuckDB/VectorDB）は残ります。再度v5.0に戻す際はそのまま利用可能です。

---

## 13. 付録

### 13.1 用語集

| 用語 | 説明 |
|------|------|
| **Message** | 発話の最小単位 |
| **Turn** | ユーザー入力1回を起点とした応答生成までの単位 |
| **Thread** | 「話題のまとまり」（6〜8ターン相当） |
| **Session** | プロセス再起動や割り込み復帰に必要なまとまり（24h〜7d） |
| **RecallPack** | 想起パック（過去の会話要約を含むプロンプト注入用メッセージ） |
| **新規性判定** | Chatが知らない情報かを判定する処理（類似度 < 0.8） |
| **Agent状態** | Agent（Mio/Shiro）のタスク状態（idle/busy） |

---

### 13.2 参考資料

- **LangGraph公式**: https://github.com/langchain-ai/langgraph
- **RenCrow v4.0実装仕様**: `docs/実装仕様_分散実行_v4.md`
- **Chat/Worker/Coderアーキテクチャ**: `docs/Chat_Worker_Coder_アーキテクチャ.md`

---

## 14. 本番デプロイチェックリスト (Phase 4.2)

### 14.1 環境変数設定

```bash
# ~/.rencrow/.env
ANTHROPIC_API_KEY=sk-ant-...
DEEPSEEK_API_KEY=sk-...
OPENAI_API_KEY=sk-...
GOOGLE_API_KEY=...
GOOGLE_SEARCH_ENGINE_ID=...
```

### 14.2 config.yaml 設定

```yaml
# 会話LLM v5.0: 3層記憶（短期Redis・中期DuckDB・長期Qdrant）
conversation:
  enabled: true  # KB自動保存を有効化
  redis_url: "redis://localhost:6379"
  duckdb_path: "./data/memory.duckdb"
  vectordb_url: "localhost:6334"
  embed_model: "nomic-embed-text"  # Ollama で利用可能なモデル
  summary_model: ""  # 空の場合はOllama chatモデルを使用
```

### 14.3 インフラ起動確認

```bash
# Redis起動確認
redis-cli ping
# 期待結果: PONG

# Qdrant起動確認
curl http://localhost:6333/
# 期待結果: {"title":"qdrant - vector search engine","version":"1.x.x"}

# DuckDB初期化確認（自動作成されるので手動不要）
ls -lh ./data/memory.duckdb
```

### 14.4 Embedder初期化確認

```bash
# Ollama で embedding モデルを pull
ollama pull nomic-embed-text

# モデル確認
ollama list | grep nomic-embed-text
```

### 14.5 KB自動保存動作確認

```bash
# RenCrow起動
./rencrow

# 別ターミナルで動作確認
# （LINE/Slack 経由でテストメッセージ送信）
# 例: "Rustについて教えて"

# ログ確認
tail -f ~/.rencrow/logs/rencrow.log | grep "SaveWebSearchToKB"
# 期待: SaveWebSearchToKB 成功ログが出力される

# kb-admin で確認
./kb-admin stats
# 期待: general コレクションにドキュメントが追加されている
```

### 14.6 E2Eテスト実行

```bash
# 統合テスト実行（KB自動保存含む）
go test ./test/integration/... -v

# 期待結果: 15/15 PASS
# - TestKBAutosave_WebSearch_SavesCalled
# - TestKBAutosave_NoConversationManager_GracefulDegradation
# - TestKBAutosave_MetadataExtraction
```

### 14.7 本番デプロイ手順

```bash
# 1. ビルド
make build

# 2. バイナリ配置
cp rencrow ~/.rencrow/bin/
cp kb-admin ~/.rencrow/bin/

# 3. systemd サービス再起動
systemctl --user restart rencrow

# 4. ログ確認
journalctl --user -u rencrow -f

# 期待ログ:
# - "ConversationEngine v5.1 enabled"
# - "Mio: ConversationManager injected (KB autosave enabled)"
```

### 14.8 トラブルシューティング

#### KB保存が動作しない場合

**症状**: Web検索は実行されるが、KBに保存されない

**確認事項**:
1. `conversation.enabled` が `true` か確認
2. Qdrant が起動しているか確認（`curl localhost:6333/`）
3. Embedder が初期化されているか確認（`ollama list | grep nomic`）
4. ログに `SaveWebSearchToKB failed` エラーがないか確認

**対処法**:
```bash
# Qdrant 再起動
docker compose -f docker-compose.infra.yml restart qdrant

# Embedder 再 pull
ollama pull nomic-embed-text

# RenCrow 再起動
systemctl --user restart rencrow
```

#### Embedder エラー

**症状**: `Failed to generate embedding` エラー

**原因**: Ollama のモデルが未ダウンロード

**対処法**:
```bash
ollama pull nomic-embed-text
systemctl --user restart rencrow
```

---

**最終更新**: 2026-03-07
**バージョン**: 5.0.0 Stable
**メンテナンス**: Phase 4.2完了、本番デプロイ可能
