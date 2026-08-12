package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

type mioPersonRelatedCatalogLookup struct {
	Name     string
	Category string
}

const mioPersonRelatedCatalogContextMaxBytes = 12 * 1024

type mioPersonRelatedCatalogConversationProjection struct {
	Items           []mioPersonRelatedCatalogConversationItem `json:"items"`
	SummaryCoverage mioPersonRelatedCatalogSummaryCoverage    `json:"summary_coverage"`
}

type mioPersonRelatedCatalogConversationItem struct {
	DisplayName      string `json:"display_name"`
	NameOriginal     string `json:"name_original"`
	NameJA           string `json:"name_ja,omitempty"`
	NameState        string `json:"name_state"`
	RelationType     string `json:"relation_type"`
	SummaryJA        string `json:"summary_ja,omitempty"`
	SummaryState     string `json:"summary_state"`
	SummarySourceURL string `json:"summary_source_url,omitempty"`
	EvidenceURL      string `json:"evidence_url,omitempty"`
}

type mioPersonRelatedCatalogSummaryCoverage struct {
	Ready       int `json:"ready"`
	Unavailable int `json:"unavailable"`
	Total       int `json:"total"`
}

var mioPersonRelatedCatalogCues = []struct {
	Cue      string
	Category string
}{
	{Cue: "ドラマ", Category: "drama"},
	{Cue: "受賞歴", Category: "award"},
	{Cue: "音楽作品", Category: "music"},
	{Cue: "音楽", Category: "music"},
	{Cue: "アニメ", Category: "anime"},
	{Cue: "小説", Category: "novel"},
	{Cue: "漫画", Category: "manga"},
}

func parseMioPersonRelatedCatalogLookup(message string) (mioPersonRelatedCatalogLookup, bool) {
	message = strings.TrimSpace(message)
	if message == "" || utf8.RuneCountInString(message) > 160 {
		return mioPersonRelatedCatalogLookup{}, false
	}
	for _, cue := range mioPersonRelatedCatalogCues {
		marker := "の" + cue.Cue
		markerIndex := strings.Index(message, marker)
		if markerIndex <= 0 {
			continue
		}
		name := strings.TrimSpace(message[:markerIndex])
		if strings.HasSuffix(name, "さん") {
			name = strings.TrimSpace(strings.TrimSuffix(name, "さん"))
		}
		if !validMioPersonRelatedCatalogName(name) {
			continue
		}
		suffix := strings.TrimSpace(message[markerIndex+len(marker):])
		if !validMioPersonRelatedCatalogSuffix(suffix) {
			continue
		}
		return mioPersonRelatedCatalogLookup{Name: name, Category: cue.Category}, true
	}
	return mioPersonRelatedCatalogLookup{}, false
}

func validMioPersonRelatedCatalogName(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || utf8.RuneCountInString(value) > 160 {
		return false
	}
	switch value {
	case "人物", "人物名", "俳優", "俳優名", "役者", "役者名", "○○", "名前", "何か", "誰か":
		return false
	}
	for _, marker := range []string{"について", "教えて", "検索", "調べて", "どんな"} {
		if strings.Contains(value, marker) {
			return false
		}
	}
	return true
}

func validMioPersonRelatedCatalogSuffix(value string) bool {
	value = strings.TrimSpace(value)
	value = strings.TrimRight(value, "。！？?!")
	switch value {
	case "", "を", "を教えて", "を教えてください", "について", "について教えて", "について教えてください",
		"を見せて", "を知りたい", "を知りたいです", "を検索して", "を検索してください",
		"について検索して", "について検索してください", "を調べて", "を調べてください",
		"について調べて", "について調べてください", "は", "はある", "はありますか":
		return true
	default:
		return false
	}
}

