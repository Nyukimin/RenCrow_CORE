package storagehost

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	glossaryapp "github.com/Nyukimin/RenCrow_CORE/internal/application/glossary"
	"github.com/Nyukimin/RenCrow_CORE/internal/glossary/domain/entity"
	glossarypersistence "github.com/Nyukimin/RenCrow_CORE/internal/glossary/infrastructure/persistence"
)

const glossaryGroupTestToken = "glossary-group-test-token"

func TestGlossaryGroupRegistersClosedOperations(t *testing.T) {
	fixture := newGlossaryGroupFixture(t, t.TempDir())
	client := fixture.client
	contract, err := client.Contract(context.Background())
	if err != nil {
		t.Fatalf("Contract: %v", err)
	}
	want := map[string]bool{
		"save": true, "find_by_term": false, "find_recent": false, "viewer_page": false, "find_by_category": false,
		"delete": true, "save_candidate": true, "find_candidate_by_id": false, "lookup": false,
	}
	got := make(map[string]bool)
	for _, operation := range contract.Operations {
		if operation.Group == GroupGlossary {
			got[operation.Op] = operation.Mutating
		}
	}
	if len(got) != len(want) {
		t.Fatalf("glossary operations=%v, want exactly %v", got, want)
	}
	for operation, mutating := range want {
		if got[operation] != mutating {
			t.Fatalf("glossary operation %q mutating=%v, want %v", operation, got[operation], mutating)
		}
	}
}

func TestGlossaryViewerPagePreservesCanonicalCountAndOrderedRows(t *testing.T) {
	fixture := newGlossaryGroupFixture(t, t.TempDir())
	ctx := context.Background()
	for index := 0; index < 205; index++ {
		item := glossaryGroupItem(fmt.Sprintf("viewer-page-%03d", index), fmt.Sprintf("term-%03d", index), "viewer page explanation", "viewer_feed")
		if err := fixture.owner.Save(ctx, item); err != nil {
			t.Fatalf("seed Glossary owner row %d: %v", index, err)
		}
	}
	store := NewGlossaryStoreClient(fixture.client)
	total, items, err := store.FindViewerPage(ctx, 200)
	if err != nil {
		t.Fatalf("FindViewerPage: %v", err)
	}
	if total != 205 || len(items) != 200 || items[0].Term != "term-204" || items[199].Term != "term-005" || items[0].Explanation != "viewer page explanation" {
		t.Fatalf("FindViewerPage total=%d items=%d first=%+v last=%+v, want canonical total and newest 200 rows", total, len(items), items[0], items[len(items)-1])
	}
	if _, _, err := store.FindViewerPage(ctx, 0); err == nil {
		t.Fatal("FindViewerPage accepted zero limit")
	} else {
		expectGlossaryError(t, err, ErrorCodeSchemaRejected)
	}
	if _, _, err := store.FindViewerPage(ctx, 201); err == nil {
		t.Fatal("FindViewerPage accepted limit above 200")
	} else {
		expectGlossaryError(t, err, ErrorCodeSchemaRejected)
	}
}

