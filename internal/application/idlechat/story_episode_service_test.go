package idlechat

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

type queuedStoryCodexGenerator struct {
	responses []string
	prompts   []string
}

func (f *queuedStoryCodexGenerator) Generate(_ context.Context, prompt string) (string, error) {
	f.prompts = append(f.prompts, prompt)
	if len(f.responses) == 0 {
		return "", context.Canceled
	}
	response := f.responses[0]
	f.responses = f.responses[1:]
	return response, nil
}

func TestStoryEpisodeServiceKeepsInvalidAndGeneratesReplacement(t *testing.T) {
	invalid := validStoryEpisodeFixture()
	invalid.EpisodeID = ""
	invalid.GenerationID = ""
	valid := validStoryEpisodeFixture()
	valid.EpisodeID = ""
	valid.GenerationID = ""
	valid.Reader, valid.Listener = "shiro", "mio"
	for i := range valid.Turns {
		if valid.Turns[i].UtteranceRole == StoryUtteranceNarration {
			valid.Turns[i].Speaker = "shiro"
		} else {
			valid.Turns[i].Speaker = "mio"
		}
	}
	invalidJSON, err := json.Marshal(invalid)
	if err != nil {
		t.Fatal(err)
	}
	validJSON, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	badReview, _ := json.Marshal(StorySemanticReview{Valid: false, Errors: []StoryValidationError{{
		Code: "entity_relation_violation", TurnIndex: 5, Evidence: "人物関係が前段と矛盾する",
	}}})
	goodReview, _ := json.Marshal(StorySemanticReview{Valid: true})
	generator := &queuedStoryCodexGenerator{responses: []string{
		string(invalidJSON), string(badReview), string(validJSON), string(goodReview),
	}}
	store := newStoryEpisodeStore(filepath.Join(t.TempDir(), "story_episodes.jsonl"), 1)
	service := NewStoryEpisodeService(store, generator, map[string]string{
		"mio": "Mio character context", "shiro": "Shiro character context",
	})

	if err := service.PrepareToTarget(context.Background()); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	snapshot := service.Snapshot()
	if snapshot.NeedsRepair != 1 || snapshot.Ready != 1 || snapshot.Missing != 0 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if len(snapshot.Episodes) != 2 {
		t.Fatalf("episodes=%d, want invalid plus replacement", len(snapshot.Episodes))
	}
	broken := snapshot.Episodes[0]
	replacement := snapshot.Episodes[1]
	if broken.ProductionStatus != StoryProductionNeedsRepair || broken.Validation.FirstInvalidTurn != 5 {
		t.Fatalf("broken=%+v", broken)
	}
	if replacement.ProductionStatus != StoryProductionReady || replacement.ReplacementForEpisodeID != broken.EpisodeID {
		t.Fatalf("replacement=%+v broken_id=%s", replacement, broken.EpisodeID)
	}
	if len(generator.prompts) != 4 || !strings.Contains(generator.prompts[0], "Mio character context") || !strings.Contains(generator.prompts[0], "Shiro character context") {
		t.Fatalf("prompts do not contain both character contexts: %#v", generator.prompts)
	}
	if !strings.Contains(generator.prompts[1], "意味検査") {
		t.Fatalf("second call must be semantic review: %q", generator.prompts[1])
	}
}

func TestStoryEpisodeServiceRejectsUncertainReview(t *testing.T) {
	artifact := validStoryEpisodeFixture()
	artifact.EpisodeID = ""
	artifact.GenerationID = ""
	artifactJSON, _ := json.Marshal(artifact)
	uncertainReview, _ := json.Marshal(StorySemanticReview{Valid: false})
	generator := &queuedStoryCodexGenerator{responses: []string{string(artifactJSON), string(uncertainReview)}}
	store := newStoryEpisodeStore(filepath.Join(t.TempDir(), "story_episodes.jsonl"), 1)
	service := NewStoryEpisodeService(store, generator, nil)
	service.maxAttempts = 1

	err := service.PrepareToTarget(context.Background())
	if err == nil {
		t.Fatal("prepare must report ready-stock shortage")
	}
	snapshot := service.Snapshot()
	if snapshot.Ready != 0 || snapshot.NeedsRepair != 1 || snapshot.Missing != 1 || snapshot.Filling {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestStoryEpisodeServiceRepairsOnlySuffixAndKeepsPrefixIDs(t *testing.T) {
	artifact := validStoryEpisodeFixture()
	artifact.ProductionStatus = StoryProductionNeedsRepair
	artifact.Validation = StoryValidationResult{Valid: false, FirstInvalidTurn: 5, Errors: []StoryValidationError{{Code: "continuity_violation", TurnIndex: 5}}}
	for i := range artifact.Turns {
		artifact.Turns[i].MessageID = "original-" + string(rune('a'+i))
	}
	suffixJSON, err := json.Marshal(struct {
		Turns []StoryEpisodeTurn `json:"turns"`
	}{Turns: artifact.Turns[4:]})
	if err != nil {
		t.Fatal(err)
	}
	goodReview, _ := json.Marshal(StorySemanticReview{Valid: true})
	generator := &queuedStoryCodexGenerator{responses: []string{string(suffixJSON), string(goodReview)}}
	store := newStoryEpisodeStore(filepath.Join(t.TempDir(), "story_episodes.jsonl"), 1)
	if err := store.append(artifact); err != nil {
		t.Fatal(err)
	}
	service := NewStoryEpisodeService(store, generator, nil)

	if err := service.RepairNeedsRepair(context.Background()); err != nil {
		t.Fatalf("repair: %v", err)
	}
	repaired, ok := service.Episode(artifact.EpisodeID)
	if !ok || repaired.ProductionStatus != StoryProductionReady || repaired.Revision != artifact.Revision+1 {
		t.Fatalf("repaired=%+v ok=%t", repaired, ok)
	}
	if repaired.FixedPrefixLength != 4 || repaired.RepairFromTurn != 5 || repaired.SuffixRegenerations != 1 {
		t.Fatalf("repair metadata=%+v", repaired)
	}
	for i := 0; i < 4; i++ {
		if repaired.Turns[i].MessageID != artifact.Turns[i].MessageID {
			t.Fatalf("prefix message %d changed: %q", i+1, repaired.Turns[i].MessageID)
		}
	}
	for i := 4; i < len(repaired.Turns); i++ {
		if repaired.Turns[i].MessageID == artifact.Turns[i].MessageID || repaired.Turns[i].MessageID == "" {
			t.Fatalf("suffix message %d was not replaced: %q", i+1, repaired.Turns[i].MessageID)
		}
	}
}
