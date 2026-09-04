package redisstore

import (
	"context"
	"errors"
	"reflect"
	"sort"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/threadmigration"
	"github.com/redis/go-redis/v9"
)

const (
	legacyRedisCaptureScanCount int64 = 256
	legacyRedisSessionPattern         = "sess:*"
	legacyRedisThreadPattern          = "thread:*"
	// Redis []byte values are base64 encoded in the JSON artifact. Keep the
	// raw capture surface to a conservative half-size budget before the single
	// snapshot validator performs the exact serialized-size check.
	legacyRedisCaptureRawBytesLimit int64 = threadmigration.ExternalSnapshotMaxFileBytes / 2
)

var (
	errLegacyRedisCaptureInvalid = errors.New("legacy Redis capture is invalid")
	errLegacyRedisCaptureSource  = errors.New("legacy Redis capture source failed")
	errLegacyRedisCaptureLimit   = errors.New("legacy Redis capture exceeds bounds")
	errLegacyRedisCaptureClient  = errors.New("legacy Redis capture client is unavailable")
)

// legacyRedisCaptureSource is the read-only boundary used by the capture
// coordinator. Read returns the value and the absolute Unix millisecond
// expiry observed for one key in one transaction.
type legacyRedisCaptureSource interface {
	Scan(ctx context.Context, cursor uint64, match string, count int64) ([]string, uint64, error)
	Read(ctx context.Context, key string) ([]byte, int64, error)
}

// CaptureLegacyThreadEntries captures the legacy sess:* and thread:* logical
// surfaces through the Redis owner. It does not claim writer quiescence; its
// caller/service boundary must prove that writers were stopped for a
// production-consistent snapshot.
func CaptureLegacyThreadEntries(ctx context.Context, redisURL string) (entries []threadmigration.RedisEntry, err error) {
	if err := legacyRedisCaptureContextError(ctx); err != nil {
		return nil, err
	}
	options, parseErr := redis.ParseURL(redisURL)
	if parseErr != nil {
		return nil, errLegacyRedisCaptureClient
	}
	options.MaxRetries = 0
	client := redis.NewClient(options)
	defer func() {
		if closeErr := client.Close(); closeErr != nil {
			entries = nil
			err = errLegacyRedisCaptureClient
		}
	}()
	if pingErr := client.Ping(ctx).Err(); pingErr != nil {
		return nil, errLegacyRedisCaptureClient
	}
	return captureLegacyThreadEntries(ctx, &legacyRedisCaptureClient{client: client})
}

// captureLegacyThreadEntries coordinates only bounded SCAN and read calls.
// No partial result is returned when any page, read, expiry, or validation
// boundary fails.
func captureLegacyThreadEntries(ctx context.Context, source legacyRedisCaptureSource) ([]threadmigration.RedisEntry, error) {
	if err := legacyRedisCaptureContextError(ctx); err != nil {
		return nil, err
	}
	if legacyRedisCaptureSourceIsNil(source) {
		return nil, errLegacyRedisCaptureSource
	}

	keys, err := scanLegacyRedisKeys(ctx, source)
	if err != nil {
		return nil, err
	}
	entries := make([]threadmigration.RedisEntry, 0, len(keys))
	var capturedBytes int64
	for _, key := range keys {
		if err := legacyRedisCaptureContextError(ctx); err != nil {
			return nil, err
		}
		value, expiry, readErr := source.Read(ctx, key)
		if readErr != nil {
			return nil, errLegacyRedisCaptureSource
		}
		if err := legacyRedisCaptureContextError(ctx); err != nil {
			return nil, err
		}
		if expiry <= 0 {
			return nil, errLegacyRedisCaptureInvalid
		}
		var ok bool
		capturedBytes, ok = addLegacyRedisCaptureBytes(capturedBytes, key, value)
		if !ok {
			return nil, errLegacyRedisCaptureLimit
		}
		entries = append(entries, threadmigration.RedisEntry{
			Key:               key,
			Value:             append([]byte(nil), value...),
			ExpireAtUnixMilli: expiry,
		})
	}
	if err := legacyRedisCaptureContextError(ctx); err != nil {
		return nil, err
	}

	// The external snapshot constructor is the single validation boundary for
	// legacy key syntax, UTF-8/JSON structure, duplicate keys, and per-value
	// bounds. Its returned Redis slice is independently owned by the snapshot.
	snapshot, err := threadmigration.NewExternalSnapshot(entries, nil)
	if err != nil {
		return nil, errLegacyRedisCaptureInvalid
	}
	return snapshot.Redis, nil
}

