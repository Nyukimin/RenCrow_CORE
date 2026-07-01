package otelexport

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestExportDryRunRedactsSecrets(t *testing.T) {
	report, err := NewService("").Export(context.Background(), ExportRequest{
		Service: "rencrow-test",
		Events: []Event{{
			Name: "worker.execution",
			Attributes: map[string]string{
				"job_id":  "job_1",
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
}

func TestExportSamplesEvents(t *testing.T) {
	report, err := NewService("").Export(context.Background(), ExportRequest{
		Events:     []Event{{Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "d"}},
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

func stringify(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}
