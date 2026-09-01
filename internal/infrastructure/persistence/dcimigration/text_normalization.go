package dcimigration

import (
	"strings"
	"unicode/utf8"
)

// normalizeSnapshotEvidence applies the one migration text-normalization
// policy to the deduplicated in-memory evidence snapshot. Source files and
// their hashes are intentionally outside this function's mutation boundary.
func normalizeSnapshotEvidence(snapshot *sourceSnapshot) {
	if snapshot == nil {
		return
	}
	counts := textNormalizationCounts{}
	for _, evidenceID := range sortedEvidenceIDs(snapshot.Evidence) {
		evidence := snapshot.Evidence[evidenceID]
		normalized, invalidBytes := normalizeUTF8Text(evidence.Snippet)
		if invalidBytes == 0 {
			continue
		}
		evidence.Snippet = normalized
		snapshot.Evidence[evidenceID] = evidence
		counts.NormalizedTextValues++
		counts.InvalidUTF8Bytes += invalidBytes
	}
	snapshot.normalization = counts
}

// normalizeUTF8Text replaces each invalid UTF-8 byte with one U+FFFD. A
// valid encoded U+FFFD is preserved and is not counted as invalid input.
func normalizeUTF8Text(value string) (string, int) {
	if utf8.ValidString(value) {
		return value, 0
	}
	var builder strings.Builder
	builder.Grow(len(value))
	invalidBytes := 0
	for len(value) > 0 {
		runeValue, size := utf8.DecodeRuneInString(value)
		if runeValue == utf8.RuneError && size == 1 {
			builder.WriteRune(utf8.RuneError)
			invalidBytes++
		} else {
			builder.WriteString(value[:size])
		}
		value = value[size:]
	}
	return builder.String(), invalidBytes
}
