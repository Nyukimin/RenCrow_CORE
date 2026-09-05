package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/taskmigration"
)

func TestRunPassesBoundedMigrationOptions(t *testing.T) {
	tests := []struct {
		args []string
		want taskmigration.Options
	}{
		{[]string{"--mode=dry-run", "--source=/source", "--receipt=/dry.json"}, taskmigration.Options{Mode: taskmigration.ModeDryRun, SourceDir: "/source", ReceiptPath: "/dry.json"}},
		{[]string{"--mode=apply", "--source=/source", "--output=/output", "--receipt=/apply.json", "--dry-run-receipt=/dry.json"}, taskmigration.Options{Mode: taskmigration.ModeApply, SourceDir: "/source", OutputDir: "/output", ReceiptPath: "/apply.json", DryRunReceipt: "/dry.json"}},
	}
	for _, test := range tests {
		var stdout, stderr bytes.Buffer
		calls := 0
		code := run(context.Background(), test.args, &stdout, &stderr, func(_ context.Context, got taskmigration.Options) (taskmigration.Receipt, error) {
			calls++
			if got != test.want {
				t.Fatalf("options = %#v, want %#v", got, test.want)
			}
			status := "ready"
			if got.Mode == taskmigration.ModeApply {
				status = "applied"
			}
			return taskmigration.Receipt{ContractVersion: taskmigration.ContractVersion, Status: status, Mode: got.Mode}, nil
		})
		if code != exitOK || calls != 1 || stderr.Len() != 0 {
			t.Fatalf("exit=%d calls=%d stderr=%q", code, calls, stderr.String())
		}
		var result taskmigration.Receipt
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Status == "blocked" {
			t.Fatalf("stdout=%q err=%v", stdout.String(), err)
		}
	}
}

func TestRunRejectsInvalidFlagsWithoutCallingMigration(t *testing.T) {
	for _, args := range [][]string{
		{"--mode=dry-run", "--source=/source"},
		{"--mode=dry-run", "--source=/source", "--receipt=/dry.json", "--output=/output"},
		{"--mode=apply", "--source=/source", "--receipt=/apply.json", "--output=/output"},
		{"--unknown=/private/value"},
	} {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), args, &stdout, &stderr, func(context.Context, taskmigration.Options) (taskmigration.Receipt, error) {
			t.Fatal("migration called for invalid arguments")
			return taskmigration.Receipt{}, nil
		})
		if code != exitArguments || stderr.String() != argumentError+"\n" || bytes.Contains(stderr.Bytes(), []byte("private")) {
			t.Fatalf("args=%v exit=%d stderr=%q", args, code, stderr.String())
		}
	}
}

func TestRunEmitsOnlySafeBlockedError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--mode=dry-run", "--source=/source", "--receipt=/dry.json"}, &stdout, &stderr, func(context.Context, taskmigration.Options) (taskmigration.Receipt, error) {
		return taskmigration.Receipt{ContractVersion: taskmigration.ContractVersion, Status: "blocked", Mode: taskmigration.ModeDryRun, ErrorCode: "source_invalid"}, errors.New("/private/raw/job")
	})
	if code != exitOperation || stderr.String() != "source_invalid\n" || bytes.Contains(stdout.Bytes(), []byte("private")) {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}
