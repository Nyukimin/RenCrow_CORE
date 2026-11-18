package taskexecution

import (
	"testing"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestHandoffKeyUsesCanonicalCoreRequestIdentity(t *testing.T) {
	canonical := HandoffKey(modulecore.NewRequestID())
	if err := canonical.Validate(); err != nil {
		t.Fatalf("canonical handoff key rejected: %v", err)
	}
	var roundtrip modulecore.RequestID = canonical
	if err := roundtrip.Validate(); err != nil {
		t.Fatalf("handoff key is not the core canonical identity: %v", err)
	}
	for _, invalid := range []HandoffKey{"", "request-legacy", "tsk_not-a-request"} {
		if err := invalid.Validate(); err == nil {
			t.Errorf("invalid handoff key %q was accepted", invalid)
		}
	}
}
