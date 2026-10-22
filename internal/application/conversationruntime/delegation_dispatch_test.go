package conversationruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestDelegationDispatcherPublishesAcceptedReadbackThenAcknowledges(t *testing.T) {
	for _, status := range []conversation.ReadbackStatus{
		conversation.ReadbackStatusAccepted,
		conversation.ReadbackStatusNeedsClarification,
		conversation.ReadbackStatusRejected,
	} {
		t.Run(string(status), func(t *testing.T) {
			fixture := newDelegationDispatcherFixture(t, status)
			result, err := fixture.dispatcher.DispatchOne(context.Background())
			if err != nil {
				t.Fatalf("DispatchOne(): %v", err)
			}
			if !result.Claimed || !result.Accepted || !result.Published || !result.Acknowledged || result.Status != status {
				t.Fatalf("DispatchOne() result = %+v, want completed %q dispatch", result, status)
			}
			if got, want := fixture.store.calls, []string{"claim", "load", "accept", "publish", "ack"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("dispatch call order = %v, want %v", got, want)
			}
			if fixture.store.claimRequest.Kind != DispatchDelegationWorkKind || fixture.store.claimRequest.Limit != 1 {
				t.Fatalf("claim request = %+v, want one dispatch_delegation item", fixture.store.claimRequest)
			}
			if fixture.providerShiro.calls != 1 || fixture.providerKuro.calls != 0 {
				t.Fatalf("readback provider calls = shiro:%d kuro:%d, want only trusted assignee shiro", fixture.providerShiro.calls, fixture.providerKuro.calls)
			}
			if fixture.providerShiro.scope.ActorID != fixture.lease.Assignee || fixture.providerShiro.scope.InputID != fixture.source.Handoff.InstructionMessageID ||
				fixture.providerShiro.scope.Purpose != conversation.PurposeReadback || fixture.providerShiro.scope.PolicyVersion != "policy-test-v1" {
				t.Fatalf("readback scope = %+v, want trusted assignee/instruction and policy scope", fixture.providerShiro.scope)
			}
			if !requestContains(fixture.providerShiro.request, "private context marker") || !requestContains(fixture.providerShiro.request, "Shiro authored persona") {
				t.Fatalf("assignee prompt omitted authorized private context or its Persona: %#v", fixture.providerShiro.request.Messages)
			}
			accepted := fixture.store.acceptedInput
			if accepted == nil || accepted.Assignee != fixture.lease.Assignee || accepted.DelegationID != fixture.source.Handoff.DelegationID || accepted.Revision != fixture.source.Handoff.Revision ||
				accepted.Readback.Sender != fixture.lease.Assignee || accepted.Readback.DelegationID != fixture.source.Handoff.DelegationID || accepted.Readback.Revision != fixture.source.Handoff.Revision {
				t.Fatalf("AcceptReadback input = %#v, want trusted store assignee/delegation metadata", accepted)
			}
			if fixture.store.acceptance.TaskStartQueued != (status == conversation.ReadbackStatusAccepted) {
				t.Fatalf("task start queued = %t for status %q", fixture.store.acceptance.TaskStartQueued, status)
			}
			if fixture.publisher.bundle == nil || len(fixture.publisher.bundle.Events) != 1 {
				t.Fatalf("published bundle = %#v, want only the committed public readback event", fixture.publisher.bundle)
			}
			publicJSON, err := json.Marshal(fixture.publisher.bundle)
			if err != nil {
				t.Fatalf("marshal published bundle: %v", err)
			}
			if strings.Contains(string(publicJSON), "private context marker") || strings.Contains(string(publicJSON), "conversation.delegation_context") {
				t.Fatalf("private source context reached the canonical publisher: %s", publicJSON)
			}
			if fixture.store.acceptedBeforePublish != true {
				t.Fatal("public readback was published before durable acceptance")
			}
		})
	}
}

