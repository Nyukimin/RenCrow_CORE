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
// OS固有の filepath.Clean は Linux 上で UNC の先頭 "//" を失うため、未加工の
// 入力を受け取って両形式を先に正規化する。ドライブルート（"C:\"）と UNC
// 共有ルート（"\\\\host\\share"）も広範囲として拒否する。
func isBroadOrSystemRoot(candidate string) bool {
	// filepath.ToSlash only rewrites the current host OS separator. Normalize
	// Windows separators explicitly so Windows roots are still recognized when
	// validation runs on Linux or macOS.
	slash := strings.ReplaceAll(filepath.ToSlash(candidate), `\`, "/")
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
		if isLetter && strings.Trim(slash[2:], "/") == "" {
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
