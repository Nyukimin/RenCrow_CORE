package runmigration

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const (
	maxSnapshotFileBytes  int64 = 256 << 20
	maxSnapshotTotalBytes int64 = 1 << 30
)

var (
	errRunMigrationFilesystem = errors.New("run migration filesystem validation failed")

	// These hooks are deliberately package-private and no-op in production. They
	// make the before/after read drift contract deterministic in tests without
	// exposing a mutation or live-store escape hatch to callers.
	snapshotBeforeReadHook = func(string) {}
	snapshotAfterReadHook  = func(string) {}
)

type expectedSnapshotFile struct {
	path string
	hash [sha256.Size]byte
}

type snapshotEntry struct {
	info      os.FileInfo
	directory bool
}

type snapshotTree struct {
	entries map[string]snapshotEntry
	files   map[string]snapshotEntry
	total   int64
}

// readSnapshotFiles reads one immutable, explicitly-manifested filesystem
// snapshot. It never opens a database or any live store. Every path and hash
// is validated before the tree is read, and the same tree is checked again
// after the read so a source mutation cannot be hidden by cached bytes.
func readSnapshotFiles(root string, expected map[string]string) (map[string][]byte, error) {
	manifest, err := validateSnapshotManifest(expected)
	if err != nil {
		return nil, err
	}

	rootPath, err := absoluteExistingPath(root)
	if err != nil {
		return nil, err
	}
	rootInfo, err := os.Lstat(rootPath)
	if err != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return nil, errRunMigrationFilesystem
	}

	before, err := inspectSnapshotTree(rootPath, manifest)
	if err != nil {
		return nil, err
	}

	paths := make([]string, 0, len(manifest))
	for path := range manifest {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	result := make(map[string][]byte, len(manifest))
	for _, path := range paths {
		entry := manifest[path]
		absolute := filepath.Join(rootPath, filepath.FromSlash(path))
		snapshotBeforeReadHook(absolute)

		file, openErr := os.Open(absolute)
		if openErr != nil {
			return nil, errRunMigrationFilesystem
		}
		openedInfo, statErr := file.Stat()
		if statErr != nil || !sameSnapshotEntry(entryForInfo(openedInfo, false), before.files[path]) {
			_ = file.Close()
			return nil, errRunMigrationFilesystem
		}

		data, readErr := io.ReadAll(io.LimitReader(file, maxSnapshotFileBytes+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || int64(len(data)) > maxSnapshotFileBytes {
			return nil, errRunMigrationFilesystem
		}

		afterInfo, lstatErr := os.Lstat(absolute)
		if lstatErr != nil || afterInfo.Mode()&os.ModeSymlink != 0 || !afterInfo.Mode().IsRegular() ||
			!sameSnapshotEntry(entryForInfo(afterInfo, false), before.files[path]) || int64(len(data)) != afterInfo.Size() {
			return nil, errRunMigrationFilesystem
		}

		digest := sha256.Sum256(data)
		if !bytes.Equal(digest[:], entry.hash[:]) {
			return nil, errRunMigrationFilesystem
		}
		result[path] = append([]byte(nil), data...)
		snapshotAfterReadHook(absolute)
	}

	if _, err := absoluteExistingPath(rootPath); err != nil {
		return nil, err
	}
	snapshotAfterReadHook(rootPath)
	after, err := inspectSnapshotTree(rootPath, manifest)
	if err != nil || !sameSnapshotTree(before, after) {
		return nil, errRunMigrationFilesystem
	}
	return result, nil
}

// publishCohort writes a complete output cohort into a fresh sibling staging
// directory, then publishes that directory with one rename. A persistent,
// exclusive parent lock serializes cooperating publishers and fails closed on
// a stale lock; this is the portable guard required because os.Rename may
// replace an empty destination directory on Linux.
func publishCohort(target string, files map[string][]byte) (status string, err error) {
	normalized, expected, err := normalizeOutputFiles(files)
	if err != nil {
		return "", err
	}

	targetPath, parent, err := resolvePublishTarget(target)
	if err != nil {
		return "", err
	}
	base := filepath.Base(targetPath)
	lockPath := filepath.Join(parent, "."+base+".rencrow-publish.lock")
	lock, err := acquirePublishLock(lockPath)
	if err != nil {
		return "", err
	}
	defer func() {
		releaseErr := releasePublishLock(lock, lockPath)
		if err == nil && releaseErr != nil {
			status = ""
			err = releaseErr
		}
	}()

	targetInfo, targetErr := os.Lstat(targetPath)
	switch {
	case targetErr == nil:
		if targetInfo.Mode()&os.ModeSymlink != 0 || !targetInfo.IsDir() {
			return "", errRunMigrationFilesystem
		}
		if err := validatePublishedModes(targetPath, expected); err != nil {
			return "", err
		}
		actual, err := readSnapshotFiles(targetPath, expected)
		if err != nil || !sameFileBytes(actual, normalized) {
			return "", errRunMigrationFilesystem
		}
		return "noop", nil
	case !os.IsNotExist(targetErr):
		return "", errRunMigrationFilesystem
	}

	stage, err := os.MkdirTemp(parent, "."+base+".rencrow-stage-")
	if err != nil {
		return "", errRunMigrationFilesystem
	}
	stagePath := stage
	defer func() {
		if stagePath != "" {
			_ = os.RemoveAll(stagePath)
		}
	}()
	if err := os.Chmod(stage, 0o700); err != nil {
		return "", errRunMigrationFilesystem
	}
	if err := writeStagedFiles(stage, normalized); err != nil {
		return "", err
	}
	if err := syncDirectory(stage); err != nil {
		return "", err
	}
	if err := validatePublishedModes(stage, expected); err != nil {
		return "", err
	}
	if _, err := readSnapshotFiles(stage, expected); err != nil {
		return "", err
	}

	// The lock and final absence check provide no-clobber for cooperating
	// publishers using this dedicated parent. The caller must reserve the
	// output parent for this operation; an unrelated creator racing an empty
	// target is outside this portable lock contract.
	if _, err := os.Lstat(targetPath); err == nil {
		return "", errRunMigrationFilesystem
	} else if !os.IsNotExist(err) {
		return "", errRunMigrationFilesystem
	}
	if _, err := absoluteExistingPath(parent); err != nil {
		return "", err
	}
	if err := os.Rename(stage, targetPath); err != nil {
		return "", errRunMigrationFilesystem
	}
	stagePath = ""
	if err := syncDirectory(parent); err != nil {
		return "", err
	}
	return "applied", nil
}

func validateSnapshotManifest(expected map[string]string) (map[string]expectedSnapshotFile, error) {
	if len(expected) == 0 {
		return nil, errRunMigrationFilesystem
	}
	manifest := make(map[string]expectedSnapshotFile, len(expected))
	for rawPath, rawHash := range expected {
		path, err := normalizeRelativePath(rawPath)
		if err != nil || len(rawHash) != hex.EncodedLen(sha256.Size) || rawHash != strings.ToLower(rawHash) {
			return nil, errRunMigrationFilesystem
		}
		decoded, err := hex.DecodeString(rawHash)
		if err != nil {
			return nil, errRunMigrationFilesystem
		}
		if _, exists := manifest[path]; exists {
			return nil, errRunMigrationFilesystem
		}
		var hash [sha256.Size]byte
		copy(hash[:], decoded)
		manifest[path] = expectedSnapshotFile{path: path, hash: hash}
	}
	return manifest, nil
}

func normalizeOutputFiles(files map[string][]byte) (map[string][]byte, map[string]string, error) {
	if len(files) == 0 {
		return nil, nil, errRunMigrationFilesystem
	}
	normalized := make(map[string][]byte, len(files))
	expected := make(map[string]string, len(files))
	var total int64
	for rawPath, data := range files {
		path, err := normalizeRelativePath(rawPath)
		if err != nil {
			return nil, nil, errRunMigrationFilesystem
		}
		if _, exists := normalized[path]; exists {
			return nil, nil, errRunMigrationFilesystem
		}
		if int64(len(data)) > maxSnapshotFileBytes || total > maxSnapshotTotalBytes-int64(len(data)) {
			return nil, nil, errRunMigrationFilesystem
		}
		total += int64(len(data))
		normalized[path] = append([]byte(nil), data...)
		digest := sha256.Sum256(data)
		expected[path] = hex.EncodeToString(digest[:])
	}
	return normalized, expected, nil
}

func normalizeRelativePath(raw string) (string, error) {
	if raw == "" || strings.IndexByte(raw, 0) >= 0 || strings.Contains(raw, "\\") || strings.Contains(raw, ":") {
		return "", errRunMigrationFilesystem
	}
	native := filepath.FromSlash(raw)
	if filepath.IsAbs(native) || filepath.VolumeName(native) != "" {
		return "", errRunMigrationFilesystem
	}
	parts := strings.Split(native, string(filepath.Separator))
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", errRunMigrationFilesystem
		}
	}
	clean := filepath.Clean(native)
	if clean != native || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errRunMigrationFilesystem
	}
	return filepath.ToSlash(clean), nil
}

func absoluteExistingPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" || strings.IndexByte(path, 0) >= 0 {
		return "", errRunMigrationFilesystem
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", errRunMigrationFilesystem
	}
	abs = filepath.Clean(abs)
	for current := abs; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return "", errRunMigrationFilesystem
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return abs, nil
}

func inspectSnapshotTree(root string, expected map[string]expectedSnapshotFile) (snapshotTree, error) {
	tree := snapshotTree{entries: map[string]snapshotEntry{}, files: map[string]snapshotEntry{}}
	var files []snapshotEntry
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info == nil {
			return errRunMigrationFilesystem
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errRunMigrationFilesystem
		}
		if path == root {
			if !info.IsDir() {
				return errRunMigrationFilesystem
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
			return errRunMigrationFilesystem
		}
		rel = filepath.ToSlash(rel)
		if info.IsDir() {
			tree.entries[rel] = entryForInfo(info, true)
			return nil
		}
		if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxSnapshotFileBytes ||
			tree.total > maxSnapshotTotalBytes-info.Size() || isSQLiteSidecarName(filepath.Base(path)) {
			return errRunMigrationFilesystem
		}
		if _, ok := expected[rel]; !ok || hasMultipleLinks(path, info) {
			return errRunMigrationFilesystem
		}
		if err := rejectAdjacentSQLiteSidecars(path); err != nil {
			return err
		}
		for _, prior := range files {
			if os.SameFile(prior.info, info) {
				return errRunMigrationFilesystem
			}
		}
		tree.total += info.Size()
		entry := entryForInfo(info, false)
		tree.entries[rel] = entry
		tree.files[rel] = entry
		files = append(files, entry)
		return nil
	})
	if err != nil {
		return snapshotTree{}, errRunMigrationFilesystem
	}
	if len(tree.files) != len(expected) {
		return snapshotTree{}, errRunMigrationFilesystem
	}
	for path := range expected {
		if _, ok := tree.files[path]; !ok {
			return snapshotTree{}, errRunMigrationFilesystem
		}
	}
	return tree, nil
}

