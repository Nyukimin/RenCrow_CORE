package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	domainsandbox "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/sandbox"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/tool"
)

type PostApplyToolRunner interface {
	ExecuteV2(ctx context.Context, toolName string, args map[string]any) (*tool.ToolResponse, error)
}

type PostApplyVerificationResult struct {
	Command    string `json:"command,omitempty"`
	OutputPath string `json:"output_path,omitempty"`
	Status     string `json:"status"`
	Output     string `json:"output,omitempty"`
}

type PostApplyVerificationRunner struct {
	runner      PostApplyToolRunner
	sandboxRoot string
}

func NewPostApplyVerificationRunner(runner PostApplyToolRunner, sandboxRoot string) *PostApplyVerificationRunner {
	return &PostApplyVerificationRunner{
		runner:      runner,
		sandboxRoot: sandboxRoot,
	}
}

func (r *PostApplyVerificationRunner) RunPostApplyVerification(ctx context.Context, req domainsandbox.PromotionApplyRequest) (PostApplyVerificationResult, error) {
	command := strings.TrimSpace(req.PostApplyVerificationCommand)
	if command == "" {
		return PostApplyVerificationResult{}, nil
	}
	if r == nil || r.runner == nil {
		return PostApplyVerificationResult{}, fmt.Errorf("post-apply verification runner unavailable")
	}
	outputPath, err := resolveSandboxOutputPath(r.sandboxRoot, req.PostApplyVerificationPath)
	if err != nil {
		return PostApplyVerificationResult{}, err
	}
	resp, err := r.runner.ExecuteV2(ctx, "shell", map[string]any{"command": command})
	if err != nil {
		return PostApplyVerificationResult{}, fmt.Errorf("post-apply verification command failed: %w", err)
	}
	if resp == nil {
		return PostApplyVerificationResult{}, fmt.Errorf("post-apply verification command returned empty response")
	}
	if resp.IsError() {
		return PostApplyVerificationResult{}, fmt.Errorf("post-apply verification command rejected: %s", resp.Error.Error())
	}
	output := resp.String()
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return PostApplyVerificationResult{}, fmt.Errorf("create post-apply verification directory: %w", err)
	}
	if err := os.WriteFile(outputPath, []byte(formatPostApplyVerification(command, output)), 0o644); err != nil {
		return PostApplyVerificationResult{}, fmt.Errorf("write post-apply verification output: %w", err)
	}
	return PostApplyVerificationResult{
		Command:    command,
		OutputPath: outputPath,
		Status:     "completed",
		Output:     output,
	}, nil
}

func resolveSandboxOutputPath(sandboxRoot string, outputPath string) (string, error) {
	if strings.TrimSpace(sandboxRoot) == "" {
		return "", fmt.Errorf("sandbox root is required for post-apply verification output")
	}
	if strings.TrimSpace(outputPath) == "" {
		return "", fmt.Errorf("post_apply_verification_path is required")
	}
	rootAbs, err := filepath.Abs(filepath.Clean(sandboxRoot))
	if err != nil {
		return "", fmt.Errorf("resolve sandbox root: %w", err)
	}
	target := filepath.Clean(outputPath)
	if !filepath.IsAbs(target) {
		target = filepath.Join(rootAbs, target)
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("resolve post_apply_verification_path: %w", err)
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil {
		return "", fmt.Errorf("check post_apply_verification_path: %w", err)
	}
	if rel == "." || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("post_apply_verification_path must be inside sandbox root")
	}
	base := filepath.Base(targetAbs)
	if base == ".env" || strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") {
		return "", fmt.Errorf("post_apply_verification_path is denied by sandbox secret guard")
	}
	for _, part := range strings.Split(targetAbs, string(filepath.Separator)) {
		switch part {
		case "secrets", "private", ".git":
			return "", fmt.Errorf("post_apply_verification_path is denied by sandbox path guard")
		}
	}
	return targetAbs, nil
}

func formatPostApplyVerification(command string, output string) string {
	return fmt.Sprintf(`# Post-apply Verification

GeneratedAt: %s
Command: %s
Status: completed

`+"```text\n%s\n```\n", time.Now().UTC().Format(time.RFC3339), command, output)
}
