// rencrow-event-trace-repair rebuilds a production Event Store snapshot into
// a separate file. It never edits or swaps the active store.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventtracerepair"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "[NG] %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("rencrow-event-trace-repair", flag.ContinueOnError)
	flags.SetOutput(stderr)
	snapshotDir := flags.String("snapshot-dir", "", "directory containing the production snapshot")
	sourceStore := flags.String("source-store", "", "read-only canonical Event Store snapshot")
	outputStore := flags.String("output-store", "", "new repaired Event Store path")
	manifest := flags.String("manifest", "", "output bounded JSON receipt")
	dryRunManifest := flags.String("dry-run-manifest", "", "checksum-bound prior dry-run receipt")
	mode := flags.String("mode", "", "repair mode: dry-run or build")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	receipt, err := eventtracerepair.Run(context.Background(), eventtracerepair.Options{
		SnapshotDir: strings.TrimSpace(*snapshotDir), SourceStore: strings.TrimSpace(*sourceStore),
		OutputStore: strings.TrimSpace(*outputStore), Manifest: strings.TrimSpace(*manifest),
		DryRunManifest: strings.TrimSpace(*dryRunManifest), Mode: strings.TrimSpace(*mode),
	})
	encoded, encodeErr := json.Marshal(receipt)
	if encodeErr != nil {
		return encodeErr
	}
	if _, writeErr := fmt.Fprintln(stdout, string(encoded)); writeErr != nil {
		return writeErr
	}
	return err
}
