package threadmigration

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	RedisPreparationReceiptSchemaVersion = "rencrow.threadmigration.redis_preparation.v1"
	RedisPreparationStatus               = "prepared_redis_not_runtime_ready"
	RedisPreparationPhase                = "step05_redis_preparation"
	RedisPreparationMaxEntries           = 10000
	RedisPreparationMaxValueBytes        = 32 * 1024 * 1024
	redisProjectionMaxJSONDepth          = 128
)

var (
	ErrRedisProjectionInvalid    = errors.New("redis projection input is invalid")
	ErrRedisProjectionWrongPhase = errors.New("redis projection input is in the wrong phase")
)

// RedisEntry is an adapter-neutral snapshot of one Redis key. ExpireAtUnixMilli
// is the Redis server's absolute expiry, not a relative TTL: a later apply must
// not extend an entry's lifetime or resurrect one that has already expired.
// The preparation operation never owns or mutates the caller's Value bytes.
type RedisEntry struct {
	Key               string `json:"key"`
	Value             []byte `json:"value"`
	ExpireAtUnixMilli int64  `json:"expire_at_unix_milli"`
}

// RedisPreparationInput contains one validated identity plan and the complete
// caller-owned legacy Redis snapshot to prepare. It contains no Redis client,
// network handle, filesystem path, or runtime state.
type RedisPreparationInput struct {
	Phase   string       `json:"phase"`
	Plan    Plan         `json:"plan"`
	Entries []RedisEntry `json:"entries"`
}

// RedisPreparationReceipt binds the complete deterministic source/output
// cohort to the validated mapping plan. ReceiptSHA256 excludes itself from
// CanonicalJSON to avoid a self-referential digest.
type RedisPreparationReceipt struct {
	SchemaVersion string `json:"schema_version"`
	Status        string `json:"status"`

	SourceCount   int    `json:"source_count"`
	OutputCount   int    `json:"output_count"`
	SourceSHA256  string `json:"source_sha256"`
	OutputSHA256  string `json:"output_sha256"`
	MappingSHA256 string `json:"mapping_sha256"`
	ReceiptSHA256 string `json:"receipt_sha256"`
}

// RedisPreparationResult is an in-memory, apply-ready Redis projection. A
// later owner operation may apply Entries; this function itself never does.
type RedisPreparationResult struct {
	Plan    Plan                    `json:"plan"`
	Entries []RedisEntry            `json:"entries"`
	Receipt RedisPreparationReceipt `json:"receipt"`
}

// PrepareRedisProjection validates and deterministically transforms only the
// legacy sess:<SessionID> and thread:<numeric ThreadID> records. The supplied
// Plan is used as-is; no mapping is inferred or created by this operation.
func PrepareRedisProjection(input RedisPreparationInput) (RedisPreparationResult, error) {
	if input.Phase != RedisPreparationPhase {
		return RedisPreparationResult{}, redisProjectionWrongPhase("Redis preparation phase %q is not %q", input.Phase, RedisPreparationPhase)
	}
	if len(input.Entries) > RedisPreparationMaxEntries {
		return RedisPreparationResult{}, redisProjectionInvalid("entry count exceeds %d", RedisPreparationMaxEntries)
	}
	if err := input.Plan.Validate(); err != nil {
		return RedisPreparationResult{}, redisProjectionInvalid("mapping plan is invalid: %v", err)
	}

	sourceEntries := cloneRedisEntries(input.Entries)
	outputEntries := make([]RedisEntry, 0, len(input.Entries))
	seenInputKeys := make(map[string]struct{}, len(input.Entries))
	seenOutputKeys := make(map[string]struct{}, len(input.Entries))
	for index, entry := range input.Entries {
		if _, exists := seenInputKeys[entry.Key]; exists {
			return RedisPreparationResult{}, redisProjectionInvalid("duplicate input key %q", entry.Key)
		}
		seenInputKeys[entry.Key] = struct{}{}
		if entry.ExpireAtUnixMilli <= 0 {
			return RedisPreparationResult{}, redisProjectionInvalid("entry %d key %q has non-positive absolute expiry", index, entry.Key)
		}
		if len(entry.Value) > RedisPreparationMaxValueBytes {
			return RedisPreparationResult{}, redisProjectionInvalid("entry %d key %q value exceeds %d bytes", index, entry.Key, RedisPreparationMaxValueBytes)
		}

		output, err := prepareRedisEntry(input.Plan, entry)
		if err != nil {
			return RedisPreparationResult{}, fmt.Errorf("prepare Redis entry %d key %q: %w", index, entry.Key, err)
		}
		if len(output.Value) > RedisPreparationMaxValueBytes {
			return RedisPreparationResult{}, redisProjectionInvalid("entry %d key %q output value exceeds %d bytes", index, entry.Key, RedisPreparationMaxValueBytes)
		}
		if _, exists := seenOutputKeys[output.Key]; exists {
			return RedisPreparationResult{}, redisProjectionInvalid("duplicate output key %q", output.Key)
		}
		seenOutputKeys[output.Key] = struct{}{}
		outputEntries = append(outputEntries, output)
	}

	sort.Slice(outputEntries, func(left, right int) bool {
		return outputEntries[left].Key < outputEntries[right].Key
	})
	sourceSHA256, err := redisEntriesSHA256(sourceEntries)
	if err != nil {
		return RedisPreparationResult{}, redisProjectionInvalid("hash source entries: %v", err)
	}
	outputSHA256, err := redisEntriesSHA256(outputEntries)
	if err != nil {
		return RedisPreparationResult{}, redisProjectionInvalid("hash output entries: %v", err)
	}
	receipt := RedisPreparationReceipt{
		SchemaVersion: RedisPreparationReceiptSchemaVersion,
		Status:        RedisPreparationStatus,
		SourceCount:   len(input.Entries),
		OutputCount:   len(outputEntries),
		SourceSHA256:  sourceSHA256,
		OutputSHA256:  outputSHA256,
		MappingSHA256: input.Plan.MappingSHA256,
	}
	receipt.ReceiptSHA256, err = receipt.ComputeSHA256()
	if err != nil {
		return RedisPreparationResult{}, redisProjectionInvalid("hash receipt: %v", err)
	}
	result := RedisPreparationResult{Plan: input.Plan, Entries: outputEntries, Receipt: receipt}
	if err := result.Validate(); err != nil {
		return RedisPreparationResult{}, redisProjectionInvalid("prepared result is invalid: %v", err)
	}
	return result, nil
}

