package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/threadmigration"
)

const (
	rollbackCutoverSchemaVersion   = "rencrow.threadmigration.rollback_cutover.v1"
	rollbackCutoverStatusApplied   = "rolled_back_not_runtime_verified"
	rollbackCutoverStatusBlocked   = "blocked"
	rollbackCutoverReceiptFilename = "rollback.json"
)

type rollbackCutoverOptions struct {
	BuildDir         string
	StageDir         string
	L1Target         string
	ArchiveTarget    string
	TopicTarget      string
	ConfigTarget     string
	RuntimeCandidate string
	RuntimeTarget    string
}

type rollbackCutoverReceipt struct {
	SchemaVersion         string `json:"schema_version"`
	Status                string `json:"status"`
	CutoverReceiptSHA256  string `json:"cutover_receipt_sha256"`
	BuildReceiptSHA256    string `json:"build_receipt_sha256"`
	StageReceiptSHA256    string `json:"stage_receipt_sha256"`
	MappingSHA256         string `json:"mapping_sha256"`
	OldGenerationRestored bool   `json:"old_generation_restored"`
	ReceiptSHA256         string `json:"receipt_sha256"`
	ErrorCode             string `json:"error_code"`
}

func (receipt rollbackCutoverReceipt) canonicalJSON() ([]byte, error) {
	copy := receipt
	copy.ReceiptSHA256 = ""
	return json.Marshal(copy)
}

func (receipt rollbackCutoverReceipt) computeSHA256() (string, error) {
	data, err := receipt.canonicalJSON()
	if err != nil {
		return "", err
	}
	return cutoverSHA256(data), nil
}

func (receipt rollbackCutoverReceipt) validate() error {
	if receipt.SchemaVersion != rollbackCutoverSchemaVersion || !validCutoverSHA256(receipt.ReceiptSHA256) {
		return errors.New("rollback receipt schema or self hash is invalid")
	}
	want, err := receipt.computeSHA256()
	if err != nil || want != receipt.ReceiptSHA256 {
		return errors.New("rollback receipt self hash does not match")
	}
	if receipt.Status == rollbackCutoverStatusApplied {
		if receipt.ErrorCode != "" || !receipt.OldGenerationRestored {
			return errors.New("rollback success state is invalid")
		}
		for _, value := range []string{receipt.CutoverReceiptSHA256, receipt.BuildReceiptSHA256, receipt.StageReceiptSHA256, receipt.MappingSHA256} {
			if !validCutoverSHA256(value) {
				return errors.New("rollback receipt hash is invalid")
			}
		}
		return nil
	}
	if receipt.Status != rollbackCutoverStatusBlocked || receipt.ErrorCode == "" || receipt.OldGenerationRestored {
		return errors.New("rollback blocked state is invalid")
	}
	return nil
}

func runRollbackCutover(args []string, stdout io.Writer, operation rollbackCutoverOperation) int {
	if stdout == nil {
		return 1
	}
	flags := flag.NewFlagSet("rencrow-thread-migrate rollback-cutover", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	buildDir := flags.String("build-dir", "", "offline build directory used by cutover")
	stageDir := flags.String("stage-dir", "", "external stage directory used by cutover")
	l1Target := flags.String("l1-target", "", "active L1 SQLite path")
	archiveTarget := flags.String("archive-target", "", "active archive SQLite path")
	topicTarget := flags.String("topic-target", "", "active IdleChat topic path")
	configTarget := flags.String("config-target", "", "active CORE config path")
	runtimeCandidate := flags.String("runtime-candidate", "", "original staged CORE runtime path")
	runtimeTarget := flags.String("runtime-target", "", "installed CORE runtime binary")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || operation == nil {
		return writeRollbackCutoverReceipt(stdout, newBlockedRollbackCutoverReceipt("invalid_arguments"), 1)
	}
	values := []string{*buildDir, *stageDir, *l1Target, *archiveTarget, *topicTarget, *configTarget, *runtimeCandidate, *runtimeTarget}
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return writeRollbackCutoverReceipt(stdout, newBlockedRollbackCutoverReceipt("invalid_arguments"), 1)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), externalOperationTimeout)
	defer cancel()
	receipt, err := operation(ctx, rollbackCutoverOptions{
		BuildDir: *buildDir, StageDir: *stageDir, L1Target: *l1Target, ArchiveTarget: *archiveTarget,
		TopicTarget: *topicTarget, ConfigTarget: *configTarget, RuntimeCandidate: *runtimeCandidate, RuntimeTarget: *runtimeTarget,
	})
	if err != nil || receipt.Status != rollbackCutoverStatusApplied || receipt.validate() != nil {
		if receipt.validate() != nil {
			receipt = newBlockedRollbackCutoverReceipt("rollback_failed")
		}
		return writeRollbackCutoverReceipt(stdout, receipt, 1)
	}
	return writeRollbackCutoverReceipt(stdout, receipt, 0)
}

