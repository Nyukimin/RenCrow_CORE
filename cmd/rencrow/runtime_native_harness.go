package main

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/Nyukimin/RenCrow_Harness/pkg/protocol"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/nativeharnessclient/delegation"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
)

// nativeCodingCloseTimeout bounds the orderly stop of the Harness at shutdown.
const nativeCodingCloseTimeout = 15 * time.Second

// nativeCodingSettings converts the authenticated native_harness.profile of the
// configuration to the settings of the delegation runtime. It adds nothing: the
// configuration validated every item at startup, and there are no defaults for
// the limits.
func nativeCodingSettings(cfg *config.Config) delegation.Settings {
	profile := cfg.NativeHarness.Profile
	return delegation.Settings{
		HarnessBinary:         profile.HarnessBinary,
		HarnessConfig:         cfg.NativeHarness.HarnessConfig,
		ExpectedBuildRevision: profile.ExpectedBuildRevision,
		Workspace: delegation.Workspace{
			Path: profile.WorkspaceRef.Path, PolicyRef: profile.WorkspaceRef.PolicyRef, ExecutionMode: profile.WorkspaceRef.ExecutionMode,
		},
		Binding: delegation.Binding{
			Selector: profile.BindingRef.Selector, ProfileRevision: profile.BindingRef.ProfileRevision, ExecutionRole: profile.BindingRef.ExecutionRole,
		},
		Limits: protocol.Limits{
			MaxModelSteps:         profile.Limits.MaxModelSteps,
			MaxToolCallsPerStep:   profile.Limits.MaxToolCallsPerStep,
			DeadlineSeconds:       profile.Limits.DeadlineSeconds,
			MaxCaptureBytes:       profile.Limits.MaxCaptureBytes,
			MaxGenerationAttempts: profile.Limits.MaxGenerationAttempts,
		},
	}
}

// buildNativeCodingRuntime builds the runtime of the execution profile
// shiro_native_coding_v1. It returns nil when the profile is not enabled by the
// configuration: nothing is wired, no process is started and every route runs as
// before. The runtime is both the admission of the orchestrator and the delegate
// of Shiro; it starts the Harness lazily (Warm tries once at startup).
func buildNativeCodingRuntime(cfg *config.Config, actions *actionmanager.Manager) (*delegation.Runtime, error) {
	if cfg == nil || !cfg.NativeHarness.Profile.Enabled {
		return nil, nil
	}
	if actions == nil {
		return nil, errors.New("the canonical Action owner is required to record the delegation")
	}
	return delegation.NewRuntime(nativeCodingSettings(cfg), actions, delegation.Options{})
}

// warmNativeCodingRuntime starts the Harness once in the background so a Harness
// that cannot be used shows in the startup log. A failure does not stop CORE:
// every admission is then blocked with the same cause until the Harness can be
// started, and no old route runs in its place.
func warmNativeCodingRuntime(runtime *delegation.Runtime) {
	go func() {
		if err := runtime.Warm(context.Background()); err != nil {
			log.Printf("WARN: shiro_native_coding_v1: the Harness is not available; selected turns are blocked until it is: %v", err)
			return
		}
		log.Printf("shiro_native_coding_v1: the Harness is running and matches the pinned build")
	}()
}

// closeNativeCodingRuntime stops the Harness in two stages (orderly shutdown with
// cancel, then the kill). It is called once at shutdown.
func closeNativeCodingRuntime(runtime *delegation.Runtime) {
	if runtime == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), nativeCodingCloseTimeout)
	defer cancel()
	if err := runtime.Close(ctx); err != nil {
		log.Printf("WARN: shiro_native_coding_v1: the Harness did not stop in order: %v", err)
	}
}
