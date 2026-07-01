package idlechat

import (
	"math/rand"
	"sync"
)

const (
	forecastTurnsPerDomain     = 100 // 1ドメインあたりの最大ターン数
	forecastCheckpointInterval = 15  // 進行チェックポイントの間隔（ターン数）
	forecastTopicStockSize     = 2   // ドメインあたりのお題ストック数
	forecastSeedLimit          = 10
	forecastGoogleTrendLimit   = 2
)

// ForecastDomain は未来展望セッションの1ドメインを定義する。
type ForecastDomain struct {
	Name    string   // 表示名（例: "AI技術"）
	RSSURLs []string // NHK RSS カテゴリURL
}

var forecastDomains = []ForecastDomain{
	{
		Name:    "AI技術",
		RSSURLs: []string{"https://www.nhk.or.jp/rss/news/cat2.xml"},
	},
	{
		Name:    "その他技術",
		RSSURLs: []string{"https://www.nhk.or.jp/rss/news/cat2.xml"},
	},
	{
		Name:    "医療",
		RSSURLs: []string{"https://www.nhk.or.jp/rss/news/cat7.xml", "https://www.nhk.or.jp/rss/news/cat2.xml"},
	},
	{
		Name:    "社会保障",
		RSSURLs: []string{"https://www.nhk.or.jp/rss/news/cat7.xml", "https://www.nhk.or.jp/rss/news/cat1.xml"},
	},
	{
		Name:    "政治",
		RSSURLs: []string{"https://www.nhk.or.jp/rss/news/cat3.xml"},
	},
	{
		Name:    "経済",
		RSSURLs: []string{"https://www.nhk.or.jp/rss/news/cat4.xml"},
	},
}

var forecastTopicAngles = []string{
	"制度・規制・ガバナンスの変化",
	"生活者の行動変化と受け止め方",
	"地方と都市の格差・インフラ影響",
	"雇用・教育・働き方の再編",
	"国際競争と地政学リスク",
	"コスト構造・投資・産業再編",
	"倫理・信頼・説明責任",
	"現場運用で起きる意外な副作用",
}

func shuffledForecastDomains() []ForecastDomain {
	out := append([]ForecastDomain(nil), forecastDomains...)
	rand.Shuffle(len(out), func(i, j int) {
		out[i], out[j] = out[j], out[i]
	})
	return out
}

// --- お題ストック ---

// --- トレンド収集 (Stage 1) ---

var domainTrendSources = map[string]TrendSourceSet{
	"AI技術":  {RedditSubs: []string{"artificial", "MachineLearning"}, HatenaCategory: "it"},
	"その他技術": {RedditSubs: []string{"technology"}, HatenaCategory: "it"},
	"医療":    {HatenaCategory: "social"},
	"社会保障":  {HatenaCategory: "social"},
	"政治":    {HatenaCategory: "economics"},
	"経済":    {RedditSubs: []string{"economics"}, HatenaCategory: "economics"},
}

var forecastDomainProfiles = map[string]forecastDomainProfile{
	"AI技術": {
		Keywords: []string{"ai", "人工知能", "生成ai", "llm", "機械学習", "半導体", "推論", "モデル", "自動運転", "ロボット"},
	},
	"その他技術": {
		Keywords: []string{"量子", "宇宙", "電池", "通信", "ネットワーク", "デバイス", "センサー", "材料", "エネルギー", "クラウド"},
	},
	"医療": {
		Keywords: []string{"医療", "病院", "診療", "治療", "創薬", "薬", "患者", "ワクチン", "手術", "介護"},
	},
	"社会保障": {
		Keywords: []string{"年金", "介護", "保険", "福祉", "少子化", "高齢化", "給付", "負担", "子育て", "生活保護"},
	},
	"政治": {
		Keywords: []string{"政権", "国会", "選挙", "外交", "防衛", "法案", "行政", "自治体", "官僚", "首相"},
	},
	"経済": {
		Keywords: []string{"金利", "為替", "株", "物価", "景気", "投資", "賃金", "雇用", "企業", "貿易"},
	},
}

var (
	trendCache *TrendCache
	trendMu    sync.RWMutex
)

// --- トレンド取得関数 ---