func TestGlossaryClientPreservesRepositoryCandidateAndIndexedLookupDTOs(t *testing.T) {
	fixture := newGlossaryGroupFixture(t, t.TempDir())
	ctx := context.Background()
	store := NewGlossaryStoreClient(fixture.client)
	item := glossaryGroupItem("glossary-private-dto", "private term", "private explanation", "viewer_feed")
	if err := store.Save(ctx, item); err != nil {
		t.Fatalf("Save: %v", err)
	}
	byTerm, err := store.FindByTerm(ctx, item.Term)
	if err != nil || byTerm == nil || *byTerm != *item {
		t.Fatalf("FindByTerm()=%+v err=%v, want exact entity DTO", byTerm, err)
	}
	recent, err := store.FindRecent(ctx, 10)
	if err != nil || len(recent) != 1 || *recent[0] != *item {
		t.Fatalf("FindRecent()=%+v err=%v, want exact entity slice", recent, err)
	}
	category, err := store.FindByCategory(ctx, item.Category, 10)
	if err != nil || len(category) != 1 || *category[0] != *item {
		t.Fatalf("FindByCategory()=%+v err=%v, want exact entity slice", category, err)
	}
	candidate := glossaryGroupCandidate("candidate-private-dto", item.Term)
	if err := store.SaveCandidate(ctx, candidate); err != nil {
		t.Fatalf("SaveCandidate: %v", err)
	}
	gotCandidate, found, err := store.FindCandidateByID(ctx, candidate.ID)
	if err != nil || !found || gotCandidate != candidate {
		t.Fatalf("FindCandidateByID()=%+v found=%v err=%v, want exact candidate DTO", gotCandidate, found, err)
	}
	lookupAny, err := store.Lookup(ctx, "define_term", item.Term, "", 10)
	if err != nil {
		t.Fatalf("Lookup define_term: %v", err)
	}
	lookup, ok := lookupAny.(glossaryapp.LookupResult)
	if !ok || lookup.Operation != "define_term" || len(lookup.Items) != 1 || lookup.Items[0].ID != item.ID || lookup.Items[0].Explanation != item.Explanation {
		t.Fatalf("Lookup() concrete type/value=%T %+v, want glossaryapp.LookupResult preserving indexed DTO", lookupAny, lookupAny)
	}
	lookupAny, err = store.Lookup(ctx, "list_category", "", item.Category, 10)
	if err != nil {
		t.Fatalf("Lookup list_category: %v", err)
	}
	lookup, ok = lookupAny.(glossaryapp.LookupResult)
	if !ok || lookup.Operation != "list_category" || len(lookup.Items) != 1 || lookup.Items[0].ID != item.ID {
		t.Fatalf("category Lookup()=%T %+v, want exact typed indexed result", lookupAny, lookupAny)
	}
	if err := store.Delete(ctx, item.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.FindByTerm(ctx, item.Term); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("FindByTerm after Delete error=%v, want sql.ErrNoRows", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, _, err := store.FindCandidateByID(ctx, candidate.ID); err != nil {
		t.Fatalf("Close must not close shared RPC client: %v", err)
	}
}

func TestGlossaryMutationsReconcileAfterHandlerAndOwnerReopen(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*glossarypersistence.SQLiteGlossaryRepository) (*entity.GlossaryItem, entity.GlossaryCandidate)
		call    func(*GlossaryStoreClient, context.Context, *entity.GlossaryItem, entity.GlossaryCandidate) error
		verify  func(*testing.T, *GlossaryStoreClient, *entity.GlossaryItem, entity.GlossaryCandidate)
	}{
		{name: "save", prepare: func(*glossarypersistence.SQLiteGlossaryRepository) (*entity.GlossaryItem, entity.GlossaryCandidate) {
			return glossaryGroupItem("recover-save", "recover save", "save explanation", "feed"), entity.GlossaryCandidate{}
		}, call: func(c *GlossaryStoreClient, ctx context.Context, item *entity.GlossaryItem, _ entity.GlossaryCandidate) error {
			return c.Save(ctx, item)
		}, verify: func(t *testing.T, c *GlossaryStoreClient, item *entity.GlossaryItem, _ entity.GlossaryCandidate) {
			t.Helper()
			got, err := c.FindByTerm(context.Background(), item.Term)
			if err != nil || got == nil || *got != *item {
				t.Fatalf("recovered Save row=%+v err=%v", got, err)
			}
		}},
		{name: "delete", prepare: func(owner *glossarypersistence.SQLiteGlossaryRepository) (*entity.GlossaryItem, entity.GlossaryCandidate) {
			item := glossaryGroupItem("recover-delete", "recover delete", "delete explanation", "feed")
			if err := owner.Save(context.Background(), item); err != nil {
				panic(err)
			}
			return item, entity.GlossaryCandidate{}
		}, call: func(c *GlossaryStoreClient, ctx context.Context, item *entity.GlossaryItem, _ entity.GlossaryCandidate) error {
			return c.Delete(ctx, item.ID)
		}, verify: func(t *testing.T, c *GlossaryStoreClient, item *entity.GlossaryItem, _ entity.GlossaryCandidate) {
			t.Helper()
			if _, err := c.FindByTerm(context.Background(), item.Term); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("recovered Delete FindByTerm error=%v, want absent", err)
			}
		}},
		{name: "save_candidate", prepare: func(*glossarypersistence.SQLiteGlossaryRepository) (*entity.GlossaryItem, entity.GlossaryCandidate) {
			return nil, glossaryGroupCandidate("recover-candidate", "candidate survives")
		}, call: func(c *GlossaryStoreClient, ctx context.Context, _ *entity.GlossaryItem, candidate entity.GlossaryCandidate) error {
			return c.SaveCandidate(ctx, candidate)
		}, verify: func(t *testing.T, c *GlossaryStoreClient, _ *entity.GlossaryItem, candidate entity.GlossaryCandidate) {
			t.Helper()
			got, found, err := c.FindCandidateByID(context.Background(), candidate.ID)
			if err != nil || !found || got != candidate {
				t.Fatalf("recovered candidate=%+v found=%v err=%v", got, found, err)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			fixture := newGlossaryGroupFixture(t, root)
			item, candidate := tc.prepare(fixture.owner)
			fixture.client.opID = "glossary-recovery-" + tc.name
			fixture.handler.crashAfterCommitFor = fixture.client.opID
			err := tc.call(NewGlossaryStoreClient(fixture.client), context.Background(), item, candidate)
			expectGlossaryError(t, err, ErrorCodeOutcomeUnknown)
			fixture.close()
			reopened := newGlossaryGroupFixture(t, root)
			reopened.client.opID = fixture.clientOpID("glossary-recovery-" + tc.name)
			if err := tc.call(NewGlossaryStoreClient(reopened.client), context.Background(), item, candidate); err != nil {
				t.Fatalf("retry after owner/handler reopen: %v", err)
			}
			tc.verify(t, NewGlossaryStoreClient(reopened.client), item, candidate)
		})
	}
}

