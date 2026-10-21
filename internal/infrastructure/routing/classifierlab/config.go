package classifierlab

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	ConfigPathEnv     = "RENCROW_ROUTING_CLASSIFIER_TEST_CONFIG"
	maxConfigFileSize = 1 << 20
)

var tokenEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type Config struct {
	SchemaVersion   string                `yaml:"schema_version"`
	Mode            Mode                  `yaml:"mode"`
	ExperimentID    string                `yaml:"experiment_id"`
	Candidate       CandidateConfig       `yaml:"candidate"`
	Shadow          ShadowConfig          `yaml:"shadow"`
	ReferenceWorker ReferenceWorkerConfig `yaml:"reference_worker"`
	Recording       RecordingConfig       `yaml:"recording"`
	Evaluation      EvaluationConfig      `yaml:"evaluation"`
}

type CandidateConfig struct {
	Endpoint              string `yaml:"endpoint"`
	AuthTokenEnv          string `yaml:"auth_token_env"`
	TimeoutMS             int    `yaml:"timeout_ms"`
	Retries               int    `yaml:"retries"`
	ExpectedFamily        string `yaml:"expected_family"`
	ExpectedModelRevision string `yaml:"expected_model_revision"`
	ExpectedPolicySHA256  string `yaml:"expected_policy_sha256"`
}

type ShadowConfig struct {
	SampleRate     float64 `yaml:"sample_rate"`
	SampleSeed     int64   `yaml:"sample_seed"`
	QueueCapacity  int     `yaml:"queue_capacity"`
	Workers        int     `yaml:"workers"`
	MaxJobAgeMS    int     `yaml:"max_job_age_ms"`
	DrainTimeoutMS int     `yaml:"drain_timeout_ms"`
}

type RecordingConfig struct {
	Directory                 string `yaml:"directory"`
	IncludeMessageText        bool   `yaml:"include_message_text"`
	IncludeRawModelOutput     bool   `yaml:"include_raw_model_output"`
	OutputQueueCapacity       int    `yaml:"output_queue_capacity"`
	TemporaryRawRetentionDays int    `yaml:"temporary_raw_retention_days"`
}

type ReferenceWorkerConfig struct {
	PythonExecutable string `yaml:"python_executable"`
	ModelDirectory   string `yaml:"model_directory"`
	Device           string `yaml:"device"`
	DType            string `yaml:"dtype"`
	IntraOpThreads   int    `yaml:"intra_op_threads"`
	InterOpThreads   int    `yaml:"inter_op_threads"`
	MaxLen           int    `yaml:"max_len"`
	HeadMaxLen       int    `yaml:"head_max_len"`
	HardTimeoutMS    int    `yaml:"hard_timeout_ms"`
	OfflineRequired  bool   `yaml:"offline_required"`
}

type EvaluationConfig struct {
	RandomSeed                     int64   `yaml:"random_seed"`
	WarmupRequests                 int     `yaml:"warmup_requests"`
	LatencyRepetitions             int     `yaml:"latency_repetitions"`
	QualityRepetitions             int     `yaml:"quality_repetitions"`
	PerformanceConcurrency         []int   `yaml:"performance_concurrency"`
	BootstrapResamples             int     `yaml:"bootstrap_resamples"`
	NoninferiorityMarginMacroF1    float64 `yaml:"noninferiority_margin_macro_f1"`
	CriticalRegressionAllowedCount int     `yaml:"critical_regression_allowed_count"`
}

