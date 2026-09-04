package memorypromotion

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type promotionStoreStub struct {
	batch           *domainmemory.ProfilePromotionBatch
	projection      []domainmemory.UserMemory
	projectionErr   error
	projectionReads int
	completed       []domainmemory.ProfileCandidate
	deferred        bool
	failed          bool
	failureText     string
}

func (s *promotionStoreStub) ClaimProfilePromotionBatch(context.Context, int, int, time.Duration, time.Time) (*domainmemory.ProfilePromotionBatch, error) {
	return s.batch, nil
}
func (s *promotionStoreStub) ListProfilePromotionProjection(context.Context, string, int) ([]domainmemory.UserMemory, error) {
	s.projectionReads++
	return s.projection, s.projectionErr
}
func (s *promotionStoreStub) CompleteProfilePromotionBatch(_ context.Context, _ domainmemory.ProfilePromotionBatch, candidates []domainmemory.ProfileCandidate, _ string, _ time.Time) (int, error) {
	s.completed = append([]domainmemory.ProfileCandidate(nil), candidates...)
	return len(candidates), nil
}
func (s *promotionStoreStub) DeferProfilePromotionBatch(context.Context, domainmemory.ProfilePromotionBatch, time.Time) error {
	s.deferred = true
	return nil
}
func (s *promotionStoreStub) FailProfilePromotionBatch(_ context.Context, _ domainmemory.ProfilePromotionBatch, _ int, _ time.Time, errorText string) error {
	s.failed = true
	s.failureText = errorText
	return nil
}

type observingPromotionExtractor struct {
	result   *domconv.ProfileExtractionResult
	existing domconv.UserProfile
	called   bool
}

func (s *observingPromotionExtractor) Extract(_ context.Context, _ *domconv.Thread, existing domconv.UserProfile) (*domconv.ProfileExtractionResult, error) {
	s.called = true
	s.existing = existing
	return s.result, nil
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
	threadID := modulecore.NewThreadID()
	threadSeq := modulecore.ThreadSeq(1)
	threadKind := modulecore.ThreadKindUserConversation
	return &domainmemory.ProfilePromotionBatch{
		LeaseToken: "lease", SessionID: "session", ThreadID: threadID, ThreadSeq: threadSeq, ThreadKind: threadKind,
		Messages: []domainmemory.ProfilePromotionMessage{{EventID: "evt-1", SessionID: "session", ThreadID: threadID, ThreadSeq: threadSeq, ThreadKind: threadKind, Text: "私はGoが好き"}},
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

func TestServiceRunOneMapsTypedExtractorFailuresToSafeCodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{
			name: "provider unavailable",
			err:  domconv.NewProfileExtractionUnavailableError(errors.New("provider payload TOP-SECRET")),
			want: "profile_extractor_unavailable",
		},
		{
			name: "invalid response",
			err:  domconv.NewProfileExtractionInvalidError(errors.New("raw response TOP-SECRET")),
			want: "profile_extractor_invalid",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &promotionStoreStub{batch: testPromotionBatch()}
			service := NewService(store, promotionExtractorStub{err: tc.err}, Options{})

			if _, err := service.RunOne(context.Background()); err == nil {
				t.Fatal("expected extractor failure")
			}
			if !store.failed || store.failureText != tc.want || strings.Contains(store.failureText, "TOP-SECRET") {
				t.Fatalf("failed=%v code=%q want=%q", store.failed, store.failureText, tc.want)
			}
		})
	}
}

func TestServiceRunOneTypedCancellationStillDefers(t *testing.T) {
	store := &promotionStoreStub{batch: testPromotionBatch()}
	service := NewService(store, promotionExtractorStub{
		err: domconv.NewProfileExtractionUnavailableError(context.Canceled),
	}, Options{})

	_, err := service.RunOne(context.Background())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v want cancellation", err)
	}
	if !store.deferred || store.failed {
		t.Fatalf("deferred=%v failed=%v", store.deferred, store.failed)
	}
}

func TestServiceRunOnePassesExistingProjectionToExtractor(t *testing.T) {
	store := &promotionStoreStub{
		batch: testPromotionBatch(),
		projection: []domainmemory.UserMemory{{
			UserID: "ren", Namespace: "user:ren", Type: domainmemory.UserMemoryTypeProfile,
			Statement: "Goを使う", State: domainmemory.MemoryStateConfirmed, Active: true,
			Confidence: 0.8, Sensitivity: "normal", Scope: "all_personas",
		}},
	}
	extractor := &observingPromotionExtractor{result: &domconv.ProfileExtractionResult{}}
	service := NewService(store, extractor, Options{UserID: "ren"})

	if _, err := service.RunOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(extractor.existing.Facts) != 1 || extractor.existing.Facts[0] != "Goを使う" {
		t.Fatalf("existing projection was not passed to extractor: %+v", extractor.existing)
	}
}

