package threadmigration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

type offlineBuildFixture struct {
	l1Source       string
	archiveSource  string
	topicSource    string
	externalSource string
	outputOne      string
	outputTwo      string
}

func TestBuildOfflineProducesFreshBoundOutputsAndLeavesSourcesUnchanged(t *testing.T) {
	fixture := newOfflineBuildFixture(t)
	sourcesBefore := offlineBuildSourceBytes(t, fixture)

	receipt, err := Build(context.Background(), OfflineBuildOptions{
		L1SourcePath:         fixture.l1Source,
		ArchiveSourcePath:    fixture.archiveSource,
		TopicSourcePath:      fixture.topicSource,
		ExternalSnapshotPath: fixture.externalSource,
		OutputDir:            fixture.outputOne,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if receipt.Status != OfflineBuildStatusReady {
		t.Fatalf("Build() status = %q, want %q", receipt.Status, OfflineBuildStatusReady)
	}
	if receipt.MappingSHA256 == "" || receipt.MappingSHA256 != receipt.L1MappingSHA256 || receipt.MappingSHA256 != receipt.ArchiveMappingSHA256 || receipt.MappingSHA256 != receipt.TopicMappingSHA256 || receipt.MappingSHA256 != receipt.RedisMappingSHA256 || receipt.MappingSHA256 != receipt.QdrantMappingSHA256 {
		t.Fatalf("mapping hash is not shared by every output: %+v", receipt)
	}
	if receipt.SourceInputsStable != 1 {
		t.Fatalf("SourceInputsStable = %d, want 1", receipt.SourceInputsStable)
	}
	if receipt.ErrorCode != "" {
		t.Fatalf("successful receipt error_code = %q", receipt.ErrorCode)
	}
	if err := receipt.Validate(); err != nil {
		t.Fatalf("receipt.Validate() error = %v", err)
	}

	wantNames := []string{
		OfflineBuildL1Filename,
		OfflineBuildArchiveFilename,
		OfflineBuildTopicFilename,
		OfflineBuildTopicQuarantineFilename,
		OfflineBuildRedisFilename,
		OfflineBuildQdrantFilename,
		OfflineBuildMappingFilename,
		OfflineBuildReceiptFilename,
	}
	sort.Strings(wantNames)
	entries, err := os.ReadDir(fixture.outputOne)
	if err != nil {
		t.Fatalf("ReadDir(output) error = %v", err)
	}
	gotNames := make([]string, 0, len(entries))
	for _, entry := range entries {
		gotNames = append(gotNames, entry.Name())
		info, err := entry.Info()
		if err != nil {
			t.Fatalf("Info(%q) error = %v", entry.Name(), err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			t.Fatalf("output %q is not a regular non-symlink file", entry.Name())
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("output %q permissions = %o, want 600", entry.Name(), info.Mode().Perm())
		}
	}
	sort.Strings(gotNames)
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("output names = %#v, want %#v", gotNames, wantNames)
	}

	buildBytes, err := os.ReadFile(filepath.Join(fixture.outputOne, OfflineBuildReceiptFilename))
	if err != nil {
		t.Fatalf("ReadFile(build receipt) error = %v", err)
	}
	serialized, err := json.Marshal(receipt)
	if err != nil {
		t.Fatalf("json.Marshal(receipt) error = %v", err)
	}
	if !bytes.Equal(buildBytes, append(serialized, '\n')) {
		t.Fatalf("build.json is not full receipt bytes: %q", buildBytes)
	}
	var decoded map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buildBytes), &decoded); err != nil {
		t.Fatalf("build.json is not JSON: %v", err)
	}
	if strings.Contains(string(buildBytes), fixture.outputOne) || strings.Contains(string(buildBytes), fixture.l1Source) || strings.Contains(string(buildBytes), fixture.externalSource) {
		t.Fatal("build receipt leaked a filesystem path")
	}

	if sourcesAfter := offlineBuildSourceBytes(t, fixture); !reflect.DeepEqual(sourcesBefore, sourcesAfter) {
		t.Fatal("Build() changed an input source")
	}
}

