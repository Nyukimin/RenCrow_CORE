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
)

// PreparedTopic は事前生成済みのお題。
type PreparedTopic struct {
	Domain  ForecastDomain `json:"domain"`
	Topic   string         `json:"topic"`
	Seeds   []string       `json:"seeds"`
	Created time.Time      `json:"created"`
}

// forecastTopicStock はドメインごとのお題バッファ（ファイル永続化付き）。
type forecastTopicStock struct {
	mu            sync.Mutex
	stock         map[string][]PreparedTopic
	filling       map[string]bool
	path          string // 永続化ファイルパス
	lastTrigger   string
	lastAttemptAt time.Time
	lastSuccessAt time.Time
	lastError     string
}

// stockFile はファイル保存形式。
type stockFile struct {
	Stock map[string][]PreparedTopic `json:"stock"`
}

// ForecastTopicStockSnapshot はDebug Viewerへ公開する読み取り専用の在庫状態。
type ForecastTopicStockSnapshot struct {
	Enabled       bool                               `json:"enabled"`
	Total         int                                `json:"total"`
	Capacity      int                                `json:"capacity"`
	Missing       int                                `json:"missing"`
	Filling       bool                               `json:"filling"`
	LastTrigger   string                             `json:"last_trigger,omitempty"`
	LastAttemptAt *time.Time                         `json:"last_attempt_at,omitempty"`
	LastSuccessAt *time.Time                         `json:"last_success_at,omitempty"`
	LastError     string                             `json:"last_error,omitempty"`
	Domains       []ForecastTopicStockDomainSnapshot `json:"domains"`
}

// ForecastTopicStockDomainSnapshot はドメイン単位の在庫内容。
type ForecastTopicStockDomainSnapshot struct {
	Name     string          `json:"name"`
	Count    int             `json:"count"`
	Capacity int             `json:"capacity"`
	Filling  bool            `json:"filling"`
	Topics   []PreparedTopic `json:"topics"`
}

func newForecastTopicStock(path string) *forecastTopicStock {
	s := &forecastTopicStock{
		stock:   make(map[string][]PreparedTopic),
		filling: make(map[string]bool),
		path:    path,
	}
	s.load()
	return s
}

func (s *forecastTopicStock) load() {
	if s.path == "" {
		log.Printf("[Forecast] Stock file path is empty, skipping load")
		return
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.lastError = fmt.Sprintf("stock_read_failed: %v", err)
			log.Printf("[Forecast] Stock file unreadable (%s): %v", s.path, err)
		}
		return
	}
	var f stockFile
	if err := json.Unmarshal(data, &f); err != nil {
		s.lastError = fmt.Sprintf("stock_parse_failed: %v", err)
		log.Printf("[Forecast] Stock file parse error: %v", err)
		return
	}
	if len(f.Stock) == 0 {
		log.Printf("[Forecast] Stock file empty or nil")
		return
	}

	cleaned := make(map[string][]PreparedTopic, len(forecastDomains))
	seen := make(map[string]struct{})
	discarded := 0
	for _, domain := range forecastDomains {
		for _, item := range f.Stock[domain.Name] {
			topic := strings.TrimSpace(item.Topic)
			key := normalizeLoopText(topic)
			if topic == "" || key == "" {
				discarded++
				continue
			}
			if _, exists := seen[key]; exists {
				discarded++
				continue
			}
			if len(cleaned[domain.Name]) >= forecastTopicStockSize {
				discarded++
				continue
			}
			seen[key] = struct{}{}
			item.Domain = domain
			item.Topic = topic
			cleaned[domain.Name] = append(cleaned[domain.Name], item)
		}
	}
	for name, items := range f.Stock {
		if !isForecastDomainName(name) {
			discarded += len(items)
		}
	}
	s.stock = cleaned
	total := s.totalLocked()
	log.Printf("[Forecast] Stock loaded from file: %d topics across %d domains", total, len(f.Stock))
	if discarded > 0 {
		log.Printf("[Forecast] Stock validation discarded %d invalid, duplicate, overflow, or unknown records", discarded)
		s.saveLocked()
	}
}

