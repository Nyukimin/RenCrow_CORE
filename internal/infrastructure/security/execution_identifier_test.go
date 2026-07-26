package security

import "testing"

// TestNextExecutionIdentifiersAreUnique は連続生成した job_id / action_id が
// 衝突しないことを確認する
//
// この組は internal/infrastructure/persistence/execution の actionKey()
// （jobID + "::" + actionID）で map キーになる。衝突するとポリシー実行の
// 監査記録が上書きされて失われる。クロック粒度が粗い環境（Windowsでは
// 100ns〜1ms程度）で時刻のみの識別子は確実に衝突する。
func TestNextExecutionIdentifiersAreUnique(t *testing.T) {
	const count = 1000
	seen := make(map[string]int, count)
	for i := 0; i < count; i++ {
		jobID, actionID := nextExecutionIdentifiers()
		key := jobID + "::" + actionID
		if prev, dup := seen[key]; dup {
			t.Fatalf("duplicate identifier %q generated at iteration %d and %d", key, prev, i)
		}
		seen[key] = i
	}
}

func TestNextExecutionIdentifiersAreUniqueUnderConcurrency(t *testing.T) {
	const goroutines = 8
	const perGoroutine = 200

	results := make(chan string, goroutines*perGoroutine)
	done := make(chan struct{})
	for g := 0; g < goroutines; g++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for i := 0; i < perGoroutine; i++ {
				jobID, actionID := nextExecutionIdentifiers()
				results <- jobID + "::" + actionID
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
