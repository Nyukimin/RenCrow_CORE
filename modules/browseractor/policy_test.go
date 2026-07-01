package browseractor

import "testing"

func TestClassifyRisk(t *testing.T) {
	cases := []struct {
		name string
		req  RunRequest
		want string
	}{
		{name: "read only", req: RunRequest{Actions: []Action{{Type: "open"}, {Type: "snapshot"}}}, want: RiskReadOnly},
		{name: "draft input", req: RunRequest{Actions: []Action{{Type: "open"}, {Type: "fill", Selector: "#name", Value: "x"}}}, want: RiskDraftInput},
		{name: "navigation", req: RunRequest{Actions: []Action{{Type: "open"}, {Type: "click", Selector: "#next"}}}, want: RiskNavigation},
		{name: "submit keyword", req: RunRequest{Actions: []Action{{Type: "click", Selector: "#send"}}}, want: RiskExternalEffect},
		{name: "enter key", req: RunRequest{Actions: []Action{{Type: "press", Key: "Enter"}}}, want: RiskExternalEffect},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyRisk(tc.req)
			if got.Risk != tc.want {
				t.Fatalf("risk=%s want=%s decision=%+v", got.Risk, tc.want, got)
			}
		})
	}
}

func TestValidateRunRequest(t *testing.T) {
	req := NormalizeRunRequest(RunRequest{
		RunID:    "run_1",
		StartURL: "http://127.0.0.1:18790/viewer",
		Actions:  []Action{{Type: "open"}},
	})
	if err := ValidateRunRequest(req); err != nil {
		t.Fatalf("ValidateRunRequest returned error: %v", err)
	}
	req.Actions = []Action{{Type: "unknown"}}
	if err := ValidateRunRequest(req); err == nil {
		t.Fatal("expected unsupported action error")
	}
}

func TestMaskSecrets(t *testing.T) {
	got := MaskSecrets("Authorization: Bearer abc Cookie: session=secret password=hidden")
	if got == "Authorization: Bearer abc Cookie: session=secret password=hidden" {
		t.Fatalf("expected masked output, got %q", got)
	}
	if got == "" || got == Mask {
		t.Fatalf("unexpected masked output: %q", got)
	}
}
