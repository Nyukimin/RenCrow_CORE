package agent

import (
	"testing"

	domainattachment "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/attachment"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
)

func TestUserMessageWithAttachmentsIncludesAudioPart(t *testing.T) {
	msg := userMessageWithAttachments("音声を確認", []domainattachment.Attachment{{
		Kind:        domainattachment.KindAudio,
		Filename:    "voice.wav",
		ContentType: "audio/wav",
		Data:        []byte("wav-bytes"),
	}})
	if len(msg.Parts) != 2 {
		t.Fatalf("parts = %d, want 2", len(msg.Parts))
	}
	if msg.Parts[1].Type != llm.MessagePartAudio || msg.Parts[1].MimeType != "audio/wav" {
		t.Fatalf("unexpected audio part: %#v", msg.Parts[1])
	}
}
