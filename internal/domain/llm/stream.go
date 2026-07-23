package llm

import "context"

type streamKey struct{}
type generationMetricsKey struct{}

// ContextWithStreamCallback はストリーミングコールバックを context に埋め込む
func ContextWithStreamCallback(ctx context.Context, cb StreamCallback) context.Context {
	return context.WithValue(ctx, streamKey{}, cb)
}

// StreamCallbackFromContext は context からストリーミングコールバックを取得する
func StreamCallbackFromContext(ctx context.Context) StreamCallback {
	cb, _ := ctx.Value(streamKey{}).(StreamCallback)
	return cb
}

func ContextWithGenerationMetricsCallback(ctx context.Context, cb GenerationMetricsCallback) context.Context {
	return context.WithValue(ctx, generationMetricsKey{}, cb)
}

func GenerationMetricsCallbackFromContext(ctx context.Context) GenerationMetricsCallback {
	cb, _ := ctx.Value(generationMetricsKey{}).(GenerationMetricsCallback)
	return cb
}
