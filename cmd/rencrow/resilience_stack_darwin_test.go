//go:build darwin

package main

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

const resilienceStackCommandSleepHelperEnv = "RENCROW_RESILIENCE_STACK_COMMAND_SLEEP_HELPER"

func TestParseResilienceStackDisabledStateAcceptsLaunchctlValues(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		label   string
		want    bool
		wantErr bool
	}{
		{name: "boolean true", output: `"com.rencrow.core" => true`, label: "com.rencrow.core", want: true},
		{name: "boolean false", output: `"com.rencrow.core" => false`, label: "com.rencrow.core", want: false},
		{name: "enabled", output: `"com.rencrow.core" => enabled`, label: "com.rencrow.core", want: false},
		{name: "enabled CRLF", output: "\"com.rencrow.core\" => enabled\r\n", label: "com.rencrow.core", want: false},
		{name: "disabled", output: `"com.rencrow.core" => disabled`, label: "com.rencrow.core", want: true},
		{name: "label prefix is absent", output: `"com.rencrow.core-storage-204" => disabled`, label: "com.rencrow.core", want: false},
		{name: "absent label", output: `"com.rencrow.other" => disabled`, label: "com.rencrow.core", want: false},
		{name: "unknown value", output: `"com.rencrow.core" => pending`, label: "com.rencrow.core", wantErr: true},
		{name: "value on next line is unknown", output: "\"com.rencrow.core\" =>\nenabled", label: "com.rencrow.core", wantErr: true},
		{name: "duplicate exact label", output: "\"com.rencrow.core\" => enabled\n\"com.rencrow.core\" => disabled", label: "com.rencrow.core", wantErr: true},
		{name: "unknown value on label prefix", output: `"com.rencrow.core-storage-204" => pending`, label: "com.rencrow.core", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseResilienceStackDisabledState([]byte(test.output), test.label)
			if (err != nil) != test.wantErr {
				t.Fatalf("parseResilienceStackDisabledState error = %v, wantErr %t", err, test.wantErr)
			}
			if err == nil && got != test.want {
				t.Fatalf("parseResilienceStackDisabledState = %t, want %t", got, test.want)
			}
		})
	}
}

func TestBootoutWaitsForLaunchdTargetToUnloadBeforeReturning(t *testing.T) {
	service := resilienceStackService{Label: "com.rencrow.test", Domain: resilienceStackDomain}
	bootoutCalls := 0
	inspectCalls := 0
	snapshots := []resilienceStackSnapshot{
		{Loaded: true, Running: true, PID: 123},
		{Loaded: true, Disabled: true, Running: false, PID: 123},
		{Disabled: true},
	}
	err := bootoutResilienceStackTarget(
		context.Background(), service, time.Second, time.Millisecond,
		func(context.Context, resilienceStackService) error {
			bootoutCalls++
			return nil
		},
		func(context.Context, resilienceStackService) (resilienceStackSnapshot, error) {
			index := inspectCalls
			inspectCalls++
			if index >= len(snapshots) {
				index = len(snapshots) - 1
			}
			return snapshots[index], nil
		},
	)
	if err != nil {
		t.Fatalf("bootoutResilienceStackTarget() error = %v", err)
	}
	if bootoutCalls != 1 {
		t.Fatalf("bootout calls = %d, want exactly one", bootoutCalls)
	}
	if inspectCalls != 3 {
		t.Fatalf("inspect calls = %d, want loaded/stopping/unloaded observations", inspectCalls)
	}
}

func TestBootoutWaitTimesOutWhileLaunchdTargetRemainsLoaded(t *testing.T) {
	service := resilienceStackService{Label: "com.rencrow.test", Domain: resilienceStackDomain}
	bootoutCalls := 0
	err := bootoutResilienceStackTarget(
		context.Background(), service, 30*time.Millisecond, time.Millisecond,
		func(context.Context, resilienceStackService) error {
			bootoutCalls++
			return nil
		},
		func(context.Context, resilienceStackService) (resilienceStackSnapshot, error) {
			return resilienceStackSnapshot{Loaded: true, Running: false, PID: 123}, nil
		},
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bootoutResilienceStackTarget() error = %v, want bounded wait timeout", err)
	}
	if bootoutCalls != 1 {
		t.Fatalf("timed-out bootout calls = %d, want exactly one", bootoutCalls)
	}
}

func TestBootoutWaitHonorsEarlierCallerDeadline(t *testing.T) {
	service := resilienceStackService{Label: "com.rencrow.test", Domain: resilienceStackDomain}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := bootoutResilienceStackTarget(
		ctx, service, time.Second, time.Millisecond,
		func(context.Context, resilienceStackService) error { return nil },
		func(context.Context, resilienceStackService) (resilienceStackSnapshot, error) {
			return resilienceStackSnapshot{Loaded: true, Running: true, PID: 123}, nil
		},
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bootoutResilienceStackTarget() error = %v, want caller deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("caller deadline took %s to stop unload wait", elapsed)
	}
}

func TestRunResilienceStackLaunchctlReturnsCallerCancelCause(t *testing.T) {
	want := errors.New("resilience stack transaction stopped")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(want)

	_, err := runResilienceStackLaunchctl(ctx, resilienceStackInspectionCommandTimeout, "print", "system")
	if !errors.Is(err, want) {
		t.Fatalf("runResilienceStackLaunchctl error = %v, want cause %v", err, want)
	}
}

func TestResilienceStackCommandTimeoutHelper(t *testing.T) {
	if os.Getenv(resilienceStackCommandSleepHelperEnv) == "1" {
		time.Sleep(5 * time.Second)
	}
}

func TestResilienceStackCommandTimeoutHonorsOperationCap(t *testing.T) {
	t.Setenv(resilienceStackCommandSleepHelperEnv, "1")
	started := time.Now()
	_, err := runResilienceStackCommand(context.Background(), 40*time.Millisecond, os.Args[0], "-test.run=^TestResilienceStackCommandTimeoutHelper$")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runResilienceStackCommand error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("timed out command took %s, want less than 1s", elapsed)
	}
}

func TestResilienceStackCommandTimeoutHonorsEarlierCallerDeadline(t *testing.T) {
	t.Setenv(resilienceStackCommandSleepHelperEnv, "1")
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := runResilienceStackCommand(ctx, time.Second, os.Args[0], "-test.run=^TestResilienceStackCommandTimeoutHelper$")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runResilienceStackCommand error = %v, want caller deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("timed out command took %s, want less than 1s", elapsed)
	}
}

func TestResilienceStackCommandTimeoutBudgets(t *testing.T) {
	if resilienceStackInspectionCommandTimeout != 10*time.Second {
		t.Fatalf("inspection timeout = %s, want 10s", resilienceStackInspectionCommandTimeout)
	}
	if resilienceStackOperationCommandTimeout != 60*time.Second {
		t.Fatalf("operation timeout = %s, want 60s", resilienceStackOperationCommandTimeout)
	}
}
