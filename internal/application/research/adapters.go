package research

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/webgather"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
)

const KnowledgeStagingTarget = "knowledge_staging"

// L1StagingStore is the exact write/readback surface used by the owner adapter.
type L1StagingStore interface {
	SaveStagingItem(context.Context, l1sqlite.L1StagingItem) (*l1sqlite.L1StagingItem, error)
	FindStagingItemByNamespaceEventID(context.Context, string, string) (l1sqlite.L1StagingItem, bool, error)
}

type Searcher interface {
	Search(context.Context, modulewebgather.SearchRequest) (modulewebgather.SearchResponse, error)
}

type Fetcher interface {
	FetchURL(context.Context, modulewebgather.FetchRequest) (modulewebgather.FetchResponse, error)
}

type ResearchFetchPacer interface {
	ResearchFetchPacing(context.Context, modulewebgather.FetchRequest) (time.Duration, bool, error)
}

type AcquisitionLeaseStore interface {
	ClaimPublicAcquisition(context.Context, string, time.Time) (l1sqlite.L1PublicAcquisitionLease, bool, error)
	ReleasePublicAcquisition(context.Context, l1sqlite.L1PublicAcquisitionLease, time.Time) (bool, error)
}

type SourcePolicy func(context.Context, c.Candidate) (retentionAllowed bool, primary bool)

// Adapters bind existing web gathering and the CORE-owned L1 staging store to
// the research engine ports. Unsupported research targets are intentionally
// left unavailable.
type Adapters struct {
	searcher           Searcher
	fetcher            Fetcher
	staging            L1StagingStore
	leases             AcquisitionLeaseStore
	sourcePolicy       SourcePolicy
	localMaterialOwner StructuredMaterialOwner
	now                func() time.Time
}

func NewAdapters(searcher Searcher, fetcher Fetcher, staging L1StagingStore) *Adapters {
	adapter := &Adapters{searcher: searcher, fetcher: fetcher, staging: staging, now: func() time.Time { return time.Now().UTC() }}
	if leases, ok := staging.(AcquisitionLeaseStore); ok {
		adapter.leases = leases
	} else if leases, ok := fetcher.(AcquisitionLeaseStore); ok {
		// Allows explicit test/alternate acquisition owners; production injects
		// the same L1 owner through the staging store.
		adapter.leases = leases
	}
	return adapter
}

func (a *Adapters) WithAcquisitionLeaseStore(leases AcquisitionLeaseStore) *Adapters {
	if a != nil {
		a.leases = leases
	}
	return a
}

func (a *Adapters) WithSourcePolicy(policy SourcePolicy) *Adapters {
	if a != nil {
		a.sourcePolicy = policy
	}
	return a
}

func (a *Adapters) WithStructuredMaterialOwner(owner StructuredMaterialOwner) *Adapters {
	if a != nil {
		a.localMaterialOwner = owner
	}
	return a
}

var _ engine.DiscoverPort = (*Adapters)(nil)
var _ engine.AcquirePort = (*Adapters)(nil)
var _ engine.OwnerPort = (*Adapters)(nil)
var _ Searcher = (*webgather.SearchUseCase)(nil)
var _ Fetcher = (*webgather.UseCase)(nil)

