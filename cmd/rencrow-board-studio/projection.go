package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type coreRef struct {
	Owner    string `json:"owner"`
	Type     string `json:"type"`
	ID       string `json:"id"`
	Revision string `json:"revision,omitempty"`
}

func ref(kind, id string) coreRef { return coreRef{Owner: "CORE", Type: kind, ID: id} }

type studioAgent struct {
	ID                string                  `json:"agent_id"`
	ActorID           string                  `json:"actor_id,omitempty"`
	Name              string                  `json:"display_name"`
	Kind              string                  `json:"actor_kind"`
	Role              string                  `json:"role,omitempty"`
	State             string                  `json:"state"`
	HeartbeatState    string                  `json:"heartbeat_state,omitempty"`
	Availability      *agentWorkAvailability  `json:"availability,omitempty"`
	QueueCompleteness string                  `json:"queue_completeness,omitempty"`
	ActivityKind      string                  `json:"activity_kind,omitempty"`
	ActivityTitle     string                  `json:"activity_title,omitempty"`
	CurrentTasks      *[]json.RawMessage      `json:"current_tasks,omitempty"`
	TaskQueue         *[]json.RawMessage      `json:"task_queue,omitempty"`
	BoardInboxQueue   *[]agentBoardInboxItem  `json:"board_inbox_queue,omitempty"`
	ForegroundWaiters *agentForegroundWaiters `json:"foreground_waiters,omitempty"`
	TaskRefs          []coreRef               `json:"task_refs"`
	TaskStackHead     *coreRef                `json:"task_stack_head,omitempty"`
	Source            coreRef                 `json:"source"`
	Observed          *time.Time              `json:"observed_at"`
	Freshness         string                  `json:"freshness"`
}
type studioProject struct {
	ID         string          `json:"project_id"`
	Title      string          `json:"title"`
	Category   string          `json:"category"`
	State      string          `json:"state"`
	Agents     []string        `json:"assigned_agent_ids"`
	Current    []coreRef       `json:"current_step_runs"`
	NotDone    *int            `json:"not_done_count"`
	Blockers   *int            `json:"blocker_count"`
	Completion *studioProgress `json:"completion"`
	Refs       []coreRef       `json:"refs"`
	Steps      []studioStep    `json:"steps,omitempty"`
	Updated    *time.Time      `json:"updated_at,omitempty"`
}
type studioProgress struct {
	Done     int    `json:"done"`
	Total    int    `json:"total"`
	Basis    string `json:"basis"`
	Revision string `json:"revision"`
}
type studioStep struct {
	ID       string              `json:"id"`
	Title    string              `json:"title"`
	State    string              `json:"state"`
	Revision string              `json:"revision,omitempty"`
	Reason   string              `json:"reason,omitempty"`
	History  []studioStepHistory `json:"history,omitempty"`
	Source   coreRef             `json:"source"`
}
type studioStepHistory struct {
	Revision string     `json:"revision"`
	Status   string     `json:"status"`
	Occurred *time.Time `json:"occurred_at"`
	Reason   string     `json:"reason,omitempty"`
}
type studioWork struct {
	ID           string     `json:"id"`
	Project      string     `json:"project_id"`
	Step         string     `json:"step_run_id"`
	Title        string     `json:"title"`
	State        string     `json:"state"`
	Agents       []string   `json:"assigned_agent_ids"`
	Dependencies []string   `json:"depends_on"`
	Blocked      []string   `json:"blocked_by"`
	Source       coreRef    `json:"source"`
	Observed     *time.Time `json:"last_observed_at"`
	Completed    *time.Time `json:"completed_at,omitempty"`
	Priority     string     `json:"priority,omitempty"`
	Refs         []coreRef  `json:"refs,omitempty"`
}
type studioArtifact struct {
	ID       string     `json:"id"`
	Project  string     `json:"project_id"`
	Title    string     `json:"title"`
	Kind     string     `json:"kind"`
	Revision string     `json:"revision"`
	State    string     `json:"state"`
	Owner    coreRef    `json:"owner_ref"`
	Evidence []coreRef  `json:"evidence_refs"`
	Created  *time.Time `json:"created_at,omitempty"`
}
type studioBlocker struct {
	ID       string     `json:"id"`
	Work     string     `json:"work_item_id"`
	Class    string     `json:"classification"`
	Reason   *string    `json:"reason"`
	Since    *time.Time `json:"since"`
	Evidence []coreRef  `json:"evidence_refs"`
}
type studioActivity struct {
	ID       string    `json:"id"`
	Occurred time.Time `json:"occurred_at"`
	Kind     string    `json:"type"`
	Summary  string    `json:"summary"`
	Actor    string    `json:"actor_id,omitempty"`
	Source   coreRef   `json:"source_ref"`
}
type studioSnapshot struct {
	Version             string            `json:"schema_version"`
	ID                  string            `json:"snapshot_id"`
	Observed            time.Time         `json:"observed_at"`
	AgentWorkObservedAt *time.Time        `json:"agent_work_observed_at,omitempty"`
	SourceHealth        map[string]string `json:"source_health"`
	Agents              []studioAgent     `json:"agents"`
	Projects            []studioProject   `json:"projects"`
	Work                []studioWork      `json:"work_items"`
	Artifacts           []studioArtifact  `json:"artifacts"`
	Blockers            []studioBlocker   `json:"blockers"`
	Activities          []studioActivity  `json:"activities"`
}

type viewerAgent struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	Task    string `json:"task_id"`
	Updated string `json:"updated_at"`
}
type viewerAgents struct {
	Agents []viewerAgent `json:"agents"`
}

