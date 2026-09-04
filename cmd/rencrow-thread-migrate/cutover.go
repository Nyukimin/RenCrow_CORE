package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/threadmigration"
)

const (
	cutoverSchemaVersion        = "rencrow.threadmigration.cutover.v1"
	cutoverStatusApplied        = "applied_not_runtime_verified"
	cutoverStatusRolledBack     = "rolled_back"
	cutoverStatusRollbackFailed = "rollback_failed"
	cutoverStatusBlocked        = "blocked"
	cutoverReceiptFilename      = "cutover.json"
	cutoverReceiptMaxBytes      = 4 << 20
)

type cutoverOptions struct {
	BuildDir         string
	StageDir         string
	L1Target         string
	ArchiveTarget    string
	TopicTarget      string
	ConfigTarget     string
	RuntimeCandidate string
	RuntimeTarget    string
}

type cutoverReceipt struct {
	SchemaVersion             string `json:"schema_version"`
	Status                    string `json:"status"`
	BuildReceiptSHA256        string `json:"build_receipt_sha256"`
	StageReceiptSHA256        string `json:"stage_receipt_sha256"`
	MappingSHA256             string `json:"mapping_sha256"`
	L1OldSHA256               string `json:"l1_old_sha256"`
	L1NewSHA256               string `json:"l1_new_sha256"`
	ArchiveOldSHA256          string `json:"archive_old_sha256"`
	ArchiveNewSHA256          string `json:"archive_new_sha256"`
	TopicOldSHA256            string `json:"topic_old_sha256"`
	TopicNewSHA256            string `json:"topic_new_sha256"`
	ConfigOldSHA256           string `json:"config_old_sha256"`
	ConfigNewSHA256           string `json:"config_new_sha256"`
	RuntimeOldSHA256          string `json:"runtime_old_sha256"`
	RuntimeNewSHA256          string `json:"runtime_new_sha256"`
	RollbackArtifactsRetained bool   `json:"rollback_artifacts_retained"`
	ReceiptSHA256             string `json:"receipt_sha256"`
	ErrorCode                 string `json:"error_code"`
}

func (receipt cutoverReceipt) canonicalJSON() ([]byte, error) {
	copy := receipt
	copy.ReceiptSHA256 = ""
	return json.Marshal(copy)
}

func (receipt cutoverReceipt) computeSHA256() (string, error) {
	data, err := receipt.canonicalJSON()
	if err != nil {
		return "", err
	}
	return cutoverSHA256(data), nil
}

func (receipt cutoverReceipt) validate() error {
	if receipt.SchemaVersion != cutoverSchemaVersion || !validCutoverSHA256(receipt.ReceiptSHA256) {
		return errors.New("cutover receipt schema or self hash is invalid")
	}
	want, err := receipt.computeSHA256()
	if err != nil || want != receipt.ReceiptSHA256 {
		return errors.New("cutover receipt self hash does not match")
	}
	if receipt.Status == cutoverStatusApplied {
		if receipt.ErrorCode != "" || !receipt.RollbackArtifactsRetained {
			return errors.New("applied cutover receipt terminal state is invalid")
		}
		for _, value := range []string{
			receipt.BuildReceiptSHA256, receipt.StageReceiptSHA256, receipt.MappingSHA256,
			receipt.L1OldSHA256, receipt.L1NewSHA256, receipt.ArchiveOldSHA256, receipt.ArchiveNewSHA256,
			receipt.TopicOldSHA256, receipt.TopicNewSHA256, receipt.ConfigOldSHA256, receipt.ConfigNewSHA256,
			receipt.RuntimeOldSHA256, receipt.RuntimeNewSHA256,
		} {
			if !validCutoverSHA256(value) {
				return errors.New("applied cutover receipt hash is invalid")
			}
		}
		return nil
	}
	if receipt.Status != cutoverStatusBlocked && receipt.Status != cutoverStatusRolledBack && receipt.Status != cutoverStatusRollbackFailed {
		return errors.New("cutover receipt status is invalid")
	}
	if receipt.ErrorCode == "" || receipt.RollbackArtifactsRetained {
		return errors.New("failed cutover receipt terminal state is invalid")
	}
	return nil
}

