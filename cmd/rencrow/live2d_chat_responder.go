package main

import (
	"context"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
)

type live2DOrchestratorResponder struct {
	orch interface {
		ProcessMessage(context.Context, orchestrator.ProcessMessageRequest) (orchestrator.ProcessMessageResponse, error)
	}
}

func newLive2DOrchestratorResponder(deps *Dependencies) *live2DOrchestratorResponder {
	if deps == nil {
		return nil
	}
	if responder, ok := deps.live2DChatResponder.(*live2DOrchestratorResponder); ok {
		return responder
	}
	return nil
}

func (r *live2DOrchestratorResponder) RespondLive2DChat(ctx context.Context, sessionID string, characterID string, message string) (string, error) {
	if r == nil || r.orch == nil {
		return "", nil
	}
	recipient := strings.ToLower(strings.TrimSpace(characterID))
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		sessionID = "viewer_live2d:" + recipient
	}
	resp, err := r.orch.ProcessMessage(ctx, orchestrator.ProcessMessageRequest{
		SessionID:   sessionID,
		Channel:     "viewer_live2d",
		ChatID:      recipient,
		To:          recipient,
		UserMessage: strings.TrimSpace(message),
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Response), nil
}
