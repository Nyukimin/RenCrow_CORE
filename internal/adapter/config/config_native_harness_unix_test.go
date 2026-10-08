//go:build !windows

package config

import (
	"os"
	"strings"
	"testing"
)

const keyFilePermissionMessage = "native_harness.issuer.key_file must not be accessible by group or others"

// chmodKeyFile sets the mode explicitly because the process umask applies to
// the mode given at creation.
func chmodKeyFile(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}

func TestNativeHarnessAcceptsOwnerOnlyKeyFile(t *testing.T) {
	for _, mode := range []os.FileMode{0o600, 0o400} {
		cfg, paths := validNativeHarnessConfig(t)
		chmodKeyFile(t, paths.keyFile, mode)

		if err := cfg.validateNativeHarnessConfig(); err != nil {
			t.Fatalf("owner-only key file (mode %o) rejected: %v", mode, err)
		}
	}
}

func TestNativeHarnessRejectsKeyFileReadableByGroupOrOthers(t *testing.T) {
	for _, mode := range []os.FileMode{0o640, 0o604, 0o644, 0o660, 0o666, 0o777, 0o610} {
		cfg, paths := validNativeHarnessConfig(t)
		chmodKeyFile(t, paths.keyFile, mode)

		err := cfg.validateNativeHarnessConfig()

		if err == nil || !strings.Contains(err.Error(), keyFilePermissionMessage) {
			t.Fatalf("mode %o: error = %v, want %q", mode, err, keyFilePermissionMessage)
		}
	}
}

func TestNativeHarnessChecksKeyFilePermissionBeforeReadingIt(t *testing.T) {
	// Arrange: a loose file whose content is also malformed. The permission
	// error must win, which shows the file was refused before it was read.
	cfg, paths := validNativeHarnessConfig(t)
	secret := randomKeyHex(t)
	writeTestFile(t, paths.keyFile, strings.ToUpper(secret))
	chmodKeyFile(t, paths.keyFile, 0o644)

	err := cfg.validateNativeHarnessConfig()

	if err == nil || !strings.Contains(err.Error(), keyFilePermissionMessage) {
		t.Fatalf("error = %v, want the permission error first", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), secret) {
		t.Fatalf("error leaks key material: %q", err)
	}
}

func TestNativeHarnessDoesNotCheckHarnessConfigPermission(t *testing.T) {
	// The Harness config holds no secret; only the key file is confidential.
	cfg, paths := validNativeHarnessConfig(t)
	chmodKeyFile(t, paths.harnessConfig, 0o644)

	if err := cfg.validateNativeHarnessConfig(); err != nil {
		t.Fatalf("a readable Harness config was rejected: %v", err)
	}
}

func TestLoadConfigRefusesToStartOnLooseKeyFile(t *testing.T) {
	_, paths := validNativeHarnessConfig(t)
	chmodKeyFile(t, paths.keyFile, 0o644)

	_, err := LoadConfig(writeStorageHostConfig(t, nativeHarnessYAML(t, paths, 300)))

	if err == nil || !strings.Contains(err.Error(), keyFilePermissionMessage) {
		t.Fatalf("error = %v, want startup refusal naming the key file permission", err)
	}
}

func TestStorageHostTokenFileKeepsItsOwnPermissionMessage(t *testing.T) {
	// The shared check must keep the storage host wording unchanged.
	_, paths := validNativeHarnessConfig(t)
	chmodKeyFile(t, paths.keyFile, 0o644)

	err := validateStorageHostTokenFile(paths.keyFile)

	if err == nil || !strings.Contains(err.Error(), "storage.host.token_file must not be accessible by group or others") {
		t.Fatalf("error = %v, want the storage.host.token_file wording", err)
	}
	if strings.Contains(err.Error(), "native_harness") {
		t.Fatalf("storage host error mentions native_harness: %v", err)
	}
}
