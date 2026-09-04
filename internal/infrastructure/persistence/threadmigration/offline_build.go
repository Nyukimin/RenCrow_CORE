package threadmigration

// This file owns the offline Step 05 build boundary.  It consumes one
// explicitly supplied, already-captured cohort and writes only fresh sibling
// artifacts.  It never opens a runtime client, mutates a source, or claims
// that the result is ready for a running service.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

const (
	OfflineBuildSchemaVersion = "rencrow.threadmigration.offline_build.v1"
	OfflineBuildStatusReady   = "ready_offline_not_runtime_ready"
	OfflineBuildStatusBlocked = "blocked"

	OfflineBuildL1Filename              = "l1.sqlite"
	OfflineBuildArchiveFilename         = "archive.sqlite"
	OfflineBuildTopicFilename           = "idlechat_topics.jsonl"
	OfflineBuildTopicQuarantineFilename = "idlechat_topics.quarantine.jsonl"
	OfflineBuildRedisFilename           = "redis.json"
	OfflineBuildQdrantFilename          = "qdrant.json"
	OfflineBuildMappingFilename         = "mapping.json"
	OfflineBuildReceiptFilename         = "build.json"

	offlineBuildTempPattern = ".rencrow-threadmigration-build-*.tmp"
)

// BuildOptions is the explicit source/output contract for the offline
// ThreadID build.  Every path is supplied by the caller; no runtime default
// or production path is inferred.
type OfflineBuildOptions struct {
	L1SourcePath         string
	ArchiveSourcePath    string
	TopicSourcePath      string
	ExternalSnapshotPath string
	OutputDir            string
}

// BuildOptions is retained as the short public name used by the migration
// specification.  It is an alias, not a second contract.
type BuildOptions = OfflineBuildOptions

// OfflineBuildReceipt is a bounded, path-free evidence record for one build.
// The receipt self-hash excludes ReceiptSHA256 from CanonicalJSON.  Counts are
// intentionally aggregate and never expose row keys or source content.
type OfflineBuildReceipt struct {
	SchemaVersion string `json:"schema_version"`
	Status        string `json:"status"`
	ErrorCode     string `json:"error_code"`

	SourceL1SHA256               string `json:"source_l1_sha256"`
	SourceArchiveSHA256          string `json:"source_archive_sha256"`
	SourceTopicSHA256            string `json:"source_topic_sha256"`
	SourceExternalSnapshotSHA256 string `json:"source_external_snapshot_sha256"`
	SourceRedisSHA256            string `json:"source_redis_sha256"`
	SourceQdrantSHA256           string `json:"source_qdrant_sha256"`
	SourceL1Bytes                int64  `json:"source_l1_bytes"`
	SourceArchiveBytes           int64  `json:"source_archive_bytes"`
	SourceTopicBytes             int64  `json:"source_topic_bytes"`
	SourceExternalSnapshotBytes  int64  `json:"source_external_snapshot_bytes"`

	L1SourceCount        int64 `json:"l1_source_count"`
	L1OutputCount        int64 `json:"l1_output_count"`
	ArchiveSourceCount   int64 `json:"archive_source_count"`
	ArchiveOutputCount   int64 `json:"archive_output_count"`
	TopicSourceCount     int   `json:"topic_source_count"`
	TopicOutputCount     int   `json:"topic_output_count"`
	TopicQuarantineCount int   `json:"topic_quarantine_count"`
	RedisSourceCount     int   `json:"redis_source_count"`
	RedisOutputCount     int   `json:"redis_output_count"`
	QdrantSourceCount    int   `json:"qdrant_source_count"`
	QdrantOutputCount    int   `json:"qdrant_output_count"`
	MappingGenericCount  int   `json:"mapping_generic_count"`
	MappingChatGPTCount  int   `json:"mapping_chatgpt_count"`

	L1OutputSHA256        string `json:"l1_output_sha256"`
	ArchiveOutputSHA256   string `json:"archive_output_sha256"`
	TopicOutputSHA256     string `json:"topic_output_sha256"`
	TopicQuarantineSHA256 string `json:"topic_quarantine_sha256"`
	RedisOutputSHA256     string `json:"redis_output_sha256"`
	QdrantOutputSHA256    string `json:"qdrant_output_sha256"`
	MappingArtifactSHA256 string `json:"mapping_artifact_sha256"`
	L1OutputBytes         int64  `json:"l1_output_bytes"`
	ArchiveOutputBytes    int64  `json:"archive_output_bytes"`
	TopicOutputBytes      int64  `json:"topic_output_bytes"`
	TopicQuarantineBytes  int64  `json:"topic_quarantine_bytes"`
	RedisOutputBytes      int64  `json:"redis_output_bytes"`
	QdrantOutputBytes     int64  `json:"qdrant_output_bytes"`
	MappingArtifactBytes  int64  `json:"mapping_artifact_bytes"`

	MappingSHA256                   string `json:"mapping_sha256"`
	L1MappingSHA256                 string `json:"l1_mapping_sha256"`
	ArchiveMappingSHA256            string `json:"archive_mapping_sha256"`
	TopicMappingSHA256              string `json:"topic_mapping_sha256"`
	RedisMappingSHA256              string `json:"redis_mapping_sha256"`
	QdrantMappingSHA256             string `json:"qdrant_mapping_sha256"`
	SQLiteCloneL1ReceiptSHA256      string `json:"sqlite_clone_l1_receipt_sha256"`
	SQLiteCloneArchiveReceiptSHA256 string `json:"sqlite_clone_archive_receipt_sha256"`
	RedisPreparationReceiptSHA256   string `json:"redis_preparation_receipt_sha256"`
	QdrantPreparationReceiptSHA256  string `json:"qdrant_preparation_receipt_sha256"`
	QdrantVectorDimension           int    `json:"qdrant_vector_dimension"`
	FullCohortReceiptSHA256         string `json:"full_cohort_receipt_sha256"`
	OutputArtifactSetSHA256         string `json:"output_artifact_set_sha256"`
	SourceInputsStable              int    `json:"source_inputs_stable"`
	ReceiptSHA256                   string `json:"receipt_sha256"`
}

