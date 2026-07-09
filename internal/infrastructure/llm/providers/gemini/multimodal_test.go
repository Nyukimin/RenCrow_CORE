package gemini

import (
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

func TestConvertMessagesUsesInlineDataParts(t *testing.T) {
	got := convertMessages([]llm.Message{{
		Role:    "user",
		Content: "画像を見て",
		Parts: []llm.MessagePart{
			{Type: llm.MessagePartText, Text: "画像を見て"},
			{Type: llm.MessagePartImage, MimeType: "image/png", Data: []byte("png")},
		},
	}})

	parts := got[0].Parts
	if parts[0].Text != "画像を見て" {
		t.Fatalf("text part = %q", parts[0].Text)
	}
	if parts[1].InlineData == nil || parts[1].InlineData.MimeType != "image/png" || parts[1].InlineData.Data != "cG5n" {
		t.Fatalf("unexpected inline data: %#v", parts[1].InlineData)
	}
}

func TestConvertMessagesUsesVideoInlineDataParts(t *testing.T) {
	got := convertMessages([]llm.Message{{
		Role:    "user",
		Content: "動画を見て",
		Parts: []llm.MessagePart{
			{Type: llm.MessagePartText, Text: "動画を見て"},
			{Type: llm.MessagePartVideo, MimeType: "video/mp4", Data: []byte("mp4")},
		},
	}})

	parts := got[0].Parts
	if parts[0].Text != "動画を見て" {
		t.Fatalf("text part = %q", parts[0].Text)
	}
	if parts[1].InlineData == nil || parts[1].InlineData.MimeType != "video/mp4" || parts[1].InlineData.Data != "bXA0" {
		t.Fatalf("unexpected inline video data: %#v", parts[1].InlineData)
	}
}
