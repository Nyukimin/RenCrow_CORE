package main

import (
	"context"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/idlechat"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

type idleChatPrefetchMockBridge struct {
	startReqs    []orchestrator.TTSSessionStart
	pushTexts    []string
	displayTexts []string
	endIDs       []string
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
	setIdleChatViewerClientCount(func() int { return 0 })
	t.Cleanup(func() {
		setIdleChatViewerClientCount(nil)
		clearAllIdleChatTTSPending()
		resetTTSPublicSessionStateForTest()
	})

	bridge := &idleChatPrefetchMockBridge{}
	manager := newIdleChatTTSPrefetchManager(bridge)
	if manager == nil {
		t.Fatal("expected prefetch manager")
	}

	manager.Push(idlechat.TTSPrefetchEvent{
		SessionID: "idle-prefetch",
		MessageID: "idle-prefetch:msg:0002",
		From:      "shiro",
		To:        "mio",
		TurnIndex: 2,
		Token:     "古書店の棚の奥で、雨に濡れた封筒が一通だけ見つかる。",
	})
	manager.Push(idlechat.TTSPrefetchEvent{
		SessionID: "idle-prefetch",
		MessageID: "idle-prefetch:msg:0002",
		From:      "shiro",
		To:        "mio",
		TurnIndex: 2,
		Token:     "誰かの秘密がまだ乾いていない感じがする。",
	})

	waitCh, ok := manager.Close(idlechat.TimelineEvent{
		Type:       "idlechat.message",
		From:       "shiro",
		To:         "mio",
		Content:    "古書店の棚の奥で、雨に濡れた封筒が一通だけ見つかる。誰かの秘密がまだ乾いていない感じがする。",
		RawContent: "古書店の棚の奥で、雨に濡れた封筒が一通だけ見つかる。誰かの秘密がまだ乾いていない感じがする。",
		SessionID:  "idle-prefetch",
		MessageID:  "idle-prefetch:msg:0002",
		TurnIndex:  2,
	})

	if !ok {
		t.Fatal("expected prefetch close to succeed")
	}
	if waitCh != nil {
		t.Fatal("no viewer clients should not produce a playback wait channel")
	}
	if len(bridge.startReqs) != 1 {
		t.Fatalf("start requests = %d, want 1", len(bridge.startReqs))
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
