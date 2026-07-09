package viewer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type DebugSystemSnapshot struct {
	UpdatedAt string             `json:"updated_at"`
	GPU       DebugGPUSnapshot   `json:"gpu"`
	Audio     DebugAudioSnapshot `json:"audio"`
}

type DebugGPUSnapshot struct {
	Available   bool              `json:"available"`
	TotalMB     int               `json:"total_mb,omitempty"`
	UsedMB      int               `json:"used_mb,omitempty"`
	FreeMB      int               `json:"free_mb,omitempty"`
	LLMUsedMB   int               `json:"llm_used_mb,omitempty"`
	STTUsedMB   int               `json:"stt_used_mb,omitempty"`
	TTSUsedMB   int               `json:"tts_used_mb,omitempty"`
	OtherUsedMB int               `json:"other_used_mb,omitempty"`
	Processes   []DebugGPUProcess `json:"processes,omitempty"`
	Note        string            `json:"note,omitempty"`
}

type DebugGPUProcess struct {
	PID         int    `json:"pid,omitempty"`
	Name        string `json:"name,omitempty"`
	Category    string `json:"category,omitempty"`
	UsedMB      int    `json:"used_mb,omitempty"`
	CommandHint string `json:"command_hint,omitempty"`
}

type DebugSystemOptions struct {
	STTBaseURL                 string
	STTStreamURL               string
	TTSBaseURL                 string
	TTSHealthPath              string
	LLMOpsConfigured           bool
	LLMOpsEnabled              bool
	LLMOpsBaseURL              string
	LocalLLM                   LocalLLMRuntimeConfig
	WebwrightFetch             WebwrightFetchRuntimeConfig
	WebGather                  WebGatherRuntimeConfig
	BrowserActor               BrowserActorRuntimeConfig
	SecretRefs                 []SecretRefRuntimeConfig
	RuntimeReadiness           RuntimeDependencyReadiness
	VoiceChatEnabled           bool
	VoiceChatGatewayConfigured bool
	VoiceChatStreamURL         string
	VoiceInputMode             string
}

type RuntimeConfig struct {
	STTStreamURL       string                      `json:"stt_stream_url,omitempty"`
	STTBaseURL         string                      `json:"stt_base_url,omitempty"`
	TTSBaseURL         string                      `json:"tts_base_url,omitempty"`
	TTSHealthPath      string                      `json:"tts_health_path,omitempty"`
	LLMOpsConfigured   bool                        `json:"llm_ops_configured"`
	LLMOpsEnabled      bool                        `json:"llm_ops_enabled"`
	LLMOpsBaseURL      string                      `json:"llm_ops_base_url,omitempty"`
	LocalLLM           LocalLLMRuntimeConfig       `json:"local_llm,omitempty"`
	WebwrightFetch     WebwrightFetchRuntimeConfig `json:"webwright_fetch,omitempty"`
	WebGather          WebGatherRuntimeConfig      `json:"web_gather,omitempty"`
	BrowserActor       BrowserActorRuntimeConfig   `json:"browser_actor,omitempty"`
	SecretRefs         []SecretRefRuntimeConfig    `json:"secret_refs,omitempty"`
	RuntimeReadiness   RuntimeDependencyReadiness  `json:"runtime_readiness,omitempty"`
	VoiceChatEnabled   bool                        `json:"voice_chat_enabled"`
	VoiceChatStreamURL string                      `json:"voice_chat_stream_url,omitempty"`
	VoiceInputMode     string                      `json:"voice_input_mode,omitempty"`
}

type SecretRefRuntimeConfig struct {
	Ref        string `json:"ref"`
	Label      string `json:"label,omitempty"`
	Scope      string `json:"scope,omitempty"`
	Configured bool   `json:"configured"`
}

