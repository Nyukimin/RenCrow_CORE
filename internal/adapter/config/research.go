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
}

// ResearchSemanticIndexConfig enables explicit indexing through a registered
// local Gateway service reference. It never selects a physical model.
type ResearchSemanticIndexConfig struct {
	Enabled    bool   `yaml:"enabled"`
	ServiceRef string `yaml:"service_ref"`
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
