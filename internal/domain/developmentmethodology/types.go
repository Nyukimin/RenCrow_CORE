// Package developmentmethodology contains the pure domain rules for the
// RenCrow development method.  It describes plans, tasks, evidence, and
// gates; durable Atlas/Backlog ownership and Implementation Unit lifecycle
// remain in their existing owner packages.
package developmentmethodology

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
)

const (
	SchemaVersion                     = 1
	MaturationPolicyDays              = 7
	DefaultImplementationAuthorityTTL = 7 * 24 * time.Hour
	RootCauseEscalation               = 3
)

// Clock is the small time boundary used by validity and due-date rules.
// Production callers use RealClock; tests can inject a deterministic clock.
type Clock interface {
	Now() time.Time
}

// RealClock reads the current UTC time.
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now().UTC() }

// FixedClock is useful for deterministic domain tests and simulations.
type FixedClock struct {
	Current time.Time
}

func (c FixedClock) Now() time.Time { return c.Current }

// ClockFunc adapts a function to Clock.
type ClockFunc func() time.Time

func (f ClockFunc) Now() time.Time { return f() }

// WorkClass is the deterministic change-surface classification used at
// development intake. It is deliberately input data, not an LLM decision.
type WorkClass string

const (
	WorkClassSpike         WorkClass = "spike"
	WorkClassBounded       WorkClass = "bounded"
	WorkClassArchitectural WorkClass = "architectural"
)

type WorkClassification struct {
	Class  WorkClass `json:"class"`
	Reason string    `json:"reason,omitempty"`
}

// TaskState is the shared methodology execution lifecycle. It is distinct
// from Backlog's delivery owner, but Task and Ledger use the same values and
// transition graph. It contains no human-wait state.
type TaskState string

// LifecycleState is the shared serialized state vocabulary used by both Task
// and Ledger. TaskState remains the compatibility name used by existing
// callers; LedgerState makes the same contract explicit at ledger call sites.
type LifecycleState = TaskState
type LedgerState = TaskState

const (
	TaskPending       TaskState = "PENDING"
	TaskReady         TaskState = "READY"
	TaskAssigned      TaskState = "ASSIGNED"
	TaskRedVerified   TaskState = "RED_VERIFIED"
	TaskGreenVerified TaskState = "GREEN_VERIFIED"
	TaskRefactored    TaskState = "REFACTORED"
	TaskReviewed      TaskState = "TASK_REVIEWED"
	TaskDone          TaskState = "DONE"
	TaskBlocked       TaskState = "BLOCKED"
	TaskFailed        TaskState = "FAILED"
	TaskCancelled     TaskState = "CANCELLED"
)

// TerminalOutcome is the only terminal result vocabulary for runs, tasks,
// and plans. "partial", "running", and "waiting" are not terminal results.
type TerminalOutcome string

const (
	OutcomeOK        TerminalOutcome = "ok"
	OutcomeFailed    TerminalOutcome = "failed"
	OutcomeBlocked   TerminalOutcome = "blocked"
	OutcomeCancelled TerminalOutcome = "cancelled"
)

// ExecutionRole represents authority, not an Agent identity or a model
// binding. A role can be carried by different runtime actors over time.
type ExecutionRole string

const (
	RoleCoder      ExecutionRole = "coder"
	RoleWorker     ExecutionRole = "worker"
	RoleReviewer   ExecutionRole = "reviewer"
	RoleController ExecutionRole = "controller"
	RoleSystem     ExecutionRole = "system_owner"
)

// AuthorityOperation names the side effect a role is asking to perform.
type AuthorityOperation string