type RuntimeDependencyReadiness struct {
	SlackCredentialsPresent      bool `json:"slack_credentials_present"`
	SlackWebhookRegistered       bool `json:"slack_webhook_registered"`
	SlackFilePayloadPipeline     bool `json:"slack_file_payload_pipeline"`
	DiscordCredentialsPresent    bool `json:"discord_credentials_present"`
	DiscordWebhookRegistered     bool `json:"discord_webhook_registered"`
	DiscordFilePayloadPipeline   bool `json:"discord_file_payload_pipeline"`
	TelegramCredentialsPresent   bool `json:"telegram_credentials_present"`
	TelegramWebhookRegistered    bool `json:"telegram_webhook_registered"`
	TelegramFilePayloadPipeline  bool `json:"telegram_file_payload_pipeline"`
	STTGatewayEnvPresent         bool `json:"stt_gateway_env_present"`
	STTGatewayConfigPresent      bool `json:"stt_gateway_config_present"`
	TTSProviderEnvPresent        bool `json:"tts_provider_env_present"`
	TTSProviderConfigPresent     bool `json:"tts_provider_config_present"`
	DistributedEnabled           bool `json:"distributed_enabled"`
	DistributedTransportsPresent bool `json:"distributed_transports_present"`
	DistributedSSHConfigured     bool `json:"distributed_ssh_configured"`
	DistributedSSHConnected      bool `json:"distributed_ssh_connected"`
	DistributedLocalTransport    bool `json:"distributed_local_transport"`
	ConversationEnabled          bool `json:"conversation_enabled"`
	L1SQLiteConfigPresent        bool `json:"l1_sqlite_config_present"`
	MemoryLayersAvailable        bool `json:"memory_layers_available"`
	MemoryLayersStatus           bool `json:"memory_layers_status_available"`
	SourceRegistryAvailable      bool `json:"source_registry_available"`
	SourceRegistryStatus         bool `json:"source_registry_status_available"`
	DomainGraphAvailable         bool `json:"domain_graph_available"`
	DomainGraphStatus            bool `json:"domain_graph_status_available"`
	KnowledgeMemoryEnabled       bool `json:"knowledge_memory_enabled"`
	KnowledgeMemoryStatus        bool `json:"knowledge_memory_status_available"`
	BrowserTraceAPIEnabled       bool `json:"browser_trace_api_enabled"`
	BrowserTraceAPIStatus        bool `json:"browser_trace_api_status_available"`
	BrowserTraceAPIFetcher       bool `json:"browser_trace_api_fetcher_available"`
	SandboxEnabled               bool `json:"sandbox_enabled"`
	SandboxStatusAvailable       bool `json:"sandbox_status_available"`
}

type LocalLLMRuntimeConfig struct {
	Enabled           bool                         `json:"enabled"`
	Provider          string                       `json:"provider,omitempty"`
	ChatBaseURL       string                       `json:"chat_base_url,omitempty"`
	WorkerBaseURL     string                       `json:"worker_base_url,omitempty"`
	ChatWorkerBaseURL string                       `json:"chat_worker_base_url,omitempty"`
	HeavyBaseURL      string                       `json:"heavy_base_url,omitempty"`
	WildBaseURL       string                       `json:"wild_base_url,omitempty"`
	ChatModel         string                       `json:"chat_model,omitempty"`
	WorkerModel       string                       `json:"worker_model,omitempty"`
	ChatWorkerModel   string                       `json:"chat_worker_model,omitempty"`
	HeavyModel        string                       `json:"heavy_model,omitempty"`
	WildModel         string                       `json:"wild_model,omitempty"`
	TimeoutSec        int                          `json:"timeout_sec,omitempty"`
	GlobalConcurrency int                          `json:"global_concurrency,omitempty"`
	ModelConcurrency  int                          `json:"model_concurrency,omitempty"`
	ModelContext      int                          `json:"model_context,omitempty"`
	LiveModels        map[string]LocalLLMLiveModel `json:"live_models,omitempty"`
}

