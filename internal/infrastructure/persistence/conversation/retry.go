package conversation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"time"
)

// RetryConfig はリトライ設定
type RetryConfig struct {
	MaxAttempts  int
	InitialDelay time.Duration
	MaxDelay     time.Duration
	Multiplier   float64
}

// DefaultRetryConfig はデフォルトのリトライ設定
var DefaultRetryConfig = RetryConfig{
	MaxAttempts:  3,
	InitialDelay: 100 * time.Millisecond,
	MaxDelay:     2 * time.Second,
	Multiplier:   2.0,
}

// RetryableError はリトライ可能なエラーかどうかを判定するインターフェース
type RetryableError interface {
	error
	IsRetryable() bool
}

// isRetryableError はエラーがリトライ可能かを判定
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	// RetryableErrorインターフェースを実装している場合
	if re, ok := err.(RetryableError); ok {
		return re.IsRetryable()
	}

	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if errors.Is(err, sql.ErrNoRows) ||
		errors.Is(err, os.ErrNotExist) ||
		errors.Is(err, os.ErrPermission) {
		return false
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}

	return true
}

// withRetry は指定された操作をリトライ付きで実行
func withRetry(ctx context.Context, config RetryConfig, operation func() error) error {
	var lastErr error
	delay := config.InitialDelay

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		err := operation()
		if err == nil {
			return nil
		}

		lastErr = err

		// リトライ不可能なエラーの場合は即座に返す
		if !isRetryableError(err) {
			return fmt.Errorf("non-retryable error on attempt %d/%d: %w", attempt, config.MaxAttempts, err)
		}

		// 最後の試行の場合はリトライしない
		if attempt == config.MaxAttempts {
			break
		}

		// コンテキストがキャンセルされていないか確認
		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled after %d attempts: %w", attempt, ctx.Err())
		case <-time.After(delay):
			// 次の遅延時間を計算（指数バックオフ）
			delay = time.Duration(float64(delay) * config.Multiplier)
			if delay > config.MaxDelay {
				delay = config.MaxDelay
			}
		}
	}

	return fmt.Errorf("operation failed after %d attempts: %w", config.MaxAttempts, lastErr)
}
