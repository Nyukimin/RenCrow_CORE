package storagehost

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/archivesqlite"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const archiveGroupTestToken = "storagehost-archive-test-token"

type archiveGroupFixture struct {
	root   string
	store  *archivesqlite.ArchiveSQLiteStore
	host   *Handler
	server *httptest.Server
	rpc    *Client
	client *ArchiveStoreClient
}

func newArchiveGroupFixture(t *testing.T) *archiveGroupFixture {
	t.Helper()
	f := &archiveGroupFixture{root: t.TempDir()}
	f.open(t, false)
	t.Cleanup(f.close)
	return f
}

func (f *archiveGroupFixture) open(t *testing.T, reuseClient bool) {
	f.openWithOwnerFactory(t, reuseClient, nil)
}

func (f *archiveGroupFixture) openWithOwnerFactory(t *testing.T, reuseClient bool, ownerFactory func(*archivesqlite.ArchiveSQLiteStore) ArchiveGroupOwner) {
	t.Helper()
	var err error
	f.store, err = archivesqlite.NewArchiveSQLiteStore(filepath.Join(f.root, "archive.db"))
	if err != nil {
		t.Fatalf("NewArchiveSQLiteStore: %v", err)
	}
	f.host, err = NewHandler(HandlerConfig{Token: archiveGroupTestToken, JournalDir: filepath.Join(f.root, "journal")})
	if err != nil {
		_ = f.store.Close()
		t.Fatalf("NewHandler: %v", err)
	}
	var owner ArchiveGroupOwner = f.store
	if ownerFactory != nil {
		owner = ownerFactory(f.store)
	}
	if err := RegisterArchiveGroup(f.host, owner); err != nil {
		_ = f.host.Close()
		_ = f.store.Close()
		t.Fatalf("RegisterArchiveGroup: %v", err)
	}
	f.server = httptest.NewServer(f.host)
	if !reuseClient {
		f.rpc, err = NewClient(ClientConfig{Endpoint: f.server.URL, Token: archiveGroupTestToken, HTTPClient: f.server.Client()})
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
	} else {
		f.rpc.endpoint = f.server.URL + RPCPath
		f.rpc.httpClient = f.server.Client()
	}
	if err := f.rpc.Handshake(context.Background()); err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	f.client = NewArchiveStoreClient(f.rpc)
}

func (f *archiveGroupFixture) close() {
	if f.server != nil {
		f.server.Close()
		f.server = nil
	}
	if f.host != nil {
		_ = f.host.Close()
		f.host = nil
	}
	if f.store != nil {
		_ = f.store.Close()
		f.store = nil
	}
}

func (f *archiveGroupFixture) restart(t *testing.T) {
	t.Helper()
	f.close()
	f.open(t, true)
}

func TestArchiveGroupRegistersOnlyClosedOperations(t *testing.T) {
	f := newArchiveGroupFixture(t)
	info, err := f.rpc.Contract(context.Background())
	if err != nil {
		t.Fatalf("Contract: %v", err)
	}
	want := map[string]bool{
		"save_thread_summary_with_receipt": true,
		"get_thread_summary":               false,
		"get_session_history":              false,
		"search_by_domain":                 false,
		"search_knowledge_archive_fts":     false,
		"archive_user_memory_with_receipt": true,
		"find_user_memory_archive":         false,
		"find_archive_request_receipt":     false,
	}
	got := make(map[string]bool)
	for _, operation := range info.Operations {
		if operation.Group == GroupArchive {
			got[operation.Op] = operation.Mutating
		}
	}
	if len(got) != len(want) {
		t.Fatalf("archive operations=%v, want exactly %v", got, want)
	}
	for operation, mutating := range want {
		if got[operation] != mutating {
			t.Fatalf("archive operation %q mutating=%v, want %v", operation, got[operation], mutating)
		}
		if mutating {
			if _, ok := f.host.recoverable[opKey(GroupArchive, operation)]; !ok {
				t.Errorf("archive mutation %q lacks owner reconciliation", operation)
			}
		}
	}
}

