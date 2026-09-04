package threadmigration

// This file contains the pure Step 05 Qdrant preparation boundary. It accepts
// adapter-neutral point snapshots and returns an in-memory canonical snapshot;
// it never opens a Qdrant client, performs network I/O, or changes runtime
// state. A later owner operation is responsible for applying the returned
// points after the receipt has been checked.

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	"github.com/google/uuid"
)

const (
	QdrantPreparationReceiptSchemaVersion = "rencrow.threadmigration.qdrant_preparation.v1"
	QdrantPreparationStatus               = "prepared_qdrant_not_runtime_ready"
	QdrantPreparationPhase                = "step05_qdrant_preparation"

	QdrantPreparationMaxPoints       = 10000
	QdrantPreparationMaxPayloadBytes = 1 << 20
	qdrantPreparationMaxJSONDepth    = 128
)

// QdrantPointSnapshot is the adapter-neutral representation of one source or
// prepared point. Payload values are individual JSON values, not an encoded
// JSON object. The transform owns neither the source nor the returned slices.
type QdrantPointSnapshot struct {
	PointID string                     `json:"point_id"`
	Vector  []float32                  `json:"vector"`
	Payload map[string]json.RawMessage `json:"payload"`
}

// QdrantPreparationInput is the complete immutable input to PrepareQdrantPoints.
// Phase must be QdrantPreparationPhase so a caller cannot accidentally use
// this migration-only transform as a runtime Qdrant route.
type QdrantPreparationInput struct {
	Phase  string                `json:"phase"`
	Plan   Plan                  `json:"plan"`
	Points []QdrantPointSnapshot `json:"points"`
}

// QdrantPreparationResult contains the validated plan and only in-memory
// output. Points are sorted by their canonical Qdrant UUID; duplicate source
// points are represented by one point and counted in the receipt.
type QdrantPreparationResult struct {
	Plan    Plan                     `json:"plan"`
	Points  []QdrantPointSnapshot    `json:"points"`
	Receipt QdrantPreparationReceipt `json:"receipt"`
}

// QdrantPreparationReceipt is deterministic evidence for the pure
// preparation. ReceiptSHA256 is computed over CanonicalJSON, which excludes
// the self-referential receipt hash.
type QdrantPreparationReceipt struct {
	SchemaVersion        string `json:"schema_version"`
	Status               string `json:"status"`
	SourceCount          int    `json:"source_count"`
	OutputCount          int    `json:"output_count"`
	DuplicateSourceCount int    `json:"duplicate_source_count"`
	VectorDimension      int    `json:"vector_dimension"`
	SourceSHA256         string `json:"source_sha256"`
	OutputSHA256         string `json:"output_sha256"`
	MappingSHA256        string `json:"mapping_sha256"`
	ReceiptSHA256        string `json:"receipt_sha256"`
}

