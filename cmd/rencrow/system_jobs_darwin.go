//go:build darwin

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
)

const (
	systemJobsCommandTimeout     = 2 * time.Second
	systemJobsCommandOutputLimit = 128 * 1024
	systemJobsPlistOutputLimit   = systemJobsMaxPlistJSONBytes
	systemJobsPlutilPath         = "/usr/bin/plutil"
)

var systemJobsLaunchdPathPattern = regexp.MustCompile(`(?m)^\s*path\s*=\s*(.+?)\s*$`)
var systemJobsLaunchdStatePattern = regexp.MustCompile(`(?m)^[\t ]*state[\t ]*=[\t ]*(.*?)[\t ]*\r?$`)

type darwinSystemJobsReader struct{}

func newSystemJobsReader() viewer.SystemJobsReader {
	return darwinSystemJobsReader{}
}

func (darwinSystemJobsReader) ReadSystemJobs(ctx context.Context) (viewer.SystemJobsSnapshot, error) {
	boundedContext, cancel := context.WithTimeout(ctx, systemJobsSnapshotTimeout)
	defer cancel()
	roots := darwinSystemJobsRoots()
	return collectSystemJobs(
		boundedContext,
		roots,
		readBoundedSystemJobsPlist,
		convertSystemJobsPlistToJSON,
		darwinSystemJobsObserver{},
	), nil
}

func darwinSystemJobsRoots() []systemJobsRoot {
	uid := os.Getuid()
	domain := "user/" + strconv.Itoa(uid)
	account, err := user.LookupId(strconv.Itoa(uid))
	home := ""
	if err == nil && filepath.IsAbs(account.HomeDir) && filepath.Clean(account.HomeDir) == account.HomeDir {
		home = account.HomeDir
	}
	systemPlistPath, err := canonicalResilienceStackPlistPathForHome(resilienceStackDomain, "com.rencrow.core", "")
	systemPath := ""
	if err == nil {
		systemPath = filepath.Dir(systemPlistPath)
	}
	launchAgentsPath, rencrowLaunchdPath := "", ""
	if home != "" {
		if uid == 501 {
			if path, pathErr := canonicalResilienceStackPlistPathForHome(resilienceStackUser501, "com.rencrow.core", home); pathErr == nil {
				launchAgentsPath = filepath.Dir(path)
			}
			if path, pathErr := canonicalResilienceStackPlistPathForHome(resilienceStackUser501, "com.rencrow.portal", home); pathErr == nil {
				rencrowLaunchdPath = filepath.Dir(path)
			}
		} else {
			launchAgentsPath = filepath.Join(home, "Library", "LaunchAgents")
			rencrowLaunchdPath = filepath.Join(home, ".rencrow", "launchd")
		}
	}
	return []systemJobsRoot{
		{id: "system_launch_daemons", domain: resilienceStackDomain, path: systemPath},
		{id: "shared_launch_agents", domain: domain, path: "/Library/LaunchAgents"},
		{id: "user_launch_agents", domain: domain, path: launchAgentsPath},
		{id: "user_rencrow_launchd", domain: domain, path: rencrowLaunchdPath},
	}
}

func convertSystemJobsPlistToJSON(ctx context.Context, data []byte) ([]byte, error) {
	converted, err := runBoundedSystemJobsCommand(ctx, systemJobsPlutilPath, []string{"-convert", "json", "-o", "-", "-"}, data, systemJobsPlistOutputLimit)
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return nil, errSystemJobsMalformedPlist
		}
		return nil, err
	}
	return converted, nil
}

type darwinSystemJobsObserver struct{}

func (darwinSystemJobsObserver) ReadDisabledStates(ctx context.Context, domain string, labels []string) (map[string]string, error) {
	output, err := runBoundedSystemJobsCommand(ctx, resilienceStackLaunchctlPath, []string{"print-disabled", domain}, nil, systemJobsCommandOutputLimit)
	if err != nil {
		return nil, err
	}
	states := make(map[string]string, len(labels))
	for _, label := range labels {
		state, found, parseErr := parseSystemJobsDisabledState(output, label)
		if parseErr != nil {
			return nil, parseErr
		}
		if found {
			states[label] = state
		}
	}
	return states, nil
}

