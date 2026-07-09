package orchestrator

import (
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
)

func TestPhase23DistributedAttributionGuardPreservesTaskMetadata(t *testing.T) {
	memory := session.NewCentralMemory()
	memory.RecordMessage(domaintransport.NewMessage("mio", "user", "sess-1", "job-0", "前の発言"))
	guard := newDistributedAttributionGuard(memory)
	jobID := task.NewJobID()
	original := task.NewTask(jobID, "続き", "line", "U123").
		WithForcedRoute(routing.RoutePLAN).
		WithRoute(routing.RoutePLAN)

	got := guard.Apply(original, "mio", "sess-1")
	if got.JobID() != jobID || got.Channel() != "line" || got.ChatID() != "U123" {
		t.Fatalf("task metadata changed: %#v", got)
	}
	if !got.HasForcedRoute() || got.ForcedRoute() != routing.RoutePLAN || got.Route() != routing.RoutePLAN {
		t.Fatalf("route metadata changed: forced=%t forcedRoute=%s route=%s", got.HasForcedRoute(), got.ForcedRoute(), got.Route())
	}
	if !strings.Contains(got.UserMessage(), "【発言帰属ガード】") || !strings.Contains(got.UserMessage(), "【ユーザー依頼】\n続き") {
		t.Fatalf("expected guard message, got %q", got.UserMessage())
	}
}

func TestPhase23DistributedAttributionGuardSkipsCodeRouteAndExistingGuard(t *testing.T) {
	memory := session.NewCentralMemory()
	memory.RecordMessage(domaintransport.NewMessage("mio", "user", "sess-1", "job-0", "前の発言"))
	guard := newDistributedAttributionGuard(memory)

	codeTask := task.NewTask(task.NewJobID(), "実装して", "line", "U123").WithRoute(routing.RouteCODE)
	if got := guard.Apply(codeTask, "coder1", "sess-1"); got.UserMessage() != "実装して" {
		t.Fatalf("CODE route should not be guarded: %q", got.UserMessage())
	}

	alreadyGuarded := task.NewTask(task.NewJobID(), "【発言帰属ガード】\n既存", "line", "U123")
	if got := guard.Apply(alreadyGuarded, "mio", "sess-1"); got.UserMessage() != alreadyGuarded.UserMessage() {
		t.Fatalf("existing guard should not be duplicated: %q", got.UserMessage())
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