func (a *Adapters) Discover(ctx context.Context, input engine.DiscoveryRequest) (engine.DiscoveryResult, error) {
	if a == nil || a.searcher == nil {
		return engine.DiscoveryResult{Code: "unavailable"}, errors.New("research discovery is unavailable")
	}
	if ctx == nil || strings.TrimSpace(input.Request.Query) == "" {
		return engine.DiscoveryResult{Code: "invalid_request"}, errors.New("research query and context are required")
	}
	provider := strings.TrimSpace(input.Provider.ProviderID)
	if provider == "" || !input.Provider.Search || !input.Provider.Enabled || !input.Provider.Ready {
		return engine.DiscoveryResult{Code: "unavailable"}, errors.New("research search provider is unavailable")
	}
	limit := input.Request.Limits.Candidates
	if limit <= 0 {
		limit = modulewebgather.DefaultSearchLimit
	}
	searchRequest := modulewebgather.SearchRequest{
		Query: input.Request.Query, Provider: provider, Limit: limit,
		Language: input.Request.Language, Freshness: modulewebgather.DefaultSearchFreshness,
		Refresh: engine.FreshAcquisition(ctx),
	}
	resp, err := a.searchWithAcquisitionLease(ctx, provider, input.Request.Query, searchRequest)
	if err != nil {
		return engine.DiscoveryResult{Code: "search_failed"}, err
	}
	candidates := make([]c.Candidate, 0, len(resp.Results))
	queryRef := digestBytes([]byte(strings.TrimSpace(input.Request.Query)))
	for _, result := range resp.Results {
		canonicalURL, err := canonicalHTTPURL(result.URL)
		if err != nil {
			continue
		}
		kind := requestedSourceKind(input.Request.SourceKinds)
		candidates = append(candidates, c.Candidate{
			ProviderID: firstNonEmpty(result.SourceEngine, resp.Provider, provider),
			SourceKind: kind, URL: canonicalURL,
			StableID: digestBytes([]byte(canonicalURL)), DiscoveredAt: a.currentTime(),
			QueryRef: queryRef, Rank: result.Rank, Title: result.Title, Snippet: result.Snippet,
		})
	}
	return engine.DiscoveryResult{Candidates: candidates}, nil
}

func (a *Adapters) searchWithAcquisitionLease(ctx context.Context, provider, query string, request modulewebgather.SearchRequest) (modulewebgather.SearchResponse, error) {
	if (provider != "rss_atom" && provider != "sitemap") || !isAbsoluteHTTPURL(query) {
		return a.searcher.Search(ctx, request)
	}
	canonicalURL, err := l1sqlite.CanonicalPublicAcquisitionURL(query)
	if err != nil {
		return modulewebgather.SearchResponse{}, err
	}
	if a.leases == nil {
		return modulewebgather.SearchResponse{}, errors.New("research public acquisition lease is unavailable")
	}
	lease, claimed, err := a.leases.ClaimPublicAcquisition(ctx, canonicalURL, time.Now().UTC())
	if err != nil {
		return modulewebgather.SearchResponse{}, err
	}
	if !claimed {
		return modulewebgather.SearchResponse{}, errors.New("public source acquisition is already leased")
	}
	leaseCtx, cancel := context.WithDeadline(ctx, lease.ExpiresAt)
	response, searchErr := a.searcher.Search(leaseCtx, request)
	cancel()
	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cleanupCancel()
	released, releaseErr := a.leases.ReleasePublicAcquisition(cleanupCtx, lease, time.Now().UTC())
	if searchErr != nil {
		return response, searchErr
	}
	if releaseErr != nil {
		return modulewebgather.SearchResponse{}, releaseErr
	}
	if !released {
		return modulewebgather.SearchResponse{}, errors.New("research public acquisition lease expired before discovery completion")
	}
	return response, nil
}

func isAbsoluteHTTPURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && u != nil && u.IsAbs() && u.Hostname() != "" && (strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https"))
}

