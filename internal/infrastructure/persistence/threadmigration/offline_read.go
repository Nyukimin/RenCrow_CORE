package threadmigration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"
)

const offlineBuildReceiptMaxBytes int64 = 1 << 20

// OfflineBuildArtifacts is the validated, apply-ready view of one complete
// offline build. Redis and Qdrant receipts describe the already-canonical
// artifact bytes used for staging; the original transformation receipts stay
// bound to BuildReceipt as build-time evidence.
type OfflineBuildArtifacts struct {
	Receipt BuildReceipt
	Plan    Plan
	Redis   RedisPreparationResult
	Qdrant  QdrantPreparationResult
}

// ReadOfflineBuildStrict revalidates an immutable eight-artifact build without
// mutating it or contacting a runtime store.
func ReadOfflineBuildStrict(ctx context.Context, dir string) (OfflineBuildArtifacts, error) {
	if ctx == nil || ctx.Err() != nil {
		return OfflineBuildArtifacts{}, offlineBuildReadError("context")
	}
	root, err := resolveOfflineBuildArtifactDir(dir)
	if err != nil {
		return OfflineBuildArtifacts{}, offlineBuildReadError("directory")
	}
	if err := verifyOfflineBuildOutputSet(root, true); err != nil {
		return OfflineBuildArtifacts{}, offlineBuildReadError("artifact_set")
	}

	receiptData, err := readOfflineBuildArtifact(ctx, filepath.Join(root, OfflineBuildReceiptFilename), offlineBuildReceiptMaxBytes)
	if err != nil {
		return OfflineBuildArtifacts{}, offlineBuildReadError("receipt_read")
	}
	var receipt BuildReceipt
	if err := decodeOfflineBuildJSONStrict(receiptData, &receipt, true); err != nil || receipt.Validate() != nil {
		return OfflineBuildArtifacts{}, offlineBuildReadError("receipt_invalid")
	}
	canonicalReceipt, err := json.Marshal(receipt)
	if err != nil || !bytes.Equal(receiptData, append(canonicalReceipt, '\n')) {
		return OfflineBuildArtifacts{}, offlineBuildReadError("receipt_noncanonical")
	}

	mappingData, err := readOfflineBuildArtifact(ctx, filepath.Join(root, OfflineBuildMappingFilename), ExternalSnapshotMaxFileBytes)
	if err != nil {
		return OfflineBuildArtifacts{}, offlineBuildReadError("mapping_read")
	}
	var mapping struct {
		Generic       []ThreadMapping `json:"generic"`
		ChatGPT       []ThreadMapping `json:"chatgpt"`
		MappingSHA256 string          `json:"mapping_sha256"`
	}
	if err := decodeOfflineBuildJSONStrict(mappingData, &mapping, true); err != nil || mapping.Generic == nil || mapping.ChatGPT == nil {
		return OfflineBuildArtifacts{}, offlineBuildReadError("mapping_invalid")
	}
	plan := Plan{Generic: mapping.Generic, ChatGPT: mapping.ChatGPT, MappingSHA256: mapping.MappingSHA256}
	canonicalMapping, err := offlineBuildMappingJSON(plan)
	if err != nil || !bytes.Equal(mappingData, canonicalMapping) || plan.MappingSHA256 != receipt.MappingSHA256 || len(plan.Generic) != receipt.MappingGenericCount || len(plan.ChatGPT) != receipt.MappingChatGPTCount {
		return OfflineBuildArtifacts{}, offlineBuildReadError("mapping_mismatch")
	}

	redisData, err := readOfflineBuildArtifact(ctx, filepath.Join(root, OfflineBuildRedisFilename), ExternalSnapshotMaxFileBytes)
	if err != nil {
		return OfflineBuildArtifacts{}, offlineBuildReadError("redis_read")
	}
	var redisEntries []RedisEntry
	if err := decodeOfflineBuildJSONStrict(redisData, &redisEntries, false); err != nil || redisEntries == nil {
		return OfflineBuildArtifacts{}, offlineBuildReadError("redis_invalid")
	}
	canonicalRedis, err := canonicalRedisEntriesJSON(redisEntries)
	if err != nil || !bytes.Equal(redisData, canonicalRedis) {
		return OfflineBuildArtifacts{}, offlineBuildReadError("redis_noncanonical")
	}
	redisHash := digestBytesFromBytes(redisData)
	redisReceipt := RedisPreparationReceipt{
		SchemaVersion: RedisPreparationReceiptSchemaVersion,
		Status:        RedisPreparationStatus,
		SourceCount:   len(redisEntries),
		OutputCount:   len(redisEntries),
		SourceSHA256:  redisHash,
		OutputSHA256:  redisHash,
		MappingSHA256: plan.MappingSHA256,
	}
	redisReceipt.ReceiptSHA256, err = redisReceipt.ComputeSHA256()
	if err != nil {
		return OfflineBuildArtifacts{}, offlineBuildReadError("redis_receipt")
	}
	redisResult := RedisPreparationResult{Plan: plan, Entries: redisEntries, Receipt: redisReceipt}
	if err := redisResult.Validate(); err != nil || len(redisEntries) != receipt.RedisOutputCount || redisHash != receipt.RedisOutputSHA256 {
		return OfflineBuildArtifacts{}, offlineBuildReadError("redis_mismatch")
	}

	qdrantData, err := readOfflineBuildArtifact(ctx, filepath.Join(root, OfflineBuildQdrantFilename), ExternalSnapshotMaxFileBytes)
	if err != nil {
		return OfflineBuildArtifacts{}, offlineBuildReadError("qdrant_read")
	}
	var qdrantPoints []QdrantPointSnapshot
	if err := decodeOfflineBuildJSONStrict(qdrantData, &qdrantPoints, false); err != nil || qdrantPoints == nil {
		return OfflineBuildArtifacts{}, offlineBuildReadError("qdrant_invalid")
	}
	canonicalQdrant, err := qdrantCanonicalPointsJSON(qdrantPoints)
	if err != nil || !bytes.Equal(qdrantData, canonicalQdrant) {
		return OfflineBuildArtifacts{}, offlineBuildReadError("qdrant_noncanonical")
	}
	qdrantHash := digestBytesFromBytes(qdrantData)
	qdrantReceipt := QdrantPreparationReceipt{
		SchemaVersion:        QdrantPreparationReceiptSchemaVersion,
		Status:               QdrantPreparationStatus,
		SourceCount:          len(qdrantPoints),
		OutputCount:          len(qdrantPoints),
		DuplicateSourceCount: 0,
		VectorDimension:      receipt.QdrantVectorDimension,
		SourceSHA256:         qdrantHash,
		OutputSHA256:         qdrantHash,
		MappingSHA256:        plan.MappingSHA256,
	}
	qdrantReceipt.ReceiptSHA256, err = qdrantReceipt.ComputeSHA256()
	if err != nil {
		return OfflineBuildArtifacts{}, offlineBuildReadError("qdrant_receipt")
	}
	qdrantResult := QdrantPreparationResult{Plan: plan, Points: qdrantPoints, Receipt: qdrantReceipt}
	if err := qdrantResult.Validate(); err != nil || len(qdrantPoints) != receipt.QdrantOutputCount || qdrantHash != receipt.QdrantOutputSHA256 {
		return OfflineBuildArtifacts{}, offlineBuildReadError("qdrant_mismatch")
	}

	if err := verifyOfflineBuildEvidence(ctx, root, receipt); err != nil {
		return OfflineBuildArtifacts{}, err
	}
	return OfflineBuildArtifacts{Receipt: receipt, Plan: plan, Redis: redisResult, Qdrant: qdrantResult}, nil
}

