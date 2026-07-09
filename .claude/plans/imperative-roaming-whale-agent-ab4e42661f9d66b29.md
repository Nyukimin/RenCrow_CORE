# 会話LLM v5.0 Phase 1 実装設計プラン

**作成日**: 2026-03-04
**対象**: PicoClaw v5.0 会話LLMシステム基盤整備
**原則**: 非破壊的追加、オプショナル機能、下位互換性維持

---

## 実装戦略サマリ

Phase 1では、**会話LLMの基盤（データモデル、インターフェース、Config）を準備**し、既存のv3/v4動作を一切変更せずに、将来のPhase 2〜4で会話機能を段階的に追加できる構造を構築します。

### Phase 1の成果物

1. **データモデル定義**（ドメイン層）
2. **ConversationManager インターフェース定義**（ドメイン層）
3. **Config拡張**（Conversation セクション追加）
4. **MioAgent への ConversationManager 注入準備**（DI拡張）
5. **Stub実装**（Redis/DuckDB/VectorDB は Phase 2以降）

**Phase 1では実装しないもの**:
- Redis/DuckDB/VectorDB の実ストア実装
- LangGraph統合
- Thread開始/終了ロジック
- Embedding生成

---

## 1. ファイル配置設計

### 1.1 新規作成ファイル

```
/home/nyukimi/picoclaw_multiLLM/
├── internal/
│   ├── domain/
│   │   ├── conversation/                  # 新規ディレクトリ
│   │   │   ├── message.go                 # Message, Speaker定数
│   │   │   ├── thread.go                  # Thread, ThreadStatus
│   │   │   ├── session_conversation.go    # SessionConversation（v5用Session）
│   │   │   ├── thread_summary.go          # ThreadSummary
│   │   │   ├── agent_status.go            # AgentStatus
│   │   │   ├── manager.go                 # ConversationManager インターフェース
│   │   │   └── errors.go                  # ドメインエラー定義
│   │   └── agent/
│   │       └── mio.go                     # 修正（conversationMgr フィールド追加）
│   ├── infrastructure/
│   │   └── persistence/
│   │       └── conversation/              # 新規ディレクトリ
│   │           └── stub_manager.go        # StubConversationManager（Phase 1用）
│   └── adapter/
│       └── config/
│           └── config.go                  # 修正（ConversationConfig追加）
└── cmd/
    └── picoclaw/
        └── main.go                        # 修正（DI拡張）
```

### 1.2 修正対象ファイル（最小限）

| ファイル | 修正内容 | 影響範囲 |
|---------|---------|---------|
| `internal/domain/agent/mio.go` | `conversationMgr ConversationManager` フィールド追加 | MioAgent構造体のみ |
| `internal/adapter/config/config.go` | `Conversation ConversationConfig` フィールド追加 | Config構造体、setDefaults(), Validate() |
| `cmd/picoclaw/main.go` | ConversationConfig読込、StubConversationManager初期化、MioAgentへの注入 | buildDependencies() のみ |

**重要**: MessageOrchestrator、Router、ShiroAgent、CoderAgent は**一切変更しない**。

---

## 2. データモデル設計

### 2.1 Message（`internal/domain/conversation/message.go`）

```go
package conversation

import "time"

// Speaker は発話者の種別
type Speaker string

const (
	SpeakerUser   Speaker = "user"
	SpeakerMio    Speaker = "mio"
	SpeakerShiro  Speaker = "shiro"
	SpeakerAka    Speaker = "aka"
	SpeakerAo     Speaker = "ao"
	SpeakerGin    Speaker = "gin"
	SpeakerSystem Speaker = "system"
	SpeakerTool   Speaker = "tool"
	SpeakerMemory Speaker = "memory"
)

// Message は発話の最小単位
type Message struct {
	Speaker   Speaker                `json:"speaker"`
	Msg       string                 `json:"msg"`
	Timestamp time.Time              `json:"ts"`
	Meta      map[string]interface{} `json:"meta,omitempty"`
}

// NewMessage はMessageを生成
func NewMessage(speaker Speaker, msg string, meta map[string]interface{}) Message {
	if meta == nil {
		meta = make(map[string]interface{})
	}
	return Message{
		Speaker:   speaker,
		Msg:       msg,
		Timestamp: time.Now(),
		Meta:      meta,
	}
}
```

### 2.2 Thread（`internal/domain/conversation/thread.go`）

