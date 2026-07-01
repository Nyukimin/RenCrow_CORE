package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/conversation"
	"github.com/redis/go-redis/v9"
)

// RedisStore はRedisを使った会話記憶ストア（短期・中期記憶）
type RedisStore struct {
	client  *redis.Client
	ttl     time.Duration // デフォルト: 24h
	metrics *RedisMetrics
}

// RedisMetrics はRedisキャッシュのメトリクス
type RedisMetrics struct {
	SessionHits   int64
	SessionMisses int64
	ThreadHits    int64
	ThreadMisses  int64
}

// NewRedisStore は新しいRedisStoreを生成
func NewRedisStore(redisURL string) (*RedisStore, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse redis URL: %w", err)
	}

	client := redis.NewClient(opts)

	// 接続確認
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to redis: %w", err)
	}

	return &RedisStore{
		client:  client,
		ttl:     24 * time.Hour, // デフォルト: 24時間
		metrics: &RedisMetrics{},
	}, nil
}

// Close はRedis接続を閉じる
func (r *RedisStore) Close() error {
	return r.client.Close()
}

// SaveSession はセッションをRedisに保存（TTL: 24h）
func (r *RedisStore) SaveSession(ctx context.Context, sess *conversation.SessionConversation) error {
	key := fmt.Sprintf("sess:%s", sess.ID)

	data, err := json.Marshal(sess)
	if err != nil {
		return fmt.Errorf("failed to marshal session: %w", err)
	}

	if err := r.client.Set(ctx, key, data, r.ttl).Err(); err != nil {
		return fmt.Errorf("failed to save session to redis: %w", err)
	}

	return nil
}

// GetSession はセッションをRedisから取得
func (r *RedisStore) GetSession(ctx context.Context, sessionID string) (*conversation.SessionConversation, error) {
	key := fmt.Sprintf("sess:%s", sessionID)

	data, err := r.client.Get(ctx, key).Bytes()
	if err == redis.Nil {
		r.metrics.SessionMisses++
		return nil, conversation.ErrSessionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get session from redis: %w", err)
	}

	r.metrics.SessionHits++

	var sess conversation.SessionConversation
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, fmt.Errorf("failed to unmarshal session: %w", err)
	}

	return &sess, nil
}

// DeleteSession はセッションをRedisから削除
func (r *RedisStore) DeleteSession(ctx context.Context, sessionID string) error {
	key := fmt.Sprintf("sess:%s", sessionID)

	if err := r.client.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("failed to delete session from redis: %w", err)
	}

	return nil
}

// ListActiveSessions はアクティブなセッション一覧を取得
func (r *RedisStore) ListActiveSessions(ctx context.Context) ([]string, error) {
	keys, err := r.client.Keys(ctx, "sess:*").Result()
	if err != nil {
		return nil, fmt.Errorf("failed to list sessions from redis: %w", err)
	}

	// "sess:" プレフィックスを除去
	sessionIDs := make([]string, 0, len(keys))
	for _, key := range keys {
		if len(key) > 5 {
			sessionIDs = append(sessionIDs, key[5:])
		}
	}

	return sessionIDs, nil
}

// SaveThread はThreadをRedisに保存（短期記憶）
func (r *RedisStore) SaveThread(ctx context.Context, thread *conversation.Thread) error {
	key := fmt.Sprintf("thread:%d", thread.ID)

	data, err := json.Marshal(thread)
	if err != nil {
		return fmt.Errorf("failed to marshal thread: %w", err)
	}

	// Thread TTL: 1時間（短期記憶）
	if err := r.client.Set(ctx, key, data, 1*time.Hour).Err(); err != nil {
		return fmt.Errorf("failed to save thread to redis: %w", err)
	}

	return nil
}

// GetThread はThreadをRedisから取得
func (r *RedisStore) GetThread(ctx context.Context, threadID int64) (*conversation.Thread, error) {
	key := fmt.Sprintf("thread:%d", threadID)

	data, err := r.client.Get(ctx, key).Bytes()
	if err == redis.Nil {
		r.metrics.ThreadMisses++
		return nil, conversation.ErrThreadNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get thread from redis: %w", err)
	}

	r.metrics.ThreadHits++

	var thread conversation.Thread
	if err := json.Unmarshal(data, &thread); err != nil {
		return nil, fmt.Errorf("failed to unmarshal thread: %w", err)
	}

	return &thread, nil
}

// DeleteThread はThreadをRedisから削除
func (r *RedisStore) DeleteThread(ctx context.Context, threadID int64) error {
	key := fmt.Sprintf("thread:%d", threadID)

	if err := r.client.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("failed to delete thread from redis: %w", err)
	}

	return nil
}

// GetMetrics はRedisキャッシュのメトリクスを取得
func (r *RedisStore) GetMetrics() RedisMetrics {
	return *r.metrics
}

// GetCacheHitRate はキャッシュヒット率を計算
func (r *RedisStore) GetCacheHitRate() (sessionHitRate, threadHitRate float64) {
	sessionTotal := r.metrics.SessionHits + r.metrics.SessionMisses
	if sessionTotal > 0 {
		sessionHitRate = float64(r.metrics.SessionHits) / float64(sessionTotal) * 100
	}

	threadTotal := r.metrics.ThreadHits + r.metrics.ThreadMisses
	if threadTotal > 0 {
		threadHitRate = float64(r.metrics.ThreadHits) / float64(threadTotal) * 100
	}

	return sessionHitRate, threadHitRate
}

