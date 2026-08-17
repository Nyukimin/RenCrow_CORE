package conversation

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func conversationTurnTestRequest() ConversationTurnRequest {
	return ConversationTurnRequest{
		TurnID: "job-domain-1", SessionID: "session-domain-1", OwnerID: "owner-domain-1",
		UserMessage: "hello", AgentMessage: "hi", AgentSpeaker: SpeakerMio,
		RecallTraceItems: []RecallTraceItem{{Layer: "L1", Kind: "memory", Summary: "bounded", Status: TraceStatusInjected, Decision: "included", TokenCount: 2}},
		Targets:          []ConversationTurnTarget{ConversationTurnTargetRedisProjection},
	}
}

func TestConversationTurnCanonicalHashBindsSemanticFieldsNotRetryClock(t *testing.T) {
	request := conversationTurnTestRequest()
	first, err := ConversationTurnPayloadSHA256(request)
	if err != nil {
		t.Fatalf("first hash: %v", err)
	}
	secondRequest := request
	secondRequest.RecallTraceItems = append([]RecallTraceItem(nil), request.RecallTraceItems...)
	secondRequest.RecallTraceItems[0].RetrievedAt = time.Now().UTC()
	second, err := ConversationTurnPayloadSHA256(secondRequest)
	if err != nil {
		t.Fatalf("second hash: %v", err)
	}
	if first == second {
		t.Fatal("trace item timestamp must remain part of the captured trace binding")
	}
	changed := request
	changed.AgentMessage = "different"
	changedHash, err := ConversationTurnPayloadSHA256(changed)
	if err != nil {
		t.Fatalf("changed hash: %v", err)
	}
	if first == changedHash {
		t.Fatal("agent text did not change canonical hash")
	}
	if strings.Contains(string(mustCanonicalPayload(t, request)), "created_at") {
		t.Fatal("canonical payload must not contain retry wall-clock fields")
	}
}

func TestConversationTurnValidationAndStableDistinctMessageIDs(t *testing.T) {
	request := conversationTurnTestRequest()
	normalized, err := NormalizeConversationTurnRequest(request)
	if err != nil || normalized.Domain != "general" {
		t.Fatalf("default domain=%q err=%v, want general", normalized.Domain, err)
	}
	userID, agentID, err := ConversationTurnMessageIDs(request.TurnID)
	if err != nil {
		t.Fatalf("message ids: %v", err)
	}
	if userID == agentID || !strings.HasPrefix(userID, "msg_") || !strings.HasPrefix(agentID, "msg_") {
		t.Fatalf("message ids are not distinct UUID-shaped IDs: %q %q", userID, agentID)
	}
	reopenedUser, reopenedAgent, err := ConversationTurnMessageIDs(request.TurnID)
	if err != nil || reopenedUser != userID || reopenedAgent != agentID {
		t.Fatalf("message IDs are not stable: %q/%q vs %q/%q", userID, agentID, reopenedUser, reopenedAgent)
	}
	invalid := request
	invalid.AgentSpeaker = Speaker("heavy")
	if !errors.Is(invalid.Validate(), ErrConversationTurnInvalid) {
		t.Fatal("legacy non-canonical agent speaker was accepted")
	}
	invalid = request
	invalid.Targets = []ConversationTurnTarget{ConversationTurnTargetRedisProjection, ConversationTurnTargetRedisProjection}
	if !errors.Is(invalid.Validate(), ErrConversationTurnInvalid) {
		t.Fatal("duplicate target was accepted")
	}
	invalid = request
	invalid.UserMessage = ""
	if !errors.Is(invalid.Validate(), ErrConversationTurnInvalid) {
		t.Fatal("missing user message was accepted")
	}
}

func TestConversationTurnTraceTextHasRequestWideBound(t *testing.T) {
	// A realistic multi-item trace stays within the request-wide bound. The
	// former bound reused the per-item limit and rejected every real turn.
	request := conversationTurnTestRequest()
	request.RecallTraceItems = []RecallTraceItem{
		{Layer: "L1", Kind: "memory", Summary: strings.Repeat("a", 5000), Status: TraceStatusInjected, Decision: "included"},
		{Layer: "L1", Kind: "memory", Summary: strings.Repeat("b", 5000), Status: TraceStatusInjected, Decision: "included"},
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("realistic trace rejected: %v", err)
	}

	over := conversationTurnTestRequest()
	items := make([]RecallTraceItem, 0, 33)
	for i := 0; i < 33; i++ {
		items = append(items, RecallTraceItem{Layer: "L1", Kind: "memory", Summary: strings.Repeat("c", 8000), Status: TraceStatusInjected, Decision: "included"})
	}
	over.RecallTraceItems = items
	if err := over.Validate(); !errors.Is(err, ErrConversationTurnInvalid) {
		t.Fatalf("trace text over request-wide bound error=%v, want invalid", err)
	}
}

func mustCanonicalPayload(t *testing.T, request ConversationTurnRequest) []byte {
	t.Helper()
	payload, err := CanonicalConversationTurnPayload(request)
	if err != nil {
		t.Fatalf("canonical payload: %v", err)
	}
	return payload
}
