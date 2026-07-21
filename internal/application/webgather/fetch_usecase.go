package webgather

import (
	"context"
	"errors"
	"log"
	"regexp"
	"strings"
	"time"

	domainsecurity "github.com/Nyukimin/RenCrow_CORE/internal/domain/security"
	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
)

type UseCase struct {
	fetcher   modulewebgather.FetchProvider
	fetchers  map[string]modulewebgather.FetchProvider
	cache     FetchCache
	extractor modulewebgather.Extractor
	staging   modulewebgather.StagingWriter
	now       func() time.Time
}

func (u *UseCase) WithFetchCache(cache FetchCache) *UseCase {
	if u == nil {
		return u
	}
	u.cache = cache
	return u
}

func NewUseCase(fetcher modulewebgather.FetchProvider, extractor modulewebgather.Extractor, staging modulewebgather.StagingWriter) *UseCase {
	u := &UseCase{
		fetcher:   fetcher,
		fetchers:  map[string]modulewebgather.FetchProvider{},
		extractor: extractor,
		staging:   staging,
		now:       func() time.Time { return time.Now().UTC() },
	}
	if fetcher != nil {
		u.fetchers[modulewebgather.DefaultFetchProvider] = fetcher
	}
	return u
}

func (u *UseCase) WithFetchProvider(name string, fetcher modulewebgather.FetchProvider) *UseCase {
	if u == nil {
		return u
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		name = modulewebgather.DefaultFetchProvider
	}
	if u.fetchers == nil {
		u.fetchers = map[string]modulewebgather.FetchProvider{}
	}
	if fetcher == nil {
		delete(u.fetchers, name)
		if name == modulewebgather.DefaultFetchProvider {
			u.fetcher = nil
		}
		return u
	}
	u.fetchers[name] = fetcher
	if name == modulewebgather.DefaultFetchProvider {
		u.fetcher = fetcher
	}
	return u
}

