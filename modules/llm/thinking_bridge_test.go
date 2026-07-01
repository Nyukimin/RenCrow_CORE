package llm

import "testing"

func TestApplyThinkingBridgeFields(t *testing.T) {
	req := map[string]interface{}{}
	ApplyThinkingBridgeFields(req, true, true)
	if req["parse_reasoning"] != true || req["include_reasoning"] != false || req["separate_reasoning"] != true || req["stream"] != true {
		t.Fatalf("ApplyThinkingBridgeFields() = %#v", req)
	}
}

func TestApplyThinkingBridgeProviderOptionsSkipsReservedKeys(t *testing.T) {
	req := map[string]interface{}{"model": "Worker"}
	ApplyThinkingBridgeProviderOptions(req, true, map[string]any{
		"think":       false,
		"model":       "BadOverride",
		" max_tokens": 100,
		"":            "ignored",
	})
	if req["think"] != false {
		t.Fatalf("ApplyThinkingBridgeProviderOptions() missing think=false: %#v", req)
	}
	if req["model"] != "Worker" {
		t.Fatalf("reserved model was overwritten: %#v", req)
	}
	if _, ok := req["max_tokens"]; ok {
		t.Fatalf("reserved max_tokens should be skipped: %#v", req)
	}
}

func TestSanitizeThinkingBridgeContentExtractsFinalAnswer(t *testing.T) {
	content := "Okay, the user is asking for a confirmation message in Japanese. Let me check the query again.\n\nFinal answer: 了解しました。"
	got := SanitizeThinkingBridgeContent(true, content, "no_reasoning")
	if got != "了解しました。" {
		t.Fatalf("SanitizeThinkingBridgeContent() = %q", got)
	}
}

func TestSanitizeThinkingBridgeContentDropsReasoningOnly(t *testing.T) {
	content := "Okay, the user is asking for a confirmation message. Let me check the query again. They wrote hello."
	if got := SanitizeThinkingBridgeContent(true, content, "no_reasoning"); got != "" {
		t.Fatalf("SanitizeThinkingBridgeContent() = %q, want empty", got)
	}
}

func TestSanitizeThinkingBridgeContentPreservesNormalContent(t *testing.T) {
	if got := SanitizeThinkingBridgeContent(true, "了解しました。", "no_reasoning"); got != "了解しました。" {
		t.Fatalf("SanitizeThinkingBridgeContent() = %q", got)
	}
	if got := SanitizeThinkingBridgeContent(false, "Okay, the user is asking. Final answer: hi", "no_reasoning"); got != "Okay, the user is asking. Final answer: hi" {
		t.Fatalf("disabled SanitizeThinkingBridgeContent() = %q", got)
	}
}
