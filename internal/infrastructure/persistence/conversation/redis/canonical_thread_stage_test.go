package redisstore

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/threadmigration"
	"github.com/redis/go-redis/v9"
)

func TestStageCanonicalThreadEntriesFreshStagesOnlyUnexpiredEntries(t *testing.T) {
	prepared := stageTestPrepared(t, 1799999999999, 1800000000001)
	fake := &canonicalThreadStageFakeClient{dbSizes: []int64{0, 1}}
	var gotOptions *redis.Options
	withCanonicalThreadStageFactory(t, func(options *redis.Options) (canonicalThreadStageClient, error) {
		gotOptions = cloneRedisOptionsForStageTest(options)
		return fake, nil
	})

	receipt, err := StageCanonicalThreadEntriesFresh(context.Background(), "redis://localhost:6379/2", 7, prepared, time.UnixMilli(1800000000000))
	if err != nil {
		t.Fatalf("StageCanonicalThreadEntriesFresh() error = %v", err)
	}
	if err := receipt.Validate(); err != nil {
		t.Fatalf("receipt.Validate() error = %v", err)
	}
	if receipt.Status != CanonicalThreadStageStatusStaged || receipt.SourceDB != 2 || receipt.TargetDB != 7 {
		t.Fatalf("receipt identity = %#v", receipt)
	}
	if receipt.PreparedCount != 2 || receipt.ExpiredCount != 1 || receipt.StagedCount != 1 || !receipt.ExactAbsoluteExpiry {
		t.Fatalf("receipt counts/expiry = %#v", receipt)
	}
	if receipt.EvaluatedAtUnixMilli != 1800000000000 || receipt.StagedOutputSHA256 == "" {
		t.Fatalf("receipt cutoff/staged hash = %#v", receipt)
	}
	if gotOptions == nil || gotOptions.DB != 7 || gotOptions.MaxRetries != 0 {
		t.Fatalf("target options = %#v", gotOptions)
	}
	if fake.pings != 1 || fake.closes != 1 {
		t.Fatalf("ping/close counts = %d/%d", fake.pings, fake.closes)
	}
	if len(fake.writes) != 1 || fake.writes[0].key != prepared.Entries[1].Key {
		t.Fatalf("writes = %#v", fake.writes)
	}
	if !bytes.Equal(fake.values[prepared.Entries[1].Key], prepared.Entries[1].Value) || fake.expiries[prepared.Entries[1].Key] != prepared.Entries[1].ExpireAtUnixMilli {
		t.Fatalf("staged value/expiry mismatch: values=%#v expiries=%#v", fake.values, fake.expiries)
	}
}

func TestStageCanonicalThreadEntriesFreshRequiresEmptyTargetBeforeWrite(t *testing.T) {
	prepared := stageTestPrepared(t, 1800000000001, 1800000000002)
	fake := &canonicalThreadStageFakeClient{dbSizes: []int64{1}}
	withCanonicalThreadStageFactory(t, func(*redis.Options) (canonicalThreadStageClient, error) {
		return fake, nil
	})

	receipt, err := StageCanonicalThreadEntriesFresh(context.Background(), "redis://localhost:6379/2", 7, prepared, time.UnixMilli(1800000000000))
	if err == nil {
		t.Fatal("StageCanonicalThreadEntriesFresh() error = nil, want blocked error")
	}
	if receipt.Status != CanonicalThreadStageStatusBlocked || receipt.ErrorCode != canonicalThreadStageErrorTargetNotFresh {
		t.Fatalf("blocked receipt = %#v", receipt)
	}
	if err := receipt.Validate(); err != nil {
		t.Fatalf("blocked receipt.Validate() error = %v", err)
	}
	if len(fake.writes) != 0 {
		t.Fatalf("writes = %#v, want none", fake.writes)
	}
}

