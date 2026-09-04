package threadmigration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

const (
	RedisInventoryReceiptSchemaVersion = "rencrow.threadmigration.redis_inventory.v1"
	RedisInventoryStatus               = "inventoried_redis_not_runtime_ready"
	RedisInventoryPhase                = "step05_redis_inventory"
	RedisInventoryMaxFacts             = 100000
)

// RedisInventoryInput is a complete caller-owned legacy Redis snapshot. The
// known plan is used only to recognize already-discovered ChatGPT tuples; new
// generic tuples are added to the returned plan.
type RedisInventoryInput struct {
	Phase     string       `json:"phase"`
	KnownPlan Plan         `json:"known_plan"`
	Entries   []RedisEntry `json:"entries"`
}

type RedisInventoryReceipt struct {
	SchemaVersion       string `json:"schema_version"`
	Status              string `json:"status"`
	SourceCount         int    `json:"source_count"`
	FactCount           int    `json:"fact_count"`
	GenericMappingCount int    `json:"generic_mapping_count"`
	ChatGPTMappingCount int    `json:"chatgpt_mapping_count"`
	SourceSHA256        string `json:"source_sha256"`
	MappingSHA256       string `json:"mapping_sha256"`
	ReceiptSHA256       string `json:"receipt_sha256"`
}

type RedisInventoryResult struct {
	Plan    Plan                  `json:"plan"`
	Receipt RedisInventoryReceipt `json:"receipt"`
}

// InventoryRedisProjection validates legacy Redis values and extracts only
// identity facts. It performs no Redis I/O and returns no source content.
func InventoryRedisProjection(input RedisInventoryInput) (RedisInventoryResult, error) {
	if input.Phase != RedisInventoryPhase {
		return RedisInventoryResult{}, redisProjectionWrongPhase("Redis inventory phase %q is not %q", input.Phase, RedisInventoryPhase)
	}
	if len(input.Entries) > RedisPreparationMaxEntries {
		return RedisInventoryResult{}, redisProjectionInvalid("entry count exceeds %d", RedisPreparationMaxEntries)
	}
	if err := input.KnownPlan.Validate(); err != nil {
		return RedisInventoryResult{}, redisProjectionInvalid("known mapping plan is invalid: %v", err)
	}

	facts := make([]LegacyThreadFact, 0, len(input.Entries)*2)
	seenKeys := make(map[string]struct{}, len(input.Entries))
	for index, entry := range input.Entries {
		if _, exists := seenKeys[entry.Key]; exists {
			return RedisInventoryResult{}, redisProjectionInvalid("duplicate input key %q", entry.Key)
		}
		seenKeys[entry.Key] = struct{}{}
		if entry.ExpireAtUnixMilli <= 0 {
			return RedisInventoryResult{}, redisProjectionInvalid("entry %d key %q has non-positive absolute expiry", index, entry.Key)
		}
		if len(entry.Value) > RedisPreparationMaxValueBytes {
			return RedisInventoryResult{}, redisProjectionInvalid("entry %d key %q value exceeds %d bytes", index, entry.Key, RedisPreparationMaxValueBytes)
		}
		entryFacts, err := inventoryRedisEntry(input.KnownPlan, entry)
		if err != nil {
			return RedisInventoryResult{}, fmt.Errorf("inventory Redis entry %d key %q: %w", index, entry.Key, err)
		}
		facts = append(facts, entryFacts...)
		if len(facts) > RedisInventoryMaxFacts {
			return RedisInventoryResult{}, redisProjectionInvalid("identity fact count exceeds %d", RedisInventoryMaxFacts)
		}
	}

	plan, err := BuildPlan(facts)
	if err != nil {
		return RedisInventoryResult{}, redisProjectionInvalid("build Redis mapping plan: %v", err)
	}
	sourceHash, err := redisEntriesSHA256(cloneRedisEntries(input.Entries))
	if err != nil {
		return RedisInventoryResult{}, redisProjectionInvalid("hash Redis source snapshot: %v", err)
	}
	receipt := RedisInventoryReceipt{
		SchemaVersion:       RedisInventoryReceiptSchemaVersion,
		Status:              RedisInventoryStatus,
		SourceCount:         len(input.Entries),
		FactCount:           len(facts),
		GenericMappingCount: len(plan.Generic),
		ChatGPTMappingCount: len(plan.ChatGPT),
		SourceSHA256:        sourceHash,
		MappingSHA256:       plan.MappingSHA256,
	}
	receipt.ReceiptSHA256, err = receipt.ComputeSHA256()
	if err != nil {
		return RedisInventoryResult{}, err
	}
	result := RedisInventoryResult{Plan: plan, Receipt: receipt}
	if err := result.Validate(); err != nil {
		return RedisInventoryResult{}, err
	}
	return result, nil
}

