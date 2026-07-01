package conversation

import "context"

// ConversationSummarizer は会話スレッドを要約・キーワード抽出する
type ConversationSummarizer interface {
	Summarize(ctx context.Context, thread *Thread) (string, error)
	ExtractKeywords(ctx context.Context, thread *Thread) ([]string, error)
}
