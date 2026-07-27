package agent

import (
	"testing"

	domainattachment "github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
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

func TestUserMessageWithAttachmentsDoesNotForwardRawVisualMedia(t *testing.T) {
	msg := userMessageWithAttachments("この画像と動画を確認して", []domainattachment.Attachment{
		{
			Kind:        domainattachment.KindImage,
			Filename:    "photo.png",
			ContentType: "image/png",
			Data:        []byte("raw-image"),
		},
		{
			Kind:        domainattachment.KindVideo,
			Filename:    "clip.mp4",
			ContentType: "video/mp4",
			Data:        []byte("raw-video"),
		},
	})

	if len(msg.Parts) != 1 {
		t.Fatalf("parts = %d, want text-only message", len(msg.Parts))
	}
	if msg.Parts[0].Type != llm.MessagePartText {
		t.Fatalf("unexpected part: %#v", msg.Parts[0])
	}
}
