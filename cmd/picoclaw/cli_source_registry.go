package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation/l1sqlite"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/sourcefetcher"
)

func cmdSourceRegistry() {
	configPath := getConfigPath()
	store, err := loadSourceRegistryStore(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialize source registry store: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()
	code := runSourceRegistryCommand(os.Args[2:], store, os.Stdout, os.Stderr)
	if code != 0 {
		os.Exit(code)
	}
}

type sourceRegistryCLIStore interface {
	SaveSourceRegistryEntry(ctx context.Context, entry l1sqlite.L1SourceRegistryEntry) (*l1sqlite.L1SourceRegistryEntry, error)
	ListSourceRegistryEntries(ctx context.Context, enabledOnly bool) ([]l1sqlite.L1SourceRegistryEntry, error)
}

func runSourceRegistryCommand(args []string, store sourceRegistryCLIStore, out io.Writer, errOut io.Writer) int {
	subcmd := "list"
	if len(args) > 0 {
		subcmd = strings.ToLower(strings.TrimSpace(args[0]))
	}
	switch subcmd {
	case "list":
		jsonOut := hasFlag(args[1:], "--json")
		enabledOnly := hasFlag(args[1:], "--enabled-only")
		entries, err := store.ListSourceRegistryEntries(context.Background(), enabledOnly)
		if err != nil {
			fmt.Fprintf(errOut, "failed to list source registry: %v\n", err)
			return 1
		}
		if jsonOut {
			writeJSONCLI(out, map[string]any{"entries": sourceRegistryCLIEntries(entries)}, false)
			return 0
		}
		if len(entries) == 0 {
			fmt.Fprintln(out, "No source registry entries")
			return 0
		}
		for _, entry := range entries {
			fmt.Fprintf(out, "%s | %s | %.2f | %s | enabled=%v\n", entry.SourceID, entry.Kind, entry.TrustScore, entry.URL, entry.Enabled)
		}
		return 0
	case "save":
		entry, jsonOut, err := parseSourceRegistrySaveArgs(args[1:])
		if err != nil {
			fmt.Fprintf(errOut, "%v\n", err)
			return 1
		}
		saved, err := store.SaveSourceRegistryEntry(context.Background(), entry)
		if err != nil {
			fmt.Fprintf(errOut, "failed to save source registry: %v\n", err)
			return 1
		}
		if jsonOut {
			writeJSONCLI(out, map[string]any{"entry": sourceRegistryCLIEntry(*saved)}, false)
			return 0
		}
		fmt.Fprintf(out, "saved source registry entry: %s\n", saved.SourceID)
		return 0
	case "disable":
		sourceID, jsonOut, err := parseSourceRegistryDisableArgs(args[1:])
		if err != nil {
			fmt.Fprintf(errOut, "%v\n", err)
			return 1
		}
		entries, err := store.ListSourceRegistryEntries(context.Background(), false)
		if err != nil {
			fmt.Fprintf(errOut, "failed to list source registry: %v\n", err)
			return 1
		}
		var target *l1sqlite.L1SourceRegistryEntry
		for i := range entries {
			if entries[i].SourceID == sourceID {
				target = &entries[i]
				break
			}
		}
		if target == nil {
			fmt.Fprintf(errOut, "source registry entry not found: %s\n", sourceID)
			return 1
		}
		target.Enabled = false
		saved, err := store.SaveSourceRegistryEntry(context.Background(), *target)
		if err != nil {
			fmt.Fprintf(errOut, "failed to disable source registry: %v\n", err)
			return 1
		}
		if jsonOut {
			writeJSONCLI(out, map[string]any{"entry": sourceRegistryCLIEntry(*saved)}, false)
			return 0
		}
		fmt.Fprintf(out, "disabled source registry entry: %s\n", saved.SourceID)
		return 0
	case "sweep":
		registryStore, ok := store.(sourcefetcher.RegistryStore)
		if !ok {
			fmt.Fprintln(errOut, "source registry store does not support sweep")
			return 1
		}
		opts, jsonOut, err := parseSourceRegistrySweepArgs(args[1:])
		if err != nil {
			fmt.Fprintf(errOut, "%v\n", err)
			return 1
		}
		result, err := sourcefetcher.SweepDueSources(context.Background(), registryStore, time.Now().UTC(), opts)
		if err != nil {
			fmt.Fprintf(errOut, "failed to sweep source registry: %v\n", err)
			return 1
		}
		if jsonOut {
			writeJSONCLI(out, map[string]any{"result": sourceRegistrySweepResultCLI(result)}, false)
			return 0
		}
		fmt.Fprintf(out, "sweep complete: sources=%d staged=%d validated=%d promoted_news=%d failed=%d\n",
			result.Sources, result.Staged, result.Validated, result.PromotedNews, result.Failed)
		return 0
	default:
		fmt.Fprintf(errOut, "unknown source-registry subcommand: %s\n", subcmd)
		fmt.Fprintln(errOut, "usage: picoclaw source-registry [list|save|disable|sweep]")
		return 1
	}
}

func parseSourceRegistrySaveArgs(args []string) (l1sqlite.L1SourceRegistryEntry, bool, error) {
	values := map[string]string{}
	jsonOut := false
	enabled := true
	for i := 0; i < len(args); i++ {
		key := strings.TrimSpace(args[i])
		switch key {
		case "--json":
			jsonOut = true
		case "--disabled":
			enabled = false
		case "--source-id", "--url", "--kind", "--trust-score", "--interval-sec", "--license-note", "--namespace":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return l1sqlite.L1SourceRegistryEntry{}, jsonOut, fmt.Errorf("%s requires a value", key)
			}
			values[key] = strings.TrimSpace(args[i+1])
			i++
		default:
			return l1sqlite.L1SourceRegistryEntry{}, jsonOut, fmt.Errorf("unknown source-registry save option: %s", key)
		}
	}
	sourceID := values["--source-id"]
	sourceURL := values["--url"]
	kind := values["--kind"]
	licenseNote := values["--license-note"]
	if sourceID == "" || sourceURL == "" || kind == "" || licenseNote == "" {
		return l1sqlite.L1SourceRegistryEntry{}, jsonOut, errors.New("source-id, url, kind, license-note are required")
	}
	trustScore := 0.5
	if raw := values["--trust-score"]; raw != "" {
		parsed, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return l1sqlite.L1SourceRegistryEntry{}, jsonOut, fmt.Errorf("invalid --trust-score: %s", raw)
		}
		trustScore = parsed
	}
	interval := time.Hour
	if raw := values["--interval-sec"]; raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			return l1sqlite.L1SourceRegistryEntry{}, jsonOut, fmt.Errorf("invalid --interval-sec: %s", raw)
		}
		interval = time.Duration(parsed) * time.Second
	}
	meta := map[string]interface{}{}
	if namespace := values["--namespace"]; namespace != "" {
		meta["namespace"] = namespace
	}
	return l1sqlite.L1SourceRegistryEntry{
		SourceID:      sourceID,
		URL:           sourceURL,
		Kind:          kind,
		TrustScore:    trustScore,
		FetchInterval: interval,
		LicenseNote:   licenseNote,
		Enabled:       enabled,
		Meta:          meta,
	}, jsonOut, nil
}

