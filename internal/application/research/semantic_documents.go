package research

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
)

const (
	researchSemanticMaxDocuments      = 20
	researchSemanticMaxChunks         = 2000
	researchSemanticMaxDocumentRunes  = 100000
	researchSemanticMaxQueryRunes     = 1000
	researchSemanticChunkRunes        = 512
	researchSemanticChunkOverlapRunes = 64
	researchSemanticEmbedBatchSize    = 16
	researchSemanticBuildDeadline     = 180 * time.Second
	researchSemanticParserRevision    = "canonical-research-text-v1"
	researchSemanticChunkerRevision   = "runes-512-overlap-64-v1"
)

// SemanticEmbeddingCapability is the current identity of an attested local
// embedding model. Revision is the SHA-256 asset revision supplied by Gateway.
type SemanticEmbeddingCapability struct {
	ModelID   string
	Revision  string
	Dimension int
}

// SemanticEmbeddingResult must echo the exact capability used for the call.
type SemanticEmbeddingResult struct {
	Capability SemanticEmbeddingCapability
	Vectors    []SemanticEmbeddingVector
}

type SemanticEmbeddingVector struct {
	Index  int
	Values []float32
}

// SemanticEmbeddingPort is backed by the authenticated local Gateway service.
type SemanticEmbeddingPort interface {
	Capability(context.Context) (SemanticEmbeddingCapability, error)
	Embed(context.Context, SemanticEmbeddingCapability, []string) (SemanticEmbeddingResult, error)
}

// ResearchDocumentExactReader re-reads one retained document by exact event key.
type ResearchDocumentExactReader interface {
	FindStagingItemByNamespaceEventID(context.Context, string, string) (l1sqlite.L1StagingItem, bool, error)
}

// ResearchDocumentVectorReader provides current scoped derived candidates.
type ResearchDocumentVectorReader interface {
	ReadResearchVectorCandidates(context.Context, l1sqlite.L1ResearchVectorReadRequest) (l1sqlite.L1ResearchVectorReadResult, error)
}

// ResearchDocumentVectorWriter atomically publishes one document generation.
type ResearchDocumentVectorWriter interface {
	PublishResearchDocumentVectors(context.Context, l1sqlite.L1ResearchVectorDocument) error
}

// ResearchSemanticRecallPorts opts LocalRecall into semantic document search.
// Recall receives a read-only vector port and cannot build or publish an index.
type ResearchSemanticRecallPorts struct {
	VectorReader ResearchDocumentVectorReader
	Embeddings   SemanticEmbeddingPort
	SourcePolicy SourcePolicy
}

// ResearchSemanticIndexPorts contains the additional writer needed by the
// explicit owner index operation.
type ResearchSemanticIndexPorts struct {
	VectorReader ResearchDocumentVectorReader
	VectorWriter ResearchDocumentVectorWriter
	Embeddings   SemanticEmbeddingPort
	SourcePolicy SourcePolicy
}

// ResearchDocumentIndexRequest names exact retained L1 event IDs to index.
type ResearchDocumentIndexRequest struct {
	EventIDs []string
}

// ResearchDocumentIndexResult reports only derived-index metadata and counts.
type ResearchDocumentIndexResult struct {
	DocumentsIndexed int
	ChunksIndexed    int
	ModelID          string
	AssetRevision    string // The attested embedding asset revision returned by Gateway.
	IndexRevision    string // SHA-256 identity over model ID, asset revision, and dimension.
	Dimension        int
}

// ResearchDocumentIndexBuilder coordinates bounded explicit index builds.
type ResearchDocumentIndexBuilder struct {
	documents    ResearchDocumentExactReader
	vectorReader ResearchDocumentVectorReader
	vectorWriter ResearchDocumentVectorWriter
	embeddings   SemanticEmbeddingPort
	sourcePolicy SourcePolicy
	gate         chan struct{}
}

// NewResearchDocumentIndexBuilder creates the serialized owner index builder.
func NewResearchDocumentIndexBuilder(documents ResearchDocumentExactReader, ports ResearchSemanticIndexPorts) *ResearchDocumentIndexBuilder {
	return &ResearchDocumentIndexBuilder{
		documents: documents, vectorReader: ports.VectorReader, vectorWriter: ports.VectorWriter,
		embeddings: ports.Embeddings, sourcePolicy: ports.SourcePolicy, gate: make(chan struct{}, 1),
	}
}

