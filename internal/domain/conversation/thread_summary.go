package conversation

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

const ThreadSummaryReceiptSchemaVersion = "conversation.thread_summary_receipt.v1"

// ThreadSummaryReceipt binds a persisted summary to the exact source thread
// evidence. Roles are stored in the receipt table, not inferred from LLM text.
type ThreadSummaryReceipt struct {
	SchemaVersion   string    `json:"schema_version"`
	GenerationMode  string    `json:"generation_mode"`
	Provider        string    `json:"provider"`
	FailureCode     string    `json:"failure_code"`
	EvidenceSHA256  string    `json:"evidence_sha256"`
	SourceTurnCount int       `json:"source_turn_count"`
	Roles           []string  `json:"roles"`
	CreatedAt       time.Time `json:"created_at"`
}

// ValidateForWrite enforces the receipt contract for a new archive write.
func (r *ThreadSummaryReceipt) ValidateForWrite() error {
	if r == nil {
		return fmt.Errorf("thread summary receipt is required")
	}
	if r.SchemaVersion != ThreadSummaryReceiptSchemaVersion {
		return fmt.Errorf("unsupported thread summary receipt schema")
	}
	if r.GenerationMode == ThreadSummaryGenerationLegacyUnverified {
		return fmt.Errorf("legacy thread summary receipt cannot be written")
	}
	if strings.TrimSpace(r.Provider) == "" {
		return fmt.Errorf("thread summary receipt provider is required")
	}
	switch r.GenerationMode {
	case ThreadSummaryGenerationLLM:
		if r.FailureCode != "" {
			return fmt.Errorf("invalid llm thread summary receipt")
		}
	case ThreadSummaryGenerationDeterministicFallback:
		if r.FailureCode != ThreadSummaryFailureUnavailable &&
			r.FailureCode != ThreadSummaryFailureInvalid &&
			r.FailureCode != ThreadSummaryFailureNotConfigured {
			return fmt.Errorf("invalid fallback failure code")
		}
	default:
		return fmt.Errorf("invalid thread summary generation mode")
	}
	if len(r.EvidenceSHA256) != hex.EncodedLen(32) {
		return fmt.Errorf("invalid evidence sha256")
	}
	if _, err := hex.DecodeString(r.EvidenceSHA256); err != nil {
		return fmt.Errorf("invalid evidence sha256")
	}
	if r.SourceTurnCount <= 0 {
		return fmt.Errorf("source turn count must be > 0")
	}
	seenRoles := make(map[string]struct{}, len(r.Roles))
	for _, role := range r.Roles {
		role = strings.TrimSpace(role)
		if role == "" || strings.ContainsAny(role, "\r\n\x00") {
			return fmt.Errorf("invalid role")
		}
		if _, ok := seenRoles[role]; ok {
			return fmt.Errorf("duplicate role")
		}
		seenRoles[role] = struct{}{}
	}
	if r.CreatedAt.IsZero() {
		return fmt.Errorf("created_at is required")
	}
	return nil
}

// ThreadSummary はThread終了時に生成される要約
type ThreadSummary struct {
	ThreadID  int64                 `json:"thread_id"`
	SessionID string                `json:"session_id"`
	Domain    string                `json:"domain"`
	Summary   string                `json:"summary"`
	Keywords  []string              `json:"keywords"`
	Roles     []string              `json:"roles,omitempty"`
	Receipt   *ThreadSummaryReceipt `json:"receipt,omitempty"`
	Embedding []float32             `json:"embedding,omitempty"`
	StartTime time.Time             `json:"ts_start"`
	EndTime   time.Time             `json:"ts_end"`
	IsNovel   bool                  `json:"is_novel"`
	Score     float32               `json:"score,omitempty"` // VectorDB類似度スコア（検索結果のみ）
}
