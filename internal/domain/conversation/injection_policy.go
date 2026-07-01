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
	if strings.TrimSpace(candidate.SourceType) == "runtime_log" {
		return InjectionDecision{Status: TraceStatusFilteredStatus, PromptSection: sectionForCandidate(candidate), Reason: "runtime logs are never directly injected", Score: candidate.Score}
	}
	if !recallRolesMatch(candidate.Roles, role) {
		return InjectionDecision{Status: TraceStatusFilteredScope, PromptSection: sectionForCandidate(candidate), Reason: "candidate roles do not match role " + role, Score: candidate.Score}
	}
	scope := strings.ToLower(strings.TrimSpace(candidate.Scope))
	if scope != "" && scope != "all" && scope != "all_personas" && scope != role && scope != role+"_only" {
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
	return InjectionDecision{Status: TraceStatusInjected, PromptSection: sectionForCandidate(candidate), Reason: "candidate passed injection policy", Score: candidate.Score}
}

func sectionForCandidate(candidate RecallCandidate) string {
	kind := strings.ToLower(strings.TrimSpace(candidate.Kind))
	layer := strings.ToUpper(strings.TrimSpace(candidate.Layer))
	sourceType := strings.ToLower(strings.TrimSpace(candidate.SourceType))
	switch {
	case isUserMemoryCandidate(candidate):
		return PromptSectionUserMemory
	case kind == "knowledge" || strings.Contains(kind, "kb"):
		return PromptSectionKnowledge
	case kind == "search_cache" || kind == "news" || sourceType == "news":
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
