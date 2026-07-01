package transport

import (
	"fmt"
	"log"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	domaintransport "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/transport"
)

// TransportFactory はConfig.Distributed.TransportsからTransportを生成
type TransportFactory struct{}

// NewTransportFactory は新しいTransportFactoryを作成
func NewTransportFactory() *TransportFactory {
	return &TransportFactory{}
}

// CreateTransports はDistributedConfigからAgent別のTransportを生成
// 戻り値: map[agentName] → domaintransport.Transport (*LocalTransport or *SSHTransport)
func (f *TransportFactory) CreateTransports(cfg config.DistributedConfig) (map[string]domaintransport.Transport, error) {
	transports := make(map[string]domaintransport.Transport)

	for agentName, tc := range cfg.Transports {
		switch tc.Type {
		case "local", "":
			lt := NewLocalTransport()
			transports[agentName] = lt
			log.Printf("[TransportFactory] Created LocalTransport for agent '%s'", agentName)

		case "ssh":
			if tc.RemoteHost == "" {
				return nil, fmt.Errorf("agent '%s': ssh transport requires remote_host", agentName)
			}
			if tc.RemoteUser == "" {
				return nil, fmt.Errorf("agent '%s': ssh transport requires remote_user", agentName)
			}
			if tc.SSHKeyPath == "" {
				return nil, fmt.Errorf("agent '%s': ssh transport requires ssh_key_path", agentName)
			}
			st := NewSSHTransportStrict(tc.RemoteHost, tc.RemoteUser, tc.SSHKeyPath, agentName, tc.StrictHostKey)
			if tc.RemoteAgentPath != "" || tc.RemoteConfigPath != "" {
				st.WithRemotePaths(tc.RemoteAgentPath, tc.RemoteConfigPath)
			}
			transports[agentName] = st
			log.Printf("[TransportFactory] Created SSHTransport for agent '%s' → %s@%s", agentName, tc.RemoteUser, tc.RemoteHost)

		default:
			return nil, fmt.Errorf("agent '%s': unknown transport type '%s'", agentName, tc.Type)
		}
	}

	return transports, nil
}
