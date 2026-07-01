package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
)

type fakeChannelRegistry struct {
	names   []string
	results map[string]error
}

func (f *fakeChannelRegistry) List() []string {
	return f.names
}

func (f *fakeChannelRegistry) ProbeAll(_ context.Context) map[string]error {
	return f.results
}

func fixedNow() time.Time {
	return time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
}

func TestRunGatewayCommand_StatusJSON_OK(t *testing.T) {
	cfg := &config.Config{Server: config.ServerConfig{Host: "0.0.0.0", Port: 18790}}
	var out, errOut bytes.Buffer
	getStatus := func(_ string) (int, error) { return 200, nil }

	code := runGatewayCommand([]string{"status", "--json"}, cfg, &out, &errOut, getStatus, func() error { return nil }, fixedNow)
	if code != 0 {
		t.Fatalf("expected code 0, got %d (err=%s)", code, errOut.String())
	}
	var payload struct {
		OK        bool           `json:"ok"`
		Component string         `json:"component"`
		Status    string         `json:"status"`
		Details   map[string]any `json:"details"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !payload.OK || payload.Component != "gateway" || payload.Status != "running" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
	if payload.Details["url"] == "" {
		t.Fatalf("expected details.url to be set: %+v", payload.Details)
	}
}

func TestRunGatewayCommand_StatusJSON_Unreachable(t *testing.T) {
	cfg := &config.Config{Server: config.ServerConfig{Host: "127.0.0.1", Port: 18790}}
	var out, errOut bytes.Buffer
	getStatus := func(_ string) (int, error) { return 0, errors.New("dial failed") }

	code := runGatewayCommand([]string{"status", "--json"}, cfg, &out, &errOut, getStatus, func() error { return nil }, fixedNow)
	if code != 1 {
		t.Fatalf("expected code 1, got %d", code)
	}
	var payload struct {
		OK   bool   `json:"ok"`
		Code string `json:"code"`
		Hint string `json:"hint"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if payload.OK {
		t.Fatalf("expected ok=false payload")
	}
	if payload.Code != "E_GATEWAY_UNREACHABLE" {
		t.Fatalf("unexpected code: %s", payload.Code)
	}
	if payload.Hint == "" {
		t.Fatalf("expected non-empty hint")
	}
}

func TestRunChannelsCommand_ListJSON(t *testing.T) {
	reg := &fakeChannelRegistry{names: []string{"line", "telegram"}}
	var out, errOut bytes.Buffer

	code := runChannelsCommand([]string{"list", "--json"}, reg, &out, &errOut, fixedNow)
	if code != 0 {
		t.Fatalf("expected code 0, got %d (err=%s)", code, errOut.String())
	}
	var payload struct {
		OK      bool `json:"ok"`
		Details struct {
			Channels []string `json:"channels"`
		} `json:"details"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !payload.OK || len(payload.Details.Channels) != 2 {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

func TestRunChannelsCommand_ProbeJSON_Down(t *testing.T) {
	reg := &fakeChannelRegistry{
		names: []string{"line", "discord"},
		results: map[string]error{
			"line":    nil,
			"discord": errors.New("token invalid"),
		},
	}
	var out, errOut bytes.Buffer

	code := runChannelsCommand([]string{"probe", "--json"}, reg, &out, &errOut, fixedNow)
	if code != 1 {
		t.Fatalf("expected code 1, got %d", code)
	}
	var payload struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if payload.OK {
		t.Fatalf("expected ok=false payload")
	}
}
