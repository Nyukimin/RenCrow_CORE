package tts

import "errors"

// Synthesis errors
var (
	// ErrProviderUnavailable indicates the TTS provider is not configured or unreachable
	ErrProviderUnavailable = errors.New("tts provider unavailable")

	// ErrSynthesisFailed indicates audio synthesis failed
	ErrSynthesisFailed = errors.New("synthesis failed")

	// ErrInvalidInput indicates the synthesis input was invalid
	ErrInvalidInput = errors.New("invalid synthesis input")
)

// Playback errors
var (
	// ErrPlaybackFailed indicates audio playback failed
	ErrPlaybackFailed = errors.New("playback failed")

	// ErrCommandNotFound indicates the playback command was not found or not configured
	ErrCommandNotFound = errors.New("playback command not found")

	// ErrAudioFileNotFound indicates the generated audio file was not found
	ErrAudioFileNotFound = errors.New("audio file not found")
)

// Repair errors (for autonomous executor)
var (
	// ErrRepairExhausted indicates all repair attempts have been exhausted
	ErrRepairExhausted = errors.New("tts repair attempts exhausted")
)

// ClassifyTTSError returns error_kind and failure_reason for ExecutionReport.
// This provides structured error classification for TTS failures in the autonomous executor.
//
// Returns:
//   - errorKind: categorized error type (e.g., "synthesis_failed", "playback_failed")
//   - failureReason: detailed error message for debugging
func ClassifyTTSError(err error) (errorKind, failureReason string) {
	if err == nil {
		return "", ""
	}

	switch {
	case errors.Is(err, ErrProviderUnavailable):
		return "provider_unavailable", err.Error()
	case errors.Is(err, ErrSynthesisFailed):
		return "synthesis_failed", err.Error()
	case errors.Is(err, ErrInvalidInput):
		return "invalid_input", err.Error()
	case errors.Is(err, ErrPlaybackFailed):
		return "playback_failed", err.Error()
	case errors.Is(err, ErrCommandNotFound):
		return "command_not_found", err.Error()
	case errors.Is(err, ErrAudioFileNotFound):
		return "audio_file_not_found", err.Error()
	case errors.Is(err, ErrRepairExhausted):
		return "repair_exhausted", err.Error()
	default:
		return "tts_unknown", err.Error()
	}
}
