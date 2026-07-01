package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	domaintransport "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/transport"
)

const shutdownTimeout = 30 * time.Second

// AgentHandler はスタンドアロンAgentの処理インターフェース
type AgentHandler interface {
	HandleMessage(ctx context.Context, msg domaintransport.Message) (domaintransport.Message, error)
}

func main() {
	standalone := flag.Bool("standalone", false, "Run in standalone mode")
	agentType := flag.String("agent", "", "Agent type: worker, coder1, coder2, coder3, audio_router")
	configPath := flag.String("config", "./config.yaml", "Path to config file")
	flag.Parse()

	// JSON 通信チャネルを汚染から保護（ライブラリ init より前に実行）
	jsonOut := protectStdout()

	if !*standalone {
		fmt.Fprintln(os.Stderr, "picoclaw-agent must be run with --standalone flag")
		os.Exit(1)
	}

	if *agentType == "" {
		fmt.Fprintln(os.Stderr, "picoclaw-agent requires --agent flag (worker, coder1, coder2, coder3, audio_router)")
		os.Exit(1)
	}

	// .envファイルを読み込み（~/.picoclaw/.env または configと同ディレクトリの.env）
	homeDir, _ := os.UserHomeDir()

	// stdoutはJSON通信に使うので、ログはstderrとファイルに出力
	logFile, err := os.OpenFile(filepath.Join(homeDir, ".picoclaw", "agent.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err == nil {
		log.SetOutput(io.MultiWriter(os.Stderr, logFile))
		defer logFile.Close()
	} else {
		log.SetOutput(os.Stderr)
	}
	loadDotEnv(filepath.Join(homeDir, ".picoclaw", ".env"))
	loadDotEnv(filepath.Join(filepath.Dir(*configPath), ".env"))

	// 設定読み込み
	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	if *agentType == "audio_router" {
		log.Printf("[picoclaw-agent] Starting standalone %s agent", *agentType)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		go func() {
			sig := <-sigCh
			log.Printf("[picoclaw-agent] Received signal: %v, shutting down...", sig)
			cancel()
		}()

		if err := runAudioRouter(ctx, cfg, *configPath, flag.Args()); err != nil && err != context.Canceled {
			log.Fatalf("AudioRouter failed: %v", err)
		}
		log.Println("[picoclaw-agent] Shutdown complete")
		return
	}

	handler, err := initHandler(*agentType, cfg)
	if err != nil {
		log.Fatalf("Failed to initialize handler: %v", err)
	}

	log.Printf("[picoclaw-agent] Starting standalone %s agent", *agentType)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// SIGTERM/SIGINT graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		sig := <-sigCh
		log.Printf("[picoclaw-agent] Received signal: %v, shutting down...", sig)
		cancel()
	}()

	if err := runMessageLoop(ctx, handler, jsonOut); err != nil {
		log.Printf("[picoclaw-agent] Message loop ended: %v", err)
	}

	log.Println("[picoclaw-agent] Shutdown complete")
}
