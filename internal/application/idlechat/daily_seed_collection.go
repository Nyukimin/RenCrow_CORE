package idlechat

import (
	"fmt"
	"strings"
	"time"

	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
)

var dailySeedCollectionTools = []string{
	"go_http",
	"rss",
	"atom",
	"wikipedia_api",
	"reddit_atom",
	"x_recent_search",
	"web_gather.fetch",
	"web_search（不明語のみ）",
	"shiro_worker_llm",
}

// DailySeedCollectionItem is one read-only news item exposed to the Debug Viewer.
type DailySeedCollectionItem struct {
	Title            string                    `json:"title"`
	Category         string                    `json:"category,omitempty"`
	Source           string                    `json:"source,omitempty"`
	SourceType       string                    `json:"source_type,omitempty"`
	URL              string                    `json:"url,omitempty"`
	SourceReadStatus string                    `json:"source_read_status,omitempty"`
	SourceReadURL    string                    `json:"source_read_url,omitempty"`
	TranslatedBody   string                    `json:"translated_body"`
	Summary          string                    `json:"summary"`
	Perspective      string                    `json:"perspective"`
	TermNotes        []modulechat.NewsTermNote `json:"term_notes"`
}

// DailySeedCollectionSource describes a configured collection target without secrets.
type DailySeedCollectionSource struct {
	Name     string `json:"name"`
	Category string `json:"category"`
	Kind     string `json:"kind"`
	URL      string `json:"url,omitempty"`
	Query    string `json:"query,omitempty"`
	Limit    int    `json:"limit,omitempty"`
	Enabled  bool   `json:"enabled"`
}

// DailySeedCollectionSnapshot is a detached observation snapshot of the in-process
// IdleChat daily seed cache. Reading it never starts collection or mutates the cache.
type DailySeedCollectionSnapshot struct {
	Status             string                      `json:"status"`
	SkillID            string                      `json:"skill_id"`
	Schedule           string                      `json:"schedule"`
	Timezone           string                      `json:"timezone"`
	FetchedAt          *time.Time                  `json:"fetched_at,omitempty"`
	EnrichmentStatus   string                      `json:"enrichment_status,omitempty"`
	EnrichmentProvider string                      `json:"enrichment_provider,omitempty"`
	EnrichmentError    string                      `json:"enrichment_error,omitempty"`
	EnrichedAt         *time.Time                  `json:"enriched_at,omitempty"`
	NextRunAt          time.Time                   `json:"next_run_at"`
	Total              int                         `json:"total"`
	WikipediaCount     int                         `json:"wikipedia_count"`
	CategoryCounts     map[string]int              `json:"category_counts"`
	SourceCounts       map[string]int              `json:"source_counts"`
	Items              []DailySeedCollectionItem   `json:"items"`
	Sources            []DailySeedCollectionSource `json:"sources"`
	Tools              []string                    `json:"tools"`
}

