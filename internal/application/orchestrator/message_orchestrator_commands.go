package orchestrator

import (
	"context"
	"fmt"
)

type messageEventEmitter func(eventType, from, to, content, route, jobID, sessionID, channel, chatID string)

type preRoutingCommandHandler struct {
	mio       MioAgent
	emit      messageEventEmitter
	responses messageResponseAssembler
}

func newPreRoutingCommandHandler(mio MioAgent, emit messageEventEmitter, responses messageResponseAssembler) *preRoutingCommandHandler {
	return &preRoutingCommandHandler{
		mio:       mio,
		emit:      emit,
		responses: responses,
	}
}

func (h *preRoutingCommandHandler) Handle(ctx context.Context, req ProcessMessageRequest) (ProcessMessageResponse, bool, error) {
	cmdResult, err := h.mio.HandleChatCommand(ctx, req.ChatID, req.UserMessage)
	if err != nil {
		return ProcessMessageResponse{}, false, fmt.Errorf("chat command failed: %w", err)
	}
	if !cmdResult.Handled {
		return ProcessMessageResponse{}, false, nil
	}
	jobID := resolveProcessMessageJobID(req.JobID)
	h.emit("agent.response", "mio", "user", cmdResult.Response, "CHAT", jobID.String(), req.SessionID, req.Channel, req.ChatID)
	return h.responses.BuildChatCommand(cmdResult.Response, jobID), true, nil
}