// BuildReceipt is the short public name used by the migration specification.
type BuildReceipt = OfflineBuildReceipt

// BuildStatusReady and BuildStatusBlocked are aliases for callers that use
// the concise status names from the surrounding migration packages.
const (
	BuildStatusReady   = OfflineBuildStatusReady
	BuildStatusBlocked = OfflineBuildStatusBlocked
	BuildSchemaVersion = OfflineBuildSchemaVersion
)

type offlineBuildPaths struct {
	l1Source       string
	archiveSource  string
	topicSource    string
	externalSource string
	outputDir      string
	l1Output       string
	archiveOutput  string
}

type offlineBuildSourceFingerprint struct {
	path  string
	info  os.FileInfo
	hash  string
	bytes int64
}

type offlineBuildOutputEvidence struct {
	Name  string `json:"name"`
	Hash  string `json:"hash"`
	Bytes int64  `json:"bytes"`
	Count int    `json:"count"`
}

type offlineBuildError struct {
	code  string
	phase string
	cause error
}

func (err *offlineBuildError) Error() string {
	if err == nil {
		return "<nil>"
	}
	return fmt.Sprintf("offline ThreadID build %s failed during %s", err.code, err.phase)
}

func (err *offlineBuildError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

// Build consumes one immutable source cohort and materializes the canonical
// offline outputs.  It uses CloneSQLite for both database destinations and
// PrepareFullCohort for the single shared mapping/transformation pass.
func Build(ctx context.Context, options BuildOptions) (BuildReceipt, error) {
	receipt := newOfflineBuildBlockedReceipt("build_blocked")
	if ctx == nil {
		return receipt, newOfflineBuildError("invalid_arguments", "preflight", errors.New("nil context"))
	}
	if err := ctx.Err(); err != nil {
		return receipt, err
	}

	paths, err := resolveOfflineBuildPaths(options)
	if err != nil {
		return finishOfflineBuildBlocked("", offlineBuildErrorCode(err, "invalid_arguments"), err)
	}

	sourcesBefore, err := fingerprintOfflineBuildSources(ctx, paths)
	if err != nil {
		return finishOfflineBuildBlocked(paths.outputDir, offlineBuildErrorCode(err, "source_read"), err)
	}

	snapshot, err := ReadExternalSnapshotStrict(paths.externalSource)
	if err != nil {
		return finishOfflineBuildBlocked(paths.outputDir, "external_snapshot", err)
	}
	if err := ctx.Err(); err != nil {
		return finishOfflineBuildBlocked(paths.outputDir, "context_canceled", err)
	}

	l1Clone, err := CloneSQLite(ctx, SQLiteCloneInput{SourcePath: paths.l1Source, DestinationPath: paths.l1Output})
	if err != nil {
		return finishOfflineBuildBlocked(paths.outputDir, "l1_clone", err)
	}
	archiveClone, err := CloneSQLite(ctx, SQLiteCloneInput{SourcePath: paths.archiveSource, DestinationPath: paths.archiveOutput})
	if err != nil {
		return finishOfflineBuildBlocked(paths.outputDir, "archive_clone", err)
	}

	l1Source, err := openSQLiteCloneReadOnly(ctx, paths.l1Source)
	if err != nil {
		return finishOfflineBuildBlocked(paths.outputDir, "l1_source_open", err)
	}
	archiveSource, err := openSQLiteCloneReadOnly(ctx, paths.archiveSource)
	if err != nil {
		_ = l1Source.Close()
		return finishOfflineBuildBlocked(paths.outputDir, "archive_source_open", err)
	}
	l1Destination, err := openOfflineBuildSQLiteDestination(ctx, paths.l1Output)
	if err != nil {
		_ = l1Source.Close()
		_ = archiveSource.Close()
		return finishOfflineBuildBlocked(paths.outputDir, "l1_destination_open", err)
	}
	archiveDestination, err := openOfflineBuildSQLiteDestination(ctx, paths.archiveOutput)
	if err != nil {
		_ = l1Source.Close()
		_ = archiveSource.Close()
		_ = l1Destination.Close()
		return finishOfflineBuildBlocked(paths.outputDir, "archive_destination_open", err)
	}
	topicFile, err := os.Open(paths.topicSource)
	if err != nil {
		_ = l1Source.Close()
		_ = archiveSource.Close()
		_ = l1Destination.Close()
		_ = archiveDestination.Close()
		return finishOfflineBuildBlocked(paths.outputDir, "topic_source_open", err)
	}

	cohort, cohortErr := PrepareFullCohort(ctx, FullCohortInput{
		SQLiteTopic: SQLiteTopicCohortInput{
			L1Source: l1Source, ArchiveSource: archiveSource, RawSource: l1Source,
			L1Destination: l1Destination, ArchiveDestination: archiveDestination,
			TopicSource: topicFile,
		},
		Redis:  snapshot.Redis,
		Qdrant: snapshot.Qdrant,
	})
	closeErr := errors.Join(topicFile.Close(), l1Source.Close(), archiveSource.Close(), l1Destination.Close(), archiveDestination.Close())
	if cohortErr != nil {
		if closeErr != nil {
			cohortErr = errors.Join(cohortErr, closeErr)
		}
		return finishOfflineBuildBlocked(paths.outputDir, "cohort_prepare", cohortErr)
	}
	if closeErr != nil {
		return finishOfflineBuildBlocked(paths.outputDir, "cohort_close", closeErr)
	}
	if err := ctx.Err(); err != nil {
		return finishOfflineBuildBlocked(paths.outputDir, "context_canceled", err)
	}

	if err := finalizeSQLiteClone(ctx, paths.l1Output); err != nil {
		return finishOfflineBuildBlocked(paths.outputDir, "l1_output_finalize", err)
	}
	if err := finalizeSQLiteClone(ctx, paths.archiveOutput); err != nil {
		return finishOfflineBuildBlocked(paths.outputDir, "archive_output_finalize", err)
	}

	jsonOutputs, err := offlineBuildJSONOutputs(cohort)
	if err != nil {
		return finishOfflineBuildBlocked(paths.outputDir, "output_encode", err)
	}
	if err := writeOfflineBuildArtifactFresh(paths.outputDir, OfflineBuildTopicFilename, jsonOutputs.topic); err != nil {
		return finishOfflineBuildBlocked(paths.outputDir, "topic_output_write", err)
	}
	if err := writeOfflineBuildArtifactFresh(paths.outputDir, OfflineBuildTopicQuarantineFilename, jsonOutputs.quarantine); err != nil {
		return finishOfflineBuildBlocked(paths.outputDir, "topic_quarantine_write", err)
	}
	if err := writeOfflineBuildArtifactFresh(paths.outputDir, OfflineBuildRedisFilename, jsonOutputs.redis); err != nil {
		return finishOfflineBuildBlocked(paths.outputDir, "redis_output_write", err)
	}
	if err := writeOfflineBuildArtifactFresh(paths.outputDir, OfflineBuildQdrantFilename, jsonOutputs.qdrant); err != nil {
		return finishOfflineBuildBlocked(paths.outputDir, "qdrant_output_write", err)
	}
	if err := writeOfflineBuildArtifactFresh(paths.outputDir, OfflineBuildMappingFilename, jsonOutputs.mapping); err != nil {
		return finishOfflineBuildBlocked(paths.outputDir, "mapping_output_write", err)
	}

	sourcesAfter, err := fingerprintOfflineBuildSources(ctx, paths)
	if err != nil {
		return finishOfflineBuildBlocked(paths.outputDir, "source_read_after", err)
	}
	if err := verifyOfflineBuildSourcesStable(sourcesBefore, sourcesAfter); err != nil {
		return finishOfflineBuildBlocked(paths.outputDir, "source_changed", err)
	}

	evidence, err := measureOfflineBuildOutputs(ctx, paths, cohort)
	if err != nil {
		return finishOfflineBuildBlocked(paths.outputDir, "output_verify", err)
	}
	receipt = offlineBuildReceiptFromCohort(receipt, sourcesBefore, snapshot, cohort, l1Clone, archiveClone, evidence)
	if err := validateOfflineBuildReceiptAgainstCohort(receipt, cohort); err != nil {
		return finishOfflineBuildBlocked(paths.outputDir, "receipt_binding", err)
	}
	if err := receipt.Validate(); err != nil {
		return finishOfflineBuildBlocked(paths.outputDir, "receipt_validation", err)
	}
	if err := verifyOfflineBuildOutputSet(paths.outputDir, false); err != nil {
		return finishOfflineBuildBlocked(paths.outputDir, "output_set", err)
	}
	if err := writeOfflineBuildReceiptFresh(paths.outputDir, receipt); err != nil {
		return finishOfflineBuildBlocked(paths.outputDir, "receipt_write", err)
	}
	if err := verifyOfflineBuildReceiptFile(paths.outputDir, receipt); err != nil {
		return finishOfflineBuildBlocked(paths.outputDir, "receipt_verify", err)
	}
	if err := verifyOfflineBuildOutputSet(paths.outputDir, true); err != nil {
		return finishOfflineBuildBlocked(paths.outputDir, "final_output_set", err)
	}
	return receipt, nil
}

func newOfflineBuildError(code, phase string, cause error) error {
	if code == "" {
		code = "build_blocked"
	}
	if phase == "" {
		phase = "operation"
	}
	return &offlineBuildError{code: code, phase: phase, cause: cause}
}

func offlineBuildErrorCode(err error, fallback string) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "context_canceled"
	}
	var typed *offlineBuildError
	if errors.As(err, &typed) && typed.code != "" {
		return typed.code
	}
	if fallback == "" {
		return "build_blocked"
	}
	return fallback
}

