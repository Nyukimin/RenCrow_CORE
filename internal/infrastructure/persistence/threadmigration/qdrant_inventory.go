package threadmigration

// This file owns the read-only Step 05 Qdrant inventory boundary. It accepts
// a complete adapter-neutral point snapshot, validates the legacy identity
// fields, and returns an in-memory Plan plus bounded receipt. It never opens a
// Qdrant client, performs network or filesystem I/O, or changes runtime state.

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	QdrantInventoryReceiptSchemaVersion = "rencrow.threadmigration.qdrant_inventory.v1"
	QdrantInventoryStatus               = "inventoried_not_runtime_ready"
	QdrantInventoryPhase                = "step05_qdrant_inventory"
	QdrantInventorySurface              = "qdrant"

	QdrantInventoryMaxPoints       = QdrantPreparationMaxPoints
	QdrantInventoryMaxFacts        = QdrantInventoryMaxPoints
	QdrantInventoryMaxPayloadBytes = QdrantPreparationMaxPayloadBytes
)

// QdrantInventoryInput is the complete caller-owned legacy Qdrant snapshot.
// KnownPlan is consulted only to recognize already-known ChatGPT tuples and
// to reject a tuple that would be both ChatGPT and generic; Qdrant facts are
// always rebuilt into the returned Plan.
type QdrantInventoryInput struct {
	Phase     string                `json:"phase"`
	KnownPlan Plan                  `json:"known_plan"`
	Points    []QdrantPointSnapshot `json:"points"`
}

// QdrantInventoryReceipt binds the exact canonical source snapshot to the
// generated mapping plan. It intentionally contains no payload, message
// content, filesystem path, client handle, or runtime alias.
type QdrantInventoryReceipt struct {
	SchemaVersion       string `json:"schema_version"`
	Status              string `json:"status"`
	SourceCount         int    `json:"source_count"`
	FactCount           int    `json:"fact_count"`
	GenericMappingCount int    `json:"generic_mapping_count"`
	ChatGPTMappingCount int    `json:"chatgpt_mapping_count"`
	VectorDimension     int    `json:"vector_dimension"`
	SourceSHA256        string `json:"source_sha256"`
	MappingSHA256       string `json:"mapping_sha256"`
	ReceiptSHA256       string `json:"receipt_sha256"`
}

// QdrantInventoryResult is the complete in-memory inventory result. A later
// migration operation may merge Plan with other source plans; this operation
// itself does not write or activate anything.
type QdrantInventoryResult struct {
	Plan    Plan                   `json:"plan"`
	Receipt QdrantInventoryReceipt `json:"receipt"`
}

