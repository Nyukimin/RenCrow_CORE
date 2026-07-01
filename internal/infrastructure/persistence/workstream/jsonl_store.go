package workstream

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	domainworkstream "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/workstream"
)

type JSONLStore struct {
	workstreamPath  string
	goalPath        string
	artifactPath    string
	annotationPath  string
	steeringPath    string
	heartbeatPath   string
	vaultUpdatePath string
	vaultRoot       string
}

func NewJSONLStore(root string) *JSONLStore {
	if root == "" {
		root = "workspace/logs/workstream"
	}
	return &JSONLStore{
		workstreamPath:  filepath.Join(root, "workstream.jsonl"),
		goalPath:        filepath.Join(root, "workstream_goal.jsonl"),
		artifactPath:    filepath.Join(root, "artifact.jsonl"),
		annotationPath:  filepath.Join(root, "artifact_annotation.jsonl"),
		steeringPath:    filepath.Join(root, "steering_queue.jsonl"),
		heartbeatPath:   filepath.Join(root, "heartbeat_schedule.jsonl"),
		vaultUpdatePath: filepath.Join(root, "vault_update_log.jsonl"),
	}
}

func NewJSONLStoreWithVault(root, vaultRoot string) *JSONLStore {
	store := NewJSONLStore(root)
	store.vaultRoot = vaultRoot
	return store
}

func (s *JSONLStore) SaveWorkstream(_ context.Context, item domainworkstream.Workstream) error {
	if err := domainworkstream.ValidateWorkstream(item); err != nil {
		return err
	}
	if s.vaultRoot != "" {
		vaultPath, err := ensureVaultFiles(s.vaultRoot, item)
		if err != nil {
			return err
		}
		if item.VaultPath == "" {
			item.VaultPath = vaultPath
		}
	}
	return appendJSONL(s.workstreamPath, item)
}

