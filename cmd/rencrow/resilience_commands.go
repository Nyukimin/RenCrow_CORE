package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/resilience"
)

const resilienceUnit = "rencrow.service"

type resilienceMonitorState struct {
	ConsecutiveFailures int       `json:"consecutive_failures"`
	LastProbe           time.Time `json:"last_probe,omitempty"`
	LastFailure         time.Time `json:"last_failure,omitempty"`
	LastRestart         time.Time `json:"last_restart,omitempty"`
}

func cmdResilience() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: rencrow resilience <capture-stop|reconcile|status|gc>")
		os.Exit(2)
	}
	var err error
	switch os.Args[2] {
	case "capture-stop":
		err = resilienceCaptureStop()
	case "reconcile":
		err = resilienceReconcile()
	case "status":
		err = resilienceStatus()
	case "gc":
		err = resilienceGC()
	default:
		err = fmt.Errorf("unknown resilience command: %s", os.Args[2])
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "resilience: %v\n", err)
		os.Exit(1)
	}
}

func resilienceRoot() string {
	if root := strings.TrimSpace(os.Getenv("RENCROW_RESILIENCE_DIR")); root != "" {
		return root
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".rencrow", "resilience")
	}
	return filepath.Join(home, ".rencrow", "resilience")
}

func resilienceCaptureStop() error {
	result := strings.TrimSpace(os.Getenv("SERVICE_RESULT"))
	if result == "" || result == "success" {
		return nil
	}
	details := map[string]string{
		"service_result": result,
		"exit_code":      strings.TrimSpace(os.Getenv("EXIT_CODE")),
		"exit_status":    strings.TrimSpace(os.Getenv("EXIT_STATUS")),
		"invocation_id":  strings.TrimSpace(os.Getenv("INVOCATION_ID")),
	}
	logs, _ := resilienceJournal(800)
	kind := classifyStop(result, details["exit_code"], logs)
	material := incidentSignatureMaterial(kind, result, details["exit_code"], details["exit_status"], logs)
	incident, err := (resilience.Store{Root: resilienceRoot()}).Capture(resilience.Observation{
		SignatureSource: material, Kind: kind, At: time.Now().UTC(), Details: details,
	})
	if err != nil {
		return err
	}
	return writeIncidentEvidence(incident, logs, nil)
}

func resilienceReconcile() error {
	now := time.Now().UTC()
	state, _ := loadResilienceMonitorState()
	// timerと手動実行が近接しても、同じ停止を2回のfailureとして数えない。
	if !state.LastProbe.IsZero() && now.Sub(state.LastProbe) >= 0 && now.Sub(state.LastProbe) < 20*time.Second {
		return nil
	}
	state.LastProbe = now
	eligible, err := coreEligibleForLivenessProbe()
	if err != nil {
		return err
	}
	if !eligible {
		state.ConsecutiveFailures = 0
		return saveResilienceMonitorState(state)
	}
	aliveErr := probeCoreLiveness()
	if aliveErr != nil {
		state.ConsecutiveFailures++
		state.LastFailure = now
		if state.ConsecutiveFailures >= 2 && (state.LastRestart.IsZero() || now.Sub(state.LastRestart) >= 2*time.Minute) {
			incident, captureErr := captureHangIncident(aliveErr)
			if captureErr != nil {
				return captureErr
			}
			if restartErr := restartCore(); restartErr != nil {
				_ = (resilience.Store{Root: resilienceRoot()}).SetLastError(incident, "restart failed: "+restartErr.Error())
				return restartErr
			}
			state.LastRestart = now
			state.ConsecutiveFailures = 0
		}
		return saveResilienceMonitorState(state)
	}
	state.ConsecutiveFailures = 0
	if err := saveResilienceMonitorState(state); err != nil {
		return err
	}

	store := resilience.Store{Root: resilienceRoot()}
	incidents, err := store.List()
	if err != nil {
		return err
	}
	for _, incident := range incidents {
		if err := reconcileIncident(store, incident, now); err != nil {
			_ = store.SetLastError(incident, err.Error())
		}
	}
	_, err = store.GC(now, resilience.DefaultDetailRetention, resilience.DefaultMetadataRetention)
	return err
}

