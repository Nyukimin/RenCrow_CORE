package idlechat

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestListReadyRejectsLegacyArtifactMissingPreparedMetadata(t *testing.T) {
	owner := newTestIdleChatRunIssuer(t)
	source, artifacts := newReadyPlaybackDialogueService(t, owner, TopicCategorySingle)
	if len(artifacts) != 1 {
		t.Fatalf("prepared artifacts=%d, want 1", len(artifacts))
	}

	tests := []struct {
		name  string
		clear func(*DialogueEpisodeArtifact)
	}{
		{name: "title", clear: func(artifact *DialogueEpisodeArtifact) { artifact.PreparedTitle = "" }},
		{name: "summary", clear: func(artifact *DialogueEpisodeArtifact) { artifact.PreparedSummary = "" }},
		{name: "quality review", clear: func(artifact *DialogueEpisodeArtifact) { artifact.PreparedQualityReview = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			artifact := cloneDialogueEpisode(artifacts[0])
			artifact.SchemaVersion = DialogueEpisodeLegacySchemaVersion
			test.clear(&artifact)
			payload, err := json.Marshal(artifact)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "dialogue_episodes.jsonl")
			if err := os.WriteFile(path, append(payload, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}

			service := NewPersistentDialogueEpisodeService(path, nil, nil, source.config)
			service.SetRunIssuer(owner)
			ready, err := service.ListReady(context.Background())
			if err != nil {
				t.Fatalf("ListReady: %v", err)
			}
			if len(ready) != 0 {
				t.Fatalf("ListReady returned %d artifact(s) with missing persisted %s", len(ready), test.name)
			}
		})
	}
}

func TestListReadyRetriesAfterTransientReadFailureAndConcurrentReload(t *testing.T) {
	owner := newTestIdleChatRunIssuer(t)
	source, artifacts := newReadyPlaybackDialogueService(t, owner, TopicCategorySingle)
	if len(artifacts) != 1 {
		t.Fatalf("prepared artifacts=%d, want 1", len(artifacts))
	}
	payload, err := os.ReadFile(source.path)
	if err != nil {
		t.Fatalf("read source dialogue store: %v", err)
	}

	path := filepath.Join(t.TempDir(), "dialogue_episodes.jsonl")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	service := NewPersistentDialogueEpisodeService(path, nil, nil, source.config)
	service.SetRunIssuer(owner)
	if _, err := service.ListReady(context.Background()); !errors.Is(err, ErrPreparedDialogueInvalid) {
		t.Fatalf("ListReady before store creation error=%v, want %v", err, ErrPreparedDialogueInvalid)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	const callers = 24
	start := make(chan struct{})
	results := make(chan struct {
		items []DialogueEpisodeArtifact
		err   error
	}, callers)
	var workers sync.WaitGroup
	workers.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer workers.Done()
			<-start
			items, err := service.ListReady(context.Background())
			results <- struct {
				items []DialogueEpisodeArtifact
				err   error
			}{items: items, err: err}
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	for result := range results {
		if result.err != nil {
			t.Errorf("concurrent ListReady: %v", result.err)
			continue
		}
		if len(result.items) != 1 || result.items[0].EpisodeID != artifacts[0].EpisodeID {
			t.Errorf("concurrent ready projection=%+v, want episode %q", result.items, artifacts[0].EpisodeID)
		}
	}
}
