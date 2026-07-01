package voiceinput

import (
	"strings"
	"testing"
	"time"
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

func (l *recordingTurnLogger) WriteUser(sessionID, channel, content string) {
	l.user = content
}

func (l *recordingTurnLogger) WriteAssistant(sessionID, channel, route, jobID, content string) {
	l.assistant = content
}

func TestPublisherPublishesUserTextAndReplyOnly(t *testing.T) {
	emitter := &recordingEmitter{}
	logger := &recordingTurnLogger{}
	publisher := Publisher{
		Events:     emitter,
		TurnLogger: logger,
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
		NewJobID: func() string { return "job-1" },
	}
	_, err := publisher.Publish(Result{
		Mode:        ModeLLM,
		UtteranceID: "utt-1",
		SessionID:   "viewer",
		Channel:     "viewer",
		ChatID:      "default",
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
