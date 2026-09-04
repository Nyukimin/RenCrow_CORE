package threadmigration

// This file coordinates the in-memory planning and disposable SQLite writes
// for the Step 05 cohort. Filesystem activation, Redis, Qdrant, owner-schema
// reconciliation, and runtime cutover intentionally remain outside this
// operation, so success never means runtime-ready.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

const (
	SQLiteTopicCohortReceiptSchemaVersion = "rencrow.threadmigration.sqlite_topic_cohort.v1"
	SQLiteTopicCohortStatus               = "materialized_sqlite_topic_not_runtime_ready"
)

// SQLiteTopicCohortInput contains caller-owned immutable source handles and
// disposable destination handles. TopicSource is consumed into memory before
// either destination is mutated.
type SQLiteTopicCohortInput struct {
	L1Source           *sql.DB
	ArchiveSource      *sql.DB
	RawSource          *sql.DB
	L1Destination      *sql.DB
	ArchiveDestination *sql.DB
	TopicSource        io.Reader
	// AdditionalPlans are independently inventoried Redis, Qdrant, or other
	// Step 05 surfaces. Every plan is merged before either destination write.
	AdditionalPlans []Plan
}

// SQLiteTopicCohortReceipt cross-binds every deterministic child result. It
// carries only bounded counts and hashes, never paths, rows, or topic content.
type SQLiteTopicCohortReceipt struct {
	SchemaVersion string `json:"schema_version"`
	Status        string `json:"status"`

	SQLiteInventoryReceiptSHA256          string   `json:"sqlite_inventory_receipt_sha256"`
	MaterializationInventoryReceiptSHA256 string   `json:"materialization_inventory_receipt_sha256"`
	SQLitePlanSHA256                      string   `json:"sqlite_plan_sha256"`
	TopicPlanSHA256                       string   `json:"topic_plan_sha256"`
	AdditionalPlanSHA256                  []string `json:"additional_plan_sha256"`
	MergedMappingSHA256                   string   `json:"merged_mapping_sha256"`

	TopicSourceCount      int    `json:"topic_source_count"`
	TopicOutputCount      int    `json:"topic_output_count"`
	TopicQuarantineCount  int    `json:"topic_quarantine_count"`
	TopicSourceSHA256     string `json:"topic_source_sha256"`
	TopicOutputSHA256     string `json:"topic_output_sha256"`
	TopicQuarantineSHA256 string `json:"topic_quarantine_sha256"`

	L1MaterializationReceiptSHA256      string `json:"l1_materialization_receipt_sha256"`
	ArchiveMaterializationReceiptSHA256 string `json:"archive_materialization_receipt_sha256"`
	ReceiptSHA256                       string `json:"receipt_sha256"`
}

// SQLiteTopicCohortResult returns apply-ready topic bytes alongside the
// disposable SQLite receipts. The receipt deliberately excludes those bytes.
type SQLiteTopicCohortResult struct {
	SQLiteInventory SQLiteInventoryResult               `json:"sqlite_inventory"`
	Plan            Plan                                `json:"plan"`
	Topic           TopicStorePreparation               `json:"topic"`
	L1Receipt       SQLiteL1MaterializationReceipt      `json:"l1_receipt"`
	ArchiveReceipt  SQLiteArchiveMaterializationReceipt `json:"archive_receipt"`
	Receipt         SQLiteTopicCohortReceipt            `json:"receipt"`
}

// SQLiteTopicCohortError identifies the failed phase and which disposable
// destinations committed. A committed destination is never rolled back by
// this coordinator; the caller must discard the whole output cohort.
type SQLiteTopicCohortError struct {
	Code             string
	Phase            string
	L1Committed      bool
	ArchiveCommitted bool
	cause            error
}

func (err *SQLiteTopicCohortError) Error() string {
	if err == nil {
		return "<nil>"
	}
	return fmt.Sprintf("SQLite/topic cohort %s failed during %s", err.Code, err.Phase)
}

