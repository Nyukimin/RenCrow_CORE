package policybundle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domainpolicy "github.com/Nyukimin/RenCrow_CORE/internal/domain/policybundle"
)

func TestStorePreservesLastKnownGoodAfterInvalidReload(t *testing.T) {
	workspace := t.TempDir()
	root := writeValidBundle(t, workspace, nil)
	store := NewStore(workspace)
	first := store.Status()
	if first.State != domainpolicy.StateActive || first.BundleRevision != "2026-08-06.1" {
		t.Fatalf("initial status=%+v", first)
	}
	if err := os.WriteFile(filepath.Join(root, "global.yaml"), []byte("tampered\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := store.Reload()
	if err == nil {
		t.Fatal("Reload accepted invalid bundle")
	}
	status := store.Status()
	if status.State != domainpolicy.StateActive || status.BundleRevision != first.BundleRevision {
		t.Fatalf("active revision was not preserved: %+v", status)
	}
	if status.LastReloadState != domainpolicy.StateInvalid || !status.ActiveRevisionPreserved {
		t.Fatalf("reload failure not projected: %+v", status)
	}
	if !strings.Contains(status.LastReloadError, "hash mismatch") {
		t.Fatalf("last reload error=%q", status.LastReloadError)
	}
	snapshot, ok := store.Snapshot()
	if !ok || snapshot.BundleRevision != first.BundleRevision {
		t.Fatalf("snapshot=%+v ok=%v", snapshot, ok)
	}
}

func TestStoreAtomicallyReplacesValidRevision(t *testing.T) {
	workspace := t.TempDir()
	root := writeValidBundle(t, workspace, nil)
	store := NewStore(workspace)
	manifestPath := filepath.Join(root, "manifest.yaml")
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(manifest), "revision: 2026-08-06.1", "revision: 2026-08-06.2", 1)
	if err := os.WriteFile(manifestPath, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := store.Reload(); err != nil {
		t.Fatalf("Reload error=%v", err)
	}
	status := store.Status()
	if status.BundleRevision != "2026-08-06.2" || status.LastReloadState != domainpolicy.StateActive {
		t.Fatalf("status=%+v", status)
	}
	if status.ActiveRevisionPreserved || status.LastReloadError != "" {
		t.Fatalf("successful reload retained failure: %+v", status)
	}
}

func TestStoreCanRecoverFromMissingInitialBundle(t *testing.T) {
	workspace := t.TempDir()
	store := NewStoreWithClock(workspace, func() time.Time {
		return time.Date(2026, 8, 6, 1, 2, 3, 0, time.UTC)
	})
	if status := store.Status(); status.State != domainpolicy.StateMissing {
		t.Fatalf("initial status=%+v", status)
	}
	writeValidBundle(t, workspace, nil)
	if err := store.Reload(); err != nil {
		t.Fatalf("Reload error=%v", err)
	}
	status := store.Status()
	if status.State != domainpolicy.StateActive || status.LastSuccessfulLoadAt != "2026-08-06T01:02:03Z" {
		t.Fatalf("recovered status=%+v", status)
	}
}

func TestSnapshotReturnsIndependentMaps(t *testing.T) {
	workspace := t.TempDir()
	writeValidBundle(t, workspace, nil)
	store := NewStore(workspace)
	first, ok := store.Snapshot()
	if !ok {
		t.Fatal("snapshot unavailable")
	}
	first.Capabilities["financial_order"] = true
	second, _ := store.Snapshot()
	if second.Capabilities["financial_order"] {
		t.Fatal("Snapshot exposed mutable active map")
	}
}
