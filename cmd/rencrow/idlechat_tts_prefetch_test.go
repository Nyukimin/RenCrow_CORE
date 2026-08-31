package main

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/idlechat"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

type idleChatPrefetchMockBridge struct {
	startReqs    []orchestrator.TTSSessionStart
	pushTexts    []string
	displayTexts []string
	endIDs       []string
}

type blockingIdleChatPrefetchBridge struct {
	idleChatPrefetchMockBridge
	started     chan struct{}
	canceled    chan struct{}
	release     chan struct{}
	startOnce   sync.Once
	cancelOnce  sync.Once
	releaseOnce sync.Once
}

func (b *blockingIdleChatPrefetchBridge) releaseSynthesis() {
	b.releaseOnce.Do(func() { close(b.release) })
}

func (b *blockingIdleChatPrefetchBridge) PushText(ctx context.Context, sessionID, text string, emotion *moduletts.EmotionState) error {
	b.startOnce.Do(func() { close(b.started) })
	select {
	case <-ctx.Done():
		b.cancelOnce.Do(func() { close(b.canceled) })
		return ctx.Err()
	case <-b.release:
		return nil
	}
}

func (b *blockingIdleChatPrefetchBridge) PushTextWithDisplay(ctx context.Context, sessionID, text, displayText string, emotion *moduletts.EmotionState) error {
	return b.PushText(ctx, sessionID, text, emotion)
}

func (m *idleChatPrefetchMockBridge) StartSession(_ context.Context, req orchestrator.TTSSessionStart) error {
	m.startReqs = append(m.startReqs, req)
	return nil
}

func (m *idleChatPrefetchMockBridge) PushText(_ context.Context, _ string, text string, _ *moduletts.EmotionState) error {
	m.pushTexts = append(m.pushTexts, text)
	return nil
}

func (m *idleChatPrefetchMockBridge) PushTextWithDisplay(_ context.Context, _ string, text string, displayText string, _ *moduletts.EmotionState) error {
	m.pushTexts = append(m.pushTexts, text)
	m.displayTexts = append(m.displayTexts, displayText)
	return nil
}

func (m *idleChatPrefetchMockBridge) EndSession(_ context.Context, sessionID string) error {
	m.endIDs = append(m.endIDs, sessionID)
	return nil
}

func (m *idleChatPrefetchMockBridge) EmitIdleChatTTSError(_ context.Context, _, _, _, _, _ string, _ error) {
}

func TestIdleChatTTSPrefetchManagerStreamsChunksInOneSession(t *testing.T) {
	clearAllIdleChatTTSPending()
	resetTTSPublicSessionStateForTest()
	resetActiveViewerControlForTest()
	activeViewerControl.Claim("audio", "prefetch-stream-viewer")
	setIdleChatViewerClientCount(func() int { return 1 })
	t.Cleanup(func() {
		setIdleChatViewerClientCount(nil)
		resetActiveViewerControlForTest()
		clearAllIdleChatTTSPending()
		resetTTSPublicSessionStateForTest()
	})

	bridge := &idleChatPrefetchMockBridge{}
	manager := newIdleChatTTSPrefetchManager(bridge)
	traceID := modulecore.NewTraceID()
	if manager == nil {
		t.Fatal("expected prefetch manager")
	}

	manager.Push(idlechat.TTSPrefetchEvent{
		SessionID: "idle-prefetch",
		MessageID: "idle-prefetch:msg:0002",
		From:      "shiro",
		To:        "mio",
		TurnIndex: 2,
		TraceID:   traceID,
		Token:     "古書店の棚の奥で、雨に濡れた封筒が一通だけ見つかる。",
	})
	manager.Push(idlechat.TTSPrefetchEvent{
		SessionID: "idle-prefetch",
		MessageID: "idle-prefetch:msg:0002",
		From:      "shiro",
		To:        "mio",
		TurnIndex: 2,
		TraceID:   traceID,
		Token:     "誰かの秘密がまだ乾いていない感じがする。",
	})

	lifecycle, ok := manager.Close(idlechat.TimelineEvent{
		Type:       "idlechat.message",
		From:       "shiro",
		To:         "mio",
		Content:    "古書店の棚の奥で、雨に濡れた封筒が一通だけ見つかる。誰かの秘密がまだ乾いていない感じがする。",
		RawContent: "古書店の棚の奥で、雨に濡れた封筒が一通だけ見つかる。誰かの秘密がまだ乾いていない感じがする。",
		SessionID:  "idle-prefetch",
		MessageID:  "idle-prefetch:msg:0002",
		TurnIndex:  2,
		TraceID:    traceID,
	})

	if !ok {
		t.Fatal("expected prefetch close to succeed")
	}
	if lifecycle.Ready == nil || lifecycle.Done == nil {
		t.Fatal("prefetch close should expose synthesis lifecycle channels")
	}
	select {
	case <-lifecycle.Done:
	case <-time.After(time.Second):
		t.Fatal("prefetch synthesis did not complete")
	}
	if len(bridge.startReqs) != 1 {
		t.Fatalf("start requests = %d, want 1", len(bridge.startReqs))
	}
	if bridge.startReqs[0].TraceID != string(traceID) {
		t.Fatalf("prefetch TTS trace_id = %q, want %q", bridge.startReqs[0].TraceID, traceID)
	}
	if len(bridge.pushTexts) == 0 {
		t.Fatalf("push texts = %d, want at least 1", len(bridge.pushTexts))
	}
	if len(bridge.endIDs) != 1 {
		t.Fatalf("end requests = %d, want 1", len(bridge.endIDs))
	}
	if !strings.HasPrefix(bridge.startReqs[0].SessionID, "idle-prefetch-tts-") {
		t.Fatalf("unexpected session id: %q", bridge.startReqs[0].SessionID)
	}
}

