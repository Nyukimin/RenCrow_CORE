package orchestrator

import (
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

type ttsEnabledFunc func() bool

type messageTaskContextBuilder struct {
	emit       messageEventEmitter
	ttsEnabled ttsEnabledFunc
}

func newMessageTaskContextBuilder(emit messageEventEmitter, ttsEnabled ttsEnabledFunc) *messageTaskContextBuilder {
	return &messageTaskContextBuilder{
		emit:       emit,
		ttsEnabled: ttsEnabled,
	}
}

func (b *messageTaskContextBuilder) Build(req ProcessMessageRequest) (task.Task, task.TaskID, string) {
	jobID := resolveProcessMessageJobID(req.JobID)
	return b.BuildWithJobID(req, jobID)
}

func resolveProcessMessageJobID(raw string) task.TaskID {
	if raw != "" {
		if taskID, err := task.ParseTaskID(raw); err == nil {
			return taskID
		}
	}
	return task.NewTaskID()
}

func (b *messageTaskContextBuilder) BuildWithJobID(req ProcessMessageRequest, jobID task.TaskID) (task.Task, task.TaskID, string) {
	t := task.NewTask(jobID, req.UserMessage, req.Channel, req.ChatID).
		WithAttachments(req.Attachments).
		WithViewerRecipient(normalizeProcessViewerRecipient(req.To))
	if len(req.Attachments) > 0 {
		b.emit("viewer.attachment.received", "viewer", "mio",
			fmt.Sprintf("%d attachment(s)", len(req.Attachments)),
			"", jobID.String(), req.SessionID, req.Channel, req.ChatID)
	}
	ttsSessionID := ""
	if b.ttsEnabled() && ttsAllowedForRequest(req) {
		ttsSessionID = fmt.Sprintf("%s-%s", req.SessionID, jobID.String())
	}
	return t, jobID, ttsSessionID
}

func ttsAllowedForRequest(req ProcessMessageRequest) bool {
	intent := strings.ToLower(strings.TrimSpace(string(req.AudioOutput)))
	if intent == string(AudioOutputDisabled) {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(req.OperationSource), "RenCrow_CMD") {
		return intent == string(AudioOutputRequested)
	}
	return true
}
