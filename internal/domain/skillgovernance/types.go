package skillgovernance

import (
	"strings"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	ScopeCore    = "core"
	ScopePlugin  = "plugin"
	ScopeProject = "project"

	TriggerStatusTriggered = "triggered"
	TriggerStatusMissed    = "missed"

	GateStatusPassed  = "passed"
	GateStatusBlocked = "blocked"
)

type SkillManifest struct {
	SkillID             string              `json:"skill_id" yaml:"skill_id"`
	Name                string              `json:"name" yaml:"name"`
	Scope               string              `json:"scope" yaml:"scope"`
	Version             string              `json:"version" yaml:"version"`
	Path                string              `json:"path" yaml:"path"`
	Description         string              `json:"description,omitempty" yaml:"description,omitempty"`
	KeywordTriggers     []string            `json:"keyword_triggers,omitempty" yaml:"keyword_triggers,omitempty"`
	IntentTriggers      []string            `json:"intent_triggers,omitempty" yaml:"intent_triggers,omitempty"`
	Enabled             bool                `json:"enabled" yaml:"enabled"`
	UpdatedAt           time.Time           `json:"updated_at" yaml:"updated_at"`
	DevelopmentContract DevelopmentContract `json:"development_contract" yaml:"development_contract"`
}

// DevelopmentContract describes the deterministic inputs, capability
// prerequisites, and evaluation boundary for a skill. It is declarative
// metadata only: it never grants authority or changes runtime policy.
type DevelopmentContract struct {
	RequiredCapability   string   `json:"required_capability" yaml:"required_capability"`
	RequiredTools        []string `json:"required_tools" yaml:"required_tools"`
	RequiredKnowledge    []string `json:"required_knowledge" yaml:"required_knowledge"`
	AuthorityRequirement string   `json:"authority_requirement" yaml:"authority_requirement"`
	InputContract        string   `json:"input_contract" yaml:"input_contract"`
	OutputContract       string   `json:"output_contract" yaml:"output_contract"`
	CostHint             string   `json:"cost_hint" yaml:"cost_hint"`
	RiskLevel            string   `json:"risk_level" yaml:"risk_level"`
	EvaluationMethod     string   `json:"evaluation_method" yaml:"evaluation_method"`
	Version              string   `json:"version" yaml:"version"`
}

// IsZero reports whether a manifest carries no development contract. An
// absent contract keeps legacy manifests valid while a present contract is
// validated as a complete unit.
func (c DevelopmentContract) IsZero() bool {
	return strings.TrimSpace(c.RequiredCapability) == "" &&
		len(c.RequiredTools) == 0 &&
		len(c.RequiredKnowledge) == 0 &&
		strings.TrimSpace(c.AuthorityRequirement) == "" &&
		strings.TrimSpace(c.InputContract) == "" &&
		strings.TrimSpace(c.OutputContract) == "" &&
		strings.TrimSpace(c.CostHint) == "" &&
		strings.TrimSpace(c.RiskLevel) == "" &&
		strings.TrimSpace(c.EvaluationMethod) == "" &&
		strings.TrimSpace(c.Version) == ""
}

type TaskContext struct {
	Text         string
	Intent       string
	Agent        string
	WorkstreamID string
}

type SkillTriggerDecision struct {
	SkillID       string   `json:"skill_id"`
	TriggerType   string   `json:"trigger_type"`
	TriggerReason string   `json:"trigger_reason"`
	Matched       bool     `json:"matched"`
	MatchedTerms  []string `json:"matched_terms,omitempty"`
}

type SkillTriggerLog struct {
	EventID       string    `json:"event_id"`
	SkillID       string    `json:"skill_id"`
	TriggerType   string    `json:"trigger_type"`
	TriggerReason string    `json:"trigger_reason,omitempty"`
	Agent         string    `json:"agent,omitempty"`
	WorkstreamID  string    `json:"workstream_id,omitempty"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
}

type SkillChangeLog struct {
	ChangeID               string    `json:"change_id"`
	SkillID                string    `json:"skill_id"`
	OldVersion             string    `json:"old_version,omitempty"`
	NewVersion             string    `json:"new_version,omitempty"`
	ChangeReason           string    `json:"change_reason,omitempty"`
	ExpectedBehaviorChange string    `json:"expected_behavior_change,omitempty"`
	EvalResult             string    `json:"eval_result,omitempty"`
	EvidenceSummary        string    `json:"evidence_summary,omitempty"`
	CreatedAt              time.Time `json:"created_at"`
}

type ContributionGateLog struct {
	EventID             string    `json:"event_id"`
	Repo                string    `json:"repo"`
	TargetBranch        string    `json:"target_branch,omitempty"`
	ProblemStatement    string    `json:"problem_statement,omitempty"`
	ExistingPRsChecked  bool      `json:"existing_prs_checked"`
	RealProblemVerified bool      `json:"real_problem_verified"`
	CoreChangeVerified  bool      `json:"core_change_verified"`
	DiffReviewed        bool      `json:"diff_reviewed"`
	TestResult          string    `json:"test_result,omitempty"`
	GateStatus          string    `json:"gate_status"`
	CreatedAt           time.Time `json:"created_at"`
}

type ExternalPRSubmitRecord struct {
	SubmitID            string    `json:"submit_id"`
	ContributionEventID string    `json:"contribution_event_id"`
	Repo                string    `json:"repo"`
	TargetBranch        string    `json:"target_branch,omitempty"`
	Title               string    `json:"title,omitempty"`
	DiffPath            string    `json:"diff_path,omitempty"`
	TestResult          string    `json:"test_result,omitempty"`
	SubmitStatus        string    `json:"submit_status"`
	PRURL               string    `json:"pr_url,omitempty"`
	FailureReason       string    `json:"failure_reason,omitempty"`
	ExternalPRCreated   bool      `json:"external_pr_created"`
	PostSubmitVerified  bool      `json:"post_submit_verified"`
	PostSubmitEvidence  string    `json:"post_submit_evidence,omitempty"`
	PRAdapter           string    `json:"pr_adapter,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
}

type CoderTranscriptEntry struct {
	EventID      string            `json:"event_id"`
	TaskID       modulecore.TaskID `json:"task_id"`
	SessionID    string            `json:"session_id,omitempty"`
	Route        string            `json:"route,omitempty"`
	Agent        string            `json:"agent,omitempty"`
	Role         string            `json:"role"`
	Segment      string            `json:"segment"`
	Text         string            `json:"text,omitempty"`
	EvidencePath string            `json:"evidence_path,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
}
