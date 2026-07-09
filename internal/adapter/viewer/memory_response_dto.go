package viewer

import (
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation/l1sqlite"
	"time"

	domconv "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/conversation"
)

type memoryEventDTO struct {
	ID          string
	Namespace   string
	SessionID   string
	ThreadID    int64
	Speaker     domconv.Speaker
	Message     string
	Meta        map[string]interface{}
	MemoryState string
	Layer       string
	Source      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type eventLogEntryDTO struct {
	ID        string
	EventType string
	Namespace string
	SessionID string
	ThreadID  int64
	Payload   map[string]interface{}
	Source    string
	CreatedAt time.Time
}

type searchCacheEntryDTO struct {
	QueryHash       string
	NormalizedQuery string
	Provider        string
	RawQuery        string
	ResultsJSON     string
	SourceURLs      []string
	RetrievedAt     time.Time
	ExpiresAt       time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type newsItemDTO struct {
	ID           string
	StagingID    string
	Category     string
	SourceID     string
	SourceURL    string
	PublishedAt  time.Time
	FetchedAt    time.Time
	RawText      string
	RawHash      string
	SummaryDraft string
	Keywords     []string
	LicenseNote  string
	Meta         map[string]interface{}
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type dailyDigestDTO struct {
	ID         string
	DigestDate string
	Category   string
	DigestSlot string
	NewsIDs    []string
	DigestText string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type knowledgeItemDTO struct {
	ID           string
	StagingID    string
	Domain       string
	Title        string
	SourceID     string
	SourceURL    string
	RawText      string
	RawHash      string
	SummaryDraft string
	Keywords     []string
	LicenseNote  string
	Meta         map[string]interface{}
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func memoryEventDTOFromL1(item l1sqlite.L1MemoryEvent) memoryEventDTO {
	return memoryEventDTO{
		ID:          item.ID,
		Namespace:   item.Namespace,
		SessionID:   item.SessionID,
		ThreadID:    item.ThreadID,
		Speaker:     item.Speaker,
		Message:     item.Message,
		Meta:        item.Meta,
		MemoryState: item.MemoryState,
		Layer:       item.Layer,
		Source:      item.Source,
		CreatedAt:   item.CreatedAt,
		UpdatedAt:   item.UpdatedAt,
	}
}

func memoryEventDTOsFromL1(items []l1sqlite.L1MemoryEvent) []memoryEventDTO {
	if items == nil {
		return nil
	}
	out := make([]memoryEventDTO, 0, len(items))
	for _, item := range items {
		out = append(out, memoryEventDTOFromL1(item))
	}
	return out
}

func memoryEventDTOFromL1Ptr(item *l1sqlite.L1MemoryEvent) *memoryEventDTO {
	if item == nil {
		return nil
	}
	dto := memoryEventDTOFromL1(*item)
	return &dto
}

func eventLogEntryDTOsFromL1(items []l1sqlite.L1EventLogEntry) []eventLogEntryDTO {
	if items == nil {
		return nil
	}
	out := make([]eventLogEntryDTO, 0, len(items))
	for _, item := range items {
		out = append(out, eventLogEntryDTO{
			ID:        item.ID,
			EventType: item.EventType,
			Namespace: item.Namespace,
			SessionID: item.SessionID,
			ThreadID:  item.ThreadID,
			Payload:   item.Payload,
			Source:    item.Source,
			CreatedAt: item.CreatedAt,
		})
	}
	return out
}

func searchCacheEntryDTOsFromL1(items []l1sqlite.L1SearchCacheEntry) []searchCacheEntryDTO {
	if items == nil {
		return nil
	}
	out := make([]searchCacheEntryDTO, 0, len(items))
	for _, item := range items {
		out = append(out, searchCacheEntryDTO{
			QueryHash:       item.QueryHash,
			NormalizedQuery: item.NormalizedQuery,
			Provider:        item.Provider,
			RawQuery:        item.RawQuery,
			ResultsJSON:     item.ResultsJSON,
			SourceURLs:      item.SourceURLs,
			RetrievedAt:     item.RetrievedAt,
			ExpiresAt:       item.ExpiresAt,
			CreatedAt:       item.CreatedAt,
			UpdatedAt:       item.UpdatedAt,
		})
	}
	return out
}

func newsItemDTOsFromL1(items []l1sqlite.L1NewsItem) []newsItemDTO {
	if items == nil {
		return nil
	}
	out := make([]newsItemDTO, 0, len(items))
	for _, item := range items {
		out = append(out, newsItemDTO{
			ID:           item.ID,
			StagingID:    item.StagingID,
			Category:     item.Category,
			SourceID:     item.SourceID,
			SourceURL:    item.SourceURL,
			PublishedAt:  item.PublishedAt,
			FetchedAt:    item.FetchedAt,
			RawText:      item.RawText,
			RawHash:      item.RawHash,
			SummaryDraft: item.SummaryDraft,
			Keywords:     item.Keywords,
			LicenseNote:  item.LicenseNote,
			Meta:         item.Meta,
			CreatedAt:    item.CreatedAt,
			UpdatedAt:    item.UpdatedAt,
		})
	}
	return out
}

func dailyDigestDTOsFromL1(items []l1sqlite.L1DailyDigest) []dailyDigestDTO {
	if items == nil {
		return nil
	}
	out := make([]dailyDigestDTO, 0, len(items))
	for _, item := range items {
		out = append(out, dailyDigestDTO{
			ID:         item.ID,
			DigestDate: item.DigestDate,
			Category:   item.Category,
			DigestSlot: item.DigestSlot,
			NewsIDs:    item.NewsIDs,
			DigestText: item.DigestText,
			CreatedAt:  item.CreatedAt,
			UpdatedAt:  item.UpdatedAt,
		})
	}
	return out
}

func knowledgeItemDTOsFromL1(items []l1sqlite.L1KnowledgeItem) []knowledgeItemDTO {
	if items == nil {
		return nil
	}
	out := make([]knowledgeItemDTO, 0, len(items))
	for _, item := range items {
		out = append(out, knowledgeItemDTO{
			ID:           item.ID,
			StagingID:    item.StagingID,
			Domain:       item.Domain,
			Title:        item.Title,
			SourceID:     item.SourceID,
			SourceURL:    item.SourceURL,
			RawText:      item.RawText,
			RawHash:      item.RawHash,
			SummaryDraft: item.SummaryDraft,
			Keywords:     item.Keywords,
			LicenseNote:  item.LicenseNote,
			Meta:         item.Meta,
			CreatedAt:    item.CreatedAt,
			UpdatedAt:    item.UpdatedAt,
		})
	}
	return out
}
