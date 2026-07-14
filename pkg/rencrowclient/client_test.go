package rencrowclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newNoRequestClient(t *testing.T) (*Client, *bool, func()) {
	t.Helper()
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	client, err := New(server.URL)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return client, &called, server.Close
}

func TestSuperAgentStatus(t *testing.T) {
	now := time.Date(2026, 5, 19, 15, 56, 0, 0, time.UTC)
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		if r.Method != http.MethodGet {
			t.Fatalf("method=%s", r.Method)
		}
		_ = json.NewEncoder(w).Encode(SuperAgentStatus{
			AgentRuns: []AgentRun{{RunID: "run_1", AgentType: "LeadAgent", Status: "completed", StartedAt: now.Add(-time.Minute), CompletedAt: now}},
			RuntimeConfig: SuperAgentRuntimeConfig{
				RunQueueSchedulerEnabled:     true,
				RunQueueSchedulerIntervalSec: 1,
				RunQueueSchedulerClaimLimit:  1,
			},
		})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.SuperAgentStatus(context.Background(), 5)
	if err != nil {
		t.Fatalf("SuperAgentStatus() error = %v", err)
	}
	if gotPath != "/viewer/superagent?limit=5" {
		t.Fatalf("path=%s", gotPath)
	}
	if len(status.AgentRuns) != 1 || status.AgentRuns[0].RunID != "run_1" {
		t.Fatalf("status=%#v", status)
	}
	if !status.RuntimeConfig.RunQueueSchedulerEnabled || status.RuntimeConfig.RunQueueSchedulerIntervalSec != 1 || status.RuntimeConfig.RunQueueSchedulerClaimLimit != 1 {
		t.Fatalf("runtime config=%#v", status.RuntimeConfig)
	}
}

func TestSuperAgentStatusRejectsDuplicateCurrentView(t *testing.T) {
	now := time.Date(2026, 5, 20, 3, 5, 0, 0, time.UTC)
	tests := []struct {
		name string
		resp SuperAgentStatus
		want string
	}{
		{
			name: "duplicate agent run",
			resp: SuperAgentStatus{
				AgentRuns: []AgentRun{
					{RunID: "run_1", AgentType: "LeadAgent", Status: "running", StartedAt: now},
					{RunID: "run_1", AgentType: "LeadAgent", Status: "completed", StartedAt: now, CompletedAt: now.Add(time.Minute)},
				},
			},
			want: "duplicate agent_run",
		},
		{
			name: "duplicate run queue",
			resp: SuperAgentStatus{
				RunQueue: []RunQueueItem{
					{QueueID: "rq_1", Status: "queued", CreatedAt: now},
					{QueueID: "rq_1", Status: "completed", CreatedAt: now, CompletedAt: now.Add(time.Minute)},
				},
			},
			want: "duplicate run_queue",
		},
		{
			name: "missing run id",
			resp: SuperAgentStatus{AgentRuns: []AgentRun{{AgentType: "LeadAgent", Status: "running"}}},
			want: "missing run_id",
		},
		{
			name: "missing run status",
			resp: SuperAgentStatus{AgentRuns: []AgentRun{{RunID: "run_1", AgentType: "LeadAgent"}}},
			want: "missing status",
		},
		{
			name: "terminal run missing completed_at",
			resp: SuperAgentStatus{AgentRuns: []AgentRun{{RunID: "run_1", AgentType: "LeadAgent", Status: "completed", StartedAt: now}}},
			want: "terminal agent_run",
		},
		{
			name: "run missing started at",
			resp: SuperAgentStatus{AgentRuns: []AgentRun{{RunID: "run_1", AgentType: "LeadAgent", Status: "running"}}},
			want: "missing started_at",
		},
		{
			name: "invalid run status",
			resp: SuperAgentStatus{AgentRuns: []AgentRun{{RunID: "run_1", AgentType: "LeadAgent", Status: "done"}}},
			want: "invalid agent_run status",
		},
		{
			name: "failed run missing summary",
			resp: SuperAgentStatus{AgentRuns: []AgentRun{{RunID: "run_1", AgentType: "LeadAgent", Status: "failed", StartedAt: now, CompletedAt: now.Add(time.Minute)}}},
			want: "failed agent_run",
		},
		{
			name: "duplicate subagent task",
			resp: SuperAgentStatus{SubagentTasks: []SubagentTask{
				{SubagentID: "sub_1", ParentRunID: "run_1", AgentType: "Worker", Task: "check", Scope: []string{"pkg"}, TerminationCondition: "done", Status: "completed", CreatedAt: now},
				{SubagentID: "sub_1", ParentRunID: "run_1", AgentType: "Worker", Task: "check", Scope: []string{"pkg"}, TerminationCondition: "done", Status: "completed", CreatedAt: now.Add(time.Second)},
			}},
			want: "duplicate subagent_task",
		},
		{
			name: "subagent task missing scope",
			resp: SuperAgentStatus{SubagentTasks: []SubagentTask{{SubagentID: "sub_1", ParentRunID: "run_1", AgentType: "Worker", Task: "check", TerminationCondition: "done", Status: "completed", CreatedAt: now}}},
			want: "missing scope",
		},
		{
			name: "subagent task missing created at",
			resp: SuperAgentStatus{SubagentTasks: []SubagentTask{{SubagentID: "sub_1", ParentRunID: "run_1", AgentType: "Worker", Task: "check", Scope: []string{"pkg"}, TerminationCondition: "done", Status: "completed"}}},
			want: "missing created_at",
		},
		{
			name: "context pack missing summary",
			resp: SuperAgentStatus{ContextPacks: []ContextPack{{ContextPackID: "ctx_1", RunID: "run_1", CreatedAt: now}}},
			want: "missing summary",
		},
		{
			name: "context pack negative tokens",
			resp: SuperAgentStatus{ContextPacks: []ContextPack{{ContextPackID: "ctx_1", RunID: "run_1", Summary: "summary", TokenEstimate: -1, CreatedAt: now}}},
			want: "token_estimate must be >= 0",
		},
		{
			name: "context pack missing created at",
			resp: SuperAgentStatus{ContextPacks: []ContextPack{{ContextPackID: "ctx_1", RunID: "run_1", Summary: "summary"}}},
			want: "missing created_at",
		},
		{
			name: "message channel missing status",
			resp: SuperAgentStatus{MessageChannels: []MessageChannel{{ChannelID: "chan_1", ChannelType: "superagent"}}},
			want: "message_channel",
		},
		{
			name: "message channel missing created at",
			resp: SuperAgentStatus{MessageChannels: []MessageChannel{{ChannelID: "chan_1", ChannelType: "superagent", Status: "active"}}},
			want: "missing created_at",
		},
		{
			name: "duplicate trace event",
			resp: SuperAgentStatus{TraceEvents: []TraceEvent{
				{EventID: "evt_1", EventType: "lead_agent_started", Status: "started", CreatedAt: now},
				{EventID: "evt_1", EventType: "lead_agent_completed", Status: "completed", CreatedAt: now.Add(time.Second)},
			}},
			want: "duplicate trace_event",
		},
		{
			name: "trace event missing type",
			resp: SuperAgentStatus{TraceEvents: []TraceEvent{{EventID: "evt_1", Status: "completed"}}},
			want: "missing event_type",
		},
		{
			name: "trace event missing created at",
			resp: SuperAgentStatus{TraceEvents: []TraceEvent{{EventID: "evt_1", EventType: "lead_agent_completed", Status: "completed"}}},
			want: "missing created_at",
		},
		{
			name: "missing queue id",
			resp: SuperAgentStatus{RunQueue: []RunQueueItem{{Status: "queued"}}},
			want: "missing queue_id",
		},
		{
			name: "missing queue status",
			resp: SuperAgentStatus{RunQueue: []RunQueueItem{{QueueID: "rq_1"}}},
			want: "missing status",
		},
		{
			name: "terminal queue missing completed_at",
			resp: SuperAgentStatus{RunQueue: []RunQueueItem{{QueueID: "rq_1", Status: "completed", CreatedAt: now}}},
			want: "terminal run_queue",
		},
		{
			name: "queue missing created at",
			resp: SuperAgentStatus{RunQueue: []RunQueueItem{{QueueID: "rq_1", Status: "queued"}}},
			want: "missing created_at",
		},
		{
			name: "invalid queue status",
			resp: SuperAgentStatus{RunQueue: []RunQueueItem{{QueueID: "rq_1", Status: "done"}}},
			want: "invalid run_queue status",
		},
		{
			name: "failed queue missing reason",
			resp: SuperAgentStatus{RunQueue: []RunQueueItem{{QueueID: "rq_1", Status: "failed", CreatedAt: now, CompletedAt: now.Add(time.Minute)}}},
			want: "failed run_queue",
		},
		{
			name: "scheduler enabled missing interval",
			resp: SuperAgentStatus{RuntimeConfig: SuperAgentRuntimeConfig{RunQueueSchedulerEnabled: true, RunQueueSchedulerClaimLimit: 1}},
			want: "invalid interval_sec",
		},
		{
			name: "scheduler enabled missing claim limit",
			resp: SuperAgentStatus{RuntimeConfig: SuperAgentRuntimeConfig{RunQueueSchedulerEnabled: true, RunQueueSchedulerIntervalSec: 1}},
			want: "invalid claim_limit",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/viewer/superagent" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(tt.resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.SuperAgentStatus(context.Background(), 0)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("SuperAgentStatus() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestRuntimeConfig(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		if r.Method != http.MethodGet {
			t.Fatalf("method=%s", r.Method)
		}
		_ = json.NewEncoder(w).Encode(RuntimeConfig{
			STTStreamURL:     "wss://127.0.0.1:8443/stt/stream",
			STTBaseURL:       "https://127.0.0.1:8443",
			TTSBaseURL:       "http://127.0.0.1:7860",
			TTSHealthPath:    "/gradio_api/info",
			LLMOpsConfigured: true,
			LLMOpsEnabled:    true,
			LLMOpsBaseURL:    "http://127.0.0.1:8079",
			LocalLLM: LocalLLMRuntimeConfig{
				Enabled:       true,
				Provider:      "local_openai",
				ChatBaseURL:   "http://127.0.0.1:8081",
				WorkerBaseURL: "http://127.0.0.1:8082",
				ChatModel:     "Chat",
				WorkerModel:   "Worker",
			},
			RuntimeReadiness: fullRuntimeReadinessWithConfig(false, true, true),
		})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := client.RuntimeConfig(context.Background())
	if err != nil {
		t.Fatalf("RuntimeConfig() error = %v", err)
	}
	if gotPath != "/viewer/runtime-config" {
		t.Fatalf("path=%s", gotPath)
	}
	if !cfg.LLMOpsEnabled || cfg.LocalLLM.ChatBaseURL != "http://127.0.0.1:8081" || cfg.TTSHealthPath != "/gradio_api/info" {
		t.Fatalf("runtime config=%#v", cfg)
	}
	if cfg.RuntimeReadiness.SlackCredentialsPresent == nil || *cfg.RuntimeReadiness.SlackCredentialsPresent {
		t.Fatalf("runtime readiness=%#v", cfg.RuntimeReadiness)
	}
	if cfg.RuntimeReadiness.SourceRegistryAvailable == nil || !*cfg.RuntimeReadiness.SourceRegistryAvailable {
		t.Fatalf("runtime readiness should include source registry availability: %#v", cfg.RuntimeReadiness)
	}
	if cfg.RuntimeReadiness.DomainGraphAvailable == nil || !*cfg.RuntimeReadiness.DomainGraphAvailable || cfg.RuntimeReadiness.DomainGraphStatus == nil || !*cfg.RuntimeReadiness.DomainGraphStatus {
		t.Fatalf("runtime readiness should include domain graph availability: %#v", cfg.RuntimeReadiness)
	}
}

func TestDebugSystemSnapshot(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		if r.Method != http.MethodGet {
			t.Fatalf("method=%s", r.Method)
		}
		_ = json.NewEncoder(w).Encode(DebugSystemSnapshot{
			UpdatedAt: "2026-05-19T17:55:00Z",
			Audio: DebugAudioSnapshot{
				STTBaseURL: "http://127.0.0.1:8766",
				TTSBaseURL: "http://127.0.0.1:7860",
				LastError:  "stt:context deadline exceeded",
			},
		})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := client.DebugSystemSnapshot(context.Background())
	if err != nil {
		t.Fatalf("DebugSystemSnapshot() error = %v", err)
	}
	if gotPath != "/viewer/debug/system" {
		t.Fatalf("path=%s", gotPath)
	}
	if snapshot.UpdatedAt == "" || snapshot.Audio.LastError == "" {
		t.Fatalf("debug system snapshot=%#v", snapshot)
	}
}

func TestDebugSystemSnapshotRejectsMalformedResponse(t *testing.T) {
	tests := []struct {
		name string
		resp DebugSystemSnapshot
		want string
	}{
		{name: "missing updated_at", resp: DebugSystemSnapshot{}, want: "missing updated_at"},
		{name: "invalid updated_at", resp: DebugSystemSnapshot{UpdatedAt: "not-a-time"}, want: "invalid updated_at"},
		{name: "invalid stt base", resp: DebugSystemSnapshot{UpdatedAt: "2026-05-19T17:55:00Z", Audio: DebugAudioSnapshot{STTBaseURL: "127.0.0.1:8766"}}, want: "debug audio stt_base_url"},
		{name: "stt ok without base", resp: DebugSystemSnapshot{UpdatedAt: "2026-05-19T17:55:00Z", Audio: DebugAudioSnapshot{STTOK: true}}, want: "stt_ok without stt_base_url"},
		{name: "stt down without evidence", resp: DebugSystemSnapshot{UpdatedAt: "2026-05-19T17:55:00Z", Audio: DebugAudioSnapshot{STTBaseURL: "http://127.0.0.1:8766"}}, want: "stt down without health or error evidence"},
		{name: "tts ok without base", resp: DebugSystemSnapshot{UpdatedAt: "2026-05-19T17:55:00Z", Audio: DebugAudioSnapshot{TTSLiveOK: true}}, want: "tts ok without tts_base_url"},
		{name: "tts down without evidence", resp: DebugSystemSnapshot{UpdatedAt: "2026-05-19T17:55:00Z", Audio: DebugAudioSnapshot{TTSBaseURL: "http://127.0.0.1:7860"}}, want: "tts down without live/ready or error evidence"},
		{name: "tts ready without live", resp: DebugSystemSnapshot{UpdatedAt: "2026-05-19T17:55:00Z", Audio: DebugAudioSnapshot{TTSBaseURL: "http://127.0.0.1:7860", TTSReadyOK: true, TTSReady: "ok"}}, want: "tts_ready_ok without tts_live_ok"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(tt.resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.DebugSystemSnapshot(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("DebugSystemSnapshot() error=%v, want %q", err, tt.want)
			}
		})
	}
}

func TestRuntimeHealth(t *testing.T) {
	now := "2026-05-19T19:57:00Z"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/health" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(RuntimeHealthReport{
			Status: "down",
			Checks: []RuntimeHealthCheck{
				{Name: "local_llm_chat", Status: "down", Message: "connection refused", DurationMS: 7067},
				{Name: "local_llm_worker", Status: "down", Message: "connection refused", DurationMS: 7071},
			},
			Timestamp: now,
		})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	report, err := client.RuntimeHealth(context.Background())
	if err != nil {
		t.Fatalf("RuntimeHealth() error = %v", err)
	}
	if report.Status != "down" || len(report.Checks) != 2 || report.Checks[0].Message == "" {
		t.Fatalf("health report=%#v", report)
	}
}

func TestRuntimeHealthRejectsMalformedResponse(t *testing.T) {
	valid := RuntimeHealthReport{
		Status: "down",
		Checks: []RuntimeHealthCheck{
			{Name: "local_llm_chat", Status: "down", Message: "connection refused", DurationMS: 7067},
		},
		Timestamp: "2026-05-19T19:57:00Z",
	}
	tests := []struct {
		name       string
		httpStatus int
		mutate     func(*RuntimeHealthReport)
		want       string
	}{
		{name: "down with http ok", httpStatus: http.StatusOK, want: "http status"},
		{name: "ok with http unavailable", httpStatus: http.StatusServiceUnavailable, mutate: func(r *RuntimeHealthReport) {
			r.Status = "ok"
			r.Checks = []RuntimeHealthCheck{{Name: "local_llm_chat", Status: "ok", Message: "reachable", DurationMS: 12}}
		}, want: "http status"},
		{name: "down without checks", httpStatus: http.StatusServiceUnavailable, mutate: func(r *RuntimeHealthReport) {
			r.Checks = nil
		}, want: "missing checks"},
		{name: "down check without message", httpStatus: http.StatusServiceUnavailable, mutate: func(r *RuntimeHealthReport) {
			r.Checks[0].Message = ""
		}, want: "missing message"},
		{name: "invalid check status", httpStatus: http.StatusServiceUnavailable, mutate: func(r *RuntimeHealthReport) {
			r.Checks[0].Status = "failed"
		}, want: "invalid check status"},
		{name: "overall mismatch", httpStatus: http.StatusOK, mutate: func(r *RuntimeHealthReport) {
			r.Status = "degraded"
			r.Checks[0].Status = "down"
		}, want: "overall status"},
		{name: "invalid timestamp", httpStatus: http.StatusServiceUnavailable, mutate: func(r *RuntimeHealthReport) {
			r.Timestamp = "now"
		}, want: "timestamp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := valid
			resp.Checks = append([]RuntimeHealthCheck(nil), valid.Checks...)
			if tt.mutate != nil {
				tt.mutate(&resp)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/health" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(tt.httpStatus)
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.RuntimeHealth(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("RuntimeHealth() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLLMOpsStatus(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Method != http.MethodGet || r.URL.Path != "/viewer/llm-ops/status" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(LLMOpsStatus{
			Roles: map[string]LLMOpsRoleState{
				"Chat":   {HealthOK: boolPtr(true)},
				"Worker": {HealthOK: boolPtr(true)},
			},
			Memory: LLMOpsMemoryStatus{LLMByRole: map[string]LLMOpsMemoryRole{
				"Chat": {Role: "Chat", Model: "chat", Port: 8081, PID: intPtr(1234), RSSMiB: 512.5},
			}},
		})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.LLMOpsStatus(context.Background())
	if err != nil {
		t.Fatalf("LLMOpsStatus() error = %v", err)
	}
	if gotPath != "/viewer/llm-ops/status" || len(status.Roles) != 2 {
		t.Fatalf("status=%#v path=%s", status, gotPath)
	}
}

func TestLLMOpsStatusUnavailableKeepsAPIErrorEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/viewer/llm-ops/status" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.LLMOpsStatus(context.Background())
	var apiErr *APIError
	if err == nil || !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadGateway || !strings.Contains(apiErr.Body, "upstream unreachable") {
		t.Fatalf("LLMOpsStatus() error = %#v, want APIError 502 with body", err)
	}
}

func TestLLMOpsHealth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/viewer/llm-ops/health" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(LLMOpsHealth{Status: "ok", Daemon: "llm-mgmt"})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	health, err := client.LLMOpsHealth(context.Background())
	if err != nil {
		t.Fatalf("LLMOpsHealth() error = %v", err)
	}
	if health.Status != "ok" || health.Daemon != "llm-mgmt" {
		t.Fatalf("health=%#v", health)
	}
}

func TestLLMOpsHealthUnavailableKeepsAPIErrorEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/viewer/llm-ops/health" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.LLMOpsHealth(context.Background())
	var apiErr *APIError
	if err == nil || !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadGateway || !strings.Contains(apiErr.Body, "upstream unreachable") {
		t.Fatalf("LLMOpsHealth() error = %#v, want APIError 502 with body", err)
	}
}

func TestLLMOpsHealthRejectsMalformedResponse(t *testing.T) {
	tests := []struct {
		name string
		resp LLMOpsHealth
		want string
	}{
		{name: "missing status", resp: LLMOpsHealth{Daemon: "llm-mgmt"}, want: "missing status"},
		{name: "invalid status", resp: LLMOpsHealth{Status: "up", Daemon: "llm-mgmt"}, want: "invalid status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/viewer/llm-ops/health" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(tt.resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.LLMOpsHealth(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("LLMOpsHealth() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLLMOpsControl(t *testing.T) {
	tests := []struct {
		name     string
		call     func(context.Context, *Client) error
		wantPath string
		wantBody string
	}{
		{
			name:     "stop",
			call:     func(ctx context.Context, c *Client) error { return c.StopLLMOps(ctx, []string{"Worker", "Wild"}) },
			wantPath: "/viewer/llm-ops/stop",
			wantBody: `{"roles":["Worker","Wild"]}`,
		},
		{
			name:     "start",
			call:     func(ctx context.Context, c *Client) error { return c.StartLLMOps(ctx, "Heavy") },
			wantPath: "/viewer/llm-ops/start",
			wantBody: `{"selection":"Heavy"}`,
		},
		{
			name:     "restart",
			call:     func(ctx context.Context, c *Client) error { return c.RestartLLMOps(ctx, "all") },
			wantPath: "/viewer/llm-ops/restart",
			wantBody: `{"selection":"all"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath string
			var gotBody string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				if r.Method != http.MethodPost {
					t.Fatalf("unexpected method: %s", r.Method)
				}
				body, _ := io.ReadAll(r.Body)
				gotBody = string(body)
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			if err := tt.call(context.Background(), client); err != nil {
				t.Fatalf("control call error = %v", err)
			}
			if gotPath != tt.wantPath || gotBody != tt.wantBody {
				t.Fatalf("request path=%s body=%s, want path=%s body=%s", gotPath, gotBody, tt.wantPath, tt.wantBody)
			}
		})
	}
}

func TestLLMOpsControlUnavailableKeepsAPIErrorEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	err = client.RestartLLMOps(context.Background(), "all")
	var apiErr *APIError
	if err == nil || !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadGateway || !strings.Contains(apiErr.Body, "upstream unreachable") {
		t.Fatalf("RestartLLMOps() error = %#v, want APIError 502 with body", err)
	}
}

func TestLLMOpsControlRejectsInvalidRequest(t *testing.T) {
	client, called, cleanup := newNoRequestClient(t)
	defer cleanup()
	tests := []struct {
		name string
		call func(context.Context, *Client) error
		want string
	}{
		{name: "stop missing roles", call: func(ctx context.Context, c *Client) error {
			return c.StopLLMOps(ctx, nil)
		}, want: "roles are required"},
		{name: "stop unknown role", call: func(ctx context.Context, c *Client) error {
			return c.StopLLMOps(ctx, []string{"Coder"})
		}, want: "invalid role"},
		{name: "start missing selection", call: func(ctx context.Context, c *Client) error {
			return c.StartLLMOps(ctx, "")
		}, want: "selection is required"},
		{name: "restart unknown selection", call: func(ctx context.Context, c *Client) error {
			return c.RestartLLMOps(ctx, "Coder")
		}, want: "invalid selection"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call(context.Background(), client)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("control error = %v, want %q", err, tt.want)
			}
			if *called {
				t.Fatalf("control sent request for invalid payload")
			}
		})
	}
}

func TestLLMOpsStatusRejectsMalformedResponse(t *testing.T) {
	valid := LLMOpsStatus{
		Roles: map[string]LLMOpsRoleState{
			"Chat": {HealthOK: boolPtr(true)},
		},
		Memory: LLMOpsMemoryStatus{LLMByRole: map[string]LLMOpsMemoryRole{
			"Chat": {Role: "Chat", PID: intPtr(1234)},
		}},
	}
	tests := []struct {
		name   string
		mutate func(*LLMOpsStatus)
		want   string
	}{
		{name: "missing roles", mutate: func(s *LLMOpsStatus) {
			s.Roles = nil
		}, want: "missing roles"},
		{name: "unknown role", mutate: func(s *LLMOpsStatus) {
			s.Roles = map[string]LLMOpsRoleState{"Coder": {HealthOK: boolPtr(true)}}
		}, want: "unknown role"},
		{name: "missing health ok", mutate: func(s *LLMOpsStatus) {
			s.Roles["Chat"] = LLMOpsRoleState{}
		}, want: "missing health_ok"},
		{name: "halted and healthy", mutate: func(s *LLMOpsStatus) {
			s.Roles["Chat"] = LLMOpsRoleState{HealthOK: boolPtr(true), Halted: boolPtr(true)}
		}, want: "halted but health_ok"},
		{name: "halted with pid", mutate: func(s *LLMOpsStatus) {
			s.Roles["Chat"] = LLMOpsRoleState{HealthOK: boolPtr(false), Halted: boolPtr(true)}
			s.Memory.LLMByRole["Chat"] = LLMOpsMemoryRole{Role: "Chat", PID: intPtr(1234)}
		}, want: "halted but pid"},
		{name: "memory role without role state", mutate: func(s *LLMOpsStatus) {
			s.Memory.LLMByRole["Worker"] = LLMOpsMemoryRole{Role: "Worker", PID: intPtr(1234)}
		}, want: "memory role \"Worker\" missing role state"},
		{name: "memory mismatched role", mutate: func(s *LLMOpsStatus) {
			s.Memory.LLMByRole["Chat"] = LLMOpsMemoryRole{Role: "Worker", PID: intPtr(1234)}
		}, want: "mismatched role"},
		{name: "memory negative port", mutate: func(s *LLMOpsStatus) {
			s.Memory.LLMByRole["Chat"] = LLMOpsMemoryRole{Role: "Chat", Port: -1}
		}, want: "negative port"},
		{name: "memory negative rss", mutate: func(s *LLMOpsStatus) {
			s.Memory.LLMByRole["Chat"] = LLMOpsMemoryRole{Role: "Chat", RSSMiB: -1}
		}, want: "negative rss_mib"},
		{name: "memory negative pid", mutate: func(s *LLMOpsStatus) {
			s.Memory.LLMByRole["Chat"] = LLMOpsMemoryRole{Role: "Chat", PID: intPtr(-1)}
		}, want: "negative pid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := valid
			resp.Roles = map[string]LLMOpsRoleState{}
			for k, v := range valid.Roles {
				resp.Roles[k] = v
			}
			resp.Memory.LLMByRole = map[string]LLMOpsMemoryRole{}
			for k, v := range valid.Memory.LLMByRole {
				resp.Memory.LLMByRole[k] = v
			}
			tt.mutate(&resp)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/viewer/llm-ops/status" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.LLMOpsStatus(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("LLMOpsStatus() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestRuntimeConfigRejectsMalformedResponse(t *testing.T) {
	valid := RuntimeConfig{
		STTStreamURL: "wss://127.0.0.1:8443/stt/stream",
		STTBaseURL:   "https://127.0.0.1:8443",
		TTSBaseURL:   "http://127.0.0.1:7860",
		LocalLLM: LocalLLMRuntimeConfig{
			Enabled:       true,
			Provider:      "local_openai",
			ChatBaseURL:   "http://127.0.0.1:8081",
			WorkerBaseURL: "http://127.0.0.1:8082",
		},
		RuntimeReadiness: fullRuntimeReadinessWithConfig(false, true, true),
	}
	tests := []struct {
		name   string
		mutate func(*RuntimeConfig)
		want   string
	}{
		{name: "enabled llm ops without configured", mutate: func(c *RuntimeConfig) {
			c.LLMOpsEnabled = true
			c.LLMOpsConfigured = false
			c.LLMOpsBaseURL = "http://127.0.0.1:8079"
		}, want: "llm_ops_enabled without llm_ops_configured"},
		{name: "enabled llm ops without base", mutate: func(c *RuntimeConfig) {
			c.LLMOpsEnabled = true
			c.LLMOpsConfigured = true
			c.LLMOpsBaseURL = ""
		}, want: "llm_ops_enabled without llm_ops_base_url"},
		{name: "local llm missing provider", mutate: func(c *RuntimeConfig) {
			c.LocalLLM.Provider = ""
		}, want: "missing provider"},
		{name: "local llm missing chat base", mutate: func(c *RuntimeConfig) {
			c.LocalLLM.ChatBaseURL = ""
		}, want: "missing chat_base_url"},
		{name: "invalid stt stream", mutate: func(c *RuntimeConfig) {
			c.STTStreamURL = "http://127.0.0.1:8443/stt/stream"
		}, want: "invalid stt_stream_url"},
		{name: "invalid tts base", mutate: func(c *RuntimeConfig) {
			c.TTSBaseURL = "127.0.0.1:7860"
		}, want: "invalid tts_base_url"},
		{name: "relative tts health path", mutate: func(c *RuntimeConfig) {
			c.TTSHealthPath = "health"
		}, want: "tts_health_path"},
		{name: "missing runtime readiness", mutate: func(c *RuntimeConfig) {
			c.RuntimeReadiness.SlackCredentialsPresent = nil
		}, want: "missing runtime_readiness"},
		{name: "missing channel file payload readiness", mutate: func(c *RuntimeConfig) {
			c.RuntimeReadiness.SlackFilePayloadPipeline = nil
		}, want: "missing runtime_readiness"},
		{name: "stt config presence mismatch", mutate: func(c *RuntimeConfig) {
			*c.RuntimeReadiness.STTGatewayConfigPresent = false
		}, want: "stt_gateway_config_present"},
		{name: "tts config presence mismatch", mutate: func(c *RuntimeConfig) {
			*c.RuntimeReadiness.TTSProviderConfigPresent = false
		}, want: "tts_provider_config_present"},
		{name: "source registry without l1", mutate: func(c *RuntimeConfig) {
			*c.RuntimeReadiness.L1SQLiteConfigPresent = false
			*c.RuntimeReadiness.SourceRegistryAvailable = true
		}, want: "source_registry_available"},
		{name: "missing source route readiness", mutate: func(c *RuntimeConfig) {
			c.RuntimeReadiness.SourceRegistryStatus = nil
		}, want: "missing runtime_readiness"},
		{name: "source registry available without status route", mutate: func(c *RuntimeConfig) {
			*c.RuntimeReadiness.SourceRegistryAvailable = true
			*c.RuntimeReadiness.SourceRegistryStatus = false
		}, want: "source_registry_available"},
		{name: "missing domain graph readiness", mutate: func(c *RuntimeConfig) {
			c.RuntimeReadiness.DomainGraphAvailable = nil
		}, want: "missing runtime_readiness"},
		{name: "domain graph available without status route", mutate: func(c *RuntimeConfig) {
			*c.RuntimeReadiness.DomainGraphAvailable = true
			*c.RuntimeReadiness.DomainGraphStatus = false
		}, want: "domain_graph_available"},
		{name: "missing memory layers readiness", mutate: func(c *RuntimeConfig) {
			c.RuntimeReadiness.MemoryLayersAvailable = nil
		}, want: "missing runtime_readiness"},
		{name: "memory layers available without l1", mutate: func(c *RuntimeConfig) {
			*c.RuntimeReadiness.L1SQLiteConfigPresent = false
			*c.RuntimeReadiness.MemoryLayersAvailable = true
			*c.RuntimeReadiness.SourceRegistryAvailable = false
		}, want: "memory_layers_available"},
		{name: "memory layers available without status route", mutate: func(c *RuntimeConfig) {
			*c.RuntimeReadiness.MemoryLayersAvailable = true
			*c.RuntimeReadiness.MemoryLayersStatus = false
		}, want: "memory_layers_available"},
		{name: "missing knowledge memory readiness", mutate: func(c *RuntimeConfig) {
			c.RuntimeReadiness.KnowledgeMemoryEnabled = nil
		}, want: "missing runtime_readiness"},
		{name: "knowledge memory enabled without status route", mutate: func(c *RuntimeConfig) {
			*c.RuntimeReadiness.KnowledgeMemoryEnabled = true
			*c.RuntimeReadiness.KnowledgeMemoryStatus = false
		}, want: "knowledge_memory_enabled"},
		{name: "missing browser trace readiness", mutate: func(c *RuntimeConfig) {
			c.RuntimeReadiness.BrowserTraceAPIEnabled = nil
		}, want: "missing runtime_readiness"},
		{name: "browser trace enabled without status route", mutate: func(c *RuntimeConfig) {
			*c.RuntimeReadiness.BrowserTraceAPIEnabled = true
			*c.RuntimeReadiness.BrowserTraceAPIStatus = false
		}, want: "browser_trace_api_enabled"},
		{name: "browser trace fetcher without status route", mutate: func(c *RuntimeConfig) {
			*c.RuntimeReadiness.BrowserTraceAPIFetcher = true
			*c.RuntimeReadiness.BrowserTraceAPIEnabled = false
			*c.RuntimeReadiness.BrowserTraceAPIStatus = false
		}, want: "browser_trace_api_fetcher_available"},
		{name: "missing sandbox readiness", mutate: func(c *RuntimeConfig) {
			c.RuntimeReadiness.SandboxEnabled = nil
		}, want: "missing runtime_readiness"},
		{name: "sandbox enabled without status route", mutate: func(c *RuntimeConfig) {
			*c.RuntimeReadiness.SandboxEnabled = true
			*c.RuntimeReadiness.SandboxStatusAvailable = false
		}, want: "sandbox_enabled"},
		{name: "missing distributed readiness", mutate: func(c *RuntimeConfig) {
			c.RuntimeReadiness.DistributedEnabled = nil
		}, want: "missing runtime_readiness"},
		{name: "slack file pipeline without webhook", mutate: func(c *RuntimeConfig) {
			*c.RuntimeReadiness.SlackWebhookRegistered = false
			*c.RuntimeReadiness.SlackFilePayloadPipeline = true
		}, want: "slack_file_payload_pipeline"},
		{name: "discord file pipeline without webhook", mutate: func(c *RuntimeConfig) {
			*c.RuntimeReadiness.DiscordWebhookRegistered = false
			*c.RuntimeReadiness.DiscordFilePayloadPipeline = true
		}, want: "discord_file_payload_pipeline"},
		{name: "telegram file pipeline without webhook", mutate: func(c *RuntimeConfig) {
			*c.RuntimeReadiness.TelegramWebhookRegistered = false
			*c.RuntimeReadiness.TelegramFilePayloadPipeline = true
		}, want: "telegram_file_payload_pipeline"},
		{name: "ssh connected without distributed enabled", mutate: func(c *RuntimeConfig) {
			*c.RuntimeReadiness.DistributedEnabled = false
			*c.RuntimeReadiness.DistributedSSHConfigured = true
			*c.RuntimeReadiness.DistributedSSHConnected = true
		}, want: "distributed_ssh_connected"},
		{name: "local transport without distributed enabled", mutate: func(c *RuntimeConfig) {
			*c.RuntimeReadiness.DistributedEnabled = false
			*c.RuntimeReadiness.DistributedLocalTransport = true
		}, want: "distributed_local_transport"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := valid
			resp.RuntimeReadiness = fullRuntimeReadinessWithConfig(false, true, true)
			tt.mutate(&resp)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/viewer/runtime-config" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.RuntimeConfig(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("RuntimeConfig() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestCreateAgentRun(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/viewer/superagent/runs" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var item AgentRun
		if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
			t.Fatal(err)
		}
		if item.RunID != "run_1" || item.AgentType != "LeadAgent" {
			t.Fatalf("payload=%#v", item)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	err = client.CreateAgentRun(context.Background(), AgentRun{
		RunID:     "run_1",
		AgentType: "LeadAgent",
		Status:    "running",
		StartedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("CreateAgentRun() error = %v", err)
	}
}

func TestCreateAgentRunRejectsInvalidRequest(t *testing.T) {
	tests := []struct {
		name string
		item AgentRun
		want string
	}{
		{
			name: "missing run id",
			item: AgentRun{AgentType: "LeadAgent", Status: "running"},
			want: "missing run_id",
		},
		{
			name: "missing agent type",
			item: AgentRun{RunID: "run_1", Status: "running"},
			want: "missing agent_type",
		},
		{
			name: "missing status",
			item: AgentRun{RunID: "run_1", AgentType: "LeadAgent"},
			want: "missing status",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			err = client.CreateAgentRun(context.Background(), tt.item)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("CreateAgentRun() error = %v, want %q", err, tt.want)
			}
			if called {
				t.Fatal("server was called for invalid request")
			}
		})
	}
}

func TestCreateTraceEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/viewer/superagent/trace-events" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var item TraceEvent
		if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
			t.Fatal(err)
		}
		if item.EventID != "event_1" || item.EventType != "run_started" || item.Status != "ok" {
			t.Fatalf("payload=%#v", item)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	err = client.CreateTraceEvent(context.Background(), TraceEvent{
		EventID:   "event_1",
		RunID:     "run_1",
		EventType: "run_started",
		Status:    "ok",
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("CreateTraceEvent() error = %v", err)
	}
}

func TestCreateTraceEventRejectsInvalidRequest(t *testing.T) {
	tests := []struct {
		name string
		item TraceEvent
		want string
	}{
		{
			name: "missing event id",
			item: TraceEvent{EventType: "run_started", Status: "ok"},
			want: "missing event_id",
		},
		{
			name: "missing event type",
			item: TraceEvent{EventID: "event_1", Status: "ok"},
			want: "missing event_type",
		},
		{
			name: "missing status",
			item: TraceEvent{EventID: "event_1", EventType: "run_started"},
			want: "missing status",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			err = client.CreateTraceEvent(context.Background(), tt.item)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("CreateTraceEvent() error = %v, want %q", err, tt.want)
			}
			if called {
				t.Fatal("server was called for invalid request")
			}
		})
	}
}

func TestRunQueueClientFlow(t *testing.T) {
	var paths []string
	now := time.Date(2026, 5, 20, 5, 40, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/viewer/superagent/run-queue":
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected method: %s", r.Method)
			}
			var item RunQueueItem
			if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
				t.Fatal(err)
			}
			if item.QueueID != "rq_1" || item.Action != "resume" {
				t.Fatalf("payload=%#v", item)
			}
			w.WriteHeader(http.StatusOK)
		case "/viewer/superagent/run-queue/claim":
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected method: %s", r.Method)
			}
			_ = json.NewEncoder(w).Encode(RunQueueClaimResponse{
				Claimed: true,
				Item:    RunQueueItem{QueueID: "rq_1", Status: "claimed", CreatedAt: now},
			})
		case "/viewer/superagent/run-queue/complete":
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected method: %s", r.Method)
			}
			var req RunQueueCompleteRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			if req.QueueID != "rq_1" || req.Status != "completed" {
				t.Fatalf("payload=%#v", req)
			}
			_ = json.NewEncoder(w).Encode(RunQueueCompleteResponse{
				Completed: true,
				Item:      RunQueueItem{QueueID: "rq_1", Status: "completed", CreatedAt: now, CompletedAt: now.Add(time.Minute)},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CreateRunQueueItem(context.Background(), RunQueueItem{
		QueueID:   "rq_1",
		RunID:     "run_1",
		Goal:      "manual ledger test",
		Action:    "resume",
		Status:    "queued",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateRunQueueItem() error = %v", err)
	}
	claim, err := client.ClaimRunQueueItem(context.Background())
	if err != nil {
		t.Fatalf("ClaimRunQueueItem() error = %v", err)
	}
	if !claim.Claimed || claim.Item.Status != "claimed" {
		t.Fatalf("claim=%#v", claim)
	}
	complete, err := client.CompleteRunQueueItem(context.Background(), RunQueueCompleteRequest{
		QueueID: "rq_1",
		Status:  "completed",
		Reason:  "done",
	})
	if err != nil {
		t.Fatalf("CompleteRunQueueItem() error = %v", err)
	}
	if !complete.Completed || complete.Item.Status != "completed" {
		t.Fatalf("complete=%#v", complete)
	}
	if len(paths) != 3 {
		t.Fatalf("paths=%#v", paths)
	}
}

func TestCreateRunQueueItemDefaultsQueuedStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/viewer/superagent/run-queue" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var item RunQueueItem
		if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
			t.Fatal(err)
		}
		if item.QueueID != "rq_1" || item.Status != "queued" {
			t.Fatalf("payload=%#v", item)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CreateRunQueueItem(context.Background(), RunQueueItem{
		QueueID: "rq_1",
		RunID:   "run_1",
		Goal:    "manual ledger test",
		Action:  "resume",
	}); err != nil {
		t.Fatalf("CreateRunQueueItem() error = %v", err)
	}
}

func TestCreateRunQueueItemRejectsInvalidRequest(t *testing.T) {
	tests := []struct {
		name string
		item RunQueueItem
		want string
	}{
		{
			name: "missing queue id",
			item: RunQueueItem{Goal: "manual ledger test", Action: "resume", Status: "queued"},
			want: "missing queue_id",
		},
		{
			name: "missing goal",
			item: RunQueueItem{QueueID: "rq_1", Action: "resume", Status: "queued"},
			want: "missing goal",
		},
		{
			name: "missing action",
			item: RunQueueItem{QueueID: "rq_1", Goal: "manual ledger test", Status: "queued"},
			want: "missing action",
		},
		{
			name: "non queued status",
			item: RunQueueItem{QueueID: "rq_1", Goal: "manual ledger test", Action: "resume", Status: "claimed"},
			want: "status must be queued",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			err = client.CreateRunQueueItem(context.Background(), tt.item)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("CreateRunQueueItem() error = %v, want %q", err, tt.want)
			}
			if called {
				t.Fatal("server was called for invalid request")
			}
		})
	}
}

func TestCompleteRunQueueItemDefaultsCompletedStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/viewer/superagent/run-queue/complete" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req RunQueueCompleteRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.QueueID != "rq_1" || req.Status != "completed" {
			t.Fatalf("payload=%#v", req)
		}
		_ = json.NewEncoder(w).Encode(RunQueueCompleteResponse{
			Completed: true,
			Item:      RunQueueItem{QueueID: "rq_1", Status: "completed", CreatedAt: time.Date(2026, 5, 20, 5, 40, 0, 0, time.UTC), CompletedAt: time.Date(2026, 5, 20, 5, 41, 0, 0, time.UTC)},
		})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.CompleteRunQueueItem(context.Background(), RunQueueCompleteRequest{
		QueueID: "rq_1",
	})
	if err != nil {
		t.Fatalf("CompleteRunQueueItem() error = %v", err)
	}
	if !resp.Completed || resp.Item.Status != "completed" {
		t.Fatalf("response=%#v", resp)
	}
}

func TestCompleteRunQueueItemRejectsInvalidRequest(t *testing.T) {
	tests := []struct {
		name string
		req  RunQueueCompleteRequest
		want string
	}{
		{
			name: "missing queue id",
			req:  RunQueueCompleteRequest{Status: "completed"},
			want: "missing queue_id",
		},
		{
			name: "invalid terminal status",
			req:  RunQueueCompleteRequest{QueueID: "rq_1", Status: "claimed"},
			want: "status must be completed, failed, or cancelled",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, called, cleanup := newNoRequestClient(t)
			defer cleanup()
			_, err := client.CompleteRunQueueItem(context.Background(), tt.req)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("CompleteRunQueueItem() error = %v, want %q", err, tt.want)
			}
			if *called {
				t.Fatalf("CompleteRunQueueItem() sent request for invalid payload")
			}
		})
	}
}

func TestCompleteRunQueueItemRejectsMalformedResponse(t *testing.T) {
	now := time.Date(2026, 5, 20, 5, 40, 0, 0, time.UTC)
	tests := []struct {
		name string
		resp RunQueueCompleteResponse
		want string
	}{
		{
			name: "wrong status",
			resp: RunQueueCompleteResponse{Completed: true, Item: RunQueueItem{QueueID: "rq_1", Status: "claimed", CreatedAt: now}},
			want: "status mismatch",
		},
		{
			name: "missing created at",
			resp: RunQueueCompleteResponse{Completed: true, Item: RunQueueItem{QueueID: "rq_1", Status: "completed", CompletedAt: now.Add(time.Minute)}},
			want: "missing created_at",
		},
		{
			name: "missing completed at",
			resp: RunQueueCompleteResponse{Completed: true, Item: RunQueueItem{QueueID: "rq_1", Status: "completed", CreatedAt: now}},
			want: "missing completed_at",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/viewer/superagent/run-queue/complete" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(tt.resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.CompleteRunQueueItem(context.Background(), RunQueueCompleteRequest{
				QueueID: "rq_1",
				Status:  "completed",
				Reason:  "done",
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("CompleteRunQueueItem() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestClaimRunQueueItemRejectsMalformedResponse(t *testing.T) {
	tests := []struct {
		name string
		resp RunQueueClaimResponse
		want string
	}{
		{
			name: "claimed without queue id",
			resp: RunQueueClaimResponse{Claimed: true, Item: RunQueueItem{Status: "claimed"}},
			want: "without queue_id",
		},
		{
			name: "claimed with wrong status",
			resp: RunQueueClaimResponse{Claimed: true, Item: RunQueueItem{QueueID: "rq_1", Status: "queued"}},
			want: "status mismatch",
		},
		{
			name: "claimed missing created at",
			resp: RunQueueClaimResponse{Claimed: true, Item: RunQueueItem{QueueID: "rq_1", Status: "claimed"}},
			want: "missing created_at",
		},
		{
			name: "not claimed with claimed item state",
			resp: RunQueueClaimResponse{Claimed: false, Item: RunQueueItem{QueueID: "rq_1", Status: "claimed"}},
			want: "not claimed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/viewer/superagent/run-queue/claim" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(tt.resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.ClaimRunQueueItem(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ClaimRunQueueItem() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestCheckExternalControl(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/viewer/ai-workflow/external-control/check" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req ExternalControlRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.Actor != "Worker" || req.ChannelID != "viewer" || req.Action != "promotion_apply" || req.HumanApproved {
			t.Fatalf("payload=%#v", req)
		}
		_ = json.NewEncoder(w).Encode(ExternalControlResponse{
			Request: req,
			Decision: ExternalControlDecision{
				Status:           "needs_approval",
				RequiresApproval: true,
				Reasons:          []string{"human approval is required for action"},
			},
		})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.CheckExternalControl(context.Background(), ExternalControlRequest{
		Actor:     "Worker",
		ChannelID: "viewer",
		Action:    "promotion_apply",
	})
	if err != nil {
		t.Fatalf("CheckExternalControl() error = %v", err)
	}
	if resp.Decision.Status != "needs_approval" || !resp.Decision.RequiresApproval {
		t.Fatalf("response=%#v", resp)
	}
}

func TestCheckExternalControlRejectsInvalidRequest(t *testing.T) {
	tests := []struct {
		name string
		req  ExternalControlRequest
		want string
	}{
		{name: "missing actor", req: ExternalControlRequest{ChannelID: "viewer", Action: "promotion_apply"}, want: "missing actor"},
		{name: "missing channel", req: ExternalControlRequest{Actor: "Worker", Action: "promotion_apply"}, want: "missing channel_id"},
		{name: "missing action", req: ExternalControlRequest{Actor: "Worker", ChannelID: "viewer"}, want: "missing action"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, called, cleanup := newNoRequestClient(t)
			defer cleanup()
			_, err := client.CheckExternalControl(context.Background(), tt.req)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("CheckExternalControl() error = %v, want %q", err, tt.want)
			}
			if *called {
				t.Fatal("server was called for invalid request")
			}
		})
	}
}

func TestCheckExternalControlRejectsMalformedResponse(t *testing.T) {
	tests := []struct {
		name string
		resp ExternalControlResponse
		want string
	}{
		{
			name: "request mismatch",
			resp: ExternalControlResponse{
				Request:  ExternalControlRequest{Actor: "Coder", ChannelID: "viewer", Action: "promotion_apply"},
				Decision: ExternalControlDecision{Status: "blocked", Reasons: []string{"actor is not allowed"}},
			},
			want: "request mismatch",
		},
		{
			name: "invalid status",
			resp: ExternalControlResponse{
				Request:  ExternalControlRequest{Actor: "Worker", ChannelID: "viewer", Action: "promotion_apply"},
				Decision: ExternalControlDecision{Status: "approved"},
			},
			want: "invalid status",
		},
		{
			name: "needs approval without flag",
			resp: ExternalControlResponse{
				Request:  ExternalControlRequest{Actor: "Worker", ChannelID: "viewer", Action: "promotion_apply"},
				Decision: ExternalControlDecision{Status: "needs_approval"},
			},
			want: "without requires_approval",
		},
		{
			name: "blocked without reasons",
			resp: ExternalControlResponse{
				Request:  ExternalControlRequest{Actor: "Worker", ChannelID: "viewer", Action: "promotion_apply"},
				Decision: ExternalControlDecision{Status: "blocked"},
			},
			want: "blocked without reasons",
		},
		{
			name: "allowed approval required without approval",
			resp: ExternalControlResponse{
				Request:  ExternalControlRequest{Actor: "Worker", ChannelID: "viewer", Action: "promotion_apply"},
				Decision: ExternalControlDecision{Status: "allowed", RequiresApproval: true},
			},
			want: "without human approval",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/viewer/ai-workflow/external-control/check" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(tt.resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.CheckExternalControl(context.Background(), ExternalControlRequest{
				Actor:     "Worker",
				ChannelID: "viewer",
				Action:    "promotion_apply",
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("CheckExternalControl() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestEvaluateHeavyWorker(t *testing.T) {
	now := time.Date(2026, 5, 20, 5, 50, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/viewer/ai-workflow/heavy-worker/evaluate" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req HeavyWorkerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.EventID != "evt_parent_1" || req.Agent != "Worker" || !req.UserRequestedDeepDive {
			t.Fatalf("payload=%#v", req)
		}
		_ = json.NewEncoder(w).Encode(HeavyWorkerResponse{
			Request:  req,
			Decision: HeavyWorkerDecision{Status: "requested", Reasons: []string{"user requested deep dive"}},
			Event: &WorkflowEvent{
				EventID:       "heavy_worker:evt_parent_1",
				ParentEventID: "evt_parent_1",
				EventType:     "heavy_worker_requested",
				Agent:         "Worker",
				Status:        "requested",
				CreatedAt:     now,
			},
		})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.EvaluateHeavyWorker(context.Background(), HeavyWorkerRequest{
		EventID:               "evt_parent_1",
		Agent:                 "Worker",
		UserRequestedDeepDive: true,
		Reason:                "deep investigation requested",
	})
	if err != nil {
		t.Fatalf("EvaluateHeavyWorker() error = %v", err)
	}
	if resp.Decision.Status != "requested" || resp.Event == nil || resp.Event.ParentEventID != "evt_parent_1" {
		t.Fatalf("response=%#v", resp)
	}
}

func TestEvaluateHeavyWorkerRejectsInvalidRequest(t *testing.T) {
	tests := []struct {
		name string
		req  HeavyWorkerRequest
		want string
	}{
		{name: "missing agent", req: HeavyWorkerRequest{EventID: "evt_1"}, want: "missing agent"},
		{name: "negative count", req: HeavyWorkerRequest{Agent: "Worker", TargetFileCount: -1}, want: "counts must be >= 0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, called, cleanup := newNoRequestClient(t)
			defer cleanup()
			_, err := client.EvaluateHeavyWorker(context.Background(), tt.req)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("EvaluateHeavyWorker() error = %v, want %q", err, tt.want)
			}
			if *called {
				t.Fatal("server was called for invalid request")
			}
		})
	}
}

func TestEvaluateHeavyWorkerRejectsMalformedResponse(t *testing.T) {
	now := time.Date(2026, 5, 20, 5, 50, 0, 0, time.UTC)
	req := HeavyWorkerRequest{EventID: "evt_parent_1", Agent: "Worker", UserRequestedDeepDive: true, Reason: "deep investigation requested"}
	tests := []struct {
		name string
		resp HeavyWorkerResponse
		want string
	}{
		{
			name: "request mismatch",
			resp: HeavyWorkerResponse{
				Request:  HeavyWorkerRequest{EventID: "other", Agent: "Worker", UserRequestedDeepDive: true, Reason: "deep investigation requested"},
				Decision: HeavyWorkerDecision{Status: "requested", Reasons: []string{"user requested deep dive"}},
				Event:    &WorkflowEvent{EventID: "heavy_worker:evt_parent_1", ParentEventID: "evt_parent_1", EventType: "heavy_worker_requested", Agent: "Worker", Status: "requested", CreatedAt: now},
			},
			want: "request mismatch",
		},
		{
			name: "requested missing event",
			resp: HeavyWorkerResponse{
				Request:  req,
				Decision: HeavyWorkerDecision{Status: "requested", Reasons: []string{"user requested deep dive"}},
			},
			want: "missing event",
		},
		{
			name: "blocked with event",
			resp: HeavyWorkerResponse{
				Request:  req,
				Decision: HeavyWorkerDecision{Status: "blocked", Reasons: []string{"reason is required"}},
				Event:    &WorkflowEvent{EventID: "evt_1", EventType: "heavy_worker_requested", Agent: "Worker", Status: "requested"},
			},
			want: "should not include event",
		},
		{
			name: "requested wrong event type",
			resp: HeavyWorkerResponse{
				Request:  req,
				Decision: HeavyWorkerDecision{Status: "requested", Reasons: []string{"user requested deep dive"}},
				Event:    &WorkflowEvent{EventID: "evt_1", ParentEventID: "evt_parent_1", EventType: "heavy_worker_started", Agent: "Worker", Status: "requested", CreatedAt: now},
			},
			want: "event_type mismatch",
		},
		{
			name: "requested event missing created at",
			resp: HeavyWorkerResponse{
				Request:  req,
				Decision: HeavyWorkerDecision{Status: "requested", Reasons: []string{"user requested deep dive"}},
				Event:    &WorkflowEvent{EventID: "evt_1", ParentEventID: "evt_parent_1", EventType: "heavy_worker_requested", Agent: "Worker", Status: "requested"},
			},
			want: "event missing created_at",
		},
		{
			name: "invalid status",
			resp: HeavyWorkerResponse{
				Request:  req,
				Decision: HeavyWorkerDecision{Status: "ok"},
			},
			want: "invalid status",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/viewer/ai-workflow/heavy-worker/evaluate" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(tt.resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.EvaluateHeavyWorker(context.Background(), req)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("EvaluateHeavyWorker() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestHeavyWorkerRuntimeDiagnostics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/viewer/ai-workflow/heavy-worker/runtime-diagnostics" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(HeavyWorkerRuntimeDiagnostics{
			Role:           "Heavy",
			Route:          "ANALYZE",
			RoutePrefix:    "/analyze",
			Provider:       "ollama",
			Configured:     true,
			BaseURL:        "http://127.0.0.1:11434",
			Model:          "heavy-v1",
			TimeoutSec:     60,
			FailureIsError: true,
			LLMOps: HeavyWorkerLLMOpsDiagnostic{
				Configured:    true,
				Enabled:       true,
				BaseURL:       "http://127.0.0.1:8079",
				LiveAvailable: true,
			},
		})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.HeavyWorkerRuntimeDiagnostics(context.Background())
	if err != nil {
		t.Fatalf("HeavyWorkerRuntimeDiagnostics() error = %v", err)
	}
	if !resp.Configured || resp.Role != "Heavy" || resp.Route != "ANALYZE" || !resp.FailureIsError {
		t.Fatalf("diagnostics=%#v", resp)
	}
}

func TestHeavyWorkerRuntimeDiagnosticsRejectsMalformedResponse(t *testing.T) {
	tests := []struct {
		name string
		resp HeavyWorkerRuntimeDiagnostics
		want string
	}{
		{
			name: "wrong role",
			resp: HeavyWorkerRuntimeDiagnostics{Role: "Worker", Route: "ANALYZE", RoutePrefix: "/analyze", FailureIsError: true},
			want: "role mismatch",
		},
		{
			name: "configured without model",
			resp: HeavyWorkerRuntimeDiagnostics{Role: "Heavy", Route: "ANALYZE", RoutePrefix: "/analyze", Configured: true, BaseURL: "http://127.0.0.1:11434", FailureIsError: true},
			want: "configured without base_url/model",
		},
		{
			name: "failure not marked error",
			resp: HeavyWorkerRuntimeDiagnostics{Role: "Heavy", Route: "ANALYZE", RoutePrefix: "/analyze"},
			want: "failure_is_error",
		},
		{
			name: "llm ops unavailable without error",
			resp: HeavyWorkerRuntimeDiagnostics{
				Role:           "Heavy",
				Route:          "ANALYZE",
				RoutePrefix:    "/analyze",
				FailureIsError: true,
				LLMOps:         HeavyWorkerLLMOpsDiagnostic{Configured: true, Enabled: true},
			},
			want: "unavailable without error",
		},
		{
			name: "llm ops live while disabled",
			resp: HeavyWorkerRuntimeDiagnostics{
				Role:           "Heavy",
				Route:          "ANALYZE",
				RoutePrefix:    "/analyze",
				FailureIsError: true,
				LLMOps:         HeavyWorkerLLMOpsDiagnostic{Configured: true, Enabled: false, LiveAvailable: true},
			},
			want: "live while disabled",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/viewer/ai-workflow/heavy-worker/runtime-diagnostics" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(tt.resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.HeavyWorkerRuntimeDiagnostics(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("HeavyWorkerRuntimeDiagnostics() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestRunCommand(t *testing.T) {
	now := time.Date(2026, 5, 20, 5, 45, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/viewer/ai-workflow/commands/run" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req CommandRunRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.CommandName != "review-architecture" || req.Text != "target docs" || req.RunID != "run_1" || req.WorkstreamID != "ws_1" {
			t.Fatalf("payload=%#v", req)
		}
		_ = json.NewEncoder(w).Encode(CommandRunResponse{
			Command: CommandRegistry{CommandName: req.CommandName},
			Event:   WorkflowEvent{EventID: "evt_1", RunID: req.RunID, WorkstreamID: req.WorkstreamID, EventType: "command_invoked", CommandName: req.CommandName, Status: "requested", CreatedAt: now},
		})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.RunCommand(context.Background(), CommandRunRequest{CommandName: "review-architecture", RunID: "run_1", WorkstreamID: "ws_1", Text: "target docs"})
	if err != nil {
		t.Fatalf("RunCommand() error = %v", err)
	}
	if resp.Event.EventID != "evt_1" || resp.Event.Status != "requested" || resp.Event.RunID != "run_1" || resp.Event.WorkstreamID != "ws_1" {
		t.Fatalf("response=%#v", resp)
	}
}

func TestRunCommandRejectsInvalidRequest(t *testing.T) {
	client, called, cleanup := newNoRequestClient(t)
	defer cleanup()
	_, err := client.RunCommand(context.Background(), CommandRunRequest{})
	if err == nil || !strings.Contains(err.Error(), "missing command_name") {
		t.Fatalf("RunCommand() error = %v, want missing command_name", err)
	}
	if *called {
		t.Fatal("server was called for invalid request")
	}
}

func TestRunCommandRejectsMalformedResponse(t *testing.T) {
	now := time.Date(2026, 5, 20, 5, 45, 0, 0, time.UTC)
	tests := []struct {
		name string
		resp CommandRunResponse
		want string
	}{
		{
			name: "command mismatch",
			resp: CommandRunResponse{
				Command: CommandRegistry{CommandName: "other-command"},
				Event:   WorkflowEvent{EventID: "evt_1", EventType: "command_invoked", CommandName: "review-architecture", Status: "requested"},
			},
			want: "command_name mismatch",
		},
		{
			name: "missing event id",
			resp: CommandRunResponse{
				Command: CommandRegistry{CommandName: "review-architecture"},
				Event:   WorkflowEvent{EventType: "command_invoked", CommandName: "review-architecture", Status: "requested"},
			},
			want: "missing event_id",
		},
		{
			name: "wrong status",
			resp: CommandRunResponse{
				Command: CommandRegistry{CommandName: "review-architecture"},
				Event:   WorkflowEvent{EventID: "evt_1", EventType: "command_invoked", CommandName: "review-architecture", Status: "completed"},
			},
			want: "status mismatch",
		},
		{
			name: "run mismatch",
			resp: CommandRunResponse{
				Command: CommandRegistry{CommandName: "review-architecture"},
				Event:   WorkflowEvent{EventID: "evt_1", RunID: "other_run", WorkstreamID: "ws_1", EventType: "command_invoked", CommandName: "review-architecture", Status: "requested", CreatedAt: now},
			},
			want: "run_id mismatch",
		},
		{
			name: "missing created at",
			resp: CommandRunResponse{
				Command: CommandRegistry{CommandName: "review-architecture"},
				Event:   WorkflowEvent{EventID: "evt_1", RunID: "run_1", WorkstreamID: "ws_1", EventType: "command_invoked", CommandName: "review-architecture", Status: "requested"},
			},
			want: "missing created_at",
		},
		{
			name: "valid fixture",
			resp: CommandRunResponse{
				Command: CommandRegistry{CommandName: "review-architecture"},
				Event:   WorkflowEvent{EventID: "evt_1", RunID: "run_1", WorkstreamID: "ws_1", EventType: "command_invoked", CommandName: "review-architecture", Status: "requested", CreatedAt: now},
			},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/viewer/ai-workflow/commands/run" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(tt.resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.RunCommand(context.Background(), CommandRunRequest{CommandName: "review-architecture", RunID: "run_1", WorkstreamID: "ws_1"})
			if tt.want == "" {
				if err != nil {
					t.Fatalf("RunCommand() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("RunCommand() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestAIWorkflowStatusAndContextBudget(t *testing.T) {
	var paths []string
	now := time.Date(2026, 5, 20, 2, 55, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/viewer/ai-workflow":
			if r.Method != http.MethodGet || r.URL.Query().Get("limit") != "25" {
				t.Fatalf("unexpected request: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(AIWorkflowStatus{
				WorkflowEvents:      []WorkflowEvent{{EventID: "evt_1", EventType: "command_invoked", Status: "requested", CreatedAt: now}},
				CommandRegistries:   []CommandRegistry{{CommandName: "review-architecture", FilePath: "commands/review-architecture.md", UpdatedAt: now}},
				ContextUsages:       []ContextUsage{{EventID: "ctx_1", SessionID: "ws_1", Agent: "Worker", ContextTokens: 10, CreatedAt: now}},
				ContextBudgetPolicy: ContextBudgetPolicy{MaxContextTokens: 1000, WarnAtRatio: 0.8, StopAtRatio: 0.95},
			})
		case "/viewer/ai-workflow/context-budget/check":
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected method: %s", r.Method)
			}
			var usage ContextUsage
			if err := json.NewDecoder(r.Body).Decode(&usage); err != nil {
				t.Fatal(err)
			}
			if usage.EventID != "ctx_1" || usage.SessionID != "ws_1" {
				t.Fatalf("payload=%#v", usage)
			}
			_ = json.NewEncoder(w).Encode(ContextBudgetResponse{
				ContextUsage: usage,
				Decision:     ContextBudgetDecision{Status: "ok", Reason: "within limit", ContextTokens: usage.ContextTokens, MaxContextTokens: 1000},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.AIWorkflowStatus(context.Background(), 25)
	if err != nil {
		t.Fatalf("AIWorkflowStatus() error = %v", err)
	}
	if len(status.WorkflowEvents) != 1 || status.ContextBudgetPolicy.MaxContextTokens != 1000 {
		t.Fatalf("status=%#v", status)
	}
	budget, err := client.CheckContextBudget(context.Background(), ContextUsage{
		EventID:       "ctx_1",
		SessionID:     "ws_1",
		Agent:         "Worker",
		ContextTokens: 10,
		CreatedAt:     time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("CheckContextBudget() error = %v", err)
	}
	if budget.Decision.Status != "ok" || budget.ContextUsage.EventID != "ctx_1" {
		t.Fatalf("budget=%#v", budget)
	}
	if len(paths) != 2 {
		t.Fatalf("paths=%#v", paths)
	}
}

func TestAIWorkflowStatusRejectsMalformedCurrentView(t *testing.T) {
	now := time.Date(2026, 5, 20, 2, 55, 0, 0, time.UTC)
	tests := []struct {
		name string
		resp AIWorkflowStatus
		want string
	}{
		{
			name: "duplicate workflow event",
			resp: AIWorkflowStatus{WorkflowEvents: []WorkflowEvent{
				{EventID: "evt_1", EventType: "command_invoked", Status: "requested", CreatedAt: now},
				{EventID: "evt_1", EventType: "command_completed", Status: "completed", CreatedAt: now.Add(time.Second)},
			}},
			want: "duplicate workflow_event",
		},
		{
			name: "missing workflow event status",
			resp: AIWorkflowStatus{WorkflowEvents: []WorkflowEvent{{EventID: "evt_1", EventType: "command_invoked", CreatedAt: now}}},
			want: "workflow_event missing status",
		},
		{
			name: "missing workflow event created at",
			resp: AIWorkflowStatus{WorkflowEvents: []WorkflowEvent{{EventID: "evt_1", EventType: "command_invoked", Status: "requested"}}},
			want: "workflow_event missing created_at",
		},
		{
			name: "duplicate project memory",
			resp: AIWorkflowStatus{ProjectMemoryIndexes: []ProjectMemoryIndex{
				{ID: "pm_1", Repo: "example/repo", FilePath: ".ai/memory.md", MemoryType: "project", UpdatedAt: now},
				{ID: "pm_1", Repo: "example/repo", FilePath: ".ai/memory.md", MemoryType: "project", UpdatedAt: now.Add(time.Second)},
			}},
			want: "duplicate project_memory_index",
		},
		{
			name: "missing project memory file path",
			resp: AIWorkflowStatus{ProjectMemoryIndexes: []ProjectMemoryIndex{
				{ID: "pm_1", Repo: "example/repo", MemoryType: "project", UpdatedAt: now},
			}},
			want: "project_memory_index missing file_path",
		},
		{
			name: "missing project memory updated at",
			resp: AIWorkflowStatus{ProjectMemoryIndexes: []ProjectMemoryIndex{
				{ID: "pm_1", Repo: "example/repo", FilePath: ".ai/memory.md", MemoryType: "project"},
			}},
			want: "project_memory_index missing updated_at",
		},
		{
			name: "duplicate worktree",
			resp: AIWorkflowStatus{WorktreeRegistries: []WorktreeRegistry{
				{WorktreeID: "wt_1", Repo: "example/repo", Path: "../worktrees/example", Branch: "feature/a", Status: "active", CreatedAt: now},
				{WorktreeID: "wt_1", Repo: "example/repo", Path: "../worktrees/example", Branch: "feature/a", Status: "closed", CreatedAt: now.Add(time.Second)},
			}},
			want: "duplicate worktree_registry",
		},
		{
			name: "missing worktree branch",
			resp: AIWorkflowStatus{WorktreeRegistries: []WorktreeRegistry{
				{WorktreeID: "wt_1", Repo: "example/repo", Path: "../worktrees/example", Status: "active", CreatedAt: now},
			}},
			want: "worktree_registry missing branch",
		},
		{
			name: "missing worktree created at",
			resp: AIWorkflowStatus{WorktreeRegistries: []WorktreeRegistry{
				{WorktreeID: "wt_1", Repo: "example/repo", Path: "../worktrees/example", Branch: "feature/a", Status: "active"},
			}},
			want: "worktree_registry missing created_at",
		},
		{
			name: "duplicate command",
			resp: AIWorkflowStatus{CommandRegistries: []CommandRegistry{
				{CommandName: "review-architecture", FilePath: "commands/review-architecture.md", UpdatedAt: now},
				{CommandName: "review-architecture", FilePath: "commands/review-architecture.md", UpdatedAt: now.Add(time.Second)},
			}},
			want: "duplicate command_registry",
		},
		{
			name: "missing command updated at",
			resp: AIWorkflowStatus{CommandRegistries: []CommandRegistry{
				{CommandName: "review-architecture", FilePath: "commands/review-architecture.md"},
			}},
			want: "command_registry missing updated_at",
		},
		{
			name: "missing command file path",
			resp: AIWorkflowStatus{CommandRegistries: []CommandRegistry{
				{CommandName: "review-architecture", UpdatedAt: now},
			}},
			want: "command_registry missing file_path",
		},
		{
			name: "duplicate context usage",
			resp: AIWorkflowStatus{ContextUsages: []ContextUsage{
				{EventID: "ctx_1", Agent: "Worker", CreatedAt: now},
				{EventID: "ctx_1", Agent: "Worker", CreatedAt: now.Add(time.Second)},
			}},
			want: "duplicate context_usage",
		},
		{
			name: "missing context usage agent",
			resp: AIWorkflowStatus{ContextUsages: []ContextUsage{{EventID: "ctx_1", CreatedAt: now}}},
			want: "context_usage missing agent",
		},
		{
			name: "missing context usage created at",
			resp: AIWorkflowStatus{ContextUsages: []ContextUsage{{EventID: "ctx_1", Agent: "Worker"}}},
			want: "context_usage missing created_at",
		},
		{
			name: "negative context usage tokens",
			resp: AIWorkflowStatus{ContextUsages: []ContextUsage{{EventID: "ctx_1", Agent: "Worker", ContextTokens: -1, CreatedAt: now}}},
			want: "context_usage counts must be >= 0",
		},
		{
			name: "negative context usage estimate",
			resp: AIWorkflowStatus{ContextUsages: []ContextUsage{{EventID: "ctx_1", Agent: "Worker", EstimatedCost: -0.01, CreatedAt: now}}},
			want: "context_usage numeric estimates must be >= 0",
		},
		{
			name: "negative context budget max",
			resp: AIWorkflowStatus{ContextBudgetPolicy: ContextBudgetPolicy{MaxContextTokens: -1, WarnAtRatio: 0.8, StopAtRatio: 0.95}},
			want: "negative max_context_tokens",
		},
		{
			name: "enabled context budget missing warn",
			resp: AIWorkflowStatus{ContextBudgetPolicy: ContextBudgetPolicy{MaxContextTokens: 1000, StopAtRatio: 0.95}},
			want: "invalid warn_at_ratio",
		},
		{
			name: "enabled context budget missing stop",
			resp: AIWorkflowStatus{ContextBudgetPolicy: ContextBudgetPolicy{MaxContextTokens: 1000, WarnAtRatio: 0.8}},
			want: "invalid stop_at_ratio",
		},
		{
			name: "enabled context budget inverted ratios",
			resp: AIWorkflowStatus{ContextBudgetPolicy: ContextBudgetPolicy{MaxContextTokens: 1000, WarnAtRatio: 0.95, StopAtRatio: 0.8}},
			want: "warn_at_ratio must be less",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/viewer/ai-workflow" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(tt.resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.AIWorkflowStatus(context.Background(), 0)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("AIWorkflowStatus() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestToolHarnessStatus(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		if r.Method != http.MethodGet {
			t.Fatalf("method=%s", r.Method)
		}
		_ = json.NewEncoder(w).Encode(ToolHarnessStatus{Items: []ToolHarnessEvent{{
			EventID:          "evt_tool_1",
			ToolName:         "file_read",
			RawInputHash:     "sha256:abc",
			ValidationStatus: "repaired",
			Repairs:          []ToolHarnessRepair{{Type: "markdown_autolink_path_unwrap", Path: []string{"path"}}},
			RelationDefaults: []ToolHarnessDefault{{Field: "offset", Value: float64(0), Reason: "limit was provided without offset"}},
			CreatedAt:        time.Now().UTC(),
		}}})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.ToolHarnessStatus(context.Background(), 3)
	if err != nil {
		t.Fatalf("ToolHarnessStatus() error = %v", err)
	}
	if gotPath != "/viewer/tool-harness/recent?limit=3" {
		t.Fatalf("path=%s", gotPath)
	}
	if len(status.Items) != 1 || status.Items[0].EventID != "evt_tool_1" {
		t.Fatalf("status=%#v", status)
	}
}

func TestToolHarnessStatusRejectsMalformedCurrentView(t *testing.T) {
	valid := func() ToolHarnessStatus {
		return ToolHarnessStatus{Items: []ToolHarnessEvent{{
			EventID:          "evt_tool_1",
			ToolName:         "file_read",
			RawInputHash:     "sha256:abc",
			ValidationStatus: "valid",
			CreatedAt:        time.Now().UTC(),
		}}}
	}
	tests := []struct {
		name   string
		mutate func(*ToolHarnessStatus)
		want   string
	}{
		{name: "duplicate event", mutate: func(s *ToolHarnessStatus) {
			s.Items = append(s.Items, s.Items[0])
		}, want: "duplicate event"},
		{name: "missing tool", mutate: func(s *ToolHarnessStatus) {
			s.Items[0].ToolName = ""
		}, want: "missing tool_name"},
		{name: "missing hash", mutate: func(s *ToolHarnessStatus) {
			s.Items[0].RawInputHash = ""
		}, want: "missing raw_input_hash"},
		{name: "missing created at", mutate: func(s *ToolHarnessStatus) {
			s.Items[0].CreatedAt = time.Time{}
		}, want: "missing created_at"},
		{name: "invalid validation status", mutate: func(s *ToolHarnessStatus) {
			s.Items[0].ValidationStatus = "skipped"
		}, want: "invalid validation_status"},
		{name: "valid with repair evidence", mutate: func(s *ToolHarnessStatus) {
			s.Items[0].Repairs = []ToolHarnessRepair{{Type: "placeholder_unwrap"}}
		}, want: "valid event includes repair evidence"},
		{name: "repaired without repair evidence", mutate: func(s *ToolHarnessStatus) {
			s.Items[0].ValidationStatus = "repaired"
		}, want: "repaired event missing repair evidence"},
		{name: "missing repair type", mutate: func(s *ToolHarnessStatus) {
			s.Items[0].ValidationStatus = "repaired"
			s.Items[0].Repairs = []ToolHarnessRepair{{Path: []string{"path"}}}
		}, want: "repair missing type"},
		{name: "missing relation default field", mutate: func(s *ToolHarnessStatus) {
			s.Items[0].ValidationStatus = "repaired"
			s.Items[0].RelationDefaults = []ToolHarnessDefault{{Reason: "limit was provided without offset"}}
		}, want: "relation default missing field"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := valid()
			tt.mutate(&resp)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/viewer/tool-harness/recent" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.ToolHarnessStatus(context.Background(), 0)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ToolHarnessStatus() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestDCIRecentAndSearch(t *testing.T) {
	var paths []string
	now := time.Date(2026, 5, 19, 16, 30, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.String())
		switch r.URL.Path {
		case "/viewer/dci/recent":
			if r.Method != http.MethodGet {
				t.Fatalf("method=%s", r.Method)
			}
			_ = json.NewEncoder(w).Encode(DCIRecentStatus{Items: []DCISearchTrace{{
				EventID:            "evt_dci_1",
				StartedAt:          now.Add(-time.Second),
				Actor:              "Worker",
				Mode:               "dci",
				UserQuery:          "Tool Harness",
				Status:             "completed",
				EndedAt:            now,
				FinalEvidenceCount: 1,
				Steps:              []DCISearchStep{{StepNo: 1, Tool: "file_read", Status: "completed", ResultCount: 1, CreatedAt: now}},
			}}})
		case "/viewer/dci/search":
			if r.Method != http.MethodPost {
				t.Fatalf("method=%s", r.Method)
			}
			var req DCISearchRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(DCISearchResult{
				Pack: DCIEvidencePack{
					EventID: "evt_dci_search",
					Query:   req.Query,
					Evidence: []DCIEvidence{{
						EvidenceID: "ev_1",
						FilePath:   "docs/10_新仕様/20_Tool_Harness_Contract_Mediation仕様.md",
						LineStart:  1,
						LineEnd:    2,
						Snippet:    "Tool Harness",
					}},
				},
				Trace: DCISearchTrace{
					EventID:            "evt_dci_search",
					StartedAt:          now.Add(-time.Second),
					Actor:              "Worker",
					Mode:               "dci",
					UserQuery:          req.Query,
					Status:             "completed",
					EndedAt:            now,
					FinalEvidenceCount: 1,
					Steps:              []DCISearchStep{{StepNo: 1, Tool: "file_read", Status: "completed", ResultCount: 1, CreatedAt: now}},
				},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	recent, err := client.DCIRecent(context.Background(), 5)
	if err != nil {
		t.Fatalf("DCIRecent() error = %v", err)
	}
	if len(recent.Items) != 1 || recent.Items[0].EventID != "evt_dci_1" {
		t.Fatalf("recent=%#v", recent)
	}
	search, err := client.DCISearch(context.Background(), DCISearchRequest{Query: "Tool Harness"})
	if err != nil {
		t.Fatalf("DCISearch() error = %v", err)
	}
	if search.Pack.EventID != "evt_dci_search" || len(search.Pack.Evidence) != 1 {
		t.Fatalf("search=%#v", search)
	}
	if len(paths) != 2 || paths[0] != "/viewer/dci/recent?limit=5" || paths[1] != "/viewer/dci/search" {
		t.Fatalf("paths=%#v", paths)
	}
}

func TestDCIRecentRejectsMalformedCurrentView(t *testing.T) {
	now := time.Date(2026, 5, 19, 16, 30, 0, 0, time.UTC)
	valid := func() DCIRecentStatus {
		return DCIRecentStatus{Items: []DCISearchTrace{{
			EventID:            "evt_dci_1",
			StartedAt:          now.Add(-time.Second),
			Actor:              "Worker",
			Mode:               "dci",
			UserQuery:          "DCI",
			Status:             "completed",
			EndedAt:            now,
			FinalEvidenceCount: 1,
			Steps:              []DCISearchStep{{StepNo: 1, Tool: "file_read", Status: "completed", CreatedAt: now}},
		}}}
	}
	tests := []struct {
		name   string
		mutate func(*DCIRecentStatus)
		want   string
	}{
		{name: "duplicate trace", mutate: func(s *DCIRecentStatus) {
			s.Items = append(s.Items, s.Items[0])
		}, want: "duplicate trace"},
		{name: "missing query", mutate: func(s *DCIRecentStatus) {
			s.Items[0].UserQuery = ""
		}, want: "missing user_query"},
		{name: "missing status", mutate: func(s *DCIRecentStatus) {
			s.Items[0].Status = ""
		}, want: "missing status"},
		{name: "missing started_at", mutate: func(s *DCIRecentStatus) {
			s.Items[0].StartedAt = time.Time{}
		}, want: "missing started_at"},
		{name: "missing actor", mutate: func(s *DCIRecentStatus) {
			s.Items[0].Actor = ""
		}, want: "trace missing actor"},
		{name: "missing mode", mutate: func(s *DCIRecentStatus) {
			s.Items[0].Mode = ""
		}, want: "trace missing mode"},
		{name: "invalid trace status", mutate: func(s *DCIRecentStatus) {
			s.Items[0].Status = "done"
		}, want: "invalid trace status"},
		{name: "failed trace missing error", mutate: func(s *DCIRecentStatus) {
			s.Items[0].Status = "failed"
			s.Items[0].ErrorMessage = ""
		}, want: "failed trace missing error_message"},
		{name: "terminal trace missing ended_at", mutate: func(s *DCIRecentStatus) {
			s.Items[0].EndedAt = time.Time{}
		}, want: "missing ended_at"},
		{name: "duplicate step", mutate: func(s *DCIRecentStatus) {
			s.Items[0].Steps = append(s.Items[0].Steps, s.Items[0].Steps[0])
		}, want: "duplicate step_no"},
		{name: "missing step tool", mutate: func(s *DCIRecentStatus) {
			s.Items[0].Steps[0].Tool = ""
		}, want: "step missing tool"},
		{name: "invalid step status", mutate: func(s *DCIRecentStatus) {
			s.Items[0].Steps[0].Status = "done"
		}, want: "invalid step status"},
		{name: "error step missing error", mutate: func(s *DCIRecentStatus) {
			s.Items[0].Steps[0].Status = "error"
			s.Items[0].Steps[0].ErrorMessage = ""
		}, want: "error step missing error_message"},
		{name: "negative step result count", mutate: func(s *DCIRecentStatus) {
			s.Items[0].Steps[0].ResultCount = -1
		}, want: "result_count"},
		{name: "step missing created at", mutate: func(s *DCIRecentStatus) {
			s.Items[0].Steps[0].CreatedAt = time.Time{}
		}, want: "step missing created_at"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := valid()
			tt.mutate(&resp)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/viewer/dci/recent" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.DCIRecent(context.Background(), 0)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("DCIRecent() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestDCISearchRejectsInvalidRequest(t *testing.T) {
	client, called, cleanup := newNoRequestClient(t)
	defer cleanup()
	_, err := client.DCISearch(context.Background(), DCISearchRequest{})
	if err == nil || !strings.Contains(err.Error(), "missing query") {
		t.Fatalf("DCISearch() error = %v, want missing query", err)
	}
	if *called {
		t.Fatal("server was called for invalid request")
	}
}

func TestDCISearchRejectsMalformedResponse(t *testing.T) {
	now := time.Date(2026, 5, 19, 16, 30, 0, 0, time.UTC)
	valid := func() DCISearchResult {
		return DCISearchResult{
			Pack: DCIEvidencePack{
				EventID: "evt_dci_search",
				Query:   "DCI",
				Evidence: []DCIEvidence{{
					EvidenceID: "ev_1",
					FilePath:   "docs/10_新仕様/19_DCI_直接コーパス探索仕様.md",
					LineStart:  1,
					LineEnd:    1,
					Snippet:    "DCI",
				}},
			},
			Trace: DCISearchTrace{
				EventID:            "evt_dci_search",
				StartedAt:          now.Add(-time.Second),
				Actor:              "Worker",
				Mode:               "dci",
				UserQuery:          "DCI",
				Status:             "completed",
				EndedAt:            now,
				FinalEvidenceCount: 1,
				Steps:              []DCISearchStep{{StepNo: 1, Tool: "file_read", Status: "completed", CreatedAt: now}},
			},
		}
	}
	tests := []struct {
		name   string
		mutate func(*DCISearchResult)
		want   string
	}{
		{name: "event mismatch", mutate: func(s *DCISearchResult) {
			s.Trace.EventID = "other"
		}, want: "event_id mismatch"},
		{name: "query mismatch", mutate: func(s *DCISearchResult) {
			s.Pack.Query = "other"
		}, want: "query mismatch"},
		{name: "terminal trace missing ended_at", mutate: func(s *DCISearchResult) {
			s.Trace.EndedAt = time.Time{}
		}, want: "missing ended_at"},
		{name: "missing started_at", mutate: func(s *DCISearchResult) {
			s.Trace.StartedAt = time.Time{}
		}, want: "missing started_at"},
		{name: "missing actor", mutate: func(s *DCISearchResult) {
			s.Trace.Actor = ""
		}, want: "trace missing actor"},
		{name: "missing mode", mutate: func(s *DCISearchResult) {
			s.Trace.Mode = ""
		}, want: "trace missing mode"},
		{name: "invalid trace status", mutate: func(s *DCISearchResult) {
			s.Trace.Status = "done"
		}, want: "invalid trace status"},
		{name: "evidence count mismatch", mutate: func(s *DCISearchResult) {
			s.Trace.FinalEvidenceCount = 2
		}, want: "evidence count mismatch"},
		{name: "pack confidence out of range", mutate: func(s *DCISearchResult) {
			s.Pack.Confidence = 1.2
		}, want: "pack confidence out of range"},
		{name: "missing evidence path", mutate: func(s *DCISearchResult) {
			s.Pack.Evidence[0].FilePath = ""
		}, want: "evidence missing file_path"},
		{name: "invalid evidence lines", mutate: func(s *DCISearchResult) {
			s.Pack.Evidence[0].LineEnd = 0
		}, want: "invalid line range"},
		{name: "evidence confidence out of range", mutate: func(s *DCISearchResult) {
			s.Pack.Evidence[0].Confidence = -0.1
		}, want: "evidence confidence out of range"},
		{name: "step missing created at", mutate: func(s *DCISearchResult) {
			s.Trace.Steps[0].CreatedAt = time.Time{}
		}, want: "step missing created_at"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := valid()
			tt.mutate(&resp)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/viewer/dci/search" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.DCISearch(context.Background(), DCISearchRequest{Query: "DCI"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("DCISearch() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestKnowledgeMemoryStatusAndReview(t *testing.T) {
	var paths []string
	now := time.Date(2026, 5, 20, 3, 25, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.String())
		switch r.URL.Path {
		case "/viewer/knowledge-memory":
			if r.Method != http.MethodGet {
				t.Fatalf("method=%s", r.Method)
			}
			_ = json.NewEncoder(w).Encode(KnowledgeMemoryStatus{
				PersonalArchive: []KnowledgePersonalArchiveEntry{{
					EntryID:      "pa_1",
					UserID:       "ren",
					OriginalText: "protected original",
					Protected:    true,
					CreatedAt:    now,
				}},
				CreativeKnowledge: []KnowledgeCreativeItem{{ItemID: "ck_1", Title: "Work", Status: "candidate", CreatedAt: now}},
				NewsKnowledge:     []KnowledgeNewsItem{{ItemID: "news_1", Source: "example", Topic: "tech", Status: "candidate", CreatedAt: now}},
				DailyIntakeRules:  []KnowledgeDailyIntakeRule{{RuleID: "rule_1", UserID: "ren", Topic: "AI", Cadence: "daily", Status: "candidate", CreatedAt: now}},
				TemporalMarkers:   []KnowledgeTemporalMarker{{MarkerID: "tm_1", Layer: "week", ReferenceID: "pa_1", Summary: "weekly memory", CreatedAt: now}},
				DreamRuns:         []KnowledgeDreamRun{{RunID: "dream_1", Status: "proposal", ReviewStatus: "pending", CreatedAt: now}},
			})
		case "/viewer/knowledge-memory/news-knowledge":
			if r.Method != http.MethodPost {
				t.Fatalf("method=%s", r.Method)
			}
			var item KnowledgeNewsItem
			if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
				t.Fatal(err)
			}
			if item.ItemID != "news_1" || item.Status != "candidate" {
				t.Fatalf("news item=%#v", item)
			}
			_ = json.NewEncoder(w).Encode(KnowledgeMemoryCreateResponse{Status: "created"})
		case "/viewer/knowledge-memory/daily-intake-rules":
			if r.Method != http.MethodPost {
				t.Fatalf("method=%s", r.Method)
			}
			var item KnowledgeDailyIntakeRule
			if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
				t.Fatal(err)
			}
			if item.RuleID != "rule_1" || item.Status != "candidate" {
				t.Fatalf("daily intake rule=%#v", item)
			}
			_ = json.NewEncoder(w).Encode(KnowledgeMemoryCreateResponse{Status: "created"})
		case "/viewer/knowledge-memory/review":
			if r.Method != http.MethodPost {
				t.Fatalf("method=%s", r.Method)
			}
			var req KnowledgeMemoryReviewRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(KnowledgeMemoryReviewResponse{
				Status:       "reviewed",
				DetailType:   req.DetailType,
				ID:           req.ID,
				ReviewStatus: req.ReviewStatus,
				Promoted:     req.Promote,
				AutoPromote:  false,
				ReviewedBy:   req.ReviewedBy,
				Comparison: KnowledgeMemoryReviewComparison{
					CurrentStatus: "candidate",
					TargetStatus:  "promoted",
					FormalTarget:  "knowledge_memory.news_knowledge:promoted",
				},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.KnowledgeMemoryStatus(context.Background(), 4)
	if err != nil {
		t.Fatalf("KnowledgeMemoryStatus() error = %v", err)
	}
	if len(status.PersonalArchive) != 1 || len(status.DreamRuns) != 1 {
		t.Fatalf("status=%#v", status)
	}
	createdNews, err := client.CreateKnowledgeNewsItem(context.Background(), KnowledgeNewsItem{
		ItemID: "news_1",
		Source: "example",
		Topic:  "tech",
		Status: "candidate",
	})
	if err != nil {
		t.Fatalf("CreateKnowledgeNewsItem() error = %v", err)
	}
	if createdNews.Status != "created" {
		t.Fatalf("createdNews=%#v", createdNews)
	}
	createdRule, err := client.CreateKnowledgeDailyIntakeRule(context.Background(), KnowledgeDailyIntakeRule{
		RuleID:  "rule_1",
		UserID:  "ren",
		Topic:   "AI",
		Cadence: "daily",
		Status:  "candidate",
	})
	if err != nil {
		t.Fatalf("CreateKnowledgeDailyIntakeRule() error = %v", err)
	}
	if createdRule.Status != "created" {
		t.Fatalf("createdRule=%#v", createdRule)
	}
	review, err := client.ReviewKnowledgeMemory(context.Background(), KnowledgeMemoryReviewRequest{
		DetailType:   "news_knowledge",
		ID:           "news_1",
		ReviewStatus: "approved",
		Promote:      true,
		ReviewedBy:   "viewer",
	})
	if err != nil {
		t.Fatalf("ReviewKnowledgeMemory() error = %v", err)
	}
	if !review.Promoted || review.Comparison.TargetStatus != "promoted" {
		t.Fatalf("review=%#v", review)
	}
	if len(paths) != 4 || paths[0] != "/viewer/knowledge-memory?limit=4" || paths[1] != "/viewer/knowledge-memory/news-knowledge" || paths[2] != "/viewer/knowledge-memory/daily-intake-rules" || paths[3] != "/viewer/knowledge-memory/review" {
		t.Fatalf("paths=%#v", paths)
	}
}

func TestCreateKnowledgeMemoryItemsRejectInvalidRequestAndResponse(t *testing.T) {
	t.Run("invalid news request", func(t *testing.T) {
		client, called, cleanup := newNoRequestClient(t)
		defer cleanup()
		_, err := client.CreateKnowledgeNewsItem(context.Background(), KnowledgeNewsItem{ItemID: "news_1", Topic: "tech", Status: "candidate"})
		if err == nil || !strings.Contains(err.Error(), "missing source") {
			t.Fatalf("CreateKnowledgeNewsItem() error = %v, want missing source", err)
		}
		if *called {
			t.Fatal("server was called for invalid request")
		}
	})
	t.Run("invalid daily rule request", func(t *testing.T) {
		client, called, cleanup := newNoRequestClient(t)
		defer cleanup()
		_, err := client.CreateKnowledgeDailyIntakeRule(context.Background(), KnowledgeDailyIntakeRule{RuleID: "rule_1", UserID: "ren", Topic: "AI", Status: "candidate"})
		if err == nil || !strings.Contains(err.Error(), "missing cadence") {
			t.Fatalf("CreateKnowledgeDailyIntakeRule() error = %v, want missing cadence", err)
		}
		if *called {
			t.Fatal("server was called for invalid request")
		}
	})
	t.Run("malformed response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(KnowledgeMemoryCreateResponse{Status: "ok"})
		}))
		defer server.Close()
		client, err := New(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.CreateKnowledgeNewsItem(context.Background(), KnowledgeNewsItem{ItemID: "news_1", Source: "example", Topic: "tech", Status: "candidate"})
		if err == nil || !strings.Contains(err.Error(), "status mismatch") {
			t.Fatalf("CreateKnowledgeNewsItem() error = %v, want status mismatch", err)
		}
	})
}

func TestKnowledgeMemoryStatusRejectsMalformedCurrentView(t *testing.T) {
	now := time.Date(2026, 5, 20, 3, 25, 0, 0, time.UTC)
	valid := func() KnowledgeMemoryStatus {
		return KnowledgeMemoryStatus{
			PersonalArchive:   []KnowledgePersonalArchiveEntry{{EntryID: "pa_1", UserID: "ren", OriginalText: "protected original", Protected: true, CreatedAt: now}},
			CreativeKnowledge: []KnowledgeCreativeItem{{ItemID: "ck_1", Title: "Work", Status: "candidate", CreatedAt: now}},
			NewsKnowledge:     []KnowledgeNewsItem{{ItemID: "news_1", Source: "example", Topic: "tech", Status: "candidate", CreatedAt: now}},
			DailyIntakeRules:  []KnowledgeDailyIntakeRule{{RuleID: "rule_1", UserID: "ren", Topic: "AI", Cadence: "daily", Status: "candidate", CreatedAt: now}},
			TemporalMarkers:   []KnowledgeTemporalMarker{{MarkerID: "tm_1", Layer: "week", ReferenceID: "pa_1", Summary: "weekly memory", CreatedAt: now}},
			DreamRuns:         []KnowledgeDreamRun{{RunID: "dream_1", Status: "proposal", ReviewStatus: "pending", CreatedAt: now}},
		}
	}
	tests := []struct {
		name   string
		mutate func(*KnowledgeMemoryStatus)
		want   string
	}{
		{name: "unprotected personal archive", mutate: func(s *KnowledgeMemoryStatus) {
			s.PersonalArchive[0].Protected = false
		}, want: "original must be protected"},
		{name: "personal archive missing created at", mutate: func(s *KnowledgeMemoryStatus) {
			s.PersonalArchive[0].CreatedAt = time.Time{}
		}, want: "personal_archive \"pa_1\" missing created_at"},
		{name: "duplicate creative item", mutate: func(s *KnowledgeMemoryStatus) {
			s.CreativeKnowledge = append(s.CreativeKnowledge, s.CreativeKnowledge[0])
		}, want: "duplicate creative_knowledge"},
		{name: "invalid creative status", mutate: func(s *KnowledgeMemoryStatus) {
			s.CreativeKnowledge[0].Status = "done"
		}, want: "invalid creative_knowledge status"},
		{name: "creative item missing created at", mutate: func(s *KnowledgeMemoryStatus) {
			s.CreativeKnowledge[0].CreatedAt = time.Time{}
		}, want: "creative_knowledge \"ck_1\" missing created_at"},
		{name: "missing news source", mutate: func(s *KnowledgeMemoryStatus) {
			s.NewsKnowledge[0].Source = ""
		}, want: "news_knowledge missing source"},
		{name: "invalid news status", mutate: func(s *KnowledgeMemoryStatus) {
			s.NewsKnowledge[0].Status = "done"
		}, want: "invalid news_knowledge status"},
		{name: "news item missing created at", mutate: func(s *KnowledgeMemoryStatus) {
			s.NewsKnowledge[0].CreatedAt = time.Time{}
		}, want: "news_knowledge \"news_1\" missing created_at"},
		{name: "missing intake cadence", mutate: func(s *KnowledgeMemoryStatus) {
			s.DailyIntakeRules[0].Cadence = ""
		}, want: "daily_intake_rule missing cadence"},
		{name: "invalid intake status", mutate: func(s *KnowledgeMemoryStatus) {
			s.DailyIntakeRules[0].Status = "done"
		}, want: "invalid daily_intake_rule status"},
		{name: "intake rule missing created at", mutate: func(s *KnowledgeMemoryStatus) {
			s.DailyIntakeRules[0].CreatedAt = time.Time{}
		}, want: "daily_intake_rule \"rule_1\" missing created_at"},
		{name: "missing marker summary", mutate: func(s *KnowledgeMemoryStatus) {
			s.TemporalMarkers[0].Summary = ""
		}, want: "temporal_marker missing summary"},
		{name: "invalid marker layer", mutate: func(s *KnowledgeMemoryStatus) {
			s.TemporalMarkers[0].Layer = "forever"
		}, want: "invalid temporal_marker layer"},
		{name: "negative marker access count", mutate: func(s *KnowledgeMemoryStatus) {
			s.TemporalMarkers[0].AccessCount = -1
		}, want: "access_count must be >= 0"},
		{name: "temporal marker missing created at", mutate: func(s *KnowledgeMemoryStatus) {
			s.TemporalMarkers[0].CreatedAt = time.Time{}
		}, want: "temporal_marker \"tm_1\" missing created_at"},
		{name: "invalid dream status", mutate: func(s *KnowledgeMemoryStatus) {
			s.DreamRuns[0].Status = "done"
		}, want: "invalid dream_run status"},
		{name: "invalid dream review status", mutate: func(s *KnowledgeMemoryStatus) {
			s.DreamRuns[0].ReviewStatus = "done"
		}, want: "invalid dream_run review_status"},
		{name: "dream auto approved", mutate: func(s *KnowledgeMemoryStatus) {
			s.DreamRuns[0].Status = "proposal"
			s.DreamRuns[0].ReviewStatus = "approved"
		}, want: "cannot be auto-approved"},
		{name: "dream promoted with pending review", mutate: func(s *KnowledgeMemoryStatus) {
			s.DreamRuns[0].Status = "promoted"
			s.DreamRuns[0].ReviewStatus = "pending"
		}, want: "pending review requires draft or proposal status"},
		{name: "dream rejected with approved review", mutate: func(s *KnowledgeMemoryStatus) {
			s.DreamRuns[0].Status = "rejected"
			s.DreamRuns[0].ReviewStatus = "approved"
		}, want: "cannot be auto-approved"},
		{name: "dream reviewed with rejected review", mutate: func(s *KnowledgeMemoryStatus) {
			s.DreamRuns[0].Status = "reviewed"
			s.DreamRuns[0].ReviewStatus = "rejected"
		}, want: "rejected review requires rejected status"},
		{name: "dream run missing created at", mutate: func(s *KnowledgeMemoryStatus) {
			s.DreamRuns[0].CreatedAt = time.Time{}
		}, want: "dream_run \"dream_1\" missing created_at"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := valid()
			tt.mutate(&resp)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/viewer/knowledge-memory" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.KnowledgeMemoryStatus(context.Background(), 0)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("KnowledgeMemoryStatus() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestReviewKnowledgeMemoryRejectsInvalidRequest(t *testing.T) {
	tests := []struct {
		name string
		req  KnowledgeMemoryReviewRequest
		want string
	}{
		{name: "missing detail type", req: KnowledgeMemoryReviewRequest{ID: "news_1", ReviewStatus: "approved"}, want: "missing detail_type"},
		{name: "missing id", req: KnowledgeMemoryReviewRequest{DetailType: "news_knowledge", ReviewStatus: "approved"}, want: "missing id"},
		{name: "invalid review", req: KnowledgeMemoryReviewRequest{DetailType: "news_knowledge", ID: "news_1", ReviewStatus: "pending"}, want: "approved or rejected"},
		{name: "promote rejected", req: KnowledgeMemoryReviewRequest{DetailType: "news_knowledge", ID: "news_1", ReviewStatus: "rejected", Promote: true}, want: "promote requires approved"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, called, cleanup := newNoRequestClient(t)
			defer cleanup()
			_, err := client.ReviewKnowledgeMemory(context.Background(), tt.req)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ReviewKnowledgeMemory() error = %v, want %q", err, tt.want)
			}
			if *called {
				t.Fatal("server was called for invalid request")
			}
		})
	}
}

func TestReviewKnowledgeMemoryRejectsMalformedResponse(t *testing.T) {
	valid := func() KnowledgeMemoryReviewResponse {
		return KnowledgeMemoryReviewResponse{
			Status:       "reviewed",
			DetailType:   "daily_intake_rule",
			ID:           "rule_1",
			ReviewStatus: "approved",
			Promoted:     true,
			AutoPromote:  false,
			Comparison: KnowledgeMemoryReviewComparison{
				CurrentStatus: "candidate",
				TargetStatus:  "enabled",
				FormalTarget:  "source_registry.daily_intake_rule:enabled",
			},
		}
	}
	tests := []struct {
		name   string
		mutate func(*KnowledgeMemoryReviewResponse)
		want   string
	}{
		{name: "id mismatch", mutate: func(s *KnowledgeMemoryReviewResponse) {
			s.ID = "other"
		}, want: "id mismatch"},
		{name: "promoted mismatch", mutate: func(s *KnowledgeMemoryReviewResponse) {
			s.Promoted = false
		}, want: "promoted mismatch"},
		{name: "auto promote", mutate: func(s *KnowledgeMemoryReviewResponse) {
			s.AutoPromote = true
		}, want: "auto_promoted"},
		{name: "missing formal target", mutate: func(s *KnowledgeMemoryReviewResponse) {
			s.Comparison.FormalTarget = ""
		}, want: "missing formal_target"},
		{name: "wrong target status", mutate: func(s *KnowledgeMemoryReviewResponse) {
			s.Comparison.TargetStatus = "promoted"
		}, want: "target_status mismatch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := valid()
			tt.mutate(&resp)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/viewer/knowledge-memory/review" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.ReviewKnowledgeMemory(context.Background(), KnowledgeMemoryReviewRequest{
				DetailType:   "daily_intake_rule",
				ID:           "rule_1",
				ReviewStatus: "approved",
				Promote:      true,
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ReviewKnowledgeMemory() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestSourceRegistryStatusStagingValidateAndPromote(t *testing.T) {
	minTrust := 0.5
	now := "2026-05-20T03:35:00Z"
	promotedAt := "2026-05-20T06:10:00Z"
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.String())
		switch r.URL.Path {
		case "/viewer/source-registry":
			switch r.Method {
			case http.MethodGet:
				if r.URL.Query().Get("action") == "staging" {
					_ = json.NewEncoder(w).Encode(SourceRegistryStagingStatus{Items: []SourceRegistryStagingItem{{
						ID:               "stg_1",
						Kind:             "external_fetch",
						Namespace:        "kb:ai",
						EventID:          "evt_1",
						SourceID:         "rss:ai",
						SourceURL:        "https://example.com/item",
						RawText:          "raw text",
						SummaryDraft:     "summary",
						ValidationStatus: "pending",
						CreatedAt:        now,
						UpdatedAt:        now,
					}}})
					return
				}
				_ = json.NewEncoder(w).Encode(SourceRegistryStatus{Entries: []SourceRegistryEntry{{
					SourceID:         "rss:ai",
					URL:              "https://example.com/feed.xml",
					Kind:             "rss",
					TrustScore:       0.9,
					FetchIntervalSec: 3600,
					Enabled:          true,
					CreatedAt:        now,
					UpdatedAt:        now,
				}}})
			case http.MethodPost:
				switch r.URL.Query().Get("action") {
				case "validate":
					var req SourceRegistryValidateRequest
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						t.Fatal(err)
					}
					if req.ID != "stg_1" || req.MinimumTrustScore == nil || *req.MinimumTrustScore != minTrust {
						t.Fatalf("validate payload=%#v", req)
					}
					_ = json.NewEncoder(w).Encode(SourceRegistryValidationResponse{Result: SourceRegistryValidationResult{
						ItemID: "stg_1",
						Passed: true,
						Status: "validated",
					}})
				case "promote":
					var req SourceRegistryPromoteRequest
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						t.Fatal(err)
					}
					if req.Target == "memory" {
						_ = json.NewEncoder(w).Encode(SourceRegistryPromotionResponse{
							Target: "memory",
							Item: map[string]any{
								"ID":        "mem_1",
								"Namespace": "kb:ai",
								"CreatedAt": promotedAt,
								"Meta": map[string]any{
									"staging_id": "stg_1",
								},
							},
						})
						return
					}
					_ = json.NewEncoder(w).Encode(SourceRegistryPromotionResponse{
						Target: "news",
						Item: map[string]any{
							"ID":        "news_1",
							"StagingID": "stg_1",
							"Category":  "ai",
							"CreatedAt": promotedAt,
						},
					})
				default:
					t.Fatalf("unexpected post action: %s", r.URL.RawQuery)
				}
			default:
				t.Fatalf("unexpected method: %s", r.Method)
			}
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.SourceRegistryStatus(context.Background(), true)
	if err != nil {
		t.Fatalf("SourceRegistryStatus() error = %v", err)
	}
	if len(status.Entries) != 1 || status.Entries[0].SourceID != "rss:ai" {
		t.Fatalf("status=%#v", status)
	}
	staging, err := client.SourceRegistryStaging(context.Background(), "pending", 5)
	if err != nil {
		t.Fatalf("SourceRegistryStaging() error = %v", err)
	}
	if len(staging.Items) != 1 || staging.Items[0].ID != "stg_1" {
		t.Fatalf("staging=%#v", staging)
	}
	validation, err := client.ValidateSourceRegistryStaging(context.Background(), SourceRegistryValidateRequest{
		ID:                "stg_1",
		MinimumTrustScore: &minTrust,
	})
	if err != nil {
		t.Fatalf("ValidateSourceRegistryStaging() error = %v", err)
	}
	if !validation.Result.Passed || validation.Result.Status != "validated" {
		t.Fatalf("validation=%#v", validation)
	}
	promotion, err := client.PromoteSourceRegistryStaging(context.Background(), SourceRegistryPromoteRequest{
		ID:       "stg_1",
		Target:   "news",
		Category: "ai",
	})
	if err != nil {
		t.Fatalf("PromoteSourceRegistryStaging() error = %v", err)
	}
	if promotion.Target != "news" {
		t.Fatalf("promotion=%#v", promotion)
	}
	memoryPromotion, err := client.PromoteSourceRegistryStaging(context.Background(), SourceRegistryPromoteRequest{
		ID:              "stg_1",
		Target:          "memory",
		TargetNamespace: "kb:ai",
	})
	if err != nil {
		t.Fatalf("PromoteSourceRegistryStaging(memory) error = %v", err)
	}
	if memoryPromotion.Target != "memory" {
		t.Fatalf("memoryPromotion=%#v", memoryPromotion)
	}
	if len(paths) != 5 {
		t.Fatalf("paths=%#v", paths)
	}
}

func TestSourceRegistryStatusRejectsMalformedCurrentView(t *testing.T) {
	tests := []struct {
		name string
		resp SourceRegistryStatus
		want string
	}{
		{name: "duplicate", resp: SourceRegistryStatus{Entries: []SourceRegistryEntry{
			{SourceID: "rss:ai", URL: "https://example.com", Kind: "rss", TrustScore: 0.9, CreatedAt: "2026-05-20T08:20:00Z", UpdatedAt: "2026-05-20T08:20:00Z"},
			{SourceID: "rss:ai", URL: "https://example.com/2", Kind: "rss", TrustScore: 0.8, CreatedAt: "2026-05-20T08:20:00Z", UpdatedAt: "2026-05-20T08:20:00Z"},
		}}, want: "duplicate source_id"},
		{name: "missing url", resp: SourceRegistryStatus{Entries: []SourceRegistryEntry{{SourceID: "rss:ai", Kind: "rss", TrustScore: 0.9}}}, want: "missing url"},
		{name: "missing created at", resp: SourceRegistryStatus{Entries: []SourceRegistryEntry{{SourceID: "rss:ai", URL: "https://example.com", Kind: "rss", TrustScore: 0.9, UpdatedAt: "2026-05-20T08:20:00Z"}}}, want: "missing created_at"},
		{name: "missing updated at", resp: SourceRegistryStatus{Entries: []SourceRegistryEntry{{SourceID: "rss:ai", URL: "https://example.com", Kind: "rss", TrustScore: 0.9, CreatedAt: "2026-05-20T08:20:00Z"}}}, want: "missing updated_at"},
		{name: "invalid created at", resp: SourceRegistryStatus{Entries: []SourceRegistryEntry{{SourceID: "rss:ai", URL: "https://example.com", Kind: "rss", TrustScore: 0.9, CreatedAt: "bad", UpdatedAt: "2026-05-20T08:20:00Z"}}}, want: "invalid created_at"},
		{name: "bad trust", resp: SourceRegistryStatus{Entries: []SourceRegistryEntry{{SourceID: "rss:ai", URL: "https://example.com", Kind: "rss", TrustScore: 2, CreatedAt: "2026-05-20T08:20:00Z", UpdatedAt: "2026-05-20T08:20:00Z"}}}, want: "trust_score"},
		{name: "invalid kind", resp: SourceRegistryStatus{Entries: []SourceRegistryEntry{{SourceID: "rss:ai", URL: "https://example.com", Kind: "scraper", TrustScore: 0.9, CreatedAt: "2026-05-20T08:20:00Z", UpdatedAt: "2026-05-20T08:20:00Z"}}}, want: "invalid kind"},
		{name: "invalid last status", resp: SourceRegistryStatus{Entries: []SourceRegistryEntry{{SourceID: "rss:ai", URL: "https://example.com", Kind: "rss", TrustScore: 0.9, LastStatus: "done", CreatedAt: "2026-05-20T08:20:00Z", UpdatedAt: "2026-05-20T08:20:00Z"}}}, want: "invalid last_status"},
		{name: "error status without error", resp: SourceRegistryStatus{Entries: []SourceRegistryEntry{{SourceID: "rss:ai", URL: "https://example.com", Kind: "rss", TrustScore: 0.9, LastFetchedAt: "2026-05-20T02:10:00Z", LastStatus: "error", CreatedAt: "2026-05-20T08:20:00Z", UpdatedAt: "2026-05-20T08:20:00Z"}}}, want: "last_error"},
		{name: "ok status without fetched at", resp: SourceRegistryStatus{Entries: []SourceRegistryEntry{{SourceID: "rss:ai", URL: "https://example.com", Kind: "rss", TrustScore: 0.9, LastStatus: "ok", CreatedAt: "2026-05-20T08:20:00Z", UpdatedAt: "2026-05-20T08:20:00Z"}}}, want: "last_fetched_at"},
		{name: "last error without status", resp: SourceRegistryStatus{Entries: []SourceRegistryEntry{{SourceID: "rss:ai", URL: "https://example.com", Kind: "rss", TrustScore: 0.9, LastError: "timeout", CreatedAt: "2026-05-20T08:20:00Z", UpdatedAt: "2026-05-20T08:20:00Z"}}}, want: "last_error without last_status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/viewer/source-registry" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(tt.resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.SourceRegistryStatus(context.Background(), false)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("SourceRegistryStatus() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestMemoryLayersStatus(t *testing.T) {
	now := "2026-05-20T08:30:00Z"
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		if r.Method != http.MethodGet || r.URL.Path != "/viewer/memory/layers" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("session_id") != "session-1" || r.URL.Query().Get("namespace") != "kb:e2e" || r.URL.Query().Get("domain") != "movie" || r.URL.Query().Get("limit") != "4" {
			t.Fatalf("unexpected query: %s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(MemoryLayersStatus{
			SessionID: "session-1",
			Namespace: "kb:e2e",
			Domain:    "movie",
			L0: []MemoryLayerEvent{{
				ID:        "l0-1",
				SessionID: "session-1",
				Message:   "current turn",
				Layer:     "L0",
				CreatedAt: now,
			}},
			L1: []MemoryLayerEvent{{
				ID:        "l1-1",
				Namespace: "kb:e2e",
				Message:   "today memory",
				Layer:     "L1",
				CreatedAt: now,
			}},
			L2: []MemoryLayerThreadSummary{{
				ThreadID: 10,
				Domain:   "movie",
				Summary:  "summary",
			}},
			L3: []MemoryLayerEvent{{
				ID:          "l3-1",
				MemoryState: "confirmed",
				Message:     "confirmed memory",
				Layer:       "L3",
				CreatedAt:   now,
			}},
			L3Qdrant: []MemoryLayerQdrantDocument{{
				ID:      "kb-1",
				Domain:  "movie",
				Content: "qdrant long-term knowledge",
			}},
		})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.MemoryLayers(context.Background(), MemoryLayersRequest{
		SessionID: "session-1",
		Namespace: "kb:e2e",
		Domain:    "movie",
		Limit:     4,
	})
	if err != nil {
		t.Fatalf("MemoryLayers() error = %v", err)
	}
	if len(status.L0) != 1 || len(status.L1) != 1 || len(status.L2) != 1 || len(status.L3) != 1 || len(status.L3Qdrant) != 1 {
		t.Fatalf("unexpected memory layers status: %+v", status)
	}
	if !strings.Contains(gotPath, "/viewer/memory/layers?") {
		t.Fatalf("unexpected path: %s", gotPath)
	}
}

func TestMemoryLayersRejectsMalformedCurrentView(t *testing.T) {
	now := "2026-05-20T08:30:00Z"
	valid := func() MemoryLayersStatus {
		return MemoryLayersStatus{
			L0: []MemoryLayerEvent{{ID: "l0-1", Message: "current turn", Layer: "L0", CreatedAt: now}},
			L1: []MemoryLayerEvent{{ID: "l1-1", Message: "today memory", Layer: "L1", CreatedAt: now}},
			L2: []MemoryLayerThreadSummary{{ThreadID: 10, Summary: "summary"}},
			L3: []MemoryLayerEvent{{ID: "l3-1", Message: "confirmed memory", Layer: "L3", CreatedAt: now}},
			L3Qdrant: []MemoryLayerQdrantDocument{{
				ID:      "kb-1",
				Domain:  "movie",
				Content: "qdrant long-term knowledge",
			}},
		}
	}
	tests := []struct {
		name   string
		mutate func(*MemoryLayersStatus)
		want   string
	}{
		{name: "missing l1 id", mutate: func(s *MemoryLayersStatus) {
			s.L1[0].ID = ""
		}, want: "l1 memory missing id"},
		{name: "missing l1 created at", mutate: func(s *MemoryLayersStatus) {
			s.L1[0].CreatedAt = ""
		}, want: "l1 memory missing created_at"},
		{name: "invalid l1 created at", mutate: func(s *MemoryLayersStatus) {
			s.L1[0].CreatedAt = "bad"
		}, want: "l1 memory invalid created_at"},
		{name: "missing l2 summary", mutate: func(s *MemoryLayersStatus) {
			s.L2[0].Summary = ""
		}, want: "l2 summary missing summary"},
		{name: "missing qdrant id", mutate: func(s *MemoryLayersStatus) {
			s.L3Qdrant[0].ID = ""
		}, want: "l3_qdrant document missing id"},
		{name: "missing qdrant content", mutate: func(s *MemoryLayersStatus) {
			s.L3Qdrant[0].Content = ""
		}, want: "l3_qdrant document missing content"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := valid()
			tt.mutate(&resp)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/viewer/memory/layers" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.MemoryLayers(context.Background(), MemoryLayersRequest{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("MemoryLayers() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestSourceRegistryStagingRejectsMalformedCurrentView(t *testing.T) {
	now := "2026-05-20T03:35:00Z"
	valid := func() SourceRegistryStagingStatus {
		return SourceRegistryStagingStatus{Items: []SourceRegistryStagingItem{{
			ID:               "stg_1",
			Kind:             "external_fetch",
			Namespace:        "kb:ai",
			SourceID:         "rss:ai",
			SourceURL:        "https://example.com/item",
			RawText:          "raw text",
			ValidationStatus: "pending",
			CreatedAt:        now,
			UpdatedAt:        now,
		}}}
	}
	tests := []struct {
		name   string
		mutate func(*SourceRegistryStagingStatus)
		want   string
	}{
		{name: "duplicate", mutate: func(s *SourceRegistryStagingStatus) {
			s.Items = append(s.Items, s.Items[0])
		}, want: "duplicate id"},
		{name: "missing namespace", mutate: func(s *SourceRegistryStagingStatus) {
			s.Items[0].Namespace = ""
		}, want: "missing namespace"},
		{name: "missing raw", mutate: func(s *SourceRegistryStagingStatus) {
			s.Items[0].RawText = ""
		}, want: "missing raw_text"},
		{name: "invalid validation status", mutate: func(s *SourceRegistryStagingStatus) {
			s.Items[0].ValidationStatus = "approved"
		}, want: "invalid validation_status"},
		{name: "missing created at", mutate: func(s *SourceRegistryStagingStatus) {
			s.Items[0].CreatedAt = ""
		}, want: "missing created_at"},
		{name: "invalid created at", mutate: func(s *SourceRegistryStagingStatus) {
			s.Items[0].CreatedAt = "not-a-time"
		}, want: "invalid created_at"},
		{name: "missing updated at", mutate: func(s *SourceRegistryStagingStatus) {
			s.Items[0].UpdatedAt = ""
		}, want: "missing updated_at"},
		{name: "invalid updated at", mutate: func(s *SourceRegistryStagingStatus) {
			s.Items[0].UpdatedAt = "not-a-time"
		}, want: "invalid updated_at"},
		{name: "validated without validated_at", mutate: func(s *SourceRegistryStagingStatus) {
			s.Items[0].ValidationStatus = "validated"
		}, want: "missing validated_at"},
		{name: "rejected without validation issues", mutate: func(s *SourceRegistryStagingStatus) {
			s.Items[0].ValidationStatus = "rejected"
			s.Items[0].Meta = map[string]any{"validated_at": "2026-05-20T02:10:00Z"}
		}, want: "missing validation_issues"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := valid()
			tt.mutate(&resp)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/viewer/source-registry" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.SourceRegistryStaging(context.Background(), "pending", 0)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("SourceRegistryStaging() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestDomainGraphAssertionsCurrentView(t *testing.T) {
	resp := validDomainGraphAssertionsResponse()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/viewer/domain-graph/assertions" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.DomainGraphAssertions(context.Background(), DomainGraphAssertionsRequest{})
	if err != nil {
		t.Fatalf("DomainGraphAssertions() error = %v", err)
	}
	if got.Total != 1 || got.Limit != 50 || got.Offset != 0 || len(got.Items) != 1 {
		t.Fatalf("unexpected response: %+v", got)
	}
	item := got.Items[0]
	if item.ID != "dg:movie:evt:hash" || item.StagingID != "kb:movie:evt:hash" || item.Domain != "movie" {
		t.Fatalf("unexpected item: %+v", item)
	}
	if item.Evidence["staging_id"] != "kb:movie:evt:hash" {
		t.Fatalf("expected evidence roundtrip: %+v", item.Evidence)
	}
}

func TestDomainGraphAssertionsBuildsQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/viewer/domain-graph/assertions" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		q := r.URL.Query()
		assertQueryValue := func(key, want string) {
			t.Helper()
			if got := q.Get(key); got != want {
				t.Fatalf("query %s = %q, want %q; raw=%s", key, got, want, r.URL.RawQuery)
			}
		}
		assertQueryValue("domain", "movie")
		assertQueryValue("entity_type", "work")
		assertQueryValue("entity_id", "movie:1")
		assertQueryValue("relation_type", "performed_by")
		assertQueryValue("source_id", "web:eiga")
		assertQueryValue("validation_status", "validated")
		assertQueryValue("limit", "25")
		assertQueryValue("offset", "5")
		_ = json.NewEncoder(w).Encode(validDomainGraphAssertionsResponse())
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.DomainGraphAssertions(context.Background(), DomainGraphAssertionsRequest{
		Domain:           " movie ",
		EntityType:       " work ",
		EntityID:         " movie:1 ",
		RelationType:     " performed_by ",
		SourceID:         " web:eiga ",
		ValidationStatus: " validated ",
		Limit:            25,
		Offset:           5,
	})
	if err != nil {
		t.Fatalf("DomainGraphAssertions() error = %v", err)
	}
}

func TestDomainGraphAssertionsRejectsMalformedCurrentView(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*DomainGraphAssertionsResponse)
		want   string
	}{
		{name: "duplicate", mutate: func(s *DomainGraphAssertionsResponse) {
			s.Items = append(s.Items, s.Items[0])
		}, want: "duplicate id"},
		{name: "missing staging id", mutate: func(s *DomainGraphAssertionsResponse) {
			s.Items[0].StagingID = ""
		}, want: "missing staging_id"},
		{name: "missing domain", mutate: func(s *DomainGraphAssertionsResponse) {
			s.Items[0].Domain = ""
		}, want: "missing domain"},
		{name: "missing entity type", mutate: func(s *DomainGraphAssertionsResponse) {
			s.Items[0].EntityType = ""
		}, want: "missing entity_type"},
		{name: "missing source id", mutate: func(s *DomainGraphAssertionsResponse) {
			s.Items[0].SourceID = ""
		}, want: "missing source_id"},
		{name: "missing raw hash", mutate: func(s *DomainGraphAssertionsResponse) {
			s.Items[0].RawHash = ""
		}, want: "missing raw_hash"},
		{name: "invalid validation status", mutate: func(s *DomainGraphAssertionsResponse) {
			s.Items[0].ValidationStatus = "approved"
		}, want: "invalid validation_status"},
		{name: "confidence out of range", mutate: func(s *DomainGraphAssertionsResponse) {
			s.Items[0].Confidence = 1.5
		}, want: "confidence out of range"},
		{name: "missing evidence", mutate: func(s *DomainGraphAssertionsResponse) {
			s.Items[0].Evidence = nil
		}, want: "missing evidence"},
		{name: "invalid created at", mutate: func(s *DomainGraphAssertionsResponse) {
			s.Items[0].CreatedAt = "not-a-time"
		}, want: "invalid created_at"},
		{name: "invalid updated at", mutate: func(s *DomainGraphAssertionsResponse) {
			s.Items[0].UpdatedAt = "not-a-time"
		}, want: "invalid updated_at"},
		{name: "negative total", mutate: func(s *DomainGraphAssertionsResponse) {
			s.Total = -1
		}, want: "total must be >= 0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := validDomainGraphAssertionsResponse()
			tt.mutate(&resp)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/viewer/domain-graph/assertions" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.DomainGraphAssertions(context.Background(), DomainGraphAssertionsRequest{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("DomainGraphAssertions() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func validDomainGraphAssertionsResponse() DomainGraphAssertionsResponse {
	now := "2026-06-06T10:00:00Z"
	return DomainGraphAssertionsResponse{
		Items: []DomainGraphAssertion{{
			ID:               "dg:movie:evt:hash",
			StagingID:        "kb:movie:evt:hash",
			Domain:           "movie",
			EntityType:       "work",
			EntityID:         "movie:1",
			RelationType:     "performed_by",
			SourceID:         "web:eiga",
			SourceURL:        "https://example.com/movie/1",
			RawHash:          "hash",
			Summary:          "summary",
			Confidence:       0.8,
			ValidationStatus: "validated",
			Evidence:         map[string]any{"staging_id": "kb:movie:evt:hash"},
			CreatedAt:        now,
			UpdatedAt:        now,
		}},
		Limit:  50,
		Offset: 0,
		Total:  1,
	}
}

func TestSourceRegistryValidateAndPromoteRejectMalformedResponse(t *testing.T) {
	promotedAt := "2026-05-20T06:10:00Z"
	tests := []struct {
		name    string
		handler func(http.ResponseWriter)
		call    func(*Client) error
		want    string
	}{
		{
			name: "validation mismatch",
			handler: func(w http.ResponseWriter) {
				_ = json.NewEncoder(w).Encode(SourceRegistryValidationResponse{Result: SourceRegistryValidationResult{ItemID: "other", Passed: true, Status: "validated"}})
			},
			call: func(c *Client) error {
				_, err := c.ValidateSourceRegistryStaging(context.Background(), SourceRegistryValidateRequest{ID: "stg_1"})
				return err
			},
			want: "item_id mismatch",
		},
		{
			name: "validation failed without issues",
			handler: func(w http.ResponseWriter) {
				_ = json.NewEncoder(w).Encode(SourceRegistryValidationResponse{Result: SourceRegistryValidationResult{ItemID: "stg_1", Status: "rejected"}})
			},
			call: func(c *Client) error {
				_, err := c.ValidateSourceRegistryStaging(context.Background(), SourceRegistryValidateRequest{ID: "stg_1"})
				return err
			},
			want: "failed without issues",
		},
		{
			name: "validation validated without passed",
			handler: func(w http.ResponseWriter) {
				_ = json.NewEncoder(w).Encode(SourceRegistryValidationResponse{Result: SourceRegistryValidationResult{ItemID: "stg_1", Status: "validated"}})
			},
			call: func(c *Client) error {
				_, err := c.ValidateSourceRegistryStaging(context.Background(), SourceRegistryValidateRequest{ID: "stg_1"})
				return err
			},
			want: "validated status without passed",
		},
		{
			name: "validation validated with issues",
			handler: func(w http.ResponseWriter) {
				_ = json.NewEncoder(w).Encode(SourceRegistryValidationResponse{Result: SourceRegistryValidationResult{
					ItemID: "stg_1",
					Passed: true,
					Status: "validated",
					Issues: []SourceRegistryValidationIssue{{Code: "policy", Message: "policy required"}},
				}})
			},
			call: func(c *Client) error {
				_, err := c.ValidateSourceRegistryStaging(context.Background(), SourceRegistryValidateRequest{ID: "stg_1"})
				return err
			},
			want: "passed with issues",
		},
		{
			name: "validation pending terminal response",
			handler: func(w http.ResponseWriter) {
				_ = json.NewEncoder(w).Encode(SourceRegistryValidationResponse{Result: SourceRegistryValidationResult{
					ItemID: "stg_1",
					Status: "pending",
					Issues: []SourceRegistryValidationIssue{{Code: "policy", Message: "policy required"}},
				}})
			},
			call: func(c *Client) error {
				_, err := c.ValidateSourceRegistryStaging(context.Background(), SourceRegistryValidateRequest{ID: "stg_1"})
				return err
			},
			want: "non-terminal status",
		},
		{
			name: "validation rejected with passed",
			handler: func(w http.ResponseWriter) {
				_ = json.NewEncoder(w).Encode(SourceRegistryValidationResponse{Result: SourceRegistryValidationResult{
					ItemID: "stg_1",
					Passed: true,
					Status: "rejected",
				}})
			},
			call: func(c *Client) error {
				_, err := c.ValidateSourceRegistryStaging(context.Background(), SourceRegistryValidateRequest{ID: "stg_1"})
				return err
			},
			want: "passed without validated status",
		},
		{
			name: "validation unknown status",
			handler: func(w http.ResponseWriter) {
				_ = json.NewEncoder(w).Encode(SourceRegistryValidationResponse{Result: SourceRegistryValidationResult{
					ItemID: "stg_1",
					Status: "approved",
					Issues: []SourceRegistryValidationIssue{{Code: "policy", Message: "policy required"}},
				}})
			},
			call: func(c *Client) error {
				_, err := c.ValidateSourceRegistryStaging(context.Background(), SourceRegistryValidateRequest{ID: "stg_1"})
				return err
			},
			want: "invalid status",
		},
		{
			name: "promotion target mismatch",
			handler: func(w http.ResponseWriter) {
				_ = json.NewEncoder(w).Encode(SourceRegistryPromotionResponse{Target: "knowledge", Item: map[string]any{"StagingID": "stg_1", "Domain": "ai"}})
			},
			call: func(c *Client) error {
				_, err := c.PromoteSourceRegistryStaging(context.Background(), SourceRegistryPromoteRequest{ID: "stg_1", Target: "news", Category: "ai"})
				return err
			},
			want: "target mismatch",
		},
		{
			name: "promotion item mismatch",
			handler: func(w http.ResponseWriter) {
				_ = json.NewEncoder(w).Encode(SourceRegistryPromotionResponse{Target: "news", Item: map[string]any{"ID": "news_1", "StagingID": "other", "Category": "ai", "CreatedAt": promotedAt}})
			},
			call: func(c *Client) error {
				_, err := c.PromoteSourceRegistryStaging(context.Background(), SourceRegistryPromoteRequest{ID: "stg_1", Target: "news", Category: "ai"})
				return err
			},
			want: "staging_id mismatch",
		},
		{
			name: "promotion news item missing created at",
			handler: func(w http.ResponseWriter) {
				_ = json.NewEncoder(w).Encode(SourceRegistryPromotionResponse{Target: "news", Item: map[string]any{"ID": "news_1", "StagingID": "stg_1", "Category": "ai"}})
			},
			call: func(c *Client) error {
				_, err := c.PromoteSourceRegistryStaging(context.Background(), SourceRegistryPromoteRequest{ID: "stg_1", Target: "news", Category: "ai"})
				return err
			},
			want: "missing created_at",
		},
		{
			name: "promotion news item missing id",
			handler: func(w http.ResponseWriter) {
				_ = json.NewEncoder(w).Encode(SourceRegistryPromotionResponse{Target: "news", Item: map[string]any{"StagingID": "stg_1", "Category": "ai", "CreatedAt": promotedAt}})
			},
			call: func(c *Client) error {
				_, err := c.PromoteSourceRegistryStaging(context.Background(), SourceRegistryPromoteRequest{ID: "stg_1", Target: "news", Category: "ai"})
				return err
			},
			want: "missing id",
		},
		{
			name: "promotion knowledge item missing id",
			handler: func(w http.ResponseWriter) {
				_ = json.NewEncoder(w).Encode(SourceRegistryPromotionResponse{Target: "knowledge", Item: map[string]any{"StagingID": "stg_1", "Domain": "ai", "CreatedAt": promotedAt}})
			},
			call: func(c *Client) error {
				_, err := c.PromoteSourceRegistryStaging(context.Background(), SourceRegistryPromoteRequest{ID: "stg_1", Target: "knowledge", Domain: "ai"})
				return err
			},
			want: "missing id",
		},
		{
			name: "promotion memory item missing id",
			handler: func(w http.ResponseWriter) {
				_ = json.NewEncoder(w).Encode(SourceRegistryPromotionResponse{Target: "memory", Item: map[string]any{"Namespace": "kb:ai", "CreatedAt": promotedAt, "Meta": map[string]any{"staging_id": "stg_1"}}})
			},
			call: func(c *Client) error {
				_, err := c.PromoteSourceRegistryStaging(context.Background(), SourceRegistryPromoteRequest{ID: "stg_1", Target: "memory", TargetNamespace: "kb:ai"})
				return err
			},
			want: "missing id",
		},
		{
			name: "promotion knowledge item missing created at",
			handler: func(w http.ResponseWriter) {
				_ = json.NewEncoder(w).Encode(SourceRegistryPromotionResponse{Target: "knowledge", Item: map[string]any{"ID": "kb_1", "StagingID": "stg_1", "Domain": "ai"}})
			},
			call: func(c *Client) error {
				_, err := c.PromoteSourceRegistryStaging(context.Background(), SourceRegistryPromoteRequest{ID: "stg_1", Target: "knowledge", Domain: "ai"})
				return err
			},
			want: "missing created_at",
		},
		{
			name: "promotion memory item missing created at",
			handler: func(w http.ResponseWriter) {
				_ = json.NewEncoder(w).Encode(SourceRegistryPromotionResponse{Target: "memory", Item: map[string]any{"ID": "mem_1", "Namespace": "kb:ai", "Meta": map[string]any{"staging_id": "stg_1"}}})
			},
			call: func(c *Client) error {
				_, err := c.PromoteSourceRegistryStaging(context.Background(), SourceRegistryPromoteRequest{ID: "stg_1", Target: "memory", TargetNamespace: "kb:ai"})
				return err
			},
			want: "missing created_at",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/viewer/source-registry" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				tt.handler(w)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			err = tt.call(client)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("call error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestBrowserTraceAPIStatusDiscoverAndFetcherProposal(t *testing.T) {
	var paths []string
	now := time.Date(2026, 5, 20, 3, 45, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.String())
		switch r.URL.Path {
		case "/viewer/browser-trace-api":
			if r.Method != http.MethodGet {
				t.Fatalf("method=%s", r.Method)
			}
			_ = json.NewEncoder(w).Encode(BrowserTraceAPIStatus{
				TraceRuns:     []BrowserTraceRun{{TraceRunID: "trace_1", TracePath: "traces/trace_1", CreatedAt: now}},
				APICandidates: []BrowserTraceAPICandidate{{CandidateID: "api_cand_1", TraceRunID: "trace_1", Method: "GET", ObservedURL: "https://example.com/api/items", ContainsPersonalData: "none", Status: "candidate", CreatedAt: now}},
			})
		case "/viewer/browser-trace-api/discover":
			if r.Method != http.MethodPost {
				t.Fatalf("method=%s", r.Method)
			}
			var req BrowserTraceAPIDiscoverRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(BrowserTraceAPIDiscoverResponse{
				TraceRun:       BrowserTraceRun{TraceRunID: req.TraceRunID, TracePath: req.TracePath, CreatedAt: now},
				APICandidates:  []BrowserTraceAPICandidate{{CandidateID: "api_cand_1", TraceRunID: req.TraceRunID, Method: "GET", ObservedURL: "https://example.com/api/items", ContainsPersonalData: "none", Status: "candidate", CreatedAt: now}},
				APISchemas:     []BrowserTraceAPISchema{{SchemaID: "schema_1", CandidateID: "api_cand_1", SchemaType: "response", SchemaJSON: `{"type":"object"}`, SampleCount: 1, CreatedAt: now}},
				APIValidations: []BrowserTraceAPIValidation{{ValidationID: "val_1", CandidateID: "api_cand_1", TraceRunID: req.TraceRunID, Passed: false, Status: "needs_review", Issues: []BrowserTraceAPIValidationIssue{{Code: "official_api_unverified", Message: "needs review"}}, CreatedAt: now}},
				CoverageReport: BrowserTraceAPICoverage{ReportID: "coverage_1", TraceRunID: req.TraceRunID, CreatedAt: now},
				APIArtifacts:   []BrowserTraceAPIArtifact{{ArtifactID: "art_1", TraceRunID: req.TraceRunID, Type: "fetcher_plan", Title: "Fetcher plan", Status: "pending_review", Content: "review only", CreatedAt: now}},
			})
		case "/viewer/browser-trace-api/validations":
			if r.Method != http.MethodPost {
				t.Fatalf("method=%s", r.Method)
			}
			var req BrowserTraceAPIValidationReviewRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(BrowserTraceAPIValidationReviewResponse{
				Candidate:           BrowserTraceAPICandidate{CandidateID: req.CandidateID, TraceRunID: "trace_1", Method: "GET", ObservedURL: "https://example.com/api/items", ContainsPersonalData: "unknown", Status: "candidate", CreatedAt: now},
				Validation:          BrowserTraceAPIValidation{ValidationID: "val_review_1", CandidateID: req.CandidateID, TraceRunID: "trace_1", Passed: true, Status: "validated", CreatedAt: now},
				OfficialPromotion:   false,
				ImplementationApply: false,
			})
		case "/viewer/browser-trace-api/fetcher-proposals":
			if r.Method != http.MethodPost {
				t.Fatalf("method=%s", r.Method)
			}
			var req BrowserTraceAPIFetcherProposalRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(BrowserTraceAPIFetcherProposalResponse{
				APIArtifact:         BrowserTraceAPIArtifact{ArtifactID: "art_fetcher_proposal_api_cand_1", TraceRunID: "trace_1", WorkstreamID: req.WorkstreamID, Type: "fetcher_proposal", Title: "Fetcher Proposal", Status: "pending_review", Content: "no direct promoted DB write", CreatedAt: now},
				WorkstreamArtifact:  &WorkstreamArtifact{ArtifactID: "art_fetcher_proposal_api_cand_1", WorkstreamID: req.WorkstreamID, Type: "browser_trace_fetcher_proposal", Status: "pending_review", CreatedAt: now},
				Candidate:           BrowserTraceAPICandidate{CandidateID: req.CandidateID, TraceRunID: "trace_1", Method: "GET", ObservedURL: "https://example.com/api/items", ContainsPersonalData: "none", Status: "candidate", CreatedAt: now},
				Validation:          BrowserTraceAPIValidation{ValidationID: "val_1", CandidateID: req.CandidateID, TraceRunID: "trace_1", Passed: true, Status: "validated", CreatedAt: now},
				OfficialPromotion:   false,
				ImplementationApply: false,
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.BrowserTraceAPIStatus(context.Background(), 5)
	if err != nil {
		t.Fatalf("BrowserTraceAPIStatus() error = %v", err)
	}
	if len(status.TraceRuns) != 1 || len(status.APICandidates) != 1 {
		t.Fatalf("status=%#v", status)
	}
	discovered, err := client.DiscoverBrowserTraceAPI(context.Background(), BrowserTraceAPIDiscoverRequest{
		TraceRunID:    "trace_1",
		TracePath:     "traces/trace_1",
		RequestsPath:  "traces/requests.jsonl",
		ResponsesPath: "traces/responses.jsonl",
	})
	if err != nil {
		t.Fatalf("DiscoverBrowserTraceAPI() error = %v", err)
	}
	if discovered.TraceRun.TraceRunID != "trace_1" || len(discovered.APIArtifacts) != 1 {
		t.Fatalf("discovered=%#v", discovered)
	}
	review, err := client.ValidateBrowserTraceAPICandidate(context.Background(), BrowserTraceAPIValidationReviewRequest{
		CandidateID:         "api_cand_1",
		Reviewer:            "client-test",
		HumanApproved:       true,
		TermsReviewed:       true,
		OfficialAPIReviewed: true,
		PIIReviewed:         true,
		SchemaReviewed:      true,
		RiskReviewed:        true,
	})
	if err != nil {
		t.Fatalf("ValidateBrowserTraceAPICandidate() error = %v", err)
	}
	if !review.Validation.Passed || review.Validation.Status != "validated" {
		t.Fatalf("review=%#v", review)
	}
	proposal, err := client.CreateBrowserTraceAPIFetcherProposal(context.Background(), BrowserTraceAPIFetcherProposalRequest{
		CandidateID:   "api_cand_1",
		WorkstreamID:  "ws_1",
		HumanApproved: true,
	})
	if err != nil {
		t.Fatalf("CreateBrowserTraceAPIFetcherProposal() error = %v", err)
	}
	if proposal.OfficialPromotion || proposal.ImplementationApply || proposal.APIArtifact.Status != "pending_review" {
		t.Fatalf("proposal=%#v", proposal)
	}
	if len(paths) != 4 {
		t.Fatalf("paths=%#v", paths)
	}
}

func TestBrowserTraceAPIStatusRejectsMalformedCurrentView(t *testing.T) {
	now := time.Date(2026, 5, 20, 3, 45, 0, 0, time.UTC)
	tests := []struct {
		name string
		resp BrowserTraceAPIStatus
		want string
	}{
		{name: "duplicate run", resp: BrowserTraceAPIStatus{TraceRuns: []BrowserTraceRun{{TraceRunID: "trace_1", TracePath: "traces/1", CreatedAt: now}, {TraceRunID: "trace_1", TracePath: "traces/2", CreatedAt: now.Add(time.Second)}}}, want: "duplicate trace_run_id"},
		{name: "run missing created at", resp: BrowserTraceAPIStatus{TraceRuns: []BrowserTraceRun{{TraceRunID: "trace_1", TracePath: "traces/1"}}}, want: "trace_run missing created_at"},
		{name: "write method candidate", resp: BrowserTraceAPIStatus{APICandidates: []BrowserTraceAPICandidate{{CandidateID: "api_1", TraceRunID: "trace_1", Method: "DELETE", ObservedURL: "https://example.com", ContainsPersonalData: "none", Status: "candidate", CreatedAt: now}}}, want: "write method"},
		{name: "candidate unknown status", resp: BrowserTraceAPIStatus{APICandidates: []BrowserTraceAPICandidate{{CandidateID: "api_1", TraceRunID: "trace_1", Method: "GET", ObservedURL: "https://example.com", ContainsPersonalData: "none", Status: "promoted", CreatedAt: now}}}, want: "candidate status"},
		{name: "candidate confidence out of range", resp: BrowserTraceAPIStatus{APICandidates: []BrowserTraceAPICandidate{{CandidateID: "api_1", TraceRunID: "trace_1", Method: "GET", ObservedURL: "https://example.com", ContainsPersonalData: "none", Status: "candidate", Confidence: 1.2, CreatedAt: now}}}, want: "candidate confidence out of range"},
		{name: "candidate missing created at", resp: BrowserTraceAPIStatus{APICandidates: []BrowserTraceAPICandidate{{CandidateID: "api_1", TraceRunID: "trace_1", Method: "GET", ObservedURL: "https://example.com", ContainsPersonalData: "none", Status: "candidate"}}}, want: "candidate missing created_at"},
		{name: "schema negative sample count", resp: BrowserTraceAPIStatus{APISchemas: []BrowserTraceAPISchema{{SchemaID: "schema_1", CandidateID: "api_1", SchemaType: "response", SchemaJSON: `{"type":"object"}`, SampleCount: -1, CreatedAt: now}}}, want: "sample_count"},
		{name: "schema invalid json", resp: BrowserTraceAPIStatus{APISchemas: []BrowserTraceAPISchema{{SchemaID: "schema_1", CandidateID: "api_1", SchemaType: "response", SchemaJSON: `{"type":`, SampleCount: 1, CreatedAt: now}}}, want: "valid json"},
		{name: "schema confidence out of range", resp: BrowserTraceAPIStatus{APISchemas: []BrowserTraceAPISchema{{SchemaID: "schema_1", CandidateID: "api_1", SchemaType: "response", SchemaJSON: `{"type":"object"}`, SampleCount: 1, Confidence: -0.1, CreatedAt: now}}}, want: "schema confidence out of range"},
		{name: "schema missing created at", resp: BrowserTraceAPIStatus{APISchemas: []BrowserTraceAPISchema{{SchemaID: "schema_1", CandidateID: "api_1", SchemaType: "response", SchemaJSON: `{"type":"object"}`, SampleCount: 1}}}, want: "schema missing created_at"},
		{name: "validation failed without issues", resp: BrowserTraceAPIStatus{APIValidations: []BrowserTraceAPIValidation{{ValidationID: "val_1", CandidateID: "api_1", TraceRunID: "trace_1", Status: "needs_review", CreatedAt: now}}}, want: "failed without issues"},
		{name: "validation unknown status", resp: BrowserTraceAPIStatus{APIValidations: []BrowserTraceAPIValidation{{ValidationID: "val_1", CandidateID: "api_1", TraceRunID: "trace_1", Status: "approved", Passed: false, Issues: []BrowserTraceAPIValidationIssue{{Code: "terms", Message: "terms required"}}, CreatedAt: now}}}, want: "validation status"},
		{name: "validated without passed", resp: BrowserTraceAPIStatus{APIValidations: []BrowserTraceAPIValidation{{ValidationID: "val_1", CandidateID: "api_1", TraceRunID: "trace_1", Status: "validated", CreatedAt: now}}}, want: "validated status without passed"},
		{name: "validated with issues", resp: BrowserTraceAPIStatus{APIValidations: []BrowserTraceAPIValidation{{ValidationID: "val_1", CandidateID: "api_1", TraceRunID: "trace_1", Status: "validated", Passed: true, Issues: []BrowserTraceAPIValidationIssue{{Code: "terms", Message: "terms required"}}, CreatedAt: now}}}, want: "validated status with issues"},
		{name: "needs review with passed", resp: BrowserTraceAPIStatus{APIValidations: []BrowserTraceAPIValidation{{ValidationID: "val_1", CandidateID: "api_1", TraceRunID: "trace_1", Status: "needs_review", Passed: true, Issues: []BrowserTraceAPIValidationIssue{{Code: "terms", Message: "terms required"}}, CreatedAt: now}}}, want: "passed without validated status"},
		{name: "validation missing created at", resp: BrowserTraceAPIStatus{APIValidations: []BrowserTraceAPIValidation{{ValidationID: "val_1", CandidateID: "api_1", TraceRunID: "trace_1", Status: "needs_review", Issues: []BrowserTraceAPIValidationIssue{{Code: "terms", Message: "terms required"}}}}}, want: "validation missing created_at"},
		{name: "coverage missing created at", resp: BrowserTraceAPIStatus{CoverageReports: []BrowserTraceAPICoverage{{ReportID: "coverage_1", TraceRunID: "trace_1"}}}, want: "coverage missing created_at"},
		{name: "artifact unknown status", resp: BrowserTraceAPIStatus{APIArtifacts: []BrowserTraceAPIArtifact{{ArtifactID: "art_1", TraceRunID: "trace_1", Type: "fetcher_plan", Title: "Plan", Status: "promoted", Content: "review only", CreatedAt: now}}}, want: "artifact status"},
		{name: "artifact missing content", resp: BrowserTraceAPIStatus{APIArtifacts: []BrowserTraceAPIArtifact{{ArtifactID: "art_1", TraceRunID: "trace_1", Type: "fetcher_plan", Title: "Plan", Status: "pending_review", CreatedAt: now}}}, want: "missing content"},
		{name: "artifact missing created at", resp: BrowserTraceAPIStatus{APIArtifacts: []BrowserTraceAPIArtifact{{ArtifactID: "art_1", TraceRunID: "trace_1", Type: "fetcher_plan", Title: "Plan", Status: "pending_review", Content: "review only"}}}, want: "artifact missing created_at"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/viewer/browser-trace-api" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(tt.resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.BrowserTraceAPIStatus(context.Background(), 0)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("BrowserTraceAPIStatus() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestBrowserTraceAPIDiscoverAndFetcherProposalRejectInvalidOrMalformed(t *testing.T) {
	now := time.Date(2026, 5, 20, 3, 45, 0, 0, time.UTC)
	client, called, cleanup := newNoRequestClient(t)
	defer cleanup()
	_, err := client.DiscoverBrowserTraceAPI(context.Background(), BrowserTraceAPIDiscoverRequest{TraceRunID: "trace_1"})
	if err == nil || !strings.Contains(err.Error(), "missing trace_path") {
		t.Fatalf("DiscoverBrowserTraceAPI() error = %v, want missing trace_path", err)
	}
	_, err = client.CreateBrowserTraceAPIFetcherProposal(context.Background(), BrowserTraceAPIFetcherProposalRequest{CandidateID: "api_1"})
	if err == nil || !strings.Contains(err.Error(), "human_approved") {
		t.Fatalf("CreateBrowserTraceAPIFetcherProposal() error = %v, want human_approved", err)
	}
	_, err = client.ValidateBrowserTraceAPICandidate(context.Background(), BrowserTraceAPIValidationReviewRequest{CandidateID: "api_1"})
	if err == nil || !strings.Contains(err.Error(), "missing reviewer") {
		t.Fatalf("ValidateBrowserTraceAPICandidate() error = %v, want missing reviewer", err)
	}
	if *called {
		t.Fatal("server was called for invalid request")
	}

	tests := []struct {
		name string
		path string
		resp any
		call func(*Client) error
		want string
	}{
		{
			name: "discover trace mismatch",
			path: "/viewer/browser-trace-api/discover",
			resp: BrowserTraceAPIDiscoverResponse{TraceRun: BrowserTraceRun{TraceRunID: "other", TracePath: "traces/trace_1"}},
			call: func(c *Client) error {
				_, err := c.DiscoverBrowserTraceAPI(context.Background(), BrowserTraceAPIDiscoverRequest{TraceRunID: "trace_1", TracePath: "traces/trace_1", RequestsPath: "requests.jsonl", ResponsesPath: "responses.jsonl"})
				return err
			},
			want: "trace_run_id mismatch",
		},
		{
			name: "proposal applies implementation",
			path: "/viewer/browser-trace-api/fetcher-proposals",
			resp: BrowserTraceAPIFetcherProposalResponse{
				APIArtifact:         BrowserTraceAPIArtifact{ArtifactID: "art_1", TraceRunID: "trace_1", Type: "fetcher_proposal", Title: "Proposal", Status: "pending_review", Content: "review only"},
				Candidate:           BrowserTraceAPICandidate{CandidateID: "api_1", TraceRunID: "trace_1", Method: "GET", ObservedURL: "https://example.com", ContainsPersonalData: "none", Status: "candidate"},
				Validation:          BrowserTraceAPIValidation{ValidationID: "val_1", CandidateID: "api_1", TraceRunID: "trace_1", Passed: true, Status: "validated"},
				ImplementationApply: true,
			},
			call: func(c *Client) error {
				_, err := c.CreateBrowserTraceAPIFetcherProposal(context.Background(), BrowserTraceAPIFetcherProposalRequest{CandidateID: "api_1", HumanApproved: true})
				return err
			},
			want: "implementation_apply",
		},
		{
			name: "validation review applies implementation",
			path: "/viewer/browser-trace-api/validations",
			resp: BrowserTraceAPIValidationReviewResponse{
				Candidate:           BrowserTraceAPICandidate{CandidateID: "api_1", TraceRunID: "trace_1", Method: "GET", ObservedURL: "https://example.com", ContainsPersonalData: "unknown", Status: "candidate"},
				Validation:          BrowserTraceAPIValidation{ValidationID: "val_1", CandidateID: "api_1", TraceRunID: "trace_1", Passed: true, Status: "validated"},
				ImplementationApply: true,
			},
			call: func(c *Client) error {
				_, err := c.ValidateBrowserTraceAPICandidate(context.Background(), BrowserTraceAPIValidationReviewRequest{CandidateID: "api_1", Reviewer: "reviewer"})
				return err
			},
			want: "implementation_apply",
		},
		{
			name: "validation review missing expected validated result",
			path: "/viewer/browser-trace-api/validations",
			resp: BrowserTraceAPIValidationReviewResponse{
				Candidate:  BrowserTraceAPICandidate{CandidateID: "api_1", TraceRunID: "trace_1", Method: "GET", ObservedURL: "https://example.com", ContainsPersonalData: "unknown", Status: "candidate", CreatedAt: now},
				Validation: BrowserTraceAPIValidation{ValidationID: "val_1", CandidateID: "api_1", TraceRunID: "trace_1", Passed: false, Status: "needs_review", Issues: []BrowserTraceAPIValidationIssue{{Code: "terms", Message: "terms required"}}, CreatedAt: now},
			},
			call: func(c *Client) error {
				_, err := c.ValidateBrowserTraceAPICandidate(context.Background(), BrowserTraceAPIValidationReviewRequest{CandidateID: "api_1", Reviewer: "reviewer", HumanApproved: true, TermsReviewed: true, OfficialAPIReviewed: true, PIIReviewed: true, SchemaReviewed: true, RiskReviewed: true})
				return err
			},
			want: "expected validated result",
		},
		{
			name: "proposal workstream artifact missing created at",
			path: "/viewer/browser-trace-api/fetcher-proposals",
			resp: BrowserTraceAPIFetcherProposalResponse{
				APIArtifact:        BrowserTraceAPIArtifact{ArtifactID: "art_1", TraceRunID: "trace_1", WorkstreamID: "ws_1", Type: "fetcher_proposal", Title: "Proposal", Status: "pending_review", Content: "review only", CreatedAt: now},
				WorkstreamArtifact: &WorkstreamArtifact{ArtifactID: "art_1", WorkstreamID: "ws_1", Type: "browser_trace_fetcher_proposal", Status: "pending_review"},
				Candidate:          BrowserTraceAPICandidate{CandidateID: "api_1", TraceRunID: "trace_1", Method: "GET", ObservedURL: "https://example.com", ContainsPersonalData: "none", Status: "candidate", CreatedAt: now},
				Validation:         BrowserTraceAPIValidation{ValidationID: "val_1", CandidateID: "api_1", TraceRunID: "trace_1", Passed: true, Status: "validated", CreatedAt: now},
			},
			call: func(c *Client) error {
				_, err := c.CreateBrowserTraceAPIFetcherProposal(context.Background(), BrowserTraceAPIFetcherProposalRequest{CandidateID: "api_1", WorkstreamID: "ws_1", HumanApproved: true})
				return err
			},
			want: "workstream artifact missing created_at",
		},
		{
			name: "proposal unvalidated",
			path: "/viewer/browser-trace-api/fetcher-proposals",
			resp: BrowserTraceAPIFetcherProposalResponse{
				APIArtifact: BrowserTraceAPIArtifact{ArtifactID: "art_1", TraceRunID: "trace_1", Type: "fetcher_proposal", Title: "Proposal", Status: "pending_review", Content: "review only"},
				Candidate:   BrowserTraceAPICandidate{CandidateID: "api_1", TraceRunID: "trace_1", Method: "GET", ObservedURL: "https://example.com", ContainsPersonalData: "none", Status: "candidate", CreatedAt: now},
				Validation:  BrowserTraceAPIValidation{ValidationID: "val_1", CandidateID: "api_1", TraceRunID: "trace_1", Passed: false, Status: "needs_review", Issues: []BrowserTraceAPIValidationIssue{{Code: "terms_unverified", Message: "terms required"}}},
			},
			call: func(c *Client) error {
				_, err := c.CreateBrowserTraceAPIFetcherProposal(context.Background(), BrowserTraceAPIFetcherProposalRequest{CandidateID: "api_1", HumanApproved: true})
				return err
			},
			want: "requires validated candidate",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != tt.path {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(tt.resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			err = tt.call(client)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("call error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestWorkstreamStatus(t *testing.T) {
	var gotPath string
	now := time.Date(2026, 5, 20, 3, 15, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		if r.Method != http.MethodGet {
			t.Fatalf("method=%s", r.Method)
		}
		_ = json.NewEncoder(w).Encode(WorkstreamStatus{
			Workstreams: []Workstream{{WorkstreamID: "ws_1", Name: "Ops", Status: "active", CreatedAt: now}},
			Goals:       []WorkstreamGoal{{GoalID: "goal_1", WorkstreamID: "ws_1", Title: "Ship", Status: "active", CreatedAt: now}},
			Artifacts:   []WorkstreamArtifact{{ArtifactID: "art_1", WorkstreamID: "ws_1", Type: "note", Status: "draft", CreatedAt: now}},
			Annotations: []WorkstreamAnnotation{{AnnotationID: "ann_1", ArtifactID: "art_1", Comment: "review", Status: "open", CreatedAt: now}},
			Steering:    []WorkstreamSteeringItem{{SteeringID: "steer_1", WorkstreamID: "ws_1", Instruction: "continue", Status: "pending", CreatedAt: now}},
			Heartbeats:  []WorkstreamHeartbeat{{HeartbeatID: "hb_1", WorkstreamID: "ws_1", ScheduleText: "daily", Task: "draft_report", Status: "active", CreatedAt: now}},
			VaultUpdates: []WorkstreamVaultUpdate{{
				UpdateID:     "upd_1",
				WorkstreamID: "ws_1",
				FilePath:     "vault/status.md",
				ReviewStatus: "pending",
				CreatedAt:    now,
			}},
		})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.WorkstreamStatus(context.Background(), 4)
	if err != nil {
		t.Fatalf("WorkstreamStatus() error = %v", err)
	}
	if gotPath != "/viewer/workstreams?limit=4" {
		t.Fatalf("path=%s", gotPath)
	}
	if len(status.Workstreams) != 1 || len(status.VaultUpdates) != 1 {
		t.Fatalf("status=%#v", status)
	}
}

func TestWorkstreamStatusRejectsMalformedCurrentView(t *testing.T) {
	now := time.Date(2026, 5, 20, 3, 15, 0, 0, time.UTC)
	valid := func() WorkstreamStatus {
		return WorkstreamStatus{
			Workstreams: []Workstream{{WorkstreamID: "ws_1", Name: "Ops", Status: "active", CreatedAt: now}},
			Goals:       []WorkstreamGoal{{GoalID: "goal_1", WorkstreamID: "ws_1", Title: "Ship", Status: "active", CreatedAt: now}},
			Artifacts:   []WorkstreamArtifact{{ArtifactID: "art_1", WorkstreamID: "ws_1", Type: "note", Status: "draft", CreatedAt: now}},
			Annotations: []WorkstreamAnnotation{{AnnotationID: "ann_1", ArtifactID: "art_1", Comment: "review", Status: "open", CreatedAt: now}},
			Steering:    []WorkstreamSteeringItem{{SteeringID: "steer_1", WorkstreamID: "ws_1", Instruction: "continue", Status: "pending", CreatedAt: now}},
			Heartbeats:  []WorkstreamHeartbeat{{HeartbeatID: "hb_1", WorkstreamID: "ws_1", ScheduleText: "daily", Task: "draft_report", Status: "active", CreatedAt: now}},
			VaultUpdates: []WorkstreamVaultUpdate{{
				UpdateID:     "upd_1",
				WorkstreamID: "ws_1",
				FilePath:     "vault/status.md",
				ReviewStatus: "pending",
				CreatedAt:    now,
			}},
		}
	}
	tests := []struct {
		name   string
		mutate func(*WorkstreamStatus)
		want   string
	}{
		{name: "duplicate workstream", mutate: func(s *WorkstreamStatus) {
			s.Workstreams = append(s.Workstreams, s.Workstreams[0])
		}, want: "duplicate workstream"},
		{name: "workstream missing created at", mutate: func(s *WorkstreamStatus) {
			s.Workstreams[0].CreatedAt = time.Time{}
		}, want: "workstream ws_1 missing created_at"},
		{name: "missing goal title", mutate: func(s *WorkstreamStatus) {
			s.Goals[0].Title = ""
		}, want: "goal missing title"},
		{name: "completed goal missing completed_at", mutate: func(s *WorkstreamStatus) {
			s.Goals[0].Status = "completed"
		}, want: "completed goal"},
		{name: "goal missing created at", mutate: func(s *WorkstreamStatus) {
			s.Goals[0].CreatedAt = time.Time{}
		}, want: "goal goal_1 missing created_at"},
		{name: "duplicate artifact", mutate: func(s *WorkstreamStatus) {
			s.Artifacts = append(s.Artifacts, s.Artifacts[0])
		}, want: "duplicate artifact"},
		{name: "artifact missing created at", mutate: func(s *WorkstreamStatus) {
			s.Artifacts[0].CreatedAt = time.Time{}
		}, want: "artifact art_1 missing created_at"},
		{name: "missing annotation comment", mutate: func(s *WorkstreamStatus) {
			s.Annotations[0].Comment = ""
		}, want: "annotation missing comment"},
		{name: "annotation missing created at", mutate: func(s *WorkstreamStatus) {
			s.Annotations[0].CreatedAt = time.Time{}
		}, want: "annotation ann_1 missing created_at"},
		{name: "missing steering instruction", mutate: func(s *WorkstreamStatus) {
			s.Steering[0].Instruction = ""
		}, want: "steering missing instruction"},
		{name: "steering missing created at", mutate: func(s *WorkstreamStatus) {
			s.Steering[0].CreatedAt = time.Time{}
		}, want: "steering steer_1 missing created_at"},
		{name: "missing heartbeat task", mutate: func(s *WorkstreamStatus) {
			s.Heartbeats[0].Task = ""
		}, want: "heartbeat missing task"},
		{name: "heartbeat missing created at", mutate: func(s *WorkstreamStatus) {
			s.Heartbeats[0].CreatedAt = time.Time{}
		}, want: "heartbeat hb_1 missing created_at"},
		{name: "duplicate vault update", mutate: func(s *WorkstreamStatus) {
			s.VaultUpdates = append(s.VaultUpdates, s.VaultUpdates[0])
		}, want: "duplicate vault_update"},
		{name: "vault update missing created at", mutate: func(s *WorkstreamStatus) {
			s.VaultUpdates[0].CreatedAt = time.Time{}
		}, want: "vault_update upd_1 missing created_at"},
		{name: "missing vault review status", mutate: func(s *WorkstreamStatus) {
			s.VaultUpdates[0].ReviewStatus = ""
		}, want: "vault_update missing review_status"},
		{name: "invalid vault review status", mutate: func(s *WorkstreamStatus) {
			s.VaultUpdates[0].ReviewStatus = "applied"
		}, want: "invalid vault_update review_status"},
		{name: "applied vault update without approved review", mutate: func(s *WorkstreamStatus) {
			s.VaultUpdates[0].ReviewStatus = "rejected"
			s.VaultUpdates[0].Applied = true
			s.VaultUpdates[0].AppliedPath = "/tmp/vault/status.md"
		}, want: "vault_update applied without approved review"},
		{name: "applied vault update without path", mutate: func(s *WorkstreamStatus) {
			s.VaultUpdates[0].ReviewStatus = "approved"
			s.VaultUpdates[0].Applied = true
		}, want: "vault_update applied without applied_path"},
		{name: "vault update path without applied", mutate: func(s *WorkstreamStatus) {
			s.VaultUpdates[0].ReviewStatus = "approved"
			s.VaultUpdates[0].AppliedPath = "/tmp/vault/status.md"
		}, want: "vault_update has applied_path without applied"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := valid()
			tt.mutate(&resp)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/viewer/workstreams" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.WorkstreamStatus(context.Background(), 0)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("WorkstreamStatus() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestCheckContextBudgetRejectsInvalidRequest(t *testing.T) {
	tests := []struct {
		name  string
		usage ContextUsage
		want  string
	}{
		{name: "missing event", usage: ContextUsage{Agent: "Worker"}, want: "missing event_id"},
		{name: "missing agent", usage: ContextUsage{EventID: "ctx_1"}, want: "missing agent"},
		{name: "negative count", usage: ContextUsage{EventID: "ctx_1", Agent: "Worker", ContextTokens: -1}, want: "counts must be >= 0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, called, cleanup := newNoRequestClient(t)
			defer cleanup()
			_, err := client.CheckContextBudget(context.Background(), tt.usage)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("CheckContextBudget() error = %v, want %q", err, tt.want)
			}
			if *called {
				t.Fatal("server was called for invalid request")
			}
		})
	}
}

func TestCheckContextBudgetRejectsMalformedResponse(t *testing.T) {
	now := time.Date(2026, 5, 20, 5, 45, 0, 0, time.UTC)
	tests := []struct {
		name string
		req  ContextUsage
		resp ContextBudgetResponse
		want string
	}{
		{
			name: "usage mismatch",
			req:  ContextUsage{EventID: "ctx_1", Agent: "Worker", ContextTokens: 10, CreatedAt: now},
			resp: ContextBudgetResponse{
				ContextUsage: ContextUsage{EventID: "other_ctx", Agent: "Worker", ContextTokens: 10, CreatedAt: now},
				Decision:     ContextBudgetDecision{Status: "ok", ContextTokens: 10},
			},
			want: "event_id mismatch",
		},
		{
			name: "invalid status",
			req:  ContextUsage{EventID: "ctx_1", Agent: "Worker", ContextTokens: 10, CreatedAt: now},
			resp: ContextBudgetResponse{
				ContextUsage: ContextUsage{EventID: "ctx_1", Agent: "Worker", ContextTokens: 10, CreatedAt: now},
				Decision:     ContextBudgetDecision{Status: "pass", ContextTokens: 10},
			},
			want: "invalid status",
		},
		{
			name: "warn missing event",
			req:  ContextUsage{EventID: "ctx_1", Agent: "Worker", ContextTokens: 850, CreatedAt: now},
			resp: ContextBudgetResponse{
				ContextUsage: ContextUsage{EventID: "ctx_1", Agent: "Worker", ContextTokens: 850, CreatedAt: now},
				Decision:     ContextBudgetDecision{Status: "warn", ContextTokens: 850},
			},
			want: "missing budget event",
		},
		{
			name: "stop wrong event type",
			req:  ContextUsage{EventID: "ctx_1", Agent: "Worker", ContextTokens: 950, CreatedAt: now},
			resp: ContextBudgetResponse{
				ContextUsage: ContextUsage{EventID: "ctx_1", Agent: "Worker", ContextTokens: 950, CreatedAt: now},
				Decision:     ContextBudgetDecision{Status: "stop", ContextTokens: 950},
				Event:        &WorkflowEvent{EventID: "evt_1", ParentEventID: "ctx_1", EventType: "context_budget_warning", Status: "stop", CreatedAt: now},
			},
			want: "event_type mismatch",
		},
		{
			name: "ok with event",
			req:  ContextUsage{EventID: "ctx_1", Agent: "Worker", ContextTokens: 10, CreatedAt: now},
			resp: ContextBudgetResponse{
				ContextUsage: ContextUsage{EventID: "ctx_1", Agent: "Worker", ContextTokens: 10, CreatedAt: now},
				Decision:     ContextBudgetDecision{Status: "ok", ContextTokens: 10},
				Event:        &WorkflowEvent{EventID: "evt_1", ParentEventID: "ctx_1", EventType: "context_budget_warning", Status: "warn", CreatedAt: now},
			},
			want: "ok should not include",
		},
		{
			name: "usage missing created at",
			req:  ContextUsage{EventID: "ctx_1", Agent: "Worker", ContextTokens: 10, CreatedAt: now},
			resp: ContextBudgetResponse{
				ContextUsage: ContextUsage{EventID: "ctx_1", Agent: "Worker", ContextTokens: 10},
				Decision:     ContextBudgetDecision{Status: "ok", ContextTokens: 10},
			},
			want: "context_usage missing created_at",
		},
		{
			name: "warn event missing created at",
			req:  ContextUsage{EventID: "ctx_1", Agent: "Worker", ContextTokens: 850, CreatedAt: now},
			resp: ContextBudgetResponse{
				ContextUsage: ContextUsage{EventID: "ctx_1", Agent: "Worker", ContextTokens: 850, CreatedAt: now},
				Decision:     ContextBudgetDecision{Status: "warn", ContextTokens: 850},
				Event:        &WorkflowEvent{EventID: "evt_1", ParentEventID: "ctx_1", EventType: "context_budget_warning", Status: "warn"},
			},
			want: "event missing created_at",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/viewer/ai-workflow/context-budget/check" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(tt.resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.CheckContextBudget(context.Background(), tt.req)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("CheckContextBudget() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestPauseAndResumeRun(t *testing.T) {
	paths := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		var req RunStateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.RunID != "run_1" {
			t.Fatalf("payload=%#v", req)
		}
		status := "paused"
		action := "none"
		applied := false
		if r.URL.Path == "/viewer/superagent/runs/resume" {
			status = "running"
			action = "resume_marker_cleared"
			applied = true
		}
		_ = json.NewEncoder(w).Encode(RunStateResponse{RunID: req.RunID, Status: status, EventID: "evt_" + status, RuntimeControlApplied: applied, RuntimeControlAction: action})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	paused, err := client.PauseRun(context.Background(), "run_1", "pause")
	if err != nil {
		t.Fatalf("PauseRun() error = %v", err)
	}
	resumed, err := client.ResumeRun(context.Background(), "run_1", "resume")
	if err != nil {
		t.Fatalf("ResumeRun() error = %v", err)
	}
	if paused.Status != "paused" || resumed.Status != "running" || !resumed.RuntimeControlApplied || resumed.RuntimeControlAction != "resume_marker_cleared" {
		t.Fatalf("statuses paused=%#v resumed=%#v", paused, resumed)
	}
	if len(paths) != 2 || paths[0] != "/viewer/superagent/runs/pause" || paths[1] != "/viewer/superagent/runs/resume" {
		t.Fatalf("paths=%#v", paths)
	}
}

func TestPauseAndResumeRunRejectInvalidRequest(t *testing.T) {
	client, called, cleanup := newNoRequestClient(t)
	defer cleanup()
	if _, err := client.PauseRun(context.Background(), "", "pause"); err == nil || !strings.Contains(err.Error(), "missing run_id") {
		t.Fatalf("PauseRun() error = %v, want missing run_id", err)
	}
	if _, err := client.ResumeRun(context.Background(), " ", "resume"); err == nil || !strings.Contains(err.Error(), "missing run_id") {
		t.Fatalf("ResumeRun() error = %v, want missing run_id", err)
	}
	if *called {
		t.Fatal("server was called for invalid request")
	}
}

func TestPauseRunRejectsMalformedResponse(t *testing.T) {
	tests := []struct {
		name string
		resp RunStateResponse
		want string
	}{
		{
			name: "wrong run id",
			resp: RunStateResponse{RunID: "other", Status: "paused", EventID: "evt_1", RuntimeControlAction: "none"},
			want: "run_id mismatch",
		},
		{
			name: "wrong status",
			resp: RunStateResponse{RunID: "run_1", Status: "running", EventID: "evt_1", RuntimeControlAction: "none"},
			want: "status mismatch",
		},
		{
			name: "missing event",
			resp: RunStateResponse{RunID: "run_1", Status: "paused", RuntimeControlAction: "none"},
			want: "missing event_id",
		},
		{
			name: "applied none",
			resp: RunStateResponse{RunID: "run_1", Status: "paused", EventID: "evt_1", RuntimeControlApplied: true, RuntimeControlAction: "none"},
			want: "action none",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/viewer/superagent/runs/pause" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(tt.resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.PauseRun(context.Background(), "run_1", "pause")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("PauseRun() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestResumeRunRejectsMalformedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/viewer/superagent/runs/resume" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(RunStateResponse{
			RunID:                "run_1",
			Status:               "running",
			EventID:              "evt_1",
			RuntimeControlAction: "cancel_requested",
		})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.ResumeRun(context.Background(), "run_1", "resume")
	if err == nil || !strings.Contains(err.Error(), "runtime_control_action mismatch") {
		t.Fatalf("ResumeRun() error = %v, want runtime_control_action mismatch", err)
	}
}

func TestCreateWorkstreamArtifact(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/viewer/workstreams/artifacts" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req WorkstreamArtifact
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.ArtifactID != "art_1" || req.WorkstreamID != "ws_1" || req.Type != "markdown" {
			t.Fatalf("payload=%#v", req)
		}
		_ = json.NewEncoder(w).Encode(WorkstreamArtifactResponse{Artifact: req})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.CreateWorkstreamArtifact(context.Background(), WorkstreamArtifact{
		ArtifactID:   "art_1",
		WorkstreamID: "ws_1",
		Type:         "markdown",
		FilePath:     "docs/example.md",
		Status:       "draft",
		CreatedAt:    time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("CreateWorkstreamArtifact() error = %v", err)
	}
	if resp.Artifact.ArtifactID != "art_1" || resp.Artifact.WorkstreamID != "ws_1" {
		t.Fatalf("response=%#v", resp)
	}
}

func TestCreateWorkstreamArtifactRejectsInvalidRequest(t *testing.T) {
	tests := []struct {
		name string
		item WorkstreamArtifact
		want string
	}{
		{name: "missing artifact", item: WorkstreamArtifact{WorkstreamID: "ws_1", Type: "markdown"}, want: "missing artifact_id"},
		{name: "missing workstream", item: WorkstreamArtifact{ArtifactID: "art_1", Type: "markdown"}, want: "missing workstream_id"},
		{name: "missing type", item: WorkstreamArtifact{ArtifactID: "art_1", WorkstreamID: "ws_1"}, want: "missing artifact_type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, called, cleanup := newNoRequestClient(t)
			defer cleanup()
			_, err := client.CreateWorkstreamArtifact(context.Background(), tt.item)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("CreateWorkstreamArtifact() error = %v, want %q", err, tt.want)
			}
			if *called {
				t.Fatal("server was called for invalid request")
			}
		})
	}
}

func TestCreateWorkstreamArtifactRejectsMalformedResponse(t *testing.T) {
	tests := []struct {
		name string
		resp WorkstreamArtifactResponse
		want string
	}{
		{
			name: "wrong artifact id",
			resp: WorkstreamArtifactResponse{Artifact: WorkstreamArtifact{ArtifactID: "other", WorkstreamID: "ws_1", Type: "markdown", Status: "draft"}},
			want: "artifact_id mismatch",
		},
		{
			name: "wrong workstream",
			resp: WorkstreamArtifactResponse{Artifact: WorkstreamArtifact{ArtifactID: "art_1", WorkstreamID: "other_ws", Type: "markdown", Status: "draft"}},
			want: "workstream_id mismatch",
		},
		{
			name: "wrong type",
			resp: WorkstreamArtifactResponse{Artifact: WorkstreamArtifact{ArtifactID: "art_1", WorkstreamID: "ws_1", Type: "json", Status: "draft"}},
			want: "artifact_type mismatch",
		},
		{
			name: "wrong default status",
			resp: WorkstreamArtifactResponse{Artifact: WorkstreamArtifact{ArtifactID: "art_1", WorkstreamID: "ws_1", Type: "markdown", Status: "completed"}},
			want: "status mismatch",
		},
		{
			name: "missing created at",
			resp: WorkstreamArtifactResponse{Artifact: WorkstreamArtifact{ArtifactID: "art_1", WorkstreamID: "ws_1", Type: "markdown", Status: "draft"}},
			want: "artifact missing created_at",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/viewer/workstreams/artifacts" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(tt.resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.CreateWorkstreamArtifact(context.Background(), WorkstreamArtifact{
				ArtifactID:   "art_1",
				WorkstreamID: "ws_1",
				Type:         "markdown",
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("CreateWorkstreamArtifact() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestReviewWorkstreamVaultUpdate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/viewer/workstreams/vault-updates/review" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req WorkstreamVaultUpdate
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.UpdateID != "upd_1" || req.WorkstreamID != "ws_1" || req.ReviewStatus != "approved" {
			t.Fatalf("payload=%#v", req)
		}
		_ = json.NewEncoder(w).Encode(WorkstreamVaultUpdateResponse{VaultUpdate: req})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.ReviewWorkstreamVaultUpdate(context.Background(), WorkstreamVaultUpdate{
		UpdateID:     "upd_1",
		WorkstreamID: "ws_1",
		FilePath:     "vault/workstreams/ws_1/STATUS.md",
		ReviewStatus: "approved",
		CreatedAt:    time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("ReviewWorkstreamVaultUpdate() error = %v", err)
	}
	if resp.VaultUpdate.UpdateID != "upd_1" || resp.VaultUpdate.ReviewStatus != "approved" {
		t.Fatalf("response=%#v", resp)
	}
}

func TestReviewWorkstreamVaultUpdateRequiresAppliedEvidenceForApprovedProposedContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/viewer/workstreams/vault-updates/review" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req WorkstreamVaultUpdate
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		req.Applied = true
		req.AppliedPath = "/tmp/vault/ws_1/STATUS.md"
		_ = json.NewEncoder(w).Encode(WorkstreamVaultUpdateResponse{VaultUpdate: req, Applied: true, AppliedPath: req.AppliedPath})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.ReviewWorkstreamVaultUpdate(context.Background(), WorkstreamVaultUpdate{
		UpdateID:        "upd_1",
		WorkstreamID:    "ws_1",
		FilePath:        "vault/workstreams/ws_1/STATUS.md",
		ReviewStatus:    "approved",
		ProposedContent: "# STATUS\n\napproved",
		CreatedAt:       time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("ReviewWorkstreamVaultUpdate() error = %v", err)
	}
	if !resp.Applied || !resp.VaultUpdate.Applied || resp.AppliedPath == "" || resp.VaultUpdate.AppliedPath == "" {
		t.Fatalf("response=%#v, want applied evidence in both top-level and vault_update", resp)
	}
}

func TestCreateWorkstreamVaultUpdate(t *testing.T) {
	now := time.Date(2026, 5, 20, 5, 5, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/viewer/workstreams/vault-updates" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req WorkstreamVaultUpdate
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.UpdateID != "upd_1" || req.WorkstreamID != "ws_1" || req.ReviewStatus != "pending" {
			t.Fatalf("payload=%#v", req)
		}
		req.CreatedAt = now
		_ = json.NewEncoder(w).Encode(WorkstreamVaultUpdateResponse{VaultUpdate: req})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.CreateWorkstreamVaultUpdate(context.Background(), WorkstreamVaultUpdate{
		UpdateID:        "upd_1",
		WorkstreamID:    "ws_1",
		FilePath:        "vault/workstreams/ws_1/STATUS.md",
		ProposedContent: "draft",
	})
	if err != nil {
		t.Fatalf("CreateWorkstreamVaultUpdate() error = %v", err)
	}
	if resp.VaultUpdate.UpdateID != "upd_1" || resp.VaultUpdate.ReviewStatus != "pending" {
		t.Fatalf("response=%#v", resp)
	}
}

func TestPreviewWorkstreamVaultUpdate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/viewer/workstreams/vault-updates/preview" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req WorkstreamVaultUpdate
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.UpdateID != "upd_1" || req.ReviewStatus != "pending" {
			t.Fatalf("payload=%#v", req)
		}
		_ = json.NewEncoder(w).Encode(WorkstreamVaultUpdatePreviewResponse{Preview: WorkstreamVaultUpdatePreview{
			UpdateID:        req.UpdateID,
			FilePath:        req.FilePath,
			ProposedContent: req.ProposedContent,
			CurrentMissing:  true,
			AddedLines:      1,
			UnifiedDiff:     "+draft",
		}})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.PreviewWorkstreamVaultUpdate(context.Background(), WorkstreamVaultUpdate{
		UpdateID:        "upd_1",
		WorkstreamID:    "ws_1",
		FilePath:        "vault/workstreams/ws_1/STATUS.md",
		ReviewStatus:    "pending",
		ProposedContent: "draft",
	})
	if err != nil {
		t.Fatalf("PreviewWorkstreamVaultUpdate() error = %v", err)
	}
	if !resp.Preview.CurrentMissing || resp.Preview.UnifiedDiff == "" {
		t.Fatalf("response=%#v", resp)
	}
}

func TestReviewWorkstreamVaultUpdateRejectsInvalidRequest(t *testing.T) {
	tests := []struct {
		name string
		item WorkstreamVaultUpdate
		want string
	}{
		{name: "missing update", item: WorkstreamVaultUpdate{WorkstreamID: "ws_1", FilePath: "vault/status.md", ReviewStatus: "approved"}, want: "missing update_id"},
		{name: "missing workstream", item: WorkstreamVaultUpdate{UpdateID: "upd_1", FilePath: "vault/status.md", ReviewStatus: "approved"}, want: "missing workstream_id"},
		{name: "missing file", item: WorkstreamVaultUpdate{UpdateID: "upd_1", WorkstreamID: "ws_1", ReviewStatus: "approved"}, want: "missing file_path"},
		{name: "missing status", item: WorkstreamVaultUpdate{UpdateID: "upd_1", WorkstreamID: "ws_1", FilePath: "vault/status.md"}, want: "missing review_status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, called, cleanup := newNoRequestClient(t)
			defer cleanup()
			_, err := client.ReviewWorkstreamVaultUpdate(context.Background(), tt.item)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ReviewWorkstreamVaultUpdate() error = %v, want %q", err, tt.want)
			}
			if *called {
				t.Fatal("server was called for invalid request")
			}
		})
	}
}

func TestReviewWorkstreamVaultUpdateRejectsMalformedResponse(t *testing.T) {
	tests := []struct {
		name string
		resp WorkstreamVaultUpdateResponse
		want string
	}{
		{
			name: "wrong update id",
			resp: WorkstreamVaultUpdateResponse{VaultUpdate: WorkstreamVaultUpdate{UpdateID: "other", WorkstreamID: "ws_1", FilePath: "vault/workstreams/ws_1/STATUS.md", ReviewStatus: "approved"}},
			want: "update_id mismatch",
		},
		{
			name: "wrong workstream",
			resp: WorkstreamVaultUpdateResponse{VaultUpdate: WorkstreamVaultUpdate{UpdateID: "upd_1", WorkstreamID: "other_ws", FilePath: "vault/workstreams/ws_1/STATUS.md", ReviewStatus: "approved"}},
			want: "workstream_id mismatch",
		},
		{
			name: "wrong file path",
			resp: WorkstreamVaultUpdateResponse{VaultUpdate: WorkstreamVaultUpdate{UpdateID: "upd_1", WorkstreamID: "ws_1", FilePath: "other.md", ReviewStatus: "approved"}},
			want: "file_path mismatch",
		},
		{
			name: "wrong status",
			resp: WorkstreamVaultUpdateResponse{VaultUpdate: WorkstreamVaultUpdate{UpdateID: "upd_1", WorkstreamID: "ws_1", FilePath: "vault/workstreams/ws_1/STATUS.md", ReviewStatus: "pending"}},
			want: "status mismatch",
		},
		{
			name: "applied without path",
			resp: WorkstreamVaultUpdateResponse{VaultUpdate: WorkstreamVaultUpdate{UpdateID: "upd_1", WorkstreamID: "ws_1", FilePath: "vault/workstreams/ws_1/STATUS.md", ReviewStatus: "approved"}, Applied: true},
			want: "applied without applied_path",
		},
		{
			name: "path without applied",
			resp: WorkstreamVaultUpdateResponse{VaultUpdate: WorkstreamVaultUpdate{UpdateID: "upd_1", WorkstreamID: "ws_1", FilePath: "vault/workstreams/ws_1/STATUS.md", ReviewStatus: "approved"}, AppliedPath: "/tmp/vault/STATUS.md"},
			want: "applied_path without applied",
		},
		{
			name: "approved proposed content not applied",
			resp: WorkstreamVaultUpdateResponse{VaultUpdate: WorkstreamVaultUpdate{UpdateID: "upd_1", WorkstreamID: "ws_1", FilePath: "vault/workstreams/ws_1/STATUS.md", ReviewStatus: "approved"}},
			want: "did not apply approved proposed_content",
		},
		{
			name: "missing created at",
			resp: WorkstreamVaultUpdateResponse{
				VaultUpdate: WorkstreamVaultUpdate{
					UpdateID:     "upd_1",
					WorkstreamID: "ws_1",
					FilePath:     "vault/workstreams/ws_1/STATUS.md",
					ReviewStatus: "approved",
					Applied:      true,
					AppliedPath:  "/tmp/vault/STATUS.md",
				},
				Applied:     true,
				AppliedPath: "/tmp/vault/STATUS.md",
			},
			want: "vault_update missing created_at",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/viewer/workstreams/vault-updates/review" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(tt.resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.ReviewWorkstreamVaultUpdate(context.Background(), WorkstreamVaultUpdate{
				UpdateID:        "upd_1",
				WorkstreamID:    "ws_1",
				FilePath:        "vault/workstreams/ws_1/STATUS.md",
				ReviewStatus:    "approved",
				ProposedContent: "draft",
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ReviewWorkstreamVaultUpdate() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestCreateWorkstreamVaultUpdateRejectsMalformedResponse(t *testing.T) {
	tests := []struct {
		name string
		resp WorkstreamVaultUpdateResponse
		want string
	}{
		{
			name: "wrong status",
			resp: WorkstreamVaultUpdateResponse{VaultUpdate: WorkstreamVaultUpdate{UpdateID: "upd_1", WorkstreamID: "ws_1", FilePath: "vault/status.md", ReviewStatus: "approved"}},
			want: "status mismatch",
		},
		{
			name: "applied on create",
			resp: WorkstreamVaultUpdateResponse{VaultUpdate: WorkstreamVaultUpdate{UpdateID: "upd_1", WorkstreamID: "ws_1", FilePath: "vault/status.md", ReviewStatus: "pending"}, Applied: true, AppliedPath: "/tmp/vault/status.md"},
			want: "must not apply",
		},
		{
			name: "missing created at",
			resp: WorkstreamVaultUpdateResponse{VaultUpdate: WorkstreamVaultUpdate{UpdateID: "upd_1", WorkstreamID: "ws_1", FilePath: "vault/status.md", ReviewStatus: "pending"}},
			want: "vault_update missing created_at",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/viewer/workstreams/vault-updates" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(tt.resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.CreateWorkstreamVaultUpdate(context.Background(), WorkstreamVaultUpdate{
				UpdateID:     "upd_1",
				WorkstreamID: "ws_1",
				FilePath:     "vault/status.md",
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("CreateWorkstreamVaultUpdate() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestPreviewWorkstreamVaultUpdateRejectsMalformedResponse(t *testing.T) {
	tests := []struct {
		name string
		resp WorkstreamVaultUpdatePreviewResponse
		want string
	}{
		{
			name: "wrong update id",
			resp: WorkstreamVaultUpdatePreviewResponse{Preview: WorkstreamVaultUpdatePreview{UpdateID: "other", FilePath: "vault/status.md", ProposedContent: "draft", UnifiedDiff: "+draft"}},
			want: "update_id mismatch",
		},
		{
			name: "missing diff",
			resp: WorkstreamVaultUpdatePreviewResponse{Preview: WorkstreamVaultUpdatePreview{UpdateID: "upd_1", FilePath: "vault/status.md", ProposedContent: "draft"}},
			want: "missing unified_diff",
		},
		{
			name: "negative line count",
			resp: WorkstreamVaultUpdatePreviewResponse{Preview: WorkstreamVaultUpdatePreview{UpdateID: "upd_1", FilePath: "vault/status.md", ProposedContent: "draft", UnifiedDiff: "+draft", AddedLines: -1}},
			want: "invalid line counts",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/viewer/workstreams/vault-updates/preview" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(tt.resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.PreviewWorkstreamVaultUpdate(context.Background(), WorkstreamVaultUpdate{
				UpdateID:        "upd_1",
				WorkstreamID:    "ws_1",
				FilePath:        "vault/status.md",
				ReviewStatus:    "pending",
				ProposedContent: "draft",
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("PreviewWorkstreamVaultUpdate() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestComplexityStatus(t *testing.T) {
	var gotPath string
	now := time.Date(2026, 5, 20, 3, 55, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		if r.Method != http.MethodGet {
			t.Fatalf("method=%s", r.Method)
		}
		_ = json.NewEncoder(w).Encode(ComplexityStatus{
			Hotspots: []ComplexityHotspot{{HotspotID: "hot_1", ScanID: "scan_1", FilePath: "internal/app.go", HotspotType: "nested_loop", RiskLevel: "medium", Summary: "loop scope contains another loop", CreatedAt: now}},
		})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.ComplexityStatus(context.Background(), 7)
	if err != nil {
		t.Fatalf("ComplexityStatus() error = %v", err)
	}
	if gotPath != "/viewer/complexity-hotspots?limit=7" {
		t.Fatalf("path=%s", gotPath)
	}
	if len(status.Hotspots) != 1 || status.Hotspots[0].HotspotID != "hot_1" {
		t.Fatalf("status=%#v", status)
	}
}

func TestComplexityStatusRejectsMalformedCurrentView(t *testing.T) {
	now := time.Date(2026, 5, 19, 16, 20, 0, 0, time.UTC)
	validScan := func() ComplexityScanEvent {
		return ComplexityScanEvent{ScanID: "scan_1", Repo: "repo", Mode: "report_only", Status: "completed", CreatedAt: now, CompletedAt: now.Add(time.Minute)}
	}
	validHotspot := func() ComplexityHotspot {
		return ComplexityHotspot{HotspotID: "hot_1", ScanID: "scan_1", FilePath: "internal/app.go", HotspotType: "nested_loop", RiskLevel: "medium", Summary: "summary", CreatedAt: now}
	}
	validEvidence := func() ComplexityHotspotEvidence {
		return ComplexityHotspotEvidence{EvidenceID: "ev_1", HotspotID: "hot_1", FilePath: "internal/app.go", Reason: "loop inside nearby loop", CreatedAt: now}
	}
	validReport := func() ComplexityReportArtifact {
		return ComplexityReportArtifact{ArtifactID: "art_1", ScanID: "scan_1", Type: "complexity_patch_proposal", Title: "title", Status: "pending_review", Content: "content", CreatedAt: now}
	}
	tests := []struct {
		name string
		resp ComplexityStatus
		want string
	}{
		{
			name: "duplicate scan",
			resp: ComplexityStatus{Scans: []ComplexityScanEvent{
				validScan(),
				{ScanID: "scan_1", Repo: "repo", Mode: "report_only", Status: "completed", CreatedAt: now.Add(time.Second), CompletedAt: now.Add(time.Minute)},
			}},
			want: "duplicate scan",
		},
		{
			name: "missing scan repo",
			resp: ComplexityStatus{Scans: []ComplexityScanEvent{
				{ScanID: "scan_1", Mode: "report_only", Status: "completed", CreatedAt: now, CompletedAt: now.Add(time.Minute)},
			}},
			want: "missing repo",
		},
		{
			name: "completed scan missing completed_at",
			resp: ComplexityStatus{Scans: []ComplexityScanEvent{
				{ScanID: "scan_1", Repo: "repo", Mode: "report_only", Status: "completed", CreatedAt: now},
			}},
			want: "completed scan",
		},
		{
			name: "scan missing created at",
			resp: ComplexityStatus{Scans: []ComplexityScanEvent{
				{ScanID: "scan_1", Repo: "repo", Mode: "report_only", Status: "running"},
			}},
			want: "scan scan_1 missing created_at",
		},
		{
			name: "negative scan count",
			resp: ComplexityStatus{Scans: []ComplexityScanEvent{
				{ScanID: "scan_1", Repo: "repo", Mode: "report_only", Status: "completed", CreatedAt: now, CompletedAt: now.Add(time.Minute), FilesScanned: -1},
			}},
			want: "counts must be >= 0",
		},
		{
			name: "duplicate hotspot",
			resp: ComplexityStatus{Hotspots: []ComplexityHotspot{
				validHotspot(),
				{HotspotID: "hot_1", ScanID: "scan_1", FilePath: "internal/app.go", HotspotType: "nested_loop", RiskLevel: "medium", Summary: "summary", CreatedAt: now.Add(time.Second)},
			}},
			want: "duplicate hotspot",
		},
		{
			name: "missing hotspot summary",
			resp: ComplexityStatus{Hotspots: []ComplexityHotspot{{HotspotID: "hot_1", ScanID: "scan_1", FilePath: "internal/app.go", HotspotType: "nested_loop", RiskLevel: "medium", CreatedAt: now}}},
			want: "missing summary",
		},
		{
			name: "hotspot confidence out of range",
			resp: ComplexityStatus{Hotspots: []ComplexityHotspot{{HotspotID: "hot_1", ScanID: "scan_1", FilePath: "internal/app.go", HotspotType: "nested_loop", RiskLevel: "medium", Summary: "summary", Confidence: 1.2, CreatedAt: now}}},
			want: "confidence out of range",
		},
		{
			name: "hotspot invalid line range",
			resp: ComplexityStatus{Hotspots: []ComplexityHotspot{{HotspotID: "hot_1", ScanID: "scan_1", FilePath: "internal/app.go", HotspotType: "nested_loop", RiskLevel: "medium", Summary: "summary", LineStart: 20, LineEnd: 10, CreatedAt: now}}},
			want: "line_end must be >= line_start",
		},
		{
			name: "hotspot missing created at",
			resp: ComplexityStatus{Hotspots: []ComplexityHotspot{{HotspotID: "hot_1", ScanID: "scan_1", FilePath: "internal/app.go", HotspotType: "nested_loop", RiskLevel: "medium", Summary: "summary"}}},
			want: "hotspot hot_1 missing created_at",
		},
		{
			name: "duplicate evidence",
			resp: ComplexityStatus{Evidence: []ComplexityHotspotEvidence{
				validEvidence(),
				{EvidenceID: "ev_1", HotspotID: "hot_1", FilePath: "internal/app.go", Reason: "loop inside nearby loop", CreatedAt: now.Add(time.Second)},
			}},
			want: "duplicate evidence",
		},
		{
			name: "missing evidence reason",
			resp: ComplexityStatus{Evidence: []ComplexityHotspotEvidence{{EvidenceID: "ev_1", HotspotID: "hot_1", FilePath: "internal/app.go", CreatedAt: now}}},
			want: "missing reason",
		},
		{
			name: "evidence negative line",
			resp: ComplexityStatus{Evidence: []ComplexityHotspotEvidence{{EvidenceID: "ev_1", HotspotID: "hot_1", FilePath: "internal/app.go", Reason: "loop inside nearby loop", LineStart: -1, CreatedAt: now}}},
			want: "line range must be >= 0",
		},
		{
			name: "evidence missing created at",
			resp: ComplexityStatus{Evidence: []ComplexityHotspotEvidence{{EvidenceID: "ev_1", HotspotID: "hot_1", FilePath: "internal/app.go", Reason: "loop inside nearby loop"}}},
			want: "evidence ev_1 missing created_at",
		},
		{
			name: "duplicate report",
			resp: ComplexityStatus{Reports: []ComplexityReportArtifact{
				validReport(),
				{ArtifactID: "art_1", ScanID: "scan_1", Type: "complexity_patch_proposal", Title: "title", Status: "pending_review", Content: "content", CreatedAt: now.Add(time.Second)},
			}},
			want: "duplicate report",
		},
		{
			name: "missing report content",
			resp: ComplexityStatus{Reports: []ComplexityReportArtifact{{ArtifactID: "art_1", ScanID: "scan_1", Type: "complexity_patch_proposal", Title: "title", Status: "pending_review", CreatedAt: now}}},
			want: "missing content",
		},
		{
			name: "proposal report claims completed",
			resp: ComplexityStatus{Reports: []ComplexityReportArtifact{{ArtifactID: "art_1", ScanID: "scan_1", Type: "complexity_patch_proposal", Title: "title", Status: "completed", Content: "content", CreatedAt: now}}},
			want: "status must be pending_review",
		},
		{
			name: "concrete diff missing patch not applied evidence",
			resp: ComplexityStatus{Reports: []ComplexityReportArtifact{{ArtifactID: "art_1", ScanID: "scan_1", Type: "complexity_concrete_diff_proposal", Title: "title", Status: "pending_review", Content: "Patch applied: `true`\nHuman approval required: `true`", CreatedAt: now}}},
			want: "must not claim patch applied",
		},
		{
			name: "concrete diff missing human approval requirement",
			resp: ComplexityStatus{Reports: []ComplexityReportArtifact{{ArtifactID: "art_1", ScanID: "scan_1", Type: "complexity_concrete_diff_proposal", Title: "title", Status: "pending_review", Content: "Patch applied: `false`", CreatedAt: now}}},
			want: "missing human approval requirement",
		},
		{
			name: "coder diff failure claims pending review",
			resp: ComplexityStatus{Reports: []ComplexityReportArtifact{{ArtifactID: "art_1", ScanID: "scan_1", Type: "complexity_coder_diff_failure", Title: "title", Status: "pending_review", Content: "Patch applied: `false`\nFailure reason: timeout", CreatedAt: now}}},
			want: "failure status must be failed",
		},
		{
			name: "coder diff failure missing patch not applied evidence",
			resp: ComplexityStatus{Reports: []ComplexityReportArtifact{{ArtifactID: "art_1", ScanID: "scan_1", Type: "complexity_coder_diff_failure", Title: "title", Status: "failed", Content: "Failure reason: timeout", CreatedAt: now}}},
			want: "failure must not claim patch applied",
		},
		{
			name: "coder diff failure missing failure reason",
			resp: ComplexityStatus{Reports: []ComplexityReportArtifact{{ArtifactID: "art_1", ScanID: "scan_1", Type: "complexity_coder_diff_failure", Title: "title", Status: "failed", Content: "Patch applied: `false`", CreatedAt: now}}},
			want: "missing failure reason",
		},
		{
			name: "report missing created at",
			resp: ComplexityStatus{Reports: []ComplexityReportArtifact{{ArtifactID: "art_1", ScanID: "scan_1", Type: "complexity_patch_proposal", Title: "title", Status: "pending_review", Content: "content"}}},
			want: "report art_1 missing created_at",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/viewer/complexity-hotspots" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(tt.resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.ComplexityStatus(context.Background(), 0)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ComplexityStatus() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestCreateComplexityConcreteDiff(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/viewer/complexity-hotspots/concrete-diffs" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req ComplexityConcreteDiffRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.HotspotID != "hot_1" || req.ArtifactID != "art_1" || !strings.Contains(req.ConcreteDiff, "diff --git") {
			t.Fatalf("payload=%#v", req)
		}
		_ = json.NewEncoder(w).Encode(complexityDiffResponseFixture("hot_1", "scan_1", "art_1"))
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.CreateComplexityConcreteDiff(context.Background(), ComplexityConcreteDiffRequest{
		HotspotID:    "hot_1",
		ArtifactID:   "art_1",
		ConcreteDiff: "diff --git a/internal/app.go b/internal/app.go\n",
	})
	if err != nil {
		t.Fatalf("CreateComplexityConcreteDiff() error = %v", err)
	}
	if resp.PatchApplied || !resp.HumanApprovalRequired || resp.ConcreteDiffArtifact.Status != "pending_review" {
		t.Fatalf("response=%#v", resp)
	}
}

func TestCreateComplexityConcreteDiffRejectsInvalidRequest(t *testing.T) {
	tests := []struct {
		name string
		req  ComplexityConcreteDiffRequest
		want string
	}{
		{
			name: "missing hotspot id",
			req:  ComplexityConcreteDiffRequest{ConcreteDiff: "diff --git a/internal/app.go b/internal/app.go\n"},
			want: "missing hotspot_id",
		},
		{
			name: "missing concrete diff",
			req:  ComplexityConcreteDiffRequest{HotspotID: "hot_1"},
			want: "missing concrete_diff",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.CreateComplexityConcreteDiff(context.Background(), tt.req)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("CreateComplexityConcreteDiff() error = %v, want %q", err, tt.want)
			}
			if called {
				t.Fatal("server was called for invalid request")
			}
		})
	}
}

func TestCreateComplexityConcreteDiffRejectsMalformedResponse(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ComplexityDiffResponse)
		want   string
	}{
		{
			name: "patch applied",
			mutate: func(resp *ComplexityDiffResponse) {
				resp.PatchApplied = true
			},
			want: "must not claim patch_applied",
		},
		{
			name: "missing human approval",
			mutate: func(resp *ComplexityDiffResponse) {
				resp.HumanApprovalRequired = false
			},
			want: "missing human approval",
		},
		{
			name: "wrong artifact type",
			mutate: func(resp *ComplexityDiffResponse) {
				resp.ConcreteDiffArtifact.Type = "complexity_patch"
			},
			want: "artifact type mismatch",
		},
		{
			name: "completed artifact",
			mutate: func(resp *ComplexityDiffResponse) {
				resp.ConcreteDiffArtifact.Status = "completed"
			},
			want: "pending_review",
		},
		{
			name: "hotspot missing created at",
			mutate: func(resp *ComplexityDiffResponse) {
				resp.Hotspot.CreatedAt = time.Time{}
			},
			want: "hotspot missing created_at",
		},
		{
			name: "artifact missing created at",
			mutate: func(resp *ComplexityDiffResponse) {
				resp.ConcreteDiffArtifact.CreatedAt = time.Time{}
			},
			want: "artifact missing created_at",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := complexityDiffResponseFixture("hot_1", "scan_1", "art_1")
			tt.mutate(&resp)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/viewer/complexity-hotspots/concrete-diffs" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.CreateComplexityConcreteDiff(context.Background(), ComplexityConcreteDiffRequest{
				HotspotID:    "hot_1",
				ArtifactID:   "art_1",
				ConcreteDiff: "diff --git a/internal/app.go b/internal/app.go\n",
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("CreateComplexityConcreteDiff() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestCreateComplexityCoderDiff(t *testing.T) {
	now := time.Date(2026, 5, 20, 6, 5, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/viewer/complexity-hotspots/coder-diffs" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req ComplexityCoderDiffRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.HotspotID != "hot_1" || req.JobID != "job_1" || req.SandboxID != "sandbox_1" {
			t.Fatalf("payload=%#v", req)
		}
		resp := complexityDiffResponseFixture("hot_1", "scan_1", "art_1")
		resp.CoderResult = &ComplexityCoderDiffResult{JobID: "job_1", ConcreteDiff: "diff --git a/internal/app.go b/internal/app.go\n"}
		resp.SandboxPromotion = &PromotionRequest{PromotionID: "promo_1", SandboxID: "sandbox_1", TargetPath: "internal/app.go", DiffPath: "sandbox/diff.patch", CreatedAt: now}
		resp.SandboxDecision = &PromotionGateDecision{Status: "needs_review", Reason: "human approval is required"}
		resp.SandboxGateLog = &PromotionGateLog{EventID: "evt_1", PromotionID: "promo_1", GateStatus: "needs_review", CreatedAt: now}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.CreateComplexityCoderDiff(context.Background(), ComplexityCoderDiffRequest{
		HotspotID:   "hot_1",
		JobID:       "job_1",
		ArtifactID:  "art_1",
		SandboxID:   "sandbox_1",
		PromotionID: "promo_1",
		TargetPath:  "internal/app.go",
		DiffPath:    "sandbox/diff.patch",
	})
	if err != nil {
		t.Fatalf("CreateComplexityCoderDiff() error = %v", err)
	}
	if resp.CoderResult == nil || resp.CoderResult.JobID != "job_1" || resp.SandboxDecision.Status != "needs_review" {
		t.Fatalf("response=%#v", resp)
	}
}

func TestCreateComplexityCoderDiffRejectsInvalidRequest(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CreateComplexityCoderDiff(context.Background(), ComplexityCoderDiffRequest{})
	if err == nil || !strings.Contains(err.Error(), "missing hotspot_id") {
		t.Fatalf("CreateComplexityCoderDiff() error = %v, want missing hotspot_id", err)
	}
	if called {
		t.Fatal("server was called for invalid request")
	}
}

func TestCreateComplexityCoderDiffRejectsMalformedResponse(t *testing.T) {
	now := time.Date(2026, 5, 20, 6, 5, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*ComplexityDiffResponse)
		want   string
	}{
		{
			name: "missing coder result",
			mutate: func(resp *ComplexityDiffResponse) {
				resp.CoderResult = nil
			},
			want: "missing coder_result",
		},
		{
			name: "job mismatch",
			mutate: func(resp *ComplexityDiffResponse) {
				resp.CoderResult.JobID = "other_job"
			},
			want: "job_id mismatch",
		},
		{
			name: "sandbox gate mismatch",
			mutate: func(resp *ComplexityDiffResponse) {
				resp.SandboxGateLog.GateStatus = "approve"
			},
			want: "sandbox gate status mismatch",
		},
		{
			name: "workstream artifact missing created at",
			mutate: func(resp *ComplexityDiffResponse) {
				resp.WorkstreamArtifact = &WorkstreamArtifact{ArtifactID: "ws_art_1", WorkstreamID: "ws_1", Type: "complexity_concrete_diff_review", Status: "pending_review"}
			},
			want: "workstream artifact missing created_at",
		},
		{
			name: "sandbox promotion missing created at",
			mutate: func(resp *ComplexityDiffResponse) {
				resp.SandboxPromotion.CreatedAt = time.Time{}
			},
			want: "sandbox promotion missing created_at",
		},
		{
			name: "sandbox gate log missing created at",
			mutate: func(resp *ComplexityDiffResponse) {
				resp.SandboxGateLog.CreatedAt = time.Time{}
			},
			want: "sandbox gate log missing created_at",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := complexityDiffResponseFixture("hot_1", "scan_1", "art_1")
			resp.CoderResult = &ComplexityCoderDiffResult{JobID: "job_1", ConcreteDiff: "diff --git a/internal/app.go b/internal/app.go\n"}
			resp.SandboxPromotion = &PromotionRequest{PromotionID: "promo_1", SandboxID: "sandbox_1", TargetPath: "internal/app.go", DiffPath: "sandbox/diff.patch", CreatedAt: now}
			resp.SandboxDecision = &PromotionGateDecision{Status: "needs_review", Reason: "human approval is required"}
			resp.SandboxGateLog = &PromotionGateLog{EventID: "evt_1", PromotionID: "promo_1", GateStatus: "needs_review", CreatedAt: now}
			tt.mutate(&resp)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/viewer/complexity-hotspots/coder-diffs" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.CreateComplexityCoderDiff(context.Background(), ComplexityCoderDiffRequest{
				HotspotID:   "hot_1",
				JobID:       "job_1",
				ArtifactID:  "art_1",
				SandboxID:   "sandbox_1",
				PromotionID: "promo_1",
				TargetPath:  "internal/app.go",
				DiffPath:    "sandbox/diff.patch",
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("CreateComplexityCoderDiff() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func complexityDiffResponseFixture(hotspotID string, scanID string, artifactID string) ComplexityDiffResponse {
	now := time.Date(2026, 5, 20, 5, 55, 0, 0, time.UTC)
	return ComplexityDiffResponse{
		Hotspot: ComplexityHotspot{
			HotspotID:   hotspotID,
			ScanID:      scanID,
			FilePath:    "internal/app.go",
			HotspotType: "nested_loop",
			RiskLevel:   "medium",
			Summary:     "nested loop",
			CreatedAt:   now,
		},
		ConcreteDiffArtifact: ComplexityReportArtifact{
			ArtifactID: artifactID,
			ScanID:     scanID,
			Type:       "complexity_concrete_diff_proposal",
			Status:     "pending_review",
			Title:      "Complexity Concrete Diff Proposal",
			Content:    "review only",
			CreatedAt:  now,
		},
		HumanApprovalRequired: true,
		PatchApplied:          false,
	}
}

func TestEvaluateRevenueHumanDecision(t *testing.T) {
	now := time.Date(2026, 5, 20, 4, 50, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/viewer/revenue/human-decision-gate" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req RevenueHumanDecision
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.DecisionID != "dec_1" || req.DecisionType != "high_ticket_offer" || req.ApprovalStatus != "" {
			t.Fatalf("payload=%#v", req)
		}
		_ = json.NewEncoder(w).Encode(RevenueHumanDecisionResponse{
			Decision: req,
			Record: RevenueHumanDecisionRecord{
				DecisionID:       req.DecisionID,
				DecisionType:     req.DecisionType,
				ApprovalStatus:   "pending",
				GateStatus:       "needs_review",
				RequiresApproval: true,
				CreatedAt:        now,
			},
			Result: RevenueHumanDecisionResult{
				Status:           "needs_review",
				RequiresApproval: true,
			},
		})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.EvaluateRevenueHumanDecision(context.Background(), RevenueHumanDecision{
		DecisionID:   "dec_1",
		DecisionType: "high_ticket_offer",
		Description:  "高単価 offer を案内する",
	})
	if err != nil {
		t.Fatalf("EvaluateRevenueHumanDecision() error = %v", err)
	}
	if resp.Result.Status != "needs_review" || !resp.Result.RequiresApproval || resp.Record.DecisionID != "dec_1" {
		t.Fatalf("response=%#v", resp)
	}
}

func TestEvaluateRevenueHumanDecisionRejectsInvalidRequest(t *testing.T) {
	client, called, cleanup := newNoRequestClient(t)
	defer cleanup()
	_, err := client.EvaluateRevenueHumanDecision(context.Background(), RevenueHumanDecision{})
	if err == nil || !strings.Contains(err.Error(), "missing decision_type") {
		t.Fatalf("EvaluateRevenueHumanDecision() error = %v, want missing decision_type", err)
	}
	if *called {
		t.Fatal("server was called for invalid request")
	}
}

func TestEvaluateRevenueHumanDecisionRejectsMalformedResponse(t *testing.T) {
	tests := []struct {
		name string
		resp RevenueHumanDecisionResponse
		want string
	}{
		{
			name: "decision mismatch",
			resp: RevenueHumanDecisionResponse{
				Record: RevenueHumanDecisionRecord{DecisionID: "other", DecisionType: "high_ticket_offer", GateStatus: "needs_review", RequiresApproval: true},
				Result: RevenueHumanDecisionResult{Status: "needs_review", RequiresApproval: true},
			},
			want: "decision_id mismatch",
		},
		{
			name: "status mismatch",
			resp: RevenueHumanDecisionResponse{
				Record: RevenueHumanDecisionRecord{DecisionID: "dec_1", DecisionType: "high_ticket_offer", GateStatus: "approved", RequiresApproval: true},
				Result: RevenueHumanDecisionResult{Status: "needs_review", RequiresApproval: true},
			},
			want: "status mismatch",
		},
		{
			name: "requires mismatch",
			resp: RevenueHumanDecisionResponse{
				Record: RevenueHumanDecisionRecord{DecisionID: "dec_1", DecisionType: "high_ticket_offer", GateStatus: "needs_review", RequiresApproval: true},
				Result: RevenueHumanDecisionResult{Status: "needs_review", RequiresApproval: false},
			},
			want: "requires_approval mismatch",
		},
		{
			name: "blocked without reasons",
			resp: RevenueHumanDecisionResponse{
				Record: RevenueHumanDecisionRecord{DecisionID: "dec_1", DecisionType: "high_ticket_offer", GateStatus: "blocked", RequiresApproval: true},
				Result: RevenueHumanDecisionResult{Status: "blocked", RequiresApproval: true},
			},
			want: "blocked without reasons",
		},
		{
			name: "missing created at",
			resp: RevenueHumanDecisionResponse{
				Record: RevenueHumanDecisionRecord{DecisionID: "dec_1", DecisionType: "high_ticket_offer", ApprovalStatus: "pending", GateStatus: "needs_review", RequiresApproval: true},
				Result: RevenueHumanDecisionResult{Status: "needs_review", RequiresApproval: true},
			},
			want: "record missing created_at",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/viewer/revenue/human-decision-gate" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(tt.resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.EvaluateRevenueHumanDecision(context.Background(), RevenueHumanDecision{
				DecisionID:   "dec_1",
				DecisionType: "high_ticket_offer",
				Description:  "30万円の導入支援を案内する",
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("EvaluateRevenueHumanDecision() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestReviewRevenueHumanDecision(t *testing.T) {
	now := time.Date(2026, 5, 20, 4, 50, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/viewer/revenue/human-decision-gate/review" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req RevenueHumanDecisionReview
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.DecisionID != "dec_1" || req.ApprovalStatus != "approved" {
			t.Fatalf("payload=%#v", req)
		}
		_ = json.NewEncoder(w).Encode(RevenueHumanDecisionResponse{
			Record: RevenueHumanDecisionRecord{
				DecisionID:       req.DecisionID,
				DecisionType:     "external_publish",
				ApprovalStatus:   "approved",
				GateStatus:       "approved",
				RequiresApproval: true,
				CreatedAt:        now,
			},
			Result: RevenueHumanDecisionResult{
				Status:           "approved",
				RequiresApproval: true,
			},
		})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.ReviewRevenueHumanDecision(context.Background(), RevenueHumanDecisionReview{
		DecisionID:     "dec_1",
		ApprovalStatus: "approved",
	})
	if err != nil {
		t.Fatalf("ReviewRevenueHumanDecision() error = %v", err)
	}
	if resp.Result.Status != "approved" || resp.Record.ApprovalStatus != "approved" {
		t.Fatalf("response=%#v", resp)
	}
}

func TestReviewRevenueHumanDecisionRejectsInvalidRequest(t *testing.T) {
	tests := []struct {
		name string
		item RevenueHumanDecisionReview
		want string
	}{
		{name: "missing decision", item: RevenueHumanDecisionReview{ApprovalStatus: "approved"}, want: "missing decision_id"},
		{name: "missing approval", item: RevenueHumanDecisionReview{DecisionID: "dec_1"}, want: "missing approval_status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, called, cleanup := newNoRequestClient(t)
			defer cleanup()
			_, err := client.ReviewRevenueHumanDecision(context.Background(), tt.item)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ReviewRevenueHumanDecision() error = %v, want %q", err, tt.want)
			}
			if *called {
				t.Fatal("server was called for invalid request")
			}
		})
	}
}

func TestReviewRevenueHumanDecisionRejectsMalformedResponse(t *testing.T) {
	tests := []struct {
		name string
		resp RevenueHumanDecisionResponse
		want string
	}{
		{
			name: "wrong decision",
			resp: RevenueHumanDecisionResponse{
				Record: RevenueHumanDecisionRecord{DecisionID: "other", DecisionType: "external_publish", ApprovalStatus: "approved", GateStatus: "approved", RequiresApproval: true},
				Result: RevenueHumanDecisionResult{Status: "approved", RequiresApproval: true},
			},
			want: "decision_id mismatch",
		},
		{
			name: "wrong approval",
			resp: RevenueHumanDecisionResponse{
				Record: RevenueHumanDecisionRecord{DecisionID: "dec_1", DecisionType: "external_publish", ApprovalStatus: "pending", GateStatus: "approved", RequiresApproval: true},
				Result: RevenueHumanDecisionResult{Status: "approved", RequiresApproval: true},
			},
			want: "approval_status mismatch",
		},
		{
			name: "result mismatch",
			resp: RevenueHumanDecisionResponse{
				Record: RevenueHumanDecisionRecord{DecisionID: "dec_1", DecisionType: "external_publish", ApprovalStatus: "approved", GateStatus: "approved", RequiresApproval: true},
				Result: RevenueHumanDecisionResult{Status: "needs_review", RequiresApproval: true},
			},
			want: "status mismatch",
		},
		{
			name: "missing created at",
			resp: RevenueHumanDecisionResponse{
				Record: RevenueHumanDecisionRecord{DecisionID: "dec_1", DecisionType: "external_publish", ApprovalStatus: "approved", GateStatus: "approved", RequiresApproval: true},
				Result: RevenueHumanDecisionResult{Status: "approved", RequiresApproval: true},
			},
			want: "record missing created_at",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/viewer/revenue/human-decision-gate/review" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(tt.resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.ReviewRevenueHumanDecision(context.Background(), RevenueHumanDecisionReview{
				DecisionID:     "dec_1",
				ApprovalStatus: "approved",
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ReviewRevenueHumanDecision() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestCreateRevenueDailyRoutineReport(t *testing.T) {
	now := time.Date(2026, 5, 20, 4, 50, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/viewer/revenue/daily-routine" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req RevenueDailyRoutineRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.ReportID != "daily_1" || req.WorkstreamID != "ws_revenue" || req.Limit != 20 {
			t.Fatalf("payload=%#v", req)
		}
		_ = json.NewEncoder(w).Encode(RevenueDailyRoutineResponse{
			Report: RevenueDailyRoutineReport{
				ReportID:            req.ReportID,
				WorkstreamID:        req.WorkstreamID,
				Date:                "2026-05-18",
				Status:              "draft_report",
				ExternalSendApplied: false,
				MarketResearch:      1,
				CreatedAt:           now,
			},
			ExternalActionsApplied:                  false,
			HumanApprovalRequiredForExternalActions: true,
		})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.CreateRevenueDailyRoutineReport(context.Background(), RevenueDailyRoutineRequest{
		ReportID:     "daily_1",
		WorkstreamID: "ws_revenue",
		Date:         "2026-05-18",
		Limit:        20,
	})
	if err != nil {
		t.Fatalf("CreateRevenueDailyRoutineReport() error = %v", err)
	}
	if resp.Report.Status != "draft_report" || resp.Report.ExternalSendApplied || resp.ExternalActionsApplied || !resp.HumanApprovalRequiredForExternalActions {
		t.Fatalf("response=%#v", resp)
	}
}

func TestCreateRevenueDailyRoutineReportRejectsInvalidRequest(t *testing.T) {
	client, called, cleanup := newNoRequestClient(t)
	defer cleanup()
	_, err := client.CreateRevenueDailyRoutineReport(context.Background(), RevenueDailyRoutineRequest{Limit: -1})
	if err == nil || !strings.Contains(err.Error(), "limit must be >= 0") {
		t.Fatalf("CreateRevenueDailyRoutineReport() error = %v, want limit must be >= 0", err)
	}
	if *called {
		t.Fatal("server was called for invalid request")
	}
}

func TestCreateRevenueDailyRoutineReportRejectsMalformedResponse(t *testing.T) {
	tests := []struct {
		name string
		resp RevenueDailyRoutineResponse
		want string
	}{
		{
			name: "report id mismatch",
			resp: RevenueDailyRoutineResponse{
				Report: RevenueDailyRoutineReport{
					ReportID:     "other",
					WorkstreamID: "ws_revenue",
					Date:         "2026-05-18",
					Status:       "draft_report",
				},
				HumanApprovalRequiredForExternalActions: true,
			},
			want: "report_id mismatch",
		},
		{
			name: "not draft",
			resp: RevenueDailyRoutineResponse{
				Report: RevenueDailyRoutineReport{
					ReportID:     "daily_1",
					WorkstreamID: "ws_revenue",
					Date:         "2026-05-18",
					Status:       "sent",
				},
				HumanApprovalRequiredForExternalActions: true,
			},
			want: "status must be draft_report",
		},
		{
			name: "external action applied",
			resp: RevenueDailyRoutineResponse{
				Report: RevenueDailyRoutineReport{
					ReportID:     "daily_1",
					WorkstreamID: "ws_revenue",
					Date:         "2026-05-18",
					Status:       "draft_report",
				},
				ExternalActionsApplied:                  true,
				HumanApprovalRequiredForExternalActions: true,
			},
			want: "applied external action",
		},
		{
			name: "report claims sent",
			resp: RevenueDailyRoutineResponse{
				Report: RevenueDailyRoutineReport{
					ReportID:            "daily_1",
					WorkstreamID:        "ws_revenue",
					Date:                "2026-05-18",
					Status:              "draft_report",
					ExternalSendApplied: true,
				},
				HumanApprovalRequiredForExternalActions: true,
			},
			want: "applied external action",
		},
		{
			name: "missing approval requirement",
			resp: RevenueDailyRoutineResponse{
				Report: RevenueDailyRoutineReport{
					ReportID:     "daily_1",
					WorkstreamID: "ws_revenue",
					Date:         "2026-05-18",
					Status:       "draft_report",
				},
			},
			want: "missing human approval requirement",
		},
		{
			name: "missing created at",
			resp: RevenueDailyRoutineResponse{
				Report: RevenueDailyRoutineReport{
					ReportID:     "daily_1",
					WorkstreamID: "ws_revenue",
					Date:         "2026-05-18",
					Status:       "draft_report",
				},
				HumanApprovalRequiredForExternalActions: true,
			},
			want: "report missing created_at",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/viewer/revenue/daily-routine" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(tt.resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.CreateRevenueDailyRoutineReport(context.Background(), RevenueDailyRoutineRequest{
				ReportID:     "daily_1",
				WorkstreamID: "ws_revenue",
				Date:         "2026-05-18",
				Limit:        20,
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("CreateRevenueDailyRoutineReport() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestCreateRevenueChannelDraft(t *testing.T) {
	now := time.Date(2026, 5, 20, 4, 50, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/viewer/revenue/channel-drafts" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req RevenueChannelDraft
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.DraftID != "draft_1" || req.Channel != "email" || !req.ExternalSendApplied {
			t.Fatalf("payload=%#v", req)
		}
		req.ExternalSendApplied = false
		req.ApprovalStatus = "pending"
		req.CreatedAt = now
		_ = json.NewEncoder(w).Encode(RevenueChannelDraftResponse{
			Draft:                  req,
			ExternalActionsApplied: false,
			HumanApprovalRequiredForExternalSendApply: true,
		})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.CreateRevenueChannelDraft(context.Background(), RevenueChannelDraft{
		DraftID:             "draft_1",
		Channel:             "email",
		Subject:             "Draft",
		Body:                "外部送信しない下書きです",
		ExternalSendApplied: true,
	})
	if err != nil {
		t.Fatalf("CreateRevenueChannelDraft() error = %v", err)
	}
	if resp.Draft.ExternalSendApplied || resp.Draft.ApprovalStatus != "pending" || !resp.HumanApprovalRequiredForExternalSendApply {
		t.Fatalf("response=%#v", resp)
	}
}

func TestCreateRevenueChannelDraftRejectsInvalidRequest(t *testing.T) {
	tests := []struct {
		name string
		item RevenueChannelDraft
		want string
	}{
		{name: "missing draft", item: RevenueChannelDraft{Channel: "email", Body: "draft"}, want: "missing draft_id"},
		{name: "missing channel", item: RevenueChannelDraft{DraftID: "draft_1", Body: "draft"}, want: "missing channel"},
		{name: "missing body", item: RevenueChannelDraft{DraftID: "draft_1", Channel: "email"}, want: "missing body"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, called, cleanup := newNoRequestClient(t)
			defer cleanup()
			_, err := client.CreateRevenueChannelDraft(context.Background(), tt.item)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("CreateRevenueChannelDraft() error = %v, want %q", err, tt.want)
			}
			if *called {
				t.Fatal("server was called for invalid request")
			}
		})
	}
}

func TestCreateRevenueChannelDraftRejectsMalformedResponse(t *testing.T) {
	tests := []struct {
		name string
		resp RevenueChannelDraftResponse
		want string
	}{
		{
			name: "draft id mismatch",
			resp: RevenueChannelDraftResponse{
				Draft: RevenueChannelDraft{
					DraftID:        "other",
					Channel:        "email",
					ApprovalStatus: "pending",
				},
				HumanApprovalRequiredForExternalSendApply: true,
			},
			want: "draft_id mismatch",
		},
		{
			name: "external action applied",
			resp: RevenueChannelDraftResponse{
				Draft: RevenueChannelDraft{
					DraftID:        "draft_1",
					Channel:        "email",
					ApprovalStatus: "pending",
				},
				ExternalActionsApplied:                    true,
				HumanApprovalRequiredForExternalSendApply: true,
			},
			want: "applied external action",
		},
		{
			name: "draft claims sent",
			resp: RevenueChannelDraftResponse{
				Draft: RevenueChannelDraft{
					DraftID:             "draft_1",
					Channel:             "email",
					ApprovalStatus:      "pending",
					ExternalSendApplied: true,
				},
				HumanApprovalRequiredForExternalSendApply: true,
			},
			want: "draft claims external send applied",
		},
		{
			name: "missing approval requirement",
			resp: RevenueChannelDraftResponse{
				Draft: RevenueChannelDraft{
					DraftID:        "draft_1",
					Channel:        "email",
					ApprovalStatus: "pending",
				},
			},
			want: "missing human approval requirement",
		},
		{
			name: "already approved",
			resp: RevenueChannelDraftResponse{
				Draft: RevenueChannelDraft{
					DraftID:        "draft_1",
					Channel:        "email",
					ApprovalStatus: "approved",
				},
				HumanApprovalRequiredForExternalSendApply: true,
			},
			want: "approval_status must be pending",
		},
		{
			name: "missing created at",
			resp: RevenueChannelDraftResponse{
				Draft: RevenueChannelDraft{
					DraftID:        "draft_1",
					Channel:        "email",
					ApprovalStatus: "pending",
				},
				HumanApprovalRequiredForExternalSendApply: true,
			},
			want: "draft missing created_at",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/viewer/revenue/channel-drafts" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(tt.resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.CreateRevenueChannelDraft(context.Background(), RevenueChannelDraft{
				DraftID: "draft_1",
				Channel: "email",
				Subject: "Draft",
				Body:    "外部送信しない下書きです",
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("CreateRevenueChannelDraft() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestApplyRevenueExternalSend(t *testing.T) {
	now := time.Date(2026, 5, 20, 4, 35, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/viewer/revenue/channel-drafts/external-send-apply" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req RevenueExternalSendApplyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.ApplyID != "apply_1" || req.DraftID != "draft_1" || req.DecisionID != "dec_1" || !req.HumanApproved {
			t.Fatalf("payload=%#v", req)
		}
		_ = json.NewEncoder(w).Encode(RevenueExternalSendApplyResponse{
			Record: RevenueExternalSendApplyRecord{
				ApplyID:             req.ApplyID,
				DraftID:             req.DraftID,
				DecisionID:          req.DecisionID,
				Channel:             "email",
				ChannelAdapter:      "unconfigured",
				ApprovalStatus:      "approved",
				HumanApproved:       true,
				ApplyStatus:         "blocked",
				SendResult:          "not_sent",
				FailureReason:       "external channel adapter is not configured",
				ExternalSendApplied: false,
				CreatedAt:           now,
			},
			ExternalActionsApplied:              false,
			PostSendVerified:                    false,
			HumanApprovalRequiredForRetry:       true,
			ExternalChannelAdapterConfiguration: "required",
			FailureReason:                       "external channel adapter is not configured",
		})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.ApplyRevenueExternalSend(context.Background(), RevenueExternalSendApplyRequest{
		ApplyID:       "apply_1",
		DraftID:       "draft_1",
		DecisionID:    "dec_1",
		HumanApproved: true,
	})
	if err != nil {
		t.Fatalf("ApplyRevenueExternalSend() error = %v", err)
	}
	if resp.Record.ApplyStatus != "blocked" || resp.Record.ExternalSendApplied || resp.ExternalActionsApplied || !resp.HumanApprovalRequiredForRetry {
		t.Fatalf("response=%#v", resp)
	}
}

func TestApplyRevenueExternalSendRejectsInvalidRequest(t *testing.T) {
	tests := []struct {
		name string
		item RevenueExternalSendApplyRequest
		want string
	}{
		{name: "missing apply", item: RevenueExternalSendApplyRequest{DraftID: "draft_1", DecisionID: "dec_1"}, want: "missing apply_id"},
		{name: "missing draft", item: RevenueExternalSendApplyRequest{ApplyID: "apply_1", DecisionID: "dec_1"}, want: "missing draft_id"},
		{name: "missing decision", item: RevenueExternalSendApplyRequest{ApplyID: "apply_1", DraftID: "draft_1"}, want: "missing decision_id"},
		{name: "missing human approval", item: RevenueExternalSendApplyRequest{ApplyID: "apply_1", DraftID: "draft_1", DecisionID: "dec_1"}, want: "requires human_approved"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, called, cleanup := newNoRequestClient(t)
			defer cleanup()
			_, err := client.ApplyRevenueExternalSend(context.Background(), tt.item)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ApplyRevenueExternalSend() error = %v, want %q", err, tt.want)
			}
			if *called {
				t.Fatal("server was called for invalid request")
			}
		})
	}
}

func TestApplyRevenueExternalSendRejectsMalformedAppliedResponse(t *testing.T) {
	tests := []struct {
		name string
		resp RevenueExternalSendApplyResponse
		want string
	}{
		{
			name: "apply id mismatch",
			resp: RevenueExternalSendApplyResponse{
				Record: RevenueExternalSendApplyRecord{ApplyID: "other", DraftID: "draft_1", DecisionID: "dec_1", ApplyStatus: "blocked"},
			},
			want: "apply_id mismatch",
		},
		{
			name: "draft id mismatch",
			resp: RevenueExternalSendApplyResponse{
				Record: RevenueExternalSendApplyRecord{ApplyID: "apply_1", DraftID: "other", DecisionID: "dec_1", ApplyStatus: "blocked"},
			},
			want: "draft_id mismatch",
		},
		{
			name: "decision id mismatch",
			resp: RevenueExternalSendApplyResponse{
				Record: RevenueExternalSendApplyRecord{ApplyID: "apply_1", DraftID: "draft_1", DecisionID: "other", ApplyStatus: "blocked"},
			},
			want: "decision_id mismatch",
		},
		{
			name: "state mismatch",
			resp: RevenueExternalSendApplyResponse{
				Record: RevenueExternalSendApplyRecord{
					ApplyID:             "apply_1",
					DraftID:             "draft_1",
					DecisionID:          "dec_1",
					Channel:             "email",
					ApprovalStatus:      "approved",
					HumanApproved:       true,
					ApplyStatus:         "blocked",
					SendResult:          "not_sent",
					FailureReason:       "external channel adapter is not configured",
					ExternalSendApplied: false,
					PostSendVerified:    false,
				},
				ExternalActionsApplied: true,
				PostSendVerified:       true,
			},
			want: "external send apply response mismatch",
		},
		{
			name: "blocked without failure reason",
			resp: RevenueExternalSendApplyResponse{
				Record: RevenueExternalSendApplyRecord{
					ApplyID:             "apply_1",
					DraftID:             "draft_1",
					DecisionID:          "dec_1",
					Channel:             "email",
					ApprovalStatus:      "approved",
					HumanApproved:       true,
					ApplyStatus:         "blocked",
					SendResult:          "not_sent",
					ExternalSendApplied: false,
					PostSendVerified:    false,
				},
				ExternalActionsApplied:        false,
				PostSendVerified:              false,
				HumanApprovalRequiredForRetry: true,
			},
			want: "without failure_reason",
		},
		{
			name: "blocked with sent result",
			resp: RevenueExternalSendApplyResponse{
				Record: RevenueExternalSendApplyRecord{
					ApplyID:             "apply_1",
					DraftID:             "draft_1",
					DecisionID:          "dec_1",
					Channel:             "email",
					ChannelAdapter:      "unconfigured",
					ApprovalStatus:      "approved",
					HumanApproved:       true,
					ApplyStatus:         "blocked",
					SendResult:          "sent",
					FailureReason:       "external channel adapter is not configured",
					ExternalSendApplied: false,
					PostSendVerified:    false,
				},
				ExternalActionsApplied:              false,
				PostSendVerified:                    false,
				HumanApprovalRequiredForRetry:       true,
				ExternalChannelAdapterConfiguration: "required",
			},
			want: "send_result=sent requires sent apply_status",
		},
		{
			name: "required channel adapter but record configured",
			resp: RevenueExternalSendApplyResponse{
				Record: RevenueExternalSendApplyRecord{
					ApplyID:             "apply_1",
					DraftID:             "draft_1",
					DecisionID:          "dec_1",
					Channel:             "email",
					ChannelAdapter:      "email_api",
					ApprovalStatus:      "approved",
					HumanApproved:       true,
					ApplyStatus:         "blocked",
					SendResult:          "not_sent",
					FailureReason:       "external channel adapter is not configured",
					ExternalSendApplied: false,
					PostSendVerified:    false,
				},
				ExternalActionsApplied:              false,
				PostSendVerified:                    false,
				HumanApprovalRequiredForRetry:       true,
				ExternalChannelAdapterConfiguration: "required",
			},
			want: "channel_adapter conflicts",
		},
		{
			name: "missing human approval requirement for retry",
			resp: RevenueExternalSendApplyResponse{
				Record: RevenueExternalSendApplyRecord{
					ApplyID:             "apply_1",
					DraftID:             "draft_1",
					DecisionID:          "dec_1",
					Channel:             "email",
					ApprovalStatus:      "approved",
					HumanApproved:       true,
					ApplyStatus:         "blocked",
					SendResult:          "not_sent",
					FailureReason:       "external channel adapter is not configured",
					ExternalSendApplied: false,
					PostSendVerified:    false,
				},
			},
			want: "missing human approval requirement for retry",
		},
		{
			name: "record missing human approval",
			resp: RevenueExternalSendApplyResponse{
				Record: RevenueExternalSendApplyRecord{
					ApplyID:             "apply_1",
					DraftID:             "draft_1",
					DecisionID:          "dec_1",
					Channel:             "email",
					ApprovalStatus:      "approved",
					ApplyStatus:         "blocked",
					SendResult:          "not_sent",
					FailureReason:       "external channel adapter is not configured",
					ExternalSendApplied: false,
					PostSendVerified:    false,
				},
				HumanApprovalRequiredForRetry: true,
			},
			want: "record missing human approval",
		},
		{
			name: "missing created at",
			resp: RevenueExternalSendApplyResponse{
				Record: RevenueExternalSendApplyRecord{
					ApplyID:             "apply_1",
					DraftID:             "draft_1",
					DecisionID:          "dec_1",
					Channel:             "email",
					ApprovalStatus:      "approved",
					HumanApproved:       true,
					ApplyStatus:         "blocked",
					SendResult:          "not_sent",
					FailureReason:       "external channel adapter is not configured",
					ExternalSendApplied: false,
					PostSendVerified:    false,
				},
				ExternalActionsApplied:        false,
				PostSendVerified:              false,
				HumanApprovalRequiredForRetry: true,
			},
			want: "record missing created_at",
		},
		{
			name: "approval status not approved",
			resp: RevenueExternalSendApplyResponse{
				Record: RevenueExternalSendApplyRecord{
					ApplyID:             "apply_1",
					DraftID:             "draft_1",
					DecisionID:          "dec_1",
					Channel:             "email",
					ApprovalStatus:      "pending",
					HumanApproved:       true,
					ApplyStatus:         "blocked",
					SendResult:          "not_sent",
					FailureReason:       "external channel adapter is not configured",
					ExternalSendApplied: false,
					PostSendVerified:    false,
				},
				HumanApprovalRequiredForRetry: true,
			},
			want: "approval_status must be approved",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/viewer/revenue/channel-drafts/external-send-apply" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(tt.resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.ApplyRevenueExternalSend(context.Background(), RevenueExternalSendApplyRequest{
				ApplyID:       "apply_1",
				DraftID:       "draft_1",
				DecisionID:    "dec_1",
				HumanApproved: true,
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ApplyRevenueExternalSend() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestRevenueStatus(t *testing.T) {
	var gotPath string
	now := time.Date(2026, 5, 20, 4, 15, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		if r.Method != http.MethodGet {
			t.Fatalf("method=%s", r.Method)
		}
		_ = json.NewEncoder(w).Encode(RevenueStatus{
			HumanDecisions: []RevenueHumanDecisionRecord{{
				DecisionID:     "dec_1",
				DecisionType:   "closed_channel_send",
				ApprovalStatus: "approved",
				GateStatus:     "approved",
				CreatedAt:      now,
			}},
			DailyRoutineReports: []RevenueDailyRoutineReport{{
				ReportID:  "report_1",
				Date:      "2026-05-19",
				Status:    "draft_report",
				CreatedAt: now,
			}},
			ChannelDrafts: []RevenueChannelDraft{{
				DraftID:        "draft_1",
				Channel:        "email",
				Body:           "下書き",
				ApprovalStatus: "pending",
				CreatedAt:      now,
			}},
			ExternalSendApplyRecords: []RevenueExternalSendApplyRecord{{
				ApplyID:        "apply_1",
				DraftID:        "draft_1",
				DecisionID:     "dec_1",
				Channel:        "email",
				ApprovalStatus: "approved",
				HumanApproved:  true,
				ApplyStatus:    "blocked",
				SendResult:     "not_sent",
				FailureReason:  "external channel adapter is not configured",
				CreatedAt:      now,
			}},
			ExternalChannelAdapter:               "unconfigured",
			ExternalChannelAdapterConfigured:     boolPtr(false),
			HumanApprovalRequiredForExternalSend: boolPtr(true),
		})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.RevenueStatus(context.Background(), 7)
	if err != nil {
		t.Fatalf("RevenueStatus() error = %v", err)
	}
	if gotPath != "/viewer/revenue?limit=7" {
		t.Fatalf("path=%s", gotPath)
	}
	if len(status.ExternalSendApplyRecords) != 1 || status.ExternalSendApplyRecords[0].ApplyID != "apply_1" {
		t.Fatalf("status=%#v", status)
	}
	if status.ExternalChannelAdapter != "unconfigured" || status.ExternalChannelAdapterConfigured == nil || *status.ExternalChannelAdapterConfigured || status.HumanApprovalRequiredForExternalSend == nil || !*status.HumanApprovalRequiredForExternalSend {
		t.Fatalf("external channel readiness=%#v", status)
	}
}

func TestRevenueStatusRejectsMalformedCurrentView(t *testing.T) {
	now := time.Date(2026, 5, 20, 4, 15, 0, 0, time.UTC)
	valid := func() RevenueStatus {
		return RevenueStatus{
			HumanDecisions: []RevenueHumanDecisionRecord{{
				DecisionID:     "dec_1",
				DecisionType:   "closed_channel_send",
				ApprovalStatus: "approved",
				GateStatus:     "approved",
				CreatedAt:      now,
			}},
			DailyRoutineReports: []RevenueDailyRoutineReport{{
				ReportID:  "report_1",
				Date:      "2026-05-19",
				Status:    "draft_report",
				CreatedAt: now,
			}},
			ChannelDrafts: []RevenueChannelDraft{{
				DraftID:        "draft_1",
				Channel:        "email",
				Body:           "下書き",
				ApprovalStatus: "pending",
				CreatedAt:      now,
			}},
			ExternalSendApplyRecords: []RevenueExternalSendApplyRecord{{
				ApplyID:        "apply_1",
				DraftID:        "draft_1",
				DecisionID:     "dec_1",
				Channel:        "email",
				ApprovalStatus: "approved",
				HumanApproved:  true,
				ApplyStatus:    "blocked",
				SendResult:     "not_sent",
				FailureReason:  "external channel adapter is not configured",
				CreatedAt:      now,
			}},
			ExternalChannelAdapter:               "unconfigured",
			ExternalChannelAdapterConfigured:     boolPtr(false),
			HumanApprovalRequiredForExternalSend: boolPtr(true),
		}
	}
	tests := []struct {
		name   string
		mutate func(*RevenueStatus)
		want   string
	}{
		{name: "duplicate decision", mutate: func(s *RevenueStatus) {
			s.HumanDecisions = append(s.HumanDecisions, s.HumanDecisions[0])
		}, want: "duplicate human_decision"},
		{name: "decision missing created at", mutate: func(s *RevenueStatus) {
			s.HumanDecisions[0].CreatedAt = time.Time{}
		}, want: "human_decision dec_1 missing created_at"},
		{name: "missing draft body", mutate: func(s *RevenueStatus) {
			s.ChannelDrafts[0].Body = ""
		}, want: "missing body"},
		{name: "daily routine report missing created at", mutate: func(s *RevenueStatus) {
			s.DailyRoutineReports[0].CreatedAt = time.Time{}
		}, want: "daily_routine_report report_1 missing created_at"},
		{name: "channel draft missing created at", mutate: func(s *RevenueStatus) {
			s.ChannelDrafts[0].CreatedAt = time.Time{}
		}, want: "channel_draft draft_1 missing created_at"},
		{name: "duplicate apply", mutate: func(s *RevenueStatus) {
			s.ExternalSendApplyRecords = append(s.ExternalSendApplyRecords, s.ExternalSendApplyRecords[0])
		}, want: "duplicate external_send_apply_record"},
		{name: "external send apply missing created at", mutate: func(s *RevenueStatus) {
			s.ExternalSendApplyRecords[0].CreatedAt = time.Time{}
		}, want: "external_send_apply_record apply_1 missing created_at"},
		{name: "applied without evidence", mutate: func(s *RevenueStatus) {
			s.ExternalSendApplyRecords[0].ApplyStatus = "sent"
			s.ExternalSendApplyRecords[0].ExternalSendApplied = true
			s.ExternalSendApplyRecords[0].PostSendVerified = false
		}, want: "without post-send verification evidence"},
		{name: "verified without applied send", mutate: func(s *RevenueStatus) {
			s.ExternalSendApplyRecords[0].PostSendVerified = true
			s.ExternalSendApplyRecords[0].PostSendEvidence = "external/email/post-send.log"
		}, want: "post-send verification without applied send"},
		{name: "sent without applied send", mutate: func(s *RevenueStatus) {
			s.ExternalSendApplyRecords[0].ApplyStatus = "sent"
		}, want: "sent without external_send_applied"},
		{name: "invalid apply status", mutate: func(s *RevenueStatus) {
			s.ExternalSendApplyRecords[0].ApplyStatus = "applied"
		}, want: "invalid apply_status"},
		{name: "external send approval status not approved", mutate: func(s *RevenueStatus) {
			s.ExternalSendApplyRecords[0].ApprovalStatus = "pending"
		}, want: "approval_status must be approved"},
		{name: "external send missing human approval", mutate: func(s *RevenueStatus) {
			s.ExternalSendApplyRecords[0].HumanApproved = false
		}, want: "missing human approval"},
		{name: "unsent without failure reason", mutate: func(s *RevenueStatus) {
			s.ExternalSendApplyRecords[0].FailureReason = ""
		}, want: "missing failure_reason for unsent external send"},
		{name: "unsent with sent result", mutate: func(s *RevenueStatus) {
			s.ExternalSendApplyRecords[0].SendResult = "sent"
		}, want: "send_result=sent requires sent apply_status"},
		{name: "unconfigured channel adapter with configured record adapter", mutate: func(s *RevenueStatus) {
			s.ExternalSendApplyRecords[0].ChannelAdapter = "email_api"
		}, want: "channel_adapter conflicts"},
		{name: "summary applied without record", mutate: func(s *RevenueStatus) {
			s.Summary.ExternalActionsApplied = true
		}, want: "summary claims external actions applied"},
		{name: "missing external channel adapter", mutate: func(s *RevenueStatus) {
			s.ExternalChannelAdapter = ""
		}, want: "missing external_channel_adapter"},
		{name: "missing external channel configured", mutate: func(s *RevenueStatus) {
			s.ExternalChannelAdapterConfigured = nil
		}, want: "missing external_channel_adapter_configured"},
		{name: "external send without human approval requirement", mutate: func(s *RevenueStatus) {
			s.HumanApprovalRequiredForExternalSend = boolPtr(false)
		}, want: "must require human approval"},
		{name: "unconfigured adapter marked configured", mutate: func(s *RevenueStatus) {
			s.ExternalChannelAdapterConfigured = boolPtr(true)
		}, want: "unconfigured external channel adapter marked configured"},
		{name: "negative summary count", mutate: func(s *RevenueStatus) {
			s.Summary.ExternalSendApplyCount = -1
		}, want: "summary counts must be >= 0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := valid()
			tt.mutate(&resp)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/viewer/revenue" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.RevenueStatus(context.Background(), 0)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("RevenueStatus() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestSandboxStatus(t *testing.T) {
	var gotPath string
	now := time.Date(2026, 5, 20, 4, 5, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		if r.Method != http.MethodGet {
			t.Fatalf("method=%s", r.Method)
		}
		_ = json.NewEncoder(w).Encode(SandboxStatus{
			Sandboxes: []SandboxRecord{{SandboxID: "sbx_1", Type: "code", Status: "active", CreatedAt: now}},
			Decisions: []PromotionGateDecision{{
				Status: "needs_review",
				Reason: "missing approval",
			}},
		})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.SandboxStatus(context.Background(), 10)
	if err != nil {
		t.Fatalf("SandboxStatus() error = %v", err)
	}
	if gotPath != "/viewer/sandbox?limit=10" {
		t.Fatalf("path=%s", gotPath)
	}
	if len(status.Sandboxes) != 1 || status.Sandboxes[0].SandboxID != "sbx_1" {
		t.Fatalf("status=%#v", status)
	}
	if len(status.Decisions) != 1 || status.Decisions[0].Status != "needs_review" {
		t.Fatalf("decisions=%#v", status.Decisions)
	}
}

func TestSandboxStatusRejectsMalformedCurrentView(t *testing.T) {
	now := time.Date(2026, 5, 20, 4, 5, 0, 0, time.UTC)
	sandbox := func(status string) SandboxRecord {
		return SandboxRecord{SandboxID: "sbx_1", Type: "code", Status: status, CreatedAt: now}
	}
	artifact := func(status string) SandboxArtifact {
		return SandboxArtifact{ArtifactID: "art_1", SandboxID: "sbx_1", Type: "rollback_plan", Status: status, CreatedAt: now}
	}
	promotion := func() PromotionRequest {
		return PromotionRequest{PromotionID: "promo_1", SandboxID: "sbx_1", TargetPath: "internal/app.go", CreatedAt: now}
	}
	gateLog := func(status string) PromotionGateLog {
		return PromotionGateLog{EventID: "evt_1", PromotionID: "promo_1", GateStatus: status, CreatedAt: now}
	}
	tests := []struct {
		name string
		resp SandboxStatus
		want string
	}{
		{
			name: "duplicate sandbox",
			resp: SandboxStatus{Sandboxes: []SandboxRecord{
				sandbox("active"),
				{SandboxID: "sbx_1", Type: "code", Status: "closed", CreatedAt: now.Add(time.Second)},
			}},
			want: "duplicate sandbox",
		},
		{
			name: "missing sandbox type",
			resp: SandboxStatus{Sandboxes: []SandboxRecord{{SandboxID: "sbx_1", Status: "active", CreatedAt: now}}},
			want: "sandbox missing type",
		},
		{
			name: "sandbox missing created at",
			resp: SandboxStatus{Sandboxes: []SandboxRecord{{SandboxID: "sbx_1", Type: "code", Status: "active"}}},
			want: "sandbox sbx_1 missing created_at",
		},
		{
			name: "duplicate artifact",
			resp: SandboxStatus{Artifacts: []SandboxArtifact{
				artifact("pending_review"),
				{ArtifactID: "art_1", SandboxID: "sbx_1", Type: "rollback_plan", Status: "completed", CreatedAt: now.Add(time.Second)},
			}},
			want: "duplicate artifact",
		},
		{
			name: "missing artifact sandbox",
			resp: SandboxStatus{Artifacts: []SandboxArtifact{{ArtifactID: "art_1", Type: "rollback_plan", Status: "pending_review", CreatedAt: now}}},
			want: "artifact missing sandbox_id",
		},
		{
			name: "artifact missing created at",
			resp: SandboxStatus{Artifacts: []SandboxArtifact{{ArtifactID: "art_1", SandboxID: "sbx_1", Type: "rollback_plan", Status: "pending_review"}}},
			want: "artifact art_1 missing created_at",
		},
		{
			name: "duplicate promotion",
			resp: SandboxStatus{Promotions: []PromotionRequest{
				promotion(),
				{PromotionID: "promo_1", SandboxID: "sbx_1", TargetPath: "internal/app.go", CreatedAt: now.Add(time.Second)},
			}},
			want: "duplicate promotion",
		},
		{
			name: "missing promotion target",
			resp: SandboxStatus{Promotions: []PromotionRequest{{PromotionID: "promo_1", SandboxID: "sbx_1", CreatedAt: now}}},
			want: "promotion missing target_path",
		},
		{
			name: "promotion missing created at",
			resp: SandboxStatus{Promotions: []PromotionRequest{{PromotionID: "promo_1", SandboxID: "sbx_1", TargetPath: "internal/app.go"}}},
			want: "promotion promo_1 missing created_at",
		},
		{
			name: "missing decision status",
			resp: SandboxStatus{Decisions: []PromotionGateDecision{{Reason: "missing approval"}}},
			want: "decision missing status",
		},
		{
			name: "invalid decision status",
			resp: SandboxStatus{Decisions: []PromotionGateDecision{{Status: "applied", Reason: "ambiguous status"}}},
			want: "invalid decision status",
		},
		{
			name: "duplicate gate log",
			resp: SandboxStatus{GateLogs: []PromotionGateLog{
				gateLog("needs_review"),
				{EventID: "evt_1", PromotionID: "promo_1", GateStatus: "promotion_applied", PostApplyVerification: "sandbox/post-apply.log", CreatedAt: now.Add(time.Second)},
			}},
			want: "duplicate gate_log",
		},
		{
			name: "missing gate log promotion",
			resp: SandboxStatus{GateLogs: []PromotionGateLog{{EventID: "evt_1", GateStatus: "needs_review", CreatedAt: now}}},
			want: "gate_log missing promotion_id",
		},
		{
			name: "invalid gate log status",
			resp: SandboxStatus{GateLogs: []PromotionGateLog{{EventID: "evt_1", PromotionID: "promo_1", GateStatus: "applied", CreatedAt: now}}},
			want: "invalid gate_status",
		},
		{
			name: "gate log missing created at",
			resp: SandboxStatus{GateLogs: []PromotionGateLog{{EventID: "evt_1", PromotionID: "promo_1", GateStatus: "needs_review"}}},
			want: "gate_log evt_1 missing created_at",
		},
		{
			name: "applied gate log missing post apply verification",
			resp: SandboxStatus{
				Promotions: []PromotionRequest{{
					PromotionID:               "promo_1",
					SandboxID:                 "sbx_1",
					TargetPath:                "internal/app.go",
					DiffPath:                  "sandbox/change.diff",
					PostApplyVerificationPath: "sandbox/post-apply.log",
					HumanApprovalStatus:       "granted",
					CreatedAt:                 now,
				}},
				GateLogs: []PromotionGateLog{{
					EventID:             "evt_1",
					PromotionID:         "promo_1",
					GateStatus:          "promotion_applied",
					HumanApprovalStatus: "granted",
					CreatedAt:           now,
				}},
			},
			want: "promotion_applied gate_log missing post_apply_verification",
		},
		{
			name: "applied gate log missing promotion record",
			resp: SandboxStatus{GateLogs: []PromotionGateLog{{
				EventID:               "evt_1",
				PromotionID:           "promo_1",
				GateStatus:            "promotion_applied",
				HumanApprovalStatus:   "granted",
				PostApplyVerification: "sandbox/post-apply.log",
				CreatedAt:             now,
			}}},
			want: "promotion_applied missing promotion record",
		},
		{
			name: "applied gate log missing human approval",
			resp: SandboxStatus{GateLogs: []PromotionGateLog{{
				EventID:               "evt_1",
				PromotionID:           "promo_1",
				GateStatus:            "promotion_applied",
				PostApplyVerification: "sandbox/post-apply.log",
				CreatedAt:             now,
			}}},
			want: "promotion_applied gate_log requires human approval",
		},
		{
			name: "applied promotion missing approval",
			resp: SandboxStatus{
				Promotions: []PromotionRequest{{
					PromotionID:               "promo_1",
					SandboxID:                 "sbx_1",
					TargetPath:                "internal/app.go",
					DiffPath:                  "sandbox/change.diff",
					PostApplyVerificationPath: "sandbox/post-apply.log",
					CreatedAt:                 now,
				}},
				GateLogs: []PromotionGateLog{{
					EventID:               "evt_1",
					PromotionID:           "promo_1",
					GateStatus:            "promotion_applied",
					HumanApprovalStatus:   "granted",
					PostApplyVerification: "sandbox/post-apply.log",
					CreatedAt:             now,
				}},
			},
			want: "promotion_applied promotion requires human approval",
		},
		{
			name: "applied promotion missing completed artifact",
			resp: SandboxStatus{
				Promotions: []PromotionRequest{{
					PromotionID:               "promo_1",
					SandboxID:                 "sbx_1",
					TargetPath:                "internal/app.go",
					DiffPath:                  "sandbox/change.diff",
					PostApplyVerificationPath: "sandbox/post-apply.log",
					HumanApprovalStatus:       "granted",
					CreatedAt:                 now,
				}},
				GateLogs: []PromotionGateLog{{
					EventID:               "evt_1",
					PromotionID:           "promo_1",
					GateStatus:            "promotion_applied",
					HumanApprovalStatus:   "granted",
					PostApplyVerification: "sandbox/post-apply.log",
					CreatedAt:             now,
				}},
			},
			want: "promotion_applied missing completed post_apply_verification artifact",
		},
		{
			name: "rollback gate log missing completed artifact",
			resp: SandboxStatus{
				Promotions: []PromotionRequest{{
					PromotionID:         "promo_1",
					SandboxID:           "sbx_1",
					TargetPath:          "internal/app.go",
					RollbackPlanPath:    "sandbox/rollback.plan",
					HumanApprovalStatus: "granted",
					CreatedAt:           now,
				}},
				GateLogs: []PromotionGateLog{{
					EventID:             "evt_1",
					PromotionID:         "promo_1",
					GateStatus:          "rollback_executed",
					HumanApprovalStatus: "granted",
					CreatedAt:           now,
				}},
			},
			want: "rollback_executed missing completed rollback_execution artifact",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/viewer/sandbox" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(tt.resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.SandboxStatus(context.Background(), 0)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("SandboxStatus() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestSandboxStatusAcceptsAppliedAndRollbackEvidence(t *testing.T) {
	now := time.Date(2026, 5, 20, 4, 5, 0, 0, time.UTC)
	resp := SandboxStatus{
		Artifacts: []SandboxArtifact{
			{ArtifactID: "art_post_apply_1", SandboxID: "sbx_1", Type: "post_apply_verification", FilePath: "sandbox/post-apply.log", Status: "completed", CreatedAt: now},
			{ArtifactID: "art_rollback_1", SandboxID: "sbx_1", Type: "rollback_execution", FilePath: "sandbox/rollback.plan", Status: "completed", CreatedAt: now},
		},
		Promotions: []PromotionRequest{{
			PromotionID:               "promo_1",
			SandboxID:                 "sbx_1",
			TargetPath:                "internal/app.go",
			DiffPath:                  "sandbox/change.diff",
			RollbackPlanPath:          "sandbox/rollback.plan",
			PostApplyVerificationPath: "sandbox/post-apply.log",
			HumanApprovalStatus:       "granted",
			CreatedAt:                 now,
		}},
		GateLogs: []PromotionGateLog{
			{
				EventID:               "evt_apply_1",
				PromotionID:           "promo_1",
				GateStatus:            "promotion_applied",
				HumanApprovalStatus:   "granted",
				PostApplyVerification: "sandbox/post-apply.log",
				CreatedAt:             now,
			},
			{
				EventID:             "evt_rollback_1",
				PromotionID:         "promo_1",
				GateStatus:          "rollback_executed",
				HumanApprovalStatus: "granted",
				CreatedAt:           now,
			},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/viewer/sandbox" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.SandboxStatus(context.Background(), 0)
	if err != nil {
		t.Fatalf("SandboxStatus() error = %v", err)
	}
	if len(got.GateLogs) != 2 {
		t.Fatalf("gate_logs=%#v", got.GateLogs)
	}
}

func TestCreatePromotionRequest(t *testing.T) {
	now := time.Date(2026, 5, 20, 5, 20, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/viewer/sandbox/promotions" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req PromotionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.PromotionID != "promo_1" || req.SandboxID != "sbx_1" || req.HumanApprovalStatus != "granted" {
			t.Fatalf("payload=%#v", req)
		}
		_ = json.NewEncoder(w).Encode(PromotionRequestResponse{
			Promotion: req,
			Decision:  PromotionGateDecision{Status: "approve", Reason: "ok"},
			GateLog:   PromotionGateLog{EventID: "evt_1", PromotionID: req.PromotionID, GateStatus: "approve", CreatedAt: now},
		})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.CreatePromotionRequest(context.Background(), PromotionRequest{
		PromotionID:         "promo_1",
		SandboxID:           "sbx_1",
		TargetPath:          "internal/example.go",
		DiffPath:            "sandbox/diff.patch",
		TestResultPath:      "sandbox/test.log",
		Reason:              "verified patch",
		RollbackPlanPath:    "sandbox/rollback.md",
		HumanApprovalStatus: "granted",
		CreatedAt:           time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("CreatePromotionRequest() error = %v", err)
	}
	if resp.Promotion.PromotionID != "promo_1" || resp.Decision.Status != "approve" || resp.GateLog.EventID != "evt_1" {
		t.Fatalf("response=%#v", resp)
	}
}

func TestCreatePromotionRequestRejectsInvalidRequest(t *testing.T) {
	tests := []struct {
		name string
		req  PromotionRequest
		want string
	}{
		{
			name: "missing promotion",
			req:  PromotionRequest{SandboxID: "sbx_1", TargetPath: "internal/example.go"},
			want: "missing promotion_id",
		},
		{
			name: "missing sandbox",
			req:  PromotionRequest{PromotionID: "promo_1", TargetPath: "internal/example.go"},
			want: "missing sandbox_id",
		},
		{
			name: "missing target",
			req:  PromotionRequest{PromotionID: "promo_1", SandboxID: "sbx_1"},
			want: "missing target_path",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, called, cleanup := newNoRequestClient(t)
			defer cleanup()
			_, err := client.CreatePromotionRequest(context.Background(), tt.req)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("CreatePromotionRequest() error = %v, want %q", err, tt.want)
			}
			if *called {
				t.Fatalf("CreatePromotionRequest() sent request for invalid payload")
			}
		})
	}
}

func TestCreatePromotionRequestRejectsMalformedResponse(t *testing.T) {
	tests := []struct {
		name string
		resp PromotionRequestResponse
		want string
	}{
		{
			name: "promotion id mismatch",
			resp: PromotionRequestResponse{
				Promotion: PromotionRequest{PromotionID: "other", SandboxID: "sbx_1", TargetPath: "internal/example.go", DiffPath: "sandbox/diff.patch"},
				Decision:  PromotionGateDecision{Status: "approve", Reason: "ok"},
				GateLog:   PromotionGateLog{EventID: "evt_1", PromotionID: "other", GateStatus: "approve"},
			},
			want: "promotion_id mismatch",
		},
		{
			name: "missing gate event",
			resp: PromotionRequestResponse{
				Promotion: PromotionRequest{PromotionID: "promo_1", SandboxID: "sbx_1", TargetPath: "internal/example.go", DiffPath: "sandbox/diff.patch"},
				Decision:  PromotionGateDecision{Status: "approve", Reason: "ok"},
				GateLog:   PromotionGateLog{PromotionID: "promo_1", GateStatus: "approve"},
			},
			want: "missing gate event_id",
		},
		{
			name: "gate status mismatch",
			resp: PromotionRequestResponse{
				Promotion: PromotionRequest{PromotionID: "promo_1", SandboxID: "sbx_1", TargetPath: "internal/example.go", DiffPath: "sandbox/diff.patch"},
				Decision:  PromotionGateDecision{Status: "approve", Reason: "ok"},
				GateLog:   PromotionGateLog{EventID: "evt_1", PromotionID: "promo_1", GateStatus: "needs_review"},
			},
			want: "gate status mismatch",
		},
		{
			name: "review without missing requirements",
			resp: PromotionRequestResponse{
				Promotion: PromotionRequest{PromotionID: "promo_1", SandboxID: "sbx_1", TargetPath: "internal/example.go", DiffPath: "sandbox/diff.patch"},
				Decision:  PromotionGateDecision{Status: "needs_review", Reason: "missing approval"},
				GateLog:   PromotionGateLog{EventID: "evt_1", PromotionID: "promo_1", GateStatus: "needs_review"},
			},
			want: "review decision without missing requirements",
		},
		{
			name: "rollback artifact path mismatch",
			resp: PromotionRequestResponse{
				Promotion:        PromotionRequest{PromotionID: "promo_1", SandboxID: "sbx_1", TargetPath: "internal/example.go", DiffPath: "sandbox/diff.patch"},
				Decision:         PromotionGateDecision{Status: "approve", Reason: "ok"},
				GateLog:          PromotionGateLog{EventID: "evt_1", PromotionID: "promo_1", GateStatus: "approve"},
				RollbackArtifact: &SandboxArtifact{ArtifactID: "art_1", SandboxID: "sbx_1", Type: "rollback_plan", FilePath: "sandbox/other.md", Status: "completed"},
			},
			want: "rollback artifact path mismatch",
		},
		{
			name: "post apply artifact type mismatch",
			resp: PromotionRequestResponse{
				Promotion:                     PromotionRequest{PromotionID: "promo_1", SandboxID: "sbx_1", TargetPath: "internal/example.go", DiffPath: "sandbox/diff.patch"},
				Decision:                      PromotionGateDecision{Status: "approve", Reason: "ok"},
				GateLog:                       PromotionGateLog{EventID: "evt_1", PromotionID: "promo_1", GateStatus: "approve"},
				PostApplyVerificationArtifact: &SandboxArtifact{ArtifactID: "art_2", SandboxID: "sbx_1", Type: "other", FilePath: "sandbox/post-apply.log", Status: "pending"},
			},
			want: "post-apply artifact type mismatch",
		},
		{
			name: "promotion missing created at",
			resp: PromotionRequestResponse{
				Promotion: PromotionRequest{PromotionID: "promo_1", SandboxID: "sbx_1", TargetPath: "internal/example.go", DiffPath: "sandbox/diff.patch"},
				Decision:  PromotionGateDecision{Status: "approve", Reason: "ok"},
				GateLog:   PromotionGateLog{EventID: "evt_1", PromotionID: "promo_1", GateStatus: "approve", CreatedAt: time.Date(2026, 5, 20, 5, 20, 0, 0, time.UTC)},
			},
			want: "promotion missing created_at",
		},
		{
			name: "gate log missing created at",
			resp: PromotionRequestResponse{
				Promotion: PromotionRequest{PromotionID: "promo_1", SandboxID: "sbx_1", TargetPath: "internal/example.go", DiffPath: "sandbox/diff.patch", CreatedAt: time.Date(2026, 5, 20, 5, 20, 0, 0, time.UTC)},
				Decision:  PromotionGateDecision{Status: "approve", Reason: "ok"},
				GateLog:   PromotionGateLog{EventID: "evt_1", PromotionID: "promo_1", GateStatus: "approve"},
			},
			want: "gate_log missing created_at",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/viewer/sandbox/promotions" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(tt.resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.CreatePromotionRequest(context.Background(), PromotionRequest{
				PromotionID:               "promo_1",
				SandboxID:                 "sbx_1",
				TargetPath:                "internal/example.go",
				DiffPath:                  "sandbox/diff.patch",
				TestResultPath:            "sandbox/test.log",
				Reason:                    "verified patch",
				RollbackPlanPath:          "sandbox/rollback.md",
				PostApplyVerificationPath: "sandbox/post-apply.log",
				HumanApprovalStatus:       "granted",
				CreatedAt:                 time.Now().UTC(),
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("CreatePromotionRequest() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestApplyPromotion(t *testing.T) {
	now := time.Date(2026, 5, 20, 5, 20, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/viewer/sandbox/promotions/apply" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req PromotionApplyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.Promotion.PromotionID != "promo_1" || !req.HumanApproved || req.PostApplyVerificationPath == "" {
			t.Fatalf("payload=%#v", req)
		}
		_ = json.NewEncoder(w).Encode(PromotionApplyResponse{
			Decision:                      PromotionGateDecision{Status: "promotion_applied", Reason: "recorded"},
			DiffApplyResult:               &PromotionDiffApplyResult{Status: "applied", AppliedFiles: []string{"internal/example.go"}},
			GateLog:                       PromotionGateLog{EventID: "evt_apply_1", PromotionID: req.Promotion.PromotionID, GateStatus: "promotion_applied", PostApplyVerification: req.PostApplyVerificationPath, CreatedAt: now},
			PostApplyVerificationArtifact: SandboxArtifact{ArtifactID: "art_post_apply_1", SandboxID: req.Promotion.SandboxID, Type: "post_apply_verification", FilePath: "sandbox/post-apply.log", Status: "completed", CreatedAt: now},
		})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.ApplyPromotion(context.Background(), PromotionApplyRequest{
		Promotion: PromotionRequest{
			PromotionID:         "promo_1",
			SandboxID:           "sbx_1",
			TargetPath:          "internal/example.go",
			DiffPath:            "sandbox/diff.patch",
			TestResultPath:      "sandbox/test.log",
			Reason:              "verified patch",
			RollbackPlanPath:    "sandbox/rollback.md",
			HumanApprovalStatus: "granted",
			CreatedAt:           time.Now().UTC(),
		},
		AppliedBy:                 "Worker",
		PostApplyVerificationPath: "sandbox/post-apply.log",
		HumanApproved:             true,
	})
	if err != nil {
		t.Fatalf("ApplyPromotion() error = %v", err)
	}
	if resp.Decision.Status != "promotion_applied" || resp.GateLog.EventID != "evt_apply_1" {
		t.Fatalf("response=%#v", resp)
	}
	if resp.DiffApplyResult == nil || resp.DiffApplyResult.Status != "applied" || len(resp.DiffApplyResult.AppliedFiles) != 1 {
		t.Fatalf("diff apply response=%#v", resp.DiffApplyResult)
	}
}

func TestApplyPromotionRejectsInvalidRequest(t *testing.T) {
	tests := []struct {
		name string
		req  PromotionApplyRequest
		want string
	}{
		{
			name: "missing promotion",
			req: PromotionApplyRequest{
				Promotion:                 PromotionRequest{SandboxID: "sbx_1", TargetPath: "internal/example.go", DiffPath: "sandbox/diff.patch", HumanApprovalStatus: "granted"},
				PostApplyVerificationPath: "sandbox/post-apply.log",
				HumanApproved:             true,
			},
			want: "missing promotion_id",
		},
		{
			name: "missing human approval",
			req: PromotionApplyRequest{
				Promotion:                 PromotionRequest{PromotionID: "promo_1", SandboxID: "sbx_1", TargetPath: "internal/example.go", DiffPath: "sandbox/diff.patch", HumanApprovalStatus: "pending"},
				PostApplyVerificationPath: "sandbox/post-apply.log",
				HumanApproved:             true,
			},
			want: "requires human approval",
		},
		{
			name: "missing diff",
			req: PromotionApplyRequest{
				Promotion:                 PromotionRequest{PromotionID: "promo_1", SandboxID: "sbx_1", TargetPath: "internal/example.go", HumanApprovalStatus: "granted"},
				PostApplyVerificationPath: "sandbox/post-apply.log",
				HumanApproved:             true,
			},
			want: "missing diff_path",
		},
		{
			name: "missing post apply evidence",
			req: PromotionApplyRequest{
				Promotion:     PromotionRequest{PromotionID: "promo_1", SandboxID: "sbx_1", TargetPath: "internal/example.go", DiffPath: "sandbox/diff.patch", HumanApprovalStatus: "granted"},
				HumanApproved: true,
			},
			want: "missing post_apply_verification_path",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, called, cleanup := newNoRequestClient(t)
			defer cleanup()
			_, err := client.ApplyPromotion(context.Background(), tt.req)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ApplyPromotion() error = %v, want %q", err, tt.want)
			}
			if *called {
				t.Fatalf("ApplyPromotion() sent request for invalid payload")
			}
		})
	}
}

func TestApplyPromotionRejectsMalformedApplyResponse(t *testing.T) {
	now := time.Date(2026, 5, 20, 5, 20, 0, 0, time.UTC)
	req := PromotionApplyRequest{
		Promotion: PromotionRequest{
			PromotionID:         "promo_1",
			SandboxID:           "sbx_1",
			TargetPath:          "internal/example.go",
			DiffPath:            "sandbox/diff.patch",
			TestResultPath:      "sandbox/test.log",
			Reason:              "verified patch",
			RollbackPlanPath:    "sandbox/rollback.md",
			HumanApprovalStatus: "granted",
			CreatedAt:           time.Now().UTC(),
		},
		AppliedBy:                 "Worker",
		PostApplyVerificationPath: "sandbox/post-apply.log",
		HumanApproved:             true,
	}
	valid := PromotionApplyResponse{
		Decision:                      PromotionGateDecision{Status: "promotion_applied", Reason: "recorded"},
		DiffApplyResult:               &PromotionDiffApplyResult{Status: "applied", AppliedFiles: []string{"internal/example.go"}},
		GateLog:                       PromotionGateLog{EventID: "evt_apply_1", PromotionID: "promo_1", GateStatus: "promotion_applied", PostApplyVerification: "sandbox/post-apply.log", CreatedAt: now},
		PostApplyVerificationArtifact: SandboxArtifact{ArtifactID: "art_post_apply_1", SandboxID: "sbx_1", Type: "post_apply_verification", FilePath: "sandbox/post-apply.log", Status: "completed", CreatedAt: now},
	}
	tests := []struct {
		name string
		resp PromotionApplyResponse
		want string
	}{
		{
			name: "artifact path mismatch",
			resp: PromotionApplyResponse{
				Decision:                      valid.Decision,
				GateLog:                       valid.GateLog,
				PostApplyVerificationArtifact: SandboxArtifact{ArtifactID: "art_post_apply_1", SandboxID: "sbx_1", Type: "post_apply_verification", Status: "completed"},
			},
			want: "artifact path mismatch",
		},
		{
			name: "gate promotion mismatch",
			resp: PromotionApplyResponse{
				Decision:                      valid.Decision,
				DiffApplyResult:               valid.DiffApplyResult,
				GateLog:                       PromotionGateLog{EventID: "evt_apply_1", PromotionID: "promo_other", GateStatus: "promotion_applied", PostApplyVerification: "sandbox/post-apply.log"},
				PostApplyVerificationArtifact: valid.PostApplyVerificationArtifact,
			},
			want: "promotion_id mismatch",
		},
		{
			name: "gate post apply mismatch",
			resp: PromotionApplyResponse{
				Decision:                      valid.Decision,
				DiffApplyResult:               valid.DiffApplyResult,
				GateLog:                       PromotionGateLog{EventID: "evt_apply_1", PromotionID: "promo_1", GateStatus: "promotion_applied", PostApplyVerification: "sandbox/other-post-apply.log"},
				PostApplyVerificationArtifact: valid.PostApplyVerificationArtifact,
			},
			want: "post_apply_verification mismatch",
		},
		{
			name: "artifact sandbox mismatch",
			resp: PromotionApplyResponse{
				Decision:                      valid.Decision,
				DiffApplyResult:               valid.DiffApplyResult,
				GateLog:                       valid.GateLog,
				PostApplyVerificationArtifact: SandboxArtifact{ArtifactID: "art_post_apply_1", SandboxID: "sbx_other", Type: "post_apply_verification", FilePath: "sandbox/post-apply.log", Status: "completed"},
			},
			want: "artifact sandbox_id mismatch",
		},
		{
			name: "diff result missing applied files",
			resp: PromotionApplyResponse{
				Decision:                      valid.Decision,
				DiffApplyResult:               &PromotionDiffApplyResult{Status: "applied"},
				GateLog:                       valid.GateLog,
				PostApplyVerificationArtifact: valid.PostApplyVerificationArtifact,
			},
			want: "missing applied_files",
		},
		{
			name: "gate log missing created at",
			resp: PromotionApplyResponse{
				Decision:                      valid.Decision,
				DiffApplyResult:               valid.DiffApplyResult,
				GateLog:                       PromotionGateLog{EventID: "evt_apply_1", PromotionID: "promo_1", GateStatus: "promotion_applied", PostApplyVerification: "sandbox/post-apply.log"},
				PostApplyVerificationArtifact: valid.PostApplyVerificationArtifact,
			},
			want: "gate_log missing created_at",
		},
		{
			name: "artifact missing created at",
			resp: PromotionApplyResponse{
				Decision:        valid.Decision,
				DiffApplyResult: valid.DiffApplyResult,
				GateLog:         valid.GateLog,
				PostApplyVerificationArtifact: SandboxArtifact{
					ArtifactID: "art_post_apply_1",
					SandboxID:  "sbx_1",
					Type:       "post_apply_verification",
					FilePath:   "sandbox/post-apply.log",
					Status:     "completed",
				},
			},
			want: "artifact missing created_at",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/viewer/sandbox/promotions/apply" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(tt.resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.ApplyPromotion(context.Background(), req)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ApplyPromotion() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestRollbackPromotion(t *testing.T) {
	now := time.Date(2026, 5, 20, 5, 20, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/viewer/sandbox/promotions/rollback" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req PromotionApplyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.Promotion.PromotionID != "promo_1" || !req.HumanApproved || req.PostApplyVerificationPath == "" {
			t.Fatalf("payload=%#v", req)
		}
		_ = json.NewEncoder(w).Encode(PromotionRollbackResponse{
			Decision:         PromotionGateDecision{Status: "rollback_executed", Reason: "rolled back"},
			RollbackResult:   PromotionDiffApplyResult{Status: "rolled_back", AppliedFiles: []string{"internal/example.go"}},
			RollbackArtifact: SandboxArtifact{ArtifactID: "art_rollback_1", SandboxID: req.Promotion.SandboxID, Type: "rollback_execution", FilePath: req.Promotion.RollbackPlanPath, Status: "completed", CreatedAt: now},
			GateLog:          PromotionGateLog{EventID: "evt_rollback_1", PromotionID: req.Promotion.PromotionID, GateStatus: "rollback_executed", CreatedAt: now},
		})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.RollbackPromotion(context.Background(), PromotionApplyRequest{
		Promotion: PromotionRequest{
			PromotionID:         "promo_1",
			SandboxID:           "sbx_1",
			TargetPath:          "internal/example.go",
			DiffPath:            "sandbox/diff.patch",
			TestResultPath:      "sandbox/test.log",
			Reason:              "verified patch",
			RollbackPlanPath:    "sandbox/rollback.md",
			HumanApprovalStatus: "granted",
			CreatedAt:           time.Now().UTC(),
		},
		AppliedBy:                 "Worker",
		PostApplyVerificationPath: "sandbox/post-rollback.log",
		HumanApproved:             true,
	})
	if err != nil {
		t.Fatalf("RollbackPromotion() error = %v", err)
	}
	if resp.Decision.Status != "rollback_executed" || resp.GateLog.EventID != "evt_rollback_1" {
		t.Fatalf("response=%#v", resp)
	}
	if resp.RollbackResult.Status != "rolled_back" || len(resp.RollbackResult.AppliedFiles) != 1 {
		t.Fatalf("rollback response=%#v", resp.RollbackResult)
	}
}

func TestRollbackPromotionRejectsInvalidRequest(t *testing.T) {
	tests := []struct {
		name string
		req  PromotionApplyRequest
		want string
	}{
		{
			name: "missing promotion",
			req: PromotionApplyRequest{
				Promotion:     PromotionRequest{SandboxID: "sbx_1", TargetPath: "internal/example.go", RollbackPlanPath: "sandbox/rollback.md", HumanApprovalStatus: "granted"},
				HumanApproved: true,
			},
			want: "missing promotion_id",
		},
		{
			name: "missing human approval",
			req: PromotionApplyRequest{
				Promotion:     PromotionRequest{PromotionID: "promo_1", SandboxID: "sbx_1", TargetPath: "internal/example.go", RollbackPlanPath: "sandbox/rollback.md", HumanApprovalStatus: "pending"},
				HumanApproved: true,
			},
			want: "requires human approval",
		},
		{
			name: "missing rollback plan",
			req: PromotionApplyRequest{
				Promotion:     PromotionRequest{PromotionID: "promo_1", SandboxID: "sbx_1", TargetPath: "internal/example.go", HumanApprovalStatus: "granted"},
				HumanApproved: true,
			},
			want: "missing rollback_plan_path",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, called, cleanup := newNoRequestClient(t)
			defer cleanup()
			_, err := client.RollbackPromotion(context.Background(), tt.req)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("RollbackPromotion() error = %v, want %q", err, tt.want)
			}
			if *called {
				t.Fatalf("RollbackPromotion() sent request for invalid payload")
			}
		})
	}
}

func TestRollbackPromotionRejectsMalformedResponse(t *testing.T) {
	now := time.Date(2026, 5, 20, 5, 20, 0, 0, time.UTC)
	tests := []struct {
		name string
		resp PromotionRollbackResponse
		want string
	}{
		{
			name: "artifact path mismatch",
			resp: PromotionRollbackResponse{
				Decision:         PromotionGateDecision{Status: "rollback_executed", Reason: "rolled back"},
				RollbackResult:   PromotionDiffApplyResult{Status: "rolled_back", AppliedFiles: []string{"internal/example.go"}},
				RollbackArtifact: SandboxArtifact{ArtifactID: "art_rollback_1", SandboxID: "sbx_1", Type: "rollback_execution", FilePath: "sandbox/other-rollback.md", Status: "completed", CreatedAt: now},
				GateLog:          PromotionGateLog{EventID: "evt_rollback_1", PromotionID: "promo_1", GateStatus: "rollback_executed", CreatedAt: now},
			},
			want: "artifact path mismatch",
		},
		{
			name: "gate log missing created at",
			resp: PromotionRollbackResponse{
				Decision:         PromotionGateDecision{Status: "rollback_executed", Reason: "rolled back"},
				RollbackResult:   PromotionDiffApplyResult{Status: "rolled_back", AppliedFiles: []string{"internal/example.go"}},
				RollbackArtifact: SandboxArtifact{ArtifactID: "art_rollback_1", SandboxID: "sbx_1", Type: "rollback_execution", FilePath: "sandbox/rollback.md", Status: "completed", CreatedAt: now},
				GateLog:          PromotionGateLog{EventID: "evt_rollback_1", PromotionID: "promo_1", GateStatus: "rollback_executed"},
			},
			want: "gate_log missing created_at",
		},
		{
			name: "artifact missing created at",
			resp: PromotionRollbackResponse{
				Decision:         PromotionGateDecision{Status: "rollback_executed", Reason: "rolled back"},
				RollbackResult:   PromotionDiffApplyResult{Status: "rolled_back", AppliedFiles: []string{"internal/example.go"}},
				RollbackArtifact: SandboxArtifact{ArtifactID: "art_rollback_1", SandboxID: "sbx_1", Type: "rollback_execution", FilePath: "sandbox/rollback.md", Status: "completed"},
				GateLog:          PromotionGateLog{EventID: "evt_rollback_1", PromotionID: "promo_1", GateStatus: "rollback_executed", CreatedAt: now},
			},
			want: "artifact missing created_at",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/viewer/sandbox/promotions/rollback" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(tt.resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.RollbackPromotion(context.Background(), PromotionApplyRequest{
				Promotion: PromotionRequest{
					PromotionID:         "promo_1",
					SandboxID:           "sbx_1",
					TargetPath:          "internal/example.go",
					DiffPath:            "sandbox/diff.patch",
					TestResultPath:      "sandbox/test.log",
					Reason:              "verified patch",
					RollbackPlanPath:    "sandbox/rollback.md",
					HumanApprovalStatus: "granted",
					CreatedAt:           now,
				},
				AppliedBy:                 "Worker",
				PostApplyVerificationPath: "sandbox/post-rollback.log",
				HumanApproved:             true,
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("RollbackPromotion() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestSubmitPromotionWorkflowCreatesRequestButDoesNotApplyWithoutApproval(t *testing.T) {
	var paths []string
	now := time.Date(2026, 5, 20, 5, 20, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/viewer/ai-workflow/external-control/check":
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected method: %s", r.Method)
			}
			var req ExternalControlRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(ExternalControlResponse{
				Request:  req,
				Decision: ExternalControlDecision{Status: "allowed", RequiresApproval: true},
			})
			return
		case "/viewer/sandbox/promotions":
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected method: %s", r.Method)
			}
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req PromotionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(PromotionRequestResponse{
			Promotion: req,
			Decision:  PromotionGateDecision{Status: "approve", Reason: "ok"},
			GateLog:   PromotionGateLog{EventID: "evt_1", PromotionID: req.PromotionID, GateStatus: "approve", CreatedAt: now},
		})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.SubmitPromotionWorkflow(context.Background(), PromotionWorkflowRequest{
		Promotion: PromotionRequest{
			PromotionID:         "promo_1",
			SandboxID:           "sbx_1",
			TargetPath:          "internal/example.go",
			DiffPath:            "sandbox/diff.patch",
			TestResultPath:      "sandbox/test.log",
			Reason:              "verified patch",
			RollbackPlanPath:    "sandbox/rollback.md",
			HumanApprovalStatus: "granted",
			CreatedAt:           time.Now().UTC(),
		},
		ApplyAfterApproval:        true,
		PostApplyVerificationPath: "sandbox/post-apply.log",
		HumanApproved:             false,
		ExternalControl: &ExternalControlRequest{
			Actor:         "Worker",
			ChannelID:     "viewer",
			Action:        "promotion_apply",
			HumanApproved: true,
		},
	})
	if err != nil {
		t.Fatalf("SubmitPromotionWorkflow() error = %v", err)
	}
	if resp.Applied || resp.ApplyResponse != nil || resp.SkippedReason != "human approval is required before apply" {
		t.Fatalf("workflow response=%#v", resp)
	}
	if len(paths) != 2 || paths[0] != "/viewer/ai-workflow/external-control/check" || paths[1] != "/viewer/sandbox/promotions" {
		t.Fatalf("paths=%#v", paths)
	}
}

func TestSubmitPromotionWorkflowRequiresExternalControlBeforeApply(t *testing.T) {
	var called bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		t.Fatalf("unexpected request without external control policy: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.SubmitPromotionWorkflow(context.Background(), PromotionWorkflowRequest{
		Promotion: PromotionRequest{
			PromotionID:         "promo_1",
			SandboxID:           "sbx_1",
			TargetPath:          "internal/example.go",
			DiffPath:            "sandbox/diff.patch",
			TestResultPath:      "sandbox/test.log",
			Reason:              "verified patch",
			RollbackPlanPath:    "sandbox/rollback.md",
			HumanApprovalStatus: "granted",
			CreatedAt:           time.Now().UTC(),
		},
		ApplyAfterApproval:        true,
		PostApplyVerificationPath: "sandbox/post-apply.log",
		HumanApproved:             true,
	})
	if err != nil {
		t.Fatalf("SubmitPromotionWorkflow() error = %v", err)
	}
	if called || resp.Applied || resp.SkippedReason != "external control policy is required before apply" {
		t.Fatalf("workflow response=%#v called=%v", resp, called)
	}
}

func TestSubmitPromotionWorkflowStopsWhenExternalControlPolicyBlocks(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Method != http.MethodPost || r.URL.Path != "/viewer/ai-workflow/external-control/check" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req ExternalControlRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(ExternalControlResponse{
			Request:  req,
			Decision: ExternalControlDecision{Status: "needs_approval", RequiresApproval: true},
		})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.SubmitPromotionWorkflow(context.Background(), PromotionWorkflowRequest{
		Promotion: PromotionRequest{
			PromotionID:         "promo_1",
			SandboxID:           "sbx_1",
			TargetPath:          "internal/example.go",
			DiffPath:            "sandbox/diff.patch",
			TestResultPath:      "sandbox/test.log",
			Reason:              "verified patch",
			RollbackPlanPath:    "sandbox/rollback.md",
			HumanApprovalStatus: "granted",
			CreatedAt:           time.Now().UTC(),
		},
		ApplyAfterApproval: true,
		HumanApproved:      true,
		ExternalControl: &ExternalControlRequest{
			Actor:     "Worker",
			ChannelID: "viewer",
			Action:    "promotion_apply",
		},
	})
	if err != nil {
		t.Fatalf("SubmitPromotionWorkflow() error = %v", err)
	}
	if resp.Applied || resp.SkippedReason != "external control policy did not allow action" {
		t.Fatalf("workflow response=%#v", resp)
	}
	if len(paths) != 1 || paths[0] != "/viewer/ai-workflow/external-control/check" {
		t.Fatalf("paths=%#v", paths)
	}
}

func TestSubmitPromotionWorkflowDoesNotApplyWhenGateDoesNotApprove(t *testing.T) {
	var paths []string
	now := time.Date(2026, 5, 20, 5, 20, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/viewer/ai-workflow/external-control/check":
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected method: %s", r.Method)
			}
			var req ExternalControlRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(ExternalControlResponse{
				Request:  req,
				Decision: ExternalControlDecision{Status: "allowed", RequiresApproval: true},
			})
			return
		case "/viewer/sandbox/promotions":
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected method: %s", r.Method)
			}
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req PromotionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(PromotionRequestResponse{
			Promotion: req,
			Decision:  PromotionGateDecision{Status: "needs_more_tests", Reason: "missing test", MissingRequirements: []string{"test_result_path"}},
			GateLog:   PromotionGateLog{EventID: "evt_1", PromotionID: req.PromotionID, GateStatus: "needs_more_tests", CreatedAt: now},
		})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.SubmitPromotionWorkflow(context.Background(), PromotionWorkflowRequest{
		Promotion: PromotionRequest{
			PromotionID:         "promo_1",
			SandboxID:           "sbx_1",
			TargetPath:          "internal/example.go",
			DiffPath:            "sandbox/diff.patch",
			Reason:              "verified patch",
			RollbackPlanPath:    "sandbox/rollback.md",
			HumanApprovalStatus: "granted",
			CreatedAt:           time.Now().UTC(),
		},
		ApplyAfterApproval:        true,
		PostApplyVerificationPath: "sandbox/post-apply.log",
		HumanApproved:             true,
		ExternalControl: &ExternalControlRequest{
			Actor:         "Worker",
			ChannelID:     "viewer",
			Action:        "promotion_apply",
			HumanApproved: true,
		},
	})
	if err != nil {
		t.Fatalf("SubmitPromotionWorkflow() error = %v", err)
	}
	if resp.Applied || resp.SkippedReason != "promotion gate did not approve" {
		t.Fatalf("workflow response=%#v", resp)
	}
	if len(paths) != 2 || paths[0] != "/viewer/ai-workflow/external-control/check" || paths[1] != "/viewer/sandbox/promotions" {
		t.Fatalf("paths=%#v", paths)
	}
}

func TestSubmitPromotionWorkflowAppliesOnlyAfterGateAndApproval(t *testing.T) {
	var paths []string
	now := time.Date(2026, 5, 20, 5, 20, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/viewer/ai-workflow/external-control/check":
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected method: %s", r.Method)
			}
			var req ExternalControlRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(ExternalControlResponse{
				Request:  req,
				Decision: ExternalControlDecision{Status: "allowed", RequiresApproval: true},
			})
		case "/viewer/sandbox/promotions":
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected method: %s", r.Method)
			}
			var req PromotionRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(PromotionRequestResponse{
				Promotion: req,
				Decision:  PromotionGateDecision{Status: "approve", Reason: "ok"},
				GateLog:   PromotionGateLog{EventID: "evt_1", PromotionID: req.PromotionID, GateStatus: "approve", CreatedAt: now},
			})
		case "/viewer/sandbox/promotions/apply":
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected method: %s", r.Method)
			}
			var req PromotionApplyRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			if req.Promotion.PromotionID != "promo_1" || !req.HumanApproved || req.PostApplyVerificationPath != "sandbox/post-apply.log" {
				t.Fatalf("apply payload=%#v", req)
			}
			_ = json.NewEncoder(w).Encode(PromotionApplyResponse{
				Decision:                      PromotionGateDecision{Status: "promotion_applied", Reason: "recorded"},
				DiffApplyResult:               &PromotionDiffApplyResult{Status: "applied", AppliedFiles: []string{"internal/example.go"}},
				GateLog:                       PromotionGateLog{EventID: "evt_apply_1", PromotionID: req.Promotion.PromotionID, GateStatus: "promotion_applied", PostApplyVerification: req.PostApplyVerificationPath, CreatedAt: now},
				PostApplyVerificationArtifact: SandboxArtifact{ArtifactID: "art_post_apply_1", SandboxID: req.Promotion.SandboxID, Type: "post_apply_verification", FilePath: "sandbox/post-apply.log", Status: "completed", CreatedAt: now},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.SubmitPromotionWorkflow(context.Background(), PromotionWorkflowRequest{
		Promotion: PromotionRequest{
			PromotionID:         "promo_1",
			SandboxID:           "sbx_1",
			TargetPath:          "internal/example.go",
			DiffPath:            "sandbox/diff.patch",
			TestResultPath:      "sandbox/test.log",
			Reason:              "verified patch",
			RollbackPlanPath:    "sandbox/rollback.md",
			HumanApprovalStatus: "granted",
			CreatedAt:           time.Now().UTC(),
		},
		ApplyAfterApproval:        true,
		AppliedBy:                 "Worker",
		PostApplyVerificationPath: "sandbox/post-apply.log",
		HumanApproved:             true,
		ExternalControl: &ExternalControlRequest{
			Actor:         "Worker",
			ChannelID:     "viewer",
			Action:        "promotion_apply",
			HumanApproved: true,
		},
	})
	if err != nil {
		t.Fatalf("SubmitPromotionWorkflow() error = %v", err)
	}
	if !resp.Applied || resp.ApplyResponse == nil || resp.ApplyResponse.Decision.Status != "promotion_applied" {
		t.Fatalf("workflow response=%#v", resp)
	}
	if len(paths) != 3 || paths[0] != "/viewer/ai-workflow/external-control/check" || paths[1] != "/viewer/sandbox/promotions" || paths[2] != "/viewer/sandbox/promotions/apply" {
		t.Fatalf("paths=%#v", paths)
	}
}

func TestSubmitPromotionWorkflowDoesNotMarkMalformedApplyResponseApplied(t *testing.T) {
	var paths []string
	now := time.Date(2026, 5, 20, 5, 20, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/viewer/ai-workflow/external-control/check":
			var req ExternalControlRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(ExternalControlResponse{
				Request:  req,
				Decision: ExternalControlDecision{Status: "allowed", RequiresApproval: true},
			})
		case "/viewer/sandbox/promotions":
			var req PromotionRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(PromotionRequestResponse{
				Promotion: req,
				Decision:  PromotionGateDecision{Status: "approve", Reason: "ok"},
				GateLog:   PromotionGateLog{EventID: "evt_1", PromotionID: req.PromotionID, GateStatus: "approve", CreatedAt: now},
			})
		case "/viewer/sandbox/promotions/apply":
			_ = json.NewEncoder(w).Encode(PromotionApplyResponse{
				Decision:                      PromotionGateDecision{Status: "promotion_applied", Reason: "recorded"},
				GateLog:                       PromotionGateLog{EventID: "evt_apply_1", PromotionID: "promo_1", GateStatus: "promotion_applied"},
				PostApplyVerificationArtifact: SandboxArtifact{ArtifactID: "art_post_apply_1", SandboxID: "sbx_1", Type: "post_apply_verification", Status: "completed"},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.SubmitPromotionWorkflow(context.Background(), PromotionWorkflowRequest{
		Promotion: PromotionRequest{
			PromotionID:         "promo_1",
			SandboxID:           "sbx_1",
			TargetPath:          "internal/example.go",
			DiffPath:            "sandbox/diff.patch",
			TestResultPath:      "sandbox/test.log",
			Reason:              "verified patch",
			RollbackPlanPath:    "sandbox/rollback.md",
			HumanApprovalStatus: "granted",
			CreatedAt:           time.Now().UTC(),
		},
		ApplyAfterApproval:        true,
		AppliedBy:                 "Worker",
		PostApplyVerificationPath: "sandbox/post-apply.log",
		HumanApproved:             true,
		ExternalControl: &ExternalControlRequest{
			Actor:         "Worker",
			ChannelID:     "viewer",
			Action:        "promotion_apply",
			HumanApproved: true,
		},
	})
	if err != nil {
		t.Fatalf("SubmitPromotionWorkflow() error = %v", err)
	}
	if resp.Applied || resp.ApplyResponse == nil || !strings.Contains(resp.SkippedReason, "artifact path mismatch") {
		t.Fatalf("workflow response=%#v, want not applied with artifact path mismatch", resp)
	}
	if len(paths) != 3 {
		t.Fatalf("paths=%#v", paths)
	}
}

func TestSubmitSkillGovernanceExternalPRSendsApprovalGatedAuditRequest(t *testing.T) {
	now := time.Date(2026, 5, 20, 4, 35, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/viewer/skill-governance/external-pr-submit" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req SkillGovernanceExternalPRSubmitRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.SubmitID != "submit_1" || req.ContributionEventID != "evt_contrib_1" || !req.HumanApproved {
			t.Fatalf("payload=%#v", req)
		}
		_ = json.NewEncoder(w).Encode(SkillGovernanceExternalPRSubmitResponse{
			Record: SkillGovernanceExternalPRSubmitRecord{
				SubmitID:            req.SubmitID,
				ContributionEventID: req.ContributionEventID,
				Repo:                req.Repo,
				Title:               req.Title,
				ApprovalStatus:      "approved",
				HumanApproved:       true,
				SubmitStatus:        "blocked",
				FailureReason:       "external PR adapter is not configured",
				ExternalPRCreated:   false,
				PostSubmitVerified:  false,
				CreatedAt:           now,
			},
			ExternalPRCreated:              false,
			PostSubmitVerified:             false,
			HumanApprovalRequiredForPR:     true,
			ExternalPRAdapterConfiguration: "required",
			Message:                        "external PR adapter is not configured; no PR was created",
		})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.SubmitSkillGovernanceExternalPR(context.Background(), SkillGovernanceExternalPRSubmitRequest{
		SubmitID:            "submit_1",
		ContributionEventID: "evt_contrib_1",
		Repo:                "example/repo",
		Title:               "Fix bug",
		HumanApproved:       true,
	})
	if err != nil {
		t.Fatalf("SubmitSkillGovernanceExternalPR() error = %v", err)
	}
	if resp.ExternalPRCreated || resp.Record.SubmitStatus != "blocked" {
		t.Fatalf("response=%#v", resp)
	}
}

func TestSubmitSkillGovernanceExternalPRRejectsInvalidRequest(t *testing.T) {
	tests := []struct {
		name string
		item SkillGovernanceExternalPRSubmitRequest
		want string
	}{
		{name: "missing submit", item: SkillGovernanceExternalPRSubmitRequest{ContributionEventID: "evt_1", Repo: "example/repo", Title: "Fix bug"}, want: "missing submit_id"},
		{name: "missing gate", item: SkillGovernanceExternalPRSubmitRequest{SubmitID: "submit_1", Repo: "example/repo", Title: "Fix bug"}, want: "missing contribution_event_id"},
		{name: "missing repo", item: SkillGovernanceExternalPRSubmitRequest{SubmitID: "submit_1", ContributionEventID: "evt_1", Title: "Fix bug"}, want: "missing repo"},
		{name: "missing title", item: SkillGovernanceExternalPRSubmitRequest{SubmitID: "submit_1", ContributionEventID: "evt_1", Repo: "example/repo"}, want: "missing title"},
		{name: "missing human approval", item: SkillGovernanceExternalPRSubmitRequest{SubmitID: "submit_1", ContributionEventID: "evt_1", Repo: "example/repo", Title: "Fix bug"}, want: "requires human_approved"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, called, cleanup := newNoRequestClient(t)
			defer cleanup()
			_, err := client.SubmitSkillGovernanceExternalPR(context.Background(), tt.item)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("SubmitSkillGovernanceExternalPR() error = %v, want %q", err, tt.want)
			}
			if *called {
				t.Fatal("server was called for invalid request")
			}
		})
	}
}

func TestSubmitSkillGovernanceExternalPRRejectsMalformedCreatedResponse(t *testing.T) {
	tests := []struct {
		name string
		resp SkillGovernanceExternalPRSubmitResponse
		want string
	}{
		{
			name: "submit id mismatch",
			resp: SkillGovernanceExternalPRSubmitResponse{
				Record: SkillGovernanceExternalPRSubmitRecord{SubmitID: "other", ContributionEventID: "evt_contrib_1", Repo: "example/repo", SubmitStatus: "blocked"},
			},
			want: "submit_id mismatch",
		},
		{
			name: "contribution event mismatch",
			resp: SkillGovernanceExternalPRSubmitResponse{
				Record: SkillGovernanceExternalPRSubmitRecord{SubmitID: "submit_1", ContributionEventID: "other", Repo: "example/repo", SubmitStatus: "blocked"},
			},
			want: "contribution_event_id mismatch",
		},
		{
			name: "repo mismatch",
			resp: SkillGovernanceExternalPRSubmitResponse{
				Record: SkillGovernanceExternalPRSubmitRecord{SubmitID: "submit_1", ContributionEventID: "evt_contrib_1", Repo: "other/repo", SubmitStatus: "blocked"},
			},
			want: "repo mismatch",
		},
		{
			name: "state mismatch",
			resp: SkillGovernanceExternalPRSubmitResponse{
				Record: SkillGovernanceExternalPRSubmitRecord{
					SubmitID:            "submit_1",
					ContributionEventID: "evt_contrib_1",
					Repo:                "example/repo",
					Title:               "Fix bug",
					ApprovalStatus:      "approved",
					HumanApproved:       true,
					SubmitStatus:        "blocked",
					FailureReason:       "external PR adapter is not configured",
					ExternalPRCreated:   false,
					PostSubmitVerified:  false,
				},
				ExternalPRCreated:          true,
				PostSubmitVerified:         true,
				HumanApprovalRequiredForPR: true,
			},
			want: "external PR submit response mismatch",
		},
		{
			name: "title mismatch",
			resp: SkillGovernanceExternalPRSubmitResponse{
				Record: SkillGovernanceExternalPRSubmitRecord{SubmitID: "submit_1", ContributionEventID: "evt_contrib_1", Repo: "example/repo", Title: "Other title", SubmitStatus: "blocked"},
			},
			want: "title mismatch",
		},
		{
			name: "missing human approval requirement",
			resp: SkillGovernanceExternalPRSubmitResponse{
				Record: SkillGovernanceExternalPRSubmitRecord{SubmitID: "submit_1", ContributionEventID: "evt_contrib_1", Repo: "example/repo", Title: "Fix bug", ApprovalStatus: "approved", HumanApproved: true, SubmitStatus: "blocked", FailureReason: "external PR adapter is not configured"},
			},
			want: "missing human approval requirement",
		},
		{
			name: "record missing human approval",
			resp: SkillGovernanceExternalPRSubmitResponse{
				Record:                     SkillGovernanceExternalPRSubmitRecord{SubmitID: "submit_1", ContributionEventID: "evt_contrib_1", Repo: "example/repo", Title: "Fix bug", ApprovalStatus: "approved", SubmitStatus: "blocked", FailureReason: "external PR adapter is not configured"},
				HumanApprovalRequiredForPR: true,
			},
			want: "record missing human approval",
		},
		{
			name: "approval status not approved",
			resp: SkillGovernanceExternalPRSubmitResponse{
				Record:                     SkillGovernanceExternalPRSubmitRecord{SubmitID: "submit_1", ContributionEventID: "evt_contrib_1", Repo: "example/repo", Title: "Fix bug", ApprovalStatus: "pending", HumanApproved: true, SubmitStatus: "blocked", FailureReason: "external PR adapter is not configured"},
				HumanApprovalRequiredForPR: true,
			},
			want: "approval_status must be approved",
		},
		{
			name: "blocked without failure reason",
			resp: SkillGovernanceExternalPRSubmitResponse{
				Record: SkillGovernanceExternalPRSubmitRecord{
					SubmitID:            "submit_1",
					ContributionEventID: "evt_contrib_1",
					Repo:                "example/repo",
					Title:               "Fix bug",
					ApprovalStatus:      "approved",
					HumanApproved:       true,
					SubmitStatus:        "blocked",
					ExternalPRCreated:   false,
					PostSubmitVerified:  false,
				},
				ExternalPRCreated:          false,
				PostSubmitVerified:         false,
				HumanApprovalRequiredForPR: true,
			},
			want: "without failure_reason",
		},
		{
			name: "missing created at",
			resp: SkillGovernanceExternalPRSubmitResponse{
				Record: SkillGovernanceExternalPRSubmitRecord{
					SubmitID:            "submit_1",
					ContributionEventID: "evt_contrib_1",
					Repo:                "example/repo",
					Title:               "Fix bug",
					ApprovalStatus:      "approved",
					HumanApproved:       true,
					SubmitStatus:        "blocked",
					FailureReason:       "external PR adapter is not configured",
					ExternalPRCreated:   false,
					PostSubmitVerified:  false,
				},
				ExternalPRCreated:          false,
				PostSubmitVerified:         false,
				HumanApprovalRequiredForPR: true,
			},
			want: "record missing created_at",
		},
		{
			name: "required pr adapter but record configured",
			resp: SkillGovernanceExternalPRSubmitResponse{
				Record: SkillGovernanceExternalPRSubmitRecord{
					SubmitID:            "submit_1",
					ContributionEventID: "evt_contrib_1",
					Repo:                "example/repo",
					Title:               "Fix bug",
					ApprovalStatus:      "approved",
					HumanApproved:       true,
					SubmitStatus:        "blocked",
					FailureReason:       "external PR adapter is not configured",
					ExternalPRCreated:   false,
					PostSubmitVerified:  false,
					PRAdapter:           "github",
				},
				ExternalPRCreated:              false,
				PostSubmitVerified:             false,
				HumanApprovalRequiredForPR:     true,
				ExternalPRAdapterConfiguration: "required",
			},
			want: "pr_adapter conflicts",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/viewer/skill-governance/external-pr-submit" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(tt.resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.SubmitSkillGovernanceExternalPR(context.Background(), SkillGovernanceExternalPRSubmitRequest{
				SubmitID:            "submit_1",
				ContributionEventID: "evt_contrib_1",
				Repo:                "example/repo",
				Title:               "Fix bug",
				HumanApproved:       true,
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("SubmitSkillGovernanceExternalPR() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestSkillGovernanceStatus(t *testing.T) {
	var gotPath string
	now := time.Date(2026, 5, 20, 4, 15, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		if r.Method != http.MethodGet {
			t.Fatalf("method=%s", r.Method)
		}
		_ = json.NewEncoder(w).Encode(SkillGovernanceStatus{
			Manifests: []SkillGovernanceManifest{{
				SkillID: "skill_1",
				Name:    "Example",
				Scope:   "project",
				Path:    ".agents/skills/example/SKILL.md",
				Enabled: true,
			}},
			TriggerLogs: []SkillGovernanceTriggerLog{{
				EventID:     "evt_trigger_1",
				SkillID:     "skill_1",
				TriggerType: "keyword",
				Status:      "triggered",
				CreatedAt:   now,
			}},
			ChangeLogs: []SkillGovernanceChangeLog{{
				ChangeID:  "change_1",
				SkillID:   "skill_1",
				CreatedAt: now,
			}},
			Contributions: []SkillGovernanceContributionGateLog{{
				EventID:    "evt_contrib_1",
				Repo:       "example/repo",
				GateStatus: "passed",
				CreatedAt:  now,
			}},
			ExternalPRSubmitRecords: []SkillGovernanceExternalPRSubmitRecord{{
				SubmitID:            "submit_1",
				ContributionEventID: "evt_contrib_1",
				Repo:                "example/repo",
				Title:               "Fix bug",
				ApprovalStatus:      "approved",
				HumanApproved:       true,
				SubmitStatus:        "blocked",
				FailureReason:       "external PR adapter is not configured",
				CreatedAt:           now,
			}},
			ExternalPRAdapter:           "unconfigured",
			ExternalPRAdapterConfigured: boolPtr(false),
			HumanApprovalRequiredForPR:  boolPtr(true),
			CoderTranscripts: []SkillGovernanceCoderTranscript{{
				EventID:   "evt_transcript_1",
				JobID:     "job_1",
				Role:      "assistant",
				Segment:   "final",
				CreatedAt: now,
			}, {
				EventID:      "evt_transcript_2",
				JobID:        "job_1",
				Role:         "coder",
				Segment:      "patch_evidence",
				EvidencePath: "workspace/logs/skill_governance/coder_evidence/job_1/skill_diff.md",
				CreatedAt:    now,
			}, {
				EventID:      "evt_transcript_3",
				JobID:        "job_1",
				Role:         "system",
				Segment:      "transcript_evidence",
				EvidencePath: "workspace/logs/skill_governance/coder_evidence/job_1/agent_transcript.md",
				CreatedAt:    now,
			}},
		})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.SkillGovernanceStatus(context.Background(), 6)
	if err != nil {
		t.Fatalf("SkillGovernanceStatus() error = %v", err)
	}
	if gotPath != "/viewer/skill-governance/recent?limit=6" {
		t.Fatalf("path=%s", gotPath)
	}
	if len(status.ExternalPRSubmitRecords) != 1 || status.ExternalPRSubmitRecords[0].SubmitID != "submit_1" {
		t.Fatalf("status=%#v", status)
	}
	if status.ExternalPRAdapter != "unconfigured" || status.ExternalPRAdapterConfigured == nil || *status.ExternalPRAdapterConfigured || status.HumanApprovalRequiredForPR == nil || !*status.HumanApprovalRequiredForPR {
		t.Fatalf("external PR readiness=%#v", status)
	}
}

func TestSkillGovernanceStatusRejectsMalformedCurrentView(t *testing.T) {
	now := time.Date(2026, 5, 20, 4, 15, 0, 0, time.UTC)
	valid := func() SkillGovernanceStatus {
		return SkillGovernanceStatus{
			ExternalPRAdapter:           "unconfigured",
			ExternalPRAdapterConfigured: boolPtr(false),
			HumanApprovalRequiredForPR:  boolPtr(true),
			Manifests: []SkillGovernanceManifest{{
				SkillID: "skill_1",
				Name:    "Example",
				Scope:   "project",
				Path:    ".agents/skills/example/SKILL.md",
				Enabled: true,
			}},
			TriggerLogs: []SkillGovernanceTriggerLog{{
				EventID:     "evt_trigger_1",
				SkillID:     "skill_1",
				TriggerType: "keyword",
				Status:      "triggered",
				CreatedAt:   now,
			}},
			ChangeLogs: []SkillGovernanceChangeLog{{
				ChangeID:  "change_1",
				SkillID:   "skill_1",
				CreatedAt: now,
			}},
			Contributions: []SkillGovernanceContributionGateLog{{
				EventID:    "evt_contrib_1",
				Repo:       "example/repo",
				GateStatus: "passed",
				CreatedAt:  now,
			}},
			ExternalPRSubmitRecords: []SkillGovernanceExternalPRSubmitRecord{{
				SubmitID:            "submit_1",
				ContributionEventID: "evt_contrib_1",
				Repo:                "example/repo",
				Title:               "Fix bug",
				ApprovalStatus:      "approved",
				HumanApproved:       true,
				SubmitStatus:        "blocked",
				FailureReason:       "external PR adapter is not configured",
				CreatedAt:           now,
			}},
			CoderTranscripts: []SkillGovernanceCoderTranscript{{
				EventID:   "evt_transcript_1",
				JobID:     "job_1",
				Role:      "assistant",
				Segment:   "final",
				CreatedAt: now,
			}, {
				EventID:      "evt_transcript_2",
				JobID:        "job_1",
				Role:         "coder",
				Segment:      "patch_evidence",
				EvidencePath: "workspace/logs/skill_governance/coder_evidence/job_1/skill_diff.md",
				CreatedAt:    now,
			}, {
				EventID:      "evt_transcript_3",
				JobID:        "job_1",
				Role:         "system",
				Segment:      "transcript_evidence",
				EvidencePath: "workspace/logs/skill_governance/coder_evidence/job_1/agent_transcript.md",
				CreatedAt:    now,
			}},
		}
	}
	tests := []struct {
		name   string
		mutate func(*SkillGovernanceStatus)
		want   string
	}{
		{name: "duplicate manifest", mutate: func(s *SkillGovernanceStatus) {
			s.Manifests = append(s.Manifests, s.Manifests[0])
		}, want: "duplicate manifest"},
		{name: "missing external pr adapter", mutate: func(s *SkillGovernanceStatus) {
			s.ExternalPRAdapter = ""
		}, want: "missing external_pr_adapter"},
		{name: "missing external pr configured", mutate: func(s *SkillGovernanceStatus) {
			s.ExternalPRAdapterConfigured = nil
		}, want: "missing external PR readiness"},
		{name: "external pr without human approval requirement", mutate: func(s *SkillGovernanceStatus) {
			*s.HumanApprovalRequiredForPR = false
		}, want: "must require human approval"},
		{name: "unconfigured adapter marked configured", mutate: func(s *SkillGovernanceStatus) {
			*s.ExternalPRAdapterConfigured = true
		}, want: "conflicts with unconfigured"},
		{name: "missing trigger status", mutate: func(s *SkillGovernanceStatus) {
			s.TriggerLogs[0].Status = ""
		}, want: "missing status"},
		{name: "trigger missing created at", mutate: func(s *SkillGovernanceStatus) {
			s.TriggerLogs[0].CreatedAt = time.Time{}
		}, want: "trigger_log evt_trigger_1 missing created_at"},
		{name: "change missing created at", mutate: func(s *SkillGovernanceStatus) {
			s.ChangeLogs[0].CreatedAt = time.Time{}
		}, want: "change_log change_1 missing created_at"},
		{name: "duplicate contribution", mutate: func(s *SkillGovernanceStatus) {
			s.Contributions = append(s.Contributions, s.Contributions[0])
		}, want: "duplicate contribution"},
		{name: "contribution missing created at", mutate: func(s *SkillGovernanceStatus) {
			s.Contributions[0].CreatedAt = time.Time{}
		}, want: "contribution evt_contrib_1 missing created_at"},
		{name: "created pr without url", mutate: func(s *SkillGovernanceStatus) {
			s.ExternalPRSubmitRecords[0].ExternalPRCreated = true
			s.ExternalPRSubmitRecords[0].SubmitStatus = "created"
			s.ExternalPRSubmitRecords[0].PostSubmitVerified = true
			s.ExternalPRSubmitRecords[0].PostSubmitEvidence = "verified"
		}, want: "without pr_url"},
		{name: "external pr submit missing created at", mutate: func(s *SkillGovernanceStatus) {
			s.ExternalPRSubmitRecords[0].CreatedAt = time.Time{}
		}, want: "external_pr_submit_record submit_1 missing created_at"},
		{name: "created status without external pr created", mutate: func(s *SkillGovernanceStatus) {
			s.ExternalPRSubmitRecords[0].SubmitStatus = "created"
		}, want: "created without external_pr_created"},
		{name: "invalid submit status", mutate: func(s *SkillGovernanceStatus) {
			s.ExternalPRSubmitRecords[0].SubmitStatus = "submitted"
		}, want: "invalid submit_status"},
		{name: "external pr approval status not approved", mutate: func(s *SkillGovernanceStatus) {
			s.ExternalPRSubmitRecords[0].ApprovalStatus = "pending"
		}, want: "approval_status must be approved"},
		{name: "external pr missing human approval", mutate: func(s *SkillGovernanceStatus) {
			s.ExternalPRSubmitRecords[0].HumanApproved = false
		}, want: "missing human approval"},
		{name: "external pr missing title", mutate: func(s *SkillGovernanceStatus) {
			s.ExternalPRSubmitRecords[0].Title = ""
		}, want: "missing title"},
		{name: "uncreated external pr without failure reason", mutate: func(s *SkillGovernanceStatus) {
			s.ExternalPRSubmitRecords[0].FailureReason = ""
		}, want: "missing failure_reason for uncreated external PR"},
		{name: "unconfigured pr adapter with configured record adapter", mutate: func(s *SkillGovernanceStatus) {
			s.ExternalPRSubmitRecords[0].PRAdapter = "github"
		}, want: "pr_adapter conflicts"},
		{name: "pr url without external pr created", mutate: func(s *SkillGovernanceStatus) {
			s.ExternalPRSubmitRecords[0].PRURL = "https://github.com/example/repo/pull/1"
		}, want: "pr_url without external_pr_created"},
		{name: "post submit verified without external pr created", mutate: func(s *SkillGovernanceStatus) {
			s.ExternalPRSubmitRecords[0].PostSubmitVerified = true
			s.ExternalPRSubmitRecords[0].PostSubmitEvidence = "external/github/post-submit.log"
		}, want: "post-submit verification without external_pr_created"},
		{name: "duplicate transcript", mutate: func(s *SkillGovernanceStatus) {
			s.CoderTranscripts = append(s.CoderTranscripts, s.CoderTranscripts[0])
		}, want: "duplicate coder_transcript"},
		{name: "transcript missing created at", mutate: func(s *SkillGovernanceStatus) {
			s.CoderTranscripts[0].CreatedAt = time.Time{}
		}, want: "coder_transcript evt_transcript_1 missing created_at"},
		{name: "evidence transcript without evidence path", mutate: func(s *SkillGovernanceStatus) {
			s.CoderTranscripts[1].EvidencePath = ""
		}, want: "patch_evidence missing evidence_path"},
		{name: "evidence transcript with traversal path", mutate: func(s *SkillGovernanceStatus) {
			s.CoderTranscripts[1].EvidencePath = "../workspace/logs/skill_diff.md"
		}, want: "invalid evidence_path"},
		{name: "incomplete evidence pair", mutate: func(s *SkillGovernanceStatus) {
			s.CoderTranscripts = s.CoderTranscripts[:2]
		}, want: "incomplete evidence pair"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := valid()
			tt.mutate(&resp)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/viewer/skill-governance/recent" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.SkillGovernanceStatus(context.Background(), 0)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("SkillGovernanceStatus() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestEvaluateSkillGovernanceContributionGate(t *testing.T) {
	now := time.Date(2026, 5, 20, 4, 50, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/viewer/skill-governance/contribution-gate" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req SkillGovernanceContributionGateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.EventID != "evt_contrib_1" || req.Repo != "example/repo" || !req.DiffHumanApproved {
			t.Fatalf("payload=%#v", req)
		}
		_ = json.NewEncoder(w).Encode(SkillGovernanceContributionGateResponse{
			GateLog: SkillGovernanceContributionGateLog{
				EventID:    req.EventID,
				Repo:       req.Repo,
				GateStatus: "passed",
				CreatedAt:  now,
			},
			Decision: SkillGovernanceContributionGateDecision{
				Status:        "passed",
				CanContribute: true,
			},
		})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.EvaluateSkillGovernanceContributionGate(context.Background(), SkillGovernanceContributionGateRequest{
		EventID:             "evt_contrib_1",
		Repo:                "example/repo",
		ProblemStatement:    "real bug",
		ExistingPRsChecked:  true,
		RealProblemVerified: true,
		CoreChangeVerified:  true,
		DiffHumanApproved:   true,
		TestResult:          "go test ./...",
	})
	if err != nil {
		t.Fatalf("EvaluateSkillGovernanceContributionGate() error = %v", err)
	}
	if resp.Decision.Status != "passed" || !resp.Decision.CanContribute {
		t.Fatalf("response=%#v", resp)
	}
}

func TestEvaluateSkillGovernanceContributionGateRejectsInvalidRequest(t *testing.T) {
	client, called, cleanup := newNoRequestClient(t)
	defer cleanup()
	_, err := client.EvaluateSkillGovernanceContributionGate(context.Background(), SkillGovernanceContributionGateRequest{})
	if err == nil || !strings.Contains(err.Error(), "missing repo") {
		t.Fatalf("EvaluateSkillGovernanceContributionGate() error = %v, want missing repo", err)
	}
	if *called {
		t.Fatal("server was called for invalid request")
	}
}

func TestEvaluateSkillGovernanceContributionGateRejectsMalformedResponse(t *testing.T) {
	tests := []struct {
		name string
		resp SkillGovernanceContributionGateResponse
		want string
	}{
		{
			name: "event mismatch",
			resp: SkillGovernanceContributionGateResponse{
				GateLog:  SkillGovernanceContributionGateLog{EventID: "other", Repo: "example/repo", GateStatus: "passed"},
				Decision: SkillGovernanceContributionGateDecision{Status: "passed", CanContribute: true},
			},
			want: "event_id mismatch",
		},
		{
			name: "repo mismatch",
			resp: SkillGovernanceContributionGateResponse{
				GateLog:  SkillGovernanceContributionGateLog{EventID: "evt_contrib_1", Repo: "other/repo", GateStatus: "passed"},
				Decision: SkillGovernanceContributionGateDecision{Status: "passed", CanContribute: true},
			},
			want: "repo mismatch",
		},
		{
			name: "status mismatch",
			resp: SkillGovernanceContributionGateResponse{
				GateLog:  SkillGovernanceContributionGateLog{EventID: "evt_contrib_1", Repo: "example/repo", GateStatus: "passed"},
				Decision: SkillGovernanceContributionGateDecision{Status: "blocked", StopReasons: []string{"missing test"}},
			},
			want: "status mismatch",
		},
		{
			name: "passed without can contribute",
			resp: SkillGovernanceContributionGateResponse{
				GateLog:  SkillGovernanceContributionGateLog{EventID: "evt_contrib_1", Repo: "example/repo", GateStatus: "passed"},
				Decision: SkillGovernanceContributionGateDecision{Status: "passed"},
			},
			want: "passed without can_contribute",
		},
		{
			name: "blocked without reasons",
			resp: SkillGovernanceContributionGateResponse{
				GateLog:  SkillGovernanceContributionGateLog{EventID: "evt_contrib_1", Repo: "example/repo", GateStatus: "blocked"},
				Decision: SkillGovernanceContributionGateDecision{Status: "blocked"},
			},
			want: "blocked without stop reasons",
		},
		{
			name: "missing created at",
			resp: SkillGovernanceContributionGateResponse{
				GateLog:  SkillGovernanceContributionGateLog{EventID: "evt_contrib_1", Repo: "example/repo", GateStatus: "passed"},
				Decision: SkillGovernanceContributionGateDecision{Status: "passed", CanContribute: true},
			},
			want: "gate log missing created_at",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/viewer/skill-governance/contribution-gate" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(tt.resp)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.EvaluateSkillGovernanceContributionGate(context.Background(), SkillGovernanceContributionGateRequest{
				EventID:             "evt_contrib_1",
				Repo:                "example/repo",
				ProblemStatement:    "real bug",
				ExistingPRsChecked:  true,
				RealProblemVerified: true,
				CoreChangeVerified:  true,
				DiffHumanApproved:   true,
				TestResult:          "go test ./...",
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("EvaluateSkillGovernanceContributionGate() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestAPIErrorIncludesStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no store", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.SuperAgentStatus(context.Background(), 0)
	if err == nil {
		t.Fatal("expected API error")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("error type = %T, want *APIError", err)
	}
	if apiErr.Method != http.MethodGet || apiErr.Path != "/viewer/superagent" {
		t.Fatalf("APIError route = %s %s", apiErr.Method, apiErr.Path)
	}
	if apiErr.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("APIError status = %d", apiErr.StatusCode)
	}
	if !strings.Contains(apiErr.Body, "no store") {
		t.Fatalf("APIError body = %q", apiErr.Body)
	}
	if !strings.Contains(apiErr.Error(), "status=503") || !strings.Contains(apiErr.Error(), "no store") {
		t.Fatalf("APIError string = %q", apiErr.Error())
	}
}

func TestAdvisorsStatusReturnsValidatedReadModel(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
  "enabled": true,
  "status": "ok",
  "profiles": [{"ID":"codex","DisplayName":"Codex","Provider":"codex","Capabilities":[],"AllowedModes":["advice_only"],"Disabled":false}],
  "recent_runs": [{"run_id":"run-1","requested_by_agent":"shiro","advisor_id":"codex","approval_mode":"advice_only","status":"completed","summary":"safe summary","started_at":"2026-07-14T00:00:00Z","finished_at":"2026-07-14T00:00:01Z","latency_millis":1000}],
  "score_snapshots": [{"snapshot_id":"score-1","advisor_id":"codex","window_start":"2026-07-13T00:00:00Z","window_end":"2026-07-14T00:00:00Z","request_count":1,"completed_count":1,"failed_count":0,"unavailable_count":0,"adopted_count":1,"success_count":1,"avg_latency_millis":1000,"avg_revision_count":0,"score":0.9,"created_at":"2026-07-14T00:00:00Z"}],
  "agent_profiles": [],
  "policy_decisions": [{"decision_id":"decision-1","agent_id":"shiro","action":"ask_advisor","decision":"allowed","reason":"profile allows action","created_at":"2026-07-14T00:00:00Z"}],
  "warnings": [],
  "summary": {"advisor_count":1,"recent_run_count":1,"failed_run_count":0,"score_snapshot_count":1,"profile_count":0,"policy_decision_count":1}
}`)
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.AdvisorsStatus(context.Background(), 5)
	if err != nil {
		t.Fatalf("AdvisorsStatus() error = %v", err)
	}
	if gotPath != "/viewer/advisors?limit=5" {
		t.Fatalf("path=%q", gotPath)
	}
	if len(status.RecentRuns) != 1 || status.RecentRuns[0].Summary != "safe summary" {
		t.Fatalf("status=%#v", status)
	}
}

func TestAdvisorsStatusRejectsMalformedOrUnsafeResponse(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "raw output field",
			body: `{"enabled":true,"status":"ok","profiles":[],"recent_runs":[{"run_id":"run-1","requested_by_agent":"shiro","advisor_id":"codex","approval_mode":"advice_only","status":"completed","raw_output":"secret"}],"score_snapshots":[],"agent_profiles":[],"policy_decisions":[],"warnings":[],"summary":{"advisor_count":0,"recent_run_count":1,"failed_run_count":0,"score_snapshot_count":0,"profile_count":0,"policy_decision_count":0}}`,
			want: "unknown field",
		},
		{
			name: "missing run id",
			body: `{"enabled":true,"status":"ok","profiles":[],"recent_runs":[{"requested_by_agent":"shiro","advisor_id":"codex","approval_mode":"advice_only","status":"completed"}],"score_snapshots":[],"agent_profiles":[],"policy_decisions":[],"warnings":[],"summary":{"advisor_count":0,"recent_run_count":1,"failed_run_count":0,"score_snapshot_count":0,"profile_count":0,"policy_decision_count":0}}`,
			want: "run_id",
		},
		{
			name: "score out of range",
			body: `{"enabled":true,"status":"ok","profiles":[],"recent_runs":[],"score_snapshots":[{"snapshot_id":"score-1","advisor_id":"codex","score":1.2}],"agent_profiles":[],"policy_decisions":[],"warnings":[],"summary":{"advisor_count":0,"recent_run_count":0,"failed_run_count":0,"score_snapshot_count":1,"profile_count":0,"policy_decision_count":0}}`,
			want: "score",
		},
		{
			name: "negative count",
			body: `{"enabled":false,"status":"unavailable","profiles":[],"recent_runs":[],"score_snapshots":[],"agent_profiles":[],"policy_decisions":[],"warnings":["store unavailable"],"summary":{"advisor_count":-1,"recent_run_count":0,"failed_run_count":0,"score_snapshot_count":0,"profile_count":0,"policy_decision_count":0}}`,
			want: "count",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()
			client, err := New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.AdvisorsStatus(context.Background(), 0)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error=%v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestAgentProfilesStatusWarnsWhenCatalogIsIncomplete(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/viewer/agents/profiles" {
			t.Fatalf("path=%q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"profiles":[{"ID":"shiro","DisplayName":"Shiro","Role":"worker","Capabilities":[],"Goals":[],"Motivation":[],"UtilityProfile":{},"AutonomyEnvelope":{},"KnowledgeAffinity":[]}],"profile_count":1}`)
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.AgentProfilesStatus(context.Background())
	if err != nil {
		t.Fatalf("AgentProfilesStatus() error = %v", err)
	}
	if status.ProfileCount != 1 || len(status.Warnings) != 1 || !strings.Contains(status.Warnings[0], "8") {
		t.Fatalf("status=%#v", status)
	}
}

func TestKnowledgeRelationsStatusAndHopValidation(t *testing.T) {
	t.Run("summary", func(t *testing.T) {
		var gotPath string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.String()
			_, _ = io.WriteString(w, `{"enabled":true,"status":"ok","warnings":[],"summary":{"entity_count":2,"item_entity_count":3,"relation_count":1,"max_hop":2}}`)
		}))
		defer server.Close()
		client, _ := New(server.URL)
		status, err := client.KnowledgeRelationsStatus(context.Background(), 20)
		if err != nil {
			t.Fatalf("KnowledgeRelationsStatus() error=%v", err)
		}
		if gotPath != "/viewer/knowledge-relations/summary?limit=20" || status.Summary.RelationCount != 1 {
			t.Fatalf("path=%q status=%#v", gotPath, status)
		}
	})
	t.Run("reject hop three", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"enabled":true,"status":"ok","warnings":[],"summary":{"entity_count":2,"item_entity_count":3,"relation_count":1,"max_hop":2},"items":[{"item_id":"b","domain":"general","title":"B","summary":"safe","source_type":"general"}],"relations":[{"src_item_id":"a","dst_item_id":"b","relation_type":"same_entity","score":5,"evidence":"same entity","hop":3}]}`)
		}))
		defer server.Close()
		client, _ := New(server.URL)
		_, err := client.KnowledgeRelationsStatus(context.Background(), 20)
		if err == nil || !strings.Contains(err.Error(), "hop") {
			t.Fatalf("error=%v", err)
		}
	})
}

func fullRuntimeReadiness(value bool) RuntimeDependencyReadiness {
	return fullRuntimeReadinessWithConfig(value, value, value)
}

func fullRuntimeReadinessWithConfig(value bool, sttConfig bool, ttsConfig bool) RuntimeDependencyReadiness {
	return RuntimeDependencyReadiness{
		SlackCredentialsPresent:      boolPtr(value),
		SlackWebhookRegistered:       boolPtr(value),
		SlackFilePayloadPipeline:     boolPtr(value),
		DiscordCredentialsPresent:    boolPtr(value),
		DiscordWebhookRegistered:     boolPtr(value),
		DiscordFilePayloadPipeline:   boolPtr(value),
		TelegramCredentialsPresent:   boolPtr(value),
		TelegramWebhookRegistered:    boolPtr(value),
		TelegramFilePayloadPipeline:  boolPtr(value),
		STTGatewayEnvPresent:         boolPtr(value),
		STTGatewayConfigPresent:      boolPtr(sttConfig),
		TTSProviderEnvPresent:        boolPtr(value),
		TTSProviderConfigPresent:     boolPtr(ttsConfig),
		DistributedEnabled:           boolPtr(value),
		DistributedTransportsPresent: boolPtr(value),
		DistributedSSHConfigured:     boolPtr(value),
		DistributedSSHConnected:      boolPtr(value),
		DistributedLocalTransport:    boolPtr(value),
		ConversationEnabled:          boolPtr(true),
		L1SQLiteConfigPresent:        boolPtr(true),
		MemoryLayersAvailable:        boolPtr(true),
		MemoryLayersStatus:           boolPtr(true),
		SourceRegistryAvailable:      boolPtr(true),
		SourceRegistryStatus:         boolPtr(true),
		DomainGraphAvailable:         boolPtr(true),
		DomainGraphStatus:            boolPtr(true),
		KnowledgeMemoryEnabled:       boolPtr(true),
		KnowledgeMemoryStatus:        boolPtr(true),
		BrowserTraceAPIEnabled:       boolPtr(true),
		BrowserTraceAPIStatus:        boolPtr(true),
		BrowserTraceAPIFetcher:       boolPtr(true),
		SandboxEnabled:               boolPtr(false),
		SandboxStatusAvailable:       boolPtr(true),
	}
}

func boolPtr(value bool) *bool {
	return &value
}

func intPtr(value int) *int {
	return &value
}
