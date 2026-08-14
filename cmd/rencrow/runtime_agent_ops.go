package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"unicode"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

const (
	agentOpsPath               = "/v1/agent/ops"
	agentOpsClient             = "RenCrow_CMD"
	agentOpsInteractionProfile = "agent-ops"
	agentOpsTaskChannel        = "agent_ops"
	agentOpsTaskChatID         = "agent-ops"
	agentOpsMaxMessageBytes    = 32 << 10
	agentOpsMaxBodyBytes       = 64 << 10
	agentOpsMaxRequestIDBytes  = 128
	agentOpsMinTokenBytes      = 32
)

var agentOpsRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
var agentOpsUserIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,255}$`)

// agentOpsExecutor is the narrow Shiro boundary used by the HTTP handler and
// its tests. Production wiring supplies the actual agents.Shiro instance.
type agentOpsExecutor interface {
	Execute(context.Context, task.Task) (string, error)
}

// agentOpsWorkerBusyNotifier is the narrow IdleChat coordination boundary
// used while an authenticated OPS request owns the foreground worker lease.
type agentOpsWorkerBusyNotifier interface {
	SetWorkerBusy(bool)
}

type agentOpsHandler struct {
	executor           agentOpsExecutor
	userID             string
	token              []byte
	workerBusyNotifier agentOpsWorkerBusyNotifier
	workerBusyMu       sync.Mutex
	workerBusyRefs     int
}

type agentOpsRequest struct {
	Message string `json:"message"`
}

type agentOpsResponse struct {
	RequestID string `json:"request_id"`
	JobID     string `json:"job_id"`
	AgentID   string `json:"agent_id"`
	Role      string `json:"role"`
	Route     string `json:"route"`
	Output    string `json:"output"`
}

type agentOpsErrorResponse struct {
	Error string `json:"error"`
}

// newAgentOpsHandler validates enabled startup configuration, reads the
// bearer token once, and returns nil when the ingress is disabled.
func newAgentOpsHandler(cfg *config.Config, executor agentOpsExecutor, workerBusyNotifier agentOpsWorkerBusyNotifier) (http.HandlerFunc, error) {
	if cfg == nil || !cfg.LocalAgentOps.Enabled {
		return nil, nil
	}
	if executor == nil {
		return nil, errors.New("local_agent_ops executor is unavailable")
	}
	userID := strings.TrimSpace(cfg.LocalAgentOps.UserID)
	if userID == "" || !agentOpsUserIDPattern.MatchString(userID) {
		return nil, errors.New("local_agent_ops.user_id is invalid")
	}
	token, err := readAgentOpsToken(cfg.LocalAgentOps.AuthTokenFile)
	if err != nil {
		return nil, err
	}
	handler := &agentOpsHandler{
		executor:           executor,
		userID:             userID,
		token:              append([]byte(nil), token...),
		workerBusyNotifier: workerBusyNotifier,
	}
	return handler.ServeHTTP, nil
}

func newConfiguredAgentOpsHandler(cfg *config.Config, executor agentOpsExecutor, workerBusyNotifier agentOpsWorkerBusyNotifier) (http.HandlerFunc, error) {
	handler, err := newAgentOpsHandler(cfg, executor, workerBusyNotifier)
	if err != nil || handler == nil {
		return handler, err
	}
	return localOnlyHandler(handler), nil
}

func readAgentOpsToken(path string) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("local_agent_ops.auth_token_file is empty")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("local_agent_ops token file is unavailable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("local_agent_ops token file must be a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("local_agent_ops token file must be owner-only")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("local_agent_ops token file cannot be read: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if len([]byte(token)) < agentOpsMinTokenBytes || token == "" || strings.IndexFunc(token, unicode.IsSpace) >= 0 {
		return nil, errors.New("local_agent_ops token file must contain exactly one non-whitespace token of at least 32 bytes")
	}
	return []byte(token), nil
}

// ServeHTTP handles one authenticated, fixed-scope OPS request.
func (h *agentOpsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeAgentOpsError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if !h.authorized(r.Header) {
		writeAgentOpsError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	client, clientOK := exactlyOneAgentOpsHeader(r.Header, "X-RenCrow-Client")
	profile, profileOK := exactlyOneAgentOpsHeader(r.Header, "X-RenCrow-Interaction-Profile")
	if !clientOK || !profileOK || client != agentOpsClient || profile != agentOpsInteractionProfile {
		writeAgentOpsError(w, http.StatusForbidden, "forbidden")
		return
	}
	requestID, requestIDOK := exactlyOneAgentOpsHeader(r.Header, "X-Request-ID")
	if !requestIDOK || !validAgentOpsRequestID(requestID) {
		writeAgentOpsError(w, http.StatusBadRequest, "invalid_request_id")
		return
	}
	if r.URL == nil || r.URL.RawQuery != "" {
		writeAgentOpsError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	contentTypes := r.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		writeAgentOpsError(w, http.StatusUnsupportedMediaType, "content_type_required")
		return
	}
	mediaType, _, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || mediaType != "application/json" {
		writeAgentOpsError(w, http.StatusUnsupportedMediaType, "content_type_required")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, agentOpsMaxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request agentOpsRequest
	if err := decoder.Decode(&request); err != nil {
		if isAgentOpsBodyTooLarge(err) {
			writeAgentOpsError(w, http.StatusRequestEntityTooLarge, "request_too_large")
		} else {
			writeAgentOpsError(w, http.StatusBadRequest, "invalid_request")
		}
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if isAgentOpsBodyTooLarge(err) {
			writeAgentOpsError(w, http.StatusRequestEntityTooLarge, "request_too_large")
		} else {
			writeAgentOpsError(w, http.StatusBadRequest, "invalid_request")
		}
		return
	}
	message := strings.TrimSpace(request.Message)
	if message == "" {
		writeAgentOpsError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if len([]byte(message)) > agentOpsMaxMessageBytes {
		writeAgentOpsError(w, http.StatusRequestEntityTooLarge, "request_too_large")
		return
	}

	parentScope, err := domaintool.NewToolExecutionScope(
		requestID,
		domaintool.ActorKindUser,
		h.userID,
		h.userID,
		[]string{domaintool.DataScopePublic, domaintool.DataScopeUser},
		domaintool.AuthenticationSourceHTTP,
	)
	if err != nil {
		writeAgentOpsError(w, http.StatusInternalServerError, "runtime_unavailable")
		return
	}
	parentContext := domaintool.WithToolExecutionScope(r.Context(), parentScope)
	jobID := task.NewJobID()
	shiroContext, err := domaintool.DeriveAgentToolExecutionScope(
		parentContext,
		requestID,
		"shiro",
		"worker",
		"ops",
		true,
	)
	if err != nil {
		writeAgentOpsError(w, http.StatusInternalServerError, "runtime_unavailable")
		return
	}
	opsTask := task.NewTask(jobID, message, agentOpsTaskChannel, agentOpsTaskChatID).WithRoute(routing.RouteOPS)
	releaseWorkerBusy := h.acquireWorkerBusyLease()
	defer releaseWorkerBusy()
	output, err := h.executor.Execute(shiroContext, opsTask)
	if err != nil || strings.TrimSpace(output) == "" {
		writeAgentOpsError(w, http.StatusInternalServerError, "execution_failed")
		return
	}

	writeJSONStatus(w, http.StatusOK, agentOpsResponse{
		RequestID: requestID,
		JobID:     jobID.String(),
		AgentID:   "shiro",
		Role:      "worker",
		Route:     routing.RouteOPS.String(),
		Output:    output,
	})
}

func (h *agentOpsHandler) acquireWorkerBusyLease() func() {
	if h.workerBusyNotifier == nil {
		return func() {}
	}

	h.workerBusyMu.Lock()
	if h.workerBusyRefs == 0 {
		h.workerBusyNotifier.SetWorkerBusy(true)
	}
	h.workerBusyRefs++
	h.workerBusyMu.Unlock()

	var releaseOnce sync.Once
	return func() {
		releaseOnce.Do(func() {
			h.workerBusyMu.Lock()
			defer h.workerBusyMu.Unlock()
			if h.workerBusyRefs <= 0 {
				return
			}
			h.workerBusyRefs--
			if h.workerBusyRefs == 0 {
				h.workerBusyNotifier.SetWorkerBusy(false)
			}
		})
	}
}

func (h *agentOpsHandler) authorized(headers http.Header) bool {
	values := headers.Values("Authorization")
	if len(values) != 1 {
		return false
	}
	scheme, presented, ok := strings.Cut(values[0], " ")
	if !ok || scheme != "Bearer" || presented == "" || strings.IndexFunc(presented, unicode.IsSpace) >= 0 {
		return false
	}
	return constantTimeAgentOpsTokenEqual(h.token, []byte(presented))
}

func exactlyOneAgentOpsHeader(headers http.Header, name string) (string, bool) {
	values := headers.Values(name)
	if len(values) != 1 {
		return "", false
	}
	return values[0], true
}

func constantTimeAgentOpsTokenEqual(expected, presented []byte) bool {
	expectedHash := sha256.Sum256(expected)
	presentedHash := sha256.Sum256(presented)
	return subtle.ConstantTimeCompare(expectedHash[:], presentedHash[:]) == 1
}

func validAgentOpsRequestID(requestID string) bool {
	if requestID == "" || len([]byte(requestID)) > agentOpsMaxRequestIDBytes {
		return false
	}
	return agentOpsRequestIDPattern.MatchString(requestID)
}

func isAgentOpsBodyTooLarge(err error) bool {
	var maxBytesError *http.MaxBytesError
	return errors.As(err, &maxBytesError)
}

func writeAgentOpsError(w http.ResponseWriter, status int, code string) {
	writeJSONStatus(w, status, agentOpsErrorResponse{Error: code})
}
