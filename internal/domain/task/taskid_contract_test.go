package task

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestNewTaskIDUsesCanonicalUUIDv7(t *testing.T) {
	first := NewTaskID()
	second := NewTaskID()

	if first == second {
		t.Fatal("NewTaskID returned the same value twice")
	}
	for _, got := range []TaskID{first, second} {
		if !strings.HasPrefix(got.String(), "tsk_") {
			t.Fatalf("TaskID %q does not use the canonical prefix", got)
		}
		parsed, err := uuid.Parse(strings.TrimPrefix(got.String(), "tsk_"))
		if err != nil {
			t.Fatalf("NewTaskID returned invalid UUID: %v", err)
		}
		if parsed.Version() != 7 {
			t.Fatalf("NewTaskID returned UUIDv%d, want UUIDv7", parsed.Version())
		}
		if err := got.Validate(); err != nil {
			t.Fatalf("NewTaskID failed validation: %v", err)
		}
	}
}

func TestParseTaskIDAcceptsOnlyCanonicalUUIDv5OrV7(t *testing.T) {
	validV7 := NewTaskID().String()
	validV5 := "tsk_" + uuid.NewSHA1(uuid.MustParse(taskIDMigrationNamespaceText), []byte("migration-test")).String()

	for _, raw := range []string{validV7, validV5} {
		got, err := ParseTaskID(raw)
		if err != nil {
			t.Errorf("ParseTaskID(%q) failed: %v", raw, err)
		}
		if got.String() != raw {
			t.Errorf("ParseTaskID(%q) returned %q", raw, got)
		}
	}

	for _, raw := range []string{
		"",
		" tsk_0192f2d6-7c31-7b4f-9e4b-9f3e6f6b8c11",
		"tsk_0192f2d6-7c31-4b4f-9e4b-9f3e6f6b8c11",
		"tsk_20260301-120000-abcd1234",
		"job_0192f2d6-7c31-7b4f-9e4b-9f3e6f6b8c11",
	} {
		if _, err := ParseTaskID(raw); err == nil {
			t.Errorf("ParseTaskID(%q) accepted a non-canonical ID", raw)
		}
	}
}

func TestMigrateLegacySessionJobIDIsDeterministicUUIDv5(t *testing.T) {
	legacy := "20260301-120000-abcd1234"
	got, err := MigrateLegacySessionTaskID(legacy)
	if err != nil {
		t.Fatalf("MigrateLegacySessionTaskID failed: %v", err)
	}
	wantUUID := uuid.NewSHA1(
		uuid.MustParse(taskIDMigrationNamespaceText),
		[]byte("TaskID\x00session_history\x00job_id\x00"+legacy),
	)
	want := TaskID("tsk_" + wantUUID.String())
	if got != want {
		t.Fatalf("migration returned %q, want %q", got, want)
	}
	if again, err := MigrateLegacySessionTaskID(legacy); err != nil || again != got {
		t.Fatalf("migration is not deterministic: %q / %v", again, err)
	}
	if parsed, err := uuid.Parse(strings.TrimPrefix(got.String(), "tsk_")); err != nil || parsed.Version() != 5 {
		t.Fatalf("migration returned non-UUIDv5 TaskID %q: %v", got, err)
	}
}
