package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation/l1sqlite"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/sourcefetcher"
	webgatherapp "github.com/Nyukimin/picoclaw_multiLLM/internal/application/webgather"
	webgatherinfra "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/webgather"
	modulewebgather "github.com/Nyukimin/picoclaw_multiLLM/modules/webgather"
)

func cmdWebGather() {
	configPath := getConfigPath()
	cfg, store, err := loadWebGatherStore(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialize web gather store: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()
	usecase := webgatherapp.NewUseCase(
		webgatherinfra.NewHTTPFetcher(),
		webgatherinfra.NewBasicExtractor(),
		webgatherapp.NewL1StagingWriter(store),
	).WithFetchCache(webgatherapp.NewL1FetchCache(store))
	if cfg.WebwrightFetch.Enabled {
		usecase.WithFetchProvider("webwright", webgatherinfra.NewWebwrightFetcher(webwrightFetcherConfigFromRuntime(cfg.WebwrightFetch)))
	}
	code := runWebGatherCommand(os.Args[2:], webGatherCLIDeps{
		Fetcher:        usecase,
		SearchCache:    webgatherapp.NewL1SearchCache(store),
		SourceRegistry: store,
		SourceRunner:   store,
		StagingStore:   store,
		WebwrightFetch: cfg.WebwrightFetch,
		CommandRunner:  execWebGatherCommand,
		SearXNGBaseURL: strings.TrimSpace(cfg.WebGather.SearXNGBaseURL),
		YaCyBaseURL:    strings.TrimSpace(cfg.WebGather.YaCyBaseURL),
	}, os.Stdout, os.Stderr)
	if code != 0 {
		os.Exit(code)
	}
}

func webwrightFetcherConfigFromRuntime(cfg config.WebwrightFetchConfig) webgatherinfra.WebwrightFetcherConfig {
	return webgatherinfra.WebwrightFetcherConfig{
		Enabled:           cfg.Enabled,
		RunnerPath:        cfg.RunnerPath,
		ConfigPath:        cfg.ConfigPath,
		OutputDir:         cfg.OutputDir,
		StagingOutputDir:  cfg.StagingOutputDir,
		UvxFrom:           cfg.UvxFrom,
		Python:            cfg.Python,
		ResponsesEndpoint: cfg.ResponsesEndpoint,
		Model:             cfg.Model,
		APIKey:            cfg.APIKey,
	}
}

type webGatherFetcher interface {
	FetchURL(ctx context.Context, req modulewebgather.FetchRequest) (modulewebgather.FetchResponse, error)
}

type webGatherSearcher interface {
	Search(ctx context.Context, req modulewebgather.SearchRequest) (modulewebgather.SearchResponse, error)
}

type webGatherSourceRegistry interface {
	SaveSourceRegistryEntry(ctx context.Context, entry l1sqlite.L1SourceRegistryEntry) (*l1sqlite.L1SourceRegistryEntry, error)
}

type webGatherSourceRunner interface {
	sourcefetcher.RegistryStore
	sourcefetcher.RegistrySourceLister
}

type webGatherStagingStore interface {
	SaveStagingItem(ctx context.Context, item l1sqlite.L1StagingItem) (*l1sqlite.L1StagingItem, error)
}

type webGatherCommandRunner func(ctx context.Context, command string, args []string, out io.Writer, errOut io.Writer) int

type webGatherCLIDeps struct {
	Fetcher        webGatherFetcher
	SearchCache    webgatherapp.SearchCache
	SourceRegistry webGatherSourceRegistry
	SourceRunner   webGatherSourceRunner
	StagingStore   webGatherStagingStore
	WebwrightFetch config.WebwrightFetchConfig
	CommandRunner  webGatherCommandRunner
	SearXNGBaseURL string
	YaCyBaseURL    string
}

func runWebGatherCommand(args []string, deps webGatherCLIDeps, out io.Writer, errOut io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errOut, "usage: picoclaw web-gather [url|search|search-and-fetch|register-url|run-source|webwright-fetch|import-webwright-jsonl|doctor] ...")
		return 2
	}
	subcmd := strings.ToLower(strings.TrimSpace(args[0]))
	switch subcmd {
	case "doctor":
		jsonOut := hasFlag(args[1:], "--json")
		result := runWebGatherDoctor(context.Background(), deps)
		if jsonOut {
			writeJSONCLI(out, result, false)
		} else {
			writeWebGatherDoctorText(out, result)
		}
		if !result.OK {
			return 1
		}
		return 0
	case "url":
		req, jsonOut, err := parseWebGatherURLArgs(args[1:])
		if err != nil {
			fmt.Fprintf(errOut, "%v\n", err)
			return 2
		}
		if deps.Fetcher == nil {
			fmt.Fprintln(errOut, "web gather fetcher is not configured")
			return 1
		}
		resp, err := deps.Fetcher.FetchURL(context.Background(), req)
		if jsonOut {
			writeJSONCLI(out, resp, true)
		}
		if err != nil {
			if !jsonOut {
				fmt.Fprintf(errOut, "web-gather failed: %s: %s\n", resp.ErrorCode, resp.ErrorMessage)
			}
			return 1
		}
		if !jsonOut {
			fmt.Fprintf(out, "web gather staged: %s | %s | %s | warnings=%d\n", resp.StagingID, resp.FinalURL, resp.RawHash, len(resp.SecurityWarnings))
		}
		return 0
	case "search":
		req, searxngURL, yacyURL, jsonOut, err := parseWebGatherSearchArgs(args[1:])
		if err != nil {
			fmt.Fprintf(errOut, "%v\n", err)
			return 2
		}
		if strings.TrimSpace(searxngURL) == "" {
			searxngURL = deps.SearXNGBaseURL
		}
		if strings.TrimSpace(yacyURL) == "" {
			yacyURL = deps.YaCyBaseURL
		}
		if req.Provider == "searxng" && strings.TrimSpace(searxngURL) == "" {
			fmt.Fprintln(errOut, "web_gather.searxng_base_url or --searxng-url is required when --provider searxng")
			return 2
		}
		if req.Provider == "yacy" && strings.TrimSpace(yacyURL) == "" {
			fmt.Fprintln(errOut, "web_gather.yacy_base_url or --yacy-url is required when --provider yacy")
			return 2
		}
		providers := map[string]modulewebgather.SearchProvider{}
		providers["rss_atom"] = webgatherinfra.NewFeedDiscoveryProvider()
		providers["sitemap"] = webgatherinfra.NewFeedDiscoveryProvider()
		if strings.TrimSpace(searxngURL) != "" {
			providers["searxng"] = webgatherinfra.NewSearXNGProvider(searxngURL)
		}
		if strings.TrimSpace(yacyURL) != "" {
			providers["yacy"] = webgatherinfra.NewYaCyProvider(yacyURL)
		}
		searcher := webgatherapp.NewSearchUseCase(deps.SearchCache, providers)
		resp, err := searcher.Search(context.Background(), req)
		if jsonOut {
			writeJSONCLI(out, resp, true)
		}
		if err != nil {
			if !jsonOut {
				if e := resp.Diagnostics["error"]; e != nil {
					fmt.Fprintf(errOut, "web-gather search failed: %v\n", e)
				} else {
					fmt.Fprintf(errOut, "web-gather search failed: %v\n", err)
				}
			}
			return 1
		}
		if !jsonOut {
			fmt.Fprintf(out, "web gather search: provider=%s results=%d cache_hit=%v\n", resp.Provider, len(resp.Results), resp.Diagnostics["cache_hit"])
			for _, result := range resp.Results {
				fmt.Fprintf(out, "%d. %s\n   %s\n   %s\n", result.Rank, result.Title, result.Snippet, result.URL)
			}
		}
		return 0
	case "search-and-fetch":
		req, searxngURL, yacyURL, jsonOut, err := parseWebGatherSearchAndFetchArgs(args[1:])
		if err != nil {
			fmt.Fprintf(errOut, "%v\n", err)
			return 2
		}
		if strings.TrimSpace(searxngURL) == "" {
			searxngURL = deps.SearXNGBaseURL
		}
		if strings.TrimSpace(yacyURL) == "" {
			yacyURL = deps.YaCyBaseURL
		}
		if req.Provider == "searxng" && strings.TrimSpace(searxngURL) == "" {
			fmt.Fprintln(errOut, "web_gather.searxng_base_url or --searxng-url is required when --provider searxng")
			return 2
		}
		if req.Provider == "yacy" && strings.TrimSpace(yacyURL) == "" {
			fmt.Fprintln(errOut, "web_gather.yacy_base_url or --yacy-url is required when --provider yacy")
			return 2
		}
		if deps.Fetcher == nil {
			fmt.Fprintln(errOut, "web gather fetcher is not configured")
			return 1
		}
		providers := map[string]modulewebgather.SearchProvider{}
		providers["rss_atom"] = webgatherinfra.NewFeedDiscoveryProvider()
		providers["sitemap"] = webgatherinfra.NewFeedDiscoveryProvider()
		if strings.TrimSpace(searxngURL) != "" {
			providers["searxng"] = webgatherinfra.NewSearXNGProvider(searxngURL)
		}
		if strings.TrimSpace(yacyURL) != "" {
			providers["yacy"] = webgatherinfra.NewYaCyProvider(yacyURL)
		}
		searcher := webgatherapp.NewSearchUseCase(deps.SearchCache, providers)
		usecase := webgatherapp.NewSearchAndFetchUseCase(searcher, deps.Fetcher)
		resp, err := usecase.SearchAndFetch(context.Background(), req)
		if jsonOut {
			writeJSONCLI(out, resp, true)
		}
		if err != nil {
			if !jsonOut {
				if e := resp.Diagnostics["error"]; e != nil {
					fmt.Fprintf(errOut, "web-gather search-and-fetch failed: %v\n", e)
				} else {
					fmt.Fprintf(errOut, "web-gather search-and-fetch failed: %v\n", err)
				}
			}
			return 1
		}
		if !jsonOut {
			fmt.Fprintf(out, "web gather search-and-fetch: provider=%s items=%d fetch_errors=%v\n", resp.Provider, len(resp.Items), resp.Diagnostics["fetch_error_cnt"])
			for _, item := range resp.Items {
				fmt.Fprintf(out, "%d. %s\n   %s\n   fetch=%s staging=%s\n", item.SearchResult.Rank, item.SearchResult.Title, item.SearchResult.URL, item.Fetch.Status, item.Fetch.StagingID)
			}
		}
		return 0
	case "register-url":
		entry, jsonOut, err := parseWebGatherRegisterURLArgs(args[1:])
		if err != nil {
			fmt.Fprintf(errOut, "%v\n", err)
			return 2
		}
		if deps.SourceRegistry == nil {
			fmt.Fprintln(errOut, "source registry is not configured")
			return 1
		}
		saved, err := deps.SourceRegistry.SaveSourceRegistryEntry(context.Background(), entry)
		if err != nil {
			fmt.Fprintf(errOut, "failed to register web gather source: %v\n", err)
			return 1
		}
		if jsonOut {
			writeJSONCLI(out, map[string]any{"entry": sourceRegistryCLIEntry(*saved)}, false)
			return 0
		}
		fmt.Fprintf(out, "registered web gather source: %s\n", saved.SourceID)
		return 0
	case "run-source":
		sourceID, jsonOut, err := parseWebGatherRunSourceArgs(args[1:])
		if err != nil {
			fmt.Fprintf(errOut, "%v\n", err)
			return 2
		}
		if deps.SourceRunner == nil {
			fmt.Fprintln(errOut, "web gather source runner is not configured")
			return 1
		}
		result, err := runWebGatherSource(context.Background(), deps.SourceRunner, sourceID, time.Now().UTC())
		if err != nil {
			fmt.Fprintf(errOut, "web-gather run-source failed: %v\n", err)
			return 1
		}
		if jsonOut {
			writeJSONCLI(out, map[string]any{"result": sourceRegistrySweepResultCLI(result)}, false)
			return 0
		}
		fmt.Fprintf(out, "web gather source run complete: sources=%d staged=%d warnings=%d failed=%d\n",
			result.Sources, result.Staged, result.Warnings, result.Failed)
		return 0
	case "webwright-fetch":
		req, err := parseWebGatherWebwrightFetchArgs(args[1:])
		if err != nil {
			fmt.Fprintf(errOut, "%v\n", err)
			return 2
		}
		if !deps.WebwrightFetch.Enabled && !req.DryRun {
			fmt.Fprintln(errOut, "webwright_fetch.enabled=true is required for web-gather webwright-fetch")
			return 1
		}
		if !req.DryRun {
			if err := checkWebwrightResponsesEndpoint(context.Background(), deps.WebwrightFetch.ResponsesEndpoint); err != nil {
				fmt.Fprintf(errOut, "web-gather webwright-fetch preflight failed: %v\n", err)
				return 1
			}
		}
		runner := deps.CommandRunner
		if runner == nil {
			runner = execWebGatherCommand
		}
		command, commandArgs, err := buildWebGatherWebwrightCommand(deps.WebwrightFetch, req)
		if err != nil {
			fmt.Fprintf(errOut, "%v\n", err)
			return 2
		}
		return runner(context.Background(), command, commandArgs, out, errOut)
	case "import-webwright-jsonl":
		path, jsonOut, err := parseWebGatherImportWebwrightJSONLArgs(args[1:])
		if err != nil {
			fmt.Fprintf(errOut, "%v\n", err)
			return 2
		}
		if deps.StagingStore == nil {
			fmt.Fprintln(errOut, "web gather staging store is not configured")
			return 1
		}
		f, err := os.Open(path)
		if err != nil {
			fmt.Fprintf(errOut, "failed to open webwright staging jsonl: %v\n", err)
			return 1
		}
		defer f.Close()
		imported, err := importWebwrightStagingJSONL(context.Background(), deps.StagingStore, f)
		if err != nil {
			fmt.Fprintf(errOut, "failed to import webwright staging jsonl: %v\n", err)
			return 1
		}
		if jsonOut {
			writeJSONCLI(out, map[string]any{"imported": imported}, false)
			return 0
		}
		fmt.Fprintf(out, "imported webwright staging items: %d\n", imported)
		return 0
	default:
		fmt.Fprintf(errOut, "unknown web-gather subcommand: %s\n", subcmd)
		fmt.Fprintln(errOut, "usage: picoclaw web-gather [url|search|search-and-fetch|register-url|run-source|webwright-fetch|import-webwright-jsonl|doctor] ...")
		return 2
	}
}

