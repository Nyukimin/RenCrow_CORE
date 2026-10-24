package orchestrator

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

const (
	defaultTTSVoiceProfile = "lumina_female"
	ttsChunkMinRunes       = moduletts.TTSChunkMinRunes
	ttsChunkMaxRunes       = moduletts.TTSChunkMaxRunes
)

func buildTTSContext(route routing.Route, urgency string, attention bool) moduletts.EmotionContext {
	ctx := moduletts.BuildRouteTTSContext(string(route), urgency, attention, time.Now())
	return emotionContextFromRouteTTS(ctx)
}

func emotionContextFromRouteTTS(ctx moduletts.RouteTTSContext) moduletts.EmotionContext {
	return moduletts.EmotionContext{
		ConversationMode:      ctx.ConversationMode,
		TimeOfDay:             ctx.TimeOfDay,
		Urgency:               ctx.Urgency,
		UserAttentionRequired: ctx.UserAttentionRequired,
	}
}

func eventForRoute(route routing.Route) string {
	return moduletts.EventForRoute(string(route))
}

func conversationModeForRoute(route routing.Route) string {
	return moduletts.ConversationModeForRoute(string(route))
}

func buildTTSPayload(eventType string, route routing.Route, text string, ctx moduletts.EmotionContext, voiceProfile string) (string, *moduletts.EmotionState) {
	filtered := moduletts.FilterSpeakableText(eventType, string(route), text)
	if filtered == "" {
		return "", nil
	}
	emotion := moduletts.PlanEmotion(moduletts.EmotionInput{
		Event:        eventForRoute(route),
		Text:         filtered,
		Context:      ctx,
		VoiceProfile: chooseNonEmpty(voiceProfile, defaultTTSVoiceProfile),
	})
	return filtered, &emotion
}

// voiceProfileForSpeaker returns the emotion-planning profile of the Agent that
// is speaking. An unresolved speaker is reported and refused: the caller skips
// the utterance instead of speaking with a default Agent's voice.
func voiceProfileForSpeaker(speaker, logPrefix string) (string, bool) {
	_, voiceProfile, ok := moduletts.VoiceForAgent(speaker)
	if !ok {
		log.Printf("%s unresolved speaker %q: utterance not voiced", logPrefix, speaker)
	}
	return voiceProfile, ok
}

func chooseNonEmpty(v, def string) string {
	return moduletts.ChooseNonEmpty(v, def)
}

func pushTTS(ctx context.Context, bridge TTSBridge, sessionID, text string, emotion *moduletts.EmotionState, prefix string) {
	pushTTSWithDisplay(ctx, bridge, sessionID, text, text, emotion, prefix)
}

func pushTTSWithDisplay(ctx context.Context, bridge TTSBridge, sessionID, speechText, displayText string, emotion *moduletts.EmotionState, prefix string) {
	if bridge == nil || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(speechText) == "" {
		return
	}
	var err error
	if displayBridge, ok := bridge.(TTSDisplayBridge); ok {
		err = displayBridge.PushTextWithDisplay(ctx, sessionID, speechText, displayText, emotion)
	} else {
		err = bridge.PushText(ctx, sessionID, speechText, emotion)
	}
	if err != nil {
		log.Printf("%s %v", prefix, err)
	}
}

type ttsStreamForwarder struct {
	bridge       TTSBridge
	parentCtx    context.Context
	sessionID    string
	route        routing.Route
	eventType    string
	ttsCtx       moduletts.EmotionContext
	voiceProfile string
	logPrefix    string
	chunker      moduletts.StreamChunker
	queue        chan string
	wg           sync.WaitGroup
	mu           sync.Mutex
	closed       bool
}

