//go:build linux || darwin

package boardcommunication

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

var errUnixSocketDialFailure = errors.New("unix socket unavailable")

func unixSocketDialContext(path string) (func(context.Context, string, string) (net.Conn, error), error) {
	if !isCleanAbsoluteSocketPath(path) {
		return nil, ErrInvalidConfiguration
	}
	dialer := &net.Dialer{}
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		if err := validateUnixSocketPath(path); err != nil {
			return nil, err
		}
		connection, err := dialer.DialContext(ctx, "unix", path)
		if err != nil {
			return nil, errUnixSocketDialFailure
		}
		return connection, nil
	}, nil
}

func validateUnixSocketPath(path string) error {
	if !isCleanAbsoluteSocketPath(path) {
		return ErrInvalidConfiguration
	}
	root := string(filepath.Separator)
	current := root
	components := strings.Split(strings.TrimPrefix(path, root), root)
	var final os.FileInfo
	for index, component := range components {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return ErrUnavailable
			}
			return ErrInvalidConfiguration
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrInvalidConfiguration
		}
		if index < len(components)-1 && !info.IsDir() {
			return ErrInvalidConfiguration
		}
		final = info
	}
	if final == nil || final.Mode()&os.ModeSocket == 0 {
		return ErrInvalidConfiguration
	}
	parentPath := filepath.Dir(path)
	parent, err := os.Lstat(parentPath)
	if err != nil || parent.Mode()&os.ModeSymlink != 0 || !parent.IsDir() || parent.Mode().Perm() != 0o700 {
		return ErrInvalidConfiguration
	}
	if !ownedByCurrentUser(parent) || !ownedByCurrentUser(final) || final.Mode().Perm()&0o077 != 0 || final.Mode().Perm()&0o600 != 0o600 {
		return ErrInvalidConfiguration
	}
	return nil
}

func ownedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}
