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
