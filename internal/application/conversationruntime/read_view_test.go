package conversationruntime

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestBuildReadViewMergesProjectionAndCanonicalTailInTurnOrder(t *testing.T) {
	ctx := context.Background()
	ownerID := "user-1"
	sessionID := modulecore.NewSessionID()
	currentTurnID := modulecore.NewTurnID()
	previousTurnID := modulecore.NewTurnID()
	futureTurnID := modulecore.NewTurnID()
	currentInput := userEvent(sessionID, currentTurnID, 15, ownerID, "current input")
	current := AcceptedTurn{
		SessionID: sessionID, TurnID: currentTurnID, ActorID: ownerID,
		TriggerKind: "user_message", InputMessageID: currentInput.MessageID, AdmissionSeq: currentInput.EventSeq,
		GenerationEpoch: 4, State: "accepted", InputEvent: currentInput,
	}

	legacyInput := legacyMessage(sessionID, modulecore.NewTurnID(), "legacy-user", "legacy question", "user-1", "agent", "mio", "user_message", 1, 0)
	legacyReply := legacyMessage(sessionID, legacyInput.Index.TurnID, "legacy-assistant", "legacy answer", "mio", "user", ownerID, "assistant_message", 1, 1)
	priorInput := canonicalMessage(userEvent(sessionID, previousTurnID, 11, ownerID, "prior question canonical"), "user-1", "agent", "mio", "user_message")
	projectedPrior := legacyMessage(sessionID, previousTurnID, string(priorInput.Index.MessageID), "stale prior projection", ownerID, "agent", "mio", "user_message", 2, 0)
	projectedPrior.EventSeq = 0 // older L1 rows may not carry v2 EventSeq values
	sharedMessageID := modulecore.NewMessageID()
	projectionDuplicate := legacyMessage(sessionID, previousTurnID, string(sharedMessageID), "stale projected text", "mio", "user", ownerID, "assistant_message", 2, 1)
	projectionDuplicate.EventSeq = 0
	projection := LegacyProjection{
		AppliedThrough: 12,
		Complete:       true,
		Messages:       []LegacyMessage{legacyInput, legacyReply, projectedPrior, projectionDuplicate},
	}

	priorReplyEvent := assistantEvent(sessionID, previousTurnID, 18, string(sharedMessageID), "prior answer canonical", "mio")
	priorReply := canonicalMessage(priorReplyEvent, "mio", "user", ownerID, "assistant_message")
	futureInput := canonicalMessage(userEvent(sessionID, futureTurnID, 20, ownerID, "future question"), ownerID, "agent", "mio", "user_message")
	futureReply := canonicalMessage(assistantEvent(sessionID, futureTurnID, 21, string(modulecore.NewMessageID()), "future answer", "mio"), "mio", "user", ownerID, "assistant_message")
	privateMessage := canonicalMessage(assistantEvent(sessionID, previousTurnID, 19, string(modulecore.NewMessageID()), "private instruction", "mio"), "mio", "agent", "worker-7", "delegation_instruction")
	reader := &fakeTailReader{tail: CanonicalTail{
		ThroughSeq: 21,
		Complete:   true,
		Messages:   []CanonicalMessage{futureReply, privateMessage, priorReply, futureInput, priorInput},
	}}
	service := NewReadViewService(reader)

	view, err := service.BuildReadView(ctx, ReadViewRequest{
		Principal: Principal{OwnerID: ownerID, ActorID: ownerID},
		Session: SessionState{
			SessionID: sessionID, OwnerID: ownerID, RuntimeVersion: ConversationRuntimeV2,
			CutoverSeq: 5, AdmissionSeq: 20, ProjectionWatermark: 10, GenerationEpoch: 4,
		},
		Accepted: current,
		Legacy:   projection,
	})
	if err != nil {
		t.Fatalf("BuildReadView(): %v", err)
	}
	if reader.afterSeq != 10 {
		t.Fatalf("tail read after event_seq = %d, want min verified projection watermark 10", reader.afterSeq)
	}
	want := []PromptMessage{
		{Role: "user", Text: "legacy question", MessageID: "legacy-user", TurnID: legacyInput.Index.TurnID},
		{Role: "assistant", Text: "legacy answer", MessageID: "legacy-assistant", TurnID: legacyInput.Index.TurnID},
		{Role: "user", Text: "prior question canonical", MessageID: string(priorInput.Index.MessageID), TurnID: previousTurnID, EventSeq: 11},
		{Role: "assistant", Text: "prior answer canonical", MessageID: string(sharedMessageID), TurnID: previousTurnID, EventSeq: 18},
		{Role: "user", Text: "current input", MessageID: string(currentInput.MessageID), TurnID: currentTurnID, EventSeq: 15},
	}
	if !reflect.DeepEqual(view.Messages, want) {
		t.Fatalf("ReadView messages = %#v, want %#v", view.Messages, want)
	}
	if view.ThroughSeq != 21 {
		t.Fatalf("ReadView through event_seq = %d, want 21", view.ThroughSeq)
	}
}

