package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	webgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
)

type mioWebQueryStub struct {
	calls    []string
	args     []map[string]any
	fail     bool
	response *tool.ToolResponse
}

func (*mioWebQueryStub) ListTools(context.Context) ([]tool.ToolMetadata, error) {
	return []tool.ToolMetadata{{ToolID: "web_gather.search_and_fetch", Category: "query"}, {ToolID: "web_gather.fetch", Category: "query"}}, nil
}

func (r *mioWebQueryStub) ExecuteV2(_ context.Context, name string, args map[string]any) (*tool.ToolResponse, error) {
	r.calls = append(r.calls, name)
	r.args = append(r.args, args)
	if r.fail {
		return nil, errors.New("upstream unavailable")
	}
	if r.response != nil {
		return r.response, nil
	}
	fetched := webgather.FetchResponse{URL: "https://example.org", Status: "ok", HTTPStatus: 200, ExtractedChars: 20, TextPreview: "actual public source"}
	if name == "web_gather.fetch" {
		return tool.NewSuccess(fetched), nil
	}
	return tool.NewSuccess(webgather.SearchAndFetchResponse{Items: []webgather.SearchAndFetchItem{{SearchResult: webgather.SearchResult{URL: fetched.URL, Title: "source"}, Fetch: fetched}}}), nil
}

func TestRuntimeMioWebRetrievalUsesExistingPolicyRunnerForSearchAndURL(t *testing.T) {
	for _, query := range []string{"current model release", "https://example.org"} {
		runner := &mioWebQueryStub{}
		port := newRuntimeMioRetrieval(nil, nil).withWebQueries(runner)
		result, err := port.Retrieve(authenticatedMioRetrievalContext(t, "ren"), conversation.TurnInput{}, domainagent.MioRetrievalRequest{SourceKind: domainagent.MioRetrievalSourceWebSearch, Query: query})
		if err != nil || result.Status != domainagent.MioRetrievalStatusFound || len(result.Items) != 1 || !strings.Contains(result.Items[0].Text, "actual public source") || len(runner.calls) != 1 {
			t.Fatalf("Web source was not delivered: %#v, calls=%v err=%v", result, runner.calls, err)
		}
		want := "web_gather.search_and_fetch"
		if runner.args[0]["store_staging"] != false {
			t.Fatal("Mio retrieval permitted staging writes")
		}
		if strings.HasPrefix(query, "https:") {
			want = "web_gather.fetch"
			if runner.args[0]["store_staging"] != false {
				t.Fatal("Mio URL retrieval permitted staging writes")
			}
			if _, exists := runner.args[0]["policy"]; exists {
				t.Fatal("Mio must use the registered fetch policy, not a private 16KB limit")
			}
		}
		if runner.calls[0] != want {
			t.Fatalf("query %q dispatched %s, want %s", query, runner.calls[0], want)
		}
	}
}

func TestRuntimeMioWebRetrievalClassifiesEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, query string
		data        any
		want        domainagent.MioRetrievalStatus
	}{
		{"empty search", "query", webgather.SearchAndFetchResponse{Items: []webgather.SearchAndFetchItem{}}, domainagent.MioRetrievalStatusNotFound},
		{"snippet only", "query", webgather.SearchAndFetchResponse{Items: []webgather.SearchAndFetchItem{{SearchResult: webgather.SearchResult{URL: "https://example.org", Snippet: "search evidence"}, Fetch: webgather.FetchResponse{Status: "failed"}}}}, domainagent.MioRetrievalStatusPartial},
		{"failed fetch", "https://example.org", webgather.FetchResponse{URL: "https://example.org", Status: "failed", HTTPStatus: 451}, domainagent.MioRetrievalStatusUnavailable},
		{"malformed", "query", map[string]any{"unexpected": "not search evidence"}, domainagent.MioRetrievalStatusUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &mioWebQueryStub{response: tool.NewSuccess(tc.data)}
			got, err := newRuntimeMioRetrieval(nil, nil).withWebQueries(runner).Retrieve(authenticatedMioRetrievalContext(t, "ren"), conversation.TurnInput{}, domainagent.MioRetrievalRequest{SourceKind: domainagent.MioRetrievalSourceWebSearch, Query: tc.query})
			if err != nil || got.Status != tc.want {
				t.Fatalf("got %#v, err %v; want %s", got, err, tc.want)
			}
		})
	}
}

func TestRuntimeMioWebRetrievalKeepsFailuresUnavailable(t *testing.T) {
	for _, query := range []string{"https://user:secret@example.org", "https://", "ordinary query"} {
		runner := &mioWebQueryStub{fail: true}
		result, err := newRuntimeMioRetrieval(nil, nil).withWebQueries(runner).Retrieve(authenticatedMioRetrievalContext(t, "ren"), conversation.TurnInput{}, domainagent.MioRetrievalRequest{SourceKind: domainagent.MioRetrievalSourceWebSearch, Query: query})
		if err != nil || result.Status != domainagent.MioRetrievalStatusUnavailable || len(result.Items) != 0 {
			t.Fatalf("failure became empty success: %#v, %v", result, err)
		}
		if strings.HasPrefix(query, "https:") && len(runner.calls) != 0 {
			t.Fatal("malformed or credential-bearing URL reached the fetcher")
		}
	}
}
