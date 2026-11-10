package conversation

import "testing"

func TestBoardCommunicationMeetingRouteMatchesOnlyExactDirection(t *testing.T) {
	route := BoardCommunicationMeetingRoute{
		ChannelID: BoardCommunicationMeetingChannelID, SenderPrincipalID: "muse-comm",
		RecipientActorID: "shiro", RecipientPrincipalID: "shiro",
	}
	if !route.Matches(BoardCommunicationMeetingChannelID, "muse-comm", "shiro") {
		t.Fatal("Matches() rejected the configured direction")
	}
	for _, test := range []struct {
		name      string
		channel   string
		sender    string
		recipient string
	}{
		{name: "other channel", channel: "another-channel", sender: "muse-comm", recipient: "shiro"},
		{name: "other sender", channel: BoardCommunicationMeetingChannelID, sender: "grok-comm", recipient: "shiro"},
		{name: "reverse direction", channel: BoardCommunicationMeetingChannelID, sender: "shiro", recipient: "muse-comm"},
		{name: "empty sender", channel: BoardCommunicationMeetingChannelID, recipient: "shiro"},
		{name: "empty recipient", channel: BoardCommunicationMeetingChannelID, sender: "muse-comm"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if route.Matches(test.channel, test.sender, test.recipient) {
				t.Fatal("Matches() authorized a different or incomplete direction")
			}
		})
	}
}

func TestBoardCommunicationMeetingPrincipalBindingsExcludeUnknownActors(t *testing.T) {
	for principal, wantActor := range map[string]string{
		"rencrow-comm": "mio", "shiro": "shiro", "kuro": "kuro", "midori": "midori",
		"aka": "aka", "ao": "ao", "kin": "kin", "gin": "gin",
	} {
		actor, ok := BoardCommunicationMeetingInternalActorID(principal)
		if !ok || actor != wantActor {
			t.Errorf("BoardCommunicationMeetingInternalActorID(%q) = %q, %t; want %q, true", principal, actor, ok, wantActor)
		}
	}
	for _, principal := range []string{"lumina", "muse-comm", "dots-comm", "grok-comm", "rencrow", "heavy", ""} {
		if actor, ok := BoardCommunicationMeetingInternalActorID(principal); ok {
			t.Errorf("BoardCommunicationMeetingInternalActorID(%q) = %q, true; want excluded", principal, actor)
		}
	}
	for principal, want := range map[string]bool{
		"muse-comm": true, "dots-comm": true, "grok-comm": true,
		"lumina": false, "shiro": false, "kit": false, "": false,
	} {
		if got := IsBoardCommunicationMeetingExternalPrincipal(principal); got != want {
			t.Errorf("IsBoardCommunicationMeetingExternalPrincipal(%q) = %t; want %t", principal, got, want)
		}
	}
}
