package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventtaskmigration"
)

func TestRunRejectsMissingRequiredFlagsAndEmitsBlockedReceipt(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run([]string{"--mode", "dry-run"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("missing flags accepted")
	}
	var receipt eventtaskmigration.Manifest
	if decodeErr := json.Unmarshal(stdout.Bytes(), &receipt); decodeErr != nil {
		t.Fatalf("decode stdout receipt: %v; output=%q", decodeErr, stdout.String())
	}
	if receipt.Status != eventtaskmigration.StatusBlocked || receipt.ErrorCode != "invalid_options" {
		t.Fatalf("unexpected receipt: %#v", receipt)
	}
}

func TestRunRejectsPositionalArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"unexpected"}, &stdout, &stderr); err == nil {
		t.Fatal("positional argument accepted")
	}
}
