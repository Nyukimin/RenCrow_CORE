package threadmigration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	FullCohortReceiptSchemaVersion = "rencrow.threadmigration.full_cohort.v1"
	FullCohortStatus               = "prepared_full_cohort_not_runtime_ready"
)

// FullCohortInput contains complete caller-owned snapshots and disposable
// SQLite destinations. It has no Redis/Qdrant clients and activates nothing.
type FullCohortInput struct {
	SQLiteTopic SQLiteTopicCohortInput
	Redis       []RedisEntry
	Qdrant      []QdrantPointSnapshot
}

type FullCohortReceipt struct {
	SchemaVersion string `json:"schema_version"`
	Status        string `json:"status"`

	SQLiteTopicReceiptSHA256       string `json:"sqlite_topic_receipt_sha256"`
	RedisInventoryReceiptSHA256    string `json:"redis_inventory_receipt_sha256"`
	QdrantInventoryReceiptSHA256   string `json:"qdrant_inventory_receipt_sha256"`
	RedisPreparationReceiptSHA256  string `json:"redis_preparation_receipt_sha256"`
	QdrantPreparationReceiptSHA256 string `json:"qdrant_preparation_receipt_sha256"`
	MergedMappingSHA256            string `json:"merged_mapping_sha256"`
	ReceiptSHA256                  string `json:"receipt_sha256"`
}

type FullCohortResult struct {
	Plan              Plan                    `json:"plan"`
	SQLiteTopic       SQLiteTopicCohortResult `json:"sqlite_topic"`
	RedisInventory    RedisInventoryResult    `json:"redis_inventory"`
	QdrantInventory   QdrantInventoryResult   `json:"qdrant_inventory"`
	RedisPreparation  RedisPreparationResult  `json:"redis_preparation"`
	QdrantPreparation QdrantPreparationResult `json:"qdrant_preparation"`
	Receipt           FullCohortReceipt       `json:"receipt"`
}

// PrepareFullCohort inventories every source and performs all in-memory
// transformations before the first disposable SQLite destination write.
func PrepareFullCohort(ctx context.Context, input FullCohortInput) (FullCohortResult, error) {
	if len(input.SQLiteTopic.AdditionalPlans) != 0 {
		return FullCohortResult{}, errors.New("full cohort owns additional plan ordering")
	}
	if err := validateSQLiteTopicCohortInput(ctx, input.SQLiteTopic); err != nil {
		return FullCohortResult{}, err
	}
	sqliteInventory, err := InventorySQLite(ctx, SQLiteInventoryInput{
		L1DB: input.SQLiteTopic.L1Source, ArchiveDB: input.SQLiteTopic.ArchiveSource, RawDB: input.SQLiteTopic.RawSource,
	})
	if err != nil {
		return FullCohortResult{}, fmt.Errorf("full cohort SQLite inventory: %w", err)
	}
	topic, err := PrepareTopicStore(input.SQLiteTopic.TopicSource)
	if err != nil {
		return FullCohortResult{}, fmt.Errorf("full cohort topic preparation: %w", err)
	}
	basePlan, err := MergePlans(sqliteInventory.Plan, topic.Plan)
	if err != nil {
		return FullCohortResult{}, fmt.Errorf("full cohort base plan: %w", err)
	}
	redisInventory, err := InventoryRedisProjection(RedisInventoryInput{
		Phase: RedisInventoryPhase, KnownPlan: basePlan, Entries: input.Redis,
	})
	if err != nil {
		return FullCohortResult{}, fmt.Errorf("full cohort Redis inventory: %w", err)
	}
	redisKnownPlan, err := MergePlans(basePlan, redisInventory.Plan)
	if err != nil {
		return FullCohortResult{}, fmt.Errorf("full cohort Redis plan merge: %w", err)
	}
	qdrantInventory, err := InventoryQdrantPoints(QdrantInventoryInput{
		Phase: QdrantInventoryPhase, KnownPlan: redisKnownPlan, Points: input.Qdrant,
	})
	if err != nil {
		return FullCohortResult{}, fmt.Errorf("full cohort Qdrant inventory: %w", err)
	}
	plan, err := MergePlans(redisKnownPlan, qdrantInventory.Plan)
	if err != nil {
		return FullCohortResult{}, fmt.Errorf("full cohort final plan merge: %w", err)
	}

	redisPreparation, err := PrepareRedisProjection(RedisPreparationInput{
		Phase: RedisPreparationPhase, Plan: plan, Entries: input.Redis,
	})
	if err != nil {
		return FullCohortResult{}, fmt.Errorf("full cohort Redis preparation: %w", err)
	}
	qdrantPreparation, err := PrepareQdrantPoints(QdrantPreparationInput{
		Phase: QdrantPreparationPhase, Plan: plan, Points: input.Qdrant,
	})
	if err != nil {
		return FullCohortResult{}, fmt.Errorf("full cohort Qdrant preparation: %w", err)
	}

	sqliteInput := input.SQLiteTopic
	sqliteInput.AdditionalPlans = []Plan{redisInventory.Plan, qdrantInventory.Plan}
	sqliteTopic, err := materializePreparedSQLiteTopicCohort(ctx, sqliteInput, sqliteInventory, topic)
	if err != nil {
		return FullCohortResult{}, err
	}
	if sqliteTopic.Plan.MappingSHA256 != plan.MappingSHA256 {
		return FullCohortResult{}, errors.New("full cohort materialization plan mismatch")
	}

	receipt := FullCohortReceipt{
		SchemaVersion:                  FullCohortReceiptSchemaVersion,
		Status:                         FullCohortStatus,
		SQLiteTopicReceiptSHA256:       sqliteTopic.Receipt.ReceiptSHA256,
		RedisInventoryReceiptSHA256:    redisInventory.Receipt.ReceiptSHA256,
		QdrantInventoryReceiptSHA256:   qdrantInventory.Receipt.ReceiptSHA256,
		RedisPreparationReceiptSHA256:  redisPreparation.Receipt.ReceiptSHA256,
		QdrantPreparationReceiptSHA256: qdrantPreparation.Receipt.ReceiptSHA256,
		MergedMappingSHA256:            plan.MappingSHA256,
	}
	receipt.ReceiptSHA256, err = receipt.ComputeSHA256()
	if err != nil {
		return FullCohortResult{}, err
	}
	result := FullCohortResult{
		Plan: plan, SQLiteTopic: sqliteTopic,
		RedisInventory: redisInventory, QdrantInventory: qdrantInventory,
		RedisPreparation: redisPreparation, QdrantPreparation: qdrantPreparation,
		Receipt: receipt,
	}
	if err := result.Validate(); err != nil {
		return FullCohortResult{}, err
	}
	return result, nil
}

