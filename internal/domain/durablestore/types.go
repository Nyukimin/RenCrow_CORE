package durablestore

import "time"

type RequestedOutcome string

const (
	OutcomeAssess    RequestedOutcome = "assess"
	OutcomeImplement RequestedOutcome = "implement"
)

type StorageClass string

const (
	ClassEphemeral      StorageClass = "ephemeral"
	ClassCache          StorageClass = "cache"
	ClassArtifact       StorageClass = "artifact"
	ClassExistingMemory StorageClass = "existing_memory"
	ClassExistingStore  StorageClass = "existing_store"
	ClassNewStore       StorageClass = "new_store"
)

type Status string

const (
	StatusCompleted Status = "completed"
	StatusRejected  Status = "rejected"
	StatusBlocked   Status = "blocked"
)

type Lifecycle string

const (
	LifecycleProposed    Lifecycle = "proposed"
	LifecycleValidated   Lifecycle = "validated"
	LifecycleImplemented Lifecycle = "implemented"
	LifecycleProvisioned Lifecycle = "provisioned"
	LifecycleActive      Lifecycle = "active"
)

type ChangeClass string

const (
	ChangeS0 ChangeClass = "S0"
	ChangeS1 ChangeClass = "S1"
	ChangeS2 ChangeClass = "S2"
)

type StorageRequirement struct {
	RequirementID        string           `json:"requirement_id"`
	DedupeKey            string           `json:"dedupe_key"`
	RequestID            string           `json:"request_id"`
	TraceID              string           `json:"trace_id,omitempty"`
	RequestedBy          string           `json:"requested_by"`
	UserScope            string           `json:"user_scope,omitempty"`
	RequestedOutcome     RequestedOutcome `json:"requested_outcome"`
	FactsToStore         []string         `json:"facts_to_store"`
	SourceSystems        []string         `json:"source_systems,omitempty"`
	ReadPatterns         []string         `json:"read_patterns,omitempty"`
	WritePatterns        []string         `json:"write_patterns,omitempty"`
	RetentionExpectation string           `json:"retention_expectation,omitempty"`
	VolumeExpectation    string           `json:"volume_expectation,omitempty"`
	SensitivityHint      string           `json:"sensitivity_hint,omitempty"`
	OwnerHint            string           `json:"owner_hint,omitempty"`
	OwnerModule          string           `json:"owner_module,omitempty"`
	Acceptance           []string         `json:"acceptance,omitempty"`
}

type StoreManifest struct {
	StoreID                string      `json:"store_id"`
	OwnerModule            string      `json:"owner_module"`
	StoreKind              string      `json:"store_kind"`
	DurabilityClass        string      `json:"durability_class"`
	DataClasses            []string    `json:"data_classes"`
	CanonicalConfigKeys    []string    `json:"canonical_config_keys"`
	ProductionRootTemplate string      `json:"production_root_template"`
	AuthoritativeWriter    string      `json:"authoritative_writer"`
	Readers                []string    `json:"readers"`
	SchemaRevision         string      `json:"schema_revision"`
	MigrationOwner         string      `json:"migration_owner"`
	RetentionPolicy        string      `json:"retention_policy"`
	BackupProfile          string      `json:"backup_profile"`
	RestoreCheck           string      `json:"restore_check"`
	RPO                    string      `json:"rpo"`
	RTO                    string      `json:"rto"`
	Sensitivity            string      `json:"sensitivity"`
	FallbackPolicy         string      `json:"fallback_policy"`
	ChangeClass            ChangeClass `json:"change_class"`
	ProposalRevision       int         `json:"proposal_revision"`
	ParentAttemptID        string      `json:"parent_attempt_id,omitempty"`
	ChangedDimensions      []string    `json:"changed_dimensions,omitempty"`
	LifecycleStatus        Lifecycle   `json:"lifecycle_status"`
	RebuildSource          string      `json:"rebuild_source,omitempty"`
	RebuildCheck           string      `json:"rebuild_check,omitempty"`
}

type Manifest struct {
	ContractVersion string          `json:"contract_version"`
	ModuleID        string          `json:"module_id"`
	Stores          []StoreManifest `json:"stores"`
}

type Classification struct {
	Class       StorageClass `json:"class"`
	ChangeClass ChangeClass  `json:"change_class,omitempty"`
	StoreID     string       `json:"store_id,omitempty"`
	OwnerModule string       `json:"owner_module,omitempty"`
	Status      Status       `json:"status,omitempty"`
	Reason      string       `json:"reason"`
}

type StorageProposal struct {
	ProposalID       string        `json:"proposal_id"`
	RequirementID    string        `json:"requirement_id"`
	OwnerModule      string        `json:"owner_module"`
	Class            StorageClass  `json:"class"`
	ChangeClass      ChangeClass   `json:"change_class"`
	ProposalRevision int           `json:"proposal_revision"`
	TargetStoreID    string        `json:"target_store_id,omitempty"`
	Manifest         StoreManifest `json:"manifest"`
	ValidationPassed bool          `json:"validation_passed"`
	ValidationErrors []string      `json:"validation_errors,omitempty"`
}

type ActivationEvidence struct {
	MigrationPassed bool `json:"migration_passed"`
	BackupDryRun    bool `json:"backup_dry_run"`
	ScratchRestore  bool `json:"scratch_restore"`
	IntegrityPassed bool `json:"integrity_passed"`
	HealthPassed    bool `json:"health_passed"`
}

func (e ActivationEvidence) Complete() bool {
	return e.MigrationPassed && e.BackupDryRun && e.ScratchRestore && e.IntegrityPassed && e.HealthPassed
}

// RequestReceipt binds one trusted request identity to the canonical durable
// workflow result. It is persisted separately so a replay can be resolved by
// request ID without exposing the workflow payload to the caller.
type RequestReceipt struct {
	RequestID     string    `json:"request_id"`
	UserScope     string    `json:"user_scope"`
	PayloadHash   string    `json:"payload_hash"`
	RequirementID string    `json:"requirement_id"`
	CreatedAt     time.Time `json:"created_at"`
}

type WorkflowResult struct {
	Status         Status             `json:"status"`
	Lifecycle      Lifecycle          `json:"lifecycle"`
	Requirement    StorageRequirement `json:"requirement"`
	Classification Classification     `json:"classification"`
	Proposal       *StorageProposal   `json:"proposal,omitempty"`
	Evidence       ActivationEvidence `json:"evidence"`
	Reason         string             `json:"reason"`
	ReasonCode     string             `json:"reason_code,omitempty"`
	EvidenceRefs   []string           `json:"evidence_refs,omitempty"`
	Deduplicated   bool               `json:"deduplicated,omitempty"`
	// RequestReplay is an in-process projection of the request-receipt path;
	// it is never persisted as workflow payload data.
	RequestReplay bool      `json:"-"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}
