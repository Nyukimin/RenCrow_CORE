package memory

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConversationArchiveParquetContract(t *testing.T) {
	if ConversationArchiveParquetFormat != "rencrow.archive.parquet.v1" {
		t.Fatalf("format = %q", ConversationArchiveParquetFormat)
	}
	if ConversationArchiveParquetPolicyRevision != "memory-parquet-export/v1" {
		t.Fatalf("policy revision = %q", ConversationArchiveParquetPolicyRevision)
	}
	if UserMemoryOwnerOperationParquetExport != "parquet_export" || UserMemoryOwnerOperationParquetVerify != "parquet_verify" {
		t.Fatalf("operations = %q, %q", UserMemoryOwnerOperationParquetExport, UserMemoryOwnerOperationParquetVerify)
	}

	result := ConversationArchiveParquetExportResult{
		ExportID:        "request-1",
		RunRelPath:      "runs/request-1",
		ManifestRelPath: "runs/request-1/manifest.json",
		ManifestSHA256:  "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Files: []ConversationArchiveParquetFile{
			{RelativePath: "thread_summaries.parquet", RowCount: 1, Bytes: 10, SHA256: "abcdef"},
		},
		Receipt: UserMemoryOwnerReceipt{RequestID: "request-1", Operation: UserMemoryOwnerOperationParquetExport},
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	text := string(encoded)
	for _, forbidden := range []string{"/tmp/", "secret", "raw_rows", "absolute_path"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("metadata response contains forbidden %q: %s", forbidden, text)
		}
	}
}
