package backlog

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	SchemaVersion2 = 2

	StatusProposalReview = "proposal_review"

	ConceptRadar     = "RADAR"
	ConceptCandidate = "CANDIDATE"
	ConceptAdopted   = "ADOPTED"
	ConceptDeferred  = "DEFERRED"
	ConceptRejected  = "REJECTED"

	DeliveryNone             = "NONE"
	DeliveryQueued           = "QUEUED"
	DeliverySpec             = "SPEC"
	DeliveryTDDRed           = "TDD_RED"
	DeliveryTDDGreen         = "TDD_GREEN"
	DeliveryRefactor         = "REFACTOR"
	DeliveryE2EPredeploy     = "E2E_PREDEPLOY"
	DeliveryBuild            = "BUILD"
	DeliveryDeploy           = "DEPLOY"
	DeliveryRestart          = "RESTART"
	DeliveryPostDeployVerify = "POST_DEPLOY_VERIFY"
	DeliveryLiveVerified     = "LIVE_VERIFIED"
	DeliveryDone             = "DONE"
	DeliveryBlocked          = "BLOCKED"
	DeliveryRejected         = "REJECTED"

	ImplementationLeaseName = "atlas_implementation"

	// LifecycleOwnerModule is the fixed owner of the Atlas lifecycle. The
	// implementation target and the feature consumers are separate fields.
	LifecycleOwnerModule = "RenCrow_CORE"
)

// SourceRef identifies the immutable source that caused an Atlas item to be
// ingested. The source body itself is intentionally not copied into the item.
type SourceRef struct {
	Type         string `json:"type"`
	Locator      string `json:"locator"`
	Strength     string `json:"strength,omitempty"`
	Repository   string `json:"repository,omitempty"`
	Revision     string `json:"revision,omitempty"`
	ContentHash  string `json:"content_hash,omitempty"`
	CapturedAt   string `json:"captured_at"`
	RawOrSummary string `json:"raw_or_summary"`
}

func (s SourceRef) DedupeKey() string {
	return strings.Join([]string{
		strings.ToLower(strings.TrimSpace(s.Type)),
		strings.TrimSpace(s.Locator),
		strings.ToLower(strings.TrimSpace(s.ContentHash)),
	}, "\x00")
}

