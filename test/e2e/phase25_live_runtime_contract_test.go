//go:build e2e

package e2e_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/pkg/rencrowclient"
)

func TestE2E_Phase25LiveRuntimeHealth(t *testing.T) {
	if os.Getenv("PICOCLAW_LIVE_E2E") != "1" {
		t.Skip("set PICOCLAW_LIVE_E2E=1 to verify live service health")
	}

	baseURL := phase25LiveBaseURL()
	client, err := rencrowclient.New(baseURL, rencrowclient.WithHTTPClient(&http.Client{Timeout: 20 * time.Second}))
	if err != nil {
		t.Fatalf("create RenCrow client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	report, err := client.RuntimeHealth(ctx)
	if err != nil {
		t.Fatalf("RuntimeHealth() live call failed at %s: %v", baseURL, err)
	}
	if report.Status != "ok" {
		t.Fatalf("live /health status=%s checks=%+v, want ok", report.Status, report.Checks)
	}
}

func TestE2E_Phase25LiveLLMOpsProxyClientBlockedOrLive(t *testing.T) {
	if os.Getenv("PICOCLAW_LIVE_E2E") != "1" {
		t.Skip("set PICOCLAW_LIVE_E2E=1 to verify live LLM Ops status")
	}

	baseURL := phase25LiveBaseURL()
	client, err := rencrowclient.New(baseURL, rencrowclient.WithHTTPClient(&http.Client{Timeout: 20 * time.Second}))
	if err != nil {
		t.Fatalf("create RenCrow client: %v", err)
	}
	healthCtx, healthCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer healthCancel()
	health, err := client.LLMOpsHealth(healthCtx)
	if err == nil {
		if health.Status == "" {
			t.Fatalf("LLMOpsHealth() live response missing status: %+v", health)
		}
	} else {
		assertLLMOpsUpstreamUnreachable(t, err)
	}
	statusCtx, statusCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer statusCancel()
	status, err := client.LLMOpsStatus(statusCtx)
	if err == nil {
		if len(status.Roles) == 0 {
			t.Fatalf("LLMOpsStatus() live response missing roles: %+v", status)
		}
		return
	}
	assertLLMOpsUpstreamUnreachable(t, err)
	controlCtx, controlCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer controlCancel()
	assertLLMOpsUpstreamUnreachable(t, client.StopLLMOps(controlCtx, []string{"Worker", "Wild"}))
	controlCtx, controlCancel = context.WithTimeout(context.Background(), 20*time.Second)
	defer controlCancel()
	assertLLMOpsUpstreamUnreachable(t, client.StartLLMOps(controlCtx, "Worker"))
	controlCtx, controlCancel = context.WithTimeout(context.Background(), 20*time.Second)
	defer controlCancel()
	assertLLMOpsUpstreamUnreachable(t, client.RestartLLMOps(controlCtx, "all"))
}

func assertLLMOpsUpstreamUnreachable(t *testing.T, err error) {
	t.Helper()
	var apiErr *rencrowclient.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadGateway || !strings.Contains(apiErr.Body, "upstream unreachable") {
		t.Fatalf("LLM Ops proxy live error = %v, want 502 upstream unreachable or valid response", err)
	}
}

func TestE2E_Phase25LiveViewerRuntimeConfigClient(t *testing.T) {
	if os.Getenv("PICOCLAW_LIVE_E2E") != "1" {
		t.Skip("set PICOCLAW_LIVE_E2E=1 to verify live Viewer runtime config")
	}

	baseURL := phase25LiveBaseURL()
	client, err := rencrowclient.New(baseURL, rencrowclient.WithHTTPClient(&http.Client{Timeout: 5 * time.Second}))
	if err != nil {
		t.Fatalf("create RenCrow client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cfg, err := client.RuntimeConfig(ctx)
	if err != nil {
		t.Fatalf("RuntimeConfig() live call failed at %s: %v", baseURL, err)
	}
	if !cfg.LocalLLM.Enabled {
		t.Fatalf("runtime config must expose enabled local_llm separately from repo example: %+v", cfg.LocalLLM)
	}
	if cfg.STTStreamURL == "" {
		t.Fatalf("runtime config must expose stt_stream_url for Viewer STT contract: %+v", cfg)
	}
	if cfg.RuntimeReadiness.SlackCredentialsPresent == nil ||
		cfg.RuntimeReadiness.SlackWebhookRegistered == nil ||
		cfg.RuntimeReadiness.SlackFilePayloadPipeline == nil ||
		cfg.RuntimeReadiness.DiscordCredentialsPresent == nil ||
		cfg.RuntimeReadiness.DiscordWebhookRegistered == nil ||
		cfg.RuntimeReadiness.DiscordFilePayloadPipeline == nil ||
		cfg.RuntimeReadiness.TelegramCredentialsPresent == nil ||
		cfg.RuntimeReadiness.TelegramWebhookRegistered == nil ||
		cfg.RuntimeReadiness.TelegramFilePayloadPipeline == nil ||
		cfg.RuntimeReadiness.STTGatewayEnvPresent == nil ||
		cfg.RuntimeReadiness.STTGatewayConfigPresent == nil ||
		cfg.RuntimeReadiness.TTSProviderEnvPresent == nil ||
		cfg.RuntimeReadiness.TTSProviderConfigPresent == nil ||
		cfg.RuntimeReadiness.DistributedEnabled == nil ||
		cfg.RuntimeReadiness.DistributedTransportsPresent == nil ||
		cfg.RuntimeReadiness.DistributedSSHConfigured == nil ||
		cfg.RuntimeReadiness.DistributedSSHConnected == nil ||
		cfg.RuntimeReadiness.DistributedLocalTransport == nil ||
		cfg.RuntimeReadiness.ConversationEnabled == nil ||
		cfg.RuntimeReadiness.L1SQLiteConfigPresent == nil ||
		cfg.RuntimeReadiness.MemoryLayersAvailable == nil ||
		cfg.RuntimeReadiness.MemoryLayersStatus == nil ||
		cfg.RuntimeReadiness.SourceRegistryAvailable == nil ||
		cfg.RuntimeReadiness.SourceRegistryStatus == nil ||
		cfg.RuntimeReadiness.KnowledgeMemoryEnabled == nil ||
		cfg.RuntimeReadiness.KnowledgeMemoryStatus == nil ||
		cfg.RuntimeReadiness.BrowserTraceAPIEnabled == nil ||
		cfg.RuntimeReadiness.BrowserTraceAPIStatus == nil ||
		cfg.RuntimeReadiness.BrowserTraceAPIFetcher == nil ||
		cfg.RuntimeReadiness.SandboxEnabled == nil ||
		cfg.RuntimeReadiness.SandboxStatusAvailable == nil {
		t.Fatalf("runtime config must expose runtime_readiness booleans: %+v", cfg.RuntimeReadiness)
	}
	if !*cfg.RuntimeReadiness.STTGatewayConfigPresent || !*cfg.RuntimeReadiness.TTSProviderConfigPresent {
		t.Fatalf("runtime config must expose effective audio config presence separately from env presence: %+v", cfg.RuntimeReadiness)
	}
	if (*cfg.RuntimeReadiness.SlackFilePayloadPipeline && !*cfg.RuntimeReadiness.SlackWebhookRegistered) ||
		(*cfg.RuntimeReadiness.DiscordFilePayloadPipeline && !*cfg.RuntimeReadiness.DiscordWebhookRegistered) ||
		(*cfg.RuntimeReadiness.TelegramFilePayloadPipeline && !*cfg.RuntimeReadiness.TelegramWebhookRegistered) {
		t.Fatalf("runtime config must not claim channel file payload pipeline without webhook route: %+v", cfg.RuntimeReadiness)
	}
	if *cfg.RuntimeReadiness.DistributedSSHConnected && (!*cfg.RuntimeReadiness.DistributedEnabled || !*cfg.RuntimeReadiness.DistributedSSHConfigured) {
		t.Fatalf("runtime config must not claim connected ssh transport without distributed ssh config: %+v", cfg.RuntimeReadiness)
	}
	if *cfg.RuntimeReadiness.SandboxEnabled && !*cfg.RuntimeReadiness.SandboxStatusAvailable {
		t.Fatalf("runtime config must not claim enabled sandbox without status route: %+v", cfg.RuntimeReadiness)
	}
	if *cfg.RuntimeReadiness.MemoryLayersAvailable && (!*cfg.RuntimeReadiness.ConversationEnabled || !*cfg.RuntimeReadiness.L1SQLiteConfigPresent || !*cfg.RuntimeReadiness.MemoryLayersStatus) {
		t.Fatalf("runtime config must not claim memory layers available without l1 and status route: %+v", cfg.RuntimeReadiness)
	}
	if *cfg.RuntimeReadiness.SourceRegistryAvailable && !*cfg.RuntimeReadiness.SourceRegistryStatus {
		t.Fatalf("runtime config must not claim source registry available without status route: %+v", cfg.RuntimeReadiness)
	}
	if *cfg.RuntimeReadiness.KnowledgeMemoryEnabled && !*cfg.RuntimeReadiness.KnowledgeMemoryStatus {
		t.Fatalf("runtime config must not claim enabled knowledge memory without status route: %+v", cfg.RuntimeReadiness)
	}
	if *cfg.RuntimeReadiness.BrowserTraceAPIEnabled && !*cfg.RuntimeReadiness.BrowserTraceAPIStatus {
		t.Fatalf("runtime config must not claim enabled browser trace API without status route: %+v", cfg.RuntimeReadiness)
	}
	if *cfg.RuntimeReadiness.BrowserTraceAPIFetcher && (!*cfg.RuntimeReadiness.BrowserTraceAPIEnabled || !*cfg.RuntimeReadiness.BrowserTraceAPIStatus) {
		t.Fatalf("runtime config must not claim browser trace fetcher route without status route: %+v", cfg.RuntimeReadiness)
	}
}

func TestE2E_Phase25LiveMemoryLayersUnavailableWhenL1Disabled(t *testing.T) {
	if os.Getenv("PICOCLAW_LIVE_E2E") != "1" {
		t.Skip("set PICOCLAW_LIVE_E2E=1 to verify live Viewer memory layers availability")
	}

	baseURL := phase25LiveBaseURL()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(baseURL + "/viewer/memory/layers")
	if err != nil {
		t.Fatalf("GET /viewer/memory/layers failed at %s: %v", baseURL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /viewer/memory/layers body: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("/viewer/memory/layers status=%d body=%q, want 503 unavailable", resp.StatusCode, string(body))
	}
	if !strings.Contains(string(body), "memory layers unavailable") {
		t.Fatalf("/viewer/memory/layers body=%q, want unavailable message", string(body))
	}
}

func TestE2E_Phase25LiveSandboxStatusUnavailableWhenSandboxDisabled(t *testing.T) {
	if os.Getenv("PICOCLAW_LIVE_E2E") != "1" {
		t.Skip("set PICOCLAW_LIVE_E2E=1 to verify live Sandbox status availability")
	}

	baseURL := phase25LiveBaseURL()
	client, err := rencrowclient.New(baseURL, rencrowclient.WithHTTPClient(&http.Client{Timeout: 5 * time.Second}))
	if err != nil {
		t.Fatalf("create RenCrow client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = client.SandboxStatus(ctx, 1)
	if err == nil {
		t.Fatalf("SandboxStatus() unexpectedly succeeded; sandbox is disabled in this live config")
	}
	var apiErr *rencrowclient.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("SandboxStatus() error = %T %v, want APIError 503", err, err)
	}
	if !strings.Contains(apiErr.Body, "sandbox store unavailable") {
		t.Fatalf("SandboxStatus() body=%q, want unavailable message", apiErr.Body)
	}
}

func TestE2E_Phase25LiveDebugSystemSnapshotClient(t *testing.T) {
	if os.Getenv("PICOCLAW_LIVE_E2E") != "1" {
		t.Skip("set PICOCLAW_LIVE_E2E=1 to verify live Viewer debug system")
	}

	baseURL := phase25LiveBaseURL()
	client, err := rencrowclient.New(baseURL, rencrowclient.WithHTTPClient(&http.Client{Timeout: 4 * time.Second}))
	if err != nil {
		t.Fatalf("create RenCrow client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	started := time.Now()
	snapshot, err := client.DebugSystemSnapshot(ctx)
	if err != nil {
		t.Fatalf("DebugSystemSnapshot() live call failed at %s: %v", baseURL, err)
	}
	if time.Since(started) > 3500*time.Millisecond {
		t.Fatalf("DebugSystemSnapshot() took too long: %s snapshot=%+v", time.Since(started), snapshot)
	}
	if snapshot.UpdatedAt == "" {
		t.Fatalf("debug system snapshot missing updated_at: %+v", snapshot)
	}
	if (snapshot.Audio.STTBaseURL != "" && !snapshot.Audio.STTOK) || (snapshot.Audio.TTSBaseURL != "" && (!snapshot.Audio.TTSLiveOK || !snapshot.Audio.TTSReadyOK)) {
		if snapshot.Audio.LastError == "" {
			t.Fatalf("audio readiness blocked but missing last_error: %+v", snapshot.Audio)
		}
	}
}

func phase25LiveBaseURL() string {
	baseURL := strings.TrimRight(os.Getenv("PICOCLAW_LIVE_BASE_URL"), "/")
	if baseURL == "" {
		return "http://127.0.0.1:18790"
	}
	return baseURL
}