func parseWebGatherURLArgs(args []string) (modulewebgather.FetchRequest, bool, error) {
	req := modulewebgather.FetchRequest{
		Namespace:       modulewebgather.DefaultNamespace,
		FetchProvider:   modulewebgather.DefaultFetchProvider,
		Extractor:       modulewebgather.DefaultExtractor,
		StoreStaging:    true,
		StoreStagingSet: true,
		LicenseNote:     modulewebgather.DefaultLicenseNote,
		Policy:          modulewebgather.DefaultFetchPolicy(),
	}
	jsonOut := false
	urlSet := false
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch arg {
		case "--json":
			jsonOut = true
		case "--allow-localhost":
			req.Policy.AllowLocalhost = true
		case "--dry-run":
			req.DryRun = true
		case "--refresh":
			req.Refresh = true
		case "--namespace", "--source-id", "--fetch-provider", "--extractor", "--timeout-sec", "--max-body-bytes", "--max-redirects", "--license-note":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return req, jsonOut, fmt.Errorf("%s requires a value", arg)
			}
			value := strings.TrimSpace(args[i+1])
			i++
			switch arg {
			case "--namespace":
				req.Namespace = value
			case "--source-id":
				req.SourceID = value
			case "--fetch-provider":
				value = strings.ToLower(value)
				if !isAllowedWebGatherFetchProvider(value) {
					return req, jsonOut, fmt.Errorf("unsupported fetch provider: %s", value)
				}
				req.FetchProvider = value
			case "--extractor":
				if !isAllowedWebGatherExtractor(value) {
					return req, jsonOut, fmt.Errorf("unsupported extractor: %s", value)
				}
				req.Extractor = value
			case "--timeout-sec":
				sec, err := strconv.Atoi(value)
				if err != nil || sec <= 0 {
					return req, jsonOut, fmt.Errorf("invalid --timeout-sec: %s", value)
				}
				req.Policy.RequestTimeout = time.Duration(sec) * time.Second
			case "--max-body-bytes":
				n, err := strconv.ParseInt(value, 10, 64)
				if err != nil || n <= 0 {
					return req, jsonOut, fmt.Errorf("invalid --max-body-bytes: %s", value)
				}
				req.Policy.MaxBodyBytes = n
			case "--max-redirects":
				n, err := strconv.Atoi(value)
				if err != nil || n < 0 {
					return req, jsonOut, fmt.Errorf("invalid --max-redirects: %s", value)
				}
				req.Policy.MaxRedirects = n
			case "--license-note":
				req.LicenseNote = value
			}
		default:
			if strings.HasPrefix(arg, "--") {
				return req, jsonOut, fmt.Errorf("unknown web-gather url option: %s", arg)
			}
			if urlSet {
				return req, jsonOut, errors.New("web-gather url accepts exactly one URL")
			}
			req.URL = arg
			urlSet = true
		}
	}
	if strings.TrimSpace(req.URL) == "" {
		return req, jsonOut, errors.New("url is required")
	}
	return req, jsonOut, nil
}