func TestBuildReadViewReconcilesInterleavedPairsBeyondPartialWatermark(t *testing.T) {
	const ownerID, actorID = "ren", "viewer-user"
	sessionID := modulecore.NewSessionID()
	aTurnID, bTurnID := modulecore.NewTurnID(), modulecore.NewTurnID()
	currentTurnID, futureTurnID := modulecore.NewTurnID(), modulecore.NewTurnID()

	aInputEvent := userEvent(sessionID, aTurnID, 1, actorID, "A input from canonical identity")
	aReplyEvent := assistantEvent(sessionID, aTurnID, 3, string(modulecore.NewMessageID()), "A canonical response", "mio")
	bInputEvent := userEvent(sessionID, bTurnID, 2, actorID, "B input")
	bReplyEvent := assistantEvent(sessionID, bTurnID, 4, string(modulecore.NewMessageID()), "B response", "mio")
	currentInputEvent := userEvent(sessionID, currentTurnID, 5, actorID, "current accepted input")
	futureInputEvent := userEvent(sessionID, futureTurnID, 6, actorID, "future input must stay out")

	legacyAInput := legacyMessage(sessionID, aTurnID, string(aInputEvent.MessageID), "A input projection", actorID, "agent", "mio", "user_message", 1, 0)
	legacyAInput.EventSeq = aInputEvent.EventSeq
	legacyAReply := legacyMessage(sessionID, aTurnID, string(aReplyEvent.MessageID), "stale A response projection", "mio", "user", ownerID, "assistant_message", 1, 1)
	legacyAReply.EventSeq = aReplyEvent.EventSeq

	request := validReadViewRequest(ownerID, sessionID, currentTurnID, currentInputEvent)
	request.Principal.ActorID = actorID
	request.Session.CutoverSeq = 1
	request.Session.AdmissionSeq = futureInputEvent.EventSeq
	request.Session.ProjectionWatermark = 1
	request.Accepted.ActorID = actorID
	request.Legacy = LegacyProjection{
		AppliedThrough: 3,
		Complete:       true,
		Messages:       []LegacyMessage{legacyAInput, legacyAReply},
	}
	reader := &fakeTailReader{tail: CanonicalTail{
		ThroughSeq: 6,
		Complete:   true,
		Messages: []CanonicalMessage{
			canonicalMessage(bReplyEvent, "mio", "user", ownerID, "assistant_message"),
			canonicalMessage(aReplyEvent, "mio", "user", ownerID, "assistant_message"),
			canonicalMessage(futureInputEvent, actorID, "agent", "mio", "user_message"),
			canonicalMessage(bInputEvent, actorID, "agent", "mio", "user_message"),
			canonicalMessage(currentInputEvent, actorID, "agent", "mio", "user_message"),
		},
	}}

	view, err := NewReadViewService(reader).BuildReadView(context.Background(), request)
	if err != nil {
		t.Fatalf("BuildReadView(interleaved pairs): %v", err)
	}
	if reader.afterSeq != 1 {
		t.Fatalf("canonical tail read after event_seq = %d, want partial contiguous watermark 1", reader.afterSeq)
	}
	if view.ProjectionWatermark != 1 {
		t.Fatalf("ReadView projection watermark = %d, want 1", view.ProjectionWatermark)
	}
	want := []PromptMessage{
		{Role: "user", Text: "A input projection", MessageID: string(aInputEvent.MessageID), TurnID: aTurnID, EventSeq: 1},
		{Role: "assistant", Text: "A canonical response", MessageID: string(aReplyEvent.MessageID), TurnID: aTurnID, EventSeq: 3},
		{Role: "user", Text: "B input", MessageID: string(bInputEvent.MessageID), TurnID: bTurnID, EventSeq: 2},
		{Role: "assistant", Text: "B response", MessageID: string(bReplyEvent.MessageID), TurnID: bTurnID, EventSeq: 4},
		{Role: "user", Text: "current accepted input", MessageID: string(currentInputEvent.MessageID), TurnID: currentTurnID, EventSeq: 5},
	}
	if !reflect.DeepEqual(view.Messages, want) {
		t.Fatalf("ReadView messages = %#v, want canonical identities in causal pair order %#v", view.Messages, want)
	}
	for _, message := range view.Messages {
		if message.MessageID == string(futureInputEvent.MessageID) || message.Text == "future input must stay out" {
			t.Fatalf("ReadView included a future accepted input: %+v", message)
		}
	}
}

