package tool

// ToolMetadata はツールの識別情報と能力を記述する（TOOL_CONTRACT 4.1）
type ToolMetadata struct {
	ToolID           string         `json:"tool_id"`
	Version          string         `json:"version"`
	Category         string         `json:"category"`                    // query, mutation, admin
	DryRun           bool           `json:"dry_run"`
	Deprecated       bool           `json:"deprecated"`
	ReplacedBy       string         `json:"replaced_by,omitempty"`
	Invariants       []string       `json:"invariants,omitempty"`
	Description      string         `json:"description,omitempty"`       // ツール説明（tool calling用）
	Parameters       map[string]any `json:"parameters,omitempty"`        // JSON Schema（tool calling用）
}