func TestDelegationReadbackResponseToFieldsRequireExactAcceptedReplySource(t *testing.T) {
	fixture := newDelegationDispatcherFixture(t, conversation.ReadbackStatusAccepted)
	fixture.store.source.ResponseTo = &DelegationDispatchReplySource{
		TurnID: modulecore.NewTurnID(), ClientSubmissionID: "clarification-answer-submission",
	}
	result, err := fixture.dispatcher.DispatchOne(context.Background())
	if err != nil || !result.Accepted || !result.Published || !result.Acknowledged {
		t.Fatalf("DispatchOne(reply-backed revision) = %+v, %v; want accepted and published", result, err)
	}
	base := fixture.store.acceptance
	input := fixture.store.acceptedInput
	if input == nil {
		t.Fatal("dispatcher did not retain the accepted readback input")
	}

	cloneWithFields := func(fields map[string]any) ReadbackAcceptance {
		cloned := base
		cloned.Event.Payload = make(map[string]any, len(base.Event.Payload)+len(fields))
		for key, value := range base.Event.Payload {
			if key == "response_to_turn_id" || key == "response_to_client_submission_id" {
				continue
			}
			cloned.Event.Payload[key] = value
		}
		for key, value := range fields {
			if value == nil {
				delete(cloned.Event.Payload, key)
			} else {
				cloned.Event.Payload[key] = value
			}
		}
		return cloned
	}
	responseTurn := string(fixture.store.source.ResponseTo.TurnID)
	responseSubmission := fixture.store.source.ResponseTo.ClientSubmissionID
	tests := []struct {
		name       string
		responseTo *DelegationDispatchReplySource
		fields     map[string]any
		wantErr    bool
	}{
		{name: "exact accepted reply binding", responseTo: fixture.store.source.ResponseTo, fields: map[string]any{
			"response_to_turn_id": responseTurn, "response_to_client_submission_id": responseSubmission,
		}},
		{name: "missing both fields", responseTo: fixture.store.source.ResponseTo, wantErr: true},
		{name: "missing submission field", responseTo: fixture.store.source.ResponseTo, fields: map[string]any{
			"response_to_turn_id": responseTurn,
		}, wantErr: true},
		{name: "missing turn field", responseTo: fixture.store.source.ResponseTo, fields: map[string]any{
			"response_to_client_submission_id": responseSubmission,
		}, wantErr: true},
		{name: "noncanonical turn ID", responseTo: fixture.store.source.ResponseTo, fields: map[string]any{
			"response_to_turn_id": "not-a-turn-id", "response_to_client_submission_id": responseSubmission,
		}, wantErr: true},
		{name: "empty submission ID", responseTo: fixture.store.source.ResponseTo, fields: map[string]any{
			"response_to_turn_id": responseTurn, "response_to_client_submission_id": " ",
		}, wantErr: true},
		{name: "mismatched submission ID", responseTo: fixture.store.source.ResponseTo, fields: map[string]any{
			"response_to_turn_id": responseTurn, "response_to_client_submission_id": "different-submission",
		}, wantErr: true},
		{name: "unexpected fields on ordinary revision", fields: map[string]any{
			"response_to_turn_id": responseTurn, "response_to_client_submission_id": responseSubmission,
		}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateDelegationReadbackAcceptance(
				cloneWithFields(test.fields), input.OperationID, fixture.lease, fixture.store.source.Handoff, input.Readback, test.responseTo,
			)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateDelegationReadbackAcceptance() error = %v, wantErr=%t", err, test.wantErr)
			}
		})
	}
}

