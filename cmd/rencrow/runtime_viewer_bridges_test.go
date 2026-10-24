package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainsession "github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/speech"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	sessionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/session"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

type viewerBridgeErrorProcessor struct{}

func (viewerBridgeErrorProcessor) ProcessMessage(ctx context.Context, _ orchestrator.ProcessMessageRequest) (orchestrator.ProcessMessageResponse, error) {
	turntiming.Mark(ctx, turntiming.PointRouteDecided)
	turntiming.Mark(ctx, turntiming.PointDispatchStarted)
	return orchestrator.ProcessMessageResponse{}, errors.New("viewer processing failed")
}

type viewerBridgeErrorAfterSessionRebindProcessor struct {
	repository  *sessionpersistence.JSONSessionRepository
	replacement *domainsession.Session
}

func (p viewerBridgeErrorAfterSessionRebindProcessor) ProcessMessage(ctx context.Context, _ orchestrator.ProcessMessageRequest) (orchestrator.ProcessMessageResponse, error) {
	turntiming.Mark(ctx, turntiming.PointRouteDecided)
	turntiming.Mark(ctx, turntiming.PointDispatchStarted)
	if err := p.repository.Save(ctx, p.replacement); err != nil {
		return orchestrator.ProcessMessageResponse{}, err
	}
	return orchestrator.ProcessMessageResponse{}, errors.New("viewer processing failed after canonical Session rebind")
}

type viewerBridgeSSERecorder struct {
	header     http.Header
	mu         sync.Mutex
	statusCode int
	body       strings.Builder
	writes     chan struct{}
}

func newViewerBridgeSSERecorder() *viewerBridgeSSERecorder {
	return &viewerBridgeSSERecorder{header: make(http.Header), writes: make(chan struct{}, 1)}
}

func (r *viewerBridgeSSERecorder) Header() http.Header { return r.header }

func (r *viewerBridgeSSERecorder) WriteHeader(statusCode int) {
	r.mu.Lock()
	if r.statusCode == 0 {
		r.statusCode = statusCode
	}
	r.mu.Unlock()
}

func (r *viewerBridgeSSERecorder) Write(data []byte) (int, error) {
	r.mu.Lock()
	if r.statusCode == 0 {
		r.statusCode = http.StatusOK
	}
	n, err := r.body.Write(data)
	r.mu.Unlock()
	select {
	case r.writes <- struct{}{}:
	default:
	}
	return n, err
}

func (r *viewerBridgeSSERecorder) Flush() {
	r.mu.Lock()
	if r.statusCode == 0 {
		r.statusCode = http.StatusOK
	}
	r.mu.Unlock()
}

func (r *viewerBridgeSSERecorder) bodySnapshot() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.String()
}

func (r *viewerBridgeSSERecorder) statusSnapshot() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.statusCode == 0 {
		return http.StatusOK
	}
	return r.statusCode
}

func (r *viewerBridgeSSERecorder) waitForBodyContains(want string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if strings.Contains(r.bodySnapshot(), want) {
			return true
		}
		if remaining := time.Until(deadline); remaining <= 0 {
			return false
		} else {
			select {
			case <-r.writes:
			case <-time.After(remaining):
				return false
			}
		}
	}
}

func newViewerBridgeLegacyAdmissionDependencies(t *testing.T) *Dependencies {
	t.Helper()
	queue, err := newRuntimePersistenceQueue()
	if err != nil {
		t.Fatalf("newRuntimePersistenceQueue: %v", err)
	}
	t.Cleanup(func() { shutdownRuntimeQueue(t, queue) })
	taskStore, err := taskpersistence.NewJSONLStoreWithOptions(t.TempDir(), taskpersistence.OpenOptions{Index: true})
	if err != nil {
		t.Fatalf("open indexed Task store: %v", err)
	}
	t.Cleanup(func() {
		if err := taskStore.Close(); err != nil {
			t.Errorf("close indexed Task store: %v", err)
		}
	})
	writeQueue, err := eventstore.NewPersistenceQueueWriteQueue(queue)
	if err != nil {
		t.Fatalf("NewPersistenceQueueWriteQueue: %v", err)
	}
	eventStore, err := eventstore.NewSQLiteStoreWithWriteQueue(filepath.Join(t.TempDir(), "event-store.sqlite"), writeQueue)
	if err != nil {
		t.Fatalf("NewSQLiteStoreWithWriteQueue: %v", err)
	}
	t.Cleanup(func() {
		if err := eventStore.Close(); err != nil {
			t.Errorf("close EventStore: %v", err)
		}
	})
	archive, err := viewer.NewCanonicalEventLog(eventStore)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog: %v", err)
	}
	return &Dependencies{
		canonicalEventStore:           eventStore,
		persistenceQueue:              queue,
		taskStore:                     taskStore,
		combinedChatProcessorEligible: true,
		combinedChatAdmissionEnabled:  true,
		eventRelay:                    &idleAwareEventListener{archive: archive, hub: viewer.NewEventHub(8)},
	}
}

