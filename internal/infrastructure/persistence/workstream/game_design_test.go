package workstream

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
)

func TestEnsureGameProjectDesignPreservesProgressOnReplay(t *testing.T) {
	store := taskPlanTestStore(t, filepath.Join(t.TempDir(), "design.db"))
	scope := taskPlanTestUserScope(t, "design")
	ctx := tool.WithToolExecutionScope(context.Background(), scope)
	input := domainworkstream.GameProjectSubmission{Name: "Existing game", IdempotencyKey: "old-seven"}
	for _, key := range domainworkstream.GameProjectStageKeys() {
		if key != "design" {
			input.Stages = append(input.Stages, domainworkstream.GameProjectStage{Key: key, Title: key, SuccessCriteria: []string{"done"}, Verification: []string{"check"}})
		}
	}
	registered, err := store.SubmitGameProjectPlan(ctx, scope, input)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.EnsureGameProjectDesign(ctx, scope, registered.Registration.WorkstreamID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Replayed || first.Goal.Title != "デザイン" || first.Goal.Status != "draft" {
		t.Fatalf("first=%+v", first)
	}
	var group sync.WaitGroup
	for i := 0; i < 2; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			result, err := store.EnsureGameProjectDesign(ctx, scope, registered.Registration.WorkstreamID)
			if err != nil || result.Goal.GoalID != first.Goal.GoalID || !result.Replayed {
				t.Errorf("concurrent replay=%+v err=%v", result, err)
			}
		}()
	}
	group.Wait()
	progress := first.Goal
	progress.Status = domainworkstream.StatusCompleted
	progress.CompletedAt = time.Now().UTC()
	if err := store.SaveGoal(ctx, progress); err != nil {
		t.Fatal(err)
	}
	second, err := store.EnsureGameProjectDesign(ctx, scope, registered.Registration.WorkstreamID)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Replayed || second.Goal.GoalID != progress.GoalID || second.Goal.Status != domainworkstream.StatusCompleted || second.Goal.CompletedAt.IsZero() {
		t.Fatalf("progress regressed: %+v", second)
	}
	goals, err := store.ListGoals(ctx, 100)
	if err != nil || len(goals) != 8 {
		t.Fatalf("goals=%d err=%v", len(goals), err)
	}
	receipt, found, err := store.GameProjectPlanReceipt(ctx, scope, input.IdempotencyKey)
	if err != nil || !found || len(receipt.Registration.GoalIDs) != 7 {
		t.Fatalf("original receipt changed: %+v %v", receipt, err)
	}
	other := scope
	other.ActorID = "other"
	other.AuthenticatedUserID = "other"
	_, err = store.EnsureGameProjectDesign(tool.WithToolExecutionScope(context.Background(), other), other, registered.Registration.WorkstreamID)
	if !errors.Is(err, domainworkstream.ErrTaskPlanForbidden) {
		t.Fatalf("other user: %v", err)
	}
}