type LocalLLMLiveModel struct {
	Role         string `json:"role,omitempty"`
	Alias        string `json:"alias,omitempty"`
	BaseURL      string `json:"base_url,omitempty"`
	BackendModel string `json:"backend_model,omitempty"`
	LoadedModel  string `json:"loaded_model,omitempty"`
	DefaultModel string `json:"default_model,omitempty"`
	Status       string `json:"status,omitempty"`
	Loaded       *bool  `json:"loaded,omitempty"`
	Error        string `json:"error,omitempty"`
}

type WebwrightFetchRuntimeConfig struct {
	Enabled           bool   `json:"enabled"`
	RunnerPath        string `json:"runner_path,omitempty"`
	ConfigPath        string `json:"config_path,omitempty"`
	OutputDir         string `json:"output_dir,omitempty"`
	StagingOutputDir  string `json:"staging_output_dir,omitempty"`
	UvxFrom           string `json:"uvx_from,omitempty"`
	Python            string `json:"python,omitempty"`
	ResponsesEndpoint string `json:"responses_endpoint,omitempty"`
	Model             string `json:"model,omitempty"`
	APIKeyConfigured  bool   `json:"api_key_configured"`
}

type WebGatherRuntimeConfig struct {
	SearXNGConfigured bool   `json:"searxng_configured"`
	SearXNGBaseURL    string `json:"searxng_base_url,omitempty"`
	YaCyConfigured    bool   `json:"yacy_configured"`
	YaCyBaseURL       string `json:"yacy_base_url,omitempty"`
	FetchCache        bool   `json:"fetch_cache"`
	FailureCache      bool   `json:"failure_cache"`
	RateState         bool   `json:"rate_state"`
}

type BrowserActorRuntimeConfig struct {
	Enabled            bool   `json:"enabled"`
	RunnerPath         string `json:"runner_path,omitempty"`
	NodeBinary         string `json:"node_binary,omitempty"`
	Browser            string `json:"browser,omitempty"`
	HeadlessDefault    bool   `json:"headless_default"`
	ProfileRoot        string `json:"profile_root,omitempty"`
	ArtifactRoot       string `json:"artifact_root,omitempty"`
	TimeoutMS          int    `json:"timeout_ms,omitempty"`
	MaxActions         int    `json:"max_actions,omitempty"`
	NetworkScope       string `json:"network_scope,omitempty"`
	AllowedOriginCount int    `json:"allowed_origin_count"`
	SaveTrace          bool   `json:"save_trace"`
	SaveScreenshot     bool   `json:"save_screenshot"`
	MaskSecrets        bool   `json:"mask_secrets"`
}

func HandleRuntimeConfig(opts DebugSystemOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(RuntimeConfig{
			STTStreamURL:       browserFacingSTTStreamURL(r, opts.STTStreamURL),
			STTBaseURL:         strings.TrimRight(strings.TrimSpace(opts.STTBaseURL), "/"),
			TTSBaseURL:         strings.TrimRight(strings.TrimSpace(opts.TTSBaseURL), "/"),
			TTSHealthPath:      strings.TrimSpace(opts.TTSHealthPath),
			LLMOpsConfigured:   opts.LLMOpsConfigured,
			LLMOpsEnabled:      opts.LLMOpsEnabled,
			LLMOpsBaseURL:      strings.TrimRight(strings.TrimSpace(opts.LLMOpsBaseURL), "/"),
			LocalLLM:           runtimeLocalLLMConfig(r.Context(), opts.LocalLLM),
			WebwrightFetch:     normalizeWebwrightFetchRuntimeConfig(opts.WebwrightFetch),
			WebGather:          normalizeWebGatherRuntimeConfig(opts.WebGather),
			BrowserActor:       normalizeBrowserActorRuntimeConfig(opts.BrowserActor),
			SecretRefs:         normalizeSecretRefs(opts.SecretRefs),
			RuntimeReadiness:   normalizeRuntimeDependencyReadiness(opts),
			VoiceChatEnabled:   opts.VoiceChatEnabled,
			VoiceChatStreamURL: browserFacingVoiceChatStreamURL(r),
			VoiceInputMode:     normalizeVoiceInputMode(opts.VoiceInputMode),
		})
	}
}

