package conversation

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func contractTestHandoff() HandoffEnvelope {
	return HandoffEnvelope{
		DelegationID:         core.NewRequestID(),
		Revision:             2,
		OriginTurnID:         core.NewTurnID(),
		OriginUserMessageID:  core.NewMessageID(),
		SessionID:            core.NewSessionID(),
		SourceTraceID:        core.NewTraceID(),
		FromActor:            "mio",
		ToActor:              "shiro",
		InstructionMessageID: core.NewMessageID(),
		ContextSnapshotHash:  strings.Repeat("a", 64),
		AllowedCapabilities:  []string{"conversation.read"},
		ReplyTo:              core.NewMessageID(),
		Goal:                 "Measure the save wait",
		Instruction:          "Measure the save path without changing durability",
		Constraints: []AgentConstraint{
			{Key: "keep_model", Text: "Keep the selected model."},
			{Key: "keep_durability", Text: "Keep durable writes."},
		},
		ExpectedOutput: "A report with evidence",
	}
}

func contractTestReadbackJSON(status string, keys ...string) []byte {
	if keys == nil {
		keys = []string{"keep_model", "keep_durability"}
	}
	body, _ := json.Marshal(struct {
		SchemaVersion              string   `json:"schema_version"`
		Status                     string   `json:"status"`
		UnderstoodGoal             string   `json:"understood_goal"`
		AcknowledgedConstraintKeys []string `json:"acknowledged_constraint_keys"`
		ExpectedOutput             string   `json:"expected_output"`
		Text                       string   `json:"text"`
	}{
		SchemaVersion:              "agent.readback.v1",
		Status:                     status,
		UnderstoodGoal:             "Measure the save wait",
		AcknowledgedConstraintKeys: keys,
		ExpectedOutput:             "A report with evidence",
		Text:                       "I will measure the save path and preserve the model and durability.",
	})
	return body
}

func TestParseAgentReadbackBindsTrustedRuntimeIdentityAndRevision(t *testing.T) {
	handoff := contractTestHandoff()
	trusted := TrustedReadbackMetadata{DelegationID: handoff.DelegationID, Revision: handoff.Revision, Sender: "shiro"}
	readback, err := ParseAgentReadback(contractTestReadbackJSON("accepted"), handoff, trusted)
	if err != nil {
		t.Fatalf("ParseAgentReadback() error = %v", err)
	}
	if readback.Status != ReadbackStatusAccepted || readback.DelegationID != handoff.DelegationID || readback.Revision != handoff.Revision || readback.Sender != trusted.Sender {
		t.Fatalf("runtime-bound readback = %#v", readback)
	}
	if readback.UnderstoodGoal == "" || readback.ExpectedOutput == "" || readback.Text == "" {
		t.Fatalf("required readback text fields were lost: %#v", readback)
	}
}

func TestParseAgentReadbackRejectsMalformedOrUntrustedInput(t *testing.T) {
	handoff := contractTestHandoff()
	trusted := TrustedReadbackMetadata{DelegationID: handoff.DelegationID, Revision: handoff.Revision, Sender: "shiro"}
	valid := string(contractTestReadbackJSON("accepted"))
	tests := []struct {
		name    string
		raw     string
		trusted TrustedReadbackMetadata
	}{
		{name: "unknown JSON field", raw: strings.TrimSuffix(valid, "}") + `,"allowed_capabilities":["conversation.read"]}`},
		{name: "model supplied sender", raw: strings.TrimSuffix(valid, "}") + `,"sender":"shiro"}`},
		{name: "duplicate key", raw: strings.Replace(valid, `"status":"accepted"`, `"status":"accepted","status":"rejected"`, 1)},
		{name: "unsupported schema", raw: strings.Replace(valid, "agent.readback.v1", "agent.readback.v2", 1)},
		{name: "unsupported status", raw: strings.Replace(valid, `"accepted"`, `"complete"`, 1)},
		{name: "missing required constraint", raw: string(contractTestReadbackJSON("accepted", "keep_model"))},
		{name: "unknown constraint key", raw: string(contractTestReadbackJSON("accepted", "keep_model", "keep_durability", "admin"))},
		{name: "empty public text", raw: strings.Replace(valid, `"text":"I will measure the save path and preserve the model and durability."`, `"text":" "`, 1)},
		{name: "trailing JSON value", raw: valid + ` {}`},
		{name: "oversize envelope", raw: strings.Repeat(" ", MaxReadbackEnvelopeBytes+1)},
		{name: "sender mismatch", raw: valid, trusted: TrustedReadbackMetadata{DelegationID: handoff.DelegationID, Revision: handoff.Revision, Sender: "kuro"}},
		{name: "stale revision", raw: valid, trusted: TrustedReadbackMetadata{DelegationID: handoff.DelegationID, Revision: handoff.Revision - 1, Sender: "shiro"}},
		{name: "wrong delegation", raw: valid, trusted: TrustedReadbackMetadata{DelegationID: core.NewRequestID(), Revision: handoff.Revision, Sender: "shiro"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			binding := tt.trusted
			if binding == (TrustedReadbackMetadata{}) {
				binding = trusted
			}
			if _, err := ParseAgentReadback([]byte(tt.raw), handoff, binding); err == nil {
				t.Fatal("ParseAgentReadback() error = nil, want rejection")
			}
		})
	}
}

