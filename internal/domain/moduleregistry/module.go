package moduleregistry

import (
	"fmt"
	"strings"
)

// Module describes a RenCrow subproject that can be edited, built, tested, and restarted independently.
type Module struct {
	ID             string   `json:"module_id" yaml:"module_id"`
	DisplayName    string   `json:"display_name" yaml:"display_name"`
	Root           string   `json:"root" yaml:"root"`
	Kind           string   `json:"kind" yaml:"kind"`
	BuildCommand   string   `json:"build_command,omitempty" yaml:"build_command,omitempty"`
	TestCommand    string   `json:"test_command,omitempty" yaml:"test_command,omitempty"`
	InstallCommand string   `json:"install_command,omitempty" yaml:"install_command,omitempty"`
	RestartTarget  string   `json:"restart_target,omitempty" yaml:"restart_target,omitempty"`
	HealthCheck    string   `json:"health_check,omitempty" yaml:"health_check,omitempty"`
	OwnerRoute     string   `json:"owner_route,omitempty" yaml:"owner_route,omitempty"`
	Aliases        []string `json:"aliases,omitempty" yaml:"aliases,omitempty"`
}

func (m Module) Validate() error {
	if strings.TrimSpace(m.ID) == "" {
		return fmt.Errorf("module_id is required")
	}
	if strings.TrimSpace(m.DisplayName) == "" {
		return fmt.Errorf("display_name is required for module %s", m.ID)
	}
	if strings.TrimSpace(m.Root) == "" {
		return fmt.Errorf("root is required for module %s", m.ID)
	}
	return nil
}

type Resolution struct {
	Module     Module   `json:"module"`
	MatchedBy  string   `json:"matched_by"`
	Confidence float64  `json:"confidence"`
	Ambiguous  bool     `json:"ambiguous,omitempty"`
	Candidates []Module `json:"candidates,omitempty"`
}

func (r Resolution) Found() bool {
	return strings.TrimSpace(r.Module.ID) != ""
}

func (r Resolution) Summary() string {
	if !r.Found() {
		return "module=unresolved"
	}
	return fmt.Sprintf("module=%s root=%s matched_by=%s confidence=%.2f", r.Module.ID, r.Module.Root, r.MatchedBy, r.Confidence)
}