// PrepareQdrantPoints validates and transforms one bounded source snapshot.
// It fails before returning any points when a source, mapping, or output
// invariant is not satisfied.
func PrepareQdrantPoints(input QdrantPreparationInput) (QdrantPreparationResult, error) {
	if input.Phase != QdrantPreparationPhase {
		return QdrantPreparationResult{}, fmt.Errorf("Qdrant preparation phase %q is not %q", input.Phase, QdrantPreparationPhase)
	}
	if len(input.Points) > QdrantPreparationMaxPoints {
		return QdrantPreparationResult{}, fmt.Errorf("Qdrant source point count %d exceeds %d", len(input.Points), QdrantPreparationMaxPoints)
	}
	if err := input.Plan.Validate(); err != nil {
		return QdrantPreparationResult{}, fmt.Errorf("validate Qdrant preparation plan: %w", err)
	}

	source := make([]qdrantValidatedPoint, 0, len(input.Points))
	seenPointIDs := make(map[string]struct{}, len(input.Points))
	vectorDimension := 0
	for index, point := range input.Points {
		validated, err := validateAndPrepareQdrantPoint(input.Plan, point, vectorDimension)
		if err != nil {
			return QdrantPreparationResult{}, fmt.Errorf("Qdrant source point %d: %w", index, err)
		}
		if index == 0 {
			vectorDimension = len(validated.source.Vector)
		}
		if _, exists := seenPointIDs[validated.source.PointID]; exists {
			return QdrantPreparationResult{}, fmt.Errorf("duplicate point ID %q in source snapshot", validated.source.PointID)
		}
		seenPointIDs[validated.source.PointID] = struct{}{}
		source = append(source, validated)
	}

	sourceSnapshots := make([]QdrantPointSnapshot, 0, len(source))
	for _, point := range source {
		sourceSnapshots = append(sourceSnapshots, point.source)
	}
	sourceJSON, err := qdrantCanonicalPointsJSON(sourceSnapshots)
	if err != nil {
		return QdrantPreparationResult{}, fmt.Errorf("encode Qdrant source snapshot: %w", err)
	}

	groups := make(map[string]qdrantPreparedGroup, len(source))
	for _, point := range source {
		outputID := point.output.PointID
		group, exists := groups[outputID]
		if !exists {
			group = qdrantPreparedGroup{point: point}
			groups[outputID] = group
			continue
		}
		if !qdrantPreparedPointsEquivalent(group.point, point) {
			return QdrantPreparationResult{}, fmt.Errorf("canonical point UUID %q has different vector or nonidentity payload", outputID)
		}
		group.duplicateCount++
		if qdrantPreparedPointLess(point, group.point) {
			group.point = point
		}
		groups[outputID] = group
	}

	output := make([]QdrantPointSnapshot, 0, len(groups))
	duplicateSourceCount := 0
	for _, group := range groups {
		output = append(output, group.point.output)
		duplicateSourceCount += group.duplicateCount
	}
	sort.Slice(output, func(left, right int) bool { return output[left].PointID < output[right].PointID })

	outputJSON, err := qdrantCanonicalPointsJSON(output)
	if err != nil {
		return QdrantPreparationResult{}, fmt.Errorf("encode Qdrant output snapshot: %w", err)
	}
	receipt := QdrantPreparationReceipt{
		SchemaVersion:        QdrantPreparationReceiptSchemaVersion,
		Status:               QdrantPreparationStatus,
		SourceCount:          len(source),
		OutputCount:          len(output),
		DuplicateSourceCount: duplicateSourceCount,
		VectorDimension:      vectorDimension,
		SourceSHA256:         qdrantSHA256(sourceJSON),
		OutputSHA256:         qdrantSHA256(outputJSON),
		MappingSHA256:        input.Plan.MappingSHA256,
	}
	receiptHash, err := receipt.ComputeSHA256()
	if err != nil {
		return QdrantPreparationResult{}, fmt.Errorf("hash Qdrant preparation receipt: %w", err)
	}
	receipt.ReceiptSHA256 = receiptHash
	result := QdrantPreparationResult{Plan: input.Plan, Points: output, Receipt: receipt}
	if err := result.Validate(); err != nil {
		return QdrantPreparationResult{}, fmt.Errorf("validate Qdrant preparation result: %w", err)
	}
	return result, nil
}

type qdrantValidatedPoint struct {
	source               QdrantPointSnapshot
	output               QdrantPointSnapshot
	nonIdentityCanonical map[string][]byte
	outputPayloadBytes   []byte
}

type qdrantPreparedGroup struct {
	point          qdrantValidatedPoint
	duplicateCount int
}

