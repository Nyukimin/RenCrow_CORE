package threadmigration

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type sqliteTransformFixture struct {
	plan          Plan
	index         sqliteTransformIndex
	legacySession string
	canonical     string
	chatGPTID     string
	chatSession   string
	chatThread    int64
	threadID      modulecore.ThreadID
	closedID      modulecore.ThreadID
	turnHash      string
}

func newSQLiteTransformFixture(t *testing.T) sqliteTransformFixture {
	t.Helper()
	canonicalSession, err := modulecore.NewMigrationID(modulecore.CanonicalSessionID, "sqlite_transform_test", "session_id", "already-canonical-source")
	if err != nil {
		t.Fatal(err)
	}
	chatGPTID := "chatgpt-transform-conversation"
	chatSession, chatThread, err := chatGPTLegacyTuple(chatGPTID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan([]LegacyThreadFact{
		{Surface: "transform", RecordKey: "generic-9", SessionID: "legacy-transform-session", LegacyThreadID: 9},
		{Surface: "transform", RecordKey: "generic-8", SessionID: "legacy-transform-session", LegacyThreadID: 8},
		{Surface: "transform", RecordKey: "canonical-5", SessionID: canonicalSession, LegacyThreadID: 5},
		{Surface: "transform", RecordKey: "chatgpt", ChatGPTConversationID: chatGPTID},
	})
	if err != nil {
		t.Fatal(err)
	}
	index, err := newSQLiteTransformIndex(plan)
	if err != nil {
		t.Fatal(err)
	}
	thread, ok := plan.LookupGeneric("", 9)
	if ok {
		t.Fatal("unexpected empty-session generic mapping")
	}
	legacyCanonical, err := canonicalGenericSessionID("legacy-transform-session")
	if err != nil {
		t.Fatal(err)
	}
	thread, ok = plan.LookupGeneric(legacyCanonical, 9)
	if !ok {
		t.Fatal("generic thread mapping is missing")
	}
	closed, ok := plan.LookupGeneric(legacyCanonical, 8)
	if !ok {
		t.Fatal("closed generic thread mapping is missing")
	}
	return sqliteTransformFixture{
		plan: plan, index: index, legacySession: "legacy-transform-session", canonical: canonicalSession,
		chatGPTID: chatGPTID, chatSession: chatSession, chatThread: chatThread,
		threadID: thread.ThreadID, closedID: closed.ThreadID, turnHash: strings.Repeat("a", 64),
	}
}

func transformTestReceipt(fixture sqliteTransformFixture, threadID int64, closedID int64, closed bool) legacyReceiptRow {
	return legacyReceiptRow{
		turnID: "turn-transform-1", payloadHash: fixture.turnHash, sessionID: fixture.legacySession,
		traceID: "turn-transform-1", threadID: threadID, closedID: closedID, closed: closed,
		userMessage: "msg-user", agentMessage: "msg-agent", status: "completed",
	}
}

func transformTestResultJSON(row legacyReceiptRow, includeClosed bool) string {
	closed := ""
	if includeClosed {
		closed = fmt.Sprintf(`,"closed_thread_id":%d`, row.closedID)
	}
	return fmt.Sprintf(`{"turn_id":%q,"trace_id":%q,"session_id":%q,"thread_id":%d%s,"user_message_id":%q,"agent_message_id":%q,"message_ids":[%q,%q],"payload_sha256":%q,"status":%q,"error_code":"","requested_targets":["redis_projection"],"pending_targets":[],"completed_targets":["redis_projection"],"idempotent_replay":false}`,
		row.turnID, row.traceID, row.sessionID, row.threadID, closed, row.userMessage, row.agentMessage,
		row.userMessage, row.agentMessage, row.payloadHash, row.status)
}

func TestResolveSQLiteThreadTupleUsesGenericLegacyAndCanonicalSessions(t *testing.T) {
	fixture := newSQLiteTransformFixture(t)
	legacy, err := resolveSQLiteThreadTuple(fixture.index, fixture.legacySession, 9)
	if err != nil {
		t.Fatalf("legacy generic tuple: %v", err)
	}
	if legacy.SessionID == modulecore.SessionID(fixture.legacySession) || legacy.ThreadID != fixture.threadID || legacy.ThreadSeq != 9 || legacy.ThreadKind != modulecore.ThreadKindUserConversation {
		t.Fatalf("legacy generic tuple = %+v", legacy)
	}
	canonical, err := resolveSQLiteThreadTuple(fixture.index, fixture.canonical, 5)
	if err != nil {
		t.Fatalf("canonical generic tuple: %v", err)
	}
	if canonical.SessionID != modulecore.SessionID(fixture.canonical) || canonical.ThreadSeq != 5 {
		t.Fatalf("canonical generic tuple = %+v", canonical)
	}
}

func TestResolveSQLiteThreadTupleUsesExactChatGPTLegacyTuple(t *testing.T) {
	fixture := newSQLiteTransformFixture(t)
	tuple, err := resolveSQLiteThreadTuple(fixture.index, fixture.chatSession, fixture.chatThread)
	if err != nil {
		t.Fatalf("ChatGPT tuple: %v", err)
	}
	mapping, ok := fixture.plan.LookupChatGPT(fixture.chatGPTID)
	if !ok {
		t.Fatal("ChatGPT mapping is missing")
	}
	if tuple.SessionID != mapping.SessionID || tuple.ThreadID != mapping.ThreadID || tuple.ThreadSeq != 1 || tuple.ThreadKind != modulecore.ThreadKindUserConversation {
		t.Fatalf("ChatGPT tuple = %+v, mapping = %+v", tuple, mapping)
	}
	if _, err := resolveSQLiteThreadTuple(fixture.index, fixture.chatSession, fixture.chatThread+1); err == nil {
		t.Fatal("nearby ChatGPT tuple unexpectedly resolved")
	}
}

func TestResolveSQLiteOptionalThreadTuplePreservesZeroAndCanonicalizesParent(t *testing.T) {
	fixture := newSQLiteTransformFixture(t)
	empty, err := resolveSQLiteOptionalThreadTuple(fixture.index, "", 0)
	if err != nil {
		t.Fatalf("empty optional tuple: %v", err)
	}
	if empty != (sqliteCanonicalThreadTuple{}) {
		t.Fatalf("empty optional tuple = %+v", empty)
	}
	parent, err := resolveSQLiteOptionalThreadTuple(fixture.index, fixture.legacySession, 0)
	if err != nil {
		t.Fatalf("parent-only optional tuple: %v", err)
	}
	if parent.SessionID == "" || parent.ThreadID != "" || parent.ThreadSeq != 0 || parent.ThreadKind != "" {
		t.Fatalf("parent-only optional tuple = %+v", parent)
	}
	if _, err := resolveSQLiteOptionalThreadTuple(fixture.index, fixture.legacySession, -1); err == nil {
		t.Fatal("negative optional tuple unexpectedly resolved")
	}
}

func TestTransformLegacyTurnResultGenericClosedTupleAndDomainDecode(t *testing.T) {
	fixture := newSQLiteTransformFixture(t)
	row := transformTestReceipt(fixture, 9, 8, true)
	encoded, err := transformLegacyTurnResult(fixture.index, row, transformTestResultJSON(row, true))
	if err != nil {
		t.Fatalf("transform result: %v", err)
	}
	var result domconv.ConversationTurnResult
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatalf("decode canonical result: %v; JSON=%s", err, encoded)
	}
	if result.TurnID != row.turnID || result.TraceID != row.traceID || result.SessionID != string(mustCanonicalTransformSession(t, fixture.legacySession)) || result.ThreadID != fixture.threadID || result.ThreadSeq != 9 || result.ThreadKind != domconv.ThreadKindUserConversation {
		t.Fatalf("canonical result identity = %+v", result)
	}
	if result.ClosedThreadID != fixture.closedID || result.ClosedThreadSeq != 8 || result.ClosedThreadKind != domconv.ThreadKindUserConversation {
		t.Fatalf("canonical closed identity = id=%q seq=%d kind=%q", result.ClosedThreadID, result.ClosedThreadSeq, result.ClosedThreadKind)
	}
	if result.PayloadSHA256 != row.payloadHash || result.UserMessageID != row.userMessage || result.AgentMessageID != row.agentMessage || len(result.MessageIDs) != 2 || result.RequestedTargets[0] != "redis_projection" || len(result.CompletedTargets) != 1 || result.IdempotentReplay {
		t.Fatalf("nonidentity fields were not preserved: %+v", result)
	}
	assertCanonicalTransformJSON(t, encoded)
}

