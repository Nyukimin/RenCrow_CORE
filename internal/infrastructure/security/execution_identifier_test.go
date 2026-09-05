package security

import "testing"

// TestNextActionIDIsUnique verifies that policy-mediated Action identities do
// not collide. TaskID is supplied separately by the owner route.
func TestNextActionIDIsUnique(t *testing.T) {
	const count = 1000
	seen := make(map[string]int, count)
	for i := 0; i < count; i++ {
		actionID := nextActionID()
		if prev, dup := seen[actionID]; dup {
			t.Fatalf("duplicate identifier %q generated at iteration %d and %d", actionID, prev, i)
		}
		seen[actionID] = i
	}
}

func TestNextActionIDIsUniqueUnderConcurrency(t *testing.T) {
	const goroutines = 8
	const perGoroutine = 200

	results := make(chan string, goroutines*perGoroutine)
	done := make(chan struct{})
	for g := 0; g < goroutines; g++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for i := 0; i < perGoroutine; i++ {
				results <- nextActionID()
			}
		}()
	}
	for g := 0; g < goroutines; g++ {
		<-done
	}
	close(results)

	seen := make(map[string]struct{}, goroutines*perGoroutine)
	for key := range results {
		if _, dup := seen[key]; dup {
			t.Fatalf("duplicate identifier %q generated concurrently", key)
		}
		seen[key] = struct{}{}
	}
}
