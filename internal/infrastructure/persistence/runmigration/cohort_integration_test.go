package runmigration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	idlechat "github.com/Nyukimin/RenCrow_CORE/internal/application/idlechat"
	browserdomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/browsertrace"
	knowledgedomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
	superdomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	taskdomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	browserstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/browsertrace"
	eventstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	knowledgestore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/knowledgememory"
	superstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/superagent"
	taskstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	core "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type fullCohortFixture struct {
	options   Options
	taskID    core.TaskID
	runID     core.RunID
	childTask core.TaskID
	childRun  core.RunID
	traceID   core.TraceID
	eventID   core.EventID
	prose     []string
}

func TestMigrationFullCohortDryRunApplyNoopAndOwnerReload(t *testing.T) {
	fixture := newFullCohortFixture(t)
	ctx := context.Background()

	first, err := Run(ctx, fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Run(ctx, fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "ready" || second.Status != "ready" || !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated dry-run receipts differ: first=%#v second=%#v", first, second)
	}
	if _, err := os.Stat(fixture.options.Target); !os.IsNotExist(err) {
		t.Fatalf("dry-run published target: %v", err)
	}

	apply := fixture.options
	apply.Mode = "apply"
	apply.Expected = &first
	applied, err := Run(ctx, apply)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Status != "applied" {
		t.Fatalf("apply status = %q, want applied", applied.Status)
	}
	noop, err := Run(ctx, apply)
	if err != nil {
		t.Fatal(err)
	}
	if noop.Status != "noop" {
		t.Fatalf("second apply status = %q, want noop", noop.Status)
	}

	assertCanonicalOwnerReload(t, fixture, ctx)
	assertCohortProsePreserved(t, fixture)
}

func TestMigrationRejectsPrimaryKeyPayloadMismatch(t *testing.T) {
	fixture := newFullCohortFixture(t)
	path := filepath.Join(fixture.options.Snapshot, "superagent.sqlite")
	db := openMigrationFixtureDB(t, path)
	if _, err := db.Exec(`UPDATE agent_run SET run_id = ? WHERE run_id = ?`, "wrong-primary", string(fixture.runID)); err != nil {
		db.Close()
		t.Fatal(err)
	}
	closeMigrationFixtureDB(t, db)
	refreshFixtureHash(t, &fixture.options, "superagent.sqlite")

	if _, err := Run(context.Background(), fixture.options); err == nil {
		t.Fatal("primary-key/payload mismatch was accepted")
	}
}

func TestMigrationRejectsSourceRunTaskActorMismatch(t *testing.T) {
	fixture := newFullCohortFixture(t)
	path := filepath.Join(fixture.options.Snapshot, "tasks", "task_run.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var run taskdomain.Run
	if err := json.Unmarshal(bytes.TrimSpace(raw), &run); err != nil {
		t.Fatal(err)
	}
	run.Assignee = "Mio"
	encoded, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	refreshFixtureHash(t, &fixture.options, "tasks/task_run.jsonl")

	if _, err := Run(context.Background(), fixture.options); err == nil {
		t.Fatal("source Run/Task actor mismatch was accepted")
	}
}

func TestMigrationRejectsOrphanEventWithTaskOnly(t *testing.T) {
	fixture := newFullCohortFixture(t)
	mutateFixtureEvent(t, &fixture.options, fixture.eventID, core.NewTaskID(), false)

	if _, err := Run(context.Background(), fixture.options); err == nil {
		t.Fatal("orphan Task-only event was accepted")
	}
}

func TestMigrationAcceptsTaskOnlyEventForKnownTask(t *testing.T) {
	fixture := newFullCohortFixture(t)
	mutateFixtureEvent(t, &fixture.options, fixture.eventID, fixture.taskID, false)

	receipt, err := Run(context.Background(), fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "ready" {
		t.Fatalf("known Task-only event status = %q, want ready", receipt.Status)
	}
}

func TestMigrationRejectsOpaqueEventExecutionReference(t *testing.T) {
	f := newFullCohortFixture(t)
	p := filepath.Join(f.options.Snapshot, "events.sqlite")
	db := openMigrationFixtureDB(t, p)
	defer db.Close()
	var raw []byte
	if err := db.QueryRow("SELECT envelope_json FROM event_envelope WHERE event_id=?", string(f.eventID)).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	fields["payload"] = json.RawMessage(`{"run_id":"legacy-execution"}`)
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("DROP TRIGGER event_envelope_append_only_update"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE event_envelope SET envelope_json=? WHERE event_id=?", string(raw), string(f.eventID)); err != nil {
		t.Fatal(err)
	}
	closeMigrationFixtureDB(t, db)
	refreshFixtureHash(t, &f.options, "events.sqlite")
	if r, err := Run(context.Background(), f.options); err == nil || r.Status != "blocked" {
		t.Fatalf("opaque execution ref accepted: %#v %v", r, err)
	}
}

func TestMigrationRejectsUnreconciledCheckpointSuccessor(t *testing.T) {
	f := newFullCohortFixture(t)
	p := filepath.Join(f.options.Snapshot, "tasks", "task_run.jsonl")
	b := mustReadMigrationFixtureFile(t, p)
	var successor taskdomain.Run
	if err := json.Unmarshal(bytes.TrimSpace(b), &successor); err != nil {
		t.Fatal(err)
	}
	successor.RunID = core.NewRunID()
	successor.StartedAt = successor.StartedAt.Add(time.Minute)
	end := successor.StartedAt.Add(time.Minute)
	successor.CompletedAt = &end
	successor.StartReason = taskdomain.RunStartReasonExplicitRerun
	b = append(b, marshalLine(successor)...)
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	refreshFixtureHash(t, &f.options, "tasks/task_run.jsonl")
	if r, err := Run(context.Background(), f.options); err == nil || r.Status != "blocked" {
		t.Fatalf("unreconciled checkpoint accepted: %#v %v", r, err)
	}
}

func TestMigrationRejectsUnknownActorInUnreferencedCanonicalRun(t *testing.T) {
	f := newFullCohortFixture(t)
	input := map[string][]byte{}
	inv := f.options.Inventory
	for role, p := range inv.Roles {
		if role != "tasks" && role != "runs" {
			inv.Roles[role] = ""
			continue
		}
		input[p] = mustReadMigrationFixtureFile(t, filepath.Join(f.options.Snapshot, filepath.FromSlash(p)))
	}
	var run taskdomain.Run
	if err := json.Unmarshal(bytes.TrimSpace(input[inv.Roles["runs"]]), &run); err != nil {
		t.Fatal(err)
	}
	run.Assignee = "worker"
	input[inv.Roles["runs"]] = marshalLine(run)
	if _, _, err := buildCohort(context.Background(), inv, input, "dry-run"); err == nil {
		t.Fatal("mechanism identity in unreferenced canonical Run accepted")
	}
}

func TestMigrationRejectsUnknownProjectionField(t *testing.T) {
	fixture := newFullCohortFixture(t)
	path := filepath.Join(fixture.options.Snapshot, "superagent.sqlite")
	db := openMigrationFixtureDB(t, path)
	var raw string
	if err := db.QueryRow(`SELECT payload FROM agent_run WHERE run_id = ?`, string(fixture.runID)).Scan(&raw); err != nil {
		db.Close()
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		db.Close()
		t.Fatal(err)
	}
	payload["unknown_projection_field"] = "reject me"
	mutated, err := json.Marshal(payload)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE agent_run SET payload = ? WHERE run_id = ?`, string(mutated), string(fixture.runID)); err != nil {
		db.Close()
		t.Fatal(err)
	}
	closeMigrationFixtureDB(t, db)
	refreshFixtureHash(t, &fixture.options, "superagent.sqlite")

	if _, err := Run(context.Background(), fixture.options); err == nil {
		t.Fatal("unknown projection field was accepted")
	}
}

func assertCanonicalOwnerReload(t *testing.T, fixture fullCohortFixture, ctx context.Context) {
	t.Helper()
	taskReader, err := taskstore.NewJSONLReader(filepath.Join(fixture.options.Target, "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	defer taskReader.Close()
	tasks, err := taskReader.ListTasks(ctx, taskdomain.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	runs, err := taskReader.ListRuns(ctx, taskdomain.RunFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 || len(runs) != 2 {
		t.Fatalf("canonical task owner counts = tasks %d runs %d, want 2/2", len(tasks), len(runs))
	}
	var foundChild bool
	for _, item := range tasks {
		if item.TaskID == fixture.childTask {
			foundChild = item.ParentTaskID == fixture.taskID && item.Assignee == "Shiro"
		}
	}
	if !foundChild {
		t.Fatalf("legacy child Task was not linked to parent: %#v", tasks)
	}

	super, err := superstore.NewSQLiteStore(filepath.Join(fixture.options.Target, "superagent.sqlite"), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer super.Close()
	agents, err := super.ListAgentRuns(ctx, 10)
	if err != nil || len(agents) != 1 || agents[0].RunID != fixture.runID || agents[0].ActorID != "shiro" {
		t.Fatalf("superagent reload = %#v err=%v", agents, err)
	}
	children, err := super.ListSubagentTasks(ctx, 10)
	if err != nil || len(children) != 1 || children[0].TaskID != fixture.childTask || children[0].RunID != fixture.childRun {
		t.Fatalf("legacy child reload = %#v err=%v", children, err)
	}

	browser, err := browserstore.NewSQLiteStore(filepath.Join(fixture.options.Target, "browser.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer browser.Close()
	traces, err := browser.ListTraceRuns(ctx, 10)
	if err != nil || len(traces) != 1 || traces[0].TaskID != fixture.taskID || traces[0].RunID != fixture.runID || traces[0].ActorID != "shiro" {
		t.Fatalf("browser reload = %#v err=%v", traces, err)
	}

	knowledge, err := knowledgestore.NewSQLiteStore(filepath.Join(fixture.options.Target, "knowledge.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer knowledge.Close()
	dreams, err := knowledge.ListDreamConsolidationRuns(ctx, 10)
	if err != nil || len(dreams) != 1 || dreams[0].TaskID != fixture.taskID || dreams[0].RunID != fixture.runID || dreams[0].ActorID != "shiro" {
		t.Fatalf("knowledge reload = %#v err=%v", dreams, err)
	}

	events, err := eventstore.NewSQLiteStore(filepath.Join(fixture.options.Target, "events.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	event, found, err := events.GetByID(ctx, fixture.eventID)
	if err != nil || !found || event.TaskID != fixture.taskID || event.RunID != fixture.runID || event.ActorID != "shiro" {
		t.Fatalf("event reload = %#v found=%v err=%v", event, found, err)
	}

	checkpointStore := idlechat.NewGenerationCheckpointStore(filepath.Join(fixture.options.Target, "checkpoints.json"))
	checkpoint, checkpointFound := checkpointStore.Get("checkpoint-1")
	if !checkpointFound || checkpoint.TaskID != fixture.taskID || checkpoint.RunID != fixture.runID {
		t.Fatalf("checkpoint reload = %#v", checkpoint)
	}
	for _, path := range []string{"word.json", "forecast.json", "story.jsonl", "dialogue.jsonl"} {
		if _, err := os.Stat(filepath.Join(fixture.options.Target, path)); err != nil {
			t.Fatalf("missing IdleChat output %s: %v", path, err)
		}
	}
}

func assertCohortProsePreserved(t *testing.T, fixture fullCohortFixture) {
	t.Helper()
	var output bytes.Buffer
	for _, path := range []string{
		"tasks/task_state.jsonl", "superagent.sqlite", "browser.sqlite", "knowledge.sqlite",
		"word.json", "forecast.json", "story.jsonl", "dialogue.jsonl", "checkpoints.json",
	} {
		data, err := os.ReadFile(filepath.Join(fixture.options.Target, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		output.Write(data)
	}
	for _, prose := range fixture.prose {
		if !bytes.Contains(output.Bytes(), []byte(prose)) {
			t.Errorf("payload prose %q was not preserved", prose)
		}
	}
}

func newFullCohortFixture(t *testing.T) fullCohortFixture {
	t.Helper()
	root := t.TempDir()
	tmp := filepath.Join(root, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", tmp)
	snapshot := filepath.Join(root, "snapshot")
	if err := os.Mkdir(snapshot, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	completed := now.Add(5 * time.Minute)
	taskID := core.NewTaskID()
	runID := core.NewRunID()
	childRunString, err := core.NewMigrationID(core.CanonicalRunID, "superagent", "subagent_id", "legacy-child-1")
	if err != nil {
		t.Fatal(err)
	}
	childTaskString, err := core.NewMigrationID(core.CanonicalTaskID, "superagent", "subagent_id", "legacy-child-1")
	if err != nil {
		t.Fatal(err)
	}
	fixture := fullCohortFixture{taskID: taskID, runID: runID, childTask: core.TaskID(childTaskString), childRun: core.RunID(childRunString), prose: []string{
		"preserve agent prose", "preserve browser prose", "preserve knowledge prose", "word payload prose", "forecast payload prose", "story payload prose", "dialogue payload prose", "checkpoint payload prose",
	}}

	rolesMap := make(map[string]string, len(roles))
	for _, role := range roles {
		rolesMap[role] = ""
	}
	files := map[string]string{}
	addSnapshot := func(role, path string, data []byte) {
		t.Helper()
		writeMigrationTestFile(t, snapshot, path, data)
		rolesMap[role] = path
		files[path] = digest(data)
	}

	generatedTaskRoot := filepath.Join(root, "generated-task-store")
	taskStore, err := taskstore.NewJSONLStore(generatedTaskRoot)
	if err != nil {
		t.Fatal(err)
	}
	canonicalTask := taskdomain.Task{
		TaskID: taskID, Title: "Fixture task", Route: taskdomain.RouteGeneral, Assignee: "Shiro",
		Status: taskdomain.StatusSucceeded, Priority: taskdomain.PriorityNormal, InterruptPolicy: taskdomain.InterruptSilent,
		CreatedAt: now, UpdatedAt: completed, StartedAt: &now, FinishedAt: &completed, Summary: "preserve task prose",
	}
	if err := taskStore.SaveTask(context.Background(), canonicalTask); err != nil {
		taskStore.Close()
		t.Fatal(err)
	}
	generation, err := taskStore.WriterGeneration()
	if err != nil {
		taskStore.Close()
		t.Fatal(err)
	}
	canonicalRun := taskdomain.Run{WriterGeneration: generation, RunID: runID, TaskID: taskID, StartReason: taskdomain.RunStartReasonFirst, Assignee: "Shiro", Status: taskdomain.RunStatusSucceeded, StartedAt: now, CompletedAt: &completed, Summary: "preserve run prose"}
	if err := taskStore.SaveRun(context.Background(), canonicalRun); err != nil {
		taskStore.Close()
		t.Fatal(err)
	}
	if err := taskStore.SaveContext(context.Background(), taskdomain.SharedRoleContext{TaskID: taskID, UserIntent: "preserve context prose", UpdatedAt: completed}); err != nil {
		taskStore.Close()
		t.Fatal(err)
	}
	if err := taskStore.SaveNotification(context.Background(), taskdomain.Notification{Type: "run.completed", Level: taskdomain.NotificationDone, TaskID: taskID, Title: "preserve notification prose", Assignee: "Shiro", Route: taskdomain.RouteGeneral, Status: taskdomain.StatusSucceeded, Summary: "preserve notification prose", Interrupt: true, CreatedAt: completed}); err != nil {
		taskStore.Close()
		t.Fatal(err)
	}
	if err := taskStore.Close(); err != nil {
		t.Fatal(err)
	}
	for role, filename := range map[string]string{
		"tasks":         "task_state.jsonl",
		"runs":          "task_run.jsonl",
		"contexts":      "task_context.jsonl",
		"notifications": "task_notifications.jsonl",
	} {
		data, err := os.ReadFile(filepath.Join(generatedTaskRoot, filename))
		if err != nil {
			t.Fatal(err)
		}
		addSnapshot(role, filepath.Join("tasks", filename), data)
	}

	superPath := filepath.Join(snapshot, "superagent.sqlite")
	super, err := superstore.NewSQLiteStore(superPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := super.SaveAgentRun(context.Background(), superdomain.AgentRun{RunID: runID, TaskID: taskID, ActorID: "shiro", Goal: "preserve agent prose", Status: "completed", StartedAt: now, CompletedAt: completed, Summary: "preserve agent prose"}); err != nil {
		super.Close()
		t.Fatal(err)
	}
	if err := super.Close(); err != nil {
		t.Fatal(err)
	}
	addLegacySubagentFixture(t, superPath, runID, now, completed)
	addSnapshot("superagent", "superagent.sqlite", mustReadMigrationFixtureFile(t, superPath))

	browserPath := filepath.Join(snapshot, "browser.sqlite")
	browser, err := browserstore.NewSQLiteStore(browserPath)
	if err != nil {
		t.Fatal(err)
	}
	traceID := core.NewTraceID()
	fixture.traceID = traceID
	if err := browser.SaveTraceRun(context.Background(), browserdomain.TraceRun{TaskID: taskID, RunID: runID, ActorID: "shiro", SiteID: "fixture", Goal: "preserve browser prose", TracePath: "fixture.trace.jsonl", CapturedAt: now, CreatedAt: now}); err != nil {
		browser.Close()
		t.Fatal(err)
	}
	if err := browser.Close(); err != nil {
		t.Fatal(err)
	}
	addSnapshot("browser", "browser.sqlite", mustReadMigrationFixtureFile(t, browserPath))

	knowledgePath := filepath.Join(snapshot, "knowledge.sqlite")
	knowledge, err := knowledgestore.NewSQLiteStore(knowledgePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := knowledge.SaveDreamConsolidationRun(context.Background(), knowledgedomain.DreamConsolidationRun{TaskID: taskID, RunID: runID, ActorID: "shiro", Scope: []string{"preserve knowledge prose"}, IdeaSeeds: []string{"fixture"}, Status: "proposal", ReviewStatus: "pending", CreatedAt: now}); err != nil {
		knowledge.Close()
		t.Fatal(err)
	}
	if err := knowledge.Close(); err != nil {
		t.Fatal(err)
	}
	addSnapshot("knowledge", "knowledge.sqlite", mustReadMigrationFixtureFile(t, knowledgePath))

	eventsPath := filepath.Join(snapshot, "events.sqlite")
	events, err := eventstore.NewSQLiteStore(eventsPath)
	if err != nil {
		t.Fatal(err)
	}
	event := core.NewRootEventEnvelope("fixture", "task.completed", now, map[string]any{"text": "preserve event prose"})
	event.TaskID, event.RunID, event.ActorKind, event.ActorID = taskID, runID, "agent", "shiro"
	fixture.eventID = event.EventID
	if err := events.AppendBatch(context.Background(), []core.EventEnvelope{event}); err != nil {
		events.Close()
		t.Fatal(err)
	}
	if err := events.Close(); err != nil {
		t.Fatal(err)
	}
	addSnapshot("events", "events.sqlite", mustReadMigrationFixtureFile(t, eventsPath))

	word := idlechat.WordPreparedTopic{Category: idlechat.TopicCategorySingle, Topic: "word payload prose", Seed: idlechat.TopicSeed{Category: idlechat.TopicCategorySingle, Genre1: "fixture"}, Axis: "観察", ContentMode: string(idlechat.DialogueContentModeFree), TaskID: taskID, RunID: runID, InitiatedBy: "shiro", Created: now}
	wordPayload, err := json.Marshal(struct {
		Stock map[string][]idlechat.WordPreparedTopic `json:"stock"`
	}{Stock: map[string][]idlechat.WordPreparedTopic{string(idlechat.TopicCategorySingle): {word}}})
	if err != nil {
		t.Fatal(err)
	}
	addSnapshot("word", "word.json", wordPayload)

	forecast := idlechat.PreparedTopic{Domain: idlechat.ForecastDomain{Name: "AI技術"}, Topic: "forecast payload prose", Seeds: []string{"fixture"}, TaskID: taskID, RunID: runID, InitiatedBy: "shiro", Created: now}
	forecastPayload, err := json.Marshal(struct {
		Stock map[string][]idlechat.PreparedTopic `json:"stock"`
	}{Stock: map[string][]idlechat.PreparedTopic{"AI技術": {forecast}}})
	if err != nil {
		t.Fatal(err)
	}
	addSnapshot("forecast", "forecast.json", forecastPayload)

	story := idlechat.StoryEpisodeArtifact{
		SchemaVersion: idlechat.StoryEpisodeSchemaVersion, EpisodeID: "story-fixture", Revision: 1, EpisodeKind: idlechat.StoryEpisodeKind,
		TaskID: taskID, RunID: runID, StoryTitle: "新しい時計", Source: idlechat.StoryEpisodeSource{Title: "昔話", Synopsis: "fixture synopsis"}, Reader: "shiro", Listener: "mio",
		Contract:         idlechat.StoryEpisodeContract{TransformationAxis: "視点反転", Genre: "human_drama", InterestDirection: "moving", InterestContract: []string{"具体化"}, ContentMode: string(idlechat.DialogueContentModeFree)},
		Ledger:           idlechat.StoryEpisodeLedger{Entities: []idlechat.StoryLedgerEntity{{ID: "person", Name: "人物", Reading: "じんぶつ", Role: "主役"}}},
		Turns:            []idlechat.StoryEpisodeTurn{{TurnIndex: 1, Speaker: "shiro", UtteranceRole: idlechat.StoryUtteranceNarration, DisplayText: "story payload prose", SpeechText: "story payload prose"}},
		ProductionStatus: idlechat.StoryProductionReady, Validation: idlechat.StoryValidationResult{Valid: true}, CreatedAt: now, UpdatedAt: completed,
	}
	storyPayload, err := json.Marshal(story)
	if err != nil {
		t.Fatal(err)
	}
	addSnapshot("story", "story.jsonl", append(storyPayload, '\n'))

	dialogue := idlechat.DialogueEpisodeArtifact{
		SchemaVersion: idlechat.DialogueEpisodeSchemaVersion, EpisodeID: "dialogue-fixture", TaskID: taskID, RunID: runID, Revision: 1, SessionID: "fixture-session", InitiatedBy: "shiro",
		TopicResult: idlechat.TopicGenerationResult{Topic: "dialogue payload prose", Category: idlechat.TopicCategorySingle, Strategy: "single", InterestingnessAxis: "観察", OpeningHook: "hook", Provider: "fixture", Seed: idlechat.TopicSeed{Category: idlechat.TopicCategorySingle, Genre1: "fixture"}},
		ArcPlan:     idlechat.DialogueArcPlan{Topic: "dialogue payload prose", Category: idlechat.TopicCategorySingle, Strategy: "single", ContentMode: idlechat.DialogueContentModeFree}, Participants: []string{"Mio", "Shiro"},
		Turns: []idlechat.DialogueEpisodeTurn{{TurnIndex: 1, MessageID: "message-1", Speaker: "Shiro", DisplayText: "dialogue payload prose", SpeechText: "dialogue payload prose"}}, ProductionStatus: idlechat.DialogueProductionReady,
		Validation: idlechat.DialogueEpisodeValidation{Valid: true}, CreatedAt: now, UpdatedAt: completed,
	}
	dialoguePayload, err := json.Marshal(dialogue)
	if err != nil {
		t.Fatal(err)
	}
	addSnapshot("dialogue", "dialogue.jsonl", append(dialoguePayload, '\n'))

	checkpoint := idlechat.GenerationCheckpoint{Key: "checkpoint-1", Kind: "checkpoint payload prose", TaskID: taskID, RunID: runID, Stage: "artifact", UpdatedAt: completed}
	checkpointPayload, err := json.Marshal(struct {
		Version     int                                      `json:"version"`
		Checkpoints map[string]idlechat.GenerationCheckpoint `json:"checkpoints"`
	}{Version: 1, Checkpoints: map[string]idlechat.GenerationCheckpoint{"checkpoint-1": checkpoint}})
	if err != nil {
		t.Fatal(err)
	}
	addSnapshot("checkpoints", "checkpoints.json", checkpointPayload)

	fixture.options = Options{Snapshot: snapshot, Target: filepath.Join(root, "cohort"), Mode: "dry-run", Inventory: Inventory{SchemaVersion: Schema, SnapshotAt: completed.Add(time.Hour), Files: files, Roles: rolesMap}}
	return fixture
}

func addLegacySubagentFixture(t *testing.T, path string, parentRunID core.RunID, createdAt, completedAt time.Time) {
	t.Helper()
	db := openMigrationFixtureDB(t, path)
	if _, err := db.Exec(`ALTER TABLE subagent_task RENAME COLUMN task_id TO subagent_id`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE subagent_task RENAME COLUMN run_id TO parent_run_id`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{
		"subagent_id": "legacy-child-1", "parent_run_id": string(parentRunID), "actor_id": "shiro", "task": "child fixture", "scope": []string{"fixture"},
		"termination_condition": "completed", "status": "completed", "created_at": createdAt, "completed_at": completedAt,
	})
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO subagent_task (subagent_id, parent_run_id, created_at, payload) VALUES (?, ?, ?, ?)`, "legacy-child-1", string(parentRunID), createdAt.Format(time.RFC3339Nano), string(payload)); err != nil {
		db.Close()
		t.Fatal(err)
	}
	closeMigrationFixtureDB(t, db)
}

func openMigrationFixtureDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func closeMigrationFixtureDB(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func mustReadMigrationFixtureFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func refreshFixtureHash(t *testing.T, options *Options, path string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(options.Snapshot, filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	options.Inventory.Files[path] = digest(data)
}

func mutateFixtureEvent(t *testing.T, options *Options, eventID core.EventID, taskID core.TaskID, keepRun bool) {
	t.Helper()
	path := filepath.Join(options.Snapshot, "events.sqlite")
	db := openMigrationFixtureDB(t, path)
	var raw string
	if err := db.QueryRow(`SELECT envelope_json FROM event_envelope WHERE event_id = ?`, string(eventID)).Scan(&raw); err != nil {
		db.Close()
		t.Fatal(err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		db.Close()
		t.Fatal(err)
	}
	taskJSON, err := json.Marshal(string(taskID))
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	envelope["task_id"] = taskJSON
	if !keepRun {
		delete(envelope, "run_id")
	}
	mutated, err := json.Marshal(envelope)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	// The owner store deliberately makes event rows append-only. Drop only its
	// source-fixture guard so this test can represent a pre-existing malformed
	// snapshot without changing production behavior.
	if _, err := db.Exec(`DROP TRIGGER event_envelope_append_only_update`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE event_envelope SET envelope_json = ? WHERE event_id = ?`, string(mutated), string(eventID)); err != nil {
		db.Close()
		t.Fatal(err)
	}
	closeMigrationFixtureDB(t, db)
	refreshFixtureHash(t, options, "events.sqlite")
}
