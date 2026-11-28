package research

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
)

func TestResearchSemanticIndexFindsParaphraseAndKeepsScopeAndLegacyDefault(t *testing.T) {
	store := newSemanticTestStore(t)
	defer store.Close()
	now := time.Now().UTC()
	publicBody := "稲作制度の概要と補助規則を記録した資料です。"
	aliceBody := "稲作制度の個人向け補助条件をまとめた資料です。"
	insertLocalRecallFixture(t, store, "public-doc", "kb:research", localRecallEvent(publicBody), "public", "", publicBody, now, time.Time{}, true)
	insertLocalRecallFixture(t, store, "alice-doc", "user:alice", localRecallEvent(aliceBody), "user", "alice", aliceBody, now, time.Time{}, true)
	embedder := newSemanticTestEmbedder()
	vectorStore := &trackingResearchVectorStore{base: store}
	policy := semanticTestSourcePolicy(true)
	index := NewResearchDocumentIndexBuilder(store, ResearchSemanticIndexPorts{
		VectorReader: vectorStore, VectorWriter: vectorStore, Embeddings: embedder, SourcePolicy: policy,
	})
	for _, item := range []struct {
		envelope c.Envelope
		body     string
	}{
		{c.Envelope{DataScope: "public"}, publicBody},
		{c.Envelope{DataScope: "user", OwnerID: "alice"}, aliceBody},
	} {
		result, err := index.Build(context.Background(), item.envelope, ResearchDocumentIndexRequest{EventIDs: []string{localRecallEvent(item.body)}})
		if err != nil || result.DocumentsIndexed != 1 || result.ChunksIndexed != 1 {
			t.Fatalf("semantic index build = %+v, err=%v", result, err)
		}
		if result.AssetRevision != embedder.capability.Revision || result.IndexRevision == result.AssetRevision || !strings.HasPrefix(result.IndexRevision, "sha256:") {
			t.Fatalf("index result conflates asset and derived index revisions: %+v", result)
		}
	}

	query := "rice policy paraphrase"
	legacy, err := NewLocalRecall(nil, store, nil).Recall(context.Background(), c.Envelope{DataScope: "public"}, c.RecallRequest{Query: query, Targets: []string{"documents"}})
	if err != nil || len(legacy.Hits) != 0 || len(legacy.Missing) != 0 {
		t.Fatalf("legacy lexical default = %+v, err=%v", legacy, err)
	}

	semantic := NewLocalRecallWithSemantic(nil, store, nil, ResearchSemanticRecallPorts{VectorReader: vectorStore, Embeddings: embedder, SourcePolicy: policy})
	result, err := semantic.Recall(context.Background(), c.Envelope{DataScope: "public"}, c.RecallRequest{Query: query, Targets: []string{"documents"}, Limit: 5})
	if err != nil || len(result.Hits) != 1 || len(result.Missing) != 0 {
		t.Fatalf("semantic paraphrase recall = %+v, err=%v", result, err)
	}
	hit := result.Hits[0]
	if hit.MatchKind != "semantic" || hit.EvidenceState != "acquired" || hit.Evidence == nil || hit.Evidence.DocumentID != "public-doc" || hit.Evidence.ArtifactHash != localRecallHash(publicBody) || hit.Evidence.Start != 0 || hit.Evidence.End != len([]rune(publicBody)) || !strings.Contains(hit.Locator, "#runes=0-") {
		t.Fatalf("semantic hit lacks exact canonical chunk evidence: %+v", hit)
	}
	if strings.Contains(hit.Excerpt, query) {
		t.Fatalf("semantic hit unexpectedly depends on literal query text: %+v", hit)
	}
	foreign, err := semantic.Recall(context.Background(), c.Envelope{DataScope: "user", OwnerID: "bob"}, c.RecallRequest{Query: query, Targets: []string{"documents"}})
	if err != nil || len(foreign.Hits) != 0 || !containsRecallTarget(foreign.Missing, "documents") {
		t.Fatalf("empty foreign-owner index was not reported unavailable: %+v, err=%v", foreign, err)
	}
	unindexedRef, err := semantic.Recall(context.Background(), c.Envelope{DataScope: "public"}, c.RecallRequest{Query: query, Targets: []string{"documents"}, DocumentRefs: []string{"unindexed-event"}})
	if err != nil || len(unindexedRef.Hits) != 0 || !containsRecallTarget(unindexedRef.Missing, "documents") {
		t.Fatalf("unindexed explicit ref was reported as a successful semantic no-match: %+v, err=%v", unindexedRef, err)
	}
}