type viewerAgentWork struct {
	ObservedAt time.Time              `json:"observed_at"`
	Actors     []viewerAgentWorkActor `json:"actors"`
}

type agentWorkAvailability struct {
	Heartbeat         string `json:"heartbeat"`
	Tasks             string `json:"tasks"`
	RunQueue          string `json:"run_queue"`
	BoardInbox        string `json:"board_inbox"`
	ForegroundWaiters string `json:"foreground_waiters"`
}

type agentForegroundWaiters struct {
	Count int                     `json:"count"`
	Items []agentForegroundWaiter `json:"items"`
}

type agentForegroundWaiter struct {
	Position int    `json:"position"`
	Priority string `json:"priority"`
}

type agentBoardInboxItem struct {
	WorkID    string     `json:"work_id"`
	Title     string     `json:"title"`
	Priority  string     `json:"priority"`
	CreatedAt time.Time  `json:"created_at"`
	NotBefore *time.Time `json:"not_before,omitempty"`
}

type viewerAgentWorkActor struct {
	ActorID           string                  `json:"actor_id"`
	HeartbeatState    string                  `json:"heartbeat_state"`
	Availability      agentWorkAvailability   `json:"availability"`
	QueueCompleteness string                  `json:"queue_completeness"`
	ActivityKind      string                  `json:"activity_kind,omitempty"`
	ActivityTitle     string                  `json:"activity_title,omitempty"`
	CurrentTasks      []json.RawMessage       `json:"current_tasks"`
	WaitingQueue      []json.RawMessage       `json:"waiting_queue"`
	BoardInboxQueue   []agentBoardInboxItem   `json:"board_inbox_queue"`
	ForegroundWaiters *agentForegroundWaiters `json:"foreground_waiters"`
}
type viewerTask struct {
	ID           string     `json:"task_id"`
	Project      string     `json:"workstream_id"`
	Title        string     `json:"title"`
	State        string     `json:"status"`
	Agent        string     `json:"assignee"`
	Priority     string     `json:"priority"`
	Dependencies []string   `json:"dependency_task_ids"`
	Updated      *time.Time `json:"updated_at"`
	Finished     *time.Time `json:"finished_at"`
	Publication  struct {
		Audience string `json:"audience"`
	} `json:"origin_publication"`
}
type viewerTasks struct {
	Items []viewerTask `json:"items"`
}

type viewerProject struct {
	ID      string     `json:"workstream_id"`
	Title   string     `json:"name"`
	State   string     `json:"status"`
	Agent   string     `json:"primary_agent"`
	Updated *time.Time `json:"updated_at"`
}
type viewerGoal struct {
	ID        string     `json:"goal_id"`
	Project   string     `json:"workstream_id"`
	Title     string     `json:"title"`
	State     string     `json:"status"`
	Created   *time.Time `json:"created_at"`
	Completed *time.Time `json:"completed_at"`
}
type viewerArtifact struct {
	ID      string     `json:"artifact_id"`
	Project string     `json:"workstream_id"`
	Title   string     `json:"title"`
	Kind    string     `json:"artifact_type"`
	State   string     `json:"status"`
	Created *time.Time `json:"created_at"`
}
type viewerWorkstreams struct {
	Workstreams []viewerProject  `json:"workstreams"`
	Goals       []viewerGoal     `json:"goals"`
	Artifacts   []viewerArtifact `json:"artifacts"`
}
type viewerAtlas struct {
	Current       []atlasItem     `json:"current"`
	Backlog       []atlasItem     `json:"backlog"`
	Pipeline      []atlasPipeline `json:"pipeline"`
	Closures      []atlasClosure  `json:"closure_receipts"`
	StageReceipts []atlasReceipt  `json:"stage_receipts"`
}
type atlasItem struct {
	ID       string `json:"backlog_item_id"`
	Project  string `json:"workstream_id"`
	Unit     string `json:"implementation_unit_id"`
	Revision int    `json:"implementation_revision"`
	Title    string `json:"title"`
	Updated  string `json:"updated_at"`
}
type atlasPipeline struct {
	ID               string        `json:"unit_id"`
	Item             string        `json:"backlog_item_id"`
	Revision         int           `json:"implementation_revision"`
	State            string        `json:"delivery_state"`
	InvalidatedStage string        `json:"invalidated_from_stage"`
	Stages           []atlasStage  `json:"stages"`
	Freezes          []atlasFreeze `json:"queue_freezes"`
	ActiveLease      *atlasLease   `json:"active_lease,omitempty"`
}
type atlasStage struct {
	Code   string `json:"stage"`
	Title  string `json:"name"`
	State  string `json:"status"`
	Reason string `json:"reason"`
}
type atlasFreeze struct {
	ID            string     `json:"freeze_id"`
	BlockedUnitID string     `json:"blocked_unit_id"`
	Revision      int        `json:"blocked_revision"`
	Reason        string     `json:"reason_code"`
	State         string     `json:"status"`
	Created       *time.Time `json:"created_at"`
}
type atlasLease struct {
	HolderUnitID string `json:"holder_unit_id"`
	Stage        string `json:"stage"`
	Revision     string `json:"revision"`
}
type atlasReceipt struct {
	ID       string     `json:"receipt_id"`
	Unit     string     `json:"unit_id"`
	Item     string     `json:"backlog_item_id"`
	Revision int        `json:"implementation_revision"`
	Stage    string     `json:"target_stage"`
	Status   string     `json:"status"`
	Reason   string     `json:"reason_code,omitempty"`
	Created  *time.Time `json:"created_at,omitempty"`
	Complete *time.Time `json:"completed_at,omitempty"`
}
type atlasClosure struct {
	Artifact string `json:"artifact_id"`
	Unit     string `json:"unit_id"`
	Revision int    `json:"implementation_revision"`
	State    string `json:"status"`
	Target   string `json:"target_delivery_state"`
}

