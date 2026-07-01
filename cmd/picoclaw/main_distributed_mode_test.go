package main

import (
	"context"
	"errors"
	"testing"

	domaintransport "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/transport"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/transport"
)

type stubSSHConnector struct {
	err error
}

func (s stubSSHConnector) Connect() error {
	return s.err
}

type stubTransport struct{}

func (stubTransport) Send(_ context.Context, _ domaintransport.Message) error {
	return nil
}

func (stubTransport) Receive(_ context.Context) (domaintransport.Message, error) {
	return domaintransport.Message{}, nil
}

func (stubTransport) Close() error {
	return nil
}

func (stubTransport) IsHealthy() bool {
	return true
}

func TestRegisterSSHTransport_SkipsFailedConnection(t *testing.T) {
	sshTransports := make(map[string]domaintransport.Transport)

	err := registerSSHTransport("coder3", stubSSHConnector{err: errors.New("boom")}, stubTransport{}, sshTransports)
	if err == nil {
		t.Fatal("registerSSHTransport() error = nil, want error")
	}
	if _, exists := sshTransports["coder3"]; exists {
		t.Fatal("failed SSH transport should not be registered")
	}
}

func TestRegisterSSHTransport_RegistersConnectedTransport(t *testing.T) {
	sshTransports := make(map[string]domaintransport.Transport)

	err := registerSSHTransport("coder3", stubSSHConnector{}, stubTransport{}, sshTransports)
	if err != nil {
		t.Fatalf("registerSSHTransport() error = %v, want nil", err)
	}
	if _, exists := sshTransports["coder3"]; !exists {
		t.Fatal("connected SSH transport should be registered")
	}
}

func TestFormatAgentUnavailableReason(t *testing.T) {
	got := formatAgentUnavailableReason("ssh connect failed", errors.New("connection reset by peer"))
	want := "ssh connect failed: connection reset by peer"
	if got != want {
		t.Fatalf("formatAgentUnavailableReason() = %q, want %q", got, want)
	}
}

func TestDistributedAgentAvailable(t *testing.T) {
	localTransports := map[string]*transport.LocalTransport{
		"coder1": transport.NewLocalTransport(),
	}
	sshTransports := map[string]domaintransport.Transport{
		"coder3": stubTransport{},
	}

	if !distributedAgentAvailable("coder1", localTransports, sshTransports) {
		t.Fatal("coder1 should be available via local transport")
	}
	if !distributedAgentAvailable("coder3", localTransports, sshTransports) {
		t.Fatal("coder3 should be available via ssh transport")
	}
	if distributedAgentAvailable("coder2", localTransports, sshTransports) {
		t.Fatal("coder2 should be unavailable")
	}
}
