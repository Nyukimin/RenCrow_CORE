package sourcefetcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	domainsecurity "github.com/Nyukimin/RenCrow_CORE/internal/domain/security"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	webgatherinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/webgather"
	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
	"github.com/mmcdole/gofeed"
	xhtml "golang.org/x/net/html"
)

type RegistryStore interface {
	DueSourceRegistryEntries(ctx context.Context, now time.Time) ([]l1sqlite.L1SourceRegistryEntry, error)
	SourceTrustScores(ctx context.Context) (map[string]float64, error)
	StageSourceRegistryFetch(ctx context.Context, sourceID string, payload l1sqlite.L1SourceFetchPayload) (*l1sqlite.L1StagingItem, error)
	ValidateStagingItem(ctx context.Context, id string, policy l1sqlite.L1StagingValidationPolicy) (*l1sqlite.L1StagingValidationResult, error)
	PromoteValidatedStagingItemToNews(ctx context.Context, id string, category string) (*l1sqlite.L1NewsItem, error)
	PromoteValidatedStagingItemToKnowledge(ctx context.Context, id string, domain string) (*l1sqlite.L1KnowledgeItem, error)
	MarkSourceRegistryFetched(ctx context.Context, sourceID string, fetchedAt time.Time, status string, lastError string) error
}

type RegistryArticleFetchStore interface {
	ClaimNewsArticleFetch(ctx context.Context, rawURL string, now time.Time) (*l1sqlite.L1NewsArticleFetch, bool, error)
	CompleteNewsArticleFetch(ctx context.Context, rawURL string, completion l1sqlite.L1NewsArticleFetchCompletion, now time.Time) error
	FailNewsArticleFetch(ctx context.Context, rawURL string, errorCode string, now time.Time, retryAfter time.Duration) error
	ReopenIncompleteNewsArticleFetch(ctx context.Context, rawURL string, minimumChars int, now time.Time) error
}

type RegistrySourceLister interface {
	ListSourceRegistryEntries(ctx context.Context, enabledOnly bool) ([]l1sqlite.L1SourceRegistryEntry, error)
}

type RegistryNewsLister interface {
	RecentNewsItems(ctx context.Context, category string, limit int) ([]l1sqlite.L1NewsItem, error)
}

type SweepOptions struct {
	LimitPerSource    int
	MinimumTrustScore float64
	ArticleFallback   modulewebgather.FetchProvider
}

type SweepResult struct {
	Sources           int
	Staged            int
	Warnings          int
	Validated         int
	PromotedNews      int
	PromotedKnowledge int
	Failed            int
	ArticleFetched    int
	ArticleReused     int
	ArticleDeferred   int
	SkippedExisting   int
}

