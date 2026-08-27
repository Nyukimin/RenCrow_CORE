package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	unitExecPathPattern = regexp.MustCompile(`\bpath=([^\s;]+)`)
	startupPhasePattern = regexp.MustCompile(`startup_phase\s+phase=([A-Za-z0-9_.-]+)\s+elapsed_ms=([0-9]+)`)
	listenerPortPattern = regexp.MustCompile(`(?:\[([^\]]+)\]|([^\s:]+)):(18790)\b`)
	listenerPIDPattern  = regexp.MustCompile(`\bpid=([0-9]+)\b`)
	configEnvPattern    = regexp.MustCompile(`(?:^|\s)RENCROW_CONFIG=([^\s]+)`)
)

type systemdServiceSnapshot struct {
	Properties map[string]string
	UnitText   string
	MainPID    int64
	ExecPath   string
	ConfigPath string
}

func requireLinuxSystemd(options verifierOptions, deps verifierDependencies) verifierOutcome {
	deps = normalizeVerifierDependencies(deps)
	if deps.Platform() != "linux" {
		return verifierOutcome{Status: "blocked", FailureBoundary: "systemd user verification is unavailable on this platform"}
	}
	if strings.TrimSpace(options.Unit) != canonicalCoreUnit {
		return verifierOutcome{Status: "blocked", FailureBoundary: "CORE systemd unit is fixed to rencrow.service"}
	}
	return verifierOutcome{}
}

func readSystemdService(ctx context.Context, options verifierOptions, deps verifierDependencies) (systemdServiceSnapshot, verifierOutcome) {
	deps = normalizeVerifierDependencies(deps)
	if outcome := requireLinuxSystemd(options, deps); outcome.Status != "" {
		return systemdServiceSnapshot{}, outcome
	}
	propertiesResult := deps.RunCommand(ctx, "systemctl", []string{
		"--user", "show", canonicalCoreUnit,
		"-p", "ActiveState", "-p", "SubState", "-p", "Result",
		"-p", "MainPID", "-p", "ExecStart", "-p", "Environment",
		"-p", "EnvironmentFiles", "-p", "FragmentPath", "-p", "NRestarts",
	})
	if commandUnavailable(propertiesResult) {
		return systemdServiceSnapshot{}, verifierOutcome{Status: "blocked", FailureBoundary: "systemd user service observation unavailable"}
	}
	properties := parseKeyValueOutput(propertiesResult.Stdout)
	pid, err := parsePositivePID(properties["MainPID"])
	if err != nil {
		return systemdServiceSnapshot{}, verifierOutcome{Status: "blocked", FailureBoundary: "CORE systemd MainPID is unavailable"}
	}
	execPath := parseSystemdExecPath(properties["ExecStart"])
	if execPath == "" {
		return systemdServiceSnapshot{}, verifierOutcome{Status: "blocked", FailureBoundary: "CORE systemd executable identity is unavailable"}
	}
	configPath := parseConfigPath(properties["Environment"])
	unitResult := deps.RunCommand(ctx, "systemctl", []string{"--user", "cat", canonicalCoreUnit})
	if commandUnavailable(unitResult) || strings.TrimSpace(unitResult.Stdout) == "" {
		return systemdServiceSnapshot{}, verifierOutcome{Status: "blocked", FailureBoundary: "canonical CORE systemd unit text is unavailable"}
	}
	return systemdServiceSnapshot{
		Properties: properties,
		UnitText:   unitResult.Stdout,
		MainPID:    pid,
		ExecPath:   expandHomePlaceholder(execPath),
		ConfigPath: expandHomePlaceholder(configPath),
	}, verifierOutcome{}
}

func commandUnavailable(result verifierCommandResult) bool {
	return result.Err != nil || result.ExitCode != 0
}

func parseSystemdExecPath(rendered string) string {
	match := unitExecPathPattern.FindStringSubmatch(rendered)
	if len(match) == 2 {
		return strings.Trim(match[1], `"'`)
	}
	// Some service-manager adapters expose a simple argv line rather than
	// systemd's rendered { path=...; argv[]=... } form.
	for _, line := range strings.Split(rendered, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ExecStart=") {
			fields := strings.Fields(strings.TrimPrefix(line, "ExecStart="))
			if len(fields) > 0 {
				return strings.Trim(fields[0], `"'`)
			}
		}
	}
	return ""
}