func TestBuildOfflineRejectsDuplicateSourceFiles(t *testing.T) {
	fixture := newOfflineBuildFixture(t)
	receipt, err := Build(context.Background(), OfflineBuildOptions{
		L1SourcePath:         fixture.l1Source,
		ArchiveSourcePath:    fixture.l1Source,
		TopicSourcePath:      fixture.topicSource,
		ExternalSnapshotPath: fixture.externalSource,
		OutputDir:            fixture.outputOne,
	})
	if err == nil {
		t.Fatal("Build() accepted duplicate source files")
	}
	if receipt.Status != OfflineBuildStatusBlocked || receipt.ErrorCode != "duplicate_source" {
		t.Fatalf("duplicate-source receipt = %+v, want blocked duplicate_source", receipt)
	}
	entries, err := os.ReadDir(fixture.outputOne)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("duplicate-source preflight created output artifacts: %#v", entries)
	}
}

func TestOfflineBuildReceiptRejectsPreparationAndDimensionTampering(t *testing.T) {
	fixture := newOfflineBuildFixture(t)
	receipt, err := Build(context.Background(), OfflineBuildOptions{
		L1SourcePath: fixture.l1Source, ArchiveSourcePath: fixture.archiveSource,
		TopicSourcePath: fixture.topicSource, ExternalSnapshotPath: fixture.externalSource,
		OutputDir: fixture.outputOne,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*OfflineBuildReceipt)
	}{
		{name: "redis preparation receipt", mutate: func(value *OfflineBuildReceipt) {
			value.RedisPreparationReceiptSHA256 = strings.Repeat("0", 64)
		}},
		{name: "qdrant preparation receipt", mutate: func(value *OfflineBuildReceipt) {
			value.QdrantPreparationReceiptSHA256 = strings.Repeat("0", 64)
		}},
		{name: "negative qdrant dimension", mutate: func(value *OfflineBuildReceipt) {
			value.QdrantVectorDimension = -1
		}},
		{name: "missing qdrant dimension", mutate: func(value *OfflineBuildReceipt) {
			value.QdrantOutputCount = 1
			value.QdrantVectorDimension = 0
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tampered := receipt
			test.mutate(&tampered)
			if err := tampered.Validate(); err == nil {
				t.Fatal("tampered receipt passed Validate()")
			}
		})
	}
}

func TestOfflineBuildReceiptBindsPreparationReceiptsToCohort(t *testing.T) {
	redisReceiptHash := strings.Repeat("a", 64)
	qdrantReceiptHash := strings.Repeat("b", 64)
	receipt := OfflineBuildReceipt{
		RedisPreparationReceiptSHA256:  redisReceiptHash,
		QdrantPreparationReceiptSHA256: qdrantReceiptHash,
		QdrantVectorDimension:          3,
	}
	cohort := FullCohortResult{
		RedisPreparation: RedisPreparationResult{Receipt: RedisPreparationReceipt{ReceiptSHA256: redisReceiptHash}},
		QdrantPreparation: QdrantPreparationResult{Receipt: QdrantPreparationReceipt{
			ReceiptSHA256: qdrantReceiptHash, VectorDimension: 3,
		}},
	}
	if err := validateOfflineBuildReceiptAgainstCohort(receipt, cohort); err != nil {
		t.Fatalf("valid cohort bindings rejected: %v", err)
	}

	tampered := receipt
	tampered.RedisPreparationReceiptSHA256 = strings.Repeat("c", 64)
	if err := validateOfflineBuildReceiptAgainstCohort(tampered, cohort); err == nil {
		t.Fatal("tampered Redis preparation binding was accepted")
	}
	tampered = receipt
	tampered.QdrantPreparationReceiptSHA256 = strings.Repeat("d", 64)
	if err := validateOfflineBuildReceiptAgainstCohort(tampered, cohort); err == nil {
		t.Fatal("tampered Qdrant preparation binding was accepted")
	}
	tampered = receipt
	tampered.QdrantVectorDimension = 4
	if err := validateOfflineBuildReceiptAgainstCohort(tampered, cohort); err == nil {
		t.Fatal("tampered Qdrant dimension binding was accepted")
	}
}