func validateAndPrepareQdrantPoint(plan Plan, point QdrantPointSnapshot, expectedDimension int) (qdrantValidatedPoint, error) {
	if err := validateQdrantSourcePointID(point.PointID); err != nil {
		return qdrantValidatedPoint{}, err
	}
	if len(point.Vector) == 0 {
		return qdrantValidatedPoint{}, errors.New("vector must not be empty")
	}
	if expectedDimension > 0 && len(point.Vector) != expectedDimension {
		return qdrantValidatedPoint{}, fmt.Errorf("vector dimension %d is inconsistent with %d", len(point.Vector), expectedDimension)
	}
	for dimension, value := range point.Vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return qdrantValidatedPoint{}, fmt.Errorf("vector value %d must be finite", dimension)
		}
	}

	if point.Payload == nil {
		return qdrantValidatedPoint{}, errors.New("payload is required")
	}
	for key, raw := range point.Payload {
		if _, err := qdrantCanonicalJSONValue(raw); err != nil {
			return qdrantValidatedPoint{}, fmt.Errorf("payload field %q is not one JSON value: %w", key, err)
		}
	}
	sourcePayloadBytes, err := qdrantPayloadJSONBytes(point.Payload)
	if err != nil {
		return qdrantValidatedPoint{}, fmt.Errorf("source payload: %w", err)
	}
	if len(sourcePayloadBytes) > QdrantPreparationMaxPayloadBytes {
		return qdrantValidatedPoint{}, fmt.Errorf("source payload exceeds %d bytes", QdrantPreparationMaxPayloadBytes)
	}

	sourceSession, err := qdrantRequiredJSONString(point.Payload, "session_id")
	if err != nil {
		return qdrantValidatedPoint{}, err
	}
	if strings.TrimSpace(sourceSession) == "" {
		return qdrantValidatedPoint{}, errors.New("session_id must not be blank")
	}
	legacyThreadID, err := qdrantLegacyThreadID(point.Payload)
	if err != nil {
		return qdrantValidatedPoint{}, err
	}
	if _, exists := point.Payload["thread_seq"]; exists {
		return qdrantValidatedPoint{}, errors.New("wrong phase: preexisting thread_seq is not allowed")
	}
	if _, exists := point.Payload["thread_kind"]; exists {
		return qdrantValidatedPoint{}, errors.New("wrong phase: preexisting thread_kind is not allowed")
	}

	mapping, err := resolveQdrantMapping(plan, sourceSession, legacyThreadID)
	if err != nil {
		return qdrantValidatedPoint{}, err
	}
	if err := mapping.ThreadID.Validate(); err != nil {
		return qdrantValidatedPoint{}, fmt.Errorf("mapping thread_id %q: %w", mapping.ThreadID, err)
	}
	outputPointID, err := qdrantCanonicalPointID(mapping.ThreadID)
	if err != nil {
		return qdrantValidatedPoint{}, err
	}

	outputPayload := make(map[string]json.RawMessage, len(point.Payload)+2)
	nonIdentityCanonical := make(map[string][]byte, len(point.Payload))
	for key, raw := range point.Payload {
		if key == "session_id" || key == "thread_id" || key == "thread_seq" || key == "thread_kind" {
			continue
		}
		outputPayload[key] = cloneQdrantRawMessage(raw)
		canonical, err := qdrantCanonicalJSONValue(raw)
		if err != nil {
			return qdrantValidatedPoint{}, fmt.Errorf("canonicalize payload field %q: %w", key, err)
		}
		nonIdentityCanonical[key] = canonical
	}
	outputPayload["session_id"] = qdrantMarshalJSONValue(mapping.SessionID)
	outputPayload["thread_id"] = qdrantMarshalJSONValue(mapping.ThreadID)
	outputPayload["thread_seq"] = qdrantMarshalJSONValue(mapping.ThreadSeq)
	outputPayload["thread_kind"] = qdrantMarshalJSONValue(mapping.ThreadKind)
	outputPayloadBytes, err := qdrantPayloadJSONBytes(outputPayload)
	if err != nil {
		return qdrantValidatedPoint{}, fmt.Errorf("output payload: %w", err)
	}
	if len(outputPayloadBytes) > QdrantPreparationMaxPayloadBytes {
		return qdrantValidatedPoint{}, fmt.Errorf("output payload exceeds %d bytes", QdrantPreparationMaxPayloadBytes)
	}

	return qdrantValidatedPoint{
		source: QdrantPointSnapshot{
			PointID: point.PointID,
			Vector:  append([]float32(nil), point.Vector...),
			Payload: cloneQdrantPayload(point.Payload),
		},
		output: QdrantPointSnapshot{
			PointID: outputPointID,
			Vector:  append([]float32(nil), point.Vector...),
			Payload: outputPayload,
		},
		nonIdentityCanonical: nonIdentityCanonical,
		outputPayloadBytes:   outputPayloadBytes,
	}, nil
}

// resolveQdrantMapping binds one legacy payload tuple to exactly one Plan
// mapping. Generic mappings use the canonicalized source session; ChatGPT
// mappings have no legacy fields in the Plan, so their historical Qdrant tuple
// is recomputed from the conversation ID and compared byte-for-byte.
func resolveQdrantMapping(plan Plan, sourceSessionID string, legacyThreadID int64) (ThreadMapping, error) {
	canonicalSessionID, err := canonicalGenericSessionID(sourceSessionID)
	if err != nil {
		return ThreadMapping{}, fmt.Errorf("canonicalize session_id %q: %w", sourceSessionID, err)
	}
	candidates := make([]ThreadMapping, 0, 2)
	if mapping, ok := plan.LookupGeneric(canonicalSessionID, legacyThreadID); ok {
		if mapping.ChatGPTConversationID == "" && string(mapping.SessionID) == canonicalSessionID && mapping.LegacyThreadID == legacyThreadID && mapping.ThreadSeq == modulecore.ThreadSeq(legacyThreadID) {
			candidates = append(candidates, mapping)
		}
	}
	for _, mapping := range plan.ChatGPT {
		chatGPTSessionID, chatGPTThreadID, err := qdrantHistoricalChatGPTTuple(mapping.ChatGPTConversationID)
		if err != nil {
			return ThreadMapping{}, fmt.Errorf("ChatGPT mapping %q legacy tuple: %w", mapping.SemanticKey, err)
		}
		if chatGPTSessionID == sourceSessionID && chatGPTThreadID == legacyThreadID {
			candidates = append(candidates, cloneMapping(mapping))
		}
	}
	switch len(candidates) {
	case 0:
		return ThreadMapping{}, fmt.Errorf("no mapping for session_id %q and thread_id %d", sourceSessionID, legacyThreadID)
	case 1:
		return candidates[0], nil
	default:
		return ThreadMapping{}, fmt.Errorf("ambiguous mapping for session_id %q and thread_id %d", sourceSessionID, legacyThreadID)
	}
}