func TestBuildReadViewIncludesPublicClarificationPromptWithoutPrivateQuestion(t *testing.T) {
	fixture := newClarificationRuntimeFixture(t, domainconversation.ClarificationDecision{
		SchemaVersion: "mio.clarification.v1", Kind: domainconversation.ClarificationDecisionAskUser, Question: "Which environment?",
	})
	ownerID := fixture.request.Session.OwnerID
	controlTurnID := fixture.request.Accepted.TurnID
	prompt := modulecore.NewRootEventEnvelope("orchestrator", "agent.response", time.Unix(5, 0).UTC(), map[string]any{
		"kind": "clarification_prompt", "status": "awaiting_user_clarification", "availability": "free",
		"from": "mio", "source_actor_id": "mio", "to": "user", "visibility": "public",
		"text": "Which deployment region should I use?", "content": "Which deployment region should I use?",
		"delegation_id": string(fixture.context.Handoff.DelegationID), "delegation_revision": uint64(1),
		"reply_to_message_id": string(fixture.context.Handoff.ReplyTo),
	})
	prompt.SessionID, prompt.TurnID, prompt.MessageID, prompt.EventSeq = fixture.request.Session.SessionID, controlTurnID, modulecore.NewMessageID(), 5
	prompt.CausationEventID = fixture.request.Accepted.InputEvent.EventID
	privateQuestion := fixture.tail.Messages[len(fixture.tail.Messages)-1]
	nextInput := userEvent(fixture.request.Session.SessionID, modulecore.NewTurnID(), 6, ownerID, "Use the west region.")
	fixture.tail.Messages = append(fixture.tail.Messages[:len(fixture.tail.Messages)-1], privateQuestion,
		canonicalMessage(prompt, "mio", "user", ownerID, "clarification_prompt"),
		canonicalMessage(nextInput, ownerID, "agent", "mio", "user_message"))
	fixture.tail.ThroughSeq = 6
	request := validReadViewRequest(ownerID, fixture.request.Session.SessionID, nextInput.TurnID, nextInput)
	request.Session.CutoverSeq = 1
	request.Session.ProjectionWatermark = 1
	request.Legacy.AppliedThrough = 1

	view, err := NewReadViewService(&fakeTailReader{tail: fixture.tail}).BuildReadView(context.Background(), request)
	if err != nil {
		t.Fatalf("BuildReadView(): %v", err)
	}
	var promptFound, privateQuestionFound bool
	for _, message := range view.Messages {
		if message.MessageID == string(prompt.MessageID) && message.Text == "Which deployment region should I use?" {
			promptFound = true
		}
		if message.MessageID == string(fixture.request.Accepted.InputMessageID) || strings.Contains(message.Text, "private worker question") {
			privateQuestionFound = true
		}
	}
	if !promptFound {
		t.Fatalf("ReadView omitted the causal public clarification prompt: %+v", view.Messages)
	}
	if privateQuestionFound {
		t.Fatalf("ReadView exposed the prior private clarification question: %+v", view.Messages)
	}
}

