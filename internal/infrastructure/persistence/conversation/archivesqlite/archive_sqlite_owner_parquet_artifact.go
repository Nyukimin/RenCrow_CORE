package archivesqlite

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"github.com/parquet-go/parquet-go"
)

const (
	parquetManifestName  = "manifest.json"
	parquetRunDir        = "runs"
	parquetStagingDir    = ".staging"
	parquetQuarantineDir = ".quarantine"
)

type archiveParquetManifest struct {
	Format    string                        `json:"format"`
	ExportID  string                        `json:"export_id"`
	CreatedAt string                        `json:"created_at"`
	TotalRows int64                         `json:"total_rows"`
	Files     []archiveParquetManifestEntry `json:"files"`
}

type archiveParquetManifestEntry struct {
	RelativePath string `json:"relative_path"`
	RowCount     int64  `json:"row_count"`
	Bytes        int64  `json:"bytes"`
	SHA256       string `json:"sha256"`
}

func verifyStoredParquetArtifacts(root string, stored parquetReceiptRow, exportID string) (archiveParquetManifest, []domainmemory.ConversationArchiveParquetFile, error) {
	if !isSafeRelativePath(stored.RunRelPath) || filepath.ToSlash(filepath.Clean(filepath.FromSlash(stored.RunRelPath))) != stored.RunRelPath {
		return archiveParquetManifest{}, nil, errors.New("unsafe stored parquet run path")
	}
	runDir, err := containedParquetPath(root, stored.RunRelPath)
	if err != nil {
		return archiveParquetManifest{}, nil, err
	}
	manifestPath := filepath.Join(runDir, parquetManifestName)
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil || manifestInfo.Mode()&os.ModeSymlink != 0 || !manifestInfo.Mode().IsRegular() {
		return archiveParquetManifest{}, nil, errors.New("manifest is not a regular file")
	}
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return archiveParquetManifest{}, nil, err
	}
	manifestSHA := strings.ToLower(stored.ManifestSHA256)
	if stored.ManifestSHA256 != manifestSHA || len(manifestSHA) != 64 || !isHex(manifestSHA) || sha256Bytes(manifestBytes) != manifestSHA {
		return archiveParquetManifest{}, nil, errors.New("manifest hash mismatch")
	}
	manifest, err := decodeStrictParquetManifest(manifestBytes)
	if err != nil || manifest.ExportID != exportID || manifest.Format != domainmemory.ConversationArchiveParquetFormat {
		if err == nil {
			err = errors.New("manifest identity mismatch")
		}
		return archiveParquetManifest{}, nil, err
	}
	if err := verifyParquetRunLayout(runDir, manifest); err != nil {
		return archiveParquetManifest{}, nil, err
	}
	verifiedFiles := make([]domainmemory.ConversationArchiveParquetFile, 0, len(manifest.Files))
	for _, entry := range manifest.Files {
		path, err := containedParquetPath(runDir, entry.RelativePath)
		if err != nil {
			return archiveParquetManifest{}, nil, err
		}
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() != entry.Bytes {
			return archiveParquetManifest{}, nil, errors.New("parquet file metadata mismatch")
		}
		got, err := sha256File(path)
		if err != nil || got != entry.SHA256 {
			return archiveParquetManifest{}, nil, errors.New("parquet file hash mismatch")
		}
		file, err := os.Open(path)
		if err != nil {
			return archiveParquetManifest{}, nil, err
		}
		reader, openErr := parquet.OpenFile(file, info.Size())
		rows := int64(-1)
		if openErr == nil {
			rows = reader.NumRows()
		}
		_ = file.Close()
		if openErr != nil || rows != entry.RowCount {
			return archiveParquetManifest{}, nil, errors.New("parquet row count mismatch")
		}
		verifiedFiles = append(verifiedFiles, domainmemory.ConversationArchiveParquetFile(entry))
	}
	return manifest, verifiedFiles, nil
}

