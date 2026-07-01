package security

import (
	"strings"
	"testing"
)

func TestSecurityProfile_Validate(t *testing.T) {
	p := StrictProfile()
	if err := p.Validate(); err != nil {
		t.Fatalf("strict profile should be valid: %v", err)
	}
	bad := p
	bad.SandboxLevel = "vm"
	if err := bad.Validate(); err == nil {
		t.Fatal("expected invalid sandbox level error")
	}
}

func TestSecurityProfileValidateRejectsInvalidScopes(t *testing.T) {
	valid := StrictProfile()
	cases := []struct {
		name   string
		mutate func(*SecurityProfile)
		want   string
	}{
		{name: "missing name", mutate: func(p *SecurityProfile) { p.Name = "" }, want: "profile name"},
		{name: "filesystem", mutate: func(p *SecurityProfile) { p.FilesystemScope = "root" }, want: "filesystem"},
		{name: "network", mutate: func(p *SecurityProfile) { p.NetworkScope = "vpn" }, want: "network"},
		{name: "process", mutate: func(p *SecurityProfile) { p.ProcessScope = "sudo" }, want: "process"},
		{name: "git", mutate: func(p *SecurityProfile) { p.GitScope = "force_push" }, want: "git"},
		{name: "sandbox", mutate: func(p *SecurityProfile) { p.SandboxLevel = "vm" }, want: "sandbox"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			profile := valid
			tc.mutate(&profile)
			err := profile.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestProfiles_ValidateAll(t *testing.T) {
	profiles := []SecurityProfile{
		StrictProfile(),
		BalancedProfile(),
		DevProfile(),
	}
	for _, p := range profiles {
		if err := p.Validate(); err != nil {
			t.Fatalf("profile %s should be valid: %v", p.Name, err)
		}
	}
}
