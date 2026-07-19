package chat

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

type TopicCategory string

const (
	TopicCategorySingle   TopicCategory = "single"
	TopicCategoryDouble   TopicCategory = "double"
	TopicCategoryExternal TopicCategory = "external"
	TopicCategoryMovie    TopicCategory = "movie"
	TopicCategoryNews     TopicCategory = "news"
	TopicCategoryForecast TopicCategory = "forecast"
	TopicCategoryStory    TopicCategory = "story"
)

var (
	ErrUnsupportedTopicCategory    = errors.New("topic_category_unsupported")
	ErrTopicSeedUnavailable        = errors.New("topic_seed_unavailable")
	ErrTopicGenerationInvalidJSON  = errors.New("topic_generation_invalid_json")
	ErrTopicGenerationNoCandidates = errors.New("topic_generation_no_candidates")
	ErrTopicContractViolation      = errors.New("topic_contract_violation")
	ErrTopicJudgeInvalidJSON       = errors.New("topic_judge_invalid_json")
	ErrTopicJudgeWinnerMissing     = errors.New("topic_judge_winner_missing")
	ErrTopicJudgeLowScore          = errors.New("topic_judge_low_score")
	ErrRecentTopicExactDuplicate   = errors.New("topic_recent_exact_duplicate")
	ErrRecentTopicTooSimilar       = errors.New("topic_recent_too_similar")
	ErrTopicGenerationFailed       = errors.New("topic_generation_failed")
)

type TopicSeed struct {
	Category TopicCategory `json:"category"`

	Genre1 string `json:"genre_1,omitempty"`
	Genre2 string `json:"genre_2,omitempty"`

	ExternalMaterial *ExternalMaterialSeed `json:"external_material,omitempty"`

	News *NewsSeed `json:"news,omitempty"`

	ForecastDomain string   `json:"forecast_domain,omitempty"`
	TrendKeywords  []string `json:"trend_keywords,omitempty"`

	StoryBase      string `json:"story_base,omitempty"`
	StoryTransform string `json:"story_transform,omitempty"`

	RecentTopics []RecentTopic `json:"recent_topics,omitempty"`
}

type ExternalMaterialSeed struct {
	Title    string `json:"title"`
	Summary  string `json:"summary,omitempty"`
	Provider string `json:"provider,omitempty"`
	URL      string `json:"url,omitempty"`
	Category string `json:"category,omitempty"`
}

type NewsSeed struct {
	Title      string `json:"title"`
	Category   string `json:"category,omitempty"`
	Source     string `json:"source,omitempty"`
	SourceType string `json:"source_type,omitempty"`
	URL        string `json:"url,omitempty"`
	Summary    string `json:"summary,omitempty"`
}

type RecentTopic struct {
	Topic    string        `json:"topic"`
	Category TopicCategory `json:"category,omitempty"`
	Strategy string        `json:"strategy,omitempty"`
}

type TopicCandidate struct {
	Topic               string `json:"topic"`
	InterestingnessAxis string `json:"interestingness_axis,omitempty"`
	OpeningHook         string `json:"opening_hook,omitempty"`
	Avoid               string `json:"avoid,omitempty"`
	Rationale           string `json:"rationale,omitempty"`
}

type TopicJudgeResult struct {
	WinnerTopic         string            `json:"winner_topic"`
	Scores              []TopicJudgeScore `json:"scores"`
	RejectReasonSummary string            `json:"reject_reason_summary,omitempty"`
}

type TopicJudgeScore struct {
	Topic                 string `json:"topic"`
	CategoryFit           int    `json:"category_fit"`
	Concreteness          int    `json:"concreteness"`
	Curiosity             int    `json:"curiosity"`
	ConversationPotential int    `json:"conversation_potential"`
	AxisStrength          int    `json:"axis_strength"`
	Novelty               int    `json:"novelty"`
	Safety                int    `json:"safety"`
	Total                 int    `json:"total"`
	Reason                string `json:"reason"`
}

type TopicGenerationResult struct {
	Topic    string        `json:"topic"`
	Category TopicCategory `json:"category"`
	Strategy string        `json:"strategy"`

	InterestingnessAxis string `json:"interestingness_axis"`
	OpeningHook         string `json:"opening_hook"`
	Avoid               string `json:"avoid"`

	Seed       TopicSeed         `json:"seed"`
	Candidates []TopicCandidate  `json:"candidates,omitempty"`
	Judge      *TopicJudgeResult `json:"judge,omitempty"`
	Provider   string            `json:"provider"`
}

