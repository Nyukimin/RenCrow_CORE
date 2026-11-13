//go:build darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const resilienceStackLaunchctlPath = "/bin/launchctl"
const resilienceStackInspectionCommandTimeout = 10 * time.Second
const resilienceStackOperationCommandTimeout = 60 * time.Second
const resilienceStackUnloadWaitTimeout = 75 * time.Second

type darwinResilienceStackOperator struct{}

func newResilienceStackOperator() resilienceStackOperator {
	return darwinResilienceStackOperator{}
}

func resilienceStackFileOwner(info os.FileInfo) (int, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("cannot inspect protected stack file owner")
	}
	return int(stat.Uid), nil
}

func acquireResilienceStackLock() (func(), error) {
	if err := validateResilienceStackProtectedFile(resilienceStackLockPath, 0, true); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	file, err := os.OpenFile(resilienceStackLockPath, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	owner, err := resilienceStackFileOwner(info)
	if err != nil || owner != 0 || info.Mode().Perm()&0o077 != 0 || !info.Mode().IsRegular() {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("stack transaction lock has unsafe owner or permissions")
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, errors.New("another stack transaction is already running")
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}

func (darwinResilienceStackOperator) ValidateTarget(ctx context.Context, service resilienceStackService) error {
	expectedOwner := 0
	if service.Domain == resilienceStackGUI501 || service.Domain == resilienceStackUser501 {
		expectedOwner = 501
	}
	if err := validateResilienceStackProtectedFile(service.PlistPath, expectedOwner, false); err != nil {
		return err
	}
	parentInfo, err := os.Lstat(filepath.Dir(service.PlistPath))
	if err != nil {
		return err
	}
	parentOwner, err := resilienceStackFileOwner(parentInfo)
	if err != nil {
		return err
	}
	if parentOwner != expectedOwner {
		return fmt.Errorf("plist directory owner for %s is %d, expected %d", service.Label, parentOwner, expectedOwner)
	}
	output, err := runResilienceStackCommand(ctx, resilienceStackInspectionCommandTimeout, "/usr/libexec/PlistBuddy", "-c", "Print :Label", service.PlistPath)
	if err != nil {
		return fmt.Errorf("read launchd plist label for %s: %w", service.Label, err)
	}
	if strings.TrimSpace(string(output)) != service.Label {
		return fmt.Errorf("launchd plist label does not match target %s", service.Label)
	}
	return nil
}

func (darwinResilienceStackOperator) Inspect(ctx context.Context, service resilienceStackService) (resilienceStackSnapshot, error) {
	disabledOutput, err := runResilienceStackLaunchctl(ctx, resilienceStackInspectionCommandTimeout, "print-disabled", service.Domain)
	if err != nil {
		return resilienceStackSnapshot{}, fmt.Errorf("inspect launchd disabled state for %s: %w", service.Label, err)
	}
	disabled, err := parseResilienceStackDisabledState(disabledOutput, service.Label)
	if err != nil {
		return resilienceStackSnapshot{}, err
	}
	output, err := runResilienceStackLaunchctl(ctx, resilienceStackInspectionCommandTimeout, "print", service.Domain+"/"+service.Label)
	if err != nil {
		if context.Cause(ctx) != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return resilienceStackSnapshot{}, fmt.Errorf("inspect launchd target %s: %w", service.Label, err)
		}
		if launchdServiceIsUnloaded(output) {
			return resilienceStackSnapshot{Disabled: disabled}, nil
		}
		return resilienceStackSnapshot{}, fmt.Errorf("inspect launchd target %s: %w", service.Label, err)
	}
	pathPattern := regexp.MustCompile(`(?m)^\s*path\s*=\s*(.+?)\s*$`)
	pathMatch := pathPattern.FindSubmatch(output)
	if len(pathMatch) != 2 || string(pathMatch[1]) != service.PlistPath {
		return resilienceStackSnapshot{}, fmt.Errorf("loaded launchd target %s does not use its approved plist", service.Label)
	}
	stateMatch := resilienceStackLaunchdStatePattern.FindSubmatch(output)
	if len(stateMatch) != 2 {
		return resilienceStackSnapshot{}, fmt.Errorf("launchd state is missing for %s", service.Label)
	}
	pidMatch := resilienceStackLaunchdPIDPattern.FindSubmatch(output)
	pid := 0
	if len(pidMatch) == 2 {
		pid, _ = strconv.Atoi(string(pidMatch[1]))
	}
	state := string(stateMatch[1])
	return resilienceStackSnapshot{Loaded: true, Disabled: disabled, Running: state == "running" && pid > 0, PID: pid}, nil
}

func (darwinResilienceStackOperator) Readiness(ctx context.Context, service resilienceStackService) error {
	switch service.Readiness.Kind {
	case "process":
		snapshot, err := (darwinResilienceStackOperator{}).Inspect(ctx, service)
		if err != nil {
			return err
		}
		return validateResilienceStackProcessSnapshot(snapshot)
	case "tcp":
		dialer := net.Dialer{Timeout: 2 * time.Second}
		connection, err := dialer.DialContext(ctx, "tcp", service.Readiness.Address)
		if err != nil {
			return err
		}
		return connection.Close()
	case "http":
		endpoint := url.URL{Scheme: "http", Host: service.Readiness.Address, Path: service.Readiness.Path}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return err
		}
		client := http.Client{
			Timeout: 2 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		response, err := client.Do(request)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("HTTP readiness returned %d", response.StatusCode)
		}
		return nil
	default:
		return fmt.Errorf("unsupported readiness kind %q", service.Readiness.Kind)
	}
}

