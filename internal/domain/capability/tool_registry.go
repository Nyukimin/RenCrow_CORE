package capability

import (
	"context"
	"errors"
	"time"
)

// ErrToolRegistryEntryNotFound is returned by exact ToolRegistry reads when
// no entry exists. It lets internal recall routes distinguish an empty result
// from a storage failure without parsing error text.
var ErrToolRegistryEntryNotFound = errors.New("tool registry entry not found")

// ToolSource はツールの生成元
type ToolSource string

const (
	ToolSourceBuiltin        ToolSource = "builtin"
	ToolSourceShiroGenerated ToolSource = "shiro-generated"
)

// ToolEntry はレジストリに保存されるツール定義
type ToolEntry struct {
	Name        string     // ツール名（ユニーク）
	Description string     // LLM に渡す説明文
	SchemaJSON  string     // llm.ToolDefinition の JSON 文字列
	Platforms   []string   // 対応 OS: ["linux"], ["windows"], ["linux", "windows"]
	Source      ToolSource // builtin / shiro-generated
	CreatedAt   time.Time
	CreatedBy   string // "shiro" / "builtin"
}

// ToolRegistryRequestReceipt is the durable audit binding for an Owner
// registration request. The request payload, actor and tool name are supplied
// by the trusted runtime boundary; they are never read from a Tool payload.
type ToolRegistryRequestReceipt struct {
	RequestID   string
	ActorID     string
	PayloadHash string
	ToolName    string
	CreatedAt   time.Time
}

// ToolRegistryRegistrationResult describes one receipt-aware registration.
// RequestReplay is true only when the exact request receipt was already
// persisted. SemanticDedupe is true when a new request matched an existing
// identical ToolEntry and received its own receipt.
type ToolRegistryRegistrationResult struct {
	Receipt        ToolRegistryRequestReceipt
	RequestReplay  bool
	SemanticDedupe bool
}

// ToolRegistryReceiptOwner is the optional durable Owner extension used by
// CORE data.write/data.recall routes. Keeping it separate preserves the
// existing ToolRegistry interface and its legacy callers/mocks.
type ToolRegistryReceiptOwner interface {
	RegisterWithReceipt(ctx context.Context, entry ToolEntry, requestID, actorID, payloadHash string) (ToolRegistryRegistrationResult, error)
	FindRequestReceipt(ctx context.Context, requestID string) (ToolRegistryRequestReceipt, bool, error)
}

// ToolRegistry はツールの永続管理インターフェース
type ToolRegistry interface {
	// Register はツールを登録または更新する（冪等）
	Register(ctx context.Context, entry ToolEntry) error

	// ListForPlatform は指定 OS で使用可能なツールを返す
	ListForPlatform(ctx context.Context, platform string) ([]ToolEntry, error)

	// Get は名前でツールを取得する
	Get(ctx context.Context, name string) (ToolEntry, error)

	// Close はデータベース接続を閉じる
	Close() error
}