func prepareRedisEntry(plan Plan, entry RedisEntry) (RedisEntry, error) {
	kind, keyIdentity, legacyThreadID, err := parseRedisLegacyKey(entry.Key)
	if err != nil {
		return RedisEntry{}, err
	}
	switch kind {
	case redisEntrySession:
		value, canonicalKey, err := transformRedisSession(plan, keyIdentity, entry.Value)
		if err != nil {
			return RedisEntry{}, err
		}
		return RedisEntry{Key: canonicalKey, Value: value, ExpireAtUnixMilli: entry.ExpireAtUnixMilli}, nil
	case redisEntryThread:
		value, canonicalKey, err := transformRedisThread(plan, legacyThreadID, entry.Value)
		if err != nil {
			return RedisEntry{}, err
		}
		return RedisEntry{Key: canonicalKey, Value: value, ExpireAtUnixMilli: entry.ExpireAtUnixMilli}, nil
	default:
		return RedisEntry{}, redisProjectionInvalid("unsupported Redis key type %q", entry.Key)
	}
}

type redisEntryKind string

const (
	redisEntrySession redisEntryKind = "session"
	redisEntryThread  redisEntryKind = "thread"
)

func parseRedisLegacyKey(key string) (redisEntryKind, string, int64, error) {
	if !utf8.ValidString(key) {
		return "", "", 0, redisProjectionInvalid("Redis key is not valid UTF-8")
	}
	switch {
	case strings.HasPrefix(key, "sess:"):
		sessionID := strings.TrimPrefix(key, "sess:")
		if err := validateLegacyRedisSessionKeyPart(sessionID); err != nil {
			return "", "", 0, err
		}
		return redisEntrySession, sessionID, 0, nil
	case strings.HasPrefix(key, "thread:"):
		text := strings.TrimPrefix(key, "thread:")
		legacyID, err := parsePositiveDecimalKeyID(text)
		if err != nil {
			return "", "", 0, err
		}
		return redisEntryThread, "", legacyID, nil
	default:
		return "", "", 0, redisProjectionInvalid("unsupported Redis key prefix or syntax %q", key)
	}
}

func parseRedisCanonicalKey(key string) (redisEntryKind, string, error) {
	if !utf8.ValidString(key) {
		return "", "", redisProjectionInvalid("output Redis key is not valid UTF-8")
	}
	switch {
	case strings.HasPrefix(key, "sess:"):
		sessionID := strings.TrimPrefix(key, "sess:")
		if err := modulecore.SessionID(sessionID).Validate(); err != nil {
			return "", "", redisProjectionInvalid("output session key is not canonical: %v", err)
		}
		return redisEntrySession, sessionID, nil
	case strings.HasPrefix(key, "thread:"):
		threadID := strings.TrimPrefix(key, "thread:")
		if err := modulecore.ThreadID(threadID).Validate(); err != nil {
			return "", "", redisProjectionInvalid("output thread key is not canonical: %v", err)
		}
		return redisEntryThread, threadID, nil
	default:
		return "", "", redisProjectionInvalid("output Redis key prefix or syntax is invalid %q", key)
	}
}

func validateLegacyRedisSessionKeyPart(value string) error {
	if value == "" {
		return redisProjectionInvalid("legacy session key identity is empty")
	}
	if !utf8.ValidString(value) {
		return redisProjectionInvalid("legacy session key identity is not valid UTF-8")
	}
	for _, char := range value {
		if char == ':' || char < 0x20 || char == 0x7f {
			return redisProjectionInvalid("legacy session key identity has invalid key syntax")
		}
	}
	return nil
}

func parsePositiveDecimalKeyID(value string) (int64, error) {
	if value == "" || value[0] == '0' {
		return 0, redisProjectionInvalid("legacy thread key must contain a positive decimal ID")
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return 0, redisProjectionInvalid("legacy thread key must contain a positive decimal ID")
		}
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != value {
		return 0, redisProjectionInvalid("legacy thread key must contain a positive decimal ID")
	}
	return parsed, nil
}

func transformRedisSession(plan Plan, legacyKeySessionID string, raw []byte) ([]byte, string, error) {
	fields, err := decodeRedisObject(raw)
	if err != nil {
		return nil, "", err
	}
	if err := validateRedisSessionSchema(fields); err != nil {
		return nil, "", err
	}
	sessionID, err := redisStringField(fields, "session_id")
	if err != nil {
		return nil, "", err
	}
	if sessionID != legacyKeySessionID {
		return nil, "", redisProjectionInvalid("session key/value identity mismatch")
	}
	canonicalSessionID, err := canonicalGenericSessionID(sessionID)
	if err != nil {
		return nil, "", redisProjectionInvalid("session_id cannot be canonicalized: %v", err)
	}
	matchedSessionID := ""
	adoptMatchedSession := func(mapping ThreadMapping) error {
		candidate := string(mapping.SessionID)
		if matchedSessionID == "" {
			matchedSessionID = candidate
			canonicalSessionID = candidate
			return nil
		}
		if matchedSessionID != candidate {
			return redisProjectionInvalid("session/history identity mismatch")
		}
		return nil
	}

	history, err := redisArrayField(fields, "history")
	if err != nil {
		return nil, "", err
	}
	canonicalHistory := make([]json.RawMessage, 0, len(history))
	for index, rawSummary := range history {
		canonicalSummary, mapping, err := transformRedisThreadSummary(plan, sessionID, rawSummary)
		if err != nil {
			return nil, "", fmt.Errorf("history item %d: %w", index, err)
		}
		if err := adoptMatchedSession(mapping); err != nil {
			return nil, "", fmt.Errorf("history item %d: %w", index, err)
		}
		canonicalHistory = append(canonicalHistory, canonicalSummary)
	}
	historyJSON, err := json.Marshal(canonicalHistory)
	if err != nil {
		return nil, "", redisProjectionInvalid("marshal canonical history: %v", err)
	}

	legacyLastThreadID, err := redisLegacyThreadIDField(fields["last_thread_id"], "last_thread_id", true)
	if err != nil {
		return nil, "", err
	}
	lastThreadID := ""
	lastThreadSeq := int64(0)
	lastThreadKind := ""
	if legacyLastThreadID > 0 {
		mapping, err := resolveRedisLegacyTuple(plan, sessionID, legacyLastThreadID)
		if err != nil {
			return nil, "", fmt.Errorf("last_thread_id %d: %w", legacyLastThreadID, err)
		}
		if err := adoptMatchedSession(mapping); err != nil {
			return nil, "", err
		}
		lastThreadID = string(mapping.ThreadID)
		lastThreadSeq = int64(mapping.ThreadSeq)
		lastThreadKind = string(mapping.ThreadKind)
	}

	output := cloneRedisJSONFields(fields)
	output["session_id"] = redisJSONScalar(canonicalSessionID)
	output["history"] = json.RawMessage(historyJSON)
	output["last_thread_id"] = redisJSONScalar(lastThreadID)
	output["last_thread_seq"] = redisJSONInt(lastThreadSeq)
	output["last_thread_kind"] = redisJSONScalar(lastThreadKind)
	encoded, err := json.Marshal(output)
	if err != nil {
		return nil, "", redisProjectionInvalid("marshal canonical session: %v", err)
	}
	return encoded, "sess:" + canonicalSessionID, nil
}

