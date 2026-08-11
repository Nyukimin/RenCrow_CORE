package agent

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

type mioMovieCatalogLookup struct {
	Kind        string
	Name        string
	Information string
}

var (
	mioPersonPossessivePattern = regexp.MustCompile(`^(.+?)(?:さん)?の(?:出演映画|出演作)(?:を|について|は|が|$)`)
	mioActorAboutPattern       = regexp.MustCompile(`^俳優の(.+?)(?:について)?(?:の)?(?:出演映画|出演作)(?:を|について|は|が|$)`)
	mioMovieOverviewPattern    = regexp.MustCompile(`^(.+?)(?:って|は)(?:どんな)?映画(?:なの|ですか|\?|？|$)`)
	mioPersonProfilePattern    = regexp.MustCompile(`^(.+?)(?:って|は)(?:どんな)?俳優(?:なの|ですか|\?|？|$)`)
)

func parseMioMovieCatalogLookup(message string) (mioMovieCatalogLookup, bool) {
	message = strings.TrimSpace(message)
	if message == "" {
		return mioMovieCatalogLookup{}, false
	}
	if information, ok := movieInformationCue(message); ok && strings.Contains(message, "映画") {
		for _, pair := range [][2]string{{"「", "」"}, {"『", "』"}, {"“", "”"}, {`"`, `"`}} {
			if title := textBetween(message, pair[0], pair[1]); validMovieCatalogEntityName(title) {
				return mioMovieCatalogLookup{Kind: "movie", Name: title, Information: information}, true
			}
		}
	}
	if matched := mioMovieOverviewPattern.FindStringSubmatch(stripMovieCatalogSourceCue(message)); len(matched) == 2 {
		name := strings.TrimSpace(matched[1])
		if validMovieCatalogEntityName(name) {
			return mioMovieCatalogLookup{Kind: "movie", Name: name, Information: "overview"}, true
		}
	}
	if hasPersonFilmographyRelation(message) {
		for _, pattern := range []*regexp.Regexp{mioActorAboutPattern, mioPersonPossessivePattern} {
			matched := pattern.FindStringSubmatch(stripMovieCatalogSourceCue(message))
			if len(matched) == 2 {
				name := strings.TrimSpace(matched[1])
				if validMovieCatalogEntityName(name) {
					return mioMovieCatalogLookup{Kind: "person", Name: name, Information: "filmography"}, true
				}
			}
		}
	}
	if matched := mioPersonProfilePattern.FindStringSubmatch(stripMovieCatalogSourceCue(message)); len(matched) == 2 {
		name := strings.TrimSpace(matched[1])
		if validMovieCatalogEntityName(name) {
			return mioMovieCatalogLookup{Kind: "person", Name: name, Information: "profile"}, true
		}
	}
	return mioMovieCatalogLookup{}, false
}

func movieInformationCue(message string) (string, bool) {
	switch {
	case strings.Contains(message, "キャスト") || strings.Contains(message, "出演者"):
		return "cast", true
	case strings.Contains(message, "スタッフ"):
		return "staff", true
	case strings.Contains(message, "あらすじ") || strings.Contains(message, "どんな映画"):
		return "overview", true
	default:
		return "", false
	}
}

func hasPersonFilmographyRelation(message string) bool {
	return strings.Contains(message, "出演映画") || strings.Contains(message, "出演作")
}

func stripMovieCatalogSourceCue(message string) string {
	message = strings.TrimSpace(message)
	for _, prefix := range []string{"RenCrowの映画DBで", "RenCrow映画DBで", "映画DBで", "映画DBの"} {
		message = strings.TrimPrefix(message, prefix)
	}
	return strings.TrimSpace(message)
}

func textBetween(value, open, close string) string {
	start := strings.Index(value, open)
	if start < 0 {
		return ""
	}
	remainder := value[start+len(open):]
	end := strings.Index(remainder, close)
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(remainder[:end])
}

func validMovieCatalogEntityName(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len([]rune(value)) > 160 {
		return false
	}
	for _, generic := range []string{"役者名", "俳優名", "映画名", "名前", "誰か", "何か", "映画", "俳優", "役者", "映画DB"} {
		if value == generic {
			return false
		}
	}
	return true
}

func (m *MioAgent) movieCatalogLookupContext(ctx context.Context, lookup mioMovieCatalogLookup) llm.Message {
	unavailable := func(reason string) llm.Message {
		return llm.Message{
			Role:    "system",
			Content: "RenCrow indexed catalog unavailable. Do not use Web検索や外部情報へfallbackせず、現在RenCrow映画カタログを照会できないと回答してください。理由: " + reason,
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
		if item.ToolID == "movie_catalog.lookup" {
			present = true
			break
		}
	}
	if !present {
		return unavailable("movie_catalog.lookup is not registered")
	}
	response, err := m.toolRunner.ExecuteV2(ctx, "movie_catalog.lookup", map[string]any{
		"kind":        lookup.Kind,
		"name":        lookup.Name,
		"information": lookup.Information,
		"limit":       10,
	})
	if err != nil || response == nil || response.IsError() {
		return unavailable("movie_catalog.lookup execution failed")
	}
	return llm.Message{
		Role:    "system",
		Content: fmt.Sprintf("RenCrow indexed catalog result; answer only from it. kind=%s name=%s information=%s. If information_available is false, say that the RenCrow catalog has no requested detail; do not infer or supplement it.\n%s", lookup.Kind, lookup.Name, lookup.Information, response.String()),
		Type:    llm.PromptContextRecall,
	}
}