func TestBuildReadViewClarificationControlKeepsOriginActorHistoryWithoutOtherUserData(t *testing.T) {
	fixture := newClarificationRuntimeFixture(t, domainconversation.ClarificationDecision{
		SchemaVersion: "mio.clarification.v1", Kind: domainconversation.ClarificationDecisionAskUser, Question: "Which environment?",
	})
	const ownerID, originActorID, otherActorID = "ren", "viewer-user", "other-user"
	fixture.request.Principal.OwnerID = ownerID
	fixture.request.Session.OwnerID = ownerID
	fixture.request.Session.CutoverSeq = 3
	fixture.request.Session.AdmissionSeq = 5
	fixture.request.Session.ProjectionWatermark = 3
	fixture.request.Legacy.AppliedThrough = 3
	fixture.context.OwnerID = ownerID
	fixture.context.OriginUserActorID = originActorID

	priorTurnID := modulecore.NewTurnID()
	priorInput := userEvent(fixture.request.Session.SessionID, priorTurnID, 1, originActorID, "Earlier user context")
	otherTurnID := modulecore.NewTurnID()
	otherInput := userEvent(fixture.request.Session.SessionID, otherTurnID, 2, otherActorID, "Other user's context must stay private")
	origin := fixture.tail.Messages[0]
	origin.Index.FromActor = originActorID
	origin.Event.Payload["source_actor_id"] = originActorID
	origin.Event.EventSeq = 3
	notice := fixture.tail.Messages[1]
	notice.Index.ToActor = ownerID
	notice.Event.EventSeq = 4
	question := fixture.tail.Messages[2]
	question.Event.EventSeq = 5
	question.Index.ToActor = "mio"
	fixture.tail.Messages = []CanonicalMessage{
		canonicalMessage(priorInput, originActorID, "agent", "mio", "user_message"),
		canonicalMessage(otherInput, otherActorID, "agent", "mio", "user_message"),
		origin, notice, question,
	}
	fixture.tail.ThroughSeq = 5
	fixture.request.Accepted.AdmissionSeq = 5
	fixture.request.Accepted.InputEvent.EventSeq = 5
	fixture.context.ControlTurn.AdmissionSeq = 5
	fixture.context.ControlTurn.InputEvent.EventSeq = 5
	fixture.context.QuestionEvent.EventSeq = 5
	fixture.context.Context = []DelegationDispatchMessage{
		{MessageID: priorInput.MessageID, Role: "user", Text: "Earlier user context"},
		{MessageID: origin.Event.MessageID, Role: "user", Text: "original user request"},
	}
	legacyPrior := legacyMessage(fixture.request.Session.SessionID, priorTurnID, string(priorInput.MessageID), "Earlier user context", originActorID, "agent", "mio", "user_message", 1, 0)
	legacyPrior.EventSeq = 1
	legacyOther := legacyMessage(fixture.request.Session.SessionID, otherTurnID, string(otherInput.MessageID), "Other user's context must stay private", otherActorID, "agent", "mio", "user_message", 2, 0)
	legacyOther.EventSeq = 2
	legacyOrigin := legacyMessage(fixture.request.Session.SessionID, origin.Index.TurnID, string(origin.Event.MessageID), "original user request", originActorID, "agent", "mio", "user_message", 3, 0)
	legacyOrigin.EventSeq = 3
	fixture.request.Legacy.Messages = []LegacyMessage{legacyPrior, legacyOther, legacyOrigin}

	reader := &clarificationTailReader{fakeTailReader: &fakeTailReader{tail: fixture.tail, filterAfter: true}, context: fixture.context}
	view, err := NewReadViewService(reader).BuildReadView(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("BuildReadView(clarification control with distinct origin actor): %v", err)
	}
	var priorFound, originFound, otherFound bool
	for _, message := range view.Messages {
		switch message.Text {
		case "Earlier user context":
			priorFound = message.Role == "user" && message.MessageID == string(priorInput.MessageID)
		case "original user request":
			originFound = message.Role == "user" && message.MessageID == string(origin.Event.MessageID)
		case "Other user's context must stay private":
			otherFound = true
		}
	}
	if !priorFound || !originFound {
		t.Fatalf("ReadView omitted allowed origin-actor history: prior=%t origin=%t messages=%+v", priorFound, originFound, view.Messages)
	}
	if otherFound {
		t.Fatalf("ReadView exposed another USER actor's history: %+v", view.Messages)
	}
	if reader.afterSeq != 3 {
		t.Fatalf("canonical tail read started at event_seq %d, want the session cutover watermark 3", reader.afterSeq)
	}
}

func TestBuildReadViewRejectsOwnerAndSessionMismatchBeforeReading(t *testing.T) {
	ownerID := "user-1"
	sessionID := modulecore.NewSessionID()
	turnID := modulecore.NewTurnID()
	input := userEvent(sessionID, turnID, 6, ownerID, "current")
	reader := &fakeTailReader{tail: CanonicalTail{ThroughSeq: 6, Complete: true}}
	service := NewReadViewService(reader)
	request := validReadViewRequest(ownerID, sessionID, turnID, input)
	request.Principal.OwnerID = "user-2"
	if _, err := service.BuildReadView(context.Background(), request); !errors.Is(err, ErrReadViewForbidden) {
		t.Fatalf("BuildReadView(owner mismatch) error = %v, want ErrReadViewForbidden", err)
	}
	if reader.calls != 0 {
		t.Fatalf("canonical tail reads = %d, want none before owner authorization", reader.calls)
	}
}

func TestBuildReadViewAcceptsAuthenticatedInputIdentityInPublicPayload(t *testing.T) {
	ownerID := "session-owner"
	actorID := "viewer-user"
	sessionID := modulecore.NewSessionID()
	turnID := modulecore.NewTurnID()
	input := userEvent(sessionID, turnID, 6, actorID, "current")
	input.ActorKind = ""
	input.ActorID = ""
	input.Payload = map[string]any{
		"from": "user", "source_actor_id": actorID, "visibility": "public",
		"text": "current", "content": "current",
	}
	request := validReadViewRequest(ownerID, sessionID, turnID, input)
	request.Principal.ActorID = actorID
	request.Accepted.ActorID = actorID
	request.Session.CutoverSeq = 0
	request.Session.ProjectionWatermark = 0
	request.Legacy.AppliedThrough = 0
	priorTurn := modulecore.NewTurnID()
	prior := canonicalMessage(userEvent(sessionID, priorTurn, 1, actorID, "prior authenticated user"), actorID, "agent", "mio", "user_message")
	reader := &fakeTailReader{tail: CanonicalTail{ThroughSeq: 6, Complete: true, Messages: []CanonicalMessage{prior}}}

	view, err := NewReadViewService(reader).BuildReadView(context.Background(), request)
	if err != nil {
		t.Fatalf("BuildReadView(v2 public input): %v", err)
	}
	if len(view.Messages) != 2 || view.Messages[0].Text != "prior authenticated user" || view.Messages[0].Role != "user" ||
		view.Messages[1].Text != "current" || view.Messages[1].Role != "user" {
		t.Fatalf("ReadView messages = %#v, want the accepted public input", view.Messages)
	}
}

