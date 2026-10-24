package orchestrator

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

type streamAborter interface {
	Abort()
}

type streamErrorReporter interface {
	Err() error
}

func requireStreamAborter(t *testing.T, value any) streamAborter {
	t.Helper()
	aborter, ok := value.(streamAborter)
	if !ok {
		t.Fatal("stream forwarder does not expose Abort")
	}
	return aborter
}

func TestTTSStreamAbortDiscardsBufferedTextAndPreventsFinalize(t *testing.T) {
	bridge := &abortBlockingTTSBridge{started: make(chan struct{}, 1)}
	forwarder := newTTSStreamForwarder(bridge, "s1", routing.RouteCHAT, "mio", "agent.response", "tts:", context.Background())
	if forwarder == nil {
		t.Fatal("expected TTS forwarder")
	}
	aborter := requireStreamAborter(t, forwarder)

	forwarder.OnToken(context.Background(), "buffered unpublished text")
	aborter.Abort()
	forwarder.Finalize(context.Background(), "late final answer.")

	if got := bridge.snapshot(); len(got) != 0 {
		t.Fatalf("aborted stream published text: %#v", got)
	}
}

func TestTTSStreamAbortCancelsInflightAndDiscardsQueuedChunks(t *testing.T) {
	bridge := &abortBlockingTTSBridge{started: make(chan struct{}, 1)}
	forwarder := newTTSStreamForwarder(bridge, "s1", routing.RouteCHAT, "mio", "agent.response", "tts:", context.Background())
	if forwarder == nil {
		t.Fatal("expected TTS forwarder")
	}
	aborter := requireStreamAborter(t, forwarder)

	forwarder.OnToken(context.Background(), "first published sentence.")
	awaitSignal(t, bridge.started, "first TTS push")
	forwarder.OnToken(context.Background(), strings.Repeat("queued sentence. ", 5))

	aborter.Abort()
	forwarder.Finalize(context.Background(), "late final answer.")

	if got := bridge.snapshot(); len(got) != 1 {
		t.Fatalf("expected only the in-flight push before abort, got %#v", got)
	}
	if !bridge.canceled() {
		t.Fatal("expected Abort to cancel the in-flight TTS push")
	}
}

func TestTTSStreamQueueSaturationReturnsTypedErrorWithoutBlocking(t *testing.T) {
	bridge := &abortBlockingTTSBridge{started: make(chan struct{}, 1)}
	forwarder := newTTSStreamForwarder(bridge, "s1", routing.RouteCHAT, "mio", "agent.response", "tts:", context.Background())
	if forwarder == nil {
		t.Fatal("expected TTS forwarder")
	}
	if _, ok := any(forwarder).(streamAborter); !ok {
		t.Fatal("stream forwarder does not expose Abort")
	}
	if _, ok := any(forwarder).(streamErrorReporter); !ok {
		t.Fatal("TTS forwarder does not expose its typed admission error")
	}
	aborter := any(forwarder).(streamAborter)
	defer aborter.Abort()

	forwarder.OnToken(context.Background(), "first published sentence.")
	awaitSignal(t, bridge.started, "first TTS push")
	admissionDone := make(chan struct{})
	go func() {
		forwarder.OnToken(context.Background(), strings.Repeat("queued sentence. ", 128))
		close(admissionDone)
	}()
	awaitSignal(t, admissionDone, "bounded TTS queue admission")

	reporter := any(forwarder).(streamErrorReporter)
	var capacityError interface {
		error
		QueueCapacity() int
	}
	if err := reporter.Err(); !errors.As(err, &capacityError) {
		t.Fatalf("expected typed queue capacity error, got %v", err)
	} else if capacityError.QueueCapacity() != 32 {
		t.Fatalf("queue capacity error reports %d, want 32", capacityError.QueueCapacity())
	}

	aborter.Abort()
	if got := bridge.snapshot(); len(got) != 1 {
		t.Fatalf("saturation should abort the remaining chunks, got %#v", got)
	}
}

