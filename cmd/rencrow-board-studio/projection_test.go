package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSnapshotUsesScopedPublicRecordsAndCanonicalAtlasRevision(t *testing.T) {
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	ago := now.Add(-time.Hour)
	actors := viewerAgents{Agents: []viewerAgent{
		{ID: "mio", State: "running", Task: "public", Updated: now.Format(time.RFC3339)},
		{ID: "kuro", State: "running", Task: "private", Updated: ago.Format(time.RFC3339)},
	}}
	public := viewerTask{ID: "public", Title: "Visible task", Agent: "mio", State: "running", Project: "project", Updated: &now, Dependencies: []string{"private"}}
	public.Publication.Audience = "public"
	private := viewerTask{ID: "private", Title: "NEVER RENDER", Agent: "kuro", State: "running"}
	private.Publication.Audience = "private"
	gameDomain := "game"
	work := viewerWorkstreams{
		Workstreams: []viewerProject{{ID: "project", Title: "Live Workstream", ProjectDomain: &gameDomain, State: "active", Agent: "mio"}},
		Artifacts:   []viewerArtifact{{ID: "artifact", Project: "project", Title: "Registered output", State: "completed"}},
	}
	atlas := viewerAtlas{
		Backlog: []atlasItem{{ID: "item", Project: "project", Unit: "unit", Revision: 2, Title: "Current Atlas work"}},
		Pipeline: []atlasPipeline{
			{ID: "unit", Item: "item", Revision: 1, State: "DONE", Stages: []atlasStage{{Code: "DONE", State: "passed"}}},
			{ID: "unit", Item: "item", Revision: 2, State: "BLOCKED", InvalidatedStage: "BUILD",
				Stages: []atlasStage{{Code: "SPEC", State: "passed"}, {Code: "BUILD", State: "blocked", Reason: "stage_failed"}}},
		},
	}
	s := projectStudio(now, actors, viewerTasks{Items: []viewerTask{public, private}}, work, atlas)
	if len(s.Work) != 2 || s.Work[0].State != "blocked" || s.Work[1].ID != "public" || len(s.Work[1].Dependencies) != 0 {
		t.Fatalf("unexpected projected work: %+v", s.Work)
	}
	if len(s.Projects) != 1 || s.Projects[0].State != "blocked" || s.Projects[0].Completion.Done != 1 ||
		s.Projects[0].Completion.Total != 2 || len(s.Blockers) != 1 || *s.Blockers[0].Reason != "stage_failed" {
		t.Fatalf("unexpected Atlas projection: %+v %+v", s.Projects, s.Blockers)
	}
	if s.Projects[0].Category != "Atlas" || s.Projects[0].ProjectDomain == nil || *s.Projects[0].ProjectDomain != gameDomain {
		t.Fatalf("project domain/category projection=%+v, want game domain independent of Atlas category", s.Projects[0])
	}
	if len(s.Agents[0].TaskRefs) != 1 || len(s.Agents[1].TaskRefs) != 0 || s.Agents[1].Freshness != "stale" {
		t.Fatalf("unexpected agent references: %+v", s.Agents)
	}
	if len(s.Artifacts) != 1 || s.Artifacts[0].State != "created" {
		t.Fatalf("registration promoted to verification: %+v", s.Artifacts)
	}
}

func TestProjectStudioUsesNewestWorkstreamAndGoalHistoryRows(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	projectDomain := "software"
	work := viewerWorkstreams{
		// Workstream and Goal stores return append-only JSONL history newest first.
		Workstreams: []viewerProject{
			{ID: "project", Title: "Current project", ProjectDomain: &projectDomain, State: "active"},
			{ID: "project", Title: "Old project", State: "paused"},
		},
		Goals: []viewerGoal{
			{ID: "goal-active", Project: "project", Title: "Current goal", State: "active"},
			{ID: "goal-active", Project: "project", Title: "Old archived goal", State: "archived"},
			{ID: "goal-archived", Project: "project", Title: "Current archived goal", State: "archived"},
			{ID: "goal-archived", Project: "project", Title: "Old active goal", State: "active"},
		},
	}
	atlas := viewerAtlas{
		Backlog:  []atlasItem{{ID: "item", Project: "project", Unit: "unit", Revision: 1}},
		Pipeline: []atlasPipeline{{ID: "unit", Item: "item", Revision: 1, State: "COMPLETED", Stages: []atlasStage{{Code: "SPEC", State: "passed"}}}},
	}

	snapshot := projectStudio(now, viewerAgents{}, viewerTasks{}, work, atlas)
	if len(snapshot.Projects) != 1 {
		t.Fatalf("projects=%+v, want one latest project row", snapshot.Projects)
	}
	project := snapshot.Projects[0]
	if project.Title != "Current project" || project.State != "active" || project.Category != "Atlas" || project.ProjectDomain == nil || *project.ProjectDomain != projectDomain {
		t.Fatalf("project projection=%+v, want latest Workstream with Atlas category and current domain", project)
	}
	goalIDs := map[string]string{}
	for _, row := range snapshot.Work {
		if row.ID == "goal-active" || row.ID == "goal-archived" {
			goalIDs[row.ID] = row.Title
		}
	}
	if len(goalIDs) != 1 || goalIDs["goal-active"] != "Current goal" {
		t.Fatalf("goal history projection=%v, want only latest active row", goalIDs)
	}
}

func TestAddArtifactsPreservesReviewStatusAndExplicitVerification(t *testing.T) {
	rows := []viewerArtifact{
		{ID: "pending-review", Title: "Review proposal", Kind: "proposal", State: "pending_review"},
		{ID: "reported", Title: "Reported proposal", Kind: "proposal", State: "reported"},
		{ID: "archived", Title: "Archived proposal", Kind: "proposal", State: "archived"},
		{ID: "unknown", Title: "Unspecified status", Kind: "proposal"},
		{ID: "created", Title: "Created proposal", Kind: "proposal", State: "created"},
		{ID: "verified", Title: "Atlas verified output", Kind: "atlas_implementation_unit", State: "pending_review"},
	}
	atlas := viewerAtlas{Closures: []atlasClosure{{Artifact: "verified", Unit: "unit", State: "completed", Target: "DONE"}}}

	snapshot := studioSnapshot{}
	snapshot.addArtifacts(rows, atlas, nil)

	wantStates := []string{"review", "reported", "archived", "unknown", "created", "verified"}
	if len(snapshot.Artifacts) != len(rows) {
		t.Fatalf("artifacts = %d, want %d", len(snapshot.Artifacts), len(rows))
	}
	for i, want := range wantStates {
		if got := snapshot.Artifacts[i].State; got != want {
			t.Errorf("artifact %q state = %q, want %q", rows[i].ID, got, want)
		}
		if snapshot.Artifacts[i].ID != rows[i].ID || snapshot.Artifacts[i].Title != rows[i].Title || snapshot.Artifacts[i].Kind != rows[i].Kind {
			t.Errorf("artifact %q changed its identity or content metadata: %+v", rows[i].ID, snapshot.Artifacts[i])
		}
	}
}