func browserFacingSTTStreamURL(r *http.Request, configured string) string {
	configured = strings.TrimSpace(configured)
	if r == nil {
		return configured
	}
	host := forwardedHost(r)
	if host == "" {
		return configured
	}
	if isHTTPSRequest(r) || isTailscaleHost(host) {
		return "wss://" + host + "/stt"
	}
	return "ws://" + host + "/stt"
}

func browserFacingVoiceChatStreamURL(r *http.Request) string {
	if r == nil {
		return ""
	}
	host := forwardedHost(r)
	if host == "" {
		return ""
	}
	if isHTTPSRequest(r) || isTailscaleHost(host) {
		return "wss://" + host + "/voice-chat"
	}
	return "ws://" + host + "/voice-chat"
}

func normalizeVoiceInputMode(raw string) string {
	switch strings.TrimSpace(raw) {
	case "vds_sub", "parallel_caption":
		return strings.TrimSpace(raw)
	default:
		return "stt_primary"
	}
}

func forwardedHost(r *http.Request) string {
	if r == nil {
		return ""
	}
	host := firstForwardedValue(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = strings.TrimSpace(r.Host)
	}
	return strings.TrimRight(host, ".")
}

func isHTTPSRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	if strings.EqualFold(firstForwardedValue(r.Header.Get("X-Forwarded-Proto")), "https") {
		return true
	}
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.URL.Scheme, "https")
}

func isTailscaleHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if i := strings.LastIndex(host, ":"); i >= 0 && !strings.Contains(host[i+1:], "]") {
		host = host[:i]
	}
	return strings.HasSuffix(strings.TrimRight(host, "."), ".ts.net")
}

func firstForwardedValue(value string) string {
	if i := strings.Index(value, ","); i >= 0 {
		value = value[:i]
	}
	return strings.TrimSpace(value)
}

func normalizeRuntimeDependencyReadiness(opts DebugSystemOptions) RuntimeDependencyReadiness {
	readiness := opts.RuntimeReadiness
	readiness.STTGatewayConfigPresent = strings.TrimSpace(opts.STTBaseURL) != "" || strings.TrimSpace(opts.STTStreamURL) != ""
	readiness.TTSProviderConfigPresent = strings.TrimSpace(opts.TTSBaseURL) != ""
	return readiness
}

