package main

import (
	"context"
	"errors"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/storagehost"
)

const runtimeStorageHostCoverageErrorMessage = "storage.host remote runtime owner coverage is incomplete"

// runtimeStorageHostUncoveredOwners names durable owners that still open local
// state or lack a verified remote route. Remove an entry only with its owner
// integration and route evidence in the same change.
var runtimeStorageHostUncoveredOwners = []string{
	"movie_catalog",
	"hobby_graph",
	"advisor",
	"sandbox",
	"dci",
	"skill_governance",
	"workstream",
	"revenue",
	"persona_architecture",
	"browser_trace_to_api",
	"complexity_hotspot",
	"super_agent_harness",
	"ai_workflow",
	"knowledge_memory",
	"operation_memory",
	"common_raw",
	"conversation_archive",
	"conversation_user_memory",
	"conversation_knowledge_relation",
	"conversation_parquet_maintenance",
	"conversation_raw_import_reconciliation",
	"conversation_category_recall",
	"tool_registry_viewer_database",
	"glossary_viewer_database",
	"knowledge_memory_public_scope",
	"knowledge_memory_private_user_scope",
	"durable_store_workflow_runtime_tool",
}

type runtimeConversationStorageOwner interface {
	storagehost.L1GroupOwner
	storagehost.TurnGroupOwner
}

type runtimeRemoteConversationStorageOwner struct {
	*storagehost.L1StoreClient
	*storagehost.TurnStoreClient
}

type runtimeStorageOwnerBundle struct {
	Remote            bool
	Client            *storagehost.Client
	TaskStore         domaintask.Store
	EventStore        runtimeCanonicalEventStore
	ArchiveStore      storagehost.ArchiveGroupOwner
	SessionRepository orchestrator.SessionRepository
	ConversationStore runtimeConversationStorageOwner
	UserMemoryStore   *storagehost.UserMemoryStoreClient
	ToolRegistry      *storagehost.ToolRegistryClient
	GlossaryStore     *storagehost.GlossaryStoreClient
	MovieCatalog      *storagehost.MovieCatalogClient
	HobbyGraph        *storagehost.HobbyGraphClient
	DurableWorkflow   *storagehost.DurableStoreWorkflowClient
	Verification      *storagehost.VerificationReportClient
}

var (
	_ runtimeConversationStorageOwner       = (*runtimeRemoteConversationStorageOwner)(nil)
	_ runtimeConversationArchiveWriter      = (*storagehost.ArchiveStoreClient)(nil)
	_ runtimeConversationArchiveRecallStore = (*storagehost.ArchiveStoreClient)(nil)
)

func newRuntimeStorageOwnerBundle(ctx context.Context, cfg *config.Config) (runtimeStorageOwnerBundle, error) {
	return newRuntimeStorageOwnerBundleWithDeps(ctx, cfg, runtimeStorageHostStartupDeps{})
}

func newRuntimeStorageOwnerBundleWithDeps(ctx context.Context, cfg *config.Config, startupDeps runtimeStorageHostStartupDeps) (runtimeStorageOwnerBundle, error) {
	result, err := newRuntimeStorageHostClientWithDeps(ctx, cfg, runtimeStorageHostOwnerRequiredOperations(), startupDeps)
	if err != nil {
		return runtimeStorageOwnerBundle{}, err
	}
	if !result.Remote {
		return runtimeStorageOwnerBundle{}, nil
	}
	if result.Client == nil {
		return runtimeStorageOwnerBundle{}, errors.New(runtimeStorageHostCoverageErrorMessage)
	}
	taskStore, err := storagehost.NewTaskStoreClient(result.Client)
	if err != nil || taskStore == nil {
		return runtimeStorageOwnerBundle{}, errors.New("storage host Task owner construction failed")
	}
	return runtimeStorageOwnerBundle{
		Remote:            true,
		Client:            result.Client,
		TaskStore:         taskStore,
		EventStore:        storagehost.NewEventStoreClient(result.Client),
		ArchiveStore:      storagehost.NewArchiveStoreClient(result.Client),
		SessionRepository: storagehost.NewSessionRepositoryClient(result.Client),
		ConversationStore: &runtimeRemoteConversationStorageOwner{
			L1StoreClient:   storagehost.NewL1StoreClient(result.Client),
			TurnStoreClient: storagehost.NewTurnStoreClient(result.Client),
		},
		UserMemoryStore: storagehost.NewUserMemoryStoreClient(result.Client),
		ToolRegistry:    storagehost.NewToolRegistryClient(result.Client),
		GlossaryStore:   storagehost.NewGlossaryStoreClient(result.Client),
		MovieCatalog:    storagehost.NewMovieCatalogClient(result.Client),
		HobbyGraph:      storagehost.NewHobbyGraphClient(result.Client),
		DurableWorkflow: storagehost.NewDurableStoreWorkflowClient(result.Client),
		Verification:    storagehost.NewVerificationReportClient(result.Client),
	}, nil
}

func runtimeStorageHostOwnerCoverageError(owners runtimeStorageOwnerBundle) error {
	if !owners.Remote || len(runtimeStorageHostUncoveredOwners) == 0 {
		return nil
	}
	return errors.New(runtimeStorageHostCoverageErrorMessage)
}

func runtimeStorageHostOwnerRequiredOperations() []storagehost.OperationSpec {
	return []storagehost.OperationSpec{
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
}
