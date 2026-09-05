package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/turninputmigration"
)

func TestRunProjectsDryRunAndApplyOptionsExactlyOnce(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want turninputmigration.Options
	}{
		{
			name: "dry-run",
			args: []string{
				"--mode", "dry-run", "--source", "/source", "--event-db", "/events.db",
				"--conversation-db", "/conversation.db", "--receipt", "/dry-receipt.json",
			},
			want: turninputmigration.Options{
				Mode: turninputmigration.ModeDryRun, SourceDir: "/source", EventDBPath: "/events.db",
				ConversationDBPath: "/conversation.db", ReceiptPath: "/dry-receipt.json",
			},
		},
		{
			name: "apply",
			args: []string{
				"--mode=apply", "--source=/source", "--event-db=/events.db", "--conversation-db=/conversation.db",
				"--receipt=/apply-receipt.json", "--output=/output", "--dry-run-receipt=/dry-receipt.json",
			},
			want: turninputmigration.Options{
				Mode: turninputmigration.ModeApply, SourceDir: "/source", EventDBPath: "/events.db",
				ConversationDBPath: "/conversation.db", ReceiptPath: "/apply-receipt.json",
				OutputDir: "/output", DryRunReceipt: "/dry-receipt.json",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			calls := 0
			code := run(context.Background(), test.args, &stdout, &stderr, func(_ context.Context, got turninputmigration.Options) (turninputmigration.Receipt, error) {
				calls++
				if got != test.want {
					t.Fatalf("options = %#v, want %#v", got, test.want)
				}
				return receiptFor(test.want.Mode), nil
			})
			if code != exitOK || calls != 1 {
				t.Fatalf("exit=%d calls=%d, want exit=%d calls=1", code, calls, exitOK)
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q", stderr.String())
			}
			assertOneReceipt(t, stdout.Bytes())
		})
	}
}

func TestRunRejectsApplyOnlyFlagIsolationAndRequiredFlags(t *testing.T) {
	baseDry := []string{"--mode=dry-run", "--source=/source", "--event-db=/events.db", "--conversation-db=/conversation.db", "--receipt=/receipt.json"}
	tests := [][]string{
		append(append([]string(nil), baseDry...), "--output="),
		append(append([]string(nil), baseDry...), "--dry-run-receipt="),
		{"--mode=apply", "--source=/source", "--event-db=/events.db", "--conversation-db=/conversation.db", "--receipt=/receipt.json", "--dry-run-receipt=/dry.json"},
		{"--mode=apply", "--source=/source", "--event-db=/events.db", "--conversation-db=/conversation.db", "--receipt=/receipt.json", "--output=/output"},
		{"--mode=dry-run", "--source=/source", "--event-db=/events.db", "--conversation-db=/conversation.db"},
		{"--mode=unknown", "--source=/source", "--event-db=/events.db", "--conversation-db=/conversation.db", "--receipt=/receipt.json"},
	}
	for _, args := range tests {
		var stdout, stderr bytes.Buffer
		calls := 0
		code := run(context.Background(), args, &stdout, &stderr, func(context.Context, turninputmigration.Options) (turninputmigration.Receipt, error) {
			calls++
			return receiptFor(turninputmigration.ModeDryRun), nil
		})
		if code != exitArguments || calls != 0 {
			t.Fatalf("args=%v exit=%d calls=%d, want argument rejection without runner", args, code, calls)
		}
		assertBlockedOutput(t, stdout.Bytes(), stderr.String(), argumentError)
	}
}

func TestRunRejectsUnknownAndPositionalArgumentsWithoutFlagOutput(t *testing.T) {
	tests := [][]string{
		{"--unknown=/secret/private/path"},
		{"--mode=dry-run", "--source=/source", "--event-db=/events.db", "--conversation-db=/conversation.db", "--receipt=/receipt.json", "unexpected"},
		{"--mode"},
	}
	for _, args := range tests {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), args, &stdout, &stderr, func(context.Context, turninputmigration.Options) (turninputmigration.Receipt, error) {
			t.Fatal("runner was called for invalid arguments")
			return turninputmigration.Receipt{}, nil
		})
		if code != exitArguments {
			t.Fatalf("args=%v exit=%d, want %d", args, code, exitArguments)
		}
		assertBlockedOutput(t, stdout.Bytes(), stderr.String(), argumentError)
		if strings.Contains(stderr.String(), "secret") || strings.Contains(stderr.String(), "private") || strings.Contains(stderr.String(), "path") {
			t.Fatalf("stderr leaked argument details: %q", stderr.String())
		}
	}
}