// InventoryQdrantPoints validates and inventories one bounded, complete
// legacy Qdrant point snapshot. Each source point contributes one
// LegacyThreadFact whose record key is its canonical source UUID.
func InventoryQdrantPoints(input QdrantInventoryInput) (QdrantInventoryResult, error) {
	if input.Phase != QdrantInventoryPhase {
		return QdrantInventoryResult{}, fmt.Errorf("Qdrant inventory phase %q is not %q", input.Phase, QdrantInventoryPhase)
	}
	if len(input.Points) > QdrantInventoryMaxPoints {
		return QdrantInventoryResult{}, fmt.Errorf("Qdrant source point count %d exceeds %d", len(input.Points), QdrantInventoryMaxPoints)
	}
	if err := input.KnownPlan.Validate(); err != nil {
		return QdrantInventoryResult{}, fmt.Errorf("validate known Qdrant mapping plan: %w", err)
	}

	facts := make([]LegacyThreadFact, 0, len(input.Points))
	seenPointIDs := make(map[string]struct{}, len(input.Points))
	vectorDimension := 0
	for index, point := range input.Points {
		sessionID, legacyThreadID, err := validateQdrantInventoryPoint(point, vectorDimension)
		if err != nil {
			return QdrantInventoryResult{}, fmt.Errorf("Qdrant source point %d: %w", index, err)
		}
		if index == 0 {
			vectorDimension = len(point.Vector)
		}
		if _, exists := seenPointIDs[point.PointID]; exists {
			return QdrantInventoryResult{}, fmt.Errorf("duplicate point ID %q in Qdrant source snapshot", point.PointID)
		}
		seenPointIDs[point.PointID] = struct{}{}

		fact, err := qdrantInventoryFact(input.KnownPlan, point.PointID, sessionID, legacyThreadID)
		if err != nil {
			return QdrantInventoryResult{}, fmt.Errorf("Qdrant source point %d: %w", index, err)
		}
		facts = append(facts, fact)
	}
	if len(facts) > QdrantInventoryMaxFacts {
		return QdrantInventoryResult{}, fmt.Errorf("Qdrant identity fact count %d exceeds %d", len(facts), QdrantInventoryMaxFacts)
	}

	plan, err := BuildPlan(facts)
	if err != nil {
		return QdrantInventoryResult{}, fmt.Errorf("build Qdrant legacy thread mapping plan: %w", err)
	}
	sourceJSON, err := qdrantCanonicalPointsJSON(input.Points)
	if err != nil {
		return QdrantInventoryResult{}, fmt.Errorf("hash Qdrant source snapshot: %w", err)
	}
	receipt := QdrantInventoryReceipt{
		SchemaVersion:       QdrantInventoryReceiptSchemaVersion,
		Status:              QdrantInventoryStatus,
		SourceCount:         len(input.Points),
		FactCount:           len(facts),
		GenericMappingCount: len(plan.Generic),
		ChatGPTMappingCount: len(plan.ChatGPT),
		VectorDimension:     vectorDimension,
		SourceSHA256:        qdrantSHA256(sourceJSON),
		MappingSHA256:       plan.MappingSHA256,
	}
	receipt.ReceiptSHA256, err = receipt.ComputeSHA256()
	if err != nil {
		return QdrantInventoryResult{}, fmt.Errorf("hash Qdrant inventory receipt: %w", err)
	}
	result := QdrantInventoryResult{Plan: plan, Receipt: receipt}
	if err := result.Validate(); err != nil {
		return QdrantInventoryResult{}, fmt.Errorf("validate Qdrant inventory result: %w", err)
	}
	return result, nil
}

func validateQdrantInventoryPoint(point QdrantPointSnapshot, expectedDimension int) (string, int64, error) {
	if err := validateQdrantSourcePointID(point.PointID); err != nil {
		return "", 0, err
	}
	if len(point.Vector) == 0 {
		return "", 0, errors.New("vector must not be empty")
	}
	if expectedDimension > 0 && len(point.Vector) != expectedDimension {
		return "", 0, fmt.Errorf("vector dimension %d is inconsistent with %d", len(point.Vector), expectedDimension)
	}
	for dimension, value := range point.Vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return "", 0, fmt.Errorf("vector value %d must be finite", dimension)
		}
	}
	if point.Payload == nil {
		return "", 0, errors.New("payload is required")
	}
	for key, raw := range point.Payload {
		if !utf8.ValidString(key) {
			return "", 0, fmt.Errorf("payload field %q is not valid UTF-8", key)
		}
		if _, err := qdrantCanonicalJSONValue(raw); err != nil {
			return "", 0, fmt.Errorf("payload field %q is not one JSON value: %w", key, err)
		}
	}
	payloadJSON, err := qdrantPayloadJSONBytes(point.Payload)
	if err != nil {
		return "", 0, fmt.Errorf("payload: %w", err)
	}
	if len(payloadJSON) > QdrantInventoryMaxPayloadBytes {
		return "", 0, fmt.Errorf("payload exceeds %d bytes", QdrantInventoryMaxPayloadBytes)
	}
	sessionID, err := qdrantRequiredJSONString(point.Payload, "session_id")
	if err != nil {
		return "", 0, err
	}
	if strings.TrimSpace(sessionID) == "" {
		return "", 0, errors.New("session_id must not be blank")
	}
	legacyThreadID, err := qdrantLegacyThreadID(point.Payload)
	if err != nil {
		return "", 0, err
	}
	if _, exists := point.Payload["thread_seq"]; exists {
		return "", 0, errors.New("wrong phase: preexisting thread_seq is not allowed")
	}
	if _, exists := point.Payload["thread_kind"]; exists {
		return "", 0, errors.New("wrong phase: preexisting thread_kind is not allowed")
	}
	return sessionID, legacyThreadID, nil
}

