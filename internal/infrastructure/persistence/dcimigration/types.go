// Package dcimigration owns the read-only Step 03 DCI capture and dry-run
// boundaries.
//
// Capture copies explicit live sources into a new offline snapshot root without
// writing live runtime state. DryRun reads that caller-supplied snapshot, plans
// deterministic canonical identity/event mappings, and writes one bounded
// receipt. Build consumes the retained plan to produce four offline output
// stores and one bounded build receipt; it does not apply output state or
// perform cutover.
package dcimigration

import (
	"time"

	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	ManifestSchemaVersion       = "rencrow.identity.dci-migration/v2"
	CaptureSchemaVersion        = "rencrow.identity.dci-capture/v1"
	BuildSchemaVersion          = "rencrow.identity.dci-build/v1"
	CutoverSchemaVersion        = "rencrow.identity.dci-cutover/v2"
	ServiceCutoverSchemaVersion = "rencrow.identity.dci-service-cutover/v1"
	LogicalHashAlgorithm        = "rencrow.sqlite.logical/v1"
	TextNormalizationAlgorithm  = "rencrow.utf8.invalid-byte-replacement/v1"
	ModeDryRun                  = "dry-run"
	ModeCapture                 = "capture"
	ModeBuild                   = "build"
	ModeCutover                 = "cutover"
	StatusReady                 = "ready"
	StatusBlocked               = "blocked"

	CutoverStatusBlocked        = StatusBlocked
	CutoverStatusApplied        = "applied"
	CutoverStatusRolledBack     = "rolled_back"
	CutoverStatusRollbackFailed = "rollback_failed"

	CaptureReceiptFilename = "capture.json"
	BuildReceiptFilename   = "build.json"
)

// ExpectedCounts are the required cutover expectations.  Zero is a valid
// expectation; callers must provide every field (negative values are rejected).
type ExpectedCounts struct {
	Searches             int `json:"searches"`
	ReadEvents           int `json:"read_events"`
	EvidenceEvents       int `json:"evidence_events"`
	TotalEvents          int `json:"total_events"`
	LegacyLimitSteps     int `json:"legacy_limit_steps"`
	NormalizedTextValues int `json:"normalized_text_values"`
	InvalidUTF8Bytes     int `json:"invalid_utf8_bytes"`
}

// SourceCounts records bounded counts by source.  It intentionally contains
// no source path or source content.
type SourceCounts struct {
	DCITraces         int `json:"dci_traces"`
	DCISteps          int `json:"dci_steps"`
	DCIEvidence       int `json:"dci_evidence"`
	DCIQueryTerms     int `json:"dci_query_terms"`
	JSONLTraces       int `json:"jsonl_traces"`
	JSONLSteps        int `json:"jsonl_steps"`
	CurrentStaging    int `json:"current_staging"`
	CurrentDCIStaging int `json:"current_dci_staging"`
	CurrentRegistry   int `json:"current_registry"`
	ArchiveStaging    int `json:"archive_staging"`
	ArchiveDCIStaging int `json:"archive_dci_staging"`
	EventStore        int `json:"event_store_events"`
}

// ActualCounts are counts after the source records have been classified and
// deduplicated.
type ActualCounts struct {
	Searches             int `json:"searches"`
	ReadEvents           int `json:"read_events"`
	EvidenceEvents       int `json:"evidence_events"`
	TotalEvents          int `json:"total_events"`
	LegacyLimitSteps     int `json:"legacy_limit_steps"`
	NormalizedTextValues int `json:"normalized_text_values"`
	InvalidUTF8Bytes     int `json:"invalid_utf8_bytes"`
}

// DedupeCounts makes the source-to-canonical reduction auditable without
// exposing any legacy values.
type DedupeCounts struct {
	SearchesRemoved   int `json:"searches_removed"`
	StepsRemoved      int `json:"steps_removed"`
	EvidenceRemoved   int `json:"evidence_removed"`
	StagingDuplicates int `json:"staging_duplicates"`
}

// ZeroCounters are the planned post-cutover invariants.  They are counters,
// rather than booleans, so a later build/apply unit can compare them directly.
type ZeroCounters struct {
	LegacyKeyZero int `json:"legacy_key_zero"`
	OrphanZero    int `json:"orphan_zero"`
}

