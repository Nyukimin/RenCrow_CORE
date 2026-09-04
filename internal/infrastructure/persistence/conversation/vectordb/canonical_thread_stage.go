package vectordb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/threadmigration"
	"github.com/qdrant/go-client/qdrant"
)

const (
	CanonicalThreadStageReceiptSchemaVersion  = "rencrow.threadmigration.qdrant_stage.v1"
	CanonicalThreadStageStatusStagedNotActive = "staged_not_active"
	CanonicalThreadStageStatusBlocked         = "blocked"

	canonicalThreadStageErrorContext         = "context_unavailable"
	canonicalThreadStageErrorURL             = "invalid_qdrant_url"
	canonicalThreadStageErrorCollection      = "invalid_target_collection"
	canonicalThreadStageErrorPrepared        = "invalid_prepared_result"
	canonicalThreadStageErrorFactory         = "target_client_unavailable"
	canonicalThreadStageErrorProbe           = "target_probe_failed"
	canonicalThreadStageErrorTargetNotFresh  = "target_not_fresh"
	canonicalThreadStageErrorCreate          = "target_collection_create_failed"
	canonicalThreadStageErrorIndex           = "target_index_failed"
	canonicalThreadStageErrorWrite           = "target_write_failed"
	canonicalThreadStageErrorCount           = "target_count_failed"
	canonicalThreadStageErrorCountMismatch   = "target_count_mismatch"
	canonicalThreadStageErrorReadback        = "target_readback_failed"
	canonicalThreadStageErrorClose           = "target_close_failed"
	canonicalThreadStageErrorReceipt         = "receipt_invalid"
	canonicalThreadStageErrorCanceled        = "context_canceled"
	canonicalThreadStageErrorDeadline        = "context_deadline"
	canonicalThreadStageErrorDimension       = "empty_vector_dimension"
	canonicalThreadStageErrorPointConversion = "point_conversion_failed"

	canonicalThreadStageMaxCollectionRunes = 255
	canonicalThreadStageMaxBatchPoints     = 128
	canonicalThreadStageMaxBatchBytes      = 4 << 20
	canonicalThreadStageMaxJSONDepth       = 128
	canonicalThreadStageScrollPageLimit    = canonicalThreadStageMaxBatchPoints
)

// CanonicalThreadStageReceipt is bounded evidence for a fresh Qdrant staging
// attempt. The target collection name is intentionally represented only by a
// SHA-256 digest so receipts can be persisted without exposing deployment
// paths, aliases, or other runtime configuration.
type CanonicalThreadStageReceipt struct {
	SchemaVersion        string `json:"schema_version"`
	Status               string `json:"status"`
	TargetCollectionHash string `json:"target_collection_hash"`
	PreparedCount        int    `json:"prepared_count"`
	StagedCount          int    `json:"staged_count"`
	VectorDimension      int    `json:"vector_dimension"`
	PreparedOutputSHA256 string `json:"prepared_output_sha256"`
	MappingSHA256        string `json:"mapping_sha256"`
	ReadbackOutputSHA256 string `json:"readback_output_sha256"`
	ReceiptSHA256        string `json:"receipt_sha256"`
	ErrorCode            string `json:"error_code"`
}

// CanonicalJSON returns the deterministic receipt payload. ReceiptSHA256 is
// excluded to avoid hashing a value which contains its own digest.
func (receipt CanonicalThreadStageReceipt) CanonicalJSON() ([]byte, error) {
	copy := receipt
	copy.ReceiptSHA256 = ""
	return json.Marshal(copy)
}

