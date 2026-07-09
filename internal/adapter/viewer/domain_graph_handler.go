package viewer

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation/l1sqlite"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type DomainGraphAssertionStore interface {
	DomainGraphAssertions(ctx context.Context, q l1sqlite.DomainGraphAssertionQuery) (int, []l1sqlite.L1DomainGraphAssertion, error)
}

type domainGraphAssertionsResponse struct {
	Items  []domainGraphAssertionDTO `json:"items"`
	Limit  int                       `json:"limit"`
	Offset int                       `json:"offset"`
	Total  int                       `json:"total"`
}

type domainGraphAssertionDTO struct {
	ID               string         `json:"id"`
	StagingID        string         `json:"staging_id"`
	Domain           string         `json:"domain"`
	EntityType       string         `json:"entity_type"`
	EntityID         string         `json:"entity_id,omitempty"`
	RelationType     string         `json:"relation_type,omitempty"`
	SourceID         string         `json:"source_id"`
	SourceURL        string         `json:"source_url,omitempty"`
	RawHash          string         `json:"raw_hash"`
	Summary          string         `json:"summary"`
	Confidence       float64        `json:"confidence"`
	ValidationStatus string         `json:"validation_status"`
	Evidence         map[string]any `json:"evidence"`
	CreatedAt        string         `json:"created_at"`
	UpdatedAt        string         `json:"updated_at"`
}

func HandleDomainGraphAssertions(store DomainGraphAssertionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "domain graph unavailable", http.StatusServiceUnavailable)
			return
		}
		q, err := parseDomainGraphAssertionQuery(r)
		if err != nil {
			http.Error(w, "invalid domain graph assertion query", http.StatusBadRequest)
			return
		}
		total, items, err := store.DomainGraphAssertions(r.Context(), q)
		if err != nil {
			http.Error(w, "failed to load domain graph assertions", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(domainGraphAssertionsResponse{
			Items:  domainGraphAssertionsToDTO(items),
			Limit:  q.Limit,
			Offset: q.Offset,
			Total:  total,
		})
	}
}

func parseDomainGraphAssertionQuery(r *http.Request) (l1sqlite.DomainGraphAssertionQuery, error) {
	limit, err := parseViewerLimit(r.URL.Query().Get("limit"), 50, 200)
	if err != nil {
		return l1sqlite.DomainGraphAssertionQuery{}, err
	}
	offset := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("offset")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return l1sqlite.DomainGraphAssertionQuery{}, err
		}
		if n < 0 {
			return l1sqlite.DomainGraphAssertionQuery{}, errors.New("negative offset")
		}
		offset = n
	}
	status := strings.TrimSpace(r.URL.Query().Get("validation_status"))
	if status != "" && status != "pending" && status != "validated" && status != "rejected" {
		return l1sqlite.DomainGraphAssertionQuery{}, errors.New("invalid validation_status")
	}
	return l1sqlite.DomainGraphAssertionQuery{
		Domain:           r.URL.Query().Get("domain"),
		EntityType:       r.URL.Query().Get("entity_type"),
		EntityID:         r.URL.Query().Get("entity_id"),
		RelationType:     r.URL.Query().Get("relation_type"),
		SourceID:         r.URL.Query().Get("source_id"),
		ValidationStatus: status,
		Limit:            limit,
		Offset:           offset,
	}, nil
}

func domainGraphAssertionsToDTO(items []l1sqlite.L1DomainGraphAssertion) []domainGraphAssertionDTO {
	out := make([]domainGraphAssertionDTO, 0, len(items))
	for _, item := range items {
		out = append(out, domainGraphAssertionToDTO(item))
	}
	return out
}

func domainGraphAssertionToDTO(item l1sqlite.L1DomainGraphAssertion) domainGraphAssertionDTO {
	dto := domainGraphAssertionDTO{
		ID:               item.ID,
		StagingID:        item.StagingID,
		Domain:           item.Domain,
		EntityType:       item.EntityType,
		EntityID:         item.EntityID,
		RelationType:     item.RelationType,
		SourceID:         item.SourceID,
		SourceURL:        item.SourceURL,
		RawHash:          item.RawHash,
		Summary:          item.Summary,
		Confidence:       item.Confidence,
		ValidationStatus: item.ValidationStatus,
		Evidence:         map[string]any{},
	}
	if item.Evidence != nil {
		dto.Evidence = item.Evidence
	}
	if !item.CreatedAt.IsZero() {
		dto.CreatedAt = item.CreatedAt.UTC().Format(time.RFC3339)
	}
	if !item.UpdatedAt.IsZero() {
		dto.UpdatedAt = item.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return dto
}