func parseConfigPath(environment string) string {
	match := configEnvPattern.FindStringSubmatch(environment)
	if len(match) == 2 {
		return strings.Trim(match[1], `"'`)
	}
	return ""
}

func expandHomePlaceholder(path string) string {
	path = strings.TrimSpace(path)
	if !strings.Contains(path, "%h") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return path
	}
	return strings.ReplaceAll(path, "%h", home)
}

func validateServiceState(snapshot systemdServiceSnapshot) verifierOutcome {
	if snapshot.Properties["ActiveState"] != "active" || snapshot.Properties["SubState"] != "running" {
		return verifierOutcome{Status: "failed", FailureBoundary: "CORE systemd service is not active and running"}
	}
	if result := strings.TrimSpace(snapshot.Properties["Result"]); result != "" && result != "success" {
		return verifierOutcome{Status: "failed", FailureBoundary: "CORE systemd service result is not successful"}
	}
	return verifierOutcome{}
}

func validateCanonicalUnitText(text string) verifierOutcome {
	checks := []string{
		"ExecStart=", "RENCROW_CONFIG=", "Restart=always", "StandardOutput=journal", "StandardError=journal",
	}
	for _, required := range checks {
		if !strings.Contains(text, required) {
			return verifierOutcome{Status: "failed", FailureBoundary: "canonical CORE systemd unit contract is incomplete"}
		}
	}
	return verifierOutcome{}
}

func validateProcessIdentity(snapshot systemdServiceSnapshot, options verifierOptions, deps verifierDependencies) verifierOutcome {
	deps = normalizeVerifierDependencies(deps)
	artifact := strings.TrimSpace(options.InstalledArtifact)
	if artifact == "" {
		artifact = snapshot.ExecPath
	}
	artifact = expandHomePlaceholder(artifact)
	if artifact == "" {
		return verifierOutcome{Status: "blocked", FailureBoundary: "installed CORE artifact input is missing"}
	}
	artifactInfo, err := os.Lstat(artifact)
	if err != nil || artifactInfo.Mode()&os.ModeSymlink != 0 || !artifactInfo.Mode().IsRegular() {
		return verifierOutcome{Status: "blocked", FailureBoundary: "installed CORE artifact is unavailable"}
	}
	if !sameCleanPath(artifact, snapshot.ExecPath) {
		return verifierOutcome{Status: "failed", FailureBoundary: "systemd executable does not match installed CORE artifact"}
	}
	observed, err := deps.Readlink(fmt.Sprintf("/proc/%d/exe", snapshot.MainPID))
	if err != nil || strings.TrimSpace(observed) == "" {
		return verifierOutcome{Status: "blocked", FailureBoundary: "CORE process executable observation is unavailable"}
	}
	if !sameCleanPath(observed, artifact) {
		return verifierOutcome{Status: "failed", FailureBoundary: "CORE process executable does not match systemd artifact"}
	}
	return verifierOutcome{Evidence: map[string]any{
		"main_pid":            snapshot.MainPID,
		"artifact_name":       safeBase(artifact),
		"systemd_exec_name":   safeBase(snapshot.ExecPath),
		"process_exec_name":   safeBase(observed),
		"artifact_size_bytes": artifactInfo.Size(),
	}}
}

func validateConfigIdentity(snapshot systemdServiceSnapshot, options verifierOptions) verifierOutcome {
	configPath := strings.TrimSpace(options.ConfigPath)
	if configPath == "" {
		configPath = snapshot.ConfigPath
	}
	configPath = expandHomePlaceholder(configPath)
	if configPath == "" {
		return verifierOutcome{Status: "blocked", FailureBoundary: "active CORE config input is missing"}
	}
	info, err := os.Lstat(configPath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return verifierOutcome{Status: "blocked", FailureBoundary: "active CORE config is unavailable"}
	}
	if snapshot.ConfigPath != "" && !sameCleanPath(configPath, snapshot.ConfigPath) {
		return verifierOutcome{Status: "failed", FailureBoundary: "systemd active config does not match supplied CORE config"}
	}
	return verifierOutcome{Evidence: map[string]any{"config_name": safeBase(configPath), "config_size_bytes": info.Size()}}
}

func sameCleanPath(left, right string) bool {
	left = expandHomePlaceholder(strings.TrimSpace(left))
	right = expandHomePlaceholder(strings.TrimSpace(right))
	if left == "" || right == "" {
		return false
	}
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return filepath.Clean(left) == filepath.Clean(right)
	}
	return filepath.Clean(leftAbs) == filepath.Clean(rightAbs)
}

