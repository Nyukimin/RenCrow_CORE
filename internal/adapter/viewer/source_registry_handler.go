package viewer

import (
	"context"
	"encoding/json"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation/l1sqlite"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/sourcefetcher"
	"gopkg.in/yaml.v3"
)

type SourceRegistryStore interface {
	SaveSourceRegistryEntry(ctx context.Context, entry l1sqlite.L1SourceRegistryEntry) (*l1sqlite.L1SourceRegistryEntry, error)
	ListSourceRegistryEntries(ctx context.Context, enabledOnly bool) ([]l1sqlite.L1SourceRegistryEntry, error)
}

type SourceRegistryStagingStore interface {
	RecentStagingItems(ctx context.Context, validationStatus string, limit int) ([]l1sqlite.L1StagingItem, error)
	ValidateStagingItem(ctx context.Context, id string, policy l1sqlite.L1StagingValidationPolicy) (*l1sqlite.L1StagingValidationResult, error)
	PromoteValidatedStagingItemToMemory(ctx context.Context, id string, targetNamespace string, promotedBy string) (*l1sqlite.L1MemoryEvent, error)
	PromoteValidatedStagingItemToNews(ctx context.Context, id string, category string) (*l1sqlite.L1NewsItem, error)
	PromoteValidatedStagingItemToKnowledge(ctx context.Context, id string, domain string) (*l1sqlite.L1KnowledgeItem, error)
	PromoteValidatedStagingItemToDomainGraph(ctx context.Context, id string, domain string, entityType string, entityID string, relationType string, confidence float64) (*l1sqlite.L1DomainGraphAssertion, error)
}

type sourceRegistryEntryDTO struct {
	SourceID         string         `json:"source_id" yaml:"source_id"`
	URL              string         `json:"url" yaml:"url"`
	Kind             string         `json:"kind" yaml:"kind"`
	TrustScore       float64        `json:"trust_score" yaml:"trust_score"`
	FetchIntervalSec int64          `json:"fetch_interval_sec" yaml:"fetch_interval_sec"`
	LicenseNote      string         `json:"license_note" yaml:"license_note"`
	Enabled          bool           `json:"enabled" yaml:"enabled"`
	Meta             map[string]any `json:"meta,omitempty" yaml:"meta,omitempty"`
	LastFetchedAt    string         `json:"last_fetched_at,omitempty" yaml:"last_fetched_at,omitempty"`
	LastStatus       string         `json:"last_status,omitempty" yaml:"last_status,omitempty"`
	LastError        string         `json:"last_error,omitempty" yaml:"last_error,omitempty"`
	CreatedAt        string         `json:"created_at,omitempty" yaml:"created_at,omitempty"`
	UpdatedAt        string         `json:"updated_at,omitempty" yaml:"updated_at,omitempty"`
}

type sourceRegistryPayload struct {
	Entries []sourceRegistryEntryDTO `json:"entries" yaml:"entries"`
}

type sourceRegistryStagingItemDTO struct {
	ID               string         `json:"id"`
	Kind             string         `json:"kind"`
	Namespace        string         `json:"namespace"`
	EventID          string         `json:"event_id"`
	SourceID         string         `json:"source_id"`
	SourceURL        string         `json:"source_url"`
	FetchedAt        string         `json:"fetched_at,omitempty"`
	PublishedAt      string         `json:"published_at,omitempty"`
	RawText          string         `json:"raw_text"`
	SummaryDraft     string         `json:"summary_draft"`
	Keywords         []string       `json:"keywords"`
	LicenseNote      string         `json:"license_note"`
	ValidationStatus string         `json:"validation_status"`
	Meta             map[string]any `json:"meta,omitempty"`
	CreatedAt        string         `json:"created_at,omitempty"`
	UpdatedAt        string         `json:"updated_at,omitempty"`
}