func transformRedisThreadSummary(plan Plan, legacySessionID string, raw []byte) (json.RawMessage, ThreadMapping, error) {
	fields, err := decodeRedisObject(raw)
	if err != nil {
		return nil, ThreadMapping{}, err
	}
	if err := validateRedisThreadSummarySchema(fields); err != nil {
		return nil, ThreadMapping{}, err
	}
	legacyThreadID, err := redisLegacyThreadIDField(fields["thread_id"], "history.thread_id", false)
	if err != nil {
		return nil, ThreadMapping{}, err
	}
	summarySessionID, err := redisStringField(fields, "session_id")
	if err != nil {
		return nil, ThreadMapping{}, err
	}
	if summarySessionID != legacySessionID {
		return nil, ThreadMapping{}, redisProjectionInvalid("session/history identity mismatch")
	}
	mapping, err := resolveRedisLegacyTuple(plan, legacySessionID, legacyThreadID)
	if err != nil {
		return nil, ThreadMapping{}, fmt.Errorf("history thread_id %d: %w", legacyThreadID, err)
	}
	output := cloneRedisJSONFields(fields)
	output["thread_id"] = redisJSONScalar(string(mapping.ThreadID))
	output["thread_seq"] = redisJSONInt(int64(mapping.ThreadSeq))
	output["thread_kind"] = redisJSONScalar(string(mapping.ThreadKind))
	output["session_id"] = redisJSONScalar(string(mapping.SessionID))
	encoded, err := json.Marshal(output)
	if err != nil {
		return nil, ThreadMapping{}, redisProjectionInvalid("marshal canonical history item: %v", err)
	}
	return json.RawMessage(encoded), mapping, nil
}

func transformRedisThread(plan Plan, keyLegacyThreadID int64, raw []byte) ([]byte, string, error) {
	fields, err := decodeRedisObject(raw)
	if err != nil {
		return nil, "", err
	}
	if err := validateRedisThreadSchema(fields); err != nil {
		return nil, "", err
	}
	valueLegacyThreadID, err := redisLegacyThreadIDField(fields["thread_id"], "thread.thread_id", false)
	if err != nil {
		return nil, "", err
	}
	if valueLegacyThreadID != keyLegacyThreadID {
		return nil, "", redisProjectionInvalid("thread key/value identity mismatch")
	}
	legacySessionID, err := redisStringField(fields, "session_id")
	if err != nil {
		return nil, "", err
	}
	mapping, err := resolveRedisLegacyTuple(plan, legacySessionID, keyLegacyThreadID)
	if err != nil {
		return nil, "", fmt.Errorf("thread_id %d: %w", keyLegacyThreadID, err)
	}
	canonicalSessionID := string(mapping.SessionID)

	output := cloneRedisJSONFields(fields)
	output["thread_id"] = redisJSONScalar(string(mapping.ThreadID))
	output["thread_seq"] = redisJSONInt(int64(mapping.ThreadSeq))
	output["thread_kind"] = redisJSONScalar(string(mapping.ThreadKind))
	output["session_id"] = redisJSONScalar(canonicalSessionID)
	encoded, err := json.Marshal(output)
	if err != nil {
		return nil, "", redisProjectionInvalid("marshal canonical thread: %v", err)
	}
	return encoded, "thread:" + string(mapping.ThreadID), nil
}

// resolveRedisLegacyTuple resolves one exact legacy (raw session, numeric
// thread) tuple against both mapping families in a merged Plan. Generic
// mappings use canonicalGenericSessionID; ChatGPT mappings use the synthetic
// legacy tuple derived from their conversation ID. Exactly one candidate is
// required so a merged plan can never silently choose an owner.
func resolveRedisLegacyTuple(plan Plan, legacySessionID string, legacyThreadID int64) (ThreadMapping, error) {
	if legacyThreadID <= 0 {
		return ThreadMapping{}, redisProjectionInvalid("legacy thread ID must be positive")
	}
	candidates := make([]ThreadMapping, 0, 2)
	canonicalSessionID, canonicalErr := canonicalGenericSessionID(legacySessionID)
	if canonicalErr == nil {
		if mapping, ok := plan.LookupGeneric(canonicalSessionID, legacyThreadID); ok {
			candidates = append(candidates, mapping)
		}
	}
	for _, mapping := range plan.ChatGPT {
		expectedSessionID, expectedThreadID, err := chatGPTLegacyTuple(mapping.ChatGPTConversationID)
		if err != nil {
			return ThreadMapping{}, redisProjectionInvalid("ChatGPT mapping %q has invalid legacy tuple: %v", mapping.SemanticKey, err)
		}
		if expectedSessionID == legacySessionID && expectedThreadID == legacyThreadID {
			candidates = append(candidates, cloneMapping(mapping))
		}
	}
	switch len(candidates) {
	case 0:
		if canonicalErr != nil {
			return ThreadMapping{}, redisProjectionInvalid("legacy session cannot be canonicalized: %v", canonicalErr)
		}
		return ThreadMapping{}, redisProjectionInvalid("missing mapping for legacy tuple %q/%d", legacySessionID, legacyThreadID)
	case 1:
		return candidates[0], nil
	default:
		return ThreadMapping{}, redisProjectionInvalid("ambiguous mapping for legacy tuple %q/%d", legacySessionID, legacyThreadID)
	}
}

func validateRedisSessionSchema(fields map[string]json.RawMessage) error {
	if err := rejectRedisPreexistingIdentity(fields, "session"); err != nil {
		return err
	}
	if err := checkRedisSchema(fields, redisSessionAllowedFields, []string{"session_id", "user_id", "history", "agenda", "last_thread_id", "created_at", "updated_at"}, "session"); err != nil {
		return err
	}
	if _, err := redisStringField(fields, "session_id"); err != nil {
		return err
	}
	if _, err := redisStringField(fields, "user_id"); err != nil {
		return err
	}
	if _, err := redisArrayField(fields, "history"); err != nil {
		return err
	}
	if _, err := redisStringField(fields, "agenda"); err != nil {
		return err
	}
	if _, err := redisLegacyThreadIDField(fields["last_thread_id"], "last_thread_id", true); err != nil {
		return err
	}
	if err := validateRedisTimeField(fields, "created_at", false); err != nil {
		return err
	}
	return validateRedisTimeField(fields, "updated_at", false)
}

