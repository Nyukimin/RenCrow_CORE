//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
)

func proveFixedThreadWriterStopped(ctx context.Context) error {
	show := exec.CommandContext(ctx, "/usr/bin/systemctl", "--user", "show", "rencrow.service", "-p", "LoadState", "-p", "ActiveState", "-p", "MainPID", "--no-pager")
	output, err := show.Output()
	if err != nil {
		return errWriterStoppedProofUnavailable
	}
	values := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		key, value, found := strings.Cut(line, "=")
		if !found || key == "" {
			return errors.New("fixed CORE service state is malformed")
		}
		values[key] = value
	}
	if len(values) != 3 || (values["LoadState"] != "loaded" && values["LoadState"] != "masked") || values["ActiveState"] != "inactive" || values["MainPID"] != "0" {
		return errors.New("fixed CORE service is not stopped")
	}
	listener := exec.CommandContext(ctx, "/usr/bin/ss", "-H", "-ltn", "sport", "=", ":18790")
	listenerOutput, err := listener.Output()
	if err != nil {
		return errWriterStoppedProofUnavailable
	}
	if len(bytes.TrimSpace(listenerOutput)) != 0 {
		return errors.New("fixed CORE listener is not stopped")
	}
	return nil
}
