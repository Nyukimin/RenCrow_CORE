package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"
)

// LLMOpsProxyOptions holds MLX management daemon connection for server-side proxying.
// Token must come from LLM_OPS_TOKEN (never send to the browser).
type LLMOpsProxyOptions struct {
	BaseURL string
	Token   string
}

const llmOpsProxyTimeout = 650 * time.Second
const llmOpsReadProxyTimeout = 3 * time.Second

func (o LLMOpsProxyOptions) ready() bool {
	return strings.TrimSpace(o.BaseURL) != "" && strings.TrimSpace(o.Token) != ""
}

func normalizeLLMOpsBase(u string) string {
	return strings.TrimRight(strings.TrimSpace(u), "/")
}

// LLMOpsIdleChatGate prepares model runtime for IdleChat.
type LLMOpsIdleChatGate struct {
	opts   LLMOpsProxyOptions
	client *http.Client
}

// LLMOpsIdleChatBusyError means Kuro/Heavy or Midori/Wild is still running.
type LLMOpsIdleChatBusyError struct {
	Roles []string
}

func (e *LLMOpsIdleChatBusyError) Error() string {
	if e == nil || len(e.Roles) == 0 {
		return "idlechat blocked: Heavy/Wild is running"
	}
	return "idlechat blocked: " + strings.Join(e.Roles, ", ") + " is running"
}

// NewLLMOpsIdleChatGate creates an IdleChat start gate backed by llm-ops.
func NewLLMOpsIdleChatGate(opts LLMOpsProxyOptions) *LLMOpsIdleChatGate {
	return &LLMOpsIdleChatGate{
		opts: opts,
	}
}

// PrepareIdleChatStart blocks when Heavy/Wild are active; otherwise it halts them and starts Worker.
func (g *LLMOpsIdleChatGate) PrepareIdleChatStart(ctx context.Context) error {
	if g == nil || !g.opts.ready() {
		return nil
	}
	status, err := g.fetchStatus(ctx)
	if err != nil {
		return err
	}
	busy := llmOpsIdleBusyRoles(status)
	if len(busy) > 0 {
		return &LLMOpsIdleChatBusyError{Roles: busy}
	}
	if err := g.postJSON(ctx, "/v1/control/stop", []byte(`{"roles":["Heavy","Wild"]}`)); err != nil {
		return err
	}
	if err := g.postJSON(ctx, "/v1/control/start", []byte(`{"selection":"Worker"}`)); err != nil {
		return err
	}
	return nil
}

type llmOpsStatusSnapshot struct {
	Roles  map[string]llmOpsRoleState `json:"roles"`
	Memory struct {
		LLMByRole map[string]llmOpsMemoryRole `json:"llm_by_role"`
	} `json:"memory"`
}

type llmOpsRoleState struct {
	HealthOK bool `json:"health_ok"`
	Halted   bool `json:"halted"`
}

type llmOpsMemoryRole struct {
	PID int `json:"pid"`
}

func (g *LLMOpsIdleChatGate) fetchStatus(ctx context.Context) (llmOpsStatusSnapshot, error) {
	var status llmOpsStatusSnapshot
	body, err := g.do(ctx, http.MethodGet, "/v1/status", nil)
	if err != nil {
		return status, err
	}
	if err := json.Unmarshal(body, &status); err != nil {
		return status, fmt.Errorf("llm-ops status decode: %w", err)
	}
	return status, nil
}

func (g *LLMOpsIdleChatGate) postJSON(ctx context.Context, path string, body []byte) error {
	_, err := g.do(ctx, http.MethodPost, path, body)
	return err
}

