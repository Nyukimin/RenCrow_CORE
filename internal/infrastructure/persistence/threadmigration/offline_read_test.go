package threadmigration

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReadOfflineBuildStrictReturnsValidatedCanonicalProjections(t *testing.T) {
	fixture, receipt := buildOfflineReadFixture(t)
	artifacts, err := ReadOfflineBuildStrict(context.Background(), fixture.outputOne)
	if err != nil {
		t.Fatalf("ReadOfflineBuildStrict() error = %v", err)
	}
	if artifacts.Receipt.ReceiptSHA256 != receipt.ReceiptSHA256 || artifacts.Plan.MappingSHA256 != receipt.MappingSHA256 {
		t.Fatalf("read receipt/plan mismatch: %+v", artifacts)
	}
	if err := artifacts.Redis.Validate(); err != nil {
		t.Fatalf("Redis.Validate() error = %v", err)
	}
	if err := artifacts.Qdrant.Validate(); err != nil {
		t.Fatalf("Qdrant.Validate() error = %v", err)
	}
	if artifacts.Redis.Receipt.OutputSHA256 != receipt.RedisOutputSHA256 || artifacts.Qdrant.Receipt.OutputSHA256 != receipt.QdrantOutputSHA256 || artifacts.Qdrant.Receipt.VectorDimension != receipt.QdrantVectorDimension {
		t.Fatalf("canonical projection binding mismatch: %+v", artifacts)
	}
}

func TestReadOfflineBuildStrictRejectsTamperedArtifact(t *testing.T) {
	fixture, _ := buildOfflineReadFixture(t)
	path := filepath.Join(fixture.outputOne, OfflineBuildRedisFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] = ' '
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadOfflineBuildStrict(context.Background(), fixture.outputOne); err == nil {
		t.Fatal("tampered Redis artifact was accepted")
	}
}

func TestReadOfflineBuildStrictRejectsMalformedReceiptJSON(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(string) string
	}{
		{name: "duplicate", mutate: func(raw string) string {
			return strings.Replace(raw, `{"schema_version":`, `{"schema_version":"duplicate","schema_version":`, 1)
		}},
		{name: "unknown", mutate: func(raw string) string {
			return strings.Replace(raw, `{"schema_version":`, `{"unknown":true,"schema_version":`, 1)
		}},
		{name: "trailing", mutate: func(raw string) string { return raw + `{}` }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, _ := buildOfflineReadFixture(t)
			path := filepath.Join(fixture.outputOne, OfflineBuildReceiptFilename)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(test.mutate(string(data))), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadOfflineBuildStrict(context.Background(), fixture.outputOne); err == nil {
				t.Fatalf("%s build receipt was accepted", test.name)
			}
		})
	}
}

func TestReadOfflineBuildStrictRejectsMalformedProjectionJSON(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		content  string
	}{
		{name: "Redis duplicate member", filename: OfflineBuildRedisFilename, content: `[{"key":"a","key":"b","value":"e30=","expire_at_unix_milli":1}]`},
		{name: "Qdrant duplicate member", filename: OfflineBuildQdrantFilename, content: `[{"point_id":"a","point_id":"b","vector":[1],"payload":{}}]`},
		{name: "Redis null", filename: OfflineBuildRedisFilename, content: `null`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, _ := buildOfflineReadFixture(t)
			if err := os.WriteFile(filepath.Join(fixture.outputOne, test.filename), []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadOfflineBuildStrict(context.Background(), fixture.outputOne); err == nil {
				t.Fatalf("%s was accepted", test.name)
			}
		})
	}
}

func TestReadOfflineBuildStrictRejectsUnexpectedEntryOrUnsafeMode(t *testing.T) {
	t.Run("unexpected entry", func(t *testing.T) {
		fixture, _ := buildOfflineReadFixture(t)
		if err := os.WriteFile(filepath.Join(fixture.outputOne, "unexpected"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadOfflineBuildStrict(context.Background(), fixture.outputOne); err == nil {
			t.Fatal("unexpected output entry was accepted")
		}
	})
	if runtime.GOOS != "windows" {
		t.Run("unsafe mode", func(t *testing.T) {
			fixture, _ := buildOfflineReadFixture(t)
			path := filepath.Join(fixture.outputOne, OfflineBuildMappingFilename)
			if err := os.Chmod(path, 0o640); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadOfflineBuildStrict(context.Background(), fixture.outputOne); err == nil {
				t.Fatal("group-readable artifact was accepted")
			}
		})
	}
}

func buildOfflineReadFixture(t *testing.T) (offlineBuildFixture, BuildReceipt) {
	t.Helper()
	fixture := newOfflineBuildFixture(t)
	receipt, err := Build(context.Background(), BuildOptions{
		L1SourcePath: fixture.l1Source, ArchiveSourcePath: fixture.archiveSource,
		TopicSourcePath: fixture.topicSource, ExternalSnapshotPath: fixture.externalSource,
		OutputDir: fixture.outputOne,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	return fixture, receipt
}