func isAllowedWebGatherExtractor(value string) bool {
	switch strings.TrimSpace(value) {
	case "go_readability", "html_basic", "plain_text", "json_text":
		return true
	default:
		return false
	}
}

func isAllowedWebGatherFetchProvider(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "http", "webwright":
		return true
	default:
		return false
	}
}

func parseWebGatherSearchArgs(args []string) (modulewebgather.SearchRequest, string, string, bool, error) {
	req := modulewebgather.SearchRequest{
		Provider:  modulewebgather.DefaultSearchProvider,
		Limit:     modulewebgather.DefaultSearchLimit,
		Language:  modulewebgather.DefaultSearchLanguage,
		Freshness: modulewebgather.DefaultSearchFreshness,
		Namespace: "kb:research",
	}
	searxngURL := ""
	yacyURL := ""
	jsonOut := false
	querySet := false
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch arg {
		case "--json":
			jsonOut = true
		case "--refresh":
			req.Refresh = true
		case "--provider", "--limit", "--language", "--freshness", "--namespace", "--searxng-url", "--yacy-url":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return req, searxngURL, yacyURL, jsonOut, fmt.Errorf("%s requires a value", arg)
			}
			value := strings.TrimSpace(args[i+1])
			i++
			switch arg {
			case "--provider":
				if !isAllowedWebGatherSearchProvider(value) {
					return req, searxngURL, yacyURL, jsonOut, fmt.Errorf("unsupported search provider: %s", value)
				}
				req.Provider = value
			case "--limit":
				n, err := strconv.Atoi(value)
				if err != nil || n <= 0 {
					return req, searxngURL, yacyURL, jsonOut, fmt.Errorf("invalid --limit: %s", value)
				}
				req.Limit = n
			case "--language":
				req.Language = value
			case "--freshness":
				req.Freshness = value
			case "--namespace":
				req.Namespace = value
			case "--searxng-url":
				searxngURL = value
			case "--yacy-url":
				yacyURL = value
			}
		default:
			if strings.HasPrefix(arg, "--") {
				return req, searxngURL, yacyURL, jsonOut, fmt.Errorf("unknown web-gather search option: %s", arg)
			}
			if querySet {
				return req, searxngURL, yacyURL, jsonOut, errors.New("web-gather search accepts exactly one query")
			}
			req.Query = arg
			querySet = true
		}
	}
	if strings.TrimSpace(req.Query) == "" {
		return req, searxngURL, yacyURL, jsonOut, errors.New("query is required")
	}
	return req, searxngURL, yacyURL, jsonOut, nil
}

