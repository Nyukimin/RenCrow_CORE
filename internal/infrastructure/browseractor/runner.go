package browseractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	modulebrowser "github.com/Nyukimin/picoclaw_multiLLM/modules/browseractor"
)

type Config struct {
	Enabled         bool
	RunnerPath      string
	NodeBinary      string
	Browser         string
	HeadlessDefault bool
	ProfileRoot     string
	ArtifactRoot    string
	TimeoutMS       int
	MaxActions      int
	NetworkScope    string
	AllowedOrigins  []string
	SaveTrace       bool
	SaveScreenshot  bool
	MaskSecrets     bool
}

type CommandRunner func(ctx context.Context, command string, args []string, stdin []byte) (stdout []byte, stderr []byte, exitCode int, err error)

type Runner struct {
	cfg           Config
	commandRunner CommandRunner
}

func NewRunner(cfg Config) *Runner {
	return &Runner{cfg: cfg.withDefaults(), commandRunner: execCommand}
}

func (r *Runner) WithCommandRunner(commandRunner CommandRunner) *Runner {
	if commandRunner == nil {
		r.commandRunner = execCommand
	} else {
		r.commandRunner = commandRunner
	}
	return r
}

func (r *Runner) Run(ctx context.Context, req modulebrowser.RunRequest) (modulebrowser.RunResponse, error) {
	cfg := r.cfg.withDefaults()
	req = applyDefaults(req, cfg)
	if err := modulebrowser.ValidateRunRequest(req); err != nil {
		return modulebrowser.RunResponse{SchemaVersion: modulebrowser.SchemaVersion, RunID: req.RunID, Status: modulebrowser.StatusFailed, Error: &modulebrowser.Error{Code: modulebrowser.ErrValidationFailed, Message: err.Error()}}, nil
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return modulebrowser.RunResponse{}, err
	}
	stdout, stderr, exitCode, err := r.runCommand(ctx, cfg.NodeBinary, []string{cfg.RunnerPath, "run", "--json"}, payload)
	if err != nil && len(stdout) == 0 {
		return modulebrowser.RunResponse{}, fmt.Errorf("browser actor sidecar failed: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	var resp modulebrowser.RunResponse
	if decErr := json.Unmarshal(stdout, &resp); decErr != nil {
		return modulebrowser.RunResponse{}, fmt.Errorf("browser actor sidecar returned invalid JSON (exit=%d stderr=%s): %w", exitCode, strings.TrimSpace(string(stderr)), decErr)
	}
	if resp.SchemaVersion == "" {
		resp.SchemaVersion = modulebrowser.SchemaVersion
	}
	return resp, nil
}

func (r *Runner) Doctor(ctx context.Context) (modulebrowser.DoctorResponse, error) {
	cfg := r.cfg.withDefaults()
	stdout, stderr, exitCode, err := r.runCommand(ctx, cfg.NodeBinary, []string{cfg.RunnerPath, "doctor", "--json"}, nil)
	if err != nil && len(stdout) == 0 {
		return modulebrowser.DoctorResponse{}, fmt.Errorf("browser actor doctor failed: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	var resp modulebrowser.DoctorResponse
	if decErr := json.Unmarshal(stdout, &resp); decErr != nil {
		return modulebrowser.DoctorResponse{}, fmt.Errorf("browser actor doctor returned invalid JSON (exit=%d stderr=%s): %w", exitCode, strings.TrimSpace(string(stderr)), decErr)
	}
	return resp, nil
}

func (r *Runner) runCommand(ctx context.Context, command string, args []string, stdin []byte) ([]byte, []byte, int, error) {
	runner := r.commandRunner
	if runner == nil {
		runner = execCommand
	}
	return runner(ctx, command, args, stdin)
}

func (cfg Config) withDefaults() Config {
	if strings.TrimSpace(cfg.RunnerPath) == "" {
		cfg.RunnerPath = "/home/nyukimi/RenCrow/RenCrow_Tools/tools/browser_actor/run_browser_actor.mjs"
	}
	if strings.TrimSpace(cfg.NodeBinary) == "" {
		cfg.NodeBinary = "node"
	}
	if strings.TrimSpace(cfg.Browser) == "" {
		cfg.Browser = "chromium"
	}
	if strings.TrimSpace(cfg.ProfileRoot) == "" {
		cfg.ProfileRoot = "workspace/browser_profiles"
	}
	if strings.TrimSpace(cfg.ArtifactRoot) == "" {
		cfg.ArtifactRoot = "workspace/browser_runs"
	}
	if cfg.TimeoutMS <= 0 {
		cfg.TimeoutMS = 30000
	}
	if cfg.MaxActions <= 0 {
		cfg.MaxActions = 30
	}
	if strings.TrimSpace(cfg.NetworkScope) == "" {
		cfg.NetworkScope = "allowlist"
	}
	if len(cfg.AllowedOrigins) == 0 {
		cfg.AllowedOrigins = []string{"http://127.0.0.1:18790", "http://localhost:18790", "file://"}
	}
	return cfg
}

func applyDefaults(req modulebrowser.RunRequest, cfg Config) modulebrowser.RunRequest {
	req = modulebrowser.NormalizeRunRequest(req)
	if req.TimeoutMS == 0 {
		req.TimeoutMS = cfg.TimeoutMS
	}
	if req.MaxActions == 0 {
		req.MaxActions = cfg.MaxActions
	}
	if len(req.AllowedOrigins) == 0 {
		req.AllowedOrigins = append([]string(nil), cfg.AllowedOrigins...)
	}
	if strings.TrimSpace(req.StorageStatePath) == "" && strings.TrimSpace(req.ProfileID) != "" {
		req.StorageStatePath = filepath.Join(cfg.ProfileRoot, req.ProfileID, "storage_state.json")
	}
	if strings.TrimSpace(req.ArtifactDir) == "" && strings.TrimSpace(req.RunID) != "" {
		req.ArtifactDir = strings.TrimRight(cfg.ArtifactRoot, "/") + "/" + req.RunID
	}
	return req
}

func execCommand(ctx context.Context, command string, args []string, stdin []byte) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	if deadline, ok := ctx.Deadline(); ok {
		_ = deadline
	} else {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		cmd = exec.CommandContext(ctx, command, args...)
	}
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 127
		}
	}
	return stdout.Bytes(), stderr.Bytes(), exitCode, err
}
