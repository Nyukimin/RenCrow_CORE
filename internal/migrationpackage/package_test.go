package migrationpackage

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildCreatesDeterministicWorkspaceStatePackage(t *testing.T) {
	source := fixtureSnapshot(t)
	first := filepath.Join(t.TempDir(), "first")
	second := filepath.Join(t.TempDir(), "second")
	mustMkdirPrivate(t, first)
	mustMkdirPrivate(t, second)
	if err := Build(source, first); err != nil {
		t.Fatal(err)
	}
	if err := Build(source, second); err != nil {
		t.Fatal(err)
	}
	left, err := os.ReadFile(filepath.Join(first, DescriptorName))
	if err != nil {
		t.Fatal(err)
	}
	right, err := os.ReadFile(filepath.Join(second, DescriptorName))
	if err != nil {
		t.Fatal(err)
	}
	if string(left) != string(right) {
		t.Fatalf("descriptor is not deterministic:\n%s\n%s", left, right)
	}
	for _, name := range cohortFiles {
		if _, err := os.Stat(filepath.Join(first, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	text := string(left)
	for _, expected := range []string{`"schema_version":"rencrow-state-export/v1"`, `"module_id":"RenCrow_CORE"`, `"consistency":"quiesced"`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("descriptor missing %s: %s", expected, text)
		}
	}
	if strings.Contains(text, source) || strings.Contains(text, first) {
		t.Fatalf("descriptor leaked an absolute path: %s", text)
	}
	var decoded descriptor
	if err := json.Unmarshal(left, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.RecordCount != 0 {
		t.Fatalf("record_count describes domain records, not cohort files: %d", decoded.RecordCount)
	}
	summary, err := Inspect(first)
	if err != nil {
		t.Fatal(err)
	}
	if summary.LogicalID != "core-state-cohort" || summary.SizeBytes <= 0 || len(summary.SHA256) != 64 {
		t.Fatalf("summary=%+v", summary)
	}
	for _, file := range decoded.Files {
		payload, err := os.ReadFile(filepath.Join(first, file.Path))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(payload)
		if fmt.Sprintf("%x", digest) != file.SHA256 {
			t.Fatalf("descriptor digest does not match copied payload %s", file.Path)
		}
	}
}

func TestInspectRejectsPayloadChangedAfterBuild(t *testing.T) {
	source := fixtureSnapshot(t)
	output := filepath.Join(t.TempDir(), "output")
	mustMkdirPrivate(t, output)
	if err := Build(source, output); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, ArchiveName), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(output); err == nil {
		t.Fatal("Inspect accepted a changed payload")
	}
}

func TestBuildRejectsUnsafeOrInvalidCohortsAndCleansOutput(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, source string)
	}{
		{name: "checksum mismatch", mutate: func(t *testing.T, source string) {
			if err := os.WriteFile(filepath.Join(source, ArchiveName), []byte("changed"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink", mutate: func(t *testing.T, source string) {
			if err := os.Remove(filepath.Join(source, ManifestName)); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(source, ChecksumsName), filepath.Join(source, ManifestName)); err != nil {
				t.Skip("symlink unavailable")
			}
		}},
		{name: "missing", mutate: func(t *testing.T, source string) {
			if err := os.Remove(filepath.Join(source, ManifestName)); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := fixtureSnapshot(t)
			test.mutate(t, source)
			output := filepath.Join(t.TempDir(), "output")
			mustMkdirPrivate(t, output)
			if err := Build(source, output); err == nil {
				t.Fatal("unsafe cohort was accepted")
			}
			entries, err := os.ReadDir(output)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("failed build left output: %#v", entries)
			}
		})
	}
}

func TestBuildRejectsNonEmptyAndOverlappingOutput(t *testing.T) {
	source := fixtureSnapshot(t)
	if err := Build(source, source); err == nil {
		t.Fatal("overlapping output was accepted")
	}
	output := filepath.Join(t.TempDir(), "output")
	mustMkdirPrivate(t, output)
	if err := os.WriteFile(filepath.Join(output, "existing"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Build(source, output); err == nil {
		t.Fatal("non-empty output was accepted")
	}
}

func fixtureSnapshot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	archive := []byte("core-snapshot")
	if err := os.WriteFile(filepath.Join(dir, ArchiveName), archive, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(archive)
	if err := os.WriteFile(filepath.Join(dir, ChecksumsName), []byte(fmt.Sprintf("%x  %s\n", digest, ArchiveName)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestName), []byte("format_version=4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func mustMkdirPrivate(t *testing.T, path string) {
	t.Helper()
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
}