func validateRedisThreadSummarySchema(fields map[string]json.RawMessage) error {
	if err := rejectRedisPreexistingIdentity(fields, "history"); err != nil {
		return err
	}
	if err := checkRedisSchema(fields, redisThreadSummaryAllowedFields, []string{"thread_id", "session_id", "domain", "summary", "keywords", "ts_start", "ts_end", "is_novel"}, "history summary"); err != nil {
		return err
	}
	if _, err := redisLegacyThreadIDField(fields["thread_id"], "history.thread_id", false); err != nil {
		return err
	}
	if _, err := redisStringField(fields, "session_id"); err != nil {
		return err
	}
	if _, err := redisStringField(fields, "domain"); err != nil {
		return err
	}
	if _, err := redisStringField(fields, "summary"); err != nil {
		return err
	}
	if _, err := redisStringArrayField(fields, "keywords"); err != nil {
		return err
	}
	if raw, ok := fields["roles"]; ok {
		if _, err := redisStringArray(raw, "roles"); err != nil {
			return err
		}
	}
	if raw, ok := fields["receipt"]; ok {
		if err := validateRedisNullableObject(raw, "receipt"); err != nil {
			return err
		}
	}
	if raw, ok := fields["embedding"]; ok {
		if _, err := redisFloatArray(raw, "embedding", true); err != nil {
			return err
		}
	}
	if err := validateRedisTimeField(fields, "ts_start", false); err != nil {
		return err
	}
	if err := validateRedisTimeField(fields, "ts_end", false); err != nil {
		return err
	}
	if _, err := redisBoolField(fields, "is_novel"); err != nil {
		return err
	}
	if raw, ok := fields["score"]; ok {
		if _, err := redisFloat(raw, "score", true); err != nil {
			return err
		}
	}
	return nil
}

func validateRedisThreadSchema(fields map[string]json.RawMessage) error {
	if err := rejectRedisPreexistingIdentity(fields, "thread"); err != nil {
		return err
	}
	if err := checkRedisSchema(fields, redisThreadAllowedFields, []string{"thread_id", "session_id", "domain", "turns", "targets", "ct", "ts_start", "status"}, "thread"); err != nil {
		return err
	}
	if _, err := redisLegacyThreadIDField(fields["thread_id"], "thread.thread_id", false); err != nil {
		return err
	}
	if _, err := redisStringField(fields, "session_id"); err != nil {
		return err
	}
	if _, err := redisStringField(fields, "domain"); err != nil {
		return err
	}
	turns, err := redisArrayField(fields, "turns")
	if err != nil {
		return err
	}
	for index, rawTurn := range turns {
		if _, err := decodeRedisObject(rawTurn); err != nil {
			return fmt.Errorf("thread.turns[%d]: %w", index, err)
		}
	}
	if _, err := redisStringArrayField(fields, "targets"); err != nil {
		return err
	}
	if err := validateRedisIntegerMap(fields["ct"], "ct"); err != nil {
		return err
	}
	if err := validateRedisTimeField(fields, "ts_start", false); err != nil {
		return err
	}
	if raw, ok := fields["ts_end"]; ok {
		if !isRedisJSONNull(raw) {
			if err := validateRedisTimeField(fields, "ts_end", false); err != nil {
				return err
			}
		} else {
			// A pointer time in the legacy Thread schema may be explicitly null.
			// There is no additional validation for that representation.
		}
	}
	_, err = redisStringField(fields, "status")
	return err
}

var (
	redisSessionAllowedFields = map[string]struct{}{
		"session_id": {}, "user_id": {}, "history": {}, "agenda": {},
		"last_thread_id": {}, "created_at": {}, "updated_at": {},
	}
	redisThreadSummaryAllowedFields = map[string]struct{}{
		"thread_id": {}, "session_id": {}, "domain": {}, "summary": {},
		"keywords": {}, "roles": {}, "receipt": {}, "embedding": {},
		"ts_start": {}, "ts_end": {}, "is_novel": {}, "score": {},
	}
	redisThreadAllowedFields = map[string]struct{}{
		"thread_id": {}, "session_id": {}, "domain": {}, "turns": {},
		"targets": {}, "ct": {}, "ts_start": {}, "ts_end": {}, "status": {},
	}
	redisPreparedSessionAllowedFields = map[string]struct{}{
		"session_id": {}, "user_id": {}, "history": {}, "agenda": {},
		"last_thread_id": {}, "last_thread_seq": {}, "last_thread_kind": {},
		"created_at": {}, "updated_at": {},
	}
	redisPreparedThreadSummaryAllowedFields = map[string]struct{}{
		"thread_id": {}, "thread_seq": {}, "thread_kind": {}, "session_id": {},
		"domain": {}, "summary": {}, "keywords": {}, "roles": {}, "receipt": {},
		"embedding": {}, "ts_start": {}, "ts_end": {}, "is_novel": {}, "score": {},
	}
	redisPreparedThreadAllowedFields = map[string]struct{}{
		"thread_id": {}, "thread_seq": {}, "thread_kind": {}, "session_id": {},
		"domain": {}, "turns": {}, "targets": {}, "ct": {}, "ts_start": {},
		"ts_end": {}, "status": {},
	}
	redisTurnAllowedFields = map[string]struct{}{
		"speaker": {}, "msg": {}, "ts": {}, "meta": {},
	}
)

func rejectRedisPreexistingIdentity(fields map[string]json.RawMessage, scope string) error {
	for _, key := range []string{"thread_seq", "thread_kind", "last_thread_seq", "last_thread_kind"} {
		if _, exists := fields[key]; exists {
			return redisProjectionWrongPhase("%s contains preexisting %s", scope, key)
		}
	}
	return nil
}

func checkRedisSchema(fields map[string]json.RawMessage, allowed map[string]struct{}, required []string, scope string) error {
	unknown := make([]string, 0)
	for key := range fields {
		if _, ok := allowed[key]; !ok {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return redisProjectionInvalid("unknown %s schema key %q", scope, unknown[0])
	}
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			return redisProjectionInvalid("missing %s schema key %q", scope, key)
		}
	}
	return nil
}

func redisLegacyThreadIDField(raw json.RawMessage, field string, allowZero bool) (int64, error) {
	if isRedisJSONString(raw) {
		value, err := redisString(raw, field)
		if err == nil && modulecore.ThreadID(value).Validate() == nil {
			return 0, redisProjectionWrongPhase("%s contains a canonical string thread_id", field)
		}
		return 0, redisProjectionInvalid("%s must be a legacy JSON integer", field)
	}
	value, err := redisInteger(raw, field)
	if err != nil {
		return 0, err
	}
	if value < 0 || (!allowZero && value == 0) {
		return 0, redisProjectionInvalid("%s must be a positive legacy thread ID", field)
	}
	return value, nil
}

