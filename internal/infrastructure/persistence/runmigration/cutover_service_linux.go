//go:build linux

package runmigration

import (
	"context"
	"errors"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	runCutoverSystemctl = "/usr/bin/systemctl"
	runCutoverSS        = "/usr/bin/ss"
	runCutoverUnit      = "rencrow.service"
	runCutoverReadyURL  = "http://127.0.0.1:18790/health/ready"
)

type linuxRunCutoverService struct {
	installedRuntime string
	activeConfig     string
}

var runCutoverCommandOutput = runFixedRunCutoverCommandOutput
var runCutoverReadiness = runCutoverReady

func newRunCutoverService(installedRuntime, activeConfig string) (runCutoverService, error) {
	if strings.TrimSpace(installedRuntime) == "" || strings.TrimSpace(activeConfig) == "" {
		return nil, runCutoverFailure{"service_owner"}
	}
	return &linuxRunCutoverService{installedRuntime: installedRuntime, activeConfig: activeConfig}, nil
}

func (s *linuxRunCutoverService) StopAndVerify(ctx context.Context, expectedRuntimeSHA256, activeConfig string) (RunCutoverServiceEvidence, error) {
	if ctx == nil || activeConfig != s.activeConfig || !isLowerSHA256(expectedRuntimeSHA256) {
		return RunCutoverServiceEvidence{}, runCutoverFailure{"service_owner"}
	}
	runtimeBytes, err := readRunCutoverFile(s.installedRuntime)
	if err != nil || digest(runtimeBytes) != expectedRuntimeSHA256 {
		return RunCutoverServiceEvidence{}, runCutoverFailure{"runtime_mismatch"}
	}
	if _, err := readRunCutoverFile(s.activeConfig); err != nil {
		return RunCutoverServiceEvidence{}, runCutoverFailure{"active_config"}
	}
	state, err := s.show(ctx)
	if err != nil || state["Id"] != runCutoverUnit || state["LoadState"] != "loaded" || state["ActiveState"] != "active" ||
		!strings.Contains(state["ExecStart"], s.installedRuntime) || positiveInt(state["MainPID"]) == 0 {
		return RunCutoverServiceEvidence{}, runCutoverFailure{"service_running"}
	}
	if _, err := runCutoverCommandOutput(ctx, runCutoverSystemctl, "--user", "mask", "--runtime", runCutoverUnit); err != nil {
		return RunCutoverServiceEvidence{}, runCutoverFailure{"service_mask"}
	}
	if _, err := runCutoverCommandOutput(ctx, runCutoverSystemctl, "--user", "stop", runCutoverUnit); err != nil {
		return RunCutoverServiceEvidence{}, runCutoverFailure{"service_stop"}
	}
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		evidence, observeErr := s.stoppedEvidence(ctx, expectedRuntimeSHA256)
		if observeErr == nil && evidence.valid(expectedRuntimeSHA256) {
			return evidence, nil
		}
		select {
		case <-ctx.Done():
			return RunCutoverServiceEvidence{}, ctx.Err()
		case <-deadline.C:
			return RunCutoverServiceEvidence{}, runCutoverFailure{"service_stopped"}
		case <-ticker.C:
		}
	}
}

func (s *linuxRunCutoverService) Restore(ctx context.Context, expectedRuntimeSHA256 string) error {
	if ctx == nil || !isLowerSHA256(expectedRuntimeSHA256) {
		return runCutoverFailure{"service_restore"}
	}
	if _, err := runCutoverCommandOutput(ctx, runCutoverSystemctl, "--user", "unmask", "--runtime", runCutoverUnit); err != nil {
		return runCutoverFailure{"service_restore"}
	}
	if _, err := runCutoverCommandOutput(ctx, runCutoverSystemctl, "--user", "start", runCutoverUnit); err != nil {
		return runCutoverFailure{"service_restore"}
	}
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.NewTimer(300 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		state, err := s.show(ctx)
		if err == nil && state["ActiveState"] == "active" && positiveInt(state["MainPID"]) > 0 {
			runtimeBytes, readErr := readRunCutoverFile(s.installedRuntime)
			if readErr == nil && digest(runtimeBytes) == expectedRuntimeSHA256 && runCutoverReadiness(ctx, client) {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return runCutoverFailure{"service_restore"}
		case <-ticker.C:
		}
	}
}

func (s *linuxRunCutoverService) stoppedEvidence(ctx context.Context, expected string) (RunCutoverServiceEvidence, error) {
	state, err := s.show(ctx)
	if err != nil {
		return RunCutoverServiceEvidence{}, err
	}
	maskedOutput, err := runCutoverCommandOutput(ctx, runCutoverSystemctl, "--user", "is-enabled", runCutoverUnit)
	if err == nil || strings.TrimSpace(maskedOutput) != "masked" {
		return RunCutoverServiceEvidence{}, runCutoverFailure{"service_stopped"}
	}
	listeners, err := runCutoverCommandOutput(ctx, runCutoverSS, "-H", "-ltnp", "sport", "=", ":18790")
	if err != nil {
		return RunCutoverServiceEvidence{}, runCutoverFailure{"listener_check"}
	}
	active := 0
	if state["ActiveState"] == "active" {
		active = 1
	}
	pidZero := 0
	if positiveInt(state["MainPID"]) == 0 {
		pidZero = 1
	}
	listenerZero := 0
	if strings.TrimSpace(listeners) == "" {
		listenerZero = 1
	}
	return RunCutoverServiceEvidence{Owner: 1, Masked: 1, Active: active, MainPIDZero: pidZero, ListenerZero: listenerZero, RuntimeSHA256: expected}, nil
}

func (s *linuxRunCutoverService) show(ctx context.Context) (map[string]string, error) {
	output, err := runCutoverCommandOutput(ctx, runCutoverSystemctl, "--user", "show", runCutoverUnit,
		"--property=Id,LoadState,ActiveState,SubState,MainPID,ExecStart")
	if err != nil {
		return nil, err
	}
	result := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		key, value, found := strings.Cut(line, "=")
		if found {
			result[key] = value
		}
	}
	return result, nil
}

func runFixedRunCutoverCommand(ctx context.Context, name string, args ...string) error {
	_, err := runFixedRunCutoverCommandOutput(ctx, name, args...)
	return err
}

func runFixedRunCutoverCommandOutput(ctx context.Context, name string, args ...string) (string, error) {
	commandContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(commandContext, name, args...).CombinedOutput()
	if len(output) > 64<<10 {
		return "", runCutoverFailure{"service_output"}
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && name == runCutoverSystemctl && len(args) >= 3 && args[1] == "is-enabled" {
			return string(output), err
		}
		return "", runCutoverFailure{"service_command"}
	}
	return string(output), nil
}

func runCutoverReady(ctx context.Context, client *http.Client) bool {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, runCutoverReadyURL, nil)
	if err != nil {
		return false
	}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	_ = response.Body.Close()
	return response.StatusCode == http.StatusOK
}

func positiveInt(value string) int {
	number, _ := strconv.Atoi(strings.TrimSpace(value))
	if number < 0 {
		return 0
	}
	return number
}
