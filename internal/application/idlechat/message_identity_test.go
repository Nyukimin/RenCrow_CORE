package idlechat

import (
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
)

func TestIdleChatMessageIDIsStablePerLogicalTurnAndUniqueAcrossTurns(t *testing.T) {
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.8, nil, "")

	first := o.idleChatMessageID("idle-identity", 1)
	reused := o.idleChatMessageID("idle-identity", 1)
	second := o.idleChatMessageID("idle-identity", 2)

	if !strings.HasPrefix(first, "msg_") {
		t.Fatalf("first message_id = %q, want msg_ UUID", first)
	}
	if reused != first {
		t.Fatalf("same logical turn changed message_id: first=%q reused=%q", first, reused)
	}
	if second == first {
		t.Fatalf("different turns reused message_id: %q", first)
	}
}
