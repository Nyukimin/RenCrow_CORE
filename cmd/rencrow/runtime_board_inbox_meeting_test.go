package main

import (
	"context"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/boardcommunication"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
)

const boardMeetingTestChannel = "slack_c0c6j86evsr"

func boardInboxMeetingTestItem(pin eventstore.BoardConnectionPin, sender string, id string, mentions, recipients []string) boardcommunication.InboxItem {
	item := boardInboxTestItem("item-"+id, "message-"+id, pin.CutoverAt.Add(time.Second), "synthetic meeting message")
	item.ChannelID = boardMeetingTestChannel
	item.SenderID = sender
	item.Mentions = append([]string(nil), mentions...)
	item.Recipients = append([]string(nil), recipients...)
	return item
}

func boardInboxMeetingTestScope(pin eventstore.BoardConnectionPin, actor string, routes ...domainconversation.BoardCommunicationMeetingRoute) boardInboxScope {
	scope := boardInboxTestScope(pin)
	scope.ResponderActorID = actor
	scope.AllowedChannelIDs = append(scope.AllowedChannelIDs, boardMeetingTestChannel)
	scope.MeetingRoutes = append([]domainconversation.BoardCommunicationMeetingRoute(nil), routes...)
	return scope
}

func boardInboxMeetingTestThread(pin eventstore.BoardConnectionPin, item boardcommunication.InboxItem, kind, replyTo string) boardcommunication.ThreadPage {
	thread := boardInboxTestThread(item, kind, replyTo)
	thread.Thread.Participants = []string{pin.PrincipalID, item.SenderID}
	return thread
}

func TestValidBoardInboxScopeBindsMeetingRouteToPinnedRecipient(t *testing.T) {
	pin := boardInboxTestPin()
	pin.PrincipalID = "shiro"
	route := domainconversation.BoardCommunicationMeetingRoute{
		ChannelID: boardMeetingTestChannel, SenderPrincipalID: "muse-comm",
		RecipientActorID: "shiro", RecipientPrincipalID: "shiro",
	}
	scope := boardInboxMeetingTestScope(pin, "shiro", route)
	if !validBoardInboxScope(scope) {
		t.Fatal("valid meeting route bound to the authenticated Shiro inbox was rejected")
	}

	tests := []struct {
		name   string
		mutate func(*boardInboxScope)
	}{
		{name: "recipient actor differs from pinned actor", mutate: func(scope *boardInboxScope) { scope.MeetingRoutes[0].RecipientActorID = "kuro" }},
		{name: "recipient principal differs from authenticated pin", mutate: func(scope *boardInboxScope) { scope.MeetingRoutes[0].RecipientPrincipalID = "kuro" }},
		{name: "route uses another channel", mutate: func(scope *boardInboxScope) { scope.MeetingRoutes[0].ChannelID = "channel-alpha" }},
		{name: "route channel is outside inbox channel allowlist", mutate: func(scope *boardInboxScope) { scope.AllowedChannelIDs = []string{"channel-alpha"} }},
		{name: "Lumina is not a meeting participant", mutate: func(scope *boardInboxScope) { scope.MeetingRoutes[0].SenderPrincipalID = "lumina" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalid := boardInboxMeetingTestScope(pin, "shiro", route)
			test.mutate(&invalid)
			if validBoardInboxScope(invalid) {
				t.Fatal("invalid meeting route scope was accepted")
			}
		})
	}
}

func TestHandleBoardInboxMeetingForwardRouteProcessesOnlyCanonicalSingleRecipientRequest(t *testing.T) {
	for _, test := range []struct {
		name       string
		mentions   []string
		recipients []string
		wantAccept bool
	}{
		{name: "exact single mention and recipient", mentions: []string{"shiro"}, recipients: []string{"shiro"}, wantAccept: true},
		{name: "multiple mentions", mentions: []string{"shiro", "kuro"}, recipients: []string{"shiro"}},
		{name: "multiple recipients", mentions: []string{"shiro"}, recipients: []string{"shiro", "kuro"}},
		{name: "mention does not match recipient", mentions: []string{"kuro"}, recipients: []string{"shiro"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			pin := boardInboxTestPin()
			pin.PrincipalID = "shiro"
			wire := newBoardInboxWireHarness(t, pin)
			item := boardInboxMeetingTestItem(pin, "muse-comm", "forward", test.mentions, test.recipients)
			wire.thread = boardInboxMeetingTestThread(pin, item, "request", "")
			wire.allowInboxItems(item.ItemID)
			wire.claimResults = []boardcommunication.ClaimResult{boardInboxClaimResult(item, "claimed")}
			owner := newBoardInboxTestOwner(wire.trace, pin)
			owner.outcomeAfterProcess = true
			scope := boardInboxMeetingTestScope(pin, "shiro", domainconversation.BoardCommunicationMeetingRoute{
				ChannelID: boardMeetingTestChannel, SenderPrincipalID: "muse-comm",
				RecipientActorID: "shiro", RecipientPrincipalID: "shiro",
			})

			if _, err := handleBoardInboxItemWithDue(context.Background(), wire.client, owner, scope, item); err != nil {
				t.Fatalf("handleBoardInboxItemWithDue(): %v", err)
			}
			if wire.threadReads != 1 {
				t.Fatalf("canonical thread reads = %d, want route-authorized item to reach canonical validation", wire.threadReads)
			}
			wantCalls := 0
			if test.wantAccept {
				wantCalls = 1
			}
			if owner.acceptCalls != wantCalls || owner.processCalls != wantCalls || wire.claimCalls != wantCalls || wire.ackCalls != wantCalls {
				t.Fatalf("accept/process/claim/ACK = %d/%d/%d/%d, want %d each", owner.acceptCalls, owner.processCalls, wire.claimCalls, wire.ackCalls, wantCalls)
			}
		})
	}
}