// ActorClassificationCounts contains only bounded classification totals.
type ActorClassificationCounts struct {
	AuthenticatedAgent int `json:"authenticated_agent"`
	LegacyUnattributed int `json:"legacy_unattributed"`
}

// Manifest is the bounded dry-run receipt.  It never carries queries,
// snippets, commands, source paths, token values, or event payloads.
type Manifest struct {
	SchemaVersion string `json:"schema_version"`
	Mode          string `json:"mode"`
	Status        string `json:"status"`

	ExpectedCounts ExpectedCounts `json:"expected_counts"`
	SourceCounts   SourceCounts   `json:"source_counts"`
	ActualCounts   ActualCounts   `json:"actual_counts"`
	DedupeCounts   DedupeCounts   `json:"dedupe_counts"`

	ExclusionReasonCounts         map[string]int            `json:"exclusion_reason_counts"`
	ActorClassification           ActorClassificationCounts `json:"actor_classification_counts"`
	LegacyActorLabelCounts        map[string]int            `json:"legacy_actor_label_counts"`
	LogicalHashAlgorithm          string                    `json:"logical_hash_algorithm"`
	SourceDatabaseLogicalSHA256   map[string]string         `json:"source_database_logical_sha256"`
	SourceSchemaSHA256            map[string]string         `json:"source_schema_sha256"`
	SourceDCIClassificationSHA256 map[string]string         `json:"source_dci_classification_sha256"`
	SourceFileSHA256              map[string]string         `json:"source_file_sha256"`
	SourceNonDCILogicalSHA256     map[string]string         `json:"source_non_dci_logical_sha256"`

	MappingSHA256              string `json:"mapping_sha256"`
	ActionSetSHA256            string `json:"action_id_set_sha256"`
	TraceSetSHA256             string `json:"trace_id_set_sha256"`
	EvidenceSetSHA256          string `json:"evidence_id_set_sha256"`
	EventSetSHA256             string `json:"event_id_set_sha256"`
	EventPlanSHA256            string `json:"event_plan_sha256"`
	TextNormalizationAlgorithm string `json:"text_normalization_algorithm"`

	PlannedZeroCounters ZeroCounters `json:"planned_zero_counters"`
	ErrorCode           string       `json:"error_code"`
}

// BuildOptions is the public offline build boundary. Build binds these paths
// to one captured snapshot and its ready dry-run manifest before creating any
// output. AgentIDs must come from the CORE composition root.
type BuildOptions struct {
	SnapshotDir    string
	BuildDir       string
	CaptureReceipt string
	DryRunManifest string
	AgentIDs       []string
}

// BuildOutputArtifact is bounded evidence for one fixed output role. Empty
// hash fields are intentional only where that database role has no such hash
// (for example, L1 has no full logical hash in its owner evidence).
type BuildOutputArtifact struct {
	FileSHA256           string `json:"file_sha256"`
	Bytes                int64  `json:"bytes"`
	OutputSchemaSHA256   string `json:"output_schema_sha256"`
	OutputLogicalSHA256  string `json:"output_logical_sha256"`
	OutputNonDCISHA256   string `json:"output_non_dci_logical_sha256"`
	QuickCheckOK         int    `json:"quick_check_ok"`
	ForeignKeyViolations int    `json:"foreign_key_violations"`
	SidecarZero          int    `json:"sidecar_zero"`
}

// BuildDCICheck is the exported, path-free projection of private DCI owner
// verification evidence.
type BuildDCICheck struct {
	OutputSchemaSHA256       string `json:"output_schema_sha256"`
	OutputLogicalSHA256      string `json:"output_logical_sha256"`
	TraceRows                int    `json:"trace_rows"`
	StepRows                 int    `json:"step_rows"`
	EvidenceRows             int    `json:"evidence_rows"`
	QueryTermRows            int    `json:"query_term_rows"`
	AuthenticatedTraces      int    `json:"authenticated_traces"`
	LegacyUnattributedTraces int    `json:"legacy_unattributed_traces"`
	DistinctActionIDs        int    `json:"distinct_action_ids"`
	DistinctTraceIDs         int    `json:"distinct_trace_ids"`
	DistinctStepEventIDs     int    `json:"distinct_step_event_ids"`
	DistinctEvidenceIDs      int    `json:"distinct_evidence_ids"`
	DistinctCreatedEventIDs  int    `json:"distinct_created_event_ids"`
	LegacyKeyMarkers         int    `json:"legacy_key_markers"`
	OrphanActionRefs         int    `json:"orphan_action_refs"`
	ForeignKeyViolations     int    `json:"foreign_key_violations"`
	QuickCheckOK             int    `json:"quick_check_ok"`
	SidecarZero              int    `json:"sidecar_zero"`
}