// EvidenceRef points to an existing test, job, receipt, trace, revision, or
// readiness artifact. Evidence is referenced rather than copied into JSONL.
type EvidenceRef struct {
	Stage      string `json:"stage"`
	Kind       string `json:"kind"`
	Ref        string `json:"ref"`
	Repository string `json:"repository,omitempty"`
	Revision   string `json:"revision,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	ObservedAt string `json:"observed_at"`
	// Passed is an external/request-side claim.  It is intentionally kept for
	// legacy wire compatibility and is never sufficient for a lifecycle gate.
	Passed bool `json:"passed"`
	// Verified is the persisted CORE-owned verification result.  It is separate
	// from Passed so an untrusted request cannot forge an owner result.
	Verified           bool   `json:"verified,omitempty"`
	VerificationResult string `json:"verification_result,omitempty"`
	VerifiedAt         string `json:"verified_at,omitempty"`
	Verifier           string `json:"verifier,omitempty"`
}

const (
	EvidenceVerificationVerified = "verified"
	EvidenceVerificationRejected = "rejected"
)

// IsVerified reports the CORE-owned positive verification result.  The bool
// field is the canonical persisted value; the status string is accepted for
// forward/legacy JSON compatibility when it explicitly says "verified".
func (e EvidenceRef) IsVerified() bool {
	return e.Verified || strings.EqualFold(strings.TrimSpace(e.VerificationResult), EvidenceVerificationVerified)
}

// SpecificationArtifact is a verified specification projection. Local bodies
// are embedded by the feature package and are addressed by SpecID, never by a
// request-provided filesystem path. External artifacts retain metadata only.
type SpecificationArtifact struct {
	SpecID        string `json:"spec_id"`
	Type          string `json:"type"`
	Status        string `json:"status"`
	Title         string `json:"title"`
	Source        any    `json:"source,omitempty"`
	Revision      int    `json:"revision,omitempty"`
	ContentPath   string `json:"content_path,omitempty"`
	ContentSHA256 string `json:"content_sha256,omitempty"`
	CapturedAt    string `json:"captured_at,omitempty"`
	Content       string `json:"content,omitempty"`
	BodyAvailable bool   `json:"body_available"`
}

// BackfillImportReceipt is stored as a non-item record in the existing
// backlog JSONL. It makes a package import auditable without introducing a
// second backlog database.
type BackfillImportReceipt struct {
	RecordType         string `json:"record_type"`
	ImportID           string `json:"import_id"`
	DatasetID          string `json:"dataset_id"`
	PackageSHA256      string `json:"package_sha256"`
	Revision           int    `json:"revision"`
	ItemCount          int    `json:"item_count"`
	SpecificationCount int    `json:"specification_count"`
	ImportedAt         string `json:"imported_at"`
}

type BackfillReconcileResult struct {
	Imported int `json:"imported"`
	Updated  int `json:"updated"`
	Skipped  int `json:"skipped"`
}

func BackfillImportID(packageSHA256 string, revision int) string {
	return fmt.Sprintf("atlas-backfill:%s:%d", strings.ToLower(strings.TrimSpace(packageSHA256)), revision)
}

func (e EvidenceRef) Key() string {
	return strings.Join([]string{
		strings.ToUpper(strings.TrimSpace(e.Stage)),
		strings.ToLower(strings.TrimSpace(e.Kind)),
		strings.TrimSpace(e.Ref),
	}, "\x00")
}

// Item is the append-only Backlog/Atlas record. The legacy fields at the end
// remain part of the wire format for /viewer/backlog compatibility; v2 state
// is authoritative whenever it is present.
type Item struct {
	SchemaVersion int `json:"schema_version"`

	ItemID             string   `json:"item_id"`
	FeatureID          string   `json:"feature_id,omitempty"`
	Kind               string   `json:"kind"`
	Title              string   `json:"title"`
	Body               string   `json:"body,omitempty"`
	Purpose            string   `json:"purpose,omitempty"`
	Problem            string   `json:"problem,omitempty"`
	Idea               string   `json:"idea,omitempty"`
	Background         string   `json:"background,omitempty"`
	ExpectedEffect     []string `json:"expected_effect,omitempty"`
	RelationRefs       []string `json:"relation_refs,omitempty"`
	TargetModules      []string `json:"target_modules,omitempty"`
	ConsumerModules    []string `json:"consumer_modules,omitempty"`
	AffectedModules    []string `json:"affected_modules,omitempty"`
	AcceptanceCriteria []string `json:"acceptance_criteria,omitempty"`

	Category    string      `json:"category,omitempty"`
	Source      string      `json:"source"`
	SourceRefs  []SourceRef `json:"source_refs,omitempty"`
	Owner       string      `json:"owner,omitempty"`
	OwnerModule string      `json:"owner_module,omitempty"`

	ConceptState  string `json:"concept_state,omitempty"`
	DeliveryState string `json:"delivery_state,omitempty"`
	// DeclaredDeliveryState retains the source package claim separately from
	// the runtime state, which starts at NONE until CORE has cumulative evidence.
	DeclaredDeliveryState string   `json:"declared_delivery_state,omitempty"`
	ReconstructionBasis   string   `json:"reconstruction_basis,omitempty"`
	MigrationStatus       string   `json:"migration_status,omitempty"`
	OriginAtlas           []string `json:"origin_atlas,omitempty"`
	SpecificationRefs     []string `json:"specification_refs,omitempty"`

	Priority   string   `json:"priority"`
	QueueRank  int      `json:"queue_rank,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	DependsOn  []string `json:"depends_on,omitempty"`
	RelatedIDs []string `json:"related_ids,omitempty"`

	AdoptionReason string `json:"adoption_reason,omitempty"`
	AdoptedAt      string `json:"adopted_at,omitempty"`

	WorkstreamID           string        `json:"workstream_id,omitempty"`
	ImplementationUnit     string        `json:"implementation_unit_id,omitempty"`
	ImplementationRevision int           `json:"implementation_revision,omitempty"`
	InvalidatedFromStage   string        `json:"invalidated_from_stage,omitempty"`
	SupersedesUnitID       string        `json:"supersedes_unit_id,omitempty"`
	BlockerResolutionRefs  []EvidenceRef `json:"blocker_resolution_refs,omitempty"`
	EvidenceRefs           []EvidenceRef `json:"evidence_refs,omitempty"`

	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`

	// Legacy compatibility projection/input.
	Status         string `json:"status,omitempty"`
	Implementer    string `json:"implementer,omitempty"`
	Implementation string `json:"implementation,omitempty"`
	TestResult     string `json:"test_result,omitempty"`
	CheckOK        bool   `json:"check_ok,omitempty"`
	CheckedBy      string `json:"checked_by,omitempty"`
}

// ImplementationUnit is the typed lifecycle projection used by the owner API.
// Durable item state remains in the append-only backlog record.
type ImplementationUnit struct {
	UnitID                 string        `json:"unit_id"`
	ItemID                 string        `json:"item_id"`
	Title                  string        `json:"title"`
	OwnerModule            string        `json:"owner_module,omitempty"`
	TargetModules          []string      `json:"target_modules,omitempty"`
	ConsumerModules        []string      `json:"consumer_modules,omitempty"`
	AffectedModules        []string      `json:"affected_modules,omitempty"`
	ConceptState           string        `json:"concept_state"`
	DeliveryState          string        `json:"delivery_state"`
	WorkstreamID           string        `json:"workstream_id,omitempty"`
	Priority               string        `json:"priority,omitempty"`
	QueueRank              int           `json:"queue_rank,omitempty"`
	ImplementationRevision int           `json:"implementation_revision,omitempty"`
	InvalidatedFromStage   string        `json:"invalidated_from_stage,omitempty"`
	SupersedesUnitID       string        `json:"supersedes_unit_id,omitempty"`
	BlockerResolutionRefs  []EvidenceRef `json:"blocker_resolution_refs,omitempty"`
	EvidenceRefs           []EvidenceRef `json:"evidence_refs,omitempty"`
	CreatedAt              string        `json:"created_at"`
	UpdatedAt              string        `json:"updated_at"`
}

func (i Item) Unit() ImplementationUnit {
	return ImplementationUnit{
		UnitID: i.ImplementationUnit, ItemID: i.ItemID, Title: i.Title,
		OwnerModule:     i.OwnerModule,
		TargetModules:   append([]string(nil), i.TargetModules...),
		ConsumerModules: append([]string(nil), i.ConsumerModules...),
		AffectedModules: append([]string(nil), i.AffectedModules...),
		ConceptState:    i.ConceptState,
		DeliveryState:   i.DeliveryState, WorkstreamID: i.WorkstreamID,
		Priority: i.Priority, QueueRank: i.QueueRank,
		ImplementationRevision: implementationRevision(i.ImplementationRevision),
		InvalidatedFromStage:   i.InvalidatedFromStage,
		SupersedesUnitID:       i.SupersedesUnitID,
		BlockerResolutionRefs:  append([]EvidenceRef(nil), i.BlockerResolutionRefs...),
		EvidenceRefs:           append([]EvidenceRef(nil), i.EvidenceRefs...),
		CreatedAt:              i.CreatedAt, UpdatedAt: i.UpdatedAt,
	}
}

func implementationRevision(value int) int {
	if value < 1 {
		return 1
	}
	return value
}

// LegacyStatus projects v2 state onto the established backlog API. check_ok is
// deliberately not considered a completion signal for v2.
func LegacyStatus(item Item) string {
	switch strings.ToUpper(strings.TrimSpace(item.DeliveryState)) {
	case DeliveryLiveVerified, DeliveryDone:
		return "ok"
	case DeliveryBlocked:
		return "blocked"
	case DeliveryRejected:
		return "rejected"
	case DeliverySpec, DeliveryTDDRed, DeliveryTDDGreen, DeliveryRefactor:
		return "implementing"
	case DeliveryE2EPredeploy, DeliveryBuild, DeliveryDeploy, DeliveryRestart, DeliveryPostDeployVerify:
		return "testing"
	case DeliveryQueued:
		return StatusProposalReview
	}
	switch strings.ToUpper(strings.TrimSpace(item.ConceptState)) {
	case ConceptRadar, ConceptCandidate:
		return "open"
	case ConceptAdopted:
		return StatusProposalReview
	case ConceptDeferred:
		return "open"
	case ConceptRejected:
		return "rejected"
	}
	if item.CheckOK {
		return "ok"
	}
	status := strings.ToLower(strings.TrimSpace(item.Status))
	if status == "" {
		return "open"
	}
	return status
}

// ProjectLegacy fills v2 fields from legacy records without adopting or
// completing them. In particular, proposal_review/open records are candidates
// and remain non-runnable until an owner calls the v2 adopt operation.
func ProjectLegacy(item Item) Item {
	if item.ImplementationRevision < 1 {
		item.ImplementationRevision = 1
	}
	if item.SchemaVersion >= SchemaVersion2 {
		if strings.TrimSpace(item.Status) == "" {
			item.Status = LegacyStatus(item)
		}
		if item.DeliveryState == "" {
			item.DeliveryState = DeliveryNone
		}
		if item.ConceptState == "" {
			item.ConceptState = ConceptCandidate
		}
		if item.DeliveryState != DeliveryLiveVerified && item.DeliveryState != DeliveryDone {
			item.CheckOK = false
		}
		return item
	}
	status := strings.ToLower(strings.TrimSpace(item.Status))
	switch status {
	case "rejected":
		item.ConceptState = ConceptRejected
		item.DeliveryState = DeliveryRejected
	case "blocked":
		item.ConceptState = ConceptCandidate
		item.DeliveryState = DeliveryBlocked
	case "implementing", "testing", "fixing":
		// Existing work may be visible in the legacy board, but adoption is not
		// inferred. It is represented as a candidate with no v2 delivery claim.
		item.ConceptState = ConceptCandidate
		item.DeliveryState = DeliveryNone
	default:
		item.ConceptState = ConceptCandidate
		item.DeliveryState = DeliveryNone
	}
	item.Status = status
	if item.Status == "" {
		item.Status = "open"
	}
	return item
}

// NewDeterministicID gives intake a stable ID when the caller omitted one.
func NewDeterministicID(refs []SourceRef, title string) string {
	h := sha256.New()
	for _, ref := range refs {
		_, _ = h.Write([]byte(ref.DedupeKey()))
	}
	_, _ = h.Write([]byte(strings.TrimSpace(title)))
	return fmt.Sprintf("atlas-%s", hex.EncodeToString(h.Sum(nil))[:20])
}