const (
	OperationPlan            AuthorityOperation = "plan"
	OperationPatch           AuthorityOperation = "patch"
	OperationProposal        AuthorityOperation = "proposal"
	OperationTestProposal    AuthorityOperation = "test_proposal"
	OperationReviewFinding   AuthorityOperation = "review_finding"
	OperationReview          AuthorityOperation = "review"
	OperationRuling          AuthorityOperation = "ruling"
	OperationStateTransition AuthorityOperation = "state_transition"
	OperationEvidence        AuthorityOperation = "evidence"
	OperationRepositoryWrite AuthorityOperation = "repository_write"
	OperationShell           AuthorityOperation = "shell"
	OperationTestExecution   AuthorityOperation = "test_execution"
	OperationBuild           AuthorityOperation = "build"
	OperationDeploy          AuthorityOperation = "deploy"
	OperationRestart         AuthorityOperation = "restart"
)

// GateStatus is a deterministic gate result. A gate never waits for a human;
// a failed safety condition is rejected or blocked immediately.
type GateStatus string

const (
	GatePassed   GateStatus = "passed"
	GateRejected GateStatus = "rejected"
	GateBlocked  GateStatus = "blocked"
)

type GateKind string

const (
	GateAuthority               GateKind = "authority"
	GateImplementationAuthority GateKind = "implementation_authority"
	GateWorktree                GateKind = "worktree"
	GateBaseline                GateKind = "baseline"
	GateTDDRed                  GateKind = "tdd_red"
	GateTDDGreen                GateKind = "tdd_green"
	GateRefactor                GateKind = "refactor"
	GateTaskReview              GateKind = "task_review"
	GateBranchReview            GateKind = "branch_review"
	GateRootCause               GateKind = "root_cause"
	GateLIVE                    GateKind = "live"
)

type GateResult struct {
	Kind        GateKind   `json:"kind"`
	Status      GateStatus `json:"status"`
	Reason      string     `json:"reason,omitempty"`
	EvidenceIDs []string   `json:"evidence_ids,omitempty"`
}

