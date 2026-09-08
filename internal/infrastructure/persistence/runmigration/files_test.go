package runmigration

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSnapshotReadsExactFilesAndPreservesSource(t *testing.T) {
	root := migrationFilesTestDir(t)
	files := map[string][]byte{
		"tasks/task_state.jsonl":      []byte("{\"task_id\":\"task-1\"}\n"),
		"events/event_envelope.jsonl": []byte("event-1\n"),
	}
	for path, data := range files {
		writeMigrationTestFile(t, root, path, data)
	}
	want := snapshotManifest(files)
	got, err := readSnapshotFiles(root, want)
	if err != nil {
		t.Fatalf("readSnapshotFiles() error = %v", err)
	}
	if !sameFileBytes(got, files) {
		t.Fatalf("readSnapshotFiles() = %q, want %q", got, files)
	}
	for path, data := range files {
		actual, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Fatalf("source read %s: %v", path, err)
		}
		if !bytes.Equal(actual, data) {
			t.Fatalf("source file %s changed", path)
		}
	}
}

func TestSnapshotRejectsManifestTreeAndSQLiteViolations(t *testing.T) {
	root := migrationFilesTestDir(t)
	data := []byte("source\n")
	writeMigrationTestFile(t, root, "data.jsonl", data)
	valid := snapshotManifest(map[string][]byte{"data.jsonl": data})

	cases := []struct {
		name     string
		manifest map[string]string
		prepare  func()
	}{
		{name: "empty manifest", manifest: map[string]string{}},
		{name: "invalid hash", manifest: map[string]string{"data.jsonl": strings.Repeat("z", sha256.Size*2)}},
		{name: "uppercase hash", manifest: map[string]string{"data.jsonl": strings.ToUpper(valid["data.jsonl"])}},
		{name: "absolute path", manifest: map[string]string{"/data.jsonl": valid["data.jsonl"]}},
		{name: "parent path", manifest: map[string]string{"../data.jsonl": valid["data.jsonl"]}},
		{name: "unexpected regular file", manifest: valid, prepare: func() {
			writeMigrationTestFile(t, root, "extra.jsonl", []byte("unexpected"))
		}},
		{name: "empty sqlite sidecar", manifest: valid, prepare: func() {
			writeMigrationTestFile(t, root, "data.jsonl-wal", nil)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.prepare != nil {
				tc.prepare()
				defer os.Remove(filepath.Join(root, "extra.jsonl"))
				defer os.Remove(filepath.Join(root, "data.jsonl-wal"))
			}
			if _, err := readSnapshotFiles(root, tc.manifest); err == nil {
				t.Fatalf("readSnapshotFiles() accepted %s", tc.name)
			}
		})
	}
}

func TestSnapshotRejectsSymlinkHardlinkAndReadDrift(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		root := migrationFilesTestDir(t)
		data := []byte("source\n")
		writeMigrationTestFile(t, root, "data.jsonl", data)
		if err := os.Symlink(filepath.Join(root, "data.jsonl"), filepath.Join(root, "alias.jsonl")); err != nil {
			if runtime.GOOS == "windows" {
				t.Skipf("symlink creation unavailable: %v", err)
			}
			t.Fatalf("create symlink: %v", err)
		}
		if _, err := readSnapshotFiles(root, snapshotManifest(map[string][]byte{"data.jsonl": data})); err == nil {
			t.Fatal("readSnapshotFiles() accepted symlink")
		}
	})

	t.Run("hardlink", func(t *testing.T) {
		root := migrationFilesTestDir(t)
		data := []byte("source\n")
		writeMigrationTestFile(t, root, "data.jsonl", data)
		if err := os.Link(filepath.Join(root, "data.jsonl"), filepath.Join(root, "alias.jsonl")); err != nil {
			message := strings.ToLower(err.Error())
			if strings.Contains(message, "not supported") || strings.Contains(message, "not implemented") {
				t.Skipf("hardlink creation unavailable: %v", err)
			}
			t.Fatalf("create hardlink: %v", err)
		}
		manifest := snapshotManifest(map[string][]byte{"data.jsonl": data, "alias.jsonl": data})
		if _, err := readSnapshotFiles(root, manifest); err == nil {
			t.Fatal("readSnapshotFiles() accepted hardlinked files")
		}
	})

	t.Run("link count unavailable", func(t *testing.T) {
		root := migrationFilesTestDir(t)
		path := filepath.Join(root, "data.jsonl")
		writeMigrationTestFile(t, root, "data.jsonl", []byte("source\n"))
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
		if !hasMultipleLinks(path, nil) {
			t.Fatal("link-count lookup with unavailable file identity was accepted")
		}
	})

	t.Run("before read drift", func(t *testing.T) {
		root := migrationFilesTestDir(t)
		data := []byte("source\n")
		writeMigrationTestFile(t, root, "data.jsonl", data)
		old := snapshotBeforeReadHook
		snapshotBeforeReadHook = func(path string) {
			if filepath.Base(path) == "data.jsonl" {
				_ = os.WriteFile(path, []byte("drifted\n"), 0o600)
			}
		}
		t.Cleanup(func() { snapshotBeforeReadHook = old })
		if _, err := readSnapshotFiles(root, snapshotManifest(map[string][]byte{"data.jsonl": data})); err == nil {
			t.Fatal("readSnapshotFiles() accepted before-read drift")
		}
	})

	t.Run("after read drift", func(t *testing.T) {
		root := migrationFilesTestDir(t)
		data := []byte("source\n")
		path := filepath.Join(root, "data.jsonl")
		writeMigrationTestFile(t, root, "data.jsonl", data)
		old := snapshotAfterReadHook
		snapshotAfterReadHook = func(hookedPath string) {
			if hookedPath == path {
				_ = os.WriteFile(path, []byte("drifted\n"), 0o600)
			}
		}
		t.Cleanup(func() { snapshotAfterReadHook = old })
		if _, err := readSnapshotFiles(root, snapshotManifest(map[string][]byte{"data.jsonl": data})); err == nil {
			t.Fatal("readSnapshotFiles() accepted after-read drift")
		}
	})
}

