// rencrow-event-trace-repair rebuilds a production Event Store snapshot or
// performs the explicitly gated checksum-bound cutover.
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
	buildManifest := flags.String("build-manifest", "", "checksum-bound build receipt for apply mode")
	expectedBuildManifestSHA256 := flags.String("expected-build-manifest-sha256", "", "expected build receipt SHA256 for apply mode")
	activeStore := flags.String("active-store", "", "exact active Event Store target for apply mode")
	rollbackDir := flags.String("rollback-dir", "", "new rollback directory for apply mode")
	installedRuntime := flags.String("installed-runtime-binary", "", "exact installed runtime target for apply mode")
	stagedRuntime := flags.String("staged-runtime-binary", "", "staged runtime artifact for apply mode")
	expectedInstalledRuntimeSHA256 := flags.String("expected-installed-runtime-sha256", "", "expected installed runtime SHA256 for apply mode")
	expectedStagedRuntimeSHA256 := flags.String("expected-staged-runtime-sha256", "", "expected staged runtime SHA256 for apply mode")
	mode := flags.String("mode", "", "repair mode: dry-run, build, or apply")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	var receipt any
	var err error
	if strings.TrimSpace(*mode) == eventtracerepair.ModeApply {
		receipt, err = eventtracerepair.Apply(context.Background(), eventtracerepair.ApplyOptions{
			SnapshotDir: strings.TrimSpace(*snapshotDir), SourceStore: strings.TrimSpace(*sourceStore),
			OutputStore: strings.TrimSpace(*outputStore), BuildManifest: strings.TrimSpace(*buildManifest),
			ExpectedBuildManifestSHA256: strings.TrimSpace(*expectedBuildManifestSHA256),
			ActiveStore:                 strings.TrimSpace(*activeStore), RollbackDir: strings.TrimSpace(*rollbackDir),
			InstalledRuntimeBinary: strings.TrimSpace(*installedRuntime), StagedRuntimeBinary: strings.TrimSpace(*stagedRuntime),
			ExpectedInstalledRuntimeSHA256: strings.TrimSpace(*expectedInstalledRuntimeSHA256),
			ExpectedStagedRuntimeSHA256:    strings.TrimSpace(*expectedStagedRuntimeSHA256), Manifest: strings.TrimSpace(*manifest),
		})
	} else {
		receipt, err = eventtracerepair.Run(context.Background(), eventtracerepair.Options{
			SnapshotDir: strings.TrimSpace(*snapshotDir), SourceStore: strings.TrimSpace(*sourceStore),
			OutputStore: strings.TrimSpace(*outputStore), Manifest: strings.TrimSpace(*manifest),
			DryRunManifest: strings.TrimSpace(*dryRunManifest), Mode: strings.TrimSpace(*mode),
		})
	}
	encoded, encodeErr := json.Marshal(receipt)
	if encodeErr != nil {
		return encodeErr
	}
	if _, writeErr := fmt.Fprintln(stdout, string(encoded)); writeErr != nil {
		return writeErr
	}
	return err
}
