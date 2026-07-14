package advisor

import (
	"context"
	"strings"
	"testing"

	advisorDomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/advisor"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

type fakeProvider struct {
	profile advisorDomain.Profile
	result  advisorDomain.AdviceResult
	req     advisorDomain.AdviceRequest
}

func (f *fakeProvider) Profile() advisorDomain.Profile {
	return f.profile
}

func (f *fakeProvider) RequestAdvice(_ context.Context, req advisorDomain.AdviceRequest) (advisorDomain.AdviceResult, error) {
	f.req = req
	f.result.RequestID = req.ID
	f.result.AdvisorID = req.AdvisorID
	return f.result, nil
}

func TestServiceRoutesToRequestedAdvisor(t *testing.T) {
	provider := &fakeProvider{
		profile: advisorDomain.Profile{ID: advisorDomain.AdvisorCodex, DisplayName: "Codex"},
		result:  advisorDomain.AdviceResult{Status: advisorDomain.StatusCompleted, Summary: "advice"},
	}
	service := NewService(provider)
	result, err := service.RequestAdvice(context.Background(), advisorDomain.AdviceRequest{
		ID:               "req-1",
		RequestedByAgent: "shiro",
		AdvisorID:        advisorDomain.AdvisorCodex,
		Purpose:          "code_advice",
		Prompt:           "調べて",
	})
	if err != nil {
		t.Fatalf("RequestAdvice failed: %v", err)
	}
	if result.Summary != "advice" || provider.req.Prompt != "調べて" {
		t.Fatalf("unexpected routing result=%#v req=%#v", result, provider.req)
	}
}

type fakeToolRunner struct {
	tools []tool.ToolMetadata
	args  map[string]any
	resp  *tool.ToolResponse
}

func (f *fakeToolRunner) ListTools(context.Context) ([]tool.ToolMetadata, error) {
	return f.tools, nil
}

func (f *fakeToolRunner) ExecuteV2(_ context.Context, _ string, args map[string]any) (*tool.ToolResponse, error) {
	f.args = args
	return f.resp, nil
}

func TestCodexToolAdvisorUsesReadOnlySandbox(t *testing.T) {
	runner := &fakeToolRunner{
		tools: []tool.ToolMetadata{{ToolID: "codex.run"}},
		resp:  tool.NewSuccess("codex advice"),
	}
	advisor := NewCodexToolAdvisor(runner)
	result, err := advisor.RequestAdvice(context.Background(), advisorDomain.AdviceRequest{
		RequestedByAgent: "shiro",
		AdvisorID:        advisorDomain.AdvisorCodex,
		Purpose:          "code_advice",
		Prompt:           "計画して",
	})
	if err != nil {
		t.Fatalf("RequestAdvice failed: %v", err)
	}
	if result.Status != advisorDomain.StatusCompleted || result.Summary != "codex advice" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if runner.args["sandbox"] != "read-only" {
		t.Fatalf("sandbox = %#v, want read-only", runner.args["sandbox"])
	}
	if prompt, _ := runner.args["prompt"].(string); prompt != "計画して" {
		t.Fatalf("prompt = %q", prompt)
	}
}

func TestCodexToolAdvisorRejectsUnavailableCodexRun(t *testing.T) {
	advisor := NewCodexToolAdvisor(&fakeToolRunner{tools: []tool.ToolMetadata{{ToolID: "other.tool"}}})
	result, err := advisor.RequestAdvice(context.Background(), advisorDomain.AdviceRequest{
		RequestedByAgent: "shiro",
		AdvisorID:        advisorDomain.AdvisorCodex,
		Purpose:          "code_advice",
		Prompt:           "計画して",
	})
	if err == nil || !strings.Contains(err.Error(), "codex.run") {
		t.Fatalf("expected unavailable error, got result=%#v err=%v", result, err)
	}
	if result.Status != advisorDomain.StatusUnavailable {
		t.Fatalf("status = %q, want unavailable", result.Status)
	}
}