func TestRunEmitsBlockedReceiptAndOnlySafeErrorCode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	secret := "/source/private/raw-id"
	blockedReceipt := receiptFor(turninputmigration.ModeDryRun)
	blockedReceipt.Status = "blocked"
	blockedReceipt.ErrorCode = "blocked_evidence"
	code := run(context.Background(), []string{
		"--mode=dry-run", "--source=/source", "--event-db=/events.db", "--conversation-db=/conversation.db", "--receipt=/receipt.json",
	}, &stdout, &stderr, func(context.Context, turninputmigration.Options) (turninputmigration.Receipt, error) {
		return blockedReceipt, errors.New(secret)
	})
	if code != exitOperation {
		t.Fatalf("exit=%d, want %d", code, exitOperation)
	}
	got := assertOneReceipt(t, stdout.Bytes())
	if got.Status != "blocked" || got.ErrorCode != "blocked_evidence" {
		t.Fatalf("blocked receipt = %#v", got)
	}
	if stderr.String() != "blocked_evidence\n" {
		t.Fatalf("stderr = %q, want one safe error-code line", stderr.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), secret) {
		t.Fatal("operation error leaked path/raw ID")
	}
}

func TestRunJSONReceiptAndExitStatus(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{
		"--mode=dry-run", "--source=/source", "--event-db=/events.db", "--conversation-db=/conversation.db", "--receipt=/receipt.json",
	}, &stdout, &stderr, func(_ context.Context, options turninputmigration.Options) (turninputmigration.Receipt, error) {
		return receiptFor(options.Mode), nil
	})
	if code != exitOK {
		t.Fatalf("exit=%d, want %d", code, exitOK)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
	receipt := assertOneReceipt(t, stdout.Bytes())
	if receipt.ContractVersion != turninputmigration.ContractVersion || receipt.Status != "ready" || receipt.Mode != turninputmigration.ModeDryRun {
		t.Fatalf("receipt = %#v", receipt)
	}
}

func TestRunRejectsInvalidSuccessfulRunnerReceipt(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{
		"--mode=dry-run", "--source=/source", "--event-db=/events.db", "--conversation-db=/conversation.db", "--receipt=/receipt.json",
	}, &stdout, &stderr, func(_ context.Context, options turninputmigration.Options) (turninputmigration.Receipt, error) {
		receipt := receiptFor(options.Mode)
		receipt.ContractVersion = "unexpected-contract"
		return receipt, nil
	})
	if code != exitOperation {
		t.Fatalf("exit=%d, want %d", code, exitOperation)
	}
	assertBlockedOutput(t, stdout.Bytes(), stderr.String(), operationError)
}

func receiptFor(mode string) turninputmigration.Receipt {
	status := "ready"
	if mode == turninputmigration.ModeApply {
		status = "applied"
	}
	return turninputmigration.Receipt{
		ContractVersion: turninputmigration.ContractVersion,
		Status:          status,
		Mode:            mode,
	}
}

func assertOneReceipt(t *testing.T, data []byte) turninputmigration.Receipt {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	var receipt turninputmigration.Receipt
	if err := decoder.Decode(&receipt); err != nil {
		t.Fatalf("stdout is not one JSON receipt: %v (%q)", err, string(data))
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("stdout contains more than one JSON value: err=%v value=%#v", err, extra)
	}
	return receipt
}

func assertBlockedOutput(t *testing.T, stdout []byte, stderr, wantCode string) {
	t.Helper()
	receipt := assertOneReceipt(t, stdout)
	if receipt.ContractVersion != turninputmigration.ContractVersion || receipt.Status != "blocked" {
		t.Fatalf("blocked receipt = %#v", receipt)
	}
	if stderr != wantCode+"\n" {
		t.Fatalf("stderr = %q, want %q", stderr, wantCode+"\n")
	}
}