func coreEligibleForLivenessProbe() (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "--user", "show", resilienceUnit,
		"--property=ActiveState", "--property=SubState", "--property=ExecMainStartTimestampMonotonic").Output()
	if err != nil {
		return false, fmt.Errorf("inspect CORE service state: %w", err)
	}
	uptimeBytes, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return false, fmt.Errorf("read system uptime: %w", err)
	}
	var uptimeSeconds float64
	if _, err := fmt.Sscan(string(uptimeBytes), &uptimeSeconds); err != nil {
		return false, fmt.Errorf("parse system uptime: %w", err)
	}
	return parseCoreProbeEligibility(string(out), uint64(uptimeSeconds*1_000_000)), nil
}

func parseCoreProbeEligibility(serviceProperties string, uptimeMicros uint64) bool {
	properties := map[string]string{}
	for _, line := range strings.Split(serviceProperties, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			properties[key] = strings.TrimSpace(value)
		}
	}
	if properties["ActiveState"] != "active" || properties["SubState"] != "running" {
		return false
	}
	var startedMicros uint64
	if _, err := fmt.Sscan(properties["ExecMainStartTimestampMonotonic"], &startedMicros); err != nil || startedMicros == 0 || uptimeMicros < startedMicros {
		return false
	}
	return uptimeMicros-startedMicros >= uint64((30 * time.Second).Microseconds())
}

func reconcileIncident(store resilience.Store, incident *resilience.Incident, now time.Time) error {
	switch incident.Status {
	case resilience.StatusRepairRequested:
		result, found, err := findRepairResult(incident.RepairJobID)
		if err != nil {
			return err
		}
		if found && result == "completed" {
			if err := buildInstallAndRestart(); err != nil {
				return store.MarkRepairFailed(incident, "repair validation/deploy failed: "+err.Error())
			}
			return store.MarkRepairCompleted(incident, now)
		}
		if found && result == "failed" {
			return store.MarkRepairFailed(incident, "repair job failed")
		}
		if incident.RepairRequestedAt != nil && now.Sub(*incident.RepairRequestedAt) > 45*time.Minute {
			return store.MarkRepairFailed(incident, "repair job timed out")
		}
		return nil
	case resilience.StatusRepairPending:
		_, err := store.ResolveStable(incident, now, resilience.DefaultVerificationAge)
		return err
	case resilience.StatusResolved:
		return nil
	case resilience.StatusRepairFailed:
		if incident.RepairRequestedAt != nil && now.Sub(*incident.RepairRequestedAt) < 10*time.Minute {
			return nil
		}
	}

	if err := store.MarkRestartRecovered(incident, now); err != nil {
		return err
	}
	if !autoRepairEnabled() || !autoRepairable(incident.Kind) || incident.RepairAttempts >= resilience.DefaultMaxRepairAttempts {
		return nil
	}
	if incident.LastRepairProbeAt != nil && now.Sub(*incident.LastRepairProbeAt) >= 0 && now.Sub(*incident.LastRepairProbeAt) < 5*time.Minute {
		return nil
	}
	if err := probeRepairBackend(selectedRepairRoute()); err != nil {
		return store.MarkRepairProbe(incident, now, err.Error())
	}
	if err := store.MarkRepairProbe(incident, now, ""); err != nil {
		return err
	}
	doctor, _ := runDoctor()
	if len(doctor) > 0 {
		_ = os.WriteFile(filepath.Join(store.IncidentDir(incident.Signature), "doctor-latest.json"), doctor, 0o600)
	}
	jobID, err := requestRepair(incident)
	if err != nil {
		return err
	}
	return store.MarkRepairRequested(incident, jobID, now, resilience.DefaultMaxRepairAttempts)
}

