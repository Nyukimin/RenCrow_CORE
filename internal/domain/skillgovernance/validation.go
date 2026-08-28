package skillgovernance

import (
	"errors"
	"strconv"
	"strings"
)

func ValidateSkillManifest(item SkillManifest) error {
	if strings.TrimSpace(item.SkillID) == "" {
		return errors.New("skill_id is required")
	}
	if strings.TrimSpace(item.Name) == "" {
		return errors.New("name is required")
	}
	if strings.TrimSpace(item.Scope) == "" {
		return errors.New("scope is required")
	}
	if strings.TrimSpace(item.Path) == "" {
		return errors.New("path is required")
	}
	if item.UpdatedAt.IsZero() {
		return errors.New("updated_at is required")
	}
	if err := ValidateDevelopmentContract(item.DevelopmentContract); err != nil {
		return err
	}
	return nil
}

// ValidateDevelopmentContract validates a declared skill contract without
// interpreting it as an authorization grant. Runtime capability and policy
// checks remain the responsibility of the owning runtime boundary.
func ValidateDevelopmentContract(item DevelopmentContract) error {
	if item.IsZero() {
		return nil
	}
	if strings.TrimSpace(item.RequiredCapability) == "" {
		return errors.New("development_contract.required_capability is required")
	}
	if err := validateNonBlankList("development_contract.required_tools", item.RequiredTools); err != nil {
		return err
	}
	if err := validateNonBlankList("development_contract.required_knowledge", item.RequiredKnowledge); err != nil {
		return err
	}
	if strings.TrimSpace(item.AuthorityRequirement) == "" {
		return errors.New("development_contract.authority_requirement is required")
	}
	if strings.TrimSpace(item.InputContract) == "" {
		return errors.New("development_contract.input_contract is required")
	}
	if strings.TrimSpace(item.OutputContract) == "" {
		return errors.New("development_contract.output_contract is required")
	}
	if strings.TrimSpace(item.CostHint) == "" {
		return errors.New("development_contract.cost_hint is required")
	}
	if strings.TrimSpace(item.RiskLevel) == "" {
		return errors.New("development_contract.risk_level is required")
	}
	if strings.TrimSpace(item.EvaluationMethod) == "" {
		return errors.New("development_contract.evaluation_method is required")
	}
	if strings.TrimSpace(item.Version) == "" {
		return errors.New("development_contract.version is required")
	}
	return nil
}

func validateNonBlankList(name string, values []string) error {
	if len(values) == 0 {
		return errors.New(name + " is required")
	}
	for index, value := range values {
		if strings.TrimSpace(value) == "" {
			return errors.New(name + "[" + strconv.Itoa(index) + "] must not be blank")
		}
	}
	return nil
}

func ValidateSkillTriggerLog(item SkillTriggerLog) error {
	if strings.TrimSpace(item.EventID) == "" {
		return errors.New("event_id is required")
	}
	if strings.TrimSpace(item.SkillID) == "" {
		return errors.New("skill_id is required")
	}
	if strings.TrimSpace(item.Status) == "" {
		return errors.New("status is required")
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}

func ValidateSkillChangeLog(item SkillChangeLog) error {
	if strings.TrimSpace(item.ChangeID) == "" {
		return errors.New("change_id is required")
	}
	if strings.TrimSpace(item.SkillID) == "" {
		return errors.New("skill_id is required")
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}

func ValidateContributionGateLog(item ContributionGateLog) error {
	if strings.TrimSpace(item.EventID) == "" {
		return errors.New("event_id is required")
	}
	if strings.TrimSpace(item.Repo) == "" {
		return errors.New("repo is required")
	}
	if strings.TrimSpace(item.GateStatus) == "" {
		return errors.New("gate_status is required")
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}

func ValidateCoderTranscriptEntry(item CoderTranscriptEntry) error {
	if strings.TrimSpace(item.EventID) == "" {
		return errors.New("event_id is required")
	}
	if strings.TrimSpace(item.Role) == "" {
		return errors.New("role is required")
	}
	if strings.TrimSpace(item.Segment) == "" {
		return errors.New("segment is required")
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}
