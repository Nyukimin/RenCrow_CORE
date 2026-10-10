package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

// Validation of native_harness.profile, the selection of the execution profile
// shiro_native_coding_v1. Source of truth: docs/05_設定リファレンス.md
// (RenCrow_Harness委譲設定). The ranges below are the ranges of the Harness
// protocol Limits and identifiers; the Harness validates them again on every
// request, so a value that slips through here is still refused there.

const (
	maxNativeHarnessIdentifierRunes = 128

	nativeHarnessExecutionModeStructuredOnly = "structured_only"
	nativeHarnessExecutionModeTrustedHost    = "trusted_host"
	nativeHarnessBindingKindAlias            = "alias"
)

// validateNativeHarnessProfile validates the profile when the section sets any
// of its items (a fully zero profile is not configured). Every item is
// required: there are no defaults. workspace is the CORE workspace_dir, which
// the Harness binary must lie outside of (the Agent edits that directory).
func (c *Config) validateNativeHarnessProfile(workspace string) error {
	profile := c.NativeHarness.Profile
	if profile == (NativeHarnessProfileConfig{}) {
		return nil
	}
	if profile.Enabled && c.Distributed.Enabled {
		return errors.New("native_harness.profile.enabled is not supported with distributed.enabled: the profile is selected only on the local OPS route")
	}
	if err := validateNativeHarnessBinary(workspace, profile.HarnessBinary); err != nil {
		return err
	}
	if err := requirePlainText("native_harness.profile.expected_build_revision", profile.ExpectedBuildRevision); err != nil {
		return err
	}
	if profile.Enabled && !domaintask.ValidCriteriaRevision(profile.ExpectedCriteriaRevision) {
		return errors.New("native_harness.profile.expected_criteria_revision must be one lowercase SHA-256 revision")
	}
	if profile.ExpectedCriteriaRevision != "" && !domaintask.ValidCriteriaRevision(profile.ExpectedCriteriaRevision) {
		return errors.New("native_harness.profile.expected_criteria_revision must be one lowercase SHA-256 revision")
	}
	ref := profile.WorkspaceRef
	if strings.TrimSpace(ref.Path) == "" {
		return errors.New("native_harness.profile.workspace_ref.path is required")
	}
	if !configPathIsAbs(ref.Path) {
		return errors.New("native_harness.profile.workspace_ref.path must be an absolute path")
	}
	if strings.ContainsRune(ref.Path, 0) || filepath.Clean(ref.Path) != ref.Path {
		return errors.New("native_harness.profile.workspace_ref.path must be a clean path (no dot segments or trailing separator)")
	}
	if err := requirePlainText("native_harness.profile.workspace_ref.policy_ref", ref.PolicyRef); err != nil {
		return err
	}
	switch ref.ExecutionMode {
	case nativeHarnessExecutionModeStructuredOnly, nativeHarnessExecutionModeTrustedHost:
	default:
		return fmt.Errorf("native_harness.profile.workspace_ref.execution_mode must be %s or %s (isolated is unavailable and is never a fallback)", nativeHarnessExecutionModeStructuredOnly, nativeHarnessExecutionModeTrustedHost)
	}
	binding := profile.BindingRef
	if binding.Kind != nativeHarnessBindingKindAlias {
		return fmt.Errorf("native_harness.profile.binding_ref.kind must be %s: the profile uses the existing Shiro execution alias", nativeHarnessBindingKindAlias)
	}
	for _, field := range []struct{ name, value string }{
		{"native_harness.profile.binding_ref.selector", binding.Selector},
		{"native_harness.profile.binding_ref.profile_revision", binding.ProfileRevision},
		{"native_harness.profile.binding_ref.execution_role", binding.ExecutionRole},
	} {
		if err := requirePlainText(field.name, field.value); err != nil {
			return err
		}
	}
	return validateNativeHarnessLimits(profile.Limits)
}

func validateNativeHarnessBinary(workspace, path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("native_harness.profile.harness_binary is required")
	}
	if !configPathIsAbs(path) || strings.ContainsRune(path, 0) {
		return errors.New("native_harness.profile.harness_binary must be an absolute path")
	}
	inside, err := pathInsideWorkspace(workspace, path)
	if err != nil {
		return fmt.Errorf("native_harness.profile.harness_binary: check against workspace_dir: %w", err)
	}
	if inside {
		return errors.New("native_harness.profile.harness_binary must be outside workspace_dir")
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("native_harness.profile.harness_binary: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("native_harness.profile.harness_binary must be a regular file")
	}
	if runtime.GOOS == "windows" {
		if !strings.EqualFold(filepath.Ext(path), ".exe") {
			return errors.New("native_harness.profile.harness_binary must end with .exe on Windows")
		}
	} else if info.Mode().Perm()&0o111 == 0 {
		return errors.New("native_harness.profile.harness_binary must be executable")
	}
	return nil
}

func validateNativeHarnessLimits(limits NativeHarnessLimits) error {
	for _, field := range []struct {
		name     string
		value    int64
		min, max int64
	}{
		{"max_model_steps", limits.MaxModelSteps, 1, 10000},
		{"max_tool_calls_per_step", limits.MaxToolCallsPerStep, 1, 128},
		{"deadline_seconds", limits.DeadlineSeconds, 1, 86400},
		{"max_capture_bytes", limits.MaxCaptureBytes, 1024, 67108864},
		{"max_generation_attempts", limits.MaxGenerationAttempts, 1, 100000},
	} {
		if field.value < field.min || field.value > field.max {
			return fmt.Errorf("native_harness.profile.limits.%s must be %d..%d (every limit is required; there are no defaults)", field.name, field.min, field.max)
		}
	}
	return nil
}

// requirePlainText requires non-empty valid UTF-8 without control characters,
// at most maxNativeHarnessIdentifierRunes runes (the Harness identifier bound).
func requirePlainText(name, value string) error {
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > maxNativeHarnessIdentifierRunes {
		return fmt.Errorf("%s must be 1..%d characters of valid UTF-8", name, maxNativeHarnessIdentifierRunes)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s must not contain control characters", name)
		}
	}
	return nil
}
