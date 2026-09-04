// rencrow-thread-migrate exposes bounded ThreadID migration capture,
// external snapshot verification, and offline output building. Quiescence is
// proved by the stopped-writer service boundary, not by this standalone
// command; an offline build never activates runtime state.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/threadmigration"
)

const (
	captureExternalCommand = "capture-external"
	captureExternalSchema  = "rencrow.threadmigration.capture_external.v1"
	verifyExternalCommand  = "verify-external"
	verifyExternalSchema   = "rencrow.threadmigration.verify_external.v1"
	buildCommand           = "build"
	buildSchema            = threadmigration.OfflineBuildSchemaVersion
	stageExternalCommand   = "stage-external"
	cutoverCommand         = "cutover"
	rollbackCutoverCommand = "rollback-cutover"
	quiesceSQLiteCommand   = "quiesce-sqlite"

	captureExternalStatusCapturedNotQuiescenceBound = "captured_not_quiescence_bound"
	captureExternalStatusBlocked                    = "blocked"
	verifyExternalStatusVerified                    = "verified"

	captureExternalErrorInvalidArguments = "invalid_arguments"
	captureExternalErrorCaptureFailed    = "capture_failed"
	verifyExternalErrorInvalidArguments  = "invalid_arguments"
	verifyExternalErrorVerifyFailed      = "verify_failed"
	buildErrorInvalidArguments           = "invalid_arguments"
	buildErrorBuildFailed                = "build_failed"

	externalOperationTimeout = 5 * time.Minute
)

// externalSnapshotOperation is the seam for the CORE-owned capture adapter.
type externalSnapshotOperation func(context.Context, string, string) (threadmigration.ExternalSnapshot, error)
type verifyExternalSnapshotOperation func(context.Context, string) (threadmigration.ExternalSnapshot, error)
type offlineBuildOperation func(context.Context, threadmigration.BuildOptions) (threadmigration.BuildReceipt, error)
type stageExternalOperation func(context.Context, stageExternalOptions) (stageExternalReceipt, error)
type cutoverOperation func(context.Context, cutoverOptions) (cutoverReceipt, error)
type rollbackCutoverOperation func(context.Context, rollbackCutoverOptions) (rollbackCutoverReceipt, error)
type quiesceSQLiteOperation func(context.Context, string) (quiesceSQLiteReceipt, error)

type externalOperations struct {
	capture  externalSnapshotOperation
	verify   verifyExternalSnapshotOperation
	build    offlineBuildOperation
	stage    stageExternalOperation
	cutover  cutoverOperation
	rollback rollbackCutoverOperation
	quiesce  quiesceSQLiteOperation
}

type externalSnapshotReceipt struct {
	Schema         string `json:"schema"`
	Status         string `json:"status"`
	RedisCount     int    `json:"redis_count"`
	QdrantCount    int    `json:"qdrant_count"`
	RedisSHA256    string `json:"redis_sha256"`
	QdrantSHA256   string `json:"qdrant_sha256"`
	SnapshotSHA256 string `json:"snapshot_sha256"`
	ErrorCode      string `json:"error_code"`
}

func main() {
	os.Exit(runWithOperations(os.Args[1:], os.Stdout, externalOperations{
		capture:  captureExternalSnapshot,
		verify:   verifyExternalSnapshot,
		build:    threadmigration.Build,
		stage:    stageExternal,
		cutover:  cutover,
		rollback: rollbackCutover,
		quiesce:  quiesceSQLite,
	}))
}

func runWithOperations(args []string, stdout io.Writer, ops externalOperations) int {
	if stdout == nil {
		return 1
	}

	if len(args) == 0 {
		return writeExternalReceipt(stdout, blockedExternalReceipt(captureExternalSchema, captureExternalErrorInvalidArguments), 1)
	}
	switch args[0] {
	case captureExternalCommand:
		return runCaptureExternal(args[1:], stdout, ops.capture)
	case verifyExternalCommand:
		return runVerifyExternal(args[1:], stdout, ops.verify)
	case buildCommand:
		return runBuild(args[1:], stdout, ops.build)
	case stageExternalCommand:
		return runStageExternal(args[1:], stdout, ops.stage)
	case cutoverCommand:
		return runCutover(args[1:], stdout, ops.cutover)
	case rollbackCutoverCommand:
		return runRollbackCutover(args[1:], stdout, ops.rollback)
	case quiesceSQLiteCommand:
		return runQuiesceSQLite(args[1:], stdout, ops.quiesce)
	default:
		return writeExternalReceipt(stdout, blockedExternalReceipt(captureExternalSchema, captureExternalErrorInvalidArguments), 1)
	}
}

