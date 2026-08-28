package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

const verifierTestObservedAt = "2026-08-27T00:00:00Z"

func testManifest(t *testing.T, commands ...string) string {
	t.Helper()
	if len(commands) == 0 {
		commands = []string{"core-health"}
	}
	checks := make([]string, 0, len(commands))
	for _, command := range commands {
		checkID, ok := checkIDForCommand(command)
		if !ok {
			t.Fatalf("unknown fixture command %q", command)
		}
		checks = append(checks, fmt.Sprintf(`{
      "check_id": %q,
      "guarantee_id": %q,
      "owner": "RenCrow_CORE",
      "purpose": "fixture",
      "target": %q,
      "phase": "runtime",
      "consumer": "fixture",
      "failure_action": "blocked",
      "cost": "low",
      "safety_gate": false,
      "coverage": ["readiness"],
      "executor": {"kind": "owner_cli", "command_id": %q},
      "receipt_schema": "rencrow.check-receipt.v1"
    }`, checkID, checkID+"-guarantee", checkID, command))
	}
	contents := fmt.Sprintf(`{"schema_version":2,"purpose":"operational_status","phase":"runtime","checks":[%s]}`, strings.Join(checks, ","))
	path := filepath.Join(t.TempDir(), "core.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write fixture manifest: %v", err)
	}
	return path
}

func verifierArgs(manifest, checkID, evidenceDir string) []string {
	return []string{
		"run",
		"--manifest", manifest,
		"--check-id", checkID,
		"--observed-at", verifierTestObservedAt,
		"--evidence-dir", evidenceDir,
	}
}

func decodeVerifierReceipt(t *testing.T, output []byte) verifierReceipt {
	t.Helper()
	var receipt verifierReceipt
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		t.Fatalf("decode receipt: %v output=%s", err, output)
	}
	return receipt
}

func TestVerifierAllowlistCoversCurrentManifestCommands(t *testing.T) {
	manifestPath := filepath.Join("..", "..", "config", "checks", "core.json")
	manifest, err := loadOwnerManifest(manifestPath)
	if err != nil {
		t.Fatalf("load current CORE manifest: %v", err)
	}
	seen := make(map[string]struct{}, len(manifest.Checks))
	for _, check := range manifest.Checks {
		if _, ok := verifierForCommand(check.Executor.CommandID); !ok {
			t.Fatalf("manifest command_id %q is not verifier-allowlisted", check.Executor.CommandID)
		}
		seen[check.Executor.CommandID] = struct{}{}
	}
	if len(seen) != 8 {
		t.Fatalf("current CORE manifest commands=%d, want 8: %#v", len(seen), seen)
	}
}