func isForecastDomainName(name string) bool {
	for _, domain := range forecastDomains {
		if domain.Name == name {
			return true
		}
	}
	return false
}

func (s *forecastTopicStock) saveLocked() {
	if s.path == "" {
		return
	}
	f := stockFile{Stock: s.stock}
	data, err := json.Marshal(f)
	if err != nil {
		s.lastError = fmt.Sprintf("stock_marshal_failed: %v", err)
		log.Printf("[Forecast] Stock file marshal error: %v", err)
		return
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.lastError = fmt.Sprintf("stock_directory_failed: %v", err)
		log.Printf("[Forecast] Stock directory create error: %v", err)
		return
	}
	tmp, err := os.CreateTemp(dir, ".forecast_topic_stock-*")
	if err != nil {
		s.lastError = fmt.Sprintf("stock_temp_failed: %v", err)
		log.Printf("[Forecast] Stock temp file error: %v", err)
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
		log.Printf("[Forecast] Stock file write error: %v", err)
	} else if strings.HasPrefix(s.lastError, "stock_") {
		s.lastError = ""
	}
}

// pop はドメインのストックから1つ取得する。空なら nil。
func (s *forecastTopicStock) pop(domain string) *PreparedTopic {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.stock[domain]
	if len(items) == 0 {
		return nil
	}
	item := items[0]
	usedKey := normalizeLoopText(item.Topic)
	remaining := make([]PreparedTopic, 0, len(items)-1)
	for _, candidate := range items[1:] {
		if usedKey != "" && normalizeLoopText(candidate.Topic) == usedKey {
			continue
		}
		remaining = append(remaining, candidate)
	}
	s.stock[domain] = remaining
	s.saveLocked()
	return &item
}

// push はドメインのストックに追加する（上限 forecastTopicStockSize）。
func (s *forecastTopicStock) push(domain string, item PreparedTopic) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(item.Topic) == "" {
		return false
	}
	items := s.stock[domain]
	itemKey := normalizeLoopText(item.Topic)
	for _, domainItems := range s.stock {
		for _, existing := range domainItems {
			if itemKey != "" && normalizeLoopText(existing.Topic) == itemKey {
				return false
			}
		}
	}
	if len(items) >= forecastTopicStockSize {
		return false
	}
	s.stock[domain] = append(items, item)
	s.saveLocked()
	return true
}

// count はドメインのストック数を返す。
func (s *forecastTopicStock) count(domain string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.stock[domain])
}

func (s *forecastTopicStock) total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.totalLocked()
}

func (s *forecastTopicStock) totalLocked() int {
	total := 0
	for _, items := range s.stock {
		total += len(items)
	}
	return total
}

func (s *forecastTopicStock) reserveDomain(domain string, target int, trigger string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.anyFillingLocked() || len(s.stock[domain]) >= target {
		return false
	}
	s.filling[domain] = true
	s.lastTrigger = strings.TrimSpace(trigger)
	s.lastAttemptAt = time.Now().UTC()
	return true
}

func (s *forecastTopicStock) reserveNextDomain(target int, trigger string) (ForecastDomain, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.anyFillingLocked() {
		return ForecastDomain{}, false
	}
	bestIndex := -1
	bestCount := target
	for i, domain := range forecastDomains {
		count := len(s.stock[domain.Name])
		if count < bestCount {
			bestIndex = i
			bestCount = count
		}
	}
	if bestIndex < 0 {
		return ForecastDomain{}, false
	}
	domain := forecastDomains[bestIndex]
	s.filling[domain.Name] = true
	s.lastTrigger = strings.TrimSpace(trigger)
	s.lastAttemptAt = time.Now().UTC()
	return domain, true
}

func (s *forecastTopicStock) doneFilling(domain string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.filling[domain] = false
	if err != nil {
		s.lastError = err.Error()
		return
	}
	s.lastSuccessAt = time.Now().UTC()
	if !strings.HasPrefix(s.lastError, "stock_") {
		s.lastError = ""
	}
}

func (s *forecastTopicStock) anyFilling() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.anyFillingLocked()
}