func (m *MioAgent) personRelatedCatalogLookupContext(ctx context.Context, lookup mioPersonRelatedCatalogLookup) llm.Message {
	unavailable := func(reason string) llm.Message {
		return llm.Message{
			Role:    "system",
			Content: "RenCrow indexed person-related catalog unavailable. Do not use Web検索や外部情報へfallbackせず、現在RenCrowの索引付き人物関連作品を照会できないと回答してください。理由: " + reason,
			Type:    llm.PromptContextRecall,
		}
	}
	if m.toolRunner == nil {
		return unavailable("ToolRunner unavailable")
	}
	metadata, err := m.toolRunner.ListTools(ctx)
	if err != nil {
		return unavailable("Tool list unavailable")
	}
	present := false
	for _, item := range metadata {
		if item.ToolID == "person_related_catalog.lookup" {
			present = true
			break
		}
	}
	if !present {
		return unavailable("person_related_catalog.lookup is not registered")
	}
	response, err := m.toolRunner.ExecuteV2(ctx, "person_related_catalog.lookup", map[string]any{
		"person_name": lookup.Name,
		"category":    lookup.Category,
		"limit":       20,
	})
	if err != nil || response == nil || response.IsError() {
		return unavailable("person_related_catalog.lookup execution failed")
	}
	if personRelatedCatalogResultEmpty(response.Result) && m.personCatalogCollector != nil {
		metadata, listErr := m.personCatalogCollector.ListTools(ctx)
		if listErr != nil {
			return unavailable("Worker Tool list unavailable")
		}
		collectPresent := false
		for _, item := range metadata {
			if item.ToolID == "person_related_catalog.collect" {
				collectPresent = true
				break
			}
		}
		if !collectPresent {
			return unavailable("person_related_catalog.collect is not registered for Worker")
		}
		collected, collectErr := m.personCatalogCollector.ExecuteV2(ctx, "person_related_catalog.collect", map[string]any{
			"person_name": lookup.Name,
			"category":    lookup.Category,
		})
		if collectErr != nil || collected == nil || collected.IsError() {
			return unavailable("person_related_catalog.collect execution failed")
		}
		response, err = m.toolRunner.ExecuteV2(ctx, "person_related_catalog.lookup", map[string]any{
			"person_name": lookup.Name,
			"category":    lookup.Category,
			"limit":       20,
		})
		if err != nil || response == nil || response.IsError() {
			return unavailable("person_related_catalog.lookup after collection failed")
		}
	}
	projection, err := projectMioPersonRelatedCatalogResult(response.Result)
	if err != nil {
		return unavailable("person_related_catalog.lookup result projection failed")
	}
	content := fmt.Sprintf("RenCrow indexed person-related catalog result; answer only from it (this indexed result). tool=person_related_catalog.lookup person_name=%s category=%s. Preserve display_name and name_original exactly; do not translate or generate title names. If the result is empty or information is unavailable, say so without web or external supplementation.\n%s", lookup.Name, lookup.Category, projection)
	if len([]byte(content)) > mioPersonRelatedCatalogContextMaxBytes {
		return unavailable("person_related_catalog.lookup result exceeds the conversation context bound")
	}
	return llm.Message{
		Role:    "system",
		Content: content,
		Type:    llm.PromptContextRecall,
	}
}

