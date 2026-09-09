package runmigration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	idlechat "github.com/Nyukimin/RenCrow_CORE/internal/application/idlechat"
	taskdomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	core "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestMigrationStoryActorDryRunApplyNoopUsesDeclaredShiro(t *testing.T) {
	f := legacyStoryFixture(t)
	f.options.Inventory.StoryActors = map[string]string{"legacy-story-actor": "shiro"}

	ctx := context.Background()
	first, err := Run(ctx, f.options)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "ready" || first.Counts["story_actor_declarations"] != 1 {
		t.Fatalf("dry-run receipt = %#v", first)
	}
	if _, err := os.Stat(f.options.Target); !os.IsNotExist(err) {
		t.Fatalf("dry-run published target: %v", err)
	}

	apply := f.options
	apply.Mode, apply.Expected = "apply", &first
	applied, err := Run(ctx, apply)
	if err != nil || applied.Status != "applied" {
		t.Fatalf("apply receipt = %#v err=%v", applied, err)
	}
	noop, err := Run(ctx, apply)
	if err != nil || noop.Status != "noop" {
		t.Fatalf("noop receipt = %#v err=%v", noop, err)
	}

	wantRun, err := core.NewMigrationID(core.CanonicalRunID, "idlechat", "generation_id", "legacy-story-actor")
	if err != nil {
		t.Fatal(err)
	}
	runs, err := migratedRuns(t, f.options.Target)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, run := range runs {
		if string(run.RunID) != wantRun {
			continue
		}
		found = true
		if run.Assignee != "Shiro" {
			t.Fatalf("Story owner = %q, want Shiro", run.Assignee)
		}
	}
	if !found {
		t.Fatalf("missing Story owner Run %q", wantRun)
	}

	storyLines, err := os.ReadFile(filepath.Join(f.options.Target, "story.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range jsonLines(storyLines) {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(line, &fields); err != nil {
			t.Fatal(err)
		}
		if _, ok := fields["initiated_by"]; ok {
			t.Fatal("Story output injected initiated_by")
		}
		var story idlechat.StoryEpisodeArtifact
		if err := strictJSON(line, &story); err != nil {
			t.Fatalf("Story output schema: %v", err)
		}
		if story.RunID != core.RunID(wantRun) || story.TaskID == "" {
			t.Fatalf("Story identity = task=%q run=%q", story.TaskID, story.RunID)
		}
	}

	dialogue, err := os.ReadFile(filepath.Join(f.options.Target, "dialogue.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var gotDialogue idlechat.DialogueEpisodeArtifact
	if err := strictJSON(jsonLines(dialogue)[0], &gotDialogue); err != nil {
		t.Fatal(err)
	}
	if gotDialogue.TaskID != f.taskID || gotDialogue.RunID != f.runID || gotDialogue.InitiatedBy != "shiro" {
		t.Fatalf("Dialogue identity changed: task=%q run=%q actor=%q", gotDialogue.TaskID, gotDialogue.RunID, gotDialogue.InitiatedBy)
	}
}

func TestMigrationStoryActorRejectsMissingAttribution(t *testing.T) {
	f := legacyStoryFixture(t)
	if _, err := Run(context.Background(), f.options); err == nil {
		t.Fatal("legacy Story without declared actor was accepted")
	}
}

func TestMigrationStoryActorRejectsInvalidAndUnusedDeclarations(t *testing.T) {
	for name, actors := range map[string]map[string]string{
		"invalid actor": {"legacy-story-actor": "codex"},
		"unused key":    {"legacy-story-actor": "shiro", "unused-story": "shiro"},
	} {
		t.Run(name, func(t *testing.T) {
			f := legacyStoryFixture(t)
			f.options.Inventory.StoryActors = actors
			if _, err := Run(context.Background(), f.options); err == nil {
				t.Fatal("invalid Story actor declarations were accepted")
			}
		})
	}
}

func TestMigrationStoryActorCannotReassignCanonicalStory(t *testing.T) {
	f := newFullCohortFixture(t)
	f.options.Inventory.StoryActors = map[string]string{"story-fixture": "mio"}
	if _, err := Run(context.Background(), f.options); err == nil {
		t.Fatal("canonical Story was reassigned by a declaration")
	}
}

func legacyStoryFixture(t *testing.T) fullCohortFixture {
	t.Helper()
	f := newFullCohortFixture(t)
	path := filepath.Join(f.options.Snapshot, "story.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var first map[string]json.RawMessage
	if err := json.Unmarshal(jsonLines(raw)[0], &first); err != nil {
		t.Fatal(err)
	}
	delete(first, "task_id")
	delete(first, "run_id")
	setField(first, "generation_id", "legacy-story-actor")
	firstLine := marshalLine(first)
	second := map[string]json.RawMessage{}
	for key, value := range first {
		second[key] = value
	}
	setField(second, "revision", 2)
	setField(second, "updated_at", f.options.Inventory.SnapshotAt)
	if err := os.WriteFile(path, append(firstLine, marshalLine(second)...), 0600); err != nil {
		t.Fatal(err)
	}
	refreshFixtureHash(t, &f.options, "story.jsonl")
	return f
}

func migratedRuns(t *testing.T, target string) ([]taskdomain.Run, error) {
	t.Helper()
	reader, err := taskstore.NewJSONLReader(filepath.Join(target, "tasks"))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return reader.ListRuns(context.Background(), taskdomain.RunFilter{})
}
