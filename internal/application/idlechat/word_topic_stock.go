package idlechat

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
	"github.com/google/uuid"
)

const wordTopicStockCapacityPerCategory = 12

var wordTopicStockCategories = []TopicCategory{TopicCategorySingle, TopicCategoryDouble}

// WordPreparedTopic is one validated topic waiting for episode preparation.
type WordPreparedTopic struct {
	Category           TopicCategory     `json:"category"`
	Topic              string            `json:"topic"`
	Seed               TopicSeed         `json:"seed"`
	Axis               string            `json:"interestingness_axis"`
	OpeningHook        string            `json:"opening_hook,omitempty"`
	Avoid              string            `json:"avoid,omitempty"`
	Judge              *TopicJudgeResult `json:"judge,omitempty"`
	ContentMode        string            `json:"content_mode"`
	ContentModeReasons []string          `json:"content_mode_reasons,omitempty"`
	GenerationID       string            `json:"generation_id"`
	InitiatedBy        string            `json:"initiated_by"`
	Created            time.Time         `json:"created"`
}

type WordTopicStockSnapshot struct {
	Enabled       bool                             `json:"enabled"`
	Total         int                              `json:"total"`
	Capacity      int                              `json:"capacity"`
	Missing       int                              `json:"missing"`
	Filling       bool                             `json:"filling"`
	LastTrigger   string                           `json:"last_trigger,omitempty"`
	LastAttemptAt *time.Time                       `json:"last_attempt_at,omitempty"`
	LastSuccessAt *time.Time                       `json:"last_success_at,omitempty"`
	LastError     string                           `json:"last_error,omitempty"`
	Categories    []WordTopicStockCategorySnapshot `json:"categories"`
}

type WordTopicStockCategorySnapshot struct {
	Name     TopicCategory       `json:"name"`
	Label    string              `json:"label"`
	Count    int                 `json:"count"`
	Capacity int                 `json:"capacity"`
	Filling  bool                `json:"filling"`
	Topics   []WordPreparedTopic `json:"topics"`
}

type wordTopicStockFile struct {
	Stock map[string][]WordPreparedTopic `json:"stock"`
}

type wordTopicStock struct {
	mu            sync.Mutex
	stock         map[TopicCategory][]WordPreparedTopic
	filling       map[TopicCategory]bool
	path          string
	lastTrigger   string
	lastAttemptAt time.Time
	lastSuccessAt time.Time
	lastError     string
}

func newWordTopicStock(path string) *wordTopicStock {
	stock := &wordTopicStock{
		stock:   make(map[TopicCategory][]WordPreparedTopic),
		filling: make(map[TopicCategory]bool),
		path:    strings.TrimSpace(path),
	}
	stock.load()
	return stock
}

func wordTopicCategoryLabel(category TopicCategory) string {
	switch category {
	case TopicCategorySingle:
		return "1ワード"
	case TopicCategoryDouble:
		return "2ワード"
	default:
		return string(category)
	}
}

func isWordTopicCategory(category TopicCategory) bool {
	return category == TopicCategorySingle || category == TopicCategoryDouble
}

func normalizeWordPreparedTopic(item WordPreparedTopic) (WordPreparedTopic, error) {
	category, err := modulechat.NormalizeTopicCategory(string(item.Category))
	if err != nil || !isWordTopicCategory(category) {
		return WordPreparedTopic{}, fmt.Errorf("unsupported word topic category %q", item.Category)
	}
	item.Category = category
	item.Seed.Category = category
	item.Topic = strings.TrimSpace(item.Topic)
	if err := modulechat.ValidateSeedForCategory(category, item.Seed); err != nil {
		return WordPreparedTopic{}, err
	}
	if strings.TrimSpace(item.Axis) == "" {
		item.Axis = modulechat.ExpectedAxisByCategory[category]
	}
	candidate := TopicCandidate{
		Topic:               item.Topic,
		InterestingnessAxis: item.Axis,
		OpeningHook:         item.OpeningHook,
		Avoid:               item.Avoid,
	}
	if err := modulechat.ValidateTopicCandidate(category, item.Seed, candidate); err != nil {
		return WordPreparedTopic{}, err
	}
	policy := ClassifyDialogueContentPolicy(TopicGenerationResult{
		Topic:    item.Topic,
		Category: category,
		Seed:     item.Seed,
	})
	if strings.TrimSpace(item.ContentMode) == "" {
		item.ContentMode = string(policy.Mode)
		item.ContentModeReasons = append([]string(nil), policy.Reasons...)
	}
	if strings.TrimSpace(item.GenerationID) == "" {
		item.GenerationID = uuid.NewString()
	}
	if strings.TrimSpace(item.InitiatedBy) == "" {
		item.InitiatedBy = "shiro"
	}
	if item.Created.IsZero() {
		item.Created = time.Now().UTC()
	} else {
		item.Created = item.Created.UTC()
	}
	return item, nil
}

