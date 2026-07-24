// Package agentcontrol loads the portable, declarative character control plane
// from a RenCrow workspace. Runtime availability and side-effect enforcement
// remain CORE responsibilities.
package agentcontrol

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	controlDirName = "control"
	schemaVersion  = 1
)

var controlFileNames = []string{
	"agents.yaml",
	"routing.yaml",
	"handoff.yaml",
	"tools.yaml",
}

var coreRouteOwners = map[string]string{
	"CHAT":     "mio",
	"PLAN":     "mio",
	"ANALYZE":  "kuro",
	"OPS":      "shiro",
	"RESEARCH": "mio",
	"CODE":     "shiro",
	"CODE1":    "shiro",
	"CODE2":    "shiro",
	"CODE3":    "shiro",
	"CODE4":    "shiro",
	"WILD":     "midori",
}

// Control is the validated, combined workspace control plane.
type Control struct {
	Agents  map[string]Agent
	Routing Routing
	Handoff Handoff
	Tools   Tools
}

// Agent describes stable character scope. It does not grant runtime access.
type Agent struct {
	Role         string   `yaml:"role"`
	Capabilities []string `yaml:"capabilities"`
	NonGoals     []string `yaml:"non_goals"`
}

// Routing maps CORE route categories to their canonical execution owner.
type Routing struct {
	Fallback string           `yaml:"fallback"`
	Routes   map[string]Route `yaml:"routes"`
}

// Route is one canonical CORE route destination.
type Route struct {
	Primary string `yaml:"primary"`
}

// Handoff is the common return contract for work outside an agent's scope.
type Handoff struct {
	DestinationOwner        string   `yaml:"destination_owner"`
	AgentSelectsDestination bool     `yaml:"agent_selects_destination"`
	RequiredFields          []string `yaml:"required_fields"`
}

// Tools describes shared tool-selection policy. CORE ToolRunner metadata still
// decides which tools are actually available and what they may execute.
type Tools struct {
	MetadataSource       string                `yaml:"metadata_source"`
	AvailabilityRequired bool                  `yaml:"availability_required"`
	Agents               map[string]AgentTools `yaml:"agents"`
}

// AgentTools is the tool posture for one character.
type AgentTools struct {
	Access     string                   `yaml:"access"`
	Rules      []string                 `yaml:"rules"`
	Selections map[string]ToolSelection `yaml:"selections"`
}

// ToolSelection declares preference without authorizing automatic fallback.
type ToolSelection struct {
	Preferred         string   `yaml:"preferred"`
	Alternatives      []string `yaml:"alternatives"`
	AutomaticFallback bool     `yaml:"automatic_fallback"`
}

type agentsFile struct {
	Version int              `yaml:"version"`
	Agents  map[string]Agent `yaml:"agents"`
}

type routingFile struct {
	Version int `yaml:"version"`
	Routing `yaml:",inline"`
}

type handoffFile struct {
	Version int `yaml:"version"`
	Handoff `yaml:",inline"`
}

type toolsFile struct {
	Version int `yaml:"version"`
	Tools   `yaml:",inline"`
}

// Load reads workspace/control. An absent control directory is supported for
// backward compatibility. A partial or invalid control set is rejected.
func Load(workspaceDir string) (*Control, error) {
	controlDir := filepath.Join(workspaceDir, controlDirName)
	if _, err := os.Stat(controlDir); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat shared agent control: %w", err)
	}

	for _, name := range controlFileNames {
		path := filepath.Join(controlDir, name)
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("shared agent control requires %s: %w", name, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("shared agent control file must not be a symbolic link: %s", name)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("shared agent control file must be regular: %s", name)
		}
	}

	var agents agentsFile
	if err := decodeStrict(filepath.Join(controlDir, "agents.yaml"), &agents); err != nil {
		return nil, err
	}
	var routes routingFile
	if err := decodeStrict(filepath.Join(controlDir, "routing.yaml"), &routes); err != nil {
		return nil, err
	}
	var handoff handoffFile
	if err := decodeStrict(filepath.Join(controlDir, "handoff.yaml"), &handoff); err != nil {
		return nil, err
	}
	var tools toolsFile
	if err := decodeStrict(filepath.Join(controlDir, "tools.yaml"), &tools); err != nil {
		return nil, err
	}

	control := &Control{
		Agents:  agents.Agents,
		Routing: routes.Routing,
		Handoff: handoff.Handoff,
		Tools:   tools.Tools,
	}
	if err := control.validate(map[string]int{
		"agents.yaml":  agents.Version,
		"routing.yaml": routes.Version,
		"handoff.yaml": handoff.Version,
		"tools.yaml":   tools.Version,
	}); err != nil {
		return nil, fmt.Errorf("validate shared agent control: %w", err)
	}
	return control, nil
}

func decodeStrict(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read shared agent control %s: %w", filepath.Base(path), err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("parse shared agent control %s: %w", filepath.Base(path), err)
	}
	return nil
}

