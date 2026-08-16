package chatgptimport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type sourceFileMetadata struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type bundleManifest struct {
	Format            string               `json:"format"`
	ExportID          string               `json:"export_id"`
	GeneratedAt       string               `json:"generated_at"`
	Files             []sourceFileMetadata `json:"files"`
	ConversationFiles int                  `json:"conversation_files"`
	Conversations     int                  `json:"conversations"`
	Messages          int                  `json:"messages"`
	UserMessages      int                  `json:"user_messages"`
	AssistantMessages int                  `json:"assistant_messages"`
	Assets            int                  `json:"assets"`
	ArtifactSHA256    string               `json:"artifact_sha256"`
	SchemaVersion     string               `json:"schema_version"`
	ConverterVersion  string               `json:"converter_version"`
	ArtifactBytes     int64                `json:"artifact_bytes"`
	ManifestSHA256    string               `json:"manifest_sha256"`
	SourceFileCount   int                  `json:"source_file_count"`
	SourceChunkCount  int                  `json:"source_chunk_count"`
	SourceObjectCount int                  `json:"source_object_count"`
}

type canonicalManifest struct {
	Format            string               `json:"format"`
	ExportID          string               `json:"export_id"`
	Files             []sourceFileMetadata `json:"files"`
	ConversationFiles int                  `json:"conversation_files"`
	Conversations     int                  `json:"conversations"`
	Messages          int                  `json:"messages"`
	UserMessages      int                  `json:"user_messages"`
	AssistantMessages int                  `json:"assistant_messages"`
	Assets            int                  `json:"assets"`
	ArtifactSHA256    string               `json:"artifact_sha256"`
	SchemaVersion     string               `json:"schema_version"`
	ConverterVersion  string               `json:"converter_version"`
	ArtifactBytes     int64                `json:"artifact_bytes"`
	SourceFileCount   int                  `json:"source_file_count"`
	SourceChunkCount  int                  `json:"source_chunk_count"`
	SourceObjectCount int                  `json:"source_object_count"`
}

var manifestFields = []string{
	"format", "export_id", "generated_at", "files", "conversation_files", "conversations", "messages",
	"user_messages", "assistant_messages", "assets", "artifact_sha256", "schema_version", "converter_version",
	"artifact_bytes", "manifest_sha256", "source_file_count", "source_chunk_count", "source_object_count",
}

func readManifest(ctx context.Context, manifestPath string, options Options) (bundleManifest, error) {
	if err := ctx.Err(); err != nil {
		return bundleManifest{}, err
	}
	file, info, err := openPrivateRegular(manifestPath)
	if err != nil {
		return bundleManifest{}, err
	}
	defer file.Close()
	if info.Size() > options.MaxManifestBytes {
		return bundleManifest{}, fmt.Errorf("%w: manifest is larger than %d bytes", ErrBounds, options.MaxManifestBytes)
	}
	data, err := io.ReadAll(io.LimitReader(&contextReader{ctx: ctx, reader: file}, options.MaxManifestBytes+1))
	if err != nil {
		return bundleManifest{}, err
	}
	if int64(len(data)) > options.MaxManifestBytes {
		return bundleManifest{}, fmt.Errorf("%w: manifest is larger than %d bytes", ErrBounds, options.MaxManifestBytes)
	}
	if int64(len(data)) != info.Size() {
		return bundleManifest{}, fmt.Errorf("%w: manifest size changed while reading", errChatGPTImportSourceChanged)
	}
	value, err := decodeManifest(data, options)
	if err != nil {
		if errors.Is(err, ErrBounds) {
			return bundleManifest{}, err
		}
		return bundleManifest{}, fmt.Errorf("%w: %w", ErrInvalidManifest, err)
	}
	return value, nil
}