func TestDelegationDispatcherRetriesGuardedLoadFailuresWithoutGeneration(t *testing.T) {
	for _, test := range []struct {
		name    string
		mutate  func(*delegationDispatcherFixture)
		wantErr string
	}{
		{name: "wrong recipient", mutate: func(f *delegationDispatcherFixture) { f.store.source.Handoff.ToActor = "kuro" }, wantErr: "recipient"},
		{name: "stale revision", mutate: func(f *delegationDispatcherFixture) {
			f.store.source.Handoff.Revision++
			f.store.lease.DedupeKey = fmt.Sprintf("delegation:%s:1:start", f.store.source.Handoff.DelegationID)
		}, wantErr: "revision"},
		{name: "invalid current lease", mutate: func(f *delegationDispatcherFixture) { f.store.loadErr = errors.New("lease token/version is stale") }, wantErr: "stale"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDelegationDispatcherFixture(t, conversation.ReadbackStatusAccepted)
			test.mutate(fixture)
			_, err := fixture.dispatcher.DispatchOne(context.Background())
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), test.wantErr) {
				t.Fatalf("DispatchOne() error = %v, want %q failure", err, test.wantErr)
			}
			if fixture.providerShiro.calls != 0 || fixture.providerKuro.calls != 0 || fixture.store.acceptedInput != nil || fixture.publisher.bundle != nil {
				t.Fatalf("guard failure crossed generation/acceptance/publication boundary: providers=%d/%d accepted=%#v published=%#v",
					fixture.providerShiro.calls, fixture.providerKuro.calls, fixture.store.acceptedInput, fixture.publisher.bundle)
			}
			if !containsCall(fixture.store.calls, "retry") || containsCall(fixture.store.calls, "ack") {
				t.Fatalf("guard failure work completion calls = %v, want retry and no ack", fixture.store.calls)
			}
		})
	}
}

func TestDelegationDispatcherRetriesGenerationAcceptancePublicationAndAckFailures(t *testing.T) {
	for _, test := range []struct {
		name       string
		configure  func(*delegationDispatcherFixture)
		wantCalls  []string
		wantAccept bool
		wantPub    bool
	}{
		{name: "generation", configure: func(f *delegationDispatcherFixture) { f.providerShiro.err = errors.New("provider unavailable") }, wantCalls: []string{"claim", "load", "retry"}},
		{name: "acceptance", configure: func(f *delegationDispatcherFixture) { f.store.acceptErr = errors.New("readback store unavailable") }, wantCalls: []string{"claim", "load", "accept", "retry"}},
		{name: "publication", configure: func(f *delegationDispatcherFixture) { f.publisher.err = errors.New("canonical dispatcher unavailable") }, wantCalls: []string{"claim", "load", "accept", "publish", "retry"}, wantAccept: true, wantPub: true},
		{name: "acknowledgement", configure: func(f *delegationDispatcherFixture) { f.store.ackErr = errors.New("ack unavailable") }, wantCalls: []string{"claim", "load", "accept", "publish", "ack", "retry"}, wantAccept: true, wantPub: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDelegationDispatcherFixture(t, conversation.ReadbackStatusAccepted)
			test.configure(fixture)
			if _, err := fixture.dispatcher.DispatchOne(context.Background()); err == nil {
				t.Fatal("DispatchOne() error = nil, want configured failure")
			}
			if !reflect.DeepEqual(fixture.store.calls, test.wantCalls) {
				t.Fatalf("dispatch calls = %v, want %v", fixture.store.calls, test.wantCalls)
			}
			if (fixture.store.acceptedInput != nil) != test.wantAccept || (fixture.publisher.bundle != nil) != test.wantPub {
				t.Fatalf("accept/publish state = %t/%t, want %t/%t", fixture.store.acceptedInput != nil, fixture.publisher.bundle != nil, test.wantAccept, test.wantPub)
			}
		})
	}
}