func TestStageCanonicalThreadEntriesFreshLeavesPartialTargetUntouchedOnWriteFailure(t *testing.T) {
	prepared := stageTestPrepared(t, 1800000000001, 1800000000002)
	fake := &canonicalThreadStageFakeClient{
		dbSizes:     []int64{0},
		writeError:  errors.New("injected write failure"),
		failOnWrite: 2,
	}
	withCanonicalThreadStageFactory(t, func(*redis.Options) (canonicalThreadStageClient, error) {
		return fake, nil
	})

	receipt, err := StageCanonicalThreadEntriesFresh(context.Background(), "redis://localhost:6379/2", 7, prepared, time.UnixMilli(1800000000000))
	if err == nil {
		t.Fatal("StageCanonicalThreadEntriesFresh() error = nil, want blocked error")
	}
	if receipt.Status != CanonicalThreadStageStatusBlocked || receipt.ErrorCode != canonicalThreadStageErrorWrite {
		t.Fatalf("blocked receipt = %#v", receipt)
	}
	if receipt.StagedCount != 1 || receipt.ExpiredCount != 0 {
		t.Fatalf("partial receipt counts = %#v", receipt)
	}
	if fake.deletes != 0 || fake.flushes != 0 {
		t.Fatalf("cleanup commands were attempted: deletes=%d flushes=%d", fake.deletes, fake.flushes)
	}
	if err := receipt.Validate(); err != nil {
		t.Fatalf("blocked receipt.Validate() error = %v", err)
	}
}

func TestStageCanonicalThreadEntriesFreshValidatesBeforeCreatingClient(t *testing.T) {
	var factoryCalls int
	withCanonicalThreadStageFactory(t, func(*redis.Options) (canonicalThreadStageClient, error) {
		factoryCalls++
		return nil, nil
	})

	receipt, err := StageCanonicalThreadEntriesFresh(context.Background(), "redis://localhost:6379/2", 7, threadmigration.RedisPreparationResult{}, time.UnixMilli(1800000000000))
	if err == nil {
		t.Fatal("StageCanonicalThreadEntriesFresh() error = nil, want invalid-prepared error")
	}
	if receipt.Status != CanonicalThreadStageStatusBlocked || receipt.ErrorCode != canonicalThreadStageErrorPrepared {
		t.Fatalf("blocked receipt = %#v", receipt)
	}
	if factoryCalls != 0 {
		t.Fatalf("factory calls = %d, want 0", factoryCalls)
	}
	if err := receipt.Validate(); err != nil {
		t.Fatalf("blocked receipt.Validate() error = %v", err)
	}
}

func TestStageCanonicalThreadEntriesFreshRejectsNonPositiveEvaluationTime(t *testing.T) {
	var factoryCalls int
	withCanonicalThreadStageFactory(t, func(*redis.Options) (canonicalThreadStageClient, error) {
		factoryCalls++
		return nil, nil
	})
	prepared := stageTestPrepared(t, 1800000000001, 1800000000002)
	receipt, err := StageCanonicalThreadEntriesFresh(context.Background(), "redis://localhost:6379/2", 7, prepared, time.Unix(0, 0))
	if err == nil {
		t.Fatal("StageCanonicalThreadEntriesFresh() error = nil, want invalid evaluation time")
	}
	if receipt.Status != CanonicalThreadStageStatusBlocked || receipt.ErrorCode != canonicalThreadStageErrorEvaluatedAt {
		t.Fatalf("blocked receipt = %#v", receipt)
	}
	if factoryCalls != 0 {
		t.Fatalf("factory calls = %d, want 0", factoryCalls)
	}
	if err := receipt.Validate(); err != nil {
		t.Fatalf("blocked receipt.Validate() error = %v", err)
	}

	invalid := CanonicalThreadStageReceipt{
		SchemaVersion:        CanonicalThreadStageReceiptSchemaVersion,
		Status:               CanonicalThreadStageStatusStaged,
		SourceDB:             2,
		TargetDB:             7,
		PreparedCount:        1,
		StagedCount:          1,
		PreparedOutputSHA256: strings.Repeat("a", 64),
		StagedOutputSHA256:   strings.Repeat("b", 64),
		MappingSHA256:        strings.Repeat("c", 64),
		ExactAbsoluteExpiry:  true,
	}
	invalid.ReceiptSHA256, err = invalid.ComputeSHA256()
	if err != nil {
		t.Fatalf("invalid receipt hash error = %v", err)
	}
	if err := invalid.Validate(); err == nil {
		t.Fatal("invalid staged receipt.Validate() error = nil for zero evaluation time")
	}
}