func parseSourceRegistryDisableArgs(args []string) (string, bool, error) {
	sourceID := ""
	jsonOut := false
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch arg {
		case "--json":
			jsonOut = true
		case "--source-id":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return "", jsonOut, errors.New("--source-id requires a value")
			}
			sourceID = strings.TrimSpace(args[i+1])
			i++
		default:
			if strings.HasPrefix(arg, "--") {
				return "", jsonOut, fmt.Errorf("unknown source-registry disable option: %s", arg)
			}
			if sourceID == "" {
				sourceID = arg
			}
		}
	}
	if sourceID == "" {
		return "", jsonOut, errors.New("source-id is required")
	}
	return sourceID, jsonOut, nil
}

func parseSourceRegistrySweepArgs(args []string) (sourcefetcher.SweepOptions, bool, error) {
	opts := sourcefetcher.SweepOptions{LimitPerSource: 10, MinimumTrustScore: 0.5}
	jsonOut := false
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch arg {
		case "--json":
			jsonOut = true
		case "--limit":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return opts, jsonOut, errors.New("--limit requires a value")
			}
			n, err := strconv.Atoi(strings.TrimSpace(args[i+1]))
			if err != nil || n <= 0 {
				return opts, jsonOut, fmt.Errorf("invalid --limit: %s", args[i+1])
			}
			opts.LimitPerSource = n
			i++
		case "--min-trust":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return opts, jsonOut, errors.New("--min-trust requires a value")
			}
			n, err := strconv.ParseFloat(strings.TrimSpace(args[i+1]), 64)
			if err != nil || n < 0 || n > 1 {
				return opts, jsonOut, fmt.Errorf("invalid --min-trust: %s", args[i+1])
			}
			opts.MinimumTrustScore = n
			i++
		default:
			return opts, jsonOut, fmt.Errorf("unknown source-registry sweep option: %s", arg)
		}
	}
	return opts, jsonOut, nil
}

func sourceRegistrySweepResultCLI(result sourcefetcher.SweepResult) map[string]any {
	return map[string]any{
		"sources":            result.Sources,
		"staged":             result.Staged,
		"validated":          result.Validated,
		"promoted_news":      result.PromotedNews,
		"promoted_knowledge": result.PromotedKnowledge,
		"failed":             result.Failed,
	}
}

func sourceRegistryCLIEntries(entries []l1sqlite.L1SourceRegistryEntry) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		out = append(out, sourceRegistryCLIEntry(entry))
	}
	return out
}

func sourceRegistryCLIEntry(entry l1sqlite.L1SourceRegistryEntry) map[string]any {
	return map[string]any{
		"source_id":          entry.SourceID,
		"url":                entry.URL,
		"kind":               entry.Kind,
		"trust_score":        entry.TrustScore,
		"fetch_interval_sec": int64(entry.FetchInterval.Seconds()),
		"license_note":       entry.LicenseNote,
		"enabled":            entry.Enabled,
		"meta":               entry.Meta,
	}
}

func loadSourceRegistryStore(configPath string) (*l1sqlite.L1SQLiteStore, error) {
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return nil, err
	}
	p := strings.TrimSpace(cfg.Conversation.L1SQLitePath)
	if p == "" {
		return nil, errors.New("conversation.l1_sqlite_path is required for source-registry CLI")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	return l1sqlite.NewL1SQLiteStore(p)
}
