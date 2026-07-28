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

// LLMGatewayConfig connects CORE to RenCrow_LLM using logical Agent IDs only.
type LLMGatewayConfig struct {
	Enabled    bool   `yaml:"enabled"`
	BaseURL    string `yaml:"base_url"`
	APIKeyEnv  string `yaml:"api_key_env"`
	TimeoutSec int    `yaml:"timeout_sec"`
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

// MioInputAudioConfig controls the text instruction paired with WAV input.
type MioInputAudioConfig struct {
	Prompt string `yaml:"prompt"`
}

// MioChatTemplateKwargs contains llama.cpp chat-template switches for Mio.
type MioChatTemplateKwargs struct {
	EnableThinking *bool `yaml:"enable_thinking"`
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
	SQLitePath string `yaml:"-"`
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

// GamesConfig は RenCrow_GAMES 連携の設定（マルチペルソナ WP6）。
type GamesConfig struct {
	AutoPlay GamesAutoPlayConfig `yaml:"auto_play"`
}

// GamesAutoPlayConfig はペルソナの自発プレイ（autoplay ランナー）の設定。
// ペースは固定間隔ではなく LLM 自身が next_check_minutes で決める
// （RenCrow_GAMES/docs/10_RenCrow自発プレイ仕様.md）。
type GamesAutoPlayConfig struct {
	// Enabled が false（既定）の間、ランナーは起動しない。
	Enabled bool `yaml:"enabled"`
	// Personas は自発プレイに常時参加できるペルソナ。空なら既定
	// (mio, shiro, midori)。
	Personas []string `yaml:"personas"`
	// MaxSessionsPerDay は 1 日の自発起動上限。0 以下は既定 8。
	MaxSessionsPerDay int `yaml:"max_sessions_per_day"`
}