// qdrantHistoricalChatGPTTuple is the legacy tuple written by the old vector
// projection. It is intentionally separate from chatGPTLegacyTuple, which is
// the SQLite raw-import tuple and has a different historical session field.
func qdrantHistoricalChatGPTTuple(conversationID string) (string, int64, error) {
	if conversationID == "" {
		return "", 0, errors.New("ChatGPT conversation ID is empty")
	}
	digest := sha256.Sum256([]byte("chatgpt-conv:" + conversationID))
	threadID := int64(binary.BigEndian.Uint64(digest[:8]) & math.MaxInt64)
	if threadID == 0 {
		threadID = 1
	}
	return "chatgpt_export", threadID, nil
}

func qdrantPreparedPointsEquivalent(left, right qdrantValidatedPoint) bool {
	if len(left.output.Vector) != len(right.output.Vector) {
		return false
	}
	for index := range left.output.Vector {
		if math.Float32bits(left.output.Vector[index]) != math.Float32bits(right.output.Vector[index]) {
			return false
		}
	}
	if len(left.nonIdentityCanonical) != len(right.nonIdentityCanonical) {
		return false
	}
	for key, leftValue := range left.nonIdentityCanonical {
		if !bytes.Equal(leftValue, right.nonIdentityCanonical[key]) {
			return false
		}
	}
	return true
}

func qdrantPreparedPointLess(left, right qdrantValidatedPoint) bool {
	if comparison := bytes.Compare(left.outputPayloadBytes, right.outputPayloadBytes); comparison != 0 {
		return comparison < 0
	}
	return left.source.PointID < right.source.PointID
}

func validateQdrantSourcePointID(pointID string) error {
	if pointID == "" {
		return errors.New("point ID must be a UUID")
	}
	parsed, err := uuid.Parse(pointID)
	if err != nil || parsed.String() != pointID {
		return fmt.Errorf("point ID %q must be a canonical UUID", pointID)
	}
	return nil
}

func qdrantCanonicalPointID(threadID modulecore.ThreadID) (string, error) {
	raw := string(threadID)
	if !strings.HasPrefix(raw, "thr_") {
		return "", fmt.Errorf("canonical thread ID %q must use thr_ prefix", threadID)
	}
	pointID := strings.TrimPrefix(raw, "thr_")
	parsed, err := uuid.Parse(pointID)
	if err != nil || parsed.String() != pointID {
		return "", fmt.Errorf("canonical thread ID %q does not contain a canonical UUID", threadID)
	}
	return pointID, nil
}

func qdrantRequiredJSONString(payload map[string]json.RawMessage, key string) (string, error) {
	raw, exists := payload[key]
	if !exists {
		return "", fmt.Errorf("payload field %q is required", key)
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", fmt.Errorf("payload field %q must be a JSON string", key)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("payload field %q must be a JSON string", key)
	}
	return value, nil
}

func qdrantLegacyThreadID(payload map[string]json.RawMessage) (int64, error) {
	raw, exists := payload["thread_id"]
	if !exists {
		return 0, errors.New("payload field \"thread_id\" is required")
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] < '1' || trimmed[0] > '9' {
		return 0, errors.New("payload field \"thread_id\" must be a canonical positive integer")
	}
	for _, character := range trimmed[1:] {
		if character < '0' || character > '9' {
			return 0, errors.New("payload field \"thread_id\" must be a canonical positive integer")
		}
	}
	value, err := strconv.ParseInt(string(trimmed), 10, 64)
	if err != nil || value <= 0 {
		return 0, errors.New("payload field \"thread_id\" must be a canonical positive integer")
	}
	return value, nil
}

func qdrantMarshalJSONValue(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("marshal validated Qdrant identity value: %v", err))
	}
	return json.RawMessage(encoded)
}

func cloneQdrantRawMessage(raw json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), raw...)
}

func cloneQdrantPayload(payload map[string]json.RawMessage) map[string]json.RawMessage {
	clone := make(map[string]json.RawMessage, len(payload))
	for key, raw := range payload {
		clone[key] = cloneQdrantRawMessage(raw)
	}
	return clone
}

