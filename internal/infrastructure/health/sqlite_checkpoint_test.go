package health

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	domainhealth "github.com/Nyukimin/RenCrow_CORE/internal/domain/health"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence"
)

type sqliteCheckpointStatusStub struct {
	status persistence.SQLiteWALCheckpointStatus
}

func (s sqliteCheckpointStatusStub) CheckpointStatus() persistence.SQLiteWALCheckpointStatus {
	return s.status
}

func TestSQLiteCheckpointCheckReportsCompleteCheckpoint(t *testing.T) {
	lastSuccess := time.Now().UTC().Add(-15 * time.Second).Truncate(time.Microsecond)
	check := NewSQLiteCheckpointCheck("event_store_checkpoint", sqliteCheckpointStatusStub{
		status: persistence.SQLiteWALCheckpointStatus{
			OpenedAt: lastSuccess.Add(-time.Minute), CheckpointAttempted: true,
			JournalMode: "wal", Synchronous: 1, WALAutoCheckpoint: 0,
			Busy: 0, LogFrames: 12, CheckpointedFrames: 12, Duration: 14 * time.Millisecond,
			LastSuccessAt: lastSuccess, LastErrorClass: persistence.SQLiteWALCheckpointErrorNone,
		},
	})

	result := check.Run(context.Background())
	if result.Name != "event_store_checkpoint" || result.Status != domainhealth.StatusOK {
		t.Fatalf("result = %+v, want an ok checkpoint check", result)
	}
	if result.Details["last_checkpoint_result"] != "complete" || result.Details["target_interval_sec"] != int64(60) {
		t.Fatalf("details = %v", result.Details)
	}
	want := map[string]any{
		"journal_mode": "wal", "synchronous": 1, "wal_autocheckpoint": 0,
		"busy": 0, "log_frames": 12, "checkpointed_frames": 12,
		"checkpoint_duration_ms": int64(14), "last_success_at": lastSuccess.Format(time.RFC3339Nano),
		"error_class": "none",
	}
	for key, value := range want {
		if got, ok := result.Details[key]; !ok || got != value {
			t.Errorf("details[%q] = %#v (present=%v), want %#v", key, got, ok, value)
		}
	}
	if age, ok := result.Details["last_success_age_sec"].(float64); !ok || age < 14 || age > 16 {
		t.Fatalf("last_success_age_sec = %#v, want about 15 seconds", result.Details["last_success_age_sec"])
	}
	if !check.ReadinessOptional() {
		t.Fatal("checkpoint check must be optional for readiness")
	}
}

func TestSQLiteCheckpointCheckDegradesForPartialAndError(t *testing.T) {
	cases := []struct {
		name       string
		class      string
		wantResult string
	}{
		{name: "partial", class: "partial", wantResult: "partial"},
		{name: "busy", class: "busy", wantResult: "partial"},
		{name: "sqlite error", class: "sqlite", wantResult: "error"},
		{name: "context error", class: "context", wantResult: "error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			check := NewSQLiteCheckpointCheck("persona_architecture_checkpoint", sqliteCheckpointStatusStub{
				status: persistence.SQLiteWALCheckpointStatus{
					OpenedAt: time.Now().Add(-time.Minute), CheckpointAttempted: true,
					LastSuccessAt: time.Now().Add(-time.Second), LastErrorClass: persistence.SQLiteWALCheckpointErrorClass(tc.class),
				},
			})
			result := check.Run(context.Background())
			if result.Status != domainhealth.StatusDegraded || result.Details["last_checkpoint_result"] != tc.wantResult {
				t.Fatalf("result = %+v, want degraded result=%q", result, tc.wantResult)
			}
		})
	}
}

func TestSQLiteCheckpointCheckDegradesAfterTargetInterval(t *testing.T) {
	check := NewSQLiteCheckpointCheck("ai_workflow_checkpoint", sqliteCheckpointStatusStub{
		status: persistence.SQLiteWALCheckpointStatus{
			OpenedAt: time.Now().Add(-2 * time.Minute), CheckpointAttempted: true,
			LastSuccessAt: time.Now().Add(-time.Minute - time.Second), LastErrorClass: "none",
		},
	})

	result := check.Run(context.Background())
	if result.Status != domainhealth.StatusDegraded || result.Details["last_checkpoint_result"] != "complete" {
		t.Fatalf("result = %+v, want degraded stale complete checkpoint", result)
	}
}