func TestSnapshotProjectsQueueGateAndScopedStageReceiptHistory(t *testing.T) {
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	work := viewerWorkstreams{Workstreams: []viewerProject{{ID: "project", Title: "Atlas", State: "active"}}}
	var atlas viewerAtlas
	input := `{
	  "backlog":[{"backlog_item_id":"old-item","workstream_id":"project","implementation_unit_id":"unit","implementation_revision":2,"title":"Old"}],
	  "current":[{"backlog_item_id":"current-item","workstream_id":"project","implementation_unit_id":"unit","implementation_revision":4,"title":"Current"}],
	  "pipeline":[{
	    "unit_id":"unit","backlog_item_id":"current-item","implementation_revision":4,"delivery_state":"BLOCKED","invalidated_from_stage":"QUEUED",
	    "stages":[
	      {"stage":"SPEC","name":"Specification","status":"passed"},
	      {"stage":"TDD_RED","name":"TDD Red","status":"blocked"},
	      {"stage":"DONE","name":"Done","status":"blocked"}
	    ],
	    "queue_freezes":[
	      {"freeze_id":"active-match","blocked_unit_id":"unit","blocked_revision":4,"reason_code":"worker_failure","status":"ACTIVE","created_at":"2026-11-12T00:00:00Z"},
	      {"freeze_id":"linked-only","blocked_unit_id":"other-unit","replacement_unit_id":"unit","blocked_revision":4,"reason_code":"linked_reason","status":"ACTIVE","created_at":"2026-11-13T00:00:00Z"},
		      {"freeze_id":"resolved-match","blocked_unit_id":"unit","blocked_revision":4,"reason_code":"resolved_reason","status":"resolved","created_at":"2026-11-13T01:00:00Z"}
	    ]
	  }],
	  "stage_receipts":[
	    {"receipt_id":"r1","unit_id":"unit","backlog_item_id":"old-item","implementation_revision":1,"target_stage":"Specification","status":"completed","created_at":"2026-11-10T00:00:00Z","completed_at":"2026-11-10T00:01:00Z"},
	    {"receipt_id":"b-completed","unit_id":"unit","backlog_item_id":"current-item","implementation_revision":2,"target_stage":"SPEC","status":"completed","created_at":"2026-11-11T00:00:00Z","completed_at":"2026-11-11T00:02:00Z"},
	    {"receipt_id":"a-failed","unit_id":"unit","backlog_item_id":"old-item","implementation_revision":2,"target_stage":"specification","status":"failed","reason_code":"stage_failed","error":"private receipt error text","result_json":"private receipt result","created_at":"2026-11-11T00:00:00Z","completed_at":"2026-11-11T00:02:00Z"},
	    {"receipt_id":"r3","unit_id":"unit","implementation_revision":3,"target_stage":"SPEC","status":"prepared","created_at":"2026-11-12T00:00:00Z"},
	    {"receipt_id":"sensitive-reason","unit_id":"unit","backlog_item_id":"current-item","implementation_revision":4,"target_stage":"SPEC","status":"failed","reason_code":"private_key_xxx","created_at":"2026-11-12T01:00:00Z"},
	    {"receipt_id":"wrong-unit","unit_id":"other-unit","implementation_revision":2,"target_stage":"SPEC","status":"failed","created_at":"2026-11-09T00:00:00Z"},
	    {"receipt_id":"wrong-stage","unit_id":"unit","implementation_revision":2,"target_stage":"DONE","status":"failed","created_at":"2026-11-09T00:00:00Z"},
	    {"receipt_id":"wrong-item","unit_id":"unit","backlog_item_id":"unrelated-item","implementation_revision":2,"target_stage":"SPEC","status":"failed","created_at":"2026-11-09T00:00:00Z"},
	    {"unit_id":"unit","implementation_revision":2,"target_stage":"SPEC","status":"failed","created_at":"2026-11-09T00:00:00Z"},
	    {"receipt_id":"unknown-status","unit_id":"unit","implementation_revision":2,"target_stage":"SPEC","status":"rejected","created_at":"2026-11-09T00:00:00Z"}
	  ]
	}`
	if err := json.Unmarshal([]byte(input), &atlas); err != nil {
		t.Fatal(err)
	}
	snapshot := projectStudio(now, viewerAgents{}, viewerTasks{}, work, atlas)
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Projects []struct {
			Current []coreRef `json:"current_step_runs"`
			Steps   []struct {
				ID       string `json:"id"`
				Title    string `json:"title"`
				State    string `json:"state"`
				Reason   string `json:"reason"`
				Revision string `json:"revision"`
				History  []struct {
					Revision string     `json:"revision"`
					Status   string     `json:"status"`
					Occurred *time.Time `json:"occurred_at"`
					Reason   string     `json:"reason"`
				} `json:"history"`
			} `json:"steps"`
			Completion struct {
				Done  int `json:"done"`
				Total int `json:"total"`
			} `json:"completion"`
		} `json:"projects"`
		Work []studioWork `json:"work_items"`
	}
	if err := json.Unmarshal(payload, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire.Projects) != 1 {
		t.Fatalf("projects = %+v", wire.Projects)
	}
	project := wire.Projects[0]
	if len(project.Steps) != 4 || project.Steps[0].ID != "unit:4:QUEUED" || project.Steps[0].Title != "実装キュー" ||
		project.Steps[0].State != "blocked" || project.Steps[0].Reason != "worker_failure" {
		t.Fatalf("queue gate step = %+v", project.Steps)
	}
	if project.Steps[1].State != "done" || project.Steps[2].State != "dependency" || project.Steps[3].State != "dependency" {
		t.Fatalf("queue propagation changed passed stage or missed downstream wait: %+v", project.Steps)
	}
	if project.Completion.Done != 1 || project.Completion.Total != 3 {
		t.Fatalf("queue display step changed pipeline denominator: %+v", project.Completion)
	}
	if len(project.Current) != 1 || project.Current[0].ID != project.Steps[0].ID {
		t.Fatalf("current stop refs = %+v, want queue gate", project.Current)
	}
	if len(wire.Work) != 1 || wire.Work[0].Step != project.Steps[0].ID || len(wire.Work[0].Refs) != 2 {
		t.Fatalf("blocked work must link to gate and its matching freeze: %+v", wire.Work)
	}
	history := project.Steps[1].History
	if len(history) != 5 {
		t.Fatalf("SPEC receipt history = %+v, want five compatible receipts", history)
	}
	wantRevisions := []string{"1", "2", "2", "3", "4"}
	wantStatuses := []string{"completed", "failed", "completed", "prepared", "failed"}
	for i := range history {
		if history[i].Revision != wantRevisions[i] || history[i].Status != wantStatuses[i] || history[i].Occurred == nil {
			t.Errorf("history[%d] = %+v, want revision %s status %s with occurrence time", i, history[i], wantRevisions[i], wantStatuses[i])
		}
	}
	if history[1].Reason != "stage_failed" || history[0].Reason != "" || history[2].Reason != "" || history[3].Reason != "" {
		t.Errorf("receipt reasons were not allowlisted by status: %+v", history)
	}
	if history[4].Reason != "" {
		t.Errorf("unregistered token-like reason code was exposed: %+v", history[4])
	}
	if history[1].Occurred.Format(time.RFC3339) != "2026-11-11T00:02:00Z" {
		t.Errorf("history timestamp did not prefer completed_at: %+v", history[1])
	}
	if strings.Contains(string(payload), "private receipt error text") || strings.Contains(string(payload), "private receipt result") ||
		strings.Contains(string(payload), "private_key_xxx") || strings.Contains(string(payload), "linked_reason") || strings.Contains(string(payload), "resolved_reason") {
		t.Fatalf("raw or unrelated receipt/freeze text leaked into projection: %s", payload)
	}
}

