package taskmanager

import (
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Failure knowledge: docs/調査/*Run無しqueued_Taskの滞留*_FailureKnowledge.md.
//
// classifyStaleQueued is the one function that decides whether a queued Task
// may be cancelled by the one-shot reaper. The dry-run and the re-validation
// inside each batch transaction both call it, so these tests pin the whole
// selection contract. The table below spells out the creation shape of every
// allowlisted kind on its own (it does not read the production rule table), so
// a change of the rules has to change a test as well.

var (
	reapTestNow    = time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	reapTestCutoff = time.Date(2026, 10, 12, 2, 39, 0, 0, time.UTC)
	reapTestOld    = time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
)

func reapTestPolicy() reapPolicy {
	return reapPolicy{CreatedBefore: reapTestCutoff, MinAge: 24 * time.Hour}
}

type reapShape struct {
	kind     ReapKind
	title    string
	route    domaintask.Route
	readOnly bool
	owner    string
	assignee string
}

// reapShapes are the creation shapes seen in the production store, one per
// allowlisted kind. Identifiers are generic placeholders.
var reapShapes = []reapShape{
	{ReapKindIdleChatConversation, "IdleChat conversation", domaintask.RouteGeneral, false, "", "Shiro"},
	{ReapKindIdleChatDialogueEpisode, "IdleChat dialogue episode", domaintask.RouteGeneral, false, "", "Mio"},
	{ReapKindIdleChatWordTopic, "IdleChat word topic", domaintask.RouteGeneral, false, "", "Shiro"},
	{ReapKindBackgroundJobFailure, "Background job failure: some_job", domaintask.RouteOperations, false, "", "shiro"},
	{ReapKindMemoryPromotion, "Memory Promotion", domaintask.RouteResearch, false, "actor-a", "actor-a"},
	{ReapKindAtlasAcquireRunnable, "atlas_acquire_runnable", domaintask.RouteOperations, false, "actor-a", "actor-a"},
	{ReapKindAtlasRecover, "atlas_recover", domaintask.RouteOperations, true, "actor-a", "actor-a"},
	{ReapKindAtlasRevise, "atlas_revise", domaintask.RouteOperations, false, "actor-a", "actor-a"},
	{ReapKindLLMProviderCall, "LLM provider call", domaintask.RouteGeneral, true, "actor-a", "actor-a"},
	{ReapKindTTSPlaybackSession, "TTS playback session", domaintask.RouteCHAT, true, "actor-a", "actor-a"},
	{ReapKindDCISearch, "DCI search", domaintask.RouteResearch, true, "user-a", "executor-a"},
	{ReapKindHeartbeatWorker, "Heartbeat worker", domaintask.RouteOperations, false, "", "worker-a"},
}

func reapTaskOf(shape reapShape, createdAt time.Time) domaintask.Task {
	return domaintask.Task{
		TaskID: modulecore.NewTaskID(), Title: shape.title, Route: shape.route, ReadOnly: shape.readOnly,
		OwnerID: shape.owner, Assignee: shape.assignee, Status: domaintask.StatusQueued,
		Priority: domaintask.PriorityNormal, InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked,
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}
}

func TestClassifyStaleQueuedAcceptsEveryAllowlistedKind(t *testing.T) {
	for _, shape := range reapShapes {
		t.Run(string(shape.kind), func(t *testing.T) {
			kind, why := classifyStaleQueued(reapTaskOf(shape, reapTestOld), staleQueuedFacts{}, reapTestPolicy(), reapTestNow)
			if why != "" || kind != shape.kind {
				t.Fatalf("classify = (%q, %q), want (%q, accepted)", kind, why, shape.kind)
			}
		})
	}
}

func TestClassifyStaleQueuedAcceptsTheTitleVariantsOfTheCreationSites(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind ReapKind
		base reapShape
		set  string
	}{
		{"heartbeat with channel", ReapKindHeartbeatWorker, reapShapes[11], "Heartbeat worker (heartbeat)"},
		{"heartbeat backlog runner", ReapKindHeartbeatWorker, reapShapes[11], "Heartbeat worker (backlog-runner)"},
		{"background job failure of another job", ReapKindBackgroundJobFailure, reapShapes[3], "Background job failure: another_job"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := reapTaskOf(tc.base, reapTestOld)
			task.Title = tc.set
			kind, why := classifyStaleQueued(task, staleQueuedFacts{}, reapTestPolicy(), reapTestNow)
			if why != "" || kind != tc.kind {
				t.Fatalf("classify = (%q, %q), want (%q, accepted)", kind, why, tc.kind)
			}
		})
	}
}

func TestClassifyStaleQueuedExcludesEachConditionWithItsReason(t *testing.T) {
	idleChat := reapShapes[0]
	memory := reapShapes[4]
	other := modulecore.NewTaskID()
	type mutation func(*domaintask.Task, *staleQueuedFacts, *reapPolicy)
	for _, tc := range []struct {
		name   string
		shape  reapShape
		mutate mutation
		want   ReapExclusion
	}{
		{"task has a Run", idleChat, func(_ *domaintask.Task, f *staleQueuedFacts, _ *reapPolicy) { f.HasRuns = true }, ExcludedHasRun},
		{"task is referenced by another Task", idleChat, func(_ *domaintask.Task, f *staleQueuedFacts, _ *reapPolicy) { f.ReferencedByTask = true }, ExcludedReferenced},
		{"created at the cutoff", idleChat, func(t *domaintask.Task, _ *staleQueuedFacts, p *reapPolicy) { t.CreatedAt = p.CreatedBefore }, ExcludedCreatedAfterCutoff},
		{"created after the cutoff", idleChat, func(t *domaintask.Task, _ *staleQueuedFacts, p *reapPolicy) {
			t.CreatedAt = p.CreatedBefore.Add(time.Second)
			t.UpdatedAt = t.CreatedAt
		}, ExcludedCreatedAfterCutoff},
		{"younger than the minimum age", idleChat, func(t *domaintask.Task, _ *staleQueuedFacts, p *reapPolicy) {
			p.CreatedBefore = reapTestNow.Add(time.Hour)
			t.CreatedAt = reapTestNow.Add(-23 * time.Hour)
			t.UpdatedAt = t.CreatedAt
		}, ExcludedYoungerThanMinAge},
		{"created in the future of now", idleChat, func(t *domaintask.Task, _ *staleQueuedFacts, p *reapPolicy) {
			p.CreatedBefore = reapTestNow.Add(48 * time.Hour)
			t.CreatedAt = reapTestNow.Add(time.Hour)
			t.UpdatedAt = t.CreatedAt
		}, ExcludedYoungerThanMinAge},
		{"title outside the allowlist", idleChat, func(t *domaintask.Task, _ *staleQueuedFacts, _ *reapPolicy) { t.Title = "something else" }, ExcludedKindNotAllowlisted},
		{"allowlisted title on another route", idleChat, func(t *domaintask.Task, _ *staleQueuedFacts, _ *reapPolicy) { t.Route = domaintask.RouteCHAT }, ExcludedKindNotAllowlisted},
		{"allowlisted title with another read_only", idleChat, func(t *domaintask.Task, _ *staleQueuedFacts, _ *reapPolicy) { t.ReadOnly = true }, ExcludedKindNotAllowlisted},
		{"owned kind without an owner", memory, func(t *domaintask.Task, _ *staleQueuedFacts, _ *reapPolicy) { t.OwnerID = "" }, ExcludedKindNotAllowlisted},
		{"unowned kind with an owner", idleChat, func(t *domaintask.Task, _ *staleQueuedFacts, _ *reapPolicy) { t.OwnerID = "actor-a" }, ExcludedKindNotAllowlisted},
		{"no assignee", idleChat, func(t *domaintask.Task, _ *staleQueuedFacts, _ *reapPolicy) { t.Assignee = "" }, ExcludedKindNotAllowlisted},
		{"non-default priority", idleChat, func(t *domaintask.Task, _ *staleQueuedFacts, _ *reapPolicy) { t.Priority = domaintask.PriorityHigh }, ExcludedKindNotAllowlisted},
		{"non-default interrupt policy", idleChat, func(t *domaintask.Task, _ *staleQueuedFacts, _ *reapPolicy) {
			t.InterruptPolicy = domaintask.InterruptSilent
		}, ExcludedKindNotAllowlisted},
		{"routed by the orchestrator", idleChat, func(t *domaintask.Task, _ *staleQueuedFacts, _ *reapPolicy) {
			t.RoutingEventID = modulecore.NewEventID()
		}, ExcludedKindNotAllowlisted},
		{"assigned by the orchestrator", idleChat, func(t *domaintask.Task, _ *staleQueuedFacts, _ *reapPolicy) {
			t.AssignmentEventID = modulecore.NewEventID()
		}, ExcludedKindNotAllowlisted},
		{"belongs to a module", idleChat, func(t *domaintask.Task, _ *staleQueuedFacts, _ *reapPolicy) { t.ModuleID = "module-a" }, ExcludedKindNotAllowlisted},
		{"belongs to a workstream", idleChat, func(t *domaintask.Task, _ *staleQueuedFacts, _ *reapPolicy) {
			t.WorkstreamID = modulecore.NewWorkstreamID()
		}, ExcludedKindNotAllowlisted},
		{"has an evidence trail", idleChat, func(t *domaintask.Task, _ *staleQueuedFacts, _ *reapPolicy) { t.Evidence = []string{"e"} }, ExcludedKindNotAllowlisted},
		{"has a summary", idleChat, func(t *domaintask.Task, _ *staleQueuedFacts, _ *reapPolicy) { t.Summary = "s" }, ExcludedKindNotAllowlisted},
		{"was started once", idleChat, func(t *domaintask.Task, _ *staleQueuedFacts, _ *reapPolicy) {
			started := t.CreatedAt.Add(time.Minute)
			t.StartedAt = &started
		}, ExcludedKindNotAllowlisted},
		{"user session origin", idleChat, func(t *domaintask.Task, _ *staleQueuedFacts, _ *reapPolicy) {
			t.OriginSessionID = modulecore.NewSessionID()
		}, ExcludedUserOrigin},
		{"user thread origin", idleChat, func(t *domaintask.Task, _ *staleQueuedFacts, _ *reapPolicy) {
			t.OriginThreadID = modulecore.NewThreadID()
		}, ExcludedUserOrigin},
		{"user turn origin", idleChat, func(t *domaintask.Task, _ *staleQueuedFacts, _ *reapPolicy) { t.OriginTurnID = modulecore.NewTurnID() }, ExcludedUserOrigin},
		{"user message origin", idleChat, func(t *domaintask.Task, _ *staleQueuedFacts, _ *reapPolicy) {
			t.OriginMessageID = modulecore.NewMessageID()
		}, ExcludedUserOrigin},
		{"has a parent", idleChat, func(t *domaintask.Task, _ *staleQueuedFacts, _ *reapPolicy) { t.ParentTaskID = other }, ExcludedHasRelations},
		{"has a dependency", idleChat, func(t *domaintask.Task, _ *staleQueuedFacts, _ *reapPolicy) {
			t.DependencyTaskIDs = []modulecore.TaskID{other}
		}, ExcludedHasRelations},
		{"supersedes another Task", idleChat, func(t *domaintask.Task, _ *staleQueuedFacts, _ *reapPolicy) { t.SupersedesTaskID = other }, ExcludedHasRelations},
		{"already running", idleChat, func(t *domaintask.Task, _ *staleQueuedFacts, _ *reapPolicy) { t.Status = domaintask.StatusRunning }, ExcludedNotQueued},
		{"already cancelled", idleChat, func(t *domaintask.Task, _ *staleQueuedFacts, _ *reapPolicy) { t.Status = domaintask.StatusCancelled }, ExcludedNotQueued},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := reapTaskOf(tc.shape, reapTestOld)
			facts := staleQueuedFacts{}
			policy := reapTestPolicy()
			tc.mutate(&task, &facts, &policy)
			kind, why := classifyStaleQueued(task, facts, policy, reapTestNow)
			if why != tc.want || kind != "" {
				t.Fatalf("classify = (%q, %q), want (\"\", %q)", kind, why, tc.want)
			}
		})
	}
}

func TestClassifyStaleQueuedReportsTheFirstFailingConditionInAFixedOrder(t *testing.T) {
	// A Task that fails several conditions is counted once, under the first of:
	// not queued, user origin, kind, cutoff, minimum age, own relations,
	// referenced, Run.
	task := reapTaskOf(reapShapes[0], reapTestOld)
	task.OriginSessionID = modulecore.NewSessionID()
	task.Title = "something else"
	task.ParentTaskID = modulecore.NewTaskID()
	facts := staleQueuedFacts{HasRuns: true, ReferencedByTask: true}
	if _, why := classifyStaleQueued(task, facts, reapTestPolicy(), reapTestNow); why != ExcludedUserOrigin {
		t.Fatalf("reason = %q, want user_origin first", why)
	}
	task.OriginSessionID = ""
	if _, why := classifyStaleQueued(task, facts, reapTestPolicy(), reapTestNow); why != ExcludedKindNotAllowlisted {
		t.Fatalf("reason = %q, want kind_not_allowlisted next", why)
	}
	task.Title = reapShapes[0].title
	if _, why := classifyStaleQueued(task, facts, reapTestPolicy(), reapTestNow); why != ExcludedHasRelations {
		t.Fatalf("reason = %q, want has_relations next", why)
	}
	task.ParentTaskID = ""
	if _, why := classifyStaleQueued(task, facts, reapTestPolicy(), reapTestNow); why != ExcludedReferenced {
		t.Fatalf("reason = %q, want referenced_by_task next", why)
	}
	facts.ReferencedByTask = false
	if _, why := classifyStaleQueued(task, facts, reapTestPolicy(), reapTestNow); why != ExcludedHasRun {
		t.Fatalf("reason = %q, want has_run last", why)
	}
}

func TestReapReasonIsAFixedCodeForEveryKindAndNeverCarriesRecordContent(t *testing.T) {
	seen := map[string]ReapKind{}
	for _, shape := range reapShapes {
		reason := reapReason(shape.kind)
		if reason != "reaped: stale queued without run ("+string(shape.kind)+")" {
			t.Fatalf("reason of %s = %q", shape.kind, reason)
		}
		if previous, dup := seen[reason]; dup {
			t.Fatalf("kinds %s and %s share the reason %q", previous, shape.kind, reason)
		}
		seen[reason] = shape.kind
	}
	if len(seen) != len(reapShapes) || len(allReapKinds()) != len(reapShapes) {
		t.Fatalf("allowlist has %d kinds, tests cover %d", len(allReapKinds()), len(reapShapes))
	}
}
