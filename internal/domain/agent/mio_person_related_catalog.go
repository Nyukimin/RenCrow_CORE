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
	return llm.Message{
		Role:    "system",
		Content: fmt.Sprintf("RenCrow indexed person-related catalog result; answer only from it (this indexed result). tool=person_related_catalog.lookup person_name=%s category=%s. Preserve display_name and name_original exactly; do not translate or generate title names. If the result is empty or information is unavailable, say so without web or external supplementation.\n%s", lookup.Name, lookup.Category, response.String()),
		Type:    llm.PromptContextRecall,
	}
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
