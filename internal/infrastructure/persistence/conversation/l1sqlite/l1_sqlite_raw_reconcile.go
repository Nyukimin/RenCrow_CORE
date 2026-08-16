package l1sqlite

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

type commonRawExpectedObject struct {
	Ref  string
	Hash string
	Size int64
}

const commonRawUploadStagingDirName = ".chatgpt-import-staging"

func (s *L1SQLiteStore) reconcileCommonRawRoot(root string) error {
	expected, err := s.commonRawExpectedObjects()
	if err != nil {
		return err
	}
	if err := validateCommonRawExistingOrphaned(root); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(expected))
	if err := reconcileCommonRawTree(root, expected, seen); err != nil {
		return err
	}
	for ref := range expected {
		if _, ok := seen[ref]; !ok {
			return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "referenced raw object is missing")
		}
	}
	return nil
}

func (s *L1SQLiteStore) commonRawExpectedObjects() (map[string]commonRawExpectedObject, error) {
	expected := make(map[string]commonRawExpectedObject)
	rows, err := s.db.Query(`SELECT storage_kind, object_ref, content_sha256, content_size, asset_refs_json FROM l1_raw_record`)
	if err != nil {
		return nil, fmt.Errorf("%w: read raw object ledger", domainmemory.ErrCommonRawUnavailable)
	}
	defer rows.Close()
	for rows.Next() {
		var storageKind, objectRef, contentHash, assetRefsJSON string
		var contentSize int64
		if err := rows.Scan(&storageKind, &objectRef, &contentHash, &contentSize, &assetRefsJSON); err != nil {
			return nil, fmt.Errorf("%w: scan raw object ledger", domainmemory.ErrCommonRawUnavailable)
		}
		if storageKind == domainmemory.CommonRawStorageObject {
			if err := addCommonRawExpectedObject(expected, objectRef, contentHash, contentSize); err != nil {
				return nil, err
			}
		} else if storageKind == domainmemory.CommonRawStorageInline && objectRef != "" {
			return nil, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "inline raw record has an object reference")
		} else if storageKind != domainmemory.CommonRawStorageInline {
			return nil, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "raw object ledger storage kind is invalid")
		}
		var refs []domainmemory.CommonRawAssetRef
		if strings.TrimSpace(assetRefsJSON) == "" {
			assetRefsJSON = "[]"
		}
		if err := json.Unmarshal([]byte(assetRefsJSON), &refs); err != nil {
			return nil, fmt.Errorf("%w: decode raw object ledger asset refs", domainmemory.ErrCommonRawUnavailable)
		}
		for _, ref := range refs {
			if err := addCommonRawExpectedObject(expected, ref.ObjectRef, ref.SHA256, ref.Size); err != nil {
				return nil, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate raw object ledger", domainmemory.ErrCommonRawUnavailable)
	}
	return expected, nil
}

func addCommonRawExpectedObject(expected map[string]commonRawExpectedObject, ref, hash string, size int64) error {
	if err := validateCommonRawObjectRef(ref); err != nil {
		return err
	}
	if !validCommonRawSHA256(hash) || size < 0 {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "raw object ledger hash or size is invalid")
	}
	if old, ok := expected[ref]; ok && (old.Hash != hash || old.Size != size) {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "raw object ledger has conflicting references")
	}
	expected[ref] = commonRawExpectedObject{Ref: ref, Hash: hash, Size: size}
	return nil
}

func reconcileCommonRawTree(root string, expected map[string]commonRawExpectedObject, seen map[string]struct{}) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("%w: inspect raw source root", domainmemory.ErrCommonRawUnavailable)
	}
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(root, name)
		if name == ".orphaned" {
			continue
		}
		if name == commonRawUploadStagingDirName {
			if err := validateCommonRawUploadStagingDirectory(path); err != nil {
				return err
			}
			continue
		}
		if name != "objects" {
			return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "raw source root contains an unknown entry")
		}
		if err := reconcileCommonRawObjectsDirectory(root, path, expected, seen); err != nil {
			return err
		}
	}
	return nil
}

func validateCommonRawUploadStagingDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("%w: inspect ChatGPT upload staging namespace", domainmemory.ErrCommonRawUnavailable)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "ChatGPT upload staging namespace is unsafe")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "ChatGPT upload staging namespace is not private")
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("%w: inspect ChatGPT upload staging namespace", domainmemory.ErrCommonRawUnavailable)
	}
	if len(entries) != 0 {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "ChatGPT upload staging namespace is not empty")
	}
	return nil
}

