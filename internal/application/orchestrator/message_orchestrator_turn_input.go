package orchestrator

import (
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type ttsEnabledFunc func() bool

// messageTurnInputBuilder owns the single construction boundary for text
// ingress. The conversation input carries only conversation identities; the
// legacy execution JobID remains an explicit companion value.
type messageTurnInputBuilder struct {
	emit       messageEventEmitter
	ttsEnabled ttsEnabledFunc
}

func newMessageTurnInputBuilder(emit messageEventEmitter, ttsEnabled ttsEnabledFunc) *messageTurnInputBuilder {
	return &messageTurnInputBuilder{
		emit:       emit,
		ttsEnabled: ttsEnabled,
	}
}

func (b *messageTurnInputBuilder) Build(req ProcessMessageRequest) (conversation.TurnInput, task.JobID, string, error) {
	jobID := resolveProcessMessageJobID(req.JobID)
	return b.BuildWithJobID(req, jobID)
}

func resolveProcessMessageJobID(raw string) task.JobID {
	if jobID := strings.TrimSpace(raw); jobID != "" {
		return task.JobIDFromString(jobID)
	}
	return task.NewJobID()
}

// buildTurnInputFromProcessRequest reconstructs the exact identities assigned
// by the request boundary. It deliberately does not derive any identity from
// the legacy JobID or generate replacement conversation IDs.
func buildTurnInputFromProcessRequest(req ProcessMessageRequest) (conversation.TurnInput, error) {
	address, err := conversation.NewChannelAddress(req.Channel, req.ChatID)
	if err != nil {
		return conversation.TurnInput{}, fmt.Errorf("build channel address: %w", err)
	}
	input, err := conversation.ReconstructTurnInput(
		modulecore.TaskID(req.RootTaskID),
		modulecore.TurnID(req.TurnID),
		modulecore.TraceID(req.TraceID),
		modulecore.MessageID(req.MessageID),
		modulecore.MessageID(req.AgentMessageID),
		req.UserMessage,
		address,
	)
	if err != nil {
		return conversation.TurnInput{}, fmt.Errorf("reconstruct turn input: %w", err)
	}
	return input.
		WithSessionID(req.SessionID).
		WithAttachments(req.Attachments).
		WithViewerRecipient(normalizeProcessViewerRecipient(req.To)), nil
}

func (b *messageTurnInputBuilder) BuildWithJobID(req ProcessMessageRequest, jobID task.JobID) (conversation.TurnInput, task.JobID, string, error) {
	input, err := buildTurnInputFromProcessRequest(req)
	if err != nil {
		return conversation.TurnInput{}, jobID, "", err
	}
	if len(req.Attachments) > 0 {
		b.emit("viewer.attachment.received", "viewer", "mio",
			fmt.Sprintf("%d attachment(s)", len(req.Attachments)),
			"", jobID.String(), req.SessionID, req.Channel, req.ChatID)
	}
	ttsSessionID := ""
	if b.ttsEnabled() && ttsAllowedForRequest(req) {
		ttsSessionID = fmt.Sprintf("%s-%s", req.SessionID, jobID.String())
	}
	return input, jobID, ttsSessionID, nil
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
