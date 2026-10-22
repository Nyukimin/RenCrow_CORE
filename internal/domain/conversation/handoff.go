package conversation

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	MaxReadbackEnvelopeBytes = 64 * 1024
	MaxReadbackTextBytes     = 16 * 1024
	MaxHandoffTextBytes      = 16 * 1024
	MaxHandoffConstraints    = 64
	MaxHandoffCapabilities   = 64
	MaxReportSummaryBytes    = 16 * 1024
	MaxReportResultBytes     = 64 * 1024
	MaxReportReferences      = 128
	MaxReportLimitations     = 64
	MaxReportLimitationBytes = 4 * 1024

	readbackSchemaVersion = "agent.readback.v1"
)

// HandoffEnvelope contains CORE-assigned routing identity and the bounded
// request content accepted from an AgentEmission. Its capabilities are issued
// by the runtime and are not inferred from any readback or report text.
type HandoffEnvelope struct {
	DelegationID         core.RequestID    `json:"delegation_id"`
	Revision             uint64            `json:"revision"`
	OriginTurnID         core.TurnID       `json:"origin_turn_id"`
	OriginUserMessageID  core.MessageID    `json:"origin_user_message_id"`
	SessionID            core.SessionID    `json:"session_id"`
	SourceTraceID        core.TraceID      `json:"source_trace_id"`
	FromActor            string            `json:"from_actor"`
	ToActor              string            `json:"to_actor"`
	InstructionMessageID core.MessageID    `json:"instruction_message_id"`
	ContextSnapshotHash  string            `json:"context_snapshot_hash"`
	AllowedCapabilities  []string          `json:"allowed_capabilities"`
	ReplyTo              core.MessageID    `json:"reply_to"`
	Goal                 string            `json:"goal"`
	Instruction          string            `json:"instruction"`
	Constraints          []AgentConstraint `json:"constraints"`
	ExpectedOutput       string            `json:"expected_output"`
}

// ValidateHandoffEnvelope checks canonical identity, revision, and bounded
// references before the envelope is accepted by a handoff owner.
func ValidateHandoffEnvelope(envelope HandoffEnvelope) error {
	ids := []struct {
		name string
		err  error
	}{
		{"delegation_id", envelope.DelegationID.Validate()},
		{"origin_turn_id", envelope.OriginTurnID.Validate()},
		{"origin_user_message_id", envelope.OriginUserMessageID.Validate()},
		{"session_id", envelope.SessionID.Validate()},
		{"source_trace_id", envelope.SourceTraceID.Validate()},
		{"instruction_message_id", envelope.InstructionMessageID.Validate()},
		{"reply_to", envelope.ReplyTo.Validate()},
	}
	for _, id := range ids {
		if id.err != nil {
			return fmt.Errorf("handoff %s is invalid: %w", id.name, id.err)
		}
	}
	if envelope.Revision == 0 {
		return fmt.Errorf("handoff revision must be positive")
	}
	if !validActorLabel(envelope.FromActor) || !validActorLabel(envelope.ToActor) || envelope.FromActor == envelope.ToActor {
		return fmt.Errorf("handoff actors are invalid")
	}
	if len(envelope.ContextSnapshotHash) != 64 || strings.ToLower(envelope.ContextSnapshotHash) != envelope.ContextSnapshotHash {
		return fmt.Errorf("handoff context_snapshot_hash must be lowercase SHA-256 hex")
	}
	if _, err := hex.DecodeString(envelope.ContextSnapshotHash); err != nil {
		return fmt.Errorf("handoff context_snapshot_hash is invalid: %w", err)
	}
	for field, value := range map[string]string{
		"goal": envelope.Goal, "instruction": envelope.Instruction, "expected_output": envelope.ExpectedOutput,
	} {
		if err := validateRequiredText(field, value, MaxHandoffTextBytes); err != nil {
			return fmt.Errorf("handoff %w", err)
		}
	}
	if len(envelope.Constraints) > MaxHandoffConstraints {
		return fmt.Errorf("handoff has too many constraints")
	}
	if len(envelope.AllowedCapabilities) > MaxHandoffCapabilities {
		return fmt.Errorf("handoff has too many allowed_capabilities")
	}
	seenCapabilities := make(map[string]struct{}, len(envelope.AllowedCapabilities))
	for _, capability := range envelope.AllowedCapabilities {
		if !validIdentifier(capability, 128) {
			return fmt.Errorf("handoff allowed capability is invalid")
		}
		if _, exists := seenCapabilities[capability]; exists {
			return fmt.Errorf("handoff allowed_capabilities contains a duplicate")
		}
		seenCapabilities[capability] = struct{}{}
	}
	seenConstraintKeys := make(map[string]struct{}, len(envelope.Constraints))
	sourceMessageCount := 0
	for _, constraint := range envelope.Constraints {
		if !validIdentifier(constraint.Key, 64) {
			return fmt.Errorf("handoff constraint key is invalid")
		}
		if _, exists := seenConstraintKeys[constraint.Key]; exists {
			return fmt.Errorf("handoff constraint keys must be unique")
		}
		seenConstraintKeys[constraint.Key] = struct{}{}
		if err := validateRequiredText("constraint text", constraint.Text, MaxHandoffTextBytes); err != nil {
			return fmt.Errorf("handoff %w", err)
		}
		for _, sourceID := range constraint.SourceMessageIDs {
			if err := sourceID.Validate(); err != nil {
				return fmt.Errorf("handoff constraint source_message_id is invalid: %w", err)
			}
			sourceMessageCount++
		}
	}
	if sourceMessageCount > MaxSourceMessageIDs {
		return fmt.Errorf("handoff has too many source_message_ids")
	}
	return nil
}

