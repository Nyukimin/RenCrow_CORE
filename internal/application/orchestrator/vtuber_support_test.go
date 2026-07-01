package orchestrator

import (
	"testing"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/routing"
	moduletts "github.com/Nyukimin/picoclaw_multiLLM/modules/tts"
)

func TestBuildVTuberRequest_ChatRouteUsesMio(t *testing.T) {
	req, ok := buildVTuberRequest("agent.response", routing.RouteCHAT, "sess1", "ありがとう、完了しました。", moduletts.EmotionContext{}, "lumina_female")
	if !ok {
		t.Fatalf("expected vtuber request")
	}
	if req.CharacterID != "mio" {
		t.Fatalf("expected mio, got %s", req.CharacterID)
	}
	if req.EmotionLabel != "happy" && req.EmotionLabel != "calm" {
		t.Fatalf("expected positive label, got %s", req.EmotionLabel)
	}
	if req.Valence <= 0 {
		t.Fatalf("expected positive valence, got %f", req.Valence)
	}
}

func TestBuildVTuberRequest_OpsRouteUsesShiro(t *testing.T) {
	ctx := moduletts.EmotionContext{Urgency: "high", UserAttentionRequired: true}
	req, ok := buildVTuberRequest("agent.response", routing.RouteOPS, "sess2", "警告です。注意してください。", ctx, "lumina_male")
	if !ok {
		t.Fatalf("expected vtuber request")
	}
	if req.CharacterID != "shiro" {
		t.Fatalf("expected shiro, got %s", req.CharacterID)
	}
	if req.Arousal <= 0.3 {
		t.Fatalf("expected elevated arousal, got %f", req.Arousal)
	}
	if req.EmotionLabel != "surprised" && req.EmotionLabel != "annoyed" && req.EmotionLabel != "thinking" {
		t.Fatalf("unexpected emotion label %s", req.EmotionLabel)
	}
}