func expectedDirectorySet(expected map[string]expectedSnapshotFile) map[string]struct{} {
	dirs := map[string]struct{}{}
	for path := range expected {
		for {
			idx := strings.LastIndexByte(path, '/')
			if idx < 0 {
				break
			}
			path = path[:idx]
			if path == "" {
				break
			}
			dirs[path] = struct{}{}
		}
	}
	return dirs
}

func entryForInfo(info os.FileInfo, directory bool) snapshotEntry {
	return snapshotEntry{info: info, directory: directory}
}

func sameSnapshotEntry(a, b snapshotEntry) bool {
	if a.info == nil || b.info == nil || a.directory != b.directory || !os.SameFile(a.info, b.info) {
		return false
	}
	return a.info.Mode().Type() == b.info.Mode().Type() && a.info.Mode().Perm() == b.info.Mode().Perm() &&
		a.info.Size() == b.info.Size() && a.info.ModTime().Equal(b.info.ModTime())
}

func sameSnapshotTree(a, b snapshotTree) bool {
	if a.total != b.total || len(a.entries) != len(b.entries) {
		return false
	}
	for path, before := range a.entries {
		after, ok := b.entries[path]
		if !ok || !sameSnapshotEntry(before, after) {
			return false
		}
	}
	return true
}

func hasMultipleLinks(path string, info os.FileInfo) bool {
	return !migrationFileIsUnaliased(path, info)
}

func isSQLiteSidecarName(name string) bool {
	return strings.HasSuffix(name, "-wal") || strings.HasSuffix(name, "-shm") || strings.HasSuffix(name, "-journal")
}

func rejectAdjacentSQLiteSidecars(path string) error {
	if isSQLiteSidecarName(filepath.Base(path)) {
		return errRunMigrationFilesystem
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(path + suffix); err == nil {
			return errRunMigrationFilesystem
		} else if !os.IsNotExist(err) {
			return errRunMigrationFilesystem
		}
	}
	return nil
}

