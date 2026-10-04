package main

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/storagehost"
)

const (
	runtimeStorageOwnersChildEnv     = "RENCROW_TEST_REMOTE_STORAGE_OWNERS_CHILD"
	runtimeStorageOwnersEndpointEnv  = "RENCROW_TEST_REMOTE_STORAGE_OWNERS_ENDPOINT"
	runtimeStorageOwnersTokenFileEnv = "RENCROW_TEST_REMOTE_STORAGE_OWNERS_TOKEN_FILE"
	runtimeStorageOwnersWorkspaceEnv = "RENCROW_TEST_REMOTE_STORAGE_OWNERS_WORKSPACE"
	runtimeStorageOwnersRootEnv      = "RENCROW_TEST_REMOTE_STORAGE_OWNERS_ROOT"
)

func TestRuntimeStorageOwnersRemoteCoverageGateBeforeLocalOwnerOpeners(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.WriteFile(workspace, []byte("workspace sentinel"), 0600); err != nil {
		t.Fatalf("create workspace sentinel: %v", err)
	}
	tokenFile := filepath.Join(root, "storage-host.token")
	if err := os.WriteFile(tokenFile, []byte(runtimeStorageHostTestToken), 0600); err != nil {
		t.Fatalf("create storage host token: %v", err)
	}
	host := newRuntimeStorageHostContractHandler(t, runtimeStorageHostOwnerRequiredOperations())
	server := httptest.NewServer(host)
	t.Cleanup(server.Close)

	cmd := exec.Command(os.Args[0], "-test.run=^TestRuntimeStorageOwnersRemoteCoverageGateChild$")
	cmd.Env = append(os.Environ(),
		runtimeStorageOwnersChildEnv+"=1",
		runtimeStorageOwnersEndpointEnv+"="+server.URL,
		runtimeStorageOwnersTokenFileEnv+"="+tokenFile,
		runtimeStorageOwnersWorkspaceEnv+"="+workspace,
		runtimeStorageOwnersRootEnv+"="+root,
	)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("remote buildDependencies() exited successfully; want the uncovered-owner gate, output=%s", output)
	}
	if !strings.Contains(string(output), runtimeStorageHostCoverageErrorMessage) {
		t.Fatalf("startup error = %q, want stable coverage error %q", output, runtimeStorageHostCoverageErrorMessage)
	}
	if strings.Contains(string(output), "open canonical Task store") || strings.Contains(string(output), runtimeStorageHostTestToken) {
		t.Fatalf("startup output exposed a local opener failure or bearer token: %q", output)
	}
	for _, path := range []string{
		filepath.Join(root, "event", "events.db"),
		filepath.Join(root, "conversation", "l1.db"),
		filepath.Join(root, "conversation", "archive.db"),
		filepath.Join(root, "sessions"),
		filepath.Join(root, "operation-memory"),
		filepath.Join(root, "raw"),
	} {
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("local owner path %q stat error = %v, want not-exist", path, statErr)
		}
	}
}