func projectMioPersonRelatedCatalogResult(result any) (string, error) {
	payload, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("marshal person related catalog result: %w", err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(payload, &envelope); err != nil || envelope == nil {
		if err == nil {
			err = fmt.Errorf("result is not an object")
		}
		return "", fmt.Errorf("decode person related catalog result: %w", err)
	}
	itemsPayload, ok := envelope["items"]
	if !ok {
		return "", fmt.Errorf("result items are missing")
	}
	var rawItems []map[string]json.RawMessage
	if err := json.Unmarshal(itemsPayload, &rawItems); err != nil || rawItems == nil {
		if err == nil {
			err = fmt.Errorf("items are not an array")
		}
		return "", fmt.Errorf("decode person related catalog items: %w", err)
	}
	coveragePayload, ok := envelope["summary_coverage"]
	if !ok {
		return "", fmt.Errorf("summary coverage is missing")
	}
	var coverage mioPersonRelatedCatalogSummaryCoverage
	if err := json.Unmarshal(coveragePayload, &coverage); err != nil {
		return "", fmt.Errorf("decode summary coverage: %w", err)
	}
	if coverage.Ready < 0 || coverage.Unavailable < 0 || coverage.Total < 0 || coverage.Ready+coverage.Unavailable != coverage.Total {
		return "", fmt.Errorf("summary coverage is inconsistent")
	}
	projection := mioPersonRelatedCatalogConversationProjection{
		Items:           make([]mioPersonRelatedCatalogConversationItem, 0, len(rawItems)),
		SummaryCoverage: coverage,
	}
	for index, rawItem := range rawItems {
		item, err := projectMioPersonRelatedCatalogItem(rawItem)
		if err != nil {
			return "", fmt.Errorf("project person related catalog item %d: %w", index, err)
		}
		projection.Items = append(projection.Items, item)
	}
	encoded, err := json.Marshal(projection)
	if err != nil {
		return "", fmt.Errorf("encode person related catalog projection: %w", err)
	}
	return string(encoded), nil
}

func projectMioPersonRelatedCatalogItem(rawItem map[string]json.RawMessage) (mioPersonRelatedCatalogConversationItem, error) {
	if rawItem == nil {
		return mioPersonRelatedCatalogConversationItem{}, fmt.Errorf("item is not an object")
	}
	displayName, err := requiredMioPersonRelatedCatalogString(rawItem, "display_name")
	if err != nil {
		return mioPersonRelatedCatalogConversationItem{}, err
	}
	nameOriginal, err := requiredMioPersonRelatedCatalogString(rawItem, "name_original")
	if err != nil {
		return mioPersonRelatedCatalogConversationItem{}, err
	}
	nameState, err := requiredMioPersonRelatedCatalogString(rawItem, "name_state")
	if err != nil {
		return mioPersonRelatedCatalogConversationItem{}, err
	}
	relationType, err := requiredMioPersonRelatedCatalogString(rawItem, "relation_type")
	if err != nil {
		return mioPersonRelatedCatalogConversationItem{}, err
	}
	summaryState, err := requiredMioPersonRelatedCatalogString(rawItem, "summary_state")
	if err != nil {
		return mioPersonRelatedCatalogConversationItem{}, err
	}
	nameJA, err := optionalMioPersonRelatedCatalogString(rawItem, "name_ja")
	if err != nil {
		return mioPersonRelatedCatalogConversationItem{}, err
	}
	summaryJA, err := optionalMioPersonRelatedCatalogString(rawItem, "summary_ja")
	if err != nil {
		return mioPersonRelatedCatalogConversationItem{}, err
	}
	summarySourceURL, err := optionalMioPersonRelatedCatalogString(rawItem, "summary_source_url")
	if err != nil {
		return mioPersonRelatedCatalogConversationItem{}, err
	}
	evidenceURL, err := optionalMioPersonRelatedCatalogString(rawItem, "evidence_url")
	if err != nil {
		return mioPersonRelatedCatalogConversationItem{}, err
	}
	return mioPersonRelatedCatalogConversationItem{
		DisplayName:      displayName,
		NameOriginal:     nameOriginal,
		NameJA:           nameJA,
		NameState:        nameState,
		RelationType:     relationType,
		SummaryJA:        summaryJA,
		SummaryState:     summaryState,
		SummarySourceURL: summarySourceURL,
		EvidenceURL:      evidenceURL,
	}, nil
}

func requiredMioPersonRelatedCatalogString(fields map[string]json.RawMessage, name string) (string, error) {
	payload, ok := fields[name]
	if !ok {
		return "", fmt.Errorf("required field %q is missing", name)
	}
	var value string
	if err := json.Unmarshal(payload, &value); err != nil {
		return "", fmt.Errorf("field %q is not a string: %w", name, err)
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("required field %q is empty", name)
	}
	return value, nil
}

func optionalMioPersonRelatedCatalogString(fields map[string]json.RawMessage, name string) (string, error) {
	payload, ok := fields[name]
	if !ok {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(payload, &value); err != nil {
		return "", fmt.Errorf("field %q is not a string: %w", name, err)
	}
	return value, nil
}

func personRelatedCatalogResultEmpty(result any) bool {
	payload, err := json.Marshal(result)
	if err != nil {
		return false
	}
	var projection struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(payload, &projection); err != nil {
		return false
	}
	return len(projection.Items) == 0
}
