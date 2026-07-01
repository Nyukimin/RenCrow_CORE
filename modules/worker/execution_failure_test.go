package worker

import "testing"

func TestClassifyExecutionFailureMetadata(t *testing.T) {
	tests := []struct {
		name      string
		errText   string
		output    string
		wantKind  string
		retryable bool
	}{
		{name: "patch parse", errText: "patch parse error: bad hunk", wantKind: "patch_parse_failed", retryable: true},
		{name: "unsafe operation", errText: "security error: protected file", wantKind: "unsafe_operation", retryable: false},
		{name: "missing command", output: "sh: pip: command not found", wantKind: "missing_command", retryable: true},
		{name: "missing dependency", output: "No module named pytest", wantKind: "missing_dependency", retryable: true},
		{name: "verification failed", output: "test failed: assert equal", wantKind: "verification_failed", retryable: true},
		{name: "spec missing", errText: "missing required field", wantKind: "spec_missing", retryable: true},
		{name: "unknown", errText: "boom", wantKind: "unknown", retryable: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyExecutionFailure(tt.errText, tt.output)
			if got.Kind != tt.wantKind || got.Retryable != tt.retryable {
				t.Fatalf("ClassifyExecutionFailure() = %#v, want kind=%s retryable=%v", got, tt.wantKind, tt.retryable)
			}
			if tt.errText != "" && got.Reason == "" {
				t.Fatalf("ClassifyExecutionFailure() reason is empty: %#v", got)
			}
		})
	}
}

func TestClassifyExecutionFailureUsesOutputAsFallbackReason(t *testing.T) {
	got := ClassifyExecutionFailure("", "stderr details")
	if got.Kind != "unknown" || got.Reason != "stderr details" || got.Retryable {
		t.Fatalf("ClassifyExecutionFailure() = %#v", got)
	}
}
