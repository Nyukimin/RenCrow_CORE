package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeStorageHostConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "server:\n  port: 18790\n" + body
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestStorageHostDefaultsToLocalMode(t *testing.T) {
	cfg, err := LoadConfig(writeStorageHostConfig(t, ""))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Storage.Host.Mode != StorageHostModeLocal {
		t.Fatalf("storage.host.mode=%q, want %q", cfg.Storage.Host.Mode, StorageHostModeLocal)
	}
	if cfg.Storage.Host.Listen != DefaultStorageHostListen {
		t.Fatalf("storage.host.listen=%q, want %q", cfg.Storage.Host.Listen, DefaultStorageHostListen)
	}
	if cfg.Storage.Host.TimeoutSec <= 0 || cfg.Storage.Host.StartupWaitSec <= 0 {
		t.Fatalf("timeouts must be positive: timeout=%d startup_wait=%d", cfg.Storage.Host.TimeoutSec, cfg.Storage.Host.StartupWaitSec)
	}
}

func TestStorageHostRemoteModeValidation(t *testing.T) {
	token := filepath.Join(t.TempDir(), "storage-host.token")
	if err := os.WriteFile(token, []byte("token-value\n"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	base := func(endpoint string) string {
		return `storage:
  host:
    mode: remote
    endpoint: "` + endpoint + `"
    token_file: "` + token + `"
`
	}
	t.Run("valid endpoint", func(t *testing.T) {
		cfg, err := LoadConfig(writeStorageHostConfig(t, base("http://127.0.0.1:18820")))
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.Storage.Host.Mode != StorageHostModeRemote {
			t.Fatalf("mode=%q, want remote", cfg.Storage.Host.Mode)
		}
	})
	cases := []struct{ name, endpoint, want string }{
		{"missing endpoint", "", "storage.host.endpoint is required when storage.host.mode is remote"},
		{"empty endpoint", "   ", "storage.host.endpoint is required when storage.host.mode is remote"},
		{"missing scheme", "192.168.1.204:18820", "storage.host.endpoint must be an http URL with host and port"},
		{"https scheme", "https://192.168.1.204:18820", "storage.host.endpoint must be an http URL with host and port"},
		{"userinfo", "http://user@127.0.0.1:18820", "storage.host.endpoint must not carry userinfo, query or fragment"},
		{"query", "http://127.0.0.1:18820?x=1", "storage.host.endpoint must not carry userinfo, query or fragment"},
		{"fragment", "http://127.0.0.1:18820#f", "storage.host.endpoint must not carry userinfo, query or fragment"},
		{"path", "http://127.0.0.1:18820/api", "storage.host.endpoint must not carry a path"},
		{"missing port", "http://127.0.0.1", "storage.host.endpoint must be an http URL with host and port"},
		{"unparseable port", "http://127.0.0.1:notaport", "storage.host.endpoint must be an http URL with host and port"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadConfig(writeStorageHostConfig(t, base(tc.endpoint)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestStorageHostRemoteModeTokenFileRequired(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "absent.token")
	if _, err := LoadConfig(writeStorageHostConfig(t, `storage:
  host:
    mode: remote
    endpoint: "http://127.0.0.1:18820"
`)); err == nil || !strings.Contains(err.Error(), "storage.host.token_file is required when storage.host.mode is remote") {
		t.Fatalf("error=%v, want token_file required", err)
	}
	if _, err := LoadConfig(writeStorageHostConfig(t, `storage:
  host:
    mode: remote
    endpoint: "http://127.0.0.1:18820"
    token_file: "storage-host.token"
`)); err == nil || !strings.Contains(err.Error(), "storage.host.token_file must be an absolute path") {
		t.Fatalf("error=%v, want absolute path", err)
	}
	if _, err := LoadConfig(writeStorageHostConfig(t, `storage:
  host:
    mode: remote
    endpoint: "http://127.0.0.1:18820"
    token_file: "`+missing+`"
`)); err == nil || !strings.Contains(err.Error(), "storage.host.token_file is unreadable") {
		t.Fatalf("error=%v, want unreadable token file", err)
	}
	unreadable := filepath.Join(dir, "unreadable.token")
	if err := os.WriteFile(unreadable, []byte("x\n"), 0o000); err != nil {
		t.Fatalf("write token: %v", err)
	}
	if os.Geteuid() != 0 {
		if _, err := LoadConfig(writeStorageHostConfig(t, `storage:
  host:
    mode: remote
    endpoint: "http://127.0.0.1:18820"
    token_file: "`+unreadable+`"
`)); err == nil || !strings.Contains(err.Error(), "storage.host.token_file is unreadable") {
			t.Fatalf("error=%v, want unreadable token file", err)
		}
	}
	worldReadable := filepath.Join(dir, "world.token")
	if err := os.WriteFile(worldReadable, []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write token: %v", err)
	}
	if os.Geteuid() != 0 {
		if _, err := LoadConfig(writeStorageHostConfig(t, `storage:
  host:
    mode: remote
    endpoint: "http://127.0.0.1:18820"
    token_file: "`+worldReadable+`"
`)); err == nil || !strings.Contains(err.Error(), "storage.host.token_file must not be accessible by group or others") {
			t.Fatalf("error=%v, want mode rejection", err)
		}
	}
}

func TestStorageHostModeRejectsUnknownValue(t *testing.T) {
	_, err := LoadConfig(writeStorageHostConfig(t, `storage:
  host:
    mode: network
`))
	if err == nil || !strings.Contains(err.Error(), `storage.host.mode must be "local" or "remote"`) {
		t.Fatalf("error=%v, want mode rejection", err)
	}
}

func TestStorageHostListenRejectsEphemeralPort(t *testing.T) {
	for _, listen := range []string{"127.0.0.1:0", ":0", "127.0.0.1", "http://127.0.0.1:18820"} {
		_, err := LoadConfig(writeStorageHostConfig(t, `storage:
  host:
    listen: "`+listen+`"
`))
		if err == nil || !strings.Contains(err.Error(), "storage.host.listen must be host:port with a fixed port") {
			t.Fatalf("listen=%q error=%v, want listen rejection", listen, err)
		}
	}
}

// TestStorageHostEndpointRejectsOutOfRangePortAndNegativeTimeouts は
// numeric port範囲と負のtimeout/startup_waitのfail closed確認。
func TestStorageHostEndpointRejectsOutOfRangePortAndNegativeTimeouts(t *testing.T) {
	token := filepath.Join(t.TempDir(), "storage-host.token")
	if err := os.WriteFile(token, []byte("token-value\n"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	remote := func(endpoint string) string {
		return `storage:
  host:
    mode: remote
    endpoint: "` + endpoint + `"
    token_file: "` + token + `"
`
	}
	for _, endpoint := range []string{"http://127.0.0.1:0", "http://127.0.0.1:70000", "http://127.0.0.1:99999"} {
		_, err := LoadConfig(writeStorageHostConfig(t, remote(endpoint)))
		if err == nil || !strings.Contains(err.Error(), "storage.host.endpoint must be an http URL with a numeric port in range") {
			t.Fatalf("endpoint=%q error=%v, want numeric port range rejection", endpoint, err)
		}
	}
	for _, tc := range []struct{ key, value, want string }{
		{"timeout_sec", "-1", "storage.host.timeout_sec must be positive"},
		{"startup_wait_sec", "-300", "storage.host.startup_wait_sec must be positive"},
	} {
		_, err := LoadConfig(writeStorageHostConfig(t, remote("http://127.0.0.1:18820")+"    "+tc.key+": "+tc.value+"\n"))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s=%s error=%v, want %q", tc.key, tc.value, err, tc.want)
		}
	}
}

// TestStorageHostEndpointErrorDoesNotEchoCredentials は不正URLを拒否する際に
// userinfo/passwordがerrorへechoされないことの確認（secretをlogへ出さない）。
func TestStorageHostEndpointErrorDoesNotEchoCredentials(t *testing.T) {
	const secret = "supersecretpassword"
	for _, endpoint := range []string{"http://user:" + secret + "@127.0.0.1:70000", "http://user:" + secret + "@127.0.0.1"} {
		_, err := LoadConfig(writeStorageHostConfig(t, `storage:
  host:
    mode: remote
    endpoint: "`+endpoint+`"
    token_file: "/nonexistent/storage-host.token"
`))
		if err == nil {
			t.Fatalf("endpoint=%q error=nil, want rejection", endpoint)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked the endpoint credential: %v", err)
		}
	}
}

// TestStorageHostTransportRejectsBareBearerOverLAN は http-only構成で
// BearerをLANへ裸公開しないことの機械的強制。
func TestStorageHostTransportRejectsBareBearerOverLAN(t *testing.T) {
	token := filepath.Join(t.TempDir(), "storage-host.token")
	if err := os.WriteFile(token, []byte("token-value\n"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	body := func(endpoint, transport string) string {
		return `storage:
  host:
    mode: remote
    endpoint: "` + endpoint + `"
    token_file: "` + token + `"
    transport: "` + transport + `"
`
	}
	if _, err := LoadConfig(writeStorageHostConfig(t, body("http://192.168.1.203:18820", ""))); err == nil ||
		!strings.Contains(err.Error(), "storage.host.transport tunnel requires a loopback endpoint") {
		t.Fatalf("error=%v, want bare LAN endpoint rejection", err)
	}
	if _, err := LoadConfig(writeStorageHostConfig(t, body("http://127.0.0.1:18821", "tunnel"))); err != nil {
		t.Fatalf("loopback tunnel endpoint: %v", err)
	}
	if _, err := LoadConfig(writeStorageHostConfig(t, body("http://192.168.1.203:18820", "tunnel"))); err == nil ||
		!strings.Contains(err.Error(), "storage.host.transport tunnel requires a loopback endpoint") {
		t.Fatalf("error=%v, want tunnel endpoint loopback rejection", err)
	}
	if _, err := LoadConfig(writeStorageHostConfig(t, body("https://192.168.1.203:18820", "tls"))); err == nil ||
		!strings.Contains(err.Error(), "storage.host.transport tls is not implemented in this build") {
		t.Fatalf("error=%v, want tls not implemented", err)
	}
	if _, err := LoadConfig(writeStorageHostConfig(t, body("http://127.0.0.1:18821", "raw"))); err == nil ||
		!strings.Contains(err.Error(), `storage.host.transport must be "tunnel" or "tls"`) {
		t.Fatalf("error=%v, want unknown transport rejection", err)
	}
}