func resolveOfflineBuildPaths(options BuildOptions) (offlineBuildPaths, error) {
	if strings.TrimSpace(options.L1SourcePath) == "" || strings.TrimSpace(options.ArchiveSourcePath) == "" ||
		strings.TrimSpace(options.TopicSourcePath) == "" || strings.TrimSpace(options.ExternalSnapshotPath) == "" ||
		strings.TrimSpace(options.OutputDir) == "" {
		return offlineBuildPaths{}, newOfflineBuildError("invalid_arguments", "preflight", errors.New("all build paths are required"))
	}
	l1, err := resolveOfflineBuildSourceFile(options.L1SourcePath)
	if err != nil {
		return offlineBuildPaths{}, newOfflineBuildError("unsafe_path", "source", err)
	}
	archive, err := resolveOfflineBuildSourceFile(options.ArchiveSourcePath)
	if err != nil {
		return offlineBuildPaths{}, newOfflineBuildError("unsafe_path", "source", err)
	}
	topic, err := resolveOfflineBuildSourceFile(options.TopicSourcePath)
	if err != nil {
		return offlineBuildPaths{}, newOfflineBuildError("unsafe_path", "topic_source", err)
	}
	external, err := resolveOfflineBuildSourceFile(options.ExternalSnapshotPath)
	if err != nil {
		return offlineBuildPaths{}, newOfflineBuildError("unsafe_path", "external_source", err)
	}
	if err := rejectOfflineBuildDuplicateSources(l1, archive, topic, external); err != nil {
		return offlineBuildPaths{}, newOfflineBuildError("duplicate_source", "source", err)
	}
	output, err := resolveOfflineBuildOutputDir(options.OutputDir)
	if err != nil {
		return offlineBuildPaths{}, newOfflineBuildError("unsafe_path", "output_directory", err)
	}
	return offlineBuildPaths{
		l1Source: l1, archiveSource: archive, topicSource: topic, externalSource: external,
		outputDir: output, l1Output: filepath.Join(output, OfflineBuildL1Filename),
		archiveOutput: filepath.Join(output, OfflineBuildArchiveFilename),
	}, nil
}