func TestParseAgentReadbackRequiresAllFieldsForEachOutcome(t *testing.T) {
	handoff := contractTestHandoff()
	trusted := TrustedReadbackMetadata{DelegationID: handoff.DelegationID, Revision: handoff.Revision, Sender: "shiro"}
	for _, status := range []string{"accepted", "needs_clarification", "rejected"} {
		t.Run(status, func(t *testing.T) {
			if _, err := ParseAgentReadback(contractTestReadbackJSON(status), handoff, trusted); err != nil {
				t.Fatalf("ParseAgentReadback(%q) error = %v", status, err)
			}
		})
	}
}

func TestValidateHandoffEnvelopeRequiresCanonicalIDsAndBoundedReferences(t *testing.T) {
	valid := contractTestHandoff()
	if err := ValidateHandoffEnvelope(valid); err != nil {
		t.Fatalf("ValidateHandoffEnvelope(valid) error = %v", err)
	}
	tests := []struct {
		name string
		edit func(*HandoffEnvelope)
	}{
		{name: "delegation ID has wrong canonical type", edit: func(v *HandoffEnvelope) { v.DelegationID = core.RequestID(core.NewMessageID()) }},
		{name: "zero revision", edit: func(v *HandoffEnvelope) { v.Revision = 0 }},
		{name: "invalid trace ID", edit: func(v *HandoffEnvelope) { v.SourceTraceID = core.TraceID(core.NewTurnID()) }},
		{name: "invalid snapshot hash", edit: func(v *HandoffEnvelope) { v.ContextSnapshotHash = "not-a-sha256" }},
		{name: "duplicate constraint key", edit: func(v *HandoffEnvelope) { v.Constraints = append(v.Constraints, v.Constraints[0]) }},
		{name: "too many capabilities", edit: func(v *HandoffEnvelope) {
			v.AllowedCapabilities = make([]string, MaxHandoffCapabilities+1)
			for i := range v.AllowedCapabilities {
				v.AllowedCapabilities[i] = fmt.Sprintf("capability.%d", i)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value := valid
			tt.edit(&value)
			if err := ValidateHandoffEnvelope(value); err == nil {
				t.Fatal("ValidateHandoffEnvelope() error = nil, want rejection")
			}
		})
	}
}

func TestValidateReportEnvelopeRejectsStaleRevisionAndUnboundedReferences(t *testing.T) {
	handoff := contractTestHandoff()
	report := ReportEnvelope{
		ReportID:           core.NewResponseID(),
		ReportRevision:     1,
		DelegationID:       handoff.DelegationID,
		DelegationRevision: handoff.Revision,
		WorkTaskID:         core.NewTaskID(),
		WorkRunID:          core.NewRunID(),
		Status:             ReportStatusSucceeded,
		Summary:            "Measured the save path",
		Result:             "The durable write is the main wait.",
		EvidenceRefs:       []core.EvidenceID{core.NewEvidenceID()},
		ArtifactRefs:       []core.ArtifactID{core.NewArtifactID()},
		Limitations:        []string{"One local run"},
		Sender:             "shiro",
	}
	if err := ValidateReportEnvelope(report, handoff); err != nil {
		t.Fatalf("ValidateReportEnvelope(valid) error = %v", err)
	}
	stale := report
	stale.DelegationRevision++
	if err := ValidateReportEnvelope(stale, handoff); err == nil {
		t.Fatal("ValidateReportEnvelope(stale) error = nil, want rejection")
	}
	tooMany := report
	tooMany.EvidenceRefs = make([]core.EvidenceID, MaxReportReferences+1)
	for i := range tooMany.EvidenceRefs {
		tooMany.EvidenceRefs[i] = core.NewEvidenceID()
	}
	if err := ValidateReportEnvelope(tooMany, handoff); err == nil {
		t.Fatal("ValidateReportEnvelope(unbounded references) error = nil, want rejection")
	}
	invalidID := report
	invalidID.WorkRunID = core.RunID(core.NewTaskID())
	if err := ValidateReportEnvelope(invalidID, handoff); err == nil {
		t.Fatal("ValidateReportEnvelope(wrong ID type) error = nil, want rejection")
	}
	invalidStatus := report
	invalidStatus.Status = ReportStatus("complete")
	if err := ValidateReportEnvelope(invalidStatus, handoff); err == nil {
		t.Fatal("ValidateReportEnvelope(invalid status) error = nil, want rejection")
	}
}