func qdrantCanonicalPointsJSON(points []QdrantPointSnapshot) ([]byte, error) {
	ordered := make([]QdrantPointSnapshot, len(points))
	for index, point := range points {
		ordered[index] = QdrantPointSnapshot{
			PointID: point.PointID,
			Vector:  append([]float32(nil), point.Vector...),
			Payload: cloneQdrantPayload(point.Payload),
		}
	}
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].PointID < ordered[right].PointID })

	encoded := []byte{'['}
	for index, point := range ordered {
		if index > 0 {
			encoded = append(encoded, ',')
		}
		var err error
		encoded, err = qdrantAppendPointJSON(encoded, point)
		if err != nil {
			return nil, err
		}
	}
	encoded = append(encoded, ']')
	return encoded, nil
}

func qdrantAppendPointJSON(destination []byte, point QdrantPointSnapshot) ([]byte, error) {
	if err := validateQdrantSourcePointID(point.PointID); err != nil {
		return nil, err
	}
	destination = append(destination, '{')
	destination = append(destination, []byte(`"point_id":`)...)
	pointID, _ := json.Marshal(point.PointID)
	destination = append(destination, pointID...)
	destination = append(destination, []byte(`,"vector":[`)...)
	for index, value := range point.Vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return nil, fmt.Errorf("vector value %d must be finite", index)
		}
		if index > 0 {
			destination = append(destination, ',')
		}
		destination = strconv.AppendFloat(destination, float64(value), 'g', -1, 32)
	}
	destination = append(destination, []byte(`],"payload":`)...)
	payload, err := qdrantPayloadJSONBytes(point.Payload)
	if err != nil {
		return nil, err
	}
	destination = append(destination, payload...)
	destination = append(destination, '}')
	return destination, nil
}

