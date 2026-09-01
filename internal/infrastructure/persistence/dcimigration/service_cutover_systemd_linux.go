//go:build linux

package dcimigration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	linuxSystemdCutoverUnit       = "rencrow.service"
	linuxSystemdSystemctlCommand  = "/usr/bin/systemctl"
	linuxSystemdSSCommand         = "/usr/bin/ss"
	linuxSystemdCutoverPort       = 18790
	linuxSystemdCutoverReadiness  = "http://127.0.0.1:18790/health/ready"
	linuxSystemdCommandTimeout    = 30 * time.Second
	linuxSystemdHTTPTimeout       = 2 * time.Second
	linuxSystemdPollInterval      = time.Second
	linuxSystemdRunningTimeout    = 300 * time.Second
	linuxSystemdStoppedTimeout    = 30 * time.Second
	linuxSystemdRunningPolls      = 301
	linuxSystemdStoppedPolls      = 31
	linuxSystemdMaxOutputBytes    = 64 << 10
	linuxSystemdMaxReadinessBytes = 64 << 10
)

var errLinuxSystemdOutputLimit = errors.New("bounded service-manager output limit exceeded")

var _ cutoverServiceManager = (*linuxSystemdCutoverServiceManager)(nil)

type linuxSystemdCommandResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Err      error
}

type linuxSystemdCommandRunner func(context.Context, string, []string) linuxSystemdCommandResult

type linuxSystemdHTTPResponse struct {
	StatusCode int
	Body       []byte
	Err        error
}

type linuxSystemdHTTPGetter func(context.Context, string) linuxSystemdHTTPResponse

type linuxSystemdCutoverDependencies struct {
	run          linuxSystemdCommandRunner
	readlink     func(string) (string, error)
	httpGet      linuxSystemdHTTPGetter
	sleep        func(context.Context, time.Duration) error
	runningPolls int
	stoppedPolls int
	pollInterval time.Duration
}

// linuxSystemdCutoverServiceManager is the Linux owner adapter for the fixed
// CORE user service.  It accepts only private artifact paths from a later
// resolver; unit, port, readiness URL, and every command remain constants.
// Unlike the read-only verifier, this boundary also owns the exact mask,
// unmask, and start command sequence used by D2d recovery.
type linuxSystemdCutoverServiceManager struct {
	installedRuntime string
	configPath       string
	run              linuxSystemdCommandRunner
	readlink         func(string) (string, error)
	httpGet          linuxSystemdHTTPGetter
	sleep            func(context.Context, time.Duration) error
	runningPolls     int
	stoppedPolls     int
	pollInterval     time.Duration
}

func newLinuxSystemdCutoverServiceManager(installedRuntime, configPath string) (*linuxSystemdCutoverServiceManager, error) {
	return newLinuxSystemdCutoverServiceManagerWithDeps(installedRuntime, configPath, linuxSystemdCutoverDependencies{pollInterval: -1})
}

func newLinuxSystemdCutoverServiceManagerWithDeps(installedRuntime, configPath string, deps linuxSystemdCutoverDependencies) (*linuxSystemdCutoverServiceManager, error) {
	runtimePath, err := absolutePath(installedRuntime)
	if err != nil {
		return nil, linuxSystemdCutoverError("invalid_options")
	}
	config, err := absolutePath(configPath)
	if err != nil {
		return nil, linuxSystemdCutoverError("invalid_options")
	}
	if strings.TrimSpace(runtimePath) == "" || strings.TrimSpace(config) == "" {
		return nil, linuxSystemdCutoverError("invalid_options")
	}
	if deps.run == nil {
		deps.run = runLinuxSystemdCommand
	}
	if deps.readlink == nil {
		deps.readlink = os.Readlink
	}
	if deps.httpGet == nil {
		deps.httpGet = getLinuxSystemdReadiness
	}
	if deps.sleep == nil {
		deps.sleep = sleepLinuxSystemdPoll
	}
	if deps.runningPolls <= 0 {
		deps.runningPolls = linuxSystemdRunningPolls
	} else if deps.runningPolls > linuxSystemdRunningPolls {
		deps.runningPolls = linuxSystemdRunningPolls
	}
	if deps.stoppedPolls <= 0 {
		deps.stoppedPolls = linuxSystemdStoppedPolls
	} else if deps.stoppedPolls > linuxSystemdStoppedPolls {
		deps.stoppedPolls = linuxSystemdStoppedPolls
	}
	if deps.pollInterval < 0 {
		deps.pollInterval = linuxSystemdPollInterval
	}
	return &linuxSystemdCutoverServiceManager{
		installedRuntime: runtimePath,
		configPath:       config,
		run:              deps.run,
		readlink:         deps.readlink,
		httpGet:          deps.httpGet,
		sleep:            deps.sleep,
		runningPolls:     deps.runningPolls,
		stoppedPolls:     deps.stoppedPolls,
		pollInterval:     deps.pollInterval,
	}, nil
}