var coreActors = map[string]string{"mio": "Mio", "shiro": "Shiro", "kuro": "Kuro", "midori": "Midori",
	"aka": "Aka", "ao": "Ao", "kin": "Kin", "gin": "Gin",
	"coder1": "Coder 1", "coder2": "Coder 2", "coder3": "Coder 3", "coder4": "Coder 4"}
var coreRoles = map[string]string{"mio": "Chat", "shiro": "Worker", "kuro": "Chat", "midori": "Chat",
	"aka": "エージェント", "ao": "エージェント", "kin": "エージェント", "gin": "エージェント",
	"coder1": "Coder", "coder2": "Coder", "coder3": "Coder", "coder4": "Coder"}
var canonicalActorIDs = []string{"mio", "shiro", "kuro", "midori", "aka", "ao", "kin", "gin"}
var legacyAgentIDs = map[string]bool{"coder1": true, "coder2": true, "coder3": true, "coder4": true}

const boardInboxGenericTitle = "BOARD inbox instruction"

var taskStates = map[string]string{"queued": "queued", "running": "working", "waiting": "waiting", "blocked": "blocked",
	"failed": "failed", "succeeded": "done", "cancelled": "canceled", "superseded": "canceled"}
var projectStates = map[string]string{"draft": "planned", "active": "active", "paused": "waiting", "waiting": "waiting",
	"completed": "done", "archived": "archived"}
var goalStates = map[string]string{"draft": "planned", "active": "active", "waiting": "waiting", "completed": "done",
	"archived": "archived", "blocked": "blocked", "failed": "failed", "canceled": "canceled", "cancelled": "canceled"}
var stepStates = map[string]string{"pending": "queued", "active": "active", "passed": "done", "blocked": "blocked", "failed": "failed"}
var monitorStates = map[string]string{"running": "working", "working": "working", "active": "working", "busy": "working",
	"thinking": "thinking", "idle": "idle", "online": "idle", "offline": "unavailable", "unavailable": "unavailable", "error": "failed"}

func mapped(lookup map[string]string, key string) string {
	if value := lookup[key]; value != "" {
		return value
	}
	return "unknown"
}

func date(value string) *time.Time {
	at, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || at.IsZero() {
		return nil
	}
	return &at
}

func timestamp(at *time.Time) *time.Time {
	if at == nil || at.IsZero() {
		return nil
	}
	return at
}

func assignee(id string) []string {
	if coreActors[id] == "" {
		return []string{}
	}
	return []string{id}
}

func validAgentWork(work viewerAgentWork) bool {
	if work.ObservedAt.IsZero() || len(work.Actors) != len(canonicalActorIDs) {
		return false
	}
	want := make(map[string]bool, len(canonicalActorIDs))
	for _, id := range canonicalActorIDs {
		want[id] = true
	}
	seen := make(map[string]bool, len(work.Actors))
	for _, actor := range work.Actors {
		if !want[actor.ActorID] || seen[actor.ActorID] ||
			!validAvailability(actor.Availability.Heartbeat) || !validAvailability(actor.Availability.Tasks) ||
			!validAvailability(actor.Availability.RunQueue) ||
			!validConfiguredAvailability(actor.Availability.BoardInbox) ||
			!validConfiguredAvailability(actor.Availability.ForegroundWaiters) ||
			(actor.QueueCompleteness != "complete" && actor.QueueCompleteness != "incomplete") {
			return false
		}
		if actor.QueueCompleteness == "complete" &&
			(actor.Availability.Tasks != "available" || actor.Availability.RunQueue != "available" ||
				actor.Availability.BoardInbox != "available" || actor.Availability.ForegroundWaiters != "available") {
			return false
		}
		if actor.Availability.Tasks == "available" && (actor.CurrentTasks == nil || actor.WaitingQueue == nil) {
			return false
		}
		if actor.Availability.Tasks == "unavailable" && (actor.CurrentTasks != nil || actor.WaitingQueue != nil) {
			return false
		}
		if actor.Availability.BoardInbox == "available" && actor.BoardInboxQueue == nil {
			return false
		}
		if actor.Availability.BoardInbox != "available" && actor.BoardInboxQueue != nil {
			return false
		}
		if actor.Availability.ForegroundWaiters == "available" &&
			(actor.ForegroundWaiters == nil || actor.ForegroundWaiters.Items == nil || actor.ForegroundWaiters.Count != len(actor.ForegroundWaiters.Items)) {
			return false
		}
		if actor.Availability.ForegroundWaiters != "available" && actor.ForegroundWaiters != nil {
			return false
		}
		for _, row := range append(append([]json.RawMessage(nil), actor.CurrentTasks...), actor.WaitingQueue...) {
			var task struct {
				ID        string `json:"task_id"`
				Title     string `json:"title"`
				Status    string `json:"status"`
				QueueKind string `json:"queue_class"`
			}
			if err := json.Unmarshal(row, &task); err != nil || task.ID == "" || task.Title == "" || task.Status == "" || task.QueueKind == "" {
				return false
			}
		}
		for _, item := range actor.BoardInboxQueue {
			if item.WorkID == "" || item.Title != boardInboxGenericTitle ||
				item.Priority == "" || item.CreatedAt.IsZero() {
				return false
			}
		}
		seen[actor.ActorID] = true
	}
	return len(seen) == len(want)
}

