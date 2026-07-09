package security

import (
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domainsecurity "github.com/Nyukimin/RenCrow_CORE/internal/domain/security"
)

// PolicyConfig はポリシー判定設定
type PolicyConfig struct {
	Mode              string
	NetworkScope      string
	NetworkAllowed    []string
	DenyCommands      []string
	Workspace         string
	WorkspaceEnforced bool
	SandboxRoot       string
	SandboxWriteOnly  bool
}

// PolicyEngine は実行可否判定を行う
type PolicyEngine struct {
	cfg   PolicyConfig
	guard *SandboxGuard
}

func NewPolicyEngine(cfg PolicyConfig) *PolicyEngine {
	return &PolicyEngine{cfg: cfg, guard: NewSandboxGuard()}
}

func (e *PolicyEngine) Evaluate(action execution.Action) execution.PolicyDecision {
	if decision, denied := e.evaluateShellPolicy(action); denied {
		return decision
	}
	if decision, denied := e.evaluateWorkspacePolicy(action); denied {
		return decision
	}
	if decision, denied := e.evaluateNetworkPolicy(action); denied {
		return decision
	}
	return allowDecision()
}

func (e *PolicyEngine) evaluateShellPolicy(action execution.Action) (execution.PolicyDecision, bool) {
	if action.Tool != "shell" {
		return execution.PolicyDecision{}, false
	}
	cmd, ok := action.Arguments["command"].(string)
	if !ok || !e.guard.IsCommandDenied(cmd, e.cfg.DenyCommands) {
		return execution.PolicyDecision{}, false
	}
	return execution.PolicyDecision{
		Decision:      execution.DecisionDeny,
		Reason:        "blocked shell command signature",
		MatchedRuleID: "deny.shell.signature",
	}, true
}

func (e *PolicyEngine) evaluateWorkspacePolicy(action execution.Action) (execution.PolicyDecision, bool) {
	if e.cfg.SandboxWriteOnly && action.Tool == "file_write" {
		p, ok := action.Arguments["path"].(string)
		if ok && e.guard.IsSafeSandboxWritePath(p, e.cfg.SandboxRoot) {
			return execution.PolicyDecision{}, false
		}
		return execution.PolicyDecision{
			Decision:      execution.DecisionDeny,
			Reason:        fmt.Sprintf("path outside sandbox: %v", action.Arguments["path"]),
			MatchedRuleID: "deny.sandbox.outside",
		}, true
	}
	if !e.cfg.WorkspaceEnforced || action.Tool != "file_write" {
		return execution.PolicyDecision{}, false
	}
	p, ok := action.Arguments["path"].(string)
	if !ok || e.guard.IsPathWithinWorkspace(p, e.cfg.Workspace) {
		return execution.PolicyDecision{}, false
	}
	return execution.PolicyDecision{
		Decision:      execution.DecisionDeny,
		Reason:        fmt.Sprintf("path outside workspace: %s", p),
		MatchedRuleID: "deny.workspace.outside",
	}, true
}

func (e *PolicyEngine) evaluateNetworkPolicy(action execution.Action) (execution.PolicyDecision, bool) {
	if !isNetworkTool(action.Tool) {
		return execution.PolicyDecision{}, false
	}
	networkScope := strings.TrimSpace(e.cfg.NetworkScope)
	if networkScope == "" {
		profile := e.profileByMode()
		networkScope = profile.NetworkScope
	}
	switch networkScope {
	case "blocked":
		return execution.PolicyDecision{
			Decision:      execution.DecisionDeny,
			Reason:        "network access blocked by policy",
			MatchedRuleID: "deny.network.blocked",
		}, true
	case "allowlist":
		return e.evaluateNetworkAllowlistPolicy(action)
	default:
		return execution.PolicyDecision{}, false
	}
}

func (e *PolicyEngine) evaluateNetworkAllowlistPolicy(action execution.Action) (execution.PolicyDecision, bool) {
	host, ok := e.guard.ExtractNetworkHost(action.Arguments)
	if !ok {
		return execution.PolicyDecision{
			Decision:      execution.DecisionDeny,
			Reason:        "network host is required under allowlist policy",
			MatchedRuleID: "deny.network.host.missing",
		}, true
	}
	allowed := e.cfg.NetworkAllowed
	if len(allowed) == 0 {
		allowed = []string{"localhost", "127.0.0.1", "::1"}
	}
	if e.guard.IsHostAllowed(host, allowed) {
		return execution.PolicyDecision{}, false
	}
	return execution.PolicyDecision{
		Decision:      execution.DecisionDeny,
		Reason:        fmt.Sprintf("host not in allowlist: %s", host),
		MatchedRuleID: "deny.network.host.not_allowlisted",
	}, true
}

func allowDecision() execution.PolicyDecision {
	return execution.PolicyDecision{
		Decision:      execution.DecisionAllow,
		Reason:        "policy allow",
		MatchedRuleID: "allow.default",
	}
}

func (e *PolicyEngine) profileByMode() domainsecurity.SecurityProfile {
	switch strings.TrimSpace(strings.ToLower(e.cfg.Mode)) {
	case "strict":
		return domainsecurity.StrictProfile()
	case "dev":
		return domainsecurity.DevProfile()
	default:
		return domainsecurity.BalancedProfile()
	}
}

func isNetworkTool(toolName string) bool {
	switch strings.TrimSpace(strings.ToLower(toolName)) {
	case "web_search", "http_request", "fetch_url", "browser.run":
		return true
	default:
		return false
	}
}
