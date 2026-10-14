package stt

import (
	"errors"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestParseStreamHealthReadsTheStreamObjectOfTheGatewayHealth(t *testing.T) {
	body := []byte(`{"status":"ok","target_health":{"ready":true},"stream":{"protocol":"stt.stream.v1","enabled":true,` +
		`"connections":2,"max_connections":32,"pending_jobs":1,"fail_fast":true,"fail_fast_reason":"gate_expired"}}`)
	health, err := ParseStreamHealth(body)
	if err != nil {
		t.Fatalf("ParseStreamHealth() = %v", err)
	}
	want := StreamHealth{Protocol: StreamProtocol, Enabled: true, Connections: 2, MaxConnections: 32, PendingJobs: 1, FailFast: true, FailFastReason: "gate_expired"}
	if health != want {
		t.Fatalf("ParseStreamHealth() = %#v, want %#v", health, want)
	}
}

func TestParseStreamHealthTellsAnOldGatewayFromABrokenAnswer(t *testing.T) {
	if _, err := ParseStreamHealth([]byte(`{"status":"ok","target_health":{"ready":true}}`)); !errors.Is(err, ErrStreamNotSupported) {
		t.Fatalf("a health without a stream object is an old gateway: %v", err)
	}
	if _, err := ParseStreamHealth([]byte(`{"stream":"yes"}`)); !errors.Is(err, ErrStreamNotSupported) {
		t.Fatalf("a stream that is not an object is not the stream report: %v", err)
	}
	if _, err := ParseStreamHealth([]byte(`<html>`)); err == nil || errors.Is(err, ErrStreamNotSupported) {
		t.Fatalf("a body that is not JSON is an unreachable gateway, not an old one: %v", err)
	}
}

func TestBuildStreamHealthReportMapsEveryStateToItsStatusAndDetail(t *testing.T) {
	ok := StreamHealth{Protocol: StreamProtocol, Enabled: true, Connections: 1, MaxConnections: 32}
	tests := []struct {
		name       string
		result     StreamProbeResult
		wantStatus core.HealthStatus
		wantReady  bool
		wantDetail string
	}{
		{"ready", StreamProbeResult{Health: ok}, core.HealthReady, true, ""},
		{"unreachable", StreamProbeResult{Err: errors.New("dial refused")}, core.HealthBlocked, false, "stream_unreachable"},
		{"old gateway", StreamProbeResult{Err: ErrStreamNotSupported}, core.HealthBlocked, false, "stream_not_supported"},
		{"disabled", StreamProbeResult{Health: StreamHealth{Protocol: StreamProtocol, Enabled: false}}, core.HealthBlocked, false, "stream_disabled"},
		{"other protocol", StreamProbeResult{Health: StreamHealth{Protocol: "stt.stream.v9", Enabled: true}}, core.HealthBlocked, false, "protocol_mismatch"},
		{"fail fast", StreamProbeResult{Health: StreamHealth{Protocol: StreamProtocol, Enabled: true, FailFast: true, FailFastReason: "counted"}}, core.HealthLive, false, "fail_fast"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := BuildStreamHealthReport(tt.result)
			if report.Module != StreamHealthModule || report.Status != tt.wantStatus || report.Ready != tt.wantReady || report.Detail != tt.wantDetail {
				t.Fatalf("report = %#v", report)
			}
		})
	}
}

func TestBuildStreamHealthReportNeverCarriesTheProbeError(t *testing.T) {
	// The probe error can name the gateway URL; the report names a state only.
	report := BuildStreamHealthReport(StreamProbeResult{Err: errors.New(`Get "http://stt.example.invalid:8766/health": dial tcp: connection refused`)})
	if report.Detail != "stream_unreachable" {
		t.Fatalf("detail = %q", report.Detail)
	}
	for key, value := range report.Metadata {
		if text, ok := value.(string); ok && (strings.Contains(text, "http") || strings.Contains(text, "refused")) {
			t.Fatalf("metadata[%s] leaks the probe error: %q", key, text)
		}
	}
}

func TestBuildStreamHealthReportShowsTheStreamCountersInMetadata(t *testing.T) {
	report := BuildStreamHealthReport(StreamProbeResult{Health: StreamHealth{
		Protocol: StreamProtocol, Enabled: true, Connections: 3, MaxConnections: 32, PendingJobs: 2, FailFast: true, FailFastReason: "gate_expired",
	}})
	if report.Metadata["connections"] != 3 || report.Metadata["max_connections"] != 32 || report.Metadata["pending_jobs"] != 2 ||
		report.Metadata["fail_fast"] != true || report.Metadata["fail_fast_reason"] != "gate_expired" {
		t.Fatalf("metadata = %#v", report.Metadata)
	}
}