func (c *Control) validate(versions map[string]int) error {
	for name, version := range versions {
		if version != schemaVersion {
			return fmt.Errorf("%s version = %d, want %d", name, version, schemaVersion)
		}
	}
	if len(c.Agents) == 0 {
		return fmt.Errorf("agents.yaml must define agents")
	}
	for name, profile := range c.Agents {
		if name != strings.ToLower(strings.TrimSpace(name)) || profile.Role == "" || len(profile.Capabilities) == 0 {
			return fmt.Errorf("invalid agent profile: %q", name)
		}
	}
	if c.Routing.Fallback != "CHAT" {
		return fmt.Errorf("routing fallback = %q, want CHAT", c.Routing.Fallback)
	}
	if len(c.Routing.Routes) != len(coreRouteOwners) {
		return fmt.Errorf("routing.yaml must define all %d CORE routes", len(coreRouteOwners))
	}
	for route, owner := range coreRouteOwners {
		got, ok := c.Routing.Routes[route]
		if !ok {
			return fmt.Errorf("routing.yaml missing CORE route %s", route)
		}
		if got.Primary != owner {
			return fmt.Errorf("route %s primary = %q, CORE execution owner is %q", route, got.Primary, owner)
		}
		if _, ok := c.Agents[owner]; !ok {
			return fmt.Errorf("route %s references undefined agent %q", route, owner)
		}
	}
	if c.Handoff.DestinationOwner != "orchestrator" {
		return fmt.Errorf("handoff destination_owner = %q, want orchestrator", c.Handoff.DestinationOwner)
	}
	if c.Handoff.AgentSelectsDestination {
		return fmt.Errorf("handoff agent_selects_destination must be false")
	}
	for _, required := range []string{"reason", "required_capability", "context"} {
		if !contains(c.Handoff.RequiredFields, required) {
			return fmt.Errorf("handoff required_fields must include %q", required)
		}
	}
	if c.Tools.MetadataSource != "core_toolrunner" {
		return fmt.Errorf("tools metadata_source = %q, want core_toolrunner", c.Tools.MetadataSource)
	}
	if !c.Tools.AvailabilityRequired {
		return fmt.Errorf("tools availability_required must be true")
	}
	for agentName := range c.Agents {
		policy, ok := c.Tools.Agents[agentName]
		if !ok || strings.TrimSpace(policy.Access) == "" {
			return fmt.Errorf("tools.yaml missing access policy for agent %q", agentName)
		}
		for capability, selection := range policy.Selections {
			if strings.TrimSpace(selection.Preferred) == "" {
				return fmt.Errorf("tool selection %s.%s must define preferred", agentName, capability)
			}
			if selection.AutomaticFallback {
				return fmt.Errorf("tool selection %s.%s automatic_fallback must be false", agentName, capability)
			}
		}
	}
	return nil
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// PromptFor renders the validated slice applicable to one character.
func (c *Control) PromptFor(agentName string) string {
	if c == nil {
		return ""
	}
	name := strings.ToLower(strings.TrimSpace(agentName))
	profile, ok := c.Agents[name]
	if !ok {
		return ""
	}

	var b strings.Builder
	b.WriteString("# Shared Agent Control\n\n")
	b.WriteString("この制御はworkspace/controlから読み込まれ、RenCrow COREが検証した共通契約です。")
	b.WriteString("実際のTool利用可否、引数、権限、安全制約はCORE runtime metadataを優先します。\n\n")
	b.WriteString("## Agent Profile\n\n")
	fmt.Fprintf(&b, "- agent: %s\n- role: %s\n", name, profile.Role)
	writeList(&b, "capabilities", profile.Capabilities)
	writeList(&b, "non_goals", profile.NonGoals)

	b.WriteString("\n## Routing\n\n")
	routes := sortedKeys(c.Routing.Routes)
	for _, route := range routes {
		fmt.Fprintf(&b, "- %s -> %s\n", route, c.Routing.Routes[route].Primary)
	}
	fmt.Fprintf(&b, "- fallback: %s\n", c.Routing.Fallback)

	b.WriteString("\n## Handoff\n\n")
	fmt.Fprintf(&b, "- destination_owner: %s\n", c.Handoff.DestinationOwner)
	fmt.Fprintf(&b, "- agent_selects_destination: %t\n", c.Handoff.AgentSelectsDestination)
	writeList(&b, "required_fields", c.Handoff.RequiredFields)
	b.WriteString("- 担当外では移譲先を指名せず、必要能力と返却情報をOrchestratorへ返します。\n")

	if policy, ok := c.Tools.Agents[name]; ok {
		b.WriteString("\n## Tools\n\n")
		fmt.Fprintf(&b, "- metadata_source: %s\n", c.Tools.MetadataSource)
		fmt.Fprintf(&b, "- availability_required: %t\n", c.Tools.AvailabilityRequired)
		fmt.Fprintf(&b, "- access: %s\n", policy.Access)
		writeList(&b, "rules", policy.Rules)
		selections := sortedKeys(policy.Selections)
		for _, capability := range selections {
			selection := policy.Selections[capability]
			fmt.Fprintf(&b, "- selection.%s.preferred: %s\n", capability, selection.Preferred)
			writeList(&b, "selection."+capability+".alternatives", selection.Alternatives)
			fmt.Fprintf(&b, "- selection.%s.automatic_fallback: %t\n", capability, selection.AutomaticFallback)
		}
	}
	return strings.TrimSpace(b.String())
}

func writeList(b *strings.Builder, label string, values []string) {
	if len(values) == 0 {
		fmt.Fprintf(b, "- %s: []\n", label)
		return
	}
	fmt.Fprintf(b, "- %s: %s\n", label, strings.Join(values, ", "))
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