func observeCoreListener(ctx context.Context, snapshot systemdServiceSnapshot, deps verifierDependencies) verifierOutcome {
	deps = normalizeVerifierDependencies(deps)
	result := deps.RunCommand(ctx, "ss", []string{"-ltnp"})
	if commandUnavailable(result) {
		return verifierOutcome{Status: "blocked", FailureBoundary: "CORE listener observation tool is unavailable"}
	}
	bind, ownerPID, foundPort := parseCoreListener(result.Stdout)
	if !foundPort {
		return verifierOutcome{Status: "blocked", FailureBoundary: "CORE listener on the reserved port is unavailable"}
	}
	if ownerPID != snapshot.MainPID {
		return verifierOutcome{Status: "failed", FailureBoundary: "CORE listener owner PID does not match systemd MainPID"}
	}
	return verifierOutcome{Status: "passed", Evidence: map[string]any{
		"listen_port":  canonicalCorePort,
		"bind_address": bind,
		"owner_pid":    ownerPID,
	}}
}

func parseCoreListener(output string) (string, int64, bool) {
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, ":18790") {
			continue
		}
		portMatch := listenerPortPattern.FindStringSubmatch(line)
		if len(portMatch) != 4 {
			continue
		}
		pidMatch := listenerPIDPattern.FindStringSubmatch(line)
		if len(pidMatch) != 2 {
			return "", 0, false
		}
		pid, err := strconv.ParseInt(pidMatch[1], 10, 64)
		if err != nil || pid <= 0 {
			return "", 0, false
		}
		bind := portMatch[1]
		if bind == "" {
			bind = portMatch[2]
		}
		return bind, pid, true
	}
	return "", 0, false
}

func runUnauthenticatedSecurityProbe(ctx context.Context, options verifierOptions, deps verifierDependencies) verifierOutcome {
	deps = normalizeVerifierDependencies(deps)
	baseURL, err := validateVerifierCoreURL(options.CoreURL)
	if err != nil {
		return verifierOutcome{Status: "blocked", FailureBoundary: "canonical CORE URL is not an allowed loopback endpoint"}
	}
	response := verifierHTTPJSON(ctx, deps.HTTPClient, http.MethodPost, coreEndpoint(baseURL, "/v1/agent/ops"), map[string]string{
		"Accept": "application/json", "Content-Type": "application/json",
	}, []byte(`{}`))
	evidence := map[string]any{"route": "/v1/agent/ops", "http_status": response.StatusCode}
	if response.Err != nil && response.StatusCode == 0 {
		return verifierOutcome{Status: "blocked", FailureBoundary: "canonical protected route is unavailable", Evidence: evidence}
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return verifierOutcome{Status: "passed", Evidence: evidence}
	}
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed {
		return verifierOutcome{Status: "blocked", FailureBoundary: "canonical protected route is unavailable", Evidence: evidence}
	}
	return verifierOutcome{Status: "failed", FailureBoundary: "protected Agent route accepted an unauthenticated request", Evidence: evidence}
}

func runRuntimeIdentityLifecycleSecurity(ctx context.Context, options verifierOptions, check manifestCheck, deps verifierDependencies) verifierOutcome {
	deps = normalizeVerifierDependencies(deps)
	base := requireLinuxSystemd(options, deps)
	if base.Status != "" {
		return base
	}
	snapshot, outcome := readSystemdService(ctx, options, deps)
	if outcome.Status != "" {
		return outcome
	}
	if outcome = validateServiceState(snapshot); outcome.Status != "" {
		return outcome
	}
	unitOutcome := validateCanonicalUnitText(snapshot.UnitText)
	if unitOutcome.Status != "" {
		return unitOutcome
	}
	identity := validateProcessIdentity(snapshot, options, deps)
	if identity.Status != "" && identity.Status != "passed" {
		return identity
	}
	config := validateConfigIdentity(snapshot, options)
	if config.Status != "" && config.Status != "passed" {
		return config
	}
	listener := observeCoreListener(ctx, snapshot, deps)
	if listener.Status != "passed" {
		return listener
	}
	security := runUnauthenticatedSecurityProbe(ctx, options, deps)
	if security.Status != "passed" {
		return security
	}
	evidence := mergeEvidence(identity.Evidence, config.Evidence, listener.Evidence, security.Evidence)
	evidence["active_state"] = snapshot.Properties["ActiveState"]
	evidence["sub_state"] = snapshot.Properties["SubState"]
	evidence["systemd_result"] = snapshot.Properties["Result"]
	evidence["restart_count"] = snapshot.Properties["NRestarts"]
	return verifierOutcome{Status: "passed", Evidence: evidence}
}