func TestTransformLegacyTurnResultCanonicalSessionAndNoClosedTuple(t *testing.T) {
	fixture := newSQLiteTransformFixture(t)
	row := transformTestReceipt(fixture, 5, 0, false)
	row.sessionID = fixture.canonical
	row.turnID = "turn-transform-canonical"
	row.traceID = row.turnID
	encodedJSON := fmt.Sprintf(`{"turn_id":%q,"trace_id":%q,"session_id":%q,"thread_id":%d,"user_message_id":%q,"agent_message_id":%q,"message_ids":[%q,%q],"payload_sha256":%q,"status":"completed"}`,
		row.turnID, row.traceID, row.sessionID, row.threadID, row.userMessage, row.agentMessage, row.userMessage, row.agentMessage, row.payloadHash)
	encoded, err := transformLegacyTurnResult(fixture.index, row, encodedJSON)
	if err != nil {
		t.Fatalf("transform canonical-session result: %v", err)
	}
	var result domconv.ConversationTurnResult
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	if result.SessionID != fixture.canonical || result.ThreadID == "" || result.ThreadSeq != 5 || result.ClosedThreadID != "" || result.ClosedThreadSeq != 0 || result.ClosedThreadKind != "" {
		t.Fatalf("canonical-session/no-closed result = %+v", result)
	}
	if strings.Contains(string(encoded), `"closed_thread_id"`) || strings.Contains(string(encoded), `"closed_thread_seq"`) || strings.Contains(string(encoded), `"closed_thread_kind"`) {
		t.Fatalf("empty closed tuple was emitted: %s", encoded)
	}
	assertCanonicalTransformJSON(t, encoded)
}

