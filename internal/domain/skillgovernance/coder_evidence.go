package skillgovernance

import modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"

// CoderProposalEvidence is a durable, review-only artifact source for Skill
// Change Eval. It stores the Coder proposal patch separately from the
// transcript-style execution summary so the Viewer can attach each as evidence
// without mixing raw tool output into prompts.
type CoderProposalEvidence struct {
	TaskID           modulecore.TaskID `json:"task_id"`
	SessionID        string
	Route            string
	Agent            string
	TaskText         string
	Plan             string
	Patch            string
	Risk             string
	CostHint         string
	ExecutionSummary string
	FormattedResult  string
	ExecutionError   string
	Success          bool
}

type CoderProposalEvidencePaths struct {
	RootPath            string
	SkillDiffPath       string
	AgentTranscriptPath string
}
