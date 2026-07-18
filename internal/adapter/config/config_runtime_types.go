package config

// ServerConfig はサーバー設定
type ServerConfig struct {
	Port int       `yaml:"port"`
	Host string    `yaml:"host"`
	TLS  TLSConfig `yaml:"tls"`
}

type TLSConfig struct {
	Enabled  bool   `yaml:"enabled"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

// OllamaConfig はOllama設定
// v4.0で旧 chat_model/worker_model を統合し、単一の Model に変更
// 全Agent（mio/shiro/IdleChat参加Agent）が同一モデルを共用する
type OllamaConfig struct {
	BaseURL    string `yaml:"base_url"`
	Model      string `yaml:"model"`       // v4: 共通モデル（例: "rencrow-v1"）
	MaxContext int    `yaml:"max_context"` // 常駐モデルの最大コンテキスト長（超過はNG）
}

// ClaudeConfig はClaude API設定
type ClaudeConfig struct {
	APIKey string `yaml:"api_key"` // 環境変数から読み込み推奨
	Model  string `yaml:"model"`
}

// DeepSeekConfig はDeepSeek API設定
type DeepSeekConfig struct {
	APIKey string `yaml:"api_key"` // 環境変数から読み込み推奨
	Model  string `yaml:"model"`
}

// OpenAIConfig はOpenAI API設定
type OpenAIConfig struct {
	APIKey string `yaml:"api_key"` // 環境変数から読み込み推奨
	Model  string `yaml:"model"`
}

// LocalLLMConfig is the primary local inference runtime for Chat / Worker / Heavy / Wild.
// It is intended for OpenAI-compatible local servers such as MLX.
type LocalLLMConfig struct {
	Enabled           bool   `yaml:"enabled"`
	Provider          string `yaml:"provider"` // local_openai (default) or ollama
	BaseURL           string `yaml:"base_url"`
	ChatBaseURL       string `yaml:"chat_base_url"`
	WorkerBaseURL     string `yaml:"worker_base_url"`
	ChatWorkerBaseURL string `yaml:"chat_worker_base_url"`
	HeavyBaseURL      string `yaml:"heavy_base_url"`
	WildBaseURL       string `yaml:"wild_base_url"`
	APIKey            string `yaml:"api_key"`
	ChatModel         string `yaml:"chat_model"`
	WorkerModel       string `yaml:"worker_model"`
	ChatWorkerModel   string `yaml:"chat_worker_model"`
	HeavyModel        string `yaml:"heavy_model"`
	WildModel         string `yaml:"wild_model"`
	TimeoutSec        int    `yaml:"timeout_sec"`
	ChatTimeoutSec    int    `yaml:"chat_timeout_sec"`
	Warmup            *bool  `yaml:"warmup"`
	GlobalConcurrency int    `yaml:"global_concurrency"`
	ModelConcurrency  int    `yaml:"model_concurrency"`
	ModelContext      int    `yaml:"model_context"`
	ChatModelContext  int    `yaml:"chat_model_context"`
}

// MioConfig controls Mio-specific behavior without changing the shared Chat provider.
type MioConfig struct {
	Generation MioGenerationConfig `yaml:"generation"`
	InputAudio MioInputAudioConfig `yaml:"input_audio"`
}

// MioGenerationConfig is the request policy used for Mio's conversational replies.
type MioGenerationConfig struct {
	Stream             bool                  `yaml:"stream"`
	MaxTokens          int                   `yaml:"max_tokens"`
	Temperature        float64               `yaml:"temperature"`
	TopP               *float64              `yaml:"top_p"`
	TopK               *int                  `yaml:"top_k"`
	MinP               *float64              `yaml:"min_p"`
	Seed               *int64                `yaml:"seed"`
	ChatTemplateKwargs MioChatTemplateKwargs `yaml:"chat_template_kwargs"`
}

// MioInputAudioConfig controls the text instruction paired with direct WAV input.
type MioInputAudioConfig struct {
	Prompt string `yaml:"prompt"`
}

// MioChatTemplateKwargs contains llama.cpp chat-template switches for Mio.
type MioChatTemplateKwargs struct {
	EnableThinking *bool `yaml:"enable_thinking"`
}

// LLMOpsConfig は MLX 運用デーモン（8079 番管理 API）への Viewer 経由プロキシ用。
// Bearer は LLM_OPS_TOKEN 環境変数から読む。
type LLMOpsConfig struct {
	Enabled bool   `yaml:"enabled"`
	BaseURL string `yaml:"base_url"` // 例: http://192.168.1.31:8079
}

// WebwrightFetchConfig は RenCrow 本体から分離された Webwright 取得 bridge 設定。
// 実行は RenCrow_Tools/tools/webwright_fetch/run_webwright_fetch.py が担当し、本体 runtime dependency にはしない。
type WebwrightFetchConfig struct {
	Enabled           bool   `yaml:"enabled"`
	RunnerPath        string `yaml:"runner_path"`
	ConfigPath        string `yaml:"config_path"`
	OutputDir         string `yaml:"output_dir"`
	StagingOutputDir  string `yaml:"staging_output_dir"`
	UvxFrom           string `yaml:"uvx_from"`
	Python            string `yaml:"python"`
	ResponsesEndpoint string `yaml:"responses_endpoint"`
	Model             string `yaml:"model"`
	APIKey            string `yaml:"api_key"`
}

// WebGatherConfig は公開 Web 情報収集ツールの任意 provider 設定。
// SearXNG は self-hosted endpoint を明示した場合だけ有効化する。
type WebGatherConfig struct {
	SearXNGBaseURL string `yaml:"searxng_base_url"`
	YaCyBaseURL    string `yaml:"yacy_base_url"`
}

// BrowserActorConfig は headless browser 操作 sidecar 設定。
// 実行は RenCrow_Tools/tools/browser_actor/run_browser_actor.mjs が担当し、本体 runtime dependency にはしない。
type BrowserActorConfig struct {
	Enabled         bool     `yaml:"enabled"`
	RunnerPath      string   `yaml:"runner_path"`
	NodeBinary      string   `yaml:"node_binary"`
	Browser         string   `yaml:"browser"`
	HeadlessDefault *bool    `yaml:"headless_default"`
	ProfileRoot     string   `yaml:"profile_root"`
	ArtifactRoot    string   `yaml:"artifact_root"`
	TimeoutMS       int      `yaml:"timeout_ms"`
	MaxActions      int      `yaml:"max_actions"`
	NetworkScope    string   `yaml:"network_scope"`
	AllowedOrigins  []string `yaml:"allowed_origins"`
	SaveTrace       *bool    `yaml:"save_trace"`
	SaveScreenshot  *bool    `yaml:"save_screenshot"`
	MaskSecrets     *bool    `yaml:"mask_secrets"`
}

func (c BrowserActorConfig) HeadlessDefaultEnabled() bool {
	return boolValueOrDefault(c.HeadlessDefault, true)
}

func (c BrowserActorConfig) SaveTraceEnabled() bool {
	return boolValueOrDefault(c.SaveTrace, true)
}

func (c BrowserActorConfig) SaveScreenshotEnabled() bool {
	return boolValueOrDefault(c.SaveScreenshot, true)
}

func (c BrowserActorConfig) MaskSecretsEnabled() bool {
	return boolValueOrDefault(c.MaskSecrets, true)
}

// CodexConfig は Codex CLI の非対話実行を RenCrow ToolRunner から呼ぶ設定。
// 既定は disabled/read-only で、workspace-write は明示指定時だけ許可する。
type CodexConfig struct {
	Enabled        bool   `yaml:"enabled"`
	Command        string `yaml:"command"`
	WorkingDir     string `yaml:"working_dir"`
	Sandbox        string `yaml:"sandbox"`
	Model          string `yaml:"model"`
	TimeoutMS      int    `yaml:"timeout_ms"`
	MaxPromptBytes int    `yaml:"max_prompt_bytes"`
	MaxOutputBytes int    `yaml:"max_output_bytes"`
	Ephemeral      *bool  `yaml:"ephemeral"`
}

type AdvisorConfig struct {
	Storage    string `yaml:"storage"`
	LogPath    string `yaml:"log_path"`
	SQLitePath string `yaml:"sqlite_path"`
}

func (c CodexConfig) EphemeralEnabled() bool {
	return boolValueOrDefault(c.Ephemeral, true)
}

func boolValueOrDefault(value *bool, def bool) bool {
	if value == nil {
		return def
	}
	return *value
}

// ComfyUIConfig is the Wild-owned image generation backend.
type ComfyUIConfig struct {
	BaseURL         string `yaml:"base_url"`
	ClientID        string `yaml:"client_id"`
	PollIntervalSec int    `yaml:"poll_interval_sec"`
	TimeoutSec      int    `yaml:"timeout_sec"`
}