func TestTransformLegacyTurnResultChatGPTAndIndexReuse(t *testing.T) {
	fixture := newSQLiteTransformFixture(t)
	row := transformTestReceipt(fixture, fixture.chatThread, 0, false)
	row.sessionID = fixture.chatSession
	row.turnID = "turn-transform-chatgpt"
	row.traceID = row.turnID
	encodedJSON := fmt.Sprintf(`{"turn_id":%q,"trace_id":%q,"session_id":%q,"thread_id":%d,"user_message_id":%q,"agent_message_id":%q,"message_ids":[%q,%q],"payload_sha256":%q,"status":"completed"}`,
		row.turnID, row.traceID, row.sessionID, row.threadID, row.userMessage, row.agentMessage, row.userMessage, row.agentMessage, row.payloadHash)
	first, err := transformLegacyTurnResult(fixture.index, row, encodedJSON)
	if err != nil {
		t.Fatalf("first ChatGPT transform: %v", err)
	}
	second, err := transformLegacyTurnResult(fixture.index, row, encodedJSON)
	if err != nil {
		t.Fatalf("second ChatGPT transform: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("same index produced nondeterministic output: %s vs %s", first, second)
	}
	var result domconv.ConversationTurnResult
	if err := json.Unmarshal(first, &result); err != nil {
		t.Fatal(err)
	}
	mapping, _ := fixture.plan.LookupChatGPT(fixture.chatGPTID)
	if result.SessionID != string(mapping.SessionID) || result.ThreadID != mapping.ThreadID || result.ThreadSeq != 1 {
		t.Fatalf("ChatGPT output = %+v; mapping = %+v", result, mapping)
	}
	assertCanonicalTransformJSON(t, first)
}

func TestTransformLegacyOutboxPayloadGenericClosedTuple(t *testing.T) {
	fixture := newSQLiteTransformFixture(t)
	receipt := transformTestReceipt(fixture, 9, 8, true)
	row := sqliteLegacyOutboxRow{
		TurnID: receipt.turnID, Target: "redis_projection", SessionID: receipt.sessionID, ThreadID: receipt.threadID,
		ClosedID: legacyOptionalInt64{Value: receipt.closedID, Valid: true}, PayloadHash: receipt.payloadHash, Receipt: receipt,
		PayloadJSON: fmt.Sprintf(`{"version":%q,"turn_id":%q,"trace_id":%q,"session_id":%q,"owner_id":"owner-1","thread_id":%d,"closed_thread_id":%d,"user_message_id":%q,"agent_message_id":%q,"target":%q,"payload_sha256":%q}`,
			turnPayloadVersion, receipt.turnID, receipt.turnID, receipt.sessionID, receipt.threadID, receipt.closedID,
			receipt.userMessage, receipt.agentMessage, "redis_projection", receipt.payloadHash),
	}
	encoded, err := transformLegacyOutboxPayload(fixture.index, row)
	if err != nil {
		t.Fatalf("transform outbox: %v", err)
	}
	var payload canonicalTurnOutboxPayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("decode canonical outbox payload: %v; JSON=%s", err, encoded)
	}
	if payload.SessionID != string(mustCanonicalTransformSession(t, fixture.legacySession)) || payload.ThreadID != fixture.threadID || payload.ThreadSeq != 9 || payload.ThreadKind != modulecore.ThreadKindUserConversation || payload.ClosedThreadID != fixture.closedID || payload.ClosedThreadSeq != 8 || payload.ClosedThreadKind != modulecore.ThreadKindUserConversation {
		t.Fatalf("canonical outbox identity = %+v", payload)
	}
	if payload.PayloadSHA256 != fixture.turnHash || payload.OwnerID != "owner-1" || payload.Target != "redis_projection" {
		t.Fatalf("canonical outbox nonidentity fields = %+v", payload)
	}
	assertCanonicalTransformJSON(t, encoded)
}

