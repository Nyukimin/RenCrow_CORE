package voiceinput

import (
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type recordingEmitter struct {
	events []recordedEvent
}

type recordedEvent struct {
	Type      string
	From      string
	To        string
	Content   string
	Route     string
	JobID     string
	SessionID string
	Channel   string
	ChatID    string
	MessageID string
}

func (e *recordingEmitter) Emit(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) {
	e.events = append(e.events, recordedEvent{
		Type: eventType, From: from, To: to, Content: content, Route: route,
		JobID: jobID, SessionID: sessionID, Channel: channel, ChatID: chatID,
	})
}

func (e *recordingEmitter) EmitWithMessageID(eventType, from, to, content, route, jobID, sessionID, channel, chatID, messageID string) {
	e.Emit(eventType, from, to, content, route, jobID, sessionID, channel, chatID)
	e.events[len(e.events)-1].MessageID = messageID
}

func newPublisherInput(t *testing.T, sessionID, channel, chatID, messageText string) conversation.TurnInput {
	t.Helper()
	address, err := conversation.NewChannelAddress(channel, chatID)
	if err != nil {
		t.Fatalf("NewChannelAddress() error = %v", err)
	}
	input, err := conversation.NewTurnInput(modulecore.NewTaskID(), messageText, address)
	if err != nil {
		t.Fatalf("NewTurnInput() error = %v", err)
	}
	return input.
		WithSessionID(sessionID).
		WithViewerRecipient("mio").
		WithRoute(routing.RouteCHAT)
}

func inputForResult(t *testing.T, result Result) conversation.TurnInput {
	t.Helper()
	return newPublisherInput(t, result.SessionID, result.Channel, result.ChatID, result.UserText)
}

type recordingTurnLogger struct {
	user      string
	assistant string
}

type recordingPublisherCorrelatedTurnLogger struct {
	userCalls          int
	assistantCalls     int
	userMessageID      string
	assistantMessageID string
	userTraceID        string
	assistantTraceID   string
	userSessionID      string
	assistantSessionID string
	userChannel        string
	assistantChannel   string
	assistantRoute     string
	assistantJobID     string
}

func (l *recordingTurnLogger) WriteUser(sessionID, channel, content string) {
	l.user = content
}

func (l *recordingTurnLogger) WriteAssistant(sessionID, channel, route, jobID, content string) {
	l.assistant = content
}

func (l *recordingPublisherCorrelatedTurnLogger) WriteUser(sessionID, channel, content string) {
	l.userCalls++
}

func (l *recordingPublisherCorrelatedTurnLogger) WriteAssistant(sessionID, channel, route, jobID, content string) {
	l.assistantCalls++
}

func (l *recordingPublisherCorrelatedTurnLogger) WriteUserWithIdentity(sessionID, channel, messageID, traceID, content string) {
	l.userCalls++
	l.userMessageID = messageID
	l.userTraceID = traceID
	l.userSessionID = sessionID
	l.userChannel = channel
}

func (l *recordingPublisherCorrelatedTurnLogger) WriteAssistantWithIdentity(sessionID, channel, route, jobID, messageID, traceID, content string) {
	l.assistantCalls++
	l.assistantMessageID = messageID
	l.assistantTraceID = traceID
	l.assistantSessionID = sessionID
	l.assistantChannel = channel
	l.assistantRoute = route
	l.assistantJobID = jobID
}

func TestPublisherRejectsMissingTraceIDBeforeEmittingOrLogging(t *testing.T) {
	emitter := &recordingEmitter{}
	logger := &recordingPublisherCorrelatedTurnLogger{}
	jobCalls := 0
	publisher := Publisher{
		Events:     emitter,
		TurnLogger: logger,
		Input:      conversation.TurnInput{},
		NewJobID: func() string {
			jobCalls++
			return modulecore.NewTaskID().String()
		},
	}

	_, err := publisher.Publish(Result{
		Mode:        ModeLLM,
		UtteranceID: "utt-missing-trace",
		SessionID:   "viewer",
		Channel:     "viewer",
		ChatID:      "default",
		UserText:    "入力",
		Reply:       "応答",
		RawFinal:    "応答",
		Source:      "RenCrow_LLM llm.final",
	})
	if err == nil {
		t.Fatal("expected missing publisher trace_id to fail closed")
	}
	if len(emitter.events) != 0 {
		t.Fatalf("missing trace_id must not emit events: %#v", emitter.events)
	}
	if logger.userCalls != 0 || logger.assistantCalls != 0 {
		t.Fatalf("missing trace_id must not write session logs: %+v", logger)
	}
	if jobCalls != 0 {
		t.Fatalf("missing trace_id must fail before allocating a job: calls=%d", jobCalls)
	}
}

func TestPublisherDoesNotReuseJobIDAsTraceID(t *testing.T) {
	jobID := modulecore.NewTaskID().String()
	result := Result{
		Mode:        ModeLLM,
		UtteranceID: "utt-trace-reuse",
		SessionID:   "viewer",
		Channel:     "viewer",
		ChatID:      "default",
		UserText:    "入力",
		Reply:       "応答",
		RawFinal:    "応答",
		Source:      "RenCrow_LLM llm.final",
	}
	input := inputForResult(t, result)
	published, err := (Publisher{
		Input:    input,
		NewJobID: func() string { return jobID },
	}).Publish(result)
	if err != nil {
		t.Fatalf("Publish failed: %v", err)
	}
	if published.TraceID != string(input.TraceID()) || modulecore.TraceID(published.TraceID).Validate() != nil || published.TraceID == published.JobID {
		t.Fatalf("publisher must return an independent canonical trace_id: %+v", published)
	}
}

func TestPublisherPassesExplicitTraceIDToCorrelatedSessionLogs(t *testing.T) {
	logger := &recordingPublisherCorrelatedTurnLogger{}
	result := Result{
		Mode:        ModeLLM,
		UtteranceID: "utt-correlated-trace",
		SessionID:   "viewer",
		Channel:     "viewer",
		ChatID:      "default",
		UserText:    "入力",
		Reply:       "応答",
		RawFinal:    "応答",
		Source:      "RenCrow_LLM llm.final",
	}
	input := inputForResult(t, result)
	published, err := (Publisher{
		TurnLogger: logger,
		Input:      input,
		NewJobID:   func() string { return modulecore.NewTaskID().String() },
	}).Publish(result)
	if err != nil {
		t.Fatalf("Publish failed: %v", err)
	}
	if published.TraceID != string(input.TraceID()) {
		t.Fatalf("publish result trace_id=%q, want explicit %q", published.TraceID, input.TraceID())
	}
	if logger.userTraceID != string(input.TraceID()) || logger.assistantTraceID != string(input.TraceID()) {
		t.Fatalf("correlated session log traces user=%q assistant=%q, want %q", logger.userTraceID, logger.assistantTraceID, input.TraceID())
	}
}

func TestPublisherUsesTurnInputIdentitiesAndBoundary(t *testing.T) {
	result := Result{
		Mode:        ModeLLM,
		UtteranceID: "utt-canonical-input",
		SessionID:   "session-1",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		UserText:    "入力",
		Reply:       "応答",
		RawFinal:    "応答",
		Source:      "RenCrow_LLM llm.final",
	}
	input := newPublisherInput(t, result.SessionID, result.Channel, result.ChatID, result.UserText)
	emitter := &recordingEmitter{}
	logger := &recordingPublisherCorrelatedTurnLogger{}
	jobID := "job-independent"
	published, err := (Publisher{
		Events:     emitter,
		TurnLogger: logger,
		Input:      input,
		NewJobID:   func() string { return jobID },
	}).Publish(result)
	if err != nil {
		t.Fatalf("Publish failed: %v", err)
	}
	if published.Input.RootTaskID() != input.RootTaskID() || published.Input.TurnID() != input.TurnID() || published.Input.TraceID() != input.TraceID() || published.Input.UserMessageID() != input.UserMessageID() || published.Input.AgentMessageID() != input.AgentMessageID() || published.Input.MessageText() != input.MessageText() || published.Input.ChannelAddress() != input.ChannelAddress() || published.Input.SessionID() != input.SessionID() || published.Input.ViewerRecipient() != input.ViewerRecipient() || published.Input.Route() != input.Route() {
		t.Fatalf("published input changed: got=%#v want=%#v", published.Input, input)
	}
	if published.JobID != jobID || published.MessageID != string(input.AgentMessageID()) || published.TraceID != string(input.TraceID()) {
		t.Fatalf("published identity = %+v, input=%#v", published, input)
	}
	if len(emitter.events) != 3 {
		t.Fatalf("expected three events, got %#v", emitter.events)
	}
	userEvent := emitter.events[0]
	if userEvent.Type != "message.received" || userEvent.MessageID != string(input.UserMessageID()) || userEvent.JobID != jobID || userEvent.SessionID != input.SessionID() || userEvent.Channel != input.ChannelAddress().ChannelType() || userEvent.ChatID != input.ChannelAddress().ExternalConversationID() {
		t.Fatalf("user event identity/boundary = %#v, input=%#v", userEvent, input)
	}
	responseEvent := emitter.events[2]
	if responseEvent.Type != "agent.response" || responseEvent.MessageID != string(input.AgentMessageID()) || responseEvent.Route != string(input.Route()) || responseEvent.JobID != jobID || responseEvent.SessionID != input.SessionID() || responseEvent.Channel != input.ChannelAddress().ChannelType() || responseEvent.ChatID != input.ChannelAddress().ExternalConversationID() {
		t.Fatalf("response event identity/boundary = %#v, input=%#v", responseEvent, input)
	}
	if logger.userMessageID != string(input.UserMessageID()) || logger.userTraceID != string(input.TraceID()) || logger.userSessionID != input.SessionID() || logger.userChannel != input.ChannelAddress().ChannelType() {
		t.Fatalf("user log identity/boundary = %+v, input=%#v", logger, input)
	}
	if logger.assistantMessageID != string(input.AgentMessageID()) || logger.assistantTraceID != string(input.TraceID()) || logger.assistantSessionID != input.SessionID() || logger.assistantChannel != input.ChannelAddress().ChannelType() || logger.assistantRoute != string(input.Route()) || logger.assistantJobID != jobID {
		t.Fatalf("assistant log identity/boundary = %+v, input=%#v", logger, input)
	}
	if input.UserMessageID() == input.AgentMessageID() {
		t.Fatal("user and agent message IDs must remain distinct")
	}
}

func TestPublisherRejectsMissingMalformedAndBoundaryMismatchBeforeSideEffects(t *testing.T) {
	baseResult := Result{
		Mode:        ModeLLM,
		UtteranceID: "utt-boundary-reject",
		SessionID:   "session-1",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		UserText:    "入力",
		Reply:       "応答",
		RawFinal:    "応答",
		Source:      "RenCrow_LLM llm.final",
	}
	validInput := newPublisherInput(t, baseResult.SessionID, baseResult.Channel, baseResult.ChatID, baseResult.UserText)
	tests := []struct {
		name   string
		input  conversation.TurnInput
		result Result
	}{
		{name: "missing input", input: conversation.TurnInput{}, result: baseResult},
		{name: "user text mismatch", input: validInput, result: func() Result { r := baseResult; r.UserText = "別の入力"; return r }()},
		{name: "session mismatch", input: validInput, result: func() Result { r := baseResult; r.SessionID = "session-2"; return r }()},
		{name: "channel mismatch", input: validInput, result: func() Result { r := baseResult; r.Channel = "slack"; return r }()},
		{name: "chat mismatch", input: validInput, result: func() Result { r := baseResult; r.ChatID = "other-user"; return r }()},
		{name: "route mismatch", input: validInput.WithRoute(routing.RouteCODE1), result: baseResult},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			emitter := &recordingEmitter{}
			logger := &recordingPublisherCorrelatedTurnLogger{}
			jobCalls := 0
			_, err := (Publisher{
				Events:     emitter,
				TurnLogger: logger,
				Input:      tc.input,
				NewJobID: func() string {
					jobCalls++
					return "job-should-not-be-allocated"
				},
			}).Publish(tc.result)
			if err == nil {
				t.Fatal("invalid input boundary was accepted")
			}
			if len(emitter.events) != 0 {
				t.Fatalf("rejected input emitted events: %#v", emitter.events)
			}
			if logger.userCalls != 0 || logger.assistantCalls != 0 {
				t.Fatalf("rejected input wrote session logs: %+v", logger)
			}
			if jobCalls != 0 {
				t.Fatalf("rejected input allocated a job: calls=%d", jobCalls)
			}
		})
	}
}

