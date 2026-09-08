package idlechat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// PreparedTopic は事前生成済みのお題。
type PreparedTopic struct {
	Domain      ForecastDomain    `json:"domain"`
	Topic       string            `json:"topic"`
	Seeds       []string          `json:"seeds"`
	TaskID      modulecore.TaskID `json:"task_id"`
	RunID       modulecore.RunID  `json:"run_id"`
	InitiatedBy string            `json:"initiated_by,omitempty"`
	Created     time.Time         `json:"created"`
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
	loadErr       error
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
		path:    strings.TrimSpace(path),
	}
	s.load()
	return s
}

func (s *forecastTopicStock) load() {
	if s == nil || s.path == "" {
		log.Printf("[Forecast] Stock file path is empty, skipping load")
		return
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.loadErr = fmt.Errorf("stock_read_failed: %w", err)
			s.lastError = fmt.Sprintf("stock_read_failed: %v", err)
			log.Printf("[Forecast] Stock file unreadable (%s): %v", s.path, err)
		}
		return
	}
	var f stockFile
	if err := json.Unmarshal(data, &f); err != nil {
		s.loadErr = fmt.Errorf("stock_parse_failed: %w", err)
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
			if err := validateIdleChatRunIdentity(item.TaskID, item.RunID); err != nil {
				discarded++
				continue
			}
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
		if err := s.saveLocked(); err != nil {
			s.loadErr = fmt.Errorf("stock_validation_cleanup_failed: %w", err)
			s.lastError = s.loadErr.Error()
			log.Printf("[Forecast] Stock validation cleanup failed: %v", err)
		}
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

func (s *forecastTopicStock) saveLocked() error {
	if s == nil || s.path == "" {
		return nil
	}
	if s.loadErr != nil {
		return s.loadErr
	}
	f := stockFile{Stock: s.stock}
	data, err := json.Marshal(f)
	if err != nil {
		s.lastError = fmt.Sprintf("stock_marshal_failed: %v", err)
		log.Printf("[Forecast] Stock file marshal error: %v", err)
		return fmt.Errorf("stock_marshal_failed: %w", err)
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.lastError = fmt.Sprintf("stock_directory_failed: %v", err)
		log.Printf("[Forecast] Stock directory create error: %v", err)
		return fmt.Errorf("stock_directory_failed: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".forecast_topic_stock-*")
	if err != nil {
		s.lastError = fmt.Sprintf("stock_temp_failed: %v", err)
		log.Printf("[Forecast] Stock temp file error: %v", err)
		return fmt.Errorf("stock_temp_failed: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(data)
	}
	if syncErr := tmp.Sync(); err == nil {
		err = syncErr
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
		return fmt.Errorf("stock_write_failed: %w", err)
	} else if strings.HasPrefix(s.lastError, "stock_") {
		s.lastError = ""
	}
	return nil
}

// pop はドメインのストックから1つ取得する。空なら nil。

func (s *forecastTopicStock) pop(domain string) (*PreparedTopic, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.stock[domain]
	if len(items) == 0 {
		return nil, nil
	}
	previous := append([]PreparedTopic(nil), items...)
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
	if err := s.saveLocked(); err != nil {
		s.stock[domain] = previous
		return nil, err
	}
	return &item, nil
}

func (s *forecastTopicStock) takeByRunID(runID modulecore.RunID) (*PreparedTopic, error) {
	if s == nil || runID == "" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, domain := range forecastDomains {
		items := s.stock[domain.Name]
		for index, item := range items {
			if item.RunID != runID {
				continue
			}
			previous := append([]PreparedTopic(nil), items...)
			s.stock[domain.Name] = append(append([]PreparedTopic(nil), items[:index]...), items[index+1:]...)
			if err := s.saveLocked(); err != nil {
				s.stock[domain.Name] = previous
				return nil, err
			}
			return &item, nil
		}
	}
	return nil, nil
}

func (s *forecastTopicStock) hasRunID(runID modulecore.RunID) bool {
	if s == nil || runID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, items := range s.stock {
		for _, item := range items {
			if item.RunID == runID {
				return true
			}
		}
	}
	return false
}

func (s *forecastTopicStock) hasExactCheckpointItem(checkpoint GenerationCheckpoint) bool {
	if s == nil || checkpoint.Result == nil || checkpoint.RunID == "" || checkpoint.TaskID == "" {
		return false
	}
	expectedTopic := strings.TrimSpace(checkpoint.Result.Topic)
	if expectedTopic == "" || strings.TrimSpace(checkpoint.Domain.Name) == "" {
		return false
	}
	expectedSeeds := append([]string(nil), checkpoint.ForecastSeeds...)
	if len(expectedSeeds) == 0 {
		expectedSeeds = append([]string(nil), checkpoint.Result.Seed.TrendKeywords...)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range s.stock[checkpoint.Domain.Name] {
		if item.TaskID != checkpoint.TaskID || item.RunID != checkpoint.RunID || item.Domain.Name != checkpoint.Domain.Name {
			continue
		}
		if strings.TrimSpace(item.Topic) != expectedTopic || !sameForecastStrings(item.Seeds, expectedSeeds) {
			continue
		}
		if resultDomain := strings.TrimSpace(checkpoint.Result.Seed.ForecastDomain); resultDomain != "" && resultDomain != checkpoint.Domain.Name {
			continue
		}
		return true
	}
	return false
}

func sameForecastStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// push はドメインのストックに追加する（上限 forecastTopicStockSize）。
func (s *forecastTopicStock) push(domain string, item PreparedTopic) (bool, error) {
	if s == nil {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(item.Topic) == "" {
		return false, errors.New("forecast topic is empty")
	}
	if strings.TrimSpace(item.InitiatedBy) == "" {
		item.InitiatedBy = "shiro"
	}
	if err := validateIdleChatRunIdentity(item.TaskID, item.RunID); err != nil {
		return false, err
	}
	items := s.stock[domain]
	itemKey := normalizeLoopText(item.Topic)
	for _, domainItems := range s.stock {
		for _, existing := range domainItems {
			if itemKey != "" && normalizeLoopText(existing.Topic) == itemKey {
				return false, nil
			}
		}
	}
	if len(items) >= forecastTopicStockSize {
		return false, nil
	}
	previous := append([]PreparedTopic(nil), items...)
	s.stock[domain] = append(items, item)
	if err := s.saveLocked(); err != nil {
		s.stock[domain] = previous
		return false, err
	}
	return true, nil
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
	capacity := len(forecastDomains) * forecastTopicStockSize
	if s == nil {
		return ForecastTopicStockSnapshot{Capacity: capacity, Missing: capacity}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return ForecastTopicStockSnapshot{
			Enabled: true, Capacity: capacity, Missing: capacity, LastError: s.lastError,
		}
	}
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
// 有効在庫が0件の場合、または未完了チェックポイントがある場合に逐次bootstrapする。
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
	_, recovering := o.nextForecastCheckpointDomain()
	if stock.total() == 0 || recovering {
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
	// Recover pending publications first. A nonempty restored stock does not
	// become a request to bootstrap unrelated empty domains.
	bootstrapMissing := stock.total() == 0
	domains := make([]ForecastDomain, 0, len(forecastDomains))
	for _, domain := range forecastDomains {
		if o.forecastTopicCheckpointExists(domain) {
			domains = append(domains, domain)
		}
	}
	if bootstrapMissing {
		for _, domain := range forecastDomains {
			if !o.forecastTopicCheckpointExists(domain) {
				domains = append(domains, domain)
			}
		}
	}
	go func() {
		for _, domain := range domains {
			if !o.forecastTopicRefillAvailable() || !o.tryBeginTopicProduction() {
				log.Printf("[Forecast] Startup bootstrap deferred because generation resources are busy")
				return
			}
			target := 1
			if o.forecastTopicCheckpointExists(domain) {
				target = forecastTopicStockSize + 1
			}
			if !stock.reserveDomain(domain.Name, target, "startup") {
				o.endTopicProduction()
				continue
			}
			o.fillForecastTopicStock(stock, domain, "startup")
		}
	}()
}

// RefillForecastTopicStockIfIdle starts at most one missing topic generation.
// It is safe to call from both the Idle monitor and Heartbeat; the stock enforces global single-flight.
func (o *IdleChatOrchestrator) RefillForecastTopicStockIfIdle(trigger string) bool {
	if o == nil || !o.forecastTopicRefillAvailable() || !o.tryBeginTopicProduction() {
		return false
	}
	o.mu.Lock()
	stock := o.topicStockBuf
	o.mu.Unlock()
	if stock == nil {
		o.endTopicProduction()
		return false
	}
	domain, ok := o.reserveForecastTopicProduction(stock, trigger)
	if !ok {
		o.endTopicProduction()
		return false
	}
	go o.fillForecastTopicStock(stock, domain, trigger)
	return true
}

func (o *IdleChatOrchestrator) reserveForecastTopicProduction(stock *forecastTopicStock, trigger string) (ForecastDomain, bool) {
	if stock == nil {
		return ForecastDomain{}, false
	}
	if checkpointDomain, recovering := o.nextForecastCheckpointDomain(); recovering {
		if stock.reserveDomain(checkpointDomain.Name, forecastTopicStockSize+1, trigger) {
			return checkpointDomain, true
		}
		return ForecastDomain{}, false
	}
	return stock.reserveNextDomain(forecastTopicStockSize, trigger)
}

func (o *IdleChatOrchestrator) forecastTopicCheckpointExists(domain ForecastDomain) bool {
	store := o.generationCheckpointStore()
	if store == nil || store.LoadError() != nil {
		return false
	}
	_, found := store.Get("forecast:" + domain.Name)
	return found
}

func (o *IdleChatOrchestrator) nextForecastCheckpointDomain() (ForecastDomain, bool) {
	store := o.generationCheckpointStore()
	if store == nil || store.LoadError() != nil {
		return ForecastDomain{}, false
	}
	for _, domain := range forecastDomains {
		if _, found := store.Get("forecast:" + domain.Name); found {
			return domain, true
		}
	}
	return ForecastDomain{}, false
}

func (o *IdleChatOrchestrator) forecastTopicRefillAvailable() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	externalBusy := o.externalLLMBusy != nil && o.externalLLMBusy()
	now := time.Now()
	autoChatDue := !o.disabled && time.Since(o.lastActivity) >= o.interval && (o.nextTopicAt.IsZero() || !now.Before(o.nextTopicAt))
	return !o.manualMode && !o.chatActive && !o.chatBusy && !o.workerBusy && !externalBusy && !autoChatDue
}

func (o *IdleChatOrchestrator) fillForecastTopicStock(stock *forecastTopicStock, domain ForecastDomain, trigger string) {
	defer o.endTopicProduction()
	err := o.produceForecastTopic(stock, domain)
	if stock != nil {
		stock.doneFilling(domain.Name, err)
	}
	if err != nil {
		log.Printf("[Forecast] Stock refill skipped: trigger=%s domain=%s error=%v", trigger, domain.Name, err)
		return
	}
	log.Printf("[Forecast] Stock refilled: trigger=%s domain=%s count=%d", trigger, domain.Name, stock.count(domain.Name))
}

func (o *IdleChatOrchestrator) produceForecastTopic(stock *forecastTopicStock, domain ForecastDomain) error {
	if o == nil {
		return errors.New("forecast topic orchestrator is not configured")
	}
	ctx := o.topicProductionContext()
	if ctx == nil {
		return errors.New("forecast topic context is not configured")
	}
	o.mu.Lock()
	issuer := o.runIssuer
	checkpointStore := o.generationCheckpoints
	o.mu.Unlock()
	if _, err := generationRunOwnerFromIssuer(issuer); err != nil {
		return err
	}
	if stock == nil || checkpointStore == nil {
		return errors.New("forecast topic persistence is not configured")
	}
	stock.mu.Lock()
	stockPath, stockLoadErr := stock.path, stock.loadErr
	stock.mu.Unlock()
	if stockPath == "" || checkpointStore.path == "" {
		return errors.New("forecast topic persistence is not configured")
	}
	if stockLoadErr != nil {
		return fmt.Errorf("forecast topic stock unavailable: %w", stockLoadErr)
	}
	if err := checkpointStore.LoadError(); err != nil {
		return err
	}

	checkpointKey := "forecast:" + domain.Name
	checkpoint, found := checkpointStore.Get(checkpointKey)
	if found {
		if checkpoint.Kind != "forecast" || checkpoint.Domain.Name != domain.Name {
			return fmt.Errorf("forecast topic checkpoint domain mismatch: got %s, want %s", checkpoint.Domain.Name, domain.Name)
		}
		if err := validateIdleChatRunIdentity(checkpoint.TaskID, checkpoint.RunID); err != nil {
			return fmt.Errorf("forecast topic checkpoint identity: %w", err)
		}
		if checkpoint.Result != nil {
			if checkpoint.Result.Category != "" && checkpoint.Result.Category != TopicCategoryForecast {
				return fmt.Errorf("forecast topic checkpoint result category mismatch: got %s", checkpoint.Result.Category)
			}
			if resultDomain := strings.TrimSpace(checkpoint.Result.Seed.ForecastDomain); resultDomain != "" && resultDomain != domain.Name {
				return fmt.Errorf("forecast topic checkpoint result domain mismatch: got %s, want %s", resultDomain, domain.Name)
			}
		}
		if checkpoint.Stage == "resume_pending" {
			previousRunID := checkpoint.RunID
			if err := reconcileGenerationResume(ctx, issuer, &checkpoint, checkpointStore); err != nil {
				if checkpoint.RunID != previousRunID {
					completionErr := finishGenerationRun(ctx, issuer, checkpoint, domaintask.StatusWaiting, "forecast resume reconciliation failed", "retry from saved forecast generation checkpoint")
					return errors.Join(err, completionErr)
				}
				return err
			}
		}
		run, err := inspectGenerationRun(ctx, issuer, checkpoint.TaskID, checkpoint.RunID)
		if err != nil {
			return err
		}
		switch run.Status {
		case domaintask.RunStatusSucceeded:
			if !stock.hasExactCheckpointItem(checkpoint) {
				return errors.New("forecast topic completion artifact is missing or mismatched")
			}
			if err := finishGenerationRun(ctx, issuer, checkpoint, domaintask.StatusSucceeded, "forecast topic saved", ""); err != nil {
				return err
			}
			return checkpointStore.Delete(checkpointKey)
		case domaintask.RunStatusFailed, domaintask.RunStatusCancelled:
			if stock.hasRunID(checkpoint.RunID) && !stock.hasExactCheckpointItem(checkpoint) {
				return errors.New("forecast topic completion artifact is mismatched")
			}
			status := domaintask.StatusFailed
			if run.Status == domaintask.RunStatusCancelled {
				status = domaintask.StatusCancelled
			}
			if err := finishGenerationRun(ctx, issuer, checkpoint, status, "forecast generation terminated", ""); err != nil {
				return err
			}
			if _, err := stock.takeByRunID(checkpoint.RunID); err != nil {
				return err
			}
			return checkpointStore.Delete(checkpointKey)
		}
		if stock.hasRunID(checkpoint.RunID) {
			if !stock.hasExactCheckpointItem(checkpoint) {
				return errors.New("forecast topic completion artifact is mismatched")
			}
			if run.Status == domaintask.RunStatusRunning {
				if err := finishGenerationRun(ctx, issuer, checkpoint, domaintask.StatusSucceeded, "forecast topic saved", ""); err != nil {
					return err
				}
				return checkpointStore.Delete(checkpointKey)
			}
			// This result was never published: its checkpoint still gates playback.
			// Remove the unpublished item before resuming the saved generation.
			if _, err := stock.takeByRunID(checkpoint.RunID); err != nil {
				return err
			}
		}
	}

	if !found {
		taskID, runID, err := issueIdleChatRun(ctx, issuer, "IdleChat forecast topic", "shiro", domaintask.RunStartReasonFirst, "")
		if err != nil {
			return err
		}
		checkpoint = GenerationCheckpoint{
			Key: checkpointKey, Kind: "forecast", TaskID: taskID, RunID: runID, Stage: "created",
			Category: TopicCategoryForecast, Domain: domain,
		}
		if err := checkpointStore.Put(checkpoint); err != nil {
			return errors.Join(err, finishGenerationRun(ctx, issuer, checkpoint, domaintask.StatusFailed, "forecast checkpoint save failed", ""))
		}
	} else {
		// Persist the resume intent before the owner can issue its successor.
		// If saving that successor ID fails, the intent allows exact recovery.
		checkpoint.Stage = "resume_pending"
		if err := checkpointStore.Put(checkpoint); err != nil {
			return err
		}
		run, err := resumeIdleChatRun(ctx, issuer, &checkpoint, checkpointStore)
		if err != nil {
			return err
		}
		checkpoint.RunID = run.RunID
	}
	if checkpoint.Result != nil {
		checkpoint.Stage = "result"
	} else if len(checkpoint.Candidates) > 0 {
		checkpoint.Stage = "candidates"
	} else if len(checkpoint.ForecastSeeds) > 0 {
		checkpoint.Stage = "seeds"
	} else {
		checkpoint.Stage = "created"
	}
	if found {
		if err := checkpointStore.Put(checkpoint); err != nil {
			return errors.Join(err, finishGenerationRun(ctx, issuer, checkpoint, domaintask.StatusWaiting, "forecast checkpoint save failed", "retry saving resumed generation checkpoint"))
		}
	}

	var topic string
	var seeds []string
	var failure *forecastTopicFailure
	if checkpoint.Result != nil {
		topic = checkpoint.Result.Topic
		seeds = append([]string(nil), checkpoint.ForecastSeeds...)
		if len(seeds) == 0 {
			seeds = append([]string(nil), checkpoint.Result.Seed.TrendKeywords...)
		}
	} else {
		topic, seeds, failure = o.generateForecastTopicForStock(domain, &checkpoint)
	}
	if failure != nil {
		err := fmt.Errorf("%s: %s", failure.ErrorCode, failure.Error)
		return errors.Join(err, finishGenerationRun(ctx, issuer, checkpoint, domaintask.StatusWaiting, "forecast generation interrupted", "retry from saved generation checkpoint"))
	}
	topic = strings.TrimSpace(topic)
	if topic == "" {
		err := errors.New("empty_topic: forecast topic generation returned empty topic")
		return errors.Join(err, finishGenerationRun(ctx, issuer, checkpoint, domaintask.StatusWaiting, "forecast generation produced no topic", "retry from saved generation checkpoint"))
	}
	checkpoint.ForecastSeeds = append([]string(nil), seeds...)
	if checkpoint.Result == nil {
		checkpoint.Result = &TopicGenerationResult{
			Topic: topic, Category: TopicCategoryForecast, Strategy: string(StrategyForecast),
			Seed: TopicSeed{
				Category: TopicCategoryForecast, ForecastDomain: domain.Name,
				ForecastHorizon: forecastHorizonForDomain(domain.Name), TrendKeywords: append([]string(nil), seeds...),
			}, Provider: "CodexExe", Initiator: "shiro",
		}
	} else {
		result := *checkpoint.Result
		result.Topic = topic
		if result.Category == "" {
			result.Category = TopicCategoryForecast
		}
		if result.Strategy == "" {
			result.Strategy = string(StrategyForecast)
		}
		if result.Seed.Category == "" {
			result.Seed.Category = TopicCategoryForecast
		}
		result.Seed.ForecastDomain = domain.Name
		if strings.TrimSpace(result.Seed.ForecastHorizon) == "" {
			result.Seed.ForecastHorizon = forecastHorizonForDomain(domain.Name)
		}
		result.Seed.TrendKeywords = append([]string(nil), seeds...)
		if strings.TrimSpace(result.Initiator) == "" {
			result.Initiator = "shiro"
		}
		checkpoint.Result = &result
	}
	checkpoint.Stage = "result"
	if err := checkpointStore.Put(checkpoint); err != nil {
		return errors.Join(err, finishGenerationRun(ctx, issuer, checkpoint, domaintask.StatusWaiting, "forecast result checkpoint save failed", "retry saving forecast result checkpoint"))
	}
	item := PreparedTopic{
		Domain: domain, Topic: topic, Seeds: append([]string(nil), seeds...), TaskID: checkpoint.TaskID,
		RunID: checkpoint.RunID, InitiatedBy: "shiro", Created: time.Now().UTC(),
	}
	added, err := stock.push(domain.Name, item)
	if err != nil {
		return errors.Join(err, finishGenerationRun(ctx, issuer, checkpoint, domaintask.StatusWaiting, "forecast stock save failed", "retry from saved generation checkpoint"))
	}
	if !added && !stock.hasRunID(checkpoint.RunID) {
		err := fmt.Errorf("duplicate_or_full: generated topic was not added for domain %s", domain.Name)
		if completionErr := finishGenerationRun(ctx, issuer, checkpoint, domaintask.StatusFailed, "forecast topic duplicate or stock full", ""); completionErr != nil {
			return errors.Join(err, completionErr)
		}
		return errors.Join(err, checkpointStore.Delete(checkpointKey))
	}
	if err := finishGenerationRun(ctx, issuer, checkpoint, domaintask.StatusSucceeded, "forecast topic saved", ""); err != nil {
		return err
	}
	return checkpointStore.Delete(checkpointKey)
}

func (o *IdleChatOrchestrator) generateForecastTopicForStock(domain ForecastDomain, checkpoint *GenerationCheckpoint) (string, []string, *forecastTopicFailure) {
	o.mu.Lock()
	generator := o.forecastTopicGenerator
	issuer := o.runIssuer
	o.mu.Unlock()
	if generator != nil {
		ctx := o.topicProductionContext()
		if checkpoint == nil {
			return "", nil, newForecastTopicFailure("checkpoint", domain.Name, "", errors.New("forecast checkpoint is nil"))
		}
		run, err := inspectGenerationRun(ctx, issuer, checkpoint.TaskID, checkpoint.RunID)
		if err != nil {
			return "", nil, newForecastTopicFailure("run", domain.Name, "", err)
		}
		var topic string
		var seeds []string
		var failure *forecastTopicFailure
		if err := executeIdleChatRunEffect(ctx, issuer, checkpoint.TaskID, checkpoint.RunID, run.Assignee, func(context.Context) error {
			topic, seeds, failure = generator(domain)
			return nil
		}); err != nil {
			return "", nil, newForecastTopicFailure("topic", domain.Name, "", err)
		}
		return topic, seeds, failure
	}
	ctx := o.topicProductionContext()
	if checkpoint == nil {
		return "", nil, newForecastTopicFailure("checkpoint", domain.Name, "", errors.New("forecast checkpoint is nil"))
	}
	provider, providerLabel := o.forecastTopicLLMInfo()
	run, err := inspectGenerationRun(ctx, issuer, checkpoint.TaskID, checkpoint.RunID)
	if err != nil {
		return "", nil, newForecastTopicFailure("run", domain.Name, providerLabel, err)
	}
	provider = newRunGuardedLLMProvider(issuer, checkpoint.TaskID, checkpoint.RunID, run.Assignee, provider)
	return o.generateForecastTopicInlineForStock(ctx, domain, checkpoint, provider, providerLabel)
}

// popForecastTopic は公開済みストックからお題を取得する。不足補充はIdle／Heartbeat契機に分離する。
// ストックが空、未永続化、または公開前なら、インライン生成へ迂回せず明示エラーを返す。
func (o *IdleChatOrchestrator) popForecastTopic(domain ForecastDomain) (string, []string, error) {
	o.mu.Lock()
	stock := o.topicStockBuf
	o.mu.Unlock()
	if stock == nil {
		return "", nil, forecastTopicStockPlaybackError(domain, "stock", "stock_unavailable", errors.New("forecast topic stock is not configured"))
	}
	stock.mu.Lock()
	loadErr := stock.loadErr
	items := append([]PreparedTopic(nil), stock.stock[domain.Name]...)
	stock.mu.Unlock()
	if loadErr != nil {
		return "", nil, forecastTopicStockPlaybackError(domain, "stock", "stock_unavailable", loadErr)
	}
	if len(items) == 0 {
		return "", nil, forecastTopicStockPlaybackError(domain, "stock", "stock_empty", errors.New("forecast topic stock has no prepared topic"))
	}
	runID := items[0].RunID
	pending, err := o.forecastTopicPublicationPending(domain.Name, runID)
	if err != nil {
		return "", nil, forecastTopicStockPlaybackError(domain, "publication", "checkpoint_unavailable", err)
	}
	if pending {
		return "", nil, forecastTopicStockPlaybackError(domain, "publication", "publication_pending", errors.New("forecast topic publication is pending"))
	}
	item, err := stock.takeByRunID(runID)
	if err != nil {
		return "", nil, forecastTopicStockPlaybackError(domain, "stock", "stock_write_failed", err)
	}
	if item == nil {
		return "", nil, forecastTopicStockPlaybackError(domain, "stock", "stock_concurrent_consume", errors.New("forecast topic was consumed concurrently"))
	}
	if strings.TrimSpace(item.Topic) == "" {
		return "", nil, forecastTopicStockPlaybackError(domain, "stock", "empty_topic", errors.New("forecast topic stock contained an empty topic"))
	}
	topic := normalizeForecastDisplayTopic(domain, item.Topic)
	log.Printf("[Forecast] Topic popped from stock: %s (remaining=%d)", domain.Name, stock.count(domain.Name))
	return topic, item.Seeds, nil
}

func forecastTopicStockPlaybackError(domain ForecastDomain, phase, code string, err error) error {
	if err == nil {
		err = errors.New("forecast topic stock playback failed")
	}
	return errors.New(formatForecastTopicError(domain, &forecastTopicFailure{
		Phase: phase, Domain: strings.TrimSpace(domain.Name), ErrorCode: code, Error: err.Error(),
	}))
}
