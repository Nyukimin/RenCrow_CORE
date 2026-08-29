package core

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

type canonicalTestID interface {
	~string
	Validate() error
	driver.Valuer
}

func testCanonicalIDContract[T canonicalTestID](t *testing.T, prefix string, newID func() T) {
	t.Helper()
	first, second := newID(), newID()
	if first == second {
		t.Fatalf("IDs must be unique: %q", first)
	}
	if err := first.Validate(); err != nil {
		t.Fatalf("generated ID must validate: %v", err)
	}
	raw := string(first)
	if !strings.HasPrefix(raw, prefix) {
		t.Fatalf("ID %q must use prefix %q", raw, prefix)
	}
	parsed, err := uuid.Parse(strings.TrimPrefix(raw, prefix))
	if err != nil || parsed.Version() != 7 {
		t.Fatalf("ID %q must contain UUIDv7: version=%d err=%v", raw, parsed.Version(), err)
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded T
	if err := json.Unmarshal(encoded, &decoded); err != nil || decoded != first {
		t.Fatalf("JSON round trip = %q, want %q, err=%v", decoded, first, err)
	}
	value, err := first.Value()
	if err != nil {
		t.Fatalf("driver value: %v", err)
	}
	var scanned T
	scanner, ok := any(&scanned).(sql.Scanner)
	if !ok {
		t.Fatalf("%T must implement sql.Scanner", &scanned)
	}
	if err := scanner.Scan(value); err != nil || scanned != first {
		t.Fatalf("SQL round trip = %q, want %q, err=%v", scanned, first, err)
	}
	var zero T
	if err := zero.Validate(); err == nil {
		t.Fatal("zero ID must be rejected")
	}
	if _, err := zero.Value(); err == nil {
		t.Fatal("zero driver value must be rejected")
	}
	for name, invalid := range map[string]T{
		"wrong_prefix": T("bad_" + strings.TrimPrefix(raw, prefix)),
		"malformed":    T(prefix + "not-a-uuid"),
		"non_v7":       T(prefix + uuid.New().String()),
	} {
		t.Run(name, func(t *testing.T) {
			if err := invalid.Validate(); err == nil {
				t.Fatalf("invalid ID %q must be rejected", invalid)
			}
		})
	}
}

func TestCanonicalIDContracts(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{"TraceID", func(t *testing.T) { testCanonicalIDContract(t, "trc_", NewTraceID) }},
		{"EventID", func(t *testing.T) { testCanonicalIDContract(t, "evt_", NewEventID) }},
		{"SessionID", func(t *testing.T) { testCanonicalIDContract(t, "ses_", NewSessionID) }},
		{"ThreadID", func(t *testing.T) { testCanonicalIDContract(t, "thr_", NewThreadID) }},
		{"TurnID", func(t *testing.T) { testCanonicalIDContract(t, "turn_", NewTurnID) }},
		{"MessageID", func(t *testing.T) { testCanonicalIDContract(t, "msg_", NewMessageID) }},
		{"UtteranceID", func(t *testing.T) { testCanonicalIDContract(t, "utt_", NewUtteranceID) }},
		{"WorkstreamID", func(t *testing.T) { testCanonicalIDContract(t, "ws_", NewWorkstreamID) }},
		{"GoalID", func(t *testing.T) { testCanonicalIDContract(t, "gol_", NewGoalID) }},
		{"TaskID", func(t *testing.T) { testCanonicalIDContract(t, "tsk_", NewTaskID) }},
		{"RunID", func(t *testing.T) { testCanonicalIDContract(t, "run_", NewRunID) }},
		{"ActionID", func(t *testing.T) { testCanonicalIDContract(t, "act_", NewActionID) }},
		{"AttemptID", func(t *testing.T) { testCanonicalIDContract(t, "att_", NewAttemptID) }},
		{"RequestID", func(t *testing.T) { testCanonicalIDContract(t, "req_", NewRequestID) }},
		{"ResponseID", func(t *testing.T) { testCanonicalIDContract(t, "rsp_", NewResponseID) }},
		{"ArtifactID", func(t *testing.T) { testCanonicalIDContract(t, "art_", NewArtifactID) }},
		{"EvidenceID", func(t *testing.T) { testCanonicalIDContract(t, "evd_", NewEvidenceID) }},
		{"MemoryID", func(t *testing.T) { testCanonicalIDContract(t, "mem_", NewMemoryID) }},
		{"RelationID", func(t *testing.T) { testCanonicalIDContract(t, "rel_", NewRelationID) }},
		{"ScheduleID", func(t *testing.T) { testCanonicalIDContract(t, "sch_", NewScheduleID) }},
		{"QueueItemID", func(t *testing.T) { testCanonicalIDContract(t, "qit_", NewQueueItemID) }},
		{"CheckpointID", func(t *testing.T) { testCanonicalIDContract(t, "ckp_", NewCheckpointID) }},
		{"ReceiptID", func(t *testing.T) { testCanonicalIDContract(t, "rcp_", NewReceiptID) }},
	}
	for _, test := range tests {
		t.Run(test.name, test.run)
	}
}

