package idlechat

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

var storySemanticValidationCodes = map[string]struct{}{
	"lexical_corruption":          {},
	"entity_relation_violation":   {},
	"continuity_violation":        {},
	"world_rule_violation":        {},
	"reading_violation":           {},
	"interest_contract_violation": {},
	"story_performance_violation": {},
	"quality_violation":           {},
}

// ValidateStoryEpisode combines deterministic checks with a separate semantic
// review. It never promotes uncertain output to ready.
func ValidateStoryEpisode(artifact StoryEpisodeArtifact, semantic StorySemanticReview) StoryValidationResult {
	errors := make([]StoryValidationError, 0, 8)
	add := func(code string, turn int, field, evidence string) {
		errors = append(errors, StoryValidationError{Code: code, TurnIndex: turn, Field: field, Evidence: evidence})
	}

	if strings.TrimSpace(artifact.SchemaVersion) != StoryEpisodeSchemaVersion {
		add("schema_violation", 0, "schema_version", "unsupported story schema")
	}
	if strings.TrimSpace(artifact.EpisodeKind) != StoryEpisodeKind {
		add("schema_violation", 0, "episode_kind", "story_reading is required")
	}
	reader := normalizeStoryAgent(artifact.Reader)
	listener := normalizeStoryAgent(artifact.Listener)
	if !isStoryAgent(reader) || !isStoryAgent(listener) || reader == listener {
		add("story_performance_violation", 0, "reader/listener", "reader and listener must be distinct Mio/Shiro agents")
	}
	validateStoryContract(artifact.Contract, add)
	validateStoryLedger(artifact.Ledger, add)

	narrationCount := 0
	interjectionCount := 0
	narrationRunes := 0
	lastRole := ""
	lastNarrationTurn := 0
	narrationsSinceInterjection := 0
	seenInterjections := make(map[string]struct{})
	for idx, turn := range artifact.Turns {
		expectedTurn := idx + 1
		if turn.TurnIndex != expectedTurn {
			add("schema_violation", expectedTurn, "turn_index", fmt.Sprintf("got %d", turn.TurnIndex))
		}
		display := strings.TrimSpace(turn.DisplayText)
		speech := strings.TrimSpace(turn.SpeechText)
		if display == "" || speech == "" {
			add("schema_violation", expectedTurn, "display_text/speech_text", "both texts are required")
		}
		if hasBrokenStoryText(display) || hasBrokenStoryText(speech) {
			add("lexical_corruption", expectedTurn, "text", "replacement or invalid control character detected")
		}

		speaker := normalizeStoryAgent(turn.Speaker)
		switch strings.TrimSpace(turn.UtteranceRole) {
		case StoryUtteranceNarration:
			narrationCount++
			narrationsSinceInterjection++
			narrationRunes += utf8.RuneCountInString(display)
			lastNarrationTurn = expectedTurn
			if speaker != reader {
				add("story_performance_violation", expectedTurn, "speaker", "only the fixed reader may narrate")
			}
			if turn.ReactsTo != 0 {
				add("schema_violation", expectedTurn, "reacts_to", "narration must not react to another turn")
			}
		case StoryUtteranceInterjection:
			interjectionCount++
			if speaker != listener {
				add("story_performance_violation", expectedTurn, "speaker", "only the listener may interject")
			}
			if lastRole == StoryUtteranceInterjection {
				add("story_performance_violation", expectedTurn, "utterance_role", "interjections must not be consecutive")
			}
			if turn.ReactsTo <= 0 || turn.ReactsTo != lastNarrationTurn {
				add("story_performance_violation", expectedTurn, "reacts_to", "interjection must point to the immediately preceding narration")
			}
			if narrationsSinceInterjection < 3 {
				add("story_performance_violation", expectedTurn, "frequency", "at least three narration turns are required between interjections")
			}
			if utf8.RuneCountInString(display) > 30 {
				add("story_performance_violation", expectedTurn, "display_text", "interjection exceeds 30 runes")
			}
			key := normalizeStoryPhrase(display)
			if _, exists := seenInterjections[key]; key != "" && exists {
				add("repetition", expectedTurn, "display_text", "same interjection repeated")
			}
			seenInterjections[key] = struct{}{}
			narrationsSinceInterjection = 0
		default:
			add("schema_violation", expectedTurn, "utterance_role", "role must be narration or interjection")
		}
		validateStoryReadings(artifact.Ledger, turn, add)
		lastRole = strings.TrimSpace(turn.UtteranceRole)
	}
	if narrationCount < 6 || narrationRunes < 500 {
		add("quality_violation", 0, "turns", "story must contain at least six staged narration turns and 500 runes")
	}
	if interjectionCount == 0 {
		add("story_performance_violation", 0, "turns", "listener interjection is required")
	} else if total := narrationCount + interjectionCount; interjectionCount*100 < total*10 || interjectionCount*100 > total*25 {
		add("story_performance_violation", 0, "frequency", "interjections must stay within 10 to 25 percent of utterances")
	}

	if !semantic.Valid && len(semantic.Errors) == 0 {
		add("quality_violation", 0, "semantic_review", "semantic review did not approve the story")
	}
	for _, item := range semantic.Errors {
		code := strings.TrimSpace(item.Code)
		if _, ok := storySemanticValidationCodes[code]; !ok {
			code = "quality_violation"
		}
		add(code, item.TurnIndex, item.Field, item.Evidence)
	}

	result := StoryValidationResult{Valid: len(errors) == 0, Errors: errors}
	for _, item := range errors {
		if item.TurnIndex <= 0 {
			continue
		}
		if result.FirstInvalidTurn == 0 || item.TurnIndex < result.FirstInvalidTurn {
			result.FirstInvalidTurn = item.TurnIndex
		}
	}
	return result
}

