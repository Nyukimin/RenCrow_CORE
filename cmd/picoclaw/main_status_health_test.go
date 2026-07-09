package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	domainexecution "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/execution"
	domainhealth "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/health"
)

func TestCollectOllamaHealthRequirements_UsesSingleModel(t *testing.T) {
	cfg := &config.Config{
		Ollama: config.OllamaConfig{
			BaseURL:    "http://127.0.0.1:11434",
			Model:      "Chat",
			MaxContext: 4096,
		},
	}

	got := collectOllamaHealthRequirements(cfg)
	if len(got) != 1 {
		t.Fatalf("expected 1 requirement, got %d: %#v", len(got), got)
	}
	if got[0].Name != "Chat" {
		t.Fatalf("unexpected requirements: %#v", got)
	}
	if got[0].MaxContext != 4096 {
		t.Fatalf("expected max context to propagate, got %#v", got)
	}
}

func TestCollectOllamaHealthRequirements_SkipsEmptyModel(t *testing.T) {
	cfg := &config.Config{
		Ollama: config.OllamaConfig{
			MaxContext: 4096,
		},
	}

	got := collectOllamaHealthRequirements(cfg)
	if len(got) != 0 {
		t.Fatalf("expected no requirements, got %#v", got)
	}
}

func TestBuildHealthService_LocalLLMUsesOpenAICompatibleChecks(t *testing.T) {
	var chatHits, workerHits int
	mux := http.NewServeMux()
	mux.HandleFunc("/chat/v1/models", func(w http.ResponseWriter, r *http.Request) {
		chatHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"Chat"}]}`))
	})
	mux.HandleFunc("/worker/v1/models", func(w http.ResponseWriter, r *http.Request) {
		workerHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"Worker"},{"id":"ChatWorker"}]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := &config.Config{
		LocalLLM: config.LocalLLMConfig{
			Enabled:         true,
			Provider:        "local_openai",
			BaseURL:         "http://127.0.0.1:1",
			ChatBaseURL:     srv.URL + "/chat",
			WorkerBaseURL:   srv.URL + "/worker",
			WildBaseURL:     "http://127.0.0.1:1",
			ChatModel:       "Chat",
			WorkerModel:     "Worker",
			ChatWorkerModel: "ChatWorker",
			WildModel:       "Wild",
			TimeoutSec:      1,
		},
		Ollama: config.OllamaConfig{BaseURL: "http://127.0.0.1:1", Model: "Chat"},
	}
	warmup := false
	cfg.LocalLLM.Warmup = &warmup

	report := buildHealthService(cfg).RunChecks(context.Background())
	if report.Status != domainhealth.StatusOK {
		t.Fatalf("status = %s, want ok; checks=%+v", report.Status, report.Checks)
	}
	if chatHits != 1 || workerHits != 2 {
		t.Fatalf("expected chat/worker hits, got chat=%d worker=%d", chatHits, workerHits)
	}
	for _, check := range report.Checks {
		if strings.HasPrefix(check.Name, "ollama") {
			t.Fatalf("local_llm health should not include ollama check: %+v", report.Checks)
		}
	}
}

func TestBuildHealthService_SkipsDisabledRuntimeTopologyRoles(t *testing.T) {
	var chatHits, workerHits int
	mux := http.NewServeMux()
	mux.HandleFunc("/chat/v1/models", func(w http.ResponseWriter, r *http.Request) {
		chatHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"Chat"}]}`))
	})
	mux.HandleFunc("/worker/v1/models", func(w http.ResponseWriter, r *http.Request) {
		workerHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"Worker"},{"id":"ChatWorker"}]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	disabled := false
	cfg := &config.Config{
		RuntimeTopology: config.RuntimeTopologyConfig{
			Modules: map[string]config.RuntimeTopologyModuleConfig{
				"RenCraw_LLM": {
					Roles: map[string]config.RuntimeTopologyRoleConfig{
						"heavy": {Enabled: &disabled},
						"wild":  {Enabled: &disabled},
					},
				},
			},
		},
		LocalLLM: config.LocalLLMConfig{
			Enabled:         true,
			Provider:        "local_openai",
			BaseURL:         "http://127.0.0.1:1",
			ChatBaseURL:     srv.URL + "/chat",
			WorkerBaseURL:   srv.URL + "/worker",
			HeavyBaseURL:    "http://127.0.0.1:1",
			WildBaseURL:     "http://127.0.0.1:1",
			ChatModel:       "Chat",
			WorkerModel:     "Worker",
			ChatWorkerModel: "ChatWorker",
			HeavyModel:      "Heavy",
			WildModel:       "Wild",
			TimeoutSec:      1,
		},
	}
	warmup := true
	cfg.LocalLLM.Warmup = &warmup

	report := buildHealthService(cfg).RunChecks(context.Background())
	if report.Status != domainhealth.StatusOK {
		t.Fatalf("status = %s, want ok; checks=%+v", report.Status, report.Checks)
	}
	if chatHits != 1 || workerHits != 2 {
		t.Fatalf("expected only chat/worker hits, got chat=%d worker=%d", chatHits, workerHits)
	}
}