func TestSnapshotDistinguishesAtlasStopFromPropagatedAndUnknownBlocks(t *testing.T) {
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	work := viewerWorkstreams{Workstreams: []viewerProject{{ID: "project", Title: "Atlas", State: "active"}}}
	tests := []struct {
		name       string
		unit       atlasPipeline
		leaseJSON  string
		wantStates []string
		wantStop   string
		wantReason string
		mustOmit   []string
	}{
		{name: "explicit BUILD stop", unit: atlasPipeline{ID: "build-unit", Item: "build-item", Revision: 1, State: "BLOCKED", InvalidatedStage: "BUILD",
			Stages: []atlasStage{{Code: "SPEC", State: "passed"}, {Code: "BUILD", State: "blocked", Reason: "step18_e2e_stage_failed"}, {Code: "DONE", State: "blocked", Reason: "step18_e2e_stage_failed"}}},
			wantStates: []string{"done", "blocked", "dependency"}, wantStop: "build-unit:1:BUILD", wantReason: "step18_e2e_stage_failed"},
		{name: "single DONE closure stop", unit: atlasPipeline{ID: "done-unit", Item: "done-item", Revision: 2, State: "BLOCKED",
			Stages: []atlasStage{{Code: "SPEC", State: "passed"}, {Code: "DONE", State: "blocked", Reason: "stage_failed"}}},
			wantStates: []string{"done", "blocked"}, wantStop: "done-unit:2:DONE", wantReason: "stage_failed"},
		{name: "closure binding detail is reduced to a safe reason", unit: atlasPipeline{ID: "closure-unit", Item: "closure-item", Revision: 5, State: "BLOCKED",
			Stages: []atlasStage{{Code: "DONE", State: "blocked", Reason: `atlas lifecycle idempotency conflict: persisted closure action_id mismatch for unit "private-unit" revision 5: stage="private-stage-action", closure="private-closure-action"`}}},
			wantStates: []string{"blocked"}, wantStop: "closure-unit:5:DONE", wantReason: "完了工程の操作IDが不一致",
			mustOmit: []string{"private-unit", "private-stage-action", "private-closure-action"}},
		{name: "broad propagated block", unit: atlasPipeline{ID: "broad-unit", Item: "broad-item", Revision: 3, State: "BLOCKED",
			Stages: []atlasStage{{Code: "BUILD", State: "blocked"}, {Code: "DONE", State: "blocked"}}},
			wantStates: []string{"unknown", "unknown"}, wantStop: "", wantReason: ""},
		{name: "unresolved explicit invalidation stays unknown", unit: atlasPipeline{ID: "mismatch-unit", Item: "mismatch-item", Revision: 3, State: "BLOCKED", InvalidatedStage: "SPEC",
			Stages: []atlasStage{{Code: "SPEC", State: "passed"}, {Code: "BUILD", State: "blocked"}}},
			wantStates: []string{"done", "unknown"}, wantStop: "", wantReason: ""},
		{name: "lease identifies stop", unit: atlasPipeline{ID: "lease-unit", Item: "lease-item", Revision: 4, State: "BLOCKED",
			Stages: []atlasStage{{Code: "BUILD", State: "blocked", Reason: "worker_failure"}, {Code: "DONE", State: "blocked"}}},
			leaseJSON:  `{"holder_unit_id":"lease-unit","stage":"Build","revision":"4"}`,
			wantStates: []string{"blocked", "dependency"}, wantStop: "lease-unit:4:BUILD", wantReason: "worker_failure"},
		{name: "token-like stage reason is omitted", unit: atlasPipeline{ID: "private-unit", Item: "private-item", Revision: 7, State: "BLOCKED", InvalidatedStage: "DONE",
			Stages: []atlasStage{{Code: "DONE", State: "blocked", Reason: "private_key_xxx"}}},
			wantStates: []string{"blocked"}, wantStop: "private-unit:7:DONE", mustOmit: []string{"private_key_xxx"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			atlas := viewerAtlas{Current: []atlasItem{{ID: test.unit.Item, Project: "project", Unit: test.unit.ID, Revision: test.unit.Revision}}, Pipeline: []atlasPipeline{test.unit}}
			if test.leaseJSON != "" {
				payload, err := json.Marshal(atlas)
				if err != nil {
					t.Fatal(err)
				}
				var envelope map[string]json.RawMessage
				if err := json.Unmarshal(payload, &envelope); err != nil {
					t.Fatal(err)
				}
				var pipeline []map[string]json.RawMessage
				if err := json.Unmarshal(envelope["pipeline"], &pipeline); err != nil {
					t.Fatal(err)
				}
				pipeline[0]["active_lease"] = json.RawMessage(test.leaseJSON)
				pipelineJSON, err := json.Marshal(pipeline)
				if err != nil {
					t.Fatal(err)
				}
				envelope["pipeline"] = pipelineJSON
				payload, err = json.Marshal(envelope)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(payload, &atlas); err != nil {
					t.Fatal(err)
				}
			}
			snapshot := projectStudio(now, viewerAgents{}, viewerTasks{}, work, atlas)
			payload, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				Projects []struct {
					Current []coreRef `json:"current_step_runs"`
					Steps   []struct {
						ID     string `json:"id"`
						State  string `json:"state"`
						Reason string `json:"reason"`
					} `json:"steps"`
				} `json:"projects"`
				Work []studioWork `json:"work_items"`
			}
			if err := json.Unmarshal(payload, &wire); err != nil {
				t.Fatal(err)
			}
			if len(wire.Projects) != 1 || len(wire.Projects[0].Steps) != len(test.wantStates) {
				t.Fatalf("unexpected projected steps: %+v", wire.Projects)
			}
			for i, want := range test.wantStates {
				if got := wire.Projects[0].Steps[i].State; got != want {
					t.Errorf("step[%d] state = %q, want %q", i, got, want)
				}
			}
			if len(wire.Work) != 1 || wire.Work[0].Step != test.wantStop {
				t.Errorf("blocked work stop = %+v, want %q", wire.Work, test.wantStop)
			}
			stopReason := ""
			for _, step := range wire.Projects[0].Steps {
				if step.ID == test.wantStop {
					stopReason = step.Reason
				}
			}
			if stopReason != test.wantReason {
				t.Errorf("stop reason = %q, want %q", stopReason, test.wantReason)
			}
			for _, private := range test.mustOmit {
				if strings.Contains(string(payload), private) {
					t.Errorf("private closure binding detail leaked: %q", private)
				}
			}
		})
	}
}

