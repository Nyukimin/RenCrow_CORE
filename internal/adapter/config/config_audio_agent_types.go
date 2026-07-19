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

// TTSConfig configures provider fallback and playback verification.
type TTSConfig struct {
	Enabled            bool                        `yaml:"enabled"`
	OutputDir          string                      `yaml:"output_dir"`
	AudioPathRoot      string                      `yaml:"audio_path_root"`
	HTTPBaseURL        string                      `yaml:"http_base_url"`
	TLSSkipVerify      bool                        `yaml:"tls_skip_verify"`
	TimeoutMS          int                         `yaml:"timeout_ms"`
	VoiceID            string                      `yaml:"voice_id"`
	Speed              float64                     `yaml:"speed"`
	ProviderParams     map[string]any              `yaml:"provider_params"`
	ProviderPriority   []string                    `yaml:"provider_priority"` // e.g. irodori
	PlaybackCommands   []TTSCommandConfig          `yaml:"playback_commands"`
	SBV2               TTSSBV2Config               `yaml:"sbv2"`
	Irodori            TTSIrodoriConfig            `yaml:"irodori"`
	PronunciationCheck TTSPronunciationCheckConfig `yaml:"pronunciation_check"`
	Azure              TTSAzureConfig              `yaml:"azure"`
	Eleven             TTSElevenLabsConfig         `yaml:"eleven"`
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

type TTSSBV2Config struct {
	Enabled    bool   `yaml:"enabled"`
	BaseURL    string `yaml:"base_url"`
	VoiceID    string `yaml:"voice_id"`
	TimeoutSec int    `yaml:"timeout_sec"`
}

type TTSIrodoriConfig struct {
	Enabled               bool    `yaml:"enabled"`
	BaseURL               string  `yaml:"base_url"`
	EndpointPath          string  `yaml:"endpoint_path"`
	VoiceID               string  `yaml:"voice_id"`
	VoiceName             string  `yaml:"voice_name"`
	ReferenceAudio        string  `yaml:"reference_audio"`
	ReferenceAudioURL     string  `yaml:"reference_audio_url"`
	TimeoutSec            int     `yaml:"timeout_sec"`
	Checkpoint            string  `yaml:"checkpoint"`
	ModelDevice           string  `yaml:"model_device"`
	ModelPrecision        string  `yaml:"model_precision"`
	CodecDevice           string  `yaml:"codec_device"`
	CodecPrecision        string  `yaml:"codec_precision"`
	EnableWatermark       bool    `yaml:"enable_watermark"`
	NumSteps              int     `yaml:"num_steps"`
	NumCandidates         int     `yaml:"num_candidates"`
	SeedRaw               string  `yaml:"seed_raw"`
	CFGGuidanceMode       string  `yaml:"cfg_guidance_mode"`
	CFGScaleText          float64 `yaml:"cfg_scale_text"`
	CFGScaleSpeaker       float64 `yaml:"cfg_scale_speaker"`
	CFGScaleRaw           string  `yaml:"cfg_scale_raw"`
	CFGMinT               float64 `yaml:"cfg_min_t"`
	CFGMaxT               float64 `yaml:"cfg_max_t"`
	ContextKVCache        bool    `yaml:"context_kv_cache"`
	TruncationFactorRaw   string  `yaml:"truncation_factor_raw"`
	RescaleKRaw           string  `yaml:"rescale_k_raw"`
	RescaleSigmaRaw       string  `yaml:"rescale_sigma_raw"`
	SpeakerKVScaleRaw     string  `yaml:"speaker_kv_scale_raw"`
	SpeakerKVMinTRaw      string  `yaml:"speaker_kv_min_t_raw"`
	SpeakerKVMaxLayersRaw string  `yaml:"speaker_kv_max_layers_raw"`
}

type TTSAzureConfig struct {
	Enabled    bool   `yaml:"enabled"`
	Endpoint   string `yaml:"endpoint"`
	APIKey     string `yaml:"api_key"`
	VoiceName  string `yaml:"voice_name"`
	TimeoutSec int    `yaml:"timeout_sec"`
}

type TTSElevenLabsConfig struct {
	Enabled    bool   `yaml:"enabled"`
	APIKey     string `yaml:"api_key"`
	VoiceID    string `yaml:"voice_id"`
	ModelID    string `yaml:"model_id"`
	TimeoutSec int    `yaml:"timeout_sec"`
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
	Name        string            `yaml:"name"`         // 任意の名前（aka, ao, gin, kin 等）
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