type fakeHealthChecker struct {
	report domainhealth.HealthReport
}

func (f *fakeHealthChecker) RunChecks(_ context.Context) domainhealth.HealthReport {
	return f.report
}

func TestRunHealthCommand_JSONContract(t *testing.T) {
	checker := &fakeHealthChecker{
		report: domainhealth.HealthReport{
			Status: domainhealth.StatusOK,
			Checks: []domainhealth.CheckResult{
				{Name: "ollama", Status: domainhealth.StatusOK, Message: "ok", Duration: 5 * time.Millisecond},
			},
		},
	}
	var out, errOut bytes.Buffer
	code := runHealthCommand([]string{"--json"}, checker, &out, &errOut, fixedNow)
	if code != 0 {
		t.Fatalf("expected code 0, got %d", code)
	}
	var payload struct {
		OK        bool   `json:"ok"`
		Component string `json:"component"`
		Status    string `json:"status"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !payload.OK || payload.Component != "health" || payload.Status != "ok" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

func TestRunStatusCommand_DeepUsageJSON(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{Host: "127.0.0.1", Port: 18790},
		Ollama: config.OllamaConfig{BaseURL: "http://127.0.0.1:11434", Model: "qwen3:8b"},
	}
	checker := &fakeHealthChecker{report: domainhealth.HealthReport{
		Status: domainhealth.StatusDegraded,
		Checks: []domainhealth.CheckResult{
			{Name: "ollama", Status: domainhealth.StatusDegraded, Message: "slow", Duration: 50 * time.Millisecond},
		},
	}}
	statsLoader := func(_ *config.Config) (map[domainexecution.Status]int, error) {
		return map[domainexecution.Status]int{
			domainexecution.StatusRunning: 2,
			domainexecution.StatusDenied:  0,
			domainexecution.StatusFailed:  3,
		}, nil
	}
	usageLoader := func(_ *config.Config) (map[string]map[string]int, error) {
		return map[string]map[string]int{"status": {"passed": 4, "failed": 1}}, nil
	}

	var out, errOut bytes.Buffer
	code := runStatusCommand(
		[]string{"--deep", "--usage", "--json"},
		cfg,
		checker,
		statsLoader,
		usageLoader,
		&out,
		&errOut,
		fixedNow,
	)
	if code != 0 {
		t.Fatalf("expected code 0, got %d (err=%s)", code, errOut.String())
	}
	var payload struct {
		Component string         `json:"component"`
		Status    string         `json:"status"`
		Details   map[string]any `json:"details"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if payload.Component != "status" || payload.Status != "degraded" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
	if _, ok := payload.Details["execution"]; !ok {
		t.Fatalf("expected execution details: %+v", payload.Details)
	}
	if _, ok := payload.Details["usage"]; !ok {
		t.Fatalf("expected usage details: %+v", payload.Details)
	}
}

func TestRunStatusCommand_UsageErrorText(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{Host: "127.0.0.1", Port: 18790},
		Ollama: config.OllamaConfig{BaseURL: "http://127.0.0.1:11434", Model: "qwen3:8b"},
	}
	checker := &fakeHealthChecker{report: domainhealth.HealthReport{Status: domainhealth.StatusOK}}
	statsLoader := func(_ *config.Config) (map[domainexecution.Status]int, error) {
		return map[domainexecution.Status]int{}, nil
	}
	usageLoader := func(_ *config.Config) (map[string]map[string]int, error) {
		return nil, errors.New("no evidence")
	}
	var out, errOut bytes.Buffer
	code := runStatusCommand(
		[]string{"--usage"},
		cfg,
		checker,
		statsLoader,
		usageLoader,
		&out,
		&errOut,
		fixedNow,
	)
	if code != 0 {
		t.Fatalf("expected code 0, got %d", code)
	}
	if !strings.Contains(out.String(), "Usage:") || !strings.Contains(out.String(), "unavailable") {
		t.Fatalf("expected usage unavailable output, got: %s", out.String())
	}
}
