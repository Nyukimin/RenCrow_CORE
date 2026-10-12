package taskmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// One-shot reaper for Run-less queued Tasks.
//
// Failure knowledge: docs/調査/*Run無しqueued_Taskの滞留*_FailureKnowledge.md.
// Before Manager.CreateAndStartRun, several creation paths persisted a queued
// Task and asked for its first Run in a separate transaction. When that second
// step was refused (execution capacity) the Task stayed queued forever, because
// nothing in the runtime starts a Run-less queued Task. This file cancels the
// ones already in the store, once, through the Task owner. It is not a
// periodic reaper (YAGNI): the creation paths are fixed and no new Run-less
// queued Task appears.
//
// What it guarantees:
//   - classifyStaleQueued is a pure function. The dry-run and the
//     re-validation inside every batch transaction call the same function, so a
//     Task that changed after the dry-run (it started, gained a Run, or was
//     referenced) is skipped, never cancelled.
//   - A batch is one ExecuteIdempotentGlobalTaskOperation: all of its Tasks are
//     cancelled in one JSONL batch together with the owner receipt, or none is.
//     The state transition goes through updateStatusInTransaction, hence through
//     domaintask.CanTransition. Cancelled is not a notifying status.
//   - Idempotence rests on the status=queued re-validation inside the
//     transaction: a Task that is already cancelled is no longer a candidate, so
//     running the command twice, or again after a crash or an uncertain commit,
//     cancels each Task exactly once. The operation receipt is the durable record
//     of a batch and a second guard: a batch with the same Task set replays its
//     stored result instead of running. A new run selects again and skips what
//     is already cancelled, so it reaches a receipt only through the same
//     plan; the first guard is what makes a re-run safe.
//
// Nothing here prints or stores a title, a context or a user text; the only
// record content it looks at is the creation shape of a Task.

// ReapPolicyVersion identifies the selection rules and the request encoding. It
// is part of every operation request, so a change of the rules cannot replay a
// receipt written under the old ones.
const ReapPolicyVersion = "reap-queued/v1"

const (
	// DefaultReapMinAge is how old a queued Task must be before it is a candidate.
	DefaultReapMinAge = 24 * time.Hour
	// DefaultReapBatchSize is the number of Tasks cancelled by one transaction.
	DefaultReapBatchSize = 2000
	// MaxReapBatchSize bounds one transaction (and its owner receipt).
	MaxReapBatchSize = 5000
)

// ReapKind names an allowlisted kind of stale queued Task.
type ReapKind string

const (
	ReapKindIdleChatConversation    ReapKind = "idlechat_conversation"
	ReapKindIdleChatDialogueEpisode ReapKind = "idlechat_dialogue_episode"
	ReapKindIdleChatWordTopic       ReapKind = "idlechat_word_topic"
	ReapKindBackgroundJobFailure    ReapKind = "background_job_failure"
	ReapKindMemoryPromotion         ReapKind = "memory_promotion"
	ReapKindAtlasAcquireRunnable    ReapKind = "atlas_acquire_runnable"
	ReapKindAtlasRecover            ReapKind = "atlas_recover"
	ReapKindAtlasRevise             ReapKind = "atlas_revise"
	ReapKindLLMProviderCall         ReapKind = "llm_provider_call"
	ReapKindTTSPlaybackSession      ReapKind = "tts_playback_session"
	ReapKindDCISearch               ReapKind = "dci_search"
	ReapKindHeartbeatWorker         ReapKind = "heartbeat_worker"
)

// ReapExclusion says why a queued Task is not a candidate. A Task is counted
// under the first condition it fails, in the order of classifyStaleQueued.
type ReapExclusion string