func (manager *linuxSystemdCutoverServiceManager) VerifyRunning(ctx context.Context, expectedSHA256 string) (cutoverServiceRunningEvidence, error) {
	if ctx == nil {
		return cutoverServiceRunningEvidence{}, linuxSystemdCutoverError("invalid_context")
	}
	if !isLowerHexSHA256(expectedSHA256) {
		return cutoverServiceRunningEvidence{}, linuxSystemdCutoverError("invalid_runtime")
	}
	verificationContext, cancel := context.WithTimeout(ctx, linuxSystemdRunningTimeout)
	defer cancel()
	for attempt := 0; attempt < manager.runningPolls; attempt++ {
		evidence, err, retry := manager.verifyRunningOnce(verificationContext, expectedSHA256)
		if err == nil {
			return evidence, nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return cutoverServiceRunningEvidence{}, err
		}
		if !retry || attempt+1 >= manager.runningPolls {
			return cutoverServiceRunningEvidence{}, err
		}
		if manager.sleep == nil {
			return cutoverServiceRunningEvidence{}, linuxSystemdCutoverError("service_poll")
		}
		if err := manager.sleep(verificationContext, manager.pollInterval); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return cutoverServiceRunningEvidence{}, err
			}
			return cutoverServiceRunningEvidence{}, linuxSystemdCutoverError("service_poll")
		}
	}
	return cutoverServiceRunningEvidence{}, linuxSystemdCutoverError("service_running")
}

func (manager *linuxSystemdCutoverServiceManager) VerifyMaintenanceStopped(ctx context.Context, expectedSHA256 string) (cutoverServiceMaintenanceStoppedEvidence, error) {
	if ctx == nil {
		return cutoverServiceMaintenanceStoppedEvidence{}, linuxSystemdCutoverError("invalid_context")
	}
	if !isLowerHexSHA256(expectedSHA256) {
		return cutoverServiceMaintenanceStoppedEvidence{}, linuxSystemdCutoverError("invalid_runtime")
	}
	verificationContext, cancel := context.WithTimeout(ctx, linuxSystemdStoppedTimeout)
	defer cancel()
	for attempt := 0; attempt < manager.stoppedPolls; attempt++ {
		evidence, err, retry := manager.verifyMaintenanceStoppedOnce(verificationContext, expectedSHA256)
		if err == nil {
			return evidence, nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return cutoverServiceMaintenanceStoppedEvidence{}, err
		}
		if !retry || attempt+1 >= manager.stoppedPolls {
			return cutoverServiceMaintenanceStoppedEvidence{}, err
		}
		if manager.sleep == nil {
			return cutoverServiceMaintenanceStoppedEvidence{}, linuxSystemdCutoverError("service_poll")
		}
		if err := manager.sleep(verificationContext, manager.pollInterval); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return cutoverServiceMaintenanceStoppedEvidence{}, err
			}
			return cutoverServiceMaintenanceStoppedEvidence{}, linuxSystemdCutoverError("service_poll")
		}
	}
	return cutoverServiceMaintenanceStoppedEvidence{}, linuxSystemdCutoverError("service_maintenance_stopped")
}

func (manager *linuxSystemdCutoverServiceManager) VerifyStopped(ctx context.Context) (cutoverServiceStoppedEvidence, error) {
	if ctx == nil {
		return cutoverServiceStoppedEvidence{}, linuxSystemdCutoverError("invalid_context")
	}
	verificationContext, cancel := context.WithTimeout(ctx, linuxSystemdStoppedTimeout)
	defer cancel()
	for attempt := 0; attempt < manager.stoppedPolls; attempt++ {
		evidence, err, retry := manager.verifyStoppedOnce(verificationContext)
		if err == nil {
			return evidence, nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return cutoverServiceStoppedEvidence{}, err
		}
		if !retry || attempt+1 >= manager.stoppedPolls {
			return cutoverServiceStoppedEvidence{}, err
		}
		if manager.sleep == nil {
			return cutoverServiceStoppedEvidence{}, linuxSystemdCutoverError("service_poll")
		}
		if err := manager.sleep(verificationContext, manager.pollInterval); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return cutoverServiceStoppedEvidence{}, err
			}
			return cutoverServiceStoppedEvidence{}, linuxSystemdCutoverError("service_poll")
		}
	}
	return cutoverServiceStoppedEvidence{}, linuxSystemdCutoverError("service_stopped")
}

func (manager *linuxSystemdCutoverServiceManager) MaskAndStop(ctx context.Context) error {
	_, err := manager.runSystemctl(ctx, []string{"--user", "mask", "--runtime", "--now", linuxSystemdCutoverUnit}, 0)
	return err
}

