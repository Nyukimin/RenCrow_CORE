package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/tool"
)

const (
	defaultCodexSandbox        = "read-only"
	defaultCodexTimeout        = 10 * time.Minute
	defaultCodexMaxPromptBytes = 64 * 1024
	defaultCodexMaxOutputBytes = 1024 * 1024
)

type CodexRunner interface {
	Run(ctx context.Context, req CodexRunRequest) (CodexRunResponse, error)
}

type CodexRunRequest struct {
	Prompt     string `json:"prompt"`
	WorkingDir string `json:"working_dir,omitempty"`
	Sandbox    string `json:"sandbox"`
	Model      string `json:"model,omitempty"`
	TimeoutMS  int    `json:"timeout_ms,omitempty"`
	Ephemeral  bool   `json:"ephemeral"`
}

type CodexRunResponse struct {
	Status       string         `json:"status"`
	FinalText    string         `json:"final_text,omitempty"`
	WorkingDir   string         `json:"working_dir,omitempty"`
	Sandbox      string         `json:"sandbox"`
	Model        string         `json:"model,omitempty"`
	ExitCode     int            `json:"exit_code"`
	EventCounts  map[string]int `json:"event_counts,omitempty"`
	Usage        map[string]any `json:"usage,omitempty"`
	StdoutTail   string         `json:"stdout_tail,omitempty"`
	StderrTail   string         `json:"stderr_tail,omitempty"`
	DurationMS   int64          `json:"duration_ms"`
	TimedOut     bool           `json:"timed_out,omitempty"`
	ErrorMessage string         `json:"error_message,omitempty"`
}

type CodexExecRunner struct {
	Command        string
	WorkingDir     string
	Sandbox        string
	Model          string
	Timeout        time.Duration
	MaxPromptBytes int
	MaxOutputBytes int
	Ephemeral      bool
}

func NewCodexExecRunner(command, workingDir, sandbox, model string, timeout time.Duration, maxPromptBytes, maxOutputBytes int, ephemeral bool) *CodexExecRunner {
	if strings.TrimSpace(command) == "" {
		command = "codex"
	}
	if sandbox == "" {
		sandbox = defaultCodexSandbox
	}
	if timeout <= 0 {
		timeout = defaultCodexTimeout
	}
	if maxPromptBytes <= 0 {
		maxPromptBytes = defaultCodexMaxPromptBytes
	}
	if maxOutputBytes <= 0 {
		maxOutputBytes = defaultCodexMaxOutputBytes
	}
	return &CodexExecRunner{
		Command:        command,
		WorkingDir:     strings.TrimSpace(workingDir),
		Sandbox:        sandbox,
		Model:          strings.TrimSpace(model),
		Timeout:        timeout,
		MaxPromptBytes: maxPromptBytes,
		MaxOutputBytes: maxOutputBytes,
		Ephemeral:      ephemeral,
	}
}