const (
	ExcludedNotQueued          ReapExclusion = "not_queued"           // only seen by the re-validation inside a batch
	ExcludedMissing            ReapExclusion = "missing"              // only seen by the re-validation inside a batch
	ExcludedUserOrigin         ReapExclusion = "user_origin"          // origin_session/thread/turn/message_id: made for a user request
	ExcludedKindNotAllowlisted ReapExclusion = "kind_not_allowlisted" // creation shape matches no allowlisted kind
	ExcludedCreatedAfterCutoff ReapExclusion = "created_after_cutoff" // not created before --created-before
	ExcludedYoungerThanMinAge  ReapExclusion = "younger_than_min_age" // younger than --min-age
	ExcludedHasRelations       ReapExclusion = "has_relations"        // has its own parent, dependency or supersedes
	ExcludedReferenced         ReapExclusion = "referenced_by_task"   // another Task names it as parent, dependency or supersedes
	ExcludedHasRun             ReapExclusion = "has_run"              // at least one Run exists
)

// planExclusions are the reasons a selection over the queued pool can count.
func planExclusions() []ReapExclusion {
	return []ReapExclusion{
		ExcludedUserOrigin, ExcludedKindNotAllowlisted, ExcludedCreatedAfterCutoff,
		ExcludedYoungerThanMinAge, ExcludedHasRelations, ExcludedReferenced, ExcludedHasRun,
	}
}

// reapRule is the creation shape of one allowlisted kind.
//
// The Task carries no field that names the code path that created it: there is
// no kind, source or action on a Task (an Action hangs off a Run, and these
// Tasks have none). Three kinds the structured fields cannot tell apart (the
// three IdleChat Tasks share route, assignee, read_only and an empty owner;
// Background job failure and Heartbeat worker share route and an empty owner),
// so the title, which every creation site sets from a fixed template, is the
// positive key for all of them. It is never the only condition: the route,
// read_only, owner and the untouched default shape must hold as well, and a
// Task made for a user request is excluded before the title is read.
//
// The title templates are copies of the literals at the creation sites
// (Semantic Duplication, accepted for a one-shot tool: sharing a constant
// would make taskmanager import every creation site). If a site renames its
// title this tool selects fewer Tasks, never more.
type reapRule struct {
	kind         ReapKind
	titleMatches func(string) bool
	route        domaintask.Route
	readOnly     bool
	// owned is true when the creation site sets owner_id.
	owned bool
}

func titleIs(exact string) func(string) bool {
	return func(title string) bool { return title == exact }
}

func titleStartsWith(prefix string) func(string) bool {
	return func(title string) bool { return strings.HasPrefix(title, prefix) }
}

// heartbeatTitle matches "Heartbeat worker" and "Heartbeat worker (<channel>)".
func heartbeatTitle(title string) bool {
	return title == "Heartbeat worker" || (strings.HasPrefix(title, "Heartbeat worker (") && strings.HasSuffix(title, ")"))
}

// reapRules lists the allowlist. Each entry names its creation site.
var reapRules = []reapRule{
	// internal/application/idlechat: issueIdleChatRun callers.
	{ReapKindIdleChatConversation, titleIs("IdleChat conversation"), domaintask.RouteGeneral, false, false},
	{ReapKindIdleChatDialogueEpisode, titleIs("IdleChat dialogue episode"), domaintask.RouteGeneral, false, false},
	{ReapKindIdleChatWordTopic, titleIs("IdleChat word topic"), domaintask.RouteGeneral, false, false},
	// cmd/rencrow/runtime_background_jobs.go, before commit 16d106ed removed the Task.
	{ReapKindBackgroundJobFailure, titleStartsWith("Background job failure: "), domaintask.RouteOperations, false, false},
	// cmd/rencrow/memory_promotion_action_runner.go.
	{ReapKindMemoryPromotion, titleIs("Memory Promotion"), domaintask.RouteResearch, false, true},
	// internal/application/backlog/action_context.go (beginAction; the title is the action name).
	{ReapKindAtlasAcquireRunnable, titleIs("atlas_acquire_runnable"), domaintask.RouteOperations, false, true},
	{ReapKindAtlasRecover, titleIs("atlas_recover"), domaintask.RouteOperations, true, true},
	{ReapKindAtlasRevise, titleIs("atlas_revise"), domaintask.RouteOperations, false, true},
	// internal/infrastructure/llm/middleware/action.go.
	{ReapKindLLMProviderCall, titleIs("LLM provider call"), domaintask.RouteGeneral, true, true},
	// cmd/rencrow/tts_action_bridge.go.
	{ReapKindTTSPlaybackSession, titleIs("TTS playback session"), domaintask.RouteCHAT, true, true},
	// internal/adapter/viewer/dci_handler.go.
	{ReapKindDCISearch, titleIs("DCI search"), domaintask.RouteResearch, true, true},
	// internal/application/heartbeat/worker_lifecycle.go (heartbeatTaskTitle).
	{ReapKindHeartbeatWorker, heartbeatTitle, domaintask.RouteOperations, false, false},
}

