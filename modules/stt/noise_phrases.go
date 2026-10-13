package stt

import (
	"strings"
	"unicode"
)

// DefaultNoisePhrases are phrases Whisper-family engines emit for silence or
// background noise (subtitle and video-outro boilerplate seen in training
// data). They are dropped only when they are the whole utterance. Phrases a
// person may genuinely say, such as a bare "ありがとうございました", are not
// listed; operators can extend the list with stt.noise_phrases.
var DefaultNoisePhrases = []string{
	"ご視聴ありがとうございました",
	"ご視聴ありがとうございます",
	"ご清聴ありがとうございました",
	"最後までご視聴いただきありがとうございました",
	"チャンネル登録お願いします",
	"チャンネル登録をお願いします",
	"チャンネル登録よろしくお願いします",
	"Thanks for watching",
	"Thank you for watching",
}

// NoisePhraseFilter drops a transcript that is entirely a known phrase.
type NoisePhraseFilter struct {
	phrases map[string]struct{}
}

// NewNoisePhraseFilter returns a filter of the defaults plus extra phrases.
func NewNoisePhraseFilter(extra []string) *NoisePhraseFilter {
	f := &NoisePhraseFilter{phrases: make(map[string]struct{}, len(DefaultNoisePhrases)+len(extra))}
	for _, list := range [][]string{DefaultNoisePhrases, extra} {
		for _, phrase := range list {
			if normalized := NormalizeNoisePhrase(phrase); normalized != "" {
				f.phrases[normalized] = struct{}{}
			}
		}
	}
	return f
}

// Match reports whether the whole text equals a known phrase and returns the
// normalised phrase. A nil filter matches nothing.
func (f *NoisePhraseFilter) Match(text string) (string, bool) {
	if f == nil {
		return "", false
	}
	normalized := NormalizeNoisePhrase(text)
	if normalized == "" {
		return "", false
	}
	_, ok := f.phrases[normalized]
	return normalized, ok
}

// NormalizeNoisePhrase lower-cases text, folds full-width ASCII and removes
// whitespace, punctuation and symbols so that "ご視聴ありがとうございました。"
// and "ご視聴 ありがとう ございました！" compare equal.
func NormalizeNoisePhrase(text string) string {
	var b strings.Builder
	for _, r := range text {
		switch {
		case r >= 0xFF01 && r <= 0xFF5E:
			r -= 0xFEE0
		case r == 0x3000:
			r = ' '
		}
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// TranscriptOutcome is what the session does with an engine transcript.
type TranscriptOutcome struct {
	// Text is the transcript to deliver; empty when EmptyReason is set.
	Text string
	// EmptyReason says why nothing is delivered (EmptyReasonNoSpeech or
	// EmptyReasonFiltered).
	EmptyReason string
	// ProviderError marks LLM-style refusal boilerplate that must surface as an
	// error, never as user text.
	ProviderError bool
}

// ClassifyTranscript applies the transcript rules in one place: empty result,
// provider boilerplate, model control markers and known hallucination phrases.
func ClassifyTranscript(raw string, filter *NoisePhraseFilter) TranscriptOutcome {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return TranscriptOutcome{EmptyReason: EmptyReasonNoSpeech}
	}
	if IsProviderErrorTranscriptText(trimmed) {
		return TranscriptOutcome{ProviderError: true}
	}
	text := NormalizeTranscriptText(trimmed)
	if text == "" {
		return TranscriptOutcome{EmptyReason: EmptyReasonFiltered}
	}
	if _, ok := filter.Match(text); ok {
		return TranscriptOutcome{EmptyReason: EmptyReasonFiltered}
	}
	return TranscriptOutcome{Text: text}
}
