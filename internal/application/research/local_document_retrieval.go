package research

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
)

// LocalDocumentRetriever exposes complete, already-retained L1 records to the
// Engine collection. It shares LocalRecall's scoped search and exact-read
// validation; it never performs network access or writes staging records.
type LocalDocumentRetriever struct {
	documents       ResearchDocumentStore
	sourcePolicy    SourcePolicy
	structuredOwner StructuredMaterialOwner
}

// StructuredMaterialOwner reads complete source material from a current,
// owner-verified PageIndex or CodeGraph snapshot and revalidates that source
// before CORE stores it. Newly read material has no staging receipt yet.
type StructuredMaterialOwner interface {
	ReadLocalMaterial(context.Context, c.Envelope, engine.LocalDocumentRequest) (engine.LocalDocumentResult, error)
	VerifyLocalMaterial(context.Context, c.Envelope, c.Document) error
}

func NewLocalDocumentRetriever(documents ResearchDocumentStore, sourcePolicy SourcePolicy) *LocalDocumentRetriever {
	return &LocalDocumentRetriever{documents: documents, sourcePolicy: sourcePolicy}
}

func (r *LocalDocumentRetriever) WithStructuredMaterialOwner(owner StructuredMaterialOwner) *LocalDocumentRetriever {
	if r != nil {
		r.structuredOwner = owner
	}
	return r
}

var _ engine.LocalDocumentPort = (*LocalDocumentRetriever)(nil)

func (r *LocalDocumentRetriever) ReadDocuments(ctx context.Context, envelope c.Envelope, request engine.LocalDocumentRequest) (engine.LocalDocumentResult, error) {
	target := request.Target
	if target == "" {
		target = "documents"
	}
	if target != "documents" && target != "code" {
		return engine.LocalDocumentResult{}, errors.New("invalid local research document target")
	}
	request.Target = target
	if target == "code" {
		if r == nil || r.structuredOwner == nil {
			return engine.LocalDocumentResult{Items: []engine.LocalDocumentItem{}, Missing: []string{"code"}, Code: "unavailable"}, nil
		}
		if request.RepoRef == "" || request.Revision == "" {
			return engine.LocalDocumentResult{Items: []engine.LocalDocumentItem{}, Missing: []string{"code"}, Code: "unavailable"}, nil
		}
		return r.structuredOwner.ReadLocalMaterial(ctx, envelope, request)
	}

	result, err := r.readStagedDocuments(ctx, envelope, request)
	if err != nil || r == nil || r.structuredOwner == nil {
		return result, err
	}
	structured, structuredErr := r.structuredOwner.ReadLocalMaterial(ctx, envelope, request)
	if structuredErr != nil {
		result.Missing = appendMissing(result.Missing, "documents")
		if result.Code == "" {
			result.Code = "unavailable"
		}
		return result, nil
	}
	result.Items = append(result.Items, structured.Items...)
	for _, missing := range structured.Missing {
		result.Missing = appendMissing(result.Missing, missing)
	}
	if structured.Code != "" {
		result.Code = structured.Code
	}
	return result, nil
}

