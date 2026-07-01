package skillgovernance

import (
	"testing"
	"time"
)

func TestMatchSkillsByKeywordAndIntent(t *testing.T) {
	manifests := []SkillManifest{
		{
			SkillID:         "core.pr-readiness",
			Enabled:         true,
			KeywordTriggers: []string{"PR", "pull request"},
			IntentTriggers:  []string{"prepare_pr"},
		},
		{
			SkillID:         "core.refactor-safety",
			Enabled:         true,
			KeywordTriggers: []string{"リファクタ"},
		},
		{
			SkillID:         "disabled.skill",
			Enabled:         false,
			KeywordTriggers: []string{"PR"},
		},
	}
	decisions := MatchSkills(manifests, TaskContext{Text: "この修正をPRに出して", Intent: "prepare_pr"})
	if len(decisions) != 1 {
		t.Fatalf("decisions=%#v", decisions)
	}
	if decisions[0].SkillID != "core.pr-readiness" || decisions[0].TriggerType != "keyword" {
		t.Fatalf("decision=%#v", decisions[0])
	}

	decisions = MatchSkills(manifests, TaskContext{Text: "外部提出", Intent: "prepare_pr"})
	if len(decisions) != 1 || decisions[0].TriggerType != "intent" {
		t.Fatalf("intent decisions=%#v", decisions)
	}
}

func TestNewTriggerLogFromDecision(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	log := NewTriggerLogFromDecision("evt_skill_1", SkillTriggerDecision{
		SkillID:       "core.pr-readiness",
		TriggerType:   "keyword",
		TriggerReason: "PR",
	}, TaskContext{
		Agent:        "Coder",
		WorkstreamID: "ws_1",
	}, now)
	if log.EventID != "evt_skill_1" || log.Status != TriggerStatusTriggered {
		t.Fatalf("log=%#v", log)
	}
	if !log.CreatedAt.Equal(now) {
		t.Fatalf("CreatedAt=%v", log.CreatedAt)
	}
}

func TestBuildBootstrapTriggerLogsMarksMissedSkills(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	manifests := []SkillManifest{
		{
			SkillID:         "core.pr-readiness",
			Enabled:         true,
			KeywordTriggers: []string{"PR"},
		},
		{
			SkillID:         "core.refactor-safety",
			Enabled:         true,
			KeywordTriggers: []string{"リファクタ"},
		},
	}
	logs := BuildBootstrapTriggerLogs(manifests, TaskContext{
		Text:         "このリファクタをPRに出して",
		Agent:        "Coder",
		WorkstreamID: "ws_1",
	}, []string{"core.pr-readiness"}, now, func(index int, skillID string) string {
		return skillID
	})
	if len(logs) != 2 {
		t.Fatalf("logs=%#v", logs)
	}
	statusBySkill := map[string]string{}
	for _, log := range logs {
		statusBySkill[log.SkillID] = log.Status
	}
	if statusBySkill["core.pr-readiness"] != TriggerStatusTriggered {
		t.Fatalf("pr-readiness status=%q", statusBySkill["core.pr-readiness"])
	}
	if statusBySkill["core.refactor-safety"] != TriggerStatusMissed {
		t.Fatalf("refactor-safety status=%q", statusBySkill["core.refactor-safety"])
	}
}
