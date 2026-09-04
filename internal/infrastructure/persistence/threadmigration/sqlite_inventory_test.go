package threadmigration

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	_ "modernc.org/sqlite"
)

var inventoryFixtureSequence uint64

type sqliteInventoryFixture struct {
	l1          *sql.DB
	archive     *sql.DB
	genericID   string
	chatGPTID   string
	chatSession string
	chatThread  int64
	turnSession string
	turnID      string
}

func newSQLiteInventoryFixture(t *testing.T, insertionOrder ...string) sqliteInventoryFixture {
	t.Helper()
	sequence := atomic.AddUint64(&inventoryFixtureSequence, 1)
	base := "file:threadmigration_inventory_" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) + fmt.Sprintf("_%d", sequence)
	l1 := openInventoryTestDB(t, base+"_l1?mode=memory&cache=shared")
	archive := openInventoryTestDB(t, base+"_archive?mode=memory&cache=shared")
	createLegacyL1Schema(t, l1)
	createLegacyArchiveSchema(t, archive)

	chatConversationID := "conv-identity"
	chatSession, chatThread, err := chatGPTLegacyTuple(chatConversationID)
	if err != nil {
		t.Fatal(err)
	}
	turnSession := migrationInventorySession(t, "turn-session")
	turnID := "turn-1"
	payloadHash := strings.Repeat("a", 64)
	resultJSON := fmt.Sprintf(`{"turn_id":%q,"trace_id":%q,"session_id":%q,"thread_id":9,"closed_thread_id":8,"user_message_id":"msg-user","agent_message_id":"msg-agent","message_ids":["msg-user","msg-agent"],"payload_sha256":%q,"status":"completed","requested_targets":["redis_projection"]}`,
		turnID, turnID, turnSession, payloadHash)
	outboxJSON := fmt.Sprintf(`{"version":%q,"turn_id":%q,"trace_id":%q,"session_id":%q,"owner_id":"owner-1","thread_id":9,"closed_thread_id":8,"user_message_id":"msg-user","agent_message_id":"msg-agent","target":"redis_projection","payload_sha256":%q}`,
		turnPayloadVersion, turnID, turnID, turnSession, payloadHash)

	// The table queries in the implementation are ordered, so fixture order is
	// intentionally configurable to prove that insertion order is irrelevant.
	if len(insertionOrder) == 0 {
		insertionOrder = []string{"l1", "archive"}
	}
	for _, store := range insertionOrder {
		switch store {
		case "l1":
			execInventory(t, l1, `INSERT INTO l1_memory_event (id, namespace, session_id, thread_id, speaker, message, meta_json, memory_state, layer, source, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				"event-generic", "session:legacy", "legacy-session", 7, "user", "generic", `{}`, "observed", "L1", "", "2026-01-01", "2026-01-01")
			execInventory(t, l1, `INSERT INTO l1_memory_event (id, namespace, session_id, thread_id, speaker, message, meta_json, memory_state, layer, source, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				"event-chatgpt", "conv:chatgpt", chatSession, chatThread, "user", "chat", fmt.Sprintf(`{"conversation_id":%q}`, chatConversationID), "observed", "L3", legacySourceChatGPT, "2026-01-02", "2026-01-02")
			execInventory(t, l1, `INSERT INTO l1_memory_event (id, namespace, session_id, thread_id, speaker, message, meta_json, memory_state, layer, source, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				"event-zero-empty", "session:unthreaded", "", 0, "system", "unthreaded", `{}`, "observed", "L1", "", "2026-01-04", "2026-01-04")
			execInventory(t, l1, `INSERT INTO l1_memory_event (id, namespace, session_id, thread_id, speaker, message, meta_json, memory_state, layer, source, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				"event-zero-session", "session:unthreaded", "unthreaded-session", 0, "assistant", "unthreaded", `{}`, "observed", "L1", "", "2026-01-05", "2026-01-05")
			execInventory(t, l1, `INSERT INTO l1_memory_event (id, namespace, session_id, thread_id, speaker, message, meta_json, memory_state, layer, source, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				"event-zero-canonical", "session:unthreaded", turnSession, 0, "system", "unthreaded", `{}`, "observed", "L1", "", "2026-01-06", "2026-01-06")
			execInventory(t, l1, `INSERT INTO l1_event_log (id, event_type, namespace, session_id, thread_id, payload_json, source, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				"log-generic", "memory", "session:legacy", "legacy-session", 7, `{}`, "", "2026-01-01")
			execInventory(t, l1, `INSERT INTO l1_event_log (id, event_type, namespace, session_id, thread_id, payload_json, source, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				"log-chatgpt", "memory", "session:chatgpt", chatSession, chatThread, `{}`, legacySourceChatGPT, "2026-01-02")
			execInventory(t, l1, `INSERT INTO l1_event_log (id, event_type, namespace, session_id, thread_id, payload_json, source, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				"log-zero-empty", "memory", "session:unthreaded", "", 0, `{}`, "", "2026-01-04")
			execInventory(t, l1, `INSERT INTO l1_event_log (id, event_type, namespace, session_id, thread_id, payload_json, source, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				"log-zero-session", "memory", "session:unthreaded", "unthreaded-session", 0, `{}`, "", "2026-01-05")
			execInventory(t, l1, `INSERT INTO l1_event_log (id, event_type, namespace, session_id, thread_id, payload_json, source, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				"log-zero-canonical", "memory", "session:unthreaded", turnSession, 0, `{}`, "", "2026-01-06")
			execInventory(t, l1, `INSERT INTO l1_profile_promotion_job (evidence_event_id, session_id, thread_id, state, attempt_count, lease_token, lease_expires_at, next_attempt_at, last_error, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				"event-generic", "legacy-session", 7, "pending", 0, "", nil, nil, "", "2026-01-01", "2026-01-01")
			execInventory(t, l1, `INSERT INTO conversation_active_thread (session_id, thread_id, domain, message_count, updated_at) VALUES (?, ?, ?, ?, ?)`,
				"legacy-session", 7, "general", 1, "2026-01-01")
			execInventory(t, l1, `INSERT INTO conversation_turn_receipt (turn_id, payload_sha256, session_id, trace_id, thread_id, closed_thread_id, user_message_id, agent_message_id, status, result_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				turnID, payloadHash, turnSession, turnID, 9, 8, "msg-user", "msg-agent", "completed", resultJSON, "2026-01-03", "2026-01-03")
			execInventory(t, l1, `INSERT INTO conversation_turn_outbox (turn_id, target, session_id, thread_id, closed_thread_id, payload_sha256, payload_json, status, lease_token, lease_expires_at, attempts, last_error, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				turnID, "redis_projection", turnSession, 9, 8, payloadHash, outboxJSON, "pending", "", nil, 0, "", "2026-01-03", "2026-01-03")
			execInventory(t, l1, `CREATE TABLE l1_raw_record (source_record_id TEXT, source_type TEXT, thread_id TEXT)`)
			for _, sourceRecordID := range []string{"event-chatgpt", "archive-chatgpt"} {
				execInventory(t, l1, `INSERT INTO l1_raw_record (source_record_id, source_type, thread_id) VALUES (?, ?, ?)`, sourceRecordID, legacySourceChatGPT, chatConversationID)
			}
		case "archive":
			execInventory(t, archive, `INSERT INTO session_thread (thread_id, session_id, ts_start, ts_end, domain, summary, keywords, embedding, is_novel, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				7, "legacy-session", "2026-01-01", nil, "general", "summary", nil, nil, nil, "2026-01-01")
			execInventory(t, archive, `INSERT INTO conversation_thread_summary_receipt (thread_id, schema_version, generation_mode, provider, failure_code, evidence_sha256, source_turn_count, roles_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				7, "v1", "deterministic", "test", "", strings.Repeat("b", 64), 1, `["user"]`, "2026-01-01")
			execInventory(t, archive, `INSERT INTO l1_memory_event_archive (id, namespace, session_id, thread_id, speaker, message, meta_json, memory_state, layer, source, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				"archive-generic", "session:legacy", "legacy-session", 7, "user", "archived", `{}`, "observed", "L1", "", "2026-01-01", "2026-01-01")
			execInventory(t, archive, `INSERT INTO l1_memory_event_archive (id, namespace, session_id, thread_id, speaker, message, meta_json, memory_state, layer, source, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				"archive-chatgpt", "conv:chatgpt", chatSession, chatThread, "user", "chat", fmt.Sprintf(`{"conversation_id":%q}`, chatConversationID), "observed", "L3", legacySourceChatGPT, "2026-01-02", "2026-01-02")
			execInventory(t, archive, `INSERT INTO l1_memory_event_archive (id, namespace, session_id, thread_id, speaker, message, meta_json, memory_state, layer, source, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				"archive-unbound", "system", "unbound-session", 0, "system", "unbound", `{}`, "observed", "L1", "", "2026-01-03", "2026-01-03")
		}
	}
	return sqliteInventoryFixture{l1: l1, archive: archive, genericID: "legacy-session", chatGPTID: chatConversationID, chatSession: chatSession, chatThread: chatThread, turnSession: turnSession, turnID: turnID}
}

func openInventoryTestDB(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func execInventory(t *testing.T, db *sql.DB, query string, args ...interface{}) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("fixture SQL %q: %v", query, err)
	}
}

func createLegacyL1Schema(t *testing.T, db *sql.DB) {
	t.Helper()
	statements := []string{
		`CREATE TABLE l1_memory_event (
			id TEXT PRIMARY KEY,
			namespace TEXT NOT NULL,
			session_id TEXT NOT NULL,
			thread_id INTEGER NOT NULL,
			speaker TEXT NOT NULL,
			message TEXT NOT NULL,
			meta_json TEXT NOT NULL DEFAULT '{}',
			memory_state TEXT NOT NULL,
			layer TEXT NOT NULL,
			source TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		)`,
		`CREATE TABLE l1_event_log (
			id TEXT PRIMARY KEY,
			event_type TEXT NOT NULL,
			namespace TEXT NOT NULL,
			session_id TEXT NOT NULL DEFAULT '',
			thread_id INTEGER NOT NULL DEFAULT 0,
			payload_json TEXT NOT NULL DEFAULT '{}',
			source TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL
		)`,
		`CREATE TABLE l1_profile_promotion_job (
			evidence_event_id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			thread_id INTEGER NOT NULL,
			state TEXT NOT NULL,
			attempt_count INTEGER NOT NULL DEFAULT 0,
			lease_token TEXT NOT NULL DEFAULT '',
			lease_expires_at TIMESTAMP,
			next_attempt_at TIMESTAMP,
			last_error TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		)`,
		`CREATE TABLE conversation_active_thread (
			session_id TEXT PRIMARY KEY,
			thread_id INTEGER NOT NULL,
			domain TEXT NOT NULL,
			message_count INTEGER NOT NULL,
			updated_at TIMESTAMP NOT NULL
		)`,
		`CREATE TABLE conversation_turn_receipt (
			turn_id TEXT PRIMARY KEY,
			payload_sha256 TEXT NOT NULL,
			session_id TEXT NOT NULL,
			trace_id TEXT NOT NULL,
			thread_id INTEGER NOT NULL,
			closed_thread_id INTEGER,
			user_message_id TEXT NOT NULL,
			agent_message_id TEXT NOT NULL,
			status TEXT NOT NULL,
			result_json TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		)`,
		`CREATE TABLE conversation_turn_outbox (
			turn_id TEXT NOT NULL,
			target TEXT NOT NULL,
			session_id TEXT NOT NULL,
			thread_id INTEGER NOT NULL,
			closed_thread_id INTEGER,
			payload_sha256 TEXT NOT NULL,
			payload_json TEXT NOT NULL,
			status TEXT NOT NULL,
			lease_token TEXT NOT NULL DEFAULT '',
			lease_expires_at TIMESTAMP,
			attempts INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			PRIMARY KEY(turn_id, target),
			FOREIGN KEY(turn_id) REFERENCES conversation_turn_receipt(turn_id)
		)`,
	}
	for _, statement := range statements {
		execInventory(t, db, statement)
	}
}

func createLegacyArchiveSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	statements := []string{
		`CREATE TABLE session_thread (
			thread_id BIGINT PRIMARY KEY,
			session_id VARCHAR NOT NULL,
			ts_start TIMESTAMP NOT NULL,
			ts_end TIMESTAMP,
			domain VARCHAR,
			summary TEXT,
			keywords TEXT,
			embedding TEXT,
			is_novel BOOLEAN,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE conversation_thread_summary_receipt (
			thread_id BIGINT PRIMARY KEY,
			schema_version TEXT NOT NULL,
			generation_mode TEXT NOT NULL,
			provider TEXT NOT NULL,
			failure_code TEXT NOT NULL,
			evidence_sha256 TEXT NOT NULL,
			source_turn_count INTEGER NOT NULL,
			roles_json TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL
		)`,
		`CREATE TABLE l1_memory_event_archive (
			id VARCHAR PRIMARY KEY,
			namespace VARCHAR NOT NULL,
			session_id VARCHAR NOT NULL,
			thread_id BIGINT NOT NULL,
			speaker VARCHAR NOT NULL,
			message TEXT NOT NULL,
			meta_json TEXT NOT NULL,
			memory_state VARCHAR NOT NULL,
			layer VARCHAR NOT NULL,
			source VARCHAR NOT NULL,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		)`,
	}
	for _, statement := range statements {
		execInventory(t, db, statement)
	}
}

func migrationInventorySession(t *testing.T, seed string) string {
	t.Helper()
	value, err := modulecore.NewMigrationID(modulecore.CanonicalSessionID, "sqlite_inventory_test", "session_id", seed)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestInventorySQLiteConvergesGenericAndChatGPTAcrossEveryLegacySurface(t *testing.T) {
	fixture := newSQLiteInventoryFixture(t)
	beforeL1 := sqliteInventoryDataVersion(t, fixture.l1)
	beforeArchive := sqliteInventoryDataVersion(t, fixture.archive)
	result, err := InventorySQLite(context.Background(), SQLiteInventoryInput{L1DB: fixture.l1, ArchiveDB: fixture.archive})
	if err != nil {
		t.Fatalf("InventorySQLite() error = %v", err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("result Validate() error = %v", err)
	}
	if after := sqliteInventoryDataVersion(t, fixture.l1); after != beforeL1 {
		t.Fatalf("L1 data_version changed from %d to %d", beforeL1, after)
	}
	if after := sqliteInventoryDataVersion(t, fixture.archive); after != beforeArchive {
		t.Fatalf("archive data_version changed from %d to %d", beforeArchive, after)
	}

	generic, ok := result.Plan.LookupBySource(l1MemoryEventSurface, "event-generic")
	if !ok {
		t.Fatal("generic event was not indexed")
	}
	for _, source := range []struct{ surface, key string }{
		{l1EventLogSurface, "log-generic"},
		{l1ProfilePromotionSurface, "event-generic"},
		{activeThreadSurface, "legacy-session"},
		{sessionThreadSurface, "7"},
		{threadSummaryReceiptSurface, "7"},
		{l1MemoryEventArchiveSurface, "archive-generic"},
	} {
		mapping, found := result.Plan.LookupBySource(source.surface, source.key)
		if !found || mapping.ThreadID != generic.ThreadID || mapping.SessionID != generic.SessionID || mapping.ThreadSeq != 7 {
			t.Fatalf("generic lookup %s/%s = %+v, found=%v; want converged mapping %+v", source.surface, source.key, mapping, found, generic)
		}
	}
	for _, source := range []struct{ surface, key string }{
		{l1MemoryEventSurface, "event-zero-empty"},
		{l1MemoryEventSurface, "event-zero-session"},
		{l1MemoryEventSurface, "event-zero-canonical"},
		{l1EventLogSurface, "log-zero-empty"},
		{l1EventLogSurface, "log-zero-session"},
		{l1EventLogSurface, "log-zero-canonical"},
		{l1MemoryEventArchiveSurface, "archive-unbound"},
	} {
		if _, found := result.Plan.LookupBySource(source.surface, source.key); found {
			t.Fatalf("optional-zero source %s/%s unexpectedly received a Thread mapping", source.surface, source.key)
		}
	}
	if closed, found := result.Plan.LookupBySource(turnReceiptSurface, closedThreadKey(fixture.turnID)); !found || closed.ThreadSeq != 8 || closed.SessionID != modulecore.SessionID(fixture.turnSession) {
		t.Fatalf("closed receipt mapping = %+v, found=%v", closed, found)
	}
	if got, found := result.Plan.LookupGeneric(fixture.turnSession, 9); !found || got.ThreadID == generic.ThreadID {
		t.Fatalf("canonical turn mapping = %+v, found=%v", got, found)
	}

	chat, found := result.Plan.LookupChatGPT(fixture.chatGPTID)
	if !found || len(result.Plan.ChatGPT) != 1 || len(result.Plan.Generic) == 0 {
		t.Fatalf("ChatGPT plan = %+v, found=%v", result.Plan.ChatGPT, found)
	}
	if chat.ChatGPTConversationID != fixture.chatGPTID || chat.SemanticKey != fixture.chatGPTID || chat.ThreadSeq != 1 || chat.ThreadKind != modulecore.ThreadKindUserConversation {
		t.Fatalf("ChatGPT mapping = %+v", chat)
	}
	wantChatSession, err := modulecore.NewMigrationID(modulecore.CanonicalSessionID, "l1_raw_record", "session_id", fixture.chatGPTID)
	if err != nil {
		t.Fatal(err)
	}
	wantChatThread, err := modulecore.NewMigrationID(modulecore.CanonicalThreadID, "l1_raw_record", "thread_id", fixture.chatGPTID)
	if err != nil {
		t.Fatal(err)
	}
	if string(chat.SessionID) != wantChatSession || string(chat.ThreadID) != wantChatThread {
		t.Fatalf("ChatGPT mapping IDs = session=%q thread=%q; want session=%q thread=%q", chat.SessionID, chat.ThreadID, wantChatSession, wantChatThread)
	}
	for _, source := range []struct{ surface, key string }{
		{l1MemoryEventSurface, "event-chatgpt"},
		{l1EventLogSurface, "log-chatgpt"},
		{l1MemoryEventArchiveSurface, "archive-chatgpt"},
	} {
		mapping, found := result.Plan.LookupBySource(source.surface, source.key)
		if !found || mapping.ThreadID != chat.ThreadID || mapping.SessionID != chat.SessionID || mapping.ChatGPTConversationID != fixture.chatGPTID {
			t.Fatalf("ChatGPT lookup %s/%s = %+v, found=%v; want %+v", source.surface, source.key, mapping, found, chat)
		}
	}
	for _, mapping := range append(append([]ThreadMapping{}, result.Plan.Generic...), result.Plan.ChatGPT...) {
		for _, source := range mapping.Sources {
			if source.Surface == "l1_raw_record" {
				t.Fatal("l1_raw_record.thread_id was incorrectly emitted as a canonical mapping source")
			}
		}
	}
	for surface, expected := range map[string]int64{
		l1MemoryEventSurface:        3,
		l1EventLogSurface:           3,
		l1MemoryEventArchiveSurface: 1,
	} {
		zero, found := result.Receipt.OptionalZeroCount(surface)
		if !found || zero.Count != expected {
			t.Fatalf("optional zero receipt for %q = %+v, found=%v; want %d", surface, zero, found, expected)
		}
	}
	if len(result.Receipt.OptionalZeroCounts) != len(legacyOptionalZeroSurfaces) {
		t.Fatalf("optional zero receipt surfaces = %d, want %d", len(result.Receipt.OptionalZeroCounts), len(legacyOptionalZeroSurfaces))
	}
	archiveCount, found := result.Receipt.SurfaceCount(l1MemoryEventArchiveSurface)
	if !found || archiveCount.Rows != 3 || archiveCount.References != 2 {
		t.Fatalf("archive event receipt count = %+v, found=%v", archiveCount, found)
	}
	for surface, wantRows := range map[string]int64{
		l1MemoryEventSurface: 5,
		l1EventLogSurface:    5,
	} {
		count, found := result.Receipt.SurfaceCount(surface)
		if !found || count.Rows != wantRows || count.References != 2 {
			t.Fatalf("%s receipt count = %+v, found=%v; want rows=%d references=2", surface, count, found, wantRows)
		}
	}
	if len(result.Receipt.SourceSchemaFingerprints) != 9 {
		t.Fatalf("schema fingerprints = %d, want 9", len(result.Receipt.SourceSchemaFingerprints))
	}
}

func TestInventorySQLiteIsDeterministicAcrossInsertionOrderAndRepeatedReads(t *testing.T) {
	first := newSQLiteInventoryFixture(t, "l1", "archive")
	second := newSQLiteInventoryFixture(t, "archive", "l1")
	firstResult, err := InventorySQLite(context.Background(), SQLiteInventoryInput{L1DB: first.l1, ArchiveDB: first.archive})
	if err != nil {
		t.Fatalf("first InventorySQLite() error = %v", err)
	}
	secondResult, err := InventorySQLite(context.Background(), SQLiteInventoryInput{L1DB: second.l1, ArchiveDB: second.archive})
	if err != nil {
		t.Fatalf("second InventorySQLite() error = %v", err)
	}
	firstPlanJSON, err := firstResult.Plan.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	secondPlanJSON, err := secondResult.Plan.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	firstReceiptJSON, err := firstResult.Receipt.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	secondReceiptJSON, err := secondResult.Receipt.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(firstPlanJSON, secondPlanJSON) || firstResult.Plan.MappingSHA256 != secondResult.Plan.MappingSHA256 || !reflect.DeepEqual(firstReceiptJSON, secondReceiptJSON) || firstResult.Receipt.ReceiptSHA256 != secondResult.Receipt.ReceiptSHA256 {
		t.Fatalf("inventory changed with insertion order:\nfirst plan=%s\nsecond plan=%s\nfirst receipt=%s\nsecond receipt=%s", firstPlanJSON, secondPlanJSON, firstReceiptJSON, secondReceiptJSON)
	}
	repeated, err := InventorySQLite(context.Background(), SQLiteInventoryInput{L1DB: first.l1, ArchiveDB: first.archive})
	if err != nil {
		t.Fatalf("repeated InventorySQLite() error = %v", err)
	}
	if repeated.Receipt.ReceiptSHA256 != firstResult.Receipt.ReceiptSHA256 || repeated.Plan.MappingSHA256 != firstResult.Plan.MappingSHA256 {
		t.Fatalf("repeated inventory hashes changed: first=%+v repeated=%+v", firstResult, repeated)
	}
}

func TestInventorySQLiteResolvesChatGPTEventLogFromArchiveMetadata(t *testing.T) {
	first := newSQLiteInventoryFixture(t)
	second := newSQLiteInventoryFixture(t)
	const conversationID = "archive-only-conversation"
	sessionID, threadID, err := chatGPTLegacyTuple(conversationID)
	if err != nil {
		t.Fatal(err)
	}
	insertArchiveOnlyChatGPT := func(fixture sqliteInventoryFixture, archiveFirst bool) {
		if archiveFirst {
			execInventory(t, fixture.archive, `INSERT INTO l1_memory_event_archive (id, namespace, session_id, thread_id, speaker, message, meta_json, memory_state, layer, source, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				"archive-chatgpt-only", "conv:archive-only", sessionID, threadID, "user", "chat", fmt.Sprintf(`{"conversation_id":%q}`, conversationID), "observed", "L3", legacySourceChatGPT, "2026-01-07", "2026-01-07")
			execInventory(t, fixture.l1, `INSERT INTO l1_event_log (id, event_type, namespace, session_id, thread_id, payload_json, source, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				"log-chatgpt-archive-only", "memory", "session:chatgpt", sessionID, threadID, `{}`, legacySourceChatGPT, "2026-01-07")
		} else {
			execInventory(t, fixture.l1, `INSERT INTO l1_event_log (id, event_type, namespace, session_id, thread_id, payload_json, source, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				"log-chatgpt-archive-only", "memory", "session:chatgpt", sessionID, threadID, `{}`, legacySourceChatGPT, "2026-01-07")
			execInventory(t, fixture.archive, `INSERT INTO l1_memory_event_archive (id, namespace, session_id, thread_id, speaker, message, meta_json, memory_state, layer, source, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				"archive-chatgpt-only", "conv:archive-only", sessionID, threadID, "user", "chat", fmt.Sprintf(`{"conversation_id":%q}`, conversationID), "observed", "L3", legacySourceChatGPT, "2026-01-07", "2026-01-07")
		}
		execInventory(t, fixture.l1, `INSERT INTO l1_raw_record (source_record_id, source_type, thread_id) VALUES (?, ?, ?)`, "archive-chatgpt-only", legacySourceChatGPT, conversationID)
	}
	insertArchiveOnlyChatGPT(first, false)
	insertArchiveOnlyChatGPT(second, true)

	firstResult, err := InventorySQLite(context.Background(), SQLiteInventoryInput{L1DB: first.l1, ArchiveDB: first.archive})
	if err != nil {
		t.Fatalf("first InventorySQLite() error = %v", err)
	}
	secondResult, err := InventorySQLite(context.Background(), SQLiteInventoryInput{L1DB: second.l1, ArchiveDB: second.archive})
	if err != nil {
		t.Fatalf("second InventorySQLite() error = %v", err)
	}
	if firstResult.Plan.MappingSHA256 != secondResult.Plan.MappingSHA256 || firstResult.Receipt.ReceiptSHA256 != secondResult.Receipt.ReceiptSHA256 {
		t.Fatalf("cross-database insertion order changed hashes: first=%s/%s second=%s/%s", firstResult.Plan.MappingSHA256, firstResult.Receipt.ReceiptSHA256, secondResult.Plan.MappingSHA256, secondResult.Receipt.ReceiptSHA256)
	}
	chat, found := firstResult.Plan.LookupChatGPT(conversationID)
	if !found {
		t.Fatalf("archive-only ChatGPT conversation was not classified: %+v", firstResult.Plan.ChatGPT)
	}
	for _, source := range []struct{ surface, key string }{
		{l1EventLogSurface, "log-chatgpt-archive-only"},
		{l1MemoryEventArchiveSurface, "archive-chatgpt-only"},
	} {
		mapping, found := firstResult.Plan.LookupBySource(source.surface, source.key)
		if !found || mapping.ChatGPTConversationID != conversationID || mapping.SessionID != chat.SessionID || mapping.ThreadID != chat.ThreadID {
			t.Fatalf("archive-only ChatGPT lookup %s/%s = %+v, found=%v; want %+v", source.surface, source.key, mapping, found, chat)
		}
	}
}

func TestInventorySQLiteStreamsRowsWithSingleConnectionAndBoundsOutput(t *testing.T) {
	fixture := newSQLiteInventoryFixture(t)
	secretPayload := strings.Repeat("row-body-that-must-not-escape-", 256)
	execInventory(t, fixture.l1, `INSERT INTO l1_memory_event (id, namespace, session_id, thread_id, speaker, message, meta_json, memory_state, layer, source, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"event-large", "session:legacy", "legacy-session", 7, "user", secretPayload, `{}`, "observed", "L1", "", "2026-01-06", "2026-01-06")
	for index := 0; index < 1024; index++ {
		execInventory(t, fixture.l1, `INSERT INTO l1_memory_event (id, namespace, session_id, thread_id, speaker, message, meta_json, memory_state, layer, source, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			fmt.Sprintf("event-large-%04d", index), "session:legacy", "legacy-session", 7, "assistant", "bulk-row", `{}`, "observed", "L1", "", "2026-01-06", "2026-01-06")
	}
	result, err := InventorySQLite(context.Background(), SQLiteInventoryInput{L1DB: fixture.l1, ArchiveDB: fixture.archive})
	if err != nil {
		t.Fatalf("InventorySQLite() with MaxOpenConns(1) error = %v", err)
	}
	planJSON, err := result.Plan.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	receiptJSON, err := result.Receipt.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(planJSON), secretPayload) || strings.Contains(string(receiptJSON), secretPayload) {
		t.Fatal("inventory output retained an arbitrary legacy message payload")
	}
	generic, found := result.Plan.LookupBySource(l1MemoryEventSurface, "event-generic")
	if !found {
		t.Fatal("generic representative mapping was not indexed")
	}
	if len(generic.Sources) > len(legacyTableNames) {
		t.Fatalf("generic mapping retained %d source references for %d legacy surfaces; want bounded representatives", len(generic.Sources), len(legacyTableNames))
	}
}

func TestInventorySQLiteRejectsCanonicalOrPartialSchema(t *testing.T) {
	fixture := newSQLiteInventoryFixture(t)
	execInventory(t, fixture.l1, `ALTER TABLE l1_memory_event ADD COLUMN thread_seq INTEGER`)
	if _, err := InventorySQLite(context.Background(), SQLiteInventoryInput{L1DB: fixture.l1, ArchiveDB: fixture.archive}); err == nil {
		t.Fatal("canonical/extended L1 schema was accepted as a legacy schema")
	}

	missing := openInventoryTestDB(t, "file:threadmigration_inventory_missing?mode=memory&cache=shared")
	createLegacyL1Schema(t, missing)
	if _, err := InventorySQLite(context.Background(), SQLiteInventoryInput{L1DB: fixture.l1, ArchiveDB: missing}); err == nil {
		t.Fatal("missing legacy archive table was accepted")
	}
}

func TestInventorySQLiteRejectsLegacyRowContractViolations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(sqliteInventoryFixture)
	}{
		{name: "profile evidence mismatch", mutate: func(f sqliteInventoryFixture) {
			execInventory(t, f.l1, `UPDATE l1_profile_promotion_job SET thread_id = 99 WHERE evidence_event_id = 'event-generic'`)
		}},
		{name: "summary orphan", mutate: func(f sqliteInventoryFixture) {
			execInventory(t, f.archive, `DELETE FROM session_thread WHERE thread_id = 7`)
		}},
		{name: "turn outbox identity mismatch", mutate: func(f sqliteInventoryFixture) {
			execInventory(t, f.l1, `UPDATE conversation_turn_outbox SET thread_id = 10 WHERE turn_id = ?`, f.turnID)
		}},
		{name: "turn receipt trace mismatch", mutate: func(f sqliteInventoryFixture) {
			execInventory(t, f.l1, `UPDATE conversation_turn_receipt SET trace_id = 'different-trace' WHERE turn_id = ?`, f.turnID)
		}},
		{name: "numeric JSON encoded as string", mutate: func(f sqliteInventoryFixture) {
			var resultJSON string
			if err := f.l1.QueryRow(`SELECT result_json FROM conversation_turn_receipt WHERE turn_id = ?`, f.turnID).Scan(&resultJSON); err != nil {
				t.Fatalf("read fixture result: %v", err)
			}
			resultJSON = strings.Replace(resultJSON, `"thread_id":9`, `"thread_id":"9"`, 1)
			execInventory(t, f.l1, `UPDATE conversation_turn_receipt SET result_json = ? WHERE turn_id = ?`, resultJSON, f.turnID)
		}},
		{name: "raw thread provenance mismatch", mutate: func(f sqliteInventoryFixture) {
			execInventory(t, f.l1, `UPDATE l1_raw_record SET thread_id = 'other-conversation' WHERE source_record_id = 'event-chatgpt'`)
		}},
		{name: "raw provenance missing", mutate: func(f sqliteInventoryFixture) {
			execInventory(t, f.l1, `DELETE FROM l1_raw_record WHERE source_record_id = 'event-chatgpt'`)
		}},
		{name: "raw provenance duplicate", mutate: func(f sqliteInventoryFixture) {
			execInventory(t, f.l1, `INSERT INTO l1_raw_record (source_record_id, source_type, thread_id) VALUES (?, ?, ?)`, "event-chatgpt", legacySourceChatGPT, f.chatGPTID)
		}},
		{name: "negative legacy thread", mutate: func(f sqliteInventoryFixture) {
			execInventory(t, f.archive, `UPDATE l1_memory_event_archive SET thread_id = -1 WHERE id = 'archive-generic'`)
		}},
		{name: "positive legacy thread with empty session", mutate: func(f sqliteInventoryFixture) {
			execInventory(t, f.l1, `UPDATE l1_memory_event SET session_id = '' WHERE id = 'event-generic'`)
		}},
		{name: "ChatGPT source with zero thread", mutate: func(f sqliteInventoryFixture) {
			execInventory(t, f.l1, `UPDATE l1_memory_event SET thread_id = 0 WHERE id = 'event-chatgpt'`)
		}},
		{name: "ChatGPT event log tuple is unregistered", mutate: func(f sqliteInventoryFixture) {
			execInventory(t, f.l1, `INSERT INTO l1_event_log (id, event_type, namespace, session_id, thread_id, payload_json, source, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				"log-chatgpt-unregistered", "memory", "session:chatgpt", "chatgpt-unregistered", 123, `{}`, legacySourceChatGPT, "2026-01-06")
		}},
		{name: "ChatGPT raw table missing", mutate: func(f sqliteInventoryFixture) {
			execInventory(t, f.l1, `DROP TABLE l1_raw_record`)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSQLiteInventoryFixture(t)
			test.mutate(fixture)
			if _, err := InventorySQLite(context.Background(), SQLiteInventoryInput{L1DB: fixture.l1, ArchiveDB: fixture.archive}); err == nil {
				t.Fatal("invalid legacy row was accepted")
			}
		})
	}
}

func TestInventorySQLiteRejectsUnexpectedJSONIdentityOccurrences(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(sqliteInventoryFixture)
	}{
		{name: "memory nested thread id", mutate: func(f sqliteInventoryFixture) {
			execInventory(t, f.l1, `UPDATE l1_memory_event SET meta_json = ? WHERE id = 'event-generic'`, `{"nested":{"thread_id":7}}`)
		}},
		{name: "event log discussion id", mutate: func(f sqliteInventoryFixture) {
			execInventory(t, f.l1, `UPDATE l1_event_log SET payload_json = ? WHERE id = 'log-generic'`, `{"discussion_id":7}`)
		}},
		{name: "archive nested thread id", mutate: func(f sqliteInventoryFixture) {
			execInventory(t, f.archive, `UPDATE l1_memory_event_archive SET meta_json = ? WHERE id = 'archive-generic'`, `{"nested":{"thread_id":7}}`)
		}},
		{name: "duplicate ChatGPT conversation id", mutate: func(f sqliteInventoryFixture) {
			execInventory(t, f.l1, `UPDATE l1_memory_event SET meta_json = ? WHERE id = 'event-chatgpt'`, `{"conversation_id":"conv-identity","conversation_id":"conv-identity"}`)
		}},
		{name: "turn result nested thread id", mutate: func(f sqliteInventoryFixture) {
			var resultJSON string
			if err := f.l1.QueryRow(`SELECT result_json FROM conversation_turn_receipt WHERE turn_id = ?`, f.turnID).Scan(&resultJSON); err != nil {
				t.Fatalf("read fixture result: %v", err)
			}
			resultJSON = strings.TrimSuffix(resultJSON, "}") + `,"nested":{"thread_id":9}}`
			execInventory(t, f.l1, `UPDATE conversation_turn_receipt SET result_json = ? WHERE turn_id = ?`, resultJSON, f.turnID)
		}},
		{name: "outbox nested discussion id", mutate: func(f sqliteInventoryFixture) {
			var payloadJSON string
			if err := f.l1.QueryRow(`SELECT payload_json FROM conversation_turn_outbox WHERE turn_id = ?`, f.turnID).Scan(&payloadJSON); err != nil {
				t.Fatalf("read fixture payload: %v", err)
			}
			payloadJSON = strings.TrimSuffix(payloadJSON, "}") + `,"nested":{"discussion_id":9}}`
			execInventory(t, f.l1, `UPDATE conversation_turn_outbox SET payload_json = ? WHERE turn_id = ?`, payloadJSON, f.turnID)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSQLiteInventoryFixture(t)
			test.mutate(fixture)
			if _, err := InventorySQLite(context.Background(), SQLiteInventoryInput{L1DB: fixture.l1, ArchiveDB: fixture.archive}); err == nil {
				t.Fatal("unsupported or duplicate JSON identity was accepted")
			}
		})
	}
}

func TestInventorySQLiteRejectsChatGPTTupleCollisionsAndMalformedMetadata(t *testing.T) {
	fixture := newSQLiteInventoryFixture(t)
	// Keep the expected tuple but give it a different exact conversation ID.
	otherConversation := "other-conversation"
	execInventory(t, fixture.l1, `UPDATE l1_memory_event SET meta_json = ? WHERE id = 'event-chatgpt'`, fmt.Sprintf(`{"conversation_id":%q}`, otherConversation))
	if _, err := InventorySQLite(context.Background(), SQLiteInventoryInput{L1DB: fixture.l1, ArchiveDB: fixture.archive}); err == nil {
		t.Fatal("ChatGPT tuple mismatch was accepted")
	}

	malformed := newSQLiteInventoryFixture(t)
	execInventory(t, malformed.l1, `UPDATE l1_memory_event SET meta_json = '{}' WHERE id = 'event-chatgpt'`)
	if _, err := InventorySQLite(context.Background(), SQLiteInventoryInput{L1DB: malformed.l1, ArchiveDB: malformed.archive}); err == nil {
		t.Fatal("ChatGPT metadata without exact conversation_id was accepted")
	}
}

func TestSQLiteInventoryReceiptRejectsTampering(t *testing.T) {
	fixture := newSQLiteInventoryFixture(t)
	result, err := InventorySQLite(context.Background(), SQLiteInventoryInput{L1DB: fixture.l1, ArchiveDB: fixture.archive})
	if err != nil {
		t.Fatal(err)
	}
	result.Receipt.SurfaceCounts[0].Rows++
	if err := result.Validate(); err == nil {
		t.Fatal("tampered receipt was accepted")
	}

	encoded, err := json.Marshal(result.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) == 0 {
		t.Fatal("receipt JSON unexpectedly empty")
	}
}

func TestSQLiteInventoryReceiptRejectsInternallyInconsistentCountsWithValidHash(t *testing.T) {
	fixture := newSQLiteInventoryFixture(t)
	base, err := InventorySQLite(context.Background(), SQLiteInventoryInput{L1DB: fixture.l1, ArchiveDB: fixture.archive})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*SQLiteInventoryReceipt)
	}{
		{name: "optional zero exceeds positive relationship", mutate: func(receipt *SQLiteInventoryReceipt) {
			for index := range receipt.OptionalZeroCounts {
				if receipt.OptionalZeroCounts[index].Surface == l1MemoryEventSurface {
					receipt.OptionalZeroCounts[index].Count++
				}
			}
		}},
		{name: "required surface loses reference", mutate: func(receipt *SQLiteInventoryReceipt) {
			for index := range receipt.SurfaceCounts {
				if receipt.SurfaceCounts[index].Surface == activeThreadSurface {
					receipt.SurfaceCounts[index].References--
				}
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := base
			result.Receipt.SurfaceCounts = append([]SQLiteInventorySurfaceCount(nil), base.Receipt.SurfaceCounts...)
			result.Receipt.OptionalZeroCounts = append([]SQLiteInventoryOptionalZeroCount(nil), base.Receipt.OptionalZeroCounts...)
			test.mutate(&result.Receipt)
			result.Receipt.ReceiptSHA256, err = result.Receipt.ComputeSHA256()
			if err != nil {
				t.Fatal(err)
			}
			if err := result.Validate(); err == nil {
				t.Fatal("internally inconsistent receipt counts were accepted after rehash")
			}
		})
	}
}

func sqliteInventoryDataVersion(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var version int64
	if err := db.QueryRow(`PRAGMA data_version`).Scan(&version); err != nil {
		t.Fatalf("read data_version: %v", err)
	}
	return version
}
