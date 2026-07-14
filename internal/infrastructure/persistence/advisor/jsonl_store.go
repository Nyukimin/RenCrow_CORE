package advisor

import (
	"bufio"
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

func (s *JSONLStore) SaveAdvisorAdoption(_ context.Context, item advisorDomain.AdvisorAdoptionRecord) error {
	if err := item.Validate(); err != nil {
		return err
	}
	return s.append(s.adoptionPath, item)
}

func (s *JSONLStore) ListAdvisorAdoptions(_ context.Context, limit int) ([]advisorDomain.AdvisorAdoptionRecord, error) {
	return readJSONL[advisorDomain.AdvisorAdoptionRecord](s, s.adoptionPath, limit)
}

func (s *JSONLStore) SaveAdvisorScoreSnapshot(_ context.Context, item advisorDomain.AdvisorScoreSnapshot) error {
	if err := item.Validate(); err != nil {
		return err
	}
	return s.append(s.scorePath, item)
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
