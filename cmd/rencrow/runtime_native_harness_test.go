package main

import (
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_Harness/pkg/protocol"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
)

func nativeCodingTestConfig(t *testing.T) *config.Config {
	t.Helper()
	root := t.TempDir()
	return &config.Config{NativeHarness: config.NativeHarnessConfig{
		HarnessConfig: filepath.Join(root, "etc", "harness.json"),
		Profile: config.NativeHarnessProfileConfig{
			Enabled:               true,
			HarnessBinary:         filepath.Join(root, "bin", "rencrow-harness"),
			ExpectedBuildRevision: "build-1",
			WorkspaceRef:          config.NativeHarnessWorkspaceRef{Path: filepath.Join(root, "work"), PolicyRef: "workspace-write", ExecutionMode: "trusted_host"},
			BindingRef:            config.NativeHarnessBindingRef{Kind: "alias", Selector: "shiro-exec", ProfileRevision: "rev-9", ExecutionRole: "worker"},
			Limits:                config.NativeHarnessLimits{MaxModelSteps: 11, MaxToolCallsPerStep: 7, DeadlineSeconds: 900, MaxCaptureBytes: 2048, MaxGenerationAttempts: 5},
		},
	}}
}

func TestNativeCodingRuntimeIsNotBuiltUnlessTheProfileIsEnabled(t *testing.T) {
	actions := actionmanager.New(nil)
	if runtime, err := buildNativeCodingRuntime(nil, actions); runtime != nil || err != nil {
		t.Fatalf("a nil configuration builds nothing: %v %v", runtime, err)
	}
	if runtime, err := buildNativeCodingRuntime(&config.Config{}, actions); runtime != nil || err != nil {
		t.Fatalf("an absent profile builds nothing: %v %v", runtime, err)
	}
	cfg := nativeCodingTestConfig(t)
	cfg.NativeHarness.Profile.Enabled = false
	if runtime, err := buildNativeCodingRuntime(cfg, actions); runtime != nil || err != nil {
		t.Fatalf("a complete but disabled profile builds nothing and starts no process: %v %v", runtime, err)
	}
}

func TestNativeCodingRuntimeNeedsTheActionOwnerToRecordTheDelegation(t *testing.T) {
	if _, err := buildNativeCodingRuntime(nativeCodingTestConfig(t), nil); err == nil {
		t.Fatal("an enabled profile without the Action owner must be refused: a delegation CORE cannot record must not run")
	}
}

func TestNativeCodingRuntimeIsBuiltFromTheEnabledProfileWithoutStartingTheHarness(t *testing.T) {
	runtime, err := buildNativeCodingRuntime(nativeCodingTestConfig(t), actionmanager.New(nil))
	if err != nil || runtime == nil {
		t.Fatalf("an enabled profile builds the runtime: %v %v", runtime, err)
	}
	closeNativeCodingRuntime(runtime) // nothing was started: a no-op
	closeNativeCodingRuntime(nil)
}

func TestNativeCodingSettingsCarryTheConfiguredProfileUnchanged(t *testing.T) {
	cfg := nativeCodingTestConfig(t)
	settings := nativeCodingSettings(cfg)
	profile := cfg.NativeHarness.Profile
	if settings.HarnessBinary != profile.HarnessBinary || settings.HarnessConfig != cfg.NativeHarness.HarnessConfig ||
		settings.ExpectedBuildRevision != "build-1" {
		t.Fatalf("paths and pinned build: %+v", settings)
	}
	if settings.Workspace.Path != profile.WorkspaceRef.Path || settings.Workspace.PolicyRef != "workspace-write" || settings.Workspace.ExecutionMode != "trusted_host" {
		t.Fatalf("workspace: %+v", settings.Workspace)
	}
	if settings.Binding.Selector != "shiro-exec" || settings.Binding.ProfileRevision != "rev-9" || settings.Binding.ExecutionRole != "worker" {
		t.Fatalf("binding: %+v", settings.Binding)
	}
	want := protocol.Limits{MaxModelSteps: 11, MaxToolCallsPerStep: 7, DeadlineSeconds: 900, MaxCaptureBytes: 2048, MaxGenerationAttempts: 5}
	if settings.Limits != want {
		t.Fatalf("limits = %+v, want %+v", settings.Limits, want)
	}
}
