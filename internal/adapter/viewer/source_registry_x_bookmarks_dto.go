package viewer

import (
	"fmt"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

func sourceRegistryXBookmarkPageToDTO(page *l1sqlite.L1XBookmarkViewPage) sourceRegistryXBookmarkPageDTO {
	items := make([]sourceRegistryXBookmarkItemDTO, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, sourceRegistryXBookmarkItemToDTO(item))
	}
	return sourceRegistryXBookmarkPageDTO{
		Items:  items,
		Total:  page.Total,
		Limit:  page.Limit,
		Offset: page.Offset,
		Summary: sourceRegistryXBookmarkSummaryDTO{
			Total: page.Summary.Total, NeedsReview: page.Summary.NeedsReview,
			MajorCounts: page.Summary.MajorCounts, MinorCounts: page.Summary.MinorCounts,
		},
	}
}

func sourceRegistryXBookmarkItemToDTO(item l1sqlite.L1StagingItem) sourceRegistryXBookmarkItemDTO {
	title := xBookmarkMetaString(item.Meta, "title")
	if title == "" {
		title = xBookmarkFirstLine(item.SummaryDraft)
	}
	if title == "" {
		title = xBookmarkFirstLine(item.RawText)
	}
	if title == "" {
		title = "無題"
	}
	classification, _ := item.Meta["classification"].(map[string]interface{})
	author, _ := item.Meta["author"].(map[string]interface{})
	return sourceRegistryXBookmarkItemDTO{
		ID: item.ID, Title: title, SourceURL: item.SourceURL, RawText: item.RawText,
		ValidationStatus: item.ValidationStatus,
		NeedsReview:      xBookmarkMapBool(classification, "needs_review"),
		Classification:   xBookmarkMapString(classification, "method"),
		UseCaseTags:      xBookmarkTagDTOs(item.Meta["use_case_tags"]),
		AuthorName:       xBookmarkMapString(author, "name"),
		AuthorUsername:   xBookmarkMapString(author, "username"),
		MediaCount:       xBookmarkSliceLength(item.Meta["media"]),
		ReferenceCount:   xBookmarkSliceLength(item.Meta["references"]),
		References:       xBookmarkReferenceDTOs(item.Meta["references"]),
		UpdatedAt:        xBookmarkTime(item.UpdatedAt),
	}
}

func xBookmarkReferenceDTOs(raw interface{}) []sourceRegistryXBookmarkReferenceDTO {
	values, ok := raw.([]interface{})
	if !ok {
		return []sourceRegistryXBookmarkReferenceDTO{}
	}
	result := make([]sourceRegistryXBookmarkReferenceDTO, 0, len(values))
	for _, value := range values {
		reference, ok := value.(map[string]interface{})
		if !ok {
			continue
		}
		author, _ := reference["author"].(map[string]interface{})
		result = append(result, sourceRegistryXBookmarkReferenceDTO{
			Kind:            xBookmarkMapString(reference, "kind"),
			URL:             xBookmarkMapString(reference, "url"),
			ResolvedURL:     xBookmarkMapString(reference, "resolved_url"),
			StatusURL:       xBookmarkMapString(reference, "status_url"),
			CaptureStatus:   xBookmarkMapString(reference, "capture_status"),
			DisplayText:     xBookmarkMapString(reference, "display_text"),
			PreviewText:     xBookmarkMapString(reference, "preview_text"),
			PageTitle:       xBookmarkMapString(reference, "page_title"),
			PageDescription: xBookmarkMapString(reference, "page_description"),
			BodyText:        xBookmarkMapString(reference, "body_text"),
			BodyCharCount:   xBookmarkMapInt(reference, "body_char_count"),
			BodyTruncated:   xBookmarkMapBool(reference, "body_truncated"),
			FetchedAt:       xBookmarkMapString(reference, "fetched_at"),
			FetchError:      xBookmarkMapString(reference, "fetch_error"),
			Text:            xBookmarkMapString(reference, "text"),
			AuthorName:      xBookmarkMapString(author, "name"),
			AuthorUsername:  xBookmarkMapString(author, "username"),
		})
	}
	return result
}

func xBookmarkTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func xBookmarkTagDTOs(raw interface{}) []sourceRegistryXBookmarkTagDTO {
	values, ok := raw.([]interface{})
	if !ok {
		return []sourceRegistryXBookmarkTagDTO{}
	}
	result := make([]sourceRegistryXBookmarkTagDTO, 0, len(values))
	for _, value := range values {
		tag, ok := value.(map[string]interface{})
		if !ok {
			continue
		}
		major := xBookmarkMapString(tag, "major")
		minor := xBookmarkMapString(tag, "minor")
		if major == "" || minor == "" {
			continue
		}
		result = append(result, sourceRegistryXBookmarkTagDTO{
			Major: major, Minor: minor, Confidence: xBookmarkMapFloat(tag, "confidence"),
			Method: xBookmarkMapString(tag, "method"), Evidence: xBookmarkStringSlice(tag["evidence"]),
		})
	}
	return result
}

func xBookmarkMetaString(meta map[string]interface{}, key string) string {
	if meta == nil {
		return ""
	}
	return xBookmarkMapString(meta, key)
}

func xBookmarkMapString(values map[string]interface{}, key string) string {
	if values == nil {
		return ""
	}
	value, ok := values[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func xBookmarkMapBool(values map[string]interface{}, key string) bool {
	if values == nil {
		return false
	}
	value, _ := values[key].(bool)
	return value
}

func xBookmarkMapFloat(values map[string]interface{}, key string) float64 {
	if values == nil {
		return 0
	}
	switch value := values[key].(type) {
	case float64:
		return value
	case float32:
		return float64(value)
	case int:
		return float64(value)
	default:
		return 0
	}
}

func xBookmarkMapInt(values map[string]interface{}, key string) int {
	if values == nil {
		return 0
	}
	switch value := values[key].(type) {
	case float64:
		return int(value)
	case float32:
		return int(value)
	case int:
		return value
	default:
		return 0
	}
}

func xBookmarkStringSlice(raw interface{}) []string {
	values, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		text := strings.TrimSpace(fmt.Sprint(value))
		if text != "" {
			result = append(result, text)
		}
	}
	return result
}

func xBookmarkSliceLength(raw interface{}) int {
	values, ok := raw.([]interface{})
	if !ok {
		return 0
	}
	return len(values)
}

func xBookmarkFirstLine(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if index := strings.IndexByte(value, '\n'); index >= 0 {
		value = value[:index]
	}
	return strings.TrimSpace(strings.TrimLeft(value, "# "))
}
