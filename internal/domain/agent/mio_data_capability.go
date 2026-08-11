package agent

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

type mioDataToolLookup struct {
	ToolID string
	Args   map[string]any
}

func parseMioDataToolLookup(message string) (mioDataToolLookup, bool) {
	message = strings.TrimSpace(message)
	if message == "" || utf8.RuneCountInString(message) > 160 {
		return mioDataToolLookup{}, false
	}

	if strings.Contains(message, "用語DB") && isAvailabilityQuestion(message) {
		return mioDataToolLookup{ToolID: "data_capability.describe", Args: map[string]any{
			"operation": "describe",
			"name":      "glossary",
		}}, true
	}
	if strings.Contains(message, "用語集") {
		if value, ok := quotedDataValue(message); ok {
			if strings.Contains(message, "カテゴリ") {
				return mioDataToolLookup{ToolID: "glossary.lookup", Args: map[string]any{
					"operation": "list_category",
					"category":  value,
					"limit":     10,
				}}, true
			}
			if strings.Contains(message, "調べ") || strings.Contains(message, "検索") || strings.Contains(message, "意味") || strings.Contains(message, "定義") {
				return mioDataToolLookup{ToolID: "glossary.lookup", Args: map[string]any{
					"operation": "define_term",
					"term":      value,
					"limit":     10,
				}}, true
			}
		}
	}

	if strings.Contains(message, "RenCrow") {
		if (strings.Contains(message, "DB") || strings.Contains(message, "データベース")) &&
			(strings.Contains(message, "どんな") || strings.Contains(message, "一覧") || strings.Contains(message, "何がある")) {
			return mioDataToolLookup{ToolID: "data_capability.describe", Args: map[string]any{"operation": "list_catalog"}}, true
		}
		if strings.Contains(message, "データ") && strings.Contains(message, "使える") {
			return mioDataToolLookup{ToolID: "data_capability.describe", Args: map[string]any{"operation": "list_available"}}, true
		}
	}
	return mioDataToolLookup{}, false
}

func quotedDataValue(message string) (string, bool) {
	for _, pair := range [][2]string{{"「", "」"}, {"『", "』"}, {"“", "”"}, {`"`, `"`}} {
		value := textBetween(message, pair[0], pair[1])
		if validDataLookupValue(value) {
			return value, true
		}
	}
	return "", false
}

func validDataLookupValue(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || utf8.RuneCountInString(value) > 160 {
		return false
	}
	switch value {
	case "用語", "用語名", "カテゴリ", "カテゴリ名", "何か", "名前":
		return false
	default:
		return true
	}
}

func isAvailabilityQuestion(message string) bool {
	return strings.Contains(message, "使える") || strings.Contains(message, "利用できる") || strings.Contains(message, "利用可能")
}

func (m *MioAgent) dataToolLookupContext(ctx context.Context, lookup mioDataToolLookup) llm.Message {
	unavailable := func(reason string) llm.Message {
		return llm.Message{
			Role:    "system",
			Content: "RenCrow indexed data unavailable. Do not use Web検索や別DBへfallbackせず、現在このRenCrowデータを照会できないと回答してください。理由: " + reason,
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
		if item.ToolID == lookup.ToolID {
			present = true
			break
		}
	}
	if !present {
		return unavailable(lookup.ToolID + " is not registered")
	}
	response, err := m.toolRunner.ExecuteV2(ctx, lookup.ToolID, lookup.Args)
	if err != nil || response == nil || response.IsError() {
		return unavailable(lookup.ToolID + " execution failed")
	}
	return llm.Message{
		Role:    "system",
		Content: fmt.Sprintf("RenCrow indexed data result; answer only from it. tool=%s. Do not infer or supplement it.\n%s", lookup.ToolID, response.String()),
		Type:    llm.PromptContextRecall,
	}
}