func TestSnapshotKeepsTerminalAtlasStopVisibleWithoutQueueing(t *testing.T) {
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	unit := atlasPipeline{ID: "terminal-unit", Item: "terminal-item", Revision: 6, State: "BLOCKED", InvalidatedStage: "DONE",
		Stages: []atlasStage{{Code: "SPEC", State: "passed"}, {Code: "DONE", State: "blocked", Reason: "stage_failed"}}}
	for _, test := range []struct {
		workstreamState string
		wantProject     string
	}{
		{workstreamState: "completed", wantProject: "done"},
		{workstreamState: "archived", wantProject: "archived"},
	} {
		work := viewerWorkstreams{Workstreams: []viewerProject{{ID: "project", Title: "Atlas", State: test.workstreamState}}}
		atlas := viewerAtlas{Current: []atlasItem{{ID: unit.Item, Project: "project", Unit: unit.ID, Revision: unit.Revision}}, Pipeline: []atlasPipeline{unit}}
		snapshot := projectStudio(now, viewerAgents{}, viewerTasks{}, work, atlas)
		if len(snapshot.Projects) != 1 || snapshot.Projects[0].State != test.wantProject || len(snapshot.Projects[0].Steps) != 2 ||
			snapshot.Projects[0].Steps[1].State != "blocked" || snapshot.Projects[0].Steps[1].Reason != "stage_failed" {
			t.Errorf("%s terminal project lost visible Atlas stop: %+v", test.workstreamState, snapshot.Projects)
		}
		if len(snapshot.Projects[0].Current) != 1 || snapshot.Projects[0].Current[0].ID != unit.ID+":6:DONE" || len(snapshot.Work) != 0 || len(snapshot.Blockers) != 0 {
			t.Errorf("%s terminal projection changed queue exclusion or stop link: current=%+v work=%+v blockers=%+v", test.workstreamState, snapshot.Projects[0].Current, snapshot.Work, snapshot.Blockers)
		}
	}

	work := viewerWorkstreams{Workstreams: []viewerProject{{ID: "project", Title: "Atlas", State: "active"}}}
	rejected := atlasPipeline{ID: "rejected-unit", Item: "rejected-item", Revision: 1, State: "REJECTED",
		Stages: []atlasStage{{Code: "SPEC", State: "blocked", Reason: "stage_failed"}}}
	atlas := viewerAtlas{Current: []atlasItem{{ID: rejected.Item, Project: "project", Unit: rejected.ID, Revision: rejected.Revision}}, Pipeline: []atlasPipeline{rejected}}
	snapshot := projectStudio(now, viewerAgents{}, viewerTasks{}, work, atlas)
	if len(snapshot.Projects) != 1 || snapshot.Projects[0].State != "archived" || snapshot.Projects[0].Steps[0].State != "unknown" || snapshot.Projects[0].Steps[0].Reason != "" || len(snapshot.Work) != 0 || len(snapshot.Blockers) != 0 {
		t.Fatalf("non-BLOCKED historical stage remained an active blocker: %+v work=%+v blockers=%+v", snapshot.Projects, snapshot.Work, snapshot.Blockers)
	}
}

func TestSnapshotProjectsTaskStackHeadsFromPublicTasksInViewerOrder(t *testing.T) {
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	actors := viewerAgents{Agents: []viewerAgent{
		{ID: "mio", State: "running", Task: "mio-monitored", Updated: now.Add(-time.Minute).Format(time.RFC3339)},
		{ID: "shiro", State: "running", Task: "shiro-later", Updated: now.Add(-2 * time.Minute).Format(time.RFC3339)},
		{ID: "kuro", State: "running", Task: "kuro-private", Updated: now.Add(-time.Minute).Format(time.RFC3339)},
		{ID: "midori", State: "idle", Updated: now.Add(-time.Minute).Format(time.RFC3339)},
		{ID: "coder1", State: "idle", Updated: now.Add(-time.Minute).Format(time.RFC3339)},
		{ID: "coder2", State: "idle", Updated: now.Add(-time.Minute).Format(time.RFC3339)},
	}}
	// Rows are already in the order returned by CORE Viewer ListTasks:
	// UpdatedAt descending, then CORE's Task ID tie order.
	privateCollision := studioTask("mio-first", "mio", "running", now.Add(-30*time.Second), "private")
	privateCollision.Title = "NEVER RENDER"
	privateMonitorTask := studioTask("kuro-private", "kuro", "running", now.Add(-5*time.Minute), "private")
	privateMonitorTask.Title = "NEVER RENDER"
	rows := []viewerTask{
		privateCollision,
		studioTask("mio-first", "mio", "running", now.Add(-time.Minute), "public"),
		studioTask("mio-monitored", "mio", "running", now.Add(-2*time.Minute), "public"),
		studioTask("shiro-first", "shiro", "running", now.Add(-3*time.Minute), "public"),
		studioTask("shiro-later", "shiro", "running", now.Add(-4*time.Minute), "public"),
		privateMonitorTask,
		studioTask("kuro-running", "kuro", "running", now.Add(-6*time.Minute), "public"),
		studioTask("kuro-queued", "kuro", "queued", now.Add(-7*time.Minute), "public"),
		studioTask("mio-upper-assignee", "MIO", "running", now.Add(-8*time.Minute), "public"),
		studioTask("midori-failed", "midori", "failed", now.Add(-9*time.Minute), "public"),
		studioTask("midori-succeeded", "midori", "succeeded", now.Add(-10*time.Minute), "public"),
		studioTask("midori-cancelled", "midori", "cancelled", now.Add(-11*time.Minute), "public"),
		studioTask("midori-unknown", "midori", "paused", now.Add(-12*time.Minute), "public"),
		studioTask("midori-waiting", "midori", "waiting", now.Add(-13*time.Minute), "public"),
		studioTask("midori-blocked", "midori", "blocked", now.Add(-14*time.Minute), "public"),
		studioTask("coder1-blocked", "coder1", "blocked", now.Add(-15*time.Minute), "public"),
		studioTask("coder2-failed", "coder2", "failed", now.Add(-16*time.Minute), "public"),
		studioTask("coder2-done", "coder2", "succeeded", now.Add(-17*time.Minute), "public"),
	}
	snapshot := projectStudio(now, actors, viewerTasks{Items: rows}, viewerWorkstreams{}, viewerAtlas{})
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Agents []struct {
			ID        string    `json:"agent_id"`
			TaskRefs  []coreRef `json:"task_refs"`
			StackHead *coreRef  `json:"task_stack_head"`
		} `json:"agents"`
		Work []studioWork `json:"work_items"`
	}
	if err := json.Unmarshal(payload, &wire); err != nil {
		t.Fatal(err)
	}
	wantHeads := map[string]string{
		"mio":    "mio-monitored",  // a fresh monitor Task wins over the earlier running row
		"shiro":  "shiro-first",    // stale monitor state does not override Viewer order
		"kuro":   "kuro-running",   // running beats an earlier queued task; private monitor Task is ignored
		"midori": "midori-waiting", // first eligible nonterminal row after failed/terminal/unknown rows
		"coder1": "coder1-blocked", // a legacy coder remains a separate agent
	}
	gotHeads := map[string]*coreRef{}
	gotTaskRefs := map[string][]coreRef{}
	for _, agent := range wire.Agents {
		gotHeads[agent.ID] = agent.StackHead
		gotTaskRefs[agent.ID] = agent.TaskRefs
	}
	for agentID, taskID := range wantHeads {
		got := gotHeads[agentID]
		if got == nil || *got != ref("task", taskID) {
			t.Errorf("%s task_stack_head = %#v, want CORE task %q", agentID, got, taskID)
		}
	}
	if gotHeads["coder2"] != nil {
		t.Errorf("terminal-only legacy coder task_stack_head = %#v, want omitted", gotHeads["coder2"])
	}
	if len(gotTaskRefs["shiro"]) != 1 || gotTaskRefs["shiro"][0] != ref("task", "shiro-later") {
		t.Errorf("stale monitor task_refs changed: %#v", gotTaskRefs["shiro"])
	}
	if len(gotTaskRefs["kuro"]) != 0 {
		t.Errorf("private monitor Task leaked into task_refs: %#v", gotTaskRefs["kuro"])
	}
	for _, row := range wire.Work {
		if row.ID == "kuro-private" {
			t.Fatal("private Task appeared in projected work items")
		}
	}
	if strings.Contains(string(payload), "kuro-private") || strings.Contains(string(payload), "NEVER RENDER") {
		t.Fatalf("private Task leaked into snapshot JSON: %s", payload)
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatal(err)
	}
	var agentsJSON []map[string]json.RawMessage
	if err := json.Unmarshal(envelope["agents"], &agentsJSON); err != nil {
		t.Fatal(err)
	}
	for _, row := range agentsJSON {
		var id string
		if err := json.Unmarshal(row["agent_id"], &id); err != nil {
			t.Fatal(err)
		}
		if id == "coder2" {
			if _, ok := row["task_stack_head"]; ok {
				t.Errorf("empty head must be omitted for %s", id)
			}
		}
		if id == "mio" {
			var head map[string]json.RawMessage
			if err := json.Unmarshal(row["task_stack_head"], &head); err != nil {
				t.Fatal(err)
			}
			if len(head) != 3 || head["owner"] == nil || head["type"] == nil || head["id"] == nil {
				t.Errorf("task_stack_head must expose only its existing task ref: %s", row["task_stack_head"])
			}
		}
	}
}

