// Command rencrow-session-migrate builds canonical Session files from a
// writer-stopped snapshot. It never mutates the source or active directory.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/sessionmigration"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "[NG] canonical Session migration failed")
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("rencrow-session-migrate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	mode := flags.String("mode", "", "dry-run or apply")
	source := flags.String("source-dir", "", "writer-stopped Session snapshot directory")
	output := flags.String("output-dir", "", "existing empty output directory for apply")
	receipt := flags.String("receipt", "", "owner-only JSON receipt path")
	dryReceipt := flags.String("dry-run-receipt", "", "matching dry-run receipt required by apply")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	result, err := sessionmigration.Run(context.Background(), sessionmigration.Options{
		Mode: strings.TrimSpace(*mode), SourceDir: strings.TrimSpace(*source), OutputDir: strings.TrimSpace(*output),
		ReceiptPath: strings.TrimSpace(*receipt), DryRunReceipt: strings.TrimSpace(*dryReceipt),
	})
	raw, encodeErr := json.Marshal(result)
	if encodeErr != nil {
		return encodeErr
	}
	if _, writeErr := fmt.Fprintln(stdout, string(raw)); writeErr != nil {
		return writeErr
	}
	return err
}