func TestBuildReadViewBuildsBoundedUntrustedReportPromptFromTrustedCanonicalContext(t *testing.T) {
	request, tail, want := reportReadViewFixture(t)
	reader := &fakeTailReader{tail: tail}
	view, err := NewReadViewService(reader).BuildReadView(context.Background(), request)
	if err != nil {
		t.Fatalf("BuildReadView(report reply): %v", err)
	}
	if len(view.Messages) != 1 {
		t.Fatalf("report reply prompt messages = %d, want one bounded report envelope", len(view.Messages))
	}
	prompt := view.Messages[0]
	if prompt.Role != "user" || prompt.MessageID != string(request.Accepted.InputMessageID) || prompt.TurnID != request.Accepted.TurnID {
		t.Fatalf("report prompt identity = %+v, want the accepted private report identity", prompt)
	}
	if prompt.Text == want.Report.Summary {
		t.Fatal("private agent report was projected as an ordinary user message")
	}
	for _, expected := range []string{
		"untrusted", "report_untrusted_data", want.OriginalRequest, want.DelegationNotice,
		want.DelegationInstruction, want.ConstraintText, want.Readback, want.Report.Result,
		string(want.Report.ReportID), string(want.DelegationID), string(want.Report.WorkTaskID),
	} {
		if !strings.Contains(prompt.Text, expected) {
			t.Fatalf("bounded report prompt omitted %q: %s", expected, prompt.Text)
		}
	}
	if len([]byte(prompt.Text)) > 256*1024 {
		t.Fatalf("report prompt bytes = %d, limit %d", len([]byte(prompt.Text)), 256*1024)
	}
	if reader.calls != 1 {
		t.Fatalf("canonical tail reads = %d, want one", reader.calls)
	}
	if reader.afterSeq != request.Session.CutoverSeq {
		t.Fatalf("report canonical tail read starts after seq %d, want cutover %d to recover original handoff context", reader.afterSeq, request.Session.CutoverSeq)
	}
}

func TestBuildReadViewRejectsReportInputThatSpoofsTheTrustedAssignee(t *testing.T) {
	request, tail, _ := reportReadViewFixture(t)
	request.Accepted.InputEvent.ActorID = "mio"
	reader := &fakeTailReader{tail: tail}
	if _, err := NewReadViewService(reader).BuildReadView(context.Background(), request); !errors.Is(err, ErrReadViewInvalid) {
		t.Fatalf("BuildReadView(spoofed report actor) error = %v, want ErrReadViewInvalid", err)
	}
	if reader.calls != 0 {
		t.Fatalf("canonical tail reads = %d, want none before trusted report validation", reader.calls)
	}
}

func TestBuildReadViewEnforcesReportOwnerAndAssigneeACLBeforeReading(t *testing.T) {
	for _, mutate := range []struct {
		name  string
		apply func(*ReadViewRequest)
	}{
		{name: "different owner", apply: func(request *ReadViewRequest) { request.Principal.OwnerID = "owner-2" }},
		{name: "different actor", apply: func(request *ReadViewRequest) { request.Principal.ActorID = "mio" }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			request, tail, _ := reportReadViewFixture(t)
			mutate.apply(&request)
			reader := &fakeTailReader{tail: tail}
			if _, err := NewReadViewService(reader).BuildReadView(context.Background(), request); !errors.Is(err, ErrReadViewForbidden) {
				t.Fatalf("BuildReadView(report ACL mismatch) error = %v, want ErrReadViewForbidden", err)
			}
			if reader.calls != 0 {
				t.Fatalf("canonical tail reads = %d, want none before report ACL validation", reader.calls)
			}
		})
	}
}

func TestBuildReadViewRejectsReportWithStaleDelegationContextRevision(t *testing.T) {
	request, tail, _ := reportReadViewFixture(t)
	for index := range tail.Messages {
		if tail.Messages[index].Index.Kind == "delegation_notice" {
			tail.Messages[index].Event.Payload["delegation_revision"] = uint64(2)
		}
	}
	reader := &fakeTailReader{tail: tail}
	if _, err := NewReadViewService(reader).BuildReadView(context.Background(), request); !errors.Is(err, ErrReadViewInvalid) {
		t.Fatalf("BuildReadView(stale delegation context revision) error = %v, want ErrReadViewInvalid", err)
	}
}