func (a *Adapters) Acquire(ctx context.Context, candidate c.Candidate) (result engine.AcquireResult, retErr error) {
	if a == nil || a.fetcher == nil {
		return engine.AcquireResult{Code: "unavailable"}, errors.New("research acquisition is unavailable")
	}
	if ctx == nil {
		return engine.AcquireResult{Code: "invalid_request"}, errors.New("research context is required")
	}
	canonicalURL, err := canonicalHTTPURL(candidate.URL)
	if err != nil {
		return engine.AcquireResult{Code: "invalid_candidate"}, err
	}
	if a.leases == nil {
		return engine.AcquireResult{Code: "unavailable"}, errors.New("research public acquisition lease is unavailable")
	}
	lease, claimed, err := a.leases.ClaimPublicAcquisition(ctx, canonicalURL, time.Now().UTC())
	if err != nil {
		return engine.AcquireResult{Code: "unavailable"}, err
	}
	if !claimed {
		return engine.AcquireResult{Code: "busy"}, errors.New("public source acquisition is already leased")
	}
	leaseCtx, cancel := context.WithDeadline(ctx, lease.ExpiresAt)
	defer cancel()
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cleanupCancel()
		released, releaseErr := a.leases.ReleasePublicAcquisition(cleanupCtx, lease, time.Now().UTC())
		if retErr == nil && (releaseErr != nil || !released) {
			result = engine.AcquireResult{Code: "unavailable"}
			if releaseErr != nil {
				retErr = releaseErr
			} else {
				retErr = errors.New("research public acquisition lease expired before completion")
			}
		}
	}()
	request := modulewebgather.FetchRequest{
		URL: canonicalURL, FetchProvider: modulewebgather.DefaultFetchProvider,
		Extractor: modulewebgather.DefaultExtractor, StoreStaging: false, StoreStagingSet: true,
		Policy: modulewebgather.DefaultFetchPolicy(), Refresh: engine.FreshAcquisition(ctx),
	}
	if err := paceResearchFetch(leaseCtx, a.fetcher, &request); err != nil {
		return engine.AcquireResult{Code: researchAcquireFailureCode(err)}, err
	}
	resp, err := a.fetcher.FetchURL(leaseCtx, request)
	if err != nil {
		return engine.AcquireResult{Code: researchAcquireFailureCode(err)}, err
	}
	if authInterstitial, _ := resp.Diagnostics["auth_interstitial"].(bool); authInterstitial {
		return engine.AcquireResult{Code: "unavailable"}, errors.New("fetch returned a password-form interstitial without substantive article content")
	}
	text := strings.TrimSpace(resp.ExtractedText)
	if resp.Status == "ok" && text == "" && cacheHit(resp) {
		if envelope, ok := engine.ExecutionEnvelope(ctx); ok {
			if cached, found := a.stagedBodyForCacheHit(ctx, envelope, canonicalURL, resp); found {
				resp.ExtractedText = cached.text
				resp.ExtractedChars = len([]rune(cached.text))
				if resp.FinalURL == "" {
					resp.FinalURL = cached.finalURL
				}
				text = cached.text
			}
		}
		if text == "" {
			request.Refresh = true
			if err := paceResearchFetch(leaseCtx, a.fetcher, &request); err != nil {
				return engine.AcquireResult{Code: researchAcquireFailureCode(err)}, err
			}
			resp, err = a.fetcher.FetchURL(leaseCtx, request)
			if err != nil {
				return engine.AcquireResult{Code: researchAcquireFailureCode(err)}, err
			}
			text = strings.TrimSpace(resp.ExtractedText)
		}
	}
	if resp.Status != "ok" || text == "" {
		return engine.AcquireResult{Code: "unavailable"}, errors.New("fetch did not return complete extracted text")
	}
	textHash := digestBytes([]byte(text))
	complete := resp.ExtractedChars > 0 && resp.ExtractedChars == len([]rune(text))
	finalURL, err := canonicalHTTPURL(firstNonEmpty(resp.FinalURL, canonicalURL))
	if err != nil {
		return engine.AcquireResult{Code: "invalid_response"}, err
	}
	retentionAllowed, primary := false, false
	if complete && a.sourcePolicy != nil {
		retentionAllowed, primary = a.sourcePolicy(ctx, candidate)
		finalCandidate := candidate
		finalCandidate.URL = finalURL
		finalRetention, finalPrimary := a.sourcePolicy(ctx, finalCandidate)
		retentionAllowed = retentionAllowed && finalRetention
		primary = primary && finalPrimary
	}
	doc := c.Document{
		ID:         firstNonEmpty(candidate.StableID, digestBytes([]byte(canonicalURL))),
		SourceKind: candidate.SourceKind, ProviderID: candidate.ProviderID,
		SourceRef:   firstNonEmpty(candidate.StableID, canonicalURL),
		OriginalURL: canonicalURL, FinalURL: finalURL,
		Title: resp.Title, RetrievedAt: a.currentTime(), ContentKind: resp.ContentType,
		ContentScope: "extracted_text", ContentState: "fetched", ExtractedHash: textHash,
		Text: text, Complete: complete,
		RetentionAllowed: retentionAllowed, Primary: primary,
		IndependentSource: independentSource(finalURL), Warnings: append([]string(nil), resp.SecurityWarnings...),
	}
	if !doc.Complete {
		doc.ContentState = "partial"
		doc.Text = ""
	}
	if !doc.Complete {
		return engine.AcquireResult{Document: doc, Code: "incomplete"}, nil
	}
	return engine.AcquireResult{Document: doc}, nil
}