```go
package conversation

import "time"

// ThreadStatus はThreadの状態
type ThreadStatus string

const (
	ThreadActive   ThreadStatus = "active"
	ThreadClosed   ThreadStatus = "closed"
	ThreadArchived ThreadStatus = "archived"
)

// Thread は「話題のまとまり」（6〜8ターン相当）
type Thread struct {
	ID        int64              `json:"thread_id"`
	SessionID string             `json:"session_id"`
	Domain    string             `json:"domain"`
	Turns     []Message          `json:"turns"`
	Targets   []string           `json:"targets"`
	Cooldown  map[string]int     `json:"ct"`
	StartTime time.Time          `json:"ts_start"`
	EndTime   *time.Time         `json:"ts_end,omitempty"`
	Status    ThreadStatus       `json:"status"`
}

// NewThread は新しいThreadを生成
func NewThread(sessionID string, domain string) *Thread {
	return &Thread{
		ID:        generateThreadID(),
		SessionID: sessionID,
		Domain:    domain,
		Turns:     make([]Message, 0, 12),
		Targets:   []string{},
		Cooldown:  make(map[string]int),
		StartTime: time.Now(),
		Status:    ThreadActive,
	}
}

// AddMessage はThreadにMessageを追加（最大12件保持）
func (t *Thread) AddMessage(msg Message) {
	t.Turns = append(t.Turns, msg)
	if len(t.Turns) > 12 {
		t.Turns = t.Turns[len(t.Turns)-12:]
	}
}

// Close はThreadを終了
func (t *Thread) Close() {
	now := time.Now()
	t.EndTime = &now
	t.Status = ThreadClosed
}

// generateThreadID はユニークなThread IDを生成（簡易実装）
func generateThreadID() int64 {
	return time.Now().UnixNano()
}
```

### 2.3 SessionConversation（`internal/domain/conversation/session_conversation.go`）

```go
package conversation

import "time"

// SessionConversation はプロセス再起動や割り込み復帰に必要なまとまり
// 既存の domain/session.Session とは別のv5用Session
type SessionConversation struct {
	ID           string          `json:"session_id"`
	UserID       string          `json:"user_id"`
	History      []ThreadSummary `json:"history"`
	Agenda       string          `json:"agenda"`
	LastThreadID int64           `json:"last_thread_id"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

// NewSessionConversation は新しいSessionConversationを生成
func NewSessionConversation(id string, userID string) *SessionConversation {
	now := time.Now()
	return &SessionConversation{
		ID:        id,
		UserID:    userID,
		History:   make([]ThreadSummary, 0),
		CreatedAt: now,
		UpdatedAt: now,
	}
}
```

### 2.4 ThreadSummary（`internal/domain/conversation/thread_summary.go`）

```go
package conversation

import "time"

// ThreadSummary はThread終了時に生成される要約
type ThreadSummary struct {
	ThreadID  int64     `json:"thread_id"`
	Domain    string    `json:"domain"`
	Summary   string    `json:"summary"`
	Keywords  []string  `json:"keywords"`
	Embedding []float32 `json:"embedding,omitempty"`
	StartTime time.Time `json:"ts_start"`
	EndTime   time.Time `json:"ts_end"`
	IsNovel   bool      `json:"is_novel"`
}
```

### 2.5 AgentStatus（`internal/domain/conversation/agent_status.go`）

```go
package conversation

import "time"

// AgentStatus はAgentのタスク状態を管理
type AgentStatus struct {
	AgentName      string    `json:"agent_name"`
	IsIdle         bool      `json:"is_idle"`
	LastTaskTime   time.Time `json:"last_task"`
	CurrentTask    string    `json:"current_task"`
	ConversationOK bool      `json:"conversation_ok"`
}

// NewAgentStatus は新しいAgentStatusを生成
func NewAgentStatus(agentName string) *AgentStatus {
	return &AgentStatus{
		AgentName:      agentName,
		IsIdle:         true,
		ConversationOK: agentName == "mio", // Mioは常時参加可能
	}
}

// CanJoinConversation は会話参加可能かを判定
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

### 2.6 ドメインエラー（`internal/domain/conversation/errors.go`）

```go
package conversation

import "errors"

var (
	// ErrThreadNotFound はThreadが見つからない場合のエラー
	ErrThreadNotFound = errors.New("thread not found")

	// ErrSessionNotFound はSessionが見つからない場合のエラー
	ErrSessionNotFound = errors.New("session not found")

	// ErrInvalidThreadStatus はThread状態が不正な場合のエラー
	ErrInvalidThreadStatus = errors.New("invalid thread status")
)
```

---

## 3. ConversationManager インターフェース設計

### 3.1 インターフェース定義（`internal/domain/conversation/manager.go`）

