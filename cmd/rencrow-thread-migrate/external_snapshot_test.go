package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/threadmigration"
)

func TestCaptureExternalUsesConfiguredOwnerRouteAndReadback(t *testing.T) {
	var calls []string
	var written threadmigration.ExternalSnapshot
	deps := captureExternalDependencies{
		loadConfig: func(path string) (*config.Config, error) {
			calls = append(calls, "load")
			if path != "config.yaml" {
				t.Fatalf("config path = %q", path)
			}
			return &config.Config{Conversation: config.ConversationConfig{
				Enabled:          true,
				RedisURL:         "redis://configured:6379",
				VectorDBURL:      "configured:6334",
				VectorCollection: "configured_collection",
			}}, nil
		},
		captureRedis: func(ctx context.Context, redisURL string) ([]threadmigration.RedisEntry, error) {
			calls = append(calls, "redis")
			if ctx == nil || ctx.Err() != nil || redisURL != "redis://configured:6379" {
				t.Fatalf("redis call = ctx %v, url %q", ctx, redisURL)
			}
			return nil, nil
		},
		captureQdrant: func(ctx context.Context, qdrantURL, collection string) ([]threadmigration.QdrantPointSnapshot, error) {
			calls = append(calls, "qdrant")
			if ctx == nil || ctx.Err() != nil || qdrantURL != "configured:6334" || collection != "configured_collection" {
				t.Fatalf("qdrant call = ctx %v, url %q, collection %q", ctx, qdrantURL, collection)
			}
			return nil, nil
		},
		newSnapshot: func(redis []threadmigration.RedisEntry, qdrant []threadmigration.QdrantPointSnapshot) (threadmigration.ExternalSnapshot, error) {
			calls = append(calls, "new")
			return threadmigration.NewExternalSnapshot(redis, qdrant)
		},
		writeSnapshot: func(path string, snapshot threadmigration.ExternalSnapshot) error {
			calls = append(calls, "write")
			if path != "snapshot.json" {
				t.Fatalf("output path = %q", path)
			}
			written = snapshot
			return nil
		},
		readSnapshot: func(path string) (threadmigration.ExternalSnapshot, error) {
			calls = append(calls, "read")
			if path != "snapshot.json" {
				t.Fatalf("readback path = %q", path)
			}
			return written, nil
		},
	}

	got, err := captureExternalSnapshotWithDependencies(context.Background(), "config.yaml", "snapshot.json", deps)
	if err != nil {
		t.Fatalf("captureExternalSnapshotWithDependencies() error = %v", err)
	}
	if got.RedisSHA256 != written.RedisSHA256 || got.QdrantSHA256 != written.QdrantSHA256 || got.SnapshotSHA256 != written.SnapshotSHA256 || len(got.Redis) != len(written.Redis) || len(got.Qdrant) != len(written.Qdrant) {
		t.Fatalf("readback snapshot = %#v, written = %#v", got, written)
	}
	if gotCalls := strings.Join(calls, ","); gotCalls != "load,redis,qdrant,new,write,read" {
		t.Fatalf("owner route calls = %q", gotCalls)
	}
}

func TestCaptureExternalDisabledConfigDoesNotCallDefaultBackends(t *testing.T) {
	backendCalls := 0
	deps := captureExternalDependencies{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Conversation: config.ConversationConfig{
				Enabled:          false,
				RedisURL:         "redis://localhost:6379",
				VectorDBURL:      "localhost:6334",
				VectorCollection: "rencrow_memory",
			}}, nil
		},
		captureRedis: func(context.Context, string) ([]threadmigration.RedisEntry, error) {
			backendCalls++
			return nil, nil
		},
		captureQdrant: func(context.Context, string, string) ([]threadmigration.QdrantPointSnapshot, error) {
			backendCalls++
			return nil, nil
		},
	}

	if _, err := captureExternalSnapshotWithDependencies(context.Background(), "config.yaml", "snapshot.json", deps); err == nil {
		t.Fatal("disabled conversation config was accepted")
	}
	if backendCalls != 0 {
		t.Fatalf("disabled config called backends %d times", backendCalls)
	}
}

func TestCaptureExternalRejectsBlankVectorCollectionWithoutBackendCalls(t *testing.T) {
	backendCalls := 0
	deps := captureExternalDependencies{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Conversation: config.ConversationConfig{
				Enabled:     true,
				RedisURL:    "redis://configured:6379",
				VectorDBURL: "configured:6334",
			}}, nil
		},
		captureRedis: func(context.Context, string) ([]threadmigration.RedisEntry, error) {
			backendCalls++
			return nil, nil
		},
		captureQdrant: func(context.Context, string, string) ([]threadmigration.QdrantPointSnapshot, error) {
			backendCalls++
			return nil, nil
		},
	}

	if _, err := captureExternalSnapshotWithDependencies(context.Background(), "config.yaml", "snapshot.json", deps); err == nil {
		t.Fatal("blank vector collection was accepted")
	}
	if backendCalls != 0 {
		t.Fatalf("blank vector collection called backends %d times", backendCalls)
	}
}

