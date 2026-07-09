package idlechat

import (
	"fmt"
	"log"
	"math/rand"
	"strings"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
)

func (o *IdleChatOrchestrator) generateTopicFromChat(sessionID string, strategy TopicStrategy) (string, TopicStrategy) {
	movieMode := strategy == StrategyMovie
	recentTopics := o.getRecentTopics(12)
	recent := recentTopicRecords(recentTopics)

	var logInfo string
	var diagnosticTopic string
	seed, promptReady := buildTopicSeedForStrategy(strategy)

	switch strategy {
	case StrategySingleGenre:
		logInfo = fmt.Sprintf("single:%s", seed.Genre1)
		diagnosticTopic = diagnosticTopicForStrategy(strategy, []string{seed.Genre1}, "", "", topicAnchor{})

	case StrategyDoubleGenre:
		logInfo = fmt.Sprintf("double:%s,%s", seed.Genre1, seed.Genre2)
		diagnosticTopic = diagnosticTopicForStrategy(strategy, []string{seed.Genre1, seed.Genre2}, "", "", topicAnchor{})

	case StrategyExternalStimulus:
		source := "external_seed_unavailable"
		if seed.ExternalMaterial != nil {
			source = "Wikipedia:" + seed.ExternalMaterial.Title
		}
		logInfo = fmt.Sprintf("external:%s", source)
		diagnosticTopic = diagnosticTopicForStrategy(strategy, nil, source, "", topicAnchor{})

	case StrategyMovie:
		logInfo = fmt.Sprintf("movie:%s", seed.Genre1)
		diagnosticTopic = diagnosticTopicForStrategy(strategy, []string{seed.Genre1}, "", "", topicAnchor{})

	case StrategyNews:
		source := "news_seed_unavailable"
		if seed.News != nil {
			source = newsSeedSourceLabel(*seed.News)
		}
		logInfo = fmt.Sprintf("news:%s", source)
		diagnosticTopic = diagnosticTopicForStrategy(strategy, nil, source, "", topicAnchor{})

	default:
		log.Printf("[IdleChat] unsupported topic strategy: %s", strategy)
		return "未対応のお題カテゴリ: " + string(strategy), strategy
	}

	log.Printf("[IdleChat] Strategy: %s (%s)", strategy, logInfo)

	if !promptReady {
		diagnostic := normalizeIdleTopic(diagnosticTopic, movieMode)
		if diagnostic == "" {
			diagnostic = string(strategy) + "_seed_unavailable"
		}
		log.Printf("[IdleChat] Topic unavailable: strategy=%s reason=%s", strategy, logInfo)
		return diagnostic, strategy
	}

	o.mu.Lock()
	topicGenerationConfig := o.topicGenerationConfig
	o.mu.Unlock()
	if !topicGenerationConfig.Enabled {
		topicGenerationConfig.Enabled = true
	}
	if topicGenerationConfig.ProviderName == "" {
		topicGenerationConfig.ProviderName = "chatworker"
	}
	generator := NewTopicGenerator(o.providerForSpeaker("chatworker"), topicGenerationConfig)
	result, err := generator.GenerateInterestingTopic(o.idleRunContext(), seed.Category, seed, recent)
	if err == nil && result != nil {
		topic := normalizeIdleTopic(result.Topic, movieMode)
		result.Topic = topic
		o.mu.Lock()
		o.sessionContext = formatTopicGenerationContext(*result)
		copied := *result
		o.currentTopicResult = &copied
		o.mu.Unlock()
		log.Printf("[IdleChat] Topic: %s (%s)", topic, strategy)
		return topic, strategy
	}
	log.Printf("[IdleChat] topic generation failed: strategy=%s error=%v", strategy, err)

	diagnostic := normalizeIdleTopic(diagnosticTopic, movieMode)
	if diagnostic == "" {
		diagnostic = "TOPIC_GENERATION_FAILED error_code=" + errorCodeForTopicGeneration(err)
	}
	o.mu.Lock()
	o.currentTopicResult = &TopicGenerationResult{
		Topic:    diagnostic,
		Category: seed.Category,
		Strategy: string(strategy),
	}
	o.mu.Unlock()
	log.Printf("[IdleChat] Topic (diagnostic): %s", diagnostic)
	return diagnostic, strategy
}

func buildTopicSeedForStrategy(strategy TopicStrategy) (TopicSeed, bool) {
	switch strategy {
	case StrategySingleGenre:
		genres := pickRandom(genrePool, 1)
		return TopicSeed{Category: TopicCategorySingle, Genre1: genres[0]}, true
	case StrategyDoubleGenre:
		genres := pickRandom(genrePool, 2)
		return TopicSeed{Category: TopicCategoryDouble, Genre1: genres[0], Genre2: genres[1]}, true
	case StrategyExternalStimulus:
		cache := getDailyCache()
		genre := pickRandom(genrePool, 1)[0]
		return modulechat.SelectExternalTopicSeed(cache, rand.Int(), genre)
	case StrategyMovie:
		genres := pickRandom(genrePool, 1)
		return TopicSeed{Category: TopicCategoryMovie, Genre1: genres[0]}, true
	case StrategyNews:
		cache := getDailyCache()
		return modulechat.SelectNewsTopicSeed(cache, rand.Int())
	default:
		category, err := modulechat.NormalizeTopicCategory(string(strategy))
		if err != nil {
			return TopicSeed{}, false
		}
		return TopicSeed{Category: category}, true
	}
}