func studioTask(id, agent, state string, updated time.Time, audience string) viewerTask {
	row := viewerTask{ID: id, Agent: agent, State: state, Updated: &updated, Title: id}
	row.Publication.Audience = audience
	return row
}

func TestSnapshotTaskStackHealthTracksTaskListAvailability(t *testing.T) {
	for _, test := range []struct {
		name            string
		taskPayload     string
		taskStatus      int
		agentsAvailable bool
		wantTaskStack   string
		wantAgents      string
	}{
		{name: "empty array succeeds without other sources", taskPayload: `{"items":[]}`, taskStatus: http.StatusOK,
			wantTaskStack: "ready", wantAgents: "unavailable"},
		{name: "null items is unavailable despite agent source", taskPayload: `{"items":null}`, taskStatus: http.StatusOK,
			agentsAvailable: true, wantTaskStack: "unavailable", wantAgents: "ready"},
		{name: "request failure is unavailable despite agent source", taskStatus: http.StatusServiceUnavailable,
			agentsAvailable: true, wantTaskStack: "unavailable", wantAgents: "ready"},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/viewer/agents":
					if !test.agentsAvailable {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					_, _ = w.Write([]byte(`{"agents":[]}`))
				case "/viewer/tasks":
					if test.taskStatus != http.StatusOK {
						w.WriteHeader(test.taskStatus)
						return
					}
					_, _ = w.Write([]byte(test.taskPayload))
				default:
					w.WriteHeader(http.StatusServiceUnavailable)
				}
			}))
			defer upstream.Close()
			origin, err := url.Parse(upstream.URL)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, ok := (source{viewer: origin, client: upstream.Client()}).snapshot(t.Context())
			if !ok {
				t.Fatal("snapshot unavailable although one independent source was available")
			}
			if got := snapshot.SourceHealth["task_stack"]; got != test.wantTaskStack {
				t.Errorf("task_stack health = %q, want %q", got, test.wantTaskStack)
			}
			if got := snapshot.SourceHealth["agents"]; got != test.wantAgents {
				t.Errorf("agents health = %q, want %q", got, test.wantAgents)
			}
		})
	}
}

func TestSnapshotAuthenticationAndPartialHealth(t *testing.T) {
	const key = "test-credential-studio-owner"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("downstream credentials must not be forwarded")
		}
		switch r.URL.Path {
		case "/viewer/agents":
			_, _ = w.Write([]byte(`{"agents":[{"id":"mio","state":"idle","preview":"hidden-preview"}]}`))
		case "/viewer/tasks":
			_, _ = w.Write([]byte(`{"items":[]}`))
		case "/viewer/workstreams":
			_, _ = w.Write([]byte(`{"workstreams":[],"artifacts":[],"goals":[]}`))
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer upstream.Close()
	origin, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	mux := handler(source{viewer: origin, client: upstream.Client()}, []byte(key))
	call := func(token, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "http://127.0.0.1:18804/snapshot", nil)
		r.RemoteAddr = "127.0.0.1:12345"
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if got := call("", ""); got.Code != 401 {
		t.Fatalf("missing auth: %d", got.Code)
	}
	if got := call(key, "https://example.invalid"); got.Code != 403 {
		t.Fatalf("cross-origin auth: %d", got.Code)
	}
	got := call(key, "")
	if got.Code != 200 || strings.Contains(got.Body.String(), "hidden-preview") ||
		!strings.Contains(got.Body.String(), `"atlas":"unavailable"`) ||
		!strings.Contains(got.Body.String(), `"agents":"ready"`) ||
		!strings.Contains(got.Body.String(), `"task_stack":"ready"`) {
		t.Fatalf("unexpected projection: %d %s", got.Code, got.Body)
	}
}