func qdrantPayloadJSONBytes(payload map[string]json.RawMessage) ([]byte, error) {
	keys := make([]string, 0, len(payload))
	for key, raw := range payload {
		if _, err := qdrantCanonicalJSONValue(raw); err != nil {
			return nil, fmt.Errorf("payload field %q is not one JSON value: %w", key, err)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	encoded := []byte{'{'}
	for index, key := range keys {
		if index > 0 {
			encoded = append(encoded, ',')
		}
		encodedKey, err := json.Marshal(key)
		if err != nil {
			return nil, fmt.Errorf("marshal payload key %q: %w", key, err)
		}
		encoded = append(encoded, encodedKey...)
		encoded = append(encoded, ':')
		encoded = append(encoded, payload[key]...)
	}
	encoded = append(encoded, '}')
	return encoded, nil
}

func qdrantSHA256(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

// CanonicalJSON returns the deterministic receipt payload. ReceiptSHA256 is
// excluded to avoid hashing a value that contains its own digest.
func (receipt QdrantPreparationReceipt) CanonicalJSON() ([]byte, error) {
	payload := struct {
		SchemaVersion        string `json:"schema_version"`
		Status               string `json:"status"`
		SourceCount          int    `json:"source_count"`
		OutputCount          int    `json:"output_count"`
		DuplicateSourceCount int    `json:"duplicate_source_count"`
		VectorDimension      int    `json:"vector_dimension"`
		SourceSHA256         string `json:"source_sha256"`
		OutputSHA256         string `json:"output_sha256"`
		MappingSHA256        string `json:"mapping_sha256"`
	}{
		SchemaVersion:        receipt.SchemaVersion,
		Status:               receipt.Status,
		SourceCount:          receipt.SourceCount,
		OutputCount:          receipt.OutputCount,
		DuplicateSourceCount: receipt.DuplicateSourceCount,
		VectorDimension:      receipt.VectorDimension,
		SourceSHA256:         receipt.SourceSHA256,
		OutputSHA256:         receipt.OutputSHA256,
		MappingSHA256:        receipt.MappingSHA256,
	}
	return json.Marshal(payload)
}

// ComputeSHA256 computes the lowercase SHA-256 of CanonicalJSON.
func (receipt QdrantPreparationReceipt) ComputeSHA256() (string, error) {
	encoded, err := receipt.CanonicalJSON()
	if err != nil {
		return "", err
	}
	return qdrantSHA256(encoded), nil
}

// Validate checks receipt schema, bounded counts, digest syntax, and the
// self-referential receipt digest.
func (receipt QdrantPreparationReceipt) Validate() error {
	if receipt.SchemaVersion != QdrantPreparationReceiptSchemaVersion {
		return fmt.Errorf("unsupported Qdrant preparation receipt schema %q", receipt.SchemaVersion)
	}
	if receipt.Status != QdrantPreparationStatus {
		return fmt.Errorf("invalid Qdrant preparation receipt status %q", receipt.Status)
	}
	if receipt.SourceCount < 0 || receipt.SourceCount > QdrantPreparationMaxPoints || receipt.OutputCount < 0 || receipt.OutputCount > QdrantPreparationMaxPoints || receipt.DuplicateSourceCount < 0 || receipt.DuplicateSourceCount > QdrantPreparationMaxPoints {
		return errors.New("Qdrant preparation receipt counts are out of bounds")
	}
	if receipt.OutputCount+receipt.DuplicateSourceCount != receipt.SourceCount {
		return errors.New("Qdrant preparation receipt counts do not reconcile")
	}
	if receipt.SourceCount > 0 && receipt.OutputCount == 0 {
		return errors.New("Qdrant preparation receipt has source points but no output points")
	}
	if receipt.VectorDimension < 0 {
		return errors.New("Qdrant preparation receipt vector dimension is negative")
	}
	if receipt.SourceCount > 0 && receipt.VectorDimension == 0 {
		return errors.New("Qdrant preparation receipt vector dimension is empty")
	}
	for _, item := range []struct {
		value string
		label string
	}{
		{receipt.SourceSHA256, "source SHA256"},
		{receipt.OutputSHA256, "output SHA256"},
		{receipt.MappingSHA256, "mapping SHA256"},
		{receipt.ReceiptSHA256, "receipt SHA256"},
	} {
		if err := validateQdrantSHA256(item.value, item.label); err != nil {
			return err
		}
	}
	computed, err := receipt.ComputeSHA256()
	if err != nil {
		return fmt.Errorf("compute Qdrant preparation receipt SHA256: %w", err)
	}
	if computed != receipt.ReceiptSHA256 {
		return errors.New("Qdrant preparation receipt SHA256 does not match canonical JSON")
	}
	return nil
}

func validateQdrantSHA256(value, label string) error {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return fmt.Errorf("%s is not lowercase hexadecimal SHA-256", label)
	}
	if _, err := hex.DecodeString(value); err != nil {
		return fmt.Errorf("%s is not lowercase hexadecimal SHA-256", label)
	}
	return nil
}

// Validate checks the output shape and binds it to the validated migration
// plan carried by the result.
func (result QdrantPreparationResult) Validate() error {
	if err := result.Receipt.Validate(); err != nil {
		return fmt.Errorf("receipt: %w", err)
	}
	if len(result.Points) != result.Receipt.OutputCount {
		return fmt.Errorf("output point count %d does not match receipt %d", len(result.Points), result.Receipt.OutputCount)
	}
	if result.Receipt.OutputCount == 0 && result.Receipt.VectorDimension != 0 {
		return errors.New("empty Qdrant output must have zero vector dimension")
	}
	if err := validateQdrantOutputPoints(result.Points, result.Receipt.VectorDimension); err != nil {
		return err
	}
	encoded, err := qdrantCanonicalPointsJSON(result.Points)
	if err != nil {
		return fmt.Errorf("encode output points: %w", err)
	}
	if got := qdrantSHA256(encoded); got != result.Receipt.OutputSHA256 {
		return errors.New("output SHA256 does not match returned points")
	}
	plan := result.Plan
	if err := plan.Validate(); err != nil {
		return fmt.Errorf("plan: %w", err)
	}
	if result.Receipt.MappingSHA256 != plan.MappingSHA256 {
		return errors.New("receipt mapping SHA256 does not match plan")
	}
	expected := make(map[string]ThreadMapping, len(plan.Generic)+len(plan.ChatGPT))
	for _, mapping := range append(append([]ThreadMapping{}, plan.Generic...), plan.ChatGPT...) {
		pointID, err := qdrantCanonicalPointID(mapping.ThreadID)
		if err != nil {
			return err
		}
		expected[pointID] = mapping
	}
	for _, point := range result.Points {
		mapping, ok := expected[point.PointID]
		if !ok {
			return fmt.Errorf("output point %q has no mapping", point.PointID)
		}
		if err := validateQdrantOutputIdentity(point, mapping); err != nil {
			return err
		}
	}
	return nil
}

func validateQdrantOutputPoints(points []QdrantPointSnapshot, expectedDimension int) error {
	seen := make(map[string]struct{}, len(points))
	for index, point := range points {
		if err := validateQdrantSourcePointID(point.PointID); err != nil {
			return fmt.Errorf("output point %d: %w", index, err)
		}
		if _, exists := seen[point.PointID]; exists {
			return fmt.Errorf("duplicate output point ID %q", point.PointID)
		}
		seen[point.PointID] = struct{}{}
		if index > 0 && points[index-1].PointID >= point.PointID {
			return errors.New("output points are not sorted by PointID")
		}
		if len(point.Vector) == 0 || len(point.Vector) != expectedDimension {
			return fmt.Errorf("output point %q has inconsistent vector dimension", point.PointID)
		}
		for dimension, value := range point.Vector {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return fmt.Errorf("output point %q vector value %d must be finite", point.PointID, dimension)
			}
		}
		if point.Payload == nil {
			return fmt.Errorf("output point %q payload is nil", point.PointID)
		}
		payload, err := qdrantPayloadJSONBytes(point.Payload)
		if err != nil {
			return fmt.Errorf("output point %q: %w", point.PointID, err)
		}
		if len(payload) > QdrantPreparationMaxPayloadBytes {
			return fmt.Errorf("output point %q payload exceeds %d bytes", point.PointID, QdrantPreparationMaxPayloadBytes)
		}
		if _, err := qdrantRequiredJSONString(point.Payload, "session_id"); err != nil {
			return fmt.Errorf("output point %q: %w", point.PointID, err)
		}
		threadID, err := qdrantRequiredJSONString(point.Payload, "thread_id")
		if err != nil || !strings.HasPrefix(threadID, "thr_") {
			return fmt.Errorf("output point %q: payload field \"thread_id\" must be a canonical ThreadID", point.PointID)
		}
		if _, err := qdrantCanonicalPointID(modulecore.ThreadID(threadID)); err != nil {
			return fmt.Errorf("output point %q: %w", point.PointID, err)
		}
		if _, err := qdrantRequiredPositiveJSONInteger(point.Payload, "thread_seq"); err != nil {
			return fmt.Errorf("output point %q: %w", point.PointID, err)
		}
		kind, err := qdrantRequiredJSONString(point.Payload, "thread_kind")
		if err != nil {
			return fmt.Errorf("output point %q: %w", point.PointID, err)
		}
		if err := modulecore.ThreadKind(kind).Validate(); err != nil {
			return fmt.Errorf("output point %q: thread_kind: %w", point.PointID, err)
		}
	}
	return nil
}

func validateQdrantOutputIdentity(point QdrantPointSnapshot, mapping ThreadMapping) error {
	session, err := qdrantRequiredJSONString(point.Payload, "session_id")
	if err != nil || session != string(mapping.SessionID) {
		return fmt.Errorf("output point %q session_id does not match mapping", point.PointID)
	}
	thread, err := qdrantRequiredJSONString(point.Payload, "thread_id")
	if err != nil || thread != string(mapping.ThreadID) {
		return fmt.Errorf("output point %q thread_id does not match mapping", point.PointID)
	}
	seq, err := qdrantRequiredPositiveJSONInteger(point.Payload, "thread_seq")
	if err != nil || seq != int64(mapping.ThreadSeq) {
		return fmt.Errorf("output point %q thread_seq does not match mapping", point.PointID)
	}
	kind, err := qdrantRequiredJSONString(point.Payload, "thread_kind")
	if err != nil || kind != string(mapping.ThreadKind) {
		return fmt.Errorf("output point %q thread_kind does not match mapping", point.PointID)
	}
	return nil
}

func qdrantRequiredPositiveJSONInteger(payload map[string]json.RawMessage, key string) (int64, error) {
	raw, exists := payload[key]
	if !exists {
		return 0, fmt.Errorf("payload field %q is required", key)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] < '1' || trimmed[0] > '9' {
		return 0, fmt.Errorf("payload field %q must be a positive integer", key)
	}
	for _, character := range trimmed[1:] {
		if character < '0' || character > '9' {
			return 0, fmt.Errorf("payload field %q must be a positive integer", key)
		}
	}
	value, err := strconv.ParseInt(string(trimmed), 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("payload field %q must be a positive integer", key)
	}
	return value, nil
}

// qdrantCanonicalJSONValue returns a deterministic comparison form for one
// JSON value. Object keys are sorted, duplicate keys are rejected, and JSON
// numbers are compared by exact rational value so 1 and 1.0 are equivalent.
func qdrantCanonicalJSONValue(raw []byte) ([]byte, error) {
	if len(raw) == 0 || !utf8.Valid(raw) {
		return nil, errors.New("JSON value is empty or not valid UTF-8")
	}
	if len(bytes.TrimSpace(raw)) == 0 || !json.Valid(raw) {
		return nil, errors.New("JSON value is malformed")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeQdrantJSONValue(decoder, 0)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return nil, errors.New("JSON value has trailing content")
		}
		return nil, fmt.Errorf("JSON value has trailing content: %w", err)
	}
	return encodeQdrantComparableJSON(value)
}