func validateParquetReplayResult(replay domainmemory.ConversationArchiveParquetExportResult, req l1sqlite.OwnerParquetExportRequest, stored parquetReceiptRow, manifest archiveParquetManifest, files []domainmemory.ConversationArchiveParquetFile) error {
	createdAt, err := time.Parse(time.RFC3339Nano, manifest.CreatedAt)
	if err != nil {
		return err
	}
	manifestSHA := strings.ToLower(stored.ManifestSHA256)
	if stored.ManifestSHA256 != manifestSHA || !isHex(manifestSHA) || len(manifestSHA) != 64 {
		return errors.New("stored manifest hash is not canonical")
	}
	wantRunRelPath := filepath.ToSlash(filepath.Join(parquetRunDir, req.RequestID))
	wantManifestRelPath := filepath.ToSlash(filepath.Join(wantRunRelPath, parquetManifestName))
	if replay.ExportID != req.RequestID || replay.CreatedAt.UTC() != createdAt.UTC() || replay.TotalRows != manifest.TotalRows ||
		replay.RunRelPath != stored.RunRelPath || replay.RunRelPath != wantRunRelPath || replay.ManifestRelPath != wantManifestRelPath ||
		replay.ManifestSHA256 != manifestSHA || replay.Receipt.RequestID != req.RequestID || replay.Receipt.Operation != domainmemory.UserMemoryOwnerOperationParquetExport ||
		replay.Receipt.Status != "completed" || replay.Receipt.OwnerRoute != "conversation_archive/parquet/export" ||
		replay.Receipt.PolicyRevision != domainmemory.ConversationArchiveParquetPolicyRevision || replay.Receipt.IdempotencyKey != req.RequestID ||
		replay.Receipt.IdempotentReplay || replay.Receipt.InputCount != int(manifest.TotalRows) || replay.Receipt.OutputCount != 5 ||
		replay.Receipt.AuditReference != req.RequestID || replay.Receipt.CompletedAt.UTC() != createdAt.UTC() || len(replay.Receipt.Warnings) != 0 {
		return errors.New("stored parquet replay result binding mismatch")
	}
	if len(replay.Files) != len(files) {
		return errors.New("stored parquet replay file metadata mismatch")
	}
	for index := range files {
		if replay.Files[index] != files[index] {
			return errors.New("stored parquet replay file metadata mismatch")
		}
	}
	return nil
}

func cleanParquetRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" || !filepath.IsAbs(root) {
		return "", errors.New("parquet export root must be a non-empty absolute path")
	}
	clean := filepath.Clean(root)
	if clean == "." || clean == string(filepath.Separator) {
		return "", errors.New("parquet export root is invalid")
	}
	if err := rejectSymlinkComponents(clean); err != nil {
		return "", err
	}
	return clean, nil
}

func rejectSymlinkComponents(path string) error {
	clean := filepath.Clean(path)
	volume := filepath.VolumeName(clean)
	rest := strings.TrimPrefix(clean, volume)
	if filepath.IsAbs(rest) {
		rest = strings.TrimPrefix(rest, string(filepath.Separator))
	}
	current := volume + string(filepath.Separator)
	if volume == "" && !filepath.IsAbs(clean) {
		current = ""
	}
	for _, part := range strings.FieldsFunc(rest, func(r rune) bool { return r == '/' || r == '\\' }) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink path component: %s", current)
		}
	}
	return nil
}

type parquetExportPaths struct {
	root          string
	stagingParent string
	staging       string
	runsDir       string
	finalDir      string
}

func prepareParquetExportPaths(root, requestID string) (parquetExportPaths, error) {
	paths := parquetExportPaths{
		root:          root,
		stagingParent: filepath.Join(root, parquetStagingDir),
		staging:       filepath.Join(root, parquetStagingDir, requestID+".partial"),
		runsDir:       filepath.Join(root, parquetRunDir),
		finalDir:      filepath.Join(root, parquetRunDir, requestID),
	}
	if err := ensurePrivateDir(root); err != nil {
		return parquetExportPaths{}, err
	}
	if err := ensurePrivateDir(paths.stagingParent); err != nil {
		return parquetExportPaths{}, err
	}
	if info, err := os.Lstat(paths.staging); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return parquetExportPaths{}, errors.New("unsafe existing parquet staging artifact")
		}
		if err := os.RemoveAll(paths.staging); err != nil {
			return parquetExportPaths{}, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return parquetExportPaths{}, err
	}
	if err := ensurePrivateDir(paths.staging); err != nil {
		return parquetExportPaths{}, err
	}
	return paths, nil
}

