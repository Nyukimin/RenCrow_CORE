package config

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// 保存host分離（storage host）の設定契約。
// 正本: docs/04_アーキテクチャ概要.md#保存host分離storage-host /
//
//	docs/05_設定リファレンス.md#保存host分離の設定storage-host
const (
	// StorageHostModeLocal は現行どおりCORE processがdurable fileを直接開く標準profile。
	StorageHostModeLocal = "local"
	// StorageHostModeRemote はdurable fileをstorage host processだけが開く明示選択profile。
	StorageHostModeRemote = "remote"
	// DefaultStorageHostListen はstorage host listenerの固定PORT（予約PORT）。
	DefaultStorageHostListen = "127.0.0.1:18820"
	// DefaultStorageHostTimeoutSec は操作単位のtimeout。20秒で故障判定しない。
	DefaultStorageHostTimeoutSec = 30
	// DefaultStorageHostStartupWaitSec は起動時のstorage host readiness待機上限。
	// production startupのreconcile実測（約192秒）を踏まえた固定既定値。
	DefaultStorageHostStartupWaitSec = 300
	// StorageHostTransportTunnel は認証済みSSH tunnel（またはloopback直結）で
	// Bearer tokenをLANへ裸公開しない構成。このbuildの既定かつ唯一の実装。
	StorageHostTransportTunnel = "tunnel"
	// StorageHostTransportTLS はnative TLS。現行buildでは未実装であり、
	// 無言でhttpへ落とさず起動時に拒否する。
	StorageHostTransportTLS = "tls"
)

// StorageHostConfig は保存媒体hostとCORE実行hostを分ける設定。local既定では他profileを必要としない。
type StorageHostConfig struct {
	Mode           string `yaml:"mode"`
	Transport      string `yaml:"transport"`
	Listen         string `yaml:"listen"`
	Endpoint       string `yaml:"endpoint"`
	TokenFile      string `yaml:"token_file"`
	TimeoutSec     int    `yaml:"timeout_sec"`
	StartupWaitSec int    `yaml:"startup_wait_sec"`
}

func (c StorageHostConfig) configured() bool {
	return strings.TrimSpace(c.Mode) != "" ||
		strings.TrimSpace(c.Transport) != "" ||
		strings.TrimSpace(c.Listen) != "" ||
		strings.TrimSpace(c.Endpoint) != "" ||
		strings.TrimSpace(c.TokenFile) != "" ||
		c.TimeoutSec != 0 ||
		c.StartupWaitSec != 0
}

// IsRemote はremote保存profileが明示選択されているかを返す。既定はlocalで現行挙動を維持する。
func (c StorageHostConfig) IsRemote() bool {
	return strings.TrimSpace(c.Mode) == StorageHostModeRemote
}

func (c *StorageHostConfig) setStorageHostDefaults() {
	if strings.TrimSpace(c.Mode) == "" {
		c.Mode = StorageHostModeLocal
	}
	if strings.TrimSpace(c.Transport) == "" {
		c.Transport = StorageHostTransportTunnel
	}
	if strings.TrimSpace(c.Listen) == "" {
		c.Listen = DefaultStorageHostListen
	}
	if c.TimeoutSec == 0 {
		c.TimeoutSec = DefaultStorageHostTimeoutSec
	}
	if c.StartupWaitSec == 0 {
		c.StartupWaitSec = DefaultStorageHostStartupWaitSec
	}
}

func (c *Config) validateStorageHostConfig() error {
	host := &c.Storage.Host
	mode := strings.TrimSpace(host.Mode)
	// 未設定（zero value）はsetDefaultsでlocalへ解決されるため、現行profileとして検証を通過させる。
	if mode == "" {
		mode = StorageHostModeLocal
	}
	if mode != StorageHostModeLocal && mode != StorageHostModeRemote {
		return fmt.Errorf(`storage.host.mode must be "local" or "remote", got %q`, host.Mode)
	}
	if host.Listen != "" {
		if _, err := parseStorageHostListen(host.Listen); err != nil {
			return err
		}
	}
	if mode != StorageHostModeRemote {
		return nil
	}
	endpoint := strings.TrimSpace(host.Endpoint)
	if endpoint == "" {
		return fmt.Errorf("storage.host.endpoint is required when storage.host.mode is remote")
	}
	transport := strings.TrimSpace(host.Transport)
	if transport == "" {
		transport = StorageHostTransportTunnel
	}
	if transport != StorageHostTransportTunnel && transport != StorageHostTransportTLS {
		return fmt.Errorf(`storage.host.transport must be "tunnel" or "tls", got %q`, transport)
	}
	if transport == StorageHostTransportTLS {
		// Fail closed: this build has no native TLS listener, and a tls request
		// is never silently served over plain http.
		return fmt.Errorf("storage.host.transport tls is not implemented in this build")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() == "" {
		return errStorageHostEndpointShape
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("storage.host.endpoint must not carry userinfo, query or fragment")
	}
	if p := strings.TrimSuffix(parsed.Path, "/"); p != "" && p != "/" {
		return fmt.Errorf("storage.host.endpoint must not carry a path")
	}
	rawPort := parsed.Port()
	if rawPort == "" {
		return errStorageHostEndpointShape
	}
	if port, err := strconv.Atoi(rawPort); err != nil || port < 1 || port > 65535 {
		return errStorageHostEndpointPort
	}
	if !isLoopbackStorageHostHost(parsed.Hostname()) {
		return fmt.Errorf("storage.host.transport tunnel requires a loopback endpoint (authenticated SSH tunnel to the storage host)")
	}
	if host.TimeoutSec < 0 {
		return fmt.Errorf("storage.host.timeout_sec must be positive")
	}
	if host.StartupWaitSec < 0 {
		return fmt.Errorf("storage.host.startup_wait_sec must be positive")
	}
	tokenFile := strings.TrimSpace(host.TokenFile)
	if tokenFile == "" {
		return fmt.Errorf("storage.host.token_file is required when storage.host.mode is remote")
	}
	if !configPathIsAbs(tokenFile) {
		return fmt.Errorf("storage.host.token_file must be an absolute path")
	}
	if err := validateStorageHostTokenFile(tokenFile); err != nil {
		return err
	}
	return nil
}

func isLoopbackStorageHostHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// errStorageHostEndpointShape and errStorageHostEndpointPort never echo the raw
// endpoint: a mistyped URL may still carry userinfo in the operator's file.
var (
	errStorageHostEndpointShape = fmt.Errorf("storage.host.endpoint must be an http URL with host and port")
	errStorageHostEndpointPort  = fmt.Errorf("storage.host.endpoint must be an http URL with a numeric port in range 1-65535")
)

func parseStorageHostListen(listen string) (string, error) {
	value := strings.TrimSpace(listen)
	if value == "" || strings.Contains(value, "://") {
		return "", fmt.Errorf("storage.host.listen must be host:port with a fixed port")
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil || host == "" || port == "" {
		return "", fmt.Errorf("storage.host.listen must be host:port with a fixed port")
	}
	numeric, err := net.LookupPort("tcp", port)
	if err != nil || numeric < 1 || numeric > 65535 {
		return "", fmt.Errorf("storage.host.listen must be host:port with a fixed port")
	}
	return net.JoinHostPort(host, port), nil
}
