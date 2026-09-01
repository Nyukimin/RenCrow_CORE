package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/dcimigration"
)

// cutoverOperation is a package-local test seam.  Its production value is
// the public owner operation; parser/form errors are rejected before it is
// reached.
var cutoverOperation = dcimigration.Cutover

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("rencrow-dci-migrate", flag.ContinueOnError)
	// The standard flag package echoes the unknown flag/value.  A migration
	// command must keep diagnostics bounded and must never echo arbitrary paths,
	// tokens, or secrets supplied on its command line.
	flags.SetOutput(io.Discard)
	mode := flags.String("mode", "", "migration mode; dry-run, capture, build, or cutover")
	snapshotDir := flags.String("snapshot-dir", "", "read-only snapshot root")
	buildDir := flags.String("build-dir", "", "fresh offline build root")
	buildReceipt := flags.String("build-receipt", "", "ready offline build receipt")
	expectedBuildReceiptSHA256 := flags.String("expected-build-receipt-sha256", "", "expected build receipt SHA-256")
	installedRuntime := flags.String("installed-runtime", "", "currently installed runtime")
	stagedRuntime := flags.String("staged-runtime", "", "staged runtime")
	expectedInstalledRuntimeSHA256 := flags.String("expected-installed-runtime-sha256", "", "expected installed runtime SHA-256")
	expectedStagedRuntimeSHA256 := flags.String("expected-staged-runtime-sha256", "", "expected staged runtime SHA-256")
	activeDCI := flags.String("active-dci", "", "active DCI source")
	activeDCIJSONL := flags.String("active-dci-jsonl", "", "active DCI JSONL source")
	activeEventStore := flags.String("active-event-store", "", "active Event Store source")
	activeL1 := flags.String("active-l1", "", "active L1 source")
	activeArchive := flags.String("active-archive", "", "active archive source")
	activeConfig := flags.String("active-config", "", "active CORE config")
	rollbackDir := flags.String("rollback-dir", "", "fresh rollback root")
	cutoverReceipt := flags.String("cutover-receipt", "", "fresh DCI cutover receipt")
	serviceReceipt := flags.String("service-receipt", "", "fresh service cutover receipt")
	captureReceipt := flags.String("capture-receipt", "", "captured snapshot receipt")
	dryRunManifest := flags.String("dry-run-manifest", "", "ready dry-run manifest")
	sourceDCI := flags.String("source-dci", "source-dci", "legacy DCI SQLite snapshot")
	sourceDCIJSONL := flags.String("source-dci-jsonl", "source-dci-jsonl", "legacy DCI JSONL snapshot")
	sourceEventStore := flags.String("source-event-store", "source-event-store", "canonical Event Store snapshot")
	sourceL1 := flags.String("source-l1", "source-l1", "current L1 SQLite snapshot")
	sourceArchive := flags.String("source-archive", "source-archive", "archive SQLite snapshot")
	manifest := flags.String("manifest", "manifest.json", "new bounded receipt path")
	liveDCI := flags.String("live-dci", "", "live DCI SQLite source for capture")
	liveDCIJSONL := flags.String("live-dci-jsonl", "", "live DCI JSONL source for capture")
	liveEventStore := flags.String("live-event-store", "", "live Event Store SQLite source for capture")
	liveL1 := flags.String("live-l1", "", "live current L1 SQLite source for capture")
	liveArchive := flags.String("live-archive", "", "live archive SQLite source for capture")
	expectedSearches := flags.Int("expected-searches", -1, "expected deduplicated search count")
	expectedReadEvents := flags.Int("expected-read-events", -1, "expected deduplicated read event count")
	expectedEvidenceEvents := flags.Int("expected-evidence-events", -1, "expected deduplicated evidence event count")
	expectedTotalEvents := flags.Int("expected-total-events", -1, "expected planned event count")
	expectedLimitSteps := flags.Int("expected-legacy-limit-steps", -1, "expected excluded legacy limit step count")
	expectedNormalizedTextValues := flags.Int("expected-normalized-text-values", -1, "expected evidence text values normalized")
	expectedInvalidUTF8Bytes := flags.Int("expected-invalid-utf8-bytes", -1, "expected invalid UTF-8 byte count")
	if err := flags.Parse(args); err != nil {
		_, _ = fmt.Fprintln(stderr, "invalid command arguments")
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "unexpected positional arguments")
		return 2
	}
	visited := make(map[string]bool)
	flags.Visit(func(flag *flag.Flag) { visited[flag.Name] = true })
	wasSet := func(name string) bool { return visited[name] }
	if *mode != dcimigration.ModeDryRun && *mode != dcimigration.ModeCapture && *mode != dcimigration.ModeBuild && *mode != dcimigration.ModeCutover {
		_, _ = fmt.Fprintln(stderr, "only --mode dry-run, capture, build, or cutover is supported")
		return 2
	}
	buildOnly := []string{"build-dir", "capture-receipt", "dry-run-manifest"}
	sourceFlags := []string{"source-dci", "source-dci-jsonl", "source-event-store", "source-l1", "source-archive"}
	liveFlags := []string{"live-dci", "live-dci-jsonl", "live-event-store", "live-l1", "live-archive"}
	expectedFlags := []string{"expected-searches", "expected-read-events", "expected-evidence-events", "expected-total-events", "expected-legacy-limit-steps", "expected-normalized-text-values", "expected-invalid-utf8-bytes"}
	cutoverFlags := []string{
		"build-receipt", "expected-build-receipt-sha256", "installed-runtime", "staged-runtime",
		"expected-installed-runtime-sha256", "expected-staged-runtime-sha256", "active-dci",
		"active-dci-jsonl", "active-event-store", "active-l1", "active-archive", "active-config",
		"rollback-dir", "cutover-receipt", "service-receipt",
	}
	cutoverRequiredFlags := append([]string{"build-dir"}, cutoverFlags...)
	rejectVisited := func(names []string) bool {
		for _, name := range names {
			if wasSet(name) {
				return true
			}
		}
		return false
	}
	allVisited := func(names []string) bool {
		for _, name := range names {
			if !wasSet(name) {
				return false
			}
		}
		return true
	}
	if *mode == dcimigration.ModeCutover {
		incompatible := append([]string{"snapshot-dir", "manifest"}, sourceFlags...)
		incompatible = append(incompatible, liveFlags...)
		incompatible = append(incompatible, expectedFlags...)
		incompatible = append(incompatible, "capture-receipt", "dry-run-manifest")
		if rejectVisited(incompatible) {
			_, _ = fmt.Fprintln(stderr, "cutover mode rejects incompatible flags")
			return 2
		}
		if !allVisited(cutoverRequiredFlags) {
			_, _ = fmt.Fprintln(stderr, "cutover mode requires all cutover flags")
			return 2
		}
		for _, name := range cutoverRequiredFlags {
			if strings.TrimSpace(flags.Lookup(name).Value.String()) == "" {
				_, _ = fmt.Fprintln(stderr, "cutover mode requires all cutover flags")
				return 2
			}
		}
		result, err := cutoverOperation(context.Background(), dcimigration.CutoverOptions{
			BuildRoot:                      *buildDir,
			BuildReceipt:                   *buildReceipt,
			ExpectedBuildReceiptSHA256:     *expectedBuildReceiptSHA256,
			InstalledRuntime:               *installedRuntime,
			StagedRuntime:                  *stagedRuntime,
			ExpectedInstalledRuntimeSHA256: *expectedInstalledRuntimeSHA256,
			ExpectedStagedRuntimeSHA256:    *expectedStagedRuntimeSHA256,
			RollbackDir:                    *rollbackDir,
			CutoverReceipt:                 *cutoverReceipt,
			ServiceReceipt:                 *serviceReceipt,
			ActiveDCI:                      *activeDCI,
			ActiveDCIJSONL:                 *activeDCIJSONL,
			ActiveEventStore:               *activeEventStore,
			ActiveL1:                       *activeL1,
			ActiveArchive:                  *activeArchive,
			ActiveConfig:                   *activeConfig,
		})
		return writeReceipt(stdout, stderr, result, err)
	}
	if !wasSet("snapshot-dir") || strings.TrimSpace(*snapshotDir) == "" {
		_, _ = fmt.Fprintln(stderr, "--snapshot-dir is required")
		return 2
	}
	if *mode == dcimigration.ModeBuild {
		if rejectVisited(append(append(append(append(append([]string{}, sourceFlags...), liveFlags...), expectedFlags...), "manifest"), cutoverFlags...)) {
			_, _ = fmt.Fprintln(stderr, "build mode rejects incompatible flags")
			return 2
		}
		if !wasSet("build-dir") || strings.TrimSpace(*buildDir) == "" || !wasSet("capture-receipt") || strings.TrimSpace(*captureReceipt) == "" || !wasSet("dry-run-manifest") || strings.TrimSpace(*dryRunManifest) == "" {
			_, _ = fmt.Fprintln(stderr, "build mode requires --build-dir, --capture-receipt, and --dry-run-manifest")
			return 2
		}
		result, err := dcimigration.Build(context.Background(), dcimigration.BuildOptions{
			SnapshotDir: *snapshotDir, BuildDir: *buildDir, CaptureReceipt: *captureReceipt,
			DryRunManifest: *dryRunManifest,
			AgentIDs:       []string{string(domconv.SpeakerMio), string(domconv.SpeakerShiro), string(domconv.SpeakerKuro), string(domconv.SpeakerMidori)},
		})
		return writeReceipt(stdout, stderr, result, err)
	}
	if *mode == dcimigration.ModeCapture {
		if rejectVisited(append(append(append(append(append([]string{}, sourceFlags...), expectedFlags...), buildOnly...), "manifest"), cutoverFlags...)) {
			_, _ = fmt.Fprintln(stderr, "capture mode rejects incompatible flags")
			return 2
		}
		if !allVisited(liveFlags) {
			_, _ = fmt.Fprintln(stderr, "capture mode requires all live source flags")
			return 2
		}
		result, err := dcimigration.Capture(context.Background(), dcimigration.CaptureOptions{
			SnapshotDir: *snapshotDir, LiveDCI: *liveDCI, LiveDCIJSONL: *liveDCIJSONL,
			LiveEventStore: *liveEventStore, LiveL1: *liveL1, LiveArchive: *liveArchive,
		})
		return writeReceipt(stdout, stderr, result, err)
	}
	if rejectVisited(append(append(append([]string{}, liveFlags...), buildOnly...), cutoverFlags...)) {
		_, _ = fmt.Fprintln(stderr, "dry-run mode rejects incompatible flags")
		return 2
	}

	expected := dcimigration.ExpectedCounts{
		Searches: *expectedSearches, ReadEvents: *expectedReadEvents,
		EvidenceEvents: *expectedEvidenceEvents, TotalEvents: *expectedTotalEvents,
		LegacyLimitSteps: *expectedLimitSteps, NormalizedTextValues: *expectedNormalizedTextValues,
		InvalidUTF8Bytes: *expectedInvalidUTF8Bytes,
	}
	if expected.Searches < 0 || expected.ReadEvents < 0 || expected.EvidenceEvents < 0 || expected.TotalEvents < 0 || expected.LegacyLimitSteps < 0 || expected.NormalizedTextValues < 0 || expected.InvalidUTF8Bytes < 0 {
		_, _ = fmt.Fprintln(stderr, "all expected count flags are required")
		return 2
	}
	result, err := dcimigration.DryRun(context.Background(), dcimigration.Options{
		SnapshotDir: *snapshotDir, SourceDCI: *sourceDCI, SourceDCIJSONL: *sourceDCIJSONL,
		SourceEventStore: *sourceEventStore, SourceL1: *sourceL1, SourceArchive: *sourceArchive,
		Manifest: *manifest, Expected: expected,
		AgentIDs: []string{string(domconv.SpeakerMio), string(domconv.SpeakerShiro), string(domconv.SpeakerKuro), string(domconv.SpeakerMidori)},
	})
	return writeReceipt(stdout, stderr, result, err)
}