func rejectOfflineBuildDuplicateSources(paths ...string) error {
	roles := []string{"l1", "archive", "topic", "external"}
	infos := make([]os.FileInfo, len(paths))
	for index, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		infos[index] = info
	}
	for left := 0; left < len(infos); left++ {
		for right := left + 1; right < len(infos); right++ {
			if os.SameFile(infos[left], infos[right]) {
				return fmt.Errorf("%s and %s sources resolve to the same file", roles[left], roles[right])
			}
		}
	}
	return nil
}

func resolveOfflineBuildSourceFile(raw string) (string, error) {
	if strings.IndexByte(raw, 0) >= 0 {
		return "", errors.New("source path contains NUL")
	}
	absolute, err := filepath.Abs(raw)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	info, err := os.Lstat(absolute)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("source must be an existing regular non-symlink file")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil || !offlineBuildSamePath(absolute, filepath.Clean(resolved)) {
		return "", errors.New("source path must be canonical")
	}
	return absolute, nil
}

func resolveOfflineBuildOutputDir(raw string) (string, error) {
	if strings.IndexByte(raw, 0) >= 0 {
		return "", errors.New("output path contains NUL")
	}
	absolute, err := filepath.Abs(raw)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	info, err := os.Lstat(absolute)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("output directory must be an existing regular directory")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil || !offlineBuildSamePath(absolute, filepath.Clean(resolved)) {
		return "", errors.New("output directory must be canonical")
	}
	if runtime.GOOS != "windows" && (info.Mode().Perm()&0o077 != 0 || info.Mode().Perm()&0o700 != 0o700) {
		return "", errors.New("output directory must be owner-only")
	}
	entries, err := os.ReadDir(absolute)
	if err != nil {
		return "", err
	}
	if len(entries) != 0 {
		return "", errors.New("output directory must be empty")
	}
	return absolute, nil
}

func offlineBuildSamePath(left, right string) bool {
	left, right = filepath.Clean(left), filepath.Clean(right)
	if left == right {
		return true
	}
	return filepath.VolumeName(left) != "" && strings.EqualFold(left, right)
}

func safeOfflineBuildOutputDir(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !offlineBuildSamePath(path, resolved) {
		return false
	}
	return runtime.GOOS == "windows" || (info.Mode().Perm()&0o077 == 0 && info.Mode().Perm()&0o700 == 0o700)
}

func fingerprintOfflineBuildSources(ctx context.Context, paths offlineBuildPaths) (map[string]offlineBuildSourceFingerprint, error) {
	result := make(map[string]offlineBuildSourceFingerprint, 4)
	for role, path := range map[string]string{
		"l1": paths.l1Source, "archive": paths.archiveSource,
		"topic": paths.topicSource, "external": paths.externalSource,
	} {
		fingerprint, err := fingerprintOfflineBuildFile(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("fingerprint %s source: %w", role, err)
		}
		result[role] = fingerprint
	}
	return result, nil
}

func fingerprintOfflineBuildFile(ctx context.Context, path string) (offlineBuildSourceFingerprint, error) {
	if ctx == nil {
		return offlineBuildSourceFingerprint{}, errors.New("nil context")
	}
	if err := ctx.Err(); err != nil {
		return offlineBuildSourceFingerprint{}, err
	}
	before, err := os.Lstat(path)
	if err != nil || !externalSnapshotRegularNonSymlink(before) {
		return offlineBuildSourceFingerprint{}, errors.New("source is not a regular non-symlink file")
	}
	file, err := os.Open(path)
	if err != nil {
		return offlineBuildSourceFingerprint{}, err
	}
	hash := sha256.New()
	buffer := make([]byte, 64*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			_ = file.Close()
			return offlineBuildSourceFingerprint{}, err
		}
		read, readErr := file.Read(buffer)
		if read > 0 {
			if _, err := hash.Write(buffer[:read]); err != nil {
				_ = file.Close()
				return offlineBuildSourceFingerprint{}, err
			}
			total += int64(read)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			_ = file.Close()
			return offlineBuildSourceFingerprint{}, readErr
		}
	}
	if err := file.Close(); err != nil {
		return offlineBuildSourceFingerprint{}, err
	}
	after, err := os.Lstat(path)
	if err != nil || !externalSnapshotRegularNonSymlink(after) || after.Size() != before.Size() || !os.SameFile(before, after) {
		return offlineBuildSourceFingerprint{}, errors.New("source changed during read")
	}
	return offlineBuildSourceFingerprint{path: path, info: before, hash: hex.EncodeToString(hash.Sum(nil)), bytes: total}, nil
}

func verifyOfflineBuildSourcesStable(before, after map[string]offlineBuildSourceFingerprint) error {
	if len(before) != len(after) {
		return errors.New("source fingerprint set changed")
	}
	for role, initial := range before {
		current, ok := after[role]
		if !ok || initial.hash != current.hash || initial.bytes != current.bytes || !os.SameFile(initial.info, current.info) {
			return fmt.Errorf("source %s changed", role)
		}
	}
	return nil
}

