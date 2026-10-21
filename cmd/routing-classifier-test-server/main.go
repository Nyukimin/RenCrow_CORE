package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/routing/classifierlab"
)

const defaultListenAddress = "127.0.0.1:18190"

// sdk_worker.py remains an isolated optional process payload; CORE startup does not call it.
//
//go:embed sdk_worker.py
var sdkWorkerScript string

func main() {
	configPath := flag.String("config", "", "resolved routing classifier YAML (required)")
	listenAddress := flag.String("listen", defaultListenAddress, "local server bind address")
	flag.Parse()
	if err := run(*configPath, *listenAddress); err != nil {
		log.Printf("routing classifier test server stopped: %v", err)
		os.Exit(1)
	}
}

func run(configPath, listenAddress string) error {
	config, err := loadResolvedConfig(configPath)
	if err != nil {
		return err
	}
	address, err := validateListenAddress(listenAddress, config.Value.Candidate.Endpoint)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen on local candidate endpoint: %w", err)
	}

	rootContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	worker, artifacts, readinessError := startPreparedWorker(rootContext, config, sdkWorkerScript, childHardTimeoutMS*time.Millisecond)
	if readinessError != "" {
		log.Printf("routing classifier test server readiness blocked: %s", readinessError)
	}
	var artifactPointer *classifierlab.PreparedArtifacts
	if artifacts != nil {
		artifactPointer = artifacts
	}
	api := newClassificationServer(config, worker, readinessError, artifactPointer)
	server := &http.Server{
		Handler:           api.Handler(),
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}

	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(listener) }()
	select {
	case <-rootContext.Done():
	case serveErr := <-serveResult:
		if !errors.Is(serveErr, http.ErrServerClosed) {
			stopWorker(worker)
			return serveErr
		}
	}

	shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shutdownErr := server.Shutdown(shutdownContext)
	if shutdownErr != nil {
		_ = server.Close()
	}
	stopWorker(worker)
	if shutdownErr != nil {
		return fmt.Errorf("shutdown HTTP server: %w", shutdownErr)
	}
	return nil
}

func startPreparedWorker(ctx context.Context, config loadedConfig, script string, hardTimeout time.Duration) (*sdkWorker, *classifierlab.PreparedArtifacts, string) {
	artifacts, err := classifierlab.LoadPreparedArtifacts(
		config.Directory,
		config.Value.ReferenceWorker.ModelDirectory,
		config.Value.Candidate.ExpectedFamily,
		config.Value.Candidate.ExpectedModelRevision,
		config.Value.ReferenceWorker.Device,
		config.Value.ReferenceWorker.DType,
		config.Value.Candidate.ExpectedPolicySHA256,
	)
	if err != nil {
		if errors.Is(err, classifierlab.ErrPreparedPolicyUnavailable) {
			return nil, nil, "POLICY_UNDEFINED"
		}
		return nil, nil, "ARTIFACT_INVALID"
	}
	if config.Token == "" {
		return nil, &artifacts, "AUTH_NOT_CONFIGURED"
	}
	worker := newSDKWorker(config, artifacts, script, hardTimeout)
	if err := worker.Start(ctx); err != nil {
		worker.Close()
		return nil, &artifacts, "MODEL_NOT_READY"
	}
	return worker, &artifacts, ""
}

func stopWorker(worker *sdkWorker) {
	if worker != nil {
		worker.Close()
	}
}

func validateListenAddress(listenAddress, endpointValue string) (string, error) {
	listenHost, listenPort, err := net.SplitHostPort(listenAddress)
	if err != nil || listenHost != "127.0.0.1" {
		return "", errors.New("--listen must bind explicitly to 127.0.0.1")
	}
	port, err := strconv.Atoi(listenPort)
	if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != listenPort {
		return "", errors.New("--listen must contain a canonical TCP port")
	}
	endpoint, err := candidateEndpointAddress(endpointValue)
	if err != nil || endpoint != listenAddress {
		return "", errors.New("--listen must match candidate.endpoint")
	}
	return listenAddress, nil
}

func candidateEndpointAddress(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Hostname() != "127.0.0.1" || parsed.Scheme != "http" || parsed.Port() == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return "", errors.New("candidate endpoint is not a local HTTP endpoint")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != parsed.Port() {
		return "", errors.New("candidate endpoint port is invalid")
	}
	return net.JoinHostPort(parsed.Hostname(), parsed.Port()), nil
}