type researchSemanticPendingChunk struct {
	start  int
	end    int
	hash   string
	text   string
	vector []float32
}

type researchSemanticPendingDocument struct {
	item   l1sqlite.L1StagingItem
	chunks []researchSemanticPendingChunk
}

// Build indexes only the exact retained L1 event IDs in the authenticated
// envelope. Embeddings remain derived values; canonical text is never copied
// into vector storage or the result.
func (b *ResearchDocumentIndexBuilder) Build(ctx context.Context, envelope c.Envelope, request ResearchDocumentIndexRequest) (ResearchDocumentIndexResult, error) {
	if ctx == nil {
		return ResearchDocumentIndexResult{}, errors.New("research semantic index context is required")
	}
	buildCtx, cancel := context.WithTimeout(ctx, researchSemanticBuildDeadline)
	defer cancel()
	if b == nil || b.gate == nil {
		return ResearchDocumentIndexResult{}, errors.New("research semantic index builder is unavailable")
	}
	select {
	case b.gate <- struct{}{}:
		defer func() { <-b.gate }()
	case <-buildCtx.Done():
		return ResearchDocumentIndexResult{}, buildCtx.Err()
	}
	if b.documents == nil || b.vectorReader == nil || b.vectorWriter == nil || b.embeddings == nil || b.sourcePolicy == nil {
		return ResearchDocumentIndexResult{}, errors.New("research semantic index ports are unavailable")
	}
	if len(request.EventIDs) < 1 || len(request.EventIDs) > researchSemanticMaxDocuments {
		return ResearchDocumentIndexResult{}, errors.New("research semantic index requires 1 to 20 exact event IDs")
	}
	scope, err := recallScope(envelope)
	if err != nil {
		return ResearchDocumentIndexResult{}, err
	}
	ownerID := scope.UserID
	if scope.Scope == "public" && envelope.OwnerID != "" {
		return ResearchDocumentIndexResult{}, errors.New("public research semantic index must not include an owner")
	}
	namespace, err := stagingNamespace(envelope)
	if err != nil {
		return ResearchDocumentIndexResult{}, err
	}
	eventIDs, err := validateResearchSemanticEventIDs(request.EventIDs)
	if err != nil {
		return ResearchDocumentIndexResult{}, err
	}
	capability, err := b.semanticCapability(buildCtx)
	if err != nil {
		return ResearchDocumentIndexResult{}, err
	}
	indexRevision := researchSemanticIndexRevision(capability)
	readRequest := researchSemanticVectorReadRequest(scope.Scope, ownerID, capability, indexRevision)
	current, err := b.vectorReader.ReadResearchVectorCandidates(buildCtx, readRequest)
	if err != nil {
		return ResearchDocumentIndexResult{}, errors.New("read current scoped research semantic index")
	}
	if current.State == l1sqlite.L1ResearchVectorReadMissing || current.State == l1sqlite.L1ResearchVectorReadTruncated {
		return ResearchDocumentIndexResult{}, errors.New("current scoped research semantic index is missing or exceeds the 2000 chunk ceiling")
	}
	if current.State != l1sqlite.L1ResearchVectorReadEmpty && current.State != l1sqlite.L1ResearchVectorReadAvailable {
		return ResearchDocumentIndexResult{}, errors.New("current scoped research semantic index state is invalid")
	}

	documents := make([]researchSemanticPendingDocument, 0, len(eventIDs))
	flatChunks := make([]struct {
		document int
		chunk    int
	}, 0)
	requestedDocumentIDs := make(map[string]struct{}, len(eventIDs))
	chunkCount := 0
	for _, eventID := range eventIDs {
		if err := buildCtx.Err(); err != nil {
			return ResearchDocumentIndexResult{}, err
		}
		item, found, readErr := b.documents.FindStagingItemByNamespaceEventID(buildCtx, namespace, eventID)
		if readErr != nil || !found || !validResearchStagingEvidence(item, envelope, namespace, eventID) || !utf8.ValidString(item.RawText) {
			return ResearchDocumentIndexResult{}, errors.New("requested research event is unavailable or changed")
		}
		if !researchSemanticSourceAllowed(buildCtx, b.sourcePolicy, item) {
			return ResearchDocumentIndexResult{}, errors.New("requested research event is not currently permitted for retention")
		}
		if _, duplicate := requestedDocumentIDs[item.ID]; duplicate {
			return ResearchDocumentIndexResult{}, errors.New("research event IDs resolve to duplicate documents")
		}
		requestedDocumentIDs[item.ID] = struct{}{}
		if utf8.RuneCountInString(item.RawText) > researchSemanticMaxDocumentRunes {
			return ResearchDocumentIndexResult{}, errors.New("research semantic document exceeds the 100000 rune limit")
		}
		chunks := researchSemanticChunks(item.RawText)
		if len(chunks) == 0 {
			return ResearchDocumentIndexResult{}, errors.New("research semantic document has no indexable text")
		}
		chunkCount += len(chunks)
		if chunkCount > researchSemanticMaxChunks {
			return ResearchDocumentIndexResult{}, errors.New("research semantic build exceeds the 2000 chunk limit")
		}
		docIndex := len(documents)
		documents = append(documents, researchSemanticPendingDocument{item: item, chunks: chunks})
		for chunkIndex := range chunks {
			flatChunks = append(flatChunks, struct {
				document int
				chunk    int
			}{document: docIndex, chunk: chunkIndex})
		}
	}
	projectedCount := len(current.Candidates)
	for _, candidate := range current.Candidates {
		if _, replacing := requestedDocumentIDs[candidate.DocumentID]; replacing {
			projectedCount--
		}
	}
	projectedCount += chunkCount
	if projectedCount > researchSemanticMaxChunks {
		return ResearchDocumentIndexResult{}, errors.New("scoped current research semantic index would exceed the 2000 chunk ceiling")
	}

	texts := make([]string, len(flatChunks))
	for i, ref := range flatChunks {
		texts[i] = documents[ref.document].chunks[ref.chunk].text
	}
	for start := 0; start < len(texts); start += researchSemanticEmbedBatchSize {
		if err := buildCtx.Err(); err != nil {
			return ResearchDocumentIndexResult{}, err
		}
		end := start + researchSemanticEmbedBatchSize
		if end > len(texts) {
			end = len(texts)
		}
		vectors, embedErr := embedResearchSemantic(buildCtx, b.embeddings, capability, texts[start:end])
		if embedErr != nil {
			return ResearchDocumentIndexResult{}, errors.New("research semantic document embedding failed")
		}
		for i, vector := range vectors {
			ref := flatChunks[start+i]
			documents[ref.document].chunks[ref.chunk].vector = vector
		}
	}
	currentCapability, err := b.semanticCapability(buildCtx)
	if err != nil || currentCapability != capability {
		return ResearchDocumentIndexResult{}, errors.New("research embedding capability changed during index build")
	}
	for _, document := range documents {
		item, found, readErr := b.documents.FindStagingItemByNamespaceEventID(buildCtx, namespace, document.item.EventID)
		if readErr != nil || !found || item.ID != document.item.ID || item.RawHash != document.item.RawHash || !validResearchStagingEvidence(item, envelope, namespace, document.item.EventID) || !researchSemanticSourceAllowed(buildCtx, b.sourcePolicy, item) {
			return ResearchDocumentIndexResult{}, errors.New("research event changed or lost retention permission during index build")
		}
	}

	result := ResearchDocumentIndexResult{ModelID: capability.ModelID, AssetRevision: capability.Revision, IndexRevision: indexRevision, Dimension: capability.Dimension}
	for _, document := range documents {
		chunks := make([]l1sqlite.L1ResearchVectorChunk, len(document.chunks))
		for i, chunk := range document.chunks {
			chunks[i] = l1sqlite.L1ResearchVectorChunk{RuneStart: chunk.start, RuneEnd: chunk.end, SHA256: chunk.hash, Vector: chunk.vector}
		}
		if err := b.vectorWriter.PublishResearchDocumentVectors(buildCtx, l1sqlite.L1ResearchVectorDocument{
			Scope: scope.Scope, OwnerID: ownerID, DocumentID: document.item.ID, EventID: document.item.EventID,
			ExpectedRawHash: document.item.RawHash, ParserRevision: researchSemanticParserRevision,
			ChunkerRevision: researchSemanticChunkerRevision, ModelRevision: indexRevision,
			Dimension: capability.Dimension, Chunks: chunks,
		}); err != nil {
			return result, errors.New("publish research semantic document generation")
		}
		result.DocumentsIndexed++
		result.ChunksIndexed += len(chunks)
	}
	readback, err := b.vectorReader.ReadResearchVectorCandidates(buildCtx, readRequest)
	if err != nil || readback.State != l1sqlite.L1ResearchVectorReadAvailable || !researchSemanticReadbackMatches(readback.Candidates, documents, indexRevision, capability.Dimension) {
		return result, errors.New("published research semantic generations failed exact readback")
	}
	return result, nil
}

