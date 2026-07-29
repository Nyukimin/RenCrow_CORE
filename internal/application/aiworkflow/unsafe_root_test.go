package aiworkflow

import (
	"strings"
	"testing"
)

// broadRootCases はホストOSに関係なく広範囲ルートとして拒否すべき入力を列挙する
//
// ホストOSに依存する filepath.Clean より前の表記を渡し、Windows / POSIX / UNC
// の各形式をどのOSでも同じように判定する。
var broadRootCases = []string{
	"/",
	"/home",
	"/tmp",
	"/var",
	"/etc",
	"/usr",
	"/System",
	"/Applications",
	"C:/",
	"C:\\",
	"c:/",
	"D:\\",
	"//host/share",
	"\\\\host\\share",
}

var safeRootCases = []string{
	"/home/nyukimi/RenCrow/RenCrow_CORE",
	"/srv/worktrees/example",
	"C:/Users/nyuki/RenCrow",
	"C:\\Users\\nyuki\\RenCrow",
	"//host/share/project",
}

func TestIsBroadOrSystemRootRejectsBroadRoots(t *testing.T) {
	for _, in := range broadRootCases {
		if !isBroadOrSystemRoot(in) {
			t.Errorf("isBroadOrSystemRoot(%q) = false, want true", in)
		}
	}
}

func TestIsBroadOrSystemRootAcceptsSpecificPaths(t *testing.T) {
	for _, in := range safeRootCases {
		if isBroadOrSystemRoot(in) {
			t.Errorf("isBroadOrSystemRoot(%q) = true, want false", in)
		}
	}
}

func TestRejectWorktreeBaseRejectsBroadRoots(t *testing.T) {
	for _, in := range broadRootCases {
		err := rejectWorktreeBase(in)
		if err == nil {
			t.Errorf("rejectWorktreeBase(%q) = nil, want error", in)
			continue
		}
		if !strings.Contains(err.Error(), "broad or system worktree base") {
			t.Errorf("rejectWorktreeBase(%q) error = %v, want broad base rejection", in, err)
		}
	}
}

func TestRejectProjectInitUnsafeRootRejectsBroadRoots(t *testing.T) {
	for _, in := range broadRootCases {
		err := rejectProjectInitUnsafeRoot(in)
		if err == nil {
			t.Errorf("rejectProjectInitUnsafeRoot(%q) = nil, want error", in)
			continue
		}
		if !strings.Contains(err.Error(), "broad or system root") {
			t.Errorf("rejectProjectInitUnsafeRoot(%q) error = %v, want broad root rejection", in, err)
		}
	}
}
