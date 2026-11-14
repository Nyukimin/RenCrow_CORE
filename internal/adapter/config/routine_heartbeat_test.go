package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeRoutineConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "server:\n  port: 8080\nheartbeat:\n" + body
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func validRoutineHeartbeatConfig() *Config {
	return &Config{
		Heartbeat: HeartbeatConfig{
			Enabled:               true,
			AgentSchedulerEnabled: true,
			Routine: RoutineHeartbeatConfig{
				Enabled:      true,
				SharedRooms:  []string{"routine-shared"},
				PrivateRooms: map[string]string{"mio": "routine-mio", "shiro": "routine-shiro"},
			},
		},
		BoardCommunication: BoardCommunicationConfig{Enabled: true},
		LocalAgentOps:      LocalAgentOpsConfig{Enabled: true, UserID: "ren"},
	}
}

func TestRoutineHeartbeatYAMLKeysAndDisabledDefault(t *testing.T) {
	path := writeRoutineConfig(t, "  routine:\n    shared_rooms: [routine-shared]\n    private_rooms:\n      mio: routine-mio\n    world_life_token_file: /tmp/world-life.token\n")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Heartbeat.Routine.Enabled {
		t.Fatal("routine.enabled defaulted to true")
	}
	if got := cfg.Heartbeat.Routine.SharedRooms; len(got) != 1 || got[0] != "routine-shared" {
		t.Fatalf("shared_rooms = %#v", got)
	}
	if got := cfg.Heartbeat.Routine.PrivateRooms["mio"]; got != "routine-mio" {
		t.Fatalf("private_rooms.mio = %q", got)
	}
	if got := cfg.Heartbeat.Routine.WorldLifeTokenFile; got != "/tmp/world-life.token" {
		t.Fatalf("world_life_token_file = %q", got)
	}
}