func (s *wordTopicStock) load() {
	if s == nil || s.path == "" {
		return
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.lastError = fmt.Sprintf("stock_read_failed: %v", err)
		}
		return
	}
	var file wordTopicStockFile
	if err := json.Unmarshal(data, &file); err != nil {
		s.lastError = fmt.Sprintf("stock_parse_failed: %v", err)
		return
	}
	for _, category := range wordTopicStockCategories {
		for _, raw := range file.Stock[string(category)] {
			item, err := normalizeWordPreparedTopic(raw)
			if err != nil || item.Category != category || s.duplicateLocked(item) {
				continue
			}
			if len(s.stock[category]) >= wordTopicStockCapacityPerCategory {
				continue
			}
			s.stock[category] = append(s.stock[category], item)
		}
	}
}

func (s *wordTopicStock) saveLocked() {
	if s == nil || s.path == "" {
		return
	}
	file := wordTopicStockFile{Stock: make(map[string][]WordPreparedTopic, len(wordTopicStockCategories))}
	for _, category := range wordTopicStockCategories {
		file.Stock[string(category)] = append([]WordPreparedTopic(nil), s.stock[category]...)
	}
	data, err := json.Marshal(file)
	if err != nil {
		s.lastError = fmt.Sprintf("stock_marshal_failed: %v", err)
		return
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.lastError = fmt.Sprintf("stock_directory_failed: %v", err)
		return
	}
	tmp, err := os.CreateTemp(dir, ".word_topic_stock-*")
	if err != nil {
		s.lastError = fmt.Sprintf("stock_temp_failed: %v", err)
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(data)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmpPath, s.path)
	}
	if err != nil {
		s.lastError = fmt.Sprintf("stock_write_failed: %v", err)
		return
	}
	if strings.HasPrefix(s.lastError, "stock_") {
		s.lastError = ""
	}
}

func (s *wordTopicStock) duplicateLocked(item WordPreparedTopic) bool {
	for _, category := range wordTopicStockCategories {
		for _, existing := range s.stock[category] {
			if modulechat.CheckRecentTopicSimilarity(item.Topic, []RecentTopic{{Topic: existing.Topic}}, modulechat.RecentTopicSimilarityThreshold) != nil {
				return true
			}
			if item.Category == TopicCategoryDouble && existing.Category == TopicCategoryDouble &&
				modulechat.CanonicalDoubleSeedKey(item.Seed) == modulechat.CanonicalDoubleSeedKey(existing.Seed) {
				return true
			}
		}
	}
	return false
}

func (s *wordTopicStock) push(raw WordPreparedTopic) bool {
	if s == nil {
		return false
	}
	item, err := normalizeWordPreparedTopic(raw)
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.stock[item.Category]) >= wordTopicStockCapacityPerCategory || s.duplicateLocked(item) {
		return false
	}
	s.stock[item.Category] = append(s.stock[item.Category], item)
	s.saveLocked()
	return true
}

func (s *wordTopicStock) pop(category TopicCategory) *WordPreparedTopic {
	if s == nil || !isWordTopicCategory(category) {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.stock[category]
	if len(items) == 0 {
		return nil
	}
	item := items[0]
	s.stock[category] = append([]WordPreparedTopic(nil), items[1:]...)
	s.saveLocked()
	return &item
}

func (s *wordTopicStock) takeByGenerationID(generationID string) *WordPreparedTopic {
	if s == nil || strings.TrimSpace(generationID) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, category := range wordTopicStockCategories {
		items := s.stock[category]
		for index, item := range items {
			if item.GenerationID != generationID {
				continue
			}
			s.stock[category] = append(append([]WordPreparedTopic(nil), items[:index]...), items[index+1:]...)
			s.saveLocked()
			return &item
		}
	}
	return nil
}

func (s *wordTopicStock) hasGenerationID(generationID string) bool {
	if s == nil || strings.TrimSpace(generationID) == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, category := range wordTopicStockCategories {
		for _, item := range s.stock[category] {
			if item.GenerationID == generationID {
				return true
			}
		}
	}
	return false
}

func (s *wordTopicStock) count(category TopicCategory) int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.stock[category])
}