func allReapKinds() []ReapKind {
	kinds := make([]ReapKind, 0, len(reapRules))
	for _, rule := range reapRules {
		kinds = append(kinds, rule.kind)
	}
	return kinds
}

// reapReason is the fixed summary written on a cancelled Task. It names the
// kind only, never a title or a text.
func reapReason(kind ReapKind) string {
	return "reaped: stale queued without run (" + string(kind) + ")"
}

// reapPolicy is what selects a Task besides its own shape.
type reapPolicy struct {
	// CreatedBefore is required: a Task created at or after it is never selected.
	CreatedBefore time.Time
	// MinAge is how old (relative to now) a Task must be.
	MinAge time.Duration
}

// staleQueuedFacts are the two facts that cannot be read from the Task itself.
type staleQueuedFacts struct {
	HasRuns          bool
	ReferencedByTask bool
}

// untouchedDefaultShape reports whether the Task still has exactly the shape a
// creation site gives it: default priority and interrupt policy, an assignee,
// and nothing that routing, assignment, a workstream, a module or an execution
// would add.
func untouchedDefaultShape(task domaintask.Task) bool {
	return task.Priority == domaintask.PriorityNormal &&
		task.InterruptPolicy == domaintask.InterruptNotifyDoneOrBlocked &&
		strings.TrimSpace(task.Assignee) != "" &&
		task.ModuleID == "" && task.ModuleRoot == "" &&
		task.RoutingEventID == "" && task.AssignmentEventID == "" &&
		task.WorkstreamID == "" && task.GoalID == "" &&
		len(task.CoderRoles) == 0 && task.AcceptedOPSClaim == nil && len(task.NativeResumeClaims) == 0 &&
		task.StartedAt == nil && task.FinishedAt == nil && task.WaitingReason == "" &&
		task.Summary == "" && len(task.NextActions) == 0 && len(task.Evidence) == 0 && len(task.Artifacts) == 0
}

func reapKindOf(task domaintask.Task) (ReapKind, bool) {
	if !untouchedDefaultShape(task) {
		return "", false
	}
	for _, rule := range reapRules {
		if !rule.titleMatches(task.Title) || task.Route != rule.route || task.ReadOnly != rule.readOnly {
			continue
		}
		if (task.OwnerID != "") != rule.owned {
			continue
		}
		return rule.kind, true
	}
	return "", false
}

func hasUserOrigin(task domaintask.Task) bool {
	return task.OriginSessionID != "" || task.OriginThreadID != "" || task.OriginTurnID != "" || task.OriginMessageID != ""
}

func hasOwnRelations(task domaintask.Task) bool {
	return task.ParentTaskID != "" || len(task.DependencyTaskIDs) > 0 || task.SupersedesTaskID != ""
}

// classifyStaleQueued decides whether task may be cancelled. It returns the
// kind and an empty exclusion for a candidate, and an empty kind and the first
// failing condition otherwise. It is pure: facts carry what the store says
// about Runs and references, and now is passed in.
func classifyStaleQueued(task domaintask.Task, facts staleQueuedFacts, policy reapPolicy, now time.Time) (ReapKind, ReapExclusion) {
	if task.Status != domaintask.StatusQueued {
		return "", ExcludedNotQueued
	}
	if hasUserOrigin(task) {
		return "", ExcludedUserOrigin
	}
	kind, ok := reapKindOf(task)
	if !ok {
		return "", ExcludedKindNotAllowlisted
	}
	if !task.CreatedAt.Before(policy.CreatedBefore) {
		return "", ExcludedCreatedAfterCutoff
	}
	if now.Sub(task.CreatedAt) < policy.MinAge {
		return "", ExcludedYoungerThanMinAge
	}
	if hasOwnRelations(task) {
		return "", ExcludedHasRelations
	}
	if facts.ReferencedByTask {
		return "", ExcludedReferenced
	}
	if facts.HasRuns {
		return "", ExcludedHasRun
	}
	return kind, ""
}

