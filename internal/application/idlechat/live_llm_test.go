package idlechat

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	appconfig "github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/llm/providers/rencrowllm"
	"gopkg.in/yaml.v3"
)

func TestLiveIdleChatTenThemesDoNotUseFallback(t *testing.T) {
	if os.Getenv("RENCROW_IDLECHAT_LIVE") != "1" {
		t.Skip("set RENCROW_IDLECHAT_LIVE=1 to run against the RenCrow_LLM Gateway")
	}

	cfgPath := os.Getenv("RENCROW_CONFIG")
	if cfgPath == "" {
		cfgPath = filepath.Join(os.Getenv("HOME"), ".rencrow", "config.yaml")
	}
	cfg := loadLiveIdleChatConfig(t, cfgPath)

	chatProvider, workerProvider := liveIdleChatProviders(t, cfg)
	probeLiveIdleChatProvider(t, "mio", chatProvider)
	probeLiveIdleChatProvider(t, "shiro", workerProvider)
	o := NewIdleChatOrchestrator(chatProvider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, cfg.IdleChat.Temperature, appconfig.BuildIdleChatAgentPrompts(cfg.Prompts), "")
	o.SetSpeakerProviders(map[string]llm.LLMProvider{
		"mio":   chatProvider,
		"shiro": workerProvider,
	})

	topics := []string{
		"郵便と古書店",
		"雨の文化祭前夜",
		"港町の倉庫街",
		"壊れたオルゴール",
		"深夜の自動販売機",
		"古い団地の掲示板",
		"地下鉄の忘れ物",
		"夏祭りの裏通り",
		"閉館後の図書室",
		"朝の市場と手紙",
	}
	if limitText := strings.TrimSpace(os.Getenv("RENCROW_IDLECHAT_LIVE_LIMIT")); limitText != "" {
		limit, err := strconv.Atoi(limitText)
		if err != nil || limit < 1 || limit > len(topics) {
			t.Fatalf("RENCROW_IDLECHAT_LIVE_LIMIT must be 1-%d, got %q", len(topics), limitText)
		}
		topics = topics[:limit]
	}

	oldTimeout := idleChatLLMGenerateTimeout
	if oldTimeout < 90*time.Second {
		idleChatLLMGenerateTimeout = 90 * time.Second
	}
	defer func() { idleChatLLMGenerateTimeout = oldTimeout }()

	for i, topic := range topics {
		speaker := "mio"
		target := "shiro"
		if i%2 == 1 {
			speaker = "shiro"
			target = "mio"
		}
		got, err := o.generateResponse(speaker, target, "idle-live-ten-themes", i, i, topic)
		if err != nil {
			t.Fatalf("theme %02d %q speaker=%s error: %v", i+1, topic, speaker, err)
		}
		if unusableIdleResponse(got, got) {
			t.Fatalf("theme %02d %q speaker=%s returned unusable response: %q", i+1, topic, speaker, got)
		}
		t.Logf("theme %02d %s/%s: %s", i+1, topic, speaker, got)
	}
}

func loadLiveIdleChatConfig(t *testing.T, cfgPath string) *appconfig.Config {
	t.Helper()
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config %s: %v", cfgPath, err)
	}
	var cfg appconfig.Config
	if err := yaml.Unmarshal([]byte(os.ExpandEnv(string(data))), &cfg); err != nil {
		t.Fatalf("parse config %s: %v", cfgPath, err)
	}
	if cfg.LLMGateway.BaseURL == "" {
		cfg.LLMGateway.BaseURL = "http://127.0.0.1:8090"
	}
	if cfg.LLMGateway.TimeoutSec <= 0 {
		cfg.LLMGateway.TimeoutSec = 600
	}
	if len(cfg.IdleChat.Participants) == 0 {
		cfg.IdleChat.Participants = []string{"mio", "shiro"}
	}
	if cfg.IdleChat.Temperature == 0 {
		cfg.IdleChat.Temperature = 0.8
	}
	if cfg.PromptsDir == "" {
		cfg.PromptsDir = "./prompts"
	}
	if cfg.WorkspaceDir == "" {
		cfg.WorkspaceDir = "./workspace"
	}
	cfg.Prompts = appconfig.LoadPrompts(cfg.PromptsDir, cfg.WorkspaceDir)
	return &cfg
}

func probeLiveIdleChatProvider(t *testing.T, name string, provider llm.LLMProvider) {
	t.Helper()
	_, err := provider.Generate(t.Context(), llm.GenerateRequest{
		Messages: []llm.Message{
			{Role: "system", Content: "疎通確認です。"},
			{Role: "user", Content: "自然な日本語で「確認しました。」だけ返してください。"},
		},
		MaxTokens:   16,
		Temperature: 0.1,
	})
	if err != nil {
		t.Fatalf("live IdleChat provider %s is unreachable: %v", name, err)
	}
}

func liveIdleChatProviders(t *testing.T, cfg *appconfig.Config) (llm.LLMProvider, llm.LLMProvider) {
	t.Helper()
	timeout := time.Duration(cfg.LLMGateway.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.LLMGateway.BaseURL), "/")
	if baseURL == "" {
		t.Fatal("llm_gateway.base_url is required for live IdleChat test")
	}
	apiKey := ""
	if envName := strings.TrimSpace(cfg.LLMGateway.APIKeyEnv); envName != "" {
		apiKey = strings.TrimSpace(os.Getenv(envName))
	}
	return rencrowllm.NewGatewayProviderWithOptions(apiKey, "mio", baseURL, timeout),
		rencrowllm.NewGatewayProviderWithOptions(apiKey, "shiro", baseURL, timeout)
}
