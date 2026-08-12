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
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
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
func (m *MioAgent) DecideAction(ctx context.Context, t task.Task) (routing.Decision, error) {
	// 優先度1: 明示コマンド
	if explicitRoute := m.parseExplicitCommand(t.UserMessage()); explicitRoute != "" {
		return routing.NewDecisionWithEvidence(explicitRoute, 1.0, "Explicit command",
			routing.DecisionEvidence{
				Source:     routing.EvidenceSourceExplicitCommand,
				Matched:    true,
				Route:      explicitRoute,
				Confidence: 1.0,
				Reason:     "explicit command matched",
			},
		), nil
	}
	// Indexed person-related catalog intents must stay on Mio's CHAT path so
	// generic creative/knowledge routing cannot bypass the bounded local Tool
	// contract and invent titles. Explicit slash commands still take priority.
	if _, matched := parseMioPersonRelatedCatalogLookup(t.UserMessage()); matched {
		return routing.NewDecisionWithEvidence(routing.RouteCHAT, 1.0, "Indexed person-related catalog intent",
			routing.DecisionEvidence{
				Source:     routing.EvidenceSourceRuleDictionary,
				Matched:    true,
				Route:      routing.RouteCHAT,
				Confidence: 1.0,
				Reason:     "bounded indexed person-related catalog intent matched",
			},
		), nil
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
		return routing.NewDecisionWithEvidence(route, confidence, "Rule dictionary match", evidence...), nil
	}
	evidence = append(evidence, routing.DecisionEvidence{
		Source:  routing.EvidenceSourceRuleDictionary,
		Matched: false,
		Reason:  "no rule dictionary match",
	})

	// 優先度3: 分類器
	if m.classifier != nil {
		classified, err := m.classifier.Classify(ctx, t)
		if err == nil && classified.Route != "" && classified.Confidence >= 0.7 {
			classified.Evidence = append(evidence, classifierEvidence(classified)...)
			return classified, nil
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
	return routing.NewDecisionWithEvidence(routing.RouteCHAT, 0.7, "No rule match, default to CHAT", evidence...), nil
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
func (m *MioAgent) Chat(ctx context.Context, t task.Task) (string, error) {
	userMessage := t.UserMessage()
	currentSpeaker := conversationSpeakerForViewerRecipient(t.ViewerRecipient(), conversation.SpeakerMio)

	// === v5.1: ConversationEngine による RecallPack 生成 ===
	var messages []llm.Message
	var recallPack *conversation.RecallPack
	if m.conversationEngine != nil {
		var err error
		recallPack, err = m.conversationEngine.BeginTurn(ctx, t.ChatID(), userMessage)
		if err != nil {
			fmt.Printf("WARN: BeginTurn failed: %v\n", err)
		}
		if recallPack != nil {
			filtered := recallPack.FilterForRole("chat")
			filtered = filtered.WithoutPersonaSystemPrompt()
			recallPack = &filtered
			if err := recordRecallTrace(ctx, m.conversationEngine, t.ChatID(), t.JobID().String(), string(currentSpeaker), filtered); err != nil {
				log.Printf("[Mio] RecordRecallTrace failed: %v", err)
			}
			// RecallPack からプロンプトメッセージを生成（system prompt + 過去文脈 + 会話履歴）
			messages = appendSharedConversationContinuityPrompt(messages, recallPack)
			messages = append(messages, recallPack.ToPromptMessages()...)
		}
	}
	if response, ok := exactSharedRecallAnswer(userMessage, recallPack); ok {
		if onToken := llm.StreamCallbackFromContext(ctx); onToken != nil {
			onToken(response)
		}
		m.rememberExpression(response)
		if m.conversationEngine != nil {
			if err := endConversationTurnAs(ctx, m.conversationEngine, t.ChatID(), userMessage, response, currentSpeaker); err != nil {
				fmt.Printf("WARN: EndTurn failed: %v\n", err)
			}
		}
		return response, nil
	}
	userMemoryInRecall := recallPack != nil && (len(recallPack.UserProfile.Facts) > 0 || len(recallPack.UserProfile.Preferences) > 0)
	if !userMemoryInRecall {
		if userMemoryPrompt, err := m.userMemoryPrompt(ctx); err != nil {
			log.Printf("[Mio] user memory recall failed: %v", err)
		} else if userMemoryPrompt != "" {
			messages = append(messages, llm.Message{
				Role:    "system",
				Content: userMemoryPrompt,
				Type:    llm.PromptContextRecall,
			})
		}
	}

	// ペルソナ調整意図を検出 → 自己編集
	if m.personaEditor != nil && detectPersonaEditIntent(userMessage) {
		result, err := m.editPersona(ctx, userMessage)
		if err != nil {
			log.Printf("[Mio] Persona edit failed: %v", err)
			// フォールバック: 通常の会話として処理を続行
		} else {
			// EndTurn で会話履歴に記録
			if m.conversationEngine != nil {
				if err := endConversationTurnAs(ctx, m.conversationEngine, t.ChatID(), userMessage, result, currentSpeaker); err != nil {
					fmt.Printf("WARN: EndTurn failed: %v\n", err)
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

	latestOther := ""
	attributionPrompt := ""
	if recallPack != nil {
		selfCtx, otherCtx := buildAttributionContextsFromShort(recallPack.ShortContext, currentSpeaker, 5)
		latestOther = latestOtherMessageFromShort(recallPack.ShortContext, currentSpeaker)
		attributionPrompt = fmt.Sprintf(
			"発言帰属ガード:\n- 現在のAgentは%s。\n- 自分の過去発言(要約): %s\n- 他者の発言(要約): %s\n要件: 他者の発言を自分の新規アイデアとして扱わない。既出アイデアに触れる場合は発言者を明示する。",
			currentSpeaker,
			strings.Join(selfCtx, " / "),
			strings.Join(otherCtx, " / "),
		)
	}

	if m.recentContext != nil {
		if glossaryContext, err := m.recentContext(ctx, 6); err != nil {
			log.Printf("[Mio] recent context failed: %v", err)
		} else if strings.TrimSpace(glossaryContext) != "" {
			messages = append(messages, llm.Message{
				Role:    "system",
				Content: glossaryContext + "\n最近の語彙は、断定せず軽い補足として扱ってください。",
				Type:    llm.PromptContextRecall,
			})
		}
	}
	if brief, ok := dailyNewsBriefFromContext(ctx); ok {
		messages = append(messages, llm.Message{
			Role:    "system",
			Content: dailyNewsBriefSystemPrompt(brief),
			Type:    llm.PromptContextRecall,
		})
	}
	if prompt := viewerRecipientSystemPrompt(t.ViewerRecipient(), userMessage); prompt != "" {
		messages = append(messages, llm.Message{Role: "system", Content: prompt, Type: llm.PromptContextVariable, Metadata: map[string]string{"runtime_context_kind": "recipient_contract"}})
	}
	if prompt := m.runtimeMioPromptContext(t); prompt != "" {
		messages = append(messages, llm.Message{Role: "system", Content: prompt, Type: llm.PromptContextVariable, Metadata: map[string]string{"runtime_context_kind": "runtime_state"}})
	}
	if attributionPrompt != "" {
		messages = append(messages, llm.Message{Role: "system", Content: attributionPrompt, Type: llm.PromptContextVariable, Metadata: map[string]string{"runtime_context_kind": "attribution_state"}})
	}

	// ユーザーメッセージを最後に追加
	currentUserMessage := userMessageWithAttachments(userMessage, t.Attachments())
	messages = assemblePromptContext(
		m.systemPromptForViewerRecipient(t.ViewerRecipient()),
		m.stablePromptContext(t),
		messages,
		currentUserMessage,
	)

	req := m.generationRequest(messages, filterLeadingAgentSelfLabel(llm.StreamCallbackFromContext(ctx), currentSpeaker))

	resp, err := m.llmProvider.Generate(ctx, req)
	if err != nil {
		return "", err
	}

	response := stripLeadingAgentSelfLabel(resp.Content, currentSpeaker)
	finalGeneration := resp
	if violatesAttributionInChat(response, latestOther) {
		retryMessages := append([]llm.Message{}, messages...)
		retryMessages = append(retryMessages, llm.Message{
			Role:    "user",
			Content: "直前の返答は発言帰属が曖昧です。誰のアイデアかを明示して1回だけ言い直してください。",
		})
		retryResp, retryErr := m.llmProvider.Generate(ctx, m.generationRequest(retryMessages, filterLeadingAgentSelfLabel(llm.StreamCallbackFromContext(ctx), currentSpeaker)))
		if retryErr == nil && strings.TrimSpace(retryResp.Content) != "" {
			response = stripLeadingAgentSelfLabel(retryResp.Content, currentSpeaker)
			finalGeneration = retryResp
		}
	}
	response = enforceExactSharedRecallAnswer(userMessage, response, recallPack)
	response = stripLeadingAgentSelfLabel(response, currentSpeaker)
	m.rememberExpression(response)
	if onMetrics := llm.GenerationMetricsCallbackFromContext(ctx); onMetrics != nil && (finalGeneration.TokensUsed > 0 || finalGeneration.TokensPerSecond > 0) {
		onMetrics(llm.GenerationMetrics{
			CompletionTokens: finalGeneration.TokensUsed,
			TokensPerSecond:  finalGeneration.TokensPerSecond,
		})
	}

	// === v5.1: EndTurn（Store） ===
	if m.conversationEngine != nil {
		if err := endConversationTurnAs(ctx, m.conversationEngine, t.ChatID(), userMessage, response, currentSpeaker); err != nil {
			fmt.Printf("WARN: EndTurn failed: %v\n", err)
		}
	}
	if err := m.captureUserMemoryCandidate(ctx, t); err != nil {
		log.Printf("[Mio] user memory candidate capture failed: %v", err)
	}

	return response, nil
}

// stripLeadingAgentSelfLabel removes a model-generated speaker label from the
// user-visible body. Speaker identity remains available through event and
// conversation metadata, so duplicating it in text is unnecessary.
func stripLeadingAgentSelfLabel(content string, speaker conversation.Speaker) string {
	trimmed := strings.TrimSpace(content)
	name := strings.TrimSpace(string(speaker))
	if trimmed == "" || name == "" {
		return trimmed
	}
	label := "[" + name + "]"
	if len(trimmed) < len(label) || !strings.EqualFold(trimmed[:len(label)], label) {
		return trimmed
	}
	return strings.TrimSpace(strings.TrimLeft(trimmed[len(label):], ":："))
}

func filterLeadingAgentSelfLabel(next llm.StreamCallback, speaker conversation.Speaker) llm.StreamCallback {
	if next == nil {
		return nil
	}
	label := "[" + strings.TrimSpace(string(speaker)) + "]"
	if label == "[]" {
		return next
	}
	var pending string
	decided := false
	strippingSeparator := false
	return func(token string) {
		if decided {
			next(token)
			return
		}
		if strippingSeparator {
			candidate := strings.TrimLeft(token, " \t\r\n:：")
			if candidate == "" {
				return
			}
			decided = true
			next(candidate)
			return
		}
		pending += token
		candidate := strings.TrimLeft(pending, " \t\r\n")
		if len(candidate) < len(label) && strings.HasPrefix(strings.ToLower(label), strings.ToLower(candidate)) {
			return
		}
		if len(candidate) >= len(label) && strings.EqualFold(candidate[:len(label)], label) {
			candidate = strings.TrimLeft(candidate[len(label):], " \t\r\n:：")
			if candidate == "" {
				pending = ""
				strippingSeparator = true
				return
			}
		}
		decided = true
		if candidate != "" {
			next(candidate)
		}
		pending = ""
	}
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