func runCutover(args []string, stdout io.Writer, operation cutoverOperation) int {
	if stdout == nil {
		return 1
	}
	flags := flag.NewFlagSet("rencrow-thread-migrate cutover", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	buildDir := flags.String("build-dir", "", "validated offline build directory")
	stageDir := flags.String("stage-dir", "", "validated external stage directory")
	l1Target := flags.String("l1-target", "", "active L1 SQLite path")
	archiveTarget := flags.String("archive-target", "", "active archive SQLite path")
	topicTarget := flags.String("topic-target", "", "active IdleChat topic path")
	configTarget := flags.String("config-target", "", "active CORE config path")
	runtimeCandidate := flags.String("runtime-candidate", "", "staged CORE runtime binary")
	runtimeTarget := flags.String("runtime-target", "", "installed CORE runtime binary")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || operation == nil {
		return writeCutoverReceipt(stdout, newBlockedCutoverReceipt("invalid_arguments"), 1)
	}
	values := []string{*buildDir, *stageDir, *l1Target, *archiveTarget, *topicTarget, *configTarget, *runtimeCandidate, *runtimeTarget}
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return writeCutoverReceipt(stdout, newBlockedCutoverReceipt("invalid_arguments"), 1)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), externalOperationTimeout)
	defer cancel()
	receipt, err := operation(ctx, cutoverOptions{
		BuildDir: *buildDir, StageDir: *stageDir, L1Target: *l1Target, ArchiveTarget: *archiveTarget,
		TopicTarget: *topicTarget, ConfigTarget: *configTarget, RuntimeCandidate: *runtimeCandidate, RuntimeTarget: *runtimeTarget,
	})
	if err != nil || receipt.Status != cutoverStatusApplied || receipt.validate() != nil {
		if receipt.validate() != nil {
			receipt = newBlockedCutoverReceipt("cutover_failed")
		}
		return writeCutoverReceipt(stdout, receipt, 1)
	}
	return writeCutoverReceipt(stdout, receipt, 0)
}

