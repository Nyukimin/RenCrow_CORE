package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/idlechat"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

type idleChatMockTTSBridge struct {
	startReqs    []orchestrator.TTSSessionStart
	pushTexts    []string
	displayTexts []string
	pushEmo      []*moduletts.EmotionState
	endIDs       []string
	notifyOnEnd  bool
	pushErr      error
	errorEvents  []string
}

func (m *idleChatMockTTSBridge) StartSession(_ context.Context, req orchestrator.TTSSessionStart) error {
	m.startReqs = append(m.startReqs, req)
	return nil
}

func (m *idleChatMockTTSBridge) PushText(_ context.Context, sessionID string, text string, emotion *moduletts.EmotionState) error {
	_ = sessionID
	m.pushTexts = append(m.pushTexts, text)
	m.pushEmo = append(m.pushEmo, emotion)
	return m.pushErr
}

func (m *idleChatMockTTSBridge) PushTextWithDisplay(_ context.Context, sessionID string, text string, displayText string, emotion *moduletts.EmotionState) error {
	_ = sessionID
	m.pushTexts = append(m.pushTexts, text)
	m.displayTexts = append(m.displayTexts, displayText)
	m.pushEmo = append(m.pushEmo, emotion)
	return m.pushErr
}

func (m *idleChatMockTTSBridge) EndSession(_ context.Context, sessionID string) error {
	m.endIDs = append(m.endIDs, sessionID)
	if m.notifyOnEnd {
		clearIdleChatTTSPending(sessionID)
	}
	return nil
}

func (m *idleChatMockTTSBridge) EmitIdleChatTTSError(_ context.Context, sessionID, characterID, speechText, displayText, errorCode string, cause error) {
	m.errorEvents = append(m.errorEvents, strings.Join([]string{sessionID, characterID, speechText, displayText, errorCode, cause.Error()}, "|"))
}

func TestEmitIdleChatTTSSendsMessage(t *testing.T) {
	bridge := &idleChatMockTTSBridge{}

	_, _ = emitIdleChatTTS(context.Background(), bridge, idlechat.TimelineEvent{
		Type:      "idlechat.message",
		From:      "shiro",
		To:        "mio",
		Content:   "はい、承知いたしました。おはようございます！",
		SessionID: "idle-1",
	})

	if len(bridge.startReqs) != 1 {
		t.Fatalf("expected 1 start request, got %d", len(bridge.startReqs))
	}
	if bridge.startReqs[0].VoiceID != "male_01" {
		t.Fatalf("expected male_01 voice, got %q", bridge.startReqs[0].VoiceID)
	}
	if len(bridge.pushTexts) != 1 {
		t.Fatalf("expected 1 push text, got %d", len(bridge.pushTexts))
	}
	if got := bridge.pushTexts[0]; got != "おはようございます！" {
		t.Fatalf("unexpected filtered text: %q", got)
	}
	if got := bridge.displayTexts[0]; got != bridge.pushTexts[0] {
		t.Fatalf("normal idlechat chunk display_text must match speech_text: display=%q speech=%q", got, bridge.pushTexts[0])
	}
	if len(bridge.pushEmo) != 1 || bridge.pushEmo[0] == nil {
		t.Fatal("expected emotion payload")
	}
	if len(bridge.endIDs) != 1 {
		t.Fatalf("expected 1 end request, got %d", len(bridge.endIDs))
	}
}

func TestEmitIdleChatTTSSkipsPlaybackWaitWhenNoViewerClients(t *testing.T) {
	clearAllIdleChatTTSPending()
	resetTTSPublicSessionStateForTest()
	setIdleChatViewerClientCount(func() int { return 0 })
	t.Cleanup(func() {
		setIdleChatViewerClientCount(nil)
		clearAllIdleChatTTSPending()
		resetTTSPublicSessionStateForTest()
	})

	bridge := &idleChatMockTTSBridge{}
	waitCh, ok := emitIdleChatTTS(context.Background(), bridge, idlechat.TimelineEvent{
		Type:      "idlechat.message",
		From:      "mio",
		To:        "shiro",
		Content:   "Viewerなしでも音声生成待ちは積みません。",
		SessionID: "idle-no-viewer",
		MessageID: "idle-no-viewer:msg:0001",
		TurnIndex: 1,
	})

	if !ok {
		t.Fatal("expected TTS route to run")
	}
	if waitCh != nil {
		t.Fatal("no Viewer clients should not create a playback wait channel")
	}
	if len(bridge.startReqs) != 1 || len(bridge.pushTexts) != 1 || len(bridge.endIDs) != 1 {
		t.Fatalf("expected TTS bridge to receive start/push/end, got start=%d push=%d end=%d", len(bridge.startReqs), len(bridge.pushTexts), len(bridge.endIDs))
	}
	if got := snapshotIdleChatTTSPending(); got.PendingSessionCount != 0 || got.PendingResponseCount != 0 {
		t.Fatalf("pending should stay empty without Viewer clients: %+v", got)
	}
	if got := resolveTTSPublicResponse(bridge.startReqs[0].SessionID); got != "" {
		t.Fatalf("public route should be cleared after no-viewer TTS, got %q", got)
	}
}

