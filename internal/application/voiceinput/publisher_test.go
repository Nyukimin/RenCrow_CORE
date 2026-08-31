package voiceinput

import (
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type recordingEmitter struct {
	events []recordedEvent
}

type recordedEvent struct {
	Type    string
	From    string
	To      string
	Content string
	Route   string
}

func (e *recordingEmitter) Emit(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) {
	e.events = append(e.events, recordedEvent{Type: eventType, From: from, To: to, Content: content, Route: route})
}

type recordingTurnLogger struct {
	user      string
	assistant string
}

type recordingPublisherCorrelatedTurnLogger struct {
	userCalls        int
	assistantCalls   int
	userTraceID      string
	assistantTraceID string
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
	l.userTraceID = traceID
}

func (l *recordingPublisherCorrelatedTurnLogger) WriteAssistantWithIdentity(sessionID, channel, route, jobID, messageID, traceID, content string) {
	l.assistantCalls++
	l.assistantTraceID = traceID
}

func TestPublisherRejectsMissingTraceIDBeforeEmittingOrLogging(t *testing.T) {
	emitter := &recordingEmitter{}
	logger := &recordingPublisherCorrelatedTurnLogger{}
	jobCalls := 0
	publisher := Publisher{
		Events:     emitter,
		TurnLogger: logger,
		NewJobID: func() string {
			jobCalls++
			return task.NewJobID().String()
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

func TestPublisherRejectsMalformedTraceIDBeforeEmittingOrLogging(t *testing.T) {
	emitter := &recordingEmitter{}
	logger := &recordingPublisherCorrelatedTurnLogger{}
	jobCalls := 0
	publisher := Publisher{
		Events:     emitter,
		TurnLogger: logger,
		TraceID:    modulecore.TraceID("not-a-canonical-trace"),
		NewJobID: func() string {
			jobCalls++
			return task.NewJobID().String()
		},
	}

	_, err := publisher.Publish(Result{
		Mode:        ModeLLM,
		UtteranceID: "utt-malformed-trace",
		SessionID:   "viewer",
		Channel:     "viewer",
		ChatID:      "default",
		UserText:    "入力",
		Reply:       "応答",
		RawFinal:    "応答",
		Source:      "RenCrow_LLM llm.final",
	})
	if err == nil {
		t.Fatal("expected malformed publisher trace_id to fail closed")
	}
	if len(emitter.events) != 0 {
		t.Fatalf("malformed trace_id must not emit events: %#v", emitter.events)
	}
	if logger.userCalls != 0 || logger.assistantCalls != 0 {
		t.Fatalf("malformed trace_id must not write session logs: %+v", logger)
	}
	if jobCalls != 0 {
		t.Fatalf("malformed trace_id must fail before allocating a job: calls=%d", jobCalls)
	}
}

func TestPublisherDoesNotReuseJobIDAsTraceID(t *testing.T) {
	jobID := task.NewJobID().String()
	traceID := modulecore.NewTraceID()
	published, err := (Publisher{
		TraceID:  traceID,
		NewJobID: func() string { return jobID },
	}).Publish(Result{
		Mode:        ModeLLM,
		UtteranceID: "utt-trace-reuse",
		SessionID:   "viewer",
		Channel:     "viewer",
		ChatID:      "default",
		UserText:    "入力",
		Reply:       "応答",
		RawFinal:    "応答",
		Source:      "RenCrow_LLM llm.final",
	})
	if err != nil {
		t.Fatalf("Publish failed: %v", err)
	}
	if published.TraceID != string(traceID) || modulecore.TraceID(published.TraceID).Validate() != nil || published.TraceID == published.JobID {
		t.Fatalf("publisher must return an independent canonical trace_id: %+v", published)
	}
}

func TestPublisherPassesExplicitTraceIDToCorrelatedSessionLogs(t *testing.T) {
	traceID := modulecore.NewTraceID()
	logger := &recordingPublisherCorrelatedTurnLogger{}
	published, err := (Publisher{
		TurnLogger: logger,
		TraceID:    traceID,
		NewJobID:   func() string { return task.NewJobID().String() },
	}).Publish(Result{
		Mode:        ModeLLM,
		UtteranceID: "utt-correlated-trace",
		SessionID:   "viewer",
		Channel:     "viewer",
		ChatID:      "default",
		UserText:    "入力",
		Reply:       "応答",
		RawFinal:    "応答",
		Source:      "RenCrow_LLM llm.final",
	})
	if err != nil {
		t.Fatalf("Publish failed: %v", err)
	}
	if published.TraceID != string(traceID) {
		t.Fatalf("publish result trace_id=%q, want explicit %q", published.TraceID, traceID)
	}
	if logger.userTraceID != string(traceID) || logger.assistantTraceID != string(traceID) {
		t.Fatalf("correlated session log traces user=%q assistant=%q, want %q", logger.userTraceID, logger.assistantTraceID, traceID)
	}
}

func TestPublisherRejectsMissingUserTextBeforeEmittingOrLogging(t *testing.T) {
	emitter := &recordingEmitter{}
	logger := &recordingPublisherCorrelatedTurnLogger{}
	jobCalls := 0
	publisher := Publisher{
		Events:     emitter,
		TurnLogger: logger,
		TraceID:    modulecore.NewTraceID(),
		NewJobID: func() string {
			jobCalls++
			return task.NewJobID().String()
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
		TraceID:    modulecore.NewTraceID(),
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
		TraceID:  modulecore.NewTraceID(),
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
		TraceID:  modulecore.NewTraceID(),
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
