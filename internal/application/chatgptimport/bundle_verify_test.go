package chatgptimport

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

type testSourceFile struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type testChunk struct {
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type testSourceIndex struct {
	Path   string      `json:"path"`
	Bytes  int64       `json:"bytes"`
	SHA256 string      `json:"sha256"`
	Chunks []testChunk `json:"chunks"`
}

type testManifest struct {
	Format            string           `json:"format"`
	ExportID          string           `json:"export_id"`
	GeneratedAt       string           `json:"generated_at"`
	Files             []testSourceFile `json:"files"`
	ConversationFiles int              `json:"conversation_files"`
	Conversations     int              `json:"conversations"`
	Messages          int              `json:"messages"`
	UserMessages      int              `json:"user_messages"`
	AssistantMessages int              `json:"assistant_messages"`
	Assets            int              `json:"assets"`
	ArtifactSHA256    string           `json:"artifact_sha256"`
	SchemaVersion     string           `json:"schema_version"`
	ConverterVersion  string           `json:"converter_version"`
	ArtifactBytes     int64            `json:"artifact_bytes"`
	ManifestSHA256    string           `json:"manifest_sha256"`
	SourceFileCount   int              `json:"source_file_count"`
	SourceChunkCount  int              `json:"source_chunk_count"`
	SourceObjectCount int              `json:"source_object_count"`
}

type testCanonicalManifest struct {
	Format            string           `json:"format"`
	ExportID          string           `json:"export_id"`
	Files             []testSourceFile `json:"files"`
	ConversationFiles int              `json:"conversation_files"`
	Conversations     int              `json:"conversations"`
	Messages          int              `json:"messages"`
	UserMessages      int              `json:"user_messages"`
	AssistantMessages int              `json:"assistant_messages"`
	Assets            int              `json:"assets"`
	ArtifactSHA256    string           `json:"artifact_sha256"`
	SchemaVersion     string           `json:"schema_version"`
	ConverterVersion  string           `json:"converter_version"`
	ArtifactBytes     int64            `json:"artifact_bytes"`
	SourceFileCount   int              `json:"source_file_count"`
	SourceChunkCount  int              `json:"source_chunk_count"`
	SourceObjectCount int              `json:"source_object_count"`
}

type testArtifactRecord struct {
	Format                string          `json:"format"`
	ExportID              string          `json:"export_id"`
	EvidenceID            string          `json:"evidence_id"`
	ConversationID        string          `json:"conversation_id"`
	ConversationTitle     string          `json:"conversation_title"`
	ConversationCreatedAt string          `json:"conversation_created_at,omitempty"`
	ConversationUpdatedAt string          `json:"conversation_updated_at,omitempty"`
	NodeID                string          `json:"node_id"`
	ParentNodeID          string          `json:"parent_node_id,omitempty"`
	ChildNodeIDs          []string        `json:"child_node_ids,omitempty"`
	OnCurrentBranch       bool            `json:"on_current_branch"`
	MessageID             string          `json:"message_id"`
	MessageCreatedAt      string          `json:"message_created_at,omitempty"`
	Role                  string          `json:"role"`
	ContentType           string          `json:"content_type"`
	Text                  string          `json:"text"`
	Content               json.RawMessage `json:"content"`
	Metadata              json.RawMessage `json:"metadata,omitempty"`
}

type bundleFixture struct {
	root         string
	manifestPath string
	artifactPath string
	manifest     testManifest
	entries      []testTarEntry
	chunkBytes   int64
}

type testTarEntry struct {
	header tar.Header
	data   []byte
}

func TestVerifyBundleAcceptsValidDeterministicFixture(t *testing.T) {
	fixture := newBundleFixture(t)
	bundle, err := VerifyBundle(context.Background(), fixture.root, fixture.manifestPath, fixture.artifactPath, Options{ChunkBytes: fixture.chunkBytes})
	if err != nil {
		t.Fatalf("VerifyBundle() error = %v", err)
	}
	defer bundle.Close()
	binding := bundle.Binding()
	if binding.ExportID != fixture.manifest.ExportID || binding.Messages != 2 || binding.SourceFileCount != 2 {
		t.Fatalf("unexpected binding: %+v", binding)
	}
	records, err := bundle.OpenRecords()
	if err != nil {
		t.Fatalf("OpenRecords() error = %v", err)
	}
	if info, err := records.(*os.File).Stat(); err != nil || info.Size() == 0 {
		t.Fatalf("records stream is not readable: info=%v err=%v", info, err)
	}
	_ = records.Close()
	encodedBundle, err := json.Marshal(bundle)
	if err != nil || string(encodedBundle) != "{}" {
		t.Fatalf("VerifiedBundle leaked internal fields: %s err=%v", encodedBundle, err)
	}
	stage := bundle.stage
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(stage); err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("verification stage is not private: info=%v err=%v", info, err)
		}
		for _, privateFile := range []string{bundle.records, bundle.sourceIndex} {
			if info, err := os.Stat(privateFile); err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("verified file is not private: info=%v err=%v", info, err)
			}
		}
	}
	if err := bundle.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := os.Stat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("verifier stage still exists after Close: %v", err)
	}
}