```go
package conversation

import "context"

// ConversationManager は会話管理の抽象化インターフェース
// Phase 1ではインターフェースのみ定義、Phase 2以降で実装
type ConversationManager interface {
	// Recall は想起（過去の会話を検索してRecallPackを生成）
	Recall(ctx context.Context, sessionID string, query string, topK int) ([]Message, error)

	// Store はメッセージを記憶
	Store(ctx context.Context, sessionID string, msg Message) error

	// FlushThread はThreadを終了し、要約を中期記憶へ保存
	FlushThread(ctx context.Context, threadID int64) (*ThreadSummary, error)

	// IsNovelInformation は新規情報かを判定（類似度 < 0.8）
	IsNovelInformation(ctx context.Context, msg Message) (bool, float32, error)

	// GetActiveThread はアクティブなThreadを取得
	GetActiveThread(ctx context.Context, sessionID string) (*Thread, error)

	// CreateThread は新しいThreadを作成
	CreateThread(ctx context.Context, sessionID string, domain string) (*Thread, error)

	// GetAgentStatus はAgent状態を取得
	GetAgentStatus(ctx context.Context, agentName string) (*AgentStatus, error)

	// UpdateAgentStatus はAgent状態を更新
	UpdateAgentStatus(ctx context.Context, status *AgentStatus) error
}
```

**設計方針**:
- Phase 1では**インターフェースのみ定義**
- Phase 2以降で実装（Redis/DuckDB/VectorDB統合）
- MioAgentはこのインターフェースに依存（DIP: 依存性逆転の原則）

---

## 4. Stub実装（Phase 1用）

### 4.1 StubConversationManager（`internal/infrastructure/persistence/conversation/stub_manager.go`）

```go
package conversation

import (
	"context"
	"fmt"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/conversation"
)

// StubConversationManager はPhase 1用のスタブ実装
// 全てのメソッドはno-op（何もしない）または空を返す
type StubConversationManager struct{}

// NewStubConversationManager は新しいStubConversationManagerを生成
func NewStubConversationManager() *StubConversationManager {
	return &StubConversationManager{}
}

func (s *StubConversationManager) Recall(ctx context.Context, sessionID string, query string, topK int) ([]conversation.Message, error) {
	// Phase 1: 常に空を返す
	return []conversation.Message{}, nil
}

func (s *StubConversationManager) Store(ctx context.Context, sessionID string, msg conversation.Message) error {
	// Phase 1: no-op
	return nil
}

func (s *StubConversationManager) FlushThread(ctx context.Context, threadID int64) (*conversation.ThreadSummary, error) {
	// Phase 1: 未実装エラー
	return nil, fmt.Errorf("FlushThread not implemented in stub")
}

func (s *StubConversationManager) IsNovelInformation(ctx context.Context, msg conversation.Message) (bool, float32, error) {
	// Phase 1: 常にfalse（新規情報なし）
	return false, 1.0, nil
}

func (s *StubConversationManager) GetActiveThread(ctx context.Context, sessionID string) (*conversation.Thread, error) {
	// Phase 1: 常にnil（Threadなし）
	return nil, conversation.ErrThreadNotFound
}

func (s *StubConversationManager) CreateThread(ctx context.Context, sessionID string, domain string) (*conversation.Thread, error) {
	// Phase 1: スタブThread生成（保存はしない）
	return conversation.NewThread(sessionID, domain), nil
}

func (s *StubConversationManager) GetAgentStatus(ctx context.Context, agentName string) (*conversation.AgentStatus, error) {
	// Phase 1: デフォルトステータス返却
	return conversation.NewAgentStatus(agentName), nil
}

func (s *StubConversationManager) UpdateAgentStatus(ctx context.Context, status *conversation.AgentStatus) error {
	// Phase 1: no-op
	return nil
}
```

**Phase 1での役割**:
- ConversationManagerインターフェースを満たす最小実装
- enabled=true時でもエラーにならない（安全なno-op）
- Phase 2以降で実装を差し替え

---

## 5. Config拡張設計

### 5.1 ConversationConfig追加（`internal/adapter/config/config.go`）

```go
// Config構造体に追加
type Config struct {
	// === v3.0 既存フィールド ===
	Server   ServerConfig   `yaml:"server"`
	Ollama   OllamaConfig   `yaml:"ollama"`
	// ... 既存フィールド省略 ...

	// === v4.0 追加フィールド ===
	Distributed DistributedConfig `yaml:"distributed"`
	IdleChat    IdleChatConfig    `yaml:"idle_chat"`

	// === v5.0 追加フィールド ===
	Conversation ConversationConfig `yaml:"conversation"`
}

// ConversationConfig は会話LLMの設定
type ConversationConfig struct {
	Enabled     bool   `yaml:"enabled"`       // 会話LLM機能の有効化（デフォルト: false）
	RedisURL    string `yaml:"redis_url"`     // Redis接続先（例: "redis://localhost:6379"）
	DuckDBPath  string `yaml:"duckdb_path"`   // DuckDBファイルパス（例: "/var/lib/picoclaw/memory.duckdb"）
	VectorDBURL string `yaml:"vectordb_url"`  // VectorDB接続先（例: "http://localhost:6333" for Qdrant）
}
```

### 5.2 setDefaults()修正