func (b *ResearchDocumentIndexBuilder) semanticCapability(ctx context.Context) (SemanticEmbeddingCapability, error) {
	capability, err := b.embeddings.Capability(ctx)
	if err != nil || !validSemanticEmbeddingCapability(capability) {
		return SemanticEmbeddingCapability{}, errors.New("attested local embedding capability is unavailable")
	}
	return capability, nil
}

func validateResearchSemanticEventIDs(eventIDs []string) ([]string, error) {
	seen := make(map[string]struct{}, len(eventIDs))
	validated := append([]string(nil), eventIDs...)
	for _, eventID := range validated {
		if eventID == "" || strings.TrimSpace(eventID) != eventID || len(eventID) > 512 || !utf8.ValidString(eventID) {
			return nil, errors.New("research semantic event IDs must be exact and bounded")
		}
		if _, duplicate := seen[eventID]; duplicate {
			return nil, errors.New("research semantic event IDs must be unique")
		}
		seen[eventID] = struct{}{}
	}
	sort.Strings(validated)
	return validated, nil
}

func validSemanticEmbeddingCapability(capability SemanticEmbeddingCapability) bool {
	if capability.ModelID == "" || strings.TrimSpace(capability.ModelID) != capability.ModelID || len(capability.ModelID) > 128 || !utf8.ValidString(capability.ModelID) {
		return false
	}
	if !strings.HasPrefix(capability.Revision, "sha256:") || !isSHA256(strings.TrimPrefix(capability.Revision, "sha256:")) {
		return false
	}
	return capability.Dimension > 0 && capability.Dimension <= l1sqlite.L1ResearchVectorMaxDimension
}

