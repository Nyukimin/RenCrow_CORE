package skillgovernance

import "time"

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
	SkillID               string    `json:"skill_id"`
	Name                  string    `json:"name"`
	Scope                 string    `json:"scope"`
	Version               string    `json:"version"`
	Path                  string    `json:"path"`
	Description           string    `json:"description,omitempty"`
	KeywordTriggers       []string  `json:"keyword_triggers,omitempty"`
	IntentTriggers        []string  `json:"intent_triggers,omitempty"`
	HumanApprovalRequired bool      `json:"human_approval_required,omitempty"`
	Enabled               bool      `json:"enabled"`
	UpdatedAt             time.Time `json:"updated_at"`
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
	HumanApprovalStatus    string    `json:"human_approval_status,omitempty"`
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
	DiffHumanApproved   bool      `json:"diff_human_approved"`
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
	ApprovalStatus      string    `json:"approval_status"`
	HumanApproved       bool      `json:"human_approved"`
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
	EventID      string    `json:"event_id"`
	JobID        string    `json:"job_id,omitempty"`
	SessionID    string    `json:"session_id,omitempty"`
	Route        string    `json:"route,omitempty"`
	Agent        string    `json:"agent,omitempty"`
	Role         string    `json:"role"`
	Segment      string    `json:"segment"`
	Text         string    `json:"text,omitempty"`
	EvidencePath string    `json:"evidence_path,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}