// Specification is the canonical development specification projection. It
// carries content and references, but does not own a filesystem or store.
type Specification struct {
	SchemaVersion       int       `json:"schema_version"`
	SpecID              string    `json:"spec_id"`
	Title               string    `json:"title"`
	Revision            int       `json:"revision"`
	Status              string    `json:"status"`
	Source              string    `json:"source"`
	ContentHash         string    `json:"content_hash"`
	Content             string    `json:"content,omitempty"`
	Purpose             string    `json:"purpose"`
	Problem             string    `json:"problem"`
	Scope               []string  `json:"scope,omitempty"`
	NonGoals            []string  `json:"non_goals,omitempty"`
	Constraints         []string  `json:"constraints,omitempty"`
	Interfaces          []string  `json:"interfaces,omitempty"`
	AcceptanceCriteria  []string  `json:"acceptance_criteria,omitempty"`
	Risk                string    `json:"risk,omitempty"`
	RollbackExpectation string    `json:"rollback_expectation,omitempty"`
	Supersedes          string    `json:"supersedes,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

const (
	SpecificationDraft    = "draft"
	SpecificationApproved = "approved"
	SpecificationRejected = "rejected"
)

// TestCycle records the deterministic RED/GREEN/REFACTOR contract attached to
// one task. The task itself remains the owner of its lifecycle state.
type TestCycle struct {
	RedCommand      string `json:"red_command,omitempty"`
	ExpectedFailure string `json:"expected_failure,omitempty"`
	GreenCommand    string `json:"green_command,omitempty"`
	RefactorCommand string `json:"refactor_command,omitempty"`
}

type Task struct {
	TaskID               string          `json:"task_id"`
	PlanID               string          `json:"plan_id"`
	Purpose              string          `json:"purpose"`
	ExactFiles           []string        `json:"exact_files,omitempty"`
	InterfacesConsumed   []string        `json:"interfaces_consumed,omitempty"`
	InterfacesProduced   []string        `json:"interfaces_produced,omitempty"`
	Dependencies         []string        `json:"dependencies,omitempty"`
	AssignedSkill        string          `json:"assigned_skill,omitempty"`
	RequiredCapability   string          `json:"required_capability,omitempty"`
	AuthorityRequirement string          `json:"authority_requirement,omitempty"`
	TestCycle            TestCycle       `json:"test_cycle,omitempty"`
	ExactCommands        []string        `json:"exact_commands,omitempty"`
	ExpectedResults      []string        `json:"expected_results,omitempty"`
	ReviewRequirement    string          `json:"review_requirement,omitempty"`
	Rollback             []string        `json:"rollback,omitempty"`
	State                TaskState       `json:"state"`
	TerminalOutcome      TerminalOutcome `json:"terminal_outcome,omitempty"`
	ImplementerAgentID   string          `json:"implementer_agent_id,omitempty"`
	ReviewerAgentID      string          `json:"reviewer_agent_id,omitempty"`
	ValidForRevision     string          `json:"valid_for_revision,omitempty"`
	RootCauseRequired    bool            `json:"root_cause_required,omitempty"`
	CreatedAt            time.Time       `json:"created_at,omitempty"`
	UpdatedAt            time.Time       `json:"updated_at,omitempty"`
}

// Plan is intentionally a plan projection. ImplementationUnitID is a
// reference to the existing Atlas owner and is not a second Unit type.
type Plan struct {
	SchemaVersion        int                 `json:"schema_version"`
	PlanID               string              `json:"plan_id"`
	ImplementationUnitID string              `json:"implementation_unit_id"`
	SpecRef              string              `json:"spec_ref"`
	SpecHash             string              `json:"spec_hash"`
	Revision             string              `json:"revision,omitempty"`
	GlobalConstraints    []string            `json:"global_constraints,omitempty"`
	FileMap              map[string][]string `json:"file_map,omitempty"`
	TaskDAG              map[string][]string `json:"task_dag,omitempty"`
	Tasks                []Task              `json:"tasks,omitempty"`
	CreatedAt            time.Time           `json:"created_at"`
	UpdatedAt            time.Time           `json:"updated_at"`
}

// AdoptionEvidence is an already-issued, CORE-verified adoption result. It
// is a reference, never a user response or a future human decision step.
type AdoptionEvidence struct {
	EvidenceID string    `json:"evidence_id"`
	UnitID     string    `json:"unit_id"`
	SpecRef    string    `json:"spec_ref"`
	SpecHash   string    `json:"spec_hash"`
	Decision   string    `json:"decision"`
	Verified   bool      `json:"verified"`
	CreatedAt  time.Time `json:"created_at"`
}

type ImplementationAuthorityRequest struct {
	ImplementationAuthorityTokenID string    `json:"implementation_authority_token_id"`
	UnitID                         string    `json:"unit_id"`
	SpecRef                        string    `json:"spec_ref"`
	SpecHash                       string    `json:"spec_hash"`
	Issuer                         string    `json:"issuer"`
	Scope                          []string  `json:"scope"`
	Reason                         string    `json:"reason"`
	IssuedAt                       time.Time `json:"issued_at"`
	ExpiresAt                      time.Time `json:"expires_at"`
}

type ImplementationAuthorityToken struct {
	ImplementationAuthorityTokenID string    `json:"implementation_authority_token_id"`
	UnitID                         string    `json:"unit_id"`
	SpecRef                        string    `json:"spec_ref"`
	SpecHash                       string    `json:"spec_hash"`
	Issuer                         string    `json:"issuer"`
	IssuedAt                       time.Time `json:"issued_at"`
	Scope                          []string  `json:"scope"`
	Reason                         string    `json:"reason"`
	ExpiresAt                      time.Time `json:"expires_at"`
	RevokedAt                      time.Time `json:"revoked_at,omitempty"`
}

type ConflictType string

const (
	ConflictReversibleLocalAmbiguity ConflictType = "reversible_local_ambiguity"
	ConflictNonDestructiveDesignGap  ConflictType = "non_destructive_design_gap"
	ConflictDestructiveIrreversible  ConflictType = "destructive_or_irreversible_conflict"
	ConflictProductSemantics         ConflictType = "product_semantics_conflict"
)

type RulingDecision string

const (
	RulingContinue RulingDecision = "continue"
	RulingBlocked  RulingDecision = "blocked"
	RulingRejected RulingDecision = "rejected"
)

type Ruling struct {
	RulingID     string         `json:"ruling_id"`
	UnitID       string         `json:"unit_id"`
	PlanID       string         `json:"plan_id"`
	TaskID       string         `json:"task_id,omitempty"`
	ConflictType ConflictType   `json:"conflict_type"`
	SpecRef      string         `json:"spec_ref"`
	Decision     RulingDecision `json:"decision"`
	Rationale    string         `json:"rationale"`
	Impact       string         `json:"impact"`
	Actor        string         `json:"actor"`
	CreatedAt    time.Time      `json:"created_at"`
}

// EvidenceReceipt is a machine-verifiable reference to a command, artifact,
// revision, and trace. Natural-language status reports are not receipts.
type EvidenceReceipt struct {
	EvidenceID         string    `json:"evidence_id"`
	IdempotencyKey     string    `json:"idempotency_key,omitempty"`
	UnitID             string    `json:"unit_id"`
	PlanID             string    `json:"plan_id"`
	SpecHash           string    `json:"spec_hash"`
	TaskID             string    `json:"task_id,omitempty"`
	Stage              string    `json:"stage"`
	EvidenceType       string    `json:"evidence_type"`
	Command            string    `json:"command,omitempty"`
	ExitCode           int       `json:"exit_code"`
	ResultSummary      string    `json:"result_summary,omitempty"`
	ExpectedFailure    string    `json:"expected_failure,omitempty"`
	ActualFailure      string    `json:"actual_failure,omitempty"`
	ArtifactRef        string    `json:"artifact_ref,omitempty"`
	ArtifactSHA256     string    `json:"artifact_sha256,omitempty"`
	GitRevision        string    `json:"git_revision,omitempty"`
	TraceID            string    `json:"trace_id,omitempty"`
	EventID            string    `json:"event_id,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
	ValidForRevision   string    `json:"valid_for_revision"`
	Verified           bool      `json:"verified"`
	VerificationResult string    `json:"verification_result,omitempty"`
	MachineGenerated   bool      `json:"machine_generated,omitempty"`
	Passed             bool      `json:"passed,omitempty"`
}

