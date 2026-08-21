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
		return []string{"tdd_red"}
	case DeliveryTDDGreen:
		return []string{"tdd_green", "unit_test", "contract_test"}
	case DeliveryRefactor:
		return []string{"refactor", "unit_test"}
	case DeliveryE2EPredeploy:
		return []string{"e2e", "e2e_predeploy"}
	case DeliveryBuild:
		return []string{"build", "artifact"}
	case DeliveryDeploy:
		return []string{"deploy_receipt", "deploy"}
	case DeliveryRestart:
		return []string{"restart_receipt", "restart"}
	case DeliveryPostDeployVerify:
		return []string{"post_deploy_verify", "health", "readiness", "production_smoke"}
	case DeliveryLiveVerified:
		return []string{"live_verified", "production_smoke", "readiness"}
	case DeliveryDone:
		return []string{"live_verified"}
	default:
		return nil
	}
}

func evidenceMatches(ref EvidenceRef, names []string) bool {
	if !ref.Passed {
		return false
	}
	stage := strings.ToLower(strings.TrimSpace(ref.Stage))
	kind := strings.ToLower(strings.TrimSpace(ref.Kind))
	for _, name := range names {
		name = strings.ToLower(name)
		if stage == name || kind == name {
			return true
		}
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
	{stage: DeliveryTDDRed, kinds: []string{"tdd_red"}},
	{stage: DeliveryTDDGreen, kinds: []string{"tdd_green", "unit_test", "contract_test"}},
	{stage: DeliveryRefactor, kinds: []string{"refactor"}},
	{stage: DeliveryE2EPredeploy, kinds: []string{"e2e", "e2e_predeploy"}},
	{stage: DeliveryBuild, kinds: []string{"build", "artifact"}},
	{stage: DeliveryDeploy, kinds: []string{"deploy_receipt", "deploy"}},
	{stage: DeliveryRestart, kinds: []string{"restart_receipt", "restart"}},
	{stage: DeliveryPostDeployVerify, kinds: []string{"post_deploy_verify", "health", "readiness"}},
	{stage: DeliveryLiveVerified, kinds: []string{"live_verified", "production_smoke"}},
}

func evidenceMatchesRequirement(ref EvidenceRef, requirement evidenceRequirement) bool {
	if !ref.Passed {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(ref.Stage), requirement.stage) {
		return true
	}
	return evidenceMatches(ref, requirement.kinds)
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
		if evidenceMatches(ref, names) {
			return true
		}
		if strings.EqualFold(strings.TrimSpace(ref.Stage), strings.TrimSpace(target)) && ref.Passed {
			return true
		}
	}
	return false
}

func appendEvidence(existing []EvidenceRef, incoming []EvidenceRef) []EvidenceRef {
	out := append([]EvidenceRef(nil), existing...)
	seen := make(map[string]struct{}, len(out))
	for _, ref := range out {
		seen[ref.Key()] = struct{}{}
	}
	for _, ref := range incoming {
		if strings.TrimSpace(ref.Ref) == "" && strings.TrimSpace(ref.Kind) == "" && strings.TrimSpace(ref.Stage) == "" {
			continue
		}
		key := ref.Key()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
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
