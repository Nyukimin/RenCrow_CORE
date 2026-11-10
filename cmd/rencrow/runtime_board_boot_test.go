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
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
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

func TestRegisterBoardCommunicationOwnerRoutesWiresAgentRequestCallbacks(t *testing.T) {
	acceptedCalls, statusCalls := 0, 0
	turnID := modulecore.NewTurnID()
	status := boardOwnerHTTPTestStatusReceipt(boardOwnerHTTPStageReplyConfirmed)
	status.TurnID = turnID
	status.ActorID = "shiro"
	status.BoardMessageID = "m_f86c29ae0967cf1559caf5dd"
	status.ThreadID = "t_0123456789abcdef01234567"
	status.ReplyMessageID = "m_0182bd3c1f33861420c2ce5e"
	handler, err := newBoardOwnerHTTPHandler("owner-7", []byte(boardOwnerHTTPTestToken), boardOwnerHTTPCallbacks{
		AgentAccept: func(_ context.Context, ownerID string, request boardOwnerHTTPRequest) (boardOwnerHTTPAcceptedReceipt, error) {
			acceptedCalls++
			if ownerID != "owner-7" || request.ActorID != "shiro" {
				t.Fatalf("outer mux agent POST callback owner/request = %q/%+v", ownerID, request)
			}
			return boardOwnerHTTPTestAcceptedReceipt(), nil
		},
		AgentStatus: func(_ context.Context, ownerID string, requestedTurn modulecore.TurnID) (boardOwnerHTTPStatusReceipt, bool, error) {
			statusCalls++
			if ownerID != "owner-7" || requestedTurn != turnID {
				t.Fatalf("outer mux agent GET callback owner/turn = %q/%q", ownerID, requestedTurn)
			}
			return status, true, nil
		},
	})
	if err != nil {
		t.Fatalf("newBoardOwnerHTTPHandler: %v", err)
	}
	mux := http.NewServeMux()
	registerBoardCommunicationOwnerRoutes(mux, &Dependencies{boardOwnerHTTP: handler})

	post := boardOwnerHTTPDo(mux, boardOwnerHTTPTestRequest(http.MethodPost, boardOwnerAgentRequestsPath, boardOwnerHTTPAgentBody("shiro", "kuro")))
	if post.Code != http.StatusAccepted || acceptedCalls != 1 {
		t.Fatalf("outer mux agent POST status/calls = %d/%d body=%s; want 202/1", post.Code, acceptedCalls, post.Body.String())
	}
	get := boardOwnerHTTPDo(mux, boardOwnerHTTPTestRequest(http.MethodGet, boardOwnerAgentRequestsPath+"/"+string(turnID), ""))
	if get.Code != http.StatusOK || statusCalls != 1 {
		t.Fatalf("outer mux agent GET status/calls = %d/%d body=%s; want 200/1", get.Code, statusCalls, get.Body.String())
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