type reportReadViewWant struct {
	Report                domainconversation.ReportEnvelope
	DelegationID          modulecore.RequestID
	OriginalRequest       string
	DelegationNotice      string
	DelegationInstruction string
	ConstraintText        string
	Readback              string
}

func reportReadViewFixture(t *testing.T) (ReadViewRequest, CanonicalTail, reportReadViewWant) {
	t.Helper()
	const ownerID, sender = "owner-1", "shiro"
	sessionID := modulecore.NewSessionID()
	originTurnID := modulecore.NewTurnID()
	originTraceID := modulecore.NewTraceID()
	originText := "元の依頼: モデルは変更しないで、保存待ちを調べて。"
	origin := userEvent(sessionID, originTurnID, 10, "viewer-user", originText)
	origin.TraceID = originTraceID

	delegationID := modulecore.NewRequestID()
	noticeText := "Shiroに依頼したよ。"
	notice := assistantEvent(sessionID, originTurnID, 11, string(modulecore.NewMessageID()), noticeText, "mio")
	notice.TraceID = originTraceID
	notice.Payload["kind"] = "delegation_notice"
	notice.Payload["delegation_id"] = string(delegationID)
	notice.Payload["delegation_revision"] = uint64(1)
	notice.Payload["reply_to_message_id"] = string(origin.MessageID)

	constraintText := "モデルを変更しない"
	constraints := []domainconversation.AgentConstraint{{
		Key: "keep_model", Text: constraintText, SourceMessageIDs: []modulecore.MessageID{origin.MessageID},
	}}
	instructionText := "保存待ちを計測し、根拠付きで報告する"
	instructionID := modulecore.NewMessageID()
	instruction := modulecore.NewEventEnvelope(originTraceID, notice.EventID, nil, "delegation_mailbox", "conversation.delegation_instruction", time.Unix(12, 0).UTC(), map[string]any{
		"from": "mio", "source_actor_id": "mio", "to": sender, "goal": "保存待ちを調べる",
		"text": instructionText, "constraints": constraints, "expected_output": "根拠付きの報告",
		"source_message_ids": []modulecore.MessageID{origin.MessageID},
	})
	instruction.SessionID, instruction.TurnID, instruction.MessageID = sessionID, originTurnID, instructionID
	instruction.EventSeq = 12

	readbackText := "条件を理解したよ。モデルは変更しない。"
	readback := assistantEvent(sessionID, originTurnID, 13, string(modulecore.NewMessageID()), readbackText, sender)
	readback.TraceID = originTraceID
	readback.Payload["kind"] = "readback"
	readback.Payload["delegation_id"] = string(delegationID)
	readback.Payload["delegation_revision"] = uint64(1)
	readback.Payload["status"] = "accepted"
	readback.Payload["reply_to_message_id"] = string(origin.MessageID)
	readback.Payload["from"] = sender
	readback.RequestID = delegationID
	readback.CausationEventID = instruction.EventID

	workTaskID, workRunID := modulecore.NewTaskID(), modulecore.NewRunID()
	report := domainconversation.ReportEnvelope{
		ReportID: modulecore.NewResponseID(), ReportRevision: 1,
		DelegationID: delegationID, DelegationRevision: 1,
		WorkTaskID: workTaskID, WorkRunID: workRunID,
		Status:  domainconversation.ReportStatusSucceeded,
		Summary: "保存待ちの原因を特定した。", Result: "SQLite commit後にprojection待ちが発生していた。",
		EvidenceRefs: []modulecore.EvidenceID{modulecore.NewEvidenceID()},
		Limitations:  []string{"別platformの確認は未実施"},
	}
	reportTurnID, reportTraceID := modulecore.NewTurnID(), modulecore.NewTraceID()
	reportMessageID := modulecore.NewMessageID()
	reportEvent := modulecore.NewEventEnvelope(reportTraceID, "", nil, "conversation_runtime", "conversation.agent_report", time.Unix(14, 0).UTC(), map[string]any{
		"kind": "agent_report", "visibility": "private", "from": sender, "source_actor_id": sender,
		"to": "mio", "text": report.Summary, "trusted_sender": sender,
		"origin_trace_id": string(originTraceID), "origin_turn_id": string(originTurnID),
		"origin_message_id": string(origin.MessageID), "delegation_id": string(delegationID),
		"delegation_revision": uint64(1), "report_id": string(report.ReportID), "report_revision": uint64(1),
		"work_task_id": string(workTaskID), "work_run_id": string(workRunID), "report": report,
	})
	reportEvent.ActorKind, reportEvent.ActorID = "agent", sender
	reportEvent.SessionID, reportEvent.TurnID, reportEvent.MessageID = sessionID, reportTurnID, reportMessageID
	reportEvent.RequestID, reportEvent.ResponseID = delegationID, report.ReportID
	reportEvent.TaskID, reportEvent.RunID = workTaskID, workRunID
	reportEvent.EventSeq = 15

	legacy := LegacyProjection{AppliedThrough: 14, Complete: true}
	accepted := AcceptedTurn{
		SessionID: sessionID, TurnID: reportTurnID, ActorID: sender, TriggerKind: "report_reply",
		ClientSubmissionID: "report:" + string(report.ReportID), InputMessageID: reportMessageID,
		AdmissionSeq: reportEvent.EventSeq, GenerationEpoch: 1, State: "accepted", InputEvent: reportEvent,
	}
	request := ReadViewRequest{
		Principal: Principal{OwnerID: ownerID, ActorID: sender},
		Session: SessionState{
			SessionID: sessionID, OwnerID: ownerID, RuntimeVersion: ConversationRuntimeV2,
			CutoverSeq: 5, AdmissionSeq: reportEvent.EventSeq, ProjectionWatermark: 14, GenerationEpoch: 1,
		},
		Accepted: accepted, Legacy: legacy,
	}
	privateInstruction := canonicalMessage(instruction, "mio", "agent", sender, "delegation_instruction")
	privateInstruction.Index.Visibility = "private"
	privateReport := canonicalMessage(reportEvent, sender, "agent", "mio", "agent_report")
	privateReport.Index.Visibility = "private"
	noticeMessage := canonicalMessage(notice, "mio", "user", ownerID, "delegation_notice")
	noticeMessage.Index.ReplyToMessageID = origin.MessageID
	readbackMessage := canonicalMessage(readback, sender, "user", ownerID, "readback")
	readbackMessage.Index.ReplyToMessageID = origin.MessageID
	tail := CanonicalTail{
		ThroughSeq: reportEvent.EventSeq, Complete: true,
		Messages: []CanonicalMessage{
			canonicalMessage(origin, "viewer-user", "agent", "mio", "user_message"),
			noticeMessage,
			privateInstruction,
			readbackMessage,
			privateReport,
		},
	}
	return request, tail, reportReadViewWant{
		Report: report, DelegationID: delegationID, OriginalRequest: originText,
		DelegationNotice: noticeText, DelegationInstruction: instructionText,
		ConstraintText: constraintText, Readback: readbackText,
	}
}