func reconcileCommonRawObjectsDirectory(root, objectsPath string, expected map[string]commonRawExpectedObject, seen map[string]struct{}) error {
	info, err := os.Lstat(objectsPath)
	if err != nil {
		return fmt.Errorf("%w: inspect raw objects directory", domainmemory.ErrCommonRawUnavailable)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "raw objects entry is unsafe")
	}
	entries, err := os.ReadDir(objectsPath)
	if err != nil {
		return fmt.Errorf("%w: read raw objects directory", domainmemory.ErrCommonRawUnavailable)
	}
	for _, entry := range entries {
		if entry.Name() != "sha256" {
			return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "raw objects directory contains an unknown entry")
		}
		if err := reconcileCommonRawSHA256Directory(root, filepath.Join(objectsPath, entry.Name()), expected, seen); err != nil {
			return err
		}
	}
	return nil
}

func reconcileCommonRawSHA256Directory(root, shaPath string, expected map[string]commonRawExpectedObject, seen map[string]struct{}) error {
	info, err := os.Lstat(shaPath)
	if err != nil {
		return fmt.Errorf("%w: inspect raw sha256 directory", domainmemory.ErrCommonRawUnavailable)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "raw sha256 entry is unsafe")
	}
	entries, err := os.ReadDir(shaPath)
	if err != nil {
		return fmt.Errorf("%w: read raw sha256 directory", domainmemory.ErrCommonRawUnavailable)
	}
	for _, prefixEntry := range entries {
		prefix := prefixEntry.Name()
		prefixPath := filepath.Join(shaPath, prefix)
		if !validCommonRawHex(prefix, 2) {
			return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "raw sha256 directory has an invalid prefix")
		}
		prefixInfo, err := os.Lstat(prefixPath)
		if err != nil {
			return fmt.Errorf("%w: inspect raw sha256 prefix", domainmemory.ErrCommonRawUnavailable)
		}
		if prefixInfo.Mode()&os.ModeSymlink != 0 || !prefixInfo.IsDir() {
			return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "raw sha256 prefix is unsafe")
		}
		if err := reconcileCommonRawPrefixDirectory(root, prefixPath, prefix, expected, seen); err != nil {
			return err
		}
	}
	return nil
}

func reconcileCommonRawPrefixDirectory(root, prefixPath, prefix string, expected map[string]commonRawExpectedObject, seen map[string]struct{}) error {
	entries, err := os.ReadDir(prefixPath)
	if err != nil {
		return fmt.Errorf("%w: read raw sha256 prefix", domainmemory.ErrCommonRawUnavailable)
	}
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(prefixPath, name)
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("%w: inspect raw object entry", domainmemory.ErrCommonRawUnavailable)
		}
		if strings.HasPrefix(name, ".common-raw-") {
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || commonRawLinkCount(info) > 1 {
				return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "raw temporary object is unsafe")
			}
			ref := filepath.ToSlash(filepath.Join("objects", "sha256", prefix, name))
			if err := quarantineCommonRawPath(root, path, ref, info.Size(), ""); err != nil {
				return err
			}
			continue
		}
		if !validCommonRawHex(name, 64) || !strings.HasPrefix(name, prefix) {
			return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "raw object name is invalid")
		}
		ref := filepath.ToSlash(filepath.Join("objects", "sha256", prefix, name))
		expectedObject, referenced := expected[ref]
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || commonRawLinkCount(info) > 1 {
			return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "raw object is not a private regular file")
		}
		if referenced {
			if err := verifyCommonRawObjectHash(path, info, expectedObject.Size, expectedObject.Hash); err != nil {
				return err
			}
			seen[ref] = struct{}{}
			continue
		}
		if err := verifyCommonRawObjectHash(path, info, info.Size(), name); err != nil {
			return err
		}
		if err := quarantineCommonRawPath(root, path, ref, info.Size(), name); err != nil {
			return err
		}
	}
	return nil
}

