package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const testBuildRevision = "0123456789abcdef0123456789abcdef01234567"

// validProfileConfig adds a valid shiro_native_coding_v1 profile to the valid
// native_harness section. The Harness binary lives outside the workspace.
func validProfileConfig(t *testing.T) (*Config, nativeHarnessPaths) {
	t.Helper()
	cfg, paths := validNativeHarnessConfig(t)
	binDir := filepath.Join(filepath.Dir(paths.keyFile), "bin")
	binary := filepath.Join(binDir, "rencrow-harness")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	writeTestFile(t, binary, "#!/bin/sh\n")
	if err := os.Chmod(binary, 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	cfg.NativeHarness.Profile = NativeHarnessProfileConfig{
		Enabled:               true,
		HarnessBinary:         binary,
		ExpectedBuildRevision: testBuildRevision,
		WorkspaceRef:          NativeHarnessWorkspaceRef{Path: filepath.Join(filepath.Dir(paths.keyFile), "work"), PolicyRef: "workspace-write", ExecutionMode: "structured_only"},
		BindingRef:            NativeHarnessBindingRef{Kind: "alias", Selector: "shiro-worker-exec", ProfileRevision: "rev-1", ExecutionRole: "worker"},
		Limits: NativeHarnessLimits{
			MaxModelSteps: 10, MaxToolCallsPerStep: 8, DeadlineSeconds: 1800, MaxCaptureBytes: 67108864, MaxGenerationAttempts: 32,
		},
	}
	return cfg, paths
}

func TestNativeHarnessProfileAbsentChangesNothing(t *testing.T) {
	cfg, _ := validNativeHarnessConfig(t) // issuer and harness_config only
	if err := cfg.validateNativeHarnessConfig(); err != nil {
		t.Fatalf("a native_harness section without a profile must stay valid: %v", err)
	}
	if cfg.NativeHarness.Profile.Enabled {
		t.Fatal("the profile is never enabled by default")
	}
}

func TestNativeHarnessProfileValidIsAccepted(t *testing.T) {
	cfg, _ := validProfileConfig(t)
	for _, mode := range []string{"structured_only", "trusted_host"} {
		cfg.NativeHarness.Profile.WorkspaceRef.ExecutionMode = mode
		if err := cfg.validateNativeHarnessConfig(); err != nil {
			t.Fatalf("mode %s rejected: %v", mode, err)
		}
	}
	cfg.NativeHarness.Profile.Enabled = false
	if err := cfg.validateNativeHarnessConfig(); err != nil {
		t.Fatalf("a complete but disabled profile must stay valid: %v", err)
	}
}

func TestNativeHarnessProfileRejectsInvalidItems(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"binary missing", func(c *Config) { c.NativeHarness.Profile.HarnessBinary = "" }, "harness_binary is required"},
		{"binary relative", func(c *Config) { c.NativeHarness.Profile.HarnessBinary = "rencrow-harness" }, "harness_binary must be an absolute path"},
		{"binary absent on disk", func(c *Config) { c.NativeHarness.Profile.HarnessBinary += ".missing" }, "harness_binary"},
		{"build revision empty", func(c *Config) { c.NativeHarness.Profile.ExpectedBuildRevision = "" }, "expected_build_revision"},
		{"build revision control char", func(c *Config) { c.NativeHarness.Profile.ExpectedBuildRevision = "abc\ndef" }, "expected_build_revision"},
		{"build revision too long", func(c *Config) { c.NativeHarness.Profile.ExpectedBuildRevision = strings.Repeat("a", 129) }, "expected_build_revision"},
		{"workspace missing", func(c *Config) { c.NativeHarness.Profile.WorkspaceRef.Path = "" }, "workspace_ref.path is required"},
		{"workspace relative", func(c *Config) { c.NativeHarness.Profile.WorkspaceRef.Path = "work" }, "workspace_ref.path must be an absolute path"},
		{"workspace not clean", func(c *Config) {
			c.NativeHarness.Profile.WorkspaceRef.Path = filepath.Join(c.NativeHarness.Profile.WorkspaceRef.Path, "..", "work") + string(filepath.Separator) + "."
		}, "clean path"},
		{"policy ref missing", func(c *Config) { c.NativeHarness.Profile.WorkspaceRef.PolicyRef = "" }, "policy_ref"},
		{"mode isolated", func(c *Config) { c.NativeHarness.Profile.WorkspaceRef.ExecutionMode = "isolated" }, "execution_mode"},
		{"mode missing", func(c *Config) { c.NativeHarness.Profile.WorkspaceRef.ExecutionMode = "" }, "execution_mode"},
		{"binding kind route", func(c *Config) { c.NativeHarness.Profile.BindingRef.Kind = "model_route" }, "binding_ref.kind"},
		{"binding selector missing", func(c *Config) { c.NativeHarness.Profile.BindingRef.Selector = "" }, "selector"},
		{"binding revision missing", func(c *Config) { c.NativeHarness.Profile.BindingRef.ProfileRevision = "" }, "profile_revision"},
		{"binding role missing", func(c *Config) { c.NativeHarness.Profile.BindingRef.ExecutionRole = "" }, "execution_role"},
		{"limits zero steps", func(c *Config) { c.NativeHarness.Profile.Limits.MaxModelSteps = 0 }, "max_model_steps"},
		{"limits steps over", func(c *Config) { c.NativeHarness.Profile.Limits.MaxModelSteps = 10001 }, "max_model_steps"},
		{"limits tools over", func(c *Config) { c.NativeHarness.Profile.Limits.MaxToolCallsPerStep = 129 }, "max_tool_calls_per_step"},
		{"limits deadline over", func(c *Config) { c.NativeHarness.Profile.Limits.DeadlineSeconds = 86401 }, "deadline_seconds"},
		{"limits capture under", func(c *Config) { c.NativeHarness.Profile.Limits.MaxCaptureBytes = 1023 }, "max_capture_bytes"},
		{"limits capture over", func(c *Config) { c.NativeHarness.Profile.Limits.MaxCaptureBytes = 67108865 }, "max_capture_bytes"},
		{"limits attempts zero", func(c *Config) { c.NativeHarness.Profile.Limits.MaxGenerationAttempts = 0 }, "max_generation_attempts"},
		{"distributed mode", func(c *Config) { c.Distributed.Enabled = true }, "distributed.enabled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, _ := validProfileConfig(t)
			tc.mutate(cfg)
			err := cfg.validateNativeHarnessConfig()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestNativeHarnessProfileBoundaryLimitsAreAccepted(t *testing.T) {
	cfg, _ := validProfileConfig(t)
	cfg.NativeHarness.Profile.Limits = NativeHarnessLimits{MaxModelSteps: 1, MaxToolCallsPerStep: 1, DeadlineSeconds: 1, MaxCaptureBytes: 1024, MaxGenerationAttempts: 1}
	if err := cfg.validateNativeHarnessConfig(); err != nil {
		t.Fatalf("lower bounds rejected: %v", err)
	}
	cfg.NativeHarness.Profile.Limits = NativeHarnessLimits{MaxModelSteps: 10000, MaxToolCallsPerStep: 128, DeadlineSeconds: 86400, MaxCaptureBytes: 67108864, MaxGenerationAttempts: 100000}
	if err := cfg.validateNativeHarnessConfig(); err != nil {
		t.Fatalf("upper bounds rejected: %v", err)
	}
}

func TestNativeHarnessProfileRejectsBinaryInsideTheWorkspace(t *testing.T) {
	cfg, paths := validProfileConfig(t)
	inside := filepath.Join(paths.workspace, "rencrow-harness")
	writeTestFile(t, inside, "x")
	if err := os.Chmod(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg.NativeHarness.Profile.HarnessBinary = inside
	err := cfg.validateNativeHarnessConfig()
	if err == nil || !strings.Contains(err.Error(), "harness_binary must be outside workspace_dir") {
		t.Fatalf("error = %v, want a refusal of a binary the Agent could edit", err)
	}
}

func TestNativeHarnessProfileRequiresAnIssuer(t *testing.T) {
	cfg, _ := validProfileConfig(t)
	cfg.NativeHarness.Issuer = NativeHarnessIssuerConfig{}
	if err := cfg.validateNativeHarnessConfig(); err == nil {
		t.Fatal("a profile without the issuer section must be refused: the whole native_harness section is validated")
	}
}

func TestNativeHarnessProfileOnlyIsNotSilentlyIgnored(t *testing.T) {
	cfg, _ := validProfileConfig(t)
	cfg.NativeHarness.HarnessConfig = ""
	cfg.NativeHarness.Issuer = NativeHarnessIssuerConfig{}
	if !cfg.NativeHarness.configured() {
		t.Fatal("a section that only sets the profile is configured")
	}
	if err := cfg.validateNativeHarnessConfig(); err == nil {
		t.Fatal("a profile alone must be refused")
	}
}

func TestLoadConfigReadsTheNativeHarnessProfile(t *testing.T) {
	cfg, paths := validProfileConfig(t)
	p := cfg.NativeHarness.Profile
	body := nativeHarnessYAML(t, paths, 300) + fmt.Sprintf(`  profile:
    enabled: true
    harness_binary: %q
    expected_build_revision: %q
    workspace_ref:
      path: %q
      policy_ref: %q
      execution_mode: %q
    binding_ref:
      kind: %q
      selector: %q
      profile_revision: %q
      execution_role: %q
    limits:
      max_model_steps: %d
      max_tool_calls_per_step: %d
      deadline_seconds: %d
      max_capture_bytes: %d
      max_generation_attempts: %d
`, p.HarnessBinary, p.ExpectedBuildRevision, p.WorkspaceRef.Path, p.WorkspaceRef.PolicyRef, p.WorkspaceRef.ExecutionMode,
		p.BindingRef.Kind, p.BindingRef.Selector, p.BindingRef.ProfileRevision, p.BindingRef.ExecutionRole,
		p.Limits.MaxModelSteps, p.Limits.MaxToolCallsPerStep, p.Limits.DeadlineSeconds, p.Limits.MaxCaptureBytes, p.Limits.MaxGenerationAttempts)

	loaded, err := LoadConfig(writeStorageHostConfig(t, body))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if loaded.NativeHarness.Profile != p {
		t.Fatalf("the profile was not read as written:\n got %+v\nwant %+v", loaded.NativeHarness.Profile, p)
	}
}
