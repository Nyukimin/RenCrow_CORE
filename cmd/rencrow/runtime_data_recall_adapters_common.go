package main

import "strings"

type runtimeDataRecallResult struct {
	Store     string           `json:"store"`
	Operation string           `json:"operation"`
	Records   []map[string]any `json:"records"`
	Partial   bool             `json:"partial"`
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