func TestServiceRunOneRejectsMoreThanSixteenRawCandidates(t *testing.T) {
	store := &promotionStoreStub{batch: testPromotionBatch()}
	facts := make([]string, 17)
	for i := range facts {
		facts[i] = fmt.Sprintf("fact-%d", i)
	}
	service := NewService(store, promotionExtractorStub{result: &domconv.ProfileExtractionResult{NewFacts: facts}}, Options{})

	if _, err := service.RunOne(context.Background()); err == nil {
		t.Fatal("expected more-than-sixteen raw candidates to fail")
	}
	if !store.failed || len(store.completed) != 0 {
		t.Fatalf("invalid candidate batch was not failed: failed=%v completed=%#v", store.failed, store.completed)
	}
}

func TestServiceRunOneFailsProjectionReadBeforeExtractor(t *testing.T) {
	store := &promotionStoreStub{batch: testPromotionBatch(), projectionErr: errors.New("projection unavailable")}
	extractor := &observingPromotionExtractor{result: &domconv.ProfileExtractionResult{}}
	service := NewService(store, extractor, Options{UserID: "ren"})

	if _, err := service.RunOne(context.Background()); err == nil {
		t.Fatal("expected projection read failure")
	}
	if !store.failed || extractor.existing.UserID != "" || len(extractor.existing.Facts) != 0 {
		t.Fatalf("projection failure crossed extractor boundary: failed=%v existing=%+v", store.failed, extractor.existing)
	}
}

func TestServiceRunOneRejectsInvalidEvidenceBeforeProjectionAndExtractor(t *testing.T) {
	batch := testPromotionBatch()
	batch.Messages[0].SessionID = "foreign-session"
	store := &promotionStoreStub{batch: batch}
	extractor := &observingPromotionExtractor{result: &domconv.ProfileExtractionResult{}}
	service := NewService(store, extractor, Options{UserID: "ren"})

	if _, err := service.RunOne(context.Background()); err == nil {
		t.Fatal("expected invalid evidence to fail")
	}
	if store.failureText != "profile_evidence_invalid" || store.projectionReads != 0 || extractor.called {
		t.Fatalf("invalid evidence crossed pre-LLM boundary: failure=%q projection_reads=%d extractor_called=%v", store.failureText, store.projectionReads, extractor.called)
	}
}

func TestServiceRunOneDoesNotPersistPrivateExtractorError(t *testing.T) {
	secret := "provider private payload TOP-SECRET"
	store := &promotionStoreStub{batch: testPromotionBatch()}
	service := NewService(store, promotionExtractorStub{err: errors.New(secret)}, Options{UserID: "ren"})

	if _, err := service.RunOne(context.Background()); err == nil {
		t.Fatal("expected extractor failure")
	}
	if store.failureText != "profile_extractor_failed" || strings.Contains(store.failureText, secret) {
		t.Fatalf("unsafe failure text=%q", store.failureText)
	}
}

func TestServiceRunOneDeduplicatesExistingAndWithinOutputStatements(t *testing.T) {
	store := &promotionStoreStub{
		batch: testPromotionBatch(),
		projection: []domainmemory.UserMemory{
			{UserID: "ren", Namespace: "user:ren", Type: domainmemory.UserMemoryTypePreference, Statement: "好み: Go", State: domainmemory.MemoryStateConfirmed, Active: true, Confidence: 0.8, Sensitivity: "normal", Scope: "all_personas"},
			{UserID: "ren", Namespace: "user:ren", Type: domainmemory.UserMemoryTypeProfile, Statement: "開発者", State: domainmemory.MemoryStateCandidate, Active: true, Confidence: 0.8, Sensitivity: "normal", Scope: "all_personas"},
		},
	}
	service := NewService(store, promotionExtractorStub{result: &domconv.ProfileExtractionResult{
		NewPreferences: map[string]string{"好み": " go "},
		NewFacts:       []string{"開発者", "  新しい 事実 ", "新しい 事実"},
	}}, Options{UserID: "ren"})

	result, err := service.RunOne(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.CandidateCount != 1 || len(store.completed) != 1 || store.completed[0].Statement != "新しい 事実" {
		t.Fatalf("deduplicated result=%+v candidates=%#v", result, store.completed)
	}
}

func TestServiceRunOneBoundsValidProjectionByTotalRunes(t *testing.T) {
	projection := make([]domainmemory.UserMemory, 32)
	for i := range projection {
		projection[i] = domainmemory.UserMemory{
			UserID: "ren", Namespace: "user:ren", Type: domainmemory.UserMemoryTypeProfile,
			Statement: strings.Repeat("x", domainmemory.ProfilePromotionProjectionStatementMax),
			State:     domainmemory.MemoryStateCandidate, Active: true, Confidence: 0.7,
			Sensitivity: "normal", Scope: "all_personas",
		}
	}
	store := &promotionStoreStub{batch: testPromotionBatch(), projection: projection}
	extractor := &observingPromotionExtractor{result: &domconv.ProfileExtractionResult{}}
	service := NewService(store, extractor, Options{UserID: "ren"})

	if _, err := service.RunOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(extractor.existing.Facts) != domainmemory.ProfilePromotionProjectionTotalMax/domainmemory.ProfilePromotionProjectionStatementMax {
		t.Fatalf("bounded facts=%d", len(extractor.existing.Facts))
	}
}