func (darwinSystemJobsObserver) Inspect(ctx context.Context, domain, label string) (systemJobsRuntimeObservation, error) {
	output, err := runBoundedSystemJobsCommand(ctx, resilienceStackLaunchctlPath, []string{"print", domain + "/" + label}, nil, systemJobsCommandOutputLimit)
	if err != nil {
		if context.Cause(ctx) == nil && launchdServiceIsUnloaded(output) {
			return systemJobsRuntimeObservation{known: true, loaded: false}, nil
		}
		return systemJobsRuntimeObservation{}, err
	}
	pathMatch := systemJobsLaunchdPathPattern.FindSubmatch(output)
	if len(pathMatch) != 2 {
		return systemJobsRuntimeObservation{}, nil
	}
	pidMatch := resilienceStackLaunchdPIDPattern.FindSubmatch(output)
	pid := 0
	if len(pidMatch) == 2 {
		pid, _ = strconv.Atoi(string(pidMatch[1]))
	}
	state := parseSystemJobsRuntimeState(output, pid)
	return systemJobsRuntimeObservation{
		known: true, loaded: true, path: strings.TrimSpace(string(pathMatch[1])), state: state, pid: pid,
	}, nil
}

func parseSystemJobsRuntimeState(output []byte, pid int) string {
	match := systemJobsLaunchdStatePattern.FindSubmatch(output)
	if len(match) != 2 {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(string(match[1]))) {
	case "running":
		if pid > 0 {
			return "running"
		}
	case "not running":
		return "not_running"
	case "waiting":
		return "waiting"
	}
	return ""
}

func parseSystemJobsDisabledState(output []byte, label string) (string, bool, error) {
	pattern := regexp.MustCompile(`(?m)^[\t ]*"` + regexp.QuoteMeta(label) + `"[\t ]*=>[\t ]*([^\r\n]*)\r?$`)
	matches := pattern.FindAllSubmatch(output, -1)
	if len(matches) == 0 {
		return "", false, nil
	}
	if len(matches) > 1 {
		return "", false, fmt.Errorf("launchd disabled state for %q appears more than once", label)
	}
	value := strings.TrimSpace(string(matches[0][1]))
	switch value {
	case "true", "disabled":
		return "disabled", true, nil
	case "false", "enabled":
		return "enabled", true, nil
	default:
		return "", false, fmt.Errorf("launchd disabled state for %q is unrecognized", label)
	}
}

func runBoundedSystemJobsCommand(ctx context.Context, executable string, args []string, stdin []byte, outputLimit int) ([]byte, error) {
	commandContext, cancel := context.WithTimeout(ctx, systemJobsCommandTimeout)
	defer cancel()
	command := exec.CommandContext(commandContext, executable, args...)
	command.WaitDelay = time.Second
	if stdin != nil {
		command.Stdin = bytes.NewReader(stdin)
	}
	output := &boundedSystemJobsOutput{limit: outputLimit}
	command.Stdout = output
	command.Stderr = output
	err := command.Run()
	data, overflow := output.snapshot()
	if overflow {
		return data, errors.New("system jobs inspection output exceeded its bound")
	}
	if err != nil {
		if cause := context.Cause(commandContext); cause != nil {
			return data, cause
		}
		return data, err
	}
	return data, nil
}

type boundedSystemJobsOutput struct {
	mu       sync.Mutex
	data     bytes.Buffer
	limit    int
	overflow bool
}

func (output *boundedSystemJobsOutput) Write(data []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	remaining := output.limit - output.data.Len()
	if remaining > len(data) {
		remaining = len(data)
	}
	if remaining > 0 {
		_, _ = output.data.Write(data[:remaining])
	}
	if remaining < len(data) {
		output.overflow = true
	}
	return len(data), nil
}

func (output *boundedSystemJobsOutput) snapshot() ([]byte, bool) {
	output.mu.Lock()
	defer output.mu.Unlock()
	return append([]byte(nil), output.data.Bytes()...), output.overflow
}
