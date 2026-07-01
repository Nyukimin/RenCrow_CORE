package persona

import (
	"sort"
	"strings"
)

func MatchTrigger(input string, definitions []TriggerDefinition) (TriggerMatch, bool) {
	input = strings.TrimSpace(input)
	if input == "" {
		return TriggerMatch{}, false
	}
	type candidate struct {
		match        TriggerMatch
		matchedTerms int
		priority     int
	}
	candidates := make([]candidate, 0, len(definitions))
	for _, def := range definitions {
		match, matchedTerms, ok := matchTriggerDefinition(input, def)
		if ok {
			candidates = append(candidates, candidate{
				match:        match,
				matchedTerms: matchedTerms,
				priority:     def.Priority,
			})
		}
	}
	if len(candidates) == 0 {
		return TriggerMatch{}, false
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].match.Confidence == candidates[j].match.Confidence {
			if candidates[i].matchedTerms == candidates[j].matchedTerms {
				return candidates[i].priority > candidates[j].priority
			}
			return candidates[i].matchedTerms > candidates[j].matchedTerms
		}
		return candidates[i].match.Confidence > candidates[j].match.Confidence
	})
	return candidates[0].match, true
}

func CanUseCanonicalResponse(policy CanonicalResponsePolicy, recent []CanonicalResponseLog, contexts []string) bool {
	if strings.TrimSpace(policy.ResponseID) == "" {
		return false
	}
	if !containsAllContexts(contexts, policy.RequiredContexts) {
		return false
	}
	uses := 0
	for i := len(recent) - 1; i >= 0; i-- {
		if recent[i].ResponseID != policy.ResponseID || !recent[i].Used {
			continue
		}
		uses++
		if policy.CooldownTurns > 0 && len(recent)-1-i < policy.CooldownTurns {
			return false
		}
	}
	if policy.MaxPerSession > 0 && uses >= policy.MaxPerSession {
		return false
	}
	return true
}

func matchTriggerDefinition(input string, def TriggerDefinition) (TriggerMatch, int, bool) {
	if strings.TrimSpace(def.TriggerID) == "" || strings.TrimSpace(def.CharacterID) == "" || len(def.Keywords) == 0 {
		return TriggerMatch{}, 0, false
	}
	hits := 0
	terms := 0
	for _, keyword := range def.Keywords {
		keyword = strings.TrimSpace(keyword)
		if keyword == "" {
			continue
		}
		terms++
		if strings.Contains(input, keyword) {
			hits++
		}
	}
	if hits == 0 || terms == 0 {
		return TriggerMatch{}, 0, false
	}
	confidence := float64(hits) / float64(terms)
	return TriggerMatch{
		TriggerID:   def.TriggerID,
		CharacterID: def.CharacterID,
		Category:    def.Category,
		Confidence:  confidence,
	}, hits, true
}

func containsAllContexts(contexts []string, required []string) bool {
	if len(required) == 0 {
		return true
	}
	seen := map[string]struct{}{}
	for _, context := range contexts {
		seen[strings.TrimSpace(context)] = struct{}{}
	}
	for _, context := range required {
		if _, ok := seen[strings.TrimSpace(context)]; !ok {
			return false
		}
	}
	return true
}
