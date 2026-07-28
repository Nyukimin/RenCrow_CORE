package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
)

func cmdGateway() {
	cfg, err := config.LoadConfig(getConfigPath())
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	getStatus := func(url string) (int, error) {
		client := &http.Client{Timeout: 2 * time.Second}
		resp, err := client.Get(url) //nolint:gosec // local CORE health probe
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		return resp.StatusCode, nil
	}
	restart := func() error {
		return exec.Command("systemctl", "restart", "rencrow.service").Run()
	}
	code := runGatewayCommand(os.Args[2:], cfg, os.Stdout, os.Stderr, getStatus, restart, func() time.Time { return time.Now().UTC() })
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
					"ok": false, "timestamp": now().Format(time.RFC3339), "component": "gateway",
					"status": "down", "code": "E_GATEWAY_UNREACHABLE",
					"hint": "rencrow gateway restart を実行",
					"details": map[string]any{"url": url, "error": err.Error()},
				}, true)
			} else {
				fmt.Fprintf(out, "[DOWN] gateway health check failed: %v\n", err)
			}
			return 1
		}
		if code >= 200 && code < 300 {
			if jsonOut {
				writeJSONCLI(out, map[string]any{
					"ok": true, "timestamp": now().Format(time.RFC3339), "component": "gateway",
					"status": "running", "details": map[string]any{"url": url, "status_code": code},
				}, true)
			} else {
				fmt.Fprintf(out, "[OK] gateway reachable: %s (%d)\n", url, code)
			}
			return 0
		}
		if jsonOut {
			writeJSONCLI(out, map[string]any{
				"ok": false, "timestamp": now().Format(time.RFC3339), "component": "gateway",
				"status": "down", "code": "E_GATEWAY_UNHEALTHY",
				"hint": "health endpoint と logs を確認",
				"details": map[string]any{"url": url, "status_code": code},
			}, true)
		} else {
			fmt.Fprintf(out, "[DOWN] gateway unhealthy: %s (%d)\n", url, code)
		}
		return 1
	case "restart":
		if err := restart(); err != nil {
			if jsonOut {
				writeJSONCLI(out, map[string]any{
					"ok": false, "timestamp": now().Format(time.RFC3339), "component": "gateway",
					"status": "down", "code": "E_GATEWAY_RESTART_FAILED",
					"hint": "systemctl権限とサービス名を確認",
					"details": map[string]any{"error": err.Error()},
				}, true)
			} else {
				fmt.Fprintf(errOut, "failed to restart via systemctl: %v\n", err)
			}
			return 1
		}
		if jsonOut {
			writeJSONCLI(out, map[string]any{
				"ok": true, "timestamp": now().Format(time.RFC3339), "component": "gateway",
				"status": "restarted", "details": map[string]any{},
			}, true)
		} else {
			fmt.Fprintln(out, "rencrow.service restarted")
		}
		return 0
	default:
		fmt.Fprintf(errOut, "unknown gateway subcommand: %s\n", subcmd)
		fmt.Fprintln(errOut, "usage: rencrow gateway [status|restart]")
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
