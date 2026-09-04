package redisstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/threadmigration"
	"github.com/redis/go-redis/v9"
)

const (
	CanonicalThreadStageReceiptSchemaVersion = "rencrow.threadmigration.redis_stage.v1"
	CanonicalThreadStageStatusStaged         = "staged_not_active"
	CanonicalThreadStageStatusBlocked        = "blocked"

	canonicalThreadStageErrorContext        = "context_unavailable"
	canonicalThreadStageErrorEvaluatedAt    = "invalid_evaluated_at"
	canonicalThreadStageErrorTargetDB       = "invalid_target_db"
	canonicalThreadStageErrorSourceURL      = "invalid_source_redis_url"
	canonicalThreadStageErrorSourceDB       = "invalid_source_db"
	canonicalThreadStageErrorSameDB         = "source_target_db_equal"
	canonicalThreadStageErrorPrepared       = "invalid_prepared_result"
	canonicalThreadStageErrorFactory        = "target_client_unavailable"
	canonicalThreadStageErrorPing           = "target_ping_failed"
	canonicalThreadStageErrorProbe          = "target_probe_failed"
	canonicalThreadStageErrorTargetNotFresh = "target_not_fresh"
	canonicalThreadStageErrorWrite          = "target_write_failed"
	canonicalThreadStageErrorReadback       = "target_readback_failed"
	canonicalThreadStageErrorInventory      = "target_inventory_failed"
	canonicalThreadStageErrorInventorySet   = "target_inventory_mismatch"
	canonicalThreadStageErrorClose          = "target_close_failed"
	canonicalThreadStageErrorReceipt        = "receipt_invalid"
	canonicalThreadStageErrorCanceled       = "context_canceled"
	canonicalThreadStageErrorDeadline       = "context_deadline"

	canonicalThreadStageScanCount int64 = 256
)

// CanonicalThreadStageReceipt is the non-active receipt for one fresh Redis
// logical-DB staging attempt. ReceiptSHA256 is computed over the same JSON
// with receipt_sha256 blank, making the receipt self-verifying and secret-free.
type CanonicalThreadStageReceipt struct {
	SchemaVersion        string `json:"schema_version"`
	Status               string `json:"status"`
	SourceDB             int    `json:"source_db"`
	TargetDB             int    `json:"target_db"`
	PreparedCount        int    `json:"prepared_count"`
	ExpiredCount         int    `json:"expired_count"`
	StagedCount          int    `json:"staged_count"`
	EvaluatedAtUnixMilli int64  `json:"evaluated_at_unix_milli"`
	PreparedOutputSHA256 string `json:"prepared_output_sha256"`
	StagedOutputSHA256   string `json:"staged_output_sha256"`
	MappingSHA256        string `json:"mapping_sha256"`
	ExactAbsoluteExpiry  bool   `json:"exact_absolute_expiry"`
	ReceiptSHA256        string `json:"receipt_sha256"`
	ErrorCode            string `json:"error_code"`
}

// CanonicalJSON returns the deterministic receipt payload with its self-hash
// excluded. encoding/json preserves this struct's field order.
func (receipt CanonicalThreadStageReceipt) CanonicalJSON() ([]byte, error) {
	copy := receipt
	copy.ReceiptSHA256 = ""
	return json.Marshal(copy)
}

