package tts

import (
	"math"
	"strings"
)

type EmotionInput struct {
	Event        string
	Text         string
	Context      EmotionContext
	VoiceProfile string
}

type vectorDelta struct {
	name  string
	apply func(v *EmotionVector)
}

func PlanEmotion(in EmotionInput) EmotionState {
	event := strings.TrimSpace(strings.ToLower(in.Event))
	if event == "" {
		event = "system_notification"
	}
	v := baseVectorForEvent(event)
	appliedContext := applyContextRules(&v, in.Context)
	appliedText := applyTextFeatures(&v, in.Text)
	applyVoiceProfileBias(&v, in.VoiceProfile)
	normalizeVector(&v)
	primary := derivePrimaryEmotion(v)
	prosody := deriveProsody(v, in.Context)
	return EmotionState{
		PrimaryEmotion: primary,
		EmotionVector:  v,
		Prosody:        prosody,
		ReasonTrace: ReasonTrace{
			Event:               event,
			AppliedContextRules: appliedContext,
			AppliedTextFeatures: appliedText,
			VoiceProfile:        strings.TrimSpace(in.VoiceProfile),
		},
	}
}

func baseVectorForEvent(event string) EmotionVector {
	switch event {
	case "task_success":
		return EmotionVector{Warmth: 0.55, Cheerfulness: 0.70, Seriousness: 0.20, Alertness: 0.15, Calmness: 0.55, Expressiveness: 0.45}
	case "task_failure":
		return EmotionVector{Warmth: 0.35, Cheerfulness: 0.10, Seriousness: 0.70, Alertness: 0.50, Calmness: 0.25, Expressiveness: 0.25}
	case "warning", "error":
		return EmotionVector{Warmth: 0.20, Cheerfulness: 0.05, Seriousness: 0.75, Alertness: 0.82, Calmness: 0.18, Expressiveness: 0.30}
	case "analysis_report":
		return EmotionVector{Warmth: 0.38, Cheerfulness: 0.15, Seriousness: 0.62, Alertness: 0.25, Calmness: 0.55, Expressiveness: 0.18}
	case "conversation":
		return EmotionVector{Warmth: 0.68, Cheerfulness: 0.32, Seriousness: 0.22, Alertness: 0.15, Calmness: 0.72, Expressiveness: 0.34}
	default:
		return EmotionVector{Warmth: 0.45, Cheerfulness: 0.18, Seriousness: 0.42, Alertness: 0.20, Calmness: 0.58, Expressiveness: 0.20}
	}
}

func applyContextRules(v *EmotionVector, ctx EmotionContext) []string {
	var applied []string
	if ctx.UserWaitingTimeSec >= 20 {
		v.Warmth += 0.08
		v.Calmness += 0.05
		applied = append(applied, "user_waiting")
	}
	if strings.EqualFold(ctx.TimeOfDay, "night") {
		v.Calmness += 0.06
		v.Alertness -= 0.04
		applied = append(applied, "night")
	}
	if ctx.RetryCount > 0 {
		v.Seriousness += 0.05
		applied = append(applied, "retry")
	}
	if ctx.ConsecutiveFailures > 0 {
		v.Alertness += 0.08
		v.Calmness -= 0.05
		applied = append(applied, "consecutive_failures")
	}
	if strings.EqualFold(ctx.Urgency, "high") {
		v.Alertness += 0.10
		v.Calmness -= 0.05
		applied = append(applied, "high_urgency")
	}
	if ctx.UserAttentionRequired {
		v.Alertness += 0.08
		v.Seriousness += 0.04
		applied = append(applied, "user_attention_required")
	}
	if strings.EqualFold(ctx.PreviousEvent, "task_failure") && strings.EqualFold(ctx.ConversationMode, "chat") {
		v.Warmth += 0.04
		applied = append(applied, "post_failure_softening")
	}
	return applied
}