func TestVerifyBundleDistinguishesManifestAndArtifactSemanticFailures(t *testing.T) {
	t.Run("manifest", func(t *testing.T) {
		fixture := newBundleFixture(t)
		fixture.manifest.Format = "unsupported"
		resignManifest(t, &fixture.manifest)
		writeTestManifest(t, fixture.manifestPath, fixture.manifest)
		_, err := VerifyBundle(context.Background(), fixture.root, fixture.manifestPath, fixture.artifactPath, Options{ChunkBytes: fixture.chunkBytes})
		if err == nil || !errors.Is(err, ErrInvalidManifest) || errors.Is(err, ErrInvalidBundle) {
			t.Fatalf("VerifyBundle() error = %v, want manifest-invalid only", err)
		}
	})

	t.Run("artifact", func(t *testing.T) {
		fixture := newBundleFixture(t)
		entries := cloneTestEntries(fixture.entries)
		recordsIndex := findTestEntry(t, entries, "records.jsonl")
		entries[recordsIndex].data = append(entries[recordsIndex].data, []byte("{}\n")...)
		rewriteFixtureArtifact(t, &fixture, entries, nil)
		_, err := VerifyBundle(context.Background(), fixture.root, fixture.manifestPath, fixture.artifactPath, Options{ChunkBytes: fixture.chunkBytes})
		if err == nil || !errors.Is(err, ErrInvalidBundle) || errors.Is(err, ErrInvalidManifest) {
			t.Fatalf("VerifyBundle() error = %v, want artifact-invalid only", err)
		}
	})
}

func TestVerifyBundleRejectsSelfConsistentResignedRecordTamper(t *testing.T) {
	tests := map[string]func(*testArtifactRecord){
		"text":   func(record *testArtifactRecord) { record.Text = "self-consistent but not source-derived" },
		"branch": func(record *testArtifactRecord) { record.OnCurrentBranch = false },
		"content": func(record *testArtifactRecord) {
			record.Content = json.RawMessage(`{"content_type":"text","parts":["changed"]}`)
		},
		"metadata":    func(record *testArtifactRecord) { record.Metadata = json.RawMessage(`{"source":"changed"}`) },
		"create time": func(record *testArtifactRecord) { record.MessageCreatedAt = "2026-08-16T00:00:00Z" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newBundleFixture(t)
			entries := cloneTestEntries(fixture.entries)
			recordsIndex := findTestEntry(t, entries, "records.jsonl")
			lines := bytes.Split(bytes.TrimSuffix(entries[recordsIndex].data, []byte{'\n'}), []byte{'\n'})
			var record testArtifactRecord
			if err := json.Unmarshal(lines[0], &record); err != nil {
				t.Fatal(err)
			}
			mutate(&record)
			lines[0], _ = json.Marshal(record)
			entries[recordsIndex].data = append(bytes.Join(lines, []byte{'\n'}), '\n')
			rewriteFixtureArtifact(t, &fixture, entries, nil)
			assertVerifyFails(t, fixture, Options{ChunkBytes: fixture.chunkBytes}, "source-derived")
		})
	}
}

func TestVerifyBundleRejectsResignedTrailingArtifactBytes(t *testing.T) {
	fixture := newBundleFixture(t)
	rewriteFixtureArtifact(t, &fixture, fixture.entries, []byte("trailing"))
	assertVerifyFails(t, fixture, Options{ChunkBytes: fixture.chunkBytes}, "trailing")
}