func paceResearchFetch(ctx context.Context, fetcher Fetcher, request *modulewebgather.FetchRequest) error {
	pacer, ok := fetcher.(ResearchFetchPacer)
	if !ok {
		return nil
	}
	delay, refreshRateLimitedCache, err := pacer.ResearchFetchPacing(ctx, *request)
	if err != nil {
		return err
	}
	if err := waitForResearchFetchPacing(ctx, delay); err != nil {
		return err
	}
	if refreshRateLimitedCache {
		request.Refresh = true
	}
	return nil
}

func waitForResearchFetchPacing(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func researchAcquireFailureCode(err error) string {
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var fetchErr *modulewebgather.Error
	if errors.As(err, &fetchErr) && fetchErr.Code == modulewebgather.ErrRateLimited {
		return string(modulewebgather.ErrRateLimited)
	}
	return "fetch_failed"
}

func (a *Adapters) Commit(ctx context.Context, req engine.CommitRequest) (c.Receipt, error) {
	if a == nil || a.staging == nil {
		return c.Receipt{}, errors.New("knowledge staging owner is unavailable")
	}
	if ctx == nil {
		return c.Receipt{}, errors.New("research context is required")
	}
	if req.Target.Target != KnowledgeStagingTarget || req.Document == nil {
		return c.Receipt{}, errors.New("research target is unavailable")
	}
	doc := *req.Document
	if !doc.RetentionAllowed || !doc.Complete || strings.TrimSpace(doc.Text) == "" || doc.ContentState != "fetched" {
		return c.Receipt{}, errors.New("document retention permission and complete text are required")
	}
	scope := req.Envelope.DataScope
	if strings.TrimSpace(scope) == "" || strings.TrimSpace(scope) != scope || req.Target.DestinationRef != "" {
		return c.Receipt{}, errors.New("research destination must be empty and data scope must be canonical")
	}
	namespace, err := stagingNamespace(req.Envelope)
	if err != nil {
		return c.Receipt{}, err
	}
	contentHash := digestBytes([]byte(doc.Text))
	if !isSHA256(req.Digest) || req.Digest != contentHash || !isSHA256(doc.ExtractedHash) || doc.ExtractedHash != contentHash {
		return c.Receipt{}, errors.New("research document digest does not match complete extracted text")
	}
	expectedIdempotencyKey, keyErr := c.CommitIdempotencyKey(firstNonEmpty(req.ResearchRunID, req.Envelope.RunID), req.Target, contentHash, &doc)
	if strings.TrimSpace(req.Envelope.RunID) == "" || keyErr != nil || req.IdempotencyKey != expectedIdempotencyKey {
		return c.Receipt{}, errors.New("research idempotency key does not match the engine run and commit input")
	}
	if doc.LocalOrigin != nil {
		return a.commitLocalMaterial(ctx, req, doc, contentHash)
	}
	if !validResearchDocumentIdentity(doc) {
		return c.Receipt{}, errors.New("research document source identity or content scope is unsupported")
	}
	finalURL, err := canonicalHTTPURL(firstNonEmpty(doc.FinalURL, doc.OriginalURL))
	if err != nil {
		return c.Receipt{}, err
	}
	originalURL, err := canonicalHTTPURL(firstNonEmpty(doc.OriginalURL, doc.FinalURL))
	if err != nil {
		return c.Receipt{}, err
	}
	if a.sourcePolicy == nil {
		return c.Receipt{}, errors.New("current research source retention policy is unavailable")
	}
	for _, checkedURL := range []string{originalURL, finalURL} {
		candidate := c.Candidate{SourceKind: doc.SourceKind, ProviderID: doc.ProviderID, URL: checkedURL}
		retentionAllowed, _ := a.sourcePolicy(ctx, candidate)
		if !retentionAllowed {
			return c.Receipt{}, errors.New("current research source retention policy does not permit this document")
		}
	}
	sourceID := modulewebgather.SourceIDFromURL(finalURL)
	eventID := researchEventID(sourceID, finalURL, contentHash, doc)
	stagingHash := contentHash
	fetchedAt := a.currentTime()
	item, err := a.staging.SaveStagingItem(ctx, l1sqlite.L1StagingItem{
		Kind: l1sqlite.L1StagingKindExternalFetch, Namespace: namespace, EventID: eventID,
		SourceID: sourceID, SourceURL: finalURL, FetchedAt: fetchedAt, PublishedAt: researchPublishedAt(doc),
		RawText: doc.Text, RawHash: stagingHash, SummaryDraft: modulewebgather.TextPreview(doc.Text, 240),
		Keywords: []string{"web_gather", "research"}, LicenseNote: "retention explicitly authorized by research request",
		ValidationStatus: l1sqlite.L1StagingStatusPending,
		Meta: map[string]interface{}{
			"research_scope": scope, "research_namespace": namespace, "research_digest": contentHash,
			"research_idempotency_key": req.IdempotencyKey,
			"original_url":             originalURL, "final_url": finalURL,
			"source_kind": doc.SourceKind, "provider_id": doc.ProviderID,
			"original_hash": doc.OriginalHash, "extracted_hash": contentHash,
			"content_complete": true, "content_scope": doc.ContentScope, "retention_allowed": true,
			"revision": doc.Revision, "source_ref": doc.SourceRef, "published_at": publishedAtMetadata(doc.PublishedAt),
			"research_policy_revision":  req.Envelope.PolicyRevision,
			"research_identity_version": "v1",
		},
	})
	if err != nil {
		return c.Receipt{}, err
	}
	readback, found, err := a.staging.FindStagingItemByNamespaceEventID(ctx, namespace, eventID)
	if err != nil {
		return c.Receipt{}, err
	}
	if !found || item == nil || readback.ID != item.ID || !researchStagingItemMatches(readback, namespace, eventID, scope, originalURL, finalURL, doc, contentHash) || readback.RawText != doc.Text {
		return c.Receipt{}, errors.New("research staging exact readback did not match the committed document")
	}
	verifiedAt := a.currentTime()
	return c.Receipt{
		Owner: "core.l1_staging", OwnerRoute: KnowledgeStagingTarget,
		AuditRef: eventID, CanonicalID: readback.ID, Digest: contentHash,
		State: "committed", Scope: scope, CommittedAt: readback.CreatedAt, VerifiedAt: verifiedAt,
	}, nil
}

type stagedCacheBody struct {
	text     string
	finalURL string
}

func (a *Adapters) stagedBodyForCacheHit(ctx context.Context, envelope c.Envelope, originalURL string, response modulewebgather.FetchResponse) (stagedCacheBody, bool) {
	cacheHash := canonicalHash(response.RawHash)
	if a == nil || a.staging == nil || ctx == nil || cacheHash == "" {
		return stagedCacheBody{}, false
	}
	namespace, err := stagingNamespace(envelope)
	if err != nil {
		return stagedCacheBody{}, false
	}
	finalURL, err := canonicalHTTPURL(firstNonEmpty(response.FinalURL, originalURL))
	if err != nil {
		return stagedCacheBody{}, false
	}
	sourceID := modulewebgather.SourceIDFromURL(finalURL)
	eventID := modulewebgather.EventID(sourceID, finalURL, cacheHash)
	item, found, err := a.staging.FindStagingItemByNamespaceEventID(ctx, namespace, eventID)
	if err != nil || !found || item.Namespace != namespace || item.EventID != eventID || item.SourceURL != finalURL || item.SourceID != sourceID || item.RawText == "" || digestBytes([]byte(item.RawText)) != cacheHash || item.RawHash != cacheHash {
		return stagedCacheBody{}, false
	}
	meta := item.Meta
	metaScope, _ := meta["research_scope"].(string)
	metaNamespace, _ := meta["research_namespace"].(string)
	metaFinalURL, _ := meta["final_url"].(string)
	metaOriginalURL, _ := meta["original_url"].(string)
	metaDigest, _ := meta["research_digest"].(string)
	metaExtractedHash, _ := meta["extracted_hash"].(string)
	metaContentScope, _ := meta["content_scope"].(string)
	doc := researchDocumentFromStagingMeta(item)
	complete, _ := meta["content_complete"].(bool)
	retention, _ := meta["retention_allowed"].(bool)
	if metaScope != envelope.DataScope || metaNamespace != namespace || metaFinalURL != finalURL || metaOriginalURL != originalURL || metaDigest != cacheHash || metaExtractedHash != cacheHash || !complete || !retention || item.LicenseNote != "retention explicitly authorized by research request" || metaContentScope != "extracted_text" || doc.Revision != "" || !validResearchDocumentIdentity(doc) {
		return stagedCacheBody{}, false
	}
	return stagedCacheBody{text: item.RawText, finalURL: finalURL}, true
}

func canonicalHash(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "sha256:")
	if !isSHA256(value) {
		return ""
	}
	return value
}

func canonicalHTTPURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return "", errors.New("research URL must be an absolute HTTP(S) URL without userinfo")
	}
	u.Fragment = ""
	return u.String(), nil
}

func cacheHit(response modulewebgather.FetchResponse) bool {
	hit, ok := response.Diagnostics["cache_hit"].(bool)
	return ok && hit
}

func (a *Adapters) Verify(ctx context.Context, envelope c.Envelope, receipt c.Receipt) (c.Receipt, error) {
	if a == nil || a.staging == nil || ctx == nil {
		return c.Receipt{}, errors.New("knowledge staging verification is unavailable")
	}
	if receipt.Owner != "core.l1_staging" || receipt.OwnerRoute != KnowledgeStagingTarget || receipt.State != "committed" || receipt.Scope == "" || receipt.Scope != envelope.DataScope || !isSHA256(receipt.Digest) || receipt.AuditRef == "" || receipt.CanonicalID == "" {
		return c.Receipt{}, errors.New("research receipt is outside the authorized scope")
	}
	namespace, err := stagingNamespace(envelope)
	if err != nil {
		return c.Receipt{}, err
	}
	item, found, err := a.staging.FindStagingItemByNamespaceEventID(ctx, namespace, receipt.AuditRef)
	if err != nil {
		return c.Receipt{}, err
	}
	if found && item.Kind == l1sqlite.L1StagingKindSearchResult {
		return a.verifyLocalMaterialReceipt(ctx, envelope, receipt, item)
	}
	if receipt.LocalIdentity != "" {
		return c.Receipt{}, errors.New("local material receipt identity is unsupported for a nonlocal staging item")
	}
	if !found || item.ID != receipt.CanonicalID || item.Namespace != namespace || item.EventID != receipt.AuditRef || item.Kind != l1sqlite.L1StagingKindExternalFetch || item.RawHash != receipt.Digest || item.RawText == "" || digestBytes([]byte(item.RawText)) != receipt.Digest {
		return c.Receipt{}, errors.New("research receipt does not match the canonical staging record")
	}
	meta := item.Meta
	originalURL, originalErr := canonicalHTTPURL(metaString(meta, "original_url"))
	finalURL, finalErr := canonicalHTTPURL(metaString(meta, "final_url"))
	sourceID := modulewebgather.SourceIDFromURL(finalURL)
	doc := researchDocumentFromStagingMeta(item)
	if policyBoundResearchSource(doc.SourceKind) && metaString(meta, "research_policy_revision") != envelope.PolicyRevision {
		return c.Receipt{}, errors.New("research source retention policy must be revalidated")
	}
	if originalErr != nil || finalErr != nil || item.SourceURL != finalURL || item.SourceID != sourceID || researchEventID(sourceID, finalURL, receipt.Digest, doc) != receipt.AuditRef ||
		!researchStagingItemMatches(item, namespace, receipt.AuditRef, receipt.Scope, originalURL, finalURL, doc, receipt.Digest) {
		return c.Receipt{}, errors.New("research receipt scope, content, or completeness metadata mismatch")
	}
	if a.sourcePolicy == nil {
		return c.Receipt{}, errors.New("current research source retention policy is unavailable")
	}
	for _, checkedURL := range []string{originalURL, finalURL} {
		candidate := c.Candidate{SourceKind: metaString(meta, "source_kind"), ProviderID: metaString(meta, "provider_id"), URL: checkedURL}
		retentionAllowed, _ := a.sourcePolicy(ctx, candidate)
		if !retentionAllowed {
			return c.Receipt{}, errors.New("current research source retention policy no longer permits this document")
		}
	}
	receipt.VerifiedAt = a.currentTime()
	return receipt, nil
}