func TestSemanticRecallInterleavesLexicalBaselineBeforeSemanticOverflow(t *testing.T) {
	store := newSemanticTestStore(t)
	defer store.Close()
	now := time.Now().UTC()
	const documentCount = 10
	fixtures := make([]struct {
		documentID string
		eventID    string
		body       string
	}, 0, documentCount)
	ids := make([]string, 0, documentCount)
	for i := 0; i < documentCount; i++ {
		suffix := string(rune('a' + i))
		documentID := "semantic-doc-" + suffix
		body := "稲作制度の資料です。 lexical-marker-" + suffix
		eventID := localRecallEvent(body)
		fixtures = append(fixtures, struct {
			documentID string
			eventID    string
			body       string
		}{documentID: documentID, eventID: eventID, body: body})
		ids = append(ids, eventID)
		insertLocalRecallFixture(t, store, documentID, "kb:research", eventID, "public", "", body, now, time.Time{}, true)
	}
	lexicalFixture := fixtures[0]
	for _, fixture := range fixtures[1:] {
		if fixture.eventID > lexicalFixture.eventID {
			lexicalFixture = fixture
		}
	}
	// Derive the marker from the selected body so the exact lexical result is
	// the last candidate under the semantic route's deterministic tie order.
	markerStart := strings.LastIndex(lexicalFixture.body, "lexical-marker-")
	lexicalQuery := lexicalFixture.body[markerStart:]
	embedder := newSemanticTestEmbedder()
	vectorStore := &trackingResearchVectorStore{base: store}
	policy := semanticTestSourcePolicy(true)
	index := NewResearchDocumentIndexBuilder(store, ResearchSemanticIndexPorts{
		VectorReader: vectorStore, VectorWriter: vectorStore, Embeddings: embedder, SourcePolicy: policy,
	})
	if _, err := index.Build(context.Background(), c.Envelope{DataScope: "public"}, ResearchDocumentIndexRequest{EventIDs: ids}); err != nil {
		t.Fatal(err)
	}
	semantic := NewLocalRecallWithSemantic(nil, store, nil, ResearchSemanticRecallPorts{VectorReader: vectorStore, Embeddings: embedder, SourcePolicy: policy})
	semanticOnly, unavailable, err := semantic.recallSemanticDocuments(context.Background(), c.Envelope{DataScope: "public"}, c.RecallRequest{Query: lexicalQuery, Targets: []string{"documents"}}, lexicalQuery, 5, *semantic.semantic)
	if err != nil || unavailable || len(semanticOnly) != 5 {
		t.Fatalf("semantic route = %#v unavailable=%t err=%v", semanticOnly, unavailable, err)
	}
	for _, hit := range semanticOnly {
		if hit.Evidence != nil && hit.Evidence.DocumentID == lexicalFixture.documentID {
			t.Fatalf("chosen lexical baseline unexpectedly fits in semantic top five: %#v", semanticOnly)
		}
	}
	lexicalOnly, unavailable, err := semantic.recallDocuments(context.Background(), c.Envelope{DataScope: "public"}, c.RecallRequest{Query: lexicalQuery, Targets: []string{"documents"}}, lexicalQuery, 5)
	if err != nil || unavailable || len(lexicalOnly) != 1 || lexicalOnly[0].Evidence == nil || lexicalOnly[0].Evidence.DocumentID != lexicalFixture.documentID {
		t.Fatalf("lexical baseline = %#v unavailable=%t err=%v", lexicalOnly, unavailable, err)
	}
	result, err := semantic.Recall(context.Background(), c.Envelope{DataScope: "public"}, c.RecallRequest{Query: lexicalQuery, Targets: []string{"documents"}, Limit: 5})
	if err != nil || len(result.Hits) != 5 || result.Hits[0].Evidence == nil || result.Hits[0].Evidence.DocumentID != lexicalOnly[0].Evidence.DocumentID {
		t.Fatalf("balanced lexical/semantic merge lost the exact baseline hit: %+v, err=%v", result, err)
	}
}

