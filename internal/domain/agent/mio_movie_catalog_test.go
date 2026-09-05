package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

func TestParseMioMovieCatalogLookup(t *testing.T) {
	tests := []struct {
		message string
		kind    string
		name    string
		info    string
		ok      bool
	}{
		{"果てしなきスカーレットってどんな映画？", "movie", "果てしなきスカーレット", "overview", true},
		{"映画「果てしなきスカーレット」のあらすじを教えて", "movie", "果てしなきスカーレット", "overview", true},
		{"映画「果てしなきスカーレット」のキャスト", "movie", "果てしなきスカーレット", "cast", true},
		{"映画「果てしなきスカーレット」のスタッフ", "movie", "果てしなきスカーレット", "staff", true},
		{"役所広司ってどんな俳優？", "person", "役所広司", "profile", true},
		{"役所広司の出演映画", "person", "役所広司", "filmography", true},
		{"映画DBで映画「PERFECT DAYS」の出演者を教えて", "movie", "PERFECT DAYS", "cast", true},
		{"役者名から調べて", "", "", "", false},
		{"役者名の出演映画を教えて", "", "", "", false},
		{"映画ってどんな映画？", "", "", "", false},
		{"PERFECT DAYSについて教えて", "", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.message, func(t *testing.T) {
			got, ok := parseMioMovieCatalogLookup(tt.message)
			if ok != tt.ok || got.Kind != tt.kind || got.Name != tt.name || got.Information != tt.info {
				t.Fatalf("got=%+v ok=%t want kind=%q name=%q info=%q ok=%t", got, ok, tt.kind, tt.name, tt.info, tt.ok)
			}
		})
	}
}

func TestMioMovieCatalogLookupInjectsResultAndSuppressesWebSearch(t *testing.T) {
	movieCalls, webCalls := 0, 0
	var gotArgs map[string]any
	runner := &mockToolRunner{
		listFunc: func(context.Context) ([]tool.ToolMetadata, error) {
			return []tool.ToolMetadata{{ToolID: "movie_catalog.lookup"}, {ToolID: "web_search"}}, nil
		},
		executeV2Func: func(_ context.Context, name string, args map[string]any) (*tool.ToolResponse, error) {
			switch name {
			case "movie_catalog.lookup":
				movieCalls++
				gotArgs = args
				return tool.NewSuccess(map[string]any{"kind": "movie", "title": "PERFECT DAYS", "cast": []string{"役所広司"}}), nil
			case "web_search":
				webCalls++
			}
			return tool.NewSuccess("unexpected"), nil
		},
	}
	var captured llm.GenerateRequest
	provider := &mockLLMProvider{generateFunc: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
		captured = req
		return llm.GenerateResponse{Content: "役所広司が出演してるよ。"}, nil
	}}
	mio := NewMioAgent(provider, &mockClassifier{}, &mockRuleDictionary{}, runner, &mockMCPClient{}, nil)
	_, err := mio.Chat(context.Background(), newAgentTurnInput(t, "映画「PERFECT DAYS」の出演者を検索して", "viewer", "user"))
	if err != nil {
		t.Fatal(err)
	}
	if movieCalls != 1 || webCalls != 0 {
		t.Fatalf("movie calls=%d web calls=%d", movieCalls, webCalls)
	}
	if gotArgs["kind"] != "movie" || gotArgs["name"] != "PERFECT DAYS" || gotArgs["information"] != "cast" || gotArgs["limit"] != 10 {
		t.Fatalf("unexpected args: %#v", gotArgs)
	}
	found := false
	for _, message := range captured.Messages {
		if message.Type == llm.PromptContextRecall && strings.Contains(message.Content, "RenCrow indexed catalog result; answer only from it") && strings.Contains(message.Content, "役所広司") {
			found = true
		}
	}
	if !found {
		t.Fatalf("catalog result context missing: %#v", captured.Messages)
	}
}

func TestMioMovieCatalogUnavailableSuppressesWebFallback(t *testing.T) {
	webCalls := 0
	runner := &mockToolRunner{
		listFunc: func(context.Context) ([]tool.ToolMetadata, error) {
			return []tool.ToolMetadata{{ToolID: "web_search"}}, nil
		},
		executeV2Func: func(_ context.Context, name string, _ map[string]any) (*tool.ToolResponse, error) {
			if name == "web_search" {
				webCalls++
			}
			return tool.NewSuccess("unexpected"), nil
		},
	}
	var captured llm.GenerateRequest
	provider := &mockLLMProvider{generateFunc: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
		captured = req
		return llm.GenerateResponse{Content: "今は照会できないよ。"}, nil
	}}
	mio := NewMioAgent(provider, &mockClassifier{}, &mockRuleDictionary{}, runner, &mockMCPClient{}, nil)
	_, err := mio.Chat(context.Background(), newAgentTurnInput(t, "役所広司の出演映画を検索して", "viewer", "user"))
	if err != nil {
		t.Fatal(err)
	}
	if webCalls != 0 {
		t.Fatalf("web fallback called %d times", webCalls)
	}
	found := false
	for _, message := range captured.Messages {
		if strings.Contains(message.Content, "RenCrow indexed catalog unavailable") && strings.Contains(message.Content, "fallback") {
			found = true
		}
	}
	if !found {
		t.Fatalf("unavailable context missing: %#v", captured.Messages)
	}
}

func TestMioMovieCatalogAllSemanticIntentsUseCatalogWithoutWeb(t *testing.T) {
	requests := []string{
		"果てしなきスカーレットってどんな映画？",
		"映画「果てしなきスカーレット」のあらすじを教えて",
		"映画「果てしなきスカーレット」のキャスト",
		"映画「果てしなきスカーレット」のスタッフ",
		"役所広司ってどんな俳優？",
		"役所広司の出演映画",
	}
	for _, request := range requests {
		t.Run(request, func(t *testing.T) {
			catalogCalls, webCalls := 0, 0
			runner := &mockToolRunner{
				listFunc: func(context.Context) ([]tool.ToolMetadata, error) {
					return []tool.ToolMetadata{{ToolID: "movie_catalog.lookup"}, {ToolID: "web_search"}}, nil
				},
				executeV2Func: func(_ context.Context, name string, args map[string]any) (*tool.ToolResponse, error) {
					if name == "movie_catalog.lookup" {
						catalogCalls++
						if strings.TrimSpace(args["information"].(string)) == "" {
							t.Fatal("information is required")
						}
						return tool.NewSuccess(map[string]any{"matched": true}), nil
					}
					if name == "web_search" {
						webCalls++
					}
					return tool.NewSuccess(nil), nil
				},
			}
			mio := NewMioAgent(&mockLLMProvider{}, &mockClassifier{}, &mockRuleDictionary{}, runner, &mockMCPClient{}, nil)
			if _, err := mio.Chat(context.Background(), newAgentTurnInput(t, request, "viewer", "user")); err != nil {
				t.Fatal(err)
			}
			if catalogCalls != 1 || webCalls != 0 {
				t.Fatalf("catalog=%d web=%d", catalogCalls, webCalls)
			}
		})
	}
}