func (receipt FullCohortReceipt) CanonicalJSON() ([]byte, error) {
	copy := receipt
	copy.ReceiptSHA256 = ""
	return json.Marshal(copy)
}

func (receipt FullCohortReceipt) ComputeSHA256() (string, error) {
	encoded, err := receipt.CanonicalJSON()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func (receipt FullCohortReceipt) Validate() error {
	if receipt.SchemaVersion != FullCohortReceiptSchemaVersion || receipt.Status != FullCohortStatus {
		return errors.New("invalid full cohort receipt schema or status")
	}
	for _, hash := range []string{
		receipt.SQLiteTopicReceiptSHA256, receipt.RedisInventoryReceiptSHA256,
		receipt.QdrantInventoryReceiptSHA256, receipt.RedisPreparationReceiptSHA256,
		receipt.QdrantPreparationReceiptSHA256, receipt.MergedMappingSHA256, receipt.ReceiptSHA256,
	} {
		if len(hash) != sha256.Size*2 || hash != strings.ToLower(hash) {
			return errors.New("invalid full cohort receipt SHA256")
		}
		if _, err := hex.DecodeString(hash); err != nil {
			return errors.New("invalid full cohort receipt SHA256")
		}
	}
	computed, err := receipt.ComputeSHA256()
	if err != nil {
		return err
	}
	if computed != receipt.ReceiptSHA256 {
		return errors.New("full cohort receipt SHA256 does not match canonical JSON")
	}
	return nil
}

func (result FullCohortResult) Validate() error {
	if err := result.Plan.Validate(); err != nil {
		return err
	}
	if err := result.SQLiteTopic.Validate(); err != nil {
		return err
	}
	if err := result.RedisInventory.Validate(); err != nil {
		return err
	}
	if err := result.QdrantInventory.Validate(); err != nil {
		return err
	}
	if err := result.RedisPreparation.Validate(); err != nil {
		return err
	}
	if err := result.QdrantPreparation.Validate(); err != nil {
		return err
	}
	if err := result.Receipt.Validate(); err != nil {
		return err
	}
	if result.Receipt.MergedMappingSHA256 != result.Plan.MappingSHA256 ||
		result.SQLiteTopic.Plan.MappingSHA256 != result.Plan.MappingSHA256 ||
		result.RedisPreparation.Plan.MappingSHA256 != result.Plan.MappingSHA256 ||
		result.QdrantPreparation.Plan.MappingSHA256 != result.Plan.MappingSHA256 {
		return errors.New("full cohort plan binding mismatch")
	}
	merged, err := MergePlans(
		result.SQLiteTopic.SQLiteInventory.Plan,
		result.SQLiteTopic.Topic.Plan,
		result.RedisInventory.Plan,
		result.QdrantInventory.Plan,
	)
	if err != nil || merged.MappingSHA256 != result.Plan.MappingSHA256 {
		return errors.New("full cohort plan is not the exact merge of child inventories")
	}
	additionalHashes, err := sortedPlanHashes([]Plan{result.RedisInventory.Plan, result.QdrantInventory.Plan})
	if err != nil || len(additionalHashes) != len(result.SQLiteTopic.Receipt.AdditionalPlanSHA256) {
		return errors.New("full cohort additional plan receipt binding mismatch")
	}
	for index := range additionalHashes {
		if additionalHashes[index] != result.SQLiteTopic.Receipt.AdditionalPlanSHA256[index] {
			return errors.New("full cohort additional plan receipt binding mismatch")
		}
	}
	if result.RedisInventory.Receipt.SourceSHA256 != result.RedisPreparation.Receipt.SourceSHA256 ||
		result.QdrantInventory.Receipt.SourceSHA256 != result.QdrantPreparation.Receipt.SourceSHA256 {
		return errors.New("full cohort source snapshot hash binding mismatch")
	}
	if result.Receipt.SQLiteTopicReceiptSHA256 != result.SQLiteTopic.Receipt.ReceiptSHA256 ||
		result.Receipt.RedisInventoryReceiptSHA256 != result.RedisInventory.Receipt.ReceiptSHA256 ||
		result.Receipt.QdrantInventoryReceiptSHA256 != result.QdrantInventory.Receipt.ReceiptSHA256 ||
		result.Receipt.RedisPreparationReceiptSHA256 != result.RedisPreparation.Receipt.ReceiptSHA256 ||
		result.Receipt.QdrantPreparationReceiptSHA256 != result.QdrantPreparation.Receipt.ReceiptSHA256 {
		return errors.New("full cohort child receipt binding mismatch")
	}
	return nil
}
