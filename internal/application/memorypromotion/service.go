package memorypromotion

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

type Store interface {
	ClaimProfilePromotionBatch(context.Context, int, int, time.Duration, time.Time) (*domainmemory.ProfilePromotionBatch, error)
	CompleteProfilePromotionBatch(context.Context, domainmemory.ProfilePromotionBatch, []domainmemory.ProfileCandidate, string, time.Time) (int, error)
	DeferProfilePromotionBatch(context.Context, domainmemory.ProfilePromotionBatch, time.Time) error
	FailProfilePromotionBatch(context.Context, domainmemory.ProfilePromotionBatch, int, time.Time, string) error
}

type Options struct {
	UserID        string
	BatchMessages int
	MaxAttempts   int
	LeaseDuration time.Duration
	Now           func() time.Time
}

type RunResult struct {
	Processed      bool
	MessageCount   int
	CandidateCount int
}

type Service struct {
	store     Store
	extractor domconv.ProfileExtractor
	options   Options
}

func NewService(store Store, extractor domconv.ProfileExtractor, options Options) *Service {
	if options.BatchMessages <= 0 {
		options.BatchMessages = 24
	}
	if options.MaxAttempts <= 0 {
		options.MaxAttempts = 5
	}
	if options.LeaseDuration <= 0 {
		options.LeaseDuration = time.Minute
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if strings.TrimSpace(options.UserID) == "" {
		options.UserID = "ren"
	}
	return &Service{store: store, extractor: extractor, options: options}
}

func (s *Service) RunOne(ctx context.Context) (RunResult, error) {
	if s == nil || s.store == nil || s.extractor == nil {
		return RunResult{}, errors.New("profile promotion service is unavailable")
	}
	batch, err := s.store.ClaimProfilePromotionBatch(
		ctx,
		s.options.BatchMessages,
		s.options.MaxAttempts,
		s.options.LeaseDuration,
		s.options.Now().UTC(),
	)
	if err != nil || batch == nil {
		return RunResult{}, err
	}
	result := RunResult{Processed: true, MessageCount: len(batch.Messages)}
	thread := &domconv.Thread{
		ID: batch.ThreadID, SessionID: batch.SessionID, Domain: "profile_promotion",
		Status: domconv.ThreadActive,
	}
	for _, item := range batch.Messages {
		thread.Turns = append(thread.Turns, domconv.Message{
			Speaker: domconv.SpeakerUser, Msg: item.Text, Timestamp: item.CreatedAt,
		})
	}
	extracted, extractErr := s.extractor.Extract(ctx, thread, domconv.NewUserProfile(s.options.UserID))
	if extractErr != nil {
		if errors.Is(extractErr, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			deferErr := s.deferBatch(batch)
			if deferErr != nil {
				return result, errors.Join(extractErr, deferErr)
			}
			return result, extractErr
		}
		failErr := s.failBatch(batch, extractErr)
		if failErr != nil {
			return result, errors.Join(extractErr, failErr)
		}
		return result, extractErr
	}
	candidates := profileCandidates(extracted)
	saved, err := s.store.CompleteProfilePromotionBatch(
		ctx, *batch, candidates, s.options.UserID, s.options.Now().UTC(),
	)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			deferErr := s.deferBatch(batch)
			return result, errors.Join(err, deferErr)
		}
		failErr := s.failBatch(batch, err)
		return result, errors.Join(err, failErr)
	}
	result.CandidateCount = saved
	return result, nil
}

func (s *Service) deferBatch(batch *domainmemory.ProfilePromotionBatch) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.store.DeferProfilePromotionBatch(ctx, *batch, s.options.Now().UTC())
}

func (s *Service) failBatch(batch *domainmemory.ProfilePromotionBatch, cause error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.store.FailProfilePromotionBatch(
		ctx, *batch, s.options.MaxAttempts, s.options.Now().UTC(), cause.Error(),
	)
}

func profileCandidates(result *domconv.ProfileExtractionResult) []domainmemory.ProfileCandidate {
	if result == nil {
		return nil
	}
	keys := make([]string, 0, len(result.NewPreferences))
	for key := range result.NewPreferences {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	candidates := make([]domainmemory.ProfileCandidate, 0, len(keys)+len(result.NewFacts))
	for _, key := range keys {
		value := strings.TrimSpace(result.NewPreferences[key])
		if value == "" {
			continue
		}
		statement := value
		if strings.TrimSpace(key) != "" {
			statement = fmt.Sprintf("%s: %s", strings.TrimSpace(key), value)
		}
		candidates = append(candidates, domainmemory.ProfileCandidate{
			Type: domainmemory.UserMemoryTypePreference, Statement: statement, Confidence: 0.7,
		})
	}
	for _, fact := range result.NewFacts {
		if fact = strings.TrimSpace(fact); fact != "" {
			candidates = append(candidates, domainmemory.ProfileCandidate{
				Type: domainmemory.UserMemoryTypeProfile, Statement: fact, Confidence: 0.7,
			})
		}
	}
	return candidates
}
