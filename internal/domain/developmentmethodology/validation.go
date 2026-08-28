package developmentmethodology

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

func ValidateWorkClassification(classification WorkClassification) error {
	if err := ValidateWorkClass(string(classification.Class)); err != nil {
		return err
	}
	return nil
}

func ValidateSpecification(spec Specification) error {
	if strings.TrimSpace(spec.SpecID) == "" {
		return errors.New("spec_id is required")
	}
	if strings.TrimSpace(spec.Title) == "" {
		return errors.New("title is required")
	}
	if spec.Revision < 1 {
		return errors.New("revision must be positive")
	}
	if strings.TrimSpace(spec.Status) == "" {
		return errors.New("status is required")
	}
	if normalized(spec.Status) == "waiting" {
		return fmt.Errorf("%w: specification cannot wait for a human", ErrInvalidState)
	}
	if strings.TrimSpace(spec.Source) == "" {
		return errors.New("source is required")
	}
	if !validSHA256(spec.ContentHash) {
		return errors.New("content_hash must be a SHA-256 hex digest")
	}
	if spec.Content != "" && !strings.EqualFold(hashText(spec.Content), strings.TrimSpace(spec.ContentHash)) {
		return errors.New("content_hash does not match content")
	}
	if strings.TrimSpace(spec.Purpose) == "" {
		return errors.New("purpose is required")
	}
	if strings.TrimSpace(spec.Problem) == "" {
		return errors.New("problem is required")
	}
	if len(spec.AcceptanceCriteria) == 0 {
		return errors.New("acceptance_criteria is required")
	}
	if err := validateStringSlice("acceptance_criteria", spec.AcceptanceCriteria); err != nil {
		return err
	}
	for name, values := range map[string][]string{
		"scope": spec.Scope, "non_goals": spec.NonGoals,
		"constraints": spec.Constraints, "interfaces": spec.Interfaces,
	} {
		if err := validateStringSlice(name, values); err != nil {
			return err
		}
	}
	if spec.CreatedAt.IsZero() || spec.UpdatedAt.IsZero() {
		return errors.New("created_at and updated_at are required")
	}
	if spec.UpdatedAt.Before(spec.CreatedAt) {
		return errors.New("updated_at must not precede created_at")
	}
	return nil
}

func validateStringSlice(name string, values []string) error {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s must not contain an empty value", name)
		}
	}
	return nil
}

// ValidateLedgerState validates the one Task/Ledger lifecycle vocabulary.
func ValidateLedgerState(value string) error {
	state := normalizedUpper(value)
	if state == "" {
		return fmt.Errorf("%w: current_state is required", ErrInvalidState)
	}
	if !validTaskState(TaskState(state)) {
		return fmt.Errorf("%w: unknown current_state %q", ErrInvalidState, value)
	}
	return nil
}

func ledgerStateIsTerminal(value string) bool {
	return TaskState(normalizedUpper(value)).IsTerminal()
}

func ledgerOutcomeForState(value string) TerminalOutcome {
	return outcomeForTaskState(TaskState(normalizedUpper(value)))
}

func ValidateTask(task Task) error {
	if strings.TrimSpace(task.TaskID) == "" {
		return errors.New("task_id is required")
	}
	if strings.TrimSpace(task.PlanID) == "" {
		return errors.New("plan_id is required")
	}
	if strings.TrimSpace(task.Purpose) == "" {
		return errors.New("task purpose is required")
	}
	state := normalizeTaskState(task.State)
	if !validTaskState(state) {
		return fmt.Errorf("%w: %q", ErrInvalidState, task.State)
	}
	if state.IsTerminal() {
		if task.TerminalOutcome == "" {
			return fmt.Errorf("%w: terminal task requires terminal_outcome", ErrTerminalOutcomeRequired)
		}
		if err := ValidateTerminalOutcome(task.TerminalOutcome); err != nil {
			return err
		}
		if outcomeForTaskState(state) != task.TerminalOutcome {
			return errors.New("terminal_outcome does not match task state")
		}
	} else if task.TerminalOutcome != "" {
		return errors.New("non-terminal task cannot have terminal_outcome")
	}
	if strings.TrimSpace(task.ValidForRevision) != "" && strings.ContainsAny(task.ValidForRevision, " \t\r\n") {
		return errors.New("valid_for_revision must be a bounded revision token")
	}
	for name, values := range map[string][]string{
		"exact_files": task.ExactFiles, "interfaces_consumed": task.InterfacesConsumed,
		"interfaces_produced": task.InterfacesProduced, "dependencies": task.Dependencies,
		"exact_commands": task.ExactCommands, "expected_results": task.ExpectedResults,
		"rollback": task.Rollback,
	} {
		if err := validateStringSlice(name, values); err != nil {
			return err
		}
	}
	return nil
}

