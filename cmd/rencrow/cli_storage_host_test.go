package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainsession "github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	advisorpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/advisor"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	dcipersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/dci"
	knowledgememorypersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/knowledgememory"
	sandboxpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/sandbox"
	skillgovernancepersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/skillgovernance"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/storagehost"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestStorageHostHelpAndUnknownSubcommand(t *testing.T) {
	globalHelp := captureStdout(t, cmdHelp)
	if !strings.Contains(globalHelp, "storage-host") {
		t.Fatalf("global help does not list storage-host: %s", globalHelp)
	}

	var out, errOut bytes.Buffer
	code := runStorageHostCommand([]string{"--help"}, storageHostCLIDeps{}, &out, &errOut)
	if code != 0 || !strings.Contains(out.String(), "Usage: rencrow storage-host") || !strings.Contains(out.String(), "  serve") {
		t.Fatalf("help code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}

	out.Reset()
	errOut.Reset()
	code = runStorageHostCommand([]string{"unknown"}, storageHostCLIDeps{}, &out, &errOut)
	if code != 2 || !strings.Contains(errOut.String(), "unknown storage-host command") {
		t.Fatalf("unknown command code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestParseStorageHostListenAddressRequiresFixedLoopbackIP(t *testing.T) {
	for _, listen := range []string{"127.0.0.1:18820", "[::1]:18820"} {
		got, err := parseStorageHostListenAddress(listen)
		if err != nil || got == "" {
			t.Errorf("parseStorageHostListenAddress(%q)=(%q,%v), want accepted loopback", listen, got, err)
		}
	}
	for _, listen := range []string{
		"0.0.0.0:18820",
		"192.168.1.20:18820",
		"localhost:18820",
		"127.0.0.1:0",
		"127.0.0.1:http",
		":18820",
		"http://127.0.0.1:18820",
	} {
		if got, err := parseStorageHostListenAddress(listen); err == nil {
			t.Errorf("parseStorageHostListenAddress(%q)=(%q,nil), want rejection", listen, got)
		}
	}
}

func TestRunStorageHostServeUsesRealEventStoreAndClosesResources(t *testing.T) {
	cfg, token := newStorageHostTestConfig(t, nextFixedLoopbackAddress(t))
	deps := realStorageHostServeDeps()
	var openedTaskPath string
	openTaskStore := deps.OpenTaskStore
	deps.OpenTaskStore = func(path string) (storageHostTaskStore, error) {
		openedTaskPath = path
		return openTaskStore(path)
	}
	listenCalled := make(chan struct{}, 1)
	realListen := deps.Listen
	deps.Listen = func(network, address string) (net.Listener, error) {
		if address != cfg.Storage.Host.Listen {
			t.Errorf("listener address=%q, want configured fixed address %q", address, cfg.Storage.Host.Listen)
		}
		listener, err := realListen(network, address)
		if err == nil {
			listenCalled <- struct{}{}
		}
		return listener, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	serveResult := make(chan error, 1)
	go func() { serveResult <- runStorageHostServe(ctx, cfg, deps) }()
	select {
	case <-listenCalled:
	case err := <-serveResult:
		t.Fatalf("storage host returned before listening: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("storage host did not bind its configured loopback listener")
	}
	if openedTaskPath != defaultTaskStorePath(cfg.WorkspaceDir) {
		t.Fatalf("Task store path=%q, want canonical path %q", openedTaskPath, defaultTaskStorePath(cfg.WorkspaceDir))
	}

	endpoint := "http://" + cfg.Storage.Host.Listen + storagehost.RPCPath
	contractRequest := storagehost.Request{
		Contract: storagehost.ContractVersion,
		OpID:     "contract-check",
		Group:    storagehost.GroupHost,
		Op:       "contract",
	}
	contractResponse, status := postStorageHostRPC(t, endpoint, token, contractRequest)
	if status != http.StatusOK || contractResponse.Error != nil {
		t.Fatalf("host.contract status=%d response=%+v", status, contractResponse)
	}
	var contract storagehost.ContractInfo
	if err := json.Unmarshal(contractResponse.Result, &contract); err != nil {
		t.Fatalf("decode host.contract: %v", err)
	}
	if contract.ContractVersion != storagehost.ContractVersion || contract.Generation < 1 {
		t.Fatalf("contract readiness fields=%+v", contract)
	}
	gotOperations := make(map[string]bool, len(contract.Operations))
	for _, operation := range contract.Operations {
		name := operation.Group + "." + operation.Op
		if _, duplicate := gotOperations[name]; duplicate {
			t.Fatalf("contract repeated operation %q", name)
		}
		gotOperations[name] = operation.Mutating
	}
	wantOperations := map[string]bool{
		"operation_memory.read_long_term": false, "operation_memory.write_long_term": true,
		"operation_memory.read_today": false, "operation_memory.append_today": true,
		"operation_memory.recent_daily_notes": false, "operation_memory.save_daily_note_for_date": true,
		"operation_memory.memory_context": false,
		"event.append":                    true, "event.append_sequenced": true, "event.get": false, "event.list_component": false,
		"session.save": true, "session.load": false, "session.exists": false, "session.delete": true, "session.load_or_create_canonical": true,
		"l1.save_message": true, "l1.save_search_cache": true, "l1.get_fresh_search_cache": false,
		"l1.get_similar_fresh_search_cache": false, "l1.invalidate_search_cache": true,
		"l1.search_knowledge_items_fts": false, "l1.search_wiki_page_index": false, "l1.append_event": true,
		"l1.recent_events": false, "l1.update_memory_state": true, "l1.promote_memory_to_namespace": true,
		"l1.recent_by_namespace": false, "l1.recent_by_state": false, "l1.recent_by_session": false,
		"l1.latest_conversation_thread_reference": false, "l1.save_recall_trace": true, "l1.recent_recall_traces": false,
		"turn.commit": true, "turn.receipt": false, "turn.outbox_claim": true, "turn.outbox_claim_next": true,
		"turn.complete": true, "turn.fail": true, "turn.projection": false, "turn.active_projection": false,
		"task.writer_generation": false, "task.tx_begin": false, "task.tx_command": false,
		"task.tx_prepare_commit": false, "task.tx_abort": false, "task.read_begin": false,
		"task.read_command": false, "task.read_end": false, "task.commit": true,
		"task.fence_reservation": false, "task.fence_acquire": true, "task.fence_release": true,
		"archive.save_thread_summary_with_receipt": true, "archive.get_thread_summary": false,
		"archive.get_session_history": false, "archive.search_by_domain": false,
		"archive.search_knowledge_archive_fts": false, "archive.archive_user_memory_with_receipt": true,
		"archive.find_user_memory_archive": false, "archive.find_archive_request_receipt": false,
		"tool_registry.register": true, "tool_registry.register_with_receipt": true,
		"tool_registry.list_for_platform": false, "tool_registry.get": false,
		"tool_registry.find_action_receipt": false,
		"user_memory.create_user_memory":    true, "user_memory.list_user_memories": false,
		"user_memory.find_user_memory_by_id": false, "user_memory.create_user_memory_candidate_with_request": true,
		"user_memory.update_user_memory_state": true, "user_memory.forget_user_memory": true,
		"user_memory.supersede_user_memory": true,
		"glossary.save":                     true, "glossary.find_by_term": false, "glossary.find_recent": false,
		"glossary.find_by_category": false, "glossary.delete": true, "glossary.save_candidate": true,
		"glossary.find_candidate_by_id": false, "glossary.lookup": false, "glossary.viewer_page": false,
		"movie_catalog.lookup": false, "movie_catalog.viewer_stats": false,
		"movie_catalog.viewer_movies": false, "movie_catalog.viewer_people": false,
		"movie_catalog.viewer_cards": false, "movie_catalog.viewer_movie": false,
		"movie_catalog.viewer_person": false, "movie_catalog.candidate_by_id": false,
		"movie_catalog.candidate_by_request": false, "movie_catalog.candidate_save": true,
		"hobby_graph.catalog_lookup": false, "hobby_graph.lyrics_lookup": false,
		"hobby_graph.viewer_stats": false, "hobby_graph.viewer_overview": false,
		"hobby_graph.candidate_by_id": false, "hobby_graph.candidate_by_request": false,
		"hobby_graph.candidate_save":                    true,
		"durable_store_workflow.find_by_dedupe_key":     false,
		"durable_store_workflow.find_by_action_id":      false,
		"durable_store_workflow.find_by_requirement_id": false,
		"durable_store_workflow.save_with_receipt":      true,
		"verification_report.save":                      true, "verification_report.list_recent": false,
		"verification_report.get_by_task_id": false, "verification_report.summary": false,
	}
	for operation, mutating := range closedOwnerStorageHostOperations() {
		wantOperations[operation] = mutating
	}
	if !reflect.DeepEqual(gotOperations, wantOperations) {
		t.Fatalf("advertised operations=%v, want %v", gotOperations, wantOperations)
	}
	for _, forbidden := range []string{"raw."} {
		for operation := range gotOperations {
			if strings.HasPrefix(operation, forbidden) {
				t.Fatalf("contract advertised unimplemented operation %q", operation)
			}
		}
	}

	unauthorized, status := postStorageHostRPC(t, endpoint, "wrong-token", contractRequest)
	if status != http.StatusUnauthorized || unauthorized.Error == nil || unauthorized.Error.Code != storagehost.ErrorCodeUnauthorized {
		t.Fatalf("wrong token status=%d response=%+v, want typed 401", status, unauthorized)
	}

	healthResp, err := http.Get("http://" + cfg.Storage.Host.Listen + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	_ = healthResp.Body.Close()
	if healthResp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /health status=%d, want no extra health route", healthResp.StatusCode)
	}

	client, err := storagehost.NewClient(storagehost.ClientConfig{
		Endpoint: "http://" + cfg.Storage.Host.Listen,
		Token:    token,
		Timeout:  3 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	clientCtx, clientCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer clientCancel()
	if err := client.Handshake(clientCtx); err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	if err := client.Call(clientCtx, storagehost.GroupOperationMemory, "write_long_term", map[string]string{"content": "remote operation memory"}, nil); err != nil {
		t.Fatalf("operation memory write_long_term: %v", err)
	}
	var operationMemoryRead struct {
		Content string `json:"content"`
	}
	if err := client.Call(clientCtx, storagehost.GroupOperationMemory, "read_long_term", struct{}{}, &operationMemoryRead); err != nil {
		t.Fatalf("operation memory read_long_term: %v", err)
	}
	if operationMemoryRead.Content != "remote operation memory" {
		t.Fatalf("operation memory read=%q, want remote content", operationMemoryRead.Content)
	}
	if persisted, err := os.ReadFile(filepath.Join(cfg.Storage.Memory.OperationMemoryDir, "MEMORY.md")); err != nil || string(persisted) != "remote operation memory" {
		t.Fatalf("storage-host operation memory file=(%q,%v), want owner-persisted content", persisted, err)
	}
	archiveClient := storagehost.NewArchiveStoreClient(client)
	archiveHistory, err := archiveClient.GetSessionHistory(clientCtx, string(modulecore.NewSessionID()), 10)
	if err != nil || len(archiveHistory) != 0 {
		t.Fatalf("ArchiveStoreClient.GetSessionHistory=%+v err=%v, want empty owner result", archiveHistory, err)
	}
	eventClient := storagehost.NewEventStoreClient(client)
	event := modulecore.NewRootEventEnvelope("core.storage_host_test", "test.persisted", time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC), map[string]any{"value": "persisted"})
	persisted, err := eventClient.AppendSequenced(clientCtx, event)
	if err != nil {
		t.Fatalf("AppendSequenced: %v", err)
	}
	readBack, found, err := eventClient.GetByID(clientCtx, event.EventID)
	if err != nil || !found {
		t.Fatalf("GetByID: found=%v err=%v", found, err)
	}
	if readBack.EventID != event.EventID || readBack.EventSeq != persisted.EventSeq || readBack.Payload["value"] != "persisted" {
		t.Fatalf("read-back=%+v persisted=%+v", readBack, persisted)
	}

	address, err := domconv.NewChannelAddress("line", "storage-host-test-conversation")
	if err != nil {
		t.Fatalf("NewChannelAddress: %v", err)
	}
	createdAt := time.Date(2026, 10, 3, 12, 45, 0, 0, time.UTC)
	sessionValue, err := domainsession.NewCanonicalSession(modulecore.NewSessionID(), "2026-10-03", address, createdAt)
	if err != nil {
		t.Fatalf("NewCanonicalSession: %v", err)
	}
	sessionValue.SetMemory("storage-host-test", "persisted")
	sessionClient := storagehost.NewSessionRepositoryClient(client)
	if err := sessionClient.Save(clientCtx, sessionValue); err != nil {
		t.Fatalf("session Save: %v", err)
	}
	loadedSession, err := sessionClient.Load(clientCtx, sessionValue.ID())
	if err != nil || loadedSession.ID() != sessionValue.ID() {
		t.Fatalf("session Load: loaded=%v err=%v", loadedSession, err)
	}
	if loaded, found := loadedSession.GetMemory("storage-host-test"); !found || loaded != "persisted" {
		t.Fatalf("session memory round-trip=(%v,%v), want persisted", loaded, found)
	}

	l1Client := storagehost.NewL1StoreClient(client)
	l1SessionID := modulecore.NewSessionID()
	threadID := modulecore.NewThreadID()
	l1Message := domconv.Message{
		Speaker:   domconv.SpeakerUser,
		Msg:       "storage-host L1 round trip",
		Timestamp: createdAt,
		Meta:      map[string]interface{}{"origin": "cli-test"},
	}
	if err := l1Client.SaveMessage(clientCtx, string(l1SessionID), threadID, 1, modulecore.ThreadKindUserConversation,
		"conv:"+string(threadID), l1Message, l1sqlite.MemoryStateObserved); err != nil {
		t.Fatalf("L1 SaveMessage: %v", err)
	}
	l1ReadBack, err := l1Client.RecentBySession(clientCtx, string(l1SessionID), 10)
	if err != nil || len(l1ReadBack) != 1 || l1ReadBack[0].Message != l1Message.Msg || l1ReadBack[0].ThreadID != threadID {
		t.Fatalf("L1 RecentBySession=%+v err=%v, want saved message", l1ReadBack, err)
	}

	turnClient := storagehost.NewTurnStoreClient(client)
	turnRequest := domconv.ConversationTurnRequest{
		TurnID:         modulecore.NewTurnID(),
		TraceID:        modulecore.NewTraceID(),
		RootTaskID:     modulecore.NewTaskID(),
		UserMessageID:  modulecore.NewMessageID(),
		AgentMessageID: modulecore.NewMessageID(),
		SessionID:      string(modulecore.NewSessionID()),
		OwnerID:        "storage-host-cli-test",
		Domain:         "general",
		UserMessage:    "user turn through storage host",
		AgentMessage:   "agent turn through storage host",
		AgentSpeaker:   domconv.SpeakerMio,
		Targets:        []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetRedisProjection},
	}
	committedTurn, err := turnClient.CommitConversationTurn(clientCtx, turnRequest)
	if err != nil {
		t.Fatalf("Turn CommitConversationTurn: %v", err)
	}
	turnReceipt, err := turnClient.GetConversationTurnReceipt(clientCtx, string(turnRequest.TurnID))
	if err != nil || turnReceipt.TurnID != committedTurn.TurnID || turnReceipt.ThreadID != committedTurn.ThreadID {
		t.Fatalf("Turn receipt=%+v err=%v, want committed turn %+v", turnReceipt, err, committedTurn)
	}

	taskClient, err := storagehost.NewTaskStoreClient(client)
	if err != nil {
		t.Fatalf("NewTaskStoreClient: %v", err)
	}
	writerGeneration, err := taskClient.WriterGeneration()
	if err != nil || writerGeneration == 0 {
		t.Fatalf("Task WriterGeneration=(%d,%v), want positive generation", writerGeneration, err)
	}
	taskValue := domaintask.Task{
		TaskID:  modulecore.NewTaskID(),
		Title:   "storage-host Task round trip",
		OwnerID: "storage-host-cli-test",
	}
	taskValue.ApplyDefaults(createdAt)
	if err := taskClient.SaveTask(clientCtx, taskValue); err != nil {
		t.Fatalf("Task SaveTask: %v", err)
	}
	runValue := domaintask.Run{
		WriterGeneration: writerGeneration,
		RunID:            modulecore.NewRunID(),
		TaskID:           taskValue.TaskID,
		StartReason:      domaintask.RunStartReasonFirst,
		Assignee:         "Shiro",
		Status:           domaintask.RunStatusRunning,
		StartedAt:        createdAt,
	}
	if err := taskClient.SaveRun(clientCtx, runValue); err != nil {
		t.Fatalf("Task SaveRun: %v", err)
	}
	taskReadBack, err := taskClient.GetTask(clientCtx, taskValue.TaskID)
	if err != nil || taskReadBack.TaskID != taskValue.TaskID || taskReadBack.Title != taskValue.Title {
		t.Fatalf("Task GetTask=%+v err=%v, want %+v", taskReadBack, err, taskValue)
	}
	runReadBack, err := taskClient.GetRun(clientCtx, runValue.RunID)
	if err != nil || runReadBack.RunID != runValue.RunID || runReadBack.WriterGeneration != writerGeneration {
		t.Fatalf("Task GetRun=%+v err=%v, want writer generation %d", runReadBack, err, writerGeneration)
	}
	sharedContext := domaintask.SharedRoleContext{
		TaskID:      taskValue.TaskID,
		UserIntent:  "persist Task context through authenticated storage RPC",
		CurrentPlan: "round trip through TaskStoreClient",
		UpdatedAt:   createdAt,
	}
	if err := taskClient.SaveContext(clientCtx, sharedContext); err != nil {
		t.Fatalf("Task SaveContext: %v", err)
	}
	contextReadBack, err := taskClient.GetContext(clientCtx, taskValue.TaskID)
	if err != nil || contextReadBack.UserIntent != sharedContext.UserIntent || contextReadBack.CurrentPlan != sharedContext.CurrentPlan {
		t.Fatalf("Task GetContext=%+v err=%v, want %+v", contextReadBack, err, sharedContext)
	}
	notification := domaintask.NewNotification(taskValue, createdAt)
	if err := taskClient.SaveNotification(clientCtx, notification); err != nil {
		t.Fatalf("Task SaveNotification: %v", err)
	}
	notifications, err := taskClient.ListNotifications(clientCtx, 10, false)
	if err != nil || len(notifications) != 1 || notifications[0].TaskID != taskValue.TaskID || notifications[0].Title != taskValue.Title {
		t.Fatalf("Task ListNotifications=%+v err=%v, want notification for task %s", notifications, err, taskValue.TaskID)
	}

	cancel()
	select {
	case err := <-serveResult:
		if err != nil {
			t.Fatalf("storage host shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("storage host did not stop after context cancellation")
	}

	journalDir := filepath.Join(cfg.Storage.Memory.OperationMemoryDir, "storage-host")
	reopened, err := storagehost.NewHandler(storagehost.HandlerConfig{Token: token, JournalDir: journalDir})
	if err != nil {
		t.Fatalf("journal writer lock was not released on shutdown: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened handler: %v", err)
	}
	reopenedTaskStore, err := taskpersistence.NewJSONLStore(defaultTaskStorePath(cfg.WorkspaceDir))
	if err != nil {
		t.Fatalf("Task .writer.lock was not released on shutdown: %v", err)
	}
	reopenedGeneration, err := reopenedTaskStore.WriterGeneration()
	if err != nil || reopenedGeneration <= writerGeneration {
		t.Fatalf("reopened Task writer generation=(%d,%v), want greater than served generation %d", reopenedGeneration, err, writerGeneration)
	}
	if err := reopenedTaskStore.Close(); err != nil {
		t.Fatalf("close reopened Task store: %v", err)
	}
}

func TestRunStorageHostServeClosedOwnersAuthenticatedRoundTrip(t *testing.T) {
	listen := nextFixedLoopbackAddress(t)
	cfg, token := newStorageHostTestConfig(t, listen)
	seedClosedOwnerStorageHostDatabases(t, cfg)

	deps := realStorageHostServeDeps()
	listenCalled := make(chan struct{}, 1)
	listenOwner := deps.Listen
	deps.Listen = func(network, address string) (net.Listener, error) {
		listener, err := listenOwner(network, address)
		if err == nil {
			listenCalled <- struct{}{}
		}
		return listener, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	serveResult := make(chan error, 1)
	go func() { serveResult <- runStorageHostServe(ctx, cfg, deps) }()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		cancel()
		if err := <-serveResult; err != nil {
			t.Errorf("storage host shutdown: %v", err)
		}
		stopped = true
	}
	t.Cleanup(stop)
	select {
	case <-listenCalled:
	case err := <-serveResult:
		stopped = true
		t.Fatalf("storage host returned before listening: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("storage host did not bind its configured loopback listener")
	}

	client, err := storagehost.NewClient(storagehost.ClientConfig{
		Endpoint: "http://" + listen,
		Token:    token,
		Timeout:  3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	clientCtx, clientCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer clientCancel()
	if err := client.Handshake(clientCtx); err != nil {
		t.Fatalf("storage host handshake: %v", err)
	}

	var contract storagehost.ContractInfo
	if err := client.Call(clientCtx, storagehost.GroupHost, "contract", struct{}{}, &contract); err != nil {
		t.Fatalf("storage host contract: %v", err)
	}
	wantOperations := closedOwnerStorageHostOperations()
	wantedGroups := map[string]bool{
		storagehost.GroupAdvisor: true, storagehost.GroupSandbox: true, storagehost.GroupDCI: true,
		storagehost.GroupSkillGovernance: true, storagehost.GroupKnowledgeMemory: true,
	}
	gotOperations := make(map[string]bool)
	for _, operation := range contract.Operations {
		if wantedGroups[operation.Group] {
			gotOperations[operation.Group+"."+operation.Op] = operation.Mutating
		}
	}
	if !reflect.DeepEqual(gotOperations, wantOperations) {
		t.Errorf("closed owner operation manifest=%v, want exact manifest %v", gotOperations, wantOperations)
	}

	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	dciActionID := modulecore.NewActionID()
	dciTrace := map[string]any{
		"trace_id": modulecore.NewTraceID(), "action_id": dciActionID,
		"started_at": now, "ended_at": now.Add(time.Second),
		"actor_attribution": "authenticated", "actor_kind": "agent", "actor_id": "shiro",
		"mode": "dci", "user_query": "storage host closed owner", "corpus_scope": []string{"docs/"},
		"status": "completed",
	}
	roundTrips := []struct {
		name          string
		group         string
		readOperation string
		readPayload   any
		writeOp       string
		writePayload  any
	}{
		{
			name: "advisor", group: storagehost.GroupAdvisor,
			readOperation: "list_advice_runs", readPayload: map[string]any{"limit": 10},
			writeOp: "save_advice_run", writePayload: map[string]any{"item": map[string]any{
				"run_id": "storage-host-cli-advisor", "requested_by_agent": "shiro", "advisor_id": "codex",
				"status": "completed", "started_at": now, "finished_at": now.Add(time.Second), "latency_millis": 1,
			}},
		},
		{
			name: "sandbox", group: storagehost.GroupSandbox,
			readOperation: "list_sandboxes", readPayload: map[string]any{"limit": 10},
			writeOp: "save_sandbox", writePayload: map[string]any{"item": map[string]any{
				"sandbox_id": "storage-host-cli-sandbox", "type": "worktree", "path": "workspace/storage-host-cli-sandbox",
				"status": "active", "created_at": now,
			}},
		},
		{
			name: "dci", group: storagehost.GroupDCI,
			readOperation: "list_recent", readPayload: map[string]any{"limit": 10},
			writeOp: "save_search_result", writePayload: map[string]any{
				"idempotency_key": "storage-host-cli-dci",
				"result": map[string]any{
					"trace": dciTrace,
					"pack":  map[string]any{"action_id": dciActionID, "query": "storage host closed owner", "corpus_scope": []string{"docs/"}, "confidence": 0.75},
				},
			},
		},
		{
			name: "skill governance", group: storagehost.GroupSkillGovernance,
			readOperation: "list_skill_manifests", readPayload: map[string]any{"limit": 10},
			writeOp: "save_skill_manifest", writePayload: map[string]any{"item": map[string]any{
				"skill_id": "storage-host-cli-skill", "name": "Storage Host CLI", "scope": "core",
				"version": "1.0.0", "path": "skills/core/storage-host-cli", "enabled": true, "updated_at": now,
			}},
		},
		{
			name: "knowledge memory", group: storagehost.GroupKnowledgeMemory,
			readOperation: "list_news_knowledge", readPayload: map[string]any{"limit": 10},
			writeOp: "save_news_knowledge", writePayload: map[string]any{"item": map[string]any{
				"item_id": "storage-host-cli-knowledge", "user_id": "storage-host-cli-user",
				"source": "storage-host-cli-test", "topic": "Private Storage Host Evidence",
				"summary": "private storage host round trip", "status": "reviewed", "visibility": "private", "created_at": now,
			}},
		},
	}
	for _, roundTrip := range roundTrips {
		t.Run(roundTrip.name, func(t *testing.T) {
			var before json.RawMessage
			if err := client.Call(clientCtx, roundTrip.group, roundTrip.readOperation, roundTrip.readPayload, &before); err != nil {
				t.Errorf("authenticated read %s.%s: %v", roundTrip.group, roundTrip.readOperation, err)
			}
			if err := client.Call(clientCtx, roundTrip.group, roundTrip.writeOp, roundTrip.writePayload, nil); err != nil {
				t.Errorf("authenticated mutation %s.%s: %v", roundTrip.group, roundTrip.writeOp, err)
			}
			var after json.RawMessage
			if err := client.Call(clientCtx, roundTrip.group, roundTrip.readOperation, roundTrip.readPayload, &after); err != nil {
				t.Errorf("authenticated read after mutation %s.%s: %v", roundTrip.group, roundTrip.readOperation, err)
			}
		})
	}

	badUserScope := storagehost.KnowledgeMemoryExecutionScope{
		RequestID: "storage-host-cli-public-cannot-read-user", ActorKind: domaintool.ActorKindAgent,
		ActorID: "shiro", AuthenticatedUserID: "storage-host-cli-user",
		AllowedDataScopes:    []string{domaintool.DataScopePublic},
		AuthenticationSource: domaintool.AuthenticationSourceAgentOrchestrator,
	}
	var rejectedSearch json.RawMessage
	err = client.Call(clientCtx, storagehost.GroupKnowledgeMemory, "search_user", map[string]any{
		"scope": badUserScope, "query": "Private Storage Host", "record_type": "news_knowledge", "limit": 10,
	}, &rejectedSearch)
	if storageHostRPCErrorCode(err) != storagehost.ErrorCodeUnauthorized {
		t.Errorf("user-scoped search without user data scope error=%v, want %s", err, storagehost.ErrorCodeUnauthorized)
	}
	goodUserScope := badUserScope
	goodUserScope.RequestID = "storage-host-cli-user-read"
	goodUserScope.AllowedDataScopes = []string{domaintool.DataScopeUser}
	var privateSearch struct {
		Items []struct {
			RecordID string `json:"record_id"`
			UserID   string `json:"user_id"`
			Scope    string `json:"scope"`
		} `json:"items"`
	}
	err = client.Call(clientCtx, storagehost.GroupKnowledgeMemory, "search_user", map[string]any{
		"scope": goodUserScope, "query": "Private Storage Host", "record_type": "news_knowledge", "limit": 10,
	}, &privateSearch)
	if err != nil || len(privateSearch.Items) != 1 || privateSearch.Items[0].RecordID != "storage-host-cli-knowledge" ||
		privateSearch.Items[0].UserID != goodUserScope.AuthenticatedUserID || privateSearch.Items[0].Scope != "user" {
		t.Errorf("authenticated user search result=%+v err=%v", privateSearch, err)
	}

	stop()
	assertClosedOwnerStorageHostPersistence(t, cfg)
}

func TestRunStorageHostServeClosesClosedOwnersWhenLaterOwnerOpenFails(t *testing.T) {
	cfg, _ := newStorageHostTestConfig(t, nextFixedLoopbackAddress(t))
	deps := realStorageHostServeDeps()
	advisorCloser := &trackedStorageHostCloser{}
	sandboxCloser := &trackedStorageHostCloser{}
	deps.OpenAdvisorStore = func(string) (storageHostAdvisorStore, error) {
		return &trackedAdvisorStorageHostStore{trackedStorageHostCloser: advisorCloser}, nil
	}
	deps.OpenSandboxStore = func(string) (storageHostSandboxStore, error) {
		return &trackedSandboxStorageHostStore{trackedStorageHostCloser: sandboxCloser}, nil
	}
	deps.OpenDCIStore = func(string) (storageHostDCIStore, error) {
		return nil, errors.New("injected DCI owner open failure")
	}
	if err := runStorageHostServe(context.Background(), cfg, deps); err == nil || !strings.Contains(err.Error(), "open canonical DCI SQLite owner") {
		t.Fatalf("serve error=%v, want canonical DCI owner open failure", err)
	}
	if !advisorCloser.closed || !sandboxCloser.closed {
		t.Fatalf("closed owners after later open failure: advisor=%v sandbox=%v", advisorCloser.closed, sandboxCloser.closed)
	}
}

func TestRunStorageHostServeClosesAllClosedOwnersWhenRegistrationFails(t *testing.T) {
	cfg, _ := newStorageHostTestConfig(t, nextFixedLoopbackAddress(t))
	deps := realStorageHostServeDeps()
	advisorCloser := &trackedStorageHostCloser{}
	sandboxCloser := &trackedStorageHostCloser{}
	dciCloser := &trackedStorageHostCloser{}
	skillCloser := &trackedStorageHostCloser{}
	knowledgeMemoryCloser := &trackedStorageHostCloser{}
	deps.OpenAdvisorStore = func(string) (storageHostAdvisorStore, error) {
		return &trackedAdvisorStorageHostStore{trackedStorageHostCloser: advisorCloser}, nil
	}
	deps.OpenSandboxStore = func(string) (storageHostSandboxStore, error) {
		return &trackedSandboxStorageHostStore{trackedStorageHostCloser: sandboxCloser}, nil
	}
	deps.OpenDCIStore = func(string) (storageHostDCIStore, error) {
		return &trackedDCIStorageHostStore{trackedStorageHostCloser: dciCloser}, nil
	}
	deps.OpenSkillGovernanceStore = func(string) (storageHostSkillGovernanceStore, error) {
		return &trackedSkillGovernanceStorageHostStore{trackedStorageHostCloser: skillCloser}, nil
	}
	deps.OpenKnowledgeMemoryStore = func(string) (storageHostKnowledgeMemoryStore, error) {
		return &trackedKnowledgeMemoryStorageHostStore{trackedStorageHostCloser: knowledgeMemoryCloser}, nil
	}
	registrationErr := errors.New("injected Advisor group registration failure")
	deps.RegisterAdvisorGroup = func(*storagehost.Handler, storagehost.AdvisorGroupOwner) error {
		return registrationErr
	}
	listenCalled := false
	deps.Listen = func(string, string) (net.Listener, error) {
		listenCalled = true
		return nil, nil
	}
	err := runStorageHostServe(context.Background(), cfg, deps)
	if !errors.Is(err, registrationErr) {
		t.Fatalf("serve error=%v, want registration failure", err)
	}
	if listenCalled {
		t.Fatal("listener started after owner registration failure")
	}
	if !advisorCloser.closed || !sandboxCloser.closed || !dciCloser.closed || !skillCloser.closed || !knowledgeMemoryCloser.closed {
		t.Fatalf("closed owners after registration failure: advisor=%v sandbox=%v dci=%v skill_governance=%v knowledge_memory=%v",
			advisorCloser.closed, sandboxCloser.closed, dciCloser.closed, skillCloser.closed, knowledgeMemoryCloser.closed)
	}
}

type trackedStorageHostCloser struct {
	closed bool
}

func (owner *trackedStorageHostCloser) Close() error {
	owner.closed = true
	return nil
}

type trackedAdvisorStorageHostStore struct {
	storagehost.AdvisorGroupOwner
	*trackedStorageHostCloser
}

type trackedSandboxStorageHostStore struct {
	storagehost.SandboxGroupOwner
	*trackedStorageHostCloser
}

type trackedDCIStorageHostStore struct {
	storagehost.DCIGroupOwner
	*trackedStorageHostCloser
}

type trackedSkillGovernanceStorageHostStore struct {
	storagehost.SkillGovernanceGroupOwner
	*trackedStorageHostCloser
}

type trackedKnowledgeMemoryStorageHostStore struct {
	storagehost.KnowledgeMemoryGroupOwner
	*trackedStorageHostCloser
}

func closedOwnerStorageHostOperations() map[string]bool {
	return map[string]bool{
		"advisor.save_advice_run": true, "advisor.list_advice_runs": false, "advisor.find_advice_run": false,
		"advisor.save_advisor_adoption": true, "advisor.list_advisor_adoptions": false, "advisor.find_advisor_adoption": false,
		"advisor.save_advisor_score_snapshot": true, "advisor.list_advisor_score_snapshots": false,
		"advisor.save_agent_policy_decision": true, "advisor.list_agent_policy_decisions": false,
		"sandbox.save_sandbox": true, "sandbox.list_sandboxes": false, "sandbox.find_sandbox": false,
		"sandbox.save_sandbox_artifact": true, "sandbox.list_sandbox_artifacts": false, "sandbox.find_sandbox_artifact": false,
		"sandbox.save_promotion_request": true, "sandbox.list_promotion_requests": false, "sandbox.find_promotion_request": false,
		"sandbox.save_promotion_gate_log": true, "sandbox.list_promotion_gate_logs": false, "sandbox.find_promotion_gate_log": false,
		"dci.save_search_trace": true, "dci.save_search_result": true,
		"dci.find_search_trace_by_action_id": false, "dci.find_search_result_by_action_id": false,
		"dci.find_search_trace_by_idempotency_key": false, "dci.find_search_result_by_idempotency_key": false,
		"dci.list_recent":                      false,
		"skill_governance.save_skill_manifest": true, "skill_governance.list_skill_manifests": false,
		"skill_governance.save_skill_trigger_log": true, "skill_governance.list_skill_trigger_logs": false,
		"skill_governance.save_skill_change_log": true, "skill_governance.list_skill_change_logs": false,
		"skill_governance.save_contribution_gate_log": true, "skill_governance.list_contribution_gate_logs": false,
		"skill_governance.find_contribution_gate_by_id":   false,
		"skill_governance.save_external_pr_submit_record": true, "skill_governance.list_external_pr_submit_records": false,
		"skill_governance.save_coder_transcript_entry": true, "skill_governance.list_coder_transcript_entries": false,
		"knowledge_memory.save_personal_archive": true, "knowledge_memory.list_personal_archive": false,
		"knowledge_memory.save_creative_knowledge": true, "knowledge_memory.list_creative_knowledge": false,
		"knowledge_memory.save_news_knowledge": true, "knowledge_memory.list_news_knowledge": false,
		"knowledge_memory.save_daily_rule": true, "knowledge_memory.list_daily_rules": false,
		"knowledge_memory.save_temporal_marker": true, "knowledge_memory.list_temporal_markers": false,
		"knowledge_memory.save_dream_run": true, "knowledge_memory.list_dream_runs": false,
		"knowledge_memory.search_public": false, "knowledge_memory.search_user": false,
		"knowledge_memory.propose_creative_candidate": true,
	}
}

func seedClosedOwnerStorageHostDatabases(t *testing.T, cfg *config.Config) {
	t.Helper()
	seeders := []struct {
		name string
		open func() (interface{ Close() error }, error)
	}{
		{"advisor", func() (interface{ Close() error }, error) {
			return advisorpersistence.NewSQLiteStore(cfg.Storage.Databases.Advisor)
		}},
		{"sandbox", func() (interface{ Close() error }, error) {
			return sandboxpersistence.NewSQLiteStore(cfg.Storage.Databases.Sandbox)
		}},
		{"dci", func() (interface{ Close() error }, error) {
			return dcipersistence.NewSQLiteStore(cfg.Storage.Databases.DCI)
		}},
		{"skill governance", func() (interface{ Close() error }, error) {
			return skillgovernancepersistence.NewSQLiteStore(cfg.Storage.Databases.SkillGovernance)
		}},
		{"knowledge memory", func() (interface{ Close() error }, error) {
			return knowledgememorypersistence.NewSQLiteStore(cfg.Storage.Databases.KnowledgeMemory)
		}},
	}
	for _, seeder := range seeders {
		owner, err := seeder.open()
		if err != nil {
			t.Fatalf("create isolated %s database: %v", seeder.name, err)
		}
		if err := owner.Close(); err != nil {
			t.Fatalf("close isolated %s database: %v", seeder.name, err)
		}
	}
}

func assertClosedOwnerStorageHostPersistence(t *testing.T, cfg *config.Config) {
	t.Helper()
	ctx := context.Background()
	advisorStore, err := advisorpersistence.NewSQLiteStore(cfg.Storage.Databases.Advisor)
	if err != nil {
		t.Errorf("reopen Advisor owner: %v", err)
	} else {
		defer advisorStore.Close()
		items, err := advisorStore.ListAdviceRuns(ctx, 10)
		if err != nil || len(items) != 1 || items[0].RunID != "storage-host-cli-advisor" {
			t.Errorf("reopened Advisor items=%+v err=%v", items, err)
		}
	}
	sandboxStore, err := sandboxpersistence.NewSQLiteStore(cfg.Storage.Databases.Sandbox)
	if err != nil {
		t.Errorf("reopen Sandbox owner: %v", err)
	} else {
		defer sandboxStore.Close()
		items, err := sandboxStore.ListSandboxes(ctx, 10)
		if err != nil || len(items) != 1 || items[0].SandboxID != "storage-host-cli-sandbox" {
			t.Errorf("reopened Sandbox items=%+v err=%v", items, err)
		}
	}
	dciStore, err := dcipersistence.NewSQLiteStore(cfg.Storage.Databases.DCI)
	if err != nil {
		t.Errorf("reopen DCI owner: %v", err)
	} else {
		defer dciStore.Close()
		items, err := dciStore.ListRecent(10)
		if err != nil || len(items) != 1 || items[0].IdempotencyKey != "storage-host-cli-dci" {
			t.Errorf("reopened DCI items=%+v err=%v", items, err)
		}
	}
	skillStore, err := skillgovernancepersistence.NewSQLiteStore(cfg.Storage.Databases.SkillGovernance)
	if err != nil {
		t.Errorf("reopen Skill Governance owner: %v", err)
	} else {
		defer skillStore.Close()
		items, err := skillStore.ListSkillManifests(ctx, 10)
		if err != nil || len(items) != 1 || items[0].SkillID != "storage-host-cli-skill" {
			t.Errorf("reopened Skill Governance items=%+v err=%v", items, err)
		}
	}
	knowledgeMemoryStore, err := knowledgememorypersistence.NewSQLiteStore(cfg.Storage.Databases.KnowledgeMemory)
	if err != nil {
		t.Errorf("reopen Knowledge Memory owner: %v", err)
	} else {
		defer knowledgeMemoryStore.Close()
		items, err := knowledgeMemoryStore.ListNewsKnowledgeItems(ctx, 10)
		if err != nil || len(items) != 1 || items[0].ItemID != "storage-host-cli-knowledge" || items[0].UserID != "storage-host-cli-user" {
			t.Errorf("reopened Knowledge Memory items=%+v err=%v", items, err)
		}
	}
}

func storageHostRPCErrorCode(err error) string {
	var wire *storagehost.Error
	if errors.As(err, &wire) {
		return wire.Code
	}
	return ""
}

func TestRunStorageHostServeRejectsInvalidTaskStorePathBeforeOpeningOwners(t *testing.T) {
	cfg, _ := newStorageHostTestConfig(t, nextFixedLoopbackAddress(t))
	cfg.WorkspaceDir = "relative-workspace"
	deps := realStorageHostServeDeps()
	eventOpenCalls, taskOpenCalls, listenCalls := 0, 0, 0
	deps.OpenEventStore = func(string) (storageHostEventStore, error) {
		eventOpenCalls++
		return nil, nil
	}
	deps.OpenTaskStore = func(string) (storageHostTaskStore, error) {
		taskOpenCalls++
		return nil, nil
	}
	deps.Listen = func(network, address string) (net.Listener, error) {
		listenCalls++
		return net.Listen(network, address)
	}
	if err := runStorageHostServe(context.Background(), cfg, deps); err == nil {
		t.Fatal("runStorageHostServe accepted a non-absolute canonical Task store path")
	}
	if eventOpenCalls != 0 || taskOpenCalls != 0 || listenCalls != 0 {
		t.Fatalf("invalid Task store path reached resources: Event owner=%d Task opener=%d listener=%d", eventOpenCalls, taskOpenCalls, listenCalls)
	}
}

func TestRunStorageHostServeOperationMemoryRecoveryFailureIsFailClosed(t *testing.T) {
	cfg, token := newStorageHostTestConfig(t, nextFixedLoopbackAddress(t))
	deps := realStorageHostServeDeps()
	operationMemoryCalls, eventCalls, listenCalls := 0, 0, 0
	deps.OpenOperationMemoryStore = func(string) (storagehost.OperationMemoryGroupOwner, error) {
		operationMemoryCalls++
		return nil, errors.New("operation memory recovery failed")
	}
	deps.OpenEventStore = func(string) (storageHostEventStore, error) {
		eventCalls++
		return nil, nil
	}
	deps.Listen = func(string, string) (net.Listener, error) {
		listenCalls++
		return nil, nil
	}
	err := runStorageHostServe(context.Background(), cfg, deps)
	if err == nil || !strings.Contains(err.Error(), "open recoverable operation memory owner") {
		t.Fatalf("runStorageHostServe error=%v, want fail-closed operation-memory recovery error", err)
	}
	if operationMemoryCalls != 1 || eventCalls != 0 || listenCalls != 0 {
		t.Fatalf("startup calls operation-memory=%d Event=%d listen=%d, want 1/0/0", operationMemoryCalls, eventCalls, listenCalls)
	}
	reopenedHandler, err := storagehost.NewHandler(storagehost.HandlerConfig{
		Token:      token,
		JournalDir: filepath.Join(cfg.Storage.Memory.OperationMemoryDir, "storage-host"),
	})
	if err != nil {
		t.Fatalf("storage-host journal leaked after operation-memory recovery failure: %v", err)
	}
	if err := reopenedHandler.Close(); err != nil {
		t.Fatalf("close reopened handler: %v", err)
	}
}

func TestRunStorageHostServeTaskRegistrationFailureClosesOwnersBeforeListening(t *testing.T) {
	cfg, token := newStorageHostTestConfig(t, nextFixedLoopbackAddress(t))
	deps := realStorageHostServeDeps()
	eventCloseCalls, l1CloseCalls, taskCloseCalls := 0, 0, 0
	openEventStore := deps.OpenEventStore
	deps.OpenEventStore = func(path string) (storageHostEventStore, error) {
		owner, err := openEventStore(path)
		if owner == nil {
			return nil, err
		}
		return trackedStorageHostEventStore{storageHostEventStore: owner, closeCalls: &eventCloseCalls}, err
	}
	deps.OpenL1Store = func(path string) (storageHostL1Store, error) {
		owner, err := l1sqlite.NewL1SQLiteStore(path)
		if owner == nil {
			return nil, err
		}
		return trackedStorageHostL1Store{L1SQLiteStore: owner, closeCalls: &l1CloseCalls}, err
	}
	openTaskStore := deps.OpenTaskStore
	deps.OpenTaskStore = func(path string) (storageHostTaskStore, error) {
		owner, err := openTaskStore(path)
		if owner == nil {
			return nil, err
		}
		return trackedStorageHostTaskStore{storageHostTaskStore: owner, closeCalls: &taskCloseCalls}, err
	}
	deps.RegisterTaskGroup = func(*storagehost.Handler, storagehost.TaskGroupOwner) error {
		return context.Canceled
	}
	listenCalls := 0
	deps.Listen = func(network, address string) (net.Listener, error) {
		listenCalls++
		return net.Listen(network, address)
	}
	deps.Serve = func(context.Context, net.Listener, http.Handler) error { return nil }

	if err := runStorageHostServe(context.Background(), cfg, deps); err == nil || !strings.Contains(err.Error(), "register canonical Task") {
		t.Fatalf("runStorageHostServe error=%v, want Task registration failure", err)
	}
	if listenCalls != 0 {
		t.Fatalf("listener calls=%d, want zero after Task registration failure", listenCalls)
	}
	if eventCloseCalls != 1 || l1CloseCalls != 1 || taskCloseCalls != 1 {
		t.Fatalf("closed owners Event=%d L1=%d Task=%d, want each opened owner closed once", eventCloseCalls, l1CloseCalls, taskCloseCalls)
	}

	reopenedTaskStore, err := taskpersistence.NewJSONLStore(defaultTaskStorePath(cfg.WorkspaceDir))
	if err != nil {
		t.Fatalf("Task .writer.lock leaked after registration failure: %v", err)
	}
	if err := reopenedTaskStore.Close(); err != nil {
		t.Fatalf("close reopened Task store: %v", err)
	}
	reopenedHandler, err := storagehost.NewHandler(storagehost.HandlerConfig{
		Token:      token,
		JournalDir: filepath.Join(cfg.Storage.Memory.OperationMemoryDir, "storage-host"),
	})
	if err != nil {
		t.Fatalf("storage-host journal lock leaked after registration failure: %v", err)
	}
	if err := reopenedHandler.Close(); err != nil {
		t.Fatalf("close reopened handler: %v", err)
	}
}

func TestRunStorageHostServeArchiveRegistrationFailureClosesOwnerBeforeListening(t *testing.T) {
	cfg, _ := newStorageHostTestConfig(t, nextFixedLoopbackAddress(t))
	deps := realStorageHostServeDeps()
	archiveCloseCalls := 0
	openArchive := deps.OpenArchiveStore
	deps.OpenArchiveStore = func(path string) (storageHostArchiveStore, error) {
		owner, err := openArchive(path)
		if owner == nil {
			return nil, err
		}
		return trackedStorageHostArchiveStore{storageHostArchiveStore: owner, closeCalls: &archiveCloseCalls}, err
	}
	deps.RegisterArchiveGroup = func(*storagehost.Handler, storagehost.ArchiveGroupOwner) error {
		return context.Canceled
	}
	listenCalls := 0
	deps.Listen = func(network, address string) (net.Listener, error) {
		listenCalls++
		return net.Listen(network, address)
	}
	deps.Serve = func(context.Context, net.Listener, http.Handler) error { return nil }

	err := runStorageHostServe(context.Background(), cfg, deps)
	if err == nil || !strings.Contains(err.Error(), "register canonical Conversation Archive") {
		t.Fatalf("runStorageHostServe error=%v, want archive registration failure", err)
	}
	if archiveCloseCalls != 1 || listenCalls != 0 {
		t.Fatalf("archive closes/listeners=%d/%d, want 1/0", archiveCloseCalls, listenCalls)
	}
}

type trackedStorageHostArchiveStore struct {
	storageHostArchiveStore
	closeCalls *int
}

func (s trackedStorageHostArchiveStore) Close() error {
	(*s.closeCalls)++
	return s.storageHostArchiveStore.Close()
}

type trackedStorageHostEventStore struct {
	storageHostEventStore
	closeCalls *int
}

func (s trackedStorageHostEventStore) Close() error {
	(*s.closeCalls)++
	return s.storageHostEventStore.Close()
}

type trackedStorageHostL1Store struct {
	*l1sqlite.L1SQLiteStore
	closeCalls *int
}

func (s trackedStorageHostL1Store) Close() error {
	(*s.closeCalls)++
	return s.L1SQLiteStore.Close()
}

type trackedStorageHostTaskStore struct {
	storageHostTaskStore
	closeCalls *int
}

func (s trackedStorageHostTaskStore) Close() error {
	(*s.closeCalls)++
	return s.storageHostTaskStore.Close()
}

func TestRunStorageHostServeOwnerOpenFailurePrecedesListening(t *testing.T) {
	listenAddress := nextFixedLoopbackAddress(t)
	cfg, token := newStorageHostTestConfig(t, listenAddress)
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	cfg.Storage.Databases.EventStore = filepath.Join(blocker, "events.db")
	deps := realStorageHostServeDeps()
	listenCalls := 0
	deps.Listen = func(network, address string) (net.Listener, error) {
		listenCalls++
		return net.Listen(network, address)
	}
	if err := runStorageHostServe(context.Background(), cfg, deps); err == nil {
		t.Fatal("runStorageHostServe succeeded with an unopenable Event owner")
	}
	if listenCalls != 0 {
		t.Fatalf("listener calls=%d, want zero after owner open failure", listenCalls)
	}

	reopened, err := storagehost.NewHandler(storagehost.HandlerConfig{
		Token:      token,
		JournalDir: filepath.Join(cfg.Storage.Memory.OperationMemoryDir, "storage-host"),
	})
	if err != nil {
		t.Fatalf("journal writer lock leaked after owner open failure: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened handler: %v", err)
	}
}

func TestRunStorageHostServeRejectsRemoteModeWithoutFallback(t *testing.T) {
	cfg, _ := newStorageHostTestConfig(t, "127.0.0.1:18820")
	cfg.Storage.Host.Mode = config.StorageHostModeRemote
	deps := realStorageHostServeDeps()
	readCalls, ownerCalls, listenCalls := 0, 0, 0
	deps.ReadToken = func(string) (string, error) { readCalls++; return "token", nil }
	deps.OpenEventStore = func(string) (storageHostEventStore, error) { ownerCalls++; return nil, nil }
	deps.Listen = func(string, string) (net.Listener, error) { listenCalls++; return nil, nil }
	if err := runStorageHostServe(context.Background(), cfg, deps); err == nil {
		t.Fatal("remote-mode config unexpectedly started a local storage host")
	}
	if readCalls != 0 || ownerCalls != 0 || listenCalls != 0 {
		t.Fatalf("remote config reached local resources: token=%d owner=%d listener=%d", readCalls, ownerCalls, listenCalls)
	}
}

func TestRunStorageHostServeRejectsInvalidListenBeforeOpeningOwners(t *testing.T) {
	for _, listen := range []string{"0.0.0.0:18820", "127.0.0.1:0"} {
		t.Run(listen, func(t *testing.T) {
			cfg, _ := newStorageHostTestConfig(t, listen)
			deps := realStorageHostServeDeps()
			ownerCalls, listenCalls := 0, 0
			deps.OpenEventStore = func(string) (storageHostEventStore, error) { ownerCalls++; return nil, nil }
			deps.Listen = func(string, string) (net.Listener, error) { listenCalls++; return nil, nil }
			if err := runStorageHostServe(context.Background(), cfg, deps); err == nil {
				t.Fatal("invalid listen address unexpectedly accepted")
			}
			if ownerCalls != 0 || listenCalls != 0 {
				t.Fatalf("invalid listen reached resources: owner=%d listener=%d", ownerCalls, listenCalls)
			}
		})
	}
}

func newStorageHostTestConfig(t *testing.T, listen string) (*config.Config, string) {
	t.Helper()
	root := t.TempDir()
	token := "storage-host-test-token"
	tokenPath := filepath.Join(root, "storage-host.token")
	if err := os.WriteFile(tokenPath, []byte(token+"\n"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	if err := os.Chmod(tokenPath, 0o600); err != nil {
		t.Fatalf("chmod token: %v", err)
	}
	operationMemoryDir := filepath.Join(root, "operation-memory")
	cfg := &config.Config{
		WorkspaceDir: root,
		Session:      config.SessionConfig{StorageDir: filepath.Join(root, "sessions")},
		Storage: config.StorageConfig{
			Host: config.StorageHostConfig{
				Mode:      config.StorageHostModeLocal,
				Listen:    listen,
				TokenFile: tokenPath,
			},
			Databases: config.DatabasePathsConfig{
				EventStore:           filepath.Join(root, "event-store.db"),
				ConversationL1:       filepath.Join(root, "conversation-l1.db"),
				ConversationArchive:  filepath.Join(root, "conversation-archive.db"),
				ToolRegistry:         filepath.Join(root, "tool-registry.db"),
				Glossary:             filepath.Join(root, "glossary.db"),
				Advisor:              filepath.Join(root, "advisor.db"),
				Sandbox:              filepath.Join(root, "sandbox.db"),
				DCI:                  filepath.Join(root, "dci.db"),
				SkillGovernance:      filepath.Join(root, "skill-governance.db"),
				MovieCatalog:         filepath.Join(root, "movie-catalog.db"),
				HobbyGraph:           filepath.Join(root, "hobby-graph.db"),
				DurableStoreWorkflow: filepath.Join(root, "durable-store-workflow.db"),
				KnowledgeMemory:      filepath.Join(root, "knowledge-memory.db"),
			},
			Memory: config.MemoryStorageConfig{
				OperationMemoryDir: operationMemoryDir,
			},
		},
		Verification: config.VerificationConfig{ReportPath: filepath.Join(root, "verification-report.jsonl")},
	}
	seedStorageHostCatalogDatabases(t, cfg)
	return cfg, token
}

func seedStorageHostCatalogDatabases(t *testing.T, cfg *config.Config) {
	t.Helper()
	for _, item := range []struct {
		path string
		sql  string
	}{
		{cfg.Storage.Databases.MovieCatalog, `
CREATE TABLE movies(movie_id TEXT PRIMARY KEY,title TEXT NOT NULL,title_lookup_key TEXT NOT NULL DEFAULT '',url TEXT NOT NULL,synopsis TEXT,fetched_at TEXT DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE people(person_id TEXT PRIMARY KEY,name TEXT NOT NULL,name_lookup_key TEXT NOT NULL DEFAULT '',url TEXT NOT NULL,profile_json TEXT,biography TEXT,fetched_at TEXT DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE movie_people(movie_id TEXT NOT NULL,person_id TEXT NOT NULL,role TEXT NOT NULL,source TEXT NOT NULL,movie_title TEXT,person_name TEXT,movie_url TEXT,person_url TEXT,PRIMARY KEY(movie_id,person_id,role,source));`},
		{cfg.Storage.Databases.HobbyGraph, `
CREATE TABLE hobby_items(item_id TEXT PRIMARY KEY,category TEXT,item_type TEXT,title TEXT,normalized_title TEXT,subtitle TEXT,canonical_source TEXT,canonical_url TEXT,metadata_json TEXT,updated_at TEXT);
CREATE TABLE hobby_relations(relation_id TEXT PRIMARY KEY,from_item_id TEXT,to_item_id TEXT,relation_type TEXT,source TEXT,evidence_url TEXT,created_at TEXT);
CREATE TABLE hobby_music_lyrics(lyrics_id TEXT PRIMARY KEY,song_item_id TEXT,language TEXT,source TEXT,source_record_id TEXT,canonical_url TEXT,rights_status TEXT,license_reference TEXT,storage_mode TEXT,content_sha256 TEXT,fetched_at TEXT,updated_at TEXT,lyrics_text TEXT);
CREATE TABLE hobby_music_syntax_features(song_item_id TEXT,language TEXT,analyzer TEXT);`},
	} {
		db, err := sql.Open("sqlite", item.path)
		if err != nil {
			t.Fatalf("open catalog test DB %q: %v", item.path, err)
		}
		if _, err := db.Exec(item.sql); err != nil {
			_ = db.Close()
			t.Fatalf("seed catalog test DB %q: %v", item.path, err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close catalog test DB %q: %v", item.path, err)
		}
	}
}

func nextFixedLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a loopback port: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release reserved loopback port: %v", err)
	}
	return address
}

func realStorageHostServeDeps() storageHostServeDeps {
	return defaultStorageHostServeDeps()
}

func postStorageHostRPC(t *testing.T, endpoint, token string, request storagehost.Request) (storagehost.Response, int) {
	t.Helper()
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal storage RPC request: %v", err)
	}
	httpRequest, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("create storage RPC request: %v", err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+token)
	httpRequest.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 3 * time.Second}
	httpResponse, err := client.Do(httpRequest)
	if err != nil {
		t.Fatalf("call storage RPC: %v", err)
	}
	defer httpResponse.Body.Close()
	var response storagehost.Response
	if err := json.NewDecoder(httpResponse.Body).Decode(&response); err != nil {
		t.Fatalf("decode storage RPC response: %v", err)
	}
	return response, httpResponse.StatusCode
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	previous := os.Stdout
	os.Stdout = writer
	fn()
	if err := writer.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	os.Stdout = previous
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close stdout reader: %v", err)
	}
	return string(output)
}
