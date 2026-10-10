package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_Harness/pkg/protocol"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
)

func nativeCodingTestConfig(t *testing.T) *config.Config {
	t.Helper()
	root := t.TempDir()
	configPath := filepath.Join(root, "etc", "harness.json")
	keyPath := filepath.Join(root, "etc", "origin.key")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	callerConfig := `{"config_version":"rencrow-harness-config/v1","caller":{"principal":"core:local","default_origin":"automation","relay_issuers":[{"issuer":"core:test","key_id":"test-key","audience":"core:local"}]},"workspaces":[]}`
	if err := os.WriteFile(configPath, []byte(callerConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte(strings.Repeat("ab", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	return &config.Config{NativeHarness: config.NativeHarnessConfig{
		HarnessConfig: configPath,
		Issuer: config.NativeHarnessIssuerConfig{
			Issuer: "core:test", KeyID: "test-key", Audience: "core:local", KeyFile: keyPath, TTLSeconds: 300,
		},
		Profile: config.NativeHarnessProfileConfig{
			Enabled:                  true,
			HarnessBinary:            filepath.Join(root, "bin", "rencrow-harness"),
			ExpectedBuildRevision:    "build-1",
			ExpectedCriteriaRevision: strings.Repeat("a", 64),
			WorkspaceRef:             config.NativeHarnessWorkspaceRef{Path: filepath.Join(root, "work"), PolicyRef: "workspace-write", ExecutionMode: "trusted_host"},
			BindingRef:               config.NativeHarnessBindingRef{Kind: "alias", Selector: "shiro-exec", ProfileRevision: "rev-9", ExecutionRole: "worker"},
			Limits:                   config.NativeHarnessLimits{MaxModelSteps: 11, MaxToolCallsPerStep: 7, DeadlineSeconds: 900, MaxCaptureBytes: 2048, MaxGenerationAttempts: 5},
		},
	}}
}

func TestNativeCodingRuntimeIsNotBuiltUnlessTheProfileIsEnabled(t *testing.T) {
	actions := actionmanager.New(nil)
	if runtime, err := buildNativeCodingRuntime(nil, actions, nil); runtime != nil || err != nil {
		t.Fatalf("a nil configuration builds nothing: %v %v", runtime, err)
	}
	if runtime, err := buildNativeCodingRuntime(&config.Config{}, actions, nil); runtime != nil || err != nil {
		t.Fatalf("an absent profile builds nothing: %v %v", runtime, err)
	}
	cfg := nativeCodingTestConfig(t)
	cfg.NativeHarness.Profile.Enabled = false
	if runtime, err := buildNativeCodingRuntime(cfg, actions, nil); runtime != nil || err != nil {
		t.Fatalf("a complete but disabled profile builds nothing and starts no process: %v %v", runtime, err)
	}
}

func TestNativeCodingRuntimeNeedsTheActionOwnerToRecordTheDelegation(t *testing.T) {
	if _, err := buildNativeCodingRuntime(nativeCodingTestConfig(t), nil, taskmanager.New(nil, taskmanager.DefaultParallelLimits())); err == nil {
		t.Fatal("an enabled profile without the Action owner must be refused: a delegation CORE cannot record must not run")
	}
}

func TestNativeCodingRuntimeIsBuiltFromTheEnabledProfileWithoutStartingTheHarness(t *testing.T) {
	runtime, err := buildNativeCodingRuntime(nativeCodingTestConfig(t), actionmanager.New(nil), taskmanager.New(nil, taskmanager.DefaultParallelLimits()))
	if err != nil || runtime == nil {
		t.Fatalf("an enabled profile builds the runtime: %v %v", runtime, err)
	}
	closeNativeCodingRuntime(runtime) // nothing was started: a no-op
	closeNativeCodingRuntime(nil)
}

func TestNativeCodingRuntimeFailsClosedOnMissingKeyAndMismatchedCallerAudience(t *testing.T) {
	t.Run("missing key", func(t *testing.T) {
		cfg := nativeCodingTestConfig(t)
		cfg.NativeHarness.Issuer.KeyFile = ""
		if runtime, err := buildNativeCodingRuntime(cfg, actionmanager.New(nil), taskmanager.New(nil, taskmanager.DefaultParallelLimits())); err == nil || runtime != nil {
			t.Fatalf("runtime=%v err=%v, want missing issuer key to block composition", runtime, err)
		}
	})

	t.Run("caller audience mismatch", func(t *testing.T) {
		cfg := nativeCodingTestConfig(t)
		cfg.NativeHarness.Issuer.Audience = "core:other"
		if runtime, err := buildNativeCodingRuntime(cfg, actionmanager.New(nil), taskmanager.New(nil, taskmanager.DefaultParallelLimits())); err == nil || runtime != nil {
			t.Fatalf("runtime=%v err=%v, want current caller profile mismatch to block composition", runtime, err)
		}
	})

	t.Run("bad key", func(t *testing.T) {
		cfg := nativeCodingTestConfig(t)
		if err := os.WriteFile(cfg.NativeHarness.Issuer.KeyFile, []byte("bad-key"), 0o600); err != nil {
			t.Fatal(err)
		}
		if runtime, err := buildNativeCodingRuntime(cfg, actionmanager.New(nil), taskmanager.New(nil, taskmanager.DefaultParallelLimits())); err == nil || runtime != nil {
			t.Fatalf("runtime=%v err=%v, want invalid issuer key to block composition", runtime, err)
		}
	})
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