func TestTransformRejectsMissingOrContradictoryMappingsAndSQLJSONMismatch(t *testing.T) {
	fixture := newSQLiteTransformFixture(t)
	row := transformTestReceipt(fixture, 9, 0, false)
	encoded := transformTestResultJSON(row, false)
	missing := row
	missing.threadID = 77
	missingJSON := strings.Replace(encoded, `"thread_id":9`, `"thread_id":77`, 1)
	if output, err := transformLegacyTurnResult(fixture.index, missing, missingJSON); err == nil || output != nil {
		t.Fatalf("missing mapping output=%s err=%v", output, err)
	}
	contradictory := row
	contradictoryJSON := strings.Replace(encoded, fmt.Sprintf(`"session_id":%q`, row.sessionID), `"session_id":"another-session"`, 1)
	if output, err := transformLegacyTurnResult(fixture.index, contradictory, contradictoryJSON); err == nil || output != nil {
		t.Fatalf("SQL/JSON mismatch output=%s err=%v", output, err)
	}
	invalidPlan := fixture.plan
	invalidPlan.Generic = append([]ThreadMapping(nil), fixture.plan.Generic...)
	invalidPlan.Generic[0].ThreadSeq++
	if index, err := newSQLiteTransformIndex(invalidPlan); err == nil || index.ready {
		t.Fatalf("contradictory plan accepted: index=%+v err=%v", index, err)
	}
}

