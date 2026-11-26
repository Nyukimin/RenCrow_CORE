//go:build !darwin

package main

import "os"

func openSystemJobsPlistFile(path string) (*os.File, error) {
	return os.Open(path)
}