func TestRuntimeStorageOwnersRequiredManifestIsExact(t *testing.T) {
	got := runtimeStorageHostOwnerRequiredOperations()
	want := []storagehost.OperationSpec{
		{Group: storagehost.GroupDurableStoreWorkflow, Op: "find_by_action_id", Mutating: false},
		{Group: storagehost.GroupDurableStoreWorkflow, Op: "find_by_dedupe_key", Mutating: false},
		{Group: storagehost.GroupDurableStoreWorkflow, Op: "find_by_requirement_id", Mutating: false},
		{Group: storagehost.GroupDurableStoreWorkflow, Op: "save_with_receipt", Mutating: true},
		{Group: storagehost.GroupArchive, Op: "archive_user_memory_with_receipt", Mutating: true},
		{Group: storagehost.GroupArchive, Op: "find_archive_request_receipt", Mutating: false},
		{Group: storagehost.GroupArchive, Op: "find_user_memory_archive", Mutating: false},
		{Group: storagehost.GroupArchive, Op: "get_session_history", Mutating: false},
		{Group: storagehost.GroupArchive, Op: "get_thread_summary", Mutating: false},
		{Group: storagehost.GroupArchive, Op: "save_thread_summary_with_receipt", Mutating: true},
		{Group: storagehost.GroupArchive, Op: "search_by_domain", Mutating: false},
		{Group: storagehost.GroupArchive, Op: "search_knowledge_archive_fts", Mutating: false},
		{Group: storagehost.GroupEvent, Op: "append", Mutating: true},
		{Group: storagehost.GroupEvent, Op: "append_sequenced", Mutating: true},
		{Group: storagehost.GroupEvent, Op: "get", Mutating: false},
		{Group: storagehost.GroupEvent, Op: "list_component", Mutating: false},
		{Group: storagehost.GroupGlossary, Op: "delete", Mutating: true},
		{Group: storagehost.GroupGlossary, Op: "find_by_category", Mutating: false},
		{Group: storagehost.GroupGlossary, Op: "find_by_term", Mutating: false},
		{Group: storagehost.GroupGlossary, Op: "find_candidate_by_id", Mutating: false},
		{Group: storagehost.GroupGlossary, Op: "find_recent", Mutating: false},
		{Group: storagehost.GroupGlossary, Op: "lookup", Mutating: false},
		{Group: storagehost.GroupGlossary, Op: "save", Mutating: true},
		{Group: storagehost.GroupGlossary, Op: "save_candidate", Mutating: true},
		{Group: storagehost.GroupHobbyGraph, Op: "candidate_by_id", Mutating: false},
		{Group: storagehost.GroupHobbyGraph, Op: "candidate_by_request", Mutating: false},
		{Group: storagehost.GroupHobbyGraph, Op: "candidate_save", Mutating: true},
		{Group: storagehost.GroupHobbyGraph, Op: "catalog_lookup", Mutating: false},
		{Group: storagehost.GroupHobbyGraph, Op: "lyrics_lookup", Mutating: false},
		{Group: storagehost.GroupHobbyGraph, Op: "viewer_overview", Mutating: false},
		{Group: storagehost.GroupHobbyGraph, Op: "viewer_stats", Mutating: false},
		{Group: storagehost.GroupL1, Op: "append_event", Mutating: true},
		{Group: storagehost.GroupL1, Op: "get_fresh_search_cache", Mutating: false},
		{Group: storagehost.GroupL1, Op: "get_similar_fresh_search_cache", Mutating: false},
		{Group: storagehost.GroupL1, Op: "invalidate_search_cache", Mutating: true},
		{Group: storagehost.GroupL1, Op: "latest_conversation_thread_reference", Mutating: false},
		{Group: storagehost.GroupL1, Op: "promote_memory_to_namespace", Mutating: true},
		{Group: storagehost.GroupL1, Op: "recent_by_namespace", Mutating: false},
		{Group: storagehost.GroupL1, Op: "recent_by_session", Mutating: false},
		{Group: storagehost.GroupL1, Op: "recent_by_state", Mutating: false},
		{Group: storagehost.GroupL1, Op: "recent_events", Mutating: false},
		{Group: storagehost.GroupL1, Op: "recent_recall_traces", Mutating: false},
		{Group: storagehost.GroupL1, Op: "save_message", Mutating: true},
		{Group: storagehost.GroupL1, Op: "save_recall_trace", Mutating: true},
		{Group: storagehost.GroupL1, Op: "save_search_cache", Mutating: true},
		{Group: storagehost.GroupL1, Op: "search_knowledge_items_fts", Mutating: false},
		{Group: storagehost.GroupL1, Op: "search_wiki_page_index", Mutating: false},
		{Group: storagehost.GroupL1, Op: "update_memory_state", Mutating: true},
		{Group: storagehost.GroupMovieCatalog, Op: "candidate_by_id", Mutating: false},
		{Group: storagehost.GroupMovieCatalog, Op: "candidate_by_request", Mutating: false},
		{Group: storagehost.GroupMovieCatalog, Op: "candidate_save", Mutating: true},
		{Group: storagehost.GroupMovieCatalog, Op: "lookup", Mutating: false},
		{Group: storagehost.GroupMovieCatalog, Op: "viewer_cards", Mutating: false},
		{Group: storagehost.GroupMovieCatalog, Op: "viewer_movie", Mutating: false},
		{Group: storagehost.GroupMovieCatalog, Op: "viewer_movies", Mutating: false},
		{Group: storagehost.GroupMovieCatalog, Op: "viewer_people", Mutating: false},
		{Group: storagehost.GroupMovieCatalog, Op: "viewer_person", Mutating: false},
		{Group: storagehost.GroupMovieCatalog, Op: "viewer_stats", Mutating: false},
		{Group: storagehost.GroupSession, Op: "delete", Mutating: true},
		{Group: storagehost.GroupSession, Op: "exists", Mutating: false},
		{Group: storagehost.GroupSession, Op: "load", Mutating: false},
		{Group: storagehost.GroupSession, Op: "load_or_create_canonical", Mutating: true},
		{Group: storagehost.GroupSession, Op: "save", Mutating: true},
		{Group: storagehost.GroupTask, Op: "commit", Mutating: true},
		{Group: storagehost.GroupTask, Op: "fence_acquire", Mutating: true},
		{Group: storagehost.GroupTask, Op: "fence_release", Mutating: true},
		{Group: storagehost.GroupTask, Op: "fence_reservation", Mutating: false},
		{Group: storagehost.GroupTask, Op: "read_begin", Mutating: false},
		{Group: storagehost.GroupTask, Op: "read_command", Mutating: false},
		{Group: storagehost.GroupTask, Op: "read_end", Mutating: false},
		{Group: storagehost.GroupTask, Op: "tx_abort", Mutating: false},
		{Group: storagehost.GroupTask, Op: "tx_begin", Mutating: false},
		{Group: storagehost.GroupTask, Op: "tx_command", Mutating: false},
		{Group: storagehost.GroupTask, Op: "tx_prepare_commit", Mutating: false},
		{Group: storagehost.GroupTask, Op: "writer_generation", Mutating: false},
		{Group: storagehost.GroupToolRegistry, Op: "find_action_receipt", Mutating: false},
		{Group: storagehost.GroupToolRegistry, Op: "get", Mutating: false},
		{Group: storagehost.GroupToolRegistry, Op: "list_for_platform", Mutating: false},
		{Group: storagehost.GroupToolRegistry, Op: "register", Mutating: true},
		{Group: storagehost.GroupToolRegistry, Op: "register_with_receipt", Mutating: true},
		{Group: storagehost.GroupTurn, Op: "active_projection", Mutating: false},
		{Group: storagehost.GroupTurn, Op: "commit", Mutating: true},
		{Group: storagehost.GroupTurn, Op: "complete", Mutating: true},
		{Group: storagehost.GroupTurn, Op: "fail", Mutating: true},
		{Group: storagehost.GroupTurn, Op: "outbox_claim", Mutating: true},
		{Group: storagehost.GroupTurn, Op: "outbox_claim_next", Mutating: true},
		{Group: storagehost.GroupTurn, Op: "projection", Mutating: false},
		{Group: storagehost.GroupTurn, Op: "receipt", Mutating: false},
		{Group: storagehost.GroupUserMemory, Op: "create_user_memory", Mutating: true},
		{Group: storagehost.GroupUserMemory, Op: "create_user_memory_candidate_with_request", Mutating: true},
		{Group: storagehost.GroupUserMemory, Op: "find_user_memory_by_id", Mutating: false},
		{Group: storagehost.GroupUserMemory, Op: "forget_user_memory", Mutating: true},
		{Group: storagehost.GroupUserMemory, Op: "list_user_memories", Mutating: false},
		{Group: storagehost.GroupUserMemory, Op: "supersede_user_memory", Mutating: true},
		{Group: storagehost.GroupUserMemory, Op: "update_user_memory_state", Mutating: true},
		{Group: storagehost.GroupVerificationReport, Op: "get_by_task_id", Mutating: false},
		{Group: storagehost.GroupVerificationReport, Op: "list_recent", Mutating: false},
		{Group: storagehost.GroupVerificationReport, Op: "save", Mutating: true},
		{Group: storagehost.GroupVerificationReport, Op: "summary", Mutating: false},
	}
	less := func(operations []storagehost.OperationSpec) {
		sort.Slice(operations, func(i, j int) bool {
			if operations[i].Group != operations[j].Group {
				return operations[i].Group < operations[j].Group
			}
			return operations[i].Op < operations[j].Op
		})
	}
	less(got)
	less(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("runtime owner manifest mismatch:\n got: %#v\nwant: %#v", got, want)
	}
	if len(got) != 99 {
		t.Fatalf("runtime owner manifest has %d operations, want the 99-operation stable owner manifest", len(got))
	}
}