func resilienceStatus() error {
	incidents, err := (resilience.Store{Root: resilienceRoot()}).List()
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]any{"root": resilienceRoot(), "incidents": incidents})
}

func resilienceGC() error {
	result, err := (resilience.Store{Root: resilienceRoot()}).GC(time.Now().UTC(), resilience.DefaultDetailRetention, resilience.DefaultMetadataRetention)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func probeCoreLiveness() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resilienceBaseURL()+"/health/live", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("liveness returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func resilienceBaseURL() string {
	if value := strings.TrimRight(strings.TrimSpace(os.Getenv("RENCROW_RESILIENCE_BASE_URL")), "/"); value != "" {
		return value
	}
	cfg, err := config.LoadConfig(getConfigPath())
	if err != nil {
		return "http://127.0.0.1:18790"
	}
	host := strings.TrimSpace(cfg.Server.Host)
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s:%d", host, cfg.Server.Port)
}

func captureHangIncident(probeErr error) (*resilience.Incident, error) {
	logs, _ := resilienceJournal(800)
	pprof := fetchPprofGoroutines()
	incident, err := (resilience.Store{Root: resilienceRoot()}).Capture(resilience.Observation{
		SignatureSource: "hang:health-live-timeout", Kind: "hang", At: time.Now().UTC(),
		Details: map[string]string{"probe_error": probeErr.Error()},
	})
	if err != nil {
		return nil, err
	}
	if err := writeIncidentEvidence(incident, logs, pprof); err != nil {
		return nil, err
	}
	return incident, nil
}

func writeIncidentEvidence(incident *resilience.Incident, logs, goroutines []byte) error {
	store := resilience.Store{Root: resilienceRoot()}
	dir := store.IncidentDir(incident.Signature)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if len(logs) > 0 {
		if _, err := os.Stat(filepath.Join(dir, "first.log.gz")); errors.Is(err, os.ErrNotExist) {
			if err := writeGzip(filepath.Join(dir, "first.log.gz"), logs); err != nil {
				return err
			}
		}
		if err := writeGzip(filepath.Join(dir, "latest.log.gz"), logs); err != nil {
			return err
		}
	}
	if len(goroutines) > 0 {
		if _, err := os.Stat(filepath.Join(dir, "first-goroutines.txt")); errors.Is(err, os.ErrNotExist) {
			_ = os.WriteFile(filepath.Join(dir, "first-goroutines.txt"), goroutines, 0o600)
		}
		return os.WriteFile(filepath.Join(dir, "latest-goroutines.txt"), goroutines, 0o600)
	}
	return nil
}

func writeGzip(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(f)
	_, writeErr := zw.Write(data)
	closeZipErr := zw.Close()
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeZipErr != nil {
		return closeZipErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp, path)
}

func resilienceJournal(lines int) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	args := []string{"--user", "-u", resilienceUnit}
	invocationID := ""
	if strings.TrimSpace(os.Getenv("SERVICE_RESULT")) != "" {
		invocationID = strings.TrimSpace(os.Getenv("INVOCATION_ID"))
	} else {
		showCtx, showCancel := context.WithTimeout(context.Background(), 2*time.Second)
		out, err := exec.CommandContext(showCtx, "systemctl", "--user", "show", resilienceUnit, "--property=InvocationID", "--value").Output()
		showCancel()
		if err == nil {
			invocationID = strings.TrimSpace(string(out))
		}
	}
	if invocationID != "" {
		args = append(args, "_SYSTEMD_INVOCATION_ID="+invocationID)
	}
	args = append(args, "-n", fmt.Sprint(lines), "--no-pager", "-o", "short-iso-precise")
	return exec.CommandContext(ctx, "journalctl", args...).CombinedOutput()
}

func fetchPprofGoroutines() []byte {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resilienceBaseURL()+"/debug/pprof/goroutine?debug=2", nil)
	if err != nil {
		return nil
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return b
}

func restartCore() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "--user", "restart", resilienceUnit).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl restart: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func runDoctor() ([]byte, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return exec.CommandContext(ctx, exe, "doctor", "--json").CombinedOutput()
}