func TestVTuberStreamAbortDiscardsBufferedTextAndCancelsInflightPush(t *testing.T) {
	bridge := &abortBlockingVTuberBridge{started: make(chan struct{}, 1)}
	forwarder := newVTuberStreamForwarder(bridge, "s1", routing.RouteCHAT, "mio", "agent.response", "vtuber:")
	if forwarder == nil {
		t.Fatal("expected VTuber forwarder")
	}
	aborter := requireStreamAborter(t, forwarder)
	forwarder.OnToken(context.Background(), "buffered unpublished text")
	aborter.Abort()
	forwarder.Finalize(context.Background(), "late final answer.")
	if got := bridge.snapshot(); len(got) != 0 {
		t.Fatalf("aborted stream published buffered text: %#v", got)
	}

	bridge = &abortBlockingVTuberBridge{started: make(chan struct{}, 1)}
	forwarder = newVTuberStreamForwarder(bridge, "s1", routing.RouteCHAT, "mio", "agent.response", "vtuber:")
	pushDone := make(chan struct{})
	go func() {
		defer close(pushDone)
		forwarder.OnToken(context.Background(), "first published sentence.")
	}()
	awaitSignal(t, bridge.started, "first VTuber push")
	requireStreamAborter(t, forwarder).Abort()
	awaitSignal(t, pushDone, "canceled VTuber push return")
	if !bridge.canceled() {
		t.Fatal("expected Abort to cancel the in-flight VTuber push")
	}
}

func TestStreamBundleAbortForwardsAndIsIdempotent(t *testing.T) {
	ttsBridge := &abortBlockingTTSBridge{started: make(chan struct{}, 1)}
	vtuberBridge := &recordingAbortVTuberBridge{}
	bundle := &streamBundle{
		tts:    newTTSStreamForwarder(ttsBridge, "s1", routing.RouteCHAT, "mio", "agent.response", "tts:", context.Background()),
		vtuber: newVTuberStreamForwarder(vtuberBridge, "s1", routing.RouteCHAT, "mio", "agent.response", "vtuber:"),
	}
	if bundle.tts == nil || bundle.vtuber == nil {
		t.Fatal("expected both stream forwarders")
	}
	bundleAborter := requireStreamAborter(t, bundle)
	bundle.tts.OnToken(context.Background(), "buffered unpublished TTS")
	bundle.vtuber.OnToken(context.Background(), "buffered unpublished VTuber")

	bundleAborter.Abort()
	bundleAborter.Abort()
	bundle.Finalize(context.Background(), "late final answer.")

	if got := ttsBridge.snapshot(); len(got) != 0 {
		t.Fatalf("bundle abort published TTS text: %#v", got)
	}
	if got := vtuberBridge.snapshot(); len(got) != 0 {
		t.Fatalf("bundle abort published VTuber text: %#v", got)
	}
}

func TestStreamBundleErrReportsTTSAdmissionFailure(t *testing.T) {
	bridge := &abortBlockingTTSBridge{started: make(chan struct{}, 1)}
	bundle := &streamBundle{tts: newTTSStreamForwarder(bridge, "s1", routing.RouteCHAT, "mio", "agent.response", "tts:", context.Background())}
	if _, ok := any(bundle).(streamErrorReporter); !ok {
		t.Fatal("stream bundle does not expose the TTS forwarder's typed error")
	}
	if bundle.tts == nil {
		t.Fatal("expected TTS forwarder")
	}
	defer requireStreamAborter(t, bundle).Abort()
	bundle.tts.OnToken(context.Background(), "first published sentence.")
	awaitSignal(t, bridge.started, "first TTS push")
	admissionDone := make(chan struct{})
	go func() {
		bundle.tts.OnToken(context.Background(), strings.Repeat("queued sentence. ", 128))
		close(admissionDone)
	}()
	awaitSignal(t, admissionDone, "bounded bundle TTS queue admission")

	var capacityError interface {
		error
		QueueCapacity() int
	}
	if err := any(bundle).(streamErrorReporter).Err(); !errors.As(err, &capacityError) {
		t.Fatalf("expected bundle to expose queue capacity error, got %v", err)
	}
	requireStreamAborter(t, bundle).Abort()
}

