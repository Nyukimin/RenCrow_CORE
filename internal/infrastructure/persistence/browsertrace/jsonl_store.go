package browsertrace

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	domaintrace "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/browsertrace"
)

type JSONLStore struct {
	traceRunPath   string
	candidatePath  string
	schemaPath     string
	validationPath string
	coveragePath   string
	artifactPath   string
}

func NewJSONLStore(root string) *JSONLStore {
	if root == "" {
		root = "workspace/logs/browser_trace_to_api"
	}
	return &JSONLStore{
		traceRunPath:   filepath.Join(root, "browser_trace_run.jsonl"),
		candidatePath:  filepath.Join(root, "api_candidate.jsonl"),
		schemaPath:     filepath.Join(root, "api_candidate_schema.jsonl"),
		validationPath: filepath.Join(root, "api_candidate_validation.jsonl"),
		coveragePath:   filepath.Join(root, "api_coverage_report.jsonl"),
		artifactPath:   filepath.Join(root, "api_artifact.jsonl"),
	}
}

func (s *JSONLStore) SaveTraceRun(_ context.Context, item domaintrace.TraceRun) error {
	if err := domaintrace.ValidateTraceRun(item); err != nil {
		return err
	}
	return appendJSONL(s.traceRunPath, item)
}

func (s *JSONLStore) ListTraceRuns(_ context.Context, limit int) ([]domaintrace.TraceRun, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []domaintrace.TraceRun
	if err := readJSONL(s.traceRunPath, func(line []byte) error {
		var item domaintrace.TraceRun
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	}); err != nil {
		return nil, err
	}
	return reverseLimit(items, limit), nil
}

func (s *JSONLStore) SaveAPICandidate(_ context.Context, item domaintrace.APICandidate) error {
	if err := domaintrace.ValidateAPICandidate(item); err != nil {
		return err
	}
	return appendJSONL(s.candidatePath, item)
}

func (s *JSONLStore) ListAPICandidates(_ context.Context, limit int) ([]domaintrace.APICandidate, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []domaintrace.APICandidate
	if err := readJSONL(s.candidatePath, func(line []byte) error {
		var item domaintrace.APICandidate
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	}); err != nil {
		return nil, err
	}
	return reverseLimit(items, limit), nil
}

func (s *JSONLStore) SaveAPICandidateSchema(_ context.Context, item domaintrace.APICandidateSchema) error {
	if err := domaintrace.ValidateAPICandidateSchema(item); err != nil {
		return err
	}
	return appendJSONL(s.schemaPath, item)
}

func (s *JSONLStore) ListAPICandidateSchemas(_ context.Context, limit int) ([]domaintrace.APICandidateSchema, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []domaintrace.APICandidateSchema
	if err := readJSONL(s.schemaPath, func(line []byte) error {
		var item domaintrace.APICandidateSchema
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	}); err != nil {
		return nil, err
	}
	return reverseLimit(items, limit), nil
}

func (s *JSONLStore) SaveAPICandidateValidationResult(_ context.Context, item domaintrace.APICandidateValidationResult) error {
	if err := domaintrace.ValidateAPICandidateValidationResult(item); err != nil {
		return err
	}
	return appendJSONL(s.validationPath, item)
}

func (s *JSONLStore) ListAPICandidateValidationResults(_ context.Context, limit int) ([]domaintrace.APICandidateValidationResult, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []domaintrace.APICandidateValidationResult
	if err := readJSONL(s.validationPath, func(line []byte) error {
		var item domaintrace.APICandidateValidationResult
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	}); err != nil {
		return nil, err
	}
	return reverseLimit(items, limit), nil
}

func (s *JSONLStore) SaveAPICoverageReport(_ context.Context, item domaintrace.APICoverageReport) error {
	if err := domaintrace.ValidateAPICoverageReport(item); err != nil {
		return err
	}
	return appendJSONL(s.coveragePath, item)
}

func (s *JSONLStore) ListAPICoverageReports(_ context.Context, limit int) ([]domaintrace.APICoverageReport, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []domaintrace.APICoverageReport
	if err := readJSONL(s.coveragePath, func(line []byte) error {
		var item domaintrace.APICoverageReport
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	}); err != nil {
		return nil, err
	}
	return reverseLimit(items, limit), nil
}

func (s *JSONLStore) SaveAPIArtifact(_ context.Context, item domaintrace.APIArtifact) error {
	if err := domaintrace.ValidateAPIArtifact(item); err != nil {
		return err
	}
	return appendJSONL(s.artifactPath, item)
}

func (s *JSONLStore) ListAPIArtifacts(_ context.Context, limit int) ([]domaintrace.APIArtifact, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []domaintrace.APIArtifact
	if err := readJSONL(s.artifactPath, func(line []byte) error {
		var item domaintrace.APIArtifact
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	}); err != nil {
		return nil, err
	}
	return reverseLimit(items, limit), nil
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
	_, err = f.Write(append(line, '\n'))
	return err
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
	if limit <= 0 || limit > len(items) {
		limit = len(items)
	}
	out := make([]T, 0, limit)
	for i := len(items) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, items[i])
	}
	return out
}
