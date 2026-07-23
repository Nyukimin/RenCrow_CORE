package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

func cmdVersion() {
	fmt.Printf("rencrow %s\ncommit: %s\nbuilt:  %s\n", Version, Commit, BuildDate)
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if strings.EqualFold(strings.TrimSpace(a), flag) {
			return true
		}
	}
	return false
}

func writeJSONCLI(out io.Writer, v any, pretty bool) {
	enc := json.NewEncoder(out)
	if pretty {
		enc.SetIndent("", "  ")
	}
	_ = enc.Encode(v)
}

func cmdHelp() {
	fmt.Printf(`RenCrow %s - Multi-LLM AI Assistant (RenCrow)

Usage: rencrow [command]

Commands:
  run       Start the HTTP server (default)
  version   Show version information
  health    Run health checks and output JSON
  status    Show system status overview
  doctor    Diagnose config and runtime prerequisites
  config    Read validated runtime configuration
  resilience  Inspect/reconcile restart and self-repair incidents
  channels  List/probe channel adapters
  gateway   Gateway status/restart operations
  ollama    Ollama status/restart operations
  logs      Show logs (use --follow to stream)
  chat      Chat with the running RenCrow server from the terminal
  evidence  List/show/summarize execution evidence
  jobs      Manage Mio parallel jobs and interrupt notifications
  source-registry  List/register L1 source registry entries
  web-gather  Fetch public web pages into pending L1 staging
  browser-actor  Operate an allowlisted browser session from JSON
  knowledge  Import Knowledge DB seed data
  help      Show this help message

Agent Mode:
  Use rencrow-agent binary for distributed execution.
  See install-agent.sh or install-agent.ps1 for setup.
`, Version)
}

// buildHealthService は HealthService を構築（CLI コマンドで共用）
