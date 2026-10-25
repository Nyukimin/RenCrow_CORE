package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	runtimePersistenceQueueCapacity           = 1024
	runtimePersistenceQueueByteCapacity       = 64 << 20
	runtimeViewerChatAdmissionCommands        = 64
	runtimeViewerChatAdmissionBytes     int64 = 8 << 20
)

func newRuntimePersistenceQueue() (*persistencequeue.Queue[persistencequeue.PersistenceCommand], error) {
	return persistencequeue.NewPersistenceQueue(runtimePersistenceQueueCapacity, runtimePersistenceQueueByteCapacity)
}

func runtimeTaskStoreSupportsRAMAdmission(store domaintask.Store) bool {
	local, ok := store.(*taskpersistence.JSONLStore)
	if !ok || local == nil {
		return false
	}
	_, indexed := local.IndexStats()
	return indexed
}

func runtimeConfiguredOwnerMatchesQueue(
	enabled bool,
	owner any,
	queue *persistencequeue.Queue[persistencequeue.PersistenceCommand],
) bool {
	if !enabled {
		return true
	}
	if owner == nil || queue == nil {
		return false
	}
	matcher, ok := owner.(interface {
		MatchesPersistenceQueue(*persistencequeue.Queue[persistencequeue.PersistenceCommand]) bool
	})
	return ok && matcher.MatchesPersistenceQueue(queue)
}

func bindRuntimeOwnerPersistenceQueue(owner any, queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]) error {
	if owner == nil || queue == nil {
		return errors.New("persistence owner and shared queue are required")
	}
	setter, ok := owner.(interface {
		SetPersistenceQueue(*persistencequeue.Queue[persistencequeue.PersistenceCommand]) error
	})
	if !ok {
		return errors.New("persistence owner does not support shared queue admission")
	}
	if err := setter.SetPersistenceQueue(queue); err != nil {
		return fmt.Errorf("bind persistence owner to shared queue: %w", err)
	}
	if !runtimeConfiguredOwnerMatchesQueue(true, owner, queue) {
		return errors.New("persistence owner did not confirm shared queue binding")
	}
	return nil
}

func runtimeCombinedChatAdmissionEnabled(cfg *config.Config, classifier any, engine any, chatProvider, distributed bool) bool {
	if cfg == nil || cfg.TTS.SpeechPlanEnabled || cfg.Distributed.Enabled || distributed || !chatProvider ||
		!cfg.TaskStore.Index.Enabled || !cfg.ActionStore.Index.Enabled {
		return false
	}
	if _, ok := classifier.(domainagent.RouteReplyClassifier); !ok {
		return false
	}
	_, ok := engine.(interface {
		CommitConversationTurn(context.Context, domainconversation.ConversationTurnRequest) (domainconversation.ConversationTurnResult, error)
	})
	return ok
}

type runtimeViewerChatBinder interface {
	BindChat(modulecore.TraceID, context.Context) (func(), error)
	MarkChatSynchronous(modulecore.TraceID) error
}

