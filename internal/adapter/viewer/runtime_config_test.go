package viewer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestHandleRuntimeConfig_ReturnsModuleGatewayStatus(t *testing.T) {
	handler := HandleRuntimeConfig(DebugSystemOptions{
		LLMGateway: LLMGatewayRuntimeConfig{
			BaseURL: "http://192.168.1.31:8090/",
			Ready:   true,
		},
		WebwrightFetch: WebwrightFetchRuntimeConfig{
			Enabled:           true,
			RunnerPath:        "tools/webwright_fetch/run_webwright_fetch.py",
			ConfigPath:        "tools/webwright_fetch/config_local_worker.yaml",
			OutputDir:         "tmp/webwright_runs",
			StagingOutputDir:  "tmp/webwright_staging",
			UvxFrom:           "git+https://github.com/microsoft/Webwright.git",
			ResponsesEndpoint: "http://192.168.1.31:8090/v1/responses/",
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
			{Ref: " env:RENCROW_LLM_API_KEY ", Label: " RenCrow LLM Gateway API key ", Scope: " llm_gateway ", Configured: true},
			{Ref: "config:webwright_fetch.api_key", Label: "Webwright Fetch local API key", Scope: "tool", Configured: true},
			{Ref: "env:RENCROW_LLM_API_KEY", Label: "duplicate", Scope: "llm_gateway", Configured: true},
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
	if !body.LLMGateway.Ready || body.LLMGateway.BaseURL != "http://192.168.1.31:8090" {
		t.Fatalf("unexpected RenCrow_LLM Gateway runtime config: %+v", body.LLMGateway)
	}
	if !body.WebwrightFetch.Enabled || body.WebwrightFetch.ResponsesEndpoint != "http://192.168.1.31:8090/v1/responses" || body.WebwrightFetch.Model != "Coder1" {
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
	if body.SecretRefs[0].Ref != "env:RENCROW_LLM_API_KEY" || body.SecretRefs[0].Label != "RenCrow LLM Gateway API key" || body.SecretRefs[0].Scope != "llm_gateway" || !body.SecretRefs[0].Configured {
		t.Fatalf("unexpected RenCrow_LLM Gateway secret ref: %+v", body.SecretRefs)
	}
	if body.SecretRefs[1].Ref != "config:webwright_fetch.api_key" || body.SecretRefs[1].Scope != "tool" || !body.SecretRefs[1].Configured {
		t.Fatalf("unexpected webwright secret ref: %+v", body.SecretRefs)
	}
	if strings.Contains(rec.Body.String(), "test-secret") {
		t.Fatalf("runtime config leaked a secret value: %s", rec.Body.String())
	}
}

func TestHandleRuntimeConfig_RefreshesLLMGatewayReadinessAndClearsStaleWarning(t *testing.T) {
	var probeContext context.Context
	handler := HandleRuntimeConfig(DebugSystemOptions{
		LLMGateway: LLMGatewayRuntimeConfig{
			BaseURL:            "http://127.0.0.1:8090/",
			Ready:              false,
			AutoStartAttempted: true,
			AutoStarted:        true,
			Warning:            "stale startup warning",
		},
		LLMGatewayHealthCheck: func(ctx context.Context) error {
			probeContext = ctx
			return nil
		},
	})
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/viewer/runtime-config", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	var body RuntimeConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode runtime config: %v", err)
	}
	if !body.LLMGateway.Ready || body.LLMGateway.Warning != "" {
		t.Fatalf("healthy request probe did not refresh gateway status: %+v", body.LLMGateway)
	}
	if !body.LLMGateway.AutoStartAttempted || !body.LLMGateway.AutoStarted {
		t.Fatalf("request probe did not preserve startup metadata: %+v", body.LLMGateway)
	}
	if probeContext == nil {
		t.Fatal("request-time gateway probe was not called")
	}
	if deadline, ok := probeContext.Deadline(); !ok || time.Until(deadline) > 1100*time.Millisecond {
		t.Fatalf("gateway probe was not bounded: deadline=%v ok=%v", deadline, ok)
	}
}

func TestHandleRuntimeConfig_ReportsLLMGatewayUnavailableWithoutLeakingProbeError(t *testing.T) {
	const privateDiagnostic = "private-llm-diagnostic-sentinel"
	handler := HandleRuntimeConfig(DebugSystemOptions{
		LLMGateway: LLMGatewayRuntimeConfig{
			BaseURL: "http://127.0.0.1:8090",
			Ready:   true,
		},
		LLMGatewayHealthCheck: func(context.Context) error {
			return errors.New(privateDiagnostic)
		},
	})
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/viewer/runtime-config", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	var body RuntimeConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode runtime config: %v", err)
	}
	if body.LLMGateway.Ready || body.LLMGateway.Warning == "" {
		t.Fatalf("failed request probe was not reflected as unavailable: %+v", body.LLMGateway)
	}
	if strings.Contains(rec.Body.String(), privateDiagnostic) {
		t.Fatalf("runtime config leaked gateway probe detail: %s", rec.Body.String())
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
	if !body.RuntimeReadiness.SlackCredentialsPresent || !body.RuntimeReadiness.SlackWebhookRegistered || !body.RuntimeReadiness.SlackFilePayloadPipeline || body.RuntimeReadiness.DiscordCredentialsPresent || body.RuntimeReadiness.DiscordWebhookRegistered || body.RuntimeReadiness.DiscordFilePayloadPipeline || !body.RuntimeReadiness.TelegramCredentialsPresent || !body.RuntimeReadiness.TelegramWebhookRegistered || !body.RuntimeReadiness.TelegramFilePayloadPipeline || !body.RuntimeReadiness.STTGatewayConfigPresent || body.RuntimeReadiness.TTSProviderEnvPresent || !body.RuntimeReadiness.TTSProviderConfigPresent || !body.RuntimeReadiness.DistributedEnabled || !body.RuntimeReadiness.DistributedTransportsPresent || !body.RuntimeReadiness.DistributedSSHConfigured || body.RuntimeReadiness.DistributedSSHConnected || !body.RuntimeReadiness.DistributedLocalTransport || !body.RuntimeReadiness.ConversationEnabled || !body.RuntimeReadiness.L1SQLiteConfigPresent || !body.RuntimeReadiness.MemoryLayersAvailable || !body.RuntimeReadiness.MemoryLayersStatus || !body.RuntimeReadiness.SourceRegistryAvailable || !body.RuntimeReadiness.SourceRegistryStatus || !body.RuntimeReadiness.DomainGraphAvailable || !body.RuntimeReadiness.DomainGraphStatus || !body.RuntimeReadiness.KnowledgeMemoryEnabled || !body.RuntimeReadiness.KnowledgeMemoryStatus || !body.RuntimeReadiness.BrowserTraceAPIEnabled || !body.RuntimeReadiness.BrowserTraceAPIStatus || !body.RuntimeReadiness.BrowserTraceAPIFetcher || body.RuntimeReadiness.SandboxEnabled || !body.RuntimeReadiness.SandboxStatusAvailable {
		t.Fatalf("unexpected runtime readiness: %+v", body.RuntimeReadiness)
	}
	if strings.Contains(rec.Body.String(), "SLACK_BOT_TOKEN") || strings.Contains(rec.Body.String(), "TELEGRAM_BOT_TOKEN") {
		t.Fatalf("runtime config leaked env names or secrets: %s", rec.Body.String())
	}
}

func TestHandleRuntimeConfig_ProbesRedisWithoutChangingCoreReadiness(t *testing.T) {
	const privateDiagnostic = "private-redis-diagnostic-sentinel"
	tests := []struct {
		name       string
		configured bool
		check      func(context.Context) error
		status     string
		reachable  bool
	}{
		{name: "disabled", status: "disabled"},
		{name: "available", configured: true, check: func(context.Context) error { return nil }, status: "available", reachable: true},
		{name: "unavailable", configured: true, check: func(context.Context) error { return errors.New(privateDiagnostic) }, status: "unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := HandleRuntimeConfig(DebugSystemOptions{
				RuntimeReadiness: RuntimeDependencyReadiness{RedisConfigured: tt.configured},
				RedisHealthCheck: tt.check,
			})
			rec := httptest.NewRecorder()
			handler(rec, httptest.NewRequest(http.MethodGet, "/viewer/runtime-config", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			var body RuntimeConfig
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.RuntimeReadiness.RedisStatus != tt.status || body.RuntimeReadiness.RedisReachable != tt.reachable {
				t.Fatalf("redis readiness=%+v", body.RuntimeReadiness)
			}
			if strings.Contains(rec.Body.String(), privateDiagnostic) {
				t.Fatalf("runtime config leaked Redis diagnostic detail: %s", rec.Body.String())
			}
		})
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