func TestIdleChatViewerDisconnectClearsPlaybackWaits(t *testing.T) {
	clearAllIdleChatTTSPending()
	resetTTSPublicSessionStateForTest()
	t.Cleanup(func() {
		clearAllIdleChatTTSPending()
		resetTTSPublicSessionStateForTest()
	})

	waitCh := registerIdleChatTTSPending("idle-disconnect-tts", "idle-disconnect:0001")
	registerTTSPublicSessionWithMessage("idle-disconnect-tts", "idle-disconnect", "idle-disconnect:0001", "idle-disconnect:msg:0001", 1)

	handleIdleChatViewerClientCountChanged(0)

	select {
	case <-waitCh:
	case <-time.After(time.Second):
		t.Fatal("viewer disconnect should close pending TTS wait channel")
	}
	if got := snapshotIdleChatTTSPending(); got.PendingSessionCount != 0 || got.PendingResponseCount != 0 {
		t.Fatalf("pending should be cleared after viewer disconnect: %+v", got)
	}
	if got := resolveTTSPublicResponse("idle-disconnect-tts"); got != "" {
		t.Fatalf("public session route should be cleared, got %q", got)
	}
}

func TestEmitIdleChatTTSCompletesNormallyOnPushFailure(t *testing.T) {
	clearAllIdleChatTTSPending()
	t.Cleanup(clearAllIdleChatTTSPending)
	bridge := &idleChatMockTTSBridge{pushErr: errors.New("irodori unavailable")}

	waitCh, ok := emitIdleChatTTS(context.Background(), bridge, idlechat.TimelineEvent{
		Type:      "idlechat.message",
		From:      "shiro",
		To:        "mio",
		Content:   "音声合成に失敗する発話です。",
		SessionID: "idle-tts-error",
		MessageID: "idle-tts-error:msg:0002",
		TurnIndex: 2,
	})

	if !ok || waitCh == nil {
		t.Fatal("expected failed push to still expose a completed wait channel")
	}
	if len(bridge.errorEvents) != 0 {
		t.Fatalf("TTS provider failures should remain log-only for IdleChat processing, got error events %#v", bridge.errorEvents)
	}
	if len(bridge.endIDs) != 1 {
		t.Fatalf("expected end session after push failure, got %d", len(bridge.endIDs))
	}
	if got := snapshotIdleChatTTSPending(); got.PendingResponseCount != 0 {
		t.Fatalf("pending response count = %d, want 0 after log-only TTS failure", got.PendingResponseCount)
	}
	select {
	case <-waitCh:
	case <-time.After(time.Second):
		t.Fatal("wait channel did not close after log-only TTS failure")
	}
}

func TestEmitIdleChatTTSSendsStorySimpleTTSEvent(t *testing.T) {
	clearAllIdleChatTTSPending()
	t.Cleanup(clearAllIdleChatTTSPending)
	bridge := &idleChatMockTTSBridge{}

	waitCh, ok := emitIdleChatTTS(context.Background(), bridge, idlechat.TimelineEvent{
		Type:      "idlechat.tts",
		From:      "mio",
		To:        "user",
		Content:   "今夜の物語です。",
		SessionID: "story-simple-1",
		MessageID: "story-simple-1:story:0001",
		TurnIndex: 1,
	})

	if !ok || waitCh == nil {
		t.Fatal("expected idlechat.tts event to enter the TTS route")
	}
	if len(bridge.startReqs) != 1 {
		t.Fatalf("expected 1 start request, got %d", len(bridge.startReqs))
	}
	if got := bridge.startReqs[0].ResponseID; got != "story-simple-1:story:0001" {
		t.Fatalf("unexpected response id: %q", got)
	}
	if len(bridge.pushTexts) != 1 || bridge.pushTexts[0] != "今夜の物語です。" {
		t.Fatalf("unexpected pushed texts: %#v", bridge.pushTexts)
	}
	if got := snapshotIdleChatTTSPending(); got.PendingResponseCount != 1 {
		t.Fatalf("pending response count = %d, want 1", got.PendingResponseCount)
	}
	if !notifyIdleChatTTSPlaybackCompleted("story-simple-1:story:0001") {
		t.Fatal("expected response id to be consumable by playback ACK")
	}
}

