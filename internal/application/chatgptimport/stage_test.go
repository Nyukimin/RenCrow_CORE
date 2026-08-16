package chatgptimport

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCreateUploadStageCreatesDistinctPrivateStageAndNoReplace(t *testing.T) {
	root := filepath.Join(t.TempDir(), "raw-sources")
	first, err := CreateUploadStage(root, "upload-one")
	if err != nil {
		t.Fatalf("CreateUploadStage(first): %v", err)
	}
	second, err := NewUploadStage(root, "upload-two")
	if err != nil {
		t.Fatalf("NewUploadStage(second): %v", err)
	}
	if first.stageDir == second.stageDir {
		t.Fatal("distinct stage IDs resolved to the same directory")
	}
	if _, err := CreateUploadStage(root, "upload-one"); err == nil {
		t.Fatal("duplicate stage ID was replaced")
	}

	stageRoot, manifestPath, artifactPath, err := first.Paths()
	if err != nil {
		t.Fatalf("first.Paths(): %v", err)
	}
	if stageRoot != first.stageDir || manifestPath != filepath.Join(stageRoot, uploadManifestName) || artifactPath != filepath.Join(stageRoot, uploadArtifactName) {
		t.Fatalf("unexpected stage paths: root=%q manifest=%q artifact=%q", stageRoot, manifestPath, artifactPath)
	}
	manifest, err := first.CreateManifest()
	if err != nil {
		t.Fatalf("CreateManifest(): %v", err)
	}
	if _, err := manifest.Write([]byte(`{"format":"test"}`)); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Close(); err != nil {
		t.Fatal(err)
	}
	artifact, err := first.CreateArtifact()
	if err != nil {
		t.Fatalf("CreateArtifact(): %v", err)
	}
	if _, err := artifact.Write([]byte("artifact")); err != nil {
		t.Fatal(err)
	}
	if err := artifact.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := first.CreateManifest(); err == nil {
		t.Fatal("CreateManifest() replaced an existing manifest")
	}

	if runtime.GOOS != "windows" {
		for name, want := range map[string]os.FileMode{
			root: 0o700,
			filepath.Join(root, uploadStagingDirName): 0o700,
			stageRoot:    0o700,
			manifestPath: 0o600,
			artifactPath: 0o600,
		} {
			info, statErr := os.Lstat(name)
			if statErr != nil {
				t.Fatalf("Lstat(%q): %v", name, statErr)
			}
			if info.Mode().Perm() != want {
				t.Fatalf("mode(%q)=%o, want %o", name, info.Mode().Perm(), want)
			}
		}
	}

	if err := first.Close(); err != nil {
		t.Fatalf("first.Close(): %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("second.Close(): %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("second first.Close(): %v", err)
	}
	if _, err := os.Lstat(stageRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("closed stage remains: %v", err)
	}
}

func TestCreateUploadStageRejectsStageIDPathEscapes(t *testing.T) {
	root := filepath.Join(t.TempDir(), "raw-sources")
	for _, stageID := range []string{
		"", ".", "..", "../escape", `..\escape`, "/absolute", `C:\escape`, "a/b", "a b", "a_b", "a$b", "日本語",
	} {
		t.Run(strings.ReplaceAll(stageID, string(filepath.Separator), "_"), func(t *testing.T) {
			if _, err := CreateUploadStage(root, stageID); err == nil {
				t.Fatalf("stage ID %q was accepted", stageID)
			}
		})
	}
	if _, err := CreateUploadStage("relative-raw", "valid"); err == nil {
		t.Fatal("relative root was accepted")
	}
	if _, err := CreateUploadStage(string(filepath.VolumeName(root))+string(filepath.Separator), "valid"); err == nil && runtime.GOOS != "windows" {
		t.Fatal("volume/root path was accepted")
	}
}

func TestCreateUploadStageRejectsUnsafeRootAndFinalSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("final symlink assertion is Unix-only")
	}
	parent := t.TempDir()
	target := filepath.Join(parent, "outside")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "raw-sources")
	if err := os.Symlink(target, root); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := CreateUploadStage(root, "valid"); err == nil {
		t.Fatal("symlink root was accepted")
	}
}

func TestCreateUploadStageCreatesOnlyFinalMissingRootComponent(t *testing.T) {
	missingParent := filepath.Join(t.TempDir(), "missing-parent")
	root := filepath.Join(missingParent, "raw-sources")
	if _, err := CreateUploadStage(root, "valid"); err == nil {
		t.Fatal("root with a missing ancestor was accepted")
	}
	if _, err := os.Lstat(missingParent); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing ancestor was created: %v", err)
	}
}

func TestCreateUploadStageRejectsUnsafeExistingModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission assertions are Unix-only")
	}
	t.Run("root", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "raw-sources")
		if err := os.Mkdir(root, 0o750); err != nil {
			t.Fatal(err)
		}
		if _, err := CreateUploadStage(root, "valid"); err == nil {
			t.Fatal("non-private existing root was accepted")
		}
	})
	t.Run("namespace", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "raw-sources")
		namespace := filepath.Join(root, uploadStagingDirName)
		if err := os.MkdirAll(namespace, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(namespace, 0o750); err != nil {
			t.Fatal(err)
		}
		if _, err := CreateUploadStage(root, "valid"); err == nil {
			t.Fatal("non-private existing namespace was accepted")
		}
	})
}

