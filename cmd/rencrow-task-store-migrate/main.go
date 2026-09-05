// Command rencrow-task-store-migrate runs the one-shot canonical Task store
// migration owned by RenCrow_CORE.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/taskmigration"
)

const (
	exitOK         = 0
	exitOperation  = 1
	exitArguments  = 2
	operationError = "operation_failed"
	argumentError  = "invalid_arguments"
)

type stringFlag struct {
	value string
	set   bool
}

func (value *stringFlag) String() string { return value.value }

func (value *stringFlag) Set(raw string) error {
	if value.set {
		return fmt.Errorf("duplicate flag")
	}
	value.value, value.set = raw, true
	return nil
}

type migrationRunner func(context.Context, taskmigration.Options) (taskmigration.Receipt, error)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr, taskmigration.Run)
	stop()
	if code != exitOK {
		os.Exit(code)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, migrate migrationRunner) int {
	flags := flag.NewFlagSet("rencrow-task-store-migrate", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var mode, source, receipt, output, dryRunReceipt stringFlag
	flags.Var(&mode, "mode", "migration mode: dry-run or apply")
	flags.Var(&source, "source", "legacy Job store directory")
	flags.Var(&receipt, "receipt", "fresh output receipt path")
	flags.Var(&output, "output", "existing empty apply output directory")
	flags.Var(&dryRunReceipt, "dry-run-receipt", "prior ready dry-run receipt path")

	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || !required(mode) || !required(source) || !required(receipt) {
		return emitBlocked(stdout, stderr, mode.value, argumentError, exitArguments)
	}
	if mode.value != taskmigration.ModeDryRun && mode.value != taskmigration.ModeApply {
		return emitBlocked(stdout, stderr, mode.value, argumentError, exitArguments)
	}
	if mode.value == taskmigration.ModeDryRun {
		if output.set || dryRunReceipt.set {
			return emitBlocked(stdout, stderr, mode.value, argumentError, exitArguments)
		}
	} else if !required(output) || !required(dryRunReceipt) {
		return emitBlocked(stdout, stderr, mode.value, argumentError, exitArguments)
	}
	if migrate == nil {
		return emitBlocked(stdout, stderr, mode.value, operationError, exitOperation)
	}

	result, err := migrate(ctx, taskmigration.Options{
		Mode: mode.value, SourceDir: source.value, OutputDir: output.value,
		ReceiptPath: receipt.value, DryRunReceipt: dryRunReceipt.value,
	})
	if err != nil || result.Status == "blocked" {
		result.ContractVersion = taskmigration.ContractVersion
		result.Status = "blocked"
		if result.Mode != taskmigration.ModeDryRun && result.Mode != taskmigration.ModeApply {
			result.Mode = mode.value
		}
		code := safeErrorCode(result.ErrorCode)
		if code == "" {
			code = operationError
		}
		result.ErrorCode = code
		return emitReceipt(stdout, stderr, result, code, exitOperation)
	}
	wantStatus := "ready"
	if mode.value == taskmigration.ModeApply {
		wantStatus = "applied"
	}
	if result.ContractVersion != taskmigration.ContractVersion || result.Status != wantStatus || result.Mode != mode.value || result.ErrorCode != "" {
		return emitBlocked(stdout, stderr, mode.value, operationError, exitOperation)
	}
	return emitReceipt(stdout, stderr, result, "", exitOK)
}

func required(value stringFlag) bool {
	return value.set && strings.TrimSpace(value.value) != ""
}

func emitBlocked(stdout, stderr io.Writer, mode, code string, exitCode int) int {
	result := taskmigration.Receipt{ContractVersion: taskmigration.ContractVersion, Status: "blocked", ErrorCode: safeErrorCode(code)}
	if mode == taskmigration.ModeDryRun || mode == taskmigration.ModeApply {
		result.Mode = mode
	}
	return emitReceipt(stdout, stderr, result, code, exitCode)
}

func emitReceipt(stdout, stderr io.Writer, result taskmigration.Receipt, code string, exitCode int) int {
	if code != "" {
		code = safeErrorCode(code)
		if code == "" {
			code = operationError
		}
		result.ErrorCode = code
	}
	data, err := json.Marshal(result)
	if err != nil {
		return emitError(stderr, operationError, exitOperation)
	}
	if _, err := fmt.Fprintln(stdout, string(data)); err != nil {
		return emitError(stderr, operationError, exitOperation)
	}
	if code != "" {
		if _, err := fmt.Fprintln(stderr, code); err != nil {
			return exitOperation
		}
	}
	return exitCode
}

func emitError(stderr io.Writer, code string, exitCode int) int {
	_, _ = fmt.Fprintln(stderr, code)
	return exitCode
}

func safeErrorCode(code string) string {
	if code == "" || len(code) > 128 {
		return ""
	}
	for _, character := range code {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' && character != '-' {
			return ""
		}
	}
	return code
}
