package idlechat

import (
	"path/filepath"
	"testing"
)

func TestDialogueCheckpointOwnsArtifactAcrossMutationAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoints.json")
	store := NewGenerationCheckpointStore(path)
	taskID, runID := testIdleChatRunIdentityPair()
	artifact := DialogueEpisodeArtifact{
		TaskID: taskID, RunID: runID, EpisodeID: "dialogue-checkpoint", Revision: 1,
		Turns:   []DialogueEpisodeTurn{{DisplayText: "saved"}},
		ArcPlan: DialogueArcPlan{SpeakerRoles: map[string]DialogueSpeakerRole{"mio": {Avoid: []string{"saved"}}}},
	}
	cp := GenerationCheckpoint{Key: "dialogue:test", Kind: "dialogue", TaskID: taskID, RunID: runID, DialogueArtifact: &artifact}
	if err := store.Put(cp); err != nil {
		t.Fatal(err)
	}
	artifact.Turns[0].DisplayText = "mutated"
	artifact.ArcPlan.SpeakerRoles["mio"].Avoid[0] = "mutated"
	for _, current := range []*GenerationCheckpointStore{store, NewGenerationCheckpointStore(path)} {
		got, ok := current.Get(cp.Key)
		if !ok || got.DialogueArtifact == nil {
			t.Fatal("missing durable dialogue artifact")
		}
		if got.DialogueArtifact.Turns[0].DisplayText != "saved" || got.DialogueArtifact.ArcPlan.SpeakerRoles["mio"].Avoid[0] != "saved" {
			t.Fatal("checkpoint retained mutable generation state")
		}
		got.DialogueArtifact.Turns[0].DisplayText = "reader mutation"
		again, _ := current.Get(cp.Key)
		if again.DialogueArtifact.Turns[0].DisplayText != "saved" {
			t.Fatal("checkpoint reader can mutate owner state")
		}
	}
}