func admitRuntimeLegacyViewerChat(
	ctx context.Context,
	queue *persistencequeue.Queue[persistencequeue.PersistenceCommand],
	taskStore domaintask.Store,
	combinedChatEnabled bool,
	relay runtimeViewerChatBinder,
	identity viewer.LegacyPreSessionAdmissionIdentity,
) (context.Context, func(), error) {
	if ctx == nil {
		return nil, nil, errors.New("Viewer chat request context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if identity.Recipient != "mio" || identity.OperationSource != "RenCrow_CMD" {
		return nil, nil, errors.New("Viewer RAM admission is outside the RenCrow_CMD to Mio scope")
	}
	if !combinedChatEnabled {
		return nil, nil, errors.New("combined one-generation CHAT runtime is unavailable")
	}
	if !runtimeTaskStoreSupportsRAMAdmission(taskStore) {
		return nil, nil, errors.New("local indexed Task owner is required for Viewer RAM admission")
	}
	if relay == nil || queue == nil || identity.TraceID.Validate() != nil {
		return nil, nil, errors.New("Viewer RAM admission dependencies or trace identity are unavailable")
	}
	admission, err := persistencequeue.Admit(queue, runtimeViewerChatAdmissionCommands, runtimeViewerChatAdmissionBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("reserve Viewer CHAT persistence capacity: %w", err)
	}
	admittedContext := persistencequeue.WithAdmission(context.Background(), admission)
	releaseChatLease, err := relay.BindChat(identity.TraceID, admittedContext)
	if err != nil {
		_ = admission.Release()
		return nil, nil, fmt.Errorf("bind Viewer CHAT persistence lease: %w", err)
	}
	var releaseLeaseOnce sync.Once
	releaseLease := func() {
		releaseLeaseOnce.Do(func() {
			if releaseChatLease != nil {
				releaseChatLease()
			}
		})
	}
	var releaseAdmissionOnce sync.Once
	var releaseAdmissionErr error
	releaseAdmission := func() error {
		releaseAdmissionOnce.Do(func() { releaseAdmissionErr = admission.Release() })
		return releaseAdmissionErr
	}
	releaseChat := func() {
		releaseLease()
		if err := releaseAdmission(); err != nil {
			log.Printf("[Viewer] RAM admission release failed error_type=%T", err)
		}
	}
	fallback := func(fallbackContext context.Context) (context.Context, error) {
		if fallbackContext == nil {
			return nil, persistencequeue.ErrNilContext
		}
		if err := admission.Barrier(fallbackContext); err != nil {
			return nil, fmt.Errorf("drain pre-route RAM persistence before durable route: %w", err)
		}
		if err := relay.MarkChatSynchronous(identity.TraceID); err != nil {
			releaseChat()
			return nil, fmt.Errorf("switch Viewer CHAT trace to synchronous publication: %w", err)
		}
		// Marking the scope serializes against any in-flight RAM OnEvent. A
		// second barrier drains an event that raced between the first barrier
		// and that serialized scope transition.
		if err := admission.Barrier(fallbackContext); err != nil {
			releaseChat()
			return nil, fmt.Errorf("drain RAM persistence after synchronous CHAT transition: %w", err)
		}
		releaseLease()
		if err := releaseAdmission(); err != nil {
			return nil, fmt.Errorf("release unused Viewer CHAT persistence capacity: %w", err)
		}
		return persistencequeue.WithAdmission(fallbackContext, nil), nil
	}
	return persistencequeue.WithSynchronousFallback(admittedContext, fallback), releaseChat, nil
}

func initializeRuntimeTaskOwner(deps *Dependencies, workspace string, selectedStores ...domaintask.Store) error {
	return initializeRuntimeTaskOwnerWithCriteriaRevision(deps, workspace, "", selectedStores...)
}

func initializeRuntimeTaskOwnerWithCriteriaRevision(deps *Dependencies, workspace, expectedCriteriaRevision string, selectedStores ...domaintask.Store) error {
	if deps == nil {
		return errors.New("runtime dependencies are nil")
	}
	if len(selectedStores) > 1 {
		return errors.New("canonical Task owner accepts at most one selected store")
	}
	if deps.taskStore != nil || deps.taskManager != nil {
		return errors.New("canonical Task owner is already initialized")
	}
	var store domaintask.Store
	if len(selectedStores) == 1 {
		store = selectedStores[0]
	}
	// The Task owner is told its store has an ID index only when this process
	// opened the store with one. A selected (remote) store lives in the storage
	// host, whose configuration is not visible here, so it is never declared.
	var managerOptions []taskmanager.Option
	if store == nil {
		path := strings.TrimSpace(defaultTaskStorePath(workspace))
		if path == "" {
			return errors.New("workspace directory is required for canonical Task owner")
		}
		var err error
		store, err = taskpersistence.NewJSONLStoreWithOptions(path, taskpersistence.OpenOptions{Index: deps.taskStoreIndex, Persist: deps.taskStorePersist, Rotation: deps.taskStoreRotation, RotationSizeValve: deps.taskStoreSizeValve})
		if err != nil {
			return fmt.Errorf("open canonical Task store: %w", err)
		}
		if deps.taskStoreIndex {
			managerOptions = append(managerOptions, taskmanager.WithIDIndex())
		}
	}
	limits := runtimeTaskParallelLimits(deps)
	deps.taskStore = store
	if expectedCriteriaRevision == "" {
		deps.taskManager = taskmanager.New(store, limits, managerOptions...)
		return nil
	}
	manager, err := taskmanager.NewWithExpectedCriteriaRevision(store, limits, expectedCriteriaRevision, managerOptions...)
	if err != nil {
		return fmt.Errorf("configure Task owner criteria revision: %w", err)
	}
	deps.taskManager = manager
	return nil
}

func taskParallelLimitsFromConfig(cfg *config.Config) taskmanager.ParallelLimits {
	if cfg == nil || cfg.TaskParallelLimits == nil {
		return taskmanager.DefaultParallelLimits()
	}
	configured := cfg.TaskParallelLimits
	return taskmanager.ParallelLimits{
		Global:            configured.Global,
		PerModule:         configured.PerModule,
		CodingTasks:       configured.CodingTasks,
		LongResearchTasks: configured.LongResearchTasks,
		DestructiveTasks:  configured.DestructiveTasks,
	}
}

func taskParallelLimitsPointer(limits taskmanager.ParallelLimits) *taskmanager.ParallelLimits {
	return &limits
}

func runtimeTaskParallelLimits(deps *Dependencies) taskmanager.ParallelLimits {
	if deps == nil || deps.taskParallelLimits == nil {
		return taskmanager.DefaultParallelLimits()
	}
	return *deps.taskParallelLimits
}

// taskExecutionRunner admits a Tool call against the currently owned Task Run
// before delegating to the existing runner. It performs no identity minting or
// context rewriting, so downstream policy and Tool runners receive the exact
// context and arguments selected by the caller.
type taskExecutionRunner struct {
	owner *taskmanager.Manager
	inner domaintool.RunnerV2
}

func (r *taskExecutionRunner) ExecuteV2(ctx context.Context, toolName string, args map[string]any) (*domaintool.ToolResponse, error) {
	if r == nil {
		return nil, errors.New("task execution runner is nil")
	}
	if r.owner == nil {
		return nil, errors.New("task execution owner is nil")
	}
	if r.inner == nil {
		return nil, errors.New("task execution inner runner is nil")
	}

	if _, err := r.owner.ValidateExecutionContext(ctx); err != nil {
		return nil, fmt.Errorf("task execution admission denied: %w", err)
	}

	return r.inner.ExecuteV2(ctx, toolName, args)
}

func (r *taskExecutionRunner) ListTools(ctx context.Context) ([]domaintool.ToolMetadata, error) {
	if r == nil {
		return nil, errors.New("task execution runner is nil")
	}
	if r.inner == nil {
		return nil, errors.New("task execution inner runner is nil")
	}
	return r.inner.ListTools(ctx)
}