// reapCandidate is a Task the dry-run selected.
type reapCandidate struct {
	TaskID    modulecore.TaskID
	Kind      ReapKind
	CreatedAt time.Time
}

// reapPlan is the outcome of a selection.
type reapPlan struct {
	policy     reapPolicy
	scanned    int
	queued     int
	candidates []reapCandidate // oldest first, then by TaskID
	byKind     map[ReapKind]int
	excluded   map[ReapExclusion]int
}

func newReapPlan(policy reapPolicy) reapPlan {
	plan := reapPlan{policy: policy, byKind: map[ReapKind]int{}, excluded: map[ReapExclusion]int{}}
	for _, kind := range allReapKinds() {
		plan.byKind[kind] = 0
	}
	for _, reason := range planExclusions() {
		plan.excluded[reason] = 0
	}
	return plan
}

// scanTasks reads the latest version of every Task once. It is the one
// deliberate unbounded Task list of the reaper: the references to a Task can
// come from any Task in the history, and there is no reverse lookup. The caller
// holds a read or write transaction, so the scan and the checks that follow see
// the same state.
func scanTasks(ctx context.Context, store Store, visit func(domaintask.Task)) (int, error) {
	tasks, err := store.ListTasks(ctx, domaintask.Filter{})
	if err != nil {
		return 0, fmt.Errorf("scan tasks: %w", err)
	}
	for _, task := range tasks {
		visit(task)
	}
	return len(tasks), nil
}

func recordReferences(referenced map[modulecore.TaskID]struct{}, task domaintask.Task) {
	if task.ParentTaskID != "" {
		referenced[task.ParentTaskID] = struct{}{}
	}
	for _, dependency := range task.DependencyTaskIDs {
		referenced[dependency] = struct{}{}
	}
	if task.SupersedesTaskID != "" {
		referenced[task.SupersedesTaskID] = struct{}{}
	}
}

// taskHasAnyRun reads by TaskID (bounded) and counts a Run of any status.
func taskHasAnyRun(ctx context.Context, store Store, taskID modulecore.TaskID) (bool, error) {
	runs, err := store.ListRuns(ctx, domaintask.RunFilter{TaskID: taskID, Limit: 1})
	if err != nil {
		return false, fmt.Errorf("read runs of a task: %w", err)
	}
	return len(runs) > 0, nil
}

// selectStaleQueued scans the store and returns the candidates and the counts
// of every exclusion.
func selectStaleQueued(ctx context.Context, store Store, policy reapPolicy, now time.Time) (reapPlan, error) {
	plan := newReapPlan(policy)
	referenced := map[modulecore.TaskID]struct{}{}
	var queued []domaintask.Task
	scanned, err := scanTasks(ctx, store, func(task domaintask.Task) {
		recordReferences(referenced, task)
		if task.Status == domaintask.StatusQueued {
			queued = append(queued, task)
		}
	})
	if err != nil {
		return reapPlan{}, err
	}
	plan.scanned, plan.queued = scanned, len(queued)
	for _, task := range queued {
		if err := ctx.Err(); err != nil {
			return reapPlan{}, err
		}
		hasRuns, err := taskHasAnyRun(ctx, store, task.TaskID)
		if err != nil {
			return reapPlan{}, err
		}
		_, isReferenced := referenced[task.TaskID]
		kind, why := classifyStaleQueued(task, staleQueuedFacts{HasRuns: hasRuns, ReferencedByTask: isReferenced}, policy, now)
		if why != "" {
			plan.excluded[why]++
			continue
		}
		plan.byKind[kind]++
		plan.candidates = append(plan.candidates, reapCandidate{TaskID: task.TaskID, Kind: kind, CreatedAt: task.CreatedAt})
	}
	sort.Slice(plan.candidates, func(i, j int) bool {
		a, b := plan.candidates[i], plan.candidates[j]
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.TaskID < b.TaskID
	})
	return plan, nil
}

// candidateSetSHA256 digests the sorted candidate IDs, so an operator can see
// that a dry-run and an apply worked on the same set.
func candidateSetSHA256(candidates []reapCandidate) string {
	ids := make([]string, len(candidates))
	for i, candidate := range candidates {
		ids[i] = string(candidate.TaskID)
	}
	sort.Strings(ids)
	sum := sha256.Sum256([]byte(strings.Join(ids, "\n")))
	return hex.EncodeToString(sum[:])
}

// reapOperationIdentity derives the operation ID and the request hash of one
// batch. The ID names the Task set only (so the same set is the same operation
// whatever the policy flags); the hash covers the policy version, the policy
// values and the set, so the same set under other flags is a conflict instead of
// a replay.
func reapOperationIdentity(policy reapPolicy, batch []reapCandidate) (operationID, requestHash string) {
	ids := make([]string, len(batch))
	for i, candidate := range batch {
		ids[i] = string(candidate.TaskID)
	}
	sort.Strings(ids)
	set := sha256.Sum256([]byte(ReapPolicyVersion + "\n" + strings.Join(ids, "\n")))
	request := sha256.Sum256([]byte(strings.Join([]string{
		ReapPolicyVersion,
		policy.CreatedBefore.UTC().Format(time.RFC3339Nano),
		policy.MinAge.String(),
		strings.Join(ids, "\n"),
	}, "\n")))
	return "reap-queued-" + hex.EncodeToString(set[:])[:40], hex.EncodeToString(request[:])
}

// GlobalOperationOwner is the Task owner capability the reaper needs: one
// unrestricted Task transaction committed together with its idempotency
// receipt. persistence/task.JSONLStore provides it.
type GlobalOperationOwner interface {
	ExecuteIdempotentGlobalTaskOperation(
		ctx context.Context,
		operationID string,
		requestHash string,
		fn func(domaintask.Store) (json.RawMessage, error),
	) (json.RawMessage, error)
}

// ErrReapUnsupportedStore reports that the Task store cannot run a global
// idempotent operation (for example a remote storage host).
var ErrReapUnsupportedStore = errors.New("task store does not support global idempotent operations")

// reapBatchResult is the owner receipt result of one batch. It is small and
// holds counts only.
type reapBatchResult struct {
	Tasks     int                   `json:"tasks"`
	Cancelled int                   `json:"cancelled"`
	Skipped   map[ReapExclusion]int `json:"skipped,omitempty"`
}

// reapBatchInTransaction re-validates every Task of the batch against the
// state the transaction sees (the same classifyStaleQueued as the dry-run, with
// the references and Runs read again) and cancels the ones that still qualify.
// Any error rolls the whole batch back.
func (m *Manager) reapBatchInTransaction(ctx context.Context, tx Store, batch []reapCandidate, policy reapPolicy) (json.RawMessage, error) {
	now := m.now()
	referenced := map[modulecore.TaskID]struct{}{}
	if _, err := scanTasks(ctx, tx, func(task domaintask.Task) { recordReferences(referenced, task) }); err != nil {
		return nil, err
	}
	txManager := &Manager{store: tx, limits: m.limits, expectedCriteriaRevision: m.expectedCriteriaRevision, idIndexed: m.idIndexed, now: m.now}
	result := reapBatchResult{Tasks: len(batch), Skipped: map[ReapExclusion]int{}}
	for _, candidate := range batch {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		task, err := tx.GetTask(ctx, candidate.TaskID)
		if errors.Is(err, domaintask.ErrNotFound) {
			result.Skipped[ExcludedMissing]++
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("re-read a task: %w", err)
		}
		hasRuns, err := taskHasAnyRun(ctx, tx, candidate.TaskID)
		if err != nil {
			return nil, err
		}
		_, isReferenced := referenced[candidate.TaskID]
		kind, why := classifyStaleQueued(task, staleQueuedFacts{HasRuns: hasRuns, ReferencedByTask: isReferenced}, policy, now)
		if why != "" {
			result.Skipped[why]++
			continue
		}
		if _, err := txManager.updateStatusInTransaction(ctx, candidate.TaskID, domaintask.StatusCancelled, reapReason(kind), "", nil); err != nil {
			return nil, fmt.Errorf("cancel a stale queued task: %w", err)
		}
		result.Cancelled++
	}
	if len(result.Skipped) == 0 {
		result.Skipped = nil
	}
	return json.Marshal(result)
}

