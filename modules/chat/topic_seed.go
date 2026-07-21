package chat

import (
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"regexp"
	"strings"
	"time"
)

type DailySeedCache struct {
	Date               string     `json:"date"`
	WikipediaSeeds     []string   `json:"wikipedia_seeds"`
	NewsSeeds          []string   `json:"news_seeds"`
	NewsSeedItems      []NewsSeed `json:"news_seed_items"`
	FetchedAt          time.Time  `json:"fetched_at"`
	EnrichmentStatus   string     `json:"enrichment_status,omitempty"`
	EnrichmentProvider string     `json:"enrichment_provider,omitempty"`
	EnrichmentError    string     `json:"enrichment_error,omitempty"`
	EnrichedAt         time.Time  `json:"enriched_at,omitempty"`
}

var newsSeedHTMLTagPattern = regexp.MustCompile(`(?s)<[^>]*>`)

type NewsSeedSource struct {
	Category    string
	Name        string
	URL         string
	Limit       int
	ErrorPrefix string
}

func SelectExternalTopicSeed(cache *DailySeedCache, wikipediaIndex int, genre string) (TopicSeed, bool) {
	if cache == nil || len(cache.WikipediaSeeds) == 0 {
		return TopicSeed{Category: TopicCategoryExternal}, false
	}
	title := selectStringByIndex(cache.WikipediaSeeds, wikipediaIndex)
	if strings.TrimSpace(title) == "" {
		return TopicSeed{Category: TopicCategoryExternal}, false
	}
	return TopicSeed{
		Category: TopicCategoryExternal,
		Genre1:   strings.TrimSpace(genre),
		ExternalMaterial: &ExternalMaterialSeed{
			Title:    strings.TrimSpace(title),
			Provider: "Wikipedia",
			Category: "wikipedia_random",
		},
	}, true
}

func SelectNewsTopicSeed(cache *DailySeedCache, newsIndex int) (TopicSeed, bool) {
	if cache == nil || (len(cache.NewsSeedItems) == 0 && len(cache.NewsSeeds) == 0) {
		return TopicSeed{Category: TopicCategoryNews}, false
	}
	if len(cache.NewsSeedItems) > 0 {
		seed := selectNewsSeedByIndex(cache.NewsSeedItems, newsIndex)
		if strings.TrimSpace(seed.Title) == "" {
			return TopicSeed{Category: TopicCategoryNews}, false
		}
		return TopicSeed{Category: TopicCategoryNews, News: &seed}, true
	}
	title := selectStringByIndex(cache.NewsSeeds, newsIndex)
	if strings.TrimSpace(title) == "" {
		return TopicSeed{Category: TopicCategoryNews}, false
	}
	seed := NewsSeed{Title: strings.TrimSpace(title)}
	return TopicSeed{Category: TopicCategoryNews, News: &seed}, true
}

func NewsSeedSourceLabel(seed NewsSeed) string {
	title := strings.TrimSpace(seed.Title)
	category := strings.TrimSpace(seed.Category)
	source := strings.TrimSpace(seed.Source)
	if category == "" || source == "" {
		return "News:" + title
	}
	return fmt.Sprintf("News:%s:%s:%s", category, source, title)
}

func NewsSeedTitles(seeds []NewsSeed) []string {
	titles := make([]string, 0, len(seeds))
	for _, seed := range seeds {
		title := strings.TrimSpace(seed.Title)
		if title != "" {
			titles = append(titles, title)
		}
	}
	return titles
}

func NewsSeedCategorySummary(seeds []NewsSeed) string {
	if len(seeds) == 0 {
		return "none"
	}
	counts := make(map[string]int)
	var order []string
	for _, seed := range seeds {
		category := strings.TrimSpace(seed.Category)
		if category == "" {
			category = "unknown"
		}
		if _, ok := counts[category]; !ok {
			order = append(order, category)
		}
		counts[category]++
	}
	parts := make([]string, 0, len(order))
	for _, category := range order {
		parts = append(parts, fmt.Sprintf("%s=%d", category, counts[category]))
	}
	return strings.Join(parts, ",")
}

func ParseNewsSeeds(reader io.Reader, source NewsSeedSource, limit int) ([]NewsSeed, error) {
	var feed struct {
		Items []struct {
			Title       string `xml:"title"`
			Link        string `xml:"link"`
			Description string `xml:"description"`
			Content     string `xml:"encoded"`
		} `xml:"channel>item"`
		Entries []struct {
			Title   string `xml:"title"`
			Summary string `xml:"summary"`
			Content string `xml:"content"`
			Links   []struct {
				Href string `xml:"href,attr"`
				Rel  string `xml:"rel,attr"`
			} `xml:"link"`
		} `xml:"entry"`
	}
	if err := xml.NewDecoder(reader).Decode(&feed); err != nil {
		return nil, err
	}

	seeds := make([]NewsSeed, 0, limit)
	for _, item := range feed.Items {
		title := strings.TrimSpace(item.Title)
		if title == "" {
			continue
		}
		seeds = append(seeds, NewsSeed{
			Title:      title,
			Category:   strings.TrimSpace(source.Category),
			Source:     strings.TrimSpace(source.Name),
			SourceType: "rss",
			URL:        strings.TrimSpace(item.Link),
			Summary:    normalizeNewsSeedSummary(firstNewsSeedText(item.Description, item.Content)),
		})
		if limit > 0 && len(seeds) >= limit {
			return seeds, nil
		}
	}
	for _, entry := range feed.Entries {
		title := strings.TrimSpace(entry.Title)
		if title == "" {
			continue
		}
		link := ""
		for _, candidate := range entry.Links {
			rel := strings.TrimSpace(candidate.Rel)
			if rel == "" || rel == "alternate" {
				link = strings.TrimSpace(candidate.Href)
				if link != "" {
					break
				}
			}
		}
		seeds = append(seeds, NewsSeed{
			Title:      title,
			Category:   strings.TrimSpace(source.Category),
			Source:     strings.TrimSpace(source.Name),
			SourceType: "atom",
			URL:        link,
			Summary:    normalizeNewsSeedSummary(firstNewsSeedText(entry.Summary, entry.Content)),
		})
		if limit > 0 && len(seeds) >= limit {
			break
		}
	}
	return seeds, nil
}

func firstNewsSeedText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func normalizeNewsSeedSummary(value string) string {
	value = newsSeedHTMLTagPattern.ReplaceAllString(value, " ")
	value = html.UnescapeString(value)
	value = strings.Join(strings.Fields(value), " ")
	const maxRunes = 800
	runes := []rune(value)
	if len(runes) > maxRunes {
		value = string(runes[:maxRunes]) + "…"
	}
	return value
}

func RecentTopicRecords(topics []string) []RecentTopic {
	out := make([]RecentTopic, 0, len(topics))
	for _, topic := range topics {
		topic = strings.TrimSpace(topic)
		if topic != "" {
			out = append(out, RecentTopic{Topic: topic})
		}
	}
	return out
}

func selectStringByIndex(items []string, index int) string {
	if len(items) == 0 {
		return ""
	}
	if index < 0 {
		index = -index
	}
	return items[index%len(items)]
}

func selectNewsSeedByIndex(items []NewsSeed, index int) NewsSeed {
	if len(items) == 0 {
		return NewsSeed{}
	}
	if index < 0 {
		index = -index
	}
	return items[index%len(items)]
}