func (u *UseCase) FetchURL(ctx context.Context, req modulewebgather.FetchRequest) (modulewebgather.FetchResponse, error) {
	req = normalizeRequest(req)
	normalizedURL, err := modulewebgather.NormalizeURL(req.URL, req.Policy.AllowLocalhost)
	if err != nil {
		return failure(req.URL, err, nil), err
	}
	req.URL = normalizedURL
	fetcher := u.fetchProvider(req.FetchProvider)
	if u == nil || fetcher == nil {
		err := modulewebgather.NewError(modulewebgather.ErrFetchFailed, "web gather fetch provider is not configured: "+req.FetchProvider)
		return failure(req.URL, err, nil), err
	}
	if u.extractor == nil {
		err := modulewebgather.NewError(modulewebgather.ErrExtractFailed, "web gather extractor is not configured")
		return failure(req.URL, err, nil), err
	}
	if req.StoreStaging && !req.DryRun && u.staging == nil {
		err := modulewebgather.NewError(modulewebgather.ErrStagingFailed, "web gather staging writer is not configured")
		return failure(req.URL, err, nil), err
	}
	if u.cache != nil && !req.Refresh {
		cached, hit, err := u.cache.Get(ctx, req, u.now())
		if err != nil {
			wrapped := modulewebgather.WrapError(modulewebgather.ErrCacheError, "failed to read web gather fetch cache", err)
			return failure(req.URL, wrapped, map[string]any{"fetch_provider": req.FetchProvider, "cache_hit": false}), wrapped
		}
		if hit {
			if cached.Status == "failed" {
				err := modulewebgather.NewError(cached.ErrorCode, cached.ErrorMessage)
				return cached, err
			}
			return cached, nil
		}
	}
	if u.cache != nil {
		if delay, err := u.cache.RateDelay(ctx, req.URL, u.now(), defaultRateMinInterval); err != nil {
			wrapped := modulewebgather.WrapError(modulewebgather.ErrCacheError, "failed to read web gather rate state", err)
			return failure(req.URL, wrapped, map[string]any{"fetch_provider": req.FetchProvider, "cache_hit": false}), wrapped
		} else if delay > 0 {
			err := modulewebgather.NewError(modulewebgather.ErrRateLimited, "web gather rate state requires waiting "+delay.String())
			resp := failure(req.URL, err, map[string]any{"fetch_provider": req.FetchProvider, "cache_hit": false, "retry_after_ms": delay.Milliseconds()})
			_ = u.saveFetchCache(ctx, req, resp)
			return resp, err
		}
	}
	log.Printf("web_gather.fetch_started url=%s fetch_provider=%s extractor=%s", modulewebgather.SafeURLForLog(req.URL), req.FetchProvider, req.Extractor)
	artifact, err := fetcher.Fetch(ctx, req.URL, req.Policy)
	if u.cache != nil {
		_ = u.cache.RecordRate(ctx, req.URL, u.now())
	}
	if err != nil {
		log.Printf("web_gather.fetch_failed url=%s error_code=%s elapsed_ms=%d", modulewebgather.SafeURLForLog(req.URL), errorCodeOf(err), artifact.Elapsed.Milliseconds())
		resp := failure(req.URL, err, map[string]any{"fetch_provider": req.FetchProvider, "cache_hit": false})
		_ = u.saveFetchCache(ctx, req, resp)
		return resp, err
	}
	log.Printf("web_gather.fetch_completed url=%s final_url=%s http_status=%d content_type=%s raw_bytes=%d elapsed_ms=%d", modulewebgather.SafeURLForLog(req.URL), modulewebgather.SafeURLForLog(artifact.FinalURL), artifact.StatusCode, artifact.ContentType, artifact.RawBytes, artifact.Elapsed.Milliseconds())
	doc, err := u.extractor.Extract(ctx, artifact, req.Extractor)
	if err != nil {
		log.Printf("web_gather.fetch_failed url=%s final_url=%s error_code=%s elapsed_ms=%d", modulewebgather.SafeURLForLog(req.URL), modulewebgather.SafeURLForLog(artifact.FinalURL), errorCodeOf(err), artifact.Elapsed.Milliseconds())
		resp := failure(req.URL, err, diagnosticsFromArtifact(req, artifact, nil))
		_ = u.saveFetchCache(ctx, req, resp)
		return resp, err
	}
	doc.Text = strings.TrimSpace(doc.Text)
	if doc.Text == "" {
		err := modulewebgather.NewError(modulewebgather.ErrEmptyContent, "extracted content is empty")
		log.Printf("web_gather.fetch_failed url=%s final_url=%s error_code=%s elapsed_ms=%d", modulewebgather.SafeURLForLog(req.URL), modulewebgather.SafeURLForLog(artifact.FinalURL), modulewebgather.ErrEmptyContent, artifact.Elapsed.Milliseconds())
		resp := failure(req.URL, err, diagnosticsFromArtifact(req, artifact, nil))
		_ = u.saveFetchCache(ctx, req, resp)
		return resp, err
	}
	if containsCredentialLikeText(doc.Text) {
		err := modulewebgather.NewError(modulewebgather.ErrBlockedByPolicy, "extracted content appears to contain credential material")
		log.Printf("web_gather.fetch_failed url=%s final_url=%s error_code=%s elapsed_ms=%d", modulewebgather.SafeURLForLog(req.URL), modulewebgather.SafeURLForLog(artifact.FinalURL), modulewebgather.ErrBlockedByPolicy, artifact.Elapsed.Milliseconds())
		resp := failure(req.URL, err, diagnosticsFromArtifact(req, artifact, nil))
		_ = u.saveFetchCache(ctx, req, resp)
		return resp, err
	}
	log.Printf("web_gather.extract_completed url=%s final_url=%s extractor=%s extracted_chars=%d", modulewebgather.SafeURLForLog(req.URL), modulewebgather.SafeURLForLog(artifact.FinalURL), doc.Extractor, len([]rune(doc.Text)))
	warnings := domainsecurity.DetectPromptInjectionWarnings(doc.Text)
	rawHash := modulewebgather.SHA256Text(doc.Text)
	meta := buildMeta(req, artifact, doc, rawHash, warnings)
	var staged modulewebgather.StagingRecord
	if req.StoreStaging && !req.DryRun {
		staged, err = u.staging.Save(ctx, req, artifact, doc, meta)
		if err != nil {
			wrapped := modulewebgather.WrapError(modulewebgather.ErrStagingFailed, "failed to save web gather staging item", err)
			log.Printf("web_gather.fetch_failed url=%s final_url=%s error_code=%s elapsed_ms=%d", modulewebgather.SafeURLForLog(req.URL), modulewebgather.SafeURLForLog(artifact.FinalURL), modulewebgather.ErrStagingFailed, artifact.Elapsed.Milliseconds())
			resp := failure(req.URL, wrapped, diagnosticsFromArtifact(req, artifact, doc.Meta))
			_ = u.saveFetchCache(ctx, req, resp)
			return resp, wrapped
		}
		if staged.RawHash != "" {
			rawHash = ensureSHA256Prefix(staged.RawHash)
		}
		log.Printf("web_gather.staging_saved url=%s final_url=%s staging_id=%s validation_status=%s raw_hash=%s security_warning_count=%d", modulewebgather.SafeURLForLog(req.URL), modulewebgather.SafeURLForLog(artifact.FinalURL), staged.ID, staged.ValidationStatus, rawHash, len(warnings))
	}
	resp := modulewebgather.FetchResponse{
		URL:              req.URL,
		FinalURL:         artifact.FinalURL,
		Status:           "ok",
		HTTPStatus:       artifact.StatusCode,
		ContentType:      artifact.ContentType,
		Title:            doc.Title,
		TextPreview:      modulewebgather.TextPreview(doc.Text, 240),
		ExtractedText:    doc.Text,
		RawHash:          rawHash,
		RawBytes:         artifact.RawBytes,
		ExtractedChars:   len([]rune(doc.Text)),
		StagingID:        staged.ID,
		ValidationStatus: staged.ValidationStatus,
		SecurityWarnings: warnings,
		Diagnostics:      diagnosticsFromArtifact(req, artifact, doc.Meta),
	}
	if req.DryRun {
		resp.Diagnostics["dry_run"] = true
	}
	_ = u.saveFetchCache(ctx, req, resp)
	return resp, nil
}