func TestBuildOfflineIsDeterministicAcrossFreshOutputDirectories(t *testing.T) {
	fixture := newOfflineBuildFixture(t)
	first, err := Build(context.Background(), OfflineBuildOptions{
		L1SourcePath: fixture.l1Source, ArchiveSourcePath: fixture.archiveSource,
		TopicSourcePath: fixture.topicSource, ExternalSnapshotPath: fixture.externalSource,
		OutputDir: fixture.outputOne,
	})
	if err != nil {
		t.Fatalf("first Build() error = %v", err)
	}
	second, err := Build(context.Background(), OfflineBuildOptions{
		L1SourcePath: fixture.l1Source, ArchiveSourcePath: fixture.archiveSource,
		TopicSourcePath: fixture.topicSource, ExternalSnapshotPath: fixture.externalSource,
		OutputDir: fixture.outputTwo,
	})
	if err != nil {
		t.Fatalf("second Build() error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("same cohort produced different receipts:\nfirst=%+v\nsecond=%+v", first, second)
	}
	for _, name := range []string{
		OfflineBuildL1Filename, OfflineBuildArchiveFilename, OfflineBuildTopicFilename,
		OfflineBuildTopicQuarantineFilename, OfflineBuildRedisFilename,
		OfflineBuildQdrantFilename, OfflineBuildMappingFilename, OfflineBuildReceiptFilename,
	} {
		left, err := os.ReadFile(filepath.Join(fixture.outputOne, name))
		if err != nil {
			t.Fatalf("read first %q: %v", name, err)
		}
		right, err := os.ReadFile(filepath.Join(fixture.outputTwo, name))
		if err != nil {
			t.Fatalf("read second %q: %v", name, err)
		}
		if !bytes.Equal(left, right) {
			t.Fatalf("output %q is not deterministic", name)
		}
	}
}

func TestBuildOfflineRejectsNonFreshOrUnsafeOutputDirectory(t *testing.T) {
	fixture := newOfflineBuildFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.outputOne, "existing"), []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	receipt, err := Build(context.Background(), OfflineBuildOptions{
		L1SourcePath: fixture.l1Source, ArchiveSourcePath: fixture.archiveSource,
		TopicSourcePath: fixture.topicSource, ExternalSnapshotPath: fixture.externalSource,
		OutputDir: fixture.outputOne,
	})
	if err == nil {
		t.Fatal("Build() accepted a non-empty output directory")
	}
	if receipt.Status != OfflineBuildStatusBlocked || receipt.Status == OfflineBuildStatusReady || receipt.ErrorCode == "" {
		t.Fatalf("blocked receipt = %+v", receipt)
	}
	entries, err := os.ReadDir(fixture.outputOne)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "existing" {
		t.Fatalf("non-empty output directory was mutated: %#v", entries)
	}

	if runtime.GOOS != "windows" {
		fresh := filepath.Join(filepath.Dir(fixture.outputOne), "unsafe-mode")
		if err := os.Mkdir(fresh, 0o750); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(fresh) })
		receipt, err := Build(context.Background(), OfflineBuildOptions{
			L1SourcePath: fixture.l1Source, ArchiveSourcePath: fixture.archiveSource,
			TopicSourcePath: fixture.topicSource, ExternalSnapshotPath: fixture.externalSource,
			OutputDir: fresh,
		})
		if err == nil || receipt.Status != OfflineBuildStatusBlocked {
			t.Fatalf("Build() accepted non-owner-only output directory: receipt=%+v err=%v", receipt, err)
		}
	}
}

