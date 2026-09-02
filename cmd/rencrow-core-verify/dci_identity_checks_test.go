package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestDCIIdentityCommandsAreFixedAndAllowlisted(t *testing.T) {
	for command, checkID := range map[string]string{
		"core-dci-identity-pre-restart":  "core_dci_identity_pre_restart",
		"core-dci-identity-post-restart": "core_dci_identity_post_restart",
		"core-dci-identity-final":        "core_dci_identity_final",
	} {
		if got, ok := checkIDForCommand(command); !ok || got != checkID {
			t.Fatalf("checkIDForCommand(%q) = %q, %t; want %q, true", command, got, ok, checkID)
		}
		if _, ok := verifierForCommand(command); !ok {
			t.Fatalf("verifierForCommand(%q) is not implemented", command)
		}
	}
}

type dciIdentityTestRuntime struct {
	artifact string
	config   string
	token    string
	pid      int64
	post     bool

	requestIDs  [][]string
	requestRaw  [][]byte
	requestAuth []string
}

func newDCIIdentityTestRuntime(t *testing.T) (*dciIdentityTestRuntime, *httptest.Server, verifierDependencies) {
	t.Helper()
	root := t.TempDir()
	runtimeState := &dciIdentityTestRuntime{
		artifact: filepath.Join(root, "rencrow"),
		config:   filepath.Join(root, "core.yaml"),
		token:    filepath.Join(root, "agent-ops.token"),
		pid:      4101,
	}
	if err := os.WriteFile(runtimeState.artifact, []byte("dci-test-artifact"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimeState.token, []byte("owner-token-012345678901234567890123"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := "local_agent_ops:\n  enabled: true\n  auth_token_file: \"" + runtimeState.token + "\"\n"
	if err := os.WriteFile(runtimeState.config, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/health/ready":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"service":"rencrow-core","ready":true}`))
		case r.Method == http.MethodPost && r.URL.Path == dciIdentityRoute:
			body, err := ioReadAllBounded(r)
			if err != nil {
				t.Errorf("read request body: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			runtimeState.requestRaw = append(runtimeState.requestRaw, body)
			runtimeState.requestIDs = append(runtimeState.requestIDs, []string{r.Header.Get("X-Request-ID"), r.Header.Get("X-RenCrow-Client"), r.Header.Get("X-RenCrow-Interaction-Profile")})
			runtimeState.requestAuth = append(runtimeState.requestAuth, r.Header.Get("Authorization"))
			var request map[string]any
			if err := json.Unmarshal(body, &request); err != nil {
				t.Errorf("decode request body: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if len(request) != 2 || request["operation"] != dciIdentityOperation || request["query"] != dciIdentityFixedQuery {
				t.Errorf("request body=%s", body)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			response := dciIdentityTestResponse(r.Header.Get("X-Request-ID"), runtimeState.post)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(response)
		default:
			http.NotFound(w, r)
		}
	}))

	show := func() string {
		return fmt.Sprintf("ActiveState=active\nSubState=running\nResult=success\nMainPID=%d\nExecStart={ path=%s; argv[]=%s run ; }\nEnvironment=RENCROW_CONFIG=%s\n", runtimeState.pid, runtimeState.artifact, runtimeState.artifact, runtimeState.config)
	}
	cat := "[Service]\nExecStart=" + runtimeState.artifact + " run\nEnvironment=RENCROW_CONFIG=" + runtimeState.config + "\nRestart=always\nStandardOutput=journal\nStandardError=journal\n"
	deps := verifierDependencies{
		HTTPClient: server.Client(),
		Platform:   func() string { return "linux" },
		Readlink:   func(string) (string, error) { return runtimeState.artifact, nil },
		RunCommand: func(_ context.Context, name string, args []string) verifierCommandResult {
			switch name {
			case "systemctl":
				if slices.Contains(args, "cat") {
					return verifierCommandResult{ExitCode: 0, Stdout: cat}
				}
				return verifierCommandResult{ExitCode: 0, Stdout: show()}
			case "ss":
				return verifierCommandResult{ExitCode: 0, Stdout: fmt.Sprintf(`LISTEN 0 128 127.0.0.1:18790 0.0.0.0:* users:(("rencrow",pid=%d,fd=7))`, runtimeState.pid)}
			default:
				return verifierCommandResult{ExitCode: 127, Err: errors.New("unexpected command")}
			}
		},
		Lstat: func(path string) (os.FileInfo, error) {
			if path == filepath.Join(string(filepath.Separator)+"proc", "4101") {
				return nil, os.ErrNotExist
			}
			return os.Lstat(path)
		},
	}
	return runtimeState, server, deps
}

func ioReadAllBounded(r *http.Request) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxVerifierBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxVerifierBodyBytes {
		return nil, errors.New("request body exceeds bound")
	}
	return data, nil
}

func sortedJSONKeysFromRaw(fields map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func dciIdentityTestResponse(requestID string, post bool) dciIdentityResponse {
	return dciIdentityResponse{
		SchemaVersion: dciIdentityResponseSchema, Status: "passed", RequestID: requestID,
		AgentID: "shiro", Role: "worker", Operation: dciIdentityOperation,
		ActionID: "act_00000000-0000-5000-8000-000000000001", TraceID: "trc_00000000-0000-5000-8000-000000000002",
		FirstWriteReplay: post, SecondWriteReplay: true, EventCount: 6, StepCount: 1, EvidenceCount: 1,
		CurrentProjectionCount: 1, ArchiveProjectionCount: 1, EventGraphSHA256: strings.Repeat("a", 64),
	}
}

func issueDCIIdentityPreEvidence(t *testing.T, server *httptest.Server, deps verifierDependencies, evidenceDir string) string {
	t.Helper()
	manifest := testManifest(t, dciIdentityPreCommandID)
	args := verifierArgs(manifest, "core_dci_identity_pre_restart", evidenceDir)
	args = append(args, "--core-url", server.URL)
	var output bytes.Buffer
	if code := runVerifierCLI(context.Background(), args, &output, &bytes.Buffer{}, deps); code != verifierExitPassed {
		t.Fatalf("pre exit=%d output=%s", code, output.String())
	}
	receipt := decodeVerifierReceipt(t, output.Bytes())
	return filepath.Join(evidenceDir, strings.TrimPrefix(receipt.EvidenceRefs[0], "relative:"))
}

func TestDCIIdentityPreAndPostUseFixedAuthenticatedRouteAndBoundedEvidence(t *testing.T) {
	runtimeState, server, deps := newDCIIdentityTestRuntime(t)
	defer server.Close()
	evidenceDir := t.TempDir()
	preManifest := testManifest(t, dciIdentityPreCommandID)
	preArgs := verifierArgs(preManifest, "core_dci_identity_pre_restart", evidenceDir)
	preArgs = append(preArgs, "--core-url", server.URL)
	var preOutput bytes.Buffer
	if code := runVerifierCLI(context.Background(), preArgs, &preOutput, &bytes.Buffer{}, deps); code != verifierExitPassed {
		t.Fatalf("pre exit=%d output=%s", code, preOutput.String())
	}
	preReceipt := decodeVerifierReceipt(t, preOutput.Bytes())
	prePath := filepath.Join(evidenceDir, strings.TrimPrefix(preReceipt.EvidenceRefs[0], "relative:"))
	preRaw, err := os.ReadFile(prePath)
	if err != nil {
		t.Fatal(err)
	}
	preInfo, err := os.Lstat(prePath)
	if err != nil || preInfo.Mode().Perm() != 0o600 || preInfo.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("pre evidence mode/info=%v/%v", preInfo, err)
	}
	if strings.Contains(string(preRaw), dciIdentityFixedQuery) || strings.Contains(string(preRaw), runtimeState.token) || strings.Contains(preOutput.String(), runtimeState.token) {
		t.Fatal("pre receipt/evidence leaked fixture or token")
	}
	var preEvidence map[string]any
	if err := decodeStrictJSON(preRaw, &preEvidence); err != nil {
		t.Fatal(err)
	}
	if preEvidence["phase"] != dciIdentityPrePhase || preEvidence["first_write_replay"] != false || preEvidence["second_write_replay"] != true || preEvidence["listener_ready"] != true || preEvidence["readiness_ready"] != true {
		t.Fatalf("pre evidence=%#v", preEvidence)
	}
	if len(runtimeState.requestRaw) != 1 || string(runtimeState.requestRaw[0]) != `{"operation":"dci_identity_acceptance","query":"RenCrow identity architecture"}` {
		t.Fatalf("pre request body=%q", runtimeState.requestRaw)
	}
	if runtimeState.requestAuth[0] != "Bearer owner-token-012345678901234567890123" || runtimeState.requestIDs[0][1] != "RenCrow_CMD" || runtimeState.requestIDs[0][2] != "agent-ops" {
		t.Fatalf("pre headers=%#v/%#v", runtimeState.requestAuth, runtimeState.requestIDs)
	}

	runtimeState.pid = 4102
	runtimeState.post = true
	postManifest := testManifest(t, dciIdentityPostCommandID)
	postArgs := verifierArgs(postManifest, "core_dci_identity_post_restart", evidenceDir)
	postArgs = append(postArgs, "--core-url", server.URL, "--dci-pre-restart-evidence", prePath)
	var postOutput bytes.Buffer
	if code := runVerifierCLI(context.Background(), postArgs, &postOutput, &bytes.Buffer{}, deps); code != verifierExitPassed {
		t.Fatalf("post exit=%d output=%s", code, postOutput.String())
	}
	postReceipt := decodeVerifierReceipt(t, postOutput.Bytes())
	postPath := filepath.Join(evidenceDir, strings.TrimPrefix(postReceipt.EvidenceRefs[0], "relative:"))
	postRaw, err := os.ReadFile(postPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(postRaw), dciIdentityFixedQuery) || strings.Contains(string(postRaw), runtimeState.token) || strings.Contains(postOutput.String(), runtimeState.token) || strings.Contains(string(postRaw), "4101") {
		t.Fatal("post receipt/evidence leaked fixture, token, or prior pid")
	}
	var postEvidence map[string]any
	if err := decodeStrictJSON(postRaw, &postEvidence); err != nil {
		t.Fatal(err)
	}
	var postEvidenceFields map[string]json.RawMessage
	if err := decodeStrictJSON(postRaw, &postEvidenceFields); err != nil {
		t.Fatal(err)
	}
	if !hasExactJSONKeys(postEvidenceFields, dciIdentityPostEvidenceKeys) {
		t.Fatalf("post evidence keys=%v, want=%v", sortedJSONKeysFromRaw(postEvidenceFields), dciIdentityPostEvidenceKeys)
	}
	if _, ok := postEvidenceFields["service_main_pid"]; ok {
		t.Fatal("post evidence must not contain service_main_pid")
	}
	var postSchema string
	if err := json.Unmarshal(postEvidenceFields["evidence_schema"], &postSchema); err != nil || postSchema != dciIdentityPostEvidenceSchema {
		t.Fatalf("post evidence schema=%q/%v", postSchema, err)
	}
	if postEvidence["phase"] != dciIdentityPostPhase || postEvidence["first_write_replay"] != true || postEvidence["second_write_replay"] != true || postEvidence["old_generation_absent"] != true || postEvidence["pre_restart_evidence_sha256"] != sha256Text(string(preRaw)) {
		t.Fatalf("post evidence=%#v", postEvidence)
	}
	if len(runtimeState.requestRaw) != 2 || runtimeState.requestIDs[0][0] != runtimeState.requestIDs[1][0] || string(runtimeState.requestRaw[0]) != string(runtimeState.requestRaw[1]) {
		t.Fatalf("same request chain bodies/ids=%q/%#v", runtimeState.requestRaw, runtimeState.requestIDs)
	}
}

func TestDCIIdentityResponseRejectsUnknownMissingAndTamperedFacts(t *testing.T) {
	requestID := "dci-test-request"
	base := dciIdentityTestResponse(requestID, false)
	encoded, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "unknown", mutate: func(value map[string]any) { value["output"] = "not allowed" }},
		{name: "missing", mutate: func(value map[string]any) { delete(value, "trace_id") }},
		{name: "null replay", mutate: func(value map[string]any) { value["first_write_replay"] = nil }},
		{name: "bad counts", mutate: func(value map[string]any) { value["event_count"] = 7 }},
		{name: "bad graph", mutate: func(value map[string]any) { value["event_graph_sha256"] = strings.Repeat("B", 64) }},
		{name: "wrong request", mutate: func(value map[string]any) { value["request_id"] = "other-request" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var value map[string]any
			if err := json.Unmarshal(encoded, &value); err != nil {
				t.Fatal(err)
			}
			tc.mutate(value)
			mutated, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := decodeDCIIdentityResponse(mutated, requestID); err == nil {
				t.Fatal("tampered response was accepted")
			}
		})
	}
}

func TestDCIDeployRevisionRequiresOwnerOnlyStrictLatestPair(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deploy.jsonl")
	record := func(binary, revision string) string {
		value := map[string]any{
			"schema_version": 1, "receipt_id": "receipt-1", "started_at": "2026-09-02T00:00:00Z",
			"finished_at": "2026-09-02T00:00:01Z", "component": "core", "binary_path": "/opt/" + binary,
			"from_revision": strings.Repeat("a", 40), "target_revision": revision, "running_units": []string{},
			"phase": "complete", "outcome": "success", "rollback_outcome": "not_attempted", "backup_path": "/backup",
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded) + "\n"
	}
	oldRevision := strings.Repeat("b", 40)
	latestRevision := strings.Repeat("c", 40)
	deferred := strings.Replace(record("rencrow", latestRevision), `"phase":"complete"`, `"deferred_units":["rencrow-resilience.service"],"reason":"active oneshot","phase":"preflight"`, 1)
	deferred = strings.Replace(deferred, `"outcome":"success"`, `"outcome":"deferred"`, 1)
	failed := strings.Replace(record("rencrow", latestRevision), `"phase":"complete"`, `"error":"readiness failed","failed_unit":"rencrow.service","phase":"readiness"`, 1)
	failed = strings.Replace(failed, `"outcome":"success"`, `"outcome":"failed"`, 1)
	content := record("rencrow", oldRevision) + record("rencrow-core-verify", oldRevision) + deferred + failed + record("rencrow", latestRevision) + record("rencrow-core-verify", latestRevision)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	revision, hash, err := loadDCIDeployRevision(path, verifierDependencies{Platform: func() string { return "linux" }})
	if err != nil || revision != latestRevision || hash != sha256Text(content) {
		t.Fatalf("revision/hash/err=%q/%q/%v", revision, hash, err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadDCIDeployRevision(path, verifierDependencies{Platform: func() string { return "linux" }}); err == nil {
		t.Fatal("non-owner-only deploy receipt was accepted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimSuffix(content, "\n")[:len(strings.TrimSuffix(content, "\n"))-1]+`,"unknown":true}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadDCIDeployRevision(path, verifierDependencies{Platform: func() string { return "linux" }}); err == nil {
		t.Fatal("unknown deploy receipt field was accepted")
	}
}

func TestDCIIdentityResponseRejectsDuplicateKeyAndTrailingValue(t *testing.T) {
	requestID := "dci-test-request"
	encoded, err := json.Marshal(dciIdentityTestResponse(requestID, false))
	if err != nil {
		t.Fatal(err)
	}
	trimmed := bytes.TrimSpace(encoded)
	duplicate := append([]byte(nil), trimmed[:len(trimmed)-1]...)
	duplicate = append(duplicate, []byte(`,"status":"passed"}`)...)
	for name, raw := range map[string][]byte{
		"duplicate key":  duplicate,
		"trailing value": append(append([]byte(nil), encoded...), []byte(`{}`)...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := decodeDCIIdentityResponse(raw, requestID); err == nil {
				t.Fatal("non-single response value was accepted")
			}
		})
	}
}

func TestDCIIdentityPostRejectsStaleTamperedAndMismatchedPriorEvidence(t *testing.T) {
	runtimeState, server, deps := newDCIIdentityTestRuntime(t)
	defer server.Close()
	evidenceDir := t.TempDir()
	preManifest := testManifest(t, dciIdentityPreCommandID)
	preArgs := verifierArgs(preManifest, "core_dci_identity_pre_restart", evidenceDir)
	preArgs = append(preArgs, "--core-url", server.URL)
	var preOutput bytes.Buffer
	if code := runVerifierCLI(context.Background(), preArgs, &preOutput, &bytes.Buffer{}, deps); code != verifierExitPassed {
		t.Fatalf("pre exit=%d output=%s", code, preOutput.String())
	}
	preReceipt := decodeVerifierReceipt(t, preOutput.Bytes())
	prePath := filepath.Join(evidenceDir, strings.TrimPrefix(preReceipt.EvidenceRefs[0], "relative:"))
	preRaw, err := os.ReadFile(prePath)
	if err != nil {
		t.Fatal(err)
	}
	var evidence map[string]any
	if err := json.Unmarshal(preRaw, &evidence); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		mutate func(map[string]any)
		want   int
	}{
		{name: "tampered fixture", mutate: func(value map[string]any) { value["fixture_sha256"] = strings.Repeat("b", 64) }, want: verifierExitBlocked},
		{name: "request mismatch", mutate: func(value map[string]any) { value["request_id"] = "different-request" }, want: verifierExitBlocked},
		{name: "stale", mutate: func(value map[string]any) { value["observed_at"] = "2026-08-26T23:54:59Z" }, want: verifierExitBlocked},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mutated := make(map[string]any, len(evidence))
			for key, value := range evidence {
				mutated[key] = value
			}
			tc.mutate(mutated)
			path := filepath.Join(t.TempDir(), "pre.json")
			writeVerifierTestJSON(t, path, mutated)
			postManifest := testManifest(t, dciIdentityPostCommandID)
			args := verifierArgs(postManifest, "core_dci_identity_post_restart", t.TempDir())
			args = append(args, "--core-url", server.URL, "--dci-pre-restart-evidence", path)
			var output bytes.Buffer
			code := runVerifierCLI(context.Background(), args, &output, &bytes.Buffer{}, deps)
			if code != tc.want {
				t.Fatalf("exit=%d output=%s", code, output.String())
			}
			if strings.Contains(output.String(), dciIdentityFixedQuery) || strings.Contains(output.String(), runtimeState.token) || strings.Contains(output.String(), "different-request") {
				t.Fatal("failure receipt leaked caller-controlled or secret data")
			}
		})
	}
}

