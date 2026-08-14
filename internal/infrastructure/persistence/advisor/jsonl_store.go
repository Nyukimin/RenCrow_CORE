package advisor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	advisorDomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/advisor"
	domainagentprofile "github.com/Nyukimin/RenCrow_CORE/internal/domain/agentprofile"
)

type JSONLStore struct {
	runPath      string
	adoptionPath string
	scorePath    string
	policyPath   string
	mu           sync.RWMutex
}

func NewJSONLStore(root string) *JSONLStore {
	if root == "" {
		root = "workspace/logs/advisor"
	}
	return &JSONLStore{
		runPath:      filepath.Join(root, "advisor_run.jsonl"),
		adoptionPath: filepath.Join(root, "advisor_adoption.jsonl"),
		scorePath:    filepath.Join(root, "advisor_score_snapshot.jsonl"),
		policyPath:   filepath.Join(root, "agent_policy_decision.jsonl"),
	}
}

func (s *JSONLStore) SaveAdviceRun(_ context.Context, item advisorDomain.AdviceRunRecord) error {
	if err := item.Validate(); err != nil {
		return err
	}
	return s.append(s.runPath, item)
}

func (s *JSONLStore) ListAdviceRuns(_ context.Context, limit int) ([]advisorDomain.AdviceRunRecord, error) {
	return readJSONL[advisorDomain.AdviceRunRecord](s, s.runPath, limit)
}

// FindAdviceRunByID returns the latest JSONL record with the exact primary ID.
func (s *JSONLStore) FindAdviceRunByID(_ context.Context, runID string) (advisorDomain.AdviceRunRecord, bool, error) {
	if s == nil {
		return advisorDomain.AdviceRunRecord{}, false, errors.New("advisor jsonl store is required")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	file, err := os.Open(s.runPath)
	if errors.Is(err, os.ErrNotExist) {
		return advisorDomain.AdviceRunRecord{}, false, nil
	}
	if err != nil {
		return advisorDomain.AdviceRunRecord{}, false, err
	}
	defer file.Close()
	var latest advisorDomain.AdviceRunRecord
	found := false
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var item advisorDomain.AdviceRunRecord
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
			return advisorDomain.AdviceRunRecord{}, false, err
		}
		if item.RunID == runID {
			latest = item
			found = true
		}
	}
	if err := scanner.Err(); err != nil {
		return advisorDomain.AdviceRunRecord{}, false, err
	}
	return latest, found, nil
}

func (s *JSONLStore) SaveAdvisorAdoption(_ context.Context, item advisorDomain.AdvisorAdoptionRecord) error {
	if err := item.Validate(); err != nil {
		return err
	}
	return s.upsert(s.adoptionPath, "adoption_id", item.AdoptionID, item)
}

func (s *JSONLStore) ListAdvisorAdoptions(_ context.Context, limit int) ([]advisorDomain.AdvisorAdoptionRecord, error) {
	return readJSONL[advisorDomain.AdvisorAdoptionRecord](s, s.adoptionPath, limit)
}

// FindAdvisorAdoptionByID returns the latest JSONL record with the exact primary ID.
func (s *JSONLStore) FindAdvisorAdoptionByID(_ context.Context, adoptionID string) (advisorDomain.AdvisorAdoptionRecord, bool, error) {
	if s == nil {
		return advisorDomain.AdvisorAdoptionRecord{}, false, errors.New("advisor jsonl store is required")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	file, err := os.Open(s.adoptionPath)
	if errors.Is(err, os.ErrNotExist) {
		return advisorDomain.AdvisorAdoptionRecord{}, false, nil
	}
	if err != nil {
		return advisorDomain.AdvisorAdoptionRecord{}, false, err
	}
	defer file.Close()
	var latest advisorDomain.AdvisorAdoptionRecord
	found := false
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var item advisorDomain.AdvisorAdoptionRecord
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
			return advisorDomain.AdvisorAdoptionRecord{}, false, err
		}
		if item.AdoptionID == adoptionID {
			latest = item
			found = true
		}
	}
	if err := scanner.Err(); err != nil {
		return advisorDomain.AdvisorAdoptionRecord{}, false, err
	}
	return latest, found, nil
}

func (s *JSONLStore) SaveAdvisorScoreSnapshot(_ context.Context, item advisorDomain.AdvisorScoreSnapshot) error {
	if err := item.Validate(); err != nil {
		return err
	}
	return s.upsert(s.scorePath, "snapshot_id", item.SnapshotID, item)
}

func (s *JSONLStore) ListAdvisorScoreSnapshots(_ context.Context, limit int) ([]advisorDomain.AdvisorScoreSnapshot, error) {
	return readJSONL[advisorDomain.AdvisorScoreSnapshot](s, s.scorePath, limit)
}

func (s *JSONLStore) SaveAgentPolicyDecision(_ context.Context, item domainagentprofile.PolicyDecision) error {
	if err := item.Validate(); err != nil {
		return err
	}
	return s.append(s.policyPath, item)
}

func (s *JSONLStore) ListAgentPolicyDecisions(_ context.Context, limit int) ([]domainagentprofile.PolicyDecision, error) {
	return readJSONL[domainagentprofile.PolicyDecision](s, s.policyPath, limit)
}

func (s *JSONLStore) append(path string, item any) error {
	if s == nil {
		return errors.New("advisor jsonl store is required")
	}
	payload, err := json.Marshal(item)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(append(payload, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

func (s *JSONLStore) upsert(path, idField, id string, item any) error {
	if s == nil {
		return errors.New("advisor jsonl store is required")
	}
	payload, err := json.Marshal(item)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	lines := bytes.Split(bytes.TrimSpace(existing), []byte{'\n'})
	output := make([][]byte, 0, len(lines)+1)
	replaced := false
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var metadata map[string]json.RawMessage
		if err := json.Unmarshal(line, &metadata); err != nil {
			return err
		}
		var existingID string
		if raw := metadata[idField]; len(raw) > 0 {
			if err := json.Unmarshal(raw, &existingID); err != nil {
				return err
			}
		}
		if existingID == id {
			output = append(output, payload)
			replaced = true
			continue
		}
		output = append(output, append([]byte(nil), line...))
	}
	if !replaced {
		output = append(output, payload)
	}
	encoded := bytes.Join(output, []byte{'\n'})
	encoded = append(encoded, '\n')
	temp, err := os.CreateTemp(dir, ".advisor-upsert-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(encoded); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func readJSONL[T any](s *JSONLStore, path string, limit int) ([]T, error) {
	if s == nil {
		return nil, errors.New("advisor jsonl store is required")
	}
	if limit <= 0 {
		limit = 50
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return []T{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	items := []T{}
	scanner := bufio.NewScanner(file)
	buffer := make([]byte, 0, 64*1024)
	scanner.Buffer(buffer, 1024*1024)
	for scanner.Scan() {
		var item T
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	start := len(items) - limit
	if start < 0 {
		start = 0
	}
	result := make([]T, 0, len(items)-start)
	for index := len(items) - 1; index >= start; index-- {
		result = append(result, items[index])
	}
	return result, nil
}