```go
func (c *Config) setDefaults() {
	// ... 既存のデフォルト設定 ...

	// v5.0 Conversation デフォルト
	// enabled: false がデフォルト（明示的に有効化が必要）
	if c.Conversation.RedisURL == "" {
		c.Conversation.RedisURL = "redis://localhost:6379"
	}
	if c.Conversation.DuckDBPath == "" {
		c.Conversation.DuckDBPath = "/var/lib/picoclaw/memory.duckdb"
	}
	if c.Conversation.VectorDBURL == "" {
		c.Conversation.VectorDBURL = "http://localhost:6333"
	}
}
```

### 5.3 Validate()修正

```go
func (c *Config) Validate() error {
	// ... 既存のバリデーション ...

	// v5.0 Conversation設定検証
	if c.Conversation.Enabled {
		if c.Conversation.RedisURL == "" {
			return fmt.Errorf("conversation.redis_url is required when conversation.enabled=true")
		}
		if c.Conversation.DuckDBPath == "" {
			return fmt.Errorf("conversation.duckdb_path is required when conversation.enabled=true")
		}
		if c.Conversation.VectorDBURL == "" {
			return fmt.Errorf("conversation.vectordb_url is required when conversation.enabled=true")
		}
	}

	return nil
}
```

---

## 6. MioAgent修正設計

### 6.1 構造体修正（`internal/domain/agent/mio.go`）

```go
package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/conversation"  // 新規import
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/routing"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/task"
)

// MioAgent は Chat（会話・意思決定）を担当するエンティティ
type MioAgent struct {
	llmProvider       llm.LLMProvider
	classifier        Classifier
	ruleDictionary    RuleDictionary
	toolRunner        ToolRunner
	mcpClient         MCPClient
	conversationMgr   conversation.ConversationManager  // 新規フィールド（6番目の依存）
}

// NewMioAgent は新しいMioAgentを作成
func NewMioAgent(
	llmProvider llm.LLMProvider,
	classifier Classifier,
	ruleDictionary RuleDictionary,
	toolRunner ToolRunner,
	mcpClient MCPClient,
	conversationMgr conversation.ConversationManager,  // 新規引数（6番目）
) *MioAgent {
	return &MioAgent{
		llmProvider:     llmProvider,
		classifier:      classifier,
		ruleDictionary:  ruleDictionary,
		toolRunner:      toolRunner,
		mcpClient:       mcpClient,
		conversationMgr: conversationMgr,  // フィールドに設定
	}
}
```

**重要な設計判断**:
- conversationMgrは**nilを許容**（nil時は既存v3動作）
- Phase 1ではStubConversationManagerを注入（no-op動作）
- Phase 2以降で実装を差し替え

### 6.2 Chat()メソッド修正

```go
// Chat は会話を実行（簡易版: キーワードベース自動Web検索）
func (m *MioAgent) Chat(ctx context.Context, t task.Task) (string, error) {
	userMessage := t.UserMessage()

	// === Phase 1: 会話管理処理の条件付き実行 ===
	if m.conversationMgr != nil {
		// 想起（Recall）実行（Phase 1ではStubなので空配列が返る）
		recallMessages, err := m.conversationMgr.Recall(ctx, t.ChatID(), userMessage, 3)
		if err != nil {
			// Recallエラーは警告のみ（会話継続）
			// TODO: 構造化ログに変更（Phase 2）
			fmt.Printf("WARN: Recall failed: %v\n", err)
		}

		// RecallPackをプロンプトに追加（Phase 1では空なのでスキップされる）
		if len(recallMessages) > 0 {
			// TODO: Phase 2でプロンプト生成ロジックを実装
			// 現時点ではrecallMessagesを使わない
		}

		// ユーザーメッセージを記憶（Phase 1ではno-op）
		userMsg := conversation.NewMessage(conversation.SpeakerUser, userMessage, nil)
		if err := m.conversationMgr.Store(ctx, t.ChatID(), userMsg); err != nil {
			// Storeエラーは警告のみ
			fmt.Printf("WARN: Store failed: %v\n", err)
		}
	}

	// === 既存の会話処理（変更なし） ===
	searchKeywords := []string{"教えて", "調べて", "検索", "について", "最新", "ニュース", "とは"}
	needsSearch := false
	for _, keyword := range searchKeywords {
		if strings.Contains(userMessage, keyword) {
			needsSearch = true
			break
		}
	}

	var messages []llm.Message
	if needsSearch && m.toolRunner != nil {
		searchResult, err := m.executeWebSearch(ctx, userMessage)
		if err == nil && searchResult != "" {
			messages = append(messages, llm.Message{
				Role:    "system",
				Content: "以下はWeb検索の結果です。この情報を参考にして質問に答えてください:\n\n" + searchResult,
			})
		}
	}

	messages = append(messages, llm.Message{Role: "user", Content: userMessage})

	req := llm.GenerateRequest{
		Messages:    messages,
		MaxTokens:   512,
		Temperature: 0.7,
	}

	resp, err := m.llmProvider.Generate(ctx, req)
	if err != nil {
		return "", err
	}

	response := resp.Content

	// === Phase 1: 応答を記憶（Phase 1ではno-op） ===
	if m.conversationMgr != nil {
		mioMsg := conversation.NewMessage(conversation.SpeakerMio, response, nil)
		if err := m.conversationMgr.Store(ctx, t.ChatID(), mioMsg); err != nil {
			// Storeエラーは警告のみ
			fmt.Printf("WARN: Store (response) failed: %v\n", err)
		}
	}

	return response, nil
}
```

