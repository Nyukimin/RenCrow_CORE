package agent

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
)

// KBManager はKB保存用のインターフェース（Phase 4.2）
type KBManager interface {
	SearchKB(ctx context.Context, domain string, query string, topK int) ([]*conversation.Document, error)
	SaveWebSearchToKB(ctx context.Context, domain string, query string, results []WebSearchResult) error
}

type SearchCacheManager interface {
	GetFreshWebSearchCache(ctx context.Context, query string) ([]WebSearchResult, bool, error)
	SaveWebSearchCache(ctx context.Context, query string, results []WebSearchResult, ttl time.Duration) error
}

type UserMemoryManager interface {
	CreateUserMemory(ctx context.Context, input domainmemory.CreateUserMemoryInput) (*domainmemory.UserMemory, error)
	ListUserMemories(ctx context.Context, userID string, state string, includeInactive bool, limit int) ([]domainmemory.UserMemory, error)
	UpdateUserMemoryState(ctx context.Context, id string, state string, reason string) (*domainmemory.UserMemory, error)
	ForgetUserMemory(ctx context.Context, id string, reason string) (*domainmemory.UserMemory, error)
	SupersedeUserMemory(ctx context.Context, oldID string, newID string, reason string) (*domainmemory.UserMemory, error)
}

// PersonaEditor はペルソナファイルの読み書きを抽象化する
type PersonaEditor interface {
	ReadPersona() (string, error)
	WritePersona(content string) error
}

// WebSearchResult はWeb検索結果（ToolRunner の GoogleSearchItem と互換）
type WebSearchResult struct {
	Title   string `json:"title"`
	Link    string `json:"link"`
	Snippet string `json:"snippet"`
}

// MioAgent は Chat（会話・意思決定）を担当するエンティティ
type MioAgent struct {
	llmProvider            llm.LLMProvider
	classifier             Classifier
	ruleDictionary         RuleDictionary
	toolRunner             ToolRunner
	personCatalogCollector ToolRunner
	mcpClient              MCPClient
	conversationEngine     conversation.ConversationEngine // v5.1: 会話エンジン（nilを許容）
	kbManager              KBManager                       // Phase 4.2: KB自動保存用（nilを許容）
	searchCacheManager     SearchCacheManager              // L1 Search Cache連携（nilを許容）
	userMemoryManager      UserMemoryManager               // Memory v0.1: user:<uid> 操作用（nilを許容）
	personaEditor          PersonaEditor                   // ペルソナ自己編集用（nilを許容）
	recentContext          func(context.Context, int) (string, error)
	systemPrompt           string
	viewerPrompts          map[string]string
	runtimeContextProvider RuntimeContextProvider
	stableRuntimeContexts  map[string]string
	expressionHistoryMu    sync.RWMutex
	expressionHistory      MioExpressionHistory
	generation             MioGenerationOptions
}

// NewMioAgent は新しいMioAgentを作成
func NewMioAgent(
	llmProvider llm.LLMProvider,
	classifier Classifier,
	ruleDictionary RuleDictionary,
	toolRunner ToolRunner,
	mcpClient MCPClient,
	conversationEngine conversation.ConversationEngine, // v5.1: ConversationEngine（nilを許容）
) *MioAgent {
	return &MioAgent{
		llmProvider:        llmProvider,
		classifier:         classifier,
		ruleDictionary:     ruleDictionary,
		toolRunner:         toolRunner,
		mcpClient:          mcpClient,
		conversationEngine: conversationEngine,
		kbManager:          nil, // WithKBManager() でセット
		searchCacheManager: nil, // WithSearchCacheManager() でセット
		userMemoryManager:  nil, // WithUserMemoryManager() でセット
		generation:         defaultMioGenerationOptions(),
		expressionHistory:  MioExpressionHistory{},
	}
}

// DecideAction はMioによる委譲判断（4段階優先順位）
func (m *MioAgent) DecideAction(ctx context.Context, t conversation.TurnInput) (routing.Decision, error) {
	decision, _, err := m.decideAction(ctx, t, false)
	return decision, err
}

