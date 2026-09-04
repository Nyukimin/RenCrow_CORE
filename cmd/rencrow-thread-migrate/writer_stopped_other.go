//go:build !linux

package main

import (
	"context"
)

func proveFixedThreadWriterStopped(context.Context) error {
	return errWriterStoppedProofUnavailable
}
