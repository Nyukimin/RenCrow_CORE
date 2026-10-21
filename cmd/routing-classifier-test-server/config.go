package main

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/routing/classifierlab"
	"gopkg.in/yaml.v3"
)

const maxConfigBytes = 1 << 20

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// The shared classifierlab package owns the resolved-config schema. These
// aliases preserve the server's local names without defining a second schema.
type resolvedConfig = classifierlab.Config
type candidateConfig = classifierlab.CandidateConfig
type shadowConfig = classifierlab.ShadowConfig
type referenceWorkerConfig = classifierlab.ReferenceWorkerConfig
type recordingConfig = classifierlab.RecordingConfig
type evaluationConfig = classifierlab.EvaluationConfig

type loadedConfig struct {
	Path      string
	Directory string
	Value     classifierlab.Config
	Token     string
}

func loadResolvedConfig(name string) (loadedConfig, error) {
	if strings.TrimSpace(name) == "" {
		return loadedConfig{}, errors.New("--config is required")
	}
	path, err := filepath.Abs(name)
	if err != nil {
		return loadedConfig{}, fmt.Errorf("resolve config path: %w", err)
	}
	file, err := os.Open(path)
	if err != nil {
		return loadedConfig{}, fmt.Errorf("open config: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	if err != nil {
		return loadedConfig{}, fmt.Errorf("read config: %w", err)
	}
	if len(data) > maxConfigBytes {
		return loadedConfig{}, fmt.Errorf("config exceeds %d bytes", maxConfigBytes)
	}
	if err := rejectDuplicateYAMLKeys(data); err != nil {
		return loadedConfig{}, fmt.Errorf("decode resolved config: %w", err)
	}
	value, err := classifierlab.ReadConfig(data)
	if err != nil {
		return loadedConfig{}, fmt.Errorf("decode resolved config: %w", err)
	}
	if err := validateResolvedConfig(value); err != nil {
		return loadedConfig{}, err
	}
	var token string
	if envNamePattern.MatchString(value.Candidate.AuthTokenEnv) {
		token = os.Getenv(value.Candidate.AuthTokenEnv)
	}
	return loadedConfig{Path: path, Directory: filepath.Dir(path), Value: value, Token: token}, nil
}

// validateResolvedConfig contains only test-server constraints. Schema and
// common range validation live in classifierlab.Config.Validate.
func validateResolvedConfig(config classifierlab.Config) error {
	if err := config.Validate(); err != nil {
		return err
	}
	if config.Mode != classifierlab.ModeOff && config.Mode != classifierlab.ModeShadow {
		return fmt.Errorf("unsupported mode %q", config.Mode)
	}
	if config.Candidate.TimeoutMS != httpWaitTimeoutMS {
		return fmt.Errorf("candidate.timeout_ms must be %d", httpWaitTimeoutMS)
	}
	endpoint, err := url.Parse(config.Candidate.Endpoint)
	if err != nil || endpoint.Scheme != "http" || endpoint.Hostname() != "127.0.0.1" || endpoint.Port() == "" || endpoint.User != nil || endpoint.Path != "" || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return errors.New("candidate.endpoint must use local http://127.0.0.1")
	}
	port, err := strconv.Atoi(endpoint.Port())
	if err != nil || port < 1 || port > 65535 {
		return errors.New("candidate.endpoint must contain a valid TCP port")
	}
	if !envNamePattern.MatchString(config.Candidate.AuthTokenEnv) {
		return errors.New("candidate.auth_token_env must be a valid environment variable name")
	}
	if config.Candidate.ExpectedFamily != "laya-multilingual" {
		return errors.New("candidate.expected_family must be laya-multilingual")
	}
	if !validRevision(config.Candidate.ExpectedModelRevision) {
		return errors.New("candidate.expected_model_revision must be a pinned lowercase commit hash")
	}
	if !validSHA256(config.Candidate.ExpectedPolicySHA256) {
		return errors.New("candidate.expected_policy_sha256 must be a prepared lowercase SHA-256 value")
	}
	worker := config.ReferenceWorker
	if worker.HardTimeoutMS != childHardTimeoutMS {
		return fmt.Errorf("reference_worker.hard_timeout_ms must be %d", childHardTimeoutMS)
	}
	if !worker.OfflineRequired {
		return errors.New("reference_worker.offline_required must be true")
	}
	if emptyOrPlaceholder(worker.PythonExecutable) || !filepath.IsAbs(worker.PythonExecutable) {
		return errors.New("reference_worker.python_executable must be a prepared absolute path")
	}
	if emptyOrPlaceholder(worker.ModelDirectory) || !filepath.IsAbs(worker.ModelDirectory) {
		return errors.New("reference_worker.model_directory must be a prepared absolute path")
	}
	if !validConfiguredDevice(worker.Device) {
		return fmt.Errorf("unsupported reference_worker.device %q", worker.Device)
	}
	if worker.DType != "float32" && worker.DType != "float16" && worker.DType != "bfloat16" {
		return fmt.Errorf("unsupported reference_worker.dtype %q", worker.DType)
	}
	if worker.IntraOpThreads < 1 || worker.IntraOpThreads > 64 || worker.InterOpThreads < 1 || worker.InterOpThreads > 64 {
		return errors.New("reference_worker thread limits must be between 1 and 64")
	}
	if worker.MaxLen < 1 || worker.MaxLen > 8192 || worker.HeadMaxLen < 1 || worker.HeadMaxLen > worker.MaxLen {
		return errors.New("reference_worker token budgets are out of range")
	}
	if config.Recording.IncludeRawModelOutput {
		return errors.New("recording.include_raw_model_output must be false")
	}
	return nil
}

func rejectDuplicateYAMLKeys(data []byte) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return err
	}
	if len(document.Content) != 1 {
		return errors.New("config document is empty")
	}
	var visit func(*yaml.Node) error
	visit = func(node *yaml.Node) error {
		if node.Kind == yaml.AliasNode {
			return errors.New("YAML aliases are not supported")
		}
		if node.Kind == yaml.MappingNode {
			seen := make(map[string]struct{}, len(node.Content)/2)
			for index := 0; index+1 < len(node.Content); index += 2 {
				key := node.Content[index]
				if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
					return errors.New("config mapping keys must be strings")
				}
				if _, exists := seen[key.Value]; exists {
					return fmt.Errorf("duplicate config field %q", key.Value)
				}
				seen[key.Value] = struct{}{}
			}
		}
		for _, child := range node.Content {
			if err := visit(child); err != nil {
				return err
			}
		}
		return nil
	}
	return visit(document.Content[0])
}

func validConfiguredDevice(value string) bool {
	if value == "cpu" || value == "mps" {
		return true
	}
	if !strings.HasPrefix(value, "cuda:") {
		return false
	}
	index := strings.TrimPrefix(value, "cuda:")
	if index == "" || strings.TrimLeft(index, "0123456789") != "" {
		return false
	}
	return index == "0" || index[0] != '0'
}

func validRevision(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	if value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validSHA256(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func emptyOrPlaceholder(value string) bool {
	return strings.TrimSpace(value) == "" || strings.HasPrefix(strings.TrimSpace(value), "REPLACE_")
}
