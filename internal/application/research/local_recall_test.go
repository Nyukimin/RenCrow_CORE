package research

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
)

func TestLocalRecallReturnsOnlyExactAuthorizedResearchDocuments(t *testing.T) {
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	insertLocalRecallFixture(t, store, "research-doc", "kb:research", "event-doc", "public", "", "検証可能な稲作制度の原資料。", now, now.Add(-24*time.Hour), true)
	insertLocalRecallFixture(t, store, "bad-meta", "kb:research", "event-bad", "public", "", "稲作制度の無許可資料。", now, time.Time{}, false)
	insertLocalRecallFixture(t, store, "alice-doc", "user:alice", "event-alice", "user", "alice", "稲作制度の個人資料。", now, time.Time{}, true)
	insertLocalRecallFixture(t, store, "bob-doc", "user:bob", "event-bob", "user", "bob", "稲作制度の別人資料。", now, time.Time{}, true)
	adapter := NewLocalRecall(nil, store, nil)
	result, err := adapter.Recall(context.Background(), c.Envelope{DataScope: "public"}, c.RecallRequest{
		Query: "稲作制度", Targets: []string{"documents"}, Limit: 20,
		TimeWindow: &c.TimeWindow{Since: now.Add(-48 * time.Hour), Until: now.Add(48 * time.Hour)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 1 || len(result.Missing) != 0 {
		t.Fatalf("result = %#v, want one verified hit", result)
	}
	hit := result.Hits[0]
	if hit.EvidenceState != "acquired" || hit.Target != "documents" || hit.TimeBasis != "published_at" || hit.Evidence == nil || hit.Evidence.DocumentID != "research-doc" || hit.Evidence.ArtifactHash != localRecallHash("検証可能な稲作制度の原資料。") || hit.Evidence.Start != 5 || hit.Evidence.End != 9 || !strings.Contains(hit.Locator, localRecallEvent("検証可能な稲作制度の原資料。")) {
		t.Fatalf("hit = %#v, missing verified document evidence fields", hit)
	}
	private, err := adapter.Recall(context.Background(), c.Envelope{DataScope: "user", OwnerID: "alice"}, c.RecallRequest{Query: "稲作制度", Targets: []string{"documents"}})
	if err != nil || len(private.Hits) != 1 || private.Hits[0].Evidence.DocumentID != "alice-doc" || private.Hits[0].TimeBasis != "fetched_at" || len(private.Missing) != 0 {
		t.Fatalf("alice documents = %#v, err=%v", private, err)
	}
	foreignRef, err := adapter.Recall(context.Background(), c.Envelope{DataScope: "user", OwnerID: "alice"}, c.RecallRequest{Query: "稲作制度", Targets: []string{"documents"}, DocumentRefs: []string{localRecallEvent("稲作制度の別人資料。")}})
	if err != nil || len(foreignRef.Hits) != 0 || len(foreignRef.Missing) != 0 {
		t.Fatalf("foreign user document ref leaked: result=%#v err=%v", foreignRef, err)
	}
}

func TestLocalRecallReportsUnavailableDocumentTarget(t *testing.T) {
	adapter := NewLocalRecall(nil, nil, nil)
	result, err := adapter.Recall(context.Background(), c.Envelope{DataScope: "public"}, c.RecallRequest{Query: "data", Targets: []string{"documents"}})
	if err != nil || len(result.Hits) != 0 || !containsRecallTarget(result.Missing, "documents") {
		t.Fatalf("unavailable documents = %#v, err=%v", result, err)
	}
}

func TestLocalRecallEnabledStructuredBackendNeverFallsBackWhenOwnerIsMissing(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repoPath, 0700); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoPath, "init", "-q")
	runGit(t, repoPath, "config", "user.email", "test@example.invalid")
	runGit(t, repoPath, "config", "user.name", "Research Test")
	if err := os.WriteFile(filepath.Join(repoPath, "example.go"), []byte("package example\n// literal evidence\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoPath, "add", "example.go")
	runGit(t, repoPath, "commit", "-q", "-m", "fixture")
	revision := strings.TrimSpace(runGit(t, repoPath, "rev-parse", "HEAD"))
	repository := RegisteredRepository{Ref: "repo-sample", Path: repoPath, AllowedRevisions: []string{revision}, Scope: "user", OwnerID: "ren"}
	adapter := NewLocalRecallWithStructured(nil, nil, []RegisteredRepository{repository}, ResearchStructuredRecallPorts{CodeGraphEnabled: true})
	result, err := adapter.Recall(context.Background(), c.Envelope{DataScope: "user", OwnerID: "ren"}, c.RecallRequest{
		Query: "literal evidence", Targets: []string{"code"}, RepoRef: repository.Ref, Revision: revision,
	})
	if err != nil || len(result.Hits) != 0 || !containsRecallTarget(result.Missing, "code") {
		t.Fatalf("enabled CodeGraph without its owner must be unavailable: result=%#v err=%v", result, err)
	}
}

func TestLocalRecallSkipsSemanticAndL1WhenPageIndexCoversExactRefs(t *testing.T) {
	documents := &localRecallDocumentStoreStub{}
	vectorReader := &localRecallMissingVectorReader{}
	embeddings := &localRecallEmbeddingStub{}
	structured := &localRecallPageIndexStub{hits: []c.Hit{localRecallPageIndexHit("spec-ledger", "revision-v1")}}
	recall := NewLocalRecallWithSemanticAndStructured(nil, documents, nil,
		ResearchSemanticRecallPorts{VectorReader: vectorReader, Embeddings: embeddings, SourcePolicy: semanticTestSourcePolicy(true)},
		ResearchStructuredRecallPorts{Owner: structured, PageIndexEnabled: true})
	result, err := recall.Recall(context.Background(), c.Envelope{DataScope: "user", OwnerID: "ren"}, c.RecallRequest{
		Query: "ledger policy", Targets: []string{"documents"}, DocumentRefs: []string{"spec-ledger@revision-v1"}, Limit: 4,
	})
	if err != nil || len(result.Hits) != 1 || len(result.Missing) != 0 || result.Hits[0].MatchKind != "pageindex_section" {
		t.Fatalf("fully covered PageIndex ref = %+v err=%v", result, err)
	}
	if structured.pageCalls != 1 || vectorReader.calls != 0 || embeddings.capabilityCalls != 0 || embeddings.embedCalls != 0 || documents.searchCalls != 0 {
		t.Fatalf("fully covered PageIndex ref triggered unrelated recall: page=%d vector=%d capability=%d embed=%d lexical=%d",
			structured.pageCalls, vectorReader.calls, embeddings.capabilityCalls, embeddings.embedCalls, documents.searchCalls)
	}
	if !pageIndexHitsCoverRequestedDocumentRefs([]string{"spec-ledger"}, result.Hits) {
		t.Fatal("owner-resolved unambiguous bare PDF ref was not covered by its exact-version evidence hit")
	}
}

func TestLocalRecallMixedPageIndexAndL1RefsKeepFallbackShortage(t *testing.T) {
	documents := &localRecallDocumentStoreStub{}
	vectorReader := &localRecallMissingVectorReader{}
	embedder := &localRecallEmbeddingStub{}
	structured := &localRecallPageIndexStub{hits: []c.Hit{localRecallPageIndexHit("spec-ledger", "revision-v1")}}
	recall := NewLocalRecallWithSemanticAndStructured(nil, documents, nil,
		ResearchSemanticRecallPorts{VectorReader: vectorReader, Embeddings: embedder, SourcePolicy: semanticTestSourcePolicy(true)},
		ResearchStructuredRecallPorts{Owner: structured, PageIndexEnabled: true})
	result, err := recall.Recall(context.Background(), c.Envelope{DataScope: "public"}, c.RecallRequest{
		Query: "ledger policy", Targets: []string{"documents"}, DocumentRefs: []string{"spec-ledger@revision-v1", "missing-l1-event"}, Limit: 4,
	})
	if err != nil || len(result.Hits) != 1 || result.Hits[0].CanonicalRef != "spec-ledger" || !containsRecallTarget(result.Missing, "documents") {
		t.Fatalf("mixed PDF and L1 refs must preserve the uncovered-ref shortage: result=%+v err=%v", result, err)
	}
	if structured.pageCalls != 1 || vectorReader.calls != 1 || documents.searchCalls != 1 {
		t.Fatalf("mixed refs did not use the existing semantic/L1 fallback: page=%d vector=%d lexical=%d", structured.pageCalls, vectorReader.calls, documents.searchCalls)
	}
}

func TestLocalRecallDoesNotTreatUnconfirmedPageIndexRefsAsCovered(t *testing.T) {
	for _, test := range []struct {
		name        string
		ref         string
		pageErr     error
		unavailable bool
	}{
		{name: "unknown reference", ref: "unknown-pdf@revision-v1", unavailable: true},
		{name: "wrong revision", ref: "spec-ledger@revision-wrong", unavailable: true},
		{name: "outside scope", ref: "spec-ledger@revision-v1", unavailable: true},
		{name: "wrong owner", ref: "spec-ledger@revision-v1", unavailable: true},
		{name: "backend stopped", ref: "spec-ledger@revision-v1", pageErr: errors.New("PageIndex backend stopped"), unavailable: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			documents := &localRecallDocumentStoreStub{}
			vectorReader := &localRecallMissingVectorReader{}
			embedder := &localRecallEmbeddingStub{}
			structured := &localRecallPageIndexStub{unavailable: test.unavailable, err: test.pageErr}
			recall := NewLocalRecallWithSemanticAndStructured(nil, documents, nil,
				ResearchSemanticRecallPorts{VectorReader: vectorReader, Embeddings: embedder, SourcePolicy: semanticTestSourcePolicy(true)},
				ResearchStructuredRecallPorts{Owner: structured, PageIndexEnabled: true})
			result, err := recall.Recall(context.Background(), c.Envelope{DataScope: "user", OwnerID: "ren"}, c.RecallRequest{
				Query: "ledger policy", Targets: []string{"documents"}, DocumentRefs: []string{test.ref}, Limit: 4,
			})
			if !containsRecallTarget(result.Missing, "documents") || len(result.Hits) != 0 {
				t.Fatalf("unconfirmed ref became successful: result=%+v err=%v", result, err)
			}
			if test.pageErr != nil && !errors.Is(err, test.pageErr) || test.pageErr == nil && err != nil {
				t.Fatalf("PageIndex error propagation = %v, want %v", err, test.pageErr)
			}
			if vectorReader.calls != 1 || documents.searchCalls != 1 {
				t.Fatalf("unconfirmed ref did not retain existing fallback: vector=%d lexical=%d", vectorReader.calls, documents.searchCalls)
			}
		})
	}
}

type localRecallPageIndexStub struct {
	hits        []c.Hit
	unavailable bool
	err         error
	pageCalls   int
}

func (stub *localRecallPageIndexStub) RecallPageIndexDocuments(context.Context, c.Envelope, c.RecallRequest, string, int) ([]c.Hit, bool, error) {
	stub.pageCalls++
	return append([]c.Hit(nil), stub.hits...), stub.unavailable, stub.err
}

func (*localRecallPageIndexStub) RecallCodeGraph(context.Context, c.Envelope, c.RecallRequest, string, int) ([]c.Hit, bool, error) {
	return nil, true, nil
}

type localRecallDocumentStoreStub struct{ searchCalls int }

func (stub *localRecallDocumentStoreStub) SearchResearchStagingDocuments(context.Context, string, string, string, []string, time.Time, time.Time, int) (l1sqlite.L1ResearchStagingSearchResult, error) {
	stub.searchCalls++
	return l1sqlite.L1ResearchStagingSearchResult{Items: []l1sqlite.L1StagingItem{}}, nil
}

func (*localRecallDocumentStoreStub) FindStagingItemByNamespaceEventID(context.Context, string, string) (l1sqlite.L1StagingItem, bool, error) {
	return l1sqlite.L1StagingItem{}, false, nil
}

type localRecallMissingVectorReader struct{ calls int }

func (reader *localRecallMissingVectorReader) ReadResearchVectorCandidates(context.Context, l1sqlite.L1ResearchVectorReadRequest) (l1sqlite.L1ResearchVectorReadResult, error) {
	reader.calls++
	return l1sqlite.L1ResearchVectorReadResult{State: l1sqlite.L1ResearchVectorReadMissing}, nil
}

type localRecallEmbeddingStub struct {
	capabilityCalls int
	embedCalls      int
}

func (stub *localRecallEmbeddingStub) Capability(context.Context) (SemanticEmbeddingCapability, error) {
	stub.capabilityCalls++
	return SemanticEmbeddingCapability{ModelID: "fixture", Revision: "sha256:" + strings.Repeat("a", 64), Dimension: 1}, nil
}

func (stub *localRecallEmbeddingStub) Embed(context.Context, SemanticEmbeddingCapability, []string) (SemanticEmbeddingResult, error) {
	stub.embedCalls++
	return SemanticEmbeddingResult{}, errors.New("embedding should not be called for an empty vector index")
}

func localRecallPageIndexHit(documentRef, revision string) c.Hit {
	locator := "pageindex://" + documentRef + "@" + revision + "/node/ledger-section/pages/2-3"
	return c.Hit{
		Target: "documents", CanonicalRef: documentRef, Revision: revision, Locator: locator,
		MatchKind: "pageindex_section", EvidenceState: "acquired", Evidence: &c.EvidenceRef{
			DocumentID: documentRef + "@" + revision, ArtifactHash: strings.Repeat("a", 64), Start: 2, End: 2, Locator: locator,
		},
	}
}

func TestLocalRecallKeepsLegacyRevisionlessHTTPEventCompatible(t *testing.T) {
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	body := "Legacy extracted HTTP evidence."
	eventID := localRecallEvent(body)
	insertLocalRecallFixture(t, store, "legacy", "kb:research", eventID, "public", "", body, time.Now().UTC(), time.Time{}, true)
	item, found, err := store.FindStagingItemByNamespaceEventID(context.Background(), "kb:research", eventID)
	if err != nil || !found {
		t.Fatalf("read legacy fixture: found=%t err=%v", found, err)
	}
	delete(item.Meta, "source_ref")
	delete(item.Meta, "published_at")
	delete(item.Meta, "revision")
	delete(item.Meta, "research_identity_version")
	if _, err := store.SaveStagingItem(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	result, err := NewLocalRecall(nil, store, nil).Recall(context.Background(), c.Envelope{DataScope: "public"}, c.RecallRequest{Query: "extracted HTTP evidence", Targets: []string{"documents"}})
	if err != nil || len(result.Hits) != 1 || result.Hits[0].Revision != localRecallHash(body) || !strings.Contains(result.Hits[0].CanonicalRef, eventID) {
		t.Fatalf("legacy HTTP event identity was not retained: result=%+v err=%v", result, err)
	}
}

func TestLocalRecallDelegatesKnowledgeThroughTheInjectedPort(t *testing.T) {
	port := localRecallFunc(func(_ context.Context, envelope c.Envelope, request c.RecallRequest) (c.RecallResult, error) {
		if envelope.DataScope != "public" || len(request.Targets) != 1 || request.Targets[0] != "knowledge" || request.Query != "known phrase" {
			t.Fatalf("knowledge call envelope=%#v request=%#v", envelope, request)
		}
		return c.RecallResult{Hits: []c.Hit{{Target: "knowledge", EvidenceState: "candidate"}}, Missing: []string{}}, nil
	})
	result, err := NewLocalRecall(port, nil, nil).Recall(context.Background(), c.Envelope{DataScope: "public"}, c.RecallRequest{Query: "known phrase"})
	if err != nil || len(result.Hits) != 1 || result.Hits[0].EvidenceState != "candidate" || len(result.Missing) != 0 {
		t.Fatalf("knowledge result = %#v, err=%v", result, err)
	}
}

type localRecallFunc func(context.Context, c.Envelope, c.RecallRequest) (c.RecallResult, error)

func (f localRecallFunc) Recall(ctx context.Context, envelope c.Envelope, request c.RecallRequest) (c.RecallResult, error) {
	return f(ctx, envelope, request)
}

func TestLocalRecallMarksDocumentScanLimitPartialEvenWithNoHits(t *testing.T) {
	adapter := NewLocalRecall(nil, limitedDocumentStore{}, nil)
	result, err := adapter.Recall(context.Background(), c.Envelope{DataScope: "public"}, c.RecallRequest{Query: "query", Targets: []string{"documents"}})
	if err != nil || len(result.Hits) != 0 || !containsRecallTarget(result.Missing, "documents") {
		t.Fatalf("limited document scan = %#v, err=%v", result, err)
	}
}

type limitedDocumentStore struct{}

func (limitedDocumentStore) SearchResearchStagingDocuments(context.Context, string, string, string, []string, time.Time, time.Time, int) (l1sqlite.L1ResearchStagingSearchResult, error) {
	return l1sqlite.L1ResearchStagingSearchResult{Items: []l1sqlite.L1StagingItem{}, ScanLimited: true}, nil
}

func (limitedDocumentStore) FindStagingItemByNamespaceEventID(context.Context, string, string) (l1sqlite.L1StagingItem, bool, error) {
	return l1sqlite.L1StagingItem{}, false, nil
}

func TestLocalRecallCodeUsesOnlyRegisteredExactCommitSnapshot(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repoPath, 0700); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoPath, "init", "-q")
	runGit(t, repoPath, "config", "user.email", "test@example.invalid")
	runGit(t, repoPath, "config", "user.name", "Research Test")
	if err := os.WriteFile(filepath.Join(repoPath, "example.go"), []byte("package example\n// oldcommit-evidence\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoPath, "add", "example.go")
	runGit(t, repoPath, "commit", "-q", "-m", "old")
	oldRevision := strings.TrimSpace(runGit(t, repoPath, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(repoPath, "example.go"), []byte("package example\n// currentcommit-evidence\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoPath, "commit", "-q", "-am", "current")
	currentRevision := strings.TrimSpace(runGit(t, repoPath, "rev-parse", "HEAD"))
	registered := RegisteredRepository{Ref: "repo:sample", Path: repoPath, AllowedRevisions: []string{oldRevision, currentRevision}, Scope: "public"}
	adapter := NewLocalRecall(nil, nil, []RegisteredRepository{registered})
	env := c.Envelope{DataScope: "public"}
	oldResult, err := adapter.Recall(context.Background(), env, c.RecallRequest{Query: "oldcommit-evidence", Targets: []string{"code"}, RepoRef: registered.Ref, Revision: oldRevision})
	if err != nil || len(oldResult.Hits) != 1 || oldResult.Hits[0].Revision != oldRevision || oldResult.Hits[0].EvidenceState != "acquired" {
		t.Fatalf("old exact commit recall = %#v, err=%v", oldResult, err)
	}
	currentResult, err := adapter.Recall(context.Background(), env, c.RecallRequest{Query: "currentcommit-evidence", Targets: []string{"code"}, RepoRef: registered.Ref, Revision: currentRevision})
	if err != nil || len(currentResult.Hits) != 1 || currentResult.Hits[0].Revision != currentRevision {
		t.Fatalf("current exact commit recall = %#v, err=%v", currentResult, err)
	}
	wrongRevision, err := adapter.Recall(context.Background(), env, c.RecallRequest{Query: "oldcommit-evidence", Targets: []string{"code"}, RepoRef: registered.Ref, Revision: strings.Repeat("a", 40)})
	if err != nil || len(wrongRevision.Hits) != 0 || !containsRecallTarget(wrongRevision.Missing, "code") {
		t.Fatalf("unregistered revision result = %#v, err=%v", wrongRevision, err)
	}
}

func insertLocalRecallFixture(t *testing.T, store *l1sqlite.L1SQLiteStore, id, namespace, eventID, scope, owner, body string, fetchedAt, publishedAt time.Time, allowed bool) {
	t.Helper()
	digest := localRecallHash(body)
	meta := map[string]any{
		"research_scope": scope, "research_namespace": namespace, "research_digest": digest,
		"content_complete": true, "retention_allowed": allowed,
		"source_kind": "web", "provider_id": "direct", "source_ref": id,
		"content_scope": "extracted_text", "extracted_hash": digest,
		"original_url": "https://example.invalid/", "final_url": "https://example.invalid/",
		"revision": "", "published_at": "", "research_identity_version": "v1",
	}
	var published any
	if !publishedAt.IsZero() {
		published = publishedAt
	}
	var publishedAtValue time.Time
	publishedMeta := ""
	if published != nil {
		publishedAtValue = publishedAt
		publishedMeta = publishedAt.UTC().Format(time.RFC3339Nano)
	}
	meta["published_at"] = publishedMeta
	if _, err := store.SaveStagingItem(context.Background(), l1sqlite.L1StagingItem{
		ID: id, Kind: l1sqlite.L1StagingKindExternalFetch, Namespace: namespace, EventID: localRecallEvent(body),
		SourceID: modulewebgather.SourceIDFromURL("https://example.invalid/"), SourceURL: "https://example.invalid/", FetchedAt: fetchedAt, PublishedAt: publishedAtValue,
		RawText: body, RawHash: digest, LicenseNote: "retention explicitly authorized by research request",
		ValidationStatus: l1sqlite.L1StagingStatusPending, Meta: meta,
	}); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, repoPath string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", repoPath}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

func localRecallHash(text string) string {
	digest := sha256.Sum256([]byte(text))
	return hex.EncodeToString(digest[:])
}

func localRecallEvent(body string) string {
	finalURL := "https://example.invalid/"
	sourceID := modulewebgather.SourceIDFromURL(finalURL)
	return modulewebgather.EventID(sourceID, finalURL, localRecallHash(body))
}

func containsRecallTarget(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