func openOfflineBuildSQLiteDestination(ctx context.Context, path string) (*sql.DB, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", sqliteCloneDSN(path, "_pragma=busy_timeout%3d5000"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

type offlineBuildJSONOutput struct {
	topic      []byte
	quarantine []byte
	redis      []byte
	qdrant     []byte
	mapping    []byte
}

func offlineBuildJSONOutputs(cohort FullCohortResult) (offlineBuildJSONOutput, error) {
	redis, err := canonicalRedisEntriesJSON(cohort.RedisPreparation.Entries)
	if err != nil {
		return offlineBuildJSONOutput{}, err
	}
	qdrant, err := qdrantCanonicalPointsJSON(cohort.QdrantPreparation.Points)
	if err != nil {
		return offlineBuildJSONOutput{}, err
	}
	mapping, err := offlineBuildMappingJSON(cohort.Plan)
	if err != nil {
		return offlineBuildJSONOutput{}, err
	}
	return offlineBuildJSONOutput{
		topic:      append([]byte(nil), cohort.SQLiteTopic.Topic.OutputJSONL...),
		quarantine: append([]byte(nil), cohort.SQLiteTopic.Topic.QuarantineJSONL...),
		redis:      redis, qdrant: qdrant, mapping: mapping,
	}, nil
}

func offlineBuildMappingJSON(plan Plan) ([]byte, error) {
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Generic       []ThreadMapping `json:"generic"`
		ChatGPT       []ThreadMapping `json:"chatgpt"`
		MappingSHA256 string          `json:"mapping_sha256"`
	}{
		Generic: canonicalMappings(plan.Generic), ChatGPT: canonicalMappings(plan.ChatGPT), MappingSHA256: plan.MappingSHA256,
	})
}

func writeOfflineBuildArtifactFresh(root, name string, data []byte) error {
	if !safeOfflineBuildOutputDir(root) || name == "" || filepath.Base(name) != name {
		return errors.New("unsafe output artifact path")
	}
	target := filepath.Join(root, name)
	if _, err := os.Lstat(target); err == nil || !errors.Is(err, os.ErrNotExist) {
		return errors.New("output artifact is not fresh")
	}
	temporary, err := os.CreateTemp(root, offlineBuildTempPattern)
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = temporary.Close()
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	written, err := temporary.Write(data)
	if err != nil || written != len(data) {
		return errors.New("write output artifact")
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	info, err := os.Lstat(temporaryName)
	if err != nil || !externalSnapshotRegularNonSymlink(info) || info.Mode().Perm() != 0o600 {
		return errors.New("temporary output artifact is unsafe")
	}
	if _, err := os.Lstat(target); err == nil || !errors.Is(err, os.ErrNotExist) {
		return errors.New("output artifact is not fresh")
	}
	if err := os.Link(temporaryName, target); err != nil {
		return err
	}
	if err := os.Remove(temporaryName); err != nil {
		return err
	}
	cleanup = false
	info, err = os.Lstat(target)
	if err != nil || !externalSnapshotRegularNonSymlink(info) || info.Mode().Perm() != 0o600 {
		return errors.New("published output artifact is unsafe")
	}
	return syncExternalSnapshotDirectory(root)
}

func writeOfflineBuildReceiptFresh(root string, receipt OfflineBuildReceipt) error {
	if err := receipt.Validate(); err != nil {
		return err
	}
	serialized, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	return writeOfflineBuildArtifactFresh(root, OfflineBuildReceiptFilename, append(serialized, '\n'))
}

func verifyOfflineBuildReceiptFile(root string, want OfflineBuildReceipt) error {
	path := filepath.Join(root, OfflineBuildReceiptFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	serialized, err := json.Marshal(want)
	if err != nil {
		return err
	}
	if string(data) != string(append(serialized, '\n')) {
		return errors.New("build receipt bytes do not match full receipt")
	}
	return nil
}

func cleanupOfflineBuildOutputs(root string) {
	if !safeOfflineBuildOutputDir(root) {
		return
	}
	for _, name := range []string{
		OfflineBuildL1Filename, OfflineBuildArchiveFilename, OfflineBuildTopicFilename,
		OfflineBuildTopicQuarantineFilename, OfflineBuildRedisFilename,
		OfflineBuildQdrantFilename, OfflineBuildMappingFilename, OfflineBuildReceiptFilename,
	} {
		path := filepath.Join(root, name)
		_ = os.Remove(path)
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			_ = os.Remove(path + suffix)
		}
	}
}

func finishOfflineBuildBlocked(root, code string, cause error) (OfflineBuildReceipt, error) {
	receipt := newOfflineBuildBlockedReceipt(code)
	if root != "" && safeOfflineBuildOutputDir(root) {
		cleanupOfflineBuildOutputs(root)
		if err := writeOfflineBuildReceiptFresh(root, receipt); err != nil {
			cleanupOfflineBuildOutputs(root)
			receipt = newOfflineBuildBlockedReceipt("receipt_write")
			return receipt, newOfflineBuildError("receipt_write", "receipt", err)
		}
		if err := verifyOfflineBuildReceiptFile(root, receipt); err != nil {
			cleanupOfflineBuildOutputs(root)
			receipt = newOfflineBuildBlockedReceipt("receipt_write")
			return receipt, newOfflineBuildError("receipt_write", "receipt", err)
		}
	}
	if cause == nil {
		cause = errors.New("offline build blocked")
	}
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return receipt, cause
	}
	return receipt, newOfflineBuildError(code, "operation", cause)
}

func newOfflineBuildBlockedReceipt(code string) OfflineBuildReceipt {
	if code == "" {
		code = "build_blocked"
	}
	receipt := OfflineBuildReceipt{SchemaVersion: OfflineBuildSchemaVersion, Status: OfflineBuildStatusBlocked, ErrorCode: code}
	receipt.ReceiptSHA256, _ = receipt.ComputeSHA256()
	return receipt
}

