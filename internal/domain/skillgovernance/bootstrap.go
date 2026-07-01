package skillgovernance

import (
	"strings"
	"time"
)

func MatchSkills(manifests []SkillManifest, task TaskContext) []SkillTriggerDecision {
	var decisions []SkillTriggerDecision
	text := strings.ToLower(task.Text)
	intent := strings.ToLower(strings.TrimSpace(task.Intent))
	for _, manifest := range manifests {
		if !manifest.Enabled {
			continue
		}
		var matchedKeywords []string
		for _, keyword := range manifest.KeywordTriggers {
			k := strings.ToLower(strings.TrimSpace(keyword))
			if k == "" {
				continue
			}
			if strings.Contains(text, k) {
				matchedKeywords = append(matchedKeywords, keyword)
			}
		}
		if len(matchedKeywords) > 0 {
			decisions = append(decisions, SkillTriggerDecision{
				SkillID:       manifest.SkillID,
				TriggerType:   "keyword",
				TriggerReason: strings.Join(matchedKeywords, ", "),
				Matched:       true,
				MatchedTerms:  matchedKeywords,
			})
			continue
		}
		for _, trigger := range manifest.IntentTriggers {
			t := strings.ToLower(strings.TrimSpace(trigger))
			if t != "" && t == intent {
				decisions = append(decisions, SkillTriggerDecision{
					SkillID:       manifest.SkillID,
					TriggerType:   "intent",
					TriggerReason: trigger,
					Matched:       true,
					MatchedTerms:  []string{trigger},
				})
				break
			}
		}
	}
	return decisions
}

func NewTriggerLogFromDecision(eventID string, decision SkillTriggerDecision, task TaskContext, now time.Time) SkillTriggerLog {
	return NewTriggerLog(eventID, decision, task, TriggerStatusTriggered, now)
}

func NewTriggerLog(eventID string, decision SkillTriggerDecision, task TaskContext, status string, now time.Time) SkillTriggerLog {
	return SkillTriggerLog{
		EventID:       eventID,
		SkillID:       decision.SkillID,
		TriggerType:   decision.TriggerType,
		TriggerReason: decision.TriggerReason,
		Agent:         task.Agent,
		WorkstreamID:  task.WorkstreamID,
		Status:        status,
		CreatedAt:     now,
	}
}

func BuildBootstrapTriggerLogs(manifests []SkillManifest, task TaskContext, usedSkillIDs []string, now time.Time, eventID func(index int, skillID string) string) []SkillTriggerLog {
	used := make(map[string]bool, len(usedSkillIDs))
	for _, id := range usedSkillIDs {
		id = strings.TrimSpace(id)
		if id != "" {
			used[id] = true
		}
	}
	decisions := MatchSkills(manifests, task)
	logs := make([]SkillTriggerLog, 0, len(decisions))
	for i, decision := range decisions {
		status := TriggerStatusMissed
		if used[decision.SkillID] {
			status = TriggerStatusTriggered
		}
		id := ""
		if eventID != nil {
			id = eventID(i, decision.SkillID)
		}
		if id == "" {
			id = "evt_skill_bootstrap"
		}
		logs = append(logs, NewTriggerLog(id, decision, task, status, now))
	}
	return logs
}
