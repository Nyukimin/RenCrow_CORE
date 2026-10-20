package orchestrator

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/speech"
)

type speechTurnCancellationContextKey struct{}

const speechSynthesisTurnTimeout = 5 * time.Minute

var (
	ErrSpeechProgressIndex      = errors.New("speech playback utterance index is out of range")
	ErrSpeechProgressNotReady   = errors.New("speech playback utterance is not ready")
	ErrSpeechProgressOutOfOrder = errors.New("speech playback phase is out of order")
	ErrSpeechPlaybackOutput     = errors.New("speech playback output is invalid or already fixed")
)

// SpeechTurnCancellation is a speech-only cancellation latch for one accepted
// turn. It can be canceled before a synthesis child context is created.
type SpeechTurnCancellation struct {
	mu        sync.Mutex
	canceled  bool
	nextID    uint64
	bindings  map[uint64]context.CancelFunc
	createdAt time.Time

	progress        [speech.MaxUtterances]speech.PlaybackPhase
	startedAt       [speech.MaxUtterances]time.Time
	endedAt         [speech.MaxUtterances]time.Time
	nextReady       int
	nextStarted     int
	nextEnded       int
	progressErr     error
	progressChanged chan struct{}
	playbackOutput  string
	outputFixed     bool
}

// NewSpeechTurnCancellation creates a latch for one accepted speech turn.
func NewSpeechTurnCancellation() *SpeechTurnCancellation {
	return &SpeechTurnCancellation{createdAt: time.Now(), progressChanged: make(chan struct{})}
}

// TurnCreatedAt returns when this accepted speech turn created its latch.
func (c *SpeechTurnCancellation) TurnCreatedAt() time.Time {
	if c == nil {
		return time.Time{}
	}
	return c.createdAt
}