func (u *UseCase) saveFetchCache(ctx context.Context, req modulewebgather.FetchRequest, resp modulewebgather.FetchResponse) error {
	if u == nil || u.cache == nil || req.DryRun {
		return nil
	}
	ttl := defaultFetchCacheTTL
	if resp.Status != "ok" {
		ttl = defaultFailureCacheTTL
	}
	return u.cache.Save(ctx, req, resp, ttl)
}

func (u *UseCase) fetchProvider(name string) modulewebgather.FetchProvider {
	if u == nil {
		return nil
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		name = modulewebgather.DefaultFetchProvider
	}
	if u.fetchers != nil {
		return u.fetchers[name]
	}
	if name == modulewebgather.DefaultFetchProvider {
		return u.fetcher
	}
	return nil
}

func normalizeRequest(req modulewebgather.FetchRequest) modulewebgather.FetchRequest {
	req.Namespace = strings.TrimSpace(req.Namespace)
	if req.Namespace == "" {
		req.Namespace = modulewebgather.DefaultNamespace
	}
	req.SourceID = strings.TrimSpace(req.SourceID)
	req.FetchProvider = strings.ToLower(strings.TrimSpace(req.FetchProvider))
	if req.FetchProvider == "" {
		req.FetchProvider = modulewebgather.DefaultFetchProvider
	}
	req.Extractor = strings.TrimSpace(req.Extractor)
	if req.Extractor == "" {
		req.Extractor = modulewebgather.DefaultExtractor
	}
	req.LicenseNote = strings.TrimSpace(req.LicenseNote)
	if req.LicenseNote == "" {
		req.LicenseNote = modulewebgather.DefaultLicenseNote
	}
	req.Policy = req.Policy.WithDefaults()
	if !req.StoreStagingSet && !req.StoreStaging {
		req.StoreStaging = true
	}
	return req
}