func decodeManifest(data []byte, options Options) (bundleManifest, error) {
	if err := validateJSONObject(data, manifestFields, manifestFields); err != nil {
		return bundleManifest{}, err
	}
	var raw struct {
		Format            string            `json:"format"`
		ExportID          string            `json:"export_id"`
		GeneratedAt       string            `json:"generated_at"`
		Files             []json.RawMessage `json:"files"`
		ConversationFiles int               `json:"conversation_files"`
		Conversations     int               `json:"conversations"`
		Messages          int               `json:"messages"`
		UserMessages      int               `json:"user_messages"`
		AssistantMessages int               `json:"assistant_messages"`
		Assets            int               `json:"assets"`
		ArtifactSHA256    string            `json:"artifact_sha256"`
		SchemaVersion     string            `json:"schema_version"`
		ConverterVersion  string            `json:"converter_version"`
		ArtifactBytes     int64             `json:"artifact_bytes"`
		ManifestSHA256    string            `json:"manifest_sha256"`
		SourceFileCount   int               `json:"source_file_count"`
		SourceChunkCount  int               `json:"source_chunk_count"`
		SourceObjectCount int               `json:"source_object_count"`
	}
	if err := decodeStrictJSON(data, &raw); err != nil {
		return bundleManifest{}, err
	}
	if raw.SourceFileCount > options.MaxSourceFiles || len(raw.Files) > options.MaxSourceFiles {
		return bundleManifest{}, fmt.Errorf("%w: manifest exceeds source file count bound", ErrBounds)
	}
	value := bundleManifest{
		Format: raw.Format, ExportID: raw.ExportID, GeneratedAt: raw.GeneratedAt,
		ConversationFiles: raw.ConversationFiles, Conversations: raw.Conversations, Messages: raw.Messages,
		UserMessages: raw.UserMessages, AssistantMessages: raw.AssistantMessages, Assets: raw.Assets,
		ArtifactSHA256: raw.ArtifactSHA256, SchemaVersion: raw.SchemaVersion, ConverterVersion: raw.ConverterVersion,
		ArtifactBytes: raw.ArtifactBytes, ManifestSHA256: raw.ManifestSHA256, SourceFileCount: raw.SourceFileCount,
		SourceChunkCount: raw.SourceChunkCount, SourceObjectCount: raw.SourceObjectCount,
	}
	var sourceTotalBytes int64
	for _, encodedFile := range raw.Files {
		if err := validateJSONObject(encodedFile, []string{"path", "bytes", "sha256"}, []string{"path", "bytes", "sha256"}); err != nil {
			return bundleManifest{}, err
		}
		var file sourceFileMetadata
		if err := decodeStrictJSON(encodedFile, &file); err != nil {
			return bundleManifest{}, err
		}
		if file.Bytes > options.MaxSourceFileBytes {
			return bundleManifest{}, fmt.Errorf("%w: manifest source file exceeds per-file bound", ErrBounds)
		}
		if file.Bytes < 0 {
			return bundleManifest{}, errors.New("manifest source file size is negative")
		}
		if sourceTotalBytes > options.MaxSourceTotalBytes-file.Bytes {
			return bundleManifest{}, fmt.Errorf("%w: manifest exceeds total source bytes bound", ErrBounds)
		}
		sourceTotalBytes += file.Bytes
		value.Files = append(value.Files, file)
	}
	if err := validateManifest(value); err != nil {
		return bundleManifest{}, err
	}
	return value, nil
}

func validateManifest(value bundleManifest) error {
	if value.Format != BundleFormat || value.SchemaVersion != RecordSchema || value.ConverterVersion != ConverterVersion {
		return errors.New("manifest format, schema, or converter version is unsupported")
	}
	if !isLowerHex64(value.ExportID) || !isLowerHex64(value.ArtifactSHA256) || !isLowerHex64(value.ManifestSHA256) {
		return errors.New("manifest contains an invalid lowercase SHA-256 value")
	}
	if _, err := time.Parse(time.RFC3339Nano, value.GeneratedAt); value.GeneratedAt == "" || err != nil {
		return errors.New("manifest generated_at is invalid")
	}
	if value.ArtifactBytes <= 0 || value.SourceFileCount <= 0 || value.SourceFileCount != len(value.Files) {
		return errors.New("manifest source or artifact counts are invalid")
	}
	if value.SourceChunkCount < 0 || value.SourceObjectCount < 0 || value.ConversationFiles < 0 || value.Conversations < 0 || value.Messages <= 0 || value.UserMessages < 0 || value.AssistantMessages < 0 || value.Assets < 0 {
		return errors.New("manifest counts are invalid")
	}
	previous := ""
	for _, file := range value.Files {
		if err := validateSourcePath(file.Path); err != nil {
			return err
		}
		if previous != "" && previous >= file.Path {
			return errors.New("manifest files are not strictly path-sorted")
		}
		previous = file.Path
		if file.Bytes < 0 || !isLowerHex64(file.SHA256) {
			return errors.New("manifest source file metadata is invalid")
		}
	}
	if sourceExportID(value.Files) != value.ExportID {
		return errors.New("manifest export_id does not match source metadata")
	}
	conversationFiles, assets := 0, 0
	for _, file := range value.Files {
		if conversationFilePattern.MatchString(path.Base(file.Path)) {
			conversationFiles++
		} else if !strings.HasSuffix(file.Path, ".json") && !strings.HasSuffix(file.Path, ".html") {
			assets++
		}
	}
	if conversationFiles != value.ConversationFiles || assets != value.Assets {
		return errors.New("manifest conversation or asset counts do not match source files")
	}
	hash, err := canonicalManifestHash(value)
	if err != nil {
		return err
	}
	if hash != value.ManifestSHA256 {
		return errors.New("manifest_sha256 does not match canonical manifest")
	}
	return nil
}