func TestVTuberStreamFinalizeStillPublishesFinalText(t *testing.T) {
	bridge := &recordingAbortVTuberBridge{}
	forwarder := newVTuberStreamForwarder(bridge, "s1", routing.RouteCHAT, "mio", "agent.response", "vtuber:")
	if forwarder == nil {
		t.Fatal("expected VTuber forwarder")
	}
	forwarder.Finalize(context.Background(), "successful final answer.")
	if got := bridge.snapshot(); len(got) != 1 || got[0] != "successful final answer." {
		t.Fatalf("Finalize should publish its final text, got %#v", got)
	}
}

func TestStreamBundleConcurrentTokenAbortFinalizeDoesNotPanic(t *testing.T) {
	bundle := &streamBundle{
		tts:    newTTSStreamForwarder(&recordingAbortTTSBridge{}, "s1", routing.RouteCHAT, "mio", "agent.response", "tts:", context.Background()),
		vtuber: newVTuberStreamForwarder(&recordingAbortVTuberBridge{}, "s1", routing.RouteCHAT, "mio", "agent.response", "vtuber:"),
	}
	if bundle.tts == nil || bundle.vtuber == nil {
		t.Fatal("expected both stream forwarders")
	}
	if _, ok := any(bundle).(streamAborter); !ok {
		t.Fatal("stream bundle does not expose Abort")
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 100; j++ {
				bundle.tts.OnToken(context.Background(), "concurrent sentence.")
				bundle.vtuber.OnToken(context.Background(), "concurrent sentence.")
			}
		}()
	}
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		bundle.Finalize(context.Background(), "concurrent final answer.")
	}()
	go func() {
		defer wg.Done()
		<-start
		any(bundle).(streamAborter).Abort()
	}()
	close(start)
	wg.Wait()
	any(bundle).(streamAborter).Abort()
	bundle.Finalize(context.Background(), "late final answer.")
}

type abortBlockingTTSBridge struct {
	mu          sync.Mutex
	texts       []string
	started     chan struct{}
	first       sync.Once
	wasCanceled bool
}

func (b *abortBlockingTTSBridge) StartSession(context.Context, TTSSessionStart) error {
	return nil
}

func (b *abortBlockingTTSBridge) PushText(ctx context.Context, _ string, text string, _ *moduletts.EmotionState) error {
	b.mu.Lock()
	b.texts = append(b.texts, text)
	first := len(b.texts) == 1
	b.mu.Unlock()
	if first {
		b.first.Do(func() { close(b.started) })
		<-ctx.Done()
		b.mu.Lock()
		b.wasCanceled = true
		b.mu.Unlock()
		return ctx.Err()
	}
	return nil
}

func (b *abortBlockingTTSBridge) EndSession(context.Context, string) error { return nil }

func (b *abortBlockingTTSBridge) snapshot() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.texts...)
}

func (b *abortBlockingTTSBridge) canceled() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.wasCanceled
}

type abortBlockingVTuberBridge struct {
	mu          sync.Mutex
	texts       []string
	started     chan struct{}
	first       sync.Once
	wasCanceled bool
}

func (b *abortBlockingVTuberBridge) PublishEmotion(ctx context.Context, req VTuberEmotionRequest) error {
	b.mu.Lock()
	b.texts = append(b.texts, req.Text)
	b.mu.Unlock()
	b.first.Do(func() { close(b.started) })
	<-ctx.Done()
	b.mu.Lock()
	b.wasCanceled = true
	b.mu.Unlock()
	return ctx.Err()
}

func (b *abortBlockingVTuberBridge) snapshot() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.texts...)
}

func (b *abortBlockingVTuberBridge) canceled() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.wasCanceled
}

type recordingAbortTTSBridge struct{}

func (b *recordingAbortTTSBridge) StartSession(context.Context, TTSSessionStart) error { return nil }

func (b *recordingAbortTTSBridge) PushText(context.Context, string, string, *moduletts.EmotionState) error {
	return nil
}

func (b *recordingAbortTTSBridge) EndSession(context.Context, string) error { return nil }

type recordingAbortVTuberBridge struct {
	mu    sync.Mutex
	texts []string
}

func (b *recordingAbortVTuberBridge) PublishEmotion(_ context.Context, req VTuberEmotionRequest) error {
	b.mu.Lock()
	b.texts = append(b.texts, req.Text)
	b.mu.Unlock()
	return nil
}

func (b *recordingAbortVTuberBridge) snapshot() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.texts...)
}

func awaitSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}
