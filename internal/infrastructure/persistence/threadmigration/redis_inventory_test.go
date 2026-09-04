package threadmigration

import (
	"strconv"
	"strings"
	"testing"
)

func TestInventoryRedisProjectionBuildsGenericPlanDeterministically(t *testing.T) {
	known, err := BuildPlan(nil)
	if err != nil {
		t.Fatal(err)
	}
	entries := []RedisEntry{
		{Key: "thread:12", Value: redisTestThreadJSON(t, "redis-new", 12), ExpireAtUnixMilli: 1800000000200},
		{Key: "sess:redis-new", Value: redisTestSessionJSON(t, "redis-new", 12), ExpireAtUnixMilli: 1800000000100},
	}
	first, err := InventoryRedisProjection(RedisInventoryInput{Phase: RedisInventoryPhase, KnownPlan: known, Entries: entries})
	if err != nil {
		t.Fatal(err)
	}
	second, err := InventoryRedisProjection(RedisInventoryInput{Phase: RedisInventoryPhase, KnownPlan: known, Entries: []RedisEntry{entries[1], entries[0]}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Plan.MappingSHA256 != second.Plan.MappingSHA256 || first.Receipt != second.Receipt {
		t.Fatalf("inventory is not deterministic: first=%+v second=%+v", first.Receipt, second.Receipt)
	}
	if len(first.Plan.Generic) != 1 || len(first.Plan.Generic[0].Sources) != 3 || first.Receipt.FactCount != 3 {
		t.Fatalf("plan/receipt = %+v / %+v", first.Plan, first.Receipt)
	}
	if err := first.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestInventoryRedisProjectionRecognizesKnownChatGPTTuple(t *testing.T) {
	conversationID := "redis-inventory-chatgpt"
	known, err := BuildPlan([]LegacyThreadFact{{Surface: "sqlite", RecordKey: "raw", ChatGPTConversationID: conversationID}})
	if err != nil {
		t.Fatal(err)
	}
	sessionID, threadID, err := chatGPTLegacyTuple(conversationID)
	if err != nil {
		t.Fatal(err)
	}
	thread := strconv.FormatInt(threadID, 10)
	raw := []byte(`{"thread_id":` + thread + `,"session_id":"` + sessionID + `","domain":"chatgpt","turns":[],"targets":[],"ct":{},"ts_start":"2026-01-01T00:00:00Z","status":"closed"}`)
	result, err := InventoryRedisProjection(RedisInventoryInput{
		Phase: RedisInventoryPhase, KnownPlan: known,
		Entries: []RedisEntry{{Key: "thread:" + thread, Value: raw, ExpireAtUnixMilli: 1800000000010}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Plan.ChatGPT) != 1 || len(result.Plan.Generic) != 0 || result.Plan.ChatGPT[0].ChatGPTConversationID != conversationID {
		t.Fatalf("unexpected plan: %+v", result.Plan)
	}
}

func TestInventoryRedisProjectionRejectsKnownAmbiguity(t *testing.T) {
	conversationID := "redis-inventory-ambiguous"
	sessionID, threadID, err := chatGPTLegacyTuple(conversationID)
	if err != nil {
		t.Fatal(err)
	}
	known, err := BuildPlan([]LegacyThreadFact{
		{Surface: "sqlite", RecordKey: "chatgpt", ChatGPTConversationID: conversationID},
		{Surface: "sqlite", RecordKey: "generic", SessionID: sessionID, LegacyThreadID: threadID},
	})
	if err != nil {
		t.Fatal(err)
	}
	thread := strconv.FormatInt(threadID, 10)
	raw := []byte(`{"thread_id":` + thread + `,"session_id":"` + sessionID + `","domain":"x","turns":[],"targets":[],"ct":{},"ts_start":"2026-01-01T00:00:00Z","status":"closed"}`)
	_, err = InventoryRedisProjection(RedisInventoryInput{Phase: RedisInventoryPhase, KnownPlan: known, Entries: []RedisEntry{{Key: "thread:" + thread, Value: raw, ExpireAtUnixMilli: 1800000000001}}})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("error = %v, want ambiguity", err)
	}
}

func TestInventoryRedisProjectionRejectsWrongPhaseAndMalformedValue(t *testing.T) {
	known, err := BuildPlan(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InventoryRedisProjection(RedisInventoryInput{Phase: "runtime", KnownPlan: known}); err == nil || !strings.Contains(err.Error(), "phase") {
		t.Fatalf("wrong phase error = %v", err)
	}
	if _, err := InventoryRedisProjection(RedisInventoryInput{
		Phase: RedisInventoryPhase, KnownPlan: known,
		Entries: []RedisEntry{{Key: "sess:x", Value: []byte(`{"session_id":`), ExpireAtUnixMilli: 1800000000001}},
	}); err == nil || !strings.Contains(err.Error(), "JSON") {
		t.Fatalf("malformed error = %v", err)
	}
}
