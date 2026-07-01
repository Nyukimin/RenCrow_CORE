package orchestrator

import (
	"sync"
	"testing"
)

func TestCoderStatus_AcquireAndRelease(t *testing.T) {
	s := NewCoderStatus()

	// 初回Acquire → 成功
	if !s.Acquire("coder1") {
		t.Error("first Acquire should succeed")
	}

	// 同じCoderを再Acquire → 失敗（ビジー）
	if s.Acquire("coder1") {
		t.Error("second Acquire should fail (busy)")
	}

	// IsBusy確認
	if !s.IsBusy("coder1") {
		t.Error("coder1 should be busy")
	}

	// 別のCoderはAcquire可能
	if !s.Acquire("coder2") {
		t.Error("coder2 should be acquirable")
	}

	// Release後は再Acquire可能
	s.Release("coder1")
	if s.IsBusy("coder1") {
		t.Error("coder1 should not be busy after release")
	}
	if !s.Acquire("coder1") {
		t.Error("Acquire after release should succeed")
	}
}

func TestCoderStatus_ConcurrentAccess(t *testing.T) {
	s := NewCoderStatus()
	const goroutines = 100

	var wg sync.WaitGroup
	acquired := make(chan bool, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			acquired <- s.Acquire("coder1")
		}()
	}

	wg.Wait()
	close(acquired)

	successCount := 0
	for ok := range acquired {
		if ok {
			successCount++
		}
	}

	if successCount != 1 {
		t.Errorf("exactly 1 goroutine should acquire, got %d", successCount)
	}
}
