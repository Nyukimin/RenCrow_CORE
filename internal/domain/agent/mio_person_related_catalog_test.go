package agent

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

func TestParseMioPersonRelatedCatalogLookup(t *testing.T) {
	tests := []struct {
		message  string
		name     string
		category string
		ok       bool
	}{
		{"役所広司のドラマ", "役所広司", "drama", true},
		{"役所広司の受賞歴", "役所広司", "award", true},
		{"役所広司の音楽作品", "役所広司", "music", true},
		{"役所広司の音楽", "役所広司", "music", true},
		{"役所広司のアニメ", "役所広司", "anime", true},
		{"役所広司の小説", "役所広司", "novel", true},
		{"役所広司の漫画", "役所広司", "manga", true},
		{"役所広司さんのドラマを教えて", "役所広司", "drama", true},
		{"役所広司の受賞歴について調べて", "役所広司", "award", true},
		{"役所広司の出演映画", "", "", false},
		{"人物のドラマ", "", "", false},
		{"俳優の漫画", "", "", false},
		{"役者のアニメ", "", "", false},
		{"○○の小説", "", "", false},
		{"役所広司についてのドラマ", "", "", false},
		{"役所広司のニュース", "", "", false},
		{"役所広司はどんな人？", "", "", false},
		{"", "", "", false},
		{strings.Repeat("あ", 161) + "のドラマ", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.message, func(t *testing.T) {
			got, ok := parseMioPersonRelatedCatalogLookup(tt.message)
			if ok != tt.ok || got.Name != tt.name || got.Category != tt.category {
				t.Fatalf("got=%+v ok=%t want name=%q category=%q ok=%t", got, ok, tt.name, tt.category, tt.ok)
			}
		})
	}
}

func TestMioPersonRelatedCatalogExecutesOnceAndInjectsIndexedContext(t *testing.T) {
	var calls, movieCalls, webCalls int
	var gotName, gotCategory string
	var gotLimit int
	runner := &mockToolRunner{
		listFunc: func(context.Context) ([]tool.ToolMetadata, error) {
			return []tool.ToolMetadata{{ToolID: "person_related_catalog.lookup"}, {ToolID: "movie_catalog.lookup"}, {ToolID: "web_search"}}, nil
		},
		executeV2Func: func(_ context.Context, name string, args map[string]any) (*tool.ToolResponse, error) {
			switch name {
			case "person_related_catalog.lookup":
				calls++
				gotName, _ = args["person_name"].(string)
				gotCategory, _ = args["category"].(string)
				gotLimit, _ = args["limit"].(int)
				return tool.NewSuccess(map[string]any{
					"display_name":  "ドラマ1",
					"name_original": "Drama One",
					"category":      "drama",
				}), nil
			case "movie_catalog.lookup":
				movieCalls++
			case "web_search":
				webCalls++
			}
			return tool.NewSuccess(nil), nil
		},
	}
	var captured llm.GenerateRequest
	provider := &mockLLMProvider{generateFunc: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
		captured = req
		return llm.GenerateResponse{Content: "結果だよ。"}, nil
	}}
	mio := NewMioAgent(provider, &mockClassifier{}, &mockRuleDictionary{}, runner, &mockMCPClient{}, nil)
	if _, err := mio.Chat(context.Background(), task.NewTask(task.NewJobID(), "役所広司のドラマ", "viewer", "user")); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || movieCalls != 0 || webCalls != 0 {
		t.Fatalf("related=%d movie=%d web=%d", calls, movieCalls, webCalls)
	}
	if gotName != "役所広司" || gotCategory != "drama" || gotLimit != 20 {
		t.Fatalf("unexpected related args name=%q category=%q limit=%d", gotName, gotCategory, gotLimit)
	}
	var contextMessage *llm.Message
	for i := range captured.Messages {
		if captured.Messages[i].Type == llm.PromptContextRecall && strings.Contains(captured.Messages[i].Content, "person_related_catalog.lookup") {
			contextMessage = &captured.Messages[i]
			break
		}
	}
	if contextMessage == nil {
		t.Fatalf("indexed related context missing: %#v", captured.Messages)
	}
	for _, marker := range []string{"answer only from it", "display_name", "name_original", "translate"} {
		if !strings.Contains(strings.ToLower(contextMessage.Content), strings.ToLower(marker)) {
			t.Fatalf("context marker %q missing: %s", marker, contextMessage.Content)
		}
	}
}