func validAvailability(value string) bool { return value == "available" || value == "unavailable" }

func validConfiguredAvailability(value string) bool {
	return validAvailability(value) || value == "not_configured"
}

func projectAgentWork(snapshot *studioSnapshot, work viewerAgentWork) {
	legacy := make([]studioAgent, 0, len(legacyAgentIDs))
	for _, agent := range snapshot.Agents {
		if legacyAgentIDs[agent.ID] {
			agent.Kind = "legacy"
			agent.ActorID = ""
			agent.Availability = nil
			agent.QueueCompleteness = ""
			agent.CurrentTasks = nil
			agent.TaskQueue = nil
			agent.BoardInboxQueue = nil
			agent.ForegroundWaiters = nil
			legacy = append(legacy, agent)
		}
	}
	snapshot.Agents = make([]studioAgent, 0, len(work.Actors)+len(legacy))
	for _, actor := range work.Actors {
		state := mapped(monitorStates, actor.HeartbeatState)
		if actor.Availability.Heartbeat == "unavailable" {
			state = "unavailable"
		}
		agent := studioAgent{
			ID: actor.ActorID, ActorID: actor.ActorID, Name: coreActors[actor.ActorID], Kind: "core", Role: coreRoles[actor.ActorID],
			State: state, HeartbeatState: actor.HeartbeatState, Availability: &actor.Availability,
			QueueCompleteness: actor.QueueCompleteness,
			ActivityKind:      actor.ActivityKind, ActivityTitle: actor.ActivityTitle,
			TaskRefs: []coreRef{}, Source: ref("agent_work", actor.ActorID), Observed: &work.ObservedAt, Freshness: "unknown",
		}
		if actor.Availability.Tasks == "unavailable" {
			// The CORE response uses null when Task data is unavailable. Omit the
			// arrays so the UI cannot mistake unknown work for an empty queue.
		} else {
			current := append([]json.RawMessage{}, actor.CurrentTasks...)
			queue := append([]json.RawMessage{}, actor.WaitingQueue...)
			agent.CurrentTasks = &current
			agent.TaskQueue = &queue
			agent.TaskRefs = []coreRef{}
			agent.TaskStackHead = nil
			for _, row := range actor.CurrentTasks {
				var task struct {
					ID string `json:"task_id"`
				}
				if json.Unmarshal(row, &task) == nil && task.ID != "" {
					taskRef := ref("task", task.ID)
					agent.TaskRefs = append(agent.TaskRefs, taskRef)
					if agent.TaskStackHead == nil {
						head := taskRef
						agent.TaskStackHead = &head
					}
				}
			}
		}
		if actor.Availability.BoardInbox == "available" {
			inbox := append([]agentBoardInboxItem{}, actor.BoardInboxQueue...)
			agent.BoardInboxQueue = &inbox
		}
		if actor.Availability.ForegroundWaiters == "available" {
			waiters := agentForegroundWaiters{
				Count: actor.ForegroundWaiters.Count,
				Items: append([]agentForegroundWaiter{}, actor.ForegroundWaiters.Items...),
			}
			agent.ForegroundWaiters = &waiters
		}
		snapshot.Agents = append(snapshot.Agents, agent)
	}
	snapshot.Agents = append(snapshot.Agents, legacy...)
}

func markAgentWorkUnavailable(snapshot *studioSnapshot) {
	legacy := make([]studioAgent, 0, len(legacyAgentIDs))
	for _, agent := range snapshot.Agents {
		if legacyAgentIDs[agent.ID] {
			agent.Kind = "legacy"
			agent.ActorID = ""
			agent.Availability = nil
			agent.QueueCompleteness = ""
			agent.CurrentTasks = nil
			agent.TaskQueue = nil
			agent.BoardInboxQueue = nil
			agent.ForegroundWaiters = nil
			legacy = append(legacy, agent)
		}
	}
	snapshot.Agents = make([]studioAgent, 0, len(canonicalActorIDs)+len(legacy))
	for _, actorID := range canonicalActorIDs {
		snapshot.Agents = append(snapshot.Agents, studioAgent{
			ID: actorID, ActorID: actorID, Name: coreActors[actorID], Kind: "core", Role: coreRoles[actorID], State: "unavailable",
			HeartbeatState: "unavailable", Availability: &agentWorkAvailability{
				Heartbeat: "unavailable", Tasks: "unavailable", RunQueue: "unavailable",
				BoardInbox: "unavailable", ForegroundWaiters: "unavailable",
			},
			QueueCompleteness: "incomplete",
			TaskRefs:          []coreRef{}, Source: ref("agent_work", actorID), Freshness: "unknown",
		})
	}
	snapshot.Agents = append(snapshot.Agents, legacy...)
}

func revision(value int) string {
	if value > 0 {
		return strconv.Itoa(value)
	}
	return ""
}

func fallback(name, otherwise string) string {
	if strings.TrimSpace(name) == "" {
		return otherwise
	}
	return name
}

func terminal(state string) bool {
	return state == "done" || state == "canceled" || state == "archived"
}

