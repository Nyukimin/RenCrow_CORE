package idlechat

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultRedditEndpoint  = "https://www.reddit.com"
	defaultXRecentEndpoint = "https://api.x.com/2/tweets/search/recent"
)

// NewsSourceConfig はIdleChatのお題キャッシュへ追加するSNS取得先を表す。
type NewsSourceConfig struct {
	RedditEnabled     bool
	RedditCommunities []string
	RedditLimit       int
	XEnabled          bool
	XBearerToken      string
	XQueries          []XNewsQuery
}

// XNewsQuery はX Recent Search APIへ渡す検索条件を表す。
type XNewsQuery struct {
	Name     string
	Category string
	Query    string
	Limit    int
}

func fetchRedditHotSeeds(communities []string, limit int) ([]NewsSeed, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	return fetchRedditHotSeedsFrom(client, defaultRedditEndpoint, communities, limit)
}

func fetchRedditHotSeedsFrom(client *http.Client, baseURL string, communities []string, limit int) ([]NewsSeed, error) {
	if client == nil {
		return nil, fmt.Errorf("reddit http client is nil")
	}
	cleanCommunities := make([]string, 0, len(communities))
	for _, community := range communities {
		community = strings.TrimSpace(strings.TrimPrefix(community, "r/"))
		if community != "" {
			cleanCommunities = append(cleanCommunities, community)
		}
	}
	if len(cleanCommunities) == 0 || limit <= 0 {
		return []NewsSeed{}, nil
	}

	endpoint := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if endpoint == "" {
		endpoint = defaultRedditEndpoint
	}
	if endpoint == defaultRedditEndpoint {
		parts := make([]string, 0, len(cleanCommunities))
		for _, community := range cleanCommunities {
			parts = append(parts, url.PathEscape(community))
		}
		endpoint += "/r/" + strings.Join(parts, "+") + "/.rss"
	}
	parsedURL, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	query := parsedURL.Query()
	query.Set("limit", strconv.Itoa(limit))
	parsedURL.RawQuery = query.Encode()

	req, err := http.NewRequest(http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "RenCrow/1.0 (https://github.com/Nyukimin/RenCrow_CORE)")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, idlechatHTTPStatusError("reddit hot status", resp.StatusCode, resp.Body)
	}

	var feed struct {
		Entries []struct {
			Title    string `xml:"title"`
			Category struct {
				Term string `xml:"term,attr"`
			} `xml:"category"`
			Links []struct {
				Href string `xml:"href,attr"`
			} `xml:"link"`
		} `xml:"entry"`
	}
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&feed); err != nil {
		return nil, fmt.Errorf("reddit atom parse: %w", err)
	}

	seeds := make([]NewsSeed, 0, min(limit, len(feed.Entries)))
	for _, entry := range feed.Entries {
		title := strings.Join(strings.Fields(entry.Title), " ")
		if title == "" {
			continue
		}
		community := strings.TrimSpace(entry.Category.Term)
		if community == "" {
			community = strings.Join(cleanCommunities, "+")
		}
		postURL := ""
		for _, link := range entry.Links {
			if href := strings.TrimSpace(link.Href); href != "" {
				postURL = href
				break
			}
		}
		if strings.HasPrefix(postURL, "/") {
			postURL = "https://www.reddit.com" + postURL
		}
		seeds = append(seeds, NewsSeed{
			Title:      title,
			Category:   "social",
			Source:     "Reddit r/" + community,
			SourceType: "reddit",
			URL:        postURL,
		})
		if len(seeds) >= limit {
			break
		}
	}
	return seeds, nil
}

func fetchXRecentSeeds(token string, source XNewsQuery) ([]NewsSeed, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	return fetchXRecentSeedsFrom(client, defaultXRecentEndpoint, token, source)
}

func fetchXRecentSeedsFrom(client *http.Client, endpoint, token string, source XNewsQuery) ([]NewsSeed, error) {
	if client == nil {
		return nil, fmt.Errorf("x http client is nil")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("x bearer token is empty")
	}
	queryText := strings.TrimSpace(source.Query)
	if queryText == "" {
		return nil, fmt.Errorf("x query is empty")
	}
	requestedLimit := source.Limit
	if requestedLimit <= 0 {
		requestedLimit = 10
	}
	apiLimit := requestedLimit
	if apiLimit < 10 {
		apiLimit = 10
	}
	if apiLimit > 100 {
		apiLimit = 100
	}

	parsedURL, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return nil, err
	}
	query := parsedURL.Query()
	query.Set("query", queryText)
	query.Set("max_results", strconv.Itoa(apiLimit))
	query.Set("tweet.fields", "created_at")
	parsedURL.RawQuery = query.Encode()

	req, err := http.NewRequest(http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "RenCrow/1.0 (https://github.com/Nyukimin/RenCrow_CORE)")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, idlechatHTTPStatusError("x recent search status", resp.StatusCode, resp.Body)
	}

	var result struct {
		Data []struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&result); err != nil {
		return nil, fmt.Errorf("x recent search json parse: %w", err)
	}

	name := strings.TrimSpace(source.Name)
	if name == "" {
		name = "X"
	}
	category := strings.TrimSpace(source.Category)
	if category == "" {
		category = "social"
	}
	seeds := make([]NewsSeed, 0, min(requestedLimit, len(result.Data)))
	for _, post := range result.Data {
		text := strings.Join(strings.Fields(post.Text), " ")
		if text == "" {
			continue
		}
		postURL := ""
		if id := strings.TrimSpace(post.ID); id != "" {
			postURL = "https://x.com/i/web/status/" + id
		}
		seeds = append(seeds, NewsSeed{
			Title:      text,
			Category:   category,
			Source:     name,
			SourceType: "x",
			URL:        postURL,
		})
		if len(seeds) >= requestedLimit {
			break
		}
	}
	return seeds, nil
}

func mergeNewsSeeds(limit int, groups ...[]NewsSeed) []NewsSeed {
	if limit <= 0 {
		return []NewsSeed{}
	}
	merged := make([]NewsSeed, 0, limit)
	seen := make(map[string]struct{})
	for _, group := range groups {
		for _, seed := range group {
			seed.Title = strings.Join(strings.Fields(seed.Title), " ")
			if seed.Title == "" {
				continue
			}
			key := strings.ToLower(seed.Title)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			merged = append(merged, seed)
			if len(merged) >= limit {
				return merged
			}
		}
	}
	return merged
}
