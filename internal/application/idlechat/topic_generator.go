package idlechat

import (
	"math/rand"
	"sync"
)

// TopicStrategy はトピック生成の戦略
type TopicStrategy string

const (
	StrategySingleGenre      TopicStrategy = "single"   // 1ジャンル単体
	StrategyDoubleGenre      TopicStrategy = "double"   // 2ジャンル掛け合わせ
	StrategyExternalStimulus TopicStrategy = "external" // 外部刺激
	StrategyMovie            TopicStrategy = "movie"    // 架空映画深掘り
	StrategyNews             TopicStrategy = "news"     // ニュース深掘り
	StrategyForecast         TopicStrategy = "forecast" // 未来展望セッション
)

type topicAnchor struct {
	Kind  string
	Value string
}

// genrePool is retained for the legacy prompt helpers. Production single and
// double selection uses the compact static plus daily-fresh word projection.
var genrePool = staticTopicWordValues()

var topicAnchorPool = []topicAnchor{
	{Kind: "人物", Value: "駆け出しのアーティスト"},
	{Kind: "人物", Value: "古書店の店主"},
	{Kind: "人物", Value: "深夜ラジオのパーソナリティ"},
	{Kind: "人物", Value: "地方博物館の学芸員"},
	{Kind: "人物", Value: "老舗工房の職人"},
	{Kind: "人物", Value: "商店街の修理屋"},
	{Kind: "人物", Value: "小さな映画館の映写担当"},
	{Kind: "物", Value: "壊れたオルゴール"},
	{Kind: "物", Value: "使い込まれた観測ノート"},
	{Kind: "物", Value: "古いカセットテープ"},
	{Kind: "物", Value: "標本箱"},
	{Kind: "物", Value: "雨に濡れたポスター"},
	{Kind: "場所", Value: "港町の倉庫街"},
	{Kind: "場所", Value: "地下街の片隅"},
	{Kind: "場所", Value: "閉館前の温室"},
	{Kind: "場所", Value: "始発前の駅"},
	{Kind: "場所", Value: "川沿いの遊歩道"},
	{Kind: "場面", Value: "閉店後の片付け時間"},
	{Kind: "場面", Value: "雨上がりの朝"},
	{Kind: "場面", Value: "文化祭の前夜"},
	{Kind: "場面", Value: "展示替えの直前"},
	{Kind: "場面", Value: "録音のリハーサル中"},
}

var (
	dailyCache *DailySeedCache
	cacheMu    sync.RWMutex
)

// chooseStrategy は通常IdleChatの生成戦略をランダムに選択
// single/double/external/movie/news: 各20%
func chooseStrategy() TopicStrategy {
	r := rand.Intn(100)
	switch {
	case r < 20:
		return StrategySingleGenre
	case r < 40:
		return StrategyDoubleGenre
	case r < 60:
		return StrategyExternalStimulus
	case r < 80:
		return StrategyMovie
	default:
		return StrategyNews
	}
}
