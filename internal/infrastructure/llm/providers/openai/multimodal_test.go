package openai

import (
	"testing"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
)

func TestConvertMessagesUsesImageURLParts(t *testing.T) {
	p := NewOpenAIProviderWithOptions("key", "model", "http://example.test", 0)
	got := p.convertMessages(llm.GenerateRequest{Messages: []llm.Message{{
		Role:    "user",
		Content: "画像を見て",
		Parts: []llm.MessagePart{
			{Type: llm.MessagePartText, Text: "画像を見て"},
			{Type: llm.MessagePartImage, MimeType: "image/png", Data: []byte("png")},
		},
	}}})

	content, ok := got[0]["content"].([]map[string]interface{})
	if !ok {
		t.Fatalf("content type = %T, want multipart array", got[0]["content"])
	}
	if content[0]["type"] != "text" || content[1]["type"] != "image_url" {
		t.Fatalf("unexpected content parts: %#v", content)
	}
	imageURL := content[1]["image_url"].(map[string]interface{})["url"].(string)
	if imageURL != "data:image/png;base64,cG5n" {
		t.Fatalf("image url = %q", imageURL)
	}
}

func TestConvertMessagesUsesVideoURLParts(t *testing.T) {
	p := NewOpenAIProviderWithOptions("key", "model", "http://example.test", 0)
	got := p.convertMessages(llm.GenerateRequest{Messages: []llm.Message{{
		Role:    "user",
		Content: "動画を見て",
		Parts: []llm.MessagePart{
			{Type: llm.MessagePartText, Text: "動画を見て"},
			{Type: llm.MessagePartVideo, MimeType: "video/mp4", Data: []byte("mp4")},
		},
	}}})

	content, ok := got[0]["content"].([]map[string]interface{})
	if !ok {
		t.Fatalf("content type = %T, want multipart array", got[0]["content"])
	}
	if content[0]["type"] != "text" || content[1]["type"] != "video_url" {
		t.Fatalf("unexpected content parts: %#v", content)
	}
	videoURL := content[1]["video_url"].(map[string]interface{})["url"].(string)
	if videoURL != "data:video/mp4;base64,bXA0" {
		t.Fatalf("video url = %q", videoURL)
	}
}

func TestConvertMessagesUsesInputAudioParts(t *testing.T) {
	p := NewOpenAIProviderWithOptions("key", "model", "http://example.test", 0)
	got := p.convertMessages(llm.GenerateRequest{Messages: []llm.Message{{
		Role:    "user",
		Content: "音声を聞いて",
		Parts: []llm.MessagePart{
			{Type: llm.MessagePartText, Text: "音声を聞いて"},
			{Type: llm.MessagePartAudio, MimeType: "audio/wav", Data: []byte("wav")},
		},
	}}})

	content, ok := got[0]["content"].([]map[string]interface{})
	if !ok {
		t.Fatalf("content type = %T, want multipart array", got[0]["content"])
	}
	if content[0]["type"] != "text" || content[1]["type"] != "input_audio" {
		t.Fatalf("unexpected content parts: %#v", content)
	}
	inputAudio := content[1]["input_audio"].(map[string]interface{})
	if inputAudio["data"] != "d2F2" || inputAudio["format"] != "wav" {
		t.Fatalf("input_audio = %#v", inputAudio)
	}
}
