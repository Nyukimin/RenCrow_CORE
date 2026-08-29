package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/migrationhook"
)

func TestRunMigrationHookValidatesWithCanonicalConfigLoader(t *testing.T) {
	candidate := filepath.Join(t.TempDir(), "core.yaml")
	if err := os.WriteFile(candidate, []byte("server:\n  host: 127.0.0.1\n  port: 18790\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := map[string]any{
		"contract_version": migrationhook.RequestContractVersion,
		"operation":        "config_validate",
		"owner":            migrationhook.Owner,
		"request_id":       "canonical-loader",
		"candidate_config": candidate,
	}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if code := runMigrationHook(nil, bytes.NewReader(data), &stdout); code != migrationhook.ExitCompleted {
		t.Fatalf("exit=%d stdout=%q", code, stdout.String())
	}
	if strings.Contains(stdout.String(), candidate) {
		t.Fatalf("receipt leaked path: %s", stdout.String())
	}
}

func TestRunMigrationHookKeepsLegacySubcommandsOutOfHookArguments(t *testing.T) {
	var stdout bytes.Buffer
	code := runMigrationHook([]string{"config", "backup-values"}, strings.NewReader(`{}`), &stdout)
	if code != migrationhook.ExitInvalidRequest || stdout.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout.String())
	}
}
