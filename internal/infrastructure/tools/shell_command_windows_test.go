//go:build windows

package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestShellCommandContextUsesPowerShell は Windows で POSIX shell に依存せず
// PowerShell を使うことを確認する
//
// "sh" 固定だと、Windows 標準環境（Git for Windows 等が未導入）では
// shell ツールが常に失敗する。
func TestShellCommandContextUsesPowerShell(t *testing.T) {
	cmd, err := shellCommandContext(context.Background(), "Write-Output ok")
	if err != nil {
		t.Fatalf("shellCommandContext() error = %v", err)
	}
	base := strings.ToLower(filepath.Base(cmd.Path))
	if base != "powershell.exe" {
		t.Fatalf("shell = %q, want powershell.exe", base)
	}
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "-Command") || !strings.Contains(joined, "Write-Output ok") {
		t.Fatalf("unexpected args: %#v", cmd.Args)
	}
}

// TestShellScriptCommandContextResolvesPOSIXShellOrFails は .sh ツールについて、
// POSIX shell が見つかればそれを使い、無ければ明示的にエラーを返すことを確認する
func TestShellScriptCommandContextResolvesPOSIXShellOrFails(t *testing.T) {
	cmd, err := shellScriptCommandContext(context.Background(), `C:\tmp\tool.sh`, "{}")
	if err != nil {
		if !strings.Contains(err.Error(), "POSIX shell required") {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	base := strings.ToLower(filepath.Base(cmd.Path))
	if base != "sh" && base != "sh.exe" && base != "bash.exe" {
		t.Fatalf("shell = %q, want a POSIX shell", base)
	}
}