func buildMeta(req modulewebgather.FetchRequest, artifact modulewebgather.FetchArtifact, doc modulewebgather.ExtractedDocument, rawHash string, warnings []string) map[string]any {
	meta := map[string]any{
		"tool":                    modulewebgather.DefaultToolName,
		"tool_version":            modulewebgather.DefaultToolVersion,
		"discovery_provider":      modulewebgather.DefaultDiscoveryProvider,
		"fetch_provider":          req.FetchProvider,
		"extractor":               doc.Extractor,
		"requested_extractor":     req.Extractor,
		"source_url":              req.URL,
		"canonical_url":           firstNonEmpty(doc.CanonicalURL, artifact.FinalURL, req.URL),
		"http_status":             artifact.StatusCode,
		"content_type":            artifact.ContentType,
		"fetched_at":              artifact.FetchedAt.UTC().Format(time.RFC3339),
		"elapsed_ms":              artifact.Elapsed.Milliseconds(),
		"raw_hash":                rawHash,
		"raw_bytes":               artifact.RawBytes,
		"extracted_chars":         len([]rune(doc.Text)),
		"title":                   doc.Title,
		"byline":                  doc.Byline,
		"site_name":               doc.SiteName,
		"security_warning_source": "web_gather",
		"security_warnings":       warnings,
		"review_required":         true,
		"auto_promote":            false,
		"license_note":            req.LicenseNote,
	}
	if !doc.PublishedAt.IsZero() {
		meta["published_at"] = doc.PublishedAt.UTC().Format(time.RFC3339)
	}
	if len(artifact.Redirects) > 0 {
		meta["redirect_count"] = len(artifact.Redirects)
	}
	for k, v := range artifact.Meta {
		if _, exists := meta[k]; !exists {
			meta[k] = v
		}
	}
	for k, v := range doc.Meta {
		if _, exists := meta[k]; !exists {
			meta[k] = v
		}
	}
	return meta
}

func diagnosticsFromArtifact(req modulewebgather.FetchRequest, artifact modulewebgather.FetchArtifact, extra map[string]any) map[string]any {
	out := map[string]any{
		"fetch_provider": req.FetchProvider,
		"extractor":      req.Extractor,
		"elapsed_ms":     artifact.Elapsed.Milliseconds(),
		"cache_hit":      false,
	}
	if artifact.ProviderName != "" {
		out["actual_fetch_provider"] = artifact.ProviderName
	}
	if artifact.FinalURL != "" {
		out["final_url"] = artifact.FinalURL
	}
	if artifact.StatusCode != 0 {
		out["http_status"] = artifact.StatusCode
	}
	if artifact.ContentType != "" {
		out["content_type"] = artifact.ContentType
	}
	for k, v := range extra {
		if _, exists := out[k]; !exists {
			out[k] = v
		}
	}
	return out
}

func failure(url string, err error, diagnostics map[string]any) modulewebgather.FetchResponse {
	if diagnostics == nil {
		diagnostics = map[string]any{}
	}
	code := modulewebgather.ErrFetchFailed
	message := ""
	var wgErr *modulewebgather.Error
	if errors.As(err, &wgErr) {
		code = wgErr.Code
		message = wgErr.Message
	} else if err != nil {
		message = err.Error()
	}
	return modulewebgather.FetchResponse{
		URL:          url,
		Status:       "failed",
		ErrorCode:    code,
		ErrorMessage: message,
		Diagnostics:  diagnostics,
	}
}

func errorCodeOf(err error) modulewebgather.ErrorCode {
	var wgErr *modulewebgather.Error
	if errors.As(err, &wgErr) {
		return wgErr.Code
	}
	return modulewebgather.ErrFetchFailed
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

var credentialLikeTextRE = regexp.MustCompile(`(?i)(authorization|set-cookie|cookie|api[_-]?key|access[_-]?token|refresh[_-]?token|password|secret)\s*[:=]|\bbearer\s+[A-Za-z0-9._~+/=-]{8,}`)

func containsCredentialLikeText(text string) bool {
	return credentialLikeTextRE.MatchString(text)
}

func ensureSHA256Prefix(hash string) string {
	hash = strings.TrimSpace(hash)
	if hash == "" || strings.HasPrefix(hash, "sha256:") {
		return hash
	}
	return "sha256:" + hash
}
