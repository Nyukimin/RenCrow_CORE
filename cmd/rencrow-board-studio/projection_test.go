package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	work := viewerWorkstreams{
		Workstreams: []viewerProject{{ID: "project", Title: "Live Workstream", State: "active", Agent: "mio"}},
		Artifacts:   []viewerArtifact{{ID: "artifact", Project: "project", Title: "Registered output", State: "completed"}},
	}
	atlas := viewerAtlas{
		Backlog: []atlasItem{{ID: "item", Project: "project", Unit: "unit", Revision: 2, Title: "Current Atlas work"}},
		Pipeline: []atlasPipeline{
			{ID: "unit", Item: "item", Revision: 1, State: "DONE", Stages: []atlasStage{{Code: "DONE", State: "passed"}}},
			{ID: "unit", Item: "item", Revision: 2, State: "BLOCKED", InvalidatedStage: "BUILD",
				Stages: []atlasStage{{Code: "SPEC", State: "passed"}, {Code: "BUILD", State: "blocked", Reason: "build_failed"}}},
		},
	}
	s := projectStudio(now, actors, viewerTasks{Items: []viewerTask{public, private}}, work, atlas)
	if len(s.Work) != 2 || s.Work[0].State != "blocked" || s.Work[1].ID != "public" || len(s.Work[1].Dependencies) != 0 {
		t.Fatalf("unexpected projected work: %+v", s.Work)
	}
	if len(s.Projects) != 1 || s.Projects[0].State != "blocked" || s.Projects[0].Completion.Done != 1 ||
		s.Projects[0].Completion.Total != 2 || len(s.Blockers) != 1 || *s.Blockers[0].Reason != "build_failed" {
		t.Fatalf("unexpected Atlas projection: %+v %+v", s.Projects, s.Blockers)
	}
	if len(s.Agents[0].TaskRefs) != 1 || len(s.Agents[1].TaskRefs) != 0 || s.Agents[1].Freshness != "stale" {
		t.Fatalf("unexpected agent references: %+v", s.Agents)
	}
	if len(s.Artifacts) != 1 || s.Artifacts[0].State != "created" {
		t.Fatalf("registration promoted to verification: %+v", s.Artifacts)
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
		"coder1": "coder1-blocked", // blocked is an eligible fallback
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
		t.Errorf("terminal-only agent task_stack_head = %#v, want omitted", gotHeads["coder2"])
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
