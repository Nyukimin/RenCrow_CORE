//go:build !windows

package runmigration

import (
	"os"
	"syscall"
)

// migrationFileIsUnaliased verifies the path identity and exact Unix link
// count. Any unavailable or changing identity is unsafe and therefore fails
// closed for an offline snapshot.
func migrationFileIsUnaliased(path string, info os.FileInfo) bool {
	if info == nil {
		return false
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, current) {
		return false
	}
	stat, ok := current.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return false
	}
	latest, err := os.Lstat(path)
	if err != nil || !os.SameFile(current, latest) {
		return false
	}
	latestStat, ok := latest.Sys().(*syscall.Stat_t)
	return ok && latestStat.Nlink == 1
}
