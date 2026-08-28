package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

const (
	verifierMinActorTokenBytes     = 32
	verifierMaxActorMessageBytes   = 32 << 10
	verifierMaxActorRequestIDBytes = 128
)

var verifierActorRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func runCanonicalActorE2E(ctx context.Context, options verifierOptions, _ manifestCheck, deps verifierDependencies) verifierOutcome {
	deps = normalizeVerifierDependencies(deps)
	if strings.TrimSpace(options.ActorTokenFile) == "" {
		configPath := strings.TrimSpace(options.ConfigPath)
		if configPath == "" {
			snapshot, outcome := readSystemdService(ctx, options, deps)
			if outcome.Status != "" {
				return verifierOutcome{Status: "blocked", FailureBoundary: "canonical Agent active config is unavailable"}
			}
			configPath = snapshot.ConfigPath
		}
		tokenPath, err := readCanonicalActorTokenPath(configPath)
		if err != nil {
			return verifierOutcome{Status: "blocked", FailureBoundary: "canonical Agent credentials are unavailable"}
		}
		options.ActorTokenFile = tokenPath
	}
	token, err := readVerifierActorToken(options.ActorTokenFile, deps)
	if err != nil {
		return verifierOutcome{Status: "blocked", FailureBoundary: "canonical Agent credentials are unavailable"}
	}
	requestID := strings.TrimSpace(options.RequestID)
	if requestID == "" {
		requestID = "core-verify-" + options.ObservedAt.UTC().Format("20060102T150405.000000000Z")
	}
	if !verifierActorRequestIDPattern.MatchString(requestID) || len([]byte(requestID)) > verifierMaxActorRequestIDBytes {
		return verifierOutcome{Status: "blocked", FailureBoundary: "canonical Agent request id is required"}
	}
	message := strings.TrimSpace(options.ActorMessage)
	if message == "" {
		message = "Read-only full-system verification. Reply briefly with OK."
	}
	if message == "" {
		return verifierOutcome{Status: "blocked", FailureBoundary: "canonical Agent request message is required"}
	}
	if len([]byte(message)) > verifierMaxActorMessageBytes {
		return verifierOutcome{Status: "blocked", FailureBoundary: "canonical Agent request message exceeds the bounded limit"}
	}
	baseURL, err := validateVerifierCoreURL(options.CoreURL)
	if err != nil {
		return verifierOutcome{Status: "blocked", FailureBoundary: "canonical CORE URL is not an allowed loopback endpoint"}
	}
	body, err := jsonActorRequest(message)
	if err != nil {
		return verifierOutcome{Status: "blocked", FailureBoundary: "canonical Agent request could not be encoded"}
	}
	response := verifierHTTPJSON(ctx, deps.HTTPClient, http.MethodPost, coreEndpoint(baseURL, "/v1/agent/ops"), map[string]string{
		"Accept":                        "application/json",
		"Content-Type":                  "application/json",
		"Authorization":                 "Bearer " + token,
		"X-Request-ID":                  requestID,
		"X-RenCrow-Client":              "RenCrow_CMD",
		"X-RenCrow-Interaction-Profile": "agent-ops",
	}, body)
	evidence := map[string]any{
		"route":           "/v1/agent/ops",
		"http_status":     response.StatusCode,
		"request_id_hash": sha256Text(requestID),
	}
	if response.Err != nil && response.StatusCode == 0 {
		return verifierOutcome{Status: "blocked", FailureBoundary: "canonical authenticated Agent route is unavailable", Evidence: evidence}
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return verifierOutcome{Status: "blocked", FailureBoundary: "canonical Agent authentication or interaction scope was rejected", Evidence: evidence}
	}
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed {
		return verifierOutcome{Status: "blocked", FailureBoundary: "canonical authenticated Agent route is unavailable", Evidence: evidence}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return verifierOutcome{Status: "failed", FailureBoundary: "canonical Agent route returned an unexpected HTTP status", Evidence: evidence}
	}
	if response.Err != nil || response.JSON == nil {
		return verifierOutcome{Status: "failed", FailureBoundary: "canonical Agent route returned invalid JSON", Evidence: evidence}
	}
	responseFields, err := validateCanonicalActorResponse(response.JSON, requestID)
	if err != nil {
		return verifierOutcome{Status: "failed", FailureBoundary: truncateVerifierMessage(err.Error()), Evidence: evidence}
	}
	evidence["response_request_id_hash"] = sha256Text(responseFields.RequestID)
	evidence["job_id_hash"] = sha256Text(responseFields.JobID)
	evidence["agent_id"] = responseFields.AgentID
	evidence["role"] = responseFields.Role
	evidence["route_name"] = responseFields.Route
	evidence["output_bytes"] = len([]byte(responseFields.Output))
	evidence["output_sha256"] = sha256Text(responseFields.Output)
	return verifierOutcome{Status: "passed", Evidence: evidence}
}

