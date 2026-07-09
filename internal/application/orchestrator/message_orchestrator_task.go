package orchestrator

import (
	"fmt"

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

func (b *messageTaskContextBuilder) Build(req ProcessMessageRequest) (task.Task, task.JobID, string) {
	jobID := task.NewJobID()
	return b.BuildWithJobID(req, jobID)
}

func (b *messageTaskContextBuilder) BuildWithJobID(req ProcessMessageRequest, jobID task.JobID) (task.Task, task.JobID, string) {
	t := task.NewTask(jobID, req.UserMessage, req.Channel, req.ChatID).
		WithAttachments(req.Attachments).
		WithViewerRecipient(normalizeProcessViewerRecipient(req.To))
	if len(req.Attachments) > 0 {
		b.emit("viewer.attachment.received", "viewer", "mio",
			fmt.Sprintf("%d attachment(s)", len(req.Attachments)),
			"", jobID.String(), req.SessionID, req.Channel, req.ChatID)
	}
	ttsSessionID := ""
	if b.ttsEnabled() {
		ttsSessionID = fmt.Sprintf("%s-%s", req.SessionID, jobID.String())
	}
	return t, jobID, ttsSessionID
}