func cloneRedisJSONFields(fields map[string]json.RawMessage) map[string]json.RawMessage {
	result := make(map[string]json.RawMessage, len(fields)+4)
	for key, value := range fields {
		result[key] = append(json.RawMessage(nil), value...)
	}
	return result
}

func decodeRedisObject(raw []byte) (map[string]json.RawMessage, error) {
	if len(raw) > RedisPreparationMaxValueBytes {
		return nil, redisProjectionInvalid("JSON value exceeds %d bytes", RedisPreparationMaxValueBytes)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, redisProjectionInvalid("JSON value is blank")
	}
	if !utf8.Valid(raw) {
		return nil, redisProjectionInvalid("JSON value is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	first, err := decoder.Token()
	if err != nil {
		return nil, redisProjectionInvalid("decode JSON object: %v", err)
	}
	if first != json.Delim('{') {
		return nil, redisProjectionInvalid("JSON value must be an object")
	}
	if err := consumeRedisJSONValue(decoder, first, 0); err != nil {
		return nil, err
	}
	if trailing, err := decoder.Token(); err != io.EOF {
		if err != nil {
			return nil, redisProjectionInvalid("trailing JSON token: %v", err)
		}
		return nil, redisProjectionInvalid("trailing JSON token %v", trailing)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, redisProjectionInvalid("decode JSON object fields: %v", err)
	}
	if fields == nil {
		return nil, redisProjectionInvalid("JSON value must be an object")
	}
	return fields, nil
}

func consumeRedisJSONValue(decoder *json.Decoder, token json.Token, depth int) error {
	if depth > redisProjectionMaxJSONDepth {
		return redisProjectionInvalid("JSON nesting exceeds %d", redisProjectionMaxJSONDepth)
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return redisProjectionInvalid("decode JSON object key: %v", err)
			}
			key, ok := keyToken.(string)
			if !ok {
				return redisProjectionInvalid("JSON object key is not a string")
			}
			if _, exists := seen[key]; exists {
				return redisProjectionInvalid("duplicate JSON object member %q", key)
			}
			seen[key] = struct{}{}
			valueToken, err := decoder.Token()
			if err != nil {
				return redisProjectionInvalid("decode JSON member %q: %v", key, err)
			}
			if err := consumeRedisJSONValue(decoder, valueToken, depth+1); err != nil {
				return err
			}
		}
		closeToken, err := decoder.Token()
		if err != nil || closeToken != json.Delim('}') {
			return redisProjectionInvalid("JSON object is not closed")
		}
	case '[':
		for decoder.More() {
			valueToken, err := decoder.Token()
			if err != nil {
				return redisProjectionInvalid("decode JSON array item: %v", err)
			}
			if err := consumeRedisJSONValue(decoder, valueToken, depth+1); err != nil {
				return err
			}
		}
		closeToken, err := decoder.Token()
		if err != nil || closeToken != json.Delim(']') {
			return redisProjectionInvalid("JSON array is not closed")
		}
	default:
		return redisProjectionInvalid("unexpected JSON delimiter %q", delim)
	}
	return nil
}

func redisStringField(fields map[string]json.RawMessage, field string) (string, error) {
	value, ok := fields[field]
	if !ok {
		return "", redisProjectionInvalid("missing JSON field %q", field)
	}
	return redisString(value, field)
}

func redisString(raw json.RawMessage, field string) (string, error) {
	if !isRedisJSONString(raw) {
		return "", redisProjectionInvalid("JSON field %q must be a string", field)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", redisProjectionInvalid("JSON field %q string is invalid: %v", field, err)
	}
	return value, nil
}

func redisBoolField(fields map[string]json.RawMessage, field string) (bool, error) {
	value, ok := fields[field]
	if !ok {
		return false, redisProjectionInvalid("missing JSON field %q", field)
	}
	return redisBool(value, field)
}

func redisBool(raw json.RawMessage, field string) (bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || (trimmed[0] != 't' && trimmed[0] != 'f') {
		return false, redisProjectionInvalid("JSON field %q must be a boolean", field)
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, redisProjectionInvalid("JSON field %q must be a boolean", field)
	}
	return value, nil
}

func redisInteger(raw json.RawMessage, field string) (int64, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return 0, redisProjectionInvalid("JSON field %q integer is invalid: %v", field, err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return 0, redisProjectionInvalid("JSON field %q must be one integer", field)
	}
	number, ok := token.(json.Number)
	if !ok || strings.ContainsAny(number.String(), ".eE") {
		return 0, redisProjectionInvalid("JSON field %q must be an integer", field)
	}
	value, err := strconv.ParseInt(number.String(), 10, 64)
	if err != nil || strconv.FormatInt(value, 10) != number.String() {
		return 0, redisProjectionInvalid("JSON field %q integer is out of range", field)
	}
	return value, nil
}

func redisIntegerField(fields map[string]json.RawMessage, field string) (int64, error) {
	raw, ok := fields[field]
	if !ok {
		return 0, redisProjectionInvalid("missing JSON field %q", field)
	}
	return redisInteger(raw, field)
}

func redisFloat(raw json.RawMessage, field string, requireFloat32 bool) (float64, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return 0, redisProjectionInvalid("JSON field %q number is invalid: %v", field, err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return 0, redisProjectionInvalid("JSON field %q must be one number", field)
	}
	number, ok := token.(json.Number)
	if !ok {
		return 0, redisProjectionInvalid("JSON field %q must be a number", field)
	}
	value, err := strconv.ParseFloat(number.String(), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, redisProjectionInvalid("JSON field %q number is not finite", field)
	}
	if requireFloat32 && (math.IsInf(float64(float32(value)), 0) || (value != 0 && float64(float32(value)) == 0)) {
		return 0, redisProjectionInvalid("JSON field %q is outside float32 range", field)
	}
	return value, nil
}

func redisArrayField(fields map[string]json.RawMessage, field string) ([]json.RawMessage, error) {
	value, ok := fields[field]
	if !ok {
		return nil, redisProjectionInvalid("missing JSON field %q", field)
	}
	return redisArray(value, field)
}

func redisArray(raw json.RawMessage, field string) ([]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, redisProjectionInvalid("JSON field %q must be an array", field)
	}
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return nil, redisProjectionInvalid("JSON field %q must be an array", field)
	}
	return values, nil
}

func redisStringArrayField(fields map[string]json.RawMessage, field string) ([]string, error) {
	value, ok := fields[field]
	if !ok {
		return nil, redisProjectionInvalid("missing JSON field %q", field)
	}
	return redisStringArray(value, field)
}

