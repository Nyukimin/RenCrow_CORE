package conversation

import "context"

// EmbeddingProvider はテキストをベクトル表現に変換する
type EmbeddingProvider interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}