func TestBuildOfflineRejectsMalformedExternalSnapshot(t *testing.T) {
	fixture := newOfflineBuildFixture(t)
	if err := os.WriteFile(fixture.externalSource, []byte(`{"schema_version":"wrong"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	receipt, err := Build(context.Background(), OfflineBuildOptions{
		L1SourcePath: fixture.l1Source, ArchiveSourcePath: fixture.archiveSource,
		TopicSourcePath: fixture.topicSource, ExternalSnapshotPath: fixture.externalSource,
		OutputDir: fixture.outputOne,
	})
	if err == nil || receipt.Status != OfflineBuildStatusBlocked || receipt.ErrorCode == "" {
		t.Fatalf("malformed external snapshot was accepted: receipt=%+v err=%v", receipt, err)
	}
	entries, err := os.ReadDir(fixture.outputOne)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != OfflineBuildReceiptFilename {
		t.Fatalf("blocked build left unexpected output artifacts: %#v", entries)
	}
}

func newOfflineBuildFixture(t *testing.T) offlineBuildFixture {
	t.Helper()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(workingDirectory, "..", "..", "Tmp", "test-runtime", "identity-step05-sol", "offline-build")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(root, "case-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })

	memory := newSQLiteInventoryFixture(t)
	l1Source := filepath.Join(directory, "l1-source.sqlite")
	archiveSource := filepath.Join(directory, "archive-source.sqlite")
	writeOfflineSQLiteSource(t, l1Source, func(db *sql.DB) {
		createLegacyL1Schema(t, db)
		cloneLegacyL1Rows(t, memory.l1, db)
		execInventory(t, db, `CREATE TABLE l1_raw_record (source_record_id TEXT, source_type TEXT, thread_id TEXT)`)
		rows, err := memory.l1.Query(`SELECT source_record_id, source_type, thread_id FROM l1_raw_record`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var sourceRecordID, sourceType, threadID string
			if err := rows.Scan(&sourceRecordID, &sourceType, &threadID); err != nil {
				t.Fatal(err)
			}
			execInventory(t, db, `INSERT INTO l1_raw_record (source_record_id, source_type, thread_id) VALUES (?, ?, ?)`, sourceRecordID, sourceType, threadID)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
	})
	writeOfflineSQLiteSource(t, archiveSource, func(db *sql.DB) {
		createLegacyArchiveSchema(t, db)
		cloneLegacyArchiveRows(t, memory.archive, db)
	})
	topicSource := filepath.Join(directory, "topics.jsonl")
	if err := os.WriteFile(topicSource, []byte(`{"session_id":"offline-topic","summary":"topic"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	externalSource := filepath.Join(directory, "external-snapshot.json")
	snapshot, err := NewExternalSnapshot(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteExternalSnapshotFresh(externalSource, snapshot); err != nil {
		t.Fatal(err)
	}
	outputOne := filepath.Join(directory, "output-one")
	outputTwo := filepath.Join(directory, "output-two")
	if err := os.Mkdir(outputOne, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outputTwo, 0o700); err != nil {
		t.Fatal(err)
	}
	return offlineBuildFixture{l1Source: l1Source, archiveSource: archiveSource, topicSource: topicSource, externalSource: externalSource, outputOne: outputOne, outputTwo: outputTwo}
}

func writeOfflineSQLiteSource(t *testing.T, path string, populate func(*sql.DB)) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	populate(db)
	if _, err := db.Exec(`PRAGMA journal_mode = DELETE`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func offlineBuildSourceBytes(t *testing.T, fixture offlineBuildFixture) map[string][]byte {
	t.Helper()
	result := make(map[string][]byte)
	for name, path := range map[string]string{
		"l1": fixture.l1Source, "archive": fixture.archiveSource,
		"topic": fixture.topicSource, "external": fixture.externalSource,
	} {
		value, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		result[name] = value
	}
	return result
}