func (manager *linuxSystemdCutoverServiceManager) UnmaskAndStart(ctx context.Context) error {
	if _, err := manager.runSystemctl(ctx, []string{"--user", "unmask", "--runtime", linuxSystemdCutoverUnit}, 0); err != nil {
		return err
	}
	_, err := manager.runSystemctl(ctx, []string{"--user", "start", linuxSystemdCutoverUnit}, 0)
	return err
}

func (manager *linuxSystemdCutoverServiceManager) verifyRunningOnce(ctx context.Context, expectedSHA256 string) (cutoverServiceRunningEvidence, error, bool) {
	if ctx == nil {
		return cutoverServiceRunningEvidence{}, linuxSystemdCutoverError("invalid_context"), false
	}
	if err := ctx.Err(); err != nil {
		return cutoverServiceRunningEvidence{}, err, false
	}
	properties, err := manager.systemdProperties(ctx)
	if err != nil {
		return cutoverServiceRunningEvidence{}, err, false
	}
	if properties["LoadState"] != "loaded" || properties["UnitFileState"] != "enabled" {
		return cutoverServiceRunningEvidence{}, linuxSystemdCutoverError("service_running"), false
	}
	if err := manager.requireSystemdEnabled(ctx, "enabled"); err != nil {
		return cutoverServiceRunningEvidence{}, err, false
	}
	if properties["ActiveState"] != "active" || properties["SubState"] != "running" {
		return cutoverServiceRunningEvidence{}, linuxSystemdCutoverRetryError("service_running"), true
	}
	pid, err := parseLinuxSystemdPID(properties["MainPID"], true)
	if err != nil {
		return cutoverServiceRunningEvidence{}, linuxSystemdCutoverRetryError("service_running"), true
	}
	installedRuntime, err := manager.validateEnabledServiceIdentity(properties, expectedSHA256)
	if err != nil {
		return cutoverServiceRunningEvidence{}, err, false
	}
	if manager.readlink == nil {
		return cutoverServiceRunningEvidence{}, linuxSystemdCutoverError("process_identity"), false
	}
	observedExe, err := manager.readlink(filepath.Join("/proc", strconv.FormatInt(pid, 10), "exe"))
	if err != nil || strings.Contains(observedExe, "(deleted)") {
		return cutoverServiceRunningEvidence{}, linuxSystemdCutoverError("process_identity"), false
	}
	observedExe, err = absolutePath(strings.TrimSpace(observedExe))
	if err != nil || !samePath(observedExe, installedRuntime) {
		return cutoverServiceRunningEvidence{}, linuxSystemdCutoverError("process_identity"), false
	}
	listenerPID, listenerFound, listenerErr := manager.listenerPID(ctx)
	if listenerErr != nil {
		return cutoverServiceRunningEvidence{}, listenerErr, false
	}
	if !listenerFound {
		return cutoverServiceRunningEvidence{}, linuxSystemdCutoverRetryError("listener"), true
	}
	if listenerPID != pid {
		return cutoverServiceRunningEvidence{}, linuxSystemdCutoverError("listener"), false
	}
	if err, retry := manager.validateReadiness(ctx); err != nil {
		return cutoverServiceRunningEvidence{}, err, retry
	}
	if err := ctx.Err(); err != nil {
		return cutoverServiceRunningEvidence{}, err, false
	}
	return cutoverServiceRunningEvidence{
		Owner:           1,
		Enabled:         1,
		Unmasked:        1,
		Active:          1,
		MainPIDPositive: 1,
		ListenerOwned:   1,
		Readiness:       1,
		RuntimeSHA256:   expectedSHA256,
	}, nil, false
}

func (manager *linuxSystemdCutoverServiceManager) verifyMaintenanceStoppedOnce(ctx context.Context, expectedSHA256 string) (cutoverServiceMaintenanceStoppedEvidence, error, bool) {
	if ctx == nil {
		return cutoverServiceMaintenanceStoppedEvidence{}, linuxSystemdCutoverError("invalid_context"), false
	}
	if err := ctx.Err(); err != nil {
		return cutoverServiceMaintenanceStoppedEvidence{}, err, false
	}
	properties, err := manager.systemdProperties(ctx)
	if err != nil {
		return cutoverServiceMaintenanceStoppedEvidence{}, err, false
	}
	if properties["LoadState"] != "loaded" || properties["UnitFileState"] != "enabled" {
		return cutoverServiceMaintenanceStoppedEvidence{}, linuxSystemdCutoverError("service_maintenance_stopped"), false
	}
	if err := manager.requireSystemdEnabled(ctx, "enabled"); err != nil {
		return cutoverServiceMaintenanceStoppedEvidence{}, err, false
	}
	if properties["ActiveState"] != "inactive" || properties["SubState"] != "dead" {
		return cutoverServiceMaintenanceStoppedEvidence{}, linuxSystemdCutoverRetryError("service_maintenance_stopped"), true
	}
	if _, err := parseLinuxSystemdPID(properties["MainPID"], false); err != nil {
		return cutoverServiceMaintenanceStoppedEvidence{}, linuxSystemdCutoverRetryError("service_maintenance_stopped"), true
	}
	if _, err := manager.validateEnabledServiceIdentity(properties, expectedSHA256); err != nil {
		return cutoverServiceMaintenanceStoppedEvidence{}, err, false
	}
	_, found, err := manager.listenerPID(ctx)
	if err != nil {
		return cutoverServiceMaintenanceStoppedEvidence{}, err, false
	}
	if found {
		return cutoverServiceMaintenanceStoppedEvidence{}, linuxSystemdCutoverRetryError("listener"), true
	}
	if err := ctx.Err(); err != nil {
		return cutoverServiceMaintenanceStoppedEvidence{}, err, false
	}
	return cutoverServiceMaintenanceStoppedEvidence{
		Owner: 1, Enabled: 1, Unmasked: 1, Active: 0, MainPIDZero: 1,
		ListenerZero: 1, RuntimeSHA256: expectedSHA256,
	}, nil, false
}