func requestRepair(incident *resilience.Incident) (string, error) {
	repairRoute := selectedRepairRoute()
	payload := map[string]any{
		"reason":       "resilience incident " + incident.Signature,
		"recent":       300,
		"target_route": repairRoute,
		"target_agent": "shiro",
		"instruction":  fmt.Sprintf("COREの%s事故を自己修復してください。事故証拠は %s にあります。既存の実ファイルだけを対象に原因を特定し、最小修正を適用してテストしてください。設定や外部依存だけが原因ならコードを変更せず診断を返してください。commit、push、systemctl、サービス再起動は行わないでください。", incident.Kind, filepath.Join(resilienceRoot(), "incidents", incident.Signature)),
	}
	body, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, resilienceBaseURL()+"/viewer/repair/run", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("repair request HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(responseBody)))
	}
	var decoded struct {
		OK    bool   `json:"ok"`
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return "", err
	}
	if !decoded.OK || decoded.JobID == "" {
		return "", errors.New("repair endpoint did not return a job id")
	}
	return decoded.JobID, nil
}

func selectedRepairRoute() string {
	switch strings.ToUpper(strings.TrimSpace(os.Getenv("RENCROW_RESILIENCE_REPAIR_ROUTE"))) {
	case "CODE1":
		return "CODE1"
	case "CODE3":
		return "CODE3"
	case "CODE4":
		return "CODE4"
	default:
		return "CODE2"
	}
}

func probeRepairBackend(route string) error {
	cfg, err := config.LoadConfig(getConfigPath())
	if err != nil {
		return fmt.Errorf("load repair backend config: %w", err)
	}
	var coder config.CoderConfig
	switch route {
	case "CODE1":
		coder = cfg.Coder1
	case "CODE3":
		coder = cfg.Coder3
	case "CODE4":
		coder = cfg.Coder4
	default:
		coder = cfg.Coder2
	}
	if !coder.Enabled || strings.TrimSpace(coder.BaseURL) == "" {
		return fmt.Errorf("repair backend %s is not configured", route)
	}
	if err := probeOpenAIModels(coder.BaseURL, coder.APIKey); err != nil {
		return fmt.Errorf("repair backend %s unavailable: %w", route, err)
	}
	return nil
}

