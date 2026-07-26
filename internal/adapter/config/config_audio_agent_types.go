package config

type ViewerLogConfig struct {
	Enabled           bool   `yaml:"enabled"`
	Path              string `yaml:"path"`
	RetentionDays     int    `yaml:"retention_days"`
	GCIntervalMinutes int    `yaml:"gc_interval_minutes"`
}

type VerificationConfig struct {
	Enabled      bool   `yaml:"enabled"`
	Mode         string `yaml:"mode"`          // dry_run|revise
	DefaultLevel string `yaml:"default_level"` // low|medium|high
	ReportPath   string `yaml:"report_path"`
}

// TTSConfig configures the single RenCrow_TTS Gateway route and playback.
type TTSConfig struct {
	Enabled            bool                        `yaml:"enabled"`
	OutputDir          string                      `yaml:"output_dir"`
	GatewayBaseURL     string                      `yaml:"gateway_base_url"`
	HTTPBaseURL        string                      `yaml:"http_base_url"` // Deprecated: use gateway_base_url.
	TLSSkipVerify      bool                        `yaml:"tls_skip_verify"`
	TimeoutMS          int                         `yaml:"timeout_ms"`
	VoiceID            string                      `yaml:"voice_id"`
	Speed              float64                     `yaml:"speed"`
	PlaybackCommands   []TTSCommandConfig          `yaml:"playback_commands"`
	PronunciationCheck TTSPronunciationCheckConfig `yaml:"pronunciation_check"`
}

// GatewayURL returns the canonical RenCrow_TTS Gateway URL. HTTPBaseURL is
// retained only so existing local config can be migrated without an outage.
func (c TTSConfig) GatewayURL() string {
	if c.GatewayBaseURL != "" {
		return c.GatewayBaseURL
	}
	return c.HTTPBaseURL
}

type TTSPronunciationCheckConfig struct {
	Enabled               bool   `yaml:"enabled"`
	ToolBaseURL           string `yaml:"tool_base_url"`
	Schedule              string `yaml:"schedule"`
	GPUMatch              string `yaml:"gpu_match"`
	MinFreeMB             int    `yaml:"min_free_mb"`
	MaxUtilizationPercent int    `yaml:"max_utilization_percent"`
	IdleSamples           int    `yaml:"idle_samples"`
	SampleIntervalSeconds int    `yaml:"sample_interval_seconds"`
	RetryIntervalSeconds  int    `yaml:"retry_interval_seconds"`
	TimeoutMinutes        int    `yaml:"timeout_minutes"`
}

type STTConfig struct {
	Enabled        bool              `yaml:"enabled"`
	Provider       string            `yaml:"provider"`
	Language       string            `yaml:"language"`
	Model          string            `yaml:"model"`
	TimeoutMS      int               `yaml:"timeout_ms"`
	BusyPolicy     string            `yaml:"busy_policy"`
	VAD            bool              `yaml:"vad"`
	EndpointPath   string            `yaml:"endpoint_path"`
	ProviderURL    string            `yaml:"provider_url"`
	StreamURL      string            `yaml:"stream_url"`
	ProviderParams map[string]any    `yaml:"provider_params"`
	Debug          STTDebugConfig    `yaml:"debug"`
	ExternalHTTP   STTExternalConfig `yaml:"external_http"`
}

type STTDebugConfig struct {
	SaveAudio      bool `yaml:"save_audio"`
	SaveTranscript bool `yaml:"save_transcript"`
}

type STTExternalConfig struct {
	URL       string `yaml:"url"`
	StreamURL string `yaml:"stream_url"`
}

type TTSCommandConfig struct {
	Name string   `yaml:"name"`
	Args []string `yaml:"args"`
}

// VTuberConfig configures VTube Studio emotion event delivery.
type VTuberConfig struct {
	Enabled        bool                             `yaml:"enabled"`
	TickIntervalMS int                              `yaml:"tick_interval_ms"`
	ConnectTimeout int                              `yaml:"connect_timeout_ms"`
	WriteTimeout   int                              `yaml:"write_timeout_ms"`
	Characters     map[string]VTuberCharacterConfig `yaml:"characters"`
}

type VTuberCharacterConfig struct {
	AudioOutput   string            `yaml:"audio_output"`
	VTSHost       string            `yaml:"vts_host"`
	VTSPort       int               `yaml:"vts_port"`
	ExpressionMap map[string]string `yaml:"expression_map"`
}

// AudioRouterConfig configures Coder4-side audio routing.
type AudioRouterConfig struct {
	Enabled           bool                               `yaml:"enabled"`
	SSEURL            string                             `yaml:"sse_url"`
	ConnectTimeoutMS  int                                `yaml:"connect_timeout_ms"`
	DownloadTimeoutMS int                                `yaml:"download_timeout_ms"`
	RetryDelayMS      int                                `yaml:"retry_delay_ms"`
	BufferMS          int                                `yaml:"buffer_ms"`
	DeviceMap         map[string]AudioRouterDeviceConfig `yaml:"device_map"`
}

type AudioRouterDeviceConfig struct {
	DeviceID    string `yaml:"device_id"`
	DisplayName string `yaml:"display_name"`
}

// GoogleSearchConfig はGoogle Search API設定
type GoogleSearchConfig struct {
	APIKey         string `yaml:"api_key"`          // 環境変数から読み込み推奨
	SearchEngineID string `yaml:"search_engine_id"` // カスタム検索エンジンID
}

// CoderConfig は Coder 個別設定（v4.1: 4体化 + Agent Persona）
type CoderConfig struct {
	Name        string            `yaml:"name"`         // 固定identity（coder1=aka, coder2=ao, coder3=kin, coder4=gin）
	DisplayName string            `yaml:"display_name"` // 表示名（赤, 青, 銀, 金 等）
	Provider    string            `yaml:"provider"`     // deepseek/openai/claude/gemini
	Model       string            `yaml:"model"`
	APIKey      string            `yaml:"api_key"`      // 環境変数参照（${...}）
	BaseURL     string            `yaml:"base_url"`     // オプション（DeepSeek 等）
	PersonaFile string            `yaml:"persona_file"` // ペルソナファイル（workspace_dir からの相対パス）
	Personality string            `yaml:"personality"`  // インラインペルソナ（persona_file がなければ使用）
	Tone        string            `yaml:"tone"`         // 口調（TTS 連携用）
	LightMemory LightMemoryConfig `yaml:"light_memory"`
	Enabled     bool              `yaml:"enabled"`
}

// LightMemoryConfig は短期記憶設定
type LightMemoryConfig struct {
	Enabled  bool `yaml:"enabled"`
	MaxTurns int  `yaml:"max_turns"` // 保持ターン数（推奨: 3〜5）
}