func ValidatePlan(plan Plan) error {
	if strings.TrimSpace(plan.PlanID) == "" {
		return errors.New("plan_id is required")
	}
	if strings.TrimSpace(plan.ImplementationUnitID) == "" {
		return errors.New("implementation_unit_id reference is required")
	}
	if strings.TrimSpace(plan.SpecRef) == "" {
		return errors.New("spec_ref is required")
	}
	if !validSHA256(plan.SpecHash) {
		return errors.New("spec_hash must be a SHA-256 hex digest")
	}
	if plan.CreatedAt.IsZero() || plan.UpdatedAt.IsZero() {
		return errors.New("created_at and updated_at are required")
	}
	if plan.UpdatedAt.Before(plan.CreatedAt) {
		return errors.New("updated_at must not precede created_at")
	}
	ids := make(map[string]struct{}, len(plan.Tasks))
	for index := range plan.Tasks {
		task := plan.Tasks[index]
		if task.PlanID == "" {
			task.PlanID = plan.PlanID
		}
		if task.PlanID != plan.PlanID {
			return fmt.Errorf("task %q belongs to another plan", task.TaskID)
		}
		if err := ValidateTask(task); err != nil {
			return err
		}
		if _, exists := ids[task.TaskID]; exists {
			return fmt.Errorf("duplicate task_id %q", task.TaskID)
		}
		ids[task.TaskID] = struct{}{}
	}
	for taskID, dependencies := range plan.TaskDAG {
		if _, exists := ids[taskID]; !exists {
			return fmt.Errorf("task_dag references unknown task %q", taskID)
		}
		if err := validateStringSlice("task_dag dependencies", dependencies); err != nil {
			return err
		}
		for _, dependency := range dependencies {
			if _, exists := ids[dependency]; !exists {
				return fmt.Errorf("task %q depends on unknown task %q", taskID, dependency)
			}
		}
	}
	if err := validateTaskDAG(plan); err != nil {
		return err
	}
	return nil
}