// qdrantInventoryFact classifies a source tuple using only exact historical
// Qdrant ChatGPT tuples. A generic known mapping is checked on the same
// canonical session conversion used by the preparation boundary, so a tuple
// that matches both categories fails closed instead of silently choosing one.
func qdrantInventoryFact(known Plan, pointID, sourceSessionID string, legacyThreadID int64) (LegacyThreadFact, error) {
	canonicalSessionID, err := canonicalGenericSessionID(sourceSessionID)
	if err != nil {
		return LegacyThreadFact{}, fmt.Errorf("canonicalize session_id %q: %w", sourceSessionID, err)
	}
	genericMatches := 0
	for _, mapping := range known.Generic {
		if string(mapping.SessionID) == canonicalSessionID && mapping.LegacyThreadID == legacyThreadID && mapping.ThreadSeq == modulecore.ThreadSeq(legacyThreadID) {
			genericMatches++
		}
	}

	chatGPTConversationID := ""
	chatGPTMatches := 0
	for _, mapping := range known.ChatGPT {
		chatGPTSessionID, chatGPTThreadID, err := qdrantHistoricalChatGPTTuple(mapping.ChatGPTConversationID)
		if err != nil {
			return LegacyThreadFact{}, fmt.Errorf("known ChatGPT mapping %q legacy tuple: %w", mapping.SemanticKey, err)
		}
		if chatGPTSessionID != sourceSessionID || chatGPTThreadID != legacyThreadID {
			continue
		}
		chatGPTMatches++
		chatGPTConversationID = mapping.ChatGPTConversationID
	}
	if chatGPTMatches > 1 {
		return LegacyThreadFact{}, fmt.Errorf("ambiguous Qdrant ChatGPT mapping for session_id %q and thread_id %d: %d mappings match", sourceSessionID, legacyThreadID, chatGPTMatches)
	}
	if genericMatches > 0 && chatGPTMatches == 1 {
		return LegacyThreadFact{}, fmt.Errorf("ambiguous known mapping for Qdrant tuple %q/%d", sourceSessionID, legacyThreadID)
	}
	if chatGPTMatches == 1 {
		return LegacyThreadFact{Surface: QdrantInventorySurface, RecordKey: pointID, ChatGPTConversationID: chatGPTConversationID}, nil
	}
	return LegacyThreadFact{Surface: QdrantInventorySurface, RecordKey: pointID, SessionID: sourceSessionID, LegacyThreadID: legacyThreadID}, nil
}

// CanonicalJSON returns the deterministic receipt payload. ReceiptSHA256 is
// excluded to avoid hashing a value that contains its own digest.
func (receipt QdrantInventoryReceipt) CanonicalJSON() ([]byte, error) {
	payload := struct {
		SchemaVersion       string `json:"schema_version"`
		Status              string `json:"status"`
		SourceCount         int    `json:"source_count"`
		FactCount           int    `json:"fact_count"`
		GenericMappingCount int    `json:"generic_mapping_count"`
		ChatGPTMappingCount int    `json:"chatgpt_mapping_count"`
		VectorDimension     int    `json:"vector_dimension"`
		SourceSHA256        string `json:"source_sha256"`
		MappingSHA256       string `json:"mapping_sha256"`
	}{
		SchemaVersion:       receipt.SchemaVersion,
		Status:              receipt.Status,
		SourceCount:         receipt.SourceCount,
		FactCount:           receipt.FactCount,
		GenericMappingCount: receipt.GenericMappingCount,
		ChatGPTMappingCount: receipt.ChatGPTMappingCount,
		VectorDimension:     receipt.VectorDimension,
		SourceSHA256:        receipt.SourceSHA256,
		MappingSHA256:       receipt.MappingSHA256,
	}
	return json.Marshal(payload)
}

