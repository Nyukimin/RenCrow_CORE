package main

import (
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
	"time"

	adapterconfig "github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	redisstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/redis"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/vectordb"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/threadmigration"
)

const (
	stageExternalSchemaVersion    = "rencrow.threadmigration.external_stage.v1"
	stageExternalStatusStaged     = "staged_not_active"
	stageExternalStatusBlocked    = "blocked"
	stageExternalConfigFilename   = "core.candidate.yaml"
	stageExternalReceiptFilename  = "stage.json"
	stageExternalInvalidArguments = "invalid_arguments"
	stageExternalOperationFailed  = "stage_failed"
)

type stageExternalOptions struct {
	ConfigPath       string
	BuildDir         string
	StageDir         string
	RedisTargetDB    int
	TargetCollection string
}

type stageExternalReceipt struct {
	SchemaVersion      string                                  `json:"schema_version"`
	Status             string                                  `json:"status"`
	BuildReceiptSHA256 string                                  `json:"build_receipt_sha256"`
	MappingSHA256      string                                  `json:"mapping_sha256"`
	ConfigCandidate    *canonicalThreadConfigCandidateReceipt  `json:"config_candidate,omitempty"`
	QdrantStage        *vectordb.CanonicalThreadStageReceipt   `json:"qdrant_stage,omitempty"`
	RedisStage         *redisstore.CanonicalThreadStageReceipt `json:"redis_stage,omitempty"`
	ReceiptSHA256      string                                  `json:"receipt_sha256"`
	ErrorCode          string                                  `json:"error_code"`
}

func (receipt stageExternalReceipt) canonicalJSON() ([]byte, error) {
	copy := receipt
	copy.ReceiptSHA256 = ""
	return json.Marshal(copy)
}

