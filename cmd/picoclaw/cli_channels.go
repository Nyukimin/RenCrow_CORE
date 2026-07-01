package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	adapterchannels "github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/channels"
	discordadapter "github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/channels/discord"
	slackadapter "github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/channels/slack"
	telegramadapter "github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/channels/telegram"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/line"
)

func lineWebhookConfigured(cfg *config.Config) bool {
	return strings.TrimSpace(cfg.Line.ChannelSecret) != "" && strings.TrimSpace(cfg.Line.AccessToken) != ""
}

func cmdChannels() {
	cfg, err := config.LoadConfig(getConfigPath())
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	registry := buildChannelRegistry(cfg)
	code := runChannelsCommand(os.Args[2:], registry, os.Stdout, os.Stderr, func() time.Time { return time.Now().UTC() })
	if code != 0 {
		os.Exit(code)
	}
}

func buildChannelRegistry(cfg *config.Config) *adapterchannels.Registry {
	registry := adapterchannels.NewRegistry()
	if lineWebhookConfigured(cfg) {
		_ = registry.Register(line.NewHandler(nil, cfg.Line.ChannelSecret, cfg.Line.AccessToken))
	}
	if strings.TrimSpace(cfg.Telegram.BotToken) != "" {
		_ = registry.Register(telegramadapter.NewAdapter(cfg.Telegram.BotToken))
	}
	if strings.TrimSpace(cfg.Discord.BotToken) != "" {
		_ = registry.Register(discordadapter.NewAdapter(cfg.Discord.BotToken))
	}
	if strings.TrimSpace(cfg.Slack.BotToken) != "" {
		_ = registry.Register(slackadapter.NewAdapter(cfg.Slack.BotToken, cfg.Slack.SigningSecret))
	}
	return registry
}

func buildOutboundChannelRegistry(cfg *config.Config) *adapterchannels.Registry {
	registry := adapterchannels.NewRegistry()
	if strings.TrimSpace(cfg.Line.AccessToken) != "" {
		_ = registry.Register(line.NewHandler(nil, cfg.Line.ChannelSecret, cfg.Line.AccessToken))
	}
	if strings.TrimSpace(cfg.Telegram.BotToken) != "" {
		_ = registry.Register(telegramadapter.NewAdapter(cfg.Telegram.BotToken))
	}
	if strings.TrimSpace(cfg.Discord.BotToken) != "" {
		_ = registry.Register(discordadapter.NewAdapter(cfg.Discord.BotToken))
	}
	if strings.TrimSpace(cfg.Slack.BotToken) != "" {
		_ = registry.Register(slackadapter.NewAdapter(cfg.Slack.BotToken, cfg.Slack.SigningSecret))
	}
	return registry
}

type channelRegistry interface {
	List() []string
	ProbeAll(ctx context.Context) map[string]error
}

func runChannelsCommand(args []string, registry channelRegistry, out io.Writer, errOut io.Writer, now func() time.Time) int {
	subcmd := "list"
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		subcmd = strings.ToLower(strings.TrimSpace(args[0]))
	}
	jsonOut := hasFlag(args, "--json")

	switch subcmd {
	case "list":
		names := registry.List()
		if jsonOut {
			status := "empty"
			if len(names) > 0 {
				status = "configured"
			}
			writeJSONCLI(out, map[string]any{
				"ok":        true,
				"timestamp": now().Format(time.RFC3339),
				"component": "channels",
				"status":    status,
				"details": map[string]any{
					"channels": names,
				},
			}, true)
			return 0
		}
		if len(names) == 0 {
			fmt.Fprintln(out, "No channels configured")
			return 0
		}
		fmt.Fprintln(out, "Configured channels:")
		for _, name := range names {
			fmt.Fprintf(out, "  - %s\n", name)
		}
		return 0
	case "probe":
		results := registry.ProbeAll(context.Background())
		names := registry.List()
		if len(results) == 0 {
			if jsonOut {
				writeJSONCLI(out, map[string]any{
					"ok":        true,
					"timestamp": now().Format(time.RFC3339),
					"component": "channels",
					"status":    "empty",
					"details": map[string]any{
						"results": map[string]any{},
					},
				}, true)
				return 0
			}
			fmt.Fprintln(out, "No channels configured")
			return 0
		}
		hasErr := false
		perChannel := make(map[string]map[string]any, len(names))
		for _, name := range names {
			err := results[name]
			if err != nil {
				hasErr = true
				perChannel[name] = map[string]any{"ok": false, "error": err.Error()}
				if !jsonOut {
					fmt.Fprintf(out, "[DOWN] %s: %v\n", name, err)
				}
				continue
			}
			perChannel[name] = map[string]any{"ok": true}
			if !jsonOut {
				fmt.Fprintf(out, "[OK] %s\n", name)
			}
		}
		if jsonOut {
			status := "ok"
			if hasErr {
				status = "degraded"
			}
			writeJSONCLI(out, map[string]any{
				"ok":        !hasErr,
				"timestamp": now().Format(time.RFC3339),
				"component": "channels",
				"status":    status,
				"details": map[string]any{
					"results": perChannel,
				},
			}, true)
		}
		if hasErr {
			return 1
		}
		return 0
	default:
		fmt.Fprintf(errOut, "unknown channels subcommand: %s\n", subcmd)
		fmt.Fprintln(errOut, "usage: picoclaw channels [list|probe]")
		return 1
	}
}