type TopicGenerationDiagnostic struct {
	SessionID         string                       `json:"session_id,omitempty"`
	Category          string                       `json:"category"`
	Strategy          string                       `json:"strategy"`
	Attempt           int                          `json:"attempt"`
	ErrorCode         string                       `json:"error_code,omitempty"`
	ErrorMessage      string                       `json:"error_message,omitempty"`
	SeedSummary       string                       `json:"seed_summary,omitempty"`
	CandidateCount    int                          `json:"candidate_count,omitempty"`
	InvalidCandidates []InvalidCandidateDiagnostic `json:"invalid_candidates,omitempty"`
	WinnerTopic       string                       `json:"winner_topic,omitempty"`
	JudgeTotal        int                          `json:"judge_total,omitempty"`
}

type InvalidCandidateDiagnostic struct {
	Topic string `json:"topic"`
	Error string `json:"error"`
}

const (
	MinJudgeTotal                  = 24
	MinCategoryFit                 = 4
	MinSafety                      = 4
	RecentTopicSimilarityThreshold = 0.82
)

var ExpectedAxisByCategory = map[TopicCategory]string{
	TopicCategorySingle:   "観察",
	TopicCategoryDouble:   "接続",
	TopicCategoryExternal: "偶然の意味化",
	TopicCategoryMovie:    "共同妄想",
	TopicCategoryNews:     "現実の影響",
	TopicCategoryForecast: "変化の分岐",
	TopicCategoryStory:    "視点反転",
}

var CommonForbiddenMetaTerms = []string{
	"カテゴリ", "strategy", "provider", "seed", "内部",
	"生成", "プロンプト", "JSON", "候補",
}

var ExternalForbiddenTerms = []string{
	"Wikipedia", "ウィキペディア",
	"外部刺激", "ランダム記事", "偶然の記事",
	"記事", "ページ", "検索結果", "取得元",
	"provider", "RSS", "URL",
}

var movieTopicPattern = regexp.MustCompile(`^「[^」]{2,24}」ってどんな映画？$`)

func NormalizeTopicCategory(s string) (TopicCategory, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "single":
		return TopicCategorySingle, nil
	case "double":
		return TopicCategoryDouble, nil
	case "external":
		return TopicCategoryExternal, nil
	case "movie":
		return TopicCategoryMovie, nil
	case "news":
		return TopicCategoryNews, nil
	case "forecast":
		return TopicCategoryForecast, nil
	case "story", "story-simple":
		return TopicCategoryStory, nil
	default:
		return "", ErrUnsupportedTopicCategory
	}
}

func StrategyFromTopicCategory(category TopicCategory) string {
	switch category {
	case TopicCategoryStory:
		return "story-simple"
	default:
		return string(category)
	}
}

func ValidateSeedForCategory(category TopicCategory, seed TopicSeed) error {
	switch category {
	case TopicCategorySingle:
		if strings.TrimSpace(seed.Genre1) == "" {
			return fmt.Errorf("%w: genre_1 is required", ErrTopicSeedUnavailable)
		}
	case TopicCategoryDouble:
		if strings.TrimSpace(seed.Genre1) == "" || strings.TrimSpace(seed.Genre2) == "" {
			return fmt.Errorf("%w: genre_1 and genre_2 are required", ErrTopicSeedUnavailable)
		}
	case TopicCategoryExternal:
		if strings.TrimSpace(seed.Genre1) == "" || seed.ExternalMaterial == nil || strings.TrimSpace(seed.ExternalMaterial.Title) == "" {
			return fmt.Errorf("%w: external material title and genre_1 are required", ErrTopicSeedUnavailable)
		}
	case TopicCategoryMovie:
		return nil
	case TopicCategoryNews:
		if seed.News == nil || strings.TrimSpace(seed.News.Title) == "" {
			return fmt.Errorf("%w: news seed is required", ErrTopicSeedUnavailable)
		}
	case TopicCategoryForecast:
		if strings.TrimSpace(seed.ForecastDomain) == "" {
			return fmt.Errorf("%w: forecast_domain is required", ErrTopicSeedUnavailable)
		}
	case TopicCategoryStory:
		if strings.TrimSpace(seed.StoryBase) == "" {
			return fmt.Errorf("%w: story_base is required", ErrTopicSeedUnavailable)
		}
	default:
		return ErrUnsupportedTopicCategory
	}
	return nil
}

