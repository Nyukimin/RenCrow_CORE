package claude

import (
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

func TestConvertMessagesUsesClaudeImageParts(t *testing.T) {
	p := NewClaudeProvider("key", "model")
	got := p.convertMessages([]llm.Message{{
		Role:    "user",
		Content: "画像を見て",
		Parts: []llm.MessagePart{
			{Type: llm.MessagePartText, Text: "画像を見て"},
			{Type: llm.MessagePartImage, MimeType: "image/png", Data: []byte("png")},
		},
	}})

	content := got[0]["content"].([]map[string]interface{})
	if content[0]["type"] != "text" || content[1]["type"] != "image" {
		t.Fatalf("unexpected content parts: %#v", content)
	}
	source := content[1]["source"].(map[string]interface{})
	if source["media_type"] != "image/png" || source["data"] != "cG5n" {
		t.Fatalf("unexpected source: %#v", source)
	}
}
