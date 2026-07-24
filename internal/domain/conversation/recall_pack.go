package conversation

import (
	"fmt"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

// PromptConstraints はプロンプト組み立ての制約
type PromptConstraints struct {
	MaxTotalTokens    int // LLM の MaxContext（デフォルト: 8192）
	MaxPromptTokens   int // プロンプトに使えるトークン（デフォルト: 4000）
	MaxResponseTokens int // 応答用トークン（デフォルト: 512）
	RecallBudgetRatio float64
}

// DefaultConstraints はデフォルトのトークン制約を返す
func DefaultConstraints() PromptConstraints {
	return PromptConstraints{
		MaxTotalTokens:    8192,
		MaxPromptTokens:   4000,
		MaxResponseTokens: 512,
		RecallBudgetRatio: 0.10,
	}
}

// RecallPack は Recall 結果を構造化した LLM プロンプト注入用フォーマット
type RecallPack struct {
	// RollingSummary: L0現在会話の古い部分を圧縮した要約
	RollingSummary string

	// ShortContext: 現在の Thread 内の直近メッセージ（最大12件）
	ShortContext []Message

	// MidSummaries: 同一セッション内の過去 Thread 要約（最大3件）
	MidSummaries []ThreadSummary

	// LongFacts: VectorDB から類似検索した過去の知識（最大3件）
	LongFacts []string

	// KBSnippets: ドメイン知識ベースからの関連情報（最大2件）
	KBSnippets []string

	// WikiSnippets: Markdown Knowledge Wiki からの仕様索引（最大3件）
	WikiSnippets []WikiSnippet

	// SearchCacheSnippets: 外部検索のfresh cache hitから得た参照情報
	SearchCacheSnippets []SearchCacheSnippet

	// RelationSnippets: Knowledge Relation layer から 1-2 hop で辿った関連知識
	RelationSnippets []RelationSnippet

	// RejectedTraceItems: role filterやbudget制御でプロンプト採用されなかった候補のtrace
	RejectedTraceItems []RecallTraceItem

	// Persona: キャラクター設定
	Persona PersonaState

	// UserProfile: ユーザーの好み・傾向
	UserProfile UserProfile

	// Constraints: トークン上限等
	Constraints PromptConstraints
}

type SearchCacheSnippet struct {
	Query       string
	Provider    string
	ResultsJSON string
	SourceURLs  []string
	RetrievedAt time.Time
	Roles       []string
}

type WikiSnippet struct {
	PageID      string
	Title       string
	Path        string
	Summary     string
	SourcePaths []string
	Related     []string
	UpdatedAt   time.Time
	Roles       []string
}

type RelationSnippet struct {
	ItemID       string
	Title        string
	Summary      string
	SourceType   string
	RelationType string
	Score        float64
	Evidence     string
	Hop          int
	Roles        []string
}

type TokenEstimator interface {
	EstimateTokens(text string) int
}

type TokenEstimatorFunc func(text string) int

func (f TokenEstimatorFunc) EstimateTokens(text string) int {
	if f == nil {
		return estimateRecallTokens(text)
	}
	return f(text)
}

// HasContext は RecallPack に何らかの文脈があるかを返す
func (rp *RecallPack) HasContext() bool {
	return len(rp.ShortContext) > 0 ||
		strings.TrimSpace(rp.RollingSummary) != "" ||
		len(rp.MidSummaries) > 0 ||
		len(rp.LongFacts) > 0 ||
		len(rp.KBSnippets) > 0 ||
		len(rp.WikiSnippets) > 0 ||
		len(rp.SearchCacheSnippets) > 0 ||
		len(rp.RelationSnippets) > 0
}

// ToPromptMessages は RecallPack を llm.Message のスライスに変換
// userMessage は含めない（呼び出し側で追加する）
func (rp *RecallPack) ToPromptMessages() []llm.Message {
	var messages []llm.Message

	// 1. システムプロンプト（Persona + UserProfile）
	systemPrompt := rp.Persona.SystemPrompt
	if profileText := rp.UserProfile.ToPromptText(); profileText != "" {
		systemPrompt += "\n\n" + profileText
	}
	if systemPrompt != "" {
		messages = append(messages, llm.Message{
			Role:    "system",
			Content: systemPrompt,
		})
	}

	// 2. 過去文脈（L2中期記憶 + L3長期記憶 + KB）
	contextText := ""
	if strings.TrimSpace(rp.RollingSummary) != "" {
		contextText += "【L0 現在会話 / rolling summary】\n"
		contextText += "- " + strings.TrimSpace(rp.RollingSummary) + "\n"
	}
	if len(rp.MidSummaries) > 0 {
		contextText += "【L2 中期記憶 / 過去の会話から思い出したこと】\n"
		for _, s := range rp.MidSummaries {
			contextText += "- " + s.Summary + "\n"
		}
	}
	if len(rp.LongFacts) > 0 {
		contextText += "【L3 長期記憶 / 過去の会話から思い出したこと】\n"
		for _, f := range rp.LongFacts {
			contextText += "- " + f + "\n"
		}
	}
	if len(rp.KBSnippets) > 0 {
		contextText += "【Knowledge DB / 参考知識】\n"
		for _, kb := range rp.KBSnippets {
			contextText += kb + "\n"
		}
	}
	if len(rp.WikiSnippets) > 0 {
		contextText += "【RenCrow Knowledge Wiki / 仕様地図】\n"
		for _, wiki := range rp.WikiSnippets {
			contextText += "- " + wiki.ToPromptText() + "\n"
		}
	}
	if len(rp.SearchCacheSnippets) > 0 {
		contextText += "【L1 Search Cache / 検索キャッシュ】\n"
		for _, cache := range rp.SearchCacheSnippets {
			contextText += "- " + cache.ToPromptText() + "\n"
		}
	}
	if len(rp.RelationSnippets) > 0 {
		contextText += "【Knowledge Relation / 関連知識】\n"
		for _, relation := range rp.RelationSnippets {
			contextText += "- " + relation.ToPromptText() + "\n"
		}
	}
	if contextText != "" {
		messages = append(messages, llm.Message{
			Role:    "system",
			Content: contextText,
		})
	}

	// 3. 直近の会話履歴（ShortContext）
	for _, msg := range rp.ShortContext {
		role := "user"
		switch msg.Speaker {
		case SpeakerMio:
			role = "assistant"
		case SpeakerUser:
			role = "user"
		default:
			role = "system"
		}
		messages = append(messages, llm.Message{
			Role:    role,
			Content: msg.Msg,
		})
	}

	return messages
}

func (rp *RecallPack) ApplyRecallBudget(maxContextTokens int, ratio float64) RecallPack {
	return rp.ApplyRecallBudgetWithEstimator(maxContextTokens, ratio, nil)
}

func (rp *RecallPack) ApplyRecallBudgetWithEstimator(maxContextTokens int, ratio float64, estimator TokenEstimator) RecallPack {
	if rp == nil {
		return RecallPack{}
	}
	if maxContextTokens <= 0 || ratio <= 0 {
		return *rp
	}
	budget := int(float64(maxContextTokens) * ratio)
	if budget <= 0 {
		return *rp
	}
	trimmed := *rp
	trimmed.MidSummaries = nil
	trimmed.LongFacts = nil
	trimmed.KBSnippets = nil
	trimmed.WikiSnippets = nil
	trimmed.SearchCacheSnippets = nil
	trimmed.RelationSnippets = nil
	used := 0
	canAdd := func(text string) (bool, int) {
		cost := estimateWithFallback(estimator, text)
		if cost > budget {
			return false, cost
		}
		if used+cost > budget {
			return false, cost
		}
		used += cost
		return true, cost
	}
	for _, summary := range rp.MidSummaries {
		if ok, _ := canAdd(summary.Summary); ok {
			trimmed.MidSummaries = append(trimmed.MidSummaries, summary)
		} else {
			trace := rejectedThreadSummaryTrace(summary, "token budget dropped L2 thread summary")
			trace.Status = TraceStatusBudgetDropped
			trace.TokenCount = estimateWithFallback(estimator, summary.Summary)
			trimmed.RejectedTraceItems = append(trimmed.RejectedTraceItems, trace)
		}
	}
	for _, fact := range rp.LongFacts {
		if ok, _ := canAdd(fact); ok {
			trimmed.LongFacts = append(trimmed.LongFacts, fact)
		} else {
			trace := rejectedKnowledgeTrace(fact, "token budget dropped L3 long fact")
			trace.Kind = "long_fact"
			trace.Status = TraceStatusBudgetDropped
			trace.TokenCount = estimateWithFallback(estimator, fact)
			trimmed.RejectedTraceItems = append(trimmed.RejectedTraceItems, trace)
		}
	}
	for _, snippet := range rp.KBSnippets {
		if ok, _ := canAdd(snippet); ok {
			trimmed.KBSnippets = append(trimmed.KBSnippets, snippet)
		} else {
			trace := rejectedKnowledgeTrace(snippet, "token budget dropped Knowledge DB snippet")
			trace.Status = TraceStatusBudgetDropped
			trace.TokenCount = estimateWithFallback(estimator, snippet)
			trimmed.RejectedTraceItems = append(trimmed.RejectedTraceItems, trace)
		}
	}
	for _, snippet := range rp.WikiSnippets {
		promptText := snippet.ToPromptText()
		if ok, _ := canAdd(promptText); ok {
			trimmed.WikiSnippets = append(trimmed.WikiSnippets, snippet)
		} else {
			trace := rejectedWikiTrace(snippet, "token budget dropped Knowledge Wiki snippet")
			trace.Status = TraceStatusBudgetDropped
			trace.TokenCount = estimateWithFallback(estimator, promptText)
			trimmed.RejectedTraceItems = append(trimmed.RejectedTraceItems, trace)
		}
	}
	for _, cache := range rp.SearchCacheSnippets {
		if ok, _ := canAdd(cache.ToPromptText()); ok {
			trimmed.SearchCacheSnippets = append(trimmed.SearchCacheSnippets, cache)
		} else {
			trace := rejectedSearchCacheTrace(cache, "token budget dropped L1 search cache")
			trace.Status = TraceStatusBudgetDropped
			trace.TokenCount = estimateWithFallback(estimator, cache.ToPromptText())
			trimmed.RejectedTraceItems = append(trimmed.RejectedTraceItems, trace)
		}
	}
	for _, relation := range rp.RelationSnippets {
		promptText := relation.ToPromptText()
		if ok, _ := canAdd(promptText); ok {
			trimmed.RelationSnippets = append(trimmed.RelationSnippets, relation)
		} else {
			trace := rejectedRelationTrace(relation, "token budget dropped Knowledge Relation snippet")
			trace.Status = TraceStatusBudgetDropped
			trace.TokenCount = estimateWithFallback(estimator, promptText)
			trimmed.RejectedTraceItems = append(trimmed.RejectedTraceItems, trace)
		}
	}
	return trimmed
}

func estimateWithFallback(estimator TokenEstimator, text string) int {
	if estimator == nil {
		return estimateRecallTokens(text)
	}
	cost := estimator.EstimateTokens(text)
	if cost <= 0 {
		return estimateRecallTokens(text)
	}
	return cost
}

func (rp *RecallPack) FilterForRole(role string) RecallPack {
	if rp == nil {
		return RecallPack{}
	}
	role = normalizeRecallRole(role)
	if role == "" {
		return *rp
	}
	policy := RecallPolicyForRole(role)
	filtered := *rp
	filtered.KBSnippets = nil
	filtered.WikiSnippets = nil
	filtered.SearchCacheSnippets = nil
	filtered.RelationSnippets = nil
	filtered.RejectedTraceItems = append([]RecallTraceItem(nil), rp.RejectedTraceItems...)
	for _, snippet := range rp.KBSnippets {
		if policyAllowsKnowledgeSnippet(policy, snippet) {
			filtered.KBSnippets = append(filtered.KBSnippets, snippet)
		} else {
			filtered.RejectedTraceItems = append(filtered.RejectedTraceItems, rejectedKnowledgeTrace(snippet, "role "+role+" does not use Knowledge DB snippets by default"))
		}
	}
	for _, snippet := range rp.WikiSnippets {
		if policyAllowsWikiSnippet(policy, role, snippet) {
			filtered.WikiSnippets = append(filtered.WikiSnippets, snippet)
			continue
		}
		reason := "role " + role + " does not use Knowledge Wiki snippets by default"
		if policy.AllowKnowledge && !recallRolesMatch(snippet.Roles, role) {
			reason = "role " + role + " does not match wiki snippet roles"
		}
		filtered.RejectedTraceItems = append(filtered.RejectedTraceItems, rejectedWikiTrace(snippet, reason))
	}
	for _, snippet := range rp.SearchCacheSnippets {
		if policyAllowsSearchCacheSnippet(policy, role, snippet) {
			filtered.SearchCacheSnippets = append(filtered.SearchCacheSnippets, snippet)
			continue
		}
		reason := "role " + role + " does not use L1 search cache by default"
		if policy.AllowSearchCache && !recallRolesMatch(snippet.Roles, role) {
			reason = "role " + role + " does not match search cache roles"
		}
		filtered.RejectedTraceItems = append(filtered.RejectedTraceItems, rejectedSearchCacheTrace(snippet, reason))
	}
	for _, snippet := range rp.RelationSnippets {
		if policyAllowsRelationSnippet(policy, role, snippet) {
			filtered.RelationSnippets = append(filtered.RelationSnippets, snippet)
			continue
		}
		reason := "role " + role + " does not use Knowledge Relation snippets by default"
		if policy.AllowKnowledge && !recallRolesMatch(snippet.Roles, role) {
			reason = "role " + role + " does not match relation snippet roles"
		}
		filtered.RejectedTraceItems = append(filtered.RejectedTraceItems, rejectedRelationTrace(snippet, reason))
	}
	return filtered
}

// WithoutPersonaSystemPrompt removes the stored conversation persona while
// preserving shared user memory and conversation context for another character.
func (rp RecallPack) WithoutPersonaSystemPrompt() RecallPack {
	rp.Persona.SystemPrompt = ""
	return rp
}

func rejectedThreadSummaryTrace(summary ThreadSummary, reason string) RecallTraceItem {
	return RecallTraceItem{
		Layer:         "L2",
		Kind:          "thread_summary",
		Summary:       summary.Summary,
		Score:         summary.Score,
		Decision:      "rejected",
		Status:        TraceStatusFilteredScope,
		PromptSection: PromptSectionConversation,
		TokenCount:    estimateRecallTokens(summary.Summary),
		Reason:        reason,
		PromptIndex:   -1,
	}
}

func rejectedKnowledgeTrace(snippet string, reason string) RecallTraceItem {
	return RecallTraceItem{
		Layer:         "L3",
		Kind:          "knowledge",
		Summary:       snippet,
		Decision:      "rejected",
		Status:        TraceStatusFilteredScope,
		PromptSection: PromptSectionKnowledge,
		TokenCount:    estimateRecallTokens(snippet),
		Reason:        reason,
		PromptIndex:   -1,
	}
}

func rejectedWikiTrace(snippet WikiSnippet, reason string) RecallTraceItem {
	return RecallTraceItem{
		Layer:         "L4",
		Kind:          "wiki_page",
		SourceID:      snippet.PageID,
		SourceType:    "knowledge_wiki",
		Summary:       snippet.ToPromptText(),
		SourceURLs:    append([]string(nil), snippet.SourcePaths...),
		RetrievedAt:   snippet.UpdatedAt,
		Decision:      "rejected",
		Status:        TraceStatusFilteredScope,
		PromptSection: PromptSectionKnowledge,
		TokenCount:    estimateRecallTokens(snippet.ToPromptText()),
		Reason:        reason,
		PromptIndex:   -1,
	}
}

func rejectedSearchCacheTrace(snippet SearchCacheSnippet, reason string) RecallTraceItem {
	return RecallTraceItem{
		Layer:         "L1",
		Kind:          "search_cache",
		Summary:       snippet.ResultsJSON,
		Query:         snippet.Query,
		Provider:      snippet.Provider,
		SourceURLs:    append([]string(nil), snippet.SourceURLs...),
		RetrievedAt:   snippet.RetrievedAt,
		Decision:      "rejected",
		Status:        TraceStatusFilteredScope,
		PromptSection: PromptSectionNews,
		TokenCount:    estimateRecallTokens(snippet.ToPromptText()),
		Reason:        reason,
		PromptIndex:   -1,
	}
}

func rejectedRelationTrace(snippet RelationSnippet, reason string) RecallTraceItem {
	return RecallTraceItem{
		Layer:         "L1",
		Kind:          "knowledge_relation",
		SourceID:      snippet.ItemID,
		SourceType:    snippet.SourceType,
		Summary:       snippet.ToPromptText(),
		Score:         float32(snippet.Score),
		Decision:      "rejected",
		Status:        TraceStatusFilteredScope,
		PromptSection: PromptSectionKnowledge,
		TokenCount:    estimateRecallTokens(snippet.ToPromptText()),
		Reason:        reason,
		PromptIndex:   -1,
	}
}

type RecallRolePolicy struct {
	Role             string
	AllowKnowledge   bool
	AllowSearchCache bool
	RequireExplicit  bool
}

func RecallPolicyForRole(role string) RecallRolePolicy {
	return NewInjectionPolicy(role).recallRolePolicy()
}

func policyAllowsKnowledgeSnippet(policy RecallRolePolicy, snippet string) bool {
	return NewInjectionPolicy(policy.Role).Decide(RecallCandidate{
		Kind:        "knowledge",
		Summary:     snippet,
		State:       "confirmed",
		Sensitivity: "normal",
	}).Status == TraceStatusInjected
}

func policyAllowsSearchCacheSnippet(policy RecallRolePolicy, role string, snippet SearchCacheSnippet) bool {
	return NewInjectionPolicy(policy.Role).Decide(RecallCandidate{
		Kind:        "search_cache",
		Summary:     snippet.ToPromptText(),
		State:       "confirmed",
		Sensitivity: "normal",
		Roles:       append([]string(nil), snippet.Roles...),
	}).Status == TraceStatusInjected
}

func policyAllowsWikiSnippet(policy RecallRolePolicy, role string, snippet WikiSnippet) bool {
	return NewInjectionPolicy(policy.Role).Decide(RecallCandidate{
		Kind:        "wiki_page",
		SourceID:    snippet.PageID,
		SourceType:  "knowledge_wiki",
		Summary:     snippet.ToPromptText(),
		State:       "confirmed",
		Sensitivity: "normal",
		Roles:       append([]string(nil), snippet.Roles...),
	}).Status == TraceStatusInjected
}

func policyAllowsRelationSnippet(policy RecallRolePolicy, role string, snippet RelationSnippet) bool {
	return NewInjectionPolicy(policy.Role).Decide(RecallCandidate{
		Kind:        "knowledge_relation",
		SourceID:    snippet.ItemID,
		SourceType:  snippet.SourceType,
		Summary:     snippet.ToPromptText(),
		State:       "confirmed",
		Sensitivity: "normal",
		Roles:       append([]string(nil), snippet.Roles...),
	}).Status == TraceStatusInjected
}

func recallRolesMatch(roles []string, role string) bool {
	if len(roles) == 0 {
		return true
	}
	for _, candidate := range roles {
		normalized := normalizeRecallRole(candidate)
		if normalized == role || normalized == "all" {
			return true
		}
	}
	return false
}

func normalizeRecallRole(role string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	switch role {
	case "mio":
		return "chat"
	case "shiro":
		return "worker"
	case "aka", "ao", "gin", "kin":
		return "coder"
	case "kuro":
		return "heavy"
	case "midori":
		return "creative"
	default:
		return role
	}
}

func estimateRecallTokens(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	runes := len([]rune(text))
	return runes/4 + 1
}

func (s SearchCacheSnippet) ToPromptText() string {
	var parts []string
	if s.Query != "" {
		parts = append(parts, fmt.Sprintf("query=%s", s.Query))
	}
	if s.Provider != "" {
		parts = append(parts, fmt.Sprintf("provider=%s", s.Provider))
	}
	if !s.RetrievedAt.IsZero() {
		parts = append(parts, fmt.Sprintf("retrieved_at=%s", s.RetrievedAt.UTC().Format(time.RFC3339)))
	}
	if len(s.SourceURLs) > 0 {
		parts = append(parts, "sources="+strings.Join(s.SourceURLs, ", "))
	}
	if s.ResultsJSON != "" {
		parts = append(parts, "results_json="+s.ResultsJSON)
	}
	return strings.Join(parts, "; ")
}

func (s WikiSnippet) ToPromptText() string {
	var parts []string
	if s.Title != "" {
		parts = append(parts, fmt.Sprintf("title=%s", s.Title))
	}
	if s.Path != "" {
		parts = append(parts, fmt.Sprintf("path=%s", s.Path))
	}
	if s.PageID != "" {
		parts = append(parts, fmt.Sprintf("page_id=%s", s.PageID))
	}
	if !s.UpdatedAt.IsZero() {
		parts = append(parts, fmt.Sprintf("updated_at=%s", s.UpdatedAt.UTC().Format(time.RFC3339)))
	}
	if len(s.SourcePaths) > 0 {
		parts = append(parts, "sources="+strings.Join(s.SourcePaths, ", "))
	}
	if len(s.Related) > 0 {
		parts = append(parts, "related="+strings.Join(s.Related, ", "))
	}
	if s.Summary != "" {
		parts = append(parts, "summary="+s.Summary)
	}
	return strings.Join(parts, "; ")
}

func (s RelationSnippet) ToPromptText() string {
	var parts []string
	if s.Title != "" {
		parts = append(parts, fmt.Sprintf("title=%s", s.Title))
	}
	if s.ItemID != "" {
		parts = append(parts, fmt.Sprintf("item_id=%s", s.ItemID))
	}
	if s.SourceType != "" {
		parts = append(parts, fmt.Sprintf("source_type=%s", s.SourceType))
	}
	if s.RelationType != "" {
		parts = append(parts, fmt.Sprintf("relation=%s", s.RelationType))
	}
	if s.Score != 0 {
		parts = append(parts, fmt.Sprintf("score=%.2f", s.Score))
	}
	if s.Hop > 0 {
		parts = append(parts, fmt.Sprintf("hop=%d", s.Hop))
	}
	if s.Evidence != "" {
		parts = append(parts, "evidence="+s.Evidence)
	}
	if s.Summary != "" {
		parts = append(parts, "summary="+s.Summary)
	}
	return strings.Join(parts, "; ")
}