func TestDelegationDispatcherInvalidReadbackCommitsFailureWithoutRepairGeneration(t *testing.T) {
	fixture := newDelegationDispatcherFixture(t, conversation.ReadbackStatusAccepted)
	fixture.providerShiro.response = `{"schema_version":"agent.readback.v1"}`

	result, err := fixture.dispatcher.DispatchOne(context.Background())
	if err != nil {
		t.Fatalf("DispatchOne(): %v", err)
	}
	if !result.Claimed || !result.Failed || !result.Published || !result.Acknowledged || result.Accepted ||
		result.FailureReason != ReadbackFailureInvalidOutput {
		t.Fatalf("DispatchOne() result = %+v, want terminal invalid-output failure published and closed", result)
	}
	if got, want := fixture.store.calls, []string{"claim", "load", "fail", "publish"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("dispatch call order = %v, want %v", got, want)
	}
	if fixture.providerShiro.calls != 1 || fixture.store.acceptedInput != nil || fixture.store.failureInput == nil {
		t.Fatalf("invalid readback path: generator calls=%d accepted=%#v failure=%#v", fixture.providerShiro.calls, fixture.store.acceptedInput, fixture.store.failureInput)
	}
	if fixture.store.failureInput.Reason != ReadbackFailureInvalidOutput || fixture.store.failureInput.Lease.LeaseToken != fixture.lease.LeaseToken {
		t.Fatalf("terminal failure input = %+v, want invalid-output reason fenced by current lease", fixture.store.failureInput)
	}
	if fixture.publisher.bundle == nil || len(fixture.publisher.bundle.Events) != 1 {
		t.Fatalf("published failure bundle = %#v, want exactly one committed event", fixture.publisher.bundle)
	}
	event := fixture.publisher.bundle.Events[0]
	if payloadString(event.Payload, "kind") != "handoff_error" || payloadString(event.Payload, "status") != "failed" ||
		payloadString(event.Payload, "availability") != "free" || payloadString(event.Payload, "visibility") != "public" ||
		payloadString(event.Payload, "reason") != string(ReadbackFailureInvalidOutput) {
		t.Fatalf("public failure event payload = %#v, want public handoff_error with Free availability", event.Payload)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal failure event: %v", err)
	}
	if strings.Contains(string(encoded), `"schema_version":"agent.readback.v1"`) || strings.Contains(string(encoded), "agent.readback.v1\"}") {
		t.Fatalf("terminal failure must not persist malformed model output: %s", encoded)
	}
}

func TestDelegationDispatcherExpiresFromAcceptedTimestampAndReusesFailureIntent(t *testing.T) {
	t.Run("deadline reached", func(t *testing.T) {
		fixture := newDelegationDispatcherFixture(t, conversation.ReadbackStatusAccepted)
		fixture.store.source.AcceptedAt = fixture.lease.LeaseExpires.Add(-time.Minute - time.Second)
		fixture.store.source.ReadbackDeadline = fixture.lease.LeaseExpires.Add(-time.Minute)

		result, err := fixture.dispatcher.DispatchOne(context.Background())
		if err != nil {
			t.Fatalf("DispatchOne(): %v", err)
		}
		if !result.Failed || result.FailureReason != ReadbackFailureExpired || fixture.providerShiro.calls != 0 {
			t.Fatalf("expired dispatch result=%+v provider calls=%d, want timeout failure without generation", result, fixture.providerShiro.calls)
		}
	})

	t.Run("persisted intent skips generation", func(t *testing.T) {
		fixture := newDelegationDispatcherFixture(t, conversation.ReadbackStatusAccepted)
		fixture.store.source.FailureIntent = &ReadbackFailureIntent{
			DelegationID: fixture.source.Handoff.DelegationID, Revision: fixture.source.Handoff.Revision,
			Assignee: fixture.source.Handoff.ToActor, Reason: ReadbackFailureInvalidOutput,
		}

		result, err := fixture.dispatcher.DispatchOne(context.Background())
		if err != nil {
			t.Fatalf("DispatchOne(): %v", err)
		}
		if !result.Failed || result.FailureReason != ReadbackFailureInvalidOutput || fixture.providerShiro.calls != 0 {
			t.Fatalf("failure-intent replay result=%+v provider calls=%d, want persisted failure without generation", result, fixture.providerShiro.calls)
		}
	})
}

