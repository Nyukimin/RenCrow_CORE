package memorypromotion

import (
	"context"
	"errors"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

type promotionStoreStub struct {
	batch     *domainmemory.ProfilePromotionBatch
	completed []domainmemory.ProfileCandidate
	deferred  bool
	failed    bool
}

func (s *promotionStoreStub) ClaimProfilePromotionBatch(context.Context, int, int, time.Duration, time.Time) (*domainmemory.ProfilePromotionBatch, error) {
	return s.batch, nil
}
func (s *promotionStoreStub) CompleteProfilePromotionBatch(_ context.Context, _ domainmemory.ProfilePromotionBatch, candidates []domainmemory.ProfileCandidate, _ string, _ time.Time) (int, error) {
	s.completed = append([]domainmemory.ProfileCandidate(nil), candidates...)
	return len(candidates), nil
}
func (s *promotionStoreStub) DeferProfilePromotionBatch(context.Context, domainmemory.ProfilePromotionBatch, time.Time) error {
	s.deferred = true
	return nil
}
func (s *promotionStoreStub) FailProfilePromotionBatch(context.Context, domainmemory.ProfilePromotionBatch, int, time.Time, string) error {
	s.failed = true
	return nil
}

type promotionExtractorStub struct {
	result *domconv.ProfileExtractionResult
	err    error
	cancel context.CancelFunc
}

func (s promotionExtractorStub) Extract(ctx context.Context, _ *domconv.Thread, _ domconv.UserProfile) (*domconv.ProfileExtractionResult, error) {
	if s.cancel != nil {
		s.cancel()
		<-ctx.Done()
	}
	return s.result, s.err
}

func testPromotionBatch() *domainmemory.ProfilePromotionBatch {
	return &domainmemory.ProfilePromotionBatch{
		LeaseToken: "lease", SessionID: "session", ThreadID: 1,
		Messages: []domainmemory.ProfilePromotionMessage{{EventID: "evt-1", Text: "私はGoが好き"}},
	}
}

func TestServiceRunOneCompletesCandidateBatch(t *testing.T) {
	store := &promotionStoreStub{batch: testPromotionBatch()}
	service := NewService(store, promotionExtractorStub{result: &domconv.ProfileExtractionResult{
		NewPreferences: map[string]string{"language": "Go"},
		NewFacts:       []string{"開発者"},
	}}, Options{UserID: "ren"})

	result, err := service.RunOne(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Processed || result.CandidateCount != 2 || len(store.completed) != 2 {
		t.Fatalf("result=%+v candidates=%#v", result, store.completed)
	}
	if store.completed[0].Type != domainmemory.UserMemoryTypePreference ||
		store.completed[1].Type != domainmemory.UserMemoryTypeProfile {
		t.Fatalf("candidates=%#v", store.completed)
	}
}

func TestServiceRunOneCancellationDefersWithoutFailure(t *testing.T) {
	store := &promotionStoreStub{batch: testPromotionBatch()}
	ctx, cancel := context.WithCancel(context.Background())
	service := NewService(store, promotionExtractorStub{cancel: cancel, err: context.Canceled}, Options{})

	_, err := service.RunOne(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if !store.deferred || store.failed {
		t.Fatalf("deferred=%v failed=%v", store.deferred, store.failed)
	}
}

func TestServiceRunOneFailureConsumesAttempt(t *testing.T) {
	store := &promotionStoreStub{batch: testPromotionBatch()}
	service := NewService(store, promotionExtractorStub{err: errors.New("bad json")}, Options{})

	if _, err := service.RunOne(context.Background()); err == nil {
		t.Fatal("expected extraction error")
	}
	if !store.failed || store.deferred {
		t.Fatalf("deferred=%v failed=%v", store.deferred, store.failed)
	}
}
