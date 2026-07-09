package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

type recordingVoiceDirectHandler struct {
	mu         sync.Mutex
	finalCalls []orchestrator.ProcessVoiceDirectRequest
	tokenCalls int
}

func (h *recordingVoiceDirectHandler) ProcessVoiceDirect(_ context.Context, req orchestrator.ProcessVoiceDirectRequest) (orchestrator.ProcessMessageResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.finalCalls = append(h.finalCalls, req)
	return orchestrator.ProcessMessageResponse{
		Response: req.FinalText,
		Route:    routing.RouteCHAT,
		JobID:    task.NewJobID().String(),
	}, nil
}

func (h *recordingVoiceDirectHandler) NotifyVoiceDirectFirstToken(context.Context, orchestrator.ProcessVoiceDirectRequest, task.JobID, time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.tokenCalls++
}

func (h *recordingVoiceDirectHandler) snapshot() ([]orchestrator.ProcessVoiceDirectRequest, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	finals := append([]orchestrator.ProcessVoiceDirectRequest(nil), h.finalCalls...)
	return finals, h.tokenCalls
}

func TestVoiceChatBridgeTracker_FinalizesVoiceDirectOnLLMFinal(t *testing.T) {
	handler := &recordingVoiceDirectHandler{}
	tracker := newVoiceChatBridgeTracker(handler, nil)

	tracker.observeClientText([]byte(`{"type":"session.start","utterance_id":"utt-1","channel":"viewer","chat_id":"viewer-user","sample_rate":16000,"channels":1,"format":"pcm16le","model":"Chat"}`))
	tracker.observeClientText([]byte(`{"type":"session.commit","utterance_id":"utt-1"}`))
	tracker.observeGatewayText([]byte(`{"type":"llm.delta","utterance_id":"utt-1","seq":1,"text":"お"}`))
	tracker.observeGatewayText([]byte(`{"type":"llm.final","utterance_id":"utt-1","text":"おはよう"}`))

	finals, tokenCalls := handler.snapshot()
	if len(finals) != 1 {
		t.Fatalf("expected one ProcessVoiceDirect call, got %d", len(finals))
	}
	if finals[0].UtteranceID != "utt-1" || finals[0].FinalText != "おはよう" {
		t.Fatalf("unexpected final call: %+v", finals[0])
	}
	if finals[0].Channel != "viewer" {
		t.Fatalf("expected viewer channel, got %q", finals[0].Channel)
	}
	if tokenCalls != 1 {
		t.Fatalf("expected one first-token notification, got %d", tokenCalls)
	}
}

func TestVoiceChatBridgeTracker_UsesStructuredFinalUserTextHint(t *testing.T) {
	handler := &recordingVoiceDirectHandler{}
	tracker := newVoiceChatBridgeTracker(handler, nil)

	tracker.observeClientText([]byte(`{"type":"session.start","utterance_id":"utt-1","viewer_session_id":"viewer","channel":"viewer","chat_id":"default"}`))
	tracker.observeGatewayText([]byte(`{"type":"llm.final","utterance_id":"utt-1","text":"はい、います。","user_text":"Mioさんいますか"}`))

	finals, _ := handler.snapshot()
	if len(finals) != 1 {
		t.Fatalf("expected one ProcessVoiceDirect call, got %d", len(finals))
	}
	if finals[0].UserText != "Mioさんいますか" || finals[0].FinalText != "はい、います。" {
		t.Fatalf("unexpected final call: %+v", finals[0])
	}
}

func TestVoiceChatBridgeTracker_DeltaIdleDoesNotFinalizeVoiceDirect(t *testing.T) {
	handler := &recordingVoiceDirectHandler{}
	tracker := newVoiceChatBridgeTracker(handler, nil)
	tracker.deltaIdleFinalizeAfter = 10 * time.Millisecond

	tracker.observeClientText([]byte(`{"type":"session.start","utterance_id":"utt-1","channel":"viewer","chat_id":"viewer-user","sample_rate":16000,"channels":1,"format":"pcm16le"}`))
	tracker.observeClientText([]byte(`{"type":"session.commit","utterance_id":"utt-1"}`))
	tracker.observeGatewayText([]byte(`{"type":"llm.delta","utterance_id":"utt-1","seq":1,"text":"お"}`))
	tracker.observeGatewayText([]byte(`{"type":"llm.delta","utterance_id":"utt-1","seq":2,"text":"はよう"}`))

	time.Sleep(30 * time.Millisecond)

	finals, tokenCalls := handler.snapshot()
	if len(finals) != 0 {
		t.Fatalf("delta idle must not finalize before llm.final: %+v", finals)
	}
	if tokenCalls != 1 {
		t.Fatalf("expected one first-token notification, got %d", tokenCalls)
	}
}

