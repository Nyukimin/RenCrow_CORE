package orchestrator

import (
	"strings"
	"sync"
)

var coderStatusSlotOrder = [...]string{"coder1", "coder2", "coder3", "coder4"}

// CoderStatus はCoderの利用状態を管理する
type CoderStatus struct {
	mu        sync.Mutex
	busy      map[string]bool
	nextIndex int
}

// NewCoderStatus は新しいCoderStatusを作成
func NewCoderStatus() *CoderStatus {
	return &CoderStatus{
		busy: make(map[string]bool),
	}
}

// Acquire はCoderがビジーでなければ占有してtrueを返す
func (s *CoderStatus) Acquire(name string) bool {
	if s == nil {
		return false
	}
	name = normalizeCoderStatusName(name)
	if name == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureBusyLocked()
	if s.busy[name] {
		return false
	}
	s.busy[name] = true
	return true
}

// AcquireNext は候補の中からround-robin順にidleなCoderを一つだけ取得する。
// 候補判定、busy確認、取得、cursor更新は同じlockで行う。
func (s *CoderStatus) AcquireNext(candidates []string) (string, bool) {
	if s == nil || len(candidates) == 0 {
		return "", false
	}
	eligible := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if name := normalizeCoderStatusName(candidate); name != "" {
			eligible[name] = struct{}{}
		}
	}
	if len(eligible) == 0 {
		return "", false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureBusyLocked()
	for offset := range coderStatusSlotOrder {
		index := (s.nextIndex + offset) % len(coderStatusSlotOrder)
		name := coderStatusSlotOrder[index]
		if _, ok := eligible[name]; !ok || s.busy[name] {
			continue
		}
		s.busy[name] = true
		s.nextIndex = (index + 1) % len(coderStatusSlotOrder)
		return name, true
	}
	return "", false
}

// Release はCoderを解放する
func (s *CoderStatus) Release(name string) {
	if s == nil {
		return
	}
	name = normalizeCoderStatusName(name)
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.busy, name)
}

// IsBusy はCoderがビジーかどうかを返す
func (s *CoderStatus) IsBusy(name string) bool {
	if s == nil {
		return false
	}
	name = normalizeCoderStatusName(name)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy[name]
}

func (s *CoderStatus) ensureBusyLocked() {
	if s.busy == nil {
		s.busy = make(map[string]bool)
	}
}

func normalizeCoderStatusName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