func readCanonicalActorTokenPath(configPath string) (string, error) {
	configPath = expandHomePlaceholder(strings.TrimSpace(configPath))
	info, err := os.Lstat(configPath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("active CORE config must be a regular non-symlink file")
	}
	raw, err := readRegularFile(configPath, maxVerifierFileBytes)
	if err != nil {
		return "", err
	}
	var config struct {
		LocalAgentOps struct {
			Enabled       bool   `yaml:"enabled"`
			AuthTokenFile string `yaml:"auth_token_file"`
		} `yaml:"local_agent_ops"`
	}
	if err := yaml.Unmarshal(raw, &config); err != nil || !config.LocalAgentOps.Enabled {
		return "", errors.New("local_agent_ops is not enabled in active CORE config")
	}
	tokenPath := strings.TrimSpace(config.LocalAgentOps.AuthTokenFile)
	if tokenPath == "" || !filepath.IsAbs(tokenPath) {
		return "", errors.New("local_agent_ops token path must be absolute")
	}
	return tokenPath, nil
}

func readVerifierActorToken(path string, deps verifierDependencies) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("actor token file is required")
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("actor token file must be a regular non-symlink file")
	}
	if deps.Platform() != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("actor token file must be owner-only")
	}
	raw, err := readRegularFile(path, 4096)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(raw))
	if len([]byte(token)) < verifierMinActorTokenBytes || strings.IndexFunc(token, unicode.IsSpace) >= 0 {
		return "", errors.New("actor token must contain one owner-only non-whitespace token")
	}
	return token, nil
}

func jsonActorRequest(message string) ([]byte, error) {
	// Encoding through json.Marshal prevents a caller-controlled message from
	// becoming a second field or a non-JSON request body.
	return jsonMarshal(map[string]string{"message": message})
}

func jsonMarshal(value any) ([]byte, error) {
	var buffer bytes.Buffer
	if err := writeJSONValue(&buffer, value); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func writeJSONValue(buffer *bytes.Buffer, value any) error {
	// Keep the helper local to the verifier package and avoid sharing runtime
	// request encoders that may grow fields outside the canonical contract.
	encoder := json.NewEncoder(buffer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

type canonicalActorResponse struct {
	RequestID string
	JobID     string
	AgentID   string
	Role      string
	Route     string
	Output    string
}

func validateCanonicalActorResponse(fields map[string]any, requestID string) (canonicalActorResponse, error) {
	read := func(name string) (string, error) {
		raw, ok := fields[name]
		value, stringOK := raw.(string)
		value = strings.TrimSpace(value)
		if !ok || !stringOK || value == "" || len([]byte(value)) > maxVerifierBodyBytes {
			return "", fmt.Errorf("canonical Agent response field %s is missing", name)
		}
		return value, nil
	}
	result := canonicalActorResponse{}
	var err error
	if result.RequestID, err = read("request_id"); err != nil {
		return result, err
	}
	if result.JobID, err = read("job_id"); err != nil {
		return result, err
	}
	if result.AgentID, err = read("agent_id"); err != nil {
		return result, err
	}
	if result.Role, err = read("role"); err != nil {
		return result, err
	}
	if result.Route, err = read("route"); err != nil {
		return result, err
	}
	if result.Output, err = read("output"); err != nil {
		return result, err
	}
	if result.RequestID != requestID {
		return result, errors.New("canonical Agent response request id does not match the request")
	}
	if result.AgentID != "shiro" || result.Role != "worker" || result.Route != "OPS" {
		return result, errors.New("canonical Agent response actor or route identity is invalid")
	}
	return result, nil
}
