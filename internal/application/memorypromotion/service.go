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
	ListProfilePromotionProjection(context.Context, string, int) ([]domainmemory.UserMemory, error)
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
	if evidenceErr := domainmemory.ValidateProfilePromotionBatchEvidence(*batch); evidenceErr != nil {
		failErr := s.failBatch(batch, "profile_evidence_invalid")
		return result, errors.Join(evidenceErr, failErr)
	}
	projection, projectionErr := s.store.ListProfilePromotionProjection(ctx, s.options.UserID, domainmemory.ProfilePromotionProjectionLimit)
	if projectionErr != nil {
		failErr := s.failBatch(batch, "profile_projection_failed")
		return result, errors.Join(projectionErr, failErr)
	}
	projection, projectionErr = boundProfilePromotionProjection(projection, s.options.UserID)
	if projectionErr != nil {
		failErr := s.failBatch(batch, "profile_projection_invalid")
		return result, errors.Join(projectionErr, failErr)
	}
	existing := profilePromotionUserProfile(projection, s.options.UserID)
	thread := &domconv.Thread{
		ID: batch.ThreadID, SessionID: batch.SessionID, Domain: "profile_promotion",
		Status: domconv.ThreadActive,
	}
	for _, item := range batch.Messages {
		thread.Turns = append(thread.Turns, domconv.Message{
			Speaker: domconv.SpeakerUser, Msg: item.Text, Timestamp: item.CreatedAt,
		})
	}
	extracted, extractErr := s.extractor.Extract(ctx, thread, existing)
	if extractErr != nil {
		if errors.Is(extractErr, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			deferErr := s.deferBatch(batch)
			if deferErr != nil {
				return result, errors.Join(extractErr, deferErr)
			}
			return result, extractErr
		}
		failureCode := "profile_extractor_failed"
		switch {
		case errors.Is(extractErr, domconv.ErrProfileExtractorUnavailable):
			failureCode = "profile_extractor_unavailable"
		case errors.Is(extractErr, domconv.ErrProfileExtractorInvalid):
			failureCode = "profile_extractor_invalid"
		}
		failErr := s.failBatch(batch, failureCode)
		if failErr != nil {
			return result, errors.Join(extractErr, failErr)
		}
		return result, extractErr
	}
	candidates, candidateErr := profileCandidates(extracted, projection)
	if candidateErr != nil {
		failErr := s.failBatch(batch, "profile_extractor_invalid")
		return result, errors.Join(candidateErr, failErr)
	}
	saved, err := s.store.CompleteProfilePromotionBatch(
		ctx, *batch, candidates, s.options.UserID, s.options.Now().UTC(),
	)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			deferErr := s.deferBatch(batch)
			return result, errors.Join(err, deferErr)
		}
		failErr := s.failBatch(batch, "profile_persistence_failed")
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

func (s *Service) failBatch(batch *domainmemory.ProfilePromotionBatch, failureCode string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.store.FailProfilePromotionBatch(
		ctx, *batch, s.options.MaxAttempts, s.options.Now().UTC(), failureCode,
	)
}

func boundProfilePromotionProjection(projection []domainmemory.UserMemory, userID string) ([]domainmemory.UserMemory, error) {
	if len(projection) > domainmemory.ProfilePromotionProjectionLimit {
		return nil, fmt.Errorf("profile promotion projection count exceeds %d", domainmemory.ProfilePromotionProjectionLimit)
	}
	validated := make([]domainmemory.UserMemory, 0, len(projection))
	for _, item := range projection {
		if err := domainmemory.ValidateProfilePromotionProjection([]domainmemory.UserMemory{item}, userID); err != nil {
			return nil, err
		}
		validated = append(validated, item)
	}
	bounded := make([]domainmemory.UserMemory, 0, len(validated))
	totalRunes := 0
	for _, item := range validated {
		if totalRunes+len([]rune(item.Statement)) > domainmemory.ProfilePromotionProjectionTotalMax {
			break
		}
		bounded = append(bounded, item)
		totalRunes += len([]rune(item.Statement))
	}
	return bounded, nil
}

func profilePromotionUserProfile(projection []domainmemory.UserMemory, userID string) domconv.UserProfile {
	profile := domconv.NewUserProfile(userID)
	for _, item := range projection {
		profile.Facts = append(profile.Facts, item.Statement)
	}
	return profile
}

func validateProfileExtractionText(value string, maxRunes int, field string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", field)
	}
	if strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("%s contains forbidden control characters", field)
	}
	if len([]rune(value)) > maxRunes {
		return fmt.Errorf("%s exceeds %d runes", field, maxRunes)
	}
	return nil
}

func profileCandidates(result *domconv.ProfileExtractionResult, existing []domainmemory.UserMemory) ([]domainmemory.ProfileCandidate, error) {
	if result == nil {
		return nil, nil
	}
	rawCount := len(result.NewPreferences) + len(result.NewFacts)
	if rawCount > domainmemory.ProfilePromotionRawCandidateLimit {
		return nil, fmt.Errorf("profile promotion raw candidate count exceeds %d", domainmemory.ProfilePromotionRawCandidateLimit)
	}
	seen := make(map[string]struct{}, len(existing)+rawCount)
	for _, item := range existing {
		seen[domainmemory.ProfilePromotionStatementKey(item.Type, item.Statement)] = struct{}{}
	}
	keys := make([]string, 0, len(result.NewPreferences))
	for key := range result.NewPreferences {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	candidates := make([]domainmemory.ProfileCandidate, 0, rawCount)
	for _, key := range keys {
		normalizedKey := domainmemory.NormalizeProfilePromotionStatement(key)
		if err := validateProfileExtractionText(key, domainmemory.ProfilePromotionPreferenceKeyMax, "profile preference key"); err != nil {
			return nil, err
		}
		if normalizedKey == "" {
			return nil, errors.New("profile preference key is required")
		}
		value := result.NewPreferences[key]
		if err := validateProfileExtractionText(value, domainmemory.ProfilePromotionPreferenceValueMax, "profile preference value"); err != nil {
			return nil, err
		}
		normalizedValue := domainmemory.NormalizeProfilePromotionStatement(value)
		statement := domainmemory.NormalizeProfilePromotionStatement(fmt.Sprintf("%s: %s", normalizedKey, normalizedValue))
		if err := validateProfileExtractionText(statement, domainmemory.ProfilePromotionProjectionStatementMax, "profile preference statement"); err != nil {
			return nil, err
		}
		dedupeKey := domainmemory.ProfilePromotionStatementKey(domainmemory.UserMemoryTypePreference, statement)
		if _, exists := seen[dedupeKey]; exists {
			continue
		}
		seen[dedupeKey] = struct{}{}
		candidates = append(candidates, domainmemory.ProfileCandidate{
			Type: domainmemory.UserMemoryTypePreference, Statement: statement, Confidence: 0.7,
			Sensitivity: "normal", Scope: "all_personas",
		})
	}
	for _, fact := range result.NewFacts {
		if err := validateProfileExtractionText(fact, domainmemory.ProfilePromotionProjectionStatementMax, "profile fact"); err != nil {
			return nil, err
		}
		statement := domainmemory.NormalizeProfilePromotionStatement(fact)
		key := domainmemory.ProfilePromotionStatementKey(domainmemory.UserMemoryTypeProfile, statement)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		candidates = append(candidates, domainmemory.ProfileCandidate{
			Type: domainmemory.UserMemoryTypeProfile, Statement: statement, Confidence: 0.7,
			Sensitivity: "normal", Scope: "all_personas",
		})
	}
	return candidates, nil
}