type delegationDispatcherFixture struct {
	dispatcher    *DelegationDispatcher
	store         *delegationDispatchStoreFake
	publisher     *delegationDispatchPublisherFake
	providerShiro *dispatchReadbackProvider
	providerKuro  *dispatchReadbackProvider
	lease         DelegationDispatchLease
	source        DelegationDispatchSource
}

func newDelegationDispatcherFixture(t *testing.T, status conversation.ReadbackStatus) *delegationDispatcherFixture {
	t.Helper()
	// Keep the injected test clock ahead of context.WithDeadline's real clock.
	now := time.Now().UTC().Add(24 * time.Hour)
	handoff := conversation.HandoffEnvelope{
		DelegationID: modulecore.NewRequestID(), Revision: 3, OriginTurnID: modulecore.NewTurnID(),
		OriginUserMessageID: modulecore.NewMessageID(), SessionID: modulecore.NewSessionID(), SourceTraceID: modulecore.NewTraceID(),
		FromActor: "mio", ToActor: "shiro", InstructionMessageID: modulecore.NewMessageID(),
		ContextSnapshotHash: strings.Repeat("a", 64), AllowedCapabilities: []string{}, ReplyTo: modulecore.NewMessageID(),
		Goal: "Summarize the task", Instruction: "Inspect the request and report.",
		Constraints:    []conversation.AgentConstraint{{Key: "keep_scope", Text: "Stay within the requested scope."}},
		ExpectedOutput: "A concise report",
	}
	if err := conversation.ValidateHandoffEnvelope(handoff); err != nil {
		t.Fatalf("ValidateHandoffEnvelope(): %v", err)
	}
	lease := DelegationDispatchLease{
		WorkID: "dispatch:test", DedupeKey: fmt.Sprintf("delegation:%s:%d:start", handoff.DelegationID, handoff.Revision),
		Kind: DispatchDelegationWorkKind, SourceEventID: modulecore.NewEventID(), SessionID: handoff.SessionID,
		Attempt: 1, LeaseToken: string(modulecore.NewRequestID()), LeaseExpires: now.Add(time.Minute), Version: 2, Assignee: "shiro",
	}
	source := DelegationDispatchSource{Handoff: handoff, Context: []DelegationDispatchMessage{{
		MessageID: handoff.OriginUserMessageID, Role: "user", Text: "private context marker: inspect this request",
	}}, AcceptedAt: now, ReadbackDeadline: now.Add(30 * time.Second)}
	store := &delegationDispatchStoreFake{lease: lease, source: source, readbackStatus: status}
	publisher := &delegationDispatchPublisherFake{store: store}
	shiro := &dispatchReadbackProvider{name: "shiro-provider", response: readbackGeneratorJSON(status)}
	kuro := &dispatchReadbackProvider{name: "kuro-provider", response: readbackGeneratorJSON(status)}
	generator := agent.NewReadbackGenerator(map[string]agent.ReadbackAssigneeConfig{
		"shiro": {Provider: shiro, SystemPrompt: "Shiro authored persona"},
		"kuro":  {Provider: kuro, SystemPrompt: "Kuro authored persona"},
	})
	dispatcher, err := NewDelegationDispatcher(DelegationDispatcherDependencies{
		Store: store, Generator: generator, Publisher: publisher,
		Config: DelegationDispatcherConfig{PolicyVersion: "policy-test-v1", LeaseDuration: time.Minute, Now: func() time.Time { return now }},
	})
	if err != nil {
		t.Fatalf("NewDelegationDispatcher(): %v", err)
	}
	return &delegationDispatcherFixture{
		dispatcher: dispatcher, store: store, publisher: publisher, providerShiro: shiro, providerKuro: kuro,
		lease: lease, source: source,
	}
}