func recentTopicRecords(topics []string) []RecentTopic {
	return modulechat.RecentTopicRecords(topics)
}

func formatTopicGenerationContext(result TopicGenerationResult) string {
	var parts []string
	if axis := strings.TrimSpace(result.InterestingnessAxis); axis != "" {
		parts = append(parts, "【このtopicの面白さの軸】\n"+axis)
	}
	if hook := strings.TrimSpace(result.OpeningHook); hook != "" {
		parts = append(parts, "【最初に拾うべき面白さ】\n"+hook)
	}
	if avoid := strings.TrimSpace(result.Avoid); avoid != "" {
		parts = append(parts, "【避ける退屈な展開】\n"+avoid)
	}
	if len(parts) == 0 {
		return ""
	}
	return "IdleChat topic internal guidance:\n" + strings.Join(parts, "\n\n") + "\n\nこの内部メタは発話にそのまま出さない。"
}

func diagnosticTopicForStrategy(strategy TopicStrategy, genres []string, source string, seed string, anchor topicAnchor) string {
	anchorValue := strings.TrimSpace(anchor.Value)
	switch strategy {
	case StrategySingleGenre:
		if len(genres) >= 1 && strings.TrimSpace(genres[0]) != "" {
			if anchorValue != "" {
				return fmt.Sprintf("%sを%sの視点から考える", genres[0], anchorValue)
			}
			return fmt.Sprintf("%sで見落としがちな判断基準", genres[0])
		}
	case StrategyDoubleGenre:
		if len(genres) >= 2 && strings.TrimSpace(genres[0]) != "" && strings.TrimSpace(genres[1]) != "" {
			if anchorValue != "" {
				return fmt.Sprintf("%sと%sを%sでつなぐ", genres[0], genres[1], anchorValue)
			}
			return fmt.Sprintf("%sと%sに共通する設計思想", genres[0], genres[1])
		}
	case StrategyExternalStimulus:
		sourceName := source
		seedText := seed
		if strings.Contains(source, ":") {
			parts := strings.SplitN(source, ":", 2)
			sourceName = parts[0]
			seedText = parts[1]
		}
		if strings.TrimSpace(seedText) != "" {
			return fmt.Sprintf("「%s」から掘る盲点と前提", seedText)
		}
		if strings.TrimSpace(sourceName) != "" {
			return fmt.Sprintf("%s由来の刺激から掘る盲点と前提", sourceName)
		}
	case StrategyMovie:
		if len(genres) >= 1 && strings.TrimSpace(genres[0]) != "" {
			return formatMovieTopicPrompt(genres[0] + "の裏側")
		}
	case StrategyNews:
		sourceName := source
		seedText := seed
		if strings.Contains(source, ":") {
			parts := strings.SplitN(source, ":", 2)
			sourceName = parts[0]
			seedText = parts[1]
		}
		if strings.TrimSpace(seedText) != "" {
			return fmt.Sprintf("「%s」の背景と影響", seedText)
		}
		if strings.TrimSpace(sourceName) == "news_seed_unavailable" {
			return "news_seed_unavailable: ニュースシード未取得"
		}
	}
	return "予想外の切り口から考える論点"
}

func normalizeIdleTopic(raw string, movieMode bool) string {
	s := strings.TrimSpace(extractVisibleLLMAnswer(raw))
	if s == "" {
		return ""
	}
	if hasPromptLeak(s) || hasInternalReasoningLeak(s) {
		return ""
	}
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	replacers := []string{
		"話題:", "",
		"トピック:", "",
		"お題:", "",
		"話題：", "",
		"トピック：", "",
		"お題：", "",
		"\"", "",
	}
	s = strings.NewReplacer(replacers...).Replace(s)
	s = strings.TrimSpace(s)
	s = extractTopicTitleFromConversationalText(s)

	for _, marker := range []string{"、つまり、", "。つまり、", " つまり、", "っていうのは", "ってのは", "というのは"} {
		if idx := strings.Index(s, marker); idx > 0 {
			s = strings.TrimSpace(s[:idx])
			break
		}
	}
	for _, ending := range []string{
		"って、めちゃくちゃ面白いんじゃない？",
		"って、面白いんじゃない？",
		"って面白いんじゃない？",
		"ってどうだろう？",
		"じゃない？",
		"でしょうか？",
		"どうだろう？",
	} {
		s = strings.TrimSpace(strings.TrimSuffix(s, ending))
	}
	s = strings.TrimSpace(strings.TrimRight(s, "。！？!? "))
	s = multiSpaceForTopic(s)
	if s == "" || hasPromptLeak(s) || hasInternalReasoningLeak(s) || strings.HasPrefix(strings.TrimSpace(s), "<") || looksTruncatedIdleTopic(s) {
		return ""
	}
	if movieMode {
		return formatMovieTopicPrompt(s)
	}
	return strings.TrimSpace(s)
}