func mergeEvidence(values ...map[string]any) map[string]any {
	merged := make(map[string]any)
	for _, value := range values {
		for key, item := range value {
			merged[key] = item
		}
	}
	return merged
}

func runStartupPhaseTrace(ctx context.Context, options verifierOptions, check manifestCheck, deps verifierDependencies) verifierOutcome {
	deps = normalizeVerifierDependencies(deps)
	base := requireLinuxSystemd(options, deps)
	if base.Status != "" {
		return base
	}
	snapshot, outcome := readSystemdService(ctx, options, deps)
	if outcome.Status != "" {
		return outcome
	}
	if outcome = validateServiceState(snapshot); outcome.Status != "" {
		return outcome
	}
	identity := validateProcessIdentity(snapshot, options, deps)
	if identity.Status != "" && identity.Status != "passed" {
		return identity
	}
	config := validateConfigIdentity(snapshot, options)
	if config.Status != "" && config.Status != "passed" {
		return config
	}
	listener := observeCoreListener(ctx, snapshot, deps)
	if listener.Status != "passed" {
		return listener
	}
	since := options.JournalSince
	if since.IsZero() {
		since = options.ObservedAt.Add(-24 * time.Hour)
	}
	journal := deps.RunCommand(ctx, "journalctl", []string{
		"--user", "-u", canonicalCoreUnit,
		"--since", formatJournalctlTime(since),
		"--until", formatJournalctlTime(options.ObservedAt),
		"--no-pager", "--output=cat",
	})
	if commandUnavailable(journal) || strings.TrimSpace(journal.Stdout) == "" {
		return verifierOutcome{Status: "blocked", FailureBoundary: "CORE startup journal is unavailable"}
	}
	phases := make(map[string]int64)
	for _, match := range startupPhasePattern.FindAllStringSubmatch(journal.Stdout, -1) {
		elapsed, err := strconv.ParseInt(match[2], 10, 64)
		if err == nil {
			phases[match[1]] = elapsed
		}
	}
	required := []string{"config_load", "llm_gateway", "dependencies_total", "server_listen_ready", "startup_total"}
	for _, phase := range required {
		if _, ok := phases[phase]; !ok {
			return verifierOutcome{Status: "failed", FailureBoundary: "CORE startup phase trace is incomplete", Evidence: map[string]any{"phases_observed": sortedPhaseNames(phases)}}
		}
	}
	readiness := runReadiness(ctx, options, check, deps)
	if readiness.Status != "passed" {
		return verifierOutcome{Status: readiness.Status, FailureBoundary: readiness.FailureBoundary, Evidence: map[string]any{"startup_phases": phases}}
	}
	requestEvidence, requestOutcome := loadStartupRequestEvidence(options.RequestEvidence, options.ObservedAt)
	if requestOutcome.Status != "" {
		return requestOutcome
	}
	evidence := mergeEvidence(identity.Evidence, config.Evidence, listener.Evidence, map[string]any{
		"unit":           canonicalCoreUnit,
		"main_pid":       snapshot.MainPID,
		"startup_phases": phases,
		"journal_bytes":  len(journal.Stdout),
		"journal_sha256": sha256Text(journal.Stdout),
	}, readiness.Evidence, requestEvidence)
	return verifierOutcome{Status: "passed", Evidence: evidence}
}

// journalctl's accepted timestamp grammar is older than RFC3339 on some
// supported Linux hosts: the T separator and trailing Z are rejected. Keep
// the frozen UTC bounds while using journalctl's portable explicit-UTC form.
func formatJournalctlTime(value time.Time) string {
	return value.UTC().Format("2006-01-02 15:04:05 UTC")
}

func sortedPhaseNames(phases map[string]int64) []string {
	result := make([]string, 0, len(phases))
	for phase := range phases {
		result = append(result, phase)
	}
	for i := 1; i < len(result); i++ {
		for j := i; j > 0 && result[j] < result[j-1]; j-- {
			result[j], result[j-1] = result[j-1], result[j]
		}
	}
	return result
}
