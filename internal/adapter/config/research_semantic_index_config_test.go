package config

import (
	"strings"
	"testing"
)

func TestResearchSemanticIndexDefaultsDisabledWithRegisteredServiceRef(t *testing.T) {
	var cfg Config
	cfg.setDefaults()
	if cfg.Research.SemanticIndex.Enabled {
		t.Fatal("semantic index must remain disabled by default")
	}
	if cfg.Research.SemanticIndex.ServiceRef != DefaultResearchSemanticIndexServiceRef {
		t.Fatalf("service_ref=%q want %q", cfg.Research.SemanticIndex.ServiceRef, DefaultResearchSemanticIndexServiceRef)
	}
}

func TestResearchSemanticIndexRequiresOwnerAndValidServiceRef(t *testing.T) {
	cfg := Config{}
	cfg.setDefaults()
	cfg.Research.SemanticIndex.Enabled = true
	if err := cfg.validateResearchSemanticIndexConfig(); err == nil || !strings.Contains(err.Error(), "research.enabled") {
		t.Fatalf("enabled index without Research owner = %v, want Research dependency error", err)
	}

	cfg.Research.Enabled = true
	if err := cfg.validateResearchSemanticIndexConfig(); err != nil {
		t.Fatalf("enabled index with configured owner and Gateway = %v", err)
	}
	cfg.Research.SemanticIndex.ServiceRef = "research_embed_local_v2"
	if err := cfg.validateResearchSemanticIndexConfig(); err != nil {
		t.Fatalf("normalized service_ref was rejected: %v", err)
	}
	cfg.Research.SemanticIndex.ServiceRef = "research/service"
	if err := cfg.validateResearchSemanticIndexConfig(); err == nil || !strings.Contains(err.Error(), "service_ref") {
		t.Fatalf("invalid service_ref = %v, want validation error", err)
	}
	cfg.Research.SemanticIndex.ServiceRef = ""
	if err := cfg.validateResearchSemanticIndexConfig(); err == nil || !strings.Contains(err.Error(), "service_ref is required") {
		t.Fatalf("missing enabled service_ref = %v, want required error", err)
	}
}