func TestCreateUploadStageRejectsUnsafeExistingNamespaceAndStageSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink assertions are Unix-only")
	}
	t.Run("namespace", func(t *testing.T) {
		parent := t.TempDir()
		root := filepath.Join(parent, "raw-sources")
		outside := filepath.Join(parent, "outside")
		if err := os.Mkdir(outside, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, uploadStagingDirName)); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, err := CreateUploadStage(root, "valid"); err == nil {
			t.Fatal("namespace symlink was accepted")
		}
	})
	t.Run("stage directory", func(t *testing.T) {
		parent := t.TempDir()
		root := filepath.Join(parent, "raw-sources")
		outside := filepath.Join(parent, "outside")
		if err := os.Mkdir(outside, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, uploadStagingDirName), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, uploadStagingDirName, "valid")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, err := CreateUploadStage(root, "valid"); err == nil {
			t.Fatal("stage directory symlink was accepted")
		}
	})
}

func TestUploadStagePartialFilesAreRemovedAndDuplicateCreationFails(t *testing.T) {
	root := filepath.Join(t.TempDir(), "raw-sources")
	stage, err := CreateUploadStage(root, "partial")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := stage.CreateManifest()
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := stage.CreateManifest(); err == nil {
		t.Fatal("duplicate manifest creation was accepted")
	}
	opened, err := stage.OpenManifest()
	if err != nil {
		t.Fatalf("OpenManifest(): %v", err)
	}
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}
	if err := stage.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	if _, err := stage.OpenManifest(); err == nil {
		t.Fatal("OpenManifest() succeeded after Close")
	}
}

func TestUploadStageRejectsSymlinkAndHardlinkDuringClose(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("link-count and symlink assertions are Unix-only")
	}
	t.Run("symlink", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "raw-sources")
		stage, err := CreateUploadStage(root, "symlink")
		if err != nil {
			t.Fatal(err)
		}
		_, manifestPath, _, err := stage.Paths()
		if err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "outside")
		if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, manifestPath); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if err := stage.Close(); err == nil {
			t.Fatal("Close() accepted symlink")
		}
		if _, err := os.Lstat(manifestPath); err != nil {
			t.Fatalf("unsafe symlink was removed: %v", err)
		}
	})
	t.Run("hardlink", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "raw-sources")
		stage, err := CreateUploadStage(root, "hardlink")
		if err != nil {
			t.Fatal(err)
		}
		_, _, artifactPath, err := stage.Paths()
		if err != nil {
			t.Fatal(err)
		}
		artifact, err := stage.CreateArtifact()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := artifact.Write([]byte("artifact")); err != nil {
			t.Fatal(err)
		}
		if err := artifact.Close(); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "alias")
		if err := os.Link(artifactPath, outside); err != nil {
			t.Skipf("hardlink unavailable: %v", err)
		}
		if err := stage.Close(); err == nil {
			t.Fatal("Close() accepted hardlink")
		}
		if _, err := os.Lstat(artifactPath); err != nil {
			t.Fatalf("unsafe hardlink was removed: %v", err)
		}
	})
}

func TestReconcileUploadStagesRemovesValidStagesAndIsIdempotent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "raw-sources")
	first, err := CreateUploadStage(root, "stale-one")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := first.CreateManifest()
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateUploadStage(root, "stale-two"); err != nil {
		t.Fatal(err)
	}
	removed, err := ReconcileUploadStages(root)
	if err != nil {
		t.Fatalf("ReconcileUploadStages(): %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed=%d, want 2 stage directories", removed)
	}
	entries, err := os.ReadDir(filepath.Join(root, uploadStagingDirName))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("staging namespace is not empty: %v", entries)
	}
	removed, err = ReconcileUploadStages(root)
	if err != nil || removed != 0 {
		t.Fatalf("second reconcile removed=%d err=%v, want 0/nil", removed, err)
	}
}

func TestReconcileUploadStagesPreflightsAllBeforeDeletion(t *testing.T) {
	root := filepath.Join(t.TempDir(), "raw-sources")
	valid, err := CreateUploadStage(root, "valid-stage")
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = valid.Paths()
	if err != nil {
		t.Fatal(err)
	}
	unsafe, err := CreateUploadStage(root, "unsafe-stage")
	if err != nil {
		t.Fatal(err)
	}
	unsafeRoot, _, _, err := unsafe.Paths()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(unsafeRoot, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcileUploadStages(root); err == nil {
		t.Fatal("unsafe nested entry was accepted")
	}
	if _, err := os.Lstat(valid.stageDir); err != nil {
		t.Fatalf("valid stage was deleted despite unsafe tree: %v", err)
	}
	if _, err := os.Lstat(unsafe.stageDir); err != nil {
		t.Fatalf("unsafe stage was deleted: %v", err)
	}
}

func TestReconcileUploadStagesRejectsUnknownNamespaceEntry(t *testing.T) {
	root := filepath.Join(t.TempDir(), "raw-sources")
	if _, err := CreateUploadStage(root, "valid-stage"); err != nil {
		t.Fatal(err)
	}
	namespace := filepath.Join(root, uploadStagingDirName)
	if err := os.WriteFile(filepath.Join(namespace, "unknown"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcileUploadStages(root); err == nil {
		t.Fatal("unknown namespace entry was accepted")
	}
	if _, err := os.Lstat(filepath.Join(namespace, "unknown")); err != nil {
		t.Fatalf("unknown namespace entry was deleted: %v", err)
	}
}

func TestReconcileUploadStagesMissingRootIsNoop(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-created")
	removed, err := ReconcileUploadStages(root)
	if err != nil || removed != 0 {
		t.Fatalf("missing root reconcile removed=%d err=%v, want 0/nil", removed, err)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing root was created: %v", err)
	}
}
