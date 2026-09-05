package orchestrator

import (
	"context"
	"fmt"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type messageEventEmitter func(eventType, from, to, content, route, taskID, sessionID, channel, chatID string)

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
		// The canonical Mio implementation only returns an error after a command
		// was recognized. Preserve that handled state so the outer lifecycle can
		// route, assign, start, and then fail the durable Task.
		return ProcessMessageResponse{}, true, fmt.Errorf("chat command failed: %w", err)
	}
	if !cmdResult.Handled {
		return ProcessMessageResponse{}, false, nil
	}
	taskID, err := modulecore.ParseTaskID(req.RootTaskID)
	if err != nil {
		return ProcessMessageResponse{}, false, fmt.Errorf("invalid root_task_id: %w", err)
	}
	return h.responses.BuildChatCommand(cmdResult.Response, taskID), true, nil
}

func (h *preRoutingCommandHandler) EmitResponse(req ProcessMessageRequest, response string, taskID modulecore.TaskID) {
	h.emit("agent.response", "mio", "user", response, "CHAT", taskID.String(), req.SessionID, req.Channel, req.ChatID)
}
