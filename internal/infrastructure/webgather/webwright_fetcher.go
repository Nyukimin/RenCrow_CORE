package webgather

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	modulewebgather "github.com/Nyukimin/picoclaw_multiLLM/modules/webgather"
)

type WebwrightFetcherConfig struct {
	Enabled           bool
	RunnerPath        string
	ConverterPath     string
	ConfigPath        string
	OutputDir         string
	StagingOutputDir  string
	UvxFrom           string
	Python            string
	ResponsesEndpoint string
	Model             string
	APIKey            string
}

type WebwrightCommandRunner func(ctx context.Context, command string, args []string) (stdout string, stderr string, exitCode int, err error)

type WebwrightFetcher struct {
	cfg    WebwrightFetcherConfig
	runner WebwrightCommandRunner
	now    func() time.Time
}

func NewWebwrightFetcher(cfg WebwrightFetcherConfig) *WebwrightFetcher {
	return &WebwrightFetcher{
		cfg:    cfg.withDefaults(),
		runner: execWebwrightCommand,
		now:    func() time.Time { return time.Now().UTC() },
	}
}

func (f *WebwrightFetcher) WithRunner(runner WebwrightCommandRunner) *WebwrightFetcher {
	if f == nil {
		return f
	}
	if runner == nil {
		f.runner = execWebwrightCommand
	} else {
		f.runner = runner
	}
	return f
}

func (f *WebwrightFetcher) Fetch(ctx context.Context, rawURL string, policy modulewebgather.FetchPolicy) (modulewebgather.FetchArtifact, error) {
	start := f.now()
	cfg := f.cfg.withDefaults()
	if policy.RequestTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, policy.RequestTimeout)
		defer cancel()
	}
	if !cfg.Enabled {
		return modulewebgather.FetchArtifact{Elapsed: time.Since(start), ProviderName: "webwright"}, modulewebgather.NewError(modulewebgather.ErrFetchFailed, "webwright_fetch.enabled=true is required")
	}
	if err := checkWebwrightEndpoint(ctx, cfg.ResponsesEndpoint); err != nil {
		return modulewebgather.FetchArtifact{Elapsed: time.Since(start), ProviderName: "webwright"}, modulewebgather.WrapError(modulewebgather.ErrFetchFailed, "webwright responses endpoint preflight failed", err)
	}
	if err := os.MkdirAll(cfg.OutputDir, 0o755); err != nil {
		return modulewebgather.FetchArtifact{Elapsed: time.Since(start), ProviderName: "webwright"}, modulewebgather.WrapError(modulewebgather.ErrFetchFailed, "failed to create webwright output dir", err)
	}
	if err := os.MkdirAll(cfg.StagingOutputDir, 0o755); err != nil {
		return modulewebgather.FetchArtifact{Elapsed: time.Since(start), ProviderName: "webwright"}, modulewebgather.WrapError(modulewebgather.ErrFetchFailed, "failed to create webwright staging dir", err)
	}
	taskID := webwrightTaskID(rawURL)
	task := webwrightTask(rawURL)
	command, args := buildWebwrightRunnerCommand(cfg, task, rawURL, taskID)
	stdout, stderr, code, err := f.run(ctx, command, args)
	if err != nil || code != 0 {
		return modulewebgather.FetchArtifact{Elapsed: time.Since(start), ProviderName: "webwright"}, webwrightCommandError("webwright runner failed", code, stdout, stderr, err)
	}
	reportPath, err := newestReportJSON(cfg.OutputDir)
	if err != nil {
		reportPath, err = synthesizeWebwrightReportJSON(cfg.OutputDir, taskID, rawURL)
		if err != nil {
			return modulewebgather.FetchArtifact{Elapsed: time.Since(start), ProviderName: "webwright"}, modulewebgather.WrapError(modulewebgather.ErrFetchFailed, "failed to locate or synthesize webwright report.json", err)
		}
	}
	jsonlPath := filepath.Join(cfg.StagingOutputDir, taskID+".jsonl")
	command, args = buildWebwrightConverterCommand(cfg, reportPath, jsonlPath, rawURL, taskID)
	stdout, stderr, code, err = f.run(ctx, command, args)
	if err != nil || code != 0 {
		return modulewebgather.FetchArtifact{Elapsed: time.Since(start), ProviderName: "webwright"}, webwrightCommandError("webwright report conversion failed", code, stdout, stderr, err)
	}
	item, err := readWebwrightJSONLItem(jsonlPath)
	if err != nil {
		return modulewebgather.FetchArtifact{Elapsed: time.Since(start), ProviderName: "webwright"}, modulewebgather.WrapError(modulewebgather.ErrFetchFailed, "failed to read converted webwright staging JSONL", err)
	}
	text := strings.TrimSpace(item.RawText)
	if text == "" {
		return modulewebgather.FetchArtifact{Elapsed: time.Since(start), ProviderName: "webwright"}, modulewebgather.NewError(modulewebgather.ErrEmptyContent, "webwright converted raw text is empty")
	}
	meta := map[string]any{
		"webwright":             true,
		"tool":                  "webwright_fetch",
		"webwright_report_path": reportPath,
		"webwright_jsonl_path":  jsonlPath,
		"webwright_task_id":     taskID,
		"review_required":       true,
		"auto_promote":          false,
	}
	for k, v := range item.Meta {
		if _, exists := meta[k]; !exists {
			meta[k] = v
		}
	}
	return modulewebgather.FetchArtifact{
		OriginalURL:  rawURL,
		FinalURL:     firstWebwrightURL(item.SourceURL, rawURL),
		StatusCode:   200,
		ContentType:  "text/plain",
		Body:         []byte(text),
		RawBytes:     int64(len([]byte(text))),
		Elapsed:      time.Since(start),
		FetchedAt:    f.now(),
		ProviderName: "webwright",
		Meta:         meta,
	}, nil
}

