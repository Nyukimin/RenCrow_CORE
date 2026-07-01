package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	browseractorinfra "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/browseractor"
	modulebrowser "github.com/Nyukimin/picoclaw_multiLLM/modules/browseractor"
)

type browserActorRunner interface {
	Run(ctx context.Context, req modulebrowser.RunRequest) (modulebrowser.RunResponse, error)
	Doctor(ctx context.Context) (modulebrowser.DoctorResponse, error)
}

type browserActorCLIDeps struct {
	Config config.BrowserActorConfig
	Runner browserActorRunner
}

func cmdBrowserActor() {
	cfg, err := config.LoadConfig(getConfigPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}
	runner := browseractorinfra.NewRunner(browserActorConfigFromRuntime(cfg.BrowserActor))
	code := runBrowserActorCommand(os.Args[2:], browserActorCLIDeps{Config: cfg.BrowserActor, Runner: runner}, os.Stdin, os.Stdout, os.Stderr)
	if code != 0 {
		os.Exit(code)
	}
}

func browserActorConfigFromRuntime(cfg config.BrowserActorConfig) browseractorinfra.Config {
	return browseractorinfra.Config{
		Enabled:         cfg.Enabled,
		RunnerPath:      cfg.RunnerPath,
		NodeBinary:      cfg.NodeBinary,
		Browser:         cfg.Browser,
		HeadlessDefault: cfg.HeadlessDefaultEnabled(),
		ProfileRoot:     cfg.ProfileRoot,
		ArtifactRoot:    cfg.ArtifactRoot,
		TimeoutMS:       cfg.TimeoutMS,
		MaxActions:      cfg.MaxActions,
		NetworkScope:    cfg.NetworkScope,
		AllowedOrigins:  cfg.AllowedOrigins,
		SaveTrace:       cfg.SaveTraceEnabled(),
		SaveScreenshot:  cfg.SaveScreenshotEnabled(),
		MaskSecrets:     cfg.MaskSecretsEnabled(),
	}
}

func runBrowserActorCommand(args []string, deps browserActorCLIDeps, in io.Reader, out io.Writer, errOut io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errOut, "usage: picoclaw browser-actor [run|doctor] --json")
		return 2
	}
	if deps.Runner == nil {
		fmt.Fprintln(errOut, "browser actor runner is not configured")
		return 1
	}
	subcmd := strings.ToLower(strings.TrimSpace(args[0]))
	switch subcmd {
	case "doctor":
		jsonOut := hasFlag(args[1:], "--json")
		resp, err := deps.Runner.Doctor(context.Background())
		if jsonOut {
			writeJSONCLI(out, resp, true)
		} else {
			writeBrowserActorDoctorText(out, resp)
		}
		if err != nil {
			fmt.Fprintf(errOut, "browser-actor doctor failed: %v\n", err)
			return 1
		}
		if !resp.OK {
			return 1
		}
		return 0
	case "run":
		if !hasFlag(args[1:], "--json") {
			fmt.Fprintln(errOut, "browser-actor run requires --json")
			return 2
		}
		if !deps.Config.Enabled {
			fmt.Fprintln(errOut, "browser_actor.enabled=true is required for browser-actor run")
			return 1
		}
		var req modulebrowser.RunRequest
		if err := json.NewDecoder(in).Decode(&req); err != nil {
			fmt.Fprintf(errOut, "invalid browser actor JSON input: %v\n", err)
			return 2
		}
		req = applyBrowserActorCLIConfig(req, deps.Config)
		resp, err := deps.Runner.Run(context.Background(), req)
		writeJSONCLI(out, resp, true)
		if err != nil {
			fmt.Fprintf(errOut, "browser-actor run failed: %v\n", err)
			return 1
		}
		if resp.Status != modulebrowser.StatusCompleted {
			return 1
		}
		return 0
	default:
		fmt.Fprintf(errOut, "unknown browser-actor command: %s\n", subcmd)
		return 2
	}
}

func applyBrowserActorCLIConfig(req modulebrowser.RunRequest, cfg config.BrowserActorConfig) modulebrowser.RunRequest {
	if req.TimeoutMS == 0 {
		req.TimeoutMS = cfg.TimeoutMS
	}
	if req.MaxActions == 0 {
		req.MaxActions = cfg.MaxActions
	}
	if len(req.AllowedOrigins) == 0 {
		req.AllowedOrigins = append([]string(nil), cfg.AllowedOrigins...)
	}
	if cfg.HeadlessDefaultEnabled() {
		req.Headless = true
	}
	if cfg.SaveTraceEnabled() {
		req.SaveTrace = true
	}
	if cfg.SaveScreenshotEnabled() {
		req.SaveScreenshot = true
	}
	if cfg.MaskSecretsEnabled() {
		req.MaskSecrets = true
	}
	if strings.TrimSpace(req.ArtifactDir) == "" && strings.TrimSpace(req.RunID) != "" {
		req.ArtifactDir = strings.TrimRight(cfg.ArtifactRoot, "/") + "/" + req.RunID
	}
	return req
}

func writeBrowserActorDoctorText(out io.Writer, result modulebrowser.DoctorResponse) {
	fmt.Fprintf(out, "browser-actor doctor: ok=%v\n", result.OK)
	for _, check := range result.Checks {
		if strings.TrimSpace(check.Detail) == "" {
			fmt.Fprintf(out, "- %s: %s\n", check.Name, check.Status)
			continue
		}
		fmt.Fprintf(out, "- %s: %s (%s)\n", check.Name, check.Status, check.Detail)
	}
}
