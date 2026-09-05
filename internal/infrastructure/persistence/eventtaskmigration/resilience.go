package eventtaskmigration

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	appresilience "github.com/Nyukimin/RenCrow_CORE/internal/application/resilience"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type resilienceEntryIdentity struct {
	rel     string
	size    int64
	modTime time.Time
	mode    os.FileMode
	sha256  string
}

type resilienceSourceIdentity struct {
	entries []resilienceEntryIdentity
	sha256  string
}

type migratedResilienceFile struct {
	rel  string
	data []byte
}

type migratedResilience struct {
	dirs           []string
	files          []migratedResilienceFile
	incidents      int
	mappedByReport int
	sha256         string
}

func loadAndMigrateResilience(root string, reportTasks map[string]modulecore.TaskID) (migratedResilience, resilienceSourceIdentity, error) {
	identity, rawFiles, err := inspectResilienceSource(root)
	if err != nil {
		return migratedResilience{}, resilienceSourceIdentity{}, err
	}
	result := migratedResilience{files: make([]migratedResilienceFile, 0, len(rawFiles))}
	incidentMetadata := make(map[string]bool)
	incidentDirectories := make(map[string]bool)
	for _, entry := range identity.entries {
		if !strings.HasSuffix(entry.rel, "/") {
			continue
		}
		dir := strings.TrimSuffix(entry.rel, "/")
		result.dirs = append(result.dirs, filepath.FromSlash(dir))
		parts := strings.Split(dir, "/")
		if len(parts) == 2 && parts[0] == "incidents" {
			incidentDirectories[parts[1]] = true
		}
	}
	for _, file := range rawFiles {
		data := append([]byte(nil), file.data...)
		if isIncidentMetadata(file.rel) {
			result.incidents++
			incidentDir := filepath.Base(filepath.Dir(file.rel))
			object, err := decodeJSONObject(data)
			if err != nil {
				return migratedResilience{}, resilienceSourceIdentity{}, coded("resilience_incident_invalid", "%s: %v", file.rel, err)
			}
			if _, exists := object["repair_task_id"]; exists {
				return migratedResilience{}, resilienceSourceIdentity{}, coded("resilience_incident_invalid", "%s already contains repair_task_id", file.rel)
			}
			legacyValue, exists := object["repair_job_id"]
			if exists {
				legacyJob, ok := legacyValue.(string)
				if !ok {
					return migratedResilience{}, resilienceSourceIdentity{}, coded("resilience_incident_invalid", "%s repair_job_id must be a string", file.rel)
				}
				delete(object, "repair_job_id")
				if legacyJob != "" {
					taskID, ok := reportTasks[legacyJob]
					if !ok {
						return migratedResilience{}, resilienceSourceIdentity{}, coded("resilience_repair_mapping_missing", "%s repair_job_id has no unique execution report mapping", file.rel)
					}
					object["repair_task_id"] = taskID.String()
					result.mappedByReport++
				}
			}
			canonical, err := json.Marshal(object)
			if err != nil {
				return migratedResilience{}, resilienceSourceIdentity{}, coded("resilience_incident_invalid", "%s: %v", file.rel, err)
			}
			incident, err := appresilience.DecodeIncident(canonical)
			if err != nil {
				return migratedResilience{}, resilienceSourceIdentity{}, coded("resilience_contract_invalid", "%s cannot round-trip current contract: %v", file.rel, err)
			}
			if incident.Signature != incidentDir {
				return migratedResilience{}, resilienceSourceIdentity{}, coded("resilience_incident_invalid", "%s signature does not match incident directory", file.rel)
			}
			data = append(canonical, '\n')
			incidentMetadata[incidentDir] = true
		}
		result.files = append(result.files, migratedResilienceFile{rel: file.rel, data: data})
	}
	for incidentDir := range incidentDirectories {
		if !incidentMetadata[incidentDir] {
			return migratedResilience{}, resilienceSourceIdentity{}, coded("resilience_incident_invalid", "incident directory %q has no incident.json", incidentDir)
		}
	}
	result.sha256 = canonicalResilienceSHA(result.dirs, result.files)
	return result, identity, nil
}

type rawResilienceFile struct {
	rel  string
	data []byte
}