func probeOpenAIModels(baseURL, apiKey string) error {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	modelsURL := baseURL + "/v1/models"
	if strings.HasSuffix(baseURL, "/v1") {
		modelsURL = baseURL + "/models"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		return err
	}
	if token := strings.TrimSpace(apiKey); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GET /v1/models returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func findRepairResult(jobID string) (string, bool, error) {
	if strings.TrimSpace(jobID) == "" {
		return "", false, nil
	}
	cfg, err := config.LoadConfig(getConfigPath())
	if err != nil {
		return "", false, err
	}
	path := cfg.ViewerLog.Path
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	if info, statErr := f.Stat(); statErr == nil && info.Size() > 8<<20 {
		_, _ = f.Seek(info.Size()-(8<<20), io.SeekStart)
		_, _ = bufio.NewReader(f).ReadString('\n')
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	result := ""
	for scanner.Scan() {
		var event struct {
			Type  string `json:"type"`
			JobID string `json:"job_id"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.JobID != jobID {
			continue
		}
		switch event.Type {
		case "repair.completed":
			result = "completed"
		case "repair.failed", "repair.start_failed":
			result = "failed"
		}
	}
	return result, result != "", scanner.Err()
}

func buildInstallAndRestart() error {
	repo := strings.TrimSpace(os.Getenv("RENCROW_RESILIENCE_REPO_DIR"))
	if repo == "" {
		return errors.New("RENCROW_RESILIENCE_REPO_DIR is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	testCmd := exec.CommandContext(ctx, "go", "test", "./...")
	testCmd.Dir = repo
	testCmd.Env = environmentWithout(os.Environ(), "RENCROW_CONFIG")
	if out, err := testCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go test: %w: %s", err, tailText(out, 4000))
	}
	candidateDir := filepath.Join(resilienceRoot(), "candidate")
	if err := os.MkdirAll(candidateDir, 0o700); err != nil {
		return err
	}
	candidate := filepath.Join(candidateDir, "rencrow")
	build := exec.CommandContext(ctx, "go", "build", "-o", candidate, "./cmd/rencrow")
	build.Dir = repo
	build.Env = environmentWithout(os.Environ(), "RENCROW_CONFIG")
	if out, err := build.CombinedOutput(); err != nil {
		return fmt.Errorf("go build: %w: %s", err, tailText(out, 4000))
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	installedTmp := exe + ".resilience-new"
	data, err := os.ReadFile(candidate)
	if err != nil {
		return err
	}
	if err := os.WriteFile(installedTmp, data, 0o755); err != nil {
		return err
	}
	if err := os.Rename(installedTmp, exe); err != nil {
		return err
	}
	return restartCore()
}

func classifyStop(result, exitCode string, logs []byte) string {
	text := strings.ToLower(string(logs))
	if strings.Contains(strings.ToLower(result), "oom") || strings.Contains(text, "out of memory") || strings.Contains(text, "oom-kill") {
		return "oom"
	}
	if strings.Contains(text, "panic:") || strings.Contains(text, "fatal error:") {
		return "panic"
	}
	if exitCode == "dumped" || strings.Contains(result, "core-dump") {
		return "fatal"
	}
	return "abnormal_exit"
}

var volatileIncidentText = regexp.MustCompile(`(?i)(0x[0-9a-f]+|\bpid[ =:]?\d+\b|goroutine \d+|:\d+)`)
var journalIncidentPrefix = regexp.MustCompile(`^\S+\s+\S+\s+[^:]+:\s*`)

func incidentSignatureMaterial(kind, result, exitCode, exitStatus string, logs []byte) string {
	lines := strings.Split(string(logs), "\n")
	start := -1
	for i := len(lines) - 1; i >= 0; i-- {
		lower := strings.ToLower(lines[i])
		if strings.Contains(lower, "panic:") || strings.Contains(lower, "fatal error:") {
			start = i
			break
		}
	}
	selected := kind + "|" + result + "|" + exitCode + "|" + exitStatus
	if start >= 0 {
		end := start + 40
		if end > len(lines) {
			end = len(lines)
		}
		normalized := make([]string, 0, end-start)
		for _, line := range lines[start:end] {
			normalized = append(normalized, journalIncidentPrefix.ReplaceAllString(line, ""))
		}
		selected += "|" + strings.Join(normalized, "\n")
	}
	return volatileIncidentText.ReplaceAllString(selected, "#")
}

func autoRepairEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("RENCROW_RESILIENCE_AUTO_REPAIR")))
	return v != "0" && v != "false" && v != "off"
}

func autoRepairable(kind string) bool {
	switch kind {
	case "panic", "fatal", "hang", "abnormal_exit":
		return true
	default:
		return false
	}
}

func loadResilienceMonitorState() (resilienceMonitorState, error) {
	var state resilienceMonitorState
	b, err := os.ReadFile(filepath.Join(resilienceRoot(), "monitor.json"))
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	return state, json.Unmarshal(b, &state)
}

func saveResilienceMonitorState(state resilienceMonitorState) error {
	if err := os.MkdirAll(resilienceRoot(), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(state, "", "  ")
	b = append(b, '\n')
	tmp := filepath.Join(resilienceRoot(), "monitor.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(resilienceRoot(), "monitor.json"))
}

func tailText(data []byte, max int) string {
	if len(data) > max {
		data = data[len(data)-max:]
	}
	return strings.TrimSpace(string(data))
}

func environmentWithout(environment []string, key string) []string {
	prefix := key + "="
	out := make([]string, 0, len(environment))
	for _, item := range environment {
		if !strings.HasPrefix(item, prefix) {
			out = append(out, item)
		}
	}
	return out
}
