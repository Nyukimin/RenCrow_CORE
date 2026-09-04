package conversation

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func conversationTurnTestRequest() ConversationTurnRequest {
	return ConversationTurnRequest{
		TurnID: modulecore.NewTurnID(), TraceID: modulecore.NewTraceID(), RootTaskID: modulecore.NewTaskID(),
		UserMessageID: modulecore.NewMessageID(), AgentMessageID: modulecore.NewMessageID(),
		SessionID: string(modulecore.NewSessionID()), OwnerID: "owner-domain-1",
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

func TestConversationTurnValidation(t *testing.T) {
	request := conversationTurnTestRequest()
	normalized, err := NormalizeConversationTurnRequest(request)
	if err != nil || normalized.Domain != "general" {
		t.Fatalf("default domain=%q err=%v, want general", normalized.Domain, err)
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
	invalid = request
	invalid.SessionID = "legacy-session"
	if !errors.Is(invalid.Validate(), ErrConversationTurnInvalid) {
		t.Fatal("legacy session ID was accepted")
	}
}

type step06ConversationTurnIdentity struct {
	name     string
	wantType reflect.Type
	prefix   string
	raw      string
}

func step06ConversationTurnIdentityValues() []step06ConversationTurnIdentity {
	turnID := modulecore.NewTurnID()
	traceID := modulecore.NewTraceID()
	rootTaskID := modulecore.NewTaskID()
	userMessageID := modulecore.NewMessageID()
	agentMessageID := modulecore.NewMessageID()
	return []step06ConversationTurnIdentity{
		{name: "TurnID", wantType: reflect.TypeOf(turnID), prefix: "turn_", raw: string(turnID)},
		{name: "TraceID", wantType: reflect.TypeOf(traceID), prefix: "trc_", raw: string(traceID)},
		{name: "RootTaskID", wantType: reflect.TypeOf(rootTaskID), prefix: "tsk_", raw: string(rootTaskID)},
		{name: "UserMessageID", wantType: reflect.TypeOf(userMessageID), prefix: "msg_", raw: string(userMessageID)},
		{name: "AgentMessageID", wantType: reflect.TypeOf(agentMessageID), prefix: "msg_", raw: string(agentMessageID)},
	}
}

func step06ConversationTurnRequest() (ConversationTurnRequest, []step06ConversationTurnIdentity) {
	request := conversationTurnTestRequest()
	identities := step06ConversationTurnIdentityValues()
	for _, identity := range identities {
		setConversationTurnIdentityRaw(&request, identity.name, identity.raw)
	}
	return request, identities
}

func setConversationTurnIdentityRaw(request *ConversationTurnRequest, fieldName, raw string) bool {
	field := reflect.ValueOf(request).Elem().FieldByName(fieldName)
	if !field.IsValid() || !field.CanSet() || field.Kind() != reflect.String {
		return false
	}
	field.SetString(raw)
	return true
}

func clearConversationTurnIdentity(request *ConversationTurnRequest, fieldName string) bool {
	field := reflect.ValueOf(request).Elem().FieldByName(fieldName)
	if !field.IsValid() || !field.CanSet() {
		return false
	}
	field.Set(reflect.Zero(field.Type()))
	return true
}

func assertStep06ConversationTurnIdentityShape(t *testing.T, request ConversationTurnRequest, identities []step06ConversationTurnIdentity) bool {
	t.Helper()
	valid := true
	seen := make(map[string]string, len(identities))
	requestValue := reflect.ValueOf(request)
	for _, identity := range identities {
		field := requestValue.FieldByName(identity.name)
		if !field.IsValid() {
			t.Errorf("ConversationTurnRequest is missing canonical %s", identity.name)
			valid = false
			continue
		}
		if field.Type() != identity.wantType {
			t.Errorf("ConversationTurnRequest.%s type=%s, want canonical %s", identity.name, field.Type(), identity.wantType)
			valid = false
		}
		if field.Kind() != reflect.String {
			t.Errorf("ConversationTurnRequest.%s must be a string-backed canonical ID", identity.name)
			valid = false
			continue
		}
		raw := field.String()
		if !strings.HasPrefix(raw, identity.prefix) {
			t.Errorf("ConversationTurnRequest.%s=%q must use prefix %q", identity.name, raw, identity.prefix)
			valid = false
		}
		if previous, duplicate := seen[raw]; raw != "" && duplicate {
			t.Errorf("ConversationTurnRequest.%s aliases %s with %q", identity.name, previous, raw)
			valid = false
		}
		if raw != "" {
			seen[raw] = identity.name
		}
	}
	return valid
}

func TestConversationTurnCanonicalIdentityFixtureRequiresIndependentIDs(t *testing.T) {
	request, identities := step06ConversationTurnRequest()
	if !assertStep06ConversationTurnIdentityShape(t, request, identities) {
		return
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("valid independent canonical identities rejected: %v", err)
	}
}

func TestConversationTurnRejectsMissingCanonicalIdentity(t *testing.T) {
	for _, identity := range step06ConversationTurnIdentityValues() {
		identity := identity
		t.Run("missing_"+identity.name, func(t *testing.T) {
			request, _ := step06ConversationTurnRequest()
			if !clearConversationTurnIdentity(&request, identity.name) {
				t.Fatalf("ConversationTurnRequest is missing canonical %s", identity.name)
			}
			if err := request.Validate(); !errors.Is(err, ErrConversationTurnInvalid) {
				t.Fatalf("missing %s was accepted: err=%v", identity.name, err)
			}
		})
	}
}

func TestConversationTurnRejectsWrongTypeCanonicalIdentity(t *testing.T) {
	requestType := reflect.TypeOf(ConversationTurnRequest{})
	for _, identity := range step06ConversationTurnIdentityValues() {
		field, ok := requestType.FieldByName(identity.name)
		if !ok {
			t.Errorf("ConversationTurnRequest is missing canonical %s", identity.name)
			continue
		}
		if field.Type != identity.wantType {
			t.Errorf("ConversationTurnRequest.%s type=%s, want canonical %s", identity.name, field.Type, identity.wantType)
		}
	}
}

func TestConversationTurnRejectsAliasedCanonicalIdentity(t *testing.T) {
	identities := step06ConversationTurnIdentityValues()
	for index, identity := range identities {
		identity := identity
		t.Run("alias_"+identity.name, func(t *testing.T) {
			request, _ := step06ConversationTurnRequest()
			alias := identities[(index+1)%len(identities)].raw
			switch identity.name {
			case "UserMessageID":
				alias = string(request.AgentMessageID)
			case "AgentMessageID":
				alias = string(request.UserMessageID)
			}
			if !setConversationTurnIdentityRaw(&request, identity.name, alias) {
				t.Fatalf("ConversationTurnRequest is missing canonical %s", identity.name)
			}
			if err := request.Validate(); !errors.Is(err, ErrConversationTurnInvalid) {
				t.Fatalf("aliased %s was accepted: err=%v", identity.name, err)
			}
		})
	}
}

func TestConversationTurnCanonicalPayloadBindsIndependentIdentities(t *testing.T) {
	request, identities := step06ConversationTurnRequest()
	if !assertStep06ConversationTurnIdentityShape(t, request, identities) {
		return
	}
	base, err := CanonicalConversationTurnPayload(request)
	if err != nil {
		t.Fatalf("base canonical payload: %v", err)
	}
	for _, identity := range identities[1:] {
		changed := request
		replacement := step06ConversationTurnIdentityValues()
		var replacementRaw string
		for _, candidate := range replacement {
			if candidate.name == identity.name {
				replacementRaw = candidate.raw
				break
			}
		}
		if !setConversationTurnIdentityRaw(&changed, identity.name, replacementRaw) {
			t.Fatalf("ConversationTurnRequest is missing canonical %s", identity.name)
		}
		payload, err := CanonicalConversationTurnPayload(changed)
		if err != nil {
			t.Fatalf("changed %s canonical payload: %v", identity.name, err)
		}
		if string(payload) == string(base) {
			t.Errorf("canonical payload did not change when %s changed under the same TurnID", identity.name)
		}
		if !strings.Contains(string(base), identity.raw) {
			t.Errorf("canonical payload omitted %s=%q", identity.name, identity.raw)
		}
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
