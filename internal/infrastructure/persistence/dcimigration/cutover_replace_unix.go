//go:build !windows

package dcimigration

import (
	"os"
	"path/filepath"
)

func atomicReplaceCutoverFile(source, target string) error {
	if err := os.Rename(source, target); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(target))
}
