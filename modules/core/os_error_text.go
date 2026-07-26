package core

import "strings"

// OS ごとに異なるエラー文言を一箇所へ集約する。
//
// shell やネットワークスタックが返す文言は OS 依存であり、Unix の文言だけを
// 見る判定は Windows で機能しない。errors.Is で判定できない（テキストしか
// 手元にない）経路のために、照合する語句をここで持つ。
// 新しい OS 固有文言が判明した場合はこのファイルだけを更新する。
var (
	missingCommandPhrases = []string{
		// Unix 系
		"command not found",
		"exit status 127",
		// Windows cmd.exe
		"is not recognized as an internal or external command",
		"exit status 9009",
		// Windows PowerShell
		"is not recognized as the name of a cmdlet",
		// Go の os/exec
		"executable file not found",
	}

	transientNetworkPhrases = []string{
		// Unix 系
		"connection refused",
		"connection reset by peer",
		"broken pipe",
		// Windows
		"actively refused",
		"forcibly closed by the remote host",
		// Go の net/http（OS 非依存）
		"client.timeout exceeded",
		"context deadline exceeded",
		"i/o timeout",
	}

	missingPathPhrases = []string{
		// Unix 系
		"no such file or directory",
		// Windows
		"cannot find the file specified",
		"cannot find the path specified",
	}
)

// IsMissingCommandText は文言が「コマンドが見つからない」ことを示すかを返す
func IsMissingCommandText(text string) bool {
	return containsAnyFold(text, missingCommandPhrases)
}

// IsTransientNetworkText は文言が一時的なネットワーク障害を示すかを返す
func IsTransientNetworkText(text string) bool {
	return containsAnyFold(text, transientNetworkPhrases)
}

// IsMissingPathText は文言が「パスが見つからない」ことを示すかを返す
func IsMissingPathText(text string) bool {
	return containsAnyFold(text, missingPathPhrases)
}

func containsAnyFold(text string, phrases []string) bool {
	lowered := strings.ToLower(strings.TrimSpace(text))
	if lowered == "" {
		return false
	}
	for _, phrase := range phrases {
		if strings.Contains(lowered, phrase) {
			return true
		}
	}
	return false
}
