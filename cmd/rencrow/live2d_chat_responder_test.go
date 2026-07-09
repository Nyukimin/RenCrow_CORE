package main

import (
	"context"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
)

type stubLive2DOrch struct {
	req orchestrator.ProcessMessageRequest
}

func (s *stubLive2DOrch) ProcessMessage(_ context.Context, req orchestrator.ProcessMessageRequest) (orchestrator.ProcessMessageResponse, error) {
	s.req = req
	return orchestrator.ProcessMessageResponse{Response: "llm response", Route: routing.RouteCHAT}, nil
}

func TestLive2DOrchestratorResponderUsesViewerLive2DChannel(t *testing.T) {
	orch := &stubLive2DOrch{}
	responder := &live2DOrchestratorResponder{orch: orch}

	resp, err := responder.RespondLive2DChat(context.Background(), "session-1", "mio", "こんにちは")
	if err != nil {
		t.Fatalf("RespondLive2DChat() error = %v", err)
	}
	if resp != "llm response" {
		t.Fatalf("response=%q", resp)
	}
	if orch.req.SessionID != "session-1" || orch.req.Channel != "viewer_live2d" || orch.req.ChatID != "mio" || orch.req.UserMessage != "こんにちは" {
		t.Fatalf("request=%#v", orch.req)
	}
}