// ComputeSHA256 computes the lowercase SHA-256 digest of CanonicalJSON.
func (receipt CanonicalThreadStageReceipt) ComputeSHA256() (string, error) {
	canonical, err := receipt.CanonicalJSON()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

// Validate checks the receipt schema, bounded counts, and canonical self-hash.
// A blocked receipt may use -1 for a DB that was not safely parsed and may
// omit preparation hashes when validation failed before they were available.
func (receipt CanonicalThreadStageReceipt) Validate() error {
	if receipt.SchemaVersion != CanonicalThreadStageReceiptSchemaVersion {
		return fmt.Errorf("canonical Redis stage receipt schema is invalid")
	}
	switch receipt.Status {
	case CanonicalThreadStageStatusStaged:
		if receipt.SourceDB < 0 || receipt.TargetDB < 0 {
			return fmt.Errorf("staged Redis receipt DB values are invalid")
		}
		if receipt.EvaluatedAtUnixMilli <= 0 {
			return fmt.Errorf("staged Redis receipt evaluated time is invalid")
		}
		if receipt.ErrorCode != "" || !receipt.ExactAbsoluteExpiry {
			return fmt.Errorf("staged Redis receipt terminal fields are invalid")
		}
	case CanonicalThreadStageStatusBlocked:
		if receipt.SourceDB < -1 || receipt.TargetDB < -1 {
			return fmt.Errorf("blocked Redis receipt DB values are invalid")
		}
		if receipt.ErrorCode == "" || receipt.ExactAbsoluteExpiry {
			return fmt.Errorf("blocked Redis receipt terminal fields are invalid")
		}
	default:
		return fmt.Errorf("canonical Redis stage receipt status is invalid")
	}
	if receipt.PreparedCount < 0 || receipt.ExpiredCount < 0 || receipt.StagedCount < 0 {
		return fmt.Errorf("canonical Redis stage receipt counts are negative")
	}
	if receipt.ExpiredCount > receipt.PreparedCount || receipt.StagedCount > receipt.PreparedCount-receipt.ExpiredCount {
		return fmt.Errorf("canonical Redis stage receipt counts are inconsistent")
	}
	if receipt.Status == CanonicalThreadStageStatusStaged && receipt.ExpiredCount+receipt.StagedCount != receipt.PreparedCount {
		return fmt.Errorf("staged Redis receipt counts are incomplete")
	}
	if receipt.PreparedOutputSHA256 != "" && !validCanonicalThreadStageSHA256(receipt.PreparedOutputSHA256) {
		return fmt.Errorf("prepared output SHA256 is invalid")
	}
	if receipt.StagedOutputSHA256 != "" && !validCanonicalThreadStageSHA256(receipt.StagedOutputSHA256) {
		return fmt.Errorf("staged output SHA256 is invalid")
	}
	if receipt.MappingSHA256 != "" && !validCanonicalThreadStageSHA256(receipt.MappingSHA256) {
		return fmt.Errorf("mapping SHA256 is invalid")
	}
	if receipt.Status == CanonicalThreadStageStatusStaged && (!validCanonicalThreadStageSHA256(receipt.PreparedOutputSHA256) || !validCanonicalThreadStageSHA256(receipt.StagedOutputSHA256) || !validCanonicalThreadStageSHA256(receipt.MappingSHA256)) {
		return fmt.Errorf("staged Redis receipt preparation hashes are missing")
	}
	if !validCanonicalThreadStageSHA256(receipt.ReceiptSHA256) {
		return fmt.Errorf("receipt SHA256 is invalid")
	}
	computed, err := receipt.ComputeSHA256()
	if err != nil {
		return err
	}
	if computed != receipt.ReceiptSHA256 {
		return fmt.Errorf("receipt SHA256 does not match canonical JSON")
	}
	return nil
}

// canonicalThreadStageClient is deliberately narrower than redis.Client so
// tests can prove write ordering and failure behavior without a Redis server.
type canonicalThreadStageClient interface {
	Ping(context.Context) error
	DBSize(context.Context) (int64, error)
	SetPXATNX(context.Context, string, []byte, int64) error
	Read(context.Context, string) ([]byte, int64, error)
	Scan(context.Context, uint64, string, int64) ([]string, uint64, error)
	Close() error
}

type canonicalThreadStageClientFactory func(*redis.Options) (canonicalThreadStageClient, error)

// newCanonicalThreadStageClient is a private test seam. Production always
// receives a cloned redis.Options whose DB is the disposable target DB.
var newCanonicalThreadStageClient canonicalThreadStageClientFactory = func(options *redis.Options) (canonicalThreadStageClient, error) {
	if options == nil {
		return nil, fmt.Errorf("nil Redis options")
	}
	return &canonicalThreadStageRedisClient{client: redis.NewClient(options)}, nil
}

type canonicalThreadStageRedisClient struct {
	client *redis.Client
}

func (client *canonicalThreadStageRedisClient) Ping(ctx context.Context) error {
	if client == nil || client.client == nil || ctx == nil {
		return fmt.Errorf("Redis client is unavailable")
	}
	return client.client.Ping(ctx).Err()
}

func (client *canonicalThreadStageRedisClient) DBSize(ctx context.Context) (int64, error) {
	if client == nil || client.client == nil || ctx == nil {
		return 0, fmt.Errorf("Redis client is unavailable")
	}
	return client.client.DBSize(ctx).Result()
}

func (client *canonicalThreadStageRedisClient) SetPXATNX(ctx context.Context, key string, value []byte, expiryMillis int64) error {
	if client == nil || client.client == nil || ctx == nil {
		return fmt.Errorf("Redis client is unavailable")
	}
	result, err := client.client.Do(ctx, "SET", key, value, "PXAT", expiryMillis, "NX").Result()
	if err != nil {
		return err
	}
	status, ok := result.(string)
	if !ok || status != "OK" {
		return fmt.Errorf("Redis SET NX did not return OK")
	}
	return nil
}

func (client *canonicalThreadStageRedisClient) Read(ctx context.Context, key string) ([]byte, int64, error) {
	if client == nil || client.client == nil || ctx == nil {
		return nil, 0, fmt.Errorf("Redis client is unavailable")
	}
	var valueCommand *redis.StringCmd
	var expiryCommand *redis.Cmd
	if _, err := client.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		valueCommand = pipe.Get(ctx, key)
		expiryCommand = pipe.Do(ctx, "PEXPIRETIME", key)
		return nil
	}); err != nil || valueCommand == nil || expiryCommand == nil {
		return nil, 0, fmt.Errorf("Redis readback transaction failed")
	}
	value, err := valueCommand.Bytes()
	if err != nil {
		return nil, 0, err
	}
	expiry, err := expiryCommand.Int64()
	if err != nil {
		return nil, 0, err
	}
	return append([]byte(nil), value...), expiry, nil
}