func inspectResilienceSource(root string) (resilienceSourceIdentity, []rawResilienceFile, error) {
	rootInfo, err := os.Lstat(root)
	if err != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return resilienceSourceIdentity{}, nil, coded("resilience_source_invalid", "source resilience root is not a non-symlink directory")
	}
	var identities []resilienceEntryIdentity
	var files []rawResilienceFile
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink is not supported: %s", rel)
		}
		slashRel := filepath.ToSlash(rel)
		parts := strings.Split(slashRel, "/")
		if info.IsDir() {
			if slashRel != "incidents" && !(len(parts) == 2 && parts[0] == "incidents" && parts[1] != "") {
				return fmt.Errorf("unsupported directory: %s", rel)
			}
			identities = append(identities, resilienceEntryIdentity{rel: slashRel + "/", modTime: info.ModTime(), mode: info.Mode()})
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported non-regular entry: %s", rel)
		}
		if len(parts) > 1 && !(len(parts) == 3 && parts[0] == "incidents" && parts[1] != "" && parts[2] != "") {
			return fmt.Errorf("unsupported file location: %s", rel)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		identities = append(identities, resilienceEntryIdentity{rel: slashRel, size: info.Size(), modTime: info.ModTime(), mode: info.Mode(), sha256: hex.EncodeToString(digest[:])})
		files = append(files, rawResilienceFile{rel: filepath.FromSlash(slashRel), data: data})
		return nil
	})
	if err != nil {
		return resilienceSourceIdentity{}, nil, coded("resilience_source_invalid", "%v", err)
	}
	sort.Slice(identities, func(i, j int) bool { return identities[i].rel < identities[j].rel })
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })
	return resilienceSourceIdentity{entries: identities, sha256: sourceResilienceSHA(identities, files)}, files, nil
}

func verifyResilienceSourceIdentity(root string, before resilienceSourceIdentity) error {
	after, _, err := inspectResilienceSource(root)
	if err != nil {
		return err
	}
	if before.sha256 != after.sha256 || !reflect.DeepEqual(before.entries, after.entries) {
		return errors.New("resilience source tree content or metadata changed")
	}
	return nil
}

func sourceResilienceSHA(identities []resilienceEntryIdentity, files []rawResilienceFile) string {
	dirs := make([]string, 0)
	for _, entry := range identities {
		if strings.HasSuffix(entry.rel, "/") {
			dirs = append(dirs, filepath.FromSlash(strings.TrimSuffix(entry.rel, "/")))
		}
	}
	canonical := make([]migratedResilienceFile, len(files))
	for i, file := range files {
		canonical[i] = migratedResilienceFile{rel: file.rel, data: file.data}
	}
	return canonicalResilienceSHA(dirs, canonical)
}

