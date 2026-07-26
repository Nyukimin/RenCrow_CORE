//go:build !windows

package tools

import (
	"context"
	"path/filepath"
	"testing"
)

// TestShellCommandContextUsesPOSIXShell は Unix 系で sh -c を使うことを確認する
func TestShellCommandContextUsesPOSIXShell(t *testing.T) {
	cmd, err := shellCommandContext(context.Background(), "echo ok")
	if err != nil {
		t.Fatalf("shellCommandContext() error = %v", err)
	}
	if got := filepath.Base(cmd.Path); got != "sh" {
		t.Fatalf("shell = %q, want sh", got)
	}
	if len(cmd.Args) < 3 || cmd.Args[1] != "-c" || cmd.Args[2] != "echo ok" {
		t.Fatalf("unexpected args: %#v", cmd.Args)
	}
}

func TestShellScriptCommandContextUsesPOSIXShell(t *testing.T) {
	cmd, err := shellScriptCommandContext(context.Background(), "/tmp/tool.sh", "{}")
	if err != nil {
		t.Fatalf("shellScriptCommandContext() error = %v", err)
	}
	if got := filepath.Base(cmd.Path); got != "sh" {
		t.Fatalf("shell = %q, want sh", got)
	}
}