func cutover(ctx context.Context, options cutoverOptions) (cutoverReceipt, error) {
	receipt := newBlockedCutoverReceipt("preflight")
	if ctx == nil || ctx.Err() != nil {
		return receipt, cutoverFailure("context")
	}
	artifacts, err := threadmigration.ReadOfflineBuildStrict(ctx, options.BuildDir)
	if err != nil {
		return receipt, cutoverFailure("build_read")
	}
	stageRoot, stageReceipt, err := readCutoverStageStrict(ctx, options.StageDir)
	if err != nil {
		return receipt, cutoverFailure("stage_read")
	}
	receipt.BuildReceiptSHA256 = artifacts.Receipt.ReceiptSHA256
	receipt.StageReceiptSHA256 = stageReceipt.ReceiptSHA256
	receipt.MappingSHA256 = artifacts.Plan.MappingSHA256
	if stageReceipt.BuildReceiptSHA256 != receipt.BuildReceiptSHA256 || stageReceipt.MappingSHA256 != receipt.MappingSHA256 || stageReceipt.ConfigCandidate == nil {
		return sealCutoverReceipt(receipt, cutoverStatusBlocked, "stage_binding"), cutoverFailure("stage_binding")
	}

	buildRoot, err := filepath.Abs(options.BuildDir)
	if err != nil {
		return sealCutoverReceipt(receipt, cutoverStatusBlocked, "build_path"), cutoverFailure("build_path")
	}
	configCandidate := filepath.Join(stageRoot, stageExternalConfigFilename)
	specs := []cutoverSwapSpec{
		{role: "l1", candidate: filepath.Join(buildRoot, threadmigration.OfflineBuildL1Filename), target: options.L1Target, oldHash: artifacts.Receipt.SourceL1SHA256, newHash: artifacts.Receipt.L1OutputSHA256, mode: 0o600, sqlite: true},
		{role: "archive", candidate: filepath.Join(buildRoot, threadmigration.OfflineBuildArchiveFilename), target: options.ArchiveTarget, oldHash: artifacts.Receipt.SourceArchiveSHA256, newHash: artifacts.Receipt.ArchiveOutputSHA256, mode: 0o600, sqlite: true},
		{role: "topic", candidate: filepath.Join(buildRoot, threadmigration.OfflineBuildTopicFilename), target: options.TopicTarget, oldHash: artifacts.Receipt.SourceTopicSHA256, newHash: artifacts.Receipt.TopicOutputSHA256, mode: 0o600},
		{role: "config", candidate: configCandidate, target: options.ConfigTarget, oldHash: stageReceipt.ConfigCandidate.SourceConfigSHA256, newHash: stageReceipt.ConfigCandidate.OutputConfigSHA256, mode: 0o600},
		{role: "runtime", candidate: options.RuntimeCandidate, target: options.RuntimeTarget, mode: 0o755, executable: true},
	}
	if err := prepareCutoverSwaps(ctx, specs, artifacts.Receipt.ReceiptSHA256); err != nil {
		return sealCutoverReceipt(receipt, cutoverStatusBlocked, "artifact_preflight"), cutoverFailure("artifact_preflight")
	}
	receipt.L1OldSHA256, receipt.L1NewSHA256 = specs[0].oldHash, specs[0].newHash
	receipt.ArchiveOldSHA256, receipt.ArchiveNewSHA256 = specs[1].oldHash, specs[1].newHash
	receipt.TopicOldSHA256, receipt.TopicNewSHA256 = specs[2].oldHash, specs[2].newHash
	receipt.ConfigOldSHA256, receipt.ConfigNewSHA256 = specs[3].oldHash, specs[3].newHash
	receipt.RuntimeOldSHA256, receipt.RuntimeNewSHA256 = specs[4].oldHash, specs[4].newHash
	if stageReceipt.ConfigCandidate.TargetRedisDB != stageReceipt.RedisStage.TargetDB || stageReceipt.ConfigCandidate.TargetCollectionSHA256 != stageReceipt.QdrantStage.TargetCollectionHash {
		return sealCutoverReceipt(receipt, cutoverStatusBlocked, "route_binding"), cutoverFailure("route_binding")
	}
	if _, err := os.Lstat(filepath.Join(stageRoot, cutoverReceiptFilename)); err == nil || !errors.Is(err, os.ErrNotExist) {
		return sealCutoverReceipt(receipt, cutoverStatusBlocked, "receipt_not_fresh"), cutoverFailure("receipt_not_fresh")
	}

	completed, applyErr := applyCutoverSwaps(specs)
	if applyErr != nil {
		rollbackErr := rollbackCutoverSwaps(completed)
		if rollbackErr != nil {
			return sealCutoverReceipt(receipt, cutoverStatusRollbackFailed, "swap_failed"), cutoverFailure("rollback_failed")
		}
		return sealCutoverReceipt(receipt, cutoverStatusRolledBack, "swap_failed"), cutoverFailure("rolled_back")
	}
	receipt.RollbackArtifactsRetained = true
	receipt = sealCutoverReceipt(receipt, cutoverStatusApplied, "")
	if err := receipt.validate(); err != nil {
		if rollbackCutoverSwaps(completed) != nil {
			return sealCutoverReceipt(receipt, cutoverStatusRollbackFailed, "receipt_invalid"), cutoverFailure("rollback_failed")
		}
		return sealCutoverReceipt(receipt, cutoverStatusRolledBack, "receipt_invalid"), cutoverFailure("rolled_back")
	}
	if err := writeCutoverReceiptFile(stageRoot, receipt); err != nil {
		if rollbackCutoverSwaps(completed) != nil {
			return sealCutoverReceipt(receipt, cutoverStatusRollbackFailed, "receipt_write"), cutoverFailure("rollback_failed")
		}
		return sealCutoverReceipt(receipt, cutoverStatusRolledBack, "receipt_write"), cutoverFailure("rolled_back")
	}
	return receipt, nil
}

type cutoverSwapSpec struct {
	role          string
	candidate     string
	target        string
	rollback      string
	oldHash       string
	newHash       string
	mode          os.FileMode
	executable    bool
	sqlite        bool
	candidateInfo os.FileInfo
	targetInfo    os.FileInfo
}

var cutoverRename = os.Rename

