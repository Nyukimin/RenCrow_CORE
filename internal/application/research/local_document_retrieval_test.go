package research

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
)

func TestLocalDocumentRetrieverReturnsScopedFullTextWithOwnerTimeBasis(t *testing.T) {
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "research.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	body := "The exact complete text for local collection."
	privateBody := "Alice's private exact source text."
	insertLocalRecallFixture(t, store, "local-exact-id", "kb:research", localRecallEvent(body), "public", "", body, now, now, true)
	insertLocalRecallFixture(t, store, "alice-exact-id", "user:alice", localRecallEvent(privateBody), "user", "alice", privateBody, now, time.Time{}, true)
	policy := func(_ context.Context, candidate c.Candidate) (bool, bool) {
		return candidate.URL == "https://example.invalid/", candidate.URL == "https://example.invalid/"
	}
	reader := NewLocalDocumentRetriever(store, policy)
	got, err := reader.ReadDocuments(context.Background(), c.Envelope{DataScope: "public"}, engine.LocalDocumentRequest{
		Query: "complete text", Limit: 4,
		TimeWindow: &c.TimeWindow{Since: now, Until: now.Add(time.Hour)},
	})
	if err != nil || got.Code != "" || len(got.Items) != 1 || len(got.Missing) != 0 {
		t.Fatalf("read local exact document: result=%+v err=%v", got, err)
	}
	doc := got.Items[0].Document
	receipt := got.Items[0].Receipt
	if doc.ID != "local-exact-id" || doc.Text != body || doc.ExtractedHash != localRecallHash(body) || doc.OriginalHash != "" || doc.TimeBasis != "published_at" || !doc.Complete || !doc.RetentionAllowed || doc.Primary {
		t.Fatalf("document lost exact owner content or provenance: %+v", doc)
	}
	if receipt.Owner != "core.l1_staging" || receipt.OwnerRoute != KnowledgeStagingTarget || receipt.CanonicalID != doc.ID || receipt.AuditRef != localRecallEvent(body) || receipt.Digest != doc.ExtractedHash || receipt.Scope != "public" || receipt.State != "committed" || receipt.CommittedAt.IsZero() || !receipt.VerifiedAt.IsZero() {
		t.Fatalf("existing exact owner receipt was not represented for verification: %+v", receipt)
	}
	outside, err := reader.ReadDocuments(context.Background(), c.Envelope{DataScope: "public"}, engine.LocalDocumentRequest{Query: "Alice's private", Limit: 4})
	if err != nil || len(outside.Items) != 0 || len(outside.Missing) != 0 {
		t.Fatalf("private source escaped into public scope = %+v err=%v", outside, err)
	}
	alice, err := reader.ReadDocuments(context.Background(), c.Envelope{DataScope: "user", OwnerID: "alice"}, engine.LocalDocumentRequest{Query: "Alice's private", Limit: 4})
	if err != nil || len(alice.Items) != 1 || alice.Items[0].Document.ID != "alice-exact-id" || len(alice.Missing) != 0 {
		t.Fatalf("exact owner scope read = %+v err=%v", alice, err)
	}
	upperExclusive, err := reader.ReadDocuments(context.Background(), c.Envelope{DataScope: "public"}, engine.LocalDocumentRequest{
		Query: "complete text", Limit: 4, TimeWindow: &c.TimeWindow{Since: now.Add(-time.Hour), Until: now},
	})
	if err != nil || len(upperExclusive.Items) != 0 {
		t.Fatalf("until boundary was not exclusive: %+v err=%v", upperExclusive, err)
	}
}

func TestLocalDocumentRetrieverUsesScopedExactDocumentRefs(t *testing.T) {
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "research.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	body := "Literal retained body used with an exact document ref."
	privateBody := "Private foreign document body."
	insertLocalRecallFixture(t, store, "canonical-local-doc", "kb:research", localRecallEvent(body), "public", "", body, now, time.Time{}, true)
	insertLocalRecallFixture(t, store, "alice-private-doc", "user:alice", localRecallEvent(privateBody), "user", "alice", privateBody, now, time.Time{}, true)
	reader := NewLocalDocumentRetriever(store, func(context.Context, c.Candidate) (bool, bool) { return true, true })
	got, err := reader.ReadDocuments(context.Background(), c.Envelope{DataScope: "public"}, engine.LocalDocumentRequest{
		Query: body, DocumentRefs: []string{"canonical-local-doc"}, Limit: 4,
	})
	if err != nil || got.Code != "" || len(got.Items) != 1 || got.Items[0].Document.ID != "canonical-local-doc" || got.Items[0].Document.Text != body || len(got.Missing) != 0 {
		t.Fatalf("exact scoped lookup = %+v err=%v", got, err)
	}
	foreign, err := reader.ReadDocuments(context.Background(), c.Envelope{DataScope: "user", OwnerID: "alice"}, engine.LocalDocumentRequest{
		Query: body, DocumentRefs: []string{"canonical-local-doc"}, Limit: 4,
	})
	if err != nil || len(foreign.Items) != 0 {
		t.Fatalf("foreign-scope ref leaked: %+v err=%v", foreign, err)
	}
	missing, err := reader.ReadDocuments(context.Background(), c.Envelope{DataScope: "public"}, engine.LocalDocumentRequest{
		Query: body, DocumentRefs: []string{"unknown-local-doc"}, Limit: 4,
	})
	if err != nil || len(missing.Items) != 0 {
		t.Fatalf("unknown ref returned a document: %+v err=%v", missing, err)
	}
	for _, refs := range [][]string{{" "}, {"same", "same"}, {strings.Repeat("x", 513)}} {
		if _, err := reader.ReadDocuments(context.Background(), c.Envelope{DataScope: "public"}, engine.LocalDocumentRequest{Query: body, DocumentRefs: refs, Limit: 4}); err == nil {
			t.Fatalf("invalid refs accepted: %#v", refs)
		}
	}
	tooMany := make([]string, 21)
	for index := range tooMany {
		tooMany[index] = "doc"
	}
	if _, err := reader.ReadDocuments(context.Background(), c.Envelope{DataScope: "public"}, engine.LocalDocumentRequest{Query: body, DocumentRefs: tooMany, Limit: 4}); err == nil {
		t.Fatal("too many refs accepted")
	}
}