func ValidateTopicCandidate(category TopicCategory, seed TopicSeed, candidate TopicCandidate) error {
	topic := strings.TrimSpace(candidate.Topic)
	if err := ValidateCommonTopic(topic); err != nil {
		return err
	}
	expectedAxis := ExpectedAxisByCategory[category]
	if expectedAxis != "" && strings.TrimSpace(candidate.InterestingnessAxis) != expectedAxis {
		return fmt.Errorf("%w: axis must be %q", ErrTopicContractViolation, expectedAxis)
	}
	switch category {
	case TopicCategorySingle:
		return nil
	case TopicCategoryDouble:
		if !containsAny(topic, strings.TrimSpace(seed.Genre1)) || !containsAny(topic, strings.TrimSpace(seed.Genre2)) {
			return fmt.Errorf("%w: double topic must contain both genres", ErrTopicContractViolation)
		}
	case TopicCategoryExternal:
		for _, term := range ExternalForbiddenTerms {
			if ContainsTopicTerm(topic, term) {
				return fmt.Errorf("%w: external topic leaks meta term %q", ErrTopicContractViolation, term)
			}
		}
		if seed.ExternalMaterial != nil && strings.TrimSpace(seed.ExternalMaterial.Title) != "" && !topicContainsLooseMaterial(topic, seed.ExternalMaterial.Title) {
			return fmt.Errorf("%w: external topic must preserve material", ErrTopicContractViolation)
		}
	case TopicCategoryMovie:
		if !movieTopicPattern.MatchString(topic) {
			return fmt.Errorf("%w: movie topic must match required format", ErrTopicContractViolation)
		}
		title := strings.TrimSuffix(strings.TrimPrefix(topic, "「"), "」ってどんな映画？")
		if strings.ContainsAny(title, "。！？!?") || containsAny(title, "あらすじ", "について", "映画について") {
			return fmt.Errorf("%w: movie title includes explanation", ErrTopicContractViolation)
		}
	case TopicCategoryNews:
		if seed.News == nil || strings.TrimSpace(seed.News.Title) == "" {
			return fmt.Errorf("%w: news seed is required", ErrTopicSeedUnavailable)
		}
		if ContainsTopicTerm(topic, "ニュースについて") || ContainsTopicTerm(topic, "記事") || ContainsTopicTerm(topic, "RSS") || ContainsTopicTerm(topic, "URL") || ContainsTopicTerm(topic, "provider") {
			return fmt.Errorf("%w: news topic leaks source or weak form", ErrTopicContractViolation)
		}
		if source := strings.TrimSpace(seed.News.Source); source != "" && ContainsTopicTerm(topic, source) {
			return fmt.Errorf("%w: news topic leaks source", ErrTopicContractViolation)
		}
	case TopicCategoryForecast:
		if strings.TrimSpace(seed.ForecastDomain) == "" {
			return fmt.Errorf("%w: forecast_domain is required", ErrTopicSeedUnavailable)
		}
		if topic == "AIの未来" || topic == "未来社会について" || topic == "人類はどうなるか" {
			return fmt.Errorf("%w: forecast topic is too abstract", ErrTopicContractViolation)
		}
		if !containsAny(topic, "変える", "変わる", "どう", "行方", "分岐", "影響", "再編", "変化") {
			return fmt.Errorf("%w: forecast topic must include change structure", ErrTopicContractViolation)
		}
	case TopicCategoryStory:
		if strings.TrimSpace(seed.StoryBase) == "" {
			return fmt.Errorf("%w: story_base is required", ErrTopicSeedUnavailable)
		}
		if !containsAny(topic, strings.TrimSpace(seed.StoryBase)) {
			return fmt.Errorf("%w: story topic must preserve story_base", ErrTopicContractViolation)
		}
		if !containsAny(topic, "視点", "役割", "語り", "語り直", "側", "記録係", "時代", "反転") {
			return fmt.Errorf("%w: story topic must include transform cue", ErrTopicContractViolation)
		}
	}
	return nil
}