func TestVoiceChatBridgeTracker_DeltaIdleDoesNotDoubleFinalizeWhenFinalArrives(t *testing.T) {
	handler := &recordingVoiceDirectHandler{}
	tracker := newVoiceChatBridgeTracker(handler, nil)
	tracker.deltaIdleFinalizeAfter = 10 * time.Millisecond

	tracker.observeClientText([]byte(`{"type":"session.start","utterance_id":"utt-1","channel":"viewer"}`))
	tracker.observeGatewayText([]byte(`{"type":"llm.delta","utterance_id":"utt-1","seq":1,"text":"お"}`))
	tracker.observeGatewayText([]byte(`{"type":"llm.final","utterance_id":"utt-1","text":"おはよう"}`))
	time.Sleep(30 * time.Millisecond)

	finals, _ := handler.snapshot()
	if len(finals) != 1 {
		t.Fatalf("expected one ProcessVoiceDirect call, got %d", len(finals))
	}
	if finals[0].FinalText != "おはよう" {
		t.Fatalf("expected llm.final text to win, got %+v", finals[0])
	}
}

func TestVoiceChatBridgeTracker_CancelClearsState(t *testing.T) {
	handler := &recordingVoiceDirectHandler{}
	tracker := newVoiceChatBridgeTracker(handler, nil)

	tracker.observeClientText([]byte(`{"type":"session.start","utterance_id":"utt-1","channel":"viewer"}`))
	tracker.observeClientText([]byte(`{"type":"session.cancel","utterance_id":"utt-1"}`))
	tracker.observeGatewayText([]byte(`{"type":"llm.final","utterance_id":"utt-1","text":"ignored"}`))

	finals, _ := handler.snapshot()
	if len(finals) != 0 {
		t.Fatalf("cancelled utterance must not finalize: %+v", finals)
	}
}

func TestVoiceChatBridgeTracker_InterruptsIdleChatDuringVoiceSession(t *testing.T) {
	handler := &recordingVoiceDirectHandler{}
	idle := &recordingVoiceChatIdleNotifier{}
	tracker := newVoiceChatBridgeTracker(handler, idle)

	tracker.observeClientText([]byte(`{"type":"session.start","utterance_id":"utt-1","channel":"viewer"}`))
	if idle.activities != 1 {
		t.Fatalf("expected voice session start to notify idle activity, got %d", idle.activities)
	}
	if got := idle.chatBusy; len(got) != 1 || got[0] != true {
		t.Fatalf("expected chat busy to start on voice input, got %#v", got)
	}

	tracker.observeGatewayText([]byte(`{"type":"llm.final","utterance_id":"utt-1","text":"おはよう"}`))
	if got := idle.chatBusy; len(got) != 2 || got[1] != false {
		t.Fatalf("expected chat busy to end after voice final, got %#v", got)
	}
}

func TestVoiceChatBridgeTracker_EndsIdleChatInterruptOnCancel(t *testing.T) {
	idle := &recordingVoiceChatIdleNotifier{}
	tracker := newVoiceChatBridgeTracker(nil, idle)

	tracker.observeClientText([]byte(`{"type":"session.start","utterance_id":"utt-1","channel":"viewer"}`))
	tracker.observeClientText([]byte(`{"type":"session.cancel","utterance_id":"utt-1"}`))

	if got := idle.chatBusy; len(got) != 2 || got[0] != true || got[1] != false {
		t.Fatalf("expected chat busy start/end on voice cancel, got %#v", got)
	}
}

func TestVoiceChatBridgeTracker_DropsMetaNoAudioFinal(t *testing.T) {
	handler := &recordingVoiceDirectHandler{}
	tracker := newVoiceChatBridgeTracker(handler, nil)

	tracker.observeClientText([]byte(`{"type":"session.start","utterance_id":"utt-1","channel":"viewer"}`))
	tracker.observeClientText([]byte(`{"type":"session.commit","utterance_id":"utt-1"}`))
	tracker.observeGatewayText([]byte(`{"type":"llm.final","utterance_id":"utt-1","text":"申し訳ございませんが、音声が提供されていないため、内容を確認することができません。音声ファイルをアップロードしてください。"}`))

	finals, _ := handler.snapshot()
	if len(finals) != 0 {
		t.Fatalf("meta no-audio final must not be emitted as chat response: %+v", finals)
	}
}

func TestVoiceDirectMetaNoAudioFinalClassifier(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{text: "音声内容を入力してください。", want: true},
		{text: "音声内容を提示してください。入力をお待ちしております。", want: true},
		{text: "音声ファイルをアップロードしていただければ、内容を要約いたします。", want: true},
		{text: "こんにちは、今日はどうしましたか？", want: false},
	}
	for _, tc := range cases {
		if got := isVoiceDirectMetaNoAudioFinal(tc.text); got != tc.want {
			t.Fatalf("isVoiceDirectMetaNoAudioFinal(%q)=%v want %v", tc.text, got, tc.want)
		}
	}
}

func TestVoiceChatBridgeTracker_SessionStartUsesViewerDefaults(t *testing.T) {
	tracker := newVoiceChatBridgeTracker(nil, nil)
	tracker.observeClientText([]byte(`{"type":"session.start","utterance_id":"utt-9","viewer_session_id":"viewer-session","channel":"viewer"}`))

	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if tracker.active.UtteranceID != "utt-9" {
		t.Fatalf("unexpected utterance id: %+v", tracker.active)
	}
	if tracker.active.SessionID != "viewer-session" {
		t.Fatalf("unexpected session id: %+v", tracker.active)
	}
	if tracker.active.Channel != "viewer" {
		t.Fatalf("unexpected channel: %+v", tracker.active)
	}
}