func validateStoryContract(contract StoryEpisodeContract, add func(string, int, string, string)) {
	if strings.TrimSpace(contract.TransformationAxis) == "" || strings.TrimSpace(contract.Genre) == "" || strings.TrimSpace(contract.InterestDirection) == "" {
		add("interest_contract_violation", 0, "story_contract", "transformation axis, genre and interest direction are required")
	}
	if len(contract.InterestContract) == 0 {
		add("interest_contract_violation", 0, "interest_contract", "at least one success condition is required")
	}
	switch DialogueContentMode(strings.TrimSpace(contract.ContentMode)) {
	case DialogueContentModeSerious, DialogueContentModeAssertive, DialogueContentModeFree:
	default:
		add("content_mode_violation", 0, "content_mode", "unsupported content mode")
	}
}

func validateStoryLedger(ledger StoryEpisodeLedger, add func(string, int, string, string)) {
	if len(ledger.Entities) == 0 {
		add("continuity_violation", 0, "story_ledger.entities", "at least one entity is required")
	}
	entityIDs := make(map[string]struct{}, len(ledger.Entities))
	for _, entity := range ledger.Entities {
		id := strings.TrimSpace(entity.ID)
		if id == "" || strings.TrimSpace(entity.Name) == "" || strings.TrimSpace(entity.Reading) == "" {
			add("reading_violation", 0, "story_ledger.entities", "entity id, name and reading are required")
			continue
		}
		if _, exists := entityIDs[id]; exists {
			add("entity_relation_violation", 0, "story_ledger.entities", "duplicate entity id: "+id)
		}
		entityIDs[id] = struct{}{}
	}
	for _, relation := range ledger.Relations {
		if _, ok := entityIDs[strings.TrimSpace(relation.Subject)]; !ok {
			add("entity_relation_violation", 0, "story_ledger.relations", "unknown relation subject")
		}
		if _, ok := entityIDs[strings.TrimSpace(relation.Object)]; !ok {
			add("entity_relation_violation", 0, "story_ledger.relations", "unknown relation object")
		}
		if strings.TrimSpace(relation.Relation) == "" {
			add("entity_relation_violation", 0, "story_ledger.relations", "relation type is required")
		}
	}
	for _, term := range ledger.CoinedTerms {
		if strings.TrimSpace(term.Surface) == "" || strings.TrimSpace(term.Reading) == "" || strings.TrimSpace(term.Meaning) == "" {
			add("lexical_corruption", 0, "story_ledger.coined_terms", "surface, reading and meaning are required")
		}
	}
}

func validateStoryReadings(ledger StoryEpisodeLedger, turn StoryEpisodeTurn, add func(string, int, string, string)) {
	check := func(surface, reading string) {
		surface = strings.TrimSpace(surface)
		reading = strings.TrimSpace(reading)
		if surface == "" || reading == "" || !storyTextContainsSurface(turn.DisplayText, surface) {
			return
		}
		if !strings.Contains(turn.SpeechText, reading) {
			add("reading_violation", turn.TurnIndex, "speech_text", fmt.Sprintf("%s must be read as %s", surface, reading))
		}
	}
	for _, entity := range ledger.Entities {
		check(entity.Name, entity.Reading)
	}
	for _, term := range ledger.CoinedTerms {
		check(term.Surface, term.Reading)
	}
}

func storyTextContainsSurface(text, surface string) bool {
	if !strings.Contains(text, surface) {
		return false
	}
	surfaceRunes := []rune(surface)
	if len(surfaceRunes) != 1 {
		return true
	}
	textRunes := []rune(text)
	for i, r := range textRunes {
		if r != surfaceRunes[0] {
			continue
		}
		if i > 0 && isStoryCompoundRune(textRunes[i-1]) {
			continue
		}
		if i+1 < len(textRunes) && isStoryCompoundRune(textRunes[i+1]) {
			continue
		}
		return true
	}
	return false
}

func isStoryCompoundRune(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Latin, r) || unicode.IsDigit(r)
}

func normalizeStoryAgent(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func isStoryAgent(value string) bool {
	return value == "mio" || value == "shiro"
}

func normalizeStoryPhrase(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.Trim(value, "。．.!！?？…‥ ")
}

func hasBrokenStoryText(value string) bool {
	if strings.ContainsRune(value, utf8.RuneError) {
		return true
	}
	for _, r := range value {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return true
		}
	}
	return false
}