func TestRuntimeStorageOwnersLocalDefaultLeavesBundleEmpty(t *testing.T) {
	for _, cfg := range []*config.Config{
		{},
		{Storage: config.StorageConfig{Host: config.StorageHostConfig{Mode: config.StorageHostModeLocal}}},
	} {
		readTokenCalls := 0
		newClientCalls := 0
		deps := runtimeStorageHostStartupDeps{
			readToken: func(string) (string, error) {
				readTokenCalls++
				return "", errors.New("unexpected token read")
			},
			newClient: func(storagehost.ClientConfig) (*storagehost.Client, error) {
				newClientCalls++
				return nil, errors.New("unexpected storage host client construction")
			},
		}
		owners, err := newRuntimeStorageOwnerBundleWithDeps(context.Background(), cfg, deps)
		if err != nil {
			t.Fatalf("newRuntimeStorageOwnerBundleWithDeps(local) error = %v", err)
		}
		if owners.Remote || owners.Client != nil || owners.TaskStore != nil || owners.EventStore != nil || owners.SessionRepository != nil || owners.ConversationStore != nil ||
			owners.UserMemoryStore != nil || owners.ToolRegistry != nil || owners.GlossaryStore != nil || owners.MovieCatalog != nil || owners.HobbyGraph != nil || owners.DurableWorkflow != nil || owners.Verification != nil {
			t.Fatalf("local bundle unexpectedly selected remote owners: %#v", owners)
		}
		if readTokenCalls != 0 || newClientCalls != 0 {
			t.Fatalf("local token/client calls = %d/%d, want 0/0", readTokenCalls, newClientCalls)
		}
	}
}

