package worker

import (
	"fmt"
	"strings"
)

type AutonomousContract struct {
	Acceptance []string
}

type AutonomousAttemptResult struct {
	Response     string
	TTSAudioFile string
	PlaybackCode int
}

func VerifyAutonomousAttempt(route string, contract AutonomousContract, last AutonomousAttemptResult) (bool, string, string) {
	if strings.TrimSpace(last.Response) == "" {
		return false, "verification_failed", "empty response"
	}

	if IsTTSCapability(contract) {
		return VerifyTTSAttempt(last)
	}

	if isCodeRouteName(route) {
		// CoderLoop の final_report は実行可能コードを含まない調査レポートも正常出力
		if !LooksLikeCoderLoopReport(last.Response) && LooksLikeNonExecutable(last.Response) {
			return false, "non_executable_output",
				"Coder output contains design document only; executable patch is required"
		}
		if ResponseLooksLikeFailure(last.Response) {
			return false, "verification_failed", ShortFailureReason(last.Response)
		}
		return true, "", ""
	}

	if ResponseLooksLikeFailure(last.Response) {
		return false, "verification_failed", ShortFailureReason(last.Response)
	}
	return true, "", ""
}

func IsTTSCapability(contract AutonomousContract) bool {
	for _, acceptance := range contract.Acceptance {
		if strings.Contains(acceptance, "実再生") || strings.Contains(acceptance, "音声ファイル生成") {
			return true
		}
	}
	return false
}

func VerifyTTSAttempt(last AutonomousAttemptResult) (bool, string, string) {
	if last.TTSAudioFile == "" && last.PlaybackCode == 0 {
		if ResponseLooksLikeFailure(last.Response) {
			return false, "verification_failed", ShortFailureReason(last.Response)
		}
		return true, "", ""
	}
	if last.TTSAudioFile == "" {
		return false, "tts_no_audio", "音声ファイルが生成されていない (TTSAudioFile が空)"
	}
	if last.PlaybackCode != 0 {
		return false, "playback_failed",
			fmt.Sprintf("再生コマンドが終了コード %d で終了した", last.PlaybackCode)
	}
	return true, "", ""
}

// LooksLikeCoderLoopReport は CoderLoop の final_report フォーマットかを判定する。
// CoderLoop 完了レポートは実行可能コードを含まなくても正常出力。
func LooksLikeCoderLoopReport(response string) bool {
	return strings.HasPrefix(response, "✅ CoderLoop:") ||
		strings.HasPrefix(response, "⚠️ CoderLoop:")
}

func LooksLikeNonExecutable(response string) bool {
	lower := strings.ToLower(response)
	executables := []string{
		"```",
		"patch:",
		"apply:",
		"execute:",
		"$ ",
		"#!/",
		"execution result",
		"success rate",
	}
	for _, marker := range executables {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	return true
}

func ResponseLooksLikeFailure(content string) bool {
	lower := strings.ToLower(strings.TrimSpace(content))
	if lower == "" {
		return false
	}
	if strings.Contains(lower, "失敗: 0") || strings.Contains(lower, "failures: 0") || strings.Contains(lower, "failed: 0") {
		return false
	}
	return strings.Contains(lower, "error") || strings.Contains(lower, "失敗") ||
		strings.Contains(content, "エラー")
}

func ShortFailureReason(content string) string {
	text := strings.TrimSpace(content)
	if len(text) <= 160 {
		return text
	}
	return text[:157] + "..."
}
