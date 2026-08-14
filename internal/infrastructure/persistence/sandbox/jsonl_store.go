package sandbox

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	domainsandbox "github.com/Nyukimin/RenCrow_CORE/internal/domain/sandbox"
)

type JSONLStore struct {
	sandboxPath   string
	artifactPath  string
	promotionPath string
	gateLogPath   string
}

func NewJSONLStore(root string) *JSONLStore {
	if root == "" {
		root = "workspace/logs/sandbox"
	}
	return &JSONLStore{
		sandboxPath:   filepath.Join(root, "sandbox_registry.jsonl"),
		artifactPath:  filepath.Join(root, "sandbox_artifact.jsonl"),
		promotionPath: filepath.Join(root, "sandbox_promotion_request.jsonl"),
		gateLogPath:   filepath.Join(root, "promotion_gate_log.jsonl"),
	}
}

func (s *JSONLStore) SaveSandbox(_ context.Context, record domainsandbox.SandboxRecord) error {
	if err := domainsandbox.ValidateSandboxRecord(record); err != nil {
		return err
	}
	return appendJSONL(s.sandboxPath, record)
}

func (s *JSONLStore) ListSandboxes(_ context.Context, limit int) ([]domainsandbox.SandboxRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	var records []domainsandbox.SandboxRecord
	if err := readJSONL(s.sandboxPath, func(line []byte) error {
		var record domainsandbox.SandboxRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return err
		}
		records = append(records, record)
		return nil
	}); err != nil {
		return nil, err
	}
	return reverseLimit(records, limit), nil
}

// FindSandboxByID returns the latest JSONL record with the exact primary ID.
func (s *JSONLStore) FindSandboxByID(_ context.Context, sandboxID string) (domainsandbox.SandboxRecord, bool, error) {
	var found domainsandbox.SandboxRecord
	matched := false
	if err := readJSONL(s.sandboxPath, func(line []byte) error {
		var record domainsandbox.SandboxRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return err
		}
		if err := domainsandbox.ValidateSandboxRecord(record); err != nil {
			return err
		}
		if record.SandboxID == sandboxID {
			found = record
			matched = true
		}
		return nil
	}); err != nil {
		return domainsandbox.SandboxRecord{}, false, err
	}
	return found, matched, nil
}

func (s *JSONLStore) SaveSandboxArtifact(_ context.Context, artifact domainsandbox.SandboxArtifact) error {
	if err := domainsandbox.ValidateSandboxArtifact(artifact); err != nil {
		return err
	}
	return appendJSONL(s.artifactPath, artifact)
}

func (s *JSONLStore) ListSandboxArtifacts(_ context.Context, limit int) ([]domainsandbox.SandboxArtifact, error) {
	if limit <= 0 {
		limit = 50
	}
	var artifacts []domainsandbox.SandboxArtifact
	if err := readJSONL(s.artifactPath, func(line []byte) error {
		var artifact domainsandbox.SandboxArtifact
		if err := json.Unmarshal(line, &artifact); err != nil {
			return err
		}
		artifacts = append(artifacts, artifact)
		return nil
	}); err != nil {
		return nil, err
	}
	return reverseLimit(artifacts, limit), nil
}

// FindSandboxArtifactByID returns the latest JSONL record with the exact primary ID.
func (s *JSONLStore) FindSandboxArtifactByID(_ context.Context, artifactID string) (domainsandbox.SandboxArtifact, bool, error) {
	var found domainsandbox.SandboxArtifact
	matched := false
	if err := readJSONL(s.artifactPath, func(line []byte) error {
		var artifact domainsandbox.SandboxArtifact
		if err := json.Unmarshal(line, &artifact); err != nil {
			return err
		}
		if err := domainsandbox.ValidateSandboxArtifact(artifact); err != nil {
			return err
		}
		if artifact.ArtifactID == artifactID {
			found = artifact
			matched = true
		}
		return nil
	}); err != nil {
		return domainsandbox.SandboxArtifact{}, false, err
	}
	return found, matched, nil
}

func (s *JSONLStore) SavePromotionRequest(_ context.Context, req domainsandbox.PromotionRequest) error {
	if err := domainsandbox.ValidatePromotionRequest(req); err != nil {
		return err
	}
	return appendJSONL(s.promotionPath, req)
}

func (s *JSONLStore) ListPromotionRequests(_ context.Context, limit int) ([]domainsandbox.PromotionRequest, error) {
	if limit <= 0 {
		limit = 50
	}
	var requests []domainsandbox.PromotionRequest
	if err := readJSONL(s.promotionPath, func(line []byte) error {
		var req domainsandbox.PromotionRequest
		if err := json.Unmarshal(line, &req); err != nil {
			return err
		}
		requests = append(requests, req)
		return nil
	}); err != nil {
		return nil, err
	}
	return reverseLimit(requests, limit), nil
}

// FindPromotionRequestByID returns the latest JSONL record with the exact primary ID.
func (s *JSONLStore) FindPromotionRequestByID(_ context.Context, promotionID string) (domainsandbox.PromotionRequest, bool, error) {
	var found domainsandbox.PromotionRequest
	matched := false
	if err := readJSONL(s.promotionPath, func(line []byte) error {
		var request domainsandbox.PromotionRequest
		if err := json.Unmarshal(line, &request); err != nil {
			return err
		}
		if err := domainsandbox.ValidatePromotionRequest(request); err != nil {
			return err
		}
		if request.PromotionID == promotionID {
			found = request
			matched = true
		}
		return nil
	}); err != nil {
		return domainsandbox.PromotionRequest{}, false, err
	}
	return found, matched, nil
}

func (s *JSONLStore) SavePromotionGateLog(_ context.Context, log domainsandbox.PromotionGateLog) error {
	if err := domainsandbox.ValidatePromotionGateLog(log); err != nil {
		return err
	}
	return appendJSONL(s.gateLogPath, log)
}

func (s *JSONLStore) ListPromotionGateLogs(_ context.Context, limit int) ([]domainsandbox.PromotionGateLog, error) {
	if limit <= 0 {
		limit = 50
	}
	var logs []domainsandbox.PromotionGateLog
	if err := readJSONL(s.gateLogPath, func(line []byte) error {
		var log domainsandbox.PromotionGateLog
		if err := json.Unmarshal(line, &log); err != nil {
			return err
		}
		logs = append(logs, log)
		return nil
	}); err != nil {
		return nil, err
	}
	return reverseLimit(logs, limit), nil
}

// FindPromotionGateLogByID returns the latest JSONL record with the exact primary ID.
func (s *JSONLStore) FindPromotionGateLogByID(_ context.Context, eventID string) (domainsandbox.PromotionGateLog, bool, error) {
	var found domainsandbox.PromotionGateLog
	matched := false
	if err := readJSONL(s.gateLogPath, func(line []byte) error {
		var log domainsandbox.PromotionGateLog
		if err := json.Unmarshal(line, &log); err != nil {
			return err
		}
		if err := domainsandbox.ValidatePromotionGateLog(log); err != nil {
			return err
		}
		if log.EventID == eventID {
			found = log
			matched = true
		}
		return nil
	}); err != nil {
		return domainsandbox.PromotionGateLog{}, false, err
	}
	return found, matched, nil
}

func appendJSONL(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	line, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
	return nil
}

func readJSONL(path string, fn func([]byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if err := fn(scanner.Bytes()); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func reverseLimit[T any](items []T, limit int) []T {
	if len(items) == 0 {
		return []T{}
	}
	out := make([]T, 0, min(limit, len(items)))
	for i := len(items) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, items[i])
	}
	return out
}
