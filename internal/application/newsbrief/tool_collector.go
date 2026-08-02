package newsbrief

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainnews "github.com/Nyukimin/RenCrow_CORE/internal/domain/newsbrief"
	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
)

const (
	liveNewsSearchAndFetchTool = "web_gather.search_and_fetch"
	liveNewsSearchTool         = "web_search"
)

// ToolCollector performs foreground news collection through the Worker tool
// runner. It returns source-labelled data; it does not generate Agent speech.
type ToolCollector struct {
	runner   domainagent.ToolRunner
	provider string
}

func NewToolCollector(runner domainagent.ToolRunner) *ToolCollector {
	return NewToolCollectorWithProvider(runner, "")
}

// NewToolCollectorWithProvider selects an explicitly configured WebGather
// provider. An empty provider preserves the local-cache/default behavior.
func NewToolCollectorWithProvider(runner domainagent.ToolRunner, provider string) *ToolCollector {
	if runner == nil {
		return nil
	}
	return &ToolCollector{runner: runner, provider: strings.TrimSpace(provider)}
}

func (c *ToolCollector) Collect(ctx context.Context, query string, now time.Time) (domainnews.DailyNewsBrief, error) {
	if c == nil || c.runner == nil {
		return domainnews.DailyNewsBrief{}, fmt.Errorf("news collector worker tool runner is unavailable")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		query = "今日のニュース 日本"
	}

	toolName := liveNewsSearchTool
	if c.hasTool(ctx, liveNewsSearchAndFetchTool) {
		toolName = liveNewsSearchAndFetchTool
	}
	args := map[string]any{
		"query":         query,
		"limit":         5,
		"max_fetches":   5,
		"language":      "ja",
		"freshness":     "day",
		"namespace":     "news:live",
		"store_staging": false,
	}
	if c.provider != "" && toolName == liveNewsSearchAndFetchTool {
		args["provider"] = c.provider
	}
	resp, err := c.runner.ExecuteV2(ctx, toolName, args)
	if err != nil {
		return domainnews.DailyNewsBrief{}, fmt.Errorf("%s failed: %w", toolName, err)
	}
	if resp == nil {
		return domainnews.DailyNewsBrief{}, fmt.Errorf("%s returned no response", toolName)
	}
	if resp.IsError() {
		return domainnews.DailyNewsBrief{}, fmt.Errorf("%s failed: %s", toolName, resp.Error.Message)
	}

	items := make([]domainnews.Item, 0, 5)
	if toolName == liveNewsSearchAndFetchTool {
		items = itemsFromSearchAndFetch(resp.Result)
	}
	if len(items) == 0 {
		items = itemsFromSearchMetadata(resp.Metadata)
	}
	if len(items) == 0 {
		return domainnews.DailyNewsBrief{}, fmt.Errorf("%s returned no news items", toolName)
	}

	status := domainnews.StatusReady
	enrichment := domainnews.EnrichmentReady
	for _, item := range items {
		if item.SourceReadStatus != "fetched" {
			status = domainnews.StatusPartial
			enrichment = domainnews.EnrichmentPartial
			break
		}
	}
	return domainnews.DailyNewsBrief{
		Date:               domainnews.ExpectedMorningDate(now),
		Slot:               domainnews.SlotMorning,
		Timezone:           domainnews.TimezoneJST,
		Source:             domainnews.SourceLiveSearch,
		SkillID:            "live_news_search",
		Status:             status,
		EnrichmentStatus:   enrichment,
		EnrichmentProvider: "news_collector.worker_tool",
		FetchedAt:          now,
		EnrichedAt:         now,
		Items:              items,
	}, nil
}

func (c *ToolCollector) hasTool(ctx context.Context, wanted string) bool {
	tools, err := c.runner.ListTools(ctx)
	if err != nil {
		return false
	}
	for _, metadata := range tools {
		if strings.EqualFold(strings.TrimSpace(metadata.ToolID), wanted) {
			return true
		}
	}
	return false
}

type searchItem struct {
	Title   string `json:"title"`
	Link    string `json:"link"`
	Snippet string `json:"snippet"`
}

func itemsFromSearchMetadata(metadata map[string]any) []domainnews.Item {
	if metadata == nil || metadata["search_items"] == nil {
		return nil
	}
	encoded, err := json.Marshal(metadata["search_items"])
	if err != nil {
		return nil
	}
	var sourceItems []searchItem
	if err := json.Unmarshal(encoded, &sourceItems); err != nil {
		return nil
	}
	items := make([]domainnews.Item, 0, len(sourceItems))
	for index, source := range sourceItems {
		if strings.TrimSpace(source.Title) == "" && strings.TrimSpace(source.Link) == "" {
			continue
		}
		items = append(items, makeLiveItem(index, source.Title, source.Link, source.Snippet, "search_result_only"))
	}
	return items
}

func itemsFromSearchAndFetch(result any) []domainnews.Item {
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil
	}
	var response modulewebgather.SearchAndFetchResponse
	if err := json.Unmarshal(encoded, &response); err != nil {
		return nil
	}
	items := make([]domainnews.Item, 0, len(response.Items))
	for index, item := range response.Items {
		title := strings.TrimSpace(item.Fetch.Title)
		if title == "" {
			title = strings.TrimSpace(item.SearchResult.Title)
		}
		link := strings.TrimSpace(item.Fetch.FinalURL)
		if link == "" {
			link = strings.TrimSpace(item.SearchResult.URL)
		}
		body := strings.TrimSpace(item.Fetch.TextPreview)
		if body == "" {
			body = strings.TrimSpace(item.SearchResult.Snippet)
		}
		if title == "" && link == "" {
			continue
		}
		readStatus := "search_result_only"
		if strings.EqualFold(strings.TrimSpace(item.Fetch.Status), "ok") && body != "" {
			readStatus = "fetched"
		}
		items = append(items, makeLiveItem(index, title, link, body, readStatus))
	}
	return items
}

func makeLiveItem(index int, title, link, summary, readStatus string) domainnews.Item {
	title = strings.TrimSpace(title)
	link = strings.TrimSpace(link)
	source := "Web検索"
	if parsed, err := url.Parse(link); err == nil && parsed.Hostname() != "" {
		source = parsed.Hostname()
	}
	return domainnews.Item{
		ID:               liveItemID(index, title, link),
		Title:            title,
		Category:         "news",
		Source:           source,
		SourceType:       domainnews.SourceLiveSearch,
		URL:              link,
		SourceReadStatus: readStatus,
		SourceReadURL:    link,
		Summary:          strings.TrimSpace(summary),
	}
}

func liveItemID(index int, title, link string) string {
	key := strings.TrimSpace(link)
	if key == "" {
		key = strings.TrimSpace(title)
	}
	if key == "" {
		key = "item"
	}
	hash := sha256.Sum256([]byte(key))
	return fmt.Sprintf("live-%s-%02d", hex.EncodeToString(hash[:6]), index)
}

var _ domainnews.DailyNewsBriefCollector = (*ToolCollector)(nil)