func TestIdleChatTTSPendingSnapshotCountsOutstandingRoutes(t *testing.T) {
	clearAllIdleChatTTSPending()
	resetTTSPublicSessionStateForTest()
	t.Cleanup(func() {
		clearAllIdleChatTTSPending()
		resetTTSPublicSessionStateForTest()
	})

	const (
		publicSessionID   = "idle-snapshot"
		internalSessionID = "idle-snapshot-tts"
		responseID        = "idle-snapshot:0000"
	)
	registerTTSPublicSessionWithMessage(internalSessionID, publicSessionID, responseID, "idle-snapshot:msg:0001", 1)
	registerIdleChatTTSPending(internalSessionID, responseID)
	registerIdleChatTopicGate(publicSessionID, internalSessionID)

	pending := snapshotIdleChatTTSPending()
	if pending.PendingSessionCount != 1 || pending.PendingResponseCount != 1 || pending.TopicGateCount != 1 || pending.TopicRouteCount != 1 {
		t.Fatalf("unexpected pending snapshot before ack: %+v", pending)
	}
	if len(pending.PendingSessionIDs) != 1 || pending.PendingSessionIDs[0] != internalSessionID {
		t.Fatalf("unexpected pending session IDs before ack: %+v", pending.PendingSessionIDs)
	}
	if len(pending.PendingResponseIDs) != 1 || pending.PendingResponseIDs[0] != responseID {
		t.Fatalf("unexpected pending response IDs before ack: %+v", pending.PendingResponseIDs)
	}
	public := snapshotTTSPublicSessions()
	if public.RouteCount != 1 {
		t.Fatalf("unexpected public session snapshot before ack: %+v", public)
	}

	if !notifyIdleChatTTSPlaybackCompleted(responseID) {
		t.Fatal("expected playback completion to match pending response")
	}

	pending = snapshotIdleChatTTSPending()
	if pending.PendingSessionCount != 0 || pending.PendingResponseCount != 0 || pending.TopicGateCount != 0 || pending.TopicRouteCount != 0 {
		t.Fatalf("unexpected pending snapshot after ack: %+v", pending)
	}
	if len(pending.PendingSessionIDs) != 0 || len(pending.PendingResponseIDs) != 0 {
		t.Fatalf("pending IDs should be empty after ack: %+v", pending)
	}
	public = snapshotTTSPublicSessions()
	if public.RouteCount != 0 {
		t.Fatalf("unexpected public session snapshot after ack: %+v", public)
	}
}

func TestEmitIdleChatTTS_AppendsSentencePauseForAgentMessage(t *testing.T) {
	bridge := &idleChatMockTTSBridge{}

	_, _ = emitIdleChatTTS(context.Background(), bridge, idlechat.TimelineEvent{
		Type:      "idlechat.message",
		From:      "mio",
		To:        "shiro",
		Content:   "次は別の観点で見てみよう",
		SessionID: "idle-3",
	})

	if len(bridge.pushTexts) != 1 {
		t.Fatalf("expected 1 push text, got %d", len(bridge.pushTexts))
	}
	if got := bridge.pushTexts[0]; got != "次は別の観点で見てみよう。" {
		t.Fatalf("unexpected filtered text: %q", got)
	}
}

