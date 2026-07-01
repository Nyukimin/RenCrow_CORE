package llm

import "testing"

func TestCloneGenerateRequestCopiesMutableFields(t *testing.T) {
	original := GenerateRequest{
		Messages: []Message{
			{
				Role: "user",
				Parts: []MessagePart{
					{Type: MessagePartImage, MimeType: "image/png", Data: []byte("png")},
				},
			},
		},
		ProviderOptions: map[string]any{"temperature": 0.2},
	}

	got := CloneGenerateRequest(original)
	got.Messages[0].Parts[0].Data[0] = 'x'
	got.ProviderOptions["temperature"] = 0.9

	if string(original.Messages[0].Parts[0].Data) != "png" {
		t.Fatalf("message part data was aliased: %q", string(original.Messages[0].Parts[0].Data))
	}
	if original.ProviderOptions["temperature"] != 0.2 {
		t.Fatalf("provider options were aliased: %+v", original.ProviderOptions)
	}
}