func TestIdleChatTTSPrefetchCloseDoesNotWaitForSynthesis(t *testing.T) {
	clearAllIdleChatTTSPending()
	resetTTSPublicSessionStateForTest()
	resetActiveViewerControlForTest()
	activeViewerControl.Claim("audio", "prefetch-blocking-viewer")
	setIdleChatViewerClientCount(func() int { return 1 })
	bridge := &blockingIdleChatPrefetchBridge{
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
		release:  make(chan struct{}),
	}
	t.Cleanup(func() {
		bridge.releaseSynthesis()
		setIdleChatViewerClientCount(nil)
		resetActiveViewerControlForTest()
		clearAllIdleChatTTSPending()
		resetTTSPublicSessionStateForTest()
	})

	manager := newIdleChatTTSPrefetchManager(bridge)
	traceID := modulecore.NewTraceID()
	manager.Push(idlechat.TTSPrefetchEvent{
		SessionID: "idle-prefetch-blocking",
		MessageID: "idle-prefetch-blocking:msg:0001",
		From:      "mio",
		To:        "user",
		TurnIndex: 1,
		TraceID:   traceID,
		Token:     "合成完了まで長くかかる発話です。",
	})
	started := time.Now()
	lifecycle, ok := manager.Close(idlechat.TimelineEvent{
		Type:       "idlechat.message",
		From:       "mio",
		To:         "user",
		Content:    "合成完了まで長くかかる発話です。",
		RawContent: "合成完了まで長くかかる発話です。",
		SessionID:  "idle-prefetch-blocking",
		MessageID:  "idle-prefetch-blocking:msg:0001",
		TurnIndex:  1,
		TraceID:    traceID,
	})
	if !ok || lifecycle.Ready == nil || lifecycle.Done == nil {
		t.Fatal("prefetch Close should return lifecycle")
	}
	select {
	case <-bridge.started:
	case <-time.After(time.Second):
		bridge.releaseSynthesis()
		t.Fatal("prefetch synthesis did not start")
	}
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		bridge.releaseSynthesis()
		t.Fatal("prefetch Close blocked on synthesis completion")
	}
	select {
	case <-lifecycle.Done:
		t.Fatal("Done closed while synthesis Push was still blocked")
	default:
	}
	bridge.releaseSynthesis()
	select {
	case <-lifecycle.Done:
	case <-time.After(time.Second):
		t.Fatal("prefetch lifecycle did not complete after synthesis release")
	}
}