// PlaybackStartedAt returns the actual playback start timestamp when present.
func (c *SpeechTurnCancellation) PlaybackStartedAt(index int) (time.Time, bool) {
	if c == nil || validateSpeechProgressIndex(index) != nil {
		return time.Time{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.startedAt[index].IsZero() {
		return time.Time{}, false
	}
	return c.startedAt[index], true
}

// PlaybackEndedAt returns the natural playback end timestamp when present.
func (c *SpeechTurnCancellation) PlaybackEndedAt(index int) (time.Time, bool) {
	if c == nil || validateSpeechProgressIndex(index) != nil {
		return time.Time{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.endedAt[index].IsZero() {
		return time.Time{}, false
	}
	return c.endedAt[index], true
}

// WithSpeechTurnCancellation attaches the latch to the processing context.
func WithSpeechTurnCancellation(ctx context.Context, cancellation *SpeechTurnCancellation) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if cancellation == nil {
		return ctx
	}
	ctx = context.WithValue(ctx, speechTurnCancellationContextKey{}, cancellation)
	return speech.WithPlaybackProgress(ctx, cancellation)
}

// SpeechSynthesisContext creates a child context that can be canceled without
// canceling the turn's processing context. done cancels and unbinds that child.
func SpeechSynthesisContext(ctx context.Context) (speechCtx context.Context, done context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	speechCtx, cancel := context.WithTimeout(ctx, speechSynthesisTurnTimeout)
	cancellation, _ := ctx.Value(speechTurnCancellationContextKey{}).(*SpeechTurnCancellation)
	if cancellation == nil {
		return speechCtx, cancel
	}
	bindingID := cancellation.bind(cancel)
	return speechCtx, func() {
		cancellation.unbind(bindingID)
		cancel()
	}
}

// Cancel latches cancellation and cancels every currently bound speech child.
func (c *SpeechTurnCancellation) Cancel() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.canceled && len(c.bindings) == 0 {
		c.mu.Unlock()
		return
	}
	c.canceled = true
	if c.progressErr == nil {
		c.progressErr = context.Canceled
		if index := c.currentProgressIndexLocked(); index >= 0 {
			c.progress[index] = speech.PlaybackPhaseCancelled
		}
		c.signalProgressLocked()
	}
	cancels := make([]context.CancelFunc, 0, len(c.bindings))
	for _, cancel := range c.bindings {
		cancels = append(cancels, cancel)
	}
	c.bindings = nil
	c.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (c *SpeechTurnCancellation) bind(cancel context.CancelFunc) uint64 {
	c.mu.Lock()
	if c.canceled {
		c.mu.Unlock()
		cancel()
		return 0
	}
	c.nextID++
	if c.bindings == nil {
		c.bindings = make(map[uint64]context.CancelFunc)
	}
	bindingID := c.nextID
	c.bindings[bindingID] = cancel
	c.mu.Unlock()
	return bindingID
}

func (c *SpeechTurnCancellation) unbind(bindingID uint64) {
	if c == nil || bindingID == 0 {
		return
	}
	c.mu.Lock()
	delete(c.bindings, bindingID)
	c.mu.Unlock()
}

// MarkReady records that a complete utterance is available to the Viewer.
func (c *SpeechTurnCancellation) MarkReady(index int) error {
	if c == nil {
		return errors.New("speech playback progress is unavailable")
	}
	if err := validateSpeechProgressIndex(index); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.progressErr != nil {
		return c.progressErr
	}
	if c.progress[index] == speech.PlaybackPhaseReady {
		return nil
	}
	if index != c.nextReady {
		return ErrSpeechProgressOutOfOrder
	}
	if index > 0 && c.progress[index-1] != speech.PlaybackPhaseStarted && c.progress[index-1] != speech.PlaybackPhaseEnded {
		return ErrSpeechProgressOutOfOrder
	}
	c.progress[index] = speech.PlaybackPhaseReady
	c.nextReady++
	c.signalProgressLocked()
	return nil
}

// WaitStarted waits until the Viewer reports actual playback start.
func (c *SpeechTurnCancellation) WaitStarted(ctx context.Context, index int) error {
	return c.waitForProgress(ctx, index, speech.PlaybackPhaseStarted)
}

// WaitEnded waits until the Viewer reports natural playback completion.
func (c *SpeechTurnCancellation) WaitEnded(ctx context.Context, index int) error {
	return c.waitForProgress(ctx, index, speech.PlaybackPhaseEnded)
}

// MarkStarted accepts a Viewer report only after this utterance is ready and
// every previous utterance has ended.
func (c *SpeechTurnCancellation) MarkStarted(index int) error {
	if c == nil {
		return errors.New("speech playback progress is unavailable")
	}
	if err := validateSpeechProgressIndex(index); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.progressErr != nil {
		return c.progressErr
	}
	if c.progress[index] == speech.PlaybackPhaseStarted {
		return nil
	}
	if index >= c.nextReady || c.progress[index] != speech.PlaybackPhaseReady {
		return ErrSpeechProgressNotReady
	}
	if index != c.nextStarted || (index > 0 && c.progress[index-1] != speech.PlaybackPhaseEnded) {
		return ErrSpeechProgressOutOfOrder
	}
	c.progress[index] = speech.PlaybackPhaseStarted
	c.startedAt[index] = time.Now()
	c.nextStarted++
	c.signalProgressLocked()
	return nil
}

// MarkEnded accepts a Viewer report only for the next started utterance.
func (c *SpeechTurnCancellation) MarkEnded(index int) error {
	if c == nil {
		return errors.New("speech playback progress is unavailable")
	}
	if err := validateSpeechProgressIndex(index); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.progressErr != nil {
		return c.progressErr
	}
	if c.progress[index] == speech.PlaybackPhaseEnded {
		return nil
	}
	if index != c.nextEnded || index >= c.nextStarted || c.progress[index] != speech.PlaybackPhaseStarted {
		return ErrSpeechProgressOutOfOrder
	}
	c.progress[index] = speech.PlaybackPhaseEnded
	c.endedAt[index] = time.Now()
	c.nextEnded++
	c.signalProgressLocked()
	return nil
}

// IsReady reports whether the Viewer has been given this utterance. It stays
// true after playback advances so duplicate reports can be rejected or handled
// idempotently by the caller without accepting an unready index.
func (c *SpeechTurnCancellation) IsReady(index int) bool {
	if c == nil || validateSpeechProgressIndex(index) != nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return index < c.nextReady
}

// SetPlaybackOutput fixes the playback owner before the first utterance is
// published. Repeating the selected value is safe for the same turn.
func (c *SpeechTurnCancellation) SetPlaybackOutput(output string) error {
	if c == nil || (output != "viewer" && output != "local") {
		return ErrSpeechPlaybackOutput
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.outputFixed {
		if c.playbackOutput == output {
			return nil
		}
		return ErrSpeechPlaybackOutput
	}
	if c.nextReady > 0 {
		return ErrSpeechPlaybackOutput
	}
	c.playbackOutput = output
	c.outputFixed = true
	return nil
}

// IsViewerPlayback defaults to Viewer ownership until transport selects a
// playback output for this turn.
func (c *SpeechTurnCancellation) IsViewerPlayback() bool {
	if c == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.outputFixed || c.playbackOutput == "viewer"
}

// MarkError makes the turn terminal and releases any synthesis progress wait.
func (c *SpeechTurnCancellation) MarkError(index int, cause error) error {
	if c == nil {
		return errors.New("speech playback progress is unavailable")
	}
	if err := validateSpeechProgressIndex(index); err != nil {
		return err
	}
	if cause == nil {
		cause = errors.New("Viewer reported speech playback error")
	}
	c.mu.Lock()
	if c.progress[index] == speech.PlaybackPhaseError {
		c.mu.Unlock()
		return nil
	}
	if c.progressErr != nil {
		err := c.progressErr
		c.mu.Unlock()
		return err
	}
	if index > c.nextReady {
		c.mu.Unlock()
		return ErrSpeechProgressOutOfOrder
	}
	phase := c.progress[index]
	if phase != "" && phase != speech.PlaybackPhaseReady && phase != speech.PlaybackPhaseStarted {
		c.mu.Unlock()
		return ErrSpeechProgressOutOfOrder
	}
	c.progress[index] = speech.PlaybackPhaseError
	c.progressErr = cause
	c.canceled = true
	cancels := c.takeBindingsLocked()
	c.signalProgressLocked()
	c.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	return nil
}

// MarkCancelled marks playback terminal and cancels only this turn's speech
// children, leaving the surrounding CHAT request context alive.
func (c *SpeechTurnCancellation) MarkCancelled(index int) error {
	if c == nil {
		return errors.New("speech playback progress is unavailable")
	}
	if err := validateSpeechProgressIndex(index); err != nil {
		return err
	}
	c.mu.Lock()
	if c.progress[index] == speech.PlaybackPhaseCancelled {
		c.mu.Unlock()
		return nil
	}
	if c.progressErr != nil {
		err := c.progressErr
		c.mu.Unlock()
		return err
	}
	if index >= c.nextReady || (c.progress[index] != speech.PlaybackPhaseReady && c.progress[index] != speech.PlaybackPhaseStarted) {
		c.mu.Unlock()
		return ErrSpeechProgressOutOfOrder
	}
	c.progress[index] = speech.PlaybackPhaseCancelled
	c.canceled = true
	c.progressErr = context.Canceled
	cancels := c.takeBindingsLocked()
	c.signalProgressLocked()
	c.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	return nil
}

func (c *SpeechTurnCancellation) waitForProgress(ctx context.Context, index int, target speech.PlaybackPhase) error {
	if c == nil {
		return errors.New("speech playback progress is unavailable")
	}
	if err := validateSpeechProgressIndex(index); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		c.mu.Lock()
		if c.progressErr != nil {
			err := c.progressErr
			c.mu.Unlock()
			return err
		}
		phase := c.progress[index]
		if target == speech.PlaybackPhaseStarted && (phase == speech.PlaybackPhaseStarted || phase == speech.PlaybackPhaseEnded) {
			c.mu.Unlock()
			return nil
		}
		if phase == target {
			c.mu.Unlock()
			return nil
		}
		if index >= c.nextReady {
			c.mu.Unlock()
			return ErrSpeechProgressNotReady
		}
		changed := c.progressChanged
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

func validateSpeechProgressIndex(index int) error {
	if index < 0 || index >= speech.MaxUtterances {
		return ErrSpeechProgressIndex
	}
	return nil
}

func (c *SpeechTurnCancellation) currentProgressIndexLocked() int {
	if c.nextStarted > c.nextEnded {
		return c.nextEnded
	}
	if c.nextReady > c.nextStarted {
		return c.nextStarted
	}
	return -1
}

func (c *SpeechTurnCancellation) signalProgressLocked() {
	if c.progressChanged == nil {
		c.progressChanged = make(chan struct{})
		return
	}
	close(c.progressChanged)
	c.progressChanged = make(chan struct{})
}

func (c *SpeechTurnCancellation) takeBindingsLocked() []context.CancelFunc {
	cancels := make([]context.CancelFunc, 0, len(c.bindings))
	for _, cancel := range c.bindings {
		cancels = append(cancels, cancel)
	}
	c.bindings = nil
	return cancels
}