func TestPublisherRejectsJobIDCollisionWithAnyTurnInputIdentity(t *testing.T) {
	result := Result{
		Mode:        ModeLLM,
		UtteranceID: "utt-job-collision",
		SessionID:   "session-1",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		UserText:    "入力",
		Reply:       "応答",
		RawFinal:    "応答",
		Source:      "RenCrow_LLM llm.final",
	}
	input := newPublisherInput(t, result.SessionID, result.Channel, result.ChatID, result.UserText)
	identities := []struct {
		name  string
		value string
	}{
		{name: "turn", value: string(input.TurnID())},
		{name: "trace", value: string(input.TraceID())},
		{name: "root task", value: string(input.RootTaskID())},
		{name: "user message", value: string(input.UserMessageID())},
		{name: "agent message", value: string(input.AgentMessageID())},
	}
	for _, identity := range identities {
		t.Run(identity.name, func(t *testing.T) {
			emitter := &recordingEmitter{}
			logger := &recordingPublisherCorrelatedTurnLogger{}
			_, err := (Publisher{
				Events:     emitter,
				TurnLogger: logger,
				Input:      input,
				NewJobID:   func() string { return identity.value },
			}).Publish(result)
			if err == nil {
				t.Fatal("job/input identity collision was accepted")
			}
			if len(emitter.events) != 0 || logger.userCalls != 0 || logger.assistantCalls != 0 {
				t.Fatalf("job collision caused side effects: events=%#v logger=%+v", emitter.events, logger)
			}
		})
	}
}