func (r *CodexExecRunner) Run(ctx context.Context, req CodexRunRequest) (CodexRunResponse, error) {
	started := time.Now()
	if err := validateCodexRunRequest(req, r.MaxPromptBytes); err != nil {
		return CodexRunResponse{}, err
	}
	workingDir := firstNonEmpty(req.WorkingDir, r.WorkingDir)
	sandbox := firstNonEmpty(req.Sandbox, r.Sandbox)
	model := firstNonEmpty(req.Model, r.Model)
	timeout := r.Timeout
	if req.TimeoutMS > 0 {
		timeout = time.Duration(req.TimeoutMS) * time.Millisecond
	}
	if err := validateCodexSandbox(sandbox); err != nil {
		return CodexRunResponse{}, err
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"exec", "--json", "--sandbox", sandbox}
	if workingDir != "" {
		args = append(args, "--cd", workingDir)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if req.Ephemeral || r.Ephemeral {
		args = append(args, "--ephemeral")
	}
	args = append(args, req.Prompt)

	cmd := exec.CommandContext(runCtx, r.Command, args...)
	stdout := &limitedBuffer{limit: r.MaxOutputBytes}
	stderr := &limitedBuffer{limit: r.MaxOutputBytes / 4}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if workingDir != "" {
		cmd.Dir = workingDir
	}

	err := cmd.Run()
	timedOut := errors.Is(runCtx.Err(), context.DeadlineExceeded)
	parsed := parseCodexJSONL(stdout.Bytes())
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	resp := CodexRunResponse{
		Status:       "completed",
		FinalText:    parsed.FinalText,
		WorkingDir:   workingDir,
		Sandbox:      sandbox,
		Model:        model,
		ExitCode:     exitCode,
		EventCounts:  parsed.EventCounts,
		Usage:        parsed.Usage,
		StdoutTail:   tailString(stdout.String(), 4000),
		StderrTail:   tailString(stderr.String(), 4000),
		DurationMS:   time.Since(started).Milliseconds(),
		TimedOut:     timedOut,
		ErrorMessage: "",
	}
	if err != nil {
		resp.Status = "failed"
		resp.ErrorMessage = err.Error()
		if timedOut {
			resp.Status = "timeout"
		}
		return resp, err
	}
	return resp, nil
}

func (r *ToolRunner) executeCodexRunV2(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
	if r.config.CodexRunner == nil {
		return tool.NewError(tool.ErrInternalError, "codex.run is not configured", nil), nil
	}
	req, err := codexRunRequestFromArgs(args)
	if err != nil {
		return tool.NewError(tool.ErrValidationFailed, err.Error(), nil), nil
	}
	resp, err := r.config.CodexRunner.Run(ctx, req)
	if err != nil {
		code := tool.ErrInternalError
		if resp.TimedOut {
			code = tool.ErrTimeout
		}
		if strings.Contains(err.Error(), "sandbox") || strings.Contains(err.Error(), "prompt") {
			code = tool.ErrValidationFailed
		}
		return tool.NewError(code, err.Error(), map[string]any{
			"status":      resp.Status,
			"exit_code":   resp.ExitCode,
			"stderr_tail": resp.StderrTail,
			"duration_ms": resp.DurationMS,
		}), nil
	}
	return tool.NewSuccess(resp), nil
}

func codexRunRequestFromArgs(args map[string]any) (CodexRunRequest, error) {
	req := CodexRunRequest{
		Sandbox: defaultCodexSandbox,
	}
	if value, ok := args["prompt"].(string); ok {
		req.Prompt = strings.TrimSpace(value)
	}
	if value, ok := args["working_dir"].(string); ok {
		req.WorkingDir = strings.TrimSpace(value)
	}
	if value, ok := args["sandbox"].(string); ok && strings.TrimSpace(value) != "" {
		req.Sandbox = strings.TrimSpace(value)
	}
	if value, ok := args["model"].(string); ok {
		req.Model = strings.TrimSpace(value)
	}
	if value, ok := int64Arg(args["timeout_ms"]); ok {
		req.TimeoutMS = int(value)
	}
	if value, ok := args["ephemeral"].(bool); ok {
		req.Ephemeral = value
	}
	if err := validateCodexRunRequest(req, defaultCodexMaxPromptBytes); err != nil {
		return req, err
	}
	if err := validateCodexSandbox(req.Sandbox); err != nil {
		return req, err
	}
	return req, nil
}

func validateCodexRunRequest(req CodexRunRequest, maxPromptBytes int) error {
	if strings.TrimSpace(req.Prompt) == "" {
		return fmt.Errorf("'prompt' is required")
	}
	if maxPromptBytes <= 0 {
		maxPromptBytes = defaultCodexMaxPromptBytes
	}
	if len([]byte(req.Prompt)) > maxPromptBytes {
		return fmt.Errorf("prompt exceeds max size: %d bytes", maxPromptBytes)
	}
	if req.TimeoutMS < 0 {
		return fmt.Errorf("timeout_ms must be >= 0")
	}
	if req.TimeoutMS > 3600000 {
		return fmt.Errorf("timeout_ms must be <= 3600000")
	}
	return nil
}

func validateCodexSandbox(sandbox string) error {
	switch sandbox {
	case "read-only", "workspace-write":
		return nil
	case "danger-full-access":
		return fmt.Errorf("sandbox danger-full-access is not allowed for codex.run")
	default:
		return fmt.Errorf("sandbox must be one of [read-only, workspace-write]")
	}
}

type codexJSONLParseResult struct {
	FinalText   string
	EventCounts map[string]int
	Usage       map[string]any
}

func parseCodexJSONL(data []byte) codexJSONLParseResult {
	result := codexJSONLParseResult{EventCounts: map[string]int{}}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		var event map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		eventType, _ := event["type"].(string)
		if eventType != "" {
			result.EventCounts[eventType]++
		}
		if eventType == "turn.completed" {
			if usage, ok := event["usage"].(map[string]any); ok {
				result.Usage = usage
			}
		}
		if eventType != "item.completed" {
			continue
		}
		item, _ := event["item"].(map[string]any)
		itemType, _ := item["type"].(string)
		if itemType == "agent_message" {
			if text, ok := item["text"].(string); ok {
				result.FinalText = text
			}
		}
	}
	return result
}

type limitedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	originalLen := len(p)
	if b.limit <= 0 {
		return originalLen, nil
	}
	if len(p) >= b.limit {
		b.buf.Reset()
		_, _ = b.buf.Write(p[len(p)-b.limit:])
		return originalLen, nil
	}
	overflow := b.buf.Len() + len(p) - b.limit
	if overflow > 0 {
		current := b.buf.Bytes()
		keep := append([]byte(nil), current[overflow:]...)
		b.buf.Reset()
		_, _ = b.buf.Write(keep)
	}
	_, _ = b.buf.Write(p)
	return originalLen, nil
}

func (b *limitedBuffer) Bytes() []byte {
	return b.buf.Bytes()
}

func (b *limitedBuffer) String() string {
	return b.buf.String()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func tailString(value string, max int) string {
	if max <= 0 || len(value) <= max {
		return value
	}
	return value[len(value)-max:]
}
