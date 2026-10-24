package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/glossary/domain/entity"
	"github.com/Nyukimin/RenCrow_CORE/internal/glossary/domain/repository"
)

const (
	maxRecentPromptItems      = 16
	maxRecentPromptItemBytes  = 1024
	maxRecentPromptBytes      = 8192
	recentPromptHeader        = "最近語彙メモ:\n"
	maxChatGlossaryQueryRunes = 512
	maxChatGlossaryResults    = 8
)

type GlossaryChatLookupStatus string

const (
	GlossaryChatLookupEmpty       GlossaryChatLookupStatus = "empty"
	GlossaryChatLookupFound       GlossaryChatLookupStatus = "found"
	GlossaryChatLookupNotFound    GlossaryChatLookupStatus = "not_found"
	GlossaryChatLookupUnavailable GlossaryChatLookupStatus = "unavailable"
	GlossaryChatLookupInvalid     GlossaryChatLookupStatus = "invalid_query"
)

// GlossaryChatRecord is the bounded owner projection eligible for a Chat
// detail read. Reference, Source, and UpdatedAt retain the glossary owner's
// provenance alongside the definition text.
type GlossaryChatRecord struct {
	Reference   string    `json:"reference"`
	Term        string    `json:"term"`
	Explanation string    `json:"explanation"`
	Source      string    `json:"source"`
	Category    string    `json:"category"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// GlossaryChatLookup carries an exact owner lookup outcome. Qualification is
// present only when a positive record came from a local owner with mutation
// invalidation hooks.
type GlossaryChatLookup struct {
	Status        GlossaryChatLookupStatus   `json:"status"`
	Records       []GlossaryChatRecord       `json:"records"`
	Qualification *GlossaryChatQualification `json:"-"`
}

// GlossaryChatQualification is an opaque, process-local admission fence.
// Callers may only use it to serialize a short bounded RAM admission against
// glossary owner mutations; the callback must not perform I/O or block.
type GlossaryChatQualification struct {
	service    *GlossaryService
	generation uint64
}

var ErrGlossaryChatQualificationStale = errors.New("glossary chat qualification is stale")

// Use runs a short RAM-only admission while glossary mutations are excluded.
// It performs no repository access. A mutation invalidates this qualification
// before the owner write can produce its success receipt.
func (q *GlossaryChatQualification) Use(admit func() error) error {
	if q == nil || q.service == nil || admit == nil {
		return ErrGlossaryChatQualificationStale
	}
	s := q.service
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	if q.generation != s.snapshotGeneration || s.mutationsInFlight != 0 {
		return ErrGlossaryChatQualificationStale
	}
	return admit()
}

type glossaryMutationHookRepository interface {
	SetGlossaryMutationHooks(before func(), after func(context.Context))
}

type recentPromptItem struct {
	contextLine string
	topic       string
}

type recentPromptSnapshot struct {
	items []recentPromptItem
}

type GlossaryService struct {
	repo repository.GlossaryRepository

	snapshotMu         sync.RWMutex
	snapshotGeneration uint64
	mutationsInFlight  uint64
	promptSnapshot     *recentPromptSnapshot
}

func NewGlossaryService(repo repository.GlossaryRepository) *GlossaryService {
	s := &GlossaryService{repo: repo}
	if owner, ok := repo.(glossaryMutationHookRepository); ok && !isNilGlossaryRepository(repo) {
		owner.SetGlossaryMutationHooks(s.invalidateRecentPromptSnapshot, s.refreshAfterMutation)
	}
	return s
}

func isNilGlossaryRepository(repo repository.GlossaryRepository) bool {
	if repo == nil {
		return true
	}
	value := reflect.ValueOf(repo)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (s *GlossaryService) AddGlossaryItem(ctx context.Context, term, explanation, source, category string) (*entity.GlossaryItem, error) {
	item := entity.NewGlossaryItem(term, explanation, source, category)
	if err := s.repo.Save(ctx, item); err != nil {
		return nil, err
	}
	return item, nil
}

// RefreshRecentPromptSnapshot rebuilds the prompt-only projection from the
// canonical repository. A refresh that overlaps a newer mutation is discarded.
func (s *GlossaryService) RefreshRecentPromptSnapshot(ctx context.Context) error {
	if s == nil || s.repo == nil {
		return errors.New("glossary service is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.snapshotMu.RLock()
	generation := s.snapshotGeneration
	s.snapshotMu.RUnlock()

	items, err := s.repo.FindRecent(ctx, maxRecentPromptItems)
	if err != nil {
		s.invalidateIfGeneration(generation)
		return err
	}
	snapshot := buildRecentPromptSnapshot(items)

	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	if generation != s.snapshotGeneration || s.mutationsInFlight != 0 {
		return nil
	}
	s.promptSnapshot = snapshot
	return nil
}

// RecentPromptSnapshot returns an immutable-by-copy view prepared outside the
// prompt path. A missing snapshot is represented as empty context and topics.
func (s *GlossaryService) RecentPromptSnapshot(limit int) (string, []string) {
	if s == nil || limit <= 0 {
		return "", []string{}
	}
	s.snapshotMu.RLock()
	snapshot := s.promptSnapshot
	s.snapshotMu.RUnlock()
	if snapshot == nil || len(snapshot.items) == 0 {
		return "", []string{}
	}
	if limit > len(snapshot.items) {
		limit = len(snapshot.items)
	}
	contextText := recentPromptHeader
	topics := make([]string, 0, limit)
	for _, item := range snapshot.items[:limit] {
		if len(contextText) > len(recentPromptHeader) {
			contextText += "\n"
		}
		contextText += item.contextLine
		topics = append(topics, item.topic)
	}
	return contextText, topics
}

func (s *GlossaryService) invalidateRecentPromptSnapshot() {
	if s == nil {
		return
	}
	s.snapshotMu.Lock()
	s.snapshotGeneration++
	s.mutationsInFlight++
	s.promptSnapshot = nil
	s.snapshotMu.Unlock()
}

func (s *GlossaryService) refreshAfterMutation(ctx context.Context) {
	if s == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.snapshotMu.Lock()
	if s.mutationsInFlight == 0 {
		s.snapshotMu.Unlock()
		return
	}
	s.mutationsInFlight--
	// Any refresh that began before this mutation finished is stale, even when
	// another mutation kept the snapshot revoked in the meantime.
	s.snapshotGeneration++
	refresh := s.mutationsInFlight == 0
	s.snapshotMu.Unlock()
	if !refresh {
		return
	}
	_ = s.RefreshRecentPromptSnapshot(ctx)
}

func (s *GlossaryService) invalidateIfGeneration(generation uint64) {
	s.snapshotMu.Lock()
	if generation == s.snapshotGeneration {
		s.snapshotGeneration++
		s.promptSnapshot = nil
	}
	s.snapshotMu.Unlock()
}

func buildRecentPromptSnapshot(items []*entity.GlossaryItem) *recentPromptSnapshot {
	snapshot := &recentPromptSnapshot{items: make([]recentPromptItem, 0, maxRecentPromptItems)}
	if len(items) > maxRecentPromptItems {
		items = items[:maxRecentPromptItems]
	}
	usedBytes := len(recentPromptHeader)
	for _, item := range items {
		if item == nil {
			continue
		}
		term := normalizePromptField(item.Term, 128)
		if term == "" {
			continue
		}
		explanation := normalizePromptField(item.Explanation, maxRecentPromptItemBytes)
		source := normalizePromptField(item.Source, 128)
		topicPrefix := term + ": "
		contextPrefix := "- " + topicPrefix
		sourceSuffix := ""
		if source != "" {
			sourceSuffix = " (" + source + ")"
		}
		availableExplanationBytes := maxRecentPromptItemBytes - len(contextPrefix) - len(sourceSuffix)
		if availableExplanationBytes < 0 {
			sourceSuffix = ""
			availableExplanationBytes = maxRecentPromptItemBytes - len(contextPrefix)
		}
		explanation = truncateUTF8Bytes(explanation, availableExplanationBytes)
		topic := topicPrefix + explanation
		contextLine := contextPrefix + explanation + sourceSuffix
		if len(contextLine) > maxRecentPromptItemBytes {
			contextLine = truncateUTF8Bytes(contextLine, maxRecentPromptItemBytes)
		}
		if len(snapshot.items) == maxRecentPromptItems {
			break
		}
		separatorBytes := 0
		if len(snapshot.items) > 0 {
			separatorBytes = 1
		}
		if usedBytes+separatorBytes+len(contextLine) > maxRecentPromptBytes {
			break
		}
		usedBytes += separatorBytes + len(contextLine)
		snapshot.items = append(snapshot.items, recentPromptItem{contextLine: contextLine, topic: topic})
	}
	return snapshot
}

func normalizePromptField(value string, byteLimit int) string {
	value = strings.ToValidUTF8(value, "�")
	value = strings.Join(strings.Fields(value), " ")
	return truncateUTF8Bytes(value, byteLimit)
}

func truncateUTF8Bytes(value string, byteLimit int) string {
	if byteLimit <= 0 {
		return ""
	}
	if len(value) <= byteLimit {
		return value
	}
	value = value[:byteLimit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return strings.TrimSpace(value)
}

func (s *GlossaryService) GetRecentGlossary(ctx context.Context, limit int) ([]*entity.GlossaryItem, error) {
	return s.repo.FindRecent(ctx, limit)
}

func (s *GlossaryService) SearchByTerm(ctx context.Context, term string) (*entity.GlossaryItem, error) {
	return s.repo.FindByTerm(ctx, term)
}

// LookupForChat performs one exact glossary-owner read for a bounded query.
// Repository I/O runs without the service mutex. A positive result is returned
// only when a local mutation-hook owner can qualify it against the current
// generation; hookless repositories fail closed for positive records.
func (s *GlossaryService) LookupForChat(ctx context.Context, query string) GlossaryChatLookup {
	if s == nil || isNilGlossaryRepository(s.repo) {
		return GlossaryChatLookup{Status: GlossaryChatLookupUnavailable, Records: []GlossaryChatRecord{}}
	}
	if !utf8.ValidString(query) {
		return GlossaryChatLookup{Status: GlossaryChatLookupInvalid, Records: []GlossaryChatRecord{}}
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return GlossaryChatLookup{Status: GlossaryChatLookupEmpty, Records: []GlossaryChatRecord{}}
	}
	if utf8.RuneCountInString(query) > maxChatGlossaryQueryRunes {
		return GlossaryChatLookup{Status: GlossaryChatLookupInvalid, Records: []GlossaryChatRecord{}}
	}
	if ctx == nil {
		ctx = context.Background()
	}

	_, hasMutationHooks := s.repo.(glossaryMutationHookRepository)
	var generation uint64
	if hasMutationHooks {
		s.snapshotMu.RLock()
		generation = s.snapshotGeneration
		mutating := s.mutationsInFlight != 0
		s.snapshotMu.RUnlock()
		if mutating {
			return GlossaryChatLookup{Status: GlossaryChatLookupUnavailable, Records: []GlossaryChatRecord{}}
		}
	}

	// FindByTerm is the glossary owner's exact lookup. Keep the database call
	// outside snapshotMu so a slow read cannot block mutation invalidation.
	item, err := s.repo.FindByTerm(ctx, query)
	if err != nil {
		return GlossaryChatLookup{Status: GlossaryChatLookupUnavailable, Records: []GlossaryChatRecord{}}
	}

	if item == nil {
		if hasMutationHooks && !s.glossaryGenerationCurrent(generation) {
			return GlossaryChatLookup{Status: GlossaryChatLookupUnavailable, Records: []GlossaryChatRecord{}}
		}
		return GlossaryChatLookup{Status: GlossaryChatLookupNotFound, Records: []GlossaryChatRecord{}}
	}
	if !hasMutationHooks {
		return GlossaryChatLookup{Status: GlossaryChatLookupUnavailable, Records: []GlossaryChatRecord{}}
	}
	if item.ID == "" || item.Term != query || strings.TrimSpace(item.Explanation) == "" || strings.TrimSpace(item.Source) == "" {
		return GlossaryChatLookup{Status: GlossaryChatLookupUnavailable, Records: []GlossaryChatRecord{}}
	}

	record := GlossaryChatRecord{
		Reference:   item.ID,
		Term:        item.Term,
		Explanation: item.Explanation,
		Source:      item.Source,
		Category:    item.Category,
		UpdatedAt:   item.UpdatedAt,
	}
	if !s.glossaryGenerationCurrent(generation) {
		return GlossaryChatLookup{Status: GlossaryChatLookupUnavailable, Records: []GlossaryChatRecord{}}
	}
	records := []GlossaryChatRecord{record}
	if len(records) > maxChatGlossaryResults {
		records = records[:maxChatGlossaryResults]
	}
	return GlossaryChatLookup{
		Status:  GlossaryChatLookupFound,
		Records: records,
		Qualification: &GlossaryChatQualification{
			service:    s,
			generation: generation,
		},
	}
}

func (s *GlossaryService) glossaryGenerationCurrent(generation uint64) bool {
	s.snapshotMu.RLock()
	defer s.snapshotMu.RUnlock()
	return generation == s.snapshotGeneration && s.mutationsInFlight == 0
}

func (s *GlossaryService) GetByCategory(ctx context.Context, category string, limit int) ([]*entity.GlossaryItem, error) {
	return s.repo.FindByCategory(ctx, category, limit)
}