func TestPublishAppliesFreshCohortAndPreservesSource(t *testing.T) {
	root := migrationFilesTestDir(t)
	source := filepath.Join(root, "source")
	writeMigrationTestFile(t, source, "legacy.jsonl", []byte("keep me\n"))
	target := filepath.Join(root, "cohort")
	files := map[string][]byte{
		"tasks/task_state.jsonl": []byte("task\n"),
		"runs/task_run.jsonl":    []byte("run\n"),
	}
	status, err := publishCohort(target, files)
	if err != nil || status != "applied" {
		t.Fatalf("publishCohort() = (%q, %v), want (applied, nil)", status, err)
	}
	got, err := readSnapshotFiles(target, snapshotManifest(files))
	if err != nil {
		t.Fatalf("read published cohort: %v", err)
	}
	if !sameFileBytes(got, files) {
		t.Fatalf("published bytes = %q, want %q", got, files)
	}
	if _, err := os.Stat(filepath.Join(root, ".cohort.rencrow-publish.lock")); !os.IsNotExist(err) {
		t.Fatalf("publish lock remains, stat error = %v", err)
	}
	sourceBytes, err := os.ReadFile(filepath.Join(source, "legacy.jsonl"))
	if err != nil || !bytes.Equal(sourceBytes, []byte("keep me\n")) {
		t.Fatalf("source changed: bytes=%q err=%v", sourceBytes, err)
	}
	if runtime.GOOS != "windows" {
		assertMigrationMode(t, target, 0o700)
		assertMigrationMode(t, filepath.Join(target, "tasks/task_state.jsonl"), 0o600)
	}
}