func runBuild(args []string, stdout io.Writer, op offlineBuildOperation) int {
	flags := flag.NewFlagSet("rencrow-thread-migrate build", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	l1Source := flags.String("l1-source", "", "legacy L1 SQLite source path")
	archiveSource := flags.String("archive-source", "", "legacy Archive SQLite source path")
	topicSource := flags.String("topic-source", "", "IdleChat topic source path")
	externalSnapshot := flags.String("external-snapshot", "", "external snapshot path")
	outputDir := flags.String("output-dir", "", "fresh offline output directory")
	if err := flags.Parse(args); err != nil {
		return writeBuildReceipt(stdout, blockedBuildReceipt(buildErrorInvalidArguments), 1)
	}
	if flags.NArg() != 0 || strings.TrimSpace(*l1Source) == "" || strings.TrimSpace(*archiveSource) == "" || strings.TrimSpace(*topicSource) == "" || strings.TrimSpace(*externalSnapshot) == "" || strings.TrimSpace(*outputDir) == "" {
		return writeBuildReceipt(stdout, blockedBuildReceipt(buildErrorInvalidArguments), 1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), externalOperationTimeout)
	defer cancel()
	if op == nil {
		return writeBuildReceipt(stdout, blockedBuildReceipt(buildErrorBuildFailed), 1)
	}
	receipt, err := op(ctx, threadmigration.BuildOptions{
		L1SourcePath:         *l1Source,
		ArchiveSourcePath:    *archiveSource,
		TopicSourcePath:      *topicSource,
		ExternalSnapshotPath: *externalSnapshot,
		OutputDir:            *outputDir,
	})
	if err != nil || receipt.Status != threadmigration.OfflineBuildStatusReady || receipt.Validate() != nil {
		return writeBuildReceipt(stdout, blockedBuildReceipt(buildErrorBuildFailed), 1)
	}
	return writeBuildReceipt(stdout, receipt, 0)
}

func blockedBuildReceipt(errorCode string) threadmigration.BuildReceipt {
	if errorCode == "" {
		errorCode = buildErrorBuildFailed
	}
	receipt := threadmigration.BuildReceipt{
		SchemaVersion: buildSchema,
		Status:        threadmigration.OfflineBuildStatusBlocked,
		ErrorCode:     errorCode,
	}
	receipt.ReceiptSHA256, _ = receipt.ComputeSHA256()
	return receipt
}

func writeBuildReceipt(stdout io.Writer, receipt threadmigration.BuildReceipt, code int) int {
	if err := receipt.Validate(); err != nil {
		receipt = blockedBuildReceipt(buildErrorBuildFailed)
		code = 1
	}
	serialized, err := json.Marshal(receipt)
	if err != nil {
		return 1
	}
	serialized = append(serialized, '\n')
	written, err := stdout.Write(serialized)
	if err != nil || written != len(serialized) {
		return 1
	}
	return code
}

func runCaptureExternal(args []string, stdout io.Writer, op externalSnapshotOperation) int {
	flags := flag.NewFlagSet("rencrow-thread-migrate capture-external", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	config := flags.String("config", "", "CORE configuration path")
	output := flags.String("output", "", "external snapshot output path")
	if err := flags.Parse(args); err != nil {
		return writeExternalReceipt(stdout, blockedExternalReceipt(captureExternalSchema, captureExternalErrorInvalidArguments), 1)
	}
	if flags.NArg() != 0 || strings.TrimSpace(*config) == "" || strings.TrimSpace(*output) == "" {
		return writeExternalReceipt(stdout, blockedExternalReceipt(captureExternalSchema, captureExternalErrorInvalidArguments), 1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), externalOperationTimeout)
	defer cancel()
	if op == nil {
		return writeExternalReceipt(stdout, blockedExternalReceipt(captureExternalSchema, captureExternalErrorCaptureFailed), 1)
	}
	snapshot, err := op(ctx, *config, *output)
	if err != nil {
		return writeExternalReceipt(stdout, blockedExternalReceipt(captureExternalSchema, captureExternalErrorCaptureFailed), 1)
	}
	if err := snapshot.Validate(); err != nil {
		return writeExternalReceipt(stdout, blockedExternalReceipt(captureExternalSchema, captureExternalErrorCaptureFailed), 1)
	}

	receipt := successfulExternalReceipt(captureExternalSchema, captureExternalStatusCapturedNotQuiescenceBound, snapshot)
	return writeExternalReceipt(stdout, receipt, 0)
}

func runVerifyExternal(args []string, stdout io.Writer, op verifyExternalSnapshotOperation) int {
	flags := flag.NewFlagSet("rencrow-thread-migrate verify-external", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	input := flags.String("input", "", "external snapshot input path")
	if err := flags.Parse(args); err != nil {
		return writeExternalReceipt(stdout, blockedExternalReceipt(verifyExternalSchema, verifyExternalErrorInvalidArguments), 1)
	}
	if flags.NArg() != 0 || strings.TrimSpace(*input) == "" {
		return writeExternalReceipt(stdout, blockedExternalReceipt(verifyExternalSchema, verifyExternalErrorInvalidArguments), 1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), externalOperationTimeout)
	defer cancel()
	if op == nil {
		return writeExternalReceipt(stdout, blockedExternalReceipt(verifyExternalSchema, verifyExternalErrorVerifyFailed), 1)
	}
	snapshot, err := op(ctx, *input)
	if err != nil || snapshot.Validate() != nil {
		return writeExternalReceipt(stdout, blockedExternalReceipt(verifyExternalSchema, verifyExternalErrorVerifyFailed), 1)
	}
	receipt := successfulExternalReceipt(verifyExternalSchema, verifyExternalStatusVerified, snapshot)
	return writeExternalReceipt(stdout, receipt, 0)
}

func successfulExternalReceipt(schema, status string, snapshot threadmigration.ExternalSnapshot) externalSnapshotReceipt {
	return externalSnapshotReceipt{
		Schema:         schema,
		Status:         status,
		RedisCount:     len(snapshot.Redis),
		QdrantCount:    len(snapshot.Qdrant),
		RedisSHA256:    snapshot.RedisSHA256,
		QdrantSHA256:   snapshot.QdrantSHA256,
		SnapshotSHA256: snapshot.SnapshotSHA256,
	}
}

func blockedExternalReceipt(schema, errorCode string) externalSnapshotReceipt {
	return externalSnapshotReceipt{
		Schema:    schema,
		Status:    captureExternalStatusBlocked,
		ErrorCode: errorCode,
	}
}

func writeExternalReceipt(stdout io.Writer, receipt externalSnapshotReceipt, code int) int {
	if err := json.NewEncoder(stdout).Encode(receipt); err != nil {
		return 1
	}
	return code
}