func TestResearchSemanticRecallRejectsChangedDeletedRevokedAndPolicyDeniedEvidence(t *testing.T) {
	store := newSemanticTestStore(t)
	defer store.Close()
	now := time.Now().UTC()
	body := "稲作制度の検証可能な根拠。"
	eventID := localRecallEvent(body)
	insertLocalRecallFixture(t, store, "semantic-doc", "kb:research", eventID, "public", "", body, now, time.Time{}, true)
	embedder := newSemanticTestEmbedder()
	vectorStore := &trackingResearchVectorStore{base: store}
	index := NewResearchDocumentIndexBuilder(store, ResearchSemanticIndexPorts{
		VectorReader: vectorStore, VectorWriter: vectorStore, Embeddings: embedder, SourcePolicy: semanticTestSourcePolicy(true),
	})
	if _, err := index.Build(context.Background(), c.Envelope{DataScope: "public"}, ResearchDocumentIndexRequest{EventIDs: []string{eventID}}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		mutate func(l1sqlite.L1StagingItem) (l1sqlite.L1StagingItem, bool)
		policy SourcePolicy
	}{
		{name: "changed", mutate: func(item l1sqlite.L1StagingItem) (l1sqlite.L1StagingItem, bool) {
			item.RawText += " changed"
			return item, true
		}, policy: semanticTestSourcePolicy(true)},
		{name: "deleted", mutate: func(item l1sqlite.L1StagingItem) (l1sqlite.L1StagingItem, bool) { return item, false }, policy: semanticTestSourcePolicy(true)},
		{name: "revoked", mutate: func(item l1sqlite.L1StagingItem) (l1sqlite.L1StagingItem, bool) {
			item.Meta = cloneSemanticMeta(item.Meta)
			item.Meta["retention_allowed"] = false
			return item, true
		}, policy: semanticTestSourcePolicy(true)},
		{name: "current source policy denied", mutate: func(item l1sqlite.L1StagingItem) (l1sqlite.L1StagingItem, bool) { return item, true }, policy: semanticTestSourcePolicy(false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			documents := semanticDocumentStoreOverride{ResearchDocumentStore: store, mutate: tc.mutate}
			recall := NewLocalRecallWithSemantic(nil, documents, nil, ResearchSemanticRecallPorts{VectorReader: vectorStore, Embeddings: embedder, SourcePolicy: tc.policy})
			result, err := recall.Recall(context.Background(), c.Envelope{DataScope: "public"}, c.RecallRequest{Query: "rice policy paraphrase", Targets: []string{"documents"}})
			if err != nil || len(result.Hits) != 0 || !containsRecallTarget(result.Missing, "documents") {
				t.Fatalf("stale or denied evidence was accepted: %+v, err=%v", result, err)
			}
		})
	}
}

func TestResearchSemanticBuildFailurePreservesGenerationAndRecallDoesNotWrite(t *testing.T) {
	store := newSemanticTestStore(t)
	defer store.Close()
	body := "稲作制度の検証可能な根拠。"
	eventID := localRecallEvent(body)
	insertLocalRecallFixture(t, store, "semantic-doc", "kb:research", eventID, "public", "", body, time.Now().UTC(), time.Time{}, true)
	embedder := newSemanticTestEmbedder()
	vectorStore := &trackingResearchVectorStore{base: store}
	index := NewResearchDocumentIndexBuilder(store, ResearchSemanticIndexPorts{
		VectorReader: vectorStore, VectorWriter: vectorStore, Embeddings: embedder, SourcePolicy: semanticTestSourcePolicy(true),
	})
	envelope := c.Envelope{DataScope: "public"}
	if _, err := index.Build(context.Background(), envelope, ResearchDocumentIndexRequest{EventIDs: []string{eventID}}); err != nil {
		t.Fatal(err)
	}
	before, err := vectorStore.ReadResearchVectorCandidates(context.Background(), researchSemanticVectorReadRequest("public", "", embedder.capability, researchSemanticIndexRevision(embedder.capability)))
	if err != nil || before.State != l1sqlite.L1ResearchVectorReadAvailable || len(before.Candidates) != 1 {
		t.Fatalf("initial vector generation = %+v, err=%v", before, err)
	}
	prior := before.Candidates[0]
	wrongEcho := embedder.capability
	wrongEcho.ModelID = "different-model"
	embedder.responseCapability = &wrongEcho
	if _, err := index.Build(context.Background(), envelope, ResearchDocumentIndexRequest{EventIDs: []string{eventID}}); err == nil {
		t.Fatal("mismatched per-call embedding capability was accepted")
	}
	embedder.responseCapability = nil
	after, err := vectorStore.ReadResearchVectorCandidates(context.Background(), researchSemanticVectorReadRequest("public", "", embedder.capability, researchSemanticIndexRevision(embedder.capability)))
	if err != nil || after.State != l1sqlite.L1ResearchVectorReadAvailable || len(after.Candidates) != 1 || after.Candidates[0].GenerationKey != prior.GenerationKey || !equalSemanticVectors(after.Candidates[0].Vector, prior.Vector) {
		t.Fatalf("failed build replaced prior generation: %+v, err=%v", after, err)
	}

	vectorStore.publishCalls = 0
	recall := NewLocalRecallWithSemantic(nil, store, nil, ResearchSemanticRecallPorts{VectorReader: vectorStore, Embeddings: embedder, SourcePolicy: semanticTestSourcePolicy(true)})
	if _, err := recall.Recall(context.Background(), envelope, c.RecallRequest{Query: "rice policy paraphrase", Targets: []string{"documents"}}); err != nil {
		t.Fatal(err)
	}
	if vectorStore.publishCalls != 0 {
		t.Fatalf("recall published %d vector generations", vectorStore.publishCalls)
	}

	stale := embedder.capability
	stale.Revision = "sha256:" + strings.Repeat("b", 64)
	embedder.capability = stale
	result, err := recall.Recall(context.Background(), envelope, c.RecallRequest{Query: "稲作制度", Targets: []string{"documents"}})
	if err != nil || len(result.Hits) != 1 || result.Hits[0].MatchKind != "literal_substring" || !containsRecallTarget(result.Missing, "documents") || vectorStore.publishCalls != 0 {
		t.Fatalf("stale model index was returned or written during recall: %+v, err=%v writes=%d", result, err, vectorStore.publishCalls)
	}
}

