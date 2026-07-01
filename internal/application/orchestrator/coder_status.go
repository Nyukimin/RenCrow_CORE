package orchestrator

import "sync"

// CoderStatus はCoderの利用状態を管理する
type CoderStatus struct {
	mu   sync.Mutex
	busy map[string]bool
}

// NewCoderStatus は新しいCoderStatusを作成
func NewCoderStatus() *CoderStatus {
	return &CoderStatus{
		busy: make(map[string]bool),
	}
}

// Acquire はCoderがビジーでなければ占有してtrueを返す
func (s *CoderStatus) Acquire(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy[name] {
		return false
	}
	s.busy[name] = true
	return true
}

// Release はCoderを解放する
func (s *CoderStatus) Release(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.busy, name)
}

// IsBusy はCoderがビジーかどうかを返す
func (s *CoderStatus) IsBusy(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy[name]
}
