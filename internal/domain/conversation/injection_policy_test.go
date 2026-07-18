package conversation

import "testing"

func TestInjectionPolicyRejectsUnsafeCandidates(t *testing.T) {
	policy := NewInjectionPolicy("chat")
	cases := []struct {
		name string
		in   RecallCandidate
		want string
	}{
		{
			name: "candidate user memory",
			in:   RecallCandidate{Kind: "user_memory", State: "candidate", Sensitivity: "normal", Scope: "all_personas"},
			want: TraceStatusFilteredStatus,
		},
		{
			name: "sensitive user memory",
			in:   RecallCandidate{Kind: "user_memory", State: "confirmed", Sensitivity: "sensitive", Scope: "all_personas"},
			want: TraceStatusFilteredSensitivity,
		},
		{
			name: "runtime log",
			in:   RecallCandidate{Kind: "context_usage", SourceType: "runtime_log", State: "confirmed"},
			want: TraceStatusFilteredStatus,
		},
		{
			name: "worker labeled conversation memory is shared",
			in:   RecallCandidate{Kind: "thread_summary", State: "confirmed", Roles: []string{"worker"}},
			want: TraceStatusInjected,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := policy.Decide(tc.in)
			if got.Status != tc.want {
				t.Fatalf("status = %s, want %s: %+v", got.Status, tc.want, got)
			}
		})
	}
}

func TestInjectionPolicyAllowsConfirmedUserMemory(t *testing.T) {
	got := NewInjectionPolicy("chat").Decide(RecallCandidate{
		Kind:        "user_memory",
		State:       "confirmed",
		Sensitivity: "normal",
		Scope:       "all_personas",
		Score:       0.9,
	})
	if got.Status != TraceStatusInjected || got.PromptSection != PromptSectionUserMemory {
		t.Fatalf("unexpected decision: %+v", got)
	}
}

func TestInjectionPolicyCharacterizationMatrix(t *testing.T) {
	cases := []struct {
		name string
		role string
		in   RecallCandidate
		want string
	}{
		{
			name: "chat confirmed all personas user memory",
			role: "chat",
			in: RecallCandidate{
				Kind:        "user_memory",
				State:       "confirmed",
				Sensitivity: "normal",
				Scope:       "all_personas",
			},
			want: TraceStatusInjected,
		},
		{
			name: "chat candidate all personas user memory",
			role: "chat",
			in: RecallCandidate{
				Kind:        "user_memory",
				State:       "candidate",
				Sensitivity: "normal",
				Scope:       "all_personas",
			},
			want: TraceStatusFilteredStatus,
		},
		{
			name: "chat confirmed sensitive user memory",
			role: "chat",
			in: RecallCandidate{
				Kind:        "user_memory",
				State:       "confirmed",
				Sensitivity: "sensitive",
				Scope:       "all_personas",
			},
			want: TraceStatusFilteredSensitivity,
		},
		{
			name: "worker labeled thread summary is shared with chat",
			role: "chat",
			in: RecallCandidate{
				Kind:  "thread_summary",
				State: "confirmed",
				Roles: []string{"worker"},
			},
			want: TraceStatusInjected,
		},
		{
			name: "chat scoped thread summary is shared with worker",
			role: "worker",
			in: RecallCandidate{
				Kind:  "thread_summary",
				State: "confirmed",
				Scope: "chat_only",
			},
			want: TraceStatusInjected,
		},
		{
			name: "runtime log is never injected",
			role: "worker",
			in: RecallCandidate{
				Kind:       "context_usage",
				SourceType: "runtime_log",
				State:      "confirmed",
			},
			want: TraceStatusFilteredStatus,
		},
		{
			name: "raw knowledge is lifecycle filtered",
			role: "worker",
			in: RecallCandidate{
				Kind:        "knowledge",
				State:       "raw",
				Sensitivity: "normal",
			},
			want: TraceStatusFilteredStatus,
		},
		{
			name: "unknown role normal knowledge",
			role: "unknown",
			in: RecallCandidate{
				Kind:        "knowledge",
				State:       "confirmed",
				Sensitivity: "normal",
			},
			want: TraceStatusFilteredScope,
		},
		{
			name: "chat generic knowledge",
			role: "chat",
			in: RecallCandidate{
				Kind:        "knowledge",
				Summary:     "generic knowledge",
				State:       "confirmed",
				Sensitivity: "normal",
			},
			want: TraceStatusFilteredScope,
		},
		{
			name: "chat explicit local knowledge",
			role: "mio",
			in: RecallCandidate{
				Kind:        "knowledge",
				Summary:     "[L1KB] local knowledge",
				State:       "confirmed",
				Sensitivity: "normal",
			},
			want: TraceStatusInjected,
		},
		{
			name: "chat generic wiki",
			role: "chat",
			in: RecallCandidate{
				Kind:        "wiki_page",
				SourceType:  "knowledge_wiki",
				State:       "confirmed",
				Sensitivity: "normal",
			},
			want: TraceStatusFilteredScope,
		},
		{
			name: "chat scoped wiki",
			role: "chat",
			in: RecallCandidate{
				Kind:        "wiki_page",
				SourceType:  "knowledge_wiki",
				State:       "confirmed",
				Sensitivity: "normal",
				Roles:       []string{"chat"},
			},
			want: TraceStatusInjected,
		},
		{
			name: "wild search cache",
			role: "wild",
			in: RecallCandidate{
				Kind:        "search_cache",
				State:       "confirmed",
				Sensitivity: "normal",
			},
			want: TraceStatusFilteredScope,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NewInjectionPolicy(tc.role).Decide(tc.in)
			if got.Status != tc.want {
				t.Fatalf("status = %s, want %s: %+v", got.Status, tc.want, got)
			}
		})
	}
}