func (client *canonicalThreadStageRedisClient) Scan(ctx context.Context, cursor uint64, match string, count int64) ([]string, uint64, error) {
	if client == nil || client.client == nil || ctx == nil {
		return nil, 0, fmt.Errorf("Redis client is unavailable")
	}
	keys, nextCursor, err := client.client.Scan(ctx, cursor, match, count).Result()
	if err != nil {
		return nil, 0, err
	}
	return append([]string(nil), keys...), nextCursor, nil
}

func (client *canonicalThreadStageRedisClient) Close() error {
	if client == nil || client.client == nil {
		return fmt.Errorf("Redis client is unavailable")
	}
	return client.client.Close()
}

// StageCanonicalThreadEntriesFresh stages a validated Redis projection into a
// fresh logical DB. It never reads or writes the source DB and never cleans a
// target after a partial failure; a blocked receipt truthfully identifies that
// unclaimed partial state.
func StageCanonicalThreadEntriesFresh(ctx context.Context, sourceRedisURL string, targetDB int, prepared threadmigration.RedisPreparationResult, now time.Time) (CanonicalThreadStageReceipt, error) {
	receipt := newCanonicalThreadStageReceipt(targetDB)
	receipt.EvaluatedAtUnixMilli = now.UnixMilli()
	if receipt.EvaluatedAtUnixMilli <= 0 {
		return blockCanonicalThreadStage(receipt, canonicalThreadStageErrorEvaluatedAt)
	}
	if ctx == nil {
		return blockCanonicalThreadStage(receipt, canonicalThreadStageErrorContext)
	}
	if code := canonicalThreadStageContextCode(ctx); code != "" {
		return blockCanonicalThreadStage(receipt, code)
	}
	if targetDB < 0 {
		return blockCanonicalThreadStage(receipt, canonicalThreadStageErrorTargetDB)
	}

	sourceOptions, err := redis.ParseURL(sourceRedisURL)
	if err != nil || sourceOptions == nil {
		return blockCanonicalThreadStage(receipt, canonicalThreadStageErrorSourceURL)
	}
	receipt.SourceDB = sourceOptions.DB
	if sourceOptions.DB < 0 {
		return blockCanonicalThreadStage(receipt, canonicalThreadStageErrorSourceDB)
	}
	if sourceOptions.DB == targetDB {
		return blockCanonicalThreadStage(receipt, canonicalThreadStageErrorSameDB)
	}

	receipt.PreparedCount = len(prepared.Entries)
	if err := prepared.Validate(); err != nil {
		return blockCanonicalThreadStage(receipt, canonicalThreadStageErrorPrepared)
	}
	receipt.PreparedOutputSHA256 = prepared.Receipt.OutputSHA256
	receipt.MappingSHA256 = prepared.Receipt.MappingSHA256
	if code := canonicalThreadStageContextCode(ctx); code != "" {
		return blockCanonicalThreadStage(receipt, code)
	}

	targetOptions := *sourceOptions
	targetOptions.DB = targetDB
	targetOptions.MaxRetries = 0
	if newCanonicalThreadStageClient == nil {
		return blockCanonicalThreadStage(receipt, canonicalThreadStageErrorFactory)
	}
	client, err := newCanonicalThreadStageClient(&targetOptions)
	if err != nil || client == nil {
		return blockCanonicalThreadStage(receipt, canonicalThreadStageErrorFactory)
	}

	stageReceipt, stageErr := stageCanonicalThreadEntriesWithClient(ctx, client, prepared, now, receipt)
	closeErr := client.Close()
	if stageErr != nil {
		return stageReceipt, stageErr
	}
	if closeErr != nil {
		return blockCanonicalThreadStage(stageReceipt, canonicalThreadStageErrorClose)
	}
	return stageReceipt, nil
}