func (m *MioAgent) decideAction(ctx context.Context, t conversation.TurnInput, allowCombined bool) (routing.Decision, *MioChatDraft, error) {
	// 優先度1: 明示コマンド
	if explicitRoute := m.parseExplicitCommand(t.MessageText()); explicitRoute != "" {
		return routing.NewDecisionWithEvidence(explicitRoute, 1.0, "Explicit command",
			routing.DecisionEvidence{
				Source:     routing.EvidenceSourceExplicitCommand,
				Matched:    true,
				Route:      explicitRoute,
				Confidence: 1.0,
				Reason:     "explicit command matched",
			},
		), nil, nil
	}
	// Indexed person-related catalog intents must stay on Mio's CHAT path so
	// generic creative/knowledge routing cannot bypass the bounded local Tool
	// contract and invent titles. Explicit slash commands still take priority.
	if _, matched := parseMioPersonRelatedCatalogLookup(t.MessageText()); matched {
		return routing.NewDecisionWithEvidence(routing.RouteCHAT, 1.0, "Indexed person-related catalog intent",
			routing.DecisionEvidence{
				Source:     routing.EvidenceSourceRuleDictionary,
				Matched:    true,
				Route:      routing.RouteCHAT,
				Confidence: 1.0,
				Reason:     "bounded indexed person-related catalog intent matched",
			},
		), nil, nil
	}
	evidence := []routing.DecisionEvidence{
		{
			Source:  routing.EvidenceSourceExplicitCommand,
			Matched: false,
			Reason:  "no explicit command matched",
		},
	}

	// 優先度2: ルール辞書
	if route, confidence, matched := m.ruleDictionary.Match(t); matched {
		evidence = append(evidence, routing.DecisionEvidence{
			Source:     routing.EvidenceSourceRuleDictionary,
			Matched:    true,
			Route:      route,
			Confidence: confidence,
			Reason:     "rule dictionary matched",
		})
		return routing.NewDecisionWithEvidence(route, confidence, "Rule dictionary match", evidence...), nil, nil
	}
	evidence = append(evidence, routing.DecisionEvidence{
		Source:  routing.EvidenceSourceRuleDictionary,
		Matched: false,
		Reason:  "no rule dictionary match",
	})

	if allowCombined && m.canUseCombinedChatDraft(ctx, t) {
		if _, ok := m.classifier.(RouteReplyClassifier); ok {
			return m.classifyChatWithDraft(ctx, t, evidence)
		}
	}

	// 優先度3: 分類器
	if m.classifier != nil {
		turntiming.Mark(ctx, turntiming.PointClassificationStarted)
		classified, err := m.classifier.Classify(ctx, t)
		turntiming.Mark(ctx, turntiming.PointClassificationEnded)
		if err == nil && classified.Route != "" && classified.Confidence >= 0.7 {
			classified.Evidence = append(evidence, classifierEvidence(classified)...)
			return classified, nil, nil
		}
		reason := "classifier returned low confidence"
		if err != nil {
			reason = fmt.Sprintf("classifier failed: %v", err)
		} else if classified.Route == "" {
			reason = "classifier returned empty route"
		}
		evidence = append(evidence, routing.DecisionEvidence{
			Source:     routing.EvidenceSourceClassifier,
			Matched:    false,
			Route:      classified.Route,
			Confidence: classified.Confidence,
			Reason:     reason,
		})
	} else {
		evidence = append(evidence, routing.DecisionEvidence{
			Source:  routing.EvidenceSourceClassifier,
			Matched: false,
			Reason:  "classifier unavailable",
		})
	}
	evidence = append(evidence, routing.DecisionEvidence{
		Source:     routing.EvidenceSourceSafeFallback,
		Matched:    true,
		Route:      routing.RouteCHAT,
		Confidence: 0.7,
		Reason:     "default to CHAT",
	})

	// 優先度4: 安全側フォールバック（CHAT）
	// 技術的キーワードがルール辞書で捕捉されなかったメッセージは会話として処理
	return routing.NewDecisionWithEvidence(routing.RouteCHAT, 0.7, "No rule match, default to CHAT", evidence...), nil, nil
}

func classifierEvidence(decision routing.Decision) []routing.DecisionEvidence {
	if len(decision.Evidence) > 0 {
		return decision.Evidence
	}
	return []routing.DecisionEvidence{{
		Source:     routing.EvidenceSourceClassifier,
		Matched:    true,
		Route:      decision.Route,
		Confidence: decision.Confidence,
		Reason:     decision.Reason,
	}}
}

