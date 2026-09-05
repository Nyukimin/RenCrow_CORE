// rencrow-event-task-migrate builds fresh Step09 Event Store, execution-report,
// and resilience artifacts from caller-provided writer-stopped snapshots. It
// never swaps a live artifact.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventtaskmigration"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "[NG] %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("rencrow-event-task-migrate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	mode := flags.String("mode", "", "migration mode: dry-run or apply")
	snapshotDir := flags.String("snapshot-dir", "", "writer-stopped snapshot directory")
	sourceEventStore := flags.String("source-event-store", "", "legacy canonical Event Store snapshot")
	sourceConversationL1 := flags.String("source-conversation-l1", "", "conversation L1 receipt snapshot")
	sourceExecutionReports := flags.String("source-execution-reports", "", "legacy execution report JSONL snapshot")
	sourceResilienceRoot := flags.String("source-resilience-root", "", "legacy complete resilience root snapshot")
	targetEventStore := flags.String("target-event-store", "", "absent fresh Step09 Event Store path")
	targetExecutionReports := flags.String("target-execution-reports", "", "absent fresh Step09 execution report JSONL path")
	targetResilienceRoot := flags.String("target-resilience-root", "", "absent fresh Step09 resilience root path")
	manifest := flags.String("manifest", "", "output metadata-only JSON receipt")
	dryRunManifest := flags.String("dry-run-manifest", "", "ready dry-run receipt required by apply")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	receipt, err := eventtaskmigration.Run(context.Background(), eventtaskmigration.Options{
		Mode: strings.TrimSpace(*mode), SnapshotDir: strings.TrimSpace(*snapshotDir),
		SourceEventStore: strings.TrimSpace(*sourceEventStore), SourceConversationL1: strings.TrimSpace(*sourceConversationL1),
		SourceExecutionReports: strings.TrimSpace(*sourceExecutionReports), TargetEventStore: strings.TrimSpace(*targetEventStore),
		SourceResilienceRoot: strings.TrimSpace(*sourceResilienceRoot), TargetExecutionReports: strings.TrimSpace(*targetExecutionReports),
		TargetResilienceRoot: strings.TrimSpace(*targetResilienceRoot), Manifest: strings.TrimSpace(*manifest),
		DryRunManifest: strings.TrimSpace(*dryRunManifest),
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