func rollbackCutover(ctx context.Context, options rollbackCutoverOptions) (rollbackCutoverReceipt, error) {
	receipt := newBlockedRollbackCutoverReceipt("preflight")
	if ctx == nil || ctx.Err() != nil {
		return receipt, cutoverFailure("context")
	}
	stageRoot, stageReceipt, cutoverReceipt, err := readAppliedCutoverStageStrict(ctx, options.StageDir)
	if err != nil {
		return receipt, cutoverFailure("cutover_receipt")
	}
	receipt.CutoverReceiptSHA256 = cutoverReceipt.ReceiptSHA256
	receipt.BuildReceiptSHA256 = cutoverReceipt.BuildReceiptSHA256
	receipt.StageReceiptSHA256 = cutoverReceipt.StageReceiptSHA256
	receipt.MappingSHA256 = cutoverReceipt.MappingSHA256
	if stageReceipt.ReceiptSHA256 != receipt.StageReceiptSHA256 || stageReceipt.BuildReceiptSHA256 != receipt.BuildReceiptSHA256 || stageReceipt.MappingSHA256 != receipt.MappingSHA256 {
		return sealRollbackCutoverReceipt(receipt, rollbackCutoverStatusBlocked, "stage_binding"), cutoverFailure("stage_binding")
	}
	buildRoot, err := inspectRollbackDirectory(options.BuildDir)
	if err != nil {
		return sealRollbackCutoverReceipt(receipt, rollbackCutoverStatusBlocked, "build_path"), cutoverFailure("build_path")
	}
	specs := []cutoverSwapSpec{
		{role: "l1", candidate: filepath.Join(buildRoot, threadmigration.OfflineBuildL1Filename), target: options.L1Target, oldHash: cutoverReceipt.L1OldSHA256, newHash: cutoverReceipt.L1NewSHA256, mode: 0o600, sqlite: true},
		{role: "archive", candidate: filepath.Join(buildRoot, threadmigration.OfflineBuildArchiveFilename), target: options.ArchiveTarget, oldHash: cutoverReceipt.ArchiveOldSHA256, newHash: cutoverReceipt.ArchiveNewSHA256, mode: 0o600, sqlite: true},
		{role: "topic", candidate: filepath.Join(buildRoot, threadmigration.OfflineBuildTopicFilename), target: options.TopicTarget, oldHash: cutoverReceipt.TopicOldSHA256, newHash: cutoverReceipt.TopicNewSHA256, mode: 0o600},
		{role: "config", candidate: filepath.Join(stageRoot, stageExternalConfigFilename), target: options.ConfigTarget, oldHash: cutoverReceipt.ConfigOldSHA256, newHash: cutoverReceipt.ConfigNewSHA256, mode: 0o600},
		{role: "runtime", candidate: options.RuntimeCandidate, target: options.RuntimeTarget, oldHash: cutoverReceipt.RuntimeOldSHA256, newHash: cutoverReceipt.RuntimeNewSHA256, mode: 0o755, executable: true},
	}
	preparation, err := prepareExplicitRollbackState(ctx, specs, cutoverReceipt.BuildReceiptSHA256)
	if err != nil {
		return sealRollbackCutoverReceipt(receipt, rollbackCutoverStatusBlocked, "artifact_preflight"), cutoverFailure("artifact_preflight")
	}
	if _, err := os.Lstat(filepath.Join(stageRoot, rollbackCutoverReceiptFilename)); err == nil || !errors.Is(err, os.ErrNotExist) {
		return sealRollbackCutoverReceipt(receipt, rollbackCutoverStatusBlocked, "receipt_not_fresh"), cutoverFailure("receipt_not_fresh")
	}
	if err := rollbackCutoverSwaps(specs); err != nil {
		return sealRollbackCutoverReceipt(receipt, rollbackCutoverStatusBlocked, "swap_failed"), cutoverFailure("swap_failed")
	}
	if err := postcheckExplicitRollback(ctx, specs, preparation); err != nil {
		return sealRollbackCutoverReceipt(receipt, rollbackCutoverStatusBlocked, "postcheck_failed"), cutoverFailure("postcheck_failed")
	}
	receipt.OldGenerationRestored = true
	receipt = sealRollbackCutoverReceipt(receipt, rollbackCutoverStatusApplied, "")
	if err := writeRollbackCutoverReceiptFile(stageRoot, receipt); err != nil {
		return sealRollbackCutoverReceipt(receipt, rollbackCutoverStatusBlocked, "receipt_write"), cutoverFailure("receipt_write")
	}
	return receipt, nil
}