func researchSemanticIndexRevision(capability SemanticEmbeddingCapability) string {
	identity := fmt.Sprintf("%s\x00%s\x00%d", capability.ModelID, capability.Revision, capability.Dimension)
	sum := sha256.Sum256([]byte(identity))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func researchSemanticVectorReadRequest(scope, ownerID string, capability SemanticEmbeddingCapability, indexRevision string) l1sqlite.L1ResearchVectorReadRequest {
	return l1sqlite.L1ResearchVectorReadRequest{
		Scope: scope, OwnerID: ownerID, ParserRevision: researchSemanticParserRevision,
		ChunkerRevision: researchSemanticChunkerRevision, ModelRevision: indexRevision,
		Dimension: capability.Dimension, Limit: researchSemanticMaxChunks,
	}
}

func researchSemanticChunks(text string) []researchSemanticPendingChunk {
	runes := []rune(text)
	chunks := make([]researchSemanticPendingChunk, 0, (len(runes)+researchSemanticChunkRunes-1)/(researchSemanticChunkRunes-researchSemanticChunkOverlapRunes))
	for start := 0; start < len(runes); {
		end := start + researchSemanticChunkRunes
		if end > len(runes) {
			end = len(runes)
		}
		chunkText := string(runes[start:end])
		if strings.TrimSpace(chunkText) != "" {
			chunks = append(chunks, researchSemanticPendingChunk{start: start, end: end, hash: digestBytes([]byte(chunkText)), text: chunkText})
		}
		if end == len(runes) {
			break
		}
		start = end - researchSemanticChunkOverlapRunes
	}
	return chunks
}

func embedResearchSemantic(ctx context.Context, port SemanticEmbeddingPort, capability SemanticEmbeddingCapability, texts []string) ([][]float32, error) {
	result, err := port.Embed(ctx, capability, texts)
	if err != nil || result.Capability != capability || len(result.Vectors) != len(texts) {
		return nil, errors.New("research embedding response capability or item count mismatch")
	}
	vectors := make([][]float32, len(texts))
	seen := make([]bool, len(texts))
	for _, indexed := range result.Vectors {
		if indexed.Index < 0 || indexed.Index >= len(texts) || seen[indexed.Index] || !validSemanticVector(indexed.Values, capability.Dimension) {
			return nil, errors.New("research embedding response contains an invalid vector")
		}
		seen[indexed.Index] = true
		vectors[indexed.Index] = append([]float32(nil), indexed.Values...)
	}
	for _, present := range seen {
		if !present {
			return nil, errors.New("research embedding response is missing an indexed vector")
		}
	}
	return vectors, nil
}

func validSemanticVector(vector []float32, dimension int) bool {
	if len(vector) != dimension || dimension < 1 {
		return false
	}
	norm := float64(0)
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return false
		}
		norm += float64(value) * float64(value)
	}
	return norm > 0 && !math.IsInf(norm, 0)
}

