package config

import "testing"

func TestConversationRuntimeDefaultsToLegacyAndRejectsUnknownVersion(t *testing.T) {
	cfg := &Config{
		Server:  ServerConfig{Port: 8080},
		Session: SessionConfig{StorageDir: "./data/sessions"},
		DCI:     DCIConfig{Enabled: boolConfigPtr(false)},
		Coder1:  CoderConfig{Name: "aka"},
		Coder2:  CoderConfig{Name: "ao"},
		Coder3:  CoderConfig{Name: "kin"},
		Coder4:  CoderConfig{Name: "gin"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with omitted conversation_runtime.version: %v, want the legacy default", err)
	}

	cfg.setDefaults()
	if cfg.ConversationRuntime.Version != "legacy" {
		t.Fatalf("conversation_runtime.version=%q, want legacy", cfg.ConversationRuntime.Version)
	}
	cfg.ConversationRuntime.Version = "experimental"
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error=nil, want unknown conversation runtime version rejection")
	}
}