func (receipt stageExternalReceipt) computeSHA256() (string, error) {
	data, err := receipt.canonicalJSON()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func (receipt stageExternalReceipt) validate() error {
	if receipt.SchemaVersion != stageExternalSchemaVersion {
		return errors.New("external stage receipt schema is invalid")
	}
	if !validStageExternalSHA256(receipt.ReceiptSHA256) {
		return errors.New("external stage receipt self hash is invalid")
	}
	want, err := receipt.computeSHA256()
	if err != nil || want != receipt.ReceiptSHA256 {
		return errors.New("external stage receipt self hash does not match")
	}
	for _, value := range []string{receipt.BuildReceiptSHA256, receipt.MappingSHA256} {
		if value != "" && !validStageExternalSHA256(value) {
			return errors.New("external stage receipt binding hash is invalid")
		}
	}
	if receipt.ConfigCandidate != nil && receipt.ConfigCandidate.validate() != nil {
		return errors.New("external stage config receipt is invalid")
	}
	if receipt.QdrantStage != nil && receipt.QdrantStage.Validate() != nil {
		return errors.New("external stage Qdrant receipt is invalid")
	}
	if receipt.RedisStage != nil && receipt.RedisStage.Validate() != nil {
		return errors.New("external stage Redis receipt is invalid")
	}
	switch receipt.Status {
	case stageExternalStatusStaged:
		if receipt.ErrorCode != "" || !validStageExternalSHA256(receipt.BuildReceiptSHA256) || !validStageExternalSHA256(receipt.MappingSHA256) || receipt.ConfigCandidate == nil || receipt.QdrantStage == nil || receipt.RedisStage == nil {
			return errors.New("staged external receipt is incomplete")
		}
		if receipt.ConfigCandidate.Status != canonicalThreadConfigCandidateReady || receipt.QdrantStage.Status != vectordb.CanonicalThreadStageStatusStagedNotActive || receipt.RedisStage.Status != redisstore.CanonicalThreadStageStatusStaged {
			return errors.New("external stage child status is incomplete")
		}
		if receipt.ConfigCandidate.TargetRedisDB != receipt.RedisStage.TargetDB || receipt.ConfigCandidate.TargetCollectionSHA256 != receipt.QdrantStage.TargetCollectionHash || receipt.MappingSHA256 != receipt.QdrantStage.MappingSHA256 || receipt.MappingSHA256 != receipt.RedisStage.MappingSHA256 {
			return errors.New("external stage child receipts are not bound")
		}
	case stageExternalStatusBlocked:
		if receipt.ErrorCode == "" {
			return errors.New("blocked external stage receipt has no error code")
		}
	default:
		return errors.New("external stage receipt status is invalid")
	}
	return nil
}

func runStageExternal(args []string, stdout io.Writer, operation stageExternalOperation) int {
	if stdout == nil {
		return 1
	}
	flags := flag.NewFlagSet("rencrow-thread-migrate stage-external", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "active CORE config path")
	buildDir := flags.String("build-dir", "", "validated offline build directory")
	stageDir := flags.String("stage-dir", "", "fresh owner-only stage directory")
	redisTargetDB := flags.Int("redis-target-db", -1, "fresh Redis logical DB")
	targetCollection := flags.String("qdrant-target-collection", "", "fresh Qdrant collection")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || strings.TrimSpace(*configPath) == "" || strings.TrimSpace(*buildDir) == "" || strings.TrimSpace(*stageDir) == "" || *redisTargetDB < 0 || strings.TrimSpace(*targetCollection) == "" || operation == nil {
		return writeStageExternalReceipt(stdout, newBlockedStageExternalReceipt(stageExternalInvalidArguments), 1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), externalOperationTimeout)
	defer cancel()
	receipt, err := operation(ctx, stageExternalOptions{
		ConfigPath: *configPath, BuildDir: *buildDir, StageDir: *stageDir,
		RedisTargetDB: *redisTargetDB, TargetCollection: *targetCollection,
	})
	if err != nil || receipt.Status != stageExternalStatusStaged || receipt.validate() != nil {
		if receipt.validate() != nil {
			receipt = newBlockedStageExternalReceipt(stageExternalOperationFailed)
		}
		return writeStageExternalReceipt(stdout, receipt, 1)
	}
	return writeStageExternalReceipt(stdout, receipt, 0)
}

func stageExternal(ctx context.Context, options stageExternalOptions) (stageExternalReceipt, error) {
	receipt := newBlockedStageExternalReceipt("preflight")
	if ctx == nil || ctx.Err() != nil {
		return receipt, errors.New("external stage blocked: context")
	}
	stageDir, err := resolveStageExternalDir(options.StageDir)
	if err != nil {
		return receipt, errors.New("external stage blocked: stage_directory")
	}
	finishBlocked := func(code string, cause error) (stageExternalReceipt, error) {
		_ = cause
		blocked := sealStageExternalReceipt(receipt, stageExternalStatusBlocked, code)
		if writeErr := writeStageExternalReceiptFile(stageDir, blocked); writeErr != nil {
			return blocked, errors.New("external stage blocked: receipt_write")
		}
		return blocked, errors.New("external stage blocked: " + code)
	}

	artifacts, err := threadmigration.ReadOfflineBuildStrict(ctx, options.BuildDir)
	if err != nil {
		return finishBlocked("build_read", err)
	}
	receipt.BuildReceiptSHA256 = artifacts.Receipt.ReceiptSHA256
	receipt.MappingSHA256 = artifacts.Plan.MappingSHA256
	cfg, err := adapterconfig.LoadConfig(options.ConfigPath)
	if err != nil {
		return finishBlocked("config_read", err)
	}
	if cfg.Conversation.RedisURL == "" || cfg.Conversation.VectorDBURL == "" || cfg.Conversation.VectorCollection == options.TargetCollection || cfg.Conversation.VectorDimension != artifacts.Receipt.QdrantVectorDimension {
		return finishBlocked("route_preflight", errors.New("active route does not match build contract"))
	}
	configReceipt, err := renderCanonicalThreadConfigCandidate(options.ConfigPath, filepath.Join(stageDir, stageExternalConfigFilename), options.RedisTargetDB, options.TargetCollection)
	receipt.ConfigCandidate = &configReceipt
	if err != nil {
		return finishBlocked("config_candidate", err)
	}
	qdrantReceipt, err := vectordb.StageCanonicalThreadPointsFresh(ctx, cfg.Conversation.VectorDBURL, options.TargetCollection, artifacts.Qdrant)
	receipt.QdrantStage = &qdrantReceipt
	if err != nil {
		return finishBlocked("qdrant_stage", err)
	}
	redisReceipt, err := redisstore.StageCanonicalThreadEntriesFresh(ctx, cfg.Conversation.RedisURL, options.RedisTargetDB, artifacts.Redis, time.Now().UTC())
	receipt.RedisStage = &redisReceipt
	if err != nil {
		return finishBlocked("redis_stage", err)
	}
	receipt = sealStageExternalReceipt(receipt, stageExternalStatusStaged, "")
	if err := receipt.validate(); err != nil {
		return finishBlocked("receipt_invalid", err)
	}
	if err := writeStageExternalReceiptFile(stageDir, receipt); err != nil {
		return receipt, errors.New("external stage blocked: receipt_write")
	}
	return receipt, nil
}

func resolveStageExternalDir(raw string) (string, error) {
	if raw == "" || strings.IndexByte(raw, 0) >= 0 {
		return "", errors.New("invalid stage directory")
	}
	absolute, err := filepath.Abs(raw)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	info, err := os.Lstat(absolute)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("stage directory is unsafe")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil || !canonicalThreadConfigSamePath(absolute, resolved) || (runtime.GOOS != "windows" && (info.Mode().Perm()&0o077 != 0 || info.Mode().Perm()&0o700 != 0o700)) {
		return "", errors.New("stage directory is not canonical owner-only")
	}
	entries, err := os.ReadDir(absolute)
	if err != nil || len(entries) != 0 {
		return "", errors.New("stage directory is not fresh")
	}
	return absolute, nil
}

func writeStageExternalReceiptFile(stageDir string, receipt stageExternalReceipt) error {
	if err := receipt.validate(); err != nil {
		return err
	}
	if err := verifyStageExternalOutputSet(stageDir, false); err != nil {
		return err
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := filepath.Join(stageDir, stageExternalReceiptFilename)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	written, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil || written != len(data) {
		return errors.New("stage receipt write failed")
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return errors.New("stage receipt metadata is unsafe")
	}
	if runtime.GOOS != "windows" {
		directory, err := os.Open(stageDir)
		if err != nil {
			return err
		}
		syncErr := directory.Sync()
		closeErr := directory.Close()
		if syncErr != nil || closeErr != nil {
			return errors.New("stage receipt directory sync failed")
		}
	}
	return nil
}

func verifyStageExternalOutputSet(stageDir string, requireReceipt bool) error {
	entries, err := os.ReadDir(stageDir)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return errors.New("stage output metadata is unsafe")
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	want := []string{}
	if _, err := os.Lstat(filepath.Join(stageDir, stageExternalConfigFilename)); err == nil {
		want = append(want, stageExternalConfigFilename)
	}
	if requireReceipt {
		want = append(want, stageExternalReceiptFilename)
	}
	sort.Strings(want)
	if len(names) != len(want) {
		return errors.New("stage output set is invalid")
	}
	for index := range names {
		if names[index] != want[index] {
			return errors.New("stage output set is invalid")
		}
	}
	return nil
}

func newBlockedStageExternalReceipt(code string) stageExternalReceipt {
	receipt := stageExternalReceipt{SchemaVersion: stageExternalSchemaVersion}
	return sealStageExternalReceipt(receipt, stageExternalStatusBlocked, code)
}

func sealStageExternalReceipt(receipt stageExternalReceipt, status, errorCode string) stageExternalReceipt {
	receipt.Status = status
	receipt.ErrorCode = errorCode
	receipt.ReceiptSHA256 = ""
	receipt.ReceiptSHA256, _ = receipt.computeSHA256()
	return receipt
}

func writeStageExternalReceipt(stdout io.Writer, receipt stageExternalReceipt, code int) int {
	if receipt.validate() != nil {
		receipt = newBlockedStageExternalReceipt(stageExternalOperationFailed)
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

func validStageExternalSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
