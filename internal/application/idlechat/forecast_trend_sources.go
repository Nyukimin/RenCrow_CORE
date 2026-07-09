package idlechat

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// fetchGoogleNewsSeeds はGoogle News RSSからキーワード検索でヘッドラインを取得する。
func fetchGoogleNewsSeeds(keyword string, limit int) []string {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return nil
	}
	rssURL := buildGoogleNewsRSSSearchURL(keyword)
	headlines, err := fetchGoogleNewsRSS(rssURL, limit)
	if err != nil {
		log.Printf("[Forecast] Google News RSS failed (q=%s): %v", keyword, err)
		return nil
	}
	return headlines
}

func buildGoogleNewsRSSSearchURL(keyword string) string {
	return fmt.Sprintf("https://news.google.com/rss/search?q=%s&hl=ja&gl=JP&ceid=JP:ja",
		url.QueryEscape(strings.TrimSpace(keyword)))
}

// fetchGoogleNewsRSS はGoogle News RSSをパースしてタイトルを取得する。
func fetchGoogleNewsRSS(rssURL string, limit int) ([]string, error) {
	req, err := http.NewRequest("GET", rssURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "RenCrow/1.0")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, idlechatHTTPStatusError("google news rss status", resp.StatusCode, resp.Body)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	content := string(body)
	var headlines []string
	inItem := false
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "<item>") {
			inItem = true
		} else if strings.HasPrefix(line, "</item>") {
			inItem = false
		} else if inItem && strings.HasPrefix(line, "<title>") {
			title := strings.TrimPrefix(line, "<title>")
			if i := strings.Index(title, "</title>"); i >= 0 {
				title = title[:i]
			}
			title = strings.TrimSpace(title)
			// Google News のタイトルは「見出し - ソース名」形式。ソース名を除去
			if i := strings.LastIndex(title, " - "); i > 0 {
				title = strings.TrimSpace(title[:i])
			}
			if title != "" && len(headlines) < limit {
				headlines = append(headlines, title)
			}
		}
	}
	return headlines, nil
}

// TrendSourceSet はドメインごとのトレンドソース設定。
type TrendSourceSet struct {
	RedditSubs     []string
	HatenaCategory string
}

type forecastDomainProfile struct {
	Keywords []string
}

// TrendCache は1時間TTLのトレンドキャッシュ。
type TrendCache struct {
	Hour              string              // "2006-01-02T15" 形式
	GoogleTrends      []string            // Google Trends JP
	RedditBySubreddit map[string][]string // subreddit → titles
	HatenaByCategory  map[string][]string // category → titles
	FetchedAt         time.Time
}

func getTrendCache() *TrendCache {
	trendMu.RLock()
	defer trendMu.RUnlock()
	return trendCache
}

func fetchHourlyTrends() error {
	hour := time.Now().In(jst).Format("2006-01-02T15")

	trendMu.RLock()
	if trendCache != nil && trendCache.Hour == hour {
		trendMu.RUnlock()
		return nil
	}
	trendMu.RUnlock()

	trendMu.Lock()
	defer trendMu.Unlock()
	if trendCache != nil && trendCache.Hour == hour {
		return nil
	}

	log.Printf("[Forecast] Fetching hourly trends for %s...", hour)

	cache := &TrendCache{
		Hour:              hour,
		RedditBySubreddit: make(map[string][]string),
		HatenaByCategory:  make(map[string][]string),
		FetchedAt:         time.Now(),
	}

	// Google Trends JP
	if trends, err := fetchGoogleTrendsJP(20); err != nil {
		log.Printf("[Forecast] Google Trends failed: %v", err)
	} else {
		cache.GoogleTrends = trends
	}

	// Reddit (全ドメインで使うサブレディットを集約)
	allSubs := make(map[string]struct{})
	for _, src := range domainTrendSources {
		for _, sub := range src.RedditSubs {
			allSubs[sub] = struct{}{}
		}
	}
	for sub := range allSubs {
		if titles, err := fetchRedditHot(sub, 10); err != nil {
			log.Printf("[Forecast] Reddit r/%s failed: %v", sub, err)
		} else {
			cache.RedditBySubreddit[sub] = titles
		}
	}

	// はてブ (全ドメインで使うカテゴリを集約)
	allCats := make(map[string]struct{})
	for _, src := range domainTrendSources {
		if src.HatenaCategory != "" {
			allCats[src.HatenaCategory] = struct{}{}
		}
	}
	for cat := range allCats {
		if titles, err := fetchHatenaHotentry(cat, 10); err != nil {
			log.Printf("[Forecast] Hatena %s failed: %v", cat, err)
		} else {
			cache.HatenaByCategory[cat] = titles
		}
	}

	trendCache = cache
	log.Printf("[Forecast] Trends fetched: google=%d reddit_subs=%d hatena_cats=%d",
		len(cache.GoogleTrends), len(cache.RedditBySubreddit), len(cache.HatenaByCategory))
	return nil
}