func (s *JSONLStore) ListWorkstreams(_ context.Context, limit int) ([]domainworkstream.Workstream, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []domainworkstream.Workstream
	if err := readJSONL(s.workstreamPath, func(line []byte) error {
		var item domainworkstream.Workstream
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

func (s *JSONLStore) SaveGoal(_ context.Context, goal domainworkstream.Goal) error {
	if err := domainworkstream.ValidateGoal(goal); err != nil {
		return err
	}
	return appendJSONL(s.goalPath, goal)
}

func (s *JSONLStore) ListGoals(_ context.Context, limit int) ([]domainworkstream.Goal, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []domainworkstream.Goal
	if err := readJSONL(s.goalPath, func(line []byte) error {
		var item domainworkstream.Goal
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

func (s *JSONLStore) SaveArtifact(_ context.Context, item domainworkstream.Artifact) error {
	if err := domainworkstream.ValidateArtifact(item); err != nil {
		return err
	}
	return appendJSONL(s.artifactPath, item)
}

func (s *JSONLStore) ListArtifacts(_ context.Context, limit int) ([]domainworkstream.Artifact, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []domainworkstream.Artifact
	if err := readJSONL(s.artifactPath, func(line []byte) error {
		var item domainworkstream.Artifact
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

func (s *JSONLStore) SaveArtifactAnnotation(_ context.Context, item domainworkstream.ArtifactAnnotation) error {
	if err := domainworkstream.ValidateArtifactAnnotation(item); err != nil {
		return err
	}
	return appendJSONL(s.annotationPath, item)
}

func (s *JSONLStore) ListArtifactAnnotations(_ context.Context, limit int) ([]domainworkstream.ArtifactAnnotation, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []domainworkstream.ArtifactAnnotation
	if err := readJSONL(s.annotationPath, func(line []byte) error {
		var item domainworkstream.ArtifactAnnotation
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

func (s *JSONLStore) SaveSteeringItem(_ context.Context, item domainworkstream.SteeringItem) error {
	if err := domainworkstream.ValidateSteeringItem(item); err != nil {
		return err
	}
	return appendJSONL(s.steeringPath, item)
}

func (s *JSONLStore) ListSteeringItems(_ context.Context, limit int) ([]domainworkstream.SteeringItem, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []domainworkstream.SteeringItem
	if err := readJSONL(s.steeringPath, func(line []byte) error {
		var item domainworkstream.SteeringItem
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

func (s *JSONLStore) SaveHeartbeatSchedule(_ context.Context, item domainworkstream.HeartbeatSchedule) error {
	if err := domainworkstream.ValidateHeartbeatSchedule(item); err != nil {
		return err
	}
	return appendJSONL(s.heartbeatPath, item)
}

func (s *JSONLStore) ListHeartbeatSchedules(_ context.Context, limit int) ([]domainworkstream.HeartbeatSchedule, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []domainworkstream.HeartbeatSchedule
	if err := readJSONL(s.heartbeatPath, func(line []byte) error {
		var item domainworkstream.HeartbeatSchedule
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

func (s *JSONLStore) SaveVaultUpdateLog(_ context.Context, item domainworkstream.VaultUpdateLog) error {
	if err := domainworkstream.ValidateVaultUpdateLog(item); err != nil {
		return err
	}
	return appendJSONL(s.vaultUpdatePath, item)
}

func (s *JSONLStore) ListVaultUpdateLogs(_ context.Context, limit int) ([]domainworkstream.VaultUpdateLog, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []domainworkstream.VaultUpdateLog
	if err := readJSONL(s.vaultUpdatePath, func(line []byte) error {
		var item domainworkstream.VaultUpdateLog
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	}); err != nil {
		return nil, err
	}
	return latestVaultUpdateLogs(items, limit), nil
}

func latestVaultUpdateLogs(items []domainworkstream.VaultUpdateLog, limit int) []domainworkstream.VaultUpdateLog {
	if limit <= 0 || limit > len(items) {
		limit = len(items)
	}
	seen := map[string]struct{}{}
	out := make([]domainworkstream.VaultUpdateLog, 0, limit)
	for i := len(items) - 1; i >= 0 && len(out) < limit; i-- {
		id := items[i].UpdateID
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, items[i])
	}
	return out
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

func ensureVaultFiles(root string, item domainworkstream.Workstream) (string, error) {
	id := strings.TrimSpace(item.WorkstreamID)
	if id == "" {
		return "", errors.New("workstream_id is required")
	}
	if strings.ContainsAny(id, `/\`) || id == "." || id == ".." || filepath.Base(id) != id {
		return "", fmt.Errorf("invalid workstream_id for vault path: %s", id)
	}
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	files := map[string]string{
		"README.md":     renderVaultREADME(item),
		"STATUS.md":     renderVaultStatus(item),
		"TODO.md":       "# TODO\n\n- [ ] 次の作業を追加する\n",
		"OPEN_LOOPS.md": "# OPEN_LOOPS\n\n- [ ] 未完了事項を追加する\n",
		"ARTIFACTS.md":  "# ARTIFACTS\n\n| artifact | status | path |\n|---|---|---|\n",
		"NOTES.md":      "# NOTES\n\n",
		"MEMORY.md":     "# MEMORY\n\nHuman Review が必要な記憶候補をここに整理する。\n",
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := writeIfMissing(path, content); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func renderVaultREADME(item domainworkstream.Workstream) string {
	return fmt.Sprintf("# %s\n\n- workstream_id: `%s`\n- status: `%s`\n\n## Purpose\n\n%s\n",
		firstNonEmpty(item.Name, item.WorkstreamID),
		item.WorkstreamID,
		firstNonEmpty(item.Status, domainworkstream.StatusDraft),
		firstNonEmpty(item.Description, "未設定"),
	)
}

func renderVaultStatus(item domainworkstream.Workstream) string {
	return fmt.Sprintf(`# STATUS

## Current Goal

未設定

## Current State

%s

## Last Progress

未設定

## Blockers

未設定

## Next Action

未設定

## Last Updated

%s
`, firstNonEmpty(item.Status, domainworkstream.StatusDraft), item.CreatedAt.Format("2006-01-02"))
}

func writeIfMissing(path, content string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.WriteFile(path, []byte(content), 0644)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
