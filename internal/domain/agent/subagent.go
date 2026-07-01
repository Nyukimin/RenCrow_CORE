package agent

import "context"

// SubagentTask はサブエージェントに渡すタスク
type SubagentTask struct {
	AgentName    string // サブエージェント名（ログ・識別用）
	Instruction  string // タスク指示文
	SystemPrompt string // システムプロンプト（空の場合はデフォルト使用）
}

// SubagentResult はサブエージェント実行結果
type SubagentResult struct {
	AgentName  string // 実行したサブエージェント名
	Output     string // 最終出力テキスト
	Iterations int    // ReActループの反復回数
}

// SubagentManager はサブエージェントタスクの実行を管理するインターフェース
// OPS ルートで ReActLoop を使ってツールを自律的に選択・実行する
type SubagentManager interface {
	RunSync(ctx context.Context, task SubagentTask) (SubagentResult, error)
}
