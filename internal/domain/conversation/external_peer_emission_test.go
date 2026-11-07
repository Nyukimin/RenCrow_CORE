package conversation

import "testing"

func TestExternalPeerPurposeAcceptsOnlyBoundReplyTarget(t *testing.T) {
	externalScope := testScope()
	externalScope.Purpose = ConversationPurpose("external_peer")

	t.Run("valid scope and external peer reply", func(t *testing.T) {
		if err := externalScope.Validate(); err != nil {
			t.Fatalf("external peer scope validation failed: %v", err)
		}
		emission, err := ParseAgentEmission([]byte(`{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"external_peer","text":"The requested update is ready."}}`), externalScope, EmissionAllowlist{})
		if err != nil {
			t.Fatalf("ParseAgentEmission(external_peer): %v", err)
		}
		if emission.Kind != EmissionKindReply || emission.UserMessage.To != "external_peer" || emission.Delegation != nil {
			t.Fatalf("unexpected external peer emission: %#v", emission)
		}
	})

	for _, tc := range []struct {
		name      string
		raw       string
		allowlist EmissionAllowlist
	}{
		{
			name:      "reply cannot target viewer user",
			raw:       `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"user","text":"The requested update is ready."}}`,
			allowlist: EmissionAllowlist{},
		},
		{
			name:      "reply rejects nonempty assignee authority",
			raw:       `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"external_peer","text":"The requested update is ready."}}`,
			allowlist: EmissionAllowlist{AllowedAssignees: []string{"shiro"}},
		},
		{
			name:      "delegate is forbidden even with an assignee",
			raw:       `{"schema_version":"mio.turn.v1","kind":"delegate","user_message":{"to":"external_peer","text":"I will delegate this."},"delegation":{"to":"shiro","goal":"Investigate","instruction":"Check the relevant behavior.","constraints":[],"expected_output":"A concise report","source_message_ids":[]}}`,
			allowlist: EmissionAllowlist{AllowedAssignees: []string{"shiro"}},
		},
		{
			name:      "wrong recipient remains forbidden with assignee authority",
			raw:       `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"user","text":"The requested update is ready."}}`,
			allowlist: EmissionAllowlist{AllowedAssignees: []string{"shiro"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseAgentEmission([]byte(tc.raw), externalScope, tc.allowlist); err == nil {
				t.Fatal("ParseAgentEmission() error = nil, want external peer authority or recipient rejection")
			}
		})
	}

	t.Run("existing Mio turn keeps user recipient", func(t *testing.T) {
		if _, err := ParseAgentEmission([]byte(validReplyJSON("The answer is ready.")), testScope(), testAllowlist()); err != nil {
			t.Fatalf("ParseAgentEmission(mio_turn): %v", err)
		}
	})
}