func TestViewerBridgeRequiresConfiguredOwnerBearerForCMDMioBeforeProcessing(t *testing.T) {
	const token = "viewer-send-owner-token-0123456789"
	tokenPath := filepath.Join(t.TempDir(), "agent-ops.token")
	if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{LocalAgentOps: config.LocalAgentOpsConfig{
		Enabled: true, AuthTokenFile: tokenPath, UserID: "ren",
	}}
	processor := viewerBridgeCaptureProcessor{requests: make(chan orchestrator.ProcessMessageRequest, 1)}
	deps := &Dependencies{canonicalEventStore: openViewerBridgeAdmissionStore(t), eventHub: viewer.NewEventHub(8)}
	handler := buildViewerBridgeHandlers(cfg, deps, "", ttsEntryRuntime{}, sessionpersistence.NewJSONSessionRepository(t.TempDir())).ViewerSendFromOrch(processor)

	for _, bearer := range []string{"", "Bearer foreign-owner-token"} {
		request := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{"message":"private recall","to":"mio"}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
		request.Header.Set("X-RenCrow-Interaction-Profile", "cmd-chat")
		if bearer != "" {
			request.Header.Set("Authorization", bearer)
		}
		request.RemoteAddr = "127.0.0.1:12345"
		request.Host = "127.0.0.1:18790"
		attachViewerBridgeTestPresence(t, deps, request)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)

		if response.Code != http.StatusUnauthorized {
			t.Fatalf("bearer=%q status=%d body=%s, want missing/foreign CMD Mio owner Bearer rejected", bearer, response.Code, response.Body.String())
		}
		select {
		case got := <-processor.requests:
			t.Fatalf("unauthenticated or foreign request reached ProcessMessage: %+v", got)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func TestViewerBridgeCarriesAuthenticatedCMDMioOwnerScopeIntoProcessMessage(t *testing.T) {
	const token = "viewer-send-owner-token-0123456789"
	tokenPath := filepath.Join(t.TempDir(), "agent-ops.token")
	if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{LocalAgentOps: config.LocalAgentOpsConfig{
		Enabled: true, AuthTokenFile: tokenPath, UserID: "ren",
	}}
	processor := viewerBridgeScopedProcessor{received: make(chan viewerBridgeScopedRequest, 2)}
	deps := newViewerBridgeLegacyAdmissionDependencies(t)
	deps.eventHub = viewer.NewEventHub(8)
	repo := sessionpersistence.NewJSONSessionRepository(t.TempDir())
	if err := repo.WarmCanonicalIndex(context.Background()); err != nil {
		t.Fatalf("warm canonical Session projection: %v", err)
	}
	legacyAddress, err := domainconversation.NewChannelAddress("viewer", "viewer-user")
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Now().UTC()
	legacySession, _, err := repo.LoadOrCreateCanonical(context.Background(), createdAt.Format("2006-01-02"), legacyAddress, createdAt)
	if err != nil || legacySession == nil {
		t.Fatalf("seed public canonical Session=%v err=%v", legacySession, err)
	}
	handler := buildViewerBridgeHandlers(cfg, deps, "", ttsEntryRuntime{}, repo).ViewerSendFromOrch(processor)

	var firstSessionID string
	for _, clientID := range []string{"cmd-owner-one", "cmd-owner-two"} {
		body := `{"message":"private recall","to":"mio","user_id":"attacker","viewer_client_id":"` + clientID + `"}`
		request := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
		request.Header.Set("X-RenCrow-Interaction-Profile", "cmd-chat")
		request.RemoteAddr = "127.0.0.1:12345"
		request.Host = "127.0.0.1:18790"
		attachViewerBridgeTestPresence(t, deps, request)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}

		select {
		case got := <-processor.received:
			if got.scope.ActorKind != domaintool.ActorKindAgent || got.scope.ActorID != "mio" || got.scope.AuthenticationSource != domaintool.AuthenticationSourceAgentOrchestrator ||
				got.scope.AuthenticatedUserID != "ren" || !got.scope.Allows(domaintool.DataScopeUser) || !got.scope.Allows(domaintool.DataScopePublic) {
				t.Fatalf("ProcessMessage scope=%+v, want Mio's agent execution scope with authenticated user ren", got.scope)
			}
			if !got.hasUserProof || got.userProof.ActorKind != domaintool.ActorKindUser || got.userProof.AuthenticationSource != domaintool.AuthenticationSourceHTTP || got.userProof.AuthenticatedUserID != "ren" {
				t.Fatalf("ProcessMessage HTTP User proof=%+v verified=%t, want validated ren proof carried through Agent delegation", got.userProof, got.hasUserProof)
			}
			if got.request.ChatID != "cmd:ren" {
				t.Fatalf("ChatID=%q, want private canonical CMD chat ID", got.request.ChatID)
			}
			if firstSessionID == "" {
				firstSessionID = got.request.SessionID
			} else if got.request.SessionID != firstSessionID {
				t.Fatalf("same owner SessionID changed from %q to %q", firstSessionID, got.request.SessionID)
			}
		case <-time.After(time.Second):
			t.Fatal("authenticated request did not reach ProcessMessage")
		}
		// ProcessMessage's capture channel unblocks before the async send handler
		// releases the CHAT persistence lease. Wait for that release before the
		// next request so the test does not create concurrent turns on one relay.
		deps.eventRelay.mu.RLock()
		activeChat := deps.eventRelay.chatBinding
		deps.eventRelay.mu.RUnlock()
		if activeChat != nil {
			select {
			case <-activeChat.done:
			case <-time.After(time.Second):
				t.Fatal("authenticated CHAT persistence lease was not released")
			}
		}
	}
	privateAddress, err := domainconversation.NewChannelAddress("viewer", "cmd:ren")
	if err != nil {
		t.Fatal(err)
	}
	ownerResolution, err := repo.LoadOrCreateCanonicalForOwner(context.Background(), createdAt.Format("2006-01-02"), privateAddress, "ren", createdAt)
	if err != nil {
		t.Fatalf("LoadOrCreateCanonicalForOwner after authenticated CMD send: %v", err)
	}
	if ownerResolution.Status != domainsession.CanonicalSessionOwnerMatched || ownerResolution.Session == nil || ownerResolution.BoundOwnerID != "ren" || ownerResolution.Session.ID() != firstSessionID {
		t.Fatalf("canonical Viewer session after authenticated CMD send=%+v, want owner-bound ren Session", ownerResolution)
	}
	publicSession, found, err := repo.LoadCanonical(context.Background(), createdAt.Format("2006-01-02"), legacyAddress)
	if err != nil || !found || publicSession == nil || publicSession.ID() != legacySession.ID() || publicSession.OwnerID() != "" {
		t.Fatalf("public legacy Session changed after private CMD send: session=%v found=%v err=%v", publicSession, found, err)
	}
}

type viewerBridgeGuardedRelayResult struct {
	request orchestrator.ProcessMessageRequest
	event   orchestrator.OrchestratorEvent
	ownerID string
	err     error
}

type viewerBridgeGuardedRelayProcessor struct {
	repository interface {
		LoadCanonicalForOwner(context.Context, string, string, domainconversation.ChannelAddress) (domainsession.CanonicalSessionOwnerResolution, error)
	}
	relay     *idleAwareEventListener
	published chan viewerBridgeGuardedRelayResult
}

func (p viewerBridgeGuardedRelayProcessor) ProcessMessage(ctx context.Context, req orchestrator.ProcessMessageRequest) (orchestrator.ProcessMessageResponse, error) {
	ownerScope, verified := domaintool.AuthenticatedHTTPUserScopeFromContext(ctx)
	if !verified {
		return orchestrator.ProcessMessageResponse{}, errors.New("authenticated Viewer User proof was not preserved into Mio ProcessMessage")
	}
	address, err := domainconversation.NewChannelAddress(req.Channel, req.ChatID)
	if err != nil {
		return orchestrator.ProcessMessageResponse{}, err
	}
	resolution, err := p.repository.LoadCanonicalForOwner(ctx, req.SessionID, ownerScope.AuthenticatedUserID, address)
	if err != nil {
		return orchestrator.ProcessMessageResponse{}, err
	}
	if resolution.Session == nil || resolution.BoundOwnerID != ownerScope.AuthenticatedUserID {
		return orchestrator.ProcessMessageResponse{}, errors.New("authenticated Viewer request did not resolve its canonical owner Session")
	}
	runID := modulecore.NewRunID()
	event := orchestrator.NewEventWithTraceID(
		modulecore.TraceID(req.TraceID), "agent.response", "mio", "user", "integrated CMD final", "CHAT",
		req.RootTaskID, req.SessionID, req.Channel, req.ChatID,
	)
	event.Visibility = "private"
	event.PublicationIncarnation = resolution.Session.PublicationIncarnation()
	event.TurnID = modulecore.TurnID(req.TurnID)
	event.MessageID = modulecore.MessageID(req.AgentMessageID)
	event.RunID = runID
	event.ActorKind = "agent"
	event.ActorID = "mio"
	accepted, publishErr := p.relay.PublishGuardedCanonicalEvent(
		event, ownerScope.AuthenticatedUserID, func(publish func() error) error { return publish() },
		guardedMioRelayTestQualification{},
	)
	p.published <- viewerBridgeGuardedRelayResult{request: req, event: accepted, ownerID: ownerScope.AuthenticatedUserID, err: publishErr}
	if publishErr != nil {
		return orchestrator.ProcessMessageResponse{}, publishErr
	}
	return orchestrator.ProcessMessageResponse{Response: accepted.Content, SessionID: req.SessionID}, nil
}

func TestAuthenticatedCMDMioBridgePublishesPrivateFinalThroughCanonicalRelay(t *testing.T) {
	const token = "viewer-send-owner-token-0123456789"
	tokenPath := filepath.Join(t.TempDir(), "agent-ops.token")
	if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true, AuthTokenFile: tokenPath, UserID: "ren"}}
	deps := newViewerBridgeLegacyAdmissionDependencies(t)
	// Drain accepted canonical RAM writes before the fixture's earlier cleanup
	// closes its SQLite EventStore.
	t.Cleanup(func() { shutdownRuntimeQueue(t, deps.persistenceQueue) })
	hub := viewer.NewEventHub(8)
	deps.eventHub = hub
	deps.eventRelay.hub = hub
	repository := sessionpersistence.NewJSONSessionRepository(t.TempDir())
	deps.sessionRepository = repository
	ownerEvents, _, _, _ := hub.SubscribeForReplayWithControlAndPrivateMioDeltas("ren")
	if ownerEvents == nil {
		t.Fatal("authenticated owner event subscription was not admitted")
	}
	defer hub.Unsubscribe(ownerEvents)
	processor := viewerBridgeGuardedRelayProcessor{repository: repository, relay: deps.eventRelay, published: make(chan viewerBridgeGuardedRelayResult, 1)}
	handler := buildViewerBridgeHandlers(cfg, deps, "", ttsEntryRuntime{}, repository).ViewerSendFromOrch(processor)
	request := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{"message":"private answer","to":"mio","user_id":"attacker"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
	request.Header.Set("X-RenCrow-Interaction-Profile", "cmd-chat")
	request.RemoteAddr = "127.0.0.1:12345"
	request.Host = "127.0.0.1:18790"
	attachViewerBridgeTestPresence(t, deps, request)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("authenticated CMD Mio send status=%d body=%s", response.Code, response.Body.String())
	}
	var result viewerBridgeGuardedRelayResult
	select {
	case result = <-processor.published:
	case <-time.After(time.Second):
		t.Fatal("authenticated CMD Mio bridge did not publish through the canonical relay")
	}
	if result.err != nil {
		t.Fatalf("guarded canonical relay publication: %v", result.err)
	}
	if result.ownerID != "ren" || result.request.ChatID != "cmd:ren" || result.request.SessionID == "" ||
		result.event.EventSeq <= 0 || result.event.EventID.Validate() != nil || result.event.Type != "agent.response" ||
		result.event.From != "mio" || result.event.To != "user" || result.event.Route != "CHAT" ||
		result.event.ChannelAddress != "cmd:ren" || result.event.Visibility != "private" ||
		result.event.PublicationIncarnation == "" || string(result.event.TurnID) != result.request.TurnID ||
		string(result.event.TraceID) != result.request.TraceID || string(result.event.SessionID) != result.request.SessionID ||
		string(result.event.MessageID) != result.request.AgentMessageID || result.event.TaskID.String() != result.request.RootTaskID {
		t.Fatalf("authenticated bridge final identity mismatch: owner=%q request=%+v event=%+v", result.ownerID, result.request, result.event)
	}
	select {
	case data := <-ownerEvents:
		var got orchestrator.OrchestratorEvent
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("decode owner live final: %v", err)
		}
		if got.EventID != result.event.EventID || got.EventSeq != result.event.EventSeq || got.PublicationIncarnation != result.event.PublicationIncarnation {
			t.Fatalf("owner live final=%+v, want canonical accepted identity %+v", got, result.event)
		}
		if !hub.ClaimOwnerScopedLiveEvent("ren", ownerEvents, data) {
			t.Fatal("authenticated owner final did not pass the last live claim")
		}
	default:
		t.Fatal("canonical private final was not queued to the authenticated owner stream")
	}
	if history := hub.History(); len(history) != 1 || history[0].EventID != result.event.EventID || history[0].PublicationIncarnation != result.event.PublicationIncarnation {
		t.Fatalf("canonical EventHub history=%+v, want exactly the stamped private final", history)
	}
}

