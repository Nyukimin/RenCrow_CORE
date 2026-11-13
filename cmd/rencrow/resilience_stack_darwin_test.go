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