type ReadbackStatus string

const (
	ReadbackStatusAccepted           ReadbackStatus = "accepted"
	ReadbackStatusNeedsClarification ReadbackStatus = "needs_clarification"
	ReadbackStatusRejected           ReadbackStatus = "rejected"
)

// TrustedReadbackMetadata must be populated from the authenticated handoff
// transport. These values are never accepted from model JSON.
type TrustedReadbackMetadata struct {
	DelegationID core.RequestID
	Revision     uint64
	Sender       string
}

// ReadbackEnvelope combines the structured model response with identity bound
// by the authenticated runtime. Runtime-bound fields are omitted from JSON.
type ReadbackEnvelope struct {
	DelegationID               core.RequestID `json:"-"`
	Revision                   uint64         `json:"-"`
	Sender                     string         `json:"-"`
	SchemaVersion              string         `json:"schema_version"`
	Status                     ReadbackStatus `json:"status"`
	UnderstoodGoal             string         `json:"understood_goal"`
	AcknowledgedConstraintKeys []string       `json:"acknowledged_constraint_keys"`
	ExpectedOutput             string         `json:"expected_output"`
	Text                       string         `json:"text"`
}

// ParseAgentReadback strictly parses the model's structured response and binds
// it to the handoff identity supplied separately by the trusted runtime.
func ParseAgentReadback(raw []byte, handoff HandoffEnvelope, trusted TrustedReadbackMetadata) (ReadbackEnvelope, error) {
	if err := ValidateHandoffEnvelope(handoff); err != nil {
		return ReadbackEnvelope{}, fmt.Errorf("invalid handoff for readback: %w", err)
	}
	if err := validateReadbackBinding(handoff, trusted); err != nil {
		return ReadbackEnvelope{}, err
	}
	if len(raw) == 0 || len(raw) > MaxReadbackEnvelopeBytes || !utf8.Valid(raw) {
		return ReadbackEnvelope{}, fmt.Errorf("readback envelope is empty, oversized, or invalid UTF-8")
	}
	if err := validateSingleJSONEnvelope(raw); err != nil {
		return ReadbackEnvelope{}, fmt.Errorf("invalid readback JSON envelope")
	}
	fields, err := strictObjectFields(raw,
		[]string{"schema_version", "status", "understood_goal", "acknowledged_constraint_keys", "expected_output", "text"},
		"schema_version", "status", "understood_goal", "acknowledged_constraint_keys", "expected_output", "text")
	if err != nil || !isJSONArray(fields["acknowledged_constraint_keys"]) {
		return ReadbackEnvelope{}, fmt.Errorf("readback fields are invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var readback ReadbackEnvelope
	if err := decoder.Decode(&readback); err != nil {
		return ReadbackEnvelope{}, fmt.Errorf("readback JSON does not match agent.readback.v1: %w", err)
	}
	readback.DelegationID = trusted.DelegationID
	readback.Revision = trusted.Revision
	readback.Sender = trusted.Sender
	if err := ValidateReadbackEnvelope(readback, handoff, trusted); err != nil {
		return ReadbackEnvelope{}, err
	}
	return readback, nil
}

// ValidateReadbackEnvelope validates a parsed readback against its current
// handoff. Accepted means the receiver reported understanding; it grants no
// capability and does not start work by itself.
func ValidateReadbackEnvelope(readback ReadbackEnvelope, handoff HandoffEnvelope, trusted TrustedReadbackMetadata) error {
	if err := ValidateHandoffEnvelope(handoff); err != nil {
		return fmt.Errorf("invalid handoff for readback: %w", err)
	}
	if err := validateReadbackBinding(handoff, trusted); err != nil {
		return err
	}
	if readback.DelegationID != trusted.DelegationID || readback.Revision != trusted.Revision || readback.Sender != trusted.Sender {
		return fmt.Errorf("readback runtime identity does not match trusted transport")
	}
	if readback.SchemaVersion != readbackSchemaVersion {
		return fmt.Errorf("readback schema_version is invalid")
	}
	switch readback.Status {
	case ReadbackStatusAccepted, ReadbackStatusNeedsClarification, ReadbackStatusRejected:
	default:
		return fmt.Errorf("readback status is invalid")
	}
	for field, value := range map[string]string{
		"understood_goal": readback.UnderstoodGoal,
		"expected_output": readback.ExpectedOutput,
		"text":            readback.Text,
	} {
		if err := validateRequiredText(field, value, MaxReadbackTextBytes); err != nil {
			return fmt.Errorf("readback %w", err)
		}
	}
	if len(readback.AcknowledgedConstraintKeys) > MaxHandoffConstraints {
		return fmt.Errorf("readback has too many acknowledged_constraint_keys")
	}
	requiredKeys := make(map[string]struct{}, len(handoff.Constraints))
	for _, constraint := range handoff.Constraints {
		requiredKeys[constraint.Key] = struct{}{}
	}
	acknowledged := make(map[string]struct{}, len(readback.AcknowledgedConstraintKeys))
	for _, key := range readback.AcknowledgedConstraintKeys {
		if !validIdentifier(key, 64) {
			return fmt.Errorf("readback acknowledged constraint key is invalid")
		}
		if _, exists := requiredKeys[key]; !exists {
			return fmt.Errorf("readback acknowledges an unknown constraint key")
		}
		if _, exists := acknowledged[key]; exists {
			return fmt.Errorf("readback acknowledged constraint keys contain a duplicate")
		}
		acknowledged[key] = struct{}{}
	}
	if readback.Status == ReadbackStatusAccepted && len(acknowledged) != len(requiredKeys) {
		return fmt.Errorf("accepted readback does not acknowledge every required constraint")
	}
	return nil
}

func validateReadbackBinding(handoff HandoffEnvelope, trusted TrustedReadbackMetadata) error {
	if err := trusted.DelegationID.Validate(); err != nil {
		return fmt.Errorf("trusted readback delegation_id is invalid: %w", err)
	}
	if trusted.DelegationID != handoff.DelegationID {
		return fmt.Errorf("readback belongs to a different delegation")
	}
	if trusted.Revision == 0 || trusted.Revision != handoff.Revision {
		return fmt.Errorf("readback revision is stale")
	}
	if !validActorLabel(trusted.Sender) || trusted.Sender != handoff.ToActor {
		return fmt.Errorf("readback sender does not match the assigned actor")
	}
	return nil
}

type ReportStatus string

const (
	ReportStatusSucceeded ReportStatus = "succeeded"
	ReportStatusPartial   ReportStatus = "partial"
	ReportStatusFailed    ReportStatus = "failed"
	ReportStatusCanceled  ReportStatus = "canceled"
)

// ReportEnvelope carries a worker report. Sender is attached from the trusted
// transport and is deliberately absent from the JSON report body.
type ReportEnvelope struct {
	ReportID           core.ResponseID   `json:"report_id"`
	ReportRevision     uint64            `json:"report_revision"`
	DelegationID       core.RequestID    `json:"delegation_id"`
	DelegationRevision uint64            `json:"delegation_revision"`
	WorkTaskID         core.TaskID       `json:"work_task_id"`
	WorkRunID          core.RunID        `json:"work_run_id"`
	Status             ReportStatus      `json:"status"`
	Summary            string            `json:"summary"`
	Result             string            `json:"result"`
	EvidenceRefs       []core.EvidenceID `json:"evidence_refs"`
	ArtifactRefs       []core.ArtifactID `json:"artifact_refs"`
	Limitations        []string          `json:"limitations"`
	Sender             string            `json:"-"`
}

// ValidateReportEnvelope checks canonical identities, current delegation
// revision, report status, trusted sender, and bounded canonical references.
func ValidateReportEnvelope(report ReportEnvelope, handoff HandoffEnvelope) error {
	if err := ValidateHandoffEnvelope(handoff); err != nil {
		return fmt.Errorf("invalid handoff for report: %w", err)
	}
	for _, id := range []struct {
		name string
		err  error
	}{
		{"report_id", report.ReportID.Validate()},
		{"delegation_id", report.DelegationID.Validate()},
		{"work_task_id", report.WorkTaskID.Validate()},
		{"work_run_id", report.WorkRunID.Validate()},
	} {
		if id.err != nil {
			return fmt.Errorf("report %s is invalid: %w", id.name, id.err)
		}
	}
	if report.DelegationID != handoff.DelegationID || report.DelegationRevision != handoff.Revision {
		return fmt.Errorf("report references a stale or different delegation revision")
	}
	if report.ReportRevision == 0 {
		return fmt.Errorf("report_revision must be positive")
	}
	if !validActorLabel(report.Sender) || report.Sender != handoff.ToActor {
		return fmt.Errorf("report sender does not match the assigned actor")
	}
	switch report.Status {
	case ReportStatusSucceeded, ReportStatusPartial, ReportStatusFailed, ReportStatusCanceled:
	default:
		return fmt.Errorf("report status is invalid")
	}
	if err := validateRequiredText("summary", report.Summary, MaxReportSummaryBytes); err != nil {
		return fmt.Errorf("report %w", err)
	}
	if err := validateRequiredText("result", report.Result, MaxReportResultBytes); err != nil {
		return fmt.Errorf("report %w", err)
	}
	if len(report.EvidenceRefs)+len(report.ArtifactRefs) > MaxReportReferences {
		return fmt.Errorf("report has too many evidence_refs and artifact_refs")
	}
	seenEvidence := make(map[core.EvidenceID]struct{}, len(report.EvidenceRefs))
	for _, ref := range report.EvidenceRefs {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("report evidence_ref is invalid: %w", err)
		}
		if _, exists := seenEvidence[ref]; exists {
			return fmt.Errorf("report evidence_refs contain a duplicate")
		}
		seenEvidence[ref] = struct{}{}
	}
	seenArtifacts := make(map[core.ArtifactID]struct{}, len(report.ArtifactRefs))
	for _, ref := range report.ArtifactRefs {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("report artifact_ref is invalid: %w", err)
		}
		if _, exists := seenArtifacts[ref]; exists {
			return fmt.Errorf("report artifact_refs contain a duplicate")
		}
		seenArtifacts[ref] = struct{}{}
	}
	if len(report.Limitations) > MaxReportLimitations {
		return fmt.Errorf("report has too many limitations")
	}
	for _, limitation := range report.Limitations {
		if err := validateRequiredText("limitation", limitation, MaxReportLimitationBytes); err != nil {
			return fmt.Errorf("report %w", err)
		}
	}
	return nil
}

func validateRequiredText(field, value string, maxBytes int) error {
	if !utf8.ValidString(value) || strings.TrimSpace(value) == "" || len([]byte(value)) > maxBytes {
		return fmt.Errorf("%s is empty, oversized, or invalid UTF-8", field)
	}
	return nil
}

func validActorLabel(value string) bool {
	return validIdentifier(value, 64)
}

func validIdentifier(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for i := 1; i < len(value); i++ {
		c := value[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.' || c == ':' {
			continue
		}
		return false
	}
	return true
}