// ReapRequest is one run of the reaper.
type ReapRequest struct {
	// CreatedBefore is required (no default): only Tasks created before it are selected.
	CreatedBefore time.Time
	// MinAge is the minimum age of a selected Task; zero is allowed only if
	// the caller means it (the CLI defaults to DefaultReapMinAge).
	MinAge time.Duration
	// BatchSize is the number of Tasks per transaction (1..MaxReapBatchSize).
	BatchSize int
	// Apply writes. Without it the run only reports what it would cancel.
	Apply bool
}

// ReapBatchReport is the outcome of one batch transaction.
type ReapBatchReport struct {
	Ordinal         int                   `json:"ordinal"`
	OperationID     string                `json:"operation_id"`
	Tasks           int                   `json:"tasks"`
	Cancelled       int                   `json:"cancelled"`
	Skipped         int                   `json:"skipped"`
	SkippedByReason map[ReapExclusion]int `json:"skipped_by_reason,omitempty"`
	// Replayed is true when the stored receipt answered and nothing ran.
	Replayed   bool  `json:"replayed"`
	DurationMs int64 `json:"duration_ms"`
}

// ReapReport is bounded: counts, digests and operation IDs, never a title, a
// context or a Task ID.
type ReapReport struct {
	Mode               string                `json:"mode"`
	PolicyVersion      string                `json:"policy_version"`
	CreatedBefore      time.Time             `json:"created_before"`
	MinAge             string                `json:"min_age"`
	BatchSize          int                   `json:"batch_size"`
	TasksScanned       int                   `json:"tasks_scanned"`
	QueuedPool         int                   `json:"queued_pool"`
	Candidates         int                   `json:"candidates"`
	CandidatesByKind   map[ReapKind]int      `json:"candidates_by_kind"`
	Excluded           int                   `json:"excluded"`
	ExcludedByReason   map[ReapExclusion]int `json:"excluded_by_reason"`
	CandidateSetSHA256 string                `json:"candidate_set_sha256"`
	PlanDurationMs     int64                 `json:"plan_duration_ms"`
	Batches            []ReapBatchReport     `json:"batches"`
	// Applied counts the Tasks this run cancelled. AlreadyApplied counts the
	// Tasks of batches whose stored receipt answered (cancelled by an earlier run).
	Applied        int `json:"applied"`
	AlreadyApplied int `json:"already_applied"`
	// Skipped counts Tasks the batch re-validation refused to cancel because
	// they no longer qualified (changed after the plan).
	Skipped int `json:"skipped"`
}

// Validate rejects a request the reaper must not run.
func (r ReapRequest) Validate() error {
	if r.CreatedBefore.IsZero() {
		return errors.New("created-before is required: a reap has no default cutoff")
	}
	if r.MinAge < 0 {
		return errors.New("min-age must not be negative")
	}
	if r.BatchSize < 1 || r.BatchSize > MaxReapBatchSize {
		return fmt.Errorf("batch-size must be between 1 and %d", MaxReapBatchSize)
	}
	return nil
}

func (r ReapRequest) policy() reapPolicy {
	return reapPolicy{CreatedBefore: r.CreatedBefore.UTC(), MinAge: r.MinAge}
}