func TestViewerBridgeKeepsUnboundPublicSessionSeparateFromAuthenticatedCMDMio(t *testing.T) {
	const token = "viewer-send-owner-token-0123456789"
	tokenPath := filepath.Join(t.TempDir(), "agent-ops.token")
	if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{LocalAgentOps: config.LocalAgentOpsConfig{
		Enabled: true, AuthTokenFile: tokenPath, UserID: "ren",
	}}
	repo := sessionpersistence.NewJSONSessionRepository(t.TempDir())
	now := time.Now().UTC()
	address, err := domainconversation.NewChannelAddress("viewer", "viewer-user")
	if err != nil {
		t.Fatal(err)
	}
	unbound, _, err := repo.LoadOrCreateCanonical(context.Background(), now.Format("2006-01-02"), address, now)
	if err != nil || unbound == nil {
		t.Fatalf("seed unbound canonical Session=%v err=%v", unbound, err)
	}
	processor := &viewerBridgeCountProcessor{done: make(chan struct{})}
	deps := newViewerBridgeLegacyAdmissionDependencies(t)
	handler := buildViewerBridgeHandlers(cfg, deps, "", ttsEntryRuntime{}, repo).ViewerSendFromOrch(processor)
	request := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{"message":"private recall","to":"mio"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
	request.Header.Set("X-RenCrow-Interaction-Profile", "cmd-chat")
	request.RemoteAddr = "127.0.0.1:12345"
	request.Host = "127.0.0.1:18790"
	attachViewerBridgeTestPresence(t, deps, request)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want private CMD session independent from legacy public Session", response.Code, response.Body.String())
	}
	select {
	case <-processor.done:
	case <-time.After(time.Second):
		t.Fatal("private CMD request did not reach ProcessMessage")
	}
	if processor.calls != 1 {
		t.Fatalf("processor calls=%d, want one private CMD request", processor.calls)
	}
	privateAddress, err := domainconversation.NewChannelAddress("viewer", "cmd:ren")
	if err != nil {
		t.Fatal(err)
	}
	ownerResolution, err := repo.LoadOrCreateCanonicalForOwner(context.Background(), now.Format("2006-01-02"), privateAddress, "ren", now)
	if err != nil {
		t.Fatalf("LoadOrCreateCanonicalForOwner after authenticated CMD send: %v", err)
	}
	if ownerResolution.Status != domainsession.CanonicalSessionOwnerMatched || ownerResolution.Session == nil || ownerResolution.BoundOwnerID != "ren" {
		t.Fatalf("private CMD Session=%+v, want owner-bound ren Session", ownerResolution)
	}
	publicSession, found, err := repo.LoadCanonical(context.Background(), now.Format("2006-01-02"), address)
	if err != nil || !found || publicSession == nil || publicSession.ID() != unbound.ID() || publicSession.OwnerID() != "" {
		t.Fatalf("unbound public Session changed after authenticated CMD send: session=%v found=%v err=%v", publicSession, found, err)
	}
}

type viewerBridgeScopedRequest struct {
	request      orchestrator.ProcessMessageRequest
	scope        domaintool.ToolExecutionScope
	userProof    domaintool.ToolExecutionScope
	hasUserProof bool
}

type viewerBridgeScopedProcessor struct {
	received chan viewerBridgeScopedRequest
}

func (p viewerBridgeScopedProcessor) ProcessMessage(ctx context.Context, req orchestrator.ProcessMessageRequest) (orchestrator.ProcessMessageResponse, error) {
	scope, _ := domaintool.ToolExecutionScopeFromContext(ctx)
	userProof, hasUserProof := domaintool.AuthenticatedHTTPUserScopeFromContext(ctx)
	p.received <- viewerBridgeScopedRequest{request: req, scope: scope, userProof: userProof, hasUserProof: hasUserProof}
	return orchestrator.ProcessMessageResponse{SessionID: req.SessionID}, nil
}

type viewerBridgeCaptureProcessor struct {
	requests chan orchestrator.ProcessMessageRequest
}

func (p viewerBridgeCaptureProcessor) ProcessMessage(_ context.Context, req orchestrator.ProcessMessageRequest) (orchestrator.ProcessMessageResponse, error) {
	p.requests <- req
	return orchestrator.ProcessMessageResponse{SessionID: req.SessionID}, nil
}

func TestViewerBridgeResolvesProductionCanonicalSessionBeforeAcceptance(t *testing.T) {
	repo := sessionpersistence.NewJSONSessionRepository(t.TempDir())
	processor := viewerBridgeCaptureProcessor{requests: make(chan orchestrator.ProcessMessageRequest, 1)}
	deps := &Dependencies{canonicalEventStore: openViewerBridgeAdmissionStore(t)}
	factories := buildViewerBridgeHandlers(&config.Config{}, deps, "", ttsEntryRuntime{}, repo)
	handler := factories.ViewerSendFromOrch(processor)

	req := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{"message":"hello","to":"mio"}`))
	req.Header.Set("Content-Type", "application/json")
	attachViewerBridgeTestPresence(t, deps, req)
	response := httptest.NewRecorder()
	handler(response, req)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var accepted struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&accepted); err != nil {
		t.Fatal(err)
	}
	if err := modulecore.SessionID(accepted.SessionID).Validate(); err != nil {
		t.Fatalf("accepted session_id=%q: %v", accepted.SessionID, err)
	}
	select {
	case observed := <-processor.requests:
		if observed.SessionID != accepted.SessionID {
			t.Fatalf("processor session_id=%q want accepted %q", observed.SessionID, accepted.SessionID)
		}
		if observed.Channel != "viewer" || observed.ChatID != "viewer-user" {
			t.Fatalf("public Viewer identity channel/chat=%q/%q, want viewer/viewer-user", observed.Channel, observed.ChatID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("processor did not receive the accepted request")
	}
}

func TestViewerBridgeRejectsOwnerBoundSessionAtPublicAddress(t *testing.T) {
	repo := sessionpersistence.NewJSONSessionRepository(t.TempDir())
	now := time.Now().UTC()
	address, err := domainconversation.NewChannelAddress("viewer", "viewer-user")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := domainsession.NewCanonicalSessionWithOwner(modulecore.NewSessionID(), "ren", now.Format("2006-01-02"), address, now)
	if err != nil {
		t.Fatalf("construct owner-bound Session: %v", err)
	}
	if err := repo.Save(context.Background(), sess); err != nil {
		t.Fatalf("seed owner-bound Session at public address: %v", err)
	}
	deps := &Dependencies{canonicalEventStore: openViewerBridgeAdmissionStore(t)}
	processor := viewerBridgeCaptureProcessor{requests: make(chan orchestrator.ProcessMessageRequest, 1)}
	handler := buildViewerBridgeHandlers(&config.Config{}, deps, "", ttsEntryRuntime{}, repo).ViewerSendFromOrch(processor)
	req := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{"message":"hello","to":"mio"}`))
	req.Header.Set("Content-Type", "application/json")
	attachViewerBridgeTestPresence(t, deps, req)
	response := httptest.NewRecorder()
	handler(response, req)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s, want public canonical identity rejection", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "session unavailable") {
		t.Fatalf("body=%q, want resolver failure before processor admission", response.Body.String())
	}
	select {
	case accepted := <-processor.requests:
		t.Fatalf("processor received request for owner-bound public session: %+v", accepted)
	default:
	}
}

