// Package harnessdeploy builds a throw-away RenCrow_Harness deployment for CORE's
// integration tests: the real rencrow-harness binary, built from the Harness
// module that is resolved for this build, a configuration for the CORE caller
// profile, and a Gateway double that speaks the strict LLM contract (harness-v1)
// and serves scripted replies.
//
// It is test support only. Nothing outside a test imports it, nothing in a
// configuration can select the double, and everything it creates lives under
// t.TempDir(): no real credential, key, path or host is involved, and the key it
// writes is random per test.
//
// The Harness has its own double in an internal package that CORE may not import;
// this one implements only what the CORE tests need, and takes the one computed
// value that has to match the Harness (the logical input digest) from the
// Harness's public package pkg/protocol.
package harnessdeploy

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/Nyukimin/RenCrow_Harness/pkg/protocol"
)

const (
	// BuildRevision is the build_revision the test binary is built with and that
	// CORE's settings pin.
	BuildRevision = "core-integration-build-1"
	// Issuer, KeyID and Audience are the CORE issuer values of the Harness caller
	// profile.
	Issuer   = "core:test-issuer"
	KeyID    = "test-key-1"
	Audience = "core:local"

	fingerprint = "bfp-v1:cf25685e3cbff5f9364f8bdadb54d8b852578931ad2602a62e077613b3a6ef8d"
	harnessPkg  = "github.com/Nyukimin/RenCrow_Harness"
)

// Deployment is one throw-away tree: configuration, registry, store, workspace
// and the binary.
type Deployment struct {
	Binary    string
	Config    string
	Data      string
	Work      string
	Secrets   string
	KeyFile   string
	Binding   protocol.Binding
	PolicyRef string
	Gateway   *Gateway

	cfg map[string]any
}

// New builds the deployment: it builds the Harness binary, writes the
// configuration (caller core:local with default origin automation, the CORE
// issuer allowlisted, an alias binding for Shiro), starts the Gateway double and
// initializes the store. It skips the test under -short because it builds and
// runs the real binary.
func New(t testing.TB) *Deployment {
	return newDeployment(t, false)
}

// NewWithVerification adds a fixed, owner-managed process verifier to the
// fixture. Existing integration cases keep the verifier-free fixture through
// New; this opt-in fixture exercises the current verifier contract.
func NewWithVerification(t testing.TB) *Deployment {
	return newDeployment(t, true)
}

func newDeployment(t testing.TB, withVerification bool) *Deployment {
	t.Helper()
	if testing.Short() {
		t.Skip("builds and runs the real Harness binary")
	}
	moduleDir := goOutput(t, "", "list", "-m", "-f", "{{.Dir}}", harnessPkg)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := &Deployment{
		Data:    filepath.Join(root, "data"),
		Work:    filepath.Join(root, "work"),
		Secrets: filepath.Join(root, "secrets"),
		Binding: protocol.Binding{
			Kind: "alias", Selector: "shiro-worker-exec", ProfileRevision: "fixture-profile-1",
			AgentID: protocol.Str("shiro"), ExecutionRole: protocol.Str("worker"),
		},
	}
	backup := filepath.Join(root, "backup")
	for _, dir := range []struct {
		path string
		mode os.FileMode
	}{{backup, 0o700}, {d.Work, 0o755}, {d.Secrets, 0o700}} {
		if err := os.MkdirAll(dir.path, dir.mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir.path, dir.mode); err != nil {
			t.Fatal(err)
		}
	}
	d.KeyFile = filepath.Join(d.Secrets, "origin.key")
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		t.Fatal(err)
	}
	writeFile(t, d.KeyFile, hex.EncodeToString(keyBytes)+"\n")

	cfg := readExample(t, moduleDir, "config.json")
	registry := readExample(t, moduleDir, "policies.json")
	d.PolicyRef = cfg["workspaces"].([]any)[0].(map[string]any)["policy_ref"].(string)
	registryPath := filepath.Join(d.Secrets, "policies.json")
	d.Config = filepath.Join(d.Secrets, "config.json")

	cfg["data_root"] = d.Data
	cfg["policy_registry_path"] = registryPath
	cfg["storage"].(map[string]any)["backup_root"] = backup
	cfg["workspaces"].([]any)[0].(map[string]any)["root"] = d.Work
	cfg["caller"] = map[string]any{
		"principal": "core:local", "default_origin": "automation",
		"readable_session_owners": []any{"core:local"}, "controllable_session_owners": []any{"core:local"},
		"relay_issuers": []any{map[string]any{"issuer": Issuer, "key_id": KeyID, "audience": Audience, "key_file": d.KeyFile}},
	}
	cfg["bindings"] = []any{map[string]any{"profile_name": BindingProfile, "binding": map[string]any{
		"kind": d.Binding.Kind, "selector": d.Binding.Selector, "profile_revision": d.Binding.ProfileRevision,
		"agent_id": *d.Binding.AgentID, "execution_role": *d.Binding.ExecutionRole,
	}}}
	if withVerification {
		configureVerificationFixture(t, d, cfg, registry)
	}

	d.Gateway = newGateway(t, d.Binding)
	cfg["gateway"].(map[string]any)["base_url"] = d.Gateway.BaseURL()
	writeJSON(t, registryPath, registry)
	writeJSON(t, d.Config, cfg)
	d.cfg = cfg

	d.Binary = buildBinary(t, root)
	init := exec.Command(d.Binary, "init", "--config", d.Config, "--data-root", d.Data)
	init.Dir = root
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatalf("rencrow-harness init: %v\n%s", err, out)
	}
	return d
}

