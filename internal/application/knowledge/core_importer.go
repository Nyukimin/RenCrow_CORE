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

type stagingItemLookup interface {
	FindStagingItemByNamespaceEventID(ctx context.Context, namespace, eventID string) (l1sqlite.L1StagingItem, bool, error)
}

type ImportOptions struct {
	Now            func() time.Time
	LinkSummarizer *ExternalLinkSummarizer
}

type ImportResult struct {
	Imported      int
	Items         []ImportedItem
	LinkSummaries LinkSummaryCounts `json:"link_summaries"`
}

type ImportedItem struct {
	EventID   string
	StagingID string
	Domain    string
	Status    string
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
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now().UTC()
	if opts.Now != nil {
		now = opts.Now().UTC()
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var result ImportResult
	var lookup stagingItemLookup
	if opts.LinkSummarizer != nil {
		lookup, _ = store.(stagingItemLookup)
	}
	linkSummaryCache := map[string]map[string]interface{}{}
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		if err := ctx.Err(); err != nil {
			return result, err
		}
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
		stripIncomingExternalLinkSummaries(&item)
		var existing *l1sqlite.L1StagingItem
		if lookup != nil {
			foundItem, found, lookupErr := lookup.FindStagingItemByNamespaceEventID(ctx, item.Namespace, item.EventID)
			if lookupErr != nil {
				return result, fmt.Errorf("failed to lookup existing knowledge core staging at line %d: %w", lineNo, lookupErr)
			}
			if found {
				existing = &foundItem
				preserveExistingStagingState(&item, foundItem)
				if opts.LinkSummarizer != nil {
					preserveExistingExternalCaptures(&item, foundItem)
				}
			}
		}
		if opts.LinkSummarizer != nil {
			counts, summaryErr := opts.LinkSummarizer.applyExternalLinkSummaries(ctx, &item, existing, now, linkSummaryCache)
			result.LinkSummaries.Ready += counts.Ready
			result.LinkSummaries.Blocked += counts.Blocked
			result.LinkSummaries.Failed += counts.Failed
			result.LinkSummaries.Reused += counts.Reused
			if existing != nil {
				mergeExistingStagingMetadata(&item, *existing)
			}
			if summaryErr != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return result, ctxErr
				}
				if _, saveErr := store.SaveStagingItem(ctx, item); saveErr != nil {
					return result, fmt.Errorf("failed to save knowledge core staging at line %d: %w", lineNo, saveErr)
				}
				return result, summaryErr
			}
		}
		saved, err := store.SaveStagingItem(ctx, item)
		if err != nil {
			return result, fmt.Errorf("failed to save knowledge core staging at line %d: %w", lineNo, err)
		}
		if saved == nil {
			return result, fmt.Errorf("failed to save knowledge core staging at line %d: store returned no item", lineNo)
		}
		result.Imported++
		result.Items = append(result.Items, ImportedItem{
			EventID:   saved.EventID,
			StagingID: saved.ID,
			Domain:    strings.TrimPrefix(saved.Namespace, "kb:"),
			Status:    saved.ValidationStatus,
		})
	}
	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("failed to read knowledge core jsonl: %w", err)
	}
	return result, nil
}

func preserveExistingStagingState(item *l1sqlite.L1StagingItem, existing l1sqlite.L1StagingItem) {
	if item == nil {
		return
	}
	if existing.ID != "" {
		item.ID = existing.ID
	}
	if existing.ValidationStatus != "" {
		item.ValidationStatus = existing.ValidationStatus
	}
}

func mergeExistingStagingMetadata(item *l1sqlite.L1StagingItem, existing l1sqlite.L1StagingItem) {
	if item == nil || existing.Meta == nil {
		return
	}
	merged := cloneExternalLinkMap(existing.Meta)
	for key, value := range item.Meta {
		merged[key] = value
	}
	if oldReferences, ok := externalLinkReferenceMaps(existing.Meta["references"]); ok {
		if newReferences, present := externalLinkReferenceMaps(item.Meta["references"]); present {
			merged["references"] = externalLinkReferenceValues(mergeExternalLinkReferences(oldReferences, newReferences))
		} else {
			merged["references"] = externalLinkReferenceValues(oldReferences)
		}
	}
	item.Meta = merged
}

func mergeExternalLinkReferences(existing, incoming []map[string]interface{}) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(existing)+len(incoming))
	used := make([]bool, len(existing))
	for _, current := range incoming {
		merged := cloneExternalLinkMap(current)
		key := externalLinkReferenceKey(current)
		if key != "" {
			for index, previous := range existing {
				if used[index] || externalLinkReferenceKey(previous) != key {
					continue
				}
				merged = mergeExternalLinkReference(previous, merged)
				used[index] = true
				break
			}
		}
		result = append(result, merged)
	}
	for index, previous := range existing {
		if !used[index] {
			result = append(result, cloneExternalLinkMap(previous))
		}
	}
	return result
}

func externalLinkReferenceKey(reference map[string]interface{}) string {
	kind := strings.TrimSpace(externalLinkString(reference, "kind"))
	if kind == "x_post" {
		identifier := strings.TrimSpace(externalLinkString(reference, "tweet_id"))
		if identifier == "" {
			identifier = strings.TrimSpace(externalLinkString(reference, "status_url"))
		}
		if identifier == "" {
			return ""
		}
		return kind + "\x00" + identifier
	}
	if kind == "external_url" {
		url := strings.TrimSpace(externalLinkString(reference, "url"))
		if url == "" {
			return ""
		}
		return kind + "\x00" + url
	}
	url := strings.TrimSpace(externalLinkString(reference, "url"))
	if kind == "" && url == "" {
		return ""
	}
	return kind + "\x00" + url
}

func stagingItemFromCoreRecord(rec coreRecord, full map[string]interface{}, now time.Time) (l1sqlite.L1StagingItem, error) {
	rec.Domain = strings.TrimSpace(rec.Domain)
	rec.ID = strings.TrimSpace(rec.ID)
	rec.Title = strings.TrimSpace(rec.Title)
	if rec.Domain == "" {
		rec.Domain = "general"
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
