package l1sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func (s *L1SQLiteStore) SaveSourceRegistryEntry(ctx context.Context, entry L1SourceRegistryEntry) (*L1SourceRegistryEntry, error) {
	entry.SourceID = strings.TrimSpace(entry.SourceID)
	entry.URL = strings.TrimSpace(entry.URL)
	entry.Kind = strings.TrimSpace(entry.Kind)
	entry.LicenseNote = strings.TrimSpace(entry.LicenseNote)
	if entry.SourceID == "" {
		return nil, errors.New("l1 source registry source_id is required")
	}
	if err := validateL1SourceKind(entry.Kind); err != nil {
		return nil, err
	}
	if err := validateOptionalSourceURL(entry.URL); err != nil {
		return nil, err
	}
	if entry.URL == "" {
		return nil, errors.New("l1 source registry url is required")
	}
	if entry.TrustScore < 0 || entry.TrustScore > 1 {
		return nil, fmt.Errorf("l1 source registry trust_score must be between 0 and 1: %f", entry.TrustScore)
	}
	if entry.FetchInterval <= 0 {
		return nil, errors.New("l1 source registry fetch_interval must be positive")
	}
	if entry.LicenseNote == "" {
		return nil, errors.New("l1 source registry license_note is required")
	}
	if entry.Meta == nil {
		entry.Meta = map[string]interface{}{}
	}
	metaJSON, err := json.Marshal(entry.Meta)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal l1 source registry meta: %w", err)
	}
	now := time.Now().UTC()
	entry.CreatedAt = now
	entry.UpdatedAt = now
	enabled := 0
	if entry.Enabled {
		enabled = 1
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO l1_source_registry (
	source_id, url, kind, trust_score, fetch_interval_sec, license_note,
	enabled, meta_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(source_id) DO UPDATE SET
	url = excluded.url,
	kind = excluded.kind,
	trust_score = excluded.trust_score,
	fetch_interval_sec = excluded.fetch_interval_sec,
	license_note = excluded.license_note,
	enabled = excluded.enabled,
	meta_json = excluded.meta_json,
	updated_at = excluded.updated_at
`, entry.SourceID, entry.URL, entry.Kind, entry.TrustScore, int64(entry.FetchInterval.Seconds()), entry.LicenseNote,
		enabled, string(metaJSON), entry.CreatedAt, entry.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to save l1 source registry entry: %w", err)
	}
	registryNamespace, err := BuildL1Namespace(NamespaceKindKnowledge, "source_registry")
	if err != nil {
		return nil, err
	}
	if _, err := s.AppendEvent(ctx, "source_registry.saved", registryNamespace, "", 0, map[string]interface{}{
		"source_id":      entry.SourceID,
		"url":            entry.URL,
		"kind":           entry.Kind,
		"trust_score":    entry.TrustScore,
		"enabled":        entry.Enabled,
		"license_note":   entry.LicenseNote,
		"fetch_interval": entry.FetchInterval.String(),
	}, "source_registry"); err != nil {
		return nil, fmt.Errorf("failed to append l1 source registry event log: %w", err)
	}
	return &entry, nil
}

func (s *L1SQLiteStore) ListSourceRegistryEntries(ctx context.Context, enabledOnly bool) ([]L1SourceRegistryEntry, error) {
	query := `
SELECT source_id, url, kind, trust_score, fetch_interval_sec, license_note,
       enabled, meta_json, last_fetched_at, last_status, last_error, created_at, updated_at
FROM l1_source_registry
`
	var args []interface{}
	if enabledOnly {
		query += "WHERE enabled = ?\n"
		args = append(args, 1)
	}
	query += "ORDER BY source_id ASC"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query l1 source registry: %w", err)
	}
	defer rows.Close()
	return scanL1SourceRegistryEntries(rows)
}

func (s *L1SQLiteStore) DueSourceRegistryEntries(ctx context.Context, now time.Time) ([]L1SourceRegistryEntry, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT source_id, url, kind, trust_score, fetch_interval_sec, license_note,
       enabled, meta_json, last_fetched_at, last_status, last_error, created_at, updated_at
FROM l1_source_registry
WHERE enabled = 1
  AND (last_fetched_at IS NULL OR datetime(last_fetched_at, '+' || fetch_interval_sec || ' seconds') <= datetime(?))
ORDER BY source_id ASC
`, now.UTC())
	if err != nil {
		return nil, fmt.Errorf("failed to query due l1 source registry: %w", err)
	}
	defer rows.Close()
	return scanL1SourceRegistryEntries(rows)
}

