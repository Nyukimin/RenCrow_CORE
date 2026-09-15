package config

import (
	"fmt"
	"net/mail"
	"path/filepath"
	"strings"
)

func (c *Config) validateGmailHeartbeat() error {
	g := c.Heartbeat.Gmail
	if !g.Enabled {
		return nil
	}
	if !c.Heartbeat.Enabled || !c.LocalAgentOps.Enabled || strings.TrimSpace(c.LocalAgentOps.UserID) == "" {
		return fmt.Errorf("heartbeat.gmail requires heartbeat and local_agent_ops enabled with an owner user_id")
	}
	address, err := mail.ParseAddress(g.Account)
	if err != nil || address.Address != g.Account || !strings.HasSuffix(strings.ToLower(g.Account), "@gmail.com") {
		return fmt.Errorf("heartbeat.gmail.account must be a Gmail address without a display name")
	}
	if g.IntervalMinutes < 5 || g.IntervalMinutes > 10080 || g.TimeoutMinutes < 1 || g.TimeoutMinutes > 60 {
		return fmt.Errorf("heartbeat.gmail interval must be 5..10080 minutes and timeout 1..60 minutes")
	}
	if g.MaxResults < 1 || g.MaxResults > 100 || strings.TrimSpace(g.Command) == "" {
		return fmt.Errorf("heartbeat.gmail requires a command and max_results in 1..100")
	}
	if !filepath.IsAbs(g.CredentialsFile) || !filepath.IsAbs(g.TokenFile) || filepath.Clean(g.CredentialsFile) == filepath.Clean(g.TokenFile) {
		return fmt.Errorf("heartbeat.gmail requires distinct absolute credentials_file and token_file paths")
	}
	return nil
}
