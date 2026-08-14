package main

import "strings"

type runtimeDataRecallResult struct {
	Store     string                    `json:"store"`
	Operation string                    `json:"operation"`
	Records   []map[string]any          `json:"records"`
	Partial   bool                      `json:"partial"`
	Evidence  runtimeDataRecallEvidence `json:"evidence"`
}

type runtimeDataRecallEvidence struct {
	RequestID       string `json:"request_id"`
	ActorID         string `json:"actor_id"`
	AgentRole       string `json:"agent_role"`
	Purpose         string `json:"purpose"`
	DataScope       string `json:"data_scope"`
	Owner           string `json:"owner"`
	OwnerRoute      string `json:"owner_route"`
	RetrievedAt     string `json:"retrieved_at"`
	FreshnessState  string `json:"freshness_state"`
	ValidationState string `json:"validation_state"`
	BudgetLimit     int    `json:"budget_limit"`
	ReturnedCount   int    `json:"returned_count"`
}

func newRuntimeDataRecallResult(store, operation string, records []map[string]any) runtimeDataRecallResult {
	if records == nil {
		records = []map[string]any{}
	}
	return runtimeDataRecallResult{Store: store, Operation: operation, Records: records}
}

func dataRecallMatches(query string, values ...string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	for _, value := range values {
		if strings.Contains(strings.ToLower(strings.TrimSpace(value)), query) {
			return true
		}
	}
	return false
}
