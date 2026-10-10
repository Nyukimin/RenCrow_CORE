package l1sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

func TestSearchKnowledgeItemsForCategoryRecallVerifiesTrimmedUnicodeQuote(t *testing.T) {
	store := newKnowledgeRecallTestStore(t)
	raw := "先行文\n引用元🧭の要約\n末尾"
	summaryDraft := "  引用元🧭の要約  "
	id := insertKnowledgeRecallTestItem(t, store, "kb:general:unicode", raw, summaryDraft, `{"scope":"user:ren"}`)
	backfillKnowledgeRecallRaw(t, store)

	items, err := store.SearchKnowledgeItemsForCategoryRecall(knowledgeRecallUserContext("ren"), "general", "引用元", 3, allowKnowledgeRecallItem)
	if err != nil || len(items) != 1 {
		t.Fatalf("search items=%+v err=%v", items, err)
	}
	item := items[0]
	if item.ID != id || item.SourceFailure != "" || item.PromptSource == nil {
		t.Fatalf("quote source result=%+v", item)
	}
	start := strings.Index(raw, strings.TrimSpace(summaryDraft))
	rawHash := domainmemory.SHA256Hex([]byte(raw))
	if item.PromptSource.Owner != "RenCrow_CORE" || item.PromptSource.SourceID == "" ||
		item.PromptSource.RawHash != rawHash || item.PromptSource.ProjectionVersion != "knowledge-recall-quote/v1" ||
		item.PromptSource.Range.Start != uint64(start) || item.PromptSource.Range.End != uint64(start+len(strings.TrimSpace(summaryDraft))) ||
		item.PromptSource.Origin != "unknown" || item.PromptSource.Sequence != 0 {
		t.Fatalf("prompt source=%+v", item.PromptSource)
	}
	if item.PromptSource.RawHash == domainmemory.SHA256Hex([]byte(strings.TrimSpace(summaryDraft))) {
		t.Fatal("whole original Raw hash unexpectedly equals the excerpt hash")
	}
}

func TestSearchKnowledgeItemsForCategoryRecallUsesTypedScopeBeforeRawRead(t *testing.T) {
	store := newKnowledgeRecallTestStore(t)
	raw := "prefix AUTH-ONLY-QUOTE suffix"
	id := insertKnowledgeRecallTestItem(t, store, "kb:general:private", raw, "AUTH-ONLY-QUOTE", `{"scope":"user:ren"}`)
	backfillKnowledgeRecallRaw(t, store)
	rawID := knowledgeRecallRawID(t, store, id)
	corruptInlineRawForTest(t, store, rawID, []byte(strings.Repeat("x", len(raw))))

	// An authenticated agent with user access but no internal grant may search
	// the user's Knowledge projection, but cannot open its private Common Raw.
	ctx := domaintool.WithToolExecutionScope(context.Background(), domaintool.ToolExecutionScope{
		RequestID: "quote-agent-without-internal", ActorKind: domaintool.ActorKindAgent, ActorID: "mio",
		AuthenticatedUserID: "ren", AllowedDataScopes: []string{domaintool.DataScopeUser},
		AuthenticationSource: domaintool.AuthenticationSourceAgentOrchestrator,
	})
	items, err := store.SearchKnowledgeItemsForCategoryRecall(ctx, "general", "AUTH-ONLY", 3, allowKnowledgeRecallItem)
	if err != nil || len(items) != 1 || items[0].SourceFailure != L1KnowledgeSourceFailureScopeDenied || items[0].PromptSource != nil {
		t.Fatalf("agent source denial items=%+v err=%v", items, err)
	}
	if items[0].SourceFailure == L1KnowledgeSourceFailureInvalid {
		t.Fatal("raw payload was inspected before the authenticated internal grant check")
	}

	otherUser, err := store.SearchKnowledgeItemsForCategoryRecall(knowledgeRecallUserContext("other"), "general", "AUTH-ONLY", 3, allowKnowledgeRecallItem)
	if err != nil || len(otherUser) != 0 {
		t.Fatalf("other user received private Knowledge item: items=%+v err=%v", otherUser, err)
	}

	ownerItems, err := store.SearchKnowledgeItemsForCategoryRecall(knowledgeRecallUserContext("ren"), "general", "AUTH-ONLY", 3, allowKnowledgeRecallItem)
	if err != nil || len(ownerItems) != 1 || ownerItems[0].SourceFailure != L1KnowledgeSourceFailureInvalid || ownerItems[0].PromptSource != nil {
		t.Fatalf("owner should discover changed Raw bytes as invalid: items=%+v err=%v", ownerItems, err)
	}
}