func HandleSourceRegistry(store SourceRegistryStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			http.Error(w, "source registry unavailable", http.StatusServiceUnavailable)
			return
		}
		switch r.Method {
		case http.MethodGet:
			if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("action")), "staging") {
				handleSourceRegistryStagingList(w, r, store)
				return
			}
			handleSourceRegistryList(w, r, store)
		case http.MethodPost:
			switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("action"))) {
			case "run":
				handleSourceRegistryRun(w, r, store)
				return
			case "validate":
				handleSourceRegistryStagingValidate(w, r, store)
				return
			case "promote":
				handleSourceRegistryStagingPromote(w, r, store)
				return
			}
			handleSourceRegistrySave(w, r, store)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func handleSourceRegistryStagingList(w http.ResponseWriter, r *http.Request, store SourceRegistryStore) {
	stagingStore, ok := store.(SourceRegistryStagingStore)
	if !ok {
		http.Error(w, "source registry staging unavailable", http.StatusServiceUnavailable)
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status == "" {
		status = l1sqlite.L1StagingStatusPending
	}
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			http.Error(w, "invalid source registry staging limit", http.StatusBadRequest)
			return
		}
		limit = parsed
	}
	items, err := stagingStore.RecentStagingItems(r.Context(), status, limit)
	if err != nil {
		http.Error(w, "failed to list source registry staging", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"items": sourceRegistryStagingItemsToDTO(items)})
}

