package conversation

import (
	"strings"
	"testing"
)

func TestBoardOwnerRequestPurposeAcceptsOnlySingleExternalPeerReply(t *testing.T) {
	scope := testScope()
	scope.Purpose = PurposeBoardOwnerRequest
	if err := scope.Validate(); err != nil {
		t.Fatalf("board owner request scope validation failed: %v", err)
	}

	const validReply = `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"external_peer","text":"Could you confirm the deployment window?"}}`
	emission, err := ParseAgentEmission([]byte(validReply), scope, EmissionAllowlist{})
	if err != nil {
		t.Fatalf("ParseAgentEmission(board_owner_request): %v", err)
	}
	if emission.Kind != EmissionKindReply || emission.UserMessage.To != "external_peer" || emission.UserMessage.Text != "Could you confirm the deployment window?" || emission.Delegation != nil {
		t.Fatalf("unexpected board owner request emission: %#v", emission)
	}
	if _, err := ParseAgentEmission([]byte(validReply+`{}`), scope, EmissionAllowlist{}); err == nil {
		t.Fatal("ParseAgentEmission() error = nil, want exactly one JSON emission")
	}
	if _, err := ParseAgentEmission([]byte(`{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"external_peer","text":"`+strings.Repeat("x", MaxUserMessageBytes+1)+`"}}`), scope, EmissionAllowlist{}); err == nil {
		t.Fatal("ParseAgentEmission() error = nil, want user message byte limit enforcement")
	}
}

func TestBoardOwnerRequestPurposeRejectsRoutingAndDelegationAuthority(t *testing.T) {
	scope := testScope()
	scope.Purpose = PurposeBoardOwnerRequest

	for _, tc := range []struct {
		name      string
		raw       string
		allowlist EmissionAllowlist
	}{
		{
			name: "reply cannot select the viewer user",
			raw:  `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"user","text":"Please confirm the deployment window."}}`,
		},
		{
			name: "reply cannot carry assignee authority",
			raw:  `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"external_peer","text":"Please confirm the deployment window."}}`,
			allowlist: EmissionAllowlist{
				AllowedAssignees: []string{"shiro"},
			},
		},
		{
			name:      "delegate output is forbidden",
			raw:       `{"schema_version":"mio.turn.v1","kind":"delegate","user_message":{"to":"external_peer","text":"I will delegate this request."},"delegation":{"to":"shiro","goal":"Investigate","instruction":"Check the deployment window.","constraints":[],"expected_output":"A concise response","source_message_ids":[]}}`,
			allowlist: EmissionAllowlist{AllowedAssignees: []string{"shiro"}},
		},
		{
			name: "tool fields are forbidden",
			raw:  `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"external_peer","text":"Please confirm the deployment window."},"tool_calls":[{"name":"send_message"}]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseAgentEmission([]byte(tc.raw), scope, tc.allowlist); err == nil {
				t.Fatal("ParseAgentEmission() error = nil, want board owner request authority rejection")
			}
		})
	}
}