func normalizeSecretRefs(in []SecretRefRuntimeConfig) []SecretRefRuntimeConfig {
	if len(in) == 0 {
		return nil
	}
	out := make([]SecretRefRuntimeConfig, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, item := range in {
		item.Ref = strings.TrimSpace(item.Ref)
		if item.Ref == "" {
			continue
		}
		if _, ok := seen[item.Ref]; ok {
			continue
		}
		seen[item.Ref] = struct{}{}
		item.Label = strings.TrimSpace(item.Label)
		item.Scope = strings.TrimSpace(item.Scope)
		out = append(out, item)
	}
	return out
}

func normalizeLocalLLMRuntimeConfig(in LocalLLMRuntimeConfig) LocalLLMRuntimeConfig {
	in.Provider = strings.TrimSpace(in.Provider)
	in.ChatBaseURL = strings.TrimRight(strings.TrimSpace(in.ChatBaseURL), "/")
	in.WorkerBaseURL = strings.TrimRight(strings.TrimSpace(in.WorkerBaseURL), "/")
	in.ChatWorkerBaseURL = strings.TrimRight(strings.TrimSpace(in.ChatWorkerBaseURL), "/")
	in.HeavyBaseURL = strings.TrimRight(strings.TrimSpace(in.HeavyBaseURL), "/")
	in.WildBaseURL = strings.TrimRight(strings.TrimSpace(in.WildBaseURL), "/")
	in.ChatModel = strings.TrimSpace(in.ChatModel)
	in.WorkerModel = strings.TrimSpace(in.WorkerModel)
	in.ChatWorkerModel = strings.TrimSpace(in.ChatWorkerModel)
	in.HeavyModel = strings.TrimSpace(in.HeavyModel)
	in.WildModel = strings.TrimSpace(in.WildModel)
	return in
}

func runtimeLocalLLMConfig(ctx context.Context, in LocalLLMRuntimeConfig) LocalLLMRuntimeConfig {
	in = normalizeLocalLLMRuntimeConfig(in)
	if !in.Enabled {
		return in
	}
	in.LiveModels = fetchLocalLLMLiveModels(ctx, in)
	return in
}

func fetchLocalLLMLiveModels(ctx context.Context, cfg LocalLLMRuntimeConfig) map[string]LocalLLMLiveModel {
	roles := []struct {
		key   string
		role  string
		alias string
		base  string
	}{
		{key: "chat", role: "Chat", alias: cfg.ChatModel, base: cfg.ChatBaseURL},
		{key: "worker", role: "Worker", alias: cfg.WorkerModel, base: cfg.WorkerBaseURL},
		{key: "chatworker", role: "ChatWorker", alias: cfg.ChatWorkerModel, base: firstNonEmpty(cfg.ChatWorkerBaseURL, cfg.WorkerBaseURL)},
	}
	out := make(map[string]LocalLLMLiveModel, len(roles))
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	for _, role := range roles {
		if strings.TrimSpace(role.alias) == "" && strings.TrimSpace(role.base) == "" {
			continue
		}
		out[role.key] = fetchLocalLLMLiveModel(ctx, client, role.role, role.alias, role.base)
	}
	return out
}

func fetchLocalLLMLiveModel(ctx context.Context, client *http.Client, role, alias, baseURL string) LocalLLMLiveModel {
	live := LocalLLMLiveModel{
		Role:    strings.TrimSpace(role),
		Alias:   strings.TrimSpace(alias),
		BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
	}
	if live.BaseURL == "" {
		live.Error = "local llm base url missing"
		return live
	}
	if err := fillLocalLLMModelList(ctx, client, &live); err != nil {
		live.Error = joinRuntimeErrors(live.Error, err.Error())
	}
	if err := fillLocalLLMModelStatus(ctx, client, &live); err != nil {
		if !strings.Contains(err.Error(), "HTTP 404") {
			live.Error = joinRuntimeErrors(live.Error, err.Error())
		}
	}
	if err := fillLocalLLMHealth(ctx, client, &live); err != nil {
		if localLLMHealthProbeOptional(err, live) {
			if live.Status == "" {
				live.Status = "models_available"
			}
			return live
		}
		live.Error = joinRuntimeErrors(live.Error, err.Error())
	}
	return live
}

func localLLMHealthProbeOptional(err error, live LocalLLMLiveModel) bool {
	if err == nil {
		return false
	}
	if !strings.Contains(err.Error(), "HTTP 404") {
		return false
	}
	return strings.TrimSpace(live.BackendModel) != "" ||
		strings.TrimSpace(live.LoadedModel) != "" ||
		(live.Loaded != nil && *live.Loaded)
}

func fillLocalLLMModelList(ctx context.Context, client *http.Client, live *LocalLLMLiveModel) error {
	var body struct {
		Data []struct {
			ID           string `json:"id"`
			BackendModel string `json:"backend_model"`
		} `json:"data"`
	}
	if err := getLocalLLMJSON(ctx, client, live.BaseURL+"/v1/models", &body); err != nil {
		return fmt.Errorf("v1/models: %w", err)
	}
	for _, item := range body.Data {
		if strings.EqualFold(strings.TrimSpace(item.ID), live.Alias) {
			live.BackendModel = strings.TrimSpace(item.BackendModel)
			return nil
		}
	}
	return fmt.Errorf("alias %q not found", live.Alias)
}

func fillLocalLLMModelStatus(ctx context.Context, client *http.Client, live *LocalLLMLiveModel) error {
	var body struct {
		Models []struct {
			ID        string `json:"id"`
			ModelPath string `json:"model_path"`
			Loaded    bool   `json:"loaded"`
		} `json:"models"`
	}
	if err := getLocalLLMJSON(ctx, client, live.BaseURL+"/v1/models/status", &body); err != nil {
		return fmt.Errorf("v1/models/status: %w", err)
	}
	target := firstNonEmptyString(live.BackendModel, live.Alias)
	for _, item := range body.Models {
		if localLLMModelMatches(target, item.ID, item.ModelPath) {
			loaded := item.Loaded
			live.Loaded = &loaded
			if live.LoadedModel == "" && item.ModelPath != "" {
				live.LoadedModel = strings.TrimSpace(item.ModelPath)
			}
			return nil
		}
	}
	return fmt.Errorf("model %q not found in status", target)
}

func fillLocalLLMHealth(ctx context.Context, client *http.Client, live *LocalLLMLiveModel) error {
	var body struct {
		Status       string `json:"status"`
		LoadedModel  string `json:"loaded_model"`
		DefaultModel string `json:"default_model"`
	}
	if err := getLocalLLMJSON(ctx, client, live.BaseURL+"/health", &body); err != nil {
		return fmt.Errorf("health: %w", err)
	}
	live.Status = strings.TrimSpace(body.Status)
	if live.LoadedModel == "" {
		live.LoadedModel = strings.TrimSpace(body.LoadedModel)
	}
	live.DefaultModel = strings.TrimSpace(body.DefaultModel)
	return nil
}

func getLocalLLMJSON(ctx context.Context, client *http.Client, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
		return err
	}
	return nil
}