func researchSemanticSourceAllowed(ctx context.Context, policy SourcePolicy, item l1sqlite.L1StagingItem) bool {
	if policy == nil {
		return false
	}
	document := researchDocumentFromStagingMeta(item)
	originalURL, originalErr := canonicalHTTPURL(metaString(item.Meta, "original_url"))
	finalURL, finalErr := canonicalHTTPURL(metaString(item.Meta, "final_url"))
	if originalErr != nil || finalErr != nil {
		return false
	}
	for _, sourceURL := range []string{originalURL, finalURL} {
		allowed, _ := policy(ctx, c.Candidate{SourceKind: document.SourceKind, ProviderID: document.ProviderID, URL: sourceURL})
		if !allowed {
			return false
		}
	}
	return true
}

func researchSemanticReadbackMatches(candidates []l1sqlite.L1ResearchVectorCandidate, documents []researchSemanticPendingDocument, indexRevision string, dimension int) bool {
	expected := make(map[string]researchSemanticPendingChunk)
	expectedRawHashes := make(map[string]string, len(documents))
	for _, document := range documents {
		documentKey := fmt.Sprintf("%s\x00%s", document.item.ID, document.item.EventID)
		expectedRawHashes[documentKey] = document.item.RawHash
		for index, chunk := range document.chunks {
			key := fmt.Sprintf("%s\x00%d", documentKey, index)
			expected[key] = chunk
		}
	}
	seen := make(map[string]struct{}, len(expected))
	for _, candidate := range candidates {
		documentKey := fmt.Sprintf("%s\x00%s", candidate.DocumentID, candidate.EventID)
		key := fmt.Sprintf("%s\x00%d", documentKey, candidate.ChunkIndex)
		want, ok := expected[key]
		if !ok {
			continue
		}
		if candidate.RawHash != expectedRawHashes[documentKey] || candidate.ParserRevision != researchSemanticParserRevision || candidate.ChunkerRevision != researchSemanticChunkerRevision || candidate.ModelRevision != indexRevision || candidate.Dimension != dimension || candidate.RuneStart != want.start || candidate.RuneEnd != want.end || candidate.ChunkSHA256 != want.hash || !equalSemanticVectors(candidate.Vector, want.vector) {
			return false
		}
		if _, duplicate := seen[key]; duplicate {
			return false
		}
		seen[key] = struct{}{}
	}
	return len(seen) == len(expected)
}