func TestPublisherRejectsMissingUserTextBeforeEmittingOrLogging(t *testing.T) {
	emitter := &recordingEmitter{}
	logger := &recordingPublisherCorrelatedTurnLogger{}
	jobCalls := 0
	publisher := Publisher{
		Events:     emitter,
		TurnLogger: logger,
		Input:      newPublisherInput(t, "viewer", "viewer", "default", "入力"),
		NewJobID: func() string {
			jobCalls++
			return modulecore.NewTaskID().String()
		},
	}

	_, err := publisher.Publish(Result{
		Mode:        ModeLLM,
		UtteranceID: "utt-missing-user-text",
		SessionID:   "viewer",
		Channel:     "viewer",
		ChatID:      "default",
		Reply:       "応答",
		RawFinal:    "応答",
		Source:      "RenCrow_LLM llm.final",
	})
	if err == nil {
		t.Fatal("expected missing LLM user_text to fail closed")
	}
	if len(emitter.events) != 0 {
		t.Fatalf("missing user_text must not emit events: %#v", emitter.events)
	}
	if logger.userCalls != 0 || logger.assistantCalls != 0 {
		t.Fatalf("missing user_text must not write session logs: %+v", logger)
	}
	if jobCalls != 0 {
		t.Fatalf("missing user_text must fail before allocating a job: calls=%d", jobCalls)
	}
}

