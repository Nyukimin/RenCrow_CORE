package developmentmethodology

import (
	"encoding/json"
	"regexp"
	"strings"
)

const RedactedValue = "[REDACTED]"

var (
	pemSecretPattern        = regexp.MustCompile(`(?s)-----BEGIN [^-]*(?:PRIVATE KEY|TOKEN|CREDENTIAL)[^-]*-----.*?-----END [^-]*(?:PRIVATE KEY|TOKEN|CREDENTIAL)[^-]*-----`)
	assignmentSecretPattern = regexp.MustCompile(`(?i)(\b(?:password|passwd|token|api[_-]?key|secret|authorization|cookie|credential|private[_-]?key)\b\s*[:=]\s*)(?:"[^"]*"|'[^']*'|Bearer\s+[^\s,;&]+|Basic\s+[^\s,;&]+|[^\s,;&]+)`)
	querySecretPattern      = regexp.MustCompile(`(?i)([?&](?:password|passwd|token|api[_-]?key|secret|authorization|cookie|credential)=)[^&\s]+`)
	bearerSecretPattern     = regexp.MustCompile(`(?i)\b(Bearer|Basic)\s+[^\s,;&]+`)
	prefixSecretPattern     = regexp.MustCompile(`(?i)\b(?:sk-[a-z0-9_-]{8,}|gh[pousr]_[a-z0-9_]{8,})\b`)
)

// RedactSecrets removes credentials from human- and machine-readable text.
// It is intentionally conservative about ordinary words: only known secret
// key names, authorization schemes, PEM blocks, and well-known key prefixes
// are replaced.
func RedactSecrets(value string) string {
	if value == "" {
		return value
	}
	value = pemSecretPattern.ReplaceAllString(value, RedactedValue)
	value = querySecretPattern.ReplaceAllString(value, `${1}`+RedactedValue)
	value = assignmentSecretPattern.ReplaceAllString(value, `${1}`+RedactedValue)
	value = bearerSecretPattern.ReplaceAllString(value, `${1} `+RedactedValue)
	return prefixSecretPattern.ReplaceAllString(value, RedactedValue)
}

func redactSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, len(values))
	for index, value := range values {
		out[index] = RedactSecrets(value)
	}
	return out
}

func (spec Specification) Redacted() Specification {
	spec.Content = RedactSecrets(spec.Content)
	spec.Source = RedactSecrets(spec.Source)
	spec.Purpose = RedactSecrets(spec.Purpose)
	spec.Problem = RedactSecrets(spec.Problem)
	spec.Scope = redactSlice(spec.Scope)
	spec.NonGoals = redactSlice(spec.NonGoals)
	spec.Constraints = redactSlice(spec.Constraints)
	spec.Interfaces = redactSlice(spec.Interfaces)
	spec.AcceptanceCriteria = redactSlice(spec.AcceptanceCriteria)
	spec.Risk = RedactSecrets(spec.Risk)
	spec.RollbackExpectation = RedactSecrets(spec.RollbackExpectation)
	return spec
}

func (plan Plan) Redacted() Plan {
	plan.GlobalConstraints = redactSlice(plan.GlobalConstraints)
	for index := range plan.Tasks {
		plan.Tasks[index] = plan.Tasks[index].Redacted()
	}
	return plan
}

func (task Task) Redacted() Task {
	task.Purpose = RedactSecrets(task.Purpose)
	task.ExactFiles = redactSlice(task.ExactFiles)
	task.InterfacesConsumed = redactSlice(task.InterfacesConsumed)
	task.InterfacesProduced = redactSlice(task.InterfacesProduced)
	task.Dependencies = redactSlice(task.Dependencies)
	task.AssignedSkill = RedactSecrets(task.AssignedSkill)
	task.RequiredCapability = RedactSecrets(task.RequiredCapability)
	task.AuthorityRequirement = RedactSecrets(task.AuthorityRequirement)
	task.TestCycle.RedCommand = RedactSecrets(task.TestCycle.RedCommand)
	task.TestCycle.ExpectedFailure = RedactSecrets(task.TestCycle.ExpectedFailure)
	task.TestCycle.GreenCommand = RedactSecrets(task.TestCycle.GreenCommand)
	task.TestCycle.RefactorCommand = RedactSecrets(task.TestCycle.RefactorCommand)
	task.ExactCommands = redactSlice(task.ExactCommands)
	task.ExpectedResults = redactSlice(task.ExpectedResults)
	task.ReviewRequirement = RedactSecrets(task.ReviewRequirement)
	task.Rollback = redactSlice(task.Rollback)
	return task
}

func (token ImplementationAuthorityToken) Redacted() ImplementationAuthorityToken {
	token.SpecRef = RedactSecrets(token.SpecRef)
	token.Issuer = RedactSecrets(token.Issuer)
	token.Scope = redactSlice(token.Scope)
	token.Reason = RedactSecrets(token.Reason)
	return token
}

func (ruling Ruling) Redacted() Ruling {
	ruling.SpecRef = RedactSecrets(ruling.SpecRef)
	ruling.Rationale = RedactSecrets(ruling.Rationale)
	ruling.Impact = RedactSecrets(ruling.Impact)
	ruling.Actor = RedactSecrets(ruling.Actor)
	return ruling
}

