package backlog

import (
	"errors"
	"fmt"
	"strings"
)

var ErrInvalidTransition = errors.New("invalid atlas state transition")
var ErrEvidenceRequired = errors.New("required evidence is missing")

func validConceptState(value string) bool {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case ConceptRadar, ConceptCandidate, ConceptAdopted, ConceptDeferred, ConceptRejected:
		return true
	default:
		return false
	}
}

func validDeliveryState(value string) bool {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case DeliveryNone, DeliveryQueued, DeliverySpec, DeliveryTDDRed, DeliveryTDDGreen,
		DeliveryRefactor, DeliveryE2EPredeploy, DeliveryBuild, DeliveryDeploy,
		DeliveryRestart, DeliveryPostDeployVerify, DeliveryLiveVerified,
		DeliveryDone, DeliveryBlocked, DeliveryRejected:
		return true
	default:
		return false
	}
}

// ValidateConceptTransition checks the owner-controlled concept state graph.
func ValidateConceptTransition(from, to string) error {
	from = strings.ToUpper(strings.TrimSpace(from))
	to = strings.ToUpper(strings.TrimSpace(to))
	if !validConceptState(from) || !validConceptState(to) {
		return fmt.Errorf("%w: unknown concept state %q -> %q", ErrInvalidTransition, from, to)
	}
	allowed := map[string]map[string]bool{
		ConceptRadar:     {ConceptCandidate: true, ConceptRejected: true},
		ConceptCandidate: {ConceptAdopted: true, ConceptDeferred: true, ConceptRejected: true},
		ConceptAdopted:   {},
		ConceptDeferred:  {ConceptCandidate: true, ConceptAdopted: true, ConceptRejected: true},
		ConceptRejected:  {},
	}
	if allowed[from][to] {
		return nil
	}
	return fmt.Errorf("%w: concept %s -> %s", ErrInvalidTransition, from, to)
}

// ValidateDeliveryTransition checks one deterministic adjacent delivery step.
// A caller cannot skip stages by supplying a later state in one request.
func ValidateDeliveryTransition(from, to string) error {
	from = strings.ToUpper(strings.TrimSpace(from))
	to = strings.ToUpper(strings.TrimSpace(to))
	if !validDeliveryState(from) || !validDeliveryState(to) {
		return fmt.Errorf("%w: unknown delivery state %q -> %q", ErrInvalidTransition, from, to)
	}
	allowed := map[string]map[string]bool{
		DeliveryNone:             {DeliveryQueued: true, DeliveryBlocked: true, DeliveryRejected: true},
		DeliveryQueued:           {DeliverySpec: true, DeliveryBlocked: true, DeliveryRejected: true},
		DeliverySpec:             {DeliveryTDDRed: true, DeliveryBlocked: true, DeliveryRejected: true},
		DeliveryTDDRed:           {DeliveryTDDGreen: true, DeliveryBlocked: true, DeliveryRejected: true},
		DeliveryTDDGreen:         {DeliveryRefactor: true, DeliveryBlocked: true, DeliveryRejected: true},
		DeliveryRefactor:         {DeliveryE2EPredeploy: true, DeliveryBlocked: true, DeliveryRejected: true},
		DeliveryE2EPredeploy:     {DeliveryBuild: true, DeliveryBlocked: true, DeliveryRejected: true},
		DeliveryBuild:            {DeliveryDeploy: true, DeliveryBlocked: true, DeliveryRejected: true},
		DeliveryDeploy:           {DeliveryRestart: true, DeliveryBlocked: true, DeliveryRejected: true},
		DeliveryRestart:          {DeliveryPostDeployVerify: true, DeliveryBlocked: true, DeliveryRejected: true},
		DeliveryPostDeployVerify: {DeliveryLiveVerified: true, DeliveryBlocked: true, DeliveryRejected: true},
		DeliveryLiveVerified:     {DeliveryDone: true},
		DeliveryDone:             {},
		DeliveryBlocked:          {},
		DeliveryRejected:         {},
	}
	if allowed[from][to] {
		return nil
	}
	return fmt.Errorf("%w: delivery %s -> %s", ErrInvalidTransition, from, to)
}

