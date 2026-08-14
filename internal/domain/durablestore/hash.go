package durablestore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

const StorageRequirementHashVersion = "storage-requirement/v1"

type canonicalStorageRequirement struct {
	RequestID            string   `json:"request_id"`
	TraceID              string   `json:"trace_id,omitempty"`
	RequestedBy          string   `json:"requested_by"`
	UserScope            string   `json:"user_scope,omitempty"`
	RequestedOutcome     string   `json:"requested_outcome"`
	FactsToStore         []string `json:"facts_to_store"`
	SourceSystems        []string `json:"source_systems,omitempty"`
	ReadPatterns         []string `json:"read_patterns,omitempty"`
	WritePatterns        []string `json:"write_patterns,omitempty"`
	RetentionExpectation string   `json:"retention_expectation,omitempty"`
	VolumeExpectation    string   `json:"volume_expectation,omitempty"`
	SensitivityHint      string   `json:"sensitivity_hint,omitempty"`
	OwnerHint            string   `json:"owner_hint,omitempty"`
	OwnerModule          string   `json:"owner_module,omitempty"`
	Acceptance           []string `json:"acceptance,omitempty"`
}

// HashStorageRequirement returns the deterministic payload hash used by the
// durable request receipt. Derived RequirementID and DedupeKey are excluded;
// trusted request identity and all semantic requirement fields are included.
func HashStorageRequirement(req StorageRequirement) string {
	canonical := canonicalStorageRequirement{
		RequestID:            strings.TrimSpace(req.RequestID),
		TraceID:              strings.TrimSpace(req.TraceID),
		RequestedBy:          strings.TrimSpace(req.RequestedBy),
		UserScope:            strings.TrimSpace(req.UserScope),
		RequestedOutcome:     normalizeHashText(string(req.RequestedOutcome)),
		FactsToStore:         normalizeHashList(req.FactsToStore),
		SourceSystems:        normalizeHashList(req.SourceSystems),
		ReadPatterns:         normalizeHashList(req.ReadPatterns),
		WritePatterns:        normalizeHashList(req.WritePatterns),
		RetentionExpectation: normalizeHashText(req.RetentionExpectation),
		VolumeExpectation:    normalizeHashText(req.VolumeExpectation),
		SensitivityHint:      normalizeHashText(req.SensitivityHint),
		OwnerHint:            strings.TrimSpace(req.OwnerHint),
		OwnerModule:          strings.TrimSpace(req.OwnerModule),
		Acceptance:           normalizeHashList(req.Acceptance),
	}
	encoded, _ := json.Marshal(struct {
		Version     string                      `json:"version"`
		Requirement canonicalStorageRequirement `json:"requirement"`
	}{Version: StorageRequirementHashVersion, Requirement: canonical})
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func ValidateRequestReceipt(receipt RequestReceipt) error {
	if strings.TrimSpace(receipt.RequestID) == "" {
		return fmt.Errorf("request_id is required")
	}
	if strings.TrimSpace(receipt.PayloadHash) == "" {
		return fmt.Errorf("payload_hash is required")
	}
	if strings.TrimSpace(receipt.RequirementID) == "" {
		return fmt.Errorf("requirement_id is required")
	}
	if receipt.CreatedAt.IsZero() {
		return fmt.Errorf("created_at is required")
	}
	return nil
}

func normalizeHashText(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
}

func normalizeHashList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = normalizeHashText(value)
	}
	return out
}
