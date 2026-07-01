package chat

import "testing"

func TestCoderProviderIsExternal(t *testing.T) {
	tests := []struct {
		provider string
		want     bool
	}{
		{provider: "local_openai", want: false},
		{provider: "ollama", want: false},
		{provider: "openai", want: true},
		{provider: "claude", want: true},
		{provider: "deepseek", want: true},
	}
	for _, tt := range tests {
		if got := CoderProviderIsExternal(tt.provider); got != tt.want {
			t.Fatalf("CoderProviderIsExternal(%q)=%t, want %t", tt.provider, got, tt.want)
		}
	}
}

func TestForecastCoderLabelIndex(t *testing.T) {
	if got := ForecastCoderLabelIndex("Coder1"); got != 0 {
		t.Fatalf("ForecastCoderLabelIndex(Coder1) = %d", got)
	}
	if got := ForecastCoderLabelIndex(" Coder4 "); got != 3 {
		t.Fatalf("ForecastCoderLabelIndex(Coder4) = %d", got)
	}
	if got := ForecastCoderLabelIndex("Coder5"); got != -1 {
		t.Fatalf("ForecastCoderLabelIndex(Coder5) = %d", got)
	}
}

func TestForecastCoderProviderAllowedRequiresExplicitExternal(t *testing.T) {
	local := IdleChatCoderProviderConfig{Provider: "local_openai"}
	external := IdleChatCoderProviderConfig{Provider: "openai"}

	if !ForecastCoderProviderAllowed(local, false) {
		t.Fatal("local forecast coder should be allowed")
	}
	if ForecastCoderProviderAllowed(external, false) {
		t.Fatal("external forecast coder should require explicit enablement")
	}
	if !ForecastCoderProviderAllowed(external, true) {
		t.Fatal("external forecast coder should be allowed when explicitly enabled")
	}
}

func TestBuildForecastProviderPlansKeepsEnabledPriorityAndSkipsExternalWithoutOptIn(t *testing.T) {
	plans := BuildForecastProviderPlans([]ForecastCoderCandidate{
		{Label: "Coder1", Coder: IdleChatCoderProviderConfig{Enabled: false, Provider: "local_openai", Model: "disabled"}},
		{Label: "Coder2", Coder: IdleChatCoderProviderConfig{Enabled: true, Provider: "openai", Model: "gpt-4o-mini"}},
		{Label: "Coder3", Coder: IdleChatCoderProviderConfig{Enabled: true, Provider: "local_openai", Model: "Worker"}},
	}, false)

	if len(plans) != 2 {
		t.Fatalf("plans len = %d, want 2: %#v", len(plans), plans)
	}
	if plans[0].Label != "Coder2" || plans[0].Allowed || plans[0].SkipReason == "" {
		t.Fatalf("first plan should preserve priority and skip external: %#v", plans[0])
	}
	if plans[1].Label != "Coder3" || !plans[1].Allowed {
		t.Fatalf("second plan should be allowed local coder: %#v", plans[1])
	}
}

func TestBuildForecastProviderPlansAllowsExternalWhenExplicit(t *testing.T) {
	plans := BuildForecastProviderPlans([]ForecastCoderCandidate{
		{Label: "Coder2", Coder: IdleChatCoderProviderConfig{Enabled: true, Provider: "openai", Model: "gpt-4o-mini"}},
	}, true)

	if len(plans) != 1 || !plans[0].Allowed {
		t.Fatalf("external-enabled plans = %#v, want allowed external", plans)
	}
	if plans[0].ProviderLabel != "Coder2 openai (gpt-4o-mini)" {
		t.Fatalf("provider label = %q", plans[0].ProviderLabel)
	}
}

func TestForecastProviderLabels(t *testing.T) {
	if got := ForecastProviderLogLabel(" "); got != "unavailable" {
		t.Fatalf("empty log label = %q", got)
	}
	if got := ForecastProviderModelLabel(" "); got != "configured provider" {
		t.Fatalf("empty model label = %q", got)
	}
	label := BuildForecastProviderLabel("Coder2", IdleChatCoderProviderConfig{Provider: "openai", Model: "gpt-4o-mini"})
	if label != "Coder2 openai (gpt-4o-mini)" {
		t.Fatalf("provider label = %q", label)
	}
}

func TestIdleChatProviderOptionsKeepsThinkOnly(t *testing.T) {
	yes := true
	got := IdleChatProviderOptions(map[string]IdleChatLLMOptions{
		" Mio ": {Think: &yes},
		"Shiro": {},
	})
	if len(got) != 1 || got["mio"]["think"] != true {
		t.Fatalf("options = %+v", got)
	}
}
