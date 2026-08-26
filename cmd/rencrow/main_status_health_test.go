package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domainhealth "github.com/Nyukimin/RenCrow_CORE/internal/domain/health"
)

func TestBuildHealthService_UsesRenCrowLLMGatewayAliases(t *testing.T) {
	var gatewayHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		gatewayHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"mio"},{"id":"worker"},{"id":"shiro"},{"id":"kuro"},{"id":"midori"}]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := &config.Config{
		LLMGateway: config.LLMGatewayConfig{Enabled: true, BaseURL: srv.URL, TimeoutSec: 1},
	}

	report := buildHealthService(cfg).RunChecks(context.Background())
	if report.Status != domainhealth.StatusOK {
		t.Fatalf("status = %s, want ok; checks=%+v", report.Status, report.Checks)
	}
	if gatewayHits.Load() != 5 {
		t.Fatalf("expected five logical alias probes, got %d", gatewayHits.Load())
	}
}

func TestBuildHealthService_UsesGatewayForAllAliases(t *testing.T) {
	var gatewayHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		gatewayHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"mio"},{"id":"worker"},{"id":"shiro"},{"id":"kuro"},{"id":"midori"}]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := &config.Config{
		LLMGateway: config.LLMGatewayConfig{Enabled: true, BaseURL: srv.URL, TimeoutSec: 1},
	}

	report := buildHealthService(cfg).RunChecks(context.Background())
	if report.Status != domainhealth.StatusOK {
		t.Fatalf("status = %s, want ok; checks=%+v", report.Status, report.Checks)
	}
	if gatewayHits.Load() != 5 {
		t.Fatalf("expected five Gateway alias probes; gateway hits=%d", gatewayHits.Load())
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
				{Name: "gateway_mio", Status: domainhealth.StatusOK, Message: "ok", Duration: 5 * time.Millisecond},
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
		Details   struct {
			Checks []struct {
				DurationMS float64 `json:"duration_ms"`
			} `json:"checks"`
		} `json:"details"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !payload.OK || payload.Component != "health" || payload.Status != "ok" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
	if len(payload.Details.Checks) != 1 || payload.Details.Checks[0].DurationMS != 5 {
		t.Fatalf("duration_ms = %+v, want 5 milliseconds", payload.Details.Checks)
	}
}

func TestRunHealthCommand_DefaultJSONProjectsDurationAsMilliseconds(t *testing.T) {
	checker := &fakeHealthChecker{report: domainhealth.HealthReport{
		Status: domainhealth.StatusOK,
		Checks: []domainhealth.CheckResult{
			{Name: "gateway_mio", Status: domainhealth.StatusOK, Message: "ok", Duration: 5 * time.Millisecond},
		},
	}}
	var out, errOut bytes.Buffer
	code := runHealthCommand(nil, checker, &out, &errOut, fixedNow)
	if code != 0 {
		t.Fatalf("expected code 0, got %d", code)
	}
	var payload struct {
		Checks []struct {
			DurationMS float64 `json:"duration_ms"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(payload.Checks) != 1 || payload.Checks[0].DurationMS != 5 {
		t.Fatalf("duration_ms = %+v, want 5 milliseconds", payload.Checks)
	}
}

func TestRunStatusCommand_DeepUsageJSON(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{Host: "127.0.0.1", Port: 18790},
	}
	checker := &fakeHealthChecker{report: domainhealth.HealthReport{
		Status: domainhealth.StatusDegraded,
		Checks: []domainhealth.CheckResult{
			{Name: "rencrow_llm_chat", Status: domainhealth.StatusDegraded, Message: "slow", Duration: 50 * time.Millisecond},
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
	checks, ok := payload.Details["checks"].([]any)
	if !ok || len(checks) != 1 {
		t.Fatalf("expected one check detail: %+v", payload.Details["checks"])
	}
	check, ok := checks[0].(map[string]any)
	if !ok || check["duration_ms"] != float64(50) {
		t.Fatalf("duration_ms = %#v, want 50 milliseconds", check["duration_ms"])
	}
}

func TestRunStatusCommand_UsageErrorText(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{Host: "127.0.0.1", Port: 18790},
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
