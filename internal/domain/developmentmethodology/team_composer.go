package developmentmethodology

import (
	"errors"
	"sort"
	"strings"

	skillgovernance "github.com/Nyukimin/RenCrow_CORE/internal/domain/skillgovernance"
)

type TeamCandidate struct {
	ExecutionID  string        `json:"execution_id"`
	Role         ExecutionRole `json:"role"`
	Capabilities []string      `json:"capabilities"`
	Tools        []string      `json:"tools"`
}

type TeamAssignment struct {
	ExecutionID string        `json:"execution_id"`
	Role        ExecutionRole `json:"role"`
	SkillID     string        `json:"skill_id"`
}

// ComposeTeam selects an execution instance by declared capability and tool
// availability, then applies the same role authority gate used at execution.
// It does not bind an Agent identity, model, or provider and cannot grant a
// capability that the candidate did not already advertise.
func ComposeTeam(skill skillgovernance.SkillManifest, operation AuthorityOperation, candidates []TeamCandidate) (TeamAssignment, error) {
	if err := skillgovernance.ValidateSkillManifest(skill); err != nil {
		return TeamAssignment{}, err
	}
	if skill.DevelopmentContract.IsZero() {
		return TeamAssignment{}, errors.New("development skill contract is required")
	}
	ordered := append([]TeamCandidate(nil), candidates...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].ExecutionID < ordered[j].ExecutionID })
	for _, candidate := range ordered {
		if strings.TrimSpace(candidate.ExecutionID) == "" || !containsFold(candidate.Capabilities, skill.DevelopmentContract.RequiredCapability) || !containsAllFold(candidate.Tools, skill.DevelopmentContract.RequiredTools) {
			continue
		}
		if !AuthorityAllows(candidate.Role, operation) {
			continue
		}
		return TeamAssignment{ExecutionID: candidate.ExecutionID, Role: candidate.Role, SkillID: skill.SkillID}, nil
	}
	return TeamAssignment{}, ErrAuthorityDenied
}

func containsFold(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(wanted)) {
			return true
		}
	}
	return false
}

func containsAllFold(values, wanted []string) bool {
	for _, item := range wanted {
		if !containsFold(values, item) {
			return false
		}
	}
	return true
}
