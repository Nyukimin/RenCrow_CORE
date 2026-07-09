package config

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

type RuntimeTopologyConfig struct {
	Version       int                                     `yaml:"version"`
	ActiveProfile string                                  `yaml:"active_profile"`
	DefaultPorts  map[string]map[string]int               `yaml:"default_ports"`
	Modules       map[string]RuntimeTopologyModuleConfig  `yaml:"modules"`
	Profiles      map[string]RuntimeTopologyProfileConfig `yaml:"profiles"`
}

type RuntimeTopologyProfileConfig struct {
	Modules map[string]RuntimeTopologyModuleConfig `yaml:"modules"`
}

type RuntimeTopologyModuleConfig struct {
	Kind        string                               `yaml:"kind"`
	Host        string                               `yaml:"host"`
	BackendHost string                               `yaml:"backend_host"`
	Root        string                               `yaml:"root"`
	Service     string                               `yaml:"service"`
	Ports       map[string]int                       `yaml:"ports"`
	Roles       map[string]RuntimeTopologyRoleConfig `yaml:"roles"`
}

type RuntimeTopologyRoleConfig struct {
	Enabled     *bool  `yaml:"enabled"`
	URL         string `yaml:"url"`
	Host        string `yaml:"host"`
	Port        int    `yaml:"port"`
	BackendURL  string `yaml:"backend_url"`
	BackendHost string `yaml:"backend_host"`
	BackendPort int    `yaml:"backend_port"`
}

func (c *Config) resolveRuntimeTopologyReferences() error {
	resolver := newRuntimeTopologyResolver(c.RuntimeTopology)
	return resolveStringFields(reflect.ValueOf(c).Elem(), resolver)
}

func (c *Config) RuntimeTopologyRoleEnabled(moduleID, roleName string) bool {
	if c == nil {
		return true
	}
	resolver := newRuntimeTopologyResolver(c.RuntimeTopology)
	module, ok := resolver.module(moduleID)
	if !ok {
		return true
	}
	role, ok := module.Roles[strings.ToLower(strings.TrimSpace(roleName))]
	if !ok {
		role, ok = module.Roles[strings.TrimSpace(roleName)]
	}
	if !ok || role.Enabled == nil {
		return true
	}
	return *role.Enabled
}

type runtimeTopologyResolver struct {
	topology RuntimeTopologyConfig
}

func newRuntimeTopologyResolver(topology RuntimeTopologyConfig) runtimeTopologyResolver {
	return runtimeTopologyResolver{topology: topology}
}

func resolveStringFields(v reflect.Value, resolver runtimeTopologyResolver) error {
	if !v.IsValid() {
		return nil
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		return resolveStringFields(v.Elem(), resolver)
	}
	switch v.Kind() {
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			field := v.Field(i)
			if !field.CanSet() && field.Kind() != reflect.Struct && field.Kind() != reflect.Pointer {
				continue
			}
			if err := resolveStringFields(field, resolver); err != nil {
				return err
			}
		}
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String || v.Type().Elem().Kind() != reflect.String {
			return nil
		}
		for _, key := range v.MapKeys() {
			raw := v.MapIndex(key).String()
			resolved, err := resolver.resolveString(raw)
			if err != nil {
				return err
			}
			v.SetMapIndex(key, reflect.ValueOf(resolved))
		}
	case reflect.String:
		if !v.CanSet() {
			return nil
		}
		resolved, err := resolver.resolveString(v.String())
		if err != nil {
			return err
		}
		v.SetString(resolved)
	}
	return nil
}

func (r runtimeTopologyResolver) resolveString(raw string) (string, error) {
	out := raw
	for {
		start := strings.Index(out, "${module:")
		if start < 0 {
			return out, nil
		}
		end := strings.IndexByte(out[start:], '}')
		if end < 0 {
			return "", fmt.Errorf("unterminated runtime topology reference in %q", raw)
		}
		end += start
		expr := out[start+len("${module:") : end]
		value, err := r.resolveModuleRef(expr)
		if err != nil {
			return "", err
		}
		out = out[:start] + value + out[end+1:]
	}
}

func (r runtimeTopologyResolver) resolveModuleRef(expr string) (string, error) {
	parts := strings.Split(expr, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid runtime topology module reference %q", expr)
	}
	moduleID, group, name := parts[0], parts[1], parts[2]
	module, ok := r.module(moduleID)
	if !ok {
		return "", fmt.Errorf("unknown runtime topology module %q", moduleID)
	}
	switch group {
	case "endpoints":
		if roleURL := module.roleEndpointURL(name); roleURL != "" {
			return roleURL, nil
		}
		return r.moduleURL(moduleID, module.endpointHost(name), name, endpointFallbackPortName(name)), nil
	case "backends":
		if roleURL := module.roleBackendURL(name); roleURL != "" {
			return roleURL, nil
		}
		return r.moduleURL(moduleID, module.backendHostForRole(name), "backend_"+name, backendFallbackPortName(name)), nil
	default:
		return "", fmt.Errorf("unsupported runtime topology module reference %q", expr)
	}
}

