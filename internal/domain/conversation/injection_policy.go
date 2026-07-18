package conversation

import "strings"

const (
	TraceStatusRetrieved           = "retrieved"
	TraceStatusFilteredScope       = "filtered_scope"
	TraceStatusFilteredSensitivity = "filtered_sensitivity"
	TraceStatusFilteredStatus      = "filtered_status"
	TraceStatusDeduped             = "deduped"
	TraceStatusBudgetDropped       = "budget_dropped"
	TraceStatusInjected            = "injected"
	TraceStatusGuardOnly           = "guard_only"
)

const (
	PromptSectionSystemPersona  = "[System Persona]"
	PromptSectionCurrentTurn    = "[Current Turn]"
	PromptSectionConversation   = "[RecallPack: Conversation]"
	PromptSectionUserMemory     = "[RecallPack: UserMemory]"
	PromptSectionKnowledge      = "[RecallPack: Knowledge]"
	PromptSectionNews           = "[RecallPack: News]"
	PromptSectionOperation      = "[RecallPack: Operation]"
	PromptSectionAttribution    = "[Guard: Attribution]"
	PromptSectionGlossaryRecent = "[Glossary Recent Context]"
	PromptSectionUserMessage    = "[User Message]"
)

type RecallCandidate struct {
	Layer       string
	Kind        string
	MemoryID    string
	SourceID    string
	SourceType  string
	Summary     string
	Score       float64
	Relevance   float64
	Recency     float64
	Confidence  float64
	SourceTrust float64
	State       string
	Sensitivity string
	Scope       string
	Roles       []string
}

type InjectionDecision struct {
	Status        string
	PromptSection string
	Reason        string
	Score         float64
	TokenCount    int
}

type InjectionPolicy struct {
	Role string
}

func NewInjectionPolicy(role string) InjectionPolicy {
	return InjectionPolicy{Role: normalizeRecallRole(role)}
}

func (p InjectionPolicy) Decide(candidate RecallCandidate) InjectionDecision {
	role := normalizeRecallRole(p.Role)
	if role == "" {
		role = "chat"
	}
	rolePolicy := NewInjectionPolicy(role).recallRolePolicy()
	if strings.TrimSpace(candidate.SourceType) == "runtime_log" {
		return InjectionDecision{Status: TraceStatusFilteredStatus, PromptSection: sectionForCandidate(candidate), Reason: "runtime logs are never directly injected", Score: candidate.Score}
	}
	sharedMemory := isSharedMemoryCandidate(candidate)
	if !sharedMemory && !recallRolesMatch(candidate.Roles, role) {
		return InjectionDecision{Status: TraceStatusFilteredScope, PromptSection: sectionForCandidate(candidate), Reason: "candidate roles do not match role " + role, Score: candidate.Score}
	}
	scope := strings.ToLower(strings.TrimSpace(candidate.Scope))
	if !sharedMemory && scope != "" && scope != "all" && scope != "all_personas" && scope != role && scope != role+"_only" {
		return InjectionDecision{Status: TraceStatusFilteredScope, PromptSection: sectionForCandidate(candidate), Reason: "candidate scope does not match role " + role, Score: candidate.Score}
	}
	if sensitivity := strings.ToLower(strings.TrimSpace(candidate.Sensitivity)); sensitivity != "" && sensitivity != "normal" {
		return InjectionDecision{Status: TraceStatusFilteredSensitivity, PromptSection: sectionForCandidate(candidate), Reason: "candidate sensitivity is not normal", Score: candidate.Score}
	}
	state := strings.ToLower(strings.TrimSpace(candidate.State))
	if isUserMemoryCandidate(candidate) {
		switch state {
		case "confirmed", "pinned":
		default:
			return InjectionDecision{Status: TraceStatusFilteredStatus, PromptSection: PromptSectionUserMemory, Reason: "user memory is not confirmed or pinned", Score: candidate.Score}
		}
	}
	if state == "candidate" || state == "observed" || state == "staging" || state == "raw" || state == "deleted" || state == "superseded" {
		return InjectionDecision{Status: TraceStatusFilteredStatus, PromptSection: sectionForCandidate(candidate), Reason: "candidate lifecycle state is not injectable", Score: candidate.Score}
	}
	if isKnowledgeCandidate(candidate) {
		if !rolePolicy.AllowKnowledge {
			return InjectionDecision{Status: TraceStatusFilteredScope, PromptSection: PromptSectionKnowledge, Reason: "role " + role + " does not use Knowledge DB snippets by default", Score: candidate.Score}
		}
		if rolePolicy.RequireExplicit && !isExplicitKnowledgeSnippet(candidate.Summary) {
			return InjectionDecision{Status: TraceStatusFilteredScope, PromptSection: PromptSectionKnowledge, Reason: "role " + role + " does not use Knowledge DB snippets by default", Score: candidate.Score}
		}
	}
	if isWikiCandidate(candidate) {
		if !rolePolicy.AllowKnowledge {
			return InjectionDecision{Status: TraceStatusFilteredScope, PromptSection: PromptSectionKnowledge, Reason: "role " + role + " does not use Knowledge Wiki snippets by default", Score: candidate.Score}
		}
		if rolePolicy.RequireExplicit && len(candidate.Roles) == 0 {
			return InjectionDecision{Status: TraceStatusFilteredScope, PromptSection: PromptSectionKnowledge, Reason: "role " + role + " does not use Knowledge Wiki snippets by default", Score: candidate.Score}
		}
	}
	if isSearchCacheCandidate(candidate) {
		if !rolePolicy.AllowSearchCache {
			return InjectionDecision{Status: TraceStatusFilteredScope, PromptSection: PromptSectionNews, Reason: "role " + role + " does not use L1 search cache by default", Score: candidate.Score}
		}
		if rolePolicy.RequireExplicit && len(candidate.Roles) == 0 {
			return InjectionDecision{Status: TraceStatusFilteredScope, PromptSection: PromptSectionNews, Reason: "role " + role + " does not use L1 search cache by default", Score: candidate.Score}
		}
	}
	return InjectionDecision{Status: TraceStatusInjected, PromptSection: sectionForCandidate(candidate), Reason: "candidate passed injection policy", Score: candidate.Score}
}

