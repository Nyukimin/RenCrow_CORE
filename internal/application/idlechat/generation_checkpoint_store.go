package idlechat

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const generationCheckpointSchemaVersion = 1

// GenerationCheckpoint records only completed generation stages. It lets a
// background CodexExe job yield to foreground conversation and continue from
// the last durable boundary without registering the same result twice.
type GenerationCheckpoint struct {
	Key             string                 `json:"key"`
	Kind            string                 `json:"kind"`
	GenerationID    string                 `json:"generation_id"`
	Stage           string                 `json:"stage"`
	Attempt         int                    `json:"attempt,omitempty"`
	Category        TopicCategory          `json:"category,omitempty"`
	Domain          ForecastDomain         `json:"domain,omitempty"`
	Seed            TopicSeed              `json:"seed,omitempty"`
	Recent          []RecentTopic          `json:"recent,omitempty"`
	Candidates      []TopicCandidate       `json:"candidates,omitempty"`
	Result          *TopicGenerationResult `json:"result,omitempty"`
	ForecastSeeds   []string               `json:"forecast_seeds,omitempty"`
	ForecastKeyword string                 `json:"forecast_keyword,omitempty"`
	StorySeed       *storyGenerationSeed   `json:"story_seed,omitempty"`
	StoryArtifact   *StoryEpisodeArtifact  `json:"story_artifact,omitempty"`
	StoryReview     *StorySemanticReview   `json:"story_review,omitempty"`
	UpdatedAt       time.Time              `json:"updated_at"`
}

type generationCheckpointFile struct {
	Version     int                             `json:"version"`
	Checkpoints map[string]GenerationCheckpoint `json:"checkpoints"`
}

type GenerationCheckpointStore struct {
	mu          sync.Mutex
	path        string
	checkpoints map[string]GenerationCheckpoint
	loadErr     error
}

func NewGenerationCheckpointStore(path string) *GenerationCheckpointStore {
	store := &GenerationCheckpointStore{
		path:        strings.TrimSpace(path),
		checkpoints: make(map[string]GenerationCheckpoint),
	}
	store.loadErr = store.load()
	return store
}

func (s *GenerationCheckpointStore) LoadError() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadErr
}

func (s *GenerationCheckpointStore) Get(key string) (GenerationCheckpoint, bool) {
	if s == nil {
		return GenerationCheckpoint{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	checkpoint, ok := s.checkpoints[strings.TrimSpace(key)]
	return cloneGenerationCheckpoint(checkpoint), ok
}

func (s *GenerationCheckpointStore) Put(checkpoint GenerationCheckpoint) error {
	if s == nil {
		return nil
	}
	checkpoint.Key = strings.TrimSpace(checkpoint.Key)
	if checkpoint.Key == "" {
		return errors.New("generation checkpoint key is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return fmt.Errorf("generation checkpoint store unavailable: %w", s.loadErr)
	}
	checkpoint.UpdatedAt = time.Now().UTC()
	previous, existed := s.checkpoints[checkpoint.Key]
	s.checkpoints[checkpoint.Key] = cloneGenerationCheckpoint(checkpoint)
	if err := s.saveLocked(); err != nil {
		if existed {
			s.checkpoints[checkpoint.Key] = previous
		} else {
			delete(s.checkpoints, checkpoint.Key)
		}
		return err
	}
	return nil
}

func (s *GenerationCheckpointStore) Delete(key string) error {
	if s == nil {
		return nil
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return fmt.Errorf("generation checkpoint store unavailable: %w", s.loadErr)
	}
	previous, exists := s.checkpoints[key]
	if !exists {
		return nil
	}
	delete(s.checkpoints, key)
	if err := s.saveLocked(); err != nil {
		s.checkpoints[key] = previous
		return err
	}
	return nil
}

func (s *GenerationCheckpointStore) load() error {
	if s == nil || s.path == "" {
		return nil
	}
	payload, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read generation checkpoints: %w", err)
	}
	var file generationCheckpointFile
	if err := json.Unmarshal(payload, &file); err != nil {
		return fmt.Errorf("decode generation checkpoints: %w", err)
	}
	if file.Version != 0 && file.Version != generationCheckpointSchemaVersion {
		return fmt.Errorf("unsupported generation checkpoint version %d", file.Version)
	}
	for key, checkpoint := range file.Checkpoints {
		checkpoint.Key = strings.TrimSpace(checkpoint.Key)
		if checkpoint.Key == "" {
			checkpoint.Key = strings.TrimSpace(key)
		}
		if checkpoint.Key != "" {
			s.checkpoints[checkpoint.Key] = cloneGenerationCheckpoint(checkpoint)
		}
	}
	return nil
}

func (s *GenerationCheckpointStore) saveLocked() error {
	if s.path == "" {
		return nil
	}
	payload, err := json.Marshal(generationCheckpointFile{
		Version:     generationCheckpointSchemaVersion,
		Checkpoints: s.checkpoints,
	})
	if err != nil {
		return fmt.Errorf("encode generation checkpoints: %w", err)
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create generation checkpoint directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".idlechat-generation-checkpoints-*")
	if err != nil {
		return fmt.Errorf("create generation checkpoint temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(payload)
	}
	if syncErr := tmp.Sync(); err == nil {
		err = syncErr
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmpPath, s.path)
	}
	if err != nil {
		return fmt.Errorf("write generation checkpoints: %w", err)
	}
	return nil
}

func cloneGenerationCheckpoint(checkpoint GenerationCheckpoint) GenerationCheckpoint {
	checkpoint.Recent = append([]RecentTopic(nil), checkpoint.Recent...)
	checkpoint.Candidates = append([]TopicCandidate(nil), checkpoint.Candidates...)
	checkpoint.ForecastSeeds = append([]string(nil), checkpoint.ForecastSeeds...)
	if checkpoint.Result != nil {
		result := *checkpoint.Result
		result.Candidates = append([]TopicCandidate(nil), checkpoint.Result.Candidates...)
		checkpoint.Result = &result
	}
	if checkpoint.StorySeed != nil {
		seed := *checkpoint.StorySeed
		seed.Contract.InterestContract = append([]string(nil), checkpoint.StorySeed.Contract.InterestContract...)
		checkpoint.StorySeed = &seed
	}
	if checkpoint.StoryArtifact != nil {
		artifact := cloneStoryEpisode(*checkpoint.StoryArtifact)
		checkpoint.StoryArtifact = &artifact
	}
	if checkpoint.StoryReview != nil {
		review := *checkpoint.StoryReview
		review.Errors = append([]StoryValidationError(nil), checkpoint.StoryReview.Errors...)
		checkpoint.StoryReview = &review
	}
	return checkpoint
}