func resolvePublishTarget(target string) (string, string, error) {
	if strings.TrimSpace(target) == "" || strings.IndexByte(target, 0) >= 0 {
		return "", "", errRunMigrationFilesystem
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", "", errRunMigrationFilesystem
	}
	abs = filepath.Clean(abs)
	parent := filepath.Dir(abs)
	if parent == abs || filepath.Base(abs) == "." || filepath.Base(abs) == string(os.PathSeparator) {
		return "", "", errRunMigrationFilesystem
	}
	parent, err = absoluteExistingPath(parent)
	if err != nil {
		return "", "", err
	}
	info, err := os.Lstat(parent)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", "", errRunMigrationFilesystem
	}
	return abs, parent, nil
}

func acquirePublishLock(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, errRunMigrationFilesystem
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, errRunMigrationFilesystem
	}
	return file, nil
}

func releasePublishLock(file *os.File, path string) error {
	if file == nil {
		return errRunMigrationFilesystem
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	removeErr := os.Remove(path)
	if syncErr != nil || closeErr != nil || removeErr != nil {
		return errRunMigrationFilesystem
	}
	return nil
}

func writeStagedFiles(stage string, files map[string][]byte) error {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		full := filepath.Join(stage, filepath.FromSlash(path))
		if err := makeStagedParents(stage, filepath.Dir(filepath.FromSlash(path))); err != nil {
			return err
		}
		file, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return errRunMigrationFilesystem
		}
		writeErr := writeAll(file, files[path])
		if writeErr == nil {
			writeErr = file.Chmod(0o600)
		}
		if writeErr == nil {
			writeErr = file.Sync()
		}
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			return errRunMigrationFilesystem
		}
	}
	return nil
}

func makeStagedParents(stage, relativeDir string) error {
	if relativeDir == "." || relativeDir == "" {
		return nil
	}
	current := stage
	for _, part := range strings.Split(relativeDir, string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." {
			return errRunMigrationFilesystem
		}
		current = filepath.Join(current, part)
		if err := os.Mkdir(current, 0o700); err != nil && !os.IsExist(err) {
			return errRunMigrationFilesystem
		}
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errRunMigrationFilesystem
		}
		if err := os.Chmod(current, 0o700); err != nil {
			return errRunMigrationFilesystem
		}
	}
	return nil
}

func writeAll(file *os.File, data []byte) error {
	for len(data) > 0 {
		n, err := file.Write(data)
		if err != nil || n <= 0 {
			return errRunMigrationFilesystem
		}
		data = data[n:]
	}
	return nil
}

func validatePublishedModes(root string, expected map[string]string) error {
	info, err := os.Lstat(root)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errRunMigrationFilesystem
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		return errRunMigrationFilesystem
	}
	expectedEntries := make(map[string]expectedSnapshotFile, len(expected))
	for path, hash := range expected {
		validated, err := normalizeRelativePath(path)
		if err != nil || validated != path {
			return errRunMigrationFilesystem
		}
		decoded, err := hex.DecodeString(hash)
		if err != nil || len(decoded) != sha256.Size {
			return errRunMigrationFilesystem
		}
		var digest [sha256.Size]byte
		copy(digest[:], decoded)
		expectedEntries[path] = expectedSnapshotFile{path: path, hash: digest}
	}
	expectedDirs := expectedDirectorySet(expectedEntries)
	err = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info == nil || info.Mode()&os.ModeSymlink != 0 {
			return errRunMigrationFilesystem
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return errRunMigrationFilesystem
		}
		rel = filepath.ToSlash(rel)
		if info.IsDir() {
			if _, ok := expectedDirs[rel]; !ok {
				return errRunMigrationFilesystem
			}
			if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
				return errRunMigrationFilesystem
			}
			return nil
		}
		if !info.Mode().IsRegular() || isSQLiteSidecarName(filepath.Base(path)) {
			return errRunMigrationFilesystem
		}
		if _, ok := expectedEntries[rel]; !ok {
			return errRunMigrationFilesystem
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			return errRunMigrationFilesystem
		}
		return nil
	})
	if err != nil {
		return errRunMigrationFilesystem
	}
	return nil
}

func sameFileBytes(actual, expected map[string][]byte) bool {
	if len(actual) != len(expected) {
		return false
	}
	for path, want := range expected {
		got, ok := actual[path]
		if !ok || !bytes.Equal(got, want) {
			return false
		}
	}
	return true
}

func syncDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return errRunMigrationFilesystem
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if syncErr != nil || closeErr != nil {
		return errRunMigrationFilesystem
	}
	return nil
}