func configureVerificationFixture(t testing.TB, d *Deployment, cfg, registry map[string]any) {
	t.Helper()
	goExecutable, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("fixed verifier requires the Go executable: %v", err)
	}
	goExecutable, err = filepath.Abs(goExecutable)
	if err != nil {
		t.Fatalf("resolve fixed verifier executable: %v", err)
	}
	cacheRoot := filepath.Join(d.Secrets, "verification-cache")
	if err := os.MkdirAll(cacheRoot, 0o700); err != nil {
		t.Fatalf("create fixed verifier cache root: %v", err)
	}
	cfg["process_profiles"] = []any{map[string]any{
		"name": "go-test", "executable": goExecutable, "is_shell": false, "argv_prefix": []any{"test"},
	}}
	envProfiles := cfg["env_profiles"].([]any)
	envProfiles = append(envProfiles, map[string]any{
		"name": "criteria-test", "values": map[string]string{
			"GOCACHE": cacheRoot, "GOTMPDIR": cacheRoot, "TMPDIR": cacheRoot, "TEMP": cacheRoot, "TMP": cacheRoot,
		},
	})
	cfg["env_profiles"] = envProfiles

	policy := registry["policies"].([]any)[0].(map[string]any)
	policy["tools"] = append(policy["tools"].([]any), "process.exec")
	policy["process_profiles"] = []any{"go-test"}
	policy["env_profiles"] = []any{"clean", "criteria-test"}
	policy["verification"] = map[string]any{
		"format_version": "rencrow-verification-plan/v1", "process_profile_ref": "go-test", "executable": goExecutable,
		"argv": []any{"test", "./..."}, "cwd": ".", "env_profile_ref": "criteria-test",
		"timeout_seconds": 120, "pass_condition": "exit_zero",
	}
	writeFile(t, filepath.Join(d.Work, "go.mod"), "module rencrow.fixture/criteria\n\ngo 1.25.0\n")
	writeFile(t, filepath.Join(d.Work, "criteria_test.go"), "package criteria\n\nimport \"testing\"\n\nfunc TestFixedCriteriaFixture(t *testing.T) {}\n")
}

// CLIConfig writes a second configuration of the same deployment for the human
// CLI profile (principal user:ren, default origin human, able to read and control
// the sessions CORE owns) and returns its path. The CLI and CORE are two entrances
// to the same Service, store and binding; only the caller profile differs.
func (d *Deployment) CLIConfig(t testing.TB) string {
	t.Helper()
	cfg := cloneJSON(t, d.cfg)
	cfg["caller"] = map[string]any{
		"principal": "user:ren", "default_origin": "human",
		"readable_session_owners": []any{"user:ren", "core:local"}, "controllable_session_owners": []any{"user:ren", "core:local"},
		"relay_issuers": []any{},
	}
	path := filepath.Join(d.Secrets, "cli-config.json")
	writeJSON(t, path, cfg)
	return path
}