func equalSemanticVectors(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type researchSemanticRankedHit struct {
	hit        c.Hit
	score      float64
	eventID    string
	documentID string
	chunkIndex int
}

func (r *LocalRecall) recallSemanticDocuments(ctx context.Context, envelope c.Envelope, request c.RecallRequest, query string, limit int, ports ResearchSemanticRecallPorts) ([]c.Hit, bool, error) {
	if r == nil || r.documents == nil || ports.VectorReader == nil || ports.Embeddings == nil || ports.SourcePolicy == nil || len([]rune(query)) > researchSemanticMaxQueryRunes || !utf8.ValidString(query) {
		return []c.Hit{}, true, errors.New("semantic research documents are unavailable or query exceeds 1000 runes")
	}
	scope, err := recallScope(envelope)
	if err != nil {
		return []c.Hit{}, true, err
	}
	ownerID := scope.UserID
	if scope.Scope == "public" && envelope.OwnerID != "" {
		return []c.Hit{}, true, errors.New("public research semantic recall must not include an owner")
	}
	namespace, err := stagingNamespace(envelope)
	if err != nil {
		return []c.Hit{}, true, err
	}
	capability, err := ports.Embeddings.Capability(ctx)
	if err != nil || !validSemanticEmbeddingCapability(capability) {
		return []c.Hit{}, true, errors.New("attested local embedding capability is unavailable")
	}
	indexRevision := researchSemanticIndexRevision(capability)
	readResult, err := ports.VectorReader.ReadResearchVectorCandidates(ctx, researchSemanticVectorReadRequest(scope.Scope, ownerID, capability, indexRevision))
	if err != nil {
		return []c.Hit{}, true, errors.New("read scoped research semantic index")
	}
	if readResult.State == l1sqlite.L1ResearchVectorReadMissing || readResult.State == l1sqlite.L1ResearchVectorReadEmpty || readResult.State == l1sqlite.L1ResearchVectorReadTruncated || len(readResult.Candidates) == 0 {
		return []c.Hit{}, true, nil
	}
	if readResult.State != l1sqlite.L1ResearchVectorReadAvailable {
		return []c.Hit{}, true, errors.New("scoped research semantic index state is invalid")
	}
	queryVectors, err := embedResearchSemantic(ctx, ports.Embeddings, capability, []string{query})
	if err != nil || len(queryVectors) != 1 {
		return []c.Hit{}, true, errors.New("research semantic query embedding is unavailable")
	}
	currentCapability, err := ports.Embeddings.Capability(ctx)
	if err != nil || !validSemanticEmbeddingCapability(currentCapability) || currentCapability != capability {
		return []c.Hit{}, true, errors.New("research embedding capability changed during semantic recall")
	}
	allowedRefs := make(map[string]struct{}, len(request.DocumentRefs))
	for _, ref := range request.DocumentRefs {
		allowedRefs[ref] = struct{}{}
	}
	coveredRefs := make(map[string]struct{}, len(allowedRefs))
	hitsByDocument := make(map[string]researchSemanticRankedHit)
	unavailable := false
	for _, candidate := range readResult.Candidates {
		matchedRefs := make([]string, 0, 2)
		for ref := range allowedRefs {
			if ref == candidate.DocumentID || ref == candidate.EventID {
				matchedRefs = append(matchedRefs, ref)
			}
		}
		if len(allowedRefs) != 0 && len(matchedRefs) == 0 {
			continue
		}
		if candidate.ParserRevision != researchSemanticParserRevision || candidate.ChunkerRevision != researchSemanticChunkerRevision || candidate.ModelRevision != indexRevision || candidate.Dimension != capability.Dimension {
			unavailable = true
			continue
		}
		item, found, readErr := r.documents.FindStagingItemByNamespaceEventID(ctx, namespace, candidate.EventID)
		if readErr != nil {
			return semanticRankedHits(hitsByDocument, limit), true, errors.New("exact research semantic evidence readback failed")
		}
		if !found || !validResearchStagingEvidence(item, envelope, namespace, candidate.EventID) || item.ID != candidate.DocumentID || item.RawHash != candidate.RawHash {
			unavailable = true
			continue
		}
		if !researchSemanticSourceAllowed(ctx, ports.SourcePolicy, item) {
			unavailable = true
			continue
		}
		runes := []rune(item.RawText)
		if candidate.RuneStart < 0 || candidate.RuneEnd <= candidate.RuneStart || candidate.RuneEnd > len(runes) || !isSHA256(candidate.ChunkSHA256) || digestBytes([]byte(string(runes[candidate.RuneStart:candidate.RuneEnd]))) != candidate.ChunkSHA256 {
			unavailable = true
			continue
		}
		score, ok := researchSemanticCosine(queryVectors[0], candidate.Vector)
		if !ok {
			unavailable = true
			continue
		}
		for _, ref := range matchedRefs {
			coveredRefs[ref] = struct{}{}
		}
		basis, timestamp := researchStagingTime(item)
		if request.TimeWindow != nil && (timestamp.Before(request.TimeWindow.Since) || !timestamp.Before(request.TimeWindow.Until)) {
			continue
		}
		baseLocator := "research-document://" + item.Namespace + "/" + item.EventID
		locator := fmt.Sprintf("%s#runes=%d-%d", baseLocator, candidate.RuneStart, candidate.RuneEnd)
		document := researchDocumentFromStagingMeta(item)
		revision := document.Revision
		if revision == "" {
			revision = item.RawHash
		}
		hit := c.Hit{
			Target: "documents", ProviderID: "core.l1_staging", CanonicalRef: baseLocator,
			Revision: revision, Locator: locator, MatchKind: "semantic", EvidenceState: "acquired",
			Title: item.SourceURL, Excerpt: researchExcerpt(item.RawText, candidate.RuneStart, candidate.RuneEnd),
			Evidence:  &c.EvidenceRef{DocumentID: item.ID, ArtifactHash: item.RawHash, Start: candidate.RuneStart, End: candidate.RuneEnd, Locator: locator},
			TimeBasis: basis,
		}
		key := item.ID
		prior, exists := hitsByDocument[key]
		if !exists || score > prior.score || score == prior.score && candidate.ChunkIndex < prior.chunkIndex {
			hitsByDocument[key] = researchSemanticRankedHit{hit: hit, score: score, eventID: item.EventID, documentID: item.ID, chunkIndex: candidate.ChunkIndex}
		}
	}
	for ref := range allowedRefs {
		if _, covered := coveredRefs[ref]; !covered {
			unavailable = true
		}
	}
	return semanticRankedHits(hitsByDocument, limit), unavailable, nil
}

func semanticRankedHits(byDocument map[string]researchSemanticRankedHit, limit int) []c.Hit {
	ranked := make([]researchSemanticRankedHit, 0, len(byDocument))
	for _, hit := range byDocument {
		ranked = append(ranked, hit)
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		if ranked[i].eventID != ranked[j].eventID {
			return ranked[i].eventID < ranked[j].eventID
		}
		return ranked[i].documentID < ranked[j].documentID
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	hits := make([]c.Hit, len(ranked))
	for i, rankedHit := range ranked {
		hits[i] = rankedHit.hit
	}
	return hits
}

func mergeResearchDocumentHits(lexical, semantic []c.Hit, limit int) []c.Hit {
	if limit < 1 {
		return []c.Hit{}
	}
	capacity := len(lexical) + len(semantic)
	if capacity > limit {
		capacity = limit
	}
	merged := make([]c.Hit, 0, capacity)
	seen := make(map[string]struct{}, capacity)
	appendUnique := func(hit c.Hit) {
		if len(merged) >= limit {
			return
		}
		key := ""
		if hit.Evidence != nil && hit.Evidence.DocumentID != "" {
			key = "document:" + hit.Evidence.DocumentID
		} else if hit.CanonicalRef != "" {
			key = "ref:" + hit.CanonicalRef
		} else {
			key = hit.Target + "\x00" + hit.ProviderID + "\x00" + hit.Locator
		}
		if _, duplicate := seen[key]; duplicate {
			return
		}
		seen[key] = struct{}{}
		merged = append(merged, hit)
	}
	for rank := 0; len(merged) < limit && (rank < len(lexical) || rank < len(semantic)); rank++ {
		if rank < len(lexical) {
			appendUnique(lexical[rank])
		}
		if rank < len(semantic) {
			appendUnique(semantic[rank])
		}
	}
	return merged
}

func researchSemanticCosine(query, document []float32) (float64, bool) {
	if len(query) == 0 || len(query) != len(document) {
		return 0, false
	}
	dot, queryNorm, documentNorm := float64(0), float64(0), float64(0)
	for i := range query {
		q, d := float64(query[i]), float64(document[i])
		if math.IsNaN(q) || math.IsInf(q, 0) || math.IsNaN(d) || math.IsInf(d, 0) {
			return 0, false
		}
		dot += q * d
		queryNorm += q * q
		documentNorm += d * d
	}
	if queryNorm == 0 || documentNorm == 0 {
		return 0, false
	}
	score := dot / math.Sqrt(queryNorm*documentNorm)
	return score, !math.IsNaN(score) && !math.IsInf(score, 0)
}
