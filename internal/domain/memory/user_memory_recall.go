package memory

import (
	"sort"
	"strings"
	"time"
)

const (
	UserMemoryRecallDefaultLimit = 12
	UserMemoryRecallMaxLimit     = 50
	UserMemoryRecallMaxScan      = 100
	UserMemoryTraceDefaultLimit  = 20
	UserMemoryTraceMaxLimit      = 100
)

const (
	UserMemoryRecallStatusInjected            = "injected"
	UserMemoryRecallStatusFilteredStatus      = "filtered_status"
	UserMemoryRecallStatusFilteredSensitivity = "filtered_sensitivity"
	UserMemoryRecallStatusFilteredScope       = "filtered_scope"
	UserMemoryRecallStatusBudgetDropped       = "budget_dropped"
	UserMemoryRecallStatusSourceFailure       = "source_failure"
)

// UserMemoryRecallDecision is the deterministic decision made for one
// bounded UserMemory candidate. It is also the source for the Recall trace.
type UserMemoryRecallDecision struct {
	Item     UserMemory
	Score    float64
	Status   string
	Reason   string
	Selected bool
}

// UserMemoryRecallTrace is the bounded trace summary returned with recall.
type UserMemoryRecallTrace struct {
	ID                string    `json:"id"`
	Status            string    `json:"status"`
	QueryTextRedacted string    `json:"query_text_redacted"`
	TotalCandidates   int       `json:"total_candidates"`
	SelectedCount     int       `json:"selected_count"`
	CreatedAt         time.Time `json:"created_at"`
}

// UserMemoryOwnerRecallItem is the owner projection plus the deterministic score.
type UserMemoryOwnerRecallItem struct {
	UserMemoryOwnerView
	Score float64 `json:"score"`
}

type UserMemoryOwnerRecallResult struct {
	Items   []UserMemoryOwnerRecallItem `json:"items"`
	Trace   UserMemoryRecallTrace       `json:"trace"`
	Receipt UserMemoryOwnerReceipt      `json:"receipt"`
}

// UserMemoryTraceSummary is the owner-safe list projection for one trace.
type UserMemoryTraceSummary struct {
	ID                  string    `json:"id"`
	Status              string    `json:"status"`
	Route               string    `json:"route"`
	Persona             string    `json:"persona"`
	TotalCandidates     int       `json:"total_candidates"`
	InjectedCount       int       `json:"injected_count"`
	TotalInjectedTokens int       `json:"total_injected_tokens"`
	CreatedAt           time.Time `json:"created_at"`
}

// UserMemoryTraceItem is a bounded and redacted owner trace item.
type UserMemoryTraceItem struct {
	ItemID        string  `json:"item_id"`
	MemoryID      string  `json:"memory_id"`
	Kind          string  `json:"kind"`
	SourceID      string  `json:"source_id,omitempty"`
	SourceType    string  `json:"source_type,omitempty"`
	Summary       string  `json:"summary,omitempty"`
	Score         float64 `json:"score"`
	Status        string  `json:"status"`
	Reason        string  `json:"reason"`
	PromptSection string  `json:"prompt_section"`
	TokenCount    int     `json:"token_count"`
	MemoryState   string  `json:"memory_state,omitempty"`
	Sensitivity   string  `json:"sensitivity,omitempty"`
}

type UserMemoryTraceDetail struct {
	UserMemoryTraceSummary
	QueryTextRedacted string                `json:"query_text_redacted"`
	Items             []UserMemoryTraceItem `json:"items"`
}

type UserMemoryOwnerTraceListResult struct {
	Items   []UserMemoryTraceSummary `json:"items"`
	Receipt UserMemoryOwnerReceipt   `json:"receipt"`
}

type UserMemoryOwnerTraceShowResult struct {
	Trace   UserMemoryTraceDetail  `json:"trace"`
	Receipt UserMemoryOwnerReceipt `json:"receipt"`
}

// RankUserMemoriesForRecall applies the owner recall policy without I/O or
// model calls. The caller may provide more records, but only the first 100 are
// considered so the operation has a fixed scan bound.
func RankUserMemoriesForRecall(query string, memories []UserMemory, limit int) []UserMemoryRecallDecision {
	return rankUserMemoriesForRecall(query, memories, limit, "")
}

// RankUserMemoriesForRecallForPersona applies the same deterministic ranking
// while excluding memories outside the active persona scope before the
// selection budget is consumed.
func RankUserMemoriesForRecallForPersona(query string, memories []UserMemory, limit int, persona string) []UserMemoryRecallDecision {
	return rankUserMemoriesForRecall(query, memories, limit, persona)
}