func (s *forecastTopicStock) anyFillingLocked() bool {
	for _, filling := range s.filling {
		if filling {
			return true
		}
	}
	return false
}

func (s *forecastTopicStock) snapshot() ForecastTopicStockSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	domains := make([]ForecastTopicStockDomainSnapshot, 0, len(forecastDomains))
	for _, domain := range forecastDomains {
		items := append([]PreparedTopic(nil), s.stock[domain.Name]...)
		domains = append(domains, ForecastTopicStockDomainSnapshot{
			Name:     domain.Name,
			Count:    len(items),
			Capacity: forecastTopicStockSize,
			Filling:  s.filling[domain.Name],
			Topics:   items,
		})
	}
	total := s.totalLocked()
	capacity := len(forecastDomains) * forecastTopicStockSize
	return ForecastTopicStockSnapshot{
		Enabled:       true,
		Total:         total,
		Capacity:      capacity,
		Missing:       capacity - total,
		Filling:       s.anyFillingLocked(),
		LastTrigger:   s.lastTrigger,
		LastAttemptAt: forecastSnapshotTime(s.lastAttemptAt),
		LastSuccessAt: forecastSnapshotTime(s.lastSuccessAt),
		LastError:     s.lastError,
		Domains:       domains,
	}
}

func forecastSnapshotTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	copy := value
	return &copy
}

// InitForecastTopicStock はお題ストックを初期化する。
// path はストックの永続化ファイルパス。
// 有効在庫が0件の場合だけ、各ドメイン1件を上限に逐次bootstrapする。
func (o *IdleChatOrchestrator) InitForecastTopicStock(path string) {
	o.mu.Lock()
	if o.topicStockBuf != nil {
		o.mu.Unlock()
		return
	}
	o.topicStockBuf = newForecastTopicStock(path)
	stock := o.topicStockBuf
	o.mu.Unlock()
	log.Printf("[Forecast] Topic stock initialized (total=%d capacity=%d)", stock.total(), len(forecastDomains)*forecastTopicStockSize)
	if stock.total() == 0 {
		o.bootstrapForecastTopicStockAsync(stock)
	}
}

// ForecastTopicStockSnapshot returns a copy of the persistent Forecast topic stock.
func (o *IdleChatOrchestrator) ForecastTopicStockSnapshot() ForecastTopicStockSnapshot {
	if o == nil {
		return ForecastTopicStockSnapshot{Capacity: len(forecastDomains) * forecastTopicStockSize, Missing: len(forecastDomains) * forecastTopicStockSize}
	}
	o.mu.Lock()
	stock := o.topicStockBuf
	o.mu.Unlock()
	if stock == nil {
		return ForecastTopicStockSnapshot{Capacity: len(forecastDomains) * forecastTopicStockSize, Missing: len(forecastDomains) * forecastTopicStockSize}
	}
	return stock.snapshot()
}

func (o *IdleChatOrchestrator) bootstrapForecastTopicStockAsync(stock *forecastTopicStock) {
	go func() {
		for _, domain := range forecastDomains {
			if !o.forecastTopicRefillAvailable() {
				log.Printf("[Forecast] Startup bootstrap deferred because generation resources are busy")
				return
			}
			if !stock.reserveDomain(domain.Name, 1, "startup") {
				continue
			}
			o.fillForecastTopicStock(stock, domain, "startup")
		}
	}()
}

// RefillForecastTopicStockIfIdle starts at most one missing topic generation.
// It is safe to call from both the Idle monitor and Heartbeat; the stock enforces global single-flight.
func (o *IdleChatOrchestrator) RefillForecastTopicStockIfIdle(trigger string) bool {
	if o == nil || !o.forecastTopicRefillAvailable() {
		return false
	}
	o.mu.Lock()
	stock := o.topicStockBuf
	o.mu.Unlock()
	if stock == nil {
		return false
	}
	domain, ok := stock.reserveNextDomain(forecastTopicStockSize, trigger)
	if !ok {
		return false
	}
	go o.fillForecastTopicStock(stock, domain, trigger)
	return true
}