// ComputeSHA256 computes the lowercase SHA-256 digest of CanonicalJSON.
func (receipt QdrantInventoryReceipt) ComputeSHA256() (string, error) {
	encoded, err := receipt.CanonicalJSON()
	if err != nil {
		return "", err
	}
	return qdrantSHA256(encoded), nil
}

// Validate checks receipt shape, bounded counts, digest syntax, and the
// self-referential receipt digest.
func (receipt QdrantInventoryReceipt) Validate() error {
	if receipt.SchemaVersion != QdrantInventoryReceiptSchemaVersion {
		return fmt.Errorf("unsupported Qdrant inventory receipt schema %q", receipt.SchemaVersion)
	}
	if receipt.Status != QdrantInventoryStatus {
		return fmt.Errorf("invalid Qdrant inventory receipt status %q", receipt.Status)
	}
	if receipt.SourceCount < 0 || receipt.SourceCount > QdrantInventoryMaxPoints || receipt.FactCount < 0 || receipt.FactCount > QdrantInventoryMaxFacts {
		return errors.New("Qdrant inventory receipt source or fact count is out of bounds")
	}
	if receipt.SourceCount != receipt.FactCount {
		return errors.New("Qdrant inventory receipt source and fact counts do not reconcile")
	}
	if receipt.GenericMappingCount < 0 || receipt.GenericMappingCount > QdrantInventoryMaxFacts || receipt.ChatGPTMappingCount < 0 || receipt.ChatGPTMappingCount > QdrantInventoryMaxFacts {
		return errors.New("Qdrant inventory receipt mapping counts are out of bounds")
	}
	if receipt.GenericMappingCount+receipt.ChatGPTMappingCount > receipt.FactCount {
		return errors.New("Qdrant inventory receipt mapping counts exceed fact count")
	}
	if receipt.VectorDimension < 0 {
		return errors.New("Qdrant inventory receipt vector dimension is negative")
	}
	if receipt.SourceCount > 0 && receipt.VectorDimension == 0 {
		return errors.New("Qdrant inventory receipt vector dimension is empty")
	}
	if receipt.SourceCount == 0 && receipt.VectorDimension != 0 {
		return errors.New("empty Qdrant inventory must have zero vector dimension")
	}
	for _, item := range []struct {
		value string
		label string
	}{
		{receipt.SourceSHA256, "source SHA256"},
		{receipt.MappingSHA256, "mapping SHA256"},
		{receipt.ReceiptSHA256, "receipt SHA256"},
	} {
		if err := validateQdrantSHA256(item.value, item.label); err != nil {
			return err
		}
	}
	computed, err := receipt.ComputeSHA256()
	if err != nil {
		return fmt.Errorf("compute Qdrant inventory receipt SHA256: %w", err)
	}
	if computed != receipt.ReceiptSHA256 {
		return errors.New("Qdrant inventory receipt SHA256 does not match canonical JSON")
	}
	return nil
}

// Validate checks the plan and all receipt-to-plan bindings. Source hash
// validation is performed before the result is built; the result carries no
// source payload, so this method deliberately cannot re-read or reconstruct
// that source snapshot.
func (result QdrantInventoryResult) Validate() error {
	if err := result.Plan.Validate(); err != nil {
		return fmt.Errorf("plan: %w", err)
	}
	if err := result.Receipt.Validate(); err != nil {
		return fmt.Errorf("receipt: %w", err)
	}
	if result.Receipt.MappingSHA256 != result.Plan.MappingSHA256 {
		return errors.New("receipt mapping SHA256 does not match plan")
	}
	if result.Receipt.GenericMappingCount != len(result.Plan.Generic) || result.Receipt.ChatGPTMappingCount != len(result.Plan.ChatGPT) {
		return errors.New("receipt mapping counts do not match plan")
	}
	factCount := 0
	for _, mapping := range append(append([]ThreadMapping{}, result.Plan.Generic...), result.Plan.ChatGPT...) {
		factCount += len(mapping.Sources)
	}
	if factCount != result.Receipt.FactCount || factCount != result.Receipt.SourceCount {
		return errors.New("receipt source or fact count does not match plan sources")
	}
	return nil
}
