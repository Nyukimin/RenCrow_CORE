package tts

import (
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

type SBV2VoiceParams struct {
	Name      string
	ModelID   int
	SpeakerID int
	Style     string
}

type SBV2G2PRequestPayload struct {
	Text string `json:"text"`
}

type SBV2EditorSynthesisPayloadInput struct {
	Model        string
	ModelFile    string
	Text         string
	MoraToneList []map[string]any
	Speaker      string
}

type SBV2EditorSynthesisPayload struct {
	Model        string           `json:"model"`
	ModelFile    string           `json:"modelFile"`
	Text         string           `json:"text"`
	MoraToneList []map[string]any `json:"moraToneList"`
	Speaker      string           `json:"speaker"`
}

func ResolveSBV2VoiceParams(name string) SBV2VoiceParams {
	normalized := strings.ToLower(strings.TrimSpace(name))
	switch normalized {
	case "shi-gozaki", "shigozaki", "shin-gozaki", "shingozaki", "shiro", "male_01", "male":
		return SBV2VoiceParams{Name: "shi-gozaki", ModelID: 6, SpeakerID: 0, Style: "Neutral"}
	case "amitaro", "mio", "female_01", "female", "":
		return SBV2VoiceParams{Name: "amitaro", ModelID: 0, SpeakerID: 0, Style: "Neutral"}
	default:
		return SBV2VoiceParams{Name: strings.TrimSpace(name), ModelID: 0, SpeakerID: 0, Style: "Neutral"}
	}
}

func BuildSBV2G2PRequestPayload(text string) SBV2G2PRequestPayload {
	return SBV2G2PRequestPayload{Text: EnsureTTSPunctuation(text)}
}

func BuildSBV2EditorSynthesisPayload(input SBV2EditorSynthesisPayloadInput) SBV2EditorSynthesisPayload {
	return SBV2EditorSynthesisPayload{
		Model:        strings.TrimSpace(input.Model),
		ModelFile:    strings.TrimSpace(input.ModelFile),
		Text:         EnsureTTSPunctuation(input.Text),
		MoraToneList: append([]map[string]any(nil), input.MoraToneList...),
		Speaker:      strings.TrimSpace(input.Speaker),
	}
}

func SBV2VoiceURL(baseURL, text string, voice SBV2VoiceParams) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if !strings.HasSuffix(strings.ToLower(base), "/voice") {
		base += "/voice"
	}
	q := make(url.Values, 4)
	q.Set("text", EnsureTTSPunctuation(text))
	q.Set("model_id", strconv.Itoa(voice.ModelID))
	q.Set("speaker_id", strconv.Itoa(voice.SpeakerID))
	q.Set("style", voice.Style)
	return base + "?" + q.Encode()
}

func SBV2EditorURL(baseURL, endpoint string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	switch endpoint {
	case "models_info":
		return strings.TrimSuffix(base, "/synthesis") + "/models_info"
	case "g2p":
		return strings.TrimSuffix(base, "/synthesis") + "/g2p"
	default:
		return base
	}
}

func EnsureTTSPunctuation(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	last, _ := utf8.DecodeLastRuneInString(text)
	switch last {
	case '。', '！', '？', '!', '?', '.', '…', '♪', '、', ',', '」', '』', ')', '）':
		return text
	default:
		return text + "。"
	}
}