func newReapReport(request ReapRequest, plan reapPlan, planDuration time.Duration) ReapReport {
	mode := "dry_run"
	if request.Apply {
		mode = "apply"
	}
	excluded := 0
	for _, count := range plan.excluded {
		excluded += count
	}
	return ReapReport{
		Mode: mode, PolicyVersion: ReapPolicyVersion, CreatedBefore: request.CreatedBefore.UTC(), MinAge: request.MinAge.String(),
		BatchSize: request.BatchSize, TasksScanned: plan.scanned, QueuedPool: plan.queued, Candidates: len(plan.candidates),
		CandidatesByKind: plan.byKind, Excluded: excluded, ExcludedByReason: plan.excluded,
		CandidateSetSHA256: candidateSetSHA256(plan.candidates), PlanDurationMs: planDuration.Milliseconds(),
		Batches: []ReapBatchReport{},
	}
}

// planReap selects the candidates inside one read transaction.
func (m *Manager) planReap(ctx context.Context, policy reapPolicy) (reapPlan, error) {
	var plan reapPlan
	err := m.readTransaction(ctx, func(txManager *Manager) error {
		var err error
		plan, err = selectStaleQueued(ctx, txManager.store, policy, m.now())
		return err
	})
	return plan, err
}

// ReapStaleQueued selects the stale queued Tasks (see classifyStaleQueued) and,
// with request.Apply, cancels them in batches. The report is returned even when
// a batch fails, so the caller can say how far the run got.
func (m *Manager) ReapStaleQueued(ctx context.Context, request ReapRequest) (ReapReport, error) {
	if m == nil || m.store == nil {
		return ReapReport{}, errors.New("task manager store is unavailable")
	}
	if ctx == nil {
		return ReapReport{}, errors.New("reap context is nil")
	}
	if err := request.Validate(); err != nil {
		return ReapReport{}, err
	}
	policy := request.policy()
	started := time.Now()
	plan, err := m.planReap(ctx, policy)
	if err != nil {
		return ReapReport{}, err
	}
	report := newReapReport(request, plan, time.Since(started))
	if !request.Apply || len(plan.candidates) == 0 {
		return report, nil
	}
	return m.executeReapPlan(ctx, request, plan, report)
}

// executeReapPlan runs the batches of plan, one global operation each.
func (m *Manager) executeReapPlan(ctx context.Context, request ReapRequest, plan reapPlan, report ReapReport) (ReapReport, error) {
	owner, ok := m.store.(GlobalOperationOwner)
	if !ok {
		return report, ErrReapUnsupportedStore
	}
	for ordinal, start := 0, 0; start < len(plan.candidates); ordinal, start = ordinal+1, start+request.BatchSize {
		end := min(start+request.BatchSize, len(plan.candidates))
		batch := plan.candidates[start:end]
		operationID, requestHash := reapOperationIdentity(plan.policy, batch)
		batchStarted := time.Now()
		invoked := false
		raw, err := owner.ExecuteIdempotentGlobalTaskOperation(ctx, operationID, requestHash, func(tx domaintask.Store) (json.RawMessage, error) {
			invoked = true
			return m.reapBatchInTransaction(ctx, tx, batch, plan.policy)
		})
		if err != nil {
			return report, fmt.Errorf("batch %d (%s): %w", ordinal, operationID, err)
		}
		var result reapBatchResult
		if err := json.Unmarshal(raw, &result); err != nil {
			return report, fmt.Errorf("batch %d (%s): decode owner result: %w", ordinal, operationID, err)
		}
		skipped := 0
		for _, count := range result.Skipped {
			skipped += count
		}
		report.Batches = append(report.Batches, ReapBatchReport{
			Ordinal: ordinal, OperationID: operationID, Tasks: result.Tasks, Cancelled: result.Cancelled, Skipped: skipped,
			SkippedByReason: result.Skipped, Replayed: !invoked, DurationMs: time.Since(batchStarted).Milliseconds(),
		})
		if invoked {
			report.Applied += result.Cancelled
			report.Skipped += skipped
		} else {
			report.AlreadyApplied += result.Cancelled
		}
	}
	return report, nil
}