func TestCanonicalThreadStageReceiptSelfHashIsCanonical(t *testing.T) {
	receipt := CanonicalThreadStageReceipt{
		SchemaVersion:        CanonicalThreadStageReceiptSchemaVersion,
		Status:               CanonicalThreadStageStatusStaged,
		SourceDB:             2,
		TargetDB:             7,
		PreparedCount:        1,
		StagedCount:          1,
		EvaluatedAtUnixMilli: 1800000000000,
		PreparedOutputSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		StagedOutputSHA256:   "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210",
		MappingSHA256:        "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		ExactAbsoluteExpiry:  true,
	}
	hash, err := receipt.ComputeSHA256()
	if err != nil {
		t.Fatalf("ComputeSHA256() error = %v", err)
	}
	receipt.ReceiptSHA256 = hash
	if err := receipt.Validate(); err != nil {
		t.Fatalf("receipt.Validate() error = %v", err)
	}
	canonical, err := receipt.CanonicalJSON()
	if err != nil {
		t.Fatalf("CanonicalJSON() error = %v", err)
	}
	if !bytes.Contains(canonical, []byte(`"receipt_sha256":""`)) {
		t.Fatalf("canonical receipt includes self hash: %s", canonical)
	}
}

func TestStageCanonicalThreadEntriesFreshBindsCutoffSelectedSubset(t *testing.T) {
	prepared := stageTestPrepared(t, 1800000000001, 1800000001001)
	firstFake := &canonicalThreadStageFakeClient{dbSizes: []int64{0, 2}}
	secondFake := &canonicalThreadStageFakeClient{dbSizes: []int64{0, 1}}
	var selected *canonicalThreadStageFakeClient
	withCanonicalThreadStageFactory(t, func(*redis.Options) (canonicalThreadStageClient, error) {
		return selected, nil
	})

	selected = firstFake
	first, err := StageCanonicalThreadEntriesFresh(context.Background(), "redis://localhost:6379/2", 7, prepared, time.UnixMilli(1800000000000))
	if err != nil {
		t.Fatalf("first staging error = %v", err)
	}
	selected = secondFake
	second, err := StageCanonicalThreadEntriesFresh(context.Background(), "redis://localhost:6379/2", 7, prepared, time.UnixMilli(1800000000500))
	if err != nil {
		t.Fatalf("second staging error = %v", err)
	}
	if first.StagedOutputSHA256 == second.StagedOutputSHA256 {
		t.Fatalf("staged subset hashes are equal: %q", first.StagedOutputSHA256)
	}
	if first.ReceiptSHA256 == second.ReceiptSHA256 {
		t.Fatalf("receipts are equal despite different cutoff-selected subsets")
	}
	if first.EvaluatedAtUnixMilli == second.EvaluatedAtUnixMilli {
		t.Fatalf("evaluated cutoffs are equal")
	}
	if len(firstFake.writes) != 2 || len(secondFake.writes) != 1 {
		t.Fatalf("writes by cutoff = %d/%d, want 2/1", len(firstFake.writes), len(secondFake.writes))
	}
}

func TestCanonicalThreadStageReceiptRejectsTamperedStagedSubset(t *testing.T) {
	prepared := stageTestPrepared(t, 1799999999999, 1800000000001)
	fake := &canonicalThreadStageFakeClient{dbSizes: []int64{0, 1}}
	withCanonicalThreadStageFactory(t, func(*redis.Options) (canonicalThreadStageClient, error) {
		return fake, nil
	})
	receipt, err := StageCanonicalThreadEntriesFresh(context.Background(), "redis://localhost:6379/2", 7, prepared, time.UnixMilli(1800000000000))
	if err != nil {
		t.Fatalf("staging error = %v", err)
	}
	receipt.StagedOutputSHA256 = strings.Repeat("0", 64)
	if err := receipt.Validate(); err == nil {
		t.Fatal("receipt.Validate() error = nil after staged subset tamper")
	}
}

