package classifierlab

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	coreconfig "github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
)

const LegacyBaselineSchemaVersion = "routing-classifier-legacy-baseline/v1"

var (
	legacyAPIKeyEnvPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	legacyBaselineSHA256   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// LegacyBaseline is a body-free identity for the resolved legacy classifier
// configuration. It deliberately contains the API-key environment variable
// name, never the value.
type LegacyBaseline struct {
	SchemaVersion          string `json:"schema_version"`
	ManifestID             string `json:"manifest_id"`
	ConfigSHA256           string `json:"config_sha256"`
	ClassifierPromptSHA256 string `json:"classifier_prompt_sha256"`
	GatewayURL             string `json:"gateway_url"`
	GatewayTimeoutMS       int64  `json:"gateway_timeout_ms"`
	AuthMode               string `json:"auth_mode"`
	APIKeyEnv              string `json:"api_key_env"`
	ContextBudgetTokens    int    `json:"context_budget_tokens"`
	MioAgentID             string `json:"mio_agent_id"`
	ChatExecutionRole      string `json:"chat_execution_role"`
	ChatExecutionAlias     string `json:"chat_execution_alias"`
	LegacyModelUnknown     bool   `json:"legacy_model_unknown"`
}

// ResolveLegacyBaseline reads the explicit CORE config through the same
// config.LoadConfig path as runtime startup, then describes the effective
// classifier prompt and Mio/Chat Gateway settings without starting services.
func ResolveLegacyBaseline(configPath string) (LegacyBaseline, error) {
	if strings.TrimSpace(configPath) == "" {
		return LegacyBaseline{}, errors.New("legacy baseline requires an explicit CORE config path")
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		return LegacyBaseline{}, fmt.Errorf("read CORE config for legacy baseline: %w", err)
	}
	cfg, err := coreconfig.LoadConfig(configPath)
	if err != nil {
		return LegacyBaseline{}, fmt.Errorf("load CORE config for legacy baseline: %w", err)
	}
	configAfterLoad, err := os.ReadFile(configPath)
	if err != nil {
		return LegacyBaseline{}, fmt.Errorf("recheck CORE config for legacy baseline: %w", err)
	}
	if !bytes.Equal(configBytes, configAfterLoad) {
		return LegacyBaseline{}, errors.New("CORE config changed while resolving legacy baseline")
	}
	if cfg == nil || cfg.Prompts == nil {
		return LegacyBaseline{}, errors.New("CORE config did not resolve classifier prompts")
	}
	if strings.TrimSpace(cfg.Prompts.Classifier) == "" || !hasClassifierPromptSource(cfg) {
		return LegacyBaseline{}, errors.New("CORE classifier prompt is missing or empty")
	}

	apiKeyEnv := strings.TrimSpace(cfg.LLMGateway.APIKeyEnv)
	authMode := "none"
	if apiKeyEnv != "" {
		if !legacyAPIKeyEnvPattern.MatchString(apiKeyEnv) {
			return LegacyBaseline{}, errors.New("legacy baseline requires a valid Gateway API-key environment variable name")
		}
		apiKey, ok := os.LookupEnv(apiKeyEnv)
		if !ok || strings.TrimSpace(apiKey) == "" {
			return LegacyBaseline{}, errors.New("legacy baseline requires the Gateway API-key environment variable to be present")
		}
		authMode = "env"
	}

	gatewayURL, err := canonicalLoopbackGatewayURL(cfg.LLMGateway.BaseURL)
	if err != nil {
		return LegacyBaseline{}, err
	}
	if cfg.LLMGateway.TimeoutSec < 1 || int64(cfg.LLMGateway.TimeoutSec) > math.MaxInt64/int64(time.Second) {
		return LegacyBaseline{}, errors.New("legacy baseline requires a representable positive Gateway timeout")
	}

	baseline := LegacyBaseline{
		SchemaVersion:          LegacyBaselineSchemaVersion,
		ConfigSHA256:           legacyBaselineHash(configBytes),
		ClassifierPromptSHA256: legacyBaselineHash([]byte(cfg.Prompts.Classifier)),
		GatewayURL:             gatewayURL,
		GatewayTimeoutMS:       int64(cfg.LLMGateway.TimeoutSec) * 1000,
		AuthMode:               authMode,
		APIKeyEnv:              apiKeyEnv,
		ContextBudgetTokens:    cfg.AIWorkflow.ContextBudgetTokens,
		MioAgentID:             "mio",
		ChatExecutionRole:      "chat",
		ChatExecutionAlias:     "mio",
		LegacyModelUnknown:     true,
	}
	baseline.ManifestID = baseline.CalculatedManifestID()
	return baseline, nil
}

// CalculatedManifestID hashes canonical JSON for this DTO with manifest_id
// blank, so callers can independently recompute the identity.
func (baseline LegacyBaseline) CalculatedManifestID() string {
	baseline.ManifestID = ""
	canonicalJSON, err := json.Marshal(baseline)
	if err != nil {
		// The DTO contains only JSON scalar fields and cannot fail to marshal.
		return ""
	}
	return legacyBaselineHash(canonicalJSON)
}

// ValidateExpectedManifestID verifies a caller-pinned baseline identity.
func (baseline LegacyBaseline) ValidateExpectedManifestID(expected string) error {
	if !legacyBaselineSHA256.MatchString(expected) || baseline.ManifestID != baseline.CalculatedManifestID() || expected != baseline.ManifestID {
		return errors.New("legacy baseline manifest ID does not match the expected hash")
	}
	return nil
}

func canonicalLoopbackGatewayURL(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", errors.New("legacy baseline requires a canonical loopback Gateway URL")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Opaque != "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" ||
		parsed.Path != "" || parsed.RawPath != "" || parsed.Hostname() != "127.0.0.1" {
		return "", errors.New("legacy baseline requires a canonical loopback Gateway URL")
	}
	portText := parsed.Port()
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 || parsed.Host != "127.0.0.1:"+strconv.Itoa(port) || parsed.String() != raw {
		return "", errors.New("legacy baseline requires a canonical loopback Gateway URL")
	}
	return raw, nil
}

func hasClassifierPromptSource(cfg *coreconfig.Config) bool {
	if cfg == nil {
		return false
	}
	baseDir := cfg.PromptsDir
	workspaceDir := cfg.WorkspaceDir
	if workspaceDir != "" && workspaceDir != baseDir && nonemptyClassifierPromptFile(workspaceDir) {
		return true
	}
	return nonemptyClassifierPromptFile(baseDir)
}

func nonemptyClassifierPromptFile(directory string) bool {
	if strings.TrimSpace(directory) == "" {
		return false
	}
	contents, err := os.ReadFile(filepath.Join(directory, "classifier.md"))
	return err == nil && strings.TrimSpace(string(contents)) != ""
}

func legacyBaselineHash(contents []byte) string {
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}