func TestChromeBridgeUsesPublicSessionVisibilityForStatusReplayAndLiveEvents(t *testing.T) {
	repo := sessionpersistence.NewJSONSessionRepository(t.TempDir())
	if err := repo.WarmCanonicalIndex(context.Background()); err != nil {
		t.Fatalf("warm canonical Session projection: %v", err)
	}
	now := time.Now().UTC()
	day := now.Format("2006-01-02")
	ownerAddress, err := domainconversation.NewChannelAddress("viewer", "cmd:ren")
	if err != nil {
		t.Fatal(err)
	}
	ownerSession, err := domainsession.NewCanonicalSessionWithOwner(modulecore.NewSessionID(), "ren", day, ownerAddress, now)
	if err != nil {
		t.Fatalf("construct owner-bound Session: %v", err)
	}
	if err := repo.Save(context.Background(), ownerSession); err != nil {
		t.Fatalf("seed owner-bound Session: %v", err)
	}
	publicAddress, err := domainconversation.NewChannelAddress("viewer", "viewer-user")
	if err != nil {
		t.Fatal(err)
	}
	publicSession, _, err := repo.LoadOrCreateCanonical(context.Background(), day, publicAddress, now)
	if err != nil || publicSession == nil {
		t.Fatalf("seed public Session=%v err=%v", publicSession, err)
	}

	hub := viewer.NewEventHub(10)
	ownerReplay := orchestrator.NewEvent("entry.stage", "system", "viewer", "owner-private-stage", "", "task-owner-private", ownerSession.ID(), "viewer", "cmd:ren")
	ownerReplay.PublicationIncarnation = ownerSession.PublicationIncarnation()
	publicReplay := orchestrator.NewEvent("entry.stage", "system", "viewer", "public-stage", "", "task-public", publicSession.ID(), "viewer", "viewer-user")
	publicReplay.PublicationIncarnation = publicSession.PublicationIncarnation()
	hub.OnEvent(ownerReplay)
	hub.OnEvent(publicReplay)
	deps := &Dependencies{eventHub: hub, sessionVisibility: newRuntimeSessionVisibility(repo)}
	_, status, events := buildViewerBridgeHandlers(&config.Config{}, deps, "", ttsEntryRuntime{}, repo).ChromeBridgeFromOrch(&viewerBridgeCountProcessor{})

	privateStatus := httptest.NewRecorder()
	status.ServeHTTP(privateStatus, httptest.NewRequest(http.MethodGet, "/?session_id="+ownerSession.ID(), nil))
	if privateStatus.Code != http.StatusForbidden {
		t.Fatalf("owner-bound Chrome status=%d body=%s, want public-only access denied", privateStatus.Code, privateStatus.Body.String())
	}
	publicStatus := httptest.NewRecorder()
	status.ServeHTTP(publicStatus, httptest.NewRequest(http.MethodGet, "/?session_id="+publicSession.ID(), nil))
	if publicStatus.Code != http.StatusOK || !strings.Contains(publicStatus.Body.String(), `"stage":"public-stage"`) || strings.Contains(publicStatus.Body.String(), "owner-private-stage") {
		t.Fatalf("public Chrome status=%d body=%s, want only public session stage", publicStatus.Code, publicStatus.Body.String())
	}

	privateEvents := httptest.NewRecorder()
	events.ServeHTTP(privateEvents, httptest.NewRequest(http.MethodGet, "/?session_id="+ownerSession.ID(), nil))
	if privateEvents.Code != http.StatusForbidden {
		t.Fatalf("owner-bound Chrome events=%d body=%s, want public-only access denied", privateEvents.Code, privateEvents.Body.String())
	}

	streamContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	publicEvents := newViewerBridgeSSERecorder()
	publicRequest := httptest.NewRequest(http.MethodGet, "/?session_id="+publicSession.ID(), nil).WithContext(streamContext)
	done := make(chan struct{})
	go func() {
		events.ServeHTTP(publicEvents, publicRequest)
		close(done)
	}()
	deadline := time.Now().Add(time.Second)
	for hub.ClientCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if hub.ClientCount() == 0 {
		cancel()
		<-done
		t.Fatalf("public Chrome event stream did not subscribe: status=%d body=%s", publicEvents.statusSnapshot(), publicEvents.bodySnapshot())
	}
	if !publicEvents.waitForBodyContains("public-stage", time.Second) {
		cancel()
		<-done
		t.Fatalf("public Chrome event stream did not replay public event: %s", publicEvents.bodySnapshot())
	}
	ownerLive := orchestrator.NewEvent("agent.response", "mio", "viewer", "owner-private-live", "", "task-owner-live", ownerSession.ID(), "viewer", "cmd:ren")
	ownerLive.PublicationIncarnation = ownerSession.PublicationIncarnation()
	hub.OnEvent(ownerLive)
	publicLive := orchestrator.NewEvent("agent.response", "shiro", "viewer", "public-live", "", "task-public-live", publicSession.ID(), "viewer", "viewer-user")
	publicLive.PublicationIncarnation = publicSession.PublicationIncarnation()
	hub.OnEvent(publicLive)
	if !publicEvents.waitForBodyContains("public-live", time.Second) {
		cancel()
		<-done
		t.Fatalf("public Chrome event stream did not deliver public live event: %s", publicEvents.bodySnapshot())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("public Chrome event stream did not stop after request cancellation")
	}
	streamBody := publicEvents.bodySnapshot()
	if publicEvents.statusSnapshot() != http.StatusOK || !strings.Contains(streamBody, "public-stage") || !strings.Contains(streamBody, "public-live") {
		t.Fatalf("public Chrome events=%d body=%s, want public replay and live events", publicEvents.statusSnapshot(), streamBody)
	}
	if strings.Contains(streamBody, "owner-private-stage") || strings.Contains(streamBody, "owner-private-live") {
		t.Fatalf("public Chrome event stream leaked owner-bound events: %s", streamBody)
	}
}

func TestChromeBridgeStatusAndEventsFailClosedWithoutSessionVisibility(t *testing.T) {
	for _, deps := range []*Dependencies{nil, {eventHub: viewer.NewEventHub(2)}} {
		_, status, events := buildViewerBridgeHandlers(&config.Config{}, deps, "", ttsEntryRuntime{}, sessionpersistence.NewJSONSessionRepository(t.TempDir())).ChromeBridgeFromOrch(&viewerBridgeCountProcessor{})
		for name, handler := range map[string]http.HandlerFunc{"status": status, "events": events} {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/?session_id=ses_018f4e2a-7b2d-7abc-8def-0123456789ab", nil))
			if response.Code != http.StatusServiceUnavailable {
				t.Errorf("%s with nil session visibility returned %d body=%s, want fail closed", name, response.Code, response.Body.String())
			}
		}
	}
}

