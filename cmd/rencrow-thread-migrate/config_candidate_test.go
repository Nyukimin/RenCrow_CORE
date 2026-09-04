package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRenderCanonicalThreadConfigCandidatePreservesAllOtherBytes(t *testing.T) {
	directory := canonicalThreadConfigTestDir(t)
	example, err := os.ReadFile(filepath.Join("..", "..", "config", "config.yaml.example"))
	if err != nil {
		t.Fatal(err)
	}
	source := "x_migration_unknown: keep # preserve unknown\n" + string(example)
	source = strings.Replace(source, `  redis_url: "redis://localhost:6379"`, `  redis_url: redis://localhost:6379/0 # preserve redis comment`, 1)
	source = strings.Replace(source, `  vectordb_url: "localhost:6334"`, "  vectordb_url: \"localhost:6334\"\n  vector_collection: 'rencrow_memory_1024' # preserve collection comment\n  vector_dimension: 1024", 1)
	sourcePath := filepath.Join(directory, "source.yaml")
	outputPath := filepath.Join(directory, "candidate.yaml")
	if err := os.WriteFile(sourcePath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	receipt, err := renderCanonicalThreadConfigCandidate(sourcePath, outputPath, 1, "rencrow_memory_1024_s5_deadbeef")
	if err != nil {
		t.Fatalf("renderCanonicalThreadConfigCandidate() error = %v", err)
	}
	if err := receipt.validate(); err != nil || receipt.Status != canonicalThreadConfigCandidateReady || !receipt.OnlyCanonicalRouteFieldsChanged {
		t.Fatalf("receipt = %+v, validate error = %v", receipt, err)
	}
	want := strings.Replace(source, `redis://localhost:6379/0`, `"redis://localhost:6379/1"`, 1)
	want = strings.Replace(want, `'rencrow_memory_1024'`, `"rencrow_memory_1024_s5_deadbeef"`, 1)
	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte(want)) {
		t.Fatal("candidate changed bytes outside the two route scalar tokens")
	}
	info, err := os.Lstat(outputPath)
	if err != nil || info.Mode().Perm() != 0o600 || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("candidate metadata is unsafe: info=%v err=%v", info, err)
	}
	encoded, _ := json.Marshal(receipt)
	if bytes.Contains(encoded, []byte("redis://")) || bytes.Contains(encoded, []byte("rencrow_memory_1024_s5_deadbeef")) || bytes.Contains(encoded, []byte(directory)) {
		t.Fatalf("receipt leaked route/path data: %s", encoded)
	}

	tampered := receipt
	tampered.SourceRedisDB = tampered.TargetRedisDB
	if err := tampered.validate(); err == nil {
		t.Fatal("tampered receipt was accepted")
	}
}

func TestRewriteCanonicalThreadConfigAcceptsPlainSingleAndDoubleQuotedScalars(t *testing.T) {
	for _, test := range []struct {
		name       string
		redis      string
		collection string
	}{
		{name: "plain", redis: "redis://localhost:6379/0", collection: "old_collection"},
		{name: "single", redis: "'redis://localhost:6379/0'", collection: "'old_collection'"},
		{name: "double", redis: `"redis://localhost:6379/0"`, collection: `"old_collection"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := []byte("conversation:\n  redis_url: " + test.redis + " # redis\n  vector_collection: " + test.collection + " # collection\n")
			output, targetURL, sourceDB, err := rewriteCanonicalThreadConfig(source, 1, "new_collection")
			if err != nil || targetURL != "redis://localhost:6379/1" || sourceDB != 0 {
				t.Fatalf("rewrite result = %q %q %d %v", output, targetURL, sourceDB, err)
			}
			if !bytes.Contains(output, []byte(`redis_url: "redis://localhost:6379/1" # redis`)) || !bytes.Contains(output, []byte(`vector_collection: "new_collection" # collection`)) {
				t.Fatalf("rewrite output = %q", output)
			}
		})
	}
}

func TestRewriteCanonicalThreadConfigRejectsAmbiguousOrDynamicRoutes(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{name: "duplicate conversation", source: "conversation: {redis_url: redis://localhost:6379/0, vector_collection: old}\nconversation: {}\n"},
		{name: "duplicate redis", source: "conversation:\n  redis_url: redis://localhost:6379/0\n  redis_url: redis://localhost:6379/2\n  vector_collection: old\n"},
		{name: "environment", source: "conversation:\n  redis_url: ${REDIS_URL}\n  vector_collection: old\n"},
		{name: "multiline", source: "conversation:\n  redis_url: redis://localhost:6379/0\n  vector_collection: |\n    old\n"},
		{name: "alias", source: "shared: &route old\nconversation:\n  redis_url: redis://localhost:6379/0\n  vector_collection: *route\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, _, err := rewriteCanonicalThreadConfig([]byte(test.source), 1, "new_collection"); err == nil {
				t.Fatal("ambiguous or dynamic route was accepted")
			}
		})
	}
	if _, _, _, err := rewriteCanonicalThreadConfig([]byte("conversation:\n  redis_url: redis://localhost:6379/0\n  vector_collection: old\n"), 0, "new_collection"); err == nil {
		t.Fatal("same Redis source and target DB was accepted")
	}
}

func TestRenderCanonicalThreadConfigCandidateRejectsUnsafeOrExistingTarget(t *testing.T) {
	directory := canonicalThreadConfigTestDir(t)
	sourcePath := filepath.Join(directory, "source.yaml")
	if err := os.WriteFile(sourcePath, []byte("conversation:\n  redis_url: redis://localhost:6379/0\n  vector_collection: old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if receipt, err := renderCanonicalThreadConfigCandidate(sourcePath, filepath.Join(directory, "unsafe.yaml"), 1, "../unsafe"); err == nil || receipt.Status != canonicalThreadConfigCandidateBlocked {
		t.Fatalf("unsafe collection result = %+v %v", receipt, err)
	}

	outputPath := filepath.Join(directory, "existing.yaml")
	if err := os.WriteFile(outputPath, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if receipt, err := renderCanonicalThreadConfigCandidate(sourcePath, outputPath, 1, "new_collection"); err == nil || receipt.Status != canonicalThreadConfigCandidateBlocked {
		t.Fatalf("existing target result = %+v %v", receipt, err)
	}
	data, _ := os.ReadFile(outputPath)
	if string(data) != "preserve" {
		t.Fatal("existing target was overwritten")
	}

	if runtime.GOOS != "windows" {
		unsafeParent := filepath.Join(directory, "unsafe-parent")
		if err := os.Mkdir(unsafeParent, 0o750); err != nil {
			t.Fatal(err)
		}
		if _, err := renderCanonicalThreadConfigCandidate(sourcePath, filepath.Join(unsafeParent, "candidate.yaml"), 1, "new_collection"); err == nil {
			t.Fatal("non-owner-only output parent was accepted")
		}
	}
}

func canonicalThreadConfigTestDir(t *testing.T) string {
	t.Helper()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(workingDirectory, "..", "..", "Tmp", "test-runtime", "identity-step05-sol", "config-candidate")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(root, "case-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return directory
}