func TestRecallPolicyCharacterizationMatrix(t *testing.T) {
	cases := []struct {
		name            string
		role            string
		knowledge       string
		wikiRoles       []string
		searchRoles     []string
		wantKnowledge   bool
		wantWiki        bool
		wantSearchCache bool
	}{
		{
			name:            "chat rejects generic recall sources",
			role:            "chat",
			knowledge:       "generic knowledge",
			wantKnowledge:   false,
			wantWiki:        false,
			wantSearchCache: false,
		},
		{
			name:            "chat accepts explicit local first recall sources",
			role:            "mio",
			knowledge:       "[L1KB] local knowledge",
			wikiRoles:       []string{"chat"},
			searchRoles:     []string{"chat"},
			wantKnowledge:   true,
			wantWiki:        true,
			wantSearchCache: true,
		},
		{
			name:            "worker accepts generic practical recall sources",
			role:            "shiro",
			knowledge:       "generic knowledge",
			wantKnowledge:   true,
			wantWiki:        true,
			wantSearchCache: true,
		},
		{
			name:            "wild accepts knowledge but rejects search cache",
			role:            "wild",
			knowledge:       "generic knowledge",
			wantKnowledge:   true,
			wantWiki:        true,
			wantSearchCache: false,
		},
		{
			name:            "unknown rejects all policy gated recall sources",
			role:            "unknown",
			knowledge:       "generic knowledge",
			wantKnowledge:   false,
			wantWiki:        false,
			wantSearchCache: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			role := normalizeRecallRole(tc.role)
			policy := RecallPolicyForRole(tc.role)
			gotKnowledge := policyAllowsKnowledgeSnippet(policy, tc.knowledge)
			gotWiki := policyAllowsWikiSnippet(policy, role, WikiSnippet{Title: "wiki", Roles: tc.wikiRoles})
			gotSearch := policyAllowsSearchCacheSnippet(policy, role, SearchCacheSnippet{Query: "query", Roles: tc.searchRoles})
			if gotKnowledge != tc.wantKnowledge {
				t.Fatalf("knowledge = %v, want %v", gotKnowledge, tc.wantKnowledge)
			}
			if gotWiki != tc.wantWiki {
				t.Fatalf("wiki = %v, want %v", gotWiki, tc.wantWiki)
			}
			if gotSearch != tc.wantSearchCache {
				t.Fatalf("search cache = %v, want %v", gotSearch, tc.wantSearchCache)
			}
		})
	}
}

func TestInjectionPolicyAndRecallRolePolicyConsistency(t *testing.T) {
	cases := []struct {
		name       string
		role       string
		candidate  RecallCandidate
		helperKind string
	}{
		{
			name: "chat generic knowledge",
			role: "chat",
			candidate: RecallCandidate{
				Kind:        "knowledge",
				Summary:     "generic knowledge",
				State:       "confirmed",
				Sensitivity: "normal",
			},
			helperKind: "knowledge",
		},
		{
			name: "chat generic wiki",
			role: "chat",
			candidate: RecallCandidate{
				Kind:        "wiki_page",
				SourceType:  "knowledge_wiki",
				State:       "confirmed",
				Sensitivity: "normal",
			},
			helperKind: "wiki_page",
		},
		{
			name: "chat generic search cache",
			role: "chat",
			candidate: RecallCandidate{
				Kind:        "search_cache",
				State:       "confirmed",
				Sensitivity: "normal",
			},
			helperKind: "search_cache",
		},
		{
			name: "unknown generic knowledge",
			role: "unknown",
			candidate: RecallCandidate{
				Kind:        "knowledge",
				Summary:     "generic knowledge",
				State:       "confirmed",
				Sensitivity: "normal",
			},
			helperKind: "knowledge",
		},
		{
			name: "worker generic knowledge",
			role: "worker",
			candidate: RecallCandidate{
				Kind:        "knowledge",
				Summary:     "generic knowledge",
				State:       "confirmed",
				Sensitivity: "normal",
			},
			helperKind: "knowledge",
		},
		{
			name: "chat explicit search cache",
			role: "chat",
			candidate: RecallCandidate{
				Kind:        "search_cache",
				State:       "confirmed",
				Sensitivity: "normal",
				Roles:       []string{"chat"},
			},
			helperKind: "search_cache",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			injectionAllowed := NewInjectionPolicy(tc.role).Decide(tc.candidate).Status == TraceStatusInjected
			helperAllowed := recallRolePolicyAllowsCandidateForTest(tc.role, tc.helperKind, tc.candidate)
			if injectionAllowed != helperAllowed {
				t.Fatalf("injection allowed = %v, helper allowed = %v", injectionAllowed, helperAllowed)
			}
		})
	}
}

func recallRolePolicyAllowsCandidateForTest(role string, helperKind string, candidate RecallCandidate) bool {
	normalizedRole := normalizeRecallRole(role)
	policy := RecallPolicyForRole(role)
	switch helperKind {
	case "knowledge":
		return policyAllowsKnowledgeSnippet(policy, candidate.Summary)
	case "wiki_page":
		return policyAllowsWikiSnippet(policy, normalizedRole, WikiSnippet{Title: "wiki", Roles: candidate.Roles})
	case "search_cache":
		return policyAllowsSearchCacheSnippet(policy, normalizedRole, SearchCacheSnippet{Query: "query", Roles: candidate.Roles})
	default:
		return false
	}
}