func SweepDueSources(ctx context.Context, store RegistryStore, now time.Time, opts SweepOptions) (SweepResult, error) {
	if store == nil {
		return SweepResult{}, fmt.Errorf("source registry store is nil")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if opts.LimitPerSource <= 0 {
		opts.LimitPerSource = 10
	}
	sources, err := store.DueSourceRegistryEntries(ctx, now)
	if err != nil {
		return SweepResult{}, err
	}
	return sweepRegistrySources(ctx, store, sources, now, opts)
}

// SweepAllFeedSources polls every enabled RSS/Atom source regardless of its
// configured due time. Linked articles remain URL-deduplicated by RegistryStore.
func SweepAllFeedSources(ctx context.Context, store interface {
	RegistryStore
	RegistrySourceLister
}, now time.Time, opts SweepOptions) (SweepResult, error) {
	if store == nil {
		return SweepResult{}, fmt.Errorf("source registry store is nil")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if opts.LimitPerSource <= 0 {
		opts.LimitPerSource = 10
	}
	entries, err := store.ListSourceRegistryEntries(ctx, true)
	if err != nil {
		return SweepResult{}, err
	}
	sources := make([]l1sqlite.L1SourceRegistryEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Kind == l1sqlite.L1SourceKindRSS || entry.Kind == l1sqlite.L1SourceKindAtom {
			sources = append(sources, entry)
		}
	}
	return sweepRegistrySources(ctx, store, sources, now, opts)
}

func sweepRegistrySources(ctx context.Context, store RegistryStore, sources []l1sqlite.L1SourceRegistryEntry, now time.Time, opts SweepOptions) (SweepResult, error) {
	trustScores, err := store.SourceTrustScores(ctx)
	if err != nil {
		return SweepResult{}, err
	}
	result := SweepResult{Sources: len(sources)}
	parser := gofeed.NewParser()
	for _, source := range sources {
		if source.Kind == l1sqlite.L1SourceKindWebGather {
			err = sweepWebGatherSource(ctx, store, source, now, &result)
		} else if source.Kind != l1sqlite.L1SourceKindRSS && source.Kind != l1sqlite.L1SourceKindAtom {
			err = sweepHTTPSource(ctx, store, source, trustScores, now, &result)
		} else {
			err = sweepFeedSource(ctx, store, parser, source, trustScores, now, opts, &result)
		}
		if err != nil {
			result.Failed++
			_ = store.MarkSourceRegistryFetched(ctx, source.SourceID, now, "error", err.Error())
			continue
		}
		if err := store.MarkSourceRegistryFetched(ctx, source.SourceID, now, "ok", ""); err != nil {
			return result, err
		}
	}
	return result, nil
}

func RunSource(ctx context.Context, store interface {
	RegistryStore
	RegistrySourceLister
}, sourceID string, now time.Time, opts SweepOptions) (SweepResult, error) {
	if store == nil {
		return SweepResult{}, fmt.Errorf("source registry store is nil")
	}
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return SweepResult{}, fmt.Errorf("source_id is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if opts.LimitPerSource <= 0 {
		opts.LimitPerSource = 10
	}
	entries, err := store.ListSourceRegistryEntries(ctx, false)
	if err != nil {
		return SweepResult{}, err
	}
	var selected *l1sqlite.L1SourceRegistryEntry
	for _, entry := range entries {
		if entry.SourceID == sourceID {
			cp := entry
			selected = &cp
			break
		}
	}
	if selected == nil {
		return SweepResult{}, fmt.Errorf("source registry entry not found: %s", sourceID)
	}
	result := SweepResult{Sources: 1}
	trustScores, err := store.SourceTrustScores(ctx)
	if err != nil {
		return result, err
	}
	parser := gofeed.NewParser()
	if selected.Kind == l1sqlite.L1SourceKindWebGather {
		err = sweepWebGatherSource(ctx, store, *selected, now, &result)
	} else if selected.Kind != l1sqlite.L1SourceKindRSS && selected.Kind != l1sqlite.L1SourceKindAtom {
		err = sweepHTTPSource(ctx, store, *selected, trustScores, now, &result)
	} else {
		err = sweepFeedSource(ctx, store, parser, *selected, trustScores, now, opts, &result)
	}
	if err != nil {
		result.Failed = 1
		_ = store.MarkSourceRegistryFetched(ctx, selected.SourceID, now, "error", err.Error())
		return result, err
	}
	if err := store.MarkSourceRegistryFetched(ctx, selected.SourceID, now, "ok", ""); err != nil {
		return result, err
	}
	return result, nil
}

func sweepWebGatherSource(ctx context.Context, store RegistryStore, source l1sqlite.L1SourceRegistryEntry, now time.Time, result *SweepResult) error {
	policy := sourceRegistryFetchPolicy(source.Meta)
	normalizedURL, err := modulewebgather.NormalizeURL(source.URL, policy.AllowLocalhost)
	if err != nil {
		return err
	}
	artifact, err := webgatherinfra.NewHTTPFetcher().Fetch(ctx, normalizedURL, policy)
	if err != nil {
		return err
	}
	extractorName := stringFromMeta(source.Meta, "extractor", modulewebgather.DefaultExtractor)
	doc, err := webgatherinfra.NewBasicExtractor().Extract(ctx, artifact, extractorName)
	if err != nil {
		return err
	}
	raw := strings.TrimSpace(doc.Text)
	if raw == "" {
		return fmt.Errorf("web gather extracted content is empty")
	}
	namespace := stringFromMeta(source.Meta, "namespace", "kb:web")
	category := stringFromMeta(source.Meta, "category", "web")
	domain := stringFromMeta(source.Meta, "domain", category)
	title := firstNonEmpty(stringFromMeta(source.Meta, "title", ""), doc.Title, source.SourceID)
	summary := firstNonEmpty(doc.Excerpt, modulewebgather.TextPreview(raw, 240), title)
	keywords := doc.Keywords
	if len(keywords) == 0 {
		keywords = []string{"web_gather", category}
	}
	meta := map[string]interface{}{
		"fetcher":                 "web_gather",
		"category":                category,
		"domain":                  domain,
		"namespace":               namespace,
		"title":                   title,
		"final_url":               artifact.FinalURL,
		"http_status":             artifact.StatusCode,
		"content_type":            artifact.ContentType,
		"fetch_provider":          "http",
		"extractor":               doc.Extractor,
		"requested_extractor":     extractorName,
		"raw_hash":                modulewebgather.SHA256Text(raw),
		"raw_bytes":               artifact.RawBytes,
		"extracted_chars":         len([]rune(raw)),
		"review_required":         true,
		"auto_promote":            false,
		"security_warning_source": "web_gather",
	}
	for k, v := range doc.Meta {
		if _, exists := meta[k]; !exists {
			meta[k] = v
		}
	}
	warnings := domainsecurity.DetectPromptInjectionWarnings(raw)
	if len(warnings) > 0 {
		meta["security_warnings"] = warnings
		result.Warnings += len(warnings)
	}
	staged, err := store.StageSourceRegistryFetch(ctx, source.SourceID, l1sqlite.L1SourceFetchPayload{
		SourceURL:    firstNonEmpty(doc.CanonicalURL, artifact.FinalURL, normalizedURL),
		FetchedAt:    firstNonZeroTime(artifact.FetchedAt, now),
		PublishedAt:  firstNonZeroTime(doc.PublishedAt, now),
		RawText:      raw,
		SummaryDraft: summary,
		Keywords:     nonEmpty(keywords...),
		Meta:         meta,
	})
	if err != nil {
		return err
	}
	if staged.ID == "" {
		return fmt.Errorf("web gather source staged empty item id")
	}
	result.Staged++
	return nil
}

func sweepHTTPSource(ctx context.Context, store RegistryStore, source l1sqlite.L1SourceRegistryEntry, trustScores map[string]float64, now time.Time, result *SweepResult) error {
	apiPlan := planSourceAPI(source)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiPlan.FetchURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if text := strings.TrimSpace(string(body)); text != "" {
			return fmt.Errorf("source fetch failed with status %d: %s", resp.StatusCode, text)
		}
		return fmt.Errorf("source fetch failed with status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return err
	}
	raw := strings.TrimSpace(string(body))
	if raw == "" {
		return fmt.Errorf("source response is empty")
	}
	namespace := stringFromMeta(source.Meta, "namespace", "kb:"+source.Kind)
	category := stringFromMeta(source.Meta, "category", source.Kind)
	domain := stringFromMeta(source.Meta, "domain", category)
	title := stringFromMeta(source.Meta, "title", source.SourceID)
	summary := title
	keywords := []string{category}
	meta := map[string]interface{}{
		"fetcher":   apiPlan.Fetcher,
		"category":  category,
		"domain":    domain,
		"namespace": namespace,
		"title":     title,
		"api_url":   apiPlan.FetchURL,
	}
	if source.Kind == l1sqlite.L1SourceKindPyPI {
		if parsed, ok := parsePyPIPayload(raw); ok {
			raw = parsed.RawText
			title = firstNonEmpty(stringFromMeta(source.Meta, "title", ""), parsed.Name, title)
			summary = firstNonEmpty(parsed.Summary, title)
			keywords = []string{"pypi", parsed.Name, parsed.LatestVersion}
			meta["fetcher"] = "source_registry_pypi"
			meta["title"] = title
			meta["package"] = parsed.Name
			meta["latest_version"] = parsed.LatestVersion
		}
	}
	meta, warnings := sourceRegistryMetaWithWarnings(meta, raw)
	result.Warnings += warnings
	staged, err := store.StageSourceRegistryFetch(ctx, source.SourceID, l1sqlite.L1SourceFetchPayload{
		SourceURL:    source.URL,
		FetchedAt:    now,
		PublishedAt:  now,
		RawText:      raw,
		SummaryDraft: summary,
		Keywords:     nonEmpty(keywords...),
		Meta:         meta,
	})
	if err != nil {
		return err
	}
	result.Staged++
	validation, err := store.ValidateStagingItem(ctx, staged.ID, l1sqlite.L1StagingValidationPolicy{
		SourceTrustScores: trustScores,
		MinimumTrustScore: 0.5,
		Now:               now,
	})
	if err != nil {
		return err
	}
	if !validation.Passed {
		return nil
	}
	result.Validated++
	if namespace == "kb:news" {
		if _, err := store.PromoteValidatedStagingItemToNews(ctx, staged.ID, category); err != nil {
			return err
		}
		result.PromotedNews++
		return nil
	}
	if _, err := store.PromoteValidatedStagingItemToKnowledge(ctx, staged.ID, domain); err != nil {
		return err
	}
	result.PromotedKnowledge++
	return nil
}

type pyPIPayload struct {
	Name          string
	Summary       string
	LatestVersion string
	RawText       string
}

func parsePyPIPayload(raw string) (pyPIPayload, bool) {
	var payload struct {
		Info struct {
			Name    string `json:"name"`
			Summary string `json:"summary"`
		} `json:"info"`
		Releases map[string]interface{} `json:"releases"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return pyPIPayload{}, false
	}
	name := strings.TrimSpace(payload.Info.Name)
	summary := strings.TrimSpace(payload.Info.Summary)
	if name == "" && summary == "" {
		return pyPIPayload{}, false
	}
	versions := make([]string, 0, len(payload.Releases))
	for version := range payload.Releases {
		if strings.TrimSpace(version) != "" {
			versions = append(versions, version)
		}
	}
	sort.Strings(versions)
	latest := ""
	if len(versions) > 0 {
		latest = versions[len(versions)-1]
	}
	parts := nonEmpty(name, summary)
	if latest != "" {
		parts = append(parts, "latest_version: "+latest)
	}
	return pyPIPayload{
		Name:          name,
		Summary:       summary,
		LatestVersion: latest,
		RawText:       strings.Join(parts, "\n"),
	}, true
}

func sweepFeedSource(ctx context.Context, store RegistryStore, parser *gofeed.Parser, source l1sqlite.L1SourceRegistryEntry, trustScores map[string]float64, now time.Time, opts SweepOptions, result *SweepResult) error {
	feed, err := parser.ParseURLWithContext(source.URL, ctx)
	if err != nil {
		return err
	}
	category := stringFromMeta(source.Meta, "category", "general")
	namespace := stringFromMeta(source.Meta, "namespace", "kb:news")
	existingNews := []l1sqlite.L1NewsItem{}
	if lister, ok := store.(RegistryNewsLister); ok {
		existingNews, err = lister.RecentNewsItems(ctx, category, 500)
		if err != nil {
			return err
		}
	}
	existingByArticle := existingFeedNewsByArticle(existingNews, source.SourceID)
	for _, existing := range existingNews {
		if existing.SourceID != source.SourceID || strings.TrimSpace(existing.SourceURL) == "" || !completeFeedNewsArticle(existing) {
			continue
		}
		if err := rememberExistingFeedArticle(ctx, store, existing, now); err != nil {
			return err
		}
	}
	attemptedArticles := map[string]struct{}{}
	limit := opts.LimitPerSource
	for i, item := range feed.Items {
		if i >= limit {
			break
		}
		title := readableFeedText(item.Title)
		description := readableFeedText(item.Description)
		content := readableFeedText(item.Content)
		publishedAt := now
		if item.PublishedParsed != nil {
			publishedAt = item.PublishedParsed.UTC()
		}
		articleKey := feedArticleKey(item.Link)
		existing, hasExisting := existingByArticle[articleKey]
		if hasExisting && completeFeedNewsArticle(existing) {
			result.SkippedExisting++
			continue
		}
		bodyParts := distinctNonEmpty(description, content)
		raw := strings.Join(distinctNonEmpty(append([]string{title}, bodyParts...)...), "\n")
		summary := firstNonEmpty(description, content, title)
		articleMeta := map[string]interface{}{
			"article_fetch_status": "not_attempted",
		}
		if strings.TrimSpace(item.Link) != "" {
			attemptedArticles[articleKey] = struct{}{}
			article, fetchErr := fetchFeedArticle(ctx, store, item.Link, source.Meta, now, opts.ArticleFallback)
			if fetchErr != nil {
				if errors.Is(fetchErr, errFeedArticleFetchDeferred) {
					result.ArticleDeferred++
					continue
				}
				articleMeta["article_fetch_status"] = "unavailable"
				articleMeta["article_fetch_error_code"] = webGatherErrorCode(fetchErr)
				if hasExisting {
					continue
				}
			} else if articleText := strings.TrimSpace(article.Document.Text); articleText != "" {
				if article.Reused {
					result.ArticleReused++
				} else {
					result.ArticleFetched++
				}
				raw = strings.Join(distinctNonEmpty(title, articleText), "\n")
				articleMeta["article_fetch_status"] = "ready"
				articleMeta["article_final_url"] = article.Artifact.FinalURL
				articleMeta["article_content_type"] = article.Artifact.ContentType
				articleMeta["article_fetch_provider"] = article.Artifact.ProviderName
				articleMeta["article_extractor"] = article.Document.Extractor
				articleMeta["article_raw_bytes"] = article.Artifact.RawBytes
				articleMeta["article_extracted_chars"] = len([]rune(articleText))
				for key, value := range articleFetchProvenance(article.Artifact, item.Link, articleText) {
					articleMeta[key] = value
				}
			}
		}
		if raw == "" {
			continue
		}
		baseMeta := map[string]interface{}{
			"fetcher":         "source_registry",
			"category":        category,
			"namespace":       namespace,
			"feed_item_title": title,
		}
		for key, value := range articleMeta {
			baseMeta[key] = value
		}
		meta, warnings := sourceRegistryMetaWithWarnings(baseMeta, raw)
		result.Warnings += warnings
		payload := l1sqlite.L1SourceFetchPayload{
			SourceURL:    firstNonEmpty(item.Link, source.URL),
			FetchedAt:    now,
			PublishedAt:  publishedAt,
			RawText:      raw,
			SummaryDraft: summary,
			Keywords:     []string{category},
			Meta:         meta,
		}
		if hasExisting {
			payload.EventID = stringFromMeta(existing.Meta, "event_id", "")
		}
		staged, err := store.StageSourceRegistryFetch(ctx, source.SourceID, payload)
		if err != nil {
			return err
		}
		result.Staged++
		stagingID := staged.ID
		if hasExisting && existing.StagingID != "" {
			stagingID = existing.StagingID
		}
		validation, err := store.ValidateStagingItem(ctx, stagingID, l1sqlite.L1StagingValidationPolicy{
			SourceTrustScores: trustScores,
			MinimumTrustScore: opts.MinimumTrustScore,
			Now:               now,
		})
		if err != nil {
			return err
		}
		if !validation.Passed {
			continue
		}
		result.Validated++
		if _, err := store.PromoteValidatedStagingItemToNews(ctx, stagingID, category); err != nil {
			return err
		}
		result.PromotedNews++
	}
	return backfillFeedArticles(ctx, store, source, existingNews, trustScores, now, opts, attemptedArticles, result)
}

func existingFeedNewsByArticle(news []l1sqlite.L1NewsItem, sourceID string) map[string]l1sqlite.L1NewsItem {
	out := map[string]l1sqlite.L1NewsItem{}
	for _, item := range news {
		if item.SourceID != sourceID || strings.TrimSpace(item.SourceURL) == "" {
			continue
		}
		key := feedArticleKey(item.SourceURL)
		current, exists := out[key]
		if !exists || (!completeFeedNewsArticle(current) && completeFeedNewsArticle(item)) {
			out[key] = item
		}
	}
	return out
}

func feedArticleKey(rawURL string) string {
	if normalized, err := modulewebgather.NormalizeURL(rawURL, true); err == nil {
		return normalized
	}
	return strings.TrimSpace(rawURL)
}

func rememberExistingFeedArticle(ctx context.Context, store RegistryStore, item l1sqlite.L1NewsItem, now time.Time) error {
	articleStore, ok := store.(RegistryArticleFetchStore)
	if !ok {
		return errors.New("news article fetch store is not configured")
	}
	_, claimed, err := articleStore.ClaimNewsArticleFetch(ctx, item.SourceURL, now)
	if err != nil || !claimed {
		return err
	}
	articleText := feedNewsArticleText(item)
	if articleText == "" {
		return articleStore.FailNewsArticleFetch(ctx, item.SourceURL, "existing_article_text_empty", now, newsArticleRetryDelay)
	}
	return articleStore.CompleteNewsArticleFetch(ctx, item.SourceURL, l1sqlite.L1NewsArticleFetchCompletion{
		FinalURL:      stringFromMeta(item.Meta, "article_final_url", item.SourceURL),
		FetchURL:      stringFromMeta(item.Meta, "article_fetch_url", item.SourceURL),
		ContentType:   stringFromMeta(item.Meta, "article_content_type", ""),
		FetchProvider: stringFromMeta(item.Meta, "article_fetch_provider", "http"),
		Extractor:     stringFromMeta(item.Meta, "article_extractor", modulewebgather.DefaultExtractor),
		RawBytes:      int64FromMeta(item.Meta, "article_raw_bytes", 0),
		ArticleText:   articleText,
		ContentSHA256: stringFromMeta(item.Meta, "article_content_sha256", modulewebgather.SHA256Text(articleText)),
	}, now)
}

const minimumCompleteArticleRunes = 200

func completeFeedNewsArticle(item l1sqlite.L1NewsItem) bool {
	return stringFromMeta(item.Meta, "article_fetch_status", "") == l1sqlite.L1NewsArticleFetchStatusReady &&
		completeExtractedArticle(feedNewsArticleText(item), stringFromMeta(item.Meta, "article_extractor", ""))
}

func completeExtractedArticle(articleText string, extractor string) bool {
	articleText = strings.TrimSpace(articleText)
	if articleText == "" || strings.HasSuffix(articleText, "…") || strings.HasSuffix(articleText, "...") {
		return false
	}
	if extractor == "nhk_news_article" {
		return true
	}
	return len([]rune(articleText)) >= minimumCompleteArticleRunes
}

func feedNewsArticleText(item l1sqlite.L1NewsItem) string {
	articleText := strings.TrimSpace(item.RawText)
	title := strings.TrimSpace(stringFromMeta(item.Meta, "feed_item_title", ""))
	if title != "" {
		articleText = strings.TrimSpace(strings.TrimPrefix(articleText, title+"\n"))
	}
	return articleText
}

func backfillFeedArticles(ctx context.Context, store RegistryStore, source l1sqlite.L1SourceRegistryEntry, existingNews []l1sqlite.L1NewsItem, trustScores map[string]float64, now time.Time, opts SweepOptions, attemptedArticles map[string]struct{}, result *SweepResult) error {
	remaining := opts.LimitPerSource
	for _, item := range existingNews {
		if remaining <= 0 {
			break
		}
		if item.SourceID != source.SourceID || completeFeedNewsArticle(item) || strings.TrimSpace(item.SourceURL) == "" {
			continue
		}
		articleKey := feedArticleKey(item.SourceURL)
		if _, attempted := attemptedArticles[articleKey]; attempted {
			continue
		}
		attemptedArticles[articleKey] = struct{}{}
		remaining--
		article, err := fetchFeedArticle(ctx, store, item.SourceURL, source.Meta, now, opts.ArticleFallback)
		if err != nil {
			if errors.Is(err, errFeedArticleFetchDeferred) {
				result.ArticleDeferred++
			}
			continue
		}
		if article.Reused {
			result.ArticleReused++
		} else {
			result.ArticleFetched++
		}
		articleText := strings.TrimSpace(article.Document.Text)
		if articleText == "" {
			continue
		}
		title := firstNonEmpty(stringFromMeta(item.Meta, "feed_item_title", ""), strings.SplitN(item.RawText, "\n", 2)[0])
		meta := map[string]interface{}{
			"fetcher":                 "source_registry",
			"category":                item.Category,
			"namespace":               stringFromMeta(source.Meta, "namespace", "kb:news"),
			"feed_item_title":         title,
			"article_fetch_status":    "ready",
			"article_final_url":       article.Artifact.FinalURL,
			"article_content_type":    article.Artifact.ContentType,
			"article_fetch_provider":  article.Artifact.ProviderName,
			"article_extractor":       article.Document.Extractor,
			"article_raw_bytes":       article.Artifact.RawBytes,
			"article_extracted_chars": len([]rune(articleText)),
		}
		for key, value := range articleFetchProvenance(article.Artifact, item.SourceURL, articleText) {
			meta[key] = value
		}
		raw := strings.Join(distinctNonEmpty(title, articleText), "\n")
		meta, warnings := sourceRegistryMetaWithWarnings(meta, raw)
		result.Warnings += warnings
		staged, err := store.StageSourceRegistryFetch(ctx, source.SourceID, l1sqlite.L1SourceFetchPayload{
			EventID:      stringFromMeta(item.Meta, "event_id", ""),
			SourceURL:    item.SourceURL,
			FetchedAt:    now,
			PublishedAt:  item.PublishedAt,
			RawText:      raw,
			SummaryDraft: item.SummaryDraft,
			Keywords:     item.Keywords,
			Meta:         meta,
		})
		if err != nil {
			return err
		}
		result.Staged++
		stagingID := item.StagingID
		if stagingID == "" {
			stagingID = staged.ID
		}
		validation, err := store.ValidateStagingItem(ctx, stagingID, l1sqlite.L1StagingValidationPolicy{SourceTrustScores: trustScores, MinimumTrustScore: opts.MinimumTrustScore, Now: now})
		if err != nil {
			return err
		}
		if !validation.Passed {
			continue
		}
		result.Validated++
		if _, err := store.PromoteValidatedStagingItemToNews(ctx, stagingID, item.Category); err != nil {
			return err
		}
		result.PromotedNews++
	}
	return nil
}

var errFeedArticleFetchDeferred = errors.New("news article fetch deferred")

type feedArticleFetchResult struct {
	Artifact modulewebgather.FetchArtifact
	Document modulewebgather.ExtractedDocument
	Reused   bool
}

const newsArticleRetryDelay = 5 * time.Minute

func fetchFeedArticle(ctx context.Context, store RegistryStore, rawURL string, sourceMeta map[string]interface{}, now time.Time, articleFallback modulewebgather.FetchProvider) (feedArticleFetchResult, error) {
	articleStore, ok := store.(RegistryArticleFetchStore)
	if !ok {
		return feedArticleFetchResult{}, errors.New("news article fetch store is not configured")
	}
	policy := sourceRegistryFetchPolicy(sourceMeta)
	normalizedURL, err := modulewebgather.NormalizeURL(rawURL, policy.AllowLocalhost)
	if err != nil {
		return feedArticleFetchResult{}, err
	}
	if err := articleStore.ReopenIncompleteNewsArticleFetch(ctx, normalizedURL, minimumCompleteArticleRunes, now); err != nil {
		return feedArticleFetchResult{}, err
	}
	entry, claimed, err := articleStore.ClaimNewsArticleFetch(ctx, normalizedURL, now)
	if err != nil {
		return feedArticleFetchResult{}, err
	}
	if !claimed {
		if entry != nil && entry.Status == l1sqlite.L1NewsArticleFetchStatusReady && strings.TrimSpace(entry.ArticleText) != "" {
			return feedArticleFetchResult{
				Artifact: modulewebgather.FetchArtifact{
					OriginalURL:  normalizedURL,
					FinalURL:     firstNonEmpty(entry.FinalURL, normalizedURL),
					ContentType:  entry.ContentType,
					RawBytes:     entry.RawBytes,
					FetchedAt:    entry.CompletedAt,
					ProviderName: entry.FetchProvider,
					Meta: map[string]any{
						"article_original_url":   entry.NormalizedURL,
						"article_fetch_url":      entry.FetchURL,
						"article_content_sha256": entry.ContentSHA256,
					},
				},
				Document: modulewebgather.ExtractedDocument{Text: entry.ArticleText, Extractor: entry.Extractor},
				Reused:   true,
			}, nil
		}
		return feedArticleFetchResult{}, errFeedArticleFetchDeferred
	}
	artifact, err := webgatherinfra.NewHTTPFetcher().Fetch(ctx, normalizedURL, policy)
	if err != nil && articleFallback != nil && webGatherErrorCode(err) == string(modulewebgather.ErrBlockedByPolicy) {
		artifact, err = articleFallback.Fetch(ctx, normalizedURL, policy)
	}
	if err != nil {
		_ = articleStore.FailNewsArticleFetch(ctx, normalizedURL, webGatherErrorCode(err), now, newsArticleRetryDelay)
		return feedArticleFetchResult{Artifact: artifact}, err
	}
	doc, err := webgatherinfra.NewBasicExtractor().Extract(ctx, artifact, modulewebgather.DefaultExtractor)
	if err != nil {
		_ = articleStore.FailNewsArticleFetch(ctx, normalizedURL, webGatherErrorCode(err), now, newsArticleRetryDelay)
		return feedArticleFetchResult{Artifact: artifact}, err
	}
	if strings.TrimSpace(doc.Text) == "" {
		err = modulewebgather.NewError(modulewebgather.ErrEmptyContent, "linked article extracted content is empty")
		_ = articleStore.FailNewsArticleFetch(ctx, normalizedURL, webGatherErrorCode(err), now, newsArticleRetryDelay)
		return feedArticleFetchResult{Artifact: artifact, Document: doc}, err
	}
	if !completeExtractedArticle(doc.Text, doc.Extractor) {
		err = modulewebgather.NewError(modulewebgather.ErrEmptyContent, "linked article extracted content is incomplete")
		_ = articleStore.FailNewsArticleFetch(ctx, normalizedURL, webGatherErrorCode(err), now, newsArticleRetryDelay)
		return feedArticleFetchResult{Artifact: artifact, Document: doc}, err
	}
	if err := articleStore.CompleteNewsArticleFetch(ctx, normalizedURL, l1sqlite.L1NewsArticleFetchCompletion{
		FinalURL: artifact.FinalURL, FetchURL: firstNonEmpty(stringFromAnyMeta(artifact.Meta, "article_fetch_url"), artifact.FinalURL),
		ContentType: artifact.ContentType, FetchProvider: artifact.ProviderName, Extractor: doc.Extractor,
		RawBytes: artifact.RawBytes, ArticleText: doc.Text,
		ContentSHA256: firstNonEmpty(stringFromAnyMeta(artifact.Meta, "article_content_sha256"), modulewebgather.SHA256Text(doc.Text)),
	}, now); err != nil {
		return feedArticleFetchResult{Artifact: artifact, Document: doc}, err
	}
	return feedArticleFetchResult{Artifact: artifact, Document: doc}, nil
}

func articleFetchProvenance(artifact modulewebgather.FetchArtifact, originalURL string, articleText string) map[string]interface{} {
	meta := map[string]interface{}{
		"article_original_url":   firstNonEmpty(stringFromAnyMeta(artifact.Meta, "article_original_url"), originalURL),
		"article_content_sha256": firstNonEmpty(stringFromAnyMeta(artifact.Meta, "article_content_sha256"), modulewebgather.SHA256Text(articleText)),
	}
	if fetchURL := stringFromAnyMeta(artifact.Meta, "article_fetch_url"); fetchURL != "" {
		meta["article_fetch_url"] = fetchURL
	}
	if !artifact.FetchedAt.IsZero() {
		meta["article_fetched_at"] = artifact.FetchedAt.UTC().Format(time.RFC3339Nano)
	}
	return meta
}

func stringFromAnyMeta(meta map[string]any, key string) string {
	if meta == nil {
		return ""
	}
	value, ok := meta[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func sourceRegistryFetchPolicy(meta map[string]interface{}) modulewebgather.FetchPolicy {
	policy := modulewebgather.DefaultFetchPolicy()
	if boolFromMeta(meta, "allow_localhost", false) {
		policy.AllowLocalhost = true
	}
	if n := int64FromMeta(meta, "request_timeout_ms", 0); n > 0 {
		policy.RequestTimeout = time.Duration(n) * time.Millisecond
	}
	if n := int64FromMeta(meta, "max_body_bytes", 0); n > 0 {
		policy.MaxBodyBytes = n
	}
	if n := int64FromMeta(meta, "max_redirects", -1); n >= 0 {
		policy.MaxRedirects = int(n)
	}
	return policy
}

func webGatherErrorCode(err error) string {
	var gatherErr *modulewebgather.Error
	if errors.As(err, &gatherErr) && gatherErr.Code != "" {
		return string(gatherErr.Code)
	}
	return string(modulewebgather.ErrFetchFailed)
}

func nonEmpty(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, strings.TrimSpace(value))
		}
	}
	return out
}

func distinctNonEmpty(values ...string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func readableFeedText(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	decoder := xhtml.NewTokenizer(strings.NewReader(raw))
	parts := make([]string, 0, 8)
	skipDepth := 0
	for {
		switch decoder.Next() {
		case xhtml.ErrorToken:
			return strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
		case xhtml.StartTagToken:
			name, _ := decoder.TagName()
			if string(name) == "script" || string(name) == "style" {
				skipDepth++
			}
		case xhtml.EndTagToken:
			name, _ := decoder.TagName()
			if (string(name) == "script" || string(name) == "style") && skipDepth > 0 {
				skipDepth--
			}
		case xhtml.TextToken:
			if skipDepth > 0 {
				continue
			}
			text := strings.TrimSpace(string(decoder.Text()))
			if text != "" {
				parts = append(parts, text)
			}
		}
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func stringFromMeta(meta map[string]interface{}, key string, def string) string {
	if meta == nil {
		return def
	}
	if value, ok := meta[key].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return def
}

func boolFromMeta(meta map[string]interface{}, key string, def bool) bool {
	if meta == nil {
		return def
	}
	if value, ok := meta[key].(bool); ok {
		return value
	}
	if value, ok := meta[key].(string); ok {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "true", "1", "yes", "on":
			return true
		case "false", "0", "no", "off":
			return false
		}
	}
	return def
}

func int64FromMeta(meta map[string]interface{}, key string, def int64) int64 {
	if meta == nil {
		return def
	}
	switch value := meta[key].(type) {
	case int:
		return int64(value)
	case int64:
		return value
	case float64:
		return int64(value)
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err == nil {
			return parsed
		}
	}
	return def
}

func firstNonZeroTime(values ...time.Time) time.Time {
	for _, value := range values {
		if !value.IsZero() {
			return value.UTC()
		}
	}
	return time.Time{}
}

func sourceRegistryMetaWithWarnings(meta map[string]interface{}, raw string) (map[string]interface{}, int) {
	warnings := domainsecurity.DetectPromptInjectionWarnings(raw)
	if len(warnings) == 0 {
		return meta, 0
	}
	if meta == nil {
		meta = map[string]interface{}{}
	}
	meta["security_warnings"] = warnings
	meta["security_warning_source"] = "source_registry"
	return meta, len(warnings)
}