func (err *SQLiteTopicCohortError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

// MaterializeSQLiteTopicCohort inventories every immutable source, prepares
// the topic output, and merges the plans before the first destination write.
func MaterializeSQLiteTopicCohort(ctx context.Context, input SQLiteTopicCohortInput) (SQLiteTopicCohortResult, error) {
	if err := validateSQLiteTopicCohortInput(ctx, input); err != nil {
		return SQLiteTopicCohortResult{}, err
	}

	inventory, err := InventorySQLite(ctx, SQLiteInventoryInput{L1DB: input.L1Source, ArchiveDB: input.ArchiveSource, RawDB: input.RawSource})
	if err != nil {
		return SQLiteTopicCohortResult{}, cohortError("inventory_failed", "inventory", false, false, err)
	}
	topic, err := PrepareTopicStore(input.TopicSource)
	if err != nil {
		return SQLiteTopicCohortResult{}, cohortError("topic_preparation_failed", "topic_preparation", false, false, err)
	}
	return materializePreparedSQLiteTopicCohort(ctx, input, inventory, topic)
}

func validateSQLiteTopicCohortInput(ctx context.Context, input SQLiteTopicCohortInput) error {
	if ctx == nil {
		return cohortError("invalid_input", "preflight", false, false, errors.New("context is nil"))
	}
	if err := ctx.Err(); err != nil {
		return cohortError("context", "preflight", false, false, err)
	}
	if input.L1Source == nil || input.ArchiveSource == nil || input.L1Destination == nil || input.ArchiveDestination == nil || input.TopicSource == nil {
		return cohortError("invalid_input", "preflight", false, false, errors.New("all source and destination inputs are required"))
	}
	return nil
}

func materializePreparedSQLiteTopicCohort(ctx context.Context, input SQLiteTopicCohortInput, inventory SQLiteInventoryResult, topic TopicStorePreparation) (SQLiteTopicCohortResult, error) {
	if err := inventory.Validate(); err != nil {
		return SQLiteTopicCohortResult{}, cohortError("inventory_validation_failed", "plan_merge", false, false, err)
	}
	if err := topic.Plan.Validate(); err != nil {
		return SQLiteTopicCohortResult{}, cohortError("topic_validation_failed", "plan_merge", false, false, err)
	}
	plans := make([]Plan, 0, 2+len(input.AdditionalPlans))
	plans = append(plans, inventory.Plan, topic.Plan)
	plans = append(plans, input.AdditionalPlans...)
	additionalPlanHashes, err := sortedPlanHashes(input.AdditionalPlans)
	if err != nil {
		return SQLiteTopicCohortResult{}, cohortError("additional_plan_failed", "plan_merge", false, false, err)
	}
	merged, err := MergePlans(plans...)
	if err != nil {
		return SQLiteTopicCohortResult{}, cohortError("plan_merge_failed", "plan_merge", false, false, err)
	}
	materializationInventory, err := rebindSQLiteInventoryPlan(inventory, merged)
	if err != nil {
		return SQLiteTopicCohortResult{}, cohortError("inventory_rebind_failed", "plan_merge", false, false, err)
	}

	l1Receipt, err := MaterializeL1SQLite(ctx, L1SQLiteMaterializationInput{
		Source: input.L1Source, Destination: input.L1Destination, Inventory: materializationInventory,
	})
	if err != nil {
		return SQLiteTopicCohortResult{}, cohortError("l1_materialization_failed", "l1_materialization", l1MaterializationCommitted(err), false, err)
	}
	archiveReceipt, err := MaterializeArchiveSQLite(ctx, ArchiveSQLiteMaterializationInput{
		Source: input.ArchiveSource, Destination: input.ArchiveDestination, Inventory: materializationInventory,
	})
	if err != nil {
		return SQLiteTopicCohortResult{}, cohortError("archive_materialization_failed", "archive_materialization", true, archiveMaterializationCommitted(err), err)
	}

	receipt := SQLiteTopicCohortReceipt{
		SchemaVersion:                         SQLiteTopicCohortReceiptSchemaVersion,
		Status:                                SQLiteTopicCohortStatus,
		SQLiteInventoryReceiptSHA256:          inventory.Receipt.ReceiptSHA256,
		MaterializationInventoryReceiptSHA256: materializationInventory.Receipt.ReceiptSHA256,
		SQLitePlanSHA256:                      inventory.Plan.MappingSHA256,
		TopicPlanSHA256:                       topic.Plan.MappingSHA256,
		AdditionalPlanSHA256:                  additionalPlanHashes,
		MergedMappingSHA256:                   merged.MappingSHA256,
		TopicSourceCount:                      topic.SourceCount,
		TopicOutputCount:                      topic.OutputCount,
		TopicQuarantineCount:                  topic.QuarantineCount,
		TopicSourceSHA256:                     topic.SourceSHA256,
		TopicOutputSHA256:                     topic.OutputSHA256,
		TopicQuarantineSHA256:                 topic.QuarantineSHA256,
		L1MaterializationReceiptSHA256:        l1Receipt.ReceiptSHA256,
		ArchiveMaterializationReceiptSHA256:   archiveReceipt.ReceiptSHA256,
	}
	receiptHash, err := receipt.ComputeSHA256()
	if err != nil {
		return SQLiteTopicCohortResult{}, cohortError("receipt_hash_failed", "receipt", true, true, err)
	}
	receipt.ReceiptSHA256 = receiptHash
	result := SQLiteTopicCohortResult{SQLiteInventory: inventory, Plan: merged, Topic: topic, L1Receipt: l1Receipt, ArchiveReceipt: archiveReceipt, Receipt: receipt}
	if err := result.Validate(); err != nil {
		return SQLiteTopicCohortResult{}, cohortError("receipt_validation_failed", "receipt", true, true, err)
	}
	return result, nil
}

func sortedPlanHashes(plans []Plan) ([]string, error) {
	hashes := make([]string, 0, len(plans))
	for index, plan := range plans {
		if err := plan.Validate(); err != nil {
			return nil, fmt.Errorf("validate additional plan %d: %w", index, err)
		}
		hashes = append(hashes, plan.MappingSHA256)
	}
	sort.Strings(hashes)
	return hashes, nil
}

func rebindSQLiteInventoryPlan(inventory SQLiteInventoryResult, plan Plan) (SQLiteInventoryResult, error) {
	if err := inventory.Validate(); err != nil {
		return SQLiteInventoryResult{}, fmt.Errorf("validate source SQLite inventory: %w", err)
	}
	if err := plan.Validate(); err != nil {
		return SQLiteInventoryResult{}, fmt.Errorf("validate merged plan: %w", err)
	}
	rebound := inventory
	rebound.Plan = plan
	rebound.Receipt.MappingSHA256 = plan.MappingSHA256
	rebound.Receipt.ReceiptSHA256 = ""
	hash, err := rebound.Receipt.ComputeSHA256()
	if err != nil {
		return SQLiteInventoryResult{}, fmt.Errorf("hash rebound SQLite inventory receipt: %w", err)
	}
	rebound.Receipt.ReceiptSHA256 = hash
	if err := rebound.Validate(); err != nil {
		return SQLiteInventoryResult{}, fmt.Errorf("validate rebound SQLite inventory: %w", err)
	}
	return rebound, nil
}

func cohortError(code, phase string, l1Committed, archiveCommitted bool, cause error) error {
	return &SQLiteTopicCohortError{Code: code, Phase: phase, L1Committed: l1Committed, ArchiveCommitted: archiveCommitted, cause: cause}
}

func l1MaterializationCommitted(err error) bool {
	var typed *L1SQLiteMaterializationError
	return errors.As(err, &typed) && typed.PostCommit
}

func archiveMaterializationCommitted(err error) bool {
	var typed *ArchiveSQLiteMaterializationError
	return errors.As(err, &typed) && typed.PostCommit
}

func (receipt SQLiteTopicCohortReceipt) CanonicalJSON() ([]byte, error) {
	copy := receipt
	copy.ReceiptSHA256 = ""
	return json.Marshal(copy)
}

func (receipt SQLiteTopicCohortReceipt) ComputeSHA256() (string, error) {
	encoded, err := receipt.CanonicalJSON()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func (receipt SQLiteTopicCohortReceipt) Validate() error {
	if receipt.SchemaVersion != SQLiteTopicCohortReceiptSchemaVersion || receipt.Status != SQLiteTopicCohortStatus {
		return errors.New("invalid SQLite/topic cohort receipt schema or status")
	}
	if receipt.TopicSourceCount < 0 || receipt.TopicOutputCount < 0 || receipt.TopicQuarantineCount < 0 || receipt.TopicOutputCount+receipt.TopicQuarantineCount != receipt.TopicSourceCount {
		return errors.New("invalid topic cohort counts")
	}
	for label, hash := range map[string]string{
		"SQLite inventory":          receipt.SQLiteInventoryReceiptSHA256,
		"materialization inventory": receipt.MaterializationInventoryReceiptSHA256,
		"SQLite plan":               receipt.SQLitePlanSHA256,
		"topic plan":                receipt.TopicPlanSHA256,
		"merged mapping":            receipt.MergedMappingSHA256,
		"topic source":              receipt.TopicSourceSHA256,
		"topic output":              receipt.TopicOutputSHA256,
		"topic quarantine":          receipt.TopicQuarantineSHA256,
		"L1 materialization":        receipt.L1MaterializationReceiptSHA256,
		"archive materialization":   receipt.ArchiveMaterializationReceiptSHA256,
		"receipt":                   receipt.ReceiptSHA256,
	} {
		if len(hash) != sha256.Size*2 || hash != strings.ToLower(hash) {
			return fmt.Errorf("%s SHA256 is invalid", label)
		}
		if _, err := hex.DecodeString(hash); err != nil {
			return fmt.Errorf("%s SHA256 is invalid", label)
		}
	}
	for index, hash := range receipt.AdditionalPlanSHA256 {
		if index > 0 && receipt.AdditionalPlanSHA256[index-1] > hash {
			return errors.New("additional plan SHA256 values are not sorted")
		}
		if len(hash) != sha256.Size*2 || hash != strings.ToLower(hash) {
			return errors.New("additional plan SHA256 is invalid")
		}
		if _, err := hex.DecodeString(hash); err != nil {
			return errors.New("additional plan SHA256 is invalid")
		}
	}
	computed, err := receipt.ComputeSHA256()
	if err != nil {
		return err
	}
	if computed != receipt.ReceiptSHA256 {
		return errors.New("SQLite/topic cohort receipt SHA256 does not match canonical JSON")
	}
	return nil
}

func (result SQLiteTopicCohortResult) Validate() error {
	if err := result.SQLiteInventory.Validate(); err != nil {
		return fmt.Errorf("SQLite inventory: %w", err)
	}
	if err := result.Plan.Validate(); err != nil {
		return fmt.Errorf("merged plan: %w", err)
	}
	if err := result.L1Receipt.Validate(); err != nil {
		return fmt.Errorf("L1 receipt: %w", err)
	}
	if err := result.ArchiveReceipt.Validate(); err != nil {
		return fmt.Errorf("archive receipt: %w", err)
	}
	if err := result.Receipt.Validate(); err != nil {
		return fmt.Errorf("cohort receipt: %w", err)
	}
	if result.SQLiteInventory.Receipt.ReceiptSHA256 != result.Receipt.SQLiteInventoryReceiptSHA256 || result.SQLiteInventory.Plan.MappingSHA256 != result.Receipt.SQLitePlanSHA256 ||
		result.Topic.Plan.MappingSHA256 != result.Receipt.TopicPlanSHA256 || result.Plan.MappingSHA256 != result.Receipt.MergedMappingSHA256 {
		return errors.New("cohort plan hash binding mismatch")
	}
	rebound, err := rebindSQLiteInventoryPlan(result.SQLiteInventory, result.Plan)
	if err != nil {
		return fmt.Errorf("rebind materialization inventory: %w", err)
	}
	if rebound.Receipt.ReceiptSHA256 != result.Receipt.MaterializationInventoryReceiptSHA256 ||
		result.L1Receipt.InventoryReceiptSHA256 != rebound.Receipt.ReceiptSHA256 || result.ArchiveReceipt.InventoryReceiptSHA256 != rebound.Receipt.ReceiptSHA256 {
		return errors.New("cohort materialization inventory binding mismatch")
	}
	if result.Topic.SourceCount != result.Receipt.TopicSourceCount || result.Topic.OutputCount != result.Receipt.TopicOutputCount || result.Topic.QuarantineCount != result.Receipt.TopicQuarantineCount ||
		result.Topic.SourceSHA256 != result.Receipt.TopicSourceSHA256 || result.Topic.OutputSHA256 != result.Receipt.TopicOutputSHA256 || result.Topic.QuarantineSHA256 != result.Receipt.TopicQuarantineSHA256 {
		return errors.New("cohort topic receipt binding mismatch")
	}
	if digestBytesFromBytes(result.Topic.OutputJSONL) != result.Topic.OutputSHA256 || digestBytesFromBytes(result.Topic.QuarantineJSONL) != result.Topic.QuarantineSHA256 {
		return errors.New("cohort topic byte hash mismatch")
	}
	if result.L1Receipt.MappingSHA256 != result.Plan.MappingSHA256 || result.ArchiveReceipt.MappingSHA256 != result.Plan.MappingSHA256 ||
		result.L1Receipt.ReceiptSHA256 != result.Receipt.L1MaterializationReceiptSHA256 || result.ArchiveReceipt.ReceiptSHA256 != result.Receipt.ArchiveMaterializationReceiptSHA256 {
		return errors.New("cohort SQLite receipt binding mismatch")
	}
	return nil
}
