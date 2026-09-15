//go:build !windows

package gmailintake

import (
	"errors"
	"os"
)

func secureGmailFile(file *os.File) error {
	if file == nil {
		return errors.New("Gmail receipt file handle is required")
	}
	if err := file.Chmod(0o600); err != nil {
		return errors.New("secure Gmail receipt file: chmod failed")
	}
	info, err := file.Stat()
	if err != nil {
		return errors.New("secure Gmail receipt file: stat failed")
	}
	return validateGmailPermissions(file.Name(), info, 0o600)
}

func secureGmailDirectory(path string) error {
	if path == "" {
		return errors.New("Gmail receipt root is required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return errors.New("inspect Gmail receipt root failed")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("Gmail receipt root is not a directory")
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return errors.New("secure Gmail receipt root: chmod failed")
	}
	info, err = os.Lstat(path)
	if err != nil {
		return errors.New("inspect Gmail receipt root after securing failed")
	}
	return validateGmailPermissions(path, info, 0o700)
}

func validateGmailPermissions(_ string, info os.FileInfo, mode os.FileMode) error {
	if info == nil || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("Gmail receipt path is unsafe")
	}
	expected := mode.Perm()
	switch expected {
	case 0o600:
		if !info.Mode().IsRegular() {
			return errors.New("Gmail receipt file is not regular")
		}
	case 0o700:
		if !info.IsDir() {
			return errors.New("Gmail receipt root is not a directory")
		}
	default:
		return errors.New("Gmail receipt permission mode is unsupported")
	}
	if info.Mode().Perm() != expected {
		return errors.New("Gmail receipt permissions are unsafe")
	}
	return nil
}
