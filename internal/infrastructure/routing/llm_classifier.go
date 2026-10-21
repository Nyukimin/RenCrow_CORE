package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
)

const classifierMaxTokens = 8

// LLMClassifier はLLMベースのタスク分類器
type LLMClassifier struct {
	llmProvider  llm.LLMProvider
	systemPrompt string
}

// NewLLMClassifier は新しいLLMClassifierを作成
func NewLLMClassifier(llmProvider llm.LLMProvider, systemPrompt string) *LLMClassifier {
	return &LLMClassifier{
		llmProvider:  llmProvider,
		systemPrompt: systemPrompt,
	}
}

// ClassifyWithChatReply is the optional one-generation route-and-reply path.
func (c *LLMClassifier) ClassifyWithChatReply(ctx context.Context, req llm.GenerateRequest) (domainagent.RouteReplyClassification, error) {
	if c.llmProvider == nil {
		return domainagent.RouteReplyClassification{}, fmt.Errorf("LLM provider is unavailable")
	}
	req.SystemPrompt = ""
	policyMessage := llm.Message{
		Role:    "system",
		Content: strings.TrimSpace(c.systemPrompt + "\n\n" + routeReplyJSONContract),
		Type:    llm.PromptContextStable,
		Metadata: map[string]string{
			"runtime_context_kind": "classifier_route_contract",
		},
	}
	characterPrefix := 0
	for characterPrefix < len(req.Messages) && req.Messages[characterPrefix].Type == llm.PromptContextCharacter {
		characterPrefix++
	}
	req.Messages = append(req.Messages, llm.Message{})
	copy(req.Messages[characterPrefix+1:], req.Messages[characterPrefix:len(req.Messages)-1])
	req.Messages[characterPrefix] = policyMessage
	req.ResponseFormat = llm.ResponseFormatJSONObject
	// Combined JSON generation is non-streaming even when Mio's regular CHAT
	// configuration enables token callbacks. Never expose route JSON to hooks.
	req.OnToken = nil
	ensureNoThinking(&req)

	resp, err := c.llmProvider.Generate(ctx, req)
	if err != nil {
		return domainagent.RouteReplyClassification{}, fmt.Errorf("LLM route and reply generation failed: %w", err)
	}
	decoded, err := decodeRouteReply(resp.Content)
	if err != nil {
		return domainagent.RouteReplyClassification{}, err
	}
	route, matched := c.parseRouteWithMatch(decoded.Route)
	if !matched || !strings.EqualFold(strings.TrimSpace(decoded.Route), string(route)) {
		return domainagent.RouteReplyClassification{}, fmt.Errorf("combined generation returned invalid route field %q", decoded.Route)
	}
	if route == routing.RouteCHAT && strings.TrimSpace(decoded.Reply) == "" {
		return domainagent.RouteReplyClassification{}, fmt.Errorf("combined CHAT generation returned an empty reply")
	}
	if route != routing.RouteCHAT && strings.TrimSpace(decoded.Reply) != "" {
		return domainagent.RouteReplyClassification{}, fmt.Errorf("combined non-CHAT generation returned a reply")
	}
	confidence := c.calculateConfidence(route, string(route))
	reason := fmt.Sprintf("LLM classified as %s", route)
	decision := routing.NewDecisionWithEvidence(route, confidence,
		reason, routing.DecisionEvidence{
			Source:     routing.EvidenceSourceClassifier,
			Matched:    true,
			Route:      route,
			Confidence: confidence,
			Reason:     reason,
		})
	return domainagent.RouteReplyClassification{
		Decision:   decision,
		Reply:      decoded.Reply,
		ReplyValid: route == routing.RouteCHAT,
		Generation: resp,
	}, nil
}

const routeReplyJSONContract = `Use the route policy above without changing its criteria. Return exactly one JSON object with the string fields "route" and "reply". The route field must contain exactly one canonical route name allowed by the route policy above; do not infer the route from the reply text. If the selected route is CHAT, write Mio's direct response in reply using the supplied persona and conversation context. For every other route, reply must be empty. Do not call tools, perform searches, or claim tool-dependent work in this response.`

type routeReplyJSON struct {
	Route string
	Reply string
}

const maxRouteReplyJSONBytes = 64 * 1024

