package idlechat

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
)

// DailySeedCache は1日1回取得する外部シードのキャッシュ
type DailySeedCache = modulechat.DailySeedCache

// NewsSeed はニュース見出しに取得元カテゴリを付与したIdleChat用シード。
type NewsSeed = modulechat.NewsSeed

// NewsSeedSource は1つのニュースRSS取得先を表す。
type NewsSeedSource = modulechat.NewsSeedSource

var defaultNewsSeedSources = []NewsSeedSource{
	{Category: "general", Name: "NHK Top", URL: "https://www.nhk.or.jp/rss/news/cat0.xml", Limit: 4},
	{Category: "culture", Name: "NHK Science/Culture", URL: "https://www.nhk.or.jp/rss/news/cat3.xml", Limit: 3},
	{Category: "business", Name: "NHK Business", URL: "https://www.nhk.or.jp/rss/news/cat5.xml", Limit: 3},
	{Category: "world", Name: "NHK World", URL: "https://www.nhk.or.jp/rss/news/cat6.xml", Limit: 3},
	{Category: "sports", Name: "NHK Sports", URL: "https://www.nhk.or.jp/rss/news/cat7.xml", Limit: 3},
	{Category: "tech", Name: "ITmedia NEWS Technology", URL: "https://rss.itmedia.co.jp/rss/2.0/news_technology.xml", Limit: 4},
	{Category: "business", Name: "ITmedia Business", URL: "https://rss.itmedia.co.jp/rss/2.0/business.xml", Limit: 3},
}

// fetchDailySeeds は1日1回、起動時に外部シードを取得してキャッシュ
func fetchDailySeeds() error {
	today := time.Now().In(jst).Format("2006-01-02")

	cacheMu.RLock()
	if dailyCache != nil && dailyCache.Date == today {
		cacheMu.RUnlock()
		return nil // 既に取得済み
	}
	cacheMu.RUnlock()

	cacheMu.Lock()
	defer cacheMu.Unlock()

	// ダブルチェック
	if dailyCache != nil && dailyCache.Date == today {
		return nil
	}

	log.Printf("[IdleChat] Fetching daily seeds for %s...", today)

	// Wikipedia Random（10件）
	wikiSeeds, err := fetchWikipediaRandom(10)
	if err != nil {
		log.Printf("[IdleChat] Wikipedia fetch failed: %v", err)
		wikiSeeds = []string{} // フォールバック
	}

	// News Headlines（カテゴリ付きRSS）
	newsSeedItems, err := fetchNewsSeedItems(defaultNewsSeedSources, 20)
	if err != nil {
		log.Printf("[IdleChat] News fetch failed: %v", err)
		newsSeedItems = []NewsSeed{} // フォールバック
	}
	newsSeeds := newsSeedTitles(newsSeedItems)

	dailyCache = &DailySeedCache{
		Date:           today,
		WikipediaSeeds: wikiSeeds,
		NewsSeeds:      newsSeeds,
		NewsSeedItems:  newsSeedItems,
		FetchedAt:      time.Now(),
	}

	log.Printf("[IdleChat] Daily seeds fetched: Wikipedia=%d, News=%d categories=%s", len(wikiSeeds), len(newsSeeds), newsSeedCategorySummary(newsSeedItems))
	return nil
}

// fetchWikipediaRandom はWikipedia Random APIから記事タイトルを取得
func fetchWikipediaRandom(limit int) ([]string, error) {
	url := fmt.Sprintf("https://ja.wikipedia.org/w/api.php?action=query&list=random&rnlimit=%d&rnnamespace=0&format=json", limit)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "RenCrow/1.0 (https://github.com/Nyukimin/RenCrow_CORE)")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, idlechatHTTPStatusError("wikipedia api returned status", resp.StatusCode, resp.Body)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		Query struct {
			Random []struct {
				Title string `json:"title"`
			} `json:"random"`
		} `json:"query"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	titles := make([]string, 0, len(result.Query.Random))
	for _, item := range result.Query.Random {
		titles = append(titles, item.Title)
	}

	return titles, nil
}

// fetchNewsHeadlines はNHK News RSSトップニュースからヘッドラインを取得
func fetchNewsHeadlines(limit int) ([]string, error) {
	seeds, err := fetchNewsSeedsFrom(NewsSeedSource{
		Category:    "general",
		Name:        "NHK Top",
		URL:         "https://www.nhk.or.jp/rss/news/cat0.xml",
		ErrorPrefix: "nhk rss",
	}, limit)
	if err != nil {
		return nil, err
	}
	return newsSeedTitles(seeds), nil
}

// fetchNewsHeadlinesFrom は指定URLのNHK RSSからヘッドラインを取得
func fetchNewsHeadlinesFrom(rssURL string, limit int) ([]string, error) {
	seeds, err := fetchNewsSeedsFrom(NewsSeedSource{
		Category:    "general",
		Name:        "RSS",
		URL:         rssURL,
		ErrorPrefix: "nhk rss",
	}, limit)
	if err != nil {
		return nil, err
	}
	return newsSeedTitles(seeds), nil
}

func fetchNewsSeedItems(sources []NewsSeedSource, limit int) ([]NewsSeed, error) {
	if limit <= 0 {
		return []NewsSeed{}, nil
	}

	items := make([]NewsSeed, 0, limit)
	var failures []string
	for _, source := range sources {
		sourceLimit := source.Limit
		if sourceLimit <= 0 || sourceLimit > limit {
			sourceLimit = limit
		}
		seeds, err := fetchNewsSeedsFrom(source, sourceLimit)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", source.Name, err))
			continue
		}
		for _, seed := range seeds {
			items = append(items, seed)
			if len(items) >= limit {
				return items, nil
			}
		}
	}
	if len(items) == 0 && len(failures) > 0 {
		return nil, fmt.Errorf("all news rss fetches failed: %s", strings.Join(failures, "; "))
	}
	for _, failure := range failures {
		log.Printf("[IdleChat] News source fetch failed: %s", failure)
	}
	return items, nil
}

func fetchNewsSeedsFrom(source NewsSeedSource, limit int) ([]NewsSeed, error) {
	req, err := http.NewRequest("GET", source.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "RenCrow/1.0 (https://github.com/Nyukimin/RenCrow_CORE)")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		prefix := strings.TrimSpace(source.ErrorPrefix)
		if prefix == "" {
			prefix = "news rss"
		}
		return nil, idlechatHTTPStatusError(prefix+" returned status", resp.StatusCode, resp.Body)
	}

	return parseNewsSeeds(resp.Body, source, limit)
}

func parseNewsSeeds(reader io.Reader, source NewsSeedSource, limit int) ([]NewsSeed, error) {
	return modulechat.ParseNewsSeeds(reader, source, limit)
}

func newsSeedTitles(seeds []NewsSeed) []string {
	return modulechat.NewsSeedTitles(seeds)
}

func newsSeedCategorySummary(seeds []NewsSeed) string {
	return modulechat.NewsSeedCategorySummary(seeds)
}

// getDailyCache は現在のキャッシュを取得（スレッドセーフ）
func getDailyCache() *DailySeedCache {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	return dailyCache
}

func idlechatHTTPStatusError(prefix string, status int, body io.Reader) error {
	data, _ := io.ReadAll(io.LimitReader(body, 4096))
	text := strings.TrimSpace(string(data))
	if text != "" {
		return fmt.Errorf("%s %d: %s", prefix, status, text)
	}
	return fmt.Errorf("%s %d", prefix, status)
}
