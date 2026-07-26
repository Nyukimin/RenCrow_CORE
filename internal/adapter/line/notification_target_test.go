package line

import (
	"testing"
)

func TestDirectUserTargetStoreRecordAndLoad(t *testing.T) {
	store := NewDirectUserTargetStore(t.TempDir())
	userID := "U0123456789abcdef0123456789abcdef"

	recorded, err := store.Record(userID)
	if err != nil {
		t.Fatalf("Record failed: %v", err)
	}
	if !recorded {
		t.Fatal("first direct user should be recorded")
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if got != userID {
		t.Fatalf("Load = %q, want %q", got, userID)
	}

	assertTargetFilePermissions(t, store.Path())
}

func TestDirectUserTargetStoreKeepsFirstUser(t *testing.T) {
	store := NewDirectUserTargetStore(t.TempDir())
	first := "U0123456789abcdef0123456789abcdef"
	second := "Ufedcba9876543210fedcba9876543210"

	if recorded, err := store.Record(first); err != nil || !recorded {
		t.Fatalf("first Record = %v, %v", recorded, err)
	}
	if recorded, err := store.Record(second); err != nil || recorded {
		t.Fatalf("second Record = %v, %v; want existing target unchanged", recorded, err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if got != first {
		t.Fatalf("Load = %q, want first target %q", got, first)
	}
}

func TestDirectUserTargetStoreRejectsNonUserTarget(t *testing.T) {
	store := NewDirectUserTargetStore(t.TempDir())
	for _, id := range []string{
		"",
		"C0123456789abcdef0123456789abcdef",
		"R0123456789abcdef0123456789abcdef",
		"${PICOCLAW_HEARTBEAT_CHAT_ID}",
	} {
		if _, err := store.Record(id); err == nil {
			t.Fatalf("Record(%q) should fail", id)
		}
	}
}

func TestTargetKindAndMask(t *testing.T) {
	tests := []struct {
		id       string
		wantKind string
		wantMask string
	}{
		{"U0123456789abcdef0123456789abcdef", "user", "U012************************cdef"},
		{"C0123456789abcdef0123456789abcdef", "group", "C012************************cdef"},
		{"R0123456789abcdef0123456789abcdef", "room", "R012************************cdef"},
	}
	for _, tt := range tests {
		kind, err := TargetKind(tt.id)
		if err != nil {
			t.Fatalf("TargetKind(%q): %v", tt.id, err)
		}
		if kind != tt.wantKind {
			t.Fatalf("TargetKind(%q) = %q, want %q", tt.id, kind, tt.wantKind)
		}
		if got := MaskTargetID(tt.id); got != tt.wantMask {
			t.Fatalf("MaskTargetID(%q) = %q, want %q", tt.id, got, tt.wantMask)
		}
	}
}
