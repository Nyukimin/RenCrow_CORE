package orchestrator

import (
	"context"
	"fmt"
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
	ttsStreamQueueCapacity = 32
)

type ttsStreamQueueFullError struct {
	capacity int
}

func (e ttsStreamQueueFullError) Error() string {
	return fmt.Sprintf("TTS stream queue reached capacity %d", e.capacity)
}

func (e ttsStreamQueueFullError) QueueCapacity() int { return e.capacity }

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

func pushTTSWithDisplay(ctx context.Context, bridge TTSBridge, sessionID, speechText, displayText string, emotion *moduletts.EmotionState, prefix string) error {
	if bridge == nil || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(speechText) == "" {
		return nil
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
	return err
}

type ttsStreamForwarder struct {
	bridge       TTSBridge
	ctx          context.Context
	cancel       context.CancelFunc
	sessionID    string
	route        routing.Route
	eventType    string
	ttsCtx       moduletts.EmotionContext
	voiceProfile string
	logPrefix    string
	chunker      moduletts.StreamChunker
	queue        chan ttsStreamChunk
	wg           sync.WaitGroup
	mu           sync.Mutex
	closed       bool
	aborted      bool
	err          error
}

// ttsStreamChunk is admitted to a bounded in-memory queue. The optional
// validator performs only the short owner/generation check immediately before
// handing this chunk to the external bridge.
type ttsStreamChunk struct {
	text     string
	validate func(func() error) error
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
	if requestCtx == nil {
		requestCtx = context.Background()
	}
	forwarderCtx, cancel := context.WithCancel(context.WithoutCancel(requestCtx))
	f := &ttsStreamForwarder{
		bridge:       bridge,
		ctx:          forwarderCtx,
		cancel:       cancel,
		sessionID:    sessionID,
		route:        route,
		eventType:    eventType,
		ttsCtx:       buildTTSContext(route, "normal", false),
		voiceProfile: voiceProfile,
		logPrefix:    logPrefix,
		queue:        make(chan ttsStreamChunk, ttsStreamQueueCapacity),
	}
	f.wg.Add(1)
	go f.run()
	return f
}

func (f *ttsStreamForwarder) run() {
	defer f.wg.Done()
	for {
		select {
		case <-f.ctx.Done():
			return
		case chunk, ok := <-f.queue:
			if !ok {
				return
			}
			f.mu.Lock()
			aborted := f.aborted
			f.mu.Unlock()
			if aborted || f.ctx.Err() != nil {
				return
			}
			ctx, cancel := context.WithTimeout(f.ctx, 20*time.Second)
			err := f.pushChunk(ctx, chunk)
			cancel()
			if err != nil {
				f.mu.Lock()
				if f.err == nil {
					f.err = err
				}
				f.abortLocked()
				f.mu.Unlock()
				f.discardQueued()
				return
			}
		}
	}
}

func (f *ttsStreamForwarder) pushChunk(ctx context.Context, chunk ttsStreamChunk) error {
	filtered, emotion := buildTTSPayload(f.eventType, f.route, chunk.text, f.ttsCtx, f.voiceProfile)
	if filtered == "" {
		return nil
	}
	if chunk.validate != nil {
		if err := chunk.validate(func() error { return nil }); err != nil {
			return fmt.Errorf("TTS chunk owner qualification failed: %w", err)
		}
	}
	// Successful validation is the handoff admission linearization point. No
	// owner guard is held across the bridge call; revocation after this point
	// may race with a bridge operation that has already been admitted.
	f.mu.Lock()
	aborted := f.aborted || f.ctx.Err() != nil
	f.mu.Unlock()
	if aborted {
		return context.Canceled
	}
	return pushTTSWithDisplay(ctx, f.bridge, f.sessionID, filtered, chunk.text, emotion, f.logPrefix)
}

func (f *ttsStreamForwarder) OnToken(ctx context.Context, token string) {
	if f == nil || token == "" {
		return
	}
	f.mu.Lock()
	if f.closed || f.aborted {
		f.mu.Unlock()
		return
	}
	var admissionErr error
	for _, chunk := range f.chunker.AcceptToken(token) {
		if admissionErr = f.enqueueLocked(ttsStreamChunk{text: chunk}); admissionErr != nil {
			f.err = admissionErr
			f.abortLocked()
			break
		}
	}
	f.mu.Unlock()
	if admissionErr != nil {
		log.Printf("%s %v", f.logPrefix, admissionErr)
		f.discardQueued()
	}
}

func (f *ttsStreamForwarder) Finalize(ctx context.Context, finalText string) {
	f.FinalizeWithAdmission(ctx, finalText, nil)
}

// FinalizeWithAdmission queues the final text with an optional per-chunk
// qualification check. Validation runs in the forwarder immediately before
// bridge handoff and never holds owner guards across the external call.
func (f *ttsStreamForwarder) FinalizeWithAdmission(ctx context.Context, finalText string, validate func(func() error) error) {
	if f == nil {
		return
	}
	f.mu.Lock()
	var admissionErr error
	if !f.closed && !f.aborted {
		for _, chunk := range f.chunker.FinalizeAll(finalText) {
			if admissionErr = f.enqueueLocked(ttsStreamChunk{text: chunk, validate: validate}); admissionErr != nil {
				f.err = admissionErr
				f.abortLocked()
				break
			}
		}
		if !f.closed {
			f.closed = true
			close(f.queue)
		}
	}
	f.mu.Unlock()
	if admissionErr != nil {
		log.Printf("%s %v", f.logPrefix, admissionErr)
		f.discardQueued()
	}
	f.wg.Wait()
	f.cancel()
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

func (f *ttsStreamForwarder) enqueueLocked(chunk ttsStreamChunk) error {
	if strings.TrimSpace(chunk.text) == "" {
		return nil
	}
	if f.closed || f.aborted || f.queue == nil {
		return nil
	}
	select {
	case f.queue <- chunk:
		return nil
	default:
		return ttsStreamQueueFullError{capacity: cap(f.queue)}
	}
}

func (f *ttsStreamForwarder) abortLocked() {
	if f.aborted {
		return
	}
	f.aborted = true
	f.chunker = moduletts.StreamChunker{}
	f.cancel()
	if !f.closed {
		f.closed = true
		close(f.queue)
	}
}

func (f *ttsStreamForwarder) discardQueued() {
	for {
		select {
		case _, ok := <-f.queue:
			if !ok {
				return
			}
		default:
			return
		}
	}
}

func (f *ttsStreamForwarder) Abort() {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.abortLocked()
	f.mu.Unlock()
	f.discardQueued()
	f.wg.Wait()
	f.cancel()
}

func (f *ttsStreamForwarder) Err() error {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}

// FinalizeWithAdmission applies the same short qualification to final speech
// chunks and the final VTuber text handoff. Neither bridge is called while the
// qualification's owner/session guards are held.
func (b *streamBundle) FinalizeWithAdmission(ctx context.Context, finalText string, validate func(func() error) error) {
	if b == nil {
		return
	}
	if b.tts != nil {
		b.tts.FinalizeWithAdmission(ctx, finalText, validate)
		if b.tts.Err() != nil {
			if b.vtuber != nil {
				b.vtuber.Abort()
			}
			return
		}
	}
	if b.vtuber != nil {
		if validate != nil {
			if err := validate(func() error { return nil }); err != nil {
				b.err = fmt.Errorf("VTuber final text owner qualification failed: %w", err)
				b.vtuber.Abort()
				return
			}
		}
		b.vtuber.Finalize(ctx, finalText)
	}
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
