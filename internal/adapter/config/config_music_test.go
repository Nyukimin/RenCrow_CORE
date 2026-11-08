package config

import (
	"strings"
	"testing"
)

func validMusicConfigForTest() *Config {
	cfg := &Config{Server: ServerConfig{Port: 18790}, Session: SessionConfig{StorageDir: "./data"}}
	cfg.Coder1.Name, cfg.Coder2.Name, cfg.Coder3.Name, cfg.Coder4.Name = "aka", "ao", "kin", "gin"
	cfg.setDefaults()
	return cfg
}

func TestMusicGatewayDefaultsOffAndAcceptsNumericLoopbackHTTP(t *testing.T) {
	cfg := validMusicConfigForTest()
	if cfg.Music.Enabled {
		t.Fatal("music gateway must default to disabled")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config validation failed: %v", err)
	}

	cfg.Music = MusicConfig{
		Enabled: true, Endpoint: "http://127.0.0.1:18796", ActorID: "ren", TokenEnv: "REN_CROW_MUSIC_TOKEN",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("numeric loopback project gateway config rejected: %v", err)
	}
}

func TestMusicGatewayRejectsNonLoopbackAndIncompleteEnabledConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  MusicConfig
		want string
	}{
		{name: "hostname", cfg: MusicConfig{Enabled: true, Endpoint: "http://localhost:18796", ActorID: "ren", TokenEnv: "REN_CROW_MUSIC_TOKEN"}, want: "music.endpoint"},
		{name: "remote ip", cfg: MusicConfig{Enabled: true, Endpoint: "http://192.0.2.4:18796", ActorID: "ren", TokenEnv: "REN_CROW_MUSIC_TOKEN"}, want: "music.endpoint"},
		{name: "https", cfg: MusicConfig{Enabled: true, Endpoint: "https://127.0.0.1:18796", ActorID: "ren", TokenEnv: "REN_CROW_MUSIC_TOKEN"}, want: "music.endpoint"},
		{name: "path", cfg: MusicConfig{Enabled: true, Endpoint: "http://127.0.0.1:18796/nested", ActorID: "ren", TokenEnv: "REN_CROW_MUSIC_TOKEN"}, want: "music.endpoint"},
		{name: "missing actor", cfg: MusicConfig{Enabled: true, Endpoint: "http://127.0.0.1:18796", TokenEnv: "REN_CROW_MUSIC_TOKEN"}, want: "music.actor_id"},
		{name: "actor too long", cfg: MusicConfig{Enabled: true, Endpoint: "http://127.0.0.1:18796", ActorID: strings.Repeat("a", 201), TokenEnv: "REN_CROW_MUSIC_TOKEN"}, want: "music.actor_id"},
		{name: "missing token env", cfg: MusicConfig{Enabled: true, Endpoint: "http://127.0.0.1:18796", ActorID: "ren"}, want: "music.token_env"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validMusicConfigForTest()
			cfg.Music = tt.cfg
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error = %v, want error containing %q", err, tt.want)
			}
		})
	}
}