type failNthLocalDocumentRead struct {
	ResearchDocumentStore
	reads int
	fail  int
}

func (s *failNthLocalDocumentRead) FindStagingItemByNamespaceEventID(ctx context.Context, namespace, eventID string) (l1sqlite.L1StagingItem, bool, error) {
	s.reads++
	if s.reads == s.fail {
		return l1sqlite.L1StagingItem{}, false, context.DeadlineExceeded
	}
	return s.ResearchDocumentStore.FindStagingItemByNamespaceEventID(ctx, namespace, eventID)
}

func TestLocalDocumentRetrieverPreservesVerifiedItemsWhenLaterExactReadFails(t *testing.T) {
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "research.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	insertLocalRecallFixture(t, store, "read-first", "kb:research", "event-first", "public", "", "Common literal retained phrase first.", now, time.Time{}, true)
	insertLocalRecallFixture(t, store, "read-second", "kb:research", "event-second", "public", "", "Common literal retained phrase second.", now.Add(-time.Second), time.Time{}, true)
	reader := NewLocalDocumentRetriever(&failNthLocalDocumentRead{ResearchDocumentStore: store, fail: 2}, func(context.Context, c.Candidate) (bool, bool) { return true, true })
	got, err := reader.ReadDocuments(context.Background(), c.Envelope{DataScope: "public"}, engine.LocalDocumentRequest{Query: "Common literal retained phrase", Limit: 4})
	if err != nil || got.Code != "unavailable" || len(got.Items) != 1 || len(got.Missing) == 0 {
		t.Fatalf("verified item was discarded after later read failure: %+v err=%v", got, err)
	}
}

type tamperingLocalDocumentStore struct{ ResearchDocumentStore }

func (s tamperingLocalDocumentStore) FindStagingItemByNamespaceEventID(ctx context.Context, namespace, eventID string) (l1sqlite.L1StagingItem, bool, error) {
	item, found, err := s.ResearchDocumentStore.FindStagingItemByNamespaceEventID(ctx, namespace, eventID)
	if found && err == nil {
		item.RawText += " modified"
	}
	return item, found, err
}

func TestLocalDocumentRetrieverRejectsTamperedExactReadbackAndMissingPolicy(t *testing.T) {
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "research.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	body := "Tamper detection body."
	now := time.Now().UTC()
	insertLocalRecallFixture(t, store, "tampered", "kb:research", localRecallEvent(body), "public", "", body, now, time.Time{}, true)
	policy := func(context.Context, c.Candidate) (bool, bool) { return true, true }
	got, err := NewLocalDocumentRetriever(tamperingLocalDocumentStore{ResearchDocumentStore: store}, policy).ReadDocuments(context.Background(), c.Envelope{DataScope: "public"}, engine.LocalDocumentRequest{Query: "Tamper detection", Limit: 4})
	if err != nil || len(got.Items) != 0 || len(got.Missing) == 0 {
		t.Fatalf("tampered owner readback was accepted: %+v err=%v", got, err)
	}
	noPolicy, err := NewLocalDocumentRetriever(store, nil).ReadDocuments(context.Background(), c.Envelope{DataScope: "public"}, engine.LocalDocumentRequest{Query: "Tamper detection", Limit: 4})
	if err != nil || len(noPolicy.Items) != 0 || noPolicy.Code != "unavailable" {
		t.Fatalf("unconfigured rights policy was not fail-closed: %+v err=%v", noPolicy, err)
	}
}

func TestLocalDocumentRetrieverUsesFetchedAtOnlyWhenPublicationTimeIsAbsent(t *testing.T) {
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "research.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC)
	body := "Fallback fetched timestamp for exact source."
	insertLocalRecallFixture(t, store, "fetched-time", "kb:research", localRecallEvent(body), "public", "", body, now, time.Time{}, true)
	policy := func(context.Context, c.Candidate) (bool, bool) { return true, true }
	reader := NewLocalDocumentRetriever(store, policy)
	got, err := reader.ReadDocuments(context.Background(), c.Envelope{DataScope: "public"}, engine.LocalDocumentRequest{
		Query: "Fallback fetched", Limit: 4,
		TimeWindow: &c.TimeWindow{Since: now, Until: now.Add(time.Hour)},
	})
	if err != nil || len(got.Items) != 1 || got.Items[0].Document.TimeBasis != "fetched_at" || got.Items[0].Document.PublishedAt != nil || !got.Items[0].Document.RetrievedAt.Equal(now) {
		t.Fatalf("fetched-at fallback was not represented faithfully: %+v err=%v", got, err)
	}
	denied := NewLocalDocumentRetriever(store, func(context.Context, c.Candidate) (bool, bool) { return false, false })
	blocked, err := denied.ReadDocuments(context.Background(), c.Envelope{DataScope: "public"}, engine.LocalDocumentRequest{Query: "Fallback fetched", Limit: 4})
	if err != nil || len(blocked.Items) != 0 || len(blocked.Missing) == 0 {
		t.Fatalf("revoked retention rights were treated as readable: %+v err=%v", blocked, err)
	}
}