func prepareCutoverSwaps(ctx context.Context, specs []cutoverSwapSpec, buildHash string) error {
	if !validCutoverSHA256(buildHash) || len(specs) != 5 {
		return errors.New("invalid cutover spec")
	}
	seen := make(map[string]struct{}, len(specs)*3)
	seenFiles := make([]os.FileInfo, 0, len(specs)*2)
	for index := range specs {
		spec := &specs[index]
		candidate, candidateInfo, candidateHash, err := inspectCutoverFile(ctx, spec.candidate, spec.mode, spec.executable)
		if err != nil {
			return err
		}
		target, targetInfo, targetHash, err := inspectCutoverFile(ctx, spec.target, spec.mode, spec.executable)
		if err != nil || os.SameFile(candidateInfo, targetInfo) {
			return errors.New("cutover source identity is invalid")
		}
		for _, info := range seenFiles {
			if os.SameFile(info, candidateInfo) || os.SameFile(info, targetInfo) {
				return errors.New("cutover source files overlap")
			}
		}
		seenFiles = append(seenFiles, candidateInfo, targetInfo)
		spec.candidate, spec.target = candidate, target
		spec.candidateInfo, spec.targetInfo = candidateInfo, targetInfo
		if spec.newHash != "" && candidateHash != spec.newHash {
			return errors.New("cutover candidate hash mismatch")
		}
		if spec.oldHash != "" && targetHash != spec.oldHash {
			return errors.New("cutover target hash mismatch")
		}
		spec.newHash, spec.oldHash = candidateHash, targetHash
		spec.rollback = target + ".pre-threadid-" + buildHash[:12]
		if _, err := os.Lstat(spec.rollback); err == nil || !errors.Is(err, os.ErrNotExist) {
			return errors.New("cutover rollback artifact is not fresh")
		}
		for _, path := range []string{candidate, target, spec.rollback} {
			key := filepath.Clean(path)
			if runtime.GOOS == "windows" {
				key = strings.ToLower(key)
			}
			if _, ok := seen[key]; ok {
				return errors.New("cutover paths overlap")
			}
			seen[key] = struct{}{}
		}
		if spec.sqlite {
			for _, base := range []string{candidate, target} {
				for _, suffix := range []string{"-wal", "-shm", "-journal"} {
					if _, err := os.Lstat(base + suffix); err == nil || !errors.Is(err, os.ErrNotExist) {
						return errors.New("SQLite sidecar is present")
					}
				}
			}
		}
	}
	return nil
}

func applyCutoverSwaps(specs []cutoverSwapSpec) ([]cutoverSwapSpec, error) {
	completed := make([]cutoverSwapSpec, 0, len(specs))
	for _, spec := range specs {
		if err := cutoverRename(spec.target, spec.rollback); err != nil {
			return completed, err
		}
		if err := cutoverRename(spec.candidate, spec.target); err != nil {
			if restoreErr := cutoverRename(spec.rollback, spec.target); restoreErr != nil {
				completed = append(completed, spec)
			}
			return completed, err
		}
		newInfo, newErr := os.Lstat(spec.target)
		oldInfo, oldErr := os.Lstat(spec.rollback)
		if newErr != nil || oldErr != nil || !os.SameFile(newInfo, spec.candidateInfo) || !os.SameFile(oldInfo, spec.targetInfo) {
			completed = append(completed, spec)
			return completed, errors.New("cutover rename identity mismatch")
		}
		completed = append(completed, spec)
	}
	return completed, nil
}

func rollbackCutoverSwaps(completed []cutoverSwapSpec) error {
	for index := len(completed) - 1; index >= 0; index-- {
		spec := completed[index]
		if _, err := os.Lstat(spec.target); err != nil {
			return errors.New("cutover target missing during rollback")
		}
		if _, err := os.Lstat(spec.candidate); err == nil || !errors.Is(err, os.ErrNotExist) {
			return errors.New("cutover candidate occupied during rollback")
		}
		if err := cutoverRename(spec.target, spec.candidate); err != nil {
			return err
		}
		if err := cutoverRename(spec.rollback, spec.target); err != nil {
			return err
		}
		candidateInfo, candidateErr := os.Lstat(spec.candidate)
		targetInfo, targetErr := os.Lstat(spec.target)
		if candidateErr != nil || targetErr != nil || !os.SameFile(candidateInfo, spec.candidateInfo) || !os.SameFile(targetInfo, spec.targetInfo) {
			return errors.New("cutover rollback identity mismatch")
		}
	}
	return nil
}