func TestVerifierHealthUsesCanonicalLoopbackAndWritesReceiptEvidence(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		if r.Method != http.MethodGet {
			t.Fatalf("method=%s, want GET", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"status":"ok","service":"rencrow-core","runtime":"go"}`))
	}))
	defer srv.Close()
	evidenceDir := t.TempDir()
	manifest := testManifest(t, "core-health")
	args := verifierArgs(manifest, "core_health", evidenceDir)
	args = append(args, "--core-url", srv.URL)
	var output bytes.Buffer
	code := runVerifierCLI(context.Background(), args, &output, &bytes.Buffer{}, verifierDependencies{
		HTTPClient: srv.Client(),
		Platform:   func() string { return runtime.GOOS },
	})
	if code != verifierExitPassed {
		t.Fatalf("exit=%d output=%s", code, output.String())
	}
	if gotPath != "/health" {
		t.Fatalf("path=%q, want /health", gotPath)
	}
	receipt := decodeVerifierReceipt(t, output.Bytes())
	if receipt.Status != "passed" || receipt.CheckID != "core_health" || receipt.ObservedAt != verifierTestObservedAt {
		t.Fatalf("receipt=%+v", receipt)
	}
	if receipt.ReceiptSchema != verifierReceiptSchema || receipt.SchemaVersion != 1 || len(receipt.EvidenceRefs) != 1 {
		t.Fatalf("receipt schema/evidence=%+v", receipt)
	}
	if !strings.HasPrefix(receipt.EvidenceRefs[0], "relative:") {
		t.Fatalf("evidence ref=%q", receipt.EvidenceRefs[0])
	}
	refPath := filepath.Join(evidenceDir, strings.TrimPrefix(receipt.EvidenceRefs[0], "relative:"))
	if _, err := os.Stat(refPath); err != nil {
		t.Fatalf("evidence file: %v", err)
	}
}

func TestVerifierCanonicalRouteUnavailableIsBlocked(t *testing.T) {
	// Use a closed httptest listener so the unit test never depends on a live
	// developer service occupying CORE's canonical port.
	server := httptest.NewServer(http.NotFoundHandler())
	deadURL := server.URL
	server.Close()
	manifest := testManifest(t, "core-health")
	var output bytes.Buffer
	args := verifierArgs(manifest, "core_health", t.TempDir())
	args = append(args, "--core-url", deadURL)
	code := runVerifierCLI(context.Background(), args, &output, &bytes.Buffer{}, verifierDependencies{
		HTTPClient: &http.Client{Timeout: 100 * time.Millisecond},
	})
	if code != verifierExitBlocked {
		t.Fatalf("exit=%d output=%s", code, output.String())
	}
	receipt := decodeVerifierReceipt(t, output.Bytes())
	if receipt.Status != "blocked" || receipt.FailureBoundary == "" {
		t.Fatalf("receipt=%+v", receipt)
	}
}

func TestVerifierMissingActorCredentialsEmitsBlockedReceipt(t *testing.T) {
	manifest := testManifest(t, "core-canonical-actor-e2e")
	var output bytes.Buffer
	args := verifierArgs(manifest, "core_canonical_actor_e2e", t.TempDir())
	args = append(args, "--config", filepath.Join(t.TempDir(), "missing-core.yaml"))
	code := runVerifierCLI(context.Background(), args, &output, &bytes.Buffer{}, verifierDependencies{})
	if code != verifierExitBlocked {
		t.Fatalf("exit=%d output=%s", code, output.String())
	}
	receipt := decodeVerifierReceipt(t, output.Bytes())
	if receipt.Status != "blocked" || !strings.Contains(receipt.FailureBoundary, "credential") {
		t.Fatalf("receipt=%+v", receipt)
	}
}

func TestVerifierActorUsesAuthenticatedCanonicalRouteAndDoesNotLeakToken(t *testing.T) {
	const token = "owner-token-012345678901234567890123"
	tokenPath := filepath.Join(t.TempDir(), "agent-ops.token")
	if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	var gotAuth string
	var gotRequestID string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/agent/ops" {
			t.Fatalf("request=%s %s", r.Method, r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		gotRequestID = r.Header.Get("X-Request-ID")
		if r.Header.Get("X-RenCrow-Client") != "RenCrow_CMD" || r.Header.Get("X-RenCrow-Interaction-Profile") != "agent-ops" {
			t.Fatalf("missing interaction scope headers")
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"request_id":"req-e2e","job_id":"job-e2e","agent_id":"shiro","role":"worker","route":"OPS","output":"diagnostic result"}`))
	}))
	defer srv.Close()
	manifest := testManifest(t, "core-canonical-actor-e2e")
	args := verifierArgs(manifest, "core_canonical_actor_e2e", t.TempDir())
	args = append(args,
		"--core-url", srv.URL,
		"--actor-token-file", tokenPath,
		"--request-id", "req-e2e",
		"--actor-message", "read-only verifier probe",
	)
	var output bytes.Buffer
	code := runVerifierCLI(context.Background(), args, &output, &bytes.Buffer{}, verifierDependencies{HTTPClient: srv.Client()})
	if code != verifierExitPassed {
		t.Fatalf("exit=%d output=%s", code, output.String())
	}
	if gotAuth != "Bearer "+token || gotRequestID != "req-e2e" || gotBody["message"] != "read-only verifier probe" {
		t.Fatalf("request auth/id/body=%q/%q/%#v", gotAuth, gotRequestID, gotBody)
	}
	if strings.Contains(output.String(), token) {
		t.Fatalf("receipt leaked token: %s", output.String())
	}
}