func TestMioPersonRelatedCatalogPreservesMovieParserFirst(t *testing.T) {
	var relatedCalls, movieCalls, webCalls int
	var gotMovieArgs map[string]any
	runner := &mockToolRunner{
		listFunc: func(context.Context) ([]tool.ToolMetadata, error) {
			return []tool.ToolMetadata{{ToolID: "movie_catalog.lookup"}, {ToolID: "person_related_catalog.lookup"}, {ToolID: "web_search"}}, nil
		},
		executeV2Func: func(_ context.Context, name string, args map[string]any) (*tool.ToolResponse, error) {
			switch name {
			case "movie_catalog.lookup":
				movieCalls++
				gotMovieArgs = args
			case "person_related_catalog.lookup":
				relatedCalls++
			case "web_search":
				webCalls++
			}
			return tool.NewSuccess(map[string]any{"matched": true}), nil
		},
	}
	mio := NewMioAgent(&mockLLMProvider{}, &mockClassifier{}, &mockRuleDictionary{}, runner, &mockMCPClient{}, nil)
	if _, err := mio.Chat(context.Background(), task.NewTask(task.NewJobID(), "役所広司の出演映画", "viewer", "user")); err != nil {
		t.Fatal(err)
	}
	if relatedCalls != 0 || movieCalls != 1 || webCalls != 0 {
		t.Fatalf("related=%d movie=%d web=%d", relatedCalls, movieCalls, webCalls)
	}
	want := map[string]any{"kind": "person", "name": "役所広司", "information": "filmography", "limit": 10}
	if !reflect.DeepEqual(gotMovieArgs, want) {
		t.Fatalf("movie args=%#v want=%#v", gotMovieArgs, want)
	}
}

func TestMioPersonRelatedCatalogUnavailableOrErrorSuppressesWeb(t *testing.T) {
	tests := []struct {
		name     string
		metadata []tool.ToolMetadata
		response *tool.ToolResponse
		err      error
	}{
		{name: "missing", metadata: []tool.ToolMetadata{{ToolID: "web_search"}}},
		{name: "error", metadata: []tool.ToolMetadata{{ToolID: "person_related_catalog.lookup"}, {ToolID: "web_search"}}, response: tool.NewError(tool.ErrInternalError, "failed", nil)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			webCalls := 0
			runner := &mockToolRunner{
				listFunc: func(context.Context) ([]tool.ToolMetadata, error) { return tt.metadata, nil },
				executeV2Func: func(_ context.Context, name string, _ map[string]any) (*tool.ToolResponse, error) {
					if name == "web_search" {
						webCalls++
					}
					return tt.response, tt.err
				},
			}
			var captured llm.GenerateRequest
			provider := &mockLLMProvider{generateFunc: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
				captured = req
				return llm.GenerateResponse{Content: "今は照会できないよ。"}, nil
			}}
			mio := NewMioAgent(provider, &mockClassifier{}, &mockRuleDictionary{}, runner, &mockMCPClient{}, nil)
			if _, err := mio.Chat(context.Background(), task.NewTask(task.NewJobID(), "役所広司のドラマを検索して", "viewer", "user")); err != nil {
				t.Fatal(err)
			}
			if webCalls != 0 {
				t.Fatalf("web fallback called %d times", webCalls)
			}
			found := false
			for _, message := range captured.Messages {
				if message.Type == llm.PromptContextRecall && strings.Contains(message.Content, "RenCrow indexed person-related catalog unavailable") && strings.Contains(message.Content, "fallback") {
					found = true
				}
			}
			if !found {
				t.Fatalf("unavailable context missing: %#v", captured.Messages)
			}
		})
	}
}

func TestMioPersonRelatedCatalogGenericQuestionDoesNotUseTool(t *testing.T) {
	calls := 0
	runner := &mockToolRunner{
		listFunc: func(context.Context) ([]tool.ToolMetadata, error) {
			return []tool.ToolMetadata{{ToolID: "person_related_catalog.lookup"}}, nil
		},
		executeV2Func: func(context.Context, string, map[string]any) (*tool.ToolResponse, error) {
			calls++
			return tool.NewSuccess(nil), nil
		},
	}
	mio := NewMioAgent(&mockLLMProvider{}, &mockClassifier{}, &mockRuleDictionary{}, runner, &mockMCPClient{}, nil)
	if _, err := mio.Chat(context.Background(), task.NewTask(task.NewJobID(), "俳優のドラマについて教えて", "viewer", "user")); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("unexpected related catalog calls=%d", calls)
	}
}
