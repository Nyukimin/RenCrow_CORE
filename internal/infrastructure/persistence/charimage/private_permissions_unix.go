//go:build !windows

package charimage

import (
	"fmt"
	"os"
	"path/filepath"
)

func validatePrivateDatabaseDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("CharImage history parent directory is unavailable: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o300 != 0o300 || info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("CharImage history parent directory permissions are unsafe (mode %o)", info.Mode().Perm())
	}
	return nil
}

func secureCreatedPrivateDatabase(path string, file *os.File) error {
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("set CharImage history database permissions: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect new CharImage history database: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
		return fmt.Errorf("new CharImage history database permissions are unsafe")
	}
	return nil
}

func validateExistingPrivateDatabase(path string, info os.FileInfo) error {
	if info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 || info.Mode().Perm()&0o600 != 0o600 {
		return fmt.Errorf("existing CharImage history database must be a private regular file: %s", filepath.Base(path))
	}
	return nil
}
