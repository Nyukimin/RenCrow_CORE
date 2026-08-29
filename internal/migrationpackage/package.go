// Package migrationpackage converts one already restore-checked CORE snapshot
// cohort into the owner state package consumed by RenCrow_Workspace.
package migrationpackage

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	ArchiveName     = "rencrow-state.tar.gz"
	ChecksumsName   = "SHA256SUMS"
	ManifestName    = "manifest.txt"
	DescriptorName  = "state-export.json"
	maxArchiveSize  = int64(512 << 30)
	maxMetadataSize = int64(1 << 20)
)

var cohortFiles = []string{ChecksumsName, ManifestName, ArchiveName}

type descriptor struct {
	SchemaVersion  string           `json:"schema_version"`
	ModuleID       string           `json:"module_id"`
	SchemaRevision string           `json:"schema_revision"`
	RecordCount    int64            `json:"record_count"`
	Consistency    string           `json:"consistency"`
	Files          []descriptorFile `json:"files"`
}

type descriptorFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Summary identifies one validated package without exposing its host path.
type Summary struct {
	LogicalID string
	SHA256    string
	SizeBytes int64
}

// Inspect validates the complete fixed CORE state package and returns a
// bounded logical identity. The descriptor digest commits to every payload
// digest and is therefore the package identity used by the owner receipt.
func Inspect(packageDir string) (Summary, error) {
	root, err := checkedDirectory(packageDir, true)
	if err != nil {
		return Summary{}, err
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != len(cohortFiles)+1 {
		return Summary{}, errors.New("state package file set is invalid")
	}
	descriptorPath := filepath.Join(root, DescriptorName)
	info, descriptorDigest, err := inspectRegular(descriptorPath, maxMetadataSize)
	if err != nil {
		return Summary{}, errors.New("state descriptor is invalid")
	}
	data, err := os.ReadFile(descriptorPath)
	if err != nil {
		return Summary{}, errors.New("read state descriptor")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var value descriptor
	if err := decoder.Decode(&value); err != nil {
		return Summary{}, errors.New("state descriptor is invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Summary{}, errors.New("state descriptor has trailing data")
	}
	if value.SchemaVersion != "rencrow-state-export/v1" || value.ModuleID != "RenCrow_CORE" ||
		value.SchemaRevision != "rencrow-core-migration-state/v1" || value.RecordCount != 0 ||
		value.Consistency != "quiesced" || len(value.Files) != len(cohortFiles) {
		return Summary{}, errors.New("state descriptor contract mismatch")
	}
	total := info.Size()
	for index, expectedName := range cohortFiles {
		file := value.Files[index]
		if file.Path != expectedName || file.Size < 0 || len(file.SHA256) != sha256.Size*2 {
			return Summary{}, errors.New("state payload descriptor is invalid")
		}
		limit := maxMetadataSize
		if file.Path == ArchiveName {
			limit = maxArchiveSize
		}
		payloadInfo, digest, err := inspectRegular(filepath.Join(root, file.Path), limit)
		if err != nil || payloadInfo.Size() != file.Size || digest != file.SHA256 {
			return Summary{}, errors.New("state payload integrity mismatch")
		}
		total += payloadInfo.Size()
	}
	return Summary{LogicalID: "core-state-cohort", SHA256: descriptorDigest, SizeBytes: total}, nil
}

// Build copies a verified snapshot cohort into an empty owner-only directory
// and publishes its deterministic state descriptor last.
func Build(snapshotDir, outputDir string) (err error) {
	source, err := checkedDirectory(snapshotDir, false)
	if err != nil {
		return err
	}
	output, err := checkedDirectory(outputDir, true)
	if err != nil {
		return err
	}
	if pathsOverlap(source, output) {
		return errors.New("snapshot and output directories must not overlap")
	}

	entries, err := os.ReadDir(output)
	if err != nil {
		return errors.New("read output directory")
	}
	if len(entries) != 0 {
		return errors.New("output directory must be empty")
	}

	created := make([]string, 0, len(cohortFiles)+1)
	defer func() {
		if err == nil {
			return
		}
		for index := len(created) - 1; index >= 0; index-- {
			_ = os.Remove(created[index])
		}
	}()

	expectedArchiveHash, err := readExpectedArchiveHash(filepath.Join(source, ChecksumsName))
	if err != nil {
		return err
	}
	files := make([]descriptorFile, 0, len(cohortFiles))
	for _, name := range cohortFiles {
		sourcePath := filepath.Join(source, name)
		limit := maxMetadataSize
		if name == ArchiveName {
			limit = maxArchiveSize
		}
		info, digest, err := inspectRegular(sourcePath, limit)
		if err != nil {
			return err
		}
		if name == ArchiveName && digest != expectedArchiveHash {
			return errors.New("snapshot archive checksum mismatch")
		}
		target := filepath.Join(output, name)
		if err := copyStable(sourcePath, target, info.Size(), digest); err != nil {
			return err
		}
		created = append(created, target)
		files = append(files, descriptorFile{Path: name, Size: info.Size(), SHA256: digest})
	}

	value := descriptor{
		SchemaVersion: "rencrow-state-export/v1", ModuleID: "RenCrow_CORE",
		SchemaRevision: "rencrow-core-migration-state/v1", RecordCount: 0,
		Consistency: "quiesced", Files: files,
	}
	data, err := json.Marshal(value)
	if err != nil {
		return errors.New("encode state descriptor")
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(output, ".state-export-*.tmp")
	if err != nil {
		return errors.New("create state descriptor")
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return errors.New("protect state descriptor")
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return errors.New("write state descriptor")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return errors.New("sync state descriptor")
	}
	if err := temporary.Close(); err != nil {
		return errors.New("close state descriptor")
	}
	descriptorPath := filepath.Join(output, DescriptorName)
	if err := os.Rename(temporaryName, descriptorPath); err != nil {
		return errors.New("publish state descriptor")
	}
	created = append(created, descriptorPath)
	return nil
}

func checkedDirectory(path string, requirePrivate bool) (string, error) {
	if strings.TrimSpace(path) == "" || strings.IndexByte(path, 0) >= 0 {
		return "", errors.New("directory path is invalid")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", errors.New("resolve directory")
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("directory is missing or unsafe")
	}
	if requirePrivate && runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("output directory must be owner-only")
	}
	return filepath.Clean(absolute), nil
}

func pathsOverlap(left, right string) bool {
	for _, pair := range [][2]string{{left, right}, {right, left}} {
		relative, err := filepath.Rel(pair[0], pair[1])
		if err == nil && (relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))) {
			return true
		}
	}
	return false
}

func readExpectedArchiveHash(path string) (string, error) {
	info, _, err := inspectRegular(path, maxMetadataSize)
	if err != nil || info.Size() == 0 {
		return "", errors.New("checksum manifest is invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", errors.New("open checksum manifest")
	}
	defer file.Close()
	scanner := bufio.NewScanner(io.LimitReader(file, maxMetadataSize+1))
	if !scanner.Scan() || scanner.Err() != nil {
		return "", errors.New("checksum manifest is invalid")
	}
	fields := strings.Fields(scanner.Text())
	if len(fields) != 2 || fields[1] != ArchiveName || len(fields[0]) != sha256.Size*2 {
		return "", errors.New("checksum manifest is invalid")
	}
	if _, err := hex.DecodeString(fields[0]); err != nil {
		return "", errors.New("checksum manifest is invalid")
	}
	if scanner.Scan() {
		return "", errors.New("checksum manifest contains unexpected entries")
	}
	return strings.ToLower(fields[0]), nil
}

func inspectRegular(path string, limit int64) (os.FileInfo, string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, "", errors.New("snapshot cohort is missing or unsafe")
	}
	if info.Size() < 0 || info.Size() > limit {
		return nil, "", errors.New("snapshot cohort exceeds size limit")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, "", errors.New("open snapshot cohort")
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, io.LimitReader(file, limit+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		return nil, "", errors.New("hash snapshot cohort")
	}
	return info, fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func copyStable(source, target string, expectedSize int64, expectedDigest string) error {
	input, err := os.Open(source)
	if err != nil {
		return errors.New("open snapshot payload")
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.New("create state payload")
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(output, hash), input)
	syncErr := output.Sync()
	closeErr := output.Close()
	actualDigest := fmt.Sprintf("%x", hash.Sum(nil))
	if copyErr != nil || syncErr != nil || closeErr != nil || written != expectedSize || actualDigest != expectedDigest {
		_ = os.Remove(target)
		return errors.New("copy state payload")
	}
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() || info.Size() != expectedSize {
		_ = os.Remove(target)
		return errors.New("snapshot payload changed during copy")
	}
	return nil
}
