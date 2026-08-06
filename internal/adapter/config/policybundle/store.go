package policybundle

import (
	"fmt"
	"sync"
	"time"

	domainpolicy "github.com/Nyukimin/RenCrow_CORE/internal/domain/policybundle"
)

type Store struct {
	workspaceDir string
	now          func() time.Time
	mu           sync.RWMutex
	active       *domainpolicy.Snapshot
	status       domainpolicy.Status
}

func NewStore(workspaceDir string) *Store {
	return NewStoreWithClock(workspaceDir, func() time.Time { return time.Now().UTC() })
}

func NewStoreWithClock(workspaceDir string, now func() time.Time) *Store {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	store := &Store{workspaceDir: workspaceDir, now: now}
	_ = store.Reload()
	return store
}

func (s *Store) Reload() error {
	if s == nil {
		return fmt.Errorf("policy store is nil")
	}
	attemptedAt := s.now().UTC().Format(time.RFC3339Nano)
	result, attempted, err := loadWorkspace(s.workspaceDir)

	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		active := cloneSnapshot(result.snapshot)
		s.active = &active
		result.status.LastReloadState = domainpolicy.StateActive
		result.status.LastReloadAt = attemptedAt
		result.status.LastSuccessfulLoadAt = attemptedAt
		result.status.ActiveRevisionPreserved = false
		s.status = result.status
		return nil
	}

	attempted.LastReloadAt = attemptedAt
	attempted.LastReloadState = attempted.State
	attempted.LastReloadError = err.Error()
	if s.active == nil {
		s.status = attempted
		return err
	}
	preserved := s.status
	preserved.State = domainpolicy.StateActive
	preserved.Error = ""
	preserved.LastReloadState = attempted.State
	preserved.LastReloadAt = attemptedAt
	preserved.LastReloadError = err.Error()
	preserved.ActiveRevisionPreserved = true
	s.status = preserved
	return err
}

func (s *Store) Status() domainpolicy.Status {
	if s == nil {
		return domainpolicy.Status{State: domainpolicy.StateInvalid, Error: "policy store is nil"}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneStatus(s.status)
}

func (s *Store) Snapshot() (domainpolicy.Snapshot, bool) {
	if s == nil {
		return domainpolicy.Snapshot{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.active == nil {
		return domainpolicy.Snapshot{}, false
	}
	return cloneSnapshot(*s.active), true
}

func cloneStatus(status domainpolicy.Status) domainpolicy.Status {
	status.DisabledCapabilities = append([]string(nil), status.DisabledCapabilities...)
	return status
}

func cloneSnapshot(snapshot domainpolicy.Snapshot) domainpolicy.Snapshot {
	snapshot.Capabilities = cloneBoolMap(snapshot.Capabilities)
	snapshot.ExternalActions = cloneStringMap(snapshot.ExternalActions)
	snapshot.ProductionDisabled = cloneBoolMap(snapshot.ProductionDisabled)
	return snapshot
}