func TestSearchKnowledgeItemsForCategoryRecallKeepsPublicAndSameUserRowsButDeniesOtherOwnerRaw(t *testing.T) {
	store := newKnowledgeRecallTestStore(t)
	privateID := insertKnowledgeRecallTestItem(t, store, "kb:general:private-scope", "evidence SHARED-QUOTE", "SHARED-QUOTE", `{"scope":"user:ren"}`)
	publicID := insertKnowledgeRecallTestItem(t, store, "kb:general:public-scope", "prefix SHARED-QUOTE suffix", "SHARED-QUOTE", `{"scope":"public"}`)
	backfillKnowledgeRecallRaw(t, store)

	ownerItems, err := store.SearchKnowledgeItemsForCategoryRecall(knowledgeRecallUserContext("ren"), "general", "SHARED-QUOTE", 3, allowKnowledgeRecallItem)
	if err != nil || len(ownerItems) != 2 {
		t.Fatalf("authenticated owner should retain public and own Knowledge rows: items=%+v err=%v", ownerItems, err)
	}
	for _, item := range ownerItems {
		if item.PromptSource == nil || item.SourceFailure != "" {
			t.Fatalf("owner quote proof missing for %s: %+v", item.ID, item)
		}
	}
	if ownerItems[0].ID != privateID && ownerItems[1].ID != privateID || ownerItems[0].ID != publicID && ownerItems[1].ID != publicID {
		t.Fatalf("unexpected selected Knowledge rows: %+v", ownerItems)
	}

	otherItems, err := store.SearchKnowledgeItemsForCategoryRecall(knowledgeRecallUserContext("other"), "general", "SHARED-QUOTE", 3, allowKnowledgeRecallItem)
	if err != nil || len(otherItems) != 1 || otherItems[0].ID != publicID ||
		otherItems[0].SourceFailure != L1KnowledgeSourceFailureScopeDenied || otherItems[0].PromptSource != nil {
		t.Fatalf("public Knowledge selection must not grant another user's private Raw access: items=%+v err=%v", otherItems, err)
	}
}

