package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

func TestParseMioDataToolLookup(t *testing.T) {
	tests := []struct {
		message string
		toolID  string
		args    map[string]any
		ok      bool
	}{
		{"RenCrowにはどんなDBがある？", "data_capability.describe", map[string]any{"operation": "list_catalog"}, true},
		{"RenCrowで今使えるデータは？", "data_capability.describe", map[string]any{"operation": "list_available"}, true},
		{"用語DBは使える？", "data_capability.describe", map[string]any{"operation": "describe", "name": "glossary"}, true},
		{"RenCrowの用語集で「CUDA」を調べて", "glossary.lookup", map[string]any{"operation": "define_term", "term": "CUDA", "limit": 10}, true},
		{"用語集の「tech」カテゴリを見せて", "glossary.lookup", map[string]any{"operation": "list_category", "category": "tech", "limit": 10}, true},
		{`RenCrowの用語集で"CUDA"の意味を教えて`, "glossary.lookup", map[string]any{"operation": "define_term", "term": "CUDA", "limit": 10}, true},
		{"CUDAって何？", "", nil, false},
		{"用語集を見せて", "", nil, false},
		{"用語集の「カテゴリ」カテゴリを見せて", "", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.message, func(t *testing.T) {
			got, ok := parseMioDataToolLookup(tt.message)
			if ok != tt.ok || got.ToolID != tt.toolID || !reflect.DeepEqual(got.Args, tt.args) {
				t.Fatalf("got=%+v ok=%t want tool=%q args=%#v ok=%t", got, ok, tt.toolID, tt.args, tt.ok)
			}
		})
	}
}

func TestMioDataToolIntentsExecuteOnceAndSuppressWeb(t *testing.T) {
	tests := []struct {
		message string
		toolID  string
		args    map[string]any
	}{
		{"RenCrowにはどんなDBがある？", "data_capability.describe", map[string]any{"operation": "list_catalog"}},
		{"RenCrowで今使えるデータは？", "data_capability.describe", map[string]any{"operation": "list_available"}},
		{"用語DBは使える？", "data_capability.describe", map[string]any{"operation": "describe", "name": "glossary"}},
		{"RenCrowの用語集で「CUDA」を調べて", "glossary.lookup", map[string]any{"operation": "define_term", "term": "CUDA", "limit": 10}},
		{"用語集の「tech」カテゴリを見せて", "glossary.lookup", map[string]any{"operation": "list_category", "category": "tech", "limit": 10}},
	}
	for _, tt := range tests {
		t.Run(tt.message, func(t *testing.T) {
			calls, webCalls := 0, 0
			var gotArgs map[string]any
			runner := &mockToolRunner{
				listFunc: func(context.Context) ([]tool.ToolMetadata, error) {
					return []tool.ToolMetadata{{ToolID: tt.toolID}, {ToolID: "web_search"}}, nil
				},
				executeV2Func: func(_ context.Context, name string, args map[string]any) (*tool.ToolResponse, error) {
					if name == "web_search" {
						webCalls++
						return tool.NewSuccess(nil), nil
					}
					calls++
					if name != tt.toolID {
						t.Fatalf("tool=%q want %q", name, tt.toolID)
					}
					gotArgs = args
					return tool.NewSuccess(map[string]any{"matched": true}), nil
				},
			}
			var captured llm.GenerateRequest
			provider := &mockLLMProvider{generateFunc: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
				captured = req
				return llm.GenerateResponse{Content: "結果だよ。"}, nil
			}}
			mio := NewMioAgent(provider, &mockClassifier{}, &mockRuleDictionary{}, runner, &mockMCPClient{}, nil)
			if _, err := mio.Chat(context.Background(), task.NewTask(task.NewJobID(), tt.message, "viewer", "user")); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || webCalls != 0 || !reflect.DeepEqual(gotArgs, tt.args) {
				t.Fatalf("calls=%d web=%d args=%#v", calls, webCalls, gotArgs)
			}
			if !hasMioDataContext(captured.Messages, "RenCrow indexed data result; answer only from it") {
				t.Fatalf("indexed context missing: %#v", captured.Messages)
			}
		})
	}
}

func TestMioDataToolUnavailableOrErrorSuppressesWeb(t *testing.T) {
	for _, tc := range []struct {
		name     string
		list     []tool.ToolMetadata
		execErr  error
		response *tool.ToolResponse
	}{
		{name: "missing", list: []tool.ToolMetadata{{ToolID: "web_search"}}},
		{name: "error", list: []tool.ToolMetadata{{ToolID: "glossary.lookup"}, {ToolID: "web_search"}}, execErr: errors.New("failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			webCalls := 0
			runner := &mockToolRunner{
				listFunc: func(context.Context) ([]tool.ToolMetadata, error) { return tc.list, nil },
				executeV2Func: func(_ context.Context, name string, _ map[string]any) (*tool.ToolResponse, error) {
					if name == "web_search" {
						webCalls++
					}
					return tc.response, tc.execErr
				},
			}
			var captured llm.GenerateRequest
			provider := &mockLLMProvider{generateFunc: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
				captured = req
				return llm.GenerateResponse{Content: "今は使えないよ。"}, nil
			}}
			mio := NewMioAgent(provider, &mockClassifier{}, &mockRuleDictionary{}, runner, &mockMCPClient{}, nil)
			if _, err := mio.Chat(context.Background(), task.NewTask(task.NewJobID(), "RenCrowの用語集で「CUDA」を検索して", "viewer", "user")); err != nil {
				t.Fatal(err)
			}
			if webCalls != 0 || !hasMioDataContext(captured.Messages, "RenCrow indexed data unavailable") {
				t.Fatalf("web=%d messages=%#v", webCalls, captured.Messages)
			}
		})
	}
}

func TestMioGenericTermQuestionDoesNotUseGlossary(t *testing.T) {
	calls := 0
	runner := &mockToolRunner{executeV2Func: func(context.Context, string, map[string]any) (*tool.ToolResponse, error) {
		calls++
		return tool.NewSuccess(nil), nil
	}}
	mio := NewMioAgent(&mockLLMProvider{}, &mockClassifier{}, &mockRuleDictionary{}, runner, &mockMCPClient{}, nil)
	if _, err := mio.Chat(context.Background(), task.NewTask(task.NewJobID(), "CUDAって何？", "viewer", "user")); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("unexpected tool calls=%d", calls)
	}
}

func hasMioDataContext(messages []llm.Message, marker string) bool {
	for _, message := range messages {
		if message.Type == llm.PromptContextRecall && strings.Contains(message.Content, marker) {
			return true
		}
	}
	return false
}