func TestRuntimeStorageOwnersRemoteBundleSelectsImplementedClients(t *testing.T) {
	root := t.TempDir()
	tokenFile := filepath.Join(root, "storage-host.token")
	if err := os.WriteFile(tokenFile, []byte(runtimeStorageHostTestToken), 0600); err != nil {
		t.Fatalf("create storage host token: %v", err)
	}
	host := newRuntimeStorageHostContractHandler(t, runtimeStorageHostOwnerRequiredOperations())
	server := httptest.NewServer(host)
	t.Cleanup(server.Close)
	cfg := runtimeStorageHostTestConfig(server.URL)
	cfg.Storage.Host.TokenFile = tokenFile
	cfg.Storage.Host.TimeoutSec = 5
	cfg.Storage.Host.StartupWaitSec = 5

	owners, err := newRuntimeStorageOwnerBundle(context.Background(), cfg)
	if err != nil {
		t.Fatalf("newRuntimeStorageOwnerBundle() error = %v", err)
	}
	if !owners.Remote || owners.Client == nil {
		t.Fatalf("remote/client = %t/%t, want true/true", owners.Remote, owners.Client != nil)
	}
	if _, ok := owners.TaskStore.(*storagehost.TaskStoreClient); !ok {
		t.Fatalf("Task store type = %T, want *storagehost.TaskStoreClient", owners.TaskStore)
	}
	if _, ok := owners.EventStore.(*storagehost.EventStoreClient); !ok {
		t.Fatalf("Event store type = %T, want *storagehost.EventStoreClient", owners.EventStore)
	}
	if _, ok := owners.ArchiveStore.(*storagehost.ArchiveStoreClient); !ok {
		t.Fatalf("Archive store type = %T, want *storagehost.ArchiveStoreClient", owners.ArchiveStore)
	}
	if _, ok := owners.SessionRepository.(*storagehost.SessionRepositoryClient); !ok {
		t.Fatalf("Session repository type = %T, want *storagehost.SessionRepositoryClient", owners.SessionRepository)
	}
	if _, ok := owners.ConversationStore.(*runtimeRemoteConversationStorageOwner); !ok {
		t.Fatalf("Conversation owner type = %T, want L1+Turn composite", owners.ConversationStore)
	}
	if owners.UserMemoryStore == nil || owners.ToolRegistry == nil || owners.GlossaryStore == nil || owners.MovieCatalog == nil || owners.HobbyGraph == nil || owners.DurableWorkflow == nil || owners.Verification == nil {
		t.Fatalf("remote bundle omitted a frozen typed owner client: %#v", owners)
	}
	if err := runtimeStorageHostOwnerCoverageError(owners); err == nil || err.Error() != runtimeStorageHostCoverageErrorMessage {
		t.Fatalf("coverage error = %v, want stable fail-closed error", err)
	}
}

