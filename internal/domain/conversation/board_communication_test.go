package conversation

import (
	"encoding/json"
	"testing"
)

func TestExternalPeerBindingKeepsWireKindSeparateFromClassification(t *testing.T) {
	payload := externalPeerPayloadFixture("", ExternalPeerClassificationRequest, "")
	binding, err := ParseExternalPeerBinding(map[string]any{"external_peer_binding": payload})
	if err != nil {
		t.Fatalf("ParseExternalPeerBinding(empty raw kind): %v", err)
	}
	if binding.WireKind != "" || binding.Classification != ExternalPeerClassificationRequest {
		t.Fatalf("parsed wire kind/classification = %q/%q, want empty/request", binding.WireKind, binding.Classification)
	}
	if binding.ResponderActorID != "" || binding.EffectiveResponderActorID() != "mio" {
		t.Fatalf("legacy responder actor = %q, effective %q; want omitted/mio", binding.ResponderActorID, binding.EffectiveResponderActorID())
	}
	if _, exists := binding.ToPayload()["responder_actor_id"]; exists {
		t.Fatal("legacy ToPayload() added responder_actor_id; want byte-compatible legacy shape")
	}
	serialized, err := json.Marshal(binding)
	if err != nil {
		t.Fatalf("json.Marshal(legacy binding): %v", err)
	}
	if string(serialized) != `{"board_instance_id":"test-instance","recipient_principal_id":"rencrow-comm","channel_id":"channel-a","thread_id":"thread-a","message_id":"board-message-1","sender_principal_id":"peer-a","wire_kind":"","classification":"request","reply_to":""}` {
		t.Fatalf("legacy binding serialization changed: %s", serialized)
	}
	if err := binding.ValidateNewRequest(); err != nil {
		t.Fatalf("ValidateNewRequest(empty raw kind): %v", err)
	}
	if got := binding.ResponderActorKind(); got != "agent" {
		t.Fatalf("legacy ResponderActorKind() = %q, want agent", got)
	}
	actorID, err := binding.SourceActorID()
	if err != nil || actorID != "board:test-instance:peer-a" {
		t.Fatalf("SourceActorID() = %q, %v; want board:test-instance:peer-a", actorID, err)
	}
}

func TestBoardSpeechCharacterIdentityIsExactAndBOARDScoped(t *testing.T) {
	for _, id := range []string{"aka", "ao", "kin", "gin", "lumina"} {
		if !IsBoardSpeechCharacter(id) {
			t.Errorf("IsBoardSpeechCharacter(%q) = false, want true", id)
		}
		if !IsExternalPeerResponderActor(id) {
			t.Errorf("IsExternalPeerResponderActor(%q) = false, want true", id)
		}
		binding := externalPeerBindingFixture("", ExternalPeerClassificationRequest, "")
		binding.ResponderActorID, binding.RecipientPrincipalID = id, id
		if got := binding.ResponderActorKind(); got != "board_character" {
			t.Errorf("ExternalPeerBinding{responder: %q}.ResponderActorKind() = %q, want board_character", id, got)
		}
		if err := binding.ValidateNewRequest(); err != nil {
			t.Errorf("ExternalPeerBinding{responder: %q}.ValidateNewRequest() = %v, want BOARD character support", id, err)
		}
	}

	for _, id := range []string{"", "mio", "shiro", "midori", "kuro", "heavy", "aka ", "Aka", "LUMINA", "lucina"} {
		if IsBoardSpeechCharacter(id) {
			t.Errorf("IsBoardSpeechCharacter(%q) = true, want false", id)
		}
	}
	for _, id := range []string{"mio", "heavy", "wild", "Aka", "aka ", "lucina"} {
		if IsExternalPeerResponderActor(id) {
			t.Errorf("IsExternalPeerResponderActor(%q) = true, want false", id)
		}
	}
}