func cloneJSON(t testing.TB, value map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var out map[string]any
	if err := decoder.Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// BindingProfile is the profile name of the binding in the Harness configuration,
// which the CLI selects with --binding.
const BindingProfile = "shiro-exec"

func goTool() string {
	tool := filepath.Join(runtime.GOROOT(), "bin", "go")
	if runtime.GOOS == "windows" {
		tool += ".exe"
	}
	return tool
}

func goOutput(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(goTool(), args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(string(out))
}

// buildBinary builds the Harness binary with the pinned build revision. The
// build runs from the test's working directory so the module that CORE's build
// resolves for the Harness is the one that is built.
func buildBinary(t testing.TB, root string) string {
	t.Helper()
	name := "rencrow-harness"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(root, "bin", name)
	cmd := exec.Command(goTool(), "build",
		"-ldflags", "-X "+harnessPkg+"/internal/cli.BuildRevision="+BuildRevision,
		"-o", binary, harnessPkg+"/cmd/rencrow-harness")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build rencrow-harness: %v\n%s", err, out)
	}
	return binary
}

func readExample(t testing.TB, moduleDir, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(moduleDir, "testdata", "contract", "examples", name))
	if err != nil {
		t.Fatalf("read the Harness example %s: %v", name, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var out map[string]any
	if err := decoder.Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func writeJSON(t testing.TB, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(raw))
}

func writeFile(t testing.TB, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Reply is one scripted generation of the double: a final text, or one Tool
// call (Name and Args) that the Harness runs before it asks again.
type Reply struct {
	Text string
	Name string
	Args string
}

// Generation is what the double received for one generation request: the
// messages in the order and with the roles the Harness sent them.
type Generation struct {
	Messages []Message
}

// Message is one Chat message of a request.
type Message struct {
	Role    string
	Content string
}

// Gateway is the strict-contract double on a loopback port.
type Gateway struct {
	t       testing.TB
	srv     *httptest.Server
	binding protocol.Binding

	mu          sync.Mutex
	script      []Reply
	generations []Generation
	hold        <-chan struct{}
	measuring   chan struct{}
}

func newGateway(t testing.TB, binding protocol.Binding) *Gateway {
	g := &Gateway{t: t, binding: binding, script: []Reply{{Text: "done"}}, measuring: make(chan struct{}, 16)}
	g.srv = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.srv.Close)
	return g
}

// BaseURL is what the Harness configuration names.
func (g *Gateway) BaseURL() string { return g.srv.URL + "/v1" }

// SetScript sets the replies of the following generations in order; the last one
// answers every generation after it.
func (g *Gateway) SetScript(replies ...Reply) {
	g.mu.Lock()
	g.script = append([]Reply(nil), replies...)
	g.mu.Unlock()
}

// Hold makes every count wait until release is closed or the Harness gives up
// the request: a Run then stays in its measuring phase.
func (g *Gateway) Hold(release <-chan struct{}) {
	g.mu.Lock()
	g.hold = release
	g.mu.Unlock()
}

// Measuring is signalled each time a count request arrives.
func (g *Gateway) Measuring() <-chan struct{} { return g.measuring }

// Generations returns the generation requests received so far.
func (g *Gateway) Generations() []Generation {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]Generation(nil), g.generations...)
}

func (g *Gateway) serve(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1")
	switch {
	case r.Method == http.MethodGet && path == "/status":
		g.status(w)
	case r.Method == http.MethodPost && path == "/context/measure":
		g.measure(w, r)
	case r.Method == http.MethodPost && path == "/chat/completions":
		g.generate(w, r)
	default:
		http.NotFound(w, r)
	}
}