func TestVerifierActorDiscoversCanonicalInputsFromActiveService(t *testing.T) {
	const token = "owner-token-012345678901234567890123"
	root := t.TempDir()
	tokenPath := filepath.Join(root, "agent-ops.token")
	if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	configPath := filepath.Join(root, "core.yaml")
	config := "local_agent_ops:\n  enabled: true\n  auth_token_file: \"" + tokenPath + "\"\n  user_id: ren\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	var gotRequestID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRequestID = r.Header.Get("X-Request-ID")
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"request_id":%q,"job_id":"job-e2e","agent_id":"shiro","role":"worker","route":"OPS","output":"OK"}`, gotRequestID)
	}))
	defer srv.Close()
	manifest := testManifest(t, "core-canonical-actor-e2e")
	args := verifierArgs(manifest, "core_canonical_actor_e2e", t.TempDir())
	args = append(args, "--core-url", srv.URL)
	show := "ActiveState=active\nSubState=running\nResult=success\nMainPID=4242\nExecStart={ path=/tmp/rencrow; argv[]=/tmp/rencrow run ; }\nEnvironment=RENCROW_CONFIG=" + configPath + "\n"
	deps := verifierDependencies{
		HTTPClient: srv.Client(),
		RunCommand: func(_ context.Context, name string, args []string) verifierCommandResult {
			if name == "systemctl" && slices.Contains(args, "show") {
				return verifierCommandResult{Stdout: show}
			}
			if name == "systemctl" && slices.Contains(args, "cat") {
				return verifierCommandResult{Stdout: "[Service]\nExecStart=/tmp/rencrow run\nEnvironment=RENCROW_CONFIG=" + configPath + "\nRestart=always\nStandardOutput=journal\nStandardError=journal\n"}
			}
			return verifierCommandResult{ExitCode: 1, Err: errors.New("unexpected command")}
		},
		Platform: func() string { return "linux" },
	}
	var output bytes.Buffer
	code := runVerifierCLI(context.Background(), args, &output, &bytes.Buffer{}, deps)
	if code != verifierExitPassed {
		t.Fatalf("exit=%d output=%s", code, output.String())
	}
	if !strings.HasPrefix(gotRequestID, "core-verify-") {
		t.Fatalf("request id=%q", gotRequestID)
	}
}

func TestCanonicalActorRequestUsesInferenceSizedTimeout(t *testing.T) {
	client := &http.Client{Timeout: 8 * time.Second}
	copy := verifierActorHTTPClient(client)
	if copy.Timeout != 60*time.Second || client.Timeout != 8*time.Second {
		t.Fatalf("actor timeout=%s source timeout=%s", copy.Timeout, client.Timeout)
	}
}

func TestVerifierSnapshotDelegatesOnlyToExplicitRestoreChecker(t *testing.T) {
	manifest := testManifest(t, "core-l1-snapshot-integrity")
	snapshotDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(snapshotDir, "manifest.txt"), []byte("format_version=4\ncreated_at_jst=20260827-090000\n"), 0o600); err != nil {
		t.Fatalf("write snapshot manifest: %v", err)
	}
	checkerPath := filepath.Join(t.TempDir(), "rencrow-storage-restore-check")
	if err := os.WriteFile(checkerPath, []byte("fixture"), 0o700); err != nil {
		t.Fatalf("write checker fixture: %v", err)
	}
	var gotName string
	var gotArgs []string
	deps := verifierDependencies{
		LookPath: func(name string) (string, error) { return checkerPath, nil },
		RunCommand: func(_ context.Context, name string, args []string) verifierCommandResult {
			gotName = name
			gotArgs = append([]string(nil), args...)
			return verifierCommandResult{ExitCode: 0, Stdout: "[OK] snapshot checksum-valid"}
		},
	}
	args := verifierArgs(manifest, "core_l1_snapshot_integrity", t.TempDir())
	args = append(args, "--snapshot-dir", snapshotDir, "--restore-check", checkerPath)
	var output bytes.Buffer
	code := runVerifierCLI(context.Background(), args, &output, &bytes.Buffer{}, deps)
	if code != verifierExitPassed {
		t.Fatalf("exit=%d output=%s", code, output.String())
	}
	if gotName != checkerPath || len(gotArgs) != 1 || gotArgs[0] != snapshotDir {
		t.Fatalf("checker invocation=%q %#v", gotName, gotArgs)
	}
}

func TestVerifierSnapshotMissingInputBlocksWithoutRunningChecker(t *testing.T) {
	manifest := testManifest(t, "core-l1-snapshot-integrity")
	called := false
	deps := verifierDependencies{RunCommand: func(context.Context, string, []string) verifierCommandResult {
		called = true
		return verifierCommandResult{ExitCode: 0}
	}}
	args := verifierArgs(manifest, "core_l1_snapshot_integrity", t.TempDir())
	var output bytes.Buffer
	code := runVerifierCLI(context.Background(), args, &output, &bytes.Buffer{}, deps)
	if code != verifierExitBlocked {
		t.Fatalf("exit=%d output=%s", code, output.String())
	}
	if called {
		t.Fatal("restore checker ran without explicit snapshot")
	}
}

func TestVerifierStartupRequiresJournalAndRequestEvidence(t *testing.T) {
	manifest := testManifest(t, "core-startup-phase-trace")
	artifact := filepath.Join(t.TempDir(), "rencrow")
	configPath := filepath.Join(t.TempDir(), "core.yaml")
	if err := os.WriteFile(artifact, []byte("binary"), 0o700); err != nil {
		t.Fatalf("artifact: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("server: {}\n"), 0o600); err != nil {
		t.Fatalf("config: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/health/ready" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"service":"rencrow-core","ready":true}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	var output bytes.Buffer
	var journalArgs []string
	deps := verifierDependencies{
		Platform:   func() string { return "linux" },
		HTTPClient: server.Client(),
		RunCommand: func(_ context.Context, name string, args []string) verifierCommandResult {
			switch name {
			case "systemctl":
				if len(args) > 1 && args[1] == "cat" {
					return verifierCommandResult{ExitCode: 0, Stdout: "[Service]\nExecStart=%h/.local/bin/rencrow run\nEnvironment=RENCROW_CONFIG=%h/.rencrow/config/core.yaml\nRestart=always\nStandardOutput=journal\nStandardError=journal\n"}
				}
				return verifierCommandResult{ExitCode: 0, Stdout: "ActiveState=active\nSubState=running\nResult=success\nMainPID=4242\nExecStart={ path=" + artifact + "; argv[]=rencrow run ; }\nEnvironment=RENCROW_CONFIG=" + configPath + "\n"}
			case "ss":
				return verifierCommandResult{ExitCode: 0, Stdout: `LISTEN 0 128 127.0.0.1:18790 0.0.0.0:* users:(("rencrow",pid=4242,fd=7))`}
			case "journalctl":
				journalArgs = append([]string(nil), args...)
				return verifierCommandResult{ExitCode: 0, Stdout: "startup_phase phase=config_load elapsed_ms=1\nstartup_phase phase=llm_gateway elapsed_ms=2\nstartup_phase phase=dependencies_total elapsed_ms=3\nstartup_phase phase=server_listen_ready elapsed_ms=4\nstartup_phase phase=startup_total elapsed_ms=5\n"}
			default:
				return verifierCommandResult{ExitCode: 127, Err: errors.New("unexpected command")}
			}
		},
		Readlink: func(string) (string, error) { return artifact, nil },
	}
	args := verifierArgs(manifest, "core_startup_phase_trace", t.TempDir())
	args = append(args, "--core-url", server.URL)
	code := runVerifierCLI(context.Background(), args, &output, &bytes.Buffer{}, deps)
	if code != verifierExitBlocked {
		t.Fatalf("exit=%d output=%s", code, output.String())
	}
	receipt := decodeVerifierReceipt(t, output.Bytes())
	if !strings.Contains(receipt.FailureBoundary, "request evidence") {
		t.Fatalf("receipt=%+v", receipt)
	}
	joinedJournalArgs := strings.Join(journalArgs, " ")
	if strings.Contains(joinedJournalArgs, "T00:00:00Z") ||
		!strings.Contains(joinedJournalArgs, "--until 2026-08-27 00:00:00 UTC") ||
		!strings.Contains(joinedJournalArgs, "--since 2026-08-26 00:00:00 UTC") {
		t.Fatalf("journalctl timestamps are not portable explicit UTC: %q", joinedJournalArgs)
	}

	requestEvidence := filepath.Join(t.TempDir(), "request.json")
	writeVerifierTestJSON(t, requestEvidence, map[string]any{
		"observed_at": verifierTestObservedAt,
		"status":      "passed",
		"request_id":  "startup-request-1",
		"trace_id":    "startup-trace-1",
		"route":       "CORE -> readiness",
	})
	args = append(args, "--request-evidence", requestEvidence)
	output.Reset()
	code = runVerifierCLI(context.Background(), args, &output, &bytes.Buffer{}, deps)
	if code != verifierExitPassed {
		t.Fatalf("fresh request evidence exit=%d output=%s", code, output.String())
	}
	receipt = decodeVerifierReceipt(t, output.Bytes())
	if receipt.Status != "passed" {
		t.Fatalf("fresh startup evidence receipt=%+v", receipt)
	}
}

func TestVerifierRuntimeIdentityUsesFakeSystemdAndSecurityProbe(t *testing.T) {
	manifest := testManifest(t, "core-runtime-identity-lifecycle-security")
	artifact := filepath.Join(t.TempDir(), "rencrow")
	configPath := filepath.Join(t.TempDir(), "core.yaml")
	if err := os.WriteFile(artifact, []byte("binary"), 0o700); err != nil {
		t.Fatalf("artifact: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("server: {}\n"), 0o600); err != nil {
		t.Fatalf("config: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/agent/ops" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	show := "ActiveState=active\nSubState=running\nResult=success\nMainPID=4242\nExecStart={ path=" + artifact + "; argv[]=" + artifact + " run ; }\nEnvironment=RENCROW_CONFIG=" + configPath + "\n"
	cat := "[Service]\nExecStart=%h/.local/bin/rencrow run\nEnvironment=RENCROW_CONFIG=%h/.rencrow/config/core.yaml\nRestart=always\nStandardOutput=journal\nStandardError=journal\n"
	deps := verifierDependencies{
		Platform:   func() string { return "linux" },
		HTTPClient: srv.Client(),
		Readlink:   func(string) (string, error) { return artifact, nil },
		RunCommand: func(_ context.Context, name string, args []string) verifierCommandResult {
			switch name {
			case "systemctl":
				if len(args) > 1 && args[1] == "cat" {
					return verifierCommandResult{ExitCode: 0, Stdout: cat}
				}
				return verifierCommandResult{ExitCode: 0, Stdout: show}
			case "ss":
				return verifierCommandResult{ExitCode: 0, Stdout: `LISTEN 0 128 127.0.0.1:18790 0.0.0.0:* users:(("rencrow",pid=4242,fd=7))`}
			default:
				return verifierCommandResult{ExitCode: 127, Err: errors.New("unexpected command")}
			}
		},
	}
	args := verifierArgs(manifest, "core_runtime_identity_lifecycle_security", t.TempDir())
	args = append(args, "--core-url", srv.URL, "--installed-artifact", artifact, "--config", configPath)
	var output bytes.Buffer
	code := runVerifierCLI(context.Background(), args, &output, &bytes.Buffer{}, deps)
	if code != verifierExitPassed {
		t.Fatalf("exit=%d output=%s", code, output.String())
	}
}

func TestVerifierMalformedManifestIsCLIErrorWithoutReceipt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":2,"purpose":"x","phase":"runtime","checks":[]}`), 0o600); err != nil {
		t.Fatalf("write malformed manifest: %v", err)
	}
	var output bytes.Buffer
	code := runVerifierCLI(context.Background(), verifierArgs(path, "core_health", t.TempDir()), &output, &bytes.Buffer{}, verifierDependencies{})
	if code != verifierExitCLIError || output.Len() != 0 {
		t.Fatalf("exit=%d output=%q", code, output.String())
	}
}