func TestGlossarySameOpIDPayloadConflictAndStrictBounds(t *testing.T) {
	fixture := newGlossaryGroupFixture(t, t.TempDir())
	client := NewGlossaryStoreClient(fixture.client)
	ctx := context.Background()
	fixture.client.opID = "glossary-payload-binding"
	item := glossaryGroupItem("payload-bound", "payload bound", "original", "feed")
	if err := client.Save(ctx, item); err != nil {
		t.Fatalf("Save: %v", err)
	}
	changed := *item
	changed.Explanation = "changed"
	expectGlossaryError(t, client.Save(ctx, &changed), ErrorCodeDuplicateConflict)
	fixture.client.opID = ""
	invalid := glossaryGroupItem("", "invalid", "blank id", "feed")
	expectGlossaryError(t, client.Save(ctx, invalid), ErrorCodeSchemaRejected)
	oversized := glossaryGroupItem("oversized", "oversized", strings.Repeat("x", 5000), "feed")
	expectGlossaryError(t, client.Save(ctx, oversized), ErrorCodeSchemaRejected)
	badCandidate := glossaryGroupCandidate("bad-candidate", "term")
	badCandidate.SourceURL = "file:///not-an-https-source"
	expectGlossaryError(t, client.SaveCandidate(ctx, badCandidate), ErrorCodeSchemaRejected)
	var result glossaryapp.LookupResult
	err := fixture.client.Call(ctx, GroupGlossary, "lookup", map[string]any{"operation": "define_term", "term": "x", "category": "", "limit": 10, "unexpected": true}, &result)
	expectGlossaryError(t, err, ErrorCodeSchemaRejected)
	if _, err := client.FindByTerm(ctx, "missing term"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing FindByTerm error=%v, want sql.ErrNoRows", err)
	}
}

func TestGlossaryMismatchedOrUnavailableOwnerReceiptStaysUnknown(t *testing.T) {
	for _, lookupMode := range []string{"mismatch", "malformed", "owner_error"} {
		t.Run(lookupMode, func(t *testing.T) {
			root := t.TempDir()
			base, err := glossarypersistence.NewSQLiteGlossaryRepository(filepath.Join(root, "glossary.db"))
			if err != nil {
				t.Fatalf("NewSQLiteGlossaryRepository: %v", err)
			}
			owner := &glossaryFaultOwner{GlossaryGroupOwner: base, saveErrorAfterCommit: errors.New("ambiguous commit response"), lookupMode: lookupMode}
			handler, err := NewHandler(HandlerConfig{Token: glossaryGroupTestToken, JournalDir: filepath.Join(root, "journal")})
			if err != nil {
				t.Fatalf("NewHandler: %v", err)
			}
			if err := RegisterGlossaryGroup(handler, owner); err != nil {
				t.Fatalf("RegisterGlossaryGroup: %v", err)
			}
			server := httptest.NewServer(handler)
			t.Cleanup(func() { server.Close(); _ = handler.Close(); _ = base.Close() })
			client, err := NewClient(ClientConfig{Endpoint: server.URL, Token: glossaryGroupTestToken, HTTPClient: server.Client()})
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			if err := client.Handshake(context.Background()); err != nil {
				t.Fatalf("Handshake: %v", err)
			}
			client.opID = "glossary-owner-receipt-" + lookupMode
			item := glossaryGroupItem("receipt-check-"+lookupMode, "receipt check "+lookupMode, "receipt-check explanation", "feed")
			store := NewGlossaryStoreClient(client)
			expectGlossaryError(t, store.Save(context.Background(), item), ErrorCodeOutcomeUnknown)
			expectGlossaryError(t, store.Save(context.Background(), item), ErrorCodeOutcomeUnknown)
			stored, err := base.FindByTerm(context.Background(), item.Term)
			if err != nil || stored == nil || stored.ID != item.ID {
				t.Fatalf("owner mutation was not committed as expected for reconciliation test: item=%+v err=%v", stored, err)
			}
		})
	}
}

