//go:build !windows

package dcimigration

import (
	"os"
	"syscall"
)

// cutoverKnownFileIsUnaliased binds the supplied identity to the exact path
// and rejects every Unix inode with more than one directory link.  The second
// stat narrows the path-replacement window and detects an observed identity or
// link-count change before the caller's subsequent operation.
func cutoverKnownFileIsUnaliased(path string, info os.FileInfo) bool {
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
