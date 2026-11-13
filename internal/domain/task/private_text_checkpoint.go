package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const PrivateTextWaitingReason = "private text paused at checkpoint"

const privateTextCheckpointDigestVersion = "private-text-checkpoint.v1"

// PrivateTextCheckpoint is durable Task-owned execution state. It is carried
// only inside SharedRoleContext and must be removed from public projections.
// An empty Output marks a prepared checkpoint; a non-empty Output is immutable.
type PrivateTextCheckpoint struct {
	TaskID      modulecore.TaskID      `json:"task_id"`
	RunID       modulecore.RunID       `json:"run_id"`
	QueueItemID modulecore.QueueItemID `json:"queue_item_id"`
	ActorID     string                 `json:"actor_id"`
	InputSHA256 string                 `json:"input_sha256"`
	Output      string                 `json:"output"`
}

type privateTextCheckpointDigestPayload struct {
	Version     string                 `json:"version"`
	TaskID      modulecore.TaskID      `json:"task_id"`
	RunID       modulecore.RunID       `json:"run_id"`
	QueueItemID modulecore.QueueItemID `json:"queue_item_id"`
	ActorID     string                 `json:"actor_id"`
	InputSHA256 string                 `json:"input_sha256"`
	Output      string                 `json:"output"`
}

// Digest hashes the versioned canonical JSON representation, including the
// output bytes. The payload is a fixed struct so field ordering is stable.
func (checkpoint PrivateTextCheckpoint) Digest() string {
	payload := privateTextCheckpointDigestPayload{
		Version:     privateTextCheckpointDigestVersion,
		TaskID:      checkpoint.TaskID,
		RunID:       checkpoint.RunID,
		QueueItemID: checkpoint.QueueItemID,
		ActorID:     checkpoint.ActorID,
		InputSHA256: checkpoint.InputSHA256,
		Output:      checkpoint.Output,
	}
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

// Validate checks the persisted cursor shape and output size. Empty output is
// valid for the prepared state; when present, output must be valid non-blank
// UTF-8 and fit the established report result byte limit.
func (checkpoint PrivateTextCheckpoint) Validate() error {
	if err := checkpoint.TaskID.Validate(); err != nil {
		return errors.New("private text checkpoint task_id is invalid")
	}
	if err := checkpoint.RunID.Validate(); err != nil {
		return errors.New("private text checkpoint run_id is invalid")
	}
	if err := checkpoint.QueueItemID.Validate(); err != nil {
		return errors.New("private text checkpoint queue_item_id is invalid")
	}
	if checkpoint.ActorID == "" || strings.TrimSpace(checkpoint.ActorID) != checkpoint.ActorID || !utf8.ValidString(checkpoint.ActorID) {
		return errors.New("private text checkpoint actor_id is invalid")
	}
	if len(checkpoint.InputSHA256) != sha256.Size*2 || strings.ToLower(checkpoint.InputSHA256) != checkpoint.InputSHA256 {
		return errors.New("private text checkpoint input_sha256 is invalid")
	}
	if _, err := hex.DecodeString(checkpoint.InputSHA256); err != nil {
		return errors.New("private text checkpoint input_sha256 is invalid")
	}
	if checkpoint.Output == "" {
		return nil
	}
	if !utf8.ValidString(checkpoint.Output) || len(checkpoint.Output) > conversation.MaxReportResultBytes || strings.TrimSpace(checkpoint.Output) == "" {
		return errors.New("private text checkpoint output is invalid")
	}
	return nil
}
