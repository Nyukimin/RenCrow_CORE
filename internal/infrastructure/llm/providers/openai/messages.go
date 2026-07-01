package openai

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
)

// convertChatMessages はChatMessageをOpenAI APIフォーマットに変換
func (p *OpenAIProvider) convertChatMessages(msgs []llm.ChatMessage) []map[string]interface{} {
	systemParts := make([]string, 0, 2)
	messages := make([]map[string]interface{}, 0, len(msgs))
	for _, m := range msgs {
		if strings.EqualFold(strings.TrimSpace(m.Role), "system") && strings.TrimSpace(m.Content) != "" && len(m.ToolCalls) == 0 && strings.TrimSpace(m.ToolCallID) == "" {
			systemParts = append(systemParts, strings.TrimSpace(m.Content))
			continue
		}
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
	if len(systemParts) > 0 {
		messages = append([]map[string]interface{}{
			{
				"role":    "system",
				"content": strings.Join(systemParts, "\n\n"),
			},
		}, messages...)
	}
	return messages
}

// convertMessages はドメインメッセージをOpenAI APIフォーマットに変換
func (p *OpenAIProvider) convertMessages(req llm.GenerateRequest) []map[string]interface{} {
	messages := make([]map[string]interface{}, 0)
	systemParts := make([]string, 0, 2)

	// システムプロンプトを最初に追加
	if req.SystemPrompt != "" {
		systemParts = append(systemParts, req.SystemPrompt)
	}

	// ユーザーメッセージを追加
	for _, msg := range req.Messages {
		if msg.Role == "system" && len(msg.Parts) == 0 {
			if strings.TrimSpace(msg.Content) != "" {
				systemParts = append(systemParts, msg.Content)
			}
			continue
		}
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
		messages = append(messages, map[string]interface{}{
			"role":    msg.Role,
			"content": content,
		})
	}

	if len(systemParts) > 0 {
		systemMessage := map[string]interface{}{
			"role":    "system",
			"content": strings.Join(systemParts, "\n\n"),
		}
		messages = append([]map[string]interface{}{systemMessage}, messages...)
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