// ComputeSHA256 returns the lowercase SHA-256 digest of CanonicalJSON.
func (receipt CanonicalThreadStageReceipt) ComputeSHA256() (string, error) {
	encoded, err := receipt.CanonicalJSON()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// Validate checks receipt schema, bounded values, safe error syntax, and the
// self-referential receipt digest. A blocked receipt may contain only the
// fields known before the failing boundary; successful receipts are complete
// and hash-bound to the prepared and read-back outputs.
func (receipt CanonicalThreadStageReceipt) Validate() error {
	if receipt.SchemaVersion != CanonicalThreadStageReceiptSchemaVersion {
		return fmt.Errorf("unsupported Qdrant stage receipt schema %q", receipt.SchemaVersion)
	}
	switch receipt.Status {
	case CanonicalThreadStageStatusStagedNotActive:
		if receipt.ErrorCode != "" {
			return errors.New("successful Qdrant stage receipt must not have an error code")
		}
	case CanonicalThreadStageStatusBlocked:
		if !validCanonicalThreadStageErrorCode(receipt.ErrorCode) {
			return errors.New("blocked Qdrant stage receipt has an invalid error code")
		}
	default:
		return fmt.Errorf("invalid Qdrant stage receipt status %q", receipt.Status)
	}
	if receipt.PreparedCount < 0 || receipt.PreparedCount > threadmigration.QdrantPreparationMaxPoints || receipt.StagedCount < 0 || receipt.StagedCount > threadmigration.QdrantPreparationMaxPoints || receipt.StagedCount > receipt.PreparedCount {
		return errors.New("Qdrant stage receipt counts are out of bounds")
	}
	if receipt.VectorDimension < 0 {
		return errors.New("Qdrant stage receipt vector dimension is negative")
	}
	if err := validateCanonicalThreadStageSHA256(receipt.TargetCollectionHash, "target collection hash"); err != nil {
		return err
	}
	for _, item := range []struct {
		value string
		label string
	}{
		{receipt.PreparedOutputSHA256, "prepared output SHA256"},
		{receipt.MappingSHA256, "mapping SHA256"},
		{receipt.ReadbackOutputSHA256, "readback output SHA256"},
	} {
		if item.value != "" {
			if err := validateCanonicalThreadStageSHA256(item.value, item.label); err != nil {
				return err
			}
		}
	}
	if err := validateCanonicalThreadStageSHA256(receipt.ReceiptSHA256, "receipt SHA256"); err != nil {
		return err
	}
	if receipt.Status == CanonicalThreadStageStatusStagedNotActive {
		if receipt.PreparedCount == 0 || receipt.PreparedCount != receipt.StagedCount || receipt.VectorDimension == 0 {
			return errors.New("successful Qdrant stage receipt has incomplete counts or dimension")
		}
		if receipt.PreparedOutputSHA256 == "" || receipt.MappingSHA256 == "" || receipt.ReadbackOutputSHA256 == "" || receipt.PreparedOutputSHA256 != receipt.ReadbackOutputSHA256 {
			return errors.New("successful Qdrant stage receipt has unbound output hashes")
		}
	}
	computed, err := receipt.ComputeSHA256()
	if err != nil {
		return fmt.Errorf("compute Qdrant stage receipt SHA256: %w", err)
	}
	if computed != receipt.ReceiptSHA256 {
		return errors.New("Qdrant stage receipt SHA256 does not match canonical JSON")
	}
	return nil
}

func validateCanonicalThreadStageSHA256(value, label string) error {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return fmt.Errorf("%s is not lowercase hexadecimal SHA-256", label)
	}
	if _, err := hex.DecodeString(value); err != nil {
		return fmt.Errorf("%s is not lowercase hexadecimal SHA-256", label)
	}
	return nil
}

func validCanonicalThreadStageErrorCode(value string) bool {
	if value == "" || len(value) > 64 || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}

// canonicalThreadStageClient is the narrow owner seam used by the staging
// operation. Production uses qdrant.Client; tests replace the private factory
// with an in-memory client and therefore never open a network connection.
type canonicalThreadStageClient interface {
	CollectionExists(context.Context, string) (bool, error)
	CreateCollection(context.Context, *qdrant.CreateCollection) error
	CreateFieldIndex(context.Context, *qdrant.CreateFieldIndexCollection) (*qdrant.UpdateResult, error)
	Upsert(context.Context, *qdrant.UpsertPoints) (*qdrant.UpdateResult, error)
	Count(context.Context, *qdrant.CountPoints) (uint64, error)
	ScrollAndOffset(context.Context, *qdrant.ScrollPoints) ([]*qdrant.RetrievedPoint, *qdrant.PointId, error)
	Close() error
}