func researchStagingItemMatches(item l1sqlite.L1StagingItem, namespace, eventID, scope, originalURL, finalURL string, doc c.Document, contentHash string) bool {
	storedScope := metaString(item.Meta, "research_scope")
	storedDigest := metaString(item.Meta, "research_digest")
	storedNamespace := metaString(item.Meta, "research_namespace")
	storedOriginalURL := metaString(item.Meta, "original_url")
	storedFinalURL := metaString(item.Meta, "final_url")
	storedSourceKind := metaString(item.Meta, "source_kind")
	storedProviderID := metaString(item.Meta, "provider_id")
	storedExtractedHash := metaString(item.Meta, "extracted_hash")
	complete, _ := item.Meta["content_complete"].(bool)
	retentionAllowed, _ := item.Meta["retention_allowed"].(bool)
	return item.Kind == l1sqlite.L1StagingKindExternalFetch && item.Namespace == namespace && item.EventID == eventID &&
		item.SourceID == modulewebgather.SourceIDFromURL(finalURL) && item.SourceURL == finalURL && item.RawText != "" && digestBytes([]byte(item.RawText)) == contentHash && item.RawHash == contentHash &&
		(item.ValidationStatus == l1sqlite.L1StagingStatusPending || item.ValidationStatus == l1sqlite.L1StagingStatusValidated) &&
		item.LicenseNote == "retention explicitly authorized by research request" &&
		storedScope == scope && storedNamespace == namespace && storedDigest == contentHash && storedOriginalURL == originalURL && storedFinalURL == finalURL &&
		storedSourceKind == doc.SourceKind && storedProviderID == doc.ProviderID && storedExtractedHash == contentHash && complete && retentionAllowed &&
		metaString(item.Meta, "content_scope") == doc.ContentScope && validResearchDocumentIdentity(doc) &&
		metaString(item.Meta, "revision") == doc.Revision && (metaString(item.Meta, "source_ref") == doc.SourceRef || (metaString(item.Meta, "research_identity_version") == "" && metaString(item.Meta, "source_ref") == "" && doc.Revision == "" && (doc.SourceKind == "web" || doc.SourceKind == "rss"))) &&
		metaString(item.Meta, "published_at") == publishedAtMetadata(doc.PublishedAt) &&
		item.PublishedAt.Equal(researchPublishedAt(doc))
}