func (manager *linuxSystemdCutoverServiceManager) validateEnabledServiceIdentity(properties map[string]string, expectedSHA256 string) (string, error) {
	installedRuntime, err := manager.validateInstalledRuntime(expectedSHA256)
	if err != nil {
		return "", err
	}
	execStart, err := parseLinuxSystemdExecStartDetails(properties["ExecStart"])
	if err != nil || !samePath(execStart.path, installedRuntime) || len(execStart.argv) != 2 || !samePath(execStart.argv[0], installedRuntime) || execStart.argv[1] != "run" {
		return "", linuxSystemdCutoverError("service_identity")
	}
	configPath, err := parseLinuxSystemdConfigPath(properties["Environment"])
	if err != nil || !samePath(configPath, manager.configPath) {
		return "", linuxSystemdCutoverError("service_identity")
	}
	if err := manager.validateConfig(); err != nil {
		return "", err
	}
	return installedRuntime, nil
}

func (manager *linuxSystemdCutoverServiceManager) verifyStoppedOnce(ctx context.Context) (cutoverServiceStoppedEvidence, error, bool) {
	if ctx == nil {
		return cutoverServiceStoppedEvidence{}, linuxSystemdCutoverError("invalid_context"), false
	}
	if err := ctx.Err(); err != nil {
		return cutoverServiceStoppedEvidence{}, err, false
	}
	properties, err := manager.systemdProperties(ctx)
	if err != nil {
		return cutoverServiceStoppedEvidence{}, err, false
	}
	if properties["LoadState"] != "masked" {
		return cutoverServiceStoppedEvidence{}, linuxSystemdCutoverError("service_stopped"), false
	}
	if properties["UnitFileState"] != "masked-runtime" {
		return cutoverServiceStoppedEvidence{}, linuxSystemdCutoverError("service_stopped"), false
	}
	if err := manager.requireSystemdEnabled(ctx, "masked-runtime"); err != nil {
		return cutoverServiceStoppedEvidence{}, err, false
	}
	if properties["ActiveState"] != "inactive" || properties["SubState"] != "dead" {
		return cutoverServiceStoppedEvidence{}, linuxSystemdCutoverRetryError("service_stopped"), true
	}
	if _, err := parseLinuxSystemdPID(properties["MainPID"], false); err != nil {
		return cutoverServiceStoppedEvidence{}, linuxSystemdCutoverRetryError("service_stopped"), true
	}
	_, found, err := manager.listenerPID(ctx)
	if err != nil {
		return cutoverServiceStoppedEvidence{}, err, false
	}
	if found {
		return cutoverServiceStoppedEvidence{}, linuxSystemdCutoverRetryError("listener"), true
	}
	if err := ctx.Err(); err != nil {
		return cutoverServiceStoppedEvidence{}, err, false
	}
	return cutoverServiceStoppedEvidence{Masked: 1, Active: 0, MainPIDZero: 1, ListenerZero: 1}, nil, false
}

func (manager *linuxSystemdCutoverServiceManager) systemdProperties(ctx context.Context) (map[string]string, error) {
	result, err := manager.runSystemctl(ctx, []string{
		"--user", "show", linuxSystemdCutoverUnit,
		"-p", "LoadState", "-p", "UnitFileState", "-p", "ActiveState", "-p", "SubState",
		"-p", "MainPID", "-p", "ExecStart", "-p", "Environment",
	}, 0)
	if err != nil {
		return nil, err
	}
	properties, err := parseLinuxSystemdProperties(result.Stdout)
	if err != nil {
		return nil, linuxSystemdCutoverError("service_evidence")
	}
	return properties, nil
}

