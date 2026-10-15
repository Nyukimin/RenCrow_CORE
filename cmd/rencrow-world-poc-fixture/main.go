package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
)

const defaultListenAddress = "127.0.0.1:18794"

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "[NG] world PoC fixture unavailable")
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("rencrow-world-poc-fixture", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	address := flags.String("listen", defaultListenAddress, "loopback listen address")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("invalid fixture arguments")
	}
	listener, err := listenLoopback(*address)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "world PoC fixture listening at http://%s\n", listener.Addr().String()); err != nil {
		_ = listener.Close()
		return errors.New("fixture status output unavailable")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serve(ctx, listener, newHandler())
}

func validateListenAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("listen address must be a loopback IP and port")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("listen address must use a loopback IP")
	}
	return nil
}

func listenLoopback(address string) (net.Listener, error) {
	if err := validateListenAddress(address); err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, errors.New("loopback listener unavailable")
	}
	return listener, nil
}

func newHandler() http.Handler {
	reader := viewer.NewMockWorldPocReader()
	bridge := viewer.NewWorldPocBridge(reader, viewer.WorldPocViewScope{ActorIDs: []string{"mio"}})
	mux := http.NewServeMux()
	mux.Handle("/viewer/world/snapshot", bridge.SnapshotHandler())
	mux.Handle("/viewer/world/stream", bridge.StreamHandler())
	return mux
}

func serve(ctx context.Context, listener net.Listener, handler http.Handler) error {
	if ctx == nil || listener == nil || handler == nil {
		return errors.New("fixture server unavailable")
	}
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.Serve(listener)
	}()

	select {
	case err := <-serveResult:
		if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			_ = server.Close()
			<-serveResult
			return errors.New("fixture shutdown failed")
		}
		err := <-serveResult
		if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	}
}