func parseWebGatherSearchAndFetchArgs(args []string) (modulewebgather.SearchAndFetchRequest, string, string, bool, error) {
	req := modulewebgather.SearchAndFetchRequest{
		Provider:        modulewebgather.DefaultSearchProvider,
		Limit:           modulewebgather.DefaultSearchLimit,
		MaxFetches:      modulewebgather.DefaultMaxFetches,
		Language:        modulewebgather.DefaultSearchLanguage,
		Freshness:       modulewebgather.DefaultSearchFreshness,
		Namespace:       "kb:research",
		FetchProvider:   modulewebgather.DefaultFetchProvider,
		Extractor:       modulewebgather.DefaultExtractor,
		StoreStaging:    true,
		StoreStagingSet: true,
		Policy:          modulewebgather.DefaultFetchPolicy(),
	}
	searxngURL := ""
	yacyURL := ""
	jsonOut := false
	querySet := false
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch arg {
		case "--json":
			jsonOut = true
		case "--refresh":
			req.Refresh = true
		case "--no-store-staging":
			req.StoreStaging = false
			req.StoreStagingSet = true
		case "--provider", "--limit", "--max-fetches", "--language", "--freshness", "--namespace", "--searxng-url", "--yacy-url", "--fetch-provider", "--extractor", "--timeout-sec", "--max-body-bytes", "--max-redirects":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return req, searxngURL, yacyURL, jsonOut, fmt.Errorf("%s requires a value", arg)
			}
			value := strings.TrimSpace(args[i+1])
			i++
			switch arg {
			case "--provider":
				if !isAllowedWebGatherSearchProvider(value) {
					return req, searxngURL, yacyURL, jsonOut, fmt.Errorf("unsupported search provider: %s", value)
				}
				req.Provider = value
			case "--limit":
				n, err := strconv.Atoi(value)
				if err != nil || n <= 0 {
					return req, searxngURL, yacyURL, jsonOut, fmt.Errorf("invalid --limit: %s", value)
				}
				req.Limit = n
			case "--max-fetches":
				n, err := strconv.Atoi(value)
				if err != nil || n <= 0 {
					return req, searxngURL, yacyURL, jsonOut, fmt.Errorf("invalid --max-fetches: %s", value)
				}
				req.MaxFetches = n
			case "--language":
				req.Language = value
			case "--freshness":
				req.Freshness = value
			case "--namespace":
				req.Namespace = value
			case "--searxng-url":
				searxngURL = value
			case "--yacy-url":
				yacyURL = value
			case "--fetch-provider":
				value = strings.ToLower(value)
				if !isAllowedWebGatherFetchProvider(value) {
					return req, searxngURL, yacyURL, jsonOut, fmt.Errorf("unsupported fetch provider: %s", value)
				}
				req.FetchProvider = value
			case "--extractor":
				if !isAllowedWebGatherExtractor(value) {
					return req, searxngURL, yacyURL, jsonOut, fmt.Errorf("unsupported extractor: %s", value)
				}
				req.Extractor = value
			case "--timeout-sec":
				sec, err := strconv.Atoi(value)
				if err != nil || sec <= 0 {
					return req, searxngURL, yacyURL, jsonOut, fmt.Errorf("invalid --timeout-sec: %s", value)
				}
				req.Policy.RequestTimeout = time.Duration(sec) * time.Second
			case "--max-body-bytes":
				n, err := strconv.ParseInt(value, 10, 64)
				if err != nil || n <= 0 {
					return req, searxngURL, yacyURL, jsonOut, fmt.Errorf("invalid --max-body-bytes: %s", value)
				}
				req.Policy.MaxBodyBytes = n
			case "--max-redirects":
				n, err := strconv.Atoi(value)
				if err != nil || n < 0 {
					return req, searxngURL, yacyURL, jsonOut, fmt.Errorf("invalid --max-redirects: %s", value)
				}
				req.Policy.MaxRedirects = n
			}
		default:
			if strings.HasPrefix(arg, "--") {
				return req, searxngURL, yacyURL, jsonOut, fmt.Errorf("unknown web-gather search-and-fetch option: %s", arg)
			}
			if querySet {
				return req, searxngURL, yacyURL, jsonOut, errors.New("web-gather search-and-fetch accepts exactly one query")
			}
			req.Query = arg
			querySet = true
		}
	}
	if strings.TrimSpace(req.Query) == "" {
		return req, searxngURL, yacyURL, jsonOut, errors.New("query is required")
	}
	return req, searxngURL, yacyURL, jsonOut, nil
}

