package config

import (
	"path/filepath"
	"testing"
)

func TestGmailHeartbeatRequiresOwnerAndBoundedConfiguration(t *testing.T) {
	c := &Config{}
	c.setDefaults()
	if c.Heartbeat.Gmail.Enabled || c.validateGmailHeartbeat() != nil {
		t.Fatal("default Gmail must remain disabled")
	}
	c.Heartbeat.Enabled = true
	c.LocalAgentOps.Enabled = true
	c.LocalAgentOps.UserID = "ren"
	c.Heartbeat.Gmail.Enabled = true
	c.Heartbeat.Gmail.Account = "owner@gmail.com"
	c.Heartbeat.Gmail.CredentialsFile = filepath.Join(t.TempDir(), "client.json")
	c.Heartbeat.Gmail.TokenFile = filepath.Join(t.TempDir(), "token.json")
	if err := c.validateGmailHeartbeat(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Config){
		func(c *Config) { c.LocalAgentOps.Enabled = false },
		func(c *Config) { c.LocalAgentOps.UserID = "" },
		func(c *Config) { c.Heartbeat.Enabled = false },
		func(c *Config) { c.Heartbeat.Gmail.Account = "Owner <owner@gmail.com>" },
		func(c *Config) { c.Heartbeat.Gmail.MaxResults = 101 },
		func(c *Config) { c.Heartbeat.Gmail.IntervalMinutes = -1 },
		func(c *Config) { c.Heartbeat.Gmail.CredentialsFile = "relative.json" },
		func(c *Config) { c.Heartbeat.Gmail.TokenFile = c.Heartbeat.Gmail.CredentialsFile },
	} {
		changed := *c
		mutate(&changed)
		if changed.validateGmailHeartbeat() == nil {
			t.Fatal("unsafe configuration accepted")
		}
	}
}
