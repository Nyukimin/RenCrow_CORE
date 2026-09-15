package task

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

const (
	taskIDPrefix                 = "tsk_"
	taskIDMigrationNamespaceText = "6570d821-e63e-592d-a51f-8cf4b43cdba5"
	taskIDMigrationSourceTable   = "session_history"
	taskIDMigrationSourceField   = "job_id"
)

var taskIDMigrationNamespace = uuid.MustParse(taskIDMigrationNamespaceText)

// TaskID identifies one executable Task. Runtime IDs use UUIDv7; UUIDv5 is
// reserved for deterministic migration of an explicitly named legacy field.
type TaskID string

// NewTaskID generates the canonical runtime TaskID.
func NewTaskID() TaskID {
	id, err := uuid.NewV7()
	if err != nil {
		// A runtime Task cannot be created without its identity. Do not silently
		// fall back to a UUID version with different ordering semantics.
		panic(fmt.Sprintf("generate canonical TaskID: %v", err))
	}
	return TaskID(taskIDPrefix + id.String())
}

// MigrateLegacySessionTaskID deterministically maps the persisted session
// history job_id field to a canonical TaskID. It is for one-time read
// migration only; new runtime IDs must come from NewTaskID.
func MigrateLegacySessionTaskID(legacy string) (TaskID, error) {
	if legacy == "" {
		return "", fmt.Errorf("legacy session job_id cannot be empty")
	}
	if strings.TrimSpace(legacy) != legacy {
		return "", fmt.Errorf("legacy session job_id cannot contain surrounding whitespace")
	}
	if strings.ContainsRune(legacy, '\x00') {
		return "", fmt.Errorf("legacy session job_id cannot contain NUL")
	}
	name := "TaskID\x00" + taskIDMigrationSourceTable + "\x00" + taskIDMigrationSourceField + "\x00" + legacy
	return TaskID(taskIDPrefix + uuid.NewSHA1(taskIDMigrationNamespace, []byte(name)).String()), nil
}

// ParseTaskID parses and validates a canonical TaskID.
func ParseTaskID(raw string) (TaskID, error) {
	id := TaskID(raw)
	if err := id.Validate(); err != nil {
		return "", err
	}
	return id, nil
}

// Validate checks the wire form and UUID version of a TaskID.
func (id TaskID) Validate() error {
	raw := string(id)
	if raw == "" {
		return fmt.Errorf("TaskID cannot be empty")
	}
	if strings.TrimSpace(raw) != raw {
		return fmt.Errorf("TaskID cannot contain surrounding whitespace")
	}
	if !strings.HasPrefix(raw, taskIDPrefix) {
		return fmt.Errorf("TaskID %q must use prefix %q", raw, taskIDPrefix)
	}
	uuidText := strings.TrimPrefix(raw, taskIDPrefix)
	parsed, err := uuid.Parse(uuidText)
	if err != nil || parsed.String() != uuidText {
		if err == nil {
			err = fmt.Errorf("UUID is not in canonical form")
		}
		return fmt.Errorf("invalid TaskID %q: %w", raw, err)
	}
	if parsed.Version() != 5 && parsed.Version() != 7 {
		return fmt.Errorf("TaskID %q uses UUIDv%d, want UUIDv5 or UUIDv7", raw, parsed.Version())
	}
	if parsed.Variant() != uuid.RFC4122 {
		return fmt.Errorf("TaskID %q uses UUID variant %v, want RFC4122", raw, parsed.Variant())
	}
	return nil
}

// String returns the canonical wire representation.
func (id TaskID) String() string { return string(id) }

// Equals reports whether two TaskIDs identify the same Task.
func (id TaskID) Equals(other TaskID) bool { return id == other }

// IsZero reports whether no TaskID is present.
func (id TaskID) IsZero() bool { return id == "" }
