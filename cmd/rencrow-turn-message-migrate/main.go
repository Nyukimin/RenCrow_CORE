package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/turnmigration"
)

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr))
}

func runCLI(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("rencrow-turn-message-migrate", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dbPath := flags.String("db", "", "explicit SQLite database path")
	manifestPath := flags.String("manifest", "", "receipt manifest output path")
	mode := flags.String("mode", turnmigration.ModeDryRun, "migration mode: dry-run or apply")
	priorManifest := flags.String("prior-dry-run-manifest", "", "ready dry-run receipt required by apply mode")
	priorReceipt := flags.String("dry-run-receipt", "", "alias for --prior-dry-run-manifest")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		_, _ = fmt.Fprintln(stderr, "invalid command arguments")
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "unexpected positional argument")
		return 2
	}
	if *priorManifest != "" && *priorReceipt != "" && *priorManifest != *priorReceipt {
		_, _ = fmt.Fprintln(stderr, "conflicting prior dry-run manifest flags")
		return 2
	}
	if *priorManifest == "" {
		*priorManifest = *priorReceipt
	}
	receipt, err := turnmigration.Run(context.Background(), turnmigration.Options{
		DBPath:                  *dbPath,
		ManifestPath:            *manifestPath,
		PriorDryRunManifestPath: *priorManifest,
		Mode:                    *mode,
	})
	if encodeErr := writeReceipt(stdout, receipt); encodeErr != nil {
		_, _ = fmt.Fprintln(stderr, "write receipt:", encodeErr)
		return 1
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func writeReceipt(writer io.Writer, receipt turnmigration.Receipt) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(receipt)
}