func validResearchDocumentIdentity(doc c.Document) bool {
	if strings.TrimSpace(doc.SourceRef) == "" || strings.TrimSpace(doc.SourceRef) != doc.SourceRef || len(doc.SourceRef) > 512 || strings.ContainsAny(doc.SourceRef, "\r\n\x00") {
		return false
	}
	if strings.TrimSpace(doc.Revision) != doc.Revision || len(doc.Revision) > 512 || strings.ContainsAny(doc.Revision, "\r\n\x00") {
		return false
	}
	switch {
	case doc.ContentScope == "extracted_text" && (doc.SourceKind == "web" || doc.SourceKind == "rss"):
		switch doc.ProviderID {
		case "direct", "google_browser_manual", "bing_rss", "bing_news_rss", "rss_atom", "sitemap", "searxng", "yacy", "local_cache":
			return doc.Revision == ""
		}
	case doc.SourceKind == "mediawiki" && doc.ProviderID == "mediawiki" && doc.ContentScope == "revision_text":
		return doc.Revision != ""
	case doc.SourceKind == "pypi" && doc.ProviderID == "pypi" && doc.ContentScope == "package_description":
		return doc.Revision != ""
	case doc.SourceKind == "github" && doc.ProviderID == "github" && doc.ContentScope == "repository_readme":
		return isGitRevision(doc.Revision)
	case doc.SourceKind == "huggingface" && doc.ProviderID == "huggingface" && (doc.ContentScope == "model_card" || doc.ContentScope == "dataset_card"):
		return isGitRevision(doc.Revision)
	case doc.SourceKind == "arxiv" && doc.ProviderID == "arxiv" && doc.ContentScope == "abstract":
		if len(doc.Revision) < 2 || doc.Revision[0] != 'v' || doc.Revision[1] < '1' || doc.Revision[1] > '9' {
			return false
		}
		for _, digit := range doc.Revision[2:] {
			if digit < '0' || digit > '9' {
				return false
			}
		}
		return true
	}
	return false
}