func rankUserMemoriesForRecall(query string, memories []UserMemory, limit int, persona string) []UserMemoryRecallDecision {
	if limit <= 0 {
		limit = UserMemoryRecallDefaultLimit
	}
	if limit > UserMemoryRecallMaxLimit {
		limit = UserMemoryRecallMaxLimit
	}
	if len(memories) > UserMemoryRecallMaxScan {
		memories = memories[:UserMemoryRecallMaxScan]
	}
	decisions := make([]UserMemoryRecallDecision, 0, len(memories))
	for _, item := range memories {
		status, reason := userMemoryRecallEligibilityForPersona(item, persona)
		score := UserMemoryRecallScore(query, item.Statement)
		if item.State == MemoryStatePinned {
			score += 1000
		}
		decisions = append(decisions, UserMemoryRecallDecision{Item: item, Score: score, Status: status, Reason: reason})
	}
	sort.SliceStable(decisions, func(i, j int) bool {
		if decisions[i].Score != decisions[j].Score {
			return decisions[i].Score > decisions[j].Score
		}
		if !decisions[i].Item.UpdatedAt.Equal(decisions[j].Item.UpdatedAt) {
			return decisions[i].Item.UpdatedAt.After(decisions[j].Item.UpdatedAt)
		}
		return decisions[i].Item.ID < decisions[j].Item.ID
	})
	selected := 0
	for i := range decisions {
		if decisions[i].Status != "" {
			continue
		}
		if selected >= limit {
			decisions[i].Status = UserMemoryRecallStatusBudgetDropped
			decisions[i].Reason = "recall limit reached"
			continue
		}
		decisions[i].Selected = true
		decisions[i].Status = UserMemoryRecallStatusInjected
		decisions[i].Reason = "selected by deterministic owner recall ranking"
		selected++
	}
	return decisions
}

// UserMemoryRecallScore is a stable lexical relevance score. It intentionally
// does not call an embedding service or an LLM.
func UserMemoryRecallScore(query, statement string) float64 {
	queryTerms := userMemoryRecallTerms(query)
	if len(queryTerms) == 0 {
		return 1
	}
	statementTerms := userMemoryRecallTerms(statement)
	score := 0
	for term := range queryTerms {
		if _, ok := statementTerms[term]; ok {
			score++
		}
	}
	return float64(score)
}

func userMemoryRecallEligibility(item UserMemory) (string, string) {
	return userMemoryRecallEligibilityForPersona(item, "")
}

func userMemoryRecallEligibilityForPersona(item UserMemory, persona string) (string, string) {
	if !userMemoryRecallScopeAllowed(item.Scope) {
		return UserMemoryRecallStatusFilteredScope, "memory scope is not recognized"
	}
	if normalizedPersona := normalizeUserMemoryRecallPersona(persona); normalizedPersona != "" && !userMemoryRecallScopeMatches(item.Scope, normalizedPersona) {
		return UserMemoryRecallStatusFilteredScope, "memory scope is not available to this persona"
	}
	if !item.Active {
		return UserMemoryRecallStatusFilteredStatus, "memory is inactive"
	}
	if strings.TrimSpace(item.SupersededBy) != "" {
		return UserMemoryRecallStatusFilteredStatus, "memory is superseded"
	}
	if strings.EqualFold(strings.TrimSpace(item.LifecycleStatus), "decayed") {
		return UserMemoryRecallStatusFilteredStatus, "memory is decayed"
	}
	if item.State != MemoryStateConfirmed && item.State != MemoryStatePinned {
		return UserMemoryRecallStatusFilteredStatus, "memory state is not confirmed or pinned"
	}
	if sensitivity := strings.TrimSpace(item.Sensitivity); sensitivity != "" && !strings.EqualFold(sensitivity, "normal") {
		return UserMemoryRecallStatusFilteredSensitivity, "memory sensitivity is not normal"
	}
	return "", ""
}

func userMemoryRecallScopeMatches(scope, persona string) bool {
	scope = strings.ToLower(strings.TrimSpace(scope))
	persona = normalizeUserMemoryRecallPersona(persona)
	if scope == "" || scope == "all" || scope == "all_personas" || scope == "global" || persona == "" {
		return true
	}
	return scope == persona || scope == persona+"_only"
}

func normalizeUserMemoryRecallPersona(persona string) string {
	switch strings.ToLower(strings.TrimSpace(persona)) {
	case "mio", "ミオ":
		return "mio"
	case "shiro", "シロ":
		return "shiro"
	case "kuro", "クロ":
		return "kuro"
	case "midori", "ミドリ":
		return "midori"
	default:
		return ""
	}
}

func userMemoryRecallScopeAllowed(scope string) bool {
	scope = strings.ToLower(strings.TrimSpace(scope))
	if scope == "" || scope == "all" || scope == "all_personas" || scope == "global" {
		return true
	}
	for _, persona := range []string{"mio", "shiro", "kuro", "midori"} {
		if scope == persona || scope == persona+"_only" {
			return true
		}
	}
	return false
}

func userMemoryRecallTerms(text string) map[string]struct{} {
	normalized := strings.ToLower(strings.Join(strings.Fields(text), ""))
	runes := []rune(normalized)
	terms := make(map[string]struct{})
	for _, field := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	}) {
		if len(field) >= 2 {
			terms[field] = struct{}{}
		}
	}
	for i := 0; i+1 < len(runes); i++ {
		terms[string(runes[i:i+2])] = struct{}{}
	}
	return terms
}
