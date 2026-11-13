//go:build darwin

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResilienceStackMaintenanceReaderRequiresExistingSafeMarker(t *testing.T) {
	root := t.TempDir()
	owner := os.Geteuid()
	marker := filepath.Join(root, "maintenance")

	active, err := resilienceStackMaintenanceActiveAt(marker, owner)
	if err != nil || active {
		t.Fatalf("missing marker state=(%t, %v), want inactive without error", active, err)
	}
	if err := validateResilienceStackProtectedFile(marker, owner, true); err != nil {
		t.Fatalf("allowMissing validator rejected a safe missing leaf: %v", err)
	}
	if removed, err := removeResilienceStackMaintenanceMarkerAt(marker, owner); err != nil || removed {
		t.Fatalf("leave on a missing marker should be idempotent: %v", err)
	}

	if err := os.WriteFile(marker, []byte("rencrow-launchd-stack-maintenance/v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	active, err = resilienceStackMaintenanceActiveAt(marker, owner)
	if err != nil || !active {
		t.Fatalf("regular marker state=(%t, %v), want active without error", active, err)
	}
	if removed, err := removeResilienceStackMaintenanceMarkerAt(marker, owner); err != nil || !removed {
		t.Fatalf("leave with a regular marker: %v", err)
	}
	if removed, err := removeResilienceStackMaintenanceMarkerAt(marker, owner); err != nil || removed {
		t.Fatalf("repeated leave should be idempotent: %v", err)
	}

	target := filepath.Join(root, "marker-target")
	if err := os.WriteFile(target, []byte("rencrow-launchd-stack-maintenance/v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, marker); err != nil {
		t.Fatal(err)
	}
	if active, err := resilienceStackMaintenanceActiveAt(marker, owner); err == nil || active {
		t.Fatalf("symlink marker state=(%t, %v), want unsafe-file error", active, err)
	}
	if _, err := removeResilienceStackMaintenanceMarkerAt(marker, owner); err == nil {
		t.Fatal("leave accepted a symlink marker")
	}
	if info, err := os.Lstat(marker); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("unsafe marker was removed: info=%v err=%v", info, err)
	}

	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("unsafe permissions"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(marker, 0o660); err != nil {
		t.Fatal(err)
	}
	if active, err := resilienceStackMaintenanceActiveAt(marker, owner); err == nil || active {
		t.Fatalf("writable marker state=(%t, %v), want unsafe-file error", active, err)
	}
	if _, err := removeResilienceStackMaintenanceMarkerAt(marker, owner); err == nil {
		t.Fatal("leave accepted a marker writable by group")
	}

	parentLink := filepath.Join(root, "parent-link")
	if err := os.Symlink(root, parentLink); err != nil {
		t.Fatal(err)
	}
	missingUnderUnsafeParent := filepath.Join(parentLink, "not-created")
	if active, err := resilienceStackMaintenanceActiveAt(missingUnderUnsafeParent, owner); err == nil || active {
		t.Fatalf("missing marker below a symlink parent state=(%t, %v), want unsafe-parent error", active, err)
	}
	if _, err := removeResilienceStackMaintenanceMarkerAt(missingUnderUnsafeParent, owner); err == nil {
		t.Fatal("leave treated an unsafe parent as an absent marker")
	}
	if err := validateResilienceStackProtectedFile(missingUnderUnsafeParent, owner, true); err == nil {
		t.Fatal("allowMissing validator accepted a missing leaf below a symlink parent")
	}
	missingParentMarker := filepath.Join(root, "missing-parent", "maintenance")
	if active, err := resilienceStackMaintenanceActiveAt(missingParentMarker, owner); err == nil || active {
		t.Fatalf("missing parent marker state=(%t, %v), want parent inspection error", active, err)
	}
	if _, err := removeResilienceStackMaintenanceMarkerAt(missingParentMarker, owner); err == nil {
		t.Fatal("leave treated a missing parent directory as an absent marker")
	}
}

func TestResilienceStackStateReaderReturnsZeroForMissingSafeState(t *testing.T) {
	root := t.TempDir()
	owner := os.Geteuid()
	statePath := filepath.Join(root, "state.json")

	state, err := readResilienceStackStateAt(statePath, owner)
	if err != nil || state != (resilienceStackState{}) {
		t.Fatalf("missing state=(%+v, %v), want zero state without error", state, err)
	}

	if err := os.WriteFile(statePath, []byte(`{"core_pid":42}`), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err = readResilienceStackStateAt(statePath, owner)
	if err != nil || state.CorePID != 42 {
		t.Fatalf("regular state=(%+v, %v), want CorePID 42", state, err)
	}

	linkPath := filepath.Join(root, "state-link.json")
	if err := os.Symlink(statePath, linkPath); err != nil {
		t.Fatal(err)
	}
	if _, err := readResilienceStackStateAt(linkPath, owner); err == nil {
		t.Fatal("state reader accepted a symlink")
	}
	parentLink := filepath.Join(root, "parent-link")
	if err := os.Symlink(root, parentLink); err != nil {
		t.Fatal(err)
	}
	if _, err := readResilienceStackStateAt(filepath.Join(parentLink, "missing.json"), owner); err == nil {
		t.Fatal("state reader treated an unsafe parent as a missing state file")
	}
	if _, err := readResilienceStackStateAt(filepath.Join(root, "missing-parent", "missing.json"), owner); err == nil {
		t.Fatal("state reader treated a missing parent as an absent state file")
	}

	unsafePath := filepath.Join(root, "unsafe-state.json")
	if err := os.WriteFile(unsafePath, []byte(`{"core_pid":42}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unsafePath, 0o660); err != nil {
		t.Fatal(err)
	}
	if _, err := readResilienceStackStateAt(unsafePath, owner); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state reader did not reject unsafe permissions: %v", err)
	}
}