func TestEmitIdleChatTTS_FormatsTopicAnnouncement(t *testing.T) {
	bridge := &idleChatMockTTSBridge{}

	_, _ = emitIdleChatTTS(context.Background(), bridge, idlechat.TimelineEvent{
		Type:      "idlechat.message",
		From:      "user",
		To:        "mio",
		Content:   "今日のお題（external）: 震災の追悼の杜で、記憶と風景の関係をどう捉えたらどうだろう？",
		SessionID: "idle-topic-1",
	})

	if len(bridge.pushTexts) != 1 {
		t.Fatalf("expected 1 push text, got %d", len(bridge.pushTexts))
	}
	want := "きょうのおだい、震災の追悼の杜で、記憶と風景の関係をどう捉えたらどうだろう？"
	if bridge.pushTexts[0] != want {
		t.Fatalf("unexpected topic tts text: got %q want %q", bridge.pushTexts[0], want)
	}
	if got := bridge.displayTexts[0]; got != "今日のお題：震災の追悼の杜で、記憶と風景の関係をどう捉えたらどうだろう？" {
		t.Fatalf("unexpected topic display text: %q", got)
	}
	if got := bridge.startReqs[0].CharacterID; got != "user" {
		t.Fatalf("topic announcement should be attributed to Ren/user, got %q", got)
	}
}

func TestNonStorySpeechTopicDoesNotRewrite(t *testing.T) {
	for _, strategy := range []idlechat.TopicStrategy{
		idlechat.StrategySingleGenre,
		idlechat.StrategyDoubleGenre,
		idlechat.StrategyExternalStimulus,
		idlechat.StrategyMovie,
		idlechat.StrategyNews,
		idlechat.StrategyForecast,
	} {
		t.Run(string(strategy), func(t *testing.T) {
			bridge := &idleChatMockTTSBridge{}
			topic := "盆栽と都市計画に共通する、成長を待つための設計"
			_, _ = emitIdleChatTTS(context.Background(), bridge, idlechat.TimelineEvent{
				Type:      "idlechat.topic",
				From:      "user",
				To:        "mio",
				Content:   "今日のお題（" + string(strategy) + "）: " + topic,
				SessionID: "idle-topic-no-rewrite",
				Category:  idlechat.TopicCategoryNews,
				Strategy:  strategy,
			})
			if len(bridge.pushTexts) != 1 {
				t.Fatalf("expected 1 push text, got %d", len(bridge.pushTexts))
			}
			want := "きょうのおだい、" + topic + "。"
			if bridge.pushTexts[0] != want {
				t.Fatalf("speech topic was rewritten: got %q want %q", bridge.pushTexts[0], want)
			}
		})
	}
}

func TestEmitIdleChatTTS_StripsSpeakerLabelsFromDisplayAndSpeech(t *testing.T) {
	bridge := &idleChatMockTTSBridge{}

	_, _ = emitIdleChatTTS(context.Background(), bridge, idlechat.TimelineEvent{
		Type:      "idlechat.message",
		From:      "mio",
		To:        "shiro",
		Content:   "mio: その封筒を開けた瞬間、棚の奥の雨音まで変わりそう。",
		SessionID: "idle-label",
	})

	if len(bridge.pushTexts) != 1 {
		t.Fatalf("expected 1 push text, got %d", len(bridge.pushTexts))
	}
	want := "その封筒を開けた瞬間、棚の奥の雨音まで変わりそう。"
	if got := bridge.pushTexts[0]; got != want {
		t.Fatalf("unexpected tts text: got %q want %q", got, want)
	}
	if got := bridge.displayTexts[0]; got != want {
		t.Fatalf("unexpected display text: got %q want %q", got, want)
	}
}

func TestEmitIdleChatTTS_DropsReasoningLinesFromScriptOutput(t *testing.T) {
	bridge := &idleChatMockTTSBridge{}

	_, _ = emitIdleChatTTS(context.Background(), bridge, idlechat.TimelineEvent{
		Type:      "idlechat.message",
		From:      "shiro",
		To:        "mio",
		Content:   "Looking at the example responses,\nshiro: 開ける前に、宛名の消え方を見たほうがいい。",
		SessionID: "idle-reasoning",
	})

	if len(bridge.pushTexts) != 1 {
		t.Fatalf("expected 1 push text, got %d", len(bridge.pushTexts))
	}
	want := "開ける前に、宛名の消え方を見たほうがいい。"
	if got := bridge.pushTexts[0]; got != want {
		t.Fatalf("unexpected tts text: got %q want %q", got, want)
	}
	if got := bridge.displayTexts[0]; got != want {
		t.Fatalf("unexpected display text: got %q want %q", got, want)
	}
}