func TestDCIIdentityPostRejectsPriorModeFactsTamperAndMissingEvidence(t *testing.T) {
	runtimeState, server, deps := newDCIIdentityTestRuntime(t)
	defer server.Close()
	preEvidenceDir := t.TempDir()
	prePath := issueDCIIdentityPreEvidence(t, server, deps, preEvidenceDir)
	preRaw, err := os.ReadFile(prePath)
	if err != nil {
		t.Fatal(err)
	}
	var preFields map[string]any
	if err := json.Unmarshal(preRaw, &preFields); err != nil {
		t.Fatal(err)
	}
	factsTamperedPath := filepath.Join(t.TempDir(), "pre-facts-tampered.json")
	preFields["response_facts_sha256"] = strings.Repeat("0", 64)
	writeVerifierTestJSON(t, factsTamperedPath, preFields)
	wrongModePath := filepath.Join(t.TempDir(), "pre-wrong-mode.json")
	if err := os.WriteFile(wrongModePath, preRaw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(wrongModePath, 0o644); err != nil {
		t.Fatal(err)
	}
	missingPath := filepath.Join(t.TempDir(), "missing-pre.json")
	manifest := testManifest(t, dciIdentityPostCommandID)
	for name, priorPath := range map[string]string{
		"wrong mode":     wrongModePath,
		"facts tampered": factsTamperedPath,
		"missing prior":  missingPath,
	} {
		t.Run(name, func(t *testing.T) {
			args := verifierArgs(manifest, "core_dci_identity_post_restart", t.TempDir())
			args = append(args, "--core-url", server.URL, "--dci-pre-restart-evidence", priorPath)
			var output bytes.Buffer
			if code := runVerifierCLI(context.Background(), args, &output, &bytes.Buffer{}, deps); code != verifierExitBlocked {
				t.Fatalf("exit=%d output=%s", code, output.String())
			}
		})
	}
	if len(runtimeState.requestRaw) != 1 {
		t.Fatalf("prior evidence rejection issued unexpected requests=%d", len(runtimeState.requestRaw))
	}
}

func TestDCIIdentityPostRejectsConfigHashChangeBeforeRequest(t *testing.T) {
	runtimeState, server, deps := newDCIIdentityTestRuntime(t)
	defer server.Close()
	prePath := issueDCIIdentityPreEvidence(t, server, deps, t.TempDir())
	changedConfig := "local_agent_ops:\n  enabled: true\n  auth_token_file: \"" + runtimeState.token + "\"\n  changed: true\n"
	if err := os.WriteFile(runtimeState.config, []byte(changedConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	runtimeState.pid = 4102
	args := verifierArgs(testManifest(t, dciIdentityPostCommandID), "core_dci_identity_post_restart", t.TempDir())
	args = append(args, "--core-url", server.URL, "--dci-pre-restart-evidence", prePath)
	var output bytes.Buffer
	if code := runVerifierCLI(context.Background(), args, &output, &bytes.Buffer{}, deps); code != verifierExitFailed {
		t.Fatalf("exit=%d output=%s", code, output.String())
	}
	if len(runtimeState.requestRaw) != 1 {
		t.Fatalf("config mismatch must stop before POST, requests=%d", len(runtimeState.requestRaw))
	}
}

func TestDCIIdentityPostRejectsOldGenerationAndRequestIDMismatch(t *testing.T) {
	runtimeState, server, deps := newDCIIdentityTestRuntime(t)
	defer server.Close()
	evidenceDir := t.TempDir()
	preManifest := testManifest(t, dciIdentityPreCommandID)
	preArgs := verifierArgs(preManifest, "core_dci_identity_pre_restart", evidenceDir)
	preArgs = append(preArgs, "--core-url", server.URL, "--request-id", "dci-restart-request")
	var preOutput bytes.Buffer
	if code := runVerifierCLI(context.Background(), preArgs, &preOutput, &bytes.Buffer{}, deps); code != verifierExitPassed {
		t.Fatalf("pre exit=%d output=%s", code, preOutput.String())
	}
	preReceipt := decodeVerifierReceipt(t, preOutput.Bytes())
	prePath := filepath.Join(evidenceDir, strings.TrimPrefix(preReceipt.EvidenceRefs[0], "relative:"))
	runtimeState.post = true
	postManifest := testManifest(t, dciIdentityPostCommandID)
	runtimeState.pid = 4101
	args := verifierArgs(postManifest, "core_dci_identity_post_restart", t.TempDir())
	args = append(args, "--core-url", server.URL, "--dci-pre-restart-evidence", prePath, "--request-id", "different-request")
	var output bytes.Buffer
	if code := runVerifierCLI(context.Background(), args, &output, &bytes.Buffer{}, deps); code != verifierExitFailed {
		t.Fatalf("request mismatch exit=%d output=%s", code, output.String())
	}
	if strings.Contains(output.String(), "different-request") {
		t.Fatal("request id leaked")
	}
	args = verifierArgs(postManifest, "core_dci_identity_post_restart", t.TempDir())
	args = append(args, "--core-url", server.URL, "--dci-pre-restart-evidence", prePath)
	output.Reset()
	if code := runVerifierCLI(context.Background(), args, &output, &bytes.Buffer{}, deps); code != verifierExitFailed {
		t.Fatalf("same generation exit=%d output=%s", code, output.String())
	}
}

func TestDCIIdentityReplayBoundariesRejectWrongPreAndPostReplay(t *testing.T) {
	t.Run("wrong pre replay", func(t *testing.T) {
		runtimeState, server, deps := newDCIIdentityTestRuntime(t)
		defer server.Close()
		runtimeState.post = true
		args := verifierArgs(testManifest(t, dciIdentityPreCommandID), "core_dci_identity_pre_restart", t.TempDir())
		args = append(args, "--core-url", server.URL)
		var output bytes.Buffer
		if code := runVerifierCLI(context.Background(), args, &output, &bytes.Buffer{}, deps); code != verifierExitFailed {
			t.Fatalf("exit=%d output=%s", code, output.String())
		}
	})

	t.Run("wrong post replay", func(t *testing.T) {
		runtimeState, server, deps := newDCIIdentityTestRuntime(t)
		defer server.Close()
		prePath := issueDCIIdentityPreEvidence(t, server, deps, t.TempDir())
		runtimeState.pid = 4102
		args := verifierArgs(testManifest(t, dciIdentityPostCommandID), "core_dci_identity_post_restart", t.TempDir())
		args = append(args, "--core-url", server.URL, "--dci-pre-restart-evidence", prePath)
		var output bytes.Buffer
		if code := runVerifierCLI(context.Background(), args, &output, &bytes.Buffer{}, deps); code != verifierExitFailed {
			t.Fatalf("exit=%d output=%s", code, output.String())
		}
		if len(runtimeState.requestRaw) != 2 {
			t.Fatalf("wrong post replay must still represent the attempted request, requests=%d", len(runtimeState.requestRaw))
		}
	})
}

func TestDCIIdentityPostWithoutPriorEvidenceIsBlocked(t *testing.T) {
	_, server, deps := newDCIIdentityTestRuntime(t)
	defer server.Close()
	args := verifierArgs(testManifest(t, dciIdentityPostCommandID), "core_dci_identity_post_restart", t.TempDir())
	args = append(args, "--core-url", server.URL)
	var output bytes.Buffer
	if code := runVerifierCLI(context.Background(), args, &output, &bytes.Buffer{}, deps); code != verifierExitBlocked {
		t.Fatalf("exit=%d output=%s", code, output.String())
	}
}

func TestDCIIdentityPostRejectsRetainedOldGenerationAndChangedArtifact(t *testing.T) {
	runtimeState, server, deps := newDCIIdentityTestRuntime(t)
	defer server.Close()
	evidenceDir := t.TempDir()
	preManifest := testManifest(t, dciIdentityPreCommandID)
	preArgs := verifierArgs(preManifest, "core_dci_identity_pre_restart", evidenceDir)
	preArgs = append(preArgs, "--core-url", server.URL)
	var preOutput bytes.Buffer
	if code := runVerifierCLI(context.Background(), preArgs, &preOutput, &bytes.Buffer{}, deps); code != verifierExitPassed {
		t.Fatalf("pre exit=%d output=%s", code, preOutput.String())
	}
	preReceipt := decodeVerifierReceipt(t, preOutput.Bytes())
	prePath := filepath.Join(evidenceDir, strings.TrimPrefix(preReceipt.EvidenceRefs[0], "relative:"))
	postManifest := testManifest(t, dciIdentityPostCommandID)
	postArgs := verifierArgs(postManifest, "core_dci_identity_post_restart", evidenceDir)
	postArgs = append(postArgs, "--core-url", server.URL, "--dci-pre-restart-evidence", prePath)

	runtimeState.pid = 4102
	baseLstat := deps.Lstat
	deps.Lstat = func(path string) (os.FileInfo, error) {
		if path == filepath.Join(string(filepath.Separator)+"proc", "4101") {
			return os.Stat(runtimeState.artifact)
		}
		return baseLstat(path)
	}
	var output bytes.Buffer
	if code := runVerifierCLI(context.Background(), postArgs, &output, &bytes.Buffer{}, deps); code != verifierExitFailed {
		t.Fatalf("retained generation exit=%d output=%s", code, output.String())
	}
	if len(runtimeState.requestRaw) != 1 {
		t.Fatal("retained old generation must stop before the POST route")
	}

	deps.Lstat = baseLstat
	if err := os.WriteFile(runtimeState.artifact, []byte("changed-artifact"), 0o700); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if code := runVerifierCLI(context.Background(), postArgs, &output, &bytes.Buffer{}, deps); code != verifierExitFailed {
		t.Fatalf("changed artifact exit=%d output=%s", code, output.String())
	}
}

func TestDCIIdentityRequestIDDerivationIsBoundedAndDeterministic(t *testing.T) {
	observedAt, err := parseVerifierObservedAt(verifierTestObservedAt)
	if err != nil {
		t.Fatal(err)
	}
	first, ok := dciIdentityRequestID("", observedAt, true)
	if !ok || first != "core-dci-20260827T000000.000000000Z" {
		t.Fatalf("derived request id=%q/%t", first, ok)
	}
	second, ok := dciIdentityRequestID(first, observedAt, false)
	if !ok || second != first {
		t.Fatalf("explicit request id=%q/%t", second, ok)
	}
	if _, ok := dciIdentityRequestID(strings.Repeat("x", verifierMaxActorRequestIDBytes+1), observedAt, false); ok {
		t.Fatal("oversize request id accepted")
	}
	if _, ok := dciIdentityRequestID(" request-id ", observedAt, false); ok {
		t.Fatal("whitespace-padded request id accepted")
	}
}

func TestDCIIdentityUsesLinuxBoundary(t *testing.T) {
	manifest := testManifest(t, dciIdentityPreCommandID)
	args := verifierArgs(manifest, "core_dci_identity_pre_restart", t.TempDir())
	var output bytes.Buffer
	code := runVerifierCLI(context.Background(), args, &output, &bytes.Buffer{}, verifierDependencies{Platform: func() string { return runtime.GOOS }})
	if runtime.GOOS != "linux" && code != verifierExitBlocked {
		t.Fatalf("platform=%s exit=%d output=%s", runtime.GOOS, code, output.String())
	}
}