func TestSnapshotProjectsCanonicalAgentWorkWithCompleteQueues(t *testing.T) {
	const ownerToken = "core-agent-work-owner-token-012345"
	agentWorkHeaders := make(chan http.Header, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != agentWorkEndpoint && (r.Header.Get("Authorization") != "" ||
			r.Header.Get("X-RenCrow-Client") != "" || r.Header.Get("X-RenCrow-Interaction-Profile") != "") {
			t.Errorf("owner credentials were sent to unrelated CORE endpoint %q", r.URL.Path)
		}
		switch r.URL.Path {
		case "/viewer/agents":
			_, _ = w.Write([]byte(`{"agents":[{"id":"mio","state":"idle"},{"id":"shiro","state":"idle"},{"id":"kuro","state":"idle"},{"id":"midori","state":"idle"},{"id":"coder1","state":"running","task_id":"stale-coder1"},{"id":"coder2","state":"running","task_id":"stale-ao"},{"id":"coder3","state":"idle"},{"id":"coder4","state":"idle"}]}`))
		case "/viewer/tasks":
			_, _ = w.Write([]byte(`{"items":[{"task_id":"stale-coder1","title":"Legacy coder 1 task","assignee":"coder1","status":"running","origin_publication":{"audience":"public"}},{"task_id":"stale-ao","title":"Legacy coder 2 task","assignee":"coder2","status":"running","origin_publication":{"audience":"public"}}]}`))
		case "/viewer/workstreams":
			_, _ = w.Write([]byte(`{"workstreams":[],"goals":[],"artifacts":[]}`))
		case "/viewer/atlas":
			_, _ = w.Write([]byte(`{"current":[],"backlog":[],"pipeline":[]}`))
		case "/viewer/board/agent-work":
			agentWorkHeaders <- r.Header.Clone()
			_, _ = w.Write([]byte(`{"observed_at":"2026-11-15T00:00:00Z","actors":[
				{"actor_id":"mio","heartbeat_state":"working","availability":{"heartbeat":"available","tasks":"available","run_queue":"available","board_inbox":"available","foreground_waiters":"available"},"queue_completeness":"complete","activity_kind":"scheduled_task","activity_title":"Mio schedule slot","current_tasks":[{"task_id":"mio-current","title":"Mio current","status":"running","queue_class":"task_only"}],"waiting_queue":[{"task_id":"mio-scheduled","title":"Scheduled","status":"queued","queue_class":"scheduled","run_queue_status":"queued"},{"task_id":"mio-board","title":"BOARD task","status":"waiting","queue_class":"task_only"}],"board_inbox_queue":[{"work_id":"inbox-1","title":"A bounded pending instruction excerpt","priority":"high","created_at":"2026-11-15T00:00:01Z","not_before":"2026-11-15T00:05:00Z","body":"private inbox body must not enter Studio DTO"}],"foreground_waiters":{"count":2,"items":[{"position":1,"priority":"high"},{"position":2,"priority":"normal"}]}},
				{"actor_id":"shiro","heartbeat_state":"idle","availability":{"heartbeat":"available","tasks":"available","run_queue":"available","board_inbox":"available","foreground_waiters":"available"},"queue_completeness":"complete","current_tasks":[],"waiting_queue":[],"board_inbox_queue":[],"foreground_waiters":{"count":0,"items":[]}},
				{"actor_id":"kuro","heartbeat_state":"idle","availability":{"heartbeat":"available","tasks":"available","run_queue":"available","board_inbox":"available","foreground_waiters":"available"},"queue_completeness":"complete","current_tasks":[],"waiting_queue":[],"board_inbox_queue":[],"foreground_waiters":{"count":0,"items":[]}},
				{"actor_id":"midori","heartbeat_state":"idle","availability":{"heartbeat":"available","tasks":"available","run_queue":"available","board_inbox":"available","foreground_waiters":"available"},"queue_completeness":"complete","current_tasks":[],"waiting_queue":[],"board_inbox_queue":[],"foreground_waiters":{"count":0,"items":[]}},
				{"actor_id":"aka","heartbeat_state":"working","availability":{"heartbeat":"available","tasks":"available","run_queue":"available","board_inbox":"available","foreground_waiters":"available"},"queue_completeness":"complete","current_tasks":[{"task_id":"aka-current","title":"Aka current","status":"running","queue_class":"task_only"}],"waiting_queue":[{"task_id":"aka-wait","title":"Aka queued","status":"queued","queue_class":"scheduled"}],"board_inbox_queue":[],"foreground_waiters":{"count":0,"items":[]}},
				{"actor_id":"ao","heartbeat_state":"idle","availability":{"heartbeat":"available","tasks":"unavailable","run_queue":"available","board_inbox":"not_configured","foreground_waiters":"unavailable"},"queue_completeness":"incomplete","current_tasks":null,"waiting_queue":null,"board_inbox_queue":null,"foreground_waiters":null},
				{"actor_id":"kin","heartbeat_state":"idle","availability":{"heartbeat":"available","tasks":"available","run_queue":"unavailable","board_inbox":"available","foreground_waiters":"available"},"queue_completeness":"incomplete","current_tasks":[],"waiting_queue":[{"task_id":"kin-queue-unknown","title":"Kin queue record","status":"queued","queue_class":"unknown"}],"board_inbox_queue":[],"foreground_waiters":{"count":0,"items":[]}},
				{"actor_id":"gin","heartbeat_state":"idle","availability":{"heartbeat":"available","tasks":"available","run_queue":"available","board_inbox":"available","foreground_waiters":"available"},"queue_completeness":"complete","current_tasks":[],"waiting_queue":[],"board_inbox_queue":[],"foreground_waiters":{"count":0,"items":[]}}
			]}`))
		default:
			t.Errorf("unexpected CORE request path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	origin, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, ok := (source{viewer: origin, client: upstream.Client(), ownerToken: []byte(ownerToken)}).snapshot(t.Context())
	if !ok {
		t.Fatal("snapshot unavailable although CORE returned valid source data")
	}
	select {
	case headers := <-agentWorkHeaders:
		if got := headers.Get("Authorization"); got != "Bearer "+ownerToken {
			t.Errorf("agent-work Authorization = %q, want owner Bearer token", got)
		}
		if got := headers.Get("X-RenCrow-Client"); got != "RenCrow_BOARD" {
			t.Errorf("agent-work client = %q, want RenCrow_BOARD", got)
		}
		if got := headers.Get("X-RenCrow-Interaction-Profile"); got != "board-studio-readonly" {
			t.Errorf("agent-work interaction profile = %q", got)
		}
	default:
		t.Error("CORE agent-work endpoint was not requested")
	}
	var wire struct {
		Agents              []map[string]json.RawMessage `json:"agents"`
		AgentWorkObservedAt *time.Time                   `json:"agent_work_observed_at"`
		Work                []studioWork                 `json:"work_items"`
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire.Agents) != 12 {
		t.Fatalf("projected agents = %d, want eight canonical actors and four separate legacy rows", len(wire.Agents))
	}
	if wire.AgentWorkObservedAt == nil || !wire.AgentWorkObservedAt.Equal(time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("agent work observation time not preserved: %v", wire.AgentWorkObservedAt)
	}
	byID := make(map[string]map[string]json.RawMessage, len(wire.Agents))
	for _, agent := range wire.Agents {
		var id string
		if err := json.Unmarshal(agent["agent_id"], &id); err != nil {
			t.Fatalf("agent id missing: %s", agent["agent_id"])
		}
		byID[id] = agent
	}
	for _, id := range []string{"mio", "shiro", "kuro", "midori", "aka", "ao", "kin", "gin"} {
		if _, ok := byID[id]; !ok {
			t.Errorf("canonical actor %q missing", id)
		}
	}
	for _, id := range []string{"aka", "ao", "kin", "gin"} {
		if string(byID[id]["role"]) != `"エージェント"` {
			t.Errorf("canonical actor %s role = %s, want エージェント", id, byID[id]["role"])
		}
	}
	if string(byID["coder1"]["role"]) != `"Coder"` {
		t.Errorf("legacy coder role = %s, want Coder", byID["coder1"]["role"])
	}
	aka := byID["aka"]
	var akaAvailability agentWorkAvailability
	if err := json.Unmarshal(aka["availability"], &akaAvailability); err != nil {
		t.Fatal(err)
	}
	if len(aka["current_tasks"]) == 0 || len(aka["task_queue"]) == 0 || string(aka["actor_id"]) != `"aka"` ||
		string(aka["heartbeat_state"]) != `"working"` ||
		akaAvailability != (agentWorkAvailability{Heartbeat: "available", Tasks: "available", RunQueue: "available", BoardInbox: "available", ForegroundWaiters: "available"}) ||
		string(aka["queue_completeness"]) != `"complete"` {
		t.Errorf("aka actor work fields missing or truncated: %+v", aka)
	}
	var currentTasks []struct {
		ID string `json:"task_id"`
	}
	if err := json.Unmarshal(aka["current_tasks"], &currentTasks); err != nil || len(currentTasks) != 1 || currentTasks[0].ID != "aka-current" {
		t.Errorf("aka current task record lost: %s (err=%v)", aka["current_tasks"], err)
	}
	shiro := byID["shiro"]
	if string(shiro["current_tasks"]) != `[]` || string(shiro["task_queue"]) != `[]` || string(shiro["board_inbox_queue"]) != `[]` {
		t.Errorf("available empty queue arrays must remain empty arrays in the Studio DTO: current=%s task=%s inbox=%s", shiro["current_tasks"], shiro["task_queue"], shiro["board_inbox_queue"])
	}
	var shiroWaiters agentForegroundWaiters
	if err := json.Unmarshal(shiro["foreground_waiters"], &shiroWaiters); err != nil || shiroWaiters.Count != 0 || shiroWaiters.Items == nil {
		t.Errorf("available empty foreground waiters must remain count=0/items=[]: %s (err=%v)", shiro["foreground_waiters"], err)
	}
	var inboxRows []struct {
		WorkID    string     `json:"work_id"`
		Title     string     `json:"title"`
		Priority  string     `json:"priority"`
		CreatedAt time.Time  `json:"created_at"`
		NotBefore *time.Time `json:"not_before"`
	}
	mio := byID["mio"]
	if err := json.Unmarshal(mio["board_inbox_queue"], &inboxRows); err != nil || len(inboxRows) != 1 ||
		inboxRows[0].WorkID != "inbox-1" || inboxRows[0].Title != "A bounded pending instruction excerpt" ||
		inboxRows[0].Priority != "high" || inboxRows[0].CreatedAt.IsZero() || inboxRows[0].NotBefore == nil {
		t.Errorf("available board inbox queue record not preserved: %s (err=%v)", mio["board_inbox_queue"], err)
	}
	var mioWaiters agentForegroundWaiters
	if err := json.Unmarshal(mio["foreground_waiters"], &mioWaiters); err != nil || mioWaiters.Count != 2 || len(mioWaiters.Items) != 2 ||
		mioWaiters.Items[0].Position != 1 || mioWaiters.Items[0].Priority != "high" || mioWaiters.Items[1].Position != 2 || mioWaiters.Items[1].Priority != "normal" {
		t.Errorf("foreground waiter priority order not preserved: %s (err=%v)", mio["foreground_waiters"], err)
	}
	if strings.Contains(string(aka["current_tasks"]), "stale-coder1") {
		t.Errorf("legacy coder task was attributed to aka: %s", aka["current_tasks"])
	}
	ao := byID["ao"]
	var aoAvailability agentWorkAvailability
	if err := json.Unmarshal(ao["availability"], &aoAvailability); err != nil {
		t.Fatal(err)
	}
	if aoAvailability.Tasks != "unavailable" || aoAvailability.BoardInbox != "not_configured" ||
		aoAvailability.ForegroundWaiters != "unavailable" || string(ao["queue_completeness"]) != `"incomplete"` ||
		len(ao["current_tasks"]) != 0 || len(ao["task_queue"]) != 0 || len(ao["board_inbox_queue"]) != 0 || len(ao["foreground_waiters"]) != 0 ||
		string(ao["task_refs"]) != `[]` || len(ao["task_stack_head"]) != 0 {
		t.Errorf("unavailable actor should omit unknown queues and stale monitor tasks: %+v", ao)
	}
	if len(wire.Work) != 2 {
		t.Errorf("legacy public task projections were lost: %+v", wire.Work)
	}
	wantWorkOwners := map[string]string{"stale-coder1": "coder1", "stale-ao": "coder2"}
	for _, task := range wire.Work {
		if len(task.Agents) != 1 || wantWorkOwners[task.ID] != task.Agents[0] {
			t.Errorf("legacy Task ownership changed: %+v", task)
		}
	}
	var coder1Head coreRef
	if err := json.Unmarshal(byID["coder1"]["task_stack_head"], &coder1Head); err != nil {
		t.Fatal(err)
	}
	var coder2Head coreRef
	if err := json.Unmarshal(byID["coder2"]["task_stack_head"], &coder2Head); err != nil {
		t.Fatal(err)
	}
	if string(byID["coder1"]["actor_kind"]) != `"legacy"` || coder1Head.ID != "stale-coder1" ||
		coder1Head.ID == "aka-current" || string(byID["coder1"]["actor_id"]) != "" ||
		string(byID["coder2"]["actor_kind"]) != `"legacy"` || coder2Head.ID != "stale-ao" {
		t.Errorf("legacy coder rows should remain separate with their own task refs: coder1=%+v coder2=%+v", byID["coder1"], byID["coder2"])
	}
	var queue []json.RawMessage
	if err := json.Unmarshal(mio["task_queue"], &queue); err != nil || len(queue) != 2 {
		t.Errorf("mio task_queue must retain both scheduled and task-only rows: %s (err=%v)", mio["task_queue"], err)
	} else if !strings.Contains(string(mio["task_queue"]), `"run_queue_status":"queued"`) {
		t.Errorf("mio scheduled task run_queue_status was dropped: %s", mio["task_queue"])
	}
	kin := byID["kin"]
	if !strings.Contains(string(kin["availability"]), `"run_queue":"unavailable"`) ||
		!strings.Contains(string(kin["task_queue"]), `"queue_class":"unknown"`) ||
		string(kin["queue_completeness"]) != `"incomplete"` {
		t.Errorf("task rows should remain visible with explicit run queue unavailability: %+v", kin)
	}
	if string(mio["activity_kind"]) != `"scheduled_task"` || string(mio["activity_title"]) != `"Mio schedule slot"` {
		t.Errorf("scheduler slot activity fields not preserved: %+v", mio)
	}
	if strings.Contains(string(data), ownerToken) {
		t.Error("CORE owner token leaked into the Studio snapshot")
	}
	if strings.Contains(string(data), "private inbox body must not enter Studio DTO") {
		t.Error("BOARD inbox body leaked into the Studio queue DTO")
	}
}

func TestSnapshotKeepsStudioProjectionWhenOwnerAgentWorkIsUnavailable(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/viewer/agents":
			_, _ = w.Write([]byte(`{"agents":[{"id":"coder1","state":"running","task_id":"stale-task"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	origin, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, ok := (source{viewer: origin, client: upstream.Client(), ownerToken: []byte("core-agent-work-owner-token-012345")}).snapshot(t.Context())
	if !ok {
		t.Fatal("Studio snapshot should remain available from the independent agent monitor source")
	}
	if snapshot.SourceHealth["agent_work"] != "unavailable" || len(snapshot.Agents) != 9 {
		t.Fatalf("agent-work failure dropped Studio projection or missed health: %+v", snapshot)
	}
	byID := map[string]studioAgent{}
	for _, agent := range snapshot.Agents {
		byID[agent.ID] = agent
	}
	for _, id := range canonicalActorIDs {
		agent, ok := byID[id]
		wantAvailability := &agentWorkAvailability{
			Heartbeat: "unavailable", Tasks: "unavailable", RunQueue: "unavailable",
			BoardInbox: "unavailable", ForegroundWaiters: "unavailable",
		}
		if !ok || agent.ActorID != id || agent.State != "unavailable" || agent.HeartbeatState != "unavailable" ||
			agent.Availability == nil || *agent.Availability != *wantAvailability ||
			agent.QueueCompleteness != "incomplete" || agent.TaskStackHead != nil || len(agent.TaskRefs) != 0 ||
			agent.CurrentTasks != nil || agent.TaskQueue != nil || agent.BoardInboxQueue != nil || agent.ForegroundWaiters != nil {
			t.Errorf("owner-unavailable placeholder for %s has inferred data: %+v", id, agent)
		}
	}
	legacy, ok := byID["coder1"]
	if !ok || legacy.Kind != "legacy" || legacy.ActorID != "" || legacy.HeartbeatState == "unavailable" {
		t.Errorf("legacy coder row should remain distinct from canonical placeholders: %+v", legacy)
	}
}

func TestProjectAgentWorkUsesOwnerObservationAndHeartbeatAvailability(t *testing.T) {
	observed := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	makeActor := func(actorID, state, heartbeatAvailability string) viewerAgentWorkActor {
		return viewerAgentWorkActor{
			ActorID: actorID, HeartbeatState: state,
			Availability: agentWorkAvailability{
				Heartbeat: heartbeatAvailability, Tasks: "unavailable", RunQueue: "unavailable",
				BoardInbox: "not_configured", ForegroundWaiters: "not_configured",
			},
			QueueCompleteness: "incomplete",
		}
	}
	work := viewerAgentWork{
		ObservedAt: observed,
		Actors: []viewerAgentWorkActor{
			makeActor("mio", "waiting", "available"),
			makeActor("shiro", "running", "available"),
			makeActor("kuro", "checking", "available"),
			makeActor("midori", "stopping", "available"),
			makeActor("aka", "unavailable", "unavailable"),
			makeActor("ao", "idle", "available"),
			makeActor("kin", "idle", "available"),
			makeActor("gin", "idle", "available"),
		},
	}
	snapshot := studioSnapshot{Observed: observed.Add(10 * time.Second), Agents: []studioAgent{}}
	projectAgentWork(&snapshot, work)
	byID := make(map[string]studioAgent, len(snapshot.Agents))
	for _, agent := range snapshot.Agents {
		byID[agent.ID] = agent
	}
	wantStates := map[string]string{
		"mio": "idle", "shiro": "working", "kuro": "thinking", "midori": "working", "aka": "unavailable",
	}
	for actorID, wantState := range wantStates {
		agent, ok := byID[actorID]
		if !ok {
			t.Fatalf("projected actor %q is missing", actorID)
		}
		if agent.State != wantState {
			t.Errorf("actor %s state = %q, want %q", actorID, agent.State, wantState)
		}
		wantFreshness := "fresh"
		if actorID == "aka" {
			wantFreshness = "unknown"
		}
		if agent.Freshness != wantFreshness {
			t.Errorf("actor %s freshness = %q, want %q", actorID, agent.Freshness, wantFreshness)
		}
		if agent.Observed == nil || !agent.Observed.Equal(observed) {
			t.Errorf("actor %s observed_at = %v, want owner observation %s", actorID, agent.Observed, observed)
		}
	}

	staleWork := work
	staleWork.ObservedAt = snapshot.Observed.Add(-91 * time.Second)
	staleSnapshot := studioSnapshot{Observed: snapshot.Observed, Agents: []studioAgent{}}
	projectAgentWork(&staleSnapshot, staleWork)
	for _, agent := range staleSnapshot.Agents {
		wantFreshness := "stale"
		if agent.Availability.Heartbeat == "unavailable" {
			wantFreshness = "unknown"
		}
		if agent.Freshness != wantFreshness {
			t.Errorf("stale observation actor %s freshness = %q, want %q", agent.ID, agent.Freshness, wantFreshness)
		}
	}
}

func TestValidAgentWorkAcceptsOnlyBoundedNonblankInboxTitles(t *testing.T) {
	newWork := func(title string) viewerAgentWork {
		observed := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
		work := viewerAgentWork{ObservedAt: observed, Actors: make([]viewerAgentWorkActor, 0, len(canonicalActorIDs))}
		for _, actorID := range canonicalActorIDs {
			actor := viewerAgentWorkActor{
				ActorID: actorID,
				Availability: agentWorkAvailability{
					Heartbeat: "available", Tasks: "available", RunQueue: "available",
					BoardInbox: "available", ForegroundWaiters: "available",
				},
				QueueCompleteness: "complete",
				CurrentTasks:      []json.RawMessage{},
				WaitingQueue:      []json.RawMessage{},
				BoardInboxQueue:   []agentBoardInboxItem{},
				ForegroundWaiters: &agentForegroundWaiters{Count: 0, Items: []agentForegroundWaiter{}},
			}
			if actorID == canonicalActorIDs[0] {
				actor.BoardInboxQueue = []agentBoardInboxItem{{
					WorkID: "board-inbox:item-1", Title: title, Priority: "normal", CreatedAt: observed,
				}}
			}
			work.Actors = append(work.Actors, actor)
		}
		return work
	}

	tests := []struct {
		name  string
		title string
		want  bool
	}{
		{name: "bounded instruction excerpt", title: "A pending instruction excerpt", want: true},
		{name: "markup-like title remains data", title: "<script>alert(1)</script>", want: true},
		{name: "160 Unicode code points", title: strings.Repeat("指", maxBoardInboxTitleRunes), want: true},
		{name: "empty title", title: "", want: false},
		{name: "whitespace only", title: " \t ", want: false},
		{name: "line break", title: "First line\nsecond line", want: false},
		{name: "over 160 Unicode code points", title: strings.Repeat("指", maxBoardInboxTitleRunes+1), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := validAgentWork(newWork(test.title)); got != test.want {
				t.Errorf("validAgentWork(title length %d) = %t, want %t", len([]rune(test.title)), got, test.want)
			}
		})
	}
}

func TestReadOwnerTokenUsesProtectedTokenValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-ops.token")
	const token = "core-agent-work-owner-token-012345"
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readOwnerToken(path)
	if err != nil || string(got) != token {
		t.Fatalf("readOwnerToken() = %q, %v", got, err)
	}
}