func decodeRouteReply(content string) (routeReplyJSON, error) {
	if len(content) > maxRouteReplyJSONBytes {
		return routeReplyJSON{}, fmt.Errorf("combined route/reply response exceeds %d bytes", maxRouteReplyJSONBytes)
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	token, err := decoder.Token()
	if err != nil {
		return routeReplyJSON{}, fmt.Errorf("decode combined route/reply JSON: %w", err)
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return routeReplyJSON{}, fmt.Errorf("combined route/reply response must be one JSON object")
	}
	var result routeReplyJSON
	seen := make(map[string]bool, 2)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return routeReplyJSON{}, fmt.Errorf("decode combined route/reply field: %w", err)
		}
		key, ok := token.(string)
		if !ok {
			return routeReplyJSON{}, fmt.Errorf("combined route/reply field name must be a string")
		}
		if seen[key] {
			return routeReplyJSON{}, fmt.Errorf("combined route/reply response repeats field %q", key)
		}
		seen[key] = true
		switch key {
		case "route":
			result.Route, err = decodeJSONString(decoder)
		case "reply":
			result.Reply, err = decodeJSONString(decoder)
		default:
			return routeReplyJSON{}, fmt.Errorf("combined route/reply response contains unknown field %q", key)
		}
		if err != nil {
			return routeReplyJSON{}, fmt.Errorf("decode combined route/reply field %q: %w", key, err)
		}
	}
	token, err = decoder.Token()
	if err != nil {
		return routeReplyJSON{}, fmt.Errorf("decode combined route/reply object end: %w", err)
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '}' {
		return routeReplyJSON{}, fmt.Errorf("combined route/reply response has invalid object end")
	}
	if !seen["route"] || !seen["reply"] {
		return routeReplyJSON{}, fmt.Errorf("combined route/reply response must include route and reply fields")
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return routeReplyJSON{}, fmt.Errorf("combined route/reply response contains trailing JSON")
		}
		return routeReplyJSON{}, fmt.Errorf("combined route/reply response has trailing content: %w", err)
	}
	return result, nil
}

func decodeJSONString(decoder *json.Decoder) (string, error) {
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return "", err
	}
	trimmed := strings.TrimSpace(string(raw))
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return "", fmt.Errorf("value must be a JSON string")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	return value, nil
}

func ensureNoThinking(req *llm.GenerateRequest) {
	options := make(map[string]any, len(req.ProviderOptions)+2)
	for key, value := range req.ProviderOptions {
		options[key] = value
	}
	options["think"] = false
	kwargs := make(map[string]any, 1)
	if existing, ok := options["chat_template_kwargs"].(map[string]any); ok {
		for key, value := range existing {
			kwargs[key] = value
		}
	}
	kwargs["enable_thinking"] = false
	options["chat_template_kwargs"] = kwargs
	req.ProviderOptions = options
}

// Classify はタスクを分類
func (c *LLMClassifier) Classify(ctx context.Context, t conversation.TurnInput) (routing.Decision, error) {
	userMessage := fmt.Sprintf("ユーザーからのメッセージ: %s", t.MessageText())

	req := llm.GenerateRequest{
		SystemPrompt: c.systemPrompt,
		Messages: []llm.Message{
			{Role: "user", Content: userMessage},
		},
		MaxTokens:   classifierMaxTokens,
		Temperature: 0.3, // 低温度で安定した分類
		ProviderOptions: map[string]any{
			"think": false,
			"chat_template_kwargs": map[string]any{
				"enable_thinking": false,
			},
		},
	}

	resp, err := c.llmProvider.Generate(ctx, req)
	if err != nil {
		return routing.Decision{}, fmt.Errorf("LLM classification failed: %w", err)
	}

	// LLM応答からルートを抽出
	route, matched := c.parseRouteWithMatch(resp.Content)
	confidence := c.calculateConfidence(route, resp.Content)
	reason := fmt.Sprintf("LLM classified as %s", route)
	source := routing.EvidenceSourceClassifier
	if !matched {
		source = routing.EvidenceSourceSafeFallback
		reason = "LLM classification fallback to CHAT"
	}

	return routing.NewDecisionWithEvidence(route, confidence, reason, routing.DecisionEvidence{
		Source:     source,
		Matched:    true,
		Route:      route,
		Confidence: confidence,
		Reason:     reason,
	}), nil
}

// parseRoute はLLM応答からルートを抽出
func (c *LLMClassifier) parseRoute(response string) routing.Route {
	route, _ := c.parseRouteWithMatch(response)
	return route
}

func (c *LLMClassifier) parseRouteWithMatch(response string) (routing.Route, bool) {
	// レスポンスをトリムして大文字化
	trimmed := strings.TrimSpace(response)
	upper := strings.ToUpper(trimmed)

	// 長いルート名から順にチェック（CODE3 を CODE より先に判定）
	validRoutes := []struct {
		key   string
		route routing.Route
	}{
		{"CODE4", routing.RouteCODE4},
		{"CODE3", routing.RouteCODE3},
		{"CODE2", routing.RouteCODE2},
		{"CODE1", routing.RouteCODE1},
		{"CODE", routing.RouteCODE},
		{"ANALYZE", routing.RouteANALYZE},
		{"RESEARCH", routing.RouteRESEARCH},
		{"WILD", routing.RouteWILD},
		{"PLAN", routing.RoutePLAN},
		{"CHAT", routing.RouteCHAT},
		{"OPS", routing.RouteOPS},
	}

	for _, vr := range validRoutes {
		if strings.Contains(upper, vr.key) {
			return vr.route, true
		}
	}

	// 無効なルート名の場合はCHATにフォールバック
	return routing.RouteCHAT, false
}

// calculateConfidence は信頼度を計算
func (c *LLMClassifier) calculateConfidence(route routing.Route, response string) float64 {
	// CHATへのフォールバックは低信頼度
	if route == routing.RouteCHAT && !strings.Contains(strings.ToUpper(response), "CHAT") {
		return 0.4
	}

	// 明確なマッチは高信頼度
	return 0.7
}
