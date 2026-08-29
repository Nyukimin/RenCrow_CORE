package eventmigration

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const maxManifestBytes = int64(1 << 20)

func writeManifest(path string, manifest Manifest) error {
	if err := validateManifestForWrite(manifest); err != nil {
		return err
	}
	abs, err := absolutePath(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return err
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(abs), ".rencrow-event-migrate-*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
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
	if err := os.Rename(temporaryName, abs); err != nil {
		return err
	}
	return nil
}

func validateManifestForWrite(manifest Manifest) error {
	if manifest.Status == StatusBlocked {
		if manifest.SchemaVersion != ManifestSchemaVersion || manifest.Mode == "" || manifest.ErrorCode == "" {
			return fmt.Errorf("blocked manifest header is invalid")
		}
		return nil
	}
	if err := validateManifest(manifest); err != nil {
		return err
	}
	if manifest.Status != StatusReady && manifest.Status != StatusApplied && manifest.Status != StatusNoop && manifest.Status != StatusBlocked {
		return fmt.Errorf("manifest status is unsupported")
	}
	return nil
}

func readManifestStrict(path string) (Manifest, error) {
	abs, err := absolutePath(path)
	if err != nil {
		return Manifest{}, err
	}
	info, err := os.Lstat(abs)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return Manifest{}, fmt.Errorf("dry-run manifest is missing or unsafe")
	}
	if info.Size() < 0 || info.Size() > maxManifestBytes {
		return Manifest{}, fmt.Errorf("dry-run manifest exceeds size bound")
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return Manifest{}, err
	}
	if int64(len(data)) != info.Size() {
		return Manifest{}, fmt.Errorf("dry-run manifest changed while reading")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode dry-run manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Manifest{}, fmt.Errorf("dry-run manifest has trailing JSON")
		}
		return Manifest{}, fmt.Errorf("dry-run manifest has trailing data")
	}
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}
