package patch

// Type はコマンドタイプを表す型
type Type string

const (
	TypeFileEdit      Type = "file_edit"
	TypeShellCommand  Type = "shell_command"
	TypeGitOperation  Type = "git_operation"
)

// Action はコマンドアクションを表す型
type Action string

// ファイル編集アクション
const (
	ActionCreate Action = "create" // 新規ファイル作成
	ActionUpdate Action = "update" // 既存ファイル更新
	ActionDelete Action = "delete" // ファイル削除
	ActionAppend Action = "append" // ファイル末尾追記
	ActionMkdir  Action = "mkdir"  // ディレクトリ作成
	ActionRename Action = "rename" // ファイル/ディレクトリリネーム
	ActionCopy   Action = "copy"   // ファイル/ディレクトリコピー
)

// シェルコマンドアクション
const (
	ActionRun Action = "run" // コマンド実行
)

// Git操作アクション
const (
	ActionAdd      Action = "add"      // git add
	ActionCommit   Action = "commit"   // git commit
	ActionReset    Action = "reset"    // git reset
	ActionCheckout Action = "checkout" // git checkout
)

// PatchCommand は単一のパッチコマンドを表す値オブジェクト
type PatchCommand struct {
	Type     Type              // コマンドタイプ
	Action   Action            // アクション
	Target   string            // ターゲット（ファイルパスまたはコマンド）
	Content  string            // 内容（ファイル内容またはコマンド引数）
	Metadata map[string]string // メタデータ（オプション）
}

// NewPatchCommand は新しいPatchCommandを作成
func NewPatchCommand(cmdType Type, action Action, target, content string) PatchCommand {
	return PatchCommand{
		Type:     cmdType,
		Action:   action,
		Target:   target,
		Content:  content,
		Metadata: make(map[string]string),
	}
}

// WithMetadata はメタデータを追加した新しいPatchCommandを返す
func (c PatchCommand) WithMetadata(key, value string) PatchCommand {
	// 新しいmapを作成（イミュータブル）
	newMetadata := make(map[string]string)
	for k, v := range c.Metadata {
		newMetadata[k] = v
	}
	newMetadata[key] = value
	c.Metadata = newMetadata
	return c
}

// GetMetadata はメタデータを取得
func (c PatchCommand) GetMetadata(key string) (string, bool) {
	if c.Metadata == nil {
		return "", false
	}
	value, ok := c.Metadata[key]
	return value, ok
}

// IsFileEdit はファイル編集コマンドかを判定
func (c PatchCommand) IsFileEdit() bool {
	return c.Type == TypeFileEdit
}

// IsShellCommand はシェルコマンドかを判定
func (c PatchCommand) IsShellCommand() bool {
	return c.Type == TypeShellCommand
}

// IsGitOperation はGit操作かを判定
func (c PatchCommand) IsGitOperation() bool {
	return c.Type == TypeGitOperation
}
