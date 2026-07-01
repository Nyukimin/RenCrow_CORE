package viewer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleRuntimeConfig_ReturnsSameOriginSTTStreamURL(t *testing.T) {
	handler := HandleRuntimeConfig(DebugSystemOptions{
		STTBaseURL:    "https://192.168.1.31:8443/",
		STTStreamURL:  "wss://192.168.1.31:8443/stt/stream",
		TTSBaseURL:    "http://127.0.0.1:7870/",
		TTSHealthPath: "/gradio_api/info",
	})
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18790/viewer/runtime-config", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	var body RuntimeConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode runtime config: %v", err)
	}
	if body.STTStreamURL != "ws://127.0.0.1:18790/stt" {
		t.Fatalf("unexpected stt stream url: %+v", body)
	}
	if body.STTBaseURL != "https://192.168.1.31:8443" {
		t.Fatalf("unexpected stt base url: %+v", body)
	}
	if body.TTSBaseURL != "http://127.0.0.1:7870" || body.TTSHealthPath != "/gradio_api/info" {
		t.Fatalf("unexpected tts runtime config: %+v", body)
	}
}

func TestHandleRuntimeConfig_ReturnsSameOriginSTTStreamURLForLANHTTP(t *testing.T) {
	handler := HandleRuntimeConfig(DebugSystemOptions{
		STTBaseURL:   "http://192.168.1.207:8766",
		STTStreamURL: "ws://192.168.1.207:8766/stt",
	})
	req := httptest.NewRequest(http.MethodGet, "http://192.168.1.204:18790/viewer/runtime-config", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	var body RuntimeConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.STTStreamURL != "ws://192.168.1.204:18790/stt" {
		t.Fatalf("unexpected LAN stt stream url: %+v", body)
	}
}

func TestHandleRuntimeConfig_ReturnsSameOriginWSSForTailscaleHTTPS(t *testing.T) {
	handler := HandleRuntimeConfig(DebugSystemOptions{
		STTBaseURL:   "http://192.168.1.207:8766",
		STTStreamURL: "ws://192.168.1.207:8766/stt",
	})
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18790/viewer/runtime-config", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "fujitsu-ubunts.tailb07d8d.ts.net")
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	var body RuntimeConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.STTStreamURL != "wss://fujitsu-ubunts.tailb07d8d.ts.net/stt" {
		t.Fatalf("unexpected Tailscale stt stream url: %+v", body)
	}
	if body.STTBaseURL != "http://192.168.1.207:8766" {
		t.Fatalf("server-side stt base url should remain LAN-local: %+v", body)
	}
}

func TestHandleRuntimeConfig_ReturnsLLMOpsEnabled(t *testing.T) {
	handler := HandleRuntimeConfig(DebugSystemOptions{
		LLMOpsConfigured: true,
		LLMOpsEnabled:    true,
		LLMOpsBaseURL:    "http://192.168.1.31:8079/",
		LocalLLM: LocalLLMRuntimeConfig{
			Enabled:           true,
			Provider:          "local_openai",
			ChatBaseURL:       "http://192.168.1.31:8081/",
			WorkerBaseURL:     "http://192.168.1.31:8082/",
			HeavyBaseURL:      "http://192.168.1.31:8083/",
			WildBaseURL:       "http://192.168.1.31:8084/",
			ChatModel:         "Chat",
			WorkerModel:       "Worker",
			ChatWorkerModel:   "ChatWorker",
			HeavyModel:        "Heavy",
			WildModel:         "Wild",
			TimeoutSec:        120,
			GlobalConcurrency: 1,
			ModelConcurrency:  1,
			ModelContext:      131072,
		},
		WebwrightFetch: WebwrightFetchRuntimeConfig{
			Enabled:           true,
			RunnerPath:        "tools/webwright_fetch/run_webwright_fetch.py",
			ConfigPath:        "tools/webwright_fetch/config_local_worker.yaml",
			OutputDir:         "tmp/webwright_runs",
			StagingOutputDir:  "tmp/webwright_staging",
			UvxFrom:           "git+https://github.com/microsoft/Webwright.git",
			ResponsesEndpoint: "http://192.168.1.31:8082/v1/responses/",
			Model:             "Coder1",
			APIKeyConfigured:  true,
		},
		WebGather: WebGatherRuntimeConfig{
			SearXNGBaseURL: "http://127.0.0.1:8888/",
			YaCyBaseURL:    "http://127.0.0.1:8090/",
			FetchCache:     true,
			FailureCache:   true,
			RateState:      true,
		},
		BrowserActor: BrowserActorRuntimeConfig{
			Enabled:            true,
			RunnerPath:         "tools/browser_actor/run_browser_actor.mjs",
			NodeBinary:         "node",
			Browser:            "chromium",
			HeadlessDefault:    true,
			ProfileRoot:        "workspace/browser_profiles",
			ArtifactRoot:       "workspace/browser_runs",
			TimeoutMS:          30000,
			MaxActions:         30,
			NetworkScope:       "allowlist",
			AllowedOriginCount: 3,
			SaveTrace:          true,
			SaveScreenshot:     true,
			MaskSecrets:        true,
		},
		SecretRefs: []SecretRefRuntimeConfig{
			{Ref: " config:local_llm.api_key ", Label: " Local LLM API key ", Scope: " local_llm ", Configured: true},
			{Ref: "config:webwright_fetch.api_key", Label: "Webwright Fetch local API key", Scope: "tool", Configured: true},
			{Ref: "config:local_llm.api_key", Label: "duplicate", Scope: "local_llm", Configured: true},
			{Ref: "", Label: "ignored", Scope: "provider", Configured: true},
		},
	})
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/viewer/runtime-config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	var body RuntimeConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.LLMOpsEnabled {
		t.Fatalf("expected llm_ops_enabled: %+v", body)
	}
	if !body.LLMOpsConfigured {
		t.Fatalf("expected llm_ops_configured: %+v", body)
	}
	if body.LLMOpsBaseURL != "http://192.168.1.31:8079" {
		t.Fatalf("unexpected llm ops base url: %+v", body)
	}
	if !body.LocalLLM.Enabled || body.LocalLLM.ChatBaseURL != "http://192.168.1.31:8081" || body.LocalLLM.WorkerModel != "Worker" || body.LocalLLM.ChatWorkerModel != "ChatWorker" || body.LocalLLM.HeavyBaseURL != "http://192.168.1.31:8083" || body.LocalLLM.HeavyModel != "Heavy" || body.LocalLLM.ModelContext != 131072 {
		t.Fatalf("unexpected local llm runtime config: %+v", body.LocalLLM)
	}
	if !body.WebwrightFetch.Enabled || body.WebwrightFetch.ResponsesEndpoint != "http://192.168.1.31:8082/v1/responses" || body.WebwrightFetch.Model != "Coder1" {
		t.Fatalf("unexpected webwright fetch runtime config: %+v", body.WebwrightFetch)
	}
	if !body.WebwrightFetch.APIKeyConfigured {
		t.Fatalf("expected webwright api key configured without exposing value: %+v", body.WebwrightFetch)
	}
	if !body.WebGather.SearXNGConfigured || body.WebGather.SearXNGBaseURL != "http://127.0.0.1:8888" || !body.WebGather.YaCyConfigured || body.WebGather.YaCyBaseURL != "http://127.0.0.1:8090" {
		t.Fatalf("unexpected web gather runtime config: %+v", body.WebGather)
	}
	if !body.WebGather.FetchCache || !body.WebGather.FailureCache || !body.WebGather.RateState {
		t.Fatalf("expected web gather cache flags: %+v", body.WebGather)
	}
	if !body.BrowserActor.Enabled || body.BrowserActor.RunnerPath != "tools/browser_actor/run_browser_actor.mjs" || body.BrowserActor.ProfileRoot != "workspace/browser_profiles" || body.BrowserActor.AllowedOriginCount != 3 {
		t.Fatalf("unexpected browser actor runtime config: %+v", body.BrowserActor)
	}
	if !body.BrowserActor.HeadlessDefault || !body.BrowserActor.SaveTrace || !body.BrowserActor.SaveScreenshot || !body.BrowserActor.MaskSecrets {
		t.Fatalf("expected browser actor safe flags: %+v", body.BrowserActor)
	}
	if len(body.SecretRefs) != 2 {
		t.Fatalf("expected normalized secret refs without duplicates: %+v", body.SecretRefs)
	}
	if body.SecretRefs[0].Ref != "config:local_llm.api_key" || body.SecretRefs[0].Label != "Local LLM API key" || body.SecretRefs[0].Scope != "local_llm" || !body.SecretRefs[0].Configured {
		t.Fatalf("unexpected local llm secret ref: %+v", body.SecretRefs)
	}
	if body.SecretRefs[1].Ref != "config:webwright_fetch.api_key" || body.SecretRefs[1].Scope != "tool" || !body.SecretRefs[1].Configured {
		t.Fatalf("unexpected webwright secret ref: %+v", body.SecretRefs)
	}
	if strings.Contains(rec.Body.String(), "test-secret") {
		t.Fatalf("runtime config leaked a secret value: %s", rec.Body.String())
	}
}

func TestHandleRuntimeConfig_ReturnsRuntimeReadinessWithoutSecretValues(t *testing.T) {
	handler := HandleRuntimeConfig(DebugSystemOptions{
		STTBaseURL:   "http://127.0.0.1:8766",
		TTSBaseURL:   "http://127.0.0.1:7870",
		STTStreamURL: "wss://127.0.0.1/stt",
		RuntimeReadiness: RuntimeDependencyReadiness{
			SlackCredentialsPresent:      true,
			SlackWebhookRegistered:       true,
			SlackFilePayloadPipeline:     true,
			DiscordCredentialsPresent:    false,
			DiscordWebhookRegistered:     false,
			DiscordFilePayloadPipeline:   false,
			TelegramCredentialsPresent:   true,
			TelegramWebhookRegistered:    true,
			TelegramFilePayloadPipeline:  true,
			STTGatewayEnvPresent:         true,
			TTSProviderEnvPresent:        false,
			DistributedEnabled:           true,
			DistributedTransportsPresent: true,
			DistributedSSHConfigured:     true,
			DistributedSSHConnected:      false,
			DistributedLocalTransport:    true,
			ConversationEnabled:          true,
			L1SQLiteConfigPresent:        true,
			MemoryLayersAvailable:        true,
			MemoryLayersStatus:           true,
			SourceRegistryAvailable:      true,
			SourceRegistryStatus:         true,
			DomainGraphAvailable:         true,
			DomainGraphStatus:            true,
			KnowledgeMemoryEnabled:       true,
			KnowledgeMemoryStatus:        true,
			BrowserTraceAPIEnabled:       true,
			BrowserTraceAPIStatus:        true,
			BrowserTraceAPIFetcher:       true,
			SandboxEnabled:               false,
			SandboxStatusAvailable:       true,
		},
	})
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/viewer/runtime-config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	var body RuntimeConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.RuntimeReadiness.SlackCredentialsPresent || !body.RuntimeReadiness.SlackWebhookRegistered || !body.RuntimeReadiness.SlackFilePayloadPipeline || body.RuntimeReadiness.DiscordCredentialsPresent || body.RuntimeReadiness.DiscordWebhookRegistered || body.RuntimeReadiness.DiscordFilePayloadPipeline || !body.RuntimeReadiness.TelegramCredentialsPresent || !body.RuntimeReadiness.TelegramWebhookRegistered || !body.RuntimeReadiness.TelegramFilePayloadPipeline || !body.RuntimeReadiness.STTGatewayEnvPresent || !body.RuntimeReadiness.STTGatewayConfigPresent || body.RuntimeReadiness.TTSProviderEnvPresent || !body.RuntimeReadiness.TTSProviderConfigPresent || !body.RuntimeReadiness.DistributedEnabled || !body.RuntimeReadiness.DistributedTransportsPresent || !body.RuntimeReadiness.DistributedSSHConfigured || body.RuntimeReadiness.DistributedSSHConnected || !body.RuntimeReadiness.DistributedLocalTransport || !body.RuntimeReadiness.ConversationEnabled || !body.RuntimeReadiness.L1SQLiteConfigPresent || !body.RuntimeReadiness.MemoryLayersAvailable || !body.RuntimeReadiness.MemoryLayersStatus || !body.RuntimeReadiness.SourceRegistryAvailable || !body.RuntimeReadiness.SourceRegistryStatus || !body.RuntimeReadiness.DomainGraphAvailable || !body.RuntimeReadiness.DomainGraphStatus || !body.RuntimeReadiness.KnowledgeMemoryEnabled || !body.RuntimeReadiness.KnowledgeMemoryStatus || !body.RuntimeReadiness.BrowserTraceAPIEnabled || !body.RuntimeReadiness.BrowserTraceAPIStatus || !body.RuntimeReadiness.BrowserTraceAPIFetcher || body.RuntimeReadiness.SandboxEnabled || !body.RuntimeReadiness.SandboxStatusAvailable {
		t.Fatalf("unexpected runtime readiness: %+v", body.RuntimeReadiness)
	}
	if strings.Contains(rec.Body.String(), "SLACK_BOT_TOKEN") || strings.Contains(rec.Body.String(), "TELEGRAM_BOT_TOKEN") {
		t.Fatalf("runtime config leaked env names or secrets: %s", rec.Body.String())
	}
}

func TestHandleRuntimeConfig_ReturnsVoiceChatFields(t *testing.T) {
	handler := HandleRuntimeConfig(DebugSystemOptions{
		STTStreamURL:     "ws://127.0.0.1/stt",
		VoiceChatEnabled: true,
		VoiceInputMode:   "vds_sub",
	})
	req := httptest.NewRequest(http.MethodGet, "https://fujitsu-ubunts.tailb07d8d.ts.net/viewer/runtime-config", nil)
	req.Header.Set("X-Forwarded-Host", "fujitsu-ubunts.tailb07d8d.ts.net")
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()

	handler(rec, req)

	var body RuntimeConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode runtime config: %v", err)
	}
	if !body.VoiceChatEnabled {
		t.Fatalf("expected voice_chat_enabled=true, got %+v", body)
	}
	if body.VoiceChatStreamURL != "wss://fujitsu-ubunts.tailb07d8d.ts.net/voice-chat" {
		t.Fatalf("unexpected voice chat stream url: %+v", body)
	}
	if body.VoiceInputMode != "vds_sub" {
		t.Fatalf("unexpected voice input mode: %+v", body)
	}
}

func TestHandleRuntimeConfig_DefaultVoiceInputModeIsSTTPrimary(t *testing.T) {
	handler := HandleRuntimeConfig(DebugSystemOptions{STTStreamURL: "ws://127.0.0.1/stt"})
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18790/viewer/runtime-config", nil))

	var body RuntimeConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode runtime config: %v", err)
	}
	if body.VoiceInputMode != "stt_primary" {
		t.Fatalf("unexpected voice input mode: %+v", body)
	}
	if body.VoiceChatEnabled {
		t.Fatalf("expected voice chat disabled by default: %+v", body)
	}
}