func TestExternalPeerResponderActorIsExplicitTrustedBindingOnly(t *testing.T) {
	for _, test := range []struct {
		name          string
		actor         string
		recipient     string
		wantActor     string
		wantEffective string
		wantErr       bool
	}{
		{name: "shiro", actor: "shiro", recipient: "shiro", wantActor: "shiro", wantEffective: "shiro"},
		{name: "midori", actor: "midori", recipient: "midori", wantActor: "midori", wantEffective: "midori"},
		{name: "kuro chat responder", actor: "kuro", recipient: "kuro", wantActor: "kuro", wantEffective: "kuro"},
		{name: "aka BOARD character", actor: "aka", recipient: "aka", wantActor: "aka", wantEffective: "aka"},
		{name: "ao BOARD character", actor: "ao", recipient: "ao", wantActor: "ao", wantEffective: "ao"},
		{name: "kin BOARD character", actor: "kin", recipient: "kin", wantActor: "kin", wantEffective: "kin"},
		{name: "gin BOARD character", actor: "gin", recipient: "gin", wantActor: "gin", wantEffective: "gin"},
		{name: "lumina BOARD character", actor: "lumina", recipient: "lumina", wantActor: "lumina", wantEffective: "lumina"},
		{name: "legacy mio omission", recipient: "rencrow-comm", wantEffective: "mio"},
		{name: "explicit mio rejected", actor: "mio", recipient: "mio", wantErr: true},
		{name: "Heavy alias rejected", actor: "heavy", recipient: "heavy", wantErr: true},
		{name: "other actor rejected", actor: "wild", recipient: "wild", wantErr: true},
		{name: "case drift rejected", actor: "Shiro", recipient: "Shiro", wantErr: true},
		{name: "whitespace rejected", actor: "shiro ", recipient: "shiro ", wantErr: true},
		{name: "recipient mismatch rejected", actor: "shiro", recipient: "midori", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			binding := externalPeerBindingFixture("request", ExternalPeerClassificationRequest, "")
			binding.RecipientPrincipalID = test.recipient
			binding.ResponderActorID = test.actor
			payload := map[string]any{"external_peer_binding": binding.ToPayload()}
			parsed, err := ParseExternalPeerBinding(payload)
			if (err != nil) != test.wantErr {
				t.Fatalf("ParseExternalPeerBinding() error = %v, wantErr %t", err, test.wantErr)
			}
			if test.wantErr {
				return
			}
			if parsed.ResponderActorID != test.wantActor || parsed.EffectiveResponderActorID() != test.wantEffective {
				t.Fatalf("parsed responder actor = %q, effective %q; want %q/%q", parsed.ResponderActorID, parsed.EffectiveResponderActorID(), test.wantActor, test.wantEffective)
			}
		})
	}

	for _, value := range []any{nil, "", 7, true} {
		payload := externalPeerPayloadFixture("request", ExternalPeerClassificationRequest, "")
		payload["responder_actor_id"] = value
		if _, err := ParseExternalPeerBinding(map[string]any{"external_peer_binding": payload}); err == nil {
			t.Fatalf("ParseExternalPeerBinding(responder_actor_id=%#v) error = nil, want strict type rejection", value)
		}
	}
}

func TestExternalPeerBindingOnlyAllowsUncorrelatedRequestShape(t *testing.T) {
	for _, test := range []struct {
		name           string
		wireKind       string
		classification ExternalPeerClassification
		replyTo        string
		wantErr        bool
	}{
		{name: "empty wire kind request", wireKind: "", classification: ExternalPeerClassificationRequest},
		{name: "explicit request wire kind", wireKind: "request", classification: ExternalPeerClassificationRequest},
		{name: "reply wire kind", wireKind: "reply", classification: ExternalPeerClassificationRequest, wantErr: true},
		{name: "reply target", wireKind: "request", classification: ExternalPeerClassificationRequest, replyTo: "board-message-1", wantErr: true},
		{name: "self declared terminal", wireKind: "request", classification: ExternalPeerClassificationCorrelatedReplyTerminal, wantErr: true},
		{name: "unknown reply", wireKind: "", classification: ExternalPeerClassificationUnknownReply, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			binding := externalPeerBindingFixture(test.wireKind, test.classification, test.replyTo)
			err := binding.ValidateNewRequest()
			if (err != nil) != test.wantErr {
				t.Fatalf("ValidateNewRequest() error = %v, wantErr %t", err, test.wantErr)
			}
		})
	}
}