type canonicalThreadStageClientFactory func(*qdrant.Config) (canonicalThreadStageClient, error)

var newCanonicalThreadStageClient canonicalThreadStageClientFactory = func(config *qdrant.Config) (canonicalThreadStageClient, error) {
	if config == nil {
		return nil, errCanonicalThreadStageInvalidInput
	}
	return qdrant.NewClient(config)
}

type canonicalThreadStageError struct {
	code  string
	cause error
}

func (err *canonicalThreadStageError) Error() string {
	if err == nil {
		return "qdrant thread stage blocked"
	}
	return "qdrant thread stage blocked: " + err.code
}

func (err *canonicalThreadStageError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

var (
	errCanonicalThreadStageInvalidContext = errors.New("invalid context")
	errCanonicalThreadStageInvalidInput   = errors.New("invalid stage input")
)

// StageCanonicalThreadPointsFresh validates a prepared migration result and
// writes it into a newly-created Qdrant collection. It never reads, writes,
// aliases, or deletes a source collection. If a post-create operation fails,
// the fresh target is deliberately left untouched and unclaimed for the
// owner-level recovery workflow; this function never deletes it.
func StageCanonicalThreadPointsFresh(ctx context.Context, qdrantURL, targetCollection string, prepared threadmigration.QdrantPreparationResult) (receipt CanonicalThreadStageReceipt, returnedErr error) {
	receipt = canonicalThreadStageBaseReceipt(targetCollection)
	if ctxErr := canonicalThreadStageContextErr(ctx); ctxErr != nil {
		return canonicalThreadStageBlockedReceipt(receipt, canonicalThreadStageContextCode(ctxErr), ctxErr)
	}
	if err := prepared.Validate(); err != nil {
		return canonicalThreadStageBlockedReceipt(receipt, canonicalThreadStageErrorPrepared, err)
	}
	if prepared.Receipt.VectorDimension <= 0 {
		return canonicalThreadStageBlockedReceipt(receipt, canonicalThreadStageErrorDimension, errors.New("vector dimension is empty"))
	}
	if !validCanonicalThreadStageCollection(targetCollection) {
		return canonicalThreadStageBlockedReceipt(receipt, canonicalThreadStageErrorCollection, errCanonicalThreadStageInvalidInput)
	}
	host, port, ok := parseCanonicalThreadStageEndpoint(qdrantURL)
	if !ok {
		return canonicalThreadStageBlockedReceipt(receipt, canonicalThreadStageErrorURL, errCanonicalThreadStageInvalidInput)
	}
	points, batches, err := canonicalThreadStageBuildPoints(prepared.Points)
	if err != nil {
		return canonicalThreadStageBlockedReceipt(receipt, canonicalThreadStageErrorPointConversion, err)
	}
	receipt.PreparedCount = len(points)
	receipt.VectorDimension = prepared.Receipt.VectorDimension
	receipt.PreparedOutputSHA256 = prepared.Receipt.OutputSHA256
	receipt.MappingSHA256 = prepared.Plan.MappingSHA256

	if newCanonicalThreadStageClient == nil {
		return canonicalThreadStageBlockedReceipt(receipt, canonicalThreadStageErrorFactory, errCanonicalThreadStageInvalidInput)
	}
	client, err := newCanonicalThreadStageClient(&qdrant.Config{
		Host:                   host,
		Port:                   port,
		SkipCompatibilityCheck: true,
	})
	if err != nil || client == nil || legacyThreadCaptureNilInterface(client) {
		if err == nil {
			err = errCanonicalThreadStageInvalidInput
		}
		return canonicalThreadStageBlockedReceipt(receipt, canonicalThreadStageErrorFactory, err)
	}
	defer func() {
		if closeErr := client.Close(); closeErr != nil && returnedErr == nil {
			receipt, returnedErr = canonicalThreadStageBlockedReceipt(receipt, canonicalThreadStageErrorClose, closeErr)
		}
	}()

	if ctxErr := canonicalThreadStageContextErr(ctx); ctxErr != nil {
		return canonicalThreadStageBlockedReceipt(receipt, canonicalThreadStageContextCode(ctxErr), ctxErr)
	}
	exists, err := client.CollectionExists(ctx, targetCollection)
	if err != nil {
		return canonicalThreadStageBlockedReceipt(receipt, canonicalThreadStageOperationCode(ctx, canonicalThreadStageErrorProbe), err)
	}
	if exists {
		return canonicalThreadStageBlockedReceipt(receipt, canonicalThreadStageErrorTargetNotFresh, errors.New("target collection already exists"))
	}

	if ctxErr := canonicalThreadStageContextErr(ctx); ctxErr != nil {
		return canonicalThreadStageBlockedReceipt(receipt, canonicalThreadStageContextCode(ctxErr), ctxErr)
	}
	if err := client.CreateCollection(ctx, &qdrant.CreateCollection{
		CollectionName: targetCollection,
		VectorsConfig: qdrant.NewVectorsConfig(&qdrant.VectorParams{
			Size:     uint64(prepared.Receipt.VectorDimension),
			Distance: qdrant.Distance_Cosine,
		}),
	}); err != nil {
		return canonicalThreadStageBlockedReceipt(receipt, canonicalThreadStageOperationCode(ctx, canonicalThreadStageErrorCreate), err)
	}

	waitTrue := true
	for _, field := range []string{"session_id", "domain"} {
		if ctxErr := canonicalThreadStageContextErr(ctx); ctxErr != nil {
			return canonicalThreadStageBlockedReceipt(receipt, canonicalThreadStageContextCode(ctxErr), ctxErr)
		}
		if _, err := client.CreateFieldIndex(ctx, &qdrant.CreateFieldIndexCollection{
			CollectionName: targetCollection,
			Wait:           &waitTrue,
			FieldName:      field,
			FieldType:      qdrant.FieldType_FieldTypeKeyword.Enum(),
		}); err != nil {
			return canonicalThreadStageBlockedReceipt(receipt, canonicalThreadStageOperationCode(ctx, canonicalThreadStageErrorIndex), err)
		}
	}

	for _, batch := range batches {
		if ctxErr := canonicalThreadStageContextErr(ctx); ctxErr != nil {
			return canonicalThreadStageBlockedReceipt(receipt, canonicalThreadStageContextCode(ctxErr), ctxErr)
		}
		if _, err := client.Upsert(ctx, &qdrant.UpsertPoints{
			CollectionName: targetCollection,
			Wait:           &waitTrue,
			Points:         batch,
		}); err != nil {
			return canonicalThreadStageBlockedReceipt(receipt, canonicalThreadStageOperationCode(ctx, canonicalThreadStageErrorWrite), err)
		}
		receipt.StagedCount += len(batch)
	}

	if ctxErr := canonicalThreadStageContextErr(ctx); ctxErr != nil {
		return canonicalThreadStageBlockedReceipt(receipt, canonicalThreadStageContextCode(ctxErr), ctxErr)
	}
	exact := true
	count, err := client.Count(ctx, &qdrant.CountPoints{CollectionName: targetCollection, Exact: &exact})
	if err != nil {
		return canonicalThreadStageBlockedReceipt(receipt, canonicalThreadStageOperationCode(ctx, canonicalThreadStageErrorCount), err)
	}
	if count != uint64(len(points)) {
		receipt.StagedCount = boundedCanonicalThreadStageCount(count)
		return canonicalThreadStageBlockedReceipt(receipt, canonicalThreadStageErrorCountMismatch, fmt.Errorf("exact count %d does not match prepared count %d", count, len(points)))
	}

	readback, err := canonicalThreadStageReadback(ctx, client, targetCollection, len(points))
	if err != nil {
		return canonicalThreadStageBlockedReceipt(receipt, canonicalThreadStageOperationCode(ctx, canonicalThreadStageReadbackErrorCode(err)), err)
	}
	readbackResult := threadmigration.QdrantPreparationResult{
		Plan:    prepared.Plan,
		Points:  readback,
		Receipt: prepared.Receipt,
	}
	if err := readbackResult.Validate(); err != nil {
		return canonicalThreadStageBlockedReceipt(receipt, canonicalThreadStageErrorReadback, err)
	}
	if len(readbackResult.Points) != len(prepared.Points) || readbackResult.Receipt.OutputSHA256 != prepared.Receipt.OutputSHA256 || readbackResult.Receipt.MappingSHA256 != prepared.Plan.MappingSHA256 {
		return canonicalThreadStageBlockedReceipt(receipt, canonicalThreadStageErrorReadback, errors.New("readback does not bind prepared output"))
	}
	receipt.ReadbackOutputSHA256 = readbackResult.Receipt.OutputSHA256
	receipt.Status = CanonicalThreadStageStatusStagedNotActive
	receipt.ErrorCode = ""
	receipt = canonicalThreadStageSealReceipt(receipt)
	if err := receipt.Validate(); err != nil {
		return canonicalThreadStageBlockedReceipt(receipt, canonicalThreadStageErrorReceipt, err)
	}
	return receipt, nil
}

func canonicalThreadStageBaseReceipt(targetCollection string) CanonicalThreadStageReceipt {
	return CanonicalThreadStageReceipt{
		SchemaVersion:        CanonicalThreadStageReceiptSchemaVersion,
		Status:               CanonicalThreadStageStatusBlocked,
		TargetCollectionHash: canonicalThreadStageSHA256([]byte(targetCollection)),
	}
}

func canonicalThreadStageBlockedReceipt(receipt CanonicalThreadStageReceipt, code string, cause error) (CanonicalThreadStageReceipt, error) {
	receipt.Status = CanonicalThreadStageStatusBlocked
	receipt.ErrorCode = canonicalThreadStageSafeErrorCode(code)
	receipt.ReceiptSHA256 = ""
	receipt = canonicalThreadStageSealReceipt(receipt)
	return receipt, &canonicalThreadStageError{code: receipt.ErrorCode, cause: cause}
}

func canonicalThreadStageSealReceipt(receipt CanonicalThreadStageReceipt) CanonicalThreadStageReceipt {
	hash, err := receipt.ComputeSHA256()
	if err != nil {
		return receipt
	}
	receipt.ReceiptSHA256 = hash
	return receipt
}

func canonicalThreadStageSHA256(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func canonicalThreadStageSafeErrorCode(value string) string {
	if validCanonicalThreadStageErrorCode(value) {
		return value
	}
	return "stage_failed"
}

func canonicalThreadStageContextErr(ctx context.Context) error {
	if ctx == nil {
		return errCanonicalThreadStageInvalidContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func canonicalThreadStageContextCode(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return canonicalThreadStageErrorCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return canonicalThreadStageErrorDeadline
	default:
		return canonicalThreadStageErrorContext
	}
}

func canonicalThreadStageOperationCode(ctx context.Context, code string) string {
	if ctxErr := canonicalThreadStageContextErr(ctx); ctxErr != nil {
		return canonicalThreadStageContextCode(ctxErr)
	}
	return code
}

func boundedCanonicalThreadStageCount(count uint64) int {
	if count > uint64(threadmigration.QdrantPreparationMaxPoints) {
		return threadmigration.QdrantPreparationMaxPoints
	}
	return int(count)
}

func validCanonicalThreadStageCollection(collection string) bool {
	if !utf8.ValidString(collection) || collection == "" || strings.TrimSpace(collection) != collection || utf8.RuneCountInString(collection) > canonicalThreadStageMaxCollectionRunes {
		return false
	}
	for index, character := range collection {
		if index == 0 {
			if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9')) {
				return false
			}
			continue
		}
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '_' || character == '-') {
			return false
		}
	}
	return true
}

func parseCanonicalThreadStageEndpoint(endpoint string) (string, int, bool) {
	if !utf8.ValidString(endpoint) {
		return "", 0, false
	}
	host, portText, err := net.SplitHostPort(endpoint)
	if err != nil || !validCanonicalThreadStageHost(host) {
		return "", 0, false
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, false
	}
	return host, port, true
}

func validCanonicalThreadStageHost(host string) bool {
	if !utf8.ValidString(host) || host == "" || strings.TrimSpace(host) != host {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	if len(host) > 253 {
		return false
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if unicode.IsControl(character) || unicode.IsSpace(character) || ((character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '-') {
				return false
			}
		}
	}
	return true
}

type canonicalThreadStagePointBatch struct {
	points      []*qdrant.PointStruct
	approxBytes int
}

func canonicalThreadStageBuildPoints(input []threadmigration.QdrantPointSnapshot) ([]*qdrant.PointStruct, [][]*qdrant.PointStruct, error) {
	points := make([]*qdrant.PointStruct, 0, len(input))
	batches := make([][]*qdrant.PointStruct, 0, (len(input)+canonicalThreadStageMaxBatchPoints-1)/canonicalThreadStageMaxBatchPoints)
	current := canonicalThreadStagePointBatch{points: make([]*qdrant.PointStruct, 0, canonicalThreadStageMaxBatchPoints)}
	for index, snapshot := range input {
		point, err := canonicalThreadStagePoint(snapshot)
		if err != nil {
			return nil, nil, fmt.Errorf("point %d: %w", index, err)
		}
		approxBytes := canonicalThreadStageApproxPointBytes(snapshot)
		if approxBytes > canonicalThreadStageMaxBatchBytes {
			return nil, nil, errors.New("point exceeds staging batch byte bound")
		}
		if len(current.points) > 0 && (len(current.points) >= canonicalThreadStageMaxBatchPoints || current.approxBytes > canonicalThreadStageMaxBatchBytes-approxBytes) {
			batches = append(batches, current.points)
			current = canonicalThreadStagePointBatch{points: make([]*qdrant.PointStruct, 0, canonicalThreadStageMaxBatchPoints)}
		}
		current.points = append(current.points, point)
		current.approxBytes += approxBytes
		points = append(points, point)
	}
	if len(current.points) > 0 {
		batches = append(batches, current.points)
	}
	return points, batches, nil
}

func canonicalThreadStagePoint(snapshot threadmigration.QdrantPointSnapshot) (*qdrant.PointStruct, error) {
	parsedID, err := canonicalThreadStageUUID(snapshot.PointID)
	if err != nil {
		return nil, err
	}
	payload := make(map[string]*qdrant.Value, len(snapshot.Payload))
	for key, raw := range snapshot.Payload {
		if !utf8.ValidString(key) {
			return nil, errors.New("payload key is not valid UTF-8")
		}
		value, err := canonicalThreadStageJSONValue(raw)
		if err != nil {
			return nil, fmt.Errorf("payload field %q: %w", key, err)
		}
		payload[key] = value
	}
	return &qdrant.PointStruct{
		Id:      &qdrant.PointId{PointIdOptions: &qdrant.PointId_Uuid{Uuid: parsedID}},
		Vectors: qdrant.NewVectorsDense(append([]float32(nil), snapshot.Vector...)),
		Payload: payload,
	}, nil
}

func canonicalThreadStageUUID(value string) (string, error) {
	parsed, err := qdrantPointUUID(value)
	if err != nil {
		return "", errors.New("point ID is not a canonical UUID")
	}
	return parsed, nil
}

func qdrantPointUUID(value string) (string, error) {
	if value == "" {
		return "", errors.New("empty UUID")
	}
	// uuid.Parse is intentionally avoided here so this file keeps its own
	// narrow conversion contract independent of migration identity internals.
	parts := strings.Split(value, "-")
	if len(parts) != 5 || len(parts[0]) != 8 || len(parts[1]) != 4 || len(parts[2]) != 4 || len(parts[3]) != 4 || len(parts[4]) != 12 {
		return "", errors.New("invalid UUID shape")
	}
	for _, part := range parts {
		for _, character := range part {
			if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
				return "", errors.New("UUID is not lowercase hexadecimal")
			}
		}
	}
	return value, nil
}

func canonicalThreadStageApproxPointBytes(snapshot threadmigration.QdrantPointSnapshot) int {
	bytesCount := 64 + len(snapshot.PointID) + len(snapshot.Vector)*4
	for key, raw := range snapshot.Payload {
		bytesCount += len(key) + len(raw) + 8
	}
	return bytesCount
}

func canonicalThreadStageReadback(ctx context.Context, client canonicalThreadStageClient, collection string, expectedCount int) ([]threadmigration.QdrantPointSnapshot, error) {
	limit := uint32(canonicalThreadStageScrollPageLimit)
	points := make([]threadmigration.QdrantPointSnapshot, 0, expectedCount)
	seen := make(map[string]struct{}, expectedCount)
	var offset *qdrant.PointId
	var previousCursor string
	for {
		if ctxErr := canonicalThreadStageContextErr(ctx); ctxErr != nil {
			return nil, ctxErr
		}
		page, next, err := client.ScrollAndOffset(ctx, &qdrant.ScrollPoints{
			CollectionName: collection,
			Offset:         canonicalThreadStageClonePointID(offset),
			Limit:          &limit,
			WithPayload:    qdrant.NewWithPayload(true),
			WithVectors:    qdrant.NewWithVectors(true),
		})
		if err != nil {
			return nil, fmt.Errorf("scroll: %w", err)
		}
		if ctxErr := canonicalThreadStageContextErr(ctx); ctxErr != nil {
			return nil, ctxErr
		}
		if len(page) == 0 && next != nil {
			return nil, errors.New("scroll returned an empty page with a cursor")
		}
		if len(points)+len(page) > expectedCount {
			return nil, errors.New("readback point count exceeds prepared count")
		}
		pageIDs := make([]string, 0, len(page))
		for _, point := range page {
			snapshot, err := legacyThreadCapturePoint(point)
			if err != nil {
				return nil, fmt.Errorf("readback point: %w", err)
			}
			if _, exists := seen[snapshot.PointID]; exists {
				return nil, errors.New("readback contains duplicate point ID")
			}
			seen[snapshot.PointID] = struct{}{}
			pageIDs = append(pageIDs, snapshot.PointID)
			points = append(points, snapshot)
		}
		if next == nil {
			break
		}
		nextID, err := legacyThreadCapturePointID(next)
		if err != nil {
			return nil, errors.New("scroll cursor is not a canonical UUID")
		}
		if previousCursor != "" && nextID <= previousCursor {
			return nil, errors.New("scroll cursor did not advance")
		}
		if len(pageIDs) > 0 {
			pageMaximum := pageIDs[0]
			for _, pointID := range pageIDs[1:] {
				if pointID > pageMaximum {
					pageMaximum = pointID
				}
			}
			if nextID < pageMaximum {
				return nil, errors.New("scroll cursor precedes returned page")
			}
		}
		previousCursor = nextID
		offset = canonicalThreadStageClonePointID(next)
	}
	if len(points) != expectedCount {
		return nil, fmt.Errorf("readback point count %d does not match prepared count %d", len(points), expectedCount)
	}
	sort.Slice(points, func(left, right int) bool { return points[left].PointID < points[right].PointID })
	return points, nil
}

func canonicalThreadStageReadbackErrorCode(err error) string {
	if errors.Is(err, context.Canceled) {
		return canonicalThreadStageErrorCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return canonicalThreadStageErrorDeadline
	}
	return canonicalThreadStageErrorReadback
}

func canonicalThreadStageClonePointID(pointID *qdrant.PointId) *qdrant.PointId {
	if pointID == nil {
		return nil
	}
	switch value := pointID.GetPointIdOptions().(type) {
	case *qdrant.PointId_Uuid:
		if value == nil {
			return nil
		}
		return &qdrant.PointId{PointIdOptions: &qdrant.PointId_Uuid{Uuid: value.Uuid}}
	case *qdrant.PointId_Num:
		if value == nil {
			return nil
		}
		return &qdrant.PointId{PointIdOptions: &qdrant.PointId_Num{Num: value.Num}}
	default:
		return nil
	}
}

// canonicalThreadStageJSONValue is the strict inverse of the adapter-neutral
// JSON payload representation. JSON integers become Qdrant int64 values when
// representable; decimal/exponent forms become finite doubles. Objects and
// arrays are recursively converted with duplicate keys, trailing content,
// invalid UTF-8, and excessive depth rejected before a write is attempted.
func canonicalThreadStageJSONValue(raw json.RawMessage) (*qdrant.Value, error) {
	if len(raw) == 0 || !utf8.Valid(raw) || !json.Valid(raw) {
		return nil, errors.New("JSON value is empty, invalid UTF-8, or malformed")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := canonicalThreadStageDecodeJSONValue(decoder, 0)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return nil, errors.New("JSON value has trailing content")
		}
		return nil, fmt.Errorf("JSON value has trailing content: %w", err)
	}
	return value, nil
}

