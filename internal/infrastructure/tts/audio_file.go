package tts

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

func saveGatewayWAV(body io.Reader, outputDir, prefix string) (string, error) {
	dir := strings.TrimSpace(outputDir)
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir tts output dir: %w", err)
	}
	safePrefix := moduletts.SanitizeAudioPrefix(prefix)
	if safePrefix == "" {
		safePrefix = "rencrow-tts"
	}
	f, err := os.CreateTemp(dir, safePrefix+"-*.wav")
	if err != nil {
		return "", fmt.Errorf("create temp wav: %w", err)
	}
	if _, err := io.Copy(f, body); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("write wav response: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("close wav response: %w", err)
	}
	if err := rejectSilentWAV(f.Name()); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return filepath.Clean(f.Name()), nil
}

func rejectSilentWAV(path string) error {
	stats, ok, err := inspectPCM16WAV(path)
	if err != nil {
		return err
	}
	if !ok || !stats.NearSilent {
		return nil
	}
	return fmt.Errorf("%w: generated wav is silent or near silent duration_ms=%d rms=%d peak=%d", ErrSynthesisFailed, stats.DurationMS, stats.RMS, stats.Peak)
}

func ensureTTSPunctuation(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	last, _ := utf8.DecodeLastRuneInString(text)
	switch last {
	case '。', '！', '？', '!', '?', '.', '…', '♪', '、', ',', '」', '』', ')', '）':
		return text
	default:
		return text + "。"
	}
}

func chunkPauseForText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "200ms"
	}
	if strings.HasSuffix(text, "。") || strings.HasSuffix(text, "！") || strings.HasSuffix(text, "？") || strings.HasSuffix(text, "!") || strings.HasSuffix(text, "?") {
		return "320ms"
	}
	if strings.HasSuffix(text, "、") || strings.HasSuffix(text, ",") {
		return "180ms"
	}
	ms := 120 + len([]rune(text))*8
	if ms > 400 {
		ms = 400
	}
	return strconv.Itoa(ms) + "ms"
}
