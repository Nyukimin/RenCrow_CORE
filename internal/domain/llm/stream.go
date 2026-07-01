package llm

import "context"

type streamKey struct{}

// ContextWithStreamCallback はストリーミングコールバックを context に埋め込む
func ContextWithStreamCallback(ctx context.Context, cb StreamCallback) context.Context {
	return context.WithValue(ctx, streamKey{}, cb)
}

// StreamCallbackFromContext は context からストリーミングコールバックを取得する
func StreamCallbackFromContext(ctx context.Context) StreamCallback {
	cb, _ := ctx.Value(streamKey{}).(StreamCallback)
	return cb
}
