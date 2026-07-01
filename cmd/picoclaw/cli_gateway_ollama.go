package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	domainhealth "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/health"
)

func cmdGateway() {
	cfg, err := config.LoadConfig(getConfigPath())
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	getStatus := func(url string) (int, error) {
		client := &http.Client{Timeout: 2 * time.Second}
		resp, err := client.Get(url) //nolint:gosec // local health probe
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		return resp.StatusCode, nil
	}
	restart := func() error {
		return exec.Command("systemctl", "restart", "picoclaw.service").Run()
	}
	code := runGatewayCommand(os.Args[2:], cfg, os.Stdout, os.Stderr, getStatus, restart, func() time.Time { return time.Now().UTC() })
	if code != 0 {
		os.Exit(code)
	}
}

func cmdOllama() {
	cfg, err := config.LoadConfig(getConfigPath())
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	target, restart, err := buildOllamaRestartAction(cfg)
	if err != nil {
		log.Fatalf("Failed to build ollama restart action: %v", err)
	}
	code := runOllamaCommand(
		os.Args[2:],
		cfg,
		buildHealthService(cfg),
		os.Stdout,
		os.Stderr,
		target,
		restart,
		func() time.Time { return time.Now().UTC() },
	)
	if code != 0 {
		os.Exit(code)
	}
}

func runGatewayCommand(
	args []string,
	cfg *config.Config,
	out io.Writer,
	errOut io.Writer,
	getStatus func(url string) (statusCode int, err error),
	restart func() error,
	now func() time.Time,
) int {
	subcmd := "status"
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		subcmd = strings.ToLower(strings.TrimSpace(args[0]))
	}
	jsonOut := hasFlag(args, "--json")

	switch subcmd {
	case "status":
		url := gatewayHealthURL(cfg)
		code, err := getStatus(url)
		if err != nil {
			if jsonOut {
				writeJSONCLI(out, map[string]any{
					"ok":        false,
					"timestamp": now().Format(time.RFC3339),
					"component": "gateway",
					"status":    "down",
					"code":      "E_GATEWAY_UNREACHABLE",
					"hint":      "picoclaw gateway restart を実行",
					"details": map[string]any{
						"url":   url,
						"error": err.Error(),
					},
				}, true)
			} else {
				fmt.Fprintf(out, "[DOWN] gateway health check failed: %v\n", err)
			}
			return 1
		}
		if code >= 200 && code < 300 {
			if jsonOut {
				writeJSONCLI(out, map[string]any{
					"ok":        true,
					"timestamp": now().Format(time.RFC3339),
					"component": "gateway",
					"status":    "running",
					"details": map[string]any{
						"url":         url,
						"status_code": code,
					},
				}, true)
			} else {
				fmt.Fprintf(out, "[OK] gateway reachable: %s (%d)\n", url, code)
			}
			return 0
		}
		if jsonOut {
			writeJSONCLI(out, map[string]any{
				"ok":        false,
				"timestamp": now().Format(time.RFC3339),
				"component": "gateway",
				"status":    "down",
				"code":      "E_GATEWAY_UNHEALTHY",
				"hint":      "health endpoint と logs を確認",
				"details": map[string]any{
					"url":         url,
					"status_code": code,
				},
			}, true)
		} else {
			fmt.Fprintf(out, "[DOWN] gateway unhealthy: %s (%d)\n", url, code)
		}
		return 1
	case "restart":
		if err := restart(); err != nil {
			if jsonOut {
				writeJSONCLI(out, map[string]any{
					"ok":        false,
					"timestamp": now().Format(time.RFC3339),
					"component": "gateway",
					"status":    "down",
					"code":      "E_GATEWAY_RESTART_FAILED",
					"hint":      "systemctl権限とサービス名を確認",
					"details": map[string]any{
						"error": err.Error(),
					},
				}, true)
			} else {
				fmt.Fprintf(out, "failed to restart via systemctl: %v\n", err)
			}
			return 1
		}
		if jsonOut {
			writeJSONCLI(out, map[string]any{
				"ok":        true,
				"timestamp": now().Format(time.RFC3339),
				"component": "gateway",
				"status":    "restarted",
				"details":   map[string]any{},
			}, true)
		} else {
			fmt.Fprintln(out, "picoclaw.service restarted")
		}
		return 0
	default:
		fmt.Fprintf(errOut, "unknown gateway subcommand: %s\n", subcmd)
		fmt.Fprintln(errOut, "usage: picoclaw gateway [status|restart]")
		return 1
	}
}

