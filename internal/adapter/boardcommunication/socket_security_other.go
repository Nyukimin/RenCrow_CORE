//go:build !linux && !darwin

package boardcommunication

import (
	"context"
	"errors"
	"net"
)

var errUnixSocketDialFailure = errors.New("unix socket unavailable")

func unixSocketDialContext(string) (func(context.Context, string, string) (net.Conn, error), error) {
	return nil, ErrUnsupported
}

func validateUnixSocketPath(string) error { return ErrUnsupported }