type ReviewType string

const (
	ReviewTypeTask   ReviewType = "task"
	ReviewTypeBranch ReviewType = "branch"
)

type ReviewVerdict string

const (
	ReviewAccepted ReviewVerdict = "accepted"
	ReviewRejected ReviewVerdict = "rejected"
	ReviewBlocked  ReviewVerdict = "blocked"
)

type ReviewRecord struct {
	ReviewID           string        `json:"review_id"`
	UnitID             string        `json:"unit_id"`
	PlanID             string        `json:"plan_id"`
	TaskID             string        `json:"task_id,omitempty"`
	ReviewType         ReviewType    `json:"review_type"`
	ImplementerAgentID string        `json:"implementer_agent_id"`
	ReviewerAgentID    string        `json:"reviewer_agent_id"`
	SpecRef            string        `json:"spec_ref"`
	SpecHash           string        `json:"spec_hash"`
	ValidForRevision   string        `json:"valid_for_revision"`
	DiffRef            string        `json:"diff_ref"`
	Findings           []string      `json:"findings,omitempty"`
	Verdict            ReviewVerdict `json:"verdict"`
	EvidenceRefs       []string      `json:"evidence_refs,omitempty"`
	CreatedAt          time.Time     `json:"created_at"`
}

type Assignment struct {
	TaskID     string        `json:"task_id"`
	Role       ExecutionRole `json:"role"`
	AgentID    string        `json:"agent_id,omitempty"`
	Skill      string        `json:"skill,omitempty"`
	AssignedAt time.Time     `json:"assigned_at,omitempty"`
}