func writeJSONResponse(w http.ResponseWriter, status int, value any) {
	raw, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "unencodable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

func (g *Gateway) descriptor() map[string]any {
	options := map[string]any{"max_tokens": 4096, "temperature": nil, "top_p": nil, "seed": nil, "stop": []string{}}
	codes := []string{"REASONING_ONLY", "EMPTY_FINAL_CONTENT", "RAW_TOOL_MARKUP", "MODEL_OUTPUT_SCHEMA_INVALID", "CONNECT_FAILED", "UPSTREAM_TRANSIENT", "RATE_LIMITED", "QUEUE_TIMEOUT"}
	return map[string]any{
		"binding": map[string]any{
			"kind": g.binding.Kind, "selector": g.binding.Selector, "profile_revision": g.binding.ProfileRevision,
			"agent_id": g.binding.AgentID, "execution_role": g.binding.ExecutionRole,
		},
		"binding_fingerprint": fingerprint,
		"stage_options":       map[string]any{"act": options, "instruction_selection": options, "work_summary": options},
		"recovery_profiles": []any{map[string]any{
			"profile_id": "same_request", "profile_revision": "builtin-v1",
			"stages": []string{"act", "instruction_selection", "work_summary"}, "transformations": []string{}, "codes": codes,
		}},
		"measure": true, "normalization": "strict-v1",
	}
}

func (g *Gateway) status(w http.ResponseWriter) {
	writeJSONResponse(w, http.StatusOK, map[string]any{
		"status": "ok", "aliases": map[string]any{}, "model_routes": map[string]any{},
		"contracts": map[string]any{"harness-v1": map[string]any{
			"normalization": "strict-v1", "measure": true, "generation_retry": "disabled", "attempt_receipt": true, "explicit_recovery": true,
			"bindings": []any{g.descriptor()},
		}},
	})
}

func requestDigest(inputDigest string) string {
	sum := sha256.Sum256([]byte("fake-normalized-request/" + inputDigest + "/" + fingerprint))
	return hex.EncodeToString(sum[:])
}

// requestFacts reads from a Chat request the values the double needs.
type requestFacts struct {
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	MaxTokens int64 `json:"max_tokens"`
	Rencrow   struct {
		Harness struct {
			Stage    string `json:"stage"`
			Recovery struct {
				ProfileID       string `json:"profile_id"`
				ProfileRevision string `json:"profile_revision"`
			} `json:"recovery"`
		} `json:"harness"`
	} `json:"rencrow"`
}

func (g *Gateway) measure(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Request            json.RawMessage `json:"request"`
		SafetyMarginTokens int64           `json:"safety_margin_tokens"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "unreadable", http.StatusBadRequest)
		return
	}
	select {
	case g.measuring <- struct{}{}:
	default:
	}
	g.mu.Lock()
	hold := g.hold
	g.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-r.Context().Done():
			return
		}
	}
	input, err := protocol.InputDigest(body.Request)
	if err != nil {
		http.Error(w, "the request is not a Chat request", http.StatusBadRequest)
		return
	}
	var facts requestFacts
	if err := json.Unmarshal(body.Request, &facts); err != nil {
		http.Error(w, "unreadable", http.StatusBadRequest)
		return
	}
	size := int64(0)
	for _, message := range facts.Messages {
		size += int64(len(message.Content))
	}
	tokens := size/4 + 7
	writeJSONResponse(w, http.StatusOK, map[string]any{
		"contract_version": "harness-v1", "state": "verified_exact", "prompt_lower": tokens, "prompt_upper": tokens,
		"effective_context_limit": 32768, "reserved_output_tokens": facts.MaxTokens, "safety_margin_tokens": body.SafetyMarginTokens,
		"request_digest": requestDigest(input), "binding_fingerprint": fingerprint, "evidence_ref": "synthetic-double-not-tokenizer",
		"reason": nil, "input_digest": input,
	})
}

func (g *Gateway) generate(w http.ResponseWriter, r *http.Request) {
	var raw bytes.Buffer
	if _, err := raw.ReadFrom(r.Body); err != nil {
		http.Error(w, "unreadable", http.StatusBadRequest)
		return
	}
	input, err := protocol.InputDigest(raw.Bytes())
	if err != nil {
		http.Error(w, "the request is not a Chat request", http.StatusBadRequest)
		return
	}
	var facts requestFacts
	if err := json.Unmarshal(raw.Bytes(), &facts); err != nil {
		http.Error(w, "unreadable", http.StatusBadRequest)
		return
	}
	generation := Generation{}
	for _, message := range facts.Messages {
		generation.Messages = append(generation.Messages, Message{Role: message.Role, Content: message.Content})
	}
	g.mu.Lock()
	g.generations = append(g.generations, generation)
	reply := g.script[min(len(g.generations)-1, len(g.script)-1)]
	g.mu.Unlock()

	attempts := int64(1)
	receipt := map[string]any{
		"contract_version": "harness-v1", "stage": facts.Rencrow.Harness.Stage, "binding_fingerprint": fingerprint,
		"request_digest": requestDigest(input), "logical_requests": 1, "backend_attempts": attempts, "generation_state": "terminal",
		"hidden_retry": false, "recovery_profile": facts.Rencrow.Harness.Recovery.ProfileID,
		"recovery_profile_revision": facts.Rencrow.Harness.Recovery.ProfileRevision, "applied_transformations": []string{},
		"usage_complete": true, "input_digest": input,
	}
	var stream bytes.Buffer
	chunk := func(finish any, delta map[string]any) {
		raw, _ := json.Marshal(map[string]any{"id": "double-chat-1", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
		fmt.Fprintf(&stream, "data: %s\n\n", raw)
	}
	finish := "stop"
	if reply.Name != "" {
		chunk(nil, map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
			"index": 0, "id": "double-call-1", "type": "function", "function": map[string]any{"name": reply.Name, "arguments": reply.Args}}}})
		finish = "tool_calls"
	} else {
		chunk(nil, map[string]any{"role": "assistant", "content": reply.Text})
	}
	chunk(finish, map[string]any{})
	terminal, _ := json.Marshal(map[string]any{
		"contract_version": "harness-v1", "outcome": "completed", "finish_reason": finish, "code": nil,
		"provider_response_id": "double-chat-1", "usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 10, "cached_tokens": nil},
		"harness_receipt": receipt,
	})
	fmt.Fprintf(&stream, "event: rencrow.terminal\ndata: %s\n\ndata: [DONE]\n\n", terminal)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(stream.Bytes())
}
