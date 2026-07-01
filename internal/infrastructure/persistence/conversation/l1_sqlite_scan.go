package conversation

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	domconv "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/conversation"
	_ "github.com/mattn/go-sqlite3"
)

func scanL1EventLogEntries(rows *sql.Rows) ([]L1EventLogEntry, error) {
	var events []L1EventLogEntry
	for rows.Next() {
		var ev L1EventLogEntry
		var payloadJSON string
		if err := rows.Scan(
			&ev.ID,
			&ev.EventType,
			&ev.Namespace,
			&ev.SessionID,
			&ev.ThreadID,
			&payloadJSON,
			&ev.Source,
			&ev.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan l1 event log: %w", err)
		}
		if payloadJSON == "" {
			payloadJSON = "{}"
		}
		if err := json.Unmarshal([]byte(payloadJSON), &ev.Payload); err != nil {
			return nil, fmt.Errorf("failed to unmarshal l1 event payload: %w", err)
		}
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("l1 event log rows error: %w", err)
	}
	return events, nil
}

func scanL1StagingItems(rows *sql.Rows) ([]L1StagingItem, error) {
	var items []L1StagingItem
	for rows.Next() {
		var item L1StagingItem
		var keywordsJSON string
		var metaJSON string
		var publishedAt sql.NullTime
		if err := rows.Scan(
			&item.ID,
			&item.Kind,
			&item.Namespace,
			&item.EventID,
			&item.SourceID,
			&item.SourceURL,
			&item.FetchedAt,
			&publishedAt,
			&item.RawText,
			&item.RawHash,
			&item.SummaryDraft,
			&keywordsJSON,
			&item.LicenseNote,
			&item.ValidationStatus,
			&metaJSON,
			&item.CreatedAt,
			&item.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan l1 staging item: %w", err)
		}
		if publishedAt.Valid {
			item.PublishedAt = publishedAt.Time
		}
		if keywordsJSON == "" {
			keywordsJSON = "[]"
		}
		if err := json.Unmarshal([]byte(keywordsJSON), &item.Keywords); err != nil {
			return nil, fmt.Errorf("failed to unmarshal l1 staging keywords: %w", err)
		}
		if metaJSON == "" {
			metaJSON = "{}"
		}
		if err := json.Unmarshal([]byte(metaJSON), &item.Meta); err != nil {
			return nil, fmt.Errorf("failed to unmarshal l1 staging meta: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("l1 staging rows error: %w", err)
	}
	return items, nil
}

func scanL1SourceRegistryEntries(rows *sql.Rows) ([]L1SourceRegistryEntry, error) {
	var entries []L1SourceRegistryEntry
	for rows.Next() {
		var entry L1SourceRegistryEntry
		var fetchIntervalSec int64
		var enabled int
		var metaJSON string
		var lastFetchedAt sql.NullTime
		if err := rows.Scan(
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
		); err != nil {
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
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("l1 source registry rows error: %w", err)
	}
	return entries, nil
}

func scanL1NewsItems(rows *sql.Rows) ([]L1NewsItem, error) {
	var items []L1NewsItem
	for rows.Next() {
		var item L1NewsItem
		var publishedAt sql.NullTime
		var keywordsJSON string
		var metaJSON string
		if err := rows.Scan(
			&item.ID,
			&item.StagingID,
			&item.Category,
			&item.SourceID,
			&item.SourceURL,
			&publishedAt,
			&item.FetchedAt,
			&item.RawText,
			&item.RawHash,
			&item.SummaryDraft,
			&keywordsJSON,
			&item.LicenseNote,
			&metaJSON,
			&item.CreatedAt,
			&item.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan l1 news item: %w", err)
		}
		if publishedAt.Valid {
			item.PublishedAt = publishedAt.Time
		}
		if keywordsJSON == "" {
			keywordsJSON = "[]"
		}
		if err := json.Unmarshal([]byte(keywordsJSON), &item.Keywords); err != nil {
			return nil, fmt.Errorf("failed to unmarshal l1 news keywords: %w", err)
		}
		if metaJSON == "" {
			metaJSON = "{}"
		}
		if err := json.Unmarshal([]byte(metaJSON), &item.Meta); err != nil {
			return nil, fmt.Errorf("failed to unmarshal l1 news meta: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("l1 news rows error: %w", err)
	}
	return items, nil
}

func scanL1DailyDigests(rows *sql.Rows) ([]L1DailyDigest, error) {
	var digests []L1DailyDigest
	for rows.Next() {
		var digest L1DailyDigest
		var newsIDsJSON string
		if err := rows.Scan(
			&digest.ID,
			&digest.DigestDate,
			&digest.Category,
			&digest.DigestSlot,
			&newsIDsJSON,
			&digest.DigestText,
			&digest.CreatedAt,
			&digest.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan l1 daily digest: %w", err)
		}
		if newsIDsJSON == "" {
			newsIDsJSON = "[]"
		}
		if err := json.Unmarshal([]byte(newsIDsJSON), &digest.NewsIDs); err != nil {
			return nil, fmt.Errorf("failed to unmarshal l1 daily digest news ids: %w", err)
		}
		digests = append(digests, digest)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("l1 daily digest rows error: %w", err)
	}
	return digests, nil
}

func scanL1KnowledgeItems(rows *sql.Rows) ([]L1KnowledgeItem, error) {
	var items []L1KnowledgeItem
	for rows.Next() {
		var item L1KnowledgeItem
		var keywordsJSON string
		var metaJSON string
		if err := rows.Scan(
			&item.ID,
			&item.StagingID,
			&item.Domain,
			&item.Title,
			&item.SourceID,
			&item.SourceURL,
			&item.RawText,
			&item.RawHash,
			&item.SummaryDraft,
			&keywordsJSON,
			&item.LicenseNote,
			&metaJSON,
			&item.CreatedAt,
			&item.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan l1 knowledge item: %w", err)
		}
		if keywordsJSON == "" {
			keywordsJSON = "[]"
		}
		if err := json.Unmarshal([]byte(keywordsJSON), &item.Keywords); err != nil {
			return nil, fmt.Errorf("failed to unmarshal l1 knowledge keywords: %w", err)
		}
		if metaJSON == "" {
			metaJSON = "{}"
		}
		if err := json.Unmarshal([]byte(metaJSON), &item.Meta); err != nil {
			return nil, fmt.Errorf("failed to unmarshal l1 knowledge meta: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("l1 knowledge rows error: %w", err)
	}
	return items, nil
}

func scanL1Events(rows *sql.Rows) ([]L1MemoryEvent, error) {
	var events []L1MemoryEvent
	for rows.Next() {
		ev, err := scanL1EventRows(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, ev...)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("l1 memory rows error: %w", err)
	}
	return events, nil
}

type l1MemoryRow interface {
	Scan(dest ...interface{}) error
}

func scanL1EventRows(row l1MemoryRow) ([]L1MemoryEvent, error) {
	var ev L1MemoryEvent
	var metaJSON string
	var speaker string
	if err := row.Scan(
		&ev.ID,
		&ev.Namespace,
		&ev.SessionID,
		&ev.ThreadID,
		&speaker,
		&ev.Message,
		&metaJSON,
		&ev.MemoryState,
		&ev.Layer,
		&ev.Source,
		&ev.CreatedAt,
		&ev.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("failed to scan l1 memory event: %w", err)
	}
	ev.Speaker = domconv.Speaker(speaker)
	if metaJSON == "" {
		metaJSON = "{}"
	}
	if err := json.Unmarshal([]byte(metaJSON), &ev.Meta); err != nil {
		return nil, fmt.Errorf("failed to unmarshal l1 memory meta: %w", err)
	}
	if err := validateL1MemoryEvent(ev); err != nil {
		return nil, err
	}
	return []L1MemoryEvent{ev}, nil
}