func projectStudio(observed time.Time, agents viewerAgents, tasks viewerTasks, work viewerWorkstreams, atlas viewerAtlas) studioSnapshot {
	snapshot := studioSnapshot{Version: "1.0", ID: fmt.Sprintf("core-view-%x", sha256.Sum256([]byte(observed.Format(time.RFC3339Nano)))),
		Observed: observed, Agents: []studioAgent{}, Projects: []studioProject{}, Work: []studioWork{}, Artifacts: []studioArtifact{},
		Blockers: []studioBlocker{}, Activities: []studioActivity{}}
	projectIndex := map[string]int{}
	for _, row := range work.Workstreams {
		if row.ID == "" {
			continue
		}
		projectIndex[row.ID] = len(snapshot.Projects)
		snapshot.Projects = append(snapshot.Projects, studioProject{ID: row.ID, Title: fallback(row.Title, row.ID), Category: "Workstream",
			State: mapped(projectStates, row.State), Agents: assignee(row.Agent), Current: []coreRef{}, Refs: []coreRef{ref("workstream", row.ID)},
			Updated: timestamp(row.Updated)})
	}
	snapshot.addAtlas(atlas, projectIndex)

	publicTasks := make(map[string]viewerTask)
	for _, row := range tasks.Items {
		if row.ID != "" && row.Publication.Audience == "public" {
			publicTasks[row.ID] = row
		}
	}
	seenTasks := map[string]bool{}
	for _, row := range tasks.Items {
		if row.ID == "" || row.Publication.Audience != "public" || seenTasks[row.ID] {
			continue
		}
		seenTasks[row.ID] = true
		projectID := ""
		if _, ok := projectIndex[row.Project]; ok {
			projectID = row.Project
		}
		state := mapped(taskStates, row.State)
		task := studioWork{ID: row.ID, Project: projectID, Title: fallback(row.Title, row.ID), State: state, Agents: assignee(row.Agent),
			Source: ref("task", row.ID), Observed: timestamp(row.Updated), Priority: row.Priority, Dependencies: []string{}, Blocked: []string{}}
		if state == "done" {
			task.Completed = timestamp(row.Finished)
		}
		for _, id := range row.Dependencies {
			if _, ok := publicTasks[id]; ok {
				task.Dependencies = append(task.Dependencies, id)
			}
		}
		if state == "blocked" {
			id := "task-block:" + row.ID
			task.Blocked = append(task.Blocked, id)
			snapshot.Blockers = append(snapshot.Blockers, studioBlocker{ID: id, Work: row.ID, Class: "blocked", Evidence: []coreRef{task.Source}})
		}
		snapshot.Work = append(snapshot.Work, task)
		// A terminal record uses finished_at, while an active event uses the observed task revision.
		if terminal(state) || state == "failed" {
			snapshot.addActivity("task_"+state, row.Title, row.Agent, row.Finished, task.Source)
		}
	}

	for _, row := range work.Goals {
		idx, ok := projectIndex[row.Project]
		if !ok || row.ID == "" {
			continue
		}
		state := mapped(goalStates, row.State)
		if state == "archived" {
			continue
		}
		project := snapshot.Projects[idx]
		snapshot.Work = append(snapshot.Work, studioWork{ID: row.ID, Project: row.Project, Title: fallback(row.Title, row.ID),
			State: state, Agents: project.Agents, Source: ref("workstream_goal", row.ID), Dependencies: []string{}, Blocked: []string{},
			Completed: timestamp(row.Completed)})
	}
	seenAgentIDs := map[string]bool{}
	for _, row := range agents.Agents {
		actorID := row.ID
		name := coreActors[actorID]
		if name == "" {
			continue
		}
		if seenAgentIDs[actorID] {
			continue
		}
		seenAgentIDs[actorID] = true
		state := mapped(monitorStates, row.State)
		at := date(row.Updated)
		freshness := "unknown"
		if at != nil {
			if age := observed.Sub(*at); age >= -5*time.Second && age <= 90*time.Second {
				freshness = "fresh"
			} else {
				freshness = "stale"
			}
		}
		if row.State == "offline" && at != nil {
			freshness = "stale"
		}
		agent := studioAgent{ID: actorID, Name: name, Kind: "core", Role: coreRoles[actorID], State: state, TaskRefs: []coreRef{},
			Source: ref("agent_monitor", actorID), Observed: at, Freshness: freshness}
		if task, ok := publicTasks[row.Task]; ok && task.Agent == actorID && task.State == "running" && (state == "working" || state == "thinking") {
			agent.TaskRefs = append(agent.TaskRefs, ref("task", task.ID))
		}
		row.ID = actorID
		agent.TaskStackHead = resolveTaskStackHead(row, state, freshness, publicTasks, snapshot.Work)
		snapshot.Agents = append(snapshot.Agents, agent)
	}
	snapshot.addArtifacts(work.Artifacts, atlas, projectIndex)
	snapshot.countProjectRows(projectIndex)
	sort.SliceStable(snapshot.Projects, func(i, j int) bool {
		a, b := snapshot.Projects[i], snapshot.Projects[j]
		rank := func(s string) int {
			switch s {
			case "active":
				return 0
			case "blocked":
				return 1
			case "waiting", "queued":
				return 2
			case "done":
				return 3
			}
			return 4
		}
		if rank(a.State) != rank(b.State) {
			return rank(a.State) < rank(b.State)
		}
		if a.Updated != nil && b.Updated != nil {
			return a.Updated.After(*b.Updated)
		}
		return a.Updated != nil
	})
	return snapshot
}

func resolveTaskStackHead(agent viewerAgent, state, freshness string, publicTasks map[string]viewerTask, workItems []studioWork) *coreRef {
	agentID := agent.ID
	preferredTask := ""
	if freshness == "fresh" && (state == "working" || state == "thinking") {
		if task, ok := publicTasks[agent.Task]; ok && task.Agent == agentID && task.State == "running" {
			preferredTask = task.ID
		}
	}
	var firstRunning, firstOther *coreRef
	for _, task := range workItems {
		if task.Source.Type != "task" || len(task.Agents) != 1 || task.Agents[0] != agentID {
			continue
		}
		head := task.Source
		switch task.State {
		case "working":
			if task.ID == preferredTask {
				return &head
			}
			if firstRunning == nil {
				firstRunning = &head
			}
		case "queued", "waiting", "blocked":
			if firstOther == nil {
				firstOther = &head
			}
		}
	}
	if firstRunning != nil {
		return firstRunning
	}
	return firstOther
}