func (f *WebwrightFetcher) run(ctx context.Context, command string, args []string) (string, string, int, error) {
	runner := f.runner
	if runner == nil {
		runner = execWebwrightCommand
	}
	return runner(ctx, command, args)
}

func (cfg WebwrightFetcherConfig) withDefaults() WebwrightFetcherConfig {
	if strings.TrimSpace(cfg.RunnerPath) == "" {
		cfg.RunnerPath = "/home/nyukimi/RenCrow/RenCrow_Tools/tools/webwright_fetch/run_webwright_fetch.py"
	}
	if strings.TrimSpace(cfg.ConverterPath) == "" {
		cfg.ConverterPath = "/home/nyukimi/RenCrow/RenCrow_Tools/tools/webwright_fetch/webwright_to_staging.py"
	}
	if strings.TrimSpace(cfg.ConfigPath) == "" {
		cfg.ConfigPath = "/home/nyukimi/RenCrow/RenCrow_Tools/tools/webwright_fetch/config_local_worker.yaml"
	}
	if strings.TrimSpace(cfg.OutputDir) == "" {
		cfg.OutputDir = "tmp/webwright_runs"
	}
	if strings.TrimSpace(cfg.StagingOutputDir) == "" {
		cfg.StagingOutputDir = "tmp/webwright_staging"
	}
	if strings.TrimSpace(cfg.Python) == "" {
		cfg.Python = "python3"
	}
	if strings.TrimSpace(cfg.Model) == "" {
		cfg.Model = "Coder1"
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		cfg.APIKey = "dummy"
	}
	return cfg
}

func buildWebwrightRunnerCommand(cfg WebwrightFetcherConfig, task string, startURL string, taskID string) (string, []string) {
	args := []string{
		cfg.RunnerPath,
		"--task", task,
		"--output-dir", cfg.OutputDir,
		"-c", cfg.ConfigPath,
		"--local-responses-endpoint", cfg.ResponsesEndpoint,
		"--local-model", cfg.Model,
		"--local-api-key", cfg.APIKey,
		"--start-url", startURL,
		"--task-id", taskID,
	}
	if strings.TrimSpace(cfg.Python) != "" {
		args = append(args, "--python", cfg.Python)
	}
	if strings.TrimSpace(cfg.UvxFrom) != "" {
		args = append(args, "--uvx-from", cfg.UvxFrom)
	}
	return "python3", args
}

func buildWebwrightConverterCommand(cfg WebwrightFetcherConfig, reportPath string, jsonlPath string, sourceURL string, taskID string) (string, []string) {
	return "python3", []string{
		cfg.ConverterPath,
		"--input", reportPath,
		"--output", jsonlPath,
		"--namespace", "kb:webwright",
		"--task-id", taskID,
		"--source-id", "webwright:" + taskID,
		"--source-url", sourceURL,
		"--output-root", cfg.OutputDir,
	}
}

func execWebwrightCommand(ctx context.Context, command string, args []string) (string, string, int, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	configureWebwrightCommand(cmd)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return stdout.String(), stderr.String(), 127, err
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		if cmd.Process != nil {
			_ = killWebwrightCommand(cmd)
		}
		err = <-done
		if err == nil {
			err = ctx.Err()
		}
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return stdout.String(), stderr.String(), exitErr.ExitCode(), err
		}
		return stdout.String(), stderr.String(), 127, err
	}
	return stdout.String(), stderr.String(), 0, nil
}