func TestCaptureExternalRejectsValidReadbackMismatch(t *testing.T) {
	generated, err := threadmigration.NewExternalSnapshot(nil, nil)
	if err != nil {
		t.Fatalf("NewExternalSnapshot(generated) error = %v", err)
	}
	readback, err := threadmigration.NewExternalSnapshot([]threadmigration.RedisEntry{{
		Key:               "sess:session-a",
		Value:             []byte(`{"session_id":"session-a"}`),
		ExpireAtUnixMilli: 1800000000001,
	}}, nil)
	if err != nil {
		t.Fatalf("NewExternalSnapshot(readback) error = %v", err)
	}
	deps := captureExternalDependencies{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Conversation: config.ConversationConfig{
				Enabled:          true,
				RedisURL:         "redis://configured:6379",
				VectorDBURL:      "configured:6334",
				VectorCollection: "configured_collection",
			}}, nil
		},
		captureRedis: func(context.Context, string) ([]threadmigration.RedisEntry, error) { return nil, nil },
		captureQdrant: func(context.Context, string, string) ([]threadmigration.QdrantPointSnapshot, error) {
			return nil, nil
		},
		newSnapshot: func([]threadmigration.RedisEntry, []threadmigration.QdrantPointSnapshot) (threadmigration.ExternalSnapshot, error) {
			return generated, nil
		},
		writeSnapshot: func(string, threadmigration.ExternalSnapshot) error { return nil },
		readSnapshot:  func(string) (threadmigration.ExternalSnapshot, error) { return readback, nil },
	}

	if _, err := captureExternalSnapshotWithDependencies(context.Background(), "config.yaml", "snapshot.json", deps); err == nil {
		t.Fatal("valid but different readback snapshot was accepted")
	}
}

func TestCaptureExternalStopsAtFirstOwnerStageFailure(t *testing.T) {
	stages := []string{"load", "redis", "qdrant", "new", "write", "read"}
	for stageIndex, stageName := range stages {
		stageIndex, stageName := stageIndex, stageName
		t.Run(stageName, func(t *testing.T) {
			calls := make([]string, 0, len(stages))
			stored := threadmigration.ExternalSnapshot{}
			deps := captureExternalDependencies{
				loadConfig: func(string) (*config.Config, error) {
					calls = append(calls, "load")
					if stageName == "load" {
						return nil, errors.New("load failure")
					}
					return &config.Config{Conversation: config.ConversationConfig{
						Enabled:          true,
						RedisURL:         "redis://configured:6379",
						VectorDBURL:      "configured:6334",
						VectorCollection: "configured_collection",
					}}, nil
				},
				captureRedis: func(context.Context, string) ([]threadmigration.RedisEntry, error) {
					calls = append(calls, "redis")
					if stageName == "redis" {
						return nil, errors.New("redis failure")
					}
					return nil, nil
				},
				captureQdrant: func(context.Context, string, string) ([]threadmigration.QdrantPointSnapshot, error) {
					calls = append(calls, "qdrant")
					if stageName == "qdrant" {
						return nil, errors.New("qdrant failure")
					}
					return nil, nil
				},
				newSnapshot: func([]threadmigration.RedisEntry, []threadmigration.QdrantPointSnapshot) (threadmigration.ExternalSnapshot, error) {
					calls = append(calls, "new")
					if stageName == "new" {
						return threadmigration.ExternalSnapshot{}, errors.New("snapshot failure")
					}
					return threadmigration.NewExternalSnapshot(nil, nil)
				},
				writeSnapshot: func(_ string, snapshot threadmigration.ExternalSnapshot) error {
					calls = append(calls, "write")
					if stageName == "write" {
						return errors.New("write failure")
					}
					stored = snapshot
					return nil
				},
				readSnapshot: func(string) (threadmigration.ExternalSnapshot, error) {
					calls = append(calls, "read")
					if stageName == "read" {
						return threadmigration.ExternalSnapshot{}, errors.New("read failure")
					}
					return stored, nil
				},
			}

			if _, err := captureExternalSnapshotWithDependencies(context.Background(), "config.yaml", "snapshot.json", deps); err == nil {
				t.Fatalf("stage %q failure was accepted", stageName)
			}
			wantCalls := strings.Join(stages[:stageIndex+1], ",")
			if gotCalls := strings.Join(calls, ","); gotCalls != wantCalls {
				t.Fatalf("owner route calls = %q, want %q", gotCalls, wantCalls)
			}
		})
	}
}

func TestCaptureExternalRejectsNilOrCanceledContext(t *testing.T) {
	loadCalls := 0
	deps := captureExternalDependencies{
		loadConfig: func(string) (*config.Config, error) {
			loadCalls++
			return nil, nil
		},
	}
	if _, err := captureExternalSnapshotWithDependencies(nil, "config.yaml", "snapshot.json", deps); err == nil {
		t.Fatal("nil context was accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := captureExternalSnapshotWithDependencies(ctx, "config.yaml", "snapshot.json", deps); err == nil {
		t.Fatal("canceled context was accepted")
	}
	if loadCalls != 0 {
		t.Fatalf("loadConfig calls = %d, want 0 for invalid context", loadCalls)
	}
}
