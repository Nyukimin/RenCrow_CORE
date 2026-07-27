package logging

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSessionLogWriterReportsWriteFailure は書き込み失敗を無言で
// 握りつぶさないことを確認する
//
// docs/10_ログ仕様.md「記録と配信の分離」より:
// 記録パスは書き込み失敗を無言で握りつぶさず error として出力する。
//
// 従来は os.MkdirAll と os.OpenFile の失敗をどちらも素の return で捨てて
// おり、会話ログが1行も残らなくても気づけなかった。
func TestSessionLogWriterReportsWriteFailure(t *testing.T) {
	// baseDir と同名のファイルを作り、配下にディレクトリを作れない状態にする
	base := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(base, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("prepare blocked path: %v", err)
	}

	var buf bytes.Buffer
	restore := captureLogOutput(t, &buf)
	defer restore()

	w := NewSessionLogWriter(base)
	w.WriteUser("session-1", "chat", "hello")

	out := buf.String()
	if strings.TrimSpace(out) == "" {
		t.Fatal("write failure should be reported, but nothing was logged")
	}
	if !strings.Contains(out, "session log") {
		t.Fatalf("log should identify the session log writer, got: %q", out)
	}
}

// TestSessionLogWriterStaysSilentOnSuccess は成功時に余計な出力を
// しないことを確認する
func TestSessionLogWriterStaysSilentOnSuccess(t *testing.T) {
	var buf bytes.Buffer
	restore := captureLogOutput(t, &buf)
	defer restore()

	w := NewSessionLogWriter(t.TempDir())
	w.WriteUser("session-2", "chat", "hello")

	if strings.TrimSpace(buf.String()) != "" {
		t.Fatalf("successful write should not log, got: %q", buf.String())
	}
}

func captureLogOutput(t *testing.T, buf *bytes.Buffer) func() {
	t.Helper()
	prevOut := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(buf)
	log.SetFlags(0)
	return func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	}
}
