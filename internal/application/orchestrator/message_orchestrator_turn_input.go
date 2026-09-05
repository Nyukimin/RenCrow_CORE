package orchestrator

import (
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type ttsEnabledFunc func() bool

// messageTurnInputBuilder owns the single construction boundary for text
// ingress. RootTaskID is the sole execution identity until routing optionally
// creates a child Task for another CORE Agent.
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

func (b *messageTurnInputBuilder) Build(req ProcessMessageRequest) (conversation.TurnInput, modulecore.TaskID, string, error) {
	taskID, err := modulecore.ParseTaskID(req.RootTaskID)
	if err != nil {
		return conversation.TurnInput{}, "", "", fmt.Errorf("invalid root_task_id: %w", err)
	}
	return b.BuildWithTaskID(req, taskID)
}

// buildTurnInputFromProcessRequest reconstructs the exact identities assigned
// by the request boundary. It deliberately does not derive any identity from
// another execution identity or generate replacement conversation IDs.
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

func (b *messageTurnInputBuilder) BuildWithTaskID(req ProcessMessageRequest, taskID modulecore.TaskID) (conversation.TurnInput, modulecore.TaskID, string, error) {
	input, err := buildTurnInputFromProcessRequest(req)
	if err != nil {
		return conversation.TurnInput{}, taskID, "", err
	}
	if len(req.Attachments) > 0 {
		b.emit("viewer.attachment.received", "viewer", "mio",
			fmt.Sprintf("%d attachment(s)", len(req.Attachments)),
			"", taskID.String(), req.SessionID, req.Channel, req.ChatID)
	}
	ttsSessionID := ""
	if b.ttsEnabled() && ttsAllowedForRequest(req) {
		ttsSessionID = fmt.Sprintf("%s-%s", req.SessionID, taskID.String())
	}
	return input, taskID, ttsSessionID, nil
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
