package tts

import (
	"testing"
	"time"
)

// 不変条件: 読み上げる発話の声は、その発話の agent.response.from の Agent の声である。
// TTS要求の voice_id は Gateway の公開 voice_id（= Agent 名）で、別名や既定へ丸めない。

func TestVoiceForAgentReturnsGatewayVoiceIDOfEachAgent(t *testing.T) {
	cases := []struct {
		agent       string
		wantVoiceID string
		wantProfile string
	}{
		{"mio", "mio", "lumina_female"},
		{"midori", "midori", "lumina_female"},
		{"shiro", "shiro", "lumina_male"},
		{"kuro", "kuro", "lumina_male"},
		{" Kuro ", "kuro", "lumina_male"},
		{"MIDORI", "midori", "lumina_female"},
	}
	for _, tc := range cases {
		t.Run(tc.agent, func(t *testing.T) {
			voiceID, profile, ok := VoiceForAgent(tc.agent)
			if !ok || voiceID != tc.wantVoiceID || profile != tc.wantProfile {
				t.Fatalf("VoiceForAgent(%q) = (%q, %q, %v), want (%q, %q, true)", tc.agent, voiceID, profile, ok, tc.wantVoiceID, tc.wantProfile)
			}
		})
	}
}

func TestVoiceForAgentRejectsNonAgentIdentities(t *testing.T) {
	// "wild" は route 名由来の旧話者名、male_01/female_01 は旧別名で、いずれも Agent ではない。
	for _, agent := range []string{"", "  ", "wild", "user", "male_01", "female_01", "coder1", "unknown"} {
		t.Run(agent, func(t *testing.T) {
			if voiceID, profile, ok := VoiceForAgent(agent); ok {
				t.Fatalf("VoiceForAgent(%q) = (%q, %q, true), want rejection", agent, voiceID, profile)
			}
		})
	}
}

func TestRespondingAgentForRouteMatchesAgentResponseFrom(t *testing.T) {
	cases := []struct {
		route     string
		recipient string
		want      string
	}{
		{"CHAT", "mio", "mio"},
		{"CHAT", "shiro", "shiro"},
		{"CHAT", "midori", "midori"},
		{"CHAT", "kuro", "kuro"},
		{"chat", " Kuro ", "kuro"},
		{"CHAT", "", "mio"},
		{"PLAN", "shiro", "mio"},
		{"RESEARCH", "kuro", "mio"},
		{"WILD", "mio", "midori"},
		{"ANALYZE", "mio", "kuro"},
		{"OPS", "mio", "shiro"},
		{"CODE", "mio", "shiro"},
		{"CODE1", "", "shiro"},
		{"CODE2", "", "shiro"},
		{"CODE3", "", "shiro"},
		{"CODE4", "", "shiro"},
	}
	for _, tc := range cases {
		t.Run(tc.route+"/"+tc.recipient, func(t *testing.T) {
			got, ok := RespondingAgentForRoute(tc.route, tc.recipient)
			if !ok || got != tc.want {
				t.Fatalf("RespondingAgentForRoute(%q, %q) = (%q, %v), want (%q, true)", tc.route, tc.recipient, got, ok, tc.want)
			}
		})
	}
}

func TestRespondingAgentForRouteRejectsUnknownRouteAndRecipient(t *testing.T) {
	cases := []struct{ route, recipient string }{
		{"", ""},
		{"UNKNOWN", ""},
		{"CHAT", "coder1"},
		{"CHAT", "wild"},
	}
	for _, tc := range cases {
		t.Run(tc.route+"/"+tc.recipient, func(t *testing.T) {
			if got, ok := RespondingAgentForRoute(tc.route, tc.recipient); ok {
				t.Fatalf("RespondingAgentForRoute(%q, %q) = (%q, true), want rejection", tc.route, tc.recipient, got)
			}
		})
	}
}

func TestBuildRouteTTSPlanUsesRespondingAgentVoiceForEveryRoute(t *testing.T) {
	cases := []struct {
		route       string
		recipient   string
		wantAgent   string
		wantProfile string
	}{
		{"CHAT", "mio", "mio", "lumina_female"},
		{"CHAT", "shiro", "shiro", "lumina_male"},
		{"CHAT", "midori", "midori", "lumina_female"},
		{"CHAT", "kuro", "kuro", "lumina_male"},
		{"PLAN", "", "mio", "lumina_female"},
		{"RESEARCH", "", "mio", "lumina_female"},
		{"WILD", "", "midori", "lumina_female"},
		{"ANALYZE", "", "kuro", "lumina_male"},
		{"OPS", "", "shiro", "lumina_male"},
		{"CODE", "", "shiro", "lumina_male"},
	}
	for _, tc := range cases {
		t.Run(tc.route+"/"+tc.recipient, func(t *testing.T) {
			got, ok := BuildRouteTTSPlan(RouteTTSPlanInput{
				Route:             tc.route,
				SessionID:         "tts-1",
				PublicPlaybackRef: "job-1",
				ChatCharacterID:   tc.recipient,
				Now:               time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC),
			})
			if !ok {
				t.Fatal("expected plan")
			}
			if got.CharacterID != tc.wantAgent || got.VoiceID != tc.wantAgent || got.VoiceProfile != tc.wantProfile {
				t.Fatalf("plan identity = character=%q voice=%q profile=%q, want agent=%q profile=%q", got.CharacterID, got.VoiceID, got.VoiceProfile, tc.wantAgent, tc.wantProfile)
			}
		})
	}
}

func TestBuildRouteTTSPlanDoesNotFallBackToMioForUnresolvedSpeaker(t *testing.T) {
	cases := []RouteTTSPlanInput{
		{Route: "UNKNOWN", SessionID: "tts-1", PublicPlaybackRef: "job-1"},
		{Route: "CHAT", ChatCharacterID: "coder1", SessionID: "tts-1", PublicPlaybackRef: "job-1"},
	}
	for _, input := range cases {
		if got, ok := BuildRouteTTSPlan(input); ok {
			t.Fatalf("BuildRouteTTSPlan(%+v) = %+v, want rejection instead of a default voice", input, got)
		}
	}
}