func TestSearchKnowledgeItemsForCategoryRecallFailsClosedOnBadProof(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *L1SQLiteStore, string)
	}{
		{
			name: "projection receipt hash",
			mutate: func(t *testing.T, store *L1SQLiteStore, itemID string) {
				dropKnowledgeRecallTriggers(t, store, "l1_raw_projection_receipt")
				if _, err := store.db.Exec(`UPDATE l1_raw_projection_receipt SET output_sha256 = ? WHERE output_record_id = ? AND status = 'completed'`, strings.Repeat("a", 64), itemID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "raw hash mismatch",
			mutate: func(t *testing.T, store *L1SQLiteStore, itemID string) {
				dropKnowledgeRecallTriggers(t, store, "l1_raw_record")
				rawID := knowledgeRecallRawID(t, store, itemID)
				if _, err := store.db.Exec(`UPDATE l1_raw_record SET inline_payload = ? WHERE raw_record_id = ?`, []byte(strings.Repeat("x", len("evidence QUOTE bytes"))), rawID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "extra lifecycle event",
			mutate: func(t *testing.T, store *L1SQLiteStore, itemID string) {
				rawID := knowledgeRecallRawID(t, store, itemID)
				var manifestID, ownerID, scope, requestID, actorID string
				if err := store.db.QueryRow(`SELECT manifest_id, owner_id, scope, request_id, actor_id FROM l1_raw_state_event WHERE raw_record_id = ?`, rawID).
					Scan(&manifestID, &ownerID, &scope, &requestID, &actorID); err != nil {
					t.Fatal(err)
				}
				if _, err := store.db.Exec(`INSERT INTO l1_raw_state_event (state_event_id, raw_record_id, manifest_id, event_type, event_hash, owner_id, scope, request_id, actor_id, reason_code, payload_json, created_at) VALUES (?, ?, ?, 'correction', ?, ?, ?, ?, ?, 'test', '{}', ?)`,
					"extra-lifecycle-event", rawID, manifestID, strings.Repeat("b", 64), ownerID, scope, requestID, actorID, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "source policy",
			mutate: func(t *testing.T, store *L1SQLiteStore, itemID string) {
				dropKnowledgeRecallTriggers(t, store, "l1_raw_record")
				rawID := knowledgeRecallRawID(t, store, itemID)
				if _, err := store.db.Exec(`UPDATE l1_raw_record SET rights = 'unknown' WHERE raw_record_id = ?`, rawID); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newKnowledgeRecallTestStore(t)
			id := insertKnowledgeRecallTestItem(t, store, "kb:general:proof", "evidence QUOTE bytes", "QUOTE", `{"scope":"user:ren"}`)
			backfillKnowledgeRecallRaw(t, store)
			test.mutate(t, store, id)
			items, err := store.SearchKnowledgeItemsForCategoryRecall(knowledgeRecallUserContext("ren"), "general", "QUOTE", 3, allowKnowledgeRecallItem)
			if err != nil || len(items) != 1 || items[0].SourceFailure != L1KnowledgeSourceFailureInvalid || items[0].PromptSource != nil {
				t.Fatalf("tampered proof items=%+v err=%v", items, err)
			}
		})
	}
}

func TestValidateKnowledgeRawQuoteIntakeBindsReceiptToHeaderBeforePayloadRead(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*knowledgeRawQuoteHeader, *domainmemory.CommonRawRecordReceipt)
	}{
		{
			name: "storage kind mismatch",
			mutate: func(_ *knowledgeRawQuoteHeader, record *domainmemory.CommonRawRecordReceipt) {
				record.StorageKind = domainmemory.CommonRawStorageObject
				record.ObjectRef = "objects/other"
			},
		},
		{
			name: "object ref mismatch",
			mutate: func(header *knowledgeRawQuoteHeader, record *domainmemory.CommonRawRecordReceipt) {
				header.rawStorage = domainmemory.CommonRawStorageObject
				header.rawObjectRef = "objects/canonical"
				record.StorageKind = domainmemory.CommonRawStorageObject
				record.ObjectRef = "objects/other"
			},
		},
		{
			name: "header size mismatch",
			mutate: func(header *knowledgeRawQuoteHeader, _ *domainmemory.CommonRawRecordReceipt) {
				header.rawSize++
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			item := L1KnowledgeItem{ID: "kb:general:receipt", RawText: "verified original bytes"}
			rawHash := domainmemory.SHA256Hex([]byte(item.RawText))
			now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
			header := knowledgeRawQuoteHeader{
				rawRecordID: "raw-record", manifestID: "manifest", rawHash: rawHash,
				rawStorage: domainmemory.CommonRawStorageInline, rawSize: int64(len(item.RawText)),
				manifestHash: "manifest-hash", manifestRequestID: "request", manifestStatus: string(domainmemory.CommonRawStateCompleted),
				manifestSourceCount: 1, manifestAssetCount: 0, manifestCreatedAt: now, manifestUpdatedAt: now,
				manifestCheckpoint: `{"manifest_id":"manifest","source_count":1,"asset_count":0,"status":"completed"}`,
			}
			record := domainmemory.CommonRawRecordReceipt{
				RawRecordID: "raw-record", SourceRecordID: item.ID, ContentSHA256: rawHash,
				ContentSize: int64(len(item.RawText)), StorageKind: domainmemory.CommonRawStorageInline,
			}
			receipt := domainmemory.CommonRawIntakeReceipt{
				RequestID: "request", ManifestID: "manifest", Status: domainmemory.CommonRawStateCompleted,
				ManifestSHA256: "manifest-hash", SourceCount: 1, Checkpoint: "completed",
				Records: []domainmemory.CommonRawRecordReceipt{record}, CreatedAt: now,
			}
			receiptJSON, err := json.Marshal(receipt)
			if err != nil {
				t.Fatal(err)
			}
			header.manifestReceipt = string(receiptJSON)
			projection := knowledgeQuoteProjectionReceipt{rawRecordID: "raw-record", inputHash: rawHash}

			if err := validateKnowledgeRawQuoteIntake(header, item, projection); err != nil {
				t.Fatalf("valid intake/header binding rejected: %v", err)
			}
			test.mutate(&header, &record)
			receipt.Records[0] = record
			receiptJSON, err = json.Marshal(receipt)
			if err != nil {
				t.Fatal(err)
			}
			header.manifestReceipt = string(receiptJSON)
			if err := validateKnowledgeRawQuoteIntake(header, item, projection); domainmemory.CommonRawErrorCodeOf(err) != domainmemory.CommonRawErrorInvalid {
				t.Fatalf("mismatched intake/header binding should fail invalid before payload read, got %v", err)
			}
		})
	}
}

func TestVerifyKnowledgeRawQuoteSourceRejectsInlinePayloadBeforeReadingMismatchedSize(t *testing.T) {
	store := newKnowledgeRecallTestStore(t)
	raw := "original inline quote source"
	id := insertKnowledgeRecallTestItem(t, store, "kb:general:inline-size", raw, "inline quote", `{"scope":"user:ren"}`)
	backfillKnowledgeRecallRaw(t, store)
	rawID := knowledgeRecallRawID(t, store, id)
	corruptInlineRawForTest(t, store, rawID, []byte(strings.Repeat("x", domainmemory.CommonRawMaxInlinePayloadSize+1)))

	tx, err := store.db.BeginTx(knowledgeRecallUserContext("ren"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	store.rawMu.Lock()
	root := store.rawSourceRoot
	store.rawMu.Unlock()
	_, err = verifyKnowledgeRawQuoteSource(knowledgeRecallUserContext("ren"), tx, root, L1KnowledgeItem{
		ID: id, RawText: raw, RawHash: domainmemory.SHA256Hex([]byte(raw)), SummaryDraft: "inline quote",
	}, "ren")
	if domainmemory.CommonRawErrorCodeOf(err) != domainmemory.CommonRawErrorInvalid {
		t.Fatalf("mismatched inline payload must be rejected by the bounded pre-read predicate, got %v", err)
	}
}

func TestSearchKnowledgeItemsForCategoryRecallLeavesNonQuoteAndRawFallbackSourceNull(t *testing.T) {
	store := newKnowledgeRecallTestStore(t)
	nonQuoteID := insertKnowledgeRecallTestItem(t, store, "kb:general:nonquote", "original evidence", "generated nonquote label", `{"scope":"user:ren"}`)
	fallbackID := insertKnowledgeRecallTestItem(t, store, "kb:general:fallback", "raw fallback phrase", "", `{"scope":"user:ren"}`)
	backfillKnowledgeRecallRaw(t, store)
	items, err := store.SearchKnowledgeItemsForCategoryRecall(knowledgeRecallUserContext("ren"), "general", "phrase", 3, allowKnowledgeRecallItem)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != fallbackID || items[0].PromptSource != nil || items[0].SourceFailure != "" {
		t.Fatalf("Raw fallback should be unverified and source-null: %+v", items)
	}
	items, err = store.SearchKnowledgeItemsForCategoryRecall(knowledgeRecallUserContext("ren"), "general", "label", 3, allowKnowledgeRecallItem)
	if err != nil || len(items) != 1 || items[0].ID != nonQuoteID || items[0].PromptSource != nil || items[0].SourceFailure != "" {
		t.Fatalf("nonquote summary should remain source-null: items=%+v err=%v", items, err)
	}
}

func TestSearchKnowledgeItemsForCategoryRecallReadsObjectSource(t *testing.T) {
	store := newKnowledgeRecallTestStore(t)
	root := filepath.Join(t.TempDir(), "raw-objects")
	if err := store.SetCommonRawSourceRoot(root); err != nil {
		t.Fatal(err)
	}
	raw := strings.Repeat("long-source-", 6000) + "対象の引用🧵" + strings.Repeat("-tail", 5000)
	id := insertKnowledgeRecallTestItem(t, store, "kb:general:object", raw, " 対象の引用🧵 ", `{"scope":"user:ren"}`)
	backfillKnowledgeRecallRaw(t, store)
	var storage string
	if err := store.db.QueryRow(`SELECT storage_kind FROM l1_raw_record WHERE source_record_id = ?`, id).Scan(&storage); err != nil {
		t.Fatal(err)
	}
	if storage != domainmemory.CommonRawStorageObject {
		t.Fatalf("storage=%q, want object", storage)
	}
	items, err := store.SearchKnowledgeItemsForCategoryRecall(knowledgeRecallUserContext("ren"), "general", "対象の引用", 3, allowKnowledgeRecallItem)
	if err != nil || len(items) != 1 || items[0].PromptSource == nil || items[0].SourceFailure != "" {
		t.Fatalf("object quote result=%+v err=%v", items, err)
	}
}

func TestSearchKnowledgeItemsForCategoryRecallRequiresEligibilityBeforeRawRead(t *testing.T) {
	store := newKnowledgeRecallTestStore(t)
	raw := "prefix AUTH-ONLY-ELIGIBILITY suffix"
	id := insertKnowledgeRecallTestItem(t, store, "kb:general:eligibility", raw, "AUTH-ONLY-ELIGIBILITY", `{"scope":"user:ren"}`)
	backfillKnowledgeRecallRaw(t, store)
	rawID := knowledgeRecallRawID(t, store, id)
	corruptInlineRawForTest(t, store, rawID, []byte(strings.Repeat("x", len(raw))))

	if items, err := store.SearchKnowledgeItemsForCategoryRecall(knowledgeRecallUserContext("ren"), "general", "AUTH-ONLY-ELIGIBILITY", 3, nil); err == nil || len(items) != 0 {
		t.Fatalf("nil eligibility callback must fail closed: items=%+v err=%v", items, err)
	}
	items, err := store.SearchKnowledgeItemsForCategoryRecall(
		knowledgeRecallUserContext("ren"), "general", "AUTH-ONLY-ELIGIBILITY", 3,
		func(L1KnowledgeItem) bool { return false },
	)
	if err != nil || len(items) != 0 {
		t.Fatalf("ineligible record must be excluded before corrupt Raw is opened: items=%+v err=%v", items, err)
	}
}

func newKnowledgeRecallTestStore(t *testing.T) *L1SQLiteStore {
	t.Helper()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func allowKnowledgeRecallItem(L1KnowledgeItem) bool { return true }

func insertKnowledgeRecallTestItem(t *testing.T, store *L1SQLiteStore, id, raw, summary, meta string) string {
	t.Helper()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	hash := domainmemory.SHA256Hex([]byte(raw))
	_, err := store.db.Exec(`INSERT INTO l1_knowledge_item
(id, staging_id, domain, title, source_id, source_url, raw_text, raw_hash, summary_draft, keywords_json, license_note, meta_json, created_at, updated_at)
VALUES (?, ?, 'general', ?, 'recall-test', 'https://example.test/knowledge', ?, ?, ?, '[]', 'owner', ?, ?, ?)`,
		id, "staging-"+id, "Title "+id, raw, hash, summary, meta, now, now)
	if err != nil {
		t.Fatalf("insert Knowledge item: %v", err)
	}
	_, err = store.db.Exec(`INSERT INTO l1_knowledge_item_fts (id, domain, title, raw_text, summary_draft, keywords_text) VALUES (?, 'general', ?, ?, ?, '')`, id, "Title "+id, raw, summary)
	if err != nil {
		t.Fatalf("insert Knowledge FTS item: %v", err)
	}
	return id
}

func backfillKnowledgeRecallRaw(t *testing.T, store *L1SQLiteStore) {
	t.Helper()
	requestID := "knowledge-recall-backfill"
	if _, err := store.BackfillKnowledgeCommonRaw(commonRawTestContext(t, requestID), requestID, "ren", "ren", true); err != nil {
		t.Fatalf("BackfillKnowledgeCommonRaw: %v", err)
	}
}

func knowledgeRecallUserContext(userID string) context.Context {
	scope := domaintool.ToolExecutionScope{
		RequestID: "knowledge-recall-" + userID, ActorKind: domaintool.ActorKindUser,
		ActorID: userID, AuthenticatedUserID: userID,
		AllowedDataScopes: []string{domaintool.DataScopeUser}, AuthenticationSource: domaintool.AuthenticationSourceHTTP,
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}

func knowledgeRecallRawID(t *testing.T, store *L1SQLiteStore, itemID string) string {
	t.Helper()
	var rawID string
	if err := store.db.QueryRow(`SELECT raw_record_id FROM l1_raw_record WHERE source_record_id = ?`, itemID).Scan(&rawID); err != nil {
		t.Fatal(err)
	}
	return rawID
}

func corruptInlineRawForTest(t *testing.T, store *L1SQLiteStore, rawID string, payload []byte) {
	t.Helper()
	dropKnowledgeRecallTriggers(t, store, "l1_raw_record")
	if _, err := store.db.Exec(`UPDATE l1_raw_record SET inline_payload = ? WHERE raw_record_id = ?`, payload, rawID); err != nil {
		t.Fatal(err)
	}
}

func dropKnowledgeRecallTriggers(t *testing.T, store *L1SQLiteStore, table string) {
	t.Helper()
	rows, err := store.db.Query(`SELECT name FROM sqlite_master WHERE type = 'trigger' AND tbl_name = ?`, table)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if _, err := store.db.Exec(`DROP TRIGGER "` + strings.ReplaceAll(name, `"`, `""`) + `"`); err != nil {
			t.Fatalf("drop test trigger %q: %v", name, err)
		}
	}
}
