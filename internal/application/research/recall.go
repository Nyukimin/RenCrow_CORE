package research

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	appkm "github.com/Nyukimin/RenCrow_CORE/internal/application/knowledgememory"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
)

const knowledgeSearchProvider = "knowledge.search"

// KnowledgeReadiness reports whether the startup-validated owner capability
// can serve the trusted envelope's public or user scope.
type KnowledgeReadiness func(context.Context, c.Envelope) bool

// RecallAdapter maps the existing indexed knowledge owner into Research recall.
// Search projections are candidates only because the owner does not provide an
// exact record/version/permission readback API. TimeWindow filters on the
// owner's SourceUpdatedAt (record update time), not a publication date.
type RecallAdapter struct {
	searcher  appkm.IndexedSearcher
	readiness KnowledgeReadiness
}

func NewRecallAdapter(searcher appkm.IndexedSearcher, readiness KnowledgeReadiness) *RecallAdapter {
	return &RecallAdapter{searcher: searcher, readiness: readiness}
}

var _ engine.RecallPort = (*RecallAdapter)(nil)

func (a *RecallAdapter) Recall(ctx context.Context, envelope c.Envelope, request c.RecallRequest) (c.RecallResult, error) {
	if ctx == nil {
		return c.RecallResult{}, errors.New("research recall context is required")
	}
	query := strings.TrimSpace(request.Query)
	if query == "" || request.Limit < 0 || request.Limit > 20 {
		return c.RecallResult{}, errors.New("invalid research recall query or limit")
	}
	if request.TimeWindow != nil && (request.TimeWindow.Since.IsZero() || request.TimeWindow.Until.IsZero() || !request.TimeWindow.Until.After(request.TimeWindow.Since)) {
		return c.RecallResult{}, errors.New("invalid research recall time window")
	}
	scope, err := recallScope(envelope)
	if err != nil {
		return c.RecallResult{}, err
	}
	targets := request.Targets
	if len(targets) == 0 {
		targets = []string{"knowledge"}
	}
	missing := make([]string, 0, len(targets))
	seen := make(map[string]struct{}, len(targets))
	knowledgeRequested := false
	for _, target := range targets {
		switch target {
		case "knowledge":
			knowledgeRequested = true
		case "documents", "code":
		default:
			return c.RecallResult{}, fmt.Errorf("unsupported research recall target %q", target)
		}
		if _, ok := seen[target]; !ok {
			seen[target] = struct{}{}
			if target != "knowledge" {
				missing = append(missing, target)
			}
		}
	}
	if !knowledgeRequested {
		return c.RecallResult{Hits: []c.Hit{}, Missing: missing}, nil
	}
	if a == nil || a.searcher == nil || a.readiness == nil || !a.readiness(ctx, envelope) {
		return c.RecallResult{Hits: []c.Hit{}, Missing: append(missing, "knowledge")}, nil
	}
	limit := request.Limit
	if limit == 0 {
		limit = 8
	}
	results, err := a.searcher.Search(ctx, appkm.SearchRequest{
		Scope: scope, Query: query, RecordType: "", Limit: limit,
	})
	if err != nil {
		return c.RecallResult{Hits: []c.Hit{}, Missing: append(missing, "knowledge")}, fmt.Errorf("knowledge search owner unavailable: %w", err)
	}
	hits := make([]c.Hit, 0, len(results))
	for _, result := range results {
		if !validRecallSearchResult(result, scope) || !withinRecallWindow(result.SourceUpdatedAt, request.TimeWindow) {
			continue
		}
		hits = append(hits, c.Hit{
			Target: "knowledge", ProviderID: knowledgeSearchProvider,
			CanonicalRef: fmt.Sprintf("knowledge://%s/%s/%s", scope.Scope, result.RecordType, url.PathEscape(result.RecordID)),
			Locator:      result.RecordID, MatchKind: "indexed_search", EvidenceState: "candidate",
		})
	}
	return c.RecallResult{Hits: hits, Missing: missing}, nil
}

func recallScope(envelope c.Envelope) (appkm.SearchScope, error) {
	switch envelope.DataScope {
	case appkm.SearchScopePublic:
		return appkm.SearchScope{Scope: appkm.SearchScopePublic}, nil
	case appkm.SearchScopeUser:
		ownerID := strings.TrimSpace(envelope.OwnerID)
		if ownerID == "" || ownerID != envelope.OwnerID {
			return appkm.SearchScope{}, errors.New("user research recall requires a canonical trusted owner id")
		}
		return appkm.SearchScope{Scope: appkm.SearchScopeUser, UserID: ownerID}, nil
	default:
		return appkm.SearchScope{}, errors.New("research recall requires public or user data scope")
	}
}

func validRecallSearchResult(result appkm.SearchResult, scope appkm.SearchScope) bool {
	if result.Scope != scope.Scope || result.UserID != scope.UserID || strings.TrimSpace(result.RecordID) == "" || strings.TrimSpace(result.Title) == "" {
		return false
	}
	if result.RecordType != "creative_knowledge" && result.RecordType != "news_knowledge" {
		return false
	}
	if result.Visibility != "public" && result.Visibility != "private" {
		return false
	}
	if scope.Scope == appkm.SearchScopePublic && result.Visibility != "public" {
		return false
	}
	if !isSHA256(result.ContentSHA256) {
		return false
	}
	if _, err := time.Parse(time.RFC3339Nano, result.SourceUpdatedAt); err != nil {
		return false
	}
	if _, err := time.Parse(time.RFC3339Nano, result.IndexedAt); err != nil {
		return false
	}
	return true
}

func withinRecallWindow(sourceUpdatedAt string, window *c.TimeWindow) bool {
	if window == nil {
		return true
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, sourceUpdatedAt)
	return err == nil && !updatedAt.Before(window.Since) && updatedAt.Before(window.Until)
}
