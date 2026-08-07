package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
)

type llmGatewayStartupStatus struct {
	BaseURL            string
	Ready              bool
	AutoStartAttempted bool
	AutoStarted        bool
	Warning            string
	Process            *os.Process
}

type llmGatewayLaunch struct {
	Command string
	Args    []string
	Dir     string
}

func ensureLLMGateway(cfg *config.Config) llmGatewayStartupStatus {
	status := llmGatewayStartupStatus{}
	if cfg == nil {
		status.Warning = "RenCrow_LLM configuration is unavailable; CORE LLM functions are disabled"
		log.Printf("SYSTEM WARNING: %s", status.Warning)
		return status
	}
	status.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.LLMGateway.BaseURL), "/")
	if llmGatewayHealthy(status.BaseURL) {
		status.Ready = true
		return status
	}

	parsed, err := url.Parse(status.BaseURL)
	if err != nil || !isLoopbackLLMHost(parsed.Hostname()) {
		status.Warning = "RenCrow_LLM is unavailable and CORE cannot start a remote Gateway; all LLM functions are disabled"
		log.Printf("SYSTEM WARNING: %s (base_url=%s)", status.Warning, status.BaseURL)
		return status
	}

	status.AutoStartAttempted = true
	configPath, err := findLLMGatewayConfig()
	if err != nil {
		status.Warning = "RenCrow_LLM is unavailable and no local Gateway config was found; all LLM functions are disabled"
		log.Printf("SYSTEM WARNING: %s (%v)", status.Warning, err)
		return status
	}
	launch, err := findLLMGatewayLaunch(configPath, parsed.Host)
	if err != nil {
		status.Warning = "RenCrow_LLM is unavailable and no local Gateway executable or Go source launcher was found; all LLM functions are disabled"
		log.Printf("SYSTEM WARNING: %s (%v)", status.Warning, err)
		return status
	}

	cmd := exec.Command(launch.Command, launch.Args...)
	cmd.Dir = launch.Dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		status.Warning = "RenCrow_LLM local Gateway start failed; all LLM functions are disabled"
		log.Printf("SYSTEM WARNING: %s (%v)", status.Warning, err)
		return status
	}
	status.Process = cmd.Process
	go func() {
		if err := cmd.Wait(); err != nil {
			log.Printf("SYSTEM WARNING: RenCrow_LLM Gateway process exited: %v", err)
		}
	}()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if llmGatewayHealthy(status.BaseURL) {
			status.Ready = true
			status.AutoStarted = true
			log.Printf("RenCrow_LLM Gateway auto-started (base_url=%s)", status.BaseURL)
			return status
		}
		time.Sleep(250 * time.Millisecond)
	}
	status.Warning = "RenCrow_LLM local Gateway was started but did not become healthy; all LLM functions are disabled"
	log.Printf("SYSTEM WARNING: %s", status.Warning)
	return status
}

func llmGatewayHealthy(baseURL string) bool {
	if strings.TrimSpace(baseURL) == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/health/ready", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false
	}
	var body struct {
		OK     bool   `json:"ok"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return false
	}
	return body.OK && strings.EqualFold(strings.TrimSpace(body.Status), "ready")
}

func isLoopbackLLMHost(host string) bool {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "localhost" || host == "" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func findLLMGatewayConfig() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("RENCROW_LLM_CONFIG")); configured != "" {
		if path, ok := existingFile(configured); ok {
			return path, nil
		}
		return "", fmt.Errorf("RENCROW_LLM_CONFIG does not exist: %s", configured)
	}
	var candidates []string
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, ".rencrow", "llm-gateway.json"),
			filepath.Join(home, ".rencrow", "rencrow-llm.json"),
			filepath.Join(home, ".rencrow", "llm-gateway.yaml"),
			filepath.Join(home, ".rencrow", "rencrow-llm.yaml"),
		)
	}
	for _, root := range llmGatewaySearchRoots() {
		candidates = append(candidates,
			filepath.Join(root, "RenCrow_LLM", "config", "rencrow-llm.json"),
			filepath.Join(root, "RenCrow_LLM", "configs", "rencrow-llm.json"),
			filepath.Join(root, "RenCrow_LLM", "examples", "rencrow-llm.yaml"),
			filepath.Join(root, "RenCrow_LLM", "examples", "rencrow-llm.yaml.example"),
		)
	}
	for _, candidate := range candidates {
		if path, ok := existingFile(candidate); ok {
			return path, nil
		}
	}
	return "", fmt.Errorf("set RENCROW_LLM_CONFIG to the RenCrow_LLM Gateway config")
}

func findLLMGatewayLaunch(configPath, listenAddress string) (llmGatewayLaunch, error) {
	args := []string{"serve", "--config", configPath}
	if strings.TrimSpace(listenAddress) != "" {
		args = append(args, "--listen", strings.TrimSpace(listenAddress))
	}
	if configured := strings.TrimSpace(os.Getenv("RENCROW_LLM_BIN")); configured != "" {
		if path, ok := existingFile(configured); ok {
			return llmGatewayLaunch{Command: path, Args: args}, nil
		}
		return llmGatewayLaunch{}, fmt.Errorf("RENCROW_LLM_BIN does not exist: %s", configured)
	}
	if command, err := exec.LookPath("rencrow-llm"); err == nil {
		return llmGatewayLaunch{Command: command, Args: args}, nil
	}

	binaryName := "rencrow-llm"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	for _, root := range llmGatewaySearchRoots() {
		gatewayDir := filepath.Join(root, "RenCrow_LLM", "gateway")
		for _, candidate := range []string{
			filepath.Join(gatewayDir, "build", binaryName),
			filepath.Join(gatewayDir, binaryName),
		} {
			if path, ok := existingFile(candidate); ok {
				return llmGatewayLaunch{Command: path, Args: args, Dir: gatewayDir}, nil
			}
		}
		if _, ok := existingFile(filepath.Join(gatewayDir, "cmd", "rencrow-llm", "main.go")); ok {
			if goCommand, err := exec.LookPath("go"); err == nil {
				goArgs := append([]string{"run", "./cmd/rencrow-llm"}, args...)
				return llmGatewayLaunch{Command: goCommand, Args: goArgs, Dir: gatewayDir}, nil
			}
		}
	}
	return llmGatewayLaunch{}, fmt.Errorf("set RENCROW_LLM_BIN or install rencrow-llm in PATH")
}

func llmGatewaySearchRoots() []string {
	seen := map[string]struct{}{}
	var roots []string
	add := func(path string) {
		path = filepath.Clean(strings.TrimSpace(path))
		if path == "." || path == "" {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		roots = append(roots, path)
	}
	if cwd, err := os.Getwd(); err == nil {
		add(cwd)
		add(filepath.Dir(cwd))
	}
	if executable, err := os.Executable(); err == nil {
		dir := filepath.Dir(executable)
		add(dir)
		add(filepath.Dir(dir))
		add(filepath.Dir(filepath.Dir(dir)))
	}
	return roots
}

func existingFile(path string) (string, bool) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	info, err := os.Stat(absolute)
	return absolute, err == nil && !info.IsDir()
}
