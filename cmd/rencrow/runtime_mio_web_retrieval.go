package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	webgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
)

// retrieveWebQuery supplies the already-existing logical Web retrieval source
// through the policy runner. A public URL requests its body; other input is a
// search query. UserMemory qualifications and publication fences are untouched.
func (r *runtimeMioRetrieval) retrieveWebQuery(ctx context.Context, query string) (domainagent.MioRetrievalResult, error) {
	source := domainagent.MioRetrievalSourceWebSearch
	unavailable := runtimeMioRetrievalResult(source, domainagent.MioRetrievalStatusUnavailable)
	if r.webQueries == nil {
		return unavailable, nil
	}
	metadata, err := r.webQueries.ListTools(ctx)
	if err != nil {
		return unavailable, nil
	}
	name := "web_gather.search_and_fetch"
	arguments := map[string]any{"query": query, "limit": runtimeMioRetrievalMaxItems, "max_fetches": 2, "store_staging": false}
	if strings.HasPrefix(strings.ToLower(query), "http:") || strings.HasPrefix(strings.ToLower(query), "https:") {
		parsed, err := url.Parse(query)
		if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.Scheme != "http" && parsed.Scheme != "https" {
			return unavailable, nil
		}
		name = "web_gather.fetch"
		arguments = map[string]any{"url": query, "store_staging": false}
	}
	registered := false
	for _, meta := range metadata {
		if meta.ToolID == name && meta.Category == "query" && !meta.Deprecated {
			registered = true
			break
		}
	}
	if !registered {
		return unavailable, nil
	}
	response, err := r.webQueries.ExecuteV2(ctx, name, arguments)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return unavailable, err
		}
		return unavailable, nil
	}
	if response == nil || response.IsError() {
		return unavailable, nil
	}
	encoded, err := json.Marshal(response.Result)
	// Two requests must still fit the continuation's shared 64 KiB envelope.
	if err != nil || len(encoded) > 28*1024 {
		return unavailable, nil
	}
	status := domainagent.MioRetrievalStatusFound
	if name == "web_gather.fetch" {
		var fetched webgather.FetchResponse
		if json.Unmarshal(encoded, &fetched) != nil || !runtimeMioFetchedBody(fetched) {
			return unavailable, nil
		}
		if fetched.TextTruncated {
			status = domainagent.MioRetrievalStatusPartial
		}
	} else {
		var results webgather.SearchAndFetchResponse
		if json.Unmarshal(encoded, &results) != nil || results.Items == nil || len(results.Items) > 2 {
			return unavailable, nil
		}
		if len(results.Items) == 0 {
			return runtimeMioRetrievalResult(source, domainagent.MioRetrievalStatusNotFound), nil
		}
		for _, item := range results.Items {
			if _, err := webgather.NormalizeURL(item.SearchResult.URL, false); err != nil {
				return unavailable, nil
			}
			if !runtimeMioFetchedBody(item.Fetch) || item.Fetch.TextTruncated {
				status = domainagent.MioRetrievalStatusPartial
			}
		}
	}
	return domainagent.MioRetrievalResult{
		SourceKind: source, Status: status,
		Items: []domainagent.MioRetrievalItem{{Text: string(encoded)}},
	}, nil
}

func runtimeMioFetchedBody(response webgather.FetchResponse) bool {
	_, err := webgather.NormalizeURL(response.URL, false)
	return err == nil && response.Status == "ok" && response.HTTPStatus >= 200 && response.HTTPStatus < 300 &&
		response.ExtractedChars > 0 && strings.TrimSpace(response.TextExcerpt+response.TextPreview) != ""
}
