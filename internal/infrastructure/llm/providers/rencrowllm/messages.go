package rencrowllm

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

// convertChatMessages はChatMessageをRenCrow_LLM Gateway APIフォーマットに変換
func (p *GatewayProvider) convertChatMessages(msgs []llm.ChatMessage) []map[string]interface{} {
	messages := make([]map[string]interface{}, 0, len(msgs))
	for _, m := range msgs {
		msg := map[string]interface{}{
			"role": m.Role,
		}
		if m.Content != "" {
			msg["content"] = m.Content
		}
		if len(m.ToolCalls) > 0 {
			tcs := make([]map[string]interface{}, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				argsJSON, _ := json.Marshal(tc.Function.Arguments)
				tcs = append(tcs, map[string]interface{}{
					"id":   tc.ID,
					"type": "function",
					"function": map[string]interface{}{
						"name":      tc.Function.Name,
						"arguments": string(argsJSON),
					},
				})
			}
			msg["tool_calls"] = tcs
		}
		if m.ToolCallID != "" {
			msg["tool_call_id"] = m.ToolCallID
		}
		messages = append(messages, msg)
	}
	return messages
}

// convertMessages はドメインメッセージをRenCrow_LLM Gateway APIフォーマットに変換
func (p *GatewayProvider) convertMessages(req llm.GenerateRequest) []map[string]interface{} {
	messages := make([]map[string]interface{}, 0)

	// システムプロンプトを最初に追加
	if req.SystemPrompt != "" {
		messages = append(messages, map[string]interface{}{"role": "system", "content": req.SystemPrompt})
	}

	// ユーザーメッセージを追加
	for _, msg := range req.Messages {
		content := any(msg.Content)
		if len(msg.Parts) > 0 {
			parts := make([]map[string]interface{}, 0, len(msg.Parts))
			for _, part := range msg.Parts {
				switch part.Type {
				case llm.MessagePartImage:
					if len(part.Data) == 0 || part.MimeType == "" {
						continue
					}
					parts = append(parts, map[string]interface{}{
						"type": "image_url",
						"image_url": map[string]interface{}{
							"url": "data:" + part.MimeType + ";base64," + base64.StdEncoding.EncodeToString(part.Data),
						},
					})
				case llm.MessagePartAudio:
					if len(part.Data) == 0 {
						continue
					}
					format := audioFormatFromMimeType(part.MimeType)
					parts = append(parts, map[string]interface{}{
						"type": "input_audio",
						"input_audio": map[string]interface{}{
							"data":   base64.StdEncoding.EncodeToString(part.Data),
							"format": format,
						},
					})
				case llm.MessagePartVideo:
					if len(part.Data) == 0 || part.MimeType == "" {
						continue
					}
					parts = append(parts, map[string]interface{}{
						"type": "video_url",
						"video_url": map[string]interface{}{
							"url": "data:" + part.MimeType + ";base64," + base64.StdEncoding.EncodeToString(part.Data),
						},
					})
				default:
					text := part.Text
					if text == "" {
						text = msg.Content
					}
					if text != "" {
						parts = append(parts, map[string]interface{}{"type": "text", "text": text})
					}
				}
			}
			if len(parts) > 0 {
				content = parts
			}
		}
		converted := map[string]interface{}{
			"role":    msg.Role,
			"content": content,
		}
		messages = append(messages, converted)
	}

	return messages
}

func audioFormatFromMimeType(mimeType string) string {
	ct := strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0]))
	switch ct {
	case "audio/mpeg", "audio/mp3":
		return "mp3"
	default:
		return "wav"
	}
}
