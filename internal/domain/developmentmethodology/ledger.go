package developmentmethodology

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func (ledger Ledger) Validate() error {
	if strings.TrimSpace(ledger.UnitID) == "" || strings.TrimSpace(ledger.PlanID) == "" || strings.TrimSpace(ledger.SpecRef) == "" {
		return fmt.Errorf("ledger unit_id, plan_id, and spec_ref are required")
	}
	if !validSHA256(ledger.SpecHash) {
		return fmt.Errorf("ledger spec_hash must be a SHA-256 hex digest")
	}
	if err := ValidateLedgerState(ledger.CurrentState); err != nil {
		return err
	}
	if strings.TrimSpace(ledger.Revision) == "" {
		if len(ledger.EvidenceRefs) > 0 || len(ledger.ReviewRecords) > 0 || len(ledger.BaselineEvidence) > 0 || len(ledger.RootCauses) > 0 {
			return fmt.Errorf("ledger revision is required when evidence is present")
		}
	} else if strings.ContainsAny(ledger.Revision, " \t\r\n") {
		return fmt.Errorf("ledger revision must be a bounded revision token")
	}
	if (strings.TrimSpace(ledger.SupersedesPlanID) == "") != (strings.TrimSpace(ledger.SupersedesRevision) == "") {
		return fmt.Errorf("ledger rollover must identify both superseded plan and revision")
	}
	if strings.ContainsAny(ledger.SupersedesPlanID, " \t\r\n") || strings.ContainsAny(ledger.SupersedesRevision, " \t\r\n") {
		return fmt.Errorf("ledger rollover references must be bounded tokens")
	}
	if ledgerStateIsTerminal(ledger.CurrentState) {
		if ledger.TerminalOutcome == "" {
			return fmt.Errorf("%w: terminal ledger state requires terminal_outcome", ErrTerminalOutcomeRequired)
		}
		if err := ValidateTerminalOutcome(ledger.TerminalOutcome); err != nil {
			return err
		}
		if expected := ledgerOutcomeForState(ledger.CurrentState); expected != ledger.TerminalOutcome {
			return fmt.Errorf("terminal_outcome does not match ledger current_state")
		}
	} else if ledger.TerminalOutcome != "" {
		return fmt.Errorf("non-terminal ledger cannot have terminal_outcome")
	}
	if ledger.TerminalOutcome == OutcomeBlocked && strings.TrimSpace(ledger.BlockedReason) == "" {
		return fmt.Errorf("blocked terminal outcome requires blocked_reason")
	}
	seenTasks := make(map[string]struct{}, len(ledger.Tasks))
	for _, task := range ledger.Tasks {
		if task.PlanID != ledger.PlanID {
			return fmt.Errorf("ledger task %q belongs to another plan", task.TaskID)
		}
		if err := ValidateTask(task); err != nil {
			return err
		}
		if _, exists := seenTasks[task.TaskID]; exists {
			return fmt.Errorf("duplicate ledger task %q", task.TaskID)
		}
		seenTasks[task.TaskID] = struct{}{}
		if strings.TrimSpace(task.ValidForRevision) != "" && task.ValidForRevision != ledger.Revision {
			return fmt.Errorf("task %q is for revision %q, ledger is %q", task.TaskID, task.ValidForRevision, ledger.Revision)
		}
	}
	for _, worktree := range ledger.Worktrees {
		if err := ValidateWorktreeGate(worktree); err != nil {
			return err
		}
	}
	seenBaselines := make(map[string]struct{}, len(ledger.BaselineEvidence))
	for _, baseline := range ledger.BaselineEvidence {
		if err := ValidateBaselineGate(baseline); err != nil {
			return err
		}
		if baseline.UnitID != ledger.UnitID || baseline.PlanID != ledger.PlanID || baseline.SpecRef != ledger.SpecRef || !strings.EqualFold(baseline.SpecHash, ledger.SpecHash) || baseline.ValidForRevision != ledger.Revision {
			return fmt.Errorf("baseline belongs to another unit, plan, specification, or revision")
		}
		key := baselineKey(baseline)
		if _, exists := seenBaselines[key]; exists {
			return fmt.Errorf("duplicate ledger baseline %q", key)
		}
		seenBaselines[key] = struct{}{}
	}
	seenEvidence := make(map[string]struct{}, len(ledger.EvidenceRefs))
	for _, receipt := range ledger.EvidenceRefs {
		if err := ValidateEvidenceReceipt(receipt); err != nil {
			return err
		}
		if receipt.UnitID != ledger.UnitID || receipt.PlanID != ledger.PlanID || !strings.EqualFold(receipt.SpecHash, ledger.SpecHash) || receipt.ValidForRevision != ledger.Revision {
			return fmt.Errorf("evidence %q belongs to another unit, plan, specification, or revision", receipt.EvidenceID)
		}
		if receipt.TaskID != "" {
			if _, exists := seenTasks[receipt.TaskID]; !exists {
				return fmt.Errorf("evidence %q references unknown task %q", receipt.EvidenceID, receipt.TaskID)
			}
		}
		key := receipt.Key()
		if _, exists := seenEvidence[key]; exists {
			return fmt.Errorf("duplicate ledger evidence key %q", key)
		}
		seenEvidence[key] = struct{}{}
	}
	for _, ruling := range ledger.Rulings {
		if ruling.UnitID != ledger.UnitID || ruling.PlanID != ledger.PlanID {
			return fmt.Errorf("ruling %q belongs to another plan", ruling.RulingID)
		}
		if err := ValidateRuling(ruling); err != nil {
			return err
		}
	}
	for _, review := range ledger.ReviewRecords {
		if err := ValidateReviewRecord(review); err != nil {
			return err
		}
		if review.UnitID != ledger.UnitID || review.PlanID != ledger.PlanID || review.SpecRef != ledger.SpecRef || !strings.EqualFold(review.SpecHash, ledger.SpecHash) || review.ValidForRevision != ledger.Revision {
			return fmt.Errorf("review %q belongs to another unit, plan, specification, or revision", review.ReviewID)
		}
		if review.TaskID != "" {
			if _, exists := seenTasks[review.TaskID]; !exists {
				return fmt.Errorf("review %q references unknown task %q", review.ReviewID, review.TaskID)
			}
		}
		for _, evidenceID := range review.EvidenceRefs {
			found := false
			for _, receipt := range ledger.EvidenceRefs {
				if receipt.EvidenceID == evidenceID {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("review %q references unknown evidence %q", review.ReviewID, evidenceID)
			}
		}
	}
	seenRoots := make(map[string]struct{}, len(ledger.RootCauses))
	for _, root := range ledger.RootCauses {
		if err := ValidateRootCauseGate(root); err != nil {
			return err
		}
		if root.UnitID != ledger.UnitID || root.PlanID != ledger.PlanID || root.SpecRef != ledger.SpecRef || !strings.EqualFold(root.SpecHash, ledger.SpecHash) || root.ValidForRevision != ledger.Revision {
			return fmt.Errorf("root cause %q belongs to another unit, plan, specification, or revision", root.EvidenceID)
		}
		if _, exists := seenTasks[root.TaskID]; !exists {
			return fmt.Errorf("root cause %q references unknown task %q", root.EvidenceID, root.TaskID)
		}
		if _, exists := seenRoots[root.EvidenceID]; exists {
			return fmt.Errorf("duplicate ledger root cause %q", root.EvidenceID)
		}
		seenRoots[root.EvidenceID] = struct{}{}
	}
	for _, task := range ledger.Tasks {
		if !task.RootCauseRequired {
			continue
		}
		found := false
		for _, root := range ledger.RootCauses {
			if root.TaskID == task.TaskID {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: task %q requires root-cause evidence", ErrRootCauseRequired, task.TaskID)
		}
	}
	if ledger.LastCheckpointAt.IsZero() {
		return fmt.Errorf("last_checkpoint_at is required")
	}
	return nil
}

// ValidateLedgerProgress validates an append-only checkpoint against the
// preceding checkpoint. Ledger is a projection of the existing Atlas/Task
// lifecycle; callers cannot post an arbitrary snapshot that skips its gates.
func ValidateLedgerProgress(previous, next Ledger) error {
	if err := previous.Validate(); err != nil {
		return fmt.Errorf("previous ledger: %w", err)
	}
	if err := next.Validate(); err != nil {
		return fmt.Errorf("next ledger: %w", err)
	}
	if previous.UnitID != next.UnitID || previous.PlanID != next.PlanID || previous.SpecRef != next.SpecRef || !strings.EqualFold(previous.SpecHash, next.SpecHash) || previous.Revision != next.Revision {
		return ValidateLedgerRollover(previous, next)
	}
	if strings.TrimSpace(next.SupersedesPlanID) != "" || strings.TrimSpace(next.SupersedesRevision) != "" {
		return fmt.Errorf("%w: rollover marker is only valid on a new ledger boundary", ErrRevisionRolloverRequired)
	}
	if !next.LastCheckpointAt.After(previous.LastCheckpointAt) {
		return fmt.Errorf("ledger checkpoint time must increase")
	}
	if err := ValidateLedgerTransition(previous.CurrentState, next.CurrentState); err != nil {
		return err
	}
	if missingTasks(previous.Tasks, next.Tasks) || missingAssignments(previous.Assignments, next.Assignments) || missingWorktrees(previous.Worktrees, next.Worktrees) || missingEvidence(previous.EvidenceRefs, next.EvidenceRefs) || missingReviews(previous.ReviewRecords, next.ReviewRecords) || missingBaselines(previous.BaselineEvidence, next.BaselineEvidence) || missingRootCauses(previous.RootCauses, next.RootCauses) || missingRulings(previous.Rulings, next.Rulings) || missingStrings(previous.ReviewFindings, next.ReviewFindings) {
		return fmt.Errorf("ledger checkpoint cannot remove or mutate prior append-only content")
	}
	switch normalizedUpper(next.CurrentState) {
	case string(TaskRedVerified):
		if len(next.Worktrees) == 0 || len(next.BaselineEvidence) == 0 {
			return fmt.Errorf("%w: worktree and baseline", ErrBaselineRequired)
		}
		for _, baseline := range next.BaselineEvidence {
			if err := ValidateBaselineGate(baseline); err != nil {
				return err
			}
		}
		red, ok := ledgerEvidence(next, string(DeliveryTDDRed), "tdd_red", "execution_report")
		if !ok {
			return fmt.Errorf("%w: RED", ErrMissingEvidence)
		}
		if err := ValidateTDDRedEvidence(red); err != nil {
			return err
		}
		for _, task := range next.Tasks {
			if task.RootCauseRequired && !ledgerHasRootCause(next, task.TaskID) {
				return fmt.Errorf("%w: task %s", ErrRootCauseRequired, task.TaskID)
			}
		}
	case string(TaskGreenVerified):
		red, redOK := ledgerEvidence(next, string(DeliveryTDDRed), "tdd_red", "execution_report")
		green, greenOK := ledgerEvidence(next, string(DeliveryTDDGreen), "tdd_green")
		if !redOK || !greenOK {
			return fmt.Errorf("%w: RED and GREEN", ErrMissingEvidence)
		}
		if err := ValidateTDDGreenEvidence(green, red); err != nil {
			return err
		}
	case string(TaskRefactored):
		green, greenOK := ledgerEvidence(next, string(DeliveryTDDGreen), "tdd_green")
		refactor, refactorOK := ledgerEvidence(next, string(DeliveryRefactor), "refactor")
		if !greenOK || !refactorOK {
			return fmt.Errorf("%w: GREEN and REFACTOR", ErrMissingEvidence)
		}
		if err := ValidateRefactorEvidence(refactor, green); err != nil {
			return err
		}
	case string(TaskReviewed):
		if !ledgerHasAcceptedReview(next, ReviewTypeTask) || !ledgerHasAcceptedReview(next, ReviewTypeBranch) {
			return fmt.Errorf("%w: accepted task and branch review", ErrReviewRequired)
		}
	case string(TaskDone):
		if err := next.ValidateLiveGate(); err != nil {
			return err
		}
	}
	return nil
}

// ValidateLedgerRollover validates the only supported way to change a
// plan-scoped ledger revision. The prior ledger must be terminal and the new
// ledger must start from a clean PENDING boundary with a new plan and
// revision. Evidence, reviews, rulings, assignments, and findings are never
// carried across the boundary; a fresh worktree and baseline are required.
// The explicit supersedes references make an accidental stale snapshot
// distinguishable from an intentional new ledger.
func ValidateLedgerRollover(previous, next Ledger) error {
	if err := previous.Validate(); err != nil {
		return fmt.Errorf("previous ledger: %w", err)
	}
	if err := next.Validate(); err != nil {
		return fmt.Errorf("next ledger: %w", err)
	}
	if previous.UnitID != next.UnitID {
		return fmt.Errorf("%w: unit is immutable across rollover", ErrRevisionRolloverRequired)
	}
	if !ledgerStateIsTerminal(previous.CurrentState) || !previous.TerminalOutcome.IsTerminal() {
		return fmt.Errorf("%w: previous ledger must be terminal", ErrRevisionRolloverRequired)
	}
	if normalizedUpper(next.CurrentState) != string(TaskPending) || next.TerminalOutcome != "" {
		return fmt.Errorf("%w: new ledger must start in PENDING", ErrRevisionRolloverRequired)
	}
	if previous.PlanID == next.PlanID || previous.Revision == next.Revision || (previous.SpecRef == next.SpecRef && strings.EqualFold(previous.SpecHash, next.SpecHash) && previous.PlanID == next.PlanID) {
		return fmt.Errorf("%w: new plan and revision are required", ErrRevisionRolloverRequired)
	}
	if next.SupersedesPlanID != previous.PlanID || next.SupersedesRevision != previous.Revision {
		return fmt.Errorf("%w: supersedes_plan_id and supersedes_revision must identify the closed ledger", ErrRevisionRolloverRequired)
	}
	if !next.LastCheckpointAt.After(previous.LastCheckpointAt) {
		return fmt.Errorf("%w: new ledger checkpoint must be later than the closed ledger", ErrRevisionRolloverRequired)
	}
	if len(next.Assignments) > 0 || len(next.EvidenceRefs) > 0 || len(next.ReviewRecords) > 0 || len(next.RootCauses) > 0 || len(next.Rulings) > 0 || len(next.ReviewFindings) > 0 {
		return fmt.Errorf("%w: stale append-only content cannot cross a rollover", ErrRevisionRolloverRequired)
	}
	if len(next.Worktrees) == 0 || len(next.BaselineEvidence) == 0 {
		return fmt.Errorf("%w: fresh worktree and baseline are required", ErrRevisionRolloverRequired)
	}
	for _, worktree := range next.Worktrees {
		if !worktree.CreatedAt.IsZero() && !worktree.CreatedAt.After(previous.LastCheckpointAt) {
			return fmt.Errorf("%w: worktree evidence predates the closed ledger", ErrRevisionRolloverRequired)
		}
	}
	for _, baseline := range next.BaselineEvidence {
		if !baseline.CreatedAt.After(previous.LastCheckpointAt) {
			return fmt.Errorf("%w: baseline evidence predates the closed ledger", ErrRevisionRolloverRequired)
		}
	}
	return nil
}

// ValidateLedgerRevisionRollover is the descriptive compatibility alias for
// callers that name the boundary by revision rather than plan.
func ValidateLedgerRevisionRollover(previous, next Ledger) error {
	return ValidateLedgerRollover(previous, next)
}

func validateLedgerStateProgress(previous, next string) error {
	return ValidateLedgerTransition(previous, next)
}

func ledgerEvidence(ledger Ledger, stage string, kinds ...string) (EvidenceReceipt, bool) {
	for _, receipt := range ledger.EvidenceRefs {
		if !strings.EqualFold(receipt.Stage, stage) {
			continue
		}
		for _, kind := range kinds {
			if strings.EqualFold(receipt.EvidenceType, kind) {
				return receipt, true
			}
		}
	}
	return EvidenceReceipt{}, false
}

func ledgerHasAcceptedReview(ledger Ledger, kind ReviewType) bool {
	for _, review := range ledger.ReviewRecords {
		if review.ReviewType == kind && review.Verdict == ReviewAccepted {
			return true
		}
	}
	return false
}

func ledgerHasRootCause(ledger Ledger, taskID string) bool {
	for _, root := range ledger.RootCauses {
		if root.TaskID == taskID && ValidateRootCauseGate(root) == nil {
			return true
		}
	}
	return false
}

func missingEvidence(previous, next []EvidenceReceipt) bool {
	for _, prior := range previous {
		found := false
		for _, candidate := range next {
			if prior.Key() == candidate.Key() && receiptEquivalent(prior, candidate) {
				found = true
				break
			}
		}
		if !found {
			return true
		}
	}
	return false
}

func missingReviews(previous, next []ReviewRecord) bool {
	for _, prior := range previous {
		found := false
		for _, candidate := range next {
			if prior.ReviewID == candidate.ReviewID && reviewEquivalent(prior, candidate) {
				found = true
				break
			}
		}
		if !found {
			return true
		}
	}
	return false
}

func missingBaselines(previous, next []BaselineEvidence) bool {
	for _, prior := range previous {
		found := false
		for _, candidate := range next {
			if baselineKey(prior) == baselineKey(candidate) && jsonEquivalent(prior, candidate) {
				found = true
				break
			}
		}
		if !found {
			return true
		}
	}
	return false
}

func missingRootCauses(previous, next []RootCauseEvidence) bool {
	for _, prior := range previous {
		found := false
		for _, candidate := range next {
			if prior.EvidenceID == candidate.EvidenceID && jsonEquivalent(prior, candidate) {
				found = true
				break
			}
		}
		if !found {
			return true
		}
	}
	return false
}

func missingTasks(previous, next []Task) bool {
	for _, prior := range previous {
		found := false
		for _, candidate := range next {
			if prior.TaskID == candidate.TaskID && taskDefinitionEquivalent(prior, candidate) {
				found = true
				break
			}
		}
		if !found {
			return true
		}
	}
	return false
}

func taskDefinitionEquivalent(left, right Task) bool {
	left.State, right.State = "", ""
	left.TerminalOutcome, right.TerminalOutcome = "", ""
	left.UpdatedAt, right.UpdatedAt = time.Time{}, time.Time{}
	left.ImplementerAgentID, right.ImplementerAgentID = "", ""
	left.ReviewerAgentID, right.ReviewerAgentID = "", ""
	return jsonEquivalent(left, right)
}

func missingAssignments(previous, next []Assignment) bool {
	for _, prior := range previous {
		found := false
		for _, candidate := range next {
			if jsonEquivalent(prior, candidate) {
				found = true
				break
			}
		}
		if !found {
			return true
		}
	}
	return false
}

func missingWorktrees(previous, next []WorktreeEvidence) bool {
	for _, prior := range previous {
		found := false
		for _, candidate := range next {
			if jsonEquivalent(prior, candidate) {
				found = true
				break
			}
		}
		if !found {
			return true
		}
	}
	return false
}

func missingRulings(previous, next []Ruling) bool {
	for _, prior := range previous {
		found := false
		for _, candidate := range next {
			if prior.RulingID == candidate.RulingID && jsonEquivalent(prior, candidate) {
				found = true
				break
			}
		}
		if !found {
			return true
		}
	}
	return false
}

func missingStrings(previous, next []string) bool {
	counts := make(map[string]int, len(next))
	for _, value := range next {
		counts[value]++
	}
	for _, value := range previous {
		if counts[value] == 0 {
			return true
		}
		counts[value]--
	}
	return false
}

func jsonEquivalent(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func (ledger Ledger) clone() Ledger {
	copyLedger := ledger
	copyLedger.Tasks = append([]Task(nil), ledger.Tasks...)
	copyLedger.Assignments = append([]Assignment(nil), ledger.Assignments...)
	copyLedger.Worktrees = append([]WorktreeEvidence(nil), ledger.Worktrees...)
	copyLedger.BaselineEvidence = append([]BaselineEvidence(nil), ledger.BaselineEvidence...)
	copyLedger.Rulings = append([]Ruling(nil), ledger.Rulings...)
	copyLedger.ReviewFindings = append([]string(nil), ledger.ReviewFindings...)
	copyLedger.ReviewRecords = append([]ReviewRecord(nil), ledger.ReviewRecords...)
	copyLedger.EvidenceRefs = append([]EvidenceReceipt(nil), ledger.EvidenceRefs...)
	copyLedger.RootCauses = append([]RootCauseEvidence(nil), ledger.RootCauses...)
	for index := range copyLedger.Tasks {
		copyLedger.Tasks[index].ExactFiles = append([]string(nil), ledger.Tasks[index].ExactFiles...)
		copyLedger.Tasks[index].Dependencies = append([]string(nil), ledger.Tasks[index].Dependencies...)
	}
	return copyLedger
}

func receiptEquivalent(left, right EvidenceReceipt) bool {
	leftKey, rightKey := left.Key(), right.Key()
	if leftKey != rightKey || left.UnitID != right.UnitID || left.PlanID != right.PlanID || !strings.EqualFold(left.SpecHash, right.SpecHash) || left.TaskID != right.TaskID || left.Stage != right.Stage || left.EvidenceType != right.EvidenceType || left.Command != right.Command || left.ExitCode != right.ExitCode || left.ResultSummary != right.ResultSummary || left.ExpectedFailure != right.ExpectedFailure || left.ActualFailure != right.ActualFailure || left.ArtifactRef != right.ArtifactRef || left.ArtifactSHA256 != right.ArtifactSHA256 || left.GitRevision != right.GitRevision || left.TraceID != right.TraceID || left.EventID != right.EventID || left.ValidForRevision != right.ValidForRevision || left.CreatedAt != right.CreatedAt || left.Passed != right.Passed || left.MachineGenerated != right.MachineGenerated {
		return false
	}
	return true
}

func baselineKey(baseline BaselineEvidence) string {
	return strings.Join([]string{
		strings.TrimSpace(baseline.UnitID), strings.TrimSpace(baseline.PlanID),
		strings.TrimSpace(baseline.SpecRef), strings.TrimSpace(baseline.SpecHash),
		strings.TrimSpace(baseline.ValidForRevision), strings.TrimSpace(baseline.WorktreePath),
		strings.TrimSpace(baseline.Branch), strings.TrimSpace(baseline.BaseRevision),
		strings.TrimSpace(baseline.GitRevision), strings.TrimSpace(baseline.Command),
	}, "\x00")
}

func (ledger *Ledger) recordEvidence(receipt EvidenceReceipt) (bool, error) {
	if ledger == nil {
		return false, fmt.Errorf("nil ledger")
	}
	receipt = receipt.Redacted()
	if err := ValidateEvidenceReceipt(receipt); err != nil {
		return false, err
	}
	if strings.TrimSpace(ledger.UnitID) == "" {
		return false, fmt.Errorf("ledger unit_id is required")
	}
	if receipt.UnitID != ledger.UnitID {
		return false, fmt.Errorf("evidence %q belongs to another unit", receipt.EvidenceID)
	}
	if receipt.PlanID != ledger.PlanID || !strings.EqualFold(receipt.SpecHash, ledger.SpecHash) {
		return false, fmt.Errorf("evidence %q belongs to another plan or specification", receipt.EvidenceID)
	}
	if ledger.Revision != "" && receipt.ValidForRevision != ledger.Revision {
		return false, fmt.Errorf("evidence %q is for revision %q, ledger is %q", receipt.EvidenceID, receipt.ValidForRevision, ledger.Revision)
	}
	if ledger.Revision == "" {
		ledger.Revision = receipt.ValidForRevision
	}
	key := receipt.Key()
	for index, existing := range ledger.EvidenceRefs {
		if existing.Key() != key {
			continue
		}
		if receiptEquivalent(existing, receipt) {
			return false, nil
		}
		// Verification is monotonic for the same evidence identity: an owner
		// verifier may upgrade an earlier unverified request-side receipt.
		if !existing.Verified && receipt.Verified && sameReceiptIdentity(existing, receipt) {
			ledger.EvidenceRefs[index] = receipt
			if receipt.CreatedAt.After(ledger.LastCheckpointAt) {
				ledger.LastCheckpointAt = receipt.CreatedAt
			}
			return true, nil
		}
		return false, ErrIdempotencyConflict
	}
	ledger.EvidenceRefs = append(ledger.EvidenceRefs, receipt)
	if receipt.CreatedAt.After(ledger.LastCheckpointAt) {
		ledger.LastCheckpointAt = receipt.CreatedAt
	}
	return true, nil
}

func sameReceiptIdentity(left, right EvidenceReceipt) bool {
	left.Verified, left.VerificationResult = false, ""
	right.Verified, right.VerificationResult = false, ""
	return receiptEquivalent(left, right)
}

// RecordEvidence appends one receipt to this plan ledger. It returns false
// for an exact replay and true for a new receipt or monotonic verification
// upgrade.
func (ledger *Ledger) RecordEvidence(receipt EvidenceReceipt) (bool, error) {
	changed, err := ledger.recordEvidence(receipt)
	if err != nil {
		return false, err
	}
	return changed, nil
}

// AddEvidence is the error-only form used by persistence adapters.
func (ledger *Ledger) AddEvidence(receipt EvidenceReceipt) error {
	_, err := ledger.recordEvidence(receipt)
	return err
}

func (ledger *Ledger) AddEvidenceReceipt(receipt EvidenceReceipt) error {
	return ledger.AddEvidence(receipt)
}

func (ledger Ledger) Evidence(evidenceID string) (EvidenceReceipt, bool) {
	for _, receipt := range ledger.EvidenceRefs {
		if receipt.EvidenceID == evidenceID {
			return receipt, true
		}
	}
	return EvidenceReceipt{}, false
}

func (ledger *Ledger) RecordReview(review ReviewRecord) (bool, error) {
	if ledger == nil {
		return false, fmt.Errorf("nil ledger")
	}
	review = review.Redacted()
	if err := ValidateReviewRecord(review); err != nil {
		return false, err
	}
	if review.UnitID != ledger.UnitID {
		return false, fmt.Errorf("review %q belongs to another unit", review.ReviewID)
	}
	if review.PlanID != ledger.PlanID || review.SpecRef != ledger.SpecRef || !strings.EqualFold(review.SpecHash, ledger.SpecHash) {
		return false, fmt.Errorf("review %q belongs to another plan or specification", review.ReviewID)
	}
	if ledger.Revision != "" && review.ValidForRevision != ledger.Revision {
		return false, fmt.Errorf("review %q is for revision %q, ledger is %q", review.ReviewID, review.ValidForRevision, ledger.Revision)
	}
	if ledger.Revision == "" {
		ledger.Revision = review.ValidForRevision
	}
	for _, existing := range ledger.ReviewRecords {
		if existing.ReviewID != review.ReviewID {
			continue
		}
		if reviewEquivalent(existing, review) {
			return false, nil
		}
		return false, ErrIdempotencyConflict
	}
	ledger.ReviewRecords = append(ledger.ReviewRecords, review)
	if review.CreatedAt.After(ledger.LastCheckpointAt) {
		ledger.LastCheckpointAt = review.CreatedAt
	}
	return true, nil
}

func reviewEquivalent(left, right ReviewRecord) bool {
	leftJSON, _ := json.Marshal(left.Redacted())
	rightJSON, _ := json.Marshal(right.Redacted())
	return string(leftJSON) == string(rightJSON)
}

func (ledger *Ledger) RecordRuling(ruling Ruling) (bool, error) {
	if ledger == nil {
		return false, fmt.Errorf("nil ledger")
	}
	ruling = ruling.Redacted()
	if err := ValidateRuling(ruling); err != nil {
		return false, err
	}
	if ruling.UnitID != ledger.UnitID || ruling.PlanID != ledger.PlanID {
		return false, fmt.Errorf("ruling %q belongs to another plan", ruling.RulingID)
	}
	for _, existing := range ledger.Rulings {
		if existing.RulingID != ruling.RulingID {
			continue
		}
		leftJSON, _ := json.Marshal(existing.Redacted())
		rightJSON, _ := json.Marshal(ruling.Redacted())
		if string(leftJSON) == string(rightJSON) {
			return false, nil
		}
		return false, ErrIdempotencyConflict
	}
	ledger.Rulings = append(ledger.Rulings, ruling)
	if RulingBlocks(ruling) {
		ledger.CurrentState = string(TaskBlocked)
		ledger.TerminalOutcome = OutcomeBlocked
		ledger.BlockedReason = RedactSecrets(ruling.Rationale)
	}
	if ruling.CreatedAt.After(ledger.LastCheckpointAt) {
		ledger.LastCheckpointAt = ruling.CreatedAt
	}
	return true, nil
}

func (ledger *Ledger) ApplyRuling(ruling Ruling) error {
	_, err := ledger.RecordRuling(ruling)
	return err
}

func (ledger *Ledger) MarkTerminal(outcome TerminalOutcome, reason string) error {
	if ledger == nil {
		return fmt.Errorf("nil ledger")
	}
	if err := ValidateTerminalOutcome(outcome); err != nil {
		return err
	}
	if ledger.TerminalOutcome != "" {
		if ledger.TerminalOutcome == outcome {
			return nil
		}
		return ErrIdempotencyConflict
	}
	if (outcome == OutcomeBlocked || outcome == OutcomeFailed) && strings.TrimSpace(reason) == "" {
		return fmt.Errorf("%w: terminal reason", ErrTerminalOutcomeRequired)
	}
	ledger.TerminalOutcome = outcome
	switch outcome {
	case OutcomeOK:
		ledger.CurrentState = string(TaskDone)
	case OutcomeBlocked:
		ledger.CurrentState, ledger.BlockedReason = string(TaskBlocked), RedactSecrets(reason)
	case OutcomeFailed:
		ledger.CurrentState = string(TaskFailed)
	case OutcomeCancelled:
		ledger.CurrentState = string(TaskCancelled)
	}
	return nil
}

func (ledger *Ledger) Checkpoint(now time.Time, resumeToken string) error {
	if ledger == nil {
		return fmt.Errorf("nil ledger")
	}
	if now.IsZero() {
		now = RealClock{}.Now()
	}
	ledger.LastCheckpointAt = now
	ledger.ResumeToken = RedactSecrets(resumeToken)
	return nil
}

// Resume returns a validated copy of the plan-scoped state. JSON persistence
// and process restarts therefore preserve the same evidence and terminal
// result without a second lifecycle engine.
func (ledger Ledger) Resume() (Ledger, error) {
	copyLedger := ledger.clone()
	if err := copyLedger.Validate(); err != nil {
		return Ledger{}, err
	}
	return copyLedger, nil
}

func (ledger Ledger) LiveGateInput() LiveGateInput {
	input := LiveGateInput{UnitID: ledger.UnitID, PlanID: ledger.PlanID, SpecHash: ledger.SpecHash, Revision: ledger.Revision, CheckOK: ledger.CheckOK, Evidence: append([]EvidenceReceipt(nil), ledger.EvidenceRefs...), Reviews: append([]ReviewRecord(nil), ledger.ReviewRecords...)}
	for _, receipt := range ledger.EvidenceRefs {
		switch normalized(receipt.EvidenceType) {
		case "accepted", "implementation_accepted":
			input.AcceptedImplementation = true
		case "tdd_green", "unit_test", "contract_test":
			// FullRelevantTests can be explicitly supplied by the caller; the
			// evidence overlay is interpreted by ValidateLIVEGate when false.
		case "regression", "full_tests", "test_suite":
			// See above.
		}
	}
	return input
}

func (ledger Ledger) ValidateLiveGate() error { return ValidateLIVEGate(ledger.LiveGateInput()) }

func (ledger Ledger) CanMarkLiveVerified() bool { return ledger.ValidateLiveGate() == nil }