func (r *LocalDocumentRetriever) readStagedDocuments(ctx context.Context, envelope c.Envelope, request engine.LocalDocumentRequest) (engine.LocalDocumentResult, error) {
	query := strings.TrimSpace(request.Query)
	if ctx == nil || request.Limit < 1 || request.Limit > l1sqlite.L1ResearchRecallMaxLimit || query == "" || len([]rune(query)) > localRecallMaxQueryRunes || !utf8.ValidString(query) || len(request.DocumentRefs) > l1sqlite.L1ResearchRecallMaxLimit {
		return engine.LocalDocumentResult{}, errors.New("invalid local research document request")
	}
	seenRefs := make(map[string]struct{}, len(request.DocumentRefs))
	for _, ref := range request.DocumentRefs {
		if ref == "" || ref != strings.TrimSpace(ref) || len(ref) > 512 {
			return engine.LocalDocumentResult{}, errors.New("invalid local research document ref")
		}
		if _, duplicate := seenRefs[ref]; duplicate {
			return engine.LocalDocumentResult{}, errors.New("duplicate local research document ref")
		}
		seenRefs[ref] = struct{}{}
	}
	if request.TimeWindow != nil && (request.TimeWindow.Since.IsZero() || !request.TimeWindow.Since.Before(request.TimeWindow.Until)) {
		return engine.LocalDocumentResult{}, errors.New("invalid local research document time window")
	}
	if _, err := recallScope(envelope); err != nil {
		return engine.LocalDocumentResult{}, err
	}
	if r == nil || r.documents == nil || r.sourcePolicy == nil {
		return engine.LocalDocumentResult{Items: []engine.LocalDocumentItem{}, Missing: []string{"documents"}, Code: "unavailable"}, nil
	}
	namespace, err := stagingNamespace(envelope)
	if err != nil {
		return engine.LocalDocumentResult{}, err
	}
	since, until := time.Time{}, time.Time{}
	if request.TimeWindow != nil {
		since, until = request.TimeWindow.Since, request.TimeWindow.Until
	}
	searched, err := r.documents.SearchResearchStagingDocuments(ctx, envelope.DataScope, envelope.OwnerID, query, request.DocumentRefs, since, until, request.Limit)
	if err != nil {
		return engine.LocalDocumentResult{Items: []engine.LocalDocumentItem{}, Missing: []string{"documents"}, Code: "unavailable"}, nil
	}
	result := engine.LocalDocumentResult{Items: []engine.LocalDocumentItem{}, Missing: []string{}}
	for _, candidate := range searched.Items {
		if candidate.Namespace != namespace || candidate.EventID == "" {
			continue
		}
		item, found, readErr := r.documents.FindStagingItemByNamespaceEventID(ctx, namespace, candidate.EventID)
		if readErr != nil {
			return engine.LocalDocumentResult{Items: result.Items, Missing: []string{"documents"}, Code: "unavailable"}, nil
		}
		if !found || !validResearchStagingEvidence(item, envelope, namespace, candidate.EventID) {
			result.Missing = []string{"documents"}
			continue
		}
		basis, timestamp := researchStagingTime(item)
		if request.TimeWindow != nil && (timestamp.Before(request.TimeWindow.Since) || !timestamp.Before(request.TimeWindow.Until)) {
			continue
		}
		meta := item.Meta
		originalURL, originalErr := canonicalHTTPURL(metaString(meta, "original_url"))
		finalURL, finalErr := canonicalHTTPURL(metaString(meta, "final_url"))
		if originalErr != nil || finalErr != nil {
			result.Missing = []string{"documents"}
			continue
		}
		doc := researchDocumentFromStagingMeta(item)
		allowed := true
		for _, checkedURL := range []string{originalURL, finalURL} {
			candidate := c.Candidate{SourceKind: doc.SourceKind, ProviderID: doc.ProviderID, URL: checkedURL}
			urlAllowed, _ := r.sourcePolicy(ctx, candidate)
			allowed = allowed && urlAllowed
		}
		if !allowed {
			result.Missing = []string{"documents"}
			continue
		}
		// The staging metadata does not preserve the acquisition-time Primary
		// decision. Keep it false instead of promoting a secondary source using
		// today's policy result.
		retrievedAt := item.FetchedAt.UTC()
		document := c.Document{
			ID: item.ID, SourceKind: doc.SourceKind, ProviderID: doc.ProviderID, SourceRef: doc.SourceRef,
			OriginalURL: originalURL, FinalURL: finalURL, Title: finalURL, PublishedAt: doc.PublishedAt,
			RetrievedAt: retrievedAt, TimeBasis: basis, Revision: doc.Revision, ContentScope: doc.ContentScope,
			ContentState: "fetched", OriginalHash: metaString(meta, "original_hash"), ExtractedHash: item.RawHash,
			Text: item.RawText, RetentionAllowed: true, Complete: true,
			IndependentSource: independentSource(finalURL),
		}
		receipt := c.Receipt{
			Owner: "core.l1_staging", OwnerRoute: KnowledgeStagingTarget, AuditRef: item.EventID,
			CanonicalID: item.ID, Digest: item.RawHash, State: "committed", Scope: envelope.DataScope,
			CommittedAt: item.CreatedAt,
		}
		result.Items = append(result.Items, engine.LocalDocumentItem{Document: document, Receipt: receipt})
	}
	if searched.ScanLimited || searched.CandidatesTruncated {
		result.Missing = []string{"documents"}
	}
	return result, nil
}
