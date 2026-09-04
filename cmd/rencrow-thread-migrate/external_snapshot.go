package main

import (
	"context"
	"errors"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/redis"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/vectordb"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/threadmigration"
)

var errCaptureExternalOperationFailed = errors.New("external snapshot capture operation failed")
var errVerifyExternalOperationFailed = errors.New("external snapshot verification operation failed")

type captureExternalDependencies struct {
	loadConfig    func(string) (*config.Config, error)
	captureRedis  func(context.Context, string) ([]threadmigration.RedisEntry, error)
	captureQdrant func(context.Context, string, string) ([]threadmigration.QdrantPointSnapshot, error)
	newSnapshot   func([]threadmigration.RedisEntry, []threadmigration.QdrantPointSnapshot) (threadmigration.ExternalSnapshot, error)
	writeSnapshot func(string, threadmigration.ExternalSnapshot) error
	readSnapshot  func(string) (threadmigration.ExternalSnapshot, error)
}

func captureExternalSnapshot(ctx context.Context, configPath, outputPath string) (threadmigration.ExternalSnapshot, error) {
	return captureExternalSnapshotWithDependencies(ctx, configPath, outputPath, captureExternalDependenciesDefaults())
}

func verifyExternalSnapshot(ctx context.Context, inputPath string) (threadmigration.ExternalSnapshot, error) {
	if ctx == nil || ctx.Err() != nil || strings.TrimSpace(inputPath) == "" {
		return threadmigration.ExternalSnapshot{}, errVerifyExternalOperationFailed
	}
	snapshot, err := threadmigration.ReadExternalSnapshotStrict(inputPath)
	if err != nil || ctx.Err() != nil {
		return threadmigration.ExternalSnapshot{}, errVerifyExternalOperationFailed
	}
	return snapshot, nil
}

func captureExternalDependenciesDefaults() captureExternalDependencies {
	return captureExternalDependencies{
		loadConfig:    config.LoadConfig,
		captureRedis:  redisstore.CaptureLegacyThreadEntries,
		captureQdrant: vectordb.CaptureLegacyThreadPoints,
		newSnapshot:   threadmigration.NewExternalSnapshot,
		writeSnapshot: threadmigration.WriteExternalSnapshotFresh,
		readSnapshot:  threadmigration.ReadExternalSnapshotStrict,
	}
}

func captureExternalSnapshotWithDependencies(ctx context.Context, configPath, outputPath string, deps captureExternalDependencies) (threadmigration.ExternalSnapshot, error) {
	if ctx == nil || ctx.Err() != nil || strings.TrimSpace(configPath) == "" || strings.TrimSpace(outputPath) == "" {
		return threadmigration.ExternalSnapshot{}, errCaptureExternalOperationFailed
	}
	if deps.loadConfig == nil {
		return threadmigration.ExternalSnapshot{}, errCaptureExternalOperationFailed
	}

	cfg, err := deps.loadConfig(configPath)
	if err != nil || cfg == nil || ctx.Err() != nil {
		return threadmigration.ExternalSnapshot{}, errCaptureExternalOperationFailed
	}
	conversation := cfg.Conversation
	vectorCollection := conversation.VectorCollection
	if !conversation.Enabled || strings.TrimSpace(conversation.RedisURL) == "" || strings.TrimSpace(conversation.VectorDBURL) == "" || strings.TrimSpace(vectorCollection) == "" {
		return threadmigration.ExternalSnapshot{}, errCaptureExternalOperationFailed
	}
	if deps.captureRedis == nil || deps.captureQdrant == nil || deps.newSnapshot == nil || deps.writeSnapshot == nil || deps.readSnapshot == nil {
		return threadmigration.ExternalSnapshot{}, errCaptureExternalOperationFailed
	}

	redisEntries, err := deps.captureRedis(ctx, conversation.RedisURL)
	if err != nil || ctx.Err() != nil {
		return threadmigration.ExternalSnapshot{}, errCaptureExternalOperationFailed
	}
	qdrantPoints, err := deps.captureQdrant(ctx, conversation.VectorDBURL, vectorCollection)
	if err != nil || ctx.Err() != nil {
		return threadmigration.ExternalSnapshot{}, errCaptureExternalOperationFailed
	}
	generated, err := deps.newSnapshot(redisEntries, qdrantPoints)
	if err != nil || ctx.Err() != nil {
		return threadmigration.ExternalSnapshot{}, errCaptureExternalOperationFailed
	}
	if err := generated.Validate(); err != nil {
		return threadmigration.ExternalSnapshot{}, errCaptureExternalOperationFailed
	}
	if err := deps.writeSnapshot(outputPath, generated); err != nil || ctx.Err() != nil {
		return threadmigration.ExternalSnapshot{}, errCaptureExternalOperationFailed
	}
	readback, err := deps.readSnapshot(outputPath)
	if err != nil || ctx.Err() != nil {
		return threadmigration.ExternalSnapshot{}, errCaptureExternalOperationFailed
	}
	if err := readback.Validate(); err != nil || !sameExternalSnapshotReceipt(generated, readback) {
		return threadmigration.ExternalSnapshot{}, errCaptureExternalOperationFailed
	}
	return readback, nil
}

func sameExternalSnapshotReceipt(generated, readback threadmigration.ExternalSnapshot) bool {
	return generated.SchemaVersion == readback.SchemaVersion &&
		len(generated.Redis) == len(readback.Redis) &&
		len(generated.Qdrant) == len(readback.Qdrant) &&
		generated.RedisSHA256 == readback.RedisSHA256 &&
		generated.QdrantSHA256 == readback.QdrantSHA256 &&
		generated.SnapshotSHA256 == readback.SnapshotSHA256
}