func TestTransformRejectsStrictJSONViolationsAndOversize(t *testing.T) {
	fixture := newSQLiteTransformFixture(t)
	row := transformTestReceipt(fixture, 9, 0, false)
	valid := transformTestResultJSON(row, false)
	cases := []string{
		strings.TrimSuffix(valid, "}") + `,"unknown":1}`,
		strings.Replace(valid, `"thread_id":9`, `"thread_id":9,"thread_id":9`, 1),
		valid + ` {}`,
		`{"turn_id":`,
		strings.TrimSuffix(valid, "}") + fmt.Sprintf(`,"turn_id":%q}`, row.turnID),
		strings.Repeat(" ", maxLegacyResultJSONBytes+1),
		strings.TrimSuffix(valid, "}") + string([]byte{0xff}) + "}",
	}
	for index, input := range cases {
		if output, err := transformLegacyTurnResult(fixture.index, row, input); err == nil || output != nil {
			t.Errorf("case %d accepted output=%s err=%v", index, output, err)
		}
	}
}

func TestTransformLegacyOutboxRejectsStrictJSONAndSQLMismatch(t *testing.T) {
	fixture := newSQLiteTransformFixture(t)
	receipt := transformTestReceipt(fixture, 9, 0, false)
	row := sqliteLegacyOutboxRow{
		TurnID: receipt.turnID, Target: "redis_projection", SessionID: receipt.sessionID, ThreadID: receipt.threadID,
		PayloadHash: receipt.payloadHash, Receipt: receipt,
		PayloadJSON: fmt.Sprintf(`{"version":%q,"turn_id":%q,"trace_id":%q,"session_id":%q,"owner_id":"owner-1","thread_id":9,"user_message_id":%q,"agent_message_id":%q,"target":"redis_projection","payload_sha256":%q}`,
			turnPayloadVersion, receipt.turnID, receipt.turnID, receipt.sessionID, receipt.userMessage, receipt.agentMessage, receipt.payloadHash),
	}
	valid := row.PayloadJSON
	for index, input := range []string{
		strings.TrimSuffix(valid, "}") + `,"unknown":1}`,
		strings.Replace(valid, `"thread_id":9`, `"thread_id":9,"thread_id":9`, 1),
		valid + ` {}`,
		`{"version":`,
		strings.Repeat("x", maxLegacyOutboxPayloadBytes+1),
		strings.TrimSuffix(valid, "}") + string([]byte{0xff}) + "}",
	} {
		candidate := row
		candidate.PayloadJSON = input
		if output, err := transformLegacyOutboxPayload(fixture.index, candidate); err == nil || output != nil {
			t.Errorf("case %d accepted output=%s err=%v", index, output, err)
		}
	}
	candidate := row
	candidate.ThreadID = 8
	if output, err := transformLegacyOutboxPayload(fixture.index, candidate); err == nil || output != nil {
		t.Fatalf("SQL mismatch accepted output=%s err=%v", output, err)
	}
}

func TestNewSQLiteTransformIndexRejectsDuplicateChatGPTLegacyTuple(t *testing.T) {
	conversationID := "duplicate-chatgpt"
	plan, err := BuildPlan([]LegacyThreadFact{{Surface: "chat", RecordKey: "one", ChatGPTConversationID: conversationID}})
	if err != nil {
		t.Fatal(err)
	}
	duplicate := plan.ChatGPT[0]
	duplicate.Sources = append([]ThreadSource(nil), duplicate.Sources...)
	duplicate.RecordKey = "two"
	duplicate.Sources[0].RecordKey = "two"
	plan.ChatGPT = append(plan.ChatGPT, duplicate)
	if index, err := newSQLiteTransformIndex(plan); err == nil || index.ready {
		t.Fatalf("duplicate ChatGPT mapping accepted: index=%+v err=%v", index, err)
	}
}

