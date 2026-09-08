package idlechat

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type storyEpisodeStore struct {
	mu                 sync.RWMutex
	path               string
	target             int
	episodes           map[string]StoryEpisodeArtifact
	order              []string
	loadErr            error
	filling            bool
	generationAttempts int
	lastFailurePhase   string
	lastError          string
}

func (s *storyEpisodeStore) loadError() error {
	if s == nil {
		return errors.New("story episode store is nil")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loadErr
}

func newStoryEpisodeStore(path string, target int) *storyEpisodeStore {
	if target < 1 {
		target = 1
	}
	store := &storyEpisodeStore{
		path:     strings.TrimSpace(path),
		target:   target,
		episodes: make(map[string]StoryEpisodeArtifact),
	}
	if err := store.load(); err != nil {
		store.loadErr = err
		store.lastFailurePhase = "storage_load"
		store.lastError = err.Error()
	}
	return store
}

func (s *storyEpisodeStore) append(artifact StoryEpisodeArtifact) error {
	if s == nil {
		return errors.New("story episode store is nil")
	}
	if strings.TrimSpace(s.path) == "" {
		return errors.New("story episode store path is empty")
	}
	artifact.EpisodeID = strings.TrimSpace(artifact.EpisodeID)
	if artifact.EpisodeID == "" {
		return errors.New("story episode id is empty")
	}
	if err := validateIdleChatRunIdentity(artifact.TaskID, artifact.RunID); err != nil {
		return fmt.Errorf("story episode identity: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return fmt.Errorf("story episode store unavailable: %w", s.loadErr)
	}
	if existing, ok := s.episodes[artifact.EpisodeID]; ok {
		if artifact.Revision > existing.Revision {
			// Revision generation owns content, while markPlayed owns playback.
			// A checkpoint may predate a playback update: retain the current
			// counters under this same storage lock when publishing new content.
			artifact.PlayCount = existing.PlayCount
			artifact.LastPlayedAt = existing.LastPlayedAt
		}
		if existing.TaskID != artifact.TaskID || existing.RunID != artifact.RunID {
			if artifact.Revision <= existing.Revision {
				return fmt.Errorf("story episode %s identity conflicts with revision %d", artifact.EpisodeID, existing.Revision)
			}
		} else {
			if artifact.Revision < existing.Revision {
				return fmt.Errorf("story episode %s revision regressed from %d to %d", artifact.EpisodeID, existing.Revision, artifact.Revision)
			}
			if artifact.CreatedAt.IsZero() {
				artifact.CreatedAt = existing.CreatedAt
			}
			if storyArtifactSameRevision(existing, artifact) {
				return nil
			}
			if existing.Revision == artifact.Revision && !storyArtifactSameRevisionIgnoringPlayback(existing, artifact) {
				return fmt.Errorf("story episode %s has conflicting revision %d", artifact.EpisodeID, artifact.Revision)
			}
		}
	}
	now := time.Now().UTC()
	if artifact.CreatedAt.IsZero() {
		artifact.CreatedAt = now
	}
	artifact.UpdatedAt = now
	payload, err := json.Marshal(artifact)
	if err != nil {
		return fmt.Errorf("encode story episode: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create story episode storage directory: %w", err)
	}
	file, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open story episode storage: %w", err)
	}
	payload = append(payload, '\n')
	if _, err = file.Write(payload); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return fmt.Errorf("write story episode storage: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("close story episode storage: %w", closeErr)
	}
	s.putLocked(artifact)
	return nil
}

func (s *storyEpisodeStore) snapshot() StoryEpisodeStockSnapshot {
	return s.snapshotExcluding("", "")
}

func (s *storyEpisodeStore) snapshotExcluding(excludeRunID modulecore.RunID, excludeEpisodeID string) StoryEpisodeStockSnapshot {
	if s == nil {
		return StoryEpisodeStockSnapshot{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	snapshot := StoryEpisodeStockSnapshot{
		Enabled:            true,
		Target:             s.target,
		Filling:            s.filling,
		GenerationAttempts: s.generationAttempts,
		LastFailurePhase:   s.lastFailurePhase,
		LastError:          s.lastError,
	}
	if s.loadErr != nil {
		snapshot.Enabled = false
		snapshot.Ready = 0
		snapshot.Missing = snapshot.Target
		snapshot.NeedsRepair = 0
		snapshot.Failed = 0
		snapshot.UntitledReady = 0
		snapshot.Episodes = nil
		snapshot.LastFailurePhase = "storage_load"
		snapshot.LastError = s.loadErr.Error()
		return snapshot
	}
	for _, id := range s.order {
		artifact, ok := s.episodes[id]
		if !ok {
			continue
		}
		if (excludeRunID != "" && artifact.RunID == excludeRunID) || (excludeEpisodeID != "" && artifact.EpisodeID == excludeEpisodeID) {
			continue
		}
		snapshot.Episodes = append(snapshot.Episodes, cloneStoryEpisode(artifact))
		switch artifact.ProductionStatus {
		case StoryProductionReady:
			if artifact.Validation.Valid {
				snapshot.Ready++
				if strings.TrimSpace(artifact.StoryTitle) == "" {
					snapshot.UntitledReady++
				}
			}
		case StoryProductionNeedsRepair:
			snapshot.NeedsRepair++
		case StoryProductionFailed:
			snapshot.Failed++
		}
	}
	if snapshot.Ready < snapshot.Target {
		snapshot.Missing = snapshot.Target - snapshot.Ready
	}
	return snapshot
}

func (s *storyEpisodeStore) nextReady() (StoryEpisodeArtifact, bool) {
	return s.nextReadyExcluding("", "")
}

func (s *storyEpisodeStore) nextReadyExcluding(excludeRunID modulecore.RunID, excludeEpisodeID string) (StoryEpisodeArtifact, bool) {
	if s == nil {
		return StoryEpisodeArtifact{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.loadErr != nil {
		return StoryEpisodeArtifact{}, false
	}
	type candidate struct {
		artifact StoryEpisodeArtifact
		order    int
	}
	var candidates []candidate
	for order, id := range s.order {
		artifact, ok := s.episodes[id]
		if !ok || (excludeRunID != "" && artifact.RunID == excludeRunID) || (excludeEpisodeID != "" && artifact.EpisodeID == excludeEpisodeID) || artifact.ProductionStatus != StoryProductionReady || !artifact.Validation.Valid {
			continue
		}
		candidates = append(candidates, candidate{artifact: artifact, order: order})
	}
	if len(candidates) == 0 {
		return StoryEpisodeArtifact{}, false
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if left.artifact.PlayCount != right.artifact.PlayCount {
			return left.artifact.PlayCount < right.artifact.PlayCount
		}
		if left.artifact.LastPlayedAt == nil && right.artifact.LastPlayedAt != nil {
			return true
		}
		if left.artifact.LastPlayedAt != nil && right.artifact.LastPlayedAt == nil {
			return false
		}
		if left.artifact.LastPlayedAt != nil && right.artifact.LastPlayedAt != nil && !left.artifact.LastPlayedAt.Equal(*right.artifact.LastPlayedAt) {
			return left.artifact.LastPlayedAt.Before(*right.artifact.LastPlayedAt)
		}
		if !left.artifact.CreatedAt.Equal(right.artifact.CreatedAt) {
			return left.artifact.CreatedAt.Before(right.artifact.CreatedAt)
		}
		return left.order < right.order
	})
	return cloneStoryEpisode(candidates[0].artifact), true
}

func (s *storyEpisodeStore) get(episodeID string) (StoryEpisodeArtifact, bool) {
	if s == nil {
		return StoryEpisodeArtifact{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.loadErr != nil {
		return StoryEpisodeArtifact{}, false
	}
	artifact, ok := s.episodes[strings.TrimSpace(episodeID)]
	return cloneStoryEpisode(artifact), ok
}

func (s *storyEpisodeStore) hasRunID(runID modulecore.RunID) bool {
	_, ok := s.artifactByRunID(runID)
	return ok
}

func (s *storyEpisodeStore) artifactByRunID(runID modulecore.RunID) (StoryEpisodeArtifact, bool) {
	if s == nil || runID == "" {
		return StoryEpisodeArtifact{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.loadErr != nil {
		return StoryEpisodeArtifact{}, false
	}
	for _, id := range s.order {
		artifact, ok := s.episodes[id]
		if !ok {
			continue
		}
		if artifact.RunID == runID {
			return cloneStoryEpisode(artifact), true
		}
	}
	return StoryEpisodeArtifact{}, false
}

func (s *storyEpisodeStore) markPlayed(episodeID string, playedAt time.Time) error {
	if s == nil {
		return errors.New("story episode store is nil")
	}
	s.mu.RLock()
	if s.loadErr != nil {
		err := s.loadErr
		s.mu.RUnlock()
		return fmt.Errorf("story episode store unavailable: %w", err)
	}
	artifact, ok := s.episodes[strings.TrimSpace(episodeID)]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("story episode %q not found", episodeID)
	}
	playedAt = playedAt.UTC()
	artifact.PlayCount++
	artifact.LastPlayedAt = &playedAt
	return s.append(artifact)
}

func (s *storyEpisodeStore) setFilling(filling bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.filling = filling
	s.mu.Unlock()
}

func (s *storyEpisodeStore) recordGenerationAttempt() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.generationAttempts++
	s.mu.Unlock()
}

func (s *storyEpisodeStore) recordFailure(phase string, err error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.lastFailurePhase = strings.TrimSpace(phase)
	if err == nil {
		s.lastError = ""
	} else {
		s.lastError = err.Error()
	}
	s.mu.Unlock()
}

func (s *storyEpisodeStore) load() error {
	if s.path == "" {
		return errors.New("story episode store path is empty")
	}
	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open story episode storage: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		if len(strings.TrimSpace(scanner.Text())) == 0 {
			continue
		}
		var artifact StoryEpisodeArtifact
		if err := json.Unmarshal(scanner.Bytes(), &artifact); err != nil {
			return fmt.Errorf("decode story episode storage line %d: %w", line, err)
		}
		if strings.TrimSpace(artifact.EpisodeID) == "" {
			return fmt.Errorf("decode story episode storage line %d: episode_id is empty", line)
		}
		if existing, ok := s.episodes[artifact.EpisodeID]; ok && existing.Revision == artifact.Revision && !storyArtifactSameRevisionIgnoringPlayback(existing, artifact) {
			return fmt.Errorf("decode story episode storage line %d: conflicting episode %s revision %d", line, artifact.EpisodeID, artifact.Revision)
		}
		s.putLocked(artifact)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan story episode storage: %w", err)
	}
	return nil
}

func (s *storyEpisodeStore) putLocked(artifact StoryEpisodeArtifact) {
	if _, exists := s.episodes[artifact.EpisodeID]; !exists {
		s.order = append(s.order, artifact.EpisodeID)
	}
	s.episodes[artifact.EpisodeID] = cloneStoryEpisode(artifact)
}

func cloneStoryEpisode(artifact StoryEpisodeArtifact) StoryEpisodeArtifact {
	artifact.Contract.InterestContract = append([]string(nil), artifact.Contract.InterestContract...)
	artifact.Ledger.Entities = append([]StoryLedgerEntity(nil), artifact.Ledger.Entities...)
	artifact.Ledger.Relations = append([]StoryLedgerRelation(nil), artifact.Ledger.Relations...)
	artifact.Ledger.WorldRules = append([]string(nil), artifact.Ledger.WorldRules...)
	artifact.Ledger.CoinedTerms = append([]StoryLedgerTerm(nil), artifact.Ledger.CoinedTerms...)
	artifact.Turns = append([]StoryEpisodeTurn(nil), artifact.Turns...)
	artifact.Validation.Errors = append([]StoryValidationError(nil), artifact.Validation.Errors...)
	if artifact.LastPlayedAt != nil {
		playedAt := *artifact.LastPlayedAt
		artifact.LastPlayedAt = &playedAt
	}
	return artifact
}

// storyArtifactSameRevision compares one persisted revision while ignoring the
// append timestamp. It keeps retries idempotent and lets the loader reject
// conflicting records for the same EpisodeID/revision.
func storyArtifactSameRevision(left, right StoryEpisodeArtifact) bool {
	left.UpdatedAt = time.Time{}
	right.UpdatedAt = time.Time{}
	return reflect.DeepEqual(left, right)
}

func storyArtifactSameRevisionIgnoringPlayback(left, right StoryEpisodeArtifact) bool {
	left.PlayCount = 0
	right.PlayCount = 0
	left.LastPlayedAt = nil
	right.LastPlayedAt = nil
	return storyArtifactSameRevision(left, right)
}

// storyArtifactMatchesCheckpoint compares the generated artifact payload with
// its checkpoint. Validation, publication, playback, and timestamps are
// derived after generation and therefore are intentionally excluded.
func storyArtifactMatchesCheckpoint(stored, checkpoint StoryEpisodeArtifact) bool {
	if stored.EpisodeID != checkpoint.EpisodeID || stored.Revision != checkpoint.Revision || stored.TaskID != checkpoint.TaskID || stored.RunID != checkpoint.RunID {
		return false
	}
	stored.ProductionStatus = ""
	checkpoint.ProductionStatus = ""
	stored.Validation = StoryValidationResult{}
	checkpoint.Validation = StoryValidationResult{}
	stored.FixedPrefixLength = 0
	checkpoint.FixedPrefixLength = 0
	stored.RepairFromTurn = 0
	checkpoint.RepairFromTurn = 0
	stored.SuffixRegenerations = 0
	checkpoint.SuffixRegenerations = 0
	stored.PlayCount = 0
	checkpoint.PlayCount = 0
	stored.LastPlayedAt = nil
	checkpoint.LastPlayedAt = nil
	stored.CreatedAt = time.Time{}
	checkpoint.CreatedAt = time.Time{}
	stored.UpdatedAt = time.Time{}
	checkpoint.UpdatedAt = time.Time{}
	return reflect.DeepEqual(stored, checkpoint)
}
