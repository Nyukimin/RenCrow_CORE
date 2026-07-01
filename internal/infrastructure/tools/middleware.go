package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/tool"
)

// withTimeout はツール関数にタイムアウトを適用する
func withTimeout(fn ToolFunc, timeout time.Duration) ToolFunc {
	return func(ctx context.Context, args map[string]interface{}) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return fn(ctx, args)
	}
}

// withPathValidation はパス引数のバリデーションを適用する
// パストラバーサルと制御文字を検出する
func withPathValidation(fn ToolFunc, argName string) ToolFunc {
	return func(ctx context.Context, args map[string]interface{}) (string, error) {
		path, ok := args[argName].(string)
		if !ok {
			return "", fmt.Errorf("'%s' argument is required and must be a string", argName)
		}
		if err := tool.ValidateNotEmpty(path, argName); err != nil {
			return "", err
		}
		if err := tool.ValidatePath(path); err != nil {
			return "", err
		}
		if err := tool.ValidateNoControlChars(path); err != nil {
			return "", err
		}
		return fn(ctx, args)
	}
}

// RetryConfig はリトライの設定
type RetryConfig struct {
	MaxAttempts int           // 最大試行回数（初回含む）
	BaseDelay   time.Duration // 初回リトライまでの待ち時間
}

// DefaultRetryConfig はデフォルトのリトライ設定（TOOL_CONTRACT §3.3: 3回、指数バックオフ）
var DefaultRetryConfig = RetryConfig{
	MaxAttempts: 3,
	BaseDelay:   1 * time.Second,
}

// withRetry はツール関数にリトライを適用する（指数バックオフ）
// リトライ対象: context がキャンセルされていないエラーのみ
// バリデーションエラー（*ToolError）はリトライしない
func withRetry(fn ToolFunc, cfg RetryConfig) ToolFunc {
	return func(ctx context.Context, args map[string]interface{}) (string, error) {
		var lastErr error
		for attempt := 0; attempt < cfg.MaxAttempts; attempt++ {
			result, err := fn(ctx, args)
			if err == nil {
				return result, nil
			}

			// バリデーションエラーはリトライしない
			if _, ok := err.(*tool.ToolError); ok {
				return "", err
			}

			// context キャンセル済みならリトライしない
			if ctx.Err() != nil {
				return "", err
			}

			lastErr = err

			// 最後の試行ならバックオフ不要
			if attempt < cfg.MaxAttempts-1 {
				delay := cfg.BaseDelay * (1 << uint(attempt)) // 指数バックオフ: 1s, 2s, 4s...
				select {
				case <-time.After(delay):
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}
		}
		return "", lastErr
	}
}

// withStringValidation は文字列引数のバリデーションを適用する
// 空チェックと長さ制限を適用する
func withStringValidation(fn ToolFunc, argName string, maxLen int) ToolFunc {
	return func(ctx context.Context, args map[string]interface{}) (string, error) {
		s, ok := args[argName].(string)
		if !ok {
			return "", fmt.Errorf("'%s' argument is required and must be a string", argName)
		}
		if err := tool.ValidateNotEmpty(s, argName); err != nil {
			return "", err
		}
		if err := tool.ValidateLength(s, maxLen); err != nil {
			return "", err
		}
		if err := tool.ValidateNoControlChars(s); err != nil {
			return "", err
		}
		return fn(ctx, args)
	}
}