func (r runtimeTopologyResolver) module(moduleID string) (RuntimeTopologyModuleConfig, bool) {
	if r.topology.ActiveProfile != "" {
		if profile, ok := r.topology.Profiles[r.topology.ActiveProfile]; ok {
			if module, ok := profile.Modules[moduleID]; ok {
				return module, true
			}
		}
	}
	module, ok := r.topology.Modules[moduleID]
	return module, ok
}

func (r runtimeTopologyResolver) moduleURL(moduleID, host, portName, fallbackPortName string) string {
	port := r.port(moduleID, portName)
	if port == 0 {
		port = r.port(moduleID, fallbackPortName)
	}
	if port == 0 {
		return ""
	}
	return "http://" + host + ":" + strconv.Itoa(port)
}

func (r runtimeTopologyResolver) port(moduleID, portName string) int {
	if module, ok := r.module(moduleID); ok {
		if role := module.Roles[portName]; role.Port > 0 {
			return role.Port
		}
		if strings.HasPrefix(portName, "backend_") {
			roleName := strings.TrimPrefix(portName, "backend_")
			if role := module.Roles[roleName]; role.BackendPort > 0 {
				return role.BackendPort
			}
		}
		if port := module.Ports[portName]; port > 0 {
			return port
		}
	}
	if ports := r.topology.DefaultPorts[moduleID]; ports != nil {
		if port := ports[portName]; port > 0 {
			return port
		}
	}
	return builtInRuntimeTopologyPort(moduleID, portName)
}

func (m RuntimeTopologyModuleConfig) host() string {
	if strings.TrimSpace(m.Host) == "" {
		return "127.0.0.1"
	}
	return strings.TrimSpace(m.Host)
}

func (m RuntimeTopologyModuleConfig) backendHost() string {
	if strings.TrimSpace(m.BackendHost) != "" {
		return strings.TrimSpace(m.BackendHost)
	}
	return m.host()
}

func (m RuntimeTopologyModuleConfig) endpointHost(roleName string) string {
	if role := m.Roles[roleName]; strings.TrimSpace(role.Host) != "" {
		return strings.TrimSpace(role.Host)
	}
	return m.host()
}

func (m RuntimeTopologyModuleConfig) roleEndpointURL(roleName string) string {
	if role := m.Roles[roleName]; strings.TrimSpace(role.URL) != "" {
		return strings.TrimRight(strings.TrimSpace(role.URL), "/")
	}
	return ""
}

func (m RuntimeTopologyModuleConfig) roleBackendURL(roleName string) string {
	if role := m.Roles[roleName]; strings.TrimSpace(role.BackendURL) != "" {
		return strings.TrimRight(strings.TrimSpace(role.BackendURL), "/")
	}
	return ""
}

func (m RuntimeTopologyModuleConfig) backendHostForRole(roleName string) string {
	if role := m.Roles[roleName]; strings.TrimSpace(role.BackendHost) != "" {
		return strings.TrimSpace(role.BackendHost)
	}
	return m.backendHost()
}

func endpointFallbackPortName(name string) string {
	switch name {
	case "chatworker", "coder1", "coder2", "coder3", "coder4":
		return "worker"
	default:
		return name
	}
}

func backendFallbackPortName(name string) string {
	switch name {
	case "chatworker", "coder1", "coder2", "coder3", "coder4":
		return "backend_worker"
	default:
		return "backend_" + name
	}
}

func builtInRuntimeTopologyPort(moduleID, portName string) int {
	if moduleID != "RenCraw_LLM" {
		if portName == "http" {
			switch moduleID {
			case "rencrow":
				return 18790
			case "RenCrow_STT":
				return 8766
			case "RenCrow_TTS":
				return 7870
			}
		}
		return 0
	}
	switch portName {
	case "mgmt":
		return 8079
	case "chat":
		return 8081
	case "worker", "chatworker", "coder1", "coder2", "coder3", "coder4":
		return 8082
	case "heavy":
		return 8083
	case "wild":
		return 8084
	case "backend_chat":
		return 18081
	case "backend_worker":
		return 11434
	case "backend_heavy":
		return 18083
	case "backend_wild":
		return 18084
	default:
		return 0
	}
}