func runOllamaCommand(
	args []string,
	cfg *config.Config,
	checker healthChecker,
	out io.Writer,
	errOut io.Writer,
	restartTarget string,
	restart func() error,
	now func() time.Time,
) int {
	subcmd := "status"
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		subcmd = strings.ToLower(strings.TrimSpace(args[0]))
	}
	jsonOut := hasFlag(args, "--json")

	switch subcmd {
	case "status":
		report := checker.RunChecks(context.Background())
		if jsonOut {
			writeJSONCLI(out, map[string]any{
				"ok":        report.Status != domainhealth.StatusDown,
				"timestamp": now().Format(time.RFC3339),
				"component": "ollama",
				"status":    report.Status,
				"details": map[string]any{
					"base_url": cfg.Ollama.BaseURL,
					"model":    cfg.Ollama.Model,
					"checks":   report.Checks,
				},
			}, true)
		} else {
			fmt.Fprintf(out, "Ollama: %s (model: %s)\n", cfg.Ollama.BaseURL, cfg.Ollama.Model)
			for _, c := range report.Checks {
				fmt.Fprintf(out, "  [%s] %s: %s (%dms)\n", c.Status, c.Name, c.Message, c.Duration.Milliseconds())
			}
			fmt.Fprintf(out, "\nOverall: %s\n", report.Status)
		}
		if report.Status == domainhealth.StatusDown {
			return 1
		}
		return 0
	case "restart":
		if err := restart(); err != nil {
			if jsonOut {
				writeJSONCLI(out, map[string]any{
					"ok":        false,
					"timestamp": now().Format(time.RFC3339),
					"component": "ollama",
					"status":    "down",
					"code":      "E_OLLAMA_RESTART_FAILED",
					"hint":      "ollama service と SSH 設定を確認",
					"details": map[string]any{
						"base_url": cfg.Ollama.BaseURL,
						"target":   restartTarget,
						"error":    err.Error(),
					},
				}, true)
			} else {
				fmt.Fprintf(errOut, "failed to restart ollama via %s: %v\n", restartTarget, err)
			}
			return 1
		}
		if jsonOut {
			writeJSONCLI(out, map[string]any{
				"ok":        true,
				"timestamp": now().Format(time.RFC3339),
				"component": "ollama",
				"status":    "restarted",
				"details": map[string]any{
					"base_url": cfg.Ollama.BaseURL,
					"model":    cfg.Ollama.Model,
					"target":   restartTarget,
				},
			}, true)
		} else {
			fmt.Fprintf(out, "ollama restarted via %s\n", restartTarget)
		}
		return 0
	default:
		fmt.Fprintf(errOut, "unknown ollama subcommand: %s\n", subcmd)
		fmt.Fprintln(errOut, "usage: picoclaw ollama [status|restart]")
		return 1
	}
}

func gatewayHealthURL(cfg *config.Config) string {
	host := strings.TrimSpace(cfg.Server.Host)
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s:%d/health", host, cfg.Server.Port)
}

func buildOllamaRestartAction(cfg *config.Config) (string, func() error, error) {
	u, err := url.Parse(strings.TrimSpace(cfg.Ollama.BaseURL))
	if err != nil {
		return "", nil, fmt.Errorf("invalid ollama.base_url: %w", err)
	}
	host := strings.TrimSpace(u.Hostname())
	if host == "" {
		host = "127.0.0.1"
	}
	restartCmd := strings.TrimSpace(os.Getenv("PICOCLAW_OLLAMA_RESTART_CMD"))
	if restartCmd == "" {
		restartCmd = "sudo systemctl restart ollama"
	}
	if isLocalOllamaHost(host) {
		return "local systemctl", func() error {
			return exec.Command("bash", "-lc", restartCmd).Run()
		}, nil
	}

	sshUser := strings.TrimSpace(os.Getenv("PICOCLAW_OLLAMA_SSH_USER"))
	if sshUser == "" {
		sshUser = strings.TrimSpace(os.Getenv("USER"))
	}
	if sshUser == "" {
		return "", nil, fmt.Errorf("PICOCLAW_OLLAMA_SSH_USER is required for remote ollama restart")
	}

	sshArgs := []string{fmt.Sprintf("%s@%s", sshUser, host), restartCmd}
	if keyPath := strings.TrimSpace(os.Getenv("PICOCLAW_OLLAMA_SSH_KEY_PATH")); keyPath != "" {
		sshArgs = append([]string{"-i", keyPath}, sshArgs...)
	}

	target := fmt.Sprintf("ssh %s@%s", sshUser, host)
	return target, func() error {
		return exec.Command("ssh", sshArgs...).Run()
	}, nil
}

func isLocalOllamaHost(host string) bool {
	switch strings.TrimSpace(strings.ToLower(host)) {
	case "", "localhost", "127.0.0.1", "::1", "0.0.0.0":
		return true
	default:
		return false
	}
}
