package tts

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	TTSChunkMinRunes = 6
	// Keep synthesis chunks short enough for realtime-ish playback. Long single
	// sentences can take tens of seconds to generate even when they are natural.
	TTSChunkTargetRunes          = 34
	TTSChunkMaxRunes             = 44
	TTSChunkSoftBoundaryMinRunes = 18
)

func NextTTSChunk(text string, final bool) (chunk, rest string, ok bool) {
	trimmed := strings.TrimLeftFunc(text, unicode.IsSpace)
	if trimmed == "" {
		return "", "", false
	}

	lastHard := -1
	lastSoft := -1
	lastSoftRunes := 0
	lastSpace := -1
	lastSpaceRunes := 0
	runeCount := 0
	for i, r := range trimmed {
		runeCount++
		end := i + utf8.RuneLen(r)
		switch {
		case IsTTSHardBoundary(r):
			lastHard = end
			if runeCount >= TTSChunkMinRunes {
				return SplitTTSChunk(trimmed, ExtendTTSChunkCut(trimmed, end))
			}
		case IsTTSSoftBoundary(r):
			lastSoft = end
			lastSoftRunes = runeCount
		case unicode.IsSpace(r):
			lastSpace = end
			lastSpaceRunes = runeCount
		}
		if runeCount >= TTSChunkTargetRunes && lastSoft > 0 && lastSoftRunes >= TTSChunkSoftBoundaryMinRunes {
			return SplitTTSChunk(trimmed, lastSoft)
		}
		if runeCount >= TTSChunkMaxRunes {
			cut := chooseTTSChunkCutForLatency(lastHard, lastSoft, lastSoftRunes, lastSpace, lastSpaceRunes)
			if cut > 0 {
				return SplitTTSChunk(trimmed, cut)
			}
			return SplitTTSChunk(trimmed, end)
		}
	}

	if lastHard > 0 && runeCount >= TTSChunkMinRunes {
		return SplitTTSChunk(trimmed, ExtendTTSChunkCut(trimmed, lastHard))
	}
	if final {
		return SplitTTSChunk(trimmed, len(trimmed))
	}
	return "", trimmed, false
}

func SplitTTSChunks(text string) []string {
	remaining := text
	chunks := make([]string, 0, 4)
	for {
		chunk, rest, ok := NextTTSChunk(remaining, true)
		if !ok {
			break
		}
		chunks = append(chunks, chunk)
		if strings.TrimSpace(rest) == "" || rest == remaining {
			break
		}
		remaining = rest
	}
	return chunks
}

func chooseTTSChunkCutForLatency(lastHard, lastSoft, lastSoftRunes, lastSpace, lastSpaceRunes int) int {
	switch {
	case lastHard > 0:
		return lastHard
	case lastSoft > 0 && lastSoftRunes >= TTSChunkSoftBoundaryMinRunes:
		return lastSoft
	case lastSpace > 0 && lastSpaceRunes >= TTSChunkSoftBoundaryMinRunes:
		return lastSpace
	default:
		return 0
	}
}

func ChooseTTSChunkCut(lastHard, lastSoft, lastSpace int) int {
	switch {
	case lastHard > 0:
		return lastHard
	case lastSoft > 0:
		return lastSoft
	case lastSpace > 0:
		return lastSpace
	default:
		return 0
	}
}

func SplitTTSChunk(text string, cut int) (chunk, rest string, ok bool) {
	if cut <= 0 || cut > len(text) {
		return "", text, false
	}
	chunk = strings.TrimSpace(text[:cut])
	rest = strings.TrimLeftFunc(text[cut:], unicode.IsSpace)
	if chunk == "" {
		return "", rest, false
	}
	return chunk, rest, true
}

func ExtendTTSChunkCut(text string, cut int) int {
	if cut <= 0 || cut >= len(text) {
		return cut
	}
	extended := cut
	for extended < len(text) {
		r, size := utf8.DecodeRuneInString(text[extended:])
		if r == utf8.RuneError && size == 0 {
			break
		}
		if !IsTTSClosingBoundary(r) {
			break
		}
		extended += size
	}
	return extended
}

func IsTTSClosingBoundary(r rune) bool {
	switch r {
	case '」', '』', '）', ')', '］', ']', '】', '〉', '》':
		return true
	default:
		return false
	}
}

func IsTTSHardBoundary(r rune) bool {
	switch r {
	case '。', '！', '？', '.', '!', '?', '\n':
		return true
	default:
		return false
	}
}

func IsTTSSoftBoundary(r rune) bool {
	switch r {
	case '、', '，', ',', ';', '；', ':', '：':
		return true
	default:
		return false
	}
}