func inspectCutoverFile(ctx context.Context, raw string, mode os.FileMode, executable bool) (string, os.FileInfo, string, error) {
	if ctx == nil || ctx.Err() != nil || raw == "" || strings.IndexByte(raw, 0) >= 0 {
		return "", nil, "", errors.New("invalid cutover file")
	}
	absolute, err := filepath.Abs(raw)
	if err != nil {
		return "", nil, "", err
	}
	absolute = filepath.Clean(absolute)
	before, err := os.Lstat(absolute)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() || before.Mode().Perm() != mode || (executable && before.Mode().Perm()&0o111 == 0) {
		return "", nil, "", errors.New("cutover file metadata is invalid")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil || !canonicalThreadConfigSamePath(absolute, resolved) {
		return "", nil, "", errors.New("cutover file path is not canonical")
	}
	file, err := os.Open(absolute)
	if err != nil {
		return "", nil, "", err
	}
	hash := sha256.New()
	buffer := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			_ = file.Close()
			return "", nil, "", err
		}
		read, readErr := file.Read(buffer)
		if read > 0 {
			_, _ = hash.Write(buffer[:read])
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			_ = file.Close()
			return "", nil, "", readErr
		}
	}
	if err := file.Close(); err != nil {
		return "", nil, "", err
	}
	after, err := os.Lstat(absolute)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || before.Mode() != after.Mode() {
		return "", nil, "", errors.New("cutover file changed during read")
	}
	return absolute, before, hex.EncodeToString(hash.Sum(nil)), nil
}

