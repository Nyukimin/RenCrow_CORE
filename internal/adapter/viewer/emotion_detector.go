package viewer

import (
	"strings"
)

// EmotionType represents character emotions
type EmotionType string

const (
	EmotionNormal   EmotionType = "normal"
	EmotionHappy    EmotionType = "happy"
	EmotionSad      EmotionType = "sad"
	EmotionAngry    EmotionType = "angry"
	EmotionSurprise EmotionType = "surprise"
	EmotionThink    EmotionType = "think"
	EmotionSpeaking EmotionType = "speaking"
)

// EmotionKeywords maps keywords to emotions
var emotionKeywords = map[EmotionType][]string{
	EmotionHappy: {
		"嬉しい", "楽しい", "良かった", "ありがとう", "感謝", "素晴らしい", "最高",
		"happy", "glad", "thank", "great", "wonderful", "excellent",
		"😊", "😄", "😁", "🎉", "✨",
	},
	EmotionSad: {
		"悲しい", "残念", "申し訳", "すみません", "ごめん", "辛い",
		"sad", "sorry", "apologize", "unfortunately",
		"😢", "😭", "💔",
	},
	EmotionAngry: {
		"怒", "腹", "許せない", "ダメ", "イライラ",
		"angry", "mad", "annoyed", "frustrated",
		"😠", "😡", "💢",
	},
	EmotionSurprise: {
		"驚", "え？", "まさか", "本当？", "すごい",
		"surprise", "wow", "amazing", "really",
		"😲", "😮", "‼️", "⁉️",
	},
	EmotionThink: {
		"考え", "検討", "確認", "調べ", "分析", "どうしよう", "うーん",
		"think", "consider", "check", "analyze", "hmm",
		"🤔", "💭",
	},
}

// DetectEmotion detects emotion from text content
func DetectEmotion(text string) EmotionType {
	text = strings.ToLower(text)

	// Check for keywords in priority order
	priorities := []EmotionType{
		EmotionAngry,    // Strongest emotion
		EmotionSurprise, // Strong reaction
		EmotionSad,      // Negative emotion
		EmotionHappy,    // Positive emotion
		EmotionThink,    // Thoughtful
	}

	for _, emotion := range priorities {
		keywords := emotionKeywords[emotion]
		for _, keyword := range keywords {
			if strings.Contains(text, strings.ToLower(keyword)) {
				return emotion
			}
		}
	}

	// Check for question marks (thinking)
	if strings.Contains(text, "？") || strings.Contains(text, "?") {
		return EmotionThink
	}

	// Default to normal
	return EmotionNormal
}

// ChatResponse represents a chat response with emotion
type ChatResponse struct {
	Message        string      `json:"message"`
	CharacterID    string      `json:"character_id"`
	Emotion        EmotionType `json:"emotion"`
	Live2DURL      string      `json:"live2d_url,omitempty"`
	Live2DEmbedURL string      `json:"live2d_embed_url,omitempty"`
}

// BuildChatResponse creates a chat response with emotion detection
func BuildChatResponse(message, characterID string, mode string) ChatResponse {
	emotion := DetectEmotion(message)

	resp := ChatResponse{
		Message:     message,
		CharacterID: characterID,
		Emotion:     emotion,
	}

	// Add Live2D URLs
	if mode == "live" {
		resp.Live2DURL = "/viewer/live2d/character?character_id=" + characterID + "&mode=live"
		resp.Live2DEmbedURL = "/viewer/live2d/embed?character_id=" + characterID + "&emotion=" + string(emotion) + "&mode=live"
	} else {
		resp.Live2DURL = "/viewer/live2d/character?character_id=" + characterID
		resp.Live2DEmbedURL = "/viewer/live2d/embed?character_id=" + characterID + "&emotion=" + string(emotion)
	}

	return resp
}