func ValidateCommonTopic(topic string) error {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return fmt.Errorf("%w: empty topic", ErrTopicContractViolation)
	}
	if strings.ContainsAny(topic, "\r\n") {
		return fmt.Errorf("%w: topic must be one line", ErrTopicContractViolation)
	}
	n := utf8.RuneCountInString(topic)
	if n < 4 || n > 90 {
		return fmt.Errorf("%w: topic length out of range", ErrTopicContractViolation)
	}
	if strings.HasPrefix(topic, "{") || strings.HasPrefix(topic, "[") || strings.Contains(topic, "\":") {
		return fmt.Errorf("%w: topic looks like json", ErrTopicContractViolation)
	}
	for _, term := range CommonForbiddenMetaTerms {
		if ContainsTopicTerm(topic, term) {
			return fmt.Errorf("%w: topic leaks meta term %q", ErrTopicContractViolation, term)
		}
	}
	if topicHasPromptLeak(topic) || topicHasInternalReasoningLeak(topic) {
		return fmt.Errorf("%w: topic leaks prompt or reasoning", ErrTopicContractViolation)
	}
	return nil
}

func ValidateJudgeResult(judge TopicJudgeResult, candidates []TopicCandidate) (TopicCandidate, TopicJudgeScore, error) {
	return ValidateJudgeResultWithThresholds(judge, candidates, MinJudgeTotal, MinCategoryFit, MinSafety)
}

func ValidateJudgeResultWithThresholds(judge TopicJudgeResult, candidates []TopicCandidate, minTotal, minCategoryFit, minSafety int) (TopicCandidate, TopicJudgeScore, error) {
	if minTotal <= 0 {
		minTotal = MinJudgeTotal
	}
	if minCategoryFit <= 0 {
		minCategoryFit = MinCategoryFit
	}
	if minSafety <= 0 {
		minSafety = MinSafety
	}
	winnerTopic := strings.TrimSpace(judge.WinnerTopic)
	if winnerTopic == "" {
		return TopicCandidate{}, TopicJudgeScore{}, ErrTopicJudgeWinnerMissing
	}
	candidateByTopic := make(map[string]TopicCandidate, len(candidates))
	for _, candidate := range candidates {
		candidateByTopic[strings.TrimSpace(candidate.Topic)] = candidate
	}
	winner, ok := candidateByTopic[winnerTopic]
	if !ok {
		return TopicCandidate{}, TopicJudgeScore{}, ErrTopicJudgeWinnerMissing
	}
	for _, score := range judge.Scores {
		if strings.TrimSpace(score.Topic) != winnerTopic {
			continue
		}
		score = NormalizeJudgeScoreTotal(score)
		if score.Total < minTotal || score.CategoryFit < minCategoryFit || score.Safety < minSafety {
			return TopicCandidate{}, score, ErrTopicJudgeLowScore
		}
		return winner, score, nil
	}
	return TopicCandidate{}, TopicJudgeScore{}, ErrTopicJudgeWinnerMissing
}

func NormalizeJudgeScoreTotal(score TopicJudgeScore) TopicJudgeScore {
	score.Total = score.CategoryFit + score.Concreteness + score.Curiosity + score.ConversationPotential + score.AxisStrength + score.Novelty + score.Safety
	return score
}

func CheckRecentTopicSimilarity(topic string, recent []RecentTopic, threshold float64) error {
	if threshold <= 0 {
		threshold = RecentTopicSimilarityThreshold
	}
	normalized := NormalizeTopicForSimilarity(topic)
	if normalized == "" {
		return nil
	}
	for _, item := range recent {
		other := NormalizeTopicForSimilarity(item.Topic)
		if other == "" {
			continue
		}
		if normalized == other {
			return ErrRecentTopicExactDuplicate
		}
		if textSimilarity(normalized, other) >= threshold {
			return ErrRecentTopicTooSimilar
		}
	}
	return nil
}