func TestArchiveStoreClientRoundTripsRealArchiveSQLite(t *testing.T) {
	f := newArchiveGroupFixture(t)
	ctx := context.Background()
	summary, receipt := archiveGroupThreadSummary()
	if err := f.client.SaveThreadSummaryWithReceipt(ctx, summary, receipt); err != nil {
		t.Fatalf("SaveThreadSummaryWithReceipt: %v", err)
	}
	got, err := f.client.GetThreadSummary(ctx, summary.ThreadID)
	if err != nil || got.ThreadID != summary.ThreadID || got.Summary != summary.Summary || got.Receipt == nil || got.Receipt.EvidenceSHA256 != receipt.EvidenceSHA256 {
		t.Fatalf("GetThreadSummary=%+v err=%v", got, err)
	}
	history, err := f.client.GetSessionHistory(ctx, summary.SessionID, 10)
	if err != nil || len(history) != 1 || history[0].ThreadID != summary.ThreadID {
		t.Fatalf("GetSessionHistory=%+v err=%v", history, err)
	}
	byDomain, err := f.client.SearchByDomain(ctx, summary.Domain, 10)
	if err != nil || len(byDomain) != 1 || byDomain[0].ThreadID != summary.ThreadID {
		t.Fatalf("SearchByDomain=%+v err=%v", byDomain, err)
	}

	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	knowledge := []l1sqlite.L1KnowledgeItem{{
		ID: "kb-space", StagingID: "stage-space", Domain: "movie", Title: "Interstellar", SourceID: "manual",
		RawText: "重力と時間を扱う宇宙映画", RawHash: "hash-space", SummaryDraft: "重力と時間", Keywords: []string{"宇宙", "重力"},
		LicenseNote: "manual", Meta: map[string]interface{}{}, CreatedAt: now, UpdatedAt: now,
	}}
	if err := f.store.ArchiveL1KnowledgeItems(ctx, knowledge); err != nil {
		t.Fatalf("ArchiveL1KnowledgeItems seed: %v", err)
	}
	items, err := f.client.SearchKnowledgeArchiveFTS(ctx, "movie", "重力", 10)
	if err != nil || len(items) != 1 || items[0].ID != "kb-space" {
		t.Fatalf("SearchKnowledgeArchiveFTS=%+v err=%v", items, err)
	}

	item := archiveGroupUserMemoryEvent("memory-archive-1", "ren", now)
	productReceipt := l1sqlite.OwnerArchiveRequest{
		RequestID: "product-request-archive-1", UserID: "ren", ActorID: "mio", PayloadHash: archiveGroupPayloadHash,
		MemoryID: item.ID, CreatedAt: now,
	}
	replayed, err := f.client.ArchiveUserMemoryWithReceipt(ctx, item, productReceipt)
	if err != nil || replayed {
		t.Fatalf("ArchiveUserMemoryWithReceipt replay=%v err=%v, want first write", replayed, err)
	}
	archived, found, err := f.client.FindUserMemoryArchive(ctx, "ren", item.ID)
	if err != nil || !found || archived.ID != item.ID || archived.Namespace != "user:ren" {
		t.Fatalf("FindUserMemoryArchive=%+v found=%v err=%v", archived, found, err)
	}
	otherUser, found, err := f.client.FindUserMemoryArchive(ctx, "other", item.ID)
	if err != nil || found || otherUser.ID != "" {
		t.Fatalf("cross-user FindUserMemoryArchive=%+v found=%v err=%v", otherUser, found, err)
	}
	storedReceipt, found, err := f.client.FindArchiveRequestReceipt(ctx, "ren", productReceipt.RequestID)
	if err != nil || !found || storedReceipt.RequestID != productReceipt.RequestID || storedReceipt.UserID != "ren" || storedReceipt.ActorID != "mio" {
		t.Fatalf("FindArchiveRequestReceipt=%+v found=%v err=%v", storedReceipt, found, err)
	}
	otherReceipt, found, err := f.client.FindArchiveRequestReceipt(ctx, "other", productReceipt.RequestID)
	if err != nil || found || otherReceipt.RequestID != "" {
		t.Fatalf("cross-user FindArchiveRequestReceipt=%+v found=%v err=%v", otherReceipt, found, err)
	}
}

type archiveRecoveryOwner struct {
	ArchiveGroupRecoveryOwner
	blockReconcile bool
	failAfterWrite bool
	summaryWrites  int
	memoryWrites   int
}

func (o *archiveRecoveryOwner) SaveThreadSummaryWithReceipt(ctx context.Context, summary *domconv.ThreadSummary, receipt *domconv.ThreadSummaryReceipt) error {
	o.summaryWrites++
	if err := o.ArchiveGroupRecoveryOwner.SaveThreadSummaryWithReceipt(ctx, summary, receipt); err != nil {
		return err
	}
	if o.failAfterWrite {
		return errors.New("injected typed owner failure after SQLite commit")
	}
	return nil
}

func (o *archiveRecoveryOwner) ArchiveUserMemoryWithReceipt(ctx context.Context, event l1sqlite.L1MemoryEvent, receipt l1sqlite.OwnerArchiveRequest) (bool, error) {
	o.memoryWrites++
	replayed, err := o.ArchiveGroupRecoveryOwner.ArchiveUserMemoryWithReceipt(ctx, event, receipt)
	if err != nil {
		return false, err
	}
	if o.failAfterWrite {
		return false, errors.New("injected typed owner failure after SQLite commit")
	}
	return replayed, nil
}

