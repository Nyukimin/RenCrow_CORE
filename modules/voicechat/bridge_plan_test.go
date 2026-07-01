package voicechat

import "testing"

func TestBuildBridgePlanDisabledWhenFeatureOff(t *testing.T) {
	plan := BuildBridgePlan(false, "ws://llm/v1/chat/audio/sessions", "")
	if plan.Enabled || plan.Available || !plan.Disabled {
		t.Fatalf("unexpected plan: %#v", plan)
	}
}

func TestBuildBridgePlanUnavailableWhenGatewayMissing(t *testing.T) {
	plan := BuildBridgePlan(true, " ", "vds_sub")
	if !plan.Enabled || plan.Available || plan.GatewayURL != "" {
		t.Fatalf("unexpected plan: %#v", plan)
	}
	if plan.InputMode != VoiceInputModeVDSSub {
		t.Fatalf("unexpected input mode: %q", plan.InputMode)
	}
}

func TestBuildBridgePlanAvailableWhenEnabledAndGatewayConfigured(t *testing.T) {
	plan := BuildBridgePlan(true, "ws://llm/v1/chat/audio/sessions", "parallel_caption")
	if !plan.Enabled || !plan.Available || plan.Disabled {
		t.Fatalf("unexpected plan: %#v", plan)
	}
	if plan.InputMode != VoiceInputModeParallelCaption {
		t.Fatalf("unexpected input mode: %q", plan.InputMode)
	}
}