func ensurePrivateDir(path string) error {
	if err := rejectSymlinkComponents(path); err != nil {
		return err
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("path is not a regular directory: %s", path)
	}
	return os.Chmod(path, 0o700)
}

func writePrivateFile(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func removeDerivedEntry(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode().IsRegular() {
		return os.Remove(path)
	}
	if !info.IsDir() {
		return fmt.Errorf("unsafe derived path: %s", path)
	}
	return os.RemoveAll(path)
}

func removeEmptyDir(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, os.ErrInvalid) {
		return err
	}
	return nil
}

func quarantineParquetRun(root, runPath, requestID string) error {
	info, err := os.Lstat(runPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return removeDerivedEntry(runPath)
	}
	quarantineDir := filepath.Join(root, parquetQuarantineDir)
	if err := ensurePrivateDir(quarantineDir); err != nil {
		_ = removeDerivedEntry(runPath)
		return err
	}
	name := requestID + "-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	target := filepath.Join(quarantineDir, name)
	if err := os.Rename(runPath, target); err != nil {
		_ = removeDerivedEntry(runPath)
		return err
	}
	return nil
}

func containedParquetPath(root, rel string) (string, error) {
	if !isSafeRelativePath(rel) {
		return "", errors.New("unsafe relative parquet path")
	}
	path := filepath.Join(root, filepath.FromSlash(rel))
	relToRoot, err := filepath.Rel(root, path)
	if err != nil || relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) || filepath.IsAbs(relToRoot) {
		return "", errors.New("parquet path escapes configured root")
	}
	if err := rejectSymlinkComponents(path); err != nil {
		return "", err
	}
	return path, nil
}

func isSafeRelativePath(rel string) bool {
	if rel == "" || filepath.IsAbs(rel) || strings.Contains(rel, "\\") {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel)))
	return clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}

func writeArchiveParquetSnapshot(staging string, snapshot archiveParquetSnapshot) ([]archiveParquetManifestEntry, int64, error) {
	if err := ensurePrivateDir(filepath.Join(staging, "l1")); err != nil {
		return nil, 0, err
	}
	entries := make([]archiveParquetManifestEntry, 0, 5)
	thread, err := writeArchiveParquetFile(filepath.Join(staging, "thread_summaries.parquet"), filepath.ToSlash("thread_summaries.parquet"), snapshot.Threads)
	if err != nil {
		return nil, 0, err
	}
	entries = append(entries, thread)
	memory, err := writeArchiveParquetFile(filepath.Join(staging, "l1", "l1_memory_event.parquet"), "l1/l1_memory_event.parquet", snapshot.Memory)
	if err != nil {
		return nil, 0, err
	}
	entries = append(entries, memory)
	news, err := writeArchiveParquetFile(filepath.Join(staging, "l1", "l1_news_item.parquet"), "l1/l1_news_item.parquet", snapshot.News)
	if err != nil {
		return nil, 0, err
	}
	entries = append(entries, news)
	knowledge, err := writeArchiveParquetFile(filepath.Join(staging, "l1", "l1_knowledge_item.parquet"), "l1/l1_knowledge_item.parquet", snapshot.Knowledge)
	if err != nil {
		return nil, 0, err
	}
	entries = append(entries, knowledge)
	stagingRows, err := writeArchiveParquetFile(filepath.Join(staging, "l1", "l1_staging_item.parquet"), "l1/l1_staging_item.parquet", snapshot.Staging)
	if err != nil {
		return nil, 0, err
	}
	entries = append(entries, stagingRows)
	sort.Slice(entries, func(i, j int) bool { return entries[i].RelativePath < entries[j].RelativePath })
	var total int64
	for _, entry := range entries {
		total += entry.RowCount
	}
	return entries, total, nil
}

