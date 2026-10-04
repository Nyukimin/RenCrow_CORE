package config

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
)

const maxStorageHostBearerTokenBytes = 4096

var errStorageHostTokenInvalid = errors.New("storage.host.token_file must contain a valid nonempty bearer token within the size limit")

// ReadStorageHostBearerToken validates the token file's native confidentiality
// before reading a bounded bearer token. Errors never contain token bytes.
func ReadStorageHostBearerToken(path string) (string, error) {
	handle, err := openValidatedStorageHostTokenFile(path)
	if err != nil {
		return "", err
	}
	defer handle.Close()

	contents, err := io.ReadAll(io.LimitReader(handle, maxStorageHostBearerTokenBytes+1))
	if err != nil || len(contents) > maxStorageHostBearerTokenBytes {
		return "", errStorageHostTokenInvalid
	}
	if bytes.HasSuffix(contents, []byte("\r\n")) {
		contents = contents[:len(contents)-2]
	} else if bytes.HasSuffix(contents, []byte("\n")) {
		contents = contents[:len(contents)-1]
	}
	if len(contents) == 0 || !validStorageHostBearerToken(contents) {
		return "", errStorageHostTokenInvalid
	}
	return string(contents), nil
}

// validateStorageHostTokenFile is shared with config validation so both the
// remote client and the storage host enforce the same platform-native rule.
func validateStorageHostTokenFile(path string) error {
	handle, err := openValidatedStorageHostTokenFile(path)
	if err != nil {
		return err
	}
	return handle.Close()
}

func openValidatedStorageHostTokenFile(path string) (*os.File, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("storage.host.token_file is required")
	}
	handle, err := os.Open(path)
	if err != nil {
		return nil, errStorageHostTokenUnreadable
	}
	info, err := handle.Stat()
	if err != nil {
		_ = handle.Close()
		return nil, errStorageHostTokenUnreadable
	}
	if !info.Mode().IsRegular() {
		_ = handle.Close()
		return nil, errStorageHostTokenUnreadable
	}
	if err := validateStorageHostTokenFileAccess(path, info); err != nil {
		_ = handle.Close()
		return nil, err
	}
	return handle, nil
}

func validStorageHostBearerToken(contents []byte) bool {
	sawTokenByte := false
	sawPadding := false
	for _, value := range contents {
		switch {
		case value >= 'A' && value <= 'Z', value >= 'a' && value <= 'z', value >= '0' && value <= '9':
			if sawPadding {
				return false
			}
			sawTokenByte = true
		case value == '-' || value == '.' || value == '_' || value == '~' || value == '+' || value == '/':
			if sawPadding {
				return false
			}
			sawTokenByte = true
		case value == '=' && sawTokenByte:
			sawPadding = true
		default:
			return false
		}
	}
	return sawTokenByte
}
