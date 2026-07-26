package aiworkflow

import (
	"path/filepath"
	"strings"
)

// broadRootNames はシステム全体または広範囲を指す既知のルートを列挙する
var broadRootNames = []string{
	"/", "/home", "/tmp", "/var", "/etc", "/usr", "/System", "/Applications",
}

// isBroadOrSystemRoot は、対象パスがシステム全体または広範囲を指すルートかを判定する
//
// filepath.Clean 済みの絶対パスを前提とする。Windows では "/" が "C:\" のように
// ドライブ付きへ解決されるため、POSIX 名の文字列比較だけではドライブルートを
// 取りこぼす。ドライブルート（"C:\"）と UNC 共有ルート（"\\\\host\\share"）も
// 広範囲として拒否する。
func isBroadOrSystemRoot(clean string) bool {
	slash := filepath.ToSlash(clean)
	trimmed := strings.TrimSuffix(slash, "/")
	for _, name := range broadRootNames {
		if slash == name || (trimmed != "" && trimmed == name) {
			return true
		}
	}
	// ドライブルート（C:/ もしくは C:）
	if len(slash) >= 2 && slash[1] == ':' {
		drive := slash[0]
		isLetter := (drive >= 'a' && drive <= 'z') || (drive >= 'A' && drive <= 'Z')
		if isLetter && (len(slash) == 2 || slash == string(drive)+":/") {
			return true
		}
	}
	// UNC 共有ルート（//host/share）
	if strings.HasPrefix(slash, "//") {
		parts := strings.Split(strings.Trim(slash, "/"), "/")
		if len(parts) <= 2 {
			return true
		}
	}
	return false
}
