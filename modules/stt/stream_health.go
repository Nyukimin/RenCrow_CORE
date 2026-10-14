package stt

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// StreamHealthModule is the name of the report of the STT audio stream in
// /viewer/modules/health.
const StreamHealthModule = "stt.stream"

// Detail codes of the stream report. They name a state; the cause of an
// unreachable gateway stays in the log, because it can carry the gateway URL.
const (
	StreamDetailUnreachable  = "stream_unreachable"
	StreamDetailNotSupported = "stream_not_supported"
	StreamDetailDisabled     = "stream_disabled"
	StreamDetailMismatch     = "protocol_mismatch"
	StreamDetailFailFast     = "fail_fast"
)

// ErrStreamNotSupported marks a gateway health without the stream object: the
// gateway predates the audio stream.
var ErrStreamNotSupported = errors.New("stt health has no stream object")

// StreamHealth is the "stream" object of the RenCrow_STT gateway's /health.
type StreamHealth struct {
	Protocol       string
	Enabled        bool
	Connections    int
	MaxConnections int
	PendingJobs    int
	// FailFast is true while STT refuses to call its target (open or half_open).
	FailFast       bool
	FailFastReason string
}

// StreamProbeResult is the outcome of reading the gateway's /health: the stream
// object, or why it could not be read.
type StreamProbeResult struct {
	Health StreamHealth
	Err    error
}

// ParseStreamHealth reads the stream object of a gateway /health body. A body
// that is JSON without the object is ErrStreamNotSupported; a body that is not
// JSON is an ordinary error (the gateway did not answer like a gateway).
func ParseStreamHealth(body []byte) (StreamHealth, error) {
	var envelope struct {
		Stream json.RawMessage `json:"stream"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&envelope); err != nil {
		return StreamHealth{}, fmt.Errorf("gateway health is not JSON: %w", err)
	}
	trimmed := bytes.TrimSpace(envelope.Stream)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return StreamHealth{}, ErrStreamNotSupported
	}
	var raw struct {
		Protocol       string `json:"protocol"`
		Enabled        bool   `json:"enabled"`
		Connections    int    `json:"connections"`
		MaxConnections int    `json:"max_connections"`
		PendingJobs    int    `json:"pending_jobs"`
		FailFast       bool   `json:"fail_fast"`
		FailFastReason string `json:"fail_fast_reason"`
	}
	if err := json.Unmarshal(trimmed, &raw); err != nil {
		return StreamHealth{}, fmt.Errorf("gateway health stream object is malformed: %w", err)
	}
	return StreamHealth(raw), nil
}

// BuildStreamHealthReport interprets a probe. Ready means a /stt connection can
// be relayed; blocked means it cannot; live means the stream works but STT
// currently refuses to call its target (fail-fast), so a recognition would end
// in an error.
func BuildStreamHealthReport(result StreamProbeResult) core.HealthReport {
	report := core.HealthReport{Module: StreamHealthModule}
	switch {
	case errors.Is(result.Err, ErrStreamNotSupported):
		return blockedStreamReport(report, StreamDetailNotSupported)
	case result.Err != nil:
		return blockedStreamReport(report, StreamDetailUnreachable)
	}
	health := result.Health
	report.Metadata = map[string]any{
		"protocol":         health.Protocol,
		"connections":      health.Connections,
		"max_connections":  health.MaxConnections,
		"pending_jobs":     health.PendingJobs,
		"fail_fast":        health.FailFast,
		"fail_fast_reason": health.FailFastReason,
	}
	switch {
	case !health.Enabled:
		return blockedStreamReport(report, StreamDetailDisabled)
	case health.Protocol != StreamProtocol:
		return blockedStreamReport(report, StreamDetailMismatch)
	case health.FailFast:
		report.Status, report.Ready, report.Detail = core.HealthLive, false, StreamDetailFailFast
	default:
		report.Status, report.Ready = core.HealthReady, true
	}
	return report
}

func blockedStreamReport(report core.HealthReport, detail string) core.HealthReport {
	report.Status, report.Ready, report.Detail = core.HealthBlocked, false, detail
	return report
}
