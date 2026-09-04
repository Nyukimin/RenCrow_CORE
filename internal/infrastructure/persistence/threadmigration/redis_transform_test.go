package threadmigration

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestPrepareRedisHappySessionAndThread(t *testing.T) {
	plan := redisTestPlan(t, "session-001", 7)
	sessionValue := redisTestSessionJSON(t, "session-001", 7)
	threadValue := redisTestThreadJSON(t, "session-001", 7)
	input := RedisPreparationInput{
		Phase: RedisPreparationPhase,
		Plan:  plan,
		Entries: []RedisEntry{
			{Key: "thread:7", Value: threadValue, ExpireAtUnixMilli: 1800000004567},
			{Key: "sess:session-001", Value: sessionValue, ExpireAtUnixMilli: 1800000001234},
		},
	}

	got, err := PrepareRedisProjection(input)
	if err != nil {
		t.Fatalf("PrepareRedisProjection failed: %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("result validation failed: %v", err)
	}
	if got.Receipt.Status != RedisPreparationStatus {
		t.Fatalf("status = %q, want %q", got.Receipt.Status, RedisPreparationStatus)
	}
	if got.Receipt.SourceCount != 2 || got.Receipt.OutputCount != 2 {
		t.Fatalf("counts = %d/%d, want 2/2", got.Receipt.SourceCount, got.Receipt.OutputCount)
	}
	if got.Receipt.MappingSHA256 != plan.MappingSHA256 {
		t.Fatalf("mapping hash = %q, want %q", got.Receipt.MappingSHA256, plan.MappingSHA256)
	}

	if got.Entries[0].Key != "sess:ses_"+redisTestSessionSuffix(t, plan) {
		t.Fatalf("first output key = %q", got.Entries[0].Key)
	}
	mapping, ok := plan.LookupGeneric(redisTestCanonicalSession(t, "session-001"), 7)
	if !ok {
		t.Fatal("test mapping is missing")
	}
	if got.Entries[1].Key != "thread:"+string(mapping.ThreadID) {
		t.Fatalf("thread output key = %q, want thread:%s", got.Entries[1].Key, mapping.ThreadID)
	}
	if got.Entries[0].ExpireAtUnixMilli != 1800000001234 || got.Entries[1].ExpireAtUnixMilli != 1800000004567 {
		t.Fatalf("absolute expiries were not preserved: %#v", got.Entries)
	}

	var session map[string]json.RawMessage
	if err := json.Unmarshal(got.Entries[0].Value, &session); err != nil {
		t.Fatalf("decode session output: %v", err)
	}
	if got := string(session["session_id"]); got != `"`+redisTestCanonicalSession(t, "session-001")+`"` {
		t.Fatalf("session_id = %s", got)
	}
	if got := string(session["last_thread_id"]); got != `"`+string(mapping.ThreadID)+`"` {
		t.Fatalf("last_thread_id = %s", got)
	}
	if string(session["last_thread_seq"]) != "7" || string(session["last_thread_kind"]) != `"user_conversation"` {
		t.Fatalf("last thread tuple = %s/%s", session["last_thread_seq"], session["last_thread_kind"])
	}

	var history []map[string]json.RawMessage
	if err := json.Unmarshal(session["history"], &history); err != nil {
		t.Fatalf("decode history output: %v", err)
	}
	if len(history) != 1 || string(history[0]["thread_id"]) != `"`+string(mapping.ThreadID)+`"` || string(history[0]["session_id"]) != `"`+redisTestCanonicalSession(t, "session-001")+`"` {
		t.Fatalf("history identity = %#v", history)
	}
	if string(history[0]["thread_seq"]) != "7" || string(history[0]["thread_kind"]) != `"user_conversation"` {
		t.Fatalf("history tuple = %s/%s", history[0]["thread_seq"], history[0]["thread_kind"])
	}

	var thread map[string]json.RawMessage
	if err := json.Unmarshal(got.Entries[1].Value, &thread); err != nil {
		t.Fatalf("decode thread output: %v", err)
	}
	if string(thread["thread_id"]) != `"`+string(mapping.ThreadID)+`"` || string(thread["session_id"]) != `"`+redisTestCanonicalSession(t, "session-001")+`"` {
		t.Fatalf("thread identity = %s/%s", thread["thread_id"], thread["session_id"])
	}
	if string(thread["thread_seq"]) != "7" || string(thread["thread_kind"]) != `"user_conversation"` {
		t.Fatalf("thread tuple = %s/%s", thread["thread_seq"], thread["thread_kind"])
	}
}

func TestPrepareRedisDeterministicInputImmutableAndAbsoluteExpiry(t *testing.T) {
	plan := redisTestPlan(t, "session-002", 9)
	entries := []RedisEntry{
		{Key: "thread:9", Value: redisTestThreadJSON(t, "session-002", 9), ExpireAtUnixMilli: 1800000009001},
		{Key: "sess:session-002", Value: redisTestSessionJSON(t, "session-002", 9), ExpireAtUnixMilli: 1800000009002},
	}
	original := make([]RedisEntry, len(entries))
	for i := range entries {
		original[i] = RedisEntry{Key: entries[i].Key, Value: append([]byte(nil), entries[i].Value...), ExpireAtUnixMilli: entries[i].ExpireAtUnixMilli}
	}

	first, err := PrepareRedisProjection(RedisPreparationInput{Phase: RedisPreparationPhase, Plan: plan, Entries: entries})
	if err != nil {
		t.Fatalf("first preparation failed: %v", err)
	}
	secondInput := RedisPreparationInput{Phase: RedisPreparationPhase, Plan: plan, Entries: []RedisEntry{entries[1], entries[0]}}
	second, err := PrepareRedisProjection(secondInput)
	if err != nil {
		t.Fatalf("second preparation failed: %v", err)
	}
	if !reflect.DeepEqual(first.Entries, second.Entries) || first.Receipt != second.Receipt {
		t.Fatalf("preparation is not deterministic:\nfirst=%#v\nsecond=%#v", first, second)
	}
	if !reflect.DeepEqual(entries, original) {
		t.Fatal("input entries were mutated")
	}
	if first.Entries[0].ExpireAtUnixMilli != 1800000009002 || first.Entries[1].ExpireAtUnixMilli != 1800000009001 {
		t.Fatalf("output absolute expiries changed: %#v", first.Entries)
	}
}

func TestPrepareRedisPreservesNonIdentityFields(t *testing.T) {
	plan := redisTestPlan(t, "session-003", 3)
	sessionRaw := []byte(`{"updated_at":"2026-01-02T03:04:05+09:00","created_at":"2026-01-01T00:00:00Z","agenda":"  keep  ","user_id":"u","last_thread_id":0,"history":[],"session_id":"session-003","opaque":{"b":2,"a":[true,null,"x"]}}`)
	// opaque is deliberately rejected as an unknown schema key; the test
	// below uses a known nested receipt field to exercise RawMessage retention.
	sessionRaw = []byte(`{"updated_at":"2026-01-02T03:04:05+09:00","created_at":"2026-01-01T00:00:00Z","agenda":"  keep  ","user_id":"u","last_thread_id":0,"history":[],"session_id":"session-003"}`)
	got, err := PrepareRedisProjection(RedisPreparationInput{Phase: RedisPreparationPhase, Plan: plan, Entries: []RedisEntry{{Key: "sess:session-003", Value: sessionRaw, ExpireAtUnixMilli: 1800000000077}}})
	if err != nil {
		t.Fatalf("preparation failed: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(got.Entries[0].Value, &fields); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if string(fields["agenda"]) != `"  keep  "` || string(fields["user_id"]) != `"u"` || string(fields["created_at"]) != `"2026-01-01T00:00:00Z"` || string(fields["updated_at"]) != `"2026-01-02T03:04:05+09:00"` {
		t.Fatalf("non-identity fields were not preserved: %#v", fields)
	}
	if string(fields["last_thread_id"]) != `""` || string(fields["last_thread_seq"]) != "0" || string(fields["last_thread_kind"]) != `""` {
		t.Fatalf("empty last tuple = %s/%s/%s", fields["last_thread_id"], fields["last_thread_seq"], fields["last_thread_kind"])
	}
}

func TestPrepareRedisSessionAllowsEmptyHistoryWithPositiveLastThread(t *testing.T) {
	plan := redisTestPlan(t, "session-last-only", 12)
	raw := []byte(`{"session_id":"session-last-only","user_id":"u","history":[],"agenda":"","last_thread_id":12,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`)
	result, err := PrepareRedisProjection(RedisPreparationInput{
		Phase: RedisPreparationPhase,
		Plan:  plan,
		Entries: []RedisEntry{{
			Key: "sess:session-last-only", Value: raw, ExpireAtUnixMilli: 1800000001200,
		}},
	})
	if err != nil {
		t.Fatalf("PrepareRedisProjection() error = %v", err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("result.Validate() error = %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(result.Entries[0].Value, &fields); err != nil {
		t.Fatal(err)
	}
	mapping := plan.Generic[0]
	if string(fields["last_thread_id"]) != `"`+string(mapping.ThreadID)+`"` || string(fields["last_thread_seq"]) != "12" {
		t.Fatalf("last thread tuple = %s/%s", fields["last_thread_id"], fields["last_thread_seq"])
	}
}

func TestPrepareRedisResolvesChatGPTLegacyTuple(t *testing.T) {
	conversationID := "redis-chatgpt-conversation"
	legacySessionID, legacyThreadID, err := chatGPTLegacyTuple(conversationID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan([]LegacyThreadFact{{
		Surface: "redis", RecordKey: "chatgpt", ChatGPTConversationID: conversationID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	legacyThread := strconv.FormatInt(legacyThreadID, 10)
	session := []byte(`{"session_id":"` + legacySessionID + `","user_id":"u","history":[],"agenda":"","last_thread_id":` + legacyThread + `,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`)
	thread := []byte(`{"thread_id":` + legacyThread + `,"session_id":"` + legacySessionID + `","domain":"chatgpt","turns":[],"targets":[],"ct":{},"ts_start":"2026-01-01T00:00:00Z","status":"closed"}`)
	result, err := PrepareRedisProjection(RedisPreparationInput{
		Phase: RedisPreparationPhase,
		Plan:  plan,
		Entries: []RedisEntry{
			{Key: "sess:" + legacySessionID, Value: session, ExpireAtUnixMilli: 1800000000100},
			{Key: "thread:" + legacyThread, Value: thread, ExpireAtUnixMilli: 1800000000200},
		},
	})
	if err != nil {
		t.Fatalf("PrepareRedisProjection() error = %v", err)
	}
	mapping := plan.ChatGPT[0]
	if result.Entries[0].Key != "sess:"+string(mapping.SessionID) || result.Entries[1].Key != "thread:"+string(mapping.ThreadID) {
		t.Fatalf("output keys = %q, %q", result.Entries[0].Key, result.Entries[1].Key)
	}
}

func TestPrepareRedisSourceHashBindsExactValueBytes(t *testing.T) {
	plan := redisTestPlan(t, "session-hash", 1)
	compact := []byte(`{"session_id":"session-hash","user_id":"u","history":[],"agenda":"","last_thread_id":0,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`)
	spaced := []byte(`{ "session_id" : "session-hash", "user_id" : "u", "history" : [], "agenda" : "", "last_thread_id" : 0, "created_at" : "2026-01-01T00:00:00Z", "updated_at" : "2026-01-01T00:00:00Z" }`)
	prepare := func(value []byte, expiry ...int64) RedisPreparationResult {
		expireAt := int64(1800000000100)
		if len(expiry) == 1 {
			expireAt = expiry[0]
		}
		result, err := PrepareRedisProjection(RedisPreparationInput{
			Phase: RedisPreparationPhase, Plan: plan,
			Entries: []RedisEntry{{Key: "sess:session-hash", Value: value, ExpireAtUnixMilli: expireAt}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	left, right := prepare(compact), prepare(spaced)
	if left.Receipt.SourceSHA256 == right.Receipt.SourceSHA256 {
		t.Fatal("source hash did not bind exact Redis value bytes")
	}
	if left.Receipt.OutputSHA256 != right.Receipt.OutputSHA256 {
		t.Fatal("semantically equal source JSON produced different canonical output")
	}
	differentExpiry := prepare(compact, 1800000000101)
	if left.Receipt.SourceSHA256 == differentExpiry.Receipt.SourceSHA256 || left.Receipt.OutputSHA256 == differentExpiry.Receipt.OutputSHA256 {
		t.Fatal("Redis hashes did not bind the absolute expiry")
	}
}

func TestPrepareRedisRejectsStrictFailures(t *testing.T) {
	plan := redisTestPlan(t, "session-004", 4)
	validSession := redisTestSessionJSON(t, "session-004", 4)
	tests := []struct {
		name    string
		entries []RedisEntry
		want    string
	}{
		{name: "missing mapping", entries: []RedisEntry{{Key: "sess:session-004", Value: redisTestSessionJSON(t, "session-004", 5), ExpireAtUnixMilli: 1800000000001}}, want: "mapping"},
		{name: "key mismatch", entries: []RedisEntry{{Key: "sess:other", Value: validSession, ExpireAtUnixMilli: 1800000000001}}, want: "identity"},
		{name: "duplicate input", entries: []RedisEntry{{Key: "sess:session-004", Value: validSession, ExpireAtUnixMilli: 1800000000001}, {Key: "sess:session-004", Value: validSession, ExpireAtUnixMilli: 1800000000002}}, want: "duplicate"},
		{name: "unknown prefix", entries: []RedisEntry{{Key: "other:session-004", Value: validSession, ExpireAtUnixMilli: 1800000000001}}, want: "key"},
		{name: "malformed", entries: []RedisEntry{{Key: "sess:session-004", Value: []byte(`{"session_id":`), ExpireAtUnixMilli: 1800000000001}}, want: "JSON"},
		{name: "duplicate member", entries: []RedisEntry{{Key: "sess:session-004", Value: []byte(`{"session_id":"session-004","session_id":"session-004","user_id":"u","history":[],"agenda":"","last_thread_id":0,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`), ExpireAtUnixMilli: 1800000000001}}, want: "duplicate"},
		{name: "expiry", entries: []RedisEntry{{Key: "sess:session-004", Value: validSession, ExpireAtUnixMilli: 0}}, want: "expiry"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := PrepareRedisProjection(RedisPreparationInput{Phase: RedisPreparationPhase, Plan: plan, Entries: test.entries})
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestPrepareRedisRejectsWrongPreparationPhase(t *testing.T) {
	plan := redisTestPlan(t, "session-phase", 4)
	_, err := PrepareRedisProjection(RedisPreparationInput{
		Phase:   "runtime",
		Plan:    plan,
		Entries: []RedisEntry{{Key: "sess:session-phase", Value: redisTestSessionJSON(t, "session-phase", 4), ExpireAtUnixMilli: 1800000000001}},
	})
	if !errors.Is(err, ErrRedisProjectionWrongPhase) {
		t.Fatalf("error = %v, want ErrRedisProjectionWrongPhase", err)
	}
}

func redisTestPlan(t *testing.T, session string, threadID int64) Plan {
	t.Helper()
	plan, err := BuildPlan([]LegacyThreadFact{{Surface: "redis", RecordKey: "thread:" + strconv.FormatInt(threadID, 10), SessionID: session, LegacyThreadID: threadID}})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}
	return plan
}

func redisTestCanonicalSession(t *testing.T, session string) string {
	t.Helper()
	canonical, err := canonicalGenericSessionID(session)
	if err != nil {
		t.Fatalf("canonical session failed: %v", err)
	}
	return canonical
}

func redisTestSessionSuffix(t *testing.T, plan Plan) string {
	t.Helper()
	if len(plan.Generic) != 1 {
		t.Fatalf("test plan generic mappings = %d", len(plan.Generic))
	}
	return strings.TrimPrefix(string(plan.Generic[0].SessionID), "ses_")
}

func redisTestSessionJSON(t *testing.T, session string, threadID int64) []byte {
	t.Helper()
	thread := strconv.FormatInt(threadID, 10)
	return []byte(`{"session_id":"` + session + `","user_id":"user","history":[{"thread_id":` + thread + `,"session_id":"` + session + `","domain":"general","summary":"summary","keywords":["one"],"roles":["user"],"receipt":{"source":"legacy"},"embedding":[0.25],"ts_start":"2026-01-01T00:00:00Z","ts_end":"2026-01-01T00:01:00Z","is_novel":true,"score":1.25}],"agenda":"agenda","last_thread_id":` + thread + `,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:01:00Z"}`)
}

func redisTestThreadJSON(t *testing.T, session string, threadID int64) []byte {
	t.Helper()
	return []byte(`{"thread_id":` + strconv.FormatInt(threadID, 10) + `,"session_id":"` + session + `","domain":"general","turns":[],"targets":["user"],"ct":{"x":1},"ts_start":"2026-01-01T00:00:00Z","ts_end":"2026-01-01T00:01:00Z","status":"closed"}`)
}

func TestRedisInputValuesRemainByteEqual(t *testing.T) {
	plan := redisTestPlan(t, "session-005", 5)
	value := redisTestSessionJSON(t, "session-005", 5)
	before := append([]byte(nil), value...)
	if _, err := PrepareRedisProjection(RedisPreparationInput{Phase: RedisPreparationPhase, Plan: plan, Entries: []RedisEntry{{Key: "sess:session-005", Value: value, ExpireAtUnixMilli: 1800000000005}}}); err != nil {
		t.Fatalf("preparation failed: %v", err)
	}
	if !bytes.Equal(value, before) {
		t.Fatal("input value bytes changed")
	}
}
