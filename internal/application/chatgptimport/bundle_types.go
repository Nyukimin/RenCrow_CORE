package chatgptimport

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

const (
	BundleFormat     = "rencrow.chatgpt_common_raw_bundle.v1"
	RecordSchema     = "rencrow.chatgpt_l3.v1"
	ConverterVersion = "chatgpt-export-memory-go/v2"

	defaultMaxManifestBytes    = int64(64 << 20)
	defaultMaxArtifactBytes    = int64(64 << 30)
	defaultChunkBytes          = int64(32 << 20)
	defaultMaxJSONLineBytes    = int64(64<<20 + 4<<20)
	defaultMaxSourceFiles      = 250000
	defaultMaxSourceFileBytes  = int64(16 << 30)
	defaultMaxSourceTotalBytes = int64(64 << 30)
	maxSourcePathBytes         = 4096
)

var (
	// ErrBounds identifies an otherwise well-formed input that exceeds a
	// configured verification bound.
	ErrBounds = errors.New("ChatGPT bundle exceeds a verification bound")
	// ErrInvalidManifest identifies manifest syntax, schema, or declared-count
	// failures. It is intentionally distinct from ErrInvalidBundle so the
	// public API can return invalid (400) for the former and artifact_invalid
	// (422) for the latter.
	ErrInvalidManifest = errors.New("ChatGPT bundle manifest is invalid")
	// ErrInvalidBundle identifies a bundle whose declared and derived content
	// do not agree. It remains the compatibility sentinel for artifact-invalid
	// TAR, record, object, hash, canonical, and reconstruction failures.
	ErrInvalidBundle = errors.New("ChatGPT bundle is invalid")
)

// Options controls bounded bundle verification. Zero values select the
// production limits.
type Options struct {
	MaxManifestBytes    int64
	MaxArtifactBytes    int64
	ChunkBytes          int64
	MaxJSONLineBytes    int64
	MaxSourceFiles      int
	MaxSourceFileBytes  int64
	MaxSourceTotalBytes int64
}

func (o Options) normalized() (Options, error) {
	if o.MaxManifestBytes < 0 || o.MaxArtifactBytes < 0 || o.ChunkBytes < 0 || o.MaxJSONLineBytes < 0 || o.MaxSourceFiles < 0 || o.MaxSourceFileBytes < 0 || o.MaxSourceTotalBytes < 0 {
		return Options{}, errors.New("verification bounds must not be negative")
	}
	if o.MaxManifestBytes == 0 {
		o.MaxManifestBytes = defaultMaxManifestBytes
	}
	if o.MaxArtifactBytes == 0 {
		o.MaxArtifactBytes = defaultMaxArtifactBytes
	}
	if o.ChunkBytes == 0 {
		o.ChunkBytes = defaultChunkBytes
	}
	if o.MaxJSONLineBytes == 0 {
		o.MaxJSONLineBytes = defaultMaxJSONLineBytes
	}
	if o.MaxSourceFiles == 0 {
		o.MaxSourceFiles = defaultMaxSourceFiles
	}
	if o.MaxSourceFileBytes == 0 {
		o.MaxSourceFileBytes = defaultMaxSourceFileBytes
	}
	if o.MaxSourceTotalBytes == 0 {
		o.MaxSourceTotalBytes = defaultMaxSourceTotalBytes
	}
	return o, nil
}

// Binding is the path-free, immutable identity and aggregate count projection
// of a verified bundle.
type Binding struct {
	Format            string `json:"format"`
	ExportID          string `json:"export_id"`
	ManifestSHA256    string `json:"manifest_sha256"`
	ArtifactSHA256    string `json:"artifact_sha256"`
	ArtifactBytes     int64  `json:"artifact_bytes"`
	SchemaVersion     string `json:"schema_version"`
	ConverterVersion  string `json:"converter_version"`
	SourceFileCount   int    `json:"source_file_count"`
	SourceChunkCount  int    `json:"source_chunk_count"`
	SourceObjectCount int    `json:"source_object_count"`
	ConversationFiles int    `json:"conversation_files"`
	Conversations     int    `json:"conversations"`
	Messages          int    `json:"messages"`
	UserMessages      int    `json:"user_messages"`
	AssistantMessages int    `json:"assistant_messages"`
	Assets            int    `json:"assets"`
}

// VerifiedBundle exposes verified streams without exposing private staging
// paths. Close removes only the verifier-owned extraction directory.
type VerifiedBundle struct {
	mu          sync.RWMutex
	binding     Binding
	root        string
	stage       string
	records     string
	sourceIndex string
	objects     map[string]objectMetadata
	closed      bool
}

type objectMetadata struct {
	path  string
	bytes int64
}

// Binding returns the safe immutable bundle identity and aggregate counts.
func (b *VerifiedBundle) Binding() Binding {
	if b == nil {
		return Binding{}
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.binding
}

// OpenRecords opens the canonical, source-derived message JSONL stream.
func (b *VerifiedBundle) OpenRecords() (io.ReadCloser, error) {
	return b.openVerifiedFile("records", "")
}

// OpenSourceIndex opens the canonical source-files JSONL stream.
func (b *VerifiedBundle) OpenSourceIndex() (io.ReadCloser, error) {
	return b.openVerifiedFile("source-index", "")
}

// OpenObject opens one verified content-addressed source chunk.
func (b *VerifiedBundle) OpenObject(hash string) (io.ReadCloser, error) {
	if !isLowerHex64(hash) {
		return nil, errors.New("object hash must be lowercase SHA-256")
	}
	return b.openVerifiedFile("object", hash)
}

func (b *VerifiedBundle) openVerifiedFile(kind, hash string) (io.ReadCloser, error) {
	if b == nil {
		return nil, errors.New("verified bundle is nil")
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return nil, errors.New("verified bundle is closed")
	}
	path := b.records
	expectedBytes := int64(-1)
	switch kind {
	case "source-index":
		path = b.sourceIndex
	case "object":
		metadata, exists := b.objects[hash]
		if !exists {
			return nil, os.ErrNotExist
		}
		path = metadata.path
		expectedBytes = metadata.bytes
	}
	file, info, err := openPrivateRegular(path)
	if err != nil {
		return nil, err
	}
	if expectedBytes >= 0 && info.Size() != expectedBytes {
		_ = file.Close()
		return nil, errors.New("verified object size changed")
	}
	return file, nil
}

// Close removes the verifier-owned extraction directory. It never removes the
// caller's manifest, artifact, or stage root.
func (b *VerifiedBundle) Close() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	root, stage := b.root, b.stage
	b.stage = ""
	b.mu.Unlock()
	return removeVerifierStage(root, stage)
}

func openPrivateRegular(path string) (*os.File, os.FileInfo, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, nil, errors.New("input must be a regular non-symlink file")
	}
	if runtime.GOOS != "windows" && before.Mode().Perm()&0o077 != 0 {
		return nil, nil, errors.New("input file must be private")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	after, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		_ = file.Close()
		return nil, nil, errors.New("input file identity changed while opening")
	}
	return file, after, nil
}

func resolvePrivateStageRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("stage root is required")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("stage root must be an existing non-symlink directory")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("stage root must be private")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	return filepath.Abs(resolved)
}

func removeVerifierStage(root, stage string) error {
	if root == "" || stage == "" {
		return nil
	}
	relative, err := filepath.Rel(root, stage)
	if err != nil || relative == "." || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || !strings.HasPrefix(filepath.Base(stage), ".chatgpt-bundle-verify-") {
		return errors.New("refusing to remove an unowned verification path")
	}
	return os.RemoveAll(stage)
}
