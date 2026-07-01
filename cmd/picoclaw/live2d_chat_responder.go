package main

import (
	"context"
	"strings"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/orchestrator"
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
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		sessionID = "viewer_live2d:" + strings.ToLower(strings.TrimSpace(characterID))
	}
	resp, err := r.orch.ProcessMessage(ctx, orchestrator.ProcessMessageRequest{
		SessionID:   sessionID,
		Channel:     "viewer_live2d",
		ChatID:      strings.ToLower(strings.TrimSpace(characterID)),
		UserMessage: strings.TrimSpace(message),
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Response), nil
}
