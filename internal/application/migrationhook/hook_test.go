package migrationhook

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunStateDescribeReturnsHonestDurableReceipt(t *testing.T) {
	request := `{"contract_version":"rencrow-migration-owner-hook-request/v1","operation":"state_describe","owner":"RenCrow_CORE","request_id":"request-1"}`
	var stdout bytes.Buffer
	code := Run(nil, strings.NewReader(request), &stdout, func(string) error {
		t.Fatal("state_describe must not load config")
		return nil
	})
	if code != ExitCompleted {
		t.Fatalf("exit=%d stdout=%q", code, stdout.String())
	}
	var receipt Receipt
	if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil {
		t.Fatalf("receipt JSON: %v", err)
	}
	if receipt.Status != "completed" || receipt.StateClass != "durable" || receipt.SchemaRevision != "rencrow-core-migration-state/v1" || receipt.ConsistencyMode != "module_backup_api" {
		t.Fatalf("receipt=%+v", receipt)
	}
	if receipt.ContractVersion != ResponseContractVersion || receipt.ContractVersion == RequestContractVersion {
		t.Fatalf("response contract=%q request contract=%q", receipt.ContractVersion, RequestContractVersion)
	}
	if receipt.Artifact != nil || receipt.Failure != nil || receipt.Counts == nil || receipt.Operations == nil || len(receipt.Operations) != 0 {
		t.Fatalf("state describe must expose empty operations and no artifact: %+v", receipt)
	}
	if len(stdout.Bytes()) > MaxJSONBytes {
		t.Fatalf("receipt exceeds %d bytes", MaxJSONBytes)
	}
}

func TestRunConfigValidateUsesCandidateAndDoesNotLeak(t *testing.T) {
	dir := t.TempDir()
	candidate := filepath.Join(dir, "candidate.yaml")
	if err := os.WriteFile(candidate, []byte("server:\n  port: 8080\nsecret: do-not-print\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relativeCandidate, err := filepath.Rel(workingDir, candidate)
	if err != nil {
		t.Fatal(err)
	}
	request := requestJSON(t, map[string]any{
		"contract_version": RequestContractVersion,
		"operation":        "config_validate",
		"owner":            Owner,
		"request_id":       "request-2",
		"candidate_config": relativeCandidate,
	})
	var stdout bytes.Buffer
	var gotPath string
	code := Run(nil, strings.NewReader(request), &stdout, func(path string) error {
		gotPath = path
		return nil
	})
	if code != ExitCompleted || gotPath != relativeCandidate {
		t.Fatalf("exit=%d path=%q stdout=%q", code, gotPath, stdout.String())
	}
	if strings.Contains(stdout.String(), candidate) || strings.Contains(stdout.String(), relativeCandidate) || strings.Contains(stdout.String(), "do-not-print") {
		t.Fatalf("receipt leaked candidate data: %s", stdout.String())
	}
}

func TestRunConfigRejectionIsSafe(t *testing.T) {
	dir := t.TempDir()
	candidate := filepath.Join(dir, "private-config.yaml")
	if err := os.WriteFile(candidate, []byte("token: super-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := requestJSON(t, map[string]any{
		"contract_version": RequestContractVersion,
		"operation":        "config_validate",
		"owner":            Owner,
		"request_id":       "request-3",
		"candidate_config": candidate,
	})
	var stdout bytes.Buffer
	code := Run(nil, strings.NewReader(request), &stdout, func(string) error {
		return errors.New("config validation failed at " + candidate + ": super-secret")
	})
	if code != ExitRejected {
		t.Fatalf("exit=%d stdout=%q", code, stdout.String())
	}
	if strings.Contains(stdout.String(), candidate) || strings.Contains(stdout.String(), "super-secret") || strings.Contains(stdout.String(), "validation failed at") {
		t.Fatalf("rejection leaked internal data: %s", stdout.String())
	}
	var receipt Receipt
	if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "rejected" || receipt.Failure == nil || receipt.Failure.Code != "config_invalid" {
		t.Fatalf("receipt=%+v", receipt)
	}
}

func TestRunRejectsMalformedOversizedUnknownAndArgumentsWithoutStdout(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		input string
	}{
		{name: "argument", args: []string{"legacy"}, input: `{}`},
		{name: "malformed", input: `{`},
		{name: "multiple JSON", input: `{}` + `{}`},
		{name: "unknown field", input: `{"contract_version":"rencrow-migration-owner-hook-request/v1","operation":"state_describe","owner":"RenCrow_CORE","request_id":"r","extra":true}`},
		{name: "old candidate_path", input: `{"contract_version":"rencrow-migration-owner-hook-request/v1","operation":"config_validate","owner":"RenCrow_CORE","request_id":"r","candidate_path":"config.yaml"}`},
		{name: "oversized", input: strings.Repeat("x", MaxJSONBytes+1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout bytes.Buffer
			if code := Run(tc.args, strings.NewReader(tc.input), &stdout, func(string) error { return nil }); code != ExitInvalidRequest {
				t.Fatalf("exit=%d", code)
			}
			if stdout.Len() != 0 {
				t.Fatalf("invalid request wrote stdout=%q", stdout.String())
			}
		})
	}
}

func TestRunRejectsSymlinkAndNonRegularCandidateWithoutCallingValidator(t *testing.T) {
	dir := t.TempDir()
	regular := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(regular, []byte("server:\n  port: 8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(dir, "config-link.yaml")
	if err := os.Symlink(regular, symlink); err != nil {
		if errors.Is(err, os.ErrPermission) {
			t.Skip("symlink creation is unavailable")
		}
		t.Fatal(err)
	}
	for _, candidate := range []string{symlink, dir} {
		request := requestJSON(t, map[string]any{
			"contract_version": RequestContractVersion,
			"operation":        "config_validate",
			"owner":            Owner,
			"request_id":       "request-4",
			"candidate_config": candidate,
		})
		var stdout bytes.Buffer
		called := false
		code := Run(nil, strings.NewReader(request), &stdout, func(string) error { called = true; return nil })
		if code != ExitRejected || called {
			t.Fatalf("candidate=%q exit=%d called=%v", candidate, code, called)
		}
		if strings.Contains(stdout.String(), candidate) {
			t.Fatalf("receipt leaked candidate path: %s", stdout.String())
		}
	}
}

func TestRunReturnsWriterFailureWithoutPartialJSON(t *testing.T) {
	request := `{"contract_version":"rencrow-migration-owner-hook-request/v1","operation":"state_describe","owner":"RenCrow_CORE","request_id":"request-5"}`
	if code := Run(nil, strings.NewReader(request), failingWriter{}, func(string) error { return nil }); code != ExitWriterFailure {
		t.Fatalf("exit=%d", code)
	}
}

func requestJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }
