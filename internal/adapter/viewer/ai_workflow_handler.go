package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	aiworkflowapp "github.com/Nyukimin/RenCrow_CORE/internal/application/aiworkflow"
	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	domainskill "github.com/Nyukimin/RenCrow_CORE/internal/domain/skillgovernance"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type AIWorkflowStateLister interface {
	ListProjectMemoryIndexes(ctx context.Context, limit int) ([]domainai.ProjectMemoryIndex, error)
	ListWorktreeRegistries(ctx context.Context, limit int) ([]domainai.WorktreeRegistry, error)
	ListCommandRegistries(ctx context.Context, limit int) ([]domainai.CommandRegistry, error)
	ListContextUsages(ctx context.Context, limit int) ([]domainai.ContextUsage, error)
}

type AIWorkflowStateStore interface {
	AIWorkflowStateLister
	SaveProjectMemoryIndex(ctx context.Context, item domainai.ProjectMemoryIndex) error
	SaveWorktreeRegistry(ctx context.Context, item domainai.WorktreeRegistry) error
	SaveCommandRegistry(ctx context.Context, item domainai.CommandRegistry) error
	SaveContextUsage(ctx context.Context, item domainai.ContextUsage) error
}

type AIWorkflowLister interface {
	AIWorkflowStateLister
	modulecore.EventReader
}

type AIWorkflowStore interface {
	AIWorkflowStateStore
	modulecore.EventStore
}

type AIWorkflowSkillBootstrap interface {
	Record(ctx context.Context, task domainskill.TaskContext, usedSkillIDs []string) ([]domainskill.SkillTriggerLog, error)
}

type UsageContinuitySnapshot struct {
	Scope               string    `json:"scope"`
	ScopeID             string    `json:"scope_id"`
	UsageCount          int       `json:"usage_count"`
	LatestEventID       string    `json:"latest_event_id"`
	LatestCreatedAt     time.Time `json:"latest_created_at"`
	LatestAgent         string    `json:"latest_agent,omitempty"`
	LatestModel         string    `json:"latest_model,omitempty"`
	LatestContextTokens int       `json:"latest_context_tokens,omitempty"`
	LatestInputTokens   int       `json:"latest_input_tokens,omitempty"`
	LatestOutputTokens  int       `json:"latest_output_tokens,omitempty"`
	LatestRunState      string    `json:"latest_run_state,omitempty"`
	CompactionID        string    `json:"compaction_id,omitempty"`
}

func HandleAIWorkflowExternalControlCheck(store AIWorkflowStore, policy domainai.ExternalControlPolicy) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req domainai.ExternalControlRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid external control policy payload", http.StatusBadRequest)
			return
		}
		decision := domainai.EvaluateExternalControl(policy, req)
		if store != nil {
			now := time.Now().UTC()
			event := modulecore.NewRootEventEnvelope("ai_workflow", "external_control_policy.checked", now, map[string]any{
				"actor_label": strings.TrimSpace(req.Actor), "status": decision.Status,
				"summary": strings.TrimSpace(req.ChannelID + " " + req.Action),
			})
			if err := store.Append(r.Context(), event); err != nil {
				http.Error(w, "failed to save external control policy event", http.StatusInternalServerError)
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"request":  req,
			"decision": decision,
		})
	}
}

func HandleAIWorkflowStatus(store AIWorkflowLister) http.HandlerFunc {
	return HandleAIWorkflowStatusWithPolicy(store, domainai.ContextBudgetPolicy{})
}

func HandleAIWorkflowStatusWithPolicy(store AIWorkflowLister, contextBudgetPolicy domainai.ContextBudgetPolicy) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "ai workflow store unavailable", http.StatusServiceUnavailable)
			return
		}
		limit, err := parseViewerLimit(r.URL.Query().Get("limit"), 20, 100)
		if err != nil {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		events, err := store.ListByComponent(r.Context(), "ai_workflow", limit)
		if err != nil {
			http.Error(w, "failed to load workflow events", http.StatusInternalServerError)
			return
		}
		memories, err := store.ListProjectMemoryIndexes(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load project memory indexes", http.StatusInternalServerError)
			return
		}
		worktrees, err := store.ListWorktreeRegistries(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load worktree registries", http.StatusInternalServerError)
			return
		}
		commands, err := store.ListCommandRegistries(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load command registries", http.StatusInternalServerError)
			return
		}
		contexts, err := store.ListContextUsages(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load context usages", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"events":                 nonNilEventEnvelopes(events),
			"project_memory_indexes": nonNilProjectMemoryIndexes(memories),
			"worktree_registries":    nonNilWorktreeRegistries(worktrees),
			"command_registries":     nonNilCommandRegistries(commands),
			"context_usages":         nonNilContextUsages(contexts),
			"usage_continuity":       buildUsageContinuitySnapshots(events, contexts, limit),
			"context_budget_policy":  contextBudgetPolicy,
		})
	}
}

func HandleAIWorkflowProjectMemoryCreate(store AIWorkflowStore) http.HandlerFunc {
	return saveAIWorkflowItem(store, "project memory index", func(ctx context.Context, store AIWorkflowStore, dec *json.Decoder) error {
		var item domainai.ProjectMemoryIndex
		if err := dec.Decode(&item); err != nil {
			return err
		}
		return store.SaveProjectMemoryIndex(ctx, item)
	})
}

func HandleAIWorkflowWorktreeCreate(store AIWorkflowStore) http.HandlerFunc {
	return saveAIWorkflowItem(store, "worktree registry", func(ctx context.Context, store AIWorkflowStore, dec *json.Decoder) error {
		var item domainai.WorktreeRegistry
		if err := dec.Decode(&item); err != nil {
			return err
		}
		return store.SaveWorktreeRegistry(ctx, item)
	})
}

func HandleAIWorkflowCommandCreate(store AIWorkflowStore) http.HandlerFunc {
	return saveAIWorkflowItem(store, "command registry", func(ctx context.Context, store AIWorkflowStore, dec *json.Decoder) error {
		var item domainai.CommandRegistry
		if err := dec.Decode(&item); err != nil {
			return err
		}
		return store.SaveCommandRegistry(ctx, item)
	})
}

func HandleAIWorkflowCommandRun(store AIWorkflowStore, skills AIWorkflowSkillBootstrap) http.HandlerFunc {
	type request struct {
		CommandName  string   `json:"command_name"`
		Text         string   `json:"text,omitempty"`
		Agent        string   `json:"agent,omitempty"`
		RunID        string   `json:"run_id,omitempty"`
		WorkstreamID string   `json:"workstream_id,omitempty"`
		UsedSkillIDs []string `json:"used_skill_ids,omitempty"`
	}
	type response struct {
		Command domainai.CommandRegistry      `json:"command"`
		Event   modulecore.EventEnvelope      `json:"event"`
		Skills  []domainskill.SkillTriggerLog `json:"skills"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "ai workflow store unavailable", http.StatusServiceUnavailable)
			return
		}
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid command run payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		commandName := strings.TrimSpace(req.CommandName)
		if commandName == "" {
			http.Error(w, "command_name is required", http.StatusBadRequest)
			return
		}
		commands, err := store.ListCommandRegistries(r.Context(), 1000)
		if err != nil {
			http.Error(w, "failed to load command registry: "+err.Error(), http.StatusInternalServerError)
			return
		}
		command, ok := findCommandRegistry(commands, commandName)
		if !ok {
			http.Error(w, "command not registered", http.StatusNotFound)
			return
		}
		agent := firstNonEmptyString(req.Agent, command.DefaultAgent, "Worker")
		now := time.Now().UTC()
		event := modulecore.NewRootEventEnvelope("ai_workflow", "command.invoked", now, map[string]any{
			"agent_label": agent, "command_name": command.CommandName,
			"status": "requested", "summary": strings.TrimSpace(req.Text),
		})
		event.RunID = modulecore.RunID(strings.TrimSpace(req.RunID))
		event.WorkstreamID = modulecore.WorkstreamID(strings.TrimSpace(req.WorkstreamID))
		if err := store.Append(r.Context(), event); err != nil {
			http.Error(w, "failed to save command event: "+err.Error(), http.StatusBadRequest)
			return
		}
		used := append([]string(nil), req.UsedSkillIDs...)
		if command.RequiredSkill != "" && !stringSliceContains(used, command.RequiredSkill) {
			used = append(used, command.RequiredSkill)
		}
		var logs []domainskill.SkillTriggerLog
		if skills != nil {
			logs, err = skills.Record(r.Context(), domainskill.TaskContext{
				Text:         firstNonEmptyString(req.Text, command.Description, command.CommandName),
				Intent:       strings.TrimPrefix(command.CommandName, "/"),
				Agent:        agent,
				WorkstreamID: req.WorkstreamID,
			}, used)
			if err != nil {
				http.Error(w, "command skill bootstrap failed: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
		writeJSON(w, http.StatusCreated, response{
			Command: command,
			Event:   event,
			Skills:  logs,
		})
	}
}

func HandleAIWorkflowContextUsageCreate(store AIWorkflowStore) http.HandlerFunc {
	return saveAIWorkflowItem(store, "context usage", func(ctx context.Context, store AIWorkflowStore, dec *json.Decoder) error {
		var item domainai.ContextUsage
		if err := dec.Decode(&item); err != nil {
			return err
		}
		return store.SaveContextUsage(ctx, item)
	})
}

func HandleAIWorkflowContextBudgetCheck(store AIWorkflowStore, policy domainai.ContextBudgetPolicy) http.HandlerFunc {
	type response struct {
		ContextUsage domainai.ContextUsage          `json:"context_usage"`
		Decision     domainai.ContextBudgetDecision `json:"decision"`
		Event        *modulecore.EventEnvelope      `json:"event,omitempty"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "ai workflow store unavailable", http.StatusServiceUnavailable)
			return
		}
		var usage domainai.ContextUsage
		if err := json.NewDecoder(r.Body).Decode(&usage); err != nil {
			http.Error(w, "invalid context budget payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		decision, err := domainai.EvaluateContextBudget(usage, policy)
		if err != nil {
			http.Error(w, "invalid context budget payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := store.SaveContextUsage(r.Context(), usage); err != nil {
			http.Error(w, "failed to save context usage: "+err.Error(), http.StatusBadRequest)
			return
		}
		var event *modulecore.EventEnvelope
		switch decision.Status {
		case domainai.ContextBudgetStatusWarn, domainai.ContextBudgetStatusStop:
			createdAt := usage.CreatedAt
			if createdAt.IsZero() {
				createdAt = time.Now().UTC()
			}
			eventType := "context_budget_warning"
			if decision.Status == domainai.ContextBudgetStatusStop {
				eventType = "context_budget_exceeded"
			}
			item := modulecore.NewRootEventEnvelope("ai_workflow", eventType, createdAt, map[string]any{
				"context_usage_record_id": usage.EventID, "agent_label": usage.Agent,
				"status": decision.Status, "summary": decision.Reason,
			})
			if err := store.Append(r.Context(), item); err != nil {
				http.Error(w, "failed to save context budget event: "+err.Error(), http.StatusBadRequest)
				return
			}
			event = &item
		}
		writeJSON(w, http.StatusCreated, response{
			ContextUsage: usage,
			Decision:     decision,
			Event:        event,
		})
	}
}

func HandleAIWorkflowHeavyWorkerEvaluate(store AIWorkflowStore, policy domainai.HeavyWorkerPolicy) http.HandlerFunc {
	type response struct {
		Request  domainai.HeavyWorkerRequest  `json:"request"`
		Decision domainai.HeavyWorkerDecision `json:"decision"`
		Event    *modulecore.EventEnvelope    `json:"event,omitempty"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "ai workflow store unavailable", http.StatusServiceUnavailable)
			return
		}
		var req domainai.HeavyWorkerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid heavy worker payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		decision := domainai.EvaluateHeavyWorker(req, policy)
		var event *modulecore.EventEnvelope
		if decision.Status == domainai.HeavyWorkerStatusRequested {
			createdAt := time.Now().UTC()
			item := modulecore.NewRootEventEnvelope("ai_workflow", "heavy_worker.requested", createdAt, map[string]any{
				"source_record_id": req.EventID, "agent_label": req.Agent,
				"status": decision.Status, "summary": strings.Join(decision.Reasons, "; "),
			})
			if err := store.Append(r.Context(), item); err != nil {
				http.Error(w, "failed to save heavy worker event: "+err.Error(), http.StatusBadRequest)
				return
			}
			event = &item
		}
		writeJSON(w, http.StatusOK, response{
			Request:  req,
			Decision: decision,
			Event:    event,
		})
	}
}

type HeavyWorkerRuntimeDiagnosticsOptions struct {
	GatewayConfigured bool
	GatewayBaseURL    string
	LogicalAlias      string
}

func HandleAIWorkflowHeavyWorkerRuntimeDiagnostics(opts HeavyWorkerRuntimeDiagnosticsOptions) http.HandlerFunc {
	type response struct {
		Role           string `json:"role"`
		Route          string `json:"route"`
		Provider       string `json:"provider,omitempty"`
		Configured     bool   `json:"configured"`
		GatewayBaseURL string `json:"gateway_base_url,omitempty"`
		LogicalAlias   string `json:"logical_alias,omitempty"`
		FailureIsError bool   `json:"failure_is_error"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, response{
			Role:           "Heavy",
			Route:          "ANALYZE",
			Provider:       "rencrow_llm",
			Configured:     opts.GatewayConfigured && strings.TrimSpace(opts.GatewayBaseURL) != "" && strings.TrimSpace(opts.LogicalAlias) != "",
			GatewayBaseURL: strings.TrimRight(strings.TrimSpace(opts.GatewayBaseURL), "/"),
			LogicalAlias:   strings.TrimSpace(opts.LogicalAlias),
			FailureIsError: true,
		})
	}
}

func HandleAIWorkflowProjectInit(scanner *aiworkflowapp.ProjectScanner, projectMemoryRoot string) http.HandlerFunc {
	type request struct {
		RepoRoot          string `json:"repo_root"`
		ProjectMemoryRoot string `json:"project_memory_root"`
		RepoName          string `json:"repo_name"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if scanner == nil {
			http.Error(w, "ai workflow project scanner unavailable", http.StatusServiceUnavailable)
			return
		}
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid project init payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.ProjectMemoryRoot == "" {
			req.ProjectMemoryRoot = projectMemoryRoot
		}
		result, err := scanner.Run(r.Context(), aiworkflowapp.ProjectInitOptions{
			RepoRoot:          req.RepoRoot,
			ProjectMemoryRoot: req.ProjectMemoryRoot,
			RepoName:          req.RepoName,
		})
		if err != nil {
			http.Error(w, "project init failed: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, result)
	}
}

func HandleAIWorkflowWorktreeCreateRuntime(manager *aiworkflowapp.WorktreeManager, baseDir string) http.HandlerFunc {
	type request struct {
		RepoRoot    string `json:"repo_root"`
		BaseDir     string `json:"base_dir"`
		RepoName    string `json:"repo_name"`
		Branch      string `json:"branch"`
		DetachedRef string `json:"detached_ref"`
		PathName    string `json:"path_name"`
		Purpose     string `json:"purpose"`
		OwnerAgent  string `json:"owner_agent"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if manager == nil {
			http.Error(w, "ai workflow worktree manager unavailable", http.StatusServiceUnavailable)
			return
		}
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid worktree create payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.BaseDir == "" {
			req.BaseDir = baseDir
		}
		result, err := manager.Create(r.Context(), aiworkflowapp.WorktreeCreateOptions{
			RepoRoot:    req.RepoRoot,
			BaseDir:     req.BaseDir,
			RepoName:    req.RepoName,
			Branch:      req.Branch,
			DetachedRef: req.DetachedRef,
			PathName:    req.PathName,
			Purpose:     req.Purpose,
			OwnerAgent:  req.OwnerAgent,
		})
		if err != nil {
			http.Error(w, "worktree create failed: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, result)
	}
}

func HandleAIWorkflowWorktreeCloseRuntime(manager *aiworkflowapp.WorktreeManager, baseDir string) http.HandlerFunc {
	type request struct {
		RepoRoot     string `json:"repo_root"`
		BaseDir      string `json:"base_dir"`
		RepoName     string `json:"repo_name"`
		WorktreeID   string `json:"worktree_id"`
		WorktreePath string `json:"worktree_path"`
		Branch       string `json:"branch"`
		OwnerAgent   string `json:"owner_agent"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if manager == nil {
			http.Error(w, "ai workflow worktree manager unavailable", http.StatusServiceUnavailable)
			return
		}
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid worktree close payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.BaseDir == "" {
			req.BaseDir = baseDir
		}
		result, err := manager.Close(r.Context(), aiworkflowapp.WorktreeCloseOptions{
			RepoRoot:     req.RepoRoot,
			BaseDir:      req.BaseDir,
			RepoName:     req.RepoName,
			WorktreeID:   req.WorktreeID,
			WorktreePath: req.WorktreePath,
			Branch:       req.Branch,
			OwnerAgent:   req.OwnerAgent,
		})
		if err != nil {
			http.Error(w, "worktree close failed: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, result)
	}
}

func findCommandRegistry(commands []domainai.CommandRegistry, name string) (domainai.CommandRegistry, bool) {
	for _, command := range commands {
		if command.CommandName == name {
			return command, true
		}
	}
	return domainai.CommandRegistry{}, false
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func buildUsageContinuitySnapshots(events []modulecore.EventEnvelope, usages []domainai.ContextUsage, limit int) []UsageContinuitySnapshot {
	if len(usages) == 0 {
		return []UsageContinuitySnapshot{}
	}
	latestRunState := latestRunStatesByScope(events)
	snapshots := map[string]UsageContinuitySnapshot{}
	for _, usage := range usages {
		for _, scope := range contextUsageScopes(usage) {
			key := scope.Scope + "\x00" + scope.ScopeID
			snap := snapshots[key]
			if snap.Scope == "" {
				snap.Scope = scope.Scope
				snap.ScopeID = scope.ScopeID
			}
			snap.UsageCount++
			if usage.CreatedAt.After(snap.LatestCreatedAt) || snap.LatestEventID == "" {
				snap.LatestEventID = usage.EventID
				snap.LatestCreatedAt = usage.CreatedAt
				snap.LatestAgent = usage.Agent
				snap.LatestModel = usage.Model
				snap.LatestContextTokens = usage.ContextTokens
				snap.LatestInputTokens = usage.InputTokens
				snap.LatestOutputTokens = usage.OutputTokens
				snap.CompactionID = usage.CompactionID
				snap.LatestRunState = latestRunState[key]
			}
			snapshots[key] = snap
		}
	}
	out := make([]UsageContinuitySnapshot, 0, len(snapshots))
	for _, snap := range snapshots {
		out = append(out, snap)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].LatestCreatedAt.Equal(out[j].LatestCreatedAt) {
			return out[i].LatestCreatedAt.After(out[j].LatestCreatedAt)
		}
		if out[i].Scope != out[j].Scope {
			return out[i].Scope < out[j].Scope
		}
		return out[i].ScopeID < out[j].ScopeID
	})
	if limit <= 0 {
		limit = 20
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

type usageScope struct {
	Scope   string
	ScopeID string
}

func contextUsageScopes(usage domainai.ContextUsage) []usageScope {
	candidates := []usageScope{
		{Scope: "job", ScopeID: strings.TrimSpace(usage.JobID)},
		{Scope: "workstream", ScopeID: strings.TrimSpace(usage.WorkstreamID)},
		{Scope: "run", ScopeID: strings.TrimSpace(usage.RunID)},
		{Scope: "session", ScopeID: strings.TrimSpace(usage.SessionID)},
	}
	out := make([]usageScope, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.ScopeID != "" {
			out = append(out, candidate)
		}
	}
	return out
}

func latestRunStatesByScope(events []modulecore.EventEnvelope) map[string]string {
	out := map[string]string{}
	latest := map[string]time.Time{}
	for _, event := range events {
		for _, scope := range []usageScope{
			{Scope: "workstream", ScopeID: strings.TrimSpace(string(event.WorkstreamID))},
			{Scope: "run", ScopeID: strings.TrimSpace(string(event.RunID))},
		} {
			if scope.ScopeID == "" {
				continue
			}
			key := scope.Scope + "\x00" + scope.ScopeID
			if event.OccurredAt.After(latest[key]) || latest[key].IsZero() {
				latest[key] = event.OccurredAt
				out[key], _ = event.Payload["status"].(string)
			}
		}
	}
	return out
}

func saveAIWorkflowItem(store AIWorkflowStore, name string, save func(context.Context, AIWorkflowStore, *json.Decoder) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "ai workflow store unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := save(r.Context(), store, json.NewDecoder(r.Body)); err != nil {
			http.Error(w, "invalid "+name+" payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"status": "created"})
	}
}

func nonNilEventEnvelopes(items []modulecore.EventEnvelope) []modulecore.EventEnvelope {
	if items == nil {
		return []modulecore.EventEnvelope{}
	}
	return items
}

func nonNilProjectMemoryIndexes(items []domainai.ProjectMemoryIndex) []domainai.ProjectMemoryIndex {
	if items == nil {
		return []domainai.ProjectMemoryIndex{}
	}
	return items
}

func nonNilWorktreeRegistries(items []domainai.WorktreeRegistry) []domainai.WorktreeRegistry {
	if items == nil {
		return []domainai.WorktreeRegistry{}
	}
	return items
}

func nonNilCommandRegistries(items []domainai.CommandRegistry) []domainai.CommandRegistry {
	if items == nil {
		return []domainai.CommandRegistry{}
	}
	return items
}

func nonNilContextUsages(items []domainai.ContextUsage) []domainai.ContextUsage {
	if items == nil {
		return []domainai.ContextUsage{}
	}
	return items
}
