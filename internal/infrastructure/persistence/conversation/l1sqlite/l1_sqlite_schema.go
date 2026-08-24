package l1sqlite

import (
	"context"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

func (s *L1SQLiteStore) initTables(ctx context.Context) error {
	schema := `
PRAGMA journal_mode=WAL;
CREATE TABLE IF NOT EXISTS l1_memory_event (
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
);
CREATE INDEX IF NOT EXISTS idx_l1_memory_namespace_created ON l1_memory_event(namespace, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_l1_memory_session_created ON l1_memory_event(session_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_l1_memory_state_created ON l1_memory_event(memory_state, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_l1_memory_thread_created ON l1_memory_event(thread_id, created_at DESC);
DROP TRIGGER IF EXISTS trg_l1_user_memory_search_insert;
DROP TRIGGER IF EXISTS trg_l1_user_memory_search_update;
DROP TRIGGER IF EXISTS trg_l1_user_memory_search_delete;
DROP TABLE IF EXISTS l1_user_memory_search_projection;
CREATE TABLE IF NOT EXISTS l1_user_memory_viewer_projection (
	id TEXT PRIMARY KEY,
	namespace TEXT NOT NULL,
	user_id TEXT NOT NULL,
	memory_type TEXT NOT NULL,
	memory_state TEXT NOT NULL,
	active INTEGER NOT NULL,
	statement TEXT NOT NULL,
	evidence_text TEXT NOT NULL,
	confidence REAL NOT NULL,
	sensitivity TEXT NOT NULL,
	scope TEXT NOT NULL,
	lifecycle_status TEXT NOT NULL,
	decay_score REAL NOT NULL,
	superseded_by TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_l1_user_memory_viewer_page
	ON l1_user_memory_viewer_projection(namespace, memory_state, active, created_at DESC, id DESC);
DROP INDEX IF EXISTS idx_l1_user_memory_viewer_search_cover;
CREATE VIRTUAL TABLE IF NOT EXISTS l1_user_memory_viewer_fts
	USING fts5(id UNINDEXED, statement, evidence_text, tokenize='trigram');
CREATE TRIGGER IF NOT EXISTS trg_l1_user_memory_viewer_fts_insert_v3
AFTER INSERT ON l1_user_memory_viewer_projection
BEGIN
	INSERT INTO l1_user_memory_viewer_fts(id, statement, evidence_text)
	VALUES(NEW.id, NEW.statement, NEW.evidence_text);
END;
CREATE TRIGGER IF NOT EXISTS trg_l1_user_memory_viewer_fts_update_v3
AFTER UPDATE OF statement, evidence_text ON l1_user_memory_viewer_projection
BEGIN
	DELETE FROM l1_user_memory_viewer_fts WHERE id = OLD.id;
	INSERT INTO l1_user_memory_viewer_fts(id, statement, evidence_text)
	VALUES(NEW.id, NEW.statement, NEW.evidence_text);
END;
CREATE TRIGGER IF NOT EXISTS trg_l1_user_memory_viewer_fts_delete_v3
AFTER DELETE ON l1_user_memory_viewer_projection
BEGIN
	DELETE FROM l1_user_memory_viewer_fts WHERE id = OLD.id;
END;
CREATE TRIGGER IF NOT EXISTS trg_l1_user_memory_viewer_insert_v2
AFTER INSERT ON l1_memory_event
WHEN NEW.speaker = 'memory' AND NEW.layer = 'L1' AND json_valid(NEW.meta_json)
	AND json_type(NEW.meta_json, '$.user_id') = 'text'
	AND json_type(NEW.meta_json, '$.type') = 'text'
	AND trim(COALESCE(json_extract(NEW.meta_json, '$.statement'), '')) = trim(NEW.message)
	AND NEW.namespace = 'user:' || trim(json_extract(NEW.meta_json, '$.user_id'))
BEGIN
	INSERT INTO l1_user_memory_viewer_projection(
		id, namespace, user_id, memory_type, memory_state, active, statement, evidence_text,
		confidence, sensitivity, scope, lifecycle_status, decay_score, superseded_by, created_at, updated_at)
	VALUES(NEW.id, NEW.namespace, trim(json_extract(NEW.meta_json, '$.user_id')),
		trim(json_extract(NEW.meta_json, '$.type')), NEW.memory_state,
		CAST(COALESCE(json_extract(NEW.meta_json, '$.active'), 1) AS INTEGER),
		NEW.message, COALESCE(json_extract(NEW.meta_json, '$.evidence_event_ids'), '[]'),
		CAST(COALESCE(json_extract(NEW.meta_json, '$.confidence'), 0.5) AS REAL),
		COALESCE(json_extract(NEW.meta_json, '$.sensitivity'), 'normal'),
		COALESCE(json_extract(NEW.meta_json, '$.scope'), 'all_personas'),
		COALESCE(json_extract(NEW.meta_json, '$.lifecycle_status'), ''),
		CAST(COALESCE(json_extract(NEW.meta_json, '$.decay_score'), 0) AS REAL),
		COALESCE(json_extract(NEW.meta_json, '$.superseded_by'), ''), NEW.created_at, NEW.updated_at)
	ON CONFLICT(id) DO UPDATE SET
		namespace=excluded.namespace, user_id=excluded.user_id, memory_type=excluded.memory_type,
		memory_state=excluded.memory_state, active=excluded.active, statement=excluded.statement,
		evidence_text=excluded.evidence_text, confidence=excluded.confidence,
		sensitivity=excluded.sensitivity, scope=excluded.scope, lifecycle_status=excluded.lifecycle_status,
		decay_score=excluded.decay_score, superseded_by=excluded.superseded_by,
		created_at=excluded.created_at, updated_at=excluded.updated_at;
END;
CREATE TRIGGER IF NOT EXISTS trg_l1_user_memory_viewer_update_v2
AFTER UPDATE OF namespace, speaker, message, meta_json, memory_state, layer, created_at, updated_at ON l1_memory_event
BEGIN
	DELETE FROM l1_user_memory_viewer_projection WHERE id = OLD.id;
	INSERT INTO l1_user_memory_viewer_projection(
		id, namespace, user_id, memory_type, memory_state, active, statement, evidence_text,
		confidence, sensitivity, scope, lifecycle_status, decay_score, superseded_by, created_at, updated_at)
	SELECT NEW.id, NEW.namespace, trim(json_extract(NEW.meta_json, '$.user_id')),
		trim(json_extract(NEW.meta_json, '$.type')), NEW.memory_state,
		CAST(COALESCE(json_extract(NEW.meta_json, '$.active'), 1) AS INTEGER),
		NEW.message, COALESCE(json_extract(NEW.meta_json, '$.evidence_event_ids'), '[]'),
		CAST(COALESCE(json_extract(NEW.meta_json, '$.confidence'), 0.5) AS REAL),
		COALESCE(json_extract(NEW.meta_json, '$.sensitivity'), 'normal'),
		COALESCE(json_extract(NEW.meta_json, '$.scope'), 'all_personas'),
		COALESCE(json_extract(NEW.meta_json, '$.lifecycle_status'), ''),
		CAST(COALESCE(json_extract(NEW.meta_json, '$.decay_score'), 0) AS REAL),
		COALESCE(json_extract(NEW.meta_json, '$.superseded_by'), ''), NEW.created_at, NEW.updated_at
	WHERE NEW.speaker = 'memory' AND NEW.layer = 'L1' AND json_valid(NEW.meta_json)
		AND json_type(NEW.meta_json, '$.user_id') = 'text'
		AND json_type(NEW.meta_json, '$.type') = 'text'
		AND trim(COALESCE(json_extract(NEW.meta_json, '$.statement'), '')) = trim(NEW.message)
		AND NEW.namespace = 'user:' || trim(json_extract(NEW.meta_json, '$.user_id'));
END;
CREATE TRIGGER IF NOT EXISTS trg_l1_user_memory_viewer_delete_v2
AFTER DELETE ON l1_memory_event
BEGIN
	DELETE FROM l1_user_memory_viewer_projection WHERE id = OLD.id;
END;
INSERT OR IGNORE INTO l1_user_memory_viewer_projection(
	id, namespace, user_id, memory_type, memory_state, active, statement, evidence_text,
	confidence, sensitivity, scope, lifecycle_status, decay_score, superseded_by, created_at, updated_at)
SELECT id, namespace, trim(json_extract(meta_json, '$.user_id')), trim(json_extract(meta_json, '$.type')), memory_state,
	CAST(COALESCE(json_extract(meta_json, '$.active'), 1) AS INTEGER),
	message, COALESCE(json_extract(meta_json, '$.evidence_event_ids'), '[]'),
	CAST(COALESCE(json_extract(meta_json, '$.confidence'), 0.5) AS REAL),
	COALESCE(json_extract(meta_json, '$.sensitivity'), 'normal'),
	COALESCE(json_extract(meta_json, '$.scope'), 'all_personas'),
	COALESCE(json_extract(meta_json, '$.lifecycle_status'), ''),
	CAST(COALESCE(json_extract(meta_json, '$.decay_score'), 0) AS REAL),
	COALESCE(json_extract(meta_json, '$.superseded_by'), ''), created_at, updated_at
FROM l1_memory_event
WHERE speaker = 'memory' AND layer = 'L1' AND json_valid(meta_json)
	AND json_type(meta_json, '$.user_id') = 'text'
	AND json_type(meta_json, '$.type') = 'text'
	AND trim(COALESCE(json_extract(meta_json, '$.statement'), '')) = trim(message)
	AND namespace = 'user:' || trim(json_extract(meta_json, '$.user_id'))
	AND NOT EXISTS (SELECT 1 FROM l1_user_memory_viewer_projection LIMIT 1);
INSERT INTO l1_user_memory_viewer_fts(id, statement, evidence_text)
SELECT id, statement, evidence_text FROM l1_user_memory_viewer_projection
WHERE NOT EXISTS (SELECT 1 FROM l1_user_memory_viewer_fts LIMIT 1);
CREATE TABLE IF NOT EXISTS l1_profile_promotion_job (
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
);
CREATE INDEX IF NOT EXISTS idx_l1_profile_promotion_state_retry
	ON l1_profile_promotion_job(state, next_attempt_at, created_at);
CREATE INDEX IF NOT EXISTS idx_l1_profile_promotion_session_thread
	ON l1_profile_promotion_job(session_id, thread_id, created_at);
CREATE INDEX IF NOT EXISTS idx_l1_profile_promotion_created
	ON l1_profile_promotion_job(created_at DESC, evidence_event_id DESC);
CREATE TABLE IF NOT EXISTS l1_search_cache (
	query_hash TEXT PRIMARY KEY,
	normalized_query TEXT NOT NULL,
	provider TEXT NOT NULL,
	raw_query TEXT NOT NULL,
	results_json TEXT NOT NULL,
	source_urls_json TEXT NOT NULL DEFAULT '[]',
	retrieved_at TIMESTAMP NOT NULL,
	expires_at TIMESTAMP NOT NULL,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_l1_search_cache_expires ON l1_search_cache(expires_at);
CREATE INDEX IF NOT EXISTS idx_l1_search_cache_retrieved ON l1_search_cache(retrieved_at DESC);
CREATE TABLE IF NOT EXISTS l1_web_gather_fetch_cache (
	cache_key TEXT PRIMARY KEY,
	url TEXT NOT NULL,
	fetch_provider TEXT NOT NULL,
	extractor TEXT NOT NULL,
	status TEXT NOT NULL,
	response_json TEXT NOT NULL,
	error_code TEXT NOT NULL DEFAULT '',
	retrieved_at TIMESTAMP NOT NULL,
	expires_at TIMESTAMP NOT NULL,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_l1_web_gather_fetch_cache_expires ON l1_web_gather_fetch_cache(expires_at);
CREATE INDEX IF NOT EXISTS idx_l1_web_gather_fetch_cache_url ON l1_web_gather_fetch_cache(url, fetch_provider, extractor);
CREATE TABLE IF NOT EXISTS l1_web_gather_rate_state (
	domain TEXT PRIMARY KEY,
	last_fetch_at TIMESTAMP NOT NULL,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_l1_web_gather_rate_state_updated ON l1_web_gather_rate_state(updated_at DESC);
CREATE TABLE IF NOT EXISTS l1_event_log (
	id TEXT PRIMARY KEY,
	event_type TEXT NOT NULL,
	namespace TEXT NOT NULL,
	session_id TEXT NOT NULL DEFAULT '',
	thread_id INTEGER NOT NULL DEFAULT 0,
	payload_json TEXT NOT NULL DEFAULT '{}',
	source TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_l1_event_log_namespace_created ON l1_event_log(namespace, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_l1_event_log_type_created ON l1_event_log(event_type, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_l1_event_log_session_created ON l1_event_log(session_id, created_at DESC);
CREATE TABLE IF NOT EXISTS conversation_lifecycle_raw_archive_outbox (
	outbox_id TEXT PRIMARY KEY,
	event_id TEXT NOT NULL UNIQUE,
	namespace TEXT NOT NULL,
	event_sha256 TEXT NOT NULL,
	status TEXT NOT NULL CHECK(status IN ('pending', 'failed', 'archived')),
	attempt_count INTEGER NOT NULL DEFAULT 0,
	last_error TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL,
	archived_at TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_conversation_raw_archive_outbox_status_created
	ON conversation_lifecycle_raw_archive_outbox(status, created_at ASC, outbox_id ASC);
CREATE TABLE IF NOT EXISTS l1_memory_owner_receipt (
	request_id TEXT PRIMARY KEY,
	operation TEXT NOT NULL,
	owner_id TEXT NOT NULL,
	actor_id TEXT NOT NULL,
	payload_hash TEXT NOT NULL,
	memory_id TEXT NOT NULL,
	audit_reference TEXT NOT NULL,
	result_json TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_l1_memory_owner_receipt_memory ON l1_memory_owner_receipt(memory_id, created_at DESC);
CREATE TABLE IF NOT EXISTS l1_user_memory_lifecycle_plan (
	plan_request_id TEXT PRIMARY KEY,
	plan_id TEXT NOT NULL UNIQUE,
	owner_id TEXT NOT NULL,
	actor_id TEXT NOT NULL,
	payload_hash TEXT NOT NULL,
	cohort_hash TEXT NOT NULL,
	actions_json TEXT NOT NULL,
	action_count INTEGER NOT NULL,
	evaluation_at TIMESTAMP NOT NULL,
	created_at TIMESTAMP NOT NULL,
	expires_at TIMESTAMP NOT NULL,
	status TEXT NOT NULL,
	receipt_json TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_l1_user_memory_lifecycle_plan_owner_status
	ON l1_user_memory_lifecycle_plan(owner_id, status, expires_at);
CREATE TABLE IF NOT EXISTS l1_user_memory_lifecycle_run_receipt (
	server_request_id TEXT PRIMARY KEY,
	plan_request_id TEXT NOT NULL UNIQUE,
	owner_id TEXT NOT NULL,
	actor_id TEXT NOT NULL,
	reason_hash TEXT NOT NULL,
	cohort_hash TEXT NOT NULL,
	actions_json TEXT NOT NULL,
	action_count INTEGER NOT NULL,
	completed_at TIMESTAMP NOT NULL,
	status TEXT NOT NULL,
	receipt_json TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_l1_user_memory_lifecycle_run_owner_completed
	ON l1_user_memory_lifecycle_run_receipt(owner_id, completed_at DESC);
CREATE TABLE IF NOT EXISTS l1_staging_item (
	id TEXT PRIMARY KEY,
	kind TEXT NOT NULL,
	namespace TEXT NOT NULL,
	event_id TEXT NOT NULL,
	source_id TEXT NOT NULL,
	source_url TEXT NOT NULL DEFAULT '',
	fetched_at TIMESTAMP NOT NULL,
	published_at TIMESTAMP,
	raw_text TEXT NOT NULL,
	raw_hash TEXT NOT NULL,
	summary_draft TEXT NOT NULL DEFAULT '',
	keywords_json TEXT NOT NULL DEFAULT '[]',
	license_note TEXT NOT NULL DEFAULT '',
	validation_status TEXT NOT NULL,
	meta_json TEXT NOT NULL DEFAULT '{}',
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_l1_staging_namespace_event ON l1_staging_item(namespace, event_id);
CREATE INDEX IF NOT EXISTS idx_l1_staging_status_created ON l1_staging_item(validation_status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_l1_staging_raw_hash ON l1_staging_item(raw_hash);
CREATE TABLE IF NOT EXISTS l1_source_registry (
	source_id TEXT PRIMARY KEY,
	url TEXT NOT NULL,
	kind TEXT NOT NULL,
	trust_score REAL NOT NULL,
	fetch_interval_sec INTEGER NOT NULL,
	license_note TEXT NOT NULL,
	enabled INTEGER NOT NULL,
	meta_json TEXT NOT NULL DEFAULT '{}',
	last_fetched_at TIMESTAMP,
	last_status TEXT NOT NULL DEFAULT '',
	last_error TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_l1_source_registry_enabled_kind ON l1_source_registry(enabled, kind);
CREATE TABLE IF NOT EXISTS l1_news_item (
	id TEXT PRIMARY KEY,
	staging_id TEXT NOT NULL UNIQUE,
	category TEXT NOT NULL,
	source_id TEXT NOT NULL,
	source_url TEXT NOT NULL DEFAULT '',
	published_at TIMESTAMP,
	fetched_at TIMESTAMP NOT NULL,
	raw_text TEXT NOT NULL,
	raw_hash TEXT NOT NULL,
	summary_draft TEXT NOT NULL DEFAULT '',
	keywords_json TEXT NOT NULL DEFAULT '[]',
	license_note TEXT NOT NULL DEFAULT '',
	meta_json TEXT NOT NULL DEFAULT '{}',
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_l1_news_category_published ON l1_news_item(category, published_at DESC);
CREATE INDEX IF NOT EXISTS idx_l1_news_source_published ON l1_news_item(source_id, published_at DESC);
CREATE INDEX IF NOT EXISTS idx_l1_news_raw_hash ON l1_news_item(raw_hash);
CREATE TABLE IF NOT EXISTS l1_news_article_fetch (
	normalized_url TEXT PRIMARY KEY,
	status TEXT NOT NULL,
	final_url TEXT NOT NULL DEFAULT '',
	fetch_url TEXT NOT NULL DEFAULT '',
	content_type TEXT NOT NULL DEFAULT '',
	fetch_provider TEXT NOT NULL DEFAULT '',
	extractor TEXT NOT NULL DEFAULT '',
	raw_bytes INTEGER NOT NULL DEFAULT 0,
	article_text TEXT NOT NULL DEFAULT '',
	content_sha256 TEXT NOT NULL DEFAULT '',
	error_code TEXT NOT NULL DEFAULT '',
	attempt_count INTEGER NOT NULL DEFAULT 0,
	lease_expires_at TIMESTAMP,
	next_attempt_at TIMESTAMP,
	completed_at TIMESTAMP,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_l1_news_article_fetch_retry
	ON l1_news_article_fetch(status, next_attempt_at, lease_expires_at);
CREATE TABLE IF NOT EXISTS l1_daily_digest (
	id TEXT PRIMARY KEY,
	digest_date TEXT NOT NULL,
	category TEXT NOT NULL,
	digest_slot TEXT NOT NULL DEFAULT 'day',
	news_ids_json TEXT NOT NULL DEFAULT '[]',
	digest_text TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL
);
CREATE TABLE IF NOT EXISTS l1_monthly_highlight (
	id TEXT PRIMARY KEY,
	month TEXT NOT NULL,
	category TEXT NOT NULL,
	source_ids_json TEXT NOT NULL DEFAULT '[]',
	highlight_text TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL
);
CREATE TABLE IF NOT EXISTS l1_knowledge_item (
	id TEXT PRIMARY KEY,
	staging_id TEXT NOT NULL UNIQUE,
	domain TEXT NOT NULL,
	title TEXT NOT NULL,
	source_id TEXT NOT NULL,
	source_url TEXT NOT NULL DEFAULT '',
	raw_text TEXT NOT NULL,
	raw_hash TEXT NOT NULL,
	summary_draft TEXT NOT NULL DEFAULT '',
	keywords_json TEXT NOT NULL DEFAULT '[]',
	license_note TEXT NOT NULL DEFAULT '',
	meta_json TEXT NOT NULL DEFAULT '{}',
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_l1_knowledge_domain_title ON l1_knowledge_item(domain, title);
CREATE INDEX IF NOT EXISTS idx_l1_knowledge_raw_hash ON l1_knowledge_item(raw_hash);
CREATE TABLE IF NOT EXISTS l1_knowledge_entity (
	entity_id TEXT PRIMARY KEY,
	canonical_name TEXT NOT NULL,
	entity_type TEXT NOT NULL,
	aliases_json TEXT NOT NULL DEFAULT '[]',
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_l1_knowledge_entity_name ON l1_knowledge_entity(canonical_name, entity_type);
CREATE TABLE IF NOT EXISTS l1_knowledge_item_entity (
	item_id TEXT NOT NULL,
	entity_id TEXT NOT NULL,
	relation_kind TEXT NOT NULL,
	score REAL NOT NULL,
	evidence TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMP NOT NULL,
	PRIMARY KEY (item_id, entity_id, relation_kind)
);
CREATE INDEX IF NOT EXISTS idx_l1_knowledge_item_entity_entity ON l1_knowledge_item_entity(entity_id);
CREATE TABLE IF NOT EXISTS l1_knowledge_item_relation (
	src_item_id TEXT NOT NULL,
	dst_item_id TEXT NOT NULL,
	relation_type TEXT NOT NULL,
	score REAL NOT NULL,
	evidence TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL,
	PRIMARY KEY (src_item_id, dst_item_id, relation_type)
);
CREATE INDEX IF NOT EXISTS idx_l1_knowledge_item_relation_src_score ON l1_knowledge_item_relation(src_item_id, score DESC);
CREATE INDEX IF NOT EXISTS idx_l1_knowledge_item_relation_dst_score ON l1_knowledge_item_relation(dst_item_id, score DESC);
CREATE TABLE IF NOT EXISTS l1_knowledge_item_fts (
	id TEXT PRIMARY KEY,
	domain TEXT NOT NULL,
	title TEXT NOT NULL DEFAULT '',
	raw_text TEXT NOT NULL DEFAULT '',
	summary_draft TEXT NOT NULL DEFAULT '',
	keywords_text TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_l1_knowledge_fts_domain ON l1_knowledge_item_fts(domain);
CREATE TABLE IF NOT EXISTS wiki_page_index (
	page_id TEXT PRIMARY KEY,
	path TEXT NOT NULL UNIQUE,
	title TEXT NOT NULL,
	type TEXT NOT NULL,
	status TEXT NOT NULL,
	owner TEXT NOT NULL DEFAULT '',
	canonical_source TEXT NOT NULL DEFAULT '',
	source_paths_json TEXT NOT NULL DEFAULT '[]',
	related_json TEXT NOT NULL DEFAULT '[]',
	summary TEXT NOT NULL DEFAULT '',
	content_hash TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_wiki_page_index_status_type ON wiki_page_index(status, type, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_wiki_page_index_path ON wiki_page_index(path);
CREATE TABLE IF NOT EXISTS wiki_page_index_fts (
	page_id TEXT PRIMARY KEY,
	title TEXT NOT NULL DEFAULT '',
	path TEXT NOT NULL DEFAULT '',
	type TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT '',
	owner TEXT NOT NULL DEFAULT '',
	canonical_source TEXT NOT NULL DEFAULT '',
	summary TEXT NOT NULL DEFAULT '',
	source_text TEXT NOT NULL DEFAULT '',
	related_text TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_wiki_page_index_fts_status ON wiki_page_index_fts(status);
CREATE INDEX IF NOT EXISTS idx_wiki_page_index_fts_type ON wiki_page_index_fts(type);
CREATE TABLE IF NOT EXISTS domain_graph_assertion (
	assertion_id TEXT PRIMARY KEY,
	staging_id TEXT NOT NULL UNIQUE,
	domain TEXT NOT NULL,
	entity_type TEXT NOT NULL,
	entity_id TEXT NOT NULL DEFAULT '',
	relation_type TEXT NOT NULL DEFAULT '',
	source_id TEXT NOT NULL,
	source_url TEXT NOT NULL DEFAULT '',
	raw_hash TEXT NOT NULL,
	summary TEXT NOT NULL DEFAULT '',
	confidence REAL NOT NULL DEFAULT 0.5,
	validation_status TEXT NOT NULL,
	evidence_json TEXT NOT NULL DEFAULT '{}',
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_domain_graph_assertion_domain_created ON domain_graph_assertion(domain, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_domain_graph_assertion_entity ON domain_graph_assertion(domain, entity_type, entity_id);
CREATE INDEX IF NOT EXISTS idx_domain_graph_assertion_source ON domain_graph_assertion(source_id, raw_hash);
CREATE TABLE IF NOT EXISTS recall_trace (
	trace_id TEXT PRIMARY KEY,
	owner_id TEXT NOT NULL DEFAULT '',
	turn_id TEXT NOT NULL,
	chat_id TEXT NOT NULL,
	persona TEXT NOT NULL,
	route TEXT NOT NULL DEFAULT '',
	user_message_hash TEXT NOT NULL DEFAULT '',
	query_text_redacted TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMP NOT NULL,
	model_id TEXT NOT NULL DEFAULT '',
	prompt_version TEXT NOT NULL DEFAULT '',
	recall_policy_version TEXT NOT NULL DEFAULT '',
	total_candidates INTEGER NOT NULL DEFAULT 0,
	injected_count INTEGER NOT NULL DEFAULT 0,
	total_injected_tokens INTEGER NOT NULL DEFAULT 0,
	status TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_recall_trace_chat_created ON recall_trace(chat_id, created_at DESC);
CREATE TABLE IF NOT EXISTS recall_trace_item (
	item_id TEXT PRIMARY KEY,
	trace_id TEXT NOT NULL,
	layer TEXT NOT NULL,
	memory_id TEXT NOT NULL DEFAULT '',
	source_id TEXT NOT NULL DEFAULT '',
	source_url TEXT NOT NULL DEFAULT '',
	source_type TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	score REAL NOT NULL DEFAULT 0,
	relevance REAL NOT NULL DEFAULT 0,
	recency REAL NOT NULL DEFAULT 0,
	confidence REAL NOT NULL DEFAULT 0,
	source_trust REAL NOT NULL DEFAULT 0,
	reason TEXT NOT NULL DEFAULT '',
	injected INTEGER NOT NULL DEFAULT 0,
	prompt_section TEXT NOT NULL DEFAULT '',
	token_count INTEGER NOT NULL DEFAULT 0,
	sensitivity TEXT NOT NULL DEFAULT '',
	memory_state TEXT NOT NULL DEFAULT '',
	is_raw_or_summary TEXT NOT NULL DEFAULT '',
	retrieved_at TIMESTAMP,
	published_at TIMESTAMP,
	event_id TEXT NOT NULL DEFAULT '',
	summary TEXT NOT NULL DEFAULT '',
	kind TEXT NOT NULL DEFAULT '',
	FOREIGN KEY(trace_id) REFERENCES recall_trace(trace_id)
);
CREATE INDEX IF NOT EXISTS idx_recall_trace_item_trace ON recall_trace_item(trace_id);
CREATE INDEX IF NOT EXISTS idx_recall_trace_item_status ON recall_trace_item(status);
CREATE TABLE IF NOT EXISTS prompt_injection_event (
	injection_id TEXT PRIMARY KEY,
	trace_id TEXT NOT NULL,
	prompt_section TEXT NOT NULL,
	order_index INTEGER NOT NULL,
	item_ids TEXT NOT NULL DEFAULT '[]',
	token_count INTEGER NOT NULL DEFAULT 0,
	redaction_level TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMP NOT NULL,
	FOREIGN KEY(trace_id) REFERENCES recall_trace(trace_id)
);
CREATE INDEX IF NOT EXISTS idx_prompt_injection_event_trace ON prompt_injection_event(trace_id, order_index);
`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("failed to initialize l1 sqlite schema: %w", err)
	}
	if err := s.applyCommonRawSchemaMigration(ctx); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `
CREATE INDEX IF NOT EXISTS idx_l1_raw_projection_progress
	ON l1_raw_projection_receipt(projection_type, output_store, revision, status, output_record_id)
`); err != nil {
		return fmt.Errorf("failed to initialize raw projection progress index: %w", err)
	}
	if err := s.applyChatGPTImportLedgerSchema(ctx); err != nil {
		return err
	}
	if err := s.applyChatGPTImportConfirmSchema(ctx); err != nil {
		return err
	}
	if err := s.applyChatGPTImportFinalizeSchema(ctx); err != nil {
		return err
	}
	if err := s.applyConversationTurnSchema(ctx); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE l1_daily_digest ADD COLUMN digest_slot TEXT NOT NULL DEFAULT 'day'`); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return fmt.Errorf("failed to migrate l1 daily digest slot: %w", err)
	}
	for _, stmt := range []string{
		`ALTER TABLE l1_source_registry ADD COLUMN last_fetched_at TIMESTAMP`,
		`ALTER TABLE l1_source_registry ADD COLUMN last_status TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE l1_source_registry ADD COLUMN last_error TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("failed to migrate l1 source registry fetch status: %w", err)
		}
	}
	for _, stmt := range []string{
		`ALTER TABLE l1_news_article_fetch ADD COLUMN fetch_url TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE l1_news_article_fetch ADD COLUMN content_sha256 TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("failed to migrate l1 news article provenance: %w", err)
		}
	}
	for _, stmt := range []string{
		`ALTER TABLE recall_trace ADD COLUMN owner_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE recall_trace_item ADD COLUMN memory_state TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("failed to migrate recall trace owner fields: %w", err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_recall_trace_owner_created ON recall_trace(owner_id, created_at DESC)`); err != nil {
		return fmt.Errorf("failed to initialize recall trace owner index: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
DROP INDEX IF EXISTS idx_l1_daily_digest_date_category;
CREATE UNIQUE INDEX IF NOT EXISTS idx_l1_daily_digest_date_category_slot ON l1_daily_digest(digest_date, category, digest_slot);
CREATE INDEX IF NOT EXISTS idx_l1_daily_digest_category_created ON l1_daily_digest(category, digest_slot, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_l1_monthly_highlight_month_category ON l1_monthly_highlight(month, category);
CREATE INDEX IF NOT EXISTS idx_l1_monthly_highlight_category_updated ON l1_monthly_highlight(category, updated_at DESC);
`); err != nil {
		return fmt.Errorf("failed to initialize l1 daily digest slot indexes: %w", err)
	}
	return nil
}