func offlineBuildReceiptFromCohort(base OfflineBuildReceipt, sources map[string]offlineBuildSourceFingerprint, snapshot ExternalSnapshot, cohort FullCohortResult, l1Clone SQLiteCloneReceipt, archiveClone SQLiteCloneReceipt, evidence []offlineBuildOutputEvidence) OfflineBuildReceipt {
	receipt := base
	receipt.Status = OfflineBuildStatusReady
	receipt.ErrorCode = ""
	receipt.SourceL1SHA256, receipt.SourceL1Bytes = sources["l1"].hash, sources["l1"].bytes
	receipt.SourceArchiveSHA256, receipt.SourceArchiveBytes = sources["archive"].hash, sources["archive"].bytes
	receipt.SourceTopicSHA256, receipt.SourceTopicBytes = sources["topic"].hash, sources["topic"].bytes
	receipt.SourceExternalSnapshotSHA256, receipt.SourceExternalSnapshotBytes = sources["external"].hash, sources["external"].bytes
	receipt.SourceRedisSHA256, receipt.SourceQdrantSHA256 = snapshot.RedisSHA256, snapshot.QdrantSHA256
	receipt.L1SourceCount = sqliteInventoryRows(cohort.SQLiteTopic.SQLiteInventory.Receipt.SurfaceCounts, map[string]struct{}{
		l1MemoryEventSurface: {}, l1EventLogSurface: {}, l1ProfilePromotionSurface: {}, activeThreadSurface: {}, turnReceiptSurface: {}, turnOutboxSurface: {},
	})
	receipt.ArchiveSourceCount = sqliteInventoryRows(cohort.SQLiteTopic.SQLiteInventory.Receipt.SurfaceCounts, map[string]struct{}{
		sessionThreadSurface: {}, threadSummaryReceiptSurface: {}, l1MemoryEventArchiveSurface: {},
	})
	receipt.L1OutputCount = sqliteMaterializationRows(cohort.SQLiteTopic.L1Receipt.TableCounts)
	receipt.ArchiveOutputCount = archiveMaterializationRows(cohort.SQLiteTopic.ArchiveReceipt.TableCounts)
	receipt.TopicSourceCount, receipt.TopicOutputCount, receipt.TopicQuarantineCount = cohort.SQLiteTopic.Topic.SourceCount, cohort.SQLiteTopic.Topic.OutputCount, cohort.SQLiteTopic.Topic.QuarantineCount
	receipt.RedisSourceCount, receipt.RedisOutputCount = cohort.RedisPreparation.Receipt.SourceCount, cohort.RedisPreparation.Receipt.OutputCount
	receipt.QdrantSourceCount, receipt.QdrantOutputCount = cohort.QdrantPreparation.Receipt.SourceCount, cohort.QdrantPreparation.Receipt.OutputCount
	receipt.MappingGenericCount, receipt.MappingChatGPTCount = len(cohort.Plan.Generic), len(cohort.Plan.ChatGPT)
	receipt.MappingSHA256 = cohort.Plan.MappingSHA256
	receipt.L1MappingSHA256, receipt.ArchiveMappingSHA256 = cohort.SQLiteTopic.L1Receipt.MappingSHA256, cohort.SQLiteTopic.ArchiveReceipt.MappingSHA256
	receipt.TopicMappingSHA256, receipt.RedisMappingSHA256 = cohort.SQLiteTopic.Receipt.MergedMappingSHA256, cohort.RedisPreparation.Receipt.MappingSHA256
	receipt.QdrantMappingSHA256 = cohort.QdrantPreparation.Receipt.MappingSHA256
	receipt.SQLiteCloneL1ReceiptSHA256 = l1Clone.ReceiptSHA256
	receipt.SQLiteCloneArchiveReceiptSHA256 = archiveClone.ReceiptSHA256
	receipt.RedisPreparationReceiptSHA256 = cohort.RedisPreparation.Receipt.ReceiptSHA256
	receipt.QdrantPreparationReceiptSHA256 = cohort.QdrantPreparation.Receipt.ReceiptSHA256
	receipt.QdrantVectorDimension = cohort.QdrantPreparation.Receipt.VectorDimension
	receipt.FullCohortReceiptSHA256 = cohort.Receipt.ReceiptSHA256
	receipt.SourceInputsStable = 1
	for _, artifact := range evidence {
		switch artifact.Name {
		case OfflineBuildL1Filename:
			receipt.L1OutputSHA256, receipt.L1OutputBytes = artifact.Hash, artifact.Bytes
		case OfflineBuildArchiveFilename:
			receipt.ArchiveOutputSHA256, receipt.ArchiveOutputBytes = artifact.Hash, artifact.Bytes
		case OfflineBuildTopicFilename:
			receipt.TopicOutputSHA256, receipt.TopicOutputBytes = artifact.Hash, artifact.Bytes
		case OfflineBuildTopicQuarantineFilename:
			receipt.TopicQuarantineSHA256, receipt.TopicQuarantineBytes = artifact.Hash, artifact.Bytes
		case OfflineBuildRedisFilename:
			receipt.RedisOutputSHA256, receipt.RedisOutputBytes = artifact.Hash, artifact.Bytes
		case OfflineBuildQdrantFilename:
			receipt.QdrantOutputSHA256, receipt.QdrantOutputBytes = artifact.Hash, artifact.Bytes
		case OfflineBuildMappingFilename:
			receipt.MappingArtifactSHA256, receipt.MappingArtifactBytes = artifact.Hash, artifact.Bytes
		}
	}
	receipt.OutputArtifactSetSHA256, _ = offlineBuildArtifactSetSHA256(evidence)
	receipt.ReceiptSHA256, _ = receipt.ComputeSHA256()
	return receipt
}