func TestBuildReadViewRejectsPublicInputWithMismatchedTrustedActor(t *testing.T) {
	ownerID := "owner-1"
	sessionID := modulecore.NewSessionID()
	turnID := modulecore.NewTurnID()
	input := userEvent(sessionID, turnID, 6, ownerID, "current")
	input.Payload["source_actor_id"] = "different-actor"
	request := validReadViewRequest(ownerID, sessionID, turnID, input)
	reader := &fakeTailReader{tail: CanonicalTail{ThroughSeq: 6, Complete: true}}
	if _, err := NewReadViewService(reader).BuildReadView(context.Background(), request); !errors.Is(err, ErrReadViewInvalid) {
		t.Fatalf("BuildReadView(mismatched source actor) error = %v, want ErrReadViewInvalid", err)
	}
	if reader.calls != 0 {
		t.Fatalf("tail reader calls = %d, want no read before input identity validation", reader.calls)
	}
}

func TestBuildReadViewRejectsIncompleteProjectionOrCanonicalTail(t *testing.T) {
	ownerID := "user-1"
	sessionID := modulecore.NewSessionID()
	turnID := modulecore.NewTurnID()
	input := userEvent(sessionID, turnID, 6, ownerID, "current")
	request := validReadViewRequest(ownerID, sessionID, turnID, input)
	reader := &fakeTailReader{tail: CanonicalTail{ThroughSeq: 6, Complete: true}}
	service := NewReadViewService(reader)

	request.Legacy.Complete = false
	if _, err := service.BuildReadView(context.Background(), request); !errors.Is(err, ErrReadViewIncompleteProjection) {
		t.Fatalf("BuildReadView(incomplete projection) error = %v, want ErrReadViewIncompleteProjection", err)
	}

	request.Legacy.Complete = true
	request.Legacy.Gaps = []LegacyProjectionGap{{EventSeq: 4, EventID: modulecore.NewEventID(), Reason: "unreconstructable legacy message"}}
	if _, err := service.BuildReadView(context.Background(), request); !errors.Is(err, ErrReadViewIncompleteProjection) {
		t.Fatalf("BuildReadView(projection with gaps) error = %v, want ErrReadViewIncompleteProjection", err)
	}
	request.Legacy.Gaps = nil
	reader.tail.Complete = false
	if _, err := service.BuildReadView(context.Background(), request); !errors.Is(err, ErrReadViewIncompleteTail) {
		t.Fatalf("BuildReadView(incomplete tail) error = %v, want ErrReadViewIncompleteTail", err)
	}
}

