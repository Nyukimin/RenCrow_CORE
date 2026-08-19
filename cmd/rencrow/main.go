package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
)

// Version 情報（go build -ldflags で注入）
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

func main() {
	cmd := "run"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	switch cmd {
	case "run":
		cmdRun()
	case "version", "-v", "--version":
		cmdVersion()
	case "health":
		cmdHealth()
	case "status":
		cmdStatus()
	case "doctor":
		cmdDoctor()
	case "config":
		cmdConfig()
	case "resilience":
		cmdResilience()
	case "channels":
		cmdChannels()
	case "gateway":
		cmdGateway()
	case "logs":
		cmdLogs()
	case "evidence":
		cmdEvidence()
	case "jobs":
		cmdJobs()
	case "source-registry":
		cmdSourceRegistry()
	case "web-gather":
		cmdWebGather()
	case "browser-actor":
		cmdBrowserActor()
	case "knowledge":
		cmdKnowledge()
	case "person-related":
		cmdPersonRelated()
	case "knowledge-memory":
		cmdKnowledgeMemory()
	case "help", "-h", "--help":
		cmdHelp()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		cmdHelp()
		os.Exit(1)
	}
}

// cmdRun はHTTPサーバーを起動する（デフォルトコマンド）
func cmdRun() {
	configPath := getConfigPath()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	log.Printf("RenCrow %s (commit: %s, built: %s)", Version, Commit, BuildDate)
	log.Printf("Loaded config from: %s", configPath)

	llmGatewayStatus := ensureLLMGateway(cfg)
	dependencies := buildDependencies(cfg)
	dependencies.llmGatewayProcess = llmGatewayStatus.Process

	// Graceful shutdown用シグナル
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		sig := <-sigCh
		log.Printf("Received signal: %v, shutting down...", sig)
		dependencies.Shutdown()
		os.Exit(0)
	}()

	// HTTPサーバー起動
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	log.Printf("Starting RenCrow server on %s", addr)

	mux := http.NewServeMux()
	registerLocalPprofRoutes(mux)

	// Live Viewer
	sttRuntime := buildSTTRuntime(cfg)
	voiceChatRuntime := buildVoiceChatRuntime(cfg, dependencies.voiceDirectHandler, dependencies.idleChatOrch)
	debugSystemOpts := sttRuntime.DebugOptions
	debugSystemOpts.RuntimeReadiness = buildRuntimeDependencyReadiness(cfg, dependencies)
	debugSystemOpts.LLMGateway = viewer.LLMGatewayRuntimeConfig{
		BaseURL:            llmGatewayStatus.BaseURL,
		Ready:              llmGatewayStatus.Ready,
		AutoStartAttempted: llmGatewayStatus.AutoStartAttempted,
		AutoStarted:        llmGatewayStatus.AutoStarted,
		Warning:            llmGatewayStatus.Warning,
	}
	debugSystemOpts.SecretRefs = buildSecretRefsFromConfig(cfg)
	debugSystemOpts.WebwrightFetch = viewer.WebwrightFetchRuntimeConfig{
		Enabled:           cfg.WebwrightFetch.Enabled,
		RunnerPath:        cfg.WebwrightFetch.RunnerPath,
		ConfigPath:        cfg.WebwrightFetch.ConfigPath,
		OutputDir:         cfg.WebwrightFetch.OutputDir,
		StagingOutputDir:  cfg.WebwrightFetch.StagingOutputDir,
		UvxFrom:           cfg.WebwrightFetch.UvxFrom,
		Python:            cfg.WebwrightFetch.Python,
		ResponsesEndpoint: cfg.WebwrightFetch.ResponsesEndpoint,
		Model:             cfg.WebwrightFetch.Model,
		APIKeyConfigured:  strings.TrimSpace(cfg.WebwrightFetch.APIKey) != "",
	}
	debugSystemOpts.WebGather = viewer.WebGatherRuntimeConfig{
		SearXNGBaseURL: strings.TrimSpace(cfg.WebGather.SearXNGBaseURL),
		YaCyBaseURL:    strings.TrimSpace(cfg.WebGather.YaCyBaseURL),
		FetchCache:     true,
		FailureCache:   true,
		RateState:      true,
	}
	debugSystemOpts.BrowserActor = viewer.BrowserActorRuntimeConfig{
		Enabled:            cfg.BrowserActor.Enabled,
		RunnerPath:         cfg.BrowserActor.RunnerPath,
		NodeBinary:         cfg.BrowserActor.NodeBinary,
		Browser:            cfg.BrowserActor.Browser,
		HeadlessDefault:    cfg.BrowserActor.HeadlessDefaultEnabled(),
		ProfileRoot:        cfg.BrowserActor.ProfileRoot,
		ArtifactRoot:       cfg.BrowserActor.ArtifactRoot,
		TimeoutMS:          cfg.BrowserActor.TimeoutMS,
		MaxActions:         cfg.BrowserActor.MaxActions,
		NetworkScope:       cfg.BrowserActor.NetworkScope,
		AllowedOriginCount: len(cfg.BrowserActor.AllowedOrigins),
		SaveTrace:          cfg.BrowserActor.SaveTraceEnabled(),
		SaveScreenshot:     cfg.BrowserActor.SaveScreenshotEnabled(),
		MaskSecrets:        cfg.BrowserActor.MaskSecretsEnabled(),
	}
	voiceChatOpts := voiceChatDebugOptions(cfg, voiceChatRuntime)
	debugSystemOpts.VoiceChatEnabled = voiceChatOpts.VoiceChatEnabled
	debugSystemOpts.VoiceChatGatewayConfigured = voiceChatOpts.VoiceChatGatewayConfigured
	debugSystemOpts.VoiceInputMode = voiceChatOpts.VoiceInputMode
	registerFeatureRoutes(mux, cfg, dependencies, sttRuntime, voiceChatRuntime, debugSystemOpts)

	server := &http.Server{
		Addr:    addr,
		Handler: withTailscaleViewerOnlyGuard(withInteractionProfileGuard(mux)),
	}
	if envBool("RENCROW_DEBUG_CONNSTATE") {
		server.ConnState = func(conn net.Conn, state http.ConnState) {
			log.Printf("[ConnState] %s -> %s (remote: %s)", state.String(), conn.LocalAddr(), conn.RemoteAddr())
		}
	}
	if cfg.Server.TLS.Enabled {
		err = server.ListenAndServeTLS(cfg.Server.TLS.CertFile, cfg.Server.TLS.KeyFile)
	} else {
		err = server.ListenAndServe()
	}
	if err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

func registerLocalPprofRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	mux.HandleFunc("/debug/pprof/", localOnlyHandler(pprof.Index))
	mux.HandleFunc("/debug/pprof/cmdline", localOnlyHandler(pprof.Cmdline))
	mux.HandleFunc("/debug/pprof/profile", localOnlyHandler(pprof.Profile))
	mux.HandleFunc("/debug/pprof/symbol", localOnlyHandler(pprof.Symbol))
	mux.HandleFunc("/debug/pprof/trace", localOnlyHandler(pprof.Trace))
}

func localOnlyHandler(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackRemoteAddr(r.RemoteAddr) {
			http.NotFound(w, r)
			return
		}
		next(w, r)
	}
}

func isLoopbackRemoteAddr(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err != nil {
		host = strings.TrimSpace(remoteAddr)
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// getConfigPath は設定ファイルパスを取得
func getConfigPath() string {
	if path := os.Getenv("RENCROW_CONFIG"); path != "" {
		return path
	}
	return "./config.yaml"
}

func defaultAssetsGitRepoPath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil || homeDir == "" {
		return filepath.Join(".rencrow", "assets-repo")
	}
	return filepath.Join(homeDir, ".rencrow", "assets-repo")
}
