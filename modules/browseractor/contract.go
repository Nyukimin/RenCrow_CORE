package browseractor

const SchemaVersion = "1.0"

const (
	StatusCompleted = "completed"
	StatusFailed    = "failed"
)

type Viewport struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type RunRequest struct {
	SchemaVersion    string   `json:"schema_version,omitempty"`
	RunID            string   `json:"run_id,omitempty"`
	Goal             string   `json:"goal,omitempty"`
	StartURL         string   `json:"start_url"`
	ProfileID        string   `json:"profile_id,omitempty"`
	StorageStatePath string   `json:"storage_state_path,omitempty"`
	Headless         bool     `json:"headless"`
	Viewport         Viewport `json:"viewport,omitempty"`
	AllowedOrigins   []string `json:"allowed_origins,omitempty"`
	ArtifactDir      string   `json:"artifact_dir,omitempty"`
	TimeoutMS        int      `json:"timeout_ms,omitempty"`
	MaxActions       int      `json:"max_actions,omitempty"`
	SaveTrace        bool     `json:"save_trace"`
	SaveScreenshot   bool     `json:"save_screenshot"`
	MaskSecrets      bool     `json:"mask_secrets"`
	Actions          []Action `json:"actions"`
}

type Action struct {
	Type      string `json:"type"`
	Selector  string `json:"selector,omitempty"`
	Value     string `json:"value,omitempty"`
	Key       string `json:"key,omitempty"`
	Name      string `json:"name,omitempty"`
	TimeoutMS int    `json:"timeout_ms,omitempty"`
}

type RunResponse struct {
	SchemaVersion string            `json:"schema_version,omitempty"`
	RunID         string            `json:"run_id,omitempty"`
	Status        string            `json:"status"`
	RiskLevel     string            `json:"risk_level,omitempty"`
	StartedAt     string            `json:"started_at,omitempty"`
	CompletedAt   string            `json:"completed_at,omitempty"`
	StartURL      string            `json:"start_url,omitempty"`
	FinalURL      string            `json:"final_url,omitempty"`
	Title         string            `json:"title,omitempty"`
	ArtifactDir   string            `json:"artifact_dir,omitempty"`
	Artifacts     map[string]string `json:"artifacts,omitempty"`
	Actions       []ActionResult    `json:"actions,omitempty"`
	Warnings      []string          `json:"warnings,omitempty"`
	Error         *Error            `json:"error,omitempty"`
}

type ActionResult struct {
	ActionID    string `json:"action_id,omitempty"`
	Type        string `json:"type,omitempty"`
	Status      string `json:"status,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
	CompletedAt string `json:"completed_at,omitempty"`
	Error       string `json:"error,omitempty"`
}

type Error struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

type DoctorResponse struct {
	SchemaVersion string        `json:"schema_version,omitempty"`
	OK            bool          `json:"ok"`
	Checks        []DoctorCheck `json:"checks,omitempty"`
}

type DoctorCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}
