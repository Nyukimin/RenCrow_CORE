package middleware

import (
	"strings"
	"testing"
)

// TestBuildRawLogEntryIsSingleWrite は1エントリを1回の書き込みにまとめる
// ことを確認する
//
// docs/10_ログ仕様.md「保持と上限」に従いローテーションを導入すると、
// ヘッダ・本文・区切りを個別に書く実装では、エントリの途中で世代交代が
// 起きて1件のログが2ファイルに分断される。分断されると解析できない。
func TestBuildRawLogEntryIsSingleWrite(t *testing.T) {
	entry := buildRawLogEntry("resp", "ollama", "stop", 512, 3, "hello world")

	if !strings.HasPrefix(entry, "ts=") {
		t.Fatalf("entry should start with ts=, got: %q", entry)
	}
	for _, want := range []string{"kind=resp", "provider=ollama", "finish=stop", "max_tokens=512", "msgs=3"} {
		if !strings.Contains(entry, want) {
			t.Errorf("entry should contain %q, got: %q", want, entry)
		}
	}
	if !strings.Contains(entry, "hello world") {
		t.Errorf("entry should contain the content, got: %q", entry)
	}
	if !strings.HasSuffix(entry, "----\n") {
		t.Errorf("entry should end with the separator, got: %q", entry)
	}
}

// TestBuildRawLogEntryTerminatesContent は本文が改行で終わっていない場合に
// 改行を補うことを確認する
func TestBuildRawLogEntryTerminatesContent(t *testing.T) {
	withoutNewline := buildRawLogEntry("resp", "p", "stop", 1, 1, "abc")
	withNewline := buildRawLogEntry("resp", "p", "stop", 1, 1, "abc\n")

	if !strings.Contains(withoutNewline, "abc\n----\n") {
		t.Fatalf("newline should be added, got: %q", withoutNewline)
	}
	if strings.Contains(withNewline, "abc\n\n----\n") {
		t.Fatalf("newline should not be duplicated, got: %q", withNewline)
	}
}

// TestRawLogLimitsFromEnv は上限の設定を環境変数から読むことを確認する
//
// 生応答ログは不具合調査で最も役立つため出力自体は止めない。上限だけを
// 設定可能にする。
func TestRawLogLimitsFromEnv(t *testing.T) {
	t.Setenv("RENCROW_RAW_LOG_MAX_BYTES", "1048576")
	t.Setenv("RENCROW_RAW_LOG_MAX_BACKUPS", "5")

	maxBytes, maxBackups := rawLogLimits()
	if maxBytes != 1048576 {
		t.Fatalf("maxBytes = %d, want 1048576", maxBytes)
	}
	if maxBackups != 5 {
		t.Fatalf("maxBackups = %d, want 5", maxBackups)
	}
}

func TestRawLogLimitsFallsBackToDefaults(t *testing.T) {
	t.Setenv("RENCROW_RAW_LOG_MAX_BYTES", "")
	t.Setenv("RENCROW_RAW_LOG_MAX_BACKUPS", "not-a-number")

	maxBytes, maxBackups := rawLogLimits()
	if maxBytes != defaultRawLogMaxBytes {
		t.Fatalf("maxBytes = %d, want %d", maxBytes, defaultRawLogMaxBytes)
	}
	if maxBackups != defaultRawLogMaxBackups {
		t.Fatalf("maxBackups = %d, want %d", maxBackups, defaultRawLogMaxBackups)
	}
}
