package otelexport

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestExportDryRunRedactsSecrets(t *testing.T) {
	traceID := modulecore.NewTraceID()
	report, err := NewService("").Export(context.Background(), ExportRequest{
		Service: "rencrow-test",
		Events: []Event{{
			Name:    "worker.execution",
			TraceID: string(traceID),
			Attributes: map[string]string{
				"task_id": "tsk_test",
				"api_key": "sk-secret",
			},
		}},
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	if report.Status != "preview" || report.Exported != 1 || len(report.RedactedKeys) != 1 {
		t.Fatalf("unexpected report: %#v", report)
	}
	payload := stringify(report.Payload)
	if strings.Contains(payload, "sk-secret") || !strings.Contains(payload, "[REDACTED]") {
		t.Fatalf("payload redaction failed: %s", payload)
	}
	if strings.Contains(payload, "trace-") {
		t.Fatalf("payload must not invent synthetic trace IDs: %s", payload)
	}
	if !strings.Contains(payload, string(traceID)) {
		t.Fatalf("payload must retain canonical trace_id: %s", payload)
	}
}

func TestExportSamplesEvents(t *testing.T) {
	traceID := modulecore.NewTraceID()
	report, err := NewService("").Export(context.Background(), ExportRequest{
		Events: []Event{
			{Name: "a", TraceID: string(traceID)},
			{Name: "b", TraceID: string(traceID)},
			{Name: "c", TraceID: string(traceID)},
			{Name: "d", TraceID: string(traceID)},
		},
		SampleRate: 0.5,
		DryRun:     true,
	})
	if err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	if report.Exported != 2 || report.Dropped != 2 {
		t.Fatalf("unexpected sampling report: %#v", report)
	}
}

func TestExportFailsWithoutCanonicalTraceID(t *testing.T) {
	_, err := NewService("").Export(context.Background(), ExportRequest{
		Events: []Event{{Name: "worker.execution"}},
		DryRun: true,
	})
	if err == nil || !strings.Contains(err.Error(), "canonical trace_id") {
		t.Fatalf("Export() error = %v, want canonical trace_id failure", err)
	}
}

func TestExportSkipsEventsWithoutCanonicalTraceID(t *testing.T) {
	traceID := modulecore.NewTraceID()
	report, err := NewService("").Export(context.Background(), ExportRequest{
		Events: []Event{
			{Name: "keep", TraceID: string(traceID)},
			{Name: "drop", TraceID: "trace-1"},
			{Name: "drop-empty"},
		},
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	if report.Exported != 1 || report.Dropped != 2 {
		t.Fatalf("unexpected report: %#v", report)
	}
	payload := stringify(report.Payload)
	if strings.Contains(payload, "trace-1") {
		t.Fatalf("invalid trace_id must not be exported: %s", payload)
	}
}

func stringify(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}
