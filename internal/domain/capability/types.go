package capability

// NodeCapabilities はノードが起動時に自己検出した実行能力の全体像
type NodeCapabilities struct {
	NodeID   string           // ノード識別子（hostname）
	Platform PlatformInfo     // OS・アーキテクチャ
	Memory   MemoryInfo       // 利用可能メモリ
	LLMs     []LLMCapability  // 利用可能な LLM 一覧
	Tools    []ToolCapability // 利用可能なツール一覧（Phase 2 で ToolRegistry 統合後に追加）
}

// PlatformInfo は OS・アーキテクチャ情報
type PlatformInfo struct {
	OS   string // "linux" / "windows" / "darwin"
	Arch string // "amd64" / "arm64"
}

// MemoryInfo はメモリ使用量情報
type MemoryInfo struct {
	TotalMB     uint64
	AvailableMB uint64
}

// LLMCapability は1つの LLM の能力情報
type LLMCapability struct {
	ProviderName string // "ollama" / "claude" / "openai" / "deepseek"
	ModelName    string // 例: "gemma3:4b"
	MaxContext   int    // コンテキスト長（トークン数）
	MaxMemoryMB  uint64 // このモデルの推定メモリ使用量
	Available    bool   // 実際に疎通確認できたか
	Quality      int    // 品質ランク（1=低 〜 5=高）
}

// ToolCapability は1つのツールの能力情報（Phase 2 で使用）
type ToolCapability struct {
	Name      string   // ツール名
	Platforms []string // 対応 OS: ["linux"], ["windows"], ["linux", "windows"]
	Source    string   // "builtin" / "shiro-generated"
}