// Chat は会話を実行（v5.1: ConversationEngine + 明示指示時のみWeb検索）
func (m *MioAgent) Chat(ctx context.Context, t conversation.TurnInput) (string, error) {
	turntiming.Mark(ctx, turntiming.PointAgentInvocationStarted)
	userMessage := t.MessageText()
	currentSpeaker := conversationSpeakerForViewerRecipient(t.ViewerRecipient(), conversation.SpeakerMio)
	speechPlanMode := SpeechPlanCallbackFromContext(ctx) != nil

	// === v5.1: ConversationEngine による RecallPack 生成 ===
	recallPack, messages := m.beginChatRecall(ctx, t, userMessage)
	if response, ok := exactSharedRecallAnswer(userMessage, recallPack); ok {
		if onToken := llm.StreamCallbackFromContext(ctx); onToken != nil {
			onToken(response)
		}
		m.rememberExpression(response)
		if m.conversationEngine != nil {
			if err := commitConversationTurn(ctx, m.conversationEngine, t, t.SessionID(), userMessage, response, currentSpeaker, recallPack); err != nil {
				return response, err
			}
		}
		return response, nil
	}
	turntiming.Mark(ctx, turntiming.PointPromptPreparationStarted)
	if m.conversationEngine == nil {
		if m.userMemoryManager != nil {
			turntiming.Mark(ctx, turntiming.PointUserMemoryStarted)
		}
		if userMemoryPrompt, err := m.userMemoryPrompt(ctx); err != nil {
			log.Printf("[Mio] user memory recall failed: %v", err)
		} else if userMemoryPrompt != "" {
			messages = append(messages, llm.Message{
				Role:    "system",
				Content: userMemoryPrompt,
				Type:    llm.PromptContextRecall,
			})
		}
		if m.userMemoryManager != nil {
			turntiming.Mark(ctx, turntiming.PointUserMemoryEnded)
		}
	}

	// ペルソナ調整意図を検出 → 自己編集
	if m.personaEditor != nil && detectPersonaEditIntent(userMessage) {
		result, err := m.editPersona(ctx, userMessage)
		if err != nil {
			log.Printf("[Mio] Persona edit failed: %v", err)
			// フォールバック: 通常の会話として処理を続行
		} else {
			// Canonical atomic commit records the user and Agent messages together.
			if m.conversationEngine != nil {
				if err := commitConversationTurn(ctx, m.conversationEngine, t, t.SessionID(), userMessage, result, currentSpeaker, recallPack); err != nil {
					return result, err
				}
			}
			m.rememberExpression(result)
			return result, nil
		}
	}

	// Google API quota保護のため、通常は明示的な検索/調査指示がある時だけ使う。
	// 朝刊キャッシュ未準備時のニュース収集は、Mioではなく専用Workerが担当し、
	// 収集済みの構造化結果だけをcontext経由で受け取る。
	movieCatalogMatched := false
	if lookup, ok := parseMioMovieCatalogLookup(userMessage); ok {
		movieCatalogMatched = true
		messages = append(messages, m.movieCatalogLookupContext(ctx, lookup))
	}
	personRelatedCatalogMatched := false
	if !movieCatalogMatched {
		if lookup, ok := parseMioPersonRelatedCatalogLookup(userMessage); ok {
			personRelatedCatalogMatched = true
			messages = append(messages, m.personRelatedCatalogLookupContext(ctx, lookup))
		}
	}
	dataToolMatched := false
	if !movieCatalogMatched && !personRelatedCatalogMatched {
		if lookup, ok := parseMioDataToolLookup(userMessage); ok {
			dataToolMatched = true
			messages = append(messages, m.dataToolLookupContext(ctx, lookup))
		}
	}
	searchQuery := userMessage
	needsSearch := !movieCatalogMatched && !personRelatedCatalogMatched && !dataToolMatched && needsWebSearch(userMessage)

	// Web検索を実行してコンテキストに追加
	if needsSearch && m.toolRunner != nil {
		searchResult, err := m.executeWebSearch(ctx, searchQuery)
		if err == nil && searchResult != "" {
			messages = append(messages, llm.Message{
				Role:    "system",
				Content: "以下はWeb検索の結果です。この情報を参考にして質問に答えてください:\n\n" + searchResult,
				Type:    llm.PromptContextRecall,
			})
		}
	}

	messages, latestOther := m.assembleChatPrompt(ctx, t, userMessage, messages, recallPack, speechPlanMode)
	turntiming.Mark(ctx, turntiming.PointPromptPreparationEnded)

	var streamCallback llm.StreamCallback
	if !speechPlanMode {
		streamCallback = llm.StreamCallbackFromContext(ctx)
	}
	onToken, flushLabels := conversation.NewSpeakerLabelStreamFilter(streamCallback, currentSpeaker)
	req := m.generationRequest(messages, onToken)
	if speechPlanMode {
		req.ResponseFormat = llm.ResponseFormatJSONObject
		// Structured JSON output is non-streaming by contract. Do not expose
		// provider tokens to the legacy TTS or Avatar stream hooks.
		req.OnToken = nil
	}
	turntiming.Mark(ctx, turntiming.PointGenerationRequestReady)

	generationCtx := turntiming.WithBodyGenerationContext(ctx)
	turntiming.Mark(ctx, turntiming.PointGenerationProviderStart)
	resp, generationDuration, err := generateSpeechPlanResponse(generationCtx, m.llmProvider, req, speechPlanMode)
	if err != nil {
		turntiming.Mark(ctx, turntiming.PointGenerationProviderEnd)
		return "", err
	}
	flushLabels()

	planResult, structuredSpeechPlan := parseSpeechPlan(ctx, resp.Content, generationDuration)
	response := conversation.StripLeadingSpeakerLabels(resp.Content, currentSpeaker)
	if structuredSpeechPlan {
		response = planResult.DisplayText
	}
	finalGeneration := resp
	if !structuredSpeechPlan && violatesAttributionInChat(response, latestOther) {
		retryMessages := append([]llm.Message{}, messages...)
		retryMessages = append(retryMessages, llm.Message{
			Role:    "user",
			Content: "直前の返答は発言帰属が曖昧です。誰のアイデアかを明示して1回だけ言い直してください。",
		})
		retryOnToken, flushRetryLabels := conversation.NewSpeakerLabelStreamFilter(llm.StreamCallbackFromContext(ctx), currentSpeaker)
		retryResp, retryErr := m.llmProvider.Generate(generationCtx, m.generationRequest(retryMessages, retryOnToken))
		if retryErr == nil {
			flushRetryLabels()
		}
		if retryErr == nil && strings.TrimSpace(retryResp.Content) != "" {
			response = conversation.StripLeadingSpeakerLabels(retryResp.Content, currentSpeaker)
			finalGeneration = retryResp
		}
	}
	turntiming.Mark(ctx, turntiming.PointGenerationProviderEnd)
	if !structuredSpeechPlan {
		response = enforceExactSharedRecallAnswer(userMessage, response, recallPack)
		response = conversation.StripLeadingSpeakerLabels(response, currentSpeaker)
	}
	m.rememberExpression(response)
	reportGenerationMetrics(ctx, finalGeneration)

	// Canonical atomic EndTurn commit.
	if m.conversationEngine != nil {
		if err := commitConversationTurn(ctx, m.conversationEngine, t, t.SessionID(), userMessage, response, currentSpeaker, recallPack); err != nil {
			return response, err
		}
	}

	return response, nil
}

