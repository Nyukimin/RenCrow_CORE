package main

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"
)

func TestLogStartupPhaseUsesStableMachineReadableFields(t *testing.T) {
	var output bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	log.SetOutput(&output)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	})

	logStartupPhase("conversation_l1_store_init", time.Now().Add(-10*time.Millisecond))
	line := output.String()
	if !strings.Contains(line, "startup_phase phase=conversation_l1_store_init elapsed_ms=") {
		t.Fatalf("startup phase log = %q, want stable phase and elapsed_ms fields", line)
	}
	if strings.Contains(line, "config.yaml") || strings.Contains(line, "token") || strings.Contains(line, "secret") {
		t.Fatalf("startup phase log contains a path or secret-like value: %q", line)
	}
}
