package tts

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestClassifyTTSError(t *testing.T) {
	tests := []struct {
		name         string
		err          error
		wantKind     string
		wantContains string
	}{
		{
			name:         "provider unavailable",
			err:          ErrProviderUnavailable,
			wantKind:     "provider_unavailable",
			wantContains: "unavailable",
		},
		{
			name:         "synthesis failed",
			err:          ErrSynthesisFailed,
			wantKind:     "synthesis_failed",
			wantContains: "synthesis",
		},
		{
			name:         "invalid input",
			err:          ErrInvalidInput,
			wantKind:     "invalid_input",
			wantContains: "invalid",
		},
		{
			name:         "playback failed",
			err:          ErrPlaybackFailed,
			wantKind:     "playback_failed",
			wantContains: "playback",
		},
		{
			name:         "command not found",
			err:          ErrCommandNotFound,
			wantKind:     "command_not_found",
			wantContains: "command",
		},
		{
			name:         "audio file not found",
			err:          ErrAudioFileNotFound,
			wantKind:     "audio_file_not_found",
			wantContains: "audio file",
		},
		{
			name:         "repair exhausted",
			err:          ErrRepairExhausted,
			wantKind:     "repair_exhausted",
			wantContains: "repair",
		},
		{
			name:         "wrapped error",
			err:          fmt.Errorf("wrapped: %w", ErrCommandNotFound),
			wantKind:     "command_not_found",
			wantContains: "command",
		},
		{
			name:         "unknown error",
			err:          errors.New("unknown tts error"),
			wantKind:     "tts_unknown",
			wantContains: "unknown",
		},
		{
			name:         "nil error",
			err:          nil,
			wantKind:     "",
			wantContains: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, reason := ClassifyTTSError(tt.err)

			if kind != tt.wantKind {
				t.Errorf("ClassifyTTSError() kind = %q, want %q", kind, tt.wantKind)
			}

			if tt.wantContains != "" && !strings.Contains(reason, tt.wantContains) {
				t.Errorf("ClassifyTTSError() reason = %q, want contains %q", reason, tt.wantContains)
			}

			if tt.err == nil && reason != "" {
				t.Errorf("ClassifyTTSError() reason = %q, want empty for nil error", reason)
			}
		})
	}
}

func TestErrorsAreDistinct(t *testing.T) {
	// Verify all errors are distinct and can be identified with errors.Is
	allErrors := []error{
		ErrProviderUnavailable,
		ErrSynthesisFailed,
		ErrInvalidInput,
		ErrPlaybackFailed,
		ErrCommandNotFound,
		ErrAudioFileNotFound,
		ErrRepairExhausted,
	}

	for i, err1 := range allErrors {
		for j, err2 := range allErrors {
			if i == j {
				continue
			}
			if errors.Is(err1, err2) {
				t.Errorf("Error %v should not match error %v", err1, err2)
			}
		}
	}
}