func resolveOfflineBuildArtifactDir(raw string) (string, error) {
	if raw == "" || bytes.IndexByte([]byte(raw), 0) >= 0 {
		return "", errors.New("invalid directory")
	}
	absolute, err := filepath.Abs(raw)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	if !safeOfflineBuildOutputDir(absolute) {
		return "", errors.New("unsafe directory")
	}
	return absolute, nil
}

func readOfflineBuildArtifact(ctx context.Context, path string, limit int64) ([]byte, error) {
	if ctx == nil || ctx.Err() != nil || limit <= 0 {
		return nil, errors.New("invalid read")
	}
	before, err := os.Lstat(path)
	if err != nil || !externalSnapshotRegularNonSymlink(before) || before.Mode().Perm() != 0o600 || before.Size() < 0 || before.Size() > limit {
		return nil, errors.New("invalid artifact")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, limit+1))
	closeErr := file.Close()
	after, statErr := os.Lstat(path)
	if readErr != nil || closeErr != nil || statErr != nil || !externalSnapshotRegularNonSymlink(after) || after.Mode().Perm() != 0o600 || !os.SameFile(before, after) || before.Size() != after.Size() || int64(len(data)) != before.Size() || int64(len(data)) > limit {
		return nil, errors.New("artifact changed during read")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

func decodeOfflineBuildJSONStrict(data []byte, target any, requireObject bool) error {
	if len(data) == 0 || !utf8.Valid(data) || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return errors.New("invalid JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	first, err := decoder.Token()
	if err != nil {
		return err
	}
	if requireObject && first != json.Delim('{') {
		return errors.New("JSON root is not an object")
	}
	if !requireObject && first != json.Delim('[') {
		return errors.New("JSON root is not an array")
	}
	if err := scanExternalSnapshotJSONValue(decoder, first, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("JSON has trailing content")
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("JSON has trailing content")
	}
	return nil
}

func verifyOfflineBuildEvidence(ctx context.Context, root string, receipt BuildReceipt) error {
	type expected struct {
		name       string
		hash       string
		bytes      int64
		count      int
		allowEmpty bool
	}
	items := []expected{
		{OfflineBuildL1Filename, receipt.L1OutputSHA256, receipt.L1OutputBytes, int(receipt.L1OutputCount), false},
		{OfflineBuildArchiveFilename, receipt.ArchiveOutputSHA256, receipt.ArchiveOutputBytes, int(receipt.ArchiveOutputCount), false},
		{OfflineBuildTopicFilename, receipt.TopicOutputSHA256, receipt.TopicOutputBytes, receipt.TopicOutputCount, true},
		{OfflineBuildTopicQuarantineFilename, receipt.TopicQuarantineSHA256, receipt.TopicQuarantineBytes, receipt.TopicQuarantineCount, true},
		{OfflineBuildRedisFilename, receipt.RedisOutputSHA256, receipt.RedisOutputBytes, receipt.RedisOutputCount, false},
		{OfflineBuildQdrantFilename, receipt.QdrantOutputSHA256, receipt.QdrantOutputBytes, receipt.QdrantOutputCount, false},
		{OfflineBuildMappingFilename, receipt.MappingArtifactSHA256, receipt.MappingArtifactBytes, receipt.MappingGenericCount + receipt.MappingChatGPTCount, false},
	}
	evidence := make([]offlineBuildOutputEvidence, 0, len(items))
	for _, item := range items {
		hash, size, err := hashOfflineBuildFile(ctx, filepath.Join(root, item.name), item.allowEmpty)
		if err != nil || hash != item.hash || size != item.bytes {
			return offlineBuildReadError("artifact_mismatch")
		}
		evidence = append(evidence, offlineBuildOutputEvidence{Name: item.name, Hash: hash, Bytes: size, Count: item.count})
	}
	setHash, err := offlineBuildArtifactSetSHA256(evidence)
	if err != nil || setHash != receipt.OutputArtifactSetSHA256 {
		return offlineBuildReadError("artifact_set_mismatch")
	}
	return nil
}

type offlineBuildReadFailure string

func (failure offlineBuildReadFailure) Error() string {
	return "offline ThreadID build read blocked: " + string(failure)
}

func offlineBuildReadError(code string) error {
	return offlineBuildReadFailure(code)
}