func isAllowedWebGatherSearchProvider(value string) bool {
	switch strings.TrimSpace(value) {
	case "local_cache", "searxng", "rss_atom", "sitemap", "yacy":
		return true
	default:
		return false
	}
}

func parseWebGatherRegisterURLArgs(args []string) (l1sqlite.L1SourceRegistryEntry, bool, error) {
	entry := l1sqlite.L1SourceRegistryEntry{
		Kind:          l1sqlite.L1SourceKindWebGather,
		TrustScore:    0.5,
		FetchInterval: time.Hour,
		LicenseNote:   modulewebgather.DefaultLicenseNote,
		Enabled:       true,
		Meta:          map[string]interface{}{"namespace": modulewebgather.DefaultNamespace},
	}
	jsonOut := false
	allowLocalhost := false
	urlSet := false
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch arg {
		case "--json":
			jsonOut = true
		case "--disabled":
			entry.Enabled = false
		case "--allow-localhost":
			allowLocalhost = true
			entry.Meta["allow_localhost"] = true
		case "--source-id", "--namespace", "--trust-score", "--interval-sec", "--license-note", "--extractor", "--timeout-sec", "--max-body-bytes", "--max-redirects":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return entry, jsonOut, fmt.Errorf("%s requires a value", arg)
			}
			value := strings.TrimSpace(args[i+1])
			i++
			switch arg {
			case "--source-id":
				entry.SourceID = value
			case "--namespace":
				entry.Meta["namespace"] = value
			case "--trust-score":
				n, err := strconv.ParseFloat(value, 64)
				if err != nil {
					return entry, jsonOut, fmt.Errorf("invalid --trust-score: %s", value)
				}
				entry.TrustScore = n
			case "--interval-sec":
				n, err := strconv.Atoi(value)
				if err != nil || n <= 0 {
					return entry, jsonOut, fmt.Errorf("invalid --interval-sec: %s", value)
				}
				entry.FetchInterval = time.Duration(n) * time.Second
			case "--license-note":
				entry.LicenseNote = value
			case "--extractor":
				if !isAllowedWebGatherExtractor(value) {
					return entry, jsonOut, fmt.Errorf("unsupported extractor: %s", value)
				}
				entry.Meta["extractor"] = value
			case "--timeout-sec":
				n, err := strconv.Atoi(value)
				if err != nil || n <= 0 {
					return entry, jsonOut, fmt.Errorf("invalid --timeout-sec: %s", value)
				}
				entry.Meta["request_timeout_ms"] = int64(n) * int64(time.Second/time.Millisecond)
			case "--max-body-bytes":
				n, err := strconv.ParseInt(value, 10, 64)
				if err != nil || n <= 0 {
					return entry, jsonOut, fmt.Errorf("invalid --max-body-bytes: %s", value)
				}
				entry.Meta["max_body_bytes"] = n
			case "--max-redirects":
				n, err := strconv.Atoi(value)
				if err != nil || n < 0 {
					return entry, jsonOut, fmt.Errorf("invalid --max-redirects: %s", value)
				}
				entry.Meta["max_redirects"] = int64(n)
			}
		default:
			if strings.HasPrefix(arg, "--") {
				return entry, jsonOut, fmt.Errorf("unknown web-gather register-url option: %s", arg)
			}
			if urlSet {
				return entry, jsonOut, errors.New("web-gather register-url accepts exactly one URL")
			}
			entry.URL = arg
			urlSet = true
		}
	}
	normalizedURL, err := modulewebgather.NormalizeURL(entry.URL, allowLocalhost)
	if err != nil {
		return entry, jsonOut, err
	}
	entry.URL = normalizedURL
	if strings.TrimSpace(entry.SourceID) == "" {
		entry.SourceID = modulewebgather.SourceIDFromURL(normalizedURL)
	}
	if strings.TrimSpace(entry.LicenseNote) == "" {
		return entry, jsonOut, errors.New("license-note is required")
	}
	return entry, jsonOut, nil
}

