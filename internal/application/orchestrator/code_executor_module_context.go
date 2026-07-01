package orchestrator

import (
	"fmt"
	"strings"

	domainmodule "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/moduleregistry"
)

const moduleContextHeader = "RenCrow module context"

func appendModuleContextToCodeRequest(message string, resolved domainmodule.Resolution) string {
	if !resolved.Found() || strings.Contains(message, moduleContextHeader) {
		return message
	}
	m := resolved.Module
	var b strings.Builder
	b.WriteString(message)
	b.WriteString("\n\n")
	b.WriteString(moduleContextHeader)
	b.WriteString(":\n")
	b.WriteString(fmt.Sprintf("- module_id: %s\n", m.ID))
	b.WriteString(fmt.Sprintf("- display_name: %s\n", m.DisplayName))
	b.WriteString(fmt.Sprintf("- root: %s\n", m.Root))
	b.WriteString(fmt.Sprintf("- kind: %s\n", m.Kind))
	if m.TestCommand != "" {
		b.WriteString(fmt.Sprintf("- test_command: %s\n", m.TestCommand))
	}
	if m.BuildCommand != "" {
		b.WriteString(fmt.Sprintf("- build_command: %s\n", m.BuildCommand))
	}
	if m.InstallCommand != "" {
		b.WriteString(fmt.Sprintf("- install_command_manual_only: %s\n", m.InstallCommand))
	}
	if m.RestartTarget != "" {
		b.WriteString(fmt.Sprintf("- restart_target_manual_only: %s\n", m.RestartTarget))
	}
	if m.HealthCheck != "" {
		b.WriteString(fmt.Sprintf("- health_check: %s\n", m.HealthCheck))
	}
	b.WriteString("- rule: do not use /home/nyukimi/RenCrow as the edit root; edit, test, and build from the selected module root.\n")
	b.WriteString("- rule: do not include service restart, service stop/start, make install, or live binary overwrite commands in Worker-executable patches; mention them only as manual approval steps.\n")
	return b.String()
}