func TestVerifierDeployIdentityUsesCatalogSourceAndGoStamp(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	catalogPath := filepath.Join(root, "ecosystem.yaml")
	const revision = "0123456789abcdef0123456789abcdef01234567"
	workspace := filepath.Join(root, "RenCrow_CORE")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatalf("workspace: %v", err)
	}
	catalog := `{"schema_version":4,"components":{"core":{"repository":"Nyukimin/RenCrow_CORE","workspace_path":"./RenCrow_CORE","version":"` + revision + `"}}}`
	if err := os.WriteFile(catalogPath, []byte(catalog), 0o600); err != nil {
		t.Fatalf("catalog: %v", err)
	}
	artifact := filepath.Join(root, "rencrow")
	if err := os.WriteFile(artifact, []byte("binary"), 0o700); err != nil {
		t.Fatalf("artifact: %v", err)
	}
	checkerDir := filepath.Join(root, "scripts")
	if err := os.Mkdir(checkerDir, 0o700); err != nil {
		t.Fatalf("checker dir: %v", err)
	}
	checkerPath := filepath.Join(checkerDir, "check_deployed_binaries.py")
	if err := os.WriteFile(checkerPath, []byte("fixture"), 0o600); err != nil {
		t.Fatalf("checker: %v", err)
	}
	manifest := testManifest(t, "core-deploy-identity-chain")
	var checkerArgs []string
	deps := verifierDependencies{RunCommand: func(_ context.Context, name string, args []string) verifierCommandResult {
		switch name {
		case "systemctl":
			if slices.Contains(args, "cat") {
				return verifierCommandResult{ExitCode: 0, Stdout: "[Service]\nExecStart=" + artifact + " run\nEnvironment=RENCROW_CONFIG=/tmp/core.yaml\nRestart=always\nStandardOutput=journal\nStandardError=journal\n"}
			}
			return verifierCommandResult{ExitCode: 0, Stdout: "ActiveState=active\nSubState=running\nResult=success\nMainPID=4242\nExecStart={ path=" + artifact + "; argv[]=" + artifact + " run ; }\nEnvironment=RENCROW_CONFIG=/tmp/core.yaml\n"}
		case "git":
			if len(args) >= 4 && args[2] == "rev-parse" {
				return verifierCommandResult{ExitCode: 0, Stdout: revision + "\n"}
			}
			return verifierCommandResult{ExitCode: 0}
		case "go":
			return verifierCommandResult{ExitCode: 0, Stdout: "artifact\tpath\tgithub.com/Nyukimin/RenCrow_CORE/cmd/rencrow\nartifact\tmod\tgithub.com/Nyukimin/RenCrow_CORE (devel)\nartifact\tbuild\tvcs.revision=" + revision + "\nartifact\tbuild\tvcs.modified=false\n"}
		default:
			if strings.Contains(filepath.Base(name), "python") {
				checkerArgs = append([]string(nil), args...)
				return verifierCommandResult{ExitCode: 0, Stdout: `[{"component":"core","status":"MATCH","built_revision":"` + revision + `","pin_revision":"` + revision + `"}]`}
			}
			return verifierCommandResult{ExitCode: 127, Err: errors.New("unexpected command")}
		}
	}}
	args := verifierArgs(manifest, "core_deploy_identity_chain", t.TempDir())
	var output bytes.Buffer
	code := runVerifierCLI(context.Background(), args, &output, &bytes.Buffer{}, deps)
	if code != verifierExitPassed {
		t.Fatalf("exit=%d output=%s", code, output.String())
	}
	receipt := decodeVerifierReceipt(t, output.Bytes())
	if receipt.Status != "passed" || strings.Contains(output.String(), revision) {
		t.Fatalf("receipt=%+v output=%s", receipt, output.String())
	}
	if !slices.Contains(checkerArgs, catalogPath) || !slices.Contains(checkerArgs, root) {
		t.Fatalf("checker args=%#v", checkerArgs)
	}
}

