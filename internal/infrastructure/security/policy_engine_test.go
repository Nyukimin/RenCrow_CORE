package security

import (
	"testing"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/execution"
)

func TestPolicyEngine_Evaluate(t *testing.T) {
	engine := NewPolicyEngine(PolicyConfig{
		DenyCommands:      []string{"rm -rf", "git reset --hard"},
		Workspace:         "/workspace",
		WorkspaceEnforced: true,
	})

	denyCmd := execution.Action{
		Tool:      "shell",
		Arguments: map[string]any{"command": "rm -rf /tmp"},
	}
	if d := engine.Evaluate(denyCmd); d.Decision != execution.DecisionDeny {
		t.Fatalf("expected deny for blocked command, got %s", d.Decision)
	}

	denyPath := execution.Action{
		Tool:      "file_write",
		Arguments: map[string]any{"path": "/etc/passwd", "content": "x"},
	}
	if d := engine.Evaluate(denyPath); d.Decision != execution.DecisionDeny {
		t.Fatalf("expected deny for outside workspace, got %s", d.Decision)
	}

	ask := execution.Action{Tool: "shell", Arguments: map[string]any{"command": "echo hi"}}
	if d := engine.Evaluate(ask); d.Decision != execution.DecisionAllow {
	}

	allow := execution.Action{Tool: "file_read", Arguments: map[string]any{"path": "/workspace/a.txt"}}
	if d := engine.Evaluate(allow); d.Decision != execution.DecisionAllow {
		t.Fatalf("expected allow for safe action, got %s", d.Decision)
	}
}

func TestPolicyEngine_Evaluate_StrictNetworkAllowlist(t *testing.T) {
	engine := NewPolicyEngine(PolicyConfig{
		Mode:           "strict",
		NetworkScope:   "allowlist",
		NetworkAllowed: []string{"api.openai.com"},
	})

	deny := execution.Action{
		Tool:      "web_search",
		Arguments: map[string]any{"url": "https://evil.com/search"},
	}
	d := engine.Evaluate(deny)
	if d.Decision != execution.DecisionDeny {
		t.Fatalf("expected deny for non-allowlisted network host, got %s", d.Decision)
	}
	if d.MatchedRuleID != "deny.network.host.not_allowlisted" {
		t.Fatalf("unexpected rule id: %s", d.MatchedRuleID)
	}

	allow := execution.Action{
		Tool:      "web_search",
		Arguments: map[string]any{"url": "https://api.openai.com/v1/models"},
	}
	if d := engine.Evaluate(allow); d.Decision != execution.DecisionAllow {
		t.Fatalf("expected allow for allowlisted host, got %s", d.Decision)
	}
}

func TestPolicyEngine_Evaluate_BrowserRunNetworkAllowlist(t *testing.T) {
	engine := NewPolicyEngine(PolicyConfig{
		NetworkScope:   "allowlist",
		NetworkAllowed: []string{"localhost"},
	})
	allow := execution.Action{
		Tool:      "browser.run",
		Arguments: map[string]any{"start_url": "http://localhost:18790/viewer"},
	}
	if d := engine.Evaluate(allow); d.Decision != execution.DecisionAllow {
		t.Fatalf("expected allow for browser.run allowlisted host, got %s reason=%s", d.Decision, d.Reason)
	}
	deny := execution.Action{
		Tool:      "browser.run",
		Arguments: map[string]any{"start_url": "https://example.com"},
	}
	if d := engine.Evaluate(deny); d.Decision != execution.DecisionDeny {
		t.Fatalf("expected deny for browser.run non-allowlisted host, got %s", d.Decision)
	}
}

func TestPolicyEngine_Evaluate_SandboxWriteOnly(t *testing.T) {
	engine := NewPolicyEngine(PolicyConfig{
		SandboxRoot:      "/workspace/sandbox",
		SandboxWriteOnly: true,
	})

	allow := execution.Action{
		Tool:      "file_write",
		Arguments: map[string]any{"path": "/workspace/sandbox/sbx_1/output.md", "content": "x"},
	}
	if d := engine.Evaluate(allow); d.Decision != execution.DecisionAllow {
		t.Fatalf("expected sandbox write allow, got %s reason=%s", d.Decision, d.Reason)
	}

	deny := execution.Action{
		Tool:      "file_write",
		Arguments: map[string]any{"path": "/workspace/docs/spec.md", "content": "x"},
	}
	d := engine.Evaluate(deny)
	if d.Decision != execution.DecisionDeny {
		t.Fatalf("expected non-sandbox write deny, got %s", d.Decision)
	}
	if d.MatchedRuleID != "deny.sandbox.outside" {
		t.Fatalf("rule = %s", d.MatchedRuleID)
	}
}

func TestPolicyEngine_Evaluate_DevModeAllowsRiskyProcess(t *testing.T) {
	engine := NewPolicyEngine(PolicyConfig{
		Mode: "dev",
	})
	act := execution.Action{
		Tool:      "shell",
		Arguments: map[string]any{"command": "echo ok"},
	}
	d := engine.Evaluate(act)
	if d.Decision != execution.DecisionAllow {
		t.Fatalf("expected allow in dev default mode, got %s", d.Decision)
	}
}