func redisStringArray(raw json.RawMessage, field string) ([]string, error) {
	items, err := redisArray(raw, field)
	if err != nil {
		return nil, err
	}
	values := make([]string, 0, len(items))
	for index, item := range items {
		value, err := redisString(item, fmt.Sprintf("%s[%d]", field, index))
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func redisFloatArray(raw json.RawMessage, field string, requireFloat32 bool) ([]float64, error) {
	items, err := redisArray(raw, field)
	if err != nil {
		return nil, err
	}
	values := make([]float64, 0, len(items))
	for index, item := range items {
		value, err := redisFloat(item, fmt.Sprintf("%s[%d]", field, index), requireFloat32)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func validateRedisNullableObject(raw json.RawMessage, field string) error {
	if isRedisJSONNull(raw) {
		return nil
	}
	if _, err := decodeRedisObject(raw); err != nil {
		return redisProjectionInvalid("JSON field %q must be an object or null: %v", field, err)
	}
	return nil
}

func validateRedisIntegerMap(raw json.RawMessage, field string) error {
	fields, err := decodeRedisObject(raw)
	if err != nil {
		return redisProjectionInvalid("JSON field %q must be an object: %v", field, err)
	}
	for key, value := range fields {
		if _, err := redisInteger(value, field+"."+key); err != nil {
			return err
		}
	}
	return nil
}

func validateRedisTimeField(fields map[string]json.RawMessage, field string, nullable bool) error {
	raw, ok := fields[field]
	if !ok {
		return redisProjectionInvalid("missing JSON field %q", field)
	}
	if nullable && isRedisJSONNull(raw) {
		return nil
	}
	value, err := redisString(raw, field)
	if err != nil {
		return err
	}
	if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
		return redisProjectionInvalid("JSON field %q is not an RFC3339 timestamp", field)
	}
	return nil
}

func isRedisJSONString(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '"'
}

func isRedisJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func redisJSONScalar(value string) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("marshal Redis identity string: %v", err))
	}
	return encoded
}

func redisJSONInt(value int64) json.RawMessage {
	return json.RawMessage(strconv.FormatInt(value, 10))
}

func cloneRedisEntries(entries []RedisEntry) []RedisEntry {
	if entries == nil {
		return []RedisEntry{}
	}
	result := make([]RedisEntry, len(entries))
	for index, entry := range entries {
		result[index] = RedisEntry{Key: entry.Key, Value: append([]byte(nil), entry.Value...), ExpireAtUnixMilli: entry.ExpireAtUnixMilli}
	}
	return result
}

func redisEntriesSHA256(entries []RedisEntry) (string, error) {
	canonical, err := canonicalRedisEntriesJSON(entries)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func canonicalRedisEntriesJSON(entries []RedisEntry) ([]byte, error) {
	ordered := cloneRedisEntries(entries)
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left].Key != ordered[right].Key {
			return ordered[left].Key < ordered[right].Key
		}
		if ordered[left].ExpireAtUnixMilli != ordered[right].ExpireAtUnixMilli {
			return ordered[left].ExpireAtUnixMilli < ordered[right].ExpireAtUnixMilli
		}
		return bytes.Compare(ordered[left].Value, ordered[right].Value) < 0
	})
	type canonicalEntry struct {
		Key               string `json:"key"`
		Value             []byte `json:"value"`
		ExpireAtUnixMilli int64  `json:"expire_at_unix_milli"`
	}
	canonical := make([]canonicalEntry, 0, len(ordered))
	for _, entry := range ordered {
		if !utf8.ValidString(entry.Key) || !utf8.Valid(entry.Value) {
			return nil, redisProjectionInvalid("entry key or value is not valid UTF-8")
		}
		canonical = append(canonical, canonicalEntry{Key: entry.Key, Value: append([]byte(nil), entry.Value...), ExpireAtUnixMilli: entry.ExpireAtUnixMilli})
	}
	if canonical == nil {
		canonical = []canonicalEntry{}
	}
	return json.Marshal(canonical)
}

func (receipt RedisPreparationReceipt) CanonicalJSON() ([]byte, error) {
	copy := receipt
	copy.ReceiptSHA256 = ""
	return json.Marshal(copy)
}

