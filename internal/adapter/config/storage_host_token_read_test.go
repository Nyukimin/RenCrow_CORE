package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadStorageHostBearerToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "storage-host.token")
	if err := os.WriteFile(path, []byte("test-token-value\r\n"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod token: %v", err)
	}
	token, err := ReadStorageHostBearerToken(path)
	if err != nil {
		t.Fatalf("ReadStorageHostBearerToken: %v", err)
	}
	if token != "test-token-value" {
		t.Fatalf("token=%q, want line-ending-trimmed bearer token", token)
	}
}

func TestReadStorageHostBearerTokenRejectsUnsafeMissingEmptyAndOversizedFiles(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		secretPath := filepath.Join(t.TempDir(), "missing-top-secret")
		_, err := ReadStorageHostBearerToken(secretPath)
		if err == nil {
			t.Fatal("missing token file was accepted")
		}
		if strings.Contains(err.Error(), "top-secret") {
			t.Fatalf("error leaked token-like path content: %v", err)
		}
	})

	t.Run("unsafe permissions", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "unsafe.token")
		const secret = "unsafe-secret-value"
		if err := os.WriteFile(path, []byte(secret), 0o644); err != nil {
			t.Fatalf("write token: %v", err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatalf("chmod token: %v", err)
		}
		_, err := ReadStorageHostBearerToken(path)
		if err == nil {
			t.Fatal("group-readable token file was accepted")
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked token: %v", err)
		}
	})

	t.Run("empty", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "empty.token")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatalf("write token: %v", err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatalf("chmod token: %v", err)
		}
		if _, err := ReadStorageHostBearerToken(path); err == nil {
			t.Fatal("empty token file was accepted")
		}
	})

	t.Run("oversized", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "oversized.token")
		const secret = "oversized-secret-value"
		contents := strings.Repeat(secret, 4096)
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatalf("write token: %v", err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatalf("chmod token: %v", err)
		}
		_, err := ReadStorageHostBearerToken(path)
		if err == nil {
			t.Fatal("oversized token file was accepted")
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked token: %v", err)
		}
	})
}