type WorktreeEvidence struct {
	WorktreePath    string    `json:"worktree_path"`
	Branch          string    `json:"branch"`
	BaseRevision    string    `json:"base_revision"`
	GitRevision     string    `json:"git_revision,omitempty"`
	Dirty           bool      `json:"dirty"`
	Isolated        bool      `json:"isolated"`
	ReadOnly        bool      `json:"read_only,omitempty"`
	ExceptionReason string    `json:"exception_reason,omitempty"`
	Verified        bool      `json:"verified"`
	CreatedAt       time.Time `json:"created_at"`
}

type BaselineEvidence struct {
	UnitID           string    `json:"unit_id"`
	PlanID           string    `json:"plan_id"`
	SpecRef          string    `json:"spec_ref"`
	SpecHash         string    `json:"spec_hash"`
	ValidForRevision string    `json:"valid_for_revision"`
	WorktreePath     string    `json:"worktree_path"`
	Branch           string    `json:"branch"`
	BaseRevision     string    `json:"base_revision"`
	Command          string    `json:"command"`
	ExitCode         int       `json:"exit_code"`
	ResultSummary    string    `json:"result_summary,omitempty"`
	GitRevision      string    `json:"git_revision"`
	Dirty            bool      `json:"dirty"`
	Verified         bool      `json:"verified"`
	CreatedAt        time.Time `json:"created_at"`
}

type RootCauseEvidence struct {
	EvidenceID                 string    `json:"evidence_id"`
	UnitID                     string    `json:"unit_id"`
	PlanID                     string    `json:"plan_id"`
	TaskID                     string    `json:"task_id,omitempty"`
	SpecRef                    string    `json:"spec_ref"`
	SpecHash                   string    `json:"spec_hash"`
	ValidForRevision           string    `json:"valid_for_revision"`
	Reproduced                 bool      `json:"reproduced"`
	ReproductionRef            string    `json:"reproduction_ref"`
	ErrorLogRef                string    `json:"error_log_ref"`
	TraceRef                   string    `json:"trace_ref"`
	CallPath                   []string  `json:"call_path"`
	Hypothesis                 string    `json:"hypothesis"`
	VerificationRef            string    `json:"verification_ref"`
	ActualFailure              string    `json:"actual_failure,omitempty"`
	FailureCount               int       `json:"failure_count"`
	ArchitectureReviewRequired bool      `json:"architecture_review_required,omitempty"`
	Escalated                  bool      `json:"escalated,omitempty"`
	Verified                   bool      `json:"verified"`
	CreatedAt                  time.Time `json:"created_at"`
}

// LiveGateInput is a pure snapshot of the evidence required to claim
// LIVE_VERIFIED. CheckOK is included for compatibility but is deliberately
// insufficient by itself.
type LiveGateInput struct {
	UnitID                     string
	PlanID                     string
	SpecHash                   string
	Revision                   string
	AcceptedImplementation     bool
	CheckOK                    bool
	FullRelevantTests          bool
	BuildEvidence              EvidenceReceipt
	BuildReceipt               EvidenceReceipt
	EcosystemEvidence          EvidenceReceipt
	DeployEvidence             EvidenceReceipt
	DeploymentReceipt          EvidenceReceipt
	RestartEvidence            EvidenceReceipt
	RestartReceipt             EvidenceReceipt
	ProcessIdentityEvidence    EvidenceReceipt
	ReadinessEvidence          EvidenceReceipt
	ReadinessReceipt           EvidenceReceipt
	ProductionSmokeEvidence    EvidenceReceipt
	ProductionSmokeReceipt     EvidenceReceipt
	ViewerVerificationEvidence EvidenceReceipt
	ViewerVerificationReceipt  EvidenceReceipt
	Evidence                   []EvidenceReceipt
	Reviews                    []ReviewRecord
}