func (m *MioAgent) systemPromptForViewerRecipient(recipient string) string {
	recipient = strings.ToLower(strings.TrimSpace(recipient))
	if recipient != "" && recipient != "mio" {
		if prompt := strings.TrimSpace(m.viewerPrompts[recipient]); prompt != "" {
			return prompt
		}
	}
	return m.systemPrompt
}

func viewerRecipientSystemPrompt(recipient, userMessage string) string {
	recipient = strings.ToLower(strings.TrimSpace(recipient))
	if recipient == "" || recipient == "mio" {
		return tokenEchoGuardPrompt(userMessage)
	}
	var role string
	switch recipient {
	case "shiro":
		role = "Shiro. Reply directly to the user in a calm, practical style. This is normal CHAT, not an OPS execution route."
	case "kuro":
		role = "Kuro. Reply directly to the user in a logical and analytical style."
	case "midori":
		role = "Midori. Reply directly to the user in a creative, idea-expanding style."
	default:
		return tokenEchoGuardPrompt(userMessage)
	}
	prompt := "Viewer recipient contract: requested_to=" + recipient + ". You are not replying as Mio; reply as " + role + " Treat your speaker identity as " + recipient + "."
	if guard := tokenEchoGuardPrompt(userMessage); guard != "" {
		prompt += "\n" + guard
	}
	return prompt
}

func tokenEchoGuardPrompt(userMessage string) string {
	if !strings.Contains(userMessage, "合言葉") && !strings.Contains(userMessage, "RC_") {
		return ""
	}
	return "Token contract: if the current user message contains a passphrase or RC_ token, include exactly the current input token once. Do not reuse older tokens from conversation context."
}
