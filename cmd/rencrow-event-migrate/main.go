// rencrow-event-migrate migrates legacy event snapshots into the canonical
// append-only Event Store. It is intentionally a standalone one-shot CLI.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventmigration"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "[NG] %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("rencrow-event-migrate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	snapshotDir := flags.String("snapshot-dir", "", "production snapshot directory")
	aiSQLite := flags.String("ai-sqlite", "", "legacy AI Workflow SQLite snapshot")
	aiJSONL := flags.String("ai-jsonl", "", "legacy AI Workflow JSONL snapshot")
	superagentSQLite := flags.String("superagent-sqlite", "", "legacy SuperAgent SQLite snapshot")
	superagentJSONL := flags.String("superagent-jsonl", "", "legacy SuperAgent JSONL snapshot")
	eventStore := flags.String("event-store", "", "canonical Event Store SQLite path")
	manifest := flags.String("manifest", "", "output JSON receipt manifest path")
	dryRunManifest := flags.String("dry-run-manifest", "", "prior dry-run receipt manifest path")
	mode := flags.String("mode", "", "migration mode: dry-run or apply")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	options := eventmigration.Options{
		SnapshotDir:      strings.TrimSpace(*snapshotDir),
		AISQLite:         strings.TrimSpace(*aiSQLite),
		AIJSONL:          strings.TrimSpace(*aiJSONL),
		SuperagentSQLite: strings.TrimSpace(*superagentSQLite),
		SuperagentJSONL:  strings.TrimSpace(*superagentJSONL),
		EventStore:       strings.TrimSpace(*eventStore),
		Manifest:         strings.TrimSpace(*manifest),
		DryRunManifest:   strings.TrimSpace(*dryRunManifest),
		Mode:             strings.TrimSpace(*mode),
	}
	receipt, err := eventmigration.Run(context.Background(), options)
	encoded, encodeErr := json.Marshal(receipt)
	if encodeErr != nil {
		return encodeErr
	}
	if _, writeErr := fmt.Fprintln(stdout, string(encoded)); writeErr != nil {
		return writeErr
	}
	return err
}