func TestSQLiteCheckpointCheckAllowsInitialGraceFromOpen(t *testing.T) {
	check := NewSQLiteCheckpointCheck("event_store_checkpoint", sqliteCheckpointStatusStub{
		status: persistence.SQLiteWALCheckpointStatus{
			OpenedAt: time.Now().Add(-5 * time.Second), LastErrorClass: "none",
		},
	})

	result := check.Run(context.Background())
	if result.Status != domainhealth.StatusOK || result.Details["last_checkpoint_result"] != "pending" {
		t.Fatalf("result = %+v, want pending initial grace with ok status", result)
	}
	if age, exists := result.Details["last_success_age_sec"]; !exists || age != nil {
		t.Fatalf("last_success_age_sec = %#v, want null during initial grace", age)
	}
}

func TestSQLiteCheckpointCheckDegradesWhenInitialGraceExpires(t *testing.T) {
	check := NewSQLiteCheckpointCheck("event_store_checkpoint", sqliteCheckpointStatusStub{
		status: persistence.SQLiteWALCheckpointStatus{
			OpenedAt: time.Now().Add(-time.Minute - time.Second), LastErrorClass: "none",
		},
	})

	result := check.Run(context.Background())
	if result.Status != domainhealth.StatusDegraded || result.Details["last_checkpoint_result"] != "pending" {
		t.Fatalf("result = %+v, want degraded pending checkpoint after initial grace", result)
	}
}

func TestSQLiteCheckpointCheckExposesOnlyBoundedCheckpointDetails(t *testing.T) {
	lastSuccess := time.Date(2026, 10, 23, 15, 40, 0, 123000000, time.UTC)
	result := NewSQLiteCheckpointCheck("event_store_checkpoint", sqliteCheckpointStatusStub{
		status: persistence.SQLiteWALCheckpointStatus{
			OpenedAt: time.Now().Add(-time.Second), CheckpointAttempted: true,
			LastErrorClass: persistence.SQLiteWALCheckpointErrorSQLite,
			JournalMode:    "file:///private/checkpoint-store-secret", Synchronous: 1, WALAutoCheckpoint: 0,
			Busy: 2, LogFrames: 8, CheckpointedFrames: 5, Duration: 1250 * time.Microsecond,
			LastSuccessAt: lastSuccess,
		},
	}).Run(context.Background())
	if len(result.Details) != 12 {
		t.Fatalf("details = %v, want the bounded checkpoint summary and operational metrics", result.Details)
	}
	want := map[string]any{
		"last_checkpoint_result": "error", "target_interval_sec": int64(60),
		"journal_mode": "unknown", "synchronous": 1, "wal_autocheckpoint": 0,
		"busy": 2, "log_frames": 8, "checkpointed_frames": 5,
		"checkpoint_duration_ms": int64(1), "last_success_at": lastSuccess.Format(time.RFC3339Nano),
		"error_class": "sqlite",
	}
	for key, value := range want {
		if got, ok := result.Details[key]; !ok || got != value {
			t.Errorf("details[%q] = %#v (present=%v), want %#v", key, got, ok, value)
		}
	}
	for _, required := range []string{"last_success_age_sec"} {
		if _, ok := result.Details[required]; !ok {
			t.Errorf("details is missing %q: %v", required, result.Details)
		}
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"file:///private/checkpoint-store-secret", "raw sqlite failure", "/private/secret-path", "token"} {
		if strings.Contains(string(encoded), secret) {
			t.Errorf("health detail disclosed %q: %s", secret, encoded)
		}
	}

	unknownClass := NewSQLiteCheckpointCheck("event_store_checkpoint", sqliteCheckpointStatusStub{
		status: persistence.SQLiteWALCheckpointStatus{
			OpenedAt: time.Now().Add(-time.Second), CheckpointAttempted: true,
			LastErrorClass: persistence.SQLiteWALCheckpointErrorClass("raw sqlite failure at /private/secret-path token"),
		},
	}).Run(context.Background())
	if unknownClass.Details["error_class"] != "unknown" {
		t.Fatalf("error_class = %#v, want bounded unknown", unknownClass.Details["error_class"])
	}
	unknownEncoded, err := json.Marshal(unknownClass)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(unknownEncoded), "raw sqlite failure at /private/secret-path token") {
		t.Fatalf("health detail disclosed an unrecognized error class: %s", unknownEncoded)
	}

	for _, forbidden := range []string{"path", "raw_error"} {
		if _, exists := result.Details[forbidden]; exists {
			t.Errorf("details contains internal field %q: %v", forbidden, result.Details)
		}
	}
}