// BuildEventStoreCheck is the exported, path-free projection of private
// Event Store owner verification evidence.
type BuildEventStoreCheck struct {
	SourceSchemaSHA256     string `json:"source_schema_sha256"`
	OutputSchemaSHA256     string `json:"output_schema_sha256"`
	SourceNonDCISHA256     string `json:"source_non_dci_logical_sha256"`
	OutputNonDCISHA256     string `json:"output_non_dci_logical_sha256"`
	OutputLogicalSHA256    string `json:"output_logical_sha256"`
	SourceEnvelopeCount    int    `json:"source_envelope_count"`
	PlannedEnvelopeCount   int    `json:"planned_envelope_count"`
	OutputEnvelopeCount    int    `json:"output_envelope_count"`
	SourceDependencyCount  int    `json:"source_dependency_count"`
	PlannedDependencyCount int    `json:"planned_dependency_count"`
	OutputDependencyCount  int    `json:"output_dependency_count"`
	PlannedDCIEventCount   int    `json:"planned_dci_event_count"`
	OutputDCIEventCount    int    `json:"output_dci_event_count"`
	ForeignKeyViolations   int    `json:"foreign_key_violations"`
	QuickCheckOK           int    `json:"quick_check_ok"`
	SidecarZero            int    `json:"sidecar_zero"`
}

// BuildL1Check is the exported, path-free projection of private current or
// archive L1 owner verification evidence.
type BuildL1Check struct {
	DCIStagingRows          int    `json:"dci_staging_rows"`
	RegistryRows            int    `json:"registry_rows"`
	CanonicalStagingRows    int    `json:"canonical_staging_rows"`
	CanonicalRegistryRows   int    `json:"canonical_registry_rows"`
	OldStagingRowsRemaining int    `json:"old_staging_rows_remaining"`
	RawTextHashMismatches   int    `json:"raw_text_hash_mismatches"`
	RawHashMismatches       int    `json:"raw_hash_mismatches"`
	PromotedReferences      int    `json:"promoted_references"`
	OrphanRows              int    `json:"orphan_rows"`
	ForeignKeyViolations    int    `json:"foreign_key_violations"`
	QuickCheckOK            int    `json:"quick_check_ok"`
	SidecarZero             int    `json:"sidecar_zero"`
	SourceSchemaSHA256      string `json:"source_schema_sha256"`
	OutputSchemaSHA256      string `json:"output_schema_sha256"`
	SourceNonDCISHA256      string `json:"source_non_dci_logical_sha256"`
	OutputNonDCISHA256      string `json:"output_non_dci_logical_sha256"`
}