func TestBuildReadViewPropagatesTailReadFailureWithoutReturningPartialContext(t *testing.T) {
	ownerID := "user-1"
	sessionID := modulecore.NewSessionID()
	turnID := modulecore.NewTurnID()
	input := userEvent(sessionID, turnID, 6, ownerID, "current")
	reader := &fakeTailReader{err: errors.New("tail unavailable")}
	service := NewReadViewService(reader)
	view, err := service.BuildReadView(context.Background(), validReadViewRequest(ownerID, sessionID, turnID, input))
	if err == nil || !errors.Is(err, reader.err) {
		t.Fatalf("BuildReadView() error = %v, want tail read error", err)
	}
	if len(view.Messages) != 0 {
		t.Fatalf("partial ReadView returned %d messages on read failure", len(view.Messages))
	}
}

type fakeTailReader struct {
	tail        CanonicalTail
	err         error
	calls       int
	afterSeq    modulecore.EventSeq
	filterAfter bool
}

func (f *fakeTailReader) ReadCompleteSessionTail(_ context.Context, _ modulecore.SessionID, after modulecore.EventSeq) (CanonicalTail, error) {
	f.calls++
	f.afterSeq = after
	tail := f.tail
	if f.filterAfter {
		tail.Messages = make([]CanonicalMessage, 0, len(f.tail.Messages))
		for _, message := range f.tail.Messages {
			if message.Event.EventSeq > after {
				tail.Messages = append(tail.Messages, message)
			}
		}
	}
	return tail, f.err
}

func validReadViewRequest(ownerID string, sessionID modulecore.SessionID, turnID modulecore.TurnID, input modulecore.EventEnvelope) ReadViewRequest {
	return ReadViewRequest{
		Principal: Principal{OwnerID: ownerID, ActorID: ownerID},
		Session: SessionState{
			SessionID: sessionID, OwnerID: ownerID, RuntimeVersion: ConversationRuntimeV2,
			CutoverSeq: 5, AdmissionSeq: input.EventSeq, ProjectionWatermark: 5, GenerationEpoch: 1,
		},
		Accepted: AcceptedTurn{
			SessionID: sessionID, TurnID: turnID, ActorID: ownerID,
			TriggerKind: "user_message", InputMessageID: input.MessageID, AdmissionSeq: input.EventSeq,
			GenerationEpoch: 1, State: "accepted", InputEvent: input,
		},
		Legacy: LegacyProjection{AppliedThrough: 5, Complete: true},
	}
}

func userEvent(sessionID modulecore.SessionID, turnID modulecore.TurnID, seq modulecore.EventSeq, actorID, text string) modulecore.EventEnvelope {
	event := modulecore.NewRootEventEnvelope("orchestrator", "conversation.user_message", time.Unix(int64(seq), 0).UTC(), map[string]any{
		"from": "user", "source_actor_id": actorID, "visibility": "public", "text": text, "content": text,
	})
	event.EventSeq = seq
	event.SessionID = sessionID
	event.TurnID = turnID
	event.MessageID = modulecore.NewMessageID()
	return event
}

func assistantEvent(sessionID modulecore.SessionID, turnID modulecore.TurnID, seq modulecore.EventSeq, messageID, text, actorID string) modulecore.EventEnvelope {
	event := modulecore.NewRootEventEnvelope("orchestrator", "agent.response", time.Unix(int64(100-seq), 0).UTC(), map[string]any{
		"from": "mio", "source_actor_id": actorID, "to": "user", "visibility": "public", "text": text, "content": text,
	})
	event.EventSeq = seq
	event.SessionID = sessionID
	event.TurnID = turnID
	event.MessageID = modulecore.MessageID(messageID)
	return event
}

func canonicalMessage(event modulecore.EventEnvelope, fromActor, toKind, toActor, kind string) CanonicalMessage {
	return CanonicalMessage{
		Event: event,
		Index: MessageIndex{
			MessageID: event.MessageID, EventID: event.EventID, SessionID: event.SessionID, TurnID: event.TurnID,
			FromActor: fromActor, ToKind: toKind, ToActor: toActor, Kind: kind, Visibility: "public",
		},
	}
}

func legacyMessage(sessionID modulecore.SessionID, turnID modulecore.TurnID, messageID, text, fromActor, toKind, toActor, kind string, turnOrder, messageOrder int64) LegacyMessage {
	return LegacyMessage{
		Index: MessageIndex{
			MessageID: modulecore.MessageID(messageID), SessionID: sessionID, TurnID: turnID,
			FromActor: fromActor, ToKind: toKind, ToActor: toActor, Kind: kind, Visibility: "public",
		},
		Text: text, TurnOrder: turnOrder, MessageOrder: messageOrder,
	}
}