func TestChromeBridgeRequiresCurrentPublicationIncarnationForStatusReplayAndLiveEvents(t *testing.T) {
	repo := sessionpersistence.NewJSONSessionRepository(t.TempDir())
	if err := repo.WarmCanonicalIndex(context.Background()); err != nil {
		t.Fatalf("warm canonical Session projection: %v", err)
	}
	now := time.Now().UTC()
	day := now.Format("2006-01-02")
	sessionID := modulecore.NewSessionID()

	oldPublicAddress, err := domainconversation.NewChannelAddress("viewer", "old-public-user")
	if err != nil {
		t.Fatal(err)
	}
	oldPublic, err := domainsession.NewCanonicalSession(sessionID, day, oldPublicAddress, now)
	if err != nil {
		t.Fatalf("construct old public Session: %v", err)
	}
	if err := repo.Save(context.Background(), oldPublic); err != nil {
		t.Fatalf("save old public Session: %v", err)
	}
	oldPublicEvent := chromeBridgePublicationEvent(sessionID, oldPublicAddress, oldPublic.PublicationIncarnation(), "old-public-stage")

	if err := repo.Delete(context.Background(), string(sessionID)); err != nil {
		t.Fatalf("delete old public Session: %v", err)
	}
	privateAddress, err := domainconversation.NewChannelAddress("viewer", "cmd:ren")
	if err != nil {
		t.Fatal(err)
	}
	oldPrivate, err := domainsession.NewCanonicalSessionWithOwner(sessionID, "ren", day, privateAddress, now.Add(time.Second))
	if err != nil {
		t.Fatalf("construct private Session with reused ID: %v", err)
	}
	if err := repo.Save(context.Background(), oldPrivate); err != nil {
		t.Fatalf("save private Session with reused ID: %v", err)
	}
	oldPrivateEvent := chromeBridgePublicationEvent(sessionID, privateAddress, oldPrivate.PublicationIncarnation(), "old-private-stage")

	if err := repo.Delete(context.Background(), string(sessionID)); err != nil {
		t.Fatalf("delete private Session: %v", err)
	}
	currentAddress, err := domainconversation.NewChannelAddress("viewer", "new-public-user")
	if err != nil {
		t.Fatal(err)
	}
	currentPublic, err := domainsession.NewCanonicalSession(sessionID, day, currentAddress, now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("construct current public Session with reused ID: %v", err)
	}
	if err := repo.Save(context.Background(), currentPublic); err != nil {
		t.Fatalf("save current public Session: %v", err)
	}
	unstampedEntryStage := chromeBridgePublicationEvent(sessionID, currentAddress, "", "unstamped-direct-entry-stage")

	hub := viewer.NewEventHub(10)
	hub.OnEvent(oldPublicEvent)
	hub.OnEvent(oldPrivateEvent)
	hub.OnEvent(unstampedEntryStage)
	deps := &Dependencies{eventHub: hub, sessionVisibility: newRuntimeSessionVisibility(repo)}
	_, status, events := buildViewerBridgeHandlers(&config.Config{}, deps, "", ttsEntryRuntime{}, repo).ChromeBridgeFromOrch(&viewerBridgeCountProcessor{})

	statusResponse := httptest.NewRecorder()
	status.ServeHTTP(statusResponse, httptest.NewRequest(http.MethodGet, "/?session_id="+string(sessionID), nil))
	if statusResponse.Code != http.StatusOK || !strings.Contains(statusResponse.Body.String(), `"stage":"unknown"`) {
		t.Fatalf("status=%d body=%s, want unknown when all same-ID candidates are stale or unstamped", statusResponse.Code, statusResponse.Body.String())
	}
	for _, secret := range []string{"old-public-stage", "old-private-stage", "unstamped-direct-entry-stage", "old-public-user", "cmd:ren", "old-public-task", "old-private-task"} {
		if strings.Contains(statusResponse.Body.String(), secret) {
			t.Fatalf("status leaked rejected candidate field %q: %s", secret, statusResponse.Body.String())
		}
	}

	freshReplay := chromeBridgePublicationEvent(sessionID, currentAddress, currentPublic.PublicationIncarnation(), "fresh-public-stage")
	hub.OnEvent(freshReplay)
	statusResponse = httptest.NewRecorder()
	status.ServeHTTP(statusResponse, httptest.NewRequest(http.MethodGet, "/?session_id="+string(sessionID), nil))
	if statusResponse.Code != http.StatusOK || !strings.Contains(statusResponse.Body.String(), `"stage":"fresh-public-stage"`) {
		t.Fatalf("status=%d body=%s, want the fresh public stage", statusResponse.Code, statusResponse.Body.String())
	}
	for _, secret := range []string{"old-public-stage", "old-private-stage", "unstamped-direct-entry-stage", "old-public-task", "old-private-task"} {
		if strings.Contains(statusResponse.Body.String(), secret) {
			t.Fatalf("status leaked rejected candidate field %q: %s", secret, statusResponse.Body.String())
		}
	}

	streamContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	publicEvents := newViewerBridgeSSERecorder()
	request := httptest.NewRequest(http.MethodGet, "/?session_id="+string(sessionID), nil).WithContext(streamContext)
	done := make(chan struct{})
	go func() {
		events.ServeHTTP(publicEvents, request)
		close(done)
	}()
	deadline := time.Now().Add(time.Second)
	for hub.ClientCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if hub.ClientCount() == 0 {
		cancel()
		<-done
		t.Fatalf("Chrome event stream did not subscribe: status=%d body=%s", publicEvents.statusSnapshot(), publicEvents.bodySnapshot())
	}
	if !publicEvents.waitForBodyContains("fresh-public-stage", time.Second) {
		cancel()
		<-done
		t.Fatalf("Chrome event stream did not replay fresh public event: %s", publicEvents.bodySnapshot())
	}
	hub.OnEvent(chromeBridgePublicationEvent(sessionID, privateAddress, oldPrivate.PublicationIncarnation(), "stale-private-live"))
	hub.OnEvent(chromeBridgePublicationEvent(sessionID, currentAddress, currentPublic.PublicationIncarnation(), "fresh-public-live"))
	if !publicEvents.waitForBodyContains("fresh-public-live", time.Second) {
		cancel()
		<-done
		t.Fatalf("Chrome event stream did not deliver fresh public live event: %s", publicEvents.bodySnapshot())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Chrome event stream did not stop after request cancellation")
	}
	streamBody := publicEvents.bodySnapshot()
	for _, visible := range []string{"fresh-public-stage", "fresh-public-live"} {
		if !strings.Contains(streamBody, visible) {
			t.Errorf("Chrome event stream omitted %q: %s", visible, streamBody)
		}
	}
	for _, secret := range []string{"old-public-stage", "old-private-stage", "unstamped-direct-entry-stage", "stale-private-live", "old-public-task", "old-private-task"} {
		if strings.Contains(streamBody, secret) {
			t.Errorf("Chrome event stream leaked rejected candidate %q: %s", secret, streamBody)
		}
	}
}

func chromeBridgePublicationEvent(sessionID modulecore.SessionID, address domainconversation.ChannelAddress, incarnation, content string) orchestrator.OrchestratorEvent {
	event := orchestrator.NewEvent("entry.stage", "system", "viewer", content, "CODE3", "task-"+strings.ReplaceAll(content, " ", "-"), string(sessionID), address.ChannelType(), address.ExternalConversationID())
	event.PublicationIncarnation = incarnation
	return event
}

type chromeBridgePublicationBindingReader struct {
	mu              sync.Mutex
	current         domainsession.CanonicalSessionPublicationBinding
	afterRead       domainsession.CanonicalSessionPublicationBinding
	changeOnRead    int
	publicationRead int
}

func (r *chromeBridgePublicationBindingReader) CurrentPublicationBinding(_ context.Context, _ string) (domainsession.CanonicalSessionPublicationBinding, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.publicationRead++
	resolved := r.current
	if r.changeOnRead > 0 && r.publicationRead == r.changeOnRead {
		r.current = r.afterRead
	}
	return resolved, nil
}

func (r *chromeBridgePublicationBindingReader) UsePublicationBinding(_ context.Context, expected domainsession.CanonicalSessionPublicationBinding, claim func() error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.current.SameIdentity(expected) {
		return errors.New("canonical Session publication binding changed")
	}
	return claim()
}

