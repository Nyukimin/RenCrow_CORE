package chat

import "testing"

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

func TestForecastCoderAlias(t *testing.T) {
	if got := ForecastCoderAlias(" Coder4 "); got != "coder4" {
		t.Fatalf("ForecastCoderAlias(Coder4) = %q", got)
	}
	if got := ForecastCoderAlias("Coder5"); got != "" {
		t.Fatalf("ForecastCoderAlias(Coder5) = %q", got)
	}
}

func TestBuildForecastProviderPlansKeepsEnabledPriority(t *testing.T) {
	plans := BuildForecastProviderPlans([]ForecastCoderCandidate{
		{Label: "Coder1", Coder: IdleChatCoderProviderConfig{Enabled: false}},
		{Label: "Coder2", Coder: IdleChatCoderProviderConfig{Enabled: true}},
		{Label: "Coder3", Coder: IdleChatCoderProviderConfig{Enabled: true}},
	})

	if len(plans) != 2 {
		t.Fatalf("plans len = %d, want 2: %#v", len(plans), plans)
	}
	if plans[0].Label != "Coder2" || !plans[0].Allowed || plans[0].SkipReason != "" {
		t.Fatalf("first plan = %#v", plans[0])
	}
	if plans[0].ProviderLabel != "Coder2 via RenCrow_LLM" {
		t.Fatalf("first provider label = %q", plans[0].ProviderLabel)
	}
	if plans[1].Label != "Coder3" || !plans[1].Allowed {
		t.Fatalf("second plan = %#v", plans[1])
	}
}

func TestForecastProviderLabels(t *testing.T) {
	if got := ForecastProviderLogLabel(" "); got != "unavailable" {
		t.Fatalf("empty log label = %q", got)
	}
	if got := BuildForecastProviderLabel("Coder2"); got != "Coder2 via RenCrow_LLM" {
		t.Fatalf("provider label = %q", got)
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
