package security

import (
	"net"
	"net/url"
	"path/filepath"
	"strings"
)

// SandboxGuard は実行引数の安全境界チェックを担当する
type SandboxGuard struct{}

func NewSandboxGuard() *SandboxGuard {
	return &SandboxGuard{}
}

// IsCommandDenied は禁止コマンドシグネチャに一致するかを判定する
func (g *SandboxGuard) IsCommandDenied(command string, denyCommands []string) bool {
	trimmed := strings.TrimSpace(command)
	for _, sig := range denyCommands {
		s := strings.TrimSpace(sig)
		if s == "" {
			continue
		}
		if strings.Contains(trimmed, s) {
			return true
		}
	}
	return false
}

// IsPathWithinWorkspace は path が workspace 配下かを判定する
func (g *SandboxGuard) IsPathWithinWorkspace(path, workspace string) bool {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(workspace) == "" {
		return false
	}

	targetAbs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return false
	}
	workspaceAbs, err := filepath.Abs(filepath.Clean(workspace))
	if err != nil {
		return false
	}

	rel, err := filepath.Rel(workspaceAbs, targetAbs)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return !strings.HasPrefix(rel, "..")
}

func (g *SandboxGuard) IsSafeSandboxWritePath(path, sandboxRoot string) bool {
	if !g.IsPathWithinWorkspace(path, sandboxRoot) {
		return false
	}
	clean := filepath.Clean(path)
	base := filepath.Base(clean)
	if base == ".env" || strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") {
		return false
	}
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		switch part {
		case "secrets", "private", ".git":
			return false
		}
	}
	return true
}

// IsHostAllowed checks if host is in allowlist (exact or suffix ".example.com").
func (g *SandboxGuard) IsHostAllowed(host string, allowlist []string) bool {
	host = strings.TrimSpace(strings.ToLower(host))
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return false
	}
	for _, raw := range allowlist {
		a := strings.TrimSpace(strings.ToLower(raw))
		a = strings.TrimSuffix(a, ".")
		if a == "" {
			continue
		}
		if strings.HasPrefix(a, ".") {
			if strings.HasSuffix(host, a) {
				return true
			}
			continue
		}
		if host == a {
			return true
		}
	}
	return false
}

// ExtractNetworkHost tries to extract hostname from action arguments.
func (g *SandboxGuard) ExtractNetworkHost(args map[string]any) (string, bool) {
	if args == nil {
		return "", false
	}
	if u, ok := args["url"].(string); ok {
		u = strings.TrimSpace(u)
		if u != "" {
			parsed, err := url.Parse(u)
			if err == nil && parsed != nil && parsed.Host != "" {
				host := parsed.Hostname()
				if host != "" {
					return strings.ToLower(host), true
				}
			}
		}
	}
	if u, ok := args["start_url"].(string); ok {
		u = strings.TrimSpace(u)
		if u != "" {
			parsed, err := url.Parse(u)
			if err == nil && parsed != nil && parsed.Host != "" {
				host := parsed.Hostname()
				if host != "" {
					return strings.ToLower(host), true
				}
			}
		}
	}
	if h, ok := args["host"].(string); ok {
		h = strings.TrimSpace(h)
		if h != "" {
			// Accept host:port and raw host.
			if strings.Contains(h, ":") {
				if host, _, err := net.SplitHostPort(h); err == nil && host != "" {
					return strings.ToLower(host), true
				}
			}
			return strings.ToLower(h), true
		}
	}
	return "", false
}