func TestPublishExactExistingCohortIsNoop(t *testing.T) {
	root := migrationFilesTestDir(t)
	target := filepath.Join(root, "cohort")
	files := map[string][]byte{"tasks/task_state.jsonl": []byte("stable\n")}
	status, err := publishCohort(target, files)
	if err != nil || status != "applied" {
		t.Fatalf("initial publish = (%q, %v)", status, err)
	}
	path := filepath.Join(target, "tasks/task_state.jsonl")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	status, err = publishCohort(target, files)
	if err != nil || status != "noop" {
		t.Fatalf("repeat publish = (%q, %v), want (noop, nil)", status, err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("noop publish rewrote the output")
	}
}

func TestPublishRejectsExistingTamperUnexpectedAndNoClobber(t *testing.T) {
	root := migrationFilesTestDir(t)
	target := filepath.Join(root, "cohort")
	files := map[string][]byte{"tasks/task_state.jsonl": []byte("original\n")}
	if status, err := publishCohort(target, files); err != nil || status != "applied" {
		t.Fatalf("initial publish = (%q, %v)", status, err)
	}
	path := filepath.Join(target, "tasks/task_state.jsonl")
	if err := os.WriteFile(path, []byte("tampered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if status, err := publishCohort(target, files); err == nil || status != "" {
		t.Fatalf("tampered publish = (%q, %v), want error", status, err)
	}
	tampered, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(tampered, []byte("tampered\n")) {
		t.Fatalf("tampered target was overwritten: bytes=%q err=%v", tampered, err)
	}
	if err := os.WriteFile(filepath.Join(target, "unexpected.jsonl"), []byte("extra"), 0o600); err != nil {
		t.Fatal(err)
	}
	if status, err := publishCohort(target, files); err == nil || status != "" {
		t.Fatalf("unexpected-file publish = (%q, %v), want error", status, err)
	}

	emptyTarget := filepath.Join(root, "empty-target")
	if err := os.Mkdir(emptyTarget, 0o700); err != nil {
		t.Fatal(err)
	}
	if status, err := publishCohort(emptyTarget, files); err == nil || status != "" {
		t.Fatalf("existing empty target publish = (%q, %v), want error", status, err)
	}
}

func TestPublishRejectsSymlinkHardlinkAndStaleLock(t *testing.T) {
	files := map[string][]byte{"task.jsonl": []byte("task\n")}

	t.Run("symlink", func(t *testing.T) {
		root := migrationFilesTestDir(t)
		target := filepath.Join(root, "cohort")
		if err := os.Symlink(filepath.Join(root, "elsewhere"), target); err != nil {
			if runtime.GOOS == "windows" {
				t.Skipf("symlink creation unavailable: %v", err)
			}
			t.Fatalf("create target symlink: %v", err)
		}
		if status, err := publishCohort(target, files); err == nil || status != "" {
			t.Fatalf("symlink target publish = (%q, %v), want error", status, err)
		}
	})

	t.Run("hardlink", func(t *testing.T) {
		root := migrationFilesTestDir(t)
		target := filepath.Join(root, "cohort")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(root, "outside.jsonl")
		writeMigrationTestFile(t, root, "outside.jsonl", files["task.jsonl"])
		if err := os.Link(outside, filepath.Join(target, "task.jsonl")); err != nil {
			message := strings.ToLower(err.Error())
			if strings.Contains(message, "not supported") || strings.Contains(message, "not implemented") {
				t.Skipf("hardlink creation unavailable: %v", err)
			}
			t.Fatalf("create target hardlink: %v", err)
		}
		if status, err := publishCohort(target, files); err == nil || status != "" {
			t.Fatalf("hardlink target publish = (%q, %v), want error", status, err)
		}
	})

	t.Run("stale lock", func(t *testing.T) {
		root := migrationFilesTestDir(t)
		target := filepath.Join(root, "cohort")
		lock := filepath.Join(root, ".cohort.rencrow-publish.lock")
		if err := os.WriteFile(lock, []byte("stale"), 0o600); err != nil {
			t.Fatal(err)
		}
		if status, err := publishCohort(target, files); err == nil || status != "" {
			t.Fatalf("stale-lock publish = (%q, %v), want error", status, err)
		}
		if _, err := os.Stat(lock); err != nil {
			t.Fatalf("stale lock was removed: %v", err)
		}
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Fatalf("target exists after stale-lock rejection: %v", err)
		}
	})
}

func TestPublishRejectsInputPathsAndSQLiteSidecars(t *testing.T) {
	root := migrationFilesTestDir(t)
	target := filepath.Join(root, "cohort")
	valid := []byte("valid\n")
	cases := []map[string][]byte{
		{},
		{"/absolute": valid},
		{"../parent": valid},
		{"cohort.sqlite-wal": nil},
		{"a//b": valid},
	}
	for i, files := range cases {
		if status, err := publishCohort(filepath.Join(target, string(rune('a'+i))), files); err == nil || status != "" {
			t.Fatalf("case %d publish = (%q, %v), want error", i, status, err)
		}
	}
}

func snapshotManifest(files map[string][]byte) map[string]string {
	manifest := make(map[string]string, len(files))
	for path, data := range files {
		digest := sha256.Sum256(data)
		manifest[path] = hex.EncodeToString(digest[:])
	}
	return manifest
}

func migrationFilesTestDir(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repo := cwd
	for {
		if _, err := os.Stat(filepath.Join(repo, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(repo)
		if parent == repo {
			t.Fatalf("cannot locate CORE repo from %s", cwd)
		}
		repo = parent
	}
	base := filepath.Join(repo, "Tmp", "test-runtime", "_identity-remediation", "step10-migration-cas", "migration-files")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(base, strings.NewReplacer("/", "-", "\\", "-", " ", "_").Replace(t.Name())+"-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func writeMigrationTestFile(t *testing.T, root, path string, data []byte) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(full, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertMigrationMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("%s mode = %o, want %o", path, info.Mode().Perm(), want)
	}
}