func TestVerifierDeployUnstampedArtifactIsUnverified(t *testing.T) {
	root := t.TempDir()
	const revision = "0123456789abcdef0123456789abcdef01234567"
	catalogPath := filepath.Join(root, "ecosystem.yaml")
	catalog := `{"components":{"core":{"repository":"Nyukimin/RenCrow_CORE","workspace_path":".","version":"` + revision + `"}}}`
	if err := os.WriteFile(catalogPath, []byte(catalog), 0o600); err != nil {
		t.Fatalf("catalog: %v", err)
	}
	artifact := filepath.Join(root, "rencrow")
	if err := os.WriteFile(artifact, []byte("not-go"), 0o700); err != nil {
		t.Fatalf("artifact: %v", err)
	}
	manifest := testManifest(t, "core-deploy-identity-chain")
	deps := verifierDependencies{RunCommand: func(_ context.Context, name string, args []string) verifierCommandResult {
		switch name {
		case "git":
			if len(args) >= 4 && args[2] == "rev-parse" {
				return verifierCommandResult{ExitCode: 0, Stdout: revision + "\n"}
			}
			return verifierCommandResult{ExitCode: 0}
		case "go":
			return verifierCommandResult{ExitCode: 1, Err: errors.New("not a Go binary")}
		default:
			return verifierCommandResult{ExitCode: 127, Err: errors.New("unexpected command")}
		}
	}}
	args := verifierArgs(manifest, "core_deploy_identity_chain", t.TempDir())
	args = append(args, "--catalog", catalogPath, "--workspace", root, "--installed-artifact", artifact)
	var output bytes.Buffer
	code := runVerifierCLI(context.Background(), args, &output, &bytes.Buffer{}, deps)
	if code != verifierExitUnverified {
		t.Fatalf("exit=%d output=%s", code, output.String())
	}
	receipt := decodeVerifierReceipt(t, output.Bytes())
	if receipt.Status != "unverified" {
		t.Fatalf("receipt=%+v", receipt)
	}
}