type glossaryFaultOwner struct {
	GlossaryGroupOwner
	saveErrorAfterCommit error
	lookupMode           string
}

func (o *glossaryFaultOwner) SaveForStorageHostOperation(ctx context.Context, identity glossarypersistence.GlossaryOperationIdentity, item *entity.GlossaryItem) error {
	if err := o.GlossaryGroupOwner.SaveForStorageHostOperation(ctx, identity, item); err != nil {
		return err
	}
	return o.saveErrorAfterCommit
}

func (o *glossaryFaultOwner) LookupStorageHostOperationReceipt(ctx context.Context, identity glossarypersistence.GlossaryOperationIdentity) (glossarypersistence.GlossaryOperationReceipt, bool, error) {
	receipt, found, err := o.GlossaryGroupOwner.LookupStorageHostOperationReceipt(ctx, identity)
	if err != nil {
		return receipt, found, err
	}
	switch o.lookupMode {
	case "mismatch":
		if found {
			receipt.Identity.PayloadHash = strings.Repeat("f", 64)
		}
	case "malformed":
		if found {
			receipt.ResultJSON = json.RawMessage("not-json")
		}
	case "owner_error":
		return glossarypersistence.GlossaryOperationReceipt{}, false, errors.New("injected receipt lookup failure")
	}
	return receipt, found, nil
}

type glossaryGroupFixture struct {
	root    string
	owner   *glossarypersistence.SQLiteGlossaryRepository
	handler *Handler
	server  *httptest.Server
	client  *Client
}

func newGlossaryGroupFixture(t *testing.T, root string) *glossaryGroupFixture {
	t.Helper()
	owner, err := glossarypersistence.NewSQLiteGlossaryRepository(filepath.Join(root, "glossary.db"))
	if err != nil {
		t.Fatalf("NewSQLiteGlossaryRepository: %v", err)
	}
	handler, err := NewHandler(HandlerConfig{Token: glossaryGroupTestToken, JournalDir: filepath.Join(root, "journal")})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	if err := RegisterGlossaryGroup(handler, owner); err != nil {
		_ = handler.Close()
		_ = owner.Close()
		t.Fatalf("RegisterGlossaryGroup: %v", err)
	}
	server := httptest.NewServer(handler)
	client, err := NewClient(ClientConfig{Endpoint: server.URL, Token: glossaryGroupTestToken, HTTPClient: server.Client()})
	if err != nil {
		server.Close()
		_ = handler.Close()
		_ = owner.Close()
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Handshake(context.Background()); err != nil {
		server.Close()
		_ = handler.Close()
		_ = owner.Close()
		t.Fatalf("Handshake: %v", err)
	}
	fixture := &glossaryGroupFixture{root: root, owner: owner, handler: handler, server: server, client: client}
	t.Cleanup(fixture.close)
	return fixture
}

func (f *glossaryGroupFixture) close() {
	if f == nil {
		return
	}
	if f.server != nil {
		f.server.Close()
		f.server = nil
	}
	if f.handler != nil {
		_ = f.handler.Close()
		f.handler = nil
	}
	if f.owner != nil {
		_ = f.owner.Close()
		f.owner = nil
	}
}

func (f *glossaryGroupFixture) clientOpID(fallback string) string { return fallback }

func glossaryGroupItem(id, term, explanation, category string) *entity.GlossaryItem {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	return &entity.GlossaryItem{ID: id, Term: term, Explanation: explanation, Source: "https://example.test/source", Category: category, CreatedAt: now, UpdatedAt: now}
}

func glossaryGroupCandidate(id, term string) entity.GlossaryCandidate {
	return entity.GlossaryCandidate{ID: id, Term: term, Explanation: "candidate explanation", SourceURL: "https://example.test/source", Category: "new_word", ProposedBy: "agent:mio", State: entity.GlossaryCandidateState, CreatedAt: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)}
}

func expectGlossaryError(t *testing.T, err error, code string) {
	t.Helper()
	var got *Error
	if !errors.As(err, &got) || got.Code != code {
		t.Fatalf("error=%v, want storagehost code %q", err, code)
	}
}
