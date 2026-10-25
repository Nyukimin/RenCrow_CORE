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

func TestCoderStatus_AcquireNextRoundRobinsAndSkipsBusySlots(t *testing.T) {
	s := NewCoderStatus()
	if !s.Acquire("coder1") {
		t.Fatal("fixture failed to occupy coder1")
	}

	if got, ok := s.AcquireNext([]string{"coder1", "coder2", "coder3", "coder4"}); !ok || got != "coder2" {
		t.Fatalf("AcquireNext() = %q, %t, want coder2", got, ok)
	}
	s.Release("coder2")
	s.Release("coder1")

	for _, want := range []string{"coder3", "coder4", "coder1"} {
		got, ok := s.AcquireNext([]string{"coder1", "coder2", "coder3", "coder4"})
		if !ok || got != want {
			t.Fatalf("AcquireNext() = %q, %t, want %s", got, ok, want)
		}
		s.Release(got)
	}
}

func TestCoderStatus_AcquireNextConcurrentDoesNotDoubleAcquire(t *testing.T) {
	s := NewCoderStatus()
	const goroutines = 64
	candidates := []string{"coder1", "coder2", "coder3", "coder4"}
	var wg sync.WaitGroup
	acquired := make(chan string, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if name, ok := s.AcquireNext(candidates); ok {
				acquired <- name
			}
		}()
	}
	wg.Wait()
	close(acquired)

	seen := make(map[string]bool)
	for name := range acquired {
		if seen[name] {
			t.Fatalf("%s was acquired more than once", name)
		}
		seen[name] = true
	}
	if len(seen) != len(candidates) {
		t.Fatalf("unique acquired coders = %v, want all four", seen)
	}
}
