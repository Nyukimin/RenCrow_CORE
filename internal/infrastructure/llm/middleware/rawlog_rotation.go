package middleware

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/logging"
)

// 生応答ログの上限設定とエントリ組み立て。
//
// docs/10_ログ仕様.md「保持と上限」に従い、すべての出力先が上限か保持期間を
// 持つようにする。生応答ログは不具合調査で最も役立つため出力自体は止めず、
// 上限だけを設ける。

const (
	// defaultRawLogMaxBytes は生応答ログ1ファイルの既定上限
	defaultRawLogMaxBytes int64 = 64 << 20 // 64MiB
	// defaultRawLogMaxBackups は保持する世代数の既定値
	defaultRawLogMaxBackups = 3
)

// rawLogLimits は環境変数から上限設定を読む
//
// 未設定や不正値では既定値を使う。ログ出力を止めないため、設定不備を
// 理由に無効化しない。
func rawLogLimits() (int64, int) {
	maxBytes := defaultRawLogMaxBytes
	if raw := strings.TrimSpace(os.Getenv("RENCROW_RAW_LOG_MAX_BYTES")); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
			maxBytes = parsed
		}
	}
	maxBackups := defaultRawLogMaxBackups
	if raw := strings.TrimSpace(os.Getenv("RENCROW_RAW_LOG_MAX_BACKUPS")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			maxBackups = parsed
		}
	}
	return maxBytes, maxBackups
}

// openRawLogSink は生応答ログの書き出し先を開く
//
// 出力先は環境変数で指定する。未指定の場合はユーザーのホーム配下へ置く。
// 従来は "/home/nyukimi/..." を既定にしていたが、Windows と macOS では
// 成立しないため os.UserHomeDir を使う。
func openRawLogSink(envVar, defaultName, label string) (rawLogSink, error) {
	path := strings.TrimSpace(os.Getenv(envVar))
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Printf("[LLM][raw] %s raw log disabled: %v", label, err)
			return nil, err
		}
		path = filepath.Join(home, ".rencrow", "logs", defaultName)
	}
	maxBytes, maxBackups := rawLogLimits()
	w, err := logging.NewRotatingWriter(path, maxBytes, maxBackups)
	if err != nil {
		log.Printf("[LLM][raw] %s raw open failed: %v", label, err)
		return nil, err
	}
	log.Printf("[LLM][raw] %s raw file enabled: %s (max_bytes=%d max_backups=%d)", label, path, maxBytes, maxBackups)
	return w, nil
}

// buildRawLogEntry は1エントリ分の文字列を組み立てる
//
// ヘッダ・本文・区切りを個別に書くと、エントリの途中で世代交代が起きて
// 1件のログが2ファイルに分断される。分断されると解析できないため、
// 1エントリを1回の書き込みにまとめる。
func buildRawLogEntry(kind, provider, finish string, maxTokens int, msgCount int, content string) string {
	var sb strings.Builder
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	fmt.Fprintf(&sb, "ts=%s kind=%s provider=%s finish=%s max_tokens=%d msgs=%d\n",
		ts, kind, provider, finish, maxTokens, msgCount)
	sb.WriteString(content)
	if !strings.HasSuffix(content, "\n") {
		sb.WriteString("\n")
	}
	sb.WriteString("----\n")
	return sb.String()
}