func validateCommonRawExistingOrphaned(root string) error {
	path := filepath.Join(root, ".orphaned")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: inspect raw orphan quarantine", domainmemory.ErrCommonRawUnavailable)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "raw orphan quarantine is unsafe")
	}
	return filepath.WalkDir(path, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("%w: inspect raw orphan quarantine", domainmemory.ErrCommonRawUnavailable)
		}
		if current == path {
			return nil
		}
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("%w: inspect raw orphan quarantine entry", domainmemory.ErrCommonRawUnavailable)
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && (!info.Mode().IsRegular() || commonRawLinkCount(info) > 1)) {
			return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "raw orphan quarantine entry is unsafe")
		}
		return nil
	})
}

func quarantineCommonRawPath(root, source, relative string, expectedSize int64, expectedHash string) error {
	if expectedHash == "" {
		content, err := os.ReadFile(source)
		if err != nil {
			return fmt.Errorf("%w: read raw temporary object", domainmemory.ErrCommonRawUnavailable)
		}
		expectedHash = domainmemory.SHA256Hex(content)
	}
	destination := filepath.Join(root, ".orphaned", filepath.FromSlash(relative))
	if err := ensureCommonRawQuarantineDirectory(root, filepath.Dir(destination)); err != nil {
		return err
	}
	if info, err := os.Lstat(destination); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || commonRawLinkCount(info) > 1 {
			return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "raw orphan collision is unsafe")
		}
		if info.Size() != expectedSize {
			return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "raw orphan collision size differs")
		}
		actual, readErr := os.ReadFile(destination)
		if readErr != nil {
			return fmt.Errorf("%w: read raw orphan collision", domainmemory.ErrCommonRawUnavailable)
		}
		if expectedHash != "" && domainmemory.SHA256Hex(actual) != expectedHash {
			return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "raw orphan collision hash differs")
		}
		if err := os.Remove(source); err != nil {
			return fmt.Errorf("%w: remove duplicate raw orphan source", domainmemory.ErrCommonRawUnavailable)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: inspect raw orphan destination", domainmemory.ErrCommonRawUnavailable)
	}
	if err := os.Link(source, destination); err != nil {
		if existingInfo, statErr := os.Lstat(destination); statErr == nil {
			if existingInfo.Mode()&os.ModeSymlink != 0 || !existingInfo.Mode().IsRegular() || commonRawLinkCount(existingInfo) > 1 || existingInfo.Size() != expectedSize {
				return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "raw orphan collision is unsafe")
			}
			actual, readErr := os.ReadFile(destination)
			if readErr != nil || (expectedHash != "" && domainmemory.SHA256Hex(actual) != expectedHash) {
				return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "raw orphan collision hash differs")
			}
			if removeErr := os.Remove(source); removeErr != nil {
				return fmt.Errorf("%w: remove duplicate raw orphan source", domainmemory.ErrCommonRawUnavailable)
			}
			return nil
		}
		return fmt.Errorf("%w: atomically quarantine raw object", domainmemory.ErrCommonRawUnavailable)
	}
	if err := os.Remove(source); err != nil {
		return fmt.Errorf("%w: remove quarantined raw object source", domainmemory.ErrCommonRawUnavailable)
	}
	_ = syncCommonRawDirectory(filepath.Dir(destination))
	return nil
}

func ensureCommonRawQuarantineDirectory(root, target string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorRoot, "raw orphan quarantine escapes root")
	}
	current := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil {
				return fmt.Errorf("%w: create raw orphan quarantine directory", domainmemory.ErrCommonRawUnavailable)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("%w: inspect raw orphan quarantine directory", domainmemory.ErrCommonRawUnavailable)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "raw orphan quarantine directory is unsafe")
		}
	}
	return nil
}

func validateCommonRawObjectRef(ref string) error {
	if strings.TrimSpace(ref) != ref || filepath.IsAbs(filepath.FromSlash(ref)) || filepath.ToSlash(ref) != ref {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "raw object reference is not canonical")
	}
	parts := strings.Split(ref, "/")
	if len(parts) != 4 || parts[0] != "objects" || parts[1] != "sha256" || !validCommonRawHex(parts[2], 2) || !validCommonRawHex(parts[3], 64) || !strings.HasPrefix(parts[3], parts[2]) {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "raw object reference is invalid")
	}
	return nil
}

func validCommonRawSHA256(value string) bool {
	return validCommonRawHex(value, sha256.Size*2)
}

func validCommonRawHex(value string, length int) bool {
	if len(value) != length || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func finalizeCommonRawObjectNoReplace(source, target string) error {
	if err := os.Link(source, target); err != nil {
		return err
	}
	return os.Remove(source)
}