func (manager *linuxSystemdCutoverServiceManager) requireSystemdEnabled(ctx context.Context, expected string) error {
	var (
		result linuxSystemdCommandResult
		err    error
	)
	switch expected {
	case "enabled":
		result, err = manager.runSystemctl(ctx, []string{"--user", "is-enabled", linuxSystemdCutoverUnit}, 0)
	case "masked-runtime":
		// A masked unit reports a non-zero is-enabled status.  The exact
		// positive value is implementation-defined; stdout remains authoritative.
		result, err = manager.runSystemctlMaskedState(ctx, []string{"--user", "is-enabled", linuxSystemdCutoverUnit})
	default:
		return linuxSystemdCutoverError("service_state")
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(result.Stdout) != expected {
		return linuxSystemdCutoverError("service_state")
	}
	if expected == "enabled" && result.ExitCode != 0 {
		return linuxSystemdCutoverError("service_state")
	}
	if expected == "masked-runtime" && result.ExitCode <= 0 {
		return linuxSystemdCutoverError("service_state")
	}
	return nil
}

func (manager *linuxSystemdCutoverServiceManager) validateInstalledRuntime(expectedSHA256 string) (string, error) {
	path, err := resolveCutoverExistingPath(manager.installedRuntime)
	if err != nil || !samePath(path, manager.installedRuntime) {
		return "", linuxSystemdCutoverError("runtime_identity")
	}
	binding, err := bindCutoverFile(path, false, false)
	if err != nil || binding.sha256 != expectedSHA256 {
		return "", linuxSystemdCutoverError("runtime_identity")
	}
	if err := validateCutoverRuntimeBinding(binding); err != nil {
		return "", linuxSystemdCutoverError("runtime_identity")
	}
	return path, nil
}

func (manager *linuxSystemdCutoverServiceManager) validateConfig() error {
	path, err := resolveCutoverExistingPath(manager.configPath)
	if err != nil || !samePath(path, manager.configPath) {
		return linuxSystemdCutoverError("config_identity")
	}
	return nil
}

func (manager *linuxSystemdCutoverServiceManager) listenerPID(ctx context.Context) (int64, bool, error) {
	result, err := manager.runSS(ctx, []string{"-ltnp"})
	if err != nil {
		return 0, false, err
	}
	found, pid, parseErr := parseLinuxSystemdListener(result.Stdout)
	if parseErr != nil {
		return 0, false, linuxSystemdCutoverError("listener")
	}
	return pid, found, nil
}

func (manager *linuxSystemdCutoverServiceManager) runSystemctl(ctx context.Context, args []string, allowedExitCode int) (linuxSystemdCommandResult, error) {
	return runLinuxSystemdFixedCommand(ctx, manager.run, linuxSystemdSystemctlCommand, args, allowedExitCode, false)
}

func (manager *linuxSystemdCutoverServiceManager) runSS(ctx context.Context, args []string) (linuxSystemdCommandResult, error) {
	return runLinuxSystemdFixedCommand(ctx, manager.run, linuxSystemdSSCommand, args, 0, false)
}

func (manager *linuxSystemdCutoverServiceManager) runSystemctlMaskedState(ctx context.Context, args []string) (linuxSystemdCommandResult, error) {
	return runLinuxSystemdFixedCommand(ctx, manager.run, linuxSystemdSystemctlCommand, args, 0, true)
}

func runLinuxSystemdFixedCommand(ctx context.Context, runner linuxSystemdCommandRunner, name string, args []string, allowedExitCode int, allowPositiveExit bool) (linuxSystemdCommandResult, error) {
	if ctx == nil {
		return linuxSystemdCommandResult{}, linuxSystemdCutoverError("invalid_context")
	}
	if err := ctx.Err(); err != nil {
		return linuxSystemdCommandResult{}, err
	}
	if runner == nil {
		return linuxSystemdCommandResult{}, linuxSystemdCutoverError("service_command")
	}
	result := runner(ctx, name, append([]string(nil), args...))
	if result.Err != nil {
		if errors.Is(result.Err, context.Canceled) || errors.Is(result.Err, context.DeadlineExceeded) {
			return linuxSystemdCommandResult{}, result.Err
		}
		return linuxSystemdCommandResult{}, linuxSystemdCutoverError("service_command")
	}
	acceptedExitCode := result.ExitCode == allowedExitCode || (allowPositiveExit && result.ExitCode > 0)
	if !acceptedExitCode || len([]byte(result.Stdout)) > linuxSystemdMaxOutputBytes || len([]byte(result.Stderr)) > linuxSystemdMaxOutputBytes {
		return linuxSystemdCommandResult{}, linuxSystemdCutoverError("service_command")
	}
	return result, nil
}

func parseLinuxSystemdProperties(output string) (map[string]string, error) {
	properties := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(output))
	seen := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		separator := strings.IndexByte(line, '=')
		if separator <= 0 {
			return nil, errors.New("systemd property line is malformed")
		}
		key := line[:separator]
		if _, exists := properties[key]; exists {
			return nil, errors.New("systemd property is duplicated")
		}
		properties[key] = line[separator+1:]
		seen = true
	}
	if err := scanner.Err(); err != nil || !seen {
		return nil, errors.New("systemd properties are missing")
	}
	return properties, nil
}