func TestNewSQLiteTransformIndexRejectsGenericAndChatGPTTupleOverlap(t *testing.T) {
	conversationID := "overlapping-chatgpt"
	legacySession, legacyThread, err := chatGPTLegacyTuple(conversationID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan([]LegacyThreadFact{
		{Surface: "chat", RecordKey: "chat", ChatGPTConversationID: conversationID},
		{Surface: "generic", RecordKey: "generic", SessionID: legacySession, LegacyThreadID: legacyThread},
	})
	if err != nil {
		t.Fatal(err)
	}
	if index, err := newSQLiteTransformIndex(plan); err == nil || index.ready {
		t.Fatalf("generic/ChatGPT tuple overlap accepted: index=%+v err=%v", index, err)
	}
}

func assertCanonicalTransformJSON(t *testing.T, encoded []byte) {
	t.Helper()
	receipt, err := AuditJSONIdentity(encoded)
	if err != nil {
		t.Fatalf("canonical output failed identity audit: %v; JSON=%s", err, encoded)
	}
	for _, occurrence := range receipt.Occurrences {
		if occurrence.Classification == JSONIdentityClassificationLegacyNumeric {
			t.Fatalf("canonical output retained legacy_numeric identity: %+v; JSON=%s", occurrence, encoded)
		}
		if occurrence.Key == JSONIdentityKeyThreadID || occurrence.Key == JSONIdentityKeyClosedThreadID {
			if occurrence.Classification != JSONIdentityClassificationCanonicalThread {
				t.Fatalf("canonical thread ID occurrence is not canonical: %+v", occurrence)
			}
		}
	}
}

func mustCanonicalTransformSession(t *testing.T, source string) modulecore.SessionID {
	t.Helper()
	value, err := canonicalGenericSessionID(source)
	if err != nil {
		t.Fatal(err)
	}
	return modulecore.SessionID(value)
}

func TestSQLiteTransformIndexRejectsUninitializedIndex(t *testing.T) {
	row := legacyReceiptRow{}
	if output, err := transformLegacyTurnResult(sqliteTransformIndex{}, row, "{}"); err == nil || output != nil {
		t.Fatalf("uninitialized index accepted output=%s err=%v", output, err)
	}
	if _, err := resolveSQLiteOptionalThreadTuple(sqliteTransformIndex{}, "", 0); err == nil {
		t.Fatal("uninitialized index resolved optional tuple")
	}
}

func TestCanonicalTransformMarshalRejectsExpandedOutputOverStorageBounds(t *testing.T) {
	fixture := newSQLiteTransformFixture(t)
	result := domconv.ConversationTurnResult{
		TurnID: "turn", TraceID: "turn", SessionID: fixture.canonical,
		ThreadID: fixture.threadID, ThreadSeq: 9, ThreadKind: domconv.ThreadKindUserConversation,
		UserMessageID: strings.Repeat("u", maxLegacyResultJSONBytes), AgentMessageID: "agent",
		PayloadSHA256: fixture.turnHash, Status: domconv.ConversationTurnCompleted,
	}
	if encoded, err := marshalCanonicalTurnResult(result); err == nil || encoded != nil {
		t.Fatalf("oversized canonical result accepted: bytes=%d err=%v", len(encoded), err)
	}
	outbox := canonicalTurnOutboxPayload{
		Version: turnPayloadVersion, TurnID: "turn", TraceID: "turn", SessionID: fixture.canonical,
		OwnerID: strings.Repeat("o", maxLegacyOutboxPayloadBytes), ThreadID: fixture.threadID,
		ThreadSeq: 9, ThreadKind: modulecore.ThreadKindUserConversation, UserMessageID: "user",
		AgentMessageID: "agent", Target: "redis_projection", PayloadSHA256: fixture.turnHash,
	}
	if encoded, err := marshalCanonicalOutboxPayload(outbox); err == nil || encoded != nil {
		t.Fatalf("oversized canonical outbox accepted: bytes=%d err=%v", len(encoded), err)
	}
}