func canonicalResilienceSHA(dirs []string, files []migratedResilienceFile) string {
	h := sha256.New()
	for _, dir := range dirs {
		rel := []byte(filepath.ToSlash(dir))
		_, _ = h.Write([]byte{'D'})
		_ = binary.Write(h, binary.BigEndian, uint64(len(rel)))
		_, _ = h.Write(rel)
	}
	for _, file := range files {
		rel := []byte(filepath.ToSlash(file.rel))
		_, _ = h.Write([]byte{'F'})
		_ = binary.Write(h, binary.BigEndian, uint64(len(rel)))
		_, _ = h.Write(rel)
		_ = binary.Write(h, binary.BigEndian, uint64(len(file.data)))
		_, _ = h.Write(file.data)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func isIncidentMetadata(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	return len(parts) == 3 && parts[0] == "incidents" && parts[1] != "" && parts[2] == "incident.json"
}

func writeAndVerifyResilienceRoot(target string, plan migratedResilience) (resultErr error) {
	if err := requireAbsentDirectoryTarget(target); err != nil {
		return err
	}
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return coded("resilience_target", "create target parent: %v", err)
	}
	temporary, err := os.MkdirTemp(parent, ".rencrow-resilience-migrate-*.tmp")
	if err != nil {
		return coded("resilience_target", "create temporary resilience root: %v", err)
	}
	defer os.RemoveAll(temporary)
	if err := os.Chmod(temporary, 0o700); err != nil {
		return coded("resilience_target", "secure temporary resilience root: %v", err)
	}
	for _, dir := range plan.dirs {
		path := filepath.Join(temporary, dir)
		if !pathWithin(temporary, path) {
			return coded("resilience_target", "unsafe relative output directory %q", dir)
		}
		if err := os.MkdirAll(path, 0o700); err != nil {
			return coded("resilience_target", "create output directory: %v", err)
		}
		if err := os.Chmod(path, 0o700); err != nil {
			return coded("resilience_target", "secure output directory: %v", err)
		}
	}
	for _, file := range plan.files {
		path := filepath.Join(temporary, file.rel)
		if !pathWithin(temporary, path) {
			return coded("resilience_target", "unsafe relative output path %q", file.rel)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return coded("resilience_target", "create output directory: %v", err)
		}
		if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
			return coded("resilience_target", "secure output directory: %v", err)
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return coded("resilience_target", "create output file: %v", err)
		}
		_, writeErr := f.Write(file.data)
		syncErr := f.Sync()
		closeErr := f.Close()
		if writeErr != nil || syncErr != nil || closeErr != nil {
			return coded("resilience_target", "write output file %q: %v %v %v", file.rel, writeErr, syncErr, closeErr)
		}
	}
	if err := verifyResilienceRoot(temporary, plan); err != nil {
		return err
	}
	if err := os.Rename(temporary, target); err != nil {
		return coded("resilience_target", "publish target root: %v", err)
	}
	published := true
	defer func() {
		if resultErr != nil && published {
			if cleanupErr := cleanupAppliedTargets(resolvedPaths{targetResilienceRoot: target}, false, false, true); cleanupErr != nil {
				resultErr = fmt.Errorf("%w; clean failed resilience target: %v", resultErr, cleanupErr)
			}
		}
	}()
	if err := verifyResilienceRoot(target, plan); err != nil {
		return err
	}
	published = false
	return nil
}

func verifyResilienceRoot(root string, plan migratedResilience) error {
	info, err := os.Lstat(root)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return coded("resilience_target_verify", "target resilience root is missing or unsafe")
	}
	identity, files, err := inspectResilienceSource(root)
	if err != nil {
		return coded("resilience_target_verify", "%v", err)
	}
	if len(files) != len(plan.files) || identity.sha256 != plan.sha256 {
		return coded("resilience_target_verify", "target resilience file set or checksum differs from plan")
	}
	for _, entry := range identity.entries {
		if !strings.HasSuffix(entry.rel, "/") {
			continue
		}
		dirInfo, err := os.Lstat(filepath.Join(root, filepath.FromSlash(strings.TrimSuffix(entry.rel, "/"))))
		if err != nil || dirInfo.Mode().Perm() != 0o700 {
			return coded("resilience_target_verify", "target resilience directory %q is not owner-only", entry.rel)
		}
	}
	for index, file := range files {
		if file.rel != plan.files[index].rel || !bytes.Equal(file.data, plan.files[index].data) {
			return coded("resilience_target_verify", "target resilience file %q differs from plan", file.rel)
		}
		fileInfo, err := os.Lstat(filepath.Join(root, file.rel))
		if err != nil || fileInfo.Mode().Perm() != 0o600 {
			return coded("resilience_target_verify", "target resilience file %q is not owner-only", file.rel)
		}
		if isIncidentMetadata(file.rel) {
			data := bytes.TrimSpace(file.data)
			if _, err := appresilience.DecodeIncident(data); err != nil {
				return coded("resilience_target_verify", "target incident %q fails current contract: %v", file.rel, err)
			}
		}
	}
	return nil
}

func requireAbsentDirectoryTarget(path string) error {
	_, err := os.Lstat(path)
	if err == nil {
		return coded("resilience_target_exists", "target resilience root already exists")
	}
	if !errors.Is(err, os.ErrNotExist) {
		return coded("resilience_target", "inspect target resilience root: %v", err)
	}
	return nil
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func pathsOverlap(a, b string) bool {
	return pathWithin(a, b) || pathWithin(b, a)
}

func resilienceTreeAliasesFile(root string, candidate os.FileInfo) (bool, error) {
	aliased := false
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() && os.SameFile(info, candidate) {
			aliased = true
			return filepath.SkipAll
		}
		return nil
	})
	return aliased, err
}

func existingPathInfo(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return info, err
}