func (o *archiveRecoveryOwner) ArchiveUserMemoryWithReceiptAndOperation(ctx context.Context, event l1sqlite.L1MemoryEvent, receipt l1sqlite.OwnerArchiveRequest, opID, payloadSHA256 string) (bool, error) {
	o.memoryWrites++
	replayed, err := o.ArchiveGroupRecoveryOwner.ArchiveUserMemoryWithReceiptAndOperation(ctx, event, receipt, opID, payloadSHA256)
	if err != nil {
		return false, err
	}
	if o.failAfterWrite {
		return false, errors.New("injected typed owner failure after SQLite commit")
	}
	return replayed, nil
}

func (o *archiveRecoveryOwner) ReconcileThreadSummaryWrite(ctx context.Context, summary *domconv.ThreadSummary, receipt *domconv.ThreadSummaryReceipt) (bool, error) {
	if o.blockReconcile {
		return false, errors.New("injected owner query failure")
	}
	return o.ArchiveGroupRecoveryOwner.ReconcileThreadSummaryWrite(ctx, summary, receipt)
}

func (o *archiveRecoveryOwner) ReconcileUserMemoryArchiveWrite(ctx context.Context, event l1sqlite.L1MemoryEvent, receipt l1sqlite.OwnerArchiveRequest, opID, payloadSHA256 string) (bool, bool, error) {
	if o.blockReconcile {
		return false, false, errors.New("injected owner query failure")
	}
	return o.ArchiveGroupRecoveryOwner.ReconcileUserMemoryArchiveWrite(ctx, event, receipt, opID, payloadSHA256)
}

