package agent

import (
	"context"
	"fmt"
	"log"
	"strings"
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
	llmProvider        llm.LLMProvider
	classifier         Classifier
	ruleDictionary     RuleDictionary
	toolRunner         ToolRunner
	mcpClient          MCPClient
	conversationEngine conversation.ConversationEngine // v5.1: 会話エンジン（nilを許容）
	kbManager          KBManager                       // Phase 4.2: KB自動保存用（nilを許容）
	searchCacheManager SearchCacheManager              // L1 Search Cache連携（nilを許容）
	userMemoryManager  UserMemoryManager               // Memory v0.1: user:<uid> 操作用（nilを許容）
	personaEditor      PersonaEditor                   // ペルソナ自己編集用（nilを許容）
	recentContext      func(context.Context, int) (string, error)
	systemPrompt       string
	viewerPrompts      map[string]string
	generation         MioGenerationOptions
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

	// === v5.1: ConversationEngine による RecallPack 生成 ===
	var messages []llm.Message
	if systemPrompt := m.systemPromptForViewerRecipient(t.ViewerRecipient()); systemPrompt != "" {
		messages = append(messages, llm.Message{Role: "system", Content: systemPrompt})
	}
	var recallPack *conversation.RecallPack
	if m.conversationEngine != nil {
		var err error
		recallPack, err = m.conversationEngine.BeginTurn(ctx, t.ChatID(), userMessage)
		if err != nil {
			fmt.Printf("WARN: BeginTurn failed: %v\n", err)
		}
		if recallPack != nil {
			filtered := recallPack.FilterForRole("chat")
			recallPack = &filtered
			if err := recordRecallTrace(ctx, m.conversationEngine, t.ChatID(), t.JobID().String(), "chat", filtered); err != nil {
				log.Printf("[Mio] RecordRecallTrace failed: %v", err)
			}
			// RecallPack からプロンプトメッセージを生成（system prompt + 過去文脈 + 会話履歴）
			messages = append(messages, recallPack.ToPromptMessages()...)
		}
	}
	if userMemoryPrompt, err := m.userMemoryPrompt(ctx); err != nil {
		log.Printf("[Mio] user memory recall failed: %v", err)
	} else if userMemoryPrompt != "" {
		messages = append(messages, llm.Message{
			Role:    "system",
			Content: userMemoryPrompt,
		})
	}
	if prompt := viewerRecipientSystemPrompt(t.ViewerRecipient(), userMessage); prompt != "" {
		messages = append(messages, llm.Message{
			Role:    "system",
			Content: prompt,
		})
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
				if err := m.conversationEngine.EndTurn(ctx, t.ChatID(), userMessage, result); err != nil {
					fmt.Printf("WARN: EndTurn failed: %v\n", err)
				}
			}
			return result, nil
		}
	}

	// Google API quota保護のため、Web検索は明示的な検索/調査指示がある時だけ使う。
	needsSearch := needsWebSearch(userMessage)

	// Web検索を実行してコンテキストに追加
	if needsSearch && m.toolRunner != nil {
		searchResult, err := m.executeWebSearch(ctx, userMessage)
		if err == nil && searchResult != "" {
			messages = append(messages, llm.Message{
				Role:    "system",
				Content: "以下はWeb検索の結果です。この情報を参考にして質問に答えてください:\n\n" + searchResult,
			})
		}
	}

	latestOther := ""
	if recallPack != nil {
		selfCtx, otherCtx := buildAttributionContextsFromShort(recallPack.ShortContext, conversation.SpeakerMio, 5)
		latestOther = latestOtherMessageFromShort(recallPack.ShortContext, conversation.SpeakerMio)
		messages = append(messages, llm.Message{
			Role: "user",
			Content: fmt.Sprintf(
				"発言帰属ガード:\n- あなたはmio。\n- 自分の過去発言(要約): %s\n- 他者の発言(要約): %s\n要件: 他者の発言を自分の新規アイデアとして扱わない。既出アイデアに触れる場合は発言者を明示する。",
				strings.Join(selfCtx, " / "),
				strings.Join(otherCtx, " / "),
			),
		})
	}

	if m.recentContext != nil {
		if glossaryContext, err := m.recentContext(ctx, 6); err != nil {
			log.Printf("[Mio] recent context failed: %v", err)
		} else if strings.TrimSpace(glossaryContext) != "" {
			messages = append(messages, llm.Message{
				Role:    "system",
				Content: glossaryContext + "\n最近の語彙は、断定せず軽い補足として扱ってください。",
			})
		}
	}

	// ユーザーメッセージを最後に追加
	messages = append(messages, userMessageWithAttachments(userMessage, t.Attachments()))

	req := m.generationRequest(messages, llm.StreamCallbackFromContext(ctx))

	resp, err := m.llmProvider.Generate(ctx, req)
	if err != nil {
		return "", err
	}

	response := strings.TrimSpace(resp.Content)
	if violatesAttributionInChat(response, latestOther) {
		retryMessages := append([]llm.Message{}, messages...)
		retryMessages = append(retryMessages, llm.Message{
			Role:    "user",
			Content: "直前の返答は発言帰属が曖昧です。誰のアイデアかを明示して1回だけ言い直してください。",
		})
		retryResp, retryErr := m.llmProvider.Generate(ctx, m.generationRequest(retryMessages, llm.StreamCallbackFromContext(ctx)))
		if retryErr == nil && strings.TrimSpace(retryResp.Content) != "" {
			response = strings.TrimSpace(retryResp.Content)
		}
	}

	// === v5.1: EndTurn（Store） ===
	if m.conversationEngine != nil {
		if err := m.conversationEngine.EndTurn(ctx, t.ChatID(), userMessage, response); err != nil {
			fmt.Printf("WARN: EndTurn failed: %v\n", err)
		}
	}
	if err := m.captureUserMemoryCandidate(ctx, t); err != nil {
		log.Printf("[Mio] user memory candidate capture failed: %v", err)
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