func TestChromeBridgeRejectsPublicationBindingThatChangesDuringPredicate(t *testing.T) {
	repo := sessionpersistence.NewJSONSessionRepository(t.TempDir())
	if err := repo.WarmCanonicalIndex(context.Background()); err != nil {
		t.Fatalf("warm canonical Session projection: %v", err)
	}
	now := time.Now().UTC()
	day := now.Format("2006-01-02")
	sessionID := modulecore.NewSessionID()
	address, err := domainconversation.NewChannelAddress("viewer", "viewer-user")
	if err != nil {
		t.Fatal(err)
	}
	oldSession, err := domainsession.NewCanonicalSession(sessionID, day, address, now)
	if err != nil {
		t.Fatal(err)
	}
	newSession, err := domainsession.NewCanonicalSession(sessionID, day, address, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	oldBinding, err := domainsession.NewCanonicalSessionPublicationBinding(oldSession)
	if err != nil {
		t.Fatal(err)
	}
	newBinding, err := domainsession.NewCanonicalSessionPublicationBinding(newSession)
	if err != nil {
		t.Fatal(err)
	}
	reader := &chromeBridgePublicationBindingReader{current: oldBinding, afterRead: newBinding, changeOnRead: 2}
	hub := viewer.NewEventHub(2)
	hub.OnEvent(chromeBridgePublicationEvent(sessionID, address, oldSession.PublicationIncarnation(), "raced-old-stage"))
	deps := &Dependencies{eventHub: hub, sessionVisibility: &runtimeSessionVisibility{reader: reader}}
	_, status, events := buildViewerBridgeHandlers(&config.Config{}, deps, "", ttsEntryRuntime{}, repo).ChromeBridgeFromOrch(&viewerBridgeCountProcessor{})

	statusResponse := httptest.NewRecorder()
	status.ServeHTTP(statusResponse, httptest.NewRequest(http.MethodGet, "/?session_id="+string(sessionID), nil))
	if statusResponse.Code != http.StatusOK || !strings.Contains(statusResponse.Body.String(), `"stage":"unknown"`) || strings.Contains(statusResponse.Body.String(), "raced-old-stage") {
		t.Fatalf("status=%d body=%s, want unknown after binding changes between ResolveEvent and IsCurrent", statusResponse.Code, statusResponse.Body.String())
	}

	reader.mu.Lock()
	reader.current = oldBinding
	reader.publicationRead = 0
	reader.mu.Unlock()
	streamContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	publicEvents := newViewerBridgeSSERecorder()
	request := httptest.NewRequest(http.MethodGet, "/?session_id="+string(sessionID), nil).WithContext(streamContext)
	done := make(chan struct{})
	go func() {
		events.ServeHTTP(publicEvents, request)
		close(done)
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		reader.mu.Lock()
		reads := reader.publicationRead
		reader.mu.Unlock()
		if hub.ClientCount() > 0 && reads >= 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	reader.mu.Lock()
	reads := reader.publicationRead
	reader.mu.Unlock()
	if hub.ClientCount() == 0 || reads < 2 {
		cancel()
		<-done
		t.Fatalf("Chrome replay did not evaluate the raced binding: clients=%d publication reads=%d status=%d body=%s", hub.ClientCount(), reads, publicEvents.statusSnapshot(), publicEvents.bodySnapshot())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Chrome event stream did not stop after request cancellation")
	}
	if strings.Contains(publicEvents.bodySnapshot(), "raced-old-stage") {
		t.Fatalf("Chrome event stream wrote event after current binding changed during predicate: %s", publicEvents.bodySnapshot())
	}
}

func TestViewerBridgeKeepsLegacyRouteWhenCombinedChatProcessorIsIneligible(t *testing.T) {
	cfg := &config.Config{TTS: config.TTSConfig{SpeechPlanEnabled: true}}
	const ownerToken = "viewer-send-owner-token-0123456789"
	ownerTokenPath := filepath.Join(t.TempDir(), "agent-ops.token")
	if err := os.WriteFile(ownerTokenPath, []byte(ownerToken), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.LocalAgentOps = config.LocalAgentOpsConfig{Enabled: true, AuthTokenFile: ownerTokenPath, UserID: "ren"}
	processorEligible := runtimeCombinedChatAdmissionEnabled(
		cfg,
		runtimeRouteReplyClassifierStub{},
		runtimeCombinedTurnEngineStub{},
		true,
		false,
	)
	if processorEligible {
		t.Fatal("speech-plan configuration unexpectedly allowed combined CHAT admission")
	}
	processor := &viewerBridgeCountProcessor{done: make(chan struct{})}
	sessionDir := t.TempDir()
	sessionRepo := sessionpersistence.NewJSONSessionRepository(sessionDir)
	deps := &Dependencies{
		canonicalEventStore:           openViewerBridgeAdmissionStore(t),
		combinedChatProcessorEligible: processorEligible,
	}
	factories := buildViewerBridgeHandlers(
		cfg,
		deps,
		"",
		ttsEntryRuntime{},
		sessionRepo,
	)
	handler := factories.ViewerSendFromOrch(processor)
	request := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{"message":"hello","to":"mio"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+ownerToken)
	request.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
	request.Header.Set("X-RenCrow-Interaction-Profile", "cmd-chat")
	request.RemoteAddr = "127.0.0.1:12345"
	request.Host = "127.0.0.1:18790"
	attachViewerBridgeTestPresence(t, deps, request)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want legacy route to remain available when combined CHAT is ineligible", response.Code, response.Body.String())
	}
	select {
	case <-processor.done:
	case <-time.After(time.Second):
		t.Fatal("legacy processor did not receive the request")
	}
	if processor.calls != 1 {
		t.Fatalf("legacy processor calls=%d, want 1", processor.calls)
	}
	entries, err := os.ReadDir(sessionDir)
	if err != nil || len(entries) != 1 || entries[0].IsDir() || !strings.HasSuffix(entries[0].Name(), ".json") {
		t.Fatalf("canonical Session files after ineligible route = (%v,%v), want one synchronously persisted Session", entries, err)
	}
	persisted, err := sessionpersistence.NewJSONSessionRepository(sessionDir).Load(context.Background(), strings.TrimSuffix(entries[0].Name(), ".json"))
	if err != nil || persisted == nil || persisted.OwnerID() != "ren" {
		t.Fatalf("persisted ineligible-route Session = (%v,%v), want owner-bound ren Session", persisted, err)
	}
	wantAddress, err := domainconversation.NewChannelAddress("viewer", "cmd:ren")
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ChannelAddress() != wantAddress {
		t.Fatalf("persisted ineligible-route ChannelAddress = %+v, want %+v", persisted.ChannelAddress(), wantAddress)
	}
}

func TestViewerBridgeFailsClosedBeforeProcessorWhenEligibleOwnerQueueIsUnavailable(t *testing.T) {
	const ownerToken = "viewer-send-owner-token-0123456789"
	ownerTokenPath := filepath.Join(t.TempDir(), "agent-ops.token")
	if err := os.WriteFile(ownerTokenPath, []byte(ownerToken), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		TaskStore:   config.TaskStoreConfig{Index: config.TaskStoreIndexConfig{Enabled: true}},
		ActionStore: config.ActionStoreConfig{Index: config.ActionStoreIndexConfig{Enabled: true}},
	}
	cfg.LocalAgentOps = config.LocalAgentOpsConfig{Enabled: true, AuthTokenFile: ownerTokenPath, UserID: "ren"}
	processorEligible := runtimeCombinedChatAdmissionEnabled(
		cfg,
		runtimeRouteReplyClassifierStub{},
		runtimeCombinedTurnEngineStub{},
		true,
		false,
	)
	if !processorEligible {
		t.Fatal("test fixture does not satisfy combined CHAT processor eligibility")
	}
	processor := &viewerBridgeCountProcessor{}
	deps := &Dependencies{
		combinedChatProcessorEligible: processorEligible,
		combinedChatAdmissionEnabled:  false,
	}
	handler := buildViewerBridgeHandlers(
		cfg,
		deps,
		"",
		ttsEntryRuntime{},
		sessionpersistence.NewJSONSessionRepository(t.TempDir()),
	).ViewerSendFromOrch(processor)
	request := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{"message":"hello","to":"mio"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+ownerToken)
	request.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
	request.Header.Set("X-RenCrow-Interaction-Profile", "cmd-chat")
	request.RemoteAddr = "127.0.0.1:12345"
	request.Host = "127.0.0.1:18790"
	attachViewerBridgeTestPresence(t, deps, request)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s, want fail-closed admission when a configured owner is not queue-bound", response.Code, response.Body.String())
	}
	if processor.calls != 0 {
		t.Fatalf("processor calls=%d, want 0 after pre-LLM admission rejection", processor.calls)
	}
}

type viewerBridgeSpeechGateProcessor struct {
	start  chan struct{}
	result chan error
}

func (p viewerBridgeSpeechGateProcessor) ProcessMessage(ctx context.Context, req orchestrator.ProcessMessageRequest) (orchestrator.ProcessMessageResponse, error) {
	<-p.start
	speechCtx, done := orchestrator.SpeechSynthesisContext(ctx)
	defer done()
	p.result <- speechCtx.Err()
	return orchestrator.ProcessMessageResponse{SessionID: req.SessionID}, nil
}

func TestViewerBridgeCancellationUsesSharedRegistryAndLatchesBeforeSpeechStart(t *testing.T) {
	processor := viewerBridgeSpeechGateProcessor{
		start:  make(chan struct{}),
		result: make(chan error, 1),
	}
	deps := &Dependencies{canonicalEventStore: openViewerBridgeAdmissionStore(t)}
	factories := buildViewerBridgeHandlers(
		&config.Config{},
		deps,
		"",
		ttsEntryRuntime{},
		sessionpersistence.NewJSONSessionRepository(t.TempDir()),
	)
	handler := factories.ViewerSendFromOrch(processor)

	req := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{"message":"hello","to":"mio"}`))
	req.Header.Set("Content-Type", "application/json")
	attachViewerBridgeTestPresence(t, deps, req)
	response := httptest.NewRecorder()
	handler(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q want=no-store", got)
	}
	var accepted struct {
		TurnID            string `json:"turn_id"`
		SpeechCancelToken string `json:"speech_cancel_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.TurnID == "" || accepted.SpeechCancelToken == "" {
		t.Fatalf("accepted response missing turn cancellation identity: %+v", accepted)
	}

	cancelBody, err := json.Marshal(map[string]string{"speech_cancel_token": accepted.SpeechCancelToken})
	if err != nil {
		t.Fatal(err)
	}
	cancelResponse := httptest.NewRecorder()
	cancelRequest := httptest.NewRequest(http.MethodPost, "/viewer/tts/cancel", strings.NewReader(string(cancelBody)))
	factories.SpeechCancel.ServeHTTP(cancelResponse, cancelRequest)
	if cancelResponse.Code != http.StatusNoContent {
		t.Fatalf("cancel status=%d want=%d body=%s", cancelResponse.Code, http.StatusNoContent, cancelResponse.Body.String())
	}

	close(processor.start)
	select {
	case err := <-processor.result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("speech context error=%v want=%v", err, context.Canceled)
		}
	case <-time.After(time.Second):
		t.Fatal("speech processor did not complete")
	}
}

