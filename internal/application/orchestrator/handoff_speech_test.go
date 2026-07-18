package orchestrator

import (
	"strings"
	"testing"
)

func TestAgentHandoffSpeechNamesRecipientAndCarriesWorkAndConversation(t *testing.T) {
	got := formatAgentHandoffSpeech("mio", "shiro", "OPSとして設定を確認する", "TTSが起動しないという相談")
	if !strings.HasPrefix(got, "Shiro、") {
		t.Fatalf("handoff must begin with recipient name: %q", got)
	}
	for _, want := range []string{"移譲内容", "OPSとして設定を確認する", "会話内容", "TTSが起動しないという相談"} {
		if !strings.Contains(got, want) {
			t.Fatalf("handoff missing %q: %q", want, got)
		}
	}
}

func TestAgentHandoffReadbackNamesDelegatorAndRepeatsWorkAndConversation(t *testing.T) {
	got := formatAgentHandoffReadbackSpeech("mio", "shiro", "OPSとして設定を確認する", "TTSが起動しないという相談")
	if !strings.HasPrefix(got, "Mio、") {
		t.Fatalf("readback must begin with delegator name: %q", got)
	}
	for _, want := range []string{"復唱", "移譲内容", "OPSとして設定を確認する", "会話内容", "TTSが起動しないという相談"} {
		if !strings.Contains(got, want) {
			t.Fatalf("readback missing %q: %q", want, got)
		}
	}
}

func TestAgentHandoffCompletionNamesDelegatorBeforeReport(t *testing.T) {
	got := formatAgentHandoffCompletionSpeech("mio", "shiro", "設定確認が完了しました")
	if !strings.HasPrefix(got, "Mio、") {
		t.Fatalf("completion must begin with delegator name: %q", got)
	}
	if !strings.Contains(got, "設定確認が完了しました") {
		t.Fatalf("completion missing report: %q", got)
	}
}

func TestAgentHandoffSpeechUsesCharacterNames(t *testing.T) {
	cases := map[string]string{
		"mio":    "Mio",
		"shiro":  "Shiro",
		"wild":   "Midori",
		"heavy":  "Kuro",
		"coder1": "Coder1",
	}
	for agentID, want := range cases {
		if got := handoffAgentName(agentID); got != want {
			t.Fatalf("handoffAgentName(%q)=%q want=%q", agentID, got, want)
		}
	}
}