func inventoryRedisEntry(known Plan, entry RedisEntry) ([]LegacyThreadFact, error) {
	kind, keySessionID, keyThreadID, err := parseRedisLegacyKey(entry.Key)
	if err != nil {
		return nil, err
	}
	fields, err := decodeRedisObject(entry.Value)
	if err != nil {
		return nil, err
	}
	switch kind {
	case redisEntrySession:
		if err := validateRedisSessionSchema(fields); err != nil {
			return nil, err
		}
		sessionID, err := redisStringField(fields, "session_id")
		if err != nil {
			return nil, err
		}
		if sessionID != keySessionID {
			return nil, redisProjectionInvalid("session key/value identity mismatch")
		}
		history, err := redisArrayField(fields, "history")
		if err != nil {
			return nil, err
		}
		facts := make([]LegacyThreadFact, 0, len(history)+1)
		for historyIndex, raw := range history {
			summary, err := decodeRedisObject(raw)
			if err != nil {
				return nil, fmt.Errorf("history item %d: %w", historyIndex, err)
			}
			if err := validateRedisThreadSummarySchema(summary); err != nil {
				return nil, fmt.Errorf("history item %d: %w", historyIndex, err)
			}
			summarySessionID, err := redisStringField(summary, "session_id")
			if err != nil || summarySessionID != sessionID {
				return nil, redisProjectionInvalid("history item %d session identity mismatch", historyIndex)
			}
			threadID, err := redisLegacyThreadIDField(summary["thread_id"], "history.thread_id", false)
			if err != nil {
				return nil, err
			}
			fact, err := redisInventoryFact(known, "redis_session_history", entry.Key+"#history/"+strconv.Itoa(historyIndex), sessionID, threadID)
			if err != nil {
				return nil, err
			}
			facts = append(facts, fact)
		}
		lastThreadID, err := redisLegacyThreadIDField(fields["last_thread_id"], "last_thread_id", true)
		if err != nil {
			return nil, err
		}
		if lastThreadID > 0 {
			fact, err := redisInventoryFact(known, "redis_session_last", entry.Key+"#last_thread_id", sessionID, lastThreadID)
			if err != nil {
				return nil, err
			}
			facts = append(facts, fact)
		}
		return facts, nil
	case redisEntryThread:
		if err := validateRedisThreadSchema(fields); err != nil {
			return nil, err
		}
		valueThreadID, err := redisLegacyThreadIDField(fields["thread_id"], "thread.thread_id", false)
		if err != nil {
			return nil, err
		}
		if valueThreadID != keyThreadID {
			return nil, redisProjectionInvalid("thread key/value identity mismatch")
		}
		sessionID, err := redisStringField(fields, "session_id")
		if err != nil {
			return nil, err
		}
		fact, err := redisInventoryFact(known, "redis_thread", entry.Key, sessionID, keyThreadID)
		if err != nil {
			return nil, err
		}
		return []LegacyThreadFact{fact}, nil
	default:
		return nil, redisProjectionInvalid("unsupported Redis key type %q", entry.Key)
	}
}

