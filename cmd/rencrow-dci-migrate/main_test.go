package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/dcimigration"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	_ "modernc.org/sqlite"
)

func TestRunRejectsUnsupportedMode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--mode", "apply", "--snapshot-dir", t.TempDir()}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("unsupported mode unexpectedly succeeded")
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "only --mode dry-run, capture, build, or cutover is supported") {
		t.Fatalf("unsupported mode output = stdout %q stderr %q", stdout.String(), stderr.String())
	}
}

func TestRunRequiresAllExpectedCounts(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--mode", "dry-run", "--snapshot-dir", t.TempDir()}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("missing expected counts unexpectedly succeeded")
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "all expected count flags are required") {
		t.Fatalf("missing expected count output = stdout %q stderr %q", stdout.String(), stderr.String())
	}
}

func TestRunRequiresUTF8NormalizationExpectedCounts(t *testing.T) {
	snapshot := filepath.Join(t.TempDir(), "utf8-counts-required")
	var stdout, stderr bytes.Buffer
	args := []string{
		"--mode", "dry-run", "--snapshot-dir", snapshot,
		"--expected-searches", "0", "--expected-read-events", "0",
		"--expected-evidence-events", "0", "--expected-total-events", "0",
		"--expected-legacy-limit-steps", "0",
	}
	code := run(args, &stdout, &stderr)
	if code != 2 || stdout.Len() != 0 || stderr.String() != "all expected count flags are required\n" {
		t.Fatalf("missing UTF-8 normalization counts result: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stderr.String(), snapshot) {
		t.Fatalf("missing UTF-8 normalization counts leaked snapshot path: %q", stderr.String())
	}
}

func TestRunRejectsNegativeUTF8NormalizationExpectedCounts(t *testing.T) {
	snapshot := filepath.Join(t.TempDir(), "utf8-counts-negative")
	var stdout, stderr bytes.Buffer
	args := []string{
		"--mode", "dry-run", "--snapshot-dir", snapshot,
		"--expected-searches", "0", "--expected-read-events", "0",
		"--expected-evidence-events", "0", "--expected-total-events", "0",
		"--expected-legacy-limit-steps", "0", "--expected-normalized-text-values", "-1",
		"--expected-invalid-utf8-bytes", "0",
	}
	code := run(args, &stdout, &stderr)
	if code != 2 || stdout.Len() != 0 || stderr.String() != "all expected count flags are required\n" {
		t.Fatalf("negative UTF-8 normalization counts result: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stderr.String(), snapshot) {
		t.Fatalf("negative UTF-8 normalization counts leaked snapshot path: %q", stderr.String())
	}
}

func TestRunRejectsPositionalArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--mode", "dry-run", "--snapshot-dir", t.TempDir(), "--expected-searches", "0", "--expected-read-events", "0", "--expected-evidence-events", "0", "--expected-total-events", "0", "--expected-legacy-limit-steps", "0", "unexpected"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("positional argument unexpectedly succeeded")
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "unexpected positional arguments") {
		t.Fatalf("positional argument output = stdout %q stderr %q", stdout.String(), stderr.String())
	}
}

