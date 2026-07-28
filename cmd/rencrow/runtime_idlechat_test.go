package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/idlechat"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
)

func TestHandleIdleChatStatusIncludesForecastStockSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forecast_topic_stock.json")
	data, err := json.Marshal(map[string]any{"stock": map[string]any{
		"AI技術": []map[string]any{{
			"topic":   "保存済みのAI技術お題",
			"seeds":   []string{"seed"},
			"created": time.Now().UTC(),
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	orch := idlechat.NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")
	orch.InitForecastTopicStock(path)
	deps := &Dependencies{idleChatOrch: orch}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/viewer/idlechat/status", nil)

	deps.handleIdleChatStatus().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		ForecastStock idlechat.ForecastTopicStockSnapshot `json:"forecast_stock"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if !payload.ForecastStock.Enabled || payload.ForecastStock.Total != 1 {
		t.Fatalf("forecast stock snapshot = %+v", payload.ForecastStock)
	}
	if got := payload.ForecastStock.Domains[0].Topics[0].Topic; got != "保存済みのAI技術お題" {
		t.Fatalf("forecast topic = %q", got)
	}
}

func TestHandleIdleChatCollectionReturnsReadOnlySnapshot(t *testing.T) {
	orch := idlechat.NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")
	deps := &Dependencies{idleChatOrch: orch}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/viewer/idlechat/collection", nil)

	deps.handleIdleChatCollection().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		OK         bool                                 `json:"ok"`
		Collection idlechat.DailySeedCollectionSnapshot `json:"collection"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if !payload.OK || payload.Collection.Schedule != "04:00" || payload.Collection.Timezone != "JST" {
		t.Fatalf("collection payload = %+v", payload)
	}

	postRecorder := httptest.NewRecorder()
	deps.handleIdleChatCollection().ServeHTTP(postRecorder, httptest.NewRequest(http.MethodPost, "/viewer/idlechat/collection", nil))
	if postRecorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d", postRecorder.Code)
	}
}

func TestIdleChatNewsSourceConfigFromRuntimeResolvesTokenWithoutChangingQueries(t *testing.T) {
	t.Setenv("RENCROW_X_BEARER_TOKEN", "secret-token")
	redditEnabled := true

	got := idleChatNewsSourceConfigFromRuntime(config.IdleChatNewsSourcesConfig{
		Reddit: config.IdleChatRedditNewsSourceConfig{
			Enabled:     &redditEnabled,
			Communities: []string{"technology", "science"},
			Limit:       8,
		},
		X: config.IdleChatXNewsSourceConfig{
			Enabled:        true,
			BearerTokenEnv: "RENCROW_X_BEARER_TOKEN",
			Queries: []config.IdleChatXNewsQueryConfig{
				{Name: "X AI", Category: "tech", Query: "AI lang:ja -is:retweet", Limit: 10},
			},
		},
	})

	if !got.RedditEnabled || got.RedditLimit != 8 || len(got.RedditCommunities) != 2 {
		t.Fatalf("unexpected Reddit runtime config: %+v", got)
	}
	if !got.XEnabled || got.XBearerToken != "secret-token" || len(got.XQueries) != 1 {
		t.Fatalf("unexpected X runtime config")
	}
	if got.XQueries[0].Query != "AI lang:ja -is:retweet" {
		t.Fatalf("X query = %q", got.XQueries[0].Query)
	}
}

func TestSelectForecastProviderPrefersCoderPriorityOverWorker(t *testing.T) {
	worker := fakeConversationProvider{name: "worker-provider"}
	chat := fakeConversationProvider{name: "chat-provider"}
	provider, label := selectForecastProvider(&config.Config{
		Coder2: config.CoderConfig{
			Enabled: true,
		},
		Coder1: config.CoderConfig{
			Enabled: true,
		},
	}, chat, worker, nil)

	if provider == nil || provider == worker || provider == chat {
		t.Fatalf("expected Coder1 provider, got %#v", provider)
	}
	if label != "Coder1 via RenCrow_LLM" {
		t.Fatalf("unexpected label: %q", label)
	}
}

func TestSelectForecastProviderUsesFirstEnabledCoderAlias(t *testing.T) {
	primary, primaryLabel := selectForecastProviders(&config.Config{
		Coder1: config.CoderConfig{
			Enabled: true,
		},
		Coder2: config.CoderConfig{
			Enabled: true,
		},
	})

	if primary == nil {
		t.Fatal("expected Coder1 RenCrow_LLM Gateway provider")
	}
	if primaryLabel != "Coder1 via RenCrow_LLM" {
		t.Fatalf("unexpected primary label: %q", primaryLabel)
	}
}

func TestSelectForecastProviderRoutesCoderAliasThroughGateway(t *testing.T) {
	primary, primaryLabel := selectForecastProviders(&config.Config{
		Coder1: config.CoderConfig{
			Enabled: true,
		},
		Coder2: config.CoderConfig{
			Enabled: true,
		},
	})

	if primary == nil || primaryLabel != "Coder1 via RenCrow_LLM" {
		t.Fatalf("expected Coder1 Gateway provider; got provider=%#v label=%q", primary, primaryLabel)
	}
}

func TestSelectForecastProviderDoesNotUseChatWhenNoCoderAvailable(t *testing.T) {
	chat := fakeConversationProvider{name: "chat-provider"}
	provider, label := selectForecastProvider(&config.Config{}, chat, nil, nil)

	if provider != nil {
		t.Fatalf("Forecast must not fall back to Chat provider, got %#v", provider)
	}
	if label != "" {
		t.Fatalf("unexpected label: %q", label)
	}
}

func TestSelectForecastProviderUsesConfiguredCoderThroughGateway(t *testing.T) {
	worker := fakeConversationProvider{name: "worker-provider"}
	chat := fakeConversationProvider{name: "chat-provider"}
	provider, label := selectForecastProvider(&config.Config{
		Coder2: config.CoderConfig{
			Enabled: true,
		},
	}, chat, worker, nil)

	if provider == nil || provider.Name() != "rencrow_llm-coder2" {
		t.Fatalf("expected logical Coder2 Gateway provider, got %#v", provider)
	}
	if label != "Coder2 via RenCrow_LLM" {
		t.Fatalf("unexpected label: %q", label)
	}
}

func TestSelectForecastProviderFallsBackToGatewayWorker(t *testing.T) {
	worker := fakeConversationProvider{name: "worker-provider"}
	provider, label := selectForecastProvider(&config.Config{}, nil, worker, nil)
	if provider != worker {
		t.Fatalf("expected initialized Gateway Worker provider, got %#v", provider)
	}
	if label != modulechat.ForecastWorkerFallbackLabel {
		t.Fatalf("unexpected label: %q", label)
	}
}

func TestSelectForecastTopicProviderUsesWorkerAsShiro(t *testing.T) {
	worker := fakeConversationProvider{name: "worker-provider"}
	provider, label := selectForecastTopicProvider(worker)

	if provider != worker {
		t.Fatalf("expected Shiro Worker provider, got %#v", provider)
	}
	if label != modulechat.ForecastTopicGeneratorAgent {
		t.Fatalf("unexpected topic generator label: %q", label)
	}
}