func (receipt RedisPreparationReceipt) ComputeSHA256() (string, error) {
	canonical, err := receipt.CanonicalJSON()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func (receipt RedisPreparationReceipt) Validate() error {
	if receipt.SchemaVersion != RedisPreparationReceiptSchemaVersion || receipt.Status != RedisPreparationStatus {
		return redisProjectionInvalid("Redis receipt schema or status is invalid")
	}
	if receipt.SourceCount < 0 || receipt.SourceCount > RedisPreparationMaxEntries || receipt.OutputCount < 0 || receipt.OutputCount > RedisPreparationMaxEntries || receipt.SourceCount != receipt.OutputCount {
		return redisProjectionInvalid("Redis receipt counts are invalid")
	}
	for label, hash := range map[string]string{
		"source":  receipt.SourceSHA256,
		"output":  receipt.OutputSHA256,
		"mapping": receipt.MappingSHA256,
		"receipt": receipt.ReceiptSHA256,
	} {
		if !validRedisSHA256(hash) {
			return redisProjectionInvalid("Redis receipt %s SHA256 is invalid", label)
		}
	}
	computed, err := receipt.ComputeSHA256()
	if err != nil {
		return err
	}
	if computed != receipt.ReceiptSHA256 {
		return redisProjectionInvalid("Redis receipt SHA256 does not match canonical JSON")
	}
	return nil
}

func (result RedisPreparationResult) Validate() error {
	if err := result.Plan.Validate(); err != nil {
		return redisProjectionInvalid("mapping plan is invalid: %v", err)
	}
	if err := result.Receipt.Validate(); err != nil {
		return err
	}
	if result.Receipt.MappingSHA256 != result.Plan.MappingSHA256 {
		return redisProjectionInvalid("Redis result is not bound to its mapping plan")
	}
	if result.Entries == nil || len(result.Entries) != result.Receipt.OutputCount || len(result.Entries) > RedisPreparationMaxEntries {
		return redisProjectionInvalid("Redis result output count is invalid")
	}
	seen := make(map[string]struct{}, len(result.Entries))
	mappings, err := newRedisCanonicalMappingIndex(result.Plan)
	if err != nil {
		return err
	}
	for index, entry := range result.Entries {
		if entry.ExpireAtUnixMilli <= 0 {
			return redisProjectionInvalid("Redis result entry %d has non-positive absolute expiry", index)
		}
		if len(entry.Value) > RedisPreparationMaxValueBytes || !utf8.ValidString(entry.Key) || !utf8.Valid(entry.Value) {
			return redisProjectionInvalid("Redis result entry %d is invalid", index)
		}
		if _, exists := seen[entry.Key]; exists {
			return redisProjectionInvalid("Redis result contains duplicate output key %q", entry.Key)
		}
		seen[entry.Key] = struct{}{}
		if index > 0 && result.Entries[index-1].Key >= entry.Key {
			return redisProjectionInvalid("Redis result output keys are not sorted")
		}
		kind, identity, err := parseRedisCanonicalKey(entry.Key)
		if err != nil {
			return err
		}
		fields, err := decodeRedisObject(entry.Value)
		if err != nil {
			return err
		}
		switch kind {
		case redisEntrySession:
			if err := validatePreparedRedisSession(identity, fields, mappings); err != nil {
				return err
			}
		case redisEntryThread:
			if err := validatePreparedRedisThread(identity, fields, mappings); err != nil {
				return err
			}
		default:
			return redisProjectionInvalid("Redis result entry %d has unsupported type", index)
		}
	}
	outputSHA256, err := redisEntriesSHA256(result.Entries)
	if err != nil {
		return err
	}
	if outputSHA256 != result.Receipt.OutputSHA256 {
		return redisProjectionInvalid("Redis output SHA256 does not match entries")
	}
	return nil
}

type redisCanonicalMappingIndex struct {
	byThreadID map[string]ThreadMapping
}

func newRedisCanonicalMappingIndex(plan Plan) (redisCanonicalMappingIndex, error) {
	index := redisCanonicalMappingIndex{byThreadID: make(map[string]ThreadMapping, len(plan.Generic)+len(plan.ChatGPT))}
	for _, mapping := range append(append([]ThreadMapping{}, plan.Generic...), plan.ChatGPT...) {
		threadID := string(mapping.ThreadID)
		if _, exists := index.byThreadID[threadID]; exists {
			return redisCanonicalMappingIndex{}, redisProjectionInvalid("mapping plan has duplicate canonical thread ID %q", threadID)
		}
		index.byThreadID[threadID] = mapping
	}
	return index, nil
}

func validatePreparedRedisSession(canonicalSessionID string, fields map[string]json.RawMessage, mappings redisCanonicalMappingIndex) error {
	if err := checkRedisSchema(fields, redisPreparedSessionAllowedFields, []string{
		"session_id", "user_id", "history", "agenda", "last_thread_id", "last_thread_seq", "last_thread_kind", "created_at", "updated_at",
	}, "prepared session"); err != nil {
		return err
	}
	sessionID, err := redisStringField(fields, "session_id")
	if err != nil {
		return err
	}
	if sessionID != canonicalSessionID {
		return redisProjectionInvalid("prepared session identity does not match its key")
	}
	if err := modulecore.SessionID(sessionID).Validate(); err != nil {
		return redisProjectionInvalid("prepared session ID is not canonical")
	}
	if _, err := redisStringField(fields, "user_id"); err != nil {
		return err
	}
	if _, err := redisStringField(fields, "agenda"); err != nil {
		return err
	}
	if err := validateRedisTimeField(fields, "created_at", false); err != nil {
		return err
	}
	if err := validateRedisTimeField(fields, "updated_at", false); err != nil {
		return err
	}

	history, err := redisArrayField(fields, "history")
	if err != nil {
		return err
	}
	seenThreadIDs := make(map[string]struct{}, len(history))
	seenThreadSeqs := make(map[int64]struct{}, len(history))
	for index, rawSummary := range history {
		summaryFields, err := decodeRedisObject(rawSummary)
		if err != nil {
			return fmt.Errorf("prepared session history item %d: %w", index, err)
		}
		threadID, seq, kind, summarySessionID, err := validatePreparedRedisThreadSummary(summaryFields, sessionID)
		if err != nil {
			return fmt.Errorf("prepared session history item %d: %w", index, err)
		}
		if _, exists := seenThreadIDs[threadID]; exists {
			return redisProjectionInvalid("prepared session history contains duplicate thread_id %q", threadID)
		}
		seenThreadIDs[threadID] = struct{}{}
		if _, exists := seenThreadSeqs[seq]; exists {
			return redisProjectionInvalid("prepared session history contains duplicate thread_seq %d", seq)
		}
		seenThreadSeqs[seq] = struct{}{}
		mapping, ok := mappings.byThreadID[threadID]
		if !ok {
			return redisProjectionInvalid("prepared session history thread_id %q has no mapping", threadID)
		}
		if string(mapping.SessionID) != summarySessionID || int64(mapping.ThreadSeq) != seq || string(mapping.ThreadKind) != kind {
			return redisProjectionInvalid("prepared session history thread_id %q tuple does not match mapping", threadID)
		}
	}

	lastThreadID, err := redisStringField(fields, "last_thread_id")
	if err != nil {
		return err
	}
	lastThreadSeq, err := redisIntegerField(fields, "last_thread_seq")
	if err != nil {
		return err
	}
	lastThreadKind, err := redisStringField(fields, "last_thread_kind")
	if err != nil {
		return err
	}
	if lastThreadID == "" {
		if lastThreadSeq != 0 || lastThreadKind != "" {
			return redisProjectionInvalid("prepared empty last thread tuple is inconsistent")
		}
		return nil
	}
	if err := modulecore.ThreadID(lastThreadID).Validate(); err != nil || lastThreadSeq <= 0 {
		return redisProjectionInvalid("prepared last thread tuple is invalid")
	}
	if err := modulecore.ThreadKind(lastThreadKind).Validate(); err != nil {
		return redisProjectionInvalid("prepared last thread kind is invalid")
	}
	mapping, ok := mappings.byThreadID[lastThreadID]
	if !ok {
		return redisProjectionInvalid("prepared last thread_id %q has no mapping", lastThreadID)
	}
	if string(mapping.SessionID) != sessionID || int64(mapping.ThreadSeq) != lastThreadSeq || string(mapping.ThreadKind) != lastThreadKind {
		return redisProjectionInvalid("prepared last thread tuple does not match mapping")
	}
	return nil
}

func validatePreparedRedisThreadSummary(fields map[string]json.RawMessage, expectedSessionID string) (string, int64, string, string, error) {
	if err := checkRedisSchema(fields, redisPreparedThreadSummaryAllowedFields, []string{
		"thread_id", "thread_seq", "thread_kind", "session_id", "domain", "summary", "keywords", "ts_start", "ts_end", "is_novel",
	}, "prepared history summary"); err != nil {
		return "", 0, "", "", err
	}
	threadID, err := redisStringField(fields, "thread_id")
	if err != nil {
		return "", 0, "", "", err
	}
	if err := modulecore.ThreadID(threadID).Validate(); err != nil {
		return "", 0, "", "", redisProjectionInvalid("prepared history thread ID is not canonical")
	}
	seq, err := redisIntegerField(fields, "thread_seq")
	if err != nil {
		return "", 0, "", "", err
	}
	if err := modulecore.ThreadSeq(seq).Validate(); err != nil {
		return "", 0, "", "", redisProjectionInvalid("prepared history thread sequence is invalid")
	}
	kind, err := redisStringField(fields, "thread_kind")
	if err != nil {
		return "", 0, "", "", err
	}
	if err := modulecore.ThreadKind(kind).Validate(); err != nil {
		return "", 0, "", "", redisProjectionInvalid("prepared history thread kind is invalid")
	}
	sessionID, err := redisStringField(fields, "session_id")
	if err != nil {
		return "", 0, "", "", err
	}
	if sessionID != expectedSessionID {
		return "", 0, "", "", redisProjectionInvalid("prepared history session identity does not match session")
	}
	if err := modulecore.SessionID(sessionID).Validate(); err != nil {
		return "", 0, "", "", redisProjectionInvalid("prepared history session ID is not canonical")
	}
	if _, err := redisStringField(fields, "domain"); err != nil {
		return "", 0, "", "", err
	}
	if _, err := redisStringField(fields, "summary"); err != nil {
		return "", 0, "", "", err
	}
	if _, err := redisStringArrayField(fields, "keywords"); err != nil {
		return "", 0, "", "", err
	}
	if raw, ok := fields["roles"]; ok {
		if _, err := redisStringArray(raw, "roles"); err != nil {
			return "", 0, "", "", err
		}
	}
	if raw, ok := fields["receipt"]; ok {
		if err := validateRedisNullableObject(raw, "receipt"); err != nil {
			return "", 0, "", "", err
		}
	}
	if raw, ok := fields["embedding"]; ok {
		if _, err := redisFloatArray(raw, "embedding", true); err != nil {
			return "", 0, "", "", err
		}
	}
	if err := validateRedisTimeField(fields, "ts_start", false); err != nil {
		return "", 0, "", "", err
	}
	if err := validateRedisTimeField(fields, "ts_end", false); err != nil {
		return "", 0, "", "", err
	}
	if _, err := redisBoolField(fields, "is_novel"); err != nil {
		return "", 0, "", "", err
	}
	if raw, ok := fields["score"]; ok {
		if _, err := redisFloat(raw, "score", true); err != nil {
			return "", 0, "", "", err
		}
	}
	return threadID, seq, kind, sessionID, nil
}

func validatePreparedRedisThread(canonicalThreadID string, fields map[string]json.RawMessage, mappings redisCanonicalMappingIndex) error {
	if err := checkRedisSchema(fields, redisPreparedThreadAllowedFields, []string{
		"thread_id", "thread_seq", "thread_kind", "session_id", "domain", "turns", "targets", "ct", "ts_start", "status",
	}, "prepared thread"); err != nil {
		return err
	}
	threadID, err := redisStringField(fields, "thread_id")
	if err != nil || threadID != canonicalThreadID {
		return redisProjectionInvalid("prepared thread identity does not match its key")
	}
	if err := modulecore.ThreadID(threadID).Validate(); err != nil {
		return redisProjectionInvalid("prepared thread ID is not canonical")
	}
	seq, err := redisIntegerField(fields, "thread_seq")
	if err != nil {
		return err
	}
	if err := modulecore.ThreadSeq(seq).Validate(); err != nil {
		return redisProjectionInvalid("prepared thread sequence is invalid")
	}
	kind, err := redisStringField(fields, "thread_kind")
	if err != nil {
		return err
	}
	if err := modulecore.ThreadKind(kind).Validate(); err != nil {
		return redisProjectionInvalid("prepared thread kind is invalid")
	}
	sessionID, err := redisStringField(fields, "session_id")
	if err != nil {
		return err
	}
	if err := modulecore.SessionID(sessionID).Validate(); err != nil {
		return redisProjectionInvalid("prepared thread session ID is not canonical")
	}
	if _, err := redisStringField(fields, "domain"); err != nil {
		return err
	}
	turns, err := redisArrayField(fields, "turns")
	if err != nil {
		return err
	}
	for index, rawTurn := range turns {
		if err := validateRedisTurn(rawTurn, index); err != nil {
			return err
		}
	}
	if _, err := redisStringArrayField(fields, "targets"); err != nil {
		return err
	}
	if err := validateRedisIntegerMap(fields["ct"], "ct"); err != nil {
		return err
	}
	if err := validateRedisTimeField(fields, "ts_start", false); err != nil {
		return err
	}
	if raw, ok := fields["ts_end"]; ok && !isRedisJSONNull(raw) {
		if err := validateRedisTimeField(fields, "ts_end", false); err != nil {
			return err
		}
	}
	status, err := redisStringField(fields, "status")
	if err != nil {
		return err
	}
	if status != "active" && status != "closed" && status != "archived" {
		return redisProjectionInvalid("prepared thread status is invalid")
	}
	mapping, ok := mappings.byThreadID[threadID]
	if !ok {
		return redisProjectionInvalid("prepared thread_id %q has no mapping", threadID)
	}
	if string(mapping.SessionID) != sessionID || int64(mapping.ThreadSeq) != seq || string(mapping.ThreadKind) != kind {
		return redisProjectionInvalid("prepared thread tuple does not match mapping")
	}
	return nil
}

func validateRedisTurn(raw json.RawMessage, index int) error {
	fields, err := decodeRedisObject(raw)
	if err != nil {
		return fmt.Errorf("thread.turns[%d]: %w", index, err)
	}
	if err := checkRedisSchema(fields, redisTurnAllowedFields, []string{"speaker", "msg", "ts"}, fmt.Sprintf("thread.turns[%d]", index)); err != nil {
		return err
	}
	if _, err := redisStringField(fields, "speaker"); err != nil {
		return err
	}
	if _, err := redisStringField(fields, "msg"); err != nil {
		return err
	}
	if err := validateRedisTimeField(fields, "ts", false); err != nil {
		return err
	}
	if rawMeta, ok := fields["meta"]; ok && !isRedisJSONNull(rawMeta) {
		if _, err := decodeRedisObject(rawMeta); err != nil {
			return redisProjectionInvalid("JSON field %q must be an object or null: %v", "meta", err)
		}
	}
	return nil
}

func validRedisSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func redisProjectionInvalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrRedisProjectionInvalid, fmt.Sprintf(format, args...))
}

func redisProjectionWrongPhase(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrRedisProjectionWrongPhase, fmt.Sprintf(format, args...))
}