func writeArchiveParquetFile[T any](path, relativePath string, rows []T) (archiveParquetManifestEntry, error) {
	if err := parquet.WriteFile(path, rows); err != nil {
		return archiveParquetManifestEntry{}, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return archiveParquetManifestEntry{}, err
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return archiveParquetManifestEntry{}, errors.New("parquet artifact is not a regular file")
	}
	digest, err := sha256File(path)
	if err != nil {
		return archiveParquetManifestEntry{}, err
	}
	return archiveParquetManifestEntry{RelativePath: relativePath, RowCount: int64(len(rows)), Bytes: info.Size(), SHA256: digest}, nil
}

func manifestFilesToDomain(files []archiveParquetManifestEntry) []domainmemory.ConversationArchiveParquetFile {
	result := make([]domainmemory.ConversationArchiveParquetFile, 0, len(files))
	for _, entry := range files {
		result = append(result, domainmemory.ConversationArchiveParquetFile(entry))
	}
	return result
}

func sha256Bytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func strictObject(data []byte, allowed map[string]struct{}) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return nil, errors.New("manifest object required")
	}
	values := make(map[string]json.RawMessage)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("manifest key must be string")
		}
		if _, exists := values[key]; exists {
			return nil, fmt.Errorf("duplicate manifest key %q", key)
		}
		if _, ok := allowed[key]; !ok {
			return nil, fmt.Errorf("unknown manifest key %q", key)
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, err
		}
		values[key] = raw
	}
	if token, err := decoder.Token(); err != nil {
		return nil, err
	} else if delim, ok := token.(json.Delim); !ok || delim != '}' {
		return nil, errors.New("manifest object not closed")
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	return values, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra interface{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing manifest data")
		}
		return err
	}
	return nil
}

func decodeStrictParquetManifest(data []byte) (archiveParquetManifest, error) {
	values, err := strictObject(data, map[string]struct{}{"format": {}, "export_id": {}, "created_at": {}, "total_rows": {}, "files": {}})
	if err != nil {
		return archiveParquetManifest{}, err
	}
	for _, key := range []string{"format", "export_id", "created_at", "total_rows", "files"} {
		if _, ok := values[key]; !ok {
			return archiveParquetManifest{}, fmt.Errorf("manifest field %q is required", key)
		}
	}
	var manifest archiveParquetManifest
	if err := json.Unmarshal(values["format"], &manifest.Format); err != nil {
		return archiveParquetManifest{}, err
	}
	if err := json.Unmarshal(values["export_id"], &manifest.ExportID); err != nil {
		return archiveParquetManifest{}, err
	}
	if err := json.Unmarshal(values["created_at"], &manifest.CreatedAt); err != nil {
		return archiveParquetManifest{}, err
	}
	if err := json.Unmarshal(values["total_rows"], &manifest.TotalRows); err != nil || manifest.TotalRows < 0 {
		return archiveParquetManifest{}, errors.New("invalid manifest total_rows")
	}
	var rawFiles []json.RawMessage
	if err := json.Unmarshal(values["files"], &rawFiles); err != nil || len(rawFiles) != 5 {
		return archiveParquetManifest{}, errors.New("manifest must contain exactly five files")
	}
	manifest.Files = make([]archiveParquetManifestEntry, 0, len(rawFiles))
	for _, raw := range rawFiles {
		entryValues, err := strictObject(raw, map[string]struct{}{"relative_path": {}, "row_count": {}, "bytes": {}, "sha256": {}})
		if err != nil {
			return archiveParquetManifest{}, err
		}
		for _, key := range []string{"relative_path", "row_count", "bytes", "sha256"} {
			if _, ok := entryValues[key]; !ok {
				return archiveParquetManifest{}, fmt.Errorf("manifest file field %q is required", key)
			}
		}
		var entry archiveParquetManifestEntry
		if err := json.Unmarshal(entryValues["relative_path"], &entry.RelativePath); err != nil {
			return archiveParquetManifest{}, err
		}
		if err := json.Unmarshal(entryValues["row_count"], &entry.RowCount); err != nil || entry.RowCount < 0 {
			return archiveParquetManifest{}, errors.New("invalid manifest row_count")
		}
		if err := json.Unmarshal(entryValues["bytes"], &entry.Bytes); err != nil || entry.Bytes < 0 {
			return archiveParquetManifest{}, errors.New("invalid manifest bytes")
		}
		if err := json.Unmarshal(entryValues["sha256"], &entry.SHA256); err != nil {
			return archiveParquetManifest{}, err
		}
		if !isSafeRelativePath(entry.RelativePath) || len(entry.SHA256) != 64 || strings.ToLower(entry.SHA256) != entry.SHA256 || !isHex(entry.SHA256) {
			return archiveParquetManifest{}, errors.New("invalid manifest file metadata")
		}
		manifest.Files = append(manifest.Files, entry)
	}
	paths := make([]string, 0, len(manifest.Files))
	seen := map[string]struct{}{}
	for _, entry := range manifest.Files {
		if _, ok := seen[entry.RelativePath]; ok {
			return archiveParquetManifest{}, errors.New("duplicate manifest file")
		}
		seen[entry.RelativePath] = struct{}{}
		paths = append(paths, entry.RelativePath)
	}
	if !sort.StringsAreSorted(paths) {
		return archiveParquetManifest{}, errors.New("manifest files are not sorted")
	}
	var total int64
	for _, entry := range manifest.Files {
		total += entry.RowCount
	}
	if total != manifest.TotalRows {
		return archiveParquetManifest{}, errors.New("manifest total_rows mismatch")
	}
	if _, err := time.Parse(time.RFC3339Nano, manifest.CreatedAt); err != nil {
		return archiveParquetManifest{}, err
	}
	return manifest, nil
}