type legacyRedisCaptureClient struct {
	client *redis.Client
}

func (source *legacyRedisCaptureClient) Scan(ctx context.Context, cursor uint64, match string, count int64) ([]string, uint64, error) {
	if source == nil || source.client == nil || ctx == nil {
		return nil, 0, errLegacyRedisCaptureClient
	}
	keys, nextCursor, err := source.client.Scan(ctx, cursor, match, count).Result()
	if err != nil {
		return nil, 0, errLegacyRedisCaptureSource
	}
	return append([]string(nil), keys...), nextCursor, nil
}

func (source *legacyRedisCaptureClient) Read(ctx context.Context, key string) ([]byte, int64, error) {
	if source == nil || source.client == nil || ctx == nil {
		return nil, 0, errLegacyRedisCaptureClient
	}
	var valueCommand *redis.StringCmd
	var expiryCommand *redis.Cmd
	_, err := source.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		valueCommand = pipe.Get(ctx, key)
		expiryCommand = pipe.Do(ctx, "PEXPIRETIME", key)
		return nil
	})
	if err != nil || valueCommand == nil || expiryCommand == nil {
		return nil, 0, errLegacyRedisCaptureSource
	}
	value, err := valueCommand.Bytes()
	if err != nil {
		return nil, 0, errLegacyRedisCaptureSource
	}
	expiry, err := expiryCommand.Int64()
	if err != nil {
		return nil, 0, errLegacyRedisCaptureSource
	}
	if expiry <= 0 {
		return nil, expiry, errLegacyRedisCaptureInvalid
	}
	return append([]byte(nil), value...), expiry, nil
}

func scanLegacyRedisKeys(ctx context.Context, source legacyRedisCaptureSource) ([]string, error) {
	seen := make(map[string]struct{})
	patterns := [...]string{legacyRedisSessionPattern, legacyRedisThreadPattern}
	for _, pattern := range patterns {
		cursor := uint64(0)
		seenCursors := make(map[uint64]struct{})
		for {
			if err := legacyRedisCaptureContextError(ctx); err != nil {
				return nil, err
			}
			page, nextCursor, err := source.Scan(ctx, cursor, pattern, legacyRedisCaptureScanCount)
			if err != nil {
				return nil, errLegacyRedisCaptureSource
			}
			if err := legacyRedisCaptureContextError(ctx); err != nil {
				return nil, err
			}
			for _, key := range page {
				if _, exists := seen[key]; exists {
					continue
				}
				seen[key] = struct{}{}
				if len(seen) > threadmigration.RedisPreparationMaxEntries {
					return nil, errLegacyRedisCaptureLimit
				}
			}
			if nextCursor == 0 {
				break
			}
			if nextCursor == cursor {
				return nil, errLegacyRedisCaptureSource
			}
			if _, exists := seenCursors[nextCursor]; exists {
				return nil, errLegacyRedisCaptureSource
			}
			seenCursors[nextCursor] = struct{}{}
			cursor = nextCursor
		}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys, nil
}

func addLegacyRedisCaptureBytes(current int64, key string, value []byte) (int64, bool) {
	keyBytes := int64(len(key))
	valueBytes := int64(len(value))
	limit := legacyRedisCaptureRawBytesLimit
	if keyBytes < 0 || valueBytes < 0 || current < 0 || current > limit || keyBytes > limit-current {
		return current, false
	}
	current += keyBytes
	if valueBytes > limit-current {
		return current, false
	}
	return current + valueBytes, true
}

func legacyRedisCaptureSourceIsNil(source legacyRedisCaptureSource) bool {
	if source == nil {
		return true
	}
	value := reflect.ValueOf(source)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func legacyRedisCaptureContextError(ctx context.Context) error {
	if ctx == nil {
		return errLegacyRedisCaptureClient
	}
	return ctx.Err()
}
