package newsbrief

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
)

// DefaultSource is one canonical daily-news input shared by the scheduled
// IdleChat cache and the persistent L1 news database.
type DefaultSource struct {
	SourceID  string
	Kind      string
	Category  string
	Name      string
	URL       string
	ItemLimit int
	Trust     float64
}

var canonicalDefaultSources = []DefaultSource{
	{SourceID: "rss:news:openai", Kind: l1sqlite.L1SourceKindRSS, Category: "ai_frontier", Name: "OpenAI News", URL: "https://openai.com/news/rss.xml", ItemLimit: 4, Trust: 0.9},
	{SourceID: "rss:news:deepmind", Kind: l1sqlite.L1SourceKindRSS, Category: "ai_frontier", Name: "Google DeepMind", URL: "https://deepmind.google/blog/rss.xml", ItemLimit: 4, Trust: 0.9},
	{SourceID: "rss:news:hugging-face", Kind: l1sqlite.L1SourceKindRSS, Category: "ai_open_source", Name: "Hugging Face Blog", URL: "https://huggingface.co/blog/feed.xml", ItemLimit: 5, Trust: 0.9},
	{SourceID: "rss:news:microsoft-research", Kind: l1sqlite.L1SourceKindRSS, Category: "ai_research", Name: "Microsoft Research", URL: "https://www.microsoft.com/en-us/research/feed/", ItemLimit: 4, Trust: 0.9},
	{SourceID: "rss:news:google-research", Kind: l1sqlite.L1SourceKindRSS, Category: "ai_research", Name: "Google Research", URL: "https://research.google/blog/rss/", ItemLimit: 4, Trust: 0.9},
	{SourceID: "rss:news:nvidia-generative-ai", Kind: l1sqlite.L1SourceKindRSS, Category: "ai_infrastructure", Name: "NVIDIA Generative AI", URL: "https://blogs.nvidia.com/blog/category/generative-ai/feed/", ItemLimit: 4, Trust: 0.9},
	{SourceID: "atom:news:arxiv-ai", Kind: l1sqlite.L1SourceKindAtom, Category: "ai_research", Name: "arXiv AI Research", URL: "https://export.arxiv.org/api/query?search_query=cat%3Acs.AI%20OR%20cat%3Acs.LG%20OR%20cat%3Acs.CL%20OR%20cat%3Acs.CV%20OR%20cat%3Acs.RO&start=0&max_results=8&sortBy=submittedDate&sortOrder=descending", ItemLimit: 8, Trust: 0.9},
	{SourceID: "rss:news:nhk-top", Kind: l1sqlite.L1SourceKindRSS, Category: "general", Name: "NHK Top", URL: "https://www.nhk.or.jp/rss/news/cat0.xml", ItemLimit: 4, Trust: 0.9},
	{SourceID: "rss:news:nhk-science-culture", Kind: l1sqlite.L1SourceKindRSS, Category: "culture", Name: "NHK Science/Culture", URL: "https://www.nhk.or.jp/rss/news/cat3.xml", ItemLimit: 3, Trust: 0.9},
	{SourceID: "rss:news:nhk-business", Kind: l1sqlite.L1SourceKindRSS, Category: "business", Name: "NHK Business", URL: "https://www.nhk.or.jp/rss/news/cat5.xml", ItemLimit: 3, Trust: 0.9},
	{SourceID: "rss:news:nhk-world", Kind: l1sqlite.L1SourceKindRSS, Category: "world", Name: "NHK World", URL: "https://www.nhk.or.jp/rss/news/cat6.xml", ItemLimit: 3, Trust: 0.9},
	{SourceID: "rss:news:nhk-sports", Kind: l1sqlite.L1SourceKindRSS, Category: "sports", Name: "NHK Sports", URL: "https://www.nhk.or.jp/rss/news/cat7.xml", ItemLimit: 3, Trust: 0.9},
	{SourceID: "rss:news:itmedia-technology", Kind: l1sqlite.L1SourceKindRSS, Category: "tech", Name: "ITmedia NEWS Technology", URL: "https://rss.itmedia.co.jp/rss/2.0/news_technology.xml", ItemLimit: 4, Trust: 0.85},
	{SourceID: "rss:news:itmedia-business", Kind: l1sqlite.L1SourceKindRSS, Category: "business", Name: "ITmedia Business", URL: "https://rss.itmedia.co.jp/rss/2.0/business.xml", ItemLimit: 3, Trust: 0.85},
}

func DefaultSources() []DefaultSource {
	return append([]DefaultSource(nil), canonicalDefaultSources...)
}

func DefaultNewsSeedSources() []modulechat.NewsSeedSource {
	sources := make([]modulechat.NewsSeedSource, 0, len(canonicalDefaultSources))
	for _, source := range canonicalDefaultSources {
		sources = append(sources, modulechat.NewsSeedSource{
			Category: source.Category,
			Name:     source.Name,
			URL:      source.URL,
			Limit:    source.ItemLimit,
		})
	}
	return sources
}

type DefaultSourceRegistry interface {
	ListSourceRegistryEntries(ctx context.Context, enabledOnly bool) ([]l1sqlite.L1SourceRegistryEntry, error)
	SaveSourceRegistryEntry(ctx context.Context, entry l1sqlite.L1SourceRegistryEntry) (*l1sqlite.L1SourceRegistryEntry, error)
}

type BootstrapResult struct {
	Existing int
	Added    int
	Failed   int
}

// BootstrapDefaultSources adds only missing canonical sources. Matching by URL
// as well as source ID preserves renamed, customized, and user-disabled rows.
func BootstrapDefaultSources(ctx context.Context, store DefaultSourceRegistry) (BootstrapResult, error) {
	if store == nil {
		return BootstrapResult{}, fmt.Errorf("default news source registry is unavailable")
	}
	existing, err := store.ListSourceRegistryEntries(ctx, false)
	if err != nil {
		return BootstrapResult{}, fmt.Errorf("list default news sources: %w", err)
	}
	ids := make(map[string]struct{}, len(existing))
	urls := make(map[string]struct{}, len(existing))
	for _, entry := range existing {
		ids[strings.TrimSpace(entry.SourceID)] = struct{}{}
		urls[strings.TrimSpace(entry.URL)] = struct{}{}
	}
	result := BootstrapResult{}
	var failures []error
	for _, source := range canonicalDefaultSources {
		_, idExists := ids[source.SourceID]
		_, urlExists := urls[source.URL]
		if idExists || urlExists {
			result.Existing++
			continue
		}
		_, err := store.SaveSourceRegistryEntry(ctx, l1sqlite.L1SourceRegistryEntry{
			SourceID:      source.SourceID,
			URL:           source.URL,
			Kind:          source.Kind,
			TrustScore:    source.Trust,
			FetchInterval: 24 * time.Hour,
			LicenseNote:   "RSS/Atom metadata; review publisher terms before reuse",
			Enabled:       source.SourceID != "rss:news:nhk-sports",
			Meta: map[string]interface{}{
				"namespace":    "kb:news",
				"category":     source.Category,
				"source_name":  source.Name,
				"source_limit": source.ItemLimit,
				"managed_by":   "rencrow_default_news",
			},
		})
		if err != nil {
			result.Failed++
			failures = append(failures, fmt.Errorf("%s: %w", source.SourceID, err))
			continue
		}
		result.Added++
		ids[source.SourceID] = struct{}{}
		urls[source.URL] = struct{}{}
	}
	return result, errors.Join(failures...)
}