func TestResearchSemanticChunkWindowsAreDeterministicRunesWithOverlap(t *testing.T) {
	text := strings.Repeat("あ", 600)
	runes := []rune(text)
	chunks := researchSemanticChunks(text)
	if len(chunks) != 2 || chunks[0].start != 0 || chunks[0].end != 512 || chunks[1].start != 448 || chunks[1].end != 600 || chunks[0].hash != localRecallHash(string(runes[:512])) || chunks[1].hash != localRecallHash(string(runes[448:600])) {
		t.Fatalf("semantic rune chunks = %+v", chunks)
	}
}

func newSemanticTestStore(t *testing.T) *l1sqlite.L1SQLiteStore {
	t.Helper()
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "semantic.db"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

type semanticTestEmbedder struct {
	capability         SemanticEmbeddingCapability
	responseCapability *SemanticEmbeddingCapability
	calls              int
}

func newSemanticTestEmbedder() *semanticTestEmbedder {
	return &semanticTestEmbedder{capability: SemanticEmbeddingCapability{ModelID: "embed_bge_m3", Revision: "sha256:" + strings.Repeat("a", 64), Dimension: 2}}
}

func (e *semanticTestEmbedder) Capability(context.Context) (SemanticEmbeddingCapability, error) {
	return e.capability, nil
}

func (e *semanticTestEmbedder) Embed(_ context.Context, capability SemanticEmbeddingCapability, texts []string) (SemanticEmbeddingResult, error) {
	e.calls++
	responseCapability := capability
	if e.responseCapability != nil {
		responseCapability = *e.responseCapability
	}
	vectors := make([]SemanticEmbeddingVector, len(texts))
	for i, text := range texts {
		values := []float32{0, 1}
		if strings.Contains(text, "稲作") || strings.Contains(text, "lexical-marker-") || text == "rice policy paraphrase" {
			values = []float32{1, 0}
		}
		vectors[i] = SemanticEmbeddingVector{Index: i, Values: values}
	}
	// The gateway may return indexed results in a different order than inputs.
	for left, right := 0, len(vectors)-1; left < right; left, right = left+1, right-1 {
		vectors[left], vectors[right] = vectors[right], vectors[left]
	}
	return SemanticEmbeddingResult{Capability: responseCapability, Vectors: vectors}, nil
}

func semanticTestSourcePolicy(allowed bool) SourcePolicy {
	return func(context.Context, c.Candidate) (bool, bool) { return allowed, allowed }
}

type trackingResearchVectorStore struct {
	base         *l1sqlite.L1SQLiteStore
	publishCalls int
}

func (s *trackingResearchVectorStore) ReadResearchVectorCandidates(ctx context.Context, request l1sqlite.L1ResearchVectorReadRequest) (l1sqlite.L1ResearchVectorReadResult, error) {
	return s.base.ReadResearchVectorCandidates(ctx, request)
}

func (s *trackingResearchVectorStore) PublishResearchDocumentVectors(ctx context.Context, input l1sqlite.L1ResearchVectorDocument) error {
	s.publishCalls++
	return s.base.PublishResearchDocumentVectors(ctx, input)
}

type semanticDocumentStoreOverride struct {
	ResearchDocumentStore
	mutate func(l1sqlite.L1StagingItem) (l1sqlite.L1StagingItem, bool)
}

func (s semanticDocumentStoreOverride) FindStagingItemByNamespaceEventID(ctx context.Context, namespace, eventID string) (l1sqlite.L1StagingItem, bool, error) {
	item, found, err := s.ResearchDocumentStore.FindStagingItemByNamespaceEventID(ctx, namespace, eventID)
	if err != nil || !found {
		return item, found, err
	}
	item, found = s.mutate(item)
	return item, found, nil
}

func cloneSemanticMeta(source map[string]any) map[string]any {
	clone := make(map[string]any, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
