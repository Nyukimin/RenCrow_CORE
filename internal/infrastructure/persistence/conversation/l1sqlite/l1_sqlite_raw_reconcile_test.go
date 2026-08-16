package l1sqlite

import (
	"os"
	"path/filepath"
	"testing"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

func TestSetCommonRawSourceRootQuarantinesUnreferencedObjectAfterReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "conversation-l1.db")
	root := filepath.Join(t.TempDir(), "raw-sources")
	content := []byte("orphaned raw object")
	hash := domainmemory.SHA256Hex(content)
	ref := objectObjectRef(hash)
	objectPath := filepath.Join(root, filepath.FromSlash(ref))
	if err := os.MkdirAll(filepath.Dir(objectPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(objectPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SetCommonRawSourceRoot(root); err != nil {
		t.Fatalf("SetCommonRawSourceRoot: %v", err)
	}
	if _, err := os.Lstat(objectPath); !os.IsNotExist(err) {
		t.Fatalf("orphan remains at canonical path, err=%v", err)
	}
	orphanPath := filepath.Join(root, ".orphaned", filepath.FromSlash(ref))
	got, err := os.ReadFile(orphanPath)
	if err != nil {
		t.Fatalf("quarantined orphan: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("quarantined content=%q want=%q", got, content)
	}
}

func TestSetCommonRawSourceRootRejectsMissingAndTamperedReferencedObject(t *testing.T) {
	for _, test := range []struct {
		name   string
		tamper func(string, []byte) error
	}{
		{name: "missing", tamper: func(path string, _ []byte) error { return os.Remove(path) }},
		{name: "tampered", tamper: func(path string, content []byte) error {
			return os.WriteFile(path, bytesOfSize(len(content), 't'), 0o600)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dbPath, root, objectPath, content := commonRawObjectFixture(t)
			if err := test.tamper(objectPath, content); err != nil {
				t.Fatal(err)
			}
			store, err := NewL1SQLiteStore(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.SetCommonRawSourceRoot(root); err == nil {
				t.Fatal("SetCommonRawSourceRoot accepted a missing/tampered referenced object")
			}
		})
	}
}

func TestSetCommonRawSourceRootRejectsUnsafeObjectTree(t *testing.T) {
	if !runtimeGOOSNotWindows() {
		t.Skip("unsafe link checks are Unix-specific")
	}
	t.Run("symlink", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "raw-sources")
		content := []byte("outside")
		hash := domainmemory.SHA256Hex(content)
		objectDir := filepath.Join(root, "objects", "sha256", hash[:2])
		if err := os.MkdirAll(objectDir, 0o700); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "outside")
		if err := os.WriteFile(outside, content, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(objectDir, hash)); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		if err := store.SetCommonRawSourceRoot(root); err == nil {
			t.Fatal("SetCommonRawSourceRoot accepted an unsafe symlink")
		}
	})
	t.Run("hardlink", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "raw-sources")
		content := []byte("hardlink")
		hash := domainmemory.SHA256Hex(content)
		objectDir := filepath.Join(root, "objects", "sha256", hash[:2])
		if err := os.MkdirAll(objectDir, 0o700); err != nil {
			t.Fatal(err)
		}
		objectPath := filepath.Join(objectDir, hash)
		if err := os.WriteFile(objectPath, content, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(objectPath, filepath.Join(objectDir, "alias")); err != nil {
			t.Skipf("hardlink unavailable: %v", err)
		}
		store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		if err := store.SetCommonRawSourceRoot(root); err == nil {
			t.Fatal("SetCommonRawSourceRoot accepted an unsafe hardlink")
		}
	})
}

func TestSetCommonRawSourceRootPreservesValidReferencedObject(t *testing.T) {
	dbPath, root, objectPath, _ := commonRawObjectFixture(t)
	store, err := NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SetCommonRawSourceRoot(root); err != nil {
		t.Fatalf("valid referenced root rejected: %v", err)
	}
	if _, err := os.Lstat(objectPath); err != nil {
		t.Fatalf("valid referenced object changed: %v", err)
	}
}

func TestSetCommonRawSourceRootAcceptsEmptyMissingRootWithoutCreating(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	missingRoot := filepath.Join(t.TempDir(), "not-created")
	if err := store.SetCommonRawSourceRoot(missingRoot); err != nil {
		t.Fatalf("missing root should be accepted without creation: %v", err)
	}
	if _, err := os.Lstat(missingRoot); !os.IsNotExist(err) {
		t.Fatalf("missing root was created, err=%v", err)
	}
}

func TestSetCommonRawSourceRootRejectsMissingRootWithReferencedObject(t *testing.T) {
	dbPath, _, _, _ := commonRawObjectFixture(t)
	store, err := NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	missingRoot := filepath.Join(t.TempDir(), "referenced-root-not-created")
	if err := store.SetCommonRawSourceRoot(missingRoot); domainmemory.CommonRawErrorCodeOf(err) != domainmemory.CommonRawErrorObject {
		t.Fatalf("missing referenced root code=%q err=%v", domainmemory.CommonRawErrorCodeOf(err), err)
	}
	if _, err := os.Lstat(missingRoot); !os.IsNotExist(err) {
		t.Fatalf("missing referenced root was created, err=%v", err)
	}
}