func (s *L1SQLiteStore) MarkSourceRegistryFetched(ctx context.Context, sourceID string, fetchedAt time.Time, status string, lastError string) error {
	sourceID = strings.TrimSpace(sourceID)
	status = strings.TrimSpace(status)
	lastError = strings.TrimSpace(lastError)
	if sourceID == "" {
		return errors.New("l1 source registry source_id is required")
	}
	if err := validateL1SourceFetchStatus(status); err != nil {
		return err
	}
	if status == L1SourceFetchStatusError && lastError == "" {
		return errors.New("l1 source registry last_error is required when fetch status is error")
	}
	if fetchedAt.IsZero() {
		fetchedAt = time.Now().UTC()
	}
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `
UPDATE l1_source_registry
SET last_fetched_at = ?, last_status = ?, last_error = ?, updated_at = ?
WHERE source_id = ?
`, fetchedAt.UTC(), status, lastError, now, sourceID)
	if err != nil {
		return fmt.Errorf("failed to update l1 source registry fetch status: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to inspect l1 source registry fetch status update: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("l1 source registry entry not found: %s", sourceID)
	}
	registryNamespace, err := BuildL1Namespace(NamespaceKindKnowledge, "source_registry")
	if err != nil {
		return err
	}
	_, err = s.AppendEvent(ctx, "source_registry.fetched", registryNamespace, "", 0, map[string]interface{}{
		"source_id":  sourceID,
		"status":     status,
		"last_error": lastError,
		"fetched_at": fetchedAt.UTC().Format(time.RFC3339),
	}, "source_registry")
	if err != nil {
		return fmt.Errorf("failed to append l1 source registry fetch event: %w", err)
	}
	return nil
}

func (s *L1SQLiteStore) SourceTrustScores(ctx context.Context) (map[string]float64, error) {
	entries, err := s.ListSourceRegistryEntries(ctx, true)
	if err != nil {
		return nil, err
	}
	scores := make(map[string]float64, len(entries))
	for _, entry := range entries {
		scores[entry.SourceID] = entry.TrustScore
	}
	return scores, nil
}

func (s *L1SQLiteStore) StageSourceRegistryFetch(ctx context.Context, sourceID string, payload L1SourceFetchPayload) (*L1StagingItem, error) {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return nil, errors.New("l1 source registry source_id is required")
	}
	entry, err := s.sourceRegistryEntryByID(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	if !entry.Enabled {
		return nil, fmt.Errorf("l1 source registry entry is disabled: %s", sourceID)
	}
	namespace := stringMeta(payload.Meta, "namespace")
	if namespace == "" {
		namespace = stringMeta(entry.Meta, "namespace")
	}
	if namespace == "" {
		namespace = "kb:news"
	}
	sourceURL := strings.TrimSpace(payload.SourceURL)
	if sourceURL == "" {
		sourceURL = entry.URL
	}
	eventID := strings.TrimSpace(payload.EventID)
	if eventID == "" {
		eventID = defaultSourceFetchEventID(entry.SourceID, sourceURL, payload.PublishedAt, payload.FetchedAt, payload.RawText)
	}
	meta := mergeStringAnyMaps(entry.Meta, payload.Meta)
	meta["source_kind"] = entry.Kind
	meta["source_registry_url"] = entry.URL
	return s.SaveStagingItem(ctx, L1StagingItem{
		Kind:         L1StagingKindExternalFetch,
		Namespace:    namespace,
		EventID:      eventID,
		SourceID:     entry.SourceID,
		SourceURL:    sourceURL,
		FetchedAt:    payload.FetchedAt,
		PublishedAt:  payload.PublishedAt,
		RawText:      payload.RawText,
		SummaryDraft: payload.SummaryDraft,
		Keywords:     payload.Keywords,
		LicenseNote:  entry.LicenseNote,
		Meta:         meta,
	})
}

func (s *L1SQLiteStore) sourceRegistryEntryByID(ctx context.Context, sourceID string) (*L1SourceRegistryEntry, error) {
	var entry L1SourceRegistryEntry
	var fetchIntervalSec int64
	var enabled int
	var metaJSON string
	var lastFetchedAt sql.NullTime
	err := s.db.QueryRowContext(ctx, `
SELECT source_id, url, kind, trust_score, fetch_interval_sec, license_note,
       enabled, meta_json, last_fetched_at, last_status, last_error, created_at, updated_at
FROM l1_source_registry
WHERE source_id = ?
`, sourceID).Scan(
		&entry.SourceID,
		&entry.URL,
		&entry.Kind,
		&entry.TrustScore,
		&fetchIntervalSec,
		&entry.LicenseNote,
		&enabled,
		&metaJSON,
		&lastFetchedAt,
		&entry.LastStatus,
		&entry.LastError,
		&entry.CreatedAt,
		&entry.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("l1 source registry entry not found: %s", sourceID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to scan l1 source registry entry: %w", err)
	}
	entry.FetchInterval = time.Duration(fetchIntervalSec) * time.Second
	entry.Enabled = enabled != 0
	if lastFetchedAt.Valid {
		entry.LastFetchedAt = lastFetchedAt.Time
	}
	if metaJSON == "" {
		metaJSON = "{}"
	}
	if err := json.Unmarshal([]byte(metaJSON), &entry.Meta); err != nil {
		return nil, fmt.Errorf("failed to unmarshal l1 source registry meta: %w", err)
	}
	return &entry, nil
}

func defaultSourceFetchEventID(sourceID, sourceURL string, publishedAt, fetchedAt time.Time, rawText string) string {
	t := publishedAt
	if t.IsZero() {
		t = fetchedAt
	}
	stamp := "undated"
	if !t.IsZero() {
		stamp = t.UTC().Format("20060102T150405Z")
	}
	sum := sha256.Sum256([]byte(sourceID + "\x00" + sourceURL + "\x00" + stamp + "\x00" + rawText))
	return fmt.Sprintf("%s:%s:%s", sourceID, stamp, hex.EncodeToString(sum[:])[:12])
}
