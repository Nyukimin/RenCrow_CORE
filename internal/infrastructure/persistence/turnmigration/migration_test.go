package turnmigration

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	_ "modernc.org/sqlite"
)

type legacyFixture struct {
	path           string
	sessionID      string
	threadID       string
	userID         string
	agentID        string
	oldTurn        string
	oldTrace       string
	unlinkedTurn   string
	unlinkedTraceA string
	unlinkedTraceB string
	itemA          string
	itemB          string
	injectionA     string
	injectionB     string
	extraMessageID string
}

func newLegacyFixture(t *testing.T) legacyFixture {
	t.Helper()
	fixture := legacyFixture{
		path:           filepath.Join(t.TempDir(), "legacy.db"),
		sessionID:      string(modulecore.NewSessionID()),
		threadID:       string(modulecore.NewThreadID()),
		userID:         string(modulecore.NewMessageID()),
		agentID:        string(modulecore.NewMessageID()),
		oldTurn:        "legacy-turn-linked",
		oldTrace:       "legacy-trace-linked",
		unlinkedTurn:   "legacy-turn-unlinked",
		unlinkedTraceA: "legacy-trace-unlinked-a",
		unlinkedTraceB: "legacy-trace-unlinked-b",
		itemA:          "opaque-item-a",
		itemB:          "opaque-item-b",
		injectionA:     "opaque-injection-a",
		injectionB:     "opaque-injection-b",
		extraMessageID: string(modulecore.NewMessageID()),
	}
	db, err := sql.Open("sqlite", fixture.path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(legacySchema); err != nil {
		t.Fatalf("create fixture schema: %v", err)
	}
	const payloadHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	resultJSON := legacyResultJSON(fixture.oldTurn, fixture.oldTrace, fixture.sessionID, fixture.userID, fixture.agentID, payloadHash)
	if _, err := db.Exec(`INSERT INTO conversation_turn_receipt(
turn_id, trace_id, payload_sha256, session_id, thread_id, thread_seq, thread_kind,
closed_thread_id, closed_thread_seq, closed_thread_kind, user_message_id, agent_message_id,
status, result_json, created_at, updated_at)
VALUES(?, ?, ?, ?, ?, 1, 'user_conversation', '', 0, '', ?, ?, 'completed', ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		fixture.oldTurn, fixture.oldTrace, payloadHash, fixture.sessionID, fixture.threadID, fixture.userID, fixture.agentID, resultJSON); err != nil {
		t.Fatalf("insert fixture receipt: %v", err)
	}
	legacyPayload := legacyOutboxJSON(fixture.oldTurn, fixture.sessionID, fixture.threadID, fixture.userID, fixture.agentID, payloadHash)
	if _, err := db.Exec(`INSERT INTO conversation_turn_outbox(
turn_id, target, session_id, thread_id, thread_seq, thread_kind,
closed_thread_id, closed_thread_seq, closed_thread_kind, payload_sha256, payload_json,
status, lease_token, lease_expires_at, attempts, last_error, created_at, updated_at)
VALUES(?, 'redis_projection', ?, ?, 1, 'user_conversation', '', 0, '', ?, ?, 'pending', '', NULL, 0, '', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		fixture.oldTurn, fixture.sessionID, fixture.threadID, payloadHash, legacyPayload); err != nil {
		t.Fatalf("insert fixture outbox: %v", err)
	}
	insertRecall := func(traceID, turnID string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO recall_trace(
trace_id, turn_id, owner_id, chat_id, persona, route, user_message_hash, query_text_redacted,
created_at, model_id, prompt_version, recall_policy_version, total_candidates, injected_count,
total_injected_tokens, status)
VALUES(?, ?, 'owner', ?, 'Mio', 'conversation', 'hash', 'query', '2026-01-01T00:00:00Z', 'model', 'prompt', 'policy', 1, 1, 4, 'completed')`,
			traceID, turnID, fixture.sessionID); err != nil {
			t.Fatalf("insert fixture recall %s: %v", traceID, err)
		}
	}
	insertRecall(fixture.oldTrace, fixture.oldTurn)
	insertRecall(fixture.unlinkedTraceA, fixture.unlinkedTurn)
	insertRecall(fixture.unlinkedTraceB, fixture.unlinkedTurn)
	if _, err := db.Exec(`INSERT INTO recall_trace_item(
item_id, trace_id, layer, memory_id, source_id, source_url, source_type, status,
score, relevance, recency, confidence, source_trust, reason, injected, prompt_section,
token_count, sensitivity, memory_state, is_raw_or_summary, retrieved_at, published_at, event_id, summary, kind)
VALUES(?, ?, 'L1', 'memory-a', 'source-a', 'https://example.test/a', 'test', 'included', 0.9, 0.8, 0.7, 0.6, 0.5, 'reason-a', 1, 'context', 4, 'normal', 'observed', 'summary', NULL, NULL, 'event-a', 'summary-a', 'fact'),
(?, ?, 'L1', 'memory-b', 'source-b', 'https://example.test/b', 'test', 'included', 0.4, 0.3, 0.2, 0.1, 0.5, 'reason-b', 0, 'context', 3, 'normal', 'observed', 'raw', NULL, NULL, 'event-b', 'summary-b', 'fact')`,
		fixture.itemA, fixture.unlinkedTraceA, fixture.itemB, fixture.unlinkedTraceB); err != nil {
		t.Fatalf("insert fixture items: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO prompt_injection_event(
injection_id, trace_id, prompt_section, order_index, item_ids, token_count, redaction_level, created_at)
VALUES(?, ?, 'context', 0, ?, 4, 'normal', '2026-01-01T00:00:00Z'),
(?, ?, 'context', 1, ?, 3, 'normal', '2026-01-01T00:00:00Z')`,
		fixture.injectionA, fixture.unlinkedTraceA, `[`+quoteJSON(fixture.itemA)+`]`, fixture.injectionB, fixture.unlinkedTraceB, `[`+quoteJSON(fixture.itemB)+`]`); err != nil {
		t.Fatalf("insert fixture injections: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO l1_memory_event(id, namespace, session_id, thread_id, thread_seq, thread_kind, speaker, message, meta_json, memory_state, layer, source, created_at, updated_at)
VALUES(?, 'conversation', ?, ?, 1, 'user_conversation', 'user', 'hello', ?, 'observed', 'L1', 'test', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
(?, 'conversation', ?, ?, 1, 'user_conversation', 'mio', 'world', ?, 'observed', 'L1', 'test', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
('opaque-event', 'conversation', ?, ?, 1, 'user_conversation', 'system', 'other', '{"legacy":"opaque"}', 'observed', 'L1', 'test', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
(?, 'conversation', ?, ?, 1, 'user_conversation', 'system', 'extra', '{"legacy":"canonical-but-unowned"}', 'observed', 'L1', 'test', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		fixture.userID, fixture.sessionID, fixture.threadID, legacyMessageMeta(fixture.oldTurn, fixture.userID, "test-domain", "user", "user", "mio"), fixture.agentID, fixture.sessionID, fixture.threadID, legacyMessageMeta(fixture.oldTurn, fixture.agentID, "test-domain", "mio", "mio", "user"), fixture.sessionID, fixture.threadID, fixture.extraMessageID, fixture.sessionID, fixture.threadID); err != nil {
		t.Fatalf("insert fixture messages: %v", err)
	}
	return fixture
}

const legacySchema = `
CREATE TABLE l1_memory_event (
 id TEXT PRIMARY KEY, namespace TEXT NOT NULL, session_id TEXT NOT NULL, thread_id TEXT NOT NULL DEFAULT '',
 thread_seq INTEGER NOT NULL DEFAULT 0, thread_kind TEXT NOT NULL DEFAULT '', speaker TEXT NOT NULL DEFAULT '',
 message TEXT NOT NULL DEFAULT '', meta_json TEXT NOT NULL DEFAULT '{}', memory_state TEXT NOT NULL DEFAULT '',
 layer TEXT NOT NULL DEFAULT '', source TEXT NOT NULL DEFAULT '', created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL
);
CREATE TABLE conversation_turn_receipt (
 turn_id TEXT PRIMARY KEY, trace_id TEXT NOT NULL, payload_sha256 TEXT NOT NULL, session_id TEXT NOT NULL,
 thread_id TEXT NOT NULL, thread_seq INTEGER NOT NULL, thread_kind TEXT NOT NULL, closed_thread_id TEXT NOT NULL,
 closed_thread_seq INTEGER NOT NULL, closed_thread_kind TEXT NOT NULL, user_message_id TEXT NOT NULL,
 agent_message_id TEXT NOT NULL, status TEXT NOT NULL, result_json TEXT NOT NULL, created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL
);
CREATE TABLE conversation_turn_outbox (
 turn_id TEXT NOT NULL, target TEXT NOT NULL, session_id TEXT NOT NULL, thread_id TEXT NOT NULL, thread_seq INTEGER NOT NULL,
 thread_kind TEXT NOT NULL, closed_thread_id TEXT NOT NULL, closed_thread_seq INTEGER NOT NULL, closed_thread_kind TEXT NOT NULL,
 payload_sha256 TEXT NOT NULL, payload_json TEXT NOT NULL, status TEXT NOT NULL, lease_token TEXT NOT NULL,
 lease_expires_at TIMESTAMP, attempts INTEGER NOT NULL, last_error TEXT NOT NULL, created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL,
 PRIMARY KEY(turn_id, target)
);
CREATE TABLE recall_trace (
 trace_id TEXT PRIMARY KEY, turn_id TEXT NOT NULL, owner_id TEXT NOT NULL, chat_id TEXT NOT NULL, persona TEXT NOT NULL,
 route TEXT NOT NULL, user_message_hash TEXT NOT NULL, query_text_redacted TEXT NOT NULL, created_at TIMESTAMP NOT NULL,
 model_id TEXT NOT NULL, prompt_version TEXT NOT NULL, recall_policy_version TEXT NOT NULL, total_candidates INTEGER NOT NULL,
 injected_count INTEGER NOT NULL, total_injected_tokens INTEGER NOT NULL, status TEXT NOT NULL
);
CREATE TABLE recall_trace_item (
 item_id TEXT PRIMARY KEY, trace_id TEXT NOT NULL, layer TEXT NOT NULL, memory_id TEXT NOT NULL, source_id TEXT NOT NULL,
 source_url TEXT NOT NULL, source_type TEXT NOT NULL, status TEXT NOT NULL, score REAL NOT NULL, relevance REAL NOT NULL,
 recency REAL NOT NULL, confidence REAL NOT NULL, source_trust REAL NOT NULL, reason TEXT NOT NULL, injected INTEGER NOT NULL,
 prompt_section TEXT NOT NULL, token_count INTEGER NOT NULL, sensitivity TEXT NOT NULL, memory_state TEXT NOT NULL,
 is_raw_or_summary TEXT NOT NULL, retrieved_at TIMESTAMP, published_at TIMESTAMP, event_id TEXT NOT NULL, summary TEXT NOT NULL, kind TEXT NOT NULL
);
CREATE TABLE prompt_injection_event (
 injection_id TEXT PRIMARY KEY, trace_id TEXT NOT NULL, prompt_section TEXT NOT NULL, order_index INTEGER NOT NULL,
 item_ids TEXT NOT NULL, token_count INTEGER NOT NULL, redaction_level TEXT NOT NULL, created_at TIMESTAMP NOT NULL
);`

func quoteJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func legacyMessageMeta(turnID, messageID, domain, speaker, from, to string) string {
	value := map[string]string{
		"domain": domain, "message_id": messageID, "turn_id": turnID,
		"speaker": speaker, "from": from, "to": to,
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func legacyResultJSON(turnID, traceID, sessionID, userID, agentID, payloadHash string) string {
	value := map[string]any{
		"turn_id": turnID, "trace_id": traceID, "session_id": sessionID, "user_message_id": userID,
		"agent_message_id": agentID, "message_ids": []string{userID, agentID}, "payload_sha256": payloadHash,
		"status": "completed",
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func legacyOutboxJSON(turnID, sessionID, threadID, userID, agentID, payloadHash string) string {
	value := map[string]any{
		"version": "rencrow.conversation_turn_outbox.v1", "turn_id": turnID, "trace_id": turnID,
		"session_id": sessionID, "owner_id": "owner", "thread_id": threadID, "thread_seq": 1,
		"thread_kind": "user_conversation", "user_message_id": userID, "agent_message_id": agentID,
		"target": "redis_projection", "payload_sha256": payloadHash,
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func TestRunMigratesExactIdentitiesAndChildren(t *testing.T) {
	fixture := newLegacyFixture(t)
	dryPath := filepath.Join(t.TempDir(), "dry-run.json")
	dryBefore, err := hashDatabaseFile(fixture.path)
	if err != nil {
		t.Fatalf("hash before dry-run: %v", err)
	}
	dry, err := Run(context.Background(), Options{DBPath: fixture.path, ManifestPath: dryPath, Mode: ModeDryRun})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if dry.Status != StatusReady || dry.OutputSHA256 != "" || dry.Before.ExistingMessageIDs != 3 {
		t.Fatalf("dry receipt=%+v", dry)
	}
	if info, err := os.Stat(dryPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("dry-run receipt mode=%v err=%v, want 0600", info, err)
	}
	dryAfter, err := hashDatabaseFile(fixture.path)
	if err != nil || dryBefore != dryAfter {
		t.Fatalf("dry-run mutated database: before=%s after=%s err=%v", dryBefore, dryAfter, err)
	}
	repeat, err := Run(context.Background(), Options{DBPath: fixture.path, ManifestPath: filepath.Join(t.TempDir(), "repeat.json"), Mode: ModeDryRun})
	if err != nil || repeat.PlanSHA256 != dry.PlanSHA256 {
		t.Fatalf("repeated dry-run receipt=%+v err=%v", repeat, err)
	}
	var opaqueMetaBefore, extraMetaBefore string
	if db, err := sql.Open("sqlite", fixture.path); err != nil {
		t.Fatal(err)
	} else {
		if err := db.QueryRow(`SELECT meta_json FROM l1_memory_event WHERE id = 'opaque-event'`).Scan(&opaqueMetaBefore); err != nil {
			db.Close()
			t.Fatalf("read opaque metadata before apply: %v", err)
		}
		if err := db.QueryRow(`SELECT meta_json FROM l1_memory_event WHERE id = ?`, fixture.extraMessageID).Scan(&extraMetaBefore); err != nil {
			db.Close()
			t.Fatalf("read extra message metadata before apply: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}

	applyPath := filepath.Join(t.TempDir(), "apply.json")
	applied, err := Run(context.Background(), Options{DBPath: fixture.path, ManifestPath: applyPath, PriorDryRunManifestPath: dryPath, Mode: ModeApply})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied.Status != StatusApplied || applied.OutputSHA256 == "" || applied.OutputSHA256 == dry.InputSHA256 {
		t.Fatalf("apply receipt=%+v", applied)
	}

	db, err := sql.Open("sqlite", fixture.path)
	if err != nil {
		t.Fatalf("open migrated db: %v", err)
	}
	defer db.Close()
	wantTurn, _ := modulecore.NewMigrationID(modulecore.CanonicalTurnID, "conversation_turn_receipt", "turn_id", fixture.oldTurn)
	wantTrace, _ := modulecore.NewMigrationID(modulecore.CanonicalTraceID, "conversation_turn_receipt", "trace_id", fixture.oldTrace)
	wantRoot, _ := modulecore.NewMigrationID(modulecore.CanonicalTaskID, "conversation_turn_receipt", "turn_id", fixture.oldTurn)
	var gotTurn, gotTrace, gotRoot, gotUser, gotAgent, gotResult string
	if err := db.QueryRow(`SELECT turn_id, trace_id, root_task_id, user_message_id, agent_message_id, result_json FROM conversation_turn_receipt`).Scan(&gotTurn, &gotTrace, &gotRoot, &gotUser, &gotAgent, &gotResult); err != nil {
		t.Fatalf("read migrated receipt: %v", err)
	}
	if gotTurn != wantTurn || gotTrace != wantTrace || gotRoot != wantRoot || gotUser != fixture.userID || gotAgent != fixture.agentID {
		t.Fatalf("migrated receipt identities=%q/%q/%q/%q/%q", gotTurn, gotTrace, gotRoot, gotUser, gotAgent)
	}
	for _, message := range []struct {
		id   string
		want string
	}{
		{id: fixture.userID, want: legacyMessageMeta(wantTurn, fixture.userID, "test-domain", "user", "user", "mio")},
		{id: fixture.agentID, want: legacyMessageMeta(wantTurn, fixture.agentID, "test-domain", "mio", "mio", "user")},
	} {
		var metaJSON string
		if err := db.QueryRow(`SELECT meta_json FROM l1_memory_event WHERE id = ?`, message.id).Scan(&metaJSON); err != nil {
			t.Fatalf("read migrated message %s: %v", message.id, err)
		}
		if metaJSON != message.want {
			t.Fatalf("message %s meta_json=%s, want %s", message.id, metaJSON, message.want)
		}
	}
	var opaqueMetaAfter string
	if err := db.QueryRow(`SELECT meta_json FROM l1_memory_event WHERE id = 'opaque-event'`).Scan(&opaqueMetaAfter); err != nil {
		t.Fatalf("read opaque metadata after apply: %v", err)
	}
	if opaqueMetaAfter != opaqueMetaBefore {
		t.Fatalf("opaque metadata changed from %q to %q", opaqueMetaBefore, opaqueMetaAfter)
	}
	var extraMetaAfter string
	if err := db.QueryRow(`SELECT meta_json FROM l1_memory_event WHERE id = ?`, fixture.extraMessageID).Scan(&extraMetaAfter); err != nil {
		t.Fatalf("read extra message metadata after apply: %v", err)
	}
	if extraMetaAfter != extraMetaBefore {
		t.Fatalf("unowned canonical message metadata changed from %q to %q", extraMetaBefore, extraMetaAfter)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(gotResult), &result); err != nil {
		t.Fatalf("decode migrated result: %v", err)
	}
	for key, want := range map[string]string{"turn_id": wantTurn, "trace_id": wantTrace, "root_task_id": wantRoot, "user_message_id": fixture.userID, "agent_message_id": fixture.agentID} {
		if result[key] != want {
			t.Fatalf("result[%s]=%v, want %s", key, result[key], want)
		}
	}
	var payload string
	if err := db.QueryRow(`SELECT payload_json FROM conversation_turn_outbox`).Scan(&payload); err != nil {
		t.Fatalf("read migrated outbox: %v", err)
	}
	var payloadValue map[string]any
	if err := json.Unmarshal([]byte(payload), &payloadValue); err != nil {
		t.Fatalf("decode migrated outbox: %v", err)
	}
	for key, want := range map[string]string{"version": OutboxPayloadVersion, "turn_id": wantTurn, "trace_id": wantTrace, "root_task_id": wantRoot, "user_message_id": fixture.userID, "agent_message_id": fixture.agentID} {
		if payloadValue[key] != want {
			t.Fatalf("outbox[%s]=%v, want %s", key, payloadValue[key], want)
		}
	}

	wantUnlinkedTurn, _ := modulecore.NewMigrationID(modulecore.CanonicalTurnID, "recall_trace", "turn_id", fixture.unlinkedTurn)
	wantUnlinkedRoot, _ := modulecore.NewMigrationID(modulecore.CanonicalTaskID, "recall_trace", "turn_id", fixture.unlinkedTurn)
	wantUnlinkedTraceA, _ := modulecore.NewMigrationID(modulecore.CanonicalTraceID, "recall_trace", "trace_id", fixture.unlinkedTraceA)
	wantUnlinkedTraceB, _ := modulecore.NewMigrationID(modulecore.CanonicalTraceID, "recall_trace", "trace_id", fixture.unlinkedTraceB)
	rows, err := db.Query(`SELECT trace_id, turn_id, root_task_id FROM recall_trace WHERE turn_id = ? ORDER BY trace_id`, wantUnlinkedTurn)
	if err != nil {
		t.Fatalf("read unlinked recalls: %v", err)
	}
	defer rows.Close()
	var gotUnlinked [][3]string
	for rows.Next() {
		var values [3]string
		if err := rows.Scan(&values[0], &values[1], &values[2]); err != nil {
			t.Fatalf("scan unlinked recall: %v", err)
		}
		gotUnlinked = append(gotUnlinked, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate unlinked recalls: %v", err)
	}
	gotUnlinkedTraces := map[string]bool{}
	for _, row := range gotUnlinked {
		if row[1] != wantUnlinkedTurn || row[2] != wantUnlinkedRoot {
			t.Fatalf("unlinked row=%v", row)
		}
		gotUnlinkedTraces[row[0]] = true
	}
	if len(gotUnlinked) != 2 || !gotUnlinkedTraces[wantUnlinkedTraceA] || !gotUnlinkedTraces[wantUnlinkedTraceB] {
		t.Fatalf("unlinked mappings=%v", gotUnlinked)
	}
	var itemTrace, injectionTrace, itemIDs string
	if err := db.QueryRow(`SELECT trace_id FROM recall_trace_item WHERE item_id = ?`, fixture.itemA).Scan(&itemTrace); err != nil {
		t.Fatalf("read migrated item: %v", err)
	}
	if err := db.QueryRow(`SELECT trace_id, item_ids FROM prompt_injection_event WHERE injection_id = ?`, fixture.injectionA).Scan(&injectionTrace, &itemIDs); err != nil {
		t.Fatalf("read migrated injection: %v", err)
	}
	if itemTrace != wantUnlinkedTraceA || injectionTrace != wantUnlinkedTraceA || itemIDs != `[`+quoteJSON(fixture.itemA)+`]` {
		t.Fatalf("child mapping item=%q injection=%q ids=%q", itemTrace, injectionTrace, itemIDs)
	}
	var messageCount int
	if err := db.QueryRow(`SELECT count(*) FROM l1_memory_event WHERE id LIKE 'msg\_%' ESCAPE '\'`).Scan(&messageCount); err != nil || messageCount != 3 {
		t.Fatalf("message count=%d err=%v", messageCount, err)
	}
	var ownerIndexCount int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_recall_trace_owner_created'`).Scan(&ownerIndexCount); err != nil || ownerIndexCount != 1 {
		t.Fatalf("owner index count=%d err=%v", ownerIndexCount, err)
	}
}

func TestRunRejectsStaleDryRunReceiptWithoutMutation(t *testing.T) {
	fixture := newLegacyFixture(t)
	dryPath := filepath.Join(t.TempDir(), "dry.json")
	if _, err := Run(context.Background(), Options{DBPath: fixture.path, ManifestPath: dryPath, Mode: ModeDryRun}); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	db, err := sql.Open("sqlite", fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE l1_memory_event SET message = 'changed' WHERE id = 'opaque-event'`); err != nil {
		db.Close()
		t.Fatalf("mutate fixture: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := hashDatabaseFile(fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(t.TempDir(), "apply.json")
	receipt, err := Run(context.Background(), Options{DBPath: fixture.path, ManifestPath: manifest, PriorDryRunManifestPath: dryPath, Mode: ModeApply})
	if err == nil || receipt.Status != StatusBlocked || receipt.ErrorCode != "dry_run_receipt_mismatch" {
		t.Fatalf("stale apply receipt=%+v err=%v", receipt, err)
	}
	after, err := hashDatabaseFile(fixture.path)
	if err != nil || before != after {
		t.Fatalf("stale apply mutated database: before=%s after=%s err=%v", before, after, err)
	}
}

func TestRunApplyTruncatesWALBeforeOutputHash(t *testing.T) {
	fixture := newLegacyFixture(t)
	db, err := sql.Open("sqlite", fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
		db.Close()
		t.Fatalf("enable WAL: %v", err)
	}
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		db.Close()
		t.Fatalf("checkpoint fixture: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	dryPath := filepath.Join(t.TempDir(), "dry.json")
	if _, err := Run(context.Background(), Options{DBPath: fixture.path, ManifestPath: dryPath, Mode: ModeDryRun}); err != nil {
		t.Fatalf("WAL dry-run: %v", err)
	}
	applied, err := Run(context.Background(), Options{DBPath: fixture.path, ManifestPath: filepath.Join(t.TempDir(), "apply.json"), PriorDryRunManifestPath: dryPath, Mode: ModeApply})
	if err != nil || applied.Status != StatusApplied || applied.OutputSHA256 == "" {
		t.Fatalf("WAL apply receipt=%+v err=%v", applied, err)
	}
	if _, err := os.Stat(fixture.path + "-wal"); err == nil {
		t.Fatalf("WAL sidecar remains after output snapshot")
	}
}

func TestRunRejectsTurnOnlyRecallMatch(t *testing.T) {
	fixture := newLegacyFixture(t)
	db, err := sql.Open("sqlite", fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE recall_trace SET trace_id = 'legacy-trace-turn-only-mismatch' WHERE trace_id = ?`, fixture.oldTrace); err != nil {
		db.Close()
		t.Fatalf("mutate recall pair: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	receipt, err := Run(context.Background(), Options{DBPath: fixture.path, ManifestPath: filepath.Join(t.TempDir(), "blocked.json"), Mode: ModeDryRun})
	if err == nil || receipt.Status != StatusBlocked || receipt.ErrorCode != "source_invalid" {
		t.Fatalf("turn-only mismatch receipt=%+v err=%v", receipt, err)
	}
}

func TestRunRejectsMalformedAndOversizeResultWithoutMutation(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "malformed", value: "{"},
		{name: "oversize", value: `{"turn_id":"` + strings.Repeat("x", maxResultJSONBytes) + `"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLegacyFixture(t)
			db, err := sql.Open("sqlite", fixture.path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE conversation_turn_receipt SET result_json = ? WHERE turn_id = ?`, test.value, fixture.oldTurn); err != nil {
				db.Close()
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := hashDatabaseFile(fixture.path)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := Run(context.Background(), Options{DBPath: fixture.path, ManifestPath: filepath.Join(t.TempDir(), "blocked.json"), Mode: ModeDryRun})
			if err == nil || receipt.Status != StatusBlocked || receipt.ErrorCode != "source_invalid" {
				t.Fatalf("invalid result receipt=%+v err=%v", receipt, err)
			}
			after, err := hashDatabaseFile(fixture.path)
			if err != nil || before != after {
				t.Fatalf("invalid result mutated database: before=%s after=%s err=%v", before, after, err)
			}
		})
	}
}

func TestRunRejectsMalformedOwnedMessageMetadataWithoutMutation(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *sql.DB, legacyFixture)
	}{
		{name: "malformed", setup: func(t *testing.T, db *sql.DB, fixture legacyFixture) {
			_, err := db.Exec(`UPDATE l1_memory_event SET meta_json = '{}' WHERE id = ?`, fixture.userID)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{name: "missing", setup: func(t *testing.T, db *sql.DB, fixture legacyFixture) {
			_, err := db.Exec(`DELETE FROM l1_memory_event WHERE id = ?`, fixture.userID)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{name: "mismatched", setup: func(t *testing.T, db *sql.DB, fixture legacyFixture) {
			meta := legacyMessageMeta("wrong-turn", fixture.userID, "test-domain", "user", "user", "mio")
			_, err := db.Exec(`UPDATE l1_memory_event SET meta_json = ? WHERE id = ?`, meta, fixture.userID)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{name: "oversize", setup: func(t *testing.T, db *sql.DB, fixture legacyFixture) {
			meta := `{"domain":"` + strings.Repeat("x", maxMessageMetaBytes) + `"}`
			_, err := db.Exec(`UPDATE l1_memory_event SET meta_json = ? WHERE id = ?`, meta, fixture.userID)
			if err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLegacyFixture(t)
			db, err := sql.Open("sqlite", fixture.path)
			if err != nil {
				t.Fatal(err)
			}
			test.setup(t, db, fixture)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := hashDatabaseFile(fixture.path)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := Run(context.Background(), Options{DBPath: fixture.path, ManifestPath: filepath.Join(t.TempDir(), "blocked.json"), Mode: ModeDryRun})
			if err == nil || receipt.Status != StatusBlocked || receipt.ErrorCode != "source_invalid" {
				t.Fatalf("invalid message metadata receipt=%+v err=%v", receipt, err)
			}
			after, err := hashDatabaseFile(fixture.path)
			if err != nil || before != after {
				t.Fatalf("invalid message metadata mutated database: before=%s after=%s err=%v", before, after, err)
			}
		})
	}
}

func TestMessageMetadataUpdateRollsBackAsOneTransaction(t *testing.T) {
	fixture := newLegacyFixture(t)
	plan, err := readPlan(context.Background(), fixture.path)
	if err != nil {
		t.Fatalf("read plan: %v", err)
	}
	if len(plan.messages) != 2 {
		t.Fatalf("planned messages=%d, want 2 receipt-owned rows", len(plan.messages))
	}
	plan.messages[1].oldMetaJSON = "{}"
	db, err := sql.Open("sqlite", fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyMessageMetadataUpdates(context.Background(), tx, plan); err == nil {
		_ = tx.Rollback()
		t.Fatal("metadata update unexpectedly succeeded with stale second row")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var userMeta string
	if err := db.QueryRow(`SELECT meta_json FROM l1_memory_event WHERE id = ?`, fixture.userID).Scan(&userMeta); err != nil {
		t.Fatal(err)
	}
	want := legacyMessageMeta(fixture.oldTurn, fixture.userID, "test-domain", "user", "user", "mio")
	if userMeta != want {
		t.Fatalf("rolled-back user metadata=%q, want %q", userMeta, want)
	}
}

func TestPlanHashIncludesReceiptOwnedMessageMetadata(t *testing.T) {
	fixture := newLegacyFixture(t)
	plan, err := readPlan(context.Background(), fixture.path)
	if err != nil {
		t.Fatalf("read plan: %v", err)
	}
	before := planHash(plan)
	plan.messages[0].oldMetaJSON = strings.Replace(plan.messages[0].oldMetaJSON, "test-domain", "changed-domain", 1)
	after := planHash(plan)
	if before == after {
		t.Fatalf("plan hash did not change after planned message metadata change: %s", before)
	}
}

func TestRunRejectsReceiptOnlyStatusInOutbox(t *testing.T) {
	fixture := newLegacyFixture(t)
	db, err := sql.Open("sqlite", fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE conversation_turn_outbox SET status = 'partial'`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	receipt, err := Run(context.Background(), Options{
		DBPath:       fixture.path,
		ManifestPath: filepath.Join(t.TempDir(), "blocked.json"),
		Mode:         ModeDryRun,
	})
	if err == nil || receipt.Status != StatusBlocked || receipt.ErrorCode != "source_invalid" {
		t.Fatalf("invalid outbox status receipt=%+v err=%v", receipt, err)
	}
}

func TestRunRequiresExplicitApplyReceipt(t *testing.T) {
	fixture := newLegacyFixture(t)
	receipt, err := Run(context.Background(), Options{DBPath: fixture.path, ManifestPath: filepath.Join(t.TempDir(), "blocked.json"), Mode: ModeApply})
	if err == nil || receipt.Status != StatusBlocked || receipt.ErrorCode != "invalid_options" {
		t.Fatalf("missing prior receipt=%+v err=%v", receipt, err)
	}
}

func TestRunRejectsManifestAliasAndDryRunReceiptOverwrite(t *testing.T) {
	fixture := newLegacyFixture(t)
	databaseBefore, err := os.ReadFile(fixture.path)
	if err != nil {
		t.Fatalf("read database before alias attempt: %v", err)
	}
	manifestAlias := filepath.Join(t.TempDir(), "database-alias.json")
	if err := os.Symlink(fixture.path, manifestAlias); err != nil {
		t.Fatalf("create database alias: %v", err)
	}
	receipt, err := Run(context.Background(), Options{DBPath: fixture.path, ManifestPath: manifestAlias, Mode: ModeDryRun})
	if err == nil || receipt.Status != StatusBlocked || receipt.ErrorCode != "invalid_path" {
		t.Fatalf("manifest alias receipt=%+v err=%v", receipt, err)
	}
	databaseAfter, err := os.ReadFile(fixture.path)
	if err != nil {
		t.Fatalf("read database after alias attempt: %v", err)
	}
	if string(databaseAfter) != string(databaseBefore) {
		t.Fatal("manifest alias attempt mutated database")
	}

	dryPath := filepath.Join(t.TempDir(), "dry.json")
	if _, err := Run(context.Background(), Options{DBPath: fixture.path, ManifestPath: dryPath, Mode: ModeDryRun}); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	dryBefore, err := os.ReadFile(dryPath)
	if err != nil {
		t.Fatalf("read dry-run receipt: %v", err)
	}
	receipt, err = Run(context.Background(), Options{
		DBPath:                  fixture.path,
		ManifestPath:            dryPath,
		PriorDryRunManifestPath: dryPath,
		Mode:                    ModeApply,
	})
	if err == nil || receipt.Status != StatusBlocked || receipt.ErrorCode != "invalid_path" {
		t.Fatalf("receipt overwrite receipt=%+v err=%v", receipt, err)
	}
	dryAfter, err := os.ReadFile(dryPath)
	if err != nil {
		t.Fatalf("read dry-run receipt after overwrite attempt: %v", err)
	}
	if string(dryAfter) != string(dryBefore) {
		t.Fatal("apply receipt path overwrote dry-run receipt")
	}
}

func TestApplyPlanRollsBackMidRebuildFailure(t *testing.T) {
	fixture := newLegacyFixture(t)
	plan, err := readPlan(context.Background(), fixture.path)
	if err != nil {
		t.Fatalf("read plan: %v", err)
	}
	inputHash, err := hashDatabaseFile(fixture.path)
	if err != nil {
		t.Fatalf("hash fixture: %v", err)
	}
	plan.receipts = append(plan.receipts, plan.receipts[0])
	if err := applyPlan(context.Background(), fixture.path, plan, inputHash); err == nil {
		t.Fatal("duplicate receipt did not fail during rebuild")
	}
	db, err := sql.Open("sqlite", fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var rootTaskColumns int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('conversation_turn_receipt') WHERE name = 'root_task_id'`).Scan(&rootTaskColumns); err != nil {
		t.Fatalf("inspect rolled-back schema: %v", err)
	}
	if rootTaskColumns != 0 {
		t.Fatalf("failed rebuild retained canonical schema column count=%d", rootTaskColumns)
	}
	var oldReceiptCount int
	if err := db.QueryRow(`SELECT count(*) FROM conversation_turn_receipt WHERE turn_id = ?`, fixture.oldTurn).Scan(&oldReceiptCount); err != nil {
		t.Fatalf("read rolled-back receipt: %v", err)
	}
	if oldReceiptCount != 1 {
		t.Fatalf("failed rebuild lost legacy receipt: count=%d", oldReceiptCount)
	}
}