func TestStartupRequestEvidenceFreshnessIsInclusiveAndFailClosed(t *testing.T) {
	observedAt, err := parseVerifierObservedAt(verifierTestObservedAt)
	if err != nil {
		t.Fatal(err)
	}
	lowerBound := observedAt.Add(-verifierEvidenceFreshnessWindow)
	cases := []struct {
		name       string
		evidenceAt time.Time
		wantStatus string
		wantWord   string
	}{
		{name: "inclusive lower bound", evidenceAt: lowerBound, wantStatus: "ok"},
		{name: "stale", evidenceAt: lowerBound.Add(-time.Nanosecond), wantStatus: "blocked", wantWord: "stale"},
		{name: "future", evidenceAt: observedAt.Add(time.Nanosecond), wantStatus: "blocked", wantWord: "future"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "request.json")
			writeVerifierTestJSON(t, path, map[string]any{
				"observed_at": tc.evidenceAt.Format(time.RFC3339Nano),
				"status":      "passed",
				"request_id":  "request-1",
				"trace_id":    "trace-1",
			})
			_, outcome := loadStartupRequestEvidence(path, observedAt)
			if tc.wantStatus == "ok" {
				if outcome.Status != "" {
					t.Fatalf("outcome=%+v, want success", outcome)
				}
				return
			}
			if outcome.Status != tc.wantStatus || !strings.Contains(strings.ToLower(outcome.FailureBoundary), tc.wantWord) {
				t.Fatalf("outcome=%+v, want %s/%s", outcome, tc.wantStatus, tc.wantWord)
			}
		})
	}
}

