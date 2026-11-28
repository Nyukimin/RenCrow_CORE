package config

const DefaultResearchSemanticIndexServiceRef = "research_embedding_local"

// ResearchConfig is administrative, never a model argument. Existing URL,
// Task and data-owner policy still applies to every execution.
type ResearchConfig struct {
	Enabled                       bool                             `yaml:"enabled"`
	ArtifactRoot                  string                           `yaml:"artifact_root"`
	PermittedProviders            []string                         `yaml:"permitted_providers"`
	RetentionAllowedHosts         []string                         `yaml:"retention_allowed_hosts"`
	ProviderRetentionAllowedHosts map[string][]string              `yaml:"provider_retention_allowed_hosts"`
	PrimaryHosts                  []string                         `yaml:"primary_hosts"`
	AnalysisAlias                 string                           `yaml:"analysis_alias"`
	Repositories                  []ResearchRepositoryConfig       `yaml:"repositories"`
	MediaWikiSite                 string                           `yaml:"mediawiki_site"`
	PyPIAllowedLicenses           []string                         `yaml:"pypi_allowed_licenses"`
	ProviderAllowedLicenses       map[string][]string              `yaml:"provider_allowed_licenses"`
	BoardDestinations             []ResearchBoardDestinationConfig `yaml:"board_destinations"`
	SemanticIndex                 ResearchSemanticIndexConfig      `yaml:"semantic_index"`
	StructuredIndex               ResearchStructuredIndexConfig    `yaml:"structured_index"`
}

// ResearchSemanticIndexConfig enables explicit indexing through a registered
// local Gateway service reference. It never selects a physical model.
type ResearchSemanticIndexConfig struct {
	Enabled    bool   `yaml:"enabled"`
	ServiceRef string `yaml:"service_ref"`
}

// ResearchStructuredIndexConfig enables only explicitly selected local
// structure backends. TrustedPDFs bind opaque source references to current
// owner scope, rights, and an expected hash; request payloads never choose a
// path or identity.
type ResearchStructuredIndexConfig struct {
	PageIndex   ResearchPageIndexConfig    `yaml:"page_index"`
	CodeGraph   ResearchCodeGraphConfig    `yaml:"code_graph"`
	TrustedPDFs []ResearchTrustedPDFConfig `yaml:"trusted_pdfs"`
}

type ResearchPageIndexConfig struct {
	Enabled               bool   `yaml:"enabled"`
	PythonExecutable      string `yaml:"python_executable"`
	RuntimeRoot           string `yaml:"runtime_root"`
	RuntimeManifestSHA256 string `yaml:"runtime_manifest_sha256"`
	TempRoot              string `yaml:"temp_root"`
	TimeoutSeconds        int    `yaml:"timeout_seconds"`
}

type ResearchCodeGraphConfig struct {
	Enabled        bool   `yaml:"enabled"`
	Executable     string `yaml:"executable"`
	BinarySHA256   string `yaml:"binary_sha256"`
	RuntimeVersion string `yaml:"runtime_version"`
	SourceCommit   string `yaml:"source_commit"`
	CacheRoot      string `yaml:"cache_root"`
	RuntimeRoot    string `yaml:"runtime_root"`
	TempRoot       string `yaml:"temp_root"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
}

type ResearchTrustedPDFConfig struct {
	DocumentRef      string `yaml:"document_ref"`
	Revision         string `yaml:"revision"`
	OriginalSHA256   string `yaml:"original_sha256"`
	DataScope        string `yaml:"data_scope"`
	OwnerID          string `yaml:"owner_id"`
	RightsBasis      string `yaml:"rights_basis"`
	RetentionAllowed bool   `yaml:"retention_allowed"`
}

// ResearchBoardDestinationConfig selects an existing pinned BOARD connection.
// Requests name Ref only; identities and destinations are administrator-owned.
type ResearchBoardDestinationConfig struct {
	Ref         string   `yaml:"ref"`
	OwnerID     string   `yaml:"owner_id"`
	PrincipalID string   `yaml:"principal_id"`
	ChannelID   string   `yaml:"channel_id"`
	ThreadID    string   `yaml:"thread_id"`
	NewThread   bool     `yaml:"new_thread"`
	Recipients  []string `yaml:"recipients"`
	DataScope   string   `yaml:"data_scope"`
	Title       string   `yaml:"title"`
}

type ResearchRepositoryConfig struct {
	Ref              string   `yaml:"ref"`
	Path             string   `yaml:"path"`
	AllowedRevisions []string `yaml:"allowed_revisions"`
	Scope            string   `yaml:"scope"`
	OwnerID          string   `yaml:"owner_id"`
}