func TestVerifyBundleRejectsResignedNonzeroMemberPadding(t *testing.T) {
	fixture := newBundleFixture(t)
	artifact, err := os.ReadFile(fixture.artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	firstSize := len(fixture.entries[0].data)
	paddingOffset := 512 + firstSize
	if paddingOffset >= len(artifact) || firstSize%512 == 0 {
		t.Fatal("fixture has no first-member padding")
	}
	artifact[paddingOffset] = 1
	rewriteFixtureRawArtifact(t, &fixture, artifact)
	assertVerifyFails(t, fixture, Options{ChunkBytes: fixture.chunkBytes}, "canonical")
}

func TestVerifyBundleRejectsResignedEndBlockAnomaly(t *testing.T) {
	fixture := newBundleFixture(t)
	artifact, err := os.ReadFile(fixture.artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	artifact[len(artifact)-1] = 1
	rewriteFixtureRawArtifact(t, &fixture, artifact)
	assertVerifyFails(t, fixture, Options{ChunkBytes: fixture.chunkBytes}, "")
}

func TestVerifyBundleRejectsResignedAlternateTarNumericEncoding(t *testing.T) {
	fixture := newBundleFixture(t)
	artifact, err := os.ReadFile(fixture.artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	copy(artifact[100:108], []byte("  0600 \x00"))
	recomputeTestTarChecksum(artifact[:512])
	rewriteFixtureRawArtifact(t, &fixture, artifact)
	assertVerifyFails(t, fixture, Options{ChunkBytes: fixture.chunkBytes}, "canonical")
}

func TestVerifyBundleCanonicalManifestHashExcludesGeneratedAt(t *testing.T) {
	fixture := newBundleFixture(t)
	fixture.manifest.GeneratedAt = "2030-01-02T03:04:05Z"
	writeTestManifest(t, fixture.manifestPath, fixture.manifest)
	bundle, err := VerifyBundle(context.Background(), fixture.root, fixture.manifestPath, fixture.artifactPath, Options{ChunkBytes: fixture.chunkBytes})
	if err != nil {
		t.Fatalf("generated_at changed canonical manifest hash: %v", err)
	}
	_ = bundle.Close()
}

func TestVerifyBundleRejectsNoncanonicalManifestRecordAndIndex(t *testing.T) {
	t.Run("manifest hash", func(t *testing.T) {
		fixture := newBundleFixture(t)
		fixture.manifest.ManifestSHA256 = strings.Repeat("0", 64)
		writeTestManifest(t, fixture.manifestPath, fixture.manifest)
		assertVerifyFails(t, fixture, Options{ChunkBytes: fixture.chunkBytes}, "manifest_sha256")
	})
	t.Run("record JSON", func(t *testing.T) {
		fixture := newBundleFixture(t)
		entries := cloneTestEntries(fixture.entries)
		index := findTestEntry(t, entries, "records.jsonl")
		entries[index].data = append([]byte{' '}, entries[index].data...)
		rewriteFixtureArtifact(t, &fixture, entries, nil)
		assertVerifyFails(t, fixture, Options{ChunkBytes: fixture.chunkBytes}, "canonical")
	})
	t.Run("source index JSON", func(t *testing.T) {
		fixture := newBundleFixture(t)
		entries := cloneTestEntries(fixture.entries)
		index := findTestEntry(t, entries, "source-files.jsonl")
		entries[index].data = append([]byte{' '}, entries[index].data...)
		rewriteFixtureArtifact(t, &fixture, entries, nil)
		assertVerifyFails(t, fixture, Options{ChunkBytes: fixture.chunkBytes}, "canonical")
	})
}

func TestVerifyBundleRejectsManifestUnknownDuplicateAndTrailingJSON(t *testing.T) {
	tests := map[string]func([]byte) []byte{
		"unknown": func(data []byte) []byte {
			return bytes.Replace(data, []byte("{\n"), []byte("{\n  \"unknown\": true,\n"), 1)
		},
		"duplicate": func(data []byte) []byte {
			return bytes.Replace(data, []byte("\"format\":"), []byte("\"format\": \"duplicate\",\n  \"format\":"), 1)
		},
		"trailing": func(data []byte) []byte { return append(data, []byte("{}")...) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newBundleFixture(t)
			data, err := os.ReadFile(fixture.manifestPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(fixture.manifestPath, mutate(data), 0o600); err != nil {
				t.Fatal(err)
			}
			assertVerifyFails(t, fixture, Options{ChunkBytes: fixture.chunkBytes}, "JSON")
		})
	}
}

func TestVerifyBundleRejectsDuplicateKeysInsideRecordAndIndex(t *testing.T) {
	tests := []string{"records.jsonl", "source-files.jsonl"}
	for _, member := range tests {
		t.Run(member, func(t *testing.T) {
			fixture := newBundleFixture(t)
			entries := cloneTestEntries(fixture.entries)
			index := findTestEntry(t, entries, member)
			entries[index].data = bytes.Replace(entries[index].data, []byte("{\""), []byte("{\"duplicate\":true,\"duplicate\":false,\""), 1)
			rewriteFixtureArtifact(t, &fixture, entries, nil)
			assertVerifyFails(t, fixture, Options{ChunkBytes: fixture.chunkBytes}, "duplicate")
		})
	}
}

func TestVerifyBundleRejectsWrongTarHeaderOrderAndName(t *testing.T) {
	t.Run("header", func(t *testing.T) {
		fixture := newBundleFixture(t)
		entries := cloneTestEntries(fixture.entries)
		entries[0].header.Mode = 0o644
		rewriteFixtureArtifact(t, &fixture, entries, nil)
		assertVerifyFails(t, fixture, Options{ChunkBytes: fixture.chunkBytes}, "header")
	})
	t.Run("order", func(t *testing.T) {
		fixture := newBundleFixture(t)
		entries := cloneTestEntries(fixture.entries)
		entries[0], entries[1] = entries[1], entries[0]
		rewriteFixtureArtifact(t, &fixture, entries, nil)
		assertVerifyFails(t, fixture, Options{ChunkBytes: fixture.chunkBytes}, "sorted")
	})
	t.Run("name", func(t *testing.T) {
		fixture := newBundleFixture(t)
		entries := cloneTestEntries(fixture.entries)
		entries[0].header.Name = "objects/sha256/aa/../../escape"
		rewriteFixtureArtifact(t, &fixture, entries, nil)
		assertVerifyFails(t, fixture, Options{ChunkBytes: fixture.chunkBytes}, "object")
		if _, err := os.Stat(filepath.Join(fixture.root, "escape")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unsafe TAR name escaped stage: %v", err)
		}
	})
}

func TestVerifyBundleRejectsMissingExtraDuplicateAndTruncatedTarMembers(t *testing.T) {
	t.Run("missing records", func(t *testing.T) {
		fixture := newBundleFixture(t)
		entries := cloneTestEntries(fixture.entries)
		index := findTestEntry(t, entries, "records.jsonl")
		entries = append(entries[:index], entries[index+1:]...)
		rewriteFixtureArtifact(t, &fixture, entries, nil)
		assertVerifyFails(t, fixture, Options{ChunkBytes: fixture.chunkBytes}, "misplaced")
	})
	t.Run("extra", func(t *testing.T) {
		fixture := newBundleFixture(t)
		entries := append(cloneTestEntries(fixture.entries), deterministicTestEntry("unexpected", []byte("x")))
		sort.Slice(entries, func(i, j int) bool { return entries[i].header.Name < entries[j].header.Name })
		rewriteFixtureArtifact(t, &fixture, entries, nil)
		assertVerifyFails(t, fixture, Options{ChunkBytes: fixture.chunkBytes}, "unexpected")
	})
	t.Run("duplicate", func(t *testing.T) {
		fixture := newBundleFixture(t)
		entries := cloneTestEntries(fixture.entries)
		index := findTestEntry(t, entries, "records.jsonl")
		entries = append(entries[:index+1], append([]testTarEntry{entries[index]}, entries[index+1:]...)...)
		rewriteFixtureArtifact(t, &fixture, entries, nil)
		assertVerifyFails(t, fixture, Options{ChunkBytes: fixture.chunkBytes}, "sorted")
	})
	t.Run("truncated", func(t *testing.T) {
		fixture := newBundleFixture(t)
		artifact, err := os.ReadFile(fixture.artifactPath)
		if err != nil {
			t.Fatal(err)
		}
		artifact = artifact[:len(artifact)-100]
		digest := sha256.Sum256(artifact)
		fixture.manifest.ArtifactBytes = int64(len(artifact))
		fixture.manifest.ArtifactSHA256 = hex.EncodeToString(digest[:])
		resignManifest(t, &fixture.manifest)
		if err := os.WriteFile(fixture.artifactPath, artifact, 0o600); err != nil {
			t.Fatal(err)
		}
		writeTestManifest(t, fixture.manifestPath, fixture.manifest)
		assertVerifyFails(t, fixture, Options{ChunkBytes: fixture.chunkBytes}, "sizes")
	})
}

func TestVerifyBundleRejectsMissingTamperedAndUnreferencedObjects(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		fixture := newBundleFixture(t)
		entries := cloneTestEntries(fixture.entries)
		entries = entries[1:]
		rewriteFixtureArtifact(t, &fixture, entries, nil)
		assertVerifyFails(t, fixture, Options{ChunkBytes: fixture.chunkBytes}, "object")
	})
	t.Run("tampered", func(t *testing.T) {
		fixture := newBundleFixture(t)
		entries := cloneTestEntries(fixture.entries)
		entries[0].data[0] ^= 0xff
		rewriteFixtureArtifact(t, &fixture, entries, nil)
		assertVerifyFails(t, fixture, Options{ChunkBytes: fixture.chunkBytes}, "hash")
	})
	t.Run("unreferenced", func(t *testing.T) {
		fixture := newBundleFixture(t)
		entries := cloneTestEntries(fixture.entries)
		orphan := []byte("orphan")
		digest := sha256.Sum256(orphan)
		hash := hex.EncodeToString(digest[:])
		entries = append(entries, deterministicTestEntry("objects/sha256/"+hash[:2]+"/"+hash, orphan))
		sort.Slice(entries, func(i, j int) bool { return entries[i].header.Name < entries[j].header.Name })
		rewriteFixtureArtifact(t, &fixture, entries, nil)
		assertVerifyFails(t, fixture, Options{ChunkBytes: fixture.chunkBytes}, "object")
	})
}

func TestVerifyBundleRejectsChunkPathCountHashAndVersionTamper(t *testing.T) {
	t.Run("chunk", func(t *testing.T) {
		fixture := newBundleFixture(t)
		entries := cloneTestEntries(fixture.entries)
		index := findTestEntry(t, entries, "source-files.jsonl")
		lines := bytes.Split(bytes.TrimSuffix(entries[index].data, []byte{'\n'}), []byte{'\n'})
		var source testSourceIndex
		if err := json.Unmarshal(lines[0], &source); err != nil {
			t.Fatal(err)
		}
		source.Chunks[0].Bytes++
		lines[0], _ = json.Marshal(source)
		entries[index].data = append(bytes.Join(lines, []byte{'\n'}), '\n')
		rewriteFixtureArtifact(t, &fixture, entries, nil)
		assertVerifyFails(t, fixture, Options{ChunkBytes: fixture.chunkBytes}, "chunk")
	})
	t.Run("path", func(t *testing.T) {
		fixture := newBundleFixture(t)
		entries := cloneTestEntries(fixture.entries)
		index := findTestEntry(t, entries, "source-files.jsonl")
		lines := bytes.Split(bytes.TrimSuffix(entries[index].data, []byte{'\n'}), []byte{'\n'})
		var source testSourceIndex
		if err := json.Unmarshal(lines[0], &source); err != nil {
			t.Fatal(err)
		}
		source.Path = "../escape"
		lines[0], _ = json.Marshal(source)
		entries[index].data = append(bytes.Join(lines, []byte{'\n'}), '\n')
		rewriteFixtureArtifact(t, &fixture, entries, nil)
		assertVerifyFails(t, fixture, Options{ChunkBytes: fixture.chunkBytes}, "path")
	})
	t.Run("count", func(t *testing.T) {
		fixture := newBundleFixture(t)
		fixture.manifest.Messages++
		resignManifest(t, &fixture.manifest)
		writeTestManifest(t, fixture.manifestPath, fixture.manifest)
		assertVerifyFails(t, fixture, Options{ChunkBytes: fixture.chunkBytes}, "count")
	})
	t.Run("artifact hash", func(t *testing.T) {
		fixture := newBundleFixture(t)
		fixture.manifest.ArtifactSHA256 = strings.Repeat("0", 64)
		resignManifest(t, &fixture.manifest)
		writeTestManifest(t, fixture.manifestPath, fixture.manifest)
		assertVerifyFails(t, fixture, Options{ChunkBytes: fixture.chunkBytes}, "SHA-256")
	})
	t.Run("version", func(t *testing.T) {
		fixture := newBundleFixture(t)
		fixture.manifest.ConverterVersion = "chatgpt-export-memory-go/v3"
		resignManifest(t, &fixture.manifest)
		writeTestManifest(t, fixture.manifestPath, fixture.manifest)
		assertVerifyFails(t, fixture, Options{ChunkBytes: fixture.chunkBytes}, "unsupported")
	})
}

func TestVerifyBundleEnforcesInjectableBounds(t *testing.T) {
	tests := map[string]Options{
		"manifest":           {MaxManifestBytes: 1},
		"artifact":           {MaxArtifactBytes: 1},
		"chunk":              {ChunkBytes: 1},
		"JSON line":          {MaxJSONLineBytes: 1},
		"source file count":  {MaxSourceFiles: 1},
		"source file bytes":  {MaxSourceFileBytes: 1},
		"source total bytes": {MaxSourceTotalBytes: 1},
	}
	for name, options := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newBundleFixture(t)
			if options.ChunkBytes == 0 {
				options.ChunkBytes = fixture.chunkBytes
			}
			_, err := VerifyBundle(context.Background(), fixture.root, fixture.manifestPath, fixture.artifactPath, options)
			if err == nil || !errors.Is(err, ErrBounds) {
				t.Fatalf("VerifyBundle() error = %v, want ErrBounds", err)
			}
		})
	}
}

