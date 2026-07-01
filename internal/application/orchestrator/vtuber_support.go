package orchestrator

import (
	"context"
	"log"
	"math"
	"strings"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/routing"
	moduletts "github.com/Nyukimin/picoclaw_multiLLM/modules/tts"
)

func buildVTuberRequest(
	eventType string,
	route routing.Route,
	sessionID string,
	text string,
	ctx moduletts.EmotionContext,
	voiceProfile string,
) (VTuberEmotionRequest, bool) {
	filtered, emotion := buildTTSPayload(eventType, route, text, ctx, voiceProfile)
	if filtered == "" || emotion == nil {
		return VTuberEmotionRequest{}, false
	}
	characterID := speakerForRoute(route)
	req := VTuberEmotionRequest{
		CharacterID:  characterID,
		Text:         filtered,
		Speaking:     true,
		Valence:      vtuberValence(emotion),
		Arousal:      vtuberArousal(emotion),
		Intensity:    vtuberIntensity(emotion),
		EmotionLabel: vtuberEmotionLabel(emotion),
		EventType:    eventType,
		Route:        route.String(),
		SessionID:    sessionID,
	}
	return req, true
}

func pushVTuber(ctx context.Context, bridge VTuberBridge, req VTuberEmotionRequest, prefix string) {
	if bridge == nil || strings.TrimSpace(req.CharacterID) == "" {
		return
	}
	if err := bridge.PublishEmotion(ctx, req); err != nil {
		log.Printf("%s %v", prefix, err)
	}
}

func vtuberValence(state *moduletts.EmotionState) float64 {
	if state == nil {
		return 0
	}
	v := state.EmotionVector
	pos := (v.Warmth * 0.45) + (v.Cheerfulness * 0.55)
	neg := (v.Seriousness * 0.65) + (v.Alertness * 0.15)
	return clampRange((pos-neg)*1.4, -1, 1)
}

func vtuberArousal(state *moduletts.EmotionState) float64 {
	if state == nil {
		return 0
	}
	v := state.EmotionVector
	return clampRange((v.Alertness*0.7)+(v.Expressiveness*0.3), 0, 1)
}

func vtuberIntensity(state *moduletts.EmotionState) float64 {
	if state == nil {
		return 0
	}
	valenceMagnitude := math.Abs(vtuberValence(state))
	v := state.EmotionVector
	raw := (valenceMagnitude * 0.35) + (v.Alertness * 0.25) + (v.Expressiveness * 0.20) + (v.Seriousness * 0.20)
	return clampRange(raw, 0, 1)
}

func vtuberEmotionLabel(state *moduletts.EmotionState) string {
	if state == nil {
		return "neutral"
	}
	v := state.EmotionVector
	switch {
	case v.Cheerfulness >= 0.55:
		return "happy"
	case v.Alertness >= 0.70:
		return "surprised"
	case v.Seriousness >= 0.62 && v.Calmness < 0.32:
		return "annoyed"
	case v.Seriousness >= 0.58:
		return "thinking"
	case v.Calmness >= 0.62 || v.Warmth >= 0.52:
		return "calm"
	default:
		return "neutral"
	}
}

func clampRange(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
