package security

import "fmt"

// SecurityProfile defines runtime permission scopes.
type SecurityProfile struct {
	Name            string
	FilesystemScope string // workspace|readonly|none
	NetworkScope    string // blocked|allowlist|full
	ProcessScope    string // none|limited|full
	GitScope        string // read|safe_write|full
	SandboxLevel    string // workspace|process|container
}

func (p SecurityProfile) Validate() error {
	if p.Name == "" {
		return fmt.Errorf("profile name is required")
	}
	if p.FilesystemScope != "workspace" && p.FilesystemScope != "readonly" && p.FilesystemScope != "none" {
		return fmt.Errorf("invalid filesystem scope: %s", p.FilesystemScope)
	}
	if p.NetworkScope != "blocked" && p.NetworkScope != "allowlist" && p.NetworkScope != "full" {
		return fmt.Errorf("invalid network scope: %s", p.NetworkScope)
	}
	if p.ProcessScope != "none" && p.ProcessScope != "limited" && p.ProcessScope != "full" {
		return fmt.Errorf("invalid process scope: %s", p.ProcessScope)
	}
	if p.GitScope != "read" && p.GitScope != "safe_write" && p.GitScope != "full" {
		return fmt.Errorf("invalid git scope: %s", p.GitScope)
	}
	if p.SandboxLevel != "workspace" && p.SandboxLevel != "process" && p.SandboxLevel != "container" {
		return fmt.Errorf("invalid sandbox level: %s", p.SandboxLevel)
	}
	return nil
}

func StrictProfile() SecurityProfile {
	return SecurityProfile{
		Name:            "strict",
		FilesystemScope: "workspace",
		NetworkScope:    "allowlist",
		ProcessScope:    "limited",
		GitScope:        "safe_write",
		SandboxLevel:    "workspace",
	}
}

func BalancedProfile() SecurityProfile {
	return SecurityProfile{
		Name:            "balanced",
		FilesystemScope: "workspace",
		NetworkScope:    "full",
		ProcessScope:    "limited",
		GitScope:        "safe_write",
		SandboxLevel:    "workspace",
	}
}

func DevProfile() SecurityProfile {
	return SecurityProfile{
		Name:            "dev",
		FilesystemScope: "workspace",
		NetworkScope:    "full",
		ProcessScope:    "full",
		GitScope:        "full",
		SandboxLevel:    "process",
	}
}