func handleSourceRegistryRun(w http.ResponseWriter, r *http.Request, store SourceRegistryStore) {
	runner, ok := store.(interface {
		sourcefetcher.RegistryStore
		sourcefetcher.RegistrySourceLister
	})
	if !ok {
		http.Error(w, "source registry runner unavailable", http.StatusServiceUnavailable)
		return
	}
	sourceID := strings.TrimSpace(r.URL.Query().Get("source_id"))
	if sourceID == "" {
		var payload struct {
			SourceID string `json:"source_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		sourceID = strings.TrimSpace(payload.SourceID)
	}
	result, err := sourcefetcher.RunSource(r.Context(), runner, sourceID, time.Now().UTC(), sourcefetcher.SweepOptions{
		LimitPerSource:    10,
		MinimumTrustScore: 0.5,
	})
	if err != nil {
		http.Error(w, "failed to run source registry source", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"result": result})
}

func handleSourceRegistryStagingValidate(w http.ResponseWriter, r *http.Request, store SourceRegistryStore) {
	stagingStore, ok := store.(SourceRegistryStagingStore)
	if !ok {
		http.Error(w, "source registry staging validator unavailable", http.StatusServiceUnavailable)
		return
	}
	var payload struct {
		ID                         string   `json:"id"`
		MinimumTrustScore          *float64 `json:"minimum_trust_score"`
		AutoPromoteMemoryCandidate bool     `json:"auto_promote_memory_candidate"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid source registry staging validation payload", http.StatusBadRequest)
		return
	}
	id := strings.TrimSpace(payload.ID)
	if id == "" {
		http.Error(w, "staging id is required", http.StatusBadRequest)
		return
	}
	minTrust := 0.5
	if payload.MinimumTrustScore != nil {
		minTrust = *payload.MinimumTrustScore
	}
	trustScores := map[string]float64{}
	if scorer, ok := store.(interface {
		SourceTrustScores(ctx context.Context) (map[string]float64, error)
	}); ok {
		scores, err := scorer.SourceTrustScores(r.Context())
		if err != nil {
			http.Error(w, "failed to read source trust scores", http.StatusInternalServerError)
			return
		}
		trustScores = scores
	}
	result, err := stagingStore.ValidateStagingItem(r.Context(), id, l1sqlite.L1StagingValidationPolicy{
		SourceTrustScores:          trustScores,
		MinimumTrustScore:          minTrust,
		Now:                        time.Now().UTC(),
		AutoPromoteMemoryCandidate: payload.AutoPromoteMemoryCandidate,
	})
	if err != nil {
		http.Error(w, "failed to validate source registry staging item", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"result": result})
}

func handleSourceRegistryStagingPromote(w http.ResponseWriter, r *http.Request, store SourceRegistryStore) {
	stagingStore, ok := store.(SourceRegistryStagingStore)
	if !ok {
		http.Error(w, "source registry staging promoter unavailable", http.StatusServiceUnavailable)
		return
	}
	var payload struct {
		ID              string   `json:"id"`
		Target          string   `json:"target"`
		Category        string   `json:"category"`
		Domain          string   `json:"domain"`
		EntityType      string   `json:"entity_type"`
		EntityID        string   `json:"entity_id"`
		RelationType    string   `json:"relation_type"`
		Confidence      *float64 `json:"confidence"`
		TargetNamespace string   `json:"target_namespace"`
		PromotedBy      string   `json:"promoted_by"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid source registry staging promotion payload", http.StatusBadRequest)
		return
	}
	id := strings.TrimSpace(payload.ID)
	if id == "" {
		http.Error(w, "staging id is required", http.StatusBadRequest)
		return
	}
	switch strings.ToLower(strings.TrimSpace(payload.Target)) {
	case "news":
		category := strings.TrimSpace(payload.Category)
		if category == "" {
			http.Error(w, "news category is required", http.StatusBadRequest)
			return
		}
		item, err := stagingStore.PromoteValidatedStagingItemToNews(r.Context(), id, category)
		if err != nil {
			http.Error(w, "failed to promote source registry staging item to news", http.StatusBadRequest)
			return
		}
		writeSourceRegistryPromotionResponse(w, "news", item)
	case "knowledge":
		domain := strings.TrimSpace(payload.Domain)
		if domain == "" {
			http.Error(w, "knowledge domain is required", http.StatusBadRequest)
			return
		}
		item, err := stagingStore.PromoteValidatedStagingItemToKnowledge(r.Context(), id, domain)
		if err != nil {
			http.Error(w, "failed to promote source registry staging item to knowledge", http.StatusBadRequest)
			return
		}
		writeSourceRegistryPromotionResponse(w, "knowledge", item)
	case "domain_graph":
		domain := strings.TrimSpace(payload.Domain)
		if domain == "" {
			http.Error(w, "domain_graph domain is required", http.StatusBadRequest)
			return
		}
		entityType := strings.TrimSpace(payload.EntityType)
		if entityType == "" {
			http.Error(w, "domain_graph entity_type is required", http.StatusBadRequest)
			return
		}
		confidence := 0.5
		if payload.Confidence != nil {
			confidence = *payload.Confidence
		}
		item, err := stagingStore.PromoteValidatedStagingItemToDomainGraph(r.Context(), id, domain, entityType, payload.EntityID, payload.RelationType, confidence)
		if err != nil {
			http.Error(w, "failed to promote source registry staging item to domain graph", http.StatusBadRequest)
			return
		}
		writeSourceRegistryPromotionResponse(w, "domain_graph", item)
	case "memory":
		namespace := strings.TrimSpace(payload.TargetNamespace)
		if namespace == "" {
			http.Error(w, "memory target_namespace is required", http.StatusBadRequest)
			return
		}
		promotedBy := strings.TrimSpace(payload.PromotedBy)
		if promotedBy == "" {
			promotedBy = "viewer"
		}
		item, err := stagingStore.PromoteValidatedStagingItemToMemory(r.Context(), id, namespace, promotedBy)
		if err != nil {
			http.Error(w, "failed to promote source registry staging item to memory", http.StatusBadRequest)
			return
		}
		writeSourceRegistryPromotionResponse(w, "memory", item)
	default:
		http.Error(w, "promotion target must be news, knowledge, domain_graph, or memory", http.StatusBadRequest)
	}
}

func writeSourceRegistryPromotionResponse(w http.ResponseWriter, target string, item interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"target": target, "item": item})
}

func handleSourceRegistryList(w http.ResponseWriter, r *http.Request, store SourceRegistryStore) {
	enabledOnly := r.URL.Query().Get("enabled_only") == "1" || strings.EqualFold(r.URL.Query().Get("enabled_only"), "true")
	entries, err := store.ListSourceRegistryEntries(r.Context(), enabledOnly)
	if err != nil {
		http.Error(w, "failed to list source registry", http.StatusInternalServerError)
		return
	}
	payload := sourceRegistryPayload{Entries: sourceRegistryEntriesToDTO(entries)}
	if wantsYAML(r) {
		w.Header().Set("Content-Type", "application/x-yaml")
		_ = yaml.NewEncoder(w).Encode(payload)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func handleSourceRegistrySave(w http.ResponseWriter, r *http.Request, store SourceRegistryStore) {
	payload, err := decodeSourceRegistryPayload(r)
	if err != nil {
		http.Error(w, "invalid source registry payload", http.StatusBadRequest)
		return
	}
	saved := make([]sourceRegistryEntryDTO, 0, len(payload.Entries))
	for _, dto := range payload.Entries {
		entry, err := store.SaveSourceRegistryEntry(r.Context(), dto.toEntry())
		if err != nil {
			http.Error(w, "failed to save source registry", http.StatusBadRequest)
			return
		}
		saved = append(saved, sourceRegistryEntryToDTO(*entry))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(sourceRegistryPayload{Entries: saved})
}

func decodeSourceRegistryPayload(r *http.Request) (sourceRegistryPayload, error) {
	var payload sourceRegistryPayload
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return payload, err
	}
	if wantsYAML(r) {
		if err := yaml.Unmarshal(body, &payload); err != nil {
			return payload, err
		}
		return payload, nil
	}
	if err := json.Unmarshal(body, &payload); err == nil && len(payload.Entries) > 0 {
		return payload, nil
	}
	var single sourceRegistryEntryDTO
	if err := json.Unmarshal(body, &single); err != nil {
		return payload, err
	}
	payload.Entries = []sourceRegistryEntryDTO{single}
	return payload, nil
}

func wantsYAML(r *http.Request) bool {
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "yaml" || format == "yml" {
		return true
	}
	ct := strings.ToLower(r.Header.Get("Content-Type"))
	return strings.Contains(ct, "yaml") || strings.Contains(ct, "yml")
}

func sourceRegistryEntriesToDTO(entries []l1sqlite.L1SourceRegistryEntry) []sourceRegistryEntryDTO {
	out := make([]sourceRegistryEntryDTO, 0, len(entries))
	for _, entry := range entries {
		out = append(out, sourceRegistryEntryToDTO(entry))
	}
	return out
}

func sourceRegistryStagingItemsToDTO(items []l1sqlite.L1StagingItem) []sourceRegistryStagingItemDTO {
	out := make([]sourceRegistryStagingItemDTO, 0, len(items))
	for _, item := range items {
		out = append(out, sourceRegistryStagingItemToDTO(item))
	}
	return out
}

func sourceRegistryStagingItemToDTO(item l1sqlite.L1StagingItem) sourceRegistryStagingItemDTO {
	dto := sourceRegistryStagingItemDTO{
		ID:               item.ID,
		Kind:             item.Kind,
		Namespace:        item.Namespace,
		EventID:          item.EventID,
		SourceID:         item.SourceID,
		SourceURL:        item.SourceURL,
		RawText:          item.RawText,
		SummaryDraft:     item.SummaryDraft,
		Keywords:         item.Keywords,
		LicenseNote:      item.LicenseNote,
		ValidationStatus: item.ValidationStatus,
		Meta:             item.Meta,
	}
	if !item.FetchedAt.IsZero() {
		dto.FetchedAt = item.FetchedAt.UTC().Format(time.RFC3339)
	}
	if !item.PublishedAt.IsZero() {
		dto.PublishedAt = item.PublishedAt.UTC().Format(time.RFC3339)
	}
	if !item.CreatedAt.IsZero() {
		dto.CreatedAt = item.CreatedAt.UTC().Format(time.RFC3339)
	}
	if !item.UpdatedAt.IsZero() {
		dto.UpdatedAt = item.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return dto
}

func sourceRegistryEntryToDTO(entry l1sqlite.L1SourceRegistryEntry) sourceRegistryEntryDTO {
	dto := sourceRegistryEntryDTO{
		SourceID:         entry.SourceID,
		URL:              entry.URL,
		Kind:             entry.Kind,
		TrustScore:       entry.TrustScore,
		FetchIntervalSec: int64(entry.FetchInterval.Seconds()),
		LicenseNote:      entry.LicenseNote,
		Enabled:          entry.Enabled,
		Meta:             entry.Meta,
		LastStatus:       entry.LastStatus,
		LastError:        entry.LastError,
	}
	if !entry.LastFetchedAt.IsZero() {
		dto.LastFetchedAt = entry.LastFetchedAt.UTC().Format(time.RFC3339)
	}
	if !entry.CreatedAt.IsZero() {
		dto.CreatedAt = entry.CreatedAt.UTC().Format(time.RFC3339)
	}
	if !entry.UpdatedAt.IsZero() {
		dto.UpdatedAt = entry.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return dto
}

func (dto sourceRegistryEntryDTO) toEntry() l1sqlite.L1SourceRegistryEntry {
	return l1sqlite.L1SourceRegistryEntry{
		SourceID:      dto.SourceID,
		URL:           dto.URL,
		Kind:          dto.Kind,
		TrustScore:    dto.TrustScore,
		FetchInterval: time.Duration(dto.FetchIntervalSec) * time.Second,
		LicenseNote:   dto.LicenseNote,
		Enabled:       dto.Enabled,
		Meta:          dto.Meta,
	}
}
