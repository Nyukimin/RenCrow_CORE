package transport

import (
	"testing"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
)

func TestTransportFactory_CreateLocal(t *testing.T) {
	factory := NewTransportFactory()
	cfg := config.DistributedConfig{
		Enabled: true,
		Transports: map[string]config.TransportConfig{
			"mio": {Type: "local"},
		},
	}

	transports, err := factory.CreateTransports(cfg)
	if err != nil {
		t.Fatalf("CreateTransports failed: %v", err)
	}

	if len(transports) != 1 {
		t.Fatalf("Expected 1 transport, got %d", len(transports))
	}

	lt, ok := transports["mio"].(*LocalTransport)
	if !ok {
		t.Fatal("Expected LocalTransport for Mio")
	}
	defer lt.Close()
}

func TestTransportFactory_CreateLocalDefault(t *testing.T) {
	factory := NewTransportFactory()
	cfg := config.DistributedConfig{
		Enabled: true,
		Transports: map[string]config.TransportConfig{
			"shiro": {Type: ""}, // empty defaults to local
		},
	}

	transports, err := factory.CreateTransports(cfg)
	if err != nil {
		t.Fatalf("CreateTransports failed: %v", err)
	}

	_, ok := transports["shiro"].(*LocalTransport)
	if !ok {
		t.Fatal("Expected LocalTransport for empty type")
	}
}

func TestTransportFactory_CreateSSH(t *testing.T) {
	factory := NewTransportFactory()
	cfg := config.DistributedConfig{
		Enabled: true,
		Transports: map[string]config.TransportConfig{
			"coder3": {
				Type:       "ssh",
				RemoteHost: "192.168.1.200:22",
				RemoteUser: "picoclaw",
				SSHKeyPath: "/home/user/.ssh/id_ed25519",
			},
		},
	}

	transports, err := factory.CreateTransports(cfg)
	if err != nil {
		t.Fatalf("CreateTransports failed: %v", err)
	}

	st, ok := transports["coder3"].(*SSHTransport)
	if !ok {
		t.Fatal("Expected SSHTransport for Coder3")
	}
	defer st.Close()

	if st.host != "192.168.1.200:22" {
		t.Errorf("Expected host '192.168.1.200:22', got '%s'", st.host)
	}
}

func TestTransportFactory_SSHMissingHost(t *testing.T) {
	factory := NewTransportFactory()
	cfg := config.DistributedConfig{
		Enabled: true,
		Transports: map[string]config.TransportConfig{
			"coder3": {
				Type:       "ssh",
				RemoteUser: "picoclaw",
				SSHKeyPath: "/path/to/key",
			},
		},
	}

	_, err := factory.CreateTransports(cfg)
	if err == nil {
		t.Error("Expected error for SSH without remote_host")
	}
}

func TestTransportFactory_SSHMissingUser(t *testing.T) {
	factory := NewTransportFactory()
	cfg := config.DistributedConfig{
		Enabled: true,
		Transports: map[string]config.TransportConfig{
			"coder3": {
				Type:       "ssh",
				RemoteHost: "192.168.1.200:22",
				SSHKeyPath: "/path/to/key",
			},
		},
	}

	_, err := factory.CreateTransports(cfg)
	if err == nil {
		t.Error("Expected error for SSH without remote_user")
	}
}

func TestTransportFactory_SSHMissingKey(t *testing.T) {
	factory := NewTransportFactory()
	cfg := config.DistributedConfig{
		Enabled: true,
		Transports: map[string]config.TransportConfig{
			"coder3": {
				Type:       "ssh",
				RemoteHost: "192.168.1.200:22",
				RemoteUser: "picoclaw",
			},
		},
	}

	_, err := factory.CreateTransports(cfg)
	if err == nil {
		t.Error("Expected error for SSH without ssh_key_path")
	}
}

func TestTransportFactory_UnknownType(t *testing.T) {
	factory := NewTransportFactory()
	cfg := config.DistributedConfig{
		Enabled: true,
		Transports: map[string]config.TransportConfig{
			"Agent": {Type: "grpc"},
		},
	}

	_, err := factory.CreateTransports(cfg)
	if err == nil {
		t.Error("Expected error for unknown transport type")
	}
}

func TestTransportFactory_MultipleAgents(t *testing.T) {
	factory := NewTransportFactory()
	cfg := config.DistributedConfig{
		Enabled: true,
		Transports: map[string]config.TransportConfig{
			"mio":   {Type: "local"},
			"shiro": {Type: "local"},
			"coder3": {
				Type:       "ssh",
				RemoteHost: "192.168.1.200:22",
				RemoteUser: "picoclaw",
				SSHKeyPath: "/path/to/key",
			},
		},
	}

	transports, err := factory.CreateTransports(cfg)
	if err != nil {
		t.Fatalf("CreateTransports failed: %v", err)
	}

	if len(transports) != 3 {
		t.Fatalf("Expected 3 transports, got %d", len(transports))
	}

	if _, ok := transports["mio"].(*LocalTransport); !ok {
		t.Error("Expected LocalTransport for Mio")
	}
	if _, ok := transports["shiro"].(*LocalTransport); !ok {
		t.Error("Expected LocalTransport for Shiro")
	}
	if _, ok := transports["coder3"].(*SSHTransport); !ok {
		t.Error("Expected SSHTransport for Coder3")
	}
}