func validateOfflineBuildReceiptAgainstCohort(receipt OfflineBuildReceipt, cohort FullCohortResult) error {
	if receipt.RedisPreparationReceiptSHA256 != cohort.RedisPreparation.Receipt.ReceiptSHA256 {
		return errors.New("offline build Redis preparation receipt binding mismatch")
	}
	if receipt.QdrantPreparationReceiptSHA256 != cohort.QdrantPreparation.Receipt.ReceiptSHA256 {
		return errors.New("offline build Qdrant preparation receipt binding mismatch")
	}
	if receipt.QdrantVectorDimension != cohort.QdrantPreparation.Receipt.VectorDimension {
		return errors.New("offline build Qdrant vector dimension binding mismatch")
	}
	return nil
}

func sqliteInventoryRows(counts []SQLiteInventorySurfaceCount, surfaces map[string]struct{}) int64 {
	var total int64
	for _, count := range counts {
		if _, ok := surfaces[count.Surface]; ok {
			total += count.Rows
		}
	}
	return total
}

func sqliteMaterializationRows(counts []SQLiteL1MaterializationTableCount) int64 {
	var total int64
	for _, count := range counts {
		total += count.Rows
	}
	return total
}

func archiveMaterializationRows(counts []SQLiteArchiveMaterializationTableCount) int64 {
	var total int64
	for _, count := range counts {
		total += count.Rows
	}
	return total
}

func measureOfflineBuildOutputs(ctx context.Context, paths offlineBuildPaths, cohort FullCohortResult) ([]offlineBuildOutputEvidence, error) {
	counts := map[string]int{
		OfflineBuildTopicFilename:           cohort.SQLiteTopic.Topic.OutputCount,
		OfflineBuildTopicQuarantineFilename: cohort.SQLiteTopic.Topic.QuarantineCount,
		OfflineBuildRedisFilename:           cohort.RedisPreparation.Receipt.OutputCount,
		OfflineBuildQdrantFilename:          cohort.QdrantPreparation.Receipt.OutputCount,
		OfflineBuildMappingFilename:         len(cohort.Plan.Generic) + len(cohort.Plan.ChatGPT),
		OfflineBuildL1Filename:              int(sqliteMaterializationRows(cohort.SQLiteTopic.L1Receipt.TableCounts)),
		OfflineBuildArchiveFilename:         int(archiveMaterializationRows(cohort.SQLiteTopic.ArchiveReceipt.TableCounts)),
	}
	pathsByName := map[string]string{
		OfflineBuildL1Filename: paths.l1Output, OfflineBuildArchiveFilename: paths.archiveOutput,
		OfflineBuildTopicFilename:           filepath.Join(paths.outputDir, OfflineBuildTopicFilename),
		OfflineBuildTopicQuarantineFilename: filepath.Join(paths.outputDir, OfflineBuildTopicQuarantineFilename),
		OfflineBuildRedisFilename:           filepath.Join(paths.outputDir, OfflineBuildRedisFilename),
		OfflineBuildQdrantFilename:          filepath.Join(paths.outputDir, OfflineBuildQdrantFilename),
		OfflineBuildMappingFilename:         filepath.Join(paths.outputDir, OfflineBuildMappingFilename),
	}
	names := make([]string, 0, len(pathsByName))
	for name := range pathsByName {
		names = append(names, name)
	}
	sort.Strings(names)
	evidence := make([]offlineBuildOutputEvidence, 0, len(names))
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path := pathsByName[name]
		info, err := os.Lstat(path)
		if err != nil || !externalSnapshotRegularNonSymlink(info) || info.Mode().Perm() != 0o600 {
			return nil, errors.New("output artifact metadata is invalid")
		}
		allowEmpty := name == OfflineBuildTopicFilename || name == OfflineBuildTopicQuarantineFilename
		hash, bytes, err := hashOfflineBuildFile(ctx, path, allowEmpty)
		if err != nil {
			return nil, err
		}
		evidence = append(evidence, offlineBuildOutputEvidence{Name: name, Hash: hash, Bytes: bytes, Count: counts[name]})
	}
	if err := verifyOfflineBuildOutputSet(paths.outputDir, false); err != nil {
		return nil, err
	}
	return evidence, nil
}

func hashOfflineBuildFile(ctx context.Context, path string, allowEmpty bool) (string, int64, error) {
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	hash := sha256.New()
	buffer := make([]byte, 64*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			_ = file.Close()
			return "", total, err
		}
		read, readErr := file.Read(buffer)
		if read > 0 {
			if _, err := hash.Write(buffer[:read]); err != nil {
				_ = file.Close()
				return "", total, err
			}
			total += int64(read)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			_ = file.Close()
			return "", total, readErr
		}
	}
	if err := file.Close(); err != nil {
		return "", total, err
	}
	if !allowEmpty && total == 0 {
		return "", 0, errors.New("output artifact is empty")
	}
	return hex.EncodeToString(hash.Sum(nil)), total, nil
}

func offlineBuildArtifactSetSHA256(evidence []offlineBuildOutputEvidence) (string, error) {
	ordered := append([]offlineBuildOutputEvidence(nil), evidence...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].Name < ordered[right].Name })
	encoded, err := json.Marshal(ordered)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func verifyOfflineBuildOutputSet(root string, includeReceipt bool) error {
	if !safeOfflineBuildOutputDir(root) {
		return errors.New("output directory is unsafe")
	}
	want := []string{
		OfflineBuildL1Filename, OfflineBuildArchiveFilename, OfflineBuildTopicFilename,
		OfflineBuildTopicQuarantineFilename, OfflineBuildRedisFilename,
		OfflineBuildQdrantFilename, OfflineBuildMappingFilename,
	}
	if includeReceipt {
		want = append(want, OfflineBuildReceiptFilename)
	}
	sort.Strings(want)
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != len(want) {
		return errors.New("output directory entry set is invalid")
	}
	for index, entry := range entries {
		if entry.Name() != want[index] {
			return errors.New("output directory contains an unexpected artifact")
		}
		info, err := entry.Info()
		if err != nil || !externalSnapshotRegularNonSymlink(info) || info.Mode().Perm() != 0o600 {
			return errors.New("output artifact metadata is invalid")
		}
	}
	return nil
}

