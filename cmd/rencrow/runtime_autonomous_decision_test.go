package main

import (
	"strings"
	"testing"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestAutonomousDecisionDecoderRejectsUntrustedAuthorityAndMalformedCandidates(t *testing.T) {
	planID := modulecore.NewTaskID()
	valid := autonomousContinueJSON(1)
	decision, err := decodeAutonomousDecision(valid, planID)
	if err != nil {
		t.Fatal(err)
	}
	wantID, err := domaintask.AutonomousWorkTaskID(planID)
	if err != nil || decision.CandidateTaskID != wantID {
		t.Fatalf("candidate identity was not assigned by code: %+v %v", decision, err)
	}
	for name, input := range map[string]string{
		"actor":          strings.TrimSuffix(valid, "}") + `,"actor":"shiro"}`,
		"owner":          strings.TrimSuffix(valid, "}") + `,"owner_id":"other"}`,
		"capabilities":   strings.TrimSuffix(valid, "}") + `,"capabilities":["external_send"]}`,
		"priority":       strings.TrimSuffix(valid, "}") + `,"priority":"critical"}`,
		"candidate_id":   strings.TrimSuffix(valid, "}") + `,"candidate_task_id":"untrusted"}`,
		"two_values":     valid + valid,
		"array":          "[" + valid + "]",
		"null":           "null",
		"empty_reason":   `{"decision":"stop","reason":" "}`,
		"missing_goal":   `{"decision":"continue","reason":"useful","title":"Idea","completion":"One paragraph"}`,
		"stop_with_work": `{"decision":"stop","reason":"done","title":"Another work"}`,
		"long_title":     strings.Replace(valid, "Architecture note 1", strings.Repeat("x", 257), 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeAutonomousDecision(input, planID); err == nil {
				t.Fatal("invalid or authority-bearing provider output was accepted")
			}
		})
	}
	stop, err := decodeAutonomousDecision(`{"decision":"stop","reason":"No useful next work."}`, planID)
	if err != nil || stop.Decision != domaintask.AutonomousDecisionStop || stop.CandidateTaskID != "" {
		t.Fatalf("valid stop rejected: %+v %v", stop, err)
	}
}