func writeReceipt(stdout, stderr io.Writer, receipt any, operationErr error) int {
	encoded, marshalErr := json.Marshal(receipt)
	if marshalErr != nil {
		_, _ = fmt.Fprintln(stderr, "receipt_encode")
		return 1
	}
	_, _ = stdout.Write(append(encoded, '\n'))
	if result, ok := receipt.(dcimigration.ServiceCutoverReceipt); ok {
		if operationErr == nil && result.Status == dcimigration.CutoverStatusApplied {
			if result.ErrorCode != "" {
				if isBoundedErrorCode(result.ErrorCode) {
					_, _ = fmt.Fprintln(stderr, result.ErrorCode)
				} else {
					_, _ = fmt.Fprintln(stderr, "dci migration blocked")
				}
			}
			return 0
		}
		if isBoundedErrorCode(result.ErrorCode) {
			_, _ = fmt.Fprintln(stderr, result.ErrorCode)
		} else {
			_, _ = fmt.Fprintln(stderr, "dci migration blocked")
		}
		return 1
	}
	if operationErr == nil {
		return 0
	}
	if result, ok := receipt.(dcimigration.CaptureReceipt); ok && result.ErrorCode != "" {
		_, _ = fmt.Fprintln(stderr, result.ErrorCode)
		return 1
	}
	if result, ok := receipt.(dcimigration.Manifest); ok && result.ErrorCode != "" {
		_, _ = fmt.Fprintln(stderr, result.ErrorCode)
		return 1
	}
	if result, ok := receipt.(dcimigration.BuildReceipt); ok && result.ErrorCode != "" {
		_, _ = fmt.Fprintln(stderr, result.ErrorCode)
		return 1
	}
	_, _ = fmt.Fprintln(stderr, "dci migration blocked")
	return 1
}

func isBoundedErrorCode(value string) bool {
	// This intentionally duplicates the private receipt syntax at the CLI
	// output boundary so malformed receipt values fail closed without leakage.
	if len(value) == 0 || len(value) > 64 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for index := 1; index < len(value); index++ {
		char := value[index]
		if !((char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '_') {
			return false
		}
	}
	return true
}
