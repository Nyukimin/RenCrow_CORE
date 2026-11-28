package toolloop

import (
	"context"
	"encoding/json"
	"fmt"

	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
)

type webBodyFetchState struct {
	Attempted bool   `json:"attempted"`
	Status    string `json:"status"`
	URL       string `json:"url,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Automatic bool   `json:"automatic,omitempty"`
}

func webSearchTool(name string) bool {
	return name == "web_search" || name == "web_gather.search"
}

func (r *webEvidenceRunner) canFetchSource() bool {
	_, ok := r.selected["web_gather.fetch"]
	return ok
}

// Read URLs from the declared structured search contracts, never from model
// prose or a page's embedded instructions. Preserve result ranking.
func (r *webEvidenceRunner) recordSearchSources(name string, result *tool.ToolResponse) {
	if result == nil || result.IsError() {
		return
	}
	var candidates []string
	switch name {
	case "web_gather.search":
		data, err := json.Marshal(result.Result)
		var response modulewebgather.SearchResponse
		if err != nil || json.Unmarshal(data, &response) != nil {
			return
		}
		for _, item := range response.Results {
			candidates = append(candidates, item.URL)
		}
	case "web_search":
		data, err := json.Marshal(result.Metadata["search_items"])
		var items []struct {
			Link string `json:"link"`
		}
		if err != nil || json.Unmarshal(data, &items) != nil {
			return
		}
		for _, item := range items {
			candidates = append(candidates, item.Link)
		}
	}
	for _, candidate := range candidates {
		if len(candidate) > 2048 {
			continue
		}
		normalized, err := modulewebgather.NormalizeURL(candidate, false)
		if err != nil {
			continue
		}
		candidate = normalized
		duplicate := false
		for _, previous := range r.sourceURLs {
			if previous == candidate {
				duplicate = true
				break
			}
		}
		if !duplicate && len(r.sourceURLs) < 10 {
			r.sourceURLs = append(r.sourceURLs, candidate)
		}
	}
}

func (r *webEvidenceRunner) bodyFetchState() webBodyFetchState {
	state := webBodyFetchState{Status: "not_attempted"}
	for _, observation := range r.observations {
		if observation.Tool != "web_gather.fetch" && observation.Tool != "web_gather.search_and_fetch" {
			continue
		}
		var responses []modulewebgather.FetchResponse
		if observation.Tool == "web_gather.fetch" {
			state = webBodyFetchState{Attempted: true, Status: "failed", Automatic: observation.Automatic}
			state.URL, _ = observation.Arguments["url"].(string)
		}
		if observation.Error != "" {
			state.Reason = observation.Error
			continue
		}
		if observation.Result == nil {
			state.Reason = "fetch returned no result"
			continue
		}
		if observation.Result.IsError() {
			state.Reason = observation.Result.Error.Message
			continue
		}
		if observation.Tool == "web_gather.fetch" {
			state.Status, state.Reason = "unverified", "fetch tool result lacks verified body metadata"
		}
		data, err := json.Marshal(observation.Result.Result)
		if err != nil {
			continue
		}
		if observation.Tool == "web_gather.fetch" {
			var response modulewebgather.FetchResponse
			if json.Unmarshal(data, &response) != nil {
				continue
			}
			responses = append(responses, response)
		} else {
			var response modulewebgather.SearchAndFetchResponse
			if json.Unmarshal(data, &response) != nil {
				continue
			}
			for _, item := range response.Items {
				responses = append(responses, item.Fetch)
			}
		}
		for _, response := range responses {
			if response.URL == "" && response.Status == "" {
				continue
			}
			state.Attempted, state.URL = true, response.URL
			state.Status, state.Reason = "unverified", "fetch result does not verify extracted body"
			if response.Status == "ok" && response.HTTPStatus >= 200 && response.HTTPStatus < 300 && response.ExtractedChars > 0 {
				state.Status, state.Reason = "succeeded", ""
				return state
			}
		}
	}
	return state
}

// Complete retrieval if the model stopped after search. Execute the same
// registered Tool through the original policy runner, scope and Action owner.
func (r *WebResearcher) completeSourceFetch(ctx context.Context, cfg Config, collector *webEvidenceRunner) (webBodyFetchState, error) {
	state := collector.bodyFetchState()
	if state.Attempted {
		return state, nil
	}
	if !collector.canFetchSource() {
		state.Reason = "web_gather.fetch is unavailable in this scope"
		return state, nil
	}
	if len(collector.sourceURLs) == 0 {
		state.Reason = "search returned no usable HTTP(S) source URL"
		return state, nil
	}
	execCtx := ctx
	if cfg.Actions != nil {
		var err error
		execCtx, err = bindToolActionAttempt(ctx, cfg, "web_gather.fetch")
		if err != nil {
			return state, fmt.Errorf("bind source fetch: %w", err)
		}
	}
	collector.autoFetching = true
	result, runErr := collector.ExecuteV2(execCtx, "web_gather.fetch", map[string]any{"url": collector.sourceURLs[0], "store_staging": false})
	collector.autoFetching = false
	if cfg.Actions != nil {
		actionID, attemptID, ok := domainexecution.BoundActionAttemptFromContext(execCtx)
		if !ok {
			return state, fmt.Errorf("source fetch Action binding is unavailable")
		}
		if err := cfg.Actions.CompleteToolAttempt(execCtx, actionID, attemptID, result, runErr); err != nil {
			return state, fmt.Errorf("complete source fetch: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return state, err
	}
	state = collector.bodyFetchState()
	if !state.Attempted && runErr != nil {
		state.Reason = runErr.Error()
	}
	return state, nil
}