func localLLMModelMatches(target, id, path string) bool {
	target = strings.TrimSpace(target)
	id = strings.TrimSpace(id)
	path = strings.TrimSpace(path)
	if target == "" {
		return false
	}
	return target == id || target == path || strings.HasSuffix(strings.TrimRight(path, "/"), "/"+target)
}

func joinRuntimeErrors(current, next string) string {
	next = strings.TrimSpace(next)
	if next == "" {
		return current
	}
	if current == "" {
		return next
	}
	return current + "; " + next
}

func normalizeWebwrightFetchRuntimeConfig(in WebwrightFetchRuntimeConfig) WebwrightFetchRuntimeConfig {
	in.RunnerPath = strings.TrimSpace(in.RunnerPath)
	in.ConfigPath = strings.TrimSpace(in.ConfigPath)
	in.OutputDir = strings.TrimSpace(in.OutputDir)
	in.StagingOutputDir = strings.TrimSpace(in.StagingOutputDir)
	in.UvxFrom = strings.TrimSpace(in.UvxFrom)
	in.Python = strings.TrimSpace(in.Python)
	in.ResponsesEndpoint = strings.TrimRight(strings.TrimSpace(in.ResponsesEndpoint), "/")
	in.Model = strings.TrimSpace(in.Model)
	return in
}

func normalizeWebGatherRuntimeConfig(in WebGatherRuntimeConfig) WebGatherRuntimeConfig {
	in.SearXNGBaseURL = strings.TrimRight(strings.TrimSpace(in.SearXNGBaseURL), "/")
	in.YaCyBaseURL = strings.TrimRight(strings.TrimSpace(in.YaCyBaseURL), "/")
	in.SearXNGConfigured = in.SearXNGConfigured || in.SearXNGBaseURL != ""
	in.YaCyConfigured = in.YaCyConfigured || in.YaCyBaseURL != ""
	return in
}