func (s *studioSnapshot) addActivity(kind, title, actor string, at *time.Time, source coreRef) {
	if timestamp(at) == nil || title == "" {
		return
	}
	if coreActors[actor] == "" {
		actor = ""
	}
	s.Activities = append(s.Activities, studioActivity{ID: kind + ":" + source.ID, Occurred: *at, Kind: kind, Summary: title, Actor: actor, Source: source})
}

func (s *studioSnapshot) countProjectRows(index map[string]int) {
	pending := map[string]int{}
	projectByWork := map[string]string{}
	for _, row := range s.Work {
		if row.Project == "" {
			continue
		}
		projectByWork[row.ID] = row.Project
		if !terminal(row.State) {
			pending[row.Project]++
		}
	}
	blocked := map[string]int{}
	for _, row := range s.Blockers {
		if project := projectByWork[row.Work]; project != "" {
			blocked[project]++
		}
	}
	for id, i := range index {
		p, b := pending[id], blocked[id]
		s.Projects[i].NotDone = &p
		s.Projects[i].Blockers = &b
	}
}

func safeReasonCode(value string) string {
	value = strings.TrimSpace(value)
	switch value {
	case "stage_failed", "worker_failure", "step18_e2e_stage_failed":
		return value
	default:
		return ""
	}
}

func sanitizeAtlasReason(value string) string {
	if code := safeReasonCode(value); code != "" {
		return code
	}
	// Atlas currently derives these fixed closure failures from CORE lifecycle
	// validation. Reduce them to stable display text and never expose IDs embedded
	// in the full error string.
	switch {
	case strings.Contains(value, "terminal item is missing canonical lifecycle context"):
		return "完了工程のライフサイクル情報が不足"
	case strings.Contains(value, "terminal item has no persisted lifecycle action binding"):
		return "完了工程の操作記録が不足"
	case strings.Contains(value, "has invalid action_id"):
		return "完了工程の操作IDが不正"
	case strings.Contains(value, "persisted closure action_id mismatch"):
		return "完了工程の操作IDが不一致"
	default:
		return ""
	}
}

func normalizeAtlasStage(value string) string {
	normalized := strings.ToUpper(strings.TrimSpace(value))
	normalized = strings.NewReplacer("-", "_", " ", "_").Replace(normalized)
	switch normalized {
	case "SPEC", "SPECIFICATION":
		return "SPEC"
	case "TDD_RED":
		return "TDD_RED"
	case "TDD_GREEN":
		return "TDD_GREEN"
	case "REFACTOR":
		return "REFACTOR"
	case "E2E", "E2E_PREDEPLOY":
		return "E2E_PREDEPLOY"
	case "BUILD":
		return "BUILD"
	case "DEPLOY":
		return "DEPLOY"
	case "RESTART":
		return "RESTART"
	case "VERIFY", "POST_DEPLOY_VERIFY":
		return "POST_DEPLOY_VERIFY"
	case "LIVE_VERIFIED":
		return "LIVE_VERIFIED"
	case "DONE":
		return "DONE"
	case "QUEUED":
		return "QUEUED"
	default:
		return ""
	}
}

func atlasBlockedStop(unit atlasPipeline) (stageIndex int, queueGate, ambiguous bool) {
	if !strings.EqualFold(strings.TrimSpace(unit.State), "BLOCKED") {
		return -1, false, false
	}
	rawInvalidated := strings.TrimSpace(unit.InvalidatedStage)
	invalidated := normalizeAtlasStage(rawInvalidated)
	if rawInvalidated != "" {
		if invalidated == "QUEUED" {
			return -1, true, false
		}
		if invalidated == "" {
			return -1, false, true
		}
		for i, stage := range unit.Stages {
			if normalizeAtlasStage(stage.Code) == invalidated && strings.EqualFold(strings.TrimSpace(stage.State), "blocked") {
				return i, false, false
			}
		}
		return -1, false, true
	}
	if lease := unit.ActiveLease; lease != nil && strings.TrimSpace(lease.HolderUnitID) == unit.ID {
		leaseRevision := strings.TrimSpace(lease.Revision)
		revisionMatches := leaseRevision == ""
		if leaseRevision != "" {
			parsed, err := strconv.Atoi(leaseRevision)
			revisionMatches = err == nil && parsed == unit.Revision
		}
		leaseStage := normalizeAtlasStage(lease.Stage)
		if revisionMatches && leaseStage != "" && leaseStage != "QUEUED" {
			for i, stage := range unit.Stages {
				state := strings.ToLower(strings.TrimSpace(stage.State))
				if normalizeAtlasStage(stage.Code) == leaseStage && (state == "blocked" || state == "active" || state == "failed") {
					return i, false, false
				}
			}
		}
	}
	blocked := -1
	count := 0
	for i, stage := range unit.Stages {
		if strings.EqualFold(strings.TrimSpace(stage.State), "blocked") {
			blocked = i
			count++
		}
	}
	if count == 1 {
		return blocked, false, false
	}
	if count > 1 {
		return -1, false, true
	}
	return -1, false, false
}

