package classifierlab

import (
	"strings"
	"testing"
)

func TestReadConfigDefaultsToOffWithoutCandidateConfiguration(t *testing.T) {
	got, err := ReadConfig([]byte("schema_version: routing-classifier-test/v1\nmode: off\n"))
	if err != nil {
		t.Fatalf("ReadConfig() error = %v", err)
	}
	if got.Mode != ModeOff {
		t.Fatalf("mode = %q, want %q", got.Mode, ModeOff)
	}
	if got.Shadow.QueueCapacity != 128 || got.Shadow.Workers != 1 || got.Candidate.TimeoutMS != 3000 {
		t.Fatalf("defaults not applied: shadow=%+v candidate=%+v", got.Shadow, got.Candidate)
	}
}

func TestReadConfigAcceptsTypedSectionNineSampleInOffMode(t *testing.T) {
	got, err := ReadConfig([]byte(sectionNineOffYAML()))
	if err != nil {
		t.Fatalf("ReadConfig(section 9 sample) error = %v", err)
	}
	if got.ReferenceWorker.IntraOpThreads != 4 || got.ReferenceWorker.MaxLen != 8192 ||
		got.Evaluation.PerformanceConcurrency[2] != 4 || got.Evaluation.BootstrapResamples != 2000 {
		t.Fatalf("typed config fields were not decoded: reference_worker=%+v evaluation=%+v", got.ReferenceWorker, got.Evaluation)
	}
}

func TestReadConfigTypeChecksReferenceWorkerAndEvaluation(t *testing.T) {
	for _, test := range []struct {
		name       string
		old, value string
	}{
		{name: "thread count", old: "intra_op_threads: 4", value: "intra_op_threads: four"},
		{name: "concurrency list", old: "performance_concurrency: [1, 2, 4]", value: "performance_concurrency: [1, nope, 4]"},
		{name: "offline flag", old: "offline_required: true", value: "offline_required: yes-maybe"},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := strings.Replace(sectionNineOffYAML(), test.old, test.value, 1)
			if _, err := ReadConfig([]byte(config)); err == nil {
				t.Fatalf("ReadConfig() accepted invalid %s type", test.name)
			}
		})
	}
}

func TestReadConfigRejectsModesThatCouldReplacePrimary(t *testing.T) {
	for _, mode := range []string{"candidate", "hybrid", "hybrid-replay", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			_, err := ReadConfig([]byte("schema_version: routing-classifier-test/v1\nmode: " + mode + "\n"))
			if err == nil {
				t.Fatalf("ReadConfig() accepted mode %q", mode)
			}
		})
	}
}

func TestReadConfigRequiresPinnedCandidateAndPolicyForShadow(t *testing.T) {
	_, err := ReadConfig([]byte("schema_version: routing-classifier-test/v1\nmode: shadow\n"))
	if err == nil {
		t.Fatal("ReadConfig() accepted shadow mode without endpoint, model revision, or policy hash")
	}

	config := validShadowYAML()
	config = strings.Replace(config, strings.Repeat("a", 64), "REPLACE_WITH_PREPARED_POLICY_SHA256", 1)
	if _, err := ReadConfig([]byte(config)); err == nil {
		t.Fatal("ReadConfig() accepted an unprepared policy hash placeholder")
	}
}

func TestReadConfigRequiresPinnedModelRevision(t *testing.T) {
	for _, revision := range []string{strings.Repeat("b", 40), strings.Repeat("c", 64)} {
		config := strings.Replace(validShadowYAML(), "expected_model_revision: "+strings.Repeat("b", 40), "expected_model_revision: "+revision, 1)
		if _, err := ReadConfig([]byte(config)); err != nil {
			t.Fatalf("ReadConfig() rejected pinned revision length %d: %v", len(revision), err)
		}
	}
	for name, revision := range map[string]string{
		"text": "revision-1", "short": strings.Repeat("b", 39), "unsupported_length": strings.Repeat("b", 41), "uppercase": strings.Repeat("B", 40),
	} {
		t.Run(name, func(t *testing.T) {
			config := strings.Replace(validShadowYAML(), "expected_model_revision: "+strings.Repeat("b", 40), "expected_model_revision: "+revision, 1)
			if _, err := ReadConfig([]byte(config)); err == nil {
				t.Fatalf("ReadConfig() accepted unpinned model revision %q", revision)
			}
		})
	}
}

func TestReadConfigRejectsUnknownKeysAndNonzeroRetries(t *testing.T) {
	config := validShadowYAML() + "\nunknown_setting: true\n"
	if _, err := ReadConfig([]byte(config)); err == nil {
		t.Fatal("ReadConfig() accepted an unknown setting")
	}

	config = strings.Replace(validShadowYAML(), "retries: 0", "retries: 1", 1)
	if _, err := ReadConfig([]byte(config)); err == nil {
		t.Fatal("ReadConfig() accepted automatic candidate retries")
	}
}