func parseLinuxSystemdPID(value string, positive bool) (int64, error) {
	pid, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || pid < 0 || (positive && pid <= 0) || (!positive && pid != 0) {
		return 0, errors.New("systemd PID is invalid")
	}
	return pid, nil
}

type linuxSystemdExecStart struct {
	path string
	argv []string
}

func parseLinuxSystemdExecStart(value string) (string, error) {
	execStart, err := parseLinuxSystemdExecStartDetails(value)
	if err != nil {
		return "", err
	}
	return execStart.path, nil
}

func parseLinuxSystemdExecStartDetails(value string) (linuxSystemdExecStart, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "ExecStart=") {
		value = strings.TrimSpace(strings.TrimPrefix(value, "ExecStart="))
	}
	if len(value) < 2 || value[0] != '{' || value[len(value)-1] != '}' {
		return linuxSystemdExecStart{}, errors.New("rendered ExecStart is missing")
	}
	fields, err := splitLinuxSystemdExecStartFields(value[1 : len(value)-1])
	if err != nil {
		return linuxSystemdExecStart{}, err
	}
	var result linuxSystemdExecStart
	pathCount, argvCount := 0, 0
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		switch {
		case strings.HasPrefix(field, "path="):
			pathCount++
			if pathCount > 1 {
				return linuxSystemdExecStart{}, errors.New("rendered ExecStart path is ambiguous")
			}
			path, err := parseLinuxSystemdSingleValue(strings.TrimPrefix(field, "path="))
			if err != nil {
				return linuxSystemdExecStart{}, errors.New("rendered ExecStart path is invalid")
			}
			result.path = path
		case strings.HasPrefix(field, "argv[]="):
			argvCount++
			if argvCount > 1 {
				return linuxSystemdExecStart{}, errors.New("rendered ExecStart argv is ambiguous")
			}
			argv, err := parseLinuxSystemdArgv(strings.TrimPrefix(field, "argv[]="))
			if err != nil || len(argv) != 2 || argv[1] != "run" {
				return linuxSystemdExecStart{}, errors.New("rendered ExecStart argv is invalid")
			}
			result.argv = argv
		}
	}
	if pathCount != 1 || result.path == "" || argvCount != 1 || len(result.argv) != 2 {
		return linuxSystemdExecStart{}, errors.New("rendered ExecStart is incomplete")
	}
	return result, nil
}

func splitLinuxSystemdExecStartFields(value string) ([]string, error) {
	fields := make([]string, 0, 4)
	start := 0
	quoted := false
	escaped := false
	for index := 0; index < len(value); index++ {
		current := value[index]
		if escaped {
			escaped = false
			continue
		}
		if quoted && current == '\\' {
			escaped = true
			continue
		}
		if current == '"' {
			quoted = !quoted
			continue
		}
		if current == ';' && !quoted {
			fields = append(fields, value[start:index])
			start = index + 1
		}
	}
	if quoted || escaped {
		return nil, errors.New("rendered ExecStart quoting is invalid")
	}
	fields = append(fields, value[start:])
	return fields, nil
}

func parseLinuxSystemdSingleValue(value string) (string, error) {
	values, err := parseLinuxSystemdArgv(value)
	if err != nil || len(values) != 1 || values[0] == "" {
		return "", errors.New("systemd value is invalid")
	}
	return values[0], nil
}

func parseLinuxSystemdArgv(value string) ([]string, error) {
	arguments := make([]string, 0, 2)
	for index := 0; index < len(value); {
		for index < len(value) && (value[index] == ' ' || value[index] == '\t' || value[index] == '\n' || value[index] == '\r') {
			index++
		}
		if index >= len(value) {
			break
		}
		var token strings.Builder
		quoted := false
		escaped := false
		for index < len(value) {
			current := value[index]
			if escaped {
				token.WriteByte(current)
				escaped = false
				index++
				continue
			}
			if current == '\\' {
				escaped = true
				index++
				continue
			}
			if current == '"' {
				quoted = !quoted
				index++
				continue
			}
			if !quoted && (current == ' ' || current == '\t' || current == '\n' || current == '\r') {
				break
			}
			token.WriteByte(current)
			index++
		}
		if escaped || quoted {
			return nil, errors.New("systemd argv quoting is invalid")
		}
		if token.Len() == 0 {
			return nil, errors.New("systemd argv value is empty")
		}
		arguments = append(arguments, token.String())
	}
	return arguments, nil
}