func (o *IdleChatOrchestrator) forecastTopicRefillAvailable() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	externalBusy := o.externalLLMBusy != nil && o.externalLLMBusy()
	now := time.Now()
	autoChatDue := time.Since(o.lastActivity) >= o.interval && (o.nextTopicAt.IsZero() || !now.Before(o.nextTopicAt))
	return !o.disabled && !o.manualMode && !o.chatActive && !o.chatBusy && !o.workerBusy && !externalBusy && !autoChatDue
}

func (o *IdleChatOrchestrator) fillForecastTopicStock(stock *forecastTopicStock, domain ForecastDomain, trigger string) {
	topic, seeds, failure := o.generateForecastTopicForStock(domain)
	if failure != nil {
		err := fmt.Errorf("%s: %s", failure.ErrorCode, failure.Error)
		stock.doneFilling(domain.Name, err)
		log.Printf("[Forecast] Stock refill skipped: trigger=%s domain=%s error_code=%s phase=%s provider=%s", trigger, domain.Name, failure.ErrorCode, failure.Phase, failure.Provider)
		return
	}
	topic = strings.TrimSpace(topic)
	if topic == "" {
		err := errors.New("empty_topic: forecast topic generation returned empty topic")
		stock.doneFilling(domain.Name, err)
		log.Printf("[Forecast] Stock refill skipped: trigger=%s domain=%s error_code=empty_topic", trigger, domain.Name)
		return
	}
	if !stock.push(domain.Name, PreparedTopic{Domain: domain, Topic: topic, Seeds: seeds, Created: time.Now().UTC()}) {
		err := errors.New("duplicate_or_full: generated topic was not added")
		stock.doneFilling(domain.Name, err)
		log.Printf("[Forecast] Stock refill discarded: trigger=%s domain=%s reason=duplicate_or_full", trigger, domain.Name)
		return
	}
	stock.doneFilling(domain.Name, nil)
	log.Printf("[Forecast] Stock refilled: trigger=%s domain=%s count=%d", trigger, domain.Name, stock.count(domain.Name))
}

func (o *IdleChatOrchestrator) generateForecastTopicForStock(domain ForecastDomain) (string, []string, *forecastTopicFailure) {
	o.mu.Lock()
	generator := o.forecastTopicGenerator
	o.mu.Unlock()
	if generator != nil {
		return generator(domain)
	}
	return o.generateForecastTopicInline(domain)
}

// popForecastTopic はストックからお題を取得する。不足補充はIdle／Heartbeat契機に分離する。
// ストックが空で生成にも失敗した場合は、汎用お題ではなくエラーコード付きの明示エラーを返す。
func (o *IdleChatOrchestrator) popForecastTopic(domain ForecastDomain) (string, []string) {
	o.mu.Lock()
	stock := o.topicStockBuf
	o.mu.Unlock()

	if stock != nil {
		if item := stock.pop(domain.Name); item != nil {
			topic := normalizeForecastDisplayTopic(domain, item.Topic)
			if strings.TrimSpace(item.Topic) == "" {
				log.Printf("[Forecast] Empty topic popped from stock: %s, discarding", domain.Name)
			} else {
				log.Printf("[Forecast] Topic popped from stock: %s (remaining=%d)", domain.Name, stock.count(domain.Name))
				return topic, item.Seeds
			}
		}
	}

	// ストック空 → インライン生成。失敗時は汎用お題ではなく明示エラーを表示する。
	log.Printf("[Forecast] Stock empty for %s, generating inline", domain.Name)
	topic, seeds, failure := o.generateForecastTopicInline(domain)
	if failure != nil {
		return formatForecastTopicError(domain, failure), seeds
	}
	if strings.TrimSpace(topic) == "" {
		failure = &forecastTopicFailure{
			Phase:     "topic",
			Domain:    strings.TrimSpace(domain.Name),
			ErrorCode: "empty_topic",
			Error:     "forecast topic generation returned empty topic",
		}
		return formatForecastTopicError(domain, failure), seeds
	}
	return normalizeForecastDisplayTopic(domain, topic), seeds
}
