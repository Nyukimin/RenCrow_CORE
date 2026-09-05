package orchestrator

import (
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestPhase23DistributedAttributionGuardPreservesTaskMetadata(t *testing.T) {
	sessionID := string(modulecore.NewSessionID())
	memory := session.NewCentralMemory()
	memory.RecordMessage(domaintransport.NewMessage("mio", "user", sessionID, "job-0", "前の発言"))
	guard := newDistributedAttributionGuard(memory)
	original := newOrchestratorTestTurnInput(t, "続き", "viewer", "viewer-user").
		WithSessionID(sessionID).
		WithAttachments([]attachment.Attachment{{ID: "att-1"}}).
		WithViewerRecipient("mio").
		WithForcedRoute(routing.RoutePLAN).
		WithRoute(routing.RoutePLAN)

	got := guard.Apply(original, "mio")
	if got.RootTaskID() != original.RootTaskID() || got.TurnID() != original.TurnID() || got.TraceID() != original.TraceID() || got.UserMessageID() != original.UserMessageID() || got.AgentMessageID() != original.AgentMessageID() || got.ChannelAddress().ChannelType() != "viewer" || got.ChannelAddress().ExternalConversationID() != "viewer-user" || got.SessionID() != sessionID {
		t.Fatalf("task metadata changed: %#v", got)
	}
	if !got.HasForcedRoute() || got.ForcedRoute() != routing.RoutePLAN || got.Route() != routing.RoutePLAN {
		t.Fatalf("route metadata changed: forced=%t forcedRoute=%s route=%s", got.HasForcedRoute(), got.ForcedRoute(), got.Route())
	}
	if got.ViewerRecipient() != original.ViewerRecipient() || len(got.Attachments()) != 1 || got.Attachments()[0].ID != "att-1" {
		t.Fatalf("recipient/attachments changed: recipient=%q attachments=%#v", got.ViewerRecipient(), got.Attachments())
	}
	if !strings.Contains(got.MessageText(), "【発言帰属ガード】") || !strings.Contains(got.MessageText(), "【ユーザー依頼】\n続き") {
		t.Fatalf("expected guard message, got %q", got.MessageText())
	}
}

func TestPhase23DistributedAttributionGuardSkipsCodeRouteAndExistingGuard(t *testing.T) {
	memory := session.NewCentralMemory()
	memory.RecordMessage(domaintransport.NewMessage("mio", "user", "sess-1", "job-0", "前の発言"))
	guard := newDistributedAttributionGuard(memory)

	codeTask := newOrchestratorTestTurnInput(t, "実装して", "line", "U123").WithRoute(routing.RouteCODE).WithSessionID("sess-1")
	if got := guard.Apply(codeTask, "coder1"); got.MessageText() != "実装して" {
		t.Fatalf("CODE route should not be guarded: %q", got.MessageText())
	}

	alreadyGuarded := newOrchestratorTestTurnInput(t, "【発言帰属ガード】\n既存", "line", "U123").WithSessionID("sess-1")
	if got := guard.Apply(alreadyGuarded, "mio"); got.MessageText() != alreadyGuarded.MessageText() {
		t.Fatalf("existing guard should not be duplicated: %q", got.MessageText())
	}
}

func TestPhase23DistributedAttributionGuardExcludesIdleChatMessages(t *testing.T) {
	memory := session.NewCentralMemory()
	idle := domaintransport.NewMessage("mio", "user", "sess-1", "job-idle", "idle content")
	idle.Type = domaintransport.MessageTypeIdleChat
	memory.RecordMessage(idle)
	memory.RecordMessage(domaintransport.NewMessage("mio", "user", "idle-session", "job-idle-prefix", "idle prefix content"))
	guard := newDistributedAttributionGuard(memory)

	got := guard.BuildMessage("続き", "mio", "sess-1")
	if got != "続き" {
		t.Fatalf("idle messages should not create guard context, got %q", got)
	}
}
