package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	agentprofileapp "github.com/Nyukimin/RenCrow_CORE/internal/application/agentprofile"
	moviecatalogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/moviecatalog"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/taskexecution"
	"gopkg.in/yaml.v3"
)

const maxTaskExecutionPolicyBytes = 1 << 20

func loadTaskExecutionPolicy(path string, registeredKinds, registeredActors []string) (taskexecution.Policy, string, error) {
	if path == "" || !filepath.IsAbs(path) {
		return taskexecution.Policy{}, "", errors.New("task execution policy path must be absolute")
	}
	file, err := os.Open(path)
	if err != nil {
		return taskexecution.Policy{}, "", fmt.Errorf("open task execution policy: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return taskexecution.Policy{}, "", fmt.Errorf("stat task execution policy: %w", err)
	}
	if !info.Mode().IsRegular() {
		return taskexecution.Policy{}, "", errors.New("task execution policy must be a regular file")
	}
	if info.Size() > maxTaskExecutionPolicyBytes {
		return taskexecution.Policy{}, "", fmt.Errorf("task execution policy exceeds %d bytes", maxTaskExecutionPolicyBytes)
	}
	contents, err := io.ReadAll(io.LimitReader(file, maxTaskExecutionPolicyBytes+1))
	if err != nil {
		return taskexecution.Policy{}, "", fmt.Errorf("read task execution policy: %w", err)
	}
	if len(contents) > maxTaskExecutionPolicyBytes {
		return taskexecution.Policy{}, "", fmt.Errorf("task execution policy exceeds %d bytes", maxTaskExecutionPolicyBytes)
	}

	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	decoder.KnownFields(true)
	var policy taskexecution.Policy
	if err := decoder.Decode(&policy); err != nil {
		return taskexecution.Policy{}, "", fmt.Errorf("decode task execution policy: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return taskexecution.Policy{}, "", errors.New("task execution policy must contain one YAML document")
		}
		return taskexecution.Policy{}, "", fmt.Errorf("decode trailing task execution policy document: %w", err)
	}
	if err := policy.Validate(registeredKinds, registeredActors); err != nil {
		return taskexecution.Policy{}, "", fmt.Errorf("validate task execution policy: %w", err)
	}
	revision, err := policy.CanonicalHash()
	if err != nil {
		return taskexecution.Policy{}, "", fmt.Errorf("hash task execution policy: %w", err)
	}
	return policy, revision, nil
}

func taskCenterOperationKinds(runtime toolRuntime) []taskcenter.Kind {
	kinds := []taskcenter.Kind{
		{ID: "movie.catalog.lookup", Label: "Movie catalog lookup", InputSchema: moviecatalogapp.LookupInputSchema(), ExecutorAvailable: runtime.MovieCatalogLookup != nil},
		{ID: "writing.novel", Label: "Novel writing", IntrinsicAllowedAgents: []string{"midori"}, UnavailableReason: "dialogue_authoring_owner_unavailable"},
	}
	if runtime.MovieCatalogLookup == nil {
		kinds[0].UnavailableReason = "movie_catalog_lookup_owner_unavailable"
	}
	return kinds
}

func taskCenterRegisteredActorIDs() []string {
	profiles := agentprofileapp.NewStaticCatalog().List()
	actors := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		actors = append(actors, profile.ID)
	}
	return actors
}

func taskCenterNativeOwner(manager *taskmanager.Manager) taskcenter.TaskCenterNativeExecutionOwner {
	if manager == nil {
		return nil
	}
	owner, _ := any(manager).(taskcenter.TaskCenterNativeExecutionOwner)
	return owner
}