func TestEmitIdleChatTTS_StripsPossibleResponsePrefix(t *testing.T) {
	bridge := &idleChatMockTTSBridge{}

	_, _ = emitIdleChatTTS(context.Background(), bridge, idlechat.TimelineEvent{
		Type:      "idlechat.message",
		From:      "shiro",
		To:        "mio",
		Content:   `Possible response: "雨上がりの空気は、薄い青色だったような気がする。"`,
		SessionID: "idle-possible-response",
	})

	if len(bridge.pushTexts) != 1 {
		t.Fatalf("expected 1 push text, got %d", len(bridge.pushTexts))
	}
	want := "雨上がりの空気は、薄い青色だったような気がする。"
	if got := bridge.pushTexts[0]; got != want {
		t.Fatalf("unexpected tts text: got %q want %q", got, want)
	}
	if got := bridge.displayTexts[0]; got != want {
		t.Fatalf("unexpected display text: got %q want %q", got, want)
	}
}

func TestEmitIdleChatTTS_DropsEmbeddedEnglishReasoningLines(t *testing.T) {
	bridge := &idleChatMockTTSBridge{}

	_, _ = emitIdleChatTTS(context.Background(), bridge, idlechat.TimelineEvent{
		Type: "idlechat.message",
		From: "shiro",
		To:   "mio",
		Content: "First, check the user's latest message:\n" +
			"Mio says, \"その指紋が残した傷が、実は次の誰かの目印だった\"\n" +
			"その「待つ存在」って、もしかして階段の先にいる誰か？\n" +
			"\" So Mio is suggesting the scars are a sign for someone to step forward,\n" +
			"and the \"waiting presence\" might be someone at the top of the stairs.",
		SessionID: "idle-english-reasoning",
	})

	if len(bridge.pushTexts) != 1 {
		t.Fatalf("expected 1 push text, got %d", len(bridge.pushTexts))
	}
	want := "その「待つ存在」って、もしかして階段の先にいる誰か？"
	if got := bridge.pushTexts[0]; got != want {
		t.Fatalf("unexpected tts text: got %q want %q", got, want)
	}
	if got := bridge.displayTexts[0]; got != want {
		t.Fatalf("unexpected display text: got %q want %q", got, want)
	}
}

func TestEmitIdleChatTTS_CutsInlineEnglishReasoningTail(t *testing.T) {
	bridge := &idleChatMockTTSBridge{}

	_, _ = emitIdleChatTTS(context.Background(), bridge, idlechat.TimelineEvent{
		Type:      "idlechat.message",
		From:      "shiro",
		To:        "mio",
		Content:   `雨上がりの庭で、苔が宿主の根を守りながら共生する姿は、静かな中にも生命力の循環が感じられる。" That's one sentence. Maybe add a question.`,
		SessionID: "idle-inline-reasoning",
	})

	if len(bridge.pushTexts) != 1 {
		t.Fatalf("expected 1 push text, got %d", len(bridge.pushTexts))
	}
	want := "雨上がりの庭で、苔が宿主の根を守りながら共生する姿は、静かな中にも生命力の循環が感じられる。"
	if got := bridge.pushTexts[0]; got != want {
		t.Fatalf("unexpected tts text: got %q want %q", got, want)
	}
	if got := bridge.displayTexts[0]; got != want {
		t.Fatalf("unexpected display text: got %q want %q", got, want)
	}
}

func TestEmitIdleChatTTSAsyncTopicAnnouncementReturnsCompletion(t *testing.T) {
	bridge := &idleChatMockTTSBridge{notifyOnEnd: true}

	done := emitIdleChatTTSAsync(bridge, idlechat.TimelineEvent{
		Type:      "idlechat.message",
		From:      "user",
		To:        "mio",
		Content:   "今日のお題（external）: 記憶と風景の関係",
		SessionID: "idle-topic-async",
	})
	if done == nil {
		t.Fatal("expected topic announcement to return a completion channel")
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("topic TTS completion was not signaled")
	}
	if len(bridge.pushTexts) != 1 {
		t.Fatalf("expected topic TTS to be pushed, got %d", len(bridge.pushTexts))
	}
}