// RequiredEvidence determines the minimum successful evidence needed to leave
// a stage. The stage may be spelled using either the domain state or its
// evidence kind so receipts from existing stores remain easy to reference.
func RequiredEvidence(target string) []string {
	switch strings.ToUpper(strings.TrimSpace(target)) {
	case DeliverySpec:
		return []string{"spec"}
	case DeliveryTDDRed:
		return []string{"tdd_red", "execution_report"}
	case DeliveryTDDGreen:
		return []string{"tdd_green", "unit_test", "contract_test", "execution_report"}
	case DeliveryRefactor:
		return []string{"refactor", "unit_test", "execution_report"}
	case DeliveryE2EPredeploy:
		return []string{"e2e", "e2e_predeploy", "execution_report"}
	case DeliveryBuild:
		return []string{"build", "artifact", "execution_report"}
	case DeliveryDeploy:
		return []string{"deploy_receipt", "deploy"}
	case DeliveryRestart:
		return []string{"restart_receipt", "restart", "deploy_receipt"}
	case DeliveryPostDeployVerify:
		return []string{"post_deploy_verify", "health", "readiness"}
	case DeliveryLiveVerified:
		return []string{"live_verified", "production_smoke"}
	case DeliveryDone:
		return []string{"live_verified"}
	default:
		return nil
	}
}

// executionReportStages is intentionally small.  An execution report is a
// generic receipt used by pre-deploy work, but its authoritative stage must be
// bound by the CORE verifier.  It is never a substitute for a specification,
// deploy receipt, readiness, or production smoke observation.
var executionReportStages = map[string]bool{
	DeliveryTDDRed:       true,
	DeliveryTDDGreen:     true,
	DeliveryRefactor:     true,
	DeliveryE2EPredeploy: true,
	DeliveryBuild:        true,
}

func evidenceMatches(ref EvidenceRef, names []string) bool {
	return evidenceMatchesForTarget(ref, names, "")
}

func evidenceMatchesForTarget(ref EvidenceRef, names []string, target string) bool {
	// Passed is only an external claim.  Runtime gates consume the persisted
	// CORE verification result and never the request-side claim.
	if !ref.IsVerified() {
		return false
	}
	target = strings.ToUpper(strings.TrimSpace(target))
	stage := strings.ToUpper(strings.TrimSpace(ref.Stage))
	kind := strings.ToLower(strings.TrimSpace(ref.Kind))
	if kind == "" {
		return false
	}
	if kind == "execution_report" {
		// Unlike named legacy receipts, a generic execution report has no
		// meaning without the exact stage marker produced by the verifier.
		return target != "" && executionReportStages[target] && stage == target
	}
	for _, name := range names {
		if kind != strings.ToLower(strings.TrimSpace(name)) {
			continue
		}
		// The kind is the canonical identity for named receipts.  The stage
		// field is an operation marker and may be normalized to LIVE_VERIFIED
		// when a cumulative request carries the complete prior history.  A
		// kind can still satisfy only the stage whose RequiredEvidence list
		// contains it; arbitrary stage-only relabeling is never accepted.
		return true
	}
	return false
}

type evidenceRequirement struct {
	stage string
	kinds []string
}

