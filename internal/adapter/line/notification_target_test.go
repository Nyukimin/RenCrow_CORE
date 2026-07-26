package line

import (
	"os"
	"path/filepath"
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

	info, err := os.Stat(store.Path())
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("target file mode = %o, want 600", perm)
	}
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

func TestDirectUserTargetStoreRejectsPermissiveExistingFile(t *testing.T) {
	store := NewDirectUserTargetStore(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(store.Path()), 0o700); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	if err := os.WriteFile(store.Path(), []byte("U0123456789abcdef0123456789abcdef\n"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if err := os.Chmod(store.Path(), 0o644); err != nil {
		t.Fatalf("Chmod failed: %v", err)
	}

	if _, err := store.Load(); err == nil {
		t.Fatal("Load should reject a target file readable by group/others")
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