**修正のポイント**:
1. `if m.conversationMgr != nil` で条件付き実行（nil安全性）
2. Phase 1ではStubなのでRecallは空、Storeはno-op
3. エラーハンドリングは警告のみ（会話継続を優先）
4. 既存の会話処理（Web検索、LLM呼び出し）は**一切変更なし**

---

## 7. main.go修正設計

### 7.1 buildDependencies()修正（`cmd/picoclaw/main.go`）

```go
func buildDependencies(cfg *config.Config) *Dependencies {
	// ... 既存のLLM Provider、Routing Components、Tool Runner、MCP Client初期化 ...

	// === v5.0 ConversationManager初期化 ===
	var conversationMgr conversation.ConversationManager
	if cfg.Conversation.Enabled {
		// Phase 1: Stub実装を使用
		conversationMgr = conversationpersistence.NewStubConversationManager()
		log.Printf("Conversation LLM enabled (Phase 1: Stub implementation)")
		// Phase 2以降:
		// conversationMgr = conversationpersistence.NewRealConversationManager(
		//     cfg.Conversation.RedisURL,
		//     cfg.Conversation.DuckDBPath,
		//     cfg.Conversation.VectorDBURL,
		// )
	} else {
		conversationMgr = nil
		log.Printf("Conversation LLM disabled (v3/v4 mode)")
	}

	// === 5. Agents ===
	mioAgent := agent.NewMioAgent(
		ollamaProvider,
		classifier,
		ruleDictionary,
		chatToolRunner,
		mcpClient,
		conversationMgr,  // 6番目の引数として注入
	)
	shiroAgent := agent.NewShiroAgent(ollamaProvider, workerToolRunner, mcpClient)

	// ... 残りの既存コード（変更なし） ...
}
```

**追加import**:
```go
import (
	// ... 既存のimport ...
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/conversation"
	conversationpersistence "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation"
)
```

**動作**:
- `conversation.enabled: false`（デフォルト）: conversationMgr = nil → v3/v4動作
- `conversation.enabled: true`: conversationMgr = StubConversationManager → no-op動作（v3/v4と同等）

---

## 8. テスト戦略

### 8.1 既存テストの保護

**原則**: 既存のv3/v4テストが**すべて通る**ことを確認

**実行コマンド**:
```bash
go test ./internal/domain/agent/... -v
go test ./internal/application/orchestrator/... -v
go test ./internal/adapter/config/... -v
```

**検証項目**:
- MioAgentのDecideAction()テスト: 既存動作を変更していないので通るはず
- MioAgentのChat()テスト: conversationMgr=nilで従来通り動作
- MessageOrchestratorテスト: MioAgent以外は変更なし

### 8.2 新規テスト追加（Phase 1範囲）

#### 8.2.1 データモデルテスト

```go
// internal/domain/conversation/message_test.go
func TestNewMessage(t *testing.T) {
	msg := NewMessage(SpeakerUser, "こんにちは", nil)
	assert.Equal(t, SpeakerUser, msg.Speaker)
	assert.Equal(t, "こんにちは", msg.Msg)
	assert.NotNil(t, msg.Timestamp)
}

// internal/domain/conversation/thread_test.go
func TestThread_AddMessage(t *testing.T) {
	thread := NewThread("sess_001", "general")
	for i := 0; i < 15; i++ {
		msg := NewMessage(SpeakerUser, fmt.Sprintf("msg %d", i), nil)
		thread.AddMessage(msg)
	}
	// 最大12件保持
	assert.Equal(t, 12, len(thread.Turns))
}

// internal/domain/conversation/agent_status_test.go
func TestAgentStatus_CanJoinConversation(t *testing.T) {
	mioStatus := NewAgentStatus("mio")
	assert.True(t, mioStatus.CanJoinConversation()) // Mioは常時参加可能

	shiroStatus := NewAgentStatus("shiro")
	shiroStatus.IsIdle = true
	shiroStatus.CurrentTask = ""
	assert.True(t, shiroStatus.CanJoinConversation()) // Shiroはidle時のみ

	shiroStatus.IsIdle = false
	assert.False(t, shiroStatus.CanJoinConversation()) // busy時はNG
}
```

#### 8.2.2 Stub実装テスト