func TestPublisherPublishesUserTextAndReplyOnly(t *testing.T) {
	emitter := &recordingEmitter{}
	logger := &recordingTurnLogger{}
	publisher := Publisher{
		Events:     emitter,
		TurnLogger: logger,
		Input:      newPublisherInput(t, "viewer", "viewer", "default", "Mioさんいますか"),
		NewJobID:   func() string { return "job-1" },
	}
	_, err := publisher.Publish(Result{
		Mode:        ModeLLM,
		UtteranceID: "utt-1",
		SessionID:   "viewer",
		Channel:     "viewer",
		ChatID:      "default",
		UserText:    "Mioさんいますか",
		Reply:       "はい、います。",
		RawFinal:    `{"user_text":"Mioさんいますか","reply":"はい、います。"}`,
		Source:      "RenCrow_LLM llm.final",
		Timings:     Timings{StartedAt: time.Now()},
	})
	if err != nil {
		t.Fatalf("Publish failed: %v", err)
	}
	if len(emitter.events) != 3 {
		t.Fatalf("expected three chat events, got %#v", emitter.events)
	}
	if emitter.events[0].Type != "message.received" || emitter.events[0].Content != "Mioさんいますか" {
		t.Fatalf("expected user text event, got %#v", emitter.events[0])
	}
	if emitter.events[2].Type != "agent.response" || emitter.events[2].Content != "はい、います。" {
		t.Fatalf("expected reply event, got %#v", emitter.events[2])
	}
	if logger.user != "Mioさんいますか" || logger.assistant != "はい、います。" {
		t.Fatalf("unexpected session log content: user=%q assistant=%q", logger.user, logger.assistant)
	}
}