func decodeQdrantJSONValue(decoder *json.Decoder, depth int) (any, error) {
	if depth > qdrantPreparationMaxJSONDepth {
		return nil, fmt.Errorf("JSON value depth exceeds %d", qdrantPreparationMaxJSONDepth)
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("decode JSON value: %w", err)
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			object := make(map[string]any)
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, fmt.Errorf("decode JSON object key: %w", err)
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, errors.New("JSON object key is not a string")
				}
				if _, exists := seen[key]; exists {
					return nil, fmt.Errorf("duplicate JSON object key %q", key)
				}
				seen[key] = struct{}{}
				child, err := decodeQdrantJSONValue(decoder, depth+1)
				if err != nil {
					return nil, err
				}
				object[key] = child
			}
			closeToken, err := decoder.Token()
			if err != nil || closeToken != json.Delim('}') {
				return nil, errors.New("JSON object is not closed")
			}
			return object, nil
		case '[':
			array := make([]any, 0)
			for decoder.More() {
				child, err := decodeQdrantJSONValue(decoder, depth+1)
				if err != nil {
					return nil, err
				}
				array = append(array, child)
			}
			closeToken, err := decoder.Token()
			if err != nil || closeToken != json.Delim(']') {
				return nil, errors.New("JSON array is not closed")
			}
			return array, nil
		default:
			return nil, fmt.Errorf("unexpected JSON delimiter %q", value)
		}
	case json.Number, string, bool:
		return value, nil
	case nil:
		return nil, nil
	default:
		return nil, fmt.Errorf("unsupported JSON value type %T", token)
	}
}