func readAppliedCutoverStageStrict(ctx context.Context, raw string) (string, stageExternalReceipt, cutoverReceipt, error) {
	root, err := inspectRollbackDirectory(raw)
	if err != nil {
		return "", stageExternalReceipt{}, cutoverReceipt{}, err
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 2 {
		return "", stageExternalReceipt{}, cutoverReceipt{}, errors.New("post-cutover stage output set is invalid")
	}
	names := []string{entries[0].Name(), entries[1].Name()}
	sort.Strings(names)
	want := []string{stageExternalReceiptFilename, cutoverReceiptFilename}
	sort.Strings(want)
	if names[0] != want[0] || names[1] != want[1] {
		return "", stageExternalReceipt{}, cutoverReceipt{}, errors.New("post-cutover stage output set is invalid")
	}
	stageData, _, err := readCutoverBoundedFile(ctx, filepath.Join(root, stageExternalReceiptFilename), cutoverReceiptMaxBytes)
	if err != nil {
		return "", stageExternalReceipt{}, cutoverReceipt{}, err
	}
	var stageReceipt stageExternalReceipt
	if err := decodeCanonicalCutoverJSON(stageData, &stageReceipt); err != nil || stageReceipt.validate() != nil {
		return "", stageExternalReceipt{}, cutoverReceipt{}, errors.New("stage receipt is invalid")
	}
	cutoverData, _, err := readCutoverBoundedFile(ctx, filepath.Join(root, cutoverReceiptFilename), cutoverReceiptMaxBytes)
	if err != nil {
		return "", stageExternalReceipt{}, cutoverReceipt{}, err
	}
	var applied cutoverReceipt
	if err := decodeCanonicalCutoverJSON(cutoverData, &applied); err != nil || applied.validate() != nil || applied.Status != cutoverStatusApplied {
		return "", stageExternalReceipt{}, cutoverReceipt{}, errors.New("cutover receipt is invalid")
	}
	return root, stageReceipt, applied, nil
}

func decodeCanonicalCutoverJSON(data []byte, output any) error {
	if scanCutoverJSON(data) != nil {
		return errors.New("JSON is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("JSON has trailing data")
	}
	canonical, err := json.Marshal(output)
	if err != nil || !bytes.Equal(data, append(canonical, '\n')) {
		return errors.New("JSON is noncanonical")
	}
	return nil
}

func inspectRollbackDirectory(raw string) (string, error) {
	if raw == "" || strings.IndexByte(raw, 0) >= 0 {
		return "", errors.New("invalid rollback directory")
	}
	root, err := filepath.Abs(raw)
	if err != nil {
		return "", err
	}
	root = filepath.Clean(root)
	info, err := os.Lstat(root)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || (runtime.GOOS != "windows" && (info.Mode().Perm()&0o077 != 0 || info.Mode().Perm()&0o700 != 0o700)) {
		return "", errors.New("rollback directory is unsafe")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || !canonicalThreadConfigSamePath(root, resolved) {
		return "", errors.New("rollback directory is not canonical")
	}
	return root, nil
}

type explicitRollbackPreparation struct {
	observedMutableTargetHashes map[string]string
}

func prepareExplicitRollback(ctx context.Context, specs []cutoverSwapSpec, buildHash string) error {
	_, err := prepareExplicitRollbackState(ctx, specs, buildHash)
	return err
}

func prepareExplicitRollbackState(ctx context.Context, specs []cutoverSwapSpec, buildHash string) (explicitRollbackPreparation, error) {
	preparation := explicitRollbackPreparation{observedMutableTargetHashes: make(map[string]string)}
	if !validCutoverSHA256(buildHash) || len(specs) != 5 {
		return preparation, errors.New("invalid rollback spec")
	}
	seen := make(map[string]struct{}, len(specs)*3)
	seenFiles := make([]os.FileInfo, 0, len(specs)*2)
	for index := range specs {
		spec := &specs[index]
		candidate, err := filepath.Abs(spec.candidate)
		if err != nil {
			return preparation, err
		}
		candidate = filepath.Clean(candidate)
		if _, err := os.Lstat(candidate); err == nil || !errors.Is(err, os.ErrNotExist) {
			return preparation, errors.New("rollback candidate is occupied")
		}
		target, targetInfo, targetHash, err := inspectCutoverFile(ctx, spec.target, spec.mode, spec.executable)
		if err != nil {
			return preparation, errors.New("rollback target is invalid")
		}
		if !isMutableRollbackRole(spec.role) && targetHash != spec.newHash {
			return preparation, errors.New("rollback target is invalid")
		}
		if isMutableRollbackRole(spec.role) {
			preparation.observedMutableTargetHashes[target] = targetHash
		}
		rollbackPath := target + ".pre-threadid-" + buildHash[:12]
		rollback, rollbackInfo, rollbackHash, err := inspectCutoverFile(ctx, rollbackPath, spec.mode, spec.executable)
		if err != nil || rollbackHash != spec.oldHash || os.SameFile(targetInfo, rollbackInfo) {
			return preparation, errors.New("rollback artifact is invalid")
		}
		for _, info := range seenFiles {
			if os.SameFile(info, targetInfo) || os.SameFile(info, rollbackInfo) {
				return preparation, errors.New("rollback files overlap")
			}
		}
		seenFiles = append(seenFiles, targetInfo, rollbackInfo)
		spec.candidate, spec.target, spec.rollback = candidate, target, rollback
		spec.candidateInfo, spec.targetInfo = targetInfo, rollbackInfo
		for _, path := range []string{candidate, target, rollback} {
			key := path
			if runtime.GOOS == "windows" {
				key = strings.ToLower(key)
			}
			if _, exists := seen[key]; exists {
				return preparation, errors.New("rollback paths overlap")
			}
			seen[key] = struct{}{}
		}
		if spec.sqlite {
			for _, base := range []string{target, rollback} {
				for _, suffix := range []string{"-wal", "-shm", "-journal"} {
					if _, err := os.Lstat(base + suffix); err == nil || !errors.Is(err, os.ErrNotExist) {
						return preparation, errors.New("SQLite sidecar is present")
					}
				}
			}
		}
	}
	return preparation, nil
}

func isMutableRollbackRole(role string) bool {
	switch role {
	case "l1", "archive", "topic":
		return true
	default:
		return false
	}
}

func postcheckExplicitRollback(ctx context.Context, specs []cutoverSwapSpec, preparation explicitRollbackPreparation) error {
	for _, spec := range specs {
		if _, _, hash, err := inspectCutoverFile(ctx, spec.target, spec.mode, spec.executable); err != nil || hash != spec.oldHash {
			return errors.New("rollback target postcheck failed")
		}
		expectedCandidateHash := spec.newHash
		if observedHash, ok := preparation.observedMutableTargetHashes[spec.target]; ok {
			expectedCandidateHash = observedHash
		}
		if _, _, hash, err := inspectCutoverFile(ctx, spec.candidate, spec.mode, spec.executable); err != nil || hash != expectedCandidateHash {
			return errors.New("rollback candidate postcheck failed")
		}
	}
	return nil
}

func writeRollbackCutoverReceiptFile(stageRoot string, receipt rollbackCutoverReceipt) error {
	if err := receipt.validate(); err != nil {
		return err
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := filepath.Join(stageRoot, rollbackCutoverReceiptFilename)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	written, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil || written != len(data) {
		return errors.New("rollback receipt write failed")
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != int64(len(data)) {
		return errors.New("rollback receipt metadata is invalid")
	}
	if runtime.GOOS != "windows" {
		directory, err := os.Open(stageRoot)
		if err != nil {
			return err
		}
		syncErr := directory.Sync()
		closeErr := directory.Close()
		if syncErr != nil || closeErr != nil {
			return errors.New("rollback receipt directory sync failed")
		}
	}
	return nil
}

func sealRollbackCutoverReceipt(receipt rollbackCutoverReceipt, status, code string) rollbackCutoverReceipt {
	receipt.Status = status
	receipt.ErrorCode = code
	if status != rollbackCutoverStatusApplied {
		receipt.OldGenerationRestored = false
	}
	receipt.ReceiptSHA256 = ""
	receipt.ReceiptSHA256, _ = receipt.computeSHA256()
	return receipt
}

func newBlockedRollbackCutoverReceipt(code string) rollbackCutoverReceipt {
	return sealRollbackCutoverReceipt(rollbackCutoverReceipt{SchemaVersion: rollbackCutoverSchemaVersion}, rollbackCutoverStatusBlocked, code)
}

func writeRollbackCutoverReceipt(stdout io.Writer, receipt rollbackCutoverReceipt, code int) int {
	if receipt.validate() != nil {
		receipt = newBlockedRollbackCutoverReceipt("rollback_failed")
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
