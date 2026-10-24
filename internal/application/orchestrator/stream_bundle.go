package orchestrator

import "context"

type streamForwarder interface {
	Finalize(ctx context.Context, finalText string)
}

type streamBundle struct {
	tts    *ttsStreamForwarder
	vtuber *vtuberStreamForwarder
	err    error
}

func (b *streamBundle) Finalize(ctx context.Context, finalText string) {
	if b == nil {
		return
	}
	if b.tts != nil {
		b.tts.Finalize(ctx, finalText)
	}
	if b.vtuber != nil {
		b.vtuber.Finalize(ctx, finalText)
	}
}

func (b *streamBundle) Abort() {
	if b == nil {
		return
	}
	// VTuber is synchronous and should receive its cancellation before the TTS
	// forwarder waits for an in-flight bridge call to release.
	if b.vtuber != nil {
		b.vtuber.Abort()
	}
	if b.tts != nil {
		b.tts.Abort()
	}
}

func (b *streamBundle) Err() error {
	if b == nil {
		return nil
	}
	if b.tts != nil && b.tts.Err() != nil {
		return b.tts.Err()
	}
	return b.err
}