func TestSnapshotEvidenceFreshnessIsInclusiveAndFailClosed(t *testing.T) {
	observedAt, err := parseVerifierObservedAt(verifierTestObservedAt)
	if err != nil {
		t.Fatal(err)
	}
	lowerBound := observedAt.Add(-verifierEvidenceFreshnessWindow)
	jst := time.FixedZone("JST", 9*60*60)
	cases := []struct {
		name       string
		snapshotAt time.Time
		wantStatus string
		wantWord   string
	}{
		{name: "inclusive lower bound", snapshotAt: lowerBound, wantStatus: "ok"},
		{name: "stale", snapshotAt: lowerBound.Add(-time.Second), wantStatus: "blocked", wantWord: "stale"},
		{name: "future", snapshotAt: observedAt.Add(time.Second), wantStatus: "blocked", wantWord: "future"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			manifestPath := filepath.Join(dir, "manifest.txt")
			contents := "format_version=4\ncreated_at_jst=" + tc.snapshotAt.In(jst).Format("20060102-150405") + "\n"
			if err := os.WriteFile(manifestPath, []byte(contents), 0o600); err != nil {
				t.Fatalf("write snapshot manifest: %v", err)
			}
			_, outcome := validateSnapshotFreshness(dir, observedAt)
			if tc.wantStatus == "ok" {
				if outcome.Status != "" {
					t.Fatalf("outcome=%+v, want success", outcome)
				}
				return
			}
			if outcome.Status != tc.wantStatus || !strings.Contains(strings.ToLower(outcome.FailureBoundary), tc.wantWord) {
				t.Fatalf("outcome=%+v, want %s/%s", outcome, tc.wantStatus, tc.wantWord)
			}
		})
	}
}