func parseWebGatherRunSourceArgs(args []string) (string, bool, error) {
	sourceID := ""
	jsonOut := false
	for _, raw := range args {
		arg := strings.TrimSpace(raw)
		switch arg {
		case "":
			continue
		case "--json":
			jsonOut = true
		default:
			if strings.HasPrefix(arg, "--") {
				return "", jsonOut, fmt.Errorf("unknown web-gather run-source option: %s", arg)
			}
			if sourceID != "" {
				return "", jsonOut, errors.New("web-gather run-source accepts exactly one source_id")
			}
			sourceID = arg
		}
	}
	if sourceID == "" {
		return "", jsonOut, errors.New("source_id is required")
	}
	return sourceID, jsonOut, nil
}

func runWebGatherSource(ctx context.Context, runner webGatherSourceRunner, sourceID string, now time.Time) (sourcefetcher.SweepResult, error) {
	entries, err := runner.ListSourceRegistryEntries(ctx, false)
	if err != nil {
		return sourcefetcher.SweepResult{}, err
	}
	for _, entry := range entries {
		if entry.SourceID != sourceID {
			continue
		}
		if entry.Kind != l1sqlite.L1SourceKindWebGather {
			return sourcefetcher.SweepResult{}, fmt.Errorf("source_id %s is not a web_gather source: %s", sourceID, entry.Kind)
		}
		return sourcefetcher.RunSource(ctx, runner, sourceID, now, sourcefetcher.SweepOptions{
			LimitPerSource:    1,
			MinimumTrustScore: 0.5,
		})
	}
	return sourcefetcher.SweepResult{}, fmt.Errorf("source registry entry not found: %s", sourceID)
}

type webGatherWebwrightFetchRequest struct {
	Task     string
	StartURL string
	TaskID   string
	DryRun   bool
}

func parseWebGatherWebwrightFetchArgs(args []string) (webGatherWebwrightFetchRequest, error) {
	var req webGatherWebwrightFetchRequest
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch arg {
		case "":
			continue
		case "--dry-run":
			req.DryRun = true
		case "--task", "--start-url", "--task-id":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return req, fmt.Errorf("%s requires a value", arg)
			}
			value := strings.TrimSpace(args[i+1])
			i++
			switch arg {
			case "--task":
				req.Task = value
			case "--start-url":
				req.StartURL = value
			case "--task-id":
				req.TaskID = value
			}
		default:
			return req, fmt.Errorf("unknown web-gather webwright-fetch option: %s", arg)
		}
	}
	if strings.TrimSpace(req.Task) == "" {
		return req, errors.New("--task is required")
	}
	return req, nil
}

