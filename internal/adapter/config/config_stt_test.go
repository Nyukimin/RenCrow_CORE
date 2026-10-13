package config

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSTTTestConfig(t *testing.T, stt string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stt.yaml")
	body := "server:\n  port: 8080\nstt:\n" + stt
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSTTDefaultsSuitUtteranceRecognition(t *testing.T) {
	cfg, err := LoadConfig(writeSTTTestConfig(t, "  enabled: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	stt := cfg.STT
	if stt.TimeoutMS != 60000 {
		t.Fatalf("timeout_ms default = %d; it must cover a %d ms utterance plus engine queueing", stt.TimeoutMS, 30000)
	}
	if stt.BusyPolicy != "direct" {
		t.Fatalf("busy_policy default = %q; queue_latest would drop finals of consecutive utterances", stt.BusyPolicy)
	}
	if stt.MinSpeechMS != 150 || stt.MaxUtteranceMS != 30000 || stt.IdleTimeoutMS != 2000 {
		t.Fatalf("utterance limits = %+v", stt)
	}
	if stt.NoisePhrases != nil {
		t.Fatalf("noise_phrases extends the built-in list and defaults to none: %v", stt.NoisePhrases)
	}
}

func TestSTTUtteranceSettingsAreReadFromConfig(t *testing.T) {
	cfg, err := LoadConfig(writeSTTTestConfig(t, `  enabled: true
  timeout_ms: 45000
  min_speech_ms: 200
  max_utterance_ms: 20000
  idle_timeout_ms: 1500
  noise_phrases:
    - "また見てね"
    - "おやすみなさい"
`))
	if err != nil {
		t.Fatal(err)
	}
	stt := cfg.STT
	if stt.TimeoutMS != 45000 || stt.MinSpeechMS != 200 || stt.MaxUtteranceMS != 20000 || stt.IdleTimeoutMS != 1500 {
		t.Fatalf("stt = %+v", stt)
	}
	if len(stt.NoisePhrases) != 2 || stt.NoisePhrases[0] != "また見てね" {
		t.Fatalf("noise_phrases = %v", stt.NoisePhrases)
	}
}

func TestSTTUtteranceValidation(t *testing.T) {
	tests := []struct {
		name string
		stt  string
		want string
	}{
		{"max too small", "  enabled: true\n  max_utterance_ms: 500\n", "stt.max_utterance_ms"},
		{"max too large", "  enabled: true\n  max_utterance_ms: 300000\n", "stt.max_utterance_ms"},
		{"idle too small", "  enabled: true\n  idle_timeout_ms: 50\n", "stt.idle_timeout_ms"},
		{"min not below max", "  enabled: true\n  min_speech_ms: 5000\n  max_utterance_ms: 4000\n", "stt.min_speech_ms"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadConfig(writeSTTTestConfig(t, tt.stt))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("LoadConfig error = %v, want it to name %s", err, tt.want)
			}
		})
	}
}

// Deployed configs carry timeout_ms: 8000. Rejecting them would stop CORE at
// the first restart, so a timeout shorter than the longest utterance is reported
// and left to the operator.
func TestSTTTimeoutShorterThanMaxUtteranceIsWarnedNotRejected(t *testing.T) {
	var out bytes.Buffer
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	cfg, err := LoadConfig(writeSTTTestConfig(t, "  enabled: true\n  timeout_ms: 8000\n"))
	if err != nil {
		t.Fatalf("an existing 8000 ms timeout must still load: %v", err)
	}
	if cfg.STT.TimeoutMS != 8000 {
		t.Fatalf("explicit timeout_ms must be kept, got %d", cfg.STT.TimeoutMS)
	}
	if !strings.Contains(out.String(), "stt.timeout_ms") || !strings.Contains(out.String(), "max_utterance_ms") {
		t.Fatalf("startup log must warn about the short timeout:\n%s", out.String())
	}
}

func TestSTTDisabledSkipsUtteranceValidation(t *testing.T) {
	if _, err := LoadConfig(writeSTTTestConfig(t, "  enabled: false\n  max_utterance_ms: 500\n")); err != nil {
		t.Fatalf("a disabled stt section must not block startup: %v", err)
	}
}

func TestUnknownSTTConfigKeysAreWarnedNotRejected(t *testing.T) {
	var out bytes.Buffer
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	cfg, err := LoadConfig(writeSTTTestConfig(t, `  enabled: true
  provider: "legacy"
  model: "large-v3"
  provider_url: "http://example.invalid:9"
  stream_url: "ws://example.invalid:9/stream"
  language: "ja"
  timeout_ms: 60000
`))
	if err != nil {
		t.Fatalf("legacy keys exist in deployed configs and must not stop CORE: %v", err)
	}
	if !cfg.STT.Enabled {
		t.Fatal("stt must stay enabled")
	}
	logged := out.String()
	for _, key := range []string{"stt.provider", "stt.model", "stt.provider_url", "stt.stream_url", "stt.language"} {
		if !strings.Contains(logged, key+" ") && !strings.Contains(logged, key+"\n") {
			t.Fatalf("startup log must name ignored key %s:\n%s", key, logged)
		}
	}
	for _, secret := range []string{"example.invalid", "legacy", "large-v3"} {
		if strings.Contains(logged, secret) {
			t.Fatalf("the warning must name keys only, not values (%q):\n%s", secret, logged)
		}
	}
	if strings.Contains(logged, "stt.timeout_ms ") {
		t.Fatalf("known keys must not be reported:\n%s", logged)
	}
}

func TestConfigExampleHasNoUnknownSTTKeys(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "config.yaml.example"))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	path := filepath.Join(t.TempDir(), "example.yaml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "ignored") && strings.Contains(out.String(), "stt.") {
		t.Fatalf("config.yaml.example carries stt keys CORE ignores:\n%s", out.String())
	}
	if cfg.STT.BusyPolicy != "direct" || cfg.STT.TimeoutMS < 2*cfg.STT.MaxUtteranceMS {
		t.Fatalf("config.yaml.example stt = %+v", cfg.STT)
	}
}

func TestUnrecognisedBusyPolicyIsWarnedAndTreatedAsDirect(t *testing.T) {
	var out bytes.Buffer
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	cfg, err := LoadConfig(writeSTTTestConfig(t, "  enabled: true\n  busy_policy: \"queue-latest\"\n"))
	if err != nil {
		t.Fatalf("an unknown busy_policy must not stop startup: %v", err)
	}
	if cfg.STT.BusyPolicy != "queue-latest" {
		t.Fatalf("the configured text is kept as written, got %q", cfg.STT.BusyPolicy)
	}
	logged := out.String()
	if !strings.Contains(logged, "stt.busy_policy") || !strings.Contains(logged, "direct") {
		t.Fatalf("startup log must say the value is not recognised and direct is used:\n%s", logged)
	}

	out.Reset()
	for _, known := range []string{"direct", "reject", "queue_latest"} {
		if _, err := LoadConfig(writeSTTTestConfig(t, "  enabled: true\n  busy_policy: \""+known+"\"\n")); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Contains(out.String(), "stt.busy_policy") {
		t.Fatalf("recognised policies must not warn:\n%s", out.String())
	}
}
