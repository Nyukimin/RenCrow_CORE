package main

import (
	"crypto/sha256"
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
	ID            string     `json:"agent_id"`
	Name          string     `json:"display_name"`
	Kind          string     `json:"actor_kind"`
	Role          string     `json:"role,omitempty"`
	State         string     `json:"state"`
	TaskRefs      []coreRef  `json:"task_refs"`
	TaskStackHead *coreRef   `json:"task_stack_head,omitempty"`
	Source        coreRef    `json:"source"`
	Observed      *time.Time `json:"observed_at"`
	Freshness     string     `json:"freshness"`
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
	ID       string  `json:"id"`
	Title    string  `json:"title"`
	State    string  `json:"state"`
	Revision string  `json:"revision,omitempty"`
	Source   coreRef `json:"source"`
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
	Version      string            `json:"schema_version"`
	ID           string            `json:"snapshot_id"`
	Observed     time.Time         `json:"observed_at"`
	SourceHealth map[string]string `json:"source_health"`
	Agents       []studioAgent     `json:"agents"`
	Projects     []studioProject   `json:"projects"`
	Work         []studioWork      `json:"work_items"`
	Artifacts    []studioArtifact  `json:"artifacts"`
	Blockers     []studioBlocker   `json:"blockers"`
	Activities   []studioActivity  `json:"activities"`
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
	Current  []atlasItem     `json:"current"`
	Backlog  []atlasItem     `json:"backlog"`
	Pipeline []atlasPipeline `json:"pipeline"`
	Closures []atlasClosure  `json:"closure_receipts"`
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
}
type atlasStage struct {
	Code   string `json:"stage"`
	Title  string `json:"name"`
	State  string `json:"status"`
	Reason string `json:"reason"`
}
type atlasFreeze struct {
	ID       string     `json:"freeze_id"`
	Revision int        `json:"blocked_revision"`
	Reason   string     `json:"reason_code"`
	State    string     `json:"status"`
	Created  *time.Time `json:"created_at"`
}
type atlasClosure struct {
	Artifact string `json:"artifact_id"`
	Unit     string `json:"unit_id"`
	Revision int    `json:"implementation_revision"`
	State    string `json:"status"`
	Target   string `json:"target_delivery_state"`
}

var coreActors = map[string]string{"mio": "Mio", "shiro": "Shiro", "kuro": "Kuro", "midori": "Midori",
	"coder1": "Coder 1", "coder2": "Coder 2", "coder3": "Coder 3", "coder4": "Coder 4"}
var coreRoles = map[string]string{"mio": "Chat", "shiro": "Worker", "kuro": "Chat", "midori": "Chat",
	"coder1": "Coder", "coder2": "Coder", "coder3": "Coder", "coder4": "Coder"}
var taskStates = map[string]string{"queued": "queued", "running": "working", "waiting": "waiting", "blocked": "blocked",
	"failed": "failed", "succeeded": "done", "cancelled": "canceled", "superseded": "canceled"}
var projectStates = map[string]string{"draft": "planned", "active": "active", "paused": "waiting", "waiting": "waiting",
	"completed": "done", "archived": "archived"}
var goalStates = map[string]string{"draft": "planned", "active": "active", "waiting": "waiting", "completed": "done",
	"archived": "archived", "blocked": "blocked", "failed": "failed", "canceled": "canceled", "cancelled": "canceled"}
var stepStates = map[string]string{"pending": "queued", "active": "active", "passed": "done", "blocked": "blocked", "failed": "failed"}
var monitorStates = map[string]string{"running": "working", "thinking": "thinking", "idle": "idle", "unavailable": "unavailable", "error": "failed"}

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
	for _, row := range agents.Agents {
		name := coreActors[row.ID]
		if name == "" {
			continue
		}
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
		agent := studioAgent{ID: row.ID, Name: name, Kind: "core", Role: coreRoles[row.ID], State: state, TaskRefs: []coreRef{},
			Source: ref("agent_monitor", row.ID), Observed: at, Freshness: freshness}
		if task, ok := publicTasks[row.Task]; ok && task.Agent == row.ID && task.State == "running" && (state == "working" || state == "thinking") {
			agent.TaskRefs = append(agent.TaskRefs, ref("task", task.ID))
		}
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
	preferredTask := ""
	if freshness == "fresh" && (state == "working" || state == "thinking") {
		if task, ok := publicTasks[agent.Task]; ok && task.Agent == agent.ID && task.State == "running" {
			preferredTask = task.ID
		}
	}
	var firstRunning, firstOther *coreRef
	for _, task := range workItems {
		if task.Source.Type != "task" || len(task.Agents) != 1 || task.Agents[0] != agent.ID {
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
		passed := 0
		for _, stage := range unit.Stages {
			if stage.Code == "" {
				continue
			}
			id := unit.ID + ":" + unitRef.Revision + ":" + stage.Code
			stageRef := ref("atlas_stage", id)
			step := studioStep{ID: id, Title: fallback(stage.Title, stage.Code), State: mapped(stepStates, stage.State),
				Revision: unitRef.Revision, Source: stageRef}
			if stage.State == "passed" {
				passed++
			}
			if stage.State == "active" || stage.Code == unit.InvalidatedStage {
				project.Current = append(project.Current, stageRef)
			}
			project.Steps = append(project.Steps, step)
		}
		if len(project.Steps) > 0 {
			project.Completion = &studioProgress{Done: passed, Total: len(project.Steps), Basis: "Atlas 検証済み工程数", Revision: "atlas-pipeline-v1"}
		}
		switch unit.State {
		case "BLOCKED":
			// A historical rejected/closed Workstream remains inspectable even when an old unit
			// has a blocked stage. Only current, unfinished work is added to the queue.
			if project.State == "archived" || project.State == "done" {
				continue
			}
			project.State = "blocked"
			var stageID string
			var reason *string
			var since *time.Time
			evidence := []coreRef{unitRef}
			for _, stage := range unit.Stages {
				if stage.Code == unit.InvalidatedStage {
					stageID = unit.ID + ":" + unitRef.Revision + ":" + stage.Code
					if stage.Reason != "" {
						text := stage.Reason
						reason = &text
					}
				}
			}
			for _, freeze := range unit.Freezes {
				if freeze.Revision != unit.Revision || freeze.ID == "" {
					continue
				}
				evidence = append(evidence, ref("atlas_queue_freeze", freeze.ID))
				if freeze.Created != nil && (since == nil || freeze.Created.After(*since)) {
					since = timestamp(freeze.Created)
					if freeze.Reason != "" {
						text := freeze.Reason
						reason = &text
					}
				}
			}
			id := "atlas-unit:" + unit.ID
			blockerID := "atlas-block:" + unit.ID
			s.Work = append(s.Work, studioWork{ID: id, Project: projectID, Step: stageID, Title: fallback(item.Title, project.Title),
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