func (receipt OfflineBuildReceipt) CanonicalJSON() ([]byte, error) {
	copy := receipt
	copy.ReceiptSHA256 = ""
	return json.Marshal(copy)
}

func (receipt OfflineBuildReceipt) ComputeSHA256() (string, error) {
	encoded, err := receipt.CanonicalJSON()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func (receipt OfflineBuildReceipt) Validate() error {
	if receipt.SchemaVersion != OfflineBuildSchemaVersion {
		return errors.New("offline build receipt schema is invalid")
	}
	if receipt.Status != OfflineBuildStatusReady && receipt.Status != OfflineBuildStatusBlocked {
		return errors.New("offline build receipt status is invalid")
	}
	if !validOfflineBuildSHA256(receipt.ReceiptSHA256) {
		return errors.New("offline build receipt SHA256 is invalid")
	}
	computed, err := receipt.ComputeSHA256()
	if err != nil || computed != receipt.ReceiptSHA256 {
		return errors.New("offline build receipt SHA256 does not match canonical JSON")
	}
	if receipt.Status == OfflineBuildStatusBlocked {
		if receipt.ErrorCode == "" {
			return errors.New("blocked offline build receipt has no error code")
		}
		return nil
	}
	if receipt.ErrorCode != "" || receipt.SourceInputsStable != 1 {
		return errors.New("ready offline build receipt has invalid failure or stability state")
	}
	hashes := map[string]string{
		"source l1": receipt.SourceL1SHA256, "source archive": receipt.SourceArchiveSHA256,
		"source topic": receipt.SourceTopicSHA256, "source external": receipt.SourceExternalSnapshotSHA256,
		"source redis": receipt.SourceRedisSHA256, "source qdrant": receipt.SourceQdrantSHA256,
		"l1 output": receipt.L1OutputSHA256, "archive output": receipt.ArchiveOutputSHA256,
		"topic output": receipt.TopicOutputSHA256, "topic quarantine": receipt.TopicQuarantineSHA256,
		"redis output": receipt.RedisOutputSHA256, "qdrant output": receipt.QdrantOutputSHA256,
		"mapping artifact": receipt.MappingArtifactSHA256, "mapping": receipt.MappingSHA256,
		"l1 mapping": receipt.L1MappingSHA256, "archive mapping": receipt.ArchiveMappingSHA256,
		"topic mapping": receipt.TopicMappingSHA256, "redis mapping": receipt.RedisMappingSHA256,
		"qdrant mapping": receipt.QdrantMappingSHA256, "full cohort": receipt.FullCohortReceiptSHA256,
		"output set": receipt.OutputArtifactSetSHA256, "l1 clone receipt": receipt.SQLiteCloneL1ReceiptSHA256,
		"archive clone receipt":      receipt.SQLiteCloneArchiveReceiptSHA256,
		"Redis preparation receipt":  receipt.RedisPreparationReceiptSHA256,
		"Qdrant preparation receipt": receipt.QdrantPreparationReceiptSHA256,
	}
	for label, hash := range hashes {
		if !validOfflineBuildSHA256(hash) {
			return fmt.Errorf("%s SHA256 is invalid", label)
		}
	}
	if receipt.L1MappingSHA256 != receipt.MappingSHA256 || receipt.ArchiveMappingSHA256 != receipt.MappingSHA256 || receipt.TopicMappingSHA256 != receipt.MappingSHA256 || receipt.RedisMappingSHA256 != receipt.MappingSHA256 || receipt.QdrantMappingSHA256 != receipt.MappingSHA256 {
		return errors.New("offline build mapping hashes are not shared")
	}
	if receipt.QdrantVectorDimension < 0 {
		return errors.New("offline build Qdrant vector dimension is invalid")
	}
	if receipt.SourceL1Bytes < 0 || receipt.SourceArchiveBytes < 0 || receipt.SourceTopicBytes < 0 || receipt.SourceExternalSnapshotBytes < 0 || receipt.L1OutputBytes <= 0 || receipt.ArchiveOutputBytes <= 0 || receipt.TopicOutputBytes < 0 || receipt.TopicQuarantineBytes < 0 || receipt.RedisOutputBytes < 0 || receipt.QdrantOutputBytes < 0 || receipt.MappingArtifactBytes <= 0 {
		return errors.New("offline build byte counts are invalid")
	}
	counts := []int64{receipt.L1SourceCount, receipt.L1OutputCount, receipt.ArchiveSourceCount, receipt.ArchiveOutputCount}
	for _, count := range counts {
		if count < 0 {
			return errors.New("offline build SQLite count is invalid")
		}
	}
	intCounts := []int{receipt.TopicSourceCount, receipt.TopicOutputCount, receipt.TopicQuarantineCount, receipt.RedisSourceCount, receipt.RedisOutputCount, receipt.QdrantSourceCount, receipt.QdrantOutputCount, receipt.MappingGenericCount, receipt.MappingChatGPTCount}
	for _, count := range intCounts {
		if count < 0 {
			return errors.New("offline build count is invalid")
		}
	}
	if receipt.TopicOutputCount+receipt.TopicQuarantineCount != receipt.TopicSourceCount || receipt.RedisSourceCount != receipt.RedisOutputCount {
		return errors.New("offline build output counts do not reconcile")
	}
	if receipt.QdrantOutputCount > 0 && receipt.QdrantVectorDimension == 0 {
		return errors.New("offline build Qdrant vector dimension is missing")
	}
	return nil
}

func validOfflineBuildSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