func checkWebwrightEndpoint(ctx context.Context, endpoint string) error {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return errors.New("webwright_fetch.responses_endpoint is required")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("invalid webwright_fetch.responses_endpoint: %s", endpoint)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported webwright_fetch.responses_endpoint scheme: %s", u.Scheme)
	}
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return fmt.Errorf("responses endpoint is not reachable: %s: %w", endpoint, err)
	}
	_ = conn.Close()
	return nil
}

func newestReportJSON(root string) (string, error) {
	type candidate struct {
		path string
		mod  time.Time
	}
	var candidates []candidate
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Base(path) != "report.json" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		candidates = append(candidates, candidate{path: path, mod: info.ModTime()})
		return nil
	}); err != nil {
		return "", err
	}
	if len(candidates) == 0 {
		return "", errors.New("report.json was not found")
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].mod.After(candidates[j].mod)
	})
	return candidates[0].path, nil
}

func synthesizeWebwrightReportJSON(root string, taskID string, sourceURL string) (string, error) {
	runDir, err := newestWebwrightRunDir(root, taskID)
	if err != nil {
		return "", err
	}
	logText, err := collectWebwrightStepLogs(filepath.Join(runDir, "logs"))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(logText) == "" {
		return "", errors.New("webwright step logs are empty")
	}
	reportPath := filepath.Join(runDir, "report.json")
	report := map[string]any{
		"task_id":    taskID,
		"source_url": firstWebwrightURL(parseWebwrightLogLine(logText, "URL:"), sourceURL),
		"report": map[string]any{
			"title":   parseWebwrightLogLine(logText, "TITLE:"),
			"summary": parseWebwrightLogLine(logText, "SUMMARY:"),
			"text":    logText,
		},
		"meta": map[string]any{
			"generated_from": "webwright_step_logs",
			"run_dir":        runDir,
		},
	}
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(reportPath, b, 0o644); err != nil {
		return "", err
	}
	return reportPath, nil
}

func newestWebwrightRunDir(root string, taskID string) (string, error) {
	type candidate struct {
		path string
		mod  time.Time
	}
	taskID = strings.TrimSpace(taskID)
	var candidates []candidate
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() || path == root {
			return nil
		}
		name := filepath.Base(path)
		if taskID != "" && !strings.HasPrefix(name, taskID+"_") && name != taskID {
			return filepath.SkipDir
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		candidates = append(candidates, candidate{path: path, mod: info.ModTime()})
		return filepath.SkipDir
	}); err != nil {
		return "", err
	}
	if len(candidates) == 0 {
		return "", errors.New("webwright run directory not found")
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].mod.After(candidates[j].mod)
	})
	return candidates[0].path, nil
}

func collectWebwrightStepLogs(logDir string) (string, error) {
	entries, err := os.ReadDir(logDir)
	if err != nil {
		return "", err
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	var parts []string
	for _, name := range names {
		b, err := os.ReadFile(filepath.Join(logDir, name))
		if err != nil {
			return "", err
		}
		if text := strings.TrimSpace(string(b)); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

func parseWebwrightLogLine(text string, prefix string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

type webwrightStagingJSONLItem struct {
	SourceURL string         `json:"SourceURL"`
	RawText   string         `json:"RawText"`
	Meta      map[string]any `json:"Meta"`
}

func readWebwrightJSONLItem(path string) (webwrightStagingJSONLItem, error) {
	f, err := os.Open(path)
	if err != nil {
		return webwrightStagingJSONLItem{}, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var item webwrightStagingJSONLItem
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			return webwrightStagingJSONLItem{}, err
		}
		return item, nil
	}
	if err := scanner.Err(); err != nil {
		return webwrightStagingJSONLItem{}, err
	}
	return webwrightStagingJSONLItem{}, errors.New("converted webwright JSONL is empty")
}

func webwrightCommandError(message string, code int, stdout string, stderr string, err error) error {
	detail := strings.TrimSpace(stderr)
	if detail == "" {
		detail = strings.TrimSpace(stdout)
	}
	if detail == "" && err != nil {
		detail = err.Error()
	}
	if detail == "" {
		detail = fmt.Sprintf("exit_code=%d", code)
	}
	return modulewebgather.NewError(modulewebgather.ErrFetchFailed, message+": "+detail)
}

func webwrightTaskID(rawURL string) string {
	sum := sha256.Sum256([]byte(rawURL))
	return "url-" + hex.EncodeToString(sum[:])[:16]
}

func webwrightTask(rawURL string) string {
	return "Open " + rawURL + " and extract the public page title, main readable text, source URL, and concise evidence summary. Do not include cookies, authorization headers, API keys, or private data."
}

func firstWebwrightURL(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