type canonicalThreadStageWrite struct {
	key     string
	value   []byte
	expires int64
}

type canonicalThreadStageFakeClient struct {
	dbSizes     []int64
	dbSizeIndex int
	pings       int
	closes      int
	writes      []canonicalThreadStageWrite
	values      map[string][]byte
	expiries    map[string]int64
	writeError  error
	failOnWrite int
	deletes     int
	flushes     int
}

func (fake *canonicalThreadStageFakeClient) Ping(context.Context) error {
	fake.pings++
	return nil
}

func (fake *canonicalThreadStageFakeClient) DBSize(context.Context) (int64, error) {
	if fake.dbSizeIndex >= len(fake.dbSizes) {
		return int64(len(fake.values)), nil
	}
	result := fake.dbSizes[fake.dbSizeIndex]
	fake.dbSizeIndex++
	return result, nil
}

func (fake *canonicalThreadStageFakeClient) SetPXATNX(_ context.Context, key string, value []byte, expires int64) error {
	if fake.failOnWrite > 0 && len(fake.writes)+1 == fake.failOnWrite {
		return fake.writeError
	}
	if fake.values == nil {
		fake.values = make(map[string][]byte)
	}
	if fake.expiries == nil {
		fake.expiries = make(map[string]int64)
	}
	fake.writes = append(fake.writes, canonicalThreadStageWrite{key: key, value: append([]byte(nil), value...), expires: expires})
	fake.values[key] = append([]byte(nil), value...)
	fake.expiries[key] = expires
	return nil
}

func (fake *canonicalThreadStageFakeClient) Read(_ context.Context, key string) ([]byte, int64, error) {
	return append([]byte(nil), fake.values[key]...), fake.expiries[key], nil
}

func (fake *canonicalThreadStageFakeClient) Scan(_ context.Context, _ uint64, _ string, _ int64) ([]string, uint64, error) {
	keys := make([]string, 0, len(fake.values))
	for key := range fake.values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys, 0, nil
}

func (fake *canonicalThreadStageFakeClient) Close() error {
	fake.closes++
	return nil
}

func stageTestPrepared(t *testing.T, sessionExpiry, threadExpiry int64) threadmigration.RedisPreparationResult {
	t.Helper()
	plan, err := threadmigration.BuildPlan([]threadmigration.LegacyThreadFact{{
		Surface: "redis", RecordKey: "thread:7", SessionID: "session-001", LegacyThreadID: 7,
	}})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	prepared, err := threadmigration.PrepareRedisProjection(threadmigration.RedisPreparationInput{
		Phase: threadmigration.RedisPreparationPhase,
		Plan:  plan,
		Entries: []threadmigration.RedisEntry{
			{Key: "thread:7", Value: []byte(`{"thread_id":7,"session_id":"session-001","domain":"general","turns":[],"targets":["user"],"ct":{"x":1},"ts_start":"2026-01-01T00:00:00Z","ts_end":"2026-01-01T00:01:00Z","status":"closed"}`), ExpireAtUnixMilli: threadExpiry},
			{Key: "sess:session-001", Value: []byte(`{"session_id":"session-001","user_id":"user","history":[],"agenda":"agenda","last_thread_id":0,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:01:00Z"}`), ExpireAtUnixMilli: sessionExpiry},
		},
	})
	if err != nil {
		t.Fatalf("PrepareRedisProjection() error = %v", err)
	}
	return prepared
}

func withCanonicalThreadStageFactory(t *testing.T, factory canonicalThreadStageClientFactory) {
	t.Helper()
	previous := newCanonicalThreadStageClient
	newCanonicalThreadStageClient = factory
	t.Cleanup(func() { newCanonicalThreadStageClient = previous })
}

func cloneRedisOptionsForStageTest(options *redis.Options) *redis.Options {
	if options == nil {
		return nil
	}
	clone := *options
	return &clone
}
