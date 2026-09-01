package dcimigration

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
)

var (
	errBuildCaptureReceiptInput = errors.New("capture receipt build input is invalid")
	errBuildManifestInput       = errors.New("dry-run manifest build input is invalid")
	errBuildInputRead           = errors.New("build input cannot be read safely")
	errBuildInputJSON           = errors.New("build input is not one valid JSON object")
)

// readBuildCaptureReceipt reads one ready capture receipt without exposing any
// filesystem or JSON details in its errors. The returned digest is for the
// exact bytes consumed by the decoder.
func readBuildCaptureReceipt(path string) (CaptureReceipt, string, error) {
	data, err := readBuildInputBytes(path, maxCaptureManifestBytes)
	if err != nil {
		return CaptureReceipt{}, "", errBuildCaptureReceiptInput
	}
	var receipt CaptureReceipt
	if err := decodeOneBuildInputObject(data, &receipt); err != nil {
		return CaptureReceipt{}, "", errBuildCaptureReceiptInput
	}
	if err := validateCaptureReceipt(receipt); err != nil || receipt.SchemaVersion != CaptureSchemaVersion || receipt.Mode != ModeCapture || receipt.Status != StatusReady {
		return CaptureReceipt{}, "", errBuildCaptureReceiptInput
	}
	return receipt, buildInputBytesSHA256(data), nil
}

// readBuildManifest reads one ready dry-run manifest without exposing any
// filesystem or JSON details in its errors. The returned digest is for the
// exact bytes consumed by the decoder.
func readBuildManifest(path string) (Manifest, string, error) {
	data, err := readBuildInputBytes(path, maxManifestBytes)
	if err != nil {
		return Manifest{}, "", errBuildManifestInput
	}
	var manifest Manifest
	if err := decodeOneBuildInputObject(data, &manifest); err != nil {
		return Manifest{}, "", errBuildManifestInput
	}
	if err := validateManifest(manifest); err != nil || manifest.SchemaVersion != ManifestSchemaVersion || manifest.Mode != ModeDryRun || manifest.Status != StatusReady {
		return Manifest{}, "", errBuildManifestInput
	}
	return manifest, buildInputBytesSHA256(data), nil
}

func readBuildInputBytes(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > limit {
		return nil, errBuildInputRead
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, errBuildInputRead
	}
	openedInfo, statErr := file.Stat()
	if statErr != nil || openedInfo.Mode()&os.ModeSymlink != 0 || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		_ = file.Close()
		return nil, errBuildInputRead
	}

	data, readErr := io.ReadAll(io.LimitReader(file, limit+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || int64(len(data)) > limit || int64(len(data)) != info.Size() {
		return nil, errBuildInputRead
	}

	latestInfo, latestErr := os.Lstat(path)
	if latestErr != nil || latestInfo.Mode()&os.ModeSymlink != 0 || !latestInfo.Mode().IsRegular() || latestInfo.Size() != info.Size() || !os.SameFile(info, latestInfo) {
		return nil, errBuildInputRead
	}
	return data, nil
}

func decodeOneBuildInputObject(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errBuildInputJSON
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errBuildInputJSON
	}
	return nil
}

func buildInputBytesSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