func readbackGeneratorJSON(status conversation.ReadbackStatus) string {
	keys := `[]`
	if status == conversation.ReadbackStatusAccepted {
		keys = ` ["keep_scope"] `
	}
	return fmt.Sprintf(`{"schema_version":"agent.readback.v1","status":%q,"understood_goal":"Summarize the task","acknowledged_constraint_keys":%s,"expected_output":"A concise report","text":"I understand the assignment."}`, status, keys)
}

func requestContains(request llm.GenerateRequest, fragment string) bool {
	for _, message := range request.Messages {
		if strings.Contains(message.Content, fragment) {
			return true
		}
	}
	return false
}

func containsCall(calls []string, want string) bool {
	for _, call := range calls {
		if call == want {
			return true
		}
	}
	return false
}

type dispatchReadbackProvider struct {
	name     string
	response string
	err      error
	calls    int
	scope    conversation.ConversationScope
	request  llm.GenerateRequest
}

func (p *dispatchReadbackProvider) GenerateConversation(_ context.Context, scope conversation.ConversationScope, request llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.calls++
	p.scope, p.request = scope, request
	return llm.GenerateResponse{Content: p.response, RequestID: modulecore.NewRequestID(), ResponseID: modulecore.NewResponseID()}, p.err
}

func (p *dispatchReadbackProvider) Name() string { return p.name }

type delegationDispatchStoreFake struct {
	lease                 DelegationDispatchLease
	source                DelegationDispatchSource
	readbackStatus        conversation.ReadbackStatus
	claimRequest          DispatchClaimRequest
	acceptedInput         *AcceptReadbackRequest
	failureInput          *FailReadbackRequest
	acceptance            ReadbackAcceptance
	failure               ReadbackFailure
	acceptedBeforePublish bool
	loadErr               error
	acceptErr             error
	failureErr            error
	ackErr                error
	retryErr              error
	calls                 []string
}

func (s *delegationDispatchStoreFake) ClaimDispatchWork(_ context.Context, request DispatchClaimRequest) ([]DelegationDispatchLease, error) {
	s.calls = append(s.calls, "claim")
	s.claimRequest = request
	return []DelegationDispatchLease{s.lease}, nil
}

func (s *delegationDispatchStoreFake) LoadDelegationDispatch(_ context.Context, _ DelegationDispatchLease, _ time.Time) (DelegationDispatchSource, error) {
	s.calls = append(s.calls, "load")
	if s.loadErr != nil {
		return DelegationDispatchSource{}, s.loadErr
	}
	return s.source, nil
}

func (s *delegationDispatchStoreFake) AcceptReadback(_ context.Context, input AcceptReadbackRequest) (ReadbackAcceptance, error) {
	s.calls = append(s.calls, "accept")
	if s.acceptErr != nil {
		return ReadbackAcceptance{}, s.acceptErr
	}
	s.acceptedInput = &input
	handoff := s.source.Handoff
	kind := "readback"
	content := input.Readback.Text
	if input.Readback.Status != conversation.ReadbackStatusAccepted {
		kind = "handoff_error"
		if input.Readback.Status == conversation.ReadbackStatusRejected {
			content = "The assigned agent rejected this handoff. " + content
		} else {
			content = "The assigned agent needs clarification before starting this work. " + content
		}
	}
	event := modulecore.EventEnvelope{
		SchemaVersion: modulecore.EventEnvelopeSchemaVersion, EventID: input.EventID, EventSeq: 42,
		TraceID: handoff.SourceTraceID, CausationEventID: s.lease.SourceEventID,
		EventType: "agent.response", ComponentID: "orchestrator", OccurredAt: input.OccurredAt,
		SessionID: handoff.SessionID, TurnID: handoff.OriginTurnID, RequestID: handoff.DelegationID, MessageID: input.MessageID,
		Payload: map[string]any{
			"kind": kind, "schema_version": input.Readback.SchemaVersion, "status": string(input.Readback.Status),
			"understood_goal": input.Readback.UnderstoodGoal, "acknowledged_constraint_keys": input.Readback.AcknowledgedConstraintKeys,
			"expected_output": input.Readback.ExpectedOutput, "delegation_id": string(input.DelegationID), "delegation_revision": input.Revision,
			"availability": "free", "visibility": "public", "from": input.Assignee, "source_actor_id": input.Assignee,
			"to": "user", "content": content, "text": content, "reply_to_message_id": string(handoff.ReplyTo),
		},
		ActorKind: "", ActorID: "",
	}
	if responseTo := s.source.ResponseTo; responseTo != nil {
		event.Payload["response_to_turn_id"] = string(responseTo.TurnID)
		event.Payload["response_to_client_submission_id"] = responseTo.ClientSubmissionID
	}
	s.acceptance = ReadbackAcceptance{
		OperationID: input.OperationID, DelegationID: input.DelegationID, Revision: input.Revision,
		OriginTurnID: handoff.OriginTurnID, SessionID: handoff.SessionID, Status: input.Readback.Status,
		Availability: "free", TaskStartQueued: input.Readback.Status == conversation.ReadbackStatusAccepted, Event: event,
	}
	return s.acceptance, nil
}