func DefaultConfig() Config {
	return Config{
		SchemaVersion: SchemaVersion,
		Mode:          ModeOff,
		Candidate:     CandidateConfig{TimeoutMS: 3000, Retries: 0},
		Shadow: ShadowConfig{
			SampleRate:     0.1,
			SampleSeed:     20261019,
			QueueCapacity:  128,
			Workers:        1,
			MaxJobAgeMS:    10000,
			DrainTimeoutMS: 2000,
		},
		ReferenceWorker: ReferenceWorkerConfig{
			PythonExecutable: "REPLACE_WITH_VENV_PYTHON_PATH",
			ModelDirectory:   "REPLACE_WITH_LOCAL_MODEL_DIRECTORY",
			Device:           "cpu",
			DType:            "float32",
			IntraOpThreads:   4,
			InterOpThreads:   1,
			MaxLen:           8192,
			HeadMaxLen:       1024,
			HardTimeoutMS:    10000,
			OfflineRequired:  true,
		},
		Recording: RecordingConfig{
			IncludeMessageText:        false,
			IncludeRawModelOutput:     false,
			OutputQueueCapacity:       128,
			TemporaryRawRetentionDays: 7,
		},
		Evaluation: EvaluationConfig{
			RandomSeed:                     20261019,
			WarmupRequests:                 20,
			LatencyRepetitions:             5,
			QualityRepetitions:             1,
			PerformanceConcurrency:         []int{1, 2, 4},
			BootstrapResamples:             2000,
			NoninferiorityMarginMacroF1:    0.01,
			CriticalRegressionAllowedCount: 0,
		},
	}
}

func LoadConfig(path string) (Config, error) {
	if strings.TrimSpace(path) == "" {
		return Config{}, errors.New("classifierlab config path is empty")
	}
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open classifierlab config: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxConfigFileSize+1))
	if err != nil {
		return Config{}, fmt.Errorf("read classifierlab config: %w", err)
	}
	if len(data) > maxConfigFileSize {
		return Config{}, fmt.Errorf("classifierlab config exceeds %d bytes", maxConfigFileSize)
	}
	return ReadConfig(data)
}

