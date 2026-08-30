package config

import (
	"fmt"
	"strings"
)

func validateVerificationConfig(config VerificationConfig, backup BackupConfig) error {
	if !config.Enabled {
		return nil
	}
	mode := strings.ToLower(strings.TrimSpace(config.Mode))
	if mode != "dry_run" && mode != "revise" {
		return fmt.Errorf("verification.mode must be dry_run or revise when enabled=true")
	}
	level := strings.ToLower(strings.TrimSpace(config.DefaultLevel))
	if level != "low" && level != "medium" && level != "high" {
		return fmt.Errorf("verification.default_level must be low, medium, or high when enabled=true")
	}
	reportPath := strings.TrimSpace(config.ReportPath)
	if !configPathIsAbs(reportPath) {
		return fmt.Errorf("verification.report_path must be an absolute path when enabled=true")
	}
	if backup.configured() {
		withinCore, err := pathWithin(strings.TrimSpace(backup.CoreSource), reportPath)
		if err != nil || !withinCore {
			return fmt.Errorf("verification.report_path must be inside backup.core_source")
		}
	}
	return nil
}