func validateTaskDAG(plan Plan) error {
	dependencies := make(map[string][]string, len(plan.Tasks)+len(plan.TaskDAG))
	for _, task := range plan.Tasks {
		dependencies[task.TaskID] = append([]string(nil), task.Dependencies...)
	}
	for taskID, values := range plan.TaskDAG {
		dependencies[taskID] = append(dependencies[taskID], values...)
	}
	visiting := make(map[string]bool, len(dependencies))
	visited := make(map[string]bool, len(dependencies))
	var visit func(string) error
	visit = func(taskID string) error {
		if visiting[taskID] {
			return fmt.Errorf("task_dag contains a cycle at %q", taskID)
		}
		if visited[taskID] {
			return nil
		}
		visiting[taskID] = true
		for _, dependency := range dependencies[taskID] {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		visiting[taskID] = false
		visited[taskID] = true
		return nil
	}
	for taskID := range dependencies {
		if err := visit(taskID); err != nil {
			return err
		}
	}
	return nil
}

func ValidatePlanAgainstSpecification(plan Plan, spec Specification) error {
	if err := ValidateSpecification(spec); err != nil {
		return err
	}
	if err := ValidatePlan(plan); err != nil {
		return err
	}
	if plan.SpecRef != spec.SpecID {
		return fmt.Errorf("%w: spec_ref=%q spec_id=%q", ErrSpecHashMismatch, plan.SpecRef, spec.SpecID)
	}
	if !strings.EqualFold(strings.TrimSpace(plan.SpecHash), strings.TrimSpace(spec.ContentHash)) {
		return fmt.Errorf("%w: spec_ref=%q", ErrSpecHashMismatch, plan.SpecRef)
	}
	return nil
}

func ValidateAdoptionEvidence(evidence AdoptionEvidence) error {
	if strings.TrimSpace(evidence.EvidenceID) == "" || strings.TrimSpace(evidence.UnitID) == "" || strings.TrimSpace(evidence.SpecRef) == "" {
		return errors.New("adoption evidence identity is required")
	}
	if !validSHA256(evidence.SpecHash) {
		return errors.New("adoption spec_hash must be a SHA-256 hex digest")
	}
	switch normalizedUpper(evidence.Decision) {
	case "ADOPT", "ADOPTED", "PROMOTE", "PROMOTED":
	default:
		return errors.New("adoption evidence decision must be ADOPT or PROMOTE")
	}
	if !evidence.Verified {
		return fmt.Errorf("%w: adoption evidence", ErrImplementationAuthorityRequired)
	}
	if evidence.CreatedAt.IsZero() {
		return errors.New("adoption evidence created_at is required")
	}
	return nil
}

func (request ImplementationAuthorityRequest) Validate() error {
	if strings.TrimSpace(request.ImplementationAuthorityTokenID) == "" || strings.TrimSpace(request.UnitID) == "" || strings.TrimSpace(request.SpecRef) == "" {
		return errors.New("implementation_authority request identity is required")
	}
	if !validSHA256(request.SpecHash) {
		return errors.New("implementation_authority request spec_hash must be a SHA-256 hex digest")
	}
	if strings.TrimSpace(request.Issuer) == "" || strings.TrimSpace(request.Reason) == "" || len(request.Scope) == 0 {
		return errors.New("implementation_authority request issuer, reason, and scope are required")
	}
	if err := validateStringSlice("scope", request.Scope); err != nil {
		return err
	}
	return nil
}

func ValidateImplementationAuthorityTokenAt(token ImplementationAuthorityToken, now time.Time) error {
	if strings.TrimSpace(token.ImplementationAuthorityTokenID) == "" || strings.TrimSpace(token.UnitID) == "" || strings.TrimSpace(token.SpecRef) == "" {
		return fmt.Errorf("%w: token identity", ErrInvalidImplementationAuthorityToken)
	}
	if !validSHA256(token.SpecHash) {
		return fmt.Errorf("%w: token spec_hash", ErrInvalidImplementationAuthorityToken)
	}
	if strings.TrimSpace(token.Issuer) == "" || strings.TrimSpace(token.Reason) == "" || len(token.Scope) == 0 {
		return fmt.Errorf("%w: token issuer, reason, and scope", ErrInvalidImplementationAuthorityToken)
	}
	if token.IssuedAt.IsZero() || token.ExpiresAt.IsZero() || !token.ExpiresAt.After(token.IssuedAt) {
		return fmt.Errorf("%w: token validity interval", ErrInvalidImplementationAuthorityToken)
	}
	if now.IsZero() {
		now = RealClock{}.Now()
	}
	if !token.RevokedAt.IsZero() || now.Before(token.IssuedAt) || !now.Before(token.ExpiresAt) {
		return ErrStaleImplementationAuthorityToken
	}
	return nil
}

func clockTime(value any) time.Time {
	switch clock := value.(type) {
	case nil:
		return RealClock{}.Now()
	case Clock:
		return clock.Now()
	case time.Time:
		return clock
	default:
		return RealClock{}.Now()
	}
}

func (token ImplementationAuthorityToken) Validate(clock any) error {
	return ValidateImplementationAuthorityTokenAt(token, clockTime(clock))
}

func (token ImplementationAuthorityToken) ValidateFor(adoption AdoptionEvidence, clock any) error {
	if err := ValidateAdoptionEvidence(adoption); err != nil {
		return err
	}
	if err := ValidateImplementationAuthorityTokenAt(token, clockTime(clock)); err != nil {
		return err
	}
	if token.UnitID != adoption.UnitID || token.SpecRef != adoption.SpecRef || !strings.EqualFold(token.SpecHash, adoption.SpecHash) {
		return fmt.Errorf("%w: token does not match adoption evidence", ErrInvalidImplementationAuthorityToken)
	}
	return nil
}

func ValidateImplementationAuthorityToken(token ImplementationAuthorityToken, adoption AdoptionEvidence, clock any) error {
	return token.ValidateFor(adoption, clock)
}

// IssueImplementationAuthorityToken requires a previously-issued, CORE-verified adoption
// result. It never waits for a user response. A zero expiry receives the
// bounded default TTL so callers cannot accidentally create an immortal
// authority grant.
func IssueImplementationAuthorityToken(request ImplementationAuthorityRequest, adoption AdoptionEvidence, clock any) (ImplementationAuthorityToken, error) {
	if err := request.Validate(); err != nil {
		return ImplementationAuthorityToken{}, err
	}
	if err := ValidateAdoptionEvidence(adoption); err != nil {
		return ImplementationAuthorityToken{}, err
	}
	if request.UnitID != adoption.UnitID || request.SpecRef != adoption.SpecRef || !strings.EqualFold(request.SpecHash, adoption.SpecHash) {
		return ImplementationAuthorityToken{}, fmt.Errorf("%w: adoption evidence does not match request", ErrImplementationAuthorityRequired)
	}
	now := clockTime(clock)
	issuedAt := request.IssuedAt
	if issuedAt.IsZero() {
		issuedAt = now
	}
	expiresAt := request.ExpiresAt
	if expiresAt.IsZero() {
		expiresAt = issuedAt.Add(DefaultImplementationAuthorityTTL)
	}
	token := ImplementationAuthorityToken{
		ImplementationAuthorityTokenID: request.ImplementationAuthorityTokenID, UnitID: request.UnitID,
		SpecRef: request.SpecRef, SpecHash: request.SpecHash, Issuer: request.Issuer,
		IssuedAt: issuedAt, Scope: append([]string(nil), request.Scope...), Reason: request.Reason,
		ExpiresAt: expiresAt,
	}
	if err := token.ValidateFor(adoption, now); err != nil {
		return ImplementationAuthorityToken{}, err
	}
	return token, nil
}

func AuthorityAllows(role ExecutionRole, operation AuthorityOperation) bool {
	role = ExecutionRole(normalized(string(role)))
	operation = AuthorityOperation(normalized(string(operation)))
	if role == RoleCoder {
		switch operation {
		case OperationPlan, OperationPatch, OperationProposal, OperationTestProposal, OperationReviewFinding:
			return true
		default:
			return false
		}
	}
	if role == RoleWorker {
		switch operation {
		case OperationRepositoryWrite, OperationShell, OperationTestExecution,
			OperationBuild, OperationDeploy, OperationRestart, OperationStateTransition,
			OperationEvidence, OperationPatch:
			return true
		default:
			return false
		}
	}
	if role == RoleReviewer {
		return operation == OperationReview || operation == OperationReviewFinding
	}
	if role == RoleController {
		return operation == OperationRuling || operation == OperationStateTransition || operation == OperationEvidence
	}
	if role == RoleSystem {
		return operation == OperationImplementationAuthority || operation == OperationRuling
	}
	return false
}

// OperationImplementationAuthority is declared here because implementation_authority is a policy operation,
// not a model/provider or Agent binding.
const OperationImplementationAuthority AuthorityOperation = "implementation_authority"

func AuthorityAllowsWithSkill(role ExecutionRole, skill string, operation AuthorityOperation) bool {
	_ = skill
	return AuthorityAllows(role, operation)
}

func ValidateAuthority(role ExecutionRole, operation AuthorityOperation) error {
	if !AuthorityAllows(role, operation) {
		return fmt.Errorf("%w: role=%s operation=%s", ErrAuthorityDenied, role, operation)
	}
	return nil
}

func CheckAuthority(role ExecutionRole, operation AuthorityOperation) GateResult {
	if err := ValidateAuthority(role, operation); err != nil {
		return GateResult{Kind: GateAuthority, Status: GateRejected, Reason: err.Error()}
	}
	return GateResult{Kind: GateAuthority, Status: GatePassed}
}

func ValidateWorktreeGate(worktree WorktreeEvidence) error {
	if strings.TrimSpace(worktree.WorktreePath) == "" || strings.TrimSpace(worktree.Branch) == "" || strings.TrimSpace(worktree.BaseRevision) == "" {
		return fmt.Errorf("%w: worktree identity", ErrWorktreeRequired)
	}
	branch := normalized(worktree.Branch)
	mainBranch := branch == "main" || branch == "master" || branch == "production" || branch == "trunk"
	if mainBranch {
		return fmt.Errorf("%w: direct branch %q", ErrWorktreeRequired, worktree.Branch)
	}
	if !worktree.Verified {
		return fmt.Errorf("%w: worktree is not verified", ErrWorktreeRequired)
	}
	if !worktree.Isolated {
		if !(worktree.ReadOnly && strings.TrimSpace(worktree.ExceptionReason) != "") {
			return fmt.Errorf("%w: production changes require isolation", ErrWorktreeRequired)
		}
	}
	return nil
}

func CheckWorktree(worktree WorktreeEvidence) GateResult {
	if err := ValidateWorktreeGate(worktree); err != nil {
		return GateResult{Kind: GateWorktree, Status: GateRejected, Reason: err.Error()}
	}
	return GateResult{Kind: GateWorktree, Status: GatePassed}
}

func ValidateBaselineGate(baseline BaselineEvidence) error {
	if strings.TrimSpace(baseline.UnitID) == "" || strings.TrimSpace(baseline.PlanID) == "" || strings.TrimSpace(baseline.SpecRef) == "" {
		return fmt.Errorf("%w: baseline plan identity", ErrBaselineRequired)
	}
	if !validSHA256(baseline.SpecHash) {
		return fmt.Errorf("%w: baseline spec_hash", ErrBaselineRequired)
	}
	if strings.TrimSpace(baseline.ValidForRevision) == "" {
		return fmt.Errorf("%w: baseline valid_for_revision", ErrBaselineRequired)
	}
	if strings.TrimSpace(baseline.WorktreePath) == "" || strings.TrimSpace(baseline.Branch) == "" || strings.TrimSpace(baseline.BaseRevision) == "" {
		return fmt.Errorf("%w: baseline identity", ErrBaselineRequired)
	}
	if strings.TrimSpace(baseline.Command) == "" || strings.TrimSpace(baseline.GitRevision) == "" {
		return fmt.Errorf("%w: baseline command and revision", ErrBaselineRequired)
	}
	if baseline.ExitCode != 0 || baseline.Dirty || !baseline.Verified {
		return fmt.Errorf("%w: baseline must be verified clean with exit code zero", ErrBaselineRequired)
	}
	if baseline.CreatedAt.IsZero() {
		return fmt.Errorf("%w: created_at", ErrBaselineRequired)
	}
	return nil
}

func CheckBaseline(baseline BaselineEvidence) GateResult {
	if err := ValidateBaselineGate(baseline); err != nil {
		return GateResult{Kind: GateBaseline, Status: GateBlocked, Reason: err.Error()}
	}
	return GateResult{Kind: GateBaseline, Status: GatePassed}
}

func ValidateEvidenceReceipt(receipt EvidenceReceipt) error {
	if strings.TrimSpace(receipt.EvidenceID) == "" || strings.TrimSpace(receipt.UnitID) == "" || strings.TrimSpace(receipt.PlanID) == "" {
		return errors.New("evidence_id, unit_id, and plan_id are required")
	}
	if !validSHA256(receipt.SpecHash) {
		return errors.New("evidence spec_hash must be a SHA-256 hex digest")
	}
	if strings.TrimSpace(receipt.Stage) == "" || strings.TrimSpace(receipt.EvidenceType) == "" {
		return errors.New("evidence stage and evidence_type are required")
	}
	if strings.TrimSpace(receipt.ValidForRevision) == "" {
		return errors.New("valid_for_revision is required")
	}
	if receipt.CreatedAt.IsZero() {
		return errors.New("evidence created_at is required")
	}
	if receipt.ExitCode < 0 {
		return errors.New("exit_code must not be negative")
	}
	if receipt.ArtifactSHA256 != "" && !validSHA256(receipt.ArtifactSHA256) {
		return errors.New("artifact_sha256 must be a SHA-256 hex digest")
	}
	if !receipt.IsMachineVerifiable() {
		return ErrNaturalLanguageEvidence
	}
	return nil
}

func evidenceReceiptMatches(receipt EvidenceReceipt, input LiveGateInput, stages, kinds []string) bool {
	if !receipt.Verified || !receipt.IsMachineVerifiable() || normalized(receipt.VerificationResult) == "rejected" {
		return false
	}
	if strings.TrimSpace(input.UnitID) == "" || strings.TrimSpace(input.PlanID) == "" || !validSHA256(input.SpecHash) || strings.TrimSpace(input.Revision) == "" {
		return false
	}
	if receipt.UnitID != input.UnitID || receipt.PlanID != input.PlanID || !strings.EqualFold(receipt.SpecHash, input.SpecHash) || strings.TrimSpace(receipt.ValidForRevision) != strings.TrimSpace(input.Revision) || receipt.ExitCode != 0 {
		return false
	}
	stage := normalizedUpper(receipt.Stage)
	kind := normalized(receipt.EvidenceType)
	stageMatches := len(stages) == 0
	for _, expected := range stages {
		if stage == normalizedUpper(expected) {
			stageMatches = true
			break
		}
	}
	kindMatches := len(kinds) == 0
	for _, expected := range kinds {
		if kind == normalized(expected) {
			kindMatches = true
			break
		}
	}
	return stageMatches && kindMatches
}

func ValidateTDDRedEvidence(red EvidenceReceipt) error {
	if err := ValidateEvidenceReceipt(red); err != nil {
		return err
	}
	if normalizedUpper(red.Stage) != normalizedUpper(DeliveryTDDRed) || (normalized(red.EvidenceType) != "tdd_red" && normalized(red.EvidenceType) != "execution_report") {
		return fmt.Errorf("%w: invalid RED stage or type", ErrMissingEvidence)
	}
	if !red.Verified {
		return fmt.Errorf("%w: RED is not verified", ErrUnverifiedEvidence)
	}
	if red.ExitCode == 0 {
		return errors.New("RED evidence must record the expected failing command")
	}
	if strings.TrimSpace(red.ExpectedFailure) == "" || strings.TrimSpace(red.ActualFailure) == "" {
		return errors.New("RED expected_failure and actual_failure are required")
	}
	if !strings.Contains(strings.ToLower(red.ActualFailure), strings.ToLower(strings.TrimSpace(red.ExpectedFailure))) {
		return errors.New("RED actual_failure does not match expected_failure")
	}
	return nil
}

func ValidateTDDGreenEvidence(green EvidenceReceipt, red EvidenceReceipt) error {
	if strings.TrimSpace(red.EvidenceID) == "" {
		return fmt.Errorf("%w: RED evidence is required before GREEN", ErrMissingEvidence)
	}
	if err := ValidateTDDRedEvidence(red); err != nil {
		return err
	}
	if err := ValidateEvidenceReceipt(green); err != nil {
		return err
	}
	if normalizedUpper(green.Stage) != normalizedUpper(DeliveryTDDGreen) || normalized(green.EvidenceType) != "tdd_green" {
		return fmt.Errorf("%w: invalid GREEN stage or type", ErrMissingEvidence)
	}
	if !green.Verified || green.ExitCode != 0 {
		return fmt.Errorf("%w: GREEN command did not pass", ErrUnverifiedEvidence)
	}
	if green.UnitID != red.UnitID || green.PlanID != red.PlanID || !strings.EqualFold(green.SpecHash, red.SpecHash) || green.ValidForRevision != red.ValidForRevision {
		return fmt.Errorf("%w: RED and GREEN bindings differ", ErrMissingEvidence)
	}
	return nil
}

func ValidateRefactorEvidence(refactor EvidenceReceipt, green EvidenceReceipt) error {
	if strings.TrimSpace(green.EvidenceID) == "" {
		return fmt.Errorf("%w: GREEN evidence is required before REFACTOR", ErrMissingEvidence)
	}
	if err := ValidateEvidenceReceipt(refactor); err != nil {
		return err
	}
	if normalizedUpper(refactor.Stage) != normalizedUpper(DeliveryRefactor) || normalized(refactor.EvidenceType) != "refactor" {
		return fmt.Errorf("%w: invalid REFACTOR stage or type", ErrMissingEvidence)
	}
	if !refactor.Verified || refactor.ExitCode != 0 || refactor.UnitID != green.UnitID || refactor.PlanID != green.PlanID || !strings.EqualFold(refactor.SpecHash, green.SpecHash) || refactor.ValidForRevision != green.ValidForRevision {
		return fmt.Errorf("%w: refactor evidence is not valid", ErrUnverifiedEvidence)
	}
	return nil
}

func ValidateTDDGate(red, green EvidenceReceipt, refactors ...EvidenceReceipt) error {
	if err := ValidateTDDRedEvidence(red); err != nil {
		return fmt.Errorf("%w: %v", ErrMissingEvidence, err)
	}
	if err := ValidateTDDGreenEvidence(green, red); err != nil {
		return err
	}
	if len(refactors) > 0 {
		if err := ValidateRefactorEvidence(refactors[0], green); err != nil {
			return err
		}
	}
	return nil
}

func ValidateReviewRecord(review ReviewRecord) error {
	if strings.TrimSpace(review.ReviewID) == "" || strings.TrimSpace(review.UnitID) == "" || strings.TrimSpace(review.PlanID) == "" || strings.TrimSpace(review.SpecRef) == "" || strings.TrimSpace(review.DiffRef) == "" {
		return fmt.Errorf("%w: review identity and references", ErrReviewRequired)
	}
	if !validSHA256(review.SpecHash) {
		return fmt.Errorf("%w: review spec_hash", ErrReviewRequired)
	}
	if strings.TrimSpace(review.ValidForRevision) == "" {
		return fmt.Errorf("%w: review valid_for_revision", ErrReviewRequired)
	}
	if review.ReviewType != ReviewTypeTask && review.ReviewType != ReviewTypeBranch {
		return fmt.Errorf("%w: review_type", ErrReviewRequired)
	}
	if review.ReviewType == ReviewTypeTask && strings.TrimSpace(review.TaskID) == "" {
		return fmt.Errorf("%w: task review task_id", ErrReviewRequired)
	}
	if strings.TrimSpace(review.ImplementerAgentID) == "" || strings.TrimSpace(review.ReviewerAgentID) == "" || review.ImplementerAgentID == review.ReviewerAgentID {
		return fmt.Errorf("%w: reviewer must be independent of implementer", ErrReviewRequired)
	}
	if review.Verdict != ReviewAccepted && review.Verdict != ReviewRejected && review.Verdict != ReviewBlocked {
		return fmt.Errorf("%w: verdict", ErrReviewRequired)
	}
	if len(review.EvidenceRefs) == 0 {
		return fmt.Errorf("%w: review evidence", ErrReviewRequired)
	}
	if err := validateStringSlice("evidence_refs", review.EvidenceRefs); err != nil {
		return err
	}
	if review.CreatedAt.IsZero() {
		return fmt.Errorf("%w: created_at", ErrReviewRequired)
	}
	return nil
}

func ValidateIndependentReview(implementerAgentID, reviewerAgentID string) error {
	if strings.TrimSpace(implementerAgentID) == "" || strings.TrimSpace(reviewerAgentID) == "" || strings.TrimSpace(implementerAgentID) == strings.TrimSpace(reviewerAgentID) {
		return ErrReviewRequired
	}
	return nil
}

func ValidateRootCauseGate(root RootCauseEvidence) error {
	if strings.TrimSpace(root.EvidenceID) == "" || strings.TrimSpace(root.UnitID) == "" || strings.TrimSpace(root.PlanID) == "" || strings.TrimSpace(root.TaskID) == "" || strings.TrimSpace(root.SpecRef) == "" {
		return fmt.Errorf("%w: root-cause identity", ErrRootCauseRequired)
	}
	if !validSHA256(root.SpecHash) {
		return fmt.Errorf("%w: root-cause spec_hash", ErrRootCauseRequired)
	}
	if strings.TrimSpace(root.ValidForRevision) == "" {
		return fmt.Errorf("%w: root-cause valid_for_revision", ErrRootCauseRequired)
	}
	if !root.Reproduced || strings.TrimSpace(root.ReproductionRef) == "" {
		return fmt.Errorf("%w: reproduction evidence", ErrRootCauseRequired)
	}
	if strings.TrimSpace(root.ErrorLogRef) == "" || strings.TrimSpace(root.TraceRef) == "" || strings.TrimSpace(root.VerificationRef) == "" {
		return fmt.Errorf("%w: error, trace, and verification evidence", ErrRootCauseRequired)
	}
	if len(root.CallPath) == 0 || strings.TrimSpace(root.Hypothesis) == "" {
		return fmt.Errorf("%w: call path and single hypothesis", ErrRootCauseRequired)
	}
	if !root.Verified {
		return fmt.Errorf("%w: root-cause evidence is not verified", ErrUnverifiedEvidence)
	}
	if ArchitectureReviewRequired(root.FailureCount) && !root.ArchitectureReviewRequired && !root.Escalated {
		return fmt.Errorf("%w: architecture assumption review is required after %d failed attempts", ErrRootCauseRequired, root.FailureCount)
	}
	if root.FailureCount < 0 {
		return fmt.Errorf("%w: failure_count must not be negative", ErrRootCauseRequired)
	}
	if root.CreatedAt.IsZero() {
		return fmt.Errorf("%w: created_at", ErrRootCauseRequired)
	}
	return nil
}

func ArchitectureReviewRequired(failureCount int) bool { return failureCount >= RootCauseEscalation }

func ValidateRuling(ruling Ruling) error {
	if strings.TrimSpace(ruling.RulingID) == "" || strings.TrimSpace(ruling.UnitID) == "" || strings.TrimSpace(ruling.PlanID) == "" || strings.TrimSpace(ruling.SpecRef) == "" {
		return errors.New("ruling identity and spec_ref are required")
	}
	switch ruling.ConflictType {
	case ConflictReversibleLocalAmbiguity, ConflictNonDestructiveDesignGap, ConflictDestructiveIrreversible, ConflictProductSemantics:
	default:
		return errors.New("invalid conflict_type")
	}
	if strings.TrimSpace(ruling.Rationale) == "" || strings.TrimSpace(ruling.Impact) == "" || strings.TrimSpace(ruling.Actor) == "" || ruling.CreatedAt.IsZero() {
		return errors.New("ruling rationale, impact, actor, and created_at are required")
	}
	if ruling.Decision != RulingContinue && ruling.Decision != RulingBlocked && ruling.Decision != RulingRejected {
		return errors.New("invalid ruling decision")
	}
	if (ruling.ConflictType == ConflictDestructiveIrreversible || ruling.ConflictType == ConflictProductSemantics) && ruling.Decision != RulingBlocked {
		return ErrConflictBlocked
	}
	return nil
}

func RulingBlocks(ruling Ruling) bool {
	return ruling.Decision == RulingBlocked || ruling.Decision == RulingRejected || ruling.ConflictType == ConflictDestructiveIrreversible || ruling.ConflictType == ConflictProductSemantics
}

func liveReceiptFromInput(input LiveGateInput, candidates []EvidenceReceipt, stages, kinds []string) EvidenceReceipt {
	for _, candidate := range append([]EvidenceReceipt{
		firstNonZeroReceipt(input.BuildEvidence, input.BuildReceipt),
		firstNonZeroReceipt(input.EcosystemEvidence),
		firstNonZeroReceipt(input.DeployEvidence, input.DeploymentReceipt),
		firstNonZeroReceipt(input.RestartEvidence, input.RestartReceipt),
		firstNonZeroReceipt(input.ProcessIdentityEvidence),
		firstNonZeroReceipt(input.ReadinessEvidence, input.ReadinessReceipt),
		firstNonZeroReceipt(input.ProductionSmokeEvidence, input.ProductionSmokeReceipt),
		firstNonZeroReceipt(input.ViewerVerificationEvidence, input.ViewerVerificationReceipt),
	}, candidates...) {
		if evidenceReceiptMatches(candidate, input, stages, kinds) {
			return candidate
		}
	}
	return EvidenceReceipt{}
}

func hasLiveReceipt(input LiveGateInput, stages, kinds []string) bool {
	return strings.TrimSpace(liveReceiptFromInput(input, input.Evidence, stages, kinds).EvidenceID) != ""
}

func requireLiveReceipt(input LiveGateInput, name string, stages, kinds []string, artifactHash bool) error {
	receipt := liveReceiptFromInput(input, input.Evidence, stages, kinds)
	if strings.TrimSpace(receipt.EvidenceID) == "" {
		return fmt.Errorf("%w: %s", ErrLiveGate, name)
	}
	if artifactHash && !validSHA256(receipt.ArtifactSHA256) {
		return fmt.Errorf("%w: %s artifact hash", ErrLiveGate, name)
	}
	return nil
}

func ValidateLIVEGate(input LiveGateInput) error {
	if strings.TrimSpace(input.UnitID) == "" || strings.TrimSpace(input.PlanID) == "" || !validSHA256(input.SpecHash) || strings.TrimSpace(input.Revision) == "" {
		return fmt.Errorf("%w: unit, plan, spec hash, and revision are required", ErrLiveGate)
	}
	allEvidence := liveGateReceipts(input)
	evidenceIDs := make(map[string]struct{}, len(allEvidence))
	for _, receipt := range allEvidence {
		if err := ValidateEvidenceReceipt(receipt); err != nil {
			return fmt.Errorf("%w: invalid evidence %q: %v", ErrLiveGate, receipt.EvidenceID, err)
		}
		if receipt.UnitID != input.UnitID || receipt.PlanID != input.PlanID || !strings.EqualFold(receipt.SpecHash, input.SpecHash) || receipt.ValidForRevision != input.Revision {
			return fmt.Errorf("%w: evidence %q has stale or foreign bindings", ErrLiveGate, receipt.EvidenceID)
		}
		evidenceIDs[receipt.EvidenceID] = struct{}{}
	}
	// CheckOK is intentionally not consulted. It is a legacy projection and
	// cannot prove the acceptance, deployment, or user-visible route.
	// The compatibility booleans are deliberately ignored as well. A caller
	// must provide a verified, revision-bound acceptance receipt.
	if !hasLiveReceipt(input, []string{"ACCEPTED"}, []string{"accepted", "implementation_accepted"}) {
		return fmt.Errorf("%w: accepted implementation", ErrLiveGate)
	}
	if !hasLiveReceipt(input, []string{string(DeliveryTDDGreen)}, []string{"tdd_green", "unit_test", "contract_test"}) || !hasLiveReceipt(input, []string{"REGRESSION", "TESTS_VERIFIED"}, []string{"regression", "full_tests", "test_suite"}) {
		return fmt.Errorf("%w: full relevant tests", ErrLiveGate)
	}
	if err := requireLiveReceipt(input, "build", []string{string(DeliveryBuild), "BUILT"}, []string{"build", "artifact"}, true); err != nil {
		return err
	}
	if !hasLiveReceipt(input, []string{"ECOSYSTEM_VERIFIED"}, []string{"ecosystem_verified", "ecosystem"}) {
		return fmt.Errorf("%w: ecosystem verification", ErrLiveGate)
	}
	if err := requireLiveReceipt(input, "deploy", []string{string(DeliveryDeploy), "DEPLOYED"}, []string{"deploy", "deploy_receipt"}, false); err != nil {
		return err
	}
	if err := requireLiveReceipt(input, "restart", []string{string(DeliveryRestart), "RESTARTED"}, []string{"restart", "restart_receipt"}, false); err != nil {
		return err
	}
	if err := requireLiveReceipt(input, "process identity", []string{"PROCESS_IDENTITY_VERIFIED", "PROCESS_VERIFIED"}, []string{"process_identity", "process_identity_verified", "runtime_identity"}, false); err != nil {
		return err
	}
	if err := requireLiveReceipt(input, "readiness", []string{string(DeliveryPostDeployVerify), "READINESS_VERIFIED"}, []string{"readiness", "health", "post_deploy_verify"}, false); err != nil {
		return err
	}
	if err := requireLiveReceipt(input, "production smoke", []string{string(DeliveryLiveVerified), "PRODUCTION_VERIFIED"}, []string{"production_smoke", "e2e", "production_verified"}, false); err != nil {
		return err
	}
	if err := requireLiveReceipt(input, "viewer verification", []string{"VIEWER_VERIFIED"}, []string{"viewer_verification", "viewer"}, false); err != nil {
		return err
	}
	var taskReview, branchReview bool
	for _, review := range input.Reviews {
		if err := ValidateReviewRecord(review); err != nil {
			return fmt.Errorf("%w: invalid review %q: %v", ErrLiveGate, review.ReviewID, err)
		}
		if review.UnitID != input.UnitID || review.PlanID != input.PlanID || review.SpecRef == "" || !strings.EqualFold(review.SpecHash, input.SpecHash) || review.ValidForRevision != input.Revision || review.Verdict != ReviewAccepted {
			return fmt.Errorf("%w: review %q has stale, foreign, or rejected bindings", ErrLiveGate, review.ReviewID)
		}
		for _, evidenceID := range review.EvidenceRefs {
			if _, exists := evidenceIDs[evidenceID]; !exists {
				return fmt.Errorf("%w: review %q references unknown evidence %q", ErrLiveGate, review.ReviewID, evidenceID)
			}
		}
		switch review.ReviewType {
		case ReviewTypeTask:
			taskReview = true
		case ReviewTypeBranch:
			branchReview = true
		}
	}
	if !taskReview || !branchReview {
		return fmt.Errorf("%w: accepted independent task and branch reviews", ErrLiveGate)
	}
	return nil
}

func liveGateReceipts(input LiveGateInput) []EvidenceReceipt {
	items := []EvidenceReceipt{
		firstNonZeroReceipt(input.BuildEvidence, input.BuildReceipt),
		firstNonZeroReceipt(input.EcosystemEvidence),
		firstNonZeroReceipt(input.DeployEvidence, input.DeploymentReceipt),
		firstNonZeroReceipt(input.RestartEvidence, input.RestartReceipt),
		firstNonZeroReceipt(input.ProcessIdentityEvidence),
		firstNonZeroReceipt(input.ReadinessEvidence, input.ReadinessReceipt),
		firstNonZeroReceipt(input.ProductionSmokeEvidence, input.ProductionSmokeReceipt),
		firstNonZeroReceipt(input.ViewerVerificationEvidence, input.ViewerVerificationReceipt),
	}
	items = append(items, input.Evidence...)
	out := make([]EvidenceReceipt, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.EvidenceID) != "" {
			out = append(out, item)
		}
	}
	return out
}

func ValidateLiveGate(input LiveGateInput) error { return ValidateLIVEGate(input) }