func isHex(value string) bool {
	_, err := hex.DecodeString(value)
	return err == nil
}

func verifyParquetRunLayout(runDir string, manifest archiveParquetManifest) error {
	info, err := os.Lstat(runDir)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("invalid parquet run directory")
	}
	entries, err := os.ReadDir(runDir)
	if err != nil {
		return err
	}
	wantRoot := map[string]struct{}{parquetManifestName: {}, "l1": {}, "thread_summaries.parquet": {}}
	seenRoot := map[string]struct{}{}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("symlink in parquet run")
		}
		seenRoot[entry.Name()] = struct{}{}
	}
	if len(seenRoot) != len(wantRoot) {
		return errors.New("extra or missing parquet run entry")
	}
	for name := range wantRoot {
		if _, ok := seenRoot[name]; !ok {
			return errors.New("extra or missing parquet run entry")
		}
	}
	l1Dir := filepath.Join(runDir, "l1")
	l1Info, err := os.Lstat(l1Dir)
	if err != nil || l1Info.Mode()&os.ModeSymlink != 0 || !l1Info.IsDir() {
		return errors.New("invalid parquet l1 directory")
	}
	l1Entries, err := os.ReadDir(l1Dir)
	if err != nil {
		return err
	}
	wantL1 := map[string]struct{}{"l1_memory_event.parquet": {}, "l1_news_item.parquet": {}, "l1_knowledge_item.parquet": {}, "l1_staging_item.parquet": {}}
	if len(l1Entries) != len(wantL1) {
		return errors.New("extra or missing parquet l1 entry")
	}
	for _, entry := range l1Entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("symlink in parquet l1 directory")
		}
		if _, ok := wantL1[entry.Name()]; !ok {
			return errors.New("extra parquet l1 entry")
		}
	}
	wantFiles := map[string]struct{}{"thread_summaries.parquet": {}, "l1/l1_memory_event.parquet": {}, "l1/l1_news_item.parquet": {}, "l1/l1_knowledge_item.parquet": {}, "l1/l1_staging_item.parquet": {}}
	for _, entry := range manifest.Files {
		if _, ok := wantFiles[entry.RelativePath]; !ok {
			return errors.New("unexpected manifest file")
		}
		delete(wantFiles, entry.RelativePath)
	}
	if len(wantFiles) != 0 {
		return errors.New("missing manifest file")
	}
	return nil
}
