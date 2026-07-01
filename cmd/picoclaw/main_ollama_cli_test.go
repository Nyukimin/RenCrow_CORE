package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	domainhealth "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/health"
)

func TestRunOllamaCommand_StatusJSON_Down(t *testing.T) {
	cfg := &config.Config{
		Ollama: config.OllamaConfig{BaseURL: "http://192.168.1.33:11434", Model: "Chat"},
	}
	checker := &fakeHealthChecker{report: domainhealth.HealthReport{
		Status: domainhealth.StatusDown,
		Checks: []domainhealth.CheckResult{
			{Name: "ollama_connection", Status: domainhealth.StatusDown, Message: "timeout", Duration: 2 * time.Second},
		},
	}}

	var out, errOut bytes.Buffer
	code := runOllamaCommand([]string{"status", "--json"}, cfg, checker, &out, &errOut, "ssh nyukimi@192.168.1.33", func() error { return nil }, fixedNow)
	if code != 1 {
		t.Fatalf("expected code 1, got %d", code)
	}

	var payload struct {
		OK        bool   `json:"ok"`
		Component string `json:"component"`
		Status    string `json:"status"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if payload.OK || payload.Component != "ollama" || payload.Status != "down" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

func TestRunOllamaCommand_RestartJSON_OK(t *testing.T) {
	cfg := &config.Config{
		Ollama: config.OllamaConfig{BaseURL: "http://192.168.1.33:11434", Model: "Chat"},
	}
	checker := &fakeHealthChecker{report: domainhealth.HealthReport{Status: domainhealth.StatusOK}}

	var out, errOut bytes.Buffer
	code := runOllamaCommand([]string{"restart", "--json"}, cfg, checker, &out, &errOut, "ssh nyukimi@192.168.1.33", func() error { return nil }, fixedNow)
	if code != 0 {
		t.Fatalf("expected code 0, got %d", code)
	}

	var payload struct {
		OK      bool `json:"ok"`
		Details struct {
			Target string `json:"target"`
		} `json:"details"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !payload.OK || payload.Details.Target != "ssh nyukimi@192.168.1.33" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

func TestBuildOllamaRestartAction_RemoteUsesSSH(t *testing.T) {
	t.Setenv("PICOCLAW_OLLAMA_SSH_USER", "nyukimi")
	t.Setenv("PICOCLAW_OLLAMA_RESTART_CMD", "sudo systemctl restart ollama")

	target, _, err := buildOllamaRestartAction(&config.Config{
		Ollama: config.OllamaConfig{BaseURL: "http://192.168.1.33:11434", Model: "Chat"},
	})
	if err != nil {
		t.Fatalf("buildOllamaRestartAction failed: %v", err)
	}
	if target != "ssh nyukimi@192.168.1.33" {
		t.Fatalf("unexpected target: %s", target)
	}
}

func TestBuildOllamaRestartAction_LocalUsesSystemctl(t *testing.T) {
	t.Setenv("PICOCLAW_OLLAMA_RESTART_CMD", "systemctl restart ollama")
	target, _, err := buildOllamaRestartAction(&config.Config{
		Ollama: config.OllamaConfig{BaseURL: "http://127.0.0.1:11434", Model: "Chat"},
	})
	if err != nil {
		t.Fatalf("buildOllamaRestartAction failed: %v", err)
	}
	if target != "local systemctl" {
		t.Fatalf("unexpected target: %s", target)
	}
}

func TestRunOllamaCommand_RestartJSON_Error(t *testing.T) {
	cfg := &config.Config{
		Ollama: config.OllamaConfig{BaseURL: "http://192.168.1.33:11434", Model: "Chat"},
	}
	var out, errOut bytes.Buffer
	code := runOllamaCommand(
		[]string{"restart", "--json"},
		cfg,
		&fakeHealthChecker{report: domainhealth.HealthReport{Status: domainhealth.StatusOK}},
		&out,
		&errOut,
		"ssh nyukimi@192.168.1.33",
		func() error { return errors.New("permission denied") },
		func() time.Time { return fixedNow() },
	)
	if code != 1 {
		t.Fatalf("expected code 1, got %d", code)
	}
	if !strings.Contains(out.String(), "E_OLLAMA_RESTART_FAILED") {
		t.Fatalf("expected restart failure json, got: %s", out.String())
	}
}

func TestBuildOllamaRestartAction_RemoteRequiresUserWhenEnvAndUserMissing(t *testing.T) {
	oldUser := os.Getenv("USER")
	t.Setenv("PICOCLAW_OLLAMA_SSH_USER", "")
	t.Setenv("USER", "")
	defer func() {
		_ = oldUser
	}()

	_, _, err := buildOllamaRestartAction(&config.Config{
		Ollama: config.OllamaConfig{BaseURL: "http://192.168.1.33:11434", Model: "Chat"},
	})
	if err == nil {
		t.Fatal("expected error for missing remote ssh user")
	}
}
