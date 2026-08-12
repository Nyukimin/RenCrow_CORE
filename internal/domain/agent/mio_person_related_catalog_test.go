package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
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

func TestMioPersonRelatedCatalogIntentRoutesToChatBeforeGenericWildRules(t *testing.T) {
	ruleCalled := false
	classifierCalled := false
	mio := NewMioAgent(
		&mockLLMProvider{},
		&mockClassifier{classifyFunc: func(context.Context, task.Task) (routing.Decision, error) {
			classifierCalled = true
			return routing.NewDecision(routing.RouteWILD, 0.99, "generic creative classification"), nil
		}},
		&mockRuleDictionary{matchFunc: func(task.Task) (routing.Route, float64, bool) {
			ruleCalled = true
			return routing.RouteWILD, 0.99, true
		}},
		&mockToolRunner{},
		&mockMCPClient{},
		nil,
	)
	decision, err := mio.DecideAction(context.Background(), task.NewTask(task.NewJobID(), "東野圭吾の小説を教えて", "viewer", "user"))
	if err != nil {
		t.Fatal(err)
	}
	if decision.Route != routing.RouteCHAT || decision.Confidence != 1 {
		t.Fatalf("decision=%+v, want deterministic CHAT", decision)
	}
	if ruleCalled || classifierCalled {
		t.Fatalf("generic routing must not run for indexed catalog intent: rule=%t classifier=%t", ruleCalled, classifierCalled)
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
					"items": []any{map[string]any{
						"display_name":  "ドラマ1",
						"name_original": "Drama One",
						"name_state":    "source_ja",
						"relation_type": "known_for",
						"summary_state": "unavailable",
					}},
					"summary_coverage": map[string]any{"ready": 0, "unavailable": 1, "total": 1},
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

func TestMioPersonRelatedCatalogProjectsTwentyVerboseItemsWithoutInternalFields(t *testing.T) {
	items := make([]any, 20)
	for i := range items {
		items[i] = map[string]any{
			"relation_id":             fmt.Sprintf("internal-relation-%02d-%s", i, strings.Repeat("x", 320)),
			"person_ref_id":           fmt.Sprintf("internal-person-%02d", i),
			"movie_catalog_person_id": fmt.Sprintf("internal-movie-person-%02d", i),
			"item_id":                 fmt.Sprintf("internal-item-%02d", i),
			"source_record_id":        fmt.Sprintf("internal-source-%02d", i),
			"canonical_url":           fmt.Sprintf("https://internal.example/canonical/%02d/%s", i, strings.Repeat("c", 240)),
			"source":                  strings.Repeat("internal-source ", 100),
			"validation_state":        "validated",
			"item_type":               "book",
			"name_ja_source_url":      fmt.Sprintf("https://internal.example/name/%02d", i),
			"display_name":            fmt.Sprintf("日本語タイトル%02d", i),
			"name_original":           fmt.Sprintf("Original Title %02d", i),
			"name_ja":                 fmt.Sprintf("日本語タイトル%02d", i),
			"name_state":              "source_ja",
			"relation_type":           "known_for",
			"summary_ja":              fmt.Sprintf("日本語サマリ%02d", i),
			"summary_state":           "source_ja",
			"summary_source_url":      fmt.Sprintf("https://public.example/summary/%02d", i),
			"evidence_url":            fmt.Sprintf("https://public.example/evidence/%02d", i),
		}
	}
	result := map[string]any{
		"items":            items,
		"summary_coverage": map[string]any{"ready": 20, "unavailable": 0, "total": 20},
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) <= mioPersonRelatedCatalogContextMaxBytes {
		t.Fatalf("verbose fixture raw bytes=%d, want >%d", len(raw), mioPersonRelatedCatalogContextMaxBytes)
	}
	runner := &mockToolRunner{
		listFunc: func(context.Context) ([]tool.ToolMetadata, error) {
			return []tool.ToolMetadata{{ToolID: "person_related_catalog.lookup"}}, nil
		},
		executeV2Func: func(context.Context, string, map[string]any) (*tool.ToolResponse, error) {
			return tool.NewSuccess(result), nil
		},
	}
	mio := NewMioAgent(&mockLLMProvider{}, &mockClassifier{}, &mockRuleDictionary{}, runner, &mockMCPClient{}, nil)
	message := mio.personRelatedCatalogLookupContext(context.Background(), mioPersonRelatedCatalogLookup{Name: "役所広司", Category: "novel"})
	if len(message.Content) > mioPersonRelatedCatalogContextMaxBytes {
		t.Fatalf("compact context bytes=%d, want <=%d: %s", len(message.Content), mioPersonRelatedCatalogContextMaxBytes, message.Content)
	}
	for i := range items {
		for _, exact := range []string{fmt.Sprintf("日本語タイトル%02d", i), fmt.Sprintf("Original Title %02d", i)} {
			if !strings.Contains(message.Content, exact) {
				t.Fatalf("exact title %q missing from compact context", exact)
			}
		}
	}
	for _, internal := range []string{
		"relation_id", "person_ref_id", "movie_catalog_person_id", "item_id", "source_record_id",
		"canonical_url", "validation_state", "item_type", "name_ja_source_url", "internal-relation-00",
	} {
		if strings.Contains(message.Content, internal) {
			t.Fatalf("internal field/value %q leaked into compact context: %s", internal, message.Content)
		}
	}
	for _, marker := range []string{"summary_coverage", "\"ready\":20", "\"unavailable\":0", "\"total\":20", "summary_source_url", "evidence_url"} {
		if !strings.Contains(message.Content, marker) {
			t.Fatalf("required projection marker %q missing: %s", marker, message.Content)
		}
	}
}

func TestMioPersonRelatedCatalogMalformedResultFailsClosedWithoutRawFallback(t *testing.T) {
	malformed := map[string]any{
		"items": []any{map[string]any{
			"display_name": "表示すべきでないraw fallback",
			"relation_id":  "secret-relation-id",
		}},
		"summary_coverage": map[string]any{"ready": 1, "unavailable": 0, "total": 1},
	}
	runner := &mockToolRunner{
		listFunc: func(context.Context) ([]tool.ToolMetadata, error) {
			return []tool.ToolMetadata{{ToolID: "person_related_catalog.lookup"}}, nil
		},
		executeV2Func: func(context.Context, string, map[string]any) (*tool.ToolResponse, error) {
			return tool.NewSuccess(malformed), nil
		},
	}
	mio := NewMioAgent(&mockLLMProvider{}, &mockClassifier{}, &mockRuleDictionary{}, runner, &mockMCPClient{}, nil)
	message := mio.personRelatedCatalogLookupContext(context.Background(), mioPersonRelatedCatalogLookup{Name: "役所広司", Category: "novel"})
	if !strings.Contains(message.Content, "RenCrow indexed person-related catalog unavailable") {
		t.Fatalf("malformed result did not fail closed: %s", message.Content)
	}
	if strings.Contains(message.Content, "表示すべきでないraw fallback") || strings.Contains(message.Content, "secret-relation-id") {
		t.Fatalf("malformed raw result leaked into unavailable context: %s", message.Content)
	}
}

func TestMioPersonRelatedCatalogEmptyLookupCollectsThroughWorkerAndRequeries(t *testing.T) {
	lookupCalls := 0
	chatCollectCalls := 0
	chatRunner := &mockToolRunner{
		listFunc: func(context.Context) ([]tool.ToolMetadata, error) {
			return []tool.ToolMetadata{{ToolID: "person_related_catalog.lookup"}}, nil
		},
		executeV2Func: func(_ context.Context, name string, _ map[string]any) (*tool.ToolResponse, error) {
			if name == "person_related_catalog.collect" {
				chatCollectCalls++
			}
			lookupCalls++
			if lookupCalls == 1 {
				return tool.NewSuccess(map[string]any{"items": []any{}, "summary_coverage": map[string]any{"total": 0}}), nil
			}
			return tool.NewSuccess(map[string]any{
				"items": []any{map[string]any{
					"display_name":  "日本語ドラマ",
					"name_original": "Original Drama",
					"name_state":    "source_ja",
					"relation_type": "known_for",
					"summary_state": "unavailable",
				}},
				"summary_coverage": map[string]any{"ready": 0, "unavailable": 1, "total": 1},
			}), nil
		},
	}
	workerCalls := 0
	var workerArgs map[string]any
	workerRunner := &mockToolRunner{
		listFunc: func(context.Context) ([]tool.ToolMetadata, error) {
			return []tool.ToolMetadata{{ToolID: "person_related_catalog.collect"}}, nil
		},
		executeV2Func: func(_ context.Context, name string, args map[string]any) (*tool.ToolResponse, error) {
			if name != "person_related_catalog.collect" {
				t.Fatalf("worker executed unexpected tool %q", name)
			}
			workerCalls++
			workerArgs = args
			return tool.NewSuccess(map[string]any{"status": "ready", "stop_reason": "enough_validated_results"}), nil
		},
	}
	var captured llm.GenerateRequest
	provider := &mockLLMProvider{generateFunc: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
		captured = req
		return llm.GenerateResponse{Content: "見つけたよ。"}, nil
	}}
	mio := NewMioAgent(provider, &mockClassifier{}, &mockRuleDictionary{}, chatRunner, &mockMCPClient{}, nil).
		WithPersonRelatedCatalogCollector(workerRunner)
	if _, err := mio.Chat(context.Background(), task.NewTask(task.NewJobID(), "役所広司のドラマ", "viewer", "user")); err != nil {
		t.Fatal(err)
	}
	if lookupCalls != 2 || workerCalls != 1 || chatCollectCalls != 0 {
		t.Fatalf("lookup=%d worker_collect=%d chat_collect=%d", lookupCalls, workerCalls, chatCollectCalls)
	}
	if !reflect.DeepEqual(workerArgs, map[string]any{"person_name": "役所広司", "category": "drama"}) {
		t.Fatalf("worker args=%#v", workerArgs)
	}
	found := false
	for _, message := range captured.Messages {
		if message.Type == llm.PromptContextRecall && strings.Contains(message.Content, "日本語ドラマ") {
			found = true
		}
	}
	if !found {
		t.Fatalf("post-collection lookup context missing: %#v", captured.Messages)
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