func TestArchiveMutationsReconcileCommitGapAfterHandlerAndOwnerReopen(t *testing.T) {
	summary, summaryReceipt := archiveGroupThreadSummary()
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	event := archiveGroupUserMemoryEvent("memory-recovery", "ren", now)
	productReceipt := l1sqlite.OwnerArchiveRequest{RequestID: "product-request-recovery", UserID: "ren", ActorID: "ren", PayloadHash: archiveGroupPayloadHash, MemoryID: event.ID, CreatedAt: now}
	for _, test := range []struct {
		name string
		call func(*ArchiveStoreClient) (any, error)
		want func(*testing.T, any)
	}{
		{
			name: "thread_summary",
			call: func(client *ArchiveStoreClient) (any, error) {
				return nil, client.SaveThreadSummaryWithReceipt(context.Background(), summary, summaryReceipt)
			},
			want: func(t *testing.T, result any) {
				if result != nil {
					t.Fatalf("summary write result=%#v, want nil", result)
				}
			},
		},
		{
			name: "user_memory",
			call: func(client *ArchiveStoreClient) (any, error) {
				return client.ArchiveUserMemoryWithReceipt(context.Background(), event, productReceipt)
			},
			want: func(t *testing.T, result any) {
				replayed, ok := result.(bool)
				if !ok || replayed {
					t.Fatalf("user-memory replay result=%#v, want original false", result)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := &archiveGroupFixture{root: t.TempDir()}
			firstOwner := &archiveRecoveryOwner{blockReconcile: true}
			f.openWithOwnerFactory(t, false, func(store *archivesqlite.ArchiveSQLiteStore) ArchiveGroupOwner {
				firstOwner.ArchiveGroupRecoveryOwner = store
				return firstOwner
			})
			t.Cleanup(f.close)

			const opID = "archive-commit-gap-recovery"
			f.rpc.opID = opID
			f.host.crashAfterCommitFor = opID
			_, err := test.call(f.client)
			if archiveGroupErrorCode(err) != ErrorCodeOutcomeUnknown {
				t.Fatalf("commit-gap call err=%v, want outcome_unknown", err)
			}
			if (test.name == "thread_summary" && firstOwner.summaryWrites != 1) || (test.name == "user_memory" && firstOwner.memoryWrites != 1) {
				t.Fatalf("first owner mutation calls summary=%d memory=%d, want exactly one selected write", firstOwner.summaryWrites, firstOwner.memoryWrites)
			}

			f.close()
			secondOwner := &archiveRecoveryOwner{}
			f.openWithOwnerFactory(t, true, func(store *archivesqlite.ArchiveSQLiteStore) ArchiveGroupOwner {
				secondOwner.ArchiveGroupRecoveryOwner = store
				return secondOwner
			})
			f.rpc.opID = opID
			result, err := test.call(f.client)
			if err != nil {
				t.Fatalf("retry after owner reopen: %v", err)
			}
			test.want(t, result)
			if test.name == "thread_summary" {
				stored, err := f.store.GetThreadSummary(context.Background(), summary.ThreadID)
				if err != nil || stored.Summary != summary.Summary || stored.Receipt == nil || stored.Receipt.EvidenceSHA256 != summaryReceipt.EvidenceSHA256 {
					t.Fatalf("reopened thread summary=%+v err=%v", stored, err)
				}
				if secondOwner.summaryWrites != 0 {
					t.Fatalf("reopened owner executed summary mutation %d times; want recovery without re-execution", secondOwner.summaryWrites)
				}
			} else {
				storedEvent, found, err := f.store.FindUserMemoryArchive(context.Background(), "ren", event.ID)
				if err != nil || !found || storedEvent.Message != event.Message {
					t.Fatalf("reopened user memory=%+v found=%v err=%v", storedEvent, found, err)
				}
				storedRequest, found, err := f.store.FindArchiveRequestReceipt(context.Background(), "ren", productReceipt.RequestID)
				if err != nil || !found || storedRequest.MemoryID != event.ID {
					t.Fatalf("reopened product receipt=%+v found=%v err=%v", storedRequest, found, err)
				}
				if secondOwner.memoryWrites != 0 {
					t.Fatalf("reopened owner executed user-memory mutation %d times; want recovery without re-execution", secondOwner.memoryWrites)
				}
			}
		})
	}
}

func TestArchiveTypedOwnerErrorsReconcileAfterRestart(t *testing.T) {
	summary, summaryReceipt := archiveGroupThreadSummary()
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	event := archiveGroupUserMemoryEvent("memory-typed-error", "ren", now)
	productReceipt := l1sqlite.OwnerArchiveRequest{RequestID: "product-request-typed-error", UserID: "ren", ActorID: "ren", PayloadHash: archiveGroupPayloadHash, MemoryID: event.ID, CreatedAt: now}
	for _, test := range []struct {
		name string
		call func(*ArchiveStoreClient) (any, error)
		want func(*testing.T, any)
	}{
		{
			name: "thread_summary",
			call: func(client *ArchiveStoreClient) (any, error) {
				return nil, client.SaveThreadSummaryWithReceipt(context.Background(), summary, summaryReceipt)
			},
			want: func(t *testing.T, result any) {
				if result != nil {
					t.Fatalf("summary write result=%#v, want nil", result)
				}
			},
		},
		{
			name: "user_memory",
			call: func(client *ArchiveStoreClient) (any, error) {
				return client.ArchiveUserMemoryWithReceipt(context.Background(), event, productReceipt)
			},
			want: func(t *testing.T, result any) {
				replayed, ok := result.(bool)
				if !ok || replayed {
					t.Fatalf("user-memory replay result=%#v, want original false", result)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := &archiveGroupFixture{root: t.TempDir()}
			firstOwner := &archiveRecoveryOwner{failAfterWrite: true}
			f.openWithOwnerFactory(t, false, func(store *archivesqlite.ArchiveSQLiteStore) ArchiveGroupOwner {
				firstOwner.ArchiveGroupRecoveryOwner = store
				return firstOwner
			})
			t.Cleanup(f.close)

			const opID = "archive-typed-error-recovery"
			f.rpc.opID = opID
			_, err := test.call(f.client)
			if archiveGroupErrorCode(err) != ErrorCodeOutcomeUnknown {
				t.Fatalf("post-commit typed owner error=%v, want outcome_unknown", err)
			}
			if (test.name == "thread_summary" && firstOwner.summaryWrites != 1) || (test.name == "user_memory" && firstOwner.memoryWrites != 1) {
				t.Fatalf("first owner mutation calls summary=%d memory=%d, want exactly one selected write", firstOwner.summaryWrites, firstOwner.memoryWrites)
			}

			f.close()
			secondOwner := &archiveRecoveryOwner{}
			f.openWithOwnerFactory(t, true, func(store *archivesqlite.ArchiveSQLiteStore) ArchiveGroupOwner {
				secondOwner.ArchiveGroupRecoveryOwner = store
				return secondOwner
			})
			f.rpc.opID = opID
			result, err := test.call(f.client)
			if err != nil {
				t.Fatalf("retry after typed error and restart: %v", err)
			}
			test.want(t, result)
			if secondOwner.summaryWrites != 0 || secondOwner.memoryWrites != 0 {
				t.Fatalf("recovery re-executed owner mutation: summary=%d memory=%d", secondOwner.summaryWrites, secondOwner.memoryWrites)
			}
		})
	}
}

func TestArchiveUserMemoryRecoveryPreservesExistingProductReplayResult(t *testing.T) {
	f := &archiveGroupFixture{root: t.TempDir()}
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	event := archiveGroupUserMemoryEvent("memory-existing-product-request", "ren", now)
	productReceipt := l1sqlite.OwnerArchiveRequest{
		RequestID: "product-request-existing", UserID: "ren", ActorID: "mio", PayloadHash: archiveGroupPayloadHash,
		MemoryID: event.ID, CreatedAt: now,
	}
	firstOwner := &archiveRecoveryOwner{blockReconcile: true}
	f.openWithOwnerFactory(t, false, func(store *archivesqlite.ArchiveSQLiteStore) ArchiveGroupOwner {
		if _, err := store.ArchiveUserMemoryWithReceipt(context.Background(), event, productReceipt); err != nil {
			t.Fatalf("preseed exact product archive receipt: %v", err)
		}
		firstOwner.ArchiveGroupRecoveryOwner = store
		return firstOwner
	})
	t.Cleanup(f.close)

	const opID = "archive-existing-product-replay"
	f.rpc.opID = opID
	f.host.crashAfterCommitFor = opID
	if result, err := f.client.ArchiveUserMemoryWithReceipt(context.Background(), event, productReceipt); archiveGroupErrorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("post-commit response loss result=%v err=%v, want outcome_unknown", result, err)
	}
	if firstOwner.memoryWrites != 1 {
		t.Fatalf("first owner ArchiveUserMemoryWithReceipt calls=%d, want one", firstOwner.memoryWrites)
	}

	f.close()
	secondOwner := &archiveRecoveryOwner{}
	f.openWithOwnerFactory(t, true, func(store *archivesqlite.ArchiveSQLiteStore) ArchiveGroupOwner {
		secondOwner.ArchiveGroupRecoveryOwner = store
		return secondOwner
	})
	f.rpc.opID = opID
	replayed, err := f.client.ArchiveUserMemoryWithReceipt(context.Background(), event, productReceipt)
	if err != nil || !replayed {
		t.Fatalf("recovered result replay=%v err=%v, want original true for pre-existing exact product receipt", replayed, err)
	}
	if secondOwner.memoryWrites != 0 {
		t.Fatalf("recovery re-executed owner mutation %d times, want zero", secondOwner.memoryWrites)
	}
}

func TestArchiveUserMemoryRecoveryRejectsTamperedReplayResult(t *testing.T) {
	f := &archiveGroupFixture{root: t.TempDir()}
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	event := archiveGroupUserMemoryEvent("memory-tampered-replay-result", "ren", now)
	productReceipt := l1sqlite.OwnerArchiveRequest{
		RequestID: "product-request-tampered-replay-result", UserID: "ren", ActorID: "mio", PayloadHash: archiveGroupPayloadHash,
		MemoryID: event.ID, CreatedAt: now,
	}
	firstOwner := &archiveRecoveryOwner{}
	f.openWithOwnerFactory(t, false, func(store *archivesqlite.ArchiveSQLiteStore) ArchiveGroupOwner {
		firstOwner.ArchiveGroupRecoveryOwner = store
		return firstOwner
	})
	t.Cleanup(f.close)

	const opID = "archive-tampered-replay-result"
	f.rpc.opID = opID
	f.host.crashAfterCommitFor = opID
	if replayed, err := f.client.ArchiveUserMemoryWithReceipt(context.Background(), event, productReceipt); archiveGroupErrorCode(err) != ErrorCodeOutcomeUnknown || replayed {
		t.Fatalf("post-commit response loss replay=%v err=%v, want false and outcome_unknown", replayed, err)
	}
	if firstOwner.memoryWrites != 1 {
		t.Fatalf("first owner mutation calls=%d, want one", firstOwner.memoryWrites)
	}

	f.close()
	db, err := sql.Open("sqlite", filepath.Join(f.root, "archive.db"))
	if err != nil {
		t.Fatalf("open archive SQLite to tamper operation result: %v", err)
	}
	result, err := db.Exec(`UPDATE conversation_archive_storagehost_user_memory_receipt
SET idempotent_replay = CASE idempotent_replay WHEN 0 THEN 1 ELSE 0 END WHERE op_id = ?`, opID)
	if err != nil {
		_ = db.Close()
		t.Fatalf("flip only stored operation result: %v", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		_ = db.Close()
		t.Fatalf("tampered operation rows=%d err=%v, want one", changed, err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close tampered archive SQLite: %v", err)
	}

	secondOwner := &archiveRecoveryOwner{}
	f.openWithOwnerFactory(t, true, func(store *archivesqlite.ArchiveSQLiteStore) ArchiveGroupOwner {
		secondOwner.ArchiveGroupRecoveryOwner = store
		return secondOwner
	})
	f.rpc.opID = opID
	if replayed, err := f.client.ArchiveUserMemoryWithReceipt(context.Background(), event, productReceipt); archiveGroupErrorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("retry after altered result replay=%v err=%v, want nonrepeatable outcome_unknown", replayed, err)
	}
	if secondOwner.memoryWrites != 0 {
		t.Fatalf("uncertain reconciliation re-executed owner mutation %d times, want zero", secondOwner.memoryWrites)
	}
}

func TestArchiveUserMemoryRecoveryRejectsMissingLegacyIntegrity(t *testing.T) {
	f := &archiveGroupFixture{root: t.TempDir()}
	legacyDB, err := sql.Open("sqlite", filepath.Join(f.root, "archive.db"))
	if err != nil {
		t.Fatalf("create legacy archive SQLite: %v", err)
	}
	if _, err := legacyDB.Exec(`CREATE TABLE conversation_archive_storagehost_user_memory_receipt (
		op_id TEXT PRIMARY KEY NOT NULL,
		payload_sha256 TEXT NOT NULL,
		request_id TEXT NOT NULL,
		user_id TEXT NOT NULL,
		memory_id TEXT NOT NULL,
		idempotent_replay INTEGER NOT NULL CHECK (idempotent_replay IN (0, 1)),
		created_at TIMESTAMP NOT NULL
	)`); err != nil {
		_ = legacyDB.Close()
		t.Fatalf("create legacy storage-host receipt schema: %v", err)
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatalf("close legacy archive SQLite: %v", err)
	}

	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	event := archiveGroupUserMemoryEvent("memory-missing-legacy-integrity", "ren", now)
	productReceipt := l1sqlite.OwnerArchiveRequest{
		RequestID: "product-request-missing-legacy-integrity", UserID: "ren", ActorID: "mio", PayloadHash: archiveGroupPayloadHash,
		MemoryID: event.ID, CreatedAt: now,
	}
	firstOwner := &archiveRecoveryOwner{}
	f.openWithOwnerFactory(t, false, func(store *archivesqlite.ArchiveSQLiteStore) ArchiveGroupOwner {
		firstOwner.ArchiveGroupRecoveryOwner = store
		return firstOwner
	})
	t.Cleanup(f.close)

	const opID = "archive-missing-legacy-integrity"
	f.rpc.opID = opID
	f.host.crashAfterCommitFor = opID
	if replayed, err := f.client.ArchiveUserMemoryWithReceipt(context.Background(), event, productReceipt); archiveGroupErrorCode(err) != ErrorCodeOutcomeUnknown || replayed {
		t.Fatalf("post-commit response loss replay=%v err=%v, want false and outcome_unknown", replayed, err)
	}
	f.close()
	db, err := sql.Open("sqlite", filepath.Join(f.root, "archive.db"))
	if err != nil {
		t.Fatalf("open migrated archive SQLite to simulate legacy receipt: %v", err)
	}
	result, err := db.Exec(`UPDATE conversation_archive_storagehost_user_memory_receipt
SET integrity_sha256 = NULL WHERE op_id = ?`, opID)
	if err != nil {
		_ = db.Close()
		t.Fatalf("remove legacy receipt integrity: %v", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		_ = db.Close()
		t.Fatalf("legacy receipt rows=%d err=%v, want one", changed, err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close migrated archive SQLite: %v", err)
	}

	secondOwner := &archiveRecoveryOwner{}
	f.openWithOwnerFactory(t, true, func(store *archivesqlite.ArchiveSQLiteStore) ArchiveGroupOwner {
		secondOwner.ArchiveGroupRecoveryOwner = store
		return secondOwner
	})
	f.rpc.opID = opID
	if replayed, err := f.client.ArchiveUserMemoryWithReceipt(context.Background(), event, productReceipt); archiveGroupErrorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("retry after missing legacy integrity replay=%v err=%v, want outcome_unknown", replayed, err)
	}
	if secondOwner.memoryWrites != 0 {
		t.Fatalf("missing integrity reconciliation re-executed owner mutation %d times, want zero", secondOwner.memoryWrites)
	}
}

func TestArchiveProtocolOperationIDRejectsDifferentPayload(t *testing.T) {
	f := &archiveGroupFixture{root: t.TempDir()}
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	event := archiveGroupUserMemoryEvent("memory-opid-conflict", "ren", now)
	receipt := l1sqlite.OwnerArchiveRequest{RequestID: "product-request-opid-conflict", UserID: "ren", ActorID: "mio", PayloadHash: archiveGroupPayloadHash, MemoryID: event.ID, CreatedAt: now}
	firstOwner := &archiveRecoveryOwner{blockReconcile: true}
	f.openWithOwnerFactory(t, false, func(store *archivesqlite.ArchiveSQLiteStore) ArchiveGroupOwner {
		firstOwner.ArchiveGroupRecoveryOwner = store
		return firstOwner
	})
	t.Cleanup(f.close)
	const opID = "archive-opid-payload-conflict"
	f.rpc.opID = opID
	f.host.crashAfterCommitFor = opID
	if _, err := f.client.ArchiveUserMemoryWithReceipt(context.Background(), event, receipt); archiveGroupErrorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("initial uncertain user-memory call error=%v, want outcome_unknown", err)
	}
	changedEvent := event
	changedEvent.Message += " changed"
	f.rpc.opID = opID
	if _, err := f.client.ArchiveUserMemoryWithReceipt(context.Background(), changedEvent, receipt); archiveGroupErrorCode(err) != ErrorCodeDuplicateConflict {
		t.Fatalf("same op_id with a different payload error=%v, want duplicate_conflict", err)
	}
	if firstOwner.memoryWrites != 1 {
		t.Fatalf("owner mutation calls=%d after conflicting replay, want one", firstOwner.memoryWrites)
	}
}

func TestArchiveMismatchedNaturalReceiptStaysOutcomeUnknown(t *testing.T) {
	summary, summaryReceipt := archiveGroupThreadSummary()
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	event := archiveGroupUserMemoryEvent("memory-mismatch", "ren", now)
	productReceipt := l1sqlite.OwnerArchiveRequest{RequestID: "product-request-mismatch", UserID: "ren", ActorID: "ren", PayloadHash: archiveGroupPayloadHash, MemoryID: event.ID, CreatedAt: now}
	for _, test := range []struct {
		name   string
		call   func(*ArchiveStoreClient) (any, error)
		tamper func(*testing.T, *sql.DB)
	}{
		{
			name: "thread_summary",
			call: func(client *ArchiveStoreClient) (any, error) {
				return nil, client.SaveThreadSummaryWithReceipt(context.Background(), summary, summaryReceipt)
			},
			tamper: func(t *testing.T, db *sql.DB) {
				if _, err := db.Exec(`UPDATE session_thread SET summary = ? WHERE thread_id = ?`, "tampered archive content", summary.ThreadID); err != nil {
					t.Fatalf("tamper committed thread summary: %v", err)
				}
			},
		},
		{
			name: "user_memory",
			call: func(client *ArchiveStoreClient) (any, error) {
				return client.ArchiveUserMemoryWithReceipt(context.Background(), event, productReceipt)
			},
			tamper: func(t *testing.T, db *sql.DB) {
				if _, err := db.Exec(`UPDATE conversation_archive_request_receipt SET payload_hash = ? WHERE request_id = ?`, strings.Repeat("b", 64), productReceipt.RequestID); err != nil {
					t.Fatalf("tamper committed user-memory receipt: %v", err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := &archiveGroupFixture{root: t.TempDir()}
			firstOwner := &archiveRecoveryOwner{blockReconcile: true}
			f.openWithOwnerFactory(t, false, func(store *archivesqlite.ArchiveSQLiteStore) ArchiveGroupOwner {
				firstOwner.ArchiveGroupRecoveryOwner = store
				return firstOwner
			})
			t.Cleanup(f.close)

			const opID = "archive-mismatch-recovery"
			f.rpc.opID = opID
			f.host.crashAfterCommitFor = opID
			if _, err := test.call(f.client); archiveGroupErrorCode(err) != ErrorCodeOutcomeUnknown {
				t.Fatalf("post-commit response loss error=%v, want outcome_unknown", err)
			}
			f.close()
			db, err := sql.Open("sqlite", filepath.Join(f.root, "archive.db"))
			if err != nil {
				t.Fatalf("open archive SQLite for mismatch setup: %v", err)
			}
			test.tamper(t, db)
			if err := db.Close(); err != nil {
				t.Fatalf("close tampered archive SQLite: %v", err)
			}
			secondOwner := &archiveRecoveryOwner{}
			f.openWithOwnerFactory(t, true, func(store *archivesqlite.ArchiveSQLiteStore) ArchiveGroupOwner {
				secondOwner.ArchiveGroupRecoveryOwner = store
				return secondOwner
			})
			f.rpc.opID = opID
			if _, err := test.call(f.client); archiveGroupErrorCode(err) != ErrorCodeOutcomeUnknown {
				t.Fatalf("retry after mismatched natural receipt error=%v, want outcome_unknown", err)
			}
			if secondOwner.summaryWrites != 0 || secondOwner.memoryWrites != 0 {
				t.Fatalf("mismatch recovery re-executed owner mutation: summary=%d memory=%d", secondOwner.summaryWrites, secondOwner.memoryWrites)
			}
		})
	}
}

func TestArchiveGroupBoundsSchemaAndRequiresCommonHandlerAuthentication(t *testing.T) {
	f := newArchiveGroupFixture(t)
	ctx := context.Background()
	if _, err := f.client.GetSessionHistory(ctx, string(modulecore.NewSessionID()), archiveMaxResults+1); archiveGroupErrorCode(err) != ErrorCodeSchemaRejected {
		t.Fatalf("client out-of-range history limit error=%v, want schema_rejected", err)
	}
	unknownPayload, err := json.Marshal(map[string]any{"session_id": string(modulecore.NewSessionID()), "limit": 5, "unexpected": true})
	if err != nil {
		t.Fatalf("marshal unknown-field payload: %v", err)
	}
	status, response := postArchiveRPC(t, f.rpc, archiveGroupTestToken, Request{
		Contract: ContractVersion, OpID: newOpID(), Group: GroupArchive, Op: archiveOpGetSessionHistory,
		Generation: f.rpc.Generation(), Payload: unknownPayload,
	})
	if status != http.StatusBadRequest || response.Error == nil || response.Error.Code != ErrorCodeSchemaRejected {
		t.Fatalf("unknown-field response status=%d response=%+v, want schema_rejected", status, response)
	}

	tooLargeSummary, tooLargeReceipt := archiveGroupThreadSummary()
	tooLargeSummary.Summary = strings.Repeat("x", (1<<20)+1)
	oversizedPayload, err := json.Marshal(archiveSaveThreadSummaryPayload{Summary: tooLargeSummary, Receipt: tooLargeReceipt})
	if err != nil {
		t.Fatalf("marshal oversized summary payload: %v", err)
	}
	status, response = postArchiveRPC(t, f.rpc, archiveGroupTestToken, Request{
		Contract: ContractVersion, OpID: newOpID(), Group: GroupArchive, Op: archiveOpSaveThreadSummary,
		Generation: f.rpc.Generation(), Payload: oversizedPayload,
	})
	if status != http.StatusBadRequest || response.Error == nil || response.Error.Code != ErrorCodeSchemaRejected {
		t.Fatalf("oversized summary response status=%d response=%+v, want schema_rejected", status, response)
	}
	if _, err := f.client.GetThreadSummary(ctx, tooLargeSummary.ThreadID); !errors.Is(err, domconv.ErrThreadNotFound) {
		t.Fatalf("oversized write persisted unexpectedly: %v", err)
	}
	tooLargeReadSummary, tooLargeReadReceipt := archiveGroupThreadSummary()
	if err := f.store.SaveThreadSummaryWithReceipt(ctx, tooLargeReadSummary, tooLargeReadReceipt); err != nil {
		t.Fatalf("seed summary for result bound: %v", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(f.root, "archive.db"))
	if err != nil {
		t.Fatalf("open archive SQLite for oversized result: %v", err)
	}
	if _, err := db.Exec(`UPDATE session_thread SET summary = ? WHERE thread_id = ?`, strings.Repeat("x", (1<<20)+1), tooLargeReadSummary.ThreadID); err != nil {
		_ = db.Close()
		t.Fatalf("seed oversized summary result: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close archive SQLite after oversized result seed: %v", err)
	}
	if _, err := f.client.SearchByDomain(ctx, tooLargeReadSummary.Domain, 10); archiveGroupErrorCode(err) != ErrorCodeStoreUnavailable {
		t.Fatalf("oversized owner result error=%v, want store_unavailable without data", err)
	}

	status, response = postArchiveRPC(t, f.rpc, "wrong-token", Request{
		Contract: ContractVersion, OpID: newOpID(), Group: GroupArchive, Op: archiveOpGetSessionHistory,
		Generation: f.rpc.Generation(), Payload: json.RawMessage(`{"session_id":"` + string(modulecore.NewSessionID()) + `","limit":1}`),
	})
	if status != http.StatusUnauthorized || response.Error == nil || response.Error.Code != ErrorCodeUnauthorized {
		t.Fatalf("unauthenticated archive response status=%d response=%+v, want unauthorized", status, response)
	}
}

func postArchiveRPC(t *testing.T, client *Client, token string, request Request) (int, Response) {
	t.Helper()
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal raw storage-host request: %v", err)
	}
	httpRequest, err := http.NewRequest(http.MethodPost, client.endpoint, bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("build raw storage-host request: %v", err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+token)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpResponse, err := client.httpClient.Do(httpRequest)
	if err != nil {
		t.Fatalf("send raw storage-host request: %v", err)
	}
	defer httpResponse.Body.Close()
	var response Response
	if err := json.NewDecoder(httpResponse.Body).Decode(&response); err != nil {
		t.Fatalf("decode raw storage-host response: %v", err)
	}
	return httpResponse.StatusCode, response
}

func archiveGroupErrorCode(err error) string {
	var storageErr *Error
	if errors.As(err, &storageErr) {
		return storageErr.Code
	}
	return ""
}

const archiveGroupPayloadHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func archiveGroupThreadSummary() (*domconv.ThreadSummary, *domconv.ThreadSummaryReceipt) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	receipt := &domconv.ThreadSummaryReceipt{
		SchemaVersion: domconv.ThreadSummaryReceiptSchemaVersion, GenerationMode: domconv.ThreadSummaryGenerationLLM,
		Provider: "test", EvidenceSHA256: archiveGroupPayloadHash, SourceTurnCount: 2, Roles: []string{"user", "mio"}, CreatedAt: now,
	}
	summary := &domconv.ThreadSummary{
		ThreadID: modulecore.NewThreadID(), ThreadSeq: 1, ThreadKind: domconv.ThreadKindUserConversation,
		SessionID: string(modulecore.NewSessionID()), Domain: "general", Summary: "A concise conversation summary",
		Keywords: []string{"conversation", "summary", "archive"}, Roles: []string{"user", "mio"}, Receipt: receipt,
		StartTime: now.Add(-time.Minute), EndTime: now,
	}
	return summary, receipt
}

func archiveGroupUserMemoryEvent(id, userID string, at time.Time) l1sqlite.L1MemoryEvent {
	return l1sqlite.L1MemoryEvent{
		ID: id, Namespace: "user:" + userID, Speaker: domconv.SpeakerUser, Message: "Keep this exact user memory",
		Meta: map[string]interface{}{"origin": "user_memory"}, MemoryState: l1sqlite.MemoryStateConfirmed,
		Layer: "L1", Source: "user_memory", CreatedAt: at, UpdatedAt: at,
	}
}