// BuildReceipt is the bounded path-free proof for one offline build. It
// repeats the ready dry-run bindings and adds measured fixed-output evidence.
// No path, query, payload, secret, or individual canonical/legacy ID is
// exposed.
type BuildReceipt struct {
	SchemaVersion string    `json:"schema_version"`
	Mode          string    `json:"mode"`
	Status        string    `json:"status"`
	StartedAt     time.Time `json:"started_at"`
	CompletedAt   time.Time `json:"completed_at"`

	ExpectedCounts ExpectedCounts `json:"expected_counts"`
	SourceCounts   SourceCounts   `json:"source_counts"`
	ActualCounts   ActualCounts   `json:"actual_counts"`
	DedupeCounts   DedupeCounts   `json:"dedupe_counts"`

	ExclusionReasonCounts         map[string]int            `json:"exclusion_reason_counts"`
	ActorClassification           ActorClassificationCounts `json:"actor_classification_counts"`
	LegacyActorLabelCounts        map[string]int            `json:"legacy_actor_label_counts"`
	LogicalHashAlgorithm          string                    `json:"logical_hash_algorithm"`
	SourceDatabaseLogicalSHA256   map[string]string         `json:"source_database_logical_sha256"`
	SourceSchemaSHA256            map[string]string         `json:"source_schema_sha256"`
	SourceDCIClassificationSHA256 map[string]string         `json:"source_dci_classification_sha256"`
	SourceFileSHA256              map[string]string         `json:"source_file_sha256"`
	SourceNonDCILogicalSHA256     map[string]string         `json:"source_non_dci_logical_sha256"`

	MappingSHA256              string `json:"mapping_sha256"`
	ActionSetSHA256            string `json:"action_id_set_sha256"`
	TraceSetSHA256             string `json:"trace_id_set_sha256"`
	EvidenceSetSHA256          string `json:"evidence_id_set_sha256"`
	EventSetSHA256             string `json:"event_id_set_sha256"`
	EventPlanSHA256            string `json:"event_plan_sha256"`
	TextNormalizationAlgorithm string `json:"text_normalization_algorithm"`

	CaptureReceiptSHA256     string `json:"capture_receipt_sha256"`
	DryRunManifestSHA256     string `json:"dry_run_manifest_sha256"`
	CaptureArtifactSetSHA256 string `json:"capture_artifact_set_sha256"`

	PlannedZeroCounters ZeroCounters `json:"planned_zero_counters"`

	OutputArtifacts         map[string]BuildOutputArtifact `json:"output_artifacts"`
	OutputArtifactSetSHA256 string                         `json:"output_artifact_set_sha256"`
	BuildRootModeOK         int                            `json:"build_root_mode_ok"`
	SidecarZero             int                            `json:"sidecar_zero"`
	SourceInputsStable      int                            `json:"source_inputs_stable"`
	DCI                     BuildDCICheck                  `json:"dci"`
	EventStore              BuildEventStoreCheck           `json:"event_store"`
	L1                      BuildL1Check                   `json:"l1"`
	Archive                 BuildL1Check                   `json:"archive"`
	ErrorCode               string                         `json:"error_code"`
}

// CutoverReceipt is the bounded, path-free proof emitted by the future
// coordinated cutover boundary.  It contains only hashes, fixed role maps,
// bounded counts, and health counters; no filesystem path, query, payload,
// secret, or individual identity is represented.
//
// A blocked receipt claims only pre-mutation validation failure.  An applied
// receipt claims the new cohort and retired JSONL.  A rolled_back receipt
// claims the old cohort was restored.  A rollback_failed receipt is truthful
// about failure and never claims complete restoration.
type CutoverReceipt struct {
	SchemaVersion string    `json:"schema_version"`
	Mode          string    `json:"mode"`
	Status        string    `json:"status"`
	StartedAt     time.Time `json:"started_at"`
	CompletedAt   time.Time `json:"completed_at"`

	BuildReceiptSHA256       string `json:"build_receipt_sha256"`
	CaptureReceiptSHA256     string `json:"capture_receipt_sha256"`
	DryRunManifestSHA256     string `json:"dry_run_manifest_sha256"`
	CaptureArtifactSetSHA256 string `json:"capture_artifact_set_sha256"`

	ExpectedCounts ExpectedCounts `json:"expected_counts"`
	SourceCounts   SourceCounts   `json:"source_counts"`
	ActualCounts   ActualCounts   `json:"actual_counts"`
	DedupeCounts   DedupeCounts   `json:"dedupe_counts"`

	ExclusionReasonCounts  map[string]int            `json:"exclusion_reason_counts"`
	ActorClassification    ActorClassificationCounts `json:"actor_classification_counts"`
	LegacyActorLabelCounts map[string]int            `json:"legacy_actor_label_counts"`
	LogicalHashAlgorithm   string                    `json:"logical_hash_algorithm"`

	SourceDatabaseLogicalSHA256   map[string]string `json:"source_database_logical_sha256"`
	SourceSchemaSHA256            map[string]string `json:"source_schema_sha256"`
	SourceDCIClassificationSHA256 map[string]string `json:"source_dci_classification_sha256"`
	SourceFileSHA256              map[string]string `json:"source_file_sha256"`
	SourceNonDCILogicalSHA256     map[string]string `json:"source_non_dci_logical_sha256"`

	MappingSHA256              string       `json:"mapping_sha256"`
	ActionSetSHA256            string       `json:"action_id_set_sha256"`
	TraceSetSHA256             string       `json:"trace_id_set_sha256"`
	EvidenceSetSHA256          string       `json:"evidence_id_set_sha256"`
	EventSetSHA256             string       `json:"event_id_set_sha256"`
	EventPlanSHA256            string       `json:"event_plan_sha256"`
	TextNormalizationAlgorithm string       `json:"text_normalization_algorithm"`
	PlannedZeroCounters        ZeroCounters `json:"planned_zero_counters"`

	OutputArtifacts               map[string]BuildOutputArtifact `json:"output_artifacts"`
	OutputArtifactSetSHA256       string                         `json:"output_artifact_set_sha256"`
	RollbackArtifactSetSHA256     string                         `json:"rollback_artifact_set_sha256"`
	ReplacementArtifactSetSHA256  string                         `json:"replacement_artifact_set_sha256"`
	ActiveBeforeArtifactSetSHA256 string                         `json:"active_before_artifact_set_sha256"`
	ActiveAfterArtifactSetSHA256  string                         `json:"active_after_artifact_set_sha256"`
	RestoredArtifactSetSHA256     string                         `json:"restored_artifact_set_sha256"`

	OldRuntimeSHA256 string `json:"old_runtime_sha256"`
	NewRuntimeSHA256 string `json:"new_runtime_sha256"`

	RollbackFileCount    int `json:"rollback_file_count"`
	ReplacementFileCount int `json:"replacement_file_count"`
	ActiveFileCount      int `json:"active_file_count"`

	JSONLRetired  int `json:"jsonl_retired"`
	JSONLRestored int `json:"jsonl_restored"`

	QuickCheckOK         int `json:"quick_check_ok"`
	ForeignKeyViolations int `json:"foreign_key_violations"`
	SidecarZero          int `json:"sidecar_zero"`
	LegacyKeyMarkers     int `json:"legacy_key_markers"`
	OrphanActionRefs     int `json:"orphan_action_refs"`
	SourceInputsStable   int `json:"source_inputs_stable"`

	DCI        BuildDCICheck        `json:"dci"`
	EventStore BuildEventStoreCheck `json:"event_store"`
	L1         BuildL1Check         `json:"l1"`
	Archive    BuildL1Check         `json:"archive"`
	ErrorCode  string               `json:"error_code"`
}