func TestReadConfigRejectsMessageTextRecordingAndRemoteEndpoints(t *testing.T) {
	config := strings.Replace(validShadowYAML(), "include_message_text: false", "include_message_text: true", 1)
	if _, err := ReadConfig([]byte(config)); err == nil {
		t.Fatal("ReadConfig() accepted message text recording in shadow mode")
	}

	for _, endpoint := range []string{
		"http://classifier.example:18190",
		"https://classifier.example:18190",
		"http://localhost:18190",
		"http://127.0.0.2:18190",
		"http://2130706433:18190",
	} {
		t.Run(endpoint, func(t *testing.T) {
			config := strings.Replace(validShadowYAML(), "http://127.0.0.1:18190", endpoint, 1)
			if _, err := ReadConfig([]byte(config)); err == nil {
				t.Fatalf("ReadConfig() accepted noncanonical candidate endpoint %q", endpoint)
			}
		})
	}
}

func TestReadConfigAllowsOnlyCanonicalLoopbackEndpoints(t *testing.T) {
	for _, endpoint := range []string{
		"http://127.0.0.1:18190",
		"https://127.0.0.1:18190",
		"http://[::1]:18190",
		"https://[::1]:18190",
	} {
		t.Run(endpoint, func(t *testing.T) {
			config := strings.Replace(validShadowYAML(), "http://127.0.0.1:18190", endpoint, 1)
			if _, err := ReadConfig([]byte(config)); err != nil {
				t.Fatalf("ReadConfig() error = %v", err)
			}
		})
	}
}

func sectionNineOffYAML() string {
	return `schema_version: routing-classifier-test/v1
mode: off
experiment_id: rencrow-route-laya-v01
candidate:
  endpoint: http://127.0.0.1:18190
  auth_token_env: RENCROW_CLASSIFIER_TEST_TOKEN
  timeout_ms: 3000
  retries: 0
  expected_family: laya-multilingual
  expected_model_revision: REPLACE_WITH_PINNED_REVISION
  expected_policy_sha256: REPLACE_WITH_PREPARED_POLICY_SHA256
shadow:
  sample_rate: 0.1
  sample_seed: 20261019
  queue_capacity: 128
  workers: 1
  max_job_age_ms: 10000
  drain_timeout_ms: 2000
reference_worker:
  python_executable: REPLACE_WITH_VENV_PYTHON_PATH
  model_directory: REPLACE_WITH_LOCAL_MODEL_DIRECTORY
  device: cpu
  dtype: float32
  intra_op_threads: 4
  inter_op_threads: 1
  max_len: 8192
  head_max_len: 1024
  hard_timeout_ms: 10000
  offline_required: true
recording:
  directory: REPLACE_WITH_PRIVATE_OUTPUT_DIRECTORY
  include_message_text: false
  include_raw_model_output: false
  output_queue_capacity: 128
  temporary_raw_retention_days: 7
evaluation:
  random_seed: 20261019
  warmup_requests: 20
  latency_repetitions: 5
  quality_repetitions: 1
  performance_concurrency: [1, 2, 4]
  bootstrap_resamples: 2000
  noninferiority_margin_macro_f1: 0.01
  critical_regression_allowed_count: 0
`
}

func validShadowYAML() string {
	return "" +
		"schema_version: routing-classifier-test/v1\n" +
		"mode: shadow\n" +
		"experiment_id: classifier-test\n" +
		"candidate:\n" +
		"  endpoint: http://127.0.0.1:18190\n" +
		"  auth_token_env: RENCROW_CLASSIFIER_TEST_TOKEN\n" +
		"  timeout_ms: 3000\n" +
		"  retries: 0\n" +
		"  expected_family: laya-multilingual\n" +
		"  expected_model_revision: " + strings.Repeat("b", 40) + "\n" +
		"  expected_policy_sha256: " + strings.Repeat("a", 64) + "\n" +
		"reference_worker:\n" +
		"  python_executable: /tmp/classifierlab/python\n" +
		"  model_directory: /tmp/classifierlab/model\n" +
		"  device: cpu\n" +
		"  dtype: float32\n" +
		"  intra_op_threads: 4\n" +
		"  inter_op_threads: 1\n" +
		"  max_len: 8192\n" +
		"  head_max_len: 1024\n" +
		"  hard_timeout_ms: 10000\n" +
		"  offline_required: true\n" +
		"recording:\n" +
		"  directory: /tmp/classifierlab-private\n" +
		"  include_message_text: false\n"
}