func parseLinuxSystemdConfigPath(environment string) (string, error) {
	paths := make([]string, 0, 1)
	for offset := 0; offset < len(environment); {
		relative := strings.Index(environment[offset:], "RENCROW_CONFIG=")
		if relative < 0 {
			break
		}
		start := offset + relative
		if start > 0 && !isLinuxSystemdTokenBoundary(environment[start-1]) {
			offset = start + len("RENCROW_CONFIG=")
			continue
		}
		path, end := linuxSystemdValueToken(environment, start+len("RENCROW_CONFIG="))
		if path == "" {
			return "", errors.New("RENCROW_CONFIG is empty")
		}
		paths = append(paths, path)
		offset = end
	}
	if len(paths) != 1 {
		return "", errors.New("RENCROW_CONFIG is missing or ambiguous")
	}
	return paths[0], nil
}

func isLinuxSystemdTokenBoundary(value byte) bool {
	return value == ' ' || value == '\t' || value == '\n' || value == '{' || value == '=' || value == ';'
}

func linuxSystemdValueToken(source string, start int) (string, int) {
	if start >= len(source) {
		return "", start
	}
	index := start
	for index < len(source) && (source[index] == ' ' || source[index] == '\t') {
		index++
	}
	quoted := index < len(source) && source[index] == '"'
	if quoted {
		index++
	}
	begin := index
	for index < len(source) {
		if quoted {
			if source[index] == '\\' && index+1 < len(source) {
				index += 2
				continue
			}
			if source[index] == '"' {
				return source[begin:index], index + 1
			}
			index++
			continue
		}
		switch source[index] {
		case ';', ' ', '\t', '\n', '}':
			return strings.Trim(source[begin:index], `"'`), index
		}
		index++
	}
	if quoted {
		return "", index
	}
	return strings.Trim(source[begin:index], `"'`), index
}

func parseLinuxSystemdListener(output string) (bool, int64, error) {
	var found bool
	var foundPID int64
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		stateIndex := -1
		for index, field := range fields {
			if field == "LISTEN" {
				stateIndex = index
				break
			}
		}
		if stateIndex < 0 {
			continue
		}
		localIndex := stateIndex + 3
		if localIndex >= len(fields) {
			return false, 0, errors.New("listener row is incomplete")
		}
		port, err := parseLinuxSystemdLocalPort(fields[localIndex])
		if err != nil {
			return false, 0, err
		}
		if port != linuxSystemdCutoverPort {
			continue
		}
		if found {
			return false, 0, errors.New("reserved listener is duplicated")
		}
		pids, err := parseLinuxSystemdPIDs(line)
		if err != nil || len(pids) != 1 {
			return false, 0, errors.New("reserved listener owner is ambiguous")
		}
		found = true
		foundPID = pids[0]
	}
	return found, foundPID, nil
}

func parseLinuxSystemdLocalPort(value string) (int, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "[") {
		closeBracket := strings.IndexByte(value, ']')
		if closeBracket < 0 || closeBracket+1 >= len(value) || value[closeBracket+1] != ':' {
			return 0, errors.New("listener address is malformed")
		}
		value = value[closeBracket+2:]
	} else {
		separator := strings.LastIndexByte(value, ':')
		if separator < 0 || separator+1 >= len(value) {
			return 0, errors.New("listener address is malformed")
		}
		value = value[separator+1:]
	}
	port, err := strconv.Atoi(value)
	if err != nil || port <= 0 || port > 65535 {
		return 0, errors.New("listener port is malformed")
	}
	return port, nil
}

func parseLinuxSystemdPIDs(line string) ([]int64, error) {
	pids := make([]int64, 0, 1)
	for offset := 0; offset < len(line); {
		relative := strings.Index(line[offset:], "pid=")
		if relative < 0 {
			break
		}
		start := offset + relative + len("pid=")
		end := start
		for end < len(line) && line[end] >= '0' && line[end] <= '9' {
			end++
		}
		if end == start {
			return nil, errors.New("listener PID is malformed")
		}
		pid, err := strconv.ParseInt(line[start:end], 10, 64)
		if err != nil || pid <= 0 {
			return nil, errors.New("listener PID is invalid")
		}
		pids = append(pids, pid)
		offset = end
	}
	return pids, nil
}