func stageCanonicalThreadEntriesWithClient(ctx context.Context, client canonicalThreadStageClient, prepared threadmigration.RedisPreparationResult, now time.Time, receipt CanonicalThreadStageReceipt) (CanonicalThreadStageReceipt, error) {
	if code := canonicalThreadStageContextCode(ctx); code != "" {
		return blockCanonicalThreadStage(receipt, code)
	}
	if err := client.Ping(ctx); err != nil {
		return blockCanonicalThreadStage(receipt, canonicalThreadStageErrorPing)
	}
	if code := canonicalThreadStageContextCode(ctx); code != "" {
		return blockCanonicalThreadStage(receipt, code)
	}
	beforeSize, err := client.DBSize(ctx)
	if err != nil {
		return blockCanonicalThreadStage(receipt, canonicalThreadStageErrorProbe)
	}
	if beforeSize != 0 {
		return blockCanonicalThreadStage(receipt, canonicalThreadStageErrorTargetNotFresh)
	}

	entries := append([]threadmigration.RedisEntry(nil), prepared.Entries...)
	sort.Slice(entries, func(left, right int) bool { return entries[left].Key < entries[right].Key })
	cutoff := now.UnixMilli()
	expected := make(map[string]struct{}, len(entries))
	active := make([]threadmigration.RedisEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.ExpireAtUnixMilli <= cutoff {
			receipt.ExpiredCount++
			continue
		}
		active = append(active, entry)
		expected[entry.Key] = struct{}{}
	}
	stagedEntries := make([]threadmigration.RedisEntry, 0, len(active))
	stagedOutputSHA256, err := canonicalThreadStageEntriesSHA256(active)
	if err != nil {
		return blockCanonicalThreadStage(receipt, canonicalThreadStageErrorReceipt)
	}
	receipt.StagedOutputSHA256 = stagedOutputSHA256

	for _, entry := range active {
		if code := canonicalThreadStageContextCode(ctx); code != "" {
			if partialHash, hashErr := canonicalThreadStageEntriesSHA256(stagedEntries); hashErr == nil {
				receipt.StagedOutputSHA256 = partialHash
			}
			return blockCanonicalThreadStage(receipt, code)
		}
		if err := client.SetPXATNX(ctx, entry.Key, entry.Value, entry.ExpireAtUnixMilli); err != nil {
			if partialHash, hashErr := canonicalThreadStageEntriesSHA256(stagedEntries); hashErr == nil {
				receipt.StagedOutputSHA256 = partialHash
			}
			return blockCanonicalThreadStage(receipt, canonicalThreadStageErrorWrite)
		}
		receipt.StagedCount++
		stagedEntries = append(stagedEntries, entry)
	}

	for _, entry := range active {
		if code := canonicalThreadStageContextCode(ctx); code != "" {
			return blockCanonicalThreadStage(receipt, code)
		}
		value, expiry, err := client.Read(ctx, entry.Key)
		if err != nil || !bytes.Equal(value, entry.Value) || expiry != entry.ExpireAtUnixMilli {
			return blockCanonicalThreadStage(receipt, canonicalThreadStageErrorReadback)
		}
	}

	keys, err := scanCanonicalThreadStageKeys(ctx, client)
	if err != nil {
		return blockCanonicalThreadStage(receipt, canonicalThreadStageErrorInventory)
	}
	if len(keys) != len(expected) {
		return blockCanonicalThreadStage(receipt, canonicalThreadStageErrorInventorySet)
	}
	for _, key := range keys {
		if _, ok := expected[key]; !ok {
			return blockCanonicalThreadStage(receipt, canonicalThreadStageErrorInventorySet)
		}
	}
	afterSize, err := client.DBSize(ctx)
	if err != nil {
		return blockCanonicalThreadStage(receipt, canonicalThreadStageErrorProbe)
	}
	if afterSize != int64(len(expected)) {
		return blockCanonicalThreadStage(receipt, canonicalThreadStageErrorInventorySet)
	}

	receipt.Status = CanonicalThreadStageStatusStaged
	receipt.ExactAbsoluteExpiry = true
	receipt.ErrorCode = ""
	sealed, err := sealCanonicalThreadStageReceipt(receipt)
	if err != nil {
		return blockCanonicalThreadStage(receipt, canonicalThreadStageErrorReceipt)
	}
	return sealed, nil
}

