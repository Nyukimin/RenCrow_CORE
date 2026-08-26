package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	adapterchannels "github.com/Nyukimin/RenCrow_CORE/internal/adapter/channels"
	discordadapter "github.com/Nyukimin/RenCrow_CORE/internal/adapter/channels/discord"
	slackadapter "github.com/Nyukimin/RenCrow_CORE/internal/adapter/channels/slack"
	telegramadapter "github.com/Nyukimin/RenCrow_CORE/internal/adapter/channels/telegram"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/line"
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
	if len(os.Args) > 2 && strings.EqualFold(strings.TrimSpace(os.Args[2]), "send") {
		registry = buildOutboundChannelRegistry(cfg)
	}
	destination, destinationErr := resolveNotificationDestination(cfg)
	code := runChannelsCommandWithDestination(
		os.Args[2:],
		registry,
		destination,
		destinationErr,
		os.Stdout,
		os.Stderr,
		func() time.Time { return time.Now().UTC() },
	)
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

type outboundChannelRegistry interface {
	channelRegistry
	Get(name string) (adapterchannels.Adapter, bool)
}

func runChannelsCommand(
	args []string,
	registry channelRegistry,
	out io.Writer,
	errOut io.Writer,
	now func() time.Time,
) int {
	return runChannelsCommandWithDestination(
		args,
		registry,
		notificationDestination{},
		fmt.Errorf("notification destination is unavailable"),
		out,
		errOut,
		now,
	)
}

func runChannelsCommandWithDestination(
	args []string,
	registry channelRegistry,
	destination notificationDestination,
	destinationErr error,
	out io.Writer,
	errOut io.Writer,
	now func() time.Time,
) int {
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
	case "send":
		message, ok := channelFlagValue(args[1:], "--message")
		message = strings.TrimSpace(message)
		if !ok || message == "" {
			fmt.Fprintln(errOut, "channels send requires a non-empty --message")
			return 1
		}
		if destinationErr != nil {
			if jsonOut {
				writeJSONCLI(out, map[string]any{
					"ok":        false,
					"timestamp": now().Format(time.RFC3339),
					"component": "channels",
					"status":    "unavailable",
					"code":      "E_NOTIFICATION_DESTINATION_UNAVAILABLE",
				}, true)
				fmt.Fprintln(errOut, "notification destination unavailable")
			} else {
				fmt.Fprintf(errOut, "notification destination unavailable: %v\n", destinationErr)
			}
			return 1
		}
		outboundRegistry, ok := registry.(outboundChannelRegistry)
		if !ok {
			fmt.Fprintln(errOut, "notification channel registry does not support sending")
			return 1
		}
		adapter, ok := outboundRegistry.Get(destination.Channel)
		if !ok {
			fmt.Fprintf(errOut, "notification channel adapter is not configured: %s\n", destination.Channel)
			return 1
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := adapter.Probe(ctx); err != nil {
			fmt.Fprintf(errOut, "notification channel probe failed: %v\n", err)
			return 1
		}
		maskedTarget := line.MaskTargetID(destination.ChatID)
		fmt.Fprintf(
			errOut,
			"notification target=%s type=%s channel=%s source=%s\n",
			maskedTarget,
			destination.TargetType,
			destination.Channel,
			destination.Source,
		)
		dryRun := hasFlag(args, "--dry-run")
		if !dryRun {
			if err := adapter.Send(ctx, destination.ChatID, message); err != nil {
				fmt.Fprintf(errOut, "notification send failed: %v\n", err)
				return 1
			}
		}
		status := "sent"
		if dryRun {
			status = "dry_run"
		}
		if jsonOut {
			writeJSONCLI(out, map[string]any{
				"ok":        true,
				"timestamp": now().Format(time.RFC3339),
				"component": "channels",
				"status":    status,
				"details": map[string]any{
					"channel":     destination.Channel,
					"target":      maskedTarget,
					"target_type": destination.TargetType,
					"source":      destination.Source,
				},
			}, true)
			return 0
		}
		if dryRun {
			fmt.Fprintf(out, "Notification dry-run passed: %s %s\n", destination.Channel, maskedTarget)
		} else {
			fmt.Fprintf(out, "Notification sent: %s %s\n", destination.Channel, maskedTarget)
		}
		return 0
	default:
		fmt.Fprintf(errOut, "unknown channels subcommand: %s\n", subcmd)
		fmt.Fprintln(errOut, "usage: rencrow channels [list|probe|send --message TEXT [--dry-run] [--json]]")
		return 1
	}
}

func channelFlagValue(args []string, name string) (string, bool) {
	for i, arg := range args {
		if arg == name && i+1 < len(args) {
			return args[i+1], true
		}
		if strings.HasPrefix(arg, name+"=") {
			return strings.TrimPrefix(arg, name+"="), true
		}
	}
	return "", false
}
