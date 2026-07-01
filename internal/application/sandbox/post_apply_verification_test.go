package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	domainsandbox "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/sandbox"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/tool"
)

type fakePostApplyToolRunner struct {
	toolName string
	args     map[string]any
	resp     *tool.ToolResponse
	err      error
}

func (f *fakePostApplyToolRunner) ExecuteV2(_ context.Context, toolName string, args map[string]any) (*tool.ToolResponse, error) {
	f.toolName = toolName
	f.args = args
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func TestPostApplyVerificationRunnerRunsCommandAndWritesEvidence(t *testing.T) {
	root := t.TempDir()
	runner := &fakePostApplyToolRunner{resp: tool.NewSuccess("ok\n")}
	verifier := NewPostApplyVerificationRunner(runner, root)

	result, err := verifier.RunPostApplyVerification(context.Background(), domainsandbox.PromotionApplyRequest{
		PostApplyVerificationPath:    "reports/post_apply.md",
		PostApplyVerificationCommand: "go test ./pkg/rencrowclient",
	})
	if err != nil {
		t.Fatalf("RunPostApplyVerification() error = %v", err)
	}
	if runner.toolName != "shell" || runner.args["command"] != "go test ./pkg/rencrowclient" {
		t.Fatalf("runner call tool=%s args=%#v", runner.toolName, runner.args)
	}
	if result.Status != "completed" || result.Output != "ok\n" {
		t.Fatalf("result=%#v", result)
	}
	if !strings.HasPrefix(result.OutputPath, root) {
		t.Fatalf("output path outside root: %s", result.OutputPath)
	}
	data, err := os.ReadFile(filepath.Join(root, "reports", "post_apply.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Command: go test ./pkg/rencrowclient") || !strings.Contains(string(data), "ok") {
		t.Fatalf("unexpected evidence file:\n%s", string(data))
	}
}

func TestPostApplyVerificationRunnerSkipsEmptyCommand(t *testing.T) {
	root := t.TempDir()
	runner := &fakePostApplyToolRunner{resp: tool.NewSuccess("unused")}
	verifier := NewPostApplyVerificationRunner(runner, root)

	result, err := verifier.RunPostApplyVerification(context.Background(), domainsandbox.PromotionApplyRequest{
		PostApplyVerificationPath: "reports/post_apply.md",
	})
	if err != nil {
		t.Fatalf("RunPostApplyVerification() error = %v", err)
	}
	if result.Status != "" || runner.toolName != "" {
		t.Fatalf("expected no-op, result=%#v runner=%#v", result, runner)
	}
}

func TestPostApplyVerificationRunnerRejectsOutputOutsideSandboxRoot(t *testing.T) {
	root := t.TempDir()
	verifier := NewPostApplyVerificationRunner(&fakePostApplyToolRunner{resp: tool.NewSuccess("ok")}, root)

	_, err := verifier.RunPostApplyVerification(context.Background(), domainsandbox.PromotionApplyRequest{
		PostApplyVerificationPath:    "../outside.md",
		PostApplyVerificationCommand: "echo ok",
	})
	if err == nil {
		t.Fatal("expected sandbox root error")
	}
	if !strings.Contains(err.Error(), "inside sandbox root") {
		t.Fatalf("err=%v", err)
	}
}

func TestPostApplyVerificationRunnerFailsRejectedCommand(t *testing.T) {
	root := t.TempDir()
	verifier := NewPostApplyVerificationRunner(&fakePostApplyToolRunner{
		resp: tool.NewError(tool.ErrPermissionDenied, "blocked", nil),
	}, root)

	_, err := verifier.RunPostApplyVerification(context.Background(), domainsandbox.PromotionApplyRequest{
		PostApplyVerificationPath:    "reports/post_apply.md",
		PostApplyVerificationCommand: "rm -rf /",
	})
	if err == nil {
		t.Fatal("expected command rejection")
	}
	if !strings.Contains(err.Error(), "command rejected") {
		t.Fatalf("err=%v", err)
	}
}
