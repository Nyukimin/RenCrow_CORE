package idlechat

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStoryEpisodeStoreKeepsNeedsRepairAndCountsOnlyReady(t *testing.T) {
	path := filepath.Join(t.TempDir(), "story_episodes.jsonl")
	store := newStoryEpisodeStore(path, 2)
	broken := validStoryEpisodeFixture()
	broken.EpisodeID = "broken"
	broken.ProductionStatus = StoryProductionNeedsRepair
	broken.Validation = StoryValidationResult{Valid: false, Errors: []StoryValidationError{{Code: "continuity_violation", TurnIndex: 4}}}
	ready := validStoryEpisodeFixture()
	ready.EpisodeID = "ready"
	ready.ProductionStatus = StoryProductionReady
	ready.Validation = StoryValidationResult{Valid: true}
	if err := store.append(broken); err != nil {
		t.Fatalf("append broken: %v", err)
	}
	if err := store.append(ready); err != nil {
		t.Fatalf("append ready: %v", err)
	}

	snapshot := store.snapshot()
	if snapshot.Ready != 1 || snapshot.NeedsRepair != 1 || snapshot.Missing != 1 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if len(snapshot.Episodes) != 2 {
		t.Fatalf("episodes=%d, want 2", len(snapshot.Episodes))
	}

	reloaded := newStoryEpisodeStore(path, 2)
	reloadedSnapshot := reloaded.snapshot()
	if reloadedSnapshot.Ready != 1 || reloadedSnapshot.NeedsRepair != 1 || len(reloadedSnapshot.Episodes) != 2 {
		t.Fatalf("reloaded snapshot=%+v", reloadedSnapshot)
	}
}

func TestStoryEpisodeStoreSelectsLeastPlayedReadyWithoutDeleting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "story_episodes.jsonl")
	store := newStoryEpisodeStore(path, 2)
	first := validStoryEpisodeFixture()
	first.EpisodeID = "first"
	first.ProductionStatus = StoryProductionReady
	first.Validation = StoryValidationResult{Valid: true}
	second := first
	second.EpisodeID = "second"
	if err := store.append(first); err != nil {
		t.Fatal(err)
	}
	if err := store.append(second); err != nil {
		t.Fatal(err)
	}

	got, ok := store.nextReady()
	if !ok || got.EpisodeID != "first" {
		t.Fatalf("next ready=%+v ok=%t", got, ok)
	}
	playedAt := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	if err := store.markPlayed(got.EpisodeID, playedAt); err != nil {
		t.Fatalf("mark played: %v", err)
	}
	next, ok := store.nextReady()
	if !ok || next.EpisodeID != "second" {
		t.Fatalf("next after play=%+v ok=%t", next, ok)
	}
	if snapshot := store.snapshot(); snapshot.Ready != 2 || len(snapshot.Episodes) != 2 {
		t.Fatalf("played episode must remain ready: %+v", snapshot)
	}
}

func TestStoryEpisodeStoreCountsLegacyReadyWithoutStoryTitle(t *testing.T) {
	store := newStoryEpisodeStore(filepath.Join(t.TempDir(), "story_episodes.jsonl"), 1)
	legacy := validStoryEpisodeFixture()
	legacy.StoryTitle = ""
	legacy.ProductionStatus = StoryProductionReady
	legacy.Validation = StoryValidationResult{Valid: true}
	if err := store.append(legacy); err != nil {
		t.Fatal(err)
	}

	snapshot := store.snapshot()
	if snapshot.Ready != 1 || snapshot.UntitledReady != 1 {
		t.Fatalf("snapshot=%+v, legacy ready must remain playable and request title backfill", snapshot)
	}
}