func encodeQdrantComparableJSON(value any) ([]byte, error) {
	switch value := value.(type) {
	case nil:
		return []byte("null"), nil
	case bool:
		if value {
			return []byte("true"), nil
		}
		return []byte("false"), nil
	case string:
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		return append([]byte("s"), encoded...), nil
	case json.Number:
		return qdrantCanonicalJSONNumber(string(value))
	case []any:
		encoded := []byte("a")
		for _, child := range value {
			childEncoded, err := encodeQdrantComparableJSON(child)
			if err != nil {
				return nil, err
			}
			encoded = qdrantAppendLengthPrefixed(encoded, childEncoded)
		}
		return encoded, nil
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		encoded := []byte("o")
		for _, key := range keys {
			encoded = qdrantAppendLengthPrefixed(encoded, []byte(key))
			childEncoded, err := encodeQdrantComparableJSON(value[key])
			if err != nil {
				return nil, err
			}
			encoded = qdrantAppendLengthPrefixed(encoded, childEncoded)
		}
		return encoded, nil
	default:
		return nil, fmt.Errorf("unsupported comparable JSON value type %T", value)
	}
}

func qdrantCanonicalJSONNumber(raw string) ([]byte, error) {
	sign := ""
	if strings.HasPrefix(raw, "-") {
		sign = "-"
		raw = raw[1:]
	}
	mantissa, exponentText := raw, "0"
	if index := strings.IndexAny(raw, "eE"); index >= 0 {
		mantissa, exponentText = raw[:index], raw[index+1:]
	}
	exponent := new(big.Int)
	if _, ok := exponent.SetString(exponentText, 10); !ok {
		return nil, fmt.Errorf("JSON number %q has an invalid exponent", raw)
	}
	integerPart, fractionPart := mantissa, ""
	if index := strings.IndexByte(mantissa, '.'); index >= 0 {
		integerPart, fractionPart = mantissa[:index], mantissa[index+1:]
	}
	digits := strings.TrimLeft(integerPart+fractionPart, "0")
	if digits == "" {
		return []byte("n0"), nil
	}
	trailingZeros := len(integerPart+fractionPart) - len(strings.TrimRight(integerPart+fractionPart, "0"))
	digits = strings.TrimRight(digits, "0")
	if digits == "" {
		return []byte("n0"), nil
	}
	scale := new(big.Int).Set(exponent)
	scale.Sub(scale, big.NewInt(int64(len(fractionPart))))
	scale.Add(scale, big.NewInt(int64(trailingZeros)))
	return []byte("n" + sign + digits + ":" + scale.String()), nil
}

func qdrantAppendLengthPrefixed(destination, value []byte) []byte {
	destination = strconv.AppendInt(destination, int64(len(value)), 10)
	destination = append(destination, ':')
	return append(destination, value...)
}