func latestActiveAtlasFreeze(unit atlasPipeline) *atlasFreeze {
	var latest *atlasFreeze
	for i := range unit.Freezes {
		freeze := &unit.Freezes[i]
		if strings.TrimSpace(freeze.ID) == "" || strings.TrimSpace(freeze.BlockedUnitID) != unit.ID ||
			freeze.Revision != unit.Revision || strings.EqualFold(strings.TrimSpace(freeze.State), "resolved") {
			continue
		}
		if latest == nil || (freeze.Created != nil && (latest.Created == nil || freeze.Created.After(*latest.Created))) ||
			(freeze.Created != nil && latest.Created != nil && freeze.Created.Equal(*latest.Created) && freeze.ID < latest.ID) {
			latest = freeze
		}
	}
	return latest
}

func atlasReceiptHistory(unit atlasPipeline, lineage map[string]bool, stageCode string, receipts []atlasReceipt) []studioStepHistory {
	type orderedHistory struct {
		entry     studioStepHistory
		receiptID string
	}
	ordered := make([]orderedHistory, 0)
	for _, receipt := range receipts {
		id := strings.TrimSpace(receipt.ID)
		if id == "" || strings.TrimSpace(receipt.Unit) != unit.ID || normalizeAtlasStage(receipt.Stage) != stageCode || receipt.Revision < 1 {
			continue
		}
		if itemID := strings.TrimSpace(receipt.Item); itemID != "" && !lineage[itemID] {
			continue
		}
		status := strings.ToLower(strings.TrimSpace(receipt.Status))
		if status != "completed" && status != "prepared" && status != "failed" {
			continue
		}
		occurred := timestamp(receipt.Complete)
		if occurred == nil {
			occurred = timestamp(receipt.Created)
		}
		entry := studioStepHistory{Revision: revision(receipt.Revision), Status: status, Occurred: occurred}
		if status == "failed" {
			entry.Reason = safeReasonCode(receipt.Reason)
		}
		ordered = append(ordered, orderedHistory{entry: entry, receiptID: id})
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.entry.Occurred != nil && b.entry.Occurred != nil && !a.entry.Occurred.Equal(*b.entry.Occurred) {
			return a.entry.Occurred.Before(*b.entry.Occurred)
		}
		if a.entry.Occurred == nil && b.entry.Occurred != nil {
			return false
		}
		if a.entry.Occurred != nil && b.entry.Occurred == nil {
			return true
		}
		return a.receiptID < b.receiptID
	})
	history := make([]studioStepHistory, 0, len(ordered))
	for _, entry := range ordered {
		history = append(history, entry.entry)
	}
	return history
}