func normalizeBrowserActorRuntimeConfig(in BrowserActorRuntimeConfig) BrowserActorRuntimeConfig {
	in.RunnerPath = strings.TrimSpace(in.RunnerPath)
	in.NodeBinary = strings.TrimSpace(in.NodeBinary)
	in.Browser = strings.TrimSpace(in.Browser)
	in.ProfileRoot = strings.TrimSpace(in.ProfileRoot)
	in.ArtifactRoot = strings.TrimSpace(in.ArtifactRoot)
	in.NetworkScope = strings.TrimSpace(in.NetworkScope)
	return in
}

type DebugAudioSnapshot struct {
	STTBaseURL string `json:"stt_base_url,omitempty"`
	TTSBaseURL string `json:"tts_base_url,omitempty"`
	STTOK      bool   `json:"stt_ok"`
	TTSLiveOK  bool   `json:"tts_live_ok"`
	TTSReadyOK bool   `json:"tts_ready_ok"`
	STTHealth  string `json:"stt_health,omitempty"`
	TTSLive    string `json:"tts_live,omitempty"`
	TTSReady   string `json:"tts_ready,omitempty"`
	LastError  string `json:"last_error,omitempty"`
}

func HandleDebugSystemSnapshot(opts DebugSystemOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s := DebugSystemSnapshot{
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			GPU:       collectGPUSnapshot(),
			Audio:     collectAudioSnapshot(opts),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s)
	}
}

func collectAudioSnapshot(opts DebugSystemOptions) DebugAudioSnapshot {
	out := DebugAudioSnapshot{
		STTBaseURL: strings.TrimRight(strings.TrimSpace(opts.STTBaseURL), "/"),
		TTSBaseURL: strings.TrimRight(strings.TrimSpace(opts.TTSBaseURL), "/"),
	}
	client := &http.Client{Timeout: 1200 * time.Millisecond}

	type probeResult struct {
		name string
		body string
		ok   bool
		err  error
	}
	probes := map[string]string{}
	if out.STTBaseURL != "" {
		probes["stt"] = out.STTBaseURL + "/health"
	}
	if out.TTSBaseURL != "" {
		if strings.TrimSpace(opts.TTSHealthPath) != "" {
			probes["tts"] = out.TTSBaseURL + "/" + strings.TrimLeft(strings.TrimSpace(opts.TTSHealthPath), "/")
		} else {
			probes["tts_live"] = out.TTSBaseURL + "/health/live"
			probes["tts_ready"] = out.TTSBaseURL + "/health/ready"
		}
	}
	results := make(chan probeResult, len(probes))
	for name, endpoint := range probes {
		go func(name, endpoint string) {
			body, ok, err := fetchEndpoint(client, endpoint)
			results <- probeResult{name: name, body: body, ok: ok, err: err}
		}(name, endpoint)
	}
	for range probes {
		result := <-results
		if result.err != nil {
			out.LastError = appendError(out.LastError, result.name+":"+result.err.Error())
			continue
		}
		switch result.name {
		case "stt":
			out.STTHealth = result.body
			out.STTOK = result.ok && isSTTHealthReady(result.body)
		case "tts":
			out.TTSLive = result.body
			out.TTSReady = result.body
			out.TTSLiveOK = result.ok
			out.TTSReadyOK = result.ok
		case "tts_live":
			out.TTSLive = result.body
			out.TTSLiveOK = result.ok
		case "tts_ready":
			out.TTSReady = result.body
			out.TTSReadyOK = result.ok
		}
	}
	return out
}

func fetchEndpoint(client *http.Client, endpoint string) (string, bool, error) {
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return "", false, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	body := strings.TrimSpace(string(bodyBytes))
	return body, resp.StatusCode >= 200 && resp.StatusCode < 300, nil
}

func appendError(cur, next string) string {
	next = strings.TrimSpace(next)
	if next == "" {
		return cur
	}
	if strings.TrimSpace(cur) == "" {
		return next
	}
	return cur + "; " + next
}

