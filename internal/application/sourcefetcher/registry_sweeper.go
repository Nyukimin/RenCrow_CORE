package sourcefetcher

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	domainsecurity "github.com/Nyukimin/RenCrow_CORE/internal/domain/security"
	webgatherinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/webgather"
	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
	"github.com/mmcdole/gofeed"
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

type RegistrySourceLister interface {
	ListSourceRegistryEntries(ctx context.Context, enabledOnly bool) ([]l1sqlite.L1SourceRegistryEntry, error)
}

type SweepOptions struct {
	LimitPerSource    int
	MinimumTrustScore float64
}

type SweepResult struct {
	Sources           int
	Staged            int
	Warnings          int
	Validated         int
	PromotedNews      int
	PromotedKnowledge int
	Failed            int
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
	policy := modulewebgather.DefaultFetchPolicy()
	if boolFromMeta(source.Meta, "allow_localhost", false) {
		policy.AllowLocalhost = true
	}
	if n := int64FromMeta(source.Meta, "request_timeout_ms", 0); n > 0 {
		policy.RequestTimeout = time.Duration(n) * time.Millisecond
	}
	if n := int64FromMeta(source.Meta, "max_body_bytes", 0); n > 0 {
		policy.MaxBodyBytes = n
	}
	if n := int64FromMeta(source.Meta, "max_redirects", -1); n >= 0 {
		policy.MaxRedirects = int(n)
	}
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
	limit := opts.LimitPerSource
	for i, item := range feed.Items {
		if i >= limit {
			break
		}
		raw := strings.TrimSpace(strings.Join(nonEmpty(item.Title, item.Description, item.Content), "\n"))
		if raw == "" {
			continue
		}
		publishedAt := now
		if item.PublishedParsed != nil {
			publishedAt = item.PublishedParsed.UTC()
		}
		meta, warnings := sourceRegistryMetaWithWarnings(map[string]interface{}{
			"fetcher":   "source_registry",
			"category":  category,
			"namespace": namespace,
		}, raw)
		result.Warnings += warnings
		staged, err := store.StageSourceRegistryFetch(ctx, source.SourceID, l1sqlite.L1SourceFetchPayload{
			SourceURL:    firstNonEmpty(item.Link, source.URL),
			FetchedAt:    now,
			PublishedAt:  publishedAt,
			RawText:      raw,
			SummaryDraft: strings.TrimSpace(item.Title),
			Keywords:     []string{category},
			Meta:         meta,
		})
		if err != nil {
			return err
		}
		result.Staged++
		validation, err := store.ValidateStagingItem(ctx, staged.ID, l1sqlite.L1StagingValidationPolicy{
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
		if _, err := store.PromoteValidatedStagingItemToNews(ctx, staged.ID, category); err != nil {
			return err
		}
		result.PromotedNews++
	}
	return nil
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