```go
// internal/infrastructure/persistence/conversation/stub_manager_test.go
func TestStubConversationManager_Recall(t *testing.T) {
	stub := NewStubConversationManager()
	messages, err := stub.Recall(context.Background(), "sess_001", "query", 3)
	assert.NoError(t, err)
	assert.Empty(t, messages) // Phase 1では常に空
}

func TestStubConversationManager_Store(t *testing.T) {
	stub := NewStubConversationManager()
	msg := conversation.NewMessage(conversation.SpeakerUser, "test", nil)
	err := stub.Store(context.Background(), "sess_001", msg)
	assert.NoError(t, err) // no-op成功
}
```

#### 8.2.3 Config読込テスト

```go
// internal/adapter/config/config_test.go
func TestConfig_ConversationDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.setDefaults()
	
	assert.False(t, cfg.Conversation.Enabled) // デフォルトfalse
	assert.Equal(t, "redis://localhost:6379", cfg.Conversation.RedisURL)
	assert.Equal(t, "/var/lib/picoclaw/memory.duckdb", cfg.Conversation.DuckDBPath)
	assert.Equal(t, "http://localhost:6333", cfg.Conversation.VectorDBURL)
}

func TestConfig_ConversationValidation(t *testing.T) {
	cfg := &Config{
		Server: ServerConfig{Port: 8080},
		Ollama: OllamaConfig{BaseURL: "http://localhost:11434", Model: "chat-v1"},
		Session: SessionConfig{StorageDir: "/tmp/sessions"},
		Conversation: ConversationConfig{
			Enabled:  true,
			RedisURL: "", // 空（エラーになるべき）
		},
	}
	
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "conversation.redis_url is required")
}
```

#### 8.2.4 MioAgent with ConversationMgr テスト

```go
// internal/domain/agent/mio_test.go
func TestMioAgent_ChatWithConversationManager(t *testing.T) {
	// Stub ConversationManager注入
	stub := conversationpersistence.NewStubConversationManager()
	mio := NewMioAgent(
		&mockLLMProvider{},
		&mockClassifier{},
		&mockRuleDictionary{},
		&mockToolRunner{},
		&mockMCPClient{},
		stub, // conversationMgr
	)

	task := task.NewTask(task.NewJobID(), "こんにちは", "test", "test")
	response, err := mio.Chat(context.Background(), task)
	
	assert.NoError(t, err)
	assert.NotEmpty(t, response)
	// Stubなのでエラーにならず、従来通りの応答が返る
}

func TestMioAgent_ChatWithNilConversationManager(t *testing.T) {
	// conversationMgr = nil（v3/v4モード）
	mio := NewMioAgent(
		&mockLLMProvider{},
		&mockClassifier{},
		&mockRuleDictionary{},
		&mockToolRunner{},
		&mockMCPClient{},
		nil, // conversationMgr
	)

	task := task.NewTask(task.NewJobID(), "こんにちは", "test", "test")
	response, err := mio.Chat(context.Background(), task)
	
	assert.NoError(t, err)
	assert.NotEmpty(t, response)
	// nil時は従来通りの動作（v3/v4互換）
}
```

### 8.3 統合テスト（E2E）

**Phase 1範囲**: 設定ファイルでconversation.enabled切り替えテスト

```bash
# conversation.enabled: false（デフォルト）
$ go run cmd/picoclaw/main.go
# → "Conversation LLM disabled (v3/v4 mode)" ログ確認

# conversation.enabled: true（Phase 1: Stub）
$ cat > config.test.yaml <<EOF
server:
  port: 18790
ollama:
  base_url: "http://localhost:11434"
  model: "chat-v1"
session:
  storage_dir: "/tmp/sessions"
conversation:
  enabled: true
  redis_url: "redis://localhost:6379"
  duckdb_path: "/tmp/memory.duckdb"
  vectordb_url: "http://localhost:6333"
EOF

$ PICOCLAW_CONFIG=config.test.yaml go run cmd/picoclaw/main.go
# → "Conversation LLM enabled (Phase 1: Stub implementation)" ログ確認
```

**検証項目**:
- 起動時のログで有効/無効が正しく表示される
- enabled=trueでもエラーにならない（Stub動作）
- 既存のLINE Webhook処理が正常動作

---

## 9. 実装順序（Phase 1の6ステップ）

### Step 1: データモデル定義（1日目）

**作業内容**:
1. `internal/domain/conversation/` ディレクトリ作成
2. 以下のファイルを作成:
   - `message.go`（Message, Speaker）
   - `thread.go`（Thread, ThreadStatus）
   - `session_conversation.go`（SessionConversation）
   - `thread_summary.go`（ThreadSummary）
   - `agent_status.go`（AgentStatus）
   - `errors.go`（ドメインエラー）

**確認**:
```bash
go build ./internal/domain/conversation/...
```

### Step 2: ConversationManager インターフェース定義（1日目）

**作業内容**:
1. `internal/domain/conversation/manager.go` 作成
2. ConversationManagerインターフェース定義

