package skillgovernance

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	domainskill "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/skillgovernance"
)

type JSONLStore struct {
	registryPath         string
	triggerLogPath       string
	changeLogPath        string
	contributionGatePath string
	externalPRSubmitPath string
	coderTranscriptPath  string
}

func NewJSONLStore(root string) *JSONLStore {
	if root == "" {
		root = "workspace/logs/skill_governance"
	}
	return &JSONLStore{
		registryPath:         filepath.Join(root, "skill_registry.jsonl"),
		triggerLogPath:       filepath.Join(root, "skill_trigger_log.jsonl"),
		changeLogPath:        filepath.Join(root, "skill_change_log.jsonl"),
		contributionGatePath: filepath.Join(root, "contribution_gate_log.jsonl"),
		externalPRSubmitPath: filepath.Join(root, "external_pr_submit_log.jsonl"),
		coderTranscriptPath:  filepath.Join(root, "coder_transcript_log.jsonl"),
	}
}

func (s *JSONLStore) SaveSkillManifest(_ context.Context, manifest domainskill.SkillManifest) error {
	if err := domainskill.ValidateSkillManifest(manifest); err != nil {
		return err
	}
	return appendJSONL(s.registryPath, manifest)
}

func (s *JSONLStore) ListSkillManifests(_ context.Context, limit int) ([]domainskill.SkillManifest, error) {
	if limit <= 0 {
		limit = 50
	}
	var manifests []domainskill.SkillManifest
	if err := readJSONL(s.registryPath, func(line []byte) error {
		var manifest domainskill.SkillManifest
		if err := json.Unmarshal(line, &manifest); err != nil {
			return err
		}
		manifests = append(manifests, manifest)
		return nil
	}); err != nil {
		return nil, err
	}
	return reverseUniqueSkillManifests(manifests, limit), nil
}

func (s *JSONLStore) SaveSkillTriggerLog(_ context.Context, log domainskill.SkillTriggerLog) error {
	if err := domainskill.ValidateSkillTriggerLog(log); err != nil {
		return err
	}
	return appendJSONL(s.triggerLogPath, log)
}

func (s *JSONLStore) ListSkillTriggerLogs(_ context.Context, limit int) ([]domainskill.SkillTriggerLog, error) {
	if limit <= 0 {
		limit = 50
	}
	var logs []domainskill.SkillTriggerLog
	if err := readJSONL(s.triggerLogPath, func(line []byte) error {
		var log domainskill.SkillTriggerLog
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

func (s *JSONLStore) SaveSkillChangeLog(_ context.Context, log domainskill.SkillChangeLog) error {
	if err := domainskill.ValidateSkillChangeLog(log); err != nil {
		return err
	}
	return appendJSONL(s.changeLogPath, log)
}

func (s *JSONLStore) ListSkillChangeLogs(_ context.Context, limit int) ([]domainskill.SkillChangeLog, error) {
	if limit <= 0 {
		limit = 50
	}
	var logs []domainskill.SkillChangeLog
	if err := readJSONL(s.changeLogPath, func(line []byte) error {
		var log domainskill.SkillChangeLog
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

func (s *JSONLStore) SaveContributionGateLog(_ context.Context, log domainskill.ContributionGateLog) error {
	if err := domainskill.ValidateContributionGateLog(log); err != nil {
		return err
	}
	return appendJSONL(s.contributionGatePath, log)
}

func (s *JSONLStore) ListContributionGateLogs(_ context.Context, limit int) ([]domainskill.ContributionGateLog, error) {
	if limit <= 0 {
		limit = 50
	}
	var logs []domainskill.ContributionGateLog
	if err := readJSONL(s.contributionGatePath, func(line []byte) error {
		var log domainskill.ContributionGateLog
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

func (s *JSONLStore) SaveExternalPRSubmitRecord(_ context.Context, record domainskill.ExternalPRSubmitRecord) error {
	if err := domainskill.ValidateExternalPRSubmitRecord(record); err != nil {
		return err
	}
	return appendJSONL(s.externalPRSubmitPath, record)
}

func (s *JSONLStore) ListExternalPRSubmitRecords(_ context.Context, limit int) ([]domainskill.ExternalPRSubmitRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	var records []domainskill.ExternalPRSubmitRecord
	if err := readJSONL(s.externalPRSubmitPath, func(line []byte) error {
		var record domainskill.ExternalPRSubmitRecord
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

func (s *JSONLStore) SaveCoderTranscriptEntry(_ context.Context, entry domainskill.CoderTranscriptEntry) error {
	if err := domainskill.ValidateCoderTranscriptEntry(entry); err != nil {
		return err
	}
	return appendJSONL(s.coderTranscriptPath, entry)
}

func (s *JSONLStore) ListCoderTranscriptEntries(_ context.Context, limit int) ([]domainskill.CoderTranscriptEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	var entries []domainskill.CoderTranscriptEntry
	if err := readJSONL(s.coderTranscriptPath, func(line []byte) error {
		var entry domainskill.CoderTranscriptEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			return err
		}
		entries = append(entries, entry)
		return nil
	}); err != nil {
		return nil, err
	}
	return reverseLimit(entries, limit), nil
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
	if len(items) == 0 {
		return []T{}
	}
	out := make([]T, 0, min(limit, len(items)))
	for i := len(items) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, items[i])
	}
	return out
}

func reverseUniqueSkillManifests(items []domainskill.SkillManifest, limit int) []domainskill.SkillManifest {
	if len(items) == 0 {
		return []domainskill.SkillManifest{}
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]domainskill.SkillManifest, 0, min(limit, len(items)))
	for i := len(items) - 1; i >= 0 && len(out) < limit; i-- {
		manifest := items[i]
		if _, ok := seen[manifest.SkillID]; ok {
			continue
		}
		seen[manifest.SkillID] = struct{}{}
		out = append(out, manifest)
	}
	return out
}