func TestVerifiedBundleReconstructsEverySourceFromStreamingAccess(t *testing.T) {
	fixture := newBundleFixture(t)
	bundle, err := VerifyBundle(context.Background(), fixture.root, fixture.manifestPath, fixture.artifactPath, Options{ChunkBytes: fixture.chunkBytes})
	if err != nil {
		t.Fatal(err)
	}
	defer bundle.Close()
	index, err := bundle.OpenSourceIndex()
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	scanner := bufio.NewScanner(index)
	for scanner.Scan() {
		var source testSourceIndex
		if err := json.Unmarshal(scanner.Bytes(), &source); err != nil {
			t.Fatal(err)
		}
		hash := sha256.New()
		var count int64
		for _, chunk := range source.Chunks {
			object, err := bundle.OpenObject(chunk.SHA256)
			if err != nil {
				t.Fatal(err)
			}
			copied, err := io.Copy(hash, object)
			_ = object.Close()
			if err != nil {
				t.Fatal(err)
			}
			count += copied
		}
		if count != source.Bytes || hex.EncodeToString(hash.Sum(nil)) != source.SHA256 {
			t.Fatalf("source %q was not reconstructed", source.Path)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyBundleRejectsSelfConsistentFalseSourceReconstructionHash(t *testing.T) {
	fixture := newBundleFixture(t)
	entries := cloneTestEntries(fixture.entries)
	indexEntry := findTestEntry(t, entries, "source-files.jsonl")
	indexLines := bytes.Split(bytes.TrimSuffix(entries[indexEntry].data, []byte{'\n'}), []byte{'\n'})
	var source testSourceIndex
	if err := json.Unmarshal(indexLines[0], &source); err != nil {
		t.Fatal(err)
	}
	falseHash := strings.Repeat("0", 64)
	source.SHA256 = falseHash
	indexLines[0], _ = json.Marshal(source)
	entries[indexEntry].data = append(bytes.Join(indexLines, []byte{'\n'}), '\n')
	fixture.manifest.Files[0].SHA256 = falseHash
	fixture.manifest.ExportID = testSourceExportID(fixture.manifest.Files)
	recordsEntry := findTestEntry(t, entries, "records.jsonl")
	recordLines := bytes.Split(bytes.TrimSuffix(entries[recordsEntry].data, []byte{'\n'}), []byte{'\n'})
	for index := range recordLines {
		var record testArtifactRecord
		if err := json.Unmarshal(recordLines[index], &record); err != nil {
			t.Fatal(err)
		}
		record.ExportID = fixture.manifest.ExportID
		recordLines[index], _ = json.Marshal(record)
	}
	entries[recordsEntry].data = append(bytes.Join(recordLines, []byte{'\n'}), '\n')
	rewriteFixtureArtifact(t, &fixture, entries, nil)
	assertVerifyFails(t, fixture, Options{ChunkBytes: fixture.chunkBytes}, "reconstruction")
}

func TestVerifyBundleFailureCleansOwnedStageWithoutDeletingInputs(t *testing.T) {
	fixture := newBundleFixture(t)
	entries := cloneTestEntries(fixture.entries)
	entries[0].header.Name = "../../escape"
	rewriteFixtureArtifact(t, &fixture, entries, nil)
	_, err := VerifyBundle(context.Background(), fixture.root, fixture.manifestPath, fixture.artifactPath, Options{ChunkBytes: fixture.chunkBytes})
	if err == nil {
		t.Fatal("VerifyBundle() succeeded for unsafe TAR")
	}
	stages, globErr := filepath.Glob(filepath.Join(fixture.root, ".chatgpt-bundle-verify-*"))
	if globErr != nil || len(stages) != 0 {
		t.Fatalf("failed verification left stage: %v err=%v", stages, globErr)
	}
	for _, input := range []string{fixture.manifestPath, fixture.artifactPath} {
		if _, err := os.Stat(input); err != nil {
			t.Fatalf("input was removed: %s: %v", input, err)
		}
	}
	if _, err := os.Stat(filepath.Join(fixture.root, "escape")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsafe TAR escaped stage: %v", err)
	}
}

func TestVerifyBundleHonorsCancellationBeforeStaging(t *testing.T) {
	fixture := newBundleFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := VerifyBundle(ctx, fixture.root, fixture.manifestPath, fixture.artifactPath, Options{ChunkBytes: fixture.chunkBytes})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("VerifyBundle() error = %v, want context.Canceled", err)
	}
	stages, _ := filepath.Glob(filepath.Join(fixture.root, ".chatgpt-bundle-verify-*"))
	if len(stages) != 0 {
		t.Fatalf("canceled verification created stages: %v", stages)
	}
}

func TestValidateSourcePathRejectsUnsafeCrossPlatformForms(t *testing.T) {
	unsafe := []string{"", "/absolute", "C:/volume", `folder\file`, "dot/./file", "parent/../file", "double//file", "nul\x00file"}
	for _, value := range unsafe {
		if err := validateSourcePath(value); err == nil {
			t.Errorf("validateSourcePath(%q) succeeded", value)
		}
	}
	if err := validateSourcePath("日本語/safe-file.json"); err != nil {
		t.Fatalf("UTF-8 relative path rejected: %v", err)
	}
}

func TestVerifyBundleRequiresExistingPrivateRootAndRegularInputs(t *testing.T) {
	t.Run("missing root", func(t *testing.T) {
		fixture := newBundleFixture(t)
		missing := filepath.Join(fixture.root, "missing", "root")
		_, err := VerifyBundle(context.Background(), missing, fixture.manifestPath, fixture.artifactPath, Options{ChunkBytes: fixture.chunkBytes})
		if err == nil {
			t.Fatal("VerifyBundle() created a missing root")
		}
		if _, statErr := os.Stat(missing); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("missing root was created: %v", statErr)
		}
	})
	t.Run("symlink input", func(t *testing.T) {
		fixture := newBundleFixture(t)
		link := filepath.Join(fixture.root, "artifact-link.tar")
		if err := os.Symlink(fixture.artifactPath, link); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		_, err := VerifyBundle(context.Background(), fixture.root, fixture.manifestPath, link, Options{ChunkBytes: fixture.chunkBytes})
		if err == nil || !strings.Contains(err.Error(), "non-symlink") {
			t.Fatalf("VerifyBundle() error = %v", err)
		}
	})
}

func newBundleFixture(t *testing.T) bundleFixture {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	chunkBytes := int64(64)
	conversation := []byte(`[{"id":"conv-1","title":"Title","create_time":1.5,"update_time":2,"current_node":"n2","mapping":{"n2":{"id":"n2","parent":"n1","children":[],"message":{"id":"m2","author":{"role":"assistant"},"create_time":1.75,"content":{"content_type":"text","parts":["world"]},"metadata":{"source":"unit"}}},"n1":{"id":"n1","parent":null,"children":["n2"],"message":{"id":"m1","author":{"role":"user"},"create_time":1.625,"content":{"content_type":"text","parts":["hello"]},"metadata":{"source":"unit"}}}}}]`)
	sources := map[string][]byte{
		"asset.bin":              []byte("asset-data"),
		"conversations-001.json": conversation,
	}
	paths := []string{"asset.bin", "conversations-001.json"}
	objects := make(map[string][]byte)
	indexes := make([]testSourceIndex, 0, len(paths))
	files := make([]testSourceFile, 0, len(paths))
	chunkCount := 0
	for _, name := range paths {
		data := sources[name]
		digest := sha256.Sum256(data)
		file := testSourceFile{Path: name, Bytes: int64(len(data)), SHA256: hex.EncodeToString(digest[:])}
		files = append(files, file)
		index := testSourceIndex{Path: name, Bytes: file.Bytes, SHA256: file.SHA256}
		for offset := int64(0); offset < int64(len(data)); offset += chunkBytes {
			end := offset + chunkBytes
			if end > int64(len(data)) {
				end = int64(len(data))
			}
			chunk := append([]byte(nil), data[offset:end]...)
			chunkDigest := sha256.Sum256(chunk)
			hash := hex.EncodeToString(chunkDigest[:])
			objects[hash] = chunk
			index.Chunks = append(index.Chunks, testChunk{SHA256: hash, Bytes: int64(len(chunk))})
			chunkCount++
		}
		indexes = append(indexes, index)
	}
	exportHash := sha256.New()
	for _, file := range files {
		_, _ = exportHash.Write([]byte(file.Path + "\x00"))
		_, _ = exportHash.Write([]byte(jsonNumber(file.Bytes) + "\x00" + file.SHA256 + "\n"))
	}
	exportID := hex.EncodeToString(exportHash.Sum(nil))
	records := recordsForFixture(t, exportID)
	var recordsJSONL bytes.Buffer
	for _, record := range records {
		data, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		recordsJSONL.Write(data)
		recordsJSONL.WriteByte('\n')
	}
	var indexJSONL bytes.Buffer
	for _, index := range indexes {
		data, err := json.Marshal(index)
		if err != nil {
			t.Fatal(err)
		}
		indexJSONL.Write(data)
		indexJSONL.WriteByte('\n')
	}
	objectHashes := make([]string, 0, len(objects))
	for hash := range objects {
		objectHashes = append(objectHashes, hash)
	}
	sort.Strings(objectHashes)
	entries := make([]testTarEntry, 0, len(objects)+2)
	for _, hash := range objectHashes {
		entries = append(entries, deterministicTestEntry("objects/sha256/"+hash[:2]+"/"+hash, objects[hash]))
	}
	entries = append(entries, deterministicTestEntry("records.jsonl", recordsJSONL.Bytes()))
	entries = append(entries, deterministicTestEntry("source-files.jsonl", indexJSONL.Bytes()))
	artifact := buildTestTar(t, entries)
	artifactDigest := sha256.Sum256(artifact)
	manifest := testManifest{
		Format: BundleFormat, ExportID: exportID, GeneratedAt: "2026-08-16T00:00:00Z", Files: files,
		ConversationFiles: 1, Conversations: 1, Messages: 2, UserMessages: 1, AssistantMessages: 1, Assets: 1,
		ArtifactSHA256: hex.EncodeToString(artifactDigest[:]), SchemaVersion: RecordSchema, ConverterVersion: ConverterVersion,
		ArtifactBytes: int64(len(artifact)), SourceFileCount: len(files), SourceChunkCount: chunkCount, SourceObjectCount: len(objects),
	}
	resignManifest(t, &manifest)
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	manifestData = append(manifestData, '\n')
	manifestPath := filepath.Join(root, "manifest.json")
	artifactPath := filepath.Join(root, "artifact.tar")
	if err := os.WriteFile(manifestPath, manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifactPath, artifact, 0o600); err != nil {
		t.Fatal(err)
	}
	return bundleFixture{root: root, manifestPath: manifestPath, artifactPath: artifactPath, manifest: manifest, entries: entries, chunkBytes: chunkBytes}
}

func recordsForFixture(t *testing.T, exportID string) []testArtifactRecord {
	t.Helper()
	content1 := json.RawMessage(`{"content_type":"text","parts":["hello"]}`)
	content2 := json.RawMessage(`{"content_type":"text","parts":["world"]}`)
	metadata := json.RawMessage(`{"source":"unit"}`)
	return []testArtifactRecord{
		{Format: RecordSchema, ExportID: exportID, EvidenceID: "chatgpt_export:conv-1:m1", ConversationID: "conv-1", ConversationTitle: "Title", ConversationCreatedAt: "1970-01-01T00:00:01.5Z", ConversationUpdatedAt: "1970-01-01T00:00:02Z", NodeID: "n1", ChildNodeIDs: []string{"n2"}, OnCurrentBranch: true, MessageID: "m1", MessageCreatedAt: "1970-01-01T00:00:01.625Z", Role: "user", ContentType: "text", Text: "hello", Content: content1, Metadata: metadata},
		{Format: RecordSchema, ExportID: exportID, EvidenceID: "chatgpt_export:conv-1:m2", ConversationID: "conv-1", ConversationTitle: "Title", ConversationCreatedAt: "1970-01-01T00:00:01.5Z", ConversationUpdatedAt: "1970-01-01T00:00:02Z", NodeID: "n2", ParentNodeID: "n1", OnCurrentBranch: true, MessageID: "m2", MessageCreatedAt: "1970-01-01T00:00:01.75Z", Role: "assistant", ContentType: "text", Text: "world", Content: content2, Metadata: metadata},
	}
}

func jsonNumber(value int64) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func deterministicTestEntry(name string, data []byte) testTarEntry {
	return testTarEntry{header: tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), ModTime: time.Unix(0, 0).UTC(), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}, data: append([]byte(nil), data...)}
}