func TestPublisherMarksVoiceInputAsVoiceChatSurface(t *testing.T) {
	emitter := &recordingEmitter{}
	publisher := Publisher{
		Events:   emitter,
		Input:    newPublisherInput(t, "viewer", "viewer", "default", "入力"),
		NewJobID: func() string { return "job-1" },
	}
	_, err := publisher.Publish(Result{
		Mode:        ModeLLM,
		UtteranceID: "utt-1",
		SessionID:   "viewer",
		Channel:     "viewer",
		ChatID:      "default",
		UserText:    "入力",
		Reply:       "はい。",
		RawFinal:    "はい。",
		Source:      "RenCrow_LLM llm.final",
		Timings:     Timings{StartedAt: time.Now()},
	})
	if err != nil {
		t.Fatalf("Publish failed: %v", err)
	}
	for _, ev := range emitter.events {
		if ev.Type != "routing.decision" {
			continue
		}
		if !strings.Contains(ev.Content, "surface=voice_chat") || !strings.Contains(ev.Content, "target_agent=mio") || !strings.Contains(ev.Content, "provider_alias=Chat") {
			t.Fatalf("routing decision should preserve voice_chat surface and target/provider: %#v", ev)
		}
		if !strings.Contains(ev.Content, "evidence=voice_direct") {
			t.Fatalf("routing decision should preserve voice_direct transport evidence: %#v", ev)
		}
		return
	}
	t.Fatalf("missing routing.decision event: %#v", emitter.events)
}

func TestPublisherDoesNotPublishRawJSONAsChatContent(t *testing.T) {
	emitter := &recordingEmitter{}
	publisher := Publisher{
		Events:   emitter,
		Input:    newPublisherInput(t, "viewer", "viewer", "default", "れん"),
		NewJobID: func() string { return "job-1" },
	}
	_, err := publisher.Publish(Result{
		Mode:        ModeLLM,
		UtteranceID: "utt-1",
		SessionID:   "viewer",
		Channel:     "viewer",
		ChatID:      "default",
		UserText:    "れん",
		Reply:       "応答",
		RawFinal:    `{"user_text":"れん","reply":"応答"}`,
		Source:      "RenCrow_LLM llm.final",
	})
	if err != nil {
		t.Fatalf("Publish failed: %v", err)
	}
	for _, ev := range emitter.events {
		if ev.Content == `{"user_text":"れん","reply":"応答"}` {
			t.Fatalf("raw JSON leaked to chat event: %#v", ev)
		}
	}
}