// ServiceCutoverRunningEvidence is the bounded, path-free projection of the
// service owner proof used by D2d.  It deliberately contains no PID, socket,
// command, configuration path, or other runtime-private value.
type ServiceCutoverRunningEvidence struct {
	Owner           int    `json:"owner"`
	Enabled         int    `json:"enabled"`
	Unmasked        int    `json:"unmasked"`
	Active          int    `json:"active"`
	MainPIDPositive int    `json:"main_pid_positive"`
	ListenerOwned   int    `json:"listener_owned"`
	Readiness       int    `json:"readiness"`
	RuntimeSHA256   string `json:"runtime_sha256"`
}

// ServiceCutoverStoppedEvidence is the bounded, path-free stopped-service
// projection.  It records only proofs, never the service identity or process
// details used to obtain them.
type ServiceCutoverStoppedEvidence struct {
	Masked       int `json:"masked"`
	Active       int `json:"active"`
	MainPIDZero  int `json:"main_pid_zero"`
	ListenerZero int `json:"listener_zero"`
}

// ServiceCutoverReceipt is the durable service-manager subreceipt for D2d.
// It binds the service lifecycle evidence to the already-durable D2c file
// subreceipt, but does not claim post-deploy readiness or real-Actor E2E.
// All fields are bounded and path-free; D2c's applied file receipt remains
// immutable even when this outer lifecycle later rolls back.
type ServiceCutoverReceipt struct {
	SchemaVersion string    `json:"schema_version"`
	Mode          string    `json:"mode"`
	Status        string    `json:"status"`
	StartedAt     time.Time `json:"started_at"`
	CompletedAt   time.Time `json:"completed_at"`

	CutoverSubreceiptSHA256 string `json:"cutover_subreceipt_sha256"`
	CutoverSubreceiptStatus string `json:"cutover_subreceipt_status"`
	CutoverTerminalStatus   string `json:"cutover_terminal_status"`

	BuildReceiptSHA256       string `json:"build_receipt_sha256"`
	CaptureReceiptSHA256     string `json:"capture_receipt_sha256"`
	DryRunManifestSHA256     string `json:"dry_run_manifest_sha256"`
	CaptureArtifactSetSHA256 string `json:"capture_artifact_set_sha256"`

	OldRuntimeSHA256 string `json:"old_runtime_sha256"`
	NewRuntimeSHA256 string `json:"new_runtime_sha256"`

	InitialRunning       ServiceCutoverRunningEvidence `json:"initial_running"`
	StoppedBeforePrepare ServiceCutoverStoppedEvidence `json:"stopped_before_prepare"`
	StoppedBeforeApply   ServiceCutoverStoppedEvidence `json:"stopped_before_apply"`
	FinalRunning         ServiceCutoverRunningEvidence `json:"final_running"`

	ErrorCode string `json:"error_code"`
}

