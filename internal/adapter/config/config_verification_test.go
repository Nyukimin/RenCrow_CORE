package config

import (
	"strings"
	"testing"
)

func TestValidateVerificationConfigRequiresCanonicalDurableReportPath(t *testing.T) {
	base := VerificationConfig{
		Enabled:      true,
		Mode:         "dry_run",
		DefaultLevel: "low",
		ReportPath:   "/srv/rencrow/db/core/reports/verification_report.jsonl",
	}
	for _, tc := range []struct {
		name   string
		mutate func(*VerificationConfig)
		want   string
	}{
		{name: "valid", mutate: func(*VerificationConfig) {}},
		{name: "mode", mutate: func(c *VerificationConfig) { c.Mode = "observe" }, want: "verification.mode"},
		{name: "level", mutate: func(c *VerificationConfig) { c.DefaultLevel = "critical" }, want: "verification.default_level"},
		{name: "relative", mutate: func(c *VerificationConfig) { c.ReportPath = "reports/verification.jsonl" }, want: "verification.report_path must be an absolute path"},
		{name: "outside-backup", mutate: func(c *VerificationConfig) { c.ReportPath = "/tmp/verification.jsonl" }, want: "verification.report_path must be inside backup.core_source"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.mutate(&cfg)
			err := validateVerificationConfig(cfg, BackupConfig{CoreSource: "/srv/rencrow/db/core"})
			if tc.want == "" {
				if err != nil {
					t.Fatalf("validateVerificationConfig() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validateVerificationConfig() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestValidateVerificationConfigAllowsDisabledPolicyWithoutReportStore(t *testing.T) {
	if err := validateVerificationConfig(VerificationConfig{}, BackupConfig{}); err != nil {
		t.Fatalf("disabled verification must remain valid: %v", err)
	}
}
