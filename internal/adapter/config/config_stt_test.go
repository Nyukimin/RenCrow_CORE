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

// /stt is a relay to the RenCrow_STT audio stream, and everything about where an
// utterance ends belongs to RenCrow_STT. What stays in CORE's stt section serves
// the stream's address and the HTTP route (/stt/file, /stt/chat-input).
func TestSTTDefaultsServeTheGatewayAndTheHTTPRoute(t *testing.T) {
	cfg, err := LoadConfig(writeSTTTestConfig(t, "  enabled: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	stt := cfg.STT
	if stt.GatewayBaseURL != "http://127.0.0.1:8766" || stt.EndpointPath != "/stt" {
		t.Fatalf("stt = %+v", stt)
	}
	if stt.TimeoutMS != 60000 {
		t.Fatalf("timeout_ms default = %d; the HTTP route sends a whole file", stt.TimeoutMS)
	}
	if stt.BusyPolicy != "direct" {
		t.Fatalf("busy_policy default = %q; queue_latest would drop requests", stt.BusyPolicy)
	}
}

// The four keys that moved to RenCrow_STT (stream.endpoint in its stt.json) are
// no longer CORE settings. A deployed config still carries them, so they are
// reported like every other key CORE does not read, by name only, and ignored.
func TestSTTKeysThatMovedToRenCrowSTTAreIgnoredAndReportedByName(t *testing.T) {
	var out bytes.Buffer
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	cfg, err := LoadConfig(writeSTTTestConfig(t, `  enabled: true
  min_speech_ms: 200
  max_utterance_ms: 500
  idle_timeout_ms: 50
  noise_phrases:
    - "また見てね"
`))
	if err != nil {
		t.Fatalf("keys that moved away must not stop CORE at its first restart: %v", err)
	}
	if !cfg.STT.Enabled {
		t.Fatal("stt must stay enabled")
	}
	logged := out.String()
	for _, key := range []string{"stt.min_speech_ms", "stt.max_utterance_ms", "stt.idle_timeout_ms", "stt.noise_phrases"} {
		if !strings.Contains(logged, key+" is not a CORE setting") {
			t.Fatalf("startup log must name the ignored key %s:\n%s", key, logged)
		}
	}
	if strings.Contains(logged, "また見てね") {
		t.Fatalf("the warning names keys, never values:\n%s", logged)
	}
}

// Deployed configs carry timeout_ms: 8000 or 60000. The timeout serves the HTTP
// route only, so no comparison with an utterance length is left to warn about.
func TestSTTTimeoutIsKeptAndNeverCompared(t *testing.T) {
	var out bytes.Buffer
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	cfg, err := LoadConfig(writeSTTTestConfig(t, "  enabled: true\n  timeout_ms: 8000\n"))
	if err != nil {
		t.Fatalf("an existing 8000 ms timeout must load: %v", err)
	}
	if cfg.STT.TimeoutMS != 8000 {
		t.Fatalf("explicit timeout_ms must be kept, got %d", cfg.STT.TimeoutMS)
	}
	if strings.Contains(out.String(), "stt.timeout_ms") {
		t.Fatalf("nothing in CORE compares timeout_ms with an utterance any more:\n%s", out.String())
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
	if cfg.STT.BusyPolicy != "direct" || cfg.STT.TimeoutMS <= 0 {
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