type viewerBridgePlaybackProgressProcessor struct {
	ready   chan struct{}
	started chan error
}

func (p viewerBridgePlaybackProgressProcessor) ProcessMessage(ctx context.Context, req orchestrator.ProcessMessageRequest) (orchestrator.ProcessMessageResponse, error) {
	progress := speech.PlaybackProgressFromContext(ctx)
	if progress == nil {
		p.started <- errors.New("Viewer playback progress was not attached to accepted turn")
		return orchestrator.ProcessMessageResponse{}, errors.New("Viewer playback progress is unavailable")
	}
	if err := progress.MarkReady(0); err != nil {
		p.started <- err
		return orchestrator.ProcessMessageResponse{}, err
	}
	close(p.ready)
	err := progress.WaitStarted(ctx, 0)
	p.started <- err
	return orchestrator.ProcessMessageResponse{SessionID: req.SessionID}, nil
}

func TestViewerBridgePlaybackProgressUsesAcceptedTurnAndActiveAudioOwner(t *testing.T) {
	resetActiveViewerControlForTest()
	defer resetActiveViewerControlForTest()
	activeViewerControl.Claim(moduletts.ViewerActiveKindAudio, "audio-owner")
	processor := viewerBridgePlaybackProgressProcessor{
		ready:   make(chan struct{}),
		started: make(chan error, 1),
	}
	deps := &Dependencies{canonicalEventStore: openViewerBridgeAdmissionStore(t)}
	factories := buildViewerBridgeHandlers(
		&config.Config{},
		deps,
		"",
		ttsEntryRuntime{},
		sessionpersistence.NewJSONSessionRepository(t.TempDir()),
	)
	handler := factories.ViewerSendFromOrch(processor)
	request := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{"message":"hello","to":"mio","viewer_client_id":"untrusted-send-id"}`))
	request.Header.Set("Content-Type", "application/json")
	attachViewerBridgeTestPresence(t, deps, request)
	response := httptest.NewRecorder()
	handler(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("send status=%d body=%s", response.Code, response.Body.String())
	}
	var accepted struct {
		RootTaskID string `json:"root_task_id"`
		TurnID     string `json:"turn_id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.RootTaskID == "" || accepted.TurnID == "" {
		t.Fatalf("accepted response lacks progress identity: %+v", accepted)
	}
	select {
	case <-processor.ready:
	case <-time.After(time.Second):
		t.Fatal("turn did not mark utterance ready")
	}

	for _, viewerClientID := range []string{"untrusted-send-id", "audio-owner"} {
		body, err := json.Marshal(map[string]any{
			"public_playback_ref": accepted.RootTaskID,
			"turn_id":             accepted.TurnID,
			"utterance_index":     0,
			"phase":               "started",
			"viewer_client_id":    viewerClientID,
		})
		if err != nil {
			t.Fatal(err)
		}
		progressResponse := httptest.NewRecorder()
		progressRequest := httptest.NewRequest(http.MethodPost, "/viewer/tts/progress", strings.NewReader(string(body)))
		factories.SpeechProgress.ServeHTTP(progressResponse, progressRequest)
		if progressResponse.Code != http.StatusNoContent {
			t.Fatalf("viewer=%s progress status=%d want=%d body=%s", viewerClientID, progressResponse.Code, http.StatusNoContent, progressResponse.Body.String())
		}
		if got := progressResponse.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("Cache-Control=%q want=no-store", got)
		}
	}
	select {
	case err := <-processor.started:
		if err != nil {
			t.Fatalf("accepted turn did not receive selected active audio progress: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("speech progress did not wake the accepted turn")
	}
}

func TestViewerAsyncErrorEventKeepsAcceptedIngressTrace(t *testing.T) {
	const token = "viewer-send-owner-token-0123456789"
	tokenPath := filepath.Join(t.TempDir(), "agent-ops.token")
	if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	store := openViewerBridgeAdmissionStore(t)
	archive, err := viewer.NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog() error = %v", err)
	}
	deps := &Dependencies{
		eventRelay:          &idleAwareEventListener{archive: archive},
		canonicalEventStore: store,
	}
	cfg := &config.Config{LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true, AuthTokenFile: tokenPath, UserID: "ren"}}
	repo := sessionpersistence.NewJSONSessionRepository(t.TempDir())
	ownerAddress, err := domainconversation.NewChannelAddress("viewer", "cmd:ren")
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Now().UTC()
	ownerSession, err := domainsession.NewCanonicalSessionWithOwner(modulecore.NewSessionID(), "ren", createdAt.Format("2006-01-02"), ownerAddress, createdAt)
	if err != nil {
		t.Fatalf("construct owner-bound CMD Session: %v", err)
	}
	if err := repo.Save(context.Background(), ownerSession); err != nil {
		t.Fatalf("seed owner-bound CMD Session: %v", err)
	}
	acceptedBinding, err := domainsession.NewCanonicalSessionPublicationBinding(ownerSession)
	if err != nil {
		t.Fatalf("capture accepted owner-bound CMD Session: %v", err)
	}
	replacement, err := domainsession.NewCanonicalSessionWithOwner(modulecore.SessionID(ownerSession.ID()), "ren", createdAt.Format("2006-01-02"), ownerAddress, createdAt.Add(time.Second))
	if err != nil {
		t.Fatalf("construct re-bound owner Session: %v", err)
	}
	replacementBinding, err := domainsession.NewCanonicalSessionPublicationBinding(replacement)
	if err != nil {
		t.Fatalf("capture replacement owner Session: %v", err)
	}
	if acceptedBinding.SameIdentity(replacementBinding) {
		t.Fatal("test replacement must represent a different publication incarnation")
	}
	factories := buildViewerBridgeHandlers(cfg, deps, "", ttsEntryRuntime{}, repo)
	handler := factories.ViewerSendFromOrch(viewerBridgeErrorAfterSessionRebindProcessor{repository: repo, replacement: replacement})

	req := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{"message":"hello","to":"mio","timings":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
	req.Header.Set("X-RenCrow-Interaction-Profile", "cmd-chat")
	req.RemoteAddr = "127.0.0.1:12345"
	req.Host = "127.0.0.1:18790"
	attachViewerBridgeTestPresence(t, deps, req)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	var accepted struct {
		OK         bool   `json:"ok"`
		RootTaskID string `json:"root_task_id"`
		TraceID    string `json:"trace_id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&accepted); err != nil {
		t.Fatalf("decode accepted response: %v", err)
	}
	if !accepted.OK || accepted.RootTaskID == "" || accepted.TraceID == "" {
		t.Fatalf("accepted response = %+v, want root_task_id and trace_id", accepted)
	}
	if err := modulecore.TraceID(accepted.TraceID).Validate(); err != nil {
		t.Fatalf("accepted trace_id = %q is invalid: %v", accepted.TraceID, err)
	}
	if accepted.RootTaskID == accepted.TraceID {
		t.Fatalf("root_task_id and trace_id must remain distinct: %q", accepted.RootTaskID)
	}

	var event modulecore.EventEnvelope
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stored, readErr := store.ListByComponent(context.Background(), "orchestrator", 100)
		if readErr != nil {
			t.Fatalf("read canonical orchestrator events: %v", readErr)
		}
		for _, candidate := range stored {
			if candidate.EventType == "viewer.error" && candidate.Payload["trace_id"] == accepted.TraceID {
				event = candidate
				break
			}
		}
		if event.EventType != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if event.EventType == "" {
		t.Fatal("timed out waiting for viewer.error canonical event")
	}
	if event.EventType != "viewer.error" {
		t.Fatalf("event type = %q, want viewer.error", event.EventType)
	}
	taskID, _ := event.Payload["task_id"].(string)
	traceID, _ := event.Payload["trace_id"].(string)
	if taskID != accepted.RootTaskID {
		t.Fatalf("viewer.error task_id = %q, want accepted root_task_id %q", taskID, accepted.RootTaskID)
	}
	if traceID != accepted.TraceID {
		t.Fatalf("viewer.error trace_id = %q, want accepted trace_id %q", traceID, accepted.TraceID)
	}
	channelAddress, ok := event.Payload["channel_address"].(string)
	if !ok || channelAddress != "cmd:ren" {
		t.Fatalf("viewer.error channel_address = %#v, want the authenticated user's CMD transport namespace", event.Payload["channel_address"])
	}
	if got := event.Payload["publication_incarnation"]; got != acceptedBinding.PublicationIncarnation() {
		t.Fatalf("viewer.error publication_incarnation = %#v, want captured accepted incarnation %q", got, acceptedBinding.PublicationIncarnation())
	}
	if got := event.Payload["visibility"]; got != "private" {
		t.Fatalf("viewer.error visibility = %#v, want private", got)
	}
	if got := event.Payload["channel"]; got != "viewer" {
		t.Fatalf("viewer.error channel = %#v, want viewer", got)
	}
	if _, exists := event.Payload["actor_kind"]; exists {
		t.Fatalf("viewer.error actor_kind = %#v, want no task execution identity before Run issuance", event.Payload["actor_kind"])
	}
	if _, exists := event.Payload["actor_id"]; exists {
		t.Fatalf("viewer.error actor_id = %#v, want no task execution identity before Run issuance", event.Payload["actor_id"])
	}
	if event.TraceID != modulecore.TraceID(accepted.TraceID) {
		t.Fatalf("canonical envelope trace_id = %q, want accepted trace_id %q", event.TraceID, accepted.TraceID)
	}
	timing, ok := event.Payload["timing"].(map[string]any)
	if !ok {
		t.Fatalf("viewer.error timing = %#v, want partial timing snapshot", event.Payload["timing"])
	}
	if timing["schema_version"] != float64(turntiming.SchemaVersion) {
		t.Fatalf("viewer.error timing schema_version = %#v", timing["schema_version"])
	}
	points, ok := timing["points_ms"].(map[string]any)
	if !ok {
		t.Fatalf("viewer.error timing points_ms = %#v", timing["points_ms"])
	}
	for _, point := range []string{"handler_received", "accepted", "async_start", "route_decided", "dispatch_started"} {
		if _, ok := points[point]; !ok {
			t.Errorf("viewer.error timing missing partial point %q: %#v", point, points)
		}
	}
	if _, ok := points[string(turntiming.PointResponsePublicationStarted)]; ok {
		t.Fatalf("failed turn must not invent response_publication_started: %#v", points)
	}
}

func TestLegacyCMDMioTerminalErrorReachesAuthenticatedGlobalSSE(t *testing.T) {
	const token = "viewer-send-owner-token-0123456789"
	tokenPath := filepath.Join(t.TempDir(), "agent-ops.token")
	if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	store := openViewerBridgeAdmissionStore(t)
	archive, err := viewer.NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog() error = %v", err)
	}
	hub := viewer.NewEventHub(16)
	hub.SetSessionPublicReplayReader(archive)
	deps := &Dependencies{
		eventRelay:          &idleAwareEventListener{archive: archive, hub: hub},
		canonicalEventStore: store,
		eventHub:            hub,
	}
	cfg := &config.Config{LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true, AuthTokenFile: tokenPath, UserID: "ren"}}
	repository := sessionpersistence.NewJSONSessionRepository(t.TempDir())
	if err := repository.WarmCanonicalIndex(context.Background()); err != nil {
		t.Fatalf("warm canonical Session index: %v", err)
	}
	address, err := domainconversation.NewChannelAddress("viewer", "cmd:ren")
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Now().UTC()
	session, err := domainsession.NewCanonicalSessionWithOwner(modulecore.NewSessionID(), "ren", createdAt.Format("2006-01-02"), address, createdAt)
	if err != nil {
		t.Fatalf("construct current CMD Session: %v", err)
	}
	if err := repository.Save(context.Background(), session); err != nil {
		t.Fatalf("save current CMD Session: %v", err)
	}
	acceptedBinding, err := domainsession.NewCanonicalSessionPublicationBinding(session)
	if err != nil {
		t.Fatalf("capture current CMD Session binding: %v", err)
	}
	deps.sessionRepository = repository
	handler := buildViewerBridgeHandlers(cfg, deps, "", ttsEntryRuntime{}, repository).ViewerSendFromOrch(viewerBridgeErrorProcessor{})
	request := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{"message":"private recall","to":"mio"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
	request.Header.Set("X-RenCrow-Interaction-Profile", "cmd-chat")
	request.RemoteAddr = "127.0.0.1:12345"
	request.Host = "127.0.0.1:18790"
	attachViewerBridgeTestPresence(t, deps, request)
	acceptedResponse := httptest.NewRecorder()
	handler.ServeHTTP(acceptedResponse, request)
	if acceptedResponse.Code != http.StatusOK {
		t.Fatalf("legacy send status=%d body=%s, want accepted response", acceptedResponse.Code, acceptedResponse.Body.String())
	}
	var accepted struct {
		SessionID  string `json:"session_id"`
		RootTaskID string `json:"root_task_id"`
		TraceID    string `json:"trace_id"`
	}
	if err := json.NewDecoder(acceptedResponse.Body).Decode(&accepted); err != nil {
		t.Fatalf("decode accepted legacy response: %v", err)
	}
	if accepted.SessionID != string(acceptedBinding.SessionID()) || accepted.RootTaskID == "" || accepted.TraceID == "" {
		t.Fatalf("accepted response=%+v, want current Session and task/trace identities", accepted)
	}
	var terminal modulecore.EventEnvelope
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		items, listErr := store.ListByComponent(context.Background(), "orchestrator", 100)
		if listErr != nil {
			t.Fatalf("read canonical terminal error: %v", listErr)
		}
		for _, item := range items {
			if item.EventType == "viewer.error" && item.Payload["trace_id"] == accepted.TraceID {
				terminal = item
				break
			}
		}
		if terminal.EventType != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if terminal.EventType == "" {
		t.Fatal("timed out waiting for bridge-published viewer.error")
	}
	if terminal.Payload["task_id"] != accepted.RootTaskID || terminal.Payload["trace_id"] != accepted.TraceID ||
		terminal.Payload["session_id"] != accepted.SessionID || terminal.Payload["publication_incarnation"] != acceptedBinding.PublicationIncarnation() {
		t.Fatalf("terminal event identity does not match the accepted current binding: payload=%#v accepted=%+v binding=%+v", terminal.Payload, accepted, acceptedBinding)
	}

	ownerStream := routeTestOwnerRequest("/viewer/events?to=mio", token)
	ownerContext, ownerCancel := context.WithCancel(ownerStream.Context())
	defer ownerCancel()
	ownerResponse := &cancelRouteSSEWriter{ResponseRecorder: httptest.NewRecorder(), cancel: ownerCancel}
	viewerGlobalEventsHandler(cfg, deps).ServeHTTP(ownerResponse, ownerStream.WithContext(ownerContext))
	ownerBody := ownerResponse.Body.String()
	if !strings.Contains(ownerBody, "viewer processing failed") || !strings.Contains(ownerBody, accepted.RootTaskID) || !strings.Contains(ownerBody, accepted.TraceID) {
		t.Fatalf("authenticated CMD SSE omitted correlated current-binding error: %s", ownerBody)
	}

	otherToken := "viewer-send-other-owner-token-0123456789"
	otherTokenPath := filepath.Join(t.TempDir(), "other-owner.token")
	if err := os.WriteFile(otherTokenPath, []byte(otherToken), 0o600); err != nil {
		t.Fatal(err)
	}
	otherCfg := &config.Config{LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true, AuthTokenFile: otherTokenPath, UserID: "other"}}
	otherStream := routeTestOwnerRequest("/viewer/events?to=mio", otherToken)
	otherContext, otherCancel := context.WithTimeout(otherStream.Context(), 100*time.Millisecond)
	defer otherCancel()
	otherResponse := &cancelRouteSSEWriter{ResponseRecorder: httptest.NewRecorder(), cancel: otherCancel}
	viewerGlobalEventsHandler(otherCfg, deps).ServeHTTP(otherResponse, otherStream.WithContext(otherContext))
	if strings.Contains(otherResponse.Body.String(), "viewer processing failed") {
		t.Fatalf("different authenticated owner received CMD terminal error: %s", otherResponse.Body.String())
	}
}