// liveVerificationRequirements is deliberately cumulative. A live smoke test
// proves only the live observation; it cannot replace the source/spec, TDD,
// build, deploy, restart, or readiness receipts that led to that observation.
var liveVerificationRequirements = []evidenceRequirement{
	{stage: DeliverySpec, kinds: []string{"spec"}},
	{stage: DeliveryTDDRed, kinds: []string{"tdd_red", "execution_report"}},
	{stage: DeliveryTDDGreen, kinds: []string{"tdd_green", "unit_test", "contract_test", "execution_report"}},
	{stage: DeliveryRefactor, kinds: []string{"refactor", "execution_report"}},
	{stage: DeliveryE2EPredeploy, kinds: []string{"e2e", "e2e_predeploy", "execution_report"}},
	{stage: DeliveryBuild, kinds: []string{"build", "artifact", "execution_report"}},
	{stage: DeliveryDeploy, kinds: []string{"deploy_receipt", "deploy"}},
	{stage: DeliveryRestart, kinds: []string{"restart_receipt", "restart", "deploy_receipt"}},
	{stage: DeliveryPostDeployVerify, kinds: []string{"post_deploy_verify", "health", "readiness"}},
	{stage: DeliveryLiveVerified, kinds: []string{"live_verified", "production_smoke"}},
}

func evidenceMatchesRequirement(ref EvidenceRef, requirement evidenceRequirement) bool {
	return evidenceMatchesForTarget(ref, requirement.kinds, requirement.stage)
}

// HasRequiredEvidence allows the current item history and newly supplied refs
// to satisfy the gate. LIVE_VERIFIED and DONE require the complete cumulative
// stage history; a single live ref is never sufficient.
func HasRequiredEvidence(target string, refs []EvidenceRef) bool {
	target = strings.ToUpper(strings.TrimSpace(target))
	if target == DeliveryLiveVerified || target == DeliveryDone {
		for _, requirement := range liveVerificationRequirements {
			matched := false
			for _, ref := range refs {
				if evidenceMatchesRequirement(ref, requirement) {
					matched = true
					break
				}
			}
			if !matched {
				return false
			}
		}
		return true
	}
	names := RequiredEvidence(target)
	if len(names) == 0 {
		return true
	}
	for _, ref := range refs {
		if evidenceMatchesForTarget(ref, names, target) {
			return true
		}
	}
	return false
}

func appendEvidence(existing []EvidenceRef, incoming []EvidenceRef) []EvidenceRef {
	out := append([]EvidenceRef(nil), existing...)
	seen := make(map[string]int, len(out))
	for index, ref := range out {
		seen[ref.Key()] = index
	}
	for _, ref := range incoming {
		if strings.TrimSpace(ref.Ref) == "" && strings.TrimSpace(ref.Kind) == "" && strings.TrimSpace(ref.Stage) == "" {
			continue
		}
		key := ref.Key()
		if index, ok := seen[key]; ok {
			// A later CORE verifier may turn an earlier persisted claim into a
			// verified result.  Keep the append-only history compact while
			// allowing that monotonic upgrade.
			if !out[index].IsVerified() && ref.IsVerified() {
				out[index] = ref
			}
			continue
		}
		seen[key] = len(out)
		out = append(out, ref)
	}
	return out
}

// TransitionDelivery validates and applies exactly one stage transition.
func TransitionDelivery(item Item, target string, refs []EvidenceRef) (Item, error) {
	target = strings.ToUpper(strings.TrimSpace(target))
	if item.SchemaVersion < SchemaVersion2 {
		item = ProjectLegacy(item)
	}
	if item.DeliveryState == "" {
		item.DeliveryState = DeliveryNone
	}
	if err := ValidateDeliveryTransition(item.DeliveryState, target); err != nil {
		return item, err
	}
	combined := appendEvidence(item.EvidenceRefs, refs)
	if !HasRequiredEvidence(target, combined) {
		return item, fmt.Errorf("%w: transition to %s", ErrEvidenceRequired, target)
	}
	item.SchemaVersion = SchemaVersion2
	if item.ImplementationRevision < 1 {
		item.ImplementationRevision = 1
	}
	item.DeliveryState = target
	item.EvidenceRefs = combined
	item.Status = LegacyStatus(item)
	if target == DeliveryLiveVerified || target == DeliveryDone {
		item.CheckOK = true
	} else {
		item.CheckOK = false
	}
	return item, nil
}