func NormalizeTopicForSimilarity(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "　", " ")
	s = strings.ToLower(s)
	s = strings.Map(func(r rune) rune {
		switch r {
		case '。', '、', ',', '.', '！', '!', '？', '?', '「', '」', '『', '』', '(', ')', '（', '）', ':', '：':
			return ' '
		default:
			return r
		}
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

func ContainsTopicTerm(topic, term string) bool {
	topic = strings.ToLower(strings.TrimSpace(topic))
	term = strings.ToLower(strings.TrimSpace(term))
	return term != "" && strings.Contains(topic, term)
}

func containsAny(s string, needles ...string) bool {
	s = strings.ToLower(s)
	for _, needle := range needles {
		if strings.Contains(s, strings.ToLower(needle)) {
			return true
		}
	}
	return false
}

func topicContainsLooseMaterial(topic, material string) bool {
	topic = NormalizeTopicForSimilarity(topic)
	material = NormalizeTopicForSimilarity(material)
	if material == "" {
		return true
	}
	if strings.Contains(topic, material) {
		return true
	}
	for _, token := range strings.Fields(material) {
		if utf8.RuneCountInString(token) >= 3 && strings.Contains(topic, token) {
			return true
		}
	}
	return false
}

func textSimilarity(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 1
	}
	ag := runeNGrams(a, 2)
	bg := runeNGrams(b, 2)
	if len(ag) == 0 || len(bg) == 0 {
		if a == b {
			return 1
		}
		return 0
	}
	inter := 0
	i, j := 0, 0
	for i < len(ag) && j < len(bg) {
		if ag[i] == bg[j] {
			inter++
			i++
			j++
			continue
		}
		if ag[i] < bg[j] {
			i++
		} else {
			j++
		}
	}
	return (2.0 * float64(inter)) / float64(len(ag)+len(bg))
}

func runeNGrams(s string, n int) []string {
	r := []rune(s)
	if len(r) < n || n <= 0 {
		return nil
	}
	out := make([]string, 0, len(r)-n+1)
	for i := 0; i <= len(r)-n; i++ {
		out = append(out, string(r[i:i+n]))
	}
	sort.Strings(out)
	return out
}

func topicHasPromptLeak(s string) bool {
	lower := strings.ToLower(strings.TrimSpace(s))
	if lower == "" {
		return false
	}
	markers := []string{
		"<|",
		"|>",
		"channel>thought",
		"channel=analysis",
		"analysis to=",
		"assistant to=",
		"発言帰属ガード",
		"相手の発言として受ける",
		"相手の案を整理",
		"前に自分も触れた",
		"次に起きそうな場面",
		"直前の相手発言",
		"直前の自分",
		"1〜2文",
		"1-2文",
		"具体物・選択",
		"具体物・理由・問い",
		"条件・制約",
		"直前と違う入口",
		"直前と入口を変え",
		"どれか一つを足してください",
		"自然な日本語だけ",
		"文で返してください",
		"要件:",
		"要件：",
		"（話題:",
		"現在の状況",
		"目標:",
		"目標：",
		"制約事項",
		"会話の制約",
		"システムプロンプト",
	}
	for _, marker := range markers {
		if strings.Contains(lower, strings.ToLower(marker)) {
			return true
		}
	}
	return strings.Contains(lower, "発言として受け")
}

func topicHasInternalReasoningLeak(s string) bool {
	lower := strings.ToLower(strings.TrimSpace(s))
	if lower == "" {
		return false
	}
	markers := []string{
		"okay, let's",
		"okay, so",
		"ok, let's",
		"alright,",
		"let me",
		"the user is asking",
		"the user's question",
		"the user wants me",
		"looking at the",
		"the example response",
		"example responses",
		"possible response",
		"the task is",
		"the requirements",
		"the previous message",
		"the user's instruction",
		"but wait",
		"maybe better",
		"i need to",
		"i should",
		"should explain",
		"ユーザーは私",
		"私はmioとして",
		"私はshiroとして",
		"mioとして、",
		"shiroとして、",
		"必要がある",
		"遵守する必要",
		"以下の点",
		"会話の制約",
		"キャラクター（",
		"**現在の状況**",
		"**目標**",
		"1. **",
		"2. **",
		"好的",
		"我现在需要",
		"用户",
		"规则",
		"检查",
		"首先",
		"比如",
		"或者",
		"因为",
		"所以",
	}
	for _, marker := range markers {
		if strings.Contains(lower, strings.ToLower(marker)) {
			return true
		}
	}
	return false
}