func TestSetCommonRawSourceRootQuarantinesCrashTemporaryObject(t *testing.T) {
	root := filepath.Join(t.TempDir(), "raw-sources")
	hash := domainmemory.SHA256Hex([]byte("temp"))
	tempDir := filepath.Join(root, "objects", "sha256", hash[:2])
	if err := os.MkdirAll(tempDir, 0o700); err != nil {
		t.Fatal(err)
	}
	tempPath := filepath.Join(tempDir, ".common-raw-crashed")
	if err := os.WriteFile(tempPath, []byte("temp"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SetCommonRawSourceRoot(root); err != nil {
		t.Fatalf("SetCommonRawSourceRoot: %v", err)
	}
	if _, err := os.Lstat(tempPath); !os.IsNotExist(err) {
		t.Fatalf("crash temporary remains in canonical tree, err=%v", err)
	}
}

func TestSetCommonRawSourceRootAcceptsEmptyChatGPTUploadStagingNamespace(t *testing.T) {
	root := filepath.Join(t.TempDir(), "raw-sources")
	namespace := filepath.Join(root, ".chatgpt-import-staging")
	if err := os.MkdirAll(namespace, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SetCommonRawSourceRoot(root); err != nil {
		t.Fatalf("empty staging namespace was rejected: %v", err)
	}
}

func TestSetCommonRawSourceRootRejectsNonemptyChatGPTUploadStagingNamespace(t *testing.T) {
	root := filepath.Join(t.TempDir(), "raw-sources")
	namespace := filepath.Join(root, ".chatgpt-import-staging")
	stage := filepath.Join(namespace, "stale-stage")
	if err := os.MkdirAll(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "manifest.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SetCommonRawSourceRoot(root); err == nil {
		t.Fatal("nonempty staging namespace was accepted")
	}
	if _, err := os.Lstat(filepath.Join(stage, "manifest.json")); err != nil {
		t.Fatalf("nonempty staging content was deleted: %v", err)
	}
}

func TestSetCommonRawSourceRootRejectsUnsafeChatGPTUploadStagingNamespace(t *testing.T) {
	root := filepath.Join(t.TempDir(), "raw-sources")
	namespace := filepath.Join(root, ".chatgpt-import-staging")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if runtimeGOOSNotWindows() {
		outside := filepath.Join(t.TempDir(), "outside")
		if err := os.Mkdir(outside, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, namespace); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
	} else if err := os.WriteFile(namespace, []byte("not-a-directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SetCommonRawSourceRoot(root); err == nil {
		t.Fatal("unsafe staging namespace was accepted")
	}
}

func TestSetCommonRawSourceRootRejectsNonPrivateChatGPTUploadStagingNamespace(t *testing.T) {
	if !runtimeGOOSNotWindows() {
		t.Skip("permission assertions are Unix-only")
	}
	root := filepath.Join(t.TempDir(), "raw-sources")
	namespace := filepath.Join(root, ".chatgpt-import-staging")
	if err := os.MkdirAll(namespace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(namespace, 0o750); err != nil {
		t.Fatal(err)
	}
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SetCommonRawSourceRoot(root); err == nil {
		t.Fatal("non-private staging namespace was accepted")
	}
}

func TestWriteCommonRawObjectDoesNotOverwriteExistingTarget(t *testing.T) {
	root := filepath.Join(t.TempDir(), "raw-sources")
	content := []byte("wanted")
	hash := domainmemory.SHA256Hex(content)
	ref := objectObjectRef(hash)
	path := filepath.Join(root, filepath.FromSlash(ref))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	existing := []byte("existing")
	if err := os.WriteFile(path, existing, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := writeCommonRawObject(root, content, hash); err == nil {
		t.Fatal("writeCommonRawObject accepted a conflicting existing target")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(existing) {
		t.Fatalf("existing target overwritten: got=%q want=%q", got, existing)
	}
}

func commonRawObjectFixture(t *testing.T) (dbPath, root, objectPath string, content []byte) {
	t.Helper()
	dbPath = filepath.Join(t.TempDir(), "conversation-l1.db")
	root = filepath.Join(t.TempDir(), "raw-sources")
	store, err := NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetCommonRawSourceRoot(root); err != nil {
		t.Fatal(err)
	}
	content = bytesOfSize(domainmemory.CommonRawMaxInlinePayloadSize+1, 'r')
	input := commonRawTestInput([]domainmemory.CommonRawRecord{{SourceRecordID: "fixture", Sensitivity: domainmemory.CommonRawPrivateSensitivity, Role: "user", ContentType: "application/octet-stream", OccurredAt: commonRawTestTime(), Content: content, ContentSHA256: domainmemory.SHA256Hex(content), Provenance: "fixture", Rights: "owner", License: "private"}}, nil)
	receipt, err := store.IntakeCommonRaw(commonRawTestContext(t, "fixture-request"), "fixture-request", commonRawTestOwner, commonRawTestOwner, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if len(receipt.Records) != 1 {
		t.Fatalf("fixture receipt=%+v", receipt)
	}
	objectPath = filepath.Join(root, filepath.FromSlash(receipt.Records[0].ObjectRef))
	return dbPath, root, objectPath, content
}
