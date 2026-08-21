package backlog

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
)

// JSONLStore preserves the original append-only Backlog file contract. Every
// Save appends a revision and List projects the latest valid record per item.
type JSONLStore struct {
	path string
	mu   sync.Mutex
}

func NewJSONLStore(path string) *JSONLStore {
	path = strings.TrimSpace(path)
	if path == "" {
		path = filepath.Join("workspace", "logs", "backlog.jsonl")
	} else if strings.HasSuffix(strings.ToLower(path), ".jsonl") {
		// keep the supplied file path
	} else {
		path = filepath.Join(path, "backlog.jsonl")
	}
	return &JSONLStore{path: path}
}

func (s *JSONLStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *JSONLStore) List(_ context.Context, limit int) ([]domainbacklog.Item, error) {
	if s == nil {
		return nil, fmt.Errorf("backlog store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	latest := map[string]domainbacklog.Item{}
	if err := readBacklogJSONL(s.path, func(raw []byte) error {
		var item domainbacklog.Item
		if err := json.Unmarshal(raw, &item); err != nil {
			// Existing backlog files are append-only and may contain a partially
			// written/corrupt historical line. Preserve the old tolerant read.
			return nil
		}
		item = NormalizeForRead(item)
		if strings.TrimSpace(item.ItemID) == "" {
			return nil
		}
		latest[item.ItemID] = item
		return nil
	}); err != nil {
		return nil, err
	}
	items := make([]domainbacklog.Item, 0, len(latest))
	for _, item := range latest {
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].CheckOK != items[j].CheckOK {
			return !items[i].CheckOK
		}
		return items[i].UpdatedAt > items[j].UpdatedAt
	})
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *JSONLStore) FindByID(ctx context.Context, itemID string) (domainbacklog.Item, bool, error) {
	items, err := s.List(ctx, 0)
	if err != nil {
		return domainbacklog.Item{}, false, err
	}
	for _, item := range items {
		if item.ItemID == strings.TrimSpace(itemID) {
			return item, true, nil
		}
	}
	return domainbacklog.Item{}, false, nil
}

func (s *JSONLStore) Save(_ context.Context, item domainbacklog.Item) error {
	if s == nil {
		return fmt.Errorf("backlog store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	item = NormalizeForSave(item, time.Now().UTC())
	if err := domainbacklog.ValidateItem(item); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(encoded, '\n'))
	return err
}

func NormalizeForSave(item domainbacklog.Item, now time.Time) domainbacklog.Item {
	item.ItemID = strings.TrimSpace(item.ItemID)
	if item.ItemID == "" {
		item.ItemID = fmt.Sprintf("backlog-%d", now.UnixNano())
	}
	item.Kind = normalizeKind(item.Kind)
	item.Title = strings.TrimSpace(item.Title)
	if item.Title == "" {
		item.Title = "untitled"
	}
	item.Body = strings.TrimSpace(item.Body)
	item.Source = strings.TrimSpace(item.Source)
	item.Owner = strings.TrimSpace(item.Owner)
	item.OwnerModule = strings.TrimSpace(item.OwnerModule)
	item.Priority = normalizePriority(item.Priority)
	if item.CreatedAt == "" {
		item.CreatedAt = now.Format(time.RFC3339)
	}
	item.UpdatedAt = now.Format(time.RFC3339)
	if item.SchemaVersion >= domainbacklog.SchemaVersion2 {
		item.SchemaVersion = domainbacklog.SchemaVersion2
		if item.ConceptState == "" {
			item.ConceptState = domainbacklog.ConceptCandidate
		}
		if item.DeliveryState == "" {
			item.DeliveryState = domainbacklog.DeliveryNone
		}
		item.Status = domainbacklog.LegacyStatus(item)
		item.CheckOK = item.DeliveryState == domainbacklog.DeliveryLiveVerified || item.DeliveryState == domainbacklog.DeliveryDone
		return item
	}
	// Legacy records remain byte-compatible in their status/check_ok fields,
	// while the in-memory projection carries safe v2 candidate/none fields.
	if item.Status == "" {
		item.Status = "open"
	}
	return item
}

func NormalizeForRead(item domainbacklog.Item) domainbacklog.Item {
	if item.SchemaVersion >= domainbacklog.SchemaVersion2 {
		if item.ConceptState == "" {
			item.ConceptState = domainbacklog.ConceptCandidate
		}
		if item.DeliveryState == "" {
			item.DeliveryState = domainbacklog.DeliveryNone
		}
		if item.Status == "" {
			item.Status = domainbacklog.LegacyStatus(item)
		}
		if item.DeliveryState != domainbacklog.DeliveryLiveVerified && item.DeliveryState != domainbacklog.DeliveryDone {
			item.CheckOK = false
		}
		return item
	}
	return domainbacklog.ProjectLegacy(item)
}

func normalizeKind(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "unimplemented", "todo", "task":
		return "unimplemented"
	default:
		return "idea"
	}
}

func normalizePriority(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "low", "normal", "high", "urgent":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "normal"
	}
}

func readBacklogJSONL(path string, fn func([]byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if err := fn([]byte(line)); err != nil {
			return err
		}
	}
	return scanner.Err()
}
