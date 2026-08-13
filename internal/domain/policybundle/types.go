package policybundle

const (
	SchemaVersion    = 1
	ContractRevision = "global-policy/v1"
)

type State string

const (
	StateMissing State = "missing"
	StateActive  State = "active"
	StateInvalid State = "invalid"
)

type Status struct {
	State                   State    `json:"state"`
	PolicyRoot              string   `json:"policy_root"`
	ContractRevision        string   `json:"contract_revision"`
	BundleID                string   `json:"bundle_id,omitempty"`
	BundleRevision          string   `json:"bundle_revision,omitempty"`
	ContentSHA256           string   `json:"content_sha256,omitempty"`
	MinimumCoreContract     string   `json:"minimum_core_contract,omitempty"`
	DeploymentProfile       string   `json:"deployment_profile"`
	DisabledCapabilities    []string `json:"disabled_capabilities"`
	Error                   string   `json:"error,omitempty"`
	LastReloadState         State    `json:"last_reload_state"`
	LastReloadAt            string   `json:"last_reload_at,omitempty"`
	LastSuccessfulLoadAt    string   `json:"last_successful_load_at,omitempty"`
	LastReloadError         string   `json:"last_reload_error,omitempty"`
	ActiveRevisionPreserved bool     `json:"active_revision_preserved"`
}

type Snapshot struct {
	BundleID           string
	BundleRevision     string
	ContentSHA256      string
	Capabilities       map[string]bool
	ExternalActions    map[string]string
	ProductionDisabled map[string]bool
}

type Manifest struct {
	SchemaVersion       int         `yaml:"schema_version"`
	BundleID            string      `yaml:"bundle_id"`
	Revision            string      `yaml:"revision"`
	CreatedAt           string      `yaml:"created_at"`
	MinimumCoreContract string      `yaml:"minimum_core_contract"`
	ContentSHA256       string      `yaml:"content_sha256"`
	Files               []FileEntry `yaml:"files"`
}

type FileEntry struct {
	Path   string `yaml:"path"`
	SHA256 string `yaml:"sha256"`
}

type GlobalPolicy struct {
	SchemaVersion     int    `yaml:"schema_version"`
	PolicyID          string `yaml:"policy_id"`
	DefaultSideEffect string `yaml:"default_side_effect"`
}

type CapabilityPolicy struct {
	SchemaVersion int             `yaml:"schema_version"`
	PolicyID      string          `yaml:"policy_id"`
	Capabilities  map[string]bool `yaml:"capabilities"`
}

type AuthorizationPolicy struct {
	SchemaVersion  int             `yaml:"schema_version"`
	PolicyID       string          `yaml:"policy_id"`
	Authorizations []Authorization `yaml:"authorizations"`
}

type Authorization struct {
	ID           string   `yaml:"id"`
	Capabilities []string `yaml:"capabilities"`
	Required     bool     `yaml:"required"`
}

type DataHandlingPolicy struct {
	SchemaVersion int                `yaml:"schema_version"`
	PolicyID      string             `yaml:"policy_id"`
	Recall        DatabaseRecallRule `yaml:"database_recall"`
	Rules         []DataHandlingRule `yaml:"rules"`
}

// DatabaseRecallRule makes every durable database a required, purpose-scoped
// recall source without permitting raw access or catalog-wide scans.
type DatabaseRecallRule struct {
	AllDatabasesAreRecallSources bool `yaml:"all_databases_are_recall_sources"`
	RouteRequired                bool `yaml:"route_required"`
	MissingRouteIsIncomplete     bool `yaml:"missing_route_is_incomplete"`
	RawAccessForbidden           bool `yaml:"raw_access_forbidden"`
	CatalogWideScanForbidden     bool `yaml:"catalog_wide_scan_forbidden"`
}

type DataHandlingRule struct {
	ID                string   `yaml:"id"`
	DataClass         string   `yaml:"data_class"`
	AllowedOperations []string `yaml:"allowed_operations"`
}

type ExternalActionPolicy struct {
	SchemaVersion int               `yaml:"schema_version"`
	PolicyID      string            `yaml:"policy_id"`
	Actions       map[string]string `yaml:"actions"`
}

type DeploymentPolicy struct {
	SchemaVersion        int      `yaml:"schema_version"`
	PolicyID             string   `yaml:"policy_id"`
	Profile              string   `yaml:"profile"`
	DisabledCapabilities []string `yaml:"disabled_capabilities"`
}