func TestIdleChatTTSPrefetchDoesNotReuseStreamAcrossTraceIDs(t *testing.T) {
	clearAllIdleChatTTSPending()
	resetTTSPublicSessionStateForTest()
	resetActiveViewerControlForTest()
	activeViewerControl.Claim("audio", "prefetch-trace-viewer")
	setIdleChatViewerClientCount(func() int { return 1 })
	var manager *idleChatTTSPrefetchManager
	var stream *idleChatTTSPrefetchStream
	firstTrace := modulecore.NewTraceID()
	t.Cleanup(func() {
		if manager != nil {
			if stream != nil {
				lifecycle, ok := stream.close(idlechat.TimelineEvent{
					Type:      "idlechat.message",
					SessionID: "idle-prefetch-replay",
					MessageID: "idle-prefetch-replay:msg:0001",
					TraceID:   firstTrace,
				})
				if ok {
					select {
					case <-lifecycle.Done:
					case <-time.After(time.Second):
						t.Error("original prefetch stream did not finish before test cleanup")
					}
				}
				deadline := time.Now().Add(time.Second)
				key := streamKey("idle-prefetch-replay", "idle-prefetch-replay:msg:0001", string(firstTrace))
				for {
					manager.mu.Lock()
					_, active := manager.streams[key]
					manager.mu.Unlock()
					if !active {
						break
					}
					if !time.Now().Before(deadline) {
						t.Error("original prefetch stream remained registered during cleanup")
						break
					}
					runtime.Gosched()
				}
			}
			manager.CancelAll()
		}
		setIdleChatViewerClientCount(nil)
		resetActiveViewerControlForTest()
		clearAllIdleChatTTSPending()
		resetTTSPublicSessionStateForTest()
	})

	bridge := &idleChatPrefetchMockBridge{}
	manager = newIdleChatTTSPrefetchManager(bridge)
	secondTrace := modulecore.NewTraceID()
	manager.Push(idlechat.TTSPrefetchEvent{
		SessionID: "idle-prefetch-replay",
		MessageID: "idle-prefetch-replay:msg:0001",
		From:      "mio",
		To:        "user",
		TurnIndex: 1,
		TraceID:   firstTrace,
		Token:     "最初の再生です。",
	})
	stream = manager.stream("idle-prefetch-replay", "idle-prefetch-replay:msg:0001", idlechat.TTSPrefetchEvent{
		SessionID: "idle-prefetch-replay",
		MessageID: "idle-prefetch-replay:msg:0001",
		TraceID:   firstTrace,
	})
	if _, ok := manager.Close(idlechat.TimelineEvent{
		Type:      "idlechat.message",
		SessionID: "idle-prefetch-replay",
		MessageID: "idle-prefetch-replay:msg:0001",
		TraceID:   secondTrace,
		Content:   "二回目の別Traceです。",
	}); ok {
		t.Fatal("Close must reject a same-session/message stream owned by another trace")
	}
	if manager.HasActive("idle-prefetch-replay", "idle-prefetch-replay:msg:0001", firstTrace) == false {
		// The first stream must remain owned by its original trace after the
		// mismatched close attempt.
		t.Fatal("mismatched Close must not consume the original stream")
	}
}

func TestIdleChatTTSPrefetchTimeoutCancelsOnlyMatchingTrace(t *testing.T) {
	bridge := &idleChatPrefetchMockBridge{}
	manager := newIdleChatTTSPrefetchManager(bridge)
	firstTrace := modulecore.NewTraceID()
	secondTrace := modulecore.NewTraceID()
	first := manager.stream("idle-prefetch-timeout-replay", "idle-prefetch-timeout-replay:msg:0001", idlechat.TTSPrefetchEvent{
		SessionID: "idle-prefetch-timeout-replay",
		MessageID: "idle-prefetch-timeout-replay:msg:0001",
		TraceID:   firstTrace,
	})
	second := manager.stream("idle-prefetch-timeout-replay", "idle-prefetch-timeout-replay:msg:0001", idlechat.TTSPrefetchEvent{
		SessionID: "idle-prefetch-timeout-replay",
		MessageID: "idle-prefetch-timeout-replay:msg:0001",
		TraceID:   secondTrace,
	})
	t.Cleanup(manager.CancelAll)

	manager.CancelTimeout(idlechat.TTSTimeoutEvent{
		Kind:      "timeout",
		SessionID: "idle-prefetch-timeout-replay",
		MessageID: "idle-prefetch-timeout-replay:msg:0001",
		TraceID:   firstTrace,
	})
	select {
	case <-first.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("timeout did not cancel the matching prefetch trace")
	}
	select {
	case <-second.ctx.Done():
		t.Fatal("timeout for trace A canceled the active trace B prefetch")
	default:
	}
}

func TestIdleChatTTSPrefetchSessionTimeoutCancelsOnlyMatchingTrace(t *testing.T) {
	bridge := &idleChatPrefetchMockBridge{}
	manager := newIdleChatTTSPrefetchManager(bridge)
	firstTrace := modulecore.NewTraceID()
	secondTrace := modulecore.NewTraceID()
	first := manager.stream("idle-prefetch-session-timeout-replay", "idle-prefetch-session-timeout-replay:msg:0001", idlechat.TTSPrefetchEvent{
		SessionID: "idle-prefetch-session-timeout-replay",
		MessageID: "idle-prefetch-session-timeout-replay:msg:0001",
		TraceID:   firstTrace,
	})
	second := manager.stream("idle-prefetch-session-timeout-replay", "idle-prefetch-session-timeout-replay:msg:0002", idlechat.TTSPrefetchEvent{
		SessionID: "idle-prefetch-session-timeout-replay",
		MessageID: "idle-prefetch-session-timeout-replay:msg:0002",
		TraceID:   secondTrace,
	})
	t.Cleanup(manager.CancelAll)

	manager.CancelTimeout(idlechat.TTSTimeoutEvent{
		Kind:      "session_audio_timeout",
		SessionID: "idle-prefetch-session-timeout-replay",
		TraceID:   firstTrace,
	})
	select {
	case <-first.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("session timeout did not cancel the matching prefetch trace")
	}
	select {
	case <-second.ctx.Done():
		t.Fatal("session timeout for trace A canceled the active trace B prefetch")
	default:
	}
}