func (s *delegationDispatchStoreFake) FailReadback(_ context.Context, input FailReadbackRequest) (ReadbackFailure, error) {
	s.calls = append(s.calls, "fail")
	if s.failureErr != nil {
		return ReadbackFailure{}, s.failureErr
	}
	s.failureInput = &input
	handoff := s.source.Handoff
	content := "委譲先から有効な復唱を確認できませんでした。Mioは再び応答できます。"
	event := modulecore.EventEnvelope{
		SchemaVersion: modulecore.EventEnvelopeSchemaVersion, EventID: input.EventID, EventSeq: 43,
		TraceID: handoff.SourceTraceID, CausationEventID: s.lease.SourceEventID,
		EventType: "agent.response", ComponentID: "orchestrator", OccurredAt: input.OccurredAt,
		SessionID: handoff.SessionID, TurnID: handoff.OriginTurnID, RequestID: handoff.DelegationID,
		MessageID: input.MessageID, Payload: map[string]any{
			"kind": "handoff_error", "status": "failed", "reason": string(input.Reason),
			"delegation_id": string(handoff.DelegationID), "delegation_revision": handoff.Revision,
			"availability": "free", "visibility": "public", "from": "mio", "source_actor_id": "mio", "to": "user",
			"content": content, "text": content, "reply_to_message_id": string(handoff.ReplyTo),
		},
	}
	s.failure = ReadbackFailure{
		OperationID: input.OperationID, DelegationID: handoff.DelegationID, Revision: handoff.Revision,
		OriginTurnID: handoff.OriginTurnID, SessionID: handoff.SessionID, Reason: input.Reason,
		Availability: "free", Event: event,
	}
	return s.failure, nil
}

func (s *delegationDispatchStoreFake) AckDispatch(_ context.Context, _ DelegationDispatchLease, _ time.Time) error {
	s.calls = append(s.calls, "ack")
	if s.acceptedInput == nil {
		return errors.New("cannot ack before acceptance")
	}
	return s.ackErr
}

func (s *delegationDispatchStoreFake) RetryDispatch(_ context.Context, _ DelegationDispatchLease, _ time.Time) error {
	s.calls = append(s.calls, "retry")
	return s.retryErr
}

type delegationDispatchPublisherFake struct {
	store  *delegationDispatchStoreFake
	bundle *CommittedBundle
	err    error
}

func (p *delegationDispatchPublisherFake) PublishCommitted(_ context.Context, bundle CommittedBundle) error {
	p.store.calls = append(p.store.calls, "publish")
	p.store.acceptedBeforePublish = p.store.acceptedInput != nil
	copy := cloneCommittedBundle(bundle)
	p.bundle = &copy
	return p.err
}