func (p InjectionPolicy) recallRolePolicy() RecallRolePolicy {
	role := normalizeRecallRole(p.Role)
	switch role {
	case "chat":
		return RecallRolePolicy{Role: "chat", AllowKnowledge: true, AllowSearchCache: true, RequireExplicit: true}
	case "worker":
		return RecallRolePolicy{Role: "worker", AllowKnowledge: true, AllowSearchCache: true}
	case "coder", "code":
		return RecallRolePolicy{Role: "coder", AllowKnowledge: true, AllowSearchCache: true}
	case "heavy", "wild":
		return RecallRolePolicy{Role: role, AllowKnowledge: true, AllowSearchCache: false}
	case "creative":
		return RecallRolePolicy{Role: "creative", AllowKnowledge: true, AllowSearchCache: false}
	default:
		return RecallRolePolicy{Role: role, AllowKnowledge: false, AllowSearchCache: false}
	}
}

func sectionForCandidate(candidate RecallCandidate) string {
	kind := strings.ToLower(strings.TrimSpace(candidate.Kind))
	layer := strings.ToUpper(strings.TrimSpace(candidate.Layer))
	sourceType := strings.ToLower(strings.TrimSpace(candidate.SourceType))
	switch {
	case isUserMemoryCandidate(candidate):
		return PromptSectionUserMemory
	case isKnowledgeCandidate(candidate) || isWikiCandidate(candidate):
		return PromptSectionKnowledge
	case isSearchCacheCandidate(candidate) || kind == "news" || sourceType == "news":
		return PromptSectionNews
	case kind == "operation" || sourceType == "operation_memory":
		return PromptSectionOperation
	case kind == "rolling_summary":
		return PromptSectionCurrentTurn
	case kind == "short_context" || kind == "thread_summary" || layer == "L0" || layer == "L1" || layer == "L2":
		return PromptSectionConversation
	default:
		return PromptSectionConversation
	}
}

func isUserMemoryCandidate(candidate RecallCandidate) bool {
	kind := strings.ToLower(strings.TrimSpace(candidate.Kind))
	sourceType := strings.ToLower(strings.TrimSpace(candidate.SourceType))
	return kind == "user_memory" || sourceType == "user_memory" || strings.HasPrefix(strings.TrimSpace(candidate.MemoryID), "user:")
}

// isSharedMemoryCandidate は、全Agentで共有する会話・ユーザー・エピソード記憶を判定する。
// Knowledge、検索結果、運用情報は含めず、それぞれのrole policyを維持する。
func isSharedMemoryCandidate(candidate RecallCandidate) bool {
	if isUserMemoryCandidate(candidate) {
		return true
	}
	kind := strings.ToLower(strings.TrimSpace(candidate.Kind))
	sourceType := strings.ToLower(strings.TrimSpace(candidate.SourceType))
	switch kind {
	case "rolling_summary", "short_context", "thread_summary", "long_fact", "episode", "episodic_memory":
		return true
	}
	return sourceType == "conversation_memory" || sourceType == "episodic_memory"
}

func isKnowledgeCandidate(candidate RecallCandidate) bool {
	kind := strings.ToLower(strings.TrimSpace(candidate.Kind))
	return kind == "knowledge" || kind == "knowledge_relation" || strings.Contains(kind, "kb")
}

func isWikiCandidate(candidate RecallCandidate) bool {
	kind := strings.ToLower(strings.TrimSpace(candidate.Kind))
	sourceType := strings.ToLower(strings.TrimSpace(candidate.SourceType))
	return kind == "wiki_page" || sourceType == "knowledge_wiki"
}

func isSearchCacheCandidate(candidate RecallCandidate) bool {
	kind := strings.ToLower(strings.TrimSpace(candidate.Kind))
	return kind == "search_cache"
}

func isExplicitKnowledgeSnippet(snippet string) bool {
	snippet = strings.TrimSpace(snippet)
	return strings.HasPrefix(snippet, "[L1KB]") || strings.HasPrefix(snippet, "[VectorKB]")
}
