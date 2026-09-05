package eventtaskmigration

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxManifestBytes = int64(1 << 20)

func validateManifestDestination(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("manifest destination is not a regular non-symlink file")
	}
	return nil
}

func writeManifest(path string, manifest Manifest) error {
	if err := validateManifest(manifest); err != nil {
		return err
	}
	if err := validateManifestDestination(path); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	temporary, err := os.CreateTemp(dir, ".rencrow-event-task-migrate-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func readManifestStrict(path string) (Manifest, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > maxManifestBytes {
		return Manifest{}, errors.New("dry-run manifest is missing or unsafe")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	if int64(len(data)) != info.Size() {
		return Manifest{}, errors.New("dry-run manifest changed during read")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode dry-run manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Manifest{}, errors.New("dry-run manifest has trailing data")
	}
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func validateManifest(m Manifest) error {
	if m.SchemaVersion != ManifestSchemaVersion {
		return errors.New("manifest schema version is unsupported")
	}
	if m.Status != StatusBlocked && m.Mode != ModeDryRun && m.Mode != ModeApply {
		return errors.New("manifest mode is invalid")
	}
	if m.Status != StatusReady && m.Status != StatusApplied && m.Status != StatusBlocked {
		return errors.New("manifest status is invalid")
	}
	if m.Status == StatusBlocked {
		if strings.TrimSpace(m.ErrorCode) == "" {
			return errors.New("blocked manifest requires error_code")
		}
		return nil
	}
	if m.ErrorCode != "" {
		return errors.New("successful manifest must not have error_code")
	}
	for _, value := range []string{m.SourceEventStoreSHA256, m.SourceConversationL1SHA256, m.CanonicalOutputSetSHA256, m.SourceExecutionReportsSHA256, m.CanonicalExecutionReportsSHA256, m.SourceResilienceSHA256, m.CanonicalResilienceSHA256} {
		if !validSHA256(value) {
			return errors.New("successful manifest contains an invalid SHA256")
		}
	}
	if m.TotalEvents < 0 || m.OrchestratorEvents < 0 || m.MappedByReceipt < 0 || m.MappedDerived < 0 || m.NoTaskEvents < 0 || m.Dependencies < 0 || m.ExecutionReportRows < 0 || m.MappedReportByEvent < 0 || m.MappedReportDerived < 0 || m.ResilienceFiles < 0 || m.ResilienceIncidents < 0 || m.MappedRepairByReport < 0 {
		return errors.New("manifest count is negative")
	}
	if m.OrchestratorEvents > m.TotalEvents || m.NoTaskEvents > m.TotalEvents || m.MappedByReceipt+m.MappedDerived > m.OrchestratorEvents {
		return errors.New("manifest counts are inconsistent")
	}
	if m.MappedReportByEvent+m.MappedReportDerived != m.ExecutionReportRows {
		return errors.New("manifest execution report counts are inconsistent")
	}
	if m.ResilienceIncidents > m.ResilienceFiles || m.MappedRepairByReport > m.ResilienceIncidents {
		return errors.New("manifest resilience counts are inconsistent")
	}
	return nil
}

func validSHA256(raw string) bool {
	if len(raw) != sha256.Size*2 || raw != strings.ToLower(raw) {
		return false
	}
	for _, r := range raw {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}
