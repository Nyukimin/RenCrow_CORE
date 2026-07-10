package execution

import (
	"fmt"
	"strings"
	"time"
)

// ExecutionReport stores evidence for end-to-end execution outcome.
type ExecutionReport struct {
	JobID         string    `json:"job_id"`
	Goal          string    `json:"goal"`
	Route         string    `json:"route,omitempty"`
	Capability    string    `json:"capability_pack,omitempty"`
	Status        string    `json:"status"`
	ErrorKind     string    `json:"error_kind,omitempty"`
	FailureReason string    `json:"failure_reason,omitempty"`
	TTSProvider   string    `json:"tts_provider,omitempty"`
	TTSVoiceID    string    `json:"tts_voice_id,omitempty"`
	TTSAudioFile  string    `json:"tts_audio_file,omitempty"`
	TTSDuration   int       `json:"tts_duration_ms,omitempty"`
	PlaybackCmd   string    `json:"playback_command,omitempty"`
	PlaybackCode  int       `json:"playback_exit_code"`
	TTSErrorKind  string    `json:"tts_error_kind,omitempty"`
	Acceptance    []string  `json:"acceptance,omitempty"`
	Constraints   []string  `json:"constraints,omitempty"`
	Artifacts     []string  `json:"artifacts,omitempty"`
	Verification  []string  `json:"verification,omitempty"`
	Rollback      []string  `json:"rollback,omitempty"`
	Steps         []string  `json:"steps,omitempty"`
	AttemptCount  int       `json:"attempt_count"`
	RepairCount   int       `json:"repair_count"`
	Error         string    `json:"error,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	FinishedAt    time.Time `json:"finished_at"`
}

func (r ExecutionReport) Validate() error {
	if strings.TrimSpace(r.JobID) == "" {
		return fmt.Errorf("job_id is required")
	}
	if strings.TrimSpace(r.Goal) == "" {
		return fmt.Errorf("goal is required")
	}
	if strings.TrimSpace(r.Status) == "" {
		return fmt.Errorf("status is required")
	}
	if r.CreatedAt.IsZero() {
		return fmt.Errorf("created_at is required")
	}
	if r.FinishedAt.IsZero() {
		return fmt.Errorf("finished_at is required")
	}
	return nil
}
