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
	State                State    `json:"state"`
	PolicyRoot           string   `json:"policy_root"`
	ContractRevision     string   `json:"contract_revision"`
	BundleID             string   `json:"bundle_id,omitempty"`
	BundleRevision       string   `json:"bundle_revision,omitempty"`
	ContentSHA256        string   `json:"content_sha256,omitempty"`
	MinimumCoreContract  string   `json:"minimum_core_contract,omitempty"`
	DeploymentProfile    string   `json:"deployment_profile"`
	DisabledCapabilities []string `json:"disabled_capabilities"`
	Error                string   `json:"error,omitempty"`
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
	Rules         []DataHandlingRule `yaml:"rules"`
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
