// Command rencrow-turn-input-migrate runs the one-shot canonical TurnInput
// session-history migration owned by RenCrow_CORE.
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

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/turninputmigration"
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

func (f *stringFlag) String() string {
	return f.value
}

func (f *stringFlag) Set(value string) error {
	if f.set {
		return fmt.Errorf("duplicate flag")
	}
	f.value = value
	f.set = true
	return nil
}

type runMigration func(context.Context, turninputmigration.Options) (turninputmigration.Receipt, error)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	exitCode := run(ctx, os.Args[1:], os.Stdout, os.Stderr, turninputmigration.Run)
	stop()
	if exitCode != exitOK {
		os.Exit(exitCode)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, migrate runMigration) int {
	flags := flag.NewFlagSet("rencrow-turn-input-migrate", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var mode, source, eventDB, conversationDB, receipt, output, dryRunReceipt stringFlag
	flags.Var(&mode, "mode", "migration mode: dry-run or apply")
	flags.Var(&source, "source", "legacy Session source directory")
	flags.Var(&eventDB, "event-db", "read-only Event Store SQLite path")
	flags.Var(&conversationDB, "conversation-db", "read-only conversation SQLite path")
	flags.Var(&receipt, "receipt", "fresh output receipt path")
	flags.Var(&output, "output", "existing empty apply output directory")
	flags.Var(&dryRunReceipt, "dry-run-receipt", "prior ready dry-run receipt path")

	if err := flags.Parse(args); err != nil {
		return emitBlocked(stdout, stderr, "", argumentError, exitArguments)
	}
	if flags.NArg() != 0 {
		return emitBlocked(stdout, stderr, mode.value, argumentError, exitArguments)
	}
	if !requiredFlag(mode) || !requiredFlag(source) || !requiredFlag(eventDB) || !requiredFlag(conversationDB) || !requiredFlag(receipt) {
		return emitBlocked(stdout, stderr, mode.value, argumentError, exitArguments)
	}
	if mode.value != turninputmigration.ModeDryRun && mode.value != turninputmigration.ModeApply {
		return emitBlocked(stdout, stderr, mode.value, argumentError, exitArguments)
	}
	if mode.value == turninputmigration.ModeDryRun {
		if output.set || dryRunReceipt.set {
			return emitBlocked(stdout, stderr, mode.value, argumentError, exitArguments)
		}
	} else if !requiredFlag(output) || !requiredFlag(dryRunReceipt) {
		return emitBlocked(stdout, stderr, mode.value, argumentError, exitArguments)
	}

	if migrate == nil {
		return emitBlocked(stdout, stderr, mode.value, operationError, exitOperation)
	}
	receiptValue, err := migrate(ctx, turninputmigration.Options{
		Mode:               mode.value,
		SourceDir:          source.value,
		EventDBPath:        eventDB.value,
		ConversationDBPath: conversationDB.value,
		OutputDir:          output.value,
		ReceiptPath:        receipt.value,
		DryRunReceipt:      dryRunReceipt.value,
	})
	if err != nil {
		receiptValue.ContractVersion = turninputmigration.ContractVersion
		receiptValue.Status = "blocked"
		if receiptValue.Mode != turninputmigration.ModeDryRun && receiptValue.Mode != turninputmigration.ModeApply {
			receiptValue.Mode = mode.value
		}
		code := safeErrorCode(receiptValue.ErrorCode)
		if code == "" {
			code = operationError
		}
		receiptValue.ErrorCode = code
		return emitReceipt(stdout, stderr, receiptValue, code, exitOperation)
	}
	if receiptValue.Status == "blocked" {
		receiptValue.ContractVersion = turninputmigration.ContractVersion
		code := safeErrorCode(receiptValue.ErrorCode)
		if code == "" {
			code = operationError
		}
		receiptValue.ErrorCode = code
		return emitReceipt(stdout, stderr, receiptValue, code, exitOperation)
	}
	wantStatus := "ready"
	if mode.value == turninputmigration.ModeApply {
		wantStatus = "applied"
	}
	if receiptValue.ContractVersion != turninputmigration.ContractVersion ||
		receiptValue.Status != wantStatus || receiptValue.Mode != mode.value || receiptValue.ErrorCode != "" {
		return emitBlocked(stdout, stderr, mode.value, operationError, exitOperation)
	}
	return emitReceipt(stdout, stderr, receiptValue, "", exitOK)
}

func requiredFlag(value stringFlag) bool {
	return value.set && strings.TrimSpace(value.value) != ""
}

func emitBlocked(stdout, stderr io.Writer, mode, code string, exitCode int) int {
	receipt := turninputmigration.Receipt{
		ContractVersion: turninputmigration.ContractVersion,
		Status:          "blocked",
		ErrorCode:       safeErrorCode(code),
	}
	if mode == turninputmigration.ModeDryRun || mode == turninputmigration.ModeApply {
		receipt.Mode = mode
	}
	return emitReceipt(stdout, stderr, receipt, code, exitCode)
}

func emitReceipt(stdout, stderr io.Writer, receipt turninputmigration.Receipt, code string, exitCode int) int {
	if code != "" {
		code = safeErrorCode(code)
		if code == "" {
			code = operationError
		}
		receipt.ErrorCode = code
	}
	data, err := json.Marshal(receipt)
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
	if _, err := fmt.Fprintln(stderr, code); err != nil {
		return exitOperation
	}
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
