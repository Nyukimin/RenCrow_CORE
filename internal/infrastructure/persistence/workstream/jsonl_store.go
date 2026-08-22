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
	"sync"
	"time"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
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
	leasePath       string
	queueFreezePath string
	stageRunPath    string
	closurePath     string
	// lifecycleMu protects the check-and-append sequences for both the
	// singleton implementation lease and queue-freeze resolution.  JSONL has
	// no transaction primitive, so all owner lifecycle decisions share this
	// mutex.
	lifecycleMu sync.Mutex
	// resolutionAppendHook is a test-only fault seam.  It is intentionally
	// unexported so production callers cannot alter the lifecycle protocol.
	resolutionAppendHook func(string) error
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
		leasePath:       filepath.Join(root, "implementation_lease.jsonl"),
		queueFreezePath: filepath.Join(root, "queue_freeze.jsonl"),
		stageRunPath:    filepath.Join(root, "stage_run_receipt.jsonl"),
		closurePath:     filepath.Join(root, "closure_receipt.jsonl"),
	}
}

func (s *JSONLStore) SaveQueueFreeze(_ context.Context, item domainworkstream.QueueFreeze) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return s.saveQueueFreezeLocked(item)
}

func (s *JSONLStore) saveQueueFreezeLocked(item domainworkstream.QueueFreeze) error {
	if item.FreezeRevision < 1 {
		item.FreezeRevision = 1
	}
	if item.Status == "" {
		item.Status = domainworkstream.QueueFreezeActive
	}
	if item.UpdatedAt.IsZero() {
		item.UpdatedAt = item.CreatedAt
	}
	if err := domainworkstream.ValidateQueueFreeze(item); err != nil {
		return err
	}
	return appendJSONL(s.queueFreezePath, item)
}

func (s *JSONLStore) GetQueueFreeze(_ context.Context, freezeID string) (domainworkstream.QueueFreeze, bool, error) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return s.getQueueFreezeLocked(freezeID)
}

func (s *JSONLStore) getQueueFreezeLocked(freezeID string) (domainworkstream.QueueFreeze, bool, error) {
	var found domainworkstream.QueueFreeze
	matched := false
	err := readJSONL(s.queueFreezePath, func(line []byte) error {
		var item domainworkstream.QueueFreeze
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		if item.FreezeID == freezeID {
			found, matched = item, true
		}
		return nil
	})
	return found, matched, err
}

func (s *JSONLStore) ListQueueFreezes(_ context.Context, limit int) ([]domainworkstream.QueueFreeze, error) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return s.listQueueFreezesLocked(limit)
}

func (s *JSONLStore) listQueueFreezesLocked(limit int) ([]domainworkstream.QueueFreeze, error) {
	var items []domainworkstream.QueueFreeze
	if err := readJSONL(s.queueFreezePath, func(line []byte) error {
		var item domainworkstream.QueueFreeze
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	}); err != nil {
		return nil, err
	}
	return latestQueueFreezes(items, limit), nil
}

