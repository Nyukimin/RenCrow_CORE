package worldprojection

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func validTarget() Target {
	return Target{
		WorldID:          "world-house",
		TimelineID:       "timeline-main",
		TimelineEpoch:    "epoch-1",
		ActorID:          "mio",
		ActorInstanceID:  "instance-mio",
		ActorTransportID: "mio",
	}
}

func TestNewWorkSetEnvelopeMarshalsExactContract(t *testing.T) {
	validUntil := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	workSet, err := NewWorkSet("world-house", "mio", "core-epoch-1", 7, validUntil, []WorkItem{{
		TaskID:                   "tsk_1",
		ExecutionState:           ExecutionQueued,
		WorkClass:                WorkClassAssignedWork,
		RequiresHousePresence:    false,
		ClassificationReasonCode: "assigned_task",
	}})
	if err != nil {
		t.Fatalf("NewWorkSet() error = %v", err)
	}
	env, err := NewEnvelope(validTarget(), KindWorkSet, workSet)
	if err != nil {
		t.Fatalf("NewEnvelope() error = %v", err)
	}
	raw, err := env.Marshal()
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var wire struct {
		ContractVersion string `json:"contract_version"`
		RecordType      string `json:"record_type"`
		WorldID         string `json:"world_id"`
		TimelineID      string `json:"timeline_id"`
		TimelineEpoch   string `json:"timeline_epoch"`
		ActorInstanceID string `json:"actor_instance_id"`
		ProjectionKind  string `json:"projection_kind"`
		Payload         struct {
			ContractVersion string `json:"contract_version"`
			SourceOwner     string `json:"source_owner"`
			SourceEpoch     string `json:"source_epoch"`
			SourceRevision  uint64 `json:"source_revision"`
			ValidUntil      string `json:"valid_until"`
			ActiveWork      []struct {
				TaskID                string `json:"task_id"`
				ExecutionState        string `json:"execution_state"`
				RequiresHousePresence bool   `json:"requires_house_presence"`
				ClassificationReason  string `json:"classification_reason_code"`
			} `json:"active_work"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("Marshal() produced invalid JSON: %v", err)
	}
	if wire.ContractVersion != ContractVersion || wire.RecordType != RecordType || wire.ProjectionKind != KindWorkSet {
		t.Fatalf("unexpected envelope header: %+v", wire)
	}
	if wire.WorldID != "world-house" || wire.TimelineID != "timeline-main" || wire.TimelineEpoch != "epoch-1" || wire.ActorInstanceID != "instance-mio" {
		t.Fatalf("unexpected destination: %+v", wire)
	}
	if wire.Payload.ContractVersion != WorkSetContract || wire.Payload.SourceOwner != SourceOwnerCORE {
		t.Fatalf("unexpected work set owner: %+v", wire.Payload)
	}
	if wire.Payload.SourceEpoch != "core-epoch-1" || wire.Payload.SourceRevision != 7 {
		t.Fatalf("unexpected source order: %+v", wire.Payload)
	}
	if wire.Payload.ValidUntil != "2026-10-20T12:00:00Z" {
		t.Fatalf("valid_until = %q", wire.Payload.ValidUntil)
	}
	if len(wire.Payload.ActiveWork) != 1 || wire.Payload.ActiveWork[0].TaskID != "tsk_1" {
		t.Fatalf("unexpected active work: %+v", wire.Payload.ActiveWork)
	}
	if !strings.HasSuffix(string(raw), "}") {
		t.Fatalf("Marshal() output is truncated")
	}
}

func TestNewWorkSetRequiresNonNilActiveWorkForFreshFullSet(t *testing.T) {
	if _, err := NewWorkSet("world-house", "mio", "core-epoch-1", 1, time.Now().UTC().Add(time.Hour), nil); err == nil {
		t.Fatal("NewWorkSet() accepted nil active_work")
	}
	workSet, err := NewWorkSet("world-house", "mio", "core-epoch-1", 1, time.Now().UTC().Add(time.Hour), []WorkItem{})
	if err != nil {
		t.Fatalf("NewWorkSet() error = %v", err)
	}
	if workSet.ActiveWork == nil || len(workSet.ActiveWork) != 0 {
		t.Fatalf("active_work = %#v, want non-nil empty slice", workSet.ActiveWork)
	}
}

func TestNewWorkSetRejectsRunningWithoutRunID(t *testing.T) {
	_, err := NewWorkSet("world-house", "mio", "core-epoch-1", 1, time.Now().UTC().Add(time.Hour), []WorkItem{{
		TaskID:                   "tsk_1",
		ExecutionState:           ExecutionRunning,
		WorkClass:                WorkClassAssignedWork,
		RequiresHousePresence:    true,
		ClassificationReasonCode: "assigned_task",
	}})
	if err == nil {
		t.Fatal("NewWorkSet() accepted running work without run_id")
	}
}

func TestNewLifePolicyRejectsFallbackOutsideAllowed(t *testing.T) {
	_, err := NewLifePolicy("world-house", "mio", "core-epoch-1", 1, time.Now().UTC().Add(time.Hour),
		"persona-r1", "condition-r1", []string{"read", "rest"}, []string{"walk"})
	if err == nil {
		t.Fatal("NewLifePolicy() accepted a fallback outside allowed activity ids")
	}
}

func TestNewLifePolicyRejectsDuplicateActivityIDs(t *testing.T) {
	_, err := NewLifePolicy("world-house", "mio", "core-epoch-1", 1, time.Now().UTC().Add(time.Hour),
		"persona-r1", "condition-r1", []string{"read", "read"}, []string{})
	if err == nil {
		t.Fatal("NewLifePolicy() accepted duplicate activity ids")
	}
}

func TestNextSourceRevisionRejectsImplicitEpochChange(t *testing.T) {
	current := SourceRevision{Epoch: "core-epoch-1", Revision: 4}
	next, err := NextSourceRevision(current, "core-epoch-1")
	if err != nil {
		t.Fatalf("NextSourceRevision() error = %v", err)
	}
	if next.Revision != 5 || next.Epoch != current.Epoch {
		t.Fatalf("next = %+v", next)
	}
	if _, err := NextSourceRevision(current, "core-epoch-2"); err == nil {
		t.Fatal("NextSourceRevision() allowed an implicit epoch change")
	}
	reset, err := ResetSourceRevision("core-epoch-2")
	if err != nil {
		t.Fatalf("ResetSourceRevision() error = %v", err)
	}
	if reset.Epoch != "core-epoch-2" || reset.Revision != 1 {
		t.Fatalf("reset = %+v", reset)
	}
}

func TestEnvelopeDigestIsStableForSamePayload(t *testing.T) {
	validUntil := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	workSet, err := NewWorkSet("world-house", "mio", "core-epoch-1", 1, validUntil, []WorkItem{})
	if err != nil {
		t.Fatalf("NewWorkSet() error = %v", err)
	}
	first, err := NewEnvelope(validTarget(), KindWorkSet, workSet)
	if err != nil {
		t.Fatalf("NewEnvelope() error = %v", err)
	}
	second, err := NewEnvelope(validTarget(), KindWorkSet, workSet)
	if err != nil {
		t.Fatalf("NewEnvelope() error = %v", err)
	}
	if first.Digest == "" || first.Digest != second.Digest {
		t.Fatalf("digest = %q / %q", first.Digest, second.Digest)
	}
}

func TestEnvelopeRejectsPayloadDestinationMismatch(t *testing.T) {
	workSet, err := NewWorkSet("other-world", "mio", "core-epoch-1", 1, time.Now().UTC().Add(time.Hour), []WorkItem{})
	if err != nil {
		t.Fatalf("NewWorkSet() error = %v", err)
	}
	if _, err := NewEnvelope(validTarget(), KindWorkSet, workSet); err == nil {
		t.Fatal("NewEnvelope() accepted a payload for another world")
	}
}
