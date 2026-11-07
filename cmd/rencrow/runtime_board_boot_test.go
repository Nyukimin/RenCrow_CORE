package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
)

type boardBootCloseObservedStore struct {
	runtimeCanonicalEventStore
	done   <-chan struct{}
	closed chan bool
}

func (s *boardBootCloseObservedStore) Close() error {
	select {
	case <-s.done:
		s.closed <- true
		return nil
	default:
		s.closed <- false
		return errors.New("EventStore closed before BOARD lifecycle joined")
	}
}

func TestBoardCommunicationBootKeepsDefaultOffAndEnabledUnavailableOwnerRequestProtected(t *testing.T) {
	disabled := &Dependencies{}
	buildBoardCommunicationRuntime(&config.Config{}, disabled, nil, nil, nil, nil)
	if disabled.boardCommunication != nil || disabled.boardOwnerHTTP != nil {
		t.Fatal("default-disabled BOARD boot created a runtime or owner route")
	}
	disabledMux := http.NewServeMux()
	registerBoardCommunicationOwnerRoutes(disabledMux, disabled)
	disabledResponse := httptest.NewRecorder()
	disabledMux.ServeHTTP(disabledResponse, httptest.NewRequest(http.MethodGet, boardOwnerRequestsPath, nil))
	if disabledResponse.Code != http.StatusNotFound {
		t.Fatalf("default-disabled BOARD route status=%d, want 404", disabledResponse.Code)
	}

	token := strings.Repeat("s", agentOpsMinTokenBytes)
	tokenPath := filepath.Join(t.TempDir(), "agent-ops.token")
	if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
		t.Fatalf("write synthetic protected owner token: %v", err)
	}
	enabled := &config.Config{
		BoardCommunication: config.BoardCommunicationConfig{Enabled: true},
		LocalAgentOps: config.LocalAgentOpsConfig{
			Enabled: true, UserID: "owner-alpha", AuthTokenFile: tokenPath,
		},
	}
	deps := &Dependencies{}
	buildBoardCommunicationRuntime(enabled, deps, nil, nil, nil, nil)
	if deps.boardCommunication == nil || deps.boardCommunication.Status().Ready ||
		deps.boardCommunication.Status().State != boardCommunicationServiceStateUnavailable || deps.boardOwnerHTTP == nil {
		t.Fatalf("enabled unavailable BOARD lane did not fail closed without blocking CORE: service=%#v handler=%T", deps.boardCommunication, deps.boardOwnerHTTP)
	}
	mux := http.NewServeMux()
	registerBoardCommunicationOwnerRoutes(mux, deps)
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+boardOwnerRequestsPath,
		strings.NewReader(`{"channel_id":"channel-alpha","peer_id":"peer-alpha","title":"Synthetic title","instruction":"Check the published result.","idempotency_key":"request-1"}`))
	request.RemoteAddr = "127.0.0.1:4567"
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), token) {
		t.Fatalf("protected BOARD owner POST status=%d body=%q; want redacted 503", response.Code, response.Body.String())
	}
}

func TestBoardCommunicationShutdownJoinsBeforeCanonicalStoreClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	releaseLane := make(chan struct{})
	done := make(chan struct{})
	cancelled := make(chan struct{})
	go func() {
		<-ctx.Done()
		close(cancelled)
		<-releaseLane
		close(done)
	}()
	storeClosed := make(chan bool, 1)
	store := &boardBootCloseObservedStore{done: done, closed: storeClosed}
	deps := &Dependencies{
		boardCommunicationCancel: cancel,
		boardCommunicationDone:   done,
		canonicalEventStore:      store,
	}
	shutdownDone := make(chan struct{})
	go func() {
		deps.Shutdown()
		close(shutdownDone)
	}()
	<-cancelled
	select {
	case closedBeforeJoin := <-storeClosed:
		if !closedBeforeJoin {
			t.Fatal("canonical EventStore closed before BOARD lifecycle join")
		}
		t.Fatal("canonical EventStore closed while BOARD lifecycle was still running")
	case <-shutdownDone:
		t.Fatal("shutdown returned before BOARD lifecycle joined and stores closed")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseLane)
	select {
	case <-shutdownDone:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not finish after BOARD lifecycle joined")
	}
	select {
	case closedAfterJoin := <-storeClosed:
		if !closedAfterJoin {
			t.Fatal("canonical EventStore closed before BOARD lifecycle join")
		}
	default:
		t.Fatal("canonical EventStore was not closed after BOARD lifecycle join")
	}
}