func ReadConfig(data []byte) (Config, error) {
	if len(data) == 0 {
		return Config{}, errors.New("classifierlab config is empty")
	}
	if len(data) > maxConfigFileSize {
		return Config{}, fmt.Errorf("classifierlab config exceeds %d bytes", maxConfigFileSize)
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	cfg := DefaultConfig()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode classifierlab config: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Config{}, errors.New("classifierlab config must contain one YAML document")
		}
		return Config{}, fmt.Errorf("decode trailing classifierlab config: %w", err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return Config{}, fmt.Errorf("inspect classifierlab config keys: %w", err)
	}
	if !hasMappingKey(&root, "schema_version") || !hasMappingKey(&root, "mode") {
		return Config{}, errors.New("classifierlab config requires schema_version and mode")
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func LoadConfigFromEnv() (Config, error) {
	path, ok := os.LookupEnv(ConfigPathEnv)
	if !ok {
		return DefaultConfig(), nil
	}
	if strings.TrimSpace(path) == "" {
		return Config{}, fmt.Errorf("%s is set but empty", ConfigPathEnv)
	}
	return LoadConfig(path)
}

func (c Config) Validate() error {
	if c.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported classifierlab schema_version %q", c.SchemaVersion)
	}
	switch c.Mode {
	case ModeOff:
	case ModeShadow:
	default:
		return fmt.Errorf("unsupported classifierlab mode %q", c.Mode)
	}
	if err := validateReferenceWorkerValues(c.ReferenceWorker); err != nil {
		return err
	}
	if err := validateEvaluationValues(c.Evaluation); err != nil {
		return err
	}
	if c.Mode == ModeOff {
		return nil
	}

	if strings.TrimSpace(c.ExperimentID) == "" || len(c.ExperimentID) > 128 {
		return errors.New("shadow mode requires an experiment_id of 1 to 128 bytes")
	}
	endpoint, err := url.Parse(c.Candidate.Endpoint)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" ||
		endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
		return errors.New("candidate.endpoint must be an HTTP(S) origin without credentials, path, query, or fragment")
	}
	if !isCanonicalLoopbackHost(endpoint.Hostname()) {
		return errors.New("candidate.endpoint must target the canonical loopback address 127.0.0.1 or ::1")
	}
	if !tokenEnvName.MatchString(c.Candidate.AuthTokenEnv) {
		return errors.New("candidate.auth_token_env must be a valid environment variable name")
	}
	if c.Candidate.TimeoutMS < 1 || c.Candidate.TimeoutMS > 60000 {
		return errors.New("candidate.timeout_ms must be between 1 and 60000")
	}
	if c.Candidate.Retries != 0 {
		return errors.New("candidate.retries must be 0")
	}
	if emptyOrPlaceholder(c.Candidate.ExpectedFamily) || emptyOrPlaceholder(c.Candidate.ExpectedModelRevision) {
		return errors.New("candidate expected family and model revision must be prepared values")
	}
	if !preparedRevisionPattern.MatchString(c.Candidate.ExpectedModelRevision) {
		return errors.New("candidate.expected_model_revision must be a pinned lowercase 40- or 64-character commit hash")
	}
	if !validSHA256(c.Candidate.ExpectedPolicySHA256) {
		return errors.New("candidate.expected_policy_sha256 must be a prepared lowercase SHA-256 value")
	}
	if math.IsNaN(c.Shadow.SampleRate) || math.IsInf(c.Shadow.SampleRate, 0) || c.Shadow.SampleRate < 0 || c.Shadow.SampleRate > 1 {
		return errors.New("shadow.sample_rate must be finite and between 0 and 1")
	}
	if c.Shadow.QueueCapacity < 1 || c.Shadow.QueueCapacity > 4096 {
		return errors.New("shadow.queue_capacity must be between 1 and 4096")
	}
	if c.Shadow.Workers < 1 || c.Shadow.Workers > 16 {
		return errors.New("shadow.workers must be between 1 and 16")
	}
	if c.Shadow.MaxJobAgeMS < 1 || c.Shadow.MaxJobAgeMS > 600000 {
		return errors.New("shadow.max_job_age_ms must be between 1 and 600000")
	}
	if c.Shadow.DrainTimeoutMS < 1 || c.Shadow.DrainTimeoutMS > 30000 {
		return errors.New("shadow.drain_timeout_ms must be between 1 and 30000")
	}
	if strings.TrimSpace(c.Recording.Directory) == "" || strings.HasPrefix(c.Recording.Directory, "REPLACE_") || !filepath.IsAbs(c.Recording.Directory) {
		return errors.New("shadow mode requires an absolute prepared recording.directory")
	}
	if c.Recording.IncludeMessageText {
		return errors.New("recording.include_message_text is not supported in shadow mode")
	}
	if c.Recording.IncludeRawModelOutput {
		return errors.New("recording.include_raw_model_output is not supported")
	}
	if c.Recording.OutputQueueCapacity < 1 || c.Recording.OutputQueueCapacity > 4096 {
		return errors.New("recording.output_queue_capacity must be between 1 and 4096")
	}
	if c.Recording.TemporaryRawRetentionDays < 1 || c.Recording.TemporaryRawRetentionDays > 365 {
		return errors.New("recording.temporary_raw_retention_days must be between 1 and 365")
	}
	if err := validatePreparedReferenceWorker(c.ReferenceWorker); err != nil {
		return err
	}
	return nil
}

func validateReferenceWorkerValues(worker ReferenceWorkerConfig) error {
	if worker.IntraOpThreads < 1 || worker.IntraOpThreads > 256 || worker.InterOpThreads < 1 || worker.InterOpThreads > 256 {
		return errors.New("reference_worker thread counts must be between 1 and 256")
	}
	if worker.MaxLen < 1 || worker.MaxLen > 1_000_000 || worker.HeadMaxLen < 1 || worker.HeadMaxLen > worker.MaxLen {
		return errors.New("reference_worker max_len and head_max_len must be positive and head_max_len must not exceed max_len")
	}
	if worker.HardTimeoutMS < 1 || worker.HardTimeoutMS > 600000 {
		return errors.New("reference_worker.hard_timeout_ms must be between 1 and 600000")
	}
	if !validPreparedDevice(worker.Device) {
		return fmt.Errorf("reference_worker.device %q is unsupported", worker.Device)
	}
	if worker.DType != "float32" && worker.DType != "float16" && worker.DType != "bfloat16" {
		return fmt.Errorf("reference_worker.dtype %q is unsupported", worker.DType)
	}
	return nil
}