func (darwinResilienceStackOperator) Bootout(ctx context.Context, service resilienceStackService) error {
	operator := darwinResilienceStackOperator{}
	return bootoutResilienceStackTarget(
		ctx,
		service,
		resilienceStackUnloadWaitTimeout,
		resilienceStackPollEvery,
		func(ctx context.Context, service resilienceStackService) error {
			_, err := runResilienceStackLaunchctl(ctx, resilienceStackOperationCommandTimeout, "bootout", service.Domain+"/"+service.Label)
			return err
		},
		operator.Inspect,
	)
}

func bootoutResilienceStackTarget(
	ctx context.Context,
	service resilienceStackService,
	waitTimeout time.Duration,
	pollInterval time.Duration,
	bootout func(context.Context, resilienceStackService) error,
	inspect func(context.Context, resilienceStackService) (resilienceStackSnapshot, error),
) error {
	if err := bootout(ctx, service); err != nil {
		return err
	}

	waitCtx, cancel := context.WithTimeout(ctx, waitTimeout)
	defer cancel()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		snapshot, err := inspect(waitCtx, service)
		if err != nil {
			return fmt.Errorf("inspect launchd target %s while waiting for unload: %w", service.Label, err)
		}
		if err := context.Cause(waitCtx); err != nil {
			return fmt.Errorf("wait for launchd target %s to unload: %w", service.Label, err)
		}
		if !snapshot.Loaded && !snapshot.Running && snapshot.PID <= 0 {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("wait for launchd target %s to unload: %w", service.Label, context.Cause(waitCtx))
		case <-ticker.C:
		}
	}
}

func (darwinResilienceStackOperator) Bootstrap(ctx context.Context, service resilienceStackService) error {
	_, err := runResilienceStackLaunchctl(ctx, resilienceStackOperationCommandTimeout, "bootstrap", service.Domain, service.PlistPath)
	return err
}

func runResilienceStackLaunchctl(ctx context.Context, timeout time.Duration, args ...string) ([]byte, error) {
	output, err := runResilienceStackCommand(ctx, timeout, resilienceStackLaunchctlPath, args...)
	if err != nil {
		return output, fmt.Errorf("launchctl %s: %w", args[0], err)
	}
	return output, nil
}

func runResilienceStackCommand(ctx context.Context, timeout time.Duration, executable string, args ...string) ([]byte, error) {
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(commandContext, executable, args...)
	command.WaitDelay = 2 * time.Second
	output, err := command.CombinedOutput()
	if err != nil {
		if cause := context.Cause(commandContext); cause != nil {
			return output, cause
		}
		return output, err
	}
	return output, nil
}

func launchdServiceIsUnloaded(output []byte) bool {
	text := strings.ToLower(string(output))
	return strings.Contains(text, "could not find service") || strings.Contains(text, "no such process")
}

func parseResilienceStackDisabledState(output []byte, label string) (bool, error) {
	pattern := regexp.MustCompile(`(?m)^[\t ]*"` + regexp.QuoteMeta(label) + `"[\t ]*=>[\t ]*([^\r\n]*)\r?$`)
	matches := pattern.FindAllSubmatch(output, -1)
	if len(matches) == 0 {
		return false, nil
	}
	if len(matches) > 1 {
		return false, fmt.Errorf("launchctl disabled state for %q appears multiple times", label)
	}
	value := strings.TrimSpace(string(matches[0][1]))
	switch value {
	case "true", "disabled":
		return true, nil
	case "false", "enabled":
		return false, nil
	default:
		return false, fmt.Errorf("launchctl disabled state for %q is unrecognized: %q", label, value)
	}
}