func TestRoutineHeartbeatRequiresRuntimeAndBoardOwners(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Config)
		want string
	}{
		{name: "heartbeat", edit: func(c *Config) { c.Heartbeat.Enabled = false }, want: "heartbeat.enabled"},
		{name: "agent scheduler", edit: func(c *Config) { c.Heartbeat.AgentSchedulerEnabled = false }, want: "agent_scheduler_enabled"},
		{name: "board", edit: func(c *Config) { c.BoardCommunication.Enabled = false }, want: "board_communication.enabled"},
		{name: "local owner", edit: func(c *Config) { c.LocalAgentOps = LocalAgentOpsConfig{} }, want: "local_agent_ops"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validRoutineHeartbeatConfig()
			tt.edit(cfg)
			if err := cfg.validateRoutineHeartbeat(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validateRoutineHeartbeat error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestRoutineHeartbeatAllowsDiaryOnlyScopeWithoutRooms(t *testing.T) {
	cfg := validRoutineHeartbeatConfig()
	cfg.Heartbeat.Routine.SharedRooms = nil
	cfg.Heartbeat.Routine.PrivateRooms = nil
	if err := cfg.validateRoutineHeartbeat(); err != nil {
		t.Fatalf("diary-only routine config: %v", err)
	}
}

func TestRoutineHeartbeatRejectsNoncanonicalActorAndRoom(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Config)
	}{
		{name: "actor", edit: func(c *Config) { c.Heartbeat.Routine.PrivateRooms["Mio"] = "routine-upper" }},
		{name: "private room", edit: func(c *Config) { c.Heartbeat.Routine.PrivateRooms["mio"] = "bad\nroom" }},
		{name: "shared room", edit: func(c *Config) { c.Heartbeat.Routine.SharedRooms = []string{""} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validRoutineHeartbeatConfig()
			tt.edit(cfg)
			if err := cfg.validateRoutineHeartbeat(); err == nil {
				t.Fatal("validateRoutineHeartbeat unexpectedly accepted invalid scope")
			}
		})
	}
}

func TestRoutineHeartbeatRequiresCompleteAuthenticatedWorldScope(t *testing.T) {
	cfg := validRoutineHeartbeatConfig()
	cfg.Heartbeat.Routine.WorldURL = "https://world.example.invalid"
	cfg.Heartbeat.Routine.WorldID = "world"
	if err := cfg.validateRoutineHeartbeat(); err == nil || !strings.Contains(err.Error(), "timeline") {
		t.Fatalf("validateRoutineHeartbeat error = %v, want incomplete timeline scope", err)
	}

	cfg = validRoutineHeartbeatConfig()
	cfg.Heartbeat.Routine.WorldURL = "https://world.example.invalid"
	cfg.Heartbeat.Routine.WorldID = "world"
	cfg.Heartbeat.Routine.TimelineID = "main"
	cfg.Heartbeat.Routine.TimelineEpoch = "epoch-1"
	cfg.Heartbeat.Routine.WorldActors = map[string]string{"mio": "world-mio", "shiro": "world-shiro"}
	cfg.Heartbeat.Routine.WorldReadTokenFile = filepath.Join(t.TempDir(), "missing-token")
	if err := cfg.validateRoutineHeartbeat(); err == nil || !strings.Contains(err.Error(), "world_read_token_file") {
		t.Fatalf("validateRoutineHeartbeat error = %v, want missing credential file", err)
	}

	cfg.Heartbeat.Routine.WorldURL = "http://192.0.2.1:8080"
	if err := cfg.validateRoutineHeartbeat(); err == nil || !strings.Contains(err.Error(), "world_url") {
		t.Fatalf("validateRoutineHeartbeat error = %v, want unsafe WORLD URL", err)
	}
}

func TestRoutineHeartbeatBindsExactWorldActorIDsAndAllowsSharedRoomAlias(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating a current-user-only Windows DACL is covered by the platform secret-file tests")
	}
	tokenPath := filepath.Join(t.TempDir(), "world-read.token")
	if err := os.WriteFile(tokenPath, []byte("world-read-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := validRoutineHeartbeatConfig()
	cfg.Heartbeat.Routine.WorldURL = "http://localhost:9000/v1"
	cfg.Heartbeat.Routine.WorldID = "living-world"
	cfg.Heartbeat.Routine.TimelineID = "main"
	cfg.Heartbeat.Routine.TimelineEpoch = "epoch-1"
	cfg.Heartbeat.Routine.WorldReadTokenFile = tokenPath
	cfg.Heartbeat.Routine.WorldActors = map[string]string{"mio": "Actor:妙", "shiro": "Actor-shiro"}
	cfg.Heartbeat.Routine.SharedRooms = []string{"room:居間"}
	cfg.Heartbeat.Routine.PrivateRooms = map[string]string{"mio": "room:居間", "shiro": "room:shiro"}
	if err := cfg.validateRoutineHeartbeat(); err != nil {
		t.Fatalf("validateRoutineHeartbeat: %v", err)
	}
	got, err := ReadRoutineWorldBearerToken(tokenPath)
	if err != nil || got != "world-read-token" {
		t.Fatalf("ReadRoutineWorldBearerToken = %q, %v", got, err)
	}
}

func TestRoutineHeartbeatRequiresDistinctWorldLifeToken(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating current-user-only Windows DACLs is covered by platform secret-file tests")
	}
	readPath := filepath.Join(t.TempDir(), "world-read.token")
	lifePath := filepath.Join(t.TempDir(), "world-life.token")
	if err := os.WriteFile(readPath, []byte("world-read-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lifePath, []byte("world-life-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := validRoutineHeartbeatConfig()
	cfg.Heartbeat.Routine.WorldURL = "http://localhost:9000/v1"
	cfg.Heartbeat.Routine.WorldID = "living-world"
	cfg.Heartbeat.Routine.TimelineID = "main"
	cfg.Heartbeat.Routine.TimelineEpoch = "epoch-1"
	cfg.Heartbeat.Routine.WorldReadTokenFile = readPath
	cfg.Heartbeat.Routine.WorldLifeTokenFile = lifePath
	cfg.Heartbeat.Routine.WorldActors = map[string]string{"mio": "world-mio", "shiro": "world-shiro"}
	if err := cfg.validateRoutineHeartbeat(); err != nil {
		t.Fatalf("validate distinct world life token: %v", err)
	}
	got, err := ReadRoutineWorldLifeBearerToken(lifePath)
	if err != nil || got != "world-life-token" {
		t.Fatalf("ReadRoutineWorldLifeBearerToken = %q, %v", got, err)
	}

	if err := os.WriteFile(lifePath, []byte("world-read-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cfg.validateRoutineHeartbeat(); err == nil || !strings.Contains(err.Error(), "distinct") {
		t.Fatalf("same WORLD read/life token error = %v, want distinct-token error", err)
	}
}

func TestRoutineHeartbeatRejectsAmbiguousWorldIdentityBinding(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Config)
		want string
	}{
		{name: "duplicate world actor", edit: func(c *Config) { c.Heartbeat.Routine.WorldActors = map[string]string{"mio": "same", "shiro": "same"} }, want: "must be unique"},
		{name: "unbound private actor", edit: func(c *Config) { c.Heartbeat.Routine.WorldActors = map[string]string{"mio": "world-mio"} }, want: "present in world_actors"},
		{name: "noncanonical core actor", edit: func(c *Config) { c.Heartbeat.Routine.WorldActors = map[string]string{"Mio": "world-mio"} }, want: "canonical CORE actor"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokenPath := filepath.Join(t.TempDir(), "world-read.token")
			if err := os.WriteFile(tokenPath, []byte("world-read-token"), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg := validRoutineHeartbeatConfig()
			cfg.Heartbeat.Routine.WorldURL = "https://world.example.invalid"
			cfg.Heartbeat.Routine.WorldID = "world"
			cfg.Heartbeat.Routine.TimelineID = "timeline"
			cfg.Heartbeat.Routine.TimelineEpoch = "epoch-1"
			cfg.Heartbeat.Routine.WorldReadTokenFile = tokenPath
			tt.edit(cfg)
			if err := cfg.validateRoutineHeartbeat(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validateRoutineHeartbeat error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestRoutineHeartbeatValidatesConfiguredMedia(t *testing.T) {
	cfg := validRoutineHeartbeatConfig()
	cfg.BrowserActor.Enabled = true
	cfg.Heartbeat.Routine.Media = map[string][]RoutineMediaItem{
		"mio": {{ID: "mdn-short", Kind: "video", Title: "MDN short", Credit: "MDN", URL: "https://media.example.invalid/short.mp4"}},
	}
	if err := cfg.validateRoutineHeartbeat(); err != nil {
		t.Fatalf("valid media config: %v", err)
	}

	tests := []struct {
		name string
		edit func(*Config)
		want string
	}{
		{name: "browser actor disabled", edit: func(c *Config) { c.BrowserActor.Enabled = false }, want: "browser_actor.enabled"},
		{name: "noncanonical actor", edit: func(c *Config) { c.Heartbeat.Routine.Media["Mio"] = c.Heartbeat.Routine.Media["mio"] }, want: "canonical CORE actor"},
		{name: "unknown kind", edit: func(c *Config) { c.Heartbeat.Routine.Media["mio"][0].Kind = "audio" }, want: "kind"},
		{name: "missing stable id", edit: func(c *Config) { c.Heartbeat.Routine.Media["mio"][0].ID = "" }, want: "id"},
		{name: "credential url", edit: func(c *Config) {
			c.Heartbeat.Routine.Media["mio"][0].URL = "https://user:pass@media.example.invalid/short.mp4"
		}, want: "HTTPS"},
		{name: "query url", edit: func(c *Config) {
			c.Heartbeat.Routine.Media["mio"][0].URL = "https://media.example.invalid/short.mp4?token=x"
		}, want: "HTTPS"},
		{name: "fragment url", edit: func(c *Config) {
			c.Heartbeat.Routine.Media["mio"][0].URL = "https://media.example.invalid/short.mp4#part"
		}, want: "HTTPS"},
		{name: "http url", edit: func(c *Config) { c.Heartbeat.Routine.Media["mio"][0].URL = "http://media.example.invalid/short.mp4" }, want: "HTTPS"},
		{name: "duplicate id", edit: func(c *Config) {
			c.Heartbeat.Routine.Media["mio"] = append(c.Heartbeat.Routine.Media["mio"], c.Heartbeat.Routine.Media["mio"][0])
		}, want: "duplicate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			caseCfg := validRoutineHeartbeatConfig()
			caseCfg.BrowserActor.Enabled = true
			caseCfg.Heartbeat.Routine.Media = map[string][]RoutineMediaItem{
				"mio": {{ID: "mdn-short", Kind: "video", Title: "MDN short", Credit: "MDN", URL: "https://media.example.invalid/short.mp4"}},
			}
			tt.edit(caseCfg)
			if err := caseCfg.validateRoutineHeartbeat(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validateRoutineHeartbeat error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestRoutineHeartbeatLimitsMediaItemsPerActor(t *testing.T) {
	cfg := validRoutineHeartbeatConfig()
	cfg.BrowserActor.Enabled = true
	items := make([]RoutineMediaItem, maxRoutineMediaItemsPerActor+1)
	for index := range items {
		items[index] = RoutineMediaItem{
			ID: fmt.Sprintf("clip-%02d", index), Kind: "video", Title: "短い映像", Credit: "提供元",
			URL: fmt.Sprintf("https://media.example.invalid/clip-%02d.mp4", index),
		}
	}
	cfg.Heartbeat.Routine.Media = map[string][]RoutineMediaItem{"mio": items}
	if err := cfg.validateRoutineHeartbeat(); err == nil || !strings.Contains(err.Error(), "1..32") {
		t.Fatalf("validateRoutineHeartbeat error = %v, want media item bound", err)
	}
}