func TestEmitIdleChatTTSAsyncSerializesIdleSpeech(t *testing.T) {
	bridge := &idleChatMockTTSBridge{notifyOnEnd: true}

	first := emitIdleChatTTSAsync(bridge, idlechat.TimelineEvent{
		Type:      "idlechat.message",
		From:      "mio",
		To:        "shiro",
		Content:   "先の発話です。",
		SessionID: "idle-serial-1",
	})
	second := emitIdleChatTTSAsync(bridge, idlechat.TimelineEvent{
		Type:      "idlechat.message",
		From:      "shiro",
		To:        "mio",
		Content:   "後の発話です。",
		SessionID: "idle-serial-1",
	})

	for name, done := range map[string]<-chan struct{}{"first": first, "second": second} {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatalf("%s TTS completion was not signaled", name)
		}
	}
	if len(bridge.pushTexts) < 2 {
		t.Fatalf("expected two serialized pushes, got %d", len(bridge.pushTexts))
	}
	if bridge.pushTexts[len(bridge.pushTexts)-2] != "先の発話です。" || bridge.pushTexts[len(bridge.pushTexts)-1] != "後の発話です。" {
		t.Fatalf("speech was not serialized in enqueue order: %#v", bridge.pushTexts)
	}
}

func TestEmitIdleChatTTSAsyncPrefetchesWithoutPlaybackCompletion(t *testing.T) {
	clearAllIdleChatTTSPending()
	resetTTSPublicSessionStateForTest()
	setIdleChatViewerClientCount(func() int { return 1 })
	t.Cleanup(func() {
		setIdleChatViewerClientCount(nil)
		clearAllIdleChatTTSPending()
		resetTTSPublicSessionStateForTest()
	})
	bridge := &idleChatMockTTSBridge{notifyOnEnd: false}

	first := emitIdleChatTTSAsync(bridge, idlechat.TimelineEvent{
		Type:      "idlechat.message",
		From:      "mio",
		To:        "shiro",
		Content:   "先に合成する発話です。",
		SessionID: "idle-prefetch-1",
	})
	second := emitIdleChatTTSAsync(bridge, idlechat.TimelineEvent{
		Type:      "idlechat.message",
		From:      "shiro",
		To:        "mio",
		Content:   "再生完了を待たずに合成する発話です。",
		SessionID: "idle-prefetch-1",
	})

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(bridge.pushTexts) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(bridge.pushTexts) < 2 {
		t.Fatalf("expected queued speech to be synthesized without playback completion, got %d pushes", len(bridge.pushTexts))
	}
	for name, done := range map[string]<-chan struct{}{"first": first, "second": second} {
		select {
		case <-done:
			t.Fatalf("%s playback completion was signaled before playback ack", name)
		default:
		}
	}

	var responseIDs []string
	for _, responseID := range snapshotIdleChatTTSPending().PendingResponseIDs {
		if strings.HasPrefix(responseID, "idle-prefetch-1:") {
			responseIDs = append(responseIDs, responseID)
		}
	}
	if len(responseIDs) != 2 {
		t.Fatalf("expected 2 pending playback responses, got %d", len(responseIDs))
	}
	for _, responseID := range responseIDs {
		notifyIdleChatTTSPlaybackCompleted(responseID)
	}
	for name, done := range map[string]<-chan struct{}{"first": first, "second": second} {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatalf("%s playback completion was not signaled after ack", name)
		}
	}
	if bridge.pushTexts[len(bridge.pushTexts)-2] != "先に合成する発話です。" ||
		bridge.pushTexts[len(bridge.pushTexts)-1] != "再生完了を待たずに合成する発話です。" {
		t.Fatalf("unexpected synthesis order: %#v", bridge.pushTexts)
	}
}

func TestMarkIdleChatTTSTimeoutConsumesPendingAsFailedPlayback(t *testing.T) {
	resetTTSPublicSessionStateForTest()
	clearAllIdleChatTTSPending()

	first := registerIdleChatTTSPending("idle-timeout-tts-1", "idle-timeout:0000")
	second := registerIdleChatTTSPending("idle-timeout-tts-2", "idle-timeout:0001")
	registerTTSPublicSessionWithMessage("idle-timeout-tts-1", "idle-timeout", "idle-timeout:0000", "idle-timeout:msg:0001", 1)
	registerTTSPublicSessionWithMessage("idle-timeout-tts-2", "idle-timeout", "idle-timeout:0001", "idle-timeout:msg:0002", 2)

	markIdleChatTTSTimeout(idlechat.TTSTimeoutEvent{
		Kind:      "timeout",
		SessionID: "idle-timeout",
		MessageID: "idle-timeout:msg:0001",
		TurnIndex: 1,
	})

	select {
	case <-first:
	case <-time.After(time.Second):
		t.Fatal("timed out utterance should be consumed as failed playback")
	}
	select {
	case <-second:
		t.Fatal("next utterance should remain pending")
	default:
	}

	if notifyIdleChatTTSPlaybackCompleted("idle-timeout:0000") {
		t.Fatal("late playback ack must not match an already consumed timeout")
	}
	public := snapshotTTSPublicSessions()
	if public.RouteCount != 1 || public.StaleRouteCount != 0 {
		t.Fatalf("only the next utterance route should remain after timeout, got %+v", public)
	}
}