// DailySeedCollectionSnapshot returns the current collection data and configured
// source catalog. Credentials are intentionally omitted.
func (o *IdleChatOrchestrator) DailySeedCollectionSnapshot(now time.Time) DailySeedCollectionSnapshot {
	snapshot := DailySeedCollectionSnapshot{
		Status:         "empty",
		SkillID:        dailySourceBriefSkillID,
		Schedule:       "04:00",
		Timezone:       "JST",
		NextRunAt:      nextDailySeedRefreshAt(now),
		CategoryCounts: make(map[string]int),
		SourceCounts:   make(map[string]int),
		Items:          []DailySeedCollectionItem{},
		Tools:          append([]string(nil), dailySeedCollectionTools...),
	}

	var sourceConfig NewsSourceConfig
	if o != nil {
		o.mu.Lock()
		sourceConfig = o.newsSourceConfig
		sourceConfig.RedditCommunities = append([]string(nil), sourceConfig.RedditCommunities...)
		sourceConfig.XQueries = append([]XNewsQuery(nil), sourceConfig.XQueries...)
		o.mu.Unlock()
	}
	snapshot.Sources = dailySeedCollectionSources(sourceConfig)

	cacheMu.RLock()
	if dailyCache != nil {
		fetchedAt := dailyCache.FetchedAt
		snapshot.FetchedAt = &fetchedAt
		snapshot.EnrichmentStatus = strings.TrimSpace(dailyCache.EnrichmentStatus)
		snapshot.EnrichmentProvider = strings.TrimSpace(dailyCache.EnrichmentProvider)
		snapshot.EnrichmentError = strings.TrimSpace(dailyCache.EnrichmentError)
		if !dailyCache.EnrichedAt.IsZero() {
			enrichedAt := dailyCache.EnrichedAt
			snapshot.EnrichedAt = &enrichedAt
		}
		snapshot.WikipediaCount = len(dailyCache.WikipediaSeeds)
		snapshot.Items = make([]DailySeedCollectionItem, 0, len(dailyCache.NewsSeedItems))
		for _, item := range dailyCache.NewsSeedItems {
			category := fallbackCollectionValue(item.Category, "unknown")
			source := fallbackCollectionValue(item.Source, "unknown")
			snapshot.Items = append(snapshot.Items, DailySeedCollectionItem{
				Title:            strings.TrimSpace(item.Title),
				Category:         category,
				Source:           source,
				SourceType:       strings.TrimSpace(item.SourceType),
				URL:              strings.TrimSpace(item.URL),
				SourceReadStatus: strings.TrimSpace(item.SourceReadStatus),
				SourceReadURL:    strings.TrimSpace(item.SourceReadURL),
				TranslatedBody:   strings.TrimSpace(item.TranslatedBody),
				Summary:          strings.TrimSpace(item.Summary),
				Perspective:      strings.TrimSpace(item.Perspective),
				TermNotes:        append([]modulechat.NewsTermNote(nil), item.TermNotes...),
			})
			snapshot.CategoryCounts[category]++
			snapshot.SourceCounts[source]++
		}
	}
	cacheMu.RUnlock()

	snapshot.Total = len(snapshot.Items)
	if snapshot.FetchedAt != nil {
		snapshot.Status = "ready"
	}
	return snapshot
}

func dailySeedCollectionSources(cfg NewsSourceConfig) []DailySeedCollectionSource {
	sources := make([]DailySeedCollectionSource, 0, 1+len(defaultNewsSeedSources)+len(cfg.RedditCommunities)+len(cfg.XQueries))
	sources = append(sources, DailySeedCollectionSource{
		Name:     "Japanese Wikipedia Random",
		Category: "wikipedia_random",
		Kind:     "wikipedia_api",
		URL:      "https://ja.wikipedia.org/w/api.php?action=query&list=random",
		Limit:    10,
		Enabled:  true,
	})
	for _, source := range defaultNewsSeedSources {
		sources = append(sources, DailySeedCollectionSource{
			Name:     strings.TrimSpace(source.Name),
			Category: strings.TrimSpace(source.Category),
			Kind:     "rss_or_atom",
			URL:      strings.TrimSpace(source.URL),
			Limit:    source.Limit,
			Enabled:  true,
		})
	}
	for _, community := range cfg.RedditCommunities {
		community = strings.TrimSpace(strings.TrimPrefix(community, "r/"))
		if community == "" {
			continue
		}
		sources = append(sources, DailySeedCollectionSource{
			Name:     "Reddit r/" + community,
			Category: "social",
			Kind:     "reddit_atom",
			URL:      fmt.Sprintf("%s/r/%s/.rss", defaultRedditEndpoint, community),
			Limit:    cfg.RedditLimit,
			Enabled:  cfg.RedditEnabled,
		})
	}
	for _, query := range cfg.XQueries {
		name := fallbackCollectionValue(query.Name, "X Recent Search")
		sources = append(sources, DailySeedCollectionSource{
			Name:     name,
			Category: fallbackCollectionValue(query.Category, "social"),
			Kind:     "x_recent_search",
			URL:      defaultXRecentEndpoint,
			Query:    strings.TrimSpace(query.Query),
			Limit:    query.Limit,
			Enabled:  cfg.XEnabled,
		})
	}
	return sources
}

func fallbackCollectionValue(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}
