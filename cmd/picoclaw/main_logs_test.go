package main

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestRunLogsCommand_JSONHeaderSnapshot(t *testing.T) {
	var out, errOut bytes.Buffer
	tailCalled := false
	code := runLogsCommand(
		[]string{"--json"},
		"/tmp/picoclaw.log",
		&out,
		&errOut,
		func(path string, n int, dst io.Writer) error {
			tailCalled = true
			if path != "/tmp/picoclaw.log" || n != 100 {
				t.Fatalf("unexpected tail args: %s %d", path, n)
			}
			_, _ = dst.Write([]byte("line-a\n"))
			return nil
		},
		func(_ string, _ io.Writer) error { return nil },
		fixedNow,
	)
	if code != 0 {
		t.Fatalf("expected code 0, got %d (err=%s)", code, errOut.String())
	}
	if !tailCalled {
		t.Fatal("expected tail function to be called")
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected json header + log lines, got: %q", out.String())
	}
	var header struct {
		Component string `json:"component"`
		Status    string `json:"status"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatalf("invalid json header: %v (line=%q)", err, lines[0])
	}
	if header.Component != "logs" || header.Status != "snapshot" {
		t.Fatalf("unexpected header: %+v", header)
	}
}