func (s *wordTopicStock) total() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.totalLocked()
}

func (s *wordTopicStock) totalLocked() int {
	total := 0
	for _, category := range wordTopicStockCategories {
		total += len(s.stock[category])
	}
	return total
}

func (s *wordTopicStock) topics() []RecentTopic {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var topics []RecentTopic
	for _, category := range wordTopicStockCategories {
		for _, item := range s.stock[category] {
			topics = append(topics, RecentTopic{Topic: item.Topic, Category: item.Category, Strategy: string(item.Category)})
		}
	}
	return topics
}

func (s *wordTopicStock) reserve(category TopicCategory, target int, trigger string) bool {
	if s == nil || !isWordTopicCategory(category) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if target < 1 {
		target = wordTopicStockCapacityPerCategory
	}
	if s.anyFillingLocked() || len(s.stock[category]) >= target {
		return false
	}
	s.filling[category] = true
	s.lastTrigger = strings.TrimSpace(trigger)
	s.lastAttemptAt = time.Now().UTC()
	return true
}

func (s *wordTopicStock) reserveNext(target int, trigger string) (TopicCategory, bool) {
	if s == nil {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if target < 1 {
		target = wordTopicStockCapacityPerCategory
	}
	if s.anyFillingLocked() {
		return "", false
	}
	selected := TopicCategory("")
	best := target
	for _, category := range wordTopicStockCategories {
		if count := len(s.stock[category]); count < best {
			selected = category
			best = count
		}
	}
	if selected == "" {
		return "", false
	}
	s.filling[selected] = true
	s.lastTrigger = strings.TrimSpace(trigger)
	s.lastAttemptAt = time.Now().UTC()
	return selected, true
}

func (s *wordTopicStock) done(category TopicCategory, err error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.filling[category] = false
	if err != nil {
		s.lastError = strings.TrimSpace(err.Error())
		return
	}
	s.lastSuccessAt = time.Now().UTC()
	if !strings.HasPrefix(s.lastError, "stock_") {
		s.lastError = ""
	}
}

func (s *wordTopicStock) anyFilling() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.anyFillingLocked()
}

func (s *wordTopicStock) anyFillingLocked() bool {
	for _, filling := range s.filling {
		if filling {
			return true
		}
	}
	return false
}

func (s *wordTopicStock) snapshot() WordTopicStockSnapshot {
	capacity := len(wordTopicStockCategories) * wordTopicStockCapacityPerCategory
	if s == nil {
		return WordTopicStockSnapshot{Capacity: capacity, Missing: capacity}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	categories := make([]WordTopicStockCategorySnapshot, 0, len(wordTopicStockCategories))
	for _, category := range wordTopicStockCategories {
		items := append([]WordPreparedTopic(nil), s.stock[category]...)
		categories = append(categories, WordTopicStockCategorySnapshot{
			Name: category, Label: wordTopicCategoryLabel(category), Count: len(items),
			Capacity: wordTopicStockCapacityPerCategory, Filling: s.filling[category], Topics: items,
		})
	}
	total := s.totalLocked()
	return WordTopicStockSnapshot{
		Enabled:       true,
		Total:         total,
		Capacity:      capacity,
		Missing:       capacity - total,
		Filling:       s.anyFillingLocked(),
		LastTrigger:   s.lastTrigger,
		LastAttemptAt: forecastSnapshotTime(s.lastAttemptAt),
		LastSuccessAt: forecastSnapshotTime(s.lastSuccessAt),
		LastError:     s.lastError,
		Categories:    categories,
	}
}

func logWordTopicStockFailure(category TopicCategory, trigger string, err error) {
	log.Printf("[IdleChat] Word topic stock refill failed: category=%s trigger=%s error=%v", category, strings.TrimSpace(trigger), err)
}