func collectGPUSnapshot() DebugGPUSnapshot {
	base, err := queryGPUMemoryTotals()
	if err != nil {
		return DebugGPUSnapshot{
			Available: false,
			Note:      err.Error(),
		}
	}

	procs, err := queryGPUProcesses()
	if err != nil {
		base.Available = true
		base.Note = err.Error()
		return base
	}
	base.Available = true
	base.Processes = procs
	for _, p := range procs {
		switch p.Category {
		case "llm":
			base.LLMUsedMB += p.UsedMB
		case "stt":
			base.STTUsedMB += p.UsedMB
		case "tts":
			base.TTSUsedMB += p.UsedMB
		default:
			base.OtherUsedMB += p.UsedMB
		}
	}
	return base
}

func queryGPUMemoryTotals() (DebugGPUSnapshot, error) {
	out, err := runCmd(2*time.Second, "nvidia-smi",
		"--query-gpu=memory.total,memory.used,memory.free",
		"--format=csv,noheader,nounits")
	if err != nil {
		return DebugGPUSnapshot{}, fmt.Errorf("nvidia-smi unavailable: %w", err)
	}
	line := firstNonEmptyLine(out)
	if line == "" {
		return DebugGPUSnapshot{}, fmt.Errorf("nvidia-smi returned empty output")
	}
	parts := splitCSVLine(line)
	if len(parts) < 3 {
		return DebugGPUSnapshot{}, fmt.Errorf("nvidia-smi parse error: %q", line)
	}
	total, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
	used, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
	free, _ := strconv.Atoi(strings.TrimSpace(parts[2]))
	return DebugGPUSnapshot{
		TotalMB: total,
		UsedMB:  used,
		FreeMB:  free,
	}, nil
}

func queryGPUProcesses() ([]DebugGPUProcess, error) {
	out, err := runCmd(2*time.Second, "nvidia-smi",
		"--query-compute-apps=pid,process_name,used_gpu_memory",
		"--format=csv,noheader,nounits")
	if err != nil {
		return nil, fmt.Errorf("nvidia-smi process query failed: %w", err)
	}
	lines := strings.Split(out, "\n")
	items := make([]DebugGPUProcess, 0, len(lines))
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		parts := splitCSVLine(line)
		if len(parts) < 3 {
			continue
		}
		pid, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
		name := strings.TrimSpace(parts[1])
		usedMB, _ := strconv.Atoi(strings.TrimSpace(parts[2]))
		hint := processCommandHint(pid)
		items = append(items, DebugGPUProcess{
			PID:         pid,
			Name:        name,
			UsedMB:      usedMB,
			Category:    classifyGPUProcess(name, hint),
			CommandHint: hint,
		})
	}
	return items, nil
}

func processCommandHint(pid int) string {
	if pid <= 0 {
		return ""
	}
	path := fmt.Sprintf("/proc/%d/cmdline", pid)
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return ""
	}
	raw := strings.ReplaceAll(string(b), "\x00", " ")
	return strings.TrimSpace(raw)
}

func classifyGPUProcess(name, hint string) string {
	text := strings.ToLower(strings.TrimSpace(name + " " + hint))
	switch {
	case strings.Contains(text, "ollama"), strings.Contains(text, "llama"), strings.Contains(text, "deepseek"), strings.Contains(text, "openai"), strings.Contains(text, "claude"):
		return "llm"
	case strings.Contains(text, "whisper"), strings.Contains(text, "stt"), strings.Contains(text, "speech-to-text"):
		return "stt"
	case strings.Contains(text, "tts"), strings.Contains(text, "vits"), strings.Contains(text, "sbv2"), strings.Contains(text, "style-bert"), strings.Contains(text, "voicevox"):
		return "tts"
	default:
		return "other"
	}
}

func runCmd(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func firstNonEmptyLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			return t
		}
	}
	return ""
}

func splitCSVLine(line string) []string {
	parts := strings.Split(line, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}