func TestStampedDeploymentEvidenceFreshnessIfProvided(t *testing.T) {
	observedAt, err := parseVerifierObservedAt(verifierTestObservedAt)
	if err != nil {
		t.Fatal(err)
	}
	lowerBound := observedAt.Add(-verifierEvidenceFreshnessWindow)
	checkerPath := filepath.Join(t.TempDir(), "check_deployed_binaries.py")
	if err := os.WriteFile(checkerPath, []byte("fixture"), 0o700); err != nil {
		t.Fatalf("write checker: %v", err)
	}
	for _, tc := range []struct {
		name       string
		evidenceAt time.Time
		wantStatus string
		wantWord   string
	}{
		{name: "inclusive lower bound", evidenceAt: lowerBound, wantStatus: "passed"},
		{name: "stale", evidenceAt: lowerBound.Add(-time.Nanosecond), wantStatus: "blocked", wantWord: "stale"},
		{name: "future", evidenceAt: observedAt.Add(time.Nanosecond), wantStatus: "blocked", wantWord: "future"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := verifierDependencies{
				RunCommand: func(context.Context, string, []string) verifierCommandResult {
					return verifierCommandResult{ExitCode: 0, Stdout: fmt.Sprintf(`[{"component":"core","status":"MATCH","observed_at":%q}]`, tc.evidenceAt.Format(time.RFC3339Nano))}
				},
			}
			outcome := runStampedDeploymentChecker(context.Background(), verifierOptions{
				ObservedAt: observedAt, CatalogPath: "catalog.json", WorkspacePath: "workspace",
				StampedChecker: checkerPath, Python: "python3",
			}, deps)
			if outcome.Status != tc.wantStatus || (tc.wantWord != "" && !strings.Contains(strings.ToLower(outcome.FailureBoundary), tc.wantWord)) {
				t.Fatalf("outcome=%+v, want %s/%s", outcome, tc.wantStatus, tc.wantWord)
			}
		})
	}
}

func writeVerifierTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal test JSON: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write test JSON: %v", err)
	}
}
