package config

import (
	"strings"
	"testing"
)

func TestResearchStructuredIndexDefaultsDisabled(t *testing.T) {
	var cfg Config
	cfg.setDefaults()
	if cfg.Research.StructuredIndex.PageIndex.Enabled || cfg.Research.StructuredIndex.CodeGraph.Enabled || len(cfg.Research.StructuredIndex.TrustedPDFs) != 0 {
		t.Fatalf("structured index must remain disabled by default: %+v", cfg.Research.StructuredIndex)
	}
	if err := cfg.validateResearchStructuredIndexConfig(); err != nil {
		t.Fatalf("disabled structured index config: %v", err)
	}
}

func TestResearchStructuredIndexRequiresAuthenticatedOwnerAndExactPDFIdentity(t *testing.T) {
	cfg := Config{}
	cfg.setDefaults()
	cfg.Research.Enabled = true
	cfg.Research.ArtifactRoot = "/private/research"
	cfg.Research.StructuredIndex.PageIndex.Enabled = true
	cfg.Research.StructuredIndex.PageIndex.PythonExecutable = "/private/runtime/python/bin/python"
	cfg.Research.StructuredIndex.PageIndex.RuntimeRoot = "/private/runtime/python"
	cfg.Research.StructuredIndex.PageIndex.RuntimeManifestSHA256 = "sha256:" + strings.Repeat("a", 64)
	cfg.Research.StructuredIndex.PageIndex.TempRoot = "/private/research/tmp"
	cfg.Research.StructuredIndex.PageIndex.TimeoutSeconds = 60
	cfg.Research.StructuredIndex.TrustedPDFs = []ResearchTrustedPDFConfig{{
		DocumentRef: "doc:ledger", Revision: "rev-1", OriginalSHA256: "sha256:" + strings.Repeat("b", 64),
		DataScope: "user", OwnerID: "ren", RightsBasis: "owner-authorized-retention", RetentionAllowed: true,
	}}
	if err := cfg.validateResearchStructuredIndexConfig(); err == nil || !strings.Contains(err.Error(), "authenticated local user owner") {
		t.Fatalf("missing authenticated owner = %v, want owner validation error", err)
	}
	cfg.LocalAgentOps = LocalAgentOpsConfig{Enabled: true, UserID: "ren"}
	if err := cfg.validateResearchStructuredIndexConfig(); err != nil {
		t.Fatalf("valid exact trusted PDF config: %v", err)
	}
	cfg.Research.StructuredIndex.TrustedPDFs[0].OriginalSHA256 = "sha256:" + strings.Repeat("B", 64)
	if err := cfg.validateResearchStructuredIndexConfig(); err == nil || !strings.Contains(err.Error(), "original_sha256") {
		t.Fatalf("uppercase/noncanonical original hash = %v, want hash validation error", err)
	}
}

func TestResearchStructuredIndexCodeGraphRequiresRegisteredRepositoryAndNestedRuntime(t *testing.T) {
	cfg := Config{}
	cfg.setDefaults()
	cfg.Research.Enabled = true
	cfg.Research.ArtifactRoot = "/private/research"
	cfg.LocalAgentOps = LocalAgentOpsConfig{Enabled: true, UserID: "ren"}
	cfg.Research.StructuredIndex.CodeGraph = ResearchCodeGraphConfig{
		Enabled: true, Executable: "/private/cbm/codebase-memory-mcp", BinarySHA256: "sha256:" + strings.Repeat("a", 64),
		RuntimeVersion: "0.12.1", SourceCommit: strings.Repeat("c", 40), CacheRoot: "/private/research/cbm",
		RuntimeRoot: "/private/research/cbm/runtime", TempRoot: "/private/research/tmp", TimeoutSeconds: 60,
	}
	if err := cfg.validateResearchStructuredIndexConfig(); err == nil || !strings.Contains(err.Error(), "registered repositories") {
		t.Fatalf("missing registered repository = %v, want repository validation error", err)
	}
	cfg.Research.Repositories = []ResearchRepositoryConfig{{
		Ref: "repo:fixture", Path: "/private/source/fixture", AllowedRevisions: []string{strings.Repeat("d", 40)}, Scope: "user", OwnerID: "ren",
	}}
	if err := cfg.validateResearchStructuredIndexConfig(); err != nil {
		t.Fatalf("valid registered CodeGraph config: %v", err)
	}
	cfg.Research.StructuredIndex.CodeGraph.RuntimeRoot = "/outside/runtime"
	if err := cfg.validateResearchStructuredIndexConfig(); err == nil || !strings.Contains(err.Error(), "runtime identity, cache layout, or timeout") {
		t.Fatalf("runtime outside trusted cache root = %v, want runtime validation error", err)
	}
}