func buildTaskCenterRuntime(cfg *config.Config, deps *Dependencies, workstreamStore viewer.WorkstreamStore, agents agentRuntime, runtime toolRuntime) {
	if deps == nil {
		return
	}
	deps.taskCenterUnavailableReason = "task_center_not_configured"
	if cfg != nil && cfg.LocalAgentOps.Enabled {
		deps.taskCenterUserID = strings.TrimSpace(cfg.LocalAgentOps.UserID)
		if deps.taskCenterUserID != "" {
			if token, err := readAgentOpsToken(cfg.LocalAgentOps.AuthTokenFile); err == nil {
				deps.taskCenterUserToken = append([]byte(nil), token...)
			} else {
				deps.taskCenterUnavailableReason = "task_center_auth_unavailable"
			}
		}
	}
	if cfg == nil || !cfg.Workstream.IsEnabled() {
		deps.taskCenterUnavailableReason = "task_center_workstream_unavailable"
		return
	}
	if strings.ToLower(strings.TrimSpace(cfg.Workstream.Storage)) != "sqlite" {
		deps.taskCenterUnavailableReason = "task_center_sqlite_required"
		return
	}
	store, ok := workstreamStore.(taskcenter.TaskPlanStore)
	if !ok || store == nil {
		deps.taskCenterUnavailableReason = "task_center_store_unavailable"
		return
	}
	kinds := taskCenterOperationKinds(runtime)
	nativeTaskOwner := taskCenterNativeOwner(deps.taskManager)
	var resultOwner taskcenter.TaskCenterExecutionResultOwner
	if owner, ok := workstreamStore.(taskcenter.TaskCenterExecutionResultOwner); ok {
		resultOwner = owner
	}
	if runtime.MovieCatalogLookup != nil && (nativeTaskOwner == nil || resultOwner == nil) {
		kinds[0].ExecutorAvailable = false
		if nativeTaskOwner == nil {
			kinds[0].UnavailableReason = "native_task_center_owner_unavailable"
		} else {
			kinds[0].UnavailableReason = "task_center_result_artifact_owner_unavailable"
		}
	}
	kindIDs := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		kindIDs = append(kindIDs, kind.ID)
	}
	actors := taskCenterRegisteredActorIDs()
	policyPath := strings.TrimSpace(cfg.TaskExecutionPolicyFile)
	if policyPath == "" {
		deps.taskCenterUnavailableReason = "task_center_policy_unconfigured"
		return
	}
	policy, _, err := loadTaskExecutionPolicy(policyPath, kindIDs, actors)
	if err != nil {
		deps.taskCenterUnavailableReason = "task_center_policy_invalid"
		return
	}
	service, err := taskcenter.NewService(store, policy, actors, kinds, time.Now)
	if err != nil {
		deps.taskCenterUnavailableReason = "task_center_unavailable"
		return
	}
	if cancellationOwner, ok := any(deps.taskManager).(taskcenter.UserTaskCancellationOwner); ok {
		service.WithUserTaskCancellationOwner(cancellationOwner)
	}
	if publications, ok := deps.sessionRepository.(taskcenter.UserTaskCancellationPublicationReader); ok {
		service.WithUserTaskCancellationPublicationReader(publications)
	}
	// Native receipt/result recovery does not need a currently connected effect
	// owner. Keep the coordinator available for historical reads and acceptance;
	// executor availability remains false when the kind handler is absent.
	if nativeTaskOwner != nil && resultOwner != nil {
		service.WithNativeExecution(nativeTaskOwner, resultOwner, runtime.MovieCatalogLookup)
	}
	deps.taskCenter = service
	attachTaskCenterPlanningPorts(cfg, deps, agents)
	deps.taskCenterUnavailableReason = ""
	if runtime.ChatRunnerV2 != nil {
		runtime.ChatRunnerV2.WithTaskCenterService(service, false)
	}
	if runtime.WorkerRunnerV2 != nil {
		runtime.WorkerRunnerV2.WithTaskCenterService(service, true)
	}
}

func registerTaskCenterRoutes(mux *http.ServeMux, cfg *config.Config, deps *Dependencies) {
	if mux == nil {
		return
	}
	mux.HandleFunc("/viewer/task-center", viewer.HandleTaskCenterPage)
	var service viewer.TaskCenterService
	var userID string
	var token []byte
	var unavailableReason string
	if deps != nil {
		if deps.taskCenterCenter != nil {
			service = deps.taskCenterCenter
		} else if deps.taskCenter != nil {
			service = deps.taskCenter
		}
		userID = deps.taskCenterUserID
		token = deps.taskCenterUserToken
		unavailableReason = deps.taskCenterUnavailableReason
	}
	mux.Handle("/api/v1/task-center/", viewer.NewTaskCenterHandler(service, userID, token, unavailableReason))
}