// fetchTrendSeeds はドメインに対応するトレンド情報を集約して返す。
func fetchTrendSeeds(domain ForecastDomain) []string {
	if err := fetchHourlyTrends(); err != nil {
		log.Printf("[Forecast] Trend fetch error: %v", err)
	}
	cache := getTrendCache()
	if cache == nil {
		return nil
	}

	src := domainTrendSources[domain.Name]
	var all []string

	// Google Trends は全ドメイン共通なので混ぜすぎない。
	if len(cache.GoogleTrends) > 0 {
		gt := cache.GoogleTrends
		if len(gt) > forecastGoogleTrendLimit {
			gt = pickRandom(gt, forecastGoogleTrendLimit)
		}
		all = append(all, gt...)
	}

	// Reddit
	for _, sub := range src.RedditSubs {
		if titles, ok := cache.RedditBySubreddit[sub]; ok {
			picked := titles
			if len(picked) > 5 {
				picked = pickRandom(titles, 5)
			}
			all = append(all, picked...)
		}
	}

	// はてブ
	if src.HatenaCategory != "" {
		if titles, ok := cache.HatenaByCategory[src.HatenaCategory]; ok {
			picked := titles
			if len(picked) > 5 {
				picked = pickRandom(titles, 5)
			}
			all = append(all, picked...)
		}
	}

	// 重複排除
	seen := make(map[string]struct{}, len(all))
	unique := make([]string, 0, len(all))
	for _, h := range all {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if _, ok := seen[h]; !ok {
			seen[h] = struct{}{}
			unique = append(unique, h)
		}
	}
	unique = rankForecastSeeds(domain, unique)
	if len(unique) > forecastSeedLimit {
		unique = unique[:forecastSeedLimit]
	}
	return unique
}

func rankForecastSeeds(domain ForecastDomain, seeds []string) []string {
	if len(seeds) < 2 {
		return seeds
	}
	type scoredSeed struct {
		text  string
		score int
	}
	scored := make([]scoredSeed, 0, len(seeds))
	for _, seed := range seeds {
		scored = append(scored, scoredSeed{
			text:  seed,
			score: scoreForecastSeed(domain, seed),
		})
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return rand.Intn(2) == 0
	})
	out := make([]string, 0, len(scored))
	for _, item := range scored {
		out = append(out, item.text)
	}
	return out
}

func scoreForecastSeed(domain ForecastDomain, seed string) int {
	s := strings.ToLower(strings.TrimSpace(seed))
	if s == "" {
		return 0
	}
	score := 1
	for _, kw := range forecastDomainProfiles[domain.Name].Keywords {
		kw = strings.ToLower(strings.TrimSpace(kw))
		if kw == "" {
			continue
		}
		if strings.Contains(s, kw) {
			score += 4
		}
	}
	if strings.Contains(s, strings.ToLower(domain.Name)) {
		score += 3
	}
	// 少し長めで具体性のある見出しを優先する。
	if n := len([]rune(s)); n >= 16 && n <= 48 {
		score += 1
	}
	return score
}

// fetchGoogleTrendsJP は Google Trends JP のRSSからトレンドワードを取得する。
func fetchGoogleTrendsJP(limit int) ([]string, error) {
	rssURL := "https://trends.google.co.jp/trending/rss?geo=JP"
	req, err := http.NewRequest("GET", rssURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "RenCrow/1.0")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, idlechatHTTPStatusError("google trends rss status", resp.StatusCode, resp.Body)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	content := string(body)
	var titles []string
	inItem := false
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "<item>") || strings.HasPrefix(line, "<item ") {
			inItem = true
		} else if strings.HasPrefix(line, "</item>") {
			inItem = false
		} else if inItem && strings.HasPrefix(line, "<title>") {
			title := strings.TrimPrefix(line, "<title>")
			if i := strings.Index(title, "</title>"); i >= 0 {
				title = title[:i]
			}
			title = strings.TrimSpace(title)
			if title != "" && len(titles) < limit {
				titles = append(titles, title)
			}
		}
	}
	return titles, nil
}

// fetchRedditHot は Reddit サブレディットの hot 記事タイトルを取得する。
func fetchRedditHot(subreddit string, limit int) ([]string, error) {
	url := fmt.Sprintf("https://www.reddit.com/r/%s/hot.json?limit=%d", subreddit, limit)
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
		return nil, idlechatHTTPStatusError(fmt.Sprintf("reddit r/%s status", subreddit), resp.StatusCode, resp.Body)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		Data struct {
			Children []struct {
				Data struct {
					Title string `json:"title"`
				} `json:"data"`
			} `json:"children"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("reddit json parse: %w", err)
	}

	var titles []string
	for _, child := range result.Data.Children {
		t := strings.TrimSpace(child.Data.Title)
		if t != "" {
			titles = append(titles, t)
		}
	}
	return titles, nil
}

// fetchHatenaHotentry ははてなブックマークのホットエントリRSSからタイトルを取得する。
func fetchHatenaHotentry(category string, limit int) ([]string, error) {
	rssURL := fmt.Sprintf("https://b.hatena.ne.jp/hotentry/%s.rss", category)
	req, err := http.NewRequest("GET", rssURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "RenCrow/1.0")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, idlechatHTTPStatusError(fmt.Sprintf("hatena %s rss status", category), resp.StatusCode, resp.Body)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	content := string(body)
	var titles []string
	inItem := false
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "<item") {
			inItem = true
		} else if strings.HasPrefix(line, "</item>") {
			inItem = false
		} else if inItem && strings.HasPrefix(line, "<title>") {
			title := strings.TrimPrefix(line, "<title>")
			if i := strings.Index(title, "</title>"); i >= 0 {
				title = title[:i]
			}
			title = strings.TrimSpace(title)
			if title != "" && len(titles) < limit {
				titles = append(titles, title)
			}
		}
	}
	return titles, nil
}