func (s *studioSnapshot) addAtlas(atlas viewerAtlas, index map[string]int) {
	items := map[string]atlasItem{}
	for _, row := range append(atlas.Backlog, atlas.Current...) {
		if previous, ok := items[row.ID]; !ok || row.Revision >= previous.Revision {
			items[row.ID] = row
		}
	}
	byProject := map[string][]atlasPipeline{}
	for _, unit := range atlas.Pipeline {
		item, ok := items[unit.Item]
		if !ok || unit.ID == "" || item.Unit != unit.ID || item.Revision != unit.Revision {
			continue
		}
		if _, ok := index[item.Project]; ok {
			byProject[item.Project] = append(byProject[item.Project], unit)
		}
	}
	for projectID, units := range byProject {
		if len(units) != 1 {
			continue
		}
		unit := units[0]
		item := items[unit.Item]
		project := &s.Projects[index[projectID]]
		project.Category = "Atlas"
		project.Refs = append(project.Refs, ref("atlas_item", item.ID))
		if at := date(item.Updated); at != nil && (project.Updated == nil || at.After(*project.Updated)) {
			project.Updated = at
		}
		unitRef := ref("atlas_unit", unit.ID)
		unitRef.Revision = revision(unit.Revision)
		project.Refs = append(project.Refs, unitRef)
		lineage := map[string]bool{unit.Item: true}
		for _, row := range append(append([]atlasItem(nil), atlas.Backlog...), atlas.Current...) {
			if row.Unit == unit.ID && row.ID != "" {
				lineage[row.ID] = true
			}
		}
		for _, candidate := range atlas.Pipeline {
			if candidate.ID == unit.ID && candidate.Item != "" {
				lineage[candidate.Item] = true
			}
		}
		blockedUnit := strings.EqualFold(strings.TrimSpace(unit.State), "BLOCKED")
		stopIndex, queueGate, ambiguous := -1, false, false
		if blockedUnit {
			stopIndex, queueGate, ambiguous = atlasBlockedStop(unit)
		}
		freeze := latestActiveAtlasFreeze(unit)
		freezeReason := ""
		if freeze != nil {
			freezeReason = safeReasonCode(freeze.Reason)
		}
		gateReason := freezeReason
		if gateReason == "" && len(unit.Stages) > 0 {
			gateReason = sanitizeAtlasReason(unit.Stages[0].Reason)
		}
		stopReason := ""
		if stopIndex >= 0 && stopIndex < len(unit.Stages) {
			stopReason = sanitizeAtlasReason(unit.Stages[stopIndex].Reason)
		}
		if stopReason == "" {
			stopReason = freezeReason
		}
		passed := 0
		actualTotal := 0
		stopID := ""
		if queueGate {
			stopID = unit.ID + ":" + unitRef.Revision + ":QUEUED"
			stopRef := ref("atlas_stage", stopID)
			project.Steps = append(project.Steps, studioStep{ID: stopID, Title: "実装キュー", State: "blocked",
				Revision: unitRef.Revision, Reason: gateReason, Source: stopRef})
			project.Current = append(project.Current, stopRef)
		}
		for rawIndex, stage := range unit.Stages {
			code := normalizeAtlasStage(stage.Code)
			if code == "" {
				continue
			}
			actualTotal++
			id := unit.ID + ":" + unitRef.Revision + ":" + code
			stageRef := ref("atlas_stage", id)
			state := mapped(stepStates, strings.ToLower(strings.TrimSpace(stage.State)))
			stageIndex := rawIndex
			isStop := stageIndex == stopIndex
			if blockedUnit {
				switch {
				case ambiguous && state == "blocked":
					state = "unknown"
				case queueGate && state == "blocked":
					state = "dependency"
				case stopIndex >= 0 && stageIndex > stopIndex && state == "blocked":
					state = "dependency"
				case isStop && state == "active":
					state = "blocked"
				}
			} else if state == "blocked" {
				// A non-BLOCKED unit can retain blocked stage states from an older
				// freeze. Keep that historical uncertainty out of the active red state.
				state = "unknown"
			}
			reason := ""
			if state == "failed" {
				reason = sanitizeAtlasReason(stage.Reason)
			} else if blockedUnit && isStop && state == "blocked" {
				reason = stopReason
			}
			step := studioStep{ID: id, Title: fallback(stage.Title, code), State: state,
				Revision: unitRef.Revision, Reason: reason, History: atlasReceiptHistory(unit, lineage, code, atlas.StageReceipts), Source: stageRef}
			if strings.EqualFold(strings.TrimSpace(stage.State), "passed") {
				passed++
			}
			if blockedUnit {
				if isStop {
					stopID = id
					project.Current = append(project.Current, stageRef)
				}
			} else if strings.EqualFold(strings.TrimSpace(stage.State), "active") || code == normalizeAtlasStage(unit.InvalidatedStage) {
				project.Current = append(project.Current, stageRef)
			}
			project.Steps = append(project.Steps, step)
		}
		if actualTotal > 0 {
			project.Completion = &studioProgress{Done: passed, Total: actualTotal, Basis: "Atlas 検証済み工程数", Revision: "atlas-pipeline-v1"}
		}
		switch unit.State {
		case "BLOCKED":
			// A historical rejected/closed Workstream remains inspectable even when an old unit
			// has a blocked stage. Only current, unfinished work is added to the queue.
			if project.State == "archived" || project.State == "done" {
				continue
			}
			project.State = "blocked"
			var reason *string
			var since *time.Time
			evidence := []coreRef{unitRef}
			blockerReason := freezeReason
			if queueGate {
				blockerReason = gateReason
			} else if stopIndex >= 0 {
				blockerReason = stopReason
			}
			if blockerReason != "" {
				text := blockerReason
				reason = &text
			}
			for _, freeze := range unit.Freezes {
				if freeze.Revision != unit.Revision || strings.TrimSpace(freeze.ID) == "" ||
					strings.TrimSpace(freeze.BlockedUnitID) != unit.ID || strings.EqualFold(strings.TrimSpace(freeze.State), "resolved") {
					continue
				}
				evidence = append(evidence, ref("atlas_queue_freeze", freeze.ID))
				if freeze.Created != nil && (since == nil || freeze.Created.After(*since)) {
					since = timestamp(freeze.Created)
				}
			}
			id := "atlas-unit:" + unit.ID
			blockerID := "atlas-block:" + unit.ID
			s.Work = append(s.Work, studioWork{ID: id, Project: projectID, Step: stopID, Title: fallback(item.Title, project.Title),
				State: "blocked", Agents: project.Agents, Dependencies: []string{}, Blocked: []string{blockerID},
				Source: unitRef, Observed: date(item.Updated), Refs: evidence})
			s.Blockers = append(s.Blockers, studioBlocker{ID: blockerID, Work: id, Class: "blocked", Reason: reason, Since: since, Evidence: evidence})
		case "REJECTED":
			project.State = "archived"
		case "DONE":
			project.State = "done"
		case "QUEUED":
			project.State = "queued"
		}
	}
}

func (s *studioSnapshot) addArtifacts(rows []viewerArtifact, atlas viewerAtlas, index map[string]int) {
	closures := map[string]atlasClosure{}
	for _, row := range atlas.Closures {
		if row.Artifact != "" && row.State == "completed" {
			closures[row.Artifact] = row
		}
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if row.ID == "" || seen[row.ID] {
			continue
		}
		seen[row.ID] = true
		project := ""
		if _, ok := index[row.Project]; ok {
			project = row.Project
		}
		state := "created"
		switch row.State {
		case "pending_review", "reported":
			state = "reported"
		case "archived":
			state = "archived"
		case "":
			state = "unknown"
		}
		owner := ref("workstream_artifact", row.ID)
		record := studioArtifact{ID: row.ID, Project: project, Title: fallback(row.Title, row.ID), Kind: row.Kind, State: state,
			Owner: owner, Evidence: []coreRef{}, Created: timestamp(row.Created)}
		if closure, ok := closures[row.ID]; ok && closure.Unit != "" {
			record.Revision = revision(closure.Revision)
			unitRef := ref("atlas_unit", closure.Unit)
			unitRef.Revision = record.Revision
			record.Evidence = append(record.Evidence, unitRef)
			// A completed Workstream record may be a rejected unit. Only an explicit
			// Atlas live-verification target establishes verification of the artifact.
			if closure.Target == "DONE" || closure.Target == "LIVE_VERIFIED" {
				record.State = "verified"
			}
		}
		s.Artifacts = append(s.Artifacts, record)
		s.addActivity("artifact_created", record.Title, "", record.Created, owner)
	}
}