func TestRunRejectsParserInputWithoutEchoingArguments(t *testing.T) {
	secretPath := filepath.Join(t.TempDir(), "secret-token")
	var stdout, stderr bytes.Buffer
	code := run([]string{"--mode", "dry-run", "--snapshot-dir", secretPath, "--unknown-flag=token-value"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("invalid parser input unexpectedly succeeded")
	}
	if got := stderr.String(); got != "invalid command arguments\n" {
		t.Fatalf("parser diagnostic = %q, want fixed diagnostic", got)
	}
	if stdout.Len() != 0 || strings.Contains(stderr.String(), secretPath) || strings.Contains(stderr.String(), "token-value") {
		t.Fatalf("parser output leaked argument: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunRejectsEveryAlternateMode(t *testing.T) {
	for _, mode := range []string{"apply"} {
		t.Run(mode, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run([]string{"--mode", mode, "--snapshot-dir", t.TempDir()}, &stdout, &stderr)
			if code == 0 || stdout.Len() != 0 || stderr.String() != "only --mode dry-run, capture, build, or cutover is supported\n" {
				t.Fatalf("mode %s result: code=%d stdout=%q stderr=%q", mode, code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunCutoverMapsExactFlagsAndEmitsOneJSON(t *testing.T) {
	args := cutoverCLIArgsForTest()
	want := dcimigration.CutoverOptions{
		BuildRoot:                      "build-root-secret",
		BuildReceipt:                   "build-receipt-secret",
		ExpectedBuildReceiptSHA256:     strings.Repeat("a", 64),
		InstalledRuntime:               "installed-runtime-secret",
		StagedRuntime:                  "staged-runtime-secret",
		ExpectedInstalledRuntimeSHA256: strings.Repeat("b", 64),
		ExpectedStagedRuntimeSHA256:    strings.Repeat("c", 64),
		RollbackDir:                    "rollback-secret",
		CutoverReceipt:                 "cutover-receipt-secret",
		ServiceReceipt:                 "service-receipt-secret",
		ActiveDCI:                      "active-dci-secret",
		ActiveDCIJSONL:                 "active-jsonl-secret",
		ActiveEventStore:               "active-events-secret",
		ActiveL1:                       "active-l1-secret",
		ActiveArchive:                  "active-archive-secret",
		ActiveConfig:                   "active-config-secret",
	}
	now := time.Now().UTC()
	ownerReceipt := dcimigration.ServiceCutoverReceipt{
		SchemaVersion: dcimigration.ServiceCutoverSchemaVersion,
		Mode:          dcimigration.ModeCutover,
		Status:        dcimigration.CutoverStatusBlocked,
		StartedAt:     now,
		CompletedAt:   now,
		ErrorCode:     "service_running",
	}
	oldOperation := cutoverOperation
	t.Cleanup(func() { cutoverOperation = oldOperation })
	var got dcimigration.CutoverOptions
	calls := 0
	cutoverOperation = func(_ context.Context, options dcimigration.CutoverOptions) (dcimigration.ServiceCutoverReceipt, error) {
		calls++
		got = options
		return ownerReceipt, nil
	}
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	if code != 1 || calls != 1 || !reflect.DeepEqual(got, want) {
		t.Fatalf("cutover result: code=%d calls=%d options=%#v", code, calls, got)
	}
	if stderr.String() != "service_running\n" {
		t.Fatalf("cutover stderr = %q", stderr.String())
	}
	var decoded dcimigration.ServiceCutoverReceipt
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil || !reflect.DeepEqual(decoded, ownerReceipt) || !strings.HasSuffix(stdout.String(), "\n") || strings.Count(stdout.String(), "\n") != 1 {
		t.Fatalf("cutover stdout = %q, decoded=%#v, err=%v", stdout.String(), decoded, err)
	}
}

func TestRunCutoverRejectsMissingAndExplicitlyEmptyRequiredFlags(t *testing.T) {
	required := []string{
		"build-dir", "build-receipt", "expected-build-receipt-sha256", "installed-runtime", "staged-runtime",
		"expected-installed-runtime-sha256", "expected-staged-runtime-sha256", "active-dci", "active-dci-jsonl",
		"active-event-store", "active-l1", "active-archive", "active-config", "rollback-dir", "cutover-receipt", "service-receipt",
	}
	oldOperation := cutoverOperation
	t.Cleanup(func() { cutoverOperation = oldOperation })
	calls := 0
	cutoverOperation = func(context.Context, dcimigration.CutoverOptions) (dcimigration.ServiceCutoverReceipt, error) {
		calls++
		return dcimigration.ServiceCutoverReceipt{}, errors.New("must not execute")
	}
	for _, name := range required {
		t.Run("missing-"+name, func(t *testing.T) {
			args := cutoverCLIArgsForTest()
			for index := 0; index < len(args); index++ {
				if args[index] == "--"+name {
					args = append(args[:index], args[index+2:]...)
					break
				}
			}
			var stdout, stderr bytes.Buffer
			if code := run(args, &stdout, &stderr); code != 2 || stdout.Len() != 0 || stderr.String() != "cutover mode requires all cutover flags\n" {
				t.Fatalf("missing %s result: code=%d stdout=%q stderr=%q", name, code, stdout.String(), stderr.String())
			}
		})
		t.Run("empty-"+name, func(t *testing.T) {
			args := cutoverCLIArgsForTest()
			for index := 0; index < len(args); index++ {
				if args[index] == "--"+name {
					args[index+1] = ""
					break
				}
			}
			var stdout, stderr bytes.Buffer
			if code := run(args, &stdout, &stderr); code != 2 || stdout.Len() != 0 || stderr.String() != "cutover mode requires all cutover flags\n" {
				t.Fatalf("empty %s result: code=%d stdout=%q stderr=%q", name, code, stdout.String(), stderr.String())
			}
		})
	}
	if calls != 0 {
		t.Fatalf("cutover operation calls on form errors = %d", calls)
	}
}

func TestRunCutoverRejectsEveryIncompatibleFlag(t *testing.T) {
	incompatible := []string{
		"snapshot-dir", "manifest", "capture-receipt", "dry-run-manifest",
		"source-dci", "source-dci-jsonl", "source-event-store", "source-l1", "source-archive",
		"live-dci", "live-dci-jsonl", "live-event-store", "live-l1", "live-archive",
		"expected-searches", "expected-read-events", "expected-evidence-events", "expected-total-events",
		"expected-legacy-limit-steps", "expected-normalized-text-values", "expected-invalid-utf8-bytes",
	}
	oldOperation := cutoverOperation
	t.Cleanup(func() { cutoverOperation = oldOperation })
	calls := 0
	cutoverOperation = func(context.Context, dcimigration.CutoverOptions) (dcimigration.ServiceCutoverReceipt, error) {
		calls++
		return dcimigration.ServiceCutoverReceipt{}, errors.New("must not execute")
	}
	for _, name := range incompatible {
		t.Run(name, func(t *testing.T) {
			value := "secret/path/value"
			if strings.HasPrefix(name, "expected-") {
				value = "1"
			}
			args := append(cutoverCLIArgsForTest(), "--"+name, value)
			var stdout, stderr bytes.Buffer
			if code := run(args, &stdout, &stderr); code != 2 || stdout.Len() != 0 || stderr.String() != "cutover mode rejects incompatible flags\n" || strings.Contains(stdout.String(), value) || strings.Contains(stderr.String(), value) {
				t.Fatalf("incompatible %s result: code=%d stdout=%q stderr=%q", name, code, stdout.String(), stderr.String())
			}
		})
	}
	if calls != 0 {
		t.Fatalf("cutover operation calls on incompatible flags = %d", calls)
	}
}

func TestRunCutoverSemanticInvalidHashReturnsBlockedJSON(t *testing.T) {
	args := cutoverCLIArgsForTest()
	for index := range args {
		if args[index] == "--expected-build-receipt-sha256" {
			args[index+1] = strings.ToUpper(args[index+1])
			break
		}
	}
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	if code != 1 || strings.Count(stdout.String(), "\n") != 1 || stderr.String() != "invalid_options\n" {
		t.Fatalf("invalid hash result: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var receipt dcimigration.ServiceCutoverReceipt
	if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil || receipt.Status != dcimigration.CutoverStatusBlocked || receipt.ErrorCode != "invalid_options" || receipt.BuildReceiptSHA256 != "" {
		t.Fatalf("invalid hash receipt = %#v, err=%v", receipt, err)
	}
	for _, secret := range []string{"build-root-secret", "installed-runtime-secret", "active-config-secret"} {
		if strings.Contains(stdout.String(), secret) || strings.Contains(stderr.String(), secret) {
			t.Fatalf("invalid hash output leaked %q: stdout=%q stderr=%q", secret, stdout.String(), stderr.String())
		}
	}
}

func TestRunCutoverStatusExitBehaviorAndBoundedError(t *testing.T) {
	statuses := []struct {
		status string
		code   int
		err    error
	}{
		{status: dcimigration.CutoverStatusApplied, code: 0},
		{status: dcimigration.CutoverStatusBlocked, code: 1},
		{status: dcimigration.CutoverStatusRolledBack, code: 1},
		{status: dcimigration.CutoverStatusRollbackFailed, code: 1},
	}
	oldOperation := cutoverOperation
	t.Cleanup(func() { cutoverOperation = oldOperation })
	for _, test := range statuses {
		t.Run(test.status, func(t *testing.T) {
			now := time.Now().UTC()
			receipt := dcimigration.ServiceCutoverReceipt{
				SchemaVersion: dcimigration.ServiceCutoverSchemaVersion, Mode: dcimigration.ModeCutover,
				Status: test.status, StartedAt: now, CompletedAt: now,
			}
			if test.status != dcimigration.CutoverStatusApplied {
				receipt.ErrorCode = test.status
			}
			cutoverOperation = func(context.Context, dcimigration.CutoverOptions) (dcimigration.ServiceCutoverReceipt, error) {
				return receipt, test.err
			}
			var stdout, stderr bytes.Buffer
			if code := run(cutoverCLIArgsForTest(), &stdout, &stderr); code != test.code || strings.Count(stdout.String(), "\n") != 1 {
				t.Fatalf("status %s result: code=%d stdout=%q stderr=%q", test.status, code, stdout.String(), stderr.String())
			}
			if test.status == dcimigration.CutoverStatusApplied && stderr.Len() != 0 {
				t.Fatalf("applied stderr = %q", stderr.String())
			}
			if test.status != dcimigration.CutoverStatusApplied && stderr.String() != test.status+"\n" {
				t.Fatalf("%s stderr = %q", test.status, stderr.String())
			}
		})
	}

	secret := "private/path/secret-token"
	cutoverOperation = func(context.Context, dcimigration.CutoverOptions) (dcimigration.ServiceCutoverReceipt, error) {
		now := time.Now().UTC()
		return dcimigration.ServiceCutoverReceipt{
			SchemaVersion: dcimigration.ServiceCutoverSchemaVersion, Mode: dcimigration.ModeCutover,
			Status: dcimigration.CutoverStatusBlocked, StartedAt: now, CompletedAt: now, ErrorCode: "service_cutover",
		}, errors.New(secret)
	}
	var stdout, stderr bytes.Buffer
	if code := run(cutoverCLIArgsForTest(), &stdout, &stderr); code != 1 || stderr.String() != "service_cutover\n" || strings.Contains(stdout.String(), secret) || strings.Contains(stderr.String(), secret) {
		t.Fatalf("bounded cutover error: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func cutoverCLIArgsForTest() []string {
	return []string{
		"--mode", "cutover", "--build-dir", "build-root-secret", "--build-receipt", "build-receipt-secret",
		"--expected-build-receipt-sha256", strings.Repeat("a", 64), "--installed-runtime", "installed-runtime-secret",
		"--staged-runtime", "staged-runtime-secret", "--expected-installed-runtime-sha256", strings.Repeat("b", 64),
		"--expected-staged-runtime-sha256", strings.Repeat("c", 64), "--active-dci", "active-dci-secret",
		"--active-dci-jsonl", "active-jsonl-secret", "--active-event-store", "active-events-secret",
		"--active-l1", "active-l1-secret", "--active-archive", "active-archive-secret",
		"--active-config", "active-config-secret", "--rollback-dir", "rollback-secret",
		"--cutover-receipt", "cutover-receipt-secret", "--service-receipt", "service-receipt-secret",
	}
}

func TestRunBlockedDryRunEmitsPathFreeJSONReceiptAndExitOne(t *testing.T) {
	snapshot := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run(dryRunArgs(snapshot, 0, 0, 0, 0, 0), &stdout, &stderr)
	if code != 1 {
		t.Fatalf("blocked run exit code = %d, want 1", code)
	}
	var manifest dcimigration.Manifest
	if err := json.Unmarshal(stdout.Bytes(), &manifest); err != nil {
		t.Fatalf("blocked stdout is not JSON receipt: %v; output=%q", err, stdout.String())
	}
	if manifest.Status != dcimigration.StatusBlocked || manifest.ErrorCode == "" {
		t.Fatalf("blocked receipt = %#v", manifest)
	}
	if strings.Contains(stdout.String(), snapshot) || strings.Contains(stderr.String(), snapshot) {
		t.Fatalf("blocked CLI output leaked snapshot path: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if stderr.String() != manifest.ErrorCode+"\n" {
		t.Fatalf("blocked stderr = %q, want only error code", stderr.String())
	}
}

func TestRunReadyDryRunEmitsPathFreeJSONReceiptAndExitZero(t *testing.T) {
	snapshot := writeEmptyCLITestSnapshot(t)
	var stdout, stderr bytes.Buffer
	code := run(dryRunArgs(snapshot, 0, 0, 0, 0, 0), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("ready run exit code = %d, stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var manifest dcimigration.Manifest
	if err := json.Unmarshal(stdout.Bytes(), &manifest); err != nil {
		t.Fatalf("ready stdout is not JSON receipt: %v; output=%q", err, stdout.String())
	}
	if manifest.Status != dcimigration.StatusReady || manifest.ErrorCode != "" {
		t.Fatalf("ready receipt = %#v", manifest)
	}
	if stderr.Len() != 0 || strings.Contains(stdout.String(), snapshot) {
		t.Fatalf("ready CLI output leaked diagnostics/path: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunCaptureEmitsPathFreeJSONReceiptAndExitZero(t *testing.T) {
	live := writeEmptyCLITestSnapshot(t)
	captureRoot := filepath.Join(t.TempDir(), "capture-root")
	var stdout, stderr bytes.Buffer
	code := run(captureArgs(captureRoot, live), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("capture run exit code = %d, stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var receipt dcimigration.CaptureReceipt
	if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil {
		t.Fatalf("capture stdout is not JSON receipt: %v; output=%q", err, stdout.String())
	}
	if receipt.SchemaVersion != dcimigration.CaptureSchemaVersion || receipt.Mode != dcimigration.ModeCapture || receipt.Status != dcimigration.StatusReady || receipt.ErrorCode != "" {
		t.Fatalf("capture receipt = %#v", receipt)
	}
	if stderr.Len() != 0 || strings.Contains(stdout.String(), live) || strings.Contains(stdout.String(), captureRoot) {
		t.Fatalf("capture output leaked path or diagnostic: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(captureRoot, dcimigration.CaptureReceiptFilename)); err != nil {
		t.Fatalf("capture receipt file missing: %v", err)
	}
}

func TestRunCaptureBlockedEmitsPathFreeReceiptAndErrorCode(t *testing.T) {
	live := writeEmptyCLITestSnapshot(t)
	captureRoot := filepath.Join(t.TempDir(), "capture-root")
	missing := filepath.Join(t.TempDir(), "missing.db")
	args := captureArgs(captureRoot, live)
	args[len(args)-1] = missing
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("blocked capture exit code = %d, stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var receipt dcimigration.CaptureReceipt
	if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil {
		t.Fatalf("blocked capture stdout is not JSON receipt: %v; output=%q", err, stdout.String())
	}
	if receipt.Status != dcimigration.StatusBlocked || receipt.ErrorCode != "unsafe_path" {
		t.Fatalf("blocked capture receipt = %#v", receipt)
	}
	if stderr.String() != receipt.ErrorCode+"\n" || strings.Contains(stdout.String(), live) || strings.Contains(stdout.String(), captureRoot) || strings.Contains(stderr.String(), missing) {
		t.Fatalf("blocked capture output leaked path or diagnostic: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunBuildEmitsReadyReceiptAndCreatesOnlyFixedOutputs(t *testing.T) {
	live := writeEmptyCLITestSnapshot(t)
	if err := os.Remove(filepath.Join(live, "source-event-store")); err != nil {
		t.Fatal(err)
	}
	store, err := eventstore.NewSQLiteStore(filepath.Join(live, "source-event-store"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "captured")
	var captureOut, captureErr bytes.Buffer
	if code := run(captureArgs(snapshot, live), &captureOut, &captureErr); code != 0 {
		t.Fatalf("capture setup failed: code=%d stdout=%q stderr=%q", code, captureOut.String(), captureErr.String())
	}
	var dryRunOut, dryRunErr bytes.Buffer
	if code := run(dryRunArgs(snapshot, 0, 0, 0, 0, 0), &dryRunOut, &dryRunErr); code != 0 {
		t.Fatalf("dry-run setup failed: code=%d stdout=%q stderr=%q", code, dryRunOut.String(), dryRunErr.String())
	}
	buildRoot := filepath.Join(t.TempDir(), "build")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--mode", "build", "--snapshot-dir", snapshot, "--build-dir", buildRoot,
		"--capture-receipt", dcimigration.CaptureReceiptFilename, "--dry-run-manifest", "manifest.json",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("build run failed: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var receipt dcimigration.BuildReceipt
	if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil {
		t.Fatalf("build stdout is not JSON receipt: %v; output=%q", err, stdout.String())
	}
	if receipt.SchemaVersion != dcimigration.BuildSchemaVersion || receipt.Mode != dcimigration.ModeBuild || receipt.Status != dcimigration.StatusReady || receipt.ErrorCode != "" {
		t.Fatalf("build receipt = %#v", receipt)
	}
	if stderr.Len() != 0 || strings.Contains(stdout.String(), snapshot) || strings.Contains(stdout.String(), buildRoot) {
		t.Fatalf("build output leaked path/diagnostic: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	want := map[string]struct{}{
		dcimigration.BuildReceiptFilename: {}, "target-dci.db": {}, "target-event-store.db": {}, "target-l1.db": {}, "target-archive.db": {},
	}
	entries, err := os.ReadDir(buildRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(want) {
		t.Fatalf("build root entry count = %d, want %d", len(entries), len(want))
	}
	for _, entry := range entries {
		if _, ok := want[entry.Name()]; !ok {
			t.Fatalf("unexpected build output %q", entry.Name())
		}
	}
}

func TestRunBuildAndOtherModesRejectIncompatibleVisitedFlags(t *testing.T) {
	tests := []struct {
		name  string
		base  []string
		flag  string
		value string
		want  string
	}{
		{name: "build source dci", base: buildFlagIsolationArgs(), flag: "--source-dci", value: "build-source-dci-secret", want: "build mode rejects incompatible flags\n"},
		{name: "build source dci jsonl", base: buildFlagIsolationArgs(), flag: "--source-dci-jsonl", value: "build-source-jsonl-secret", want: "build mode rejects incompatible flags\n"},
		{name: "build source event store", base: buildFlagIsolationArgs(), flag: "--source-event-store", value: "build-source-events-secret", want: "build mode rejects incompatible flags\n"},
		{name: "build source l1", base: buildFlagIsolationArgs(), flag: "--source-l1", value: "build-source-l1-secret", want: "build mode rejects incompatible flags\n"},
		{name: "build source archive", base: buildFlagIsolationArgs(), flag: "--source-archive", value: "build-source-archive-secret", want: "build mode rejects incompatible flags\n"},
		{name: "build live dci", base: buildFlagIsolationArgs(), flag: "--live-dci", value: "build-live-dci-secret", want: "build mode rejects incompatible flags\n"},
		{name: "build live dci jsonl", base: buildFlagIsolationArgs(), flag: "--live-dci-jsonl", value: "build-live-jsonl-secret", want: "build mode rejects incompatible flags\n"},
		{name: "build live event store", base: buildFlagIsolationArgs(), flag: "--live-event-store", value: "build-live-events-secret", want: "build mode rejects incompatible flags\n"},
		{name: "build live l1", base: buildFlagIsolationArgs(), flag: "--live-l1", value: "build-live-l1-secret", want: "build mode rejects incompatible flags\n"},
		{name: "build live archive", base: buildFlagIsolationArgs(), flag: "--live-archive", value: "build-live-archive-secret", want: "build mode rejects incompatible flags\n"},
		{name: "build expected searches", base: buildFlagIsolationArgs(), flag: "--expected-searches", value: "1", want: "build mode rejects incompatible flags\n"},
		{name: "build expected reads", base: buildFlagIsolationArgs(), flag: "--expected-read-events", value: "1", want: "build mode rejects incompatible flags\n"},
		{name: "build expected evidence", base: buildFlagIsolationArgs(), flag: "--expected-evidence-events", value: "1", want: "build mode rejects incompatible flags\n"},
		{name: "build expected total", base: buildFlagIsolationArgs(), flag: "--expected-total-events", value: "1", want: "build mode rejects incompatible flags\n"},
		{name: "build expected limits", base: buildFlagIsolationArgs(), flag: "--expected-legacy-limit-steps", value: "1", want: "build mode rejects incompatible flags\n"},
		{name: "build expected normalized", base: buildFlagIsolationArgs(), flag: "--expected-normalized-text-values", value: "1", want: "build mode rejects incompatible flags\n"},
		{name: "build expected invalid bytes", base: buildFlagIsolationArgs(), flag: "--expected-invalid-utf8-bytes", value: "1", want: "build mode rejects incompatible flags\n"},
		{name: "build manifest", base: buildFlagIsolationArgs(), flag: "--manifest", value: "build-manifest-secret", want: "build mode rejects incompatible flags\n"},
		{name: "capture source dci", base: captureFlagIsolationArgs(), flag: "--source-dci", value: "capture-source-dci-secret", want: "capture mode rejects incompatible flags\n"},
		{name: "capture source dci jsonl", base: captureFlagIsolationArgs(), flag: "--source-dci-jsonl", value: "capture-source-jsonl-secret", want: "capture mode rejects incompatible flags\n"},
		{name: "capture source event store", base: captureFlagIsolationArgs(), flag: "--source-event-store", value: "capture-source-events-secret", want: "capture mode rejects incompatible flags\n"},
		{name: "capture source l1", base: captureFlagIsolationArgs(), flag: "--source-l1", value: "capture-source-l1-secret", want: "capture mode rejects incompatible flags\n"},
		{name: "capture source archive", base: captureFlagIsolationArgs(), flag: "--source-archive", value: "capture-source-archive-secret", want: "capture mode rejects incompatible flags\n"},
		{name: "capture expected searches", base: captureFlagIsolationArgs(), flag: "--expected-searches", value: "1", want: "capture mode rejects incompatible flags\n"},
		{name: "capture expected reads", base: captureFlagIsolationArgs(), flag: "--expected-read-events", value: "1", want: "capture mode rejects incompatible flags\n"},
		{name: "capture expected evidence", base: captureFlagIsolationArgs(), flag: "--expected-evidence-events", value: "1", want: "capture mode rejects incompatible flags\n"},
		{name: "capture expected total", base: captureFlagIsolationArgs(), flag: "--expected-total-events", value: "1", want: "capture mode rejects incompatible flags\n"},
		{name: "capture expected limits", base: captureFlagIsolationArgs(), flag: "--expected-legacy-limit-steps", value: "1", want: "capture mode rejects incompatible flags\n"},
		{name: "capture expected normalized", base: captureFlagIsolationArgs(), flag: "--expected-normalized-text-values", value: "1", want: "capture mode rejects incompatible flags\n"},
		{name: "capture expected invalid bytes", base: captureFlagIsolationArgs(), flag: "--expected-invalid-utf8-bytes", value: "1", want: "capture mode rejects incompatible flags\n"},
		{name: "capture build dir", base: captureFlagIsolationArgs(), flag: "--build-dir", value: "capture-build-secret", want: "capture mode rejects incompatible flags\n"},
		{name: "capture receipt", base: captureFlagIsolationArgs(), flag: "--capture-receipt", value: "capture-receipt-secret", want: "capture mode rejects incompatible flags\n"},
		{name: "capture dry-run manifest", base: captureFlagIsolationArgs(), flag: "--dry-run-manifest", value: "capture-manifest-secret", want: "capture mode rejects incompatible flags\n"},
		{name: "capture manifest", base: captureFlagIsolationArgs(), flag: "--manifest", value: "capture-manifest-secret", want: "capture mode rejects incompatible flags\n"},
		{name: "dry-run live dci", base: dryRunFlagIsolationArgs(), flag: "--live-dci", value: "dry-live-dci-secret", want: "dry-run mode rejects incompatible flags\n"},
		{name: "dry-run live dci jsonl", base: dryRunFlagIsolationArgs(), flag: "--live-dci-jsonl", value: "dry-live-jsonl-secret", want: "dry-run mode rejects incompatible flags\n"},
		{name: "dry-run live event store", base: dryRunFlagIsolationArgs(), flag: "--live-event-store", value: "dry-live-events-secret", want: "dry-run mode rejects incompatible flags\n"},
		{name: "dry-run live l1", base: dryRunFlagIsolationArgs(), flag: "--live-l1", value: "dry-live-l1-secret", want: "dry-run mode rejects incompatible flags\n"},
		{name: "dry-run live archive", base: dryRunFlagIsolationArgs(), flag: "--live-archive", value: "dry-live-archive-secret", want: "dry-run mode rejects incompatible flags\n"},
		{name: "dry-run build dir", base: dryRunFlagIsolationArgs(), flag: "--build-dir", value: "dry-build-secret", want: "dry-run mode rejects incompatible flags\n"},
		{name: "dry-run receipt", base: dryRunFlagIsolationArgs(), flag: "--capture-receipt", value: "dry-receipt-secret", want: "dry-run mode rejects incompatible flags\n"},
		{name: "dry-run manifest", base: dryRunFlagIsolationArgs(), flag: "--dry-run-manifest", value: "dry-manifest-secret", want: "dry-run mode rejects incompatible flags\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			args := append(append([]string(nil), tt.base...), tt.flag, tt.value)
			if code := run(args, &stdout, &stderr); code != 2 || stdout.Len() != 0 || stderr.String() != tt.want || strings.Contains(stdout.String(), tt.value) || strings.Contains(stderr.String(), tt.value) {
				t.Fatalf("result: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			for _, value := range args {
				if strings.HasPrefix(value, "--") || value == "build" || value == "capture" || value == "dry-run" {
					continue
				}
				if _, err := strconv.Atoi(value); err == nil {
					continue
				}
				if strings.Contains(stdout.String(), value) || strings.Contains(stderr.String(), value) {
					t.Fatalf("flag value leaked: %q stdout=%q stderr=%q", value, stdout.String(), stderr.String())
				}
			}
		})
	}
}

func buildFlagIsolationArgs() []string {
	return []string{"--mode", "build", "--snapshot-dir", "build-snapshot-secret", "--build-dir", "build-root-secret", "--capture-receipt", "build-capture-secret", "--dry-run-manifest", "build-dryrun-secret"}
}

func captureFlagIsolationArgs() []string {
	return []string{"--mode", "capture", "--snapshot-dir", "capture-snapshot-secret", "--live-dci", "capture-live-dci-secret", "--live-dci-jsonl", "capture-live-jsonl-secret", "--live-event-store", "capture-live-events-secret", "--live-l1", "capture-live-l1-secret", "--live-archive", "capture-live-archive-secret"}
}

func dryRunFlagIsolationArgs() []string {
	return []string{"--mode", "dry-run", "--snapshot-dir", "dry-snapshot-secret", "--expected-searches", "0", "--expected-read-events", "0", "--expected-evidence-events", "0", "--expected-total-events", "0", "--expected-legacy-limit-steps", "0", "--expected-normalized-text-values", "0", "--expected-invalid-utf8-bytes", "0"}
}

func TestRunBuildBlockedIsBoundedAndPathFree(t *testing.T) {
	snapshot := filepath.Join(t.TempDir(), "missing-snapshot")
	buildRoot := filepath.Join(t.TempDir(), "build")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--mode", "build", "--snapshot-dir", snapshot, "--build-dir", buildRoot,
		"--capture-receipt", "capture.json", "--dry-run-manifest", "dry-run.json",
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("blocked build exit code = %d, stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var receipt dcimigration.BuildReceipt
	if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil {
		t.Fatalf("blocked build stdout is not JSON: %v", err)
	}
	if receipt.Status != dcimigration.StatusBlocked || receipt.ErrorCode == "" || stderr.String() != receipt.ErrorCode+"\n" {
		t.Fatalf("blocked build result = receipt=%#v stderr=%q", receipt, stderr.String())
	}
	if strings.Contains(stdout.String(), snapshot) || strings.Contains(stdout.String(), buildRoot) || strings.Contains(stderr.String(), snapshot) || strings.Contains(stderr.String(), buildRoot) {
		t.Fatalf("blocked build leaked path: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if _, err := os.Lstat(buildRoot); !os.IsNotExist(err) {
		t.Fatalf("blocked prepare created build root: %v", err)
	}
}

func captureArgs(captureRoot, live string) []string {
	return []string{
		"--mode", "capture", "--snapshot-dir", captureRoot,
		"--live-dci", filepath.Join(live, "source-dci"),
		"--live-dci-jsonl", filepath.Join(live, "source-dci-jsonl"),
		"--live-event-store", filepath.Join(live, "source-event-store"),
		"--live-l1", filepath.Join(live, "source-l1"),
		"--live-archive", filepath.Join(live, "source-archive"),
	}
}

func TestRunCaptureRejectsSymlinkLiveSource(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not portable on Windows")
	}
	live := writeEmptyCLITestSnapshot(t)
	link := filepath.Join(t.TempDir(), "source-link")
	if err := os.Symlink(filepath.Join(live, "source-dci"), link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	args := captureArgs(filepath.Join(t.TempDir(), "capture-root"), live)
	args[5] = link
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("symlink capture exit code = %d, stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var receipt dcimigration.CaptureReceipt
	if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil {
		t.Fatalf("symlink capture stdout is not JSON receipt: %v; output=%q", err, stdout.String())
	}
	if receipt.Status != dcimigration.StatusBlocked || receipt.ErrorCode != "unsafe_path" || stderr.String() != "unsafe_path\n" {
		t.Fatalf("symlink capture result = receipt=%#v stderr=%q", receipt, stderr.String())
	}
}

func dryRunArgs(snapshot string, searches, reads, evidence, total, limits int) []string {
	return []string{
		"--mode", "dry-run", "--snapshot-dir", snapshot,
		"--expected-searches", formatInt(searches), "--expected-read-events", formatInt(reads),
		"--expected-evidence-events", formatInt(evidence), "--expected-total-events", formatInt(total),
		"--expected-legacy-limit-steps", formatInt(limits),
		"--expected-normalized-text-values", "0", "--expected-invalid-utf8-bytes", "0",
	}
}

func formatInt(value int) string {
	return strconv.Itoa(value)
}

func writeEmptyCLITestSnapshot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "source-dci-jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	writeCLITestDB(t, filepath.Join(root, "source-dci"), []string{
		`CREATE TABLE dci_search_trace (event_id TEXT PRIMARY KEY, started_at TEXT NOT NULL, ended_at TEXT, actor TEXT NOT NULL, mode TEXT NOT NULL, user_query TEXT, corpus_scope TEXT, status TEXT NOT NULL, final_evidence_count INTEGER DEFAULT 0, error_message TEXT)`,
		`CREATE TABLE dci_search_step (id INTEGER PRIMARY KEY AUTOINCREMENT, event_id TEXT NOT NULL, step_no INTEGER NOT NULL, tool TEXT NOT NULL, command_text TEXT, file_path TEXT, result_count INTEGER, status TEXT NOT NULL, error_message TEXT, created_at TEXT NOT NULL)`,
		`CREATE TABLE dci_evidence (evidence_id TEXT PRIMARY KEY, event_id TEXT NOT NULL, source_id TEXT, file_path TEXT NOT NULL, heading TEXT, line_start INTEGER, line_end INTEGER, snippet TEXT NOT NULL, reason TEXT, confidence REAL DEFAULT 0.0, created_at TEXT NOT NULL)`,
		`CREATE TABLE dci_query_terms (id INTEGER PRIMARY KEY AUTOINCREMENT, event_id TEXT NOT NULL, term TEXT NOT NULL, term_type TEXT, parent_term TEXT, created_at TEXT NOT NULL)`,
	})
	writeCLITestDB(t, filepath.Join(root, "source-event-store"), []string{
		`CREATE TABLE event_envelope (event_id TEXT PRIMARY KEY NOT NULL, trace_id TEXT NOT NULL, schema_version TEXT NOT NULL, event_type TEXT NOT NULL, component_id TEXT NOT NULL, occurred_at TEXT NOT NULL, envelope_json TEXT NOT NULL)`,
		`CREATE TABLE event_dependency (event_id TEXT NOT NULL, dependency_event_id TEXT NOT NULL, relation_type TEXT NOT NULL CHECK (relation_type IN ('causation','dependency')), PRIMARY KEY(event_id,dependency_event_id), FOREIGN KEY(event_id) REFERENCES event_envelope(event_id) ON UPDATE RESTRICT ON DELETE RESTRICT, FOREIGN KEY(dependency_event_id) REFERENCES event_envelope(event_id) ON UPDATE RESTRICT ON DELETE RESTRICT)`,
	})
	writeCLITestDB(t, filepath.Join(root, "source-l1"), []string{
		`CREATE TABLE l1_staging_item (id TEXT PRIMARY KEY, kind TEXT NOT NULL, namespace TEXT NOT NULL, event_id TEXT NOT NULL, source_id TEXT NOT NULL, source_url TEXT NOT NULL DEFAULT '', fetched_at TIMESTAMP NOT NULL, published_at TIMESTAMP, raw_text TEXT NOT NULL, raw_hash TEXT NOT NULL, summary_draft TEXT NOT NULL DEFAULT '', keywords_json TEXT NOT NULL DEFAULT '[]', license_note TEXT NOT NULL DEFAULT '', validation_status TEXT NOT NULL, meta_json TEXT NOT NULL DEFAULT '{}', created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL)`,
		`CREATE TABLE l1_source_registry (source_id TEXT PRIMARY KEY, url TEXT NOT NULL, kind TEXT NOT NULL, trust_score REAL NOT NULL, fetch_interval_sec INTEGER NOT NULL, license_note TEXT NOT NULL, enabled INTEGER NOT NULL, meta_json TEXT NOT NULL DEFAULT '{}', last_fetched_at TIMESTAMP, last_status TEXT NOT NULL DEFAULT '', last_error TEXT NOT NULL DEFAULT '', created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL)`,
	})
	writeCLITestDB(t, filepath.Join(root, "source-archive"), []string{
		`CREATE TABLE l1_staging_item_archive (id VARCHAR PRIMARY KEY, kind VARCHAR NOT NULL, namespace VARCHAR NOT NULL, event_id VARCHAR NOT NULL, source_id VARCHAR NOT NULL, source_url TEXT NOT NULL, fetched_at TIMESTAMP NOT NULL, published_at TIMESTAMP, raw_text TEXT NOT NULL, raw_hash VARCHAR NOT NULL, summary_draft TEXT NOT NULL, keywords_json TEXT NOT NULL, license_note TEXT NOT NULL, validation_status VARCHAR NOT NULL, meta_json TEXT NOT NULL, created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL)`,
	})
	return root
}

func writeCLITestDB(t *testing.T, path string, schema []string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range schema {
		if _, err := db.ExecContext(context.Background(), statement); err != nil {
			t.Fatal(err)
		}
	}
}
