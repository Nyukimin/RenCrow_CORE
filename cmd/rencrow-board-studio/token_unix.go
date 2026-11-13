//go:build !windows

package main

import (
	"errors"
	"io/fs"
)

func checkTokenPermissions(mode fs.FileMode) error {
	if mode.Perm()&0077 != 0 {
		return errors.New("Studio token file must be private")
	}
	return nil
}