func TestRuntimeStorageOwnersCoverageManifestNamesPendingBoundaries(t *testing.T) {
	want := []string{
		"movie_catalog", "hobby_graph", "advisor", "sandbox", "dci", "skill_governance", "workstream", "revenue", "persona_architecture", "browser_trace_to_api", "complexity_hotspot", "super_agent_harness", "ai_workflow", "knowledge_memory",
		"operation_memory", "common_raw", "conversation_archive", "conversation_user_memory", "conversation_knowledge_relation", "conversation_parquet_maintenance", "conversation_raw_import_reconciliation", "conversation_category_recall", "tool_registry_viewer_database", "glossary_viewer_database", "knowledge_memory_public_scope", "knowledge_memory_private_user_scope", "durable_store_workflow_runtime_tool",
	}
	if !reflect.DeepEqual(runtimeStorageHostUncoveredOwners, want) {
		t.Fatalf("pending owner coverage manifest = %#v, want %#v", runtimeStorageHostUncoveredOwners, want)
	}
}

func TestInitializeRuntimeTaskOwnerUsesSelectedRemoteStoreWithoutLocalOpen(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.WriteFile(workspace, []byte("workspace sentinel"), 0600); err != nil {
		t.Fatalf("create workspace sentinel: %v", err)
	}
	client, err := storagehost.NewClient(storagehost.ClientConfig{Endpoint: "http://127.0.0.1:18820", Token: "test-token"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	remoteStore, err := storagehost.NewTaskStoreClient(client)
	if err != nil {
		t.Fatalf("NewTaskStoreClient: %v", err)
	}
	deps := &Dependencies{}
	if err := initializeRuntimeTaskOwner(deps, workspace, remoteStore); err != nil {
		t.Fatalf("initializeRuntimeTaskOwner(remote) error = %v", err)
	}
	if deps.taskStore != remoteStore || deps.taskManager == nil {
		t.Fatalf("selected Task store/manager = %T/%v, want injected remote store and manager", deps.taskStore, deps.taskManager != nil)
	}
}

func TestRuntimeStorageOwnersRemoteCoverageGateChild(t *testing.T) {
	if os.Getenv(runtimeStorageOwnersChildEnv) != "1" {
		return
	}
	root := os.Getenv(runtimeStorageOwnersRootEnv)
	cfg := &config.Config{
		WorkspaceDir:       os.Getenv(runtimeStorageOwnersWorkspaceEnv),
		OperationMemoryDir: filepath.Join(root, "operation-memory"),
	}
	cfg.Storage.Host = config.StorageHostConfig{
		Mode:           config.StorageHostModeRemote,
		Transport:      config.StorageHostTransportTunnel,
		Endpoint:       os.Getenv(runtimeStorageOwnersEndpointEnv),
		TokenFile:      os.Getenv(runtimeStorageOwnersTokenFileEnv),
		TimeoutSec:     5,
		StartupWaitSec: 5,
	}
	cfg.Storage.Databases.EventStore = filepath.Join(root, "event", "events.db")
	cfg.Storage.Databases.ConversationL1 = filepath.Join(root, "conversation", "l1.db")
	cfg.Storage.Databases.ConversationArchive = filepath.Join(root, "conversation", "archive.db")
	cfg.Storage.Memory.RawSourceDir = filepath.Join(root, "raw")
	cfg.Session.StorageDir = filepath.Join(root, "sessions")

	buildDependencies(cfg)
	t.Fatal("buildDependencies() returned; remote uncovered-owner gate should terminate startup")
}