func TestCanonicalIDGenerationHasNoDuplicatesAcrossOneMillionIDs(t *testing.T) {
	seen := make(map[TraceID]struct{}, 1_000_000)
	for i := 0; i < 1_000_000; i++ {
		id := NewTraceID()
		if _, exists := seen[id]; exists {
			t.Fatalf("duplicate TraceID after %d generations: %q", i+1, id)
		}
		seen[id] = struct{}{}
	}
}

func TestMigrationIDIsDeterministicAndTypeSeparated(t *testing.T) {
	taskID, err := NewMigrationID(CanonicalTaskID, "legacy_job", "job_id", "same-value")
	if err != nil {
		t.Fatalf("task migration ID: %v", err)
	}
	replayed, err := NewMigrationID(CanonicalTaskID, "legacy_job", "job_id", "same-value")
	if err != nil {
		t.Fatalf("replayed migration ID: %v", err)
	}
	traceID, err := NewMigrationID(CanonicalTraceID, "legacy_job", "job_id", "same-value")
	if err != nil {
		t.Fatalf("trace migration ID: %v", err)
	}
	if taskID != replayed || taskID == traceID {
		t.Fatalf("migration separation failed: task=%q replay=%q trace=%q", taskID, replayed, traceID)
	}
	if taskID != "tsk_091803b5-c485-5ff4-8aed-41d6baf839db" {
		t.Fatalf("migration mapping drifted: %q", taskID)
	}
	if !strings.HasPrefix(taskID, "tsk_") || !strings.HasPrefix(traceID, "trc_") {
		t.Fatalf("migration prefixes are wrong: task=%q trace=%q", taskID, traceID)
	}
	for _, raw := range []string{taskID, traceID} {
		parsed, err := uuid.Parse(raw[strings.IndexByte(raw, '_')+1:])
		if err != nil || parsed.Version() != 5 {
			t.Fatalf("migration ID %q must contain UUIDv5: version=%d err=%v", raw, parsed.Version(), err)
		}
	}
}

func TestMigrationIDRejectsUnknownTypeAndEmptySourcePath(t *testing.T) {
	if _, err := NewMigrationID("unknown", "table", "field", "value"); err == nil {
		t.Fatal("unknown canonical type must be rejected")
	}
	if _, err := NewMigrationID(CanonicalTaskID, "", "field", "value"); err == nil {
		t.Fatal("empty source table must be rejected")
	}
	if _, err := NewMigrationID(CanonicalTaskID, "table", "", "value"); err == nil {
		t.Fatal("empty source field must be rejected")
	}
	if _, err := NewMigrationID(CanonicalTaskID, "table", "field", ""); err == nil {
		t.Fatal("empty source value must be rejected")
	}
}

func TestCanonicalIDGeneratorFailsClosed(t *testing.T) {
	if _, err := newCanonicalID("trc_", func() (uuid.UUID, error) {
		return uuid.Nil, errors.New("entropy unavailable")
	}); err == nil || !strings.Contains(err.Error(), "entropy unavailable") {
		t.Fatalf("entropy failure must be returned without fallback: %v", err)
	}
	if _, err := newCanonicalID("trc_", func() (uuid.UUID, error) {
		return uuid.New(), nil
	}); err == nil {
		t.Fatal("non-v7 generator result must be rejected")
	}
}