func looksTruncatedIdleTopic(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return true
	}
	if strings.HasSuffix(s, "、") || strings.HasSuffix(s, ",") {
		return true
	}
	for _, suffix := range []string{"そして", "また", "から", "ため", "との", "への", "取り", "取"} {
		if strings.HasSuffix(s, suffix) {
			return true
		}
	}
	if idx := strings.LastIndexAny(s, "、,"); idx >= 0 {
		tail := []rune(strings.TrimSpace(s[idx+len("、"):]))
		if len(tail) > 0 && len(tail) <= 2 {
			return true
		}
	}
	return false
}

func idleTopicGeneratorSystemPrompt() string {
	return `あなたはRenCrowのidleChat用お題生成器です。
キャラクターとして会話せず、感想・相づち・呼びかけ・絵文字を出さないでください。
出力はユーザーが指定した条件に合う「お題」本文だけを1行で返してください。`
}

func extractTopicTitleFromConversationalText(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.Trim(s, "「」『』\"' ")
	s = trimLeadingTopicReaction(s)
	for _, marker := range []string{"って組み合わせ", "という組み合わせ"} {
		if idx := strings.Index(s, marker); idx > 0 {
			return strings.TrimSpace(strings.Trim(s[:idx], "「」『』\"' "))
		}
	}
	for _, marker := range []string{"めっちゃ", "すごく", "なんか物語", "物語になりそう", "エモい"} {
		if idx := strings.Index(s, marker); idx > 0 {
			s = strings.TrimSpace(strings.TrimRight(s[:idx], "、。！？!? "))
			break
		}
	}
	return strings.TrimSpace(strings.Trim(s, "「」『』\"' "))
}

func trimLeadingTopicReaction(s string) string {
	for {
		trimmed := strings.TrimSpace(s)
		cut := -1
		for _, mark := range []string{"！", "!", "？", "?"} {
			if idx := strings.Index(trimmed, mark); idx >= 0 && utf8.RuneCountInString(trimmed[:idx]) < 40 {
				if cut == -1 || idx < cut {
					cut = idx
				}
			}
		}
		if cut < 0 {
			return trimmed
		}
		prefix := trimmed[:cut]
		if !containsAny(prefix, "えー", "うーん", "わあ", "おお", "なるほど", "たしかに") {
			return trimmed
		}
		s = strings.TrimSpace(trimmed[cut+len(string([]rune(trimmed[cut:])[0])):])
	}
}

func formatMovieTopicPrompt(raw string) string {
	title := strings.TrimSpace(raw)
	if title == "" {
		return ""
	}
	for {
		switch {
		case strings.HasPrefix(title, "「") && strings.HasSuffix(title, "」"):
			title = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(title, "「"), "」"))
			continue
		case strings.HasPrefix(title, "『") && strings.HasSuffix(title, "』"):
			title = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(title, "『"), "』"))
			continue
		}
		break
	}
	if idx := strings.Index(title, "ってどんな映画"); idx >= 0 {
		title = title[:idx]
	}
	title = strings.TrimSpace(strings.Trim(title, "「」『』\"'"))
	title = multiSpaceForTopic(title)
	if title == "" {
		return ""
	}
	if utf8.RuneCountInString(title) > 24 {
		title = truncate(title, 24)
		title = strings.TrimSpace(strings.TrimSuffix(title, "..."))
	}
	return fmt.Sprintf("「%s」ってどんな映画？", title)
}

func isMovieTopicPrompt(topic string) bool {
	s := strings.TrimSpace(topic)
	return strings.HasPrefix(s, "「") && strings.Contains(s, "」ってどんな映画")
}

func multiSpaceForTopic(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func collectLatestSessionSnippets(entries []session.ConversationEntry, match func(domaintransport.Message) bool, max int) []string {
	latestSessionID := ""
	for i := len(entries) - 1; i >= 0; i-- {
		m := entries[i].Message
		if match(m) && strings.TrimSpace(m.SessionID) != "" {
			latestSessionID = m.SessionID
			break
		}
	}
	if latestSessionID == "" {
		return nil
	}

	snippets := make([]string, 0, max)
	for i := len(entries) - 1; i >= 0 && len(snippets) < max; i-- {
		m := entries[i].Message
		if m.SessionID == latestSessionID && match(m) {
			snippets = append(snippets, truncate(m.Content, 80))
		}
	}
	return snippets
}

func isIdleSession(sessionID string) bool {
	return strings.HasPrefix(strings.ToLower(sessionID), "idle-")
}

func isIdleMessage(m domaintransport.Message) bool {
	return m.Type == domaintransport.MessageTypeIdleChat || isIdleSession(m.SessionID)
}

func isWorkerMessage(m domaintransport.Message) bool {
	return strings.EqualFold(m.From, "shiro") || strings.EqualFold(m.To, "shiro")
}

func isUserMessage(m domaintransport.Message) bool {
	return strings.EqualFold(m.From, "user")
}