// Ledger is plan-scoped runtime state. It is a projection that can be stored
// by existing CORE persistence; this package does not create a physical store.
type Ledger struct {
	SchemaVersion      int                 `json:"schema_version"`
	UnitID             string              `json:"unit_id"`
	PlanID             string              `json:"plan_id"`
	SpecRef            string              `json:"spec_ref"`
	SpecHash           string              `json:"spec_hash"`
	Revision           string              `json:"revision,omitempty"`
	SupersedesPlanID   string              `json:"supersedes_plan_id,omitempty"`
	SupersedesRevision string              `json:"supersedes_revision,omitempty"`
	CurrentState       string              `json:"current_state"`
	TerminalOutcome    TerminalOutcome     `json:"terminal_outcome,omitempty"`
	Tasks              []Task              `json:"tasks,omitempty"`
	Assignments        []Assignment        `json:"assignments,omitempty"`
	Worktrees          []WorktreeEvidence  `json:"worktrees,omitempty"`
	BaselineEvidence   []BaselineEvidence  `json:"baseline_evidence,omitempty"`
	Rulings            []Ruling            `json:"rulings,omitempty"`
	ReviewFindings     []string            `json:"review_findings,omitempty"`
	ReviewRecords      []ReviewRecord      `json:"review_records,omitempty"`
	EvidenceRefs       []EvidenceReceipt   `json:"evidence_refs,omitempty"`
	RootCauses         []RootCauseEvidence `json:"root_causes,omitempty"`
	BlockedReason      string              `json:"blocked_reason,omitempty"`
	LastCheckpointAt   time.Time           `json:"last_checkpoint_at"`
	ResumeToken        string              `json:"resume_token,omitempty"`
	CheckOK            bool                `json:"check_ok,omitempty"`
}

var (
	ErrInvalidTransition                   = errors.New("invalid development methodology transition")
	ErrInvalidState                        = errors.New("invalid development methodology state")
	ErrMissingEvidence                     = errors.New("required development evidence is missing")
	ErrUnverifiedEvidence                  = errors.New("evidence is not CORE-verified")
	ErrNaturalLanguageEvidence             = errors.New("natural-language report is not evidence")
	ErrAuthorityDenied                     = errors.New("authority does not permit operation")
	ErrImplementationAuthorityRequired     = errors.New("implementation_authority token is required")
	ErrInvalidImplementationAuthorityToken = errors.New("invalid implementation_authority token")
	ErrStaleImplementationAuthorityToken   = errors.New("implementation_authority token is stale or expired")
	ErrWorktreeRequired                    = errors.New("isolated worktree is required")
	ErrBaselineRequired                    = errors.New("verified clean baseline is required")
	ErrReviewRequired                      = errors.New("independent review is required")
	ErrRootCauseRequired                   = errors.New("root-cause evidence is required")
	ErrSpecHashMismatch                    = errors.New("plan and specification hashes differ")
	ErrConflictBlocked                     = errors.New("conflict requires blocked ruling")
	ErrIdempotencyConflict                 = errors.New("idempotency key has conflicting payload")
	ErrRevisionRolloverRequired            = errors.New("explicit ledger revision rollover is required")
	ErrTerminalOutcomeRequired             = errors.New("terminal outcome is required")
	ErrLiveGate                            = errors.New("LIVE_VERIFIED gate is incomplete")
	ErrSecretDetected                      = errors.New("secret value must be redacted")
)

