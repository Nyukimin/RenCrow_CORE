package patch

// CommandResult は単一コマンドの実行結果
type CommandResult struct {
	Command PatchCommand // 実行したコマンド
	Success bool         // 成功したか
	Output  string       // 出力
	Error   string       // エラーメッセージ（失敗時）
}

// PatchExecutionResult はパッチ実行の結果を表す値オブジェクト
type PatchExecutionResult struct {
	Success       bool            // 全体の成功/失敗
	ExecutedCmds  int             // 実行したコマンド数
	FailedCmds    int             // 失敗したコマンド数
	Results       []CommandResult // 各コマンドの結果
	Summary       string          // 実行結果のサマリ
	GitCommit     string          // auto-commit時のコミットハッシュ
	FailureKind   string          // 失敗種別
	FailureReason string          // 失敗理由の要約
	Retryable     bool            // 再試行対象か
	FailedIndex   int             // 最初に失敗したコマンドインデックス
}

// NewPatchExecutionResult は新しいPatchExecutionResultを作成
func NewPatchExecutionResult() *PatchExecutionResult {
	return &PatchExecutionResult{
		Success:      true,
		ExecutedCmds: 0,
		FailedCmds:   0,
		Results:      make([]CommandResult, 0),
		Summary:      "",
		GitCommit:    "",
		FailedIndex:  -1,
	}
}

// AddResult はコマンド結果を追加
func (r *PatchExecutionResult) AddResult(result CommandResult) {
	r.Results = append(r.Results, result)
	r.ExecutedCmds++
	if !result.Success {
		r.FailedCmds++
		r.Success = false
		if r.FailedIndex < 0 {
			r.FailedIndex = len(r.Results) - 1
		}
	}
}

// WithSummary はサマリを設定
func (r *PatchExecutionResult) WithSummary(summary string) *PatchExecutionResult {
	r.Summary = summary
	return r
}

// WithGitCommit はGitコミットハッシュを設定
func (r *PatchExecutionResult) WithGitCommit(commitHash string) *PatchExecutionResult {
	r.GitCommit = commitHash
	return r
}

// WithFailureMetadata は失敗分類メタデータを設定
func (r *PatchExecutionResult) WithFailureMetadata(kind, reason string, retryable bool) *PatchExecutionResult {
	r.FailureKind = kind
	r.FailureReason = reason
	r.Retryable = retryable
	return r
}

// HasFailures は失敗があるかを判定
func (r *PatchExecutionResult) HasFailures() bool {
	return r.FailedCmds > 0
}

// SuccessRate は成功率を返す（0.0 - 1.0）
func (r *PatchExecutionResult) SuccessRate() float64 {
	if r.ExecutedCmds == 0 {
		return 0.0
	}
	return float64(r.ExecutedCmds-r.FailedCmds) / float64(r.ExecutedCmds)
}
