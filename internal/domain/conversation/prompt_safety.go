package conversation

import (
	"strings"
	"unicode"
)

const unsafeGeneratedExampleReason = "degenerate generated Agent text is not reusable prompt context"

// IsUnsafeGeneratedTextForPrompt reports generated text that is safe to retain
// as conversation evidence but unsafe to feed back to a model as an example.
// It intentionally does not inspect user messages: quoted or repetitive user
// input remains authoritative conversation context.
func IsUnsafeGeneratedTextForPrompt(value string) bool {
	compact := make([]rune, 0, len([]rune(value)))
	last := rune(0)
	run := 0
	for _, current := range []rune(strings.TrimSpace(value)) {
		if current == last {
			run++
		} else {
			last = current
			run = 1
		}
		if run >= 8 {
			return true
		}
		if unicode.IsSpace(current) || unicode.IsPunct(current) {
			continue
		}
		compact = append(compact, current)
	}
	for motifLen := 3; motifLen <= 8; motifLen++ {
		for start := 0; start+2*motifLen <= len(compact); start++ {
			if string(compact[start:start+motifLen]) == string(compact[start+motifLen:start+2*motifLen]) {
				return true
			}
		}
	}
	return false
}

// WithoutUnsafeAgentExamples returns a prompt projection copy. Durable source
// messages are not changed; rejected examples remain visible in the Recall
// trace with a bounded summary and deterministic reason.
func (rp RecallPack) WithoutUnsafeAgentExamples() RecallPack {
	out := rp
	out.ShortContext = make([]Message, 0, len(rp.ShortContext))
	out.RejectedTraceItems = append([]RecallTraceItem(nil), rp.RejectedTraceItems...)
	for _, message := range rp.ShortContext {
		if IsChatAgentSpeaker(message.Speaker) && IsUnsafeGeneratedTextForPrompt(message.Msg) {
			summary := strings.TrimSpace(message.Msg)
			if runes := []rune(summary); len(runes) > 160 {
				summary = string(runes[:160]) + "…"
			}
			out.RejectedTraceItems = append(out.RejectedTraceItems, RecallTraceItem{
				Layer:         "L0",
				Kind:          "short_context",
				Summary:       summary,
				Decision:      "excluded",
				Status:        TraceStatusFilteredStatus,
				Reason:        unsafeGeneratedExampleReason,
				PromptSection: PromptSectionConversation,
			})
			continue
		}
		out.ShortContext = append(out.ShortContext, message)
	}
	return out
}