func redisInventoryFact(known Plan, surface, recordKey, sessionID string, threadID int64) (LegacyThreadFact, error) {
	canonicalSessionID, err := canonicalGenericSessionID(sessionID)
	if err != nil {
		return LegacyThreadFact{}, redisProjectionInvalid("canonicalize legacy session: %v", err)
	}
	_, genericMatch := known.LookupGeneric(canonicalSessionID, threadID)
	chatGPTConversationID := ""
	for _, mapping := range known.ChatGPT {
		legacySessionID, legacyThreadID, err := chatGPTLegacyTuple(mapping.ChatGPTConversationID)
		if err != nil {
			return LegacyThreadFact{}, redisProjectionInvalid("known ChatGPT mapping is invalid: %v", err)
		}
		if legacySessionID == sessionID && legacyThreadID == threadID {
			if chatGPTConversationID != "" {
				return LegacyThreadFact{}, redisProjectionInvalid("multiple ChatGPT mappings match Redis tuple %q/%d", sessionID, threadID)
			}
			chatGPTConversationID = mapping.ChatGPTConversationID
		}
	}
	if genericMatch && chatGPTConversationID != "" {
		return LegacyThreadFact{}, redisProjectionInvalid("ambiguous known mapping for Redis tuple %q/%d", sessionID, threadID)
	}
	if chatGPTConversationID != "" {
		return LegacyThreadFact{Surface: surface, RecordKey: recordKey, ChatGPTConversationID: chatGPTConversationID}, nil
	}
	return LegacyThreadFact{Surface: surface, RecordKey: recordKey, SessionID: sessionID, LegacyThreadID: threadID}, nil
}

func (receipt RedisInventoryReceipt) CanonicalJSON() ([]byte, error) {
	copy := receipt
	copy.ReceiptSHA256 = ""
	return json.Marshal(copy)
}

func (receipt RedisInventoryReceipt) ComputeSHA256() (string, error) {
	encoded, err := receipt.CanonicalJSON()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func (receipt RedisInventoryReceipt) Validate() error {
	if receipt.SchemaVersion != RedisInventoryReceiptSchemaVersion || receipt.Status != RedisInventoryStatus {
		return errors.New("invalid Redis inventory receipt schema or status")
	}
	if receipt.SourceCount < 0 || receipt.SourceCount > RedisPreparationMaxEntries || receipt.FactCount < 0 || receipt.FactCount > RedisInventoryMaxFacts || receipt.GenericMappingCount < 0 || receipt.ChatGPTMappingCount < 0 {
		return errors.New("invalid Redis inventory receipt counts")
	}
	for _, value := range []string{receipt.SourceSHA256, receipt.MappingSHA256, receipt.ReceiptSHA256} {
		if !validRedisSHA256(value) {
			return errors.New("invalid Redis inventory receipt SHA256")
		}
	}
	computed, err := receipt.ComputeSHA256()
	if err != nil {
		return err
	}
	if computed != receipt.ReceiptSHA256 {
		return errors.New("Redis inventory receipt SHA256 does not match canonical JSON")
	}
	return nil
}

func (result RedisInventoryResult) Validate() error {
	if err := result.Plan.Validate(); err != nil {
		return fmt.Errorf("Redis inventory plan: %w", err)
	}
	if err := result.Receipt.Validate(); err != nil {
		return err
	}
	if result.Receipt.MappingSHA256 != result.Plan.MappingSHA256 || result.Receipt.GenericMappingCount != len(result.Plan.Generic) || result.Receipt.ChatGPTMappingCount != len(result.Plan.ChatGPT) {
		return errors.New("Redis inventory receipt does not match its plan")
	}
	sourceCount := 0
	for _, mapping := range append(append([]ThreadMapping{}, result.Plan.Generic...), result.Plan.ChatGPT...) {
		sourceCount += len(mapping.Sources)
	}
	if sourceCount != result.Receipt.FactCount {
		return errors.New("Redis inventory fact count does not match plan sources")
	}
	return nil
}