func (manager *linuxSystemdCutoverServiceManager) validateReadiness(ctx context.Context) (error, bool) {
	if ctx == nil {
		return linuxSystemdCutoverError("invalid_context"), false
	}
	if err := ctx.Err(); err != nil {
		return err, false
	}
	if manager.httpGet == nil {
		return linuxSystemdCutoverError("readiness"), false
	}
	response := manager.httpGet(ctx, linuxSystemdCutoverReadiness)
	if len(response.Body) > linuxSystemdMaxReadinessBytes {
		return linuxSystemdCutoverError("readiness"), false
	}
	if response.Err != nil {
		if errors.Is(response.Err, context.Canceled) || errors.Is(response.Err, context.DeadlineExceeded) {
			return response.Err, false
		}
		return linuxSystemdCutoverError("readiness"), true
	}
	if err := ctx.Err(); err != nil {
		return err, false
	}
	if response.StatusCode == http.StatusServiceUnavailable {
		return linuxSystemdCutoverRetryError("readiness"), true
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return linuxSystemdCutoverError("readiness"), false
	}
	if rejectDuplicateJSONKeys(response.Body) != nil {
		return linuxSystemdCutoverError("readiness"), false
	}
	var payload struct {
		OK      *bool  `json:"ok"`
		Status  string `json:"status"`
		Service string `json:"service"`
		Runtime string `json:"runtime"`
		Ready   *bool  `json:"ready"`
	}
	decoder := json.NewDecoder(bytes.NewReader(response.Body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return linuxSystemdCutoverError("readiness"), false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return linuxSystemdCutoverError("readiness"), false
	}
	if payload.OK == nil || !*payload.OK ||
		payload.Status != "ready" ||
		payload.Service != "rencrow-core" ||
		payload.Runtime != "go" ||
		payload.Ready == nil || !*payload.Ready {
		return linuxSystemdCutoverError("readiness"), false
	}
	return nil, false
}

func runLinuxSystemdCommand(ctx context.Context, name string, args []string) linuxSystemdCommandResult {
	if ctx == nil {
		return linuxSystemdCommandResult{Err: linuxSystemdCutoverError("invalid_context")}
	}
	if err := ctx.Err(); err != nil {
		return linuxSystemdCommandResult{Err: err}
	}
	commandContext, cancel := context.WithTimeout(ctx, linuxSystemdCommandTimeout)
	defer cancel()
	command := exec.CommandContext(commandContext, name, args...)
	stdout := &linuxSystemdBoundedBuffer{limit: linuxSystemdMaxOutputBytes}
	stderr := &linuxSystemdBoundedBuffer{limit: linuxSystemdMaxOutputBytes}
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return linuxSystemdCommandResult{Err: ctxErr}
	}
	if stdout.overflow || stderr.overflow {
		return linuxSystemdCommandResult{Err: errors.New("systemd command output is oversized")}
	}
	result := linuxSystemdCommandResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		} else {
			result.Err = err
			result.ExitCode = -1
		}
	}
	return result
}

type linuxSystemdBoundedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (buffer *linuxSystemdBoundedBuffer) Write(value []byte) (int, error) {
	if len(value) == 0 {
		return 0, nil
	}
	remaining := buffer.limit - buffer.Len()
	if remaining <= 0 {
		buffer.overflow = true
		return 0, errLinuxSystemdOutputLimit
	}
	if len(value) > remaining {
		_, _ = buffer.Buffer.Write(value[:remaining])
		buffer.overflow = true
		return remaining, errLinuxSystemdOutputLimit
	}
	return buffer.Buffer.Write(value)
}

func getLinuxSystemdReadiness(ctx context.Context, endpoint string) linuxSystemdHTTPResponse {
	if ctx == nil {
		return linuxSystemdHTTPResponse{Err: linuxSystemdCutoverError("invalid_context")}
	}
	if endpoint != linuxSystemdCutoverReadiness {
		return linuxSystemdHTTPResponse{Err: linuxSystemdCutoverError("readiness")}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return linuxSystemdHTTPResponse{Err: linuxSystemdCutoverError("readiness")}
	}
	client := &http.Client{
		Timeout: linuxSystemdHTTPTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return linuxSystemdHTTPResponse{Err: err}
		}
		return linuxSystemdHTTPResponse{Err: linuxSystemdCutoverError("readiness")}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, linuxSystemdMaxReadinessBytes+1))
	if err != nil {
		return linuxSystemdHTTPResponse{StatusCode: response.StatusCode, Err: linuxSystemdCutoverError("readiness")}
	}
	if len(body) > linuxSystemdMaxReadinessBytes {
		return linuxSystemdHTTPResponse{StatusCode: response.StatusCode, Body: body, Err: linuxSystemdCutoverError("readiness")}
	}
	return linuxSystemdHTTPResponse{StatusCode: response.StatusCode, Body: body}
}

func sleepLinuxSystemdPoll(ctx context.Context, duration time.Duration) error {
	if ctx == nil {
		return linuxSystemdCutoverError("invalid_context")
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type linuxSystemdRetryableError struct{ code string }

func (err *linuxSystemdRetryableError) Error() string {
	return "canonical CORE service evidence is transient"
}

func (err *linuxSystemdRetryableError) Unwrap() error {
	return linuxSystemdCutoverError(err.code)
}

func linuxSystemdCutoverRetryError(code string) error {
	if !validErrorCode(code) {
		code = "service_poll"
	}
	return &linuxSystemdRetryableError{code: code}
}

func linuxSystemdCutoverError(code string) error {
	if !validErrorCode(code) {
		code = "service_cutover"
	}
	return newCodedError(code, "canonical CORE service manager operation failed")
}