func buildWebGatherWebwrightCommand(cfg config.WebwrightFetchConfig, req webGatherWebwrightFetchRequest) (string, []string, error) {
	runnerPath := strings.TrimSpace(cfg.RunnerPath)
	if runnerPath == "" {
		runnerPath = defaultRenCrowToolsPath("tools", "webwright_fetch", "run_webwright_fetch.py")
	}
	args := []string{
		runnerPath,
		"--task", req.Task,
	}
	if outputDir := strings.TrimSpace(cfg.OutputDir); outputDir != "" {
		args = append(args, "--output-dir", outputDir)
	}
	if configPath := strings.TrimSpace(cfg.ConfigPath); configPath != "" {
		args = append(args, "-c", configPath)
	}
	if python := strings.TrimSpace(cfg.Python); python != "" {
		args = append(args, "--python", python)
	}
	if uvxFrom := strings.TrimSpace(cfg.UvxFrom); uvxFrom != "" {
		args = append(args, "--uvx-from", uvxFrom)
	}
	if endpoint := strings.TrimSpace(cfg.ResponsesEndpoint); endpoint != "" {
		args = append(args, "--local-responses-endpoint", endpoint)
	}
	if model := strings.TrimSpace(cfg.Model); model != "" {
		args = append(args, "--local-model", model)
	}
	if apiKey := strings.TrimSpace(cfg.APIKey); apiKey != "" {
		args = append(args, "--local-api-key", apiKey)
	}
	if strings.TrimSpace(req.StartURL) != "" {
		args = append(args, "--start-url", strings.TrimSpace(req.StartURL))
	}
	if strings.TrimSpace(req.TaskID) != "" {
		args = append(args, "--task-id", strings.TrimSpace(req.TaskID))
	}
	if req.DryRun {
		args = append(args, "--dry-run")
	}
	return "python3", args, nil
}

func checkWebwrightResponsesEndpoint(ctx context.Context, endpoint string) error {
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

type webGatherDoctorResult struct {
	OK     bool                   `json:"ok"`
	Checks []webGatherDoctorCheck `json:"checks"`
}

type webGatherDoctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

func runWebGatherDoctor(ctx context.Context, deps webGatherCLIDeps) webGatherDoctorResult {
	result := webGatherDoctorResult{OK: true}
	add := func(name string, ok bool, status string, detail string) {
		if !ok {
			result.OK = false
		}
		result.Checks = append(result.Checks, webGatherDoctorCheck{Name: name, Status: status, Detail: detail})
	}
	if deps.StagingStore == nil {
		add("l1_staging_store", false, "fail", "staging store is not configured")
	} else {
		add("l1_staging_store", true, "ok", "configured")
	}
	if strings.TrimSpace(deps.SearXNGBaseURL) == "" {
		add("searxng", true, "skipped", "web_gather.searxng_base_url is not configured")
	} else {
		add("searxng", true, "ok", deps.SearXNGBaseURL)
	}
	if !deps.WebwrightFetch.Enabled {
		add("webwright_enabled", true, "skipped", "webwright_fetch.enabled=false")
		return result
	}
	add("webwright_enabled", true, "ok", "enabled")
	runnerPath := strings.TrimSpace(deps.WebwrightFetch.RunnerPath)
	if runnerPath == "" {
		runnerPath = defaultRenCrowToolsPath("tools", "webwright_fetch", "run_webwright_fetch.py")
	}
	if st, err := os.Stat(runnerPath); err != nil {
		add("webwright_runner", false, "fail", err.Error())
	} else if st.IsDir() {
		add("webwright_runner", false, "fail", runnerPath+" is a directory")
	} else {
		add("webwright_runner", true, "ok", runnerPath)
	}
	python := strings.TrimSpace(deps.WebwrightFetch.Python)
	if python == "" {
		python = "python3"
	}
	if resolved, err := exec.LookPath(python); err != nil {
		add("webwright_python", false, "fail", err.Error())
	} else {
		add("webwright_python", true, "ok", resolved)
	}
	if strings.TrimSpace(deps.WebwrightFetch.UvxFrom) == "" {
		add("webwright_uvx_from", true, "ok", "disabled; external package fetch is opt-in")
	} else {
		add("webwright_uvx_from", true, "ok", deps.WebwrightFetch.UvxFrom)
	}
	if err := checkWebwrightResponsesEndpoint(ctx, deps.WebwrightFetch.ResponsesEndpoint); err != nil {
		add("webwright_responses_endpoint", false, "fail", err.Error())
	} else {
		add("webwright_responses_endpoint", true, "ok", deps.WebwrightFetch.ResponsesEndpoint)
	}
	return result
}

func defaultRenCrowToolsPath(parts ...string) string {
	root := strings.TrimSpace(os.Getenv("RENCROW_TOOLS_ROOT"))
	if root == "" {
		home, err := os.UserHomeDir()
		if err == nil && strings.TrimSpace(home) != "" {
			root = filepath.Join(home, "RenCrow", "RenCrow_Tools")
		}
	}
	if root == "" {
		root = filepath.Join("RenCrow", "RenCrow_Tools")
	}
	return filepath.Join(append([]string{root}, parts...)...)
}

func writeWebGatherDoctorText(out io.Writer, result webGatherDoctorResult) {
	fmt.Fprintf(out, "web-gather doctor: ok=%v\n", result.OK)
	for _, check := range result.Checks {
		if strings.TrimSpace(check.Detail) == "" {
			fmt.Fprintf(out, "- %s: %s\n", check.Name, check.Status)
			continue
		}
		fmt.Fprintf(out, "- %s: %s (%s)\n", check.Name, check.Status, check.Detail)
	}
}

func execWebGatherCommand(ctx context.Context, command string, args []string, out io.Writer, errOut io.Writer) int {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Stdout = out
	cmd.Stderr = errOut
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(errOut, "failed to run %s: %v\n", command, err)
		return 127
	}
	return 0
}