**確認**:
```bash
go build ./internal/domain/conversation/...
```

### Step 3: Stub実装（1日目）

**作業内容**:
1. `internal/infrastructure/persistence/conversation/` ディレクトリ作成
2. `stub_manager.go` 作成（StubConversationManager）

**確認**:
```bash
go build ./internal/infrastructure/persistence/conversation/...
```

### Step 4: Config拡張（2日目）

**作業内容**:
1. `internal/adapter/config/config.go` 修正:
   - ConversationConfig構造体追加
   - setDefaults()修正
   - Validate()修正

**確認**:
```bash
go test ./internal/adapter/config/... -v
```

### Step 5: MioAgent修正（2日目）

**作業内容**:
1. `internal/domain/agent/mio.go` 修正:
   - conversationMgrフィールド追加
   - NewMioAgent()引数追加
   - Chat()メソッド修正（条件付き会話管理処理）

**確認**:
```bash
go test ./internal/domain/agent/... -v
```

### Step 6: main.go修正（3日目）

**作業内容**:
1. `cmd/picoclaw/main.go` 修正:
   - ConversationManager初期化（enabled判定）
   - MioAgentへの注入

**確認**:
```bash
go build ./cmd/picoclaw/
./picoclaw  # 起動確認
```

### Step 7: テスト追加（4〜5日目）

**作業内容**:
1. データモデルテスト作成
2. Stub実装テスト作成
3. Configテスト追加
4. MioAgentテスト追加
5. E2Eテスト実行

**確認**:
```bash
go test ./... -v
```

---

## 10. リスク管理

### 10.1 既存機能への影響

| リスク | 対策 | 検証方法 |
|-------|------|---------|
| MioAgent破壊 | conversationMgr=nilで従来動作を保証 | 既存テスト全通過 |
| MessageOrchestrator破壊 | MessageOrchestratorは変更しない | 既存テスト全通過 |
| Config読込エラー | Validateで厳密チェック、デフォルト値設定 | Configテスト |
| 起動エラー | enabled=falseがデフォルト、enabled=true時もStubで安全 | 起動テスト |

### 10.2 Phase 1実装の制約

**Phase 1で実装しないもの**:
- Redis/DuckDB/VectorDB の実ストア（Phase 2以降）
- LangGraph統合（Phase 2）
- Thread開始/終了条件（Phase 3）
- Embedding生成（Phase 3）
- KB統合（Phase 4）

**Phase 1の目的**:
- データモデルとインターフェースの確定
- DI構造の準備
- 既存コードへの影響範囲の最小化
- Phase 2以降のスムーズな実装のための基盤整備

---

## 11. Phase 1完了条件

### 11.1 必須条件

- [ ] データモデル（Message, Thread, SessionConversation, ThreadSummary, AgentStatus）が定義され、ビルド成功
- [ ] ConversationManagerインターフェースが定義され、ビルド成功
- [ ] StubConversationManagerが実装され、全メソッドがno-op/空を返す
- [ ] ConversationConfigが追加され、setDefaults()/Validate()が動作
- [ ] MioAgentにconversationMgrフィールドが追加され、NewMioAgent()が6引数を受け取る
- [ ] main.goでConversationManager初期化・注入が実装され、enabled切り替えが動作
- [ ] 既存の全テストが通過（MioAgent, MessageOrchestrator, Config）
- [ ] 新規テストが追加され、全て通過（データモデル、Stub、Config、MioAgent）
- [ ] E2Eテストでconversation.enabled=false/true両方が動作確認

### 11.2 成果物チェックリスト

#### コードファイル

- [ ] `/home/nyukimi/picoclaw_multiLLM/internal/domain/conversation/message.go`
- [ ] `/home/nyukimi/picoclaw_multiLLM/internal/domain/conversation/thread.go`
- [ ] `/home/nyukimi/picoclaw_multiLLM/internal/domain/conversation/session_conversation.go`
- [ ] `/home/nyukimi/picoclaw_multiLLM/internal/domain/conversation/thread_summary.go`
- [ ] `/home/nyukimi/picoclaw_multiLLM/internal/domain/conversation/agent_status.go`
- [ ] `/home/nyukimi/picoclaw_multiLLM/internal/domain/conversation/manager.go`
- [ ] `/home/nyukimi/picoclaw_multiLLM/internal/domain/conversation/errors.go`
- [ ] `/home/nyukimi/picoclaw_multiLLM/internal/infrastructure/persistence/conversation/stub_manager.go`
- [ ] `/home/nyukimi/picoclaw_multiLLM/internal/domain/agent/mio.go`（修正）
- [ ] `/home/nyukimi/picoclaw_multiLLM/internal/adapter/config/config.go`（修正）
- [ ] `/home/nyukimi/picoclaw_multiLLM/cmd/picoclaw/main.go`（修正）