func canonicalThreadStageDecodeJSONValue(decoder *json.Decoder, depth int) (*qdrant.Value, error) {
	if depth > canonicalThreadStageMaxJSONDepth {
		return nil, fmt.Errorf("JSON value depth exceeds %d", canonicalThreadStageMaxJSONDepth)
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("decode JSON value: %w", err)
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			fields := make(map[string]*qdrant.Value)
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, fmt.Errorf("decode JSON object key: %w", err)
				}
				key, ok := keyToken.(string)
				if !ok || !utf8.ValidString(key) {
					return nil, errors.New("JSON object key is not a valid UTF-8 string")
				}
				if _, exists := seen[key]; exists {
					return nil, fmt.Errorf("duplicate JSON object key %q", key)
				}
				seen[key] = struct{}{}
				child, err := canonicalThreadStageDecodeJSONValue(decoder, depth+1)
				if err != nil {
					return nil, err
				}
				fields[key] = child
			}
			closeToken, err := decoder.Token()
			if err != nil || closeToken != json.Delim('}') {
				return nil, errors.New("JSON object is not closed")
			}
			return qdrant.NewValueFromFields(fields), nil
		case '[':
			values := make([]*qdrant.Value, 0)
			for decoder.More() {
				child, err := canonicalThreadStageDecodeJSONValue(decoder, depth+1)
				if err != nil {
					return nil, err
				}
				values = append(values, child)
			}
			closeToken, err := decoder.Token()
			if err != nil || closeToken != json.Delim(']') {
				return nil, errors.New("JSON array is not closed")
			}
			return qdrant.NewValueFromList(values...), nil
		default:
			return nil, fmt.Errorf("unexpected JSON delimiter %q", value)
		}
	case json.Number:
		return canonicalThreadStageJSONNumber(value)
	case string:
		if !utf8.ValidString(value) {
			return nil, errors.New("JSON string is not valid UTF-8")
		}
		return qdrant.NewValueString(value), nil
	case bool:
		return qdrant.NewValueBool(value), nil
	case nil:
		return qdrant.NewValueNull(), nil
	default:
		return nil, fmt.Errorf("unsupported JSON value type %T", token)
	}
}

func canonicalThreadStageJSONNumber(value json.Number) (*qdrant.Value, error) {
	raw := value.String()
	if !strings.ContainsAny(raw, ".eE") {
		integer, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, errors.New("JSON integer is outside int64 range")
		}
		return qdrant.NewValueInt(integer), nil
	}
	double, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(double) || math.IsInf(double, 0) {
		return nil, errors.New("JSON number is not a finite double")
	}
	return qdrant.NewValueDouble(double), nil
}
