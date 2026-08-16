package memory

import "time"

const (
	ConversationArchiveParquetFormat         = "rencrow.archive.parquet.v1"
	ConversationArchiveParquetPolicyRevision = "memory-parquet-export/v1"
	UserMemoryOwnerOperationParquetExport    = "parquet_export"
	UserMemoryOwnerOperationParquetVerify    = "parquet_verify"
)

// Export metadata is deliberately limited to relative artifact names and
// integrity information. Rows and the configured filesystem root never cross
// the owner-domain boundary.
type ConversationArchiveParquetFile struct {
	RelativePath string `json:"relative_path"`
	RowCount     int64  `json:"row_count"`
	Bytes        int64  `json:"bytes"`
	SHA256       string `json:"sha256"`
}

type ConversationArchiveParquetExportResult struct {
	ExportID        string                           `json:"export_id"`
	CreatedAt       time.Time                        `json:"created_at"`
	TotalRows       int64                            `json:"total_rows"`
	RunRelPath      string                           `json:"run_relpath"`
	ManifestRelPath string                           `json:"manifest_relpath"`
	ManifestSHA256  string                           `json:"manifest_sha256"`
	Files           []ConversationArchiveParquetFile `json:"files"`
	Receipt         UserMemoryOwnerReceipt           `json:"receipt"`
}

type ConversationArchiveParquetVerifyResult struct {
	ExportID        string                           `json:"export_id"`
	CreatedAt       time.Time                        `json:"created_at"`
	TotalRows       int64                            `json:"total_rows"`
	RunRelPath      string                           `json:"run_relpath"`
	ManifestRelPath string                           `json:"manifest_relpath"`
	ManifestSHA256  string                           `json:"manifest_sha256"`
	Files           []ConversationArchiveParquetFile `json:"files"`
	Receipt         UserMemoryOwnerReceipt           `json:"receipt"`
}