func canonicalManifestHash(value bundleManifest) (string, error) {
	files := append([]sourceFileMetadata(nil), value.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	canonical := canonicalManifest{
		Format: value.Format, ExportID: value.ExportID, Files: files,
		ConversationFiles: value.ConversationFiles, Conversations: value.Conversations, Messages: value.Messages,
		UserMessages: value.UserMessages, AssistantMessages: value.AssistantMessages, Assets: value.Assets,
		ArtifactSHA256: value.ArtifactSHA256, SchemaVersion: value.SchemaVersion, ConverterVersion: value.ConverterVersion,
		ArtifactBytes: value.ArtifactBytes, SourceFileCount: value.SourceFileCount, SourceChunkCount: value.SourceChunkCount,
		SourceObjectCount: value.SourceObjectCount,
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func (value bundleManifest) binding() Binding {
	return Binding{
		Format: value.Format, ExportID: value.ExportID, ManifestSHA256: value.ManifestSHA256,
		ArtifactSHA256: value.ArtifactSHA256, ArtifactBytes: value.ArtifactBytes,
		SchemaVersion: value.SchemaVersion, ConverterVersion: value.ConverterVersion,
		SourceFileCount: value.SourceFileCount, SourceChunkCount: value.SourceChunkCount,
		SourceObjectCount: value.SourceObjectCount, ConversationFiles: value.ConversationFiles,
		Conversations: value.Conversations, Messages: value.Messages, UserMessages: value.UserMessages,
		AssistantMessages: value.AssistantMessages, Assets: value.Assets,
	}
}

func sourceExportID(files []sourceFileMetadata) string {
	hash := sha256.New()
	for _, file := range files {
		_, _ = fmt.Fprintf(hash, "%s\x00%d\x00%s\n", file.Path, file.Bytes, file.SHA256)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func validateSourcePath(value string) error {
	if value == "" || !utf8.ValidString(value) || len(value) > maxSourcePathBytes {
		return errors.New("source path is empty, invalid UTF-8, or too long")
	}
	if strings.ContainsAny(value, "\\\x00") || strings.HasPrefix(value, "/") || path.IsAbs(value) {
		return errors.New("source path is not a safe relative path")
	}
	if len(value) >= 2 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) && value[1] == ':' {
		return errors.New("source path contains a volume")
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return errors.New("source path is not normalized")
		}
	}
	if path.Clean(value) != value {
		return errors.New("source path is not normalized")
	}
	return nil
}

func isLowerHex64(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

func validateJSONObject(data []byte, allowed, required []string) error {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, field := range allowed {
		allowedSet[field] = struct{}{}
	}
	for field := range object {
		if _, exists := allowedSet[field]; !exists {
			return fmt.Errorf("unknown JSON field %q", field)
		}
	}
	for _, field := range required {
		if raw, exists := object[field]; !exists || len(raw) == 0 {
			return fmt.Errorf("missing required JSON field %q", field)
		}
	}
	return nil
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON contains trailing data")
		}
		return err
	}
	return nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	if token, err := decoder.Token(); err == nil {
		return fmt.Errorf("JSON contains trailing token %v", token)
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate JSON field %q", key)
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("JSON object is not closed")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("JSON array is not closed")
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}