#### テストファイル

- [ ] `/home/nyukimi/picoclaw_multiLLM/internal/domain/conversation/message_test.go`
- [ ] `/home/nyukimi/picoclaw_multiLLM/internal/domain/conversation/thread_test.go`
- [ ] `/home/nyukimi/picoclaw_multiLLM/internal/domain/conversation/agent_status_test.go`
- [ ] `/home/nyukimi/picoclaw_multiLLM/internal/infrastructure/persistence/conversation/stub_manager_test.go`
- [ ] `/home/nyukimi/picoclaw_multiLLM/internal/adapter/config/config_test.go`（追加テスト）
- [ ] `/home/nyukimi/picoclaw_multiLLM/internal/domain/agent/mio_test.go`（追加テスト）

#### ドキュメント

- [ ] `.serena/memories/conversation_llm_v5_status.md`（Phase 1完了を記録）

---

## 12. Phase 2への引き継ぎ事項

Phase 1完了後、Phase 2（LangGraph統合）で実装すべき項目:

1. **実ストア実装**:
   - RedisStore（中期記憶）
   - DuckDBStore（中期記憶warm）
   - VectorDBStore（長期記憶・KB）

2. **RealConversationManager実装**:
   - StubConversationManagerをRealConversationManagerに差し替え
   - Recall/Store/FlushThreadの実装
   - IsNovelInformationの実装（Embedding + 類似検索）

3. **LangGraph統合**:
   - ConversationOrchestrator実装
   - Router/Mio/Shiro/Memory Node実装
   - ConversationState定義

4. **Thread管理ロジック**:
   - Thread開始/終了条件実装
   - Thread要約生成（LLM呼び出し）

---

## 13. 設計判断の記録

### 13.1 なぜconversationMgrをnilable（nilを許容）にしたか？

**理由**:
- v3/v4との完全互換性を保証
- enabled=false時に不要なオーバーヘッドを回避
- Phase 1でStub実装を使う際の安全性確保

**代替案**:
- 常にStub/Real実装を注入（nilを禁止）
- → 却下理由: enabled=false時にも不要なオブジェクト生成

### 13.2 なぜStub実装を用意したか？

**理由**:
- Phase 1で基盤整備のみ完了し、Phase 2以降で実装を段階的に追加
- enabled=trueでもエラーにならない（安全なno-op）
- インターフェース設計の妥当性を早期検証

**代替案**:
- Phase 1で実ストア実装まで完了
- → 却下理由: Phase 1が長期化、リスク増大

### 13.3 なぜSessionConversationを新規作成したか？

**理由**:
- 既存のdomain/session.Sessionはv3/v4の履歴管理用
- v5.0の会話LLMでは異なるデータ構造（ThreadSummaryリスト）が必要
- 既存Sessionを変更すると影響範囲が大きい

**代替案**:
- 既存Sessionを拡張
- → 却下理由: 既存コードへの影響が大きい、下位互換性リスク

### 13.4 なぜMioAgentのみ修正するか？

**理由**:
- 会話LLMは**Chatのサブシステム**（仕様書の最重要設計原則）
- Worker/Coderは会話LLMに参加しない（従来通り実行のみ）
- MessageOrchestratorは変更禁止（コアシステム）

**代替案**:
- MessageOrchestratorに会話管理を統合
- → 却下理由: コアシステムの変更は禁止、影響範囲が大きい

---

## 14. 次のステップ

Phase 1完了後の作業:

1. **Phase 2開始前の準備**:
   - Redis/DuckDB/VectorDB環境構築
   - LangGraph依存インストール
   - Embedding生成ライブラリ選定

2. **Phase 2設計**:
   - RealConversationManager設計
   - LangGraphグラフ設計
   - Thread管理フロー設計

3. **Phase 2実装**:
   - 段階的実装プラン策定
   - 週次レビュー

---

## 15. まとめ

Phase 1の目標は、**会話LLMの基盤を非破壊的に追加**し、既存のv3/v4動作を一切変更せずに、将来の段階的実装を可能にすることです。

**Phase 1で達成すること**:
- ✅ データモデル定義（ドメイン層）
- ✅ ConversationManagerインターフェース定義
- ✅ Stub実装（安全なno-op）
- ✅ Config拡張（conversation セクション）
- ✅ MioAgentへのDI準備（6番目の依存）
- ✅ 既存テスト全通過
- ✅ 新規テスト追加

**Phase 1で達成しないこと**:
- ❌ 実ストア実装（Phase 2以降）
- ❌ LangGraph統合（Phase 2）
- ❌ Thread管理ロジック（Phase 3）
- ❌ KB統合（Phase 4）

この設計に従って実装することで、**リスクを最小化**し、**段階的な機能追加**を実現できます。

---

**最終更新**: 2026-03-04
**ステータス**: Phase 1設計完了、実装開始準備完了