func TestExternalPeerBindingBrowserReplyFollowupPreservesWireMetadata(t *testing.T) {
	binding := externalPeerBindingFixture("reply", ExternalPeerClassification("browser_reply_followup"), "agent-reply-1")
	binding.MessageID = "browser-reply-2"
	binding.SenderPrincipalID = "ren-browser"
	binding.RecipientPrincipalID = "shiro"
	binding.ResponderActorID = "shiro"

	parsed, err := ParseExternalPeerBinding(map[string]any{"external_peer_binding": binding.ToPayload()})
	if err != nil {
		t.Fatalf("ParseExternalPeerBinding(browser followup): %v", err)
	}
	if !parsed.Equal(binding) || parsed.WireKind != "reply" || parsed.ReplyTo != "agent-reply-1" {
		t.Fatalf("parsed browser followup = %+v; want exact raw kind, reply target, and classification", parsed)
	}
	if err := parsed.ValidateNewRequest(); err == nil {
		t.Fatal("ValidateNewRequest(browser followup) = nil; reply-only followup must not be treated as a request")
	}

	for _, test := range []struct {
		name   string
		mutate func(*ExternalPeerBinding)
	}{
		{name: "non-browser sender", mutate: func(value *ExternalPeerBinding) { value.SenderPrincipalID = "grok" }},
		{name: "empty wire kind", mutate: func(value *ExternalPeerBinding) { value.WireKind = "" }},
		{name: "request wire kind", mutate: func(value *ExternalPeerBinding) { value.WireKind = "request" }},
		{name: "missing reply target", mutate: func(value *ExternalPeerBinding) { value.ReplyTo = "" }},
		{name: "recipient and responder differ", mutate: func(value *ExternalPeerBinding) { value.RecipientPrincipalID = "midori" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := binding
			test.mutate(&changed)
			if err := changed.Validate(); err == nil {
				t.Fatalf("Validate() = nil for malformed browser reply followup: %+v", changed)
			}
		})
	}
}

func TestParseExternalPeerBindingRejectsLegacyKindOnlyShape(t *testing.T) {
	payload := externalPeerPayloadFixture("request", ExternalPeerClassificationRequest, "")
	payload["kind"] = payload["wire_kind"]
	delete(payload, "wire_kind")
	delete(payload, "classification")
	if _, err := ParseExternalPeerBinding(map[string]any{"external_peer_binding": payload}); err == nil {
		t.Fatal("ParseExternalPeerBinding(legacy kind-only payload) error = nil, want strict schema rejection")
	}
}

func TestExternalPeerBindingEqualityAndContextUseDistinctFields(t *testing.T) {
	left := externalPeerBindingFixture("", ExternalPeerClassificationRequest, "")
	right := left
	right.MessageID = "board-message-2"
	right.WireKind = "request"
	if left.Equal(right) {
		t.Fatal("Equal() ignored source-message and raw-kind differences")
	}
	withResponder := left
	withResponder.ResponderActorID = "shiro"
	if left.Equal(withResponder) {
		t.Fatal("Equal() ignored the explicit responder actor")
	}
	if !left.SameContext(right) {
		t.Fatalf("SameContext() rejected the same BOARD thread tuple: left=%+v right=%+v", left, right)
	}
	contextWithResponder := left
	contextWithResponder.ResponderActorID = "shiro"
	if left.SameContext(contextWithResponder) {
		t.Fatal("SameContext() accepted a different responder actor")
	}
	right.ThreadID = "thread-other"
	if left.SameContext(right) {
		t.Fatal("SameContext() accepted a different BOARD thread")
	}
}

func externalPeerBindingFixture(wireKind string, classification ExternalPeerClassification, replyTo string) ExternalPeerBinding {
	return ExternalPeerBinding{
		BoardInstanceID: "test-instance", RecipientPrincipalID: "rencrow-comm",
		ChannelID: "channel-a", ThreadID: "thread-a", MessageID: "board-message-1",
		SenderPrincipalID: "peer-a", WireKind: wireKind, Classification: classification, ReplyTo: replyTo,
	}
}

func externalPeerPayloadFixture(wireKind string, classification ExternalPeerClassification, replyTo string) map[string]any {
	binding := externalPeerBindingFixture(wireKind, classification, replyTo)
	return map[string]any{
		"board_instance_id":      binding.BoardInstanceID,
		"recipient_principal_id": binding.RecipientPrincipalID,
		"channel_id":             binding.ChannelID,
		"thread_id":              binding.ThreadID,
		"message_id":             binding.MessageID,
		"sender_principal_id":    binding.SenderPrincipalID,
		"wire_kind":              binding.WireKind,
		"classification":         string(binding.Classification),
		"reply_to":               binding.ReplyTo,
	}
}