// Options is the complete bounded dry-run API contract.  All five source
// paths and the manifest path are resolved relative to SnapshotDir when they
// are not absolute.  AgentIDs must come from the CORE composition root.
type Options struct {
	SnapshotDir      string
	SourceDCI        string
	SourceDCIJSONL   string
	SourceEventStore string
	SourceL1         string
	SourceArchive    string
	Manifest         string
	Expected         ExpectedCounts
	AgentIDs         []string
}

type sourcePaths struct {
	root       string
	dci        string
	dciJSONL   string
	eventStore string
	l1         string
	archive    string
	manifest   string
}

type legacyStep struct {
	ID           int64
	SearchID     string
	StepNo       int
	Tool         string
	CommandText  string
	FilePath     string
	ResultCount  int
	Status       string
	ErrorMessage string
	CreatedAt    time.Time
}

type legacySearch struct {
	ID                 string
	StartedAt          time.Time
	EndedAt            time.Time
	Actor              string
	Mode               string
	Query              string
	CorpusScope        []string
	Status             string
	FinalEvidenceCount int
	ErrorMessage       string
	Steps              map[int]legacyStep
}

type legacyEvidence struct {
	ID         string
	SearchID   string
	SourceID   string
	FilePath   string
	Heading    string
	LineStart  int
	LineEnd    int
	Snippet    string
	Reason     string
	Confidence float64
	CreatedAt  time.Time
}

type legacyRegistryRef struct {
	SourceID    string
	SearchID    string
	EvidenceID  string
	RawMetaJSON string
	OriginTable string
}

// l1StagingRef is the bounded migration-only identity for one classified
// staging row. The raw text itself remains in the cloned SQLite source; only
// its byte hash is retained here so projections can prove that it was not
// rewritten.
type l1StagingRef struct {
	ID            string
	EventID       string
	SourceID      string
	RawHash       string
	RawTextSHA256 string
	RawMetaJSON   string
	SearchID      string
	EvidenceID    string
	OriginTable   string
}

type sourceSnapshot struct {
	Counts             SourceCounts
	Searches           map[string]legacySearch
	Evidence           map[string]legacyEvidence
	RegistryRefs       []legacyRegistryRef
	StagingIDs         map[string]struct{}
	StagingEvidenceIDs map[string]struct{}
	ExistingEventIDs   map[string]struct{}
	SourceHashes       map[string]sourceHashes
	currentL1          l1SourceData
	archiveL1          l1SourceData
	normalization      textNormalizationCounts
}

type textNormalizationCounts struct {
	NormalizedTextValues int
	InvalidUTF8Bytes     int
}

// migrationPlan is the single deterministic output of migration planning.
// Identity maps are intentionally private: legacy values may be used by later
// offline build steps in this package, but must never become receipt fields or
// a production payload contract.
type migrationPlan struct {
	actual          ActualCounts
	Events          []modulecore.EventEnvelope
	mappingLines    []string
	eventPlanSHA256 string
	searches        map[string]searchMigrationIDs
	readEvents      map[readEventKey]modulecore.EventID
	evidence        map[string]evidenceMigrationIDs
}

type searchMigrationIDs struct {
	actionID         modulecore.ActionID
	traceID          modulecore.TraceID
	startedEventID   modulecore.EventID
	terminalEventID  modulecore.EventID
	actorAttribution domaindci.ActorAttribution
	actorKind        string
	actorID          string
}

type readEventKey struct {
	searchID string
	stepNo   int
}

type evidenceMigrationIDs struct {
	evidenceID     modulecore.EvidenceID
	createdEventID modulecore.EventID
}

// sourceHashes are collected while a read-only source database remains open.
// Classification is the legacy/domain-specific evidence hash; DatabaseLogical,
// Schema, and NonDCI are the v2 source-binding hashes. File is used only for
// the legacy JSONL source.
type sourceHashes struct {
	DatabaseLogical string
	Schema          string
	Classification  string
	File            string
	NonDCI          string
}
