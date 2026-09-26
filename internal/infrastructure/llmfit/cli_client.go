package llmfit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	domainllmops "github.com/Nyukimin/RenCrow_CORE/internal/domain/llmops"
)

const (
	defaultCLITimeout  = 5 * time.Second
	defaultCLITopLimit = 20
	cliWaitDelay       = 2 * time.Second
	stderrExcerptLimit = 160
)

// CLIProvider runs the llmfit executable on the same host:
//
//	llmfit system --json
//	llmfit recommend --json --limit N
//
// The CLI JSON schema is not documented in API.md. Observed on llmfit v1.1.15:
// `llmfit system --json` returns {"providers":{...},"system":{...}} (same
// "system" block as /api/v1/system, no "node" block; "providers" is ignored),
// see testdata/system_cli.json. `recommend --json` is assumed to match
// /api/v1/models/top; HTTP mode is the validated path, so verify recommend
// output against a real llmfit build before relying on cli mode in production.
//
// Filters other than Limit (use_case, runtime, min_fit, max_context, sort) are
// accepted and validated but not forwarded, because the CLI flag names for them
// are undocumented; forwarding guessed flags could fail the whole command.
//
// Arguments are fixed arrays; no user-controlled string is passed through and
// no shell is involved. stderr is logged and only a short excerpt reaches the
// error message.
type CLIProvider struct {
	nodeID     string
	executable string
	timeout    time.Duration
	// Now stamps CollectedAt on returned observations. Injectable for tests.
	Now func() time.Time
}

// NewCLIProvider creates a provider that executes executable (name resolved via
// PATH, or an absolute path). A non-positive timeout falls back to 5s.
func NewCLIProvider(nodeID, executable string, timeout time.Duration) *CLIProvider {
	if timeout <= 0 {
		timeout = defaultCLITimeout
	}
	executable = strings.TrimSpace(executable)
	if executable == "" {
		executable = "llmfit"
	}
	return &CLIProvider{
		nodeID:     strings.TrimSpace(nodeID),
		executable: executable,
		timeout:    timeout,
		Now:        time.Now,
	}
}

// Health implements CapabilityProvider. For the CLI it only verifies that the
// executable can be resolved; it does not run llmfit.
func (p *CLIProvider) Health(ctx context.Context) error {
	_, err := p.lookPath("health")
	return err
}

// System implements CapabilityProvider.
func (p *CLIProvider) System(ctx context.Context) (*domainllmops.NodeHardwareProfile, error) {
	const op = "system"
	stdout, err := p.run(ctx, op, []string{"system", "--json"})
	if err != nil {
		return nil, err
	}
	resp, err := decodeSystem(bytes.NewReader(stdout))
	if err != nil {
		return nil, newProviderError(ErrorKindInvalidResponse, p.nodeID, op, "invalid system JSON: "+err.Error(), err)
	}
	profile := mapSystem(p.nodeID, resp, p.now())
	if profile.OS == "" {
		// The CLI output has no "node" block. The command runs on this host,
		// so the OS is known without asking llmfit.
		profile.OS = runtime.GOOS
	}
	return &profile, nil
}

// TopModels implements CapabilityProvider.
func (p *CLIProvider) TopModels(ctx context.Context, query ModelFitQuery) ([]domainllmops.ModelFitAssessment, error) {
	const op = "recommend"
	q, err := validateQuery(p.nodeID, op, query)
	if err != nil {
		return nil, err
	}
	return p.recommend(ctx, op, q.Limit)
}

// SearchModels implements CapabilityProvider. The CLI has no documented search
// flag, so the recommend output is filtered by a case-insensitive substring
// match on the model name.
func (p *CLIProvider) SearchModels(ctx context.Context, query ModelFitQuery) ([]domainllmops.ModelFitAssessment, error) {
	const op = "recommend"
	q, err := validateQuery(p.nodeID, op, query)
	if err != nil {
		return nil, err
	}
	// Ask for a generous list so the keyword filter has something to match.
	models, err := p.recommend(ctx, op, 0)
	if err != nil {
		return nil, err
	}
	keyword := strings.ToLower(q.Keyword)
	if keyword == "" {
		return models, nil
	}
	filtered := make([]domainllmops.ModelFitAssessment, 0, len(models))
	for _, model := range models {
		if strings.Contains(strings.ToLower(model.ModelID), keyword) {
			filtered = append(filtered, model)
		}
	}
	if q.Limit > 0 && len(filtered) > q.Limit {
		filtered = filtered[:q.Limit]
	}
	return filtered, nil
}

func (p *CLIProvider) recommend(ctx context.Context, op string, limit int) ([]domainllmops.ModelFitAssessment, error) {
	if limit <= 0 {
		limit = defaultCLITopLimit
	}
	stdout, err := p.run(ctx, op, []string{"recommend", "--json", "--limit", strconv.Itoa(limit)})
	if err != nil {
		return nil, err
	}
	resp, err := decodeModels(bytes.NewReader(stdout))
	if err != nil {
		return nil, newProviderError(ErrorKindInvalidResponse, p.nodeID, op, "invalid recommend JSON: "+err.Error(), err)
	}
	models, skipped := mapModels(p.nodeID, resp, p.now())
	if skipped > 0 {
		log.Printf("[llmfit] node=%s op=%s skipped %d model entries without a name", p.nodeID, op, skipped)
	}
	return models, nil
}

func (p *CLIProvider) lookPath(op string) (string, error) {
	path, err := exec.LookPath(p.executable)
	if err != nil {
		return "", newProviderError(ErrorKindExecutable, p.nodeID, op, fmt.Sprintf("executable %q not found", p.executable), err)
	}
	return path, nil
}

// run executes the command without a shell, bounded by the provider timeout,
// and returns stdout only. stderr is logged; a one-line excerpt is placed in
// the error message on failure.
func (p *CLIProvider) run(ctx context.Context, op string, args []string) ([]byte, error) {
	path, err := p.lookPath(op)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, path, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = cliWaitDelay

	runErr := cmd.Run()
	if stderr.Len() > 0 {
		log.Printf("[llmfit] node=%s op=%s stderr: %s", p.nodeID, op, truncate(strings.TrimSpace(stderr.String()), 1024))
	}
	if runErr == nil {
		return stdout.Bytes(), nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, newProviderError(ErrorKindUnreachable, p.nodeID, op, fmt.Sprintf("command timed out after %s", p.timeout), runErr)
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		message := fmt.Sprintf("exit status %d", exitErr.ExitCode())
		if excerpt := stderrExcerpt(stderr.String()); excerpt != "" {
			message += ": " + excerpt
		}
		return nil, newProviderError(ErrorKindServerError, p.nodeID, op, message, runErr)
	}
	return nil, newProviderError(ErrorKindUnreachable, p.nodeID, op, "command failed: "+runErr.Error(), runErr)
}

func stderrExcerpt(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return truncate(strings.TrimSpace(s), stderrExcerptLimit)
}

func (p *CLIProvider) now() time.Time {
	if p.Now == nil {
		return time.Now()
	}
	return p.Now()
}