func applyTextFeatures(v *EmotionVector, text string) []string {
	lower := strings.ToLower(text)
	var applied []string
	apply := func(name string, fn func()) {
		fn()
		applied = append(applied, name)
	}
	if containsAny(lower, "ありがとう", "ありがとうございます", "thank you") {
		apply("gratitude", func() {
			v.Warmth += 0.04
			v.Cheerfulness += 0.02
		})
	}
	if containsAny(lower, "すみません", "申し訳", "ごめん") {
		apply("apology", func() {
			v.Seriousness += 0.04
			v.Calmness += 0.02
		})
	}
	if containsAny(lower, "問題ありません", "了解", "承知", "ok") {
		apply("confirmation", func() {
			v.Calmness += 0.04
			v.Warmth += 0.02
		})
	}
	if containsAny(lower, "注意", "警告", "warning", "危険") {
		apply("warning_phrase", func() {
			v.Alertness += 0.06
			v.Seriousness += 0.03
		})
	}
	if containsAny(lower, "完了", "成功", "できました", "done", "finished") {
		apply("success_phrase", func() {
			v.Cheerfulness += 0.05
		})
	}
	if containsAny(lower, "かもしれ", "おそらく", "たぶん", "maybe") {
		apply("uncertainty_phrase", func() {
			v.Seriousness += 0.03
			v.Expressiveness -= 0.02
		})
	}
	if containsAny(lower, "おはよう", "こんにちは", "こんばんは", "hello", "hi") {
		apply("greeting", func() {
			v.Warmth += 0.03
			v.Cheerfulness += 0.02
		})
	}
	return applied
}

func applyVoiceProfileBias(v *EmotionVector, profile string) {
	switch strings.TrimSpace(strings.ToLower(profile)) {
	case "lumina_female":
		v.Warmth += 0.05
		v.Calmness += 0.03
		v.Expressiveness += 0.02
		if v.Alertness > 0.78 {
			v.Alertness = 0.78
		}
	default:
	}
}

func normalizeVector(v *EmotionVector) {
	v.Warmth = clamp01(v.Warmth)
	v.Cheerfulness = clamp01(v.Cheerfulness)
	v.Seriousness = clamp01(v.Seriousness)
	v.Alertness = clamp01(v.Alertness)
	v.Calmness = clamp01(v.Calmness)
	v.Expressiveness = clamp01(v.Expressiveness)
	if v.Alertness > 0.65 {
		v.Calmness = math.Min(v.Calmness, 0.45)
	}
	if v.Cheerfulness > 0.55 {
		v.Seriousness = math.Min(v.Seriousness, 0.45)
	}
	if v.Seriousness > 0.60 {
		v.Expressiveness = math.Min(v.Expressiveness, 0.35)
	}
}

func derivePrimaryEmotion(v EmotionVector) string {
	switch {
	case v.Alertness >= 0.65:
		return "alert"
	case v.Seriousness >= 0.60:
		return "serious"
	case v.Cheerfulness >= 0.55:
		return "cheerful"
	case v.Warmth >= 0.50:
		return "warm"
	default:
		return "calm"
	}
}

func deriveProsody(v EmotionVector, ctx EmotionContext) Prosody {
	speed := 0.48 + (v.Alertness * 0.10) - (v.Calmness * 0.05)
	if strings.EqualFold(ctx.TimeOfDay, "night") {
		speed -= 0.03
	}
	pitch := 0.50 + (v.Warmth * 0.04) + (v.Cheerfulness * 0.03)
	pause := 0.50 + (v.Calmness * 0.08) - (v.Alertness * 0.10)
	return Prosody{
		Speed:          clamp01(speed),
		Pitch:          clamp01(pitch),
		Pause:          clamp01(pause),
		Expressiveness: clamp01(v.Expressiveness),
	}
}

func containsAny(s string, patterns ...string) bool {
	for _, p := range patterns {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
