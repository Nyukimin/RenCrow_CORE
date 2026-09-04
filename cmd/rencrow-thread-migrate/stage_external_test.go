package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	redisstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/redis"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/vectordb"
)

func TestRunStageExternalWritesOneValidatedReceipt(t *testing.T) {
	want := validStageExternalReceipt(t)
	var got stageExternalOptions
	var stdout bytes.Buffer
	code := runStageExternal([]string{
		"--config", "core.yaml", "--build-dir", "build", "--stage-dir", "stage",
		"--redis-target-db", "1", "--qdrant-target-collection", "fresh_collection",
	}, &stdout, func(_ context.Context, options stageExternalOptions) (stageExternalReceipt, error) {
		got = options
		return want, nil
	})
	if code != 0 {
		t.Fatalf("runStageExternal() code = %d, output = %q", code, stdout.String())
	}
	if got.ConfigPath != "core.yaml" || got.BuildDir != "build" || got.StageDir != "stage" || got.RedisTargetDB != 1 || got.TargetCollection != "fresh_collection" {
		t.Fatalf("stage options = %+v", got)
	}
	wantJSON, _ := json.Marshal(want)
	if stdout.String() != string(append(wantJSON, '\n')) {
		t.Fatalf("stdout = %q, want canonical receipt", stdout.String())
	}
}

func TestRunStageExternalRejectsInvalidArgumentsBeforeOperation(t *testing.T) {
	called := false
	var stdout bytes.Buffer
	code := runStageExternal([]string{"--redis-target-db", "1"}, &stdout, func(context.Context, stageExternalOptions) (stageExternalReceipt, error) {
		called = true
		return stageExternalReceipt{}, nil
	})
	if code == 0 || called {
		t.Fatalf("invalid arguments result: code=%d called=%v output=%q", code, called, stdout.String())
	}
	var receipt stageExternalReceipt
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &receipt); err != nil || receipt.validate() != nil || receipt.Status != stageExternalStatusBlocked {
		t.Fatalf("blocked receipt = %+v, decode=%v validate=%v", receipt, err, receipt.validate())
	}
}

func TestStageExternalReceiptRejectsCrossChildMismatch(t *testing.T) {
	receipt := validStageExternalReceipt(t)
	config := *receipt.ConfigCandidate
	config.TargetCollectionSHA256 = strings.Repeat("f", 64)
	config, err := sealCanonicalThreadConfigCandidate(config)
	if err != nil {
		t.Fatal(err)
	}
	receipt.ConfigCandidate = &config
	receipt = sealStageExternalReceipt(receipt, stageExternalStatusStaged, "")
	if err := receipt.validate(); err == nil {
		t.Fatal("cross-child collection mismatch was accepted")
	}
}

func TestStageExternalBuildReadFailureWritesPathFreeBlockedReceipt(t *testing.T) {
	directory := canonicalThreadConfigTestDir(t)
	receipt, err := stageExternal(context.Background(), stageExternalOptions{
		ConfigPath: "secret-config-path", BuildDir: "missing-secret-build",
		StageDir: directory, RedisTargetDB: 1, TargetCollection: "fresh_collection",
	})
	if err == nil || receipt.Status != stageExternalStatusBlocked || receipt.ErrorCode != "build_read" || receipt.validate() != nil {
		t.Fatalf("stageExternal() = %+v, %v", receipt, err)
	}
	data, readErr := os.ReadFile(filepath.Join(directory, stageExternalReceiptFilename))
	if readErr != nil {
		t.Fatal(readErr)
	}
	want, _ := json.Marshal(receipt)
	if !bytes.Equal(data, append(want, '\n')) || bytes.Contains(data, []byte("secret")) || bytes.Contains(data, []byte(directory)) {
		t.Fatalf("blocked stage receipt is invalid or leaked data: %q", data)
	}
	entries, _ := os.ReadDir(directory)
	if len(entries) != 1 || entries[0].Name() != stageExternalReceiptFilename {
		t.Fatalf("blocked stage output set = %#v", entries)
	}
}

func validStageExternalReceipt(t *testing.T) stageExternalReceipt {
	t.Helper()
	mappingHash := strings.Repeat("a", 64)
	collectionHash := canonicalThreadConfigSHA256([]byte("fresh_collection"))
	config, err := sealCanonicalThreadConfigCandidate(canonicalThreadConfigCandidateReceipt{
		SchemaVersion: canonicalThreadConfigCandidateSchema, Status: canonicalThreadConfigCandidateReady,
		SourceConfigSHA256: strings.Repeat("b", 64), OutputConfigSHA256: strings.Repeat("c", 64),
		SourceRedisDB: 0, TargetRedisDB: 1, TargetCollectionSHA256: collectionHash,
		OnlyCanonicalRouteFieldsChanged: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	qdrantReceipt := vectordb.CanonicalThreadStageReceipt{
		SchemaVersion:        vectordb.CanonicalThreadStageReceiptSchemaVersion,
		Status:               vectordb.CanonicalThreadStageStatusStagedNotActive,
		TargetCollectionHash: collectionHash, PreparedCount: 1, StagedCount: 1, VectorDimension: 1,
		PreparedOutputSHA256: strings.Repeat("d", 64), MappingSHA256: mappingHash, ReadbackOutputSHA256: strings.Repeat("d", 64),
	}
	qdrantReceipt.ReceiptSHA256, err = qdrantReceipt.ComputeSHA256()
	if err != nil || qdrantReceipt.Validate() != nil {
		t.Fatalf("Qdrant test receipt = %+v, %v", qdrantReceipt, err)
	}
	redisReceipt := redisstore.CanonicalThreadStageReceipt{
		SchemaVersion: redisstore.CanonicalThreadStageReceiptSchemaVersion,
		Status:        redisstore.CanonicalThreadStageStatusStaged,
		SourceDB:      0, TargetDB: 1, PreparedCount: 1, StagedCount: 1,
		EvaluatedAtUnixMilli: 1800000000000,
		PreparedOutputSHA256: strings.Repeat("e", 64), StagedOutputSHA256: strings.Repeat("f", 64), MappingSHA256: mappingHash,
		ExactAbsoluteExpiry: true,
	}
	redisReceipt.ReceiptSHA256, err = redisReceipt.ComputeSHA256()
	if err != nil || redisReceipt.Validate() != nil {
		t.Fatalf("Redis test receipt = %+v, %v", redisReceipt, err)
	}
	receipt := stageExternalReceipt{
		SchemaVersion: stageExternalSchemaVersion, Status: stageExternalStatusStaged,
		BuildReceiptSHA256: strings.Repeat("9", 64), MappingSHA256: mappingHash,
		ConfigCandidate: &config, QdrantStage: &qdrantReceipt, RedisStage: &redisReceipt,
	}
	receipt = sealStageExternalReceipt(receipt, stageExternalStatusStaged, "")
	if err := receipt.validate(); err != nil {
		t.Fatal(err)
	}
	return receipt
}