func (g *LLMOpsIdleChatGate) do(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	base := normalizeLLMOpsBase(g.opts.BaseURL)
	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, reqBody)
	if err != nil {
		return nil, fmt.Errorf("llm-ops request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(g.opts.Token))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := g.client
	if client == nil {
		client = &http.Client{Timeout: llmOpsProxyTimeoutFor(method, path)}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llm-ops %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("llm-ops %s %s failed: status=%d body=%s", method, path, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if readErr != nil {
		return nil, fmt.Errorf("llm-ops response read: %w", readErr)
	}
	return respBody, nil
}

func llmOpsIdleBusyRoles(status llmOpsStatusSnapshot) []string {
	var busy []string
	for _, role := range []string{"Heavy", "Wild"} {
		state := status.Roles[role]
		mem := status.Memory.LLMByRole[role]
		if mem.PID > 0 || (!state.Halted && state.HealthOK) {
			busy = append(busy, role)
		}
	}
	sort.Strings(busy)
	return busy
}

// HandleLLMOpsHealth proxies GET /health to the MLX management API.
func HandleLLMOpsHealth(opts LLMOpsProxyOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if strings.TrimSpace(opts.BaseURL) == "" {
			http.Error(w, "llm ops proxy not configured", http.StatusServiceUnavailable)
			return
		}
		proxyLLMOps(w, r, opts, http.MethodGet, "/health", nil)
	}
}

// HandleLLMOpsStatus proxies GET /v1/status to the MLX management API.
func HandleLLMOpsStatus(opts LLMOpsProxyOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !opts.ready() {
			http.Error(w, "llm ops proxy not configured", http.StatusServiceUnavailable)
			return
		}
		proxyLLMOps(w, r, opts, http.MethodGet, "/v1/status", nil)
	}
}

// HandleLLMOpsStart proxies POST /v1/control/start. Empty body defaults to Worker selection.
func HandleLLMOpsStart(opts LLMOpsProxyOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !opts.ready() {
			http.Error(w, "llm ops proxy not configured", http.StatusServiceUnavailable)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		if len(bytes.TrimSpace(body)) == 0 {
			body = []byte(`{"selection":"Worker"}`)
		}
		proxyLLMOps(w, r, opts, http.MethodPost, "/v1/control/start", body)
	}
}

// HandleLLMOpsStop proxies POST /v1/control/stop. Empty body defaults to Chat+Worker.
func HandleLLMOpsStop(opts LLMOpsProxyOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !opts.ready() {
			http.Error(w, "llm ops proxy not configured", http.StatusServiceUnavailable)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		if len(bytes.TrimSpace(body)) == 0 {
			body = []byte(`{"roles":["Chat","Worker"]}`)
		}
		proxyLLMOps(w, r, opts, http.MethodPost, "/v1/control/stop", body)
	}
}

// HandleLLMOpsRestart proxies POST /v1/control/restart. Empty body defaults to all roles.
func HandleLLMOpsRestart(opts LLMOpsProxyOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !opts.ready() {
			http.Error(w, "llm ops proxy not configured", http.StatusServiceUnavailable)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		if len(bytes.TrimSpace(body)) == 0 {
			body = []byte(`{"roles":"all"}`)
		}
		proxyLLMOps(w, r, opts, http.MethodPost, "/v1/control/restart", body)
	}
}

func proxyLLMOps(w http.ResponseWriter, r *http.Request, opts LLMOpsProxyOptions, method, path string, body []byte) {
	base := normalizeLLMOpsBase(opts.BaseURL)
	target := base + path
	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	upReq, err := http.NewRequestWithContext(r.Context(), method, target, reqBody)
	if err != nil {
		http.Error(w, "bad upstream request", http.StatusInternalServerError)
		return
	}
	upReq.Header.Set("Authorization", "Bearer "+strings.TrimSpace(opts.Token))
	if body != nil {
		upReq.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: llmOpsProxyTimeoutFor(method, path)}
	resp, err := client.Do(upReq)
	if err != nil {
		log.Printf("[viewer] llm-ops %s %s: %v", method, path, err)
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	} else if resp.StatusCode != http.StatusNoContent {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		log.Printf("[viewer] llm-ops response copy: %v", err)
	}
}

func llmOpsProxyTimeoutFor(method, path string) time.Duration {
	if method == http.MethodGet && (path == "/health" || path == "/v1/status") {
		return llmOpsReadProxyTimeout
	}
	return llmOpsProxyTimeout
}
