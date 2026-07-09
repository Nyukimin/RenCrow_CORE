package knowledge

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"io"
	"strings"
	"time"
)

type StagingStore interface {
	SaveStagingItem(ctx context.Context, item l1sqlite.L1StagingItem) (*l1sqlite.L1StagingItem, error)
}

type ImportOptions struct {
	Now func() time.Time
}

type ImportResult struct {
	Imported int
}

type coreRecord struct {
	ID          string                 `json:"id"`
	Domain      string                 `json:"domain"`
	Title       string                 `json:"title"`
	Keywords    []string               `json:"keywords"`
	Summary     string                 `json:"summary"`
	RawText     string                 `json:"raw_text"`
	SourceID    string                 `json:"source_id"`
	SourceURL   string                 `json:"source_url"`
	LicenseNote string                 `json:"license_note"`
	Meta        map[string]interface{} `json:"meta"`
}

func ImportKnowledgeCoreJSONL(ctx context.Context, store StagingStore, r io.Reader, opts ImportOptions) (ImportResult, error) {
	if store == nil {
		return ImportResult{}, fmt.Errorf("knowledge staging store is required")
	}
	now := time.Now().UTC()
	if opts.Now != nil {
		now = opts.Now().UTC()
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var result ImportResult
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec coreRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			return result, fmt.Errorf("invalid knowledge core jsonl at line %d: %w", lineNo, err)
		}
		var full map[string]interface{}
		if err := json.Unmarshal([]byte(line), &full); err != nil {
			return result, fmt.Errorf("invalid knowledge core metadata at line %d: %w", lineNo, err)
		}
		item, err := stagingItemFromCoreRecord(rec, full, now)
		if err != nil {
			return result, fmt.Errorf("invalid knowledge core record at line %d: %w", lineNo, err)
		}
		if _, err := store.SaveStagingItem(ctx, item); err != nil {
			return result, fmt.Errorf("failed to save knowledge core staging at line %d: %w", lineNo, err)
		}
		result.Imported++
	}
	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("failed to read knowledge core jsonl: %w", err)
	}
	return result, nil
}

func stagingItemFromCoreRecord(rec coreRecord, full map[string]interface{}, now time.Time) (l1sqlite.L1StagingItem, error) {
	rec.Domain = strings.TrimSpace(rec.Domain)
	rec.ID = strings.TrimSpace(rec.ID)
	rec.Title = strings.TrimSpace(rec.Title)
	if rec.Domain == "" {
		return l1sqlite.L1StagingItem{}, fmt.Errorf("domain is required")
	}
	if rec.ID == "" {
		if rec.Title == "" {
			return l1sqlite.L1StagingItem{}, fmt.Errorf("id or title is required")
		}
		rec.ID = rec.Domain + ":" + strings.ToLower(strings.ReplaceAll(rec.Title, " ", "_"))
	}
	sourceID := strings.TrimSpace(rec.SourceID)
	if sourceID == "" {
		sourceID = "knowledge_core_import"
	}
	rawText := strings.TrimSpace(rec.RawText)
	if rawText == "" {
		rawText = strings.TrimSpace(strings.Join([]string{rec.Title, rec.Summary}, "\n"))
	}
	if rawText == "" {
		return l1sqlite.L1StagingItem{}, fmt.Errorf("raw_text or summary is required")
	}
	meta := map[string]interface{}{}
	if rec.Meta != nil {
		for k, v := range rec.Meta {
			meta[k] = v
		}
	}
	for k, v := range full {
		switch k {
		case "id", "domain", "keywords", "summary", "raw_text", "source_id", "source_url", "license_note", "meta":
			continue
		default:
			meta[k] = v
		}
	}
	if rec.Title != "" {
		meta["title"] = rec.Title
	}
	meta["domain"] = rec.Domain
	return l1sqlite.L1StagingItem{
		Kind:             l1sqlite.L1StagingKindExternalFetch,
		Namespace:        "kb:" + rec.Domain,
		EventID:          rec.ID,
		SourceID:         sourceID,
		SourceURL:        strings.TrimSpace(rec.SourceURL),
		FetchedAt:        now,
		RawText:          rawText,
		SummaryDraft:     strings.TrimSpace(rec.Summary),
		Keywords:         append([]string(nil), rec.Keywords...),
		LicenseNote:      strings.TrimSpace(rec.LicenseNote),
		ValidationStatus: l1sqlite.L1StagingStatusPending,
		Meta:             meta,
	}, nil
}