func TestMarkIdleChatTTSSessionAudioTimeoutClosesAllPendingForSession(t *testing.T) {
	resetTTSPublicSessionStateForTest()
	clearAllIdleChatTTSPending()

	first := registerIdleChatTTSPending("idle-drain-tts-1", "idle-drain:0000")
	second := registerIdleChatTTSPending("idle-drain-tts-2", "idle-drain:0001")
	registerTTSPublicSessionWithMessage("idle-drain-tts-1", "idle-drain", "idle-drain:0000", "idle-drain:msg:0001", 1)
	registerTTSPublicSessionWithMessage("idle-drain-tts-2", "idle-drain", "idle-drain:0001", "idle-drain:msg:0002", 2)

	markIdleChatTTSTimeout(idlechat.TTSTimeoutEvent{
		Kind:           "session_audio_timeout",
		SessionID:      "idle-drain",
		RemainingIndex: 1,
		RemainingCount: 2,
	})

	for name, ch := range map[string]<-chan struct{}{"first": first, "second": second} {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatalf("%s pending wait should close on session audio timeout", name)
		}
	}
	public := snapshotTTSPublicSessions()
	if public.RouteCount != 0 || public.StaleRouteCount != 0 {
		t.Fatalf("session audio timeout should consume all public routes, got %+v", public)
	}
}

func TestEmitIdleChatTTS_RemovesLoopNotesFromSpeechOnly(t *testing.T) {
	bridge := &idleChatMockTTSBridge{}
	content := "今回のまとめです。\n注記: テンプレ反復で打ち切り\n\n本文を読み上げます。"

	_, _ = emitIdleChatTTS(context.Background(), bridge, idlechat.TimelineEvent{
		Type:      "idlechat.message",
		From:      "mio",
		To:        "user",
		Content:   content,
		SessionID: "idle-note-1",
	})

	if len(bridge.pushTexts) != 1 {
		t.Fatalf("expected 1 push text, got %d", len(bridge.pushTexts))
	}
	if strings.Contains(bridge.pushTexts[0], "注記:") {
		t.Fatalf("note leaked into speech text: %q", bridge.pushTexts[0])
	}
	if !strings.Contains(bridge.pushTexts[0], "今回のまとめです。") || !strings.Contains(bridge.pushTexts[0], "本文を読み上げます。") {
		t.Fatalf("unexpected speech text: %q", bridge.pushTexts[0])
	}
	if got := formatIdleChatDisplayText(idlechat.TimelineEvent{Content: content}); strings.Contains(got, "注記:") {
		t.Fatalf("tts display text should not keep note, got %q", got)
	}
}

func TestEmitIdleChatTTSSkipsNonMessageEvent(t *testing.T) {
	bridge := &idleChatMockTTSBridge{}

	_, _ = emitIdleChatTTS(context.Background(), bridge, idlechat.TimelineEvent{
		Type:      "idlechat.summary",
		From:      "shiro",
		Content:   "summary",
		SessionID: "idle-2",
	})

	if len(bridge.startReqs) != 0 || len(bridge.pushTexts) != 0 || len(bridge.endIDs) != 0 {
		t.Fatal("expected no tts calls for non-message event")
	}
}

func TestIdleChatVoiceForSpeaker(t *testing.T) {
	voiceID, voiceProfile := idleChatVoiceForSpeaker("shiro")
	if voiceID != "male_01" || voiceProfile != "lumina_male" {
		t.Fatalf("unexpected shiro voice mapping: %q %q", voiceID, voiceProfile)
	}
	voiceID, voiceProfile = idleChatVoiceForSpeaker("mio")
	if voiceID != "mio" || voiceProfile != "lumina_female" {
		t.Fatalf("unexpected mio voice mapping: %q %q", voiceID, voiceProfile)
	}
}