func validatePreparedReferenceWorker(worker ReferenceWorkerConfig) error {
	if emptyOrPlaceholder(worker.PythonExecutable) || !filepath.IsAbs(worker.PythonExecutable) {
		return errors.New("shadow mode requires a prepared absolute reference_worker.python_executable")
	}
	if emptyOrPlaceholder(worker.ModelDirectory) || !filepath.IsAbs(worker.ModelDirectory) {
		return errors.New("shadow mode requires a prepared absolute reference_worker.model_directory")
	}
	if !worker.OfflineRequired {
		return errors.New("reference_worker.offline_required must be true")
	}
	return nil
}

func validateEvaluationValues(evaluation EvaluationConfig) error {
	if evaluation.WarmupRequests < 0 || evaluation.WarmupRequests > 1_000_000 {
		return errors.New("evaluation.warmup_requests must be between 0 and 1000000")
	}
	if evaluation.LatencyRepetitions < 1 || evaluation.LatencyRepetitions > 100_000 {
		return errors.New("evaluation.latency_repetitions must be between 1 and 100000")
	}
	if evaluation.QualityRepetitions < 1 || evaluation.QualityRepetitions > 100_000 {
		return errors.New("evaluation.quality_repetitions must be between 1 and 100000")
	}
	if len(evaluation.PerformanceConcurrency) == 0 || len(evaluation.PerformanceConcurrency) > 128 {
		return errors.New("evaluation.performance_concurrency must contain 1 to 128 values")
	}
	seenConcurrency := make(map[int]struct{}, len(evaluation.PerformanceConcurrency))
	for _, concurrency := range evaluation.PerformanceConcurrency {
		if concurrency < 1 || concurrency > 4096 {
			return errors.New("evaluation.performance_concurrency values must be between 1 and 4096")
		}
		if _, exists := seenConcurrency[concurrency]; exists {
			return errors.New("evaluation.performance_concurrency values must be unique")
		}
		seenConcurrency[concurrency] = struct{}{}
	}
	if evaluation.BootstrapResamples < 1 || evaluation.BootstrapResamples > 1_000_000 {
		return errors.New("evaluation.bootstrap_resamples must be between 1 and 1000000")
	}
	if math.IsNaN(evaluation.NoninferiorityMarginMacroF1) || math.IsInf(evaluation.NoninferiorityMarginMacroF1, 0) ||
		evaluation.NoninferiorityMarginMacroF1 < 0 || evaluation.NoninferiorityMarginMacroF1 > 1 {
		return errors.New("evaluation.noninferiority_margin_macro_f1 must be finite and between 0 and 1")
	}
	if evaluation.CriticalRegressionAllowedCount < 0 || evaluation.CriticalRegressionAllowedCount > 1_000_000 {
		return errors.New("evaluation.critical_regression_allowed_count must be between 0 and 1000000")
	}
	return nil
}

func isCanonicalLoopbackHost(host string) bool {
	return host == "127.0.0.1" || host == "::1"
}

func hasMappingKey(root *yaml.Node, key string) bool {
	if root == nil || root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return false
	}
	content := root.Content[0]
	if content.Kind != yaml.MappingNode {
		return false
	}
	for index := 0; index+1 < len(content.Content); index += 2 {
		if content.Content[index].Value == key {
			return true
		}
	}
	return false
}

func emptyOrPlaceholder(value string) bool {
	return strings.TrimSpace(value) == "" || strings.HasPrefix(strings.TrimSpace(value), "REPLACE_")
}

func validSHA256(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