func buildTestTar(t *testing.T, entries []testTarEntry) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	for _, entry := range entries {
		header := entry.header
		header.Size = int64(len(entry.data))
		if err := writer.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func resignManifest(t *testing.T, manifest *testManifest) {
	t.Helper()
	canonical := testCanonicalManifest{
		Format: manifest.Format, ExportID: manifest.ExportID, Files: append([]testSourceFile(nil), manifest.Files...),
		ConversationFiles: manifest.ConversationFiles, Conversations: manifest.Conversations, Messages: manifest.Messages,
		UserMessages: manifest.UserMessages, AssistantMessages: manifest.AssistantMessages, Assets: manifest.Assets,
		ArtifactSHA256: manifest.ArtifactSHA256, SchemaVersion: manifest.SchemaVersion, ConverterVersion: manifest.ConverterVersion,
		ArtifactBytes: manifest.ArtifactBytes, SourceFileCount: manifest.SourceFileCount, SourceChunkCount: manifest.SourceChunkCount,
		SourceObjectCount: manifest.SourceObjectCount,
	}
	sort.Slice(canonical.Files, func(i, j int) bool { return canonical.Files[i].Path < canonical.Files[j].Path })
	data, err := json.Marshal(canonical)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	manifest.ManifestSHA256 = hex.EncodeToString(digest[:])
}

func cloneTestEntries(entries []testTarEntry) []testTarEntry {
	result := make([]testTarEntry, len(entries))
	for index, entry := range entries {
		result[index] = testTarEntry{header: entry.header, data: append([]byte(nil), entry.data...)}
	}
	return result
}

func findTestEntry(t *testing.T, entries []testTarEntry, name string) int {
	t.Helper()
	for index := range entries {
		if entries[index].header.Name == name {
			return index
		}
	}
	t.Fatalf("test TAR entry %q not found", name)
	return -1
}

func rewriteFixtureArtifact(t *testing.T, fixture *bundleFixture, entries []testTarEntry, trailing []byte) {
	t.Helper()
	artifact := append(buildTestTar(t, entries), trailing...)
	digest := sha256.Sum256(artifact)
	fixture.manifest.ArtifactBytes = int64(len(artifact))
	fixture.manifest.ArtifactSHA256 = hex.EncodeToString(digest[:])
	resignManifest(t, &fixture.manifest)
	if err := os.WriteFile(fixture.artifactPath, artifact, 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestManifest(t, fixture.manifestPath, fixture.manifest)
	fixture.entries = cloneTestEntries(entries)
}

func rewriteFixtureRawArtifact(t *testing.T, fixture *bundleFixture, artifact []byte) {
	t.Helper()
	digest := sha256.Sum256(artifact)
	fixture.manifest.ArtifactBytes = int64(len(artifact))
	fixture.manifest.ArtifactSHA256 = hex.EncodeToString(digest[:])
	resignManifest(t, &fixture.manifest)
	if err := os.WriteFile(fixture.artifactPath, artifact, 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestManifest(t, fixture.manifestPath, fixture.manifest)
}

func writeTestManifest(t *testing.T, path string, manifest testManifest) {
	t.Helper()
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertVerifyFails(t *testing.T, fixture bundleFixture, options Options, wantText string) {
	t.Helper()
	_, err := VerifyBundle(context.Background(), fixture.root, fixture.manifestPath, fixture.artifactPath, options)
	if err == nil {
		t.Fatal("VerifyBundle() unexpectedly succeeded")
	}
	if wantText != "" && !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(wantText)) {
		t.Fatalf("VerifyBundle() error = %v, want text %q", err, wantText)
	}
}

func recomputeTestTarChecksum(header []byte) {
	for index := 148; index < 156; index++ {
		header[index] = ' '
	}
	var sum int64
	for _, value := range header {
		sum += int64(value)
	}
	copy(header[148:156], []byte(fmt.Sprintf("%06o\x00 ", sum)))
}

func testSourceExportID(files []testSourceFile) string {
	hash := sha256.New()
	for _, file := range files {
		_, _ = hash.Write([]byte(file.Path + "\x00"))
		_, _ = hash.Write([]byte(jsonNumber(file.Bytes) + "\x00" + file.SHA256 + "\n"))
	}
	return hex.EncodeToString(hash.Sum(nil))
}