func latestQueueFreezes(items []domainworkstream.QueueFreeze, limit int) []domainworkstream.QueueFreeze {
	seen := map[string]struct{}{}
	out := make([]domainworkstream.QueueFreeze, 0, len(items))
	for index := len(items) - 1; index >= 0; index-- {
		if _, ok := seen[items[index].FreezeID]; ok {
			continue
		}
		seen[items[index].FreezeID] = struct{}{}
		out = append(out, items[index])
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func (s *JSONLStore) latestActiveQueueFreezeLocked() (domainworkstream.QueueFreeze, bool, error) {
	items, err := s.listQueueFreezesLocked(0)
	if err != nil {
		return domainworkstream.QueueFreeze{}, false, err
	}
	for _, item := range items {
		// Empty status is treated as active for legacy freeze records.  A
		// malformed/unknown status therefore fails closed rather than silently
		// reopening the queue.
		if item.Status == domainworkstream.QueueFreezeActive || strings.TrimSpace(item.Status) == "" {
			return item, true, nil
		}
	}
	return domainworkstream.QueueFreeze{}, false, nil
}

func (s *JSONLStore) SaveStageRunReceipt(_ context.Context, item domainworkstream.StageRunReceipt) error {
	if err := domainworkstream.ValidateStageRunReceipt(item); err != nil {
		return err
	}
	return appendJSONL(s.stageRunPath, item)
}

func (s *JSONLStore) FindStageRunReceipt(_ context.Context, key string) (domainworkstream.StageRunReceipt, bool, error) {
	var found domainworkstream.StageRunReceipt
	matched := false
	err := readJSONL(s.stageRunPath, func(line []byte) error {
		var item domainworkstream.StageRunReceipt
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		if item.IdempotencyKey == key || item.ReceiptID == key {
			found, matched = item, true
		}
		return nil
	})
	return found, matched, err
}

func (s *JSONLStore) GetStageRunReceipt(ctx context.Context, key string) (domainworkstream.StageRunReceipt, bool, error) {
	return s.FindStageRunReceipt(ctx, key)
}

func (s *JSONLStore) ListStageRunReceipts(_ context.Context, limit int) ([]domainworkstream.StageRunReceipt, error) {
	var items []domainworkstream.StageRunReceipt
	if err := readJSONL(s.stageRunPath, func(line []byte) error {
		var item domainworkstream.StageRunReceipt
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	}); err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	out := make([]domainworkstream.StageRunReceipt, 0, len(items))
	for index := len(items) - 1; index >= 0; index-- {
		key := items[index].IdempotencyKey
		if key == "" {
			key = items[index].ReceiptID
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, items[index])
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *JSONLStore) SaveClosureReceipt(_ context.Context, item domainworkstream.ClosureReceipt) error {
	if err := domainworkstream.ValidateClosureReceipt(item); err != nil {
		return err
	}
	return appendJSONL(s.closurePath, item)
}

func (s *JSONLStore) FindClosureReceipt(_ context.Context, key string) (domainworkstream.ClosureReceipt, bool, error) {
	var found domainworkstream.ClosureReceipt
	matched := false
	err := readJSONL(s.closurePath, func(line []byte) error {
		var item domainworkstream.ClosureReceipt
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		if item.IdempotencyKey == key || item.ReceiptID == key {
			found, matched = item, true
		}
		return nil
	})
	return found, matched, err
}

func (s *JSONLStore) GetClosureReceipt(ctx context.Context, key string) (domainworkstream.ClosureReceipt, bool, error) {
	return s.FindClosureReceipt(ctx, key)
}

func (s *JSONLStore) ListClosureReceipts(_ context.Context, limit int) ([]domainworkstream.ClosureReceipt, error) {
	var items []domainworkstream.ClosureReceipt
	if err := readJSONL(s.closurePath, func(line []byte) error {
		var item domainworkstream.ClosureReceipt
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	}); err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	out := make([]domainworkstream.ClosureReceipt, 0, len(items))
	for index := len(items) - 1; index >= 0; index-- {
		key := items[index].IdempotencyKey
		if key == "" {
			key = items[index].ReceiptID
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, items[index])
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// AcquireImplementationLeaseIfUnfrozen performs the freeze check and
// singleton lease check under the same JSONL lifecycle mutex.
func (s *JSONLStore) AcquireImplementationLeaseIfUnfrozen(_ context.Context, item domainworkstream.ImplementationLease) (bool, string, error) {
	if err := domainworkstream.ValidateImplementationLease(item); err != nil {
		return false, "", err
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if _, frozen, err := s.latestActiveQueueFreezeLocked(); err != nil {
		return false, "", err
	} else if frozen {
		return false, domainworkstream.ErrQueueFrozen.Error(), nil
	}
	current, ok, err := s.latestImplementationLeaseLocked()
	if err != nil {
		return false, "", err
	}
	if ok && current.HolderUnitID != "" && current.HolderUnitID != item.HolderUnitID {
		return false, domainworkstream.ErrImplementationLeaseHeld.Error(), nil
	}
	if err := appendJSONL(s.leasePath, item); err != nil {
		return false, "", err
	}
	return true, "", nil
}

// AcquireImplementationLease persists Atlas's singleton WIP lease in the
// existing Workstream JSONL root. Legacy callers retain the old result shape;
// the revision-2 path uses AcquireImplementationLeaseIfUnfrozen directly.
func (s *JSONLStore) AcquireImplementationLease(ctx context.Context, item domainworkstream.ImplementationLease) (bool, error) {
	acquired, _, err := s.AcquireImplementationLeaseIfUnfrozen(ctx, item)
	return acquired, err
}

// ResolveQueueFreezeAndAcquireLease resolves one exact active freeze and
// acquires the replacement lease while holding the same lifecycle mutex.
func (s *JSONLStore) ResolveQueueFreezeAndAcquireLease(_ context.Context, freezeID string, resolution domainworkstream.QueueFreezeResolution, replacement domainworkstream.ImplementationLease) (domainworkstream.QueueFreeze, domainworkstream.ImplementationLease, bool, error) {
	if strings.TrimSpace(freezeID) == "" {
		return domainworkstream.QueueFreeze{}, domainworkstream.ImplementationLease{}, false, fmt.Errorf("freeze_id is required")
	}
	if err := domainworkstream.ValidateQueueFreezeResolution(resolution, replacement); err != nil {
		return domainworkstream.QueueFreeze{}, domainworkstream.ImplementationLease{}, false, err
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	freeze, found, err := s.getQueueFreezeLocked(freezeID)
	if err != nil {
		return domainworkstream.QueueFreeze{}, domainworkstream.ImplementationLease{}, false, err
	}
	if !found {
		return domainworkstream.QueueFreeze{}, domainworkstream.ImplementationLease{}, false, domainworkstream.ErrQueueFreezeNotFound
	}
	if freeze.Status == domainworkstream.QueueFreezeResolved {
		if !freeze.MatchesResolved(resolution) {
			return freeze, domainworkstream.ImplementationLease{}, false, domainworkstream.ErrQueueFreezeResolutionConflict
		}
		if freeze.ResolutionAcquired {
			return freeze, freeze.ReplacementLease, true, nil
		}
		return freeze, domainworkstream.ImplementationLease{}, false, nil
	}
	if freeze.Status != domainworkstream.QueueFreezeActive && strings.TrimSpace(freeze.Status) != "" {
		return freeze, domainworkstream.ImplementationLease{}, false, domainworkstream.ErrQueueFreezeResolutionConflict
	}
	if freeze.FreezeRevision != resolution.ExpectedFreezeRevision {
		return freeze, domainworkstream.ImplementationLease{}, false, fmt.Errorf("%w: expected %d current %d", domainworkstream.ErrQueueFreezeRevisionConflict, resolution.ExpectedFreezeRevision, freeze.FreezeRevision)
	}
	pendingResolution := strings.TrimSpace(freeze.ResolutionRequestID) != ""
	if pendingResolution && !freeze.MatchesResolution(resolution) {
		return freeze, domainworkstream.ImplementationLease{}, false, domainworkstream.ErrQueueFreezeResolutionConflict
	}
	current, leaseFound, err := s.latestImplementationLeaseLocked()
	if err != nil {
		return freeze, domainworkstream.ImplementationLease{}, false, err
	}
	// A release tombstone is the latest JSONL record but does not represent a
	// held lease for the resolution decision.
	if leaseFound && strings.TrimSpace(current.HolderUnitID) == "" {
		leaseFound = false
	}
	if leaseFound && current.HolderUnitID != "" && current.HolderUnitID != replacement.HolderUnitID {
		return freeze, domainworkstream.ImplementationLease{}, false, nil
	}
	if leaseFound && !jsonlLeaseMatchesReplacement(current, replacement) {
		return freeze, domainworkstream.ImplementationLease{}, false, domainworkstream.ErrQueueFreezeResolutionConflict
	}

	// Persist the request identity as an active pending freeze before the
	// replacement lease.  If the process stops after the lease append, the
	// latest freeze remains active and the queue stays fail-closed; replay can
	// compare the exact payload rather than guessing which request was in
	// flight.
	if !pendingResolution {
		now := time.Now().UTC()
		freeze.ResolutionRequestID = resolution.ResolutionRequestID
		freeze.ReplacementUnitID = resolution.ReplacementUnitID
		freeze.SupersedesUnitID = resolution.SupersedesUnitID
		freeze.BlockerResolutionRefs = append([]domainbacklog.EvidenceRef(nil), resolution.BlockerResolutionRefs...)
		freeze.ResolutionPayloadHash = resolution.ResolutionPayloadHash
		freeze.ResolutionAcquired = false
		freeze.UpdatedAt = now
		if err := s.saveQueueFreezeLocked(freeze); err != nil {
			return freeze, domainworkstream.ImplementationLease{}, false, err
		}
	}

	if !leaseFound {
		if err := appendJSONL(s.leasePath, replacement); err != nil {
			return freeze, domainworkstream.ImplementationLease{}, false, err
		}
	}
	if s.resolutionAppendHook != nil {
		if err := s.resolutionAppendHook("after_lease_before_resolved_freeze"); err != nil {
			return freeze, replacement, false, err
		}
	}

	// The replacement lease is durable before the resolved freeze record is
	// appended.  A write failure therefore leaves only active freeze + matching
	// lease, which blocks every other unit and is safe to finish by replay.
	now := time.Now().UTC()
	freeze.Status = domainworkstream.QueueFreezeResolved
	freeze.ReplacementLease = replacement
	freeze.ResolutionAcquired = true
	freeze.UpdatedAt = now
	freeze.ResolvedAt = now
	if err := s.saveQueueFreezeLocked(freeze); err != nil {
		return freeze, replacement, false, err
	}
	return freeze, replacement, true, nil
}

func jsonlLeaseMatchesReplacement(current, replacement domainworkstream.ImplementationLease) bool {
	return current.LeaseName == replacement.LeaseName &&
		current.HolderUnitID == replacement.HolderUnitID &&
		current.HolderWorkstreamID == replacement.HolderWorkstreamID &&
		current.Stage == replacement.Stage &&
		current.Revision == replacement.Revision
}

func (s *JSONLStore) ReleaseImplementationLease(_ context.Context, leaseName, holderUnitID string) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	current, ok, err := s.latestImplementationLeaseLocked()
	if err != nil {
		return err
	}
	if !ok || current.LeaseName != leaseName || (holderUnitID != "" && current.HolderUnitID != holderUnitID) {
		return nil
	}
	// A tombstone keeps the append-only history while making the latest state
	// unambiguously free.
	return appendJSONL(s.leasePath, map[string]any{
		"lease_name": leaseName, "released_by": holderUnitID,
		"released_at": time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func (s *JSONLStore) GetImplementationLease(_ context.Context, leaseName string) (domainworkstream.ImplementationLease, bool, error) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	item, ok, err := s.latestImplementationLeaseLocked()
	if err != nil || !ok || item.LeaseName != leaseName || item.HolderUnitID == "" {
		return domainworkstream.ImplementationLease{}, false, err
	}
	return item, true, nil
}

func (s *JSONLStore) HeartbeatImplementationLease(_ context.Context, item domainworkstream.ImplementationLease) error {
	if strings.TrimSpace(item.LeaseName) == "" || strings.TrimSpace(item.HolderUnitID) == "" {
		return fmt.Errorf("lease_name and holder_unit_id are required")
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	current, ok, err := s.latestImplementationLeaseLocked()
	if err != nil {
		return err
	}
	if !ok || current.HolderUnitID != item.HolderUnitID || current.LeaseName != item.LeaseName {
		return fmt.Errorf("implementation lease is not held by %s", item.HolderUnitID)
	}
	return appendJSONL(s.leasePath, item)
}

// Short aliases keep the store convenient for narrow owner adapters.
func (s *JSONLStore) AcquireLease(ctx context.Context, item domainworkstream.ImplementationLease) (bool, error) {
	return s.AcquireImplementationLease(ctx, item)
}
func (s *JSONLStore) ReleaseLease(ctx context.Context, name, holder string) error {
	return s.ReleaseImplementationLease(ctx, name, holder)
}
func (s *JSONLStore) GetLease(ctx context.Context, name string) (domainworkstream.ImplementationLease, bool, error) {
	return s.GetImplementationLease(ctx, name)
}
func (s *JSONLStore) HeartbeatLease(ctx context.Context, item domainworkstream.ImplementationLease) error {
	return s.HeartbeatImplementationLease(ctx, item)
}

func (s *JSONLStore) latestImplementationLeaseLocked() (domainworkstream.ImplementationLease, bool, error) {
	var latest domainworkstream.ImplementationLease
	found := false
	err := readJSONL(s.leasePath, func(line []byte) error {
		var item domainworkstream.ImplementationLease
		if err := json.Unmarshal(line, &item); err == nil && item.LeaseName != "" {
			latest = item
			found = true
		}
		return nil
	})
	return latest, found, err
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

// FindGoalByID returns the latest JSONL record with the exact primary ID.
func (s *JSONLStore) FindGoalByID(_ context.Context, goalID string) (domainworkstream.Goal, bool, error) {
	var found domainworkstream.Goal
	matched := false
	if err := readJSONL(s.goalPath, func(line []byte) error {
		var item domainworkstream.Goal
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		if item.GoalID == goalID {
			found = item
			matched = true
		}
		return nil
	}); err != nil {
		return domainworkstream.Goal{}, false, err
	}
	return found, matched, nil
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
		"MEMORY.md":     "# MEMORY\n\nQuality Review が必要な記憶候補をここに整理する。\n",
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