func policyBoundResearchSource(kind string) bool {
	switch kind {
	case "mediawiki", "pypi", "github", "huggingface", "arxiv":
		return true
	default:
		return false
	}
}

func researchDocumentFromStagingMeta(item l1sqlite.L1StagingItem) c.Document {
	publishedAt, _ := time.Parse(time.RFC3339Nano, metaString(item.Meta, "published_at"))
	var published *time.Time
	if !publishedAt.IsZero() {
		published = &publishedAt
	}
	sourceRef := metaString(item.Meta, "source_ref")
	if sourceRef == "" && metaString(item.Meta, "research_identity_version") == "" && metaString(item.Meta, "revision") == "" && (metaString(item.Meta, "source_kind") == "web" || metaString(item.Meta, "source_kind") == "rss") {
		if canonical, err := canonicalHTTPURL(metaString(item.Meta, "final_url")); err == nil {
			sourceRef = canonical
		}
	}
	return c.Document{
		SourceKind: metaString(item.Meta, "source_kind"), ProviderID: metaString(item.Meta, "provider_id"),
		SourceRef: sourceRef, Revision: metaString(item.Meta, "revision"),
		ContentScope: metaString(item.Meta, "content_scope"), PublishedAt: published,
	}
}

func researchPublishedAt(doc c.Document) time.Time {
	if doc.PublishedAt == nil {
		return time.Time{}
	}
	return doc.PublishedAt.UTC()
}

func publishedAtMetadata(value *time.Time) string {
	if value == nil || value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func researchEventID(sourceID, finalURL, contentHash string, doc c.Document) string {
	if doc.Revision == "" {
		return modulewebgather.EventID(sourceID, finalURL, contentHash)
	}
	identity, _ := json.Marshal(struct {
		SourceID, FinalURL, ContentHash, SourceKind, ProviderID, ContentScope, SourceRef, Revision string
	}{sourceID, finalURL, contentHash, doc.SourceKind, doc.ProviderID, doc.ContentScope, doc.SourceRef, doc.Revision})
	return "research:" + digestBytes(identity)
}

func metaString(meta map[string]interface{}, key string) string {
	value, _ := meta[key].(string)
	return value
}

// IdempotencyKey matches the research engine's run/target/destination/digest key.
func IdempotencyKey(runID, target, destinationRef, digest string) string {
	return digestBytes([]byte(runID + "\x00" + target + "\x00" + destinationRef + "\x00" + digest))
}

func stagingNamespace(envelope c.Envelope) (string, error) {
	switch envelope.DataScope {
	case "public":
		return l1sqlite.BuildL1Namespace(l1sqlite.NamespaceKindKnowledge, "research")
	case "user":
		ownerID := strings.TrimSpace(envelope.OwnerID)
		if ownerID == "" || ownerID != envelope.OwnerID {
			return "", errors.New("user research scope requires a canonical owner id")
		}
		return l1sqlite.BuildL1Namespace(l1sqlite.NamespaceKindUser, ownerID)
	default:
		return "", errors.New("research data scope must be public or user")
	}
}

func (a *Adapters) currentTime() time.Time {
	if a != nil && a.now != nil {
		return a.now().UTC()
	}
	return time.Now().UTC()
}

func requestedSourceKind(kinds []string) string {
	for _, kind := range kinds {
		if strings.TrimSpace(kind) != "" {
			return strings.TrimSpace(kind)
		}
	}
	return "web"
}

func independentSource(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func digestBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func isSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && strings.ToLower(value) == value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
