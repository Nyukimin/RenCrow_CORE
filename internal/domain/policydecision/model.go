package policydecision

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

const RecordVersion = 1

type Outcome string

const (
	OutcomeAllowed     Outcome = "allowed"
	OutcomeBlocked     Outcome = "blocked"
	OutcomeUnavailable Outcome = "unavailable"
	OutcomeDegraded    Outcome = "degraded"
)

type Record struct {
	RecordVersion          int       `json:"record_version"`
	DecisionID             string    `json:"decision_id"`
	TraceID                string    `json:"trace_id,omitempty"`
	RequestID              string    `json:"request_id,omitempty"`
	Requester              string    `json:"requester"`
	AuthenticatedScopes    []string  `json:"authenticated_scopes,omitempty"`
	Module                 string    `json:"module"`
	Action                 string    `json:"action"`
	ResourceRef            string    `json:"resource_ref,omitempty"`
	BinaryContractRevision string    `json:"binary_contract_revision"`
	GlobalBundleRevision   string    `json:"global_bundle_revision"`
	ModulePolicyRevision   string    `json:"module_policy_revision"`
	DeploymentRevision     string    `json:"deployment_revision"`
	MatchedPolicyIDs       []string  `json:"matched_policy_ids,omitempty"`
	Outcome                Outcome   `json:"outcome"`
	Reasons                []string  `json:"reasons"`
	InputSnapshotSHA256    string    `json:"input_snapshot_sha256,omitempty"`
	AuthorizationID        string    `json:"authorization_id,omitempty"`
	DomainDecisionID       string    `json:"domain_decision_id,omitempty"`
	ExecutionResult        string    `json:"execution_result,omitempty"`
	CreatedAt              time.Time `json:"created_at"`
}

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func Validate(record Record) error {
	if record.RecordVersion != RecordVersion {
		return fmt.Errorf("record_version must be %d", RecordVersion)
	}
	for name, value := range map[string]string{
		"decision_id":              record.DecisionID,
		"requester":                record.Requester,
		"module":                   record.Module,
		"action":                   record.Action,
		"binary_contract_revision": record.BinaryContractRevision,
		"global_bundle_revision":   record.GlobalBundleRevision,
		"module_policy_revision":   record.ModulePolicyRevision,
		"deployment_revision":      record.DeploymentRevision,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	switch record.Outcome {
	case OutcomeAllowed, OutcomeBlocked, OutcomeUnavailable, OutcomeDegraded:
	default:
		return fmt.Errorf("outcome is invalid: %q", record.Outcome)
	}
	if len(record.Reasons) == 0 {
		return fmt.Errorf("reasons is required")
	}
	for _, reason := range record.Reasons {
		if strings.TrimSpace(reason) == "" {
			return fmt.Errorf("reasons must not contain an empty value")
		}
	}
	if record.InputSnapshotSHA256 != "" && !sha256Pattern.MatchString(record.InputSnapshotSHA256) {
		return fmt.Errorf("input_snapshot_sha256 must be lowercase SHA-256")
	}
	if record.CreatedAt.IsZero() {
		return fmt.Errorf("created_at is required")
	}
	return nil
}