func parseWebGatherImportWebwrightJSONLArgs(args []string) (string, bool, error) {
	path := ""
	jsonOut := false
	for _, raw := range args {
		arg := strings.TrimSpace(raw)
		switch arg {
		case "":
			continue
		case "--json":
			jsonOut = true
		default:
			if strings.HasPrefix(arg, "--") {
				return "", jsonOut, fmt.Errorf("unknown web-gather import-webwright-jsonl option: %s", arg)
			}
			if path != "" {
				return "", jsonOut, errors.New("web-gather import-webwright-jsonl accepts exactly one path")
			}
			path = arg
		}
	}
	if path == "" {
		return "", jsonOut, errors.New("path is required")
	}
	return path, jsonOut, nil
}

func importWebwrightStagingJSONL(ctx context.Context, store webGatherStagingStore, reader io.Reader) (int, error) {
	if store == nil {
		return 0, errors.New("web gather staging store is required")
	}
	if reader == nil {
		return 0, errors.New("webwright staging JSONL reader is required")
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	imported := 0
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var item l1sqlite.L1StagingItem
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			return imported, fmt.Errorf("invalid webwright staging JSONL at line %d: %w", lineNo, err)
		}
		if err := validateWebwrightStagingItem(item); err != nil {
			return imported, fmt.Errorf("invalid webwright staging item at line %d: %w", lineNo, err)
		}
		item.ValidationStatus = l1sqlite.L1StagingStatusPending
		if _, err := store.SaveStagingItem(ctx, item); err != nil {
			return imported, fmt.Errorf("failed to save webwright staging item at line %d: %w", lineNo, err)
		}
		imported++
	}
	if err := scanner.Err(); err != nil {
		return imported, fmt.Errorf("failed to scan webwright staging JSONL: %w", err)
	}
	return imported, nil
}

func validateWebwrightStagingItem(item l1sqlite.L1StagingItem) error {
	if strings.TrimSpace(item.Kind) != l1sqlite.L1StagingKindExternalFetch {
		return fmt.Errorf("kind must be %s", l1sqlite.L1StagingKindExternalFetch)
	}
	status := strings.TrimSpace(item.ValidationStatus)
	if status != "" && status != l1sqlite.L1StagingStatusPending {
		return fmt.Errorf("validation_status must be pending")
	}
	if webGatherCredentialLikeText(item.RawText) {
		return errors.New("raw_text appears to contain credential material")
	}
	if item.Meta == nil {
		return errors.New("meta is required")
	}
	if !boolMeta(item.Meta, "webwright") && stringMetaForWebGatherCLI(item.Meta, "tool") != "webwright_fetch" {
		return errors.New("meta must identify webwright_fetch")
	}
	if !boolMeta(item.Meta, "review_required") {
		return errors.New("meta.review_required must be true")
	}
	if boolMeta(item.Meta, "auto_promote") {
		return errors.New("meta.auto_promote must be false")
	}
	return nil
}

func boolMeta(meta map[string]interface{}, key string) bool {
	value, ok := meta[key]
	if !ok {
		return false
	}
	if b, ok := value.(bool); ok {
		return b
	}
	if s, ok := value.(string); ok {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "true", "1", "yes", "on":
			return true
		default:
			return false
		}
	}
	return false
}

func stringMetaForWebGatherCLI(meta map[string]interface{}, key string) string {
	value, ok := meta[key]
	if !ok {
		return ""
	}
	if s, ok := value.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

var webGatherCredentialLikeTextRE = regexp.MustCompile(`(?i)(authorization|set-cookie|cookie|api[_-]?key|access[_-]?token|refresh[_-]?token|password|secret)\s*[:=]|\bbearer\s+[A-Za-z0-9._~+/=-]{8,}`)

func webGatherCredentialLikeText(text string) bool {
	return webGatherCredentialLikeTextRE.MatchString(text)
}

func loadWebGatherStore(configPath string) (*config.Config, *l1sqlite.L1SQLiteStore, error) {
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return nil, nil, err
	}
	p := strings.TrimSpace(cfg.Conversation.L1SQLitePath)
	if p == "" {
		return nil, nil, errors.New("conversation.l1_sqlite_path is required for web-gather CLI")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, nil, err
	}
	store, err := l1sqlite.NewL1SQLiteStore(p)
	if err != nil {
		return nil, nil, err
	}
	return cfg, store, nil
}