// newTTSStreamForwarder voices the stream of the given speaker (the Agent whose
// model generates the tokens, equal to the turn's agent.response from).
func newTTSStreamForwarder(bridge TTSBridge, sessionID string, route routing.Route, speaker, eventType, logPrefix string, requestCtx context.Context) *ttsStreamForwarder {
	if bridge == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	voiceProfile, ok := voiceProfileForSpeaker(speaker, logPrefix)
	if !ok {
		return nil
	}
	f := &ttsStreamForwarder{
		bridge:       bridge,
		parentCtx:    requestCtx,
		sessionID:    sessionID,
		route:        route,
		eventType:    eventType,
		ttsCtx:       buildTTSContext(route, "normal", false),
		voiceProfile: voiceProfile,
		logPrefix:    logPrefix,
		queue:        make(chan string, 32),
	}
	f.wg.Add(1)
	go f.run()
	return f
}

func (f *ttsStreamForwarder) run() {
	defer f.wg.Done()
	for text := range f.queue {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(f.parentCtx), 20*time.Second)
		f.pushChunk(ctx, text)
		cancel()
	}
}

func (f *ttsStreamForwarder) pushChunk(ctx context.Context, text string) {
	filtered, emotion := buildTTSPayload(f.eventType, f.route, text, f.ttsCtx, f.voiceProfile)
	if filtered == "" {
		return
	}
	pushTTSWithDisplay(ctx, f.bridge, f.sessionID, filtered, text, emotion, f.logPrefix)
}

func (f *ttsStreamForwarder) OnToken(ctx context.Context, token string) {
	if f == nil || token == "" {
		return
	}
	for _, chunk := range f.chunker.AcceptToken(token) {
		f.emit(ctx, chunk)
	}
}

func (f *ttsStreamForwarder) Finalize(ctx context.Context, finalText string) {
	if f == nil {
		return
	}
	defer f.closeAndDrain()
	for _, chunk := range f.chunker.FinalizeAll(finalText) {
		f.emit(ctx, chunk)
	}
}

func pushTTSTextChunks(ctx context.Context, bridge TTSBridge, sessionID string, route routing.Route, eventType, text string, ttsCtx moduletts.EmotionContext, voiceProfile string, prefix string) {
	for _, displayChunk := range SplitTTSChunks(text) {
		filtered, emotion := buildTTSPayload(eventType, route, displayChunk, ttsCtx, voiceProfile)
		if filtered == "" {
			continue
		}
		pushTTSWithDisplay(ctx, bridge, sessionID, filtered, displayChunk, emotion, prefix)
	}
}

func (f *ttsStreamForwarder) emit(_ context.Context, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	f.mu.Lock()
	closed := f.closed
	q := f.queue
	f.mu.Unlock()
	if closed || q == nil {
		return
	}
	q <- text
}

func (f *ttsStreamForwarder) closeAndDrain() {
	f.mu.Lock()
	if !f.closed && f.queue != nil {
		close(f.queue)
		f.closed = true
	}
	f.mu.Unlock()
	f.wg.Wait()
}

func nextTTSChunk(text string, final bool) (chunk, rest string, ok bool) {
	return moduletts.NextTTSChunk(text, final)
}

func SplitTTSChunks(text string) []string {
	return moduletts.SplitTTSChunks(text)
}

func chooseTTSChunkCut(lastHard, lastSoft, lastSpace int) int {
	return moduletts.ChooseTTSChunkCut(lastHard, lastSoft, lastSpace)
}

func splitTTSChunk(text string, cut int) (chunk, rest string, ok bool) {
	return moduletts.SplitTTSChunk(text, cut)
}

func extendTTSChunkCut(text string, cut int) int {
	return moduletts.ExtendTTSChunkCut(text, cut)
}

func isTTSClosingBoundary(r rune) bool {
	return moduletts.IsTTSClosingBoundary(r)
}

func isTTSHardBoundary(r rune) bool {
	return moduletts.IsTTSHardBoundary(r)
}

func isTTSSoftBoundary(r rune) bool {
	return moduletts.IsTTSSoftBoundary(r)
}
