package orchestrator

import "context"

type streamForwarder interface {
	Finalize(ctx context.Context, finalText string)
}

type streamBundle struct {
	tts    *ttsStreamForwarder
	vtuber *vtuberStreamForwarder
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