func canonicalThreadStageEntriesSHA256(entries []threadmigration.RedisEntry) (string, error) {
	canonical, err := canonicalThreadStageEntriesJSON(entries)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func canonicalThreadStageEntriesJSON(entries []threadmigration.RedisEntry) ([]byte, error) {
	type canonicalEntry struct {
		Key               string `json:"key"`
		Value             []byte `json:"value"`
		ExpireAtUnixMilli int64  `json:"expire_at_unix_milli"`
	}
	ordered := make([]canonicalEntry, 0, len(entries))
	for _, entry := range entries {
		ordered = append(ordered, canonicalEntry{
			Key:               entry.Key,
			Value:             append([]byte(nil), entry.Value...),
			ExpireAtUnixMilli: entry.ExpireAtUnixMilli,
		})
	}
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left].Key != ordered[right].Key {
			return ordered[left].Key < ordered[right].Key
		}
		if ordered[left].ExpireAtUnixMilli != ordered[right].ExpireAtUnixMilli {
			return ordered[left].ExpireAtUnixMilli < ordered[right].ExpireAtUnixMilli
		}
		return bytes.Compare(ordered[left].Value, ordered[right].Value) < 0
	})
	return json.Marshal(ordered)
}

func scanCanonicalThreadStageKeys(ctx context.Context, client canonicalThreadStageClient) ([]string, error) {
	seen := make(map[string]struct{})
	cursor := uint64(0)
	seenCursors := make(map[uint64]struct{})
	for {
		if code := canonicalThreadStageContextCode(ctx); code != "" {
			return nil, fmt.Errorf("%s", code)
		}
		page, nextCursor, err := client.Scan(ctx, cursor, "*", canonicalThreadStageScanCount)
		if err != nil {
			return nil, err
		}
		for _, key := range page {
			seen[key] = struct{}{}
			if len(seen) > threadmigration.RedisPreparationMaxEntries {
				return nil, fmt.Errorf("inventory exceeds staging bounds")
			}
		}
		if nextCursor == 0 {
			break
		}
		if nextCursor == cursor {
			return nil, fmt.Errorf("inventory cursor did not advance")
		}
		if _, ok := seenCursors[nextCursor]; ok {
			return nil, fmt.Errorf("inventory cursor repeated")
		}
		seenCursors[nextCursor] = struct{}{}
		cursor = nextCursor
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys, nil
}

func newCanonicalThreadStageReceipt(targetDB int) CanonicalThreadStageReceipt {
	if targetDB < 0 {
		targetDB = -1
	}
	return CanonicalThreadStageReceipt{
		SchemaVersion: CanonicalThreadStageReceiptSchemaVersion,
		Status:        CanonicalThreadStageStatusBlocked,
		SourceDB:      -1,
		TargetDB:      targetDB,
	}
}

func blockCanonicalThreadStage(receipt CanonicalThreadStageReceipt, code string) (CanonicalThreadStageReceipt, error) {
	receipt.Status = CanonicalThreadStageStatusBlocked
	receipt.ExactAbsoluteExpiry = false
	receipt.ErrorCode = code
	sealed, err := sealCanonicalThreadStageReceipt(receipt)
	if err != nil {
		return receipt, canonicalThreadStageError(canonicalThreadStageErrorReceipt)
	}
	return sealed, canonicalThreadStageError(code)
}

func sealCanonicalThreadStageReceipt(receipt CanonicalThreadStageReceipt) (CanonicalThreadStageReceipt, error) {
	receipt.ReceiptSHA256 = ""
	digest, err := receipt.ComputeSHA256()
	if err != nil {
		return receipt, err
	}
	receipt.ReceiptSHA256 = digest
	if err := receipt.Validate(); err != nil {
		return receipt, err
	}
	return receipt, nil
}

type canonicalThreadStageError string

func (err canonicalThreadStageError) Error() string {
	return "canonical Redis staging blocked: " + string(err)
}

func canonicalThreadStageContextCode(ctx context.Context) string {
	if ctx == nil {
		return canonicalThreadStageErrorContext
	}
	switch ctx.Err() {
	case nil:
		return ""
	case context.Canceled:
		return canonicalThreadStageErrorCanceled
	case context.DeadlineExceeded:
		return canonicalThreadStageErrorDeadline
	default:
		return canonicalThreadStageErrorContext
	}
}

func validCanonicalThreadStageSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