func TestHandleBoardInboxMeetingReverseRouteOnlyCorrelatesReplies(t *testing.T) {
	for _, test := range []struct {
		name          string
		kind          string
		replyTo       string
		terminalMatch bool
		wantTerminal  int
		wantClaim     int
		wantACK       int
	}{
		{name: "reverse new request is never processed", kind: "request"},
		{name: "uncorrelated reply remains pending", kind: "reply", replyTo: "message-original", wantTerminal: 1},
		{name: "correlated terminal reply is ACKed", kind: "reply", replyTo: "message-original", terminalMatch: true, wantTerminal: 1, wantClaim: 1, wantACK: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			pin := boardInboxTestPin()
			pin.PrincipalID = "shiro"
			wire := newBoardInboxWireHarness(t, pin)
			item := boardInboxMeetingTestItem(pin, "kuro", "reverse", nil, []string{"shiro"})
			wire.thread = boardInboxMeetingTestThread(pin, item, test.kind, test.replyTo)
			wire.allowInboxItems(item.ItemID)
			wire.claimResults = []boardcommunication.ClaimResult{boardInboxClaimResult(item, "claimed")}
			owner := newBoardInboxTestOwner(wire.trace, pin)
			owner.terminalMatch = test.terminalMatch
			scope := boardInboxMeetingTestScope(pin, "shiro", domainconversation.BoardCommunicationMeetingRoute{
				ChannelID: boardMeetingTestChannel, SenderPrincipalID: "shiro",
				RecipientActorID: "kuro", RecipientPrincipalID: "kuro",
			})

			if _, err := handleBoardInboxItemWithDue(context.Background(), wire.client, owner, scope, item); err != nil {
				t.Fatalf("handleBoardInboxItemWithDue(): %v", err)
			}
			if wire.threadReads != 1 || owner.acceptCalls != 0 || owner.processCalls != 0 || owner.terminalCalls != test.wantTerminal ||
				wire.claimCalls != test.wantClaim || wire.ackCalls != test.wantACK {
				t.Fatalf("thread/accept/process/correlate/claim/ACK = %d/%d/%d/%d/%d/%d; want 1/0/0/%d/%d/%d",
					wire.threadReads, owner.acceptCalls, owner.processCalls, owner.terminalCalls, wire.claimCalls, wire.ackCalls,
					test.wantTerminal, test.wantClaim, test.wantACK)
			}
		})
	}
}

func TestHandleBoardInboxMeetingRouteRejectsUnrelatedChannelBeforeCanonicalRead(t *testing.T) {
	pin := boardInboxTestPin()
	pin.PrincipalID = "shiro"
	wire := newBoardInboxWireHarness(t, pin)
	item := boardInboxMeetingTestItem(pin, "muse-comm", "other-channel", []string{"shiro"}, []string{"shiro"})
	item.ChannelID = "channel-unrelated"
	wire.thread = boardInboxMeetingTestThread(pin, item, "request", "")
	owner := newBoardInboxTestOwner(wire.trace, pin)
	scope := boardInboxMeetingTestScope(pin, "shiro", domainconversation.BoardCommunicationMeetingRoute{
		ChannelID: boardMeetingTestChannel, SenderPrincipalID: "muse-comm",
		RecipientActorID: "shiro", RecipientPrincipalID: "shiro",
	})

	if _, err := handleBoardInboxItemWithDue(context.Background(), wire.client, owner, scope, item); err != nil {
		t.Fatalf("handleBoardInboxItemWithDue(): %v", err)
	}
	if wire.threadReads != 0 || owner.acceptCalls != 0 || owner.processCalls != 0 || wire.claimCalls != 0 || wire.ackCalls != 0 {
		t.Fatalf("unrelated meeting message caused thread/owner/claim/ACK work: %d/%d/%d/%d/%d", wire.threadReads, owner.acceptCalls, owner.processCalls, wire.claimCalls, wire.ackCalls)
	}
}

func TestHandleBoardInboxMeetingRouteCannotRelaxPinnedChannelAllowlist(t *testing.T) {
	pin := boardInboxTestPin()
	pin.PrincipalID = "shiro"
	wire := newBoardInboxWireHarness(t, pin)
	item := boardInboxMeetingTestItem(pin, "muse-comm", "pinned-channel", []string{"shiro"}, []string{"shiro"})
	wire.thread = boardInboxMeetingTestThread(pin, item, "request", "")
	owner := newBoardInboxTestOwner(wire.trace, pin)
	scope := boardInboxMeetingTestScope(pin, "shiro", domainconversation.BoardCommunicationMeetingRoute{
		ChannelID: boardMeetingTestChannel, SenderPrincipalID: "muse-comm",
		RecipientActorID: "shiro", RecipientPrincipalID: "shiro",
	})
	scope.AllowedChannelIDs = []string{"channel-alpha"}

	if _, err := handleBoardInboxItemWithDue(context.Background(), wire.client, owner, scope, item); err != nil {
		t.Fatalf("handleBoardInboxItemWithDue(): %v", err)
	}
	if wire.threadReads != 0 || owner.acceptCalls != 0 || owner.processCalls != 0 || wire.claimCalls != 0 || wire.ackCalls != 0 {
		t.Fatalf("meeting route bypassed the pinned channel allowlist: thread/owner/claim/ACK = %d/%d/%d/%d/%d",
			wire.threadReads, owner.acceptCalls, owner.processCalls, wire.claimCalls, wire.ackCalls)
	}
}