func readCutoverStageStrict(ctx context.Context, raw string) (string, stageExternalReceipt, error) {
	if ctx == nil || ctx.Err() != nil || raw == "" || strings.IndexByte(raw, 0) >= 0 {
		return "", stageExternalReceipt{}, errors.New("invalid stage directory")
	}
	root, err := filepath.Abs(raw)
	if err != nil {
		return "", stageExternalReceipt{}, err
	}
	root = filepath.Clean(root)
	info, err := os.Lstat(root)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || (runtime.GOOS != "windows" && (info.Mode().Perm()&0o077 != 0 || info.Mode().Perm()&0o700 != 0o700)) {
		return "", stageExternalReceipt{}, errors.New("stage directory is unsafe")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || !canonicalThreadConfigSamePath(root, resolved) {
		return "", stageExternalReceipt{}, errors.New("stage directory is not canonical")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 2 {
		return "", stageExternalReceipt{}, errors.New("stage output set is invalid")
	}
	names := []string{entries[0].Name(), entries[1].Name()}
	sort.Strings(names)
	want := []string{stageExternalConfigFilename, stageExternalReceiptFilename}
	sort.Strings(want)
	if names[0] != want[0] || names[1] != want[1] {
		return "", stageExternalReceipt{}, errors.New("stage output set is invalid")
	}
	for _, entry := range entries {
		entryInfo, err := entry.Info()
		if err != nil || entryInfo.Mode()&os.ModeSymlink != 0 || !entryInfo.Mode().IsRegular() || entryInfo.Mode().Perm() != 0o600 {
			return "", stageExternalReceipt{}, errors.New("stage output metadata is invalid")
		}
	}
	data, _, err := readCutoverBoundedFile(ctx, filepath.Join(root, stageExternalReceiptFilename), cutoverReceiptMaxBytes)
	if err != nil || scanCutoverJSON(data) != nil {
		return "", stageExternalReceipt{}, errors.New("stage receipt JSON is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var receipt stageExternalReceipt
	if err := decoder.Decode(&receipt); err != nil || receipt.validate() != nil {
		return "", stageExternalReceipt{}, errors.New("stage receipt is invalid")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return "", stageExternalReceipt{}, errors.New("stage receipt has trailing JSON")
	}
	canonical, err := json.Marshal(receipt)
	if err != nil || !bytes.Equal(data, append(canonical, '\n')) {
		return "", stageExternalReceipt{}, errors.New("stage receipt is noncanonical")
	}
	configData, _, err := readCutoverBoundedFile(ctx, filepath.Join(root, stageExternalConfigFilename), canonicalThreadConfigMaxBytes)
	if err != nil || receipt.ConfigCandidate == nil || cutoverSHA256(configData) != receipt.ConfigCandidate.OutputConfigSHA256 {
		return "", stageExternalReceipt{}, errors.New("stage config candidate mismatch")
	}
	return root, receipt, nil
}

func readCutoverBoundedFile(ctx context.Context, path string, limit int64) ([]byte, os.FileInfo, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, nil, errors.New("invalid context")
	}
	before, err := os.Lstat(path)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() || before.Mode().Perm() != 0o600 || before.Size() < 0 || before.Size() > limit {
		return nil, nil, errors.New("invalid bounded file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, limit+1))
	closeErr := file.Close()
	after, statErr := os.Lstat(path)
	if readErr != nil || closeErr != nil || statErr != nil || !os.SameFile(before, after) || before.Size() != after.Size() || int64(len(data)) != before.Size() || int64(len(data)) > limit {
		return nil, nil, errors.New("bounded file changed during read")
	}
	return data, before, nil
}

func scanCutoverJSON(data []byte) error {
	if len(data) == 0 || !utf8.Valid(data) || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return errors.New("invalid JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	first, err := decoder.Token()
	if err != nil {
		return err
	}
	if err := scanCutoverJSONValue(decoder, first, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func scanCutoverJSONValue(decoder *json.Decoder, token json.Token, depth int) error {
	if depth > 256 {
		return errors.New("JSON nesting is too deep")
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is invalid")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("duplicate JSON member")
			}
			seen[key] = struct{}{}
			child, err := decoder.Token()
			if err != nil || scanCutoverJSONValue(decoder, child, depth+1) != nil {
				return errors.New("invalid JSON member")
			}
		}
		closeToken, err := decoder.Token()
		if err != nil || closeToken != json.Delim('}') {
			return errors.New("JSON object is not closed")
		}
	case '[':
		for decoder.More() {
			child, err := decoder.Token()
			if err != nil || scanCutoverJSONValue(decoder, child, depth+1) != nil {
				return errors.New("invalid JSON array")
			}
		}
		closeToken, err := decoder.Token()
		if err != nil || closeToken != json.Delim(']') {
			return errors.New("JSON array is not closed")
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	return nil
}

func writeCutoverReceiptFile(stageRoot string, receipt cutoverReceipt) error {
	if err := receipt.validate(); err != nil {
		return err
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	file, err := os.OpenFile(filepath.Join(stageRoot, cutoverReceiptFilename), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	written, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil || written != len(data) {
		return errors.New("cutover receipt write failed")
	}
	receiptInfo, err := os.Lstat(filepath.Join(stageRoot, cutoverReceiptFilename))
	if err != nil || receiptInfo.Mode()&os.ModeSymlink != 0 || !receiptInfo.Mode().IsRegular() || receiptInfo.Mode().Perm() != 0o600 || receiptInfo.Size() != int64(len(data)) {
		return errors.New("cutover receipt metadata is invalid")
	}
	if runtime.GOOS != "windows" {
		directory, err := os.Open(stageRoot)
		if err != nil {
			return err
		}
		syncErr := directory.Sync()
		closeErr := directory.Close()
		if syncErr != nil || closeErr != nil {
			return errors.New("cutover receipt directory sync failed")
		}
	}
	return nil
}

func sealCutoverReceipt(receipt cutoverReceipt, status, code string) cutoverReceipt {
	receipt.Status = status
	receipt.ErrorCode = code
	if status != cutoverStatusApplied {
		receipt.RollbackArtifactsRetained = false
	}
	receipt.ReceiptSHA256 = ""
	receipt.ReceiptSHA256, _ = receipt.computeSHA256()
	return receipt
}

func newBlockedCutoverReceipt(code string) cutoverReceipt {
	return sealCutoverReceipt(cutoverReceipt{SchemaVersion: cutoverSchemaVersion}, cutoverStatusBlocked, code)
}

func writeCutoverReceipt(stdout io.Writer, receipt cutoverReceipt, code int) int {
	if receipt.validate() != nil {
		receipt = newBlockedCutoverReceipt("cutover_failed")
		code = 1
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		return 1
	}
	data = append(data, '\n')
	written, err := stdout.Write(data)
	if err != nil || written != len(data) {
		return 1
	}
	return code
}

type cutoverError string

func (err cutoverError) Error() string { return "ThreadID cutover blocked: " + string(err) }

func cutoverFailure(code string) error { return cutoverError(code) }

func cutoverSHA256(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func validCutoverSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