func (receipt EvidenceReceipt) Redacted() EvidenceReceipt {
	receipt.Command = RedactSecrets(receipt.Command)
	receipt.ResultSummary = RedactSecrets(receipt.ResultSummary)
	receipt.ExpectedFailure = RedactSecrets(receipt.ExpectedFailure)
	receipt.ActualFailure = RedactSecrets(receipt.ActualFailure)
	receipt.ArtifactRef = RedactSecrets(receipt.ArtifactRef)
	receipt.TraceID = RedactSecrets(receipt.TraceID)
	receipt.EventID = RedactSecrets(receipt.EventID)
	return receipt
}

func (review ReviewRecord) Redacted() ReviewRecord {
	review.SpecRef = RedactSecrets(review.SpecRef)
	review.DiffRef = RedactSecrets(review.DiffRef)
	review.Findings = redactSlice(review.Findings)
	review.EvidenceRefs = redactSlice(review.EvidenceRefs)
	return review
}

func (worktree WorktreeEvidence) Redacted() WorktreeEvidence {
	worktree.WorktreePath = RedactSecrets(worktree.WorktreePath)
	worktree.Branch = RedactSecrets(worktree.Branch)
	worktree.ExceptionReason = RedactSecrets(worktree.ExceptionReason)
	return worktree
}

func (baseline BaselineEvidence) Redacted() BaselineEvidence {
	baseline.WorktreePath = RedactSecrets(baseline.WorktreePath)
	baseline.Branch = RedactSecrets(baseline.Branch)
	baseline.Command = RedactSecrets(baseline.Command)
	baseline.ResultSummary = RedactSecrets(baseline.ResultSummary)
	return baseline
}

func (root RootCauseEvidence) Redacted() RootCauseEvidence {
	root.ReproductionRef = RedactSecrets(root.ReproductionRef)
	root.ErrorLogRef = RedactSecrets(root.ErrorLogRef)
	root.TraceRef = RedactSecrets(root.TraceRef)
	root.CallPath = redactSlice(root.CallPath)
	root.Hypothesis = RedactSecrets(root.Hypothesis)
	root.VerificationRef = RedactSecrets(root.VerificationRef)
	root.ActualFailure = RedactSecrets(root.ActualFailure)
	return root
}

func (ledger Ledger) Redacted() Ledger {
	ledger.SpecRef = RedactSecrets(ledger.SpecRef)
	ledger.BlockedReason = RedactSecrets(ledger.BlockedReason)
	ledger.ResumeToken = RedactSecrets(ledger.ResumeToken)
	ledger.ReviewFindings = redactSlice(ledger.ReviewFindings)
	for index := range ledger.Tasks {
		ledger.Tasks[index] = ledger.Tasks[index].Redacted()
	}
	for index := range ledger.Rulings {
		ledger.Rulings[index] = ledger.Rulings[index].Redacted()
	}
	for index := range ledger.Worktrees {
		ledger.Worktrees[index] = ledger.Worktrees[index].Redacted()
	}
	for index := range ledger.BaselineEvidence {
		ledger.BaselineEvidence[index] = ledger.BaselineEvidence[index].Redacted()
	}
	for index := range ledger.ReviewRecords {
		ledger.ReviewRecords[index] = ledger.ReviewRecords[index].Redacted()
	}
	for index := range ledger.EvidenceRefs {
		ledger.EvidenceRefs[index] = ledger.EvidenceRefs[index].Redacted()
	}
	for index := range ledger.RootCauses {
		ledger.RootCauses[index] = ledger.RootCauses[index].Redacted()
	}
	return ledger
}

// MarshalJSON methods make the redaction boundary hard to accidentally skip
// when a domain artifact is sent to an event or Viewer adapter.
func (spec Specification) MarshalJSON() ([]byte, error) {
	type plain Specification
	return json.Marshal(plain(spec.Redacted()))
}

func (plan Plan) MarshalJSON() ([]byte, error) {
	type plain Plan
	return json.Marshal(plain(plan.Redacted()))
}

func (token ImplementationAuthorityToken) MarshalJSON() ([]byte, error) {
	type plain ImplementationAuthorityToken
	return json.Marshal(plain(token.Redacted()))
}

func (ruling Ruling) MarshalJSON() ([]byte, error) {
	type plain Ruling
	return json.Marshal(plain(ruling.Redacted()))
}

func (receipt EvidenceReceipt) MarshalJSON() ([]byte, error) {
	type plain EvidenceReceipt
	return json.Marshal(plain(receipt.Redacted()))
}

func (review ReviewRecord) MarshalJSON() ([]byte, error) {
	type plain ReviewRecord
	return json.Marshal(plain(review.Redacted()))
}

func (root RootCauseEvidence) MarshalJSON() ([]byte, error) {
	type plain RootCauseEvidence
	return json.Marshal(plain(root.Redacted()))
}

func (ledger Ledger) MarshalJSON() ([]byte, error) {
	type plain Ledger
	return json.Marshal(plain(ledger.Redacted()))
}

// ContainsSecret reports whether replacing recognized secret material would
// change the value. It is useful to fail closed at adapter boundaries.
func ContainsSecret(value string) bool { return strings.TrimSpace(value) != RedactSecrets(value) }