func normalized(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

func normalizedUpper(value string) string { return strings.ToUpper(strings.TrimSpace(value)) }

func validSHA256(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func hashText(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func nowFrom(clock Clock) time.Time {
	if clock == nil {
		return RealClock{}.Now()
	}
	return clock.Now()
}

func firstNonZeroReceipt(values ...EvidenceReceipt) EvidenceReceipt {
	for _, value := range values {
		if strings.TrimSpace(value.EvidenceID) != "" || strings.TrimSpace(value.EvidenceType) != "" || strings.TrimSpace(value.Stage) != "" {
			return value
		}
	}
	return EvidenceReceipt{}
}

func (e EvidenceReceipt) Key() string {
	if strings.TrimSpace(e.IdempotencyKey) != "" {
		return "idempotency:" + strings.TrimSpace(e.IdempotencyKey)
	}
	if strings.TrimSpace(e.EvidenceID) != "" {
		return "evidence:" + strings.TrimSpace(e.EvidenceID)
	}
	return strings.Join([]string{
		strings.TrimSpace(e.UnitID), strings.TrimSpace(e.TaskID),
		normalizedUpper(e.Stage), normalized(e.EvidenceType),
		strings.TrimSpace(e.ValidForRevision), strings.TrimSpace(e.ArtifactRef),
	}, "\x00")
}

func (e EvidenceReceipt) IsMachineVerifiable() bool {
	return strings.TrimSpace(e.Command) != "" || strings.TrimSpace(e.ArtifactRef) != "" || strings.TrimSpace(e.TraceID) != "" || strings.TrimSpace(e.EventID) != ""
}

func (e EvidenceReceipt) IsVerifiedSuccess() bool {
	return e.Verified && normalized(e.VerificationResult) != "rejected" && (e.Passed || e.ExitCode == 0)
}

func (s TaskState) IsTerminal() bool {
	switch s {
	case TaskDone, TaskBlocked, TaskFailed, TaskCancelled:
		return true
	default:
		return false
	}
}

func (o TerminalOutcome) IsTerminal() bool {
	switch o {
	case OutcomeOK, OutcomeFailed, OutcomeBlocked, OutcomeCancelled:
		return true
	default:
		return false
	}
}

func (t ImplementationAuthorityToken) IsRevoked() bool { return !t.RevokedAt.IsZero() }

func NewTask(planID, taskID, purpose string) Task {
	now := RealClock{}.Now()
	return Task{PlanID: strings.TrimSpace(planID), TaskID: strings.TrimSpace(taskID), Purpose: strings.TrimSpace(purpose), State: TaskPending, CreatedAt: now, UpdatedAt: now}
}

func NewLedger(unitID, planID, specRef, specHash string) Ledger {
	now := RealClock{}.Now()
	return Ledger{SchemaVersion: SchemaVersion, UnitID: strings.TrimSpace(unitID), PlanID: strings.TrimSpace(planID), SpecRef: strings.TrimSpace(specRef), SpecHash: strings.TrimSpace(specHash), CurrentState: string(TaskPending), LastCheckpointAt: now}
}

// HashContent returns the canonical lowercase SHA-256 used by Specification
// and Plan references.
func HashContent(content string) string { return hashText(content) }

// Backlog delivery constants are aliases to the existing owner package. This
// package never defines another Atlas delivery state machine.
const (
	DeliveryNone             = domainbacklog.DeliveryNone
	DeliveryQueued           = domainbacklog.DeliveryQueued
	DeliverySpec             = domainbacklog.DeliverySpec
	DeliveryTDDRed           = domainbacklog.DeliveryTDDRed
	DeliveryTDDGreen         = domainbacklog.DeliveryTDDGreen
	DeliveryRefactor         = domainbacklog.DeliveryRefactor
	DeliveryE2EPredeploy     = domainbacklog.DeliveryE2EPredeploy
	DeliveryBuild            = domainbacklog.DeliveryBuild
	DeliveryDeploy           = domainbacklog.DeliveryDeploy
	DeliveryRestart          = domainbacklog.DeliveryRestart
	DeliveryPostDeployVerify = domainbacklog.DeliveryPostDeployVerify
	DeliveryLiveVerified     = domainbacklog.DeliveryLiveVerified
	DeliveryDone             = domainbacklog.DeliveryDone
	DeliveryBlocked          = domainbacklog.DeliveryBlocked
	DeliveryRejected         = domainbacklog.DeliveryRejected
)

// StringError adds a stable field name while retaining errors.Is support from
// callers that need to classify a failed gate.
func StringError(base error, detail string) error {
	if strings.TrimSpace(detail) == "" {
		return base
	}
	return fmt.Errorf("%w: %s", base, detail)
}
